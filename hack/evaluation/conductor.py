#!/usr/bin/env python3
"""Black-box evaluation: absolute load, public feature controls and owned Jobs only."""
import argparse
import base64
import copy
import datetime as dt
import gzip
import hashlib
import io
import json
import math
import os
from pathlib import Path
import ssl
import shutil
import subprocess
import sys
import time
import threading
import urllib.error
import urllib.parse
import urllib.request
import zlib

import evidence_io

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent


class Client:
    def __init__(self, config):
        self.config = config
        origin = urllib.parse.urlsplit(config['endpoint'])
        if origin.scheme != 'https' or not origin.hostname or origin.username or origin.password or origin.path not in ('', '/') or origin.query or origin.fragment:
            raise ValueError('FrozenInputInvalid: API requires an HTTPS origin')
        auth = evidence_io.module('evaluation_auth', HERE / 'auth.py')
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
        auth = evidence_io.module('evaluation_delete_auth', HERE / 'auth.py')
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
    config = evidence_io.load(path)
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
    return {'schema': 'e5-binding.v4', 'failurePolicy': 'v2', 'status': 'BOUND_NOT_MEASURED', 'createdUTC': evidence_io.now(), 'config': config,
            'schedule': schedule, 'sources': {name: evidence_io.ref(HERE / name) for name in ('conductor.py', 'producer.py', 'raw.py', 'archive.py')},
            'limitations': ['Single frozen seed set; no independent-repetition claim', 'Meter wall energy is not scheduler ownership energy']}


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
        self.archive = evidence_io.module('evaluation_producer_archive', HERE / 'archive.py')
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
        evidence_io.save(self.evidence, {'at': evidence_io.now(), 'rows': self.rows})
        return self.rows



class ProducerLog:
    """Spool stdout independently of telemetry and provisional reduction."""
    def __init__(self, source, path):
        self.output = path.open('wb')
        self.reader = path.open('rb')
        self.written = 0
        self.error = None
        self.done = threading.Event()
        self.thread = threading.Thread(target=self.capture, args=(source,))
        self.thread.start()

    def capture(self, source):
        try:
            with io.BufferedReader(source) as stream, self.output:
                for line in stream:
                    self.output.write(line)
                    self.output.flush()
                    self.written += len(line)
        except Exception as error:
            self.error = error
        finally:
            self.done.set()

    def lines(self):
        stop = self.written
        while self.reader.tell() < stop:
            yield self.reader.readline(stop - self.reader.tell())
        self.check_error()

    def check_error(self):
        if self.error is not None:
            raise RuntimeError('ProducerCaptureFailed: ' + self.reader.name + ': ' + str(self.error)) from self.error

    def eof(self):
        return self.error is None and self.done.is_set() and self.reader.tell() == self.written

    def join(self):
        self.thread.join()
        self.check_error()

    def close(self):
        try:
            self.join()
        finally:
            self.reader.close()


class ProvisionalD1:
    """Own only the latest successful closed-minute input and projection."""
    def __init__(self, io, config, folder, publish):
        self.io, self.config, self.folder, self.publish = io, config, folder, publish
        self.last_attempt, self.minute, self.inputs = 0, None, None
        self.closed_through = None
        self.archive = evidence_io.module('evaluation_live_archive', HERE / 'archive.py')
        self.raw = evidence_io.module('evaluation_live_raw', HERE / 'raw.py')
        self.judgment = evidence_io.module('evaluation_live_judgment', HERE / 'judgment.py')

    def poll(self, phase):
        now = time.monotonic()
        if now - self.last_attempt < self.config['timeboxes']['flushSeconds']:
            return
        self.last_attempt = now
        minute = time.time_ns() // 60_000_000_000
        if minute == self.minute:
            return
        target = self.folder / 'provisional-inputs' / str(time.time_ns())
        try:
            cutoff = self.archive.collect_requests_snapshot(target, phase, self.io, self.config['timeboxes']['apiSeconds'])
            if self.closed_through and evidence_io.timestamp_ns(cutoff) <= evidence_io.timestamp_ns(self.closed_through):
                raise ValueError('Stale closed-minute request snapshot')
            clients = {}
            for workload in self.config['workloads']:
                path = self.folder / (workload['name'] + '-client.jsonl')
                clients[workload['name']] = []
                if path.exists():
                    with path.open('rb') as source:
                        for line in source:
                            # A live writer may have an incomplete last write.
                            if not line.endswith(b'\n'):
                                break
                            row = json.loads(line)
                            clients[workload['name']].append(row)
            ids = {w['uid']: [r['requestID'] for r in clients[w['name']] if isinstance(r.get('requestID'), str) and r['requestID']]
                   for w in self.config['workloads']}
            indexes = self.raw.iter_request_indexes(target, list(ids), phase['startUTC'], cutoff, actual_response_ids=ids)
            if any(value['errors'] for value in indexes.values()):
                raise ValueError('Partial raw request snapshot')
            value = self.judgment.provisional_requests(self.config, phase, clients, indexes, evidence_io.now(), cutoff)
            if self.publish:
                self.publish(value)
            evidence_io.save(self.folder / 'provisional-d1.json', value)
        except Exception as error:
            evidence_io.save(self.folder / 'provisional-d1-error.json', {'at': evidence_io.now(), 'error': str(error)})
            if target.exists():
                shutil.rmtree(target)
            return
        finally:
            self.raw.release_request_cache()
        if self.inputs:
            shutil.rmtree(self.inputs)
        self.inputs, self.minute = target, minute
        self.closed_through = cutoff


class CPO:
    def __init__(self, args, *, go_callback=None, provisional_callback=None):
        self.a, self.out = args, Path(args.out)
        self.b = evidence_io.load_binding(args.binding)
        self.c = self.b['config']
        self.ws = {w['name']: w for w in self.c['workloads']}
        self.tb = self.c['timeboxes']
        self.io = Client(self.c)
        self.api = self.io.api
        self.go_callback = go_callback
        self.provisional_callback = provisional_callback
        self.residuals = []

    def disk_boundary(self, *args):
        usage = os.statvfs(self.out)
        if usage.f_bavail * usage.f_frsize < self.c['evaluation']['minimumFreeBytes']:
            raise RuntimeError('EvidenceCapacityInsufficient: workspace free bytes')

    def factor(self):
        import settings_patch
        return settings_patch.factor_apply(self.api, self.c['evaluation']['settingsPatch'], self.out / 'settings-patch.json')

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
            evidence_io.save(path, trace['offsetsNS'])
            try:
                subpath = path.resolve().relative_to(Path(volume['mountPath']).resolve()).as_posix()
            except ValueError as error:
                raise ValueError('FrozenInputInvalid: explicit arrivals outside frozen workspace') from error
            policy.update(offsetsFile='/evaluation/.chill-arrivals.json', offsetsFileSHA256=evidence_io.ref(path)['sha256'])
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
        evidence_io.save(folder / ('job-' + name + '.json'), result)
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
        fact = {'at': evidence_io.now(), 'jobs': [{'name': job['metadata']['name'], 'uid': job['metadata']['uid']} for job in foreign],
                'otherProducerRunningOnGeneratorNode': running}
        path = Path(folder) / 'generator-interference.json'
        history = evidence_io.load(path) if path.exists() else []
        history.append(fact)
        evidence_io.save(path, history)
        return fact

    def sample_resources(self, telemetry, deadline, phase):
        try:
            return telemetry.poll(deadline) if telemetry else []
        except Exception as error:
            if isinstance(error, urllib.error.HTTPError) and error.code in (401, 403):
                raise
            phase.setdefault('diagnosticErrors', []).append({'at': evidence_io.now(), 'error': str(error)})
            return []

    def monitor(self, offset, folder):
        self.disk_boundary()
        return offset

    def run(self):
        stage = next(row for row in self.b['schedule'] if row['id'] == self.a.stage)
        folder = Path(self.c['evaluation']['stageFolder'])
        if folder.exists():
            raise RuntimeError('FrozenInputInvalid: no implicit stage overwrite/retry')
        receipt = evidence_io.load(self.out / 'settings-patch.json')
        if not receipt.get('accepted'):
            raise RuntimeError('ControlConflict: settings patch not read back')
        # Provenance labels and shared-generator activity qualify the result;
        # neither is permission to discard an otherwise observable experiment.
        auth = evidence_io.module('evaluation_auth', HERE / 'auth.py')
        budget = auth.lifetime_budget([stage], self.tb, self.c.get('tokenSafetyMarginSeconds', 60))
        evidence_io.save(self.out / 'token-preflight.json', auth.check(self.c['tokenFile'], budget, endpoint=self.c['endpoint'], ca_file=self.c['caFile']))
        folder.mkdir(parents=True)
        phase = {'failurePolicy': 'v2', 'stage': stage, 'status': 'RUNNING', 'starts': {}, 'startUTC': None, 'endUTC': None,
                 'sourceObservations': {'declared': self.b['sources'], 'imageSourceSHA': os.environ.get('CHILL_HARNESS_SOURCE_SHA')},
                 'drain': {'complete': False, 'reasons': []}}
        self.observe_generators(folder)
        jobs, pods, streams, captures, ready, drained = {}, {}, [], {}, {}, set()
        judgment = evidence_io.module('evaluation_capture_judgment', HERE / 'judgment.py')
        phase['producerCapture'] = {name: {'plannedRequests': stage['traces'][name]['offered'], 'receivedRequestRows': 0,
                                         'drained': False, 'collectionLoss': 0}
                                    for name in self.ws if stage['traces'][name]['offered']}
        def read_capture(name, capture):
            try:
                for line in capture.lines():
                    if not line.endswith(b'\n'):
                        phase.setdefault('truncatedLogBytes', {})[name] = base64.b64encode(line).decode()
                        phase['drain']['reasons'].append('TruncatedLogEOF:' + name)
                        continue
                    text = line.decode('utf-8')
                    try:
                        row = json.loads(text)
                        if not isinstance(row, dict):
                            raise ValueError('client JSON row is not an object')
                    except ValueError as error:
                        phase.setdefault('clientLogErrors', []).append({'producer': name, 'error': str(error)})
                        continue
                    if 'scheduledOffsetNs' in row:
                        phase['producerCapture'][name]['receivedRequestRows'] += 1
                    event = row.get('evaluationEvent')
                    if event == 'ready':
                        trace = stage['traces'][name]
                        if name in ready or row['offered'] != trace['offered'] or row['arrivalSHA256'] != trace['arrivalSHA256'] or not row['payload']:
                            raise RuntimeError('IdentityMismatch: producer Ready input identity')
                        ready[name] = row
                        evidence_io.save(folder / ('payload-' + name + '.json'), row)
                    elif event == 'start':
                        if not released or name in phase['starts']:
                            raise RuntimeError('IdentityMismatch: producer start protocol')
                        phase['starts'][name] = row
                    elif event == 'drained':
                        drained.add(name)
                        phase['producerCapture'][name]['drained'] = True
            finally:
                metadata = phase['producerCapture'][name]
                metadata['collectionLoss'] = judgment.collection_loss(metadata['plannedRequests'], metadata['receivedRequestRows'], metadata['drained'])

        deadline = time.monotonic() + self.tb['setupSeconds'] + self.tb['barrierSeconds']
        try:
            for name in self.ws:
                if not stage['traces'][name]['offered']:
                    continue
                job = self.io.k(['create', '-f', '-', '-o', 'json'], stdin=json.dumps(self.job(name, stage, folder)))
                if not self.owned(job):
                    raise RuntimeError('IdentityMismatch: created Job ownership')
                jobs[name] = job
                evidence_io.save(folder / ('created-' + name + '.json'), job)
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
                        evidence_io.save(folder / ('producer-' + name + '.json'), pod)
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError('BarrierTimeout: generator setup')
                    time.sleep(.2)
                error = (folder / (name + '-log.stderr')).open('w')
                process = subprocess.Popen(['/usr/bin/kubectl', '-n', self.c['namespace'], 'logs', '-f', pod['metadata']['name'], '-c', 'load'], stdout=subprocess.PIPE, stderr=error, bufsize=0)
                capture = ProducerLog(process.stdout, folder / (name + '-client.jsonl'))
                streams.append((name, process, capture, error))
                captures[name] = capture
            telemetry = ProducerResources(self.io, pods, self.tb['apiSeconds'], folder / 'producer-resources.json') if pods else None
            provisional = ProvisionalD1(self.io, self.c, folder, self.provisional_callback)
            released = not jobs
            if released:
                phase['startUTC'] = phase['goUTC'] = evidence_io.now()
                phase['endUTC'] = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=stage['seconds'])).isoformat()
                deadline = time.monotonic() + stage['seconds']
                self.record_go(folder, jobs, pods, phase['goUTC'])
            last_producer_poll = None
            while True:
                self.monitor(0, folder)
                if last_producer_poll is None or time.monotonic() - last_producer_poll >= self.tb['flushSeconds']:
                    for name, job in jobs.items():
                        current = self.io.k(['get', 'job', job['metadata']['name'], '-o', 'json'])
                        if current['metadata']['uid'] != job['metadata']['uid']:
                            raise RuntimeError('IdentityMismatch: generator Job UID')
                        if any(c['type'] == 'Failed' and c['status'] == 'True' for c in current.get('status', {}).get('conditions', [])):
                            evidence_io.save(folder / ('terminal-' + name + '.json'), current)
                            raise RuntimeError('ProducerFailed: ' + job['metadata']['name'] + ' (' + name + ')')
                    last_producer_poll = time.monotonic()
                time.sleep(.2)
                for name, capture in captures.items():
                    read_capture(name, capture)
                if not released and len(ready) == len(jobs):
                    for name, pod in pods.items():
                        current = self.io.k(['get', 'pod', pod['metadata']['name'], '-o', 'json'])
                        self.assert_pod(current, pod)
                        if time.time_ns() >= evidence_io.timestamp_ns(ready[name]['at']) + round(ready[name]['barrierSeconds'] * 1e9):
                            raise RuntimeError('BarrierTimeout: Ready expired')
                    phase['goUTC'] = evidence_io.now()
                    for pod in pods.values():
                        self.io.k(['exec', pod['metadata']['name'], '-c', 'load', '--', 'python3', '-c', "from pathlib import Path; Path('/tmp/evaluation-start').touch()"])
                    released = True
                    phase['startUTC'] = phase['goUTC']
                    phase['endUTC'] = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=stage['seconds'])).isoformat()
                    phase['intervalSource'] = 'GO until producer start observations are available'
                    self.record_go(folder, jobs, pods, phase['goUTC'])
                    deadline = time.monotonic() + stage['seconds'] + self.tb['drainSeconds'] + self.tb['collectSeconds']
                if jobs and len(phase['starts']) == len(jobs):
                    phase['startUTC'] = min(phase['starts'].values(), key=lambda row: evidence_io.timestamp_ns(row['at']))['at']
                    end_ns = max(evidence_io.timestamp_ns(row['at']) for row in phase['starts'].values()) + round(stage['seconds'] * 1e9)
                    seconds, nanos = divmod(end_ns, 1000000000)
                    phase['endUTC'] = dt.datetime.fromtimestamp(seconds, dt.timezone.utc).strftime('%Y-%m-%dT%H:%M:%S') + f'.{nanos:09d}+00:00'
                    counters = self.sample_resources(telemetry, deadline, phase)
                    for name in drained:
                        matching = [row for row in counters if row.get('podUID') == pods[name]['metadata']['uid']]
                        if matching:
                            evidence_io.save(folder / (name + '-client-end-counter.json'), matching[-1])
                evidence_io.save(folder / 'phase.json', phase)
                if released and (not jobs or len(phase['starts']) == len(jobs)):
                    provisional.poll(phase)
                if released and not jobs and time.monotonic() >= deadline:
                    break
                if jobs and all(capture.eof() for capture in captures.values()) and all(p.poll() is not None for _, p, _, _ in streams):
                    if not released:
                        raise RuntimeError('NoLoad: producer EOF before GO')
                    if drained != set(jobs) or any(p.returncode for _, p, _, _ in streams):
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
                evidence_io.save(folder / ('terminal-' + name + '.json'), current)
                if any(c['type']=='Failed' and c['status']=='True' for c in current.get('status',{}).get('conditions',[])):
                    phase['drain']['reasons'].append('ProducerJobFailed:' + name)
            phase['drain']['complete'] = not phase['drain']['reasons'] and drained == set(jobs)
            if not phase['drain']['complete']:
                cutoff = evidence_io.now()
                phase['plannedEndUTC'] = phase['endUTC']
                if phase['endUTC'] and evidence_io.timestamp_ns(phase['endUTC']) > evidence_io.timestamp_ns(cutoff):
                    phase['endUTC'] = cutoff
                # Stop only exact recorded evaluator Job UIDs; cleanup failure
                # stays a safety hold, but does not destroy collected facts.
                try:
                    self._cleanup_jobs()
                except Exception as error:
                    phase.setdefault('cleanupErrors', []).append(str(error))
                for _, process, _, _ in streams:
                    if process.poll() is None:
                        process.terminate()
                    process.wait(timeout=self.tb['apiSeconds'])
            capture_errors = {}
            for name, capture in captures.items():
                try:
                    capture.join()
                except Exception as error:
                    capture_errors[name] = str(error)
                try:
                    read_capture(name, capture)
                except Exception as error:
                    capture_errors[name] = str(error)
            if capture_errors:
                phase['captureErrors'] = capture_errors
                raise RuntimeError('; '.join(capture_errors.values()))
            phase['drain']['complete'] = not phase['drain']['reasons'] and drained == set(jobs)
            phase.update(drainedUTC=evidence_io.now(), status='DRAINED_NOT_EVALUATED' if phase['drain']['complete'] else 'PARTIAL_DRAIN_NOT_EVALUATED',
                         drainScope='Drain completion is independent of diagnostic CPU/throttle counters')
            evidence_io.save(folder / 'phase.json', phase)
            time.sleep(self.tb['flushSeconds'])
            archive = evidence_io.module('evaluation_archive', HERE / 'archive.py')
            evidence_io.save(folder / 'collected.json', archive.collect(folder, phase, self.c['collection'], self.io, self.tb['collectSeconds']))
        except Exception as error:
            phase.update(status='STOP', error=str(error), failureUTC=evidence_io.now())
            for name, job in jobs.items():
                try:
                    current = self.io.k(['get', 'job', job['metadata']['name'], '-o', 'json'])
                    if current['metadata']['uid'] != job['metadata']['uid'] or not self.owned(current):
                        raise RuntimeError('ForeignOwnership: producer changed before STOP')
                    evidence_io.save(folder / ('stopped-job-' + name + '.json'), current)
                    self.io.delete_job(job['metadata']['name'], job['metadata']['uid'])
                except Exception as stop_error:
                    phase.setdefault('cleanupErrors', []).append(str(stop_error))
            evidence_io.save(folder / 'phase.json', phase)
            raise
        finally:
            primary_error = sys.exc_info()[0] is not None
            capture_errors = {}
            for name, process, capture, error in streams:
                if process.poll() is None:
                    process.terminate()
                process.wait(timeout=self.tb['apiSeconds'])
                try:
                    capture.close()
                except Exception as failure:
                    capture_errors[name] = str(failure)
                error.close()
            if capture_errors:
                phase.setdefault('captureErrors', {}).update(capture_errors)
                if not primary_error:
                    phase.update(status='STOP', error='; '.join(capture_errors.values()), failureUTC=evidence_io.now())
                evidence_io.save(folder / 'phase.json', phase)
                if not primary_error:
                    raise RuntimeError(phase['error'])

    def record_go(self, folder, jobs, pods, at):
        value = {'goUTC': at, 'producerRefs': [{'workloadUID': self.ws[name]['uid'], 'shard': 0,
                 'jobRef': {key: job['metadata'][key] for key in ('name', 'uid')},
                 'podRef': {key: pods[name]['metadata'][key] for key in ('name', 'uid')}} for name, job in jobs.items()]}
        evidence_io.save(folder / 'go.json', value)
        if self.go_callback:
            self.go_callback(value)

    def cleanup_jobs(self):
        import settings_patch
        # Job cleanup and settings restoration are independent obligations.
        # A failed delete must retain the pool, not retain our control patch.
        attempt = {'startedAt': evidence_io.now(), 'cleanup': {}, 'restore': {'status': 'Complete', 'receipts': []}}
        failure = None
        try:
            self._cleanup_jobs()
            attempt['cleanup'] = {'status': 'Complete'}
        except Exception as error:
            failure = error
            attempt['cleanup'] = {'status': 'Failed', 'error': str(error)}
        attempt['cleanup']['finishedAt'] = evidence_io.now()
        attempt['cleanup']['residuals'] = copy.deepcopy(self.residuals)
        for path in sorted(self.out.rglob('settings-patch.json')):
            receipt = {'path': str(path.relative_to(self.out)), 'startedAt': evidence_io.now()}
            try:
                settings_patch.factor_revert(self.api, path)
                receipt['status'] = 'Complete'
            except Exception as error:
                if failure is None:
                    failure = error
                receipt.update(status='Failed', error=str(error))
                attempt['restore']['status'] = 'Failed'
                residual = {'kind': 'SchedulerSettings', 'name': settings_patch.SETTINGS, 'reason': 'RemoteEffectUnknown'}
                if residual not in self.residuals:
                    self.residuals.append(residual)
            receipt['finishedAt'] = evidence_io.now()
            attempt['restore']['receipts'].append(receipt)
        attempt['finishedAt'] = evidence_io.now()
        path = self.out / 'cleanup-outcome.json'
        evidence = evidence_io.load(path) if path.exists() else {'attempts': []}
        evidence['attempts'].append(attempt)
        evidence_io.save(path, evidence)
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
                    for record in [evidence_io.load(path)] if record.get('kind') == 'Job'}
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
        # Cleanup and the lost-writer check are independent safety obligations.
        children = self.c['evaluation']['execution'].get('childRefs', [])
        uncertain = any(child.get('uncertain') for child in children)
        for child in children:
            for record in child.get('injectionRecords') or []:
                if record['outcome'] in ('Unknown', 'Requested'):
                    for key, kind in (('nodeRef', 'Node'), ('podRef', 'Pod')):
                        if record.get(key):
                            self.residuals.append({'kind': kind, 'name': record[key]['name'], 'reason': 'RemoteEffectUnknown'})
        try:
            self.cleanup_jobs()
        finally:
            self.observe_stage_writers()
        if uncertain:
            raise RuntimeError('RemoteEffectUnknown: unresolved child or injection effects')
        if self.residuals:
            raise RuntimeError('ResidualState: earlier evaluation harness writers remain')

    def observe_stage_writers(self):
        execution = self.c['evaluation']['execution']
        procedures = [p for child in execution.get('childRefs', []) for p in child.get('procedures') or []]
        recorded_jobs = {p['jobRef']['uid']: p['jobRef'] for p in procedures if p.get('jobRef')}
        recorded_pods = {p['podRef']['uid']: p['podRef'] for p in procedures if p.get('podRef')}
        selector = 'edge.dacs.io/workload-role=system,edge.dacs.io/evaluation-run-uid=' + execution['runUID']
        deadline = time.monotonic() + self.tb['drainSeconds']
        while True:
            jobs = [job for job in self.io.k(['get', 'jobs', '-l', selector, '-o', 'json'])['items'] if self.owned(job)]
            job_uids = set(recorded_jobs) | {job['metadata']['uid'] for job in jobs}
            pods = [pod for pod in self.io.k(['get', 'pods', '-l', selector, '-o', 'json'])['items']
                    if pod['metadata']['uid'] in recorded_pods or any(
                        owner.get('kind') == 'Job' and owner.get('uid') in job_uids
                        for owner in pod['metadata'].get('ownerReferences', []))]
            residuals = [{'kind': 'Pod', 'name': ref['name'], 'reason': 'RemoteEffectUnknown'}
                         for uid, ref in recorded_pods.items() if not any(pod['metadata']['uid'] == uid for pod in pods)]
            for uid in job_uids:
                if not any(any(owner.get('uid') == uid for owner in pod['metadata'].get('ownerReferences', [])) for pod in pods):
                    ref = recorded_jobs.get(uid) or next(job['metadata'] for job in jobs if job['metadata']['uid'] == uid)
                    residuals.append({'kind': 'Job', 'name': ref['name'], 'reason': 'RemoteEffectUnknown'})
            for pod in pods:
                statuses = pod.get('status', {}).get('containerStatuses', [])
                terminated = bool(statuses) and all('terminated' in status.get('state', {}) for status in statuses)
                node = pod.get('spec', {}).get('nodeName')
                ready = False
                if node:
                    try:
                        observed = self.io.k(['get', 'node', node, '-o', 'json'])
                        ready = any(c['type'] == 'Ready' and c['status'] == 'True' for c in observed.get('status', {}).get('conditions', []))
                    except Exception as error:
                        residuals.append({'kind': 'Node', 'name': node, 'reason': 'RemoteEffectUnknown', 'error': str(error)})
                if not terminated or not ready:
                    residuals.append({'kind': 'Pod', 'name': pod['metadata']['name'],
                                      'reason': 'ResidualState' if ready else 'RemoteEffectUnknown'})
            if not residuals:
                return
            if time.monotonic() >= deadline:
                self.residuals.extend(residuals)
                return
            time.sleep(.2)


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
        evidence_io.save(args.out, bind(args.config))
    elif args.command == 'judge':
        import judgment
        return 2 if judgment.judge(args.binding, args.out)['components']['slo']['status'] in ('INVALID_NO_LOAD','NOT_EVALUATED') else 0
    else:
        print(evidence_io.module('evaluation_tables', HERE / 'tables.py').render(args.out, REPO))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
