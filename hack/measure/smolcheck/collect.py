# collect.py BEO: per ProfilingRun, runtime start -> "serving worker started" (idle: model load; active: load + warmup), attempt timeout, result.
import json, re, subprocess, sys, datetime as dt
import os
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
beo = sys.argv[1]
k = lambda *a: subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], '-n', ENV['namespace'], *a], capture_output=True, text=True).stdout
def P(s): return dt.datetime.fromisoformat(s.replace('Z', '+00:00'))
runs = json.loads(k('get', 'profilingrun', '-o', 'json'))['items']
load = []
for r in sorted(runs, key=lambda r: r['metadata']['creationTimestamp']):
    name = r['metadata']['name']
    if not name.startswith(beo + '-'): continue
    cond = {c['type']: c.get('reason') for c in r.get('status', {}).get('conditions', [])}
    job = (r.get('status') or {}).get('job', {}).get('name', '')
    pods = json.loads(k('get', 'pod', '-l', 'job-name=' + job, '-o', 'json') or '{"items":[]}')['items'] if job else []
    if not pods:
        print(f'  {name[len(beo)+1:]} {cond} pod gone'); continue
    p = pods[0]
    args = ' '.join(a for c in p['spec']['containers'] for a in c.get('args') or [])
    att = (re.search(r'--attempt-timeout=(\S+)', args) or [None, '?'])[1]
    st = {c['name']: c.get('state', {}).get('terminated') or c.get('state', {}).get('running') or {} for c in p['status'].get('containerStatuses', [])}
    start = st.get('runtime', {}).get('startedAt')
    log = k('logs', p['metadata']['name'], '-c', 'runtime')
    m = re.search(r'^(\S+ \S+) INFO serving worker started', log, re.M)
    ready = None
    if m and start:
        ready = (dt.datetime.strptime(m.group(1), '%Y/%m/%d %H:%M:%S').replace(tzinfo=dt.timezone.utc) - P(start)).total_seconds()
    if '-idle-' in name and ready is not None: load.append(ready)
    fail = 'readiness timed out' if 'readiness timed out' in log else ''
    print(f'  {name[len(beo)+1:]} ready_s={ready} attempt={att} exec={cond.get("ExecutionComplete")} evidence={cond.get("EvidenceVerified")} {fail}')
if load: print(f'  model load (idle runs) s: {load}')
