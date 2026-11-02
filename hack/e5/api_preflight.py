#!/usr/bin/env python3
"""Read-only E5 API preflight against the live entrypoint (stdlib + kubectl/curl).

Sources: e5.py CPO.api/state/settings/operation/factor; evaluation.py prepare;
producer Kubernetes verbs: evaluation_rbac.yaml and e5 producer Job adapter.
Operations and settings do NOT support dryRun (httpapi/operations.go and
scheduler.go): only SelfSubjectAccessReview via kubectl auth can-i is sent.
No operation, settings write, producer creation, or node mutation is submitted.
Run: python3 hack/e5/api_preflight.py
"""
import base64
import datetime as dt
import json
import subprocess
import tempfile
from pathlib import Path
from urllib.parse import urlencode


def command(*args):
    return subprocess.run(args, capture_output=True, text=True, timeout=60)


def kube(*args):
    result = command('kubectl', *args)
    if result.returncode:
        raise RuntimeError(result.stderr.strip())
    return json.loads(result.stdout)


def table(headers, rows):
    print('| ' + ' | '.join(headers) + ' |')
    print('|' + '|'.join('---' for _ in headers) + '|')
    for row in rows:
        print('| ' + ' | '.join(str(v).replace('|', '/').replace('\n', ' ') for v in row) + ' |')


def main():
    namespace = 'chill-system'
    service = kube('get', 'service', 'chill-serving-entrypoint', '-n', namespace, '-o', 'json')
    deployment = kube('get', 'deployment', 'chill-serving-entrypoint', '-n', namespace, '-o', 'json')
    container = deployment['spec']['template']['spec']['containers'][0]
    scheduler = next((arg.split('=', 1)[1] for arg in container.get('args', [])
                      if arg.startswith('--serving-scheduler-configmap=')), 'serving-scheduler')
    tls_volume = next(v for v in deployment['spec']['template']['spec']['volumes'] if v['name'] == 'tls')
    secret = kube('get', 'secret', tls_volume['secret']['secretName'], '-n', namespace, '-o', 'json')
    issued = command('kubectl', 'create', 'token', 'chill-evaluation-action', '-n', namespace, '--duration=10m')
    if issued.returncode:
        raise RuntimeError(issued.stderr.strip())
    token = issued.stdout.strip()
    now = dt.datetime.now(dt.timezone.utc)
    stamp = lambda value: value.isoformat().replace('+00:00', 'Z')
    rows = []
    with tempfile.TemporaryDirectory(prefix='chill-api-preflight-') as temporary:
        root = Path(temporary)
        ca = root / 'ca.crt'
        ca.write_bytes(base64.b64decode(secret['data']['tls.crt']))
        config = kube('config', 'view', '--raw', '--minify', '-o', 'json')
        config['users'] = [{'name': 'preflight', 'user': {'token': token}}]
        config['contexts'][0]['context']['user'] = 'preflight'
        credentials = root / 'kubeconfig'
        credentials.write_text(json.dumps(config))
        credentials.chmod(0o600)

        def permission(verb, target, *extra):
            result = command('kubectl', '--kubeconfig', str(credentials), 'auth', 'can-i', verb, target, *extra)
            return result.stdout.strip() or result.stderr.strip()

        def get(path):
            host = 'chill-serving-entrypoint.chill-system.svc'
            port = next(p['port'] for p in service['spec']['ports'] if p['name'] == 'https')
            url = f'https://{host}:{port}{path}'
            # Header travels over stdin, never command arguments or report output.
            result = subprocess.run(['curl', '--silent', '--show-error', '--noproxy', '*',
                '--max-time', '45', '--cacert', str(ca), '--resolve',
                f'{host}:{port}:{service["spec"]["clusterIP"]}', '--config', '-',
                '--write-out', '\n%{http_code}', url], input='header = "Authorization: Bearer '+token+'"\n',
                capture_output=True, text=True, timeout=50)
            body, _, status = result.stdout.rpartition('\n')
            note = 'live GET; TLS verified'
            if result.returncode:
                note = result.stderr.strip()
            elif not status.startswith('2'):
                note = body[:250]
            rows.append((path, 'GET', status or 'transport error', note))
            try:
                return json.loads(body)
            except ValueError:
                return None

        state = get('/api/v1/scheduler/state')
        for path in ('/api/v1/scheduler/settings', '/api/v1/scheduler',
                     '/api/v1/scheduler/profiles', '/api/v1/pools', '/api/v1/workloads'):
            get(path)
        if isinstance(state, dict):
            for workload in state.get('snapshot', {}).get('workloads', []):
                from urllib.parse import quote
                get('/api/v1/workloads/' + quote(workload['name'], safe=''))
        get('/api/v1/observations/raw?' + urlencode({'hour': now.strftime('%Y%m%dT%H')}))
        get('/api/v1/observations/load?' + urlencode({'from': stamp(now-dt.timedelta(seconds=30)), 'to': stamp(now)}))
        config_permission = permission('update', 'configmaps/'+scheduler, '-n', namespace)
        for kind in ('reset', 'unroute', 'prepare', 'route'):
            allowed = permission('post', '/chill/api/v1/scheduler/operations')
            rows.append(('/api/v1/scheduler/operations ('+kind+')', 'POST',
                         'RBAC '+allowed, 'not sent: no dryRun; ConfigMap update='+config_permission))
        rows.append(('/api/v1/scheduler/settings', 'PUT',
                     'RBAC '+permission('put', '/chill/api/v1/scheduler/settings'),
                     'not sent: no dryRun; ConfigMap update='+config_permission))
        for resource in ('jobs.batch', 'pods'):
            result = command('kubectl', '--kubeconfig', str(credentials), '-n', namespace,
                             'get', resource, '-o', 'json')
            note = result.stderr.strip()
            if result.returncode == 0:
                note = str(len(json.loads(result.stdout).get('items', [])))+' objects; read-only list'
            rows.append(('producer '+resource, 'GET', 'OK' if result.returncode == 0 else 'ERROR', note))
        for resource, verbs in (('jobs.batch', ('create', 'get', 'list', 'watch', 'delete')),
                                ('pods', ('get', 'list', 'watch')),
                                ('pods/log', ('get',)), ('pods/exec', ('create',))):
            for verb in verbs:
                resource_name, _, subresource = resource.partition('/')
                extra = ('--subresource='+subresource,) if subresource else ()
                rows.append(('producer '+resource, verb, 'RBAC '+permission(verb, resource_name, '-n', namespace, *extra),
                             'permission only; no producer/exec submitted'))
    print('Captured UTC: '+stamp(now)+'; SA: system:serviceaccount:chill-system:chill-evaluation-action')
    print('Source inventory: hack/e5/e5.py, evaluation.py, charts/chill/templates/evaluation_rbac.yaml')
    table(('API', 'Method', 'Result', 'Notes'), rows)
    nodes = kube('get', 'nodes', '-o', 'json')['items']
    events = kube('get', 'events', '-A', '--field-selector', 'involvedObject.kind=Node', '-o', 'json')['items']
    node_rows = []
    cutoff = now-dt.timedelta(hours=24)
    for node in nodes:
        name = node['metadata']['name']
        ready = next((c for c in node['status'].get('conditions', []) if c['type'] == 'Ready'), {})
        count = 0
        for event in events:
            if event.get('involvedObject', {}).get('name') != name or event.get('reason') not in ('NodeNotReady', 'NotReady'):
                continue
            series = event.get('series', {})
            timestamp = series.get('lastObservedTime') or event.get('lastTimestamp') or event.get('eventTime') or event['metadata']['creationTimestamp']
            if dt.datetime.fromisoformat(timestamp.replace('Z', '+00:00')) >= cutoff:
                count += series.get('count', event.get('count', 1))
        node_rows.append((name, ready.get('status', 'Unknown'), ready.get('lastTransitionTime', ''), count))
    table(('Node', 'Ready', 'Last transition', 'NotReady retained event count (24h)'), node_rows)
    print('Node/event inventory uses operator read credentials. Event counts sum retained event-series counts with last occurrence in 24h; Kubernetes event TTL/aggregation prevents a complete 24h transition history.')


if __name__ == '__main__':
    main()
