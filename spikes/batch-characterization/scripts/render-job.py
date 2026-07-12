#!/usr/bin/env python3
"""Render the batch Job with one previously derived execution contract."""

# TODO(internal): Job rendering becomes a profiler controller responsibility.
# Use owner references, immutable run identity, status conditions, and cleanup
# policy rather than piping an ephemeral manifest from a local script.

from __future__ import annotations

import argparse
import json
from pathlib import Path

import yaml


def upsert_env(container: dict, name: str, value: str) -> None:
    env = container.setdefault("env", [])
    for item in env:
        if item.get("name") == name:
            item.clear()
            item.update({"name": name, "value": value})
            return
    env.append({"name": name, "value": value})


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest", type=Path)
    parser.add_argument("contract", type=Path)
    parser.add_argument("--batch-sizes", default=None)
    args = parser.parse_args()
    document = yaml.safe_load(args.manifest.read_text())
    contract = json.loads(args.contract.read_text())
    if contract.get("provider") != "CPUExecutionProvider":
        raise SystemExit("this renderer supports only CPUExecutionProvider")

    pod_spec = document["spec"]["template"]["spec"]
    runtime = next(item for item in pod_spec["containers"] if item["name"] == "runtime")
    if args.batch_sizes:
        batches = [int(value) for value in args.batch_sizes.split(",")]
        if not batches or any(batch < 1 for batch in batches):
            raise SystemExit("batch sizes must be positive integers")
        document["spec"]["completions"] = len(batches)
        upsert_env(runtime, "BATCH_SIZES", ",".join(str(batch) for batch in batches))
    runtime.setdefault("resources", {}).setdefault("limits", {})["cpu"] = contract["cpu"]["limit"]
    runtime.setdefault("resources", {}).setdefault("requests", {})["cpu"] = contract["scheduling"]["cpuRequest"]
    upsert_env(runtime, "ORT_INTRA_OP", str(contract["runtimeOptions"]["intraOpThreads"]))
    upsert_env(runtime, "ORT_INTER_OP", str(contract["runtimeOptions"]["interOpThreads"]))
    document["spec"]["template"].setdefault("metadata", {}).setdefault("annotations", {})[
        "spikes.chill.dacs.io/execution-contract-policy"
    ] = contract["cpu"]["policy"]
    print(yaml.safe_dump(document, sort_keys=False))


if __name__ == "__main__":
    main()
