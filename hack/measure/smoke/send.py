# send.py WORKLOAD DATASET_DIR N OUT: send N raw samples one by one through the ChillServer gateway, save request summary + full response.
import json, os, sys, time, base64
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
sys.path.insert(0, str(Path(CONFIG["paths"]["repository"]) / "load-generator/src"))
from chill_loadgen.http_client import WorkloadHTTPClient
S = ENV["serverURL"]; CA = ENV["caFile"]
wl, ds, n, out = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
repository = Path(CONFIG["paths"]["repository"]).resolve()
if repository == Path(out).resolve() or repository in Path(out).resolve().parents:
    raise SystemExit("request output must remain outside the repository source tree")
client = WorkloadHTTPClient(S, bearer_token=open(ENV['tokenFile']).read().strip(), timeout_seconds=float(os.environ.get('TIMEOUT', '120')), ca_file=CA)
recs = sorted(os.listdir(ds))[:n]
with open(out, 'w') as f:
    for r in recs:
        fields = os.environ.get('FIELDS', '').split(',') if os.environ.get('FIELDS') else sorted(os.listdir(os.path.join(ds, r)))
        inputs = {name: open(os.path.join(ds, r, name), 'rb').read() for name in fields}
        t = time.time()
        try:
            resp = client.infer(wl, inputs); body = resp.body.decode('utf-8', 'replace')
            line = dict(record=r, inputs={k: len(v) for k, v in inputs.items()}, status=resp.status_code, requestID=resp.request_id,
                        latencyMs=round((time.time() - t) * 1000, 1), body=json.loads(body) if body.startswith('{') else body)
        except Exception as e:
            line = dict(record=r, error=repr(e), latencyMs=round((time.time() - t) * 1000, 1))
        f.write(json.dumps(line) + '\n'); f.flush(); print(json.dumps(line)[:400])
client.close()
