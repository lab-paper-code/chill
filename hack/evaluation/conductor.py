#!/usr/bin/env python3
"""Black-box evaluation: absolute load, public feature controls and owned Jobs only."""
import argparse
import base64
from collections import Counter
import copy
import datetime as dt
import gzip
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import selectors
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zlib

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
SETTINGS = '/api/v1/scheduler/settings'
# GET projects these read-only fields beside the writable SchedulerSettings.
SETTINGS_PROJECTIONS = {'staticRouting', 'staticRoutingValidatedAt', 'powerObservation', 'meters', 'requestTimeout'}


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def load(path):
    return json.loads(Path(path).read_text(), parse_constant=lambda value: (_ for _ in ()).throw(ValueError('Nonfinite JSON: ' + value)))


def save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + '.tmp')
    temporary.write_text(json.dumps(value, indent=2, allow_nan=False) + '\n')
    temporary.replace(path)


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def ref(path):
    path = Path(path)
    return {'path': str(path), 'bytes': path.stat().st_size, 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}


class Client:
    def __init__(self, config):
        self.config = config
        origin = urllib.parse.urlsplit(config['endpoint'])
        if origin.scheme != 'https' or not origin.hostname or origin.username or origin.password or origin.path not in ('', '/') or origin.query or origin.fragment:
            raise ValueError('FrozenInputInvalid: API requires an HTTPS origin')
        auth = module('evaluation_auth', HERE / 'auth.py')
        self.http = urllib.request.build_opener(auth.NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=config['caFile'])))
        self.collector_token_file = config['collectorTokenFile']

    def api(self, method, path, body=None, timeout=None, *, headers=None, response=None):
        token = self.collector_token_file if method == 'GET' else self.config['tokenFile']
        request_headers = {'Authorization': 'Bearer ' + Path(token).read_text().strip(), **(headers or {})}
        if body is not None:
            request_headers['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.config['endpoint'].rstrip('/') + path, headers=request_headers,
                                         data=None if body is None else json.dumps(body, allow_nan=False).encode(), method=method)
        metadata = response if response is not None else {}
        try:
            with self.http.open(request, timeout=timeout if timeout is not None else self.config.get('timeboxes', {}).get('apiSeconds', 30)) as observed:
                metadata.update(statusCode=observed.status, etag=observed.headers.get('ETag'))
                value = json.load(observed)
                metadata['body'] = value
                return value
        except urllib.error.HTTPError as error:
            metadata.update(statusCode=error.code, etag=error.headers.get('ETag'))
            data = error.read(1048577)
            if len(data) > 1048576:
                metadata['body'] = {'error': 'HTTP error body exceeded 1MiB bound'}
            else:
                try:
                    metadata['body'] = json.loads(data)
                except ValueError:
                    metadata['body'] = data.decode('utf-8', errors='replace')
            raise

    def k(self, args, stdin=None, timeout=None):
        command = ['/usr/bin/kubectl', '-n', self.config['namespace'], *args]
        result = subprocess.run(command, input=stdin, capture_output=True, text=True,
                                timeout=timeout if timeout is not None else self.config.get('timeboxes', {}).get('apiSeconds', 60))
        if result.returncode:
            raise RuntimeError('ToolFailed: kubectl ' + str(result.returncode) + ': ' + result.stderr.strip())
        return json.loads(result.stdout) if result.stdout.strip() else {}

    def delete_job(self, name, uid):
        token = Path('/var/run/secrets/kubernetes.io/serviceaccount/token').read_text().strip()
        host = 'https://' + os.environ['KUBERNETES_SERVICE_HOST'] + ':' + os.environ.get('KUBERNETES_SERVICE_PORT_HTTPS', '443')
        body = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'propagationPolicy': 'Foreground', 'preconditions': {'uid': uid}}
        request = urllib.request.Request(host + '/apis/batch/v1/namespaces/' + self.config['namespace'] + '/jobs/' + name,
                                         data=json.dumps(body).encode(), method='DELETE',
                                         headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        auth = module('evaluation_delete_auth', HERE / 'auth.py')
        opener = urllib.request.build_opener(auth.NoRedirect(), urllib.request.HTTPSHandler(
            context=ssl.create_default_context(cafile='/var/run/secrets/kubernetes.io/serviceaccount/ca.crt')))
        with opener.open(request, timeout=self.config.get('timeboxes', {}).get('apiSeconds', 30)) as response:
            return json.load(response)


def parse_seeds(seeds, names):
    if not isinstance(seeds, dict) or set(seeds) != set(names):
        raise ValueError('FrozenInputInvalid: seed workload keys')
    result = {}
    for name, value in seeds.items():
        if not isinstance(value, str) or not value or any(c not in '0123456789' for c in value):
            raise ValueError('FrozenInputInvalid: seed requires uint64 decimal string')
        normalized = value.lstrip('0') or '0'
        if len(normalized) > 20 or (len(normalized) == 20 and normalized > '18446744073709551615'):
            raise ValueError('FrozenInputInvalid: seed requires uint64 decimal string')
        result[name] = int(normalized)
    return result


def bind(path):
    config = load(path)
    names = [w['name'] for w in config['workloads']]
    if len(set(names)) != len(names) or not names:
        raise ValueError('FrozenInputInvalid: workload names')
    seeds = parse_seeds(config['seeds'], names)
    sys.path.insert(0, str(REPO / 'load-generator/src'))
    from chill_loadgen.cli import segmented_arrivals_ns
    schedule = []
    for stage in config['stages']:
        parts = stage['parts']
        if not parts:
            raise ValueError('FrozenInputInvalid: explicit load parts required')
        traces = {}
        for name in names:
            segments = []
            for part in parts:
                seconds, rates = part['seconds'], part['rates']
                if type(seconds) not in (int, float) or not math.isfinite(seconds) or seconds <= 0 or set(rates) != set(names):
                    raise ValueError('FrozenInputInvalid: absolute load part')
                rate = rates[name]
                if type(rate) not in (int, float) or not math.isfinite(rate) or rate < 0:
                    raise ValueError('FrozenInputInvalid: finite nonnegative rate required')
                segments.append({'rate': rate, 'seconds': seconds})
            if name in config.get('traceOffsets', {}):
                base = sum(round(segment['seconds'] * 1e9) for segment in segments)
                offsets = config['traceOffsets'][name]
                if not isinstance(offsets, list) or any(type(x) is not int or x < 0 or x >= base for x in offsets) or offsets != sorted(offsets):
                    raise ValueError('FrozenInputInvalid: explicit arrival offsets')
            else:
                offsets, base = segmented_arrivals_ns(config['arrivals'][name], segments, seeds[name])
            traces[name] = {'seed': seeds[name], 'offsetsNS': offsets, 'offered': len(offsets),
                            'arrivalSHA256': hashlib.sha256(json.dumps(offsets, separators=(',', ':')).encode()).hexdigest()}
        schedule.append({'id': stage['id'], 'parts': parts, 'seconds': sum(p['seconds'] for p in parts),
                         'rates': {name: max(p['rates'][name] for p in parts) for name in names}, 'traces': traces})
    return {'schema': 'e5-binding.v4', 'failurePolicy': 'v2', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': now(), 'config': config,
            'schedule': schedule, 'sources': {name: ref(HERE / name) for name in ('conductor.py', 'producer.py', 'raw.py', 'archive.py')},
            'limitations': ['Single frozen seed set; no independent-repetition claim', 'Meter wall energy is not scheduler ownership energy']}


def load_binding(path):
    value = load(path)
    if value.get('schema') != 'e5-binding.v4':
        raise ValueError('FrozenInputInvalid: binding v4 required')
    return value


def merge_patch(current, patch):
    """RFC 7396: objects recurse, arrays/scalars replace, null deletes keys."""
    if not isinstance(patch, dict):
        return copy.deepcopy(patch)
    result = copy.deepcopy(current) if isinstance(current, dict) else {}
    for key, value in patch.items():
        if value is None:
            result.pop(key, None)
        else:
            result[key] = merge_patch(result.get(key), value)
    return result


def path_value(value, path):
    for key in path:
        if not isinstance(value, dict):
            return {'exists': False, 'blockedBy': copy.deepcopy(value)}
        if key not in value:
            return {'exists': False}
        value = value[key]
    return {'exists': True, 'value': copy.deepcopy(value)}


def patch_changes(before, after, patch, prefix=(), created_parents=()):
    """Track patched members, including members of newly created objects."""
    changed = []
    for key, value in patch.items():
        path = (*prefix, key)
        left, right = path_value(before, (key,)), path_value(after, (key,))
        if isinstance(value, dict) and (not left['exists'] or isinstance(before.get(key), dict)):
            parents = (*created_parents, path) if not left['exists'] else created_parents
            members = patch_changes(before.get(key, {}), after[key], value, path, parents)
            if members:
                changed.extend(members)
                continue
            if left == right:
                continue
        if left != right:
            changed.append({'path': list(path), 'before': left, 'applied': right,
                            'createdParents': [list(parent) for parent in created_parents]})
    return changed


def restore_paths(current, changed):
    result = copy.deepcopy(current)
    for change in changed:
        target = result
        for key in change['path'][:-1]:
            if key not in target:
                target[key] = {}
            if not isinstance(target[key], dict):
                raise RuntimeError('ControlConflict: patched settings parent changed')
            target = target[key]
        key, original = change['path'][-1], change['before']
        if original['exists']:
            target[key] = copy.deepcopy(original['value'])
        else:
            target.pop(key, None)
    parents = {tuple(parent) for change in changed for parent in change['createdParents']}
    for path in sorted(parents, key=len, reverse=True):
        if path_value(result, path) == {'exists': True, 'value': {}}:
            target = result
            for key in path[:-1]:
                target = target[key]
            target.pop(path[-1])
    return result


def settings_get(api):
    metadata = {}
    current = api('GET', SETTINGS, response=metadata)
    etag = metadata.get('etag')
    if metadata.get('statusCode') != 200 or not isinstance(etag, str) or len(etag) < 3 or not etag.startswith('"') or not etag.endswith('"'):
        raise RuntimeError('ObservationUnavailable: public settings GET lacks HTTP 200 and quoted ETag')
    return current, metadata


def settings_put(api, request, etag, attempt):
    attempt.update(ifMatch=etag, request=request, requestedAt=now())
    metadata = {}
    try:
        value = api('PUT', SETTINGS, request, headers={'If-Match': etag}, response=metadata)
        if metadata.get('statusCode') != 200 or not metadata.get('etag') or not isinstance(value, dict) or not isinstance(value.get('after'), dict) or not value.get('appliedAt'):
            raise RuntimeError('RemoteEffectUnknown: settings PUT lacks application/ETag evidence')
        return value
    finally:
        attempt.update(statusCode=metadata.get('statusCode'), responseETag=metadata.get('etag'), response=metadata.get('body'))


def factor_apply(api, declared, path):
    """Apply exactly once with the operator's public optimistic concurrency API."""
    path = Path(path)
    if path.exists():
        raise RuntimeError('ControlConflict: settings receipt already exists')
    if not isinstance(declared, dict) or set(declared) & SETTINGS_PROJECTIONS:
        raise ValueError('FrozenInputInvalid: settingsPatch requires writable SchedulerSettings fields')
    current, metadata = settings_get(api)
    writable = {key: value for key, value in current.items() if key not in SETTINGS_PROJECTIONS}
    request = merge_patch(writable, declared)
    changed = patch_changes(current, request, declared)
    receipt = {'settingsPatch': declared, 'before': current, 'beforeETag': metadata['etag'], 'changed': changed,
               'request': request, 'requestedAt': now(), 'effectUncertain': bool(changed), 'put': {}}
    save(path, receipt)
    if changed:
        receipt['put'] = {'ifMatch': metadata['etag'], 'request': request, 'requestedAt': now()}
        save(path, receipt)
        try:
            receipt['response'] = settings_put(api, request, metadata['etag'], receipt['put'])
            receipt['applied'] = receipt['response']['after']
            for change in changed:
                change['applied'] = path_value(receipt['applied'], change['path'])
            receipt['appliedAt'] = receipt['response']['appliedAt']
            receipt['effectUncertain'] = False
        except Exception as error:
            rejected = receipt['put'].get('statusCode') in (400, 401, 403, 404, 405, 409, 422, 428)
            receipt.update(error=str(error), failedAt=now(), effectUncertain=not rejected)
            save(path, receipt)
            raise
        save(path, receipt)
    receipt['readback'], readback_meta = settings_get(api)
    receipt['readbackETag'] = readback_meta['etag']
    receipt['accepted'] = all(path_value(receipt['readback'], change['path']) == change['applied'] for change in changed)
    save(path, receipt)
    if not receipt['accepted']:
        raise RuntimeError('ControlConflict: public settings patch readback differs')
    return receipt


def factor_revert(api, path):
    path = Path(path)
    if not path.exists():
        return
    receipt = load(path)
    if receipt.get('effectUncertain'):
        raise RuntimeError('RemoteEffectUnknown: settings PUT outcome not confirmed; no rollback or retry')
    if receipt.get('revertedAt') or not receipt.get('appliedAt'):
        return
    attempts = receipt.setdefault('revertAttempts', [])
    if attempts and attempts[-1].get('statusCode') != 409:
        if attempts[-1].get('statusCode') != 200:
            raise RuntimeError('RemoteEffectUnknown: prior settings revert outcome unconfirmed')
        # Successful PUT already has server-side application evidence. Readback only;
        # never repeat it after a lost local write or an external subsequent change.
        receipt['revertReadback'], metadata = settings_get(api)
        receipt['revertReadbackETag'] = metadata['etag']
        receipt['revertedAt'] = receipt['revertResponse']['appliedAt']
        save(path, receipt)
        return
    while len(attempts) < 2:
        current, metadata = settings_get(api)
        eligible, skipped = [], []
        for change in receipt['changed']:
            observed = path_value(current, change['path'])
            if observed == change['applied']:
                eligible.append(change)
            else:
                skipped.append({'path': change['path'], 'observed': observed, 'reason': 'external-change-revert-skipped'})
        receipt['revertSkipped'] = skipped
        if not eligible:
            receipt.update(revertReadback=current, revertReadbackETag=metadata['etag'], revertedAt=now(), revertedPaths=[])
            save(path, receipt)
            return
        writable = {key: value for key, value in current.items() if key not in SETTINGS_PROJECTIONS}
        request = restore_paths(writable, eligible)
        attempt = {'ifMatch': metadata['etag'], 'request': request, 'requestedAt': now()}
        attempts.append(attempt)
        receipt.update(revertRequest=request, revertRequestedAt=attempt['requestedAt'])
        save(path, receipt)
        try:
            receipt['revertResponse'] = settings_put(api, request, metadata['etag'], attempt)
        except Exception as error:
            receipt.update(revertError=str(error), revertFailedAt=now())
            save(path, receipt)
            if attempt.get('statusCode') == 409:
                continue
            raise
        save(path, receipt)
        receipt['revertReadback'], readback_meta = settings_get(api)
        receipt['revertReadbackETag'] = readback_meta['etag']
        receipt['revertedPaths'] = [change['path'] for change in eligible]
        receipt['revertReadbackExternalChanges'] = [
            {'path': change['path'], 'observed': path_value(receipt['revertReadback'], change['path']),
             'reason': 'external-change-after-revert'} for change in eligible
            if path_value(receipt['revertReadback'], change['path']) != change['before']]
        receipt['revertedAt'] = receipt['revertResponse']['appliedAt']
        save(path, receipt)
        return
    raise RuntimeError('SettingsConflict: second revert If-Match conflict; Returned blocked')


def resource_telemetry(resources, producers):
    clients = {(pod['metadata']['uid'], status['containerID']) for pod in producers
               for status in pod.get('status', {}).get('containerStatuses', []) if status['name'] == 'load' and status.get('containerID')}
    result = []
    for item in resources:
        row = item['row']
        if row.get('kind') != 'node':
            continue
        for sample in row.get('samples') or []:
            for container in sample.get('containers') or []:
                if (container.get('podUID'), container.get('containerID')) in clients:
                    result.append({**container, 'at': sample['sampledAt'], 'receivedAt': row.get('receivedAt'),
                                   'role': 'client', 'node': row['node'], 'sourceRef': item['sourceRef'],
                                   'nrThrottled': container.get('throttledPeriods'), 'memoryBytes': container.get('memoryCurrentBytes')})
    return result


class ProducerResources:
    """Read only the evaluator's exact Pod/container cumulative counters."""
    def __init__(self, io, pods, api_seconds, evidence):
        self.io, self.pods, self.api_seconds, self.evidence = io, list(pods.values()), api_seconds, evidence
        self.archive = module('evaluation_producer_archive', HERE / 'archive.py')
        self.first = dt.datetime.now(dt.timezone.utc).replace(minute=0, second=0, microsecond=0)
        self.minute = None
        self.rows = []

    def poll(self, deadline):
        self.archive._remaining(deadline)
        minute = time.time_ns() // 60_000_000_000
        if minute == self.minute:
            return self.rows
        resources = []
        current = dt.datetime.now(dt.timezone.utc).replace(minute=0, second=0, microsecond=0)
        for hour in self.archive._hours(self.first, current):
            endpoint = self.archive.RAW_PATH + '/' + hour + '/resources'
            try:
                with self.archive._raw_get(self.io, endpoint, min(deadline, time.monotonic() + self.api_seconds)) as response:
                    content = response.headers.get_content_type()
                    if response.status != 200 or content not in ('application/x-ndjson', 'application/gzip'):
                        raise RuntimeError('Unexpected producer raw resources response')
                    source = gzip.GzipFile(fileobj=response) if content == 'application/gzip' else response
                    for index, line in enumerate(source, 1):
                        self.archive._remaining(deadline)
                        resources.append({'row': json.loads(line), 'sourceRef': {'endpoint': endpoint, 'line': index}})
            except urllib.error.HTTPError as error:
                if error.code == 409:
                    try:
                        code = json.loads(error.read()).get('code')
                    except ValueError:
                        code = None
                    if code != 'ObservationNotReady':
                        raise
                elif error.code != 404:
                    raise
        self.rows = resource_telemetry(resources, self.pods)
        self.minute = minute
        save(self.evidence, {'at': now(), 'rows': self.rows})
        return self.rows



class CPO:
    def __init__(self, args, *, go_callback=None):
        self.a, self.out = args, Path(args.out)
        self.b = load_binding(args.binding)
        self.c = self.b['config']
        self.ws = {w['name']: w for w in self.c['workloads']}
        self.tb = self.c['timeboxes']
        self.io = Client(self.c)
        self.api = self.io.api
        self.go_callback = go_callback
        self.residuals = []

    def disk_boundary(self, *args):
        usage = os.statvfs(self.out)
        if usage.f_bavail * usage.f_frsize < self.c['evaluation']['minimumFreeBytes']:
            raise RuntimeError('EvidenceCapacityInsufficient: workspace free bytes')

    def factor(self):
        return factor_apply(self.api, self.c['evaluation']['settingsPatch'], self.out / 'settings-patch.json')

    def producer_job_name(self, name, stage):
        execution = self.c['evaluation']['execution']
        return 'load-' + execution['stageUID'].replace('-', '')[:16] + '-' + str(list(self.ws).index(name)) + '-0'

    def job(self, name, stage, folder):
        workload, trace = self.ws[name], stage['traces'][name]
        generator = self.c['loadGenerator']
        execution = self.c['evaluation']['execution']
        label = self.producer_job_name(name, stage)
        labels = {'edge.dacs.io/workload-role': 'load-generator', 'edge.dacs.io/evaluation-run-uid': execution['runUID'],
                  'edge.dacs.io/evaluation-stage': execution['stageID'], 'edge.dacs.io/evaluation-step': 'stage'}
        owner = {'apiVersion': 'edge.dacs.io/v1alpha1', 'kind': 'EvaluationStage', 'name': execution['stageName'],
                 'uid': execution['stageUID'], 'controller': True, 'blockOwnerDeletion': True}
        policy = {'arrivalSHA256': trace['arrivalSHA256'], 'offered': trace['offered'],
                  'shards': generator['shards'][name], 'barrierSeconds': self.tb['barrierSeconds'],
                  'segments': [{'rate': part['rates'][name], 'seconds': part['seconds']} for part in stage['parts']],
                  'datasetRef': workload['datasetRef'], 'sourceSHA': generator['sourceSHA']}
        trace_mounts, trace_volumes = [], []
        if name in self.c.get('traceOffsets', {}):
            volume = self.c.get('traceVolume')
            if not volume:
                raise ValueError('FrozenInputInvalid: explicit arrivals require the frozen workspace volume')
            path = Path(folder) / ('arrivals-' + name + '.json')
            save(path, trace['offsetsNS'])
            try:
                subpath = path.resolve().relative_to(Path(volume['mountPath']).resolve()).as_posix()
            except ValueError as error:
                raise ValueError('FrozenInputInvalid: explicit arrivals outside frozen workspace') from error
            policy.update(offsetsFile='/evaluation/.chill-arrivals.json', offsetsFileSHA256=ref(path)['sha256'])
            trace_volumes.append({'name': 'evaluation-arrivals',
                                  'persistentVolumeClaim': {'claimName': volume['claimName'], 'readOnly': True}})
            trace_mounts.append({'name': 'evaluation-arrivals', 'mountPath': policy['offsetsFile'],
                                 'subPath': subpath, 'readOnly': True})
        encoded = base64.b64encode(zlib.compress(json.dumps(policy, separators=(',', ':'), allow_nan=False).encode())).decode('ascii')
        if len(encoded) > 120000:
            raise RuntimeError('FrozenInputInvalid: arrival policy exceeds safe exec argument size')
        args = [encoded, 'replay', '--endpoint', workload['producerEndpoint'], '--token-file', workload['producerTokenFile'],
                '--ca-file', workload['producerCAFile'], '--workload', name, '--dataset-dir', '/evaluation',
                '--fields', ','.join(workload['fields']), '--arrivals', self.c['arrivals'][name], '--seed', str(trace['seed']),
                '--workers', str(workload['workers']), '--duration', str(stage['seconds']), '--rate', str(stage['rates'][name]),
                '--delivery-margin', str(workload['deliverySeconds']), '--timeout', str(workload['socketSeconds'])]
        if workload.get('lifetimeSeconds') is not None:
            args += ['--request-lifetime', str(workload['lifetimeSeconds'])]
        if 'gatewayCapSeconds' in workload:
            args += ['--workload-slo', str(workload['slo']['targetLatencyNanos'] / 1e9),
                     '--gateway-timeout', str(workload['gatewayCapSeconds'])]
        # Preserve operational account/CA/token projections, never host-supplied executable templates.
        pod = copy.deepcopy(workload.get('podSpec', {}))
        allowed = {'serviceAccountName', 'automountServiceAccountToken', 'volumes', 'imagePullSecrets', 'securityContext', 'tolerations'}
        ignored_pod_keys = sorted(set(pod) - allowed)
        pod = {key: value for key, value in pod.items() if key in allowed}
        mounts = copy.deepcopy(workload.get('volumeMounts', []))
        data = {'name': 'evaluation-data', 'mountPath': '/evaluation'}
        pod.setdefault('volumes', []).extend([{'name': 'evaluation-data', 'emptyDir': {}}, *trace_volumes])
        pod.update(restartPolicy='Never', nodeName=generator['nodeName'],
                   initContainers=[{'name': 'dataset', 'image': workload['dataset']['source']['image'],
                                    'command': ['sh', '-ec', 'cp -R "$1"/. /evaluation/', '--', workload['dataset']['source']['path'].rstrip('/') + '/evaluation'],
                                    'volumeMounts': [data], 'resources': copy.deepcopy(generator['resources'])}],
                   containers=[{'name': 'load', 'image': generator['image'], 'command': ['python3', '-u', '-c', (HERE / 'producer.py').read_text()],
                                'args': args, 'volumeMounts': [data, *mounts, *trace_mounts], 'resources': copy.deepcopy(generator['resources'])}])
        result = {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': label, 'namespace': self.c['namespace'], 'labels': labels, 'ownerReferences': [owner],
                  'annotations': {'edge.dacs.io/ignored-pod-keys': json.dumps(ignored_pod_keys)}},
                  'spec': {'backoffLimit': 0, 'parallelism': 1, 'completions': 1,
                           'activeDeadlineSeconds': math.ceil(sum(self.tb.values()) + stage['seconds']),
                           'template': {'metadata': {'labels': labels}, 'spec': pod}}}
        save(folder / ('job-' + name + '.json'), result)
        return result

    def owned(self, obj):
        execution = self.c['evaluation']['execution']
        owners = {execution['stageUID']} if execution.get('stageUID') else {child['uid'] for child in execution.get('childRefs', [])}
        return obj['metadata'].get('labels', {}).get('edge.dacs.io/evaluation-run-uid') == execution['runUID'] and any(
            owner.get('kind') == 'EvaluationStage' and owner.get('uid') in owners
            for owner in obj['metadata'].get('ownerReferences', []))

    def live_pod(self, job):
        found = self.io.k(['get', 'pods', '-l', 'job-name=' + job['metadata']['name'], '-o', 'json'])['items']
        pods = [pod for pod in found if not pod['metadata'].get('deletionTimestamp') and any(
            owner.get('kind') == 'Job' and owner.get('uid') == job['metadata']['uid'] for owner in pod['metadata'].get('ownerReferences', []))]
        if len(pods) > 1:
            raise RuntimeError('IdentityMismatch: multiple generator Pods')
        return pods[0] if pods else None

    def assert_pod(self, current, original):
        if current['metadata']['uid'] != original['metadata']['uid']:
            raise RuntimeError('IdentityMismatch: producer Pod changed')
        statuses = [s for s in current.get('status', {}).get('containerStatuses', []) if s['name'] == 'load']
        before = [s for s in original['status']['containerStatuses'] if s['name'] == 'load']
        if len(statuses) != 1 or statuses[0].get('containerID') != before[0]['containerID'] or statuses[0].get('restartCount') or not statuses[0].get('state', {}).get('running'):
            raise RuntimeError('IdentityMismatch: producer process changed or stopped')

    def observe_generators(self, folder):
        active = self.io.k(['get', 'jobs', '-l', 'edge.dacs.io/workload-role=load-generator', '-o', 'json'])['items']
        foreign = [job for job in active if not self.owned(job) and not any(
            c['type'] in ('Complete','Failed') and c['status']=='True' for c in job.get('status',{}).get('conditions',[]))]
        uids = {job['metadata']['uid'] for job in foreign}
        candidates = self.io.k(['get','pods','-l','edge.dacs.io/workload-role=load-generator','-o','json'])['items'] if uids else []
        running = [{'name': pod['metadata']['name'], 'uid': pod['metadata']['uid'], 'node': pod.get('spec',{}).get('nodeName')}
                   for pod in candidates if pod.get('spec',{}).get('nodeName') == self.c['loadGenerator']['nodeName']
                   and pod.get('status',{}).get('phase') == 'Running'
                   and any(status.get('state',{}).get('running') is not None for status in pod.get('status',{}).get('containerStatuses',[]))
                   and any(ref.get('kind')=='Job' and ref.get('uid') in uids for ref in pod['metadata'].get('ownerReferences',[]))]
        fact = {'at': now(), 'jobs': [{'name': job['metadata']['name'], 'uid': job['metadata']['uid']} for job in foreign],
                'otherProducerRunningOnGeneratorNode': running}
        path = Path(folder) / 'generator-interference.json'
        history = load(path) if path.exists() else []
        history.append(fact)
        save(path, history)
        return fact

    def sample_resources(self, telemetry, deadline, phase):
        try:
            return telemetry.poll(deadline) if telemetry else []
        except Exception as error:
            if isinstance(error, urllib.error.HTTPError) and error.code in (401, 403):
                raise
            phase.setdefault('diagnosticErrors', []).append({'at': now(), 'error': str(error)})
            return []

    def monitor(self, offset, folder):
        self.disk_boundary()
        return offset

    def run(self):
        stage = next(row for row in self.b['schedule'] if row['id'] == self.a.stage)
        parse_ns = module('evaluation_run_time', HERE / 'raw.py')._time
        folder = Path(self.c['evaluation']['stageFolder'])
        if folder.exists():
            raise RuntimeError('FrozenInputInvalid: no implicit stage overwrite/retry')
        receipt = load(self.out / 'settings-patch.json')
        if not receipt.get('accepted'):
            raise RuntimeError('ControlConflict: settings patch not read back')
        # Provenance labels and shared-generator activity qualify the result;
        # neither is permission to discard an otherwise observable experiment.
        auth = module('evaluation_auth', HERE / 'auth.py')
        budget = auth.lifetime_budget([stage], self.tb, self.c.get('tokenSafetyMarginSeconds', 60))
        save(self.out / 'token-preflight.json', auth.check(self.c['tokenFile'], budget, endpoint=self.c['endpoint'], ca_file=self.c['caFile']))
        folder.mkdir(parents=True)
        phase = {'failurePolicy': 'v2', 'stage': stage, 'status': 'RUNNING', 'starts': {}, 'startUTC': None, 'endUTC': None,
                 'sourceObservations': {'declared': self.b['sources'], 'imageSourceSHA': os.environ.get('CHILL_HARNESS_SOURCE_SHA')},
                 'drain': {'complete': False, 'reasons': []}}
        self.observe_generators(folder)
        jobs, pods, streams, buffers, ready, drained = {}, {}, [], {}, {}, set()
        selector = selectors.DefaultSelector()
        deadline = time.monotonic() + self.tb['setupSeconds'] + self.tb['barrierSeconds']
        try:
            for name in self.ws:
                if not stage['traces'][name]['offered']:
                    continue
                job = self.io.k(['create', '-f', '-', '-o', 'json'], stdin=json.dumps(self.job(name, stage, folder)))
                if not self.owned(job):
                    raise RuntimeError('IdentityMismatch: created Job ownership')
                jobs[name] = job
                save(folder / ('created-' + name + '.json'), job)
            for name, job in jobs.items():
                while True:
                    self.monitor(0, folder)
                    pod = self.live_pod(job)
                    if pod and pod.get('status', {}).get('phase') == 'Failed':
                        raise RuntimeError('ToolFailed: producer Pod failed')
                    statuses = (pod or {}).get('status', {}).get('containerStatuses', [])
                    load_status = next((s for s in statuses if s['name'] == 'load'), {})
                    if load_status.get('state', {}).get('running') and load_status.get('containerID'):
                        digest = self.c['loadGenerator']['image'].split('@')[-1]
                        if not load_status.get('imageID', '').endswith(digest) or load_status.get('restartCount'):
                            raise RuntimeError('IdentityMismatch: generator image or restart')
                        pods[name] = pod
                        save(folder / ('producer-' + name + '.json'), pod)
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError('BarrierTimeout: generator setup')
                    time.sleep(.2)
                output = (folder / (name + '-client.jsonl')).open('w')
                error = (folder / (name + '-log.stderr')).open('w')
                process = subprocess.Popen(['/usr/bin/kubectl', '-n', self.c['namespace'], 'logs', '-f', pod['metadata']['name'], '-c', 'load'], stdout=subprocess.PIPE, stderr=error, bufsize=0)
                os.set_blocking(process.stdout.fileno(), False)
                streams.append((process, output, error))
                buffers[name] = b''
                selector.register(process.stdout, selectors.EVENT_READ, (name, output))
            telemetry = ProducerResources(self.io, pods, self.tb['apiSeconds'], folder / 'producer-resources.json') if pods else None
            released = not jobs
            if released:
                phase['startUTC'] = phase['goUTC'] = now()
                phase['endUTC'] = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=stage['seconds'])).isoformat()
                deadline = time.monotonic() + stage['seconds']
                self.record_go(folder, jobs, pods, phase['goUTC'])
            while True:
                self.monitor(0, folder)
                for key, _ in selector.select(.2):
                    name, output = key.data
                    try:
                        chunk = os.read(key.fileobj.fileno(), 65536)
                    except BlockingIOError:
                        continue
                    buffers[name] += chunk
                    while b'\n' in buffers[name]:
                        line, buffers[name] = buffers[name].split(b'\n', 1)
                        text = line.decode('utf-8')
                        output.write(text + '\n')
                        output.flush()
                        try:
                            row = json.loads(text)
                            if not isinstance(row, dict):
                                raise ValueError('client JSON row is not an object')
                        except ValueError as error:
                            phase.setdefault('clientLogErrors', []).append({'producer': name, 'error': str(error)})
                            continue
                        event = row.get('evaluationEvent')
                        if event == 'ready':
                            trace = stage['traces'][name]
                            if name in ready or row['offered'] != trace['offered'] or row['arrivalSHA256'] != trace['arrivalSHA256'] or not row['payload']:
                                raise RuntimeError('IdentityMismatch: producer Ready input identity')
                            ready[name] = row
                            save(folder / ('payload-' + name + '.json'), row)
                        elif event == 'start':
                            if not released or name in phase['starts']:
                                raise RuntimeError('IdentityMismatch: producer start protocol')
                            phase['starts'][name] = row
                        elif event == 'drained':
                            drained.add(name)
                    if not chunk:
                        if buffers[name]:
                            phase.setdefault('truncatedLogBytes', {})[name] = base64.b64encode(buffers[name]).decode()
                            phase['drain']['reasons'].append('TruncatedLogEOF:' + name)
                            buffers[name] = b''
                        selector.unregister(key.fileobj)
                if not released and len(ready) == len(jobs):
                    for name, pod in pods.items():
                        current = self.io.k(['get', 'pod', pod['metadata']['name'], '-o', 'json'])
                        self.assert_pod(current, pod)
                        if time.time_ns() >= parse_ns(ready[name]['at']) + round(ready[name]['barrierSeconds'] * 1e9):
                            raise RuntimeError('BarrierTimeout: Ready expired')
                    phase['goUTC'] = now()
                    for pod in pods.values():
                        self.io.k(['exec', pod['metadata']['name'], '-c', 'load', '--', 'python3', '-c', "from pathlib import Path; Path('/tmp/evaluation-start').touch()"])
                    released = True
                    phase['startUTC'] = phase['goUTC']
                    phase['endUTC'] = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=stage['seconds'])).isoformat()
                    phase['intervalSource'] = 'GO until producer start observations are available'
                    self.record_go(folder, jobs, pods, phase['goUTC'])
                    deadline = time.monotonic() + stage['seconds'] + self.tb['drainSeconds'] + self.tb['collectSeconds']
                if jobs and len(phase['starts']) == len(jobs):
                    phase['startUTC'] = min(phase['starts'].values(), key=lambda row: parse_ns(row['at']))['at']
                    end_ns = max(parse_ns(row['at']) for row in phase['starts'].values()) + round(stage['seconds'] * 1e9)
                    seconds, nanos = divmod(end_ns, 1000000000)
                    phase['endUTC'] = dt.datetime.fromtimestamp(seconds, dt.timezone.utc).strftime('%Y-%m-%dT%H:%M:%S') + f'.{nanos:09d}+00:00'
                    counters = self.sample_resources(telemetry, deadline, phase)
                    for name in drained:
                        matching = [row for row in counters if row.get('podUID') == pods[name]['metadata']['uid']]
                        if matching:
                            save(folder / (name + '-client-end-counter.json'), matching[-1])
                save(folder / 'phase.json', phase)
                if released and not jobs and time.monotonic() >= deadline:
                    break
                if jobs and not selector.get_map() and all(p.poll() is not None for p, _, _ in streams):
                    if not released:
                        raise RuntimeError('NoLoad: producer EOF before GO')
                    if drained != set(jobs) or any(p.returncode for p, _, _ in streams):
                        phase['drain']['reasons'].append('ProducerEOFWithoutCompleteDrain')
                    break
                if time.monotonic() >= deadline:
                    if not released:
                        raise RuntimeError('NoLoad: generator setup/release watchdog')
                    phase['drain']['reasons'].append('DrainWatchdog')
                    break
            for name, job in jobs.items():
                if phase['drain']['reasons']:
                    break
                current = self.io.k(['get', 'job', job['metadata']['name'], '-o', 'json'])
                if current['metadata']['uid'] != job['metadata']['uid']:
                    raise RuntimeError('IdentityMismatch: generator Job UID')
                save(folder / ('terminal-' + name + '.json'), current)
                if any(c['type']=='Failed' and c['status']=='True' for c in current.get('status',{}).get('conditions',[])):
                    phase['drain']['reasons'].append('ProducerJobFailed:' + name)
            phase['drain']['complete'] = not phase['drain']['reasons'] and drained == set(jobs)
            if not phase['drain']['complete']:
                cutoff = now()
                phase['plannedEndUTC'] = phase['endUTC']
                if phase['endUTC'] and parse_ns(phase['endUTC']) > parse_ns(cutoff):
                    phase['endUTC'] = cutoff
                # Stop only exact recorded evaluator Job UIDs; cleanup failure
                # stays a safety hold, but does not destroy collected facts.
                try:
                    self._cleanup_jobs()
                except Exception as error:
                    phase.setdefault('cleanupErrors', []).append(str(error))
                for process, _, _ in streams:
                    if process.poll() is None:
                        process.terminate()
                    process.wait(timeout=self.tb['apiSeconds'])
            phase.update(drainedUTC=now(), status='DRAINED_NOT_EVALUATED' if phase['drain']['complete'] else 'PARTIAL_DRAIN_NOT_EVALUATED',
                         drainScope='Drain completion is independent of diagnostic CPU/throttle counters')
            save(folder / 'phase.json', phase)
            time.sleep(self.tb['flushSeconds'])
            archive = module('evaluation_archive', HERE / 'archive.py')
            save(folder / 'collected.json', archive.collect(folder, phase, self.c['collection'], self.io, self.tb['collectSeconds']))
        except Exception as error:
            phase.update(status='STOP', error=str(error), failureUTC=now())
            for name, job in jobs.items():
                try:
                    current = self.io.k(['get', 'job', job['metadata']['name'], '-o', 'json'])
                    if current['metadata']['uid'] != job['metadata']['uid'] or not self.owned(current):
                        raise RuntimeError('ForeignOwnership: producer changed before STOP')
                    save(folder / ('stopped-job-' + name + '.json'), current)
                    self.io.delete_job(job['metadata']['name'], job['metadata']['uid'])
                except Exception as stop_error:
                    phase.setdefault('cleanupErrors', []).append(str(stop_error))
            save(folder / 'phase.json', phase)
            raise
        finally:
            selector.close()
            for process, output, error in streams:
                if process.poll() is None:
                    process.terminate()
                process.wait(timeout=self.tb['apiSeconds'])
                process.stdout.close()
                output.close()
                error.close()

    def record_go(self, folder, jobs, pods, at):
        value = {'goUTC': at, 'producerRefs': [{'workloadUID': self.ws[name]['uid'], 'shard': 0,
                 'jobRef': {key: job['metadata'][key] for key in ('name', 'uid')},
                 'podRef': {key: pods[name]['metadata'][key] for key in ('name', 'uid')}} for name, job in jobs.items()]}
        save(folder / 'go.json', value)
        if self.go_callback:
            self.go_callback(value)

    def cleanup_jobs(self):
        # Job cleanup and settings restoration are independent obligations.
        # A failed delete must retain the pool, not retain our control patch.
        attempt = {'startedAt': now(), 'cleanup': {}, 'restore': {'status': 'Complete', 'receipts': []}}
        failure = None
        try:
            self._cleanup_jobs()
            attempt['cleanup'] = {'status': 'Complete'}
        except Exception as error:
            failure = error
            attempt['cleanup'] = {'status': 'Failed', 'error': str(error)}
        attempt['cleanup']['finishedAt'] = now()
        attempt['cleanup']['residuals'] = copy.deepcopy(self.residuals)
        for path in sorted(self.out.rglob('settings-patch.json')):
            receipt = {'path': str(path.relative_to(self.out)), 'startedAt': now()}
            try:
                factor_revert(self.api, path)
                receipt['status'] = 'Complete'
            except Exception as error:
                if failure is None:
                    failure = error
                receipt.update(status='Failed', error=str(error))
                attempt['restore']['status'] = 'Failed'
                residual = {'kind': 'SchedulerSettings', 'name': SETTINGS, 'reason': 'RemoteEffectUnknown'}
                if residual not in self.residuals:
                    self.residuals.append(residual)
            receipt['finishedAt'] = now()
            attempt['restore']['receipts'].append(receipt)
        attempt['finishedAt'] = now()
        path = self.out / 'cleanup-outcome.json'
        evidence = load(path) if path.exists() else {'attempts': []}
        evidence['attempts'].append(attempt)
        save(path, evidence)
        if failure is not None:
            raise failure

    def _cleanup_jobs(self):
        execution = self.c['evaluation']['execution']
        selector = 'edge.dacs.io/workload-role=load-generator,edge.dacs.io/evaluation-run-uid=' + execution['runUID']
        if execution.get('stageID'):
            selector += ',edge.dacs.io/evaluation-stage=' + execution['stageID']
        jobs = self.io.k(['get', 'jobs', '-l', selector, '-o', 'json'])['items']
        deadline = time.monotonic() + self.tb['drainSeconds']
        recorded = {record['metadata']['uid']: record for path in self.out.rglob('created-*.json')
                    for record in [load(path)] if record.get('kind') == 'Job'}
        for job in jobs:
            if not self.owned(job) or job['metadata']['uid'] not in recorded or recorded[job['metadata']['uid']]['metadata']['name'] != job['metadata']['name']:
                self.residuals.append({'kind': 'Job', 'name': job['metadata']['name'], 'reason': 'RemoteEffectUnknown'})
                raise RuntimeError('RemoteEffectUnknown: Job lacks exact evaluator creation UID evidence')
            name, uid = job['metadata']['name'], job['metadata']['uid']
            try:
                self.io.delete_job(name, uid)
            except Exception:
                self.residuals.append({'kind': 'Job', 'name': name, 'uid': uid, 'reason': 'RemoteEffectUnknown'})
                raise
            while True:
                remaining = self.io.k(['get', 'jobs', '-l', selector, '-o', 'json'])['items']
                pods = self.io.k(['get', 'pods', '-l', 'job-name=' + name, '-o', 'json'])['items']
                if not any(j['metadata']['uid'] == uid for j in remaining) and not any(any(o.get('uid') == uid for o in p['metadata'].get('ownerReferences', [])) for p in pods):
                    break
                if time.monotonic() >= deadline:
                    self.residuals.append({'kind': 'Job', 'name': name, 'reason': 'ResidualState'})
                    raise RuntimeError('ResidualState: evaluator Job/Pod cleanup deadline')
                time.sleep(.2)
        while True:
            pods = self.io.k(['get', 'pods', '-l', selector, '-o', 'json'])['items']
            if not pods:
                break
            for pod in pods:
                if not any(owner.get('kind') == 'Job' and owner.get('uid') in recorded and self.owned(recorded[owner['uid']])
                           for owner in pod['metadata'].get('ownerReferences', [])):
                    self.residuals.append({'kind': 'Pod', 'name': pod['metadata']['name'], 'reason': 'RemoteEffectUnknown'})
                    raise RuntimeError('RemoteEffectUnknown: labeled Pod lacks exact evaluator Job UID evidence')
            if time.monotonic() >= deadline:
                self.residuals.extend({'kind': 'Pod', 'name': pod['metadata']['name'], 'reason': 'ResidualState'} for pod in pods)
                raise RuntimeError('ResidualState: evaluator Pods remain after Job disappearance')
            time.sleep(.2)

    def finish(self):
        # Cleanup only evaluation-owned effects and the deliberately patched settings paths.
        self.cleanup_jobs()


def judge(binding, folder):
    bound = load_binding(binding)
    config = bound['config']
    phase = load(folder / 'phase.json')
    stage = phase['stage']
    raw = module('evaluation_raw', HERE / 'raw.py')
    analysis = module('evaluation_analysis', HERE / 'd2/analyze_before.py')
    clients = {}
    unknown = []
    results = {}
    for workload in config['workloads']:
        name = workload['name']
        path = folder / (name + '-client.jsonl')
        rows = []
        if path.exists():
            for index, line in enumerate(path.read_text().splitlines(), 1):
                try:
                    row = json.loads(line)
                    if 'scheduledOffsetNs' in row:
                        rows.append(row)
                except (ValueError, TypeError):
                    unknown.append(name + ': malformed client JSONL line ' + str(index))
        clients[name] = rows
    indexes = raw.iter_request_indexes(folder, [w['uid'] for w in config['workloads']], phase['startUTC'], phase['endUTC'],
                                      actual_response_ids={w['uid']: [r['requestID'] for r in clients[w['name']] if isinstance(r.get('requestID'), str) and r['requestID']] for w in config['workloads']})
    for workload in config['workloads']:
        name = workload['name']
        trace = stage['traces'][name]
        rows = clients[name]
        indexed = indexes[workload['uid']]
        returned_ids = [row['requestID'] for row in rows if isinstance(row.get('requestID'), str) and row['requestID']]
        counts = Counter(returned_ids)
        quarantined = {rid for rid, count in counts.items() if count > 1}
        quarantined.update(item['requestID'] for item in indexed['summary'].get('finalConflicts', []))
        quarantined.update(indexed['summary'].get('invalidFinalRequestIDs', []))
        if any(row.get('workloadUID') not in (None, workload['uid']) for row in rows):
            raise ValueError('IdentityMismatch: returned Workload UID differs from frozen identity')
        usable = [row for row in rows if row.get('requestID') not in quarantined]
        supported = any(row.get('requestID') in indexed['finals'] for row in usable)
        start = phase['starts'].get(name, {}).get('monotonicNS')
        quality = analysis.issuance_fidelity(rows, trace['offsetsNS'], start, round(stage['seconds'] * 1e9), analysis.stats, config['generatorQuality'])
        completed, actual, scheduled, outcomes = [], [], [], Counter()
        for row in usable:
            final = indexed['finals'].get(row.get('requestID'))
            outcomes[final.get('outcome') if final else 'unfinished-or-no-final'] += 1
            timing = analysis.request_timing(row, final, None, start)
            if timing['sloLatencyNS'] is not None:
                completed.append(timing['sloLatencyNS'])
            actual.append(timing['actualLatencyNS'])
            scheduled.append(timing['scheduledLatencyNS'])
        offered = trace['offered']
        hits = sum(value <= workload['slo']['targetLatencyNanos'] for value in completed)
        required = math.ceil(offered * workload['slo']['percentile'] / 100)
        qstatus = quality['generatorQuality']['status']
        identities = indexed['identities']
        identity_known = (supported and len(identities) == 1 and identities[0]['workloadUID'] == workload['uid']
                          and bool(identities[0]['definitionID']))
        substages, base = [], 0
        for part in stage['parts']:
            stop = base + round(part['seconds'] * 1e9)
            planned = sum(base <= offset < stop for offset in trace['offsetsNS'])
            latencies = []
            part_rows = [row for row in rows if base <= row['scheduledOffsetNs'] < stop]
            part_issued = analysis.issued_calls(part_rows)
            part_supported = any(row.get('requestID') in indexed['finals'] for row in usable
                                 if base <= row['scheduledOffsetNs'] < stop)
            for row in usable:
                if base <= row['scheduledOffsetNs'] < stop:
                    latency = analysis.request_timing(row, indexed['finals'].get(row.get('requestID')), None, start)['sloLatencyNS']
                    if latency is not None:
                        latencies.append(latency)
            successes = sum(value <= workload['slo']['targetLatencyNanos'] for value in latencies)
            rank = math.ceil(planned * workload['slo']['percentile'] / 100)
            substages.append({'id': part['id'], 'durationSeconds': part['seconds'], 'rate': part['rates'][name], 'offered': planned,
                              'completedLatencyNS': analysis.stats(latencies), 'deadlineSuccess': successes, 'requiredSuccessfulRank': rank,
                              'D1': 'NOT_OFFERED' if not planned else 'INVALID_NO_LOAD' if not part_issued else
                                    'NOT_EVALUATED' if not part_supported else 'FAIL' if successes < rank else 'OBSERVED_THRESHOLD_MET'})
            base = stop
        results[name] = {'offered': offered, 'clientRows': len(rows), 'outcomes': dict(outcomes), 'completedLatencyNS': analysis.stats(completed),
                         'gatewayCompletedLatencyNS': analysis.stats(completed), 'actualLatencyNS': analysis.stats(actual), 'scheduledLatencyNS': analysis.stats(scheduled),
                         'latencyScope': 'CHILL HTTP receipt to final elapsedNanos; client RTT and scheduled latency are references only',
                         'deadlineSuccess': hits, 'deadlineSuccessFraction': hits / offered if offered else None, 'requiredSuccessfulRank': required,
                         'unfinishedOrUnsent': max(0, offered - len(completed)), 'issuanceFidelity': quality,
                         'D1': 'NOT_OFFERED' if not offered else 'INVALID_NO_LOAD' if qstatus == 'INVALID_NO_LOAD' else
                               'NOT_EVALUATED' if not supported else 'FAIL' if hits < required else 'OBSERVED_THRESHOLD_MET',
                         'slo': workload['slo'], 'workload': {key: workload[key] for key in ('name', 'uid', 'generation')},
                         'workloadUID': workload['uid'],
                         'definitionID': identities[0]['definitionID'] if identity_known else None,
                         'actualGatewayIdentities': len(identities), 'observedDefinitions': identities, 'substages': substages,
                         'rawEvidenceErrors': indexed['errors'], 'quarantinedRequestIDs': sorted(quarantined),
                         'quarantinedClientRows': len(rows)-len(usable),
                         'rawEvidenceErrorCount': len(indexed['errors']), 'quarantinedRequestCount': len(quarantined),
                         'unkeyedRawRowCount': len(indexed['summary'].get('unkeyedRows', [])),
                         'identityMissingReason': None if identity_known else 'Actual returned request IDs lack one complete frozen-UID definition identity'}
    power = load(folder / 'power.json')
    tail_power = load(folder / 'power-drain-inclusive.json')
    failures = None
    if config['evaluation'].get('failureInjection'):
        failures = load(folder / 'failure-events.json')
    statuses = [row['D1'] for row in results.values()]
    measured = any(status in ('FAIL', 'OBSERVED_THRESHOLD_MET') for status in statuses)
    slo_status = ('FAIL' if 'FAIL' in statuses else 'PASS' if measured else
                  'INVALID_NO_LOAD' if 'INVALID_NO_LOAD' in statuses else 'NOT_EVALUATED')
    result = {'schema': 'e5-judgment.v3', 'failurePolicy': 'v2', 'at': now(),
              'status': 'FAIL' if slo_status=='FAIL' else 'MEASURED_DESCRIPTIVE' if measured else 'PARTIAL',
              'notReadyReasons': [], 'clientParseErrors': unknown, 'requests': results, 'power': power, 'powerDrainInclusive': tail_power,
              'components': {'slo': {'status': slo_status, 'workloads': {name: row['D1'] for name,row in results.items()}},
                             'loadFidelity': {name: row['issuanceFidelity'] for name,row in results.items()},
                             'energy': {'offered': power.get('energyStatus'), 'drainInclusive': tail_power.get('energyStatus')},
                             'drain': phase.get('drain', {}),
                             'injection': {'status': 'NOT_REQUESTED' if failures is None else 'REALIZED' if
                                 len(failures.get('records', [])) == len(config['evaluation']['failureInjection']) and
                                 all(item.get('record', {}).get('outcome') == 'Complete' for item in failures.get('records', []))
                                 else 'NOT_REALIZED', 'evidence': failures}},
              'ignoredKeys': config.get('ignoredKeys', []), 'failureScenarioEvidence': failures,
              'limitations': bound['limitations'], 'judgmentCriteria': ['independent SLO, load fidelity, energy, injection and drain facts']}
    resources = folder / 'producer-resources.json'
    result['cgroups'] = load(resources) if resources.exists() else {'rows': [], 'meaning': 'No generator offered in this window'}
    save(folder / 'judgment.json', result)
    return result


def summarize(binding, root, *, evaluation=None):
    stages = evaluation['stages'] if evaluation else []
    results = evaluation.get('results', {}) if evaluation else {}
    measurements = [value.get('measurement', {}).get('verdict', 'NOT_EVALUATED') for value in results.values()]
    slo_statuses = measurements + [stage['kpis'].get('components', {}).get('slo', {}).get('status') for stage in stages]
    status = 'FAIL' if 'FAIL' in slo_statuses else 'MEASURED_DESCRIPTIVE' if 'PASS' in slo_statuses else 'NOT_EVALUATED'
    result = {'schema': 'e5-summary.v4', 'failurePolicy': 'v2', 'stages': stages, 'status': status, 'results': results,
              'completeness': 'PARTIAL' if 'NOT_EVALUATED' in measurements or len(stages) < len(results) or any(
                  stage['kpis'].get('power', {}).get('complete') is False or stage['kpis']['status']=='PARTIAL' for stage in stages) else 'OBSERVED',
              'components': {'measurement': {key: value.get('measurement') for key,value in results.items()},
                             'execution': {key: value.get('execution') for key,value in results.items()},
                             'energy': {stage['id']: stage['kpis'].get('power') for stage in stages}}}
    save(root / 'e5-summary.json', result)
    return result


def publication_measurements(binding, power):
    return {'measurementContract': {'binding': 'e5-binding.v4', 'judgment': 'e5-judgment.v3', 'failurePolicy': 'v2', 'energyScope': 'declared-node-meters', 'energyCoverage': binding['config']['collection']['energyCoverage']}}


def publication_stage_metadata(binding, phase, definitions, identity_source, phase_source, folder, root, rows):
    workloads = {w['name']: w for w in definitions['workloads']}
    for name, row in rows.items():
        workload = workloads[name]
        known = row.get('workloadUID') == workload['uid'] and row.get('definitionID') is not None
        row.update(workload={key: workload[key] for key in ('name', 'uid', 'generation')}, slo=workload['slo'],
                   identitySource=identity_source if known else None)


def self_check():
    import unittest
    suite = unittest.defaultTestLoader.discover(str(HERE), pattern='test_evaluation.py')
    if not unittest.TextTestRunner().run(suite).wasSuccessful():
        raise SystemExit(1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('self-check', 'bind', 'judge', 'tables'))
    parser.add_argument('--config', type=Path)
    parser.add_argument('--binding', type=Path)
    parser.add_argument('--out', type=Path)
    args = parser.parse_args()
    if args.command == 'self-check':
        self_check()
    elif args.command == 'bind':
        save(args.out, bind(args.config))
    elif args.command == 'judge':
        return 2 if judge(args.binding, args.out)['components']['slo']['status'] in ('INVALID_NO_LOAD','NOT_EVALUATED') else 0
    else:
        print(module('evaluation_tables', HERE / 'tables.py').render(args.out, REPO))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
