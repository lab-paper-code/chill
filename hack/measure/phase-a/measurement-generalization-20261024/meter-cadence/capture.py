"""Finite approved read-only meter observation; retained as evidence/provenance, not deployed code."""
import concurrent.futures
import csv
import datetime
import json
import os
import argparse
import math
from pathlib import Path
import re
import time
import urllib.error
import urllib.request

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
ROOT = Path(ENV["cadenceCaptureOutput"])
BASE = ENV["meterURL"]
NODES = ENV["cadenceNodes"]
INTERVAL_NS = 100_000_000
SLOTS = 1200
DURATION_NS = SLOTS * INTERVAL_NS
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def utc(ns):
    return datetime.datetime.fromtimestamp(ns / 1e9, datetime.timezone.utc).isoformat()


def power(text, node):
    matches = []
    for line in text.splitlines():
        match = re.fullmatch(r'shelly_power_total_watts\{([^}]*)\}\s+([^\s]+)', line)
        if match:
            labels = dict(re.findall(r'(\w+)="([^"]*)"', match.group(1)))
            if labels.get("hostname") == node and labels.get("device_type") == NODES[node]:
                value = float(match.group(2))
                if not math.isfinite(value):
                    raise ValueError("nonfinite power")
                matches.append((value, match.group(2)))
    if len(matches) != 1:
        raise ValueError(f"expected one exact power series, found {len(matches)}")
    return matches[0]


def fetch(url, timeout):
    row = {"url": url, "actual_start_monotonic_ns": time.monotonic_ns(), "actual_start_wall_unix_ns": time.time_ns()}
    row.update(status=None, error=None, raw_reply="", headers={})
    try:
        with OPENER.open(url, timeout=timeout) as response:
            row["status"] = response.status
            row["headers"] = dict(response.headers)
            row["raw_reply"] = response.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        row.update(status=exc.code, error=str(exc), raw_reply=exc.read().decode("utf-8", errors="replace"))
    except Exception as exc:
        row["error"] = f"{type(exc).__name__}: {exc}"
    row["actual_end_wall_unix_ns"] = time.time_ns()
    row["actual_end_monotonic_ns"] = time.monotonic_ns()
    row["duration_ns"] = row["actual_end_monotonic_ns"] - row["actual_start_monotonic_ns"]
    row["actual_start_utc"] = utc(row["actual_start_wall_unix_ns"])
    row["actual_end_utc"] = utc(row["actual_end_wall_unix_ns"])
    return row


def capture(node, anchor_mono, anchor_wall):
    deadline = anchor_mono + DURATION_NS
    with (ROOT / f"{node}.raw.jsonl").open("x") as out:
        for slot in range(SLOTS):
            planned = anchor_mono + slot * INTERVAL_NS
            remaining = planned - time.monotonic_ns()
            if remaining > 0:
                time.sleep(remaining / 1e9)
            row = {"node": node, "slot": slot, "planned_start_monotonic_ns": planned,
                   "planned_start_wall_unix_ns": anchor_wall + slot * INTERVAL_NS,
                   "planned_wall_kind": "projection from real wall/monotonic anchor, not observed wall timestamp",
                   "power_w": None, "power_text": None, "power_unit": "W", "parser_error": None}
            now = time.monotonic_ns()
            if now >= planned + INTERVAL_NS or now >= deadline:
                row.update(skipped=True, skip_reason="missed scheduling slot; no catch-up burst", status=None, error=None,
                           actual_start_monotonic_ns=None, actual_start_wall_unix_ns=None,
                           actual_end_monotonic_ns=None, actual_end_wall_unix_ns=None, duration_ns=None, raw_reply=None)
            else:
                row.update(skipped=False)
                row.update(fetch(BASE + "/prometheus?hostname=" + node, min(2.0, (deadline-now)/1e9)))
                row["start_jitter_ns"] = row["actual_start_monotonic_ns"] - planned
                if row["status"] == 200 and row["error"] is None:
                    try:
                        row["power_w"], row["power_text"] = power(row["raw_reply"], node)
                    except Exception as exc:
                        row["parser_error"] = f"{type(exc).__name__}: {exc}"
            out.write(json.dumps(row, ensure_ascii=False) + "\n")
            out.flush()
    return node


# One tiny parser/arithmetic self-check; no framework, no live request.
check_node = next(iter(NODES))
assert power(f'shelly_power_total_watts{{device_type="{NODES[check_node]}",hostname="{check_node}"}} 12.3\n', check_node) == (12.3, "12.3")
assert (SLOTS - 1) * INTERVAL_NS == 119_900_000_000
if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, default=ROOT)
    args = parser.parse_args()
    ROOT = args.output_dir.resolve()
    repository = Path(CONFIG["paths"]["repository"]).resolve()
    if repository == ROOT or repository in ROOT.parents:
        parser.error("output must remain outside the repository source tree")
    ROOT.mkdir(parents=True, exist_ok=True)
    health_before = fetch(BASE + "/devices", 3.0)
    (ROOT / "health-before.json").write_text(json.dumps(health_before, indent=2) + "\n")
    if health_before["status"] != 200 or health_before["error"]:
        raise SystemExit("pre-observation registry health failed; no meter capture started")
    anchor_mono = time.monotonic_ns() + 500_000_000
    anchor_wall = time.time_ns() + 500_000_000
    meta = {"nodes": NODES, "interval_ns": INTERVAL_NS, "scheduled_slots_per_node": SLOTS,
            "duration_seconds": 120, "anchor_monotonic_ns": anchor_mono, "anchor_wall_unix_ns": anchor_wall,
            "anchor_wall_utc": utc(anchor_wall), "deadline_monotonic_ns": anchor_mono + DURATION_NS,
            "same_source_concurrency": 1, "catch_up_bursts": False, "parser_arithmetic_selfcheck": "passed",
            "clock_note": "Host CLOCK_MONOTONIC scheduling/duration and independently observed CLOCK_REALTIME endpoints; planned wall values projected, no meter acquisition timestamps.",
            "scope": "Only HTTP GET existing /devices before/after and /prometheus?hostname=<node> within 120s. No load, configuration, restart, deployment, CR or actuation."}
    (ROOT / "capture-metadata.json").write_text(json.dumps(meta, indent=2) + "\n")
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(lambda n: capture(n, anchor_mono, anchor_wall), NODES))
    health_after = fetch(BASE + "/devices", 3.0)
    (ROOT / "health-after.json").write_text(json.dumps(health_after, indent=2) + "\n")
    print(json.dumps({"capture_complete": True, "anchor_utc": utc(anchor_wall), "health_before_status": health_before["status"], "health_after_status": health_after["status"], "artifacts": str(ROOT)}))
