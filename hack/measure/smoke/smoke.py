# smoke.py NODE OUTDIR WL1=PROFILE1[:DATASET] [WL2=PROFILE2[:DATASET]]: end-to-end serving smoke through the gateway on one free Node.
# manual prepare and route operations per Workload -> N requests each -> records -> one server reset.
# Operation requests/results retain native ACKs and reset steps. Any failed operation exits nonzero.
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
t0 = now(); instances = {}; attempted_uids = []
def operation(body, prefix):
    global ok
    Path(out, prefix + '-request.json').write_text(json.dumps(body, indent=2) + '\n')
    code, raw = api('POST', '/api/v1/scheduler/operations', body)
    Path(out, prefix + '-result.json').write_text(raw)
    result = json.loads(raw)
    L(body['kind'], code, result.get('status'))
    if code != 200 or result.get('status') != 'complete' or result.get('error'):
        ok = False
        raise RuntimeError(body['kind'] + ' failed; retain operation evidence and stop')
    return result
ok = False
try:
    ok = True
    taints = [t['key'] for t in nodeobj['spec'].get('taints', [])]
    if 'chill.dacs.io/measurement' in taints: raise SystemExit(f'{node} is held by a measurement')
    for it in items:
        _, before = api('GET', '/api/v1/scheduler/state'); before_ids = {i['id'] for i in (json.loads(before)['snapshot'].get('instances') or [])}
        attempted_uids.append(it['uid'])
        operation({'kind': 'prepare', 'workloadUID': it['uid'], 'profileID': it['profile'], 'node': ref, 'reason': f'Prepare serving smoke on {node}'}, 'prepare-' + it['wl'])
        for _ in range(30):
            _, snap = api('GET', '/api/v1/scheduler/state'); new = [i for i in (json.loads(snap)['snapshot'].get('instances') or []) if i['id'] not in before_ids and i['node']['name'] == node]
            if new: break
            time.sleep(5)
        instances[it['wl']] = new[0]['id']; L('instance', it['wl'], new[0]['id'])
    for it in items:
        operation({'kind': 'route', 'workloadUID': it['uid'], 'weights': {instances[it['wl']]: 1000}, 'reason': f'Serving smoke on {node}'}, 'route-' + it['wl'])
    for it in items:
        r = subprocess.run(['python3', os.path.join(os.path.dirname(os.path.abspath(__file__)), 'send.py'), it['wl'], it['ds'], str(N), f"{out}/requests-{it['wl']}.jsonl"], capture_output=True, text=True)
        lines = [json.loads(l) for l in open(f"{out}/requests-{it['wl']}.jsonl")]
        good = sum(1 for l in lines if l.get('status') == 200)
        L('requests', it['wl'], f'{good}/{len(lines)} HTTP 200', json.dumps(lines[0].get('body'))[:300] if lines else '')
        ok &= good == len(lines) and len(lines) == N
finally:
    try:
        if attempted_uids:
            operation({'kind': 'reset', 'workloadUIDs': attempted_uids, 'reason': f'End serving smoke on {node}: reset owned workload scope'}, 'cleanup-reset')
    finally:
        t1 = now()
        for it in items:
            _, b = api('GET', f"/api/v1/observations/workloads/{it['uid']}?from={t0}&to={t1}&limit=1000"); open(f"{out}/observations-{it['wl']}.json", 'w').write(b)
        _, b = api('GET', f'/api/v1/observations/scheduler?from={t0}&to={t1}&limit=1000'); open(f'{out}/scheduler-actions.json', 'w').write(b)
        L('done', 'OK' if ok else 'FAIL')
sys.exit(0 if ok else 1)
