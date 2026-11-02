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
                  variant=True):
    """Exercise generate_coverage, not just its independent selection helpers."""
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        model, device, path = 'mobilenet-v2-050', 'raspberry-pi-5-8g', 'firstfloor-ort-cpu-arm64'
        cell = {'id': 'cell', 'model': {'name': model}, 'deviceClass': {'name': device},
                'executionPath': path, 'powerMode': 'fixed', 'support': {'verdict': 'Compatible'},
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
                                  'executionPath': path, 'powerMode': 'fixed', 'preprocessor': 'pre'}},
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
        inputs = {
            'measurement-coverage': {'completeness': 'complete', 'snapshotId': 'snapshot', 'cells': [cell]},
            'batch-energy-optimizations': {'items': [previous]},
            'profiles': {'profiles': [{'id': 'ns/coverage-old-runtime/uid', 'condition': {'runtimeImage': image}}]
                        if phase == 'Complete' else []},
            'inventory': {'classes': [{'name': device, 'architecture': 'arm64', 'memory': '8Gi',
                'nodes': [{'name': 'node', 'ready': 'True', 'schedulingDisabled': False}]}]},
            'models': {'items': [{'metadata': {'name': model}, 'spec': {
                'executionPaths': [{'name': path, 'artifact': 'artifact', 'runtime': {'family': 'firstfloor',
                    'backend': 'ort-cpu', 'settings': {'firstfloor': {'maxBatchSize': 2}}}}],
                'artifacts': [{'name': 'artifact', 'source': {'architecture': 'arm64'}}]}}]},
            'preprocessors': {'items': [{'metadata': {'name': 'pre'}, 'spec': {
                'artifacts': [{'target': {'model': model, 'artifact': 'artifact'}}], 'input': {'fields': []}}}]},
            'datasets': {'items': [{'metadata': {'name': 'dataset'}, 'spec': {'fields': []}}]}}
        values = root / 'runtime.json'
        values.write_text(json.dumps({'profiling': {'executionDeclarations': [{'deviceClass': device,
            'runtime': {'payload': json.dumps({'image': 'new-runtime', 'architecture': 'arm64',
                'runtime': {'family': 'firstfloor', 'backends': ['ort-cpu']}})}}]}}))
        policy = gen.load(gen.HERE / 'coverage-plan.json')
        policy['runtimeValues'] = str(values)
        if memory_policy is not None:
            policy['memoryMaxBatch'] = memory_policy
        policy_path = root / 'policy.json'
        policy_path.write_text(json.dumps(policy))
        facts = {'data': {'execution-policies.yaml': json.dumps({'policies': [{
            'deviceClass': device, 'executionPath': path, 'workloadMode': 'saturated-serial-fixed-batch',
            'runtimeMemoryLimitBytes': 17179869184}]})}}

        def api(url, body=None):
            assert body is None, 'generation must not submit or preflight requests'
            return copy.deepcopy(result if url.endswith('/result') else inputs[url.rsplit('/', 1)[-1]])

        output = root / 'out'
        with patch.object(gen, 'coverage_api', side_effect=api), \
                patch.object(gen.subprocess, 'check_output', return_value=json.dumps(facts).encode()), \
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


class CoverageGenerationTest(unittest.TestCase):
    def test_generation_conditions(self):
        generation_self_check(load_generator())


if __name__ == '__main__':
    unittest.main()
