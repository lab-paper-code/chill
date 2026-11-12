"""Offline hydrated-page regressions; no scheduler or power HTTP calls."""
import copy
import gzip
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import prediction_analysis as analysis
from test_prediction_analysis import fixture, power, stamp, NODE


class PredictionReviewTests(unittest.TestCase):
    def archive(self, root, observations):
        path = root / 'scheduler-observations'
        path.mkdir(exist_ok=True)
        with gzip.open(path / '0000.json.gz', 'wt') as output:
            json.dump({'events': observations['events'], 'coverage': {'gaps': []}}, output)
        return analysis.load_observations(path)

    def test_report_keeps_only_stage_id_from_realistic_stage_object(self):
        stage = {'id': 'workload', 'from': stamp(2), 'to': stamp(6),
                 'traces': {'model': {'offsetsNS': list(range(20000)), 'offered': 20000}}}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'stages.json'
            path.write_text(json.dumps([stage]))
            stages = analysis.load_stages(path)
            observations = self.archive(root, fixture())
            result = analysis.offline_report(stages, observations, [power(stamp(2), stamp(6))])
            self.assertEqual(result['stages'][0]['predictionAnalysis']['nodes'][0]['stage'], 'workload')
            self.assertNotIn('traces', json.dumps(result))
            self.assertLess(len(json.dumps(result)), 4000)

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

    def test_hydration_preserves_energy_eligibility_and_selected_terms_only(self):
        for status, comparison, kind in [('Overload', 'decidable', 'slo-recovery'), ('Feasible', 'undecidable', 'transition')]:
            observations = fixture(True)
            decision = observations['events'][0]['decision']
            decision['keep']['status'] = status
            decision['capacity']['energyComparison'] = {'status': comparison, 'missingCellRefs': [0]}
            decision['capacity']['missingCells'] = [{'node': NODE, 'phase': 'after', 'required': 'exact idle'}]
            decision['capacity']['plans'].append({'node': NODE, 'key': 'unused', 'idleCosts': {'before': {'watts': 999}}})
            observations['events'][0]['snapshot'] = {'options': ['unneeded large catalog']}
            with tempfile.TemporaryDirectory() as directory:
                captured = self.archive(Path(directory), observations)
            frozen = captured['events'][0]['decision']
            self.assertEqual(frozen['keep']['status'], status)
            self.assertEqual(frozen['capacity']['energyComparison'], decision['capacity']['energyComparison'])
            self.assertEqual(frozen['capacity']['missingCells'], decision['capacity']['missingCells'])
            self.assertEqual(len(frozen['capacity']['plans']), 1)
            self.assertNotIn('snapshot', captured['events'][0])
            result = analysis.analyze('stage', stamp(1), stamp(6), captured, [])
            self.assertEqual(result['acceptance'][0]['kind'], kind)
            self.assertEqual(result['acceptance'][0]['verdict'], 'NOT_APPLICABLE')

    def test_hydrated_predecessor_or_unknown_leading_interval(self):
        prior = fixture()['events'][0]
        later = copy.deepcopy(prior)
        later['at'] = later['decision']['at'] = stamp(4)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            captured = self.archive(root, {'events': [prior, later]})
            result = analysis.analyze('stage', stamp(2), stamp(6), captured, [])
            self.assertEqual([(row['from'], row['to']) for row in result['nodes']], [(stamp(2), stamp(4)), (stamp(4), stamp(6))])
            self.assertEqual(result['nodes'][0]['decisionAt'], stamp(1))
            captured = self.archive(root, {'events': [later]})
            result = analysis.analyze('stage', stamp(2), stamp(6), captured, [])
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

    def test_ninety_decisions_use_one_cli_and_no_server_queries(self):
        observations = {'events': [], 'complete': True, 'errors': []}
        base = analysis.ns(stamp(0))
        for index in range(90):
            event = copy.deepcopy(fixture()['events'][0])
            event['at'] = event['decision']['at'] = analysis.stamp(base + index * 1_000_000_000)
            observations['events'].append(event)
        stages = [{'id': 'stage', 'from': stamp(0), 'to': analysis.stamp(base + 90_000_000_000)}]
        stages.append(dict(stages[0], id='same-interval'))
        measurement = {'nodes': [NODE], 'series': 'watts', 'meters': [{'id': 'meter', 'sourceIdentity': '{"metric":"watts"}'}]}
        collection = {'powerMeasurement': measurement, 'energyCoverage': {'maxGapFraction': .1, 'maxSingleGapSeconds': 2}}
        def cli(command, stdout, check):
            windows = json.loads(Path(command[command.index('--windows') + 1]).read_text())
            result = []
            for row in windows:
                result.append({'id': row['id'], 'power': {'from': row['from'], 'to': row['to'], 'nodes': [{
                    'node': NODE, 'meter': 'meter', 'sourceIdentity': {'metric': 'watts'}, 'meterComplete': True,
                    'meterJoules': 13, 'samples': 2, 'gapSeconds': 0, 'gapFraction': 0, 'maximumSampleGapSeconds': 1}]}})
            json.dump({'windows': result}, stdout)
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(analysis.subprocess, 'run', side_effect=cli) as invoked, patch('urllib.request.urlopen', side_effect=AssertionError('offline analysis must not query servers')):
                result = analysis.run_offline(stages, observations, collection, Path(directory), 'power-cli', 'raw', 'settings.json', 'state.json')
            self.assertEqual(invoked.call_count, 1)
            windows = json.loads((Path(directory) / 'prediction-windows.json').read_text())
            self.assertEqual(len(windows), 90)
            self.assertEqual(len(result['stages'][0]['predictionAnalysis']['nodes']), 90)
            self.assertEqual(result['stages'][0]['predictionAnalysis']['nodes'][0]['measuredWatts'], 13)

    def test_incomplete_hydrated_coverage_remains_unknown(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            observations = self.archive(root, fixture())
            path = root / 'scheduler-observations' / '0000.json.gz'
            with gzip.open(path, 'wt') as output:
                json.dump({'events': fixture()['events'], 'coverage': {'gaps': [{'reason': 'missing'}]}}, output)
            observations = analysis.load_observations(path.parent)
        result = analysis.analyze('stage', stamp(2), stamp(6), observations, [power(stamp(2), stamp(6))])
        self.assertEqual(result['nodes'][0]['status'], 'UNKNOWN')

    def test_scheduler_gaps_only_affect_overlapping_stage_and_action_windows(self):
        first = fixture(True)
        second = copy.deepcopy(first)
        decision = second['events'][0]['decision']
        second['events'][0]['at'] = decision['at'] = stamp(11)
        decision['target']['actions'][0]['id'] = 'route-b'
        receipt = second['events'][1]
        receipt['at'] = stamp(13)
        receipt['action'].update(id='route-b', startedAt=stamp(11), completedAt=stamp(13))
        stages = [{'id': 'A', 'from': stamp(2), 'to': stamp(6)},
                  {'id': 'B', 'from': stamp(12), 'to': stamp(16)}]
        for gap, affected in [
                ({'from': stamp(12), 'to': stamp(13), 'stream': 'scheduler', 'reason': 'missing B'}, True),
                ({'from': stamp(6), 'to': stamp(11), 'stream': 'scheduler', 'reason': 'between stages'}, False)]:
            with self.subTest(gap=gap), tempfile.TemporaryDirectory() as directory:
                folder = Path(directory)
                with gzip.open(folder / '0000.json.gz', 'wt') as output:
                    json.dump({'events': first['events'] + second['events'], 'coverage': {'gaps': [gap]}}, output)
                observations = analysis.load_observations(folder)
                powers = [power(row['from'], row['to'], watts=30)
                          for row in analysis.requested_windows(stages, observations)]
                reports = analysis.offline_report(stages, observations, powers)['stages']
            a, b = [stage['predictionAnalysis'] for stage in reports]
            self.assertEqual([row['status'] for row in a['nodes']], ['MEASURED', 'MEASURED'])
            self.assertEqual(a['acceptance'][0]['verdict'], 'PASS')
            self.assertEqual(a['actions'][0]['coverageStatus'], 'COMPLETE')
            self.assertEqual([row['status'] for row in b['nodes']], ['UNKNOWN' if affected else 'MEASURED', 'MEASURED'])
            self.assertEqual(b['acceptance'][0]['verdict'], 'UNKNOWN' if affected else 'PASS')
            self.assertEqual(b['actions'][0]['coverageStatus'], 'UNKNOWN' if affected else 'COMPLETE')
            self.assertEqual(observations['gaps'], [gap])
