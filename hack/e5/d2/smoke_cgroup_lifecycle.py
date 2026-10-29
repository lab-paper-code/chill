#!/usr/bin/env python3
"""One offline collector regression: fresh Pod ID, missing PID, restart, coverage."""
import ast
import contextlib
import datetime as dt
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def main():
    sample = module('sampler', ROOT / 'sample_cgroups.py')
    analysis = module('analysis', ROOT / 'analyze_before.py')
    tree = ast.parse((ROOT.parent / 'e5.py').read_text())
    cpo = next(n for n in tree.body if isinstance(n, ast.ClassDef) and n.name == 'CPO')
    method = next(n for n in cpo.body if isinstance(n, ast.FunctionDef) and n.name == 'sampler')
    scope = dict(module=lambda *args: sample, D2=ROOT, load=lambda _: [{'node': 'fixture', 'local': True}],
                 sys=sys, subprocess=subprocess, json=json)
    exec(compile(ast.Module(body=[method], type_ignores=[]), 'CPO.sampler', 'exec'), scope)
    pod = {'metadata': {'namespace': 'chill-system', 'name': 'native', 'uid': 'uid'},
           'spec': {'nodeName': 'fixture'}, 'status': {'containerStatuses': [
               {'name': 'firstfloor', 'containerID': 'containerd://current', 'restartCount': 1}]}}
    queries = []
    def get_pods(argv, **kwargs):
        queries.append(argv)
        return {'items': [pod]}
    class Sink(io.StringIO):
        def close(self):
            pass
    source = Sink()
    collector = SimpleNamespace(c={'collection': {'hosts': 'fixture'}}, tb={'apiSeconds': 1},
                                a=SimpleNamespace(stage='E5-2'), io=SimpleNamespace(k=get_pods))
    with tempfile.TemporaryDirectory() as directory:
        folder = Path(directory)
        (folder / 'pre-pods.json').write_text(json.dumps({'containerID': 'stale'}))
        with patch.object(subprocess, 'Popen', return_value=SimpleNamespace(stdin=source)):
            children = scope['sampler'](collector, folder, 5)
        for _, out, error in children:
            out.close(); error.close()
        assert queries == [['get', 'pods', '-A', '-o', 'json']]
        clock = [0]
        inspected = []
        def run(argv, **kwargs):
            if 'inspect' in argv:
                cid = argv[-1]; inspected.append(cid)
                pid = {'current': None, 'second': 202, 'third': 303}[cid]
                value = {'info': {'pid': pid}}
            else:
                cid = 'second' if clock[0] == 1 else 'third'
                value = {'containers': [{'id': cid, 'metadata': {'name': 'firstfloor'},
                         'labels': {'io.kubernetes.pod.namespace': 'chill-system',
                                    'io.kubernetes.pod.name': 'native', 'io.kubernetes.pod.uid': 'uid'}}]}
            return SimpleNamespace(returncode=0, stdout=json.dumps(value))
        def read_text(path, *args, **kwargs):
            name = str(path)
            if name.startswith('/proc/'):
                if name == '/proc/202/cgroup' and clock[0] >= 2:
                    raise FileNotFoundError(name)
                return '0::/fixture\n'
            if name.endswith('/cpu.stat'):
                return 'usage_usec %d\nnr_throttled 0\nthrottled_usec 0\n' % (clock[0] * 1000000)
            if name.endswith('/memory.current'):
                return '100'
            raise AssertionError(name)
        base = dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)
        class DateTime(dt.datetime):
            @classmethod
            def now(cls, tz=None):
                return base + dt.timedelta(seconds=clock[0])
        output = io.StringIO()
        with patch.object(sys, 'argv', ['-', 'fixture', '5', 'E5-2']), \
             patch.object(subprocess, 'run', run), patch.object(Path, 'read_text', read_text), \
             patch.object(time, 'monotonic', lambda: clock[0]), \
             patch.object(time, 'sleep', lambda seconds: clock.__setitem__(0, clock[0] + seconds)), \
             patch('glob.glob', return_value=[]), patch.object(dt, 'datetime', DateTime), \
             contextlib.redirect_stdout(output):
            exec(source.getvalue(), {})
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        assert not any(row['kind'] == 'collector-error' for row in rows)
        counters = [row for row in rows if row['kind'] == 'cgroup']
        assert inspected == ['current', 'second', 'third'], inspected
        assert [row['pidAbsent'] for row in counters] == [True, False, True, False, False]
        assert all(counters[i][key] is None for i in (0, 2)
                   for key in ('cpuUsageNanos', 'nrThrottled', 'throttledNanos', 'memoryBytes'))
        (folder / 'fixture-cgroup.jsonl').write_text(output.getvalue())
        judged = analysis.telemetry(analysis.Inputs(folder), base.timestamp(), base.timestamp() + 4)
        assert judged['roles']['native']['pidAbsentSamples'] == 2
        assert judged['roles']['native']['coverage'] == 'incomplete'
        assert judged['roles']['native']['cpuObservedWallSeconds'] == 1
        assert judged['roles']['native']['cpuMeanCores'] == 1
        assert not judged['collectorErrors']
    print('PASS cgroup lifecycle: current Pod ID, missing PID continuation, next-sample restart resolution, missing coverage; cluster calls=0')


if __name__ == '__main__':
    main()
