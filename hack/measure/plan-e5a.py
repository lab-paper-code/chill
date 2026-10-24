# plan-e5a.py: E5-essential phase (a) = every lane item at each E5 Node's reference power mode, with status and a time estimate.
# Reference modes: non-Jetson fixed, jetsono MAXN, jetsonx MODE_15W_6CORE, jetson-nano MAXN. genesis CUDA SmolVLM2 excluded (GPU OOM).
import json, subprocess, collections
import os
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
V = CONFIG["paths"]["campaign"]
REF = ENV['referenceModes']
FILES = {n: f'{V}/lanes/{n}.yaml' for n in ENV['laneNodes']}
FILES[ENV['nanoNode']] = f"{V}/lanes/{ENV['nanoNode']}.after903.yaml"
EXTRA = {'rasp5': ['rasp5-911-smolvlm-int8-q2'], 'jetson-nano': ['jetson-nano-903-smolvlm-int8-q1-maxn'],
         'orangepi': [], 'genesis2': [], 'genesis3': []}
docs = lambda p: [json.loads(d) for d in open(p).read().split('---\n') if d.strip()]
live = {}
for o in json.loads(subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], '-n', ENV['namespace'], 'get', 'beo,occupancy,opcost', '-o', 'json'], capture_output=True, text=True).stdout)['items']:
    s = o.get('status') or {}; live[o['metadata']['name']] = (s.get('phase') or 'Pending', s.get('reason') or s.get('outcome') or '')
held = set(l.split()[3] for l in open(V + '/feeder-held.txt')) if __import__('os').path.exists(V + '/feeder-held.txt') else set()
def model(name): return 'smolvlm' if 'smolvlm' in name else 'plc' if 'plc' in name else 'mnv2' if 'mnv2' in name else 'cost'
def est(d):  # minutes, from the measured durations of this campaign (BEO mnv2 CPU 4-7, PLC 7-15, SmolVLM int8 29-38, jetsono SmolVLM CUDA 31, jetson-nano mnv2 CUDA 44)
    k, n = d['kind'], d['metadata']['name']
    if k == 'BatchEnergyOptimization':
        p = d['spec']['state']['candidate']['executionPath']; m = model(n)
        if m == 'mnv2': return 40 if 'cuda' in p and 'jetson-nano' in n else 15 if 'cuda' in p else 6
        if m == 'plc': return 15
        if 'fp32' in n: return 120
        return 30 if 'cuda' in p else (60 if 'jetson' in n else 35)
    if k == 'DispatchOccupancyMeasurement':
        return 2 + (d['spec']['warmupSeconds'] + d['spec']['durationSeconds']) / 60 + 3
    steps = d['spec']['steps']; m = sum(4 if s['kind'] in ('prepare', 'remove') else (2 if s['kind'] == 'idle' else 0) for s in steps)
    pw = sum(1 for s in steps if s['kind'] == 'power'); reboot = {'jetsono': 6, 'jetson-nano': 2, 'jetsonx': 1}
    return 5 + m + pw * reboot.get(n.split('-0')[0] if not n.startswith('jetson-nano') else 'jetson-nano', 0)
def mode(d, beo_mode):
    if d['kind'] == 'BatchEnergyOptimization': return d['spec']['state']['candidate']['powerMode']
    if d['kind'] == 'DispatchOccupancyMeasurement': return beo_mode.get(d['spec']['batchEnergyOptimization'])
    pm = [s['powerMode'] for s in d['spec']['steps'] if s['kind'] == 'power' and s.get('name') == 'up']
    names = [s.get('batchEnergyOptimization') for s in d['spec']['steps'] if s.get('batchEnergyOptimization')]
    return pm[0] if pm else (beo_mode.get(names[0]) if names else None)
def blocker(d, st):
    n = d['metadata']['name']; k = d['kind']
    if st[0] == 'Complete': return ''
    if k == 'OperationCostMeasurement' and any('smolvlm' in (s.get('batchEnergyOptimization') or '') for s in d['spec']['steps']): return 'SmolVLM2 serving timeout fix'
    if k == 'DispatchOccupancyMeasurement' and 'smolvlm' in n: return 'SmolVLM2 serving timeout fix'
    if k == 'DispatchOccupancyMeasurement' and 'plc' in n: return 'PLC progress fix' + (' + gateway (~196 req/s)' if ('cuda' in n or n.startswith(('genesis', 'lattepanda', 'jetsono'))) else '')
    if k == 'OperationCostMeasurement': return 'held (opcost hold)'
    if k == 'BatchEnergyOptimization' and 'cuda' in d['spec']['state']['candidate']['executionPath']: return 'held (CUDA BEO hold)'
    return ''
total = {}
for node, f in FILES.items():
    ds = docs(f); beo_mode = {d['metadata']['name']: d['spec']['state']['candidate']['powerMode'] for d in ds if d['kind'] == 'BatchEnergyOptimization'}
    for x in EXTRA.get(node, []): beo_mode[x] = REF.get(node, 'fixed')
    ref = REF.get(node, 'fixed'); rows = []
    for d in ds:
        n = d['metadata']['name']
        if mode(d, beo_mode) != ref or '-007-occ' in n or '-006-' in n or '-008-' in n or '-009-' in n: continue
        if node.startswith('genesis') and 'smolvlm' in n and 'cuda' in n: continue
        st = live.get(n, ('not created', ''))
        rows.append((n, d['kind'][:3], st[0], st[1][:40], blocker(d, st), 0 if st[0] == 'Complete' else est(d)))
    for x in EXTRA.get(node, []): rows.insert(0, (x, 'Bat', live.get(x, ('-', ''))[0], live.get(x, ('', ''))[1][:40], '', 0))
    done = sum(1 for r in rows if r[2] == 'Complete'); rem = sum(r[5] for r in rows); blk = sum(r[5] for r in rows if r[4])
    total[node] = (len(rows), done, rem, blk)
    print(f'\n### {node} (reference mode {ref}): {len(rows)} items, {done} Complete, remaining ≈ {rem/60:.1f} h (of which blocked ≈ {blk/60:.1f} h)')
    for r in rows: print(f'- {r[0]} [{r[1]}] {r[2]} {r[3]} {("BLOCKED: " + r[4]) if r[4] else ""} {("~%d min" % r[5]) if r[5] else ""}')
print('\nfront (mobilenet base BEO + d, front blocks) and CPU variants included; per-lane remaining h:', {k: round(v[2] / 60, 1) for k, v in total.items()})
