#!/usr/bin/env python3
"""Offline harness result regression: no cluster or store access."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import urllib.error
from unittest.mock import patch

import input_snapshot

spec = importlib.util.spec_from_file_location('evaluation', Path(__file__).with_name('evaluation.py'))
evaluation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evaluation)


class ConditionReasonTest(unittest.TestCase):
    def test_http_error_has_token_and_original_message(self):
        with tempfile.TemporaryDirectory() as directory:
            harness = object.__new__(evaluation.Harness)
            harness.run = harness.root = Path(directory)
            harness.spec = {}
            harness.execution = {'runUID': 'run'}
            harness.scope = 'run'
            harness.prefix = 'experiments/run/run'
            harness.upload = lambda path, key: {'store': 'evidence', 'key': key, 'size': path.stat().st_size}
            error = urllib.error.HTTPError('https://chill', 403, 'Forbidden', {}, None)
            with patch.object(input_snapshot, 'prepare', side_effect=error):
                self.assertEqual(harness.execute('prepare'), 1)
            result = json.loads((harness.run / 'results/run/prepare.json').read_text())
            self.assertEqual(result['execution'], {'status': 'Failed', 'reason': 'ProcedureFailed', 'message': 'HTTP Error 403: Forbidden'})


if __name__ == '__main__':
    unittest.main()
