"""Monotonic open-loop request scheduling."""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Callable, Sequence


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


def run_schedule(
    requests: Sequence[ScheduledRequest],
    submit: Callable[[ScheduledRequest], None],
    *,
    clock_ns: Callable[[], int] = time.monotonic_ns,
    sleep: Callable[[float], None] = time.sleep,
) -> None:
    """Submit each request at its offset from one monotonic start time."""

    previous_offset_ns = -1
    for request in requests:
        if request.scheduled_offset_ns < previous_offset_ns:
            raise ValueError("requests must be ordered by scheduled_offset_ns")
        previous_offset_ns = request.scheduled_offset_ns

    start_ns = clock_ns()
    for request in requests:
        remaining_ns = start_ns + request.scheduled_offset_ns - clock_ns()
        if remaining_ns > 0:
            sleep(remaining_ns / 1_000_000_000)
        submit(request)
