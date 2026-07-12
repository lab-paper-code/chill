#!/usr/bin/env python3
# TODO(internal): Replace this latency-only console summary with profiler
# derivation that validates repetitions/power coverage and writes conditions.
"""Validate and summarize latency-only batch characterization evidence."""

from __future__ import annotations

import json
import sys
from pathlib import Path


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} RESULTS.jsonl")
    records = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines() if line.strip()]
    if not records:
        raise SystemExit("result file is empty")
    records.sort(key=lambda record: record["batchSize"])
    expected_state = (records[0]["artifactDigest"], records[0]["runtime"], records[0]["provider"], records[0]["nodeName"])
    sweep_ids = {record.get("sweepId", "legacy") for record in records}
    assert len(sweep_ids) == 1
    for record in records:
        assert record["status"] == "Succeeded"
        assert (record["artifactDigest"], record["runtime"], record["provider"], record["nodeName"]) == expected_state
        assert record["power"] is None
        assert record["energyPerRequest"] is None
        assert record["bSat"] is None

    print("batch  mean_ms  p95_ms  p99_ms  items_per_second")
    print("-----  -------  ------  ------  ----------------")
    for record in records:
        latency = record["latencyMs"]
        print(f"{record['batchSize']:>5}  {latency['mean']:>7.3f}  {latency['p95']:>6.3f}  "
              f"{latency['p99']:>6.3f}  {record['throughputItemsPerSecond']:>16.3f}")
    print("\npower evidence: unavailable")
    print("energy/request: not derived")
    print("b_sat: not derived")


if __name__ == "__main__":
    main()
