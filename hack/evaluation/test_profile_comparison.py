"""Consumer-visible serving/profile comparison from synthetic archived segments."""
import gzip
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


def stamp(ns):
    return '2026-11-12T00:00:00.%09dZ' % ns


def request(rid, instance, batch, start, finish, receive, idle=0, service=10):
    return {'kind': 'final', 'requestID': rid, 'instance': instance, 'profileID': 'profile',
            'batchID': batch, 'batchSize': 2, 'nativeRunningAt': stamp(start),
            'nativeFinishedAt': stamp(finish), 'nativeElapsedMS': (finish - receive) / 1e6,
            'serviceDurationNanos': service * 1_000_000, 'precedingIdleNanos': idle,
            # Gateway time must never stand in for native receive time.
            'receivedAt': stamp(999_000_000)}


def profile_trial(trial='physical'):
    """Two batches sharing an idle bin and scheduled arrivals, with opposite dispatch ingress."""
    samples, requests = [], []
    for batch, start, finish, dispatches in [
            (1, 10_000_000, 12_000_000, [1_000_000, 10_000_000]),
            (2, 30_000_000, 50_000_000, [12_000_000, 11_000_000])]:
        members = [trial + '-%s-%s' % (batch, member) for member in range(2)]
        samples.append({'trialID': trial, 'batchID': batch, 'batchSize': 2,
                        'requestIDs': members, 'serviceDurationNanos': finish - start,
                        'precedingIdleNanos': 0, 'startedAt': stamp(start), 'finishedAt': stamp(finish)})
        for rid, dispatch in zip(members, dispatches):
            requests.append({'requestID': rid, 'scheduledAt': stamp(0),
                             'dispatchedAt': stamp(dispatch), 'completedAt': stamp(finish),
                             'latencyMs': finish / 1e6,
                             'response': {'progress': {'batchID': batch, 'at': stamp(finish),
                                          'nativeElapsedMS': finish / 1e6,
                                          'nativeHTTP': {'handlerEnteredAt': stamp(dispatch + 1_000),
                                                         'receivedNanos': -(dispatch + 1_000)}}}})
    return {'schemaVersion': 'chill.dacs.io/profiling-raw-measurement.v1alpha14',
            'podUID': trial, 'runtime': {'phases': [{'nativeSamples': samples, 'requests': requests}]}}


def catalog_samples(raw):
    return [{key: sample[key] for key in ('trialID', 'serviceDurationNanos', 'precedingIdleNanos')}
            for phase in raw['runtime']['phases'] for sample in phase['nativeSamples']]


class ProfileComparisonTests(unittest.TestCase):
    def run_report(self, rows, samples, edges='0,1,5', cohort=None, client_directory=False,
                   profile_raws=(), error=None):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            raw = root / 'obs' / '20261112T00'
            raw.mkdir(parents=True)
            # Repeat rows to exercise supplements and batch deduplication.
            with gzip.open(raw / 'requests.jsonl.gz', 'wt') as handle:
                for row in rows:
                    handle.write(json.dumps(row) + '\n')
            profiles = root / 'profiles.json'
            profiles.write_text(json.dumps({'profiles': [{'id': 'profile', 'batches': [
                {'batch': 2, 'serviceSamples': samples}]}]}))
            out = root / 'comparison.json'
            client_args = []
            if cohort is not None:
                clients = root / 'cohort-client.jsonl'
                clients.write_text(''.join(json.dumps({'requestID': rid}) + '\n' for rid in cohort))
                client_args = ['--clients', str(root if client_directory else clients)]
            profile_args = []
            for index, evidence in enumerate(profile_raws):
                path = root / ('profile-raw-%s.json' % index)
                path.write_text(json.dumps(evidence))
                profile_args.extend(['--profile-raw', str(path)])
            result = subprocess.run([sys.executable, str(Path(__file__).with_name('profile_comparison.py')),
                                     '--raw', str(root / 'obs'), '--profiles', str(profiles),
                                     '--idle-bin-edges-ms', edges, '--out', str(out), *client_args, *profile_args],
                                    capture_output=True, text=True)
            if error is not None:
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(error, result.stderr)
                return None
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(out.read_text())

    def test_native_intervals_late_supplements_bins_and_batch_dedup(self):
        first = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        same_batch = {**first, 'requestID': 'a2'}
        # Same batch ID on a different instance is a separate batch.
        second = request('b', 'two', 1, 30_000_000, 40_000_000, 25_000_000,
                         idle=1_000_000, service=30)
        third = request('c', 'one', 2, 50_000_000, 60_000_000, 45_000_000,
                        idle=5_000_000, service=50)
        # Exact start is included; exact finish is excluded. These requests never finish a batch.
        arrivals = [dict(kind='native', requestID=rid, instance=instance,
                         nativeFinishedAt=stamp(receive + 100_000_000), nativeElapsedMS=100)
                    for rid, instance, receive in [('start', 'one', 10_000_000),
                                                   ('finish', 'two', 40_000_000),
                                                   ('elsewhere', 'two', 55_000_000)]]
        final = {'kind': 'final', 'requestID': 'c', 'instance': 'one', 'profileID': 'profile',
                 'batchID': 0, 'batchSize': 0, 'nativeElapsedMS': 1}
        partial = {'kind': 'native', 'requestID': 'c', 'instance': 'one',
                   'nativeElapsedMS': 2}
        samples = [{'serviceDurationNanos': value * 1_000_000, 'precedingIdleNanos': idle}
                   for value, idle in [(2, 0), (4, 0), (6, 1_000_000), (8, None)]]
        report = self.run_report([first, same_batch, second, final, partial,
                                  {**third, 'kind': 'native'}, first, *arrivals], samples)
        self.assertEqual(report['idleBinEdgesMS'], [0, 1, 5])
        self.assertEqual(report['profileConditioning'], ['profileID', 'batchSize', 'idleBin'])
        self.assertEqual(report['profileIngressConditioning'], 'not conditioned on ingress')
        cells = {(row['idleBin'], row['otherArrived']): row for row in report['cells']}
        first_cell = cells[('[0,1)', True)]
        self.assertEqual(first_cell['serving'], {'n': 1, 'p50': 10, 'p95': 10})
        self.assertEqual(first_cell['profile'], {'n': 2, 'p50': 3, 'p95': 4})
        self.assertTrue(first_cell['profileIdleBinSupported'])
        self.assertAlmostEqual(first_cell['p50Ratio'], 10 / 3)
        self.assertEqual(first_cell['p95Ratio'], 2.5)
        self.assertEqual(cells[('[1,5)', False)]['serving']['p50'], 30)
        unsupported = cells[('[5,inf)', False)]
        self.assertFalse(unsupported['profileIdleBinSupported'])
        self.assertEqual(unsupported['profile'], {'n': 0, 'p50': None, 'p95': None})
        self.assertIsNone(unsupported['p50Ratio'])
        self.assertEqual(sum(row['serving']['n'] for row in report['cells']), 3)
        self.assertFalse(any(key in report for key in ('pass', 'fail', 'verdict')))
        self.assertFalse(first_cell['profileIngressConditioned'])
        self.assertFalse(first_cell['profileIngressSupported'])
        self.assertFalse(first_cell['sameConditionsComparison'])
        self.assertEqual(first_cell['profileUnconditioned'], first_cell['profile'])

    def test_batch_member_supplies_missing_running_time(self):
        first = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        first.pop('nativeRunningAt')
        sibling = request('a2', 'one', 1, 10_000_000, 20_000_000, 0)
        report = self.run_report([first, sibling], [
            {'serviceDurationNanos': 1_000_000, 'precedingIdleNanos': 0}])
        self.assertEqual(len(report['cells']), 1)
        self.assertEqual(report['cells'][0]['serving']['n'], 1)
        self.assertIs(report['cells'][0]['otherArrived'], False)

    def test_adjacent_nanosecond_bins_stay_distinct(self):
        gaps = [1_000_000, 1_000_001, 1_000_002]
        rows = [request(str(index), 'one', index + 1, 10_000_000 * (index + 1),
                        10_000_000 * (index + 1) + 1_000_000, 0, idle=gap)
                for index, gap in enumerate(gaps)]
        samples = [{'serviceDurationNanos': (index + 1) * 1_000_000,
                    'precedingIdleNanos': gap} for index, gap in enumerate(gaps)]
        report = self.run_report(rows, samples, edges='0,1,1.000001,1.000002,2')
        self.assertEqual(len(report['cells']), 3)
        self.assertEqual(report['idleBinEdgesNanos'], [0, 1_000_000, 1_000_001, 1_000_002, 2_000_000])
        self.assertEqual([cell['idleBinIndex'] for cell in report['cells']], [1, 2, 3])
        self.assertEqual([cell['idleBin'] for cell in report['cells']],
                         ['[1,1.000001)', '[1.000001,1.000002)', '[1.000002,2)'])
        self.assertEqual([cell['profile']['n'] for cell in report['cells']], [1, 1, 1])

    def test_partial_batch_member_supplies_timing_and_identity(self):
        complete = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        for key in ('nativeRunningAt', 'nativeFinishedAt', 'profileID', 'batchSize'):
            complete.pop(key)
        partial = request('a2', 'one', 1, 10_000_000, 20_000_000, 0)
        partial.pop('serviceDurationNanos')
        arrival = request('b', 'one', 2, 30_000_000, 40_000_000, 15_000_000)
        report = self.run_report([complete, partial, arrival], [
            {'serviceDurationNanos': 1_000_000, 'precedingIdleNanos': 0}])
        self.assertEqual(report['counts']['comparedBatches'], 2)
        cell = next(cell for cell in report['cells'] if cell['otherArrived'] is True)
        self.assertEqual(cell['serving']['n'], 1)

    def test_client_cohort_limits_batches_not_observed_arrivals(self):
        selected = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        sibling = {**selected, 'requestID': 'a2'}
        outside = request('b', 'one', 2, 30_000_000, 40_000_000, 15_000_000)
        for directory in (False, True):
            with self.subTest(directory=directory):
                report = self.run_report([selected, sibling, outside], [
                    {'serviceDurationNanos': 1_000_000, 'precedingIdleNanos': 0}],
                    cohort=['a2'], client_directory=directory)
                self.assertEqual(report['counts']['comparedBatches'], 1)
                self.assertEqual(report['cells'][0]['serving']['n'], 1)
                self.assertIs(report['cells'][0]['otherArrived'], True)
                self.assertTrue(any('Longer executions' in caveat for caveat in report['limitations']))

    def test_unknown_idle_and_ingress_are_not_zero_or_no_arrival(self):
        row = request('a', 'one', 1, 10_000_000, 20_000_000, 0, idle=None)
        row.pop('nativeRunningAt')
        report = self.run_report([row], [{'serviceDurationNanos': 1_000_000,
                                          'precedingIdleNanos': None}])
        cell = report['cells'][0]
        self.assertEqual(cell['idleBin'], 'unknown')
        self.assertIsNone(cell['otherArrived'])
        self.assertFalse(cell['profileIdleBinSupported'])
        self.assertEqual(cell['profile']['n'], 0)
        self.assertEqual(report['marginalByIngress'][0]['profile']['n'], 1)

    def test_profile_raw_conditions_same_idle_bin_on_dispatch_ingress(self):
        raw = profile_trial()
        first = request('serving-first', 'one', 1, 10_000_000, 20_000_000, 0)
        arrival = request('arrival', 'one', 2, 70_000_000, 80_000_000, 15_000_000,
                          service=40)
        # Another physical trial's dispatch at 40ms must not affect this trial.
        elsewhere = profile_trial('elsewhere')
        for member in elsewhere['runtime']['phases'][0]['requests']:
            member['dispatchedAt'] = stamp(40_000_000)
        report = self.run_report([first, arrival], catalog_samples(raw), profile_raws=[raw, elsewhere])
        self.assertEqual(report['profileIngressConditioning'], 'conditioned on ingress')
        self.assertEqual(report['profileConditioning'], ['profileID', 'batchSize', 'idleBin', 'otherArrived'])
        cells = {cell['otherArrived']: cell for cell in report['cells']}
        self.assertEqual(cells[True]['profile'], {'n': 1, 'p50': 2, 'p95': 2})
        self.assertEqual(cells[False]['profile'], {'n': 1, 'p50': 20, 'p95': 20})
        self.assertEqual(cells[True]['p50Ratio'], 5)
        self.assertEqual(cells[False]['p50Ratio'], 2)
        self.assertTrue(all(cell['sameConditionsComparison'] for cell in cells.values()))
        self.assertTrue(all(cell['profileIngressSupported'] for cell in cells.values()))
        self.assertEqual(report['counts']['profileSamplesWithIngress'], 2)
        self.assertEqual(len(report['sources']['profileRaw']), 2)
        self.assertIn('dispatchedAt directly', report['profileReceiveDefinition'])
        marginal = {cell['otherArrived']: cell for cell in report['marginalByIngress']}
        self.assertEqual(marginal[True]['profile']['p50'], 2)
        self.assertEqual(marginal[False]['profile']['p50'], 20)
        self.assertFalse(marginal[True]['sameConditionsComparison'])

    def test_profile_ingress_half_open_boundaries_and_own_members(self):
        raw = profile_trial()
        phase = raw['runtime']['phases'][0]
        # Own member at start is excluded. Other batch at finish is excluded.
        phase['requests'][3]['dispatchedAt'] = stamp(12_000_000)
        serving = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        no_arrival = self.run_report([serving], catalog_samples(raw), profile_raws=[raw])
        self.assertEqual(no_arrival['cells'][0]['profile']['n'], 2)
        # Other batch at exact start belongs to the ingress cell.
        phase['requests'][3]['dispatchedAt'] = stamp(10_000_000)
        at_start = self.run_report([serving], catalog_samples(raw), profile_raws=[raw])
        self.assertEqual(at_start['cells'][0]['profile']['p50'], 20)
        self.assertEqual(at_start['cells'][0]['profile']['n'], 1)

    def test_profile_raw_missing_dispatch_or_run_stays_unconditioned(self):
        for missing in ('dispatchedAt', 'startedAt', 'finishedAt'):
            with self.subTest(missing=missing):
                raw = profile_trial()
                phase = raw['runtime']['phases'][0]
                for record in phase['requests' if missing == 'dispatchedAt' else 'nativeSamples']:
                    record.pop(missing)
                report = self.run_report([request('a', 'one', 1, 10_000_000, 20_000_000, 0)],
                                         catalog_samples(raw), profile_raws=[raw])
                self.assertEqual(report['profileIngressConditioning'], 'not conditioned on ingress')
                self.assertFalse(report['cells'][0]['profileIngressConditioned'])
                self.assertEqual(report['cells'][0]['profile']['n'], 2)
                self.assertEqual(report['counts']['profileSamplesWithoutIngress'], 2)

    def test_delayed_dispatch_changes_ingress_with_same_scheduled_arrivals(self):
        raw = profile_trial()
        requests = raw['runtime']['phases'][0]['requests']
        arrivals = [member['scheduledAt'] for member in requests]
        serving = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        before = self.run_report([serving], catalog_samples(raw), profile_raws=[raw])
        self.assertEqual(before['cells'][0]['profile'], {'n': 1, 'p50': 20, 'p95': 20})
        requests[3]['dispatchedAt'] = stamp(12_000_000)
        after = self.run_report([serving], catalog_samples(raw), profile_raws=[raw])
        self.assertEqual(after['cells'][0]['profile'], {'n': 2, 'p50': 11, 'p95': 20})
        self.assertEqual([member['scheduledAt'] for member in requests], arrivals)

    def test_profile_dispatch_does_not_require_native_http_receipts(self):
        raw = profile_trial()
        for member in raw['runtime']['phases'][0]['requests']:
            member.pop('response')
        report = self.run_report([request('a', 'one', 1, 10_000_000, 20_000_000, 0)],
                                 catalog_samples(raw), profile_raws=[raw])
        self.assertEqual(report['profileIngressConditioning'], 'conditioned on ingress')
        self.assertEqual(report['cells'][0]['profile'], {'n': 1, 'p50': 20, 'p95': 20})

    def test_catalog_without_raw_timestamps_stays_unconditioned(self):
        report = self.run_report([request('a', 'one', 1, 10_000_000, 20_000_000, 0)],
                                 catalog_samples(profile_trial()))
        cell = report['cells'][0]
        self.assertEqual(report['profileIngressConditioning'], 'not conditioned on ingress')
        self.assertFalse(cell['profileIngressConditioned'])
        self.assertFalse(cell['sameConditionsComparison'])
        self.assertEqual(cell['profile'], {'n': 2, 'p50': 11, 'p95': 20})
        self.assertEqual(cell['profileUnconditioned'], cell['profile'])

    def test_mixed_profile_evidence_never_pools_unknown_ingress(self):
        raw = profile_trial()
        samples = catalog_samples(raw) + [{'trialID': 'legacy', 'serviceDurationNanos': 100_000_000,
                                           'precedingIdleNanos': 0}]
        report = self.run_report([request('a', 'one', 1, 10_000_000, 20_000_000, 0)],
                                 samples, profile_raws=[raw])
        cell = report['cells'][0]
        self.assertEqual(cell['profile']['p50'], 20)
        self.assertEqual(cell['profile']['n'], 1)
        self.assertEqual(cell['profileUnconditioned'], {'n': 1, 'p50': 100, 'p95': 100})
        self.assertIn('partially conditioned', report['profileIngressConditioning'])
        self.assertEqual(report['counts']['profileSamplesWithoutIngress'], 1)

    def test_missing_ingress_cell_does_not_fall_back_to_other_population(self):
        raw = profile_trial()
        for member in raw['runtime']['phases'][0]['requests'][2:]:
            member['dispatchedAt'] = stamp(12_000_000)
        serving = request('a', 'one', 1, 10_000_000, 20_000_000, 0)
        arrival = request('b', 'one', 2, 30_000_000, 40_000_000, 15_000_000)
        report = self.run_report([serving, arrival], catalog_samples(raw), profile_raws=[raw])
        cell = next(cell for cell in report['cells'] if cell['otherArrived'])
        self.assertTrue(cell['profileIdleBinSupported'])
        self.assertFalse(cell['profileIngressSupported'])
        self.assertFalse(cell['sameConditionsComparison'])
        self.assertEqual(cell['profile']['n'], 0)
        self.assertIsNone(cell['p50Ratio'])

    def test_profile_raw_rejects_wrong_catalog_trial_samples(self):
        raw = profile_trial()
        samples = catalog_samples(raw)
        samples[0]['serviceDurationNanos'] += 1
        self.run_report([], samples, profile_raws=[raw],
                        error='profile raw samples do not match catalog trial')


if __name__ == '__main__':
    unittest.main()
