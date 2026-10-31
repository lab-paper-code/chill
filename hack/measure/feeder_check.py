"""Offline regression: python3 hack/measure/feeder_check.py (no cluster/config needed)."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
from unittest.mock import patch


def check():
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        config = root / 'config.json'
        config.write_text(json.dumps({'measure': {'statusLog': str(root / 'status.log')},
                                      'paths': {'campaign': directory}}))
        spec = importlib.util.spec_from_file_location('feeder', Path(__file__).with_name('feeder.py'))
        feeder = importlib.util.module_from_spec(spec)
        with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(config)):
            spec.loader.exec_module(feeder)
        documents = [{'kind': 'BatchEnergyOptimization', 'metadata': {'name': name},
                      'spec': {'state': {'targetNode': 'node'}}}
                     for name in ('bad', 'next', 'existing')]
        source = root / 'documents.json'
        source.write_text('---\n'.join(map(json.dumps, documents)))
        manifest = root / 'plan.json'
        for schema in ('coverage-feeder-plan.v1', 'approved-resume.v1'):
            manifest.write_text(json.dumps({'schemaVersion': schema,
                                           'sequence': [{'stage': 1, 'paths': [str(source)]}]}))
            (root / 'feeder.log').write_text('')
            states = {'bad': ('Failed', 'metric failure', ''), 'existing': ('Complete', '', '')}
            created = []
            released = iter((False, True, True, True))

            def create(*args, inp=None):
                assert args == ('create', '-f', '-')
                name = json.loads(inp)['metadata']['name']
                created.append(name)
                states[name] = ('Complete', '', '')
                return SimpleNamespace(returncode=0, stdout='', stderr='')

            with patch.object(feeder, 'state', side_effect=lambda kind, name: states.get(name)), \
                 patch.object(feeder, 'k', side_effect=create), \
                 patch.object(feeder, 'node_released', side_effect=lambda node: next(released, True)), \
                 patch.object(feeder.time, 'sleep'), \
                 patch('sys.argv', ['feeder.py', '--manifest', str(manifest), '--stages', '1']), \
                 contextlib.redirect_stdout(io.StringIO()) as output:
                try:
                    feeder.main()
                except SystemExit as error:
                    assert schema != 'coverage-feeder-plan.v1'
                    assert 'campaign stopped after terminal failure' in str(error)
                else:
                    assert schema == 'coverage-feeder-plan.v1'
                    assert 'success=1 failed=1 skipped=1' in output.getvalue()
                    feeder.log.close()
                    feeder.main()
                    assert 'success=0 failed=1 skipped=2' in output.getvalue()
                finally:
                    feeder.log.close()
            if schema == 'coverage-feeder-plan.v1':
                assert created == ['next']
                assert 'FAILED node BatchEnergyOptimization bad' in (root / 'status.log').read_text()
                assert 'TERMINAL node bad Failed' in (root / 'feeder.log').read_text()
                assert (root / 'feeder-failed.txt').read_text().count(' bad ') == 1
            else:
                assert created == []
    print('feeder offline checks passed')


if __name__ == '__main__':
    check()
