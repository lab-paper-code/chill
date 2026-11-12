"""Offline request timing, empirical statistics and generator issuance quality.

Client exchange evidence, HTTP receipt/final facts and frozen arrival traces are inputs.
"""
from datetime import datetime
import math
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
    """Scheduled reference only; SLO starts at the actual prepared-request send."""
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


def client_outcome(row):
    """Keep unsent work and legacy/missing client measurements unknown."""
    if row.get('issued') is False or row.get('error') == 'UnsentAtWindowEnd':
        return 'unissued'
    elapsed = row.get('elapsedNanos')
    if type(elapsed) is not int or elapsed < 0:
        return 'unknown'
    outcome, status = row.get('outcome'), row.get('status')
    if outcome == 'completed':
        return outcome if type(status) is int and 200 <= status < 300 and not row.get('error') else 'unknown'
    if outcome == 'http_error':
        return outcome if type(status) is int and 100 <= status < 600 and not 200 <= status < 300 else 'unknown'
    return outcome if outcome in ('timeout', 'transport_error', 'canceled') else 'unknown'


def request_timing(row, final, received_ns, start_ns):
    """Use exact client full-read E for SLO; retain native completion guards."""
    server_elapsed = final.get('elapsedNanos') if final else None
    server_elapsed = server_elapsed if type(server_elapsed) is int and server_elapsed >= 0 else None
    executed = final and final.get('outcome') == 'completed' and final.get('executionComplete') is True
    outcome = client_outcome(row)
    elapsed = row.get('elapsedNanos') if outcome not in ('unknown', 'unissued') else None
    slo = elapsed if outcome == 'completed' and executed else None
    sent, finished = row.get('sentMonotonicNS'), row.get('finishedMonotonicNS')
    actual = finished - sent if type(sent) is int and type(finished) is int and finished >= sent and row.get('issued') is not False else None
    wall_sent = row.get('sentAtUnixNS')
    entrance = received_ns - wall_sent if type(received_ns) is int and type(wall_sent) is int and row.get('issued') is not False else None
    return {'sloLatencyNS': slo, 'scheduledLatencyNS': scheduled_latency_ns(row, start_ns),
            'clientElapsedNS': elapsed, 'clientOutcome': outcome,
            'serverElapsedNS': server_elapsed, 'gatewayCompletedLatencyNS': server_elapsed if executed else None,
            'actualLatencyNS': actual, 'entranceDelayNS': entrance,
            'entranceDelayMeaning': 'Gateway receivedAt minus client sentAtUnixNS; cross-host clock offset/uncertainty retained, including negative values; not one-way network latency'}


def rejection(value):
    """Read explicit overload metadata; never infer a policy from old rows."""
    if (isinstance(value, dict) and value.get('code') in ('QueueFull', 'DeadlineInfeasible') and
            value.get('stage') in ('gateway', 'executor') and value.get('policy') in ('Reject', 'Delay')):
        return {key: value[key] for key in ('code', 'stage', 'policy')}
    return None


def request_class(row, final, timing, target_ns):
    """Join flat client rejection fields with server admission facts.

    Non-admission HTTP rejections are failures. A client timeout cannot become
    a native late success; incomplete is reserved for missing/conflicting facts.
    """
    client = timing['clientOutcome']
    server = final.get('outcome') if final else None
    if client in ('unknown', 'unissued'):
        return 'incomplete', None, None
    if server == 'timeout':
        return 'timeout', 'serverFinalDeadline', None
    if client == 'timeout':
        return 'timeout', 'clientSocket', None
    if timing['sloLatencyNS'] is not None:
        return ('onTime' if timing['sloLatencyNS'] <= target_ns else 'late'), None, None
    if client == 'http_error':
        native = rejection(final.get('rejection')) if final else None
        http = rejection({'code': row.get('rejectionCode'), 'stage': row.get('rejectionStage'),
                          'policy': row.get('rejectionPolicy')})
        if (final and 'rejection' in final and native is None or
                row.get('rejectionCode') in ('QueueFull', 'DeadlineInfeasible') and http is None):
            return 'incomplete', None, None
        metadata = native or http
        if metadata:
            expected = 429 if metadata['code'] == 'QueueFull' else 503
            if (row['status'] != expected or server not in (None, 'rejected') or
                    native and http and native != http or server == 'rejected' and native is None):
                return 'incomplete', None, None
            return 'rejected', None, metadata
    if client in ('http_error', 'transport_error', 'canceled') or server in ('error', 'canceled', 'stopped'):
        return 'failed', None, None
    return 'incomplete', None, None


ADMISSION_WAIT_REASONS = (
    'PreIPCRunning', 'ProfileMismatch', 'MissingPolicyOrBatch', 'ResidualUnsupported',
    'MissingObservedAt', 'NoCapacity', 'AdmissionStopped', 'DeadlineInfeasible')
DIAGNOSTIC_FIELDS = (
    'admissionPrediction', 'admissionWaits', 'deferredNanos', 'nativeDeferredAt',
    'nativeHTTP', 'serverTiming', 'gatewayWaitNanos', 'executorWaitNanos',
    'nativeElapsedMS', 'nativeOutcome', 'nativeError', 'error', 'rejection')


def transport_accounting(facts):
    """Subtract only same-clock offsets; native spans overlap gateway exchange."""
    timing = facts['serverTiming']
    gateway = {item['stage']: item.get('elapsedNanos') for item in timing
               if isinstance(item, dict) and isinstance(item.get('stage'), str)} if isinstance(timing, list) else {}
    def span(offsets, left, right):
        a, b = offsets.get(left), offsets.get(right)
        return b - a if type(a) is int and type(b) is int and 0 <= a <= b else None
    stages = ('workerHTTPStart', 'workerHTTPGetConn', 'workerHTTPGotConn',
              'workerHTTPWroteRequest', 'workerHTTPFirstResponseByte', 'workerHTTPDone')
    segments = {left + 'To' + right[0].upper() + right[1:]: span(gateway, left, right)
                for left, right in zip(stages, stages[1:])}
    total = span(gateway, stages[0], stages[-1])
    offsets = [gateway.get(stage) for stage in stages]
    present = [value for value in offsets if type(value) is int]
    valid = (total is not None and
             all(value is None or type(value) is int and offsets[0] <= value <= offsets[-1] for value in offsets) and
             all(a <= b for a, b in zip(present, present[1:])))
    if not valid:
        segments = dict.fromkeys(segments)
    measured = sum(value for value in segments.values() if value is not None)
    native = facts['nativeHTTP'] if isinstance(facts['nativeHTTP'], dict) else {}
    elapsed_ms = facts.get('nativeElapsedMS')
    native_elapsed = elapsed_ms * 1_000_000 if type(elapsed_ms) in (int, float) and math.isfinite(elapsed_ms) else None
    prefix = native.get('receivedNanos')
    suffix = span(native, 'finishedNanos', 'responseWriteStartNanos')
    outside = total - native_elapsed if total is not None and native_elapsed is not None else None
    residual = outside - prefix - suffix if outside is not None and type(prefix) is int and suffix is not None else None
    return {'gatewayExchangeNanos': total, 'gatewaySegmentsNanos': segments,
            'gatewayUnmeasuredNanos': total - measured if total is not None else None,
            'exchangeOutsideNativeNanos': outside,
            'outsideNativeUnmeasuredNanos': residual,
            'connectDiagnosticNanos': span(gateway, 'workerHTTPConnectStart', 'workerHTTPConnectDone'),
            'nativeSegmentsNanos': {
                'decode': span(native, 'decodeStartNanos', 'decodeDoneNanos'),
                'decodeToEnqueue': span(native, 'decodeDoneNanos', 'enqueueNanos'),
                'handlerEntryToReceived': native.get('receivedNanos'),
                'enqueueToFinished': span(native, 'enqueueNanos', 'finishedNanos'),
                'finishedToResponseWriteStart': span(native, 'finishedNanos', 'responseWriteStartNanos')},
            'meaning': 'Gateway segments are exclusive same-clock spans. Connect overlaps acquisition. Native spans use a separate local clock and overlap gateway exchange; response-write completion and one-way network latency are unmeasured.'}


def request_diagnostics(final, native_records):
    """Retain diagnostics without changing HTTP outcome or completion.

    Finished native rows supply outcome, elapsed and executor wait. Partial
    elapsed is not a completed span; direct final waits remain authoritative.
    Disagreement between completion facts stays explicit and unknown.
    """
    final = final or {}
    facts = {key: final.get(key) for key in DIAGNOSTIC_FIELDS}
    sources = {key: 'final' if facts[key] is not None else 'missing' for key in facts}
    for key in ('nativeDeferredAt', 'nativeHTTP'):
        values = [record['row'][key] for record in native_records if record['row'].get(key) is not None]
        if facts[key] is not None:
            values.insert(0, facts[key])
        if not values:
            continue
        if key == 'nativeHTTP' and all(isinstance(value, dict) for value in values):
            merged, conflict = {}, False
            for value in values:
                for field, reached in value.items():
                    if reached is not None:
                        conflict |= field in merged and merged[field] != reached
                        merged.setdefault(field, reached)
            if not conflict:
                facts[key] = merged
                if sources[key] == 'missing':
                    sources[key] = 'nativeSupplement'
                elif merged != final[key]:
                    sources[key] = 'final+nativeSupplement'
                continue
        elif all(value == values[0] for value in values):
            facts[key] = values[0]
            if sources[key] == 'missing':
                sources[key] = 'nativeSupplement'
            continue
        facts[key], sources[key] = None, 'conflictingNativeSupplements'
    def finished(row):
        return (bool(row.get('nativeOutcome')) or row.get('nativeExecutionComplete') is True or
                row.get('nativeFinishedAt') is not None)
    completed = [record['row'] for record in native_records if finished(record['row'])]
    for key in ('nativeOutcome', 'nativeElapsedMS', 'executorWaitNanos'):
        direct = facts[key] is not None and (key == 'executorWaitNanos' or finished(final))
        values = ([facts[key]] if direct else []) + [
            row[key] for row in completed if row.get(key) is not None]
        if not values:
            if key == 'nativeElapsedMS' and facts[key] is not None:
                facts[key], sources[key] = None, 'partialNativeProgress'
            continue
        if all(value == values[0] for value in values):
            facts[key] = values[0]
            sources[key] = 'final' if direct else 'nativeSupplement'
        else:
            facts[key], sources[key] = None, 'conflictingNativeSupplements'
    facts['fieldSources'] = sources
    facts['nativeSupplements'] = [
        {'ref': record['ref'], **{key: record['row'].get(key) for key in DIAGNOSTIC_FIELDS},
         'nativeExecutionComplete': record['row'].get('nativeExecutionComplete'),
         'nativeFinishedAt': record['row'].get('nativeFinishedAt')} for record in native_records]
    facts['transportAccounting'] = transport_accounting(facts)
    return facts


def non_on_time_breakdown(requests, missing_rows):
    """Counts overlap. Observed waits and timings do not prove a failure cause."""
    selected = [row for row in requests if row['outcomeClass'] != 'onTime']
    reasons = dict.fromkeys(ADMISSION_WAIT_REASONS, 0)
    reasons.update(unknown=0, missing=missing_rows, none=0)
    missing = {key: missing_rows for key in DIAGNOSTIC_FIELDS}
    sources = {}
    durations = {}
    states = {}
    predictions = {'withinRemainingSLO': 0, 'exceedsRemainingSLO': 0, 'unknown': missing_rows}
    rejections, native_outcomes, errors = {}, {}, {}
    for row in selected:
        facts = row['facts']
        for key in missing:
            if facts[key] is None:
                missing[key] += 1
        waits = facts['admissionWaits']
        observed = {wait.get('reason') for wait in waits if isinstance(wait, dict)} if isinstance(waits, list) else set()
        if not observed:
            reasons['none' if waits == [] else 'missing'] += 1
        for reason in observed:
            reasons[reason if reason in ADMISSION_WAIT_REASONS else 'unknown'] += 1
        for state in {wait.get('state', 'unknown') for wait in waits if isinstance(wait, dict)} if isinstance(waits, list) else ():
            states[state] = states.get(state, 0) + 1
        prediction = facts['admissionPrediction']
        predicted = prediction.get('predictedCompletionNanos') if isinstance(prediction, dict) else None
        remaining = prediction.get('remainingSLONanos') if isinstance(prediction, dict) else None
        if type(predicted) is int and type(remaining) is int:
            predictions['withinRemainingSLO' if predicted <= remaining else 'exceedsRemainingSLO'] += 1
            durations.setdefault('predictedCompletionNanos', []).append(predicted)
            durations.setdefault('remainingSLONanos', []).append(remaining)
        else:
            predictions['unknown'] += 1
        rejection = facts['rejection']
        if isinstance(rejection, dict):
            key = '/'.join(str(rejection.get(field, 'unknown')) for field in ('stage', 'policy', 'code'))
            rejections[key] = rejections.get(key, 0) + 1
        for field, counts in (('nativeOutcome', native_outcomes), ('error', errors)):
            value = facts[field]
            if isinstance(value, str) and value:
                counts[value] = counts.get(value, 0) + 1
        for key, source in facts['fieldSources'].items():
            counts = sources.setdefault(key, {})
            counts[source] = counts.get(source, 0) + 1
        for key in ('deferredNanos', 'gatewayWaitNanos', 'executorWaitNanos'):
            value = facts[key]
            if type(value) is int and value >= 0:
                durations.setdefault(key, []).append(value)
        for key in ('nativeHTTP', 'serverTiming'):
            value = facts[key]
            if isinstance(value, dict):
                for field, elapsed in value.items():
                    if field.endswith(('Nanos', 'NS')) and type(elapsed) is int and elapsed >= 0:
                        durations.setdefault(key + '.' + field, []).append(elapsed)
        accounting = facts['transportAccounting']
        for key in ('gatewayExchangeNanos', 'gatewayUnmeasuredNanos', 'connectDiagnosticNanos',
                    'exchangeOutsideNativeNanos', 'outsideNativeUnmeasuredNanos'):
            if accounting[key] is not None:
                durations.setdefault(key, []).append(accounting[key])
        for group in ('gatewaySegmentsNanos', 'nativeSegmentsNanos'):
            for key, value in accounting[group].items():
                if value is not None:
                    durations.setdefault(group + '.' + key, []).append(value)
    return {'requests': len(selected) + missing_rows,
            'directFacts': {'admissionWaitReasons': reasons, 'missingFields': missing,
                            'admissionWaitStates': states, 'admissionPrediction': predictions,
                            'rejections': rejections, 'nativeOutcomes': native_outcomes, 'errors': errors,
                            'fieldSources': sources, 'timingNS': {key: stats(values) for key, values in sorted(durations.items())}},
            'meaning': 'Wait reasons and recorded timings are overlapping direct observations, not exclusive root causes. Missing evidence remains unknown. Native supplements never change HTTP/client classifications. Connect/native spans overlap gateway segments and must not be summed; residual retains unmeasured transport/write/read costs.'}


def request_metrics(rows, finals, target_ns, offered, duration_ns, start_ns, native_records=None, received_at_ns=None):
    """Reduce an arrival cohort, including its drain completions."""
    classes = dict.fromkeys(('onTime', 'late', 'rejected', 'timeout', 'failed', 'incomplete'), 0)
    causes = dict.fromkeys(('serverFinalDeadline', 'clientSocket'), 0)
    waits = {'gateway': [], 'executor': []}
    groups = {}
    requests = []
    native_records, received_at_ns = native_records or {}, received_at_ns or {}
    for row in rows:
        rid = row.get('requestID')
        final = finals.get(rid)
        timing = request_timing(row, final, received_at_ns.get(rid), start_ns)
        outcome, cause, metadata = request_class(row, final, timing, target_ns)
        requests.append({'requestID': rid, 'requestIndex': row.get('requestIndex'),
                         'outcomeClass': outcome, 'timeoutCause': cause,
                         'timing': timing,
                         'facts': request_diagnostics(final, native_records.get(rid, []))})
        classes[outcome] += 1
        if cause:
            causes[cause] += 1
        group = None
        if metadata:
            key = tuple(metadata[key] for key in ('policy', 'stage', 'code'))
            group = groups.setdefault(key, {**metadata, 'count': 0, 'gateway': [], 'executor': []})
            group['count'] += 1
        for stage in waits:
            value = final.get(stage + 'WaitNanos') if final else None
            if type(value) is int and value >= 0:
                waits[stage].append(value)
                if group is not None:
                    group[stage].append(value)
    classes['incomplete'] += offered - len(rows)
    return {'outcomeClasses': classes, 'timeoutCauses': causes,
            'requestAnalysis': requests,
            'nonOnTimeBreakdown': non_on_time_breakdown(requests, offered - len(rows)),
            'onTimeSuccessRate': classes['onTime'] / offered if offered else None,
            'loadLengthSeconds': duration_ns / 1e9,
            'goodputRequestsPerSecond': classes['onTime'] / (duration_ns / 1e9) if duration_ns else None,
            'gatewayWaitNS': stats(waits['gateway']), 'executorWaitNS': stats(waits['executor']),
            'rejections': [{**{key: group[key] for key in ('code', 'stage', 'policy', 'count')},
                            'gatewayWaitNS': stats(group['gateway']), 'executorWaitNS': stats(group['executor'])}
                           for _, group in sorted(groups.items())]}


def slo_quantile(completed, failures, offered, percentile):
    """Failures occupy the infinite tail; missing evidence is never completion."""
    rank = math.ceil(offered * percentile / 100)
    if rank > offered - failures:
        return {'quantileNanos': None, 'quantileStatus': 'unbounded'}
    if not rank or len(completed) + failures != offered:
        return {'quantileNanos': None, 'quantileStatus': 'unknown'}
    return {'quantileNanos': sorted(completed)[rank - 1], 'quantileStatus': 'finite'}


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