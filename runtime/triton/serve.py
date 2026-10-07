#!/usr/bin/env python3
"""Launch official Triton and retain its queue timing and request release traces."""
from __future__ import annotations

import json
import signal
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path


def queue_records(entries):
    traces = {}
    for entry in entries:
        trace = traces.setdefault(entry["id"], {})
        if "request_id" in entry:
            trace["requestID"] = entry["request_id"]
        for timestamp in entry.get("timestamps", []):
            if timestamp["name"] in ("QUEUE_START", "COMPUTE_START", "REQUEST_END"):
                trace[timestamp["name"]] = timestamp["ns"]
    for trace in traces.values():
        if "QUEUE_START" not in trace and "REQUEST_END" not in trace:
            continue
        native_id = trace["requestID"]
        _, separator, definition = native_id.rpartition("~")
        if not separator or len(definition.split("/")) != 4:
            continue  # Ordinary native requests have no admitted CHILL assignment to release.
        record = {"requestID": native_id, "workloadUID": definition.split("/")[0],
                  "definition": definition,
                  "at": datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")}
        if "QUEUE_START" in trace:
            start = trace["QUEUE_START"]
            compute = trace.get("COMPUTE_START", 0)
            if type(start) is not int or start < 1 or type(compute) is not int or (compute and compute < start):
                raise ValueError("native trace has invalid monotonic queue timestamps")
            yield {**record, "phase": "queue_trace",
                   "queueStartNanos": str(start), "computeStartNanos": str(compute)}
        if "REQUEST_END" in trace:
            yield {**record, "phase": "finished"}


def collect(root, final=False):
    for path in root.glob("trace.json.*"):
        if path.stat().st_size > 64 * 1024:
            raise ValueError("native timestamp trace exceeds the record bound")
        try:
            entries = json.loads(path.read_text())
        except json.JSONDecodeError:
            if final:
                raise
            continue  # Triton is still writing this completed trace file.
        for record in queue_records(entries):
            print("CHILL_PROGRESS " + json.dumps(record, separators=(",", ":")), flush=True)
        path.unlink()


def main():
    with tempfile.TemporaryDirectory(prefix="chill-trace-") as directory:
        root = Path(directory)
        process = subprocess.Popen(["tritonserver", *sys.argv[1:],
            "--trace-config", "rate=1", "--trace-config", "level=TIMESTAMPS",
            "--trace-config", "triton,file=" + str(root / "trace.json"),
            "--trace-config", "triton,log-frequency=1"])
        def stop(signum, frame):
            process.send_signal(signum)
        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        try:
            while process.poll() is None:
                collect(root)
                try:
                    process.wait(timeout=0.05)
                except subprocess.TimeoutExpired:
                    pass
            collect(root, final=True)
            return process.returncode
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait()


if __name__ == "__main__":
    sys.exit(main())
