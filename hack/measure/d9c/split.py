# split.py: per-batch decomposition of occupancy runs from observation rows.
# usage: CHILL_EXPERIMENT_CONFIG=/absolute/environment.json python3 split.py <workload> <from> <to> <label>
import json, os, sys, subprocess, statistics as st, datetime as dt, urllib.parse as up
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
S, CA, TOK = ENV['serverURL'], ENV['caFile'], open(ENV['tokenFile']).read().strip()
wl, frm, to, label = sys.argv[1:5]
uid = subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], '-n', ENV['namespace'], 'get', 'workload', wl, '-o', 'jsonpath={.metadata.uid}'], capture_output=True, text=True).stdout
def get(q):
    out = subprocess.run(['curl', '-sS', '--cacert', CA, '-H', 'Authorization: Bearer ' + TOK, f'{S}/api/v1/observations/workloads/{uid}?{q}'], capture_output=True, text=True).stdout
    return json.loads(out)
rows, cur = [], None
while True:
    q = f'from={frm}&to={to}&limit=1000' + (f'&cursor={up.quote(cur)}' if cur else '')
    d = get(q); rows += d.get('requests') or []
    cur = d.get('nextCursor')
    if not cur or not d.get('requests'): break
def P(s):
    b, _, f = s.rstrip('Z').partition('.'); return dt.datetime.fromisoformat(b + '.' + f[:6].ljust(6, '0')).timestamp()
B = {}
for r in rows:
    if r.get('outcome') != 'completed' or r.get('runningNanos') is None: continue
    t = P(r['receivedAt']); b = B.setdefault((r['instance'], r['batchID']), dict(start=1e18, exe=0, q=1e18, svc=r.get('serviceMS'), be=r.get('backendMS'), pre=r.get('preprocessMS'), qms=[]))
    b['start'] = min(b['start'], t + r['runningNanos'] / 1e9); b['exe'] = max(b['exe'], t + r['executedNanos'] / 1e9)
    b['q'] = min(b['q'], t + r['queuedNanos'] / 1e9); b['qms'].append(r['queueMS'])
inst = max({k[0] for k in B}, key=lambda i: sum(1 for k in B if k[0] == i))
ids = sorted(k[1] for k in B if k[0] == inst); bb = [B[(inst, i)] for i in ids]
t0 = bb[0]['start'] + 60; bb = [b for b, i in zip(bb, ids) if t0 <= b['start'] <= t0 + 360]
ids = [i for i in ids if t0 <= B[(inst, i)]['start'] <= t0 + 360]
gap, inter, idle, run = [], [], 0, []
for (i, b), (j, n) in zip(zip(ids, bb), zip(ids[1:], bb[1:])):
    if j != i + 1: continue
    inter.append((n['start'] - b['start']) * 1e3); gap.append((n['start'] - b['exe']) * 1e3); run.append((b['exe'] - b['start']) * 1e3)
    idle += n['q'] > b['exe']
q = lambda a, p: sorted(a)[int(p * (len(a) - 1))]
m = lambda a: round(st.mean(a), 2)
svc = [b['svc'] for b in bb if b['svc'] is not None]; be = [b['be'] for b in bb if b['be'] is not None]; pre = [b['pre'] for b in bb if b['pre'] is not None]
qm = [x for b in bb for x in b['qms']]
print(json.dumps(dict(label=label, rows=len(rows), batches=len(bb), X=round(1e3 / st.mean(inter), 2),
    inter_start_ms=dict(mean=m(inter), p50=round(q(inter, .5), 2), p99=round(q(inter, .99), 2)),
    exec_ms_start_to_executed=dict(mean=m(run), p50=round(q(run, .5), 2)),
    serviceMS=dict(mean=m(svc), p50=round(q(svc, .5), 2), p99=round(q(svc, .99), 2)), backendMS=m(be), preprocessMS=m(pre),
    gap_after_exec_ms=dict(mean=m(gap), p50=round(q(gap, .5), 2), p99=round(q(gap, .99), 2), max=round(max(gap), 2)),
    idle_fraction=round(idle / max(1, len(gap)), 4), queueMS=dict(p50=round(q(qm, .5), 1), p05=round(q(qm, .05), 1)))))
