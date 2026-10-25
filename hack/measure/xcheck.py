"""README 7 X rule: parallel X within max(3σ, 2% of solo mean).

Run `python3 hack/measure/xcheck.py initial|rerun|final` with
CHILL_EXPERIMENT_CONFIG set. No arguments runs the offline self-check.
Initial uses lanes-start/top.log and 3500m; rerun/final use
rerun-start/top2.log and 85% of 6000m. Final also requires Mi memory rows.
"""
import argparse
import json
import os
import statistics as st
import subprocess
import sys
from pathlib import Path

FRONTS = ['orangepi', 'lattepanda', 'genesis3', 'jetsono', 'jetsonx', 'jetson-nano']
PRESETS = {
    'initial': (['rasp5-007-occ-mnv2-cpu', 'genesis2-007-occ-mnv2-cpu'], [], 'lanes-start', 'top.log', 3500),
    'rerun': (['rasp5-922-occ-mnv2-cpu', 'genesis2-921-occ-mnv2-cpu'], [
        'orangepi-922-occ-mnv2-cpu', 'lattepanda-921-occ-mnv2-cpu',
        'genesis3-921-occ-mnv2-cpu', 'jetsono-921-occ-mnv2-cpu-maxn',
        'jetsonx-921-occ-mnv2-cpu-15w6c', 'jetson-nano-921-occ-mnv2-cpu-maxn'],
        'rerun-start', 'top2.log', 0.85 * 6000),
    'final': (None, None, 'rerun-start', 'top2.log', 0.85 * 6000),
}


def capture(preset, final=None):
    parallel, fronts, start, log, limit = PRESETS[preset]
    if preset == 'final':
        parallel = [final['rasp5'], final['genesis2']]
        fronts = [final[n] for n in FRONTS]
    return parallel, fronts, start, log, limit


def cpu_peak(lines, start, preset):
    mx = 0
    for line in lines:
        f = line.split()
        if (len(f) > 2 and f[0] >= start and f[2].endswith('m')
                and (preset != 'final' or len(f) > 3 and f[3].endswith('Mi'))):
            mx = max(mx, int(f[2][:-1]))
    return mx


def run(preset):
    config = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
    env, campaign = config['measure'], Path(config['paths']['campaign'])
    final = json.loads((campaign / 'feeder-final.json').read_text()) if preset == 'final' else None
    parallel, fronts, start_file, log_file, limit = capture(preset, final)

    def occ(name):
        o = json.loads(subprocess.run([env['kubectl'], '--kubeconfig', env['kubeconfig'], '--context', env['kubeContext'], '-n', env['namespace'], 'get', 'occupancy', name, '-o', 'json'], capture_output=True, text=True).stdout)
        s = o.get('status', {})
        return s.get('phase'), s.get('reason'), float(s.get('throughput') or 0), s.get('perRequestNanos')

    ok = True
    for image, par, solos in [
            ('cpu-arm64 (rasp5)', parallel[0], ['rasp5-930-occ-mnv2-cpu', 'rasp5-931-occ-mnv2-cpu']),
            ('cpu-amd64 (genesis2)', parallel[1], ['genesis2-930-occ-mnv2-cpu', 'genesis2-932-occ-mnv2-cpu']),
            ('cuda-amd64 (genesis2)', 'genesis2-009-occ-mnv2-cuda', ['genesis2-931-occ-mnv2-cuda', 'genesis2-933-occ-mnv2-cuda'])]:
        p = occ(par); s = [occ(x) for x in solos]
        xs = [x[2] for x in s]; m = st.mean(xs); sd = st.stdev(xs); tol = max(3 * sd, 0.02 * m)
        bad = p[0] != 'Complete' or any(x[0] != 'Complete' for x in s) or abs(p[2] - m) > tol
        ok &= not bad
        print(f'XCHECK {image}: parallel {par} {p[0]} X {p[2]:.3f} d {p[3]} | solo X {xs} d {[x[3] for x in s]} mean {m:.3f} sd {sd:.3f} tol {tol:.3f} diff {p[2]-m:+.3f} -> {"FAIL" if bad else "pass"}')
    for n in fronts:
        p = occ(n); bad = p[0] != 'Complete'; ok &= not bad
        print(f'XCHECK front {n} {p[0]} {p[1] or ""} X {p[2]:.3f} d {p[3]} -> {"FAIL" if bad else "pass"}')
    start = (campaign / start_file).read_text().strip()
    with (campaign / log_file).open() as lines:
        mx = cpu_peak(lines, start, preset)
    print(f'XCHECK ChillServer max CPU since {start}: {mx}m (limit {limit:.0f}m) -> {"FAIL" if mx > limit else "pass"}')
    return 0 if ok and mx <= limit else 1


def self_check():
    initial, rerun = capture('initial'), capture('rerun')
    assert initial == (['rasp5-007-occ-mnv2-cpu', 'genesis2-007-occ-mnv2-cpu'], [], 'lanes-start', 'top.log', 3500)
    assert rerun[0] == ['rasp5-922-occ-mnv2-cpu', 'genesis2-921-occ-mnv2-cpu']
    assert len(rerun[1]) == 6 and rerun[1][0] == 'orangepi-922-occ-mnv2-cpu'
    final = {n: n + '-final' for n in ['rasp5', 'genesis2'] + FRONTS}
    selected = capture('final', final)
    assert selected[:2] == (['rasp5-final', 'genesis2-final'], [final[n] for n in FRONTS])
    assert selected[2:] == rerun[2:] == ('rerun-start', 'top2.log', 0.85 * 6000)
    lines = ['00 pod 9000m 1Mi', '02 pod 3500m', '03 pod 5100m 1Mi', '04 pod 6000m 1Gi', '05 pod 7000 1Mi']
    assert cpu_peak(lines, '01', 'initial') == cpu_peak(lines, '01', 'rerun') == 6000
    assert cpu_peak(lines, '01', 'final') == 5100
    print('PASS: initial/rerun/final CR selection, CPU windows/filters/limits')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('preset', nargs='?', choices=PRESETS)
    args = parser.parse_args()
    sys.exit(run(args.preset) if args.preset else self_check())
