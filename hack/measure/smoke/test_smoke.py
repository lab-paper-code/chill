import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


class SmokeCleanupTest(unittest.TestCase):
    def test_reset_failure_collects_read_only_evidence_and_propagates(self):
        script = Path(__file__).with_name('smoke.py')
        for status in ('failed', 'partial'):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                token = root / 'token'
                token.write_text('test-token')
                config = root / 'config.json'
                config.write_text(json.dumps({
                    'paths': {'repository': str(script.parents[3])},
                    'measure': {'serverURL': 'https://test', 'caFile': 'test-ca',
                                'tokenFile': str(token), 'kubectl': 'kubectl',
                                'kubeconfig': 'test-config', 'kubeContext': 'test',
                                'namespace': 'test'},
                }))
                out = root / 'out'
                calls = []
                states = iter(([], [{'id': 'instance', 'node': {'name': 'node'}}]))

                def run(args, **kwargs):
                    if args[0] == 'kubectl':
                        return SimpleNamespace(stdout=json.dumps({
                            'metadata': {'uid': 'uid'}, 'spec': {},
                        }))
                    if args[0] == 'python3':
                        Path(args[-1]).write_text('{"status": 200}\n')
                        return SimpleNamespace(stdout='')
                    self.assertEqual(args[0], 'curl')
                    method = args[args.index('-X') + 1]
                    url = args[args.index('-X') + 2]
                    body = json.loads(args[args.index('--data') + 1]) if '--data' in args else None
                    calls.append((method, url, body))
                    if body:
                        result = {'status': status if body['kind'] == 'reset' else 'complete'}
                    elif url.endswith('/scheduler/state'):
                        result = {'snapshot': {'instances': next(states)}}
                    else:
                        result = {'evidence': url}
                    return SimpleNamespace(stdout=json.dumps(result) + '\n200')

                with patch.dict(os.environ, {'CHILL_EXPERIMENT_CONFIG': str(config), 'N': '1'}), \
                     patch.object(sys, 'argv', [str(script), 'node', str(out), 'wl=profile:dataset']), \
                     patch.object(subprocess, 'run', side_effect=run):
                    with self.assertRaisesRegex(RuntimeError, 'reset failed'):
                        runpy.run_path(str(script), run_name='__main__')
                reset = next(i for i, (_, _, body) in enumerate(calls) if body and body['kind'] == 'reset')
                self.assertEqual([method for method, _, _ in calls[reset + 1:]], ['GET', 'GET'])
                for filename, endpoint in (
                    ('observations-wl.json', '/observations/workloads/uid?'),
                    ('scheduler-actions.json', '/observations/scheduler?'),
                ):
                    self.assertIn(endpoint, json.loads((out / filename).read_text())['evidence'])
