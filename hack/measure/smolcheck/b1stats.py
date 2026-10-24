# b1stats.py BEO: batch-1 latency (generation time) mean/p95/p99 from the raw evidence of its verified active-b1 ProfilingRuns.
import json, os, subprocess, sys, statistics as st
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
beo = sys.argv[1]
env = dict(os.environ)
for part in open(ENV['s3EnvFile']).read().replace('export ', '').split():
    k, v = part.split('=', 1); env[k] = v
AWS = ENV['awsCommand']
def s3(key):
    return json.loads(subprocess.run([AWS, '--endpoint-url', env['CHILL_S3_ENDPOINT'], 's3', 'cp', f"s3://{env['CHILL_S3_BUCKET']}/{key}", '-'],
                                     capture_output=True, text=True, env=env).stdout)
runs = json.loads(subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], '-n', ENV['namespace'], 'get', 'profilingrun', '-o', 'json'], capture_output=True, text=True).stdout)['items']
lat = []
for r in runs:
    n = r['metadata']['name']
    if not n.startswith(beo + '-active-b1-') or not (r.get('status') or {}).get('evidence'): continue
    raw = s3(s3(r['status']['evidence']['key'])['rawMeasurement']['key'])
    for rep in raw['runtime']['repetitionResults']:
        lat += rep['latencyMs']
if not lat: print(f'  {beo} b1: no verified samples'); sys.exit()
lat.sort(); q = lambda p: lat[min(len(lat) - 1, int(p * len(lat)))]
print(f'  {beo} b1 generation s: n={len(lat)} mean={st.mean(lat)/1e3:.2f} p95={q(.95)/1e3:.2f} p99={q(.99)/1e3:.2f} max={lat[-1]/1e3:.2f}')
