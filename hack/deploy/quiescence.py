"""Temporarily release source-defined GPU probe nodes through the public API."""
from contextlib import contextmanager
from decimal import Decimal
import json
from pathlib import Path
import ssl
import sys
import time
import urllib.request

import api_checks
import runtime


def _allocations(document, nodes):
    """Include deleting Pods until Kubernetes reports their GPU allocation gone."""
    allocations = []
    for pod in document['items']:
        spec = pod.get('spec', {})
        if spec.get('nodeName') not in nodes or pod.get('status', {}).get('phase') in ('Succeeded', 'Failed'):
            continue
        containers = spec.get('containers', []) + spec.get('initContainers', [])
        if any(Decimal(str(container.get('resources', {}).get(field, {}).get('nvidia.com/gpu', 0))) > 0
               for container in containers for field in ('requests', 'limits')):
            meta = pod['metadata']
            allocations.append({'podUID': meta['uid'], 'podName': meta['name'],
                                'namespace': meta['namespace'], 'node': spec['nodeName']})
    return allocations


def _matched(allocations, state, namespace):
    matched = []
    for pod in allocations:
        instances = [instance for instance in (state.get('instances') or [])
                     if instance.get('podUID') == pod['podUID']
                     and instance.get('podName') == pod['podName']
                     and instance.get('node', {}).get('name') == pod['node']]
        if pod['namespace'] != namespace or len(instances) != 1:
            raise RuntimeError('GPU probe blocked by foreign or unmatched Pod: '
                               + pod['namespace'] + '/' + pod['podName'] + ' UID=' + pod['podUID'])
        instance = instances[0]
        owner = instance.get('ownerWorkloadUID') or instance.get('workloadUID')
        if not owner or not instance.get('id') or not instance['node'].get('uid'):
            raise RuntimeError('GPU probe blocked: CHILL instance lacks exact removal identity for Pod '
                               + pod['podUID'])
        matched.append({'pod': pod, 'instance': instance['id'], 'node': instance['node'],
                        'workloadUID': owner})
    return matched


@contextmanager
def gpu_probes(config, nodes, out, kube, run, timeout):
    """Exclude probe nodes, remove their owned consumers, then restore settings."""
    targets = {node['name'] for node in nodes if node['gpuRequired']}
    if not targets:
        yield
        return
    namespace = runtime._argument(kube, ('--namespace', '-n'))
    out = Path(out) / 'runtime-quiescence'
    out.mkdir(parents=True, exist_ok=True, mode=0o700)
    evaluation = str(Path(__file__).resolve().parents[1] / 'evaluation')
    sys.path.insert(0, evaluation)
    try:
        import settings_patch
    finally:
        sys.path.pop(0)
    origin = api_checks._origin(config['apiBase'])
    opener = urllib.request.build_opener(
        api_checks.auth.NoRedirect(), urllib.request.HTTPSHandler(
            context=ssl.create_default_context(cafile=str(config['caFile']))))
    deadline = time.monotonic() + timeout
    sequence = 0

    def remaining():
        seconds = deadline - time.monotonic()
        if seconds <= 0:
            raise TimeoutError('GPU probe quiescence deadline exceeded; inspect runtime-quiescence evidence')
        return seconds

    def api(method, route, payload=None, headers=None, response=None):
        nonlocal sequence
        sequence += 1
        path = out / ('%03d-api.json' % sequence)
        headers = dict(headers or {})
        api_checks._report(path.with_suffix('.request.json'), {
            'method': method, 'route': route, 'body': payload,
            'ifMatch': headers.get('If-Match')})
        token = api_checks._token(config)
        headers['Authorization'] = 'Bearer ' + token
        data = None
        if payload is not None:
            headers['Content-Type'] = 'application/json'
            data = json.dumps(payload).encode()
        request = urllib.request.Request(origin + route, data=data, headers=headers, method=method)
        status, received_headers, body = api_checks._fetch(request, opener, path, remaining())
        etag = received_headers.get('ETag')
        api_checks._report(path.with_suffix('.response.json'), {'statusCode': status, 'etag': etag})
        if response is not None:
            response.update(statusCode=status, etag=etag, body=body.decode('utf-8', errors='replace'))
        if status != 200:
            try:
                refusal = json.loads(body)
                detail = '%s: %s' % (refusal.get('code', ''), refusal.get('message', ''))
            except (ValueError, AttributeError):
                detail = 'response is not a public JSON error'
            detail = detail.replace(token, '[REDACTED]')
            raise RuntimeError('GPU probe quiescence API ' + method + ' ' + route
                               + ' returned HTTP ' + str(status) + ': ' + detail
                               + '; evidence=' + str(path))
        value = json.loads(body)
        if response is not None:
            response['body'] = value
        return value

    def pods():
        nonlocal sequence
        sequence += 1
        name = '%03d-pods.json' % sequence
        seconds = remaining()
        raw = run(list(kube) + ['--request-timeout=%ss' % seconds,
                                'get', 'pods', '--all-namespaces', '-o', 'json'],
                  'runtime-quiescence-' + name, timeout=seconds)
        api_checks._save(out / name, raw.encode() if isinstance(raw, str) else raw)
        return _allocations(json.loads(raw), targets)
    receipt = out / 'settings-patch.json'
    failure = None
    try:
        state = api('GET', '/api/v1/scheduler/state')
        initial = _matched(pods(), state, namespace)
        owners = {match['workloadUID'] for match in initial}
        api_checks._report(out / 'consumers-before.json', initial)
        declared = {}

        def apply_api(method, route, *args, **kwargs):
            value = api(method, route, *args, **kwargs)
            # Derive array replacements from the helper's own ETag-bound GET,
            # not an earlier snapshot that might overwrite concurrent members.
            if method == 'GET' and not declared:
                excluded = value.get('excludedNodes') or []
                declared.update(excludedNodes=excluded + sorted(targets - set(excluded)),
                                autonomousWorkloads=[uid for uid in value.get('autonomousWorkloads') or []
                                                     if uid not in owners])
            return value

        settings_patch.factor_apply(apply_api, declared, receipt)
        state = api('GET', '/api/v1/scheduler/state')
        current = _matched(pods(), state, namespace)
        api_checks._report(out / 'consumers-after-settings.json', current)
        if any(match['workloadUID'] not in owners for match in current):
            raise RuntimeError('GPU probe blocked by a new workload consumer after settings apply: '
                               + json.dumps(current) + '; evidence=' + str(out))
        removed = set()
        for match in current:
            operation = {'kind': 'remove', 'workloadUID': match['workloadUID'],
                         'node': match['node'], 'instance': match['instance'],
                         'reason': 'runtime probe quiescence'}
            pod = match['pod']
            identity = '%s/%s UID=%s node=%s instance=%s owner=%s' % (
                pod['namespace'], pod['podName'], pod['podUID'], pod['node'],
                match['instance'], match['workloadUID'])
            try:
                result = api('POST', '/api/v1/scheduler/operations', operation)
                if result.get('status') != 'complete' or result.get('error'):
                    raise RuntimeError('remove status=%s error=%s' % (
                        result.get('status'), result.get('error')))
            except Exception as error:
                raise RuntimeError('GPU probe remove refused for ' + identity + ': ' + str(error)
                                   + '; evidence=' + str(out)) from error
            removed.add(match['pod']['podUID'])
        while True:
            allocated = pods()
            if not allocated:
                break
            if any(pod['podUID'] not in removed for pod in allocated):
                raise RuntimeError('GPU probe blocked by a new GPU consumer during resource release: '
                                   + json.dumps(allocated) + '; evidence=' + str(out))
            time.sleep(min(1, remaining()))
        api_checks._report(out / 'released.json', {'nodes': sorted(targets), 'podUIDs': sorted(removed)})
        yield
    except BaseException as error:
        failure = error
        api_checks._report(out / 'failure.json', {'type': type(error).__name__, 'error': str(error)})
        raise
    finally:
        # A failed preparation/probe must not consume the restoration deadline.
        deadline = time.monotonic() + timeout
        try:
            settings_patch.factor_revert(api, receipt)
            restored = json.loads(receipt.read_text()) if receipt.exists() else {}
            outcome = 'completed' if restored.get('revertedAt') else 'not required'
            print('GPU probe settings restoration: %s; skipped tracked paths=%s; evidence=%s' % (
                outcome, len(restored.get('revertSkipped', [])), out), file=sys.stderr)
        except BaseException as error:
            api_checks._report(out / 'restoration-failure.json',
                               {'type': type(error).__name__, 'error': str(error)})
            print('GPU probe settings restoration FAILED: %s; evidence=%s' % (error, out),
                  file=sys.stderr)
            if failure is not None:
                raise RuntimeError('GPU probe quiescence failed: %s; settings restoration also failed: %s'
                                   % (failure, error)) from error
            raise
