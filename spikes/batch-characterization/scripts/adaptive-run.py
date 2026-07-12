#!/usr/bin/env python3
"""Run the adaptive batch search against the isolated Kubernetes spike."""

# TODO(internal): Replace local subprocess orchestration and filesystem
# campaigns with a reconciler-owned profiling Campaign/Run lifecycle. Resolve
# Node, artifact, runtime, and PowerObserver targets from Kubernetes objects.

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from adaptive_search import search  # noqa: E402


ROOT = Path(__file__).resolve().parents[3]
SPIKE = ROOT / "spikes" / "batch-characterization"


def timestamp(value: str) -> datetime:
    value = value.replace("Z", "+00:00")
    value = re.sub(r"(\.\d{6})\d+(?=[+-]\d{2}:\d{2}$)", r"\1", value)
    return datetime.fromisoformat(value)


def run_batch(batch: int, evidence_dir: Path) -> float:
    print(f"\n=== measuring batch {batch} ===", flush=True)
    environment = dict(os.environ, BATCH_SIZES=str(batch))
    subprocess.run([SPIKE / "scripts" / "run.sh"], cwd=ROOT, env=environment, check=True)
    subprocess.run([SPIKE / "scripts" / "collect.sh"], cwd=ROOT, check=True)

    latest = SPIKE / "results" / "latest"
    runtime = json.loads((latest / "results.jsonl").read_text().strip())
    power = json.loads((latest / "power-observations.jsonl").read_text().strip())
    runtime_start = timestamp(runtime["measurementStartedAt"])
    runtime_end = timestamp(runtime["measurementEndedAt"])
    samples = [sample for sample in power["samples"]
               if runtime_start <= timestamp(sample["observedAt"]) <= runtime_end]
    if not samples:
        raise RuntimeError(f"batch {batch} has no overlapping power samples")
    mean_watts = sum(sample["watts"] for sample in samples) / len(samples)
    epsilon = mean_watts * (runtime["latencyMs"]["mean"] / 1000.0) / batch

    evidence_dir.mkdir(parents=True, exist_ok=True)
    with (evidence_dir / "results.jsonl").open("a") as stream:
        stream.write(json.dumps(runtime, sort_keys=True) + "\n")
    with (evidence_dir / "power-observations.jsonl").open("a") as stream:
        stream.write(json.dumps(power, sort_keys=True) + "\n")
    with (evidence_dir / "search-measurements.jsonl").open("a") as stream:
        stream.write(json.dumps({"batchSize": batch, "energyJPerRequest": epsilon,
                                 "meanWatts": mean_watts, "powerSamples": len(samples)},
                                sort_keys=True) + "\n")
    print(f"batch={batch} energy={epsilon:.6f} J/request", flush=True)
    return epsilon


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--maximum-batch", type=int, default=16)
    parser.add_argument("--campaign", default=datetime.now().strftime("%Y%m%dT%H%M%S"))
    args = parser.parse_args()
    evidence_dir = SPIKE / "results" / "campaigns" / args.campaign
    if evidence_dir.exists():
        raise SystemExit(f"campaign already exists: {evidence_dir}")

    result = search(lambda batch: run_batch(batch, evidence_dir), args.maximum_batch)
    payload = {
        "strategy": "exponential-until-first-rise-then-discrete-binary",
        "maximumBatch": args.maximum_batch,
        "measuredBatches": list(result.measured_batches),
        "candidateBracket": list(result.bracket) if result.bracket else None,
        "candidateBSat": result.candidate,
        "acceptedBSat": None,
        "stoppedReason": result.stopped_reason,
    }
    (evidence_dir / "search-result.json").write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
    print("\n" + json.dumps(payload, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
