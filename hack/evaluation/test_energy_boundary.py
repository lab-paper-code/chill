"""Stage energy reports preserve node identity, coverage and actual intervals."""
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import archive
import evidence_io
import judgment
import tables


START = '2026-11-12T00:00:00Z'
END = '2026-11-12T00:00:12.500000001Z'
NODES = [{'name': name, 'uid': name + '-uid'} for name in ('worker', 'genesis1', 'orin-nano')]


def scheduler_events(sets=None):
    sets = sets if sets is not None else [NODES[:1], NODES[:1]]
    return [{'at': '2026-11-12T00:00:0' + str(i + 1) + 'Z',
             'decision': {'at': '2026-11-12T00:00:0' + str(i + 1) + 'Z',
                          'search': {'comparedNodes': nodes}}} for i, nodes in enumerate(sets)]


def power():
    return {'from': START, 'to': END, 'complete': True, 'energyStatus': 'MEASURED',
            'wholePoolJoules': 175, 'nodes': [
                {'node': node, 'joules': joules, 'meterComplete': True, 'policyMet': True}
                for node, joules in zip(NODES, (100, 50, 25))], 'reasons': []}


class EnergyBoundaryTests(unittest.TestCase):
    def report(self, measured=None, events=None, declared=None, errors=None):
        measured = power() if measured is None else measured
        events = scheduler_events() if events is None else events
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_io.save(root / 'binding.json', {'schema': 'e5-binding.v4',
                'config': {'workloads': [], 'evaluation': {}, 'collection': {'powerMeasurement': {'nodes': declared or NODES}}},
                'limitations': []})
            evidence_io.save(root / 'phase.json', {'stage': {}, 'startUTC': START, 'endUTC': END})
            evidence_io.save(root / 'power.json', measured)
            evidence_io.save(root / 'power-drain-inclusive.json', measured)
            evidence_io.save(root / 'scheduler-decisions.json', {'from': START, 'to': END, 'events': events,
                'complete': not errors, 'errors': errors or []})
            original = evidence_io.module
            def modules(name, path):
                return SimpleNamespace(iter_request_indexes=lambda *args, **kwargs: {}) if name == 'evaluation_raw' else original(name, path)
            with patch.object(evidence_io, 'module', side_effect=modules):
                result = judgment.judge(root / 'binding.json', root)
            boundary = result.get('energyBoundary')
            # Exercise the publication-facing renderer, including stages without request rows.
            text = tables.render(root, evaluation={'schema': 'e5-summary.v5', 'status': 'MEASURED_DESCRIPTIVE',
                'stages': [{'id': 'workload', 'kpis': {'requests': {}, 'power': measured, 'energyBoundary': boundary}}]})
            return boundary, text

    def test_complete_stage_uses_actual_interval_for_all_sets(self):
        result, text = self.report()
        self.assertIsNotNone(result, text)
        rows = result['rows']
        self.assertEqual([row['scope'] for row in rows], ['compared', 'non-compared', 'non-compared', 'whole-pool'])
        self.assertEqual([row['joules'] for row in rows], [100, 50, 25, 175])
        self.assertTrue(all(row['status'] == 'MEASURED' for row in rows))
        for row in rows:
            self.assertEqual(row['averageWatts'], row['joules'] / 12.500000001)
        self.assertIn('workload | compared | worker@worker-uid | 100 | ', text)
        self.assertIn('whole-pool', text)
        self.assertEqual(result['comparedSetStatus'], 'CONSTANT')

    def test_partial_compared_member_preserves_other_complete_sets(self):
        for field in ('meterComplete', 'policyMet'):
            with self.subTest(field=field):
                measured = power()
                measured['nodes'][0][field] = False
                measured.update(complete=False, energyStatus='PARTIAL', wholePoolJoules=None)
                result, text = self.report(measured)
                self.assertIsNotNone(result, text)
                rows = result['rows']
                self.assertEqual(rows[0]['status'], 'PARTIAL')
                self.assertIsNone(rows[0]['joules'])
                self.assertIsNone(rows[0]['averageWatts'])
                self.assertEqual([row['joules'] for row in rows[1:]], [50, 25, None])
                self.assertIn('UNKNOWN | UNKNOWN | PARTIAL', text)
                self.assertIn(field, text)

    def test_compared_set_variation_uses_union_and_lists_decisions(self):
        events = scheduler_events([NODES[:1], NODES[1:2]])
        result, text = self.report(events=events)
        self.assertIsNotNone(result, text)
        self.assertEqual(result['comparedSetStatus'], 'VARYING')
        self.assertEqual(result['rows'][0]['nodes'], NODES[1:2] + NODES[:1])
        self.assertEqual(result['rows'][0]['joules'], 150)
        self.assertEqual(len(result['comparedSets']), 2)
        self.assertEqual(result['comparedSets'][1]['decisionAts'], [events[1]['at']])
        self.assertIn('VARYING', text)
        self.assertIn('2026-11-12T00:00:02Z', text)
        self.assertIn('orin-nano@orin-nano-uid', text)

    def test_zero_joules_remains_measured(self):
        measured = power()
        for node in measured['nodes']:
            node['joules'] = 0
        measured['wholePoolJoules'] = 0
        result, text = self.report(measured)
        self.assertIsNotNone(result, text)
        self.assertTrue(all(row['joules'] == 0 and row['averageWatts'] == 0 and row['status'] == 'MEASURED'
                            for row in result['rows']))
        self.assertNotIn('UNKNOWN', text)

    def test_recreated_node_is_missing_by_uid(self):
        other = {**NODES[0], 'uid': 'new-worker'}
        result, text = self.report(events=scheduler_events([[other]]))
        self.assertIsNotNone(result, text)
        self.assertIsNone(result['rows'][0]['joules'])
        self.assertIn('member missing: worker@new-worker', text)
        self.assertEqual(result['rows'][-1]['joules'], 175)

    def test_missing_declared_non_compared_member_stays_unknown(self):
        missing = {'name': 'absent', 'uid': 'absent-uid'}
        measured = power()
        measured.update(complete=False, energyStatus='PARTIAL', wholePoolJoules=None)
        result, text = self.report(measured, declared=NODES + [missing])
        self.assertIsNotNone(result, text)
        row = next(row for row in result['rows'] if row['nodes'] == [missing])
        self.assertIsNone(row['joules'])
        self.assertIn('member missing: absent@absent-uid', text)
        self.assertEqual(result['rows'][0]['joules'], 100)

    def test_missing_decisions_never_imply_empty_comparison(self):
        for events, errors in (([], None), (scheduler_events(), ['scheduler coverage gap']),
                               ([{'at': START, 'decision': {'at': START, 'search': {}}}], None)):
            with self.subTest(events=events, errors=errors):
                result, text = self.report(events=events, errors=errors)
                self.assertIsNotNone(result, text)
                self.assertEqual(result['comparedSetStatus'], 'UNAVAILABLE')
                self.assertIsNone(result['rows'][0]['joules'])
                self.assertEqual(result['rows'][-1]['joules'], 175)
                self.assertIn('UNKNOWN', text)


    def test_renderer_keeps_meter_facts_when_decision_boundary_is_unavailable(self):
        measured = power()
        text = tables.render('/tmp', evaluation={'schema': 'e5-summary.v5', 'status': 'MEASURED_DESCRIPTIVE',
            'stages': [{'id': 'workload', 'kpis': {'requests': {}, 'power': measured}}]})
        self.assertIn('compared |  | UNKNOWN | UNKNOWN | UNAVAILABLE', text)
        self.assertIn('unclassified | genesis1@genesis1-uid | 50 | ', text)
        self.assertIn(' | 175 | ', text)

    def test_collected_decision_pages_reach_stage_report(self):
        self.collected_decision_pages_reach_stage_report()

    def test_raw_collection_timeout_does_not_prevent_decision_capture(self):
        self.collected_decision_pages_reach_stage_report(raw_exhausted=True)

    def collected_decision_pages_reach_stage_report(self, raw_exhausted=False):
        import os
        import urllib.parse
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / 'measured'
            folder.mkdir()
            env = root / 'environment.json'
            evidence_io.save(env, {'paths': {'campaign': str(root)}})
            events = scheduler_events([NODES[:1], NODES[1:2]])
            measurement = {'nodes': NODES, 'series': 'watts',
                'meters': [{'id': node['uid'], 'sourceIdentity': '{"metric":"watts"}'} for node in NODES]}
            window = {'from': START, 'to': END, 'nodes': [
                {'node': node, 'meter': node['uid'], 'sourceIdentity': {'metric': 'watts'},
                 'meterJoules': joules, 'meterComplete': True, 'samples': 10, 'gapSeconds': 0,
                 'gapFraction': 0, 'maximumSampleGapSeconds': 1}
                for node, joules in zip(NODES, (100, 50, 25))]}
            now = [100]
            def collect_streams(*args):
                if raw_exhausted:
                    now[0] = 104
                    raise TimeoutError('raw deadline exhausted')
            def api(method, path, **kwargs):
                self.assertEqual(kwargs['timeout'], 3)
                if path.startswith('/api/v1/observations/power?'):
                    return window
                self.assertTrue(path.startswith('/api/v1/observations/scheduler?'), path)
                query = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
                self.assertEqual(query['filter'], ['decision'])
                self.assertEqual(query['from'], [START])
                self.assertEqual(query['to'], [END])
                index = 1 if query.get('cursor') else 0
                return {'events': [events[index]], 'nextCursor': '1' if index == 0 else '',
                        'coverage': {'gaps': []}}
            collection = {'powerMeasurement': measurement, 'maximumDurationSeconds': 3,
                          'energyCoverage': {'maxGapFraction': .1, 'maxSingleGapSeconds': 2}}
            with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(env)), patch.object(
                    archive, '_collect_streams', side_effect=collect_streams), patch.object(
                    archive.time, 'monotonic', side_effect=lambda: now[0]), patch.object(
                    archive.time, 'time_ns', return_value=evidence_io.timestamp_ns(END) + 180_000_000_000):
                archive.collect(folder, {'startUTC': START, 'endUTC': END, 'drainedUTC': END}, collection, SimpleNamespace(api=api), 5)
            if raw_exhausted:
                raw_index = evidence_io.load(folder / 'raw-source-index.json')
                self.assertFalse(raw_index['complete'])
                self.assertIn('raw deadline exhausted', raw_index['errors'])
            decisions = evidence_io.load(folder / 'scheduler-decisions.json')
            result = judgment.energy_boundary(evidence_io.load(folder / 'power.json'), decisions, NODES)
            self.assertEqual(result['comparedSetStatus'], 'VARYING')
            self.assertEqual(result['rows'][0]['joules'], 150)

if __name__ == '__main__':
    unittest.main()
