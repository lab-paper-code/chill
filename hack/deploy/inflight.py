"""Read-only cutover gate for retained CHILL measurement state (runbook §2.1)."""
from collections import Counter
import json
import os
from pathlib import Path
import tempfile


RESOURCES = ('profilingruns,batchenergyoptimizations,modelloadedidlebaselines,'
             'dispatchoccupancymeasurements,evaluationruns')
KINDS = {'ProfilingRun', 'BatchEnergyOptimization', 'ModelLoadedIdleBaseline',
         'DispatchOccupancyMeasurement', 'EvaluationRun', 'EvaluationStage'}
TERMINAL_FAILURES = {'InvalidRunSpec', 'ExecutionPolicyInvalid', 'ActuationPolicyInvalid',
                     'ExecutionLost', 'JobFailed', 'RequiredStateNotConfirmed', 'EvidenceInvalid'}


def blockers(objects, jobs):
    """Return exactly the runbook's state-loss blockers, not quality judgments."""
    blocked = []
    for obj in objects:
        kind, metadata = obj['kind'], obj['metadata']
        status = obj.get('status', {})
        conditions = {c['type']: c for c in status.get('conditions', [])}
        def true(name):
            return conditions.get(name, {}).get('status') == 'True'
        reasons = []
        if metadata.get('deletionTimestamp'):
            reasons.append('deletion/finalizer pending')
        if kind == 'ProfilingRun':
            frozen = bool(status.get('job', {}).get('uid') or status.get('evidence', {}).get('key'))
            terminal = true('EvidenceVerified') or any(
                c.get('status') == 'False' and c.get('type') in ('ExecutionComplete', 'EvidenceVerified')
                and c.get('reason') in TERMINAL_FAILURES for c in conditions.values())
            if not terminal and not frozen:
                reasons.append('unfrozen/no retained RunConfig identity')
            if not terminal:
                reasons.append('execution/evidence still in progress')
            if status.get('jobCleanup') == 'Pending':
                reasons.append('native Job cleanup pending')
        elif kind == 'BatchEnergyOptimization':
            if status.get('phase') not in ('Complete', 'Failed'):
                reasons.append('campaign nonterminal/unfrozen')
        elif kind == 'ModelLoadedIdleBaseline':
            if status.get('phase') not in ('Complete', 'Insufficient'):
                reasons.append('idle campaign nonterminal/unfrozen')
        elif kind == 'DispatchOccupancyMeasurement':
            if status.get('phase') not in ('Complete', 'Insufficient', 'Failed'):
                if not obj.get('spec', {}).get('conditions'):
                    reasons.append('occupancy inputs unfrozen')
                reasons.append('occupancy nonterminal')
        elif kind == 'EvaluationRun':
            if not (status.get('frozenConfigMapRef') or status.get('frozenConfig')):
                reasons.append('evaluation config unfrozen')
            if not status.get('finishedAt') or not true('Published') or true('PoolAcquired'):
                reasons.append('evaluation execution/publication/return incomplete')
        if reasons:
            blocked.append({'kind': kind, 'namespace': metadata.get('namespace', ''),
                            'name': metadata['name'], 'reasons': reasons})
    for job in jobs:
        metadata = job['metadata']
        if not any(ref.get('kind') in KINDS for ref in metadata.get('ownerReferences', [])):
            continue
        status = job.get('status', {})
        terminal = any(c.get('status') == 'True' and c.get('type') in ('Complete', 'Failed')
                       for c in status.get('conditions', []))
        if status.get('active', 0) or not terminal:
            blocked.append({'kind': 'Job', 'namespace': metadata.get('namespace', ''),
                            'name': metadata['name'], 'reasons': ['owned native Job still active/incomplete']})
    return blocked


def _save(path, data):
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data)


def collect(kube, run, out, label):
    """Retain complete read-only evidence, including all deployment blockers."""
    if not label or Path(label).name != label or label in ('.', '..'):
        raise ValueError('inflight evidence label must be a nonempty filename component')
    out = Path(out)
    out.mkdir(parents=True, exist_ok=True, mode=0o700)
    evidence = Path(tempfile.mkdtemp(prefix=label + '-', dir=str(out)))
    raw_objects = run(list(kube) + ['get', RESOURCES, '--all-namespaces', '-o', 'json'],
                      evidence.name + '-objects', read_output=False)
    os.link(raw_objects, evidence / 'objects.json')
    (evidence / 'objects.json').chmod(0o600)
    raw_jobs = run(list(kube) + ['get', 'jobs', '--all-namespaces', '-o', 'json'],
                   evidence.name + '-jobs', read_output=False)
    os.link(raw_jobs, evidence / 'jobs.json')
    (evidence / 'jobs.json').chmod(0o600)
    with (evidence / 'objects.json').open(encoding='utf-8') as stream:
        objects = json.load(stream)['items']
    with (evidence / 'jobs.json').open(encoding='utf-8') as stream:
        jobs = json.load(stream)['items']
    blocked = blockers(objects, jobs)
    _save(evidence / 'blockers.json', (json.dumps(blocked, ensure_ascii=False, indent=2) + '\n').encode())
    return {'checked': dict(Counter(obj['kind'] for obj in objects)),
            'blockers': blocked, 'evidenceDirectory': str(evidence)}


def require_clear(report):
    if report['blockers']:
        raise RuntimeError('ADR 0019: in-flight measurement blockers prevent deployment; evidence: ' + report['evidenceDirectory'])


def check(kube, run, out, label):
    """Stop state-changing cutovers when retained measurements remain in flight."""
    report = collect(kube, run, out, label)
    require_clear(report)
    return report
