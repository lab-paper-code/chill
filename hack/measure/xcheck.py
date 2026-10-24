# xcheck.py: README 7 X rule. Parallel front d X must lie within max(3σ, 2% of mean) of the two solo X of the same runtime image; ChillServer max CPU <= 3500m.
import json, subprocess, statistics as st, sys
import os
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
V = CONFIG["paths"]["campaign"]
def occ(n):
    o = json.loads(subprocess.run([ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'], '-n', ENV['namespace'], 'get', 'occupancy', n, '-o', 'json'], capture_output=True, text=True).stdout)
    s = o.get('status', {}); return s.get('phase'), s.get('reason'), float(s.get('throughput') or 0), s.get('perRequestNanos')
ok = True
for image, par, solos in [('cpu-arm64 (rasp5)', 'rasp5-007-occ-mnv2-cpu', ['rasp5-930-occ-mnv2-cpu', 'rasp5-931-occ-mnv2-cpu']),
                          ('cpu-amd64 (genesis2)', 'genesis2-007-occ-mnv2-cpu', ['genesis2-930-occ-mnv2-cpu', 'genesis2-932-occ-mnv2-cpu']),
                          ('cuda-amd64 (genesis2)', 'genesis2-009-occ-mnv2-cuda', ['genesis2-931-occ-mnv2-cuda', 'genesis2-933-occ-mnv2-cuda'])]:
    p = occ(par); s = [occ(x) for x in solos]
    xs = [x[2] for x in s]; m = st.mean(xs); sd = st.stdev(xs); tol = max(3 * sd, 0.02 * m)
    bad = p[0] != 'Complete' or any(x[0] != 'Complete' for x in s) or abs(p[2] - m) > tol
    ok &= not bad
    print(f'XCHECK {image}: parallel {par} {p[0]} X {p[2]:.3f} d {p[3]} | solo X {xs} d {[x[3] for x in s]} mean {m:.3f} sd {sd:.3f} tol {tol:.3f} diff {p[2]-m:+.3f} -> {"FAIL" if bad else "pass"}')
start = open(V + '/lanes-start').read().strip(); mx = 0
for l in open(V + '/top.log'):
    f = l.split()
    if len(f) > 2 and f[0] >= start and f[2].endswith('m'):
        mx = max(mx, int(f[2][:-1]))
print(f'XCHECK ChillServer max CPU since {start}: {mx}m -> {"FAIL" if mx > 3500 else "pass"}')
sys.exit(0 if ok and mx <= 3500 else 1)
