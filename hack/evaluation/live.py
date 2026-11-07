#!/usr/bin/env python3
"""In-Run diagnostic watcher; all evidence stays under the step root."""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

import input_snapshot

EXECUTOR_PREFIX = 'chill-executor-'


def timeboxes(context):
    if not hasattr(context, 'snapshot'):
        return {**input_snapshot.TIMEBOXES, **json.loads(context.config['operational']).get('timeboxes', {})}
    stage = context.execution.get('stageID') or context.snapshot['runSpec']['stages'][0]['id']
    return context.snapshot['bindings'][stage]['config']['timeboxes']


def ready_routed(state):
    ready = {i['podUID'] for i in state['snapshot'].get('instances') or [] if i.get('ready')}
    routed = {i.split('/')[0] for w in (state['decision']['keep'].get('routing') or {}).values()
              for i, weight in w.items() if weight > 0}
    return ready, routed


def health(service):
    try:
        spec = service['spec']
        ip = spec['clusterIP']
        if ':' in ip:
            ip = '[' + ip + ']'
        opener = urllib.request.build_opener(input_snapshot.AUTH.NoRedirect())
        with opener.open(f"http://{ip}:{spec['ports'][0]['port']}/v2/health/ready", timeout=5) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code
    except Exception as error:
        return type(error).__name__ + ':' + str(error)[:80]


def start_watcher(context, step):
    return subprocess.Popen([sys.executable, str(Path(__file__).resolve()), '--out', str(context.root),
                             '--step', step, '--namespace', context.execution['namespace'],
                             '--api-url', context.config['chillAPIURL'],
                             '--watch-seconds', str(timeboxes(context)['watchSeconds']),
                             '--parent-pid', str(os.getpid())], start_new_session=True)


def stop_watcher(context, process):
    """A slow diagnostic cycle never delays the step: after apiSeconds the group is killed."""
    process.terminate()
    try:
        process.wait(timeout=timeboxes(context)['apiSeconds'])
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()


def append(path, value):
    with path.open('a') as stream:
        stream.write(json.dumps(value, allow_nan=False) + '\n')


class Watcher:
    def __init__(self, out, step, config, namespace, parent_pid):
        self.config, self.namespace, self.parent_pid = config, namespace, parent_pid
        folder = out / 'watch'
        folder.mkdir(parents=True, exist_ok=True)
        self.rows, self.alerts = folder / (step + '.jsonl'), folder / (step + '-alerts.jsonl')
        self.rows.touch(); self.alerts.touch()
        self.first_seen_action, self.held_since, self.boot, self.seen_alerts = {}, {}, {}, set()

    def alert(self, key, detail):
        if key in self.seen_alerts:
            return
        value = {'at': input_snapshot.now(), 'key': list(key), 'detail': detail}
        append(self.alerts, value)
        self.seen_alerts.add(key)
        print('EVALUATION_ALERT ' + json.dumps(value), flush=True)

    def scheduler(self, row, instant):
        state = input_snapshot.api(self.config, '/api/v1/scheduler')['state']
        decision = state['decision']
        row['decisionAt'], row['status'] = decision.get('at'), decision.get('status')
        workloads = {}
        for workload in decision.get('workloads') or []:
            uid, status, reason = workload['uid'], workload['state'], workload.get('reason')
            workloads[uid[:8]] = {'state': status, 'reason': reason}
            if status in ('Fixed', 'Pending'):
                since = self.held_since.setdefault(uid, instant)
                held = (instant - since).total_seconds()
                workloads[uid[:8]]['heldSeconds'] = round(held)
            else:
                self.held_since.pop(uid, None)
        row['workloads'] = workloads
        loads = {(load.get('workloadUID') or load.get('uid') or '?')[:8]:
                 {'inputsPerSecond': load.get('inputsPerSecond'), 'recentInputsPerSecond': load.get('recentInputsPerSecond')}
                 for load in (decision.get('search') or {}).get('loads') or []}
        row['loads'] = loads
        rising = [uid for uid, value in loads.items()
                  if (value['recentInputsPerSecond'] or 0) > (value['inputsPerSecond'] or 0) * 1.05]
        actions = []
        for action in state.get('actions') or []:
            aid = action.get('id', '')
            if action.get('kind') in ('remove', 'power') and action.get('status') != 'complete' and rising:
                self.alert(('actionOnRisingLoad', aid[:60]), f'{aid} while recent load rising for {rising}')
            first = self.first_seen_action.setdefault(aid, instant)
            actions.append({'kind': action.get('kind'), 'node': (action.get('node') or {}).get('name'),
                            'instance': str(action.get('instance', ''))[:8], 'status': action.get('status'),
                            'from': action.get('from'), 'to': str(action.get('to', ''))[:40],
                            'seenSeconds': round((instant - first).total_seconds())})
            if action.get('status') in ('failed', 'partial'):
                self.alert(('action', action.get('kind'), (action.get('node') or {}).get('name'), action.get('status')),
                           json.dumps(actions[-1]))
        row['actions'] = actions
        instances = snapshot.get('instances') or []
        ready, routed = ready_routed(state)
        row['instances'] = {'total': len(instances), 'ready': len(ready), 'routable': len(routed & ready),
                            'replacementCandidates': sum(1 for instance in instances if instance.get('replacementReason'))}
        executors = {}
        for service in input_snapshot.kubectl(self.namespace, 'get', 'services')['items']:
            name = service['metadata']['name']
            if not name.startswith(EXECUTOR_PREFIX):
                continue
            uid = name[len(EXECUTOR_PREFIX):]
            if uid in routed and uid in ready:
                code = health(service)
                if isinstance(code, str):
                    code = code.split(':', 1)[0]
                executors[uid[:8]] = code
                if code != 200:
                    self.alert(('executor', uid[:8], str(code)), f'routable executor {uid} -> {code}')
        row['executors'] = executors

    def kube(self, row, instant):
        nodes = {}
        for node in input_snapshot.kubectl(self.namespace, 'get', 'nodes')['items']:
            name = node['metadata']['name']
            ready = next((c['status'] for c in node['status']['conditions'] if c['type'] == 'Ready'), '?')
            boot = node['status']['nodeInfo']['bootID']
            nodes[name] = ready
            if ready != 'True':
                self.alert(('node', name, 'NotReady', boot[:8]), f'{name} Ready={ready}')
            if name in self.boot and self.boot[name] != boot:
                self.alert(('node', name, 'reboot', boot[:8]), f'{name} bootID {self.boot[name][:8]} -> {boot[:8]}')
            self.boot[name] = boot
        row['nodes'] = nodes
        pods = []
        for pod in input_snapshot.kubectl(self.namespace, 'get', 'pods')['items']:
            if not pod['metadata']['name'].startswith('serving-'):
                continue
            for container in pod['status'].get('containerStatuses') or []:
                state = container.get('state') or {}
                reason = (state.get('terminated') or state.get('waiting') or {}).get('reason')
                last = ((container.get('lastState') or {}).get('terminated') or {}).get('reason')
                if container.get('restartCount') or reason or last:
                    pods.append({'pod': pod['metadata']['name'][8:20], 'node': pod['spec'].get('nodeName'),
                                 'restarts': container.get('restartCount'), 'reason': reason, 'last': last})
                for issue in (reason, last):
                    if issue in ('OOMKilled', 'CrashLoopBackOff', 'Error'):
                        self.alert(('pod', issue, pod['spec'].get('nodeName'), pod['metadata']['uid'][:8]),
                                   f"{pod['metadata']['name']} on {pod['spec'].get('nodeName')}: {issue}")
        row['servingPodIssues'] = pods
        jobs = {}
        for job in input_snapshot.kubectl(self.namespace, 'get', 'jobs')['items']:
            name = job['metadata']['name']
            if not name.startswith('load-'):
                continue
            conditions = {c['type']: c['status'] for c in job['status'].get('conditions') or []}
            status = 'Failed' if conditions.get('Failed') == 'True' else 'Complete' if conditions.get('Complete') == 'True' else 'Running'
            jobs[name] = status
            if status == 'Failed':
                self.alert(('loadJob', name, 'Failed'), f'load generator Job {name} Failed')
        row['loadJobs'] = jobs

    def power(self, row, instant):
        start = (instant - dt.timedelta(seconds=120)).strftime('%Y-%m-%dT%H:%M:%SZ')
        end = instant.strftime('%Y-%m-%dT%H:%M:%SZ')
        power = input_snapshot.api(self.config, f'/api/v1/observations/power?from={start}&to={end}')
        row['power'] = {node['node']['name']: {key: node.get(key) for key in
                        ('meterComplete', 'gapCount', 'maximumSampleGapSeconds')} for node in power.get('nodes') or []}

    def harness(self, row, instant):
        status = Path(f'/proc/{self.parent_pid}/status').read_text().splitlines()
        values = {key: int(value.split()[0]) for line in status for key, value in [line.split(':', 1)]
                  if key in ('VmRSS', 'VmHWM')}
        row['harness'] = {'vmRSSKiB': values['VmRSS'], 'vmHWMKiB': values['VmHWM']}

    def cycle(self):
        instant = dt.datetime.now(dt.timezone.utc)
        row = {'at': instant.isoformat()}
        for name in ('scheduler', 'kube', 'power', 'harness'):
            try:
                getattr(self, name)(row, instant)
            except Exception as error:
                row[name + 'Error'] = f'{type(error).__name__}: {error}'[:200]
        append(self.rows, row)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--step', choices=['stage', 'finish'], required=True)
    parser.add_argument('--namespace', required=True)
    parser.add_argument('--api-url', required=True)
    parser.add_argument('--watch-seconds', type=float, required=True)
    parser.add_argument('--parent-pid', type=int, required=True)
    args = parser.parse_args()
    stopped = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopped.set())
    signal.signal(signal.SIGINT, lambda *_: stopped.set())
    watcher = Watcher(args.out, args.step, {'chillAPIURL': args.api_url}, args.namespace, args.parent_pid)
    while not stopped.is_set():
        started = time.monotonic()
        watcher.cycle()
        stopped.wait(max(0, args.watch_seconds - (time.monotonic() - started)))


if __name__ == '__main__':
    main()
