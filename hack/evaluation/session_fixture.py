#!/usr/bin/env python3
"""Extract the stored trials the profile-session derivation test replays.

Reads the archived control-387 raw measurements (read-only) and writes
internal/profiling/derivation/testdata/session_trials.json.gz: for every trial of
the selected conditions, its phase windows and counts, its native batch rows, its
node power samples and its post-load window. Rows keep their phase order; each
level names how many rows it owns. Times are microseconds from the
trial's first power sample. Native rows are delta coded (start from the
previous finish, preceding idle from that gap). The enqueue time is left out:
it only feeds dispatch occupancy, which the replay does not check.
Usage: session_fixture.py <evidence root> <output file>
"""
import gzip
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

CONDITIONS = {
    't1-c387-genesis2-ic-cuda': 'genesis2-ic-cuda',
    'p1-c387-e5-0017-jetsono': 'jetsono-ic-cpu',
    't1-c387r1-lattepanda-ic-cpu': 'lattepanda-ic-cpu',
}


def nanos(text):
    stamp, _, fraction = text.rstrip('Z').partition('.')
    whole = datetime.fromisoformat(stamp).replace(tzinfo=timezone.utc)
    return int(whole.timestamp()) * 10**9 + int((fraction + '000000000')[:9])


def micros(at, origin):
    return (at - origin) // 1000


def trial(raw):
    runtime, metrics = raw['runtime'], raw['metrics']
    power = sorted((nanos(s['observedAt']), s['watts']) for s in metrics['samples'] if s.get('watts') is not None)
    origin = power[0][0]
    levels, rows = [], []
    for phase in runtime['phases']:
        levels.append({
            'phase': phase['id'], 'start': micros(nanos(phase['measurementStartedAt']), origin),
            'end': micros(nanos(phase['measurementEndedAt']), origin),
            'sent': phase['sentInputs'], 'completed': phase['completedInputs'],
            'batches': len(phase['nativeSamples']),
        })
        rows += sorted(phase['nativeSamples'], key=lambda r: r['batchID'])
    batch = runtime['batchSize']
    if any(r['batchSize'] != batch for r in rows):
        raise ValueError('native batch size differs from the trial batch')
    previous_id, previous_finish = rows[0]['batchID'] - 1, None
    columns = {k: [] for k in ('batchIDDelta', 'start', 'duration', 'serviceResidual', 'idle')}
    for r in rows:
        start, finish = micros(nanos(r['startedAt']), origin), micros(nanos(r['finishedAt']), origin)
        columns['batchIDDelta'].append(r['batchID'] - previous_id)
        columns['start'].append(start - (previous_finish if previous_finish is not None else 0))
        columns['duration'].append(finish - start)
        columns['serviceResidual'].append(r['serviceDurationNanos'] // 1000 - (finish - start))
        gap = start - previous_finish if previous_finish is not None else 0
        columns['idle'].append(None if r['precedingIdleNanos'] is None else r['precedingIdleNanos'] // 1000 - gap)
        previous_id, previous_finish = r['batchID'], finish
    post = runtime['postLoad']
    times, watts, last = [], [], 0
    for at, w in power:
        times.append(micros(at, origin) - last)
        watts.append(w)
        last = micros(at, origin)
    return {
        'run': raw['runConfig']['measurement'].get('runName') or '', 'batch': batch, 'originNanos': origin,
        'memoryBytes': runtime['memoryBytes'], 'memoryKind': runtime['memorySource'],
        'postLoad': [micros(nanos(post['startedAt']), origin), micros(nanos(post['endedAt']), origin)],
        'levels': levels, 'rows': columns, 'powerTime': times, 'powerWatts': watts,
    }


def main(root, output):
    root = Path(root)
    entries = json.loads((root / 'new-evidence/final-t1-receipt.json').read_text()) + \
        json.loads((root / 'p1-current-plan/verified-evidence/receipt.json').read_text())
    fixture, seen = {name: [] for name in CONDITIONS.values()}, set()
    for entry in entries:
        name = CONDITIONS.get(entry['campaign'])
        if name is None or entry['uid'] in seen or '-active-' not in entry['run']:
            continue
        seen.add(entry['uid'])
        record = trial(json.loads(Path(entry['raw']).read_text()))
        record['run'] = entry['run']
        fixture[name].append(record)
    for trials in fixture.values():
        trials.sort(key=lambda t: t['run'])
    with gzip.open(output, 'wt', compresslevel=9) as out:
        json.dump(fixture, out, separators=(',', ':'))


if __name__ == '__main__':
    main(*sys.argv[1:])
