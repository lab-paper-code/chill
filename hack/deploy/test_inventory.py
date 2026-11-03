"""Behavioral checks for release-owned full-schema CRD capture."""
from pathlib import Path
import unittest
from unittest.mock import Mock

import yaml

import inventory


def render(*items):
    return yaml.safe_dump_all(items).encode()


def crd(name, **metadata):
    return {'apiVersion': 'apiextensions.k8s.io/v1', 'kind': 'CustomResourceDefinition',
            'metadata': {'name': name, **metadata}}


class Inventory(unittest.TestCase):
    def setUp(self):
        self.kube = ['kubectl', '--kubeconfig', '/private/config', '-n', 'release']
        self.evidence = Path('/evidence/003-crds.yaml')
        self.run = Mock(return_value=self.evidence)

    def test_mixed_manifest_and_hooks_select_only_declared_crds(self):
        manifest = render(
            None, 'ignored YAML scalar',
            {'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': 'worker'}},
            crd('zebras.example.org'),
            {'apiVersion': 'example.org/v1', 'kind': 'Widget', 'metadata': {'name': 'instance'}})
        hooks = render(
            {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': 'migrate'}},
            crd('alphas.example.org', namespace='not-the-release',
                annotations={'helm.sh/hook': 'pre-upgrade'}))

        report = inventory.capture_crds(manifest, hooks, 'release', self.kube, self.run)

        self.run.assert_called_once_with(
            [*self.kube, 'get', 'crd', 'alphas.example.org', 'zebras.example.org', '-o', 'yaml'],
            name='crds.yaml', read_output=False)
        self.assertEqual(report, {'namespace': 'release',
                                  'names': ['alphas.example.org', 'zebras.example.org'],
                                  'status': 'captured', 'evidence': str(self.evidence)})

    def test_duplicate_owned_name_is_fetched_once(self):
        item = crd('widgets.example.org')
        report = inventory.capture_crds(render(item, item), render(item),
                                        'release', self.kube, self.run)
        self.assertEqual(report['names'], ['widgets.example.org'])
        self.run.assert_called_once_with(
            [*self.kube, 'get', 'crd', 'widgets.example.org', '-o', 'yaml'],
            name='crds.yaml', read_output=False)

    def test_no_declared_crds_skips_command(self):
        for manifest, hooks in ((b'', b''), (render({'kind': 'ConfigMap'}), render(None))):
            with self.subTest(manifest=manifest):
                report = inventory.capture_crds(manifest, hooks, 'release', self.kube, self.run)
                self.assertEqual(report, {'namespace': 'release', 'names': [],
                                          'status': 'none-declared', 'evidence': None})
                self.run.assert_not_called()

    def test_capture_command_errors_propagate(self):
        self.run.side_effect = RuntimeError('command failed')
        with self.assertRaisesRegex(RuntimeError, 'command failed'):
            inventory.capture_crds(render(crd('widgets.example.org')), b'',
                                   'release', self.kube, self.run)
