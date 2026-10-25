#!/usr/bin/env python3
"""Short CPO demo: prepare -> start -> stage/run -> collect -> summary -> finish.

Reuses E5's authenticated API, producer/barrier/counter lifecycle, raw collection
and owned cleanup, but not its normative R/G/10800-second scientific gates.
All outputs are PRIVATE operational evidence below paths.campaign. --help and
--self-check need no experiment environment, cluster, images, or credentials.
"""
import argparse
import copy
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import sys

HERE = Path(__file__).resolve().parent
ALIASES = {'mobilenet': 'image-classification-tight-slo', 'smolvlm2': 'smolvlm2-vqa', 'plc': 'plc-anomaly'}


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


ANALYSIS = module('demo_statistics', HERE / 'd2/analyze_before.py')
epoch = ANALYSIS.epoch


def schedule(config, e5):
    sys.path.insert(0, str(e5.REPO / 'load-generator/src'))
    from chill_loadgen import cli
    stages = []
    if not isinstance(config.get('stages'), list) or not config['stages']:
        raise ValueError('A nonempty stages list is required')
    for row in config['stages']:
        label, seconds, rates = row['id'], row['seconds'], row['rates']
        if not isinstance(label, str) or not re.fullmatch('[A-Za-z0-9][A-Za-z0-9-]{0,39}', label) or any(s['id'].lower() == label.lower() for s in stages):
            raise ValueError('Unique short Kubernetes-safe stage IDs required')
        if not e5.positive(seconds) or set(rates) != set(ALIASES):
            raise ValueError('Positive seconds and mobilenet/smolvlm2/plc rates required')
        if any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in rates.values()):
            raise ValueError('Rates must be finite nonnegative requests/second')
        stage = {'id': label, 'seconds': seconds, 'rates': {ALIASES[k]: v for k, v in rates.items()}, 'traces': {}}
        stage['parts'] = [{'id': label, 'seconds': seconds, 'rates': stage['rates']}]
        for name, rate in stage['rates'].items():
            offsets = cli.arrival_offsets_ns('poisson', rate, seconds, e5.SEEDS[name]) if rate else []
            stage['traces'][name] = {'seed': e5.SEEDS[name], 'offsetsNS': offsets, 'offered': len(offsets), 'arrivalSHA256': hashlib.sha256(json.dumps(offsets, separators=(',', ':')).encode()).hexdigest()}
        stages.append(stage)
    if any(stages[0]['rates'].values()):
        raise ValueError('First stage must be zero-load autonomous bootstrap')
    return stages


def assemble(e5, args, spec):
    """Register missing canonical definitions, then capture actual applied inputs."""
    if args.out.exists() and any(args.out.iterdir()):
        raise RuntimeError('Live preparation requires a fresh output directory; retain partial captures')
    recipe_path = Path(spec.get('operationalRecipe', e5.OPERATIONAL_RECIPE)).resolve()
    op = e5.load(recipe_path)
    e5.need(spec, ['controlSourceSHA', 'timeboxes', 'measurementSLO'], 'demo config')
    e5.need(spec['timeboxes'], e5.REQUIRED['timeboxes'], 'timeboxes')
    if any(not e5.positive(v) for v in spec['timeboxes'].values()):
        raise ValueError('Finite positive demo timeboxes required')
    if set(spec['measurementSLO']) != set(ALIASES):
        raise ValueError('Measurement SLO required for each workload alias')
    for slo in spec['measurementSLO'].values():
        if not e5.positive(slo['targetLatencyNanos']) or not 0 < slo['percentile'] <= 100:
            raise ValueError('Positive measurement SLO latency and percentile in (0,100] required')
    source = e5.subprocess.check_output(['git', '-C', str(e5.REPO), 'rev-parse', '--verify', spec['controlSourceSHA'] + '^{commit}'], text=True).strip()
    args.out.mkdir(parents=True)
    d = e5.module('demo_capture', HERE / 'd2/conduct_before.py')
    d.BASE, d.CA, d.TOKEN = op['endpoint'], Path(op['caFile']), Path(op['tokenFile'])
    d.K = e5.ENVIRONMENT['d2']['kubectl'] + ['-n', 'chill-system']
    io = object.__new__(d.Conductor)
    io.a = argparse.Namespace(command='demo-prepare', phase='demo-prepare')
    io.out = io.phase_out = args.out
    io.source, io.journal, io.journal_lock = source, args.out / 'prepare-commands.jsonl', e5.threading.Lock()
    io.ctx = e5.ssl.create_default_context(cafile=op['caFile'])
    io.http = e5.urllib.request.build_opener(d.NoRedirect(), e5.urllib.request.HTTPSHandler(context=io.ctx))
    timeout = spec['timeboxes']['apiSeconds']
    scheduler = io.api('GET', '/api/v1/scheduler', timeout=timeout)
    nodes = io.k(['get', 'nodes', '-o', 'json'], timeout=timeout)
    pods = io.k(['get', 'pods', '-A', '-o', 'json'], timeout=timeout)
    e5.save(args.out / 'scheduler-before-register.json', scheduler)
    for name, value in [('nodes', nodes), ('pods', pods)]:
        e5.save(args.out / (name + '.json'), value)
    servers = [p for p in pods['items'] if p['metadata'].get('namespace') == 'chill-system' and p['metadata'].get('labels', {}).get('app.kubernetes.io/component') == 'serving-entrypoint']
    if len(servers) != 1:
        raise RuntimeError('Actual serving-entrypoint Pod is ambiguous')
    container = next(c for c in servers[0]['spec']['containers'] if c['name'] == op['collection']['serverContainer'])
    status = next(c for c in servers[0]['status']['containerStatuses'] if c['name'] == container['name'])
    if not status.get('ready') or 'sha256:' not in status.get('imageID', ''):
        raise RuntimeError('Actual Ready serving image digest required')
    flags = dict(arg.split('=', 1) for arg in container['args'] if arg.startswith('--') and '=' in arg)
    cap = flags['--request-timeout']
    if not cap.endswith('s') or not e5.positive(float(cap[:-1])):
        raise ValueError('Actual positive server request-timeout in seconds required')
    prototype = io.job('demo-timing', 0, 1)['spec']['template']['spec']['containers'][0]['args']
    timing = {flag: prototype[prototype.index(flag) + 1] for flag in ('--workers', '--request-lifetime', '--delivery-margin', '--timeout')}
    wire = e5.module('demo_templates', HERE / 'e5-wire.py')
    registration = object.__new__(wire.Prepare)
    registration.op, registration.out, registration.tb, registration.http = op, args.out, spec['timeboxes'], io.http
    by_name = {w['name']: w for w in scheduler['state']['snapshot']['workloads']}
    registration.n = {'workloads': {name: {'slo': by_name[name]['slo'] if name in by_name else spec['measurementSLO'][next(alias for alias, canonical in ALIASES.items() if canonical == name)]} for name in e5.WORKLOADS}}
    def registration_api(method, path, body=None, headers=None, accepted=(200,)):
        if method == 'PUT':
            raise RuntimeError('Existing canonical SLO changed during capture; no SLO mutation permitted')
        result, status_code = wire.Prepare.api(registration, method, path, body, headers, accepted)
        io.record({'kind': 'canonical-management', 'method': method, 'path': path, 'status': status_code})
        return result, status_code
    registration.api = registration_api
    scheduler = registration.canonical()
    e5.save(args.out / 'scheduler.json', scheduler)
    for name in e5.WORKLOADS:
        actual, _ = registration.api('GET', '/api/v1/workloads/' + name)
        e5.save(args.out / ('canonical-' + name + '.json'), actual)
    collection = {**op['collection'], 'powerExecutable': e5.E5['powerExecutable'], 'powerReceipt': e5.E5['powerReceipt']}
    for key, flag in [('observationDirectory', '--observation-directory'), ('schedulerConfigMap', '--serving-scheduler-configmap'), ('metricSourcesConfigMap', '--profiler-facts-configmap'), ('metricSourcesKey', '--metric-sources-key')]:
        collection[key] = flags.get(flag, collection[key])
    archive_path = args.out / 'required-raw-refs.json'
    e5.save(archive_path, [{'key': 'demo-live-' + name, 'localPath': str(args.out / (name + '.json'))} for name in ('scheduler', 'nodes', 'pods')])
    collection['archiveRefs'] = str(archive_path)
    c = {key: op[key] for key in ('endpoint', 'caFile', 'tokenFile', 'kubeContext')}
    c.update(approval={'sourceSHA': source, 'controlImageDigest': status['imageID']}, inputs={'nodes': str(args.out / 'nodes.json'), 'scheduler': str(args.out / 'scheduler.json')}, timeboxes=spec['timeboxes'], collection=collection, workloads=[])
    for name in e5.WORKLOADS:
        actual = [w for w in scheduler['state']['snapshot']['workloads'] if w['name'] == name]
        if len(actual) != 1:
            raise RuntimeError('Registered canonical workload has no unambiguous applied definition: ' + name)
        actual, recipe = actual[0], op['workloads'][name]
        template = args.out / ('producer-template-' + name + '.json')
        e5.save(template, wire.template(op, name, e5.ENVIRONMENT['d2']['producerImage']))
        alias = next(alias for alias, canonical in ALIASES.items() if canonical == name)
        requested = {'workers': int(timing['--workers']), 'lifetimeSeconds': float(timing['--request-lifetime']), 'deliverySeconds': float(timing['--delivery-margin']), 'socketSeconds': float(timing['--timeout'])}
        requested.update(spec.get('requestTiming', {}).get(alias, {}))
        if any(not e5.positive(requested[key]) for key in ('workers', 'lifetimeSeconds', 'deliverySeconds', 'socketSeconds')) or type(requested['workers']) is not int:
            raise ValueError('Positive configured request timings and integer workers required')
        lifetime = max(requested['lifetimeSeconds'], actual['slo']['targetLatencyNanos'] / 1e9)
        delivery = requested['deliverySeconds']
        c['workloads'].append({**{key: actual[key] for key in ('name', 'uid', 'generation', 'slo')}, 'datasetDir': recipe['datasetDir'], 'fields': recipe['fields'], 'jobTemplate': str(template), 'producerEndpoint': op['producer']['endpoint'], 'producerTokenFile': op['producer']['tokenFile'], 'producerCAFile': op['producer']['caFile'], 'producerDatasetDir': op['producer']['datasetDir'], 'workers': requested['workers'], 'lifetimeSeconds': lifetime, 'deliverySeconds': delivery, 'socketSeconds': max(requested['socketSeconds'], lifetime + 2 * delivery), 'gatewayCapSeconds': float(cap[:-1])})
    c['demoSkipped'] = ['E5 R/kappa capacity binding and G clipping', '10800-second normative schedule and factor baselines', 'phase6 scientific approvals/D1/D6/D9 judgment gates', 'image/source publication and profiling-vs-evaluation metadata Jobs; existing pinned images/datasets reused', 'existing scheduler SLO mutation: existing definitions retained; missing canonical definitions created from recipe plus configured SLO']
    c['demoProvenance'] = {'recipe': str(recipe_path), 'controlSourceSHA': 'Main-provided helm-revision source claim resolved in git, not inferred from image bytes', 'controlImageDigest': 'Live Ready Pod containerStatus.imageID', 'requestTiming': 'D2 Conductor.job baseline plus explicit requestTiming overrides; lifetime/socket extended only to cover actual scheduler SLO', 'measurementSLO': spec['measurementSLO']}
    config_path = args.out / 'demo-e5-config.json'
    e5.save(config_path, c)
    return config_path


def finish_definitions(e5, cpo):
    """Delete exact captured canonical UIDs only after the existing owned cleanup."""
    if not (cpo.out / 'finish-complete.json').exists():
        raise RuntimeError('Owned instances/routing/settings cleanup must complete before definition deletion')
    wire = e5.module('demo_definition_cleanup', HERE / 'e5-wire.py')
    manager = object.__new__(wire.Prepare)
    manager.op, manager.out, manager.tb, manager.http = cpo.c, cpo.out, cpo.tb, cpo.io.http
    for w in cpo.c['workloads']:
        path = '/api/v1/workloads/' + w['name']
        current, _ = manager.api('GET', path)
        metadata = current['desired']['metadata']
        if metadata['uid'] != w['uid'] or metadata['generation'] != w['generation']:
            raise RuntimeError('Canonical definition identity changed; no deletion: ' + w['name'])
        e5.save(cpo.out / ('finish-definition-' + w['name'] + '-before.json'), current)
        result, _ = manager.api('DELETE', path, headers={'If-Match': metadata['resourceVersion']}, accepted=(202,))
        e5.save(cpo.out / ('finish-definition-' + w['name'] + '-ack.json'), result)
    deadline = e5.time.monotonic() + cpo.tb['settleSeconds']
    while True:
        views = {w['name']: manager.api('GET', '/api/v1/workloads/' + w['name'], accepted=(200, 404))[1] for w in cpo.c['workloads']}
        state, _ = manager.api('GET', '/api/v1/scheduler/state')
        e5.save(cpo.out / 'finish-definitions-last-state.json', {'at': e5.now(), 'statuses': views, 'state': state})
        if all(status == 404 for status in views.values()) and not any(w['uid'] in cpo.uids for w in state['snapshot']['workloads']):
            e5.save(cpo.out / 'finish-definitions-complete.json', {'at': e5.now(), 'deletedUIDs': cpo.uids})
            return
        if e5.time.monotonic() >= deadline:
            raise RuntimeError('Canonical definition deletion did not publish within existing settle timebox')
        e5.time.sleep(1)


def prepare(e5, args):
    target = args.out / 'binding.json'
    if target.exists():
        raise RuntimeError('No implicit preparation overwrite; use a fresh output directory')
    args.config = args.config.resolve()
    spec = e5.load(args.config)
    config_path = Path(spec['e5Config']).resolve() if spec.get('e5Config') else assemble(e5, args, spec)
    c = e5.load(config_path)
    e5.need(c, ['approval', 'inputs', 'timeboxes', 'workloads', 'collection', 'endpoint', 'caFile', 'tokenFile'], 'e5Config')
    e5.need(c['approval'], ['sourceSHA', 'controlImageDigest'], 'approval')
    e5.need(c['timeboxes'], e5.REQUIRED['timeboxes'], 'timeboxes')
    if any(not e5.positive(v) for v in c['timeboxes'].values()):
        raise ValueError('Finite positive E5 timeboxes required')
    if len(c['workloads']) != 3 or {w['name'] for w in c['workloads']} != set(ALIASES.values()):
        raise ValueError('Existing three canonical workload definitions required')
    stages = schedule(spec, e5)
    if any(c['timeboxes']['setupSeconds'] + s['seconds'] + c['timeboxes']['drainSeconds'] + c['timeboxes']['flushSeconds'] > 4200 for s in stages):
        raise ValueError('Stage exceeds existing finite collector duration limit')
    sys.path.insert(0, str(e5.REPO / 'load-generator/src'))
    from chill_loadgen import cli
    producer = module('demo_producer', HERE / 'e5-producer.py')
    payload = {}
    sources = {'demoConfig': e5.ref(args.config), 'e5Config': e5.ref(config_path), 'environment': e5.ref(e5.ENVIRONMENT_PATH)}
    for filename in ('demo.py', 'e5.py', 'e5-wire.py', 'e5-producer.py', 'e5-archive.py', 'e5-raw.py'):
        sources[filename] = e5.ref(HERE / filename)
    for w in c['workloads']:
        e5.need(w, [k for k in e5.REQUIRED['workload'] if k not in ('R', 'profilingRecordIDs')], w['name'])
        if any(not e5.positive(w[k]) for k in ('workers', 'lifetimeSeconds', 'deliverySeconds', 'socketSeconds', 'gatewayCapSeconds')) or type(w['workers']) is not int:
            raise ValueError('Positive workload timing and integer workers required')
        if not e5.positive(w['slo']['targetLatencyNanos']) or not 0 < w['slo']['percentile'] <= 100:
            raise ValueError('Valid frozen workload SLO required')
        if w['lifetimeSeconds'] < w['slo']['targetLatencyNanos'] / 1e9 or w['gatewayCapSeconds'] < w['lifetimeSeconds'] or w['socketSeconds'] <= w['lifetimeSeconds'] + w['deliverySeconds']:
            raise ValueError('Require lifetime>=SLO, gateway>=lifetime, socket>lifetime+delivery')
        records = cli.load_records(Path(w['datasetDir']), w['fields'])
        if not records:
            raise ValueError('Nonempty existing evaluation dataset required')
        payload[w['name']] = producer.identity(records)
        job = e5.load(w['jobTemplate'])
        pod = job['spec']['template']['spec']
        if pod['restartPolicy'] != 'Never' or len(pod['containers']) != 1 or pod['containers'][0]['name'] != 'load':
            raise ValueError('Existing single load container/Never Job required')
        if any('@sha256:' not in container['image'] for container in pod['containers'] + pod.get('initContainers', [])):
            raise ValueError('Existing digest-pinned producer/dataset images required; no build')
        sources['job-' + w['name']] = e5.ref(w['jobTemplate'])
    c['smoke'] = stages[0]
    c['harnessRepositoryCommit'] = e5.subprocess.check_output(['git', '-C', str(e5.REPO), 'rev-parse', 'HEAD'], text=True).strip()
    binding = {'schema': 'demo-e5-binding.v1', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': e5.now(), 'config': c, 'sources': sources, 'payload': payload, 'schedule': stages, 'demoConfig': spec}
    e5.save(target, binding)
    print(str(target))


def stage_folder(out, stage):
    return out / ('smoke-' + stage['id'])


def run_stage(cpo, stage):
    index = cpo.b['schedule'].index(stage)
    if index:
        previous = stage_folder(cpo.out, cpo.b['schedule'][index - 1])
        if not (previous / 'collected.json').exists() or cpo.b['schedule'][index - 1]['id'] != cpo.d.load(previous / 'phase.json')['stage']['id'] or cpo.d.load(previous / 'phase.json')['status'] != 'DRAINED_NOT_EVALUATED':
            raise RuntimeError('Previous stage must have drained and collected successfully')
        if not any(cpo.b['schedule'][index - 1]['rates'].values()):
            state = cpo.state()
            missing = []
            for uid in cpo.uids:
                owned = {i['id'] for i in state['snapshot']['instances'] or [] if i.get('ownerWorkloadUID') == uid and i.get('workloadUID') == uid and i.get('ready')}
                if not any(weight > 0 and identifier in owned for identifier, weight in state['snapshot'].get('routing', {}).get(uid, {}).items()):
                    missing.append(uid)
            if missing:
                raise RuntimeError('Bootstrap has no owned Ready positive route: ' + ', '.join(missing))
    if not (cpo.out / 'start-ack.json').exists():
        raise RuntimeError('Explicit zero-instance start is required before stages')
    cpo.a.command = 'smoke'
    cpo.a.stage = stage['id']
    cpo.c['smoke'] = stage
    cpo.run()
    folder = stage_folder(cpo.out, stage)
    for name in stage['rates']:
        path = folder / (name + '-client.jsonl')
        if not stage['rates'][name] and not path.exists():
            path.write_text('')


def resume(e5, cpo):
    """Explicitly re-enable owned autonomy; never overwrite original start evidence."""
    if not (cpo.out / 'initial.json').exists() or not (cpo.out / 'start-ack.json').exists():
        raise RuntimeError('Resume requires the original initial/start evidence; use start for a new run')
    for frozen in cpo.b['sources'].values():
        if e5.ref(frozen['path']) != frozen:
            raise RuntimeError('Resume requires explicitly re-frozen inputs; no automatic source refresh')
    alerts = e5.CAMPAIGN / 'control-alerts.jsonl'
    alert_offset = alerts.stat().st_size
    jobs = cpo.io.k(['get', 'jobs', '-o', 'json'])['items']
    if any(not job.get('spec', {}).get('suspend') and not any(row.get('type') in ('Complete', 'Failed') and row.get('status') == 'True' for row in job.get('status', {}).get('conditions', [])) for job in jobs):
        raise RuntimeError('Resume requires no nonterminal producer/measurement Jobs')
    state, settings = cpo.idle(), cpo.settings()
    if any(i.get('ownerWorkloadUID') not in cpo.uids for i in state['snapshot']['instances'] or []):
        raise RuntimeError('Foreign physical ownership; no demo resume')
    if any(uid not in cpo.uids for uid in settings.get('autonomousWorkloads', [])) or any(uid not in cpo.uids for uid in settings.get('staticRouting') or {}):
        raise RuntimeError('Foreign control declarations; no demo resume')
    folder = cpo.out / ('resume-' + re.sub('[^A-Za-z0-9]', '', e5.now()))
    folder.mkdir()
    e5.save(folder / 'pre-state.json', state)
    e5.save(folder / 'pre-settings.json', settings)
    marker = cpo.out / 'native-mode-authorization.json'
    if marker.exists():
        e5.save(folder / 'prior-native-mode-authorization.json', e5.load(marker))
    settings['autonomousWorkloads'], settings['staticRouting'] = cpo.uids, {}
    settings['excludedNodes'] = e5.E5['excludedNodes']
    cpo.io.a.command, cpo.io.a.phase = 'resume', 'e5-demo-resume'
    cpo.native_evidence = state.get('results') or []
    cpo.authorize_native_modes()
    e5.save(folder / 'resume-ack.json', cpo.put(settings, 'Approved demo resume from owned current topology; preserve original initial/start evidence'))
    if not state['snapshot']['instances']:
        bootstrap = copy.deepcopy(cpo.b['schedule'][0])
        bootstrap['id'] = 'E0-' + folder.name
        cpo.c['smoke'], cpo.a.stage, cpo.a.command = bootstrap, bootstrap['id'], 'smoke'
        cpo.run()
        e5.save(folder / 'bootstrap.json', {'stage': bootstrap, 'phaseFolder': str(stage_folder(cpo.out, bootstrap)), 'meaning': 'Fresh zero-load bootstrap; original E0 evidence retained'})
    deadline = e5.time.monotonic() + cpo.tb['settleSeconds']
    while True:
        alert_offset = cpo.monitor(alert_offset, folder)
        current = cpo.state()
        e5.save(folder / 'last-state.json', current)
        if any(i.get('ownerWorkloadUID') not in cpo.uids for i in current['snapshot']['instances'] or []):
            raise RuntimeError('Foreign physical ownership appeared during demo resume')
        ready = True
        for uid in cpo.uids:
            owned = {i['id'] for i in current['snapshot']['instances'] or [] if i.get('ownerWorkloadUID') == uid and i.get('workloadUID') == uid and i.get('ready')}
            ready = ready and any(weight > 0 and identifier in owned for identifier, weight in current['snapshot'].get('routing', {}).get(uid, {}).items())
        if ready and not current['snapshot'].get('inProgressTarget') and not any(action.get('status') in ('running', 'cancel') for action in current.get('actions') or []):
            e5.save(folder / 'ready.json', current)
            print('Resumed owned autonomous topology: ' + str(folder))
            return
        if e5.time.monotonic() >= deadline:
            raise RuntimeError('Resume Ready/positive routing did not converge within existing settle timebox')
        e5.time.sleep(1)


def placement(snapshot, uids):
    return {
        'instances': {i['id']: {**{key: i.get(key) for key in ('node', 'ownerWorkloadUID', 'workloadUID', 'allocation', 'maxBatch', 'maxWaitNanos')}, 'profileID': i.get('profileID') or (i.get('profile') or {}).get('id')} for i in snapshot.get('instances') or [] if i.get('ownerWorkloadUID') in uids or i.get('workloadUID') in uids},
        'modes': {n['node']['name']: n['powerMode'] for n in snapshot.get('nodes', [])},
        'routing': {uid: snapshot.get('routing', {}).get(uid, {}) for uid in uids},
    }


def summarize(e5, binding, out):
    raw = e5.module('demo_raw', HERE / 'e5-raw.py')
    analysis = ANALYSIS
    rows, stage_summaries = [], []
    previous_end = None
    previous_snapshot = e5.load(out / 'initial.json')['state']['snapshot'] if (out / 'initial.json').exists() else None
    for stage in binding['schedule']:
        folder = stage_folder(out, stage)
        if not (folder / 'phase.json').exists():
            continue
        phase = e5.load(folder / 'phase.json')
        if not phase.get('startUTC') or not phase.get('endUTC'):
            stage_summaries.append({'stage': stage['id'], 'status': phase['status'], 'error': phase.get('error'), 'metricsAvailable': False})
            continue
        gap = None if previous_end is None else epoch(phase['startUTC']) - epoch(previous_end)
        previous_end = phase['endUTC']
        events, provenance = raw.scheduler_events(folder)
        pre = e5.load(folder / 'pre-state.json')['snapshot']
        baseline = previous_snapshot or pre
        post = e5.load(folder / 'post-state.json')['snapshot'] if (folder / 'post-state.json').exists() else None
        lower, upper = epoch(baseline['at']), epoch(post['at'] if post else phase.get('drainedUTC') or phase['endUTC'])
        events = [e for e in events if epoch(e.get('at')) is not None and lower <= epoch(e['at']) <= upper]
        snapshots = [(epoch(pre['at']), pre['at'], pre, 'pre-state.json')]
        snapshots += [(epoch(e['at']), e['at'], e['snapshot'], e.get('_rawRefs')) for e in events if e.get('snapshot')]
        if post:
            snapshots.append((epoch(post['at']), post['at'], post, 'post-state.json'))
        previous_snapshot = post or pre
        snapshots.sort(key=lambda item: item[0])
        changes, windows = [], []
        before, before_at = placement(baseline, [w['uid'] for w in binding['config']['workloads']]), baseline['at']
        for _, at, snapshot, source in snapshots:
            current = placement(snapshot, [w['uid'] for w in binding['config']['workloads']])
            for kind in ('instances', 'modes', 'routing'):
                for key in sorted(set(before[kind]) | set(current[kind])):
                    left, right = before[kind].get(key), current[kind].get(key)
                    if left != right:
                        changes.append({'observedAtUTC': at, 'previousObservedAtUTC': before_at, 'kind': kind, 'id': key, 'change': 'created' if left is None else 'deleted' if right is None else 'changed', 'before': left, 'after': right, 'source': source})
                        windows.append((epoch(before_at), epoch(at)))
            before, before_at = current, at
        actions = []
        for event in events:
            action = event.get('action')
            if action:
                actions.append({'at': event['at'], 'action': action, 'source': event.get('_rawRefs')})
                if epoch(action.get('startedAt')) is not None:
                    windows.append((epoch(action['startedAt']), epoch(action.get('completedAt') or event['at'])))
        power_path = folder / 'power.json'
        power = e5.load(power_path) if power_path.exists() else {}
        drain_power_path = folder / 'power-drain-inclusive.json'
        drain_power = e5.load(drain_power_path) if drain_power_path.exists() else {}
        summary = {'stage': stage['id'], 'status': phase['status'], 'startUTC': phase['startUTC'], 'endUTC': phase['endUTC'], 'drainedUTC': phase.get('drainedUTC'), 'gapFromPreviousOfferedWindowSeconds': gap, 'gapMeaning': 'No offered demo requests during per-stage drain/collection/setup; failures in this gap are not measurable by these producers', 'placementChanges': changes, 'actions': actions, 'transitionWindowsEpoch': windows, 'schedulerCapture': provenance, 'energyJ': power.get('wholePoolJoules') if power.get('complete') else None, 'activeEnergyJ': power.get('activePoolJoules') if power.get('complete') else None, 'drainInclusiveEnergyJ': drain_power.get('wholePoolJoules') if drain_power.get('complete') else None, 'energyScope': 'Shared whole/active pool; never attributed to individual workloads', 'workloads': {}}
        for w in binding['config']['workloads']:
            name = w['name']
            client_path = folder / (name + '-client.jsonl')
            clients = [json.loads(line) for line in client_path.read_text().splitlines() if line.strip()] if client_path.exists() else []
            clients = [r for r in clients if 'scheduledOffsetNs' in r]
            offered = stage['traces'][name]['offered']
            successful = [r for r in clients if r.get('status') == 200 and not r.get('error')]
            good = {id(r) for r in successful}
            start = phase['starts'].get(name)
            transition_failures = 0
            if start:
                for row in clients:
                    sent = epoch(start['at']) + (row['sentMonotonicNS'] - start['monotonicNS']) / 1e9
                    end = epoch(start['at']) + (row['finishedMonotonicNS'] - start['monotonicNS']) / 1e9
                    if id(row) not in good and any(sent <= hi and end >= lo for lo, hi in windows):
                        transition_failures += 1
            measurement = binding.get('demoConfig', {}).get('measurementSLO', {}).get(next(alias for alias, canonical in ALIASES.items() if canonical == name), w['slo'])
            slo_ms = measurement['targetLatencyNanos'] / 1e6
            metric = {'rate': stage['rates'][name], 'offered': offered, 'clientRows': len(clients), 'missingClientRows': max(0, offered - len(clients)), 'httpSuccess': len(successful), 'successFraction': len(successful) / offered if offered else None, 'latencyMS': analysis.stats([r['latencyMs'] for r in successful]), 'latencyScope': 'Client HTTP round trip of successful responses, not server execution latency', 'sloMS': slo_ms, 'sloPercentile': w['slo']['percentile'], 'withinSLOFraction': sum(r['latencyMs'] <= slo_ms for r in successful) / offered if offered else None, 'transitionFailures': transition_failures if provenance.get('complete') and len(clients) == offered and (start or not offered) else None, 'transitionFailureMeaning': 'Failed/unsent client requests overlapping observed placement-change brackets or elapsed scheduler actions; null if scheduler/client coverage incomplete', 'errors': [r for r in clients if id(r) not in good]}
            metric.update(sloPercentile=measurement['percentile'], schedulerSLO=w['slo'], measurementSLO=measurement)
            summary['workloads'][name] = metric
            rows.append((summary, name, metric))
        e5.save(folder / 'summary.json', summary)
        stage_summaries.append(summary)
    e5.save(out / 'summary.json', {'schema': 'e5-demo-summary.v1', 'stages': stage_summaries})
    def fmt(value):
        return '—' if value is None else f'{value:.3f}' if isinstance(value, float) else str(value)
    table = ['# Autonomous workload-pool demo', '', 'Client latency is successful HTTP round trip. SLO is the configured reporting-only measurementSLO, or captured scheduler SLO when omitted; scheduler SLO is never changed. Energy is shared pool energy, repeated per workload (do not sum rows). Missing evidence is —, not zero.', '', '| Stage | Workload | Rate/s | Success/offered | Success % | p95 ms | p99 ms | SLO p/ms | Within SLO % | Pool J | Active J | Drain pool J | Placement changes | Transition failures | Gap s |', '|---|---|---:|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|']
    for s, name, m in rows:
        table.append('| ' + ' | '.join(map(str, [s['stage'], name, fmt(m['rate']), f"{m['httpSuccess']}/{m['offered']}", fmt(None if m['successFraction'] is None else m['successFraction'] * 100), fmt(m['latencyMS']['p95']), fmt(m['latencyMS']['p99']), f"p{m['sloPercentile']}/{fmt(m['sloMS'])}", fmt(None if m['withinSLOFraction'] is None else m['withinSLOFraction'] * 100), fmt(s['energyJ']), fmt(s['activeEnergyJ']), fmt(s['drainInclusiveEnergyJ']), len(s['placementChanges']), fmt(m['transitionFailures']), fmt(s['gapFromPreviousOfferedWindowSeconds'])])) + ' |')
    for s in stage_summaries:
        table += ['', '## ' + s['stage'], '']
        if not s.get('metricsAvailable', True):
            table.append('No completed offered window: ' + str(s.get('error', s['status'])))
            continue
        table += ['| Observed UTC (previous observation) | Change | Before → after |', '|---|---|---|']
        for change in s['placementChanges']:
            detail = json.dumps(change['before'], ensure_ascii=False) + ' → ' + json.dumps(change['after'], ensure_ascii=False)
            table.append(f"| {change['observedAtUTC']} ({change['previousObservedAtUTC']}) | {change['kind']} {change['id']} {change['change']} | `{detail.replace('|', '/').replace(chr(10), ' ')}` |")
        if not s['schedulerCapture'].get('complete'):
            table.append('Scheduler coverage incomplete; transition failures are unknown. See summary.json schedulerCapture.errors.')
    (out / 'summary.md').write_text('\n'.join(table) + '\n')
    return stage_summaries


def self_check():
    """Run the real CPO stage loop against local pipes and mocked API/kubectl."""
    import tempfile
    from types import SimpleNamespace
    from unittest.mock import patch
    with tempfile.TemporaryDirectory(prefix='chill-demo-') as temp:
        root = Path(temp)
        environment = root / 'environment.json'
        environment.write_text(json.dumps({'paths': {'repository': str(HERE.parents[1]), 'ops': temp, 'campaign': temp, 'd2State': temp}, 'e5': {'operationalRecipe': temp, 'nodes': [], 'excludedNodes': []}, 'd2': {'repoBin': temp, 'apiBase': 'https://fixture.invalid', 'caFile': temp, 'tokenFile': temp, 'kubectl': ['fixture-kubectl'], 'jobTemplate': temp, 'workloadUID': 'fixture', 'workloadName': 'fixture'}}))
        with patch.dict(os.environ, {'CHILL_EXPERIMENT_CONFIG': str(environment)}):
            e5 = module('demo_smoke_e5', HERE / 'e5.py')
            d = e5.module('demo_smoke_conduct', HERE / 'd2/conduct_before.py')
        template = root / 'job.json'
        template.write_text(json.dumps({'spec': {'template': {'spec': {'restartPolicy': 'Never', 'containers': [{'name': 'load', 'image': 'fixture@sha256:fixture'}]}}}}))
        workloads = [{'name': name, 'uid': name, 'generation': 1, 'slo': {'percentile': 95, 'targetLatencyNanos': 1000000000}, 'jobTemplate': str(template), 'producerEndpoint': 'https://fixture.invalid', 'producerTokenFile': '/fixture/token', 'producerCAFile': '/fixture/ca', 'producerDatasetDir': '/fixture/data', 'fields': ['input'], 'workers': 1, 'gatewayCapSeconds': 2, 'lifetimeSeconds': 1, 'deliverySeconds': .1, 'socketSeconds': 3} for name in ALIASES.values()]
        config = {'stages': [{'id': 'E0', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 0)}, {'id': 'A', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 10000)}, {'id': 'B', 'seconds': .001, 'rates': {'mobilenet': 20000, 'smolvlm2': 10000, 'plc': 10000}}, {'id': 'C', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 10000)}]}
        stages = schedule(config, e5)
        cpo = object.__new__(e5.CPO)
        cpo.out = root / 'run'
        cpo.out.mkdir()
        cpo.a = SimpleNamespace(mode='smoke', command='smoke', stage='E0')
        cpo.c = {'workloads': workloads, 'smoke': stages[0]}
        cpo.b = {'schema': 'demo-e5-binding.v1', 'config': cpo.c, 'schedule': stages, 'sources': {}, 'payload': {w['name']: [] for w in workloads}}
        cpo.tb = dict.fromkeys(('setupSeconds', 'barrierSeconds', 'drainSeconds', 'apiSeconds', 'settleSeconds', 'collectSeconds'), 3)
        cpo.tb['flushSeconds'] = .001
        cpo.ws = {w['name']: w for w in workloads}
        cpo.uids = [w['uid'] for w in workloads]
        cpo.d = d
        settings = {'autonomousWorkloads': [], 'staticRouting': {}, 'excludedNodes': []}
        snapshot = {'at': e5.now(), 'instances': [], 'nodes': [], 'workloads': workloads, 'routing': {}, 'outstanding': [], 'inProgressTarget': None}
        cpo.state = lambda: {'snapshot': copy.deepcopy(snapshot), 'actions': [], 'results': None}
        cpo.idle = cpo.state
        cpo.settings = lambda: copy.deepcopy(settings)
        cpo.put = lambda value, reason: settings.update(copy.deepcopy(value)) or {'validatedAt': e5.now()}
        cpo.api = lambda *args: {'pending': 0, 'assigned': 0}
        cpo.authorize_native_modes = lambda: None
        cpo.monitor = lambda offset, folder: offset
        cpo.native_evidence = []
        cpo.bound_nodes = []
        cpo.io = SimpleNamespace(a=SimpleNamespace(), phase_out=cpo.out)
        pods, followers, calls = {}, {}, []
        class Process:
            def __init__(self, name):
                self.name, self.done = name, False
                reader, self.writer = os.pipe()
                self.stdout = os.fdopen(reader, 'rb')
                trace = next(s for s in stages if s['id'] == cpo.a.stage)['traces'][name]
                self.emit({'e5Event': 'ready', 'at': e5.now(), 'payload': [], 'offered': trace['offered'], 'arrivalSHA256': trace['arrivalSHA256'], 'barrierSeconds': 3})
            def emit(self, row):
                os.write(self.writer, (json.dumps(row) + '\n').encode())
            def poll(self):
                return 0 if self.done else None
            def terminate(self):
                if not self.done:
                    os.close(self.writer)
                    self.done = True
            def wait(self, timeout=None):
                self.terminate()
                return 0
        def popen(argv, **kwargs):
            name = next(n for n, pod in pods.items() if 'pod/' + pod['metadata']['name'] in argv)
            followers[name] = Process(name)
            return followers[name]
        def kubectl(argv, stdin=None, **kwargs):
            calls.append(argv)
            if argv[:2] == ['get', 'jobs']:
                return {'items': []}
            if argv[:2] == ['get', 'job']:
                name = next(n for n in pods if argv[2].endswith('-' + str(e5.SEEDS[n])))
                return {'metadata': {'name': argv[2], 'uid': 'job-' + name}}
            if argv[0] == 'create':
                job = json.loads(stdin)
                args = job['spec']['template']['spec']['containers'][0]['args']
                name = args[args.index('--workload') + 1]
                job['metadata']['uid'] = 'job-' + name
                pods[name] = {'metadata': {'name': 'pod-' + name, 'uid': 'poduid-' + name, 'ownerReferences': [{'kind': 'Job', 'uid': 'job-' + name}]}, 'spec': {'nodeName': 'fixture'}, 'status': {'phase': 'Running', 'containerStatuses': [{'name': 'load', 'containerID': 'fixture-' + name, 'state': {'running': {'startedAt': e5.now()}}}]}}
                return job
            if argv[:2] == ['get', 'pods']:
                name = next(n for n in pods if argv[3].endswith('-' + str(e5.SEEDS[n])))
                return {'items': [pods[name]]}
            name = next(n for n in pods if pods[n]['metadata']['name'] == argv[1 if argv[0] == 'exec' else 2])
            if argv[0] == 'get':
                return pods[name]
            if argv[0] == 'exec':
                process = followers[name]
                if '.touch()' in argv[-1]:
                    process.emit({'e5Event': 'start', 'at': e5.now(), 'monotonicNS': 1000000000})
                    stage = next(s for s in stages if s['id'] == cpo.a.stage)
                    for index, offset in enumerate(stage['traces'][name]['offsetsNS']):
                        process.emit({'requestIndex': index, 'scheduledOffsetNs': offset, 'sentMonotonicNS': 1000000000 + offset, 'finishedMonotonicNS': 1000000000 + offset + 1000, 'status': 200 if index else 503, 'latencyMs': .001})
                    process.emit({'e5Event': 'window-end', 'at': e5.now()})
                    process.emit({'e5Event': 'drained', 'at': e5.now()})
                else:
                    assert '.unlink()' in argv[-1]
                    process.terminate()
                return {}
            raise AssertionError(argv)
        cpo.io.k = kubectl
        cpo.io.wait_load_container = lambda job, folder, deadline: next(pod for pod in pods.values() if pod['metadata']['ownerReferences'][0]['uid'] == job[1])
        cpo.io.assert_producer_startable = lambda *args, **kwargs: None
        cpo.io.wait_job_complete = lambda *args: {'status': {'conditions': [{'type': 'Complete', 'status': 'True'}]}}
        def boundary(folder, prefix):
            if prefix == 'post':
                snapshot['instances'] = [{'id': 'native/' + w['name'], 'ownerWorkloadUID': w['uid'], 'workloadUID': w['uid'], 'ready': True, 'node': {'name': 'fixture'}, 'maxBatch': stages.index(next(s for s in stages if s['id'] == cpo.a.stage)) + 1} for w in workloads]
                snapshot['routing'] = {w['uid']: {'native/' + w['name']: 1} for w in workloads}
            snapshot['at'] = e5.now()
            e5.save(folder / (prefix + '-state.json'), cpo.state())
        cpo.boundary = boundary
        def sampler(folder, seconds):
            rows = [{'kind': 'cgroup', 'role': role, 'pod': pod, 'cpuUsageNanos': 1, 'nrThrottled': 0, 'throttledNanos': 0} for role, pod in [('server', 'server'), ('edgemesh', 'mesh')] + [('client', 'pod-' + w['name']) for w in workloads] for _ in range(2)]
            (folder / 'fixture-cgroup.jsonl').write_text('\n'.join(map(json.dumps, rows)) + '\n')
            return []
        cpo.sampler = sampler
        def collect(folder):
            e5.save(folder / 'collected.json', {'status': 'CAPTURED_NOT_EVALUATED'})
            e5.save(folder / 'power.json', {'complete': True, 'wholePoolJoules': 10, 'activePoolJoules': 5})
            e5.save(folder / 'power-drain-inclusive.json', {'complete': True, 'wholePoolJoules': 11})
        cpo.collect = collect
        original_module = e5.module
        def modules(name, path):
            if name == 'demo_raw':
                def events(folder):
                    phase = e5.load(folder / 'phase.json')
                    return [{'at': phase['endUTC'], 'action': {'kind': 'route', 'phase': 'complete', 'startedAt': phase['startUTC'], 'completedAt': phase['endUTC']}}], {'complete': True, 'errors': []}
                return SimpleNamespace(scheduler_events=events)
            return original_module(name, path)
        e5.module = modules
        (root / 'control-alerts.jsonl').write_text('')
        with patch.object(e5.subprocess, 'Popen', popen), patch.object(d, 'client_end_counter', lambda *args: {'fixtureCounter': True}):
            cpo.start()
            for stage in stages:
                run_stage(cpo, stage)
            summaries = summarize(e5, cpo.b, cpo.out)
        assert len(summaries) == 4
        assert summaries[1]['gapFromPreviousOfferedWindowSeconds'] >= 0
        for summary in summaries[1:]:
            assert summary['placementChanges'] and summary['energyJ'] == 10
            for metric in summary['workloads'].values():
                assert metric['offered'] == metric['clientRows'] and metric['transitionFailures'] == 1
                assert metric['httpSuccess'] == metric['offered'] - 1
        assert 'p95 ms' in (cpo.out / 'summary.md').read_text()
        assert sum(call[0] == 'create' for call in calls) == 9
        assert sum(call[0] == 'exec' for call in calls) == 18
        assert e5.load(cpo.out / 'smoke-E0/pre-state.json')['results'] is None and cpo.native_evidence == []
        import ast
        requested = {arg for arg in e5.load(cpo.out / 'smoke-A/job-image-classification-tight-slo.json')['spec']['template']['spec']['containers'][0]['args'] if arg.startswith('--')}
        for source_sha in ('e18ac391d4353e5abba03558b163312f2a7b629b', '4fb9bf8b0f8112549cf8d939b911c19ddfc9c54e'):
            source = e5.subprocess.check_output(['git', '-C', str(e5.REPO), 'show', source_sha + ':load-generator/src/chill_loadgen/cli.py'], text=True)
            tree = ast.parse(source)
            supported = {arg.value for node in ast.walk(tree) if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == 'add_argument' for arg in node.args if isinstance(arg, ast.Constant) and isinstance(arg.value, str) and arg.value.startswith('--')}
            assert requested & supported == requested, (source_sha, sorted(requested - supported))
            exports = {alias.name for node in ast.walk(tree) if isinstance(node, ast.ImportFrom) and node.module == 'chill_loadgen.schedule' for alias in node.names}
            assert {'ScheduledRequest', 'partition_schedule', 'run_schedule'} <= exports
        assert not requested & {'--workload-slo', '--gateway-timeout'}
        original_initial = (cpo.out / 'initial.json').read_bytes()
        original_start = (cpo.out / 'start-ack.json').read_bytes()
        resume(e5, cpo)
        ready_snapshot = copy.deepcopy(snapshot)
        snapshot['instances'], snapshot['routing'] = [], {}
        bootstraps = []
        def bootstrap_fixture():
            assert not any(cpo.c['smoke']['rates'].values()) and cpo.c['smoke']['seconds'] == stages[0]['seconds']
            bootstraps.append(cpo.a.stage)
            snapshot.update(copy.deepcopy(ready_snapshot))
        with patch.object(cpo, 'run', side_effect=bootstrap_fixture):
            resume(e5, cpo)
        assert len(bootstraps) == 1 and bootstraps[0].startswith('E0-resume-')
        assert (cpo.out / 'initial.json').read_bytes() == original_initial and (cpo.out / 'start-ack.json').read_bytes() == original_start
        assert (cpo.out / 'smoke-E0/phase.json').exists() and len(list(cpo.out.glob('resume-*/resume-ack.json'))) == 2
        normative = root / 'normative-job'
        normative.mkdir()
        cpo.b['schema'] = 'e5-binding.v1'
        scientific_job = cpo.job(workloads[0]['name'], stages[1], normative)
        assert {'--workload-slo', '--gateway-timeout'} <= set(scientific_job['spec']['template']['spec']['containers'][0]['args'])
        cpo.b['schema'] = 'demo-e5-binding.v1'
        failed_stage = copy.deepcopy(stages[1])
        failed_stage['id'] = cpo.a.stage = 'FAIL'
        cpo.c['smoke'] = failed_stage
        captured_before_delete = []
        def remove_fixture(job, uid):
            name = next(n for n in pods if job.endswith('-' + str(e5.SEEDS[n])))
            path = cpo.out / 'smoke-FAIL' / ('startup-' + name) / ('failed-producer-' + pods[name]['metadata']['uid'] + '.log')
            assert path.read_text() == 'fixture producer log\\n'
            captured_before_delete.append(uid)
        cpo.io.remove_job = remove_fixture
        with patch.object(cpo.io, 'wait_load_container', side_effect=RuntimeError('fixture startup terminated')), patch.object(e5.subprocess, 'run', return_value=SimpleNamespace(stdout='fixture producer log\\n', stderr='fixture parser stderr\\n', returncode=0)):
            try:
                cpo.run()
            except RuntimeError as error:
                assert str(error) == 'fixture startup terminated'
            else:
                raise AssertionError('Producer startup failure was swallowed')
        assert len(captured_before_delete) == 3
        cleanup_calls, removed = len(calls), []
        cpo.io.remove_job = lambda name, uid: removed.append((name, uid))
        cpo.cleanup_jobs()
        assert len(removed) == 9 and not any('fail' in argv[2] for argv in calls[cleanup_calls:] if argv[:2] == ['get', 'job'])
        unknown = object.__new__(e5.CPO)
        unknown.out = root / 'unknown-cleanup'
        record = unknown.out / 'phase'
        record.mkdir(parents=True)
        e5.save(record / 'created-fixture.json', {'metadata': {'name': 'fixture', 'uid': 'owned'}})
        e5.save(record / 'phase.json', {'ownedProducerStops': [{'name': 'fixture', 'uid': 'different', 'stopped': True}]})
        unknown.io = SimpleNamespace(k=lambda *args: {'metadata': {'uid': 'foreign'}}, remove_job=lambda *args: (_ for _ in ()).throw(AssertionError('Foreign Job deletion')))
        try:
            unknown.cleanup_jobs()
        except RuntimeError as error:
            assert str(error) == 'Owned producer UID changed'
        else:
            raise AssertionError('Mismatched deletion ACK bypassed exact-UID cleanup')
        fixture_env = json.loads(environment.read_text())
        fixture_env['e5'].update(plan=temp, producerNode='fixture', powerExecutable=temp, powerReceipt=temp)
        fixture_env['d2']['producerImage'] = 'fixture@sha256:fixture'
        environment.write_text(json.dumps(fixture_env))
        e5.E5.update(fixture_env['e5'])
        e5.ENVIRONMENT['d2'].update(fixture_env['d2'])
        op = {'endpoint': 'https://fixture.invalid', 'caFile': temp, 'tokenFile': temp, 'kubeContext': 'fixture', 'collection': {'serverContainer': 'serving-entrypoint', 'observationDirectory': '/fixture', 'schedulerConfigMap': 'fixture', 'metricSourcesConfigMap': 'fixture', 'metricSourcesKey': 'fixture'}, 'producer': {'endpoint': 'https://fixture.invalid', 'tokenFile': temp, 'caFile': temp, 'datasetDir': '/data'}, 'workloads': {w['name']: {'datasetDir': temp, 'fields': ['input'], 'datasetImage': 'fixture@sha256:fixture', 'datasetImagePath': '/fixture'} for w in workloads}}
        for recipe_workload in op['workloads'].values():
            recipe_workload.update(modelRef='fixture-model', preprocessorRef='fixture-preprocessor', datasetRef='fixture-dataset')
        recipe = root / 'recipe.json'
        recipe.write_text(json.dumps(op))
        source_spec = {'operationalRecipe': str(recipe), 'controlSourceSHA': 'HEAD', 'timeboxes': cpo.tb, 'measurementSLO': {alias: {'percentile': 99, 'targetLatencyNanos': 60000000000} for alias in ALIASES}}
        server = {'metadata': {'namespace': 'chill-system', 'labels': {'app.kubernetes.io/component': 'serving-entrypoint'}}, 'spec': {'containers': [{'name': 'serving-entrypoint', 'args': ['--request-timeout=70s']}]}, 'status': {'containerStatuses': [{'name': 'serving-entrypoint', 'ready': True, 'imageID': 'fixture@sha256:fixture'}]}}
        registration_snapshot = copy.deepcopy(snapshot)
        registration_snapshot['workloads'] = [copy.deepcopy(workloads[0])]
        base_spec = {key: {'name': 'fixture-' + kind} for key, kind in [('modelRef', 'model'), ('preprocessorRef', 'preprocessor'), ('datasetRef', 'dataset')]}
        registered = {workloads[0]['name']: {'desired': {'metadata': {'uid': workloads[0]['uid'], 'generation': 1, 'resourceVersion': '1'}, 'spec': base_spec}, 'appliedDefinition': workloads[0]}}
        management_calls, native_cleaned = [], [False]
        def management_fixture(self, method, path, body=None, headers=None, accepted=(200,)):
            management_calls.append((method, path))
            if path == '/api/v1/scheduler':
                return {'state': {'snapshot': copy.deepcopy(registration_snapshot)}, 'settings': settings}, 200
            if path == '/api/v1/scheduler/state':
                return {'snapshot': copy.deepcopy(registration_snapshot)}, 200
            name = body['name'] if method == 'POST' else path.rsplit('/', 1)[-1]
            if method == 'POST':
                assert name not in registered
                slo = body['spec']['slo']
                definition = {'name': name, 'uid': 'registered-' + name, 'generation': 1, 'slo': {'percentile': slo['percentile'], 'targetLatencyNanos': int(slo['targetLatency'][:-2])}}
                registered[name] = {'desired': {'metadata': {'uid': definition['uid'], 'generation': 1, 'resourceVersion': '1'}, 'spec': body['spec']}, 'appliedDefinition': definition}
                registration_snapshot['workloads'].append(definition)
                return copy.deepcopy(registered[name]), 201
            if method == 'DELETE':
                assert native_cleaned[0] and headers == {'If-Match': '1'}
                result = registered.pop(name)
                registration_snapshot['workloads'] = [w for w in registration_snapshot['workloads'] if w['name'] != name]
                return result, 202
            assert method == 'GET'
            return (copy.deepcopy(registered[name]), 200) if name in registered else ({'error': 'NotFound'}, 404)
        def api_fixture(self, method, path, **kwargs):
            assert method == 'GET' and path == '/api/v1/scheduler'
            return {'state': {'snapshot': copy.deepcopy(registration_snapshot)}, 'settings': settings}
        def modules_for_assembly(name, path):
            if name == 'demo_capture':
                return d
            loaded = original_module(name, path)
            if name in ('demo_templates', 'demo_definition_cleanup'):
                loaded.Prepare.api = management_fixture
            return loaded
        with patch.dict(os.environ, {'CHILL_EXPERIMENT_CONFIG': str(environment)}), patch.object(e5, 'module', modules_for_assembly), patch.object(e5.ssl, 'create_default_context', return_value=None), patch.object(d.Conductor, 'api', api_fixture), patch.object(d.Conductor, 'k', lambda self, argv, **kw: {'items': [] if argv[1] == 'nodes' else [server]}), patch.object(d.Conductor, 'job', return_value={'spec': {'template': {'spec': {'containers': [{'args': ['--workers', '320', '--request-lifetime', '2', '--delivery-margin', '.010', '--timeout', '2.02']}]}}}}):
            assembled = e5.load(assemble(e5, SimpleNamespace(out=root / 'assembly'), source_spec))
            cpo.c = assembled
            cpo.uids = [w['uid'] for w in assembled['workloads']]
            cpo.io.http = None
            try:
                finish_definitions(e5, cpo)
            except RuntimeError as error:
                assert 'cleanup must complete' in str(error)
            else:
                raise AssertionError('Definition deletion before native cleanup was allowed')
            e5.save(cpo.out / 'finish-complete.json', {'fixture': 'owned native cleanup completed'})
            native_cleaned[0] = True
            finish_definitions(e5, cpo)
        assert assembled['demoSkipped'] and assembled['approval']['controlImageDigest'] == 'fixture@sha256:fixture'
        assert len(assembled['workloads']) == 3
        assert assembled['workloads'][0]['uid'] == workloads[0]['uid'] and assembled['workloads'][0]['slo'] == workloads[0]['slo']
        assert sum(method == 'POST' for method, _ in management_calls) == 2
        assert sum(method == 'DELETE' for method, _ in management_calls) == 3 and not registered
        assert all(w['workers'] == 320 and w['lifetimeSeconds'] >= w['slo']['targetLatencyNanos'] / 1e9 for w in assembled['workloads'])
        assert (root / 'assembly/register-plc-anomaly.json').exists() and (cpo.out / 'finish-definitions-complete.json').exists()
        print('PASS demo: canonical registration/cleanup, E0/A/B/C real CPO loop, immutable CLI AST option intersection, owned/empty resume preserving start, producer logs before deletion, gap/energy/placement/failure summaries; mocked API/kubectl only')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('command', choices=['prepare', 'start', 'resume', 'stage', 'run', 'collect', 'summary', 'finish'], nargs='?')
    parser.add_argument('--config', type=Path, help='Demo JSON; required for prepare')
    parser.add_argument('--out', type=Path, help='Fresh absolute external campaign directory; binding.json is stored here')
    parser.add_argument('--release', type=Path, help='Existing CPO release: executor CampaignOps, command e5-execute, bindingPath, sourceSHA, mainInstructionUTC')
    parser.add_argument('--stage', help='Configured stage ID; required for stage/collect')
    parser.add_argument('--self-check', action='store_true', help='Run one local mocked API/kubectl lifecycle smoke, without cluster access')
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return
    if args.command is None or args.out is None:
        parser.error('command and --out required')
    if not os.environ.get('CHILL_EXPERIMENT_CONFIG'):
        parser.error('CHILL_EXPERIMENT_CONFIG must identify the existing external environment JSON')
    e5 = module('demo_e5', HERE / 'e5.py')
    if not args.out.is_absolute() or e5.CAMPAIGN.resolve() not in args.out.resolve().parents:
        parser.error('--out must be an absolute directory below paths.campaign')
    if args.command == 'prepare':
        if args.config is None:
            parser.error('prepare requires --config')
        prepare(e5, args)
        return
    binding_path = args.out / 'binding.json'
    binding = e5.load(binding_path)
    if binding.get('schema') != 'demo-e5-binding.v1':
        raise ValueError('Prepared demo binding required')
    if args.command == 'summary':
        summarize(e5, binding, args.out)
        return
    if args.release is None:
        parser.error('Live CPO commands require --release')
    cpo_args = argparse.Namespace(binding=binding_path, release=args.release, out=args.out, command='smoke', mode='smoke', stage=args.stage or binding['schedule'][0]['id'])
    cpo = e5.CPO(cpo_args)
    if args.command == 'start':
        cpo.start()
    elif args.command == 'resume':
        resume(e5, cpo)
    elif args.command in ('stage', 'run'):
        selected = [s for s in binding['schedule'] if s['id'] == args.stage] if args.command == 'stage' else binding['schedule']
        if not selected:
            parser.error('stage requires a configured --stage')
        for stage in selected:
            folder = stage_folder(args.out, stage)
            if args.command == 'run' and (folder / 'collected.json').exists() and e5.load(folder / 'phase.json')['status'] == 'DRAINED_NOT_EVALUATED':
                continue
            try:
                run_stage(cpo, stage)
            finally:
                summarize(e5, binding, args.out)
    elif args.command == 'collect':
        selected = next((s for s in binding['schedule'] if s['id'] == args.stage), None)
        if selected is None:
            parser.error('collect requires a configured --stage')
        folder = stage_folder(args.out, selected)
        if not (folder / 'collected.json').exists():
            cpo.collect(folder)
        print('Collected evidence: ' + str(folder / 'collected.json'))
    elif args.command == 'finish':
        cpo.finish()
        cpo.cleanup_jobs()
        finish_definitions(e5, cpo)


if __name__ == '__main__':
    main()
