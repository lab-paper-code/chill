"""Run the source startup decoder against the actual selected deployment render."""
import json
from pathlib import Path

import yaml


def _flag(arguments, name, default=''):
    for index, value in enumerate(arguments):
        if value.startswith(name + '='):
            return value[len(name) + 1:]
        if value == name and index + 1 < len(arguments):
            return arguments[index + 1]
    return default


def validate(source, evidence, manifest, kube, run, scheduler_file=None, timeout=300):
    source, evidence, manifest = Path(source), Path(evidence), Path(manifest)
    documents = [x for x in yaml.safe_load_all(manifest.read_text()) if isinstance(x, dict)]
    servers = [(item, container) for item in documents if item.get('kind') == 'Deployment'
               for container in item.get('spec', {}).get('template', {}).get('spec', {}).get('containers', [])
               if container['name'] == 'serving-entrypoint']
    if not servers:
        return {'checked': False, 'reason': 'render has no ChillServer Deployment'}
    if len(servers) != 1:
        raise ValueError('deployment render must contain one ChillServer')
    deployment, container = servers[0]
    args = container.get('args', [])
    name = _flag(args, '--serving-scheduler-configmap')
    namespace = _flag(args, '--serving-namespace', deployment['metadata']['namespace'])
    if scheduler_file is None and name and not any(x.get('kind') == 'ConfigMap' and x['metadata']['name'] == name and x['metadata'].get('namespace', namespace) == namespace for x in documents):
        raw = run([*kube, '-n', namespace, 'get', 'configmap', name, '-o', 'json'], 'scheduler-configmap.json')
        scheduler_file = evidence / 'selected-scheduler.json'
        scheduler_file.write_bytes(raw)
    binary = evidence / 'chill-server-validator'
    if not binary.exists():
        run(['go', 'build', '-buildvcs=false', '-o', str(binary), './cmd/chill-server'], 'validator-build.log', timeout=timeout, cwd=source)
    argv = [str(binary), '--check-deployment-config=' + str(manifest)]
    if scheduler_file is not None:
        argv.append('--scheduler-configmap-file=' + str(scheduler_file))
    run(argv, 'validator.log', timeout=60)
    receipt = {'checked': True, 'manifest': str(manifest), 'schedulerConfigMap': name,
               'namespace': namespace, 'schedulerInput': str(scheduler_file) if scheduler_file else 'render'}
    (evidence / 'configuration-check.json').write_text(json.dumps(receipt, indent=2) + '\n')
    return receipt
