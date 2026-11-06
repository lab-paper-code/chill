"""Frozen evaluation input preparation and environment observations."""
import copy
import datetime as dt
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import ssl
import subprocess
import tempfile
import urllib.request

HERE=Path(__file__).resolve().parent
REPO=HERE.parent.parent
TOKEN='/var/run/secrets/kubernetes.io/serviceaccount/token'
CA='/etc/chill/ca/tls.crt'
TIMEBOXES = {'setupSeconds': 300, 'barrierSeconds': 300, 'drainSeconds': 120, 'flushSeconds': 5,
             'collectSeconds': 300, 'apiSeconds': 30, 'watchSeconds': 60, 'gateSeconds': 300}


def now(): return dt.datetime.now(dt.timezone.utc).isoformat()
def load(path): return json.loads(Path(path).read_text())
def save(path,value):
    path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    temporary=path.with_name(path.name+'.tmp');temporary.write_text(json.dumps(value,indent=2,allow_nan=False)+'\n');temporary.replace(path)
def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value

AUTH=module('evaluation_auth',HERE/'auth.py')


def api(config,path):
    request=urllib.request.Request(config['chillAPIURL'].rstrip('/')+path,headers={'Authorization':'Bearer '+Path(TOKEN).read_text().strip()})
    opener=urllib.request.build_opener(AUTH.NoRedirect(),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=CA)))
    with opener.open(request,timeout=30) as response: return json.load(response)


def kubectl(namespace,*args):
    result=subprocess.run(['/usr/bin/kubectl','-n',namespace,*args,'-o','json'],capture_output=True,text=True,timeout=60)
    if result.returncode: raise RuntimeError('TargetUnavailable: '+result.stderr.strip())
    return json.loads(result.stdout)


def trace_workspace(pod):
    volume = next((volume for volume in pod['spec']['volumes'] if volume['name'] == 'work'), {})
    mounts = [mount for container in pod['spec']['containers'] for mount in container.get('volumeMounts', [])
              if mount['name'] == 'work']
    if 'persistentVolumeClaim' not in volume or len(mounts) != 1 or any(key in mounts[0] for key in ('subPath', 'subPathExpr')):
        raise ValueError('FrozenInputInvalid: arrivals require the evaluator workspace PVC mount')
    return {'claimName': volume['persistentVolumeClaim']['claimName'], 'mountPath': mounts[0]['mountPath']}


def pod_environment(items, nodes):
    """Freeze only active Pods actually assigned to participating nodes."""
    return [{'namespace': pod['metadata']['namespace'], 'name': pod['metadata']['name'],
             'uid': pod['metadata']['uid'], 'node': pod['spec']['nodeName'], 'phase': pod['status']['phase'],
             'ownerReferences': [{key: ref[key] for key in ('apiVersion', 'kind', 'name', 'uid', 'controller') if key in ref}
                                 for ref in pod['metadata'].get('ownerReferences', [])],
             'mirror': 'kubernetes.io/config.mirror' in pod['metadata'].get('annotations', {})}
            for pod in items if pod.get('spec', {}).get('nodeName') in nodes and
            pod.get('status', {}).get('phase') in ('Running', 'Pending')]


def competing_pods(current, baseline, job_uids, workload_uids, run_namespace):
    """Resident UIDs are environment facts, not an ownership/namespace guess."""
    initial = {pod['uid'] for pod in baseline}
    intruders = []
    for pod in current:
        if pod['uid'] in initial or pod['mirror']:
            continue
        refs = pod['ownerReferences']
        if any(ref.get('apiVersion') == 'apps/v1' and ref.get('kind') == 'DaemonSet' for ref in refs):
            continue
        if pod['namespace'] == run_namespace and any(ref.get('apiVersion') == 'batch/v1' and
                ref.get('kind') == 'Job' and ref.get('uid') in job_uids for ref in refs):
            continue
        # Normal target autoscaling belongs to the measured system. Reuse the
        # controller's unique Workload owner-reference predicate (a08fda9d).
        workloads = [ref for ref in refs if ref.get('kind') == 'Workload' and
                     ref.get('apiVersion') == 'edge.dacs.io/v1alpha1']
        if len(workloads) == 1 and workloads[0].get('uid') in workload_uids:
            continue
        intruders.append(pod)
    return intruders


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
    ignored = sorted(set(operational) - {'timeboxes', 'workloads', 'collection'})
    timeboxes = dict(TIMEBOXES)
    ignored.extend('timeboxes.' + key for key in set(operational.get('timeboxes', {})) - set(timeboxes))
    timeboxes.update({key: value for key, value in operational.get('timeboxes', {}).items() if key in timeboxes})
    if 'drainTimeoutSeconds' in config:
        timeboxes['drainSeconds'] = float(config['drainTimeoutSeconds'])
    if any(type(value) not in (int, float) or not math.isfinite(value) or value <= 0 for value in timeboxes.values()):
        raise ValueError('FrozenInputInvalid: positive finite timeboxes required')
    generator = copy.deepcopy(spec['loadGenerator'])
    resources = generator['resources']
    if (not isinstance(generator.get('nodeName'), str) or not generator['nodeName'] or not isinstance(resources, dict) or set(resources) - {'requests', 'limits'} or
            any(not isinstance(values, dict) or set(values) - {'memory'} for values in resources.values())):
        raise ValueError('FrozenInputInvalid: generator nodeName and memory-only resources required')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,39}', spec['row']):
        raise ValueError('FrozenInputInvalid: safe row label required')
    workloads = []
    allowed = {'producerEndpoint', 'producerTokenFile', 'producerCAFile', 'deliverySeconds', 'socketSeconds',
               'workers', 'lifetimeSeconds', 'gatewayCapSeconds', 'podSpec', 'volumeMounts'}
    for pinned in scenario['workloads']:
        operation = operational['workloads'][pinned['name']]
        ignored.extend('workloads.' + pinned['name'] + '.' + key for key in set(operation) - allowed)
        operation = {key: value for key, value in operation.items() if key in allowed}
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
    ignored.extend('collection.' + key for key in set(collection) - {'maximumDurationSeconds'})
    collection = {key: value for key, value in collection.items() if key == 'maximumDurationSeconds'}
    if 'maximumDurationSeconds' in collection and (type(collection['maximumDurationSeconds']) not in (int, float) or
                                                   not math.isfinite(collection['maximumDurationSeconds']) or collection['maximumDurationSeconds'] <= 0):
        raise ValueError('FrozenInputInvalid: positive collection duration bound required')
    return {'namespace': execution['namespace'], 'endpoint': config['chillAPIURL'], 'caFile': CA,
            'ignoredKeys': sorted(ignored), 'failurePolicy': 'v2',
            'tokenFile': TOKEN, 'collectorTokenFile': TOKEN, 'timeboxes': timeboxes,
            'workloads': workloads,
            'loadGenerator': generator, 'seeds': scenario['spec']['seeds'], 'arrivals': scenario['spec']['arrivalDistribution'],
            'traceOffsets': scenario.get('traceOffsets', {}),
            'traceVolume': copy.deepcopy(config.get('traceVolume')),
            'stages': [{'id': spec['row'], 'parts': [{'id': p['id'], 'seconds': p['durationSeconds'], 'rates': p['rates']} for p in scenario['spec']['parts']]}],
            'generatorQuality': {'latenessToleranceNS': quality['latenessToleranceNanos'], 'maxExceedingFraction': quality['maxExceedingFraction']},
            'collection': {**collection, 'energyCoverage': spec['judgment']['energyCoverage'], 'powerMeasurement': spec['powerMeasurement']},
            'evaluation': {'execution': execution, 'sourceSHA': spec['harness'].get('sourceSHA'),
                           'minimumFreeBytes': int(config['minimumFreeBytes']), 'stageFolder': str(root / 'measured'),
                           'settingsPatch': spec.get('settingsPatch', {}), 'barrier': generator['barrier'],
                           'failureInjection': spec.get('failureInjection', [])}}


def prepare(context):
    usage=os.statvfs(context.run)
    if usage.f_bavail*usage.f_frsize<int(context.config['minimumFreeBytes']):
        raise ValueError('EvidenceCapacityInsufficient: workspace free bytes')
    context.environment_start = {'capturedAt': now(), 'pods': participant_pods(context, context.spec)}
    save(context.root / 'environment-start.json', context.environment_start)
    scenarios = {}
    context.scenarios = scenarios
    context.harness_module = module('evaluation_harness', HERE / 'conductor.py')
    save(context.root / 'source-observations.json', {'declaredSourceSHA': context.spec['harness'].get('sourceSHA'),
                                                'imageSourceSHA': os.environ.get('CHILL_HARNESS_SOURCE_SHA')})
    for template in context.spec['stages']:
        pin = template['loadScenarioRef']
        identity = pin['uid']
        if identity in scenarios:
            continue
        obj = kubectl(context.execution['namespace'], 'get', 'loadscenario', pin['name'])
        if obj['metadata']['uid'] != identity:
            raise ValueError('IdentityMismatch: LoadScenario UID')
        scenario = obj['spec']
        folder = context.inputs / identity
        folder.mkdir(parents=True, exist_ok=True)
        names = [w['name'] for w in scenario['workloads']]
        context.harness_module.parse_seeds(scenario['seeds'], names)
        if set(scenario['datasetRefs']) != set(names) or set(scenario['arrivalDistribution']) != set(names):
            raise ValueError('FrozenInputInvalid: workload key coverage')
        workloads, datasets, fields = [], {}, {}
        for workload_pin in scenario['workloads']:
            view = api(context.config, '/api/v1/workloads/' + workload_pin['name'])
            applied = view.get('appliedDefinition') or {}
            actual = {key: applied.get(key) for key in ('name', 'uid', 'generation', 'slo', 'model', 'preprocessor', 'dataset')}
            if any(actual.get(key) != workload_pin[key] for key in ('name', 'uid')):
                raise ValueError('IdentityMismatch: applied Workload ' + workload_pin['name'])
            actual['requestedGeneration'] = workload_pin.get('generation')
            workloads.append(actual)
            dataset_pin = scenario['datasetRefs'][workload_pin['name']]
            if set(dataset_pin) != {'name', 'uid'}:
                raise ValueError('FrozenInputInvalid: Dataset reference fields')
            dataset = kubectl(context.execution['namespace'], 'get', 'dataset', dataset_pin['name'])
            if dataset['metadata']['uid'] != dataset_pin['uid']:
                raise ValueError('IdentityMismatch: Dataset UID')
            source = dataset['spec']['source']
            if not source['image'] or not source['path'].startswith('/') or '..' in Path(source['path']).parts:
                raise ValueError('FrozenInputInvalid: Dataset carrier and safe absolute path required')
            declared = kubectl(context.execution['namespace'], 'get', 'workload', workload_pin['name'])
            if declared['metadata']['uid'] != workload_pin['uid']:
                raise ValueError('IdentityMismatch: Workload resource UID')
            preprocessor = kubectl(context.execution['namespace'], 'get', 'preprocessor', declared['spec']['preprocessorRef']['name'])
            required_fields = [field['name'] for field in preprocessor['spec']['input']['fields']]
            if not required_fields or len(set(required_fields)) != len(required_fields) or not set(required_fields) <= {f['name'] for f in dataset['spec']['fields']}:
                raise ValueError('FrozenInputInvalid: Dataset/preprocessor record fields')
            datasets[workload_pin['name']] = dataset
            fields[workload_pin['name']] = required_fields
        traces = {}
        for name, reference in scenario.get('traceRefs', {}).items():
            path = folder / ('trace-' + name + '.json')
            context.store.get(reference, path)
            traces[name] = load(path)
        if 'traceVolume' not in context.config:
            context.config['traceVolume'] = trace_workspace(kubectl(context.execution['namespace'], 'get', 'pod', os.environ['HOSTNAME']))
        scenarios[identity] = {'ref': {'name': obj['metadata']['name'], 'uid': identity}, 'spec': scenario,
                               'workloads': workloads, 'datasets': datasets, 'fields': fields, 'traceOffsets': traces}
    scientific = {'nodes': [node['name'] for node in context.spec['powerMeasurement']['nodes']]}
    environment(context.run, scientific)
    context.run.mkdir(parents=True, exist_ok=True)
    started = now()
    bindings, targets = {}, {}
    for template in context.spec['stages']:
        stage = {**{key: context.spec[key] for key in ('target', 'harness', 'loadGenerator', 'powerMeasurement', 'judgment', 'evidenceStore')}, 'settingsPatch': context.spec.get('settingsPatch', {}), **template}
        targets[template['id']] = check_target(context, stage['target'])
        check_fleet(context, stage)
        root = context.run / 'stages' / template['id']
        root.mkdir(parents=True, exist_ok=True)
        config = stage_config(stage, scenarios[template['loadScenarioRef']['uid']], context.execution, context.config, root)
        path = root / 'prepared-config.json'
        save(path, config)
        bindings[template['id']] = context.harness_module.bind(path)
    snapshot = {'schemaVersion': 'evaluation-snapshot.v2alpha1', 'inputsVersion': 'evaluation-inputs.v2alpha1',
                'runUID': context.execution['runUID'], 'runSpec': context.spec, 'scenarios': scenarios,
                'environment': scientific, 'environmentStart': context.environment_start,
                'bindings': bindings, 'targets': targets, 'generatorNode': context.generator_node,
                'preparedAt': started, 'config': context.config}
    path=context.inputs/'snapshot.json';save(path,snapshot)
    ref=context.upload(path,'experiments/'+context.execution['runUID']+'/run/prepare/input-snapshot.json')
    context.outputs={'inputSnapshot':ref}


def check_target(context, target):
    identities = []
    for component in target['components']:
        resource = component['resource']
        obj = kubectl(resource['namespace'], 'get', resource['kind'], resource['name'])
        if resource.get('uid') and obj['metadata']['uid'] != resource['uid']:
            raise ValueError('IdentityMismatch: external target UID')
        stage_id = context.execution.get('stageID')
        expected = next((item for item in getattr(context, 'snapshot', {}).get('targets', {}).get(stage_id, [])
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
        available = bool(current) and all(pod.get('status', {}).get('containerStatuses') and
            all(status.get('ready') for status in pod['status']['containerStatuses']) and
            any(c['type']=='Ready' and c['status']=='True' for c in pod.get('status',{}).get('conditions',[])) for pod in current)
        identities.append({'name': component['name'], 'uid': obj['metadata']['uid'], 'resource': resource, 'available': available,
                           'pods': [{'uid': pod['metadata']['uid'], 'containers': pod.get('status', {}).get('containerStatuses', [])} for pod in current]})
    return identities


def participant_pods(context, spec):
    nodes = list(spec['powerMeasurement']['nodes'])
    generator = getattr(context, 'snapshot', {}).get('generatorNode', getattr(context, 'generator_node', None))
    if generator is None:
        node = kubectl(context.execution['namespace'], 'get', 'node', spec['loadGenerator']['nodeName'])
        generator = {key: node['metadata'][key] for key in ('name', 'uid')}
    context.generator_node = generator
    nodes.append(generator)
    for pin in nodes:
        node = kubectl(context.execution['namespace'], 'get', 'node', pin['name'])
        if node['metadata']['uid'] != pin['uid']:
            raise ValueError('IdentityMismatch: participating Node UID')
        ready = any(c['type'] == 'Ready' and c['status'] == 'True' for c in node.get('status', {}).get('conditions', []))
        path = context.root / ('node-' + pin['name'] + '-observations.json')
        history = load(path) if path.exists() else []
        history.append({'at': now(), 'node': pin, 'ready': ready})
        save(path, history)
    result = []
    for name in sorted({pin['name'] for pin in nodes}):
        items = kubectl(context.execution['namespace'], 'get', 'pods', '--all-namespaces',
                        '--field-selector', 'spec.nodeName=' + name)['items']
        result.extend(pod_environment(items, {name}))
    return result


def check_fleet(context, spec):
    current = participant_pods(context, spec)
    baseline = context.snapshot['environmentStart'] if hasattr(context, 'snapshot') else context.environment_start
    stages = {uid for uid in [context.execution.get('stageUID'),
                             *(child['uid'] for child in context.execution.get('childRefs', []))] if uid}
    jobs = kubectl(context.execution['namespace'], 'get', 'jobs', '-l',
                   'edge.dacs.io/evaluation-run-uid=' + context.execution['runUID'])['items']
    job_uids = {job['metadata']['uid'] for job in jobs
                if job['metadata'].get('labels', {}).get('edge.dacs.io/evaluation-run-uid') == context.execution['runUID'] and
                any(ref.get('apiVersion') == 'edge.dacs.io/v1alpha1' and ref.get('controller') is True and
                    ((ref.get('kind') == 'EvaluationRun' and ref.get('uid') == context.execution['runUID']) or
                     (ref.get('kind') == 'EvaluationStage' and ref.get('uid') in stages))
                    for ref in job['metadata'].get('ownerReferences', []))}
    scenarios = context.snapshot['scenarios'] if hasattr(context, 'snapshot') else context.scenarios
    workload_uids = {workload['uid'] for workload in scenarios[spec['loadScenarioRef']['uid']]['spec']['workloads']}
    intruders = competing_pods(current, baseline['pods'], job_uids, workload_uids, context.execution['namespace'])
    path = context.root / 'environment-observations.json'
    history = load(path) if path.exists() else []
    history.append({'at': now(), 'pods': current, 'newCompetingPods': intruders})
    save(path, history)
    if intruders:
        save(context.root / 'environment-intrusions.json',
             {'detectedAt': now(), 'baselineCapturedAt': baseline['capturedAt'], 'pods': intruders})


def open_snapshot(context):
    path=context.inputs/'snapshot.json'
    reference=context.execution.get('inputSnapshotRef') or context.spec.get('inputSnapshotRef')
    if context.step=='finish' and context.execution.get('prepareCompleted') is True:
        try:
            if not reference: raise ValueError('missing snapshot reference')
            context.inputs.mkdir(parents=True,exist_ok=True)
            with tempfile.TemporaryDirectory(prefix='finish-snapshot-',dir=context.inputs) as directory:
                downloaded=Path(directory)/'snapshot.json'
                context.store.get(reference,downloaded)
                context.snapshot=load(downloaded)
        except Exception as error: raise RuntimeError('EvidenceUnreadable: '+str(error)) from error
    else:
        if not path.exists(): context.store.get(reference,path)
        context.snapshot=load(path)
    try:
        if context.snapshot.get('schemaVersion') != 'evaluation-snapshot.v2alpha1' or context.snapshot.get('inputsVersion') != 'evaluation-inputs.v2alpha1' or context.snapshot.get('runUID') != context.execution['runUID']:
            raise ValueError('FrozenInputInvalid: input snapshot execution identity')
    except Exception as error:
        if context.step=='finish': raise RuntimeError('EvidenceUnreadable: '+str(error)) from error
        raise
    if context.execution.get('stageID'):
        if not any(stage['id']==context.execution['stageID'] for stage in context.snapshot['runSpec']['stages']):
            raise ValueError('FrozenInputInvalid: execution stage absent from snapshot')
        if context.spec['stageID']!=context.execution['stageID']:
            raise ValueError('FrozenInputInvalid: child stage ID differs from execution')
    environment(context.run,context.snapshot['environment']);context.harness_module=module('evaluation_harness',HERE/'conductor.py')
