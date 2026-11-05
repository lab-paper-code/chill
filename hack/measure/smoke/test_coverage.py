"""Offline generator consumer smoke: real manifests, mocked read-only inputs."""
import contextlib
import copy
import importlib.util
import io
import json
import os
import tempfile
import unittest
from collections import defaultdict
from pathlib import Path
from unittest.mock import patch


def load_coverage_plan():
    with tempfile.TemporaryDirectory() as temp:
        config = Path(temp) / 'environment.json'
        config.write_text(json.dumps({'paths': {'campaign': temp}, 'measure': {
            'capturedValues': temp, 'namespace': 'chill-system', 'authority': 'offline',
            'kubectl': 'offline-kubectl', 'kubeconfig': 'offline', 'kubeContext': 'offline'}}))
        with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(config)):
            spec = importlib.util.spec_from_file_location('coverage_plan', Path(__file__).parents[1] / 'coverage_plan.py')
            gen = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(gen)
        return gen


def generate_case(gen, image='old-runtime', phase='Complete', curve=None, evidence=None,
                  annotated=True, controlled=False, prefix=True, memory_policy=None, cell_annotation=True,
                  variant=True, allocatable='20Gi', memory_fraction=0.8, policy_limit=17179869184,
                  model='mobilenet-v2-050', device='raspberry-pi-5-8g', runtime_image='new-runtime',
                  node_uid='node-uid', timing_seconds=None, power_mode='fixed', timing_power_mode=None,
                  selection_available=True, current_id=None, display_status=None, workload_percentile=99,
                  planned_batches=(1, 2), declared_batches=()):
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
        cell['currentMeasurement'] = {'id': current_id or 'ns/coverage-old-runtime/uid',
                                      'measuredAt': '2026-01-01T00:00:00Z'}
        if source_mode != power_mode:
            cell['currentMeasurement'] = None  # timing belongs to another logical cell
        if display_status:
            cell['assessment']['displayStatus'] = display_status
        if not selection_available:
            del cell['currentMeasurement']
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
                                'creationTimestamp': '2026-01-01', 'annotations': annotations, 'namespace': 'ns'},
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
                    {'workloadMode': 'empirical-cadence-fixed-batch', 'windowSeconds': timing_seconds}]}]}
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
            'datasets': {'items': [{'metadata': {'name': 'dataset'}, 'spec': {'fields': []}}]},
            'workloads': [{'desired': {'metadata': {'name': 'actual-workload'}, 'spec': {
                'modelRef': {'name': model}, 'preprocessorRef': {'name': 'pre'},
                'datasetRef': {'name': 'dataset'}, 'slo': {'percentile': workload_percentile, 'targetLatency': '1s'}}}}]}
        if declared_batches:
            inputs["models"]["items"][0]["spec"]["artifacts"][0]["derivation"] = {"declaredBatchSizes": list(declared_batches)}
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
            'deviceClass': device, 'executionPath': path, 'workloadMode': 'empirical-cadence-fixed-batch',
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
            if url.endswith("/preflight"):
                assert body is not None and "batchSizes" not in body["spec"] and "retryLimit" not in body["spec"]
                return {"resolves": True, "batchPlan": {"plannedBatches": list(planned_batches)}}
            assert body is None, "generation must not submit requests"
            return copy.deepcopy(result if url.endswith('/result') else inputs[url.rsplit('/', 1)[-1]])

        output = root / 'out'
        with patch.object(gen, 'coverage_api', side_effect=api), \
                patch.object(gen.subprocess, 'check_output', side_effect=kubectl), \
                contextlib.redirect_stdout(io.StringIO()):
            gen.generate_coverage(output, policy_path)
        captured = {name: json.loads((output / (name + '.json')).read_text()) for name in
                    ('summary', 'cells', 'existing-cells', 'excluded-batches', 'excluded-cells', 'resume-plan')}
        captured['manifests'] = [document for path in output.glob('lanes/*.yaml')
                                 for document in gen.yaml.safe_load_all(path.read_text())]
        return captured


def generation_self_check(gen):
    old = generate_case(gen)
    assert old['summary']['staleRuntimeCellCount'] == 0
    assert old['summary']['manifestCellCount'] == 0
    assert old['summary']['currentRuntimeMeasuredCellCount'] == 1
    current = generate_case(gen, image='new-runtime')
    assert current['summary']['currentRuntimeMeasuredCellCount'] == 1
    complete = generate_case(gen, image='new-runtime', variant=False)
    assert complete['summary']['manifestCellCount'] == 0 and complete['summary']['currentRuntimeMeasuredCellCount'] == 1
    assert current['summary']['manifestCellCount'] == 0
    for phase in ('Measuring', ''):
        running = generate_case(gen, image='new-runtime', phase=phase)
        assert running['summary']['manifestCellCount'] == 0 and len(running['existing-cells']) == 1
        stale = generate_case(gen, phase=phase)
        assert stale['summary']['manifestCellCount'] == 0 and len(stale['existing-cells']) == 1
    assert generate_case(gen, image='new-runtime', phase='Failed')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', phase='Measuring', annotated=False)['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', curve='old-curve')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', evidence='old-evidence')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, controlled=True)['summary']['manifestCellCount'] == 0  # migrated shared evidence, CPU history is not a condition
    fallback = generate_case(gen, image='new-runtime', phase='Measuring', cell_annotation=False)
    assert fallback['existing-cells']
    assert generate_case(gen, image='new-runtime', phase='Measuring', curve='old-curve')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, image='new-runtime', phase='Measuring', evidence='old-evidence')['summary']['manifestCellCount'] == 1
    assert generate_case(gen, selection_available=False)['summary']['manifestCellCount'] == 0
    latest_failed = generate_case(gen, current_id='ns/newer-failed/failed-uid')
    assert latest_failed['summary']['manifestCellCount'] == 1 and not latest_failed['existing-cells']
    assert generate_case(gen, phase='Failed', display_status='insufficient')['summary']['manifestCellCount'] == 1
    rule = {'model': 'mobilenet-v2-050', 'deviceClass': 'raspberry-pi-5-8g', 'executionPath': None,
            'maxBatch': 1, 'conditions': {'runtimeImage': 'new-runtime',
                'executionPath': 'firstfloor-ort-cpu-arm64', 'memoryLimitBytes': 17179869184},
            'basis': 'observed-failure', 'observations': [{'kind': 'OOMKilled', 'batch': 2}]}
    same = generate_case(gen, phase='Failed', memory_policy=[rule])
    assert same['cells'][0]['batches'] == [1, 2] and not same['excluded-batches']
    assert same['cells'][0]['batchEstimates'][0]['memoryPolicy']['status'] == 'applied'
    conflict = generate_case(gen, phase='Failed', memory_policy=[rule], declared_batches=[1, 2])
    assert conflict['summary']['manifestCellCount'] == 0 and conflict['excluded-cells']
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
    assert lower['cells'][0]['batchEstimates'][0]['memoryPolicy']['status'] == 'applied'
    timing_generation_self_check(gen)


def timing_generation_self_check(gen):
    for obsolete in ({'image': 'new-runtime', 'curve': 'old-curve'},
                     {'image': 'new-runtime', 'evidence': 'old-evidence'}):
        planned = generate_case(gen, timing_seconds=61, **obsolete)
        assert planned['summary']['manifestCellCount'] == 1, planned['excluded-cells']
        assert planned['cells'][0]['batches'] == [1, 2]
        assert all(row['timingSource'] is None for row in planned['cells'][0]['batchEstimates'])
    for image in ('old-runtime', 'new-runtime'):
        planned = generate_case(gen, phase='Failed', image=image, timing_seconds=61)
        assert planned['summary']['manifestCellCount'] == 1
        assert planned['cells'][0]['batches'] == [1, 2]
        assert planned['excluded-batches'] == []
        assert planned['cells'][0]['minimumServiceSamplesPerTrial'] == 299
        spec = planned['manifests'][0]['spec']
        assert spec['activeProtocol']['workload'] == 'actual-workload'
        assert spec['activeProtocol']['durationSeconds'] == 30
        assert spec['trialsPerBatch'] == 3
    higher_mode = generate_case(gen, image='new-runtime', timing_seconds=61,
        power_mode='high', timing_power_mode='low', variant=False, cell_annotation=False)
    assert higher_mode['summary']['manifestCellCount'] == 1, higher_mode['excluded-cells']
    assert higher_mode['cells'][0]['batches'] == [1, 2]
    assert all(row['timingSource'] is None for row in higher_mode['cells'][0]['batchEstimates'])
    same_mode = generate_case(gen, phase='Failed', image='new-runtime', timing_seconds=61,
                             power_mode='high', timing_power_mode='high')
    assert same_mode['cells'][0]['batches'] == [1, 2] and not same_mode['excluded-batches']
    assert all(r['timingSource'] is None or r['timingSource']['powerMode'] == 'high'
               for r in same_mode['cells'][0]['batchEstimates'])


def coverage_self_check(gen):
    generation_self_check(gen)
    policy = gen.load(gen.HERE / "coverage-plan.json")
    model = "mobilenet-v2-050"
    cell = {"id": "cell", "model": {"name": model}, "deviceClass": {"name": "device"},
            "executionPath": "path", "powerMode": "mode", "support": {"verdict": "Compatible"},
            "assessment": {"displayStatus": "unmeasured", "reasons": []}}
    cell["currentMeasurement"] = None
    partial = copy.deepcopy(cell)
    partial["assessment"] = {"displayStatus": "insufficient", "reasons": [{"code": "TargetBatchMissing"}]}
    unknown = copy.deepcopy(cell)
    unknown["support"]["verdict"] = "Unknown"
    unsupported = copy.deepcopy(cell)
    unsupported["support"]["verdict"] = "Incompatible"
    other = copy.deepcopy(partial)
    other["assessment"]["reasons"] = [{"code": "Unrelated"}]
    running = dict(cell, assessment={"displayStatus": "running", "reasons": []})
    assert gen.coverage_targets({"completeness": "complete", "cells": [cell, partial, running, unknown, unsupported, other]},
                            policy, {}) == ([cell, partial, running, other], [unknown], [], [])
    missing = copy.deepcopy(unknown)
    missing["support"]["reasons"] = [{"code": "RuntimeDeclarationMissing"}]
    missing["assessment"]["displayStatus"] = "unknown"
    device = {"name": "device", "architecture": "amd64"}
    native_model = {"spec": {
        "executionPaths": [{"name": "path", "artifact": "a",
                            "runtime": {"family": "firstfloor", "backend": "ort-cuda"}}],
        "artifacts": [{"name": "a", "source": {"architecture": "amd64"}}]}}
    declarations = [{"deviceClass": "device", "node": "actual-node", "runtime": {"payload": json.dumps({
        "architecture": "amd64", "runtime": {"family": "firstfloor", "backends": ["ort-cuda"]}})}}]
    assert gen.coverage_declared_on_node(missing, "actual-node", device, native_model, declarations, {("device", "path")})
    assert not gen.coverage_declared_on_node(missing, "other-node", device, native_model, declarations, {("device", "path")})
    assert not gen.coverage_declared_on_node(missing, "actual-node", device, native_model, declarations, set())
    assert not gen.coverage_declared_on_node(unknown, "actual-node", device, native_model, declarations, {("device", "path")})
    assert not gen.coverage_declared_on_node(missing, "actual-node", dict(device, architecture="arm64"),
                                        native_model, declarations, {("device", "path")})
    assert gen.coverage_targets({"completeness": "complete", "cells": [missing]}, policy,
                            {}, {missing["id"]: "actual-node"}) == ([missing], [], [], [])
    older = copy.deepcopy(declarations[0])
    newer = copy.deepcopy(older)
    older["runtime"]["payload"] = json.dumps(dict(json.loads(older["runtime"]["payload"]), image="old-image"))
    newer["runtime"]["payload"] = json.dumps(dict(json.loads(newer["runtime"]["payload"]), image="new-image"))
    assert gen.coverage_required_condition(cell, "actual-node", native_model, [older, newer])["runtimeImage"] == "new-image"
    result = {"result": {"curvePlan": {"schemaVersion": gen.SHARED_CURVE_SCHEMA},
                         "completedTrials": [{"evidence": {"schemaVersion": gen.SHARED_EVIDENCE_SCHEMA}}]}}
    assert gen.coverage_shared_result(result)
    old_result = copy.deepcopy(result)
    old_result["result"]["curvePlan"]["schemaVersion"] = "chill.dacs.io/curve-plan.v1alpha10"
    assert not gen.coverage_shared_result(old_result)
    old_result = copy.deepcopy(result)
    old_result["result"]["completedTrials"][0]["evidence"]["schemaVersion"] = "chill.dacs.io/profiling-execution-evidence.v1alpha16"
    assert not gen.coverage_shared_result(old_result)
    assert gen.coverage_runtime_images(gen.HERE.parents[1] / policy["runtimeValues"])
    plan = {"metadata": {"name": "old", "creationTimestamp": "2026-01-01"},
            "status": {"phase": "Complete"},
            "spec": {"state": {"targetNode": "node", "cpuRequest": "4", "candidate": {
                "modelSpec": model, "deviceClass": "device", "executionPath": "path",
                "powerMode": "mode", "preprocessor": "pre"}},
                "activeProtocol": {"durationSeconds": 10, "dataset": "dataset"},
                "idleProtocol": {"durationSeconds": 10}, "cpuQuota": "4", "cpuAffinity": [3],
                "batchSizes": [1], "retryLimit": 1}}
    newer = copy.deepcopy(plan)
    newer["metadata"] = {"name": "new", "creationTimestamp": "2026-02-01"}
    spec, source = gen.coverage_defaults(cell, "node", [plan, newer], [])
    assert source == "request:new" and "cpuQuota" not in spec and "cpuRequest" not in spec["state"]
    assert "cpuAffinity" not in spec
    spec, source = gen.coverage_defaults(cell, "different-node", [plan, newer], [])
    assert source == "request:new" and "cpuQuota" not in spec and "cpuRequest" not in spec["state"]
    assert "batchSizes" not in spec and "retryLimit" not in spec and spec["activeProtocol"]["dataset"] == "dataset"
    assert plan["spec"]["batchSizes"] == [1] and plan["spec"]["retryLimit"] == 1
    assert "cpuAffinity" in plan["spec"]  # inheritance never mutates retained requests
    profile = {"id": "profile", "nodeName": "node", "measuredAt": "2026-03-01", "condition": {
        "logicalModel": model, "deviceClass": "device", "artifact": {"executionPath": "path"},
        "powerMode": "mode", "preprocessor": {"name": "pre"}, "cpu": {"request": "2", "limit": "2"}}}
    for field in ("activeProtocol", "idleProtocol"):
        profile["condition"][field] = {"durationSeconds": 10, "warmupIterations": 2,
                                      "settlingDurationSeconds": 2, "dataset": {"name": "dataset"},
                                      "metricIntervalMilliseconds": 1000, "metricTimeoutMilliseconds": 500,
                                      "metricDurationSeconds": 12}
    different = copy.deepcopy(plan)
    different["spec"]["state"]["targetNode"] = "other"
    spec, source = gen.coverage_defaults(cell, "node", [different], [profile])
    assert source == "profile:profile" and "retryLimit" not in spec
    assert "cpuQuota" not in spec and "cpuRequest" not in spec["state"]
    assert "settlingDurationSeconds" not in spec["activeProtocol"]
    execution_conditions = {"runtimeImage": "current-runtime", "curveSchema": gen.SHARED_CURVE_SCHEMA,
                            "evidenceSchema": gen.SHARED_EVIDENCE_SCHEMA}
    historical_conditions = dict(execution_conditions, runtimeImage="historical-runtime")
    assert gen.coverage_condition_matches(historical_conditions, execution_conditions)
    assert not gen.coverage_condition_matches(dict(historical_conditions, curveSchema="old-schema"), execution_conditions)
    timings = [{"model": model, "deviceClass": device, "batch": batch, "secondsPerCall": seconds,
                "executionPath": "path", "powerMode": cell["powerMode"], "source": device,
                "executionConditions": execution_conditions}
               for device, batch, seconds in
               [("device", 1, .1), ("device", 4, .4), ("slow", 16, 10)]]
    other_cells = [dict(timings[0], **{key: "other"}, secondsPerCall=600)
                   for key in ("model", "deviceClass", "executionPath", "powerMode")]
    assert gen.coverage_batch_plan(cell, [1], timings + other_cells, policy,
                               execution_conditions=execution_conditions)[1][0]["secondsPerCall"] == .1
    historical_timing = dict(timings[0], executionConditions=historical_conditions, secondsPerCall=.2)
    assert gen.coverage_batch_plan(cell, [1], [historical_timing], policy,
                               execution_conditions=execution_conditions)[1][0]["secondsPerCall"] == .2
    included, estimates, reason = gen.coverage_batch_plan(cell, [1, 3, 16], timings, policy, execution_conditions=execution_conditions)
    assert included == [1, 3, 16] and reason is None
    assert estimates[-1]["secondsPerCall"] is None  # another class is not grounds for exclusion
    assert estimates[1]["estimatedCallsInWindow"] is None
    slow = dict(timings[0], secondsPerCall=60.001)
    included, estimates, reason = gen.coverage_batch_plan(cell, [1, 2], [slow], policy, execution_conditions=execution_conditions)
    assert included == [1, 2] and reason is None and estimates[0]["estimatedCallsInWindow"] == 0
    fast = timings[0]
    mixed = [fast, dict(fast, batch=2, secondsPerCall=61), dict(fast, batch=4, secondsPerCall=.1)]
    included, estimates, reason = gen.coverage_batch_plan(cell, [4, 1, 2], mixed, policy, execution_conditions=execution_conditions)
    assert included == [1, 2, 4] and reason is None
    active = {"durationSeconds": 60, "durationToleranceSeconds": 0,
              "metricObservation": {"durationSeconds": 62}}
    gen.coverage_active_protocol(active, policy)
    assert active["durationSeconds"] == 30 and active["durationToleranceSeconds"] == 0
    assert active["metricObservation"]["durationSeconds"] == 32
    facts = {"condition": {"logicalModel": model, "deviceClass": "device", "artifact": {"executionPath": "path"},
                          "powerMode": "mode", "runtimeImage": execution_conditions["runtimeImage"]},
             "request": {"name": "source", "uid": "source-uid"},
             "batches": [{"batch": 1, "completedCalls": 100, "quality": [
                 {"workloadMode": "empirical-cadence-fixed-batch", "windowSeconds": 10},
                 {"workloadMode": "idle-model-loaded", "windowSeconds": 500}]}]}
    timed_result = dict(result, measurementFacts=facts)
    assert gen.coverage_timings([timed_result])[0]["secondsPerCall"] == .1
    assert gen.coverage_timings([timed_result])[0]["executionConditions"] == execution_conditions
    assert gen.coverage_timings([dict(old_result, measurementFacts=facts)]) == []
    assert gen.coverage_timings([{"measurementFacts": None}]) == []
    assert gen.coverage_batch_plan(cell, [1], [], policy)[0] == [1]
    heavy_model = max(policy["models"], key=lambda name: policy["models"][name]["priority"])
    classes = {
        "arm": {"architecture": "arm64", "memory": "64Gi"},
        "amd": {"architecture": "amd64", "memory": "8Gi"},
        "gpu": {"architecture": "amd64", "memory": "64Gi"},
        "nano": {"architecture": "arm64", "memory": "4096Mi", "accelerator": "nvidia-jetson-nano"},
        "orin": {"architecture": "arm64", "memory": "8Gi", "accelerator": "nvidia-jetson-agx-orin"},
        "rk": {"architecture": "arm64", "memory": "16Gi", "accelerator": "rk3588-npu"}}
    pilot_lanes = defaultdict(list)
    cases = [("arm", "ort-cpu", "CPU arm64"), ("amd", "ort-cpu", "CPU amd64"),
             ("gpu", "ort-cuda", "CUDA amd64"), ("nano", "ort-cuda", "L4T r32"),
             ("orin", "ort-cuda", "L4T r35"), ("rk", "rknnlite", "RKNN"),
             ("nano", "ort-cpu", "CPU arm64"), ("orin", "ort-cpu", "CPU arm64")]
    for index, (node, path, family) in enumerate(cases):
        m = heavy_model if index >= 6 else model
        row = {"node": node, "model": m, "cellId": str(index),
               "priority": policy["models"][m]["priority"], "estimatedSeconds": 100 if index >= 6 else 1}
        candidate = {"deviceClass": node, "executionPath": path}
        assert gen.coverage_runtime_family(candidate, classes[node]) == family
        pilot_lanes[node].append((row, {"spec": {"state": {"candidate": candidate}}}))
    selected = gen.coverage_pilot(pilot_lanes, policy, classes)
    assert {r["cellId"] for lane in selected.values() for r, _ in lane} == set(map(str, range(7)))
    assert len(selected["nano"]) == 2  # CUDA family and heavy CPU model share an ordered lane
    assert selected["nano"][0][1] is pilot_lanes["nano"][0][1]
    assert gen.coverage_pilot({"arm": pilot_lanes["arm"]}, policy, classes) == {"arm": pilot_lanes["arm"]}
    assert gen.coverage_pilot({}, policy, classes) == {}
    assert gen.coverage_memory("4Gi") == gen.coverage_memory("4096Mi") == gen.coverage_memory("4294967296")
    lane = [
        ({"powerMode": "fixed", "model": model, "cellId": str(index)},
         {"spec": {"state": {"candidate": {"executionPath": path}}}})
        for index, path in enumerate(("firstfloor-ort-cuda-vlm-arm64", "firstfloor-ort-cpu-arm64",
                                     "firstfloor-ort-cuda-arm64", "firstfloor-ort-cuda-vlm-arm64",
                                     "firstfloor-ort-cpu-int8-arm64"))]
    for index, m in ((5, "smolvlm2-500m-video-instruct"), (6, model)):
        lane.append(({"powerMode": "fixed", "model": m, "cellId": str(index)},
                     {"spec": {"state": {"candidate": {"executionPath": "firstfloor-ort-cuda-amd64"}}}}))
    assert [r["cellId"] for r, _ in gen.coverage_lane_order(lane, policy)] == ["1", "2", "4", "6", "0", "3", "5"]
    print("coverage generation/reuse, scoped memory exclusions, inheritance, batches and runtime-family pilot checked")


class CoverageGenerationTest(unittest.TestCase):
    def test_server_preflight_owns_candidate_domain(self):
        gen = load_coverage_plan()
        for candidates in ([1], [1, 4]):
            planned = generate_case(gen, phase='Failed', planned_batches=candidates)
            self.assertEqual(planned['cells'][0]['candidateBatches'], candidates)
            self.assertEqual(planned['cells'][0]['batches'], candidates)
            self.assertNotIn('batchSizes', planned['manifests'][0]['spec'])
            self.assertNotIn('retryLimit', planned['manifests'][0]['spec'])

    def test_actual_workload_percentile_and_unambiguous_source(self):
        gen = load_coverage_plan()
        planned = generate_case(gen, phase='Failed', workload_percentile=95)
        self.assertEqual(planned['cells'][0]['minimumServiceSamplesPerTrial'], 59)
        with self.assertRaises(ValueError):
            generate_case(gen, phase='Failed', workload_percentile=100)
        spec = {'state': {'candidate': {'modelSpec': 'model', 'preprocessor': 'pre'}},
                'activeProtocol': {'dataset': 'data'}}
        workload = {'desired': {'metadata': {'name': 'actual'}, 'spec': {
            'modelRef': {'name': 'model'}, 'preprocessorRef': {'name': 'pre'},
            'datasetRef': {'name': 'data'}, 'slo': {'percentile': 99}}}}
        self.assertEqual(gen.coverage_workload(copy.deepcopy(spec), [workload]), 299)
        for sources in ([], [workload, copy.deepcopy(workload)]):
            with self.assertRaises(ValueError):
                gen.coverage_workload(copy.deepcopy(spec), sources)
        wrong_inputs = copy.deepcopy(workload)
        wrong_inputs['desired']['spec']['datasetRef']['name'] = 'other'
        with self.assertRaises(ValueError):
            gen.coverage_workload(copy.deepcopy(spec), [wrong_inputs])

    def test_generation_conditions(self):
        coverage_self_check(load_coverage_plan())


if __name__ == '__main__':
    unittest.main()
