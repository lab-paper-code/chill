#!/usr/bin/env python3
"""Join one ordered runtime result with one in-Pod power observation per batch."""

# TODO(internal): Join by immutable Run identity and timestamp overlap in the
# profiler derivation layer. Zip/order matching is spike-only and cannot survive
# retries, parallelism, duplicated batches, or controller restarts.

from __future__ import annotations

import json
import re
import sys
from datetime import datetime
from pathlib import Path


def timestamp(value: str) -> datetime:
    # Python 3.8 accepts microseconds while Go emits RFC3339 nanoseconds.
    value = value.replace("Z", "+00:00")
    value = re.sub(r"(\.\d{6})\d+(?=[+-]\d{2}:\d{2}$)", r"\1", value)
    return datetime.fromisoformat(value)


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit(f"usage: {sys.argv[0]} RUNTIME.jsonl POWER.jsonl")
    runtime = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines() if line.strip()]
    power = [json.loads(line) for line in Path(sys.argv[2]).read_text().splitlines() if line.strip()]
    if len(runtime) != len(power):
        raise SystemExit(f"runtime/power result count differs: {len(runtime)} != {len(power)}")

    print("batch  infer  power  fail  overlap_s  mean_w  energy_j/request")
    print("-----  -----  -----  ----  ---------  ------  ----------------")
    for runtime_record, power_record in zip(runtime, power):
        if runtime_record["nodeName"] != power_record["source"]["nodeName"]:
            raise SystemExit("runtime and power Node identity differ")
        runtime_start = timestamp(runtime_record["measurementStartedAt"])
        runtime_end = timestamp(runtime_record["measurementEndedAt"])
        power_start = timestamp(power_record["startedAt"])
        power_end = timestamp(power_record["endedAt"])
        overlap_start = max(runtime_start, power_start)
        overlap_end = min(runtime_end, power_end)
        overlap_seconds = max(0.0, (overlap_end - overlap_start).total_seconds())
        samples = [
            sample for sample in power_record["samples"]
            if overlap_start <= timestamp(sample["observedAt"]) <= overlap_end
        ]
        if not samples:
            raise SystemExit(f"batch {runtime_record['batchSize']} has no overlapping power samples")
        watts = sum(sample["watts"] for sample in samples) / len(samples)
        latency_seconds = runtime_record["latencyMs"]["mean"] / 1000.0
        energy = watts * latency_seconds / runtime_record["batchSize"]
        inference_count = runtime_record.get("inferenceCount")
        if inference_count is None:
            inference_count = sum(len(item["latencyMs"]) for item in runtime_record["repetitionResults"])
        print(f"{runtime_record['batchSize']:>5}  {inference_count:>5}  {len(samples):>5}  "
              f"{power_record['summary']['failures']:>4}  {overlap_seconds:>9.3f}  "
              f"{watts:>6.3f}  {energy:>16.6f}")
    print("\nb_sat: not derived; integrated-path validation only")


if __name__ == "__main__":
    main()
