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
        raw = module('smoke_preflight_raw', HERE / 'raw.py')
        stamp = '2026-10-27T00:00:00.000000001Z'
        raw_rows = [
            {'workloadUID': 'w', 'requestID': 'assigned-only', 'kind': 'assigned', 'receivedAt': stamp},
            {'workloadUID': 'w', 'requestID': 'conflict', 'kind': 'assigned', 'receivedAt': stamp},
            {'workloadUID': 'w', 'requestID': 'conflict', 'kind': 'assigned', 'receivedAt': '2026-10-27T00:00:00.000000002Z'},
        ]
        with patch.object(raw, '_root', return_value=root), \
             patch.object(raw, '_manifest', return_value=({}, [], [])), \
             patch.object(raw, '_population'), \
             patch.object(raw, '_rows', return_value=[(None, row, {'line': i}) for i, row in enumerate(raw_rows, 1)]):
            indexed = raw.iter_request_indexes(root, ['w'], '2026-10-27T00:00:00Z', '2026-10-27T00:00:01Z')['w']
        assert indexed['receivedAtNS'] == {'assigned-only': raw._time(stamp)}
        assert not indexed['finals'] and indexed['summary']['finalConflicts']
        with patch.dict(os.environ, {}, clear=True), \
             patch.object(raw, '_manifest', return_value=({}, [], [])), \
             patch.object(raw, '_population'), \
             patch.object(raw, '_rows', return_value=[(None, row, {'line': i}) for i, row in enumerate(raw_rows, 1)]):
            assert raw.iter_request_indexes(root, ['w'], '2026-10-27T00:00:00Z', '2026-10-27T00:00:01Z', approved_root=root)['w']['receivedAtNS'] == indexed['receivedAtNS']
            assert raw.index_requests(root, 'w', '2026-10-27T00:00:00Z', '2026-10-27T00:00:01Z', approved_root=root)['receivedAtNS'] == indexed['receivedAtNS']
        assert raw._root(root / 'stage', root) == root / 'stage'
        try:
            raw._root(root.parent, root)
        except ValueError:
            pass
        else:
            raise AssertionError('Offline raw reader escaped the retained capture')
        environment = root / 'environment.json'
        environment.write_text(json.dumps({'paths': {'repository': str(HERE.parents[1]), 'ops': directory, 'campaign': directory, 'd2State': directory}, 'evaluation': {'operationalRecipe': directory, 'nodes': [], 'excludedNodes': []}}))
        with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(environment)):
            harness_module = module('smoke_preflight_harness', HERE / 'conductor.py')
        harness_module.self_check_g0()
        assert harness_module.g0_evidence_gate({'g0Evidence': {'status': 'UNKNOWN'}}, 1, required=False)['status'] == 'UNKNOWN'
        g0_run = root / 'synthetic-g0'
        (g0_run / 'run').mkdir(parents=True)
        rates = dict(zip(harness_module.WORKLOADS, [.54, .12, .54]))
        slo = {'percentile': 95, 'targetLatencyNanos': 32_000_000}
        witness = {'schema': 'g0-verdict.v2', 'status': 'FAIL', 'generatorQualityStatus': 'VALID', 'configuredRateTotal': 1.2,
                   'server': {'missingSamples': 0, 'restarts': 0, 'oomKills': 0, 'memoryLimitBytes': 100},
                   'gateway': dict.fromkeys(('rejected', 'unavailable', 'timeouts', 'incomplete'), 0),
                   'memoryBytesPeak': 50, 'cpuCoresPeak': 100,
                   'cpu10sSamples': [{'bin': i, 'samples': 10, 'podUID': 'pod', 'containerID': 'container',
                                     'cpuCores': 100, 'memoryBytesMax': 50,
                                     'nrThrottledDelta': 0, 'throttledNanosDelta': 0} for i in range(12)],
                   'workloads': {name: {'rate': rate, 'planned': 10, 'issued': 10, 'supplied': True, 'quality': {'status': 'VALID'}} for name, rate in rates.items()}}
        witness['workloads']['plc-anomaly'].update(receiptLatencyMS={'p95': 20}, withinSLOFraction=1)
        witness['workloads']['smolvlm2-vqa'].update(SLOVerdict='FAIL', successFraction=.4)
        frozen = {'schema': 'demo-e5-binding.v2', 'demoConfig': {'measurementSLO': {'plc': slo}},
                  'schedule': [{'id': 'G-check', 'seconds': 120, 'traces': {name: {'offered': 10} for name in rates},
                                'parts': [{'alpha': dict.fromkeys(['mobilenet', 'plc', 'smolvlm2'], 1.2), 'rates': rates}]}]}
        frozen['config'] = {'workloads': [{'name': 'plc-anomaly', 'uid': 'synthetic-plc'}]}
        folder = g0_run / 'run/smoke-G-check'
        harness_module.save(folder / 'phase.json', {'startUTC': '2026-10-27T00:00:00Z', 'endUTC': '2026-10-27T00:02:00Z'})
        (folder / 'plc-anomaly-client.jsonl').write_text(''.join(json.dumps({'requestID': str(i)})+'\n' for i in range(10)))
        harness_module.save(g0_run / 'g0-verdict.json', witness)
        harness_module.save(g0_run / 'run/binding.json', frozen)
        record = {'runID': 'synthetic-g0', 'verdict': str(g0_run / 'g0-verdict.json'),
                  'binding': str(g0_run / 'run/binding.json')}
        approved = {'g0Evidence': record, 'workloads': [{'name': name, 'R': 1, 'slo': slo} for name in rates]}
        # Synthetic raw-index adapter only; actual percentile and G0 gate are exercised.
        original_module = harness_module.module
        indexed = {'errors': [], 'finals': {str(i): {'outcome': 'completed', 'executionComplete': True,
                   'elapsedNanos': 20_000_000} for i in range(10)}}
        def g0_modules(name, path):
            if name == 'g0_raw':
                return SimpleNamespace(index_requests=lambda *args: copy.deepcopy(indexed))
            return original_module(name, path)
        with patch.object(harness_module, 'module', side_effect=g0_modules):
            gate = harness_module.g0_evidence_gate(approved, 1)
            assert gate['status'] == 'VALID' and gate['cpuCoresPeak'] == 100 and 'csCPUFraction' not in gate
            assert gate['gatewayVerdict'] == 'PASS' and gate['historicalOverallStatusReference'] == 'FAIL'
            assert gate['smolSLOReference']['SLOVerdict'] == 'FAIL'
            assert gate['sources']['verdict'] == harness_module.ref(record['verdict'])
            for invalid in ({}, {'g0Evidence': {'status': 'UNKNOWN'}}, dict(approved, g0Evidence=dict(record, runID='wrong-run')),
                            dict(approved, g0Evidence=dict(record, csCPULimitCores=0))):
                try:
                    harness_module.g0_evidence_gate(invalid, 1)
                except ValueError:
                    pass
                else:
                    raise AssertionError('Invalid frozen G0 evidence accepted')
        issuance = module('smoke_issuance_stats', HERE / 'd2' / 'analyze_before.py')
        stats = issuance.stats
        planned = [0, 10_000_000, 20_000_000]
        # Completion order differs from send order; stdout order is irrelevant.
        client = [{'requestIndex': planned.index(offset), 'scheduledOffsetNs': offset, 'sentMonotonicNS': 100+sent}
                  for offset, sent in [(20_000_000, 90_000_000), (0, 0), (10_000_000, 10_000_000)]]
        fidelity = issuance.issuance_fidelity(client, planned, 100, 100_000_000, stats)
        assert fidelity['gapsOver50MS'] == 1 and fidelity['sendsLateOver50MS'] == 1
        assert fidelity['actualGapNS']['max'] == 80_000_000
        assert fidelity['gapIntervalsOver50MS'][0]['plannedArrivalsInside'] == 1
        # Intentional sparse Poisson gaps are visible but not late sends.
        sparse = issuance.issuance_fidelity([{'requestIndex': 0, 'scheduledOffsetNs': 80_000_000, 'sentMonotonicNS': 80_000_100}], [80_000_000], 100, 100_000_000, stats)
        assert sparse['gapsOver50MS'] == 1 and sparse['sendsLateOver50MS'] == 0
        assert issuance.issuance_fidelity([{'requestIndex': 0, 'scheduledOffsetNs': 0}], [0], 100, 100_000_000, stats)['status'] == 'UNKNOWN'
        assert issuance.issuance_fidelity([], [], None, 100_000_000, stats)['status'] == 'NOT_OFFERED'
        assert issuance.issuance_fidelity([], [0], 100, 100_000_000, stats)['status'] == 'UNKNOWN'
        assert issuance.issuance_fidelity([client[0]]*3, planned, 100, 100_000_000, stats)['status'] == 'UNKNOWN'
        unsent = issuance.issuance_fidelity([{'requestIndex': 0, 'scheduledOffsetNs': 0, 'error': 'UnsentAtWindowEnd'}], [0], 100, 100_000_000, stats)
        assert unsent['issued'] == 0 and unsent['gapsOver50MS'] == 1
        spec = harness_module.load(HERE / 'preflight-config.example.json')
        schedule = demo.schedule(spec, harness_module)
        assert schedule[0]['readyGate'] and schedule[0]['seconds'] == 720 and schedule[1]['seconds'] == 60
        assert schedule[0]['startBarrier'] == {'reservations': 0, 'transitions': 0}
        assert 'startBarrier' not in schedule[1]
        assert spec['timeboxes']['collectSeconds'] == 240
        assert spec['timeboxes']['finishResetWaitSeconds'] == 600
        assert auth.lifetime_budget(schedule, spec['timeboxes'], spec['tokenSafetyMarginSeconds']) == 2820
        token = root / 'token'
        def jwt(seconds):
            return 'e30.' + base64.urlsafe_b64encode(json.dumps({'exp': time.time() + seconds}).encode()).decode().rstrip('=') + '.signature'
        token.write_text(jwt(1))
        workloads = [{'name': name, 'uid': name} for name in demo.ALIASES.values()]
        config = {'tokenFile': str(token), 'timeboxes': spec['timeboxes'], 'tokenSafetyMarginSeconds': 30, 'workloads': workloads, 'smoke': schedule[0]}
        binding = {'schema': 'demo-e5-binding.v2', 'schedule': schedule, 'config': config, 'demoConfig': spec}
        out = root / 'run'
        out.mkdir()
        cpo = object.__new__(harness_module.CPO)
        cpo.b, cpo.c, cpo.out, cpo.tb = binding, config, out, config['timeboxes']
        cpo.a = SimpleNamespace(command='smoke', mode='smoke', stage='E0', row_tag='')
        for command in (cpo.start, cpo.run):
            try:
                command()
            except ValueError as error:
                assert 'remaining' in str(error) and 'required' in str(error)
            else:
                raise AssertionError('Expired token passed start/run')
        assert not (out / 'initial.json').exists() and not (out / 'smoke-E0').exists()
        prepare_spec = dict(spec, operationalRecipe=str(root / 'token-config.json'))
        prepare_spec.pop('evaluationConfig', None)
        harness_module.save(root / 'token-config.json', config)
        harness_module.save(root / 'prepare-config.json', prepare_spec)
        prepare_args = SimpleNamespace(out=root / 'prepare', config=root / 'prepare-config.json')
        with patch.object(demo, 'assemble', side_effect=AssertionError('Expired token reached assembly')):
            try:
                demo.prepare(harness_module, prepare_args)
            except ValueError as error:
                assert 'remaining' in str(error) and 'required' in str(error)
            else:
                raise AssertionError('Expired preflight token was accepted')
        token.write_text(jwt(7200))
        record = cpo.check_token_lifetime()
        assert record['requiredSeconds'] == 2820 and record['expiresUTC']
        assert cpo.check_token_lifetime(schedule[1])['requiredSeconds'] == 1095
        with patch.object(demo, 'assemble', side_effect=RuntimeError('Local assembly boundary reached')) as assembly:
            try:
                demo.prepare(harness_module, prepare_args)
            except RuntimeError as error:
                assert str(error) == 'Local assembly boundary reached'
            else:
                raise AssertionError('Prepare did not reach its local assembly boundary')
            assembly.assert_called_once()  # 2820s is allowed when the real token covers it.
        cpo.b = dict(binding, schema='e5-binding.v2')
        cpo.a.command = 'campaign'
        cpo.c = dict(config, factors=[{'name': 'one'}])
        assert cpo.check_token_lifetime()['requiredSeconds'] == auth.lifetime_budget(schedule + schedule, spec['timeboxes'], 30)
        cpo.b, cpo.c, cpo.a.command = binding, config, 'smoke'
        initial = {'instances': None, 'reservations': None}
        harness_module.save(out / 'initial.json', {'state': {'snapshot': initial}})
        harness_module.save(out / 'finish-complete.json', {})
        occupied = {'snapshot': {'instances': [], 'reservations': [{'owner': 'reset'}]}}
        empty = {'snapshot': copy.deepcopy(initial)}
        with patch.object(cpo, 'state', side_effect=[occupied, empty], create=True), patch.object(harness_module.time, 'sleep'):
            demo.wait_preflight_empty(harness_module, cpo)
        for stage in schedule:
            folder = demo.stage_folder(out, stage)
            folder.mkdir()
            for name in ('power.json', 'power-drain-inclusive.json'):
                harness_module.save(folder / name, {'complete': True})
        stages = [
            {'stage': 'E0', 'status': 'DRAINED_NOT_EVALUATED', 'readyGate': {'observedUTC': harness_module.now(), 'missingWorkloadUIDs': [], 'timedOut': False}, 'workloads': {}},
            {'stage': 'MIX', 'status': 'DRAINED_NOT_EVALUATED', 'workloads': {w['name']: {'offered': 10, 'httpSuccess': 10, 'clientRows': 10} for w in workloads}},
        ]
        assert demo.preflight_result(harness_module, binding, out, stages)['status'] == 'PASS'
        assert not (out / 'finish-definitions-complete.json').exists()
        # The public JSON contract omits reservations when the list is empty.
        omitted = {'snapshot': {'instances': None}, 'actions': [{'status': 'complete'}]}
        harness_module.save(out / 'initial.json', {'state': omitted})
        with patch.object(cpo, 'state', return_value=omitted, create=True):
            demo.wait_preflight_empty(harness_module, cpo)
        assert demo.preflight_result(harness_module, binding, out, stages)['status'] == 'PASS'
        assert harness_module.load(out / 'preflight-final-state.json') == omitted
        def fails(check, rows=stages):
            result = demo.preflight_result(harness_module, binding, out, rows)
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
        harness_module.save(power, {'complete': True})
        harness_module.save(out / 'preflight-final-state.json', occupied)
        fails('finalReservations0')
        with patch.object(cpo, 'tb', dict(settleSeconds=0)), patch.object(cpo, 'state', return_value=occupied, create=True):
            try:
                demo.wait_preflight_empty(harness_module, cpo)
            except RuntimeError:
                pass
            else:
                raise AssertionError('Final reservations did not stop preflight')
        harness_module.save(out / 'preflight-final-state.json', empty)
        harness_module.save(out / 'preflight-STOP.json', {})
        fails('noStop')
        harness_module.save(out / 'preflight-STOP.json', {'error': "'reservations'", 'status': 'FAIL'})
        fails('noStop')
        judged = demo.preflight_result(harness_module, binding, out, stages, judge=True)
        assert judged['status'] == 'PASS' and judged['resolvedHistoricalStop']['error'] == "'reservations'"
        harness_module.save(out / 'preflight-final-state.json', occupied)
        assert demo.preflight_result(harness_module, binding, out, stages, judge=True)['status'] == 'FAIL'
        harness_module.save(out / 'preflight-final-state.json', dict(omitted, actions=[{'status': 'cancel'}]))
        fails('finalTransitions0')
        assert demo.preflight_result(harness_module, binding, out, stages, judge=True)['status'] == 'FAIL'
        harness_module.save(out / 'preflight-final-state.json', omitted)
        harness_module.save(out / 'preflight-STOP.json', {'error': 'another failure'})
        assert demo.preflight_result(harness_module, binding, out, stages, judge=True)['status'] == 'FAIL'
        (out / 'preflight-STOP.json').unlink()
        (out / 'preflight-final-state.json').unlink()
        fails('finalInstances0')
    print('PASS preflight: JWT/opaque expiry, start/run refusal, 720s Ready/2820s budget, Ready/success/power/cleanup PASS/FAIL; local only')


if __name__ == '__main__':
    main()
