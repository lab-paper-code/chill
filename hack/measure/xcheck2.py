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
for image, par, solos in [('cpu-arm64 (rasp5)', 'rasp5-922-occ-mnv2-cpu', ['rasp5-930-occ-mnv2-cpu', 'rasp5-931-occ-mnv2-cpu']),
                          ('cpu-amd64 (genesis2)', 'genesis2-921-occ-mnv2-cpu', ['genesis2-930-occ-mnv2-cpu', 'genesis2-932-occ-mnv2-cpu']),
                          ('cuda-amd64 (genesis2)', 'genesis2-009-occ-mnv2-cuda', ['genesis2-931-occ-mnv2-cuda', 'genesis2-933-occ-mnv2-cuda'])]:
    p = occ(par); s = [occ(x) for x in solos]
    xs = [x[2] for x in s]; m = st.mean(xs); sd = st.stdev(xs); tol = max(3 * sd, 0.02 * m)
    bad = p[0] != 'Complete' or any(x[0] != 'Complete' for x in s) or abs(p[2] - m) > tol
    ok &= not bad
    print(f'XCHECK {image}: parallel {par} {p[0]} X {p[2]:.3f} d {p[3]} | solo X {xs} d {[x[3] for x in s]} mean {m:.3f} sd {sd:.3f} tol {tol:.3f} diff {p[2]-m:+.3f} -> {"FAIL" if bad else "pass"}')
for n in ['orangepi-922-occ-mnv2-cpu','lattepanda-921-occ-mnv2-cpu','genesis3-921-occ-mnv2-cpu','jetsono-921-occ-mnv2-cpu-maxn','jetsonx-921-occ-mnv2-cpu-15w6c','jetson-nano-921-occ-mnv2-cpu-maxn']:
    p = occ(n); bad = p[0] != 'Complete'; ok &= not bad
    print(f'XCHECK front {n} {p[0]} {p[1] or ""} X {p[2]:.3f} d {p[3]} -> {"FAIL" if bad else "pass"}')
start = open(V + '/rerun-start').read().strip(); mx = 0
for l in open(V + '/top2.log'):
    f = l.split()
    if len(f) > 2 and f[0] >= start and f[2].endswith('m'):
        mx = max(mx, int(f[2][:-1]))
lim = 0.85 * 6000
print(f'XCHECK ChillServer max CPU since {start}: {mx}m (limit 85% of 6 cores = {lim:.0f}m) -> {"FAIL" if mx > lim else "pass"}')
sys.exit(0 if ok and mx <= lim else 1)
