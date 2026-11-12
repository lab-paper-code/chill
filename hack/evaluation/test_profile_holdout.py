"""Synthetic raw evidence exercises C1(a), including the real Go derivation bridge."""
import copy
import datetime as dt
import os
import unittest
from unittest import mock

import profile_holdout as analysis


def stamp(seconds):
    return (dt.datetime(2026, 11, 13, tzinfo=dt.timezone.utc) +
            dt.timedelta(seconds=seconds)).isoformat().replace('+00:00', 'Z')


def raw_fixture(trial, service_ms=10):
    batch = 2
    phases = []
    batch_id = 0
    for index, kind in enumerate(('sparse', 'saturation', 'utilization', 'utilization')):
        start = index * 10
        native, requests = [], []
        for j in range(4):
            batch_id += 1
            at = start + 1 + j * (.012 if kind == 'saturation' else 1)
            records = [f'{trial}-{batch_id}-{member}' for member in range(batch)]
            service = service_ms / 1000
            gap = None if batch_id == 1 else (2_000_000 if kind == 'saturation' else 990_000_000)
            native.append({'batchID': batch_id, 'batchSize': batch, 'recordIDs': records, 'requestIDs': records,
                           'units': [1, 1], 'trialID': trial, 'serviceDurationNanos': service_ms * 1_000_000,
                           'precedingIdleNanos': gap, 'runnerServiceMs': service_ms,
                           'backendCallMs': service_ms, 'nativeRequestMs': service_ms + 1,
                           'preprocessMs': 0, 'postprocessMs': 0,
                           'startedAt': stamp(at), 'finishedAt': stamp(at + service),
                           'earliestEnqueuedAt': stamp(at - .02), 'latestEnqueuedAt': stamp(at - .02)})
            for member, record in enumerate(records):
                arrival = at - .002 + member * .001
                requests.append({'requestID': record, 'recordID': record, 'arrivalAt': stamp(arrival),
                                 'sentAt': stamp(arrival), 'completedAt': stamp(at + service),
                                 'latencyMs': service_ms + 2 - member, 'response': {}})
        phases.append({'id': f'p{index}', 'kind': kind, 'utilization': .3 * (index - 1) if index > 1 else 0,
                       'arrivalRatePerSecond': .8, 'measurementStartedAt': stamp(start),
                       'measurementEndedAt': stamp(start + 10),
                       'measurementElapsedSecondsMonotonic': 10, 'completedInputs': 8,
                       'driverGC': {'cycles': 0, 'pauseTotalMilliseconds': 0, 'maxPauseMilliseconds': 0},
                       'latencyMs': [service_ms + 2] * 4, 'nativeSamples': native, 'requests': requests})
        if kind != 'saturation':
            exposure = 3.999
            phases[-1].update(scheduledInputs=8, sentInputs=8, arrivalSeed=f'{trial}/p{index}',
                              issueElapsedSecondsMonotonic=exposure, achievedArrivalRatePerSecond=8 / exposure,
                              issueLagMs=dict(count=8, min=0, mean=0, p50=0, p95=0, p99=0, max=0))
        else:
            phases[-1].pop('arrivalRatePerSecond')
            phases[-1]['workers'] = 4
    return {'schemaVersion': 'chill.dacs.io/profiling-raw-measurement.v1alpha13', 'podUID': trial,
            'runConfig': {'schemaVersion': 'chill.dacs.io/profiling-run-config.v1alpha21',
                          'measurement': {'batch': batch, 'sloPercentile': 97, 'sloTargetLatencyNanos': 1_000_000_000,
                                          'operatingPoints': {'requestLifetimeSeconds': 60,
                                                              'deliveryMarginMilliseconds': 0}}},
            'runtime': {'schemaVersion': 'chill.dacs.io/batch-characterization.v1alpha13', 'runtimeVersion': 'ortcpu-v1',
                        'measurementStartedAt': stamp(0), 'measurementEndedAt': stamp(40), 'phases': phases},
            'metrics': {'samples': [{'observedAt': stamp(i), 'watts': 14} for i in range(41)]}}


def idle_fixture(trial='idle'):
    return {'schemaVersion': 'chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha8', 'podUID': trial,
            'runtime': {'runtimeVersion': 'ortcpu-v1', 'measurementStartedAt': stamp(0), 'measurementEndedAt': stamp(10),
                        'inferenceCountAfterModelLoad': 0, 'modelResidentAtEnd': True},
            'metrics': {'samples': [{'observedAt': stamp(i), 'watts': 10} for i in range(11)]}}


class HoldoutTests(unittest.TestCase):
    def test_raw_projection_and_phase_folds_do_not_leak(self):
        runs = [raw_fixture('a'), raw_fixture('b')]
        folds = analysis.folds(runs)
        self.assertEqual([fold['kind'] for fold in folds], ['phase', 'phase', 'trial', 'trial'])
        first = folds[0]
        training = analysis.training_trials(runs, first, [0, 1])
        self.assertTrue(all([p['id'] for p in t['phases']] == ['p0', 'p1', 'p3'] for t in training))
        heldout_ids = {s['batchID'] for s in runs[0]['runtime']['phases'][2]['nativeSamples']}
        self.assertFalse(heldout_ids.intersection(s['batchID'] for s in training[0]['nativeSamples']))
        self.assertEqual(training[0]['nativeSamples'][0]['precedingIdleNanos'], None)
        self.assertEqual(analysis.training_trials(runs, folds[2], [1])[0]['trialID'], 'b')

    def test_cluster_bootstrap_never_resamples_requests(self):
        draws = analysis.bootstrap_indices([0, 1, 2], 10, 7)
        self.assertEqual(len(draws), 10)
        self.assertTrue(all(len(draw) == 3 and set(draw) <= {0, 1, 2} for draw in draws))
        self.assertIsNone(analysis.interval([1, 2], independent_trials=1, confidence=.95))
        self.assertEqual(analysis.interval([1, 2, 3, 4], independent_trials=2, confidence=.95), [1, 4])

    def test_power_stabilization_uses_entire_observed_suffix(self):
        samples = [{'observedAt': stamp(i), 'watts': v}
                   for i, v in enumerate([20, 10, 10, 20, 10, 10, 10, 10, 10])]
        result = analysis.stabilization(samples, stamp(0), 0, 2, 2)
        self.assertEqual(result['stabilizationSeconds'], 6)
        self.assertEqual(result['finalIdleWatts'], 10)
        self.assertEqual(result['observedThrough'], stamp(8))
        self.assertIsNone(analysis.stabilization(samples[:2], stamp(0), 0, 2, 2)['stabilizationSeconds'])

    def test_invalid_raw_fails_before_derivation(self):
        bad = raw_fixture('a')
        bad['runtime']['phases'][2]['requests'].pop()
        with self.assertRaisesRegex(ValueError, 'request count'):
            analysis.validate_runs([bad])
        bad = raw_fixture('a')
        bad['schemaVersion'] = 'old'
        with self.assertRaisesRegex(ValueError, 'v1alpha13'):
            analysis.validate_runs([bad])
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            analysis.validate_runs([raw_fixture('a'), raw_fixture('a')])

    def test_poisson_coverage_seeds_and_gc_are_required(self):
        for field, value, message in (
                ('sentInputs', 7, 'counts'), ('scheduledInputs', 9, 'counts'),
                ('arrivalSeed', 'other/p0', 'arrivalSeed'),
                ('issueElapsedSecondsMonotonic', 11, 'drain'),
                ('achievedArrivalRatePerSecond', .8, 'achieved rate'),
                ('driverGC', {'cycles': 0, 'pauseTotalMilliseconds': 1, 'maxPauseMilliseconds': 1}, 'driverGC')):
            with self.subTest(field=field):
                raw = raw_fixture('a')
                raw['runtime']['phases'][0][field] = value
                with self.assertRaisesRegex(ValueError, message):
                    analysis.validate_runs([raw])
        runs = [raw_fixture('a'), raw_fixture('b')]
        analysis.validate_runs(runs)
        seeds = [p['arrivalSeed'] for raw in runs for p in raw['runtime']['phases'] if p['kind'] != 'saturation']
        self.assertEqual(len(seeds), len(set(seeds)))

    def test_arrival_rate_ignores_schedule_lag_target_and_drain(self):
        raw = raw_fixture('a')
        phase = raw['runtime']['phases'][2]
        for request in phase['requests']:
            request['sentAt'] = stamp(25)
            request['completedAt'] = stamp(26)
        phase['arrivalRatePerSecond'] = 100
        phase['measurementElapsedSecondsMonotonic'] = 100
        analysis.validate_runs([raw])
        metrics = analysis.phase_metrics([raw], {'profile': {'idleWatts': 10, 'energyPerInputJoules': 5}},
                                         [0], {('a', 'p2'): 14}, {'kind': 'phase', 'id': 'p2'})
        self.assertAlmostEqual(metrics['p2']['power']['sentRatePerSecond'], 8 / 3.999)

    def test_artifact_image_changes_cannot_share_active_or_idle_evidence(self):
        active = raw_fixture('a')
        active['runConfig']['images'] = {'runtime': 'runtime-image', 'artifact': 'artifact-a'}
        other = copy.deepcopy(active)
        other['podUID'] = 'b'
        other['runConfig']['images']['artifact'] = 'artifact-b'
        with self.assertRaisesRegex(ValueError, 'artifact'):
            analysis.validate_runs([active, other])
        idle = idle_fixture()
        idle['runConfig'] = {'images': {'runtime': 'runtime-image', 'artifact': 'artifact-b'}}
        bridge = mock.Mock()
        with self.assertRaisesRegex(ValueError, 'artifact'):
            analysis.analyze([active], [idle], bridge, 3, 1, .95, 0, 2, 2, {})
        bridge.call.assert_not_called()

    def test_observed_runtime_versions_cannot_share_active_or_idle_evidence(self):
        active, other = raw_fixture('a'), raw_fixture('b')
        for raw in (active, other):
            raw['runConfig']['images'] = {'runtime': 'runtime:tag', 'artifact': 'artifact:tag'}
        other['runtime']['runtimeVersion'] = 'ortcpu-v2'
        with self.assertRaisesRegex(ValueError, 'runtimeVersion'):
            analysis.validate_runs([active, other])
        idle = idle_fixture()
        idle['runConfig'] = copy.deepcopy(active['runConfig'])
        idle['runtime']['runtimeVersion'] = 'ortcpu-v2'
        bridge = mock.Mock()
        with self.assertRaisesRegex(ValueError, 'runtimeVersion'):
            analysis.analyze([active], [idle], bridge, 3, 1, .95, 0, 2, 2, {})
        bridge.call.assert_not_called()

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_phase_bootstrap_pairs_training_and_heldout_physical_trial_draws(self):
        runs = [raw_fixture('a'), raw_fixture('b'), raw_fixture('c')]
        repetitions = 4
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge, \
                mock.patch.object(analysis, 'evaluate_fold', wraps=analysis.evaluate_fold) as evaluate, \
                mock.patch.object(analysis, 'phase_metrics', wraps=analysis.phase_metrics) as metrics, \
                mock.patch.object(analysis, 'bootstrap_indices', wraps=analysis.bootstrap_indices) as bootstrap:
            analysis.analyze(runs, [idle_fixture()], bridge, repetitions, 17, .95, 0, 2, 2, {})
        for fold_index, fold in enumerate(analysis.folds(runs)):
            for replicate in range(1, repetitions + 1):
                call_index = fold_index * (repetitions + 1) + replicate
                training = evaluate.call_args_list[call_index][0][4]
                heldout = metrics.call_args_list[call_index][0][2]
                if fold['kind'] == 'phase':
                    self.assertEqual(training, heldout)
                else:
                    self.assertTrue(set(training).isdisjoint(heldout))
        # Phase folds draw active trials once plus idle independently; trial
        # folds draw their disjoint training/held-out populations independently.
        self.assertEqual(bootstrap.call_count, 2 * 2 + 3 * 3)

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_synthetic_raw_real_derivation_without_product_replay(self):
        runs = [raw_fixture('a'), raw_fixture('b'), raw_fixture('c')]
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [idle_fixture()], bridge, 12, 17, .95, 0, 2, 2, {})
        self.assertEqual(len(report['folds']), 5)
        first = report['folds'][0]
        self.assertEqual(first['profile']['batch'], 2)
        self.assertEqual(first['profile']['idleWatts'], 10)
        self.assertAlmostEqual(first['profile']['energyPerInputJoules'], 5)
        self.assertAlmostEqual(first['profile']['dispatchOccupancyNanosPerInput'], 1_000_000)
        self.assertEqual(len(first['profile']['serviceSamples']), 36)
        self.assertEqual(report['schema'], 'profile-holdout-report.v3')
        self.assertFalse(report['replayExecuted'])
        self.assertEqual(len(report['retainedMetrics']), 3)
        self.assertEqual(len(report['removedMetrics']), 2)
        self.assertNotIn('traces', first)
        self.assertNotIn('latencyMS', first['phases'][0])
        self.assertNotIn('serviceMS', first['phases'][0])
        self.assertEqual(first['phases'][0]['power']['measuredWatts'], 14)
        expected = 10 + 5 * 8 / 3.999
        self.assertAlmostEqual(first['phases'][0]['power']['sentRatePerSecond'], 8 / 3.999)
        self.assertAlmostEqual(first['phases'][0]['power']['predictedWatts'], expected)
        for bound in first['phases'][0]['power']['predictedCIWatts']:
            self.assertAlmostEqual(bound, expected)
        self.assertAlmostEqual(first['phasePower'][0]['sentRatePerSecond'], 8 / 3.999)
        self.assertIsNone(first['phasePower'][1]['sentRatePerSecond'])
        self.assertIsNone(first['phasePower'][1]['predictedWatts'])
        self.assertEqual(len(first['phasePower']), 12)
        self.assertIsNone(report['folds'][2]['phases'][0]['power']['measuredCIWatts'])
        self.assertAlmostEqual(first['saturation'][0]['measuredCycleMS'], 12)
        self.assertAlmostEqual(first['saturation'][0]['predictedCycleMS'], 12)
        self.assertEqual(report['postLoadPower'][0]['reason'], 'no post-load samples')
        changed = copy.deepcopy(runs)
        for phase in changed[0]['runtime']['phases']:
            if phase['kind'] == 'utilization':
                for sample in phase['nativeSamples']:
                    sample['serviceDurationNanos'] = 20_000_000
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            fold = analysis.folds(changed)[2]
            one = analysis.evaluate_fold(changed, [idle_fixture()], bridge, fold, [1, 2])
        self.assertEqual(one['profile']['serviceSamples'][0]['serviceDurationNanos'], 10_000_000)

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_single_trial_and_missing_holdout_epi_are_reported(self):
        raw = raw_fixture('only')
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze([raw], [idle_fixture()], bridge, 3, 1, .95, 0, 2, 2, {})
        self.assertEqual(len(report['folds']), 2)
        self.assertIsNone(report['folds'][0]['phases'][0]['power']['predictedCIWatts'])
        raw['runtime']['phases'].pop()
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze([raw], [idle_fixture()], bridge, 3, 1, .95, 0, 2, 2, {})
        self.assertIsNone(report['folds'][0]['profile'])
        self.assertIn('no EPI training population', report['folds'][0]['unavailableReason'])

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_phase_poison_is_excluded_and_trial_variance_reaches_ci(self):
        runs = [raw_fixture('a', 10), raw_fixture('b', 11), raw_fixture('c', 12)]
        for index, raw in enumerate(runs):
            for sample in raw['metrics']['samples']:
                sample['watts'] += index
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [idle_fixture()], bridge, 30, 13, .95, 0, 2, 2, {})
            fold = analysis.folds(runs)[0]
            before = analysis.evaluate_fold(runs, [idle_fixture()], bridge, fold, [0, 1, 2])
            for raw in runs:
                for sample in raw['runtime']['phases'][2]['nativeSamples']:
                    sample['serviceDurationNanos'] = 50_000_000
            after = analysis.evaluate_fold(runs, [idle_fixture()], bridge, fold, [0, 1, 2])
        self.assertEqual(before['profile'], after['profile'])
        low, high = report['folds'][0]['phases'][0]['power']['predictedCIWatts']
        self.assertLess(low, high)


if __name__ == '__main__':
    unittest.main()
