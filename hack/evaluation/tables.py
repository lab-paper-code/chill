"""Render black-box judgments; never infer or reinterpret historical artifacts."""
import json
from pathlib import Path

from judgment import energy_boundary


def sections(path):
    if not Path(path).exists():
        return None
    value = json.loads(Path(path).read_text())
    if value.get('schema') != 'e5-judgment.v4':
        raise ValueError('FrozenInputInvalid: judgment v4 required')
    return value


def publication_requests(judgment):
    fields = ('offered', 'clientRows', 'unfinishedOrUnsent', 'collectionLoss', 'outcomes', 'completedLatencyNS',
              'outcomeClasses', 'timeoutCauses', 'onTimeSuccessRate', 'goodputRequestsPerSecond', 'loadLengthSeconds',
              'rejections', 'gatewayWaitNS', 'executorWaitNS',
              'gatewayCompletedLatencyNS', 'serverOutcomes', 'latencyScope', 'scheduledLatencyNS', 'actualLatencyNS',
              'clientFailures', 'clientUnknown', 'quantileNanos', 'quantileStatus',
              'deadlineSuccess', 'deadlineSuccessFraction', 'requiredSuccessfulRank', 'D1', 'slo', 'workload',
              'workloadUID', 'definitionID', 'actualGatewayIdentities', 'substages', 'identityMissingReason',
              'quarantinedRequestIDs', 'quarantinedClientRows', 'identityMismatchClientRows', 'quarantinedRequestCount',
              'observedDefinitions', 'rawEvidenceErrorCount', 'unkeyedRawRowCount', 'observationGaps')
    result = {}
    for name, value in judgment['requests'].items():
        row = {key: value[key] for key in fields if key in value}
        quality = value['issuanceFidelity']
        row['issuanceFidelity'] = quality
        result[name] = row
    return result


def render(root, repository=None, *, evaluation=None):
    if evaluation is None:
        evaluation = json.loads((Path(root) / 'e5-summary.json').read_text())
    if evaluation.get('schema') != 'e5-summary.v5':
        raise ValueError('FrozenInputInvalid: summary v5 required')
    lines = ['Evaluation: ' + evaluation['status'],
             'stage | workload | offered | completed | on-time success rate | goodput req/s | completed latency ns p50/p95/p99/max | SLO | declared-meter J']
    for stage in evaluation.get('stages', []):
        kpis = stage['kpis']
        energy = kpis.get('wholePoolJoules')
        for name, result in kpis['requests'].items():
            lines.append(' | '.join(str(value) for value in (stage['id'], name, result['offered'], result['completedLatencyNS']['samples'],
                                                           result.get('onTimeSuccessRate'), result.get('goodputRequestsPerSecond'),
                                                           '/'.join(str(result['completedLatencyNS'].get(key)) for key in ('p50', 'p95', 'p99', 'max')),
                                                           result['D1'], 'UNKNOWN' if energy is None else energy)))
            for gap in result.get('observationGaps', []):
                lines.append('Observation gap: ' + json.dumps(gap, sort_keys=True))
    lines.append('stage | energy scope | nodes (name@UID) | J | average W | status | reasons')
    for stage in evaluation.get('stages', []):
        boundary = stage['kpis'].get('energyBoundary')
        if not boundary:
            power = stage['kpis'].get('power') or {}
            boundary = energy_boundary(power, {}, [row['node'] for row in power.get('nodes', [])])
        lines.append('Energy interval: ' + stage['id'] + ' [' + str(boundary['from']) + ', ' + str(boundary['to']) + ')')
        lines.append('Compared set: ' + stage['id'] + ' ' + boundary['comparedSetStatus'])
        for variant in boundary['comparedSets']:
            lines.append('Compared set decisions: ' + json.dumps(variant, sort_keys=True))
        for row in boundary['rows']:
            lines.append(' | '.join(str(value) for value in (
                stage['id'], row['scope'], ','.join(node['name'] + '@' + node['uid'] for node in row['nodes']),
                'UNKNOWN' if row['joules'] is None else row['joules'],
                'UNKNOWN' if row['averageWatts'] is None else row['averageWatts'], row['status'], '; '.join(row['reasons']))))
    lines.append('Prediction consistency (v1alpha7): stage | row kind | prediction / measured / error / coverage')
    for stage in evaluation.get('stages', []):
        prediction = stage['kpis'].get('predictionAnalysis') or {}
        if not prediction:
            lines.append(stage['id'] + ' | UNKNOWN | v1alpha7 prediction analysis unavailable')
        for kind in ('nodes', 'actions', 'acceptance'):
            for row in prediction.get(kind, []):
                lines.append(stage['id'] + ' | ' + kind + ' | ' + json.dumps(row, sort_keys=True))
    lines.append('SLO denominator: the complete frozen planned population, not successful responses. Meter energy includes every declared node, whether serving or idle.')
    return '\n'.join(lines) + '\n'
