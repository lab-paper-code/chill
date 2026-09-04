import json
import tempfile
import unittest
from pathlib import Path

from chill_loadgen.fixed_schedule import load_fixed_schedule
from chill_loadgen.schedule import ScheduledRequest


class FixedScheduleTest(unittest.TestCase):
    def test_loads_single_and_batched_requests(self) -> None:
        entries = [
            {
                "request_id": "request-1",
                "scheduled_offset_ms": 0,
                "item_count": 1,
            },
            {
                "request_id": "request-2",
                "scheduled_offset_ms": 125,
                "item_count": 8,
            },
        ]

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "schedule.json"
            path.write_text(json.dumps(entries), encoding="utf-8")

            requests = load_fixed_schedule(path)

        self.assertEqual(
            [
                ScheduledRequest("request-1", 0, 1),
                ScheduledRequest("request-2", 125_000_000, 8),
            ],
            requests,
        )

    def test_rejects_an_invalid_entry(self) -> None:
        entries = [
            {
                "request_id": "request-1",
                "scheduled_offset_ms": 0,
                "item_count": "8",
            }
        ]

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "schedule.json"
            path.write_text(json.dumps(entries), encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "entry 0"):
                load_fixed_schedule(path)


if __name__ == "__main__":
    unittest.main()
