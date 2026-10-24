# smoke.py NODE OUTDIR WL1=PROFILE1[:DATASET] [WL2=PROFILE2[:DATASET]]: end-to-end serving smoke through the gateway on one free Node.
# manual prepare (CHILL API) per Workload -> staticRouting via settings PUT (reason) -> N requests each -> records -> cleanup
# (staticRouting {} with reason, manual remove). Exits nonzero if any step fails; cleanup always runs.
import json, os, subprocess, sys, time, datetime as dt
import os
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
S = ENV["serverURL"]
CA = ENV["caFile"]
node, out = sys.argv[1], sys.argv[2]
repository = Path(CONFIG["paths"]["repository"]).resolve()
if repository == Path(out).resolve() or repository in Path(out).resolve().parents:
    raise SystemExit("smoke output must remain outside the repository source tree")
os.makedirs(out, exist_ok=True)
N = int(os.environ.get('N', '5'))
now = lambda: dt.datetime.now(dt.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
LOG = open(out + '/smoke.log', 'a')
def L(*a): s = now() + ' ' + ' '.join(map(str, a)); print(s); LOG.write(s + '\n'); LOG.flush()
def api(method, path, body=None, timeout=900):
    a = ['curl', '-sS', '--max-time', str(timeout), '--cacert', CA, '-H', 'Authorization: Bearer ' + open(ENV['tokenFile']).read().strip(), '-X', method, S + path, '-w', '\n%{http_code}']
    if body is not None: a += ['-H', 'Content-Type: application/json', '--data', json.dumps(body)]
    r = subprocess.run(a, capture_output=True, text=True).stdout.rsplit('\n', 1)
    return int(r[1]) if len(r) > 1 and r[1].isdigit() else 0, r[0]
def kj(*a): return json.loads(subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], *a, '-o', 'json'], capture_output=True, text=True).stdout)
nodeobj = kj('get', 'node', node); ref = {'name': node, 'uid': nodeobj['metadata']['uid']}
items = []
for arg in sys.argv[3:]:
    wl, rest = arg.split('=', 1); prof, ds = rest.split(':', 1)
    items.append(dict(wl=wl, uid=kj('-n', ENV['namespace'], 'get', 'workload', wl)['metadata']['uid'], profile=prof, ds=ds))
t0 = now(); instances = {}; ok = True
def settings_put(routing, reason):
    _, s = api('GET', '/api/v1/scheduler/settings'); s = json.loads(s); s.pop('staticRoutingValidatedAt', None)
    s['staticRouting'] = routing; s['reason'] = reason
    code, body = api('PUT', '/api/v1/scheduler/settings', s); L('settings PUT', code, json.dumps(routing)[:200]); return code
ok = False
try:
    ok = True
    taints = [t['key'] for t in nodeobj['spec'].get('taints', [])]
    if 'chill.dacs.io/measurement' in taints: raise SystemExit(f'{node} is held by a measurement')
    for it in items:
        _, before = api('GET', '/api/v1/scheduler/state'); before_ids = {i['id'] for i in (json.loads(before)['snapshot'].get('instances') or [])}
        code, body = api('POST', '/api/v1/scheduler/operations', {'kind': 'prepare', 'workloadUID': it['uid'], 'profileID': it['profile'], 'node': ref})
        open(f"{out}/prepare-{it['wl']}.json", 'w').write(body)
        st = json.loads(body).get('status') if body.startswith('{') else body[:300]
        L('prepare', it['wl'], code, st)
        if code != 200 or st != 'complete': ok = False; raise SystemExit('prepare failed')
        for _ in range(30):
            _, snap = api('GET', '/api/v1/scheduler/state'); new = [i for i in (json.loads(snap)['snapshot'].get('instances') or []) if i['id'] not in before_ids and i['node']['name'] == node]
            if new: break
            time.sleep(5)
        instances[it['wl']] = new[0]['id']; L('instance', it['wl'], new[0]['id'])
    if settings_put({it['uid']: {instances[it['wl']]: 1000} for it in items}, f'serving smoke on {node}') != 200: ok = False; raise SystemExit('staticRouting failed')
    time.sleep(15)
    for it in items:
        r = subprocess.run(['python3', os.path.join(os.path.dirname(os.path.abspath(__file__)), 'send.py'), it['wl'], it['ds'], str(N), f"{out}/requests-{it['wl']}.jsonl"], capture_output=True, text=True)
        lines = [json.loads(l) for l in open(f"{out}/requests-{it['wl']}.jsonl")]
        good = sum(1 for l in lines if l.get('status') == 200)
        L('requests', it['wl'], f'{good}/{len(lines)} HTTP 200', json.dumps(lines[0].get('body'))[:300] if lines else '')
        ok &= good == len(lines) and len(lines) == N
finally:
    if instances: settings_put({it['uid']: {} for it in items}, f'end serving smoke on {node}: unroute'); time.sleep(10)
    for wl, inst in instances.items():
        it = next(i for i in items if i['wl'] == wl)
        code, body = api('POST', '/api/v1/scheduler/operations', {'kind': 'remove', 'workloadUID': it['uid'], 'node': ref, 'instance': inst})
        open(f'{out}/remove-{wl}.json', 'w').write(body); L('remove', wl, code, json.loads(body).get('status') if body.startswith('{') else body[:200])
    if instances: settings_put({}, f'end serving smoke on {node}: drop declaration')
    t1 = now()
    for it in items:
        _, b = api('GET', f"/api/v1/observations/workloads/{it['uid']}?from={t0}&to={t1}&limit=1000"); open(f"{out}/observations-{it['wl']}.json", 'w').write(b)
    _, b = api('GET', f'/api/v1/observations/scheduler?from={t0}&to={t1}&limit=1000'); open(f'{out}/scheduler-actions.json', 'w').write(b)
    L('done', 'OK' if ok else 'FAIL')
sys.exit(0 if ok else 1)
