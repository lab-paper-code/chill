#!/usr/bin/env python3
"""EvaluationRun Job adapter for frozen black-box declarations and Dataset carriers.

Operational ConfigMap values contain client budgets and credential projections,
never scientific host files. Immutable result objects are uploaded last.
"""
import argparse
import copy
import datetime as dt
import importlib.util
import json
import math
import os
from pathlib import Path
import ssl
import subprocess
import re
import tempfile
import time
from types import SimpleNamespace, MethodType
import urllib.request

HERE=Path(__file__).resolve().parent
REPO=HERE.parent.parent
TOKEN='/var/run/secrets/kubernetes.io/serviceaccount/token'
CA='/etc/chill/ca/tls.crt'
STEPS=('prepare','finish','publish','factor','stage','judge','cleanup-jobs')


def now(): return dt.datetime.now(dt.timezone.utc).isoformat()
def load(path): return json.loads(Path(path).read_text())
def save(path,value):
    path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    temporary=path.with_name(path.name+'.tmp');temporary.write_text(json.dumps(value,indent=2,allow_nan=False)+'\n');temporary.replace(path)
def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value

AUTH=module('evaluation_auth',HERE/'auth.py')


class Store:
    def __init__(self): self.name=os.environ['CHILL_EVIDENCE_STORE']
    def transfer(self,operation,key,path,size=0):
        result=subprocess.run([str(REPO/'bin/evaluation-evidence'),'--operation',operation,'--key',key,'--file',str(path),'--size',str(size)],capture_output=True,text=True)
        if result.returncode == 3: raise FileNotFoundError(key)
        if result.returncode: raise RuntimeError('EvidenceStoreFailed: '+result.stderr.strip())
        return json.loads(result.stdout)
    def put(self,key,path): return self.transfer('put',key,path)
    def get(self,ref,path):
        if ref['store']!=self.name: raise ValueError('FrozenInputInvalid: foreign evidence store')
        return self.transfer('get',ref['key'],path,ref.get('size',0))


def api(config,path):
    request=urllib.request.Request(config['chillAPIURL'].rstrip('/')+path,headers={'Authorization':'Bearer '+Path(TOKEN).read_text().strip()})
    opener=urllib.request.build_opener(AUTH.NoRedirect(),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=CA)))
    with opener.open(request,timeout=30) as response: return json.load(response)


def kubectl(namespace,*args):
    result=subprocess.run(['/usr/bin/kubectl','-n',namespace,*args,'-o','json'],capture_output=True,text=True,timeout=60)
    if result.returncode: raise RuntimeError('TargetUnavailable: '+result.stderr.strip())
    return json.loads(result.stdout)


def environment(run, scientific):
    work = run.parent.parent
    env = {'paths': {'repository': str(REPO), 'ops': str(work), 'campaign': str(run.parent), 'inputs': str(work / 'inputs')},
           'evaluation': scientific}
    path = work / 'environment.json'
    save(path, env)
    os.environ['CHILL_EXPERIMENT_CONFIG'] = str(path)
    return env


def stage_config(spec, scenario, execution, config, root):
    operational = json.loads(config['operational'])
    if set(operational) - {'timeboxes', 'tokenSafetyMarginSeconds', 'workloads', 'collection'}:
        raise ValueError('FrozenInputInvalid: unknown operational fields')
    timeboxes = {'setupSeconds': 300, 'barrierSeconds': 300, 'drainSeconds': 120, 'flushSeconds': 5,
                 'collectSeconds': 300, 'apiSeconds': 30}
    if set(operational.get('timeboxes', {})) - set(timeboxes):
        raise ValueError('FrozenInputInvalid: unknown operational timeboxes')
    timeboxes.update(operational.get('timeboxes', {}))
    if 'drainTimeoutSeconds' in config:
        timeboxes['drainSeconds'] = float(config['drainTimeoutSeconds'])
    if any(type(value) not in (int, float) or not math.isfinite(value) or value <= 0 for value in timeboxes.values()):
        raise ValueError('FrozenInputInvalid: positive finite timeboxes required')
    generator = copy.deepcopy(spec['loadGenerator'])
    resources = generator['resources']
    if (not isinstance(generator.get('nodeName'), str) or not generator['nodeName'] or not isinstance(resources, dict) or set(resources) - {'requests', 'limits'} or
            any(not isinstance(values, dict) or set(values) - {'memory'} for values in resources.values())):
        raise ValueError('FrozenInputInvalid: generator nodeName and memory-only resources required')
    names = {w['name'] for w in scenario['workloads']}
    if set(generator['shards']) != names or any(type(n) is not int or n < 1 for n in generator['shards'].values()):
        raise ValueError('FrozenInputInvalid: declared generator shard coverage')
    if set(operational.get('workloads', {})) != names:
        raise ValueError('FrozenInputInvalid: operational workload coverage')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,39}', spec['row']):
        raise ValueError('FrozenInputInvalid: safe row label required')
    workloads = []
    allowed = {'producerEndpoint', 'producerTokenFile', 'producerCAFile', 'deliverySeconds', 'socketSeconds',
               'workers', 'lifetimeSeconds', 'gatewayCapSeconds', 'podSpec', 'volumeMounts'}
    for pinned in scenario['workloads']:
        operation = operational['workloads'][pinned['name']]
        if set(operation) - allowed:
            raise ValueError('FrozenInputInvalid: unknown operational workload fields')
        workload = {**copy.deepcopy(pinned), **copy.deepcopy(operation)}
        required = {'producerEndpoint', 'producerTokenFile', 'producerCAFile', 'deliverySeconds', 'socketSeconds', 'workers'}
        if required - operation.keys():
            raise ValueError('FrozenInputInvalid: missing operational client fields')
        if type(workload['workers']) is not int or workload['workers'] <= 0 or any(
                type(workload[key]) not in (int, float) or not math.isfinite(workload[key]) or workload[key] <= 0
                for key in ('deliverySeconds', 'socketSeconds')):
            raise ValueError('FrozenInputInvalid: positive client worker/time budgets required')
        lifetime = workload.get('lifetimeSeconds')
        if lifetime is not None and (type(lifetime) not in (int, float) or not math.isfinite(lifetime) or
                                     lifetime < pinned['slo']['targetLatencyNanos'] / 1e9 or workload['socketSeconds'] <= lifetime + workload['deliverySeconds']):
            raise ValueError('FrozenInputInvalid: lifetime/socket must cover frozen SLO and delivery')
        if 'gatewayCapSeconds' in workload and (type(workload['gatewayCapSeconds']) not in (int, float) or
                                               not math.isfinite(workload['gatewayCapSeconds']) or workload['gatewayCapSeconds'] <= 0 or
                                               lifetime is not None and lifetime > workload['gatewayCapSeconds']):
            raise ValueError('FrozenInputInvalid: configured client lifetime cap')
        dataset = scenario['datasets'][pinned['name']]
        workload.update(datasetRef=scenario['spec']['datasetRefs'][pinned['name']], dataset=dataset['spec'],
                        fields=scenario['fields'][pinned['name']])
        workloads.append(workload)
    quality = spec['judgment']['generatorQuality']
    collection = operational.get('collection', {})
    if set(collection) - {'maximumDurationSeconds'}:
        raise ValueError('FrozenInputInvalid: unknown collection fields')
    if 'maximumDurationSeconds' in collection and (type(collection['maximumDurationSeconds']) not in (int, float) or
                                                   not math.isfinite(collection['maximumDurationSeconds']) or collection['maximumDurationSeconds'] <= 0):
        raise ValueError('FrozenInputInvalid: positive collection duration bound required')
    return {'namespace': execution['namespace'], 'endpoint': config['chillAPIURL'], 'caFile': CA,
            'tokenFile': TOKEN, 'collectorTokenFile': TOKEN, 'timeboxes': timeboxes,
            'tokenSafetyMarginSeconds': operational.get('tokenSafetyMarginSeconds', 60), 'workloads': workloads,
            'loadGenerator': generator, 'seeds': scenario['spec']['seeds'], 'arrivals': scenario['spec']['arrivalDistribution'],
            'traceOffsets': scenario.get('traceOffsets', {}),
            'stages': [{'id': spec['row'], 'parts': [{'id': p['id'], 'seconds': p['durationSeconds'], 'rates': p['rates']} for p in scenario['spec']['parts']]}],
            'generatorQuality': {'latenessToleranceNS': quality['latenessToleranceNanos'], 'maxExceedingFraction': quality['maxExceedingFraction']},
            'collection': {**collection, 'energyCoverage': spec['judgment']['energyCoverage'], 'powerMeasurement': spec['powerMeasurement']},
            'evaluation': {'execution': execution, 'sourceSHA': spec['harness']['sourceSHA'],
                           'minimumFreeBytes': int(config['minimumFreeBytes']), 'stageFolder': str(root / 'measured'),
                           'settingsPatch': spec.get('settingsPatch', {}), 'barrier': generator['barrier'],
                           'failureInjection': spec.get('failureInjection', [])}}


def result_record(execution,step,started,status='Complete',reason='ProcedureComplete',measurement=None,artifacts=None,outputs=None):
    record={'schemaVersion':'evaluation-step.v1alpha1','runUID':execution['runUID'],'step':step,'execution':{'status':status,'reason':reason},'startedAt':started,'finishedAt':now(),'artifacts':artifacts or []}
    for key in ('stageUID','stageID'):
        if execution.get(key): record[key]=execution[key]
    if measurement: record['measurement']=measurement
    if outputs: record['outputs']=outputs
    if step in ('finish','cleanup-jobs'): record['cleanup']={'status':status,'reason':reason}
    return record


def verdict(judgment):
    status=judgment['status'];failure=judgment.get('failureScenarioEvidence') or {}
    if status.startswith('INVALID') or failure.get('status')=='INVALID': return {'verdict':'INVALID','reason':'GeneratorInvalid' if status=='INVALID_GENERATOR' else 'InjectionInvalid'}
    if status in ('NOT_EVALUATED','NOT_READY','INCOMPLETE','UNKNOWN') or failure.get('status')=='UNMEASURED': return {'verdict':'NOT_EVALUATED','reason':judgment.get('reason') or 'ObservationUnavailable'}
    return {'verdict':'FAIL' if status=='FAIL' else 'PASS','reason':'SLOFailed' if status=='FAIL' else 'MeasurementComplete'}


def exit_code(record):
    if record['execution']['status']!='Complete': return 1
    return 2 if record.get('measurement',{}).get('verdict') in ('INVALID','NOT_EVALUATED') else 0


class Harness:
    def __init__(self,run,specdir,store=None):
        self.run=run.resolve();self.spec=load(specdir/'spec.json');self.execution=load(specdir/'execution.json');self.config=load(specdir/'config.json')
        self.config['namespace']=self.execution['namespace'];self.store=store or Store();self.inputs=self.run.parent.parent/'inputs'
        self.scope='stages/'+self.execution['stageID'] if self.execution.get('stageID') else 'run'
        self.prefix='experiments/'+self.execution['runUID']+'/'+self.scope
        self.root=self.run/self.scope if self.scope!='run' else self.run
        self.harness_module=None
    def upload(self,path,key): return self.store.put(key,path)
    def emit(self,value,name,key=None):
        path=self.root/name;save(path,value);return self.upload(path,key or self.prefix+'/'+self.step+'/'+name)
    def prepare(self):
        usage=os.statvfs(self.run)
        if usage.f_bavail*usage.f_frsize<int(self.config['minimumFreeBytes']):
            raise ValueError('EvidenceCapacityInsufficient: workspace free bytes')
        scenarios = {}
        self.harness_module = module('evaluation_harness', HERE / 'conductor.py')
        if self.spec['harness']['sourceSHA'] != os.environ.get('CHILL_HARNESS_SOURCE_SHA'):
            raise ValueError('IdentityMismatch: harness source differs from image revision')
        for template in self.spec['stages']:
            pin = template['loadScenarioRef']
            identity = pin['uid']
            if identity in scenarios:
                continue
            obj = kubectl(self.execution['namespace'], 'get', 'loadscenario', pin['name'])
            if obj['metadata']['uid'] != identity:
                raise ValueError('IdentityMismatch: LoadScenario UID')
            scenario = obj['spec']
            folder = self.inputs / identity
            folder.mkdir(parents=True, exist_ok=True)
            names = [w['name'] for w in scenario['workloads']]
            self.harness_module.parse_seeds(scenario['seeds'], names)
            if set(scenario['datasetRefs']) != set(names) or set(scenario['arrivalDistribution']) != set(names):
                raise ValueError('FrozenInputInvalid: workload key coverage')
            workloads, datasets, fields = [], {}, {}
            for workload_pin in scenario['workloads']:
                view = api(self.config, '/api/v1/workloads/' + workload_pin['name'])
                applied = view.get('appliedDefinition') or {}
                actual = {key: applied.get(key) for key in ('name', 'uid', 'generation', 'slo', 'model', 'preprocessor', 'dataset')}
                if any(actual.get(key) != workload_pin[key] for key in ('name', 'uid', 'generation')):
                    raise ValueError('IdentityMismatch: applied Workload ' + workload_pin['name'])
                workloads.append(actual)
                dataset_pin = scenario['datasetRefs'][workload_pin['name']]
                if set(dataset_pin) != {'name', 'uid'}:
                    raise ValueError('FrozenInputInvalid: Dataset reference fields')
                dataset = kubectl(self.execution['namespace'], 'get', 'dataset', dataset_pin['name'])
                if dataset['metadata']['uid'] != dataset_pin['uid']:
                    raise ValueError('IdentityMismatch: Dataset UID')
                source = dataset['spec']['source']
                if not re.fullmatch(r'.+@sha256:[0-9a-f]{64}', source['image']) or not source['path'].startswith('/') or '..' in Path(source['path']).parts:
                    raise ValueError('FrozenInputInvalid: immutable Dataset carrier and clean absolute path required')
                declared = kubectl(self.execution['namespace'], 'get', 'workload', workload_pin['name'])
                if declared['metadata']['uid'] != workload_pin['uid']:
                    raise ValueError('IdentityMismatch: Workload resource UID')
                preprocessor = kubectl(self.execution['namespace'], 'get', 'preprocessor', declared['spec']['preprocessorRef']['name'])
                required_fields = [field['name'] for field in preprocessor['spec']['input']['fields']]
                if not required_fields or len(set(required_fields)) != len(required_fields) or not set(required_fields) <= {f['name'] for f in dataset['spec']['fields']}:
                    raise ValueError('FrozenInputInvalid: Dataset/preprocessor record fields')
                datasets[workload_pin['name']] = dataset
                fields[workload_pin['name']] = required_fields
            traces = {}
            for name, reference in scenario.get('traceRefs', {}).items():
                path = folder / ('trace-' + name + '.json')
                self.store.get(reference, path)
                traces[name] = load(path)
            scenarios[identity] = {'ref': {'name': obj['metadata']['name'], 'uid': identity}, 'spec': scenario,
                                   'workloads': workloads, 'datasets': datasets, 'fields': fields, 'traceOffsets': traces}
        scientific = {'nodes': [node['name'] for node in self.spec['powerMeasurement']['nodes']]}
        environment(self.run, scientific)
        self.run.mkdir(parents=True, exist_ok=True)
        started = now()
        bindings, targets = {}, {}
        for template in self.spec['stages']:
            stage = {**{key: self.spec[key] for key in ('target', 'harness', 'loadGenerator', 'powerMeasurement', 'judgment', 'evidenceStore')}, 'settingsPatch': self.spec.get('settingsPatch', {}), **template}
            targets[template['id']] = self.check_target(stage['target'])
            self.check_fleet(stage)
            root = self.run / 'stages' / template['id']
            root.mkdir(parents=True, exist_ok=True)
            config = stage_config(stage, scenarios[template['loadScenarioRef']['uid']], self.execution, self.config, root)
            path = root / 'prepared-config.json'
            save(path, config)
            bindings[template['id']] = self.harness_module.bind(path)
        snapshot = {'schemaVersion': 'evaluation-snapshot.v2alpha1', 'inputsVersion': 'evaluation-inputs.v2alpha1',
                    'runUID': self.execution['runUID'], 'runSpec': self.spec, 'scenarios': scenarios,
                    'environment': scientific, 'bindings': bindings, 'targets': targets, 'generatorNode': self.generator_node,
                    'preparedAt': started, 'config': self.config}
        path=self.inputs/'snapshot.json';save(path,snapshot)
        ref=self.upload(path,'experiments/'+self.execution['runUID']+'/run/prepare/input-snapshot.json')
        self.outputs={'inputSnapshot':ref}
    def check_target(self, target):
        identities = []
        for component in target['components']:
            resource = component['resource']
            obj = kubectl(resource['namespace'], 'get', resource['kind'], resource['name'])
            if resource.get('uid') and obj['metadata']['uid'] != resource['uid']:
                raise ValueError('IdentityMismatch: external target UID')
            stage_id = self.execution.get('stageID')
            expected = next((item for item in getattr(self, 'snapshot', {}).get('targets', {}).get(stage_id, [])
                             if item['name'] == component['name']), None)
            if expected and obj['metadata']['uid'] != expected['uid']:
                raise ValueError('IdentityMismatch: external target resource recreated')
            if obj['kind'] == 'Pod':
                pods = {'items': [obj]}
            else:
                selector = obj['spec'].get('selector', {})
                terms = [key + '=' + value for key, value in selector.get('matchLabels', {}).items()]
                for expression in selector.get('matchExpressions', []):
                    key, operator = expression['key'], expression['operator']
                    if operator in ('In', 'NotIn'):
                        terms.append(key + (' in (' if operator == 'In' else ' notin (') + ','.join(expression['values']) + ')')
                    elif operator in ('Exists', 'DoesNotExist'):
                        terms.append(('!' if operator == 'DoesNotExist' else '') + key)
                    else:
                        raise ValueError('FrozenInputInvalid: unsupported target selector operator')
                if not terms:
                    raise RuntimeError('TargetUnavailable: declared target has no Pod selector')
                pods = kubectl(resource['namespace'], 'get', 'pods', '-l', ','.join(terms))
            current = [pod for pod in pods['items'] if not pod['metadata'].get('deletionTimestamp')]
            if not current:
                raise RuntimeError('TargetUnavailable: no target Pods')
            for pod in current:
                statuses = pod.get('status', {}).get('containerStatuses', [])
                if not statuses or any(not status.get('ready') for status in statuses) or not any(c['type'] == 'Ready' and c['status'] == 'True' for c in pod.get('status', {}).get('conditions', [])):
                    raise RuntimeError('TargetUnavailable: target Pod not Ready')
            identities.append({'name': component['name'], 'uid': obj['metadata']['uid'], 'resource': resource,
                               'releaseInstance': obj['metadata'].get('labels', {}).get('app.kubernetes.io/instance'),
                               'pods': [{'uid': pod['metadata']['uid'], 'containers': pod.get('status', {}).get('containerStatuses', [])} for pod in current]})
        self.target_identities = identities
        return identities

    def owns_environment_pod(self, pod, targets, owner_objects):
        """Resolve controller UIDs; labels on the Pod itself never prove ownership."""
        namespace = pod['metadata']['namespace']
        run_namespace = self.execution['namespace']
        if any(target['resource']['kind'] == 'Pod' and target['resource']['namespace'] == namespace and
               target['uid'] == pod['metadata']['uid'] for target in targets):
            return True, []
        stage_uids = {uid for uid in [self.execution.get('stageUID'),
                                     *(child['uid'] for child in self.execution.get('childRefs', []))] if uid}
        roots = {(target['resource']['namespace'], target['resource']['kind'], target['uid'])
                 for target in targets if target['resource']['kind'] != 'Pod'}
        release_labels = {(target['resource']['namespace'], target['releaseInstance'])
                          for target in targets if target['releaseInstance']}
        # Mirror Pods and DaemonSet-owned Pods are persistent node infrastructure,
        # not competing experiments, independent of their namespace or labels.
        if 'kubernetes.io/config.mirror' in pod['metadata'].get('annotations', {}):
            return True, []
        controller_versions = {'ReplicaSet': 'apps/v1', 'Deployment': 'apps/v1', 'StatefulSet': 'apps/v1',
                               'Job': 'batch/v1', 'CronJob': 'batch/v1'}
        owner_namespaces = {run_namespace, *(target['resource']['namespace'] for target in targets)}
        pending, seen, issues = [(pod, 'Pod')], set(), []
        while pending:
            obj, kind = pending.pop()
            meta = obj['metadata']
            refs = meta.get('ownerReferences', [])
            if kind == 'Job' and namespace == run_namespace and any(
                    ref['apiVersion'] == 'edge.dacs.io/v1alpha1' and ref.get('controller') is True and
                    ((ref['kind'] == 'EvaluationRun' and ref['uid'] == self.execution['runUID']) or
                     (ref['kind'] == 'EvaluationStage' and ref['uid'] in stage_uids)) for ref in refs):
                # Reaching this live Job required matching its actual UID to the
                # Pod's owner chain; Run labels alone cannot mint an owned Job.
                return True, []
            if kind != 'Pod' and ((namespace, kind, meta['uid']) in roots or
                                 (namespace, meta.get('labels', {}).get('app.kubernetes.io/instance')) in release_labels):
                return True, []
            if any(ref['kind'] == 'DaemonSet' and ref['apiVersion'] == 'apps/v1' for ref in refs):
                return True, []
            # Match the controller's unique Workload-owner predicate (a08fda9d),
            # then resolve the referenced cluster-scoped CR's actual UID.
            workloads = [ref for ref in refs if ref['kind'] == 'Workload' and ref['apiVersion'] == 'edge.dacs.io/v1alpha1']
            for ref in refs:
                workload = len(workloads) == 1 and ref == workloads[0]
                if not workload and (namespace not in owner_namespaces or controller_versions.get(ref['kind']) != ref['apiVersion']):
                    continue
                key = (namespace, ref['apiVersion'], ref['kind'], ref['name'], ref['uid'])
                identity = {key: ref[key] for key in ('kind', 'name', 'uid')}
                if key in seen:
                    issues.append({'owner': identity, 'failure': 'RepeatedOwnerReference'})
                    continue
                seen.add(key)
                if key not in owner_objects:
                    try:
                        owner_objects[key] = kubectl(namespace, 'get', ref['kind'], ref['name'])
                    except (RuntimeError, subprocess.TimeoutExpired, OSError) as error:
                        message = str(error)
                        failure = ('NotFound' if re.search(r'NotFound|not found|\b404\b', message, re.I) else
                                   'Forbidden' if re.search(r'Forbidden|\b403\b', message, re.I) else type(error).__name__)
                        return False, [{'owner': identity, 'failure': failure, 'message': message}]
                owner = owner_objects[key]
                if owner['metadata']['uid'] != ref['uid']:
                    issues.append({'owner': identity, 'failure': 'UIDMismatch'})
                    continue
                if workload:
                    return True, []
                pending.append((owner, ref['kind']))
        return False, issues or [{'failure': 'NoRecognizedOwner'}]

    def check_fleet(self, spec):
        nodes = list(spec['powerMeasurement']['nodes'])
        generator = getattr(self, 'snapshot', {}).get('generatorNode')
        if generator is None:
            node = kubectl(self.execution['namespace'], 'get', 'node', spec['loadGenerator']['nodeName'])
            generator = {key: node['metadata'][key] for key in ('name', 'uid')}
        self.generator_node = generator
        nodes.append(generator)
        for pin in nodes:
            node = kubectl(self.execution['namespace'], 'get', 'node', pin['name'])
            if node['metadata']['uid'] != pin['uid']:
                raise ValueError('IdentityMismatch: participating Node UID')
            if not any(c['type'] == 'Ready' and c['status'] == 'True' for c in node.get('status', {}).get('conditions', [])):
                raise RuntimeError('TargetUnavailable: participating Node NotReady')
        targets = (self.snapshot['targets'][self.execution['stageID']] if hasattr(self, 'snapshot')
                   else self.target_identities)
        owner_objects = {}
        for name in sorted({pin['name'] for pin in nodes}):
            pods = kubectl(self.execution['namespace'], 'get', 'pods', '--all-namespaces',
                           '--field-selector', 'spec.nodeName=' + name)['items']
            for pod in pods:
                if pod.get('spec', {}).get('nodeName') != name or pod.get('status', {}).get('phase') not in ('Running', 'Pending'):
                    continue
                owned, issues = self.owns_environment_pod(pod, targets, owner_objects)
                if not owned:
                    identity = pod['metadata']['namespace'] + '/' + pod['metadata']['name']
                    raise RuntimeError('TargetUnavailable: external Pod on participating Node ' + name + ': ' +
                                       identity + '; ownership unknown: ' + json.dumps(issues, sort_keys=True))
    def open_snapshot(self):
        path=self.inputs/'snapshot.json'
        reference=self.execution.get('inputSnapshotRef') or self.spec.get('inputSnapshotRef')
        if self.step=='finish' and self.execution.get('prepareCompleted') is True:
            try:
                if not reference: raise ValueError('missing snapshot reference')
                self.inputs.mkdir(parents=True,exist_ok=True)
                with tempfile.TemporaryDirectory(prefix='finish-snapshot-',dir=self.inputs) as directory:
                    downloaded=Path(directory)/'snapshot.json'
                    self.store.get(reference,downloaded)
                    self.snapshot=load(downloaded)
            except Exception as error: raise RuntimeError('EvidenceUnreadable: '+str(error)) from error
        else:
            if not path.exists(): self.store.get(reference,path)
            self.snapshot=load(path)
        try:
            if self.snapshot.get('schemaVersion') != 'evaluation-snapshot.v2alpha1' or self.snapshot.get('inputsVersion') != 'evaluation-inputs.v2alpha1' or self.snapshot.get('runUID') != self.execution['runUID']:
                raise ValueError('FrozenInputInvalid: input snapshot execution identity')
        except Exception as error:
            if self.step=='finish': raise RuntimeError('EvidenceUnreadable: '+str(error)) from error
            raise
        if self.execution.get('stageID'):
            template=next(stage for stage in self.snapshot['runSpec']['stages'] if stage['id']==self.execution['stageID'])
            expected={**{key:self.snapshot['runSpec'][key] for key in ('target','harness','loadGenerator','powerMeasurement','judgment','evidenceStore')},**({'settingsPatch':self.snapshot['runSpec']['settingsPatch']} if 'settingsPatch' in self.snapshot['runSpec'] else {}),**template}
            for key,value in expected.items():
                if key != 'id' and self.spec.get(key) != value:
                    raise ValueError('FrozenInputInvalid: child differs from frozen '+key)
            if self.spec['stageID']!=self.execution['stageID']:
                raise ValueError('FrozenInputInvalid: child stage ID differs from execution')
        environment(self.run,self.snapshot['environment']);self.harness_module=module('evaluation_harness',HERE/'conductor.py')
    def controller(self):
        stage_id=self.execution.get('stageID',self.snapshot['runSpec']['stages'][0]['id'])
        binding=copy.deepcopy(self.snapshot['bindings'][stage_id]);config=binding['config']
        config['evaluation']['execution']=self.execution
        if 'stageID' not in self.execution: config['evaluation'].pop('stageFolder',None)
        path=self.root/'binding.json';save(path,binding)
        args=SimpleNamespace(binding=path,out=self.root,stage=self.spec.get('row',binding['schedule'][0]['id']))
        return self.harness_module.CPO(args,go_callback=self.go)
    def go(self,value):
        value.update({key:self.execution[key] for key in ('runUID','stageUID','stageID')})
        path=self.root/'measured/go.json';save(path,value)
        ref=self.upload(path,self.prefix+'/go.json');print('EVALUATION_GO '+ref['store']+'/'+ref['key']+' '+str(ref['size']),flush=True)
    def factor(self):
        self.check_fleet(self.spec)
        self.controller().factor()
    def stage(self):
        controller = self.controller()
        self.check_target(self.spec['target'])
        self.check_fleet(self.spec)
        last = [0]
        def monitor(instance, offset, folder):
            instance.disk_boundary()
            if time.monotonic() - last[0] >= 30:
                targets = self.check_target(self.spec['target'])
                save(folder / 'target-observations.json', {'at': now(), 'targets': targets})
                self.check_fleet(self.spec)
                last[0] = time.monotonic()
            return offset
        controller.monitor = MethodType(monitor, controller)
        controller.run()
        phase = load(self.root / 'measured/phase.json')
        parser = module('evaluation_start_times', HERE / 'raw.py')._time
        starts = [parser(value['at']) for value in phase['starts'].values()]
        skew = max(starts) - min(starts) if starts else 0
        save(self.root / 'measured/generator-barrier.json',
             {'startSkewNanos': skew, 'maximumStartSkewNanos': self.spec['loadGenerator']['barrier']['maxStartSkewNanos'],
              'accepted': skew <= self.spec['loadGenerator']['barrier']['maxStartSkewNanos']})
    def judge(self):
        controller_binding=self.root/'binding.json';folder=self.root/'measured'
        if self.spec.get('failureInjection'): self.injections(folder)
        self.measurement=verdict(self.harness_module.judge(controller_binding,folder))
        if not load(folder/'generator-barrier.json')['accepted']:
            self.measurement={'verdict':'INVALID','reason':'GeneratorInvalid'}
    def injections(self,folder):
        records=[]
        for plan in self.spec['failureInjection']:
            path=self.root/('injection-'+plan['id']+'.json')
            self.store.get({'store':self.store.name,'key':self.prefix+'/injections/'+plan['id']+'.json'},path)
            record=load(path)
            if any(record.get(key)!=self.execution[key] for key in ('runUID','stageUID','stageID')):
                raise ValueError('FrozenInputInvalid: injection evidence execution identity')
            records.append(record)
        self.emit({'plans':self.spec['failureInjection'],'records':records},'injection-records.json')
        events=[]
        for plan,original in zip(self.spec['failureInjection'],records):
            record=original['record'];path=self.root/('injection-'+plan['id']+'.json')
            events.append({'kind':plan['action'],'atUTC':record.get('requestedAt',record['scheduledAt']),'exitCode':0 if record.get('outcome')=='Complete' else 1,'podUID':record.get('podRef',{}).get('uid'),'nodeUID':record.get('nodeRef',{}).get('uid'),'commandRef':str(path),'responseRef':str(path)})
        save(folder/'failure-events.json',{'stage':self.spec['row'],'selectionPolicy':'evaluation-controller-v1','plans':self.spec['failureInjection'],'records':records,'events':events})
    def cleanup_jobs(self):
        controller=self.controller()
        try: controller.cleanup_jobs()
        finally: self.residuals=controller.residuals if hasattr(controller,'residuals') else []
    def finish(self):
        if self.execution.get('prepareCompleted') is False:
            self.reason='NoControlEffects'
            return
        self.controller().finish()
    def publish(self):
        tables=module('evaluation_tables',HERE/'tables.py');stages=[];results={};artifacts=[];procedures={};actual_specs={};stage_outcomes=[]
        prepare_failed=self.execution.get('prepareCompleted') is False
        run_spec=self.spec if prepare_failed else self.snapshot['runSpec']
        templates=[] if prepare_failed else run_spec['stages']
        not_started={'status':'Failed','reason':'NotStarted'}
        def execution_outcome(values):
            executions=[value.get('execution',not_started) for value in values]
            failures=[value for value in executions if value['status']!='Complete']
            return next((value for value in failures if value.get('reason')!='NotStarted'),failures[0] if failures else {'status':'Complete','reason':'ProcedureComplete'})
        publication_prefix='serving/experiments.v1alpha1/'+self.execution['runUID']+'/'
        for template in templates:
            sid=template['id'];scope=self.run/'stages'/sid;result_path=self.run/('publish-result-'+sid+'.json')
            try:
                self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/stages/'+sid+'/judge/result.json'},result_path)
                result=load(result_path)
            except FileNotFoundError:
                result={'execution':{'status':'Failed','reason':'NotStarted'},'measurement':{'verdict':'NOT_EVALUATED','reason':'NotStarted'},'artifacts':[]}
            results[sid]=result;artifacts.extend(result['artifacts'])
            procedures[sid]={}
            for step in ('factor','stage','judge','cleanup-jobs'):
                if step=='judge': procedures[sid][step]=result;continue
                path=self.run/('publish-'+sid+'-'+step+'.json')
                try:
                    self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/stages/'+sid+'/'+step+'/result.json'},path)
                    value=load(path);procedures[sid][step]=value;artifacts.extend(value['artifacts'])
                except FileNotFoundError:
                    procedures[sid][step]={'execution':{'status':'Failed','reason':'NotStarted'}}
            if (scope/'execution-spec.json').exists(): actual_specs[sid]=load(scope/'execution-spec.json')
            child=procedures[sid]
            injections=[]
            for plan in template.get('failureInjection',[]):
                injection_path=self.run/('publish-injection-'+sid+'-'+plan['id']+'.json')
                try:
                    self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/stages/'+sid+'/injections/'+plan['id']+'.json'},injection_path)
                    evidence=load(injection_path)
                    if evidence['runUID']!=self.execution['runUID'] or evidence['stageID']!=sid: raise ValueError('FrozenInputInvalid: injection publication identity')
                    record=evidence['record']
                except FileNotFoundError: record={}
                original=record.get('outcome','NotStarted')
                injection_outcome={'Complete':'Executed','Executed':'Executed','Cancelled':'Cancelled','NotStarted':'Cancelled'}.get(original,'Invalid')
                reason=record.get('reason') or original
                if original!=injection_outcome and reason!=original: reason=original+': '+reason
                injections.append({'id':plan['id'],'action':plan['action'],'outcome':injection_outcome,'reason':reason,'scheduledOffsetSeconds':plan['offsetSeconds'],'actualOffsetSeconds':record.get('actualOffsetSeconds')})
            cleanup=child['cleanup-jobs']
            stage_outcomes.append({'id':sid,'execution':execution_outcome(child.values()),'measurement':result.get('measurement',{'verdict':'NOT_EVALUATED','reason':'NotStarted'}),'cleanup':cleanup.get('cleanup',not_started),'injections':injections})
            phase_path=scope/'measured/phase.json'
            if not phase_path.exists(): continue
            phase=load(phase_path);judgment=tables.sections(scope/'measured/judgment.json')
            if phase.get('startUTC') and phase.get('endUTC') and judgment:
                bound=self.snapshot['bindings'][sid];requests=tables.publication_requests(judgment)
                definitions={'workloads':self.snapshot['scenarios'][template['loadScenarioRef']['uid']]['workloads']}
                phase_source=next(({key:ref[key] for key in ('store','key')} for ref in artifacts if ref['key'].endswith('/stage/artifacts/measured/phase.json') and '/stages/'+sid+'/' in ref['key']),None)
                self.harness_module.publication_stage_metadata(bound,phase,definitions,
                    self.execution.get('inputSnapshotRef') or self.spec.get('inputSnapshotRef'),
                    phase_source,scope/'measured',self.run,requests)
                power=judgment.get('power') or {}
                kpis={'status':judgment['status'],'requests':requests,'wholePoolJoules':power.get('wholePoolJoules') if power.get('complete') else None,'power':{key:power.get(key) for key in ('scope','complete','wholePoolJoules','energyCoverage')},'limitations':bound['limitations']}
                kpis.update(self.harness_module.publication_measurements(bound,power))
                stages.append({'id':sid,'from':phase['startUTC'],'to':phase['endUTC'],'kpis':kpis})
        generated=now();start=min((stage['from'] for stage in stages),default=generated if prepare_failed else self.snapshot['preparedAt']);end=max((stage['to'] for stage in stages),default=generated)
        prefix='serving/experiments.v1alpha1/'+self.execution['runUID']+'/'
        for step in ('prepare','finish'):
            path=self.run/('publish-run-'+step+'.json')
            try:
                self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/run/'+step+'/result.json'},path)
                value=load(path);procedures[step]=value;artifacts.extend(value['artifacts'])
            except FileNotFoundError: procedures[step]={'execution':{'status':'Failed','reason':'NotStarted'}}
        if prepare_failed:
            start=procedures['prepare'].get('startedAt',generated)
            end=procedures['finish'].get('finishedAt',generated)
            start_time=dt.datetime.fromisoformat(start);end_time=dt.datetime.fromisoformat(end)
            if end_time<=start_time: end=(start_time+dt.timedelta(microseconds=1)).isoformat()
        execution_path=self.run/'publication-execution.json';save(execution_path,{'runUID':self.execution['runUID'],'results':results,'procedures':procedures,'stages':actual_specs,'frozenConfig':self.config})
        index_path=self.run/'publication-evidence-index.json';save(index_path,{'artifacts':artifacts})
        objects=[{'store':self.store.name,'key':prefix+name,'localPath':str(path)} for name,path in (('execution.json',execution_path),('evidence-index.json',index_path))]
        if prepare_failed:
            aggregated={'stages':[],'results':{},'status':'NOT_EVALUATED','reason':procedures['prepare']['execution']['reason']}
        else:
            first_binding=next(iter(self.snapshot['bindings'].values()));save(self.run/'binding.json',first_binding)
            aggregated=self.harness_module.summarize(None,self.run,evaluation={'stages':stages,'results':results})
        measurement=aggregated['status']
        scenarios=[] if prepare_failed else list(self.snapshot['scenarios'].values())
        definitions={workload['uid']:workload for scenario in scenarios for workload in scenario['workloads']}
        definition={'schema':'evaluation-definition.v1alpha1','run':{'name':self.execution['runName'],'uid':self.execution['runUID']},'spec':run_spec,'loadScenarios':[{'name':s['ref']['name'],'uid':s['ref']['uid'],'spec':s['spec']} for s in scenarios],'workloads':list(definitions.values())}
        finish=procedures['finish']
        children=self.execution['childRefs']
        created_ids={child['stageID'] for child in children}
        cleanup_results=[finish]+[procedures[child['stageID']]['cleanup-jobs'] for child in children]
        cleanup=execution_outcome([entry for value in cleanup_results for entry in ({'execution':value.get('cleanup',not_started)},value)]+[{'execution':child.get('cleanup',not_started)} for child in children])
        uncertain=next((child for child in children if child['uncertain']),None)
        if uncertain:
            reason=uncertain['execution']['reason']
            cleanup={'status':'Failed','reason':reason if reason in ('ExecutionLost','RemoteEffectUnknown') else 'RemoteEffectUnknown'}
        elif any('Unknown' in injection['reason'] or 'Requested' in injection['reason'] for stage in stage_outcomes if stage['id'] in created_ids for injection in stage['injections']):
            cleanup={'status':'Failed','reason':'RemoteEffectUnknown'}
        if cleanup['status']!='Complete': cleanup={**cleanup,'status':'Failed'}
        elif not children: cleanup=finish['cleanup']
        unjudged=next((stage['measurement'] for stage in stage_outcomes if stage['measurement']['verdict']=='NOT_EVALUATED'),None)
        measured=verdict(aggregated)
        if measured['verdict']=='NOT_EVALUATED' and unjudged: measured=unjudged
        outcome={'execution':execution_outcome([procedures['prepare'],finish]+[{'execution':stage['execution']} for stage in stage_outcomes]),'measurement':measured,'cleanup':cleanup,'stages':stage_outcomes}
        residuals=[];seen_refs=set()
        def collect_residuals(value,reason='ResidualState'):
            if not isinstance(value,dict): return
            reason=value.get('reason') or reason
            failed=any(value.get(axis,{}).get('status')=='Failed' for axis in ('execution','cleanup')) or value.get('status')=='Failed'
            for residual in value.get('residuals',[]) if failed else []:
                if residual['kind'] in ('Node','Pod','Job','SchedulerSettings'):
                    item={key:residual[key] for key in ('kind','name')};item['reason']=residual.get('reason') or reason
                    if item not in residuals: residuals.append(item)
            reference=value.get('residualStateRef')
            if reference and reference['key'] not in seen_refs:
                seen_refs.add(reference['key']);path=self.run/('publication-residual-'+str(len(seen_refs))+'.json')
                self.store.get(reference,path);collect_residuals(load(path),reason)
            record=value.get('record',{})
            if record.get('outcome') in ('Unknown','Requested'):
                for key,kind in (('nodeRef','Node'),('podRef','Pod')):
                    if record.get(key,{}).get('name'):
                        item={'kind':kind,'name':record[key]['name'],'reason':record.get('reason') or 'RemoteEffectUnknown'}
                        if item not in residuals: residuals.append(item)
            for key in ('details','cleanup'):
                collect_residuals(value.get(key),reason)
        collect_residuals(finish)
        path=self.run/'publication-run-residual.json'
        try: self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/run/residual.json'},path)
        except FileNotFoundError: pass
        else: collect_residuals(load(path))
        for child in children:
            sid=child['stageID'];collect_residuals(procedures[sid]['cleanup-jobs'])
            path=self.run/('publication-finalizer-'+sid+'.json')
            try: self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/stages/'+sid+'/finalizer.json'},path)
            except FileNotFoundError: continue
            collect_residuals(load(path))
        outcome['cleanup']={**outcome['cleanup'],'residuals':residuals}
        summary={'schemaVersion':'serving-experiment.v1alpha1','id':self.execution['runUID'],'from':start,'to':end,'status':measurement,'stages':stages,'sources':[{key:obj[key] for key in ('store','key')} for obj in objects],'definition':definition,'outcome':outcome}
        summary_path=self.run/'publication-summary.json';save(summary_path,summary)
        manifest=self.run/'publication-manifest.json';save(manifest,{'objects':objects,'summary':{'store':self.store.name,'key':prefix+'summary.json','localPath':str(summary_path)}})
        (self.run/'tables.txt').write_text(tables.render(self.run,REPO,evaluation=aggregated))
        private={'logicalStore':self.store.name,'bucket':os.environ['CHILL_EVIDENCE_BUCKET'],'endpoint':os.environ['CHILL_EVIDENCE_ENDPOINT'],'region':os.environ['CHILL_EVIDENCE_REGION'],'accessKeyID':os.environ['AWS_ACCESS_KEY_ID'],'secretAccessKey':os.environ['AWS_SECRET_ACCESS_KEY'],'tempDir':str(self.run.parent.parent/'.evidence-tmp')}
        Path(private['tempDir']).mkdir(mode=0o700,parents=True,exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='evaluation-publish-',dir='/private') as directory:
            credentials=Path(directory)/'config.json';credentials.write_text(json.dumps(private));credentials.chmod(0o600)
            result=subprocess.run([str(REPO/'bin/evaluation-publish'),'--config',str(credentials),'--manifest',str(manifest)],capture_output=True,text=True)
            if result.returncode: raise RuntimeError('PublicationFailed: '+result.stderr.strip())
        self.outputs={'summary':{'store':self.store.name,'key':prefix+'summary.json','size':summary_path.stat().st_size}}
    def execute(self,step):
        self.step=step;self.outputs={};self.measurement=None;self.residuals=[];self.reason='ProcedureComplete';started=now();status='Complete';reason='ProcedureComplete';artifacts=[]
        before={path:(path.stat().st_mtime_ns,path.stat().st_size) for path in self.root.rglob('*') if path.is_file()}
        message=''
        try:
            save(self.root/'execution-spec.json',self.spec)
            save(self.root/'execution.json',self.execution)
            if step!='prepare' and not (step in ('finish','publish') and self.execution.get('prepareCompleted') is False): self.open_snapshot()
            getattr(self,step.replace('-','_'))()
            reason=self.reason
        except Exception as error:
            status='Failed';candidate=str(error).split(':',1)[0]
            reason=candidate if len(candidate)<=1024 and re.fullmatch(r'[A-Z][a-zA-Z0-9]*',candidate) else 'ProcedureFailed'
            if isinstance(error,(FileNotFoundError,KeyError)): reason='MissingInput'
            elif isinstance(error,json.JSONDecodeError): reason='FrozenInputInvalid'
            message=str(error)[:32768]
            save(self.root/(step+'-error.json'),{'at':now(),'reason':reason,'error':str(error)})
            print(step+' failed: '+reason+': '+str(error),flush=True)
        try:
            for path in sorted(self.root.rglob('*')):
                if not path.is_file() or path.is_symlink() or not path.stat().st_size or 'results' in path.relative_to(self.root).parts: continue
                if before.get(path)==(path.stat().st_mtime_ns,path.stat().st_size): continue
                relative=path.relative_to(self.root).as_posix()
                if self.scope=='run' and relative.startswith('stages/'): continue
                artifacts.append(self.upload(path,self.prefix+'/'+step+'/artifacts/'+relative))
            record=result_record(self.execution,step,started,status,reason,self.measurement,artifacts,self.outputs)
            if message: record['execution']['message']=message
            if self.residuals: record['residuals']=self.residuals
            path=self.run/'results'/self.scope/(step+'.json');save(path,record)
            self.upload(path,self.prefix+'/'+step+'/result.json')
            return exit_code(record)
        except Exception as error:
            print('EvidenceStoreFailed: '+str(error),flush=True);return 1


def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('step',choices=STEPS);parser.add_argument('--run',type=Path,required=True);parser.add_argument('--spec',type=Path,required=True)
    args=parser.parse_args()
    try: return Harness(args.run,args.spec).execute(args.step)
    except Exception as error: print(str(error),flush=True);return 1

if __name__=='__main__': raise SystemExit(main())
