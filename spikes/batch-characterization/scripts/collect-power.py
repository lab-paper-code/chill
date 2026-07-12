#!/usr/bin/env python3

# TODO(internal): Replace Prometheus port-forward collection and local files
# with PowerObserver results attached to an immutable profiling Run. Integrate
# timestamped samples rather than finalizing the current provisional mean-power method.
"""Join batch measurement windows with raw Shelly samples from Prometheus."""

from __future__ import annotations

import argparse
import json
import statistics
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from pathlib import Path


METRIC = "shelly_power_total_watts"


def instant(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def isoformat(timestamp: float) -> str:
    return datetime.fromtimestamp(timestamp, timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def query_raw_samples(prometheus_url: str, node: str, start: datetime, end: datetime) -> tuple[dict[str, str], list[tuple[float, float]]]:
    # One scrape interval of padding lets us report whether samples bracket the
    # experiment, while calculations below use only samples inside the window.
    duration_seconds = max(1, int((end - start).total_seconds()) + 21)
    selector = f'{METRIC}{{node="{node}"}}'
    query = f"{selector}[{duration_seconds}s]"
    params = urllib.parse.urlencode({"query": query, "time": end.isoformat()})
    request = urllib.request.Request(f"{prometheus_url.rstrip('/')}/api/v1/query?{params}")
    with urllib.request.urlopen(request, timeout=10) as response:
        payload = json.load(response)
    if payload.get("status") != "success":
        raise RuntimeError(f"Prometheus query failed: {payload}")
    series = payload["data"]["result"]
    if len(series) != 1:
        raise RuntimeError(f"expected one Shelly series for node={node}, found {len(series)}")
    samples = [(float(timestamp), float(watts)) for timestamp, watts in series[0]["values"]]
    return series[0]["metric"], samples


def enrich(record: dict[str, object], prometheus_url: str, node: str, minimum_samples: int) -> dict[str, object]:
    start = instant(str(record["measurementStartedAt"]))
    end = instant(str(record["measurementEndedAt"]))
    labels, raw_samples = query_raw_samples(prometheus_url, node, start, end)
    start_timestamp = start.timestamp()
    end_timestamp = end.timestamp()
    samples = [(timestamp, watts) for timestamp, watts in raw_samples if start_timestamp <= timestamp <= end_timestamp]
    intervals = [right[0] - left[0] for left, right in zip(samples, samples[1:])]
    sample_values = [watts for _, watts in samples]
    sample_count = len(samples)
    quality = "sufficient" if sample_count >= minimum_samples else "insufficient"

    evidence: dict[str, object] = {
        "source": "shelly-wall-via-edge-metrics-prometheus",
        "metric": METRIC,
        "labels": labels,
        "measurementStartedAt": record["measurementStartedAt"],
        "measurementEndedAt": record["measurementEndedAt"],
        "samples": [{"timestamp": isoformat(timestamp), "watts": watts} for timestamp, watts in samples],
        "sampleCount": sample_count,
        "medianSampleIntervalSeconds": round(statistics.median(intervals), 3) if intervals else None,
        "quality": quality,
        "minimumSamplesRequired": minimum_samples,
    }
    record["powerEvidence"] = evidence
    if not samples:
        record["power"] = None
        record["energyPerRequest"] = None
        return record

    mean_watts = round(statistics.fmean(sample_values), 6)
    record["power"] = {
        "meanWatts": mean_watts,
        "minWatts": min(sample_values),
        "maxWatts": max(sample_values),
        "sampleCount": sample_count,
        "quality": quality,
    }
    mean_execution_seconds = float(record["latencyMs"]["mean"]) / 1000.0  # type: ignore[index]
    joules = mean_watts * mean_execution_seconds / int(record["batchSize"])
    record["energyPerRequest"] = {
        "joules": round(joules, 9),
        "method": "mean-wall-watts-times-mean-fixed-batch-execution-seconds-divided-by-batch",
        "provisional": quality != "sufficient",
    }
    # b_sat is a cross-batch conclusion and is intentionally not decided while
    # any point lacks the required power-sample coverage.
    record["bSat"] = None
    return record


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("results", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--prometheus-url", default="http://127.0.0.1:19090")
    parser.add_argument("--node")
    parser.add_argument("--minimum-samples", type=int, default=5)
    args = parser.parse_args()

    records = [json.loads(line) for line in args.results.read_text().splitlines() if line.strip()]
    if not records:
        raise SystemExit("result file is empty")
    nodes = {str(record["nodeName"]) for record in records}
    node = args.node or (next(iter(nodes)) if len(nodes) == 1 else None)
    if not node:
        raise SystemExit("records contain multiple nodes; pass --node explicitly")

    enriched = [enrich(record, args.prometheus_url, node, args.minimum_samples) for record in records]
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text("".join(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n" for record in enriched))

    print("batch  samples  mean_watts  quality       energy_per_request_j")
    print("-----  -------  ----------  ------------  --------------------")
    all_sufficient = True
    for record in sorted(enriched, key=lambda value: value["batchSize"]):
        evidence = record["powerEvidence"]
        power = record["power"]
        energy = record["energyPerRequest"]
        quality = evidence["quality"]
        all_sufficient = all_sufficient and quality == "sufficient"
        print(f"{record['batchSize']:>5}  {evidence['sampleCount']:>7}  "
              f"{power['meanWatts'] if power else 'N/A':>10}  {quality:<12}  "
              f"{energy['joules'] if energy else 'N/A':>20}")
    print(f"\nb_sat: {'eligible for separate derivation' if all_sufficient else 'not derived; insufficient sample coverage'}")
    print(f"evidence: {args.output}")


if __name__ == "__main__":
    main()
