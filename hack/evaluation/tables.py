"""Render black-box judgments; never infer or reinterpret historical artifacts."""
import json
from pathlib import Path


def sections(path):
    if not Path(path).exists():
        return None
    value = json.loads(Path(path).read_text())
    if value.get('schema') != 'e5-judgment.v2':
        raise ValueError('FrozenInputInvalid: judgment v2 required')
    return value


def publication_requests(judgment):
    fields = ('offered', 'clientRows', 'unfinishedOrUnsent', 'outcomes', 'completedLatencyNS',
              'gatewayCompletedLatencyNS', 'latencyScope', 'scheduledLatencyNS', 'actualLatencyNS',
              'deadlineSuccess', 'deadlineSuccessFraction', 'requiredSuccessfulRank', 'D1', 'slo', 'workload',
              'workloadUID', 'definitionID', 'actualGatewayIdentities', 'substages', 'identityMissingReason')
    result = {}
    for name, value in judgment['requests'].items():
        row = {key: value[key] for key in fields if key in value}
        quality = value['issuanceFidelity']
        row['issuanceFidelity'] = {key: quality[key] for key in ('offered', 'issued', 'supplied', 'issuanceFraction', 'generatorQuality')}
        result[name] = row
    return result


def render(root, repository=None, *, evaluation=None):
    if evaluation is None:
        evaluation = json.loads((Path(root) / 'e5-summary.json').read_text())
    lines = ['Evaluation: ' + evaluation['status'], 'stage | workload | offered | completed | deadline success | SLO | declared-meter J']
    for stage in evaluation.get('stages', []):
        kpis = stage['kpis']
        energy = kpis.get('wholePoolJoules')
        for name, result in kpis['requests'].items():
            lines.append(' | '.join(str(value) for value in (stage['id'], name, result['offered'], result['completedLatencyNS']['samples'],
                                                           result['deadlineSuccess'], result['D1'], 'UNKNOWN' if energy is None else energy)))
    lines.append('SLO denominator: the complete frozen planned population, not successful responses. Meter energy includes every declared node, whether serving or idle.')
    return '\n'.join(lines) + '\n'
