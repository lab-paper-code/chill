#!/usr/bin/env python3
"""Offline black-box cutover regressions; no credentials, cluster or image builds."""
import base64
import copy
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

import archive
import conductor
import evaluation
import raw

sys.path.insert(0, str(conductor.REPO / 'load-generator/src'))
from chill_loadgen import cli

NAME = 'plc-anomaly'
SOURCE = {'metric': 'watts', 'endpoint': 'https://meter.invalid'}
START = '2026-11-03T00:00:00Z'
END = '2026-11-03T00:00:10Z'


class SettingsAPI:
    def __init__(self):
        self.value = {'controls': {'power': True, 'routing': True, 'replicas': True}, 'controlIntervalNanos': 30,
                      'minimumDemand': {'other': 7, 'remove': 9}, 'autonomousWorkloads': ['one'],
                      'meters': ['read-only'], 'requestTimeout': 999, 'staticRouting': {}, 'powerObservation': {}}
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
        assert not set(body) & conductor.SETTINGS_PROJECTIONS
        before = copy.deepcopy(self.value)
        projection = {key: value for key, value in self.value.items() if key in conductor.SETTINGS_PROJECTIONS}
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
    assembled = evaluation.stage_config(spec, scenario, execution, config, root)
    return spec, scenario, execution, config, assembled


def power_window():
    return {'from': START, 'to': END, 'complete': False, 'reasons': ['No scheduler history'],
            'nodes': [{'node': {'name': name, 'uid': name + '-uid'}, 'meter': name + '-meter',
                       'joules': None, 'reasons': ['Inactive node'], 'meterJoules': joules, 'meterComplete': True,
                       'gapSeconds': 0, 'gapFraction': 0, 'maximumSampleGapSeconds': .75,
                       'sourceIdentity': SOURCE, 'samples': 15} for name, joules in (('idle', 100), ('serving', 200))]}


class EvaluationTests(unittest.TestCase):
    def test_public_patch_apply_readback_revert(self):
        api = SettingsAPI()
        before = copy.deepcopy(api.value)
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / 'patch.json'
            applied = conductor.factor_apply(api, {'controls': {'power': False}, 'controlIntervalNanos': 42, 'autonomousWorkloads': ['two'], 'minimumDemand': {'remove': None}}, receipt)
            self.assertTrue(applied['accepted'])
            self.assertIn('appliedAt', applied)
            self.assertNotIn('remove', api.value['minimumDemand'])
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT', 'GET'])
            api.value['controls']['routing'] = False
            api.value['minimumDemand']['other'] = 8
            conductor.factor_revert(api, receipt)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT', 'GET', 'GET', 'PUT', 'GET'])
            expected = copy.deepcopy(before)
            expected['controls']['routing'] = False
            expected['minimumDemand']['other'] = 8
            self.assertEqual(api.value, expected)
            final = conductor.load(receipt)
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
                conductor.factor_apply(api, {'controls': {'power': False}}, path)
            conductor.factor_revert(api, path)
            self.assertEqual(api.value, before)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT'])
            self.assertNotIn('appliedAt', conductor.load(path))
            self.assertIn('failedAt', conductor.load(path))

    def test_patch_conflict_preserves_another_writer(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            conductor.factor_apply(api, {'controlIntervalNanos': 42}, path)
            api.value['controlIntervalNanos'] = 50
            conductor.factor_revert(api, path)
            self.assertEqual(api.value['controlIntervalNanos'], 50)
            self.assertEqual(sum(call[0] == 'PUT' for call in api.calls), 1)
            self.assertEqual(conductor.load(path)['revertSkipped'][0]['reason'], 'external-change-revert-skipped')
            self.assertIn('revertedAt', conductor.load(path))

    def test_missing_if_match_returns_428_and_changes_nothing(self):
        api = SettingsAPI()
        before, metadata = copy.deepcopy(api.value), {}
        with self.assertRaises(urllib.error.HTTPError) as caught:
            api('PUT', conductor.SETTINGS, {}, response=metadata)
        self.assertEqual(caught.exception.code, 428)
        self.assertEqual(metadata['statusCode'], 428)
        self.assertEqual(api.value, before)
        api.omit_get_etag = True
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(RuntimeError, 'ETag'):
            conductor.factor_apply(api, {'controls': {'power': False}}, Path(directory) / 'patch.json')
        self.assertEqual(sum(call[0] == 'PUT' for call in api.calls), 1)

    def test_apply_conflict_is_recorded_without_retry_or_revert(self):
        api = SettingsAPI()
        api.reject_codes = [409]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaises(urllib.error.HTTPError):
                conductor.factor_apply(api, {'controls': {'power': False}}, path)
            conductor.factor_revert(api, path)
            receipt = conductor.load(path)
            self.assertEqual(receipt['put']['ifMatch'], '"1"')
            self.assertEqual(receipt['put']['statusCode'], 409)
            self.assertEqual(receipt['put']['response']['code'], 'SettingsConflict')
            self.assertNotIn('appliedAt', receipt)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT'])

    def test_revert_conflict_refreshes_once_and_preserves_external_fields(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            conductor.factor_apply(api, {'controls': {'power': False}}, path)
            api.reject_codes = [409]
            api.on_conflict = lambda: api.value['controls'].update(routing=False)
            conductor.factor_revert(api, path)
            receipt = conductor.load(path)
            self.assertEqual([a['statusCode'] for a in receipt['revertAttempts']], [409, 200])
            self.assertEqual([a['ifMatch'] for a in receipt['revertAttempts']], ['"2"', '"3"'])
            self.assertEqual(receipt['revertAttempts'][-1]['responseETag'], '"4"')
            self.assertFalse(api.value['controls']['routing'])
            self.assertTrue(api.value['controls']['power'])

    def test_second_revert_conflict_blocks_return_without_more_puts(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            conductor.factor_apply(api, {'controls': {'power': False}}, path)
            api.reject_codes = [409, 409]
            with self.assertRaisesRegex(RuntimeError, 'SettingsConflict'):
                conductor.factor_revert(api, path)
            count = len(api.calls)
            with self.assertRaisesRegex(RuntimeError, 'SettingsConflict'):
                conductor.factor_revert(api, path)
            self.assertEqual(len(api.calls), count)
            self.assertNotIn('revertedAt', conductor.load(path))

    def test_uncertain_put_is_not_silently_returned_or_rolled_back(self):
        api = SettingsAPI()
        api.transport_error = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaises(OSError):
                conductor.factor_apply(api, {'controls': {'power': False}}, path)
            with self.assertRaisesRegex(RuntimeError, 'RemoteEffectUnknown'):
                conductor.factor_revert(api, path)
            self.assertEqual([call[0] for call in api.calls], ['GET', 'PUT'])

    def test_put_readback_mismatch_retains_applied_receipt(self):
        api = SettingsAPI()
        api.drift = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            with self.assertRaisesRegex(RuntimeError, 'readback'):
                conductor.factor_apply(api, {'controlIntervalNanos': 42}, path)
            self.assertIn('appliedAt', conductor.load(path))
            self.assertFalse(conductor.load(path)['accepted'])

    def test_patch_added_nested_object_reverts_absence(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            conductor.factor_apply(api, {'minimumDemand': {'new': 7}}, path)
            conductor.factor_revert(api, path)
            self.assertNotIn('new', api.value['minimumDemand'])

    def test_null_control_uses_canonical_applied_value_for_readback_and_revert(self):
        api = SettingsAPI()
        def normalize(value):
            value['controls'].setdefault('power', False)
            return value
        api.normalize = normalize
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            receipt = conductor.factor_apply(api, {'controls': {'power': None}}, path)
            self.assertTrue(receipt['accepted'])
            self.assertNotIn('power', receipt['request']['controls'])
            self.assertEqual(receipt['changed'][0]['applied'], {'exists': True, 'value': False})
            self.assertFalse(api.value['controls']['power'])
            conductor.factor_revert(api, path)
            self.assertTrue(api.value['controls']['power'])
            self.assertEqual(conductor.load(path)['revertSkipped'], [])

    def test_new_map_reverts_only_own_members_and_preserves_external_entry(self):
        api = SettingsAPI()
        del api.value['minimumDemand']
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            receipt = conductor.factor_apply(api, {'minimumDemand': {'evaluation': 7}}, path)
            self.assertEqual(receipt['changed'][0]['path'], ['minimumDemand', 'evaluation'])
            api.value['minimumDemand']['operator'] = 9
            conductor.factor_revert(api, path)
            self.assertEqual(api.value['minimumDemand'], {'operator': 9})
            self.assertEqual(conductor.load(path)['revertSkipped'], [])

    def test_new_map_is_removed_when_own_member_cleanup_leaves_it_empty(self):
        api = SettingsAPI()
        del api.value['minimumDemand']
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            conductor.factor_apply(api, {'minimumDemand': {'evaluation': 7}}, path)
            conductor.factor_revert(api, path)
            self.assertNotIn('minimumDemand', api.value)

    def test_empty_patch_is_read_only(self):
        api = SettingsAPI()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'patch.json'
            self.assertTrue(conductor.factor_apply(api, {}, path)['accepted'])
            conductor.factor_revert(api, path)
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
            conductor.save(path, config)
            bound = conductor.bind(path)
            self.assertEqual(bound['schema'], 'e5-binding.v3')
            self.assertEqual(bound['schedule'][0]['parts'][0]['rates'], {NAME: 2.5})
            self.assertEqual(bound['schedule'][0]['traces'][NAME]['seed'], 202)
            self.assertEqual(bound['schedule'], conductor.bind(path)['schedule'])

    def test_dataset_reference_drives_job_and_placement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, _, config = fixture(root)
            path = root / 'config.json'
            conductor.save(path, config)
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

    def test_materialized_dataset_actual_bytes_are_producer_evidence(self):
        producer = evaluation.module('producer_test', evaluation.HERE / 'producer.py')
        with tempfile.TemporaryDirectory() as directory:
            data = Path(directory)
            for record, content in (('02', b'second'), ('01', b'first')):
                (data / record).mkdir()
                (data / record / 'input').write_bytes(content)
            records = cli.load_records(data, ['input'])
            offsets = [1, 2]
            policy = {'segments': [{'rate': 1, 'seconds': 1}], 'offsetsNS': offsets, 'offered': 2,
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
            execution = {key: value for key, value in execution.items() if key not in ('stageUID', 'stageID', 'stageName')}
            execution.update(runName='run', childRefs=[])
            spec.update(target={'components': []}, evidenceStore='store', stages=[{'id': 'one', 'row': 'e5-test', 'loadScenarioRef': {'name': 'load', 'uid': 'load-uid'}}])
            specdir = root / 'spec'
            for name, value in (('spec', spec), ('execution', execution), ('config', config)):
                conductor.save(specdir / (name + '.json'), value)
            uploads = []
            store = SimpleNamespace(put=lambda key, path: uploads.append(key) or {'store': 'store', 'key': key, 'size': Path(path).stat().st_size})
            harness = evaluation.Harness(root / 'campaign/run', specdir, store)
            harness.run.mkdir(parents=True)
            objects = {
                'loadscenario': {'metadata': {'name': 'load', 'uid': 'load-uid'}, 'spec': scenario['spec']},
                'dataset': scenario['datasets'][NAME],
                'workload': {'metadata': {'uid': 'workload'}, 'spec': {'preprocessorRef': {'name': 'preprocessor'}}},
                'preprocessor': {'spec': {'input': {'fields': [{'name': 'input'}]}}},
            }
            def k(namespace, verb, kind, *args):
                return copy.deepcopy(objects[kind])
            def fleet(stage):
                harness.generator_node = {'name': 'genesis1', 'uid': 'generator-node'}
            with patch.object(evaluation, 'kubectl', side_effect=k), patch.object(evaluation, 'api', return_value={'appliedDefinition': scenario['workloads'][0]}), patch.object(harness, 'check_fleet', side_effect=fleet), patch.dict(os.environ, CHILL_HARNESS_SOURCE_SHA=spec['harness']['sourceSHA']):
                harness.prepare()
            snapshot = conductor.load(harness.inputs / 'snapshot.json')
            self.assertEqual(snapshot['schemaVersion'], 'evaluation-snapshot.v2alpha1')
            self.assertEqual(snapshot['inputsVersion'], 'evaluation-inputs.v2alpha1')
            frozen = snapshot['bindings']['one']['config']['workloads'][0]
            self.assertEqual(frozen['datasetRef'], {'name': 'records', 'uid': 'dataset'})
            self.assertEqual(frozen['dataset']['source']['path'], '/records')
            self.assertEqual(frozen['fields'], ['input'])
            self.assertEqual(len(uploads), 1)

    def test_real_stage_loop_uses_only_owned_ready_go_and_request_drain(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, scenario, execution, _, config = fixture(root)
            config['stages'][0]['parts'][0]['seconds'] = .01
            config['traceOffsets'] = {NAME: [1]}
            config['timeboxes'] = dict.fromkeys(config['timeboxes'], 5)
            config['timeboxes']['flushSeconds'] = .001
            path = root / 'config.json'
            conductor.save(path, config)
            controller = object.__new__(conductor.CPO)
            controller.b = conductor.bind(path)
            controller.c, controller.out, controller.tb = config, root, config['timeboxes']
            controller.ws = {w['name']: w for w in config['workloads']}
            controller.a = SimpleNamespace(stage='e5-test')
            controller.monitor = lambda *args: 0
            controller.go_callback = lambda value: None
            ready = {'evaluationEvent': 'ready', 'at': START, 'barrierSeconds': 10000000000,
                     'offered': 1, 'arrivalSHA256': controller.b['schedule'][0]['traces'][NAME]['arrivalSHA256'],
                     'payload': [{'recordID': 'one', 'fields': {'input': {'bytes': 1, 'sha256': 'a' * 64}}}],
                     'datasetRef': scenario['spec']['datasetRefs'][NAME], 'sourceSHA': spec['loadGenerator']['sourceSHA']}
            script = ('import sys,json\nprint(' + repr(json.dumps(ready)) + ',flush=True)\nsys.stdin.readline()\n'
                      'print(json.dumps({"evaluationEvent":"start","at":' + repr(START) + ',"monotonicNS":1}),flush=True)\n'
                      'print(json.dumps({"evaluationEvent":"drained"}),flush=True)\nsys.stdin.readline()\n')
            pod = {'metadata': {'name': 'producer', 'uid': 'pod', 'ownerReferences': [{'kind': 'Job', 'uid': 'job'}]},
                   'status': {'phase': 'Running', 'containerStatuses': [
                       {'name': 'load', 'containerID': 'container', 'imageID': spec['loadGenerator']['image'], 'restartCount': 0, 'state': {'running': {'startedAt': START}}}]}}
            process = [None]
            jobs = []
            original_popen = subprocess.Popen
            def launch(*args, **kwargs):
                process[0] = original_popen([sys.executable, '-u', '-c', script], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=kwargs['stderr'], bufsize=0)
                return process[0]
            def k(args, stdin=None, **kwargs):
                if args[:2] == ['get', 'jobs']:
                    return {'items': [{'metadata': {'name': 'resident-tenant-generator', 'uid': 'tenant-job',
                                                   'labels': {'edge.dacs.io/workload-role': 'load-generator'}}}]}
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
                    return {**jobs[0], 'status': {'conditions': [{'type': 'Complete', 'status': 'True'}]}}
                if args[0] == 'exec':
                    process[0].stdin.write(b'release\n')
                    process[0].stdin.flush()
                    return {}
                raise AssertionError(args)
            controller.io = SimpleNamespace(k=k, delete_job=lambda *args: None)
            counters = [{'podUID': 'pod', 'containerID': 'container', 'at': '2099-01-01T00:00:00Z', 'cpuUsageNanos': 1, 'nrThrottled': 0, 'throttledNanos': 0}]
            telemetry = SimpleNamespace(ready=lambda *args: True, poll=lambda *args: counters)
            original_module = conductor.module
            auth = original_module('test_auth', conductor.HERE / 'auth.py')
            def modules(name, path):
                if name == 'evaluation_auth':
                    return SimpleNamespace(lifetime_budget=auth.lifetime_budget, check=lambda *args, **kwargs: {'accepted': True})
                if name == 'evaluation_archive':
                    return SimpleNamespace(collect=lambda *args: {'status': 'CAPTURED_NOT_EVALUATED'})
                return original_module(name, path)
            conductor.save(root / 'settings-patch.json', {'accepted': True})
            with patch.object(conductor.subprocess, 'Popen', side_effect=launch), patch.object(conductor, 'ProducerResources', return_value=telemetry), patch.object(conductor, 'module', side_effect=modules):
                controller.run()
            process[0].stdin.close()
            phase = conductor.load(root / 'measured/phase.json')
            self.assertEqual(phase['status'], 'DRAINED_NOT_EVALUATED')
            self.assertEqual(set(phase['starts']), {NAME})
            self.assertTrue((root / 'measured' / (NAME + '-client-end-counter.json')).exists())

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
            conductor.save(root / 'measured/created-one.json', job)
            pod = {'metadata': {'name': 'residual', 'uid': 'pod', 'ownerReferences': [{'kind': 'Job', 'uid': 'job'}]}}
            controller.io = SimpleNamespace(k=lambda args: {'items': [] if args[1] == 'jobs' else [pod]})
            with self.assertRaisesRegex(RuntimeError, 'ResidualState'):
                controller.cleanup_jobs()
            self.assertEqual(controller.residuals, [{'kind': 'Pod', 'name': 'residual', 'reason': 'ResidualState'}])

    def test_slo_failure_is_valid_without_internal_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, _, _, _, config = fixture(root)
            config['traceOffsets'] = {NAME: [1, 2]}
            path = root / 'config.json'
            conductor.save(path, config)
            bound = conductor.bind(path)
            conductor.save(root / 'binding.json', bound)
            phase = {'stage': bound['schedule'][0], 'startUTC': START, 'endUTC': END, 'drainedUTC': END,
                     'starts': {NAME: {'at': START, 'monotonicNS': 100}}, 'status': 'DRAINED_NOT_EVALUATED'}
            conductor.save(root / 'phase.json', phase)
            power = archive.meter_energy(power_window(), spec['powerMeasurement'], spec['judgment']['energyCoverage'], START, END)
            for name in ('power.json', 'power-drain-inclusive.json'):
                conductor.save(root / name, power)
            rows = [{'requestIndex': i, 'requestID': 'r' + str(i), 'scheduledOffsetNs': i + 1, 'scheduledMonotonicNS': i + 101,
                     'sentMonotonicNS': i + 101, 'finishedMonotonicNS': 3000000000, 'issued': True} for i in range(2)]
            (root / (NAME + '-client.jsonl')).write_text(''.join(json.dumps(row) + '\n' for row in rows))
            indexed = {config['workloads'][0]['uid']: {'errors': [], 'identities': [{'workloadUID': 'workload', 'definitionID': 'definition'}],
                                                     'summary': {'uniqueRequests': 2}, 'finals': {
                'r0': {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 2000000000},
                'r1': {'outcome': 'timeout', 'executionComplete': False, 'elapsedNanos': 2000000000}}}}
            original_module = conductor.module
            def modules(name, path):
                if name == 'evaluation_raw':
                    return SimpleNamespace(iter_request_indexes=lambda *args, **kwargs: indexed)
                return original_module(name, path)
            with patch.object(conductor, 'module', side_effect=modules):
                judgment = conductor.judge(root / 'binding.json', root)
            self.assertEqual(judgment['schema'], 'e5-judgment.v2')
            self.assertEqual(judgment['status'], 'FAIL')
            self.assertEqual(judgment['requests'][NAME]['offered'], 2)
            self.assertEqual(judgment['requests'][NAME]['deadlineSuccess'], 0)
            self.assertEqual(judgment['requests'][NAME]['issuanceFidelity']['generatorQuality']['status'], 'VALID')
            self.assertEqual(evaluation.verdict(judgment)['verdict'], 'FAIL')
            self.assertEqual(judgment['requests'][NAME]['substages'][0]['offered'], 2)
            self.assertEqual(judgment['requests'][NAME]['definitionID'], 'definition')
            rows[1]['requestID'] = 'r0'
            (root / (NAME + '-client.jsonl')).write_text(''.join(json.dumps(row) + '\n' for row in rows))
            with patch.object(conductor, 'module', side_effect=modules):
                duplicated = conductor.judge(root / 'binding.json', root)
            self.assertEqual(duplicated['status'], 'NOT_READY')
            self.assertIsNone(duplicated['requests'][NAME]['definitionID'])

    def test_missing_stage_is_not_published_as_valid_subset(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stages = [{'id': 'measured', 'kpis': {'status': 'MEASURED_DESCRIPTIVE'}}]
            results = {'measured': {'measurement': {'verdict': 'PASS'}}, 'missing': {'measurement': {'verdict': 'NOT_EVALUATED'}}}
            summary = conductor.summarize(None, root, evaluation={'stages': stages, 'results': results})
            self.assertEqual(summary['status'], 'NOT_EVALUATED')
            results['missing']['measurement']['verdict'] = 'FAIL'
            self.assertEqual(conductor.summarize(None, root, evaluation={'stages': stages, 'results': results})['status'], 'FAIL')

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
            conductor.save(root / 'raw-source-index.json', index)
            _, sources, errors = raw._manifest(root, {'requests'})
            self.assertFalse(errors)
            self.assertFalse(sources)

    def test_participating_node_gate_uses_structural_ownership_not_names(self):
        spec = {'powerMeasurement': {'nodes': [{'name': 'meter-node', 'uid': 'meter-uid'}]},
                'loadGenerator': {'nodeName': 'generator-node'}}
        nodes = {'meter-node': 'meter-uid', 'generator-node': 'generator-uid'}
        def ref(kind, name, uid, version='apps/v1'):
            return {'apiVersion': version, 'kind': kind, 'name': name, 'uid': uid}
        def pod(name, *, node='meter-node', phase='Running', namespace='evaluation-ns', labels=None, owners=None, annotations=None):
            return {'apiVersion': 'v1', 'kind': 'Pod',
                    'metadata': {'name': name, 'uid': name + '-uid', 'namespace': namespace, 'labels': labels or {},
                                 'ownerReferences': owners or [], 'annotations': annotations or {}},
                    'spec': {'nodeName': node}, 'status': {'phase': phase}}
        targets = [{'uid': 'target-uid', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment',
                                                    'name': 'server', 'namespace': 'evaluation-ns'},
                    'releaseInstance': 'frozen-release'}]
        objects = {
            ('ReplicaSet', 'chill-rs'): {'metadata': {'uid': 'chill-rs-uid', 'ownerReferences': [ref('Deployment', 'server', 'target-uid')]}},
            ('ReplicaSet', 'foreign-rs'): {'metadata': {'uid': 'foreign-rs-uid', 'ownerReferences': [ref('Deployment', 'tenant', 'foreign-deployment-uid')]}},
            ('Deployment', 'tenant'): {'metadata': {'uid': 'foreign-deployment-uid'}},
            ('Job', 'eval-label-job'): {'metadata': {'uid': 'eval-label-job-uid', 'labels': {'edge.dacs.io/evaluation-run-uid': 'run-uid'}}},
            ('Job', 'eval-job'): {'metadata': {'uid': 'eval-job-uid', 'ownerReferences': [ref('EvaluationStage', 'stage', 'stage-uid', 'edge.dacs.io/v1alpha1')]}},
            ('Job', 'release-job'): {'metadata': {'uid': 'release-job-uid', 'ownerReferences': [ref('CronJob', 'release-cron', 'cron-uid', 'batch/v1')]}},
            ('CronJob', 'release-cron'): {'metadata': {'uid': 'cron-uid', 'labels': {'app.kubernetes.io/instance': 'frozen-release'}}},
            ('StatefulSet', 'release-stateful'): {'metadata': {'uid': 'stateful-uid', 'labels': {'app.kubernetes.io/instance': 'frozen-release'}}},
            ('Workload', 'service'): {'metadata': {'uid': 'workload-uid'}},
        }
        cases = [
            (pod('vib-outside', node='other-node'), False),
            (pod('vib-completed', phase='Succeeded'), False),
            (pod('bff-failed', node='generator-node', phase='Failed'), False),
            (pod('nvidia-cuda-validator-f4l98', node='generator-node', phase='Succeeded', namespace='gpu-operator'), False),
            (pod('device-plugin', node='generator-node', owners=[ref('DaemonSet', 'plugins', 'ds-uid')], namespace='gpu-operator'), False),
            (pod('static-pod', phase='Pending', namespace='node-infra', annotations={'kubernetes.io/config.mirror': 'mirror-hash'}), False),
            (pod('vib-release-owned', labels={'app.kubernetes.io/instance': 'frozen-release'}), False),
            (pod('bff-release-owner-chain', owners=[ref('ReplicaSet', 'chill-rs', 'chill-rs-uid')]), False),
            (pod('evaluation-label-owned', labels={'edge.dacs.io/evaluation-run-uid': 'run-uid'}), False),
            (pod('evaluation-job-owned', owners=[ref('Job', 'eval-job', 'eval-job-uid', 'batch/v1')]), False),
            (pod('evaluation-job-label-owned', owners=[ref('Job', 'eval-label-job', 'eval-label-job-uid', 'batch/v1')]), False),
            (pod('release-cron-owned', owners=[ref('Job', 'release-job', 'release-job-uid', 'batch/v1')]), False),
            (pod('release-stateful-owned', owners=[ref('StatefulSet', 'release-stateful', 'stateful-uid')]), False),
            (pod('workload-owned', phase='Pending', namespace='model-serving', owners=[ref('Workload', 'service', 'workload-uid', 'edge.dacs.io/v1alpha1')]), False),
            (pod('evaluation-ref-owned', owners=[ref('EvaluationRun', 'run', 'run-uid', 'edge.dacs.io/v1alpha1')]), False),
            (pod('vib-external-deployment', owners=[ref('ReplicaSet', 'foreign-rs', 'foreign-rs-uid')]), True),
            (pod('arbitrary-external', phase='Pending', node='generator-node', namespace='tenant'), True),
            (pod('other-release', labels={'app.kubernetes.io/instance': 'different-release'}), True),
            (pod('same-release-name-elsewhere', namespace='other-release-ns', labels={'app.kubernetes.io/instance': 'frozen-release'}), True),
            (pod('other-evaluation', labels={'edge.dacs.io/evaluation-run-uid': 'other-run'}), True),
            (pod('stale-workload-owner', owners=[ref('Workload', 'service', 'stale-uid', 'edge.dacs.io/v1alpha1')]), True),
            (pod('ambiguous-workload-owner', owners=[ref('Workload', 'service', 'workload-uid', 'edge.dacs.io/v1alpha1'),
                                                    ref('Workload', 'second', 'second-uid', 'edge.dacs.io/v1alpha1')]), True),
            (pod('invalid-evaluation-ref', owners=[ref('EvaluationRun', 'run', 'run-uid', 'unrelated/v1')]), True),
            (pod('unknown-phase', phase='Unknown'), False),
        ]
        for candidate, blocked in cases:
            with self.subTest(name=candidate['metadata']['name']):
                harness = object.__new__(evaluation.Harness)
                harness.execution = {'namespace': 'evaluation-ns', 'runUID': 'run-uid', 'stageUID': 'stage-uid', 'stageID': 'stage'}
                harness.snapshot = {'generatorNode': {'name': 'generator-node', 'uid': 'generator-uid'}, 'targets': {'stage': targets}}
                queries = []
                def k(namespace, verb, kind, *args):
                    if kind == 'node':
                        return {'metadata': {'name': args[0], 'uid': nodes[args[0]]},
                                'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}
                    if kind != 'pods':
                        return copy.deepcopy(objects[(kind, args[0])])
                    self.assertIn('--all-namespaces', args)
                    self.assertIn('--field-selector', args)
                    selector = args[args.index('--field-selector') + 1]
                    self.assertIn(selector, {'spec.nodeName=' + node for node in nodes})
                    queries.append(selector)
                    return {'items': [candidate]}
                with patch.object(evaluation, 'kubectl', side_effect=k):
                    if blocked:
                        with self.assertRaisesRegex(RuntimeError, 'external Pod on participating Node'):
                            harness.check_fleet(spec)
                    else:
                        harness.check_fleet(spec)
                        self.assertEqual(set(queries), {'spec.nodeName=' + node for node in nodes})

    def test_target_without_selector_never_scans_namespace_pods(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {}
        target = {'components': [{'name': 'serving', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'namespace': 'tenant', 'name': 'server'}}]}
        deployment = {'kind': 'Deployment', 'metadata': {'uid': 'deployment'}, 'spec': {}}
        with patch.object(evaluation, 'kubectl', return_value=deployment) as k:
            with self.assertRaisesRegex(RuntimeError, 'no Pod selector'):
                harness.check_target(target)
            k.assert_called_once_with('tenant', 'get', 'Deployment', 'server')

    def test_target_uses_declared_match_expression_selector(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {}
        target = {'components': [{'name': 'serving', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'namespace': 'tenant', 'name': 'server'}}]}
        deployment = {'kind': 'Deployment', 'metadata': {'uid': 'deployment'}, 'spec': {'selector': {'matchExpressions': [
            {'key': 'app', 'operator': 'In', 'values': ['server']}, {'key': 'retired', 'operator': 'DoesNotExist'}]}}}
        pod = {'metadata': {'uid': 'pod'}, 'status': {'containerStatuses': [{'name': 'server', 'ready': True}],
                                                  'conditions': [{'type': 'Ready', 'status': 'True'}]}}
        with patch.object(evaluation, 'kubectl', side_effect=[deployment, {'items': [pod]}]) as k:
            harness.check_target(target)
            self.assertEqual(k.call_args_list[-1].args, ('tenant', 'get', 'pods', '-l', 'app in (server),!retired'))

    def test_target_runtime_changes_are_observations_not_exact_gates(self):
        harness = object.__new__(evaluation.Harness)
        harness.execution = {}
        target = {'components': [{'name': 'serving', 'resource': {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'namespace': 'target-namespace', 'name': 'server'}}]}
        deployment = {'kind': 'Deployment', 'metadata': {'uid': 'deployment', 'labels': {'app.kubernetes.io/instance': 'observed-release'}},
                      'spec': {'selector': {'matchLabels': {'app': 'server'}}}}
        pod = {'metadata': {'uid': 'pod'}, 'status': {'containerStatuses': [{'name': 'server', 'ready': True, 'restartCount': 7, 'imageID': 'unrelated-current-runtime'}],
                                                           'conditions': [{'type': 'Ready', 'status': 'True'}]}}
        with patch.object(evaluation, 'kubectl', side_effect=[deployment, {'items': [pod]}]):
            observed = harness.check_target(target)
        self.assertEqual(observed[0]['pods'][0]['containers'][0]['imageID'], 'unrelated-current-runtime')
        self.assertEqual(observed[0]['releaseInstance'], 'observed-release')
        self.assertEqual(observed[0]['resource'], target['components'][0]['resource'])
        self.assertIs(harness.target_identities, observed)
        pod['status']['conditions'][0]['status'] = 'False'
        with patch.object(evaluation, 'kubectl', side_effect=[deployment, {'items': [pod]}]), self.assertRaisesRegex(RuntimeError, 'TargetUnavailable'):
            harness.check_target(target)

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


if __name__ == '__main__':
    unittest.main()
