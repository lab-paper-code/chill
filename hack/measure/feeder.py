# feeder.py: lane orchestrator. Each node's documents are created in file order, one at a time, after the
# previous one is terminal (the node queue runs them one at a time anyway, so per-node order is unchanged).
# Occupancies obey the persisted feeder-cap limit. Held documents block their lane;
# failed terminal records stop approved campaigns, but coverage plans record them and continue.
# Coverage lanes stop after measure.maxConsecutiveFailures (default 2); other lanes continue.
# Restartable: existing terminal CRs are never recreated. Skipped counts existing successes
# and unattempted documents in stopped lanes.
# Approved resume: python3 feeder.py --manifest PATH/resume-plan.json --stages 4,5,6
# Coverage failures: feeder.log/statusLog and feeder-failed.txt; retry only via a new plan.
# Offline regression: python3 hack/measure/feeder_check.py
import argparse, json, os, re, subprocess, time, datetime as dt
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
V = CONFIG["paths"]["campaign"]
ST = ENV["statusLog"]
CAPF = V + '/feeder-cap'
KIND = {'BatchEnergyOptimization': 'beo', 'ModelLoadedIdleBaseline': 'idlebase', 'OperationCostMeasurement': 'opcost',
        'DispatchOccupancyMeasurement': 'occupancy'}
now = lambda: dt.datetime.now(dt.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
log = None
def L(*a): log.write(now() + ' ' + ' '.join(map(str, a)) + '\n'); log.flush()
def S(msg): open(ST, 'a').write(f'{msg} {now()}\n'); L('STATUS', msg)
def cap(): return int(open(CAPF).read()) if os.path.exists(CAPF) else 1000
def k(*a, inp=None): return subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], '-n', ENV['namespace'], *a], input=inp, capture_output=True, text=True)
def docs(path): return [json.loads(d) for d in open(path).read().split('---\n') if d.strip()]
def manifest_queues(path, stages):
    plan = json.load(open(path))
    selected = [int(s) for s in stages.split(',')]
    steps = {s['stage']: s for s in plan['sequence']}
    if selected != sorted(set(selected)) or not selected or any(s not in steps for s in selected):
        raise ValueError('stages must be distinct known stage numbers in approved order')
    if 7 in selected and selected != [7]:
        raise ValueError('post-D2 stage7 must run separately, only after D2 acceptance')
    queues, names = {}, set()
    for stage in selected:
        for filename in steps[stage]['paths']:
            for d in docs(filename):
                name = d['metadata']['name']
                if name in names:
                    raise ValueError(f'duplicate staged CR: {name}')
                names.add(name)
                spec = d['spec']
                node = spec['state']['targetNode'] if d['kind'] == 'BatchEnergyOptimization' else spec['node']
                queues.setdefault(node, []).append(d)
    return queues
def node_released(node):
    r = k('get', 'node', node, '-o', 'json')
    if r.returncode:
        raise SystemExit(f'cannot confirm Node handback: {node}: {r.stderr.strip()}')
    return not any(t['key'] == 'chill.dacs.io/measurement'
                   for t in json.loads(r.stdout)['spec'].get('taints', []))
def state(kind, name):
    r = k('get', KIND[kind], name, '-o', 'json')
    if r.returncode: return None
    o = json.loads(r.stdout); s = o.get('status') or {}
    return s.get('phase') or '', s.get('reason') or s.get('outcome') or '', (o['metadata'].get('annotations') or {}).get('chill.dacs.io/admission', '')
def terminal(st): return st[0] in ('Complete', 'Failed', 'Insufficient') or st[2].startswith('Refused')
def occ_inflight():
    o = json.loads(k('get', 'occupancy', '-o', 'json').stdout)['items']
    return sum(1 for x in o if not terminal(((x.get('status') or {}).get('phase') or '', '', (x['metadata'].get('annotations') or {}).get('chill.dacs.io/admission', ''))))
HOLDF = V + '/feeder-hold.json'
def held(d):
    # Hold rules (feeder-hold.json, re-read every pass): block creation and advancement of matching documents.
    #   drop: regex of names never created; occupancy: regex of held occupancy names;
    #   opcostSmolvlm: hold operation costs with a SmolVLM2 step; beoCuda: hold CUDA BEOs.
    if not os.path.exists(HOLDF): return False
    h = json.load(open(HOLDF)); kind, name = d['kind'], d['metadata']['name']
    if h.get('drop') and re.search(h['drop'], name): return True
    if kind == 'OperationCostMeasurement':
        if h.get('opcost'): return True
        if h.get('opcostSmolvlm') and any('smolvlm' in (s.get('batchEnergyOptimization') or '') for s in d['spec']['steps']): return True
    if kind == 'DispatchOccupancyMeasurement' and re.search(h.get('occupancy', '^$'), name): return True
    # An occupancy whose BatchEnergyOptimization was not created (held) would be refused ProfileMissing.
    if kind == 'DispatchOccupancyMeasurement' and state('BatchEnergyOptimization', d['spec']['batchEnergyOptimization']) is None: return True
    if kind == 'BatchEnergyOptimization' and h.get('beoCuda') and 'cuda' in d['spec']['state']['candidate']['executionPath']: return True
    return False
def previous_names(lines):
    terminal, held_names = set(), set()
    for line in lines:
        fields = line.split()
        if len(fields) > 3 and fields[1] == 'TERMINAL': terminal.add(fields[3])
        if len(fields) > 3 and fields[1] == 'HELD': held_names.add(fields[3])
    return terminal, held_names
assert previous_names(('t HELD n later', 't TERMINAL n done')) == ({'done'}, {'later'})
def run(queues, final, continue_on_failure=False):
    pos = {n: 0 for n in queues}
    created = set()
    failures = {n: 0 for n in queues}
    stopped = set()
    limit = ENV.get('maxConsecutiveFailures', 2)
    if continue_on_failure and (type(limit) is not int or limit < 1):
        raise ValueError('measure.maxConsecutiveFailures must be a positive integer')
    counts = dict(success=0, failed=0, skipped=0)
    # A held document has not had its future terminal result reported.
    seen, held_seen = previous_names(open(V + '/feeder.log'))
    while True:
        busy = False
        hold = json.load(open(HOLDF)) if os.path.exists(HOLDF) else {}
        pause = hold.get('pauseNodes', [])
        for n, q in queues.items():
            if n in stopped:
                continue
            while pos[n] < len(q):
                d = q[pos[n]]; kind, name = d['kind'], d['metadata']['name']; st = state(kind, name)
                if st is None and held(d):
                    if name not in held_seen:
                        held_seen.add(name); L('HELD', n, name)
                        open(V + '/feeder-held.txt', 'a').write(f'{now()} {n} {kind} {name}\n')
                    busy = True; break
                if st is None:
                    # Pausing stops the next creation while existing CRs remain observable.
                    if n in pause:
                        busy = True; break
                    if not node_released(n):
                        busy = True; break
                    if kind == 'DispatchOccupancyMeasurement' and occ_inflight() >= cap():
                        busy = True; break
                    r = k('create', '-f', '-', inp=json.dumps(d)); L('CREATE', n, name, r.returncode, (r.stdout + r.stderr).strip()[:200])
                    if r.returncode:
                        raise SystemExit(f'cannot create staged CR: {name}: {r.stderr.strip()}')
                    created.add(name)
                    busy = True; break
                if not terminal(st):
                    busy = True; break
                failed = st[0] in ('Failed', 'Insufficient') or st[2].startswith('Refused')
                if failed and not continue_on_failure:
                    raise SystemExit(f'campaign stopped after terminal failure: {name}: {st}')
                if name not in seen:
                    seen.add(name); L('TERMINAL', n, name, *st)
                    if failed:
                        S(f'FAILED {n} {kind} {name} {st}')
                        open(V + '/feeder-failed.txt', 'a').write(f'{now()} {n} {kind} {name} {st}\n')
                    elif kind == 'DispatchOccupancyMeasurement':
                        final[n] = name
                stop = continue_on_failure and failed and failures[n] + 1 >= limit
                if not stop and not node_released(n):
                    busy = True; break
                counts['failed' if failed else 'success' if name in created else 'skipped'] += 1
                pos[n] += 1
                failures[n] = failures[n] + 1 if failed else 0
                if stop:
                    stopped.add(n)
                    counts['skipped'] += len(q) - pos[n]
                    message = f'LANE STOPPED {n} after {limit} consecutive failures'
                    S(message); print(message)
                    break
            if n not in stopped and pos[n] == len(q) and ('done', n) not in seen:
                seen.add(('done', n)); S(f'LANE DONE {n}')
        json.dump(final, open(V + '/feeder-final.json', 'w'))
        if not busy:
            summary = 'CAMPAIGN SUMMARY ' + ' '.join(f'{key}={value}' for key, value in counts.items())
            S(summary)
            print(summary)
            return counts
        time.sleep(30)
def main():
    global log
    parser = argparse.ArgumentParser(description='Resume approved immutable staged paths through existing per-Node lanes.')
    parser.add_argument('--manifest', required=True)
    parser.add_argument('--stages', required=True)
    parser.add_argument('--check-manifest', action='store_true', help='read-only queue/input check; no PID/log/cluster calls')
    args = parser.parse_args()
    queues = manifest_queues(args.manifest, args.stages)
    if args.check_manifest:
        assert all(d['kind'] != 'DispatchOccupancyMeasurement' or
                   d['spec']['supplyMode'] == 'closed-loop' for q in queues.values() for d in q)
        cost_names = [d['metadata']['name'] for q in queues.values() for d in q if d['kind'] == 'OperationCostMeasurement']
        assert len(cost_names) == len(set(cost_names)) <= 14
        hold = json.load(open(HOLDF)) if os.path.exists(HOLDF) else {}
        assert not any(re.search(hold.get('drop') or '^$', d['metadata']['name'])
                       for q in queues.values() for d in q)
        for invalid in ('3,2', '4,7', '7,4', '1,1'):
            try:
                manifest_queues(args.manifest, invalid)
            except ValueError:
                pass
            else:
                raise AssertionError(f'unsafe stage selection admitted: {invalid}')
        print(json.dumps({n: [d['metadata']['name'] for d in q] for n, q in queues.items()}, indent=2))
        return
    log = open(V + '/feeder.log', 'a')
    open(V + '/feeder.pid', 'w').write(str(os.getpid()) + '\n')
    S(f'APPROVED FEEDER START manifest={args.manifest} stages={args.stages}')
    hold = json.load(open(HOLDF)) if os.path.exists(HOLDF) else {}
    summary = 'FEEDER HOLD ' + json.dumps({
        'pauseNodes': hold.get('pauseNodes', []), 'drop': hold.get('drop', '')}, sort_keys=True)
    S(summary); print(summary)
    run(queues, {}, continue_on_failure=
        json.load(open(args.manifest)).get('schemaVersion') == 'coverage-feeder-plan.v1')
    S('APPROVED FEEDER DONE')

if __name__ == '__main__':
    main()
