#!/usr/bin/env python3
"""Offline black-box cutover regressions; no credentials, cluster or image builds."""
import base64
import copy
from contextlib import nullcontext
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
import urllib.error
from unittest.mock import patch
import zlib
import time

import archive
import conductor
import evaluation
import evidence_io
import input_snapshot
import live
import judgment as judgment_rules
import publication
import request_analysis
import settings_patch
import raw

sys.path.insert(0, str(conductor.REPO / 'load-generator/src'))
from chill_loadgen import cli

NAME = 'plc-anomaly'
SOURCE = {'metric': 'watts', 'endpoint': 'https://meter.invalid'}
START = '2026-11-03T00:00:00Z'
END = '2026-11-03T00:00:10Z'


# Server truth: settings GET projections that PUT rejects (httpapi/scheduler.go).
SERVER_READ_ONLY = {'staticRouting', 'staticRoutingValidatedAt', 'powerObservation', 'meters', 'requestTimeout'}


class SettingsAPI:
    def __init__(self):
        self.value = {'controls': {'power': True, 'routing': True, 'replicas': True}, 'controlIntervalNanos': 30,
                      'minimumDemand': {'other': 7, 'remove': 9}, 'autonomousWorkloads': ['one'],
                      'meters': ['read-only'], 'requestTimeout': 999, 'staticRouting': {}, 'powerObservation': {},
                      'staticRoutingValidatedAt': '2026-11-03T00:00:00Z'}
        self.calls, self.reject_codes = [], []
        self.fail_put = self.drift = self.transport_error = self.omit_get_etag = False
        self.revision = 1
        self.on_conflict = lambda: None
        self.normalize = lambda value: value

    def __call__(self, method, path, body=None, *, headers=None, response=None):
        assert path == '/api/v1/scheduler/settings'
        self.calls.append((method, path, copy.deepcopy(body), copy.deepcopy(headers)))
        metadata = response if response is not None else {}
        etag = '"' + str(self.revision) + '"'
        if method == 'GET':
            metadata.update(statusCode=200, etag=None if self.omit_get_etag else etag, body=copy.deepcopy(self.value))
            return copy.deepcopy(self.value)
        if self.transport_error:
            raise OSError('Connection lost after PUT')
        code = 428 if not headers or not headers.get('If-Match') else 409 if headers['If-Match'] != etag else 403 if self.fail_put else self.reject_codes.pop(0) if self.reject_codes else None
        if code:
            if code == 409:
                self.revision += 1
                self.on_conflict()
            error = {'code': 'SettingsPreconditionRequired' if code == 428 else 'SettingsConflict' if code == 409 else 'Forbidden'}
            metadata.update(statusCode=code, etag='"' + str(self.revision) + '"', body=error)
            raise urllib.error.HTTPError('https://settings.invalid' + path, code, error['code'], {}, None)
        # Mirrors the live server: GET adds these projections, PUT rejects them.
        assert not set(body) & SERVER_READ_ONLY, set(body) & SERVER_READ_ONLY
        before = copy.deepcopy(self.value)
        projection = {key: value for key, value in self.value.items() if key in SERVER_READ_ONLY}
        self.value = self.normalize({**copy.deepcopy(body), **projection})
        self.revision += 1
        result = {'before': before, 'after': copy.deepcopy(self.value), 'appliedAt': START}
        if self.drift:
            self.value['controlIntervalNanos'] += 1
        metadata.update(statusCode=200, etag='"' + str(self.revision) + '"', body=result)
        return result


def fixture(root):
    workload = {'name': NAME, 'uid': 'workload', 'generation': 1, 'slo': {'percentile': 95, 'targetLatencyNanos': 1000000000}}
    scenario = {'spec': {'seeds': {NAME: '202'}, 'datasetRefs': {NAME: {'name': 'records', 'uid': 'dataset'}},
                         'arrivalDistribution': {NAME: 'poisson'}, 'workloads': [{k: workload[k] for k in ('name', 'uid', 'generation')}],
                         'parts': [{'id': 'explicit', 'durationSeconds': 10, 'rates': {NAME: 2.5}}]},
                'workloads': [workload], 'datasets': {NAME: {'metadata': {'uid': 'dataset'}, 'spec': {
                    'source': {'image': 'records@sha256:' + 'a' * 64, 'path': '/records'}, 'fields': [{'name': 'input'}]}}},
                'fields': {NAME: ['input']}, 'traceOffsets': {}}
    spec = {'row': 'e5-test', 'harness': {'sourceSHA': 'a' * 40}, 'loadGenerator': {
        'nodeName': 'genesis1', 'image': 'generator@sha256:' + 'b' * 64, 'sourceSHA': 'b' * 40,
        'shards': {NAME: 3}, 'resources': {'requests': {'memory': '1Gi'}, 'limits': {'memory': '1Gi'}},
        'barrier': {'minimumReadySamples': 2, 'maxStartSkewNanos': 1000000000}},
        'settingsPatch': {'controls': {'power': False}, 'controlIntervalNanos': 31},
        'judgment': {'generatorQuality': {'latenessToleranceNanos': 50000000, 'maxExceedingFraction': .01},
                     'energyCoverage': {'maxGapFraction': .01, 'maxSingleGapSeconds': 30}},
        'powerMeasurement': {'nodes': [{'name': 'idle', 'uid': 'idle-uid'}, {'name': 'serving', 'uid': 'serving-uid'}],
                             'meters': [{'id': 'idle-meter', 'sourceIdentity': json.dumps(SOURCE)}, {'id': 'serving-meter', 'sourceIdentity': json.dumps(SOURCE)}],
                             'series': 'watts', 'sampling': {'intervalNanos': 750000000}}}
    execution = {'runUID': 'run', 'stageUID': 'stage', 'stageID': 'one', 'stageName': 'stage', 'namespace': 'chill-system'}
    operation = {'workloads': {NAME: {'producerEndpoint': 'https://serving.invalid', 'producerCAFile': '/ca/tls.crt',
                  'producerTokenFile': '/token', 'deliverySeconds': 10, 'socketSeconds': 30, 'workers': 3,
                  'podSpec': {'serviceAccountName': 'loadgen', 'volumes': [{'name': 'ca', 'secret': {'secretName': 'ca'}}]},
                  'volumeMounts': [{'name': 'ca', 'mountPath': '/ca', 'readOnly': True}]}}}
    config = {'operational': json.dumps(operation), 'chillAPIURL': 'https://serving.invalid', 'minimumFreeBytes': '1'}
    assembled = input_snapshot.stage_config(spec, scenario, execution, config, root)
    return spec, scenario, execution, config, assembled


def power_window():
    return {'from': START, 'to': END, 'complete': False, 'reasons': ['No scheduler history'],
            'nodes': [{'node': {'name': name, 'uid': name + '-uid'}, 'meter': name + '-meter',
                       'joules': None, 'reasons': ['Inactive node'], 'meterJoules': joules, 'meterComplete': True,
                       'gapSeconds': 0, 'gapFraction': 0, 'maximumSampleGapSeconds': .75,
                       'sourceIdentity': SOURCE, 'samples': 15} for name, joules in (('idle', 100), ('serving', 200))]}


def live_fixture(root):
    harness = object.__new__(evaluation.Harness)
    harness.run, harness.root = root, root / 'stages/one'
    harness.scope, harness.prefix = 'stages/one', 'experiments/run/stages/one'
    harness.execution = {'runUID': 'run', 'stageID': 'one', 'namespace': 'release'}
    harness.config = {'chillAPIURL': 'https://chill.invalid', 'operational': '{}'}
    harness.spec = {'loadGenerator': {'nodeName': 'generator'}, 'powerMeasurement': {'nodes': [{'name': 'meter'}]},
                    'settingsPatch': {'autonomousWorkloads': ['stage-workload']}}
    harness.snapshot = {'runSpec': {'settingsPatch': {'autonomousWorkloads': ['run-workload']}},
                        'bindings': {'one': {'config': {'timeboxes': {**input_snapshot.TIMEBOXES,
                                                                    'watchSeconds': 2, 'gateSeconds': 5}}}}}
    instances = [{'podUID': uid, 'ready': uid != 'unready', 'node': {'name': node}, 'ownerWorkloadUID': owner}
                 for uid, node, owner in [('active', 'dynamic', 'stage-workload'), ('unready', 'other', 'run-workload'),
                                          ('unrouted', 'meter', 'stage-workload'), ('retired', 'meter', 'stage-workload')]]
    state = {'snapshot': {'instances': instances},
             'decision': {'at': START, 'keep': {'routing': {'workload': {'active/model': 1, 'retired/model': 1}}}},
             'actions': [{'kind': 'stop', 'instance': 'retired/model', 'status': 'running'}]}
    nodes = [{'metadata': {'name': name}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}],
                                                   'nodeInfo': {'bootID': 'original-boot'}}}
             for name in ('generator', 'meter', 'dynamic', 'other')]
    objects = {'nodes': nodes,
               'services': [{'metadata': {'name': 'chill-executor-' + instance['podUID']},
                             'spec': {'clusterIP': '10.0.0.1', 'ports': [{'port': 8000}]}} for instance in instances],
               'pods': [{'metadata': {'uid': instance['podUID'], 'name': 'serving-' + instance['podUID']},
                         'spec': {'nodeName': instance['node']['name']}, 'status': {}} for instance in instances],
               'jobs': []}
    def kube(namespace, verb, resource):
        assert namespace == 'release' and verb == 'get'
        return {'items': objects[resource]}
    return harness, state, objects, kube


class EvaluationTests(unittest.TestCase):
    def test_start_gate_only_blocks_participating_not_ready_nodes(self):
        for blocked in ('generator', 'meter', 'dynamic', 'other'):
            with self.subTest(node=blocked), tempfile.TemporaryDirectory() as directory:
                harness, state, objects, kube = live_fixture(Path(directory))
                next(node for node in objects['nodes'] if node['metadata']['name'] == blocked)['status']['conditions'][0]['status'] = 'False'
                with patch.object(input_snapshot, 'api', return_value={'state': state}), patch.object(input_snapshot, 'kubectl', side_effect=kube), patch.object(live, 'health', return_value=200):
                    attempt = live.gate_attempt(harness)
                self.assertEqual(attempt['passed'], blocked == 'other')
                self.assertEqual(attempt['participatingNodes'], ['dynamic', 'generator', 'meter'])
                self.assertEqual(attempt['notReady'], [] if blocked == 'other' else [blocked])
                self.assertEqual(attempt['notReadyNonParticipating'], ['other'] if blocked == 'other' else [])

    def test_start_gate_rechecks_and_records_exclusion_reasons(self):
        with tempfile.TemporaryDirectory() as directory:
            harness, state, objects, kube = live_fixture(Path(directory))
            clock = [0]
            def sleep(seconds):
                clock[0] += seconds
            with patch.object(input_snapshot, 'api', return_value={'state': state}), patch.object(input_snapshot, 'kubectl', side_effect=kube), patch.object(live, 'health', side_effect=[503, 200]) as probe, patch.object(live.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(live.time, 'sleep', side_effect=sleep):
                live.start_gate(harness)
            attempts = evidence_io.load(harness.root / 'start-gate.json')
            self.assertEqual([attempt['passed'] for attempt in attempts], [False, True])
            self.assertEqual(clock[0], 2)
            self.assertEqual(probe.call_count, 2)
            self.assertEqual(attempts[-1]['excludedExecutors'], {
                'chill-executor-unready': {'reason': 'notReadyInCHILL', 'node': 'other'},
                'chill-executor-unrouted': {'reason': 'notRouted', 'node': 'meter'},
                'chill-executor-retired': {'reason': 'retiring:stop', 'node': 'meter'}})
            self.assertEqual(list(attempts[-1]['executors']), ['chill-executor-active'])

    def test_start_gate_requires_an_executor(self):
        with tempfile.TemporaryDirectory() as directory:
            harness, state, objects, kube = live_fixture(Path(directory))
            objects['services'] = []
            with patch.object(input_snapshot, 'api', return_value={'state': state}), patch.object(input_snapshot, 'kubectl', side_effect=kube):
                attempt = live.gate_attempt(harness)
            self.assertFalse(attempt['passed'])
            self.assertEqual(attempt['executors'], {})

    def test_start_gate_timeout_is_target_unavailable_and_preserves_attempts(self):
        with tempfile.TemporaryDirectory() as directory:
            harness, state, objects, kube = live_fixture(Path(directory))
            clock = [0]
            def sleep(seconds):
                clock[0] += seconds
            harness.upload = lambda path, key: {'store': 'test', 'key': key, 'size': path.stat().st_size}
            harness.controller = lambda: self.fail('gate must run before controller or load generation')
            with patch.object(input_snapshot, 'open_snapshot'), patch.object(input_snapshot, 'api', return_value={'state': state}), patch.object(input_snapshot, 'kubectl', side_effect=kube), patch.object(live, 'health', return_value=503), patch.object(live, 'start_watcher'), patch.object(live, 'stop_watcher'), patch.object(live.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(live.time, 'sleep', side_effect=sleep), patch('builtins.print'):
                self.assertEqual(harness.execute('stage'), 1)
            record = evidence_io.load(harness.run / 'results/stages/one/stage.json')
            self.assertEqual(record['execution']['reason'], 'TargetUnavailable')
            self.assertLess(clock[0], 5)  # gateSeconds=5, watchSeconds=2: attempts at 0, 2, 4; none at the deadline
            attempts = evidence_io.load(harness.root / 'start-gate.json')
            self.assertEqual(len(attempts), 3)
            self.assertTrue(all(not attempt['passed'] for attempt in attempts))
            self.assertTrue(any(ref['key'].endswith('/artifacts/start-gate.json') for ref in record['artifacts']))

    def test_start_gate_success_after_deadline_is_target_unavailable(self):
        with tempfile.TemporaryDirectory() as directory:
            harness, _, _, _ = live_fixture(Path(directory))
            clock = [0]
            def slow_pass(context):
                clock[0] += live.timeboxes(context)['gateSeconds'] + 1
                return {'at': START, 'passed': True}
            with patch.object(live, 'gate_attempt', side_effect=slow_pass), patch.object(live.time, 'monotonic', side_effect=lambda: clock[0]):
                with self.assertRaisesRegex(RuntimeError, '^TargetUnavailable'):
                    live.start_gate(harness)

    def test_parent_run_deletion_stops_stage_load(self):
        # Defect 20: deleting the parent Run is the operator's stop; the running stage must end
        # through the conductor's STOP path instead of offering the whole load window.
        for deleting in (False, True):
            with self.subTest(deleting=deleting), tempfile.TemporaryDirectory() as directory:
                harness, _, _, _ = live_fixture(Path(directory))
                harness.execution['runName'], harness.spec['target'] = 'run-name', {}
                parent = {'metadata': {'name': 'run-name', 'uid': 'run', **({'deletionTimestamp': START} if deleting else {})}}
                controller = SimpleNamespace(observe_generators=lambda folder: None)
                def run():
                    controller.monitor(0, harness.root / 'measured')
                    raise RuntimeError('LoadContinued')
                controller.run = run
                def kubectl(namespace, *args):
                    self.assertEqual((namespace, *args), ('release', 'get', 'evaluationrun', 'run-name'))
                    return parent
                with patch.object(live, 'start_gate'), patch.object(harness, 'controller', return_value=controller), \
                        patch.object(input_snapshot, 'check_target', return_value=[]), patch.object(input_snapshot, 'check_fleet'), \
                        patch.object(input_snapshot, 'kubectl', side_effect=kubectl):
                    with self.assertRaisesRegex(RuntimeError, '^Stopped' if deleting else '^LoadContinued'):
                        harness.stage()

    def test_stop_watcher_kills_process_group_when_cycle_overruns(self):
        with tempfile.TemporaryDirectory() as directory:
            harness, _, _, _ = live_fixture(Path(directory))
            waits = []
            def wait(timeout=None):
                waits.append(timeout)
                if timeout is not None:
                    raise live.subprocess.TimeoutExpired('watch', timeout)
            process = SimpleNamespace(pid=4242, terminate=lambda: None, wait=wait)
            with patch.object(live.os, 'killpg') as killpg:
                live.stop_watcher(harness, process)
            killpg.assert_called_once_with(4242, live.signal.SIGKILL)
            self.assertEqual(waits, [live.timeboxes(harness)['apiSeconds'], None])

    def test_step_uploads_every_nonempty_artifact_after_watcher_stops(self):
        for step in ('stage', 'finish'):
            for failed in (False, True):
                with self.subTest(step=step, failed=failed), tempfile.TemporaryDirectory() as directory:
                    harness, _, _, _ = live_fixture(Path(directory))
                    if step == 'finish':
                        harness.scope, harness.prefix, harness.root = 'run', 'experiments/run/run', harness.run
                    events, uploaded = [], []
                    def launch(args, **_):
                        events.append('watch-start')
                        folder = harness.root / 'watch'
                        folder.mkdir(parents=True)
                        live.append(folder / (step + '.jsonl'), {'at': START})
                        return SimpleNamespace(terminate=lambda: events.append('terminate'), wait=wait)
                    def wait(timeout=None):
                        events.append('wait')
                        live.append(harness.root / 'watch' / (step + '-alerts.jsonl'), {'at': END, 'detail': 'last alert'})
                    def run():
                        events.append('step')
                        evidence_io.save(harness.root / 'measured/evidence.json', {'observed': True})
                        (harness.root / 'empty').touch()
                        if failed:
                            raise RuntimeError('TargetUnavailable: test target')
                    def upload(path, key):
                        self.assertEqual(events[-1], 'wait')
                        uploaded.append(key)
                        return {'store': 'test', 'key': key, 'size': path.stat().st_size}
                    setattr(harness, step, run)
                    harness.upload = upload
                    with patch.object(input_snapshot, 'open_snapshot'), patch.object(live.subprocess, 'Popen', side_effect=launch), patch('builtins.print'):
                        self.assertEqual(harness.execute(step), int(failed))
                    self.assertEqual(events, ['watch-start', 'step', 'terminate', 'wait'])
                    record = evidence_io.load(harness.run / 'results' / harness.scope / (step + '.json'))
                    files = {path.relative_to(harness.root).as_posix() for path in harness.root.rglob('*')
                             if path.is_file() and path.stat().st_size and 'results' not in path.relative_to(harness.root).parts}
                    prefix = harness.prefix + '/' + step + '/artifacts/'
                    artifact_files = {ref['key'][len(prefix):] for ref in record['artifacts']}
                    self.assertEqual(artifact_files, files)
                    self.assertEqual(set(uploaded), {prefix + name for name in files} | {harness.prefix + '/' + step + '/result.json'})

    def test_live_watcher_records_diagnostics_memory_and_deduplicated_alerts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            harness, state, objects, kube = live_fixture(root)
            state['decision'].update(status='Pending', workloads=[{'uid': 'workload-full', 'state': 'Fixed', 'reason': 'ReservedScope: diagnostic'}],
                                     search={'loads': [{'workloadUID': 'workload-full', 'inputsPerSecond': 1, 'recentInputsPerSecond': 2}]})
            state['actions'] = [{'id': 'power-change', 'kind': 'power', 'node': {'name': 'dynamic'}, 'status': 'partial'}]
            objects['nodes'][0]['status']['conditions'][0]['status'] = 'False'
            objects['pods'][0]['status']['containerStatuses'] = [{'restartCount': 2, 'lastState': {'terminated': {'reason': 'OOMKilled'}}}]
            objects['jobs'] = [{'metadata': {'name': 'load-failed'}, 'status': {'conditions': [{'type': 'Failed', 'status': 'True'}]}}]
            def api(config, path):
                if path == '/api/v1/scheduler/settings':
                    return {'scaleDownStabilizationNanos': 60_000_000_000}
                if path == '/api/v1/scheduler':
                    return {'state': state}
                return {'nodes': [{'node': {'name': 'meter'}, 'meterComplete': False, 'gapCount': 2, 'maximumSampleGapSeconds': 4}]}
            watcher = live.Watcher(root, 'stage', harness.config, 'release', os.getpid())
            with patch.object(input_snapshot, 'api', side_effect=api), patch.object(input_snapshot, 'kubectl', side_effect=kube), patch.object(live, 'health', return_value=503), patch('builtins.print') as log:
                watcher.cycle()
                watcher.held_since['workload-full'] -= live.dt.timedelta(seconds=61)
                objects['nodes'][0]['status']['nodeInfo']['bootID'] = 'changed-boot'
                watcher.cycle()
                count = log.call_count
                watcher.cycle()
                self.assertEqual(log.call_count, count)
            rows = [json.loads(line) for line in watcher.rows.read_text().splitlines()]
            self.assertEqual(len(rows), 3)
            row = rows[-1]
            self.assertGreaterEqual(row['workloads']['workload']['heldSeconds'], 60)
            self.assertEqual(row['instances'], {'total': 4, 'ready': 3, 'routable': 2, 'replacementCandidates': 0})
            self.assertEqual(row['loadJobs'], {'load-failed': 'Failed'})
            self.assertEqual(row['servingPodIssues'][0]['last'], 'OOMKilled')
            self.assertEqual(row['power']['meter']['gapCount'], 2)
            self.assertGreater(row['harness']['vmRSSKiB'], 0)
            self.assertGreaterEqual(row['harness']['vmHWMKiB'], row['harness']['vmRSSKiB'])
            alerts = [json.loads(line) for line in watcher.alerts.read_text().splitlines()]
            self.assertEqual({alert['key'][0] for alert in alerts},
                             {'held', 'actionOnRisingLoad', 'action', 'executor', 'node', 'pod', 'loadJob'})
            self.assertTrue(any(alert['key'][2] == 'reboot' for alert in alerts if alert['key'][0] == 'node'))
            self.assertTrue(all(call.args[0].startswith('EVALUATION_ALERT ') for call in log.call_args_list))

    def test_live_watcher_records_cycle_errors_without_stopping(self):
        with tempfile.TemporaryDirectory() as directory:
            watcher = live.Watcher(Path(directory), 'finish', {}, 'release', -1)
            with patch.object(input_snapshot, 'api', side_effect=OSError('API offline')), patch.object(input_snapshot, 'kubectl', side_effect=OSError('kube offline')):
                watcher.cycle()
                watcher.cycle()
            rows = [json.loads(line) for line in watcher.rows.read_text().splitlines()]
            self.assertEqual(len(rows), 2)
            for row in rows:
                self.assertTrue(all(name + 'Error' in row for name in ('scheduler', 'kube', 'power', 'harness')))

    def exercise_publication(self, lost=False, residual=False):
        temporary_directory = tempfile.TemporaryDirectory
        with temporary_directory() as directory:
            root = Path(directory)
            run = root / 'campaign/run'
            run.mkdir(parents=True)
            spec, scenario, execution, config, assembled = fixture(root)
            reference = {'store': 'store', 'key': 'experiments/run/run/prepare/input-snapshot.json', 'size': 123}
            execution = {'runUID': 'run', 'runName': 'run', 'prepareCompleted': True, 'childRefs': [], 'inputSnapshotRef': reference}
            template = {'id': 'one', 'loadScenarioRef': {'name': 'load', 'uid': 'load-uid'}}
            spec['stages'] = [template]
            scenario['ref'] = {'name': 'load', 'uid': 'load-uid'}
            path = root / 'config.json'
            evidence_io.save(path, assembled)
            bound = conductor.bind(path)
            snapshot = {'runSpec': spec, 'bindings': {'one': bound}, 'scenarios': {'load-uid': scenario}, 'preparedAt': START}
            scope = run / 'stages/one/measured'
            evidence_io.save(scope / 'phase.json', {'startUTC': START, 'endUTC': END})
            request = {'offered': 1, 'clientRows': 1, 'deadlineSuccess': 1, 'D1': 'OBSERVED_THRESHOLD_MET',
                       'completedLatencyNS': {'samples': 1}, 'workloadUID': scenario['workloads'][0]['uid'],
                       'definitionID': 'observed-definition', 'issuanceFidelity': {}}
            evidence_io.save(scope / 'judgment.json', {'schema': 'e5-judgment.v3', 'status': 'MEASURED_DESCRIPTIVE',
                'requests': {NAME: request}, 'components': {'slo': {'status': 'PASS'}},
                'power': {'complete': True, 'wholePoolJoules': 1}})
            complete = {'execution': {'status': 'Complete', 'reason': 'ProcedureComplete'},
                        'cleanup': {'status': 'Complete', 'reason': 'ProcedureComplete'}, 'artifacts': []}
            records = {'experiments/run/stages/one/' + step + '/result.json': {
                **complete, 'measurement': {'verdict': 'PASS'}} for step in ('factor', 'stage', 'judge', 'cleanup-jobs')}
            records.update({'experiments/run/run/' + step + '/result.json': complete for step in ('prepare', 'finish')})
            if lost:
                execution['childRefs'] = [{'stageID': 'one', 'uid': 'stage', 'name': 'stage',
                    'execution': {'status': 'Failed', 'reason': 'ExecutionLost'},
                    'cleanup': {'status': 'Complete', 'reason': 'ProcedureComplete'}, 'uncertain': True}]
                # A missing stage result must not erase the controller's execution failure.
                del records['experiments/run/stages/one/stage/result.json']
            if residual:
                records['experiments/run/run/finish/result.json'] = {
                    **complete, 'cleanup': {'status': 'Failed', 'reason': 'ResidualState'},
                    'residuals': [{'kind': 'Pod', 'name': 'remaining-producer', 'reason': 'ResidualState'}]}
            def get(ref, destination):
                if ref['key'] not in records:
                    raise FileNotFoundError(ref['key'])
                evidence_io.save(destination, records[ref['key']])
            context = SimpleNamespace(run=run, spec=spec, execution=execution, config=config, snapshot=snapshot,
                                      store=SimpleNamespace(name='store', get=get))
            def publish(args, **kwargs):
                manifest = evidence_io.load(args[args.index('--manifest') + 1])
                summary = evidence_io.load(manifest['summary']['localPath'])
                row = summary['stages'][0]['kpis']['requests'][NAME]
                declared = summary['definition']['workloads'][0]
                source = row['identitySource']
                verified = (row['workloadUID'] == declared['uid'] and row['definitionID'] == 'observed-definition'
                            and row['slo'] == declared['slo'] and row['slo']['percentile'] == 95
                            and any(item['store'] == source['store'] and item['key'] == source['key'] for item in summary['sources']))
                self.assertTrue(verified, 'Applied p95 SLO must be verifiable from the published source set')
                if lost:
                    self.assertEqual(summary['outcome']['execution'], {'status': 'Failed', 'reason': 'ExecutionLost'})
                    self.assertEqual(summary['outcome']['cleanup']['status'], 'Failed' if residual else 'Complete')
                    self.assertEqual(summary['outcome']['cleanup']['residuals'],
                        [{'kind': 'Pod', 'name': 'remaining-producer', 'reason': 'ResidualState'}] if residual else [])
                return SimpleNamespace(returncode=0, stderr='')
            env = {'CHILL_EVIDENCE_BUCKET': 'bucket', 'CHILL_EVIDENCE_ENDPOINT': 'https://store.invalid',
                   'CHILL_EVIDENCE_REGION': 'region', 'AWS_ACCESS_KEY_ID': 'test', 'AWS_SECRET_ACCESS_KEY': 'test'}
            with patch.dict(os.environ, env), patch.object(publication.tempfile, 'TemporaryDirectory',
                 side_effect=lambda **kwargs: temporary_directory(prefix=kwargs['prefix'], dir=root)), \
                 patch.object(publication.subprocess, 'run', side_effect=publish):
                publication.publish(context)

    def test_publication_links_frozen_applied_slo_source(self):
        self.exercise_publication()

    def test_execution_loss_cleanup_uses_latest_observations(self):
        self.exercise_publication(lost=True)
        self.exercise_publication(lost=True, residual=True)

    def test_all_no_load_stages_cannot_become_summary_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            stages = [{'id': 'one', 'kpis': {'status': 'PARTIAL', 'components': {'slo': {'status': 'INVALID_NO_LOAD'}}}},
                      {'id': 'two', 'kpis': {'status': 'PARTIAL', 'components': {'slo': {'status': 'NOT_EVALUATED'}}}}]
            results = {stage['id']: {'measurement': {'verdict': 'NOT_EVALUATED'}} for stage in stages}
            for records in (results, {}):
                summary = judgment_rules.summarize(None, Path(directory), evaluation={'stages': stages, 'results': records})
                self.assertEqual(summary['status'], 'NOT_EVALUATED')
                self.assertEqual(publication.verdict(summary)['verdict'], 'NOT_EVALUATED')

    def test_diagnostic_authorization_failures_remain_fatal(self):
        controller = object.__new__(conductor.CPO)
        for code in (401, 403):
            with self.subTest(code=code):
                error = urllib.error.HTTPError('https://api.invalid', code, 'Denied', {}, None)
                def poll(deadline):
                    raise error
                phase = {}
                with self.assertRaises(urllib.error.HTTPError) as caught:
                    controller.sample_resources(SimpleNamespace(poll=poll), 1, phase)
                self.assertEqual(caught.exception.code, code)
                self.assertNotIn('diagnosticErrors', phase)

    def test_slo_fidelity_energy_injection_and_provenance_are_independent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, _, _, _, config = fixture(root)
            other = 'other-workload'
            template = config['workloads'][0]
            config['workloads'].append({**copy.deepcopy(template), 'name': other, 'uid': 'other-uid'})
            names = [NAME, other]
            config['seeds'] = dict.fromkeys(names, '202')
            config['arrivals'] = dict.fromkeys(names, 'poisson')
            config['loadGenerator']['shards'] = dict.fromkeys(names, 1)
            config['stages'][0]['parts'][0]['rates'] = dict.fromkeys(names, 2.5)
            config['traceOffsets'] = dict.fromkeys(names, [1, 2])
            config['evaluation']['failureInjection'] = [{'id': 'unrealized'}]
            path = root / 'config.json'
            evidence_io.save(path, config)
            bound = conductor.bind(path)
            evidence_io.save(root / 'binding.json', bound)
            evidence_io.save(root / 'phase.json', {'stage': bound['schedule'][0], 'startUTC': START, 'endUTC': END,
                'drainedUTC': END, 'starts': dict.fromkeys(names, {'at': START, 'monotonicNS': 100}),
                'drain': {'complete': False, 'reasons': ['DrainWatchdog']}})
            evidence_io.save(root / 'failure-events.json', {'status': 'INVALID', 'records': [{'record': {'outcome': 'Cancelled'}}]})
            window = power_window()
            window['nodes'][0].update(meterComplete=False, gapFraction=.2)
            partial = archive.meter_energy(window, spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
            missing = {'complete': False, 'energyStatus': 'UNAVAILABLE', 'observedJoules': None, 'wholePoolJoules': None, 'reasons': ['no samples']}
            evidence_io.save(root / 'power.json', partial)
            evidence_io.save(root / 'power-drain-inclusive.json', missing)
            clients, indexed = {}, {}
            for workload in config['workloads']:
                name, uid = workload['name'], workload['uid']
                rows = [{'requestIndex': i, 'requestID': name + str(i), 'workloadUID': uid, 'scheduledOffsetNs': i+1,
                         'issued': True, 'sentMonotonicNS': 1000000101+i} for i in range(2)]
                clients[name] = rows
                (root / (name + '-client.jsonl')).write_text(''.join(json.dumps(row)+'\n' for row in rows))
                indexed[uid] = {'errors': [{'error': 'one unrelated raw hour/provenance error'}],
                    'identities': [{'workloadUID': uid, 'definitionID': 'definition'}],
                    'summary': {'uniqueRequests': 2, 'finalConflicts': [{'requestID': name+'0'}] if name==NAME else []},
                    'finals': {name+str(i): {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 100000000}
                               for i in range(2) if name!=NAME or i!=0}}
            original_module = evidence_io.module
            def modules(name, path):
                return SimpleNamespace(iter_request_indexes=lambda *args, **kwargs: indexed) if name=='evaluation_raw' else original_module(name, path)
            with patch.object(evidence_io, 'module', side_effect=modules):
                result = judgment_rules.judge(root / 'binding.json', root)
                self.assertEqual(result['requests'][other]['deadlineSuccess'], 2)
                self.assertEqual(result['requests'][other]['D1'], 'OBSERVED_THRESHOLD_MET')
                self.assertEqual(result['requests'][NAME]['quarantinedRequestIDs'], [NAME+'0'])
                self.assertEqual(result['requests'][NAME]['completedLatencyNS']['samples'], 1)
                self.assertEqual(result['components']['energy'], {'offered': 'PARTIAL', 'drainInclusive': 'UNAVAILABLE'})
                self.assertEqual(result['components']['injection']['status'], 'NOT_REALIZED')
                self.assertEqual(result['components']['loadFidelity'][other]['generatorQuality']['status'], 'OBSERVED')
                self.assertEqual(publication.verdict(result)['verdict'], 'FAIL')
                evidence_io.save(root / 'power.json', missing)
                no_power = judgment_rules.judge(root / 'binding.json', root)
                self.assertEqual(no_power['requests'][other]['deadlineSuccess'], 2)
                self.assertEqual(no_power['components']['energy']['offered'], 'UNAVAILABLE')
                self.assertIsNone(no_power['power']['wholePoolJoules'])
                no_samples = power_window()
                for node in no_samples['nodes']:
                    node.update(meterJoules=None, samples=0, meterComplete=False)
                unavailable = archive.meter_energy(no_samples, spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
                self.assertEqual(unavailable['energyStatus'], 'UNAVAILABLE')
                self.assertIsNone(unavailable['observedJoules'])
                measured = archive.meter_energy(power_window(), spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
                evidence_io.save(root / 'power.json', measured)
                for name in names:
                    for row in clients[name]:
                        row.update(issued=False, error='UnsentAtWindowEnd')
                    (root / (name + '-client.jsonl')).write_text(''.join(json.dumps(row)+'\n' for row in clients[name]))
                no_load = judgment_rules.judge(root / 'binding.json', root)
                self.assertEqual(no_load['components']['slo']['status'], 'INVALID_NO_LOAD')
                self.assertEqual(publication.verdict(no_load)['verdict'], 'NOT_EVALUATED')
                self.assertEqual(no_load['power']['energyStatus'], 'MEASURED')
                for name in names:
                    self.assertEqual(no_load['requests'][name]['substages'][0]['D1'], 'INVALID_NO_LOAD')
                clients[NAME][0]['workloadUID'] = 'wrong-workload'
                (root / (NAME + '-client.jsonl')).write_text(json.dumps(clients[NAME][0])+'\n')
                mismatched = judgment_rules.judge(root / 'binding.json', root)
                self.assertEqual(mismatched['requests'][NAME]['identityMismatchClientRows'], 1)
                self.assertEqual(mismatched['requests'][NAME]['deadlineSuccess'], 0)

    def test_lateness_and_missing_sends_are_quality_facts_not_global_invalidity(self):
        quality = {'latenessToleranceNS': 50000000, 'maxExceedingFraction': .01}
        rows = [{'requestIndex': 0, 'scheduledOffsetNs': 1, 'sentMonotonicNS': 1000000101, 'issued': True},
                {'requestIndex': 1, 'scheduledOffsetNs': 2, 'issued': False, 'error': 'UnsentAtWindowEnd'}]
        result = request_analysis.issuance_fidelity(rows, [1, 2], 100, 2000000000, request_analysis.stats, quality)
        self.assertEqual(result['generatorQuality']['status'], 'OBSERVED')
        self.assertEqual(result['generatorQuality']['thresholdLabel'], 'EXCEEDS_REPORTED_THRESHOLD')
        self.assertEqual(result['unissued'], 1)
        self.assertEqual(result['sendLatenessNS']['samples'], 1)
        empty = request_analysis.issuance_fidelity([], [1, 2], 100, 2000000000, request_analysis.stats, quality)
        self.assertEqual(empty['generatorQuality']['status'], 'INVALID_NO_LOAD')

    def test_unknown_operational_keys_are_ignored_but_effective_fields_validate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, config, _ = fixture(root)
            operation = json.loads(config['operational'])
            operation.update(futureFeature=True, timeboxes={'unusedBudget': -1}, collection={'futureCoverage': 'ignored'})
            operation['workloads'][NAME].update(uid='wrong-uid', futureClient=True)
            operation['workloads'][NAME]['podSpec']['hostNetwork'] = True
            config['operational'] = json.dumps(operation)
            assembled = input_snapshot.stage_config(spec, scenario, execution, config, root)
            self.assertIn('futureFeature', assembled['ignoredKeys'])
            self.assertIn('timeboxes.unusedBudget', assembled['ignoredKeys'])
            self.assertIn('workloads.' + NAME + '.uid', assembled['ignoredKeys'])
            self.assertEqual(assembled['workloads'][0]['uid'], 'workload')
            path = root / 'config.json'
            evidence_io.save(path, assembled)
            controller = object.__new__(conductor.CPO)
            controller.b = conductor.bind(path)
            controller.c, controller.tb = assembled, assembled['timeboxes']
            controller.ws = {NAME: assembled['workloads'][0]}
            job = controller.job(NAME, controller.b['schedule'][0], root)
            self.assertNotIn('hostNetwork', job['spec']['template']['spec'])
            self.assertIn('hostNetwork', job['metadata']['annotations']['edge.dacs.io/ignored-pod-keys'])
            operation['workloads'][NAME]['workers'] = 0
            config['operational'] = json.dumps(operation)
            with self.assertRaisesRegex(ValueError, 'positive client'):
                input_snapshot.stage_config(spec, scenario, execution, config, root)

    def test_runtime_health_is_recorded_but_wrong_node_uid_remains_fatal(self):
        with tempfile.TemporaryDirectory() as directory:
            harness = object.__new__(evaluation.Harness)
            harness.root = Path(directory)
            harness.execution = {'namespace': 'evaluation-ns'}
            target = {'components': [{'name': 'server', 'resource': {'kind': 'Pod', 'name': 'server', 'namespace': 'evaluation-ns'}}]}
            pod = {'kind': 'Pod', 'metadata': {'uid': 'target'},
                   'status': {'containerStatuses': [{'name': 'server', 'ready': False}], 'conditions': [{'type': 'Ready', 'status': 'False'}]}}
            with patch.object(input_snapshot, 'kubectl', return_value=pod):
                self.assertFalse(input_snapshot.check_target(harness, target)[0]['available'])
            spec = {'powerMeasurement': {'nodes': [{'name': 'node', 'uid': 'node-uid'}]}, 'loadGenerator': {'nodeName': 'node'}}
            node = {'metadata': {'name': 'node', 'uid': 'node-uid'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'False'}]}}
            def k(namespace, verb, kind, *args):
                return node if kind == 'node' else {'items': []}
            with patch.object(input_snapshot, 'kubectl', side_effect=k):
                self.assertEqual(input_snapshot.participant_pods(harness, spec), [])
                self.assertFalse(evidence_io.load(harness.root / 'node-node-observations.json')[-1]['ready'])
                node['metadata']['uid'] = 'replacement'
                with self.assertRaisesRegex(ValueError, 'Node UID'):
                    input_snapshot.participant_pods(harness, spec)

    def test_energy_windows_collect_independently_of_raw_and_each_other(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / 'measured'
            folder.mkdir()
            spec, _, _, _, config = fixture(root)
            env = root / 'environment.json'
            evidence_io.save(env, {'paths': {'campaign': str(root)}})
            tail_end = '2026-11-03T00:00:11Z'
            phase = {'startUTC': START, 'endUTC': END, 'drainedUTC': tail_end}
            calls = []
            def api(method, path, **kwargs):
                calls.append(path)
                if len(calls) == 1:
                    raise RuntimeError('offered power unavailable')
                window = power_window()
                window['to'] = tail_end
                return window
            with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(env)), patch.object(archive.time, 'time_ns', return_value=evidence_io.timestamp_ns(tail_end)+180000000000), patch.object(archive, '_raw_get', side_effect=RuntimeError('raw index unavailable')):
                archive.collect(folder, phase, config['collection'], SimpleNamespace(api=api), 5)
            self.assertEqual(len(calls), 2)
            self.assertEqual(evidence_io.load(folder / 'power.json')['energyStatus'], 'UNAVAILABLE')
            self.assertEqual(evidence_io.load(folder / 'power-drain-inclusive.json')['energyStatus'], 'MEASURED')

    def test_overwrite_is_still_a_safety_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, _, _, _, config = fixture(root)
            path = root / 'config.json'
            evidence_io.save(path, config)
            controller = object.__new__(conductor.CPO)
            controller.b, controller.c = conductor.bind(path), config
            controller.a = SimpleNamespace(stage='e5-test')
            Path(config['evaluation']['stageFolder']).mkdir()
            with self.assertRaisesRegex(RuntimeError, 'overwrite'):
                controller.run()

    def test_public_patch_apply_readback_revert(self):
        api = SettingsAPI()
        before = copy.deepcopy(api.value)
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / 'patch.json'
            applied = settings_patch.factor_apply(api, {'controls': {'power': False}, 'controlIntervalNanos': 42, 'autonomousWorkloads': ['two'], 'minimumDemand': {'remove': None}}, receipt)
            self.assertTrue(applied['accepted'])
            self.assertIn('appliedAt', applied)
            self.assertNotIn('remove', api.value['minimumDemand'])
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT', 'GET'])
            api.value['controls']['routing'] = False
            api.value['minimumDemand']['other'] = 8
            settings_patch.factor_revert(api, receipt)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT', 'GET', 'GET', 'PUT', 'GET'])
            expected = copy.deepcopy(before)
            expected['controls']['routing'] = False
            expected['minimumDemand']['other'] = 8
            self.assertEqual(api.value, expected)
            final = evidence_io.load(receipt)
            for key in ('request', 'response', 'readback', 'appliedAt', 'revertRequest', 'revertResponse', 'revertReadback', 'revertedAt'):
                self.assertIn(key, final)
            self.assertNotEqual(final['revertRequest'], before)

    def test_failed_put_never_reverts_other_settings(self):
        api = SettingsAPI()
        api.fail_put = True
        before = copy.deepcopy(api.value)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaisesRegex(urllib.error.HTTPError, '403'):
                settings_patch.factor_apply(api, {'controls': {'power': False}}, path)
            settings_patch.factor_revert(api, path)
            self.assertEqual(api.value, before)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT'])
            self.assertNotIn('appliedAt', evidence_io.load(path))
            self.assertIn('failedAt', evidence_io.load(path))

    def test_patch_conflict_preserves_another_writer(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            settings_patch.factor_apply(api, {'controlIntervalNanos': 42}, path)
            api.value['controlIntervalNanos'] = 50
            settings_patch.factor_revert(api, path)
            self.assertEqual(api.value['controlIntervalNanos'], 50)
            self.assertEqual(sum(call[0] == 'PUT' for call in api.calls), 1)
            self.assertEqual(evidence_io.load(path)['revertSkipped'][0]['reason'], 'external-change-revert-skipped')
            self.assertIn('revertedAt', evidence_io.load(path))

    def test_missing_if_match_returns_428_and_changes_nothing(self):
        api = SettingsAPI()
        before, metadata = copy.deepcopy(api.value), {}
        with self.assertRaises(urllib.error.HTTPError) as caught:
            api('PUT', settings_patch.SETTINGS, {}, response=metadata)
        self.assertEqual(caught.exception.code, 428)
        self.assertEqual(metadata['statusCode'], 428)
        self.assertEqual(api.value, before)
        api.omit_get_etag = True
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(RuntimeError, 'ETag'):
            settings_patch.factor_apply(api, {'controls': {'power': False}}, Path(directory) / 'patch.json')
        self.assertEqual(sum(call[0] == 'PUT' for call in api.calls), 1)

    def test_apply_conflict_is_recorded_without_retry_or_revert(self):
        api = SettingsAPI()
        api.reject_codes = [409]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaises(urllib.error.HTTPError):
                settings_patch.factor_apply(api, {'controls': {'power': False}}, path)
            settings_patch.factor_revert(api, path)
            receipt = evidence_io.load(path)
            self.assertEqual(receipt['put']['ifMatch'], '"1"')
            self.assertEqual(receipt['put']['statusCode'], 409)
            self.assertEqual(receipt['put']['response']['code'], 'SettingsConflict')
            self.assertNotIn('appliedAt', receipt)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT'])

    def test_revert_conflict_refreshes_once_and_preserves_external_fields(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            settings_patch.factor_apply(api, {'controls': {'power': False}}, path)
            api.reject_codes = [409]
            api.on_conflict = lambda: api.value['controls'].update(routing=False)
            settings_patch.factor_revert(api, path)
            receipt = evidence_io.load(path)
            self.assertEqual([a['statusCode'] for a in receipt['revertAttempts']], [409, 200])
            self.assertEqual([a['ifMatch'] for a in receipt['revertAttempts']], ['"2"', '"3"'])
            self.assertEqual(receipt['revertAttempts'][-1]['responseETag'], '"4"')
            self.assertFalse(api.value['controls']['routing'])
            self.assertTrue(api.value['controls']['power'])

    def test_second_revert_conflict_blocks_return_without_more_puts(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            settings_patch.factor_apply(api, {'controls': {'power': False}}, path)
            api.reject_codes = [409, 409]
            with self.assertRaisesRegex(RuntimeError, 'SettingsConflict'):
                settings_patch.factor_revert(api, path)
            count = len(api.calls)
            with self.assertRaisesRegex(RuntimeError, 'SettingsConflict'):
                settings_patch.factor_revert(api, path)
            self.assertEqual(len(api.calls), count)
            self.assertNotIn('revertedAt', evidence_io.load(path))

    def test_uncertain_put_is_not_silently_returned_or_rolled_back(self):
        api = SettingsAPI()
        api.transport_error = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaises(OSError):
                settings_patch.factor_apply(api, {'controls': {'power': False}}, path)
            with self.assertRaisesRegex(RuntimeError, 'RemoteEffectUnknown'):
                settings_patch.factor_revert(api, path)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT'])

    def test_put_readback_mismatch_retains_applied_receipt(self):
        api = SettingsAPI()
        api.drift = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaisesRegex(RuntimeError, 'readback'):
                settings_patch.factor_apply(api, {'controlIntervalNanos': 42}, path)
            self.assertIn('appliedAt', evidence_io.load(path))
            self.assertFalse(evidence_io.load(path)['accepted'])

    def test_patch_added_nested_object_reverts_absence(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            settings_patch.factor_apply(api, {'minimumDemand': {'new': 7}}, path)
            settings_patch.factor_revert(api, path)
            self.assertNotIn('new', api.value['minimumDemand'])

    def test_null_control_uses_canonical_applied_value_for_readback_and_revert(self):
        api = SettingsAPI()
        def normalize(value):
            value['controls'].setdefault('power', False)
            return value
        api.normalize = normalize
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            receipt = settings_patch.factor_apply(api, {'controls': {'power': None}}, path)
            self.assertTrue(receipt['accepted'])
            self.assertNotIn('power', receipt['request']['controls'])
            self.assertEqual(receipt['changed'][0]['applied'], {'exists': True, 'value': False})
            self.assertFalse(api.value['controls']['power'])
            settings_patch.factor_revert(api, path)
            self.assertTrue(api.value['controls']['power'])
            self.assertEqual(evidence_io.load(path)['revertSkipped'], [])

    def test_new_map_reverts_only_own_members_and_preserves_external_entry(self):
        api = SettingsAPI()
        del api.value['minimumDemand']
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            receipt = settings_patch.factor_apply(api, {'minimumDemand': {'evaluation': 7}}, path)
            self.assertEqual(receipt['changed'][0]['path'], ['minimumDemand', 'evaluation'])
            api.value['minimumDemand']['operator'] = 9
            settings_patch.factor_revert(api, path)
            self.assertEqual(api.value['minimumDemand'], {'operator': 9})
            self.assertEqual(evidence_io.load(path)['revertSkipped'], [])

    def test_new_map_is_removed_when_own_member_cleanup_leaves_it_empty(self):
        api = SettingsAPI()
        del api.value['minimumDemand']
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            settings_patch.factor_apply(api, {'minimumDemand': {'evaluation': 7}}, path)
            settings_patch.factor_revert(api, path)
            self.assertNotIn('minimumDemand', api.value)

    def test_empty_patch_is_read_only(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            self.assertTrue(settings_patch.factor_apply(api, {}, path)['accepted'])
            settings_patch.factor_revert(api, path)
            self.assertEqual([c[0] for c in api.calls], ['GET', 'GET'])

    def test_inline_uint64_seeds(self):
        self.assertEqual(conductor.parse_seeds({NAME: '18446744073709551615'}, [NAME]), {NAME: 18446744073709551615})
        self.assertEqual(conductor.parse_seeds({NAME: '0'}, [NAME]), {NAME: 0})
        self.assertEqual(conductor.parse_seeds({NAME: '0' * 100 + '1'}, [NAME]), {NAME: 1})
        for value in (202, True, '-1', '+1', '1.0', ' 1', '1 ', '', '١', '18446744073709551616'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                conductor.parse_seeds({NAME: value}, [NAME])
        for seeds in ({}, {'foreign': '1'}):
            with self.assertRaises(ValueError):
                conductor.parse_seeds(seeds, [NAME])

    def test_absolute_rates_freeze_without_derived_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, _, _, _, config = fixture(root)
            path = root / 'config.json'
            evidence_io.save(path, config)
            bound = conductor.bind(path)
            self.assertEqual(bound['schema'], 'e5-binding.v4')
            self.assertEqual(bound['schedule'][0]['parts'][0]['rates'], {NAME: 2.5})
            self.assertEqual(bound['schedule'][0]['traces'][NAME]['seed'], 202)
            self.assertEqual(bound['schedule'], conductor.bind(path)['schedule'])

    def test_dataset_reference_drives_job_and_placement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, _, config = fixture(root)
            path = root / 'config.json'
            evidence_io.save(path, config)
            controller = object.__new__(conductor.CPO)
            controller.b = conductor.bind(path)
            controller.c = config
            controller.ws = {w['name']: w for w in config['workloads']}
            controller.tb = config['timeboxes']
            stage = controller.b['schedule'][0]
            job = controller.job(NAME, stage, root)
            pod = job['spec']['template']['spec']
            self.assertEqual(pod['nodeName'], spec['loadGenerator']['nodeName'])
            self.assertEqual(pod['initContainers'][0]['image'], scenario['datasets'][NAME]['spec']['source']['image'])
            self.assertEqual(pod['initContainers'][0]['command'][-1], '/records/evaluation')
            self.assertIn('/evaluation/', pod['initContainers'][0]['command'][2])
            self.assertEqual(pod['containers'][0]['args'][pod['containers'][0]['args'].index('--dataset-dir') + 1], '/evaluation')
            self.assertEqual(pod['containers'][0]['args'][pod['containers'][0]['args'].index('--fields') + 1], 'input')
            self.assertEqual(job['metadata']['ownerReferences'][0]['uid'], execution['stageUID'])
            self.assertEqual(pod['containers'][0]['image'], spec['loadGenerator']['image'])
            self.assertEqual(pod['containers'][0]['resources'], spec['loadGenerator']['resources'])
            policy = json.loads(zlib.decompress(base64.b64decode(pod['containers'][0]['args'][0])))
            self.assertEqual(policy['datasetRef'], scenario['spec']['datasetRefs'][NAME])
            self.assertEqual(policy['shards'], 3)
            self.assertNotIn('offsetsNS', policy)

    def test_campaign_generated_arrivals_fit_exec_argv_and_reproduce_sha(self):
        # Rates copied from the archived 20261103-a1 LoadScenarios in
        # deployB/control-323-plan/eval-objects-backup.json; never read that host file.
        names = ['image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa']
        campaigns = {
            '3-7': [('e5-3-50', 600, [53.075340806062236, 19.36827038762583, 0.28916889079534397]),
                    ('e5-3-70', 600, [74.30547712848713, 27.11557854267616, 0.40483644711348155]),
                    ('e5-3-90', 600, [95.53561345091202, 34.862886697726495, 0.5205040034316192])],
            '7-7': [('e5-7', 900, [31.84520448363734, 11.620962232575499, 0.17350133447720636])],
        }
        producer = input_snapshot.module('producer_arrival_test', evaluation.HERE / 'producer.py')
        for row, parts in campaigns.items():
            with self.subTest(row=row), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                _, _, _, _, config = fixture(root)
                template = config['workloads'][0]
                config['workloads'] = [{**copy.deepcopy(template), 'name': name, 'uid': name + '-uid'} for name in names]
                config['seeds'] = dict.fromkeys(names, '202')
                config['arrivals'] = dict.fromkeys(names, 'poisson')
                config['loadGenerator']['shards'] = dict.fromkeys(names, 3)
                config['stages'] = [{'id': row, 'parts': [
                    {'id': part, 'seconds': seconds, 'rates': dict(zip(names, rates))} for part, seconds, rates in parts]}]
                path = root / 'config.json'
                evidence_io.save(path, config)
                controller = object.__new__(conductor.CPO)
                controller.b = conductor.bind(path)
                controller.c, controller.tb = config, config['timeboxes']
                controller.ws = {workload['name']: workload for workload in config['workloads']}
                stage = controller.b['schedule'][0]
                sizes = []
                for name in names:
                    container = controller.job(name, stage, root)['spec']['template']['spec']['containers'][0]
                    argv = container['command'] + container['args']
                    sizes.append(sum(len(arg.encode()) + 1 for arg in argv))
                    self.assertLessEqual(max(len(arg.encode()) for arg in argv), 120000)
                    self.assertLessEqual(sizes[-1], 120000)
                    policy = json.loads(zlib.decompress(base64.b64decode(container['args'][0])))
                    self.assertNotIn('offsetsNS', policy)
                    self.assertNotIn('offsetsFile', policy)
                    offsets, duration = producer.arrival_trace(policy, 'poisson', 202)
                    self.assertEqual(offsets, stage['traces'][name]['offsetsNS'])
                    self.assertEqual(duration, round(stage['seconds'] * 1e9))
                    policy['arrivalSHA256'] = '0' * 64
                    with self.assertRaisesRegex(ValueError, 'arrival generation differs'):
                        producer.arrival_trace(policy, 'poisson', 202)
                print(row + ': maximum producer argv bytes=' + str(max(sizes)))

    def test_explicit_arrivals_use_workspace_file_and_validate_both_shas(self):
        producer = input_snapshot.module('producer_explicit_test', evaluation.HERE / 'producer.py')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, config, _ = fixture(root)
            workspace = input_snapshot.trace_workspace({'spec': {
                'volumes': [{'name': 'work', 'persistentVolumeClaim': {'claimName': 'run-workspace'}}],
                'containers': [{'volumeMounts': [{'name': 'work', 'mountPath': str(root)}]}]}})
            scenario['traceOffsets'] = {NAME: list(range(0, 1000000000, 10000))}
            config['traceVolume'] = workspace
            assembled = input_snapshot.stage_config(spec, scenario, execution, config, root)
            path = root / 'config.json'
            evidence_io.save(path, assembled)
            controller = object.__new__(conductor.CPO)
            controller.b = conductor.bind(path)
            controller.c, controller.tb = assembled, assembled['timeboxes']
            controller.ws = {workload['name']: workload for workload in assembled['workloads']}
            stage = controller.b['schedule'][0]
            pod = controller.job(NAME, stage, root)['spec']['template']['spec']
            container = pod['containers'][0]
            policy = json.loads(zlib.decompress(base64.b64decode(container['args'][0])))
            self.assertNotIn('offsetsNS', policy)
            mount = next(mount for mount in container['volumeMounts'] if mount['name'] == 'evaluation-arrivals')
            self.assertEqual(mount['mountPath'], '/evaluation/.chill-arrivals.json')
            self.assertTrue(mount['readOnly'])
            volume = next(volume for volume in pod['volumes'] if volume['name'] == mount['name'])
            self.assertEqual(volume['persistentVolumeClaim'], {'claimName': 'run-workspace', 'readOnly': True})
            self.assertLess(sum(len(arg.encode()) + 1 for arg in container['command'] + container['args']), 120000)
            policy['offsetsFile'] = str(root / mount['subPath'])
            offsets, _ = producer.arrival_trace(policy, 'poisson', 202)
            self.assertEqual(offsets, scenario['traceOffsets'][NAME])
            policy['arrivalSHA256'] = '0' * 64
            with self.assertRaisesRegex(ValueError, 'arrival generation differs'):
                producer.arrival_trace(policy, 'poisson', 202)
            Path(policy['offsetsFile']).write_text('[]')
            with self.assertRaisesRegex(ValueError, 'arrival file SHA differs'):
                producer.arrival_trace(policy, 'poisson', 202)

    def test_materialized_dataset_actual_bytes_are_producer_evidence(self):
        producer = input_snapshot.module('producer_test', evaluation.HERE / 'producer.py')
        with tempfile.TemporaryDirectory() as directory:
            data = Path(directory)
            for record, content in (('02', b'second'), ('01', b'first')):
                (data / record).mkdir()
                (data / record / 'input').write_bytes(content)
            records = cli.load_records(data, ['input'])
            offsets = [1, 2]
            trace_path = data / '.chill-arrivals.json'
            evidence_io.save(trace_path, offsets)
            policy = {'segments': [{'rate': 1, 'seconds': 1}], 'offsetsFile': str(trace_path),
                      'offsetsFileSHA256': evidence_io.ref(trace_path)['sha256'], 'offered': 2,
                      'arrivalSHA256': hashlib.sha256(json.dumps(offsets, separators=(',', ':')).encode()).hexdigest(),
                      'datasetRef': {'name': 'records', 'uid': 'dataset'}, 'shards': 1, 'barrierSeconds': 10}
            encoded = base64.b64encode(zlib.compress(json.dumps(policy).encode())).decode()
            observed = []
            def replay(records, offsets, duration, binding, kw):
                observed.append(copy.deepcopy(binding))
            def cli_main(args):
                cli.replay(records, arrivals='poisson', seed=202)
                return 0
            with patch.object(sys, 'argv', ['producer.py', encoded, 'replay']), patch.object(cli, 'main', side_effect=cli_main), patch.object(cli, 'replay'), patch.object(producer, 'replay_shards', side_effect=replay):
                self.assertEqual(producer.main(), 0)
            actual = observed[0]['payload']
            self.assertEqual([r['recordID'] for r in actual], ['01', '02'])
            self.assertEqual(actual[0]['fields']['input'], {'bytes': 5, 'sha256': hashlib.sha256(b'first').hexdigest()})

    def test_prepare_freezes_dataset_carrier_without_host_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, config, _ = fixture(root)
            scenario['spec']['traceRefs'] = {NAME: {'store': 'store', 'key': 'trace.json', 'size': 9}}
            execution = {key: value for key, value in execution.items() if key not in ('stageUID', 'stageID', 'stageName')}
            execution.update(runName='run', childRefs=[])
            spec.update(target={'components': []}, evidenceStore='store', stages=[{'id': 'one', 'row': 'e5-test', 'loadScenarioRef': {'name': 'load', 'uid': 'load-uid'}}])
            specdir = root / 'spec'
            for name, value in (('spec', spec), ('execution', execution), ('config', config)):
                evidence_io.save(specdir / (name + '.json'), value)
            uploads = []
            store = SimpleNamespace(put=lambda key, path: uploads.append(key) or {'store': 'store', 'key': key, 'size': Path(path).stat().st_size},
                                    get=lambda reference, path: evidence_io.save(path, [1, 2, 3]))
            harness = evaluation.Harness(root / 'campaign/run', specdir, store)
            harness.run.mkdir(parents=True)
            objects = {
                'loadscenario': {'metadata': {'name': 'load', 'uid': 'load-uid'}, 'spec': scenario['spec']},
                'dataset': scenario['datasets'][NAME],
                'workload': {'metadata': {'uid': 'workload'}, 'spec': {'preprocessorRef': {'name': 'preprocessor'}}},
                'preprocessor': {'spec': {'input': {'fields': [{'name': 'input'}]}}},
                'pods': {'items': [{'metadata': {'namespace': 'tenant', 'name': 'resident', 'uid': 'resident-uid'},
                                    'spec': {'nodeName': 'genesis1'}, 'status': {'phase': 'Pending'}}]},
                'jobs': {'items': []},
                'pod': {'spec': {'volumes': [{'name': 'work', 'persistentVolumeClaim': {'claimName': 'workspace'}}],
                                 'containers': [{'volumeMounts': [{'name': 'work', 'mountPath': str(root)}]}]}},
            }
            def k(namespace, verb, kind, *args):
                if kind == 'node':
                    return {'metadata': {'name': args[0], 'uid': next((node['uid'] for node in spec['powerMeasurement']['nodes'] if node['name'] == args[0]), 'generator-node')},
                            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}
                return copy.deepcopy(objects[kind])
            with patch.object(input_snapshot, 'kubectl', side_effect=k), patch.object(input_snapshot, 'api', return_value={'appliedDefinition': {**scenario['workloads'][0], 'generation': 2}}), patch.dict(os.environ, CHILL_HARNESS_SOURCE_SHA='different-image-revision', HOSTNAME='harness-pod'):
                harness.prepare()
            snapshot = evidence_io.load(harness.inputs / 'snapshot.json')
            self.assertEqual(snapshot['schemaVersion'], 'evaluation-snapshot.v2alpha1')
            self.assertEqual(snapshot['inputsVersion'], 'evaluation-inputs.v2alpha1')
            self.assertEqual(snapshot['environmentStart'], evidence_io.load(harness.root / 'environment-start.json'))
            self.assertEqual([pod['uid'] for pod in snapshot['environmentStart']['pods']], ['resident-uid'])
            self.assertEqual(snapshot['bindings']['one']['config']['traceVolume'],
                             {'claimName': 'workspace', 'mountPath': str(root)})
            self.assertEqual(snapshot['bindings']['one']['config']['traceOffsets'], {NAME: [1, 2, 3]})
            frozen = snapshot['bindings']['one']['config']['workloads'][0]
            self.assertEqual(frozen['datasetRef'], {'name': 'records', 'uid': 'dataset'})
            self.assertEqual(frozen['dataset']['source']['path'], '/records')
            self.assertEqual(frozen['fields'], ['input'])
            self.assertEqual(frozen['generation'], 2)
            self.assertEqual(frozen['requestedGeneration'], 1)
            self.assertEqual(len(uploads), 1)

    def exercise_stage_policy(self, mode):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, _, config = fixture(root)
            config['stages'][0]['parts'][0]['seconds'] = .01
            config['traceOffsets'] = {NAME: [1]}
            if mode in ('slow-reducer', 'slow-status'):
                config['traceOffsets'][NAME] = [1] * 134156
            if mode == 'late-watchdog-rows':
                config['traceOffsets'][NAME] = [1, 2, 3]
            config['traceVolume'] = {'claimName': 'workspace', 'mountPath': str(root)}
            config['timeboxes'] = dict.fromkeys(config['timeboxes'], 5)
            config['timeboxes']['flushSeconds'] = .001
            config['timeboxes']['drainSeconds'] = .01
            if mode in ('watchdog', 'late-reader-error', 'late-watchdog-rows', 'late-watchdog-drained'):
                config['timeboxes'].update(drainSeconds=.01, collectSeconds=.01)
            path = root / 'config.json'
            evidence_io.save(path, config)
            controller = object.__new__(conductor.CPO)
            controller.b = conductor.bind(path)
            controller.b['sources']['producer.py']['sha256'] = '0' * 64
            controller.c, controller.out, controller.tb = config, root, config['timeboxes']
            controller.residuals = []
            controller.ws = {w['name']: w for w in config['workloads']}
            controller.a = SimpleNamespace(stage='e5-test')
            controller.monitor = lambda *args: 0
            go = []
            controller.go_callback = go.append
            controller.provisional_callback = None
            signals = []
            controller.progress_callback = lambda phase=None, at=None, **facts: signals.append({'phase': phase, 'at': at, **facts})
            publications = []
            controller.progress_publish_callback = lambda: publications.append(len(signals))
            ready = {'evaluationEvent': 'ready', 'at': START, 'barrierSeconds': 10000000000,
                     'offered': len(config['traceOffsets'][NAME]), 'arrivalSHA256': controller.b['schedule'][0]['traces'][NAME]['arrivalSHA256'],
                     'payload': [{'recordID': 'one', 'fields': {'input': {'bytes': 1, 'sha256': 'a' * 64}}}],
                     'datasetRef': {'name': 'different-provenance', 'uid': 'different'}, 'sourceSHA': 'different-revision'}
            # Hold the load window open until provisional D1 actually ran, so the
            # guard under test (not read batching) decides drain-time behavior.
            wait_for_poll = ('from pathlib import Path\nimport time\nwhile not Path(' + repr(str(root / 'poll-started')) + ').exists(): time.sleep(.01)\n'
                             if mode in ('slow-reducer', 'drain-progress') else '')
            tail = ('import time;time.sleep(60)\n' if mode in ('watchdog', 'failed-job', 'late-reader-error') else
                    'sys.stdout.write(\'{"partial":\');sys.stdout.flush();sys.exit(7)\n' if mode == 'eof' else 'print(json.dumps({"evaluationEvent":"drained","at":' + repr(END) + '}),flush=True)\n')
            if mode == 'hot-log':
                tail = 'sys.stdout.write(\'{"diagnostic":1}\\n\'*50000);sys.stdout.flush()\n' + tail
            if mode in ('slow-reducer', 'slow-status'):
                tail = ('for index in range(134156):\n'
                        ' print(json.dumps({"requestIndex":index,"scheduledOffsetNs":1,"finishedMonotonicNS":2,"padding":"x"*540}),flush=True)\n'
                        'print(json.dumps({"evaluationEvent":"drained"}),flush=True)\n'
                        'from pathlib import Path\nPath(' + repr(str(root / 'producer-finished')) + ').touch()\n')
            if mode in ('late-watchdog-rows', 'late-watchdog-drained'):
                tail = ('sys.stdin.readline()\n'
                        'print(json.dumps({"requestIndex":0,"scheduledOffsetNs":1,"finishedMonotonicNS":2}),flush=True)\n'
                        + ('print(json.dumps({"evaluationEvent":"drained"}),flush=True)\n' if mode == 'late-watchdog-drained' else '')
                        + 'from pathlib import Path\nPath(' + repr(str(root / 'tail-emitted')) + ').touch()\n'
                        'import time;time.sleep(60)\n')
            script = ('import sys,json\nprint(' + repr(json.dumps(ready)) + ',flush=True)\nsys.stdin.readline()\n'
                      'print(json.dumps({"evaluationEvent":"start","at":' + repr(START) + ',"monotonicNS":1}),flush=True)\n'
                      + wait_for_poll +
                      'print(json.dumps({"evaluationEvent":"window-end","at":' + repr(END) + '}),flush=True)\n' + tail)
            pod = {'metadata': {'name': 'producer', 'uid': 'pod', 'ownerReferences': [{'kind': 'Job', 'uid': 'job'}]},
                   'status': {'phase': 'Running', 'containerStatuses': [
                       {'name': 'load', 'containerID': 'container', 'imageID': spec['loadGenerator']['image'], 'restartCount': 0, 'state': {'running': {'startedAt': START}}}]}}
            process = [None]
            jobs, job_polls = [], []
            original_popen = subprocess.Popen
            def launch(*args, **kwargs):
                process[0] = original_popen([sys.executable, '-u', '-c', script], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=kwargs['stderr'], bufsize=0)
                return process[0]
            def k(args, stdin=None, **kwargs):
                if args[:2] == ['get', 'jobs']:
                    return {'items': []}
                if args[0] == 'create':
                    job = json.loads(stdin)
                    job['metadata']['uid'] = 'job'
                    jobs.append(job)
                    return job
                if args[:2] == ['get', 'pods']:
                    return {'items': [pod]}
                if args[:2] == ['get', 'pod']:
                    return pod
                if args[:2] == ['get', 'job']:
                    job_polls.append(conductor.time.monotonic())
                    terminal = 'Failed' if mode == 'failed-job' and go else 'Complete'
                    return {**jobs[0], 'status': {'conditions': [{'type': terminal, 'status': 'True'}]}}
                if args[0] == 'exec':
                    process[0].stdin.write(b'release\n')
                    process[0].stdin.flush()
                    return {}
                raise AssertionError(args)
            controller.io = SimpleNamespace(k=k, delete_job=lambda *args: None)
            if mode in ('late-watchdog-rows', 'late-watchdog-drained'):
                def cleanup_after_last_read():
                    process[0].stdin.write(b'tail\n')
                    process[0].stdin.flush()
                    deadline = time.monotonic() + 5
                    while not (root / 'tail-emitted').exists() and time.monotonic() < deadline:
                        time.sleep(.01)
                    self.assertTrue((root / 'tail-emitted').exists())
                controller._cleanup_jobs = cleanup_after_last_read
            def missing_samples(*args):
                raise RuntimeError('diagnostic counters unavailable')
            telemetry = SimpleNamespace(poll=missing_samples)
            original_module = evidence_io.module
            auth = original_module('test_auth', conductor.HERE / 'auth.py')
            def modules(name, path):
                if name == 'evaluation_auth':
                    return SimpleNamespace(lifetime_budget=auth.lifetime_budget, check=lambda *args, **kwargs: {'accepted': True})
                if name == 'evaluation_archive':
                    return SimpleNamespace(collect=lambda *args: {'status': 'CAPTURED_NOT_EVALUATED', 'raw': [{'bytes': 17}, {'bytes': 25}]})
                return original_module(name, path)
            evidence_io.save(root / 'settings-patch.json', {'accepted': True})
            clock = conductor.time.monotonic
            def slow_reducer(_phase):
                (root / 'poll-started').touch()
                # The reducer must not starve the independent evidence writer.
                deadline = time.monotonic() + 10
                while not (root / 'producer-finished').exists() and time.monotonic() < deadline:
                    time.sleep(.01)
                self.assertTrue((root / 'producer-finished').exists(), 'producer pipe blocked while reducer slept')
                time.sleep(.05)
                captured = (root / 'measured' / (NAME + '-client.jsonl')).read_bytes()
                self.assertGreater(len(captured), 80_000_000)
                self.assertTrue(captured.endswith(b'{"evaluationEvent": "drained"}\n'))
            blocked = []
            def slow_status(*args):
                publications.append(len(signals))
                if go and not blocked:
                    blocked.append({})
                    started = time.monotonic()
                    time.sleep(60)
                    captured = (root / 'measured' / (NAME + '-client.jsonl')).read_bytes()
                    blocked[0].update(statusBlockedSeconds=time.monotonic() - started,
                                      rowsCapturedBeforeStatusReturned=captured.count(b'"scheduledOffsetNs":'),
                                      capturedBytes=len(captured),
                                      drained=captured.endswith(b'{"evaluationEvent": "drained"}\n'))
                    print(json.dumps(blocked[0]))
            if mode == 'slow-status':
                harness = object.__new__(evaluation.Harness)
                harness.root, harness.progress_records = root, signals
                harness.execution = {'namespace': 'tenant', 'stageName': 'stage', 'stageUID': 'stage-uid'}
                controller.progress_callback = harness.progress
                controller.progress_publish_callback = harness.publish_progress
            original_capture = conductor.ProducerLog.capture
            def fail_after_stream_ends(capture, source):
                original_capture(capture, source)
                capture.error = OSError('late reader failure')
            with patch.object(conductor.subprocess, 'Popen', side_effect=launch), patch.object(conductor, 'ProducerResources', return_value=telemetry), patch.object(evidence_io, 'module', side_effect=modules), patch.object(conductor.time, 'monotonic', side_effect=(lambda: 100) if mode == 'hot-log' else clock):
                def drain_poll(_phase):
                    (root / 'poll-started').touch()
                    if any(s['phase'] == 'loadEnded' for s in signals):
                        time.sleep(1)  # RUN-3 in miniature: a reparse that would hold the loop after load ended
                boundary_seen, boundary_published = [], []
                if mode == 'drain-progress':
                    record, publish = controller.progress_callback, controller.progress_publish_callback
                    def timed_progress(phase=None, at=None, **facts):
                        record(phase, at, **facts)
                        if phase == 'drainStarted':
                            boundary_seen.append(time.monotonic())
                    def timed_publish():
                        publish()
                        if boundary_seen and not boundary_published:
                            boundary_published.append(time.monotonic())
                    controller.progress_callback, controller.progress_publish_callback = timed_progress, timed_publish
                slow = (patch.object(conductor.ProvisionalD1, 'poll', side_effect=slow_reducer) if mode == 'slow-reducer' else
                        patch.object(conductor.ProvisionalD1, 'poll', side_effect=drain_poll) if mode == 'drain-progress' else
                        patch.object(conductor.ProvisionalD1, 'poll', return_value=None))
                with slow:
                    late = patch.object(conductor.ProducerLog, 'capture', new=fail_after_stream_ends) if mode == 'late-reader-error' else patch.object(controller, 'monitor', controller.monitor)
                    status = patch.object(input_snapshot, 'kubectl', side_effect=slow_status) if mode == 'slow-status' else nullcontext()
                    with late, status:
                        result = self.finish_stage_policy(controller, mode, process, root, go, jobs, job_polls, signals, publications)
                    if mode == 'drain-progress':
                        # Consumer view (RUN-3): drainStarted reaches a status publication without waiting out a reparse.
                        self.assertLess(boundary_published[0] - boundary_seen[0], .5, 'drain boundary publication waited for provisional D1')
                    if mode == 'slow-status':
                        self.assertEqual(len(blocked), 1, 'the actual status publisher must block once after GO')
                        self.assertGreaterEqual(blocked[0]['statusBlockedSeconds'], 60)
                        self.assertEqual(blocked[0]['rowsCapturedBeforeStatusReturned'], 134156)
                        self.assertGreater(blocked[0]['capturedBytes'], 80_000_000)
                        self.assertTrue(blocked[0]['drained'])
                    return result

    def finish_stage_policy(self, controller, mode, process, root, go, jobs, job_polls, signals, publications):
        if mode in ('failed-job', 'late-reader-error'):
            try:
                message = 'ProducerFailed: load-' if mode == 'failed-job' else 'ProducerCaptureFailed:.*late reader failure'
                with self.assertRaisesRegex(RuntimeError, message):
                    controller.run()
            finally:
                if process[0]:
                    process[0].stdin.close()
            phase = evidence_io.load(root / 'measured/phase.json')
            self.assertEqual(phase['status'], 'STOP')
            if mode == 'failed-job':
                self.assertIn(jobs[0]['metadata']['name'], phase['error'])
            else:
                self.assertIn('late reader failure', str(phase['captureErrors']))
            self.assertTrue(go, 'failure must be observed after GO')
            return
        controller.run()
        process[0].stdin.close()
        phase = evidence_io.load(root / 'measured/phase.json')
        self.assertEqual(phase['status'], 'DRAINED_NOT_EVALUATED' if mode in ('normal', 'hot-log', 'slow-reducer', 'slow-status', 'drain-progress') else 'PARTIAL_DRAIN_NOT_EVALUATED')
        self.assertEqual(set(phase['starts']), {NAME})
        self.assertTrue(phase['diagnosticErrors'])
        self.assertTrue((root / 'measured/collected.json').exists())
        self.assertFalse((root / 'measured' / (NAME + '-client-end-counter.json')).exists())
        if mode == 'hot-log':
            self.assertLessEqual(len(job_polls), 2, 'hot log reads must not bypass the configured producer polling interval')
        if mode in ('slow-reducer', 'slow-status'):
            self.assertEqual(phase['producerCapture'][NAME], {'plannedRequests': 134156, 'receivedRequestRows': 134156, 'drained': True, 'collectionLoss': 0})
        if mode in ('late-watchdog-rows', 'late-watchdog-drained'):
            capture = phase['producerCapture'][NAME]
            self.assertEqual(capture['receivedRequestRows'], 1)
            self.assertEqual(capture['drained'], mode == 'late-watchdog-drained')
            self.assertEqual(capture['collectionLoss'], 2 if mode == 'late-watchdog-rows' else 0)
            disk = [json.loads(line) for line in (root / 'measured' / (NAME + '-client.jsonl')).read_bytes().splitlines()]
            self.assertEqual(sum('scheduledOffsetNs' in row for row in disk), capture['receivedRequestRows'])
        boundaries = [signal for signal in signals if signal['phase']]
        self.assertEqual([signal['phase'] for signal in boundaries],
                         ['loadStarted', 'loadEnded', 'drainStarted', 'drainEnded', 'flushStarted', 'collectStarted', 'collectEnded'])
        self.assertEqual(boundaries[0]['at'], START)
        self.assertEqual(boundaries[1]['at'], END)
        expected_missing = (0 if mode in ('normal', 'hot-log', 'slow-reducer', 'slow-status', 'drain-progress') else 3 if mode == 'late-watchdog-rows' else 1)
        self.assertEqual(boundaries[3]['producerRowsMissing'], {NAME: expected_missing})
        self.assertEqual(boundaries[-1]['bytes'], 42)
        self.assertEqual(len(publications) - 4, len(job_polls) - (2 if mode in ('normal', 'hot-log', 'slow-reducer', 'slow-status', 'drain-progress') else 1))
        self.assertEqual(publications[-4:], [4, 5, 6, 7], 'post-ingestion boundaries publish once each')

    def test_stop_cleanup_tail_is_in_final_phase_without_replacing_primary_error(self):
        def check(controller, mode, process, root, go, jobs, job_polls, signals, publications):
            original = controller.io.k
            def failed_after_go(args, *a, **kw):
                result = original(args, *a, **kw)
                if args[:2] == ['get', 'job'] and go:
                    result['status'] = {'conditions': [{'type': 'Failed', 'status': 'True'}]}
                return result
            controller.io.k = failed_after_go
            controller.io.delete_job = lambda *args: controller._cleanup_jobs()
            try:
                with self.assertRaisesRegex(RuntimeError, 'ProducerFailed:'):
                    controller.run()
            finally:
                if process[0]:
                    process[0].stdin.close()
            phase = evidence_io.load(root / 'measured/phase.json')
            disk = [json.loads(line) for line in (root / 'measured' / (NAME + '-client.jsonl')).read_bytes().splitlines()]
            self.assertEqual(sum('scheduledOffsetNs' in row for row in disk), 1)
            self.assertEqual(phase['producerCapture'][NAME]['receivedRequestRows'], 1)
            self.assertEqual(phase['producerCapture'][NAME]['collectionLoss'], 2)
            self.assertEqual(phase['status'], 'STOP')
            self.assertTrue(phase['error'].startswith('ProducerFailed:'))
            print(json.dumps({'scenario': 'STOP cleanup tail', 'diskRows': 1,
                              'phaseCapture': phase['producerCapture'][NAME], 'primaryError': phase['error']}))
        with patch.object(self, 'finish_stage_policy', side_effect=check):
            self.exercise_stage_policy('late-watchdog-rows')

    def test_capture_writer_preserves_disk_reserve_during_slow_reducer(self):
        def check(controller, mode, process, root, go, jobs, job_polls, signals, publications):
            path = root / 'measured' / (NAME + '-client.jsonl')
            headroom = 1024 * 1024
            minimum = controller.c['evaluation']['minimumFreeBytes']
            def available(_):
                size = path.stat().st_size if path.exists() else 0
                return SimpleNamespace(f_bavail=max(0, minimum + headroom - size), f_frsize=1)
            controller.monitor = conductor.CPO.monitor.__get__(controller)
            try:
                def slow_poll(_):
                    (root / 'poll-started').touch()  # releases the fixture's load-window hold
                    time.sleep(.2)
                with patch.object(conductor.os, 'statvfs', side_effect=available), patch.object(conductor.ProvisionalD1, 'poll', side_effect=slow_poll):
                    with self.assertRaisesRegex(RuntimeError, 'EvidenceCapacityInsufficient'):
                        controller.run()
            finally:
                if process[0]:
                    process[0].stdin.close()
            self.assertLessEqual(path.stat().st_size, headroom)
            phase = evidence_io.load(root / 'measured/phase.json')
            disk = [json.loads(line) for line in path.read_bytes().splitlines()]
            rows = sum('scheduledOffsetNs' in row for row in disk)
            capture = phase['producerCapture'][NAME]
            self.assertEqual(capture['receivedRequestRows'], rows)
            self.assertEqual(capture['collectionLoss'], 134156 - rows)
            self.assertGreater(capture['collectionLoss'], 0)
            self.assertIn('EvidenceCapacityInsufficient', str(phase['captureErrors']))
            print(json.dumps({'scenario': 'writer reserve', 'headroom': headroom,
                              'spooledBytes': path.stat().st_size, 'phaseCapture': capture,
                              'captureErrors': phase['captureErrors']}))
        with patch.object(self, 'finish_stage_policy', side_effect=check):
            self.exercise_stage_policy('slow-reducer')

    def test_optional_pre_go_publisher_does_not_consume_ready_barrier(self):
        def check(controller, mode, process, root, go, jobs, job_polls, signals, publications):
            once = []
            def delayed_status():
                if not once:
                    once.append(True)
                    time.sleep(1.1)
            controller.progress_publish_callback = delayed_status
            try:
                controller.run()
            finally:
                if process[0]:
                    process[0].stdin.close()
            self.assertTrue(go)
            print(json.dumps({'scenario': 'pre-GO progress', 'barrierSeconds': .8,
                              'publisherDelaySeconds': 1.1, 'goEvents': len(go)}))
        original = json.dumps
        def finite_ready(value, *args, **kwargs):
            if isinstance(value, dict) and value.get('evaluationEvent') == 'ready':
                value = dict(value, at=evidence_io.now(), barrierSeconds=.8)
            return original(value, *args, **kwargs)
        with patch.object(self, 'finish_stage_policy', side_effect=check), patch.object(json, 'dumps', side_effect=finite_ready):
            self.exercise_stage_policy('normal')

    def test_status_publish_blocked_60_seconds_does_not_block_producer_capture(self):
        self.exercise_stage_policy('slow-status')


    def test_drain_boundaries_publish_without_provisional_reparse(self):
        self.exercise_stage_policy('drain-progress')
    def test_missing_cpu_samples_and_provenance_labels_do_not_gate_go_or_drain(self):
        self.exercise_stage_policy('normal')

    def test_producer_eof_preserves_partial_collection(self):
        self.exercise_stage_policy('eof')

    def test_drain_watchdog_stops_owned_writers_and_collects(self):
        self.exercise_stage_policy('watchdog')

    def test_failed_producer_job_fails_stage_without_waiting_for_log_eof(self):
        self.exercise_stage_policy('failed-job')

    def test_hot_log_stream_does_not_bypass_producer_poll_interval(self):
        self.exercise_stage_policy('hot-log')

    def test_slow_reducer_keeps_80mb_producer_evidence_and_drained(self):
        self.exercise_stage_policy('slow-reducer')

    def test_late_capture_failure_during_watchdog_shutdown_is_recorded(self):
        self.exercise_stage_policy('late-reader-error')

    def test_watchdog_phase_counts_include_disk_tail_after_last_poll(self):
        for mode in ('late-watchdog-rows', 'late-watchdog-drained'):
            with self.subTest(mode=mode):
                self.exercise_stage_policy(mode)

    def test_cleanup_never_deletes_harness_or_unknown_job_uid(self):
        with tempfile.TemporaryDirectory() as directory:
            controller = object.__new__(conductor.CPO)
            controller.out = Path(directory)
            controller.c = {'evaluation': {'execution': {'runUID': 'run', 'stageUID': 'stage', 'stageID': 'one'}}}
            controller.tb, controller.residuals = {'drainSeconds': 1}, []
            job = {'kind': 'Job', 'metadata': {'name': 'load-one', 'uid': 'unknown', 'labels': {'edge.dacs.io/evaluation-run-uid': 'run'},
                                             'ownerReferences': [{'kind': 'EvaluationStage', 'uid': 'stage'}]}}
            calls = []
            def k(args):
                calls.append(args)
                return {'items': [job]}
            controller.io = SimpleNamespace(k=k, delete_job=lambda *args: self.fail('Unknown UID deleted'))
            with self.assertRaisesRegex(RuntimeError, 'RemoteEffectUnknown'):
                controller.cleanup_jobs()
            self.assertIn('edge.dacs.io/workload-role=load-generator', calls[0][3])

    def test_job_disappearance_does_not_hide_owned_pod_residuals(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            controller = object.__new__(conductor.CPO)
            controller.out = root
            controller.c = {'evaluation': {'execution': {'runUID': 'run', 'stageUID': 'stage', 'stageID': 'one'}}}
            controller.tb, controller.residuals = {'drainSeconds': 0}, []
            job = {'kind': 'Job', 'metadata': {'name': 'load-one', 'uid': 'job', 'labels': {'edge.dacs.io/evaluation-run-uid': 'run'},
                                             'ownerReferences': [{'kind': 'EvaluationStage', 'uid': 'stage'}]}}
            evidence_io.save(root / 'measured/created-one.json', job)
            pod = {'metadata': {'name': 'residual', 'uid': 'pod', 'ownerReferences': [{'kind': 'Job', 'uid': 'job'}]}}
            controller.io = SimpleNamespace(k=lambda args: {'items': [] if args[1] == 'jobs' else [pod]})
            with self.assertRaisesRegex(RuntimeError, 'ResidualState'):
                controller.cleanup_jobs()
            self.assertEqual(controller.residuals, [{'kind': 'Pod', 'name': 'residual', 'reason': 'ResidualState'}])

    def test_cleanup_and_restore_failures_are_independent_and_finish_is_idempotent(self):
        for cleanup_failure, restore_failure in ((False, False), (True, False), (True, True), (False, True)):
            with self.subTest(cleanup_failure=cleanup_failure, restore_failure=restore_failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                api = SettingsAPI()
                receipt_path = root / 'stages/one/settings-patch.json'
                settings_patch.factor_apply(api, {'controls': {'power': False}}, receipt_path)
                api.value['controls']['routing'] = False  # unrelated operator change
                api.revision += 1
                api.fail_put = restore_failure
                controller = object.__new__(conductor.CPO)
                controller.out = root
                controller.c = {'evaluation': {'execution': {'runUID': 'run', 'childRefs': [{'uid': 'stage', 'stageID': 'one'}]}}}
                controller.tb, controller.residuals = {'drainSeconds': 0}, []
                job = {'kind': 'Job', 'metadata': {'name': 'load-one', 'uid': 'job',
                       'labels': {'edge.dacs.io/evaluation-run-uid': 'run'},
                       'ownerReferences': [{'kind': 'EvaluationStage', 'uid': 'stage'}]}}
                evidence_io.save(root / 'stages/one/measured/created-one.json', job)
                jobs, order = [job], []
                def delete(name, uid):
                    order.append('delete')
                    if cleanup_failure:
                        raise RuntimeError('ToolFailed: delete denied')
                    jobs.clear()
                def settings(*args, **kwargs):
                    order.append('settings-' + args[0])
                    return api(*args, **kwargs)
                controller.api = settings
                controller.io = SimpleNamespace(k=lambda args: {'items': list(jobs) if args[1] == 'jobs' else []},
                                                delete_job=delete)
                harness = object.__new__(evaluation.Harness)
                harness.run = harness.root = root
                harness.scope, harness.prefix = 'run', 'experiments/run/run'
                harness.spec = {}
                harness.execution = {'runUID': 'run', 'namespace': 'evaluation-ns', 'prepareCompleted': True}
                harness.controller = lambda: controller
                harness.upload = lambda path, key: {'store': 'test', 'key': key, 'size': path.stat().st_size}
                with patch('builtins.print'), patch.object(input_snapshot, 'open_snapshot'), patch.object(live, 'start_watcher'), patch.object(live, 'stop_watcher'):
                    self.assertEqual(harness.execute('finish'), int(cleanup_failure or restore_failure))
                result = evidence_io.load(root / 'results/run/finish.json')
                self.assertEqual(result['cleanup']['status'], 'Failed' if cleanup_failure or restore_failure else 'Complete')
                self.assertEqual(order[0], 'delete')
                self.assertIn('settings-GET', order)
                evidence = evidence_io.load(root / 'cleanup-outcome.json')['attempts']
                self.assertEqual(evidence[0]['cleanup']['status'], 'Failed' if cleanup_failure else 'Complete')
                self.assertEqual(evidence[0]['restore']['status'], 'Failed' if restore_failure else 'Complete')
                self.assertEqual(api.value['controls']['power'], not restore_failure)
                self.assertFalse(api.value['controls']['routing'])
                if cleanup_failure:
                    self.assertIn('delete denied', evidence[0]['cleanup']['error'])
                    self.assertIn('delete denied', result['execution']['message'])
                    self.assertTrue(any(residual['kind'] == 'Job' for residual in result['residuals']))
                if restore_failure:
                    self.assertIn('Forbidden', evidence[0]['restore']['receipts'][0]['error'])
                    self.assertTrue(any(residual['kind'] == 'SchedulerSettings' for residual in result['residuals']))
                else:
                    reverted_at = evidence_io.load(receipt_path)['revertedAt']
                    calls_before_retry = len(api.calls)
                    api.value['controls']['power'] = False  # operator changed it after our successful revert
                    api.revision += 1
                    controller.residuals = []  # a fresh finish Job has a fresh controller
                    with patch('builtins.print'), patch.object(input_snapshot, 'open_snapshot'), patch.object(live, 'start_watcher'), patch.object(live, 'stop_watcher'):
                        self.assertEqual(harness.execute('finish'), int(cleanup_failure))
                    self.assertEqual(len(api.calls), calls_before_retry)
                    self.assertFalse(api.value['controls']['power'])
                    self.assertEqual(evidence_io.load(receipt_path)['revertedAt'], reverted_at)
                    evidence = evidence_io.load(root / 'cleanup-outcome.json')['attempts']
                    self.assertEqual(len(evidence), 2)
                    self.assertEqual(evidence[0]['cleanup']['status'], 'Failed' if cleanup_failure else 'Complete')

    def test_finish_requires_observed_termination_on_ready_node_and_excludes_itself(self):
        for mode in ('clean', 'running', 'not-ready', 'unknown-node', 'no-termination', 'missing-pod', 'missing-both', 'run-stage',
                     'complete-no-termination', 'complete-not-ready'):
            with self.subTest(mode=mode):
                controller = object.__new__(conductor.CPO)
                stage_id = 'run' if mode == 'run-stage' else 'one'
                procedure = {'jobRef': {'name': 'old-stage', 'uid': 'old'}, 'podRef': {'name': 'old-stage-pod', 'uid': 'old-pod'}}
                controller.c = {'evaluation': {'execution': {'runUID': 'run', 'childRefs': [
                    {'uid': 'stage', 'stageID': stage_id, 'procedures': [procedure]}]}}}
                controller.tb, controller.residuals = {'drainSeconds': 0}, []
                controller.cleanup_jobs = lambda: None
                labels = {'edge.dacs.io/evaluation-run-uid': 'run', 'edge.dacs.io/evaluation-stage': stage_id}
                job = {'metadata': {'name': 'old-stage', 'uid': 'old', 'labels': labels,
                       'ownerReferences': [{'kind': 'EvaluationStage', 'uid': 'stage'}]},
                       'status': {'conditions': [] if mode == 'running' else [{'type': 'Complete' if mode.startswith('complete-') else 'Failed', 'status': 'True'}]}}
                pod = {'metadata': {'name': 'old-stage-pod', 'uid': 'old-pod', 'labels': labels,
                       'ownerReferences': [{'kind': 'Job', 'uid': 'old'}]}, 'spec': {'nodeName': 'node'},
                       'status': {'phase': 'Running' if mode == 'running' else 'Failed', 'containerStatuses': [
                           {'name': 'evaluation', 'state': {} if mode in ('running', 'no-termination', 'complete-no-termination') else {'terminated': {'exitCode': 1}}}]}}
                finish = {'metadata': {'name': 'self-finish', 'uid': 'self', 'labels': {**labels, 'edge.dacs.io/evaluation-stage': 'run'},
                          'ownerReferences': [{'kind': 'EvaluationRun', 'uid': 'run'}]}}
                own_pod = {'metadata': {'name': 'self-pod', 'uid': 'self-pod', 'labels': finish['metadata']['labels'],
                           'ownerReferences': [{'kind': 'Job', 'uid': 'self'}]}, 'status': {'phase': 'Running'}}
                def k(args):
                    if args[1] == 'node':
                        ready = {'not-ready': 'False', 'unknown-node': 'Unknown', 'complete-not-ready': 'False'}.get(mode, 'True')
                        return {'status': {'conditions': [{'type': 'Ready', 'status': ready}]}}
                    if mode == 'missing-both':
                        return {'items': [finish] if args[1] == 'jobs' else [own_pod]}
                    return {'items': [job, finish] if args[1] == 'jobs' else ([own_pod] if mode == 'missing-pod' else [pod, own_pod])}
                controller.io = SimpleNamespace(k=k)
                if mode in ('clean', 'run-stage'):
                    controller.finish()
                    self.assertEqual(controller.residuals, [])
                else:
                    with self.assertRaisesRegex(RuntimeError, 'ResidualState'):
                        controller.finish()
                    self.assertTrue(any(r['kind'] == 'Pod' and r['name'] == 'old-stage-pod' for r in controller.residuals))

    def test_finish_uses_successful_job_evidence_when_pod_is_gone(self):
        complete = {'conditions': [{'type': 'Complete', 'status': 'True'}], 'active': 0, 'succeeded': 1}
        cases = [('complete', complete, True),
                 ('complete-absent-counters', {'conditions': complete['conditions']}, True),
                 ('succeeded', {'succeeded': 1, 'active': 0, 'terminating': 0}, True),
                 ('failed', {'conditions': [{'type': 'Failed', 'status': 'True'}]}, False),
                 ('running', {'active': 1}, False),
                 ('complete-active', {**complete, 'active': 1}, False),
                 ('complete-terminating', {**complete, 'terminating': 1}, False)]
        for name, status, clean in cases:
            for recorded_pod in (False, True):
                with self.subTest(case=name, recorded_pod=recorded_pod), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    procedure = {'jobRef': {'name': 'old-factor', 'uid': 'job'}}
                    if recorded_pod:
                        procedure['podRef'] = {'name': 'collected-pod', 'uid': 'pod'}
                    execution = {'runUID': 'run', 'prepareCompleted': True, 'childRefs': [
                        {'uid': 'stage', 'stageID': 'one', 'procedures': [procedure]}]}
                    controller = object.__new__(conductor.CPO)
                    controller.c = {'evaluation': {'execution': execution}}
                    controller.tb, controller.residuals = {'drainSeconds': 0}, []
                    controller.cleanup_jobs = lambda: None
                    job = {'metadata': {'name': 'old-factor', 'uid': 'job',
                           'labels': {'edge.dacs.io/evaluation-run-uid': 'run'},
                           'ownerReferences': [{'kind': 'EvaluationStage', 'uid': 'stage'}]}, 'status': status}
                    controller.io = SimpleNamespace(k=lambda args: {'items': [job] if args[1] == 'jobs' else []})
                    harness = object.__new__(evaluation.Harness)
                    harness.run = harness.root = root
                    harness.scope, harness.prefix = 'run', 'experiments/run/run'
                    harness.spec, harness.execution = {}, execution
                    harness.controller = lambda: controller
                    harness.upload = lambda path, key: {'store': 'test', 'key': key, 'size': path.stat().st_size}
                    with patch('builtins.print'), patch.object(input_snapshot, 'open_snapshot'), patch.object(live, 'start_watcher'), patch.object(live, 'stop_watcher'):
                        self.assertEqual(harness.execute('finish'), 0 if clean else 1)
                    result = evidence_io.load(root / 'results/run/finish.json')
                    self.assertEqual(result['cleanup']['status'], 'Complete' if clean else 'Failed')
                    self.assertEqual(bool(result.get('residuals')), not clean)

    def test_unknown_injection_retains_pool_and_names_affected_node(self):
        controller = object.__new__(conductor.CPO)
        controller.c = {'evaluation': {'execution': {'runUID': 'run', 'childRefs': [
            {'uid': 'stage', 'stageID': 'one', 'uncertain': True, 'injectionRecords': [
                {'outcome': 'Unknown', 'nodeRef': {'name': 'cordoned-node', 'uid': 'node'}}]}]}}}
        controller.tb, controller.residuals = {'drainSeconds': 0}, []
        controller.cleanup_jobs = lambda: None
        controller.io = SimpleNamespace(k=lambda args: {'items': []})
        with self.assertRaisesRegex(RuntimeError, 'RemoteEffectUnknown'):
            controller.finish()
        self.assertEqual(controller.residuals, [{'kind': 'Node', 'name': 'cordoned-node', 'reason': 'RemoteEffectUnknown'}])

    def test_slo_failure_is_valid_without_internal_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, _, _, _, config = fixture(root)
            config['traceOffsets'] = {NAME: [1, 2]}
            path = root / 'config.json'
            evidence_io.save(path, config)
            bound = conductor.bind(path)
            evidence_io.save(root / 'binding.json', bound)
            phase = {'stage': bound['schedule'][0], 'startUTC': START, 'endUTC': END, 'drainedUTC': END,
                     'starts': {NAME: {'at': START, 'monotonicNS': 100}}, 'status': 'DRAINED_NOT_EVALUATED'}
            evidence_io.save(root / 'phase.json', phase)
            power = archive.meter_energy(power_window(), spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
            for name in ('power.json', 'power-drain-inclusive.json'):
                evidence_io.save(root / name, power)
            rows = [{'requestIndex': i, 'requestID': 'r' + str(i), 'scheduledOffsetNs': i + 1, 'scheduledMonotonicNS': i + 101,
                     'sentMonotonicNS': i + 101, 'finishedMonotonicNS': 3000000000, 'issued': True} for i in range(2)]
            (root / (NAME + '-client.jsonl')).write_text(''.join(json.dumps(row) + '\n' for row in rows))
            indexed = {config['workloads'][0]['uid']: {'errors': [], 'identities': [{'workloadUID': 'workload', 'definitionID': 'definition'}],
                                                     'summary': {'uniqueRequests': 2}, 'finals': {
                'r0': {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 2000000000},
                'r1': {'outcome': 'timeout', 'executionComplete': False, 'elapsedNanos': 2000000000}}}}
            original_module = evidence_io.module
            def modules(name, path):
                if name == 'evaluation_raw':
                    return SimpleNamespace(iter_request_indexes=lambda *args, **kwargs: indexed)
                return original_module(name, path)
            with patch.object(evidence_io, 'module', side_effect=modules):
                judgment = judgment_rules.judge(root / 'binding.json', root)
            self.assertEqual(judgment['schema'], 'e5-judgment.v3')
            self.assertEqual(judgment['status'], 'FAIL')
            self.assertEqual(judgment['requests'][NAME]['offered'], 2)
            self.assertEqual(judgment['requests'][NAME]['deadlineSuccess'], 0)
            self.assertEqual(judgment['requests'][NAME]['issuanceFidelity']['generatorQuality']['status'], 'OBSERVED')
            self.assertEqual(publication.verdict(judgment)['verdict'], 'FAIL')
            self.assertEqual(judgment['requests'][NAME]['substages'][0]['offered'], 2)
            self.assertEqual(judgment['requests'][NAME]['definitionID'], 'definition')
            cutoff = '2026-11-03T00:00:31Z'
            live = judgment_rules.provisional_requests(config, phase, {NAME: rows}, indexed, cutoff, cutoff)
            self.assertNotIn('D1', live['workloads'][NAME])
            rows[1]['requestID'] = 'r0'
            (root / (NAME + '-client.jsonl')).write_text(''.join(json.dumps(row) + '\n' for row in rows))
            with patch.object(evidence_io, 'module', side_effect=modules):
                duplicated = judgment_rules.judge(root / 'binding.json', root)
            self.assertEqual(duplicated['status'], 'PARTIAL')
            self.assertIsNone(duplicated['requests'][NAME]['definitionID'])
            self.assertEqual(duplicated['requests'][NAME]['substages'][0]['D1'], 'NOT_EVALUATED')

    def test_provisional_scheduled_boundaries_as_of_missing_results_and_conflicts(self):
        with tempfile.TemporaryDirectory() as directory:
            _, _, _, _, config = fixture(Path(directory))
        config['workloads'] = [config['workloads'][0]]
        workload = config['workloads'][0]
        workload['slo'] = {'percentile': 100, 'targetLatencyNanos': 10}
        stage = {'seconds': 2, 'parts': [{'id': 'first', 'seconds': 1, 'rates': {NAME: 1}},
                                       {'id': 'second', 'seconds': 1, 'rates': {NAME: 1}}],
                 'traces': {NAME: {'offsetsNS': [0, 1000000000], 'offered': 2}}}
        phase = {'stage': stage, 'starts': {NAME: {'at': START, 'monotonicNS': 100}}}
        rows = [{'requestIndex': i, 'scheduledOffsetNs': offset, 'requestID': 'r' + str(i),
                 'sentMonotonicNS': 3000000100, 'issued': True} for i, offset in enumerate([0, 1000000000])]
        indexed = {workload['uid']: {'errors': [], 'identities': [], 'summary': {},
                    'finals': {'r0': {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 1},
                               'r1': {'outcome': 'error', 'elapsedNanos': 1}}}}
        def reduce(events, cutoff=END):
            return judgment_rules.provisional_requests(config, phase, {NAME: events}, indexed, cutoff, cutoff)
        early = reduce(rows, '2026-11-02T23:59:59.999999999Z')
        self.assertEqual(early['workloads'][NAME]['offered'], 0)
        self.assertEqual(early['workloads'][NAME]['unfinishedOrUnsent'], 0)
        self.assertEqual(early['generatorQuality'][NAME]['issued'], 0)
        closed = reduce([], START)
        self.assertEqual(closed['workloads'][NAME]['offered'], 1)
        self.assertEqual(closed['workloads'][NAME]['unfinishedOrUnsent'], 0)
        self.assertEqual(closed['workloads'][NAME]['collectionLoss'], 1)
        self.assertEqual(closed['generatorQuality'][NAME]['unissued'], 1)
        self.assertTrue(closed['workloads'][NAME]['reachable'])
        first = reduce(rows, '2026-11-03T00:00:00.999999999Z')
        self.assertEqual(first['workloads'][NAME]['offered'], 1)
        self.assertEqual(first['workloads'][NAME]['deadlineSuccess'], 1)
        self.assertEqual(first['generatorQuality'][NAME]['issued'], 1)
        self.assertEqual([part['offered'] for part in first['workloads'][NAME]['substages']], [1, 0])
        live = reduce(rows, '2026-11-03T00:00:01Z')
        self.assertEqual(live['workloads'][NAME]['offered'], 2)
        self.assertEqual(live['generatorQuality'][NAME]['sendsAfterWindow'], 2)
        self.assertFalse(live['workloads'][NAME]['reachable'])
        indexed[workload['uid']]['summary'] = {'finalConflicts': [{'requestID': 'r0'}]}
        self.assertEqual(reduce(rows)['workloads'][NAME]['deadlineSuccess'], 0)
        indexed[workload['uid']]['summary'] = {}
        rows[1]['requestID'] = 'r0'
        self.assertEqual(reduce(rows)['workloads'][NAME]['deadlineSuccess'], 0)
        stage['traces'][NAME] = {'offsetsNS': [], 'offered': 0}
        zero = reduce([])
        self.assertEqual(zero['workloads'][NAME]['offered'], 0)
        self.assertTrue(zero['workloads'][NAME]['reachable'])
        self.assertEqual(zero['generatorQuality'][NAME]['status'], 'NOT_OFFERED')

    def test_collection_loss_is_not_unfinished_or_success_in_final_and_provisional(self):
        with tempfile.TemporaryDirectory() as directory:
            _, _, _, _, config = fixture(Path(directory))
        workload = config['workloads'][0]
        workload['slo'] = {'percentile': 100, 'targetLatencyNanos': 10}
        phase = {'stage': {'seconds': 2, 'parts': [{'id': 'all', 'seconds': 2, 'rates': {NAME: 1}}],
                           'traces': {NAME: {'offsetsNS': [0, 1, 2], 'offered': 3}}},
                 'starts': {NAME: {'at': START, 'monotonicNS': 100}}}
        rows = [{'requestIndex': 0, 'scheduledOffsetNs': 0, 'requestID': 'one', 'issued': True},
                {'requestIndex': 1, 'scheduledOffsetNs': 1, 'issued': False, 'error': 'UnsentAtWindowEnd'}]
        indexes = {workload['uid']: {'errors': [], 'identities': [], 'summary': {},
                                     'finals': {'one': {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 1}}}}
        for drained in (False, True):
            stream = rows + ([{'evaluationEvent': 'drained'}] if drained else [])
            final = judgment_rules.reduce_requests(config, phase, {NAME: stream}, indexes)[NAME]
            provisional = judgment_rules.provisional_requests(config, phase, {NAME: stream}, indexes, END, END)['workloads'][NAME]
            self.assertEqual(final['collectionLoss'], 0 if drained else 1)
            self.assertEqual(final['unfinishedOrUnsent'], 2 if drained else 1)
            self.assertEqual((final['deadlineSuccess'], final['offered'], final['requiredSuccessfulRank'], final['D1']), (1, 3, 3, 'FAIL'))
            self.assertEqual(provisional['collectionLoss'], final['collectionLoss'])
            self.assertEqual(provisional['unfinishedOrUnsent'], final['unfinishedOrUnsent'])
            published = input_snapshot.module('capture_tables', conductor.HERE / 'tables.py').publication_requests({'requests': {NAME: final}})[NAME]
            self.assertEqual(published['collectionLoss'], final['collectionLoss'])

    def test_foreign_workload_row_is_set_aside_without_stopping_other_workloads(self):
        # Exact-match audit R15: one row naming another Workload used to abort every workload's judgment.
        with tempfile.TemporaryDirectory() as directory:
            _, _, _, _, config = fixture(Path(directory))
        first = config['workloads'][0]
        second = {**first, 'name': 'second', 'uid': 'second-uid'}
        config['workloads'] = [first, second]
        for workload in config['workloads']:
            workload['slo'] = {'percentile': 50, 'targetLatencyNanos': 10}
        traces = {name: {'offsetsNS': [0, 1], 'offered': 2} for name in (NAME, 'second')}
        phase = {'stage': {'seconds': 2, 'parts': [{'id': 'all', 'seconds': 2, 'rates': {NAME: 1, 'second': 1}}], 'traces': traces},
                 'starts': {name: {'at': START, 'monotonicNS': 100} for name in (NAME, 'second')}}
        def rows(prefix, uid):
            return [{'requestIndex': i, 'scheduledOffsetNs': i, 'requestID': prefix + str(i), 'workloadUID': uid,
                     'sentMonotonicNS': 100 + i, 'issued': True} for i in range(2)]
        clients = {NAME: rows('a', first['uid']), 'second': rows('b', 'second-uid')}
        clients[NAME][1]['workloadUID'] = 'someone-else'
        finals = {rid: {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 1}
                  for rid in ('a0', 'a1', 'b0', 'b1')}
        indexes = {uid: {'errors': [], 'identities': [], 'summary': {}, 'finals': finals} for uid in (first['uid'], 'second-uid')}
        result = judgment_rules.reduce_requests(config, phase, clients, indexes)
        mismatched, other = result[NAME], result['second']
        self.assertEqual(mismatched['identityMismatchClientRows'], 1)
        self.assertEqual((mismatched['offered'], mismatched['deadlineSuccess']), (2, 1), 'foreign row must not count as success')
        self.assertEqual(mismatched['collectionLoss'], 0, 'a received row is not collection loss')
        self.assertEqual(other['identityMismatchClientRows'], 0, 'always reported, 0 included')
        self.assertEqual((other['offered'], other['deadlineSuccess'], other['D1']), (2, 2, 'OBSERVED_THRESHOLD_MET'))
        published = input_snapshot.module('capture_tables', conductor.HERE / 'tables.py').publication_requests({'requests': result})
        self.assertEqual(published[NAME]['identityMismatchClientRows'], 1)

    def test_generator_index_diagnostics_do_not_discard_http_finals(self):
        with tempfile.TemporaryDirectory() as directory:
            _, _, _, _, config = fixture(Path(directory))
        config['workloads'] = [config['workloads'][0]]
        workload = config['workloads'][0]
        workload['slo'] = {'percentile': 100, 'targetLatencyNanos': 10}
        stage = {'seconds': 2, 'parts': [{'id': 'all', 'seconds': 2, 'rates': {NAME: 1}}],
                 'traces': {NAME: {'offsetsNS': [0, 1000000000], 'offered': 2}}}
        phase = {'stage': stage, 'starts': {NAME: {'monotonicNS': 100}}}
        indexed = {workload['uid']: {'errors': [], 'identities': [], 'summary': {},
                    'finals': {'r' + str(i): {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 1}
                               for i in range(2)}}}
        for anomaly in ('missing', 'duplicate', 'schedule'):
            rows = [{'requestIndex': i, 'requestID': 'r' + str(i), 'scheduledOffsetNs': offset,
                     'sentMonotonicNS': 100 + offset, 'issued': True} for i, offset in enumerate([0, 1000000000])]
            if anomaly == 'missing':
                rows[0].pop('requestIndex')
            elif anomaly == 'duplicate':
                rows[1]['requestIndex'] = 0
            else:
                rows[1]['scheduledOffsetNs'] += 1
            result = judgment_rules.reduce_requests(config, phase, {NAME: rows}, indexed)[NAME]
            self.assertEqual((result['offered'], result['deadlineSuccess'], result['requiredSuccessfulRank']), (2, 2, 2))
            self.assertEqual(result['completedLatencyNS']['samples'], 2)
            self.assertEqual(result['D1'], 'OBSERVED_THRESHOLD_MET')
            self.assertEqual(result['issuanceFidelity']['issued'], 2)
            self.assertGreater(result['issuanceFidelity']['invalidScheduleRows'], 0)

    def test_progress_boundaries_do_not_call_slow_failing_status_publisher(self):
        harness = object.__new__(evaluation.Harness)
        harness.progress_records = []
        controller = object.__new__(conductor.CPO)
        controller.progress_callback = harness.progress
        def slow_failure(*args):
            conductor.time.sleep(.01)
            raise OSError('status service unavailable')
        with patch.object(harness, 'status', side_effect=slow_failure) as publish, patch('builtins.print'):
            controller.progress('loadStarted', START)
            controller.progress('loadEnded', END)
            controller.progress('drainStarted', END)
            self.assertEqual(publish.call_count, 0, 'ingestion must never wait for status publication')
            self.assertEqual([row['phase'] for row in harness.progress_records], ['loadStarted', 'loadEnded', 'drainStarted'])

    def test_progress_heartbeat_replaces_full_history_after_failed_initial_publish(self):
        with tempfile.TemporaryDirectory() as directory:
            harness = object.__new__(evaluation.Harness)
            harness.root = Path(directory)
            harness.progress_records = []
            harness.spec = {}
            harness.execution = {'namespace': 'ns', 'stageName': 'stage', 'stageUID': 'stage-uid'}
            evidence_io.save(harness.root / 'measured/generator-barrier.json', {'accepted': True})
            status = {'provisionalD1': {'asOf': START}}
            attempts = []
            def kubectl(namespace, *args):
                self.assertEqual(namespace, 'ns')
                self.assertEqual(args[:3], ('patch', 'evaluationstage', 'stage'))
                operations = json.loads(args[-1])
                self.assertEqual(operations[0], {'op': 'test', 'path': '/metadata/uid', 'value': 'stage-uid'})
                attempts.append(operations)
                if len(attempts) == 1:
                    raise OSError('first status publication failed')
                for operation in operations[1:]:
                    self.assertNotIn('/-', operation['path'])
                    status[operation['path'][len('/status/'):]] = operation['value']
            with patch.object(input_snapshot, 'kubectl', side_effect=kubectl), patch('builtins.print') as log:
                harness.progress('loadStarted', START)
                harness.publish_progress()
                self.assertNotIn('progress', status)
                harness.progress('loadEnded', END)
                harness.publish_progress()
                self.assertEqual([row['phase'] for row in status['progress']], ['loadStarted', 'loadEnded'])
                harness.publish_progress()
                self.assertEqual(len(status['progress']), 2, 'repeated publication must not append duplicates')
                self.assertEqual(status['provisionalD1']['asOf'], START)
                self.assertTrue(status['lastProgressAt'])
                with patch.object(evaluation.judgment, 'judge', return_value={}), patch.object(evaluation.publication, 'verdict', return_value={'verdict': 'PASS'}):
                    harness.judge()
                self.assertEqual([row['phase'] for row in status['progress']], ['loadStarted', 'loadEnded', 'judgeStarted', 'judgeEnded'])
                self.assertEqual(evidence_io.load(harness.root / 'stage-progress.json'), status['progress'])
                boundaries = [json.loads(call.args[0].split(' ', 1)[1]) for call in log.call_args_list if call.args[0].startswith('EVALUATION_PROGRESS ')]
                self.assertEqual(boundaries, status['progress'])

    def test_provisional_second_poll_updates_and_partial_capture_preserves_it(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, _, _, _, config = fixture(root)
            config['timeboxes']['flushSeconds'] = 0
            published = []
            poller = conductor.ProvisionalD1(None, config, root, published.append)
            cutoff = [END]
            def capture(target, *args):
                target.mkdir(parents=True)
                return cutoff[0]
            poller.archive = SimpleNamespace(collect_requests_snapshot=capture)
            index = {w['uid']: {'errors': [], 'identities': [], 'summary': {}, 'finals': {}} for w in config['workloads']}
            released = []
            poller.raw = SimpleNamespace(iter_request_indexes=lambda *args, **kwargs: index, release_request_cache=lambda: released.append(True))
            stage = {'seconds': 1, 'parts': [{'id': 'p', 'seconds': 1, 'rates': {w['name']: 0 for w in config['workloads']}}],
                     'traces': {w['name']: {'offsetsNS': [], 'offered': 0} for w in config['workloads']}}
            phase = {'stage': stage, 'startUTC': START, 'endUTC': END, 'starts': {}}
            poller.poll(phase)
            self.assertEqual(published[-1]['closedThrough'], END)
            poller.minute = None
            cutoff[0] = '2026-11-03T00:01:00Z'
            poller.poll(phase)
            updated = evidence_io.load(root / 'provisional-d1.json')
            self.assertEqual(updated['closedThrough'], cutoff[0])
            self.assertEqual(published[-1], updated)
            self.assertEqual(len(published), 2)
            poller.minute = None
            cutoff[0] = '2026-11-03T00:02:00Z'
            for value in index.values():
                value['errors'] = [{'error': 'partial capture'}]
            poller.poll(phase)
            self.assertEqual(evidence_io.load(root / 'provisional-d1.json'), updated)
            self.assertEqual(len(published), 2)
            poller.minute = None
            cutoff[0] = START
            poller.poll(phase)
            self.assertEqual(evidence_io.load(root / 'provisional-d1.json'), updated)
            self.assertEqual(len(published), 2)
            self.assertEqual(len(released), 4)

    def test_missing_stage_is_not_published_as_valid_subset(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stages = [{'id': 'measured', 'kpis': {'status': 'MEASURED_DESCRIPTIVE'}}]
            results = {'measured': {'measurement': {'verdict': 'PASS'}}, 'missing': {'measurement': {'verdict': 'NOT_EVALUATED'}}}
            summary = judgment_rules.summarize(None, root, evaluation={'stages': stages, 'results': results})
            self.assertEqual(summary['status'], 'MEASURED_DESCRIPTIVE')
            self.assertEqual(summary['completeness'], 'PARTIAL')
            results['missing']['measurement']['verdict'] = 'FAIL'
            self.assertEqual(judgment_rules.summarize(None, root, evaluation={'stages': stages, 'results': results})['status'], 'FAIL')

    def test_meter_energy_includes_idle_declared_node_without_history(self):
        with tempfile.TemporaryDirectory() as directory:
            spec, _, _, _, _ = fixture(Path(directory))
            result = archive.meter_energy(power_window(), spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
            self.assertTrue(result['complete'])
            self.assertEqual(result['wholePoolJoules'], 300)
            self.assertEqual(result['scope'], 'declared-node-meters')
            self.assertEqual(len(result['nodes']), 2)

    def test_meter_gap_fails_coverage(self):
        with tempfile.TemporaryDirectory() as directory:
            spec, _, _, _, _ = fixture(Path(directory))
            window = power_window()
            window['nodes'][0].update(gapSeconds=2, gapFraction=.2, maximumSampleGapSeconds=2)
            result = archive.meter_energy(window, spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
            self.assertFalse(result['complete'])
            self.assertIsNone(result['wholePoolJoules'])
            self.assertEqual(result['observedJoules'], 300)
            self.assertEqual(result['energyStatus'], 'PARTIAL')
            self.assertEqual(result['energyCoverage']['maximumNodeGapFraction'], .2)
            window['nodes'][0]['meterComplete'] = False
            self.assertFalse(archive.meter_energy(window, spec['powerMeasurement'], {'maxGapFraction': 1, 'maxSingleGapSeconds': 30}, START, END)['complete'])

    def test_meter_identity_and_scope_are_exact(self):
        with tempfile.TemporaryDirectory() as directory:
            spec, _, _, _, _ = fixture(Path(directory))
            measurement, policy = spec['powerMeasurement'], spec['judgment']['energyCoverage']
            for field, value in (('meter', 'foreign'), ('sourceIdentity', {'metric': 'foreign'}), ('node', {'name': 'idle', 'uid': 'recreated'}), ('gapFraction', float('nan'))):
                window = power_window()
                window['nodes'][0][field] = value
                self.assertFalse(archive.meter_energy(window, measurement, policy, START, END)['complete'])
            window = power_window()
            window['nodes'].append({'node': {'name': 'outside', 'uid': 'outside'}, 'meter': 'other', 'meterJoules': 999999})
            self.assertEqual(archive.meter_energy(window, measurement, policy, START, END)['wholePoolJoules'], 300)

    def test_request_stream_absence_needs_no_internal_stream(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            index = {'complete': True, 'sources': [], 'errors': [], 'requestedHours': ['20261103T00'], 'supportingHours': [],
                     'absentStreams': [{'hour': '20261103T00', 'stream': 'requests', 'origin': 'api', 'reason': 'source-hour-has-no-stream-segments'}]}
            evidence_io.save(root / 'raw-source-index.json', index)
            _, sources, errors = raw._manifest(root, {'requests'})
            self.assertFalse(errors)
            self.assertFalse(sources)

    def test_environment_baseline_and_new_pod_rules(self):
        def ref(kind, uid, version):
            return {'apiVersion': version, 'kind': kind, 'name': kind.lower(), 'uid': uid}
        def pod(uid, *, phase='Running', node='node', refs=None, mirror=False):
            return {'metadata': {'namespace': 'evaluation-ns', 'name': 'same-name', 'uid': uid,
                                 'ownerReferences': refs or [], 'annotations': {'kubernetes.io/config.mirror': 'hash'} if mirror else {}},
                    'spec': {'nodeName': node}, 'status': {'phase': phase}}
        resident = pod('resident', refs=[ref('Deployment', 'unrelated', 'apps/v1')])
        ownerless = pod('ownerless')
        baseline = input_snapshot.pod_environment([resident, ownerless], {'node'})
        cases = [
            (resident, False), (ownerless, False),
            (pod('external-new'), True),
            (pod('pending-new', phase='Pending'), True),
            (pod('own-job-new', refs=[ref('Job', 'own-job', 'batch/v1')]), False),
            (pod('foreign-job-new', refs=[ref('Job', 'foreign-job', 'batch/v1')]), True),
            (pod('serving-new', refs=[ref('Workload', 'target-workload', 'edge.dacs.io/v1alpha1')]), False),
            (pod('unrelated-serving-new', refs=[ref('Workload', 'other-workload', 'edge.dacs.io/v1alpha1')]), True),
            (pod('infra-new', refs=[ref('DaemonSet', 'infra', 'apps/v1')]), False),
            (pod('mirror-new', mirror=True), False),
            (pod('completed-new', phase='Succeeded'), False),
            (pod('failed-new', phase='Failed'), False),
            (pod('outside-new', node='other-node'), False),
        ]
        for candidate, blocked in cases:
            with self.subTest(uid=candidate['metadata']['uid']):
                current = input_snapshot.pod_environment([candidate], {'node'})
                intruders = input_snapshot.competing_pods(current, baseline, {'own-job'}, {'target-workload'}, 'evaluation-ns')
                self.assertEqual(bool(intruders), blocked)

    def test_new_competing_pods_are_recorded_without_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            harness = object.__new__(evaluation.Harness)
            harness.root = Path(directory)
            harness.execution = {'namespace': 'evaluation-ns', 'runUID': 'run', 'stageUID': 'stage'}
            harness.snapshot = {'environmentStart': {'capturedAt': START, 'pods': []},
                                'scenarios': {'scenario': {'spec': {'workloads': [{'uid': 'target-workload'}]}}}}
            current = [{'namespace': 'tenant', 'name': 'intruder', 'uid': 'intruder-uid', 'node': 'node',
                        'phase': 'Running', 'ownerReferences': [], 'mirror': False}]
            with patch.object(input_snapshot, 'participant_pods', return_value=current), patch.object(input_snapshot, 'kubectl', return_value={'items': []}):
                input_snapshot.check_fleet(harness, {'loadScenarioRef': {'uid': 'scenario'}})
            evidence = evidence_io.load(harness.root / 'environment-intrusions.json')
            self.assertEqual(evidence['pods'], current)
            self.assertEqual(evidence['baselineCapturedAt'], START)

    def test_run_job_pod_uses_actual_job_uid_and_controller_reference(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {'namespace': 'evaluation-ns', 'runUID': 'run', 'stageUID': 'stage'}
        harness.snapshot = {'environmentStart': {'capturedAt': START, 'pods': []},
                            'scenarios': {'scenario': {'spec': {'workloads': []}}}}
        current = [{'namespace': 'evaluation-ns', 'name': 'producer', 'uid': 'pod', 'node': 'node', 'phase': 'Pending',
                    'ownerReferences': [{'apiVersion': 'batch/v1', 'kind': 'Job', 'uid': 'actual-job'}], 'mirror': False}]
        job = {'metadata': {'uid': 'actual-job', 'labels': {'edge.dacs.io/evaluation-run-uid': 'run'},
                            'ownerReferences': [{'apiVersion': 'edge.dacs.io/v1alpha1', 'kind': 'EvaluationStage',
                                                 'uid': 'stage', 'controller': True}]}}
        with tempfile.TemporaryDirectory() as directory:
            harness.root = Path(directory)
            with patch.object(input_snapshot, 'participant_pods', return_value=current), patch.object(input_snapshot, 'kubectl', return_value={'items': [job]}):
                input_snapshot.check_fleet(harness, {'loadScenarioRef': {'uid': 'scenario'}})
                job['metadata']['uid'] = 'different-job'
                input_snapshot.check_fleet(harness, {'loadScenarioRef': {'uid': 'scenario'}})
                self.assertTrue((harness.root / 'environment-intrusions.json').exists())

    def test_other_running_producer_is_observed_not_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _, _, _, _, config = fixture(root)
            controller = object.__new__(conductor.CPO)
            controller.c = config
            foreign = {'metadata': {'name': 'dom-generator', 'uid': 'foreign-job'}}
            pod = {'metadata': {'name': 'dom-pod', 'uid': 'foreign-pod', 'ownerReferences': [{'kind': 'Job', 'uid': 'foreign-job'}]},
                   'spec': {'nodeName': config['loadGenerator']['nodeName']},
                   'status': {'phase': 'Running', 'containerStatuses': [{'state': {'running': {'startedAt': START}}}]}}
            controller.io = SimpleNamespace(k=lambda args: {'items': [foreign] if args[1]=='jobs' else [pod]})
            fact = controller.observe_generators(root)
            self.assertEqual(fact['otherProducerRunningOnGeneratorNode'][0]['uid'], 'foreign-pod')
            self.assertTrue((root / 'generator-interference.json').exists())

    def test_target_without_selector_never_scans_namespace_pods(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {}
        target = {'components': [{'name': 'serving', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'namespace': 'tenant', 'name': 'server'}}]}
        deployment = {'kind': 'Deployment', 'metadata': {'uid': 'deployment'}, 'spec': {}}
        with patch.object(input_snapshot, 'kubectl', return_value=deployment) as k:
            with self.assertRaisesRegex(RuntimeError, 'no Pod selector'):
                input_snapshot.check_target(harness, target)
            k.assert_called_once_with('tenant', 'get', 'Deployment', 'server')

    def test_target_uses_declared_match_expression_selector(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {}
        target = {'components': [{'name': 'serving', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'namespace': 'tenant', 'name': 'server'}}]}
        deployment = {'kind': 'Deployment', 'metadata': {'uid': 'deployment'}, 'spec': {'selector': {'matchExpressions': [
            {'key': 'app', 'operator': 'In', 'values': ['server']}, {'key': 'retired', 'operator': 'DoesNotExist'}]}}}
        pod = {'metadata': {'uid': 'pod'}, 'status': {'containerStatuses': [{'name': 'server', 'ready': True}],
                                                  'conditions': [{'type': 'Ready', 'status': 'True'}]}}
        with patch.object(input_snapshot, 'kubectl', side_effect=[deployment, {'items': [pod]}]) as k:
            input_snapshot.check_target(harness, target)
            self.assertEqual(k.call_args_list[-1].args, ('tenant', 'get', 'pods', '-l', 'app in (server),!retired'))

    def test_target_runtime_changes_are_observations_not_exact_gates(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {}
        target = {'components': [{'name': 'serving', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'namespace': 'target-namespace', 'name': 'server'}}]}
        deployment = {'kind': 'Deployment', 'metadata': {'uid': 'deployment', 'labels': {'app.kubernetes.io/instance': 'observed-release'}},
                      'spec': {'selector': {'matchLabels': {'app': 'server'}}}}
        pod = {'metadata': {'uid': 'pod'}, 'status': {'containerStatuses': [{'name': 'server', 'ready': True, 'restartCount': 7, 'imageID': 'unrelated-current-runtime'}],
                                                           'conditions': [{'type': 'Ready', 'status': 'True'}]}}
        with patch.object(input_snapshot, 'kubectl', side_effect=[deployment, {'items': [pod]}]):
            observed = input_snapshot.check_target(harness, target)
        self.assertEqual(observed[0]['pods'][0]['containers'][0]['imageID'], 'unrelated-current-runtime')
        self.assertEqual(observed[0]['resource'], target['components'][0]['resource'])
        pod['status']['conditions'][0]['status'] = 'False'
        with patch.object(input_snapshot, 'kubectl', side_effect=[deployment, {'items': [pod]}]):
            self.assertFalse(input_snapshot.check_target(harness, target)[0]['available'])

    def test_foreign_job_not_owned(self):
        controller = object.__new__(conductor.CPO)
        controller.c = {'evaluation': {'execution': {'runUID': 'run', 'stageUID': 'stage'}}}
        job = {'metadata': {'labels': {'edge.dacs.io/evaluation-run-uid': 'run'}, 'ownerReferences': [{'kind': 'EvaluationStage', 'uid': 'foreign'}]}}
        self.assertFalse(controller.owned(job))
        job['metadata']['ownerReferences'][0]['uid'] = 'stage'
        self.assertTrue(controller.owned(job))

    def test_step_result_failure_is_not_measurement_failure(self):
        execution = {'runUID': 'run', 'stageUID': 'stage', 'stageID': 'one'}
        for verdict, code in (('PASS', 0), ('FAIL', 0), ('INVALID', 2), ('NOT_EVALUATED', 2)):
            result = evaluation.result_record(execution, 'judge', START, measurement={'verdict': verdict, 'reason': 'Measured'})
            self.assertEqual(evaluation.exit_code(result), code)
        self.assertEqual(evaluation.exit_code(evaluation.result_record(execution, 'stage', START, status='Failed')), 1)

    def test_failed_prepare_publication_keeps_outcomes_references_and_credentials_private(self):
        temporary_directory = tempfile.TemporaryDirectory
        for returncode in (0, 1):
            with self.subTest(returncode=returncode), temporary_directory() as directory:
                root = Path(directory)
                run = root / 'campaign/run'
                run.mkdir(parents=True)
                at = START.replace('Z', '+00:00')
                residual_ref = {'store': 'store', 'key': 'experiments/run/run/residual-state.json'}
                records = {
                    'experiments/run/run/prepare/result.json': {
                        'execution': {'status': 'Failed', 'reason': 'FrozenInputInvalid'},
                        'startedAt': at, 'artifacts': []},
                    'experiments/run/run/finish/result.json': {
                        'execution': {'status': 'Complete', 'reason': 'ProcedureComplete'},
                        'cleanup': {'status': 'Failed', 'reason': 'ResidualState'},
                        'residualStateRef': residual_ref, 'finishedAt': at, 'artifacts': []},
                    residual_ref['key']: {
                        'status': 'Failed', 'reason': 'ResidualState',
                        'residuals': [{'kind': 'Node', 'name': 'node'}]},
                }
                def get(reference, path):
                    if reference['key'] not in records:
                        raise FileNotFoundError(reference['key'])
                    input_snapshot.save(path, records[reference['key']])
                context = SimpleNamespace(
                    run=run, spec={'stages': []}, config={'minimumFreeBytes': '1'},
                    execution={'runUID': 'run', 'runName': 'example', 'prepareCompleted': False, 'childRefs': []},
                    store=SimpleNamespace(name='store', get=get))
                credentials_paths = []
                def publish(args, **kwargs):
                    credentials = Path(args[args.index('--config') + 1])
                    credentials_paths.append(credentials)
                    self.assertEqual(credentials.stat().st_mode & 0o777, 0o600)
                    private = input_snapshot.load(credentials)
                    self.assertEqual(private['secretAccessKey'], 'private-secret')
                    self.assertEqual(private['tempDir'], str(root / '.evidence-tmp'))
                    self.assertEqual(Path(private['tempDir']).stat().st_mode & 0o777, 0o700)
                    manifest = input_snapshot.load(args[args.index('--manifest') + 1])
                    self.assertEqual(manifest['summary']['key'], 'serving/experiments.v1alpha1/run/summary.json')
                    return SimpleNamespace(returncode=returncode, stderr='publication denied')
                env = {'CHILL_EVIDENCE_BUCKET': 'bucket', 'CHILL_EVIDENCE_ENDPOINT': 'https://store.invalid',
                       'CHILL_EVIDENCE_REGION': 'region', 'AWS_ACCESS_KEY_ID': 'private-key',
                       'AWS_SECRET_ACCESS_KEY': 'private-secret'}
                def private_directory(**kwargs):
                    return temporary_directory(prefix=kwargs['prefix'], dir=root)
                with patch.dict(os.environ, env), patch.object(publication.tempfile, 'TemporaryDirectory', side_effect=private_directory), patch.object(publication.subprocess, 'run', side_effect=publish) as publisher:
                    if returncode:
                        with self.assertRaisesRegex(RuntimeError, 'PublicationFailed: publication denied'):
                            publication.publish(context)
                    else:
                        publication.publish(context)
                        self.assertEqual(context.outputs['summary']['key'], 'serving/experiments.v1alpha1/run/summary.json')
                    publisher.assert_called_once()
                self.assertFalse(credentials_paths[0].exists())
                summary = input_snapshot.load(run / 'publication-summary.json')
                self.assertEqual(summary['status'], 'NOT_EVALUATED')
                self.assertEqual(summary['outcome']['execution'], {'status': 'Failed', 'reason': 'FrozenInputInvalid'})
                self.assertEqual(summary['outcome']['cleanup']['residuals'],
                                 [{'kind': 'Node', 'name': 'node', 'reason': 'ResidualState'}])
                self.assertLess(evidence_io.timestamp_ns(summary['from']), evidence_io.timestamp_ns(summary['to']))
                self.assertEqual(summary['definition']['spec'], context.spec)
                self.assertEqual([source['key'] for source in summary['sources']],
                                 ['serving/experiments.v1alpha1/run/execution.json',
                                  'serving/experiments.v1alpha1/run/evidence-index.json'])
                self.assertNotIn('private-secret', ''.join(path.read_text() for path in run.rglob('*') if path.is_file()))


if __name__ == '__main__':
    unittest.main()
