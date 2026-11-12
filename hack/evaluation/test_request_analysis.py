"""Recording diagnostics must not alter cohort membership or outcome classes."""
import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import judgment
import raw
import request_analysis as analysis


class RequestAnalysisTest(unittest.TestCase):
    def client(self, rid='r', **changes):
        return {'requestIndex': 0, 'scheduledOffsetNs': 0, 'requestID': rid,
                'issued': True, 'outcome': 'http_error', 'status': 503,
                'elapsedNanos': 120, **changes}

    def final(self, **changes):
        return {'outcome': 'rejected', 'executionComplete': False,
                'rejection': {'code': 'DeadlineInfeasible', 'stage': 'executor', 'policy': 'Reject'}, **changes}

    def metrics(self, final, row=None, **kwargs):
        return analysis.request_metrics([row or self.client()], {'r': final} if final else {},
                                        100, 1, 1000, 0, **kwargs)

    def test_prediction_and_each_wait_reason_survive_executor_rejection(self):
        prediction = {'at': '2026-11-12T00:00:00Z', 'predictedCompletionNanos': 40,
                      'remainingSLONanos': 80, 'input': {'instance': 'i', 'running': []}}
        waits = [{'at': prediction['at'], 'elapsedNanos': i, 'state': 'AdmissionUnknown',
                  'reason': reason, 'input': {'instance': 'i'}}
                 for i, reason in enumerate(analysis.ADMISSION_WAIT_REASONS)]
        final = self.final(admissionPrediction=prediction, admissionWaits=waits, deferredNanos=0)
        before = copy.deepcopy(final)
        result = self.metrics(final)
        request = result['requestAnalysis'][0]
        self.assertEqual(request['outcomeClass'], 'rejected')
        self.assertEqual(request['facts']['admissionPrediction'], prediction)
        self.assertEqual(request['facts']['admissionWaits'], waits)
        self.assertEqual(final, before)
        facts = result['nonOnTimeBreakdown']['directFacts']
        for reason in analysis.ADMISSION_WAIT_REASONS:
            self.assertEqual(facts['admissionWaitReasons'][reason], 1)
        self.assertEqual(facts['admissionPrediction']['withinRemainingSLO'], 1)
        self.assertEqual(facts['timingNS']['deferredNanos']['p50'], 0)

    def test_supplement_native_clock_and_gateway_exclusive_accounting(self):
        stages = ('workerHTTPStart', 'workerHTTPGetConn', 'workerHTTPGotConn',
                  'workerHTTPWroteRequest', 'workerHTTPFirstResponseByte', 'workerHTTPDone')
        timing = [{'stage': stage, 'elapsedNanos': value} for stage, value in zip(stages, (0, 5, 15, 30, 80, 100))]
        timing += [{'stage': 'workerHTTPConnectStart', 'elapsedNanos': 6},
                   {'stage': 'workerHTTPConnectDone', 'elapsedNanos': 14}]
        native = {'handlerEnteredAt': '2026-11-12T00:00:00Z', 'decodeStartNanos': 0,
                  'receivedNanos': 0, 'decodeDoneNanos': 2, 'enqueueNanos': 4, 'finishedNanos': 34,
                  'responseWriteStartNanos': 40}
        supplement = {'row': {'nativeHTTP': native, 'nativeDeferredAt': native['handlerEnteredAt'],
                              'nativeOutcome': 'completed'}, 'ref': {'line': 2}}
        result = self.metrics(self.final(serverTiming=timing, nativeElapsedMS=.000034,
                                         nativeOutcome='completed'), native_records={'r': [supplement]})
        facts = result['requestAnalysis'][0]['facts']
        self.assertEqual(facts['nativeHTTP'], native)
        self.assertEqual(facts['nativeDeferredAt'], native['handlerEnteredAt'])
        self.assertEqual(facts['fieldSources']['nativeHTTP'], 'nativeSupplement')
        accounting = facts['transportAccounting']
        self.assertEqual(sum(accounting['gatewaySegmentsNanos'].values()), 100)
        self.assertEqual(accounting['gatewayUnmeasuredNanos'], 0)
        self.assertEqual(accounting['connectDiagnosticNanos'], 8)
        self.assertEqual(accounting['nativeSegmentsNanos']['enqueueToFinished'], 30)
        self.assertEqual(result['outcomeClasses']['rejected'], 1)
        breakdown = result['nonOnTimeBreakdown']
        self.assertEqual(breakdown['directFacts']['timingNS']['nativeSegmentsNanos.decode']['p50'], 2)
        self.assertEqual(accounting['exchangeOutsideNativeNanos'], 66)
        self.assertEqual(accounting['outsideNativeUnmeasuredNanos'], 60)
        self.assertEqual(accounting['exchangeOutsideNativeNanos'],
                         accounting['nativeSegmentsNanos']['handlerEntryToReceived'] +
                         accounting['nativeSegmentsNanos']['finishedToResponseWriteStart'] +
                         accounting['outsideNativeUnmeasuredNanos'])
        partial = analysis.request_diagnostics(self.final(serverTiming=[timing[0], timing[1], timing[5]]), [])
        self.assertEqual(partial['transportAccounting']['gatewayUnmeasuredNanos'], 95)
        invalid = analysis.request_diagnostics(self.final(serverTiming=[timing[0],
            {'stage': 'workerHTTPGetConn', 'elapsedNanos': 101}, timing[5]]), [])
        self.assertEqual(invalid['transportAccounting']['gatewayUnmeasuredNanos'], 100)

    def test_timeout_final_uses_only_finished_native_supplement_diagnostics(self):
        stages = ('workerHTTPStart', 'workerHTTPGetConn', 'workerHTTPGotConn',
                  'workerHTTPWroteRequest', 'workerHTTPFirstResponseByte', 'workerHTTPDone')
        timing = [{'stage': stage, 'elapsedNanos': value}
                  for stage, value in zip(stages, (0, 5, 15, 30, 80, 100))]
        final = {'outcome': 'timeout', 'executionComplete': False, 'elapsedNanos': 100,
                 'nativeElapsedMS': .000010, 'serverTiming': timing,
                 'nativeHTTP': {'handlerEnteredAt': 'same', 'responseWriteStartNanos': 40}}
        queued = {'row': {'nativeQueuedAt': '2026-11-12T00:00:00Z', 'nativeExecutionComplete': False,
                          'nativeElapsedMS': .000004,
                          'nativeHTTP': {'handlerEnteredAt': 'same', 'receivedNanos': 0,
                                         'enqueueNanos': 4}}, 'ref': {'line': 1}}
        deferred = {'row': {'nativeDeferredAt': '2026-11-12T00:00:00Z',
                            'nativeElapsedMS': .000010}, 'ref': {'line': 2}}
        finished = {'row': {'nativeOutcome': 'completed', 'nativeExecutionComplete': True,
                            'nativeFinishedAt': '2026-11-12T00:00:00.000000034Z',
                            'nativeElapsedMS': .000034, 'executorWaitNanos': 7,
                            'nativeHTTP': {'handlerEnteredAt': 'same', 'finishedNanos': 34}},
                    'ref': {'line': 3}}
        before = copy.deepcopy((final, queued, deferred, finished))
        for records in ([queued, deferred, finished], [finished, deferred, queued]):
            with self.subTest(order=[record['ref']['line'] for record in records]):
                result = self.metrics(final, self.client(outcome='timeout', status=0),
                                      native_records={'r': records})
                request = result['requestAnalysis'][0]
                facts = request['facts']
                self.assertEqual(facts['nativeOutcome'], 'completed')
                self.assertEqual(facts['nativeElapsedMS'], .000034)
                self.assertEqual(facts['executorWaitNanos'], 7)
                for field in ('nativeOutcome', 'nativeElapsedMS', 'executorWaitNanos'):
                    self.assertEqual(facts['fieldSources'][field], 'nativeSupplement')
                self.assertEqual(facts['fieldSources']['nativeHTTP'], 'final+nativeSupplement')
                self.assertEqual(facts['nativeHTTP']['responseWriteStartNanos'], 40)
                self.assertEqual(facts['nativeSupplements'][records.index(finished)]['nativeFinishedAt'],
                                 finished['row']['nativeFinishedAt'])
                self.assertEqual(facts['transportAccounting']['exchangeOutsideNativeNanos'], 66)
                self.assertEqual(facts['transportAccounting']['outsideNativeUnmeasuredNanos'], 60)
                self.assertEqual(request['outcomeClass'], 'timeout')
                self.assertEqual(request['timeoutCause'], 'serverFinalDeadline')
                self.assertIsNone(request['timing']['sloLatencyNS'])
                self.assertEqual(result['outcomeClasses']['onTime'], 0)
                breakdown = result['nonOnTimeBreakdown']['directFacts']
                self.assertEqual(breakdown['nativeOutcomes'], {'completed': 1})
                self.assertEqual(breakdown['timingNS']['executorWaitNanos']['p50'], 7)
                self.assertEqual(breakdown['timingNS']['exchangeOutsideNativeNanos']['p50'], 66)
                for field in ('nativeOutcome', 'nativeElapsedMS', 'executorWaitNanos'):
                    self.assertEqual(breakdown['fieldSources'][field], {'nativeSupplement': 1})
        self.assertEqual((final, queued, deferred, finished), before)
        partial = analysis.request_diagnostics(final, [queued, deferred])
        self.assertIsNone(partial['nativeElapsedMS'])
        self.assertEqual(partial['fieldSources']['nativeElapsedMS'], 'partialNativeProgress')
        self.assertIsNone(partial['transportAccounting']['exchangeOutsideNativeNanos'])
        self.assertIsNone(partial['executorWaitNanos'])
        direct_wait = analysis.request_diagnostics({**final, 'executorWaitNanos': 9}, [finished])
        self.assertIsNone(direct_wait['executorWaitNanos'])
        self.assertEqual(direct_wait['fieldSources']['executorWaitNanos'], 'conflictingNativeSupplements')
        for marker in ('nativeOutcome', 'nativeExecutionComplete', 'nativeFinishedAt'):
            row = {key: value for key, value in finished['row'].items()
                   if key not in ('nativeOutcome', 'nativeExecutionComplete', 'nativeFinishedAt') or key == marker}
            with self.subTest(marker=marker):
                facts = analysis.request_diagnostics(final, [queued, {'row': row, 'ref': {'line': 3}}])
                self.assertEqual(facts['nativeElapsedMS'], .000034)
                self.assertEqual(facts['executorWaitNanos'], 7)
        direct = {**final, **{key: finished['row'][key] for key in
                  ('nativeOutcome', 'nativeExecutionComplete', 'nativeFinishedAt',
                   'nativeElapsedMS', 'executorWaitNanos')}}
        agreed = analysis.request_diagnostics(direct, [queued, finished])
        for field in ('nativeOutcome', 'nativeElapsedMS', 'executorWaitNanos'):
            self.assertEqual(agreed['fieldSources'][field], 'final')
            value = 'batch_error' if field == 'nativeOutcome' else 99
            conflict = analysis.request_diagnostics({**direct, field: value}, [queued, finished])
            self.assertIsNone(conflict[field])
            self.assertEqual(conflict['fieldSources'][field], 'conflictingNativeSupplements')
        self.assertIsNone(analysis.request_diagnostics(
            {**direct, 'nativeElapsedMS': 99}, [finished])['transportAccounting']['exchangeOutsideNativeNanos'])
        other = {'row': {**finished['row'], 'nativeElapsedMS': 99}, 'ref': {'line': 4}}
        conflict = analysis.request_diagnostics(final, [finished, other])
        self.assertIsNone(conflict['nativeElapsedMS'])
        self.assertEqual(conflict['fieldSources']['nativeElapsedMS'], 'conflictingNativeSupplements')

    def test_missing_and_conflicting_facts_remain_unknown(self):
        result = analysis.request_metrics([self.client()], {}, 100, 2, 1000, 0)
        facts = result['nonOnTimeBreakdown']['directFacts']
        self.assertEqual(facts['missingFields']['nativeHTTP'], 2)
        self.assertEqual(facts['admissionWaitReasons']['missing'], 2)
        self.assertEqual(facts['admissionPrediction']['unknown'], 2)
        unknown = self.metrics(self.final(admissionWaits=[{'reason': 'future', 'state': 'deferred'}]))
        self.assertEqual(unknown['nonOnTimeBreakdown']['directFacts']['admissionWaitReasons']['unknown'], 1)
        records = [{'row': {'nativeHTTP': {'finishedNanos': value}}, 'ref': {'line': value}} for value in (1, 2)]
        diagnosed = analysis.request_diagnostics(None, records)
        self.assertIsNone(diagnosed['nativeHTTP'])
        self.assertEqual(diagnosed['fieldSources']['nativeHTTP'], 'conflictingNativeSupplements')
        self.assertEqual(len(diagnosed['nativeSupplements']), 2)
        timeout = self.metrics({'outcome': 'completed', 'executionComplete': True},
                               self.client(outcome='timeout', status=0), native_records={'r': records})
        self.assertEqual(timeout['outcomeClasses']['timeout'], 1)
        self.assertIsNone(timeout['requestAnalysis'][0]['timing']['sloLatencyNS'])

    def test_raw_retains_supplements_without_promoting_native_only_or_changing_final(self):
        final = {'kind': 'final', 'requestID': 'r', 'workloadUID': 'uid', 'definitionID': 'd',
                 'receivedAt': '2026-11-12T00:00:00Z', 'outcome': 'timeout', 'elapsedNanos': 100}
        native = {'kind': 'native', 'requestID': 'r', 'workloadUID': 'uid',
                  'nativeOutcome': 'completed', 'nativeHTTP': {'finishedNanos': 5}}
        orphan = {**native, 'requestID': 'orphan'}
        rows = [(None, row, {'line': i}) for i, row in enumerate((final, native, orphan), 1)]
        raw.release_request_cache()
        try:
            with patch.object(raw, '_root', return_value=Path('/archive')), \
                 patch.object(raw, '_signature', return_value=('synthetic',)), \
                 patch.object(raw, '_manifest', return_value=({}, [], [])), \
                 patch.object(raw, '_population'), patch.object(raw, '_rows', return_value=iter(rows)):
                indexed = raw.iter_request_indexes('/archive', ['uid'], '2026-11-12T00:00:00Z',
                                                  '2026-11-12T00:00:01Z')['uid']
            self.assertIs(indexed['finals']['r'], final)
            self.assertNotIn('orphan', indexed['finals'])
            self.assertEqual(indexed['nativeRecords']['r'][0]['row'], native)
            self.assertEqual(indexed['summary']['uniqueRequests'], 1)
            self.assertEqual(indexed['summary']['unresolvedNativeOnly'][0]['requestID'], 'orphan')
        finally:
            raw.release_request_cache()

    def test_judgment_passes_native_and_receipt_facts_to_each_cohort(self):
        config = {'workloads': [{'name': 'model', 'uid': 'uid', 'generation': 1,
                                'slo': {'percentile': 95, 'targetLatencyNanos': 100}}],
                  'generatorQuality': {'latenessToleranceNS': 50, 'maxExceedingFraction': .01}}
        phase = {'stage': {'seconds': 1, 'parts': [{'id': 'all', 'seconds': 1, 'rates': {'model': 1}}],
                           'traces': {'model': {'offsetsNS': [0], 'offered': 1}}},
                 'starts': {'model': {'monotonicNS': 0}}}
        indexed = {'finals': {'r': self.final()}, 'receivedAtNS': {'r': 10}, 'errors': [], 'summary': {},
                   'identities': [{'workloadUID': 'uid', 'definitionID': 'd'}],
                   'nativeRecords': {'r': [{'row': {'nativeHTTP': {'finishedNanos': 5}}, 'ref': {'line': 2}}]}}
        result = judgment.reduce_requests(config, phase, {'model': [self.client(sentAtUnixNS=12)]}, {'uid': indexed})['model']
        request = result['requestAnalysis'][0]
        self.assertEqual(request['timing']['entranceDelayNS'], -2)
        self.assertEqual(request['facts']['nativeHTTP'], {'finishedNanos': 5})
        self.assertEqual(result['substages'][0]['requestAnalysis'], result['requestAnalysis'])

    def test_progressive_native_timing_merges_without_losing_http_write_boundary(self):
        final = self.final(nativeHTTP={'handlerEnteredAt': 'same', 'finishedNanos': 4,
                                       'responseWriteStartNanos': 6})
        records = [{'row': {'nativeHTTP': {'handlerEnteredAt': 'same', 'enqueueNanos': 1}},
                    'ref': {'line': 1}},
                   {'row': {'nativeHTTP': {'handlerEnteredAt': 'same', 'finishedNanos': 4}},
                    'ref': {'line': 2}}]
        facts = analysis.request_diagnostics(final, records)
        self.assertEqual(facts['nativeHTTP'], {'handlerEnteredAt': 'same', 'enqueueNanos': 1,
                                               'finishedNanos': 4, 'responseWriteStartNanos': 6})
        self.assertEqual(facts['fieldSources']['nativeHTTP'], 'final+nativeSupplement')

    def test_judge_writes_request_analysis_alongside_compact_verdict(self):
        request = {'D1': 'FAIL', 'issuanceFidelity': {}, 'requestAnalysis': [{'requestID': 'r'}],
                   'nonOnTimeBreakdown': {'requests': 1},
                   'substages': [{'requestAnalysis': [{'requestID': 'r'}]}]}
        phase = {'stage': 'workload', 'startUTC': '2026-11-12T00:00:00Z',
                 'endUTC': '2026-11-12T00:00:01Z'}
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            for name, value in (('phase', phase), ('power', {}), ('power-drain-inclusive', {})):
                (folder / (name + '.json')).write_text(json.dumps(value))
            bound = {'config': {'workloads': [], 'evaluation': {}, 'collection': {'powerMeasurement': {'nodes': []}}}, 'limitations': []}
            with patch.object(judgment.evidence_io, 'load_binding', return_value=bound), \
                 patch.object(judgment.evidence_io, 'module', return_value=raw), \
                 patch.object(raw, 'iter_request_indexes', return_value={}), \
                 patch.object(judgment, 'reduce_requests', return_value={'model': request}):
                judgment.judge(folder / 'binding.json', folder)
            artifact = json.loads((folder / 'request-analysis.json').read_text())
            verdict = json.loads((folder / 'judgment.json').read_text())
            self.assertEqual(artifact['requests']['model']['requestAnalysis'], [{'requestID': 'r'}])
            self.assertEqual(artifact['requests']['model']['nonOnTimeBreakdown'], {'requests': 1})
            self.assertNotIn('requestAnalysis', verdict['requests']['model'])
            self.assertNotIn('requestAnalysis', verdict['requests']['model']['substages'][0])


if __name__ == '__main__':
    unittest.main()
