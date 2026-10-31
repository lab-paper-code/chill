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
        (root / 'feeder-hold.json').write_text(json.dumps({
            'pauseNodes': ['unrelated-node'], 'drop': '^never-create-this$'}))
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
                    assert '\"pauseNodes\": [\"unrelated-node\"]' in output.getvalue()
                    assert '\"drop\": \"^never-create-this$\"' in (root / 'status.log').read_text()
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
        for limit, reset in ((2, False), (3, False), (2, True)):
            feeder.ENV['maxConsecutiveFailures'] = limit
            names = ['bad1', 'bridge', 'bad2', 'bad3', 'blocked'] if reset else ['bad1', 'bad2', 'bad3', 'blocked']
            queues = {'node': [{'kind': 'BatchEnergyOptimization', 'metadata': {'name': name}}
                               for name in names],
                      'other': [{'kind': 'BatchEnergyOptimization', 'metadata': {'name': 'healthy'}}]}
            states = {'bad1': ('Failed', 'failure', ''), 'bad2': ('Insufficient', '', ''),
                      'bad3': ('', '', 'Refused admission'), 'bridge': ('Complete', '', '')}
            created = []
            (root / 'feeder.log').write_text('')
            with open(root / 'feeder.log', 'a') as feeder.log, \
                 patch.object(feeder, 'state', side_effect=lambda kind, name: states.get(name)), \
                 patch.object(feeder, 'k', side_effect=create), \
                 patch.object(feeder, 'node_released', return_value=True), \
                 patch.object(feeder.time, 'sleep'), \
                 contextlib.redirect_stdout(io.StringIO()) as output:
                counts = feeder.run(queues, {}, continue_on_failure=True)
            assert created == ['healthy']  # stopped lane never creates its next document
            assert counts == {'success': 1, 'failed': 3 if reset else limit, 'skipped': 2 if reset else 4 - limit}
            message = f'LANE STOPPED node after {limit} consecutive failures'
            assert message in output.getvalue() and message in (root / 'status.log').read_text()
            assert message in (root / 'feeder.log').read_text()
            assert 'LANE DONE other' in (root / 'feeder.log').read_text()
    print('feeder offline checks passed')


if __name__ == '__main__':
    check()
