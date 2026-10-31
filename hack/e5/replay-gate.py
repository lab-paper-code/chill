#!/usr/bin/env python3
"""Build archived server sources and replay decisions and frozen commit guards offline."""
import argparse
import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

REPO = Path(__file__).resolve().parents[2]


def run(argv, cwd=REPO, **kwargs):
    return subprocess.run(argv, cwd=str(cwd), check=True, **kwargs)


def capture(argv):
    return run(argv, stdout=subprocess.PIPE).stdout


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def build(source, cache):
    sha = capture(['git', 'rev-parse', source if source != 'tree' else 'HEAD']).decode().strip()
    tool = {p.name: p.read_bytes() for p in sorted((REPO / 'hack/kappa').glob('*.go')) if not p.name.endswith('_test.go')}
    patch = capture(['git', 'diff', '--binary', 'HEAD']) if source == 'tree' else b''
    untracked = {}
    if source == 'tree':
        for name in capture(['git', 'ls-files', '--others', '--exclude-standard', '-z']).decode().split('\0'):
            if name:
                p = REPO / name
                if p.is_file():
                    untracked[name] = p.read_bytes()
    h = hashlib.sha256(sha.encode() + patch + capture(['go', 'version']) + capture(['go', 'env', 'GOOS', 'GOARCH']))
    for name, data in sorted({**tool, **untracked}.items()):
        h.update(name.encode() + b'\0' + data)
    key = h.hexdigest()
    dest = cache / 'builds' / key
    binary = dest / 'kappa'
    if binary.exists():
        return binary, json.loads((dest / 'build.json').read_text())
    dest.parent.mkdir(parents=True, exist_ok=True)
    began = time.monotonic()
    with tempfile.TemporaryDirectory(dir=str(dest.parent)) as temporary:
        work = Path(temporary)
        archive = work / 'source.tar'
        run(['git', 'archive', '--format=tar', '-o', str(archive), sha])
        src = work / 'source'
        src.mkdir()
        with tarfile.open(str(archive)) as stream:
            stream.extractall(str(src))  # Trusted local git archive, not external tar input.
        if patch:
            run(['git', 'apply', '--binary', '-'], cwd=src, input=patch)
        for name, data in untracked.items():
            p = src / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(data)
        target = src / 'hack/kappa'
        target.mkdir(parents=True, exist_ok=True)
        for p in target.glob('*.go'):
            p.unlink()
        for name, data in tool.items():
            (target / name).write_bytes(data)
        available = any('func SchedulerReductionCommit(' in p.read_text() for p in (src / 'internal/serving').glob('*.go') if not p.name.endswith('_test.go'))
        if available:
            # The committed shim assumes no guard; a source that defines the
            # production guard gets the real binding so rejections are its own.
            (target / 'guard_api.go').write_text('''package main
import "github.com/lab-paper-code/chill/internal/serving"
const guardAvailable = true
func reductionWorkloads(s serving.SchedulerSettings, snap serving.SchedulerSnapshot, rows []serving.CapacityRow, target serving.SchedulerCandidate) []string { return serving.SchedulerReductionWorkloads(s, snap, rows, target) }
func reductionCommit(s serving.SchedulerSettings, snap serving.SchedulerSnapshot, rows []serving.CapacityRow, target serving.SchedulerCandidate, uids []string) string { return serving.SchedulerReductionCommit(s, snap, rows, target, uids) }
''')
        if not any('func CapacityPlanningKappa(' in p.read_text() for p in (src / 'internal/serving').glob('*.go') if not p.name.endswith('_test.go')):
            # Historical planners used policy κ directly; do not transplant the
            # current safety envelope into the source used as a replay baseline.
            (target / 'kappa_api.go').write_text('''package main
import "github.com/lab-paper-code/chill/internal/serving"
func planningKappa(in serving.CapacityInput, rows map[string]serving.CapacityRow) float64 {
    row := rows[in.Key()]
    if row.Kappa == nil || row.Status == serving.CapacityPending { return 0 }
    return *row.Kappa
}
''')
        env = dict(os.environ, GOFLAGS='-buildvcs=false', GOMAXPROCS='2')
        with (work / 'build.log').open('w') as log:
            run(['flock', '/tmp/chill-gotest.lock', 'nice', '-n', '19', 'go', 'build', '-p', '1', '-o', str(work / 'kappa'), './hack/kappa'], cwd=src, env=env, stdout=log, stderr=subprocess.STDOUT)
        meta = dict(source=source, sha=sha, cacheKey=key, guardAvailable=available,
                    harnessSHA256={k: hashlib.sha256(v).hexdigest() for k, v in tool.items()},
                    treePatchSHA256=hashlib.sha256(patch).hexdigest() if source == 'tree' else None,
                    buildSeconds=time.monotonic() - began)
        (work / 'build.json').write_text(json.dumps(meta, indent=2) + '\n')
        src_cleanup = work / 'source'
        shutil.rmtree(str(src_cleanup))
        archive.unlink()
        work.rename(dest)
    return binary, meta


def prepare(stage, cache):
    files = sorted((stage / 'obs').glob('*/scheduler.jsonl*'))
    if not files:
        raise ValueError('no scheduler records: ' + str(stage))
    catalogs = sorted((stage / 'obs').glob('*/catalog.jsonl*'))
    requests = sorted((stage / 'obs').glob('*/requests.jsonl*'))
    if not catalogs or not requests:
        raise ValueError('catalog and requests archives are required: ' + str(stage))
    inputs = [dict(path=str(p), sha256=digest(p)) for p in files + catalogs + requests]
    key = hashlib.sha256(json.dumps(inputs, sort_keys=True).encode()).hexdigest()
    folder = cache / 'records' / key
    folder.mkdir(parents=True, exist_ok=True)
    records = folder / 'records.jsonl.gz'
    if not records.exists():
        # Sort decision ticks across hour files, retaining only one identical
        # copy of an exported tick. Conflicting duplicates fail closed.
        ticks = {}
        for p in files:
            opener = gzip.open if p.suffix == '.gz' else open
            with opener(str(p), 'rt') as stream:
                for line in stream:
                    if '"decision"' not in line:
                        continue
                    r = json.loads(line)
                    if r.get('decision') is None:
                        continue
                    if not r.get('snapshot'):
                        raise ValueError('decision without snapshot: ' + r['at'])
                    data = json.dumps(r, separators=(',', ':'), sort_keys=True)
                    if r['at'] in ticks and ticks[r['at']] != data:
                        raise ValueError('conflicting duplicate tick: ' + r['at'])
                    ticks[r['at']] = data
        if not ticks:
            raise ValueError('no decision ticks: ' + str(stage))
        with gzip.open(str(records) + '.tmp', 'wt') as stream:
            for at in sorted(ticks):
                stream.write(ticks[at] + '\n')
        Path(str(records) + '.tmp').replace(records)
    catalog = folder / 'catalog.jsonl.gz'
    if not catalog.exists():
        with gzip.open(str(catalog) + '.tmp', 'wb') as output:
            for p in catalogs:
                opener = gzip.open if p.suffix == '.gz' else open
                with opener(str(p), 'rb') as stream:
                    shutil.copyfileobj(stream, output)
        Path(str(catalog) + '.tmp').replace(catalog)
    arrivals = folder / 'arrivals.jsonl.gz'
    if not arrivals.exists():
        indexed = {}
        for p in requests:
            opener = gzip.open if p.suffix == '.gz' else open
            with opener(str(p), 'rt') as stream:
                for line in stream:
                    row = json.loads(line)
                    if row.get('kind') not in ('assigned', 'final'):
                        continue
                    identity = row['requestID']
                    if identity in indexed and indexed[identity][0] == 'final' and row['kind'] != 'final':
                        continue
                    accepted = any(row.get(k) is not None for k in ('acceptedNanos', 'dispatchedAt', 'dispatchedNanos'))
                    arrival = dict(id=identity, at=row['receivedAt'], workloadUID=row.get('workloadUID', ''),
                                   definitionID=row.get('definitionID', ''), lifetimeNanos=row.get('lifetimeNanos', 0),
                                   accepted=accepted, compute=row.get('validatedNanos') is not None or accepted,
                                   outcome=row.get('outcome', ''))
                    indexed[identity] = row['kind'], arrival
        with gzip.open(str(arrivals) + '.tmp', 'wt') as stream:
            for _, arrival in sorted(indexed.values(), key=lambda value: (value[1]['at'], value[1]['id'])):
                stream.write(json.dumps(arrival, separators=(',', ':')) + '\n')
        Path(str(arrivals) + '.tmp').replace(arrivals)
    return records, catalog, arrivals, inputs


def summarize(compare, guard):
    if len(compare) != len(guard) or not compare:
        raise ValueError('compare/guard coverage differs or is empty')
    bad = []
    rejected = future_rejected = recovery_rejected = 0
    for c, g in zip(compare, guard):
        if c['at'] != g['at'] or c['targetKey'] != g['targetKey']:
            raise ValueError('compare/guard tick mismatch')
        checks = [x for x in g.get('checks', []) if x['rejected']]
        rejected += any(x['offset'] == 0 for x in checks)
        future_rejected += any(x['offset'] > 0 for x in checks)
        # One bad tick per recorded origin, even if several future snapshots
        # reject it. Mixed-workload owners count if ANY workload gains supply.
        blocked = [x for x in checks if any(d['supplyIncrease'] for d in x['demand'])]
        recovery_rejected += bool(blocked)
        reasons = []
        if c['lostRecovery']:
            reasons.append('keep-short recorded supply increase replaced by Keep/no target')
        if blocked:
            reasons.append('guard rejected recorded supply increase')
        if reasons:
            bad.append(dict(at=c['at'], targetID=g.get('targetID'), reasons=reasons, checks=blocked))
    return dict(ticks=len(compare), disagreements=sum(c['disagreement'] for c in compare),
                populationMismatchTicks=sum(c.get('populationMismatch', False) for c in compare),
                badTicks=len(bad), admissionRejectedTicks=rejected,
                futureRejectedTicks=future_rejected, recoveryRejectedTicks=recovery_rejected,
                badTickDetails=bad)


def compare_bad_ticks(summary, baseline):
    def reasons(run):
        return {(tick['at'], tick['targetID']): set(tick['reasons']) for tick in run['badTickDetails']}
    current, previous = reasons(summary), reasons(baseline)
    new = [dict(at=at, targetID=target, reasons=sorted(added))
           for (at, target), causes in sorted(current.items(), key=lambda item: (item[0][0], item[0][1] or ''))
           if (added := causes - previous.get((at, target), set()))]
    return dict(newBadTicks=new,
                fixedTicks=len(previous.keys() - current.keys()), verdict='FAIL' if new else 'PASS')


def markdown(report):
    text = ['# Offline decision + guard replay gate', '',
            'Bad tick limit: %d (reference %s; candidates must have zero added bad reasons by recorded at + targetID per stage); verdict: **%s**.' % (report['maxBadTicks'], report.get('reference'), report['verdict']), '',
            '| source | stage | ticks | disagreements | population mismatch | bad ticks | new bad ticks | fixed ticks | guard t0 | guard next | supply-increase rejected | seconds | verdict |',
            '|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|']
    for r in report['runs']:
        text.append('| %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %.3f | %s |' % (
            r['source'], r['stage'], r['ticks'], r['disagreements'], r['populationMismatchTicks'], r['badTicks'],
            len(r['newBadTicks']), r['fixedTicks'], r['admissionRejectedTicks'], r['futureRejectedTicks'], r['recoveryRejectedTicks'],
            r['elapsedSeconds'], r['verdict']))
    text.append('')
    for r in report['runs']:
        if r['newBadTicks']:
            text.append('New bad ticks (%s / %s): `%s`' % (r['source'], r['stage'], json.dumps(r['newBadTicks'], ensure_ascii=False)))
    text += ['', 'Definitions: keep-short = demand > current Ready positive-route κ; supply increase = target positive-route κ > keep κ (per workload).',
             'Bad tick = keep-short recorded supply increase changed to Keep/no target, OR guard rejects a recorded supply increase at admission or any of the next N ticks; count each origin once.',
             'Demand = max(recorded minimumDemand, snapshot.scaleDown.rate). κ sums the source planner’s capacity for positive integer routes: its batch-1 safety envelope when available, otherwise historical policy row κ; not measured throughput or a complete backlog/SLO judgment.',
             'Compare includes route weights and wait limits; disagreements alone do not fail the gate. Guard reasons are the unmodified server API result, not an inferred internal predicate.',
             'Catalog references are decoded by the archived server decoder; omitted arrivals are restored from real request rows (final supersedes assigned; native supplements excluded). Population mismatches expose final-state versus original capture differences.',
             'Targets and reduction workload scope stay frozen at admission; future snapshots are recorded observations, not a closed-loop simulator or completion callbacks.',
             'Historical source without the production guard reports GuardNotPresentInSource (not a rejection); its guardAvailable=false is explicit in build and tick evidence.',
             'All decision ticks exported with each stage are included (including setup/teardown); duplicate identical exported ticks are deduplicated. Future horizon truncates at end of recording.',
             'No cluster operations or product planner changes. See per-run compare.jsonl and guard.jsonl for every tick.', '']
    return '\n'.join(text)


def self_check():
    d = dict(supplyIncrease=True)
    c = dict(at='a', targetKey='t', disagreement=True, lostRecovery=False)
    g = dict(at='a', targetKey='t', checks=[dict(at='b', offset=1, rejected=True, demand=[d])])
    s = summarize([c], [g])
    assert s['badTicks'] == 1 and s['futureRejectedTicks'] == 1
    g['checks'].append(g['checks'][0])
    assert summarize([c], [g])['badTicks'] == 1
    g['checks'] = []
    assert summarize([c], [g])['badTicks'] == 0
    c['lostRecovery'] = True
    assert summarize([c], [g])['badTicks'] == 1
    baseline = dict(badTickDetails=[dict(at='a', targetID='recorded', reasons=['lostRecovery'])])
    candidate = dict(badTickDetails=[dict(at='b', targetID='recorded', reasons=['lostRecovery'])])
    delta = compare_bad_ticks(candidate, baseline)
    assert delta == dict(newBadTicks=[dict(at='b', targetID='recorded', reasons=['lostRecovery'])], fixedTicks=1, verdict='FAIL')
    candidate['badTickDetails'][0]['at'] = 'a'
    assert compare_bad_ticks(candidate, baseline)['verdict'] == 'PASS'
    candidate['badTickDetails'][0]['reasons'].append('guard rejection')
    delta = compare_bad_ticks(candidate, baseline)
    assert delta == dict(newBadTicks=[dict(at='a', targetID='recorded', reasons=['guard rejection'])], fixedTicks=0, verdict='FAIL')
    candidate['badTickDetails'][0]['reasons'] = []
    assert compare_bad_ticks(candidate, baseline)['verdict'] == 'PASS'
    candidate['badTickDetails'][0]['reasons'] = ['lostRecovery']
    candidate['badTickDetails'][0]['targetID'] = 'other'
    assert compare_bad_ticks(candidate, baseline)['verdict'] == 'FAIL'
    assert compare_bad_ticks(dict(badTickDetails=[]), baseline) == dict(newBadTicks=[], fixedTicks=1, verdict='PASS')
    print('replay-gate self-check: PASS')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source', action='append', help='git SHA/ref or tree; repeat for a comparison matrix')
    p.add_argument('--stage', action='append', type=Path, help='recorded stage directory; repeat')
    p.add_argument('--rows', type=Path)
    p.add_argument('--profiles', type=Path)
    p.add_argument('--scheduler', type=Path, help='base scheduler dump carrying row capacity settings')
    p.add_argument('--out', type=Path)
    p.add_argument('--cache', type=Path, default=Path.home() / '.cache/chill-replay-gate')
    p.add_argument('--next', type=int, default=3)
    p.add_argument('--max-bad-ticks', type=int, default=0)
    p.add_argument('--reference', help='source whose per-stage bad ticks are the baseline; candidates must have zero added bad reasons by recorded at + targetID (default: first --source)')
    p.add_argument('--self-check', action='store_true')
    a = p.parse_args()
    if a.self_check:
        self_check()
        return 0
    if not all((a.source, a.stage, a.rows, a.profiles, a.scheduler, a.out)):
        p.error('--source --stage --rows --profiles --scheduler --out are required')
    if a.next < 0 or a.max_bad_ticks < 0:
        p.error('--next and --max-bad-ticks must be nonnegative')
    a.cache = a.cache.resolve()
    a.out = a.out.resolve()
    a.out.mkdir(parents=True, exist_ok=False)
    began = time.monotonic()
    report = dict(createdAtUTC=datetime.datetime.now(datetime.timezone.utc).isoformat(), nextTicks=a.next,
                  maxBadTicks=a.max_bad_ticks, inputs={k: dict(path=str(getattr(a, k).resolve()), sha256=digest(getattr(a, k))) for k in ('rows', 'profiles', 'scheduler')}, runs=[])
    try:
        stages = [(s.resolve(), *prepare(s.resolve(), a.cache)) for s in a.stage]
        reference = a.reference or a.source[0]
        baselines = {}
        report['reference'] = reference
        for number, source in enumerate(a.source):
            binary, meta = build(source, a.cache)
            for stage, records, catalog, arrivals, inputs in stages:
                folder = a.out / ('%02d-%s' % (number, meta['sha'][:8])) / stage.name
                folder.mkdir(parents=True)
                start = time.monotonic()
                results = {}
                for mode in ('compare', 'guard'):
                    argv = [str(binary), '-mode', mode, '-format', 'jsonl', '-next', str(a.next),
                            '-scheduler', str(a.scheduler.resolve()), '-profiles', str(a.profiles.resolve()),
                            '-records', str(records), '-catalog', str(catalog), '-arrivals', str(arrivals),
                            '-rows', str(a.rows.resolve()), '-workers', '2']
                    with (folder / (mode + '.jsonl')).open('w') as stdout, (folder / (mode + '.stderr')).open('w') as stderr:
                        run(['nice', '-n', '19'] + argv, env=dict(os.environ, GOMAXPROCS='2'), stdout=stdout, stderr=stderr)
                    with (folder / (mode + '.jsonl')).open() as stream:
                        results[mode] = [json.loads(line) for line in stream]
                summary = summarize(results['compare'], results['guard'])
                baseline = baselines.get(stage.name)
                if baseline is None:  # the reference source itself is the baseline, not a candidate
                    summary.update(newBadTicks=[], fixedTicks=0, verdict='REFERENCE')
                else:
                    summary.update(compare_bad_ticks(summary, baseline))
                summary.update(source=source, sourceSHA=meta['sha'], stage=stage.name, build=meta,
                               records=inputs, elapsedSeconds=time.monotonic() - start,
                               evidence=str(folder), baseline=baseline and {k: baseline[k] for k in ('source', 'badTicks', 'recoveryRejectedTicks')})
                if source == reference:
                    baselines[stage.name] = summary
                (folder / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
                report['runs'].append(summary)
                candidates = [r for r in report['runs'] if r['verdict'] != 'REFERENCE']
                report['verdict'] = 'PASS' if candidates and all(r['verdict'] == 'PASS' for r in candidates) else 'FAIL'
                report['elapsedSeconds'] = time.monotonic() - began
                (a.out / 'summary.json').write_text(json.dumps(report, indent=2) + '\n')
                (a.out / 'summary.md').write_text(markdown(report))
        print(markdown(report))
        return 0 if report['verdict'] == 'PASS' else 2
    except Exception as exc:
        report.update(verdict='ERROR', error=str(exc), elapsedSeconds=time.monotonic() - began)
        (a.out / 'summary.json').write_text(json.dumps(report, indent=2) + '\n')
        raise


if __name__ == '__main__':
    sys.exit(main())
