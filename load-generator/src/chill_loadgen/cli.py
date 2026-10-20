"""Command-line entry point for the external load generator."""

from __future__ import annotations

import argparse
import json
import random
import sys
import threading
import time
from collections.abc import Sequence
from pathlib import Path
from typing import Callable, Dict, List, Optional, TextIO, Tuple

from chill_loadgen import __version__
from chill_loadgen.http_client import InferenceResponse, WorkloadHTTPClient, _valid_identifier
from chill_loadgen.schedule import ScheduledRequest, partition_schedule, run_schedule

# A request sent this late after its scheduled time is reported as delayed.
DELAY_TOLERANCE_NS = 10_000_000

Record = Tuple[str, Dict[str, bytes]]


def load_records(dataset_dir: Path, fields: Sequence[str]) -> List[Record]:
    """Read `<dataset_dir>/<recordID>/<field>` for every record, ordered by record ID."""

    records = []
    for record_dir in sorted(p for p in dataset_dir.iterdir() if p.is_dir()):
        inputs = {}
        for field in fields:
            path = record_dir / field
            if not path.is_file():
                raise ValueError(f"record {record_dir.name} has no field {field}")
            inputs[field] = path.read_bytes()
        records.append((record_dir.name, inputs))
    if not records:
        raise ValueError(f"{dataset_dir} contains no records")
    return records


def arrival_offsets_ns(arrivals: str, rate: float, duration: float, seed: int) -> List[int]:
    """Return send offsets in [0, duration): a fixed interval, or seeded exponential gaps."""

    if arrivals == "fixed":
        return [round(index * 1_000_000_000 / rate) for index in range(int(rate * duration))]
    rng = random.Random(seed)
    offsets: List[int] = []
    at = rng.expovariate(rate)
    while at < duration:
        offsets.append(round(at * 1_000_000_000))
        at += rng.expovariate(rate)
    return offsets


def replay(
    records: Sequence[Record],
    *,
    workload: str,
    rate: float,
    duration: float,
    workers: int,
    new_client: Callable[[], WorkloadHTTPClient],
    out: TextIO,
    arrivals: str = "fixed",
    seed: int = 0,
    clock_ns: Callable[[], int] = time.monotonic_ns,
    sleep: Callable[[float], None] = time.sleep,
) -> None:
    """Send records round-robin at an open-loop rate and write one JSON line per request."""

    schedule = [
        ScheduledRequest(str(index), offset_ns, 1)
        for index, offset_ns in enumerate(arrival_offsets_ns(arrivals, rate, duration, seed))
    ]
    lock = threading.Lock()
    start_ns = clock_ns()

    def run_worker(requests: Sequence[ScheduledRequest]) -> None:
        client = new_client()

        def submit(request: ScheduledRequest, delayed: bool) -> None:
            record_id, inputs = records[int(request.request_id) % len(records)]
            line: Dict[str, object] = {
                "workload": workload,
                "record": record_id,
                "scheduledOffsetNs": request.scheduled_offset_ns,
                "delayed": delayed,
            }
            sent_ns = clock_ns()
            try:
                response: InferenceResponse = client.infer(workload, inputs)
                line.update(
                    status=response.status_code,
                    requestID=response.request_id,
                    workloadUID=response.workload_uid,
                )
            except OSError as error:  # transport, TLS and timeout failures
                client.close()  # the connection reopens on the next request
                line["error"] = type(error).__name__
            line["latencyMs"] = (clock_ns() - sent_ns) / 1_000_000
            with lock:
                out.write(json.dumps(line) + "\n")
                out.flush()

        try:
            run_schedule(
                requests,
                submit,
                start_ns=start_ns,
                delay_tolerance_ns=DELAY_TOLERANCE_NS,
                clock_ns=clock_ns,
                sleep=sleep,
            )
        finally:
            client.close()

    threads = [
        threading.Thread(target=run_worker, args=(requests,))
        for requests in partition_schedule(schedule, workers)
        if requests
    ]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()


def _positive(kind: Callable[[str], float]) -> Callable[[str], float]:
    def parse(value: str) -> float:
        parsed = kind(value)
        if not parsed > 0:
            raise argparse.ArgumentTypeError("must be positive")
        return parsed

    return parse


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="chill-loadgen",
        description="Generate an external open-loop inference workload.",
    )
    parser.add_argument("--version", action="version", version=__version__)
    commands = parser.add_subparsers(dest="command")
    replay_parser = commands.add_parser(
        "replay",
        help="replay a Dataset evaluation directory to one Workload",
        description="Replay <dataset-dir>/<recordID>/<field> records as raw samples "
        "at a fixed open-loop rate; print one JSON line per request.",
    )
    replay_parser.add_argument("--endpoint", required=True, help="gateway HTTPS origin")
    replay_parser.add_argument("--token-file", required=True, type=Path, help="file holding the bearer token")
    replay_parser.add_argument("--ca-file", help="trusted CA PEM bundle (default: system store)")
    replay_parser.add_argument("--workload", required=True, help="registered Workload ID")
    replay_parser.add_argument("--dataset-dir", required=True, type=Path, help="the Dataset's evaluation directory")
    replay_parser.add_argument("--fields", required=True, help="comma-separated preprocessor input fields")
    replay_parser.add_argument("--rate", required=True, type=_positive(float), help="requests per second")
    replay_parser.add_argument("--duration", required=True, type=_positive(float), help="seconds")
    replay_parser.add_argument(
        "--arrivals", choices=("fixed", "poisson"), default="fixed", help="send times (default fixed)"
    )
    replay_parser.add_argument("--seed", type=int, default=0, help="Poisson arrival seed (default 0)")
    replay_parser.add_argument("--workers", type=_positive(int), default=8, help="concurrent connections (default 8)")
    replay_parser.add_argument("--timeout", type=_positive(float), default=30, help="per-request seconds (default 30)")
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command != "replay":
        parser.print_help()
        return 0

    fields = [field for field in args.fields.split(",") if field]
    try:
        if not _valid_identifier(args.workload):
            raise ValueError("--workload must be a Kubernetes DNS subdomain name")
        if not fields:
            raise ValueError("--fields must name at least one field")
        records = load_records(args.dataset_dir, fields)
        token = args.token_file.read_text().strip()
        # Validate endpoint and token before any traffic.
        WorkloadHTTPClient(args.endpoint, bearer_token=token, timeout_seconds=args.timeout, ca_file=args.ca_file).close()
    except (OSError, ValueError) as error:
        parser.error(str(error))
    replay(
        records,
        workload=args.workload,
        rate=args.rate,
        duration=args.duration,
        arrivals=args.arrivals,
        seed=args.seed,
        new_client=lambda: WorkloadHTTPClient(
            args.endpoint, bearer_token=token, timeout_seconds=args.timeout, ca_file=args.ca_file
        ),
        out=sys.stdout,
    )
    return 0
