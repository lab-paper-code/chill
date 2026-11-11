"""Arrival-cohort overload metrics and strict stored-contract cutover."""
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest

import judgment
import raw
import tables

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'load-generator/src'))
from chill_loadgen import cli


class OverloadAdmissionTest(unittest.TestCase):
    def setUp(self):
        self.config = {'workloads': [{'name': 'model', 'uid': 'uid', 'generation': 1,
                                     'slo': {'percentile': 95, 'targetLatencyNanos': 100}}],
                       'generatorQuality': {'latenessToleranceNS': 50, 'maxExceedingFraction': .01}}
        self.phase = {'stage': {'seconds': 10, 'parts': [{'id': 'all', 'seconds': 10, 'rates': {'model': 1}}],
                                'traces': {'model': {'offsetsNS': list(range(10)), 'offered': 10}}},
                      'starts': {'model': {'at': '2026-11-03T00:00:00Z', 'monotonicNS': 100}},
                      'producerCapture': {'model': {'drained': True}}}
        self.index = {'finals': {}, 'errors': [], 'summary': {},
                      'identities': [{'workloadUID': 'uid', 'definitionID': 'definition'}]}

    def client(self, index, outcome='completed', elapsed=80, status=200, **facts):
        return {'requestIndex': index, 'scheduledOffsetNs': index, 'requestID': 'r' + str(index),
                'issued': True, 'sentMonotonicNS': 100 + index, 'outcome': outcome,
                'status': status, 'elapsedNanos': elapsed, **facts}

    def completed(self, index):
        self.index['finals']['r' + str(index)] = {'outcome': 'completed', 'executionComplete': True}

    def reject(self, index, code='QueueFull', stage='gateway', policy='Reject', **waits):
        metadata = {'code': code, 'stage': stage, 'policy': policy}
        self.index['finals']['r' + str(index)] = {'outcome': 'rejected', 'executionComplete': False,
                                                'rejection': metadata, 'admissionPolicy': policy, **waits}
        return self.client(index, 'http_error', 1, 429 if code == 'QueueFull' else 503,
                           rejectionCode=code, rejectionStage=stage, rejectionPolicy=policy)

    def reduce(self, rows):
        return judgment.reduce_requests(self.config, self.phase, {'model': rows}, {'uid': self.index})['model']

    def test_mixed_classes_partition_offered_and_publish_wait_samples(self):
        rows = [self.client(0), self.client(1, elapsed=101),
                self.reject(2),
                self.reject(3, 'DeadlineInfeasible', 'executor', 'Delay', gatewayWaitNanos=0, executorWaitNanos=7),
                self.client(4, 'http_error', status=408), self.client(5, 'timeout'),
                self.client(6, 'transport_error'), self.client(7, 'http_error', status=500),
                self.client(8, issued=False, error='UnsentAtWindowEnd')]
        self.completed(0)
        self.completed(1)
        self.index['finals']['r4'] = {'outcome': 'timeout'}
        self.completed(5)  # Native completion after client socket timeout is not late success.
        result = self.reduce(rows)
        self.assertEqual(result['outcomeClasses'], {'onTime': 1, 'late': 1, 'rejected': 2,
                                                    'timeout': 2, 'failed': 2, 'incomplete': 2})
        self.assertEqual(sum(result['outcomeClasses'].values()), result['offered'])
        self.assertEqual(result['timeoutCauses'], {'serverFinalDeadline': 1, 'clientSocket': 1})
        self.assertEqual(result['onTimeSuccessRate'], .1)
        self.assertEqual(result['goodputRequestsPerSecond'], .1)
        self.assertEqual(result['completedLatencyNS']['samples'], 2)
        self.assertEqual(result['completedLatencyNS']['max'], 101)
        self.assertEqual(result['gatewayWaitNS']['samples'], 1)
        self.assertEqual(result['gatewayWaitNS']['p50'], 0)
        self.assertEqual(result['executorWaitNS']['p99'], 7)
        groups = {(row['policy'], row['stage'], row['code']): row for row in result['rejections']}
        self.assertEqual(groups['Reject', 'gateway', 'QueueFull']['executorWaitNS']['samples'], 0)
        self.assertEqual(groups['Delay', 'executor', 'DeadlineInfeasible']['count'], 1)
        self.assertEqual(groups['Delay', 'executor', 'DeadlineInfeasible']['gatewayWaitNS']['samples'], 1)
        part = result['substages'][0]
        self.assertEqual(part['outcomeClasses'], result['outcomeClasses'])
        published = tables.publication_requests({'requests': {'model': result}})['model']
        for key in ('outcomeClasses', 'timeoutCauses', 'onTimeSuccessRate', 'goodputRequestsPerSecond',
                    'completedLatencyNS', 'loadLengthSeconds', 'rejections', 'gatewayWaitNS', 'executorWaitNS'):
            self.assertEqual(published[key], result[key])
        json.dumps(published, allow_nan=False)

    def test_rejection_heavy_low_latency_never_improves_verdict_or_goodput(self):
        rows = [self.client(i, elapsed=90) for i in range(10)]
        for i in range(10):
            self.completed(i)
        baseline = self.reduce(rows)
        rows = [self.client(0, elapsed=1)] + [self.reject(i) for i in range(1, 10)]
        rejected = self.reduce(rows)
        self.assertLess(rejected['completedLatencyNS']['p99'], baseline['completedLatencyNS']['p99'])
        self.assertLess(rejected['onTimeSuccessRate'], baseline['onTimeSuccessRate'])
        self.assertLess(rejected['goodputRequestsPerSecond'], baseline['goodputRequestsPerSecond'])
        self.assertEqual((baseline['D1'], rejected['D1']), ('OBSERVED_THRESHOLD_MET', 'FAIL'))
        self.assertEqual(rejected['outcomeClasses']['rejected'], 9)

    def test_timeout_precedence_requires_client_success_for_late(self):
        for client, server, expected, cause in (
                ('completed', 'completed', 'late', None),
                ('timeout', 'completed', 'timeout', 'clientSocket'),
                ('timeout', 'timeout', 'timeout', 'serverFinalDeadline'),
                ('http_error', 'timeout', 'timeout', 'serverFinalDeadline')):
            with self.subTest(client=client, server=server):
                row = self.client(0, client, elapsed=101, status=408 if client == 'http_error' else 200)
                self.index['finals']['r0'] = {'outcome': server, 'executionComplete': True}
                result = self.reduce([row])
                self.assertEqual(result['outcomeClasses'][expected], 1)
                self.assertEqual(result['completedLatencyNS']['samples'], int(expected == 'late'))
                if cause:
                    self.assertEqual(result['timeoutCauses'][cause], 1)

    def test_provisional_denominator_uses_same_workload_cohort_cutoff(self):
        self.phase['stage']['traces']['model'] = {'offsetsNS': [0, 3_000_000_000, 9_000_000_000], 'offered': 3}
        self.phase['startUTC'] = '2026-11-02T23:59:50Z'  # Another producer's earlier start must not extend denominator.
        self.phase['endUTC'] = '2026-11-03T00:00:20Z'
        row = self.client(0, elapsed=80, finishedMonotonicNS=11_000_000_000)
        self.completed(0)
        cutoff = '2026-11-03T00:00:02Z'
        value = judgment.provisional_requests(self.config, self.phase, {'model': [row]}, {'uid': self.index},
                                              '2026-11-03T00:00:12Z', cutoff)['workloads']['model']
        self.assertEqual(value['offered'], 1)
        self.assertEqual(value['loadLengthSeconds'], 2)
        self.assertEqual(value['goodputRequestsPerSecond'], .5)
        self.assertEqual(value['onTimeSuccessRate'], 1)
        self.assertEqual(value['completedLatencyNS']['samples'], 1)
        self.assertEqual(value['substages'][0]['goodputRequestsPerSecond'], .5)
        self.assertNotIn('D1', value)
        self.assertNotIn('D1', value['substages'][0])
        final = self.reduce([row])
        self.assertEqual(final['loadLengthSeconds'], 10)
        self.assertEqual(final['goodputRequestsPerSecond'], .1)
        for early in ('2026-11-02T23:59:59Z', '2026-11-03T00:00:00Z'):
            zero = judgment.provisional_requests(self.config, self.phase, {'model': [row]}, {'uid': self.index},
                                                 cutoff, early)['workloads']['model']
            self.assertIsNone(zero['goodputRequestsPerSecond'])
            self.assertEqual(zero['loadLengthSeconds'], 0)
            self.assertEqual(sum(zero['outcomeClasses'].values()), zero['offered'])

    def test_empty_started_cohort_keeps_its_cutoff_length(self):
        self.phase['stage']['traces']['model'] = {'offsetsNS': [], 'offered': 0}
        cutoff = '2026-11-03T00:00:02Z'
        value = judgment.provisional_requests(self.config, self.phase, {'model': []}, {'uid': self.index},
                                              cutoff, cutoff)['workloads']['model']
        self.assertEqual(value['loadLengthSeconds'], 2)
        self.assertEqual(value['goodputRequestsPerSecond'], 0)
        self.assertIsNone(value['onTimeSuccessRate'])

    def test_quarantine_missing_and_duplicate_identity_stay_incomplete(self):
        rows = [self.client(0), self.client(1), self.client(2), self.client(2)]
        for i in range(3):
            self.completed(i)
        self.index['summary'] = {'finalConflicts': [{'requestID': 'r0'}], 'invalidFinalRequestIDs': ['r1']}
        result = self.reduce(rows)
        self.assertEqual(result['outcomeClasses']['incomplete'], 10)
        self.assertEqual(result['deadlineSuccess'], 0)
        self.assertEqual(result['collectionLoss'], 0)
        self.assertEqual(result['quarantinedClientRows'], 4)
        rows = [self.reject(0)]
        del self.index['finals']['r0']['rejection']['policy']
        self.index['summary'] = {}
        self.assertEqual(self.reduce(rows)['outcomeClasses']['incomplete'], 10)

    def test_absent_rejection_metadata_never_gets_defaults(self):
        row = self.client(0, 'http_error', status=429, error='QueueFull')
        result = self.reduce([row])
        self.assertEqual(result['outcomeClasses']['failed'], 1)
        self.assertEqual(result['rejections'], [])
        self.index['finals']['r0'] = {'outcome': 'rejected'}
        self.assertEqual(self.reduce([row])['outcomeClasses']['failed'], 1)

    def test_loadgen_rejection_row_joins_without_final_and_quarantines_conflicts(self):
        metadata = {'code': 'DeadlineInfeasible', 'stage': 'gateway', 'policy': 'Delay'}
        response = SimpleNamespace(status_code=503, request_id='r0', workload_uid='uid',
                                   elapsed_nanos=7, body=json.dumps(metadata).encode())
        row = self.client(0)
        cli._infer(SimpleNamespace(infer=lambda *_: response), 'model', b'inputs', row,
                   SimpleNamespace(enqueue=lambda _: None))
        self.assertEqual(row['outcome'], 'http_error')
        self.assertEqual(row['rejectionCode'], metadata['code'])
        self.assertEqual(row['rejectionStage'], metadata['stage'])
        self.assertEqual(row['rejectionPolicy'], metadata['policy'])
        self.assertNotIn('rejection', row)
        result = self.reduce([row])
        self.assertEqual(result['outcomeClasses']['rejected'], 1)
        self.assertEqual(result['rejections'][0]['policy'], 'Delay')
        self.index['finals']['r0'] = {'outcome': 'rejected', 'rejection': metadata}
        self.assertEqual(self.reduce([row])['outcomeClasses']['rejected'], 1)
        for field, value in (('stage', 'executor'), ('policy', 'Reject')):
            with self.subTest(conflicting=field):
                self.index['finals']['r0']['rejection'] = {**metadata, field: value}
                result = self.reduce([row])
                self.assertEqual(result['outcomeClasses']['incomplete'], 10)
                self.assertEqual(result['rejections'], [])

    def test_complete_non_admission_http_rejections_are_failed(self):
        for status, code in ((400, 'InvalidInput'), (401, 'Unauthorized'), (403, 'Forbidden'), (422, 'InvalidInput')):
            with self.subTest(status=status):
                row = self.client(0, 'http_error', status=status, rejectionCode=code)
                self.index['finals']['r0'] = {'outcome': 'rejected', 'executionComplete': False, 'error': code}
                result = self.reduce([row])
                self.assertEqual(result['outcomeClasses']['failed'], 1)
                self.assertEqual(result['outcomeClasses']['incomplete'], 9)
                self.assertEqual(result['outcomeClasses']['rejected'], 0)
                self.assertEqual(result['rejections'], [])

    def test_partial_admission_metadata_is_incomplete(self):
        row = self.client(0, 'http_error', status=503, rejectionCode='DeadlineInfeasible', rejectionStage='gateway')
        self.assertEqual(self.reduce([row])['outcomeClasses']['incomplete'], 10)

    def test_raw_final_conflicts_include_admission_facts(self):
        original = {'kind': 'final', 'workloadUID': 'uid', 'requestID': 'r0', 'outcome': 'rejected',
                    'rejection': {'code': 'QueueFull', 'stage': 'gateway', 'policy': 'Reject'}}
        for field, value in (('rejection', {'code': 'QueueFull', 'stage': 'executor', 'policy': 'Reject'}),
                             ('admissionPolicy', 'Delay'), ('gatewayWaitNanos', 0), ('executorWaitNanos', 1)):
            with self.subTest(field=field):
                self.assertNotEqual(raw._digest(original), raw._digest({**original, field: value}))

    def test_contract_versions_are_strict_for_file_and_in_memory_readers(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'judgment.json'
            for version in ('e5-judgment.v3', 'e5-judgment.v5', None):
                path.write_text(json.dumps({'schema': version}))
                with self.assertRaisesRegex(ValueError, 'judgment v4 required'):
                    tables.sections(path)
            path.write_text(json.dumps({'schema': 'e5-judgment.v4'}))
            self.assertEqual(tables.sections(path)['schema'], 'e5-judgment.v4')
            for version in ('e5-summary.v4', 'e5-summary.v6', None):
                value = {'schema': version, 'status': 'PARTIAL', 'stages': []}
                (root / 'e5-summary.json').write_text(json.dumps(value))
                with self.assertRaisesRegex(ValueError, 'summary v5 required'):
                    tables.render(root)
                with self.assertRaisesRegex(ValueError, 'summary v5 required'):
                    tables.render(root, evaluation=value)
            summary = judgment.summarize({}, root, evaluation={'stages': [], 'results': {}})
            self.assertEqual(summary['schema'], 'e5-summary.v5')
            self.assertIn('on-time success rate | goodput req/s', tables.render(root))


if __name__ == '__main__':
    unittest.main()
