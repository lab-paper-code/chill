#!/usr/bin/env python3
"""Report archived native batch service against published profile ServiceSamples.

Ported from the profile-serving-equivalence stage-decomposition prototypes.
This diagnostic never changes profiles or evaluation judgments.

Optional --profile-raw files are archived profiling-raw-measurement JSON objects.
Their podUID and native sample batchSize join catalog serviceSamples by trialID
and batch size. The complete trial's service/idle multiset must match the catalog.
Native HTTP receive anchors and batch startedAt/finishedAt condition that trial
on other-request ingress; sentAt is retained as the actual-send evidence.
Catalog-only samples remain explicitly not conditioned on ingress.
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
        return -1
    number(value, 'precedingIdleNanos')
    return bisect_right(edges, value) - 1


def milliseconds_text(nanos):
    whole, fraction = divmod(nanos, 1_000_000)
    return str(whole) + (('.%06d' % fraction).rstrip('0') if fraction else '')


def bin_label(index, edges):
    if index < 0:
        return 'unknown'
    high = milliseconds_text(edges[index + 1]) if index + 1 < len(edges) else 'inf'
    return '[%s,%s)' % (milliseconds_text(edges[index]), high)


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


def comparison(key, serving, profile, edges=None, conditioned=False, unconditioned=(),
               idle_supported=None):
    pid, batch, gap, other = key
    observed, reference = stats(serving), stats(profile)
    result = {'profileID': pid, 'batchSize': batch, 'otherArrived': other,
              'serving': observed, 'profile': reference,
              'profileIngressConditioned': conditioned,
              'profileIngressSupported': conditioned and other is not None and bool(profile),
              'profileUnconditioned': stats(unconditioned),
              'sameConditionsComparison': conditioned and other is not None and bool(profile) and gap != -1 and edges is not None,
              'p50Ratio': observed['p50'] / reference['p50'] if serving and profile else None,
              'p95Ratio': observed['p95'] / reference['p95'] if serving and profile else None}
    if edges is not None:
        result.update(idleBin=bin_label(gap, edges), idleBinIndex=gap if gap >= 0 else None,
                      profileIdleBinSupported=bool(profile) if idle_supported is None else idle_supported)
    return result


def profile_ingress(raws):
    """Recover ingress on each trial's native clock, never from scheduled arrivalAt."""
    trials = {}
    for raw in raws:
        trial = raw['podUID']
        if not trial or trial in trials:
            raise ValueError('missing or duplicate profile raw trial ID')
        if not raw.get('schemaVersion', '').startswith('chill.dacs.io/profiling-raw-measurement.'):
            raise ValueError('profile raw measurement required')
        runtime = raw['runtime']
        requests = {}
        samples = []
        for phase in runtime['phases']:
            samples.extend(phase['nativeSamples'])
            for request in phase['requests']:
                rid = request['requestID']
                if not rid or rid in requests:
                    raise ValueError('missing or duplicate profile raw request ID')
                receive = None
                timing = request.get('response', {}).get('progress', {}).get('nativeHTTP') or {}
                if request.get('sentAt') and timing.get('handlerEnteredAt') and timing.get('receivedNanos') is not None:
                    evidence_io.timestamp_ns(request['sentAt'])
                    offset = timing['receivedNanos']
                    if type(offset) is not int or offset < 0:
                        raise ValueError('invalid profile native receivedNanos')
                    receive = evidence_io.timestamp_ns(timing['handlerEnteredAt']) + offset
                requests[rid] = receive
        receives = sorted(value for value in requests.values() if value is not None)
        batches = defaultdict(list)
        seen = set()
        for sample in samples:
            if sample['trialID'] != trial or sample['batchID'] in seen:
                raise ValueError('profile raw native trial mismatch or duplicate batch ID')
            seen.add(sample['batchID'])
            size = sample['batchSize']
            members = sample['requestIDs']
            if len(members) != size or len(set(members)) != size or any(rid not in requests for rid in members):
                raise ValueError('profile raw batch membership is incomplete')
            other = None
            if sample.get('startedAt') and sample.get('finishedAt'):
                start = evidence_io.timestamp_ns(sample['startedAt'])
                end = evidence_io.timestamp_ns(sample['finishedAt'])
                if end <= start:
                    raise ValueError('profile native finish must follow running')
                arrivals = bisect_left(receives, end) - bisect_left(receives, start)
                arrivals -= sum(start <= requests[rid] < end for rid in members if requests[rid] is not None)
                if arrivals:
                    other = True
                elif all(value is not None for value in requests.values()):
                    other = False
            batches[size].append((sample, other))
        trials[trial] = batches
    return trials


def service_pair(sample):
    return sample['serviceDurationNanos'], sample.get('precedingIdleNanos')


def analyze(paths, profiles, edges, cohort=None, profile_raws=()):
    rows = fold_requests(paths)
    raw_trials = profile_ingress(profile_raws)
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
    profile_unconditioned = defaultdict(list)
    profile_unconditioned_all = defaultdict(list)
    profile_idle_support = set()
    conditioned_batches = set()
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
            trials = defaultdict(list)
            for sample in batch['serviceSamples']:
                trials[sample.get('trialID')].append(sample)
            entries = []
            for trial, samples in trials.items():
                native = raw_trials.get(trial, {}).get(size)
                if native is None:
                    entries.extend((sample, None) for sample in samples)
                    continue
                if Counter(map(service_pair, samples)) != Counter(service_pair(sample) for sample, _ in native):
                    raise ValueError('profile raw samples do not match catalog trial: ' + str(trial))
                entries.extend(native)
            if any(other is not None for _, other in entries):
                conditioned_batches.add((pid, size))
            for sample, other in entries:
                duration = number(sample['serviceDurationNanos'], 'profile serviceDurationNanos', True) / 1e6
                gap = idle_bin(sample.get('precedingIdleNanos'), edges)
                counts['profileSamples'] += 1
                counts['profileSamplesWithoutIngress' if other is None else 'profileSamplesWithIngress'] += 1
                if gap >= 0:
                    profile_idle_support.add((pid, size, gap))
                if other is None:
                    profile_unconditioned_all[(pid, size)].append(duration)
                    if gap >= 0:
                        profile_unconditioned[(pid, size, gap)].append(duration)
                else:
                    profile_all[(pid, size, other)].append(duration)
                    if gap >= 0:
                        profile_cells[(pid, size, gap, other)].append(duration)

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
            candidates = complete if key == 'serviceDurationNanos' else members.values()
            values = [member[key] for member in candidates if member.get(key) is not None]
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

    def compare(key, serving, marginal=False):
        pid, size, gap, other = key
        conditioned = (pid, size) in conditioned_batches
        unknown = (profile_unconditioned_all[(pid, size)] if marginal
                   else profile_unconditioned[(pid, size, gap)])
        reference = (profile_all[(pid, size, other)] if marginal
                     else profile_cells[key]) if conditioned and other is not None else []
        if not conditioned:
            reference = unknown
        return comparison(key, serving, reference, None if marginal else edges, conditioned,
                          unknown, (pid, size, gap) in profile_idle_support)

    ingress_conditioning = 'not conditioned on ingress'
    if counts['profileSamplesWithIngress']:
        ingress_conditioning = ('partially conditioned on ingress; samples without timestamps remain not conditioned'
                                if counts['profileSamplesWithoutIngress'] else 'conditioned on ingress')

    return {
        'units': 'ms', 'quantiles': {'p50': 'median', 'p95': 'nearest-rank'},
        'idleBinEdgesNanos': edges,
        'idleBinEdgesMS': [edge / 1e6 for edge in edges], 'idleBins': '[lower,upper); last upper=inf; missing=unknown',
        'servingConditioning': ['profileID', 'batchSize', 'idleBin', 'otherArrived'],
        'profileConditioning': ['profileID', 'batchSize', 'idleBin'] + (['otherArrived'] if conditioned_batches else []),
        'profileIngressConditioning': ingress_conditioning,
        'ratioMeaning': 'serving / profile; profileIngressConditioned marks ingress-matched references; legacy references are not conditioned on ingress',
        'profileSupportMeaning': 'profileIdleBinSupported tests idle-bin support; profileIngressSupported tests the matched ingress cell',
        'sameConditionsMeaning': 'sameConditionsComparison matches profileID, batchSize, measured idle bin and observed ingress only; runtime image equivalence is not established',
        'receiveDefinition': 'nativeFinishedAt - nativeElapsedMS; same instance; running interval [nativeRunningAt,nativeFinishedAt); exclude own batch requests',
        'profileReceiveDefinition': 'nativeHTTP.handlerEnteredAt + receivedNanos, with actual sentAt evidence; same physical trial; interval [startedAt,finishedAt); exclude own batch requests',
        'limitations': [
            'Only requests with observed native finish and elapsed contribute derived receives; absence is not proof of no ingress.',
            'Longer executions are more likely to overlap arrivals (length bias); ingress association does not establish causality.',
            'Profile ID joins do not verify actual serving runtime image or other instance conditions.',
            'Unknown idle is not zero and cannot establish profile idle-bin support.',
            'Profile samples without native receive/run timestamps remain not conditioned; profileUnconditioned reports them separately.',
            'Native UTC anchors correlate profiling receives and runs; sentAt is a client-clock send anchor, not native receipt.'],
        'counts': dict(counts),
        'cells': [compare(key, serving_cells[key]) for key in ordered(serving_cells)],
        # Reproduce the plan table separately; these rows deliberately marginalize idle.
        'marginalByIngress': [compare(key, serving_all[key], marginal=True)
                              for key in ordered(serving_all)],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--raw', required=True, action='append',
                        help='fetched obs directory or requests.jsonl[.gz]; repeat for segments')
    parser.add_argument('--clients', action='append', default=[],
                        help='client JSONL or measured directory; optional output request cohort')
    parser.add_argument('--profiles', required=True, help='stored profile catalog JSON with profiles[]')
    parser.add_argument('--profile-raw', action='append', default=[],
                        help='archived profiling raw JSON; repeat for physical trials; joins catalog trialID')
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
        profile_raws = [evidence_io.load(path) for path in args.profile_raw]
        report = analyze(sorted(paths), profiles, edges, cohort, profile_raws)
        report['sources'] = {'requests': [str(path) for path in sorted(paths)],
                             'profiles': args.profiles, 'profileRaw': args.profile_raw,
                             'clients': [str(path) for path in sorted(clients)]}
        evidence_io.save(args.out, report)
    except (ValueError, KeyError, OSError) as exc:
        parser.error(str(exc))


if __name__ == '__main__':
    main()
