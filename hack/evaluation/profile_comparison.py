#!/usr/bin/env python3
"""Report archived native batch service against published profile ServiceSamples.

Ported from the profile-serving-equivalence stage-decomposition prototypes.
This diagnostic never changes profiles or evaluation judgments.
"""
import argparse
from bisect import bisect_left, bisect_right
from collections import Counter, defaultdict
from decimal import Decimal
import gzip
import json
import math
from pathlib import Path
import statistics

import evidence_io

_FIELDS = ('instance', 'profileID', 'batchID', 'batchSize', 'serviceDurationNanos',
           'precedingIdleNanos', 'nativeRunningAt', 'nativeFinishedAt', 'nativeElapsedMS')


def stats(values):
    values = sorted(values)
    return {'n': len(values), 'p50': statistics.median(values) if values else None,
            'p95': values[math.ceil(.95 * len(values)) - 1] if values else None}


def number(value, name, positive=False):
    if (type(value) not in (int, float) or not math.isfinite(value) or
            (value <= 0 if positive else value < 0)):
        raise ValueError('invalid ' + name)
    return value


def idle_bin(value, edges):
    if value is None:
        return 'unknown'
    number(value, 'precedingIdleNanos')
    index = bisect_right(edges, value) - 1
    low = edges[index] / 1e6
    high = format(edges[index + 1] / 1e6, 'g') if index + 1 < len(edges) else 'inf'
    return '[%s,%s)' % (format(low, 'g'), high)


def json_rows(paths):
    for path in paths:
        opener = gzip.open if path.suffix == '.gz' else open
        with opener(path, 'rt', encoding='utf-8') as handle:
            for line, text in enumerate(handle, 1):
                row = json.loads(text)
                if not isinstance(row, dict):
                    raise ValueError('%s:%s: record must be an object' % (path, line))
                yield row


def fold_requests(paths):
    records = defaultdict(list)
    for row in json_rows(paths):
        if row.get('kind') in ('final', 'native') and row.get('requestID'):
            records[row['requestID']].append({key: row[key] for key in _FIELDS if key in row})
    folded = {}
    for rid, rows in records.items():
        result = {}
        for key in _FIELDS:
            candidates = rows
            # Queued/running updates have partial elapsed values, not completed spans.
            if key == 'nativeElapsedMS':
                candidates = [row for row in rows if row.get('nativeFinishedAt')]
            values = [row[key] for row in candidates if row.get(key) is not None and
                      not (key in ('batchID', 'batchSize', 'serviceDurationNanos') and row[key] == 0)]
            if values:
                if any(value != values[0] for value in values):
                    raise ValueError('conflicting %s for request %s' % (key, rid))
                result[key] = values[0]
        folded[rid] = result
    return folded


def comparison(key, serving, profile, conditioned_idle):
    pid, batch, gap, other = key
    observed, reference = stats(serving), stats(profile)
    result = {'profileID': pid, 'batchSize': batch, 'otherArrived': other,
              'serving': observed, 'profile': reference,
              'p50Ratio': observed['p50'] / reference['p50'] if serving and profile else None,
              'p95Ratio': observed['p95'] / reference['p95'] if serving and profile else None}
    if conditioned_idle:
        result.update(idleBin=gap, profileIdleBinSupported=bool(profile))
    return result


def analyze(paths, profiles, edges, cohort=None):
    rows = fold_requests(paths)
    receives = defaultdict(list)
    received_by_request = {}
    batches = defaultdict(dict)
    counts = Counter()
    for rid, row in rows.items():
        instance = row.get('instance')
        if instance and row.get('nativeFinishedAt') and row.get('nativeElapsedMS') is not None:
            elapsed = number(row['nativeElapsedMS'], 'nativeElapsedMS')
            receive = evidence_io.timestamp_ns(row['nativeFinishedAt']) - round(elapsed * 1e6)
            receives[instance].append(receive)
            received_by_request[rid] = receive
        else:
            counts['requestsWithoutDerivedReceive'] += 1
        if instance and row.get('batchID'):
            batches[(instance, row['batchID'])][rid] = row
    for times in receives.values():
        times.sort()

    profile_cells = defaultdict(list)
    profile_all = defaultdict(list)
    seen_profiles = set()
    for profile in profiles['profiles']:
        pid = profile['id']
        if pid in seen_profiles:
            raise ValueError('duplicate profile ID: ' + pid)
        seen_profiles.add(pid)
        seen_batches = set()
        for batch in profile['batches']:
            size = batch['batch']
            if size in seen_batches:
                raise ValueError('duplicate profile batch: ' + pid)
            seen_batches.add(size)
            for sample in batch['serviceSamples']:
                duration = number(sample['serviceDurationNanos'], 'profile serviceDurationNanos', True) / 1e6
                gap = idle_bin(sample.get('precedingIdleNanos'), edges)
                profile_all[(pid, size)].append(duration)
                # Unknown/startup idle cannot establish measured support for an idle cell.
                if gap != 'unknown':
                    profile_cells[(pid, size, gap)].append(duration)

    serving_cells = defaultdict(list)
    serving_all = defaultdict(list)
    for (instance, _), members in batches.items():
        if cohort is not None and not cohort.intersection(members):
            continue
        # A supplement can finish a timed-out request. Count the actual batch once.
        complete = [row for row in members.values() if row.get('serviceDurationNanos')]
        if not complete:
            counts['batchesWithoutServiceSample'] += 1
            continue
        row = complete[0]
        for key in ('profileID', 'batchSize', 'serviceDurationNanos', 'precedingIdleNanos',
                    'nativeRunningAt', 'nativeFinishedAt'):
            values = [member[key] for member in complete if member.get(key) is not None]
            if any(value != values[0] for value in values):
                raise ValueError('conflicting batch ' + key)
            if values:
                row = {**row, key: values[0]}
        pid, size = row.get('profileID'), row.get('batchSize')
        if not pid or not size:
            counts['batchesWithoutProfileOrSize'] += 1
            continue
        duration = number(row['serviceDurationNanos'], 'serving serviceDurationNanos', True) / 1e6
        gap = idle_bin(row.get('precedingIdleNanos'), edges)
        other = None
        if row.get('nativeRunningAt') and row.get('nativeFinishedAt'):
            start = evidence_io.timestamp_ns(row['nativeRunningAt'])
            end = evidence_io.timestamp_ns(row['nativeFinishedAt'])
            if end < start:
                raise ValueError('native finish precedes running')
            times = receives[instance]
            arrivals = bisect_left(times, end) - bisect_left(times, start)
            # Requests belonging to this batch are not "another request".
            arrivals -= sum(start <= received_by_request[rid] < end for rid in members
                            if rid in received_by_request)
            other = arrivals > 0
        serving_cells[(pid, size, gap, other)].append(duration)
        serving_all[(pid, size, 'all', other)].append(duration)
        counts['comparedBatches'] += 1

    def ordered(items):
        return sorted(items, key=lambda item: (item[0], item[1], item[2], str(item[3])))

    return {
        'units': 'ms', 'quantiles': {'p50': 'median', 'p95': 'nearest-rank'},
        'idleBinEdgesMS': [edge / 1e6 for edge in edges], 'idleBins': '[lower,upper); last upper=inf; missing=unknown',
        'servingConditioning': ['profileID', 'batchSize', 'idleBin', 'otherArrived'],
        'profileConditioning': ['profileID', 'batchSize', 'idleBin'],
        'profileIngressConditioning': 'not conditioned on ingress',
        'ratioMeaning': 'serving / profile; profile reference is not conditioned on ingress',
        'profileSupportMeaning': 'profileIdleBinSupported tests measured idle-bin support only, not ingress support',
        'receiveDefinition': 'nativeFinishedAt - nativeElapsedMS; same instance; running interval [nativeRunningAt,nativeFinishedAt); exclude own batch requests',
        'limitations': [
            'Only requests with observed native finish and elapsed contribute derived receives; absence is not proof of no ingress.',
            'Ingress association does not establish causality.',
            'Profile ID joins do not verify actual serving runtime image or other instance conditions.',
            'Unknown idle is not zero and cannot establish profile idle-bin support.'],
        'counts': dict(counts),
        'cells': [comparison(key, serving_cells[key], profile_cells[key[:3]], True)
                  for key in ordered(serving_cells)],
        # Reproduce the plan table separately; these rows deliberately marginalize idle.
        'marginalByIngress': [comparison(key, serving_all[key], profile_all[key[:2]], False)
                              for key in ordered(serving_all)],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--raw', required=True, action='append',
                        help='fetched obs directory or requests.jsonl[.gz]; repeat for segments')
    parser.add_argument('--clients', action='append', default=[],
                        help='client JSONL or measured directory; optional output request cohort')
    parser.add_argument('--profiles', required=True, help='stored profile catalog JSON with profiles[]')
    parser.add_argument('--idle-bin-edges-ms', required=True, help='comma-separated increasing edges starting at 0')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    try:
        decimal_edges = [Decimal(value) * 1_000_000 for value in args.idle_bin_edges_ms.split(',')]
        if (not decimal_edges or any(not edge.is_finite() or edge != int(edge) for edge in decimal_edges)
                or decimal_edges[0] != 0 or any(a >= b for a, b in zip(decimal_edges, decimal_edges[1:]))):
            raise ValueError('idle bin edges must increase from zero with nanosecond precision')
        edges = [int(edge) for edge in decimal_edges]
        paths = set()
        for value in args.raw:
            path = Path(value)
            found = list(path.glob('*/requests.jsonl.gz')) if path.is_dir() else [path]
            if not found:
                raise ValueError('no request segments: ' + value)
            paths.update(found)
        clients = []
        for value in args.clients:
            path = Path(value)
            found = list(path.glob('*-client.jsonl')) if path.is_dir() else [path]
            if not found:
                raise ValueError('no client files: ' + value)
            clients.extend(found)
        cohort = {row['requestID'] for row in json_rows(clients) if row.get('requestID')} if clients else None
        profiles = evidence_io.load(args.profiles)
        report = analyze(sorted(paths), profiles, edges, cohort)
        report['sources'] = {'requests': [str(path) for path in sorted(paths)],
                             'profiles': args.profiles, 'clients': [str(path) for path in sorted(clients)]}
        evidence_io.save(args.out, report)
    except (ValueError, KeyError, OSError) as exc:
        parser.error(str(exc))


if __name__ == '__main__':
    main()
