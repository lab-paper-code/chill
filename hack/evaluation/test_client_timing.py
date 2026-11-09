"""Client full-body E drives SLO; server records remain execution diagnostics."""

import copy
import json
import unittest

import judgment
import request_analysis as analysis
import tables


class ClientTimingTest(unittest.TestCase):
    def setUp(self):
        self.final = {'outcome': 'completed', 'executionComplete': True, 'elapsedNanos': 50}
        self.row = {'requestIndex': 0, 'scheduledOffsetNs': 0, 'issued': True, 'requestID': 'opaque-id',
                    'sentMonotonicNS': 100, 'finishedMonotonicNS': 1000, 'sentAtUnixNS': 200,
                    'status': 200, 'outcome': 'completed', 'elapsedNanos': 120}
        self.config = {'workloads': [{'name': 'model', 'uid': 'uid', 'generation': 1,
                                     'slo': {'percentile': 95, 'targetLatencyNanos': 100}}],
                       'generatorQuality': {'latenessToleranceNS': 50, 'maxExceedingFraction': .01}}

    def reduce(self, rows, finals=None, offered=None):
        offered = len(rows) if offered is None else offered
        phase = {'stage': {'seconds': 1, 'parts': [{'id': 'all', 'seconds': 1, 'rates': {'model': offered}}],
                           'traces': {'model': {'offsetsNS': list(range(offered)), 'offered': offered}}},
                 'starts': {'model': {'monotonicNS': 100}}, 'producerCapture': {'model': {'drained': True}}}
        indexed = {'finals': finals or {}, 'errors': [], 'summary': {},
                   'identities': [{'workloadUID': 'uid', 'definitionID': 'definition'}]}
        return judgment.reduce_requests(self.config, phase, {'model': rows}, {'uid': indexed})['model']

    def test_exact_client_duration_is_independent_of_server_and_caller_brackets(self):
        timing = analysis.request_timing(self.row, self.final, 190, 100)
        self.assertEqual(timing['sloLatencyNS'], 120)
        self.assertEqual(timing['serverElapsedNS'], 50)
        self.assertEqual(timing['actualLatencyNS'], 900)
        self.assertEqual(timing['scheduledLatencyNS'], 900)
        self.assertEqual(timing['entranceDelayNS'], -10)
        result = self.reduce([self.row], {'opaque-id': self.final})
        self.assertEqual(result['deadlineSuccess'], 0)
        self.assertEqual(result['D1'], 'FAIL')
        self.assertEqual(result['completedLatencyNS']['p95'], 120)
        self.assertEqual(result['gatewayCompletedLatencyNS']['p95'], 50)
        faster = {**self.row, 'elapsedNanos': 80}
        result = self.reduce([faster], {'opaque-id': {**self.final, 'elapsedNanos': 200}})
        self.assertEqual(result['deadlineSuccess'], 1)
        self.assertEqual(result['completedLatencyNS']['p95'], 80)
        self.assertEqual(result['gatewayCompletedLatencyNS']['p95'], 200)

    def test_full_success_and_native_execution_guards_remain(self):
        for change in ({'outcome': 'timeout', 'error': 'TimeoutError'},
                       {'outcome': 'http_error', 'status': 503},
                       {'outcome': 'transport_error', 'error': 'IncompleteRead'},
                       {'outcome': 'canceled', 'error': 'KeyboardInterrupt'},
                       {'status': 503}, {'elapsedNanos': True}, {'issued': False}):
            with self.subTest(change=change):
                row = {**self.row, **change}
                self.assertIsNone(analysis.request_timing(row, self.final, None, 100)['sloLatencyNS'])
        for final in (None, {**self.final, 'executionComplete': False}, {**self.final, 'outcome': 'error'}):
            self.assertIsNone(analysis.request_timing(self.row, final, None, 100)['sloLatencyNS'])

    def test_missing_id_timeout_is_failure_without_a_fake_server_join(self):
        timeout = {'requestIndex': 0, 'scheduledOffsetNs': 0, 'issued': True, 'sentMonotonicNS': 100,
                   'elapsedNanos': 80, 'outcome': 'timeout', 'error': 'TimeoutError'}
        original = copy.deepcopy(timeout)
        result = self.reduce([timeout])
        self.assertEqual((result['offered'], result['deadlineSuccess'], result['clientFailures']), (1, 0, 1))
        self.assertEqual(result['D1'], 'FAIL')
        self.assertEqual(result['quantileStatus'], 'unbounded')
        self.assertIsNone(result['quantileNanos'])
        self.assertEqual(result['gatewayCompletedLatencyNS']['samples'], 0)
        self.assertFalse(result['reachable'])
        self.assertEqual(result['substages'][0]['clientFailures'], 1)
        self.assertIsNone(result['definitionID'])
        self.assertEqual(result['unfinishedOrUnsent'], 0)
        self.assertEqual(timeout, original)
        published = tables.publication_requests({'requests': {'model': result}})['model']
        self.assertEqual(published['clientFailures'], 1)
        self.assertEqual(published['quantileStatus'], 'unbounded')
        json.dumps(published, allow_nan=False)

    def test_failures_remain_in_offered_percentile_denominator(self):
        rows = [{**self.row, 'requestIndex': i, 'scheduledOffsetNs': i, 'requestID': 'r' + str(i),
                 'elapsedNanos': i + 1} for i in range(18)]
        finals = {row['requestID']: self.final for row in rows}
        rows.extend({'requestIndex': i, 'scheduledOffsetNs': i, 'issued': True, 'sentMonotonicNS': 100 + i,
                     'elapsedNanos': 80, 'outcome': 'timeout', 'error': 'TimeoutError'} for i in (18, 19))
        result = self.reduce(rows, finals)
        self.assertEqual((result['offered'], result['deadlineSuccess'], result['clientFailures']), (20, 18, 2))
        self.assertEqual(result['requiredSuccessfulRank'], 19)
        self.assertEqual(result['D1'], 'FAIL')
        self.assertEqual(result['quantileStatus'], 'unbounded')
        self.config['workloads'][0]['slo']['percentile'] = 90
        result = self.reduce(rows, finals)
        self.assertEqual(result['quantileStatus'], 'finite')
        self.assertEqual(result['quantileNanos'], 18)
        self.assertEqual(result['D1'], 'OBSERVED_THRESHOLD_MET')

    def test_legacy_evidence_stays_unknown_and_unmodified(self):
        legacy = {key: value for key, value in self.row.items() if key not in ('elapsedNanos', 'outcome')}
        original = copy.deepcopy((legacy, self.final))
        result = self.reduce([legacy], {'opaque-id': self.final})
        self.assertEqual(result['completedLatencyNS']['samples'], 0)
        self.assertEqual(result['gatewayCompletedLatencyNS']['samples'], 1)
        self.assertEqual(result['clientUnknown'], 1)
        self.assertEqual(result['clientFailures'], 0)
        self.assertEqual(result['D1'], 'NOT_EVALUATED')
        self.assertEqual(result['quantileStatus'], 'unknown')
        self.assertEqual((legacy, self.final), original)
        unsent = {**self.row, 'issued': False, 'error': 'GeneratorThreadStartError'}
        result = self.reduce([unsent], {'opaque-id': self.final})
        self.assertEqual(result['clientFailures'], 0)
        self.assertEqual(result['D1'], 'INVALID_NO_LOAD')
