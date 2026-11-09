"""Render black-box judgments; never infer or reinterpret historical artifacts."""
import json
from pathlib import Path


def sections(path):
    if not Path(path).exists():
        return None
    value = json.loads(Path(path).read_text())
    if value.get('schema') != 'e5-judgment.v3':
        raise ValueError('FrozenInputInvalid: judgment v3 required')
    return value


def publication_requests(judgment):
    fields = ('offered', 'clientRows', 'unfinishedOrUnsent', 'collectionLoss', 'outcomes', 'completedLatencyNS',
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
        if evaluation.get('schema') != 'e5-summary.v4':
            raise ValueError('FrozenInputInvalid: summary v4 required')
    lines = ['Evaluation: ' + evaluation['status'], 'stage | workload | offered | completed | deadline success | SLO | declared-meter J']
    for stage in evaluation.get('stages', []):
        kpis = stage['kpis']
        energy = kpis.get('wholePoolJoules')
        for name, result in kpis['requests'].items():
            lines.append(' | '.join(str(value) for value in (stage['id'], name, result['offered'], result['completedLatencyNS']['samples'],
                                                           result['deadlineSuccess'], result['D1'], 'UNKNOWN' if energy is None else energy)))
            for gap in result.get('observationGaps', []):
                lines.append('Observation gap: ' + json.dumps(gap, sort_keys=True))
    lines.append('SLO denominator: the complete frozen planned population, not successful responses. Meter energy includes every declared node, whether serving or idle.')
    return '\n'.join(lines) + '\n'
