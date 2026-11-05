"""Read-only authority checks for the reviewed old-contract retirement repair."""
import json
from pathlib import Path

import yaml


BUILD_SECONDS = 300
CHECK_SECONDS = 120


def check(source, evidence, kube, run, config, label, inflight_report):
    source, evidence = Path(source), Path(evidence)
    if label not in ('retirement-preflight', 'retirement-pre-upgrade'):
        raise ValueError('unknown retirement proof stage')
    blocked = inflight_report['blockers']
    if (len(blocked) != 1 or blocked[0]['kind'] != 'BatchEnergyOptimization'
            or blocked[0]['reasons'] != ['campaign nonterminal/unfrozen']):
        raise RuntimeError('retirement repair requires one pre-execution campaign blocker only')
    blocker = blocked[0]
    captured = Path(inflight_report['evidenceDirectory']) / 'objects.json'
    with captured.open(encoding='utf-8') as stream:
        objects = json.load(stream)['items']
    candidates = [obj for obj in objects if obj['kind'] == blocker['kind']
                  and obj['metadata'].get('namespace') == blocker['namespace']
                  and obj['metadata']['name'] == blocker['name']]
    if len(candidates) != 1:
        raise RuntimeError('retirement blocker identity is not unique in captured objects')
    original = candidates[0]
    metadata = original['metadata']
    with (evidence / 'render-before.yaml').open(encoding='utf-8') as stream:
        systems = [obj for obj in yaml.safe_load_all(stream)
                   if isinstance(obj, dict) and obj.get('kind') == 'ChillSystem']
    if (len(systems) != 1
            or systems[0]['spec']['managementNamespace'] != metadata['namespace']):
        raise RuntimeError('retirement repair must use the rendered system management namespace')
    binary = evidence / 'pretrial-retirement-check'
    if not binary.exists():
        run(['go', 'build', '-buildvcs=false', '-o', str(binary),
             './hack/pretrial-retirement-check'], 'retirement-proof-build.log',
            timeout=BUILD_SECONDS, cwd=source)
    args = [str(binary), '--namespace', metadata['namespace'],
            '--system-name', systems[0]['metadata']['name'],
            '--name', metadata['name'], '--uid', metadata['uid']]
    for flag in ('--kubeconfig', '--context'):
        args += [flag, kube[kube.index(flag) + 1]]
    proof = json.loads(run(args, label + '.json', timeout=CHECK_SECONDS))
    request = proof.get('request', {})
    if (proof.get('eligible') is not True
            or any(request.get(key) != metadata[key]
                   for key in ('namespace', 'name', 'uid', 'generation'))):
        raise RuntimeError('fresh retirement authority did not verify the captured request identity')
    receipt = {'eligible': True, 'authority': 'reviewed source pretrial-retirement-check',
               'evidenceDirectory': inflight_report['evidenceDirectory'], 'proof': proof}
    (evidence / (label + '-receipt.json')).write_text(json.dumps(receipt, indent=2) + '\n')
    return receipt
