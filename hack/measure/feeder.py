# feeder.py: lane orchestrator. Each node's documents are created in file order, one at a time, after the
# previous one is terminal (the node queue runs them one at a time anyway, so per-node order is unchanged).
# Occupancies are uncapped until an occupancy fails writing its record to the evidence store (DNS / S3
# transport failure); then the cap becomes 4 in flight cluster-wide (persisted in feeder-cap) and the failed
# occupancy is re-run right away under the next free 9NN name. Restartable: existing terminal CRs are skipped.
# usage: python3 feeder.py rerun | lanes | phase-a
# Approved resume: python3 feeder.py --manifest PATH/resume-plan.json --stages 4,5,6
import argparse, json, os, re, subprocess, sys, time, datetime as dt
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
V = CONFIG["paths"]["campaign"]
ST = ENV["statusLog"]
CAPF = V + '/feeder-cap'
KIND = {'BatchEnergyOptimization': 'beo', 'ModelLoadedIdleBaseline': 'idlebase', 'OperationCostMeasurement': 'opcost',
        'DispatchOccupancyMeasurement': 'occupancy'}
STORE_FAIL = re.compile(r'evidence store transport failed|lookup chill-evidence-store|i/o timeout')
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
def rename(d, node):
    tail = d['metadata']['name'][len(node) + 5:]
    for n in range(940, 1000):
        name = f'{node}-{n}-{tail}'
        if state(d['kind'], name) is None:
            e = json.loads(json.dumps(d)); e['metadata']['name'] = name; return e
HOLDF = V + '/feeder-hold.json'
def held(d):
    # Hold rules (feeder-hold.json, re-read every pass): skip creating matching documents; the node's queue continues.
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
def run(queues, final, approved=False):
    pos = {n: 0 for n in queues}
    # A held document has not had its future terminal result reported.
    seen, held_seen = previous_names(open(V + '/feeder.log'))
    while True:
        busy = False
        hold = json.load(open(HOLDF)) if os.path.exists(HOLDF) else {}
        pause = hold.get('pauseNodes', [])
        for n, q in queues.items():
            while pos[n] < len(q):
                d = q[pos[n]]; kind, name = d['kind'], d['metadata']['name']; st = state(kind, name)
                if st is None and held(d):
                    if name not in held_seen:
                        held_seen.add(name); L('HELD', n, name)
                        open(V + '/feeder-held.txt', 'a').write(f'{now()} {n} {kind} {name}\n')
                    if approved:
                        busy = True; break
                    pos[n] += 1; continue
                if st is None:
                    # Pausing stops the next creation while existing CRs remain observable.
                    if n in pause:
                        busy = True; break
                    if approved and not node_released(n):
                        busy = True; break
                    if kind == 'DispatchOccupancyMeasurement' and occ_inflight() >= cap():
                        busy = True; break
                    r = k('create', '-f', '-', inp=json.dumps(d)); L('CREATE', n, name, r.returncode, (r.stdout + r.stderr).strip()[:200])
                    if approved and r.returncode:
                        raise SystemExit(f'cannot create staged CR: {name}: {r.stderr.strip()}')
                    busy = True; break
                if not terminal(st):
                    busy = True; break
                if approved and (st[0] in ('Failed', 'Insufficient') or st[2].startswith('Refused')):
                    raise SystemExit(f'campaign stopped after terminal failure: {name}: {st}')
                if approved and not node_released(n):
                    busy = True; break
                if name not in seen:
                    seen.add(name); L('TERMINAL', n, name, *st)
                    if kind == 'DispatchOccupancyMeasurement': final[n] = name
                    if st[0] in ('Failed', 'Insufficient') or st[2].startswith('Refused'):
                        S(f'LANE {n} {name} {st[0]} {st[1][:160]} {st[2][:60]}')
                        with open(V + '/control-alerts.jsonl', 'a') as alerts:
                            alerts.write(json.dumps({'at': now(), 'event': 'measurement terminal failure', 'node': n, 'kind': kind, 'name': name, 'phase': st[0], 'reason': st[1], 'admission': st[2]}) + '\n')
                        if not (kind == 'DispatchOccupancyMeasurement' and STORE_FAIL.search(st[1])):
                            raise SystemExit(f'campaign stopped after unexpected terminal failure: {name}')
                        if kind == 'DispatchOccupancyMeasurement' and STORE_FAIL.search(st[1]):
                            if cap() > 4:
                                open(CAPF, 'w').write('4'); S('FEEDER CAP 4: evidence-store DNS/transport failure on record write')
                            e = rename(d, n); q.insert(pos[n] + 1, e); S(f'FEEDER RERUN {name} as {e["metadata"]["name"]}')
                pos[n] += 1
            if pos[n] == len(q) and ('done', n) not in seen:
                seen.add(('done', n)); S(f'LANE DONE {n}')
        json.dump(final, open(V + '/feeder-final.json', 'w'))
        if not busy: return
        time.sleep(30)
def main():
    global log
    if sys.argv[1:] and sys.argv[1].startswith('--'):
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
        run(queues, {}, approved=True)
        S('APPROVED FEEDER DONE')
        return
    log = open(V + '/feeder.log', 'a')
    final = {}
    open(V + '/feeder.pid', 'w').write(str(os.getpid()) + '\n')
    if sys.argv[1] == 'rerun':
        q = {}
        for n in ENV['rerunNodes']:
            d = next(x for x in docs(f'{V}/front/{n}.yaml') if x['kind'] == 'DispatchOccupancyMeasurement' and '-007-' in x['metadata']['name'])
            d['metadata']['name'] = n + '-007-' + d['metadata']['name'][len(n) + 5:]; q[n] = [rename(d, n)]
        json.dump(q, open(V + '/front-rerun.json', 'w'))
        S(f'FRONT RERUN START (8 in parallel, cap {cap()})'); run(q, final); S(f'FRONT RERUN DONE {json.dumps(final)}')
    else:
        folder = 'phase-a' if sys.argv[1] == 'phase-a' else 'lanes'
        files = {n: f'{V}/{folder}/{n}.yaml' for n in ENV['feederNodes']}
        if folder == 'lanes': files[ENV['nanoNode']] = f"{V}/lanes/{ENV['nanoNode']}.after903.yaml"
        S(f'LANES FEEDER START (per-node file order, occupancy cap {cap()})'); run({n: docs(f) for n, f in files.items()}, final); S('LANES ALL DONE')

if __name__ == '__main__':
    main()
