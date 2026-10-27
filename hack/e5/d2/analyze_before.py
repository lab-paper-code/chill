#!/usr/bin/env python3
"""Offline D2 phase analysis; never contacts a service or changes inputs.

Usage: python3 analyze_before.py --phase-dir PHASE --out SUMMARY.json
Optional --idle-summary IDLE.json and --rate RATE produce the declared CPU slope.
The adjacent SUMMARY.txt is a descriptive report, not a new acceptance gate.
Missing evidence is null/unknown. Quantiles use empirical nearest rank.
Optional additional cgroup JSONL records in cgmon.log or *cgroup*.jsonl:
{at, role, cgroup|containerID, cpuUsageNanos, nrThrottled, throttledNanos,
 memoryBytes, restartCount}. Roles: server/client/native/edgemesh (or cs/em).
phase.offeredRequests must be the actual generated schedule count, never rate*time.
"""
import argparse
import bisect
from collections import Counter, defaultdict
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import re



def number(value):
    if isinstance(value, bool):
        return None
    try:
        value = float(value)
        return value if math.isfinite(value) else None
    except (TypeError, ValueError):
        return None


def epoch(value):
    try:
        # Go emits RFC3339Nano; Python3.8 fromisoformat only accepts 0/3/6
        # fraction digits. Parse whole seconds and retain the actual fraction.
        match = re.fullmatch(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})', value)
        if match is None:
            return None
        fraction = match.group(2) or ''
        offset = '+00:00' if match.group(3) == 'Z' else match.group(3)
        return datetime.fromisoformat(match.group(1) + offset).timestamp() + (int(fraction) / 10 ** len(fraction) if fraction else 0)
    except (AttributeError, TypeError, ValueError, OverflowError):
        return None


def stats(values):
    values = sorted(v for v in values if v is not None and math.isfinite(v))
    return {'samples': len(values), 'mean': sum(values) / len(values) if values else None,
            **{name: values[max(0, math.ceil(q * len(values)) - 1)] if values else None
               for name, q in [('p50', .50), ('p95', .95), ('p99', .99), ('max', 1)]}}


def scheduled_monotonic_ns(row, start_ns):
    scheduled = row.get('scheduledMonotonicNS')
    if type(scheduled) is int:
        return scheduled
    offset = row.get('scheduledOffsetNs')
    return start_ns + offset if type(start_ns) is int and type(offset) is int else None


def scheduled_latency_ns(row, start_ns):
    """Scheduled end-to-end reference only; never the receipt-origin SLO."""
    if row.get('issued') is False or row.get('error') == 'UnsentAtWindowEnd':
        return None
    scheduled = scheduled_monotonic_ns(row, start_ns)
    finished = row.get('finishedMonotonicNS')
    return finished - scheduled if scheduled is not None and type(finished) is int and finished >= scheduled else None


def generator_quality_config(config):
    policy = config.get('generatorQuality')
    if 'generatorQuality' not in config:
        return None
    if not isinstance(policy, dict) or set(policy) != {'latenessToleranceNS', 'maxExceedingFraction'}:
        raise ValueError('generatorQuality requires latenessToleranceNS and maxExceedingFraction')
    if type(policy['latenessToleranceNS']) is not int or policy['latenessToleranceNS'] < 0:
        raise ValueError('generatorQuality.latenessToleranceNS requires a nonnegative integer')
    fraction = policy['maxExceedingFraction']
    if type(fraction) not in (int, float) or not math.isfinite(fraction) or not 0 <= fraction <= 1:
        raise ValueError('generatorQuality.maxExceedingFraction requires a finite fraction in [0,1]')
    return dict(policy)


def request_timing(row, final, received_ns, start_ns):
    """Join actual HTTP receipt/final facts; never substitute client RTT for SLO."""
    elapsed = final.get('elapsedNanos') if final else None
    suitable = row.get('issued') is not False and row.get('error') != 'UnsentAtWindowEnd' and final and final.get('outcome') == 'completed' and final.get('executionComplete') is True
    slo = elapsed if suitable and type(elapsed) is int and elapsed >= 0 else None
    sent, finished = row.get('sentMonotonicNS'), row.get('finishedMonotonicNS')
    actual = finished - sent if type(sent) is int and type(finished) is int and finished >= sent and row.get('issued') is not False else None
    wall_sent = row.get('sentAtUnixNS')
    entrance = received_ns - wall_sent if type(received_ns) is int and type(wall_sent) is int and row.get('issued') is not False else None
    return {'sloLatencyNS': slo, 'scheduledLatencyNS': scheduled_latency_ns(row, start_ns),
            'actualLatencyNS': actual, 'entranceDelayNS': entrance,
            'entranceDelayMeaning': 'Gateway receivedAt minus client sentAtUnixNS; cross-host clock offset/uncertainty retained, including negative values; not one-way network latency'}


def issuance_fidelity(client, offsets, start_ns, duration_ns, stats, quality=None):
    """Describe send-call timing, not stdout flush timing or wire arrival."""
    threshold = 50_000_000
    issued = [row for row in client if row.get('issued') is not False and row.get('error') != 'UnsentAtWindowEnd']
    missing = sum(type(row.get('sentMonotonicNS')) is not int for row in issued)
    result = {'thresholdNS': threshold, 'offered': len(offsets), 'issued': len(issued), 'missingSendTimestamps': missing,
              'supplied': False, 'issuanceFraction': None,
              'generatorQuality': {'status': 'UNKNOWN', 'policy': quality, 'reason': 'Lateness policy is not configured' if quality is None else 'Issuance timing/population incomplete'},
              'meaning': 'Offered is the full frozen trace, including unsent arrivals; supplied requires every planned arrival issued, not merely an unchanged actual-count denominator. Late sends, including sends after the window, remain visible. Per-producer monotonic infer-call timestamps, sorted across workers; gaps run through the later of window end and last send. Planned Poisson/zero-load gaps are not automatically stalls. No wall-clock or gateway causality claim.'}
    if len(client) != len(offsets):
        return dict(result, status='UNKNOWN', reason='Frozen planned population has missing or extra client rows')
    if any(type(row.get('requestIndex')) is not int or not 0 <= row['requestIndex'] < len(offsets) or row.get('scheduledOffsetNs') != offsets[row['requestIndex']] for row in client):
        return dict(result, status='UNKNOWN', reason='Request index or scheduled offset differs from frozen planned trace')
    if len({row['requestIndex'] for row in client}) != len(offsets):
        return dict(result, status='UNKNOWN', reason='Duplicate request indexes leave planned arrivals unaccounted')
    if not offsets:
        return dict(result, status='NOT_OFFERED', reason='Frozen trace contains no arrivals')
    if type(start_ns) is not int or missing:
        return dict(result, status='UNKNOWN', reason='Actual monotonic start/send timestamp missing')
    sent = sorted(row['sentMonotonicNS'] - start_ns for row in issued)
    if any(at < 0 for at in sent):
        return dict(result, status='UNKNOWN', reason='Send timestamp precedes actual producer start')
    if any(scheduled_monotonic_ns(row, start_ns) != start_ns + row['scheduledOffsetNs'] for row in client):
        return dict(result, status='UNKNOWN', reason='Scheduled monotonic timestamp conflicts with frozen arrival')
    horizon = max(duration_ns, sent[-1] if sent else 0)
    edges = [0] + sent + [horizon]
    planned = [0] + offsets + [duration_ns]
    gaps = [right-left for left, right in zip(edges, edges[1:])]
    long_gaps = [{'fromOffsetNS': left, 'toOffsetNS': right, 'gapNS': right-left,
                  'plannedArrivalsInside': bisect.bisect_left(offsets, right)-bisect.bisect_right(offsets, left)}
                 for left, right in zip(edges, edges[1:]) if right-left > threshold]
    late = [row['sentMonotonicNS']-scheduled_monotonic_ns(row, start_ns) for row in issued]
    result.update(status='OBSERVED', supplied=len(issued) == len(offsets), issuanceFraction=len(issued)/len(offsets),
                  unissued=len(offsets)-len(issued), sendsAfterWindow=sum(at >= duration_ns for at in sent),
                  gapHorizonNS=horizon, actualGapNS=stats(gaps),
                  plannedGapNS=stats([right-left for left, right in zip(planned, planned[1:])]),
                  gapsOver50MS=len(long_gaps), gapsOver50MSFraction=len(long_gaps)/len(gaps),
                  gapIntervalsOver50MS=long_gaps, sendLatenessNS=stats(late),
                  sendsLateOver50MS=sum(value > threshold for value in late))
    if quality is not None:
        exceeding = sum(value > quality['latenessToleranceNS'] for value in late)
        fraction = exceeding / len(issued) if issued else None
        valid = result['supplied'] and fraction is not None and fraction <= quality['maxExceedingFraction']
        result['generatorQuality'] = {'status': 'VALID' if valid else 'INVALID_GENERATOR', 'policy': quality,
                                      'exceeding': exceeding, 'denominator': len(issued), 'exceedingFraction': fraction,
                                      'reason': None if valid else 'Frozen trace not fully issued or lateness exceeding fraction exceeds configured maximum'}
    return result


def counts(rows, key):
    return dict(Counter(str(r.get(key, '<missing>')) for r in rows))


class Inputs:
    def __init__(self, root):
        self.root, self.sources, self.errors = root, {}, []

    def json(self, path):
        path = Path(path)
        try:
            data = json.loads(path.read_text())
            self.sources[str(path)] = {'present': True, 'bytes': path.stat().st_size}
            return data
        except (OSError, ValueError) as exc:
            self.sources[str(path)] = {'present': path.exists(), 'error': str(exc)}
            self.errors.append({'path': str(path), 'error': str(exc)})
            return None

    def lines(self, path):
        path = Path(path)
        try:
            with path.open() as stream:
                for index, line in enumerate(stream, 1):
                    if line.strip():
                        yield index, line.rstrip('\n')
            self.sources[str(path)] = {'present': True, 'bytes': path.stat().st_size}
        except OSError as exc:
            self.sources[str(path)] = {'present': path.exists(), 'error': str(exc)}
            self.errors.append({'path': str(path), 'error': str(exc)})

    def jsonlines(self, path):
        for index, line in self.lines(path):
            try:
                row = json.loads(line)
                if not isinstance(row, dict):
                    raise ValueError('expected object')
                yield row
            except ValueError as exc:
                self.errors.append({'path': str(path), 'line': index, 'error': str(exc)})

    def pages(self, folder, key):
        rows, metadata = [], []
        paths = sorted((self.root / folder).glob('*.json'))
        if not paths:
            self.sources[str(self.root / folder)] = {'present': False}
        for path in paths:
            page = self.json(path)
            if not isinstance(page, dict):
                continue
            values = page.get(key)
            valid = isinstance(values, list) and all(isinstance(v, dict) for v in values)
            metadata.append({'path': str(path), 'rowCount': len(values) if valid else None,
                             **{k: v for k, v in page.items() if k != key}})
            if valid:
                rows.extend(values)
            else:
                self.errors.append({'path': str(path), 'error': 'missing/invalid ' + key})
        cursors = [number(p.get('nextCursor')) for p in metadata if p.get('nextCursor')]
        complete = bool(metadata) and any(not p.get('nextCursor') for p in metadata)
        complete = complete and not any(p.get('gapUntil') or p.get('rowCount') is None for p in metadata)
        # Cursor is a numeric offset in the source API, not a timestamp.
        complete = complete and all(c is not None and c <= len(rows) for c in cursors)
        return rows, {'pages': metadata, 'paginationApparentlyComplete': complete,
                      'note': 'Terminal page and numeric offsets checked; collector provenance remains required.'}


def request_summary(rows):
    groups, unkeyed = defaultdict(list), []
    for row in rows:
        if row.get('requestID'):
            groups[(row.get('workloadUID'), row['requestID'])].append(row)
        else:
            unkeyed.append(row)
    unique, conflicts = [], []
    for (uid, rid), events in groups.items():
        finals = {json.dumps(r, sort_keys=True): r for r in events if r.get('kind') == 'final'}
        assigned = [r for r in events if r.get('kind') == 'assigned']
        native = [r for r in events if r.get('kind') == 'native']
        # Never let a late native outcome/elapsed overwrite the settled HTTP final.
        final = next(iter(finals.values())) if len(finals) == 1 else None
        if len(finals) > 1:
            conflicts.append({'workloadUID': uid, 'requestID': rid, 'finals': list(finals.values())})
        unique.append({'workloadUID': uid, 'requestID': rid, 'final': final,
                       'assigned': assigned[-1] if assigned else None,
                       'nativeSupplements': list({json.dumps(r, sort_keys=True): r for r in native}.values()),
                       'otherRows': [r for r in events if r.get('kind') not in ('final', 'assigned', 'native')],
                       'rawRows': len(events)})
    finals = [r['final'] for r in unique if r['final'] is not None]
    elapsed, post, invalid = [], [], []
    for row in finals:
        e, x = number(row.get('elapsedNanos')), number(row.get('exchangeNanos'))
        if e is not None and e >= 0:
            elapsed.append(e / 1e6)
        if e is not None and x is not None and e >= x >= 0:
            post.append((e - x) / 1e6)
        elif e is not None and x is not None:
            invalid.append(row['requestID'])
    completed = [r for r in finals if r.get('outcome') == 'completed']
    return {'rawRows': len(rows), 'kinds': counts(rows, 'kind'), 'uniqueRequests': len(unique),
            'uniqueFinalRows': len(finals), 'outcomes': counts(finals, 'outcome'),
            'errors': counts(finals, 'error'), 'executionComplete': counts(finals, 'executionComplete'),
            'completed': len(completed), 'withoutUnambiguousHTTPFinal': len(unique) - len(finals),
            'elapsedMS': stats(elapsed), 'postexchangeMS': stats(post),
            'postexchangeInvalidRequestIDs': invalid,
            'timingMeaning': 'elapsedNanos minus exchangeNanos is same-gateway post-backend-return to Response entry, not native wall-clock subtraction or full socket delivery.',
            'finalConflicts': conflicts, 'unkeyedRows': unkeyed, 'requests': unique}


def scheduler_summary(rows):
    unique = {json.dumps(r, sort_keys=True): r for r in rows}
    decisions = [r['decision'] for r in unique.values() if isinstance(r.get('decision'), dict)]
    interpreted = []
    for d in decisions:
        capacity = d.get('capacity') or {}
        search = capacity.get('search')
        examined = number((d.get('search') or {}).get('examined'))
        if isinstance(search, dict) and (number(search.get('evaluated')) or 0) > 0:
            meaning = 'actual-plan-search-evaluations'
        elif isinstance(search, dict) and (number(search.get('durationNanos')) or 0) > 0:
            meaning = 'search-timed-no-positive-evaluation-count'
        elif examined is not None and examined > 0:
            meaning = 'node-plan-construction-observed-search-not-established'
        elif d.get('workloads') and all(w.get('state') == 'Disabled' for w in d['workloads']):
            meaning = 'no-autonomy-early-return-no-search-evidence'
        else:
            meaning = 'early-return-or-search-unobserved'
        interpreted.append({'interpretation': meaning, 'decision': d})
    return {'rawEvents': len(rows), 'uniqueEvents': len(unique), 'decisions': len(decisions),
            'evaluationElapsedMS': stats([number(d.get('evaluationElapsedNanos')) / 1e6
                for d in decisions if number(d.get('evaluationElapsedNanos')) is not None]),
            'statuses': counts(decisions, 'status'), 'interpretations': counts(interpreted, 'interpretation'),
            'interpretationNote': 'search.examined counts node plans, not solver/search calls. KEEP alone proves neither search nor its absence. capacity.search is source-specific and retained verbatim.',
            'records': interpreted, 'events': list(unique.values())}


def telemetry(inputs, start, end):
    by, restart = defaultdict(list), defaultdict(list)
    aliases = {'cs': 'server', 'em': 'edgemesh'}
    paths = [inputs.root / 'cgmon.log'] + sorted(inputs.root.glob('*cgroup*.jsonl'))
    unparsed, metadata = [], []
    for path in dict.fromkeys(paths):
        for index, line in inputs.lines(path):
            if line.lstrip().startswith('{'):
                try:
                    row = json.loads(line)
                    if row.get('kind') not in (None, 'cgroup'):
                        metadata.append({'path': str(path), 'line': index, 'record': row})
                        continue
                    at = epoch(row.get('at'))
                    if at is None:
                        raise ValueError('missing/invalid at')
                    role = aliases.get(row.get('role'), row.get('role', 'unknown'))
                    identity = row.get('containerID') or row.get('cgroup')
                    identity = str(row.get('node', '')) + '/' + str(identity) if identity else '<identity-unknown>'
                    by[(role, identity, 'cumulative')].append({**row, '_t': at, '_format': 'cumulative'})
                except (ValueError, AttributeError, TypeError) as exc:
                    unparsed.append({'path': str(path), 'line': index, 'text': line, 'error': str(exc)})
                continue
            at = epoch(line.split(' ', 1)[0])
            if at is None:
                unparsed.append({'path': str(path), 'line': index, 'text': line})
                continue
            identities = dict(re.findall(r'\b(em|cs)=([^\s]+)', line))
            found = False
            for match in re.finditer(r'\b(em|cs) cpu=([^ ]+) thr=([^ ]+) thrms=([^ ]+) memMi=([^ ]+)', line):
                found = True
                k, cpu, thr, thrms, mem = match.groups()
                ident = identities.get(k, '<identity-unknown>')
                restart[aliases[k]].append({'at': line.split(' ', 1)[0], 'podRestart': ident})
                by[(aliases[k], ident, 'cgmon-delta')].append({'_t': at, '_format': 'cgmon-delta',
                    'cpu': number(cpu), 'nrThrottledDelta': number(thr),
                    'throttledNanosDelta': number(thrms) * 1e6 if number(thrms) is not None else None,
                    'memoryBytes': number(mem) * 1048576 if number(mem) is not None else None})
            if not found:
                unparsed.append({'path': str(path), 'line': index, 'text': line})
    all_series = []
    for (role, identity, source_format), samples in by.items():
        samples = sorted({json.dumps(r, sort_keys=True): r for r in samples}.values(), key=lambda r: r['_t'])
        if start is not None and samples[-1]['_t'] < start or end is not None and samples[0]['_t'] > end:
            continue
        # Include the nearest real samples outside each boundary, not interpolated counters.
        before = [i for i, r in enumerate(samples) if start is not None and r['_t'] <= start]
        after = [i for i, r in enumerate(samples) if end is not None and r['_t'] >= end]
        samples = samples[before[-1] if before else 0:(after[0] + 1) if after else len(samples)]
        intervals, resets, missing, cpu_seconds, seconds = [], [], [], [], []
        throttle, throttle_ns, in_window, boundary = [], [], [], []
        for previous, current in zip(samples, samples[1:]):
            dt = current['_t'] - previous['_t']
            if dt <= 0:
                continue
            intervals.append(dt)
            if source_format == 'cgmon-delta':
                u = current.get('cpu')
                n, t = current.get('nrThrottledDelta'), current.get('throttledNanosDelta')
            else:
                delta = {}
                for key in ('cpuUsageNanos', 'nrThrottled', 'throttledNanos'):
                    a, b = number(previous.get(key)), number(current.get(key))
                    delta[key] = b - a if a is not None and b is not None else None
                if any(v is not None and v < 0 for v in delta.values()):
                    resets.append({'at': current.get('at'), 'counterDeltas': delta})
                    continue
                u = delta['cpuUsageNanos'] / 1e9 if delta['cpuUsageNanos'] is not None else None
                n, t = delta['nrThrottled'], delta['throttledNanos']
            if any(v is None for v in (u, n, t)):
                missing.append({'firstEpoch': previous['_t'], 'lastEpoch': current['_t'],
                                'cpuMissing': u is None, 'nrThrottledMissing': n is None, 'throttleTimeMissing': t is None})
            if u is not None:
                cpu_seconds.append(u)
                seconds.append(dt)
                current['_cpuInterval'] = [previous['_t'], current['_t']]
            if n is not None:
                throttle.append(n)
                inside = start is not None and end is not None and previous['_t'] >= start and current['_t'] <= end
                (in_window if inside else boundary).append(n)
            if t is not None:
                throttle_ns.append(t)
        all_series.append({'role': role, 'identity': identity, 'sourceFormat': source_format, 'samples': len(samples),
            'firstEpoch': samples[0]['_t'], 'lastEpoch': samples[-1]['_t'],
            'spanSeconds': samples[-1]['_t'] - samples[0]['_t'],
            'boundaryOverhangSeconds': {'before': max(0, start - samples[0]['_t']) if start is not None else None,
                                        'after': max(0, samples[-1]['_t'] - end) if end is not None else None},
            'bracketsWindow': start is not None and end is not None and samples[0]['_t'] <= start and samples[-1]['_t'] >= end,
            'cadenceSeconds': stats(intervals), 'intervalsOverNominalOneSecond': [v for v in intervals if v > 1],
            'counterResets': resets, 'missingCounterIntervals': missing, 'cpuIntervalSamples': len(cpu_seconds),
            'cpuObservedSeconds': sum(seconds) if seconds else None,
            'cpuMeanCores': sum(cpu_seconds) / sum(seconds) if seconds and sum(seconds) else None,
            'nrThrottledDelta': sum(throttle) if throttle else None,
            'nrThrottledWhollyWithinWindowDelta': sum(in_window) if in_window else None,
            'nrThrottledBoundaryIntervalDelta': sum(boundary) if boundary else None,
            'throttleIntervalSamples': len(throttle),
            'throttledNanosDelta': sum(throttle_ns) if throttle_ns else None,
            'memoryBytes': stats([number(r.get('memoryBytes')) for r in samples]), 'rawSamples': samples})
    exact_roles = {s['role'] for s in all_series if s['sourceFormat'] == 'cumulative'}
    series = [s for s in all_series if s['sourceFormat'] == 'cumulative' or s['role'] not in exact_roles]
    coarse = [s for s in all_series if s['sourceFormat'] == 'cgmon-delta' and s['role'] in exact_roles]
    roles = {}
    for role in ('server', 'client', 'native', 'edgemesh', 'unknown'):
        selected = [s for s in series if s['role'] == role]
        covered = []
        for a, b in sorted(r['_cpuInterval'] for s in selected for r in s['rawSamples'] if '_cpuInterval' in r):
            if covered and a <= covered[-1][1]:
                covered[-1][1] = max(covered[-1][1], b)
            else:
                covered.append([a, b])
        seconds = sum(b - a for a, b in covered)
        roles[role] = {'identities': len(selected), 'cpuObservedWallSeconds': seconds or None,
            'sourcePreference': 'cumulative' if role in exact_roles else 'cgmon-delta',
            'cpuMeanCores': sum(s['cpuMeanCores'] * s['cpuObservedSeconds'] for s in selected
                                  if s['cpuMeanCores'] is not None) / seconds if seconds else None,
            'nrThrottledDelta': sum(s['nrThrottledDelta'] for s in selected)
                                  if selected and all(s['nrThrottledDelta'] is not None for s in selected) else None,
            'nrThrottledWhollyWithinWindowDelta': sum(s['nrThrottledWhollyWithinWindowDelta'] for s in selected)
                if selected and all(s['nrThrottledWhollyWithinWindowDelta'] is not None for s in selected) else None,
            'nrThrottledBoundaryIntervalDelta': sum(s['nrThrottledBoundaryIntervalDelta'] for s in selected)
                if selected and all(s['nrThrottledBoundaryIntervalDelta'] is not None for s in selected) else None,
            'coverage': 'bracketed-counter-span' if selected and all(s['bracketsWindow'] for s in selected) else 'unknown'}
    return {'roles': roles, 'series': series, 'coarseDescriptiveSeries': coarse,
            'collectorMetadata': metadata, 'collectorErrors': [m for m in metadata if m['record'].get('kind') == 'collector-error'],
            'restartObservations': dict(restart), 'unparsed': unparsed,
            'limitations': ['Exact cumulative series take precedence per role; coarse cgmon is retained separately, never added to the same role twice.',
                'Counter deltas cover actual bracket samples including declared boundary overhang; CPU divides by observed wall span. No interpolation or zero-fill.',
                'Cadence and intervals exceeding nominal 1s are descriptive, not a new jitter/gap acceptance threshold.',
                'cgmon cpu rounded to 0.01 CPU-second; thrms truncated to milliseconds; memMi is usage, not working set. Cgroup resets are not observable in this coarse format.',
                'Distinct node/container/restart generations are retained, never subtracted across identities.']}


def main():
    workload_name = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())['d2']['workloadName']
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--phase-dir', required=True, type=Path)
    parser.add_argument('--out', required=True, type=Path)
    parser.add_argument('--idle-summary', type=Path)
    parser.add_argument('--rate', type=float)
    args = parser.parse_args()
    inputs = Inputs(args.phase_dir.resolve())
    phase = inputs.json(inputs.root / 'phase.json')
    if not isinstance(phase, dict):
        parser.error('phase.json must be a JSON object')
    start, end = epoch(phase.get('startUTC')), epoch(phase.get('endUTC'))
    warmup = number(phase.get('warmupSeconds'))
    measured_start = start + warmup if start is not None and warmup is not None else None
    requests, request_pages = inputs.pages('request-pages', 'requests')
    events, scheduler_pages = inputs.pages('scheduler-pages', 'events')
    client = list(inputs.jsonlines(inputs.root / 'client.jsonl'))
    req = request_summary(requests)
    measured_rows = [r for r in requests if measured_start is not None and epoch(r.get('receivedAt')) is not None
                     and measured_start <= epoch(r['receivedAt']) and (end is None or epoch(r['receivedAt']) < end)]
    measured = request_summary(measured_rows)
    finals = {r['requestID']: r['final'] for r in req['requests'] if r['final'] is not None}
    client_missing_final = [i for i, r in enumerate(client) if not r.get('requestID') or r['requestID'] not in finals]
    bad_client = [i for i, r in enumerate(client) if r.get('error') or r.get('status') != 200]
    offered = number(phase.get('offeredRequests'))
    rate = args.rate if args.rate is not None else number(phase.get('rate'))
    if rate is not None and (not math.isfinite(rate) or rate < 0):
        parser.error('rate must be finite and nonnegative')
    cg = telemetry(inputs, start, end)
    cpu_window = telemetry(inputs, measured_start, end) if measured_start is not None else None
    boundaries = {name: inputs.json(inputs.root / name) for name in
        ('pre-state.json', 'post-state.json', 'pre-pools.json', 'post-pools.json')}
    workloads = [[w for w in (boundaries.get(name) or {}).get('snapshot', {}).get('workloads', [])
                  if w.get('name') == workload_name]
                 for name in ('pre-state.json', 'post-state.json')]
    slo = workloads[0][0].get('slo', {}) if len(workloads[0]) == 1 else {}
    target_ns = number(slo.get('targetLatencyNanos'))
    latency_known = (all(len(ws) == 1 for ws in workloads)
        and workloads[0][0].get('uid') == workloads[1][0].get('uid')
        and slo == workloads[1][0].get('slo') and slo.get('percentile') == 95
        and target_ns is not None and target_ns > 0
        and measured['uniqueFinalRows'] > 0
        and measured['elapsedMS']['samples'] == measured['uniqueFinalRows']
        and all(r.get('workloadUID') == workloads[0][0].get('uid') for r in measured_rows))
    latency_failure = latency_known and measured['elapsedMS']['p95'] > target_ns / 1e6
    observed_failure = bool(bad_client or any(r.get('outcome') != 'completed' for r in finals.values()))
    client_ids = [r.get('requestID') for r in client]
    duplicate_client_ids = len([v for v in client_ids if v]) != len(set(v for v in client_ids if v))
    offered_complete = False if observed_failure else None
    if not observed_failure and offered is not None and offered == len(client) and not client_missing_final and not duplicate_client_ids:
        offered_complete = True
    throttle_roles = cg['roles']
    # Native quota throttling is a profile condition; conductor.verify retains
    # the existing exact physical policy/allocation guard before and after load.
    throttle_failure = any(s['role'] != 'native' and (s['nrThrottledWhollyWithinWindowDelta'] or 0) > 0 for s in cg['series'])
    unknown = []
    if rate != 0 and not latency_known:
        unknown.append('postwarm p95 latency or matching frozen Workload SLO binding absent/invalid')
    if offered is None:
        unknown.append('exact offered schedule count absent; finished client JSONL cannot reveal unsent or lost attempts')
    elif offered != len(client):
        unknown.append('offered schedule count differs from finished client row count; unsent/unfinished/lost population not resolved')
    if duplicate_client_ids:
        unknown.append('duplicate gateway IDs in client rows prevent unique offered-to-final accounting')
    if start is None or end is None or measured_start is None or end <= measured_start:
        unknown.append('invalid/missing phase or measurement interval')
    if cg['unparsed']:
        unknown.append('unparsed telemetry retained')
    if cg['collectorErrors']:
        unknown.append('collector errors retained')
    if any((s['nrThrottledBoundaryIntervalDelta'] or 0) > 0 for s in cg['series']):
        unknown.append('throttle in boundary-straddling counter interval cannot be assigned wholly to phase')
    if client_missing_final:
        unknown.append('client rows without gateway final; HTTP 200 is not completion evidence')
    if not request_pages['paginationApparentlyComplete']:
        unknown.append('request pagination/gap incomplete')
    if req['finalConflicts'] or req['unkeyedRows'] or inputs.errors:
        unknown.append('conflicting/unkeyed/unreadable source evidence')
    for role in ('server', 'client', 'native'):
        if throttle_roles[role]['nrThrottledDelta'] is None:
            unknown.append(role + ' throttle evidence absent')
    if any(s['counterResets'] or s['missingCounterIntervals'] or s['identity'] == '<identity-unknown>' for s in cg['series']):
        unknown.append('telemetry resets/missing counters/unknown identity retained')
    for role in ('server', 'client', 'native'):
        selected = [s for s in cg['series'] if s['role'] == role]
        if selected and not all(s['bracketsWindow'] for s in selected):
            unknown.append(role + ' cgroup generations do not individually bracket full phase')
    kind = str(phase.get('kind', phase.get('phase', ''))).lower()
    autonomous = 'autonom' in kind or 'p3a' in kind or phase.get('autonomous') is True
    static = not autonomous and ('static' in kind or 'staircase' in kind or kind in ('p0', 'p1') or phase.get('static') is True)
    eligibility = 'not-applicable' if not static or rate == 0 else 'ineligible' if observed_failure or throttle_failure or latency_failure or offered_complete is False else 'unknown' if unknown else 'eligible'
    summary = {'schema': 'd2-before-analysis.v1', 'phaseDirectory': str(inputs.root), 'phase': phase,
        'analysisUTC': datetime.now(timezone.utc).isoformat(), 'rate': rate,
        'windows': {'phaseStartEpoch': start, 'measurementStartEpoch': measured_start, 'phaseEndEpoch': end,
                    'requestPopulation': 'receivedAt in half-open measurement interval; phase totals also retained'},
        'requests': req, 'measurementRequests': measured,
        'requestPages': request_pages, 'schedulerPages': scheduler_pages,
        'scheduler': scheduler_summary(events),
        'measurementScheduler': scheduler_summary([r for r in events if measured_start is not None
            and epoch(r.get('at')) is not None and measured_start <= epoch(r['at'])
            and (end is None or epoch(r['at']) < end)]),
        'client': {'rows': len(client), 'offeredRequests': offered, 'statuses': counts(client, 'status'),
            'errors': counts(client, 'error'), 'delayed': counts(client, 'delayed'),
            'scheduledOffsetNs': stats([number(r.get('scheduledOffsetNs')) for r in client]),
            'missingScheduledOffsetRows': sum(number(r.get('scheduledOffsetNs')) is None for r in client),
            'latencyMS': stats([number(r.get('latencyMs')) for r in client]),
            'withoutGatewayFinalRowIndices': client_missing_final, 'failedRowIndices': bad_client,
            'records': client, 'allOfferedFinalCompleted': offered_complete},
        'cgroups': cg, 'measurementCgroups': cpu_window,
        'staticStaircase': {'status': eligibility, 'unknownReasons': unknown,
            'observedCompletionFailure': observed_failure, 'observedThrottle': throttle_failure,
            'observedLatencyExceedance': latency_failure,
            'latencySLO': {'source': 'pre/post-state frozen canonical Workload slo',
                'percentile': slo.get('percentile'), 'targetLatencyNanos': target_ns,
                'observedPostwarmP95MS': measured['elapsedMS']['p95'], 'evidenceKnown': latency_known},
            'interpretation': 'User Main082 component stop rule: allocated native quota throttling is descriptive; other component throttle, failure, postwarm p95 above matching frozen Workload SLO, and unknown evidence stop. Physical allocation remains conductor.verify guarded; no G bound inferred from one stage.'},
        'comparison': {'autonomous': autonomous, 'static': static,
            'inputMatching': 'dynamic trajectories may diverge despite matched logical initial8/seed/rate; not identical per-Tick inputs or physical Pods' if autonomous else 'physical/source/input matching requires paired boundary identity evidence, not established by this single-phase analyzer'},
        'cpuSlopeMillicorePerRequestPerSecond': None}
    # Boundary snapshots and logs remain provenance, never synthetic completion evidence.
    summary['boundaryArtifacts'] = boundaries
    log_path = inputs.root / 'server.log'
    log_counts = Counter()
    for _, line in inputs.lines(log_path):
        for label, pattern in [('gc', r'\bgc \d+ @'), ('error', r'\bERROR\b'), ('panic', r'\bpanic:'), ('OOM', r'OOMKilled|out of memory')]:
            if re.search(pattern, line):
                log_counts[label] += 1
    summary['serverLog'] = {'patternCounts': dict(log_counts), 'note': 'Pattern counts only; no inferred absence of GC CPU, restarts, errors or OOM outside captured logs.'}
    if args.idle_summary:
        idle = inputs.json(args.idle_summary)
        current = (cpu_window or cg)['roles']['server']['cpuMeanCores']
        baseline = None
        if isinstance(idle, dict):
            baseline = ((idle.get('measurementCgroups') or idle.get('cgroups') or {}).get('roles') or {}).get('server', {}).get('cpuMeanCores')
        if current is not None and number(baseline) is not None and rate is not None and rate > 0:
            summary['cpuSlopeMillicorePerRequestPerSecond'] = 1000 * (current - baseline) / rate
        summary['idleComparison'] = {'path': str(args.idle_summary), 'idleCores': baseline,
            'phaseCores': current, 'note': 'Descriptive subtraction; comparable static identity/resources must be established separately.'}
    summary['sources'], summary['sourceErrors'] = inputs.sources, inputs.errors
    summary['staticStaircase']['unknownReasons'] = list(dict.fromkeys(unknown + (['source errors retained'] if inputs.errors else [])))
    if summary['staticStaircase']['status'] == 'eligible' and inputs.errors:
        summary['staticStaircase']['status'] = 'unknown'
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(summary, indent=2, sort_keys=True, allow_nan=False) + '\n')
    text = [f"Phase: {phase.get('phase', '<unknown>')} | SHA: {phase.get('sourceSha', '<unknown>')} | rate: {rate}",
        f"Requests: raw={len(requests)} unique={req['uniqueRequests']} final={req['uniqueFinalRows']} completed={req['completed']} missing-final={req['withoutUnambiguousHTTPFinal']}",
        'Outcomes: ' + json.dumps(req['outcomes'], sort_keys=True),
        'Measurement elapsed ms: ' + json.dumps(measured['elapsedMS'], sort_keys=True),
        'Measurement postexchange ms: ' + json.dumps(measured['postexchangeMS'], sort_keys=True),
        'Tick evaluation ms: ' + json.dumps(summary['scheduler']['evaluationElapsedMS'], sort_keys=True),
        'Tick interpretations: ' + json.dumps(summary['scheduler']['interpretations'], sort_keys=True),
        f"Client rows={len(client)} offered={offered} missing-gateway-final={len(client_missing_final)} delayed={json.dumps(summary['client']['delayed'])}",
        'Cgroups: ' + json.dumps(cg['roles'], sort_keys=True),
        'Static staircase: ' + summary['staticStaircase']['status'],
        'Unknowns: ' + '; '.join(summary['staticStaircase']['unknownReasons']),
        'CPU slope millicore/(req/s): ' + str(summary['cpuSlopeMillicorePerRequestPerSecond']),
        'Comparison: ' + summary['comparison']['inputMatching'],
        f"Source errors: {len(inputs.errors)}. Detailed rows, missingness, telemetry intervals/restarts and page metadata are in JSON."]
    text_out = args.out.with_suffix('.txt') if args.out.suffix != '.txt' else args.out.with_name(args.out.stem + '-report.txt')
    text_out.write_text('\n'.join(text) + '\n')


if __name__ == '__main__':
    main()
