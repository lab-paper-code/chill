import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from adaptive_search import search


class AdaptiveSearchTest(unittest.TestCase):
    def test_stops_coarse_at_first_rise_then_refines_bracket(self):
        curve = {1: .096, 2: .093, 3: .091, 4: .090, 5: .089,
                 6: .091, 7: .092, 8: .093, 16: .095}
        calls = []
        result = search(lambda batch: calls.append(batch) or curve[batch], 16)
        self.assertEqual(result.bracket, (4, 8))
        self.assertEqual(result.candidate, 5)
        self.assertNotIn(16, calls)
        self.assertEqual(calls, [1, 2, 4, 8, 6, 7, 5])

    def test_does_not_claim_candidate_without_a_rise(self):
        result = search(lambda batch: -float(batch), 16)
        self.assertIsNone(result.candidate)
        self.assertEqual(result.measured_batches, (1, 2, 4, 8, 16))
        self.assertEqual(result.stopped_reason, "no-rise-before-maximum")

    def test_does_not_measure_a_cached_coarse_point_twice(self):
        calls = []
        curve = {1: 5., 2: 4., 3: 3., 4: 2., 5: 3., 6: 4., 7: 5., 8: 6.}
        search(lambda batch: calls.append(batch) or curve[batch], 8)
        self.assertEqual(len(calls), len(set(calls)))

    def test_first_rise_search_does_not_guarantee_global_optimum(self):
        # A local rise at batch 4 stops the coarse search, even though the
        # unobserved batch 8 is the true global minimum.
        curve = {1: 10., 2: 8., 3: 8.5, 4: 9., 8: 4.}
        calls = []
        result = search(lambda batch: calls.append(batch) or curve[batch], 8)

        self.assertEqual(result.bracket, (2, 4))
        self.assertEqual(result.candidate, 2)
        self.assertNotIn(8, calls)
        self.assertNotEqual(result.candidate, min(curve, key=curve.get))


if __name__ == "__main__":
    unittest.main()
