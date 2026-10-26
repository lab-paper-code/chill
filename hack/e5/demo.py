#!/usr/bin/env python3
"""Short CPO demo: prepare -> start -> stage/run -> collect -> summary -> finish.

Reuses E5's authenticated API, producer/barrier/counter lifecycle, raw collection
and server-owned reset cleanup, but not its normative R/G/10800-second scientific gates.
Outputs are PRIVATE operational evidence. publish --experiment-id ID --store-config
/private/store.json uploads raw inputs before a final immutable summary; corrections
require a new ID. --help and --self-check need no live experiment environment.

E0 may set "readyGate": true: seconds becomes the maximum zero-load window,
ending early only when all three owned canonical workloads are Ready with positive
routing. Omit it (or false) for unchanged fixed-duration stages. phase.json and
summary.json retain readyGate.observedUTC (null on timeout), timedOut and the
last missingWorkloadUIDs; ready-gate-state.json retains the actual observation.

A stage may instead use "parts": [{"id": "high", "seconds": 180, "rates":
{"mobilenet": 100, "smolvlm2": 0.3, "plc": 40}}, {"id": "low", "seconds":
300, "rates": {"mobilenet": 20, "smolvlm2": 0.3, "plc": 40}}].
Omit stage-level seconds/rates when using parts. Parts share one producer window,
with no drain/collection/setup between rates; stage seconds is their sum and
stage rates are their maxima (not averages). Multipart summary.json includes
part-level success/p95 and observed placement/batch changes; summary.md shows
part rows. Request attribution uses scheduled offsets, not completion times.

Set top-level "staticPlacement": true to capture existing manual placement with
autonomy already off: start/run/finish never change scheduler control or routes.
No zero-load bootstrap is required. A stage may set "startBarrier":
{"reservations": 0, "transitions": 0}; before any producer setup it waits at most
that stage's seconds for no reservations or running/cancel actions. Timeout
records STOP and the last state, with no load started. Parts remain continuous.
For a PLC-only 60 -> 140 req/s profile window, use staticPlacement with a single
stage's parts (e.g. 60 seconds each), rates {"mobilenet": 0, "smolvlm2": 0,
"plc": 60} then {"mobilenet": 0, "smolvlm2": 0, "plc": 140}. Prepare captures
existing definitions; start records them; run produces/collects; finish removes
only owned producer Jobs, retaining the manual instances, routes and settings.
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
    if 'staticPlacement' in config and type(config['staticPlacement']) is not bool:
        raise ValueError('staticPlacement must be a boolean')
    stages = []
    if not isinstance(config.get('stages'), list) or not config['stages']:
        raise ValueError('A nonempty stages list is required')
    for row in config['stages']:
        label = row['id']
        if not isinstance(label, str) or not re.fullmatch('[A-Za-z0-9][A-Za-z0-9-]{0,39}', label) or any(s['id'].lower() == label.lower() for s in stages):
            raise ValueError('Unique short Kubernetes-safe stage IDs required')
        if 'parts' in row and ('seconds' in row or 'rates' in row):
            raise ValueError('Use either stage seconds/rates or parts, not both')
        inputs = row.get('parts', [row])
        if not isinstance(inputs, list) or not inputs:
            raise ValueError('A nonempty parts list is required')
        parts = []
        for part in inputs:
            if not isinstance(part, dict):
                raise ValueError('Each part must be an object')
            part_id, seconds, rates = part.get('id'), part.get('seconds'), part.get('rates')
            if not isinstance(part_id, str) or not re.fullmatch('[A-Za-z0-9][A-Za-z0-9-]{0,39}', part_id) or any(p['id'].lower() == part_id.lower() for p in parts):
                raise ValueError('Unique short Kubernetes-safe part IDs required within each stage')
            if not e5.positive(seconds) or not isinstance(rates, dict) or set(rates) != set(ALIASES):
                raise ValueError('Positive seconds and mobilenet/smolvlm2/plc rates required')
            if any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in rates.values()):
                raise ValueError('Rates must be finite nonnegative requests/second')
            if part is not row and any(key in part for key in ('readyGate', 'startBarrier', 'parts')):
                raise ValueError('readyGate, startBarrier and nested parts are not supported within parts')
            parts.append({'id': part_id, 'seconds': seconds, 'rates': {ALIASES[k]: v for k, v in rates.items()}})
        seconds = sum(p['seconds'] for p in parts)
        rates = {name: max(p['rates'][name] for p in parts) for name in ALIASES.values()}
        if 'readyGate' in row and type(row['readyGate']) is not bool:
            raise ValueError('readyGate must be a boolean')
        if row.get('readyGate') and (stages or any(rates.values())):
            raise ValueError('readyGate is only supported on the first zero-load stage')
        stage = {'id': label, 'seconds': seconds, 'rates': rates, 'traces': {}}
        if row.get('readyGate'):
            stage['readyGate'] = True
        if 'startBarrier' in row:
            barrier = row['startBarrier']
            if not isinstance(barrier, dict) or set(barrier) != {'reservations', 'transitions'} or any(type(v) is not int or v != 0 for v in barrier.values()):
                raise ValueError('startBarrier requires reservations: 0 and transitions: 0')
            stage['startBarrier'] = dict(barrier)
        stage['parts'] = parts
        for name in stage['rates']:
            offsets, base = [], 0
            for part in parts:
                rate = part['rates'][name]
                if rate:
                    offsets.extend(base + x for x in cli.arrival_offsets_ns('poisson', rate, part['seconds'], e5.SEEDS[name]))
                base += round(part['seconds'] * 1e9)
            stage['traces'][name] = {'seed': e5.SEEDS[name], 'offsetsNS': offsets, 'offered': len(offsets), 'arrivalSHA256': hashlib.sha256(json.dumps(offsets, separators=(',', ':')).encode()).hexdigest()}
        stages.append(stage)
    if not config.get('staticPlacement') and any(stages[0]['rates'].values()):
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
    timing = {flag: prototype[prototype.index(flag) + 1] for flag in ('--workers', '--delivery-margin', '--timeout')}
    wire = e5.module('demo_templates', HERE / 'e5-wire.py')
    registration = object.__new__(wire.Prepare)
    registration.op, registration.out, registration.tb, registration.http = op, args.out, spec['timeboxes'], io.http
    by_name = {w['name']: w for w in scheduler['state']['snapshot']['workloads']}
    registration.n = {'workloads': {name: {'slo': by_name[name]['slo'] if name in by_name else spec['measurementSLO'][next(alias for alias, canonical in ALIASES.items() if canonical == name)]} for name in e5.WORKLOADS}}
    def registration_api(method, path, body=None, headers=None, accepted=(200,)):
        if method == 'PUT' or (spec.get('staticPlacement') and method != 'GET'):
            raise RuntimeError('No canonical mutation permitted during static capture or existing SLO capture')
        result, status_code = wire.Prepare.api(registration, method, path, body, headers, accepted)
        io.record({'kind': 'canonical-management', 'method': method, 'path': path, 'status': status_code})
        return result, status_code
    registration.api = registration_api
    if spec.get('staticPlacement'):
        if any(name not in by_name for name in e5.WORKLOADS):
            raise RuntimeError('Static placement requires existing canonical definitions')
    else:
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
        requested = {'workers': int(timing['--workers']), 'deliverySeconds': float(timing['--delivery-margin']), 'socketSeconds': float(timing['--timeout'])}
        requested.update(spec.get('requestTiming', {}).get(alias, {}))
        if any(not e5.positive(requested[key]) for key in ('workers', 'deliverySeconds', 'socketSeconds')) or type(requested['workers']) is not int:
            raise ValueError('Positive configured request timings and integer workers required')
        lifetime = requested.get('lifetimeSeconds')
        if lifetime is not None and (not e5.positive(lifetime) or lifetime < actual['slo']['targetLatencyNanos'] / 1e9 or lifetime > float(cap[:-1]) or requested['socketSeconds'] <= lifetime + requested['deliverySeconds']):
            raise ValueError('Explicit lifetime must cover SLO, fit gateway cap and socket budget')
        c['workloads'].append({**{key: actual[key] for key in ('name', 'uid', 'generation', 'slo')}, 'datasetDir': recipe['datasetDir'], 'fields': recipe['fields'], 'jobTemplate': str(template), 'producerEndpoint': op['producer']['endpoint'], 'producerTokenFile': op['producer']['tokenFile'], 'producerCAFile': op['producer']['caFile'], 'producerDatasetDir': op['producer']['datasetDir'], **requested, 'gatewayCapSeconds': float(cap[:-1])})
    c['demoSkipped'] = ['E5 R/kappa capacity binding and G clipping', '10800-second normative schedule and factor baselines', 'phase6 scientific approvals/D1/D6/D9 judgment gates', 'image/source publication and profiling-vs-evaluation metadata Jobs; existing pinned images/datasets reused', 'existing scheduler SLO mutation: existing definitions retained; missing canonical definitions created from recipe plus configured SLO']
    c['demoProvenance'] = {'recipe': str(recipe_path), 'controlSourceSHA': 'Main-provided helm-revision source claim resolved in git, not inferred from image bytes', 'controlImageDigest': 'Live Ready Pod containerStatus.imageID', 'requestTiming': 'D2 Conductor.job socket/worker baseline plus explicit requestTiming overrides; absent lifetime omits client deadline header', 'measurementSLO': spec['measurementSLO']}
    config_path = args.out / 'demo-e5-config.json'
    e5.save(config_path, c)
    return config_path


def finish_definitions(e5, cpo):
    """Delete exact captured canonical UIDs only after the existing owned cleanup."""
    if cpo.static_placement():
        return
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
    stages = schedule(spec, e5)
    config_path = Path(spec['e5Config']).resolve() if spec.get('e5Config') else assemble(e5, args, spec)
    c = e5.load(config_path)
    e5.need(c, ['approval', 'inputs', 'timeboxes', 'workloads', 'collection', 'endpoint', 'caFile', 'tokenFile'], 'e5Config')
    e5.need(c['approval'], ['sourceSHA', 'controlImageDigest'], 'approval')
    e5.need(c['timeboxes'], e5.REQUIRED['timeboxes'], 'timeboxes')
    if any(not e5.positive(v) for v in c['timeboxes'].values()):
        raise ValueError('Finite positive E5 timeboxes required')
    if len(c['workloads']) != 3 or {w['name'] for w in c['workloads']} != set(ALIASES.values()):
        raise ValueError('Existing three canonical workload definitions required')
    if any(c['timeboxes']['setupSeconds'] + s['seconds'] + c['timeboxes']['drainSeconds'] + c['timeboxes']['flushSeconds'] > 4200 for s in stages):
        raise ValueError('Stage exceeds existing finite collector duration limit')
    sys.path.insert(0, str(e5.REPO / 'load-generator/src'))
    from chill_loadgen import cli
    producer = module('demo_producer', HERE / 'e5-producer.py')
    payload = {}
    sources = {'demoConfig': e5.ref(args.config), 'e5Config': e5.ref(config_path), 'environment': e5.ref(e5.ENVIRONMENT_PATH)}
    sources['scheduler'] = e5.ref(c['inputs']['scheduler'])
    scheduler_settings = e5.load(c['inputs']['scheduler'])['settings']
    for filename in ('demo.py', 'e5.py', 'e5-wire.py', 'e5-producer.py', 'e5-archive.py', 'e5-raw.py'):
        sources[filename] = e5.ref(HERE / filename)
    for w in c['workloads']:
        e5.need(w, [k for k in e5.REQUIRED['workload'] if k not in ('R', 'profilingRecordIDs')], w['name'])
        if any(not e5.positive(w[k]) for k in ('workers', 'deliverySeconds', 'socketSeconds', 'gatewayCapSeconds')) or type(w['workers']) is not int:
            raise ValueError('Positive workload timing and integer workers required')
        if not e5.positive(w['slo']['targetLatencyNanos']) or not 0 < w['slo']['percentile'] <= 100:
            raise ValueError('Valid frozen workload SLO required')
        lifetime = w.get('lifetimeSeconds')
        if lifetime is not None and (not e5.positive(lifetime) or lifetime < w['slo']['targetLatencyNanos'] / 1e9 or w['gatewayCapSeconds'] < lifetime or w['socketSeconds'] <= lifetime + w['deliverySeconds']):
            raise ValueError('Explicit lifetime requires lifetime>=SLO, gateway>=lifetime, socket>lifetime+delivery')
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
    binding = {'schema': 'demo-e5-binding.v1', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': e5.now(), 'schedulerSettings': scheduler_settings, 'config': c, 'sources': sources, 'payload': payload, 'schedule': stages, 'demoConfig': spec}
    e5.save(target, binding)
    print(str(target))


def stage_folder(out, stage):
    return out / ('smoke-' + stage['id'])


def phase_path(folder):
    recovered = folder / 'collected-phase.json'
    return recovered if recovered.exists() and (folder / 'collected.json').exists() else folder / 'phase.json'


def run_stage(cpo, stage):
    index = cpo.b['schedule'].index(stage)
    if index:
        previous = stage_folder(cpo.out, cpo.b['schedule'][index - 1])
        if not (previous / 'collected.json').exists() or cpo.b['schedule'][index - 1]['id'] != cpo.d.load(phase_path(previous))['stage']['id'] or cpo.d.load(phase_path(previous))['status'] != 'DRAINED_NOT_EVALUATED':
            raise RuntimeError('Previous stage must have drained and collected successfully')
        if not cpo.static_placement() and not any(cpo.b['schedule'][index - 1]['rates'].values()):
            state = cpo.state()
            missing = cpo.missing_ready_routes(state)
            if missing:
                raise RuntimeError('Bootstrap has no owned Ready positive route: ' + ', '.join(missing))
    if not (cpo.out / 'start-ack.json').exists():
        raise RuntimeError('Explicit start is required before stages')
    cpo.a.command = 'smoke'
    cpo.a.stage = stage['id']
    cpo.c['smoke'] = stage
    cpo.run()
    folder = stage_folder(cpo.out, stage)
    for name in stage['rates']:
        path = folder / (name + '-client.jsonl')
        if not stage['rates'][name] and not path.exists():
            path.write_text('')


def collect_stage(e5, cpo, stage):
    folder = stage_folder(cpo.out, stage)
    if (folder / 'collected.json').exists():
        return
    phase = e5.load(folder / 'phase.json')
    if phase['status'] == 'STOP':
        if phase.get('error') != 'Drain/terminal timebox exceeded; preserve partial evidence, stop' or not phase.get('startUTC') or not phase.get('endUTC'):
            raise RuntimeError('Only an otherwise completed producer window stopped by planner-idle timeout can be collected without replay')
        active = {name for name, rate in stage['rates'].items() if rate}
        for field in ('starts', 'producerDrained', 'clientEndCounterRelease'):
            if not active <= set(phase.get(field) or {}):
                raise RuntimeError('Completed producer evidence missing: ' + field)
        for name in active:
            terminal = e5.load(folder / ('terminal-' + name + '.json'))
            if not any(row.get('type') == 'Complete' and row.get('status') == 'True' for row in terminal.get('status', {}).get('conditions', [])):
                raise RuntimeError('Producer completion proof missing: ' + name)
        derived = folder / 'collected-phase.json'
        if derived.exists():
            raise RuntimeError('Partial recovery already retained; no implicit recovery overwrite')
        phase['collectionRecovery'] = {'originalPhase': str(folder / 'phase.json'), 'originalStatus': phase['status'], 'originalError': phase['error'], 'meaning': 'No replay; original STOP/client/start/end retained. Drain and post-boundary are late recovery observations; drain-inclusive energy includes this interval.'}
        phase['drainedUTC'] = cpo.request_drain(folder, phase)
        cpo.boundary(folder, 'recovery-post')
        phase['postBoundaryPrefix'], phase['status'] = 'recovery-post', 'DRAINED_NOT_EVALUATED'
        e5.save(derived, phase)
        cpo.collect(folder, phase=phase)
    else:
        cpo.collect(folder)


def resume(e5, cpo):
    """Reset the owned scope once, then re-enable autonomy with original evidence retained."""
    if cpo.static_placement():
        raise RuntimeError('Static placement does not reset or resume autonomy; use start/run')
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
    state, settings = cpo.state(), cpo.settings()
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
    cpo.clear_owned(folder.name)
    settings = cpo.settings()
    settings['autonomousWorkloads'] = cpo.uids
    settings['excludedNodes'] = e5.E5['excludedNodes']
    cpo.io.a.command, cpo.io.a.phase = 'resume', 'e5-demo-resume'
    cpo.native_evidence = state.get('results') or []
    cpo.authorize_native_modes()
    e5.save(folder / 'resume-ack.json', cpo.put(settings))
    if not cpo.state()['snapshot']['instances']:
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
        if not cpo.missing_ready_routes(current):
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
        phase = e5.load(phase_path(folder))
        if not phase.get('startUTC') or not phase.get('endUTC'):
            stage_summaries.append({'stage': stage['id'], 'status': phase['status'], 'error': phase.get('error'), 'metricsAvailable': False})
            for key in ('readyGate', 'startBarrier'):
                if key in phase:
                    stage_summaries[-1][key] = phase[key]
            continue
        gap = None if previous_end is None else epoch(phase['startUTC']) - epoch(previous_end)
        previous_end = phase['endUTC']
        events, provenance = raw.scheduler_events(folder)
        pre = e5.load(folder / 'pre-state.json')['snapshot']
        baseline = previous_snapshot or pre
        post_path = folder / (phase.get('postBoundaryPrefix', 'post') + '-state.json')
        post = e5.load(post_path)['snapshot'] if post_path.exists() else None
        lower, upper = epoch(baseline['at']), epoch(post['at'] if post else phase.get('drainedUTC') or phase['endUTC'])
        events = [e for e in events if epoch(e.get('at')) is not None and lower <= epoch(e['at']) <= upper]
        snapshots = [(epoch(pre['at']), pre['at'], pre, 'pre-state.json')]
        snapshots += [(epoch(e['at']), e['at'], e['snapshot'], e.get('_rawRefs')) for e in events if e.get('snapshot')]
        if post:
            snapshots.append((epoch(post['at']), post['at'], post, post_path.name))
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
        summary['collectionRecovery'] = phase.get('collectionRecovery')
        summary['demoRequestDrain'] = phase.get('demoRequestDrain')
        for key in ('readyGate', 'startBarrier'):
            if key in phase:
                summary[key] = phase[key]
        if len(stage['parts']) > 1:
            summary['parts'] = []
            base = 0
            for part in stage['parts']:
                stop = base + round(part['seconds'] * 1e9)
                lo, hi = epoch(phase['startUTC']) + base / 1e9, epoch(phase['startUTC']) + stop / 1e9
                observed = [c for c in changes if lo <= epoch(c['observedAtUTC']) < hi]
                batch = [c for c in observed if c['kind'] == 'instances' and c['before'] is not None and c['after'] is not None and any(c['before'].get(k) != c['after'].get(k) for k in ('maxBatch', 'maxWaitNanos'))]
                summary['parts'].append({'id': part['id'], 'seconds': part['seconds'], 'startOffsetNS': base, 'endOffsetNS': stop, 'windowEpoch': [lo, hi], 'placementChanges': observed, 'batchChanges': batch, 'changeAttribution': 'Observed UTC relative to earliest producer start; not causal attribution. Changes first observed after the offered window remain stage-only.', 'schedulerCapture': provenance, 'workloads': {}})
                base = stop
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
            within = sum(r['latencyMs'] <= slo_ms for r in successful)
            metric = {'rate': stage['rates'][name], 'offered': offered, 'clientRows': len(clients), 'missingClientRows': max(0, offered - len(clients)), 'httpSuccess': len(successful), 'successFraction': len(successful) / offered if offered else None, 'successNumerator': len(successful), 'successDenominator': offered, 'withinSLONumerator': within, 'withinSLODenominator': offered, 'latencyMS': analysis.stats([r['latencyMs'] for r in successful]), 'latencyScope': 'Client HTTP round trip of successful responses, not server execution latency', 'sloMS': slo_ms, 'sloPercentile': w['slo']['percentile'], 'withinSLOFraction': within / offered if offered else None, 'transitionFailures': transition_failures if provenance.get('complete') and len(clients) == offered and (start or not offered) else None, 'transitionFailureMeaning': 'Failed/unsent client requests overlapping observed placement-change brackets or elapsed scheduler actions; null if scheduler/client coverage incomplete', 'errors': [r for r in clients if id(r) not in good]}
            metric.update(sloPercentile=measurement['percentile'], schedulerSLO=w['slo'], measurementSLO=measurement)
            summary['workloads'][name] = metric
            rows.append((summary, name, metric))
            for part_summary, part in zip(summary.get('parts', []), stage['parts']):
                lo, hi = part_summary['startOffsetNS'], part_summary['endOffsetNS']
                selected = [r for r in clients if lo <= r['scheduledOffsetNs'] < hi]
                successes = [r for r in selected if r.get('status') == 200 and not r.get('error')]
                count = sum(lo <= offset < hi for offset in stage['traces'][name]['offsetsNS'])
                part_summary['workloads'][name] = {'rate': part['rates'][name], 'offered': count, 'clientRows': len(selected), 'missingClientRows': max(0, count - len(selected)), 'httpSuccess': len(successes), 'successNumerator': len(successes), 'successDenominator': count, 'successFraction': len(successes) / count if count else None, 'latencyMS': analysis.stats([r['latencyMs'] for r in successes]), 'latencyScope': metric['latencyScope'], 'producerWindowEpoch': [epoch(start['at']) + lo / 1e9, epoch(start['at']) + hi / 1e9] if start else None}
                bounds = part_summary['workloads'][name]['producerWindowEpoch']
                part_summary['workloads'][name]['producerWindowUTC'] = [e5.dt.datetime.fromtimestamp(t, e5.dt.timezone.utc).isoformat() for t in bounds] if bounds else None
                part_summary['workloads'][name]['windowMeaning'] = 'Frozen scheduled rate interval anchored to actual producer start; not an independently observed boundary event'
        e5.save(folder / 'summary.json', summary)
        stage_summaries.append(summary)
    scheduler_settings = binding.get('schedulerSettings')
    e5.save(out / 'summary.json', {'schema': 'e5-demo-summary.v1', 'schedulerSettings': scheduler_settings, 'missingReasons': {'schedulerSettings': None if scheduler_settings is not None else 'Binding does not contain frozen scheduler settings; current settings are not substituted'}, 'stages': stage_summaries})
    def fmt(value):
        return '—' if value is None else f'{value:.3f}' if isinstance(value, float) else str(value)
    table = ['# Autonomous workload-pool demo', '', 'Client latency is successful HTTP round trip. SLO is the configured reporting-only measurementSLO, or captured scheduler SLO when omitted; scheduler SLO is never changed. Energy is shared pool energy, repeated per workload (do not sum rows). Missing evidence is —, not zero.', '', '| Stage | Workload | Rate/s | Success/offered | Success % | p95 ms | p99 ms | SLO p/ms | Within SLO % | Pool J | Active J | Drain pool J | Placement changes | Transition failures | Gap s |', '|---|---|---:|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|']
    for s, name, m in rows:
        table.append('| ' + ' | '.join(map(str, [s['stage'], name, fmt(m['rate']), f"{m['httpSuccess']}/{m['offered']}", fmt(None if m['successFraction'] is None else m['successFraction'] * 100), fmt(m['latencyMS']['p95']), fmt(m['latencyMS']['p99']), f"p{m['sloPercentile']}/{fmt(m['sloMS'])}", fmt(None if m['withinSLOFraction'] is None else m['withinSLOFraction'] * 100), fmt(s['energyJ']), fmt(s['activeEnergyJ']), fmt(s['drainInclusiveEnergyJ']), len(s['placementChanges']), fmt(m['transitionFailures']), fmt(s['gapFromPreviousOfferedWindowSeconds'])])) + ' |')
    for s in stage_summaries:
        table += ['', '## ' + s['stage'], '']
        if s.get('collectionRecovery'):
            table.append('Original phase STOP preserved; collected without load replay. Request drain/post-state were observed during recovery, so drain-inclusive energy contains the late interval. See collectionRecovery in summary.json.')
        if not s.get('metricsAvailable', True):
            table.append('No completed offered window: ' + str(s.get('error', s['status'])))
            continue
        if s.get('parts'):
            table += ['Parts share one producer window; requests belong to their scheduled part. Batch/placement counts use observation time, not causal attribution; incomplete scheduler capture means counts are only observed changes.', '', '| Part | Workload | Rate/s | Success/offered | Success % | p95 ms | Placement changes | Batch changes |', '|---|---|---:|---|---:|---:|---:|---:|']
            for part in s['parts']:
                for name, metric in part['workloads'].items():
                    table.append('| ' + ' | '.join(map(str, [part['id'], name, fmt(metric['rate']), f"{metric['httpSuccess']}/{metric['offered']}", fmt(None if metric['successFraction'] is None else metric['successFraction'] * 100), fmt(metric['latencyMS']['p95']), len(part['placementChanges']), len(part['batchChanges'])])) + ' |')
            table.append('')
        table += ['| Observed UTC (previous observation) | Change | Before → after |', '|---|---|---|']
        for change in s['placementChanges']:
            detail = json.dumps(change['before'], ensure_ascii=False) + ' → ' + json.dumps(change['after'], ensure_ascii=False)
            table.append(f"| {change['observedAtUTC']} ({change['previousObservedAtUTC']}) | {change['kind']} {change['id']} {change['change']} | `{detail.replace('|', '/').replace(chr(10), ' ')}` |")
        if not s['schedulerCapture'].get('complete'):
            table.append('Scheduler coverage incomplete; transition failures are unknown. See summary.json schedulerCapture.errors.')
    (out / 'summary.md').write_text('\n'.join(table) + '\n')
    return stage_summaries


def publish(e5, binding, out, identifier, store_config):
    """Upload raw evidence, then immutable summary; corrections require a new ID."""
    summaries = summarize(e5, binding, out)
    stages = []
    for summary in summaries:
        if not summary.get('startUTC') or not summary.get('endUTC'):
            raise RuntimeError('Cannot publish a final record without actual stage window bounds')
        workloads = {}
        for name, metric in summary['workloads'].items():
            workloads[name] = {key: value for key, value in metric.items() if key != 'errors'}
            workloads[name]['population'] = 'Frozen offered arrivals; latency is successful client HTTP round trips only'
            workloads[name]['missingReasons'] = {
                'successFraction': 'No offered requests' if not metric['offered'] else None,
                'withinSLOFraction': 'No offered requests' if not metric['offered'] else None,
                'latencyMS': 'No successful client responses' if not metric['httpSuccess'] else None,
                'transitionFailures': 'Scheduler/client coverage incomplete' if metric['transitionFailures'] is None else None,
            }
        kpis = {'status': summary['status'], 'visibility': 'PRIVATE_OPERATIONAL_ONLY', 'workloads': workloads,
                'judgmentCounts': {summary['status']: 1}, 'wholePoolJoules': summary['energyJ'], 'activePoolJoules': summary['activeEnergyJ'],
                'drainInclusiveWholePoolJoules': summary['drainInclusiveEnergyJ'], 'energyPopulation': summary['energyScope'],
                'gapFromPreviousOfferedWindowSeconds': summary['gapFromPreviousOfferedWindowSeconds'],
                'missingReasons': {'energy': 'Actual whole/active power coverage incomplete' if summary['energyJ'] is None or summary['activeEnergyJ'] is None else None,
                                  'drainInclusiveEnergy': 'Actual drain-inclusive power coverage incomplete' if summary['drainInclusiveEnergyJ'] is None else None,
                                  'gap': 'First offered window' if summary['gapFromPreviousOfferedWindowSeconds'] is None else None},
                'limitations': [summary['gapMeaning'], 'Operational demo only; no E5 scientific PASS or workload-attributed energy', *binding['config'].get('demoSkipped', [])]}
        kpis['schedulerSettings'] = binding.get('schedulerSettings')
        for key in ('readyGate', 'startBarrier'):
            if key in summary:
                kpis[key] = summary[key]
        if 'parts' in summary:
            kpis['parts'] = summary['parts']
        kpis['missingReasons']['schedulerSettings'] = None if kpis['schedulerSettings'] is not None else 'Binding does not contain frozen scheduler settings'
        stage = next(row for row in binding['schedule'] if row['id'] == summary['stage'])
        stages.append({'id': summary['stage'], 'from': summary['startUTC'], 'to': summary['endUTC'], 'kpis': kpis, '_folder': stage_folder(out, stage)})
    status = 'OPERATIONAL_DEMO_COMPLETE' if len(stages) == len(binding['schedule']) and all(stage['kpis']['status'] == 'DRAINED_NOT_EVALUATED' for stage in stages) else 'PARTIAL'
    return e5.publish_experiment(out, identifier, store_config, stages, status)




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
        config['stages'][-1] = {'id': 'C', 'parts': [{'id': 'high', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 20000)}, {'id': 'low', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 10000)}]}
        stages = schedule(config, e5)
        from chill_loadgen import cli
        assert stages[-1]['seconds'] == .002
        for name in ALIASES.values():
            expected = cli.arrival_offsets_ns('poisson', 20000, .001, e5.SEEDS[name])
            expected += [1000000 + x for x in cli.arrival_offsets_ns('poisson', 10000, .001, e5.SEEDS[name])]
            assert stages[-1]['traces'][name]['offsetsNS'] == expected
            single = cli.arrival_offsets_ns('poisson', 10000, .001, e5.SEEDS[name])
            assert stages[1]['traces'][name]['offsetsNS'] == single
        for invalid in ([], [None], [dict(config['stages'][0], seconds=0)], [dict(config['stages'][0], rates={'mobilenet': 1})], [config['stages'][0], config['stages'][0]], [dict(config['stages'][0], readyGate=True)]):
            try:
                schedule({'stages': [{'id': 'bad', 'parts': invalid}]}, e5)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid parts accepted')
        try:
            schedule({'stages': [dict(config['stages'][0], parts=[config['stages'][0]])]}, e5)
        except ValueError:
            pass
        else:
            raise AssertionError('Ambiguous stage accepted')
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
        settings = {'autonomousWorkloads': [], 'staticRouting': {}, 'excludedNodes': [], 'minimumDemand': {cpo.uids[0]: .3}, 'scaleDownStabilizationNanos': 120000000000, 'transitionReserveFraction': .25, 'comparisonPeriodNanos': 600000000000}
        cpo.b['schedulerSettings'] = copy.deepcopy(settings)
        snapshot = {'at': e5.now(), 'instances': [], 'nodes': [], 'workloads': workloads, 'routing': {}, 'outstanding': [], 'reservations': []}
        scheduler_actions = []
        cpo.state = lambda: {'snapshot': copy.deepcopy(snapshot), 'actions': copy.deepcopy(scheduler_actions), 'results': None}
        cpo.idle = cpo.state
        cpo.settings = lambda: copy.deepcopy(settings)
        cpo.put = lambda value: settings.update(copy.deepcopy(value)) or {'validatedAt': e5.now()}
        cpo.api = lambda *args: {'pending': 0, 'assigned': 0}
        cpo.authorize_native_modes = lambda: None
        cpo.monitor = lambda offset, folder: offset
        cpo.native_evidence = []
        cpo.bound_nodes = []
        cpo.io = SimpleNamespace(a=SimpleNamespace(), phase_out=cpo.out)
        def operation_fixture(method, path, body, **kwargs):
            assert method == 'POST' and path == '/api/v1/scheduler/operations'
            if body['kind'] == 'reset':
                settings['autonomousWorkloads'] = []
                snapshot['instances'], snapshot['routing'] = [], {}
            elif body['kind'] != 'release':
                raise AssertionError(body)
            return {'operation': body, 'status': 'complete', 'steps': [], 'endedAt': e5.now()}
        cpo.io.api = operation_fixture
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
        def collect(folder, phase=None):
            e5.save(folder / 'collected.json', {'status': 'CAPTURED_NOT_EVALUATED'})
            e5.save(folder / 'power.json', {'complete': True, 'wholePoolJoules': 10, 'activePoolJoules': 5})
            e5.save(folder / 'power-drain-inclusive.json', {'complete': True, 'wholePoolJoules': 11})
        cpo.collect = collect
        original_module = e5.module
        def modules(name, path):
            if name == 'demo_raw':
                def events(folder):
                    phase = e5.load(folder / 'phase.json')
                    captured = [{'at': phase['endUTC'], 'action': {'kind': 'route', 'phase': 'complete', 'startedAt': phase['startUTC'], 'completedAt': phase['endUTC']}}]
                    if folder.name == 'smoke-C':
                        state = copy.deepcopy(e5.load(folder / 'pre-state.json')['snapshot'])
                        at = (e5.dt.datetime.fromisoformat(phase['startUTC']) + e5.dt.timedelta(seconds=.001)).isoformat()
                        state['at'] = at
                        for instance in state['instances']:
                            instance['maxBatch'] = 88
                        captured.append({'at': at, 'snapshot': state})
                    return captured, {'complete': True, 'errors': []}
                return SimpleNamespace(scheduler_events=events)
            return original_module(name, path)
        e5.module = modules
        (root / 'control-alerts.jsonl').write_text('')
        with patch.object(e5.subprocess, 'Popen', popen), patch.object(d, 'client_end_counter', lambda *args: {'fixtureCounter': True}):
            cpo.start()
            snapshot['reservations'] = [{'owner': 'fixture-transition'}]
            scheduler_actions.append({'id': 'fixture-action', 'status': 'running'})
            with patch.object(cpo, 'idle', side_effect=AssertionError('Demo stage must not await planner idle')):
                for stage in stages:
                    run_stage(cpo, stage)
            summaries = summarize(e5, cpo.b, cpo.out)
        assert len(summaries) == 4
        high, low = summaries[-1]['parts']
        assert (high['id'], low['id']) == ('high', 'low')
        assert high['endOffsetNS'] == low['startOffsetNS'] == 1000000
        assert low['batchChanges'] and all(c['after']['maxBatch'] == 88 for c in low['batchChanges'])
        for name in ALIASES.values():
            total = summaries[-1]['workloads'][name]
            assert sum(p['workloads'][name]['offered'] for p in (high, low)) == total['offered']
            assert sum(p['workloads'][name]['httpSuccess'] for p in (high, low)) == total['httpSuccess']
            assert high['workloads'][name]['successFraction'] < 1 and low['workloads'][name]['successFraction'] == 1
            assert low['workloads'][name]['latencyMS']['p95'] == .001
            assert high['workloads'][name]['producerWindowUTC'][1] == low['workloads'][name]['producerWindowUTC'][0]
            import base64
            import zlib
            args = e5.load(cpo.out / 'smoke-C' / ('job-' + name + '.json'))['spec']['template']['spec']['containers'][0]['args']
            policy = json.loads(zlib.decompress(base64.b64decode(args[0])))
            assert policy['segments'] == [{'rate': 20000, 'seconds': .001}, {'rate': 10000, 'seconds': .001}]
            assert policy['arrivalSHA256'] == stages[-1]['traces'][name]['arrivalSHA256']
        assert '| low |' in (cpo.out / 'summary.md').read_text()
        # Exercise the optional gate through the real zero-load loop, including timeout.
        for gate_ready in (True, False):
            gated = schedule({'stages': [dict(config['stages'][0], seconds=1 if gate_ready else .01, readyGate=True)]}, e5)[0]
            gate_out = root / ('ready-gate' if gate_ready else 'ready-gate-timeout')
            gate_out.mkdir()
            (gate_out / 'start-ack.json').write_bytes((cpo.out / 'start-ack.json').read_bytes())
            ready_state = cpo.state()
            missing_state = copy.deepcopy(ready_state)
            missing_state['snapshot']['routing'][cpo.uids[-1]] = {'native/' + cpo.uids[-1]: 0}
            observations = []
            def gate_state():
                observations.append(True)
                return copy.deepcopy(ready_state if gate_ready and len(observations) >= 3 else missing_state)
            with patch.object(cpo, 'out', gate_out), patch.object(cpo, 'state', gate_state), patch.dict(cpo.b, {'schedule': [gated]}):
                run_stage(cpo, gated)
                gate_phase = e5.load(gate_out / 'smoke-E0/phase.json')
                gate_summary = summarize(e5, cpo.b, gate_out)[0]
            evidence = gate_phase['readyGate']
            assert evidence == gate_summary['readyGate']
            assert evidence['timedOut'] is not gate_ready
            assert bool(evidence['observedUTC']) is gate_ready
            assert (gate_out / 'smoke-E0/ready-gate-state.json').exists()
            if gate_ready:
                assert not evidence['missingWorkloadUIDs'] and len(observations) >= 3
                assert epoch(gate_phase['endUTC']) - epoch(gate_phase['startUTC']) < gated['seconds']
            else:
                assert evidence['missingWorkloadUIDs'] == [cpo.uids[-1]]
        assert 'readyGate' not in e5.load(cpo.out / 'smoke-E0/phase.json')
        for invalid in (dict(config['stages'][0], readyGate='true'), dict(config['stages'][1], readyGate=True)):
            try:
                schedule({'stages': [invalid]}, e5)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid ready gate accepted')
        # Static capture runs the real producers without scheduler writes.
        static_config = {'staticPlacement': True, 'stages': [{'id': 'PLC', 'startBarrier': {'reservations': 0, 'transitions': 0}, 'parts': [{'id': 'low', 'seconds': .001, 'rates': {'mobilenet': 0, 'smolvlm2': 0, 'plc': 60000}}, {'id': 'high', 'seconds': .001, 'rates': {'mobilenet': 0, 'smolvlm2': 0, 'plc': 140000}}]}]}
        static_stages = schedule(static_config, e5)
        static_out = root / 'static-placement'
        static_out.mkdir()
        static_state = cpo.state()
        static_state['snapshot']['reservations'] = []
        static_state['actions'] = []
        manual_before = copy.deepcopy(static_state)
        static_settings = dict(copy.deepcopy(settings), autonomousWorkloads=[])
        def no_mutation(*args, **kwargs):
            raise AssertionError('Static placement must not mutate scheduler control or routes')
        with patch.object(cpo, 'out', static_out), patch.object(cpo, 'settings', lambda: copy.deepcopy(static_settings)), patch.object(cpo, 'put', no_mutation), patch.object(cpo.io, 'api', no_mutation), patch.object(cpo, 'state', lambda: copy.deepcopy(static_state)), patch.dict(cpo.b, {'schedule': static_stages, 'demoConfig': static_config}), patch.object(e5.subprocess, 'Popen', popen), patch.object(d, 'client_end_counter', lambda *args: {'fixtureCounter': True}):
            cpo.start()
            with patch.object(cpo, 'boundary', lambda folder, prefix: e5.save(folder / (prefix + '-state.json'), cpo.state())):
                original_stages = stages
                stages = static_stages
                try:
                    run_stage(cpo, static_stages[0])
                finally:
                    stages = original_stages
            static_summary = summarize(e5, cpo.b, static_out)[0]
            assert static_summary['startBarrier']['observedUTC']
            assert len(static_summary['parts']) == 2
            assert e5.load(static_out / 'start-ack.json')['staticPlacement']
            cpo.finish()
            finish_definitions(e5, cpo)
            assert cpo.state() == manual_before and static_settings['autonomousWorkloads'] == []
            try:
                resume(e5, cpo)
            except RuntimeError as error:
                assert 'does not reset' in str(error)
            else:
                raise AssertionError('Static resume enabled autonomy')
            failed_static = schedule({'staticPlacement': True, 'stages': [dict(config['stages'][0], id='STATIC-FAIL')]}, e5)[0]
            with patch.dict(cpo.b, {'schedule': [failed_static]}), patch.object(cpo, 'boundary', lambda folder, prefix: e5.save(folder / (prefix + '-state.json'), cpo.state())), patch.object(cpo, 'request_drain', side_effect=RuntimeError('fixture static drain failure')):
                try:
                    run_stage(cpo, failed_static)
                except RuntimeError as error:
                    assert str(error) == 'fixture static drain failure'
                else:
                    raise AssertionError('Static failure was swallowed')
            assert e5.load(static_out / 'smoke-STATIC-FAIL/phase.json')['status'] == 'STOP'
            assert not (static_out / 'smoke-STATIC-FAIL/failed-owned-control-restore.json').exists()
            assert cpo.state() == manual_before
        # Both counters must clear; cancel actions count, completed actions do not.
        for clears in (True, False):
            barrier_stage = schedule({'stages': [dict(config['stages'][0], seconds=1 if clears else .01, startBarrier={'reservations': 0, 'transitions': 0})]}, e5)[0]
            barrier_out = root / ('barrier-clear' if clears else 'barrier-timeout')
            barrier_out.mkdir()
            (barrier_out / 'start-ack.json').write_bytes((cpo.out / 'start-ack.json').read_bytes())
            busy = copy.deepcopy(static_state)
            busy['snapshot']['reservations'] = [{'id': 'reserved'}]
            busy['actions'] = [{'status': 'cancel'}, {'status': 'complete'}]
            observations = []
            def barrier_state():
                observations.append(True)
                state = copy.deepcopy(busy)
                if clears and len(observations) >= 2:
                    state['snapshot']['reservations'] = []
                if clears and len(observations) >= 3:
                    state['actions'] = [{'status': 'complete'}]
                return state
            with patch.object(cpo, 'out', barrier_out), patch.object(cpo, 'state', barrier_state), patch.dict(cpo.b, {'schedule': [barrier_stage]}):
                if clears:
                    run_stage(cpo, barrier_stage)
                    assert len(observations) >= 3
                else:
                    with patch.object(cpo, 'sampler', side_effect=AssertionError('Timed out barrier must not start collection')), patch.object(cpo.io, 'k', side_effect=lambda argv, **kwargs: {'items': []} if argv == ['get', 'jobs', '-o', 'json'] else no_mutation()):
                        try:
                            run_stage(cpo, barrier_stage)
                        except RuntimeError as error:
                            assert 'start barrier timebox' in str(error)
                        else:
                            raise AssertionError('Busy barrier started load')
                barrier_phase = e5.load(barrier_out / 'smoke-E0/phase.json')
                barrier_summary = summarize(e5, cpo.b, barrier_out)[0]
                assert barrier_phase['startBarrier'] == barrier_summary['startBarrier']
                assert barrier_phase['startBarrier']['timedOut'] is not clears
                if not clears:
                    assert barrier_phase['status'] == 'STOP' and barrier_phase['startUTC'] is None
                    assert barrier_phase['startBarrier']['reservations'] == 1 and barrier_phase['startBarrier']['transitions'] == 1
                assert (barrier_out / 'smoke-E0/start-barrier-state.json').exists()
        for invalid_config in ({'staticPlacement': 'true', 'stages': [config['stages'][0]]}, *({'stages': [dict(config['stages'][0], startBarrier=value)]} for value in (None, {}, {'reservations': False, 'transitions': 0}, {'reservations': 1, 'transitions': 0}, {'reservations': 0, 'transitions': 0, 'seconds': 1})), {'stages': [{'id': 'bad', 'parts': [dict(config['stages'][0], startBarrier={'reservations': 0, 'transitions': 0})]}]}):
            try:
                schedule(invalid_config, e5)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid static placement/start barrier accepted')
        frozen_settings = copy.deepcopy(cpo.b['schedulerSettings'])
        settings['minimumDemand'][cpo.uids[0]] = 9
        settings['scaleDownStabilizationNanos'] = 300000000000
        settings['transitionReserveFraction'] = 0
        settings['comparisonPeriodNanos'] = 900000000000
        summarize(e5, cpo.b, cpo.out)
        assert e5.load(cpo.out / 'summary.json')['schedulerSettings'] == frozen_settings
        drained = e5.load(cpo.out / 'smoke-A/phase.json')['demoRequestDrain']
        assert drained['reservations'][0]['owner'] == 'fixture-transition' and drained['actions'][0]['status'] == 'running'
        source_folder = cpo.out / 'smoke-A'
        recovery_out = root / 'collection-recovery'
        recovery_folder = stage_folder(recovery_out, stages[1])
        recovery_folder.mkdir(parents=True)
        prior = e5.load(source_folder / 'phase.json')
        prior.update(status='STOP', error='Drain/terminal timebox exceeded; preserve partial evidence, stop', drainedUTC=None)
        prior.pop('demoRequestDrain')
        e5.save(recovery_folder / 'phase.json', prior)
        for path in source_folder.iterdir():
            if path.is_file() and (path.name == 'pre-state.json' or path.name.endswith('-client.jsonl') or path.name.startswith('terminal-')):
                (recovery_folder / path.name).write_bytes(path.read_bytes())
        (recovery_out / 'start-ack.json').write_bytes((cpo.out / 'start-ack.json').read_bytes())
        original_phase = (recovery_folder / 'phase.json').read_bytes()
        original_clients = {path.name: path.read_bytes() for path in recovery_folder.glob('*-client.jsonl')}
        with patch.object(cpo, 'out', recovery_out), patch.object(cpo, 'idle', side_effect=AssertionError('Collection must not await planner idle')), patch.object(cpo, 'run', side_effect=AssertionError('Collection must not replay load')):
            collect_stage(e5, cpo, stages[1])
            recovered = e5.load(phase_path(recovery_folder))
            assert recovered['status'] == 'DRAINED_NOT_EVALUATED' and recovered['collectionRecovery']['originalStatus'] == 'STOP'
            assert (recovered['startUTC'], recovered['endUTC']) == (prior['startUTC'], prior['endUTC'])
            assert summarize(e5, cpo.b, recovery_out)[0]['collectionRecovery']['originalStatus'] == 'STOP'
            with patch.object(cpo, 'run', return_value=None) as next_stage:
                run_stage(cpo, stages[2])
                assert next_stage.call_count == 1
        assert (recovery_folder / 'phase.json').read_bytes() == original_phase
        assert all((recovery_folder / name).read_bytes() == content for name, content in original_clients.items())
        for outstanding, pending in [([{'id': 'unfinished'}], 0), ([], 1)]:
            snapshot['outstanding'] = outstanding
            with patch.object(cpo, 'api', return_value={'pending': pending}), patch.object(e5.time, 'monotonic', side_effect=[0, 0, 4]), patch.object(e5.time, 'sleep', return_value=None):
                try:
                    cpo.request_drain(root, {})
                except RuntimeError as error:
                    assert str(error).startswith('Request drain timebox exceeded')
                else:
                    raise AssertionError('Unfinished requests were ignored')
        snapshot['outstanding'] = []
        with patch.object(e5.time, 'monotonic', side_effect=[0, 0, 4]), patch.object(e5.time, 'sleep', return_value=None):
            try:
                e5.CPO.idle(cpo)
            except RuntimeError as error:
                assert str(error).startswith('Drain/terminal timebox exceeded')
            else:
                raise AssertionError('Global/finish idle stopped waiting for scheduler transitions')
        assert summaries[1]['gapFromPreviousOfferedWindowSeconds'] >= 0
        for summary in summaries[1:]:
            assert summary['placementChanges'] and summary['energyJ'] == 10
            for metric in summary['workloads'].values():
                assert metric['offered'] == metric['clientRows'] and metric['transitionFailures'] == 1
                assert metric['httpSuccess'] == metric['offered'] - 1
        assert 'p95 ms' in (cpo.out / 'summary.md').read_text()
        assert sum(call[0] == 'create' for call in calls) == 10  # Nine autonomous, one static PLC.
        assert sum(call[0] == 'exec' for call in calls) == 20
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
        assert (cpo.out / 'smoke-E0/phase.json').exists()
        normative = root / 'normative-job'
        normative.mkdir()
        cpo.b['schema'] = 'e5-binding.v1'
        scientific_job = cpo.job(workloads[0]['name'], stages[1], normative)
        assert {'--workload-slo', '--gateway-timeout'} <= set(scientific_job['spec']['template']['spec']['containers'][0]['args'])
        workloads[0].pop('lifetimeSeconds')
        optional_job = cpo.job(workloads[0]['name'], stages[1], normative)
        assert '--request-lifetime' not in optional_job['spec']['template']['spec']['containers'][0]['args']
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
            before_static_capture = len(management_calls)
            static_assembled = e5.load(assemble(e5, SimpleNamespace(out=root / 'static-assembly'), dict(source_spec, staticPlacement=True)))
            assert len(static_assembled['workloads']) == 3
            assert all(method == 'GET' for method, _ in management_calls[before_static_capture:])
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
        assert all(w['workers'] == 320 and 'lifetimeSeconds' not in w for w in assembled['workloads'])
        assert (root / 'assembly/register-plc-anomaly.json').exists() and (cpo.out / 'finish-definitions-complete.json').exists()
        reset = object.__new__(e5.CPO)
        reset.b = {'schema': 'e5-binding.v1'}
        reset.out = root / 'reset'
        reset.out.mkdir()
        reset.uids, reset.tb = ['owned'], {'settleSeconds': 194}
        failure = {'status': 'failed', 'error': 'native drain failed', 'steps': [{'name': 'drain', 'status': 'failed'}]}
        requests = []
        def failed_reset(method, path, body, **kwargs):
            requests.append(body)
            return failure
        reset.io = SimpleNamespace(api=failed_reset)
        reset.put = lambda *args: (_ for _ in ()).throw(AssertionError('Settings advanced after failed reset'))
        e5.save(reset.out / 'initial.json', {'settings': {}})
        try:
            reset.finish()
        except RuntimeError as error:
            assert 'reset failed' in str(error)
        else:
            raise AssertionError('Failed reset was accepted')
        assert requests == [{'kind': 'reset', 'workloadUIDs': ['owned'], 'reason': 'Approved campaign owned reset: finish'}]
        assert e5.load(reset.out / 'finish-reset-result.json') == failure
        assert not (reset.out / 'finish-complete.json').exists()
        conductor = object.__new__(d.Conductor)
        conductor.out = root / 'conductor-reset'
        conductor.out.mkdir()
        conductor.api = failed_reset
        conductor.put = lambda *args: (_ for _ in ()).throw(AssertionError('D2 advanced after failed reset'))
        e5.save(conductor.out / 'lifecycle.json', {'originalSettings': {}})
        try:
            conductor.cleanup()
        except RuntimeError as error:
            assert 'reset failed' in str(error)
        else:
            raise AssertionError('D2 accepted failed reset')
        assert e5.load(conductor.out / 'cleanup-reset-result.json') == failure
        assert not (conductor.out / 'cleanup-complete.json').exists()
        publication_root = root / 'publication-test'
        evidence = publication_root / 'stage'
        evidence.mkdir(parents=True)
        (evidence / 'phase.json').write_text('{"actual":"raw"}')
        (evidence / 'zero-client.jsonl').write_text('')
        store_config = root / 'store.json'
        store_config.write_text(json.dumps({'logicalStore': 'private-evidence'}))
        record = {'id': 'stage', 'from': '2026-10-25T00:00:00Z', 'to': '2026-10-25T00:01:00Z', 'kpis': {'status': 'observed', 'visibility': 'PRIVATE_OPERATIONAL_ONLY', 'wholePoolJoules': None, 'reason': 'Missing meter evidence'}, '_folder': evidence}
        def upload_fixture(argv, **kwargs):
            assert argv[:3] == ['go', 'run', './hack/e5/publish']
            manifest = e5.load(argv[argv.index('--manifest') + 1])
            summary = e5.load(manifest['summary']['localPath'])
            assert summary['schemaVersion'] == 'serving-experiment.v1alpha1'
            assert summary['stages'][0]['kpis']['wholePoolJoules'] is None
            assert summary['sources'] == [{key: obj[key] for key in ('store', 'key')} for obj in manifest['objects']]
            assert all(Path(obj['localPath']).stat().st_size > 0 for obj in manifest['objects'])
            assert all(source['key'].startswith('serving/experiments.v1alpha1/campaign-1/raw/') for source in summary['sources'])
            assert e5.load(manifest['objects'][-1]['localPath'])['emptyFiles'] == ['stage/zero-client.jsonl']
            return SimpleNamespace(returncode=0, stdout='{"outcome":"Created"}', stderr='')
        with patch.object(e5.subprocess, 'run', side_effect=upload_fixture):
            assert e5.publish_experiment(publication_root, 'campaign-1', store_config, [record], 'PARTIAL')['outcome'] == 'Created'
        with patch.object(e5.subprocess, 'run') as upload:
            try:
                e5.publish_experiment(publication_root, 'campaign-1', store_config, [record], 'PARTIAL')
            except RuntimeError as error:
                assert 'new ID' in str(error)
            else:
                raise AssertionError('Existing publication attempt was overwritten')
            upload.assert_not_called()
        with patch.object(e5.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stdout='', stderr='immutable collision')):
            try:
                e5.publish_experiment(publication_root, 'collision', store_config, [record], 'PARTIAL')
            except RuntimeError as error:
                assert 'new ID' in str(error)
            else:
                raise AssertionError('Remote immutable collision was ignored')
        for invalid in ('../escape', 'bad/name', '.hidden', '비ASCII'):
            with patch.object(e5.subprocess, 'run') as upload:
                try:
                    e5.publish_experiment(publication_root, invalid, store_config, [record], 'PARTIAL')
                except ValueError:
                    pass
                else:
                    raise AssertionError('Unsafe publication ID accepted')
                upload.assert_not_called()
        frozen_root = root / 'frozen-settings'
        frozen_root.mkdir()
        frozen_binding = {'config': {'factors': []}, 'schedulerSettings': frozen_settings, 'schedule': [{'id': 'E5-' + str(i)} for i in range(7)], 'limitations': [], 'accounting': {'d': 'fixture', 'exactCost': 'fixture'}}
        frozen_path = root / 'frozen-binding.json'
        e5.save(frozen_path, frozen_binding)
        assert e5.summarize(frozen_path, frozen_root)['schedulerSettings'] == frozen_settings
        del frozen_binding['schedulerSettings']
        historical_path = root / 'historical-binding.json'
        e5.save(historical_path, frozen_binding)
        historical = e5.summarize(historical_path, frozen_root)
        assert historical['schedulerSettings'] is None and any('frozen scheduler settings' in row['reason'] for row in historical['missing'])
        print('PASS demo: campaign lifecycle, continuous parts and per-part success/p95/batch summaries, Ready gate early exit/timeout, static placement without scheduler mutation (including failure), start barrier clear/timeout, optional lifetime, server reset evidence and failure stop; mocked API/kubectl only')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('command', choices=['prepare', 'start', 'resume', 'stage', 'run', 'collect', 'summary', 'publish', 'finish'], nargs='?')
    parser.add_argument('--config', type=Path, help='Demo JSON; required for prepare')
    parser.add_argument('--out', type=Path, help='Fresh absolute external campaign directory; binding.json is stored here')
    parser.add_argument('--release', type=Path, help='Existing CPO release: executor CampaignOps, command e5-execute, bindingPath, sourceSHA, mainInstructionUTC')
    parser.add_argument('--stage', help='Configured stage ID; required for stage/collect')
    parser.add_argument('--experiment-id', help='publish: immutable ASCII ID; corrections require a new ID')
    parser.add_argument('--store-config', type=Path, help='publish: private logicalStore/S3 config for existing PutImmutable bridge')
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
    if args.command == 'publish':
        if not args.experiment_id or not args.store_config:
            parser.error('publish requires --experiment-id and --store-config; raw inputs upload before final immutable summary')
        publish(e5, binding, args.out, args.experiment_id, args.store_config)
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
            if args.command == 'run' and (folder / 'collected.json').exists() and e5.load(phase_path(folder))['status'] == 'DRAINED_NOT_EVALUATED':
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
        collect_stage(e5, cpo, selected)
        print('Collected evidence: ' + str(folder / 'collected.json'))
    elif args.command == 'finish':
        cpo.finish()
        cpo.cleanup_jobs()
        finish_definitions(e5, cpo)


if __name__ == '__main__':
    main()
