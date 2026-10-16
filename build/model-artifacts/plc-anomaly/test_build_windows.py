import importlib.util
import json
import tempfile
import unittest
from datetime import datetime, timedelta
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("build_windows", Path(__file__).with_name("build_windows.py"))
BUILD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILD)


def raw(stamp, **overrides):
    record = {"stats": None, "CURRENT_TIME": stamp.timestamp() * 1000, **{key: 1.0 for key in BUILD.FEATURES}}
    return json.dumps({**record, **overrides})


def series(start, count, step=1):
    stamp = datetime.fromisoformat(start)
    return [((stamp + timedelta(seconds=step * i)).isoformat(" ", "milliseconds"), raw(stamp + timedelta(seconds=step * i)))
            for i in range(count)]


class BuildWindowsTest(unittest.TestCase):
    def test_windows_skip_gaps_and_unusable_records(self):
        records = series("2026-08-01 00:00:00", 90) + series("2026-08-01 00:01:41", 60, step=2)
        records[70] = (records[70][0], raw(datetime(2026, 8, 1), SPEED="x"))
        found = BUILD.windows(reversed(records))
        # 0..59 fills one window; the bad record at 70 and the 11 s gap at 90 drop partial windows.
        self.assertEqual([window[0][0] for window in found], [records[0][0], records[90][0]])
        self.assertTrue(all(len(window) == BUILD.WINDOW for window in found))

    def test_splits_are_disjoint_by_time_and_written_as_record_directories(self):
        records = []
        for day in range(28, 32):
            records += series(f"2026-07-{day} 00:00:00", 600)
        for day in range(1, 21):
            records += series(f"2026-08-{day:02d} 00:00:00", 600 * 55)
        records += series("2026-08-13 23:59:30", 60)  # straddles train and validation
        groups = BUILD.split(BUILD.windows(records))
        for name, (start, end) in BUILD.SPLITS.items():
            self.assertTrue(groups[name])
            self.assertTrue(all(start <= w[0][0][:10] and w[-1][0][:10] <= end for w in groups[name]))
        self.assertFalse(any(w[0][0].startswith("2026-08-13 23:59:30") for g in groups.values() for w in g))
        subsets = BUILD.sample(groups)
        self.assertEqual(subsets, BUILD.sample(groups))
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            BUILD.write(output, subsets)
            self.assertEqual(sorted(p.name for p in output.iterdir()), ["evaluation", "profiling"])
            for subset, size in BUILD.SIZES.items():
                self.assertEqual(len(list((output / subset).iterdir())), size)
            window = subsets["evaluation"][0]
            record = output / "evaluation" / BUILD.record_id(window)
            self.assertEqual([p.name for p in record.iterdir()], ["records"])
            self.assertEqual(json.loads((record / "records").read_text("utf-8")), [json.loads(r) for _, r in window])

    def test_write_rejects_windows_above_the_field_bound(self):
        window = [(time, raw(datetime(2026, 8, 19), pad="x" * 2000)) for time, _ in series("2026-08-19 00:00:00", 60)]
        with tempfile.TemporaryDirectory() as directory, self.assertRaises(ValueError):
            BUILD.write(Path(directory), {"evaluation": [window]})


if __name__ == "__main__":
    unittest.main()
