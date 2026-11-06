"""Bind the current-source read-only Go authority to every fresh inflight blocker."""
import json
from pathlib import Path


BUILD_SECONDS = 300
CHECK_SECONDS = 120
IDENTITY = ('namespace', 'name', 'uid', 'generation')


def identity(metadata):
    if (any(not isinstance(metadata.get(key), str) or not metadata[key]
            for key in ('namespace', 'name', 'uid', 'resourceVersion'))
            or type(metadata.get('generation')) is not int or metadata['generation'] < 1):
        raise RuntimeError('prejob authority requires complete captured object identities')
    return {key: metadata[key] for key in IDENTITY}


def check(source, evidence, kube, run, rendered, label, inflight_report, previous=None):
    source, evidence = Path(source), Path(evidence)
    if label not in ('prejob-preflight', 'prejob-pre-upgrade'):
        raise ValueError('unknown prejob proof stage')
    blocked = inflight_report['blockers']
    if not blocked:
        raise RuntimeError('prejobRepairOnly requires proved parent and child blockers')
    captured = Path(inflight_report['evidenceDirectory']) / 'objects.json'
    with captured.open(encoding='utf-8') as stream:
        objects = json.load(stream)['items']
    indexed = {}
    for obj in objects:
        meta = obj['metadata']
        key = (obj['kind'], meta.get('namespace', ''), meta['name'])
        if key in indexed:
            raise RuntimeError('prejob captured object identity is not unique')
        indexed[key] = obj
    originals = {}
    for blocker in blocked:
        key = (blocker['kind'], blocker['namespace'], blocker['name'])
        if key in originals or key not in indexed or key[0] not in ('BatchEnergyOptimization', 'ProfilingRun'):
            raise RuntimeError('prejobRepairOnly has an unproved or duplicate inflight blocker')
        obj = indexed[key]
        identity(obj['metadata'])
        originals[key] = obj
    parents = {key: obj for key, obj in originals.items() if key[0] == 'BatchEnergyOptimization'}
    if not parents:
        raise RuntimeError('prejobRepairOnly requires actual parent blockers')
    systems = [obj for obj in rendered if obj['kind'] == 'ChillSystem']
    if len(systems) != 1 or any(systems[0]['spec']['managementNamespace'] != key[1] for key in originals):
        raise RuntimeError('prejobRepairOnly must use the rendered system management namespace')
    pairs = []
    covered = set()
    for key, parent in sorted(parents.items()):
        meta = parent['metadata']
        children = []
        for child_key, child in originals.items():
            if child_key[0] != 'ProfilingRun' or child_key[1] != key[1]:
                continue
            owners = [ref for ref in child['metadata'].get('ownerReferences', [])
                      if ref.get('controller') is True]
            if (len(owners) == 1 and owners[0].get('apiVersion') == parent['apiVersion']
                    and owners[0].get('kind') == key[0] and owners[0].get('name') == key[2]
                    and owners[0].get('uid') == meta['uid']):
                children.append((child_key, child))
        if len(children) != 1:
            raise RuntimeError('prejob parent must bind exactly one captured owned child blocker')
        child_key, child = children[0]
        pairs.append((parent, child))
        covered.update((key, child_key))
    if covered != set(originals):
        raise RuntimeError('prejobRepairOnly cannot cover every inflight blocker with proved parent/child identities')
    cohort = [{'request': identity(parent['metadata']), 'child': identity(child['metadata'])}
              for parent, child in pairs]
    if previous is not None and cohort != previous['cohort']:
        raise RuntimeError('prejob frozen request/child cohort or generation changed before upgrade')
    binary = evidence / 'prejob-retirement-check'
    if label == 'prejob-preflight':
        run(['go', 'build', '-buildvcs=false', '-o', str(binary),
             './hack/prejob-retirement-check'], 'prejob-proof-build.log',
            timeout=BUILD_SECONDS, cwd=source)
    elif previous is None or not binary.is_file():
        raise RuntimeError('prejob final proof requires the source-compiled preflight authority')
    proofs = []
    for index, (parent, child) in enumerate(pairs):
        meta = parent['metadata']
        args = [str(binary), '--namespace', meta['namespace'],
                '--system-name', systems[0]['metadata']['name'],
                '--name', meta['name'], '--uid', meta['uid']]
        for flag in ('--kubeconfig', '--context'):
            args += [flag, kube[kube.index(flag) + 1]]
        proof = json.loads(run(args, label + '-%d.json' % index, timeout=CHECK_SECONDS))
        if (type(proof.get('version')) is not int or proof['version'] != 1
                or proof.get('eligible') is not True or proof.get('reason') != 'ExecutionPolicyInvalid'
                or identity(proof.get('request', {})) != identity(meta)
                or identity(proof.get('child', {})) != identity(child['metadata'])):
            raise RuntimeError('fresh prejob authority did not verify the captured request/child identities')
        proofs.append(proof)
    receipt = {'eligible': True, 'authority': 'current-source prejob-retirement-check',
               'evidenceDirectory': inflight_report['evidenceDirectory'],
               'cohort': cohort, 'proofs': proofs}
    (evidence / (label + '-receipt.json')).write_text(json.dumps(receipt, indent=2) + '\n')
    return receipt
