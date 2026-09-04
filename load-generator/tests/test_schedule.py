import unittest

from chill_loadgen.schedule import ScheduledRequest, partition_schedule, run_schedule


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


class PartitionScheduleTest(unittest.TestCase):
    def test_distributes_requests_round_robin_without_changing_them(self) -> None:
        requests = [
            ScheduledRequest("request-1", 0, 1),
            ScheduledRequest("request-2", 10, 8),
            ScheduledRequest("request-3", 20, 4),
            ScheduledRequest("request-4", 30, 2),
        ]

        self.assertEqual(
            [[requests[0], requests[2]], [requests[1], requests[3]]],
            partition_schedule(requests, worker_count=2),
        )

    def test_requires_at_least_one_worker(self) -> None:
        with self.assertRaisesRegex(ValueError, "worker_count"):
            partition_schedule([], worker_count=0)


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
            start_ns=clock.read(),
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

    def test_marks_a_request_delayed_against_the_shared_start(self) -> None:
        clock = FakeClock()
        submitted = []

        run_schedule(
            [ScheduledRequest("request-1", 0, 1)],
            lambda request, delayed: submitted.append(delayed),
            start_ns=clock.read() - 2_000_000,
            delay_tolerance_ns=1_000_000,
            clock_ns=clock.read,
            sleep=clock.sleep,
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
                start_ns=0,
                delay_tolerance_ns=1_000_000,
            )

        self.assertEqual([], submitted)


if __name__ == "__main__":
    unittest.main()
