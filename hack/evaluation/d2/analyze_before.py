"""Offline request timing, empirical statistics and generator issuance quality.

Only HTTP receipt/final outcomes and evaluator-owned arrival traces are inputs.
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