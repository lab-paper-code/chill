"""Offline generator consumer smoke: real manifests, mocked read-only inputs."""
import contextlib
import copy
import importlib.util
import io
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


def load_generator():
    with tempfile.TemporaryDirectory() as temp:
        config = Path(temp) / 'environment.json'
        config.write_text(json.dumps({'paths': {'campaign': temp}, 'measure': {
            'capturedValues': temp, 'namespace': 'chill-system', 'authority': 'offline',
            'kubectl': 'offline-kubectl', 'kubeconfig': 'offline', 'kubeContext': 'offline'}}))
        with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(config)):
            spec = importlib.util.spec_from_file_location('coverage_generator', Path(__file__).parents[1] / 'gen.py')
            gen = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(gen)
        return gen


def generate_case(gen, image='old-runtime', phase='Complete', curve=None, evidence=None,
                  annotated=True, controlled=False, prefix=True, memory_policy=None, cell_annotation=True,
                  variant=True, allocatable='20Gi', memory_fraction=0.8, policy_limit=17179869184,
                  model='mobilenet-v2-050', device='raspberry-pi-5-8g', runtime_image='new-runtime',
                  node_uid='node-uid', timing_seconds=None, power_mode='fixed', timing_power_mode=None):
    """Exercise generate_coverage, not just its independent selection helpers."""
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        path = 'firstfloor-ort-cpu-arm64'
        source_mode = power_mode if timing_power_mode is None else timing_power_mode
        cell = {'id': 'cell', 'model': {'name': model}, 'deviceClass': {'name': device},
                'executionPath': path, 'powerMode': power_mode, 'support': {'verdict': 'Compatible'},
                'assessment': {'displayStatus': 'usable' if phase == 'Complete' else 'problem', 'reasons': []},
                'variants': [{'condition': {'runtimeImage': image}, 'profileIds': ['ns/coverage-old-runtime/uid']}]
                            if phase == 'Complete' else []}
        if not variant:
            cell['variants'] = []
            cell['assessment']['displayStatus'] = 'unmeasured'
        conditions = {'runtimeImage': image, 'curveSchema': curve or gen.SHARED_CURVE_SCHEMA,
                      'evidenceSchema': evidence or gen.SHARED_EVIDENCE_SCHEMA}
        annotations = {}
        if cell_annotation:
            annotations[gen.load(gen.HERE / 'coverage-plan.json')['cellAnnotation']] = 'cell'
        if annotated:
            annotations['chill.dacs.io/coverage-execution-conditions'] = json.dumps(conditions)
        previous = {'metadata': {'name': ('coverage-' if prefix else 'pilot-') + 'old-runtime', 'uid': 'uid',
                                'creationTimestamp': '2026-01-01', 'annotations': annotations},
                    'status': {'phase': phase}, 'spec': {
                        'state': {'targetNode': 'node', 'candidate': {'modelSpec': model, 'deviceClass': device,
                                  'executionPath': path, 'powerMode': source_mode, 'preprocessor': 'pre'}},
                        'activeProtocol': {'durationSeconds': 60, 'dataset': 'dataset',
                                           'metricObservation': {'durationSeconds': 62}},
                        'idleProtocol': {'durationSeconds': 10, 'metricObservation': {'durationSeconds': 12}},
                        'retryLimit': 1}}
        if controlled:
            previous['spec']['state']['cpuRequest'] = '3'
            previous['spec']['cpuQuota'] = '3'
        result = {'campaign': {'uid': 'uid'}, 'result': {
            'curvePlan': {'schemaVersion': conditions['curveSchema']},
            'completedTrials': [{'evidence': {'schemaVersion': conditions['evidenceSchema']}}]}}
        if timing_seconds is not None:
            result['measurementFacts'] = {
                'condition': {'logicalModel': model, 'deviceClass': device, 'runtimeImage': image,
                              'artifact': {'executionPath': path}, 'powerMode': source_mode},
                'request': {'name': previous['metadata']['name'], 'uid': 'uid'},
                'batches': [{'batch': 1, 'completedCalls': 1, 'quality': [
                    {'workloadMode': 'saturated-serial-fixed-batch', 'windowSeconds': timing_seconds}]}]}
        inputs = {
            'measurement-coverage': {'completeness': 'complete', 'snapshotId': 'snapshot', 'cells': [cell]},
            'batch-energy-optimizations': {'items': [previous]},
            'profiles': {'profiles': [{'id': 'ns/coverage-old-runtime/uid', 'nodeName': 'node',
                'measuredAt': '2026-01-01', 'condition': {'runtimeImage': image, 'logicalModel': model,
                'deviceClass': device, 'artifact': {'executionPath': path}, 'powerMode': source_mode}}]
                        if phase == 'Complete' or timing_seconds is not None else []},
            'inventory': {'classes': [{'name': device, 'architecture': 'arm64', 'memory': '8Gi',
                'nodes': [{'name': 'node', 'uid': 'node-uid', 'ready': 'True', 'schedulingDisabled': False}]}]},
            'models': {'items': [{'metadata': {'name': model}, 'spec': {
                'executionPaths': [{'name': path, 'artifact': 'artifact', 'runtime': {'family': 'firstfloor',
                    'backend': 'ort-cpu', 'settings': {'firstfloor': {'maxBatchSize': 2}}}}],
                'artifacts': [{'name': 'artifact', 'source': {'architecture': 'arm64'}}]}}]},
            'preprocessors': {'items': [{'metadata': {'name': 'pre'}, 'spec': {
                'artifacts': [{'target': {'model': model, 'artifact': 'artifact'}}], 'input': {'fields': []}}}]},
            'datasets': {'items': [{'metadata': {'name': 'dataset'}, 'spec': {'fields': []}}]}}
        values = root / 'runtime.json'
        values.write_text(json.dumps({'profiling': {'executionDeclarations': [{'deviceClass': device,
            'runtime': {'payload': json.dumps({'image': runtime_image, 'architecture': 'arm64',
                'runtime': {'family': 'firstfloor', 'backends': ['ort-cpu']}})}}]}}))
        policy = gen.load(gen.HERE / 'coverage-plan.json')
        policy['runtimeValues'] = str(values)
        if memory_policy is not None:
            policy['memoryMaxBatch'] = memory_policy
        policy_path = root / 'policy.json'
        policy_path.write_text(json.dumps(policy))
        facts = {'data': {'execution-policies.yaml': json.dumps({'policies': [{
            'deviceClass': device, 'executionPath': path, 'workloadMode': 'saturated-serial-fixed-batch',
            'runtimeMemoryLimitBytes': policy_limit}]})}}
        nodes = {'items': [{'metadata': {'name': 'node', 'uid': node_uid},
                            'status': {'allocatable': {} if allocatable is None else {'memory': allocatable}}}]}
        args = ['--fact-source-config-map=chill-profiler-facts']
        if memory_fraction is not None:
            args.append('--measurement-memory-fraction=' + str(memory_fraction))
        deployments = {'items': [{'spec': {'template': {'spec': {'containers': [{'args': args}]}}}}]}

        def kubectl(command):
            if 'nodes' in command:
                return json.dumps(nodes).encode()
            if 'deployments' in command:
                return json.dumps(deployments).encode()
            assert command[-4:] == ['configmap', 'chill-profiler-facts', '-o', 'json']
            return json.dumps(facts).encode()

        def api(url, body=None):
            assert body is None, 'generation must not submit or preflight requests'
            return copy.deepcopy(result if url.endswith('/result') else inputs[url.rsplit('/', 1)[-1]])

        output = root / 'out'
        with patch.object(gen, 'coverage_api', side_effect=api), \
                patch.object(gen.subprocess, 'check_output', side_effect=kubectl), \
                contextlib.redirect_stdout(io.StringIO()):
            gen.generate_coverage(output, policy_path)
        return {name: json.loads((output / (name + '.json')).read_text()) for name in
                ('summary', 'cells', 'existing-cells', 'excluded-batches', 'excluded-cells', 'resume-plan')}


def generation_self_check(gen):
    old = generate_case(gen)
    assert old['summary']['staleRuntimeCellCount'] == 1
    assert old['summary']['manifestCellCount'] == 1 and not old['existing-cells']
    assert old['resume-plan']['cells'][0]['batches'] == [1, 2]
    current = generate_case(gen, image='new-runtime')
    assert current['summary']['currentRuntimeMeasuredCellCount'] == 1
    complete = generate_case(gen, image='new-runtime', variant=False)
    assert complete['summary']['manifestCellCount'] == 0 and len(complete['existing-cells']) == 1
    assert current['summary']['manifestCellCount'] == 0
    for phase in ('Measuring', ''):
        running = generate_case(gen, image='new-runtime', phase=phase)
        assert running['summary']['manifestCellCount'] == 0 and len(running['existing-cells']) == 1
        stale = generate_case(gen, phase=phase)
        assert stale['summary']['manifestCellCount'] == 1 and not stale['existing-cells']
    assert generate_case(gen, image='new-runtime', phase='Failed')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', phase='Measuring', annotated=False)['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', curve='old-curve')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', evidence='old-evidence')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, controlled=True)['summary']['manifestCellCount'] == 1
    fallback = generate_case(gen, image='new-runtime', phase='Measuring', cell_annotation=False)
    assert fallback['existing-cells']
    assert generate_case(gen, image='new-runtime', phase='Measuring', curve='old-curve')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', phase='Measuring', evidence='old-evidence')['summary']['manifestCellCount'] == 1
    rule = {'model': 'mobilenet-v2-050', 'deviceClass': 'raspberry-pi-5-8g', 'executionPath': None,
            'maxBatch': 1, 'conditions': {'runtimeImage': 'new-runtime',
                'executionPath': 'firstfloor-ort-cpu-arm64', 'memoryLimitBytes': 17179869184},
            'basis': 'observed-failure', 'observations': [{'kind': 'OOMKilled', 'batch': 2}]}
    same = generate_case(gen, phase='Failed', memory_policy=[rule])
    assert same['cells'][0]['batches'] == [1]
    assert same['excluded-batches'][0]['reason'] == gen.load(gen.HERE / 'coverage-plan.json')['memoryExcludedBatchReason']
    assert same['excluded-batches'][0]['memoryPolicy']['status'] == 'applied'
    for key, value in (('runtimeImage', 'old-runtime'), ('memoryLimitBytes', 1024),
                       ('executionPath', 'other-path'), ('runtimeImage', None)):
        changed_rule = copy.deepcopy(rule)
        changed_rule['conditions'][key] = value
        changed = generate_case(gen, phase='Failed', memory_policy=[changed_rule])
        assert changed['summary']['manifestCellCount'] == 0
        assert changed['excluded-cells'][0]['reason'] == gen.load(gen.HERE / 'coverage-plan.json')['memoryReviewReason']
        state = changed['excluded-batches'][0]['memoryPolicy']
        assert state['status'] == ('conditions-unknown' if value is None else 'conditions-changed')
        assert state['rule']['conditions'][key] == value  # old cap and evidence are never cleared
    # Retained failure-investigation/nodes.json: Xavier allocatable is 6915992Ki, not board RAM.
    xavier = next(r for r in gen.load(gen.HERE / 'coverage-plan.json')['memoryMaxBatch'] if
                  r['deviceClass'] == 'jetson-xavier-nx-8g' and r['executionPath'] == 'firstfloor-ort-cpu-arm64')
    inputs = {'phase': 'Failed', 'model': xavier['model'], 'device': xavier['deviceClass'],
              'runtime_image': xavier['conditions']['runtimeImage'], 'allocatable': '6915992Ki'}
    same_xavier = generate_case(gen, **inputs)
    state = same_xavier['excluded-batches'][0]['memoryPolicy']
    assert state['status'] == 'applied' and state['currentConditions']['memoryLimitBytes'] == 5665580646
    assert state['currentConditions']['memoryLimitStatus'] == 'confirmed'
    assert same_xavier['summary']['manifestCellCount'] == 0
    changed_xavier = generate_case(gen, **dict(inputs, allocatable='8Gi'))
    assert changed_xavier['excluded-batches'][0]['memoryPolicy']['status'] == 'conditions-changed'
    for missing in ({'allocatable': None}, {'allocatable': 'invalid'}, {'memory_fraction': None},
                    {'memory_fraction': 0}, {'node_uid': 'different-node-uid'}):
        unknown = generate_case(gen, **dict(inputs, **missing))
        state = unknown['excluded-batches'][0]['memoryPolicy']
        assert state['status'] == 'conditions-unknown'
        assert state['currentConditions']['memoryLimitStatus'] == 'unconfirmed'
    lower_policy = copy.deepcopy(rule)
    lower_policy['conditions']['memoryLimitBytes'] = 1024
    lower = generate_case(gen, phase='Failed', memory_policy=[lower_policy], policy_limit=1024)
    assert lower['excluded-batches'][0]['memoryPolicy']['status'] == 'applied'
    timing_generation_self_check(gen)


def timing_generation_self_check(gen):
    for obsolete in ({'image': 'old-runtime'}, {'image': 'new-runtime', 'curve': 'old-curve'},
                     {'image': 'new-runtime', 'evidence': 'old-evidence'}):
        planned = generate_case(gen, timing_seconds=61, **obsolete)
        assert planned['summary']['manifestCellCount'] == 1, planned['excluded-cells']
        assert planned['cells'][0]['batches'] == [1, 2]
        assert all(row['timingSource'] is None for row in planned['cells'][0]['batchEstimates'])
    current = generate_case(gen, phase='Failed', image='new-runtime', timing_seconds=61)
    assert current['summary']['manifestCellCount'] == 0
    assert [r['batch'] for r in current['excluded-batches']] == [1, 2]
    assert all(r['reason'] == gen.load(gen.HERE / 'coverage-plan.json')['excludedBatchReason']
               for r in current['excluded-batches'])
    higher_mode = generate_case(gen, image='new-runtime', timing_seconds=61,
        power_mode='high', timing_power_mode='low', variant=False, cell_annotation=False)
    assert higher_mode['summary']['manifestCellCount'] == 1, higher_mode['excluded-cells']
    assert higher_mode['cells'][0]['batches'] == [1, 2]
    assert all(row['timingSource'] is None for row in higher_mode['cells'][0]['batchEstimates'])
    same_mode = generate_case(gen, phase='Failed', image='new-runtime', timing_seconds=61,
                             power_mode='high', timing_power_mode='high')
    assert [r['batch'] for r in same_mode['excluded-batches']] == [1, 2]
    assert all(r['timingSource'] is None or r['timingSource']['powerMode'] == 'high'
               for r in same_mode['excluded-batches'])


class CoverageGenerationTest(unittest.TestCase):
    def test_generation_conditions(self):
        generation_self_check(load_generator())


if __name__ == '__main__':
    unittest.main()
