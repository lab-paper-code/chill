"""Capture complete live schemas for CRDs declared by this Helm release."""
import yaml


def capture_crds(raw_live_manifest, raw_live_hooks, namespace, kube, run):
    names = sorted({
        item['metadata']['name']
        for raw in (raw_live_manifest, raw_live_hooks)
        for item in yaml.safe_load_all(raw)
        if isinstance(item, dict) and item.get('kind') == 'CustomResourceDefinition'
    })
    report = {'namespace': namespace, 'names': names, 'status': 'none-declared', 'evidence': None}
    if names:
        path = run([*kube, 'get', 'crd', *names, '-o', 'yaml'],
                   name='crds.yaml', read_output=False)
        report.update(status='captured', evidence=str(path))
    return report
