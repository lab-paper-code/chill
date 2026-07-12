#!/usr/bin/env python3
"""Derive the CPU-only runtime contract from one live Kubernetes Node."""

# TODO(internal): Generalize this spike-only CPU derivation into runtime-specific
# contract builders selected by DeviceClass + ModelArtifact compatibility.
# Admission/isolation and resource requests must be resolved before a run is Ready.

from __future__ import annotations

import argparse
import json
import math
import sys
from datetime import datetime, timezone
from pathlib import Path


POLICY = "OnePodPerNodeFullCPU"


def cpu_cores(quantity: str) -> float:
    if quantity.endswith("m"):
        return float(quantity[:-1]) / 1000.0
    return float(quantity)


def derive(node: dict, scheduling_request: str) -> dict:
    node_name = node["metadata"]["name"]
    allocatable = node.get("status", {}).get("allocatable", {}).get("cpu")
    if not allocatable:
        raise ValueError(f"Node {node_name!r} has no status.allocatable.cpu")
    allocatable_cores = cpu_cores(str(allocatable))
    budget = math.floor(allocatable_cores)
    if budget < 1:
        raise ValueError(
            f"Node {node_name!r} allocatable CPU {allocatable!r} cannot provide one whole CPU thread"
        )
    return {
        "apiVersion": "spikes.chill.dacs.io/v1alpha1",
        "kind": "DerivedExecutionContract",
        "derivedAt": datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z"),
        "nodeName": node_name,
        "runtime": "onnxruntime",
        "provider": "CPUExecutionProvider",
        "cpu": {
            "policy": POLICY,
            "budgetSource": "KubernetesNodeStatusAllocatable",
            "observedAllocatable": str(allocatable),
            "budgetCores": budget,
            "limit": str(budget),
        },
        "runtimeOptions": {
            "intraOpThreads": budget,
            "interOpThreads": 1,
        },
        "scheduling": {
            "cpuRequest": scheduling_request,
            "status": "SpikeOnlyUnresolved",
            "note": "CPU request and node exclusivity are not part of this execution-contract derivation.",
        },
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("node_json", help="Node JSON path or - for stdin")
    parser.add_argument("--scheduling-request", default="100m")
    args = parser.parse_args()
    raw = sys.stdin.read() if args.node_json == "-" else Path(args.node_json).read_text()
    try:
        contract = derive(json.loads(raw), args.scheduling_request)
    except (KeyError, TypeError, ValueError) as error:
        raise SystemExit(f"cannot derive execution contract: {error}")
    print(json.dumps(contract, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
