"""Command-line entry point for the external load generator."""

from __future__ import annotations

import argparse
import json
import math
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


def replay_open_loop(
    records: Sequence[Record], *, workload: str, offsets: Sequence[int],
    new_client: Callable[[], WorkloadHTTPClient], out: TextIO, start_ns: int,
    clock_ns: Callable[[], int] = time.monotonic_ns,
    sleep: Callable[[float], None] = time.sleep,
    request_lifetime_seconds: Optional[float] = None,
) -> None:
    """Dispatch every planned arrival independently, without an in-flight cap."""
    lock = threading.Lock()
    threads: List[threading.Thread] = []
    failures: List[BaseException] = []

    def base_row(request: ScheduledRequest) -> Dict[str, object]:
        index = int(request.request_id)
        return {
            "workload": workload, "record": records[index % len(records)][0],
            "requestIndex": index, "scheduledOffsetNs": request.scheduled_offset_ns,
            "scheduledMonotonicNS": start_ns + request.scheduled_offset_ns,
        }

    def emit(row: Dict[str, object]) -> None:
        encoded = json.dumps(row) + "\n"
        with lock:
            out.write(encoded)
            out.flush()

    def send(request: ScheduledRequest) -> None:
        client = None
        try:
            index = int(request.request_id)
            record_id, inputs = records[index % len(records)]
            scheduled = start_ns + request.scheduled_offset_ns
            client = new_client()
            sent = clock_ns()
            row = base_row(request)
            row.update(sentMonotonicNS=sent, latenessNs=sent - scheduled,
                       delayed=sent - scheduled > DELAY_TOLERANCE_NS, issued=True)
            remaining = (request_lifetime_seconds - (sent - scheduled) / 1e9
                         if request_lifetime_seconds is not None else None)
            if remaining is not None and remaining < 1e-9:
                row.pop("sentMonotonicNS")
                row.update(issued=False, error="GeneratorDelayExpired",
                           generatorFailure="lifetime_expired_before_send",
                           finishedMonotonicNS=sent, latencyMs=(sent - scheduled) / 1e6,
                           scheduledLatencyMs=(sent - scheduled) / 1e6)
                emit(row)
                return
            try:
                response = (client.infer(workload, inputs, request_lifetime_seconds=remaining)
                            if remaining is not None else client.infer(workload, inputs))
                row.update(status=response.status_code, requestID=response.request_id,
                           workloadUID=response.workload_uid)
            except OSError as error:
                row["error"] = type(error).__name__
            finished = clock_ns()
            row.update(finishedMonotonicNS=finished,
                       latencyMs=(finished - scheduled) / 1_000_000,
                       scheduledLatencyMs=(finished - scheduled) / 1_000_000,
                       serviceLatencyMs=(finished - sent) / 1_000_000)
            emit(row)
        except BaseException as error:
            with lock:
                failures.append(error)
        finally:
            if client is not None:
                client.close()

    def submit(request: ScheduledRequest, delayed: bool) -> None:
        thread = threading.Thread(target=send, args=(request,))
        try:
            thread.start()
        except RuntimeError as error:
            now = clock_ns()
            row = base_row(request)
            row.update(issued=False, error="GeneratorThreadStartError",
                       generatorFailure=type(error).__name__,
                       latenessNs=now - row["scheduledMonotonicNS"],
                       finishedMonotonicNS=now)
            emit(row)
            return
        threads.append(thread)

    try:
        run_schedule(
            [ScheduledRequest(str(index), offset, 1) for index, offset in enumerate(offsets)],
            submit, start_ns=start_ns, delay_tolerance_ns=DELAY_TOLERANCE_NS,
            clock_ns=clock_ns, sleep=sleep,
        )
    finally:
        for thread in threads:
            thread.join()
    if failures:
        raise RuntimeError("Open-loop request failed") from failures[0]


def replay(
    records: Sequence[Record],
    *,
    workload: str,
    rate: Optional[float],
    duration: float,
    workers: int,
    new_client: Callable[[], WorkloadHTTPClient],
    out: TextIO,
    arrivals: str = "fixed",
    seed: int = 0,
    clock_ns: Callable[[], int] = time.monotonic_ns,
    sleep: Callable[[float], None] = time.sleep,
    open_loop: bool = False,
    request_lifetime_seconds: Optional[float] = None,
) -> None:
    """Send open-loop arrivals or refill a fixed outstanding population, then drain its tail."""

    if arrivals != "closed-loop" and rate is None:
        raise ValueError("rate is required for open-loop arrivals")
    if open_loop and arrivals != "closed-loop":
        replay_open_loop(
            records, workload=workload,
            offsets=arrival_offsets_ns(arrivals, rate, duration, seed),
            new_client=new_client, out=out, start_ns=clock_ns(),
            clock_ns=clock_ns, sleep=sleep,
            request_lifetime_seconds=request_lifetime_seconds,
        )
        return
    schedule = [] if arrivals == "closed-loop" else [
        ScheduledRequest(str(index), offset_ns, 1)
        for index, offset_ns in enumerate(arrival_offsets_ns(arrivals, rate, duration, seed))
    ]
    lock = threading.Lock()
    duration_ns = round(duration * 1_000_000_000)
    start_ns = clock_ns()

    def run_worker(requests: Sequence[ScheduledRequest], worker: int = 0) -> None:
        client = new_client()

        def submit(request: ScheduledRequest, delayed: bool) -> None:
            # A sender behind schedule stops at the duration instead of draining it.
            if clock_ns() - start_ns >= duration_ns:
                return
            record_id, inputs = records[int(request.request_id) % len(records)]
            line: Dict[str, object] = {
                "workload": workload,
                "record": record_id,
                "scheduledOffsetNs": request.scheduled_offset_ns,
                "delayed": delayed,
            }
            sent_ns = clock_ns()
            line["sentMonotonicNS"] = sent_ns
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
            finished_ns = clock_ns()
            line["finishedMonotonicNS"] = finished_ns
            line["latencyMs"] = (finished_ns - sent_ns) / 1_000_000
            encoded = json.dumps(line) + "\n"
            with lock:
                out.write(encoded)
                out.flush()

        try:
            if arrivals == "closed-loop":
                index = worker
                while clock_ns() - start_ns < duration_ns:
                    submit(ScheduledRequest(str(index), clock_ns() - start_ns, 1), False)
                    index += workers
            else:
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

    threads = (
        [threading.Thread(target=run_worker, args=((), worker)) for worker in range(workers)]
        if arrivals == "closed-loop"
        else [
            threading.Thread(target=run_worker, args=(requests,))
            for requests in partition_schedule(schedule, workers)
            if requests
        ]
    )
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()


def _positive(kind: Callable[[str], float]) -> Callable[[str], float]:
    def parse(value: str) -> float:
        parsed = kind(value)
        if not math.isfinite(parsed) or not parsed > 0:
            raise argparse.ArgumentTypeError("must be positive")
        return parsed

    return parse


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="chill-loadgen",
        description="Generate an external open-loop inference workload or fixed outstanding load.",
    )
    parser.add_argument("--version", action="version", version=__version__)
    commands = parser.add_subparsers(dest="command")
    replay_parser = commands.add_parser(
        "replay",
        help="replay a Dataset evaluation directory to one Workload",
        description="Replay <dataset-dir>/<recordID>/<field> records as raw samples "
        "at an open-loop rate or with fixed outstanding connections; print one JSON line per request.",
    )
    replay_parser.add_argument("--endpoint", required=True, help="gateway HTTPS origin")
    replay_parser.add_argument("--token-file", required=True, type=Path, help="file holding the bearer token")
    replay_parser.add_argument("--ca-file", help="trusted CA PEM bundle (default: system store)")
    replay_parser.add_argument("--workload", required=True, help="registered Workload ID")
    replay_parser.add_argument("--dataset-dir", required=True, type=Path, help="the Dataset's evaluation directory")
    replay_parser.add_argument("--fields", required=True, help="comma-separated preprocessor input fields")
    replay_parser.add_argument("--rate", type=_positive(float), help="requests per second (required for fixed/poisson)")
    replay_parser.add_argument("--duration", required=True, type=_positive(float), help="seconds")
    replay_parser.add_argument(
        "--arrivals", choices=("fixed", "poisson", "closed-loop"), default="fixed",
        help="send times; closed-loop immediately refills --workers outstanding requests (default fixed)",
    )
    replay_parser.add_argument("--seed", type=int, default=0, help="Poisson arrival seed (default 0)")
    replay_parser.add_argument("--workers", type=_positive(int), default=8, help="concurrent connections (default 8)")
    replay_parser.add_argument("--timeout", type=_positive(float), help="socket seconds (E5 default lifetime + 2*delivery; otherwise lifetime + delivery or 30)")
    replay_parser.add_argument("--request-lifetime", type=_positive(float), help="native request lifetime seconds; sends X-Chill-Request-Timeout")
    replay_parser.add_argument("--delivery-margin", type=_positive(float), default=0.01, help="socket delivery seconds beyond lifetime (default .01)")
    replay_parser.add_argument("--workload-slo", type=_positive(float), help="frozen E5 Workload SLO seconds; paired with gateway timeout")
    replay_parser.add_argument("--gateway-timeout", type=_positive(float), help="frozen E5 gateway lifetime cap seconds; paired with workload SLO")
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command != "replay":
        parser.print_help()
        return 0

    fields = [field for field in args.fields.split(",") if field]
    try:
        if args.arrivals != "closed-loop" and args.rate is None:
            raise ValueError("--rate is required for fixed/poisson arrivals")
        if args.arrivals == "closed-loop" and args.rate is not None:
            raise ValueError("--rate does not apply to closed-loop arrivals")
        e5_contract = args.workload_slo is not None or args.gateway_timeout is not None
        if e5_contract:
            if args.workload_slo is None or args.gateway_timeout is None:
                raise ValueError("E5 replay requires --workload-slo and --gateway-timeout")
            if args.request_lifetime is not None:
                if args.request_lifetime < args.workload_slo:
                    raise ValueError("--request-lifetime must cover --workload-slo")
                if args.request_lifetime > args.gateway_timeout:
                    raise ValueError("--gateway-timeout must cover --request-lifetime")
        required_timeout = (
            args.request_lifetime + args.delivery_margin if args.request_lifetime is not None else 30
        )
        default_timeout = required_timeout + args.delivery_margin if e5_contract and args.request_lifetime is not None else required_timeout
        timeout = args.timeout if args.timeout is not None else default_timeout
        if args.request_lifetime is not None and (
            timeout < required_timeout or e5_contract and timeout <= required_timeout
        ):
            raise ValueError("socket --timeout must exceed lifetime + delivery for E5 and cover it for measurement replay")
        if not _valid_identifier(args.workload):
            raise ValueError("--workload must be a Kubernetes DNS subdomain name")
        if not fields:
            raise ValueError("--fields must name at least one field")
        records = load_records(args.dataset_dir, fields)
        token = args.token_file.read_text().strip()
        # Validate endpoint and token before any traffic.
        WorkloadHTTPClient(
            args.endpoint, bearer_token=token, timeout_seconds=timeout, ca_file=args.ca_file,
            request_lifetime_seconds=args.request_lifetime,
        ).close()
    except (OSError, ValueError) as error:
        parser.error(str(error))
    replay(
        records,
        workload=args.workload,
        rate=args.rate,
        duration=args.duration,
        arrivals=args.arrivals,
        seed=args.seed,
        workers=args.workers,
        new_client=lambda: WorkloadHTTPClient(
            args.endpoint, bearer_token=token, timeout_seconds=timeout, ca_file=args.ca_file,
            request_lifetime_seconds=args.request_lifetime,
        ),
        out=sys.stdout,
        open_loop=e5_contract,
        request_lifetime_seconds=args.request_lifetime,
    )
    return 0
