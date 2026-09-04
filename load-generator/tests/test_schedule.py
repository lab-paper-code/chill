import unittest

from chill_loadgen.schedule import ScheduledRequest, run_schedule


class FakeClock:
    def __init__(self) -> None:
        self.now_ns = 1_000_000_000

    def read(self) -> int:
        return self.now_ns

    def sleep(self, seconds: float) -> None:
        self.now_ns += round(seconds * 1_000_000_000)


class ScheduledRequestTest(unittest.TestCase):
    def test_requires_valid_identity_offset_and_item_count(self) -> None:
        with self.assertRaisesRegex(ValueError, "request_id"):
            ScheduledRequest("", 0, 1)
        with self.assertRaisesRegex(ValueError, "scheduled_offset_ns"):
            ScheduledRequest("request-1", -1, 1)
        with self.assertRaisesRegex(ValueError, "item_count"):
            ScheduledRequest("request-1", 0, 0)


class RunScheduleTest(unittest.TestCase):
    def test_submits_single_and_batched_requests_at_absolute_offsets(self) -> None:
        clock = FakeClock()
        submitted = []
        requests = [
            ScheduledRequest("request-1", 0, 1),
            ScheduledRequest("request-2", 2_000_000_000, 8),
            ScheduledRequest("request-3", 2_000_000_000, 4),
        ]

        run_schedule(
            requests,
            lambda request, delayed: submitted.append(
                (request.request_id, request.item_count, clock.read(), delayed)
            ),
            delay_tolerance_ns=1_000_000,
            clock_ns=clock.read,
            sleep=clock.sleep,
        )

        self.assertEqual(
            [
                ("request-1", 1, 1_000_000_000, False),
                ("request-2", 8, 3_000_000_000, False),
                ("request-3", 4, 3_000_000_000, False),
            ],
            submitted,
        )

    def test_marks_a_request_that_misses_its_schedule(self) -> None:
        clock = FakeClock()
        submitted = []

        def oversleep(seconds: float) -> None:
            clock.sleep(seconds)
            clock.now_ns += 2_000_000

        run_schedule(
            [ScheduledRequest("request-1", 1_000_000_000, 1)],
            lambda request, delayed: submitted.append(delayed),
            delay_tolerance_ns=1_000_000,
            clock_ns=clock.read,
            sleep=oversleep,
        )

        self.assertEqual([True], submitted)

    def test_rejects_an_unordered_schedule_before_submission(self) -> None:
        submitted = []
        requests = [
            ScheduledRequest("request-1", 2, 1),
            ScheduledRequest("request-2", 1, 1),
        ]

        with self.assertRaisesRegex(ValueError, "ordered"):
            run_schedule(
                requests,
                lambda request, delayed: submitted.append(request),
                delay_tolerance_ns=1_000_000,
            )

        self.assertEqual([], submitted)


if __name__ == "__main__":
    unittest.main()
