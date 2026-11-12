#!/usr/bin/env python3
"""Measure the production firstfloor actor and its production IPC runner."""
from __future__ import annotations

import http.client
import http.server
import json
import math
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import time
import threading
from datetime import datetime
from typing import Any
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from ortcpu import runner as common

REQUEST_SCHEMA = "chill.dacs.io/firstfloor-runtime-request.v1alpha2"
FRAME_BYTES = 64 << 20
PROVIDERS = {"ort-cpu": "CPUExecutionProvider", "ort-cuda": "CUDAExecutionProvider", "rknnlite": ""}
TERMINATION_SCHEMA = "chill.dacs.io/measurement-worker-termination.v1alpha1"


class ExecutionError(Exception):
    """The native runtime failed to execute, rather than producing invalid evidence."""

    def __init__(self, message: str, failure_reason: str = "ExecutionFailed"):
        super().__init__(message)
        self.failure_reason = failure_reason


def record_stage(stage: str, failure_reason: str = "") -> None:
    path = os.environ.get("CHILL_TERMINATION_MESSAGE_PATH")
    if path:
        record = {"schemaVersion": TERMINATION_SCHEMA, "stage": stage}
        if failure_reason:
            record["failureReason"] = failure_reason
        Path(path).write_text(json.dumps(record, separators=(",", ":")), encoding="utf-8")


def record_failure(error: Exception) -> None:
    cause = error
    while cause.__cause__ is not None and not isinstance(cause, ExecutionError):
        cause = cause.__cause__
    path = os.environ.get("CHILL_TERMINATION_MESSAGE_PATH")
    if path:
        stage = json.loads(Path(path).read_text(encoding="utf-8"))["stage"]
        if isinstance(cause, ExecutionError):
            record_stage(stage, cause.failure_reason)
        else:
            record_stage("ResultValidation" if stage == "Execution" else stage, "IntegrityError")


def deadline_policy(settings: dict[str, Any]) -> str:
    value = settings.get("deadlinePolicy", "keep")
    if value not in ("drop", "keep", "defer"):
        raise common.ContractError("firstfloor deadlinePolicy must be drop, keep, or defer")
    return value


def initialized_batches(run: dict[str, Any]) -> list[int]:
    batches = run["actor"]["batchSizes"]
    if not batches or run["measurement"]["batch"] not in batches:
        raise common.ContractError("configured initialized batches must include the selected fixed K")
    return batches


def validate_request(request: dict[str, Any]) -> None:
    run = common.require_object(request.get("runConfig"), "runConfig")
    state = common.require_object(run.get("state"), "state")
    backend = state.get("backend")
    if backend not in PROVIDERS:
        raise common.ContractError("unsupported firstfloor backend")
    common.validate_request(request, REQUEST_SCHEMA, backend)
    settings = common.require_object(state["firstfloor"], "state.firstfloor")
    common.require_fields(settings, {"maxBatchSize", "maxQueueSize"} |
                          ({"deadlinePolicy"} if "deadlinePolicy" in settings else set()), "state.firstfloor")
    deadline_policy(settings)
    maximum = common.require_integer(settings["maxBatchSize"], "maxBatchSize", 1)
    queue = common.require_integer(settings["maxQueueSize"], "maxQueueSize", 1)
    if maximum > queue or queue > 1 << 20:
        raise common.ContractError("firstfloor batch/queue capacity is invalid")
    initialized_batches(run)
    if backend != "rknnlite" and "hostContract" in run["delivery"]:
        raise common.ContractError("ORT cannot use RKNN host declarations")
    if backend == "ort-cuda":
        access = common.require_object(run["delivery"].get("runtimeAccess"), "CUDA runtimeAccess")
        uuid = common.require_string(access.get("gpuUUID"), "frozen GPU UUID")
        if not uuid.startswith("GPU-") or access.get("resources") != {"nvidia.com/gpu": "1"}:
            raise common.ContractError("CUDA requires one frozen whole physical GPU allocation")


def memory_peak(root: Path = common.CGROUP_ROOT) -> tuple[int, str]:
    v2 = (root / "cpu.max").is_file() or (root / "memory.peak").is_file()
    path = root / "memory.peak" if v2 else root / "memory" / "memory.max_usage_in_bytes"
    try:
        peak = int(common.read_required_text(path, "kernel cgroup memory peak"))
    except ValueError as error:
        raise common.ContractError("kernel cgroup memory peak is malformed") from error
    if peak < 1:
        raise common.ContractError("kernel cgroup memory peak must be positive")
    source = "cgroup-v2-memory.peak" if v2 else "cgroup-v1-memory.max_usage_in_bytes"
    return peak, source + ":runtime-container-lifetime"


def memory_sampler(run: dict[str, Any], root: Path = common.CGROUP_ROOT) -> common.MemorySampler | None:
    """Prefer the kernel high-water mark; a v2 kernel without memory.peak is sampled in the window."""
    if (root / "cpu.max").is_file() and not (root / "memory.peak").is_file():
        interval = common.require_integer(run["metrics"]["intervalMilliseconds"], "metrics intervalMilliseconds", 1)
        return common.MemorySampler(interval, root)
    memory_peak(root)
    return None


def milliseconds(value: Any, name: str, positive: bool = True) -> float:
    if (isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or
            value < 0 or (positive and value == 0)):
        raise common.ContractError(f"{name} must contain {'positive' if positive else 'nonnegative'} finite milliseconds")
    return float(value)


def load_plan(request: dict[str, Any]) -> dict[str, Any]:
    run = request["runConfig"]
    measurement, state = run["measurement"], run["state"]
    # The controller injects the same Pod IP it gives the off-node load Job.
    host = common.require_string(os.environ.get("POD_IP"), "measurement POD_IP")
    authority = f"[{host}]" if ":" in host else host
    return {"nativeEndpoint": f"http://{authority}:8080",
            "inferPath": "/v2/models/" + quote(state["model"], safe="") + "/infer",
            "model": state["model"], "fields": state["preprocessor"]["inputs"],
            "batch": measurement["batch"], "podUID": request["podUID"],
            "sloPercentile": measurement["sloPercentile"],
            "sloTargetLatencyNanos": measurement["sloTargetLatencyNanos"],
            "workloadResourceVersion": measurement.get("workloadResourceVersion", request["podUID"]),
            "operatingPoints": measurement["operatingPoints"],
            "maxQueueRequests": run["actor"]["maxQueueRequests"],
            "maxQueueBytes": min(request["maxInputBytes"], FRAME_BYTES),
            "minServiceSamples": measurement["minServiceSamples"],
            "maxLatencySamples": request["maxLatencySamples"], "maxResultBytes": request["maxResultBytes"]}


class LoadControl:
    """Hold the external producer until observer handoff, then accept one result."""
    def __init__(self, request: dict[str, Any], address: tuple[str, int] = ("0.0.0.0", 8081)):
        self.plan = load_plan(request)
        self.ready = threading.Event()
        self.finished = threading.Event()
        self.result: dict[str, Any] | None = None
        self.error: Exception | None = None
        self.lock = threading.Lock()
        self.phase_windows: list[dict[str, str]] = []
        self.supply_sizing: dict[str, Any] | None = None
        self.finalize_measurement = None
        self.final_observation: dict[str, Any] | None = None
        socket_timeout = self.plan["operatingPoints"]["requestLifetimeSeconds"]
        ready_timeout = request["observerReadyTimeoutMilliseconds"] / 1000 * (len(request["observerReadyPaths"]) + 3)
        self.phase_ids = ["sparse", "saturation"] + [f"utilization-{index}" for index in range(len(self.plan["operatingPoints"]["utilizations"]))]
        control = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def setup(self) -> None:
                self.request.settimeout(socket_timeout)
                super().setup()

            def reply(self, status: int, value: dict[str, Any]) -> None:
                body = json.dumps(value, allow_nan=False, separators=(",", ":")).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self) -> None:
                if self.path != "/measurement/load":
                    self.reply(404, {"error": "unknown measurement control path"})
                    return
                if not control.ready.wait(ready_timeout):
                    self.reply(503, {"error": "measurement control readiness timed out"})
                    return
                if control.error is not None:
                    self.reply(503, {"error": str(control.error)})
                else:
                    self.reply(200, control.plan)

            def do_POST(self) -> None:
                if self.path not in ("/measurement/load-result", "/measurement/phase", "/measurement/supply"):
                    self.reply(404, {"error": "unknown measurement control path"})
                    return
                with control.lock:
                    if not control.ready.is_set() or control.finished.is_set():
                        self.reply(409, {"error": "measurement load result is not expected"})
                        return
                    try:
                        length = int(self.headers.get("Content-Length", "0"))
                        if not 0 < length <= request["maxResultBytes"]:
                            raise common.ContractError("measurement load result exceeds frozen result budget")
                        payload = self.rfile.read(length)
                        if len(payload) != length:
                            raise common.ContractError("measurement load result is truncated")
                        value = common.require_object(json.loads(payload, object_pairs_hook=common.no_duplicate_object), "load result")
                        if self.path == "/measurement/supply":
                            common.require_fields(value, {"nativeSamples"}, "sparse supply evidence")
                            if (control.supply_sizing is not None or len(control.phase_windows) != 1 or
                                    "measurementEndedAt" not in control.phase_windows[0]):
                                raise common.ContractError("supply sizing requires exactly one completed sparse phase")
                            control.supply_sizing = size_supply(control.plan, value["nativeSamples"])
                            self.reply(200, control.supply_sizing)
                            return
                        if self.path == "/measurement/phase":
                            common.require_fields(value, {"id", "action"}, "phase boundary")
                            if value["action"] == "start":
                                index = len(control.phase_windows)
                                if (index >= len(control.phase_ids) or value["id"] != control.phase_ids[index] or
                                        index and "measurementEndedAt" not in control.phase_windows[-1]):
                                    raise common.ContractError("phase start differs from frozen order")
                                if value["id"] != "sparse" and control.supply_sizing is None:
                                    raise common.ContractError("saturation sizing must complete before later phases")
                                at = common.now()
                                control.phase_windows.append({"id": value["id"], "measurementStartedAt": at})
                            elif value["action"] == "end":
                                if not control.phase_windows or control.phase_windows[-1]["id"] != value["id"] or "measurementEndedAt" in control.phase_windows[-1]:
                                    raise common.ContractError("phase end has no matching active phase")
                                if len(control.phase_windows) == len(control.phase_ids):
                                    if control.finalize_measurement is None:
                                        raise common.ContractError("measurement finalizer is not ready")
                                    control.final_observation = control.finalize_measurement()
                                    at = control.final_observation["endedAt"]
                                else:
                                    at = common.now()
                                control.phase_windows[-1]["measurementEndedAt"] = at
                            else:
                                raise common.ContractError("phase action must be start or end")
                            self.reply(200, {"at": at})
                            return
                        if value.get("status") == "Failed":
                            common.require_fields(value, {"status", "error"} |
                                                  ({"failureReason"} if "failureReason" in value else set()) |
                                                  ({"issueEvidence"} if "issueEvidence" in value else set()), "failed load result")
                            reason = value.get("failureReason", "ExecutionFailed")
                            if reason not in ("ExecutionFailed", "MemoryAllocationFailed"):
                                raise common.ContractError("unsupported native load failure reason")
                            control.error = ExecutionError(common.require_string(value["error"], "load failure"), reason)
                            if "issueEvidence" in value:
                                issue = common.require_object(value["issueEvidence"], "failed issue evidence")
                                common.require_fields(issue, {"id", "kind", "arrivalRatePerSecond",
                                                      "issueElapsedSecondsMonotonic", "achievedArrivalRatePerSecond",
                                                      "issueLagToleranceMs", "issueLagMs"} |
                                                      ({"utilization"} if "utilization" in issue else set()), "failed issue evidence")
                                control.error.issue_evidence = issue
                                control.error.args = (str(control.error) + "; issueEvidence=" +
                                                      json.dumps(issue, allow_nan=False, separators=(",", ":")),)
                        else:
                            common.require_fields(value, {"status", "phases", "supplySizing"}, "load result")
                            if value["status"] != "Succeeded":
                                raise common.ContractError("unsupported measurement load result status")
                            if control.supply_sizing is None or value["supplySizing"] != control.supply_sizing:
                                raise common.ContractError("uploaded supply sizing differs from native sizing result")
                            validate_phases(value["phases"], control.plan, control.supply_sizing)
                            if sparse_max_seconds(value["phases"][0]["nativeSamples"], control.plan) != control.supply_sizing["maxBatchServiceSeconds"]:
                                raise common.ContractError("uploaded sparse samples differ from native sizing source")
                            if len(control.phase_windows) != len(value["phases"]):
                                raise common.ContractError("load result lacks measurement-node phase windows")
                            for phase, window in zip(value["phases"], control.phase_windows):
                                if any(phase.get(field) != window.get(field) for field in ("id", "measurementStartedAt", "measurementEndedAt")):
                                    raise common.ContractError("phase power windows must use measurement-node boundaries")
                            control.result = value
                    except (ValueError, TypeError, KeyError, common.ContractError, OSError, subprocess.SubprocessError) as error:
                        control.error = common.ContractError(f"invalid external load result: {error}")
                        control.finished.set()
                        self.reply(400, {"error": str(control.error)})
                        return
                    control.finished.set()
                    self.reply(200, {"status": "Accepted"})

            def log_message(self, *args: Any) -> None:
                pass

        self.server = http.server.ThreadingHTTPServer(address, Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def wait_result(self, process: subprocess.Popen) -> dict[str, Any]:
        while not self.finished.wait(common.SIGNAL_POLL_SECONDS):
            if process.poll() is not None:
                raise ExecutionError("native actor exited while waiting for external load result")
        if self.error is not None:
            raise self.error
        return self.result

    def close(self) -> None:
        self.error = self.error or ExecutionError("measurement control stopped")
        self.ready.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


def utc_timestamp(value: Any, name: str) -> tuple[datetime, int]:
    text = common.require_string(value, name)
    fraction = text.split(".", 1)[1].split("Z", 1)[0].split("+", 1)[0] if "." in text else ""
    if len(fraction) > 9 or fraction and not fraction.isdigit():
        raise common.ContractError(name + " must be RFC3339Nano")
    if fraction:
        # Python 3.8 accepts microsecond ISO text; retain the remaining nanos below.
        prefix, suffix = text.split(".", 1)
        text = prefix + "." + fraction.ljust(6, "0")[:6] + suffix[len(fraction):]
    try:
        result = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError as error:
        raise common.ContractError(name + " must be RFC3339Nano") from error
    if result.tzinfo is None or result.utcoffset().total_seconds() != 0:
        raise common.ContractError(name + " must be UTC")
    return result, int(fraction.ljust(9, "0") or "0") % 1000


def sparse_max_seconds(samples: Any, plan: dict[str, Any]) -> float:
    if not isinstance(samples, list) or not samples:
        raise common.ContractError("sparse sizing requires observed native batches")
    maximum, previous_end, previous_id = 0, None, 0
    for sample in samples:
        sample = common.require_object(sample, "sparse native sample")
        batch_id = common.require_integer(sample.get("batchID"), "sparse batch ID", 1)
        if batch_id <= previous_id or sample.get("batchSize") != plan["batch"] or sample.get("trialID") != plan["podUID"]:
            raise common.ContractError("sparse native batches must match this fixed-batch physical trial in execution order")
        duration = common.require_integer(sample.get("serviceDurationNanos"), "sparse service duration", 1)
        earliest = utc_timestamp(sample.get("earliestEnqueuedAt"), "sparse earliest enqueue")
        started = utc_timestamp(sample.get("startedAt"), "sparse exchange start")
        finished = utc_timestamp(sample.get("finishedAt"), "sparse exchange end")
        if not earliest <= started < finished:
            raise common.ContractError("sparse native exchange/enqueue timestamps are unordered")
        if previous_end is None or earliest >= previous_end:
            maximum = max(maximum, duration)
        previous_end, previous_id = finished, batch_id
    if maximum == 0:
        raise common.ContractError("sparse sizing has no observed no-backlog native batch")
    return maximum / 1_000_000_000


def size_supply(plan: dict[str, Any], samples: Any) -> dict[str, Any]:
    points = plan["operatingPoints"]
    payload = {"batch": plan["batch"], "maxQueueRequests": plan["maxQueueRequests"],
               "maxQueueBytes": plan["maxQueueBytes"],
               "framedDecodedRequestUpperBytes": points["framedDecodedRequestUpperBytes"],
               "requestLifetimeSeconds": points["requestLifetimeSeconds"],
               "deliveryMarginMilliseconds": points["deliveryMarginMilliseconds"],
               "maxBatchServiceSeconds": sparse_max_seconds(samples, plan)}
    completed = subprocess.run(["/opt/chill/bin/serving-worker", "--size-saturation"],
                               input=json.dumps(payload, allow_nan=False), capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        raise common.ContractError("native saturation sizing failed: " + completed.stderr.strip())
    result = common.require_object(json.loads(completed.stdout, object_pairs_hook=common.no_duplicate_object), "native supply sizing")
    workers = common.require_integer(result.get("workers"), "native saturation workers", plan["batch"])
    if workers > 64 or result.get("maxBatchServiceSeconds") != payload["maxBatchServiceSeconds"]:
        raise common.ContractError("native saturation sizing returned invalid workers/service evidence")
    return result


def validate_phases(phases: Any, plan: dict[str, Any], supply_sizing: dict[str, Any]) -> None:
    points = plan["operatingPoints"]
    expected = [("sparse", "sparse", None), ("saturation", "saturation", None)] + [
        (f"utilization-{index}", "utilization", value) for index, value in enumerate(points["utilizations"])
    ]
    if not isinstance(phases, list) or len(phases) != len(expected):
        raise common.ContractError("load phases do not cover every frozen operating point")
    batch_ids, request_ids = set(), set()
    previous_end = None
    throughput = 0.0
    for phase, (phase_id, kind, utilization) in zip(phases, expected):
        phase = common.require_object(phase, "load phase")
        required = {"id", "kind", "measurementStartedAt", "measurementEndedAt",
                    "measurementElapsedSecondsMonotonic", "completedInputs", "requests", "nativeSamples", "latencyMs"}
        issue_fields = {"achievedArrivalRatePerSecond", "issueElapsedSecondsMonotonic", "issueLagMs", "issueLagToleranceMs"}
        common.require_fields(phase, required | ({"workers"} if kind == "saturation" else {"arrivalRatePerSecond"} | issue_fields) |
                              ({"utilization"} if kind == "utilization" else set()), "load phase")
        if phase["id"] != phase_id or phase["kind"] != kind:
            raise common.ContractError("load phases differ from frozen order")
        start = utc_timestamp(phase["measurementStartedAt"], "phase start")
        end = utc_timestamp(phase["measurementEndedAt"], "phase end")
        elapsed = milliseconds(phase["measurementElapsedSecondsMonotonic"], "phase elapsed seconds")
        if end <= start or previous_end is not None and start < previous_end or elapsed < points["phaseDurationSeconds"]:
            raise common.ContractError("load phase window does not satisfy frozen duration/order")
        previous_end = end
        if kind == "saturation":
            if phase["workers"] != supply_sizing["workers"]:
                raise common.ContractError("saturation workers differ from frozen native sizing")
        else:
            rate = points["utilizations"][int(phase_id.rsplit("-", 1)[1])] * throughput if kind == "utilization" else 1000 / points["sparseMeanGapMilliseconds"]
            if phase["arrivalRatePerSecond"] != rate or kind == "utilization" and phase["utilization"] != utilization:
                raise common.ContractError("Poisson phase rate differs from measured saturation/frozen operating point")
        requests, samples, latencies = phase["requests"], phase["nativeSamples"], phase["latencyMs"]
        if not isinstance(requests, list) or not isinstance(samples, list) or not isinstance(latencies, list) or not samples:
            raise common.ContractError("load phase must retain requests and native samples")
        if len(samples) > plan["maxLatencySamples"] or len(latencies) != len(samples):
            raise common.ContractError("load phase native/client samples exceed budget or disagree")
        completed = common.require_integer(phase["completedInputs"], "phase completed inputs", 1)
        if completed != len(requests) or completed != len(samples) * plan["batch"]:
            raise common.ContractError("load phase completed inputs do not match fixed-batch membership")
        if kind != "saturation":
            issue_elapsed = milliseconds(phase["issueElapsedSecondsMonotonic"], "issue elapsed seconds")
            achieved_rate = milliseconds(phase["achievedArrivalRatePerSecond"], "achieved arrival rate")
            lag = common.require_object(phase["issueLagMs"], "issue lag")
            common.require_fields(lag, {"count", "min", "mean", "p50", "p95", "p99", "max"}, "issue lag")
            values = {key: milliseconds(lag[key], "issue lag " + key, False)
                      for key in ("min", "mean", "p50", "p95", "p99", "max")}
            if (common.require_integer(lag["count"], "issue lag count", 1) != completed or
                    phase["issueLagToleranceMs"] != 10 or values["max"] > 10 or
                    issue_elapsed > elapsed or achieved_rate != completed / issue_elapsed or
                    not values["min"] <= values["p50"] <= values["p95"] <= values["p99"] <= values["max"] or
                    not values["min"] <= values["mean"] <= values["max"]):
                raise common.ContractError("Poisson issue evidence disagrees with completed inputs or schedule tolerance")
        rows, groups = {}, {}
        for row in requests:
            row = common.require_object(row, "load request")
            common.require_fields(row, {"requestID", "recordID", "arrivalAt", "sentAt", "completedAt", "latencyMs", "response"}, "load request")
            request_id = common.require_string(row["requestID"], "load request ID")
            common.require_string(row["recordID"], "load record ID")
            if request_id in request_ids:
                raise common.ContractError("load request IDs must be unique across phases")
            request_ids.add(request_id)
            rows[request_id] = row
            arrival = utc_timestamp(row["arrivalAt"], "request arrival")
            sent = utc_timestamp(row["sentAt"], "request send")
            completed_at = utc_timestamp(row["completedAt"], "request completion")
            if not arrival <= sent < completed_at:
                raise common.ContractError("load-node request timestamps are unordered")
            milliseconds(row["latencyMs"], "request latency")
            response = common.require_object(row["response"], "native response")
            progress = common.require_object(response.get("progress"), "native response progress")
            if {"batchRequestIDs", "batchStartedAt", "batchFinishedAt", "serviceSample",
                    "preprocessMS", "backendMS", "postprocessMS", "firstTokenMS"}.intersection(progress):
                raise common.ContractError("retained native progress must omit repeated batch proof")
            if response.get("id") != request_id or response.get("executionComplete") is not True or progress.get("requestID") != request_id:
                raise common.ContractError("native response identity/completion disagrees with load request")
            if progress.get("batchSize") != plan["batch"] or milliseconds(progress.get("nativeElapsedMS"), "native elapsed span") > row["latencyMs"]:
                raise common.ContractError("retained native request batch size or elapsed span is invalid")
            actual_batch_id = common.require_integer(progress.get("batchID"), "native response batch ID", 1)
            common.require_integer(progress.get("units"), "native request units", 1)
            groups.setdefault(actual_batch_id, []).append(row)
        known = 0
        covered = set()
        for sample, latency in zip(samples, latencies):
            sample = common.require_object(sample, "native sample")
            batch_id = common.require_integer(sample["batchID"], "load batch ID", 1)
            if batch_id in batch_ids or sample["batchSize"] != plan["batch"] or sample["trialID"] != plan["podUID"]:
                raise common.ContractError("load batch identity/size/provenance mismatch")
            batch_ids.add(batch_id)
            if sample["precedingIdleNanos"] is not None:
                common.require_integer(sample["precedingIdleNanos"], "load preceding idle")
                known += 1
            common.require_integer(sample["serviceDurationNanos"], "load service duration", 1)
            matching = groups.get(batch_id, [])
            if len(matching) != plan["batch"]:
                raise common.ContractError("load native sample membership is incomplete")
            ids = common.require_names(sample.get("requestIDs"), "native sample request IDs")
            if (len(ids) != plan["batch"] or len(set(ids)) != plan["batch"] or
                    set(ids) != {row["requestID"] for row in matching}):
                raise common.ContractError("load native batch membership is invalid")
            if covered.intersection(ids):
                raise common.ContractError("load requests belong to multiple native samples")
            covered.update(ids)
            if sample["recordIDs"] != [rows[item]["recordID"] for item in ids] or sample["units"] != [rows[item]["response"]["progress"]["units"] for item in ids]:
                raise common.ContractError("load native record/units evidence differs from request membership")
            service = sample["serviceDurationNanos"] / 1_000_000
            stages = (milliseconds(sample["preprocessMs"], "native preprocess", False) +
                      milliseconds(sample["backendCallMs"], "native backend") +
                      milliseconds(sample["postprocessMs"], "native postprocess", False))
            if stages > service or service > milliseconds(sample["nativeRequestMs"], "native request span"):
                raise common.ContractError("native timing spans are not nested")
            if sample["nativeRequestMs"] > milliseconds(latency, "batch client latency") or latency != max(rows[item]["latencyMs"] for item in ids):
                raise common.ContractError("load native span exceeds correlated maximum client latency")
        if len(covered) != len(requests) or known < plan["minServiceSamples"]:
            raise common.ContractError("load phase lacks complete membership or minimum known-gap batches")
        if kind == "saturation":
            throughput = completed / elapsed


def validate_metadata(metadata: dict[str, Any], request: dict[str, Any], cpu_max: str,
                      affinity: list[int], cpuset: str) -> dict[str, Any]:
    run = request["runConfig"]
    state = run["state"]
    actual = common.require_object(metadata.get("runtime"), "actual runtime metadata")
    backend = state["backend"]
    policy = common.require_object(metadata.get("policy"), "actual actor policy")
    if (metadata.get("model") != state["model"] or metadata.get("mode") != "profiling" or
            metadata.get("maxQueueRequests") != state["firstfloor"]["maxQueueSize"] or
            metadata.get("maxBatchSize") != state["firstfloor"]["maxBatchSize"] or
            policy.get("deadlinePolicy") != deadline_policy(state["firstfloor"]) or
            policy.get("fixedBatchSize") != run["measurement"]["batch"] or
            metadata.get("modelInstanceCount") != 1 or
            metadata.get("maxQueueBytes") != min(request["maxInputBytes"], FRAME_BYTES) or
            metadata.get("maxRequestBytes") != FRAME_BYTES or
            metadata.get("profileID") or metadata.get("decisionID")):
        raise common.ContractError("actual actor configuration differs from frozen profiling conditions")
    if actual.get("runtime") != backend or actual.get("synthetic") is not False:
        raise common.ContractError("actual runtime must match the real frozen backend")
    preprocessor = state["preprocessor"]
    model_files = sorted(str(Path(request["modelDirectory"]) / Path(item).name) for item in state["artifactFiles"])
    if (actual.get("modelFiles") != model_files or actual.get("inputs") != preprocessor["inputs"] or
            actual.get("outputs") != preprocessor["outputs"]):
        raise common.ContractError("actual model files/input/output metadata differs from frozen declarations")
    for field in ("intraOpThreads", "interOpThreads"):
        common.require_integer(actual.get(field), f"actual {field}", 0)
    if (actual.get("cpuAffinity") != affinity or actual.get("initializedBatches") != initialized_batches(run) or
            actual.get("warmupIterations") != run["actor"].get("warmupIterations", 0)):
        raise common.ContractError("actual initialized batches, warmup, affinity or threads differ from frozen conditions")
    providers = actual.get("providers")
    if not isinstance(providers, list) or (
        backend != "rknnlite" and providers != [PROVIDERS[backend]] and
        not (backend == "ort-cuda" and providers == ["CUDAExecutionProvider", "CPUExecutionProvider"])
    ) or (backend == "rknnlite" and (providers or actual.get("device") != "npu")):
        raise common.ContractError("actual provider differs from the requested backend (fallback is forbidden)")
    if backend == "ort-cpu" and (providers != ["CPUExecutionProvider"] or actual.get("device") != "cpu" or actual.get("gpuUUID")):
        raise common.ContractError("CPU execution must use only the CPU provider/device")
    if backend == "ort-cuda":
        uuid = common.require_string(actual.get("gpuUUID"), "actual GPU UUID")
        if not uuid.startswith("GPU-") or actual.get("device") != "cuda":
            raise common.ContractError("CUDA execution requires one whole physical GPU")
        if uuid != run["delivery"]["runtimeAccess"]["gpuUUID"]:
            raise common.ContractError("actual GPU UUID differs from frozen allocation")
    if actual.get("cgroupCPUMax") != cpu_max or actual.get("cpuSet") != cpuset:
        raise common.ContractError("actual runner cgroup quota/cpuset differs from measurement container")
    available = actual.get("availableProviders", [])
    if not isinstance(available, list) or (backend != "rknnlite" and PROVIDERS[backend] not in available):
        raise common.ContractError("actual available providers do not support the selected backend")
    return actual


class FirstfloorSession:
    """Inspect one native actor without generating traffic on the measured node."""
    def __init__(self, port: int, request: dict[str, Any]):
        self.request = request
        self.connection = http.client.HTTPConnection("127.0.0.1", port, timeout=None)
        self.path = "/v2/models/" + quote(request["runConfig"]["state"]["model"], safe="")
        self.baseline: dict[str, int] = {}
    def json_call(self, method: str, path: str, payload: bytes | None = None) -> dict[str, Any]:
        try:
            self.connection.request(method, path, payload, {"Content-Type": "application/json"})
            response = self.connection.getresponse()
            data = response.read(FRAME_BYTES + 1)
        except (OSError, http.client.HTTPException) as error:
            raise ExecutionError("native actor connection failed") from error
        if len(data) > FRAME_BYTES:
            raise common.ContractError("native response exceeds IPC frame budget")
        if response.status != 200:
            failure_reason = "ExecutionFailed"
            if response.status == 500:
                try:
                    failure = json.loads(data, object_pairs_hook=common.no_duplicate_object)
                except (ValueError, common.ContractError):
                    failure = None
                if (isinstance(failure, dict) and failure.get("failureReason") == "MemoryAllocationFailed"
                        and failure.get("executionComplete") is True and isinstance(failure.get("error"), str)):
                    failure_reason = "MemoryAllocationFailed"
            raise ExecutionError(f"native actor {path} HTTP {response.status}: {data[:1024]!r}", failure_reason)
        return common.require_object(json.loads(data, object_pairs_hook=common.no_duplicate_object), "actor response")

    def metadata(self) -> dict[str, Any]:
        return self.json_call("GET", self.path + "/metadata")

    def counts(self) -> dict[str, int]:
        state = self.json_call("GET", self.path + "/state")
        if state.get("ready") is not True or state.get("requests") not in (None, []):
            raise common.ContractError("actor must be ready with no outstanding requests at measurement boundaries")
        return {key: common.require_integer(state.get(key), "actual actor " + key)
                for key in ("acceptedRequests", "completedRequests", "executions")}


    def get_providers(self) -> list[str]:
        return self.metadata()["runtime"]["providers"]



def wait_ready(process: subprocess.Popen, session: FirstfloorSession, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    session.connection.timeout = min(timeout, 1.0)
    while True:
        if process.poll() is not None:
            raise common.ContractError(f"serving-worker exited before readiness: {process.returncode}")
        try:
            session.metadata()
            session.connection.close()  # the probe socket keeps its 1 s connect-time timeout
            session.connection.timeout = None
            return
        except (OSError, http.client.HTTPException, common.ContractError, ExecutionError):
            session.connection.close()
        if time.monotonic() >= deadline:
            raise common.ContractError("serving-worker model readiness timed out")
        time.sleep(common.SIGNAL_POLL_SECONDS)


def worker_config(request: dict[str, Any]) -> dict[str, Any]:
    # Go freezes the common startup body. Only the profiling lifecycle envelope
    # differs; never reconstruct runtime/provider/domains or runner arguments here.
    config = json.loads(json.dumps(request["runConfig"]["actor"]))
    config.update(mode="profiling", profilePath="",
                  runnerTimeoutMS=request["observerReadyTimeoutMilliseconds"],
                  maxQueueBytes=min(request["maxInputBytes"], FRAME_BYTES),
                  maxRequestBytes=FRAME_BYTES)
    config["policy"] = {"deadlinePolicy": deadline_policy(request["runConfig"]["state"]["firstfloor"]),
                        "fixedBatchSize": request["runConfig"]["measurement"]["batch"]}
    return config


def execute(request: dict[str, Any], workspace: Path) -> dict[str, Any]:
    validate_request(request)
    run = request["runConfig"]
    architecture = platform.machine()
    if common.normalize_architecture(architecture) != common.normalize_architecture(run["targetNode"]["architecture"]):
        raise common.ContractError("actual architecture differs from frozen Node")
    cpu_max, affinity, cpuset = common.read_cpu_environment()
    sampler = memory_sampler(run)
    try:
        return execute_sampled(request, workspace, run, architecture, cpu_max, affinity, cpuset, sampler)
    finally:
        if sampler is not None:
            sampler.close()


def execute_active(request: dict[str, Any], process: subprocess.Popen, session: FirstfloorSession,
                   control: LoadControl, actual: dict[str, Any], architecture: str, cpu_max: str,
                   affinity: list[int], cpuset: str, sampler: common.MemorySampler | None) -> dict[str, Any]:
    run = request["runConfig"]
    state, measurement = run["state"], run["measurement"]
    record_stage("Observer")
    timeout = request["observerReadyTimeoutMilliseconds"] / 1000
    for ready_path in request["observerReadyPaths"]:
        common.wait_for_signal(Path(ready_path), timeout)
    warmup_completed = common.now()
    common.publish_immutable(Path(request["workloadWindowStartPath"]), b"signal\n")
    common.wait_for_signal(Path(request["workloadWindowReadyPath"]), timeout)
    session.baseline = session.counts()
    session.connection.close()
    common.publish_immutable(Path(request["measurementStartPath"]), b"signal\n")
    stat_before = common.cpu_stat()
    process_before = time.process_time()
    started_at, started = common.now(), time.monotonic()
    if sampler is not None:
        sampler.start()
    def finalize_measurement() -> dict[str, Any]:
        if sampler is not None:
            memory = sampler.stop()
        else:
            peak, source = memory_peak()
            memory = {"memoryBytes": peak, "memorySource": source}
        ended_at = common.now()
        elapsed = time.monotonic() - started
        common.publish_immutable(Path(request["measurementEndPath"]), b"signal\n")
        return {"memory": memory, "endedAt": ended_at, "elapsed": elapsed,
                "processCPU": time.process_time() - process_before, "cpuStat": common.cpu_stat()}

    control.finalize_measurement = finalize_measurement
    record_stage("Execution")
    control.ready.set()
    uploaded = control.wait_result(process)
    phases = uploaded["phases"]
    observation = control.final_observation
    if observation is None:
        raise common.ContractError("load result lacks frozen measurement-end observations")
    elapsed, ended_at, memory = observation["elapsed"], observation["endedAt"], observation["memory"]
    process_cpu, stat_after = observation["processCPU"], observation["cpuStat"]
    record_stage("ResultValidation")
    latencies = [latency for phase in phases for latency in phase["latencyMs"]]
    completed_inputs = sum(phase["completedInputs"] for phase in phases)
    return {"schemaVersion": common.RESULT_SCHEMA, "status": "Succeeded", "experimentId": request["podUID"],
            "nodeName": request["scheduledNodeName"], "model": state["model"], "architecture": architecture,
            "runtime": state["runtimeFamily"], "runtimeVersion": actual.get("runtimeVersion", ""),
            "backend": state["backend"], "provider": PROVIDERS[state["backend"]],
            "availableProviders": actual.get("availableProviders", []), "batchSize": measurement["batch"],
            "warmupIterations": measurement["warmupIterations"], "warmupCompletedAt": warmup_completed,
            "inferenceCount": len(latencies), "measurementMode": "duration",
            "measurementStartedAt": started_at, "measurementEndedAt": ended_at,
            "measurementElapsedSecondsMonotonic": elapsed,
            "cpuContract": common.runtime_cpu_evidence(cpu_max, affinity, cpuset, process_cpu, stat_before, stat_after),
            "derivedExecutionContract": common.derived_contract(run), "phases": phases, "supplySizing": uploaded["supplySizing"],
            "completionIndex": 0, "inputs": state["preprocessor"]["inputs"], "measurementIterations": 0,
            "targetDurationSecondsPerRepetition": float(measurement["durationSeconds"]),
            "latencyMs": {"mean": sum(latencies) / len(latencies),
                          "p50": common.percentile(latencies, .5), "p95": common.percentile(latencies, .95),
                          "p99": common.percentile(latencies, .99)},
            "throughputItemsPerSecond": completed_inputs / elapsed, "power": None, "energyPerRequest": None, **memory}


def execute_sampled(request: dict[str, Any], workspace: Path, run: dict[str, Any],
                    architecture: str, cpu_max: Any, affinity: Any, cpuset: Any,
                    sampler: common.MemorySampler | None) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="firstfloor-", dir=workspace) as directory:
        config_path = Path(directory) / "worker.json"
        delivery_path = Path(directory) / "delivery.json"
        delivery_path.write_text(json.dumps(run["delivery"], allow_nan=False), encoding="utf-8")
        config = worker_config(request)
        if run["state"]["backend"] == "rknnlite":
            config["runnerCommand"] = [str(delivery_path) if arg == "/config/delivery.json" else arg
                                       for arg in config["runnerCommand"]]
        config_path.write_text(json.dumps(config, allow_nan=False), encoding="utf-8")
        session = FirstfloorSession(8080, request)
        control = LoadControl(request) if run["measurement"]["workloadMode"] != common.IDLE_WORKLOAD else None
        child_environment = None
        if run["state"]["backend"] == "ort-cuda":
            child_environment = dict(os.environ, CUDA_VISIBLE_DEVICES=run["delivery"]["runtimeAccess"]["gpuUUID"])
        try:
            process = subprocess.Popen(["/opt/chill/bin/serving-worker", "--config", str(config_path),
                                        "--bind-address", "0.0.0.0:8080", "--node-name", request["scheduledNodeName"]],
                                       stdout=sys.stderr, stderr=sys.stderr, start_new_session=True, env=child_environment)
        except Exception:
            if control is not None:
                control.close()
            session.connection.close()
            raise
        try:
            wait_ready(process, session, request["observerReadyTimeoutMilliseconds"] / 1000.0)
            loaded_at = common.now()
            actual = validate_metadata(session.metadata(), request, cpu_max, affinity, cpuset)
            measurement = run["measurement"]
            if measurement["workloadMode"] == common.IDLE_WORKLOAD:
                session.baseline = session.counts()
                # No requests own this socket during settling/measurement. End
                # its lifetime before idle so the final metadata/counters use a
                # new connection, rather than one expired by worker IdleTimeout.
                session.connection.close()
                result = common.execute_idle(request, actual.get("runtimeVersion", ""), session, actual.get("availableProviders", []),
                                             architecture, cpu_max, affinity, cpuset, loaded_at, PROVIDERS[run["state"]["backend"]],
                                             memory_sampler=sampler, phase=record_stage)
                executions = 0
            else:
                result = execute_active(request, process, session, control, actual, architecture,
                                        cpu_max, affinity, cpuset, sampler)
                executions = result["inferenceCount"]
            observed = session.counts()
            expected = {"acceptedRequests": executions * measurement["batch"],
                        "completedRequests": executions * measurement["batch"], "executions": executions}
            if any(observed[key] - session.baseline[key] != expected[key] for key in observed):
                raise common.ContractError("actual actor counters changed across idle or disagree with uploaded input/batch samples")
            if process.poll() is not None:
                raise common.ContractError("native actor exited during measurement")
            result["firstfloor"] = {"runtime": actual, "mode": "profiling", "modelInstanceCount": 1,
                                     "maxBatchSize": run["state"]["firstfloor"]["maxBatchSize"],
                                     "maxQueueSize": run["state"]["firstfloor"]["maxQueueSize"],
                                     "deadlinePolicy": deadline_policy(run["state"]["firstfloor"]),
                                     "executionCount": executions, "inferenceCount": executions * measurement["batch"],
                                     "batchExecutionCounts": {str(measurement["batch"]): executions} if executions else {}}
            if sampler is None and measurement["workloadMode"] == common.IDLE_WORKLOAD:
                result["memoryBytes"], result["memorySource"] = memory_peak()
            result["derivedExecutionContract"] = common.derived_contract(run, PROVIDERS[run["state"]["backend"]])
            result["cpuContract"]["ortIntraOpThreads"] = actual["intraOpThreads"]
            result["cpuContract"]["ortInterOpThreads"] = actual["interOpThreads"]
            if run["state"]["backend"] == "rknnlite":
                facts = actual["rknn"]
                # Active K comes from actual completed actor samples; idle K was actually queried during initialization.
                selected = result["phases"][0]["nativeSamples"][0]["batchSize"] if executions else measurement["batch"]
                result["rknn"] = dict(facts, selectedBatch=selected)
            return result
        except Exception as error:
            # Preserve the actual cause before shutdown can consume the attempt
            # deadline; a runtime crash must not turn into a measurement budget.
            record_failure(error)
            raise
        finally:
            session.connection.close()
            if control is not None:
                control.close()
            # The worker owns runner shutdown; kill the group only after its grace period.
            try:
                process.terminate()
                process.wait(timeout=10)
            except ProcessLookupError:
                process.wait()
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, 9)
                process.wait()


def main() -> None:
    arguments = common.parse_args()
    try:
        record_stage("Startup")
        request = common.decode_request(sys.stdin.buffer)
        result = execute(request, arguments.result_file.parent)
        common.publish_immutable(arguments.result_file, common.encode_result(result, request["maxResultBytes"]))
    except (common.ContractError, OSError, ValueError, http.client.HTTPException, ExecutionError) as error:
        record_failure(error)
        common.fail(str(error))


if __name__ == "__main__":
    main()
