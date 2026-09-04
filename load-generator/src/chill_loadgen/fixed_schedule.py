"""Load a fixed request schedule from JSON."""

from __future__ import annotations

import json
from pathlib import Path
from typing import List

from chill_loadgen.schedule import ScheduledRequest


_REQUEST_FIELDS = {"request_id", "scheduled_offset_ms", "item_count"}


def load_fixed_schedule(path: Path) -> List[ScheduledRequest]:
    with path.open("r", encoding="utf-8") as stream:
        entries = json.load(stream)

    if not isinstance(entries, list):
        raise ValueError("fixed schedule must be a JSON array")

    requests = []
    for index, entry in enumerate(entries):
        if (
            not isinstance(entry, dict)
            or set(entry) != _REQUEST_FIELDS
            or not isinstance(entry.get("request_id"), str)
            or type(entry.get("scheduled_offset_ms")) is not int
            or type(entry.get("item_count")) is not int
        ):
            raise ValueError(f"fixed schedule entry {index} has invalid fields")

        requests.append(
            ScheduledRequest(
                request_id=entry["request_id"],
                scheduled_offset_ns=entry["scheduled_offset_ms"] * 1_000_000,
                item_count=entry["item_count"],
            )
        )

    return requests
