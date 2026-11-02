"""Offline tests of the actual round deployment/receipt boundary."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('evaluation_round', HERE/'round.py')
round_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(round_module)


class RoundDeployTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.digest = 'sha256:'+'b'*64
        self.publication = {'sourceSHA': 'a'*40, 'runtimeBuilds': 0, 'roles': {'server': {
            'image': 'registry.invalid/chill/chill-serving-entrypoint:published@'+self.digest,
            'digest': self.digest, 'dockerfile': 'build/docker/chill-server.Dockerfile',
            'buildArgs': {}, 'platforms': ['linux/amd64'], 'platformDigests': [self.digest],
            'platformReceipts': [{'platform': 'linux/amd64', 'digest': self.digest,
                                  'configDigest': 'sha256:'+'c'*64, 'revision': 'a'*40, 'variant': ''}]}}}
        self.publication_path = self.root/'publication.json'
        self.write_publication()
        self.runner = object.__new__(round_module.Round)
        self.runner.c = {'sourceSHA': 'a'*40, 'environment': '/ops/environment.json',
                         'minimumFreeBytes': 310_000_000_000, 'publication': str(self.publication_path),
                         'deployment': {'context': 'fixed-context', 'kubeconfig': '/ops/kubeconfig',
                                        'release': 'chill', 'registry': 'registry.invalid/chill', 'expectedRevision': 265}}
        self.runner.repo = HERE.parents[1]
        self.runner.attempt = self.root
        self.runner.env = {'d2': {'namespace': 'fixed-namespace', 'kubectl': ['kubectl', '--kubeconfig', '/ops/kubeconfig', '--context', 'fixed-context']}}
        self.runner.state = {}
        self.templates = {'server': {'spec': {'containers': [{'name': 'server', 'image': 'old'}]}}}
        self.runner.templates = lambda name: copy.deepcopy(self.templates)
        self.runner.server_status = lambda name: {'pod/server': 0}
        self.runner.controller = lambda: SimpleNamespace(state=lambda: {'snapshot': {}})
        self.runner.cluster = lambda *args: {'items': [{'metadata': {'uid': 'fixture-pod'}, 'spec': {'containers': [{'name': 'server', 'image': 'chill-serving-entrypoint:old'}]}, 'status': {'containerStatuses': [{'name': 'server', 'imageID': 'old'}]}}]}
        self.calls = []
        self.runner.command = self.command

    def write_publication(self):
        self.publication_path.write_text(json.dumps(self.publication))

    def command(self, argv, name, **kwargs):
        self.calls.append(argv)
        evidence = Path(argv[argv.index('--evidence')+1])
        self.assertFalse(evidence.exists(), 'execute must own a fresh evidence directory')
        evidence.mkdir()
        (evidence/'state.json').write_text(json.dumps({'ready': True}))
        output = self.root/'deploy.stdout'
        output.write_text('READY: '+str(evidence)+'\n')
        return output

    def test_execute_pins_recipe_and_prebuilt_without_approval(self):
        self.runner.deploy()
        self.assertEqual(len(self.calls), 1)
        argv = self.calls[0]
        self.assertEqual(argv[1], 'run')
        self.assertIn('--execute', argv)
        self.assertNotIn('--chart-diff-approved', argv)
        self.assertEqual(argv[argv.index('--source')+1], 'a'*40)
        recipe = round_module.load(argv[2])
        self.assertEqual({key: recipe[key] for key in self.runner.c['deployment']}, self.runner.c['deployment'])
        self.assertEqual(recipe['namespace'], 'fixed-namespace')
        self.assertEqual(recipe['apiEnvironment'], self.runner.c['environment'])
        self.assertEqual(recipe['images'], [{'role': 'server', 'prebuilt': str(self.publication_path)}])
        self.assertEqual(recipe['minFreeBytes'], 310_000_000_000)
        self.assertEqual(self.runner.state['deploymentEvidence'], str(self.root/'deployment'))

    def test_same_digest_skips_execute_but_checks_receipt_and_readiness(self):
        self.runner.cluster = lambda *args: {'items': [{'metadata': {'uid': 'fixture-pod'}, 'spec': {'containers': [{'name': 'server', 'image': 'chill-serving-entrypoint:published'}]}, 'status': {'containerStatuses': [{'name': 'server', 'imageID': 'repository@'+self.digest}]}}]}
        self.runner.deploy()
        self.assertEqual(self.calls, [])
        self.assertTrue(round_module.load(self.root/'readiness.json')['sameDigestSkipped'])
        del self.publication['roles']['server']['dockerfile']
        self.write_publication()
        with self.assertRaisesRegex(ValueError, 'provenance'):
            self.runner.deploy()

    def test_chart_drift_failure_does_not_record_success(self):
        def rejected(*args, **kwargs):
            raise RuntimeError('chart drift requires exact approval')
        self.runner.command = rejected
        with self.assertRaisesRegex(RuntimeError, 'chart drift'):
            self.runner.deploy()
        self.assertNotIn('deploymentEvidence', self.runner.state)
        self.assertFalse((self.root/'readiness.json').exists())

    def test_non_image_template_drift_is_rejected(self):
        after = copy.deepcopy(self.templates)
        after['server']['spec']['containers'][0]['args'] = ['changed']
        self.runner.templates = lambda name: copy.deepcopy(after if name.endswith('after') else self.templates)
        with self.assertRaisesRegex(ValueError, 'Non-server deployment template changed'):
            self.runner.deploy()

    def test_old_minimal_receipt_is_rejected_before_cluster_access(self):
        entry = self.publication['roles']['server']
        self.publication['roles']['server'] = {key: entry[key] for key in ('image', 'digest', 'platformDigests')}
        self.write_publication()
        self.runner.cluster = lambda *args: self.fail('invalid receipt reached cluster access')
        with self.assertRaisesRegex(ValueError, 'provenance'):
            self.runner.deploy()

    def test_missing_platform_receipts_rejected_on_matching_digest(self):
        self.runner.cluster = lambda *args: self.fail('incomplete receipt reached cluster access')
        del self.publication['roles']['server']['platformReceipts']
        self.write_publication()
        with self.assertRaises(ValueError):
            self.runner.deploy()

    def test_harness_target_mismatch_rejected_before_deployment(self):
        self.runner.cluster = lambda *args: self.fail('target mismatch reached cluster access')
        for argv in (['kubectl', '--kubeconfig', '/ops/kubeconfig', '--context', 'other-context'],
                     ['kubectl', '--kubeconfig', '/ops/other-kubeconfig', '--context', 'fixed-context'],
                     ['kubectl'],
                     ['kubectl', '--kubeconfig', '/ops/kubeconfig', '--context', 'fixed-context', '--server=https://other.invalid']):
            with self.subTest(argv=argv):
                self.runner.env['d2']['kubectl'] = argv
                with self.assertRaisesRegex(ValueError, 'environment d2.kubectl'):
                    self.runner.deploy()
        self.assertEqual(self.calls, [])
        self.assertFalse((self.root/'deployment-recipe.json').exists())

    def test_equals_form_harness_target_matches_recipe(self):
        self.runner.env['d2']['kubectl'] = ['kubectl', '--kubeconfig=/ops/kubeconfig', '--context=fixed-context']
        self.assertEqual(self.runner.deployment_recipe()['context'], 'fixed-context')


if __name__ == '__main__':
    unittest.main()
