"""Monotonic open-loop request scheduling."""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Callable, List, Sequence


@dataclass(frozen=True)
class ScheduledRequest:
    request_id: str
    scheduled_offset_ns: int
    item_count: int

    def __post_init__(self) -> None:
        if not self.request_id:
            raise ValueError("request_id must not be empty")
        if self.scheduled_offset_ns < 0:
            raise ValueError("scheduled_offset_ns must be non-negative")
        if self.item_count < 1:
            raise ValueError("item_count must be positive")


def partition_schedule(
    requests: Sequence[ScheduledRequest], worker_count: int
) -> List[List[ScheduledRequest]]:
    """Distribute one schedule across workers in round-robin order."""

    if worker_count < 1:
        raise ValueError("worker_count must be positive")

    worker_schedules: List[List[ScheduledRequest]] = [
        [] for _ in range(worker_count)
    ]
    for index, request in enumerate(requests):
        worker_schedules[index % worker_count].append(request)
    return worker_schedules


def run_schedule(
    requests: Sequence[ScheduledRequest],
    submit: Callable[[ScheduledRequest, bool], None],
    *,
    start_ns: int,
    delay_tolerance_ns: int,
    clock_ns: Callable[[], int] = time.monotonic_ns,
    sleep: Callable[[float], None] = time.sleep,
) -> None:
    """Submit each request at its offset from the shared monotonic start."""

    if delay_tolerance_ns < 0:
        raise ValueError("delay_tolerance_ns must be non-negative")

    previous_offset_ns = -1
    for request in requests:
        if request.scheduled_offset_ns < previous_offset_ns:
            raise ValueError("requests must be ordered by scheduled_offset_ns")
        previous_offset_ns = request.scheduled_offset_ns

    for request in requests:
        scheduled_ns = start_ns + request.scheduled_offset_ns
        remaining_ns = scheduled_ns - clock_ns()
        if remaining_ns > 0:
            sleep(remaining_ns / 1_000_000_000)
        delayed = clock_ns() - scheduled_ns > delay_tolerance_ns
        submit(request, delayed)
