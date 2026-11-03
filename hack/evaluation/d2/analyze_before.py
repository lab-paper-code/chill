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


def issued_calls(client):
    return [row for row in client if row.get('issued') is not False and row.get('error') != 'UnsentAtWindowEnd'
            and (row.get('issued') is True or type(row.get('sentMonotonicNS')) is int or row.get('requestID'))]


def issuance_fidelity(client, offsets, start_ns, duration_ns, stats, quality=None):
    """Timing thresholds label observations; only zero evidenced sends invalidate load."""
    issued = issued_calls(client)
    # v2 physical boundary: at least one actual infer-call/send or returned request
    # is needed to claim an offered-load experiment. No arbitrary lateness cliff.
    no_load = bool(offsets) and not issued
    timed = [row for row in issued if type(start_ns) is int and type(row.get('sentMonotonicNS')) is int
             and row['sentMonotonicNS'] >= start_ns and type(row.get('scheduledOffsetNs')) is int]
    sent = sorted(row['sentMonotonicNS'] - start_ns for row in timed)
    late = [row['sentMonotonicNS'] - start_ns - row['scheduledOffsetNs'] for row in timed]
    indexes = [row.get('requestIndex') for row in client if type(row.get('requestIndex')) is int]
    invalid_schedule = sum(type(row.get('requestIndex')) is not int or not 0 <= row['requestIndex'] < len(offsets) or
                           row.get('scheduledOffsetNs') != offsets[row['requestIndex']] for row in client)
    duplicates = len(indexes) - len(set(indexes))
    supplied = len(issued) == len(offsets) and len(client) == len(offsets) and not invalid_schedule and not duplicates
    threshold = quality['latenessToleranceNS'] if quality else 50_000_000
    exceeding = sum(value > threshold for value in late)
    fraction = exceeding / len(late) if late else None
    horizon = max(duration_ns, sent[-1] if sent else 0)
    edges, planned = [0] + sent + [horizon], [0] + offsets + [duration_ns]
    gaps = [right - left for left, right in zip(edges, edges[1:])]
    return {'failurePolicy': 'v2', 'status': 'NOT_OFFERED' if not offsets else 'PARTIAL' if not supplied else 'OBSERVED',
            'thresholdNS': threshold, 'offered': len(offsets), 'issued': len(issued),
            'unissued': max(0, len(offsets) - len(issued)), 'missingClientRows': max(0, len(offsets) - len(client)),
            'extraClientRows': max(0, len(client) - len(offsets)), 'duplicateIndexes': duplicates,
            'invalidScheduleRows': invalid_schedule, 'missingSendTimestamps': len(issued) - len(timed),
            'supplied': supplied, 'issuanceFraction': len(issued) / len(offsets) if offsets else None,
            'durationNS': duration_ns, 'achievedRequestsPerSecond': len(issued) / (duration_ns / 1e9) if duration_ns else None,
            'sendsAfterWindow': sum(at >= duration_ns for at in sent), 'sendLatenessNS': stats(late),
            'actualGapNS': stats(gaps), 'plannedGapNS': stats([b-a for a,b in zip(planned, planned[1:])]),
            'generatorQuality': {'status': 'INVALID_NO_LOAD' if no_load else 'NOT_OFFERED' if not offsets else 'OBSERVED',
                                 'policy': quality, 'exceeding': exceeding, 'denominator': len(late),
                                 'exceedingFraction': fraction,
                                 'thresholdLabel': 'UNKNOWN' if fraction is None or quality is None else
                                 'EXCEEDS_REPORTED_THRESHOLD' if fraction > quality['maxExceedingFraction'] else 'WITHIN_REPORTED_THRESHOLD',
                                 'reason': 'No evidenced infer-call/send or returned request' if no_load else None}}