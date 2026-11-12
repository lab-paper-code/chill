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


def raw_fixture(trial, service_ms=10, batch=2):
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
                           'units': [1] * batch, 'trialID': trial, 'serviceDurationNanos': service_ms * 1_000_000,
                           'precedingIdleNanos': gap, 'runnerServiceMs': service_ms,
                           'backendCallMs': service_ms, 'nativeRequestMs': service_ms + 1,
                           'preprocessMs': 0, 'postprocessMs': 0,
                           'startedAt': stamp(at), 'finishedAt': stamp(at + service),
                           'earliestEnqueuedAt': stamp(at - .02), 'latestEnqueuedAt': stamp(at - .02)})
            for member, record in enumerate(records):
                arrival = at - .002 + member * .001
                requests.append({'requestID': record, 'recordID': record, 'scheduledAt': stamp(arrival),
                                 'dispatchedAt': stamp(arrival), 'completedAt': stamp(at + service),
                                 'latencyMs': service_ms + 2 - member,
                                 'response': {'progress': {'nativeHTTP': {
                                     'handlerEnteredAt': stamp(arrival), 'receivedNanos': 0}}}})
        phases.append({'id': f'p{index}', 'kind': kind, 'utilization': .3 * (index - 1) if index > 1 else 0,
                       'arrivalRatePerSecond': .8, 'measurementStartedAt': stamp(start),
                       'measurementEndedAt': stamp(start + 10),
                       'measurementElapsedSecondsMonotonic': 10, 'completedInputs': 4 * batch,
                       'driverGC': {'cycles': 0, 'pauseTotalMilliseconds': 0, 'maxPauseMilliseconds': 0},
                       'latencyMs': [service_ms + 2] * 4, 'nativeSamples': native, 'requests': requests})
        exposure = (analysis.evidence_io.timestamp_ns(requests[-1]['scheduledAt']) -
                    analysis.evidence_io.timestamp_ns(stamp(start))) / 1e9
        phases[-1].update(scheduledInputs=4 * batch, sentInputs=4 * batch,
                          offeringStartedAt=stamp(start), offeringEndedAt=requests[-1]['scheduledAt'],
                          drainEndedAt=max(r['completedAt'] for r in requests),
                          issueElapsedSecondsMonotonic=exposure, achievedArrivalRatePerSecond=4 * batch / exposure)
        if kind != 'saturation':
            phases[-1].update(arrivalSeed=f'{trial}/p{index}',
                              issueLagMs=dict(count=4 * batch, min=0, mean=0, p50=0, p95=0, p99=0, max=0))
        else:
            phases[-1].pop('arrivalRatePerSecond')
            phases[-1]['workers'] = 4
    return {'schemaVersion': 'chill.dacs.io/profiling-raw-measurement.v1alpha14', 'podUID': trial,
            'runConfig': {'schemaVersion': 'chill.dacs.io/profiling-run-config.v1alpha22',
                          'actor': {'maxQueueRequests': 4},
                          'measurement': {'batch': batch, 'sloPercentile': 97, 'sloTargetLatencyNanos': 1_000_000_000,
                                          'operatingPoints': {'requestLifetimeSeconds': 60,
                                                              'deliveryMarginMilliseconds': 0}}},
            'runtime': {'schemaVersion': 'chill.dacs.io/batch-characterization.v1alpha14', 'runtimeVersion': 'ortcpu-v1',
                        'measurementStartedAt': stamp(0), 'measurementEndedAt': stamp(51), 'phases': phases,
                        'postLoad': {'startedAt': stamp(41), 'endedAt': stamp(51), 'durationSecondsMonotonic': 10,
                                     'actorPID': 42, 'modelResidentAtEnd': True,
                                     'countersBefore': {'inferences': 32}, 'countersAfter': {'inferences': 32}}},
            'metrics': {'samples': [{'observedAt': stamp(i), 'watts': 14 if i <= 40 else 10} for i in range(52)]}}


def idle_fixture(trial='idle'):
    return {'schemaVersion': 'chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha8', 'podUID': trial,
            'runtime': {'runtimeVersion': 'ortcpu-v1', 'measurementStartedAt': stamp(0), 'measurementEndedAt': stamp(10),
                        'inferenceCountAfterModelLoad': 0, 'modelResidentAtEnd': True},
            'metrics': {'samples': [{'observedAt': stamp(i), 'watts': 10} for i in range(11)]}}


class HoldoutTests(unittest.TestCase):
    def setUp(self):
        settings = {
            'observationWindowNanos': 10_000_000_000, 'comparisonPeriodNanos': 10_000_000_000,
            'controlIntervalNanos': 60_000_000_000, 'maxReplicas': 1, 'maxInstances': 1,
            'capacityWindowNanos': 10_000_000_000, 'capacityWindows': 1,
            'capacityPassingWindows': 1, 'capacityMinimumRequests': 1, 'capacityGridSteps': 1,
            'capacityBisectionWidth': .1, 'capacityTailSamples': 1, 'planSearchWorkers': 1,
            'planSearchStrategy': 'v2', 'planSearchEvaluations': 1, 'planExactFinalists': 1,
            'defaultOperationCost': {kind: {'seconds': 1} for kind in ('prepare', 'remove', 'power')},
            'defaultIdlePower': {'nodeWatts': 10},
        }
        original = analysis.GoBridge.__init__
        patcher = mock.patch.object(analysis.GoBridge, '__init__',
                                    lambda bridge, executable: original(bridge, executable, settings))
        patcher.start()
        self.addCleanup(patcher.stop)

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
        with self.assertRaisesRegex(ValueError, 'v1alpha14'):
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
            request['dispatchedAt'] = stamp(25)
            request['completedAt'] = stamp(26)
            request['latencyMs'] = (analysis.evidence_io.timestamp_ns(request['completedAt']) -
                                    analysis.evidence_io.timestamp_ns(request['scheduledAt'])) / 1e6
        phase['arrivalRatePerSecond'] = 100
        phase['measurementElapsedSecondsMonotonic'] = 100
        analysis.validate_runs([raw])
        metrics = analysis.phase_metrics([raw], {'profile': {'idleWatts': 10, 'energyPerInputJoules': 5}},
                                         [0], {('a', 'p2'): 14}, {'kind': 'phase', 'id': 'p2'})
        self.assertAlmostEqual(metrics['p2']['power']['scheduledRatePerSecond'], 8 / 3.999)

    def test_power_and_rate_share_offering_window_excluding_drain(self):
        raw = raw_fixture('a')
        phase = raw['runtime']['phases'][2]
        phase.update(completedInputs=100, scheduledInputs=100, sentInputs=100,
                     issueElapsedSecondsMonotonic=10, measurementElapsedSecondsMonotonic=20)
        metrics = analysis.phase_metrics([raw], {'profile': {'idleWatts': 10, 'energyPerInputJoules': 1}},
                                         [0], {('a', 'p2'): 20}, {'kind': 'phase', 'id': 'p2'})
        self.assertEqual(metrics['p2']['power']['scheduledRatePerSecond'], 10)
        self.assertEqual(metrics['p2']['power']['predictedWatts'], 20)
        points = [(0, 20), (10, 20), (10.000000001, 10), (20, 10)]
        self.assertAlmostEqual(analysis.time_mean(points, 0, 20), 15)
        self.assertEqual(analysis.time_mean(points, 0, 10), 20)

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_bridge_power_uses_native_clock_not_load_offering_utc(self):
        raw = raw_fixture('a')
        phase = raw['runtime']['phases'][0]
        phase.update(offeringStartedAt=stamp(1000), offeringEndedAt=stamp(1004),
                     issueElapsedSecondsMonotonic=4)
        phase['requests'][0]['response']['progress']['nativeHTTP']['handlerEnteredAt'] = stamp(0)
        for i, point in enumerate(raw['metrics']['samples']):
            if i <= 40:
                point['watts'] = 10 + i
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            result = bridge.call({'trials': analysis.training_trials([raw], {'kind': 'all'}, [0]),
                                  'idle': analysis.idle_observations([idle_fixture()])})
        power = result['trials'][0]['phasePower'][0]
        self.assertEqual(analysis.evidence_io.timestamp_ns(power['windowStart']),
                         analysis.evidence_io.timestamp_ns(analysis.measurement_offering(raw['runtime']['phases'][0])[0]))
        self.assertEqual(analysis.evidence_io.timestamp_ns(power['windowEnd']),
                         analysis.evidence_io.timestamp_ns(analysis.measurement_offering(raw['runtime']['phases'][0])[1]))
        self.assertAlmostEqual(power['meanWallWatts'], 12)
        self.assertAlmostEqual(power['scheduledRatePerSecond'], 2)

    def test_scheduler_input_uses_scheduled_arrivals_before_native_dispatch(self):
        runs = [raw_fixture('a'), raw_fixture('b')]
        phase = runs[0]['runtime']['phases'][2]
        phase['requests'][0]['scheduledAt'] = stamp(20.5)
        phase['requests'][0]['dispatchedAt'] = stamp(21)
        bridge = mock.Mock(support_runs=[], support_indices=[])
        fold = analysis.folds(runs)[0]
        analysis.evaluate_fold(runs, [idle_fixture()], bridge, fold, [1])
        request = bridge.call.call_args[0][0]
        heldout = request['heldout'][0]
        self.assertEqual(heldout['arrivals'][0], stamp(20.5))
        self.assertEqual(heldout['sloPercentile'], 97)
        self.assertTrue(all(p['id'] != phase['id'] for t in request['trials'] for p in t['phases']))
        metrics = analysis.phase_metrics(runs, {'profile': {'idleWatts': 10, 'energyPerInputJoules': 1}},
                                         [0], {('a', phase['id']): 14}, fold)
        self.assertEqual(metrics[phase['id']]['latency']['measuredNanos'], 510_000_000)

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
            analysis.analyze([active], [idle], bridge, 3, 1, .95, 0, 2, 2)
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
            analysis.analyze([active], [idle], bridge, 3, 1, .95, 0, 2, 2)
        bridge.call.assert_not_called()

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_phase_bootstrap_pairs_training_and_heldout_physical_trial_draws(self):
        runs = [raw_fixture('a'), raw_fixture('b'), raw_fixture('c')]
        repetitions = 4
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge, \
                mock.patch.object(analysis, 'evaluate_fold', wraps=analysis.evaluate_fold) as evaluate, \
                mock.patch.object(analysis, 'phase_metrics', wraps=analysis.phase_metrics) as metrics, \
                mock.patch.object(analysis, 'bootstrap_indices', wraps=analysis.bootstrap_indices) as bootstrap:
            analysis.analyze(runs, [idle_fixture()], bridge, repetitions, 17, .95, 0, 2, 2)
        for fold_index, fold in enumerate(analysis.folds(runs)):
            for replicate in range(1, repetitions + 1):
                call_index = fold_index * (repetitions + 1) + replicate
                training = evaluate.call_args_list[call_index][0][4]
                heldout = metrics.call_args_list[call_index][0][2]
                if fold['kind'] == 'phase':
                    self.assertEqual(training, heldout)
                else:
                    self.assertTrue(set(training).isdisjoint(heldout))
        # Independent loaded-idle evidence remains separate from active trial draws.
        self.assertEqual(bootstrap.call_count, 2 + 3 * 2 + len(analysis.folds(runs)))

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_synthetic_raw_real_derivation_and_scheduler(self):
        runs = [raw_fixture('a'), raw_fixture('b'), raw_fixture('c')]
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [idle_fixture()], bridge, 12, 17, .95, 0, 2, 2)
        self.assertEqual(len(report['folds']), 5)
        first = report['folds'][0]
        self.assertEqual(first['profile']['batch'], 2)
        self.assertEqual(first['profile']['idleWatts'], 10)
        self.assertAlmostEqual(first['profile']['energyPerInputJoules'], 5)
        self.assertAlmostEqual(first['profile']['dispatchOccupancyNanosPerInput'], 1_000_000)
        self.assertEqual(len(first['profile']['serviceSamples']), 36)
        self.assertEqual(report['schema'], 'profile-holdout-report.v3')
        self.assertTrue(report['replayExecuted'])
        self.assertEqual(len(report['retainedMetrics']), 5)
        self.assertNotIn('removedMetrics', report)
        self.assertNotIn('traces', first)
        self.assertIsNone(first['phases'][0]['latency']['predictedNanos'])
        self.assertTrue(first['phases'][0]['latency']['unavailableReasons'])
        self.assertEqual(first['phases'][0]['latency']['measuredNanos'], 12_000_000)
        service = first['phases'][0]['batchServiceDistribution']
        self.assertEqual(service['twoSampleTest']['statistic'], 0)
        self.assertEqual(service['twoSampleTest']['statisticCI'], [0, 0])
        self.assertEqual([row['percentile'] for row in service['quantiles']], [50, 95, 99])
        self.assertNotIn('serviceMS', first['phases'][0])
        self.assertAlmostEqual(first['phases'][0]['power']['measuredWatts'], 14)
        expected = 10 + 5 * 8 / 3.999
        self.assertAlmostEqual(first['phases'][0]['power']['scheduledRatePerSecond'], 8 / 3.999)
        self.assertAlmostEqual(first['phases'][0]['power']['predictedWatts'], expected)
        for bound in first['phases'][0]['power']['predictedCIWatts']:
            self.assertAlmostEqual(bound, expected)
        self.assertAlmostEqual(first['phasePower'][0]['scheduledRatePerSecond'], 8 / 3.999)
        self.assertAlmostEqual(first['phasePower'][1]['scheduledRatePerSecond'], 8 / 1.035)
        self.assertAlmostEqual(first['phasePower'][1]['predictedWatts'], 10 + 5 * 8 / 1.035)
        self.assertEqual(len(first['phasePower']), 12)
        self.assertIsNone(report['folds'][2]['phases'][0]['power']['measuredCIWatts'])
        self.assertAlmostEqual(first['saturation'][0]['measuredCycleMS'], 12)
        self.assertAlmostEqual(first['saturation'][0]['predictedCycleMS'], 12)
        self.assertEqual(report['postLoadPower'][0]['stabilizationSeconds'], 2)
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
    def test_scheduler_slo_estimate_uses_exported_api(self):
        runs = [raw_fixture('a', batch=1), raw_fixture('b', batch=1)]
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [idle_fixture()], bridge, 3, 17, .95, 0, 2, 2)
        latency = report['folds'][0]['phases'][0]['latency']
        self.assertGreater(latency['predictedNanos'], 0)
        self.assertEqual(latency['measuredNanos'], 12_000_000)
        self.assertIsNotNone(latency['predictedCINanos'])

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_campaign_profile_supplies_measured_single_batch_support(self):
        runs = [raw_fixture('one-a', batch=1), raw_fixture('one-b', batch=1),
                raw_fixture('two-a'), raw_fixture('two-b')]
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [idle_fixture()], bridge, 3, 17, .95, 0, 2, 2)
        self.assertEqual([r['batch'] for r in report['batches']], [1, 2])
        phase = report['batches'][1]['folds'][0]['phases'][0]
        self.assertGreater(phase['latency']['predictedNanos'], 0)
        self.assertFalse(phase['latency']['unavailableReasons'])

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_catalog_idle_is_independent_and_postload_is_diagnostic(self):
        runs = [raw_fixture('a'), raw_fixture('b')]
        separate = idle_fixture()
        for sample in separate['metrics']['samples']:
            sample['watts'] = 9
        for sample in runs[0]['metrics']['samples']:
            if sample['observedAt'] >= stamp(41):
                sample['watts'] = 11
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [separate], bridge, 2, 17, .95, 0, 2, 2)
        self.assertEqual(report['separateIdleEstimateWatts'], 9)
        heldout_a = next(f for f in report['folds'] if f['kind'] == 'trial' and f['id'] == 'a')
        self.assertEqual(heldout_a['profile']['idleWatts'], 9)
        invalid = raw_fixture('invalid')
        invalid['runtime']['postLoad']['countersAfter']['inferences'] += 1
        with self.assertRaisesRegex(ValueError, 'zero inference'):
            analysis.validate_runs([invalid])

    def test_latency_window_comparison_matches_replay_aggregation(self):
        phase = {'requests': [{'scheduledAt': stamp(at), 'completedAt': stamp(at + latency)}
                              for at, latency in ((0, .001), (.1, .1), (1, .01), (1.1, .01), (1.2, .01))]}
        estimate = {'windows': [{'windowStart': stamp(0), 'windowEnd': stamp(1)},
                                {'windowStart': stamp(1), 'windowEnd': stamp(2)}]}
        self.assertEqual(analysis.measured_replay_quantile(phase, estimate, 50), 6_400_000)
        self.assertEqual(analysis.measured_replay_quantile(phase, {}, 50), 10_000_000)

    def test_postload_uses_native_last_phase_and_total_end(self):
        raw = raw_fixture('a')
        analysis.validate_runs([raw])
        raw['runtime']['postLoad']['startedAt'] = stamp(39)
        with self.assertRaisesRegex(ValueError, 'last phase'):
            analysis.validate_runs([raw])
        raw = raw_fixture('a')
        raw['runtime']['measurementEndedAt'] = stamp(50)
        with self.assertRaisesRegex(ValueError, 'measurementEndedAt'):
            analysis.validate_runs([raw])

    def test_campaign_ignores_provenance_resource_versions(self):
        first, second = raw_fixture('a'), raw_fixture('b')
        first['runConfig']['targetNode'] = {'uid': 'node', 'resourceVersion': '1'}
        second['runConfig']['targetNode'] = {'uid': 'node', 'resourceVersion': '2'}
        self.assertEqual(analysis.execution_condition(first, True), analysis.execution_condition(second, True))
        with mock.patch.object(analysis, 'analyze_batch', return_value={}) as analyze_batch:
            analysis.analyze([first, second], [idle_fixture()], mock.Mock(), 2, 1, .95, 0, 2, 2)
            analyze_batch.assert_called_once()
        second['runConfig']['actor']['maxQueueRequests'] = 5
        self.assertNotEqual(analysis.execution_condition(first, True), analysis.execution_condition(second, True))

    def test_power_projection_stays_on_measurement_clock(self):
        raw = raw_fixture('a')
        phase = raw['runtime']['phases'][2]
        before = analysis.measurement_offering(phase)
        for request in phase['requests']:
            for key in ('scheduledAt', 'dispatchedAt', 'completedAt'):
                request[key] = stamp((analysis.evidence_io.timestamp_ns(request[key]) -
                                      analysis.evidence_io.timestamp_ns(stamp(0))) / 1e9 + 3600)
        self.assertEqual(analysis.measurement_offering(phase), before)

    def test_service_distribution_detects_heldout_shift(self):
        runs = [raw_fixture('a', 10), raw_fixture('b', 30)]
        evaluated = {'profile': {'serviceSamples': runs[0]['runtime']['phases'][0]['nativeSamples']}}
        same = analysis.service_distribution(runs, evaluated, [0], 'p2')
        shifted = analysis.service_distribution(runs, evaluated, [1], 'p2')
        self.assertEqual(same['twoSampleTest']['statistic'], 0)
        self.assertEqual(shifted['twoSampleTest']['statistic'], 1)
        self.assertEqual(shifted['quantiles'][0]['errorNanos'], -20_000_000)

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_single_trial_and_missing_holdout_epi_are_reported(self):
        raw = raw_fixture('only')
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze([raw], [idle_fixture()], bridge, 3, 1, .95, 0, 2, 2)
        self.assertEqual(len(report['folds']), 2)
        self.assertIsNone(report['folds'][0]['phases'][0]['power']['predictedCIWatts'])
        raw['runtime']['phases'].pop()
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze([raw], [idle_fixture()], bridge, 3, 1, .95, 0, 2, 2)
        self.assertIsNone(report['folds'][0]['profile'])
        self.assertIn('no EPI training population', report['folds'][0]['unavailableReason'])

    @unittest.skipUnless(os.environ.get('C1_PROFILE_CLI'), 'set C1_PROFILE_CLI to built Go bridge')
    def test_phase_poison_is_excluded_and_trial_variance_reaches_ci(self):
        runs = [raw_fixture('a', 10), raw_fixture('b', 11), raw_fixture('c', 12)]
        for index, raw in enumerate(runs):
            for sample in raw['metrics']['samples']:
                sample['watts'] += index
        with analysis.GoBridge(os.environ['C1_PROFILE_CLI']) as bridge:
            report = analysis.analyze(runs, [idle_fixture()], bridge, 30, 13, .95, 0, 2, 2)
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
