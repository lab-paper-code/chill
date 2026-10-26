#!/usr/bin/env python3
"""Local-only token/start/run and PASS/FAIL evidence regression check."""
import base64
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
from types import SimpleNamespace
from unittest.mock import patch

HERE = Path(__file__).resolve().parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def main():
    auth = module('smoke_auth', HERE / 'auth.py')
    auth.self_check()
    demo = module('smoke_demo', HERE / 'demo.py')
    with tempfile.TemporaryDirectory(prefix='chill-preflight-') as directory:
        root = Path(directory)
        raw = module('smoke_preflight_raw', HERE / 'e5-raw.py')
        assert raw._root(root / 'stage', root) == root / 'stage'
        try:
            raw._root(root.parent, root)
        except ValueError:
            pass
        else:
            raise AssertionError('Offline raw reader escaped the retained capture')
        environment = root / 'environment.json'
        environment.write_text(json.dumps({'paths': {'repository': str(HERE.parents[1]), 'ops': directory, 'campaign': directory, 'd2State': directory}, 'e5': {'operationalRecipe': directory, 'nodes': [], 'excludedNodes': []}}))
        with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(environment)):
            e5 = module('smoke_preflight_e5', HERE / 'e5.py')
        stats = module('smoke_issuance_stats', HERE / 'd2' / 'analyze_before.py').stats
        planned = [0, 10_000_000, 20_000_000]
        # Completion order differs from send order; stdout order is irrelevant.
        client = [{'requestIndex': planned.index(offset), 'scheduledOffsetNs': offset, 'sentMonotonicNS': 100+sent}
                  for offset, sent in [(20_000_000, 90_000_000), (0, 0), (10_000_000, 10_000_000)]]
        fidelity = e5.issuance_fidelity(client, planned, 100, 100_000_000, stats)
        assert fidelity['gapsOver50MS'] == 1 and fidelity['sendsLateOver50MS'] == 1
        assert fidelity['actualGapNS']['max'] == 80_000_000
        assert fidelity['gapIntervalsOver50MS'][0]['plannedArrivalsInside'] == 1
        # Intentional sparse Poisson gaps are visible but not late sends.
        sparse = e5.issuance_fidelity([{'requestIndex': 0, 'scheduledOffsetNs': 80_000_000, 'sentMonotonicNS': 80_000_100}], [80_000_000], 100, 100_000_000, stats)
        assert sparse['gapsOver50MS'] == 1 and sparse['sendsLateOver50MS'] == 0
        assert e5.issuance_fidelity([{'requestIndex': 0, 'scheduledOffsetNs': 0}], [0], 100, 100_000_000, stats)['status'] == 'UNKNOWN'
        assert e5.issuance_fidelity([], [], None, 100_000_000, stats)['status'] == 'NOT_OFFERED'
        assert e5.issuance_fidelity([], [0], 100, 100_000_000, stats)['status'] == 'UNKNOWN'
        assert e5.issuance_fidelity([client[0]]*3, planned, 100, 100_000_000, stats)['status'] == 'UNKNOWN'
        unsent = e5.issuance_fidelity([{'requestIndex': 0, 'scheduledOffsetNs': 0, 'error': 'UnsentAtWindowEnd'}], [0], 100, 100_000_000, stats)
        assert unsent['issued'] == 0 and unsent['gapsOver50MS'] == 1
        spec = e5.load(HERE / 'preflight-config.example.json')
        schedule = demo.schedule(spec, e5)
        assert schedule[0]['readyGate'] and schedule[0]['seconds'] == 720 and schedule[1]['seconds'] == 60
        assert schedule[0]['startBarrier'] == {'reservations': 0, 'transitions': 0}
        assert 'startBarrier' not in schedule[1]
        assert spec['timeboxes']['collectSeconds'] == 240
        assert auth.lifetime_budget(schedule, spec['timeboxes'], spec['tokenSafetyMarginSeconds']) == 1620
        token = root / 'token'
        def jwt(seconds):
            return 'e30.' + base64.urlsafe_b64encode(json.dumps({'exp': time.time() + seconds}).encode()).decode().rstrip('=') + '.signature'
        token.write_text(jwt(1))
        workloads = [{'name': name, 'uid': name} for name in demo.ALIASES.values()]
        config = {'tokenFile': str(token), 'timeboxes': spec['timeboxes'], 'tokenSafetyMarginSeconds': 30, 'workloads': workloads, 'smoke': schedule[0]}
        binding = {'schema': 'demo-e5-binding.v1', 'schedule': schedule, 'config': config, 'demoConfig': spec}
        out = root / 'run'
        out.mkdir()
        cpo = object.__new__(e5.CPO)
        cpo.b, cpo.c, cpo.out, cpo.tb = binding, config, out, config['timeboxes']
        cpo.a = SimpleNamespace(command='smoke', mode='smoke', stage='E0')
        for command in (cpo.start, cpo.run):
            try:
                command()
            except ValueError as error:
                assert 'remaining' in str(error) and 'required' in str(error)
            else:
                raise AssertionError('Expired token passed start/run')
        assert not (out / 'initial.json').exists() and not (out / 'smoke-E0').exists()
        token.write_text(jwt(3600))
        record = cpo.check_token_lifetime()
        assert record['requiredSeconds'] == 1620 and record['expiresUTC']
        assert cpo.check_token_lifetime(schedule[1])['requiredSeconds'] == 495
        cpo.b = dict(binding, schema='e5-binding.v1')
        cpo.a.command = 'campaign'
        cpo.c = dict(config, factors=[{'name': 'one'}])
        assert cpo.check_token_lifetime()['requiredSeconds'] == auth.lifetime_budget(schedule + schedule, spec['timeboxes'], 30)
        cpo.b, cpo.c, cpo.a.command = binding, config, 'smoke'
        initial = {'instances': None, 'reservations': None}
        e5.save(out / 'initial.json', {'state': {'snapshot': initial}})
        e5.save(out / 'finish-complete.json', {})
        e5.save(out / 'finish-definitions-complete.json', {})
        occupied = {'snapshot': {'instances': [], 'reservations': [{'owner': 'reset'}]}}
        empty = {'snapshot': copy.deepcopy(initial)}
        with patch.object(cpo, 'state', side_effect=[occupied, empty], create=True), patch.object(e5.time, 'sleep'):
            demo.wait_preflight_empty(e5, cpo)
        for stage in schedule:
            folder = demo.stage_folder(out, stage)
            folder.mkdir()
            for name in ('power.json', 'power-drain-inclusive.json'):
                e5.save(folder / name, {'complete': True})
        stages = [
            {'stage': 'E0', 'status': 'DRAINED_NOT_EVALUATED', 'readyGate': {'observedUTC': e5.now(), 'missingWorkloadUIDs': [], 'timedOut': False}, 'workloads': {}},
            {'stage': 'MIX', 'status': 'DRAINED_NOT_EVALUATED', 'workloads': {w['name']: {'offered': 10, 'httpSuccess': 10, 'clientRows': 10} for w in workloads}},
        ]
        assert demo.preflight_result(e5, binding, out, stages)['status'] == 'PASS'
        # The public JSON contract omits reservations when the list is empty.
        omitted = {'snapshot': {'instances': None}, 'actions': [{'status': 'complete'}]}
        e5.save(out / 'initial.json', {'state': omitted})
        with patch.object(cpo, 'state', return_value=omitted, create=True):
            demo.wait_preflight_empty(e5, cpo)
        assert demo.preflight_result(e5, binding, out, stages)['status'] == 'PASS'
        assert e5.load(out / 'preflight-final-state.json') == omitted
        def fails(check, rows=stages):
            result = demo.preflight_result(e5, binding, out, rows)
            assert result['status'] == 'FAIL' and result['checks'][check] == 'FAIL', result
        broken = copy.deepcopy(stages)
        broken[1]['workloads'][workloads[0]['name']]['httpSuccess'] = 9
        fails('success100Percent', broken)
        broken = copy.deepcopy(stages)
        broken[0]['readyGate']['timedOut'] = True
        fails('ready3', broken)
        fails('stagesComplete', stages[:1])
        power = demo.stage_folder(out, schedule[1]) / 'power.json'
        power.unlink()
        fails('powerComplete')
        e5.save(power, {'complete': True})
        e5.save(out / 'preflight-final-state.json', occupied)
        fails('finalReservations0')
        with patch.object(cpo, 'tb', dict(settleSeconds=0)), patch.object(cpo, 'state', return_value=occupied, create=True):
            try:
                demo.wait_preflight_empty(e5, cpo)
            except RuntimeError:
                pass
            else:
                raise AssertionError('Final reservations did not stop preflight')
        e5.save(out / 'preflight-final-state.json', empty)
        e5.save(out / 'preflight-STOP.json', {})
        fails('noStop')
        e5.save(out / 'preflight-STOP.json', {'error': "'reservations'", 'status': 'FAIL'})
        fails('noStop')
        judged = demo.preflight_result(e5, binding, out, stages, judge=True)
        assert judged['status'] == 'PASS' and judged['resolvedHistoricalStop']['error'] == "'reservations'"
        e5.save(out / 'preflight-final-state.json', occupied)
        assert demo.preflight_result(e5, binding, out, stages, judge=True)['status'] == 'FAIL'
        e5.save(out / 'preflight-final-state.json', dict(omitted, actions=[{'status': 'cancel'}]))
        fails('finalTransitions0')
        assert demo.preflight_result(e5, binding, out, stages, judge=True)['status'] == 'FAIL'
        e5.save(out / 'preflight-final-state.json', omitted)
        e5.save(out / 'preflight-STOP.json', {'error': 'another failure'})
        assert demo.preflight_result(e5, binding, out, stages, judge=True)['status'] == 'FAIL'
        (out / 'preflight-STOP.json').unlink()
        (out / 'preflight-final-state.json').unlink()
        fails('finalInstances0')
    print('PASS preflight: JWT/opaque expiry, start/run refusal, 720s Ready/1620s budget, Ready/success/power/cleanup PASS/FAIL; local only')


if __name__ == '__main__':
    main()
