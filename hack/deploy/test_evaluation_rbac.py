"""Behavioral checks for rendered RBAC grant expansion."""
import json
import unittest

import evaluation_rbac


def documents():
    def obj(kind, name, **fields):
        return dict(apiVersion='v1' if kind == 'ServiceAccount' else 'rbac.authorization.k8s.io/v1',
                    kind=kind, metadata={'name': name, 'namespace': 'review'} if kind in ('ServiceAccount', 'Role', 'RoleBinding') else {'name': name}, **fields)
    def binding(kind, name, role_kind, role, account):
        return obj(kind, name, roleRef={'kind': role_kind, 'name': role},
                   subjects=[{'kind': 'ServiceAccount', 'namespace': 'review', 'name': account}])
    return [obj('ServiceAccount', 'custom-action'), obj('ServiceAccount', 'custom-publish'),
            obj('ClusterRole', 'collector', rules=[{'nonResourceURLs': ['/chill/api/v1/workloads', '/chill/api/v1/scheduler'], 'verbs': ['get']}]),
            obj('Role', 'named-config', rules=[{'apiGroups': [''], 'resources': ['configmaps'], 'resourceNames': ['scheduler'], 'verbs': ['get', 'patch']},
                                            {'apiGroups': [''], 'resources': ['pods/log'], 'resourceNames': [], 'verbs': ['get']}]),
            binding('RoleBinding', 'evaluation-config', 'Role', 'named-config', 'custom-action'),
            binding('ClusterRoleBinding', 'evaluation-collector', 'ClusterRole', 'collector', 'custom-action'),
            # Same-name documents must both retain their intended grants, not last-wins.
            binding('ClusterRoleBinding', 'evaluation-collector', 'ClusterRole', 'collector', 'evaluation-evidence-collector')]


def render(items):
    return '\n'.join('---\n# Source: chill/templates/evaluation_rbac.yaml\n' + json.dumps(x) for x in items)


class EvaluationRBAC(unittest.TestCase):
    def test_namespaced_named_subresource_and_cluster_url_grants(self):
        result = evaluation_rbac.plan(render(documents()), 'review')
        grants = {(x['serviceAccount'], x['namespace'], x['verb'], x['target'], x['subresource']) for x in result['checks']}
        self.assertEqual(grants, {
            ('custom-action', 'review', 'get', 'configmaps/scheduler', ''),
            ('custom-action', 'review', 'patch', 'configmaps/scheduler', ''),
            ('custom-action', 'review', 'get', 'pods', 'log'),
            *{(sa, '', 'get', url, '') for sa in ('custom-action', 'evaluation-evidence-collector')
              for url in ('/chill/api/v1/workloads', '/chill/api/v1/scheduler')},
        })
        self.assertIn({'namespace': 'review', 'name': 'custom-publish'}, result['serviceAccounts'])

    def test_disabled_and_unresolvable_grants(self):
        self.assertEqual(evaluation_rbac.plan('', 'review')['serviceAccounts'], [])
        items = documents()
        items = [x for x in items if x['kind'] != 'ClusterRole']
        with self.assertRaisesRegex(ValueError, 'absent from render'):
            evaluation_rbac.plan(render(items), 'review')
