#!/usr/bin/env python3
"""Short CPO demo: prepare -> start -> stage/run -> collect -> summary -> finish.

Reuses E5's authenticated API, producer/barrier/counter lifecycle, raw collection
and server-owned reset cleanup, but not its normative R/G/10800-second scientific gates.
Outputs are PRIVATE operational evidence. publish --experiment-id ID --store-config
/private/store.json uploads raw inputs before a final immutable summary; corrections
require a new ID. --help and --self-check need no live experiment environment.

Demo finish/resume reset first disables owned autonomy, then waits within the
existing settleSeconds for public reservations and running/cancel actions to
clear. *-reset-wait.json retains observations and the last state. On timeout,
one reset is still attempted; reset failures/409 are retained without retries.

Autonomous setup requires minimumDemand keyed by the three canonical names;
after registration it reads canonical UIDs from workload GET responses and waits
up to 60s (2s polls) for the scheduler snapshot to agree before replacing obsolete
UID keys and freezing settings readback. Start refuses floor drift.
Static placement remains read-only.

Canonical definitions with matching recipe references, SLO and spec fields are
reused in place (UID/capacity rows retained); mismatches are deleted and registered
from the requested recipe/SLO. Binding records per-definition reuse. Finish and
preflight retain definitions by default; --delete-definitions opts into deletion
after owned cleanup, never for static placement.
Reset uses the synchronous OperationResult contract: HTTP budget is settleSeconds
+ powerDownSeconds (default 1800, server operation-timeout default upper bound)
+ apiSeconds, not settleSeconds alone; configure the power bound for the deployment.

Offline reporting: report DIRECTORY [--compare DIRECTORY ...] [--out DIRECTORY]
rebuilds timeline.svg and report-tables.md from retained phase/summary/client/
power/scheduler evidence only, without environment settings or cluster access.
--out defaults to DIRECTORY; an external --out leaves every input unchanged.
report --self-check exercises local synthetic multipart evidence and missing data.

Offline preflight rejudgment: preflight --judge DIRECTORY --out /tmp/FRESH
reuses summary generation without environment settings or cluster access, leaving
the retained capture unchanged. Only the historical post-reset reservations
KeyError can be resolved by complete retained cleanup evidence; other STOPs fail.
Public snapshot reservations use omitempty (an absent member encodes an empty
list), matching the existing finish-reset-wait and barrier reservation lookup.

E0 may set "readyGate": true: seconds becomes the maximum zero-load window,
ending early only when all three owned canonical workloads are Ready with positive
routing. Omit it (or false) for unchanged fixed-duration stages. phase.json and
summary.json retain readyGate.observedUTC (null on timeout), timedOut and the
last missingWorkloadUIDs; ready-gate-state.json retains the actual observation.

Diagnostic-only (진단 전용, E5 금지): top-level or stage-level
"tolerateArchive": ["power", "power-drain-inclusive"] in demo.py
allows only those power archive failures to advance after a completed request
window. A stage list overrides the top-level list ([] restores strict capture).
Original NOT_READY evidence remains; collected.json and summary.json record
missingArchives and energy stays null. All other collection/request failures stop.

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
timeboxes.barrierSeconds (default 300), independently of stage duration. It requires
zero reservations and running/cancel actions in the same snapshot, whose at must
be later than the latest completed action endedAt in state.results. Waiting evidence
records snapshotAt and reservationOwners. Timeout records STOP and the last state,
with no load started. Parts remain continuous.
For stages with producers, the same zero-state check also runs immediately
before each GO touch, bounded by the existing setup and earliest producer-ready
barrier deadlines (no timebox extension). GO observations/timeouts are retained
under startBarrier.producerRelease and go-barrier-<workload>-state.json.
On STOP/failure, only producer Jobs recorded as created by this run are removed
with exact UID checks, including earlier stages. Evidence-capture errors do not
skip deletion. A pre-existing same-name Job is named explicitly before setup;
it is never adopted or automatically deleted. Measurement Jobs are untouched.
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
import time

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
    ANALYSIS.generator_quality_config(config)
    if 'staticPlacement' in config and type(config['staticPlacement']) is not bool:
        raise ValueError('staticPlacement must be a boolean')
    allowed_archives = {'power', 'power-drain-inclusive'}
    if not isinstance(config.get('stages'), list) or not config['stages']:
        raise ValueError('A nonempty stages list is required')
    capacity = config.get('capacityR')
    if capacity is not None and (not isinstance(capacity, dict) or set(capacity) != set(ALIASES) or any(not e5.positive(v) for v in capacity.values())):
        raise ValueError('capacityR requires final approved positive mobilenet/smolvlm2/plc capacities')
    shares = e5.pool_shares(config)
    share_by_alias = {'mobilenet': shares['MNV'], 'smolvlm2': shares['Smol'], 'plc': shares['PLC']}
    if capacity is not None:
        e5.gateway_budget(config, sum(capacity[name] * share_by_alias[name] for name in ALIASES))
    for source in [config] + config.get('stages', []):
        if 'tolerateArchive' in source:
            value = source['tolerateArchive']
            if not isinstance(value, list) or any(not isinstance(name, str) or name not in allowed_archives for name in value):
                raise ValueError('tolerateArchive must be a list of power and/or power-drain-inclusive')
    stages = []
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
            part_id, seconds = part.get('id'), part.get('seconds')
            rates = part.get('rates')
            if 'rateFractions' in part:
                raise ValueError('Use alpha and poolShares; rateFractions is obsolete')
            if 'alpha' in part:
                alpha = part['alpha']
                if type(alpha) in (int, float):
                    alpha = dict.fromkeys(ALIASES, alpha)
                if rates is not None or capacity is None or not isinstance(alpha, dict) or set(alpha) != set(ALIASES) or any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in alpha.values()):
                    raise ValueError('alpha requires capacityR and finite nonnegative utilization, without rates')
                rates = {name: capacity[name] * share_by_alias[name] * alpha[name] for name in ALIASES}
            if not isinstance(part_id, str) or not re.fullmatch('[A-Za-z0-9][A-Za-z0-9-]{0,39}', part_id) or any(p['id'].lower() == part_id.lower() for p in parts):
                raise ValueError('Unique short Kubernetes-safe part IDs required within each stage')
            if not e5.positive(seconds) or not isinstance(rates, dict) or set(rates) != set(ALIASES):
                raise ValueError('Positive seconds and mobilenet/smolvlm2/plc rates required')
            if any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in rates.values()):
                raise ValueError('Rates must be finite nonnegative requests/second')
            if part is not row and any(key in part for key in ('readyGate', 'startBarrier', 'parts', 'tolerateArchive')):
                raise ValueError('readyGate, startBarrier, tolerateArchive and nested parts are not supported within parts')
            parts.append({'id': part_id, 'seconds': seconds, 'rates': {ALIASES[k]: v for k, v in rates.items()}, **({'alpha': dict(alpha), 'poolShares': dict(shares)} if 'alpha' in part else {})})
        seconds = sum(p['seconds'] for p in parts)
        rates = {name: max(p['rates'][name] for p in parts) for name in ALIASES.values()}
        if 'readyGate' in row and type(row['readyGate']) is not bool:
            raise ValueError('readyGate must be a boolean')
        if row.get('readyGate') and (stages or any(rates.values())):
            raise ValueError('readyGate is only supported on the first zero-load stage')
        stage = {'id': label, 'seconds': seconds, 'rates': rates, 'traces': {}}
        if 'tolerateArchive' in row or 'tolerateArchive' in config:
            stage['tolerateArchive'] = list(row.get('tolerateArchive', config.get('tolerateArchive', [])))
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

def bind_minimum_demand(api, spec):
    """Re-key declared canonical floors after registration; freeze only readback."""
    scheduler, _ = api('GET', '/api/v1/scheduler')
    if spec.get('staticPlacement'):
        return scheduler
    floors = spec.get('minimumDemand')
    if not isinstance(floors, dict) or set(floors) != set(ALIASES.values()) or any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in floors.values()):
        raise ValueError('minimumDemand requires finite nonnegative floors keyed by all canonical names')
    mapping = {}
    for name in floors:
        actual, _ = api('GET', '/api/v1/workloads/' + name)
        uid = actual['desired']['metadata'].get('uid')
        if not isinstance(uid, str) or not uid:
            raise RuntimeError('Missing canonical minimumDemand UID: ' + name)
        mapping[name] = uid
    if len(set(mapping.values())) != len(mapping):
        raise RuntimeError('Canonical minimumDemand UIDs are not unique')
    deadline = time.monotonic() + 60
    while True:
        workloads = scheduler['state']['snapshot']['workloads']
        missing = {name: uid for name, uid in mapping.items()
                   if [w['uid'] for w in workloads if w['name'] == name] != [uid]}
        if not missing:
            break
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError('minimumDemand scheduler snapshot UID mismatch after 60s; no settings PUT: ' + json.dumps(missing, sort_keys=True))
        time.sleep(min(2, remaining))
        scheduler, _ = api('GET', '/api/v1/scheduler')
    settings = copy.deepcopy(scheduler['settings'])
    settings.pop('staticRouting', None)
    settings.pop('staticRoutingValidatedAt', None)
    # Remove obsolete UID keys, retaining floors only for other live definitions.
    live = {w['uid'] for w in workloads} - set(mapping.values())
    expected = {uid: value for uid, value in (settings.get('minimumDemand') or {}).items() if uid in live}
    expected.update({mapping[name]: value for name, value in floors.items()})
    settings['minimumDemand'] = expected
    api('PUT', '/api/v1/scheduler/settings', settings)
    readback, _ = api('GET', '/api/v1/scheduler')
    identities = {w['name']: w['uid'] for w in readback['state']['snapshot']['workloads']}
    if readback['settings'].get('minimumDemand') != expected or any(identities.get(name) != uid for name, uid in mapping.items()):
        raise RuntimeError('minimumDemand readback/identity mismatch; no demo start')
    return readback



def canonical_definitions(e5, registration):
    """Keep matching applied identities; replace only mismatched definitions."""
    reuse = {}
    for name in e5.WORKLOADS:
        path = '/api/v1/workloads/' + name
        actual, status = registration.api('GET', path, accepted=(200, 404))
        reuse[name] = False
        if status == 404:
            continue
        desired = actual['desired']
        existing = desired['spec']
        recipe = registration.op['workloads'][name]
        refs = {key: {'name': recipe[key]} for key in ('modelRef', 'preprocessorRef', 'datasetRef')}
        applied = actual.get('appliedDefinition') or {}
        if (set(existing) == set(refs) | {'slo'}
                and {key: value for key, value in existing.items() if key != 'slo'} == refs
                and applied.get('slo') == registration.n['workloads'][name]['slo']
                and applied.get('uid') == desired['metadata']['uid']
                and applied.get('generation') == desired['metadata']['generation']):
            reuse[name] = True
            continue
        e5.save(registration.out / ('replace-' + name + '-before.json'), actual)
        result, _ = registration.api('DELETE', path, headers={'If-Match': desired['metadata']['resourceVersion']}, accepted=(202,))
        e5.save(registration.out / ('replace-' + name + '-ack.json'), result)
        deadline = time.monotonic() + registration.tb['settleSeconds']
        while registration.api('GET', path, accepted=(200, 404))[1] != 404:
            if time.monotonic() >= deadline:
                raise RuntimeError('Canonical replacement deletion did not settle: ' + name)
            time.sleep(1)
    scheduler = registration.canonical()
    identities = {}
    for name in e5.WORKLOADS:
        actual, _ = registration.api('GET', '/api/v1/workloads/' + name)
        identities[name] = actual['desired']['metadata']
    deadline = time.monotonic() + registration.tb['settleSeconds']
    while True:
        workloads = scheduler['state']['snapshot']['workloads']
        if all(any(w['name'] == name and w['uid'] == identity['uid']
                   and w['generation'] == identity['generation']
                   and w['slo'] == registration.n['workloads'][name]['slo']
                   for w in workloads) for name, identity in identities.items()):
            return scheduler, reuse
        if time.monotonic() >= deadline:
            raise RuntimeError('Canonical registered identities did not reach scheduler snapshot')
        time.sleep(1)
        scheduler, _ = registration.api('GET', '/api/v1/scheduler')


def assemble(e5, args, spec):
    """Reuse matching canonical definitions, then capture actual applied inputs."""
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
    registration.n = {'workloads': {name: {'slo': spec['measurementSLO'][next(alias for alias, canonical in ALIASES.items() if canonical == name)]} for name in e5.WORKLOADS}}
    def registration_api(method, path, body=None, headers=None, accepted=(200,)):
        if spec.get('staticPlacement') and method != 'GET':
            raise RuntimeError('No canonical mutation permitted during static capture')
        result, status_code = wire.Prepare.api(registration, method, path, body, headers, accepted)
        io.record({'kind': 'canonical-management', 'method': method, 'path': path, 'status': status_code})
        return result, status_code
    registration.api = registration_api
    if spec.get('staticPlacement'):
        if any(name not in by_name for name in e5.WORKLOADS):
            raise RuntimeError('Static placement requires existing canonical definitions')
        reused = {name: True for name in e5.WORKLOADS}
    else:
        scheduler, reused = canonical_definitions(e5, registration)
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
    c.update(approval={'sourceSHA': source, 'controlImageDigest': status['imageID']}, inputs={'nodes': str(args.out / 'nodes.json'), 'scheduler': str(args.out / 'scheduler.json')}, timeboxes=spec['timeboxes'], collection=collection, workloads=[], canonicalDefinitionsReused=reused)
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
    c['demoSkipped'] = ['E5 R/kappa capacity binding and G clipping', '10800-second normative schedule and factor baselines', 'phase6 scientific approvals/D1/D6/D9 judgment gates', 'image/source publication and profiling-vs-evaluation metadata Jobs; existing pinned images/datasets reused']
    c['demoProvenance'] = {'recipe': str(recipe_path), 'controlSourceSHA': 'Main-provided helm-revision source claim resolved in git, not inferred from image bytes', 'controlImageDigest': 'Live Ready Pod containerStatus.imageID', 'requestTiming': 'D2 Conductor.job socket/worker baseline plus explicit requestTiming overrides; absent lifetime omits client deadline header', 'measurementSLO': spec['measurementSLO']}
    config_path = args.out / 'demo-e5-config.json'
    e5.save(config_path, c)
    return config_path


def finish_definitions(e5, cpo, *, delete_definitions=False):
    """Retain identities unless deletion was explicitly requested after cleanup."""
    if cpo.static_placement() or not delete_definitions:
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
    if spec.get('preflight'):
        if spec.get('staticPlacement') or len(stages) != 2 or not stages[0].get('readyGate') or len(stages[1]['parts']) != 1 or stages[1]['seconds'] != 60 or stages[1]['rates'] != {ALIASES['mobilenet']: 20, ALIASES['smolvlm2']: .3, ALIASES['plc']: 40}:
            raise ValueError('Preflight requires zero-load Ready gate then 60s mixed load (20/.3/40)')
        e5.require_strict_archives(spec)
        auth = e5.module('demo_auth', HERE / 'auth.py')
        if auth.lifetime_budget(stages, spec['timeboxes'], spec.get('tokenSafetyMarginSeconds', 60)) > 1620:
            raise ValueError('Preflight duration budget exceeds 27 minutes')
    token_config = e5.load(spec['e5Config']) if spec.get('e5Config') else e5.load(spec.get('operationalRecipe', e5.OPERATIONAL_RECIPE))
    token_config = dict(token_config, timeboxes=spec.get('timeboxes', token_config.get('timeboxes')),
                        tokenSafetyMarginSeconds=spec.get('tokenSafetyMarginSeconds', token_config.get('tokenSafetyMarginSeconds', 60)))
    e5.token_preflight(token_config, stages)
    config_path = Path(spec['e5Config']).resolve() if spec.get('e5Config') else assemble(e5, args, spec)
    c = e5.load(config_path)
    if 'generatorQuality' in spec:
        c['generatorQuality'] = ANALYSIS.generator_quality_config(spec)
    else:
        ANALYSIS.generator_quality_config(c)
    if spec.get('preflight'):
        e5.require_strict_archives(c)
    e5.need(c, ['approval', 'inputs', 'timeboxes', 'workloads', 'collection', 'endpoint', 'caFile', 'tokenFile'], 'e5Config')
    e5.need(c['approval'], ['sourceSHA', 'controlImageDigest'], 'approval')
    e5.need(c['timeboxes'], e5.REQUIRED['timeboxes'], 'timeboxes')
    if any(not e5.positive(v) for v in c['timeboxes'].values()):
        raise ValueError('Finite positive E5 timeboxes required')
    c['tokenSafetyMarginSeconds'] = token_config['tokenSafetyMarginSeconds']
    c['tokenLifetime'] = e5.token_preflight(c, stages)
    if len(c['workloads']) != 3 or {w['name'] for w in c['workloads']} != set(ALIASES.values()):
        raise ValueError('Existing three canonical workload definitions required')
    if any(c['timeboxes']['setupSeconds'] + s['seconds'] + c['timeboxes']['drainSeconds'] + c['timeboxes']['flushSeconds'] > 4200 for s in stages):
        raise ValueError('Stage exceeds existing finite collector duration limit')
    wire = e5.module('demo_floor_binding', HERE / 'e5-wire.py')
    manager = object.__new__(wire.Prepare)
    manager.op, manager.out, manager.tb = c, args.out, c['timeboxes']
    d = e5.module('demo_floor_client', HERE / 'd2/conduct_before.py')
    manager.http = e5.urllib.request.build_opener(d.NoRedirect(), e5.urllib.request.HTTPSHandler(context=e5.ssl.create_default_context(cafile=c['caFile'])))
    scheduler = bind_minimum_demand(manager.api, spec)
    args.out.mkdir(parents=True, exist_ok=True)
    scheduler_path = args.out / 'scheduler-bound.json'
    e5.save(scheduler_path, scheduler)
    c['inputs'] = dict(c['inputs'], scheduler=str(scheduler_path))
    sys.path.insert(0, str(e5.REPO / 'load-generator/src'))
    from chill_loadgen import cli
    producer = module('demo_producer', HERE / 'e5-producer.py')
    payload = {}
    sources = {'demoConfig': e5.ref(args.config), 'e5Config': e5.ref(config_path), 'environment': e5.ref(e5.ENVIRONMENT_PATH)}
    sources['scheduler'] = e5.ref(c['inputs']['scheduler'])
    if spec.get('G') is not None:
        sources['gMeasurement'] = e5.ref(spec.get('GSource') or spec['inputs']['gMeasurement'])
    scheduler_settings = e5.load(c['inputs']['scheduler'])['settings']
    scheduler_settings = dict(scheduler_settings, powerObservation=e5.power_observation(c))
    for filename in ('demo.py', 'e5.py', 'auth.py', 'e5-wire.py', 'e5-producer.py', 'e5-archive.py', 'e5-raw.py'):
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
    binding = {'schema': 'demo-e5-binding.v1', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': e5.now(), 'schedulerSettings': scheduler_settings, 'canonicalDefinitionsReused': c.get('canonicalDefinitionsReused', {}), 'config': c, 'sources': sources, 'payload': payload, 'schedule': stages, 'demoConfig': spec}
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
    cpo.c['smoke'] = stage
    if phase['status'] == 'STOP':
        archive_stop = bool(stage.get('tolerateArchive')) and phase.get('error') == 'Required exact-hour archive/current capture incomplete; preserve private index and stop' and bool(phase.get('drainedUTC'))
        if (phase.get('error') != 'Drain/terminal timebox exceeded; preserve partial evidence, stop' and not archive_stop) or not phase.get('startUTC') or not phase.get('endUTC'):
            raise RuntimeError('Only an otherwise completed producer window stopped by planner-idle timeout or explicitly tolerated archives can be collected without replay')
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
        phase['collectionRecovery'] = {'originalPhase': str(folder / 'phase.json'), 'originalStatus': phase['status'], 'originalError': phase['error'], 'meaning': 'No replay; original STOP/client/start/end retained. ' + ('Original drain/post-boundary retained; only explicitly tolerated archives may be missing.' if archive_stop else 'Drain and post-boundary are late recovery observations; drain-inclusive energy includes this interval.')}
        if not archive_stop:
            phase['drainedUTC'] = cpo.request_drain(folder, phase)
            cpo.boundary(folder, 'recovery-post')
            phase['postBoundaryPrefix'] = 'recovery-post'
        phase['status'] = 'DRAINED_NOT_EVALUATED'
        cpo.collect(folder, phase=phase)
        e5.save(derived, phase)
    else:
        cpo.collect(folder)


def resume(e5, cpo):
    """Reset the owned scope once, then re-enable autonomy with original evidence retained."""
    cpo.check_power_observation()
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


def wait_preflight_empty(e5, cpo):
    deadline = e5.time.monotonic() + cpo.tb['settleSeconds']
    while True:
        state = cpo.state()
        e5.save(cpo.out / 'preflight-final-state.json', state)
        snap = state['snapshot']
        if not snap['instances'] and not snap.get('reservations') and not any(a.get('status') in ('running', 'cancel') for a in state.get('actions') or []):
            return
        if e5.time.monotonic() >= deadline:
            raise RuntimeError('Preflight final instances/reservations did not reach zero within settle timebox')
        e5.time.sleep(min(1, max(0, deadline - e5.time.monotonic())))


def preflight_result(e5, binding, out, stages, *, judge=False):
    """Missing or partial evidence is FAIL; never infer cleanup from reset ACKs."""
    ready = next((s.get('readyGate') for s in stages if s.get('readyGate')), {})
    configured = binding['config']['workloads']
    ready_count = len(configured) - len(ready.get('missingWorkloadUIDs', [w['uid'] for w in configured]))
    loaded = [s for s in stages if any(m.get('offered', 0) for m in s.get('workloads', {}).values())]
    success = len(loaded) == 1 and set(loaded[0]['workloads']) == {w['name'] for w in configured} and all(
        m.get('offered', 0) > 0 and m.get('clientRows') == m['offered'] and m.get('httpSuccess') == m['offered']
        for m in loaded[0]['workloads'].values())
    def snapshot(path):
        return e5.load(path)['state']['snapshot'] if path.name == 'initial.json' else e5.load(path)['snapshot']
    initial = snapshot(out / 'initial.json') if (out / 'initial.json').exists() else None
    final_path = out / 'preflight-final-state.json'
    final = snapshot(final_path) if final_path.exists() else None
    power_complete = len(stages) == len(binding['schedule']) and all(
        (stage_folder(out, s) / name).exists() and e5.load(stage_folder(out, s) / name).get('complete') is True
        for s in binding['schedule'] for name in ('power.json', 'power-drain-inclusive.json'))
    checks = {
        'emptyStart': initial is not None and 'instances' in initial and not initial['instances'] and not initial.get('reservations'),
        'ready3': len(configured) == ready_count == 3 and bool(ready.get('observedUTC')) and not ready.get('timedOut'),
        'success100Percent': success,
        'powerComplete': power_complete,
        'finalInstances0': final is not None and 'instances' in final and not final['instances'],
        'finalReservations0': final is not None and 'instances' in final and not final.get('reservations'),
        'finalTransitions0': final_path.exists() and not any(a.get('status') in ('running', 'cancel') for a in e5.load(final_path).get('actions') or []),
        'stagesComplete': len(stages) == len(binding['schedule']) and all(s.get('status') == 'DRAINED_NOT_EVALUATED' for s in stages),
        'finishComplete': (out / 'finish-complete.json').exists(),
        'noStop': not (out / 'preflight-STOP.json').exists(),
    }
    stop_path = out / 'preflight-STOP.json'
    stop = e5.load(stop_path) if stop_path.exists() else None
    resolved_stop = judge and stop is not None and stop.get('error') == "'reservations'" and all(value for key, value in checks.items() if key != 'noStop')
    if resolved_stop:
        checks['noStop'] = True
    return {'status': 'PASS' if all(checks.values()) else 'FAIL',
            'checks': {key: 'PASS' if value else 'FAIL' for key, value in checks.items()},
            'readyWorkloads': ready_count, 'scope': 'Operational preflight only; not E5 scientific PASS',
            'resolvedHistoricalStop': stop if resolved_stop else None}


def summarize(e5, binding, out, destination=None, *, judge=False):
    raw = e5.module('demo_raw', HERE / 'e5-raw.py')
    analysis = ANALYSIS
    destination = destination or out
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
        events, provenance = raw.scheduler_events(folder, approved_root=out) if judge else raw.scheduler_events(folder)
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
        collected = e5.load(folder / 'collected.json') if (folder / 'collected.json').exists() else {}
        missing_archives = collected.get('missingArchives', {})
        if 'power' in missing_archives:
            power = {}
        if 'power-drain-inclusive' in missing_archives:
            drain_power = {}
        summary = {'stage': stage['id'], 'status': phase['status'], 'startUTC': phase['startUTC'], 'endUTC': phase['endUTC'], 'drainedUTC': phase.get('drainedUTC'), 'gapFromPreviousOfferedWindowSeconds': gap, 'gapMeaning': 'No offered demo requests during per-stage drain/collection/setup; failures in this gap are not measurable by these producers', 'placementChanges': changes, 'actions': actions, 'transitionWindowsEpoch': windows, 'schedulerCapture': provenance, 'energyJ': power.get('wholePoolJoules') if power.get('complete') else None, 'activeEnergyJ': power.get('activePoolJoules') if power.get('complete') else None, 'drainInclusiveEnergyJ': drain_power.get('wholePoolJoules') if drain_power.get('complete') else None, 'energyScope': 'Shared whole/active pool; never attributed to individual workloads', 'workloads': {}}
        summary['collectionRecovery'] = phase.get('collectionRecovery')
        summary['missingArchives'] = missing_archives
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
            successful = [r for r in clients if r.get('issued') is not False and r.get('status') == 200 and not r.get('error')]
            good = {id(r) for r in successful}
            start = phase['starts'].get(name)
            fidelity = analysis.issuance_fidelity(clients, stage['traces'][name]['offsetsNS'], (start or {}).get('monotonicNS'), round(stage['seconds'] * 1e9), analysis.stats, analysis.generator_quality_config(binding.get('demoConfig', {}) if 'generatorQuality' in binding.get('demoConfig', {}) else binding['config']))
            indexed = raw.index_requests(folder, w['uid'], phase['startUTC'], phase['endUTC'], actual_response_ids=[r['requestID'] for r in clients if r.get('requestID')])
            response_ids = [r['requestID'] for r in clients if r.get('requestID')]
            if len(response_ids) != len(set(response_ids)):
                indexed = dict(indexed, errors=indexed['errors'] + ['Duplicate client requestIDs cannot identify independent offered responses'])
            timings = {id(r): analysis.request_timing(r, indexed['finals'].get(r.get('requestID')), indexed.get('receivedAtNS', {}).get(r.get('requestID')), (start or {}).get('monotonicNS')) for r in clients}
            latencies = {id(r): timings[id(r)]['sloLatencyNS'] for r in successful}
            missing_latency = sum(value is None for value in latencies.values())
            transition_failures = None if fidelity['status'] == 'UNKNOWN' else 0
            if start and transition_failures is not None:
                for row in clients:
                    sent_ns = row.get('sentMonotonicNS') if row.get('issued') is not False and row.get('error') != 'UnsentAtWindowEnd' else analysis.scheduled_monotonic_ns(row, start['monotonicNS'])
                    if type(sent_ns) is not int or type(row.get('finishedMonotonicNS')) is not int:
                        transition_failures = None
                        break
                    sent = epoch(start['at']) + (sent_ns - start['monotonicNS']) / 1e9
                    end = epoch(start['at']) + (row['finishedMonotonicNS'] - start['monotonicNS']) / 1e9
                    if id(row) not in good and any(sent <= hi and end >= lo for lo, hi in windows):
                        transition_failures += 1
            measurement = binding.get('demoConfig', {}).get('measurementSLO', {}).get(next(alias for alias, canonical in ALIASES.items() if canonical == name), w['slo'])
            slo_ms = measurement['targetLatencyNanos'] / 1e6
            within = sum(value is not None and value <= measurement['targetLatencyNanos'] for value in latencies.values())
            metric = {'rate': stage['rates'][name], 'offered': offered, 'clientRows': len(clients), 'missingClientRows': max(0, offered - len(clients)), 'httpSuccess': len(successful), 'successFraction': len(successful) / offered if offered else None, 'successNumerator': len(successful), 'successDenominator': offered, 'withinSLONumerator': within, 'withinSLODenominator': offered, 'latencyMS': analysis.stats([value / 1e6 for value in latencies.values() if value is not None]), 'latencyScope': 'Gateway elapsedNanos: CHILL receipt to response per 03_routing; client and scheduled durations are reference only', 'missingSLOLatencyRows': missing_latency, 'serviceLatencyMS': analysis.stats([timings[id(r)]['actualLatencyNS'] / 1e6 for r in successful if timings[id(r)]['actualLatencyNS'] is not None]), 'sloMS': slo_ms, 'sloPercentile': w['slo']['percentile'], 'withinSLOFraction': within / offered if offered and not missing_latency and not indexed['errors'] and fidelity['status'] != 'UNKNOWN' else None, 'transitionFailures': transition_failures if provenance.get('complete') and len(clients) == offered and (start or not offered) else None, 'transitionFailureMeaning': 'Failed/unsent client requests overlapping observed placement-change brackets or elapsed scheduler actions; unsent intervals begin at scheduled arrival; null if scheduler/client coverage incomplete', 'errors': [r for r in clients if id(r) not in good]}
            metric.update(sloPercentile=measurement['percentile'], schedulerSLO=w['slo'], measurementSLO=measurement)
            metric['issuanceFidelity'] = fidelity
            metric.update(scheduledLatencyMS=analysis.stats([t['scheduledLatencyNS'] / 1e6 for t in timings.values() if t['scheduledLatencyNS'] is not None]), entranceDelayNS=analysis.stats([t['entranceDelayNS'] for t in timings.values()]), entranceDelayMeaning='receivedAt minus sentAtUnixNS; cross-host clock offset/uncertainty retained, including negative values; not pure network delay', missingEntranceDelayRows=sum(t['entranceDelayNS'] is None for t in timings.values()), rawJoinErrors=indexed['errors'])
            quality = fidelity['generatorQuality']['status']
            metric['SLOVerdict'] = 'UNMEASURED' if not offered else 'INVALID_GENERATOR' if quality == 'INVALID_GENERATOR' else 'UNKNOWN' if quality == 'UNKNOWN' or metric['withinSLOFraction'] is None else 'OBSERVED_THRESHOLD_MET' if within >= math.ceil(offered * measurement['percentile'] / 100) else 'FAIL'
            summary['workloads'][name] = metric
            rows.append((summary, name, metric))
            for part_summary, part in zip(summary.get('parts', []), stage['parts']):
                lo, hi = part_summary['startOffsetNS'], part_summary['endOffsetNS']
                selected = [r for r in clients if lo <= r['scheduledOffsetNs'] < hi]
                successes = [r for r in selected if r.get('issued') is not False and r.get('status') == 200 and not r.get('error')]
                count = sum(lo <= offset < hi for offset in stage['traces'][name]['offsetsNS'])
                part_latencies = [latencies[id(r)] for r in successes]
                part_within = sum(value is not None and value <= measurement['targetLatencyNanos'] for value in part_latencies)
                part_summary['workloads'][name] = {'rate': part['rates'][name], 'offered': count, 'clientRows': len(selected), 'missingClientRows': max(0, count - len(selected)), 'httpSuccess': len(successes), 'successNumerator': len(successes), 'successDenominator': count, 'successFraction': len(successes) / count if count else None, 'latencyMS': analysis.stats([value / 1e6 for value in part_latencies if value is not None]), 'missingSLOLatencyRows': sum(value is None for value in part_latencies), 'serviceLatencyMS': analysis.stats([timings[id(r)]['actualLatencyNS'] / 1e6 for r in successes if timings[id(r)]['actualLatencyNS'] is not None]), 'scheduledLatencyMS': analysis.stats([timings[id(r)]['scheduledLatencyNS'] / 1e6 for r in selected if timings[id(r)]['scheduledLatencyNS'] is not None]), 'entranceDelayNS': analysis.stats([timings[id(r)]['entranceDelayNS'] for r in selected]), 'withinSLONumerator': part_within, 'withinSLODenominator': count, 'withinSLOFraction': part_within / count if count and all(value is not None for value in part_latencies) and not indexed['errors'] and fidelity['status'] != 'UNKNOWN' else None, 'latencyScope': metric['latencyScope'], 'producerWindowEpoch': [epoch(start['at']) + lo / 1e9, epoch(start['at']) + hi / 1e9] if start else None, 'generatorQuality': fidelity['generatorQuality']}
                bounds = part_summary['workloads'][name]['producerWindowEpoch']
                part_summary['workloads'][name]['producerWindowUTC'] = [e5.dt.datetime.fromtimestamp(t, e5.dt.timezone.utc).isoformat() for t in bounds] if bounds else None
                part_summary['workloads'][name]['windowMeaning'] = 'Frozen scheduled rate interval anchored to actual producer start; not an independently observed boundary event'
        qualities = [m['issuanceFidelity']['generatorQuality']['status'] for m in summary['workloads'].values() if m['offered']]
        summary['generatorQualityStatus'] = 'INVALID_GENERATOR' if 'INVALID_GENERATOR' in qualities else 'UNKNOWN' if 'UNKNOWN' in qualities else 'VALID' if qualities else 'NOT_OFFERED'
        if summary['generatorQualityStatus'] == 'INVALID_GENERATOR':
            summary['status'] = 'INVALID_GENERATOR'
        summary_folder = stage_folder(destination, stage)
        summary_folder.mkdir(parents=True, exist_ok=True)
        e5.save(summary_folder / 'summary.json', summary)
        stage_summaries.append(summary)
    scheduler_settings = binding.get('schedulerSettings')
    result = {'schema': 'e5-demo-summary.v1', 'schedulerSettings': scheduler_settings, 'missingReasons': {'schedulerSettings': None if scheduler_settings is not None else 'Binding does not contain frozen scheduler settings; current settings are not substituted'}, 'stages': stage_summaries}
    qualities = [summary.get('generatorQualityStatus', 'UNKNOWN') for summary in stage_summaries]
    quality_status = 'INVALID_GENERATOR' if 'INVALID_GENERATOR' in qualities else 'UNKNOWN' if 'UNKNOWN' in qualities or len(stage_summaries) != len(binding['schedule']) else 'VALID' if 'VALID' in qualities else 'NOT_OFFERED'
    result['generatorQualityStatus'] = quality_status
    result['status'] = quality_status if quality_status in ('INVALID_GENERATOR', 'UNKNOWN') else 'DESCRIPTIVE'
    if binding.get('demoConfig', {}).get('preflight'):
        result['preflight'] = preflight_result(e5, binding, out, stage_summaries, judge=judge)
    e5.save(destination / 'summary.json', result)
    def fmt(value):
        return '—' if value is None else f'{value:.3f}' if isinstance(value, float) else str(value)
    table = ['# Autonomous workload-pool demo', '', 'SLO latency is gateway elapsedNanos from CHILL receipt to response (03_routing). Scheduled end-to-end and actual client HTTP round trip are reference only; entrance delay is receivedAt minus sentAtUnixNS with cross-host clock uncertainty retained, not clamped. Generator lateness quality is separate; missing policy is UNKNOWN, exceeded policy is INVALID_GENERATOR, not an SLO failure. Offered is the full frozen trace, including unsent requests. SLO is the configured measurementSLO, or captured scheduler SLO when omitted. Energy is shared pool energy, repeated per workload (do not sum rows). Missing evidence is —, not zero.', '', '| Stage | Workload | Rate/s | Success/offered | Success % | p95 ms | p99 ms | SLO p/ms | Within SLO % | Pool J | Active J | Drain pool J | Placement changes | Transition failures | Gap s |', '|---|---|---:|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|']
    if 'preflight' in result:
        table[2:2] = ['Preflight: **' + result['preflight']['status'] + '** (Ready ' + str(result['preflight']['readyWorkloads']) + '/3; operational only).', '',
                      *[key + ': ' + value for key, value in result['preflight']['checks'].items()], '']
    for s, name, m in rows:
        table.append('| ' + ' | '.join(map(str, [s['stage'], name, fmt(m['rate']), f"{m['httpSuccess']}/{m['offered']}", fmt(None if m['successFraction'] is None else m['successFraction'] * 100), fmt(m['latencyMS']['p95']), fmt(m['latencyMS']['p99']), f"p{m['sloPercentile']}/{fmt(m['sloMS'])}", fmt(None if m['withinSLOFraction'] is None else m['withinSLOFraction'] * 100), fmt(s['energyJ']), fmt(s['activeEnergyJ']), fmt(s['drainInclusiveEnergyJ']), len(s['placementChanges']), fmt(m['transitionFailures']), fmt(s['gapFromPreviousOfferedWindowSeconds'])])) + ' |')
    table += ['', '## Issuance fidelity', '']
    for s, name, m in rows:
        fidelity = m['issuanceFidelity']
        lateness = fidelity.get('sendLatenessNS', {})
        table.append(f"Issuance {s['stage']}/{name}: {fidelity['status']}; issued/planned={fidelity['issued']}/{fidelity['offered']}, supplied={fidelity['supplied']}; gaps >50ms={fmt(fidelity.get('gapsOver50MS'))}, sends late >50ms={fmt(fidelity.get('sendsLateOver50MS'))}; lateness ns p50/p95/p99/max={fmt(lateness.get('p50'))}/{fmt(lateness.get('p95'))}/{fmt(lateness.get('p99'))}/{fmt(lateness.get('max'))}.")
    for s in stage_summaries:
        table += ['', '## ' + s['stage'], '']
        if s.get('missingArchives'):
            table.append('Tolerated missing archives (not measured): ' + json.dumps(s['missingArchives']))
        if s.get('collectionRecovery'):
            table.append(s['collectionRecovery']['meaning'] + ' See collectionRecovery in summary.json.')
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
    (destination / 'summary.md').write_text('\n'.join(table) + '\n')
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
            workloads[name]['population'] = 'Full frozen offered arrivals including unsent; SLO uses gateway receipt-to-response elapsedNanos; scheduled and client RTT are reference only'
            workloads[name]['missingReasons'] = {
                'successFraction': 'No offered requests' if not metric['offered'] else None,
                'withinSLOFraction': 'No offered requests' if not metric['offered'] else 'Successful raw final/elapsedNanos or population coverage missing' if metric['missingSLOLatencyRows'] or metric['rawJoinErrors'] else None,
                'latencyMS': 'No successful gateway receipt-to-response observations' if not metric['latencyMS']['samples'] else 'Some successful raw final/elapsedNanos missing' if metric['missingSLOLatencyRows'] else None,
                'transitionFailures': 'Scheduler/client coverage incomplete' if metric['transitionFailures'] is None else None,
                'issuanceFidelity': metric['issuanceFidelity'].get('reason'),
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
    planned = [0, 10_000_000, 20_000_000]
    clients = [{'requestIndex': i, 'scheduledOffsetNs': offset, 'scheduledMonotonicNS': 100 + offset,
                'sentMonotonicNS': 100 + offset + delay, 'finishedMonotonicNS': 100 + offset + delay + 1_000_000,
                'latencyMs': 1, 'serviceLatencyMs': 1}
               for i, (offset, delay) in enumerate(zip(planned, [0, 0, 170_000_000]))]
    fidelity = ANALYSIS.issuance_fidelity(clients[::-1], planned, 100, 100_000_000, ANALYSIS.stats)
    assert fidelity['supplied'] and fidelity['issuanceFraction'] == 1 and fidelity['sendsAfterWindow'] == 1
    assert fidelity['sendLatenessNS']['p95'] == 170_000_000 and fidelity['sendsLateOver50MS'] == 1
    assert ANALYSIS.scheduled_latency_ns(clients[-1], None) == 171_000_000
    historical = dict(clients[-1])
    historical.pop('scheduledMonotonicNS')
    assert ANALYSIS.scheduled_latency_ns(historical, 100) == 171_000_000
    assert ANALYSIS.scheduled_latency_ns(historical, None) is None
    unsent = dict(clients[-1], error='UnsentAtWindowEnd')
    unsent.pop('sentMonotonicNS')
    fidelity = ANALYSIS.issuance_fidelity(clients[:2] + [unsent], planned, 100, 100_000_000, ANALYSIS.stats)
    assert fidelity['offered'] == 3 and fidelity['issued'] == 2 and not fidelity['supplied']
    assert fidelity['issuanceFraction'] == 2 / 3 and fidelity['unissued'] == 1
    assert ANALYSIS.scheduled_latency_ns(unsent, 100) is None
    policy = ANALYSIS.generator_quality_config({'generatorQuality': {'latenessToleranceNS': 50_000_000, 'maxExceedingFraction': 1 / 3}})
    assert ANALYSIS.issuance_fidelity(clients, planned, 100, 100_000_000, ANALYSIS.stats, policy)['generatorQuality']['status'] == 'VALID'
    strict = dict(policy, maxExceedingFraction=0)
    assert ANALYSIS.issuance_fidelity(clients, planned, 100, 100_000_000, ANALYSIS.stats, strict)['generatorQuality']['status'] == 'INVALID_GENERATOR'
    assert ANALYSIS.issuance_fidelity(clients, planned, 100, 100_000_000, ANALYSIS.stats)['generatorQuality']['status'] == 'UNKNOWN'
    final = {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 500_000}
    timing = ANALYSIS.request_timing(dict(clients[-1], sentAtUnixNS=200), final, 150, 100)
    assert timing['sloLatencyNS'] == 500_000 and timing['scheduledLatencyNS'] == 171_000_000 and timing['entranceDelayNS'] == -50
    assert ANALYSIS.request_timing(clients[-1], None, 150, 100)['sloLatencyNS'] is None
    assert ANALYSIS.request_timing(clients[-1], final, None, 100)['entranceDelayNS'] is None
    for invalid in ({}, {'latenessToleranceNS': -1, 'maxExceedingFraction': 0}, {'latenessToleranceNS': 1, 'maxExceedingFraction': 1.1}, {'latenessToleranceNS': True, 'maxExceedingFraction': 0}):
        try:
            ANALYSIS.generator_quality_config({'generatorQuality': invalid})
        except ValueError:
            pass
        else:
            raise AssertionError('Accepted invalid generator quality policy')
    with tempfile.TemporaryDirectory(prefix='chill-demo-') as temp:
        root = Path(temp)
        environment = root / 'environment.json'
        environment.write_text(json.dumps({'paths': {'repository': str(HERE.parents[1]), 'ops': temp, 'campaign': temp, 'd2State': temp}, 'e5': {'operationalRecipe': temp, 'nodes': [], 'excludedNodes': []}, 'd2': {'repoBin': temp, 'apiBase': 'https://fixture.invalid', 'caFile': temp, 'tokenFile': temp, 'kubectl': ['fixture-kubectl'], 'jobTemplate': temp, 'workloadUID': 'fixture', 'workloadName': 'fixture'}}))
        with patch.dict(os.environ, {'CHILL_EXPERIMENT_CONFIG': str(environment)}):
            e5 = module('demo_smoke_e5', HERE / 'e5.py')
            d = e5.module('demo_smoke_conduct', HERE / 'd2/conduct_before.py')
        # Settings share the configured budget, with one readback and no PUT retry.
        settings_probe = object.__new__(e5.CPO)
        requested = {'autonomousWorkloads': [], 'excludedNodes': ['held'], 'controls': {'membership': True}}
        for seconds in (5, 129):
            settings_probe.tb = {'apiSeconds': seconds}
            for failure, observed in (
                    (None, requested),
                    (TimeoutError('response timed out'), requested),
                    (e5.urllib.error.URLError(TimeoutError('response timed out')), requested),
                    (TimeoutError('response timed out'), dict(requested, autonomousWorkloads=['owned'])),
                    (e5.urllib.error.URLError('connection refused'), requested)):
                settings_calls = []
                def settings_api(method, path, body=None, timeout=None):
                    settings_calls.append((method, path, timeout))
                    assert path == '/api/v1/scheduler/settings'
                    if method == 'PUT':
                        assert body == requested
                        if failure is not None:
                            raise failure
                        return {'validatedAt': 'fixture'}
                    assert method == 'GET'
                    return copy.deepcopy(observed)
                settings_probe.io = SimpleNamespace(api=settings_api)
                assert settings_probe.settings() == observed
                settings_calls.clear()
                timed_out = isinstance(getattr(failure, 'reason', failure), TimeoutError)
                try:
                    receipt = settings_probe.put(dict(requested, staticRouting={}, staticRoutingValidatedAt='old', powerObservation={}))
                except RuntimeError as error:
                    assert timed_out and observed != requested and 'readback mismatch; no retransmission' in str(error)
                except e5.urllib.error.URLError as error:
                    assert error is failure and not timed_out
                else:
                    assert observed == requested
                    assert receipt == ({'appliedByReadback': True, 'settings': observed} if timed_out else {'validatedAt': 'fixture'})
                expected_methods = ['PUT', 'GET'] if timed_out else ['PUT']
                assert settings_calls == [(method, '/api/v1/scheduler/settings', max(30, seconds)) for method in expected_methods]
        fixture_power = {'sampling': {'Interval': 750000000, 'Duration': 5000000000, 'RequestTimeout': 700000000, 'MaxEvents': 9}, 'coverage': {'MaximumGap': 3000000000, 'MaximumBoundaryDistance': 1000000000, 'MinimumCoverage': .75}}
        config = {'collection': {'schedulerConfigMap': 'fixture'}, 'timeboxes': {'apiSeconds': 1}}
        with patch.object(e5.subprocess, 'run', return_value=SimpleNamespace(stdout=json.dumps({'data': {'scheduler.json': json.dumps({'powerObservation': fixture_power})}}))):
            assert e5.power_observation(config) == fixture_power
        e5.power_observation = lambda c: copy.deepcopy(fixture_power)
        template = root / 'job.json'
        template.write_text(json.dumps({'spec': {'template': {'spec': {'restartPolicy': 'Never', 'containers': [{'name': 'load', 'image': 'fixture@sha256:fixture'}]}}}}))
        workloads = [{'name': name, 'uid': name, 'generation': 1, 'slo': {'percentile': 95, 'targetLatencyNanos': 1000000000}, 'jobTemplate': str(template), 'producerEndpoint': 'https://fixture.invalid', 'producerTokenFile': '/fixture/token', 'producerCAFile': '/fixture/ca', 'producerDatasetDir': '/fixture/data', 'fields': ['input'], 'workers': 1, 'gatewayCapSeconds': 2, 'lifetimeSeconds': 1, 'deliverySeconds': .1, 'socketSeconds': 3} for name in ALIASES.values()]
        config = {'stages': [{'id': 'E0', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 0)}, {'id': 'A', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 10000)}, {'id': 'B', 'seconds': .001, 'rates': {'mobilenet': 20000, 'smolvlm2': 10000, 'plc': 10000}}, {'id': 'C', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 10000)}]}
        config['stages'][-1] = {'id': 'C', 'parts': [{'id': 'high', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 20000)}, {'id': 'low', 'seconds': .001, 'rates': dict.fromkeys(ALIASES, 10000)}]}
        stages = schedule(config, e5)
        evaluation = json.loads((HERE / 'demo-config.e5.example.json').read_text())
        try:
            schedule(evaluation, e5)
        except ValueError:
            pass
        else:
            raise AssertionError('Unapproved E5 capacityR accepted')
        evaluation['capacityR'] = dict.fromkeys(ALIASES, .01)
        evaluated = schedule(evaluation, e5)
        assert [s['id'] for s in evaluated] == [f'E5-{i}' for i in range(8)]
        assert sum(s['seconds'] for s in evaluated) == 195 * 60
        assert math.isclose(evaluated[7]['rates'][ALIASES['mobilenet']], .00135)
        assert math.isclose(evaluated[3]['parts'][2]['rates'][ALIASES['plc']], .0009)
        assert [s['offsetSeconds'] for s in evaluation['failureInjectionRunbook']['steps']] == [180, 360, 600]
        assert evaluated[3]['parts'][2]['poolShares'] == evaluation['poolShares']
        assert evaluated[3]['parts'][2]['alpha'] == dict.fromkeys(ALIASES, .9)
        for bad in (None, {}, {'MNV': .5, 'PLC': .5, 'Smol': .5}, {'MNV': -1, 'PLC': 1, 'Smol': 1}, {'MNV': float('nan'), 'PLC': .5, 'Smol': .5}, {'MNV': True, 'PLC': 0, 'Smol': 0}):
            try:
                schedule(dict(evaluation, poolShares=bad), e5)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid poolShares accepted')
        custom = schedule(dict(evaluation, poolShares={'MNV': .5, 'PLC': .25, 'Smol': .25}), e5)
        assert math.isclose(custom[1]['rates'][ALIASES['mobilenet']], .0015)
        gateway = json.loads((HERE / 'g-check-config.example.json').read_text())
        gateway['capacityR'] = dict.fromkeys(ALIASES, .01)
        g_stages = schedule(gateway, e5)
        assert len(g_stages) == 1 and g_stages[0]['id'] == 'G-check' and g_stages[0]['seconds'] == 120
        assert len(g_stages[0]['parts']) == 1
        assert g_stages[0]['parts'][0]['alpha'] == dict.fromkeys(ALIASES, 1.2)
        assert math.isclose(g_stages[0]['rates'][ALIASES['plc']], .0012)
        assert e5.pool_shares({}) == {'MNV': .45, 'PLC': .1, 'Smol': .45}
        with patch.object(e5.sys, 'stderr') as warning:
            e5.gateway_budget({}, .01)
            assert 'G unconfirmed' in ''.join(call.args[0] for call in warning.write.call_args_list)
        e5.gateway_budget({'G': .015, 'GSource': 'fixture'}, .01)
        for invalid in ({'G': .014, 'GSource': 'fixture'}, {'G': .015}, {'G': -1, 'GSource': 'fixture'}):
            try:
                e5.gateway_budget(invalid, .01)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid gateway budget accepted')
        assert gateway['measurementSLO']['plc'] == {'percentile': 95, 'targetLatencyNanos': 32000000}
        for bad in (None, {}, dict.fromkeys(ALIASES, -1), dict.fromkeys(ALIASES, float('nan'))):
            invalid = dict(evaluation, capacityR=bad)
            try:
                schedule(invalid, e5)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid E5 capacityR accepted')
        tolerant = schedule(dict(config, tolerateArchive=['power', 'power-drain-inclusive']), e5)
        assert all(s['tolerateArchive'] == ['power', 'power-drain-inclusive'] for s in tolerant)
        override = copy.deepcopy(config)
        override['tolerateArchive'] = ['power']
        override['stages'][1]['tolerateArchive'] = []
        assert schedule(override, e5)[1]['tolerateArchive'] == []
        for invalid in ('power', ['requests'], [None]):
            try:
                schedule(dict(config, tolerateArchive=invalid), e5)
            except ValueError:
                pass
            else:
                raise AssertionError('Invalid tolerateArchive accepted')
        strict_config = {'approval': {}}
        e5.require_strict_archives(strict_config)
        for value in ([], ['power'], None, False):
            for forbidden in ({'tolerateArchive': value}, {'smoke': {'tolerateArchive': value}}, {'stages': [{'tolerateArchive': value}]}):
                config_path = root / 'forbidden-e5.json'
                config_path.write_text(json.dumps(dict(strict_config, **forbidden)))
                binding_path = root / 'forbidden-binding.json'
                binding_path.write_text(json.dumps({'schema': 'e5-binding.v1', 'config': forbidden}))
                for check, path in ((e5.bind, config_path), (e5.load_binding, binding_path)):
                    try:
                        check(path)
                    except ValueError as exc:
                        assert str(exc) == 'tolerateArchive is diagnostic-only in demo.py; forbidden in normative E5 configuration'
                    else:
                        raise AssertionError('Normative E5 accepted tolerateArchive')
        binding_path.write_text(json.dumps({'schema': 'demo-e5-binding.v1', 'config': {'smoke': {'tolerateArchive': ['power']}}}))
        assert e5.load_binding(binding_path)['config']['smoke']['tolerateArchive'] == ['power']
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
        cpo.c = {'workloads': workloads, 'smoke': stages[0], 'generatorQuality': {'latenessToleranceNS': 50_000_000, 'maxExceedingFraction': 0}}
        cpo.b = {'schema': 'demo-e5-binding.v1', 'config': cpo.c, 'schedule': stages, 'sources': {}, 'payload': {w['name']: [] for w in workloads}}
        cpo.tb = dict.fromkeys(('setupSeconds', 'barrierSeconds', 'drainSeconds', 'apiSeconds', 'settleSeconds', 'collectSeconds'), 3)
        cpo.tb['flushSeconds'] = .001
        fixture_token = root / 'fixture-token'
        fixture_token.write_text('e30.' + e5.base64.urlsafe_b64encode(json.dumps({'exp': e5.time.time() + 86400}).encode()).decode().rstrip('=') + '.signature')
        cpo.c.update(tokenFile=str(fixture_token), timeboxes=cpo.tb)
        cpo.ws = {w['name']: w for w in workloads}
        cpo.uids = [w['uid'] for w in workloads]
        cpo.d = d
        settings = {'autonomousWorkloads': [], 'staticRouting': {}, 'excludedNodes': [], 'minimumDemand': dict.fromkeys(cpo.uids, .3), 'scaleDownStabilizationNanos': 120000000000, 'transitionReserveFraction': .25, 'comparisonPeriodNanos': 600000000000}
        cpo.b['schedulerSettings'] = copy.deepcopy(settings)
        cpo.b['schedulerSettings']['powerObservation'] = copy.deepcopy(fixture_power)
        # A deleted/re-registered definition gets a new UID; old floor keys die.
        floors = dict(zip(ALIASES.values(), (20, .3, 40)))
        floor_workloads = [{'name': name, 'uid': 'new-' + name} for name in floors]
        floor_settings = {'minimumDemand': {'old-' + name: value for name, value in floors.items()}}
        floor_calls = []
        corrupt_readback = [False]
        stale_reads = [0]
        def floor_api(method, path, body=None):
            floor_calls.append(method)
            if path.startswith('/api/v1/workloads/'):
                workload = next(w for w in floor_workloads if w['name'] == path.rsplit('/', 1)[-1])
                return {'desired': {'metadata': {'uid': workload['uid']}}}, 200
            if method == 'PUT':
                floor_settings.update(copy.deepcopy(body))
                return {}, 200
            result = {'state': {'snapshot': {'workloads': copy.deepcopy(floor_workloads)}}, 'settings': copy.deepcopy(floor_settings)}
            if stale_reads[0]:
                stale_reads[0] -= 1
                for w in result['state']['snapshot']['workloads']:
                    w['uid'] = 'old-' + w['name']
            if corrupt_readback[0] and floor_calls[-2:] == ['PUT', 'GET']:
                result['settings']['minimumDemand'] = {}
            return result, 200
        floor_readback = bind_minimum_demand(floor_api, {'minimumDemand': floors})
        assert floor_readback['settings']['minimumDemand'] == {'new-' + name: value for name, value in floors.items()}
        assert floor_calls == ['GET'] * 4 + ['PUT', 'GET']
        floor_workloads[0]['uid'] = 'reregistered'
        assert bind_minimum_demand(floor_api, {'minimumDemand': floors})['settings']['minimumDemand'] == {'reregistered': 20, 'new-smolvlm2-vqa': .3, 'new-plc-anomaly': 40}
        corrupt_readback[0] = True
        try:
            bind_minimum_demand(floor_api, {'minimumDemand': floors})
        except RuntimeError as error:
            assert 'readback/identity mismatch' in str(error)
        else:
            raise AssertionError('Incorrect minimumDemand readback accepted')
        corrupt_readback[0] = False
        floor_calls.clear()
        stale_reads[0] = 1
        with patch.object(time, 'sleep') as sleep:
            floor_readback = bind_minimum_demand(floor_api, {'minimumDemand': floors})
        sleep.assert_called_once_with(2)
        assert floor_calls == ['GET'] * 5 + ['PUT', 'GET']
        assert floor_readback['settings']['minimumDemand']['reregistered'] == 20
        floor_calls.clear()
        stale_reads[0] = 1000
        clock, sleeps = [0], []
        def floor_sleep(seconds):
            sleeps.append(seconds)
            clock[0] += seconds
        with patch.object(time, 'monotonic', side_effect=lambda: clock[0]), patch.object(time, 'sleep', side_effect=floor_sleep):
            try:
                bind_minimum_demand(floor_api, {'minimumDemand': floors})
            except RuntimeError as error:
                assert 'snapshot UID mismatch after 60s; no settings PUT' in str(error)
                assert 'reregistered' in str(error)
            else:
                raise AssertionError('Permanently stale canonical UID accepted')
        assert sleeps == [2] * 30 and floor_calls == ['GET'] * 34
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
                self.emitter = None
                reader, self.writer = os.pipe()
                self.stdout = os.fdopen(reader, 'rb')
                trace = next(s for s in stages if s['id'] == cpo.a.stage)['traces'][name]
                self.emit({'e5Event': 'ready', 'at': e5.now(), 'payload': [], 'offered': trace['offered'], 'arrivalSHA256': trace['arrivalSHA256'], 'barrierSeconds': 3})
            def emit(self, row):
                try:
                    os.write(self.writer, (json.dumps(row) + '\n').encode())
                except OSError:
                    if not self.done:
                        raise
            def poll(self):
                return 0 if self.done else None
            def terminate(self):
                if not self.done:
                    self.done = True
                    os.close(self.writer)
            def wait(self, timeout=None):
                self.terminate()
                if self.emitter is not None:
                    self.emitter.join(timeout=timeout or 3)
                    assert not self.emitter.is_alive(), 'Mock producer emitter failed to stop'
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
                    stage = next(s for s in stages if s['id'] == cpo.a.stage)
                    def emit_offered():
                        process.emit({'e5Event': 'start', 'at': e5.now(), 'monotonicNS': 1000000000})
                        for index, offset in enumerate(stage['traces'][name]['offsetsNS']):
                            process.emit({'requestID': name + '-' + str(index), 'requestIndex': index, 'scheduledOffsetNs': offset, 'scheduledMonotonicNS': 1000000000 + offset, 'sentMonotonicNS': 1000000000 + offset, 'finishedMonotonicNS': 1000000000 + offset + 1000, 'sentAtUnixNS': 2000000000 + offset, 'finishedAtUnixNS': 2000000000 + offset + 1000, 'status': 200 if index else 503, 'latencyMs': .001, 'scheduledLatencyMs': .001, 'serviceLatencyMs': .001, 'latenessNs': 0})
                        process.emit({'e5Event': 'window-end', 'at': e5.now()})
                        process.emit({'e5Event': 'drained', 'at': e5.now()})
                    process.emitter = e5.threading.Thread(target=emit_offered, daemon=True)
                    process.emitter.start()
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
                def requests(folder, uid, start, end, actual_response_ids=None):
                    workload = next(w for w in workloads if w['uid'] == uid)
                    path = folder / (workload['name'] + '-client.jsonl')
                    clients = [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
                    return {'finals': {r['requestID']: {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 1000} for r in clients if r.get('requestID') and r.get('status') == 200}, 'receivedAtNS': {r['requestID']: r['sentAtUnixNS'] - 100 for r in clients if r.get('requestID')}, 'errors': []}
                return SimpleNamespace(scheduler_events=events, index_requests=requests)
            return original_module(name, path)
        e5.module = modules
        (root / 'control-alerts.jsonl').write_text('')
        with patch.object(e5.subprocess, 'Popen', popen), patch.object(d, 'client_end_counter', lambda *args: {'fixtureCounter': True}):
            with patch.object(e5, 'power_observation', return_value=dict(fixture_power, coverage=dict(fixture_power['coverage'], MaximumGap=2000000000))):
                for operation in (cpo.start, cpo.run):
                    try:
                        operation()
                    except RuntimeError as error:
                        assert 'power observation differs' in str(error)
                    else:
                        raise AssertionError('Start/run accepted changed operating power policy')
                assert not stage_folder(cpo.out, stages[0]).exists()
            with patch.object(cpo, 'settings', return_value=dict(settings, minimumDemand={'old-uid': .3})):
                try:
                    cpo.start()
                except RuntimeError as error:
                    assert 'minimumDemand differs' in str(error)
                else:
                    raise AssertionError('Start accepted stale UID floors')
            cpo.start()
            snapshot['reservations'] = [{'owner': 'fixture-transition'}]
            scheduler_actions.append({'id': 'fixture-action', 'status': 'running'})
            with patch.object(cpo, 'idle', side_effect=AssertionError('Demo stage must not await planner idle')):
                for stage in stages:
                    run_stage(cpo, stage)
            summaries = summarize(e5, cpo.b, cpo.out)
        assert len(summaries) == 4
        for summary in summaries:
            for metric in summary['workloads'].values():
                fidelity = metric['issuanceFidelity']
                assert fidelity['status'] == ('OBSERVED' if metric['offered'] else 'NOT_OFFERED')
                assert fidelity['issued'] == metric['clientRows']
                assert fidelity['supplied'] == bool(metric['offered'])
                if metric['offered']:
                    assert fidelity['gapsOver50MS'] == fidelity['sendsLateOver50MS'] == 0
        with patch.object(e5, 'publish_experiment', return_value={'outcome': 'fixture'}) as publication:
            assert publish(e5, cpo.b, cpo.out, 'fixture-fidelity', root / 'store.json')['outcome'] == 'fixture'
        published_stages = publication.call_args.args[3]
        for name, metric in summaries[-1]['workloads'].items():
            published = published_stages[-1]['kpis']['workloads'][name]
            assert published['issuanceFidelity'] == metric['issuanceFidelity']
            assert published['missingReasons']['issuanceFidelity'] is None
        assert '## Issuance fidelity' in (cpo.out / 'summary.md').read_text()
        client_path = stage_folder(cpo.out, stages[1]) / (workloads[0]['name'] + '-client.jsonl')
        original_client = client_path.read_text()
        records = [json.loads(line) for line in original_client.splitlines()]
        next(row for row in records if 'scheduledOffsetNs' in row).pop('sentMonotonicNS')
        client_path.write_text('\n'.join(json.dumps(row) for row in records) + '\n')
        try:
            unknown = summarize(e5, cpo.b, cpo.out)[1]['workloads'][workloads[0]['name']]
            assert unknown['issuanceFidelity']['status'] == 'UNKNOWN' and unknown['transitionFailures'] is None
        finally:
            client_path.write_text(original_client)
        for variant in ('late-completion', 'late-send', 'unsent', 'missing-client', 'missing-finish', 'historical'):
            records = [json.loads(line) for line in original_client.splitlines()]
            row = next(row for row in records if row.get('status') == 200)
            if variant == 'late-completion':
                row['finishedMonotonicNS'] = row['scheduledMonotonicNS'] + 100_000_000_000
            elif variant == 'late-send':
                row['sentMonotonicNS'] += 60_000_000
                row['finishedMonotonicNS'] += 60_000_000
            elif variant == 'unsent':
                row.update(error='UnsentAtWindowEnd')
                row.pop('sentMonotonicNS')
            elif variant == 'missing-client':
                records.remove(row)
            elif variant == 'missing-finish':
                row.pop('finishedMonotonicNS')
            else:
                row.pop('scheduledMonotonicNS')
            client_path.write_text('\n'.join(json.dumps(row) for row in records) + '\n')
            try:
                metric = summarize(e5, cpo.b, cpo.out)[1]['workloads'][workloads[0]['name']]
                baseline = summaries[1]['workloads'][workloads[0]['name']]
                assert metric['offered'] == metric['withinSLODenominator'] == baseline['offered']
                if variant == 'late-completion':
                    assert metric['scheduledLatencyMS']['max'] == 100_000
                    assert metric['latencyMS'] == baseline['latencyMS'] and metric['withinSLONumerator'] == baseline['withinSLONumerator']
                elif variant == 'late-send':
                    assert metric['SLOVerdict'] == 'INVALID_GENERATOR' and metric['issuanceFidelity']['generatorQuality']['status'] == 'INVALID_GENERATOR'
                    assert metric['latencyMS'] == baseline['latencyMS'] and metric['withinSLONumerator'] == baseline['withinSLONumerator']
                elif variant in ('unsent', 'missing-client'):
                    assert not metric['issuanceFidelity']['supplied'] and metric['httpSuccess'] == baseline['httpSuccess'] - 1
                elif variant == 'missing-finish':
                    assert metric['missingSLOLatencyRows'] == 0 and metric['latencyMS'] == baseline['latencyMS']
                else:
                    assert metric['latencyMS'] == baseline['latencyMS']
            finally:
                client_path.write_text(original_client)
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
        # Real collection gate: only explicit power gaps advance, never raw/request failures.
        archive_refs = root / 'archive-refs.json'
        e5.save(archive_refs, [])
        for label, allowed, errors, raw_complete, succeeds in (
                ('strict', [], ['power: timeout'], True, False),
                ('partial', ['power'], ['power: timeout', 'power-drain-inclusive: timeout'], True, False),
                ('power', ['power', 'power-drain-inclusive'], ['power: timeout', 'power-drain-inclusive: timeout'], True, True),
                ('other', ['power'], ['requests: timeout'], True, False),
                ('raw', ['power'], ['power: timeout'], False, False)):
            probe_out = root / ('archives-' + label)
            folder = stage_folder(probe_out, stages[1])
            folder.mkdir(parents=True)
            original = e5.load(stage_folder(cpo.out, stages[1]) / 'phase.json')
            original.update(status='STOP', error='Required exact-hour archive/current capture incomplete; preserve private index and stop')
            e5.save(folder / 'phase.json', original)
            for name in stages[1]['rates']:
                e5.save(folder / ('terminal-' + name + '.json'), {'status': {'conditions': [{'type': 'Complete', 'status': 'True'}]}})
            captured = {'status': 'NOT_READY', 'errors': errors}
            e5.save(folder / 'archive-collection.json', captured)
            e5.save(folder / 'raw-source-index.json', {'complete': raw_complete})
            stage = dict(stages[1], tolerateArchive=allowed)
            probe = SimpleNamespace(out=probe_out, c={'smoke': stage, 'collection': {'archiveRefs': str(archive_refs), 'serverContainer': 'server'}}, b={'schema': 'demo-e5-binding.v1', 'sources': {}, 'schedule': [stage, stages[2]]}, tb={'collectSeconds': 1, 'apiSeconds': 1}, d=SimpleNamespace(K=['fixture'], load=e5.load), io=SimpleNamespace(k=lambda *args: {'items': [{'metadata': {'name': 'server'}}]}))
            probe.collect = lambda folder, phase=None: e5.CPO.collect(probe, folder, phase)
            with patch.object(e5, 'module', return_value=SimpleNamespace(collect=lambda *args: captured)), patch.object(e5.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout='logs')):
                try:
                    if allowed:
                        collect_stage(e5, probe, stage)
                    else:
                        probe.collect(folder)
                except RuntimeError:
                    assert not succeeds
                else:
                    assert succeeds
            assert e5.load(folder / 'phase.json') == original
            assert (folder / 'collected.json').exists() == succeeds
            if succeeds:
                missing = e5.load(folder / 'collected.json')['missingArchives']
                assert set(missing) == set(allowed)
                assert e5.load(phase_path(folder))['status'] == 'DRAINED_NOT_EVALUATED'
                (probe_out / 'start-ack.json').write_text('{}')
                probe.static_placement = lambda: True
                probe.a = SimpleNamespace()
                probe.run = lambda: None
                run_stage(probe, stages[2])
                e5.save(stage_folder(cpo.out, stages[1]) / 'collected.json', {'missingArchives': missing})
                summary = summarize(e5, cpo.b, cpo.out)[1]
                assert summary['energyJ'] is None and summary['drainInclusiveEnergyJ'] is None
                assert summary['missingArchives'] == missing
                original['error'] = 'Producer log stream failed'
                e5.save(folder / 'phase.json', original)
                (folder / 'collected.json').unlink()
                try:
                    collect_stage(e5, probe, stage)
                except RuntimeError as error:
                    assert 'Only an otherwise completed' in str(error)
                else:
                    raise AssertionError('Request failure tolerated')
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
        static_state['snapshot']['actions'] = []
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
        # Use one snapshot: live completion cannot release stale snapshot state.
        for clears in (True, False):
            barrier_stage = schedule({'stages': [dict(config['stages'][0], seconds=.001, startBarrier={'reservations': 0, 'transitions': 0})]}, e5)[0]
            barrier_out = root / ('barrier-clear' if clears else 'barrier-timeout')
            barrier_out.mkdir()
            (barrier_out / 'start-ack.json').write_bytes((cpo.out / 'start-ack.json').read_bytes())
            removed_barrier = []
            if not clears:
                e5.save(barrier_out / 'prior/created-producer.json', {'metadata': {'name': 'prior-producer', 'uid': 'prior-uid'}})
                e5.save(barrier_out / 'prior/phase.json', {'status': 'DRAINED_NOT_EVALUATED'})
            busy = copy.deepcopy(static_state)
            busy['snapshot']['at'] = '2026-10-26T00:00:00Z'
            busy['snapshot']['reservations'] = [{'owner': 'reserved-plan'}]
            busy['snapshot']['actions'] = [{'status': 'cancel'}]
            busy['actions'] = [{'status': 'complete'}]
            busy['results'] = [{'status': 'complete', 'endedAt': '2026-10-26T00:00:01Z'}]
            waiting = cpo.start_barrier_observation(busy, e5.time.monotonic() + 3, e5.now())
            assert waiting['snapshotAt'] == busy['snapshot']['at'] and waiting['reservationOwners'] == ['reserved-plan']
            assert waiting['transitions'] == 1 and not waiting['observedUTC']
            observations = []
            def barrier_state():
                observations.append(True)
                state = copy.deepcopy(busy)
                if clears and len(observations) >= 2:
                    state['snapshot']['reservations'] = []
                if clears and len(observations) >= 3:
                    state['snapshot']['actions'] = []
                    stale = cpo.start_barrier_observation(state, e5.time.monotonic() + 3, e5.now())
                    assert stale['reservations'] == stale['transitions'] == 0 and not stale['snapshotFresh'] and not stale['observedUTC']
                if clears and len(observations) >= 4:
                    state['snapshot']['at'] = '2026-10-26T00:00:02Z'
                return state
            with patch.object(cpo, 'out', barrier_out), patch.object(cpo, 'state', barrier_state), patch.dict(cpo.b, {'schedule': [barrier_stage]}):
                if clears:
                    run_stage(cpo, barrier_stage)
                    assert len(observations) >= 4
                    assert cpo.native_evidence == busy['results']
                else:
                    with patch.object(cpo, 'sampler', side_effect=AssertionError('Timed out barrier must not start collection')), patch.object(cpo.io, 'remove_job', lambda name, uid: removed_barrier.append((name, uid)), create=True), patch.object(cpo.io, 'k', side_effect=lambda argv, **kwargs: {'items': []} if argv == ['get', 'jobs', '-o', 'json'] else {'metadata': {'uid': 'prior-uid'}} if argv == ['get', 'job', 'prior-producer', '-o', 'json'] else no_mutation()):
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
                assert barrier_phase['startBarrier']['maximumSeconds'] == cpo.tb['barrierSeconds']
                assert bool(barrier_phase['startBarrier']['observedUTC']) is clears
                if not clears:
                    assert barrier_phase['status'] == 'STOP' and barrier_phase['startUTC'] is None
                    assert barrier_phase['startBarrier']['reservations'] == 1 and barrier_phase['startBarrier']['transitions'] == 1
                    assert removed_barrier == [('prior-producer', 'prior-uid')]
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
        assert e5.load(cpo.out / 'smoke-E0/pre-state.json')['results'] is None
        # Reservations can reappear during setup: GO must wait, not just stage entry.
        for go_clears in (True, False):
            go_out = root / ('go-clear' if go_clears else 'go-timeout')
            go_out.mkdir()
            (go_out / 'start-ack.json').write_bytes((cpo.out / 'start-ack.json').read_bytes())
            go_stage = copy.deepcopy(static_stages[0])
            go_stage['seconds'] = 1  # Keep stage-entry capture independent of filesystem latency.
            go_folder = stage_folder(go_out, go_stage)
            plc_name = ALIASES['plc']
            go_observations = []
            def go_state():
                state = copy.deepcopy(static_state)
                if (go_folder / ('startup-' + plc_name) / 'producer-ready-event.json').exists():
                    go_observations.append(True)
                    state['snapshot']['reservations'] = [{'id': 'new-during-setup'}] if len(go_observations) == 1 else []
                    state['snapshot']['actions'] = [{'status': 'running'}] if not go_clears or len(go_observations) < 3 else []
                    state['actions'] = []  # Live actions must not override the snapshot.
                return state
            original_stages = stages
            stages = [go_stage]
            before_go_calls = len(calls)
            removed_go = []
            real_producer_deadline = d.producer_barrier_deadline
            try:
                with patch.object(cpo, 'out', go_out), patch.object(cpo, 'state', go_state), patch.object(cpo, 'settings', lambda: copy.deepcopy(static_settings)), patch.object(cpo, 'put', no_mutation), patch.dict(cpo.b, {'schedule': stages, 'demoConfig': static_config}), patch.object(cpo, 'boundary', lambda folder, prefix: e5.save(folder / (prefix + '-state.json'), cpo.state())), patch.object(e5.subprocess, 'Popen', popen), patch.object(d, 'client_end_counter', lambda *args: {'fixtureCounter': True}), patch.object(d, 'producer_barrier_deadline', side_effect=lambda row, seconds: real_producer_deadline(row, seconds) if go_clears else e5.time.time() - 1), patch.object(cpo.io, 'remove_job', lambda name, uid: removed_go.append((name, uid)), create=True), patch.object(e5.subprocess, 'run', return_value=SimpleNamespace(stdout='fixture stopped producer', stderr='', returncode=0)):
                    if go_clears:
                        run_stage(cpo, go_stage)
                    else:
                        try:
                            run_stage(cpo, go_stage)
                        except RuntimeError as error:
                            assert 'Producer release start barrier timebox' in str(error)
                        else:
                            raise AssertionError('Expired GO barrier released a producer')
                    go_phase = e5.load(go_folder / 'phase.json')
                    release = go_phase['startBarrier']['producerRelease'][plc_name]
                    assert release['timedOut'] is not go_clears
                    assert bool(release['observedUTC']) is go_clears
                    assert (go_folder / ('go-barrier-' + plc_name + '-state.json')).exists()
                    assert sum(argv[0] == 'exec' and '.touch()' in argv[-1] for argv in calls[before_go_calls:]) == int(go_clears)
                    if go_clears:
                        assert len(go_observations) >= 3 and go_phase['status'] == 'DRAINED_NOT_EVALUATED'
                    else:
                        assert go_phase['status'] == 'STOP' and go_phase['startUTC'] is None
                        assert removed_go and release['transitions'] == 1
            finally:
                stages = original_stages
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
            if '-fail-' in job:
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
        assert len(captured_before_delete) == 12  # Current failure and earlier owned stages.
        removed = []
        cpo.io.remove_job = lambda name, uid: removed.append((name, uid))
        cpo.cleanup_jobs()
        assert removed == []  # STOP acknowledgments prevent automatic deletion retries.
        # Name collisions include terminal Jobs; no create or foreign deletion.
        settings['autonomousWorkloads'] = list(cpo.uids)
        collision_stage = dict(failed_stage, id='COLLISION')
        collision_name = cpo.producer_job_name(workloads[0]['name'], collision_stage)
        collision_job = {'metadata': {'name': collision_name, 'uid': 'foreign'}, 'status': {'conditions': [{'type': 'Complete', 'status': 'True'}]}}
        cpo.c['smoke'] = collision_stage
        with patch.object(cpo.io, 'k', return_value={'items': [collision_job]}) as collision_k, patch.object(cpo.io, 'remove_job', side_effect=AssertionError('Collision Job deletion')):
            try:
                cpo.run()
            except RuntimeError as error:
                assert collision_name in str(error) and 'already exists' in str(error)
            else:
                raise AssertionError('Terminal same-name Job accepted')
            assert collision_k.call_count == 1
        # Failure evidence lookup is secondary: exact-owned deletion still runs.
        capture_stage = dict(failed_stage, id='CAPTURE-FAIL')
        cpo.c['smoke'] = capture_stage
        capture_removed = []
        def capture_k(argv, **kwargs):
            if argv[:2] == ['get', 'job']:
                raise RuntimeError('fixture evidence lookup failure')
            return kubectl(argv, **kwargs)
        with patch.object(cpo.io, 'k', capture_k), patch.object(cpo.io, 'remove_job', lambda name, uid: capture_removed.append((name, uid))), patch.object(cpo.io, 'wait_load_container', side_effect=RuntimeError('fixture startup terminated')):
            try:
                cpo.run()
            except RuntimeError as error:
                assert str(error) == 'fixture startup terminated'
            else:
                raise AssertionError('Producer failure was swallowed')
        capture_phase = e5.load(cpo.out / 'smoke-CAPTURE-FAIL/phase.json')
        assert len(capture_removed) == 3 and all(row['stopped'] for row in capture_phase['ownedProducerStops'])
        assert len(capture_phase['cleanupErrors']) == 3
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
        source_spec = {'operationalRecipe': str(recipe), 'controlSourceSHA': 'HEAD', 'timeboxes': cpo.tb, 'measurementSLO': {alias: copy.deepcopy(workloads[0]['slo']) for alias in ALIASES}}
        server = {'metadata': {'namespace': 'chill-system', 'labels': {'app.kubernetes.io/component': 'serving-entrypoint'}}, 'spec': {'containers': [{'name': 'serving-entrypoint', 'args': ['--request-timeout=70s']}]}, 'status': {'containerStatuses': [{'name': 'serving-entrypoint', 'ready': True, 'imageID': 'fixture@sha256:fixture'}]}}
        registration_snapshot = copy.deepcopy(snapshot)
        registration_snapshot['workloads'] = [copy.deepcopy(workloads[0])]
        base_spec = {key: {'name': 'fixture-' + kind} for key, kind in [('modelRef', 'model'), ('preprocessorRef', 'preprocessor'), ('datasetRef', 'dataset')]}
        base_spec['slo'] = {'percentile': 95, 'targetLatency': '1s'}
        registered = {workloads[0]['name']: {'desired': {'metadata': {'uid': workloads[0]['uid'], 'generation': 1, 'resourceVersion': '1'}, 'spec': base_spec}, 'appliedDefinition': workloads[0]}}
        management_calls, native_cleaned = [], [False]
        stale_snapshots = []
        def management_fixture(self, method, path, body=None, headers=None, accepted=(200,)):
            management_calls.append((method, path))
            if path == '/api/v1/scheduler':
                return {'state': {'snapshot': stale_snapshots.pop(0) if stale_snapshots else copy.deepcopy(registration_snapshot)}, 'settings': settings}, 200
            if path == '/api/v1/scheduler/state':
                return {'snapshot': copy.deepcopy(registration_snapshot)}, 200
            name = body['name'] if method == 'POST' else path.rsplit('/', 1)[-1]
            if method == 'POST':
                assert name not in registered
                slo = body['spec']['slo']
                definition = {'name': name, 'uid': 'registered-' + name + '-' + str(len(management_calls)), 'generation': 1, 'slo': {'percentile': slo['percentile'], 'targetLatencyNanos': int(slo['targetLatency'][:-2])}}
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
            assert assembled['canonicalDefinitionsReused'] == {name: name == workloads[0]['name'] for name in ALIASES.values()}
            before_reuse = len(management_calls)
            reused = e5.load(assemble(e5, SimpleNamespace(out=root / 'reuse-assembly'), source_spec))
            assert all(reused['canonicalDefinitionsReused'].values())
            assert [w['uid'] for w in reused['workloads']] == [w['uid'] for w in assembled['workloads']]
            assert all(method == 'GET' for method, _ in management_calls[before_reuse:])
            native_cleaned[0] = True
            for field in ('modelRef', 'slo', 'requestLifetime'):
                name = workloads[0]['name']
                before_uid = registered[name]['desired']['metadata']['uid']
                if field == 'slo':
                    registered[name]['appliedDefinition']['slo'] = {'percentile': 99, 'targetLatencyNanos': 2}
                    registered[name]['desired']['spec']['slo'] = {'percentile': 99, 'targetLatency': '2ns'}
                else:
                    registered[name]['desired']['spec'][field] = {'name': 'changed'} if field == 'modelRef' else '2s'
                before_replace = len(management_calls)
                stale_snapshots.append(copy.deepcopy(registration_snapshot))
                replaced = e5.load(assemble(e5, SimpleNamespace(out=root / ('replace-' + field)), source_spec))
                assert replaced['canonicalDefinitionsReused'] == {key: key != name for key in ALIASES.values()}
                assert next(w['uid'] for w in replaced['workloads'] if w['name'] == name) != before_uid
                assert [method for method, _ in management_calls[before_replace:] if method != 'GET'] == ['DELETE', 'POST']
            before_static_capture = len(management_calls)
            static_assembled = e5.load(assemble(e5, SimpleNamespace(out=root / 'static-assembly'), dict(source_spec, staticPlacement=True)))
            assert len(static_assembled['workloads']) == 3
            assert all(method == 'GET' for method, _ in management_calls[before_static_capture:])
            cpo.c = replaced
            cpo.uids = [w['uid'] for w in replaced['workloads']]
            cpo.io.http = None
            before_retained = len(management_calls)
            finish_definitions(e5, cpo)
            assert len(management_calls) == before_retained and len(registered) == 3
            try:
                finish_definitions(e5, cpo, delete_definitions=True)
            except RuntimeError as error:
                assert 'cleanup must complete' in str(error)
            else:
                raise AssertionError('Definition deletion before native cleanup was allowed')
            e5.save(cpo.out / 'finish-complete.json', {'fixture': 'owned native cleanup completed'})
            native_cleaned[0] = True
            finish_definitions(e5, cpo)
            assert len(registered) == 3
            finish_definitions(e5, cpo, delete_definitions=True)
        assert assembled['demoSkipped'] and assembled['approval']['controlImageDigest'] == 'fixture@sha256:fixture'
        assert len(assembled['workloads']) == 3
        assert assembled['workloads'][0]['uid'] == workloads[0]['uid'] and assembled['workloads'][0]['slo'] == workloads[0]['slo']
        assert sum(method == 'POST' for method, _ in management_calls) == 5
        assert sum(method == 'DELETE' for method, _ in management_calls) == 6 and not registered
        assert all(w['workers'] == 320 and 'lifetimeSeconds' not in w for w in assembled['workloads'])
        assert (root / 'assembly/register-plc-anomaly.json').exists() and (cpo.out / 'finish-definitions-complete.json').exists()
        # Active owner keep: reservations may clear before the running action.
        for label, clears_at, reset_error in (('idle', 0, None), ('clear', 2, None), ('timeout', None, None), ('failed', None, 'result'), ('conflict', None, '409')):
            cleanup = object.__new__(e5.CPO)
            cleanup.b = {'schema': 'demo-e5-binding.v1'}
            cleanup.out = root / ('cleanup-wait-' + label)
            cleanup.out.mkdir()
            cleanup.uids, cleanup.tb = ['owned'], {'settleSeconds': 2}
            cleanup_settings = {'autonomousWorkloads': ['owned', 'foreign']}
            cleanup.settings = lambda: copy.deepcopy(cleanup_settings)
            cleanup.put = lambda value: cleanup_settings.update(copy.deepcopy(value)) or {'validatedAt': e5.now()}
            e5.save(cleanup.out / 'initial.json', {'settings': copy.deepcopy(cleanup_settings)})
            clock, reset_calls, reads = [0], [], []
            def cleanup_state():
                assert cleanup_settings['autonomousWorkloads'] == ['foreign']
                busy = clears_at is None or clock[0] < clears_at
                reads.append(clock[0])
                return {'snapshot': {'reservations': [{'owner': 'active-owner-keep'}] if busy and clock[0] == 0 else []}, 'actions': [{'status': 'running'}] if busy else [{'status': 'complete'}]}
            cleanup.state = cleanup_state
            def cleanup_reset(method, path, body, **kwargs):
                wait = e5.load(cleanup.out / 'finish-reset-wait.json')
                assert wait['observations'][-1]['elapsedSeconds'] == clock[0]
                assert not wait['timedOut'] or clock[0] == 2
                reset_calls.append(body)
                if reset_error == '409':
                    raise RuntimeError('HTTP 409 active owner keep')
                return {'status': 'failed' if reset_error else 'complete'}
            cleanup.io = SimpleNamespace(api=cleanup_reset)
            def advance(seconds):
                clock[0] += seconds
            with patch.object(e5.time, 'monotonic', lambda: clock[0]), patch.object(e5.time, 'sleep', advance):
                try:
                    cleanup.finish()
                except RuntimeError as error:
                    assert reset_error and ('reset failed' in str(error) or 'HTTP 409' in str(error))
                else:
                    assert not reset_error
            assert len(reset_calls) == 1
            wait = e5.load(cleanup.out / 'finish-reset-wait.json')
            assert wait['timedOut'] == (clears_at is None)
            assert wait['observations'][-1]['transitions'] == (1 if clears_at is None else 0)
            assert clock[0] == (clears_at if clears_at is not None else 2)
            assert len(reads) >= 3 if clears_at != 0 else len(reads) >= 1
            assert (cleanup.out / 'finish-complete.json').exists() == (not reset_error)
            if clears_at == 2:
                assert wait['observations'][1]['reservations'] == 0 and wait['observations'][1]['transitions'] == 1
            # Resume shares clear_owned; it must obey the same gate.
            if label == 'idle':
                with patch.object(e5.time, 'monotonic', lambda: 0):
                    cleanup.clear_owned('resume-fixture')
                assert e5.load(cleanup.out / 'resume-fixture-reset-wait.json')['timedOut'] is False
        reset = object.__new__(e5.CPO)
        reset.b = {'schema': 'e5-binding.v1'}
        reset.out = root / 'reset'
        reset.out.mkdir()
        reset.uids, reset.tb = ['owned'], {'settleSeconds': 194}
        reset.tb.update(powerDownSeconds=200, apiSeconds=129)
        failure = {'status': 'failed', 'error': 'native drain failed', 'steps': [{'name': 'drain', 'status': 'failed'}]}
        requests = []
        def failed_reset(method, path, body, **kwargs):
            assert kwargs['timeout'] == 523  # 194 settle + 200 power + 129 response margin.
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
        conductor.api = lambda *args, **kwargs: failure
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
        print('PASS demo: campaign lifecycle, continuous parts and per-part success/p95/batch summaries, Ready gate early exit/timeout, static placement without scheduler mutation (including failure), stage-entry and pre-GO barriers clear/timeout, optional lifetime, server reset evidence and failure stop, reset autonomy-off reservation/action wait clear/timeout and single attempt on failure/409; mocked API/kubectl only')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('command', choices=['prepare', 'preflight', 'start', 'resume', 'stage', 'run', 'collect', 'summary', 'publish', 'finish', 'report'], nargs='?')
    parser.add_argument('directory', type=Path, nargs='?', help='report: retained demo folder (offline)')
    parser.add_argument('--compare', type=Path, nargs='+', action='append', default=[], help='report: retained comparison folder(s); repeatable')
    parser.add_argument('--config', type=Path, help='Demo JSON; required for prepare')
    parser.add_argument('--judge', type=Path, help='preflight: offline retained capture rejudgment; requires fresh --out')
    parser.add_argument('--out', type=Path, help='Fresh absolute external campaign directory; binding.json is stored here')
    parser.add_argument('--release', type=Path, help='Existing CPO release: executor CampaignOps, command e5-execute, bindingPath, sourceSHA, mainInstructionUTC')
    parser.add_argument('--stage', help='Configured stage ID; required for stage/collect')
    parser.add_argument('--experiment-id', help='publish: immutable ASCII ID; corrections require a new ID')
    parser.add_argument('--store-config', type=Path, help='publish: private logicalStore/S3 config for existing PutImmutable bridge')
    parser.add_argument('--delete-definitions', action='store_true', help='finish/preflight: delete captured canonical definitions after owned cleanup (default: retain UID/capacity rows)')
    parser.add_argument('--self-check', action='store_true', help='Run one local mocked API/kubectl lifecycle smoke, without cluster access')
    args = parser.parse_args()
    if args.command == 'report':
        timeline = module('demo_timeline', HERE / 'timeline.py')
        if args.self_check:
            timeline.self_check()
            return
        if args.directory is None:
            parser.error('report requires a demo directory')
        timeline.report(args.directory, [p for group in args.compare for p in group], args.out)
        return
    if args.directory is not None or args.compare:
        parser.error('directory and --compare are only valid for report')
    if args.self_check:
        self_check()
        return
    if args.judge is not None:
        if args.command != 'preflight' or args.out is None:
            parser.error('--judge requires preflight and a fresh --out')
        source = args.judge.resolve()
        destination = args.out.resolve()
        if destination == source or source in destination.parents or destination in source.parents:
            parser.error('--out must be separate from the retained capture')
        from types import SimpleNamespace
        import datetime as dt
        offline = SimpleNamespace(module=module, dt=dt,
                                  load=lambda path: json.loads(Path(path).read_text()),
                                  save=lambda path, value: Path(path).write_text(json.dumps(value, indent=2) + '\n'))
        binding = offline.load(source / 'binding.json')
        if binding.get('schema') != 'demo-e5-binding.v1' or not binding.get('demoConfig', {}).get('preflight'):
            parser.error('--judge requires a retained preflight binding')
        destination.mkdir(parents=True, exist_ok=False)
        summarize(offline, binding, source, destination, judge=True)
        result = offline.load(destination / 'summary.json')['preflight']
        print(json.dumps(result, indent=2))
        if result['status'] != 'PASS':
            raise SystemExit(2)
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
    if args.command == 'preflight':
        if args.config is None or args.release is None:
            parser.error('preflight requires --config and --release')
        if e5.load(args.config).get('preflight') is not True:
            parser.error('preflight requires the standard preflight config (preflight: true)')
        prepare(e5, args)
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
    if args.command == 'preflight':
        try:
            cpo.start()
            for stage in binding['schedule']:
                run_stage(cpo, stage)
            cpo.finish()
            cpo.cleanup_jobs()
            finish_definitions(e5, cpo, delete_definitions=args.delete_definitions)
            wait_preflight_empty(e5, cpo)
        except Exception as error:
            e5.save(args.out / 'preflight-STOP.json', {'at': e5.now(), 'error': str(error), 'status': 'FAIL'})
            raise
        finally:
            summarize(e5, binding, args.out)
            if any(args.out.glob('smoke-*/phase.json')):
                module('demo_timeline', HERE / 'timeline.py').report(args.out, [], None)
        result = e5.load(args.out / 'summary.json')['preflight']
        print('Preflight: ' + result['status'])
        if result['status'] != 'PASS':
            raise SystemExit(2)
        return
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
        finish_definitions(e5, cpo, delete_definitions=args.delete_definitions)


if __name__ == '__main__':
    main()
