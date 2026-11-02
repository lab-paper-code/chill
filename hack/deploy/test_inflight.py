"""Local cutover-state checks; no cluster commands or mutations."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import inflight


def obj(kind, status=None, **extra):
    return {'kind': kind, 'metadata': {'name': 'measurement', 'namespace': 'other-namespace'},
            'status': status or {}, **extra}


def condition(name, status='True', **extra):
    return {'type': name, 'status': status, **extra}


def job(owner, status):
    return obj('Job', status, metadata={'name': 'worker', 'namespace': 'other-namespace',
                                      'ownerReferences': [{'kind': owner}]})


class InflightTests(unittest.TestCase):
    def test_preserved_terminal_states(self):
        states = [obj('ProfilingRun', {'conditions': [condition('EvidenceVerified')]}),
                  obj('EvaluationRun', {'frozenConfigMapRef': {'name': 'frozen'},
                                       'finishedAt': '2026-11-03T00:00:00Z',
                                       'conditions': [condition('Published')]})]
        for kind, phases in [('BatchEnergyOptimization', ('Complete', 'Failed')),
                             ('ModelLoadedIdleBaseline', ('Complete', 'Insufficient')),
                             ('DispatchOccupancyMeasurement', ('Complete', 'Insufficient', 'Failed'))]:
            states.extend(obj(kind, {'phase': phase}) for phase in phases)
        for reason in inflight.TERMINAL_FAILURES:
            for name in ('ExecutionComplete', 'EvidenceVerified'):
                states.append(obj('ProfilingRun', {'conditions': [condition(name, 'False', reason=reason)]}))
        for state in states:
            with self.subTest(state=state):
                self.assertEqual(inflight.blockers([state], []), [])

    def test_profiling_freeze_is_not_completion(self):
        state = obj('ProfilingRun')
        self.assertEqual(inflight.blockers([state], [])[0]['kind'], 'ProfilingRun')
        for identity in ({'job': {'uid': 'retained'}}, {'evidence': {'key': 'retained'}}):
            state['status'] = identity
            self.assertEqual(inflight.blockers([state], [])[0]['name'], 'measurement')
        for name, status, reason in [('ExecutionComplete', 'True', 'JobFailed'),
                                     ('EvidenceVerified', 'False', 'ScientificQualityLow'),
                                     ('Unrelated', 'False', 'JobFailed')]:
            state['status'] = {'conditions': [condition(name, status, reason=reason)]}
            self.assertTrue(inflight.blockers([state], []))
        state['status'] = {'conditions': [condition('EvidenceVerified')], 'jobCleanup': 'Pending'}
        self.assertEqual(inflight.blockers([state], [])[0]['kind'], 'ProfilingRun')
        state['status']['jobCleanup'] = 'Complete'
        self.assertEqual(inflight.blockers([state], []), [])
        state['metadata']['deletionTimestamp'] = '2026-11-03T00:00:00Z'
        self.assertEqual(inflight.blockers([state], [])[0]['name'], 'measurement')

    def test_campaign_boundaries(self):
        for kind in ('BatchEnergyOptimization', 'ModelLoadedIdleBaseline', 'DispatchOccupancyMeasurement'):
            self.assertTrue(inflight.blockers([obj(kind, {'phase': 'Running'})], []))
        self.assertTrue(inflight.blockers([obj('ModelLoadedIdleBaseline', {'phase': 'Failed'})], []))
        self.assertTrue(inflight.blockers([obj('BatchEnergyOptimization', {'phase': 'Insufficient'})], []))
        state = obj('DispatchOccupancyMeasurement', {'phase': 'Running'})
        self.assertEqual(inflight.blockers([state], [])[0]['kind'], 'DispatchOccupancyMeasurement')
        state['spec'] = {'conditions': [{'deviceClass': 'cpu'}]}
        self.assertEqual(inflight.blockers([state], [])[0]['name'], 'measurement')
        state['status']['phase'] = 'Complete'
        state.pop('spec')
        self.assertEqual(inflight.blockers([state], []), [])
        state['metadata']['deletionTimestamp'] = 'now'
        self.assertTrue(inflight.blockers([state], []))

    def test_evaluation_requires_freeze_publication_and_pool_return(self):
        status = {'frozenConfig': {'retained': True}, 'finishedAt': 'now',
                  'conditions': [condition('Published'), condition('PoolAcquired', 'False')]}
        self.assertEqual(inflight.blockers([obj('EvaluationRun', status)], []), [])
        for field in ('frozenConfig', 'finishedAt', 'conditions'):
            changed = copy.deepcopy(status)
            changed.pop(field)
            self.assertTrue(inflight.blockers([obj('EvaluationRun', changed)], []))
        for conditions in ([condition('Published', 'False')],
                           [condition('Published'), condition('PoolAcquired')]):
            changed = dict(status, conditions=conditions)
            self.assertTrue(inflight.blockers([obj('EvaluationRun', changed)], []))

    def test_jobs_only_block_for_chill_owners_and_unfinished_execution(self):
        for owner in inflight.KINDS:
            for terminal in ('Complete', 'Failed'):
                status = {'conditions': [condition(terminal)]}
                self.assertEqual(inflight.blockers([], [job(owner, status)]), [])
                self.assertTrue(inflight.blockers([], [job(owner, dict(status, active=1))]))
            self.assertTrue(inflight.blockers([], [job(owner, {})]))
            self.assertTrue(inflight.blockers([], [job(owner, {'conditions': [condition('Complete', 'False')]})]))
        self.assertEqual(inflight.blockers([], [job('ForeignController', {'active': 2}),
                                                obj('Job', {'active': 1})]), [])

    def test_check_persists_evidence_before_refusing_and_uses_unique_labels(self):
        kube = ['kubectl', '--kubeconfig', '/private/config', '--context', 'selected', '--namespace', 'chill']
        objects = [obj('ProfilingRun')]
        jobs = [job('ForeignController', {'active': 1})]
        calls = []
        def run(argv, name, timeout=120, cwd=None, env=None):
            calls.append((argv, name))
            return json.dumps({'items': jobs if argv[len(kube) + 1] == 'jobs' else objects}).encode()
        with tempfile.TemporaryDirectory() as out:
            with self.assertRaises(RuntimeError):
                inflight.check(kube, run, out, 'before-cutover')
            evidence, = Path(out).iterdir()
            blocked = json.loads((evidence / 'blockers.json').read_bytes())
            self.assertEqual([(row['kind'], row['name']) for row in blocked], [('ProfilingRun', 'measurement')])
            for path in evidence.iterdir():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            objects.clear()
            report = inflight.check(kube, run, out, 'before-cutover')
            self.assertEqual(report['blockers'], [])
            self.assertNotEqual(report['evidenceDirectory'], str(evidence))
            self.assertEqual(len(list(Path(out).iterdir())), 2)


if __name__ == '__main__':
    unittest.main()
