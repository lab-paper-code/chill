"""v1alpha7-shaped synthetic prediction/measurement joins (no live evidence)."""
import copy
import unittest

import prediction_analysis as analysis

NODE = {'name': 'worker', 'uid': 'worker-uid'}


def stamp(second):
    return '2026-11-12T00:00:%02dZ' % second


def fixture(transition=False, gain=100):
    costs = {phase: {'source': 'exact', 'watts': watts, 'references': ['idle-' + phase],
              'powerMode': 'mode', 'state': [], 'requestWatts': 2, 'requestReferences': ['profile']}
             for phase, watts in [('before', 10), ('mid', 20), ('after', 30)]}
    plan = {'node': NODE, 'key': 'selected' if transition else 'keep', 'idleCosts': costs,
            'preNanos': 0, 'postNanos': 0, 'actionCosts': []}
    target = {'id': 'target', 'keep': not transition, 'expectedJoules': 100,
              'actions': [{'id': 'route', 'kind': 'route', 'node': NODE, 'dependsOn': []}] if transition else []}
    decision = {'at': stamp(1), 'keep': {'expectedJoules': 100 + gain, 'status': 'Feasible'}, 'target': target,
                'search': {'comparedNodes': [NODE]}, 'capacity': {'routeNanos': 2_000_000_000, 'plans': [plan],
                                                                'energyComparison': {'status': 'decidable'}}}
    events = [{'at': stamp(1), 'decision': decision}]
    if transition:
        events.append({'at': stamp(3), 'action': {'id': 'route', 'kind': 'route', 'node': NODE,
                       'phase': 'complete', 'startedAt': stamp(1), 'completedAt': stamp(3)}})
    return {'complete': True, 'events': events, 'errors': []}


def power(start, end, watts=13, complete=True):
    return {'from': start, 'to': end, 'nodes': [{'node': NODE, 'joules': watts * (
        analysis.ns(end) - analysis.ns(start)) / 1e9, 'meterComplete': complete, 'policyMet': True}]}


class PredictionTests(unittest.TestCase):
    def analyze(self, observations=None, start=2, end=6, watts=13, complete=True):
        observations = fixture() if observations is None else observations
        windows = analysis.windows(stamp(start), stamp(end), observations)
        powers = [power(row['from'], row['to'], watts, complete) for row in windows]
        return analysis.analyze('stage', stamp(start), stamp(end), observations, powers)

    def test_window_join_and_clipping(self):
        observations = fixture()
        later = copy.deepcopy(observations['events'][0])
        later['at'] = later['decision']['at'] = stamp(4)
        observations['events'].append(later)
        rows = self.analyze(observations)['nodes']
        self.assertEqual([(r['from'], r['to']) for r in rows], [(stamp(2), stamp(4)), (stamp(4), stamp(6))])
        self.assertEqual(rows[0]['predictedWatts'], 12)
        self.assertEqual(rows[0]['errorWatts'], -1)
        self.assertEqual(rows[0]['idle']['references'], ['idle-before'])

    def test_transition_selects_active_mid_then_after(self):
        result = self.analyze(fixture(True))
        self.assertEqual([r['phase'] for r in result['nodes']], ['mid', 'after'])
        self.assertEqual([r['predictedWatts'] for r in result['nodes']], [22, 32])
        self.assertEqual(result['actions'][0]['actualSeconds'], 2)
        self.assertIsNone(result['actions'][0]['actualJoules'])

    def test_dependency_leaves_accept_null_and_empty_lists(self):
        for dependencies in (None, []):
            for chain in (False, True):
                with self.subTest(dependencies=dependencies, chain=chain):
                    observations = fixture(True)
                    decision = observations['events'][0]['decision']
                    actions = decision['target']['actions']
                    actions[0]['dependsOn'] = ['bridge'] if chain else dependencies
                    if chain:
                        actions.extend([
                            {'id': 'bridge', 'kind': 'route', 'node': NODE,
                             'from': 'ready-subset', 'dependsOn': ['leaf']},
                            {'id': 'leaf', 'kind': 'route', 'node': NODE,
                             'from': 'ready-subset', 'dependsOn': dependencies},
                        ])
                    result = self.analyze(observations)
                    expected = self.analyze(fixture(True))
                    self.assertEqual(result['nodes'], expected['nodes'])
                    self.assertEqual(result['acceptance'], expected['acceptance'])

    def test_incomplete_meter_is_unknown_not_zero(self):
        result = self.analyze(complete=False)
        self.assertEqual(result['nodes'][0]['status'], 'UNKNOWN')
        self.assertIsNone(result['nodes'][0]['measuredWatts'])
        self.assertIsNone(result['nodes'][0]['errorWatts'])

    def test_absolute_node_error_vs_positive_gain_only(self):
        result = self.analyze(fixture(True), watts=30)
        self.assertEqual(result['acceptance'][0]['absoluteErrorJoules'], 2)
        self.assertEqual(result['acceptance'][0]['verdict'], 'PASS')
        self.assertEqual(self.analyze(fixture(True, 2), watts=30)['acceptance'][0]['verdict'], 'FAIL')
        for transition, gain in [(True, 0), (True, -1), (False, 100)]:
            result = self.analyze(fixture(transition, gain))
            self.assertEqual(result['acceptance'][0]['verdict'], 'NOT_APPLICABLE')
        self.assertEqual(self.analyze(fixture(True), complete=False)['acceptance'][0]['verdict'], 'UNKNOWN')

    def test_action_energy_uses_only_matching_measured_window(self):
        observations = fixture(True)
        target = observations['events'][0]['decision']['target']
        target['actions'].insert(0, {'id': 'prepare', 'kind': 'prepare', 'node': NODE, 'dependsOn': []})
        target['actions'][1]['dependsOn'] = ['prepare']
        plan = observations['events'][0]['decision']['capacity']['plans'][0]
        plan['actionCosts'] = [{'action': 'prepare', 'kind': 'prepare', 'seconds': 1, 'joules': 40,
                                'provenance': {'source': 'exact', 'costRef': 'operation-cost'}}]
        observations['events'].append({'at': stamp(2), 'action': {
            'id': 'prepare', 'kind': 'prepare', 'node': NODE, 'phase': 'complete',
            'startedAt': stamp(1), 'completedAt': stamp(2)}})
        windows = analysis.windows(stamp(1), stamp(6), observations)
        powers = [power(w['from'], w['to'], 30) for w in windows]
        result = analysis.analyze('stage', stamp(1), stamp(6), observations, powers)
        self.assertEqual(result['nodes'][0]['phase'], 'action:prepare')
        self.assertEqual(result['nodes'][0]['predictedWatts'], 40)
        action = next(row for row in result['actions'] if row['id'] == 'prepare')
        self.assertEqual((action['predictedSeconds'], action['actualSeconds']), (1, 1))
        self.assertEqual((action['predictedJoules'], action['actualJoules'], action['errorJoules']), (40, 30, 10))
        self.assertEqual(action['source']['costRef'], 'operation-cost')

    def test_missing_phase_receipt_and_recreated_uid_are_unknown(self):
        observations = fixture(True)
        observations['events'].pop()
        self.assertEqual(self.analyze(observations)['acceptance'][0]['verdict'], 'UNKNOWN')
        powers = [power(stamp(2), stamp(6))]
        powers[0]['nodes'][0]['node'] = {**NODE, 'uid': 'replacement'}
        result = analysis.analyze('stage', stamp(2), stamp(6), fixture(), powers)
        self.assertIsNone(result['nodes'][0]['measuredWatts'])

    def test_node_absolute_errors_do_not_cancel_each_other(self):
        observations = fixture(True)
        decision = observations['events'][0]['decision']
        second = {'name': 'second', 'uid': 'second-uid'}
        plan = copy.deepcopy(decision['capacity']['plans'][0])
        plan['node'] = second
        decision['capacity']['plans'].append(plan)
        decision['search']['comparedNodes'].append(second)
        powers = [power(w['from'], w['to'], 30) for w in analysis.windows(stamp(2), stamp(6), observations)]
        for window in powers:
            other = power(window['from'], window['to'], 24)['nodes'][0]
            other['node'] = second
            window['nodes'].append(other)
        result = analysis.analyze('stage', stamp(2), stamp(6), observations, powers)
        self.assertEqual(result['acceptance'][0]['absoluteErrorJoules'], 24)



if __name__ == '__main__':
    unittest.main()
