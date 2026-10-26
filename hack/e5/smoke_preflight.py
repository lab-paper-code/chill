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
        environment = root / 'environment.json'
        environment.write_text(json.dumps({'paths': {'repository': str(HERE.parents[1]), 'ops': directory, 'campaign': directory, 'd2State': directory}, 'e5': {'operationalRecipe': directory, 'nodes': [], 'excludedNodes': []}}))
        with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(environment)):
            e5 = module('smoke_preflight_e5', HERE / 'e5.py')
        spec = e5.load(HERE / 'preflight-config.example.json')
        schedule = demo.schedule(spec, e5)
        assert schedule[0]['readyGate'] and schedule[0]['seconds'] == 720 and schedule[1]['seconds'] == 60
        assert spec['timeboxes']['collectSeconds'] == 120
        assert auth.lifetime_budget(schedule, spec['timeboxes'], spec['tokenSafetyMarginSeconds']) == 1380
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
        assert record['requiredSeconds'] == 1380 and record['expiresUTC']
        assert cpo.check_token_lifetime(schedule[1])['requiredSeconds'] == 375
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
        (out / 'preflight-STOP.json').unlink()
        (out / 'preflight-final-state.json').unlink()
        fails('finalInstances0')
    print('PASS preflight: JWT/opaque expiry, start/run refusal, 720s Ready/1380s budget, Ready/success/power/cleanup PASS/FAIL; local only')


if __name__ == '__main__':
    main()
