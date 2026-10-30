#!/usr/bin/env python3
"""Offline bind/check/judge and immutable private publication; released CPO campaign control.
Cleanup is one server reset with retained steps/ACKs; no client cleanup assembly.
No implicit phase advance, retry, runtime build, fault injection, or guessed phase6 values.
Settings GET/PUT use at least 30s (or the larger configured apiSeconds).
A timed-out settings PUT is never resent: matching GET readback confirms application.
Factor reset polls fresh physical instance ACKs for timeboxes.factorAckWaitSeconds (default 120s).
failure-select uses config.failureSelectSnapshotTTLSeconds (default 15; finite >0)
for snapshot wall-clock age and operator expiry, not CHILL's internal observation age.
Client JSONL non-object/malformed lines remain in raw evidence and judge coverage;
they do not stop collection or replace missing end-counter evidence.
Targeted full tagged GO preserves restored static route weights when enabling
autonomy; start-state-restoration.json retains the immediate post-GO snapshot and
compares its routes with the restored fingerprint (mismatch stops with exit 2).
The first full campaign's freeze/release boundary remains separate.
Row tags accept ASCII letters/digits (for example 3b), not reserved round numbers.
Full restoration receipts select targeted GO mode; multiple stored tags require an
explicit --row-tag for summary selection.
"""
import argparse
import base64
import bisect
from collections import Counter, defaultdict
import copy
import datetime as dt
import hashlib
import importlib.util
import inspect
import json
import math
import os
from pathlib import Path
import re
import selectors
import ssl
import shlex
import subprocess
import sys
import time
import threading
import urllib.parse
import urllib.error
import urllib.request
import zlib

HERE = Path(__file__).resolve().parent
PRODUCER_ALIASES = {'mobilenet': 'image-classification-tight-slo', 'plc': 'plc-anomaly', 'smolvlm2': 'smolvlm2-vqa'}


def freeze_producer_config(config):
    resources = config.get('producerResources', {'cpu': '2', 'memory': '1Gi'})
    if not isinstance(resources, dict) or set(resources) != {'cpu', 'memory'}:
        raise ValueError('producerResources requires cpu and memory quantity strings')
    for name, pattern in (
        ('cpu', r'([0-9]+(?:\.[0-9]+)?|\.[0-9]+)(m?)'),
        ('memory', r'([0-9]+(?:\.[0-9]+)?|\.[0-9]+)([EPTGMK]i|[EPTGMkKmun]|[eE][+-]?[0-9]+)?'),
    ):
        value = resources[name]
        match = re.fullmatch(pattern, value) if isinstance(value, str) else None
        if not match or not any(digit in '123456789' for digit in match[1]):
            raise ValueError('producerResources.' + name + ' requires a positive Kubernetes quantity string')
        if name == 'cpu':
            fractional = match[1].partition('.')[2].rstrip('0')
            if len(fractional) > (0 if match[2] else 3):
                raise ValueError('producerResources.cpu requires precision of at least one millicore')
    shards = config.get('producerShards', {'mobilenet': 3, 'plc': 1, 'smolvlm2': 1})
    if not isinstance(shards, dict) or set(shards) != set(PRODUCER_ALIASES) or any(type(v) is not int or v < 1 for v in shards.values()):
        raise ValueError('producerShards requires positive integer mobilenet/plc/smolvlm2 counts')
    config['producerResources'], config['producerShards'] = dict(resources), dict(shards)


def set_producer_resources(pod, config):
    for container in pod['containers'] + pod.get('initContainers', []):
        resources = container.setdefault('resources', {})
        for field in ('requests', 'limits'):
            resources.setdefault(field, {}).update(config['producerResources'])


def approval_sources(approval):
    if 'sourceSHA' in approval:
        raise ValueError('Single sourceSHA is removed; explicit server/loadgen/power sources required')
    sources = approval.get('sources')
    if not isinstance(sources, dict) or set(sources) != {'server', 'loadgen', 'power'}:
        raise ValueError('sources requires exact server/loadgen/power component identities')
    for identity in sources.values():
        if not isinstance(identity, str) or not (
            re.fullmatch('[0-9a-f]{40}', identity)
            or re.fullmatch(r'[A-Za-z0-9_-]+(?:/[A-Za-z0-9_.-]+)+', identity)
        ) or '..' in Path(identity).parts:
            raise ValueError('Component source requires a full Git SHA or pinned repository-relative binary path')
    return dict(sources)


def require_component_source(receipt, identity, component, executable=None):
    if re.fullmatch('[0-9a-f]{40}', identity):
        matches = receipt.get('sourceSHA') == identity
    else:
        expected = REPO / identity
        actual = receipt.get('executable')
        matches = isinstance(actual, str) and Path(actual).resolve() == expected.resolve()
        if executable is not None:
            matches = matches and Path(executable).resolve() == expected.resolve()
    if not matches:
        raise ValueError(component + ' receipt differs from frozen component source identity')


def validate_g0_evidence(evidence, verdict, binding, weighted_capacity, expected_rates, plc_slo):
    """Judge gateway-only G0 criteria directly; aggregate/Smol SLO verdicts are references."""
    def finite(value, minimum=0):
        return type(value) in (int, float) and math.isfinite(value) and value >= minimum
    def matches(actual, expected):
        return finite(actual) and math.isclose(actual, expected, rel_tol=1e-9, abs_tol=1e-9)
    try:
        if verdict['generatorQualityStatus'] != 'VALID':
            raise ValueError('G0 requires generator VALID')
        if not finite(weighted_capacity) or weighted_capacity <= 0 or not matches(verdict['configuredRateTotal'], 1.2 * weighted_capacity):
            raise ValueError('G0 alpha=1.2 total differs from final weighted capacity')
        if set(expected_rates) != {'image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa'}:
            raise ValueError('G0 requires all three final workload rates')
        stages = binding['schedule']
        if binding['schema'] != 'demo-e5-binding.v1' or len(stages) != 1 or stages[0]['id'] != 'G-check' or stages[0]['seconds'] != 120 or len(stages[0]['parts']) != 1:
            raise ValueError('G0 requires the frozen one-shot 120s G-check binding')
        part = stages[0]['parts'][0]
        if set(part['alpha']) != {'mobilenet', 'plc', 'smolvlm2'} or any(not matches(v, 1.2) for v in part['alpha'].values()):
            raise ValueError('G0 binding must record alpha=1.2 for every workload')
        if set(verdict['workloads']) != set(expected_rates) or set(part['rates']) != set(expected_rates) or not matches(sum(expected_rates.values()), 1.2 * weighted_capacity):
            raise ValueError('G0 workload mix differs from final weighted capacity')
        for name, rate in expected_rates.items():
            row = verdict['workloads'][name]
            if not matches(row['rate'], rate) or not matches(part['rates'][name], rate):
                raise ValueError('G0 recorded workload rate differs from final approved mix')
            if type(row['planned']) is not int or row['planned'] <= 0 or type(row['issued']) is not int or row['planned'] != row['issued'] or row['planned'] != stages[0]['traces'][name]['offered'] or row['supplied'] is not True or row['quality']['status'] != 'VALID':
                raise ValueError('G0 requires complete frozen planned=offered=issued supply and generator VALID')
        limit, peak = evidence['csCPULimitCores'], verdict['cpuCoresPeak']
        if not finite(limit) or limit <= 0 or not finite(peak) or peak >= limit:
            raise ValueError('G0 cs CPU fraction must be recorded peak / frozen positive limit <1')
        samples = verdict['cpu10sSamples']
        if not samples or any(not finite(row['cpuCores']) or row['cpuCores'] > peak for row in samples):
            raise ValueError('G0 CPU samples missing or inconsistent with recorded peak')
        plc = verdict['workloads']['plc-anomaly']
        recorded_slo = binding['demoConfig']['measurementSLO']['plc']
        if plc_slo != recorded_slo or plc_slo['percentile'] != 95 or not finite(plc_slo['targetLatencyNanos']) or plc_slo['targetLatencyNanos'] <= 0:
            raise ValueError('G0 PLC SLO differs from final approved p95 SLO')
        p95 = plc['receiptLatencyMS']['p95']
        if not finite(p95) or p95 * 1e6 > plc_slo['targetLatencyNanos']:
            raise ValueError('G0 PLC receive-origin p95 exceeds approved SLO')
    except (KeyError, TypeError, IndexError, AttributeError) as error:
        raise ValueError('Incomplete G0 evidence: ' + str(error)) from error


def self_check_g0():
    """Explicit synthetic evidence, never a claim about a historical campaign."""
    names = ['image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa']
    rates = dict(zip(names, [.54, .12, .54]))
    slo = {'percentile': 95, 'targetLatencyNanos': 32_000_000}
    evidence = {'csCPULimitCores': 2}
    verdict = {'status': 'PASS', 'generatorQualityStatus': 'VALID', 'configuredRateTotal': 1.2,
               'cpuCoresPeak': 1, 'cpu10sSamples': [{'cpuCores': 1}],
               'workloads': {name: {'rate': rate, 'planned': 10, 'issued': 10, 'supplied': True, 'quality': {'status': 'VALID'}} for name, rate in rates.items()}}
    verdict['workloads']['plc-anomaly']['receiptLatencyMS'] = {'p95': 20}
    binding = {'schema': 'demo-e5-binding.v1', 'demoConfig': {'measurementSLO': {'plc': slo}},
               'schedule': [{'id': 'G-check', 'seconds': 120, 'traces': {name: {'offered': 10} for name in names}, 'parts': [{'alpha': dict.fromkeys(['mobilenet', 'plc', 'smolvlm2'], 1.2), 'rates': rates}]}]}
    validate_g0_evidence(evidence, verdict, binding, 1, rates, slo)
    smol_failure = copy.deepcopy(verdict)
    smol_failure.update(status='FAIL', GCertified=False)
    smol_failure['workloads']['smolvlm2-vqa'].update(SLOVerdict='FAIL', successFraction=.4)
    validate_g0_evidence(evidence, smol_failure, binding, 1, rates, slo)
    bad_cases = [({}, evidence, binding),
                 (dict(verdict, generatorQualityStatus='UNKNOWN'), evidence, binding),
                 (dict(verdict, configuredRateTotal=1.3), evidence, binding),
                 (dict(verdict, cpuCoresPeak=2), evidence, binding), (verdict, {}, binding), (verdict, evidence, {})]
    for field, value in [('issued', 9), ('supplied', False), ('rate', .13), ('quality', {'status': 'INVALID_GENERATOR'}), ('receiptLatencyMS', {'p95': 33})]:
        bad = copy.deepcopy(verdict)
        bad['workloads']['plc-anomaly'][field] = value
        bad_cases.append((bad, evidence, binding))
    for bad, record, frozen in bad_cases:
        try:
            validate_g0_evidence(record, bad, frozen, 1, rates, slo)
        except ValueError:
            pass
        else:
            raise AssertionError('Invalid/incomplete/mismatched G0 evidence accepted')
    try:
        validate_g0_evidence(evidence, verdict, binding, 1, rates, dict(slo, targetLatencyNanos=36_000_000))
    except ValueError:
        pass
    else:
        raise AssertionError('Mismatched approved PLC SLO accepted')


def normative_stage_specs(burst_low_seconds):
    return [
        ('E5-0', 600, [0, 0, 0]), ('E5-1', 1800, [.3]*3),
        ('E5-2-low', burst_low_seconds, [.3]*3),
        ('E5-2-high', 900-burst_low_seconds, [.3, .3, .9]),
        ('E5-3-50', 600, [.5]*3), ('E5-3-70', 600, [.7]*3),
        ('E5-3-90', 600, [.9]*3), ('E5-4', 900, [1.2]*3),
        ('E5-5-low', 600, [.3]*3), ('E5-5-high', 600, [.9]*3),
        ('E5-6', 3600, [.3]*3), ('E5-7', 900, [.3]*3),
    ]


def failure_select_snapshot_ttl(config):
    seconds=config.get('failureSelectSnapshotTTLSeconds',15)
    if not positive(seconds):raise ValueError('failureSelectSnapshotTTLSeconds must be finite and greater than zero')
    return seconds


def serving_failure_targets(state, load_window, uids, excluded_nodes=()):
    """Intersect API-ready physical identities, positive routes and window assignments."""
    snapshot=state['snapshot']; candidates=[]
    assignments={w['uid']:w.get('instanceAssignments',{}) for w in load_window['workloads']}
    live_nodes={n['node']['name']:n['node']['uid'] for n in snapshot['nodes'] if n.get('ready')}
    for instance in instance_rows(snapshot['instances']):
        uid=instance.get('workloadUID'); node=instance['node']
        weight=(snapshot.get('routing',{}).get(uid) or {}).get(instance['id'],0)
        assigned=assignments.get(uid,{}).get(instance['id'],0)
        if uid not in uids or instance.get('ownerWorkloadUID')!=uid or not instance.get('ready') or instance.get('stopped') or weight<=0 or assigned<=0 or node['uid'] in excluded_nodes:
            continue
        if not instance.get('podUID') or not instance.get('podName') or live_nodes.get(node['name'])!=node['uid']:continue
        candidates.append({'instanceID':instance['id'],'workloadUID':uid,'routingWeight':weight,'assigned':assigned,
                           'podName':instance['podName'],'podUID':instance['podUID'],
                           'nodeName':node['name'],'nodeUID':node['uid'],
                           'snapshotAt':snapshot['at'],'windowFrom':load_window['from'],'windowTo':load_window['to']})
    return sorted(candidates,key=lambda item:(item['nodeName'],item['podName'],item['instanceID']))


def validate_failure_events(evidence, start_utc, end_utc, epoch=None):
    """Check recorded manual evidence, not success or recommended injection offsets."""
    kinds = ('delete-serving-pod', 'cordon-delete', 'uncordon')
    events = evidence.get('events')
    if evidence.get('status')=='INVALID':raise ValueError('INVALID: '+evidence.get('reason','Failure target selection rejected'))
    if evidence.get('stage') != 'E5-7' or not isinstance(events, list) or len(events) != 3:
        raise ValueError('E5-7 requires all three actual operator command records')
    if evidence.get('selectionPolicy')=='active-serving-v1':
        if epoch is None:raise ValueError('INVALID: active selection requires the RFC3339Nano timestamp parser')
        selections=evidence.get('selections',[])
        if len(selections)!=2 or selections[0]['nodeUID']==selections[1]['nodeUID']:
            raise ValueError('INVALID: two distinct active serving Node selections required')
        for event,selection in zip(events[:2],selections):
            if selection.get('status')!='SELECTED' or any(event.get(key)!=selection.get(key) for key in ('kind','podUID','nodeUID')) or selection.get('routingWeight',0)<=0 or selection.get('assigned',0)<=0:
                raise ValueError('INVALID: command target differs from positive route/assigned selection')
            candidates=serving_failure_targets(load(selection['stateRef']),load(selection['loadRef']),selection['allowedWorkloadUIDs'])
            keys=('instanceID','workloadUID','routingWeight','assigned','podName','podUID','nodeName','nodeUID','snapshotAt','windowFrom','windowTo')
            if not any(all(candidate[key]==selection[key] for key in keys) for candidate in candidates):
                raise ValueError('INVALID: selection is not supported by captured scheduler state/window evidence')
            selected=epoch(selection['selectedAtUTC']);at=epoch(event['atUTC']);snapshot_at=epoch(selection['snapshotAt'])
            if any(value is None for value in (selected,at,snapshot_at)) or not snapshot_at<=selected<=at<=snapshot_at+selection['maximumAgeSeconds'] or epoch(selection['windowTo'])!=snapshot_at:
                raise ValueError('INVALID: selection expired before operator injection')
            options=load(selection['deleteOptionsRef'])
            if options!={'apiVersion':'v1','kind':'DeleteOptions','preconditions':{'uid':selection['podUID']}}:
                raise ValueError('INVALID: deletion requires UID-only precondition, no grace override')
            command=load(event['commandRef'])
            expected=selection['deleteArgv']
            actual=command.get('deleteArgv',command.get('argv'))
            if actual!=expected:raise ValueError('INVALID: recorded delete command differs from selected UID-precondition command')
    start = dt.datetime.fromisoformat(start_utc.replace('Z', '+00:00')).timestamp()
    end = dt.datetime.fromisoformat(end_utc.replace('Z', '+00:00')).timestamp()
    for event, kind in zip(events, kinds):
        at = dt.datetime.fromisoformat(event['atUTC'].replace('Z', '+00:00')).timestamp()
        if (event.get('kind') != kind or type(event.get('exitCode')) is not int
                or not start <= at <= end
                or not event.get('nodeUID') or not event.get('commandRef') or not event.get('responseRef')
                or kind != 'uncordon' and not event.get('podUID')):
            raise ValueError('E5-7 operator command identity/timestamp/result incomplete')
    if events[1]['nodeUID'] != events[2]['nodeUID']:
        raise ValueError('E5-7 cordon/uncordon Node identity differs')
    # Recovery comes only from the API scheduler/cluster streams, never operator readbacks.


def d25_acceptance(cr, raw):
    """Current identified-idle contract; preserve signed d and report idle-only range."""
    def finite(value):
        return type(value) in (int, float) and math.isfinite(value)
    idle, idle_ns, signed = raw.get('idleFraction'), raw.get('idleNanosPerRequest'), raw.get('perRequestNanos')
    configured_idle = raw.get('conditions', {}).get('maximumIdleFraction')
    if type(configured_idle) not in (str, int, float):
        raise ValueError('d25 frozen maximumIdleFraction missing')
    maximum_idle_fraction = float(configured_idle)
    if not finite(maximum_idle_fraction) or not 0 <= maximum_idle_fraction <= 1:
        raise ValueError('d25 frozen maximumIdleFraction must be in [0,1]')
    if (cr.get('status', {}).get('phase') != 'Complete' or not cr.get('metadata', {}).get('uid')
            or raw.get('verdict') != 'Accepted' or raw.get('unknownRows') != 0
            or raw.get('idleIdentified') is not True or raw.get('drainError')
            or not finite(idle) or not 0 <= idle <= maximum_idle_fraction
            or not finite(idle_ns) or not 0 <= idle_ns <= 1e6
            or not finite(signed) or cr['status'].get('perRequestNanos') != signed):
        raise ValueError('d25 Accepted/complete/unknown/identified idle/signed evidence incomplete')
    if (not raw.get('loadJob', {}).get('name')
            or not str(raw['loadJob'].get('state', '')).startswith('Complete ')
            or not raw.get('drainState')
            or type(raw.get('batches')) is not int or raw['batches'] <= 0
            or type(raw.get('cycles')) is not int or raw['cycles'] <= 0):
        raise ValueError('d25 actual completed load/execution/drain evidence incomplete')
    native = raw['drainState'].get('native')
    if not isinstance(native, dict) or not native or any(
            state.get('requests') != [] for state in native.values()):
        raise ValueError('d25 native execution drain evidence incomplete')
    return {'perRequestNanos': signed, 'idleFraction': idle, 'idleNanosPerRequest': idle_ns,
            'maximumIdleFraction': maximum_idle_fraction,
            'dRange': [signed-idle_ns, signed], 'kappaErrorBound': idle,
            'meaning': 'Identified idle-only range, not a confidence interval; signed raw retained, planner consumes max(d,0)'}


def profiling_evaluation_separation(workload, payload):
    if 'profilingRecordIDs' not in workload:
        return {'status':'UNKNOWN','reason':'Actual profiling record enumeration/pinned-carrier split proof not retained; not inferred from absent/empty IDs'}
    if set(workload['profilingRecordIDs']) & {record['recordID'] for record in payload}:
        raise ValueError('Profiling and evaluation record IDs overlap')
    return {'status':'DISJOINT_RECORD_IDS','reason':'Provided profiling IDs do not overlap evaluation IDs; pinned-carrier equality is not inferred'}


def self_check():
    settings = {'maxCPUs': 3, 'staticRouting': {}, 'staticRoutingValidatedAt': 'read-only',
                'powerObservation': {}, 'meters': [{'id': 'accepted-meter'}], 'requestTimeout': 30_000_000_000}
    writer = object.__new__(CPO)
    writer.api = lambda method, path, body: (method, path, body)
    method, path, body = writer.put(settings)
    assert (method, path, body) == ('PUT', '/api/v1/scheduler/settings', {'maxCPUs': 3})
    assert 'meters' in settings and 'staticRouting' in settings
    import io
    import tempfile
    from types import SimpleNamespace
    with tempfile.TemporaryDirectory(prefix='e5-credentials-') as temporary:
        operator=Path(temporary)/'operator';collector=Path(temporary)/'collector'
        operator.write_text('operator-fixture');collector.write_text('collector-fixture')
        connection=module('e5_credential_check',D2/'conduct_before.py')
        connection.BASE='https://fixture.invalid';connection.TOKEN=operator
        client=object.__new__(connection.Conductor)
        client.collector_token_file=collector;client.record=lambda record:None
        wire=module('e5_wire_credential_check',HERE/'e5-wire.py')
        prepare=object.__new__(wire.Prepare)
        prepare.op={'endpoint':connection.BASE,'tokenFile':str(operator),'collectorTokenFile':str(collector)}
        prepare.tb={'apiSeconds':1}
        for method in ('GET','PUT','POST'):
            expected='Bearer '+('collector-fixture' if method=='GET' else 'operator-fixture')
            def opened(request,timeout):
                assert request.get_header('Authorization')==expected
                response=io.BytesIO(b'{}');response.status=200
                return response
            client.http=prepare.http=SimpleNamespace(open=opened)
            assert client.api(method,'/api/v1/scheduler/settings')=={}
            assert prepare.api(method,'/api/v1/scheduler/settings')==({},200)
    spec=importlib.util.spec_from_file_location('e5_check_analysis',HERE/'d2/analyze_before.py')
    analysis=importlib.util.module_from_spec(spec);spec.loader.exec_module(analysis)
    rows=[{'requestIndex':0,'scheduledOffsetNs':0,'scheduledMonotonicNS':100,'sentMonotonicNS':60_000_100,'finishedMonotonicNS':61_000_100,'latencyMs':1,'serviceLatencyMs':1}]
    assert analysis.scheduled_latency_ns(rows[0],None)==61_000_000
    assert analysis.scheduled_latency_ns(dict(rows[0],scheduledMonotonicNS=None),100)==61_000_000
    fidelity=analysis.issuance_fidelity(rows,[0],100,50_000_000,analysis.stats)
    assert fidelity['supplied'] and fidelity['issued']==fidelity['offered']==1
    assert fidelity['sendLatenessNS']['p95']==60_000_000 and fidelity['sendsAfterWindow']==1
    unsent=dict(rows[0],error='UnsentAtWindowEnd')
    assert analysis.scheduled_latency_ns(unsent,100) is None
    fidelity=analysis.issuance_fidelity([unsent],[0],100,50_000_000,analysis.stats)
    assert not fidelity['supplied'] and fidelity['offered']==1 and fidelity['issued']==0
    assert not analysis.issuance_fidelity([],[0],100,50_000_000,analysis.stats)['supplied']
    policy=analysis.generator_quality_config({'generatorQuality':{'latenessToleranceNS':50_000_000,'maxExceedingFraction':0}})
    assert analysis.issuance_fidelity(rows,[0],100,50_000_000,analysis.stats,policy)['generatorQuality']['status']=='INVALID_GENERATOR'
    assert fidelity['generatorQuality']['status']=='UNKNOWN'
    for name in ('demo-config.example.json','demo-config.e5.example.json','g-check-config.example.json','preflight-config.example.json'):
        default_policy=analysis.generator_quality_config(json.loads((HERE/name).read_text()))
        assert default_policy=={'latenessToleranceNS':50_000_000,'maxExceedingFraction':.01},name
    population=[dict(rows[0],requestIndex=i,scheduledOffsetNs=i,scheduledMonotonicNS=100+i,sentMonotonicNS=50_000_100+i) for i in range(100)]
    population[0]['sentMonotonicNS']+=1
    assert analysis.issuance_fidelity(population,list(range(100)),100,100_000_000,analysis.stats,default_policy)['generatorQuality']['status']=='VALID'
    population[1]['sentMonotonicNS']+=1
    assert analysis.issuance_fidelity(population,list(range(100)),100,100_000_000,analysis.stats,default_policy)['generatorQuality']['status']=='INVALID_GENERATOR'
    timing=analysis.request_timing(dict(rows[0],sentAtUnixNS=200),{'outcome':'completed','executionComplete':True,'elapsedNanos':500_000},150,100)
    assert timing['sloLatencyNS']==500_000 and timing['scheduledLatencyNS']==61_000_000 and timing['entranceDelayNS']==-50
    assert analysis.request_timing(rows[0],None,150,100)['sloLatencyNS'] is None
    assert analysis.issuance_fidelity([dict(rows[0],issued=False,error='ThreadStartError')],[0],100,50_000_000,analysis.stats,policy)['issued']==0
    config = {}
    freeze_producer_config(config)
    assert config == {'producerResources': {'cpu': '2', 'memory': '1Gi'}, 'producerShards': {'mobilenet': 3, 'plc': 1, 'smolvlm2': 1}}
    for key, invalid in (
        ('producerResources', None), ('producerResources', {}),
        ('producerResources', {'cpu': '0', 'memory': '1Gi'}),
        ('producerResources', {'cpu': '0.0001', 'memory': '1Gi'}),
        ('producerResources', {'cpu': '2', 'memory': '-1Gi'}),
        ('producerResources', {'cpu': 2, 'memory': '1Gi'}),
        ('producerShards', None), ('producerShards', {}),
        ('producerShards', {'mobilenet': True, 'plc': 1, 'smolvlm2': 1}),
        ('producerShards', {'mobilenet': 0, 'plc': 1, 'smolvlm2': 1}),
    ):
        try:
            freeze_producer_config({key: invalid})
        except ValueError:
            pass
        else:
            raise AssertionError('Invalid ' + key + ' accepted')
    custom = {'producerResources': {'cpu': '1500m', 'memory': '512Mi'}, 'producerShards': dict.fromkeys(PRODUCER_ALIASES, 2)}
    freeze_producer_config(custom)
    pod = {'containers': [{'name': 'load'}, {'name': 'sidecar', 'resources': {'requests': {'cpu': '1'}, 'limits': {'memory': '2Gi'}}}], 'initContainers': [{'name': 'dataset'}]}
    set_producer_resources(pod, custom)
    for container in pod['containers'] + pod['initContainers']:
        assert container['resources']['requests'] == container['resources']['limits'] == custom['producerResources']
    components = approval_sources({'sources': {'server': 'a'*40, 'loadgen': 'b'*40,
                                              'power': 'bin/observation-power-source-f8c3d2b4'}})
    require_component_source({'sourceSHA': 'a'*40}, components['server'], 'server')
    require_component_source({'sourceSHA': 'b'*40}, components['loadgen'], 'loadgen')
    from unittest.mock import patch
    with patch.dict(globals(), REPO=Path('/frozen/repository'), __file__='/ops/harness/e5-frozen/hack/e5/e5.py'):
        pinned = str(REPO / components['power'])
        require_component_source({'sourceSHA': 'c'*40, 'executable': pinned}, components['power'], 'power', pinned)
        for component, receipt in [('server', {'sourceSHA': 'b'*40}),
                                   ('loadgen', {'sourceSHA': 'a'*40}),
                                   ('power', {'executable': pinned+'-different'})]:
            try:
                require_component_source(receipt, components[component], component)
            except ValueError:
                pass
            else:
                raise AssertionError('Mismatched component source accepted')
    try:
        approval_sources({'sourceSHA': 'a'*40, 'sources': components})
    except ValueError:
        pass
    else:
        raise AssertionError('Removed single-source approval accepted')
    self_check_g0()
    stages = normative_stage_specs(300)
    assert sum(seconds for _, seconds, _ in stages) == 11700
    assert sum(seconds for name, seconds, _ in stages if not name.startswith(('E5-0', 'E5-7'))) == 10200
    assert stages[-1] == ('E5-7', 900, [.3]*3)
    runbook = json.loads((HERE/'demo-config.e5.example.json').read_text())['failureInjectionRunbook']
    assert [step['offsetSeconds'] for step in runbook['steps']] == [180, 360, 600]
    evidence = {'stage':'E5-7', 'events':[
        {'offsetSeconds':offset,'kind':kind,'exitCode':0,'atUTC':at,'nodeUID':'node',
         'podUID':'pod','commandRef':'command','responseRef':'response'}
        for offset,kind,at in [(180,'delete-serving-pod','2026-10-28T00:03:00Z'),
                              (360,'cordon-delete','2026-10-28T00:06:00Z'),
                              (600,'uncordon','2026-10-28T00:10:00Z')]]}
    validate_failure_events(evidence,'2026-10-28T00:00:00Z','2026-10-28T00:15:00Z')
    failed = copy.deepcopy(evidence)
    failed['events'][2]['exitCode'] = 1
    failed['events'][0]['atUTC'] = '2026-10-28T00:02:59Z'
    validate_failure_events(failed,'2026-10-28T00:00:00Z','2026-10-28T00:15:00Z')
    identity = [{'recordID':'evaluation'}]
    assert profiling_evaluation_separation({},identity)['status'] == 'UNKNOWN'
    assert profiling_evaluation_separation({'profilingRecordIDs':['profiling']},identity)['status'] == 'DISJOINT_RECORD_IDS'
    try:
        profiling_evaluation_separation({'profilingRecordIDs':['evaluation']},identity)
    except ValueError:
        pass
    else:
        raise AssertionError('Known profiling/evaluation overlap accepted')
    try:
        validate_failure_events(dict(evidence,events=evidence['events'][:2]),'2026-10-28T00:00:00Z','2026-10-28T00:15:00Z')
    except ValueError:
        pass
    else:
        raise AssertionError('Unexercised E5-7 accepted as measured')
    d_cr = {'metadata': {'uid': 'actual-measurement'}, 'status': {'phase': 'Complete', 'perRequestNanos': -20}}
    d_raw = {'verdict': 'Accepted', 'unknownRows': 0, 'idleIdentified': True,
             'idleFraction': .05, 'idleNanosPerRequest': 10, 'perRequestNanos': -20,
             'loadJob': {'name': 'load', 'state': 'Complete CompletionsReached'},
             'drainState': {'native': {'instance': {'requests': []}}}, 'batches': 100, 'cycles': 99}
    d_raw['conditions'] = {'maximumIdleFraction': '0.05'}
    assert d25_acceptance(d_cr,d_raw)['dRange'] == [-30,-20]
    configured = dict(d_raw, idleFraction=.08, conditions={'maximumIdleFraction':'0.1'})
    assert d25_acceptance(d_cr,configured)['maximumIdleFraction'] == .1
    for bad in (dict(d_raw,idleFraction=.050001), dict(d_raw,unknownRows=1),
                dict(d_raw,idleIdentified=False), dict(d_raw,idleNanosPerRequest=1000001),
                dict(d_raw,drainState={}), dict(d_raw,perRequestNanos=20)):
        try:
            d25_acceptance(d_cr,bad)
        except ValueError:
            pass
        else:
            raise AssertionError('Invalid d25 evidence accepted')
    self_check_factor()
    self_check_restore_go()
    self_check_failure_selection(analysis)
    self_check_client_jsonl(analysis)
    print('PASS E5 receive-origin SLO, scheduled reference, generator quality and full planned issuance; offline only')

def self_check_factor():
    """Exercise the real factor method offline, without operational environment inputs."""
    import ast
    from types import SimpleNamespace
    tree=ast.parse(Path(__file__).read_text())
    selected=[n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name in ('fingerprint','instance_rows','paired_baseline','baseline_settings','restoration_exclusions','full_restore_path','rebound_fingerprint','prepare_body','row_folder','replay_receipt')]
    cpo=next(n for n in tree.body if isinstance(n,ast.ClassDef) and n.name=='CPO')
    methods=[n for n in cpo.body if isinstance(n,ast.FunctionDef) and n.name in ('factor','start_barrier_observation','disk_boundary')]
    selected.append(ast.ClassDef(name='CPO',bases=[],keywords=[],body=methods,decorator_list=[]))
    node={'name':'node','uid':'node-uid'}
    targets=[{'id':str(cap),'node':node,'profile':{'id':str(cap)},'allocation':{'cpu':1},'maxBatch':1,'maxWaitNanos':0,'workloadUID':'workload'} for cap in (2,32)]
    baseline={'instances':targets,'nodes':[{'node':node,'powerMode':'paired-mode'}],'routing':{'workload':{'2':.5,'32':.5}}}
    ns={'NODES':['node'],'copy':copy,'load':lambda _: {'snapshot':baseline},'need':lambda *args:None,'now':lambda:'now','positive':lambda x:type(x) in (int,float) and math.isfinite(x) and x>0}
    exec(compile(ast.fix_missing_locations(ast.Module(body=selected,type_ignores=[])),__file__,'exec'),ns)
    for bad in (None,'owner-served','cap-delay','stale-delay','missing-delay','wrong-cap','foreign-served','foreign-after','stale','node','allocation','maxWaitNanos','static-good','static-drift','full-good','full-drift'):
        runner=ns['CPO']();requests=[];prepared=[];receipts={};clock=[0];polls=[0]
        ns['time']=SimpleNamespace(monotonic=lambda:clock[0],sleep=lambda seconds:clock.__setitem__(0,clock[0]+seconds))
        ns['save']=lambda path,body:receipts.__setitem__(str(path),copy.deepcopy(body))
        static=bad in ('static-good','static-drift');full=bad in ('full-good','full-drift')
        runner.a=SimpleNamespace(mode='full' if full else 'static' if static else 'membership-off',stage='E5-5' if full else 'E5-3' if static else 'E5-2',pre_state=Path('/offline/selection.json') if static or full else None,row_tag='3b' if full else '223' if static else '')
        runner.c={'factors':[{'name':'membership-off','controls':{}}]}
        runner.out=Path('/offline');runner.uids=['workload'];runner.tb={'settleSeconds':1}
        if bad is not None:runner.tb['factorAckWaitSeconds']=3
        runner.d=SimpleNamespace(epoch=lambda value:value)
        ns['load']=lambda _: {'state':{'snapshot':dict(baseline,reservations=[{'id':'not-restorable'}])},'fingerprint':ns['fingerprint'](baseline,['workload']),'settings':{'autonomousWorkloads':['workload'],'controls':{'membership':True}}}
        runner.disk_boundary=lambda *args:None
        runner.check_power_observation=lambda:None
        runner.check_token_lifetime=lambda:None
        runner.b={'sources':{}}
        defaults={str(cap):{'instance':dict(target,maxBatch=cap)} for cap,target in zip((2,32),targets)}
        if bad in ('node','allocation','maxWaitNanos'):
            defaults['2']['instance'][bad]=None
        runner.api=lambda *args:{'preparations':defaults}
        runner.clear_owned=lambda *args:None
        runner.operation=lambda *args:None
        def state():
            polls[0]+=bool(prepared)
            delayed=polls[0]==1
            wrong=bad=='wrong-cap' or bad=='cap-delay' and delayed
            stale=bad=='stale' or bad=='stale-delay' and delayed
            instances=[] if bad=='missing-delay' and delayed else [dict(i,maxBatch=2 if wrong else i['maxBatch']) for i in prepared]
            served='foreign' if bad=='foreign-served' or bad=='foreign-after' and len(prepared)==2 and polls[0]>1 else 'workload' if bad=='owner-served' else ''
            instances=[dict(i,workloadUID=served) for i in instances]
            return {'snapshot':{'nodes':baseline['nodes'],'at':9 if stale else 11,'instances':instances},'results':[{'endedAt':10}]}
        runner.state=state
        def prepare(method,path,body,**kwargs):
            requests.append(body)
            instance=dict(next(t for t in targets if t['profile']['id']==body['profileID']),id='new-'+body['profileID'],ownerWorkloadUID=body['workloadUID'],workloadUID='')
            prepared.append(instance)
            polls[0]=0
            return {'status':'complete','actual':{'instances':[dict(instance,profile=body['profileID'])],'native':{instance['id']:{'metadata':{'policy':{'profileID':body['profileID'],'maxBatchSize':body['maxBatch'],'maxBatchWaitMS':0}}}}}}
        runner.io=SimpleNamespace(api=prepare)
        runner.settings=lambda:{}
        ns['owned_target']=lambda *args:{}
        def route_ack(chosen,*args):
            if not (static or full):return None
            snapshot={'instances':[dict(i,workloadUID='workload') for i in prepared],'nodes':baseline['nodes'],'routing':{'workload':{i['id']:.5 for i in prepared}}}
            if bad in ('static-drift','full-drift'):snapshot['instances'][0]['maxBatch']=99
            return {'snapshot':snapshot}
        runner.route_ack=route_ack
        runner.release_routing=lambda *args:None
        runner.authorize_native_modes=lambda:None
        runner.put=lambda settings:settings
        try:
            runner.factor()
        except RuntimeError as error:
            assert bad in ('wrong-cap','foreign-served','foreign-after','stale','node','allocation','maxWaitNanos','static-drift','full-drift'),(bad,str(error))
            if bad in ('wrong-cap','foreign-served','stale'):
                assert 'ACK timed out' in str(error),str(error)
                receipt=next(body for path,body in receipts.items() if path.endswith('-snapshot-ack.json'))
                assert not receipt['accepted'] and receipt['state']==state()
                assert receipt['freshness']['snapshotFresh']==(bad!='stale')
                assert clock[0]==3
            elif bad=='foreign-after':assert 'empty or owner served UID' in str(error),str(error)
            elif bad in ('static-drift','full-drift'):
                assert 'fingerprint differs' in str(error)
                assert not receipts['/offline/full-E5-5-3b-restore.json' if full else '/offline/static-replay-restore.json']['accepted']
            else:assert 'unsupported manual wait/resource override' in str(error),str(error)
        else:
            assert bad in (None,'owner-served','cap-delay','stale-delay','missing-delay','static-good','full-good'),'Mismatched paired state accepted: '+str(bad)
            assert [r['maxBatch'] for r in requests]==[1,1]
            acks=[body for path,body in receipts.items() if path.endswith('-snapshot-ack.json')]
            assert len(acks)==2 and all(body['accepted'] for body in acks)
            assert all(body['timeboxSeconds']==(120 if bad is None else 3) for body in acks)
            assert clock[0]==(2 if bad in ('cap-delay','stale-delay','missing-delay') else 0)
            if static or full:
                receipt=receipts['/offline/full-E5-5-3b-restore.json' if full else '/offline/static-replay-restore.json']
                assert receipt['accepted'] and receipt['fingerprint']==receipt['expectedFingerprint']
                assert receipt['physicalIDMapping']=={'2':'new-2','32':'new-32'}
                if full:
                    assert receipt['fullSettings']['autonomousWorkloads']==['workload']
                    assert receipt['restorationExclusions']['originalReservations']==1
    import tempfile
    ns['json']=json
    ns['load']=lambda path:json.loads(path.read_text())
    with tempfile.TemporaryDirectory() as temporary:
        root=Path(temporary)
        (root/'static-replay-restore.json').write_text(json.dumps({'rowTag':'223'}))
        assert ns['row_folder'](root,'static','E5-2')==root/'static-E5-2'
        assert ns['row_folder'](root,'static','E5-3')==root/'static-E5-3-223'
        assert ns['row_folder'](root,'full','E5-3')==root/'full-E5-3'
        assert ns['row_folder'](root,'full','E5-4')==root/'full-E5-4-223'
        (root/'full-E5-1-3b-restore.json').write_text(json.dumps({'rowTag':'3b'}))
        assert ns['row_folder'](root,'full','E5-1','3b')==root/'full-E5-1-3b'
        assert ns['row_folder'](root,'full','E5-1')==root/'full-E5-1-3b'
        runner=ns['CPO']();runner.out=root
        for free in (310_000_000_000,309_999_999_999):
            ns['os']=SimpleNamespace(statvfs=lambda _:SimpleNamespace(f_bavail=free,f_frsize=1))
            try:runner.disk_boundary('static-E5-3-223','before')
            except RuntimeError:assert free<310_000_000_000
            else:assert free==310_000_000_000
        records=[json.loads(line) for line in (root/'disk-boundaries.jsonl').read_text().splitlines()]
        assert [row['accepted'] for row in records]==[True,False]
    print('PASS E5 factor paired maxBatch/fresh ACK, static/full fingerprint match/mismatch, full settings/reservation disclosure, append-only row paths and 310 GB boundary; offline only')


def self_check_restore_go():
    """Run the actual targeted settings/GO/receipt statements with local API doubles."""
    import ast
    from types import SimpleNamespace
    tree=ast.parse(Path(__file__).read_text())
    cpo=next(n for n in tree.body if isinstance(n,ast.ClassDef) and n.name=='CPO')
    run=next(n for n in cpo.body if isinstance(n,ast.FunctionDef) and n.name=='run')
    blocks=[body for node in ast.walk(run) for _,body in ast.iter_fields(node) if isinstance(body,list)]
    body=next(body for body in blocks if any(isinstance(n,ast.If) and isinstance(n.test,ast.Name) and n.test.id=='targeted' and any(isinstance(x,ast.Expr) and isinstance(x.value,ast.Call) and isinstance(x.value.func,ast.Attribute) and x.value.func.attr=='authorize_native_modes' for x in n.body) for n in body))
    indices=[i for i,n in enumerate(body) if isinstance(n,ast.If) and isinstance(n.test,ast.Name) and n.test.id=='targeted']
    go=compile(ast.fix_missing_locations(ast.Module(body=body[indices[0]:indices[-1]+1],type_ignores=[])),__file__,'exec')
    setup=[n for n in run.body if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id in ('restore_path','targeted','restored') for t in n.targets)]
    setup=compile(ast.fix_missing_locations(ast.Module(body=setup,type_ignores=[])),__file__,'exec')
    for targeted,drift in ((True,False),(True,True),(False,False)):
        events=[];receipts={};routing={'plc':{'fast':766,'slow':234}}
        expected=copy.deepcopy(routing);pod={'metadata':{'name':'producer'}}
        def api(args,**kwargs):
            if args[0]=='exec':
                events.append('GO')
                if drift:routing['plc']={'fast':1,'slow':1}
            return pod
        def state():
            assert events[-1]=='GO'
            events.append('snapshot')
            return {'snapshot':{'routing':copy.deepcopy(routing)}}
        def release(*args):
            events.append('release')
            routing['plc']={'fast':1,'slow':1}
        runner=SimpleNamespace(uids=['plc'],tb={'apiSeconds':1,'barrierSeconds':1},io=SimpleNamespace(k=api,assert_producer_startable=lambda *args,**kwargs:None),state=state,release_routing=release,authorize_native_modes=lambda:events.append('authorize'),put=lambda settings:events.append('autonomy-on') or settings)
        restored={'accepted':True,'fingerprint':{'routing':expected},'fullSettings':{'autonomousWorkloads':['plc']}}
        runner.a=SimpleNamespace(mode='full' if targeted else 'static',row_tag='3b')
        runner.out=Path('/offline')
        ns={'self':runner,'st':{'id':'E5-1'},'restored':restored,'folder':Path('/offline/full-E5-1-3b'),'barrier':None,'pods':{'plc':pod},'now':lambda:'now','save':lambda path,value:receipts.__setitem__(path.name,copy.deepcopy(value))}
        ns['full_restore_path']=lambda root,row,tag:SimpleNamespace(exists=lambda:tag=='3b')
        ns['load']=lambda _:restored
        ns['replay_receipt']=lambda _: {}
        exec(setup,ns)
        assert ns['targeted']==targeted
        try:exec(go,ns)
        except RuntimeError as error:
            assert targeted and drift and 'route weights differ' in str(error)
        else:
            assert not drift
        assert 'release' not in events
        if targeted:
            receipt=receipts['start-state-restoration.json']
            assert events==['authorize','autonomy-on','GO','snapshot']
            assert receipt['accepted']==receipt['postGO']['accepted']==(not drift)
            assert receipt['postGO']['state']['snapshot']['routing']==routing
            assert receipt['postGO']['expectedRouting']==expected
            if not drift:assert routing==expected
        else:assert events==['GO'] and not receipts
    print('PASS E5 tag 3b selects restored mode; 766/234 routes retained through GO, post-GO drift rejected; offline only')


def self_check_failure_selection(analysis):
    """Synthetic active-serving/runbook regression; zero cluster requests or mutations."""
    import ast
    import contextlib
    import io
    import tempfile
    from types import SimpleNamespace
    tree=ast.parse(Path(__file__).read_text())
    names=('positive','failure_select_snapshot_ttl','instance_rows','row_folder','replay_receipt','full_restore_path','serving_failure_targets','validate_failure_events')
    selected=[n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name in names]
    cpo=next(n for n in tree.body if isinstance(n,ast.ClassDef) and n.name=='CPO')
    selected.append(ast.ClassDef(name='CPO',bases=[],keywords=[],body=[n for n in cpo.body if isinstance(n,ast.FunctionDef) and n.name=='failure_select'],decorator_list=[]))
    at='2026-10-30T00:03:00+00:00'
    ns={'Path':Path,'dt':dt,'json':json,'math':math,'urllib':urllib,'shlex':shlex,'now':lambda:at,
        'ENVIRONMENT':{'d2':{'kubectl':['fixture-kubectl']}},
        'load':lambda path:json.loads(Path(path).read_text()),
        'save':lambda path,body:Path(path).write_text(json.dumps(body))}
    exec(compile(ast.fix_missing_locations(ast.Module(body=selected,type_ignores=[])),__file__,'exec'),ns)
    for invalid in (None,0,-1,True,'15',float('inf'),float('nan')):
        try:ns['failure_select_snapshot_ttl']({'failureSelectSnapshotTTLSeconds':invalid})
        except ValueError as error:assert 'failureSelectSnapshotTTLSeconds' in str(error)
        else:raise AssertionError('Invalid snapshot TTL accepted')
    accepted=(None,'nano','age-11','custom-ttl')
    for bad in (*accepted,'age-16','zero-weight','zero-assigned','same-node','missing-pod','stale'):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);folder=root/'full-E5-7-3b';folder.mkdir()
            ns['save'](root/'full-E5-7-3b-restore.json',{'rowTag':'3b','accepted':True})
            ns['save'](folder/'phase.json',{'status':'RUNNING','startUTC':'2026-10-30T00:00:00+00:00','stage':{'seconds':900}})
            instances=[{'id':'i'+str(n),'podUID':'p'+str(n),'podName':'pod'+str(n),'workloadUID':'w','ownerWorkloadUID':'w','ready':True,'node':{'name':'n'+str(n),'uid':'n'+str(n)}} for n in range(2)]
            if bad=='same-node':instances[1]['node']=instances[0]['node']
            if bad=='missing-pod':instances[1].pop('podUID')
            nodes=[{'node':{'name':'n'+str(n),'uid':'n'+str(n)},'ready':True} for n in range(2)]
            state={'snapshot':{'at':'2026-10-30T00:02:00+00:00' if bad=='stale' else at,'nodes':nodes,'instances':instances,'routing':{'w':{'i0':1,'i1':0 if bad=='zero-weight' else 1}}}}
            window={'from':'2026-10-30T00:02:00+00:00','to':at,'workloads':[{'uid':'w','instanceAssignments':{'i0':2,'i1':0 if bad=='zero-assigned' else 3}}]}
            if bad=='nano':
                state['snapshot']['at']='2026-10-30T00:02:59.123456789Z';window['to']=state['snapshot']['at']
            if bad in ('age-11','age-16','custom-ttl'):
                state['snapshot']['at']='2026-10-30T00:02:'+('49' if bad=='age-11' else '44')+'+00:00'
                window['to']=state['snapshot']['at']
            runner=ns['CPO']();runner.a=SimpleNamespace(mode='full',stage='E5-7',row_tag='3b',failure_kind='delete-serving-pod')
            runner.out=root;runner.uids=['w'];runner.tb={'apiSeconds':1}
            runner.c={'failureSelectSnapshotTTLSeconds':20} if bad=='custom-ttl' else {}
            def kubernetes(argv,**kwargs):
                raise AssertionError('API-only selection attempted kubectl access')
            runner.io=SimpleNamespace(k=kubernetes)
            runner.d=SimpleNamespace(epoch=analysis.epoch)
            runner.state=lambda:state;runner.settings=lambda:{'observationWindowNanos':60_000_000_000,'maxObservationAgeNanos':5_000_000_000}
            runner.api=lambda method,path:window
            try:
                with contextlib.redirect_stdout(io.StringIO()):runner.failure_select()
            except ValueError as error:
                assert bad not in accepted
                if bad=='age-16':assert 'snapshot is stale' in str(error)
                evidence=ns['load'](folder/'failure-events.json')
                assert evidence['status']=='INVALID' and not list(folder.glob('*-delete-options.json'))
                try:ns['validate_failure_events'](evidence,'2026-10-30T00:00:00Z','2026-10-30T00:15:00Z',analysis.epoch)
                except ValueError as error:assert str(error).startswith('INVALID:')
                else:raise AssertionError('Cancelled injection accepted')
            else:
                assert bad in accepted
                runner.a.failure_kind='cordon-delete'
                with contextlib.redirect_stdout(io.StringIO()):runner.failure_select()
                evidence=ns['load'](folder/'failure-events.json')
                assert len({s['nodeUID'] for s in evidence['selections']})==2
                for selection in evidence['selections']:
                    ttl=ns['failure_select_snapshot_ttl'](runner.c)
                    assert selection['maximumAgeSeconds']==ttl
                    assert math.isclose(analysis.epoch(selection['expiresAtUTC']),analysis.epoch(selection['snapshotAt'])+ttl,rel_tol=0,abs_tol=1e-6)
                assert runner.settings()['maxObservationAgeNanos']==5_000_000_000
                events=[]
                for selection in evidence['selections']:
                    command=folder/(selection['kind']+'-command.json');ns['save'](command,{'deleteArgv':selection['deleteArgv']})
                    assert ns['load'](selection['deleteOptionsRef'])['preconditions']=={'uid':selection['podUID']}
                    events.append({'kind':selection['kind'],'atUTC':at,'exitCode':0,'podUID':selection['podUID'],'nodeUID':selection['nodeUID'],'commandRef':str(command),'responseRef':str(command)})
                events.append(dict(events[-1],kind='uncordon'))
                evidence['events']=events
                ns['validate_failure_events'](evidence,'2026-10-30T00:00:00Z','2026-10-30T00:15:00Z',analysis.epoch)
                expired=copy.deepcopy(evidence);expired['events'][0]['atUTC']='2026-10-30T00:03:21Z'
                try:ns['validate_failure_events'](expired,'2026-10-30T00:00:00Z','2026-10-30T00:15:00Z',analysis.epoch)
                except ValueError as error:assert str(error).startswith('INVALID:')
                else:raise AssertionError('Expired selection accepted')
    print('PASS E5-7 snapshot TTL: default 15s accepts age 11s, rejects 16s; configured TTL/expiry; CHILL observation age remains 5s; positive route/assigned + live UID + distinct Nodes; operator-only, offline')


def self_check_client_jsonl(analysis):
    """Synthetic trailing stderr fixture; run the real judge without cluster access."""
    import ast
    import tempfile
    from types import SimpleNamespace
    tree=ast.parse(Path(__file__).read_text())
    selected=[n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name in ('judge','reported_power')]
    at='2026-10-29T00:00:00Z'
    row={'requestID':'fixture-response','requestIndex':0,'scheduledOffsetNs':0,
         'scheduledMonotonicNS':100,'sentMonotonicNS':100,'finishedMonotonicNS':200,
         'sentAtUnixNS':1793232000000000000,'status':200}
    final={'outcome':'completed','executionComplete':True,'elapsedNanos':100,'receivedAt':at}
    indexed={'fixture-uid':{'summary':{'outcomes':{'completed':1}},'finals':{'fixture-response':final},
                            'receivedAtNS':{},'errors':[],'sources':[]}}
    raw=SimpleNamespace(scheduler_events=lambda folder:([],{'complete':True,'errors':[]}),
                        stream_events=lambda folder,kind:([],{'complete':True,'errors':[],'sources':[]}),
                        iter_request_indexes=lambda *args,**kwargs:indexed)
    with tempfile.TemporaryDirectory(prefix='e5-client-jsonl-') as temporary:
        folder=Path(temporary)
        stage={'id':'E5-1','seconds':1,'traces':{'fixture':{'offered':1,'offsetsNS':[0]}},'parts':[]}
        config={'workloads':[{'name':'fixture','uid':'fixture-uid','slo':{'percentile':95,'targetLatencyNanos':1000}}],
                'mSeconds':0,'inputs':{'scheduler':str(folder/'scheduler.json')},
                'collection':{},
                'generatorQuality':{'latenessToleranceNS':50_000_000,'maxExceedingFraction':.01}}
        phase={'stage':stage,'startUTC':at,'endUTC':'2026-10-29T00:00:01Z',
               'drainedUTC':'2026-10-29T00:00:02Z','starts':{'fixture':{'monotonicNS':100}},'status':'DRAINED_NOT_EVALUATED'}
        payloads={'phase.json':phase,'scheduler.json':{'snapshot':{'options':[]}},
                  'power.json':{'complete':True,'wholePoolJoules':1,'activePoolJoules':1},
                  'power-drain-inclusive.json':{'complete':True,'wholePoolJoules':1,'activePoolJoules':1}}
        for name,value in payloads.items():(folder/name).write_text(json.dumps(value))
        source=json.dumps(row)+'\nTraceback (most recent call last):\n'
        path=folder/'fixture-client.jsonl';path.write_text(source)
        ns=dict(globals(),D2=HERE/'d2',E5={'thermalSlackNodes':[]},
                load_binding=lambda _: {'config':config,'limitations':[]},
                load=lambda path:json.loads(Path(path).read_text()),
                module=lambda name,path:analysis if name=='d2analyze' else raw,
                now=lambda:at,ref=lambda path:{'path':str(path)},
                save=lambda path,value:Path(path).write_text(json.dumps(value)))
        exec(compile(ast.Module(body=selected,type_ignores=[]),__file__,'exec'),ns)
        result=ns['judge'](None,folder)
        assert result['status']=='MEASURED_DESCRIPTIVE',result
        assert result['requests']['fixture']['clientRows']==result['requests']['fixture']['deadlineSuccess']==1
        assert result['coverage']['malformedLines']==1
        first=result['coverage']['firstMalformedLine']
        assert first['line']==2 and first['content']=='Traceback (most recent call last):'
        assert path.read_text()==source and (folder/'judgment.json').exists()
        assert str(folder/'cgmon.log') not in result['sources']
        resources=[]
        for second in (0,2):
            stamp='2026-10-29T00:00:0'+str(second)+'Z'
            resources.extend([
                {'row':{'kind':'server','at':stamp,'podUID':'server','processStartedAt':at,'gcCPUSeconds':second/2,'gcCycles':second},'sourceRef':{'line':second+1}},
                {'row':{'kind':'node','node':{'name':'n','uid':'nu'},'receivedAt':stamp,'samples':[{'sampledAt':stamp,'containers':[{'podUID':'server','containerID':'c','cgroupVersion':2,'cpuUsageNanos':second*1e9,'throttledNanos':second,'throttledPeriods':second,'memoryCurrentBytes':10}],'thermal':[{'sensor':'/sensor','milliCelsius':65000}]}]},'sourceRef':{'line':second+2}}])
        rows,thermal=resource_telemetry(resources,[],[])
        assert rows[0]['nrThrottled']==0 and rows[1]['memoryBytes']==10 and thermal[0]['bin']=='60-70'
        native=copy.deepcopy(resources[1]);native['row']['samples'][0]['containers'][0]['podUID']='native'
        native_rows,_=resource_telemetry([native],[],[{'snapshot':{'instances':[{'id':'native/c','podUID':'native'}]}}])
        assert native_rows[0]['role']=='native'
        assert resource_telemetry([native],[],[])[0][0]['role']=='unknown'
        counters=[{'kind':'cgroup','role':role,'at':'2026-10-29T00:00:0'+str(second)+'Z','containerID':role,'cpuUsageNanos':second*1e9,'nrThrottled':second,'throttledNanos':second} for role in ('client','server') for second in (0,2)]
        (folder/'fixture-cgroup.jsonl').write_text(''.join(json.dumps(row)+'\n' for row in counters))
        first,last=analysis.epoch(at),analysis.epoch('2026-10-29T00:00:02Z')
        telemetry=analysis.telemetry(analysis.Inputs(folder),first,last,resource_rows=rows)
        assert telemetry['roles']['server']['identities']==telemetry['roles']['client']['identities']==1
        gc=runtime_gc(resources,first,last,analysis)
        assert gc['complete'] and gc['cpuSecondsDelta']==1 and gc['cyclesDelta']==2
        reset=copy.deepcopy(resources);reset[2]['row']['gcCPUSeconds']=-1
        assert runtime_gc(reset,first,last,analysis)['cpuSecondsDelta'] is None
        assert runtime_gc(resources,first,last+1,analysis)['complete'] is False
        snapshot={'at':'2026-10-29T00:00:02Z','nodes':[{'node':{'name':'n','uid':'nu'},'ready':True}],
            'instances':[{'id':'i','podUID':'p','node':{'name':'n','uid':'nu'},'workloadUID':'w','ready':True}],'routing':{'w':{'i':1}}}
        pod={'row':{'kind':'pod','event':'changed','at':at,'pod':{'uid':'p','node':'n','ready':'True'}},'sourceRef':{'line':9}}
        commands=[{}, {}, {'nodeUID':'nu','atUTC':at}]
        recovery=failure_api_recovery(commands,[{'snapshot':snapshot,'_rawRefs':[{'line':8}]}],[pod],last,analysis)
        assert recovery['status']=='OBSERVED' and recovery['observation']['clusterRef']=={'line':9}
        pod['row']['event']='deleted'
        assert failure_api_recovery(commands,[{'snapshot':snapshot}],[pod],last,analysis)['status']=='UNMEASURED'
    print('PASS E5 trailing stderr coverage; API resource counters plus client-only local cgroups; cumulative GC delta/reset/brackets and scheduler+cluster recovery; offline only')





if __name__=='__main__' and sys.argv[1:2]==['tables']:
    spec=importlib.util.spec_from_file_location('e5_tables',HERE/'tables.py')
    tables=importlib.util.module_from_spec(spec);spec.loader.exec_module(tables)
    if len(sys.argv)!=3:sys.exit('Usage: e5.py tables <result-directory|--self-check>')
    if sys.argv[2]=='--self-check':tables.self_check(HERE.parents[1])
    else:print(tables.render(Path(sys.argv[2]),HERE.parents[1]))
    sys.exit(0)

ENVIRONMENT_PATH = Path(os.environ['CHILL_EXPERIMENT_CONFIG'])
ENVIRONMENT = json.loads(ENVIRONMENT_PATH.read_text())
E5 = ENVIRONMENT['e5']
REPO = Path(ENVIRONMENT['paths']['repository'])
OPS = Path(ENVIRONMENT['paths']['ops'])
CAMPAIGN = Path(ENVIRONMENT['paths']['campaign'])
D2_STATE = Path(ENVIRONMENT['paths']['d2State'])
D2 = HERE / 'd2'
OPERATIONAL_RECIPE = Path(E5['operationalRecipe'])
NODES = E5['nodes']
WORKLOADS = ['image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa']
SEEDS = dict(zip(WORKLOADS, [101, 202, 303]))
REQUIRED = {
 'approval': ['sources', 'controlImageDigest', 'runtimeDigests'],
 'inputs': ['scheduler', 'capacity', 'capacityScheduler', 'ranking', 'd25', 'costs', 'bias', 'gateway', 'producer', 'powerObservation', 'evaluationPayload'],
 'numerical': ['g0Evidence', 'mSeconds', 'N', 'burstLowSeconds', 'timeboxes', 'smokeSeconds'],
 'workload': ['name', 'uid', 'generation', 'slo', 'R', 'deliverySeconds', 'socketSeconds', 'gatewayCapSeconds', 'workers', 'datasetDir', 'fields', 'jobTemplate', 'producerEndpoint', 'producerTokenFile', 'producerCAFile', 'producerDatasetDir'],
 'collection': ['hosts', 'powerExecutable', 'powerReceipt'],
 'factor': ['name', 'controls', 'witness'],
 'timeboxes': ['setupSeconds', 'barrierSeconds', 'drainSeconds', 'flushSeconds', 'collectSeconds', 'apiSeconds', 'settleSeconds'],
}


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, str(path)); result = importlib.util.module_from_spec(spec); spec.loader.exec_module(result); return result


def load(path):
    return json.loads(Path(path).read_text(), parse_constant=lambda x: (_ for _ in ()).throw(ValueError('Nonfinite JSON: '+x)))

def pool_shares(config):
    shares = config.get('poolShares', {'MNV': .45, 'PLC': .1, 'Smol': .45})
    if not isinstance(shares, dict) or set(shares) != {'MNV', 'PLC', 'Smol'} or any(type(v) not in (int, float) or not math.isfinite(v) or not 0 <= v <= 1 for v in shares.values()) or not math.isclose(sum(shares.values()), 1, rel_tol=0, abs_tol=1e-9):
        raise ValueError('poolShares requires finite MNV/PLC/Smol shares in [0,1] summing to 1')
    config['poolShares'] = dict(shares)
    return shares

def g0_evidence_gate(config, weighted_capacity, *, required=True, expected_rates=None, plc_slo=None):
    """Freeze a measured alpha=1.2 supply/CPU/receive-origin PLC witness, never maximum G."""
    evidence = config.get('g0Evidence')
    if evidence is None or evidence == {'status': 'UNKNOWN'}:
        if required:
            raise ValueError('Frozen g0Evidence required; UNKNOWN is not E5 approval')
        return {'status': 'UNKNOWN', 'sources': {}}
    if not isinstance(evidence, dict):
        raise ValueError('g0Evidence requires a runID, verdict, binding and csCPULimitCores record')
    need(evidence, ['runID', 'verdict', 'binding', 'csCPULimitCores'], 'g0Evidence')
    sources = {key: ref(evidence[key]) for key in ('verdict', 'binding')}
    verdict, binding = load(evidence['verdict']), load(evidence['binding'])
    if not isinstance(evidence['runID'], str) or not evidence['runID'].strip() or Path(evidence['verdict']).parent.name != evidence['runID']:
        raise ValueError('G0 runID must identify the recorded verdict directory')
    if Path(evidence['binding']).parent.parent != Path(evidence['verdict']).parent:
        raise ValueError('G0 binding must belong to the recorded run')
    if expected_rates is None:
        shares = pool_shares(config)
        weights = dict(zip(WORKLOADS, (shares['MNV'], shares['PLC'], shares['Smol'])))
        expected_rates = {w['name']: 1.2 * weights[w['name']] * w['R'] for w in config['workloads']}
    if plc_slo is None:
        plc_slo = next(w['slo'] for w in config['workloads'] if w['name'] == 'plc-anomaly')
    validate_g0_evidence(evidence, verdict, binding, weighted_capacity, expected_rates, plc_slo)
    return {'status': 'VALID', 'gatewayVerdict': 'PASS', 'runID': evidence['runID'],
            'csCPUFraction': verdict['cpuCoresPeak'] / evidence['csCPULimitCores'],
            'historicalOverallStatusReference': verdict.get('status'),
            'smolSLOReference': {key: verdict['workloads']['smolvlm2-vqa'].get(key)
                                 for key in ('SLOVerdict','successFraction','withinSLOFraction')},
            'successFractionReference': {name: row.get('successFraction') for name, row in verdict['workloads'].items()
                                         if name != 'smolvlm2-vqa'}, 'sources': sources}

def require_strict_archives(config):
    """Reject diagnostic archive tolerance anywhere in normative E5 inputs."""
    if isinstance(config, dict):
        if 'tolerateArchive' in config:
            raise ValueError('tolerateArchive is diagnostic-only in demo.py; forbidden in normative E5 configuration')
        for value in config.values(): require_strict_archives(value)
    elif isinstance(config, list):
        for value in config: require_strict_archives(value)


def load_binding(path):
    binding = load(path)
    if binding.get('schema') != 'demo-e5-binding.v1': require_strict_archives(binding)
    return binding



def save(path, obj):
    path = Path(path)
    if OPS.resolve() not in path.resolve().parents: raise ValueError('Output must remain below the external ops directory')
    path.parent.mkdir(parents=True, exist_ok=True); path.write_text(json.dumps(obj, indent=2, allow_nan=False)+'\n')


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def need(obj, names, label):
    if not isinstance(obj, dict): raise ValueError(label+': object required')
    for key in names:
        if key not in obj or obj[key] is None: raise ValueError(label+': required '+key)


def positive(x):
    return type(x) in (int, float) and math.isfinite(x) and x > 0


def token_preflight(config, stages):
    config['timeboxes'].setdefault('finishResetWaitSeconds', 600)
    auth = module('e5_auth', HERE / 'auth.py')
    budget = auth.lifetime_budget(stages, config['timeboxes'], config.get('tokenSafetyMarginSeconds', 60))
    return auth.check(config['tokenFile'], budget, endpoint=config.get('endpoint'), ca_file=config.get('caFile'),
                      timeout=config['timeboxes']['apiSeconds'])


def ref(path, allow_empty=False):
    p = Path(path)
    if not p.is_absolute() or not (str(p).startswith(str(OPS)+'/') or str(p).startswith(str(REPO)+'/')): raise ValueError('Absolute approved-tree path required: '+str(p))
    if any(x.lower() in ('secret', 'secrets', '.token', 's3.env') or x.endswith('.token') for x in p.parts): raise ValueError('Sensitive input excluded: '+str(p))
    size=p.stat().st_size
    if not size and not allow_empty: raise ValueError('Empty raw input: '+str(p))
    digest=hashlib.sha256()
    with p.open('rb') as stream:
        for chunk in iter(lambda:stream.read(1024*1024),b''):digest.update(chunk)
    return {'path':str(p),'bytes':size,'sha256':digest.hexdigest()}


def collection_manifest(root,binding):
    root=Path(root);children=list(root.rglob('collected.json'))+list(root.rglob('collection-manifest.json'))
    children=[p for p in children if p.parent!=root];covered={p.parent for p in children};artifacts=[]
    for path in sorted(root.rglob('*')):
        if not path.is_file() or path==root/'collection-manifest.json':continue
        if any(parent in covered for parent in path.parents) and path.name not in ('collected.json','collection-manifest.json','judgment.json'):continue
        artifacts.append(ref(path,allow_empty=True))
    save(root/'collection-manifest.json',{'schema':'e5-private-campaign-collection.v1','at':now(),'visibility':'PRIVATE_OPERATIONAL_ONLY; no raw/configuration publication','binding':ref(binding),'artifacts':artifacts,'meaning':'Nested private collection manifests cover original files; include actual judgments, command receipts, selected static configuration, factor resets and physical returns without duplicating large raw bytes.'})


def d25_gate(index_path,snap,refs=None,measurements=None):
    refs={} if refs is None else refs
    expected_path=Path(E5['d25Manifest'])
    expected={x['caseID']:x for x in load(expected_path)['targets']}; closures=load(index_path)['closures']
    if len(closures)!=25 or {x['caseID'] for x in closures}!=set(expected): raise ValueError('Actual exact25 closure index required; preparation manifest is not completion evidence')
    for entry in closures:
        need(entry,['caseID','crPath','rawPath','returnStatePath','returnEvidencePath','returnedMode'],'d25 closure index')
        for field in ('crPath','rawPath','returnStatePath','returnEvidencePath'): refs[entry['caseID']+'-'+field]=ref(entry[field])
        cr=load(entry['crPath']); raw=load(entry['rawPath']); wanted=expected[entry['caseID']]
        accepted = d25_acceptance(cr,raw)
        if measurements is not None: measurements[entry['caseID']] = accepted
        if any(raw.get(k)!=wanted[k] for k in ('node','profileID','powerMode','runtimeImage')): raise ValueError('d25 exact Node/profile/mode/runtime mismatch')
        key=cr['status']['result']['key']
        consumers=[o for o in snap['dispatchOccupancy'] if o['source']==key and o['node']==raw['node'] and o['powerMode']==raw['powerMode'] and o['runtimeImage']==raw['runtimeImage'] and o['model']==raw['model'] and o['perRequestNanos']==max(0,raw['perRequestNanos'])]
        if len(consumers)!=1: raise ValueError('Actual d25 signed-raw/planner-max0 consumer replacement missing')
        returned=load(entry['returnStatePath']); node=next(n for n in returned['snapshot']['nodes'] if n['node']==raw['node'])
        if not node['ready'] or node.get('measurement') or node['powerMode']!=entry['returnedMode'] or any(i['node']==raw['node'] for i in instance_rows(returned['snapshot']['instances'])) or any(a.get('node')==raw['node'] and a.get('status') in ('running','cancel') for a in returned.get('actions',[])): raise ValueError('Actual same-node normal-return state incomplete')
    return refs


def instance_rows(value):
    """Only Go's nil instance slice is empty; non-null values stay unmodified."""
    return () if value is None else value


def routing_weights(routing):
    """Go nil/empty UID maps and zero weights carry no active distribution."""
    result={}
    for uid,weights in (routing or {}).items():
        active={identifier:weight for identifier,weight in (weights or {}).items() if weight!=0}
        if active:result[uid]=active
    return result


def fingerprint(snapshot, uids):
    return {'instances': sorted([{'id': i['id'], 'node': i['node'], 'profileID': i['profile']['id'], 'allocation': i['allocation'], 'maxBatch': i['maxBatch'], 'maxWaitNanos': i['maxWaitNanos'], 'workloadUID': i['workloadUID']} for i in instance_rows(snapshot['instances']) if i.get('workloadUID') in uids], key=lambda i: i['id']), 'routing': {u: snapshot.get('routing', {}).get(u, {}) for u in uids}, 'modes': {n['node']['name']: n['powerMode'] for n in snapshot['nodes'] if n['node']['name'] in NODES}}


def scheduler_boundary_observation(state):
    snapshot = state['snapshot']
    return {'observedUTC': now(), 'snapshotAt': snapshot.get('at'),
            'reservations': len(snapshot.get('reservations') or []),
            'transitions': sum(action.get('status') in ('running', 'cancel') for action in state.get('actions') or []),
            'snapshotTransitions': sum(action.get('status') in ('running', 'cancel') for action in snapshot.get('actions') or []),
            'meaning': 'Observation only; stage progression does not wait for scheduler quiet'}


def owned_target(snapshot,uids,routing):
    """Declared target from real physical ownership; never a served-UID observation."""
    if any(i.get('ownerWorkloadUID') not in uids for i in instance_rows(snapshot['instances'])):raise RuntimeError('Foreign physical ownership; no owned-target/reset substitution')
    return {'instances':sorted([{'id':i['id'],'node':i['node'],'profileID':i['profile']['id'],'allocation':i['allocation'],'maxBatch':i['maxBatch'],'maxWaitNanos':i['maxWaitNanos'],'workloadUID':i['ownerWorkloadUID']} for i in instance_rows(snapshot['instances'])],key=lambda i:i['id']),'routing':routing,'modes':{n['node']['name']:n['powerMode'] for n in snapshot['nodes'] if n['node']['name'] in NODES}}


def paired_baseline(path, uids):
    record=load(path)
    desired=fingerprint(record.get('state',record)['snapshot'],uids)
    if 'fingerprint' in record and desired!=record['fingerprint']:
        raise RuntimeError('Frozen selection state differs from its recorded fingerprint')
    return desired


def baseline_settings(path):
    record=load(path)
    return copy.deepcopy(record['settings'] if 'settings' in record else load(path.with_name('pre-settings.json')))


def restoration_exclusions(path,uids):
    record=load(path); snapshot=record.get('state',record)['snapshot']
    return {'originalReservations':len(snapshot.get('reservations') or []),
            'excludedOwnerOnlyInstances':[i['id'] for i in instance_rows(snapshot['instances']) if i.get('ownerWorkloadUID') in uids and i.get('workloadUID') not in uids],
            'meaning':'Existing fingerprint restores served canonical instances/routes/modes only; reservations and owner-only non-serving preparations are not restored.'}


def full_restore_path(root,row,tag=None):
    if tag:return root/('full-'+row+'-'+tag+'-restore.json')
    paths=sorted(root.glob('full-'+row+'-*-restore.json'))
    if len(paths)>1:raise ValueError('Multiple full restorations for '+row+'; specify --row-tag')
    return paths[0] if paths else None


def rebound_fingerprint(desired, mapping):
    chosen=copy.deepcopy(desired)
    for target in chosen['instances']:target['id']=mapping[target['id']]
    chosen['instances'].sort(key=lambda target:target['id'])
    chosen['routing']={uid:{mapping[old]:weight for old,weight in route.items()} for uid,route in desired['routing'].items()}
    return chosen


def prepare_body(target):
    return {'kind':'prepare','workloadUID':target['workloadUID'],'profileID':target['profileID'],'node':target['node'],'maxBatch':target['maxBatch'],'reason':'Restore actual paired baseline'}


def replay_receipt(root):
    path=root/'static-replay-restore.json'
    return load(path) if path.exists() else {}


def row_folder(root, mode, row, tag=None):
    if mode=='full':
        path=full_restore_path(root,row,tag)
        if path and path.exists():return root/('full-'+row+'-'+load(path)['rowTag'])
        if tag and row=='E5-1':return root/('full-'+row+'-'+tag)
    if tag is None:tag=replay_receipt(root).get('rowTag','')
    appended=mode=='static' and row in ('E5-3','E5-4','E5-5','E5-6') or mode=='full' and row in ('E5-4','E5-5','E5-6','E5-7')
    return root/(mode+'-'+row+('-'+tag if tag and appended else ''))


def selected_fingerprint(root):
    restored=replay_receipt(root)
    if restored and not restored['accepted']:raise RuntimeError('Static restoration fingerprint was rejected; no subsequent row')
    return restored['fingerprint'] if restored else load(root/'static-selection.json')['fingerprint']


def static_restore_dry_run(binding, root, pre_state, tag):
    b=load_binding(binding);uids=[w['uid'] for w in b['config']['workloads']]
    desired=paired_baseline(pre_state,uids)
    if desired!=load(root/'static-selection.json')['fingerprint']:raise RuntimeError('Offline restoration input differs from original selection')
    settings=copy.deepcopy(load(pre_state)['settings']);settings['autonomousWorkloads']=[]
    for key in ('staticRouting','staticRoutingValidatedAt','powerObservation'):settings.pop(key,None)
    result={'schema':'e5-static-restore-dry-run.v1','at':now(),'clusterRequests':0,
            'scientificResults':'UNMEASURED; offline plan only','preState':ref(pre_state),
            'sources':b['config']['approval']['sources'],
            'expectedFingerprint':desired,'identityComparison':'Exact fingerprint after old-to-new physical ID rebinding only; no allocation/profile/cap/wait/route/mode tolerance',
            'autonomyOffSettingsPUT':settings,'routeRequestMeaning':'Old IDs shown in templates; substitute only physical IDs returned in actual prepare ACKs',
            'requests':[{'kind':'reset','workloadUIDs':uids,'reason':'Approved campaign owned reset: factor-static-E5-3-'+tag}]
                       +[{'kind':'unroute','workloadUID':uid,'reason':'Keep paired factor preparations unloaded until exact baseline route ACK'} for uid in uids]
                       +[prepare_body(target) for target in desired['instances']],
            'routeRequests':[{'kind':'route','workloadUID':uid,'weights':weights,'reason':'Restore actual paired initial block through public prepare and route operations'} for uid,weights in desired['routing'].items()],
            'nativeModeTargets':desired['modes'],'diskMinimumBytes':310_000_000_000,
            'order':['factor --mode static --stage E5-3 --pre-state '+str(pre_state)+' --row-tag '+tag,
                     *['stage --mode static --stage E5-'+str(n)+' --row-tag '+tag for n in range(3,7)],
                     'restore --row-tag '+tag,
                     *['stage --mode full --stage E5-'+str(n)+' --row-tag '+tag for n in range(4,8)],
                     'finish --row-tag '+tag,'summarize','tables'],
            'rowDirectories':[str(row_folder(root,mode,'E5-'+str(n),tag)) for mode,ids in (('static',range(3,7)),('full',range(4,8))) for n in ids]}
    path=root/('static-restore-dry-run-'+tag+'.json')
    if path.exists():raise RuntimeError('Offline plan destination already exists; no overwrite')
    save(path,result)
    print(json.dumps({'resultPath':str(path),**result},ensure_ascii=False,indent=2))


def full_restore_dry_run(binding,root,pre_state,row,tag):
    b=load_binding(binding);uids=[w['uid'] for w in b['config']['workloads']]
    desired=paired_baseline(pre_state,uids);settings=baseline_settings(pre_state)
    if set(settings.get('autonomousWorkloads',[]))!=set(uids):raise ValueError('Original full pre-settings must enable canonical autonomy')
    for key in ('staticRouting','staticRoutingValidatedAt','powerObservation'):settings.pop(key,None)
    off=copy.deepcopy(settings);off['autonomousWorkloads']=[]
    result={'schema':'e5-full-restore-dry-run.v1','at':now(),'clusterRequests':0,
            'scientificResults':'UNMEASURED; offline plan only','preState':ref(pre_state),
            'preSettings':ref(pre_state.with_name('pre-settings.json')) if 'settings' not in load(pre_state) else ref(pre_state),
            'sources':b['config']['approval']['sources'],'label':'표적 재실행, 시작 상태 복원, 소스 R1~R6+R4b+C1+C2','rowTag':tag,
            'expectedFingerprint':desired,'restorationExclusions':restoration_exclusions(pre_state,uids),
            'identityComparison':'One exact fingerprint comparison after prepare-ACK ID rebinding; mismatch exits 2 before stage',
            'autonomyOffSettingsPUT':off,'fullSettingsPUTAtProducerRelease':settings,
            'requests':[{'kind':'reset','workloadUIDs':uids,'reason':'Approved campaign owned reset: factor-full-'+row+'-'+tag}]
                       +[{'kind':'unroute','workloadUID':uid,'reason':'Keep paired factor preparations unloaded until exact baseline route ACK'} for uid in uids]
                       +[prepare_body(target) for target in desired['instances']],
            'routeRequests':[{'kind':'route' if any(weights.values()) else 'unroute','workloadUID':uid,**({'weights':weights} if any(weights.values()) else {})} for uid,weights in desired['routing'].items()],
            'nativeModeTargets':desired['modes'],'diskMinimumBytes':310_000_000_000,
            'order':['factor --mode full --stage '+row+' --pre-state '+str(pre_state)+' --row-tag '+tag,
                     'stage --mode full --stage '+row+' --row-tag '+tag,
                     'judge --out '+str(row_folder(root,'full',row,tag))],
            'failureInjection':'Operator only: failure-select immediately before each injection; execute emitted UID-precondition runbook and record actual command/result. Unsatisfied selection cancels injection as INVALID.' if row=='E5-7' else None}
    path=root/('full-'+row+'-restore-dry-run-'+tag+'.json')
    if path.exists():raise RuntimeError('Offline plan destination already exists; no overwrite')
    save(path,result);print(json.dumps({'resultPath':str(path),**result},ensure_ascii=False,indent=2))

def power_observation(c, *, frozen=False):
    """Read the server-accepted operating-power policy through CHILL settings."""
    if frozen:
        policy = load(c['inputs']['powerObservation'])['schedulerSettings']['powerObservation']
    else:
        request = urllib.request.Request(c['endpoint']+'/api/v1/scheduler/settings', headers={'Authorization':'Bearer '+(OPS/'.token-collector').read_text().strip()})
        auth = module('e5_power_auth', HERE/'auth.py')
        opener = urllib.request.build_opener(auth.NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=c['caFile'])))
        with opener.open(request, timeout=c['timeboxes']['apiSeconds']) as response:
            policy = json.load(response)['powerObservation']
    coverage = policy['coverage']
    if not all(positive(coverage[k]) for k in ('MaximumGap', 'MaximumBoundaryDistance', 'MinimumCoverage')) or coverage['MinimumCoverage'] > 1:
        raise ValueError('Explicit finite operating power coverage required')
    return policy



def bind(path):
    c = load(path); require_strict_archives(c)
    c['failureSelectSnapshotTTLSeconds']=failure_select_snapshot_ttl(c)
    freeze_producer_config(c)
    module('e5_quality', D2/'analyze_before.py').generator_quality_config(c)
    need(c, ['approval', 'inputs', 'g0Evidence', 'mSeconds', 'N', 'burstLowSeconds', 'timeboxes', 'smokeSeconds', 'workloads', 'collection', 'factors', 'endpoint', 'caFile', 'tokenFile', 'kubeContext'], 'phase6')
    c['collectorTokenFile']=str(OPS/'.token-collector')
    need(c['approval'], REQUIRED['approval'], 'approval'); need(c['inputs'], REQUIRED['inputs'], 'inputs'); need(c['collection'], REQUIRED['collection'], 'collection'); need(c['timeboxes'], REQUIRED['timeboxes'], 'timeboxes')
    c['collection']['energyCoverage']=module('e5energy_policy',HERE/'e5-archive.py').energy_policy(c['collection'])
    component_sources = approval_sources(c['approval'])
    if not positive(c['smokeSeconds']) or type(c['mSeconds']) not in (int,float) or not math.isfinite(c['mSeconds']) or c['mSeconds']<0 or not 0 < c['burstLowSeconds'] < 900: raise ValueError('Actual m/smoke and 0<burstLowSeconds<900 required')
    if any(not positive(v) for v in c['timeboxes'].values()) or c['timeboxes']['setupSeconds']+3600+c['timeboxes']['drainSeconds']+c['timeboxes']['flushSeconds'] > 4200: raise ValueError('Finite collector-compatible timeboxes required (sum<=4200s)')
    if type(c['N']) is not int or c['N'] < 1 or c['N'] != len(c['factors']): raise ValueError('N must equal a nonempty preselected witnessed factor set; absent witness is NOT_READY')
    refs = {k: ref(v) for k, v in c['inputs'].items()}
    refs['inputConfig'] = ref(path)
    for name in ('e5.py','auth.py','e5-wire.py','e5-producer.py','e5-raw.py','e5-archive.py'): refs['package-'+name]=ref(HERE/name)
    refs['operationalRecipe']=ref(OPERATIONAL_RECIPE); refs['experimentEnvironment']=ref(ENVIRONMENT_PATH)
    for name in ('conduct_before.py','bind_before.py','analyze_before.py','sample_cgroups.py','native_mode_request.py'):refs['D2-helper-'+name]=ref(D2/name)
    receipt=load(c['collection']['powerReceipt']); power=ref(c['collection']['powerExecutable'])
    require_component_source(receipt,component_sources['power'],'power',power['path'])
    if receipt.get('executable')!=power['path'] or receipt.get('sha256')!=power['sha256'] or receipt.get('buildExitCode')!=0: raise ValueError('Actual frozen-source existing offline power tool receipt required')
    refs['powerExecutable']=power;refs['powerReceipt']=ref(c['collection']['powerReceipt'])
    s = load(c['inputs']['scheduler']); snap = s['state']['snapshot'] if 'state' in s else s['snapshot']; settings = s['settings']
    cap = load(c['inputs']['capacity']); rows = cap if isinstance(cap, list) else cap['rows']
    capacity_source = load(c['inputs']['capacityScheduler'])
    capacity_snapshot = capacity_source['state']['snapshot']
    recorded_payload = load(c['inputs']['evaluationPayload'])['payload']
    c['d25Acceptance'] = {}
    d25_gate(c['inputs']['d25'],snap,refs,c['d25Acceptance'])
    approved_modes=defaultdict(set)
    scope_path=Path(E5['d25Manifest'])
    for case in load(scope_path)['targets']:approved_modes[case['node']['name']].add(case['powerMode'])
    mode_scope=load(c['inputs']['ranking'])['baselineModes']
    if set(mode_scope)!=set(NODES) or any(mode not in approved_modes[name] for name,mode in mode_scope.items()):raise ValueError('Actual reference inference modes must remain within approved d25/G2 conditions; do not expand a power-mode study or manufacture native readback')
    refs['approvedInferenceModeScope']=refs['ranking']
    gateway = load(c['inputs']['gateway'])
    need(gateway, ['requestTimeoutSeconds', 'sourceSHA', 'controlImageDigest', 'capturedUTC'], 'Actual gateway HandlerConfig capture')
    require_component_source(gateway,component_sources['server'],'server')
    if gateway['controlImageDigest'] != c['approval']['controlImageDigest']: raise ValueError('Gateway actual source/image mismatch')
    producer_receipt = load(c['inputs']['producer'])
    need(producer_receipt, ['sourceSHA', 'imageDigest', 'deploymentReceipt'], 'Actual deployed producer receipt')
    require_component_source(producer_receipt,component_sources['loadgen'],'loadgen')
    refs['producer-deployment'] = ref(producer_receipt['deploymentReceipt'])
    native = module('d2eligibility', D2/'bind_before.py')
    if settings['completionMarginMS']/1000 != c['mSeconds']: raise ValueError('m must equal actual scheduler completionMarginMS')
    if sorted(settings['excludedNodes']) != sorted(E5['excludedNodes']): raise ValueError('Approved seven-node/genesis2 hardware-exclusion scope required')
    if len(c['workloads']) != 3 or {w['name'] for w in c['workloads']} != set(WORKLOADS): raise ValueError('Exactly three canonical workloads required')
    sys.path.insert(0, str(REPO/'load-generator/src')); from chill_loadgen import cli
    payload = {}; kappa = {}
    for w in c['workloads']:
        need(w, REQUIRED['workload'], w.get('name', 'workload'))
        if not all(positive(w[k]) for k in ('R', 'deliverySeconds', 'socketSeconds', 'gatewayCapSeconds', 'workers')): raise ValueError('Actual positive workload numeric bindings required')
        if type(w['workers']) is not int: raise ValueError('Integer workers required')
        need(w['slo'], ['percentile', 'targetLatencyNanos'], 'SLO')
        if not 0 < w['slo']['percentile'] <= 100 or not positive(w['slo']['targetLatencyNanos']): raise ValueError('Invalid SLO')
        lifetime=w.get('lifetimeSeconds')
        if lifetime is not None and (not positive(lifetime) or lifetime < w['slo']['targetLatencyNanos']/1e9 or w['gatewayCapSeconds'] < lifetime or w['socketSeconds'] <= lifetime+w['deliverySeconds']): raise ValueError('Explicit lifetime requires L>=SLO, cap>=L, socket>L+delivery')
        if w['gatewayCapSeconds'] != gateway['requestTimeoutSeconds']: raise ValueError('Workload cap differs from actual captured HandlerConfig.RequestTimeout')
        actual = [x for x in snap['workloads'] if x['uid'] == w['uid']]
        if len(actual) != 1 or any(actual[0].get(k) != w[k] for k in ('name', 'generation', 'slo')): raise ValueError('Actual Workload UID/generation/SLO mismatch: '+w['name'])
        definition=actual[0]
        w['definitionID']='/'.join(str(x) for x in (w['uid'],w['generation'],definition['model']['uid'],definition['model']['generation'],definition['preprocessor']['uid'],definition['preprocessor']['generation']))
        if w['name'] == WORKLOADS[0] and w['slo'] != {'percentile': 95, 'targetLatencyNanos': 1000000000}: raise ValueError('Canonical tight MNV p95 1s required')
        source_definitions = [x for x in capacity_snapshot['workloads'] if x['name']==w['name']]
        if len(source_definitions)!=1 or any(source_definitions[0].get(k)!=definition.get(k) for k in ('name','generation','slo','model','preprocessor')):
            raise ValueError('Capacity source/current canonical definitions differ; original UID/rows cannot be relabeled')
        capacity_uid = source_definitions[0]['uid']
        w['capacityDefinitionProvenance'] = {'sourceUID':capacity_uid,'currentUID':w['uid'],
                                            'source':refs['capacityScheduler'],'identity':'Exact name/generation/SLO/model/preprocessor equality; original rows preserved'}
        selected = [r for r in rows if r.get('input', {}).get('workloadUID') == capacity_uid and r['input'].get('slo') == w['slo'] and r['input'].get('completionMarginMS') == settings['completionMarginMS'] and r['input'].get('node', {}).get('name') in NODES]
        outside_scope=[r for r in selected if r['input'].get('powerMode')!=mode_scope[r['input']['node']['name']]]
        selected=[r for r in selected if r['input'].get('powerMode')==mode_scope[r['input']['node']['name']]]
        if any(r.get('status') not in ('Built','CapacityRejected') or r.get('previous') or not r.get('key') or not r.get('builtAt') for r in selected): raise ValueError('Pending/unknown/stale capacity rows cannot bind R')
        if {r['input']['node']['name'] for r in selected}!=set(NODES): raise ValueError('Actual capacity evidence missing for an E5 Node; no zero fill')
        rejected=[r for r in selected if r['status']=='CapacityRejected']
        if any(not r.get('reason') for r in rejected): raise ValueError('CapacityRejected needs its actual source rejection reason')
        selected=[r for r in selected if r['status']=='Built']
        if any(type(r.get('kappa')) not in (int,float) or not math.isfinite(r['kappa']) or r['kappa']<0 for r in selected): raise ValueError('Built kappa missing/invalid; no zero fill')
        eligible=[];native_rejected=[]
        for row in selected:
            inp = row['input'];matches=[];reasons=[]
            for option in snap['options']:
                instance = option['instance']; profile = instance['profile']
                if '$ref' in profile: profile = s['profiles'][profile['$ref']]
                if w['uid'] not in (option.get('compatibleUIDs') or []): continue
                expected = {'node': instance['node'], 'profileID': profile['id'], 'powerMode': profile['condition']['powerMode'], 'allocation': instance['allocation'], **{key: settings[key] for key in native.SETTINGS}}
                if any(inp.get(key) != value for key, value in expected.items()): continue
                node = next(n for n in snap['nodes'] if n['node'] == instance['node'])
                actual_reasons=native.eligibility(instance,profile,node,None,settings,inp['maxBatch'])
                if profile['condition']['runtimeImage'].rsplit('sha256:',1)[-1] not in c['approval']['runtimeDigests']:actual_reasons.append('RuntimeOutsideFrozen7')
                reasons.extend(actual_reasons)
                matches.append(instance)
                if not actual_reasons:eligible.append(row);break
            if not matches: raise ValueError('Built capacity row lacks its actual matching source option: '+str(row['key']))
            if row not in eligible:
                native_rejected.append({'row':row,'nativeEligibilityReasons':sorted(set(reasons))});continue
            occupancy = inp.get('dispatchOccupancy')
            if not occupancy or not occupancy.get('source') or occupancy not in snap['dispatchOccupancy']: raise ValueError('Missing exact signed d source/planner max0 occupancy')
        selected=eligible
        by = {n: max([r['kappa'] for r in selected if r['input']['node']['name']==n], default=0) for n in NODES}
        if not math.isclose(sum(by.values()), w['R'], rel_tol=1e-12): raise ValueError('R must equal actual per-Node maximum eligible Built kappa sum; source-rejected rows remain excluded with reasons')
        kappa[w['name']] = {'perNodeEligibleContribution':by,'R':w['R'],'rows':selected,'sourceRejectedRows':rejected,'nativeEligibilityRejectedRows':native_rejected,'modeScope':mode_scope,'scopeSource':str(scope_path),'outsideApprovedModeScopeRows':outside_scope,'meaning':'R is the per-Node eligible maximum within existing approved reference inference conditions, not every conceptual Built row or a native realization ACK. Zero eligible contribution requires actual source/native rejection evidence; missing data is never filled with zero. Physical factor reset is separately checked against actual selected configuration.'}
        records = cli.load_records(Path(w['datasetDir']), w['fields'])
        producer = module('e5producer_identity', HERE/'e5-producer.py')
        payload[w['name']] = producer.identity(records)
        if payload[w['name']] != recorded_payload[w['name']]:
            raise ValueError('Host evaluation payload differs from actual captured producer identity')
        w['profilingEvaluationSeparation'] = profiling_evaluation_separation(w, payload[w['name']])
        template = load(w['jobTemplate']); refs['job-'+w['name']] = ref(w['jobTemplate'])
        pod = template['spec']['template']['spec']
        if pod['restartPolicy'] != 'Never' or len(pod['containers']) != 1 or pod['containers'][0]['name'] != 'load': raise ValueError('Actual single load container/Never Job template required')
        if pod['containers'][0]['image'] != producer_receipt['imageDigest']: raise ValueError('Producer template image does not match actual final source CLI receipt')
        for container in pod['containers']+pod.get('initContainers', []):
            if '@sha256:' not in container['image']: raise ValueError('Actual pinned producer and dataset image digests required')
        for key in ('datasetDir', 'jobTemplate'):
            if not Path(w[key]).is_absolute(): raise ValueError('Absolute workload paths required')
    for f in c['factors']:
        need(f, REQUIRED['factor'], 'factor')
        if f['name'] not in ('membership-off', 'replicas-off') and not f.get('approvedDirection'): raise ValueError('Additional factor needs explicit approved direction')
        if set(f['controls']) != set(settings['controls']) or not any(f['controls'][k] != settings['controls'][k] for k in f['controls']): raise ValueError('Factor must change actual source controls')
        refs['witness-'+f['name']] = ref(f['witness'])
    refs['hosts'] = ref(c['collection']['hosts'])
    stages = normative_stage_specs(c['burstLowSeconds'])
    c['failureInjectionRunbook'] = copy.deepcopy(load(HERE/'demo-config.e5.example.json')['failureInjectionRunbook'])
    refs['failureRunbook'] = ref(HERE/'demo-config.e5.example.json')
    ws = {w['name']: w for w in c['workloads']}; schedule = []
    shares = pool_shares(c)
    share_by_workload = dict(zip(WORKLOADS, (shares['MNV'], shares['PLC'], shares['Smol'])))
    g0 = g0_evidence_gate(c, sum(ws[name]['R'] * share_by_workload[name] for name in WORKLOADS))
    refs.update({'g0Evidence-'+key: value for key, value in g0['sources'].items()})
    c['g0Gate'] = {key: value for key, value in g0.items() if key != 'sources'}
    for label, duration, fractions in stages:
        intended = {name: ws[name]['R']*share_by_workload[name]*fractions[j] for j, name in enumerate(WORKLOADS)}; rates = dict(intended)
        st = {'id': label, 'seconds': duration, 'poolShares': shares, 'alpha': dict(zip(WORKLOADS, fractions)), 'intendedRates': intended, 'rates': rates, 'traces': {}}
        for name, rate in rates.items():
            offsets = cli.arrival_offsets_ns('poisson', rate, duration, SEEDS[name]) if rate else []
            st['traces'][name] = {'seed': SEEDS[name], 'offsetsNS': offsets, 'offered': len(offsets), 'arrivalSHA256': hashlib.sha256(json.dumps(offsets, separators=(',', ':')).encode()).hexdigest()}
        schedule.append(st)
    # A normative row has one producer window; rate changes have no drain/setup hole.
    combined = []
    for row_id in ['E5-0', 'E5-1', 'E5-2', 'E5-3', 'E5-4', 'E5-5', 'E5-6', 'E5-7']:
        parts = [part for part in schedule if part['id'] == row_id or part['id'].startswith(row_id+'-')]
        row = {'id': row_id, 'seconds': sum(part['seconds'] for part in parts), 'parts': parts, 'rates': {name: max(part['rates'][name] for part in parts) for name in WORKLOADS}, 'traces': {}}
        if row_id == 'E5-7':
            row['failureInjectionRunbook'] = copy.deepcopy(c['failureInjectionRunbook'])
        for name in WORKLOADS:
            offsets = []; base = 0
            for part in parts:
                offsets.extend(base+x for x in part['traces'][name]['offsetsNS']); base += round(part['seconds']*1e9)
            row['traces'][name] = {'seed': SEEDS[name], 'offsetsNS': offsets, 'offered': len(offsets), 'arrivalSHA256': hashlib.sha256(json.dumps(offsets,separators=(',',':')).encode()).hexdigest()}
        combined.append(row)
    schedule = combined
    smoke = copy.deepcopy(next(row for row in schedule if row['id']=='E5-1')); smoke['id']='SMOKE'; smoke['seconds']=c['smokeSeconds']; smoke['parts']=[dict(smoke['parts'][0], id='SMOKE', seconds=c['smokeSeconds'])]
    for name, rate in smoke['rates'].items():
        offsets = cli.arrival_offsets_ns('poisson', rate, c['smokeSeconds'], SEEDS[name]) if rate else []
        smoke['traces'][name] = {'seed': SEEDS[name], 'offsetsNS': offsets, 'offered': len(offsets), 'arrivalSHA256': hashlib.sha256(json.dumps(offsets,separators=(',',':')).encode()).hexdigest()}
        if not offsets: raise ValueError('Approved smokeSeconds must produce at least one real recorded request per canonical Workload with frozen seed; do not invent extra arrivals')
    c['smoke'] = smoke
    settings = dict(settings, powerObservation=power_observation(c,frozen=True))
    c['harnessRepositoryCommit']=subprocess.check_output(['git','-C',str(REPO),'rev-parse','HEAD'],text=True).strip()
    c['tokenLifetime'] = token_preflight(c, schedule)
    return {'schema': 'e5-binding.v1', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': now(), 'schedulerSettings': settings, 'config': c, 'sources': refs, 'kappa': kappa, 'payload': payload, 'schedule': schedule, 'rowOrder': ['E5-0','E5-1','E5-2','E5-3','E5-S','E5-4','E5-5','E5-6','E5-7'], 'fullSeconds': 11700, 'staticSeconds': 10200, 'factorSeconds': 2100*c['N'], 'accounting': {'d': '34/34 approved new-rule evidence; d25 closure subset frozen separately', 'exactCost': '69/69 historical exact-cost coverage, not current candidate count', 'evaluation': '0/7', 'planRows': '0/9', 'failureScenario': 'E5-7 scheduled; actual manual operator evidence required'}, 'limitations': ['B-only timing uncertainty; no profile statistical CI/global95 guarantee', 'D04/D09/D14/D23/D33 known systematic profile-serving bias; causal shares unknown', 'Profiling/evaluation record-ID separation and pinned-carrier equality UNKNOWN where actual enumeration is absent; host evaluation matches the retained producer payload', 'Single paired seed set is a case study, not three independent repetitions', E5['runtimeExclusionNote']]}


class CPO:
    def __init__(self, a):
        self.a = a; self.b = load_binding(a.binding); self.c = self.b['config']; self.out = a.out
        if self.b['status'] != 'BOUND_NOT_MEASURED': raise ValueError('Binding not ready')
        if 'powerObservation' not in self.b.get('schedulerSettings', {}):
            raise ValueError('Binding lacks frozen operating power observation; bind again')
        release = load(a.release)
        self.release = release
        expected={'executor':'CampaignOps','bindingPath':str(a.binding),'sources':approval_sources(self.c['approval'])}
        if approval_sources(release)!=expected['sources'] or any(release.get(k)!=v for k,v in expected.items()) or not release.get('mainInstructionUTC'): raise ValueError('Actual Main/CPO frozen component-source release required')
        if release.get('command')!='e5-execute' and (release.get('command')!=a.command or release.get('stage')!=a.stage): raise ValueError('Command outside frozen release')
        if not a.out.is_absolute() or not a.binding.is_absolute() or not a.release.is_absolute() or CAMPAIGN.resolve() not in a.out.resolve().parents: raise ValueError('Absolute external campaign output and input paths required')
        self.out.mkdir(parents=True, exist_ok=True)
        d = module('d2conduct', D2/'conduct_before.py'); self.d = d; self.io = object.__new__(d.Conductor)
        self.io.out = self.out; self.io.journal = self.out/'commands.jsonl'; self.io.source = self.c['approval']['sources']['server']
        self.io.journal_lock=threading.Lock()
        self.io.a=argparse.Namespace(phase='e5-'+a.mode+'-'+a.stage,command=a.command)
        self.io.phase_out=self.out
        # Existing authenticated client and kubectl contracts, bound to actual phase6 configuration.
        d.BASE = self.c['endpoint']; d.CA = Path(self.c['caFile']); d.TOKEN = Path(self.c['tokenFile']); d.K = ENVIRONMENT['d2']['kubectl']+['-n','chill-system']
        self.io.collector_token_file=OPS/'.token-collector'
        self.io.ctx = ssl.create_default_context(cafile=self.c['caFile']); self.io.http = urllib.request.build_opener(d.NoRedirect(), urllib.request.HTTPSHandler(context=self.io.ctx))
        self.uids = [w['uid'] for w in self.c['workloads']]; self.tb = self.c['timeboxes']; self.ws = {w['name']: w for w in self.c['workloads']}
        self.bound_nodes=[row['node'] for row in self.state()['snapshot']['nodes'] if row['node']['name'] in NODES]
        marker=self.out/('native-mode-authorization'+('-'+a.row_tag if a.row_tag else '')+'.json')
        self.native_mode_authorized_since=load(marker)['authorizedSinceUTC'] if marker.exists() else None
        self.native_evidence=[]
    def disk_boundary(self, row, boundary):
        usage=os.statvfs(self.out);free=usage.f_bavail*usage.f_frsize
        record={'at':now(),'row':row,'boundary':boundary,'availableBytes':free,'minimumBytes':310_000_000_000,'path':str(self.out),'accepted':free>=310_000_000_000}
        with (self.out/'disk-boundaries.jsonl').open('a') as stream:stream.write(json.dumps(record)+'\n')
        if not record['accepted']:raise RuntimeError('Disk free below 310 GB; stop at row boundary')

    def api(self, method, path, body=None):
        settings_path = path == '/api/v1/scheduler/settings'
        timeout = max(30, self.tb['apiSeconds']) if settings_path else self.tb['apiSeconds']
        try:
            return self.io.api(method,path,body,timeout=timeout)
        except (TimeoutError, urllib.error.URLError) as error:
            if not settings_path or method != 'PUT' or not isinstance(getattr(error, 'reason', error), TimeoutError):
                raise
            observed = self.settings()
            mismatched = [key for key, value in body.items() if key not in observed or observed[key] != value]
            if mismatched:
                raise RuntimeError('Settings PUT timed out; readback mismatch; no retransmission: ' + ', '.join(mismatched)) from error
            return {'appliedByReadback': True, 'settings': observed}
    def state(self): return self.api('GET','/api/v1/scheduler/state')
    def settings(self): return self.api('GET','/api/v1/scheduler/settings')
    def put(self, settings):
        settings = copy.deepcopy(settings); settings.pop('staticRouting',None); settings.pop('staticRoutingValidatedAt',None)
        settings.pop('powerObservation', None)
        settings.pop('meters', None)
        settings.pop('requestTimeout', None)
        return self.api('PUT','/api/v1/scheduler/settings',settings)
    def start_barrier_observation(self,state,deadline,observed):
        snapshot=state['snapshot']; reservations=snapshot.get('reservations') or []
        completed=[(self.d.epoch(row.get('endedAt')),row.get('endedAt')) for row in state.get('results') or []]
        latest=max((row for row in completed if row[0] is not None),default=(float('-inf'),None))
        at=self.d.epoch(snapshot.get('at'))
        evidence={'snapshotAt':snapshot.get('at'),'reservationOwners':[row.get('owner') for row in reservations],
                  'reservations':len(reservations),'transitions':sum(a.get('status') in ('running','cancel') for a in snapshot.get('actions') or []),
                  'latestActionEndedAt':latest[1],'snapshotFresh':at is not None and at>latest[0],
                  'timedOut':time.monotonic()>=deadline,'observedUTC':None}
        if not evidence['timedOut'] and evidence['snapshotFresh'] and evidence['reservations']==0 and evidence['transitions']==0:evidence['observedUTC']=observed
        return evidence
    def request_drain(self,folder,phase):
        end=time.monotonic()+self.tb['drainSeconds']
        while time.monotonic()<end:
            state=self.state();views={name:self.api('GET','/api/v1/workloads/'+name) for name in self.ws}
            if not state['snapshot'].get('outstanding') and all(view.get('pending')==0 for view in views.values()):
                at=now()
                phase['demoRequestDrain']={'observedUTC':at,'reservations':state['snapshot'].get('reservations'),'actions':state.get('actions'),'pending':{name:view.get('pending') for name,view in views.items()},'meaning':'Requests drained; scheduler reservations recorded, not awaited'}
                save(folder/'request-drain-state.json',{'at':at,'state':state,'workloads':views})
                return at
            time.sleep(1)
        raise RuntimeError('Request drain timebox exceeded; preserve partial evidence, stop')
    def boundary(self, folder, prefix):
        for label,path in [('state','/api/v1/scheduler/state'),('pools','/api/v1/pools'),('settings','/api/v1/scheduler/settings')]: save(folder/(prefix+'-'+label+'.json'),self.api('GET',path))
        snapshot=load(folder/(prefix+'-state.json'))['snapshot'];actual_workloads=snapshot['workloads'];by_id={i['id']:i for i in instance_rows(snapshot['instances'])}
        for expected in self.c['workloads']:
            found=[w for w in actual_workloads if w['uid']==expected['uid']]
            if len(found)!=1 or any(found[0][key]!=expected[key] for key in ('name','generation','slo')): raise RuntimeError('Canonical UID/generation/SLO changed across frozen E5 inputs')
        for uid in self.uids:
            for identifier,weight in snapshot.get('routing',{}).get(uid,{}).items():
                instance=by_id.get(identifier)
                if weight>0 and (not instance or instance.get('ownerWorkloadUID')!=uid or instance.get('workloadUID')!=uid):raise RuntimeError('Active route requires exact canonical owner plus served UID; prepared owner-only semantics are never a load-ready exemption')
        # Pod identity, image digests and observation-directory bytes are recorded
        # by cluster/resources and collected after drain, never via Kubernetes/host reads.
    def authorize_native_modes(self):
        self.native_mode_authorized_since=now()
        save(self.out/('native-mode-authorization'+('-'+self.a.row_tag if self.a.row_tag else '')+'.json'),{'authorizedSinceUTC':self.native_mode_authorized_since,'release':str(self.a.release),'meaning':'Actual pre-enable authorization fence; not a settings/native success ACK'})
    def static_placement(self):
        return self.b.get('schema')=='demo-e5-binding.v1' and self.b.get('demoConfig',{}).get('staticPlacement') is True
    def check_power_observation(self):
        if power_observation(self.c) != self.b['schedulerSettings']['powerObservation']:
            raise RuntimeError('Operating power observation differs from frozen binding; no E5 load')
    def start(self):
        self.check_token_lifetime()
        if (self.out/'initial.json').exists(): raise RuntimeError('No implicit restart')
        static=self.static_placement()
        s=self.state(); settings=self.settings()
        self.check_power_observation()
        if static:
            if settings.get('autonomousWorkloads'):raise RuntimeError('Static placement requires autonomy already off')
        elif s['snapshot']['instances'] or settings.get('autonomousWorkloads') or settings.get('staticRouting'): raise RuntimeError('Canonical three must start at zero; unrelated serving present')
        if self.b.get('schema') == 'demo-e5-binding.v1' and not static:
            frozen = self.b.get('schedulerSettings', {}).get('minimumDemand')
            if frozen is None or settings.get('minimumDemand') != frozen or any(w['uid'] not in frozen for w in self.c['workloads']):
                raise RuntimeError('minimumDemand differs from frozen canonical UID readback; no demo start')
        for w in self.c['workloads']:
            bound = next(x for x in s['snapshot']['workloads'] if x['uid']==w['uid'])
            if any(bound.get(k)!=w[k] for k in ('name','generation','slo')): raise RuntimeError('Fresh actual Workload generation/SLO changed')
            actual=self.api('GET','/api/v1/workloads/'+w['name']); save(self.out/('workload-'+w['name']+'.json'),actual)
        save(self.out/'initial.json',{'state':s,'settings':settings,'at':now()})
        if static:
            save(self.out/'start-ack.json',{'at':now(),'staticPlacement':True,'meaning':'Observed existing placement; no scheduler mutation'})
            return
        self.release_routing('start-release', 'Release owned routing before automatic campaign start')
        settings['autonomousWorkloads']=self.uids; settings['excludedNodes']=E5['excludedNodes']
        self.authorize_native_modes()
        save(self.out/'start-ack.json',self.put(settings))
    def freeze(self, at_boundary=False):
        if not at_boundary: raise RuntimeError('D9 freeze is executed inside the released full E5-3 command at actual window-end; late baseline selection is forbidden')
        if (self.out/'static-selection.json').exists(): raise RuntimeError('Static selection already frozen')
        s=self.state(); chosen=fingerprint(s['snapshot'],self.uids); settings=self.settings()
        if set(settings['autonomousWorkloads']) != set(self.uids): raise RuntimeError('Actual full-control E5-3 configuration required')
        if any(not chosen['routing'][u] for u in self.uids): raise RuntimeError('Selected canonical route missing')
        save(self.out/'static-selection.json',{'at':now(),'state':s,'schedulerBoundaryObservation':scheduler_boundary_observation(s),'fingerprint':chosen,'settings':settings,'selection':'Actual final E5-3 alpha=0.9 scheduler choice; rate=alpha*share*R; no scheduler-quiet requirement'})
        self.route_ack(chosen,settings,'static','D9 actual E5-3 maximum normal alpha=0.9 selected configuration, same instances/modes/routes')
    def operation(self, body, prefix):
        save(self.out/(prefix+'-request.json'),body)
        timeout = self.tb['settleSeconds']
        if body['kind'] == 'reset':
            power = self.tb.get('powerDownSeconds', 1800)
            if not positive(power):
                raise ValueError('Positive reset powerDownSeconds upper bound required')
            timeout += power + self.tb.get('apiSeconds', 30)
        save(self.out/(prefix+'-http-budget.json'),{'timeoutSeconds':timeout,'contract':'Synchronous OperationResult; reset covers settle + power-down upper bound + API margin'})
        result=self.io.api('POST','/api/v1/scheduler/operations',body,timeout=timeout)
        save(self.out/(prefix+'-result.json'),result)
        if result.get('status')!='complete' or result.get('error'):
            raise RuntimeError('Scheduler '+body['kind']+' failed; retained step/ACK evidence, no subsequent operation')
        return result
    def release_routing(self, prefix, reason):
        for uid in self.uids:
            self.operation({'kind':'release','workloadUID':uid,'reason':reason},prefix+'-'+uid)
    def route_ack(self,chosen,settings,prefix,reason):
        settings['autonomousWorkloads']=[uid for uid in settings.get('autonomousWorkloads',[]) if uid not in self.uids]
        save(self.out/(prefix+'-autonomy-ack.json'),self.put(settings))
        for uid in self.uids:
            weights=chosen['routing'].get(uid) or {}
            body={'kind':'route' if any(weights.values()) else 'unroute','workloadUID':uid,'reason':reason}
            if body['kind']=='route':body['weights']=weights
            result=self.operation(body,prefix+'-'+uid)
            if routing_weights(result.get('actual',{}).get('routing')).get(uid,{})!=routing_weights({uid:weights}).get(uid,{}):
                raise RuntimeError('Completed route ACK differs from selected weights')
        end,_=self.io.publication_end(result,seconds=self.tb['settleSeconds'])
        while True:
            state=self.state()
            save(self.out/(prefix+'-last-state.json'),state)
            if fingerprint(state['snapshot'],self.uids)==chosen:
                save(self.out/(prefix+'-native-ack.json'),result)
                save(self.out/(prefix+'-ready.json'),state)
                return state
            remaining=end-time.monotonic()
            if remaining<=0:raise RuntimeError('Selected physical configuration did not publish after route ACK')
            time.sleep(min(1,remaining))
    def restore(self):
        restored=replay_receipt(self.out)
        if restored and (not restored['accepted'] or restored['rowTag']!=self.a.row_tag):raise RuntimeError('Automatic release requires matching accepted static restoration')
        if self.a.row_tag and (self.out/'restore-ack.json').exists():raise RuntimeError('Static routing already released; no implicit repeat')
        if not (row_folder(self.out,'static','E5-6')/'collected.json').exists(): raise RuntimeError('Complete actual 170-minute static replay required before restoration')
        if self.a.row_tag:self.disk_boundary('restore','before')
        chosen=selected_fingerprint(self.out)
        if fingerprint(self.state()['snapshot'],self.uids)!=chosen: raise RuntimeError('Actual selected static configuration changed before automatic restoration')
        self.release_routing('restore-release','Release static replay routing before automatic restoration')
        settings=self.settings(); settings['autonomousWorkloads']=self.uids
        self.authorize_native_modes()
        save(self.out/'restore-ack.json',self.put(settings))
    def monitor(self,offset,folder):
        path=CAMPAIGN/'control-alerts.jsonl'
        with path.open() as stream:stream.seek(offset);text=stream.read();new_offset=stream.tell()
        if not text:return new_offset
        for line in text.splitlines():
            row=json.loads(line)
            decision=self.d.resolve_control_alert(row,self.native_evidence,self.bound_nodes,authorized_since=self.native_mode_authorized_since if self.a.mode!='static' else None,node_reader=lambda name:self.io.k(['get','node',name,'-o','json'],timeout=self.tb['apiSeconds']))
            if decision['stop']:
                save(folder/'control-stop-alert.json',{'row':row,'decision':decision})
                raise RuntimeError('Immediate CPO/Main control safety STOP: '+decision['classification'])
            if decision['classification'] in ('approved-node-actuation-boot','approved-node-actuation-in-progress'):
                with (folder/'node-actuation-boot-alerts.jsonl').open('a') as out:out.write(json.dumps({'row':row,'decision':decision})+'\n')
        return new_offset
    def sampler(self, folder, seconds):
        sample=module('d2sample',D2/'sample_cgroups.py')
        source=sample.REMOTE
        roles=source.index("                if ns=='chill-system' and name=='serving-entrypoint':")
        role_end=source.index("                if role is None: continue",roles)
        source=source[:roles]+"                if ns=='chill-system' and name=='load' and pod.startswith('e5-'): role='client'\n"+source[role_end:]
        thermal=source.index('    if began>=next_thermal:')
        source=source[:thermal]+source[source.index('    time.sleep(',thermal):]
        # Only external producers are discoverable by this sampler.
        pods=self.io.k(['get','pods','-l','chill.dacs.io/e5='+self.a.mode,'-o','json'],timeout=self.tb['apiSeconds'])['items']
        children=[]
        for h in load(self.c['collection']['hosts']):
            if h['node'] != E5['producerNode']: continue
            argv=[sys.executable,'-u','-'] if h.get('local') else h['ssh']
            if not h.get('local') and argv[-3:] != ['python3','-u','-']: raise RuntimeError('Verified existing producer SSH python sampler argv required')
            output=(folder/(h['node']+'-cgroup.jsonl')).open('w'); error=open(os.devnull,'w')
            current=[{'id':status.get('containerID','').split('://')[-1],
                      'metadata':{'name':status['name'],'attempt':status.get('restartCount')},
                      'labels':{'io.kubernetes.pod.namespace':pod['metadata']['namespace'],
                                'io.kubernetes.pod.name':pod['metadata']['name'],
                                'io.kubernetes.pod.uid':pod['metadata']['uid']}}
                     for pod in pods if pod['spec'].get('nodeName')==h['node']
                     for status in pod.get('status',{}).get('containerStatuses',[])]
            host_source=source.replace('initial=None\n','initial='+repr(current)+'\n',1)
            p=subprocess.Popen(argv+[h['node'],str(seconds),self.a.stage],stdin=subprocess.PIPE,stdout=output,stderr=error,text=True); p.stdin.write(host_source); p.stdin.close(); children.append((p,output,error))
        return children
    def producer_job_name(self,name,stage):
        return 'e5-'+self.a.mode.lower()+'-'+stage['id'].lower()+'-'+str(SEEDS[name])+('-'+self.a.row_tag.lower() if self.a.row_tag else '')
    def job(self, name, stage, folder):
        w=self.ws[name]; trace=stage['traces'][name]; job=load(w['jobTemplate']); label=self.producer_job_name(name,stage)
        job['metadata']={'name':label,'namespace':'chill-system','labels':{'chill.dacs.io/e5':self.a.mode}}
        spec=job['spec']; spec.pop('selector',None); spec.pop('manualSelector',None); spec['backoffLimit']=0; spec['activeDeadlineSeconds']=math.ceil(self.tb['setupSeconds']+self.tb['barrierSeconds']+stage['seconds']+self.tb['drainSeconds']); spec['template']['metadata']={'labels':{'chill.dacs.io/e5':self.a.mode}}
        pod=spec['template']['spec']; set_producer_resources(pod,self.c)
        alias=next(alias for alias,canonical in PRODUCER_ALIASES.items() if canonical==name)
        container=pod['containers'][0]; policy={'payload':self.b['payload'][name], 'arrivalSHA256':trace['arrivalSHA256'], 'offered':trace['offered'], 'shards':self.c['producerShards'][alias], 'barrierSeconds':self.tb['barrierSeconds'], 'segments': [{'rate':part['rates'][name], 'seconds':part['seconds']} for part in stage['parts']]}
        container['command']=['python3','-u','-c',(HERE/'e5-producer.py').read_text()]
        encoded=base64.b64encode(zlib.compress(json.dumps(policy,separators=(',',':'),allow_nan=False).encode())).decode('ascii')
        if len(encoded)>120000: raise RuntimeError('Full frozen payload identity exceeds safe exec argument size; no payload subset is silently dropped')
        container['args']=[encoded,'replay','--endpoint',w['producerEndpoint'],'--token-file',w['producerTokenFile'],'--ca-file',w['producerCAFile'],'--workload',name,'--dataset-dir',w['producerDatasetDir'],'--fields',','.join(w['fields']),'--arrivals','poisson','--seed',str(SEEDS[name]),'--workers',str(w['workers']),'--duration',str(stage['seconds']),'--rate',str(stage['rates'][name]),'--workload-slo',str(w['slo']['targetLatencyNanos']/1e9),'--gateway-timeout',str(w['gatewayCapSeconds']),'--delivery-margin',str(w['deliverySeconds']),'--timeout',str(w['socketSeconds'])]
        if w.get('lifetimeSeconds') is not None:container['args']+=['--request-lifetime',str(w['lifetimeSeconds'])]
        if self.b.get('schema')=='demo-e5-binding.v1':
            # The pinned D2 producer predates these flags; demo prepare checks the same budgets on the host.
            for flag in ('--workload-slo','--gateway-timeout'):
                index=container['args'].index(flag);del container['args'][index:index+2]
        save(folder/('job-'+name+'.json'),job); return job
    def missing_ready_routes(self, state):
        missing=[]
        for uid in self.uids:
            owned={i['id'] for i in state['snapshot'].get('instances') or [] if i.get('ownerWorkloadUID')==uid and i.get('workloadUID')==uid and i.get('ready')}
            if not any(weight>0 and identifier in owned for identifier,weight in (state['snapshot'].get('routing',{}).get(uid) or {}).items()):missing.append(uid)
        return missing
    def check_token_lifetime(self, stage=None):
        stages = self.b['schedule']
        if self.a.command == 'campaign':
            stages = stages + [s for s in stages if s['id'] != 'E5-0'] + [
                s for _ in self.c['factors'] for s in self.b['schedule'] if s['id'] in ('E5-2', 'E5-5')]
        elif self.b.get('schema') != 'demo-e5-binding.v1' and self.a.command == 'smoke':
            stages = [self.c['smoke']]
        elif stage is not None:
            stages = stages[stages.index(stage):] if stage in stages else [stage]
        record = token_preflight(self.c, stages)
        save(self.out / ('token-preflight-' + (stage['id'] if stage else 'start') + ('-'+self.a.row_tag if self.a.row_tag else '') + '.json'), record)
        return record
    def run(self):
        """Recheck frozen operating power policy before any stage can launch load."""
        st=self.c['smoke'] if self.a.command=='smoke' else next(x for x in self.b['schedule'] if x['id']==self.a.stage); folder=row_folder(self.out,self.a.mode,st['id'],self.a.row_tag)
        if folder.exists(): raise RuntimeError('No implicit stage retry/overwrite')
        restore_path=full_restore_path(self.out,st['id'],self.a.row_tag) if self.a.mode=='full' and self.a.row_tag else None
        targeted=bool(restore_path and restore_path.exists())
        restored=load(restore_path) if targeted else replay_receipt(self.out)
        if self.a.row_tag and (not restored or restored['rowTag']!=self.a.row_tag or not restored['accepted']):raise RuntimeError('Tagged rows require matching successful factor restoration')
        if restored and self.a.row_tag!=restored['rowTag']:raise RuntimeError('Restored selection requires explicit matching --row-tag')
        if targeted and (restored['stage']!=st['id'] or self.settings().get('autonomousWorkloads')):raise RuntimeError('Targeted full row requires matching restored state held with autonomy off until producer release')
        if self.a.row_tag:self.disk_boundary(folder.name,'before')
        self.check_token_lifetime(st)
        self.check_power_observation()
        deferred_restore=self.a.mode=='full' and st['id']=='E5-4' and not (self.out/'restore-ack.json').exists()
        if self.a.command!='smoke' and not targeted:
            order = ['E5-0','E5-1','E5-2','E5-3','E5-4','E5-5','E5-6','E5-7'] if self.a.mode=='full' else ['E5-1','E5-2','E5-3','E5-4','E5-5','E5-6'] if self.a.mode=='static' else ['E5-2','E5-5']
            index=order.index(st['id'])
            if index:
                previous=row_folder(self.out,self.a.mode,order[index-1])
                if not (previous/'collected.json').exists() or load(previous/'phase.json')['status']!='DRAINED_NOT_EVALUATED': raise RuntimeError('Previous normative row successful raw capture required')
            if deferred_restore and not (row_folder(self.out,'static','E5-6')/'collected.json').exists(): raise RuntimeError('Actual 170min static replay precedes full E5-4')
        if self.a.mode=='static' and st['id']=='E5-0': raise RuntimeError('Static baseline replays E5-1..6 only')
        if self.a.mode=='static':
            if not restored:load(self.out/'static-ready.json')
            chosen=selected_fingerprint(self.out)
            if self.settings()['autonomousWorkloads'] or fingerprint(self.state()['snapshot'],self.uids)!=chosen: raise RuntimeError('Actual selected static freeze drift')
        elif self.a.mode not in ('full','smoke'):
            factor=next(f for f in self.c['factors'] if f['name']==self.a.mode)
            if not st['id'].startswith(('E5-2','E5-5')): raise RuntimeError('Factor comparison is exactly E5-2/E5-5')
            if self.settings()['controls']!=factor['controls']: raise RuntimeError('Factor source controls not actually active')
        static=self.static_placement()
        if static:
            if self.settings().get('autonomousWorkloads'):raise RuntimeError('Static placement requires autonomy to remain off')
        elif self.a.mode in ('full','smoke') and not deferred_restore and not targeted and set(self.settings()['autonomousWorkloads'])!=set(self.uids): raise RuntimeError('Actual canonical-three autonomous control required')
        for frozen in self.b['sources'].values():
            if ref(frozen['path'])!=frozen: raise RuntimeError('Frozen input/package identity changed; no E5 load')
        jobs_live=self.io.k(['get','jobs','-o','json'])['items']
        expected={self.producer_job_name(name,st) for name,rate in st['rates'].items() if rate}
        collisions=sorted(j['metadata']['name'] for j in jobs_live if j['metadata']['name'] in expected)
        if collisions:raise RuntimeError('Producer Job name already exists (possibly retained from an earlier run): '+', '.join(collisions)+'; no create or automatic deletion attempted')
        if any(not j.get('spec',{}).get('suspend') and not any(x.get('type') in ('Complete','Failed') and x.get('status')=='True' for x in j.get('status',{}).get('conditions',[])) for j in jobs_live): raise RuntimeError('Concurrent nonterminal producer/measurement Job; no E5 load')
        alert_path=CAMPAIGN/'control-alerts.jsonl'
        if not alert_path.exists(): raise RuntimeError('Existing safety alert monitor required')
        alert_offset=alert_path.stat().st_size
        folder.mkdir(); self.io.a.phase='e5-'+self.a.mode+'-'+st['id']; self.io.a.command=self.a.command; self.io.phase_out=folder
        barrier=None
        if self.b.get('schema')=='demo-e5-binding.v1' and 'startBarrier' in st:
            deadline=time.monotonic()+self.tb['barrierSeconds']
            barrier={'maximumSeconds':self.tb['barrierSeconds'],'observedUTC':None,'timedOut':False}
            try:
                while True:
                    alert_offset=self.monitor(alert_offset,folder)
                    state=self.state(); observed=now()
                    barrier.update(self.start_barrier_observation(state,deadline,observed))
                    save(folder/'start-barrier-state.json',{'observedUTC':observed,'state':state,'startBarrier':barrier})
                    if barrier['observedUTC']:break
                    if barrier['timedOut']:raise RuntimeError('Stage start barrier timebox exceeded; no load started')
                    time.sleep(min(.2,max(0,deadline-time.monotonic())))
            except Exception as e:
                phase={'stage':st,'mode':self.a.mode,'status':'STOP','startUTC':None,'endUTC':None,'startBarrier':barrier,'error':str(e),'failureUTC':now()}
                save(folder/'phase.json',phase)
                try:self.cleanup_jobs()
                except Exception as cleanup_error:
                    phase.setdefault('cleanupErrors',[]).append('owned-jobs: '+str(cleanup_error));save(folder/'phase.json',phase)
                raise
        if targeted:
            save(folder/'start-state-restoration.json',restored)
        self.boundary(folder,'pre'); children=self.sampler(folder,self.tb['setupSeconds']+st['seconds']+self.tb['drainSeconds']+self.tb['flushSeconds']); jobs=[]; streams=[]; phase={'stage':st,'mode':self.a.mode,'status':'RUNNING','startUTC':None,'endUTC':None,'starts':{}}
        if barrier is not None:phase['startBarrier']=barrier
        pre_state=load(folder/'pre-state.json')
        phase['schedulerBoundaryObservations']={'start':scheduler_boundary_observation(pre_state)}
        self.native_evidence=(pre_state.get('results') or [])+[load(path) for root in (self.out,self.out.parent/'smoke-run') for path in root.glob('*-mode/result.json')]
        save(folder/'phase.json',phase); sel=selectors.DefaultSelector(); buffers={};pods={};deadline=time.monotonic()+self.tb['setupSeconds']
        try:
            for name,rate in st['rates'].items():
                if not rate: continue
                created=self.io.k(['create','-f','-','-o','json'],stdin=json.dumps(self.job(name,st,folder))); jobs.append((name,created['metadata']['name'],created['metadata']['uid'])); save(folder/('created-'+name+'.json'),created)
            for name,job,uid in jobs:
                startup=folder/('startup-'+name);startup.mkdir()
                pod=self.io.wait_load_container((job,uid),startup,deadline);pods[name]=pod
                log=(folder/(name+'-client.jsonl')).open('w');err=(folder/(name+'-log.stderr')).open('w')
                p=subprocess.Popen(self.d.K+['logs','-f','pod/'+pod['metadata']['name'],'-c','load'],stdout=subprocess.PIPE,stderr=err,bufsize=0);os.set_blocking(p.stdout.fileno(),False);streams.append((p,log,err));buffers[name]=b'';sel.register(p.stdout,selectors.EVENT_READ,(name,log))
            ready=set();window_ended=set();drained=set();counter_released=set();released=not jobs
            if released: phase['startUTC']=now(); end=time.monotonic()+st['seconds']
            ready_gate=self.b.get('schema')=='demo-e5-binding.v1' and st.get('readyGate') is True
            if ready_gate:
                if jobs:raise RuntimeError('Ready gate requires a zero-load stage')
                phase['readyGate']={'maximumSeconds':st['seconds'],'observedUTC':None,'timedOut':False,'missingWorkloadUIDs':list(self.uids)}
                save(folder/'phase.json',phase)
            while True:
                alert_offset=self.monitor(alert_offset,folder)
                if ready_gate:
                    state=self.state(); observed=now()
                    missing=self.missing_ready_routes(state)
                    phase['readyGate']['missingWorkloadUIDs']=missing
                    phase['readyGate']['timedOut']=time.monotonic()>=end
                    if not missing and not phase['readyGate']['timedOut']:
                        phase['readyGate']['observedUTC']=observed
                    if phase['readyGate']['observedUTC'] or phase['readyGate']['timedOut']:
                        save(folder/'ready-gate-state.json',{'observedUTC':observed,'state':state})
                        save(folder/'phase.json',phase)
                        break
                if not released and time.monotonic()>deadline: raise RuntimeError('Producer setup timebox exceeded')
                for key,_ in sel.select(timeout=min(.2,max(0,end-time.monotonic())) if ready_gate else .2):
                    name,log=key.data;lines,buffers[name],eof=self.d.read_stream_lines(key.fileobj,buffers[name])
                    if eof:sel.unregister(key.fileobj)
                    for line in lines:
                        log.write(line+'\n');log.flush()
                        try:
                            row=json.loads(line)
                        except ValueError:
                            continue
                        if not isinstance(row,dict):continue
                        if row.get('e5Event')=='ready':
                            ready.add(name); jobname=next(j for n,j,u in jobs if n==name); found=self.io.k(['get','pods','-l','job-name='+jobname,'-o','json'])['items']
                            uid=next(u for n,j,u in jobs if n==name)
                            if len(found)!=1 or found[0]['metadata']['uid']!=pods[name]['metadata']['uid'] or not any(owner.get('kind')=='Job' and owner.get('uid')==uid for owner in found[0]['metadata'].get('ownerReferences',[])): raise RuntimeError('Producer exact Pod/Job identity changed after startup')
                            if row.get('payload')!=self.b['payload'][name] or row.get('offered')!=st['traces'][name]['offered'] or row.get('arrivalSHA256')!=st['traces'][name]['arrivalSHA256'] or row.get('barrierSeconds')!=self.tb['barrierSeconds']:raise RuntimeError('Actual ready payload/trace/barrier differs from frozen input')
                            alias=next(alias for alias,canonical in PRODUCER_ALIASES.items() if canonical==name)
                            if row.get('shards')!=self.c['producerShards'][alias]:raise RuntimeError('Actual ready shards differs from frozen producerShards')
                            startup=folder/('startup-'+name);save(startup/'producer-ready-event.json',row)
                            self.io.assert_producer_startable(startup,found[0],pods[name],barrier_seconds=self.tb['barrierSeconds'])
                            phase.setdefault('producerReadiness',{})[name]={'producerReadyUTC':row['at'],'setupBarrierDeadlineUTC':dt.datetime.fromtimestamp(self.d.producer_barrier_deadline(row,self.tb['barrierSeconds']),dt.timezone.utc).isoformat(),'readyEventPath':str(startup/'producer-ready-event.json')}
                            save(folder/'phase.json',phase)
                            save(folder/('producer-'+name+'.json'),found[0])
                        if row.get('e5Event')=='start': phase['starts'][name]=row; save(folder/'phase.json',phase)
                        if row.get('e5Event')=='drained':
                            drained.add(name);phase.setdefault('producerDrained',{})[name]=row;save(folder/'phase.json',phase)
                        if row.get('e5Event')=='window-end':
                            window_ended.add(name)
                            if self.a.mode=='full' and st['id']=='E5-3' and len(window_ended)==len(jobs):
                                phase['selectedAtBoundaryUTC']=now(); save(folder/'phase.json',phase); self.freeze(at_boundary=True)
                if jobs and len(phase['starts'])==len(jobs) and phase['endUTC'] is None:
                    phase['startUTC']=min(x['at'] for x in phase['starts'].values())
                    phase['endUTC']=max((dt.datetime.fromisoformat(x['at'])+dt.timedelta(seconds=st['seconds'])).isoformat() for x in phase['starts'].values())
                    save(folder/'phase.json',phase)
                if phase['endUTC'] is not None:
                    for name in sorted(drained-counter_released):
                        pod=pods[name];cid=next(status['containerID'] for status in pod['status']['containerStatuses'] if status['name']=='load')
                        collector=folder/(pod['spec']['nodeName']+'-cgroup.jsonl')
                        counter=self.d.client_end_counter(collector,pod['metadata']['uid'],cid,phase['endUTC'])
                        if counter is None:continue
                        current=self.io.k(['get','pod',pod['metadata']['name'],'-o','json'],timeout=self.tb['apiSeconds'])
                        self.d.assert_producer_running(current,pod)
                        proof=folder/(name+'-client-end-counter.json')
                        save(proof,{'at':now(),'originalEndUTC':phase['endUTC'],'podUID':pod['metadata']['uid'],'containerID':cid,'collectorPath':str(collector),'actualCounterRow':counter,'actualPod':current})
                        self.io.k(['exec',pod['metadata']['name'],'-c','load','--','python3','-c',"from pathlib import Path; Path('/tmp/e5-start').unlink()"],timeout=self.tb['apiSeconds'])
                        counter_released.add(name);phase.setdefault('clientEndCounterRelease',{})[name]={'at':now(),'proofPath':str(proof)};save(folder/'phase.json',phase)
                if not released and len(ready)==len(jobs):
                    coverage=Counter(); unavailable=set()
                    for path in folder.glob('*-cgroup.jsonl'):
                        for line in path.read_text().splitlines():
                            observed=json.loads(line)
                            if observed.get('kind')=='collector-error': raise RuntimeError('Prestart telemetry collector error')
                            if observed.get('kind')=='cgroup' and observed.get('pidAbsent'): unavailable.add((observed['role'],observed.get('pod')))
                            if observed.get('kind')=='cgroup' and all(observed.get(k) is not None for k in ('cpuUsageNanos','nrThrottled','throttledNanos')): coverage[(observed['role'],observed.get('pod'))]+=1
                    if not all(coverage[('client',pod['metadata']['name'])]>=2 or ('client',pod['metadata']['name']) in unavailable for pod in pods.values()): continue
                    for name,pod in pods.items():
                        current=self.io.k(['get','pod',pod['metadata']['name'],'-o','json'],timeout=self.tb['apiSeconds'])
                        self.io.assert_producer_startable(folder/('startup-'+name),current,pod,barrier_seconds=self.tb['barrierSeconds'])
                    if deferred_restore:
                        self.restore()
                        frame=self.state();chosen=selected_fingerprint(self.out)
                        if fingerprint(frame['snapshot'],self.uids)!=chosen or frame['snapshot'].get('reservations'):raise RuntimeError('Selected static configuration was not retained at actual E5-4 producer release')
                        save(folder/'actual-start-configuration.json',frame)
                    if targeted:
                        self.authorize_native_modes()
                        save(folder/'full-settings-release-ack.json',self.put(restored['fullSettings']))
                    # Same barrier file protocol as existing CPO producer; actual UTC start per producer is retained, not claimed identical.
                    if barrier is not None:
                        go_deadline=min(deadline,time.monotonic()+min(dt.datetime.fromisoformat(row['setupBarrierDeadlineUTC']).timestamp()-time.time() for row in phase['producerReadiness'].values()))
                    for name,pod in pods.items():
                        current=self.io.k(['get','pod',pod['metadata']['name'],'-o','json'],timeout=self.tb['apiSeconds'])
                        self.io.assert_producer_startable(folder/('startup-'+name),current,pod,barrier_seconds=self.tb['barrierSeconds'])
                        if barrier is not None:
                            evidence={'observedUTC':None,'timedOut':False}
                            phase['startBarrier'].setdefault('producerRelease',{})[name]=evidence
                            while True:
                                alert_offset=self.monitor(alert_offset,folder)
                                state=self.state();observed=now()
                                evidence.update(self.start_barrier_observation(state,go_deadline,observed))
                                save(folder/('go-barrier-'+name+'-state.json'),{'observedUTC':observed,'state':state,'producerRelease':evidence})
                                save(folder/'phase.json',phase)
                                if evidence['timedOut']:raise RuntimeError('Producer release start barrier timebox exceeded; no GO for '+name)
                                if evidence['observedUTC']:break
                                time.sleep(min(.2,max(0,go_deadline-time.monotonic())))
                        self.io.k(['exec',pod['metadata']['name'],'-c','load','--','python3','-c',"from pathlib import Path; Path('/tmp/e5-start').touch()"],timeout=self.tb['apiSeconds'])
                    if targeted:
                        frame=self.state();expected=restored['fingerprint']['routing']
                        routing={uid:frame['snapshot'].get('routing',{}).get(uid,{}) for uid in self.uids}
                        restored['postGO']={'observedUTC':now(),'state':frame,'routing':routing,'expectedRouting':expected,'accepted':routing==expected}
                        restored['accepted']=restored['accepted'] and restored['postGO']['accepted']
                        save(folder/'start-state-restoration.json',restored)
                        if not restored['postGO']['accepted']:raise RuntimeError('Restored route weights differ from fingerprint immediately after producer GO')
                    phase['startUTC']=now(); end=time.monotonic()+st['seconds']+self.tb['drainSeconds']; released=True; save(folder/'phase.json',phase)
                if released and not ready_gate and time.monotonic()>end:
                    if jobs and (sel.get_map() or any(p.poll() is None for p,l,e in streams)): raise RuntimeError('Producer drain watchdog exceeded')
                    break
                if released and jobs and not sel.get_map() and all(p.poll() is not None for p,l,e in streams): break
            if any(p.wait()!=0 for p,l,e in streams): raise RuntimeError('Producer log stream failed')
            if counter_released!={name for name,job,uid in jobs}:raise RuntimeError('Producer EOF without exact post-end counter/owned-marker release; no completed boundary claim')
            for name,job,uid in jobs:
                terminal_folder=folder/('terminal-'+name);terminal_folder.mkdir()
                terminal=self.io.wait_job_complete((job,uid),terminal_folder,end);save(folder/('terminal-'+name+'.json'),terminal)
            if jobs:
                if len(phase['starts'])!=len(jobs): raise RuntimeError('Missing actual producer start')
                phase['startUTC']=min(x['at'] for x in phase['starts'].values()); phase['endUTC']=max((dt.datetime.fromisoformat(x['at'])+dt.timedelta(seconds=st['seconds'])).isoformat() for x in phase['starts'].values())
            else: phase['endUTC']=now()
            if self.b.get('schema')=='demo-e5-binding.v1':phase['drainedUTC']=self.request_drain(folder,phase)
            else:phase['drainedUTC']=now()
            phase['drainScope']='Producer/request drain only; scheduler reservations and transitions may continue into the next stage'
            self.boundary(folder,'post')
            phase['schedulerBoundaryObservations']['end']=scheduler_boundary_observation(load(folder/'post-state.json'))
            phase['status']='DRAINED_NOT_EVALUATED'; save(folder/'phase.json',phase)
            for sampler,output,error in children:
                intentional=sampler.poll() is None
                if intentional:sampler.terminate()
                code=sampler.wait(timeout=15);output.flush();error.flush()
                if code not in (0,-signal.SIGTERM): raise RuntimeError('Producer cgroup sampler failed')
            time.sleep(self.tb['flushSeconds'])
            self.collect(folder)
        except Exception as e:
            phase['status']='STOP'; phase['error']=str(e); phase['failureUTC']=now(); phase['ownedProducerStops']=[]; save(folder/'phase.json',phase)
            if not static:
                try:
                    prior=load(self.out/'initial.json')['settings'];current=self.settings();restore=copy.deepcopy(current)
                    members=[uid for uid in current.get('autonomousWorkloads',[]) if uid not in self.uids]+[uid for uid in prior.get('autonomousWorkloads',[]) if uid in self.uids]
                    restore['autonomousWorkloads']=members;restore['excludedNodes']=prior['excludedNodes']
                    response=self.put(restore)
                    observed=self.settings()
                    save(folder/'failed-owned-control-restore.json',{'at':now(),'prior':prior,'requested':restore,'response':response,'actual':observed,'topologyRestored':False,'instruction':'Do not recreate missing static/native IDs; preserve actual topology for CPO/Main'})
                    if sorted(observed.get('autonomousWorkloads',[]))!=sorted(members) or sorted(observed['excludedNodes'])!=sorted(prior['excludedNodes']):raise RuntimeError('Prior owned autonomy/exclusions did not restore')
                except Exception as restore_error:phase.setdefault('cleanupErrors',[]).append('owned-control-restore: '+str(restore_error))
            for name,job,uid in jobs:
                try:
                    terminal=self.io.k(['get','job',job,'-o','json']); save(folder/('stop-job-'+name+'.json'),terminal)
                    if terminal['metadata']['uid']!=uid:raise RuntimeError('Owned producer Job UID changed before failure-log capture')
                    try:
                        startup=folder/('startup-'+name);startup.mkdir(exist_ok=True)
                        found=self.io.k(['get','pods','-l','job-name='+job,'-o','json'])['items']
                        for pod in found:
                            if not any(owner.get('kind')=='Job' and owner.get('uid')==uid for owner in pod['metadata'].get('ownerReferences',[])):continue
                            prefix='failed-producer-'+pod['metadata']['uid']
                            captured=subprocess.run(self.d.K+['logs',pod['metadata']['name'],'-c','load','--timestamps=true'],capture_output=True,text=True,timeout=self.tb['apiSeconds'])
                            (startup/(prefix+'.log')).write_text(captured.stdout)
                            (startup/(prefix+'.stderr')).write_text(captured.stderr)
                            save(startup/(prefix+'.json'),{'at':now(),'jobUID':uid,'pod':pod,'exitCode':captured.returncode,'stdoutPath':str(startup/(prefix+'.log')),'stderrPath':str(startup/(prefix+'.stderr'))})
                            if captured.returncode:phase.setdefault('cleanupErrors',[]).append('producer-log-capture '+name+': exit '+str(captured.returncode))
                    except Exception as capture_error:phase.setdefault('cleanupErrors',[]).append('producer-log-capture '+name+': '+str(capture_error))
                except Exception as capture_error:phase.setdefault('cleanupErrors',[]).append('producer-job-capture '+name+': '+str(capture_error))
                try:
                    self.io.remove_job(job,uid); phase['ownedProducerStops'].append({'name':job,'uid':uid,'stopped':True})
                except Exception as stop_error:
                    phase['ownedProducerStops'].append({'name':job,'uid':uid,'stopped':False,'error':str(stop_error)})
            try:self.cleanup_jobs(exclude=folder)
            except Exception as cleanup_error:phase.setdefault('cleanupErrors',[]).append('owned-jobs: '+str(cleanup_error))
            save(folder/'phase.json',phase)
            raise
        finally:
            preserving_primary=sys.exc_info()[0] is not None;cleanup_errors=[]
            for p,l,e in streams:
                try:
                    if p.poll() is None:p.terminate()
                    try:p.wait(timeout=15)
                    except subprocess.TimeoutExpired:p.kill();p.wait()
                    p.stdout.close();l.close();e.close()
                except Exception as cleanup_error:cleanup_errors.append('log-follower: '+str(cleanup_error))
            for p,o,e in children:
                try:
                    if p.poll() is None:p.terminate()
                    try:p.wait(timeout=15)
                    except subprocess.TimeoutExpired:p.kill();p.wait()
                    o.close();e.close()
                except Exception as cleanup_error:cleanup_errors.append('sampler: '+str(cleanup_error))
            if self.a.row_tag:
                try:self.disk_boundary(folder.name,'after')
                except Exception:
                    if not preserving_primary:raise
            if cleanup_errors:
                save(folder/'secondary-cleanup-errors.json',{'at':now(),'primaryError':phase.get('error'),'cleanupErrors':cleanup_errors})
                if not preserving_primary:raise RuntimeError('Cleanup failed: '+str(cleanup_errors))
            # Failure stops only exact-UID owned producers; retained raw/native ownership remains for CPO drain/cleanup.
    def collect(self, folder, phase=None):
        phase=load(folder/'phase.json') if phase is None else phase; start,end=phase['startUTC'],phase['endUTC']
        if not start or not end: raise RuntimeError('Actual interval required')
        collector=module('e5archive',HERE/'e5-archive.py')
        tolerated=set(self.c.get('smoke',{}).get('tolerateArchive',[])) & {'power','power-drain-inclusive'} if self.b.get('schema')=='demo-e5-binding.v1' else set()
        prior=folder/'archive-collection.json'
        captured=load(prior) if tolerated and prior.exists() else collector.collect(folder,phase,self.c['collection'],self.io,self.tb['collectSeconds'])
        missing={}; measurement_failures={}
        if captured.get('status')!='CAPTURED_NOT_EVALUATED':
            errors=captured.get('errors') or []
            incomplete_power={name for name in ('power','power-drain-inclusive')
                              if (folder/(name+'.json')).exists() and load(folder/(name+'.json')).get('complete') is False}
            for error in errors:
                name=error.split(': ',1)[0]
                if name in incomplete_power and error.startswith(name+': Actual power window incomplete: '):measurement_failures[name]=error
                elif name in tolerated:missing[name]=error
            allowed=set(missing)|set(measurement_failures)
            if not errors or any(error.split(': ',1)[0] not in allowed for error in errors) or not load(folder/'raw-source-index.json').get('complete'):
                raise RuntimeError('Required exact-hour archive/current capture incomplete; preserve private index and stop')
        manifest={'schema':'e5-private-collection.v1','visibility':'PRIVATE_OPERATIONAL_ONLY; exclude original raw and sensitive configuration from git/public evidence indexes','at':now(),'status':'CAPTURED_NOT_EVALUATED','raw':[],'observationCapture':str(folder/'raw-source-index.json'),'bindingSources':self.b['sources'],'physicalIdentities':'API resources and cluster, state Node references, full scheduler events','streams':['definitions','requests','management','scheduler','catalog','resources','cluster']}
        if missing:
            manifest.update(status='CAPTURED_WITH_MISSING_ARCHIVES',missingArchives=missing,archiveStatus=captured['status'])
        if measurement_failures:
            manifest.update(status='CAPTURED_WITH_INCOMPLETE_MEASUREMENTS',measurementFailures=measurement_failures,archiveStatus=captured['status'])
        for p in sorted(folder.rglob('*')):
            if p.is_file() and p.name!='collected.json': manifest['raw'].append(ref(p,allow_empty=True))
        save(folder/'collected.json',manifest)
    def failure_select(self):
        """Read-only selection; actual kubectl injection remains an operator action."""
        path=full_restore_path(self.out,self.a.stage,self.a.row_tag) if self.a.row_tag else None
        if self.a.mode!='full' or self.a.stage!='E5-7' or not path or not path.exists():raise ValueError('failure-select requires a restored targeted full E5-7 row with --row-tag')
        folder=row_folder(self.out,'full','E5-7',self.a.row_tag);phase=load(folder/'phase.json')
        if phase.get('status')!='RUNNING' or not phase.get('startUTC'):raise RuntimeError('Failure selection requires the actual running E5-7 offered window')
        path=folder/'failure-events.json'
        evidence=load(path) if path.exists() else {'stage':'E5-7','selectionPolicy':'active-serving-v1','status':'PENDING_OPERATOR','selections':[],'events':[]}
        if evidence.get('status')=='INVALID':raise RuntimeError('Failure selection already INVALID; no injection')
        selections=evidence['selections'];kind=self.a.failure_kind
        if kind!=('delete-serving-pod' if not selections else 'cordon-delete') or len(selections)>=2:raise ValueError('Select delete-serving-pod once, then cordon-delete once; no implicit repeat')
        prefix=folder/('failure-'+kind)
        try:
            maximum_age=failure_select_snapshot_ttl(self.c)
            settings=self.settings();state=self.state();snapshot=state['snapshot']
            end=dt.datetime.fromtimestamp(self.d.epoch(snapshot['at']),dt.timezone.utc)
            start=end-dt.timedelta(seconds=settings['observationWindowNanos']/1e9)
            window=self.api('GET','/api/v1/observations/load?'+urllib.parse.urlencode({'from':start.isoformat(),'to':snapshot['at']}))
            sources={}
            for label,body in (('state',state),('load',window)):
                source=Path(str(prefix)+'-'+label+'.json');save(source,body);sources[label+'Ref']=str(source)
            selected_at=now();at=dt.datetime.fromisoformat(selected_at.replace('Z','+00:00'))
            if not 0<=(at-end).total_seconds()<=maximum_age:raise ValueError('Latest scheduler snapshot is stale; injection cancelled')
            phase_start=dt.datetime.fromisoformat(phase['startUTC'].replace('Z','+00:00'))
            if not phase_start<=at<phase_start+dt.timedelta(seconds=phase['stage']['seconds']):raise ValueError('Outside actual offered window; injection cancelled')
            excluded=[selection['nodeUID'] for selection in selections]
            candidates=serving_failure_targets(state,window,self.uids,excluded)
            if len({target['nodeUID'] for target in candidates})<(1 if selections else 2):
                raise ValueError('No positive-route/assigned serving Pods on two distinct Nodes; injection cancelled')
            target=candidates[0]
            options=Path(str(prefix)+'-delete-options.json')
            save(options,{'apiVersion':'v1','kind':'DeleteOptions','preconditions':{'uid':target['podUID']}})
            kubectl=ENVIRONMENT['d2']['kubectl']
            delete=kubectl+['delete','--raw','/api/v1/namespaces/chill-system/pods/'+urllib.parse.quote(target['podName'],safe=''),'-f',str(options)]
            commands=([kubectl+['cordon',target['nodeName']]] if selections else [])+[delete]
            target.update(kind=kind,status='SELECTED',selectedAtUTC=selected_at,maximumAgeSeconds=maximum_age,expiresAtUTC=(end+dt.timedelta(seconds=maximum_age)).isoformat(),
                          allowedWorkloadUIDs=self.uids,deleteOptionsRef=str(options),deleteArgv=delete,
                          operatorArgv=commands,operatorCommands=[shlex.join(command) for command in commands],**sources)
            selections.append(target);save(path,evidence)
            print(json.dumps({'resultPath':str(path),'selection':target,'clusterMutations':0,
                              'operatorRequirement':'Execute immediately, before expiresAtUTC; record actual atUTC/exitCode/podUID/nodeUID/commandRef/responseRef in events. For cordon-delete commandRef must record deleteArgv plus actual cordon command/result. Uncordon same Node at recommended 600s; recovery comes from scheduler and cluster API records.'},ensure_ascii=False,indent=2))
        except Exception as error:
            evidence.update(status='INVALID',reason=str(error),invalidAtUTC=now())
            save(path,evidence)
            raise
    def factor(self):
        static=self.a.mode=='static'; full=self.a.mode=='full'
        if static:
            if not self.a.pre_state or not self.a.row_tag or self.a.stage!='E5-3':raise ValueError('Static factor restoration requires --pre-state, --row-tag and --stage E5-3')
            if (self.out/'static-replay-restore.json').exists():raise RuntimeError('Static restoration already attempted; no implicit repeat')
            f={'name':'static'}
        elif full:
            if not self.a.pre_state or not self.a.row_tag or self.a.stage not in ('E5-1','E5-5','E5-6','E5-7'):raise ValueError('Full restoration requires --pre-state, --row-tag and a targeted E5-1/5/6/7 row')
            if full_restore_path(self.out,self.a.stage,self.a.row_tag).exists():raise RuntimeError('Full row restoration already attempted; no implicit repeat')
            f={'name':'full'}
        else:
            f=next(f for f in self.c['factors'] if f['name']==self.a.mode)
            if self.a.stage not in ('E5-2','E5-5'): raise RuntimeError('Factor reset is one actual paired E5-2/E5-5 block')
        prefix='factor-'+f['name']+'-'+self.a.stage+('-'+self.a.row_tag if self.a.row_tag else '')
        if (static or full) and (self.out/(prefix+'-reset-request.json')).exists():raise RuntimeError('Restoration reset already attempted; preserve evidence, no implicit repeat')
        ack_wait=self.tb.get('factorAckWaitSeconds',120)
        if not positive(ack_wait):raise ValueError('factorAckWaitSeconds must be finite and greater than zero')
        desired=paired_baseline(self.a.pre_state or row_folder(self.out,'full',self.a.stage)/'pre-state.json',self.uids)
        if static and desired!=load(self.out/'static-selection.json')['fingerprint']:raise RuntimeError('Restoration input differs from original frozen selection')
        restore_settings=baseline_settings(self.a.pre_state) if static or full else None
        if full and set(restore_settings.get('autonomousWorkloads',[]))!=set(self.uids):raise ValueError('Original full pre-settings must enable canonical autonomy')
        if static or full:
            self.check_power_observation()
            self.check_token_lifetime()
            for frozen in self.b['sources'].values():
                if ref(frozen['path'])!=frozen:raise RuntimeError('Frozen input/package identity changed; no restoration')
        profiles=self.api('GET','/api/v1/scheduler/profiles')['preparations']
        for target in desired['instances']:
            default=profiles[target['profileID']]['instance']
            if any(target[key]!=default[key] for key in ('node','allocation','maxWaitNanos')): raise RuntimeError('Selected factor initial state needs unsupported manual wait/resource override; no generic reset is claimed')
        if static or full:self.disk_boundary(prefix,'before')
        self.clear_owned(prefix)
        current=self.state()
        for node in current['snapshot']['nodes']:
            if node['node']['name'] in desired['modes'] and node['powerMode']!=desired['modes'][node['node']['name']]: self.native_mode(node['node'],desired['modes'][node['node']['name']],prefix)
        for uid in self.uids:
            self.operation({'kind':'unroute','workloadUID':uid,'reason':'Keep paired factor preparations unloaded until exact baseline route ACK'},prefix+'-unroute-'+uid)
        mapping={}
        for target in desired['instances']:
            body=prepare_body(target);save(self.out/(prefix+'-prepare-'+target['node']['name']+'-request.json'),body)
            result=self.io.api('POST','/api/v1/scheduler/operations',body,timeout=self.tb['settleSeconds'])
            if result.get('status')!='complete': raise RuntimeError('Source-supported factor preparation incomplete')
            save(self.out/(prefix+'-prepare-'+target['node']['name']+'.json'),result)
            created=[i for i in result['actual']['instances'] if i.get('node')==target['node'] and i.get('ownerWorkloadUID')==target['workloadUID'] and i.get('profile')==target['profileID']]
            if len(created)!=1:raise RuntimeError('Native prepare completion lacks its exact Node/profile/owner physical identity')
            identifier=created[0]['id'];policy=result['actual']['native'][identifier]['metadata']['policy']
            need(policy,['profileID','maxBatchSize','maxBatchWaitMS'],'Actual completed native prepare policy')
            if policy['profileID']!=target['profileID'] or policy['maxBatchSize']!=target['maxBatch'] or round(policy['maxBatchWaitMS']*1e6)!=target['maxWaitNanos']:raise RuntimeError('Native prepared profile/cap/wait differs from exact paired target')
            deadline=time.monotonic()+ack_wait
            while True:
                state=self.state();barrier=self.start_barrier_observation(state,deadline,now())
                matches=[i for i in instance_rows(state['snapshot']['instances']) if i['id']==identifier]
                instance=matches[0] if len(matches)==1 else {}
                matched=instance.get('node')==target['node'] and instance.get('profile',{}).get('id')==target['profileID'] and instance.get('ownerWorkloadUID')==target['workloadUID'] and instance.get('workloadUID') in (None,'',target['workloadUID']) and all(instance.get(key)==target[key] for key in ('allocation','maxBatch','maxWaitNanos'))
                remaining=deadline-time.monotonic()
                accepted=barrier['snapshotFresh'] and matched and remaining>0
                if accepted or remaining<=0:
                    save(self.out/(prefix+'-prepare-'+identifier+'-snapshot-ack.json'),{'timeboxSeconds':ack_wait,'accepted':accepted,'target':target,'freshness':barrier,'state':state})
                    if not accepted:raise RuntimeError('Fresh prepared projection ACK timed out; final snapshot body retained with identity/cap/wait/allocation and freshness evidence')
                    mapping[target['id']]=identifier;break
                time.sleep(min(1,remaining))
        prepared=self.state();actual=prepared['snapshot']
        if {i['id'] for i in instance_rows(actual['instances'])}!=set(mapping.values()) or any(i.get('workloadUID') not in (None,'',i.get('ownerWorkloadUID')) for i in instance_rows(actual['instances'])):raise RuntimeError('Paired prepared state is not exactly the returned physical IDs with empty or owner served UID')
        # Prepared instances may already serve their owner; exact paired routing still requires ACK.
        chosen=owned_target(actual,self.uids,{uid:{mapping[old]:weight for old,weight in route.items()} for uid,route in desired['routing'].items()})
        save(self.out/(prefix+'-prepared-target.json'),{'status':'PREPARED_NOT_LOAD_READY','actualState':prepared,'intendedTargetBinding':chosen})
        routed=self.route_ack(chosen,copy.deepcopy(restore_settings) if static or full else self.settings(),prefix,'Restore actual paired initial block through public prepare and route operations')
        if static or full:
            expected=rebound_fingerprint(desired,mapping)
            observed=fingerprint(routed['snapshot'],self.uids)
            receipt={'at':now(),'accepted':observed==expected,'rowTag':self.a.row_tag,'label':'표적 재실행, 시작 상태 복원, 소스 R1~R6+R4b+C1+C2' if full else '태그 '+self.a.row_tag+' 정적 복원','preState':str(self.a.pre_state),'physicalIDMapping':mapping,'originalFingerprint':desired,'expectedFingerprint':expected,'fingerprint':observed,'comparison':'One post-restore exact comparison; only prepare-ACK physical IDs rebound'}
            if full:
                receipt.update(stage=self.a.stage,fullSettings=restore_settings,restorationExclusions=restoration_exclusions(self.a.pre_state,self.uids))
            save(full_restore_path(self.out,self.a.stage,self.a.row_tag) if full else self.out/'static-replay-restore.json',receipt)
            if not receipt['accepted']:raise RuntimeError('Restored fingerprint differs from original pre-state')
            self.disk_boundary(prefix,'after')
            return
        self.release_routing(prefix+'-release','Release paired baseline routing before autonomous factor')
        settings=self.settings(); settings['controls']=f['controls']; settings['autonomousWorkloads']=self.uids
        self.authorize_native_modes()
        save(self.out/(prefix+'-ack.json'),self.put(settings))
    def clear_owned(self,prefix):
        wait_seconds = self.tb.get('finishResetWaitSeconds', 600)
        if not positive(wait_seconds):
            raise ValueError('finishResetWaitSeconds must be finite and greater than zero')
        settings = self.settings()
        settings['autonomousWorkloads'] = [uid for uid in settings.get('autonomousWorkloads', []) if uid not in self.uids]
        ack = self.put(settings)
        save(self.out/(prefix+'-reset-autonomy-ack.json'), ack)
        off_at = ack.get('appliedAt') or ack.get('validatedAt') or now()
        started = time.monotonic()
        deadline = started + wait_seconds
        observations = []
        while True:
            state = self.state()
            barrier = self.start_barrier_observation(state, deadline, now())
            snapshot_at = self.d.epoch(barrier['snapshotAt'])
            fresh = barrier['snapshotFresh'] and snapshot_at >= self.d.epoch(off_at)
            reservations = barrier['reservations']
            transitions = barrier['transitions'] + sum(action.get('status') in ('running', 'cancel') for action in state.get('actions') or [])
            observations.append({'at': now(), 'elapsedSeconds': time.monotonic()-started, 'reservations': reservations, 'transitions': transitions, 'snapshotAt': barrier['snapshotAt'], 'latestActionEndedAt': barrier['latestActionEndedAt'], 'snapshotFresh': fresh})
            remaining = deadline - time.monotonic()
            clear = fresh and not reservations and not transitions
            if clear or remaining <= 0:
                timed_out = not clear or remaining < 0
                save(self.out/(prefix+'-reset-wait.json'), {'timeboxSeconds': wait_seconds, 'autonomyOffAtUTC': off_at, 'timedOut': timed_out, 'observations': observations, 'state': state, 'meaning': 'Autonomy off; fresh public snapshot and zero reservations/running-cancel entries required before one reset; timeout fails without reset or forced deletion'})
                if timed_out:
                    raise RuntimeError('Owned reset wait timed out; no fresh zero-reservation/action observation within bound; reset not attempted')
                break
            time.sleep(min(1, remaining))
        return self.operation({'kind':'reset','workloadUIDs':self.uids,'reason':'Approved campaign owned reset: '+prefix},prefix+'-reset')
    def native_mode(self,node,target,prefix):
        folder=self.out/(prefix+'-'+node['name']+'-mode'); request=folder/'release.json'
        save(request,{'executor':'CampaignOps','command':'node-native-mode','mainInstructionUTC':self.release['mainInstructionUTC'],'node':node,'targetMode':target,'maxActuationSeconds':900,'parentFrozenE5Release':str(self.a.release)})
        run=subprocess.run([sys.executable,str(D2/'native_mode_request.py'),'--release',str(request),'--out',str(folder)],timeout=960,capture_output=True,text=True)
        save(folder/'caller.json',{'at':now(),'exitCode':run.returncode,'stdout':run.stdout,'stderr':run.stderr})
        if run.returncode: raise RuntimeError('Existing finite native mode realization failed; no host power fallback')
    def cleanup_jobs(self,exclude=None):
        for p in self.out.glob('*/created-*.json'):
            if p.parent==exclude:continue
            if self.a.row_tag and not p.parent.name.endswith('-'+self.a.row_tag):continue
            j=load(p); name=j['metadata']['name']; uid=j['metadata']['uid']; phase=p.parent/'phase.json'
            stops=(load(phase).get('ownedProducerStops') or []) if phase.exists() else []
            if any(stop.get('name')==name and stop.get('uid')==uid and stop.get('stopped') is True for stop in stops):continue
            actual=self.io.k(['get','job',name,'-o','json'])
            if actual['metadata']['uid']!=uid: raise RuntimeError('Owned producer UID changed')
            self.io.remove_job(name,uid)
            if phase.exists():
                record=load(phase);record.setdefault('ownedProducerStops',[]).append({'name':name,'uid':uid,'stopped':True});save(phase,record)
    def factor_restore(self):
        settings=self.settings(); initial=load(self.c['inputs']['scheduler'])['settings']; settings['controls']=initial['controls']; save(self.out/'factor-restored.json',self.put(settings))
    def finish(self):
        prefix='finish'+('-'+self.a.row_tag if self.a.row_tag else '')
        if self.a.row_tag and (self.out/(prefix+'-complete.json')).exists():raise RuntimeError('Finish already recorded; no implicit repeat')
        if self.static_placement():
            save(self.out/(prefix+'-state.json'),self.state())
            save(self.out/(prefix+'-complete.json'),{'at':now(),'staticPlacement':True,'meaning':'Existing manual placement retained; no scheduler mutation'})
            return
        initial=load(self.out/'initial.json')
        result=self.clear_owned(prefix)
        settings=copy.deepcopy(initial['settings'])
        settings['autonomousWorkloads']=[uid for uid in settings.get('autonomousWorkloads',[]) if uid not in self.uids]
        save(self.out/(prefix+'-settings-ack.json'),self.put(settings))
        save(self.out/(prefix+'-state.json'),self.state())
        save(self.out/(prefix+'-complete.json'),{'at':now(),'canonicalInstances':0,'originalSettingsRestored':True,'resetStatus':result['status'],'powerReturn':'Server reset minimum supported idle mode; original physical modes are not recreated'})
    def campaign(self):
        self.start()
        def row(mode, name):
            self.a.mode=mode; self.a.stage=name; self.a.command='stage'
            self.run()
            result=judge(self.a.binding,self.out/(mode+'-'+name))
            save(self.out/'campaign-position.json',{'at':now(),'mode':mode,'lastCapturedRow':name,'judgment':result['status'],'approval':str(self.a.release),'scientificAccounting':'Recorded row verdict, not a continuation gate; incomplete energy remains UNKNOWN'})
            if result['status']=='INVALID_GENERATOR': raise RuntimeError('Generator invalid; frozen campaign stopped without advancing')
        for name in ('E5-0','E5-1','E5-2','E5-3'): row('full',name)
        for name in ('E5-1','E5-2','E5-3','E5-4','E5-5','E5-6'): row('static',name)
        # Keep the selected physical configuration fixed while E5-4 producers prepare; restore once at their release barrier.
        for name in ('E5-4','E5-5','E5-6','E5-7'): row('full',name)
        for f in self.c['factors']:
            self.a.mode=f['name']
            for name in ('E5-2','E5-5'):
                self.a.stage=name; self.factor(); row(f['name'],name); self.factor_restore()
        self.finish(); self.cleanup_jobs(); summarize(self.a.binding,self.out);collection_manifest(self.out,self.a.binding)


def reported_power(power):
    """Keep coverage failures, but never expose incomplete-window energy as measured."""
    value=dict(power or {})
    known=value.get('complete') is True and all(value.get(key) is not None for key in ('wholePoolJoules','activePoolJoules'))
    value['energyStatus']='MEASURED' if known else 'UNKNOWN'
    if not known:
        for key in ('wholePoolJoules','activePoolJoules','wholePoolMeanWatts','activePoolMeanWatts'):
            value[key]=None
    return value


def resource_telemetry(resources, clusters, scheduler):
    """Map API identities/units to the shared cumulative-counter reducer."""
    containers={}
    server_pods={item['row'].get('podUID') for item in resources if item['row'].get('kind')=='server'}
    native_ids={instance['id'] for event in scheduler for instance in instance_rows((event.get('snapshot') or {}).get('instances') or []) if instance.get('podUID') and instance.get('id','').startswith(instance['podUID']+'/')}
    for item in clusters:
        row=item['row']; pod=row.get('pod') or {}
        if row.get('event')=='server-start':server_pods.add(pod.get('uid'))
        for container in pod.get('containers') or []:
            containers[(pod.get('uid'),container.get('containerID'))]=container.get('name')
    cgroups=[]; thermals=[]
    for item in resources:
        row=item['row']
        if row.get('kind')!='node':continue
        node=row.get('node') or {}
        for sample in row.get('samples') or []:
            common={'at':sample.get('sampledAt'),'node':node.get('name'),'nodeUID':node.get('uid'),
                    'receivedAt':row.get('receivedAt'),'sourceRef':item['sourceRef']}
            for container in sample.get('containers') or []:
                name=containers.get((container.get('podUID'),container.get('containerID')))
                identity=(container.get('podUID') or '')+'/'+(container.get('containerID') or '')
                identified=bool(node.get('uid') and container.get('podUID') and container.get('containerID'))
                role='unknown' if not identified else 'server' if container['podUID'] in server_pods else 'native' if identity in native_ids else 'edgemesh' if name=='edgemesh' else 'unknown'
                cgroups.append({**container,**common,'kind':'cgroup','role':role,
                                'nrThrottled':container.get('throttledPeriods'),'memoryBytes':container.get('memoryCurrentBytes')})
            for sensor in sample.get('thermal') or []:
                value=sensor.get('milliCelsius'); temperature=value/1000 if value is not None else None
                thermals.append({**common,'zone':{'path':sensor.get('sensor'),'millidegreesC':value},
                    'bin':'unknown' if temperature is None else '<60' if temperature<60 else '60-70' if temperature<70 else '70-80' if temperature<80 else '>=80',
                    'active':None,'latched':None})
    return cgroups,thermals


def runtime_gc(resources, start, end, d):
    """Keep process generations separate; cumulative counters are not cycle distributions."""
    grouped=defaultdict(list)
    for item in resources:
        row=item['row']; at=d.epoch(row.get('at'))
        if row.get('kind')=='server' and at is not None:
            grouped[(row.get('podUID'),row.get('processStartedAt'))].append((at,item))
    series=[]
    for (pod,process),items in grouped.items():
        items.sort(key=lambda item:item[0])
        if items[-1][0]<start or items[0][0]>end:continue
        before=[item for item in items if item[0]<=start];after=[item for item in items if item[0]>=end]
        left=before[-1] if before else items[0];right=after[0] if after else items[-1]
        selected=[item for at,item in items if left[0]<=at<=right[0]]
        deltas={};resets=[]
        for key in ('gcCPUSeconds','gcCycles'):
            values=[d.number(item['row'].get(key)) for item in selected]
            reset=any(a is not None and b is not None and b<a for a,b in zip(values,values[1:]))
            if reset:resets.append(key)
            deltas[key]=values[-1]-values[0] if len(values)>1 and all(value is not None for value in values) and not reset else None
        series.append({'podUID':pod,'processStartedAt':process,'firstEpoch':left[0],'lastEpoch':right[0],
            'bracketsWindow':bool(pod and process and before and after),'boundaryOverhangSeconds':{'before':max(0,start-left[0]),'after':max(0,right[0]-end)},
            'cpuSecondsDelta':deltas['gcCPUSeconds'],'cyclesDelta':deltas['gcCycles'],'counterResets':resets,'records':selected})
    complete=len(series)==1 and series[0]['bracketsWindow'] and all(series[0][key] is not None for key in ('cpuSecondsDelta','cyclesDelta'))
    return {'meaning':'runtime-metrics-delta','complete':complete,'cpuSecondsDelta':series[0]['cpuSecondsDelta'] if complete else None,
        'cyclesDelta':series[0]['cyclesDelta'] if complete else None,'capturedTotalCPUMS':series[0]['cpuSecondsDelta']*1000 if complete else None,'series':series,
        'scope':'Window cumulative runtime/metrics delta between nearest real boundary samples; overhang retained, no interpolation. Process restarts/resets and missing brackets remain unknown; no GC-cycle distribution.'}


def failure_api_recovery(commands, scheduler, clusters, end, d):
    """Require a ready positive route and matching recorded Ready Pod after uncordon."""
    node_uid=commands[2]['nodeUID'];uncordon=d.epoch(commands[2]['atUTC'])
    candidates=[]
    for event in scheduler:
        snapshot=event.get('snapshot') or {};at=d.epoch(snapshot.get('at'))
        if at is None or not uncordon<=at<=end:continue
        ready_nodes=[node for node in snapshot.get('nodes') or [] if (node.get('node') or {}).get('uid')==node_uid and node.get('ready')]
        if not ready_nodes:continue
        for instance in instance_rows(snapshot.get('instances') or []):
            uid=instance.get('workloadUID')
            if (instance.get('node') or {}).get('uid')!=node_uid or not instance.get('ready') or instance.get('stopped') or not uid or (snapshot.get('routing',{}).get(uid) or {}).get(instance.get('id'),0)<=0:continue
            pods=[item for item in clusters if (item['row'].get('pod') or {}).get('uid')==instance.get('podUID') and d.epoch(item['row'].get('at')) is not None and d.epoch(item['row']['at'])<=at]
            latest=max(pods,key=lambda item:d.epoch(item['row']['at'])) if pods else None
            pod=(latest['row'].get('pod') or {}) if latest else {}
            if latest and latest['row'].get('event')!='deleted' and not pod.get('deletionTimestamp') and pod.get('ready')=='True' and pod.get('node')==ready_nodes[0]['node']['name']:
                candidates.append({'atUTC':snapshot['at'],'nodeUID':node_uid,'instance':instance,'pod':pod,
                                   'schedulerRefs':event.get('_rawRefs',[]),'clusterRef':latest['sourceRef']})
    observed=min(candidates,key=lambda item:d.epoch(item['atUTC'])) if candidates else None
    return {'status':'OBSERVED' if observed else 'UNMEASURED','observation':observed,
        'evidenceSource':['scheduler','cluster'],'meaning':'CHILL-recorded ready Node, ready Pod and positive serving route after operator uncordon; cordon/schedulability itself is not recorded. No operator readback or inferred physical recovery.'}


def judge(binding, folder):
    b=load_binding(binding); c=b['config']; phase=load(folder/'phase.json'); st=phase['stage']; d=module('d2analyze',D2/'analyze_before.py'); inp=d.Inputs(folder); unknown=[]; results={}
    raw=module('e5raw',HERE/'e5-raw.py'); events,paging=raw.scheduler_events(folder)
    events=[e for e in events if d.epoch(e.get('at')) is not None and d.epoch(phase['startUTC'])<=d.epoch(e['at'])<=d.epoch(phase['drainedUTC'])]
    resources,resource_capture=raw.stream_events(folder,'resources')
    clusters,cluster_capture=raw.stream_events(folder,'cluster')
    resource_rows,thermals=resource_telemetry(resources,clusters,events)
    for name,capture in (('scheduler',paging),('resources',resource_capture),('cluster',cluster_capture)):
        unknown.extend(name+': '+str(error) for error in capture['errors'])
    sched=d.scheduler_summary(events); searches=[]; final_indexes={}
    tick_cpu=[]; samples=[row for row in resource_rows if row.get('role')=='server']
    for e in events:
        decision=e.get('decision') or {}; at=d.epoch(e.get('at')); elapsed=d.number(decision.get('evaluationElapsedNanos'))
        at=d.epoch(decision.get('at'))
        if at is not None and elapsed is not None:
            finish=at+elapsed/1e9
            if (decision.get('capacity') or {}).get('search'): searches.append((at,finish))
            prior=[r for r in samples if d.epoch(r['at']) is not None and d.epoch(r['at'])<=at]
            after=[r for r in samples if d.epoch(r['at']) is not None and d.epoch(r['at'])>=finish]
            left=max(prior,key=lambda r:d.epoch(r['at'])) if prior else None; right=min(after,key=lambda r:d.epoch(r['at'])) if after else None
            same=left and right and all(left.get(key)==right.get(key) and left.get(key) for key in ('nodeUID','podUID','containerID')) and left.get('cpuUsageNanos') is not None and right.get('cpuUsageNanos') is not None and right['cpuUsageNanos']>=left['cpuUsageNanos']
            tick_cpu.append({'at':decision.get('at'),'evaluationElapsedNS':elapsed,'bracketingStart':left,'bracketingEnd':right,'cpuUsageDeltaNS':right['cpuUsageNanos']-left['cpuUsageNanos'] if same and left.get('cpuUsageNanos') is not None and right.get('cpuUsageNanos') is not None else None,'meaning':'1s sample brackets nominal Tick start through evaluation duration; includes outside-Tick CPU. Raw counter/reset identity retained; not exclusive search CPU.'})
    starts=[]; prefix_ends=[]; furthest=-math.inf
    for lo,hi in sorted(searches):
        starts.append(lo);furthest=max(furthest,hi);prefix_ends.append(furthest)
    def during_search(at):
        if at is None:return False
        index=bisect.bisect_right(starts,at)-1
        return index>=0 and at<prefix_ends[index]
    clients={}
    for w in c['workloads']:
        path=folder/(w['name']+'-client.jsonl');clients[w['name']]=[x for x in inp.jsonlines(path) if 'scheduledOffsetNs' in x] if path.exists() else []
    received_indexes=raw.iter_request_indexes(folder,[w['uid'] for w in c['workloads']],phase['startUTC'],phase['endUTC'])
    indexes=raw.iter_request_indexes(folder,[w['uid'] for w in c['workloads']],phase['startUTC'],phase['endUTC'],actual_response_ids={w['uid']:[x['requestID'] for x in clients[w['name']] if x.get('requestID')] for w in c['workloads']})
    for w in c['workloads']:
        indexed=indexes[w['uid']]; summary=indexed['summary']; finals=indexed['finals']; pages={'rawCaptureComplete':not indexed['errors'],'sources':indexed['sources']}
        final_indexes[w['name']]=finals
        unknown.extend(w['name']+': '+str(error) for error in indexed['errors'])
        client=clients[w['name']]
        offered=st['traces'][w['name']]['offered']; completed=[]; gateway_completed=[]; residual=[]; tails={'search':[],'outsideSearch':[]}; minutes=defaultdict(lambda:{'offered':0,'deadlineSuccess':0,'outcomes':Counter()}); success=0; used=set(); native_missing=[]; observed_outcomes=Counter();unsent=0
        start_ns=phase['starts'].get(w['name'],{}).get('monotonicNS')
        for index,offset in enumerate(st['traces'][w['name']]['offsetsNS']): minutes[offset//60000000000]['offered']+=1
        for x in client:
            key=x.get('requestID'); row=finals.get(key); minute=x['scheduledOffsetNs']//60000000000
            if key:used.add(key)
            no_send=x.get('issued') is False or x.get('error')=='UnsentAtWindowEnd';unsent+=int(no_send)
            outcome=row.get('outcome') if row else 'unsent-at-window-end' if no_send else 'unfinished-or-no-final'; minutes[minute]['outcomes'][outcome]+=1;observed_outcomes[outcome]+=1
            if not no_send and row and row.get('outcome')=='completed' and row.get('executionComplete') is True and type(row.get('elapsedNanos')) is int and row['elapsedNanos']>=0:
                elapsed=row['elapsedNanos']; gateway_completed.append(elapsed)
                completed.append(elapsed)
                if elapsed<=w['slo']['targetLatencyNanos']: success+=1; minutes[minute]['deadlineSuccess']+=1
                if row.get('nativeElapsedMS') is not None and row.get('dispatchedNanos') is not None: residual.append(elapsed-row['dispatchedNanos']-row['nativeElapsedMS']*1e6)
                else: native_missing.append(key)
                if row.get('nativeElapsedMS') is not None:
                    tail=elapsed-row['nativeElapsedMS']*1e6; at=d.epoch(row['receivedAt']); tails['search' if during_search(at) else 'outsideSearch'].append(tail)
        unfinished=max(0,offered-len(client))+unsent+sum(not finals.get(x.get('requestID')) and x.get('issued') is not False and x.get('error')!='UnsentAtWindowEnd' for x in client); pct=w['slo']['percentile']; rank=math.ceil(offered*pct/100); deadline_rate=success/offered if offered else None
        normal=not st['id'].startswith('E5-4')
        results[w['name']]={'offered':offered,'clientRows':len(client),'unfinishedOrUnsent':unfinished,'outcomes':summary['outcomes'],'completedLatencyNS':d.stats(completed),'completionResidualNS':d.stats(residual),'marginNS':c['mSeconds']*1e9,'gatewayTailNS':{k:d.stats(v) for k,v in tails.items()},'deadlineSuccess':success,'deadlineSuccessFraction':deadline_rate,'requiredSuccessfulRank':rank,'D1':'UNMEASURED' if offered==0 else 'FAIL' if normal and success<rank else 'OBSERVED_THRESHOLD_MET' if normal else 'OVERLOAD_DESCRIPTIVE','minutes':dict(minutes),'nativeResidualMissingIDs':native_missing,'rawPopulation':received_indexes[w['uid']]['summary'],'offeredResponsePopulation':summary,'pages':pages}
        timings=[d.request_timing(x,finals.get(x.get('requestID')),indexed.get('receivedAtNS',{}).get(x.get('requestID')),start_ns) for x in client]
        results[w['name']].update(gatewayCompletedLatencyNS=d.stats(gateway_completed),latencyScope='Gateway elapsedNanos: CHILL receipt to response, per 03_routing; scheduled/client round trip are reference only',scheduledLatencyNS=d.stats([t['scheduledLatencyNS'] for t in timings]),entranceDelayNS=d.stats([t['entranceDelayNS'] for t in timings]),entranceDelayMeaning='receivedAt minus client sentAtUnixNS; cross-host clock uncertainty retained, not clamped or interpreted as pure network delay',actualLatencyNS=d.stats([t['actualLatencyNS'] for t in timings]))
        results[w['name']]['outcomes']=dict(observed_outcomes);results[w['name']]['gatewayFinalOutcomes']=received_indexes[w['uid']]['summary']['outcomes'];results[w['name']]['offeredGatewayFinalOutcomes']=summary['outcomes'];results[w['name']]['unsentAtWindowEnd']=unsent;results[w['name']]['actualGatewayIdentities']=len(used)
        results[w['name']]['issuanceFidelity']=d.issuance_fidelity(client,st['traces'][w['name']]['offsetsNS'],phase['starts'].get(w['name'],{}).get('monotonicNS'),round(st['seconds']*1e9),d.stats,d.generator_quality_config(c))
        quality=results[w['name']]['issuanceFidelity']['generatorQuality']['status']
        if offered and quality=='UNKNOWN':unknown.append(w['name']+': generator quality unknown')
        if offered and quality!='VALID':results[w['name']]['D1']='INVALID_GENERATOR' if quality=='INVALID_GENERATOR' else 'UNKNOWN'
        sorted_residual=sorted(residual)
        results[w['name']]['selectedResidualPercentile']={'percentile':pct,'nanoseconds':sorted_residual[max(0,math.ceil(len(sorted_residual)*pct/100)-1)] if sorted_residual else None}
        for minute, values in results[w['name']]['minutes'].items():
            at=d.epoch(phase['starts'].get(w['name'],{}).get('at') or phase['startUTC'])
            values['searchObserved']=any(lo<at+(int(minute)+1)*60 and hi>=at+int(minute)*60 for lo,hi in searches)
        sub=[]; base=0
        for part in st['parts']:
            stop=base+round(part['seconds']*1e9); offsets=[x for x in st['traces'][w['name']]['offsetsNS'] if base<=x<stop]
            selected=[x for x in client if base<=x['scheduledOffsetNs']<stop]; good=[]; outcomes=Counter()
            for x in selected:
                r=finals.get(x.get('requestID')); outcomes[r.get('outcome') if r else 'unsent-at-window-end' if x.get('issued') is False or x.get('error')=='UnsentAtWindowEnd' else 'unfinished-or-no-final']+=1
                if x.get('issued') is not False and x.get('error')!='UnsentAtWindowEnd' and r and r.get('outcome')=='completed' and r.get('executionComplete') is True and type(r.get('elapsedNanos')) is int and r['elapsedNanos']>=0:
                    good.append(r['elapsedNanos'])
            hits=sum(x<=w['slo']['targetLatencyNanos'] for x in good); required=math.ceil(len(offsets)*pct/100)
            sub.append({'id':part['id'],'offered':len(offsets),'outcomes':dict(outcomes),'unfinishedOrUnsent':max(0,len(offsets)-len(selected)),'completedLatencyNS':d.stats(good),'deadlineSuccess':hits,'requiredSuccessfulRank':required,'D1':'UNMEASURED' if not offsets else 'FAIL' if normal and hits<required else 'OBSERVED_THRESHOLD_MET' if normal else 'OVERLOAD_DESCRIPTIVE','rate':part['rates'][w['name']],'intendedRate':part.get('intendedRates',{}).get(w['name']),'poolShares':part.get('poolShares'),'alpha':part.get('alpha')}); base=stop
            sub[-1]['latencyScope']=results[w['name']]['latencyScope']
            if offsets and quality!='VALID':sub[-1]['D1']='INVALID_GENERATOR' if quality=='INVALID_GENERATOR' else 'UNKNOWN'
            sub[-1]['unfinishedOrUnsent']+=sum(not finals.get(x.get('requestID')) for x in selected)
        results[w['name']]['substages']=sub
    start,end=d.epoch(phase['startUTC']),d.epoch(phase['endUTC']); cg=d.telemetry(inp,start,end,resource_rows=resource_rows)
    stored_power={'power':inp.json(folder/'power.json'),'powerDrainInclusive':inp.json(folder/'power-drain-inclusive.json')}
    available={name:value for name,value in stored_power.items() if value and value.get('from') and value.get('to')}
    energy=module('e5energy',HERE/'e5-archive.py').e5_power_windows(folder,c['collection'],available) if available else {}
    power=reported_power(energy.get('power',stored_power['power']))
    bootstrap=None
    if st['id']=='E5-0':
        post=inp.json(folder/'post-state.json')
        bootstrap={w['name']:[i for i in instance_rows((post or {}).get('snapshot',{}).get('instances',[])) if i.get('workloadUID')==w['uid'] and i.get('ready')] for w in c['workloads']}
    if not power or not power.get('complete') or power.get('wholePoolJoules') is None or power.get('activePoolJoules') is None: unknown.append('Whole/active POOL transition-inclusive power incomplete')
    tail_power=reported_power(energy.get('powerDrainInclusive',stored_power['powerDrainInclusive']))
    if not tail_power or not tail_power.get('complete') or tail_power.get('wholePoolJoules') is None or tail_power.get('activePoolJoules') is None: unknown.append('Actual transition/request/action drain-inclusive energy incomplete')
    profile_dump=load(c['inputs']['scheduler']); profile_snap=profile_dump['state']['snapshot'] if 'state' in profile_dump else profile_dump['snapshot']; profiles={}
    for option in profile_snap['options']:
        profile=option['instance']['profile']
        if '$ref' in profile: profile=profile_dump['profiles'][profile['$ref']]
        profiles[profile['id']]=profile
    thermal_by_node=defaultdict(dict)
    for observation in thermals:
        if d.epoch(observation['at']) is not None:
            zone=observation['zone']['path'];thermal_by_node[observation['node']].setdefault(zone,[]).append((d.epoch(observation['at']),observation))
    for zones in thermal_by_node.values():
        for zone,items in zones.items():
            ordered=sorted(items,key=lambda item:item[0]);zones[zone]=([at for at,item in ordered],[item for at,item in ordered])
    thermal_slack=[]; grouped=defaultdict(list)
    for name,result in results.items():
        w=next(w for w in c['workloads'] if w['name']==name)
        for row in final_indexes[name].values():
            if row.get('node',{}).get('name') not in E5['thermalSlackNodes'] or not positive(row.get('serviceMS')): continue
            at=d.epoch(row['receivedAt'])
            for zone,(epochs,observations) in thermal_by_node[row['node']['name']].items():
                index=bisect.bisect_right(epochs,at)-1;latest=observations[index] if index>=0 else None
                key=(name,row['profileID'],row['batchSize'],zone,latest['bin'] if latest else 'unknown')
                grouped[key].append((row['serviceMS']*1e6,at-epochs[index] if latest else None))
    for (name,pid,batch,zone,bin_name),items in grouped.items():
        w=next(w for w in c['workloads'] if w['name']==name); profile=profiles.get(pid); measured=next((x for x in profile['batches'] if x['batch']==batch),None) if profile else None
        values=sorted(measured['durationNanos']) if measured else []; index=max(0,math.ceil(len(values)*w['slo']['percentile']/100)-1); reference=values[index] if values else None
        observed=sorted(x[0] for x in items); quantile=observed[max(0,math.ceil(len(observed)*w['slo']['percentile']/100)-1)]
        thermal_slack.append({'workload':name,'profileID':pid,'batch':batch,'sensorPath':zone,'sampledPriorBin':bin_name,'profileQuantileNS':reference,'observedNativeServiceQuantileNS':quantile,'profileTimingDeltaNS':quantile-reference if reference is not None else None,'serviceOnlySLOSlackNS':w['slo']['targetLatencyNanos']-c['mSeconds']*1e9-quantile,'samples':len(items),'sampledThermalAgeSeconds':d.stats([x[1] for x in items]),'rawSources':results[name]['pages']['sources'],'meaning':'Separate descriptive view for each real sensor path; never sum sensor-group sample counts. Prior sampled zone temperature/age retained; no temperature causality or end-to-end feasibility claim.'})
    failure_capture = None
    if st['id']=='E5-7':
        try:
            receipt_path = folder/'failure-events.json'
            evidence = load(receipt_path)
            if (folder/'start-state-restoration.json').exists() and evidence.get('selectionPolicy')!='active-serving-v1':raise ValueError('INVALID: targeted row requires active-serving selection evidence')
            validate_failure_events(evidence,phase['startUTC'],phase['endUTC'],d.epoch)
            references = [ref(receipt_path)]
            for event in evidence['events']:
                references.extend(ref(event[key]) for key in ('commandRef','responseRef'))
            for selection in evidence.get('selections',[]):
                references.extend(ref(selection[key]) for key in ('stateRef','loadRef','deleteOptionsRef'))
            recovery=failure_api_recovery(evidence['events'],events,clusters,end,d)
            failure_capture = {'status':'CAPTURED','events':evidence['events'],'selections':evidence.get('selections'),'recovery':recovery,'sources':references,
                               'recoverySources':{'scheduler':paging,'cluster':cluster_capture},
                               'meaning':'Actual operator commands and API target selection; recovery is CHILL scheduler/cluster evidence, not operator readback. Failed commands and unrecovered state retained. Serving loss/cordon proxy, not physical power/network loss.'}
        except (OSError,ValueError,KeyError,TypeError) as exc:
            failure_capture={'status':'INVALID' if str(exc).startswith('INVALID:') else 'UNMEASURED','reason':str(exc),'sourcePath':str(folder/'failure-events.json')}
            failure_capture['recoverySources']={'scheduler':paging,'cluster':cluster_capture}
    result={'schema':'e5-judgment.v1','visibility':'PRIVATE_OPERATIONAL_ONLY; raw references/configuration excluded from public indexes','at':now(),'status':'NOT_READY' if unknown else 'FAIL' if any(r['D1']=='FAIL' for r in results.values()) else 'MEASURED_DESCRIPTIVE','notReadyReasons':unknown,'requests':results,'Tick':sched,'cgroups':cg,'power':power,'thermal':thermals,'D6':'Observed thermal bins only; unknown active/latched remains unknown, profile slack must use bound source samples','rawErrors':inp.errors,'sources':inp.sources,'limitations':b['limitations'],'accounting':{'specScenarioDenominator':7,'planRowDenominator':9,'failureScenario':failure_capture['status'] if failure_capture else 'NOT_EXERCISED_IN_THIS_ROW'}}
    malformed=[error for error in inp.errors if error.get('line') is not None and Path(error['path']).name in {w['name']+'-client.jsonl' for w in c['workloads']}]
    result['coverage']={'malformedLines':len(malformed),'firstMalformedLine':malformed[0] if malformed else None}
    result['failureScenarioEvidence']=failure_capture
    if failure_capture and failure_capture['status']=='INVALID':result['status']='INVALID'
    restoration=folder/'start-state-restoration.json'
    result['startStateRestoration']=load(restoration) if restoration.exists() else None
    result['energyBelowResolution']={'profiles':[{'profileID':profile['id'],'batch':batch['batch'],
        'energyPerInputJoules':batch.get('energyPerInputJoules'),'energyBelowResolution':True}
        for profile in profiles.values() for batch in profile['batches'] if batch.get('energyBelowResolution') is True],
        'source':ref(c['inputs']['scheduler']),
        'scope':'Frozen scheduler profiles, including ineligible candidates; not evidence of actual use or measured whole-POOL savings. Zero is below resolution, not exact zero energy.'}
    if phase.get('mode')=='smoke':
        result['scientificResults']='UNMEASURED; operational smoke never fills E5 rows'
        if result['status']=='MEASURED_DESCRIPTIVE': result['status']='OPERATIONAL_SMOKE_COMPLETE'
    result['TickCPUBrackets']=tick_cpu
    result['clusterObservations']={'records':clusters,'capture':cluster_capture,'meaning':'API Pod identity, readiness, restart/termination/imageID and observer issue transitions; server logs and kubectl receipts are not judgment inputs.'}
    result['canonicalFirstInstances']=bootstrap
    result['D6ProfileSlack']=thermal_slack
    result['searchWindows']={'intervals':searches,'meaning':'Nominal source decision.at through monotonic evaluationElapsed mapped onto wall time; clock/capture offset is retained as uncertainty, not exact CPU attribution'}
    result['GC']=runtime_gc(resources,start,end,d)
    decisions=[x['decision'] for x in sched['records']]
    result['intendedOverload']={'requested':st['id']=='E5-4','sourceOverloadDecisionObserved':any(w.get('state')=='Overload' for decision in decisions for w in decision.get('workloads',[])),'actualUnsent':sum(r['unsentAtWindowEnd'] for r in results.values()),'meaning':'Report actual source judgment and unissued offered arrivals explicitly; intended alpha=1.2 (rate=alpha*share*R) is not proof that all overload reached the gateway.'}
    result['searchBudget']={'budgetCutDecisions':sum(bool(((x.get('capacity') or {}).get('search') or {}).get('budgetCut')) for x in decisions),'gainWithinUncertaintyDecisions':sum('TransitionGainWithinUncertainty' in json.dumps(x) for x in decisions),'rawSearch':[(x.get('capacity') or {}).get('search') for x in decisions]}
    result['observationDirectoryUsage']={'records':[item for item in resources if item['row'].get('directoryObservedAt') or any(error.get('field')=='observationDirectoryBytes' for error in item['row'].get('errors') or [])],
                                         'meaning':'Server-observed directory bytes at directoryObservedAt, sampled once per minute; not whole PVC usage.'}
    result['powerDrainInclusive']=tail_power
    result['schedulerCapture']=paging
    result['resourceCapture']=resource_capture
    result['resourceObservations']=resources
    result['resourceRoleScope']='Server: all node-agent sampled containers in the process-identified server Pod (current chart has one); native: exact scheduler physical podUID/containerID. Other or missing identities stay separate/unknown. SampledAt is the Node clock and receivedAt the server clock; neither is corrected.'
    result['phaseStatus']=phase['status']
    result['phaseError']=phase.get('error')
    result['schedulerBoundaryObservations']=phase.get('schedulerBoundaryObservations')
    result['drainScope']=phase.get('drainScope')
    result['judgmentCriteria']=['SLO','energy','generatorQuality','powerCompleteness']
    if any(r['issuanceFidelity']['generatorQuality']['status']=='INVALID_GENERATOR' for r in results.values()):
        result['status']='INVALID_GENERATOR'
    save(folder/'judgment.json',result); return result


def summarize(binding, root, tag=None):
    b=load_binding(binding); stages={}; missing=[]
    modes={'full':[x['id'] for x in b['schedule']], 'static':[x['id'] for x in b['schedule'] if x['id'] not in ('E5-0','E5-7')]}
    modes.update({f['name']:['E5-2','E5-5'] for f in b['config']['factors']})
    targeted={}
    for row in ('E5-1','E5-5','E5-6','E5-7'):
        path=full_restore_path(root,row,tag)
        if path and path.exists():targeted[row]=load(path)
    tags={record['rowTag'] for record in targeted.values()}
    if len(tags)>1:raise ValueError('Multiple targeted restoration tags; specify --row-tag')
    selected_tag=next(iter(tags),None)
    folders={(mode,row):row_folder(root,mode,row,selected_tag if mode=='full' and row in targeted else None) for mode,ids in modes.items() for row in ids}
    for mode, ids in modes.items():
        for row_id in ids:
            folder=folders[(mode,row_id)]
            try:
                value=load(folder/'judgment.json')
                if value.get('scientificResults','').startswith('UNMEASURED'): raise ValueError('Operational smoke is not a scientific row')
                value['power']=reported_power(value.get('power'))
                stages[(mode,row_id)]=value
            except (OSError,ValueError,KeyError) as exc: missing.append({'mode':mode,'row':row_id,'reason':str(exc)})
    comparisons=[]
    for row_id in modes['static']:
        full=stages.get(('full',row_id)); static=stages.get(('static',row_id))
        if full and static:
            f=load(folders[('full',row_id)]/'phase.json'); s=load(folders[('static',row_id)]/'phase.json')
            if f['stage']['traces'] != s['stage']['traces']: missing.append({'row':row_id,'reason':'Paired frozen traces differ'}); continue
            full_j,static_j=full['power']['wholePoolJoules'],static['power']['wholePoolJoules']
            comparisons.append({'row':row_id,'judgments':{'full':full['status'],'static':static['status']},
                'energyStatus':'MEASURED' if full_j is not None and static_j is not None else 'UNKNOWN',
                'fullWholePOOLJ':full_j,'staticWholePOOLJ':static_j,
                'staticMinusFullWholePOOLJ':static_j-full_j if full_j is not None and static_j is not None else None,
                'fullActivePOOLJ':full['power']['activePoolJoules'],'staticActivePOOLJ':static['power']['activePoolJoules'],
                'requestMetrics':{'full':full.get('requests',{}),'static':static.get('requests',{})}})
            full_tail=reported_power(full.get('powerDrainInclusive') or load(folders[('full',row_id)]/'power-drain-inclusive.json'))
            static_tail=reported_power(static.get('powerDrainInclusive') or load(folders[('static',row_id)]/'power-drain-inclusive.json'))
            full_bound,static_bound=full['power'].get('gapErrorBoundJoules'),static['power'].get('gapErrorBoundJoules')
            comparisons[-1]['energyGapError']={'fullBoundJoules':full_bound,'staticBoundJoules':static_bound,
                'differenceBoundJoules':full_bound+static_bound if full_bound is not None and static_bound is not None else None,
                'fullCoverage':full['power'].get('energyCoverage'),'staticCoverage':static['power'].get('energyCoverage')}
            comparisons[-1]['drainInclusive']={'full':full_tail,'static':static_tail,'meaning':'Separate actual phase.drainedUTC horizon; inspect phase drainScope. Scheduler transitions/reservations are not awaited; nominal170min totals retain offered windows.'}
    plan_rows={row_id:stages[('full',row_id)]['status'] if ('full',row_id) in stages else 'UNMEASURED' for row_id in modes['full']}
    plan_rows['E5-S']=('MEASURED_DESCRIPTIVE' if all(pair['energyStatus']=='MEASURED' for pair in comparisons) else 'INCOMPLETE') if len(comparisons)==6 else 'UNMEASURED'
    spec={'steady':'E5-1','gradual-up-and-down':'E5-3','burst':'E5-2','post-transition-drop-resurge':'E5-5','mixed-heterogeneous':'E5-1','pool-overload':'E5-4'}
    scenario={name:plan_rows[row] if plan_rows[row]!='UNMEASURED' and (name!='gradual-up-and-down' or plan_rows['E5-5']!='UNMEASURED') else 'UNMEASURED' for name,row in spec.items()}
    failure = stages.get(('full','E5-7'),{}).get('failureScenarioEvidence')
    scenario['node-serving-control-failure']=plan_rows['E5-7'] if failure and failure.get('status')=='CAPTURED' else 'UNMEASURED'
    factor_results={f['name']:{row:stages.get((f['name'],row)) for row in ('E5-2','E5-5')} for f in b['config']['factors']}
    row_statuses={mode+'-'+row:value['status'] for (mode,row),value in stages.items()}
    incomplete=any(value['status'] in ('NOT_READY','INCOMPLETE','UNKNOWN') or value['power']['energyStatus']=='UNKNOWN' for value in stages.values())
    status='INVALID_GENERATOR' if 'INVALID_GENERATOR' in row_statuses.values() else 'INVALID' if 'INVALID' in row_statuses.values() else 'NOT_READY' if missing else 'INCOMPLETE' if incomplete else 'MEASURED_DESCRIPTIVE'
    full_values=[pair['fullWholePOOLJ'] for pair in comparisons]; static_values=[pair['staticWholePOOLJ'] for pair in comparisons]
    result={'schema':'e5-summary.v1','at':now(),'status':status,'missing':missing,'planRows':plan_rows,'planResultLinks':str(sum(x!='UNMEASURED' for x in plan_rows.values()))+'/9','specScenarios':scenario,'specResultLinks':str(sum(x!='UNMEASURED' for x in scenario.values()))+'/7','failureScope':'E5-7 manual serving loss/cordon proxy; actual event/recovery evidence required, not physical network/power loss','failureScenarioEvidence':failure,'pairedComparisons':comparisons,'fullMatched170minWholePOOLJ':sum(full_values) if len(comparisons)==6 and all(value is not None for value in full_values) else None,'static170minWholePOOLJ':sum(static_values) if len(comparisons)==6 and all(value is not None for value in static_values) else None,'controlFactors':factor_results,'limitations':b['limitations'],'staticSelection':str(root/'static-selection.json'),'accounting':{'d':b['accounting']['d'],'exactCost':b['accounting']['exactCost'],'evaluation':str(sum(x!='UNMEASURED' for x in scenario.values()))+'/7'}}
    result['rowJudgments']=row_statuses
    full_bounds=[pair['energyGapError']['fullBoundJoules'] for pair in comparisons]
    static_bounds=[pair['energyGapError']['staticBoundJoules'] for pair in comparisons]
    result['energyGapErrorBoundJoules']={
        'fullMatched170min':sum(full_bounds) if len(comparisons)==6 and all(value is not None for value in full_bounds) else None,
        'static170min':sum(static_bounds) if len(comparisons)==6 and all(value is not None for value in static_bounds) else None}
    result['scenarioLimitations']={'gradual-up-and-down':'E5-3 increases and E5-5 abrupt drop only; gradual decrease unmeasured',
        'burst':'Smol burst only; other-model bursts unmeasured',
        'node-serving-control-failure':'Manual serving loss/cordon proxy only; physical network/power loss and all control failure types unmeasured',
        'controlFactors':'membership-off and replicas-off only; not all independent control contributions',
        'repetitions':'One paired seed set; no independent repetitions'}
    result['energyBelowResolution']={mode+'-'+row:value.get('energyBelowResolution') for (mode,row),value in stages.items()}
    result['conditions']=['Generator opens a new TLS connection per request (no keep-alive); entrance delay and gateway CPU include handshake cost.']
    if targeted:
        result['conditions'].append('표적 재실행, 시작 상태 복원, 소스 R1~R6+R4b+C1+C2')
        result['targetedRestorations']=targeted
    if 'genesis2' in (b.get('schedulerSettings') or {}).get('excludedNodes',[]):
        result['conditions'].append('genesis2 하드웨어 결함으로 제외 (excluded for hardware failure); seven serving Nodes only.')
    result['schedulerSettings']=b.get('schedulerSettings')
    result['visibility']='PRIVATE_OPERATIONAL_ONLY; original raw/configuration and their paths excluded from public evidence indexes'
    result['rowJudgmentCounts']=dict(Counter(value['status'] for value in stages.values()))
    result['measurementMeaning']='Measured result links are not criterion PASS or complete scenario coverage; negative SLO/source overload findings and declared unmeasured scope remain visible.'
    result['issuanceFidelity']={mode+'-'+row:{name:metrics.get('issuanceFidelity') for name,metrics in value.get('requests',{}).items()} for (mode,row),value in stages.items()}
    replay=replay_receipt(root)
    result['compositeRun']={'label':replay['label'],'rowTag':replay['rowTag'],'restorationAccepted':replay['accepted']} if replay else None
    if targeted:result['compositeRun']={'label':'표적 재실행, 시작 상태 복원, 소스 R1~R6+R4b+C1+C2','rowTag':selected_tag,'restorationAccepted':all(record['accepted'] for record in targeted.values())}
    result['rowDirectories']={mode+'-'+row:str(folder) for (mode,row),folder in folders.items()}
    finish=root/('finish'+('-'+selected_tag if targeted else '-'+replay['rowTag'] if replay else '')+'-complete.json')
    restored=load(finish) if finish.exists() else None
    result['physicalRestoration']=restored
    save(root/'e5-summary.json',result); return result


def publication_measurements(binding, power):
    """Record the current collector's semantics, without a comparability gate."""
    config=binding['config']
    coverage=power.get('energyCoverage')
    policy=coverage['policy'] if coverage else module('e5publication_energy',HERE/'e5-archive.py').energy_policy(config.get('collection',{}))
    return {'energyCoverage':coverage,'gapErrorBoundJoules':power.get('gapErrorBoundJoules'),
        'measurementRules':{'harnessRepositoryCommit':config.get('harnessRepositoryCommit'),
            'energyCoverage':policy,'evidenceSource':'api','gcMeaning':'runtime-metrics-delta'}}


def publish_experiment(root, identifier, store_config, stages, status):
    """Publish PRIVATE raw inputs first, then one immutable serving summary.

    CLI: publish --experiment-id ID --store-config /private/store.json.
    Existing IDs are never corrected in place: choose a new experiment ID.
    """
    if not isinstance(identifier,str) or not re.fullmatch(r'[A-Za-z0-9_-][A-Za-z0-9._-]*',identifier):
        raise ValueError('Safe ASCII experiment ID required')
    config=load(store_config);store=config['logicalStore']
    if not isinstance(store,str) or not store.strip():raise ValueError('Configured logical store required')
    if not stages:raise ValueError('Actual completed stage windows required for publication')
    for stage in stages:
        lower,upper=dt.datetime.fromisoformat(stage['from'].replace('Z','+00:00')),dt.datetime.fromisoformat(stage['to'].replace('Z','+00:00'))
        if lower.tzinfo is None or upper.tzinfo is None or lower>=upper:raise ValueError('Real timezone-qualified [from,to) stage windows required')
    destination=root/('publication-'+identifier)
    if destination.exists():raise RuntimeError('Publication ID already attempted locally; retain evidence and choose a new ID')
    destination.mkdir()
    prefix='serving/experiments.v1alpha1/'+identifier+'/'
    objects=[];empty=[]
    folders=sorted({stage['_folder'] for stage in stages})
    for folder in folders:
        for path in sorted(folder.rglob('*')):
            if not path.is_file():continue
            if path.resolve()==Path(store_config).resolve():raise ValueError('Private store configuration cannot be inside raw evidence')
            if path.is_symlink():raise ValueError('Symlink evidence is not published')
            relative=path.relative_to(root).as_posix()
            if any(c in relative for c in '\\:%?#\x00\r\n\t') or relative.strip()!=relative:raise ValueError('Raw evidence requires clean relative object keys')
            ref(path,allow_empty=True)
            if not path.stat().st_size:
                empty.append(relative)
                continue
            objects.append({'store':store,'key':prefix+'raw/'+relative,'localPath':str(path)})
    inventory=destination/'raw-inventory.json'
    save(inventory,{'schemaVersion':'serving-experiment-raw-inventory.v1alpha1','emptyFiles':empty,'objects':[{'store':row['store'],'key':row['key']} for row in objects]})
    objects.append({'store':store,'key':prefix+'raw/inventory.json','localPath':str(inventory)})
    summary={'schemaVersion':'serving-experiment.v1alpha1','id':identifier,'from':min((stage['from'] for stage in stages),key=lambda value:dt.datetime.fromisoformat(value.replace('Z','+00:00'))),'to':max((stage['to'] for stage in stages),key=lambda value:dt.datetime.fromisoformat(value.replace('Z','+00:00'))),'status':status,'stages':[{key:value for key,value in stage.items() if key!='_folder'} for stage in stages],'sources':[{'store':row['store'],'key':row['key']} for row in objects]}
    summary_path=destination/'summary.json';save(summary_path,summary)
    manifest=destination/'manifest.json'
    save(manifest,{'objects':objects,'summary':{'store':store,'key':prefix+'summary.json','localPath':str(summary_path)}})
    result=subprocess.run(['go','run','./hack/e5/publish','--config',str(store_config),'--manifest',str(manifest)],cwd=REPO,capture_output=True,text=True)
    save(destination/'upload-result.json',{'exitCode':result.returncode,'stdout':result.stdout,'stderr':result.stderr})
    if result.returncode:raise RuntimeError('Immutable publication failed; retain evidence and choose a new ID for corrections')
    receipt=json.loads(result.stdout);save(destination/'receipt.json',receipt)
    return receipt


def publish_campaign(binding, root, identifier, store_config):
    result=summarize(binding,root);stages=[]
    bound=load_binding(binding)
    folders={Path(path) for path in result['rowDirectories'].values()} if result['compositeRun'] else root.iterdir()
    for folder in sorted(folders):
        if not folder.is_dir() or not (folder/'phase.json').exists():continue
        phase=load(folder/'phase.json');judgment=load(folder/'judgment.json')
        requests={}
        fields=('offered','clientRows','unfinishedOrUnsent','outcomes','completedLatencyNS','gatewayCompletedLatencyNS','latencyScope','scheduledLatencyNS','entranceDelayNS','entranceDelayMeaning','actualLatencyNS','completionResidualNS','gatewayTailNS','deadlineSuccess','deadlineSuccessFraction','requiredSuccessfulRank','D1','unsentAtWindowEnd','actualGatewayIdentities','gatewayFinalOutcomes','offeredGatewayFinalOutcomes','selectedResidualPercentile')
        for name,value in judgment['requests'].items():
            requests[name]={key:value[key] for key in fields if key in value}
            requests[name]['issuanceFidelity']=value.get('issuanceFidelity')
            requests[name].update(successNumerator=value['deadlineSuccess'],successDenominator=value['offered'],population='Frozen offered arrivals; final outcome and successful execution populations remain separate')
            population_fields=('rawRows','populationUniqueRows','duplicateRows','kinds','uniqueRequests','uniqueFinalRows','outcomes','executionComplete','completed','withoutUnambiguousHTTPFinal','nativeSupplementRows','nativeOutcomes','complete')
            for key in ('rawPopulation','offeredResponsePopulation'):
                population=value.get(key) or {}
                requests[name][key]={field:population[field] for field in population_fields if field in population}
            requests[name]['missingReasons']={'deadlineSuccessFraction':'No offered arrivals' if not value['offered'] else None,'completedLatencyNS':'No successful execution-complete gateway responses' if not value.get('completedLatencyNS',{}).get('samples') else None,'completionResidualNS':'No complete native/dispatched residual observations' if not value.get('completionResidualNS',{}).get('samples') else None}
        power=judgment.get('power') or {}
        kpis={'status':judgment['status'],'visibility':'PRIVATE_OPERATIONAL_ONLY','requests':requests,'judgmentCounts':dict(Counter(value['D1'] for value in judgment['requests'].values())),'wholePoolJoules':power.get('wholePoolJoules') if power.get('complete') else None,'activePoolJoules':power.get('activePoolJoules') if power.get('complete') else None,'energyPopulation':'Whole/active shared pool during offered [from,to); not attributed per workload','limitations':result['limitations'],'missingReasons':{'energy':None if power.get('complete') else 'Actual whole/active power coverage incomplete','judgment':None if judgment['status']!='NOT_READY' else 'Required evidence incomplete; actual reasons retained in raw judgment'}}
        kpis.update(publication_measurements(bound,power))
        kpis['schedulerSettings']=result['schedulerSettings']
        kpis['missingReasons']['schedulerSettings']=None if result['schedulerSettings'] is not None else 'Binding does not contain frozen scheduler settings'
        stages.append({'id':folder.name,'from':phase['startUTC'],'to':phase['endUTC'],'kpis':kpis,'_folder':folder})
    return publish_experiment(root,identifier,store_config,stages,result['status'])


def check_preparation():
    """Real recorded-data and missing-input gates only; never an E5 run or readiness claim."""
    import ast
    sys.dont_write_bytecode=True
    scripts=('e5.py','e5-wire.py','e5-producer.py','e5-raw.py','e5-archive.py')
    for name in scripts:ast.parse((HERE/name).read_text(),filename=name)
    op=load(OPERATIONAL_RECIPE);sys.path.insert(0,str(REPO/'load-generator/src'))
    from chill_loadgen import cli
    producer=module('e5recordedproducer',HERE/'e5-producer.py')
    parser=cli.build_parser();replay=next(a for a in parser._actions if getattr(a,'choices',None) and 'replay' in a.choices).choices['replay']
    flags={flag for action in replay._actions for flag in action.option_strings}
    assert {'--workload-slo','--gateway-timeout','--request-lifetime','--delivery-margin','--timeout'}<=flags
    payloads={}
    for name in WORKLOADS:
        w=op['workloads'][name];records=cli.load_records(Path(w['datasetDir']),w['fields']);identities=producer.identity(records)
        assert identities and len({item['recordID'] for item in identities})==len(identities)
        assert all(set(item['fields'])==set(w['fields']) and all(field['bytes']>0 and len(field['sha256'])==64 for field in item['fields'].values()) for item in identities)
        payloads[name]={'records':len(identities),'fields':w['fields'],'identitySHA256':hashlib.sha256(json.dumps(identities,separators=(',',':')).encode()).hexdigest(),'assignedSeed':SEEDS[name]}
    plan=Path(E5['plan'])
    try:bind(plan)
    except (ValueError,KeyError) as error:missing_parameters=str(error)
    else:raise AssertionError('Non-phase6 actual plan unexpectedly accepted as a complete E5 binding')
    reader=module('e5recordedraw',HERE/'e5-raw.py');events,provenance=reader.scheduler_events(CAMPAIGN)
    assert not provenance['complete'] and provenance['errors'] and not events
    collector=module('e5recordedarchive',HERE/'e5-archive.py');collector._self_check()
    assert list(inspect.signature(collector.collect).parameters)==['folder','phase','collection','io','timeout_seconds']
    schema=subprocess.run([sys.executable,'-B',str(HERE/'e5-wire.py'),'schema'],capture_output=True,text=True,timeout=5)
    assert schema.returncode==0;required= json.loads(schema.stdout)
    assert {'g0Evidence','mSeconds','N','workloads'}<=set(required['numbers'])
    return {'schema':'e5-preparation-check.v1','at':now(),'visibility':'PRIVATE_OPERATIONAL_ONLY','status':'PREPARATION_INPUT_CHECK_ONLY','scriptsParsed':list(scripts),'actualRecordedInputs':payloads,'missingPhase6Gate':{'status':'NOT_READY','reason':missing_parameters},'missingRawGate':{'status':'NOT_READY','errors':provenance['errors']},'collectionInterface':'PARSED_ONLY; actual capture/reducer not exercised','sourceCLI':'Actual installed source parser/loaders; pinned-carrier equality still requires released metadata Jobs','scientificResults':'UNMEASURED; no binding/run/cluster request/inference generated'}


def check_recorded(source_sha):
    """One authorized real BEFORE-input check; no E5 parameter/default or live inference."""
    import ast
    import resource
    sys.dont_write_bytecode=True
    op=load(OPERATIONAL_RECIPE);cfg=op['affectedSmoke'];out=Path(cfg['outputDirectory']);reuse=out.exists()
    result_path=out/('affected-smoke.consumer-retry.json' if reuse else 'affected-smoke.json')
    if reuse:
        prior=load(out/'affected-smoke.json');index=load(out/'raw-source-index.json')
        if result_path.exists() or prior['status']!='NOT_READY' or index['complete'] is not True or index['errors']:raise ValueError('Only one failed-check consumer retry with a complete unchanged capture is permitted')
    else:out.mkdir()
    result={'at':now(),'status':'NOT_READY','visibility':'PRIVATE_OPERATIONAL_ONLY','scientificResults':'UNMEASURED; historical BEFORE input, not an E5 run','scope':cfg,'reusesSuccessfulRawCapture':reuse}
    if reuse:result['originalFailedAttempt']=ref(out/'affected-smoke.json')
    save(result_path,result)
    try:
        if not source_sha or not re.fullmatch('[0-9a-f]{40}',source_sha):raise ValueError('Actual source-once frozen final SHA required')
        receipt_bytes=Path(cfg['powerReceipt']).read_bytes();receipt=json.loads(receipt_bytes);binary=ref(cfg['powerExecutable'])
        if receipt.get('sourceSHA')!=source_sha or receipt.get('executable')!=binary['path'] or receipt.get('sha256')!=binary['sha256'] or receipt.get('buildExitCode')!=0:raise ValueError('Actual same-final-SHA existing power binary/receipt required; no build fallback')
        phase_dir=Path(cfg['phaseDirectory']);phase=load(phase_dir/'phase.json');before=load(cfg['beforeBinding']);post=load(phase_dir/'post-state.json')
        need(phase,['startUTC','endUTC','drainedUTC','offeredRequests','sourceSha'],'Actual terminal BEFORE phase')
        if phase.get('error') or phase['sourceSha']!=before['beforeSha'] or not positive(phase['offeredRequests']) or post['snapshot'].get('outstanding') or post['snapshot'].get('reservations') or any(a.get('status') in ('running','cancel') for a in post.get('actions',[])):raise ValueError('Actual completed nonempty BEFORE input and recorded terminal drain required; no made-up interval')
        for name in ('e5.py','e5-wire.py','e5-producer.py'):ast.parse((HERE/name).read_text(),filename=name)
        receipt_copy=out/('power-receipt.consumer-retry.json' if reuse else 'power-receipt.json');receipt_copy.write_bytes(receipt_bytes)
        result['inputs']={'beforePhase':ref(phase_dir/'phase.json'),'beforePostState':ref(phase_dir/'post-state.json'),'beforeBinding':ref(cfg['beforeBinding']),'powerReceipt':ref(receipt_copy),'powerExecutable':binary}
        result['sourceIdentities']={'historicalBefore':phase['sourceSha'],'existingReducer':source_sha}
        if reuse:
            if load(out/'phase.json')!=phase or result['inputs']['beforePhase']!=prior['inputs']['beforePhase'] or result['inputs']['beforePostState']!=prior['inputs']['beforePostState'] or result['inputs']['beforeBinding']!=prior['inputs']['beforeBinding']:raise ValueError('Actual frozen phase/binding changed; no substitute capture')
        else:save(out/'phase.json',phase);save(out/'pre-state.json',load(phase_dir/'pre-state.json'))
        resource.setrlimit(resource.RLIMIT_FSIZE,(cfg['maxArtifactFileBytes'],cfg['maxArtifactFileBytes']))
        d=module('e5affectedconduct',D2/'conduct_before.py')
        if reuse:
            for source in index['sources']:
                actual=ref(source['path'])
                if any(actual[key]!=source[key] for key in ('path','bytes','sha256')):raise ValueError('Captured raw identity changed; no API recapture')
            facts=prior['inputs']['settings']
            actual_facts=ref(out/'pre-settings.json')
            if any(actual_facts[key]!=facts[key] for key in ('path','bytes','sha256')):raise ValueError('Captured settings identity changed')
            archive=module('e5affectedarchive',HERE/'e5-archive.py');scope,configured=archive._scope(out)
            reduction=archive._reduce(out,phase,cfg['powerExecutable'],time.monotonic()+cfg['timeoutSeconds']-40,scope,configured)
            captured={'status':'NOT_READY' if reduction['errors'] else 'CAPTURED_NOT_EVALUATED','errors':reduction['errors'],'sourceIndex':ref(out/'raw-source-index.json'),'originalCollection':ref(out/'archive-collection.json'),'powerReduction':ref(out/'power-reduction.json'),'liveOrS3Recapture':False}
            save(out/'archive-consumer-retry.json',captured)
        else:
            d.BASE=op['endpoint'];d.CA=Path(op['caFile']);d.TOKEN=OPS/'.token-collector'
            io=object.__new__(d.Conductor);io.journal_lock=threading.Lock();io.journal=out/'commands.jsonl';io.a=argparse.Namespace(phase='e5-affected-historical-input-check');io.source=source_sha
            io.http=urllib.request.build_opener(d.NoRedirect(),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=op['caFile'])))
            settings=io.api('GET','/api/v1/scheduler/settings',timeout=cfg['timeoutSeconds']-40)
            save(out/'pre-settings.json',settings);result['inputs']['settings']=ref(out/'pre-settings.json');save(result_path,result)
            collection={'powerExecutable':cfg['powerExecutable']}
            captured=module('e5affectedarchive',HERE/'e5-archive.py').collect(out,phase,collection,io,cfg['timeoutSeconds']-40)
        if captured['status']!='CAPTURED_NOT_EVALUATED':raise ValueError('Actual archive/current/reducer incomplete; retain NOT_READY manifest')
        collected_bytes=sum(p.stat().st_size for p in out.rglob('*') if p.is_file())
        if collected_bytes>cfg['maxCollectedBytes']:raise ValueError('Collected actual input exceeds smoke byte bound; no subset substituted')
        evidence=load(cfg['nativeSafetyEvidence']);proof=load(evidence['nativeHelperResult']);bound=state_node_references(load(out/'pre-state.json'),NODES)
        alerts=[]
        for line in (CAMPAIGN/'control-alerts.jsonl').read_text().splitlines():
            row=json.loads(line)
            if row.get('at')==evidence['actualAlertAt'] and row.get('uid') in evidence['actualControlPodUIDs'] and row.get('restarts')==evidence['restartCounts']['after']:alerts.append(row)
        if not alerts:raise ValueError('Original actual native restart rows missing; no manufactured alert')
        decisions=[];notifications=[]
        for row in alerts:
            known=d.resolve_control_alert(row,[proof],bound)
            unknown=d.resolve_control_alert(row,[],bound,report_candidate=lambda r,x:notifications.append({'historicalReplayOnly':True,'row':r,'decision':x}))
            if known['stop'] or not unknown['stop']:raise ValueError('Actual recorded safety proof/unknown fail-closed mismatch')
            decisions.append({'raw':row,'actualCompletedProofDecision':known,'sameRecordedRowWithoutEvidence':unknown})
        result['recordedSafety']={'source':ref(cfg['nativeSafetyEvidence']),'nativeProof':ref(evidence['nativeHelperResult']),'decisions':decisions,'historicalUnknownNotifications':notifications,'liveFaultOrRebootInjected':False,'newImmediateLiveNodeLookup':'UNEXERCISED; actual shared completed-proof and unknown-stop paths only'}
        resource.setrlimit(resource.RLIMIT_AS,(cfg['maxReaderAddressSpaceBytes'],cfg['maxReaderAddressSpaceBytes']))
        recorded_clients=[]
        with (phase_dir/'client.jsonl').open() as source:
            for number,line in enumerate(source,1):
                row=json.loads(line)
                if 'scheduledOffsetNs' in row:recorded_clients.append({'line':number,'row':row})
        actual_ids=[x['row']['requestID'] for x in recorded_clients if x['row'].get('requestID')]
        missing_ids=[x for x in recorded_clients if not x['row'].get('requestID')]
        raw=module('e5affectedraw',HERE/'e5-raw.py');events,provenance=raw.scheduler_events(out)
        received=raw.index_requests(out,before['workload']['uid'],phase['startUTC'],phase['endUTC'])
        indexed=raw.index_requests(out,before['workload']['uid'],phase['startUTC'],phase['endUTC'],actual_response_ids=actual_ids)
        conditions={'schedulerComplete':provenance['complete'],'receivedRequestErrorsEmpty':not received['errors'],'receivedHTTPFinalsUnambiguous':not received['summary']['withoutUnambiguousHTTPFinal'],'offeredResponseErrorsEmpty':not indexed['errors'],'actualReturnedIDsUniquelyFound':len(actual_ids)==len(set(actual_ids)) and set(actual_ids)<=set(indexed['finals']),'clientOutcomeCountMatchesOffered':len(recorded_clients)==phase['offeredRequests'],'clientResponseIDsComplete':not missing_ids,'clientOffsetsInsideOriginalWindow':all(type(x['row']['scheduledOffsetNs']) is int and 0<=x['row']['scheduledOffsetNs']<round(phase['durationSeconds']*1e9) for x in recorded_clients)}
        reasons=[name for name,complete in conditions.items() if not complete]
        raw_verdict={'at':now(),'status':'NOT_READY' if reasons else 'RECORDED_RAW_GATE_COMPLETE','scientificResults':'UNMEASURED','schedulerProvenance':provenance,'schedulerEvents':len(events),'requestPopulation':received['summary'],'offeredResponsePopulation':indexed['summary'],'requestErrors':{'received':received['errors'],'offeredResponse':indexed['errors']},'offeredRequests':phase['offeredRequests'],'actualReturnedResponseIDs':len(actual_ids),'clientRowsWithoutResponseID':missing_ids,'clientSource':ref(phase_dir/'client.jsonl'),'receivedAtWindow':{'from':phase['startUTC'],'to':phase['endUTC']},'receivedVsOfferedCountEqual':received['summary']['uniqueFinalRows']==phase['offeredRequests'],'conditions':conditions,'reasons':reasons,'identityMeaning':'Only actual responseIDs are joined; missing responseIDs stay individually unknown, never inferred from aggregate gateway counts.'}
        save(out/'recorded-offered-raw-verdict.json',raw_verdict);result['recordedRawVerdict']=ref(out/'recorded-offered-raw-verdict.json')
        if reasons:raise ValueError('Actual offered/raw identity population NOT_READY: '+','.join(reasons))
        result.update(status='AFFECTED_RECORDED_INPUT_CHECK_COMPLETE',finishedUTC=now(),collectedBytes=collected_bytes,schedulerEvents=len(events),requestPopulation=received['summary'],offeredResponsePopulation=indexed['summary'],power=load(out/'power.json'),powerDrainInclusive=load(out/'power-drain-inclusive.json'),notEstablished=['E5 numeric binding','canonical registration/pinned-carrier equality','live failure or native reboot scenario','E5 scientific result'])
        save(result_path,result);return result
    except Exception as error:
        result.update(error=str(error),finishedUTC=now());save(result_path,result);raise


def main():
    p=argparse.ArgumentParser(description=__doc__); p.add_argument('command',choices=['self-check','schema','check-preparation','check-recorded','bind','check','judge','summarize','publish','campaign','start','stage','smoke','restore','collect','factor','factor-restore','cleanup-jobs','finish','failure-select']); p.add_argument('--config',type=Path); p.add_argument('--binding',type=Path); p.add_argument('--out',type=Path); p.add_argument('--release',type=Path);p.add_argument('--source-sha'); p.add_argument('--stage',default=''); p.add_argument('--mode',default='full'); p.add_argument('--experiment-id'); p.add_argument('--store-config',type=Path); p.add_argument('--pre-state',type=Path); p.add_argument('--row-tag',default=''); p.add_argument('--dry-run',action='store_true'); p.add_argument('--failure-kind',choices=['delete-serving-pod','cordon-delete']); a=p.parse_args()
    try:
        if a.command=='self-check':self_check();return 0
        for value in (a.config,a.binding,a.out,a.release,a.pre_state):
            if value is not None and not value.is_absolute(): raise ValueError('All paths must be absolute')
        if a.row_tag and not re.fullmatch('[A-Za-z0-9]+',a.row_tag):raise ValueError('--row-tag requires ASCII letters and digits')
        if a.pre_state and not (a.command=='factor' and a.row_tag and (a.mode=='static' and a.stage=='E5-3' or a.mode=='full' and a.stage in ('E5-1','E5-5','E5-6','E5-7'))):raise ValueError('--pre-state requires tagged static or targeted full factor restoration')
        if a.pre_state and a.mode=='full' and (a.pre_state.name!='pre-state.json' or not re.fullmatch('full-'+re.escape(a.stage)+'(?:-[A-Za-z0-9]+)?',a.pre_state.parent.name) or a.pre_state.parent.parent!=a.out):raise ValueError('Targeted restoration requires the corresponding original full row pre-state.json in this Run')
        if a.dry_run and not a.pre_state:raise ValueError('--dry-run requires factor --pre-state')
        if a.command=='smoke' and (a.mode!='smoke' or a.stage!='SMOKE'): raise ValueError('Smoke requires --mode smoke --stage SMOKE; never counted as E5')
        if a.command=='schema': print(json.dumps(REQUIRED,indent=2)); return 0
        if a.command=='check-recorded':
            result=check_recorded(a.source_sha);print(json.dumps({'status':result['status'],'at':result['finishedUTC'],'scientificResults':result['scientificResults']}));return 0
        if a.command=='check-preparation':
            if a.out and a.out.exists():raise ValueError('Check receipt already exists; no unapproved repeat')
            result=check_preparation()
            if a.out:save(a.out,result)
            print(json.dumps(result,indent=2));return 0
        if a.command=='bind':
            if not a.config or not a.out: raise ValueError('--config and --out required')
            if a.out.exists(): raise ValueError('Binding destination exists; do not overwrite frozen inputs')
            result=bind(a.config); save(a.out,result); return 0
        if not a.binding: raise ValueError('--binding required')
        if a.command=='check':
            b=load_binding(a.binding)
            for x in b['sources'].values():
                if ref(x['path'])!=x: raise ValueError('Frozen input/package identity changed')
            assert sum(x['seconds'] for x in b['schedule'])==11700
            assert sum(x['seconds'] for x in b['schedule'] if x['id'] not in ('E5-0','E5-7'))==10200
            assert set(SEEDS.values())=={101,202,303}
            assert len(b['rowOrder'])==9
            print(json.dumps({'status':'OFFLINE_INPUT_CHECK_ONLY','scientificResults':'UNMEASURED','scheduleSeconds':21900,'N':b['config']['N']})); return 0
        if not a.out: raise ValueError('--out absolute run or stage directory required')
        if a.dry_run:
            if a.mode=='full':full_restore_dry_run(a.binding,a.out,a.pre_state,a.stage,a.row_tag)
            else:static_restore_dry_run(a.binding,a.out,a.pre_state,a.row_tag)
            return 0
        if a.command=='judge':
            result=judge(a.binding,a.out)
            return 2 if result['status']=='INVALID_GENERATOR' or (result.get('failureScenarioEvidence') or {}).get('status')=='INVALID' else 0
        if a.command=='summarize': summarize(a.binding,a.out,a.row_tag or None); return 0
        if a.command=='publish':
            if not a.experiment_id or not a.store_config:raise ValueError('publish requires --experiment-id and --store-config; summary is final immutable record')
            publish_campaign(a.binding,a.out,a.experiment_id,a.store_config);return 0
        if not a.release: raise ValueError('CPO command requires --release')
        obj=CPO(a)
        if a.command in ('stage','smoke'): obj.run()
        elif a.command=='collect': obj.collect(a.out)
        else: getattr(obj,a.command.replace('-','_'))()
        return 0
    except Exception as exc:
        if a.out and a.command!='bind': save(a.out/('STOP'+('-'+a.row_tag if a.row_tag else '')+'.json'),{'at':now(),'command':a.command,'error':str(exc),'status':'NOT_READY','scientificResults':'UNMEASURED_OR_PARTIAL; never PASS'})
        print('NOT_READY: '+str(exc),file=sys.stderr); return 2


# Local check: needs CHILL_EXPERIMENT_CONFIG (module constants) but no cluster binding or network.
if __name__=='__main__' and sys.argv[1:]==['self-check']:
    self_check()
    sys.exit(0)
if __name__=='__main__': sys.exit(main())
