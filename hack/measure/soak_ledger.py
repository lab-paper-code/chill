#!/usr/bin/env python3
"""Manual ledger soak; --self-check is local and never contacts the cluster."""
import argparse
from collections import Counter
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import ssl
import time
from types import SimpleNamespace

_parser_spec = importlib.util.spec_from_file_location(
    'soak_timestamp_parser', Path(__file__).resolve().parents[1] / 'e5/d2/analyze_before.py')
_parser = importlib.util.module_from_spec(_parser_spec)
_parser_spec.loader.exec_module(_parser)


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')

def timestamp(value):
    parsed = _parser.epoch(value)
    if parsed is None:
        raise RuntimeError('Missing/invalid observation/ACK RFC3339Nano timestamp')
    return parsed


def profile_id(instance):
    return (instance.get('profile') or {}).get('id') or instance.get('profileID')



def ids(value):
    if isinstance(value, dict):
        return ({value['id']} if isinstance(value.get('id'), str) else set()).union(*(ids(v) for v in value.values()))
    if isinstance(value, list):
        return set().union(*(ids(v) for v in value))
    return set()


class Invariants:
    def __init__(self):
        self.previous = {}
        self.removed = set()
        self.pending = set()
        self.native_unavailable = Counter()

    def check(self, state, pods, pools):
        instances = state['snapshot'].get('instances') or []
        by_uid = {p['metadata']['uid']: p for p in pods['items']}
        violations = []
        for instance in instances:
            pod = by_uid.get(instance.get('podUID'), {})
            ready = any(c.get('type') == 'Ready' and c.get('status') == 'True'
                        for c in pod.get('status', {}).get('conditions', []))
            if self.previous.get(instance['id']) is True and instance.get('ready') is False and ready:
                reasons = instance.get('reasons') or instance.get('reason') or instance.get('failure')
                if reasons:
                    self.native_unavailable[instance['id']] += 1
                else:
                    violations.append({'kind': 'ready-flicker', 'instance': instance['id']})
        for identity in sorted(self.removed & (ids(instances) | ids(pools))):
            violations.append({'kind': 'removed-reappeared', 'instance': identity})
        observed = {i['podUID'] for i in instances if i.get('podUID')}
        # Confirm each differing UID in the following cycle, allowing create/delete propagation.
        difference = observed ^ set(by_uid)
        confirmed = difference & self.pending
        if confirmed:
            violations.append({'kind': 'pod-ledger-mismatch', 'uids': sorted(confirmed),
                               'stateUIDs': sorted(observed), 'podUIDs': sorted(by_uid)})
        self.pending = difference
        self.previous = {i['id']: i.get('ready') for i in instances}
        return violations


def run(client, args, namespace):
    out = args.out
    out.mkdir(parents=True, exist_ok=True)
    guard = Invariants()
    counts = Counter()
    timings = []
    cycles = 0
    operations = []
    active = list(args.instances)
    replacement = None
    replaced = False
    start = time.monotonic()
    deadline = start + args.duration
    fence = None

    def fresh_state():
        until = time.monotonic() + 129
        while True:
            state = client.state()
            snapshot = state['snapshot']
            if fence is None or timestamp(snapshot.get('observedAt') or snapshot.get('at')) > fence:
                return state
            if time.monotonic() >= until:
                raise RuntimeError('No scheduler snapshot newer than completed operation ACK within 129s')
            time.sleep(1)

    def operation(body):
        nonlocal fence
        body = dict(body, reason='Manual ledger soak (autonomy disabled)')
        begun = time.monotonic()
        receipt = {'request': body}
        try:
            receipt['ack'] = client.api('POST', '/api/v1/scheduler/operations', body, timeout=1800)
            ack = receipt['ack']
            if ack.get('status') != 'complete' or ack.get('error'):
                raise RuntimeError('Operation ACK not complete')
            fence = timestamp(ack.get('completedAt') or ack.get('endedAt'))
        except Exception as error:
            receipt['error'] = str(error)
            raise
        finally:
            receipt['seconds'] = time.monotonic() - begun
            timings.append(receipt['seconds'])
            operations.append(receipt)
            save(out / ('operation-%05d.json' % len(timings)), receipt)
        fresh_state()
        return ack

    def observe():
        state = fresh_state()
        pods = client.k(['get', 'pods', '-n', namespace, '-l', 'edge.dacs.io/serving-pod-uid', '-o', 'json'])
        pools = client.api('GET', '/api/v1/pools')
        if fence is not None and timestamp(pools.get('observedAt')) <= fence:
            raise RuntimeError('POOL observation is not newer than completed operation ACK')
        return state, pods, pools

    initial, pods, pools = observe()
    selected = {i['id']: i for i in initial['snapshot'].get('instances') or [] if i['id'] in active}
    if len(selected) != len(active) or any(not i.get('ready') for i in selected.values()):
        raise RuntimeError('Select 2–3 existing Ready manually prepared instances')
    owners = {i.get('ownerWorkloadUID') for i in selected.values()}
    if owners != {args.workload_uid}:
        raise RuntimeError('Selected instances must be owned by --workload-uid')
    template = selected[active[-1]]
    profile = profile_id(template)
    if not profile or not template.get('node'):
        raise RuntimeError('Replacement requires original profile.id (or profileID) and node identity')
    guard.check(initial, pods, pools)
    save(out / 'initial.json', dict(state=initial, pods=pods, pools=pools))
    try:
        while time.monotonic() < deadline:
            operations = []
            violations = []
            state = pods = pools = None
            try:
                settings = client.settings()
                if settings.get('autonomousWorkloads'):
                    raise RuntimeError('Autonomous workloads must remain disabled')
                if not replaced and time.monotonic() - start >= args.duration / 2:
                    operation({'kind': 'unroute', 'workloadUID': args.workload_uid})
                    old = active[-1]
                    operation({'kind': 'remove', 'workloadUID': args.workload_uid,
                               'instance': old, 'node': template['node']})
                    guard.removed.add(old)
                    active.pop()
                    operation({'kind': 'prepare', 'workloadUID': args.workload_uid,
                               'profileID': profile, 'node': template['node']})
                    replacement = set(selected)
                    replaced = True
                state, pods, pools = observe()
                if replacement is not None:
                    candidates = [i['id'] for i in state['snapshot'].get('instances') or []
                                  if i['id'] not in replacement and i.get('ownerWorkloadUID') == args.workload_uid
                                  and i.get('node') == template['node'] and i.get('ready')]
                    if len(candidates) == 1:
                        active.append(candidates[0])
                        replacement = None
                violations.extend(guard.check(state, pods, pools))
                kind = ('route', 'unroute', 'release')[cycles % 3]
                body = {'kind': kind, 'workloadUID': args.workload_uid}
                if kind == 'route':
                    body['weights'] = {identity: 1000 for identity in active}
                operation(body)
            except Exception as error:
                violations.append({'kind': 'cycle-error', 'error': str(error)})
                if state is None:
                    try:
                        state, pods, pools = observe()
                        violations.extend(guard.check(state, pods, pools))
                    except Exception as observation_error:
                        violations.append({'kind': 'observation-error', 'error': str(observation_error)})
            cycles += 1
            for violation in violations:
                counts[violation['kind']] += 1
            if violations:
                save(out / ('violation-%05d.json' % cycles), dict(cycle=cycles, violations=violations,
                     state=state, pods=pods, pools=pools, operations=operations))
            time.sleep(max(0, min(start + cycles * args.interval, deadline) - time.monotonic()))
    finally:
        if not replaced or replacement is not None:
            counts['replacement-incomplete'] += 1
        summary = dict(cycles=cycles, violations=sum(counts.values()), violationKinds=dict(counts),
                       operations=len(timings), meanOperationACKSeconds=sum(timings) / len(timings) if timings else None,
                       nativeUnavailable=sum(guard.native_unavailable.values()),
                       nativeUnavailableByInstance=dict(guard.native_unavailable),
                       replacementComplete=replaced and replacement is None, removedIDs=sorted(guard.removed))
        save(out / 'summary.json', summary)
    return summary


def self_check():
    import copy
    import tempfile
    from unittest.mock import patch
    pod = {'metadata': {'uid': 'p'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}
    instance = dict(id='a', podUID='p', ready=True, ownerWorkloadUID='w', profile={'id': 'prof'}, node={'name': 'n', 'uid': 'n'})
    assert profile_id(instance) == profile_id({'profileID': 'prof'}) == 'prof'
    assert profile_id({}) is None
    nano = timestamp('2026-10-27T03:54:14.529208023Z')
    assert timestamp('2026-10-27T03:54:14Z') < nano < timestamp('2026-10-27T03:54:15Z')
    assert nano == timestamp('2026-10-27T12:54:14.529208023+09:00')
    guard = Invariants()
    state = {'snapshot': {'instances': [instance]}}
    assert not guard.check(state, {'items': [pod]}, {})
    bad = copy.deepcopy(state); bad['snapshot']['instances'][0]['ready'] = False
    assert guard.check(bad, {'items': [pod]}, {})[0]['kind'] == 'ready-flicker'
    assert not guard.check(state, {'items': [pod]}, {})
    unavailable = copy.deepcopy(bad)
    unavailable['snapshot']['instances'][0]['reasons'] = ['NativeUnavailable']
    assert not guard.check(unavailable, {'items': [pod]}, {})
    assert guard.native_unavailable == {'a': 1}
    guard.removed.add('gone')
    assert any(v['kind'] == 'removed-reappeared' for v in guard.check(state, {'items': [pod]}, {'instances': [{'id': 'gone'}]}))
    assert not guard.check(state, {'items': []}, {})
    assert guard.check(state, {'items': []}, {})[0]['kind'] == 'pod-ledger-mismatch'
    class Client:
        def __init__(self):
            self.instances = [instance, dict(instance, id='b', podUID='q')]
            self.calls = []
            self.stale = None
            self.stale_reads = 0
        def stamp(self): return dt.datetime.fromtimestamp(clock[0], dt.timezone.utc).isoformat()
        def state(self):
            if self.stale is not None:
                stale, self.stale = self.stale, None
                self.stale_reads += 1
                return stale
            return {'snapshot': {'at': self.stamp(), 'instances': copy.deepcopy(self.instances)}}
        def settings(self): return {'autonomousWorkloads': []}
        def k(self, argv): return {'items': [dict(pod, metadata={'uid': i['podUID']}) for i in self.instances]}
        def api(self, method, path, body=None, **kwargs):
            if method == 'GET': return {'observedAt': self.stamp(), 'instances': copy.deepcopy(self.instances)}
            self.calls.append(body['kind'])
            if body['kind'] == 'prepare': assert body['profileID'] == 'prof'
            self.stale = self.state()
            if body['kind'] == 'remove': self.instances = [i for i in self.instances if i['id'] != body['instance']]
            if body['kind'] == 'prepare': self.instances.append(dict(instance, id='new', podUID='new-p'))
            return {'status': 'complete', 'endedAt': self.stamp()}
    clock = [0.0]
    with tempfile.TemporaryDirectory() as folder, patch.object(time, 'monotonic', side_effect=lambda: clock[0]), patch.object(time, 'sleep', side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)):
        client = Client()
        result = run(client, SimpleNamespace(out=Path(folder), instances=['a', 'b'], workload_uid='w', duration=120, interval=20), 'fixture')
        assert result['cycles'] == 6 and result['violations'] == 0 and result['replacementComplete']
        assert {'route', 'unroute', 'release', 'remove', 'prepare'} <= set(client.calls)
        assert client.stale_reads == result['operations']
        assert result['nativeUnavailable'] == 0
        assert json.loads((Path(folder) / 'summary.json').read_text()) == result
    print('soak ledger self-check: OK')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--self-check', action='store_true')
    parser.add_argument('--out', type=Path)
    parser.add_argument('--workload-uid')
    parser.add_argument('--instances', nargs='+')
    parser.add_argument('--duration', type=float, default=1800, help='seconds (default: 30 minutes)')
    parser.add_argument('--interval', type=float, default=20)
    args = parser.parse_args()
    if args.self_check:
        self_check(); return
    if not args.out or not args.workload_uid or not args.instances or len(args.instances) not in (2, 3) or len(set(args.instances)) != len(args.instances):
        parser.error('--out, --workload-uid and 2–3 distinct --instances required')
    if args.interval <= 0 or args.duration < 4 * args.interval:
        parser.error('--interval must be positive and duration at least four intervals')
    config = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
    repository = Path(config['paths']['repository']).resolve()
    if args.out.resolve() == repository or repository in args.out.resolve().parents:
        parser.error('--out must be outside repository')
    spec = importlib.util.spec_from_file_location('soak_conductor', Path(__file__).resolve().parents[1] / 'e5/d2/conduct_before.py')
    helper = importlib.util.module_from_spec(spec); spec.loader.exec_module(helper)
    client = object.__new__(helper.Conductor)
    client.http = helper.urllib.request.build_opener(helper.NoRedirect(), helper.urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(helper.CA))))
    client.record = lambda value: None
    client.phase_out = args.out
    client.a = SimpleNamespace(command='soak-ledger')
    # Fail closed before any mutation, then recheck every cycle.
    if client.settings().get('autonomousWorkloads'):
        parser.error('Disable all autonomous workloads before the manual soak')
    result = run(client, args, config['measure']['namespace'])
    print(json.dumps(result))
    raise SystemExit(1 if result['violations'] else 0)


if __name__ == '__main__':
    main()
