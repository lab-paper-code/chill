"""Regressions for realistic stage capture and conservative prediction verdicts."""
import copy
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import urllib.parse

import archive
import evidence_io
import judgment
import prediction_analysis as analysis
from test_prediction_analysis import fixture, power, stamp, NODE


class PredictionReviewTests(unittest.TestCase):
    def capture(self, events, start=2, end=6, prior=None, elapsed_per_call=0, budget=300):
        calls, clock = [], [0.0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / 'stage'
            folder.mkdir()
            env = root / 'env.json'
            evidence_io.save(env, {'paths': {'campaign': str(root)}})
            def api(method, path, timeout):
                query = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
                calls.append((path, timeout, clock[0]))
                clock[0] += elapsed_per_call
                if '/scheduler?' in path:
                    if query.get('order') == ['desc']:
                        return {'events': [prior] if prior else [], 'coverage': {'gaps': []}}
                    first, last = analysis.ns(query['from'][0]), analysis.ns(query['to'][0])
                    selected = [e for e in ([prior] if prior else []) + events if first <= analysis.ns(e['at']) < last]
                    index = int(query.get('cursor', ['0'])[0])
                    return {'events': selected[index:index + 1], 'nextCursor': str(index + 1) if index + 1 < len(selected) else '', 'coverage': {'gaps': []}}
                first, last = query['from'][0], query['to'][0]
                return {'from': first, 'to': last, 'nodes': [{
                    'node': NODE, 'meter': 'meter', 'sourceIdentity': {'metric': 'watts'},
                    'meterJoules': (analysis.ns(last) - analysis.ns(first)) / 1e9 * 13,
                    'meterComplete': True, 'samples': 10, 'gapSeconds': 0, 'gapFraction': 0, 'maximumSampleGapSeconds': 1}]}
            measurement = {'nodes': [NODE], 'series': 'watts', 'meters': [{'id': 'meter', 'sourceIdentity': '{"metric":"watts"}'}]}
            with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(env)), patch.object(archive, '_collect_streams'), patch.object(
                    archive.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(archive.time, 'time_ns', return_value=analysis.ns(stamp(end)) + 180_000_000_000):
                manifest = archive.collect(folder, {'startUTC': stamp(start), 'endUTC': stamp(end), 'drainedUTC': stamp(end)},
                    {'powerMeasurement': measurement, 'energyCoverage': {'maxGapFraction': .1, 'maxSingleGapSeconds': 2}}, SimpleNamespace(api=api), budget)
            return evidence_io.load(folder / 'scheduler-decisions.json'), evidence_io.load(folder / 'prediction-power.json'), calls, manifest

    def test_judge_passes_only_id_from_realistic_stage_object(self):
        stage = {'id': 'workload', 'seconds': 4, 'parts': [{'id': 'load', 'seconds': 4}],
                 'traces': {'model': {'offsetsNS': list(range(20000)), 'offered': 20000}}}
        bound = {'config': {'workloads': [], 'evaluation': {}, 'collection': {'powerMeasurement': {'nodes': [NODE]}}}, 'limitations': []}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_io.save(root / 'phase.json', {'stage': stage, 'startUTC': stamp(2), 'endUTC': stamp(6)})
            for name in ('power', 'power-drain-inclusive'):
                evidence_io.save(root / (name + '.json'), power(stamp(2), stamp(6)))
            evidence_io.save(root / 'scheduler-decisions.json', fixture())
            with patch.object(evidence_io, 'load_binding', return_value=bound), patch.object(evidence_io, 'module', return_value=SimpleNamespace(iter_request_indexes=lambda *a, **k: {})), patch.object(judgment, 'reduce_requests', return_value={}):
                result = judgment.judge(root / 'binding.json', root)
            prediction = result['predictionAnalysis']
            self.assertEqual(prediction['nodes'][0]['stage'], 'workload')
            self.assertLess(len(json.dumps(prediction)), 4000)

    def test_bridge_and_post_remove_wait_use_only_matching_phase(self):
        observations = fixture(True)
        decision = observations['events'][0]['decision']
        plan = decision['capacity']['plans'][0]
        plan['idleCosts']['before']['state'] = ['old']
        plan['idleCosts']['mid']['state'] = ['old', 'new']
        plan['idleCosts']['after']['state'] = ['new']
        decision['target']['actions'] = [
            {'id': 'prepare', 'kind': 'prepare', 'node': NODE, 'dependsOn': []},
            {'id': 'route', 'kind': 'route', 'node': NODE, 'dependsOn': ['prepare']},
            {'id': 'stop', 'kind': 'stop', 'node': NODE, 'dependsOn': ['route']},
            {'id': 'remove', 'kind': 'remove', 'node': NODE, 'dependsOn': ['stop']}]
        observations['events'] = observations['events'][:1]
        for action, start, end in [('prepare', 3, 4), ('route', 4, 5), ('stop', 5, 6), ('remove', 7, 8)]:
            observations['events'].append({'at': stamp(end), 'action': {'id': action, 'kind': action, 'node': NODE, 'phase': 'complete', 'startedAt': stamp(start), 'completedAt': stamp(end)}})
        actual = analysis.receipts(observations)
        self.assertEqual(analysis.phase(decision, plan, analysis.ns(stamp(2)), actual)[0], 'before')
        self.assertEqual(analysis.phase(decision, plan, analysis.ns(stamp(4)), actual)[0], 'mid')
        for at in (5, 6):
            self.assertIsNone(analysis.phase(decision, plan, analysis.ns(stamp(at)), actual)[0])
        self.assertEqual(analysis.phase(decision, plan, analysis.ns(stamp(8)), actual)[0], 'after')

    def test_slo_recovery_and_undecidable_transitions_have_no_energy_verdict(self):
        for status, comparison, kind in [('Overload', 'decidable', 'slo-recovery'), ('Feasible', 'undecidable', 'transition')]:
            observations = fixture(True)
            decision = observations['events'][0]['decision']
            decision['keep']['status'] = status
            decision['capacity']['energyComparison'] = {'status': comparison, 'missingCellRefs': [0]}
            decision['capacity']['missingCells'] = [{'node': NODE, 'phase': 'after', 'required': 'exact idle'}]
            captured, powers, _, _ = self.capture(observations['events'], start=1)
            frozen = captured['events'][0]['decision']
            self.assertEqual(frozen['keep']['status'], status)
            self.assertEqual(frozen['capacity']['energyComparison'], decision['capacity']['energyComparison'])
            self.assertEqual(frozen['capacity']['missingCells'], decision['capacity']['missingCells'])
            result = analysis.analyze('stage', stamp(1), stamp(6), captured, powers)
            self.assertEqual(result['acceptance'][0]['kind'], kind)
            self.assertEqual(result['acceptance'][0]['verdict'], 'NOT_APPLICABLE')

    def test_capture_predecessor_or_report_unknown_leading_interval(self):
        prior = fixture()['events'][0]
        later = copy.deepcopy(prior)
        later['at'] = later['decision']['at'] = stamp(4)
        captured, powers, _, _ = self.capture([later], prior=prior)
        result = analysis.analyze('stage', stamp(2), stamp(6), captured, powers)
        self.assertEqual([(row['from'], row['to']) for row in result['nodes']], [(stamp(2), stamp(4)), (stamp(4), stamp(6))])
        self.assertEqual(result['nodes'][0]['decisionAt'], stamp(1))
        captured, powers, _, _ = self.capture([later])
        result = analysis.analyze('stage', stamp(2), stamp(6), captured, powers)
        self.assertEqual(result['nodes'][0]['from'], stamp(2))
        self.assertEqual(result['nodes'][0]['to'], stamp(4))
        self.assertEqual(result['nodes'][0]['status'], 'UNKNOWN')

    def test_action_seconds_do_not_fall_back_without_exact_cost(self):
        observations = fixture(True)
        action = observations['events'][0]['decision']['target']['actions'][0]
        action['expectedSeconds'] = 999
        action['expectedSource'] = {'statistic': 'transferred'}
        result = analysis.analyze('stage', stamp(2), stamp(6), observations, [])
        self.assertIsNone(result['actions'][0]['predictedSeconds'])

    def test_ninety_decisions_dedupe_windows_and_share_deadline(self):
        events = []
        for index in range(90):
            event = copy.deepcopy(fixture()['events'][0])
            event['at'] = event['decision']['at'] = analysis.stamp(analysis.ns(stamp(0)) + index * 1_000_000_000)
            events.append(event)
        # Helper's stage end is RFC3339 seconds; 90-second stage crosses a minute.
        with patch(__name__ + '.stamp', side_effect=lambda second: analysis.stamp(analysis.ns('2026-11-12T00:00:00Z') + second * 1_000_000_000)):
            _, _, calls, manifest = self.capture(events, start=0, end=90, elapsed_per_call=.01)
            power_calls = [row for row in calls if '/power?' in row[0]]
            self.assertEqual(len(power_calls), 91)
            self.assertEqual(len({row[0] for row in power_calls}), 91)
            self.assertEqual(manifest['powerQueries'], 91)
            self.assertTrue(all(timeout <= 300 - elapsed for _, timeout, elapsed in calls))
            _, _, limited, _ = self.capture(events, start=0, end=90, elapsed_per_call=1, budget=3)
            self.assertLessEqual(len(limited), 3)
