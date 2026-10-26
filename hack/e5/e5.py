#!/usr/bin/env python3
"""Offline bind/check/judge and immutable private publication; released CPO campaign control.
Cleanup is one server reset with retained steps/ACKs; no client cleanup assembly.
No implicit phase advance, retry, runtime build, fault injection, or guessed phase6 values.
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
import subprocess
import sys
import time
import threading
import urllib.parse
import urllib.request
import zlib

HERE = Path(__file__).resolve().parent
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
 'approval': ['phase6ApprovalUTC', 'e5StartApprovalUTC', 'sourceSHA', 'controlImageDigest', 'runtimeDigests', 'D1', 'D6', 'D9'],
 'inputs': ['scheduler', 'capacity', 'nodes', 'pods', 'after', 'd25', 'gMeasurement', 'mMeasurement', 'ranking', 'costs', 'bias', 'gateway', 'producer'],
 'numerical': ['G', 'mSeconds', 'N', 'burstLowSeconds', 'timeboxes', 'smokeSeconds'],
 'workload': ['name', 'uid', 'generation', 'slo', 'R', 'deliverySeconds', 'socketSeconds', 'gatewayCapSeconds', 'workers', 'datasetDir', 'fields', 'profilingRecordIDs', 'jobTemplate', 'producerEndpoint', 'producerTokenFile', 'producerCAFile', 'producerDatasetDir'],
 'collection': ['hosts', 'archiveRefs', 'observationDirectory', 'serverContainer', 'schedulerConfigMap', 'serverHostReadArgv', 'metricSourcesConfigMap', 'metricSourcesKey', 'powerExecutable', 'powerReceipt'],
 'factor': ['name', 'controls', 'witness'],
 'timeboxes': ['setupSeconds', 'barrierSeconds', 'drainSeconds', 'flushSeconds', 'collectSeconds', 'apiSeconds', 'settleSeconds'],
}
HOST_USAGE = r'''
import datetime as dt, json, pathlib, subprocess, sys
directory=sys.argv[1]
def command(argv):
    p=subprocess.run(argv,capture_output=True,text=True,timeout=20,check=True)
    return json.loads(p.stdout)
found=[x for x in command(['/usr/bin/crictl','ps','-o','json'])['containers'] if x.get('metadata',{}).get('name')=='serving-entrypoint' and x.get('labels',{}).get('io.kubernetes.pod.namespace')=='chill-system']
if len(found)!=1: raise RuntimeError('Exact host server container ambiguous')
c=found[0];pid=command(['/usr/bin/crictl','inspect',c['id']])['info']['pid']
if not directory.startswith('/') or '..' in pathlib.Path(directory).parts: raise ValueError('Absolute captured observation directory required')
root=pathlib.Path('/proc')/str(pid)/'root'/directory.lstrip('/')/'v1alpha5'
print(json.dumps({'at':dt.datetime.now(dt.timezone.utc).isoformat(),'containerID':c['id'],'podUID':c.get('labels',{}).get('io.kubernetes.pod.uid'),'directory':directory,'bytes':sum(p.stat().st_size for p in root.rglob('*') if p.is_file())}))
'''


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, str(path)); result = importlib.util.module_from_spec(spec); spec.loader.exec_module(result); return result


def load(path):
    return json.loads(Path(path).read_text(), parse_constant=lambda x: (_ for _ in ()).throw(ValueError('Nonfinite JSON: '+x)))

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


def d25_gate(index_path,snap,refs=None):
    refs={} if refs is None else refs
    expected_path=Path(E5['d25Manifest'])
    expected={x['caseID']:x for x in load(expected_path)['targets']}; closures=load(index_path)['closures']
    if len(closures)!=25 or {x['caseID'] for x in closures}!=set(expected): raise ValueError('Actual exact25 closure index required; preparation manifest is not completion evidence')
    for entry in closures:
        need(entry,['caseID','crPath','rawPath','returnStatePath','returnEvidencePath','returnedMode'],'d25 closure index')
        for field in ('crPath','rawPath','returnStatePath','returnEvidencePath'): refs[entry['caseID']+'-'+field]=ref(entry[field])
        cr=load(entry['crPath']); raw=load(entry['rawPath']); wanted=expected[entry['caseID']]
        if cr['status']['phase']!='Complete' or not cr['metadata'].get('uid') or raw.get('verdict')!='Accepted' or raw.get('unknownRows')!=0 or raw.get('idleIdentified') is not True or raw.get('precisionTargetMet') is not True or raw.get('drainError') or raw.get('idleNanosPerRequest',math.inf)>1e6: raise ValueError('Actual d25 scientific/physical/idle/precision closure incomplete: '+entry['caseID'])
        if any(raw.get(k)!=wanted[k] for k in ('node','profileID','powerMode','runtimeImage')) or cr['status'].get('perRequestNanos')!=raw.get('perRequestNanos'): raise ValueError('d25 exact Node/profile/mode/runtime or signed status/raw mismatch')
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


def owned_target(snapshot,uids,routing):
    """Declared target from real physical ownership; never a served-UID observation."""
    if any(i.get('ownerWorkloadUID') not in uids for i in instance_rows(snapshot['instances'])):raise RuntimeError('Foreign physical ownership; no owned-target/reset substitution')
    return {'instances':sorted([{'id':i['id'],'node':i['node'],'profileID':i['profile']['id'],'allocation':i['allocation'],'maxBatch':i['maxBatch'],'maxWaitNanos':i['maxWaitNanos'],'workloadUID':i['ownerWorkloadUID']} for i in instance_rows(snapshot['instances'])],key=lambda i:i['id']),'routing':routing,'modes':{n['node']['name']:n['powerMode'] for n in snapshot['nodes'] if n['node']['name'] in NODES}}

def power_observation(c):
    """Read the explicit operational sampler policy, not mutable API settings."""
    command = ENVIRONMENT['d2']['kubectl'] + ['-n', 'chill-system', 'get', 'configmap', c['collection']['schedulerConfigMap'], '-o', 'json']
    result = subprocess.run(command, capture_output=True, text=True, check=True, timeout=c['timeboxes']['apiSeconds'])
    config = json.loads(json.loads(result.stdout)['data']['scheduler.json'])
    policy = config['powerObservation']
    coverage = policy['coverage']
    if not all(positive(coverage[k]) for k in ('MaximumGap', 'MaximumBoundaryDistance', 'MinimumCoverage')) or coverage['MinimumCoverage'] > 1:
        raise ValueError('Explicit finite operating power coverage required')
    return policy



def bind(path):
    c = load(path); require_strict_archives(c)
    need(c, ['approval', 'inputs', 'G', 'mSeconds', 'N', 'burstLowSeconds', 'timeboxes', 'smokeSeconds', 'workloads', 'collection', 'factors', 'endpoint', 'caFile', 'tokenFile', 'kubeContext'], 'phase6')
    need(c['approval'], REQUIRED['approval'], 'approval'); need(c['inputs'], REQUIRED['inputs'], 'inputs'); need(c['collection'], REQUIRED['collection'], 'collection'); need(c['timeboxes'], REQUIRED['timeboxes'], 'timeboxes')
    if not all(positive(c[k]) for k in ('G', 'smokeSeconds')) or type(c['mSeconds']) not in (int,float) or not math.isfinite(c['mSeconds']) or c['mSeconds']<0 or not 0 < c['burstLowSeconds'] < 900: raise ValueError('Actual G/m/smoke and 0<burstLowSeconds<900 required')
    if any(not positive(v) for v in c['timeboxes'].values()) or c['timeboxes']['setupSeconds']+3600+c['timeboxes']['drainSeconds']+c['timeboxes']['flushSeconds'] > 4200: raise ValueError('Finite collector-compatible timeboxes required (sum<=4200s)')
    if type(c['N']) is not int or c['N'] < 1 or c['N'] != len(c['factors']): raise ValueError('N must equal a nonempty preselected witnessed factor set; absent witness is NOT_READY')
    host_read=c['collection']['serverHostReadArgv']
    if not isinstance(host_read,list) or not host_read or not Path(host_read[0]).is_absolute() or host_read[-1]!='-': raise ValueError('Actual approved server-host privileged Python stdin reader argv required')
    refs = {k: ref(v) for k, v in c['inputs'].items()}
    for name in ('e5.py','auth.py','e5-wire.py','e5-producer.py','e5-raw.py','e5-archive.py'): refs['package-'+name]=ref(HERE/name)
    refs['operationalRecipe']=ref(OPERATIONAL_RECIPE); refs['experimentEnvironment']=ref(ENVIRONMENT_PATH)
    for name in ('conduct_before.py','bind_before.py','analyze_before.py','sample_cgroups.py','native_mode_request.py'):refs['D2-helper-'+name]=ref(D2/name)
    receipt=load(c['collection']['powerReceipt']); power=ref(c['collection']['powerExecutable'])
    if receipt.get('sourceSHA')!=c['approval']['sourceSHA'] or receipt.get('executable')!=power['path'] or receipt.get('sha256')!=power['sha256'] or receipt.get('buildExitCode')!=0: raise ValueError('Actual frozen-source existing offline power tool receipt required')
    refs['powerExecutable']=power;refs['powerReceipt']=ref(c['collection']['powerReceipt'])
    s = load(c['inputs']['scheduler']); snap = s['state']['snapshot'] if 'state' in s else s['snapshot']; settings = s['settings']
    cap = load(c['inputs']['capacity']); rows = cap if isinstance(cap, list) else cap['rows']
    d25_gate(c['inputs']['d25'],snap,refs)
    approved_modes=defaultdict(set)
    scope_path=Path(E5['d25Manifest'])
    for case in load(scope_path)['targets']:approved_modes[case['node']['name']].add(case['powerMode'])
    mode_scope={node['node']['name']:node['powerMode'] for node in snap['nodes'] if node['node']['name'] in NODES}
    if set(mode_scope)!=set(NODES) or any(mode not in approved_modes[name] for name,mode in mode_scope.items()):raise ValueError('Actual reference inference modes must remain within approved d25/G2 conditions; do not expand a power-mode study or manufacture native readback')
    refs['approvedInferenceModeScope']=ref(scope_path)
    gateway = load(c['inputs']['gateway'])
    need(gateway, ['requestTimeoutSeconds', 'sourceSHA', 'controlImageDigest', 'capturedUTC'], 'Actual gateway HandlerConfig capture')
    if gateway['sourceSHA'] != c['approval']['sourceSHA'] or gateway['controlImageDigest'] != c['approval']['controlImageDigest']: raise ValueError('Gateway actual source/image mismatch')
    producer_receipt = load(c['inputs']['producer'])
    need(producer_receipt, ['sourceSHA', 'imageDigest', 'cliFlags', 'deploymentReceipt'], 'Actual deployed producer receipt')
    if producer_receipt['sourceSHA'] != c['approval']['sourceSHA'] or not {'--workload-slo','--gateway-timeout','--request-lifetime','--delivery-margin','--timeout'} <= set(producer_receipt['cliFlags']): raise ValueError('Actual D2 loadgen image with final source CLI required; runtime7 rebuild remains zero')
    refs['producer-deployment'] = ref(producer_receipt['deploymentReceipt'])
    native = module('d2eligibility', D2/'bind_before.py')
    knodes = load(c['inputs']['nodes'])['items']
    if settings['completionMarginMS']/1000 != c['mSeconds']: raise ValueError('m must equal actual scheduler completionMarginMS')
    if sorted(settings['excludedNodes']) != sorted(E5['excludedNodes']): raise ValueError('Approved eight-node exclusions required')
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
        selected = [r for r in rows if r.get('input', {}).get('workloadUID') == w['uid'] and r['input'].get('slo') == w['slo'] and r['input'].get('completionMarginMS') == settings['completionMarginMS'] and r['input'].get('node', {}).get('name') in NODES]
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
                kube = next(n for n in knodes if n['metadata']['uid'] == instance['node']['uid'])
                actual_reasons=native.eligibility(instance,profile,node,kube,settings,inp['maxBatch'])
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
        if set(w['profilingRecordIDs']) & {x['recordID'] for x in payload[w['name']]}: raise ValueError('Profiling/evaluation payload subsets overlap')
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
    for k in ('hosts', 'archiveRefs'): refs[k] = ref(c['collection'][k])
    archive = load(c['collection']['archiveRefs'])
    if not archive or any(not x.get('key') or not x.get('localPath') for x in archive): raise ValueError('Exact approved archive source key/local path bindings required')
    stages = [('E5-0', 600, [0,0,0]), ('E5-1', 1800, [.3,.3,.3]), ('E5-2-low', c['burstLowSeconds'], [.3,.3,.3]), ('E5-2-high', 900-c['burstLowSeconds'], [.3,.3,.9]), ('E5-3-50', 600, [.5]*3), ('E5-3-70', 600, [.7]*3), ('E5-3-90', 600, [.9]*3), ('E5-4', 900, [1.2]*3), ('E5-5-low', 600, [.3]*3), ('E5-5-high', 600, [.9]*3), ('E5-6', 3600, [.3]*3)]
    ws = {w['name']: w for w in c['workloads']}; schedule = []
    for label, duration, fractions in stages:
        intended = {name: ws[name]['R']*fractions[j] for j, name in enumerate(WORKLOADS)}; rates = dict(intended)
        remainder = c['G']-rates[WORKLOADS[0]]-rates[WORKLOADS[2]]
        if remainder < 0: raise ValueError(label+': negative PLC G remainder requires user decision')
        rates[WORKLOADS[1]] = min(rates[WORKLOADS[1]], remainder)
        if label == 'E5-4' and rates != intended:
            decision = c.get('overloadClipDecision')
            if not decision or not decision.get('approvalUTC') or not decision.get('retainedOverloadWitness'): raise ValueError('Clipping intended overload requires explicit approved retained-overload witness')
            refs['overloadClipWitness'] = ref(decision['retainedOverloadWitness'])
        st = {'id': label, 'seconds': duration, 'intendedRates': intended, 'rates': rates, 'PLCRemainingG': remainder, 'PLCClippedBy': intended[WORKLOADS[1]]-rates[WORKLOADS[1]], 'traces': {}}
        for name, rate in rates.items():
            offsets = cli.arrival_offsets_ns('poisson', rate, duration, SEEDS[name]) if rate else []
            st['traces'][name] = {'seed': SEEDS[name], 'offsetsNS': offsets, 'offered': len(offsets), 'arrivalSHA256': hashlib.sha256(json.dumps(offsets, separators=(',', ':')).encode()).hexdigest()}
        schedule.append(st)
    # A normative row has one producer window; rate changes have no drain/setup hole.
    combined = []
    for row_id in ['E5-0', 'E5-1', 'E5-2', 'E5-3', 'E5-4', 'E5-5', 'E5-6']:
        parts = [part for part in schedule if part['id'] == row_id or part['id'].startswith(row_id+'-')]
        row = {'id': row_id, 'seconds': sum(part['seconds'] for part in parts), 'parts': parts, 'rates': {name: max(part['rates'][name] for part in parts) for name in WORKLOADS}, 'traces': {}}
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
    settings = dict(settings, powerObservation=power_observation(c))
    c['harnessRepositoryCommit']=subprocess.check_output(['git','-C',str(REPO),'rev-parse','HEAD'],text=True).strip()
    c['tokenLifetime'] = token_preflight(c, schedule)
    return {'schema': 'e5-binding.v1', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': now(), 'schedulerSettings': settings, 'config': c, 'sources': refs, 'kappa': kappa, 'payload': payload, 'schedule': schedule, 'rowOrder': ['E5-0','E5-1','E5-2','E5-3','E5-S','E5-4','E5-5','E5-6'], 'fullSeconds': 10800, 'staticSeconds': 10200, 'factorSeconds': 2100*c['N'], 'accounting': {'d': '9/34 last verified; phase6 raw supersedes only by actual evidence', 'exactCost': '69/69', 'evaluation': '0/7', 'planRows': '0/8', 'failureScenario': 'UNEXERCISED; no fault injection authorized'}, 'limitations': ['B-only timing uncertainty; no profile statistical CI/global95 guarantee', 'D04/D09/D14/D23/D33 known systematic profile-serving bias; causal shares unknown', 'Single paired seed set is a case study, not three independent repetitions', E5['runtimeExclusionNote']]}


class CPO:
    def __init__(self, a):
        self.a = a; self.b = load_binding(a.binding); self.c = self.b['config']; self.out = a.out
        if self.b['status'] != 'BOUND_NOT_MEASURED': raise ValueError('Binding not ready')
        if 'powerObservation' not in self.b.get('schedulerSettings', {}):
            raise ValueError('Binding lacks frozen operating power observation; bind again')
        release = load(a.release)
        self.release = release
        expected={'executor':'CampaignOps','bindingPath':str(a.binding),'sourceSHA':self.c['approval']['sourceSHA']}
        if any(release.get(k)!=v for k,v in expected.items()) or not release.get('mainInstructionUTC'): raise ValueError('Actual Main/CPO frozen input release required')
        if release.get('command')!='e5-execute' and (release.get('command')!=a.command or release.get('stage')!=a.stage): raise ValueError('Command outside frozen release')
        if not a.out.is_absolute() or not a.binding.is_absolute() or not a.release.is_absolute() or CAMPAIGN.resolve() not in a.out.resolve().parents: raise ValueError('Absolute external campaign output and input paths required')
        self.out.mkdir(parents=True, exist_ok=True)
        d = module('d2conduct', D2/'conduct_before.py'); self.d = d; self.io = object.__new__(d.Conductor)
        self.io.out = self.out; self.io.journal = self.out/'commands.jsonl'; self.io.source = self.c['approval']['sourceSHA']
        self.io.journal_lock=threading.Lock()
        self.io.a=argparse.Namespace(phase='e5-'+a.mode+'-'+a.stage,command=a.command)
        self.io.phase_out=self.out
        # Existing authenticated client and kubectl contracts, bound to actual phase6 configuration.
        d.BASE = self.c['endpoint']; d.CA = Path(self.c['caFile']); d.TOKEN = Path(self.c['tokenFile']); d.K = ENVIRONMENT['d2']['kubectl']+['-n','chill-system']
        self.io.ctx = ssl.create_default_context(cafile=self.c['caFile']); self.io.http = urllib.request.build_opener(d.NoRedirect(), urllib.request.HTTPSHandler(context=self.io.ctx))
        self.uids = [w['uid'] for w in self.c['workloads']]; self.tb = self.c['timeboxes']; self.ws = {w['name']: w for w in self.c['workloads']}
        self.bound_nodes=[{'name':node['metadata']['name'],'uid':node['metadata']['uid']} for node in load(self.c['inputs']['nodes'])['items'] if node['metadata']['name'] in NODES]
        marker=self.out/'native-mode-authorization.json'
        self.native_mode_authorized_since=load(marker)['authorizedSinceUTC'] if marker.exists() else None
        self.native_evidence=[]

    def api(self, method, path, body=None): return self.io.api(method,path,body,timeout=self.tb['apiSeconds'])
    def state(self): return self.api('GET','/api/v1/scheduler/state')
    def settings(self): return self.api('GET','/api/v1/scheduler/settings')
    def put(self, settings):
        settings = copy.deepcopy(settings); settings.pop('staticRouting',None); settings.pop('staticRoutingValidatedAt',None)
        settings.pop('powerObservation', None)
        return self.api('PUT','/api/v1/scheduler/settings',settings)
    def idle(self):
        end = time.monotonic()+self.tb['drainSeconds']
        while time.monotonic()<end:
            s=self.state(); snap=s['snapshot']
            if not snap.get('outstanding') and not snap.get('reservations') and not any(x.get('status') in ('running','cancel') for x in s.get('actions',[])):
                views=[self.api('GET','/api/v1/workloads/'+n) for n in WORKLOADS]
                if all(v.get('pending')==0 and v.get('assigned')==0 for v in views): return s
            time.sleep(1)
        raise RuntimeError('Drain/terminal timebox exceeded; preserve partial evidence, stop')
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
        save(folder/(prefix+'-pods.json'),self.io.k(['get','pods','-A','-o','json']))
        save(folder/(prefix+'-nodes.json'),self.io.k(['get','nodes','-o','json']))
        pods=self.io.k(['get','pods','-l','app.kubernetes.io/component=serving-entrypoint','-o','json'])['items']
        if len(pods)!=1: raise RuntimeError('Actual server Pod ambiguous')
        statuses=[x for x in pods[0].get('status',{}).get('containerStatuses',[]) if x['name']==self.c['collection']['serverContainer']]
        expected_digest=self.c['approval']['controlImageDigest'].rsplit('sha256:',1)[-1]
        if len(statuses)!=1 or statuses[0].get('imageID','').rsplit('sha256:',1)[-1]!=expected_digest or not statuses[0].get('ready'): raise RuntimeError('Actual ready control image differs from final D2 receipt')
        argv=self.c['collection']['serverHostReadArgv']+[self.c['collection']['observationDirectory']]
        r=subprocess.run(argv,input=HOST_USAGE,capture_output=True,text=True,timeout=self.tb['apiSeconds'])
        save(folder/(prefix+'-pvc-usage.json'),{'stdout':r.stdout,'stderr':r.stderr,'exitCode':r.returncode,'scope':'Actual host /proc/container-pid/root observation directory only'})
        if r.returncode: raise RuntimeError('PVC raw usage unavailable')
    def authorize_native_modes(self):
        self.native_mode_authorized_since=now()
        save(self.out/'native-mode-authorization.json',{'authorizedSinceUTC':self.native_mode_authorized_since,'release':str(self.a.release),'meaning':'Actual pre-enable authorization fence; not a settings/native success ACK'})
    def static_placement(self):
        return self.b.get('schema')=='demo-e5-binding.v1' and self.b.get('demoConfig',{}).get('staticPlacement') is True
    def check_power_observation(self):
        if power_observation(self.c) != self.b['schedulerSettings']['powerObservation']:
            raise RuntimeError('Operating power observation differs from frozen binding; no E5 load')
    def start(self):
        self.check_token_lifetime()
        if (self.out/'initial.json').exists(): raise RuntimeError('No implicit restart')
        static=self.static_placement()
        s=self.state() if static else self.idle(); settings=self.settings()
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
        if s['snapshot'].get('reservations') or any(a.get('status') in ('running','cancel') for a in s.get('actions',[])): raise RuntimeError('E5-3 boundary configuration is transitioning; do not invent a weaker static selection')
        if set(settings['autonomousWorkloads']) != set(self.uids): raise RuntimeError('Actual full-control E5-3 configuration required')
        if any(not chosen['routing'][u] for u in self.uids): raise RuntimeError('Selected canonical route missing')
        save(self.out/'static-selection.json',{'at':now(),'state':s,'fingerprint':chosen,'settings':settings,'selection':'Actual final E5-3 0.9Rw scheduler choice; no weak baseline selection'})
        self.route_ack(chosen,settings,'static','D9 actual E5-3 maximum normal 0.9Rw selected configuration, same instances/modes/routes')
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
            if fingerprint(state['snapshot'],self.uids)==chosen and self.io.idle(state):
                save(self.out/(prefix+'-native-ack.json'),result)
                save(self.out/(prefix+'-ready.json'),state)
                return state
            remaining=end-time.monotonic()
            if remaining<=0:raise RuntimeError('Selected physical configuration did not publish after route ACK')
            time.sleep(min(1,remaining))
    def restore(self):
        if not (self.out/'static-E5-6'/'collected.json').exists(): raise RuntimeError('Complete actual 170-minute static replay required before restoration')
        chosen=load(self.out/'static-selection.json')['fingerprint']
        if fingerprint(self.idle()['snapshot'],self.uids)!=chosen: raise RuntimeError('Actual selected static configuration changed before automatic restoration')
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
        sample=module('d2sample',D2/'sample_cgroups.py'); source=sample.REMOTE.replace("pod.startswith('d2-before-')", "pod.startswith('e5-')")
        children=[]
        for h in load(self.c['collection']['hosts']):
            argv=[sys.executable,'-u','-'] if h.get('local') else h['ssh']
            if not h.get('local') and argv[-3:] != ['python3','-u','-']: raise RuntimeError('Verified existing SSH python sampler argv required')
            output=(folder/(h['node']+'-cgroup.jsonl')).open('w'); error=(folder/(h['node']+'-collector.stderr')).open('w')
            p=subprocess.Popen(argv+[h['node'],str(seconds),self.a.stage],stdin=subprocess.PIPE,stdout=output,stderr=error,text=True); p.stdin.write(source); p.stdin.close(); children.append((p,output,error))
        return children
    def producer_job_name(self,name,stage):
        return 'e5-'+self.a.mode.lower()+'-'+stage['id'].lower()+'-'+str(SEEDS[name])
    def job(self, name, stage, folder):
        w=self.ws[name]; trace=stage['traces'][name]; job=load(w['jobTemplate']); label=self.producer_job_name(name,stage)
        job['metadata']={'name':label,'namespace':'chill-system','labels':{'chill.dacs.io/e5':self.a.mode}}
        spec=job['spec']; spec.pop('selector',None); spec.pop('manualSelector',None); spec['backoffLimit']=0; spec['activeDeadlineSeconds']=math.ceil(self.tb['setupSeconds']+self.tb['barrierSeconds']+stage['seconds']+self.tb['drainSeconds']); spec['template']['metadata']={'labels':{'chill.dacs.io/e5':self.a.mode}}
        pod=spec['template']['spec']; container=pod['containers'][0]; policy={'payload':self.b['payload'][name], 'arrivalSHA256':trace['arrivalSHA256'], 'offered':trace['offered'], 'barrierSeconds':self.tb['barrierSeconds'], 'segments': [{'rate':part['rates'][name], 'seconds':part['seconds']} for part in stage['parts']]}
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
        save(self.out / ('token-preflight-' + (stage['id'] if stage else 'start') + '.json'), record)
        return record
    def run(self):
        """Recheck frozen operating power policy before any stage can launch load."""
        st=self.c['smoke'] if self.a.command=='smoke' else next(x for x in self.b['schedule'] if x['id']==self.a.stage); folder=self.out/(self.a.mode+'-'+st['id'])
        if folder.exists(): raise RuntimeError('No implicit stage retry/overwrite')
        self.check_token_lifetime(st)
        self.check_power_observation()
        deferred_restore=self.a.mode=='full' and st['id']=='E5-4' and not (self.out/'restore-ack.json').exists()
        if self.a.command!='smoke':
            order = ['E5-0','E5-1','E5-2','E5-3','E5-4','E5-5','E5-6'] if self.a.mode=='full' else ['E5-1','E5-2','E5-3','E5-4','E5-5','E5-6'] if self.a.mode=='static' else ['E5-2','E5-5']
            index=order.index(st['id'])
            if index:
                previous=self.out/(self.a.mode+'-'+order[index-1])
                if not (previous/'collected.json').exists() or load(previous/'phase.json')['status']!='DRAINED_NOT_EVALUATED': raise RuntimeError('Previous normative row successful raw capture required')
            if deferred_restore and not (self.out/'static-E5-6'/'collected.json').exists(): raise RuntimeError('Actual 170min static replay precedes full E5-4')
        if self.a.mode=='static' and st['id']=='E5-0': raise RuntimeError('Static baseline replays E5-1..6 only')
        if self.a.mode=='static':
            ready=load(self.out/'static-ready.json'); chosen=load(self.out/'static-selection.json')['fingerprint']
            if self.settings()['autonomousWorkloads'] or fingerprint(self.idle()['snapshot'],self.uids)!=chosen: raise RuntimeError('Actual selected static freeze drift')
        elif self.a.mode not in ('full','smoke'):
            factor=next(f for f in self.c['factors'] if f['name']==self.a.mode)
            if not st['id'].startswith(('E5-2','E5-5')): raise RuntimeError('Factor comparison is exactly E5-2/E5-5')
            if self.settings()['controls']!=factor['controls']: raise RuntimeError('Factor source controls not actually active')
        static=self.static_placement()
        if static:
            if self.settings().get('autonomousWorkloads'):raise RuntimeError('Static placement requires autonomy to remain off')
        elif self.a.mode in ('full','smoke') and not deferred_restore and set(self.settings()['autonomousWorkloads'])!=set(self.uids): raise RuntimeError('Actual canonical-three autonomous control required')
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
        self.boundary(folder,'pre'); children=self.sampler(folder,self.tb['setupSeconds']+st['seconds']+self.tb['drainSeconds']+self.tb['flushSeconds']); jobs=[]; streams=[]; phase={'stage':st,'mode':self.a.mode,'status':'RUNNING','startUTC':None,'endUTC':None,'starts':{}}
        if barrier is not None:phase['startBarrier']=barrier
        self.native_evidence=(load(folder/'pre-state.json').get('results') or [])+[load(path) for root in (self.out,self.out.parent/'smoke-run') for path in root.glob('*-mode/result.json')]
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
                        log.write(line+'\n');log.flush();row=json.loads(line)
                        if row.get('e5Event')=='ready':
                            ready.add(name); jobname=next(j for n,j,u in jobs if n==name); found=self.io.k(['get','pods','-l','job-name='+jobname,'-o','json'])['items']
                            uid=next(u for n,j,u in jobs if n==name)
                            if len(found)!=1 or found[0]['metadata']['uid']!=pods[name]['metadata']['uid'] or not any(owner.get('kind')=='Job' and owner.get('uid')==uid for owner in found[0]['metadata'].get('ownerReferences',[])): raise RuntimeError('Producer exact Pod/Job identity changed after startup')
                            if row.get('payload')!=self.b['payload'][name] or row.get('offered')!=st['traces'][name]['offered'] or row.get('arrivalSHA256')!=st['traces'][name]['arrivalSHA256'] or row.get('barrierSeconds')!=self.tb['barrierSeconds']:raise RuntimeError('Actual ready payload/trace/barrier differs from frozen input')
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
                    coverage=Counter()
                    for path in folder.glob('*-cgroup.jsonl'):
                        for line in path.read_text().splitlines():
                            observed=json.loads(line)
                            if observed.get('kind')=='collector-error': raise RuntimeError('Prestart telemetry collector error')
                            if observed.get('kind')=='cgroup' and all(observed.get(k) is not None for k in ('cpuUsageNanos','nrThrottled','throttledNanos')): coverage[(observed['role'],observed.get('pod'))]+=1
                    if not all(any(role==r and count>=2 for (role,pod),count in coverage.items()) for r in ('server','edgemesh')) or not all(coverage[('client',pod['metadata']['name'])]>=2 for pod in pods.values()): continue
                    for name,pod in pods.items():
                        current=self.io.k(['get','pod',pod['metadata']['name'],'-o','json'],timeout=self.tb['apiSeconds'])
                        self.io.assert_producer_startable(folder/('startup-'+name),current,pod,barrier_seconds=self.tb['barrierSeconds'])
                    if deferred_restore:
                        self.restore()
                        frame=self.state();chosen=load(self.out/'static-selection.json')['fingerprint']
                        if fingerprint(frame['snapshot'],self.uids)!=chosen or frame['snapshot'].get('reservations'):raise RuntimeError('Selected static configuration was not retained at actual E5-4 producer release')
                        save(folder/'actual-start-configuration.json',frame)
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
            else:self.idle();phase['drainedUTC']=now()
            self.boundary(folder,'post'); phase['status']='DRAINED_NOT_EVALUATED'; save(folder/'phase.json',phase)
            collector_status=[]
            for sampler,output,error in children:
                intentional=sampler.poll() is None
                if intentional:sampler.terminate()
                code=sampler.wait(timeout=15);output.flush();error.flush()
                collector_status.append({'output':output.name,'stderr':error.name,'exitCode':code,'intentionalStopAfterActualDrain':intentional})
            save(folder/'collector-status.json',collector_status)
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
        missing={}
        if captured.get('status')!='CAPTURED_NOT_EVALUATED':
            errors=captured.get('errors') or []
            for error in errors:
                name=error.split(': ',1)[0]
                if name in tolerated:missing[name]=error
            if not errors or len(missing)==0 or any(error.split(': ',1)[0] not in tolerated for error in errors) or not load(folder/'raw-source-index.json').get('complete'):
                raise RuntimeError('Required exact-hour archive/current capture incomplete; preserve private index and stop')
        for suffix in ('control-alerts.jsonl','cgmon.log','upload.log'):
            path=CAMPAIGN/suffix
            if path.exists(): (folder/suffix).write_bytes(path.read_bytes())
        pods=self.io.k(['get','pods','-l','app.kubernetes.io/component=serving-entrypoint','-o','json'])['items']
        if len(pods)!=1: raise RuntimeError('Actual server logs ambiguous')
        r=subprocess.run(self.d.K+['logs',pods[0]['metadata']['name'],'-c',self.c['collection']['serverContainer'],'--since-time='+start,'--timestamps=true'],capture_output=True,text=True,timeout=self.tb['apiSeconds']); (folder/'server.log').write_text(r.stdout)
        if r.returncode: raise RuntimeError('Server GC/Tick log capture failed')
        manifest={'schema':'e5-private-collection.v1','visibility':'PRIVATE_OPERATIONAL_ONLY; exclude original raw and sensitive configuration from git/public evidence indexes','at':now(),'status':'CAPTURED_NOT_EVALUATED','raw':[],'observationCapture':str(folder/'raw-source-index.json'),'archiveRequired':load(self.c['collection']['archiveRefs']),'bindingSources':self.b['sources'],'physicalIdentities':'pre/post Pods, Node UID, containerID/imageID, profile/runtime/allocation, full scheduler events','cadence':{'cgroupSeconds':1,'restartSeconds':15,'thermalSeconds':5},'streams':['definitions','requests','management','scheduler','catalog']}
        if missing:
            manifest.update(status='CAPTURED_WITH_MISSING_ARCHIVES',missingArchives=missing,archiveStatus=captured['status'])
        for p in sorted(folder.rglob('*')):
            if p.is_file() and p.name!='collected.json': manifest['raw'].append(ref(p,allow_empty=True))
        save(folder/'collected.json',manifest)
    def factor(self):
        f=next(f for f in self.c['factors'] if f['name']==self.a.mode)
        if self.a.stage not in ('E5-2','E5-5'): raise RuntimeError('Factor reset is one actual paired E5-2/E5-5 block')
        baseline=load(self.out/('full-'+self.a.stage)/'pre-state.json')['snapshot']
        desired=fingerprint(baseline,self.uids); profiles=self.api('GET','/api/v1/scheduler/profiles')['preparations']
        for target in desired['instances']:
            default=profiles[target['profileID']]['instance']
            if any(target[key]!=default[key] for key in ('node','allocation','maxBatch','maxWaitNanos')): raise RuntimeError('Selected factor initial state needs unsupported manual cap/wait/resource override; no generic reset is claimed')
        self.clear_owned('factor-'+f['name']+'-'+self.a.stage)
        current=self.idle()
        for node in current['snapshot']['nodes']:
            if node['node']['name'] in desired['modes'] and node['powerMode']!=desired['modes'][node['node']['name']]: self.native_mode(node['node'],desired['modes'][node['node']['name']],'factor-'+f['name']+'-'+self.a.stage)
        for uid in self.uids:
            self.operation({'kind':'unroute','workloadUID':uid,'reason':'Keep paired factor preparations unloaded until exact baseline route ACK'},'factor-'+f['name']+'-'+self.a.stage+'-unroute-'+uid)
        mapping={}
        for target in desired['instances']:
            result=self.io.api('POST','/api/v1/scheduler/operations',{'kind':'prepare','workloadUID':target['workloadUID'],'profileID':target['profileID'],'node':target['node'],'reason':'Restore actual paired factor baseline'},timeout=self.tb['settleSeconds'])
            if result.get('status')!='complete': raise RuntimeError('Source-supported factor preparation incomplete')
            save(self.out/('factor-'+f['name']+'-'+self.a.stage+'-prepare-'+target['node']['name']+'.json'),result)
            created=[i for i in result['actual']['instances'] if i.get('node')==target['node'] and i.get('ownerWorkloadUID')==target['workloadUID'] and i.get('profile')==target['profileID']]
            if len(created)!=1:raise RuntimeError('Native prepare completion lacks its exact Node/profile/owner physical identity')
            identifier=created[0]['id'];policy=result['actual']['native'][identifier]['metadata']['policy']
            need(policy,['profileID','maxBatchSize','maxBatchWaitMS'],'Actual completed native prepare policy')
            if policy['profileID']!=target['profileID'] or policy['maxBatchSize']!=target['maxBatch'] or round(policy['maxBatchWaitMS']*1e6)!=target['maxWaitNanos']:raise RuntimeError('Native prepared profile/cap/wait differs from exact paired target')
            deadline=time.monotonic()+self.tb['settleSeconds']
            while time.monotonic()<deadline:
                state=self.idle();matches=[i for i in instance_rows(state['snapshot']['instances']) if i['id']==identifier]
                if not matches:time.sleep(1);continue
                instance=matches[0]
                if instance['node']!=target['node'] or instance.get('profile',{}).get('id')!=target['profileID'] or instance.get('ownerWorkloadUID')!=target['workloadUID'] or instance.get('workloadUID') or any(instance.get(key)!=target[key] for key in ('allocation','maxBatch','maxWaitNanos')):raise RuntimeError('Fresh prepared projection has a hard identity/cap/wait/allocation or unexpected served-UID mismatch')
                mapping[target['id']]=identifier;break
            else:raise RuntimeError('Exact native prepared identity absent from fresh snapshot within existing settle timebox')
        prepared=self.idle();actual=prepared['snapshot']
        if {i['id'] for i in instance_rows(actual['instances'])}!=set(mapping.values()) or any(i.get('workloadUID') for i in instance_rows(actual['instances'])):raise RuntimeError('Paired prepared state is not exactly the returned physical IDs with empty served UID')
        # This is a declared target, not a claim that unrouted prepared instances already serve a UID.
        chosen=owned_target(actual,self.uids,{uid:{mapping[old]:weight for old,weight in route.items()} for uid,route in desired['routing'].items()})
        save(self.out/('factor-'+f['name']+'-'+self.a.stage+'-prepared-target.json'),{'status':'PREPARED_UNROUTED_NOT_LOAD_READY','actualState':prepared,'intendedTargetBinding':chosen})
        self.route_ack(chosen,self.settings(),'factor-'+f['name']+'-'+self.a.stage,'Restore actual paired initial block through public prepare and route operations')
        self.release_routing('factor-'+f['name']+'-'+self.a.stage+'-release','Release paired baseline routing before autonomous factor')
        settings=self.settings(); settings['controls']=f['controls']; settings['autonomousWorkloads']=self.uids
        self.authorize_native_modes()
        save(self.out/('factor-'+f['name']+'-'+self.a.stage+'-ack.json'),self.put(settings))
    def clear_owned(self,prefix):
        if self.b.get('schema') == 'demo-e5-binding.v1':
            settings = self.settings()
            settings['autonomousWorkloads'] = [uid for uid in settings.get('autonomousWorkloads', []) if uid not in self.uids]
            save(self.out/(prefix+'-reset-autonomy-ack.json'), self.put(settings))
            started = time.monotonic()
            deadline = started + self.tb['settleSeconds']
            observations = []
            while True:
                state = self.state()
                reservations = len(state['snapshot'].get('reservations') or [])
                transitions = sum(action.get('status') in ('running', 'cancel') for action in state.get('actions') or [])
                elapsed = time.monotonic() - started
                observations.append({'at': now(), 'elapsedSeconds': elapsed, 'reservations': reservations, 'transitions': transitions})
                remaining = deadline - time.monotonic()
                if not reservations and not transitions or remaining <= 0:
                    save(self.out/(prefix+'-reset-wait.json'), {'timeboxSeconds': self.tb['settleSeconds'], 'timedOut': bool(reservations or transitions), 'observations': observations, 'state': state, 'meaning': 'Autonomy off; bounded public reservation/action wait, then one reset only (no automatic retry)'})
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
            j=load(p); name=j['metadata']['name']; uid=j['metadata']['uid']; phase=p.parent/'phase.json'
            stops=(load(phase).get('ownedProducerStops') or []) if phase.exists() else []
            if any(stop.get('name')==name and stop.get('uid')==uid and stop.get('stopped') is True for stop in stops):continue
            actual=self.io.k(['get','job',name,'-o','json'])
            if actual['metadata']['uid']!=uid: raise RuntimeError('Owned producer UID changed')
            self.io.remove_job(name,uid)
            if phase.exists():
                record=load(phase);record.setdefault('ownedProducerStops',[]).append({'name':name,'uid':uid,'stopped':True});save(phase,record)
    def factor_restore(self):
        self.idle(); settings=self.settings(); initial=load(self.c['inputs']['scheduler'])['settings']; settings['controls']=initial['controls']; save(self.out/'factor-restored.json',self.put(settings))
    def finish(self):
        if self.static_placement():
            save(self.out/'finish-state.json',self.state())
            save(self.out/'finish-complete.json',{'at':now(),'staticPlacement':True,'meaning':'Existing manual placement retained; no scheduler mutation'})
            return
        initial=load(self.out/'initial.json')
        result=self.clear_owned('finish')
        settings=copy.deepcopy(initial['settings'])
        settings['autonomousWorkloads']=[uid for uid in settings.get('autonomousWorkloads',[]) if uid not in self.uids]
        save(self.out/'finish-settings-ack.json',self.put(settings))
        save(self.out/'finish-state.json',self.state())
        save(self.out/'finish-complete.json',{'at':now(),'canonicalInstances':0,'originalSettingsRestored':True,'resetStatus':result['status'],'powerReturn':'Server reset minimum supported idle mode; original physical modes are not recreated'})
    def campaign(self):
        self.start()
        def row(mode, name):
            self.a.mode=mode; self.a.stage=name; self.a.command='stage'
            self.run()
            result=judge(self.a.binding,self.out/(mode+'-'+name))
            if result['status']=='NOT_READY': raise RuntimeError('Required row evidence incomplete; frozen campaign stopped without advancing')
            save(self.out/'campaign-position.json',{'at':now(),'mode':mode,'lastCapturedRow':name,'judgment':result['status'],'approval':str(self.a.release),'scientificAccounting':'Row evidence only; fault scenario unexercised'})
        for name in ('E5-0','E5-1','E5-2','E5-3'): row('full',name)
        for name in ('E5-1','E5-2','E5-3','E5-4','E5-5','E5-6'): row('static',name)
        # Keep the selected physical configuration fixed while E5-4 producers prepare; restore once at their release barrier.
        for name in ('E5-4','E5-5','E5-6'): row('full',name)
        for f in self.c['factors']:
            self.a.mode=f['name']
            for name in ('E5-2','E5-5'):
                self.a.stage=name; self.factor(); row(f['name'],name); self.factor_restore()
        self.finish(); self.cleanup_jobs(); summarize(self.a.binding,self.out);collection_manifest(self.out,self.a.binding)


def judge(binding, folder):
    b=load_binding(binding); c=b['config']; phase=load(folder/'phase.json'); st=phase['stage']; d=module('d2analyze',D2/'analyze_before.py'); inp=d.Inputs(folder); unknown=[]; results={}
    raw=module('e5raw',HERE/'e5-raw.py'); events,paging=raw.scheduler_events(folder)
    if not paging.get('complete'): unknown.extend(paging.get('errors') or ['Scheduler raw capture incomplete'])
    events=[e for e in events if d.epoch(e.get('at')) is not None and d.epoch(phase['startUTC'])<=d.epoch(e['at'])<=d.epoch(phase['drainedUTC'])]
    sched=d.scheduler_summary(events); searches=[]; final_indexes={}
    tick_cpu=[]; samples=[]
    for path in folder.glob('*-cgroup.jsonl'):
        samples.extend(r for r in inp.jsonlines(path) if r.get('kind')=='cgroup' and r.get('role')=='server')
    for e in events:
        decision=e.get('decision') or {}; at=d.epoch(e.get('at')); elapsed=d.number(decision.get('evaluationElapsedNanos'))
        at=d.epoch(decision.get('at'))
        if at is not None and elapsed is not None:
            finish=at+elapsed/1e9
            if (decision.get('capacity') or {}).get('search'): searches.append((at,finish))
            prior=[r for r in samples if d.epoch(r['at']) is not None and d.epoch(r['at'])<=at]
            after=[r for r in samples if d.epoch(r['at']) is not None and d.epoch(r['at'])>=finish]
            left=max(prior,key=lambda r:r['at']) if prior else None; right=min(after,key=lambda r:r['at']) if after else None
            same=left and right and left.get('containerID')==right.get('containerID')
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
        offered=st['traces'][w['name']]['offered']; completed=[]; residual=[]; tails={'search':[],'outsideSearch':[]}; minutes=defaultdict(lambda:{'offered':0,'deadlineSuccess':0,'outcomes':Counter()}); success=0; used=set(); native_missing=[]; observed_outcomes=Counter();unsent=0
        for index,offset in enumerate(st['traces'][w['name']]['offsetsNS']): minutes[offset//60000000000]['offered']+=1
        for x in client:
            key=x.get('requestID'); row=finals.get(key); minute=x['scheduledOffsetNs']//60000000000
            if key and key in used: unknown.append(w['name']+': duplicate gateway request ID'); continue
            if key:used.add(key)
            no_send=x.get('error')=='UnsentAtWindowEnd';unsent+=int(no_send)
            outcome=row.get('outcome') if row else 'unsent-at-window-end' if no_send else 'unfinished-or-no-final'; minutes[minute]['outcomes'][outcome]+=1;observed_outcomes[outcome]+=1
            if row and row.get('definitionID')!=w['definitionID']: unknown.append(w['name']+': exact frozen definition identity mismatch')
            if row and row.get('outcome')=='completed' and row.get('executionComplete') and positive(row.get('elapsedNanos')):
                elapsed=row['elapsedNanos']; completed.append(elapsed)
                if elapsed<=w['slo']['targetLatencyNanos']: success+=1; minutes[minute]['deadlineSuccess']+=1
                if row.get('nativeElapsedMS') is not None and row.get('dispatchedNanos') is not None: residual.append(elapsed-row['dispatchedNanos']-row['nativeElapsedMS']*1e6)
                else: native_missing.append(key)
                if row.get('nativeElapsedMS') is not None:
                    tail=elapsed-row['nativeElapsedMS']*1e6; at=d.epoch(row['receivedAt']); tails['search' if during_search(at) else 'outsideSearch'].append(tail)
        unfinished=max(0,offered-len(client))+unsent+sum(not finals.get(x.get('requestID')) and x.get('error')!='UnsentAtWindowEnd' for x in client); pct=w['slo']['percentile']; rank=math.ceil(offered*pct/100); deadline_rate=success/offered if offered else None
        issued=[x for x in client if x.get('error')!='UnsentAtWindowEnd']
        if len(client)!=offered or len(used)!=len(issued) or any(not x.get('requestID') or x['requestID'] not in finals for x in issued) or indexed['errors'] or summary['finalConflicts'] or summary['unkeyedRows'] or summary['withoutUnambiguousHTTPFinal']: unknown.append(w['name']+': offered/final population incomplete or conflicting')
        unknown.extend(w['name']+': received cohort: '+str(error) for error in received_indexes[w['uid']]['errors'])
        if {x.get('requestIndex') for x in client}!=set(range(offered)) or any(type(x.get('requestIndex')) is not int or not 0<=x['requestIndex']<offered or x['scheduledOffsetNs']!=st['traces'][w['name']]['offsetsNS'][x['requestIndex']] or x.get('record')!=b['payload'][w['name']][x['requestIndex']%len(b['payload'][w['name']])]['recordID'] for x in client):unknown.append(w['name']+': actual request index/arrival/payload assignment differs from frozen trace')
        if native_missing: unknown.append(w['name']+': monotonic residual native/dispatched fields missing')
        normal=not st['id'].startswith('E5-4')
        results[w['name']]={'offered':offered,'clientRows':len(client),'unfinishedOrUnsent':unfinished,'outcomes':summary['outcomes'],'completedLatencyNS':d.stats(completed),'completionResidualNS':d.stats(residual),'marginNS':c['mSeconds']*1e9,'gatewayTailNS':{k:d.stats(v) for k,v in tails.items()},'deadlineSuccess':success,'deadlineSuccessFraction':deadline_rate,'requiredSuccessfulRank':rank,'D1':'UNMEASURED' if offered==0 else 'FAIL' if normal and success<rank else 'OBSERVED_THRESHOLD_MET' if normal else 'OVERLOAD_DESCRIPTIVE','minutes':dict(minutes),'nativeResidualMissingIDs':native_missing,'rawPopulation':received_indexes[w['uid']]['summary'],'offeredResponsePopulation':summary,'pages':pages}
        results[w['name']]['outcomes']=dict(observed_outcomes);results[w['name']]['gatewayFinalOutcomes']=received_indexes[w['uid']]['summary']['outcomes'];results[w['name']]['offeredGatewayFinalOutcomes']=summary['outcomes'];results[w['name']]['unsentAtWindowEnd']=unsent;results[w['name']]['actualGatewayIdentities']=len(used)
        results[w['name']]['issuanceFidelity']=d.issuance_fidelity(client,st['traces'][w['name']]['offsetsNS'],phase['starts'].get(w['name'],{}).get('monotonicNS'),round(st['seconds']*1e9),d.stats)
        if results[w['name']]['issuanceFidelity']['status']=='UNKNOWN':unknown.append(w['name']+': issuance fidelity unknown')
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
                r=finals.get(x.get('requestID')); outcomes[r.get('outcome') if r else 'unsent-at-window-end' if x.get('error')=='UnsentAtWindowEnd' else 'unfinished-or-no-final']+=1
                if r and r.get('outcome')=='completed' and r.get('executionComplete') and positive(r.get('elapsedNanos')): good.append(r['elapsedNanos'])
            hits=sum(x<=w['slo']['targetLatencyNanos'] for x in good); required=math.ceil(len(offsets)*pct/100)
            sub.append({'id':part['id'],'offered':len(offsets),'outcomes':dict(outcomes),'unfinishedOrUnsent':max(0,len(offsets)-len(selected)),'completedLatencyNS':d.stats(good),'deadlineSuccess':hits,'requiredSuccessfulRank':required,'D1':'UNMEASURED' if not offsets else 'FAIL' if normal and hits<required else 'OBSERVED_THRESHOLD_MET' if normal else 'OVERLOAD_DESCRIPTIVE','rate':part['rates'][w['name']],'intendedRate':part['intendedRates'][w['name']],'PLCRemainingG':part['PLCRemainingG'],'PLCClippedBy':part['PLCClippedBy']}); base=stop
            sub[-1]['unfinishedOrUnsent']+=sum(not finals.get(x.get('requestID')) for x in selected)
        results[w['name']]['substages']=sub
        if any(x['D1']=='FAIL' for x in sub): results[w['name']]['D1']='FAIL'
    start,end=d.epoch(phase['startUTC']),d.epoch(phase['endUTC']); cg=d.telemetry(inp,start,end); power=inp.json(folder/'power.json')
    bootstrap=None
    if st['id']=='E5-0':
        post=inp.json(folder/'post-state.json')
        bootstrap={w['name']:[i for i in instance_rows((post or {}).get('snapshot',{}).get('instances',[])) if i.get('workloadUID')==w['uid'] and i.get('ready')] for w in c['workloads']}
        if any(not instances for instances in bootstrap.values()):unknown.append('E5-0 lacks an actual Ready first instance for each canonical Workload')
    if not power or not power.get('complete') or power.get('wholePoolJoules') is None or power.get('activePoolJoules') is None: unknown.append('Whole/active POOL transition-inclusive power incomplete')
    tail_power=inp.json(folder/'power-drain-inclusive.json')
    if not tail_power or not tail_power.get('complete') or tail_power.get('wholePoolJoules') is None or tail_power.get('activePoolJoules') is None: unknown.append('Actual transition/request/action drain-inclusive energy incomplete')
    if not paging.get('complete'): unknown.append('Scheduler raw capture incomplete')
    boot_events=[]; power_actions=[];safety=module('e5safety',D2/'conduct_before.py')
    bound_nodes=[{'name':node['metadata']['name'],'uid':node['metadata']['uid']} for node in load(c['inputs']['nodes'])['items'] if node['metadata']['name'] in NODES]
    for event in events:
        proof=(event.get('action') or {}).get('actual') or {}
        if isinstance(proof,dict) and (proof.get('operation') or {}).get('kind')=='power':power_actions.append(proof)
    notices=folder/'node-actuation-boot-alerts.jsonl'
    if notices.exists():
        for notice in inp.jsonlines(notices):
            immediate=notice['decision'];completion=None;matched=[]
            if immediate.get('requiresPostDrainCompletion'):
                requested=d.epoch(immediate.get('requestedAt'))
                matched=[proof for proof in power_actions if proof['operation'].get('node')==immediate['node'] and proof['operation'].get('powerMode')==immediate['targetMode'] and requested is not None and d.epoch(proof.get('startedAt')) is not None and d.epoch(proof.get('endedAt')) is not None and d.epoch(proof['startedAt'])<=requested<=d.epoch(proof['endedAt'])]
                completion=safety.control_alert_decision(notice['row'],matched,bound_nodes)
                if completion['stop']:unknown.append('Immediately correlated in-progress native boot lacks its actual completed durable same-Node/mode operation; no completion invented')
            boot_events.append({'raw':notice['row'],'immediateDecision':immediate,'durableCompletionDecision':completion,'matchingActualPowerResults':matched})
    if cg['collectorErrors'] or cg['unparsed']: unknown.append('Collector errors/unparsed telemetry')
    collector_status=inp.json(folder/'collector-status.json')
    if not collector_status or any(row['exitCode'] not in (0,-15) or row['exitCode']==-15 and not row['intentionalStopAfterActualDrain'] for row in collector_status):unknown.append('Required host collector exited unexpectedly; transport/reboot gap is not filled with zero')
    for role in ('server','edgemesh','native'):
        if cg['roles'][role]['nrThrottledDelta'] is None: unknown.append(role+' telemetry missing')
    thermals=[]
    for p in folder.glob('*-cgroup.jsonl'):
        for row in inp.jsonlines(p):
            if row.get('kind')=='thermal':
                for z in row['zones']:
                    t=z.get('millidegreesC'); temp=t/1000 if t is not None else None
                    thermals.append({'at':row['at'],'node':row['node'],'zone':z,'bin':'unknown' if temp is None else '<60' if temp<60 else '60-70' if temp<70 else '70-80' if temp<80 else '>=80','active':row.get('activeThrottle'),'latched':row.get('latchedThrottle')})
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
    for r in load(c['collection']['archiveRefs']):
        try: ref(r['localPath'])
        except (OSError, ValueError) as exc: unknown.append('Required raw archive '+r['key']+': '+str(exc))
    if not (folder/'server.log').exists() or not (folder/'collected.json').exists(): unknown.append('Full raw collection/GC logs missing')
    if inp.errors: unknown.append('Raw read/parse errors')
    result={'schema':'e5-judgment.v1','visibility':'PRIVATE_OPERATIONAL_ONLY; raw references/configuration excluded from public indexes','at':now(),'status':'NOT_READY' if unknown or phase['status']!='DRAINED_NOT_EVALUATED' else 'FAIL' if any(r['D1']=='FAIL' for r in results.values()) else 'MEASURED_DESCRIPTIVE','notReadyReasons':unknown,'requests':results,'Tick':sched,'cgroups':cg,'power':power,'thermal':thermals,'D6':'Observed thermal bins only; unknown active/latched remains unknown, profile slack must use bound source samples','rawErrors':inp.errors,'sources':inp.sources,'limitations':b['limitations'],'accounting':{'specScenarioDenominator':7,'planRowDenominator':8,'failureScenario':'UNEXERCISED; never completed by E5 schedule'}}
    if phase.get('mode')=='smoke':
        result['scientificResults']='UNMEASURED; operational smoke never fills E5 rows'
        if result['status']=='MEASURED_DESCRIPTIVE': result['status']='OPERATIONAL_SMOKE_COMPLETE'
    result['TickCPUBrackets']=tick_cpu
    result['nativeAgentRestartObservations']=boot_events
    result['canonicalFirstInstances']=bootstrap
    result['D6ProfileSlack']=thermal_slack
    result['searchWindows']={'intervals':searches,'meaning':'Nominal source decision.at through monotonic evaluationElapsed mapped onto wall time; clock/capture offset is retained as uncertainty, not exact CPU attribution'}
    gc_cpu=[]; gc_records=[]
    if (folder/'server.log').exists():
        for line in (folder/'server.log').read_text().splitlines():
            at=d.epoch(line.split(' ',1)[0])
            match=re.search(r'ms clock, (.*?) ms cpu,',line)
            if match and at is not None and start<=at<=end:
                values=[float(x) for x in re.findall(r'\d+(?:\.\d+)?',match.group(1))]
                gc_cpu.append(sum(values)); gc_records.append(line)
    result['GC']={'records':gc_records,'cpuMS':d.stats(gc_cpu),'capturedTotalCPUMS':sum(gc_cpu) if gc_cpu else None,'scope':'GC log emission timestamps within actual offered window; a boundary-straddling GC is not phase-attributed','meaning':'Preserved gctrace=1 phase CPU terms only; absent events remain unknown, not zero. Operational smoke need not force a GC.'}
    if not gc_records and phase.get('mode')!='smoke':
        result['status']='NOT_READY';result['notReadyReasons'].append('Actual enabled gctrace GC CPU records missing; unknown is not zero')
    decisions=[x['decision'] for x in sched['records']]
    result['intendedOverload']={'requested':st['id']=='E5-4','sourceOverloadDecisionObserved':any(w.get('state')=='Overload' for decision in decisions for w in decision.get('workloads',[])),'actualUnsent':sum(r['unsentAtWindowEnd'] for r in results.values()),'meaning':'Report actual source judgment and unissued offered arrivals explicitly; an intended 1.2R label is not proof that all overload reached the gateway.'}
    result['searchBudget']={'budgetCutDecisions':sum(bool(((x.get('capacity') or {}).get('search') or {}).get('budgetCut')) for x in decisions),'gainWithinUncertaintyDecisions':sum('TransitionGainWithinUncertainty' in json.dumps(x) for x in decisions),'rawSearch':[(x.get('capacity') or {}).get('search') for x in decisions]}
    result['PVCUsage']={key:inp.json(folder/(key+'-pvc-usage.json')) for key in ('pre','post')}
    result['powerDrainInclusive']=tail_power
    violations=[]
    for event in events:
        counts=Counter(i['node']['uid'] for i in instance_rows((event.get('snapshot') or {}).get('instances',[])))
        if any(count>1 for count in counts.values()): violations.append({'at':event.get('at'),'counts':dict(counts)})
    result['oneInstancePerNodeViolations']=violations
    if violations and result['status']!='NOT_READY': result['status']='FAIL'
    if inp.errors:
        result['status']='NOT_READY'
        result['notReadyReasons'].append('Required boundary/raw source missing or unreadable')
    save(folder/'judgment.json',result); return result


def summarize(binding, root):
    b=load_binding(binding); stages={}; missing=[]
    modes={'full':[x['id'] for x in b['schedule']], 'static':[x['id'] for x in b['schedule'] if x['id']!='E5-0']}
    modes.update({f['name']:['E5-2','E5-5'] for f in b['config']['factors']})
    for mode, ids in modes.items():
        for row_id in ids:
            folder=root/(mode+'-'+row_id)
            try:
                value=load(folder/'judgment.json')
                if value['status'] not in ('MEASURED_DESCRIPTIVE','FAIL'): raise ValueError('Judgment not scientifically evaluable')
                stages[(mode,row_id)]=value
            except (OSError,ValueError,KeyError) as exc: missing.append({'mode':mode,'row':row_id,'reason':str(exc)})
    comparisons=[]
    for row_id in modes['static']:
        full=stages.get(('full',row_id)); static=stages.get(('static',row_id))
        if full and static:
            f=load(root/('full-'+row_id)/'phase.json'); s=load(root/('static-'+row_id)/'phase.json')
            if f['stage']['traces'] != s['stage']['traces']: missing.append({'row':row_id,'reason':'Paired frozen traces differ'}); continue
            comparisons.append({'row':row_id,'fullWholePOOLJ':full['power']['wholePoolJoules'],'staticWholePOOLJ':static['power']['wholePoolJoules'],'staticMinusFullWholePOOLJ':static['power']['wholePoolJoules']-full['power']['wholePoolJoules'],'fullActivePOOLJ':full['power']['activePoolJoules'],'staticActivePOOLJ':static['power']['activePoolJoules'],'requestMetrics':{'full':full['requests'],'static':static['requests']}})
            full_tail=load(root/('full-'+row_id)/'power-drain-inclusive.json'); static_tail=load(root/('static-'+row_id)/'power-drain-inclusive.json')
            comparisons[-1]['drainInclusive']={'full':full_tail,'static':static_tail,'meaning':'Separate actual start-through-action/native/request-drain horizon; nominal170min totals above retain the offered windows.'}
    plan_rows={row_id:('MEASURED' if ('full',row_id) in stages else 'UNMEASURED') for row_id in modes['full']}
    plan_rows['E5-S']='MEASURED' if len(comparisons)==6 and (root/'static-native-ack.json').exists() else 'UNMEASURED'
    spec={'steady':'E5-1','gradual-up-and-down':'E5-3','burst':'E5-2','post-transition-drop-resurge':'E5-5','mixed-heterogeneous':'E5-1','pool-overload':'E5-4'}
    scenario={name:'MEASURED' if plan_rows[row]=='MEASURED' and (name!='gradual-up-and-down' or plan_rows['E5-5']=='MEASURED') else 'UNMEASURED' for name,row in spec.items()}
    scenario['node-serving-control-failure']='UNEXERCISED'
    factor_results={f['name']:{row:stages.get((f['name'],row)) for row in ('E5-2','E5-5')} for f in b['config']['factors']}
    result={'schema':'e5-summary.v1','at':now(),'status':'NOT_READY' if missing else 'MEASURED_DESCRIPTIVE','missing':missing,'planRows':plan_rows,'planResultLinks':str(sum(x=='MEASURED' for x in plan_rows.values()))+'/8','specScenarios':scenario,'specResultLinks':str(sum(x=='MEASURED' for x in scenario.values()))+'/7','failureScope':'No fault injection. Failure scenario remains uncompleted.','pairedComparisons':comparisons,'fullMatched170minWholePOOLJ':sum(x['fullWholePOOLJ'] for x in comparisons) if len(comparisons)==6 else None,'static170minWholePOOLJ':sum(x['staticWholePOOLJ'] for x in comparisons) if len(comparisons)==6 else None,'controlFactors':factor_results,'limitations':b['limitations'],'staticSelection':str(root/'static-selection.json'),'accounting':{'d':b['accounting']['d'],'exactCost':b['accounting']['exactCost'],'evaluation':str(sum(x=='MEASURED' for x in scenario.values()))+'/7'}}
    result['schedulerSettings']=b.get('schedulerSettings')
    if result['schedulerSettings'] is None:
        result['status']='NOT_READY';result['missing'].append({'reason':'Binding does not contain frozen scheduler settings; current settings are not substituted'})
    result['visibility']='PRIVATE_OPERATIONAL_ONLY; original raw/configuration and their paths excluded from public evidence indexes'
    result['rowJudgmentCounts']=dict(Counter(value['status'] for value in stages.values()))
    result['measurementMeaning']='Measured result links are not criterion PASS; negative D1/source overload findings remain visible.'
    result['issuanceFidelity']={mode+'-'+row:{name:metrics.get('issuanceFidelity') for name,metrics in value['requests'].items()} for (mode,row),value in stages.items()}
    restored=load(root/'finish-complete.json') if (root/'finish-complete.json').exists() else None
    result['physicalRestoration']=restored
    if not restored or restored.get('originalSettingsRestored') is not True or restored.get('resetStatus')!='complete':
        result['status']='NOT_READY';result['missing'].append({'reason':'Server reset and input settings restoration incomplete'})
    save(root/'e5-summary.json',result); return result


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
    for folder in sorted(root.iterdir()):
        if not folder.is_dir() or not (folder/'phase.json').exists():continue
        phase=load(folder/'phase.json');judgment=load(folder/'judgment.json')
        requests={}
        fields=('offered','clientRows','unfinishedOrUnsent','outcomes','completedLatencyNS','completionResidualNS','gatewayTailNS','deadlineSuccess','deadlineSuccessFraction','requiredSuccessfulRank','D1','unsentAtWindowEnd','actualGatewayIdentities','gatewayFinalOutcomes','offeredGatewayFinalOutcomes','selectedResidualPercentile')
        for name,value in judgment['requests'].items():
            requests[name]={key:value[key] for key in fields if key in value}
            requests[name]['issuanceFidelity']=value.get('issuanceFidelity')
            requests[name].update(successNumerator=value['deadlineSuccess'],successDenominator=value['offered'],population='Frozen offered arrivals; final outcome and successful execution populations remain separate')
            population_fields=('rawRows','populationUniqueRows','duplicateRows','kinds','uniqueRequests','uniqueFinalRows','outcomes','executionComplete','completed','withoutUnambiguousHTTPFinal','nativeSupplementRows','nativeOutcomes','complete')
            for key in ('rawPopulation','offeredResponsePopulation'):
                population=value.get(key) or {}
                requests[name][key]={field:population[field] for field in population_fields if field in population}
            requests[name]['missingReasons']={'deadlineSuccessFraction':'No offered arrivals' if not value['offered'] else None,'completedLatencyNS':'No successful execution-complete responses' if not value.get('completedLatencyNS',{}).get('samples') else None,'completionResidualNS':'No complete native/dispatched residual observations' if not value.get('completionResidualNS',{}).get('samples') else None}
        power=judgment.get('power') or {}
        kpis={'status':judgment['status'],'visibility':'PRIVATE_OPERATIONAL_ONLY','requests':requests,'judgmentCounts':dict(Counter(value['D1'] for value in judgment['requests'].values())),'wholePoolJoules':power.get('wholePoolJoules') if power.get('complete') else None,'activePoolJoules':power.get('activePoolJoules') if power.get('complete') else None,'energyPopulation':'Whole/active shared pool during offered [from,to); not attributed per workload','limitations':result['limitations'],'missingReasons':{'energy':None if power.get('complete') else 'Actual whole/active power coverage incomplete','judgment':None if judgment['status']!='NOT_READY' else 'Required evidence incomplete; actual reasons retained in raw judgment'}}
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
    collector=module('e5recordedarchive',HERE/'e5-archive.py');ast.parse(collector.HOST_READER);ast.parse(HOST_USAGE)
    assert list(inspect.signature(collector.collect).parameters)==['folder','phase','collection','io','timeout_seconds']
    schema=subprocess.run([sys.executable,'-B',str(HERE/'e5-wire.py'),'schema'],capture_output=True,text=True,timeout=5)
    assert schema.returncode==0;required= json.loads(schema.stdout)
    assert {'G','mSeconds','N','workloads'}<=set(required['numbers'])
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
        else:save(out/'phase.json',phase);save(out/'pre-nodes.json',load(before['inputs']['nodes']['path']))
        resource.setrlimit(resource.RLIMIT_FSIZE,(cfg['maxArtifactFileBytes'],cfg['maxArtifactFileBytes']))
        d=module('e5affectedconduct',D2/'conduct_before.py')
        if reuse:
            for source in index['sources']:
                actual=ref(source['path'])
                if any(actual[key]!=source[key] for key in ('path','bytes','sha256')):raise ValueError('Captured raw identity changed; no live/S3 repeat')
            facts=load(out/'metric-sources-provenance.json')['value']
            actual_facts=ref(facts['path'])
            if any(actual_facts[key]!=facts[key] for key in ('path','bytes','sha256')):raise ValueError('Captured metric-source identity changed')
            archive=module('e5affectedarchive',HERE/'e5-archive.py');scope,configured=archive._scope(out)
            reduction=archive._reduce(out,phase,cfg['powerExecutable'],time.monotonic()+cfg['timeoutSeconds']-40,scope,configured)
            captured={'status':'NOT_READY' if reduction['errors'] else 'CAPTURED_NOT_EVALUATED','errors':reduction['errors'],'sourceIndex':ref(out/'raw-source-index.json'),'originalCollection':ref(out/'archive-collection.json'),'powerReduction':ref(out/'power-reduction.json'),'liveOrS3Recapture':False}
            save(out/'archive-consumer-retry.json',captured)
        else:
            d.K=ENVIRONMENT['d2']['kubectl']+['-n','chill-system']
            io=object.__new__(d.Conductor);io.journal_lock=threading.Lock();io.journal=out/'commands.jsonl';io.a=argparse.Namespace(phase='e5-affected-historical-input-check');io.source=source_sha
            pods=io.k(['get','pods','-l','app.kubernetes.io/component=serving-entrypoint','-o','json'],timeout=20)['items']
            if len(pods)!=1:raise ValueError('Exact current raw-serving container required')
            container=next(c for c in pods[0]['spec']['containers'] if c['name']=='serving-entrypoint');flags={x.split('=',1)[0]:x.split('=',1)[1] for x in container['args'] if '=' in x}
            collection={**op['collection'],'observationDirectory':flags['--observation-directory'],'metricSourcesConfigMap':flags['--profiler-facts-configmap'],'metricSourcesKey':flags['--metric-sources-key'],'powerExecutable':cfg['powerExecutable']}
            usage=subprocess.run(collection['serverHostReadArgv']+[collection['observationDirectory']],input=HOST_USAGE,capture_output=True,text=True,timeout=30)
            if usage.returncode:raise RuntimeError('Actual retained-byte preflight failed')
            size=json.loads(usage.stdout);save(out/'retained-byte-preflight.json',size)
            if size['bytes']>cfg['maxRetainedBytes']:raise ValueError('Actual retained input exceeds smoke byte bound; no truncated success')
            captured=module('e5affectedarchive',HERE/'e5-archive.py').collect(out,phase,collection,io,cfg['timeoutSeconds']-40)
        if captured['status']!='CAPTURED_NOT_EVALUATED':raise ValueError('Actual archive/current/reducer incomplete; retain NOT_READY manifest')
        collected_bytes=sum(p.stat().st_size for p in out.rglob('*') if p.is_file())
        if collected_bytes>cfg['maxCollectedBytes']:raise ValueError('Collected actual input exceeds smoke byte bound; no subset substituted')
        evidence=load(cfg['nativeSafetyEvidence']);proof=load(evidence['nativeHelperResult']);nodes=load(out/'pre-nodes.json')['items'];bound=[{'name':n['metadata']['name'],'uid':n['metadata']['uid']} for n in nodes if n['metadata']['name'] in NODES]
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
    p=argparse.ArgumentParser(description=__doc__); p.add_argument('command',choices=['schema','check-preparation','check-recorded','bind','check','judge','summarize','publish','campaign','start','stage','smoke','restore','collect','factor','factor-restore','cleanup-jobs','finish']); p.add_argument('--config',type=Path); p.add_argument('--binding',type=Path); p.add_argument('--out',type=Path); p.add_argument('--release',type=Path);p.add_argument('--source-sha'); p.add_argument('--stage',default=''); p.add_argument('--mode',default='full'); p.add_argument('--experiment-id'); p.add_argument('--store-config',type=Path); a=p.parse_args()
    try:
        for value in (a.config,a.binding,a.out,a.release):
            if value is not None and not value.is_absolute(): raise ValueError('All paths must be absolute')
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
            assert sum(x['seconds'] for x in b['schedule'])==10800
            assert sum(x['seconds'] for x in b['schedule'] if x['id']!='E5-0')==10200
            assert set(SEEDS.values())=={101,202,303}
            assert len(b['rowOrder'])==8
            print(json.dumps({'status':'OFFLINE_INPUT_CHECK_ONLY','scientificResults':'UNMEASURED','scheduleSeconds':21000,'N':b['config']['N']})); return 0
        if not a.out: raise ValueError('--out absolute run or stage directory required')
        if a.command=='judge': return 2 if judge(a.binding,a.out)['status'] in ('NOT_READY','FAIL') else 0
        if a.command=='summarize': return 2 if summarize(a.binding,a.out)['status']=='NOT_READY' else 0
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
        if a.out and a.command!='bind': save(a.out/'STOP.json',{'at':now(),'command':a.command,'error':str(exc),'status':'NOT_READY','scientificResults':'UNMEASURED_OR_PARTIAL; never PASS'})
        print('NOT_READY: '+str(exc),file=sys.stderr); return 2


if __name__=='__main__': sys.exit(main())
