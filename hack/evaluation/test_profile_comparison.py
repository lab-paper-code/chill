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


class ProfileComparisonTests(unittest.TestCase):
    def run_report(self, rows, samples, edges='0,1,5', cohort=None, client_directory=False):
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
            result = subprocess.run([sys.executable, str(Path(__file__).with_name('profile_comparison.py')),
                                     '--raw', str(root / 'obs'), '--profiles', str(profiles),
                                     '--idle-bin-edges-ms', edges, '--out', str(out), *client_args],
                                    capture_output=True, text=True)
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


if __name__ == '__main__':
    unittest.main()
