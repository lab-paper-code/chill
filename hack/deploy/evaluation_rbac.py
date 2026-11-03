"""Check rendered evaluation SA grants against live authorization; never grant RBAC."""
import json
from pathlib import Path
import re

import yaml


def plan(raw, namespace):
    documents, accounts = [], set()
    for chunk in re.split(r'^---[ \t]*$', raw, flags=re.MULTILINE):
        item = yaml.safe_load(chunk)
        if not isinstance(item, dict):
            continue
        documents.append(item)
        kind, meta = item.get('kind'), item.get('metadata', {})
        evaluation_source = bool(re.search(r'^# Source: .*[/]evaluation[^/\n]*\.yaml$', chunk, re.MULTILINE))
        evaluation_name = 'evaluation' in meta.get('name', '')
        if kind == 'ServiceAccount' and (evaluation_source or evaluation_name):
            accounts.add((meta.get('namespace', namespace), meta['name']))
        if kind in ('RoleBinding', 'ClusterRoleBinding') and (evaluation_source or evaluation_name):
            accounts.update((s.get('namespace', meta.get('namespace', namespace)), s['name'])
                            for s in item.get('subjects', []) if s.get('kind') == 'ServiceAccount')
    roles = {(x['kind'], x['metadata'].get('namespace', namespace) if x['kind'] == 'Role' else '',
              x['metadata']['name']): x for x in documents if x.get('kind') in ('Role', 'ClusterRole')}
    checks = {}
    for binding in documents:
        if binding.get('kind') not in ('RoleBinding', 'ClusterRoleBinding'):
            continue
        meta = binding['metadata']
        scope = meta.get('namespace', namespace) if binding['kind'] == 'RoleBinding' else ''
        subjects = {(s.get('namespace', meta.get('namespace', namespace)), s['name'])
                    for s in binding.get('subjects', []) if s.get('kind') == 'ServiceAccount'} & accounts
        if not subjects:
            continue
        ref = binding['roleRef']
        role = roles.get((ref['kind'], scope if ref['kind'] == 'Role' else '', ref['name']))
        if role is None:
            raise ValueError('Evaluation binding references a role absent from render: ' + meta['name'])
        for sa_namespace, sa in sorted(subjects):
            for rule in role.get('rules', []):
                targets = [(url, '') for url in rule.get('nonResourceURLs', [])]
                for group in rule.get('apiGroups', []):
                    for resource in rule.get('resources', []):
                        base, separator, subresource = resource.partition('/')
                        target = base + ('.' + group if group else '')
                        targets.extend((target + ('/' + name if name else ''), subresource)
                                       for name in (rule.get('resourceNames') or ['']))
                for verb in rule.get('verbs', []):
                    for target, subresource in targets:
                        key = (sa_namespace, sa, scope, verb, target, subresource)
                        if key not in checks:
                            checks[key] = {'serviceAccount': sa, 'serviceAccountNamespace': sa_namespace,
                                           'namespace': scope, 'verb': verb, 'target': target,
                                           'subresource': subresource, 'bindings': [], 'status': 'pending'}
                        checks[key]['bindings'].append(meta['name'])
    return {'passed': False, 'serviceAccounts': [{'namespace': ns, 'name': name}
                                                for ns, name in sorted(accounts)],
            'checks': [checks[key] for key in sorted(checks)]}


def check(raw, namespace, kube, run, out):
    out = Path(out)
    receipt = plan(raw, namespace)
    def save():
        out.write_text(json.dumps(receipt, indent=2) + '\n')
    save()
    try:
        for index, item in enumerate(receipt['checks']):
            sa_namespace = item['serviceAccountNamespace']
            argv = list(kube) + ['auth', 'can-i', item['verb'], item['target'],
                                '--as=system:serviceaccount:' + sa_namespace + ':' + item['serviceAccount'],
                                '--as-group=system:serviceaccounts',
                                '--as-group=system:serviceaccounts:' + sa_namespace,
                                '--as-group=system:authenticated']
            argv += ['--namespace', item['namespace']] if item['namespace'] else ['--all-namespaces']
            if item['subresource']:
                argv += ['--subresource', item['subresource']]
            try:
                result = run(argv, 'evaluation-rbac-%04d.log' % index, timeout=30)
                if result.strip() != b'yes':
                    raise ValueError('Authorization response was not yes')
            except Exception as error:
                item.update(status='failed', error=str(error))
                raise ValueError('Evaluation SA authorization failed: ' + json.dumps(item)) from error
            item['status'] = 'allowed'
        receipt['passed'] = True
        return receipt
    finally:
        save()
