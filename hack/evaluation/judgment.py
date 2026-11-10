"""Offline request judgment, experiment summaries and publication measurements."""
from bisect import bisect_right
from collections import Counter
import json
import math
from pathlib import Path

import evidence_io

HERE = Path(__file__).resolve().parent


def collection_loss(planned, received, drained):
    """Missing evaluator client rows are not proof of unissued requests."""
    return 0 if drained else max(0, planned - received)


def reduce_requests(config, phase, clients, indexes, *, closed_through=None):
    """Pure request judgment shared by provisional and drained publication."""
    stage = phase['stage']
    analysis = evidence_io.module('evaluation_analysis', HERE / 'request_analysis.py')
    raw = evidence_io.module('evaluation_raw', HERE / 'raw.py')
    results = {}
    for workload in config['workloads']:
        name = workload['name']
        trace = stage['traces'][name]
        stream = clients[name]
        drained = (phase.get('producerCapture', {}).get(name, {}).get('drained', False) or
                   any(row.get('evaluationEvent') == 'drained' for row in stream))
        rows = [row for row in stream if 'scheduledOffsetNs' in row]
        if closed_through is not None:
            count = 0
            if trace['offsetsNS']:
                horizon = evidence_io.timestamp_ns(closed_through) - evidence_io.timestamp_ns(phase['starts'][name]['at'])
                count = bisect_right(trace['offsetsNS'], horizon)
            trace = {**trace, 'offsetsNS': trace['offsetsNS'][:count], 'offered': count}
            rows = [row for row in rows if type(row.get('requestIndex')) is int and 0 <= row['requestIndex'] < count]
        indexed = indexes[workload['uid']]
        returned_ids = [row['requestID'] for row in rows if isinstance(row.get('requestID'), str) and row['requestID']]
        counts = Counter(returned_ids)
        quarantined = {rid for rid, count in counts.items() if count > 1}
        quarantined.update(item['requestID'] for item in indexed['summary'].get('finalConflicts', []))
        quarantined.update(indexed['summary'].get('invalidFinalRequestIDs', []))
        # A row naming another Workload is set aside like other quarantined rows; it never
        # counts as a success here, the offered denominator is unchanged, and the count is reported.
        mismatched = [row for row in rows if row.get('workloadUID') not in (None, workload['uid'])]
        usable = [row for row in rows if row.get('requestID') not in quarantined and
                  row.get('workloadUID') in (None, workload['uid'])]
        client_failures = {'http_error', 'timeout', 'transport_error', 'canceled'}
        failure_rows = [row for row in usable if analysis.client_outcome(row) in client_failures]
        supported = bool(failure_rows) or any(row.get('requestID') in indexed['finals'] and
                                            analysis.client_outcome(row) == 'completed' for row in usable)
        recorded_gaps = indexed['summary'].get('observationGaps', [])
        window_start = evidence_io.timestamp_ns(phase['startUTC']) if recorded_gaps else 0
        window_end = evidence_io.timestamp_ns(closed_through or phase['endUTC']) if recorded_gaps else 0
        gaps = raw.overlapping_gaps(recorded_gaps, window_start, window_end) if recorded_gaps else []
        start = phase['starts'].get(name, {}).get('monotonicNS')
        quality = analysis.issuance_fidelity(rows, trace['offsetsNS'], start, round(stage['seconds'] * 1e9), analysis.stats, config['generatorQuality'])
        completed, gateway, actual, scheduled, outcomes, server_outcomes = [], [], [], [], Counter(), Counter()
        for row in usable:
            final = indexed['finals'].get(row.get('requestID'))
            server_outcomes[final.get('outcome') if final else 'unfinished-or-no-final'] += 1
            timing = analysis.request_timing(row, final, None, start)
            outcomes[timing['clientOutcome']] += 1
            gateway.append(timing['gatewayCompletedLatencyNS'])
            if timing['sloLatencyNS'] is not None:
                completed.append(timing['sloLatencyNS'])
            actual.append(timing['actualLatencyNS'])
            scheduled.append(timing['scheduledLatencyNS'])
        offered = trace['offered']
        loss = collection_loss(offered, len(rows), drained)
        hits = sum(value <= workload['slo']['targetLatencyNanos'] for value in completed)
        required = math.ceil(offered * workload['slo']['percentile'] / 100)
        qstatus = quality['generatorQuality']['status']
        identities = indexed['identities']
        identity_known = (any(row.get('requestID') in indexed['finals'] for row in usable)
                          and len(identities) == 1 and identities[0]['workloadUID'] == workload['uid']
                          and bool(identities[0]['definitionID']))
        substages, base = [], 0
        for part in stage['parts']:
            stop = base + round(part['seconds'] * 1e9)
            planned = sum(base <= offset < stop for offset in trace['offsetsNS'])
            latencies, failures = [], 0
            part_rows = [row for row in rows if base <= row['scheduledOffsetNs'] < stop]
            part_issued = analysis.issued_calls(part_rows)
            part_supported = any((row.get('requestID') in indexed['finals'] and analysis.client_outcome(row) == 'completed') or
                                 analysis.client_outcome(row) in client_failures
                                 for row in usable if base <= row['scheduledOffsetNs'] < stop)
            for row in usable:
                if base <= row['scheduledOffsetNs'] < stop:
                    timing = analysis.request_timing(row, indexed['finals'].get(row.get('requestID')), None, start)
                    latency = timing['sloLatencyNS']
                    failures += timing['clientOutcome'] in client_failures
                    if latency is not None:
                        latencies.append(latency)
            successes = sum(value <= workload['slo']['targetLatencyNanos'] for value in latencies)
            rank = math.ceil(planned * workload['slo']['percentile'] / 100)
            part_gaps = []
            if planned and gaps and name in phase['starts']:
                part_start = evidence_io.timestamp_ns(phase['starts'][name]['at'])
                part_gaps = raw.overlapping_gaps(gaps, part_start + base, min(part_start + stop, window_end))
            substages.append({'id': part['id'], 'durationSeconds': part['seconds'], 'rate': part['rates'][name], 'offered': planned,
                              'completedLatencyNS': analysis.stats(latencies), 'deadlineSuccess': successes, 'requiredSuccessfulRank': rank,
                              'clientFailures': failures, **analysis.slo_quantile(latencies, failures, planned, workload['slo']['percentile']),
                              'D1': 'NOT_OFFERED' if not planned else 'NOT_EVALUATED' if part_gaps else
                                    'INVALID_NO_LOAD' if not part_issued else 'NOT_EVALUATED' if not part_supported else
                                    'FAIL' if successes < rank else 'OBSERVED_THRESHOLD_MET',
                              'observationGaps': part_gaps})
            base = stop
        results[name] = {'offered': offered, 'clientRows': len(rows), 'outcomes': dict(outcomes), 'completedLatencyNS': analysis.stats(completed),
                         'gatewayCompletedLatencyNS': analysis.stats(gateway), 'serverOutcomes': dict(server_outcomes),
                         'actualLatencyNS': analysis.stats(actual), 'scheduledLatencyNS': analysis.stats(scheduled),
                         'latencyScope': 'Client prepared-request send to full response-body read (elapsedNanos); server elapsed and caller/scheduled durations are diagnostics',
                         'clientFailures': len(failure_rows), 'clientUnknown': max(0, offered - len(completed) - len(failure_rows)),
                         **analysis.slo_quantile(completed, len(failure_rows), offered, workload['slo']['percentile']),
                         'deadlineSuccess': hits, 'deadlineSuccessFraction': hits / offered if offered else None, 'requiredSuccessfulRank': required,
                         'unfinishedOrUnsent': max(0, offered - len(completed) - len(failure_rows) - loss),
                         'collectionLoss': loss, 'issuanceFidelity': quality,
                         'reachable': hits + max(0, offered - sum(row.get('requestID') in indexed['finals'] or
                                                               analysis.client_outcome(row) in client_failures for row in usable)) >= required,
                         'D1': 'NOT_OFFERED' if not offered else 'NOT_EVALUATED' if gaps else
                               'INVALID_NO_LOAD' if qstatus == 'INVALID_NO_LOAD' else
                               'NOT_EVALUATED' if not supported else 'FAIL' if hits < required else 'OBSERVED_THRESHOLD_MET',
                         'observationGaps': gaps,
                         'slo': workload['slo'], 'workload': {key: workload[key] for key in ('name', 'uid', 'generation')},
                         'workloadUID': workload['uid'],
                         'definitionID': identities[0]['definitionID'] if identity_known else None,
                         'actualGatewayIdentities': len(identities), 'observedDefinitions': identities, 'substages': substages,
                         'rawEvidenceErrors': indexed['errors'], 'quarantinedRequestIDs': sorted(quarantined),
                         'quarantinedClientRows': len(rows)-len(usable),
                         'identityMismatchClientRows': len(mismatched),
                         'rawEvidenceErrorCount': len(indexed['errors']), 'quarantinedRequestCount': len(quarantined),
                         'unkeyedRawRowCount': len(indexed['summary'].get('unkeyedRows', [])),
                         'identityMissingReason': None if identity_known else 'Actual returned request IDs lack one complete frozen-UID definition identity'}
    return results


def provisional_requests(config, phase, clients, indexes, as_of, closed_through):
    """Bounded projection of the same request reducer; never publish a verdict."""
    results = reduce_requests(config, phase, clients, indexes, closed_through=closed_through)
    keys = ('offered', 'deadlineSuccess', 'requiredSuccessfulRank', 'outcomes', 'unfinishedOrUnsent', 'collectionLoss', 'slo', 'reachable')
    return {'asOf': as_of, 'closedThrough': closed_through, 'provisional': True,
            'workloads': {name: {**{key: row[key] for key in keys},
                                'substages': [{key: value for key, value in part.items() if key not in ('D1', 'observationGaps')} for part in row['substages']]}
                          for name, row in results.items()},
            'generatorQuality': {name: row['issuanceFidelity'] for name, row in results.items()}}


def judge(binding, folder):
    bound = evidence_io.load_binding(binding)
    config = bound['config']
    phase = evidence_io.load(folder / 'phase.json')
    raw = evidence_io.module('evaluation_raw', HERE / 'raw.py')
    clients = {}
    unknown = []
    for workload in config['workloads']:
        name = workload['name']
        path = folder / (name + '-client.jsonl')
        rows = []
        if path.exists():
            for index, line in enumerate(path.read_text().splitlines(), 1):
                try:
                    row = json.loads(line)
                    if isinstance(row, dict):
                        rows.append(row)
                except (ValueError, TypeError):
                    unknown.append(name + ': malformed client JSONL line ' + str(index))
        clients[name] = rows
    indexes = raw.iter_request_indexes(folder, [w['uid'] for w in config['workloads']], phase['startUTC'], phase['endUTC'],
                                      actual_response_ids={w['uid']: [r['requestID'] for r in clients[w['name']] if isinstance(r.get('requestID'), str) and r['requestID']] for w in config['workloads']})
    results = reduce_requests(config, phase, clients, indexes)
    power = evidence_io.load(folder / 'power.json')
    tail_power = evidence_io.load(folder / 'power-drain-inclusive.json')
    failures = None
    if config['evaluation'].get('failureInjection'):
        failures = evidence_io.load(folder / 'failure-events.json')
    statuses = [row['D1'] for row in results.values()]
    measured = any(status in ('FAIL', 'OBSERVED_THRESHOLD_MET') for status in statuses)
    slo_status = ('FAIL' if 'FAIL' in statuses else 'PASS' if measured else
                  'INVALID_NO_LOAD' if 'INVALID_NO_LOAD' in statuses else 'NOT_EVALUATED')
    result = {'schema': 'e5-judgment.v3', 'failurePolicy': 'v2', 'at': evidence_io.now(),
              'status': 'FAIL' if slo_status=='FAIL' else 'MEASURED_DESCRIPTIVE' if measured else 'PARTIAL',
              'notReadyReasons': [], 'clientParseErrors': unknown, 'requests': results, 'power': power, 'powerDrainInclusive': tail_power,
              'components': {'slo': {'status': slo_status, 'workloads': {name: row['D1'] for name,row in results.items()}},
                             'loadFidelity': {name: row['issuanceFidelity'] for name,row in results.items()},
                             'energy': {'offered': power.get('energyStatus'), 'drainInclusive': tail_power.get('energyStatus')},
                             'drain': phase.get('drain', {}),
                             'injection': {'status': 'NOT_REQUESTED' if failures is None else 'REALIZED' if
                                 len(failures.get('records', [])) == len(config['evaluation']['failureInjection']) and
                                 all(item.get('record', {}).get('outcome') == 'Complete' for item in failures.get('records', []))
                                 else 'NOT_REALIZED', 'evidence': failures}},
              'ignoredKeys': config.get('ignoredKeys', []), 'failureScenarioEvidence': failures,
              'limitations': bound['limitations'], 'judgmentCriteria': ['independent SLO, load fidelity, energy, injection and drain facts']}
    resources = folder / 'producer-resources.json'
    result['cgroups'] = evidence_io.load(resources) if resources.exists() else {'rows': [], 'meaning': 'No generator offered in this window'}
    evidence_io.save(folder / 'judgment.json', result)
    return result


def summarize(binding, root, *, evaluation=None):
    stages = evaluation['stages'] if evaluation else []
    results = evaluation.get('results', {}) if evaluation else {}
    measurements = [value.get('measurement', {}).get('verdict', 'NOT_EVALUATED') for value in results.values()]
    if measurements:
        selected = next(value.get('measurement', {'verdict': 'NOT_EVALUATED'}) for verdict in ('INVALID', 'NOT_EVALUATED', 'FAIL', 'PASS')
                        for value in results.values()
                        if value.get('measurement', {}).get('verdict', 'NOT_EVALUATED') == verdict)
        status = 'MEASURED_DESCRIPTIVE' if selected.get('verdict') == 'PASS' else selected.get('verdict', 'NOT_EVALUATED')
    else:
        slo_statuses = [stage['kpis'].get('components', {}).get('slo', {}).get('status') for stage in stages]
        status = 'FAIL' if 'FAIL' in slo_statuses else 'MEASURED_DESCRIPTIVE' if 'PASS' in slo_statuses else 'NOT_EVALUATED'
    result = {'schema': 'e5-summary.v4', 'failurePolicy': 'v2', 'stages': stages, 'status': status, 'results': results,
              'completeness': 'PARTIAL' if 'NOT_EVALUATED' in measurements or len(stages) < len(results) or any(
                  stage['kpis'].get('power', {}).get('complete') is False or stage['kpis']['status']=='PARTIAL' for stage in stages) else 'OBSERVED',
              'components': {'measurement': {key: value.get('measurement') for key,value in results.items()},
                             'execution': {key: value.get('execution') for key,value in results.items()},
                             'energy': {stage['id']: stage['kpis'].get('power') for stage in stages}}}
    if measurements and selected.get('reason'):
        result['reason'] = selected['reason']
    evidence_io.save(root / 'e5-summary.json', result)
    return result


def publication_measurements(binding, power):
    return {'measurementContract': {'binding': 'e5-binding.v4', 'judgment': 'e5-judgment.v3', 'failurePolicy': 'v2', 'energyScope': 'declared-node-meters', 'energyCoverage': binding['config']['collection']['energyCoverage']}}


def publication_stage_metadata(binding, phase, definitions, identity_source, phase_source, folder, root, rows):
    workloads = {w['name']: w for w in definitions['workloads']}
    for name, row in rows.items():
        workload = workloads[name]
        known = row.get('workloadUID') == workload['uid'] and row.get('definitionID') is not None
        row.update(workload={key: workload[key] for key in ('name', 'uid', 'generation')}, slo=workload['slo'],
                   identitySource=identity_source if known else None)


