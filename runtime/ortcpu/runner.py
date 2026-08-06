#!/usr/bin/env python3
"""Execute one frozen CHILL CPU ONNX Runtime measurement."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import platform
import re
import stat
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, BinaryIO


REQUEST_SCHEMA = "chill.dacs.io/ort-cpu-runtime-request.v1alpha9"
RETAINED_REQUEST_SCHEMA = "chill.dacs.io/ort-cpu-runtime-request.v1alpha8"
INTENT_SCHEMA = "chill.dacs.io/profiling-run-intent.v1alpha9"
RETAINED_INTENT_SCHEMAS = {
    "chill.dacs.io/profiling-run-intent.v1alpha8",
    "chill.dacs.io/profiling-run-intent.v1alpha7",
    "chill.dacs.io/profiling-run-intent.v1alpha6",
}
ACTIVE_ONLY_RETAINED_INTENT_SCHEMA = "chill.dacs.io/profiling-run-intent.v1alpha6"
RESULT_SCHEMA = "chill.dacs.io/batch-characterization.v1alpha2"
DERIVED_CONTRACT_SCHEMA = "chill.dacs.io/cpu-execution-contract.v1alpha1"
SUPPORTED_WORKLOAD = "saturated-serial-fixed-batch"
SUPPORTED_INPUT_GENERATOR = "numpy-default-rng-seed-0-fp32-v1"
SUPPORTED_OUTPUT_VALIDATION = "nonempty-shape-batch-v1"
IDLE_WORKLOAD = "idle-model-loaded"
NO_INPUT_GENERATOR = "none"
NO_OUTPUT_VALIDATION = "none"
IDLE_RESULT_SCHEMA = "chill.dacs.io/model-loaded-idle-runtime.v1alpha2"
MAX_REQUEST_BYTES = 1 << 20
INPUT_GENERATION_CHUNK_ELEMENTS = 8192
SIGNAL_POLL_SECONDS = 0.01
DIGEST_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
CGROUP_ROOT = Path("/sys/fs/cgroup")


class ContractError(Exception):
    """The frozen runtime request cannot be executed faithfully."""


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def log(stage: str, message: str, **fields: object) -> None:
    suffix = " ".join(
        f"{key}={json.dumps(value, separators=(',', ':'))}" for key, value in fields.items()
    )
    print(f"{now()} INFO  [{stage:<12}] {message}" + (f" | {suffix}" if suffix else ""), file=sys.stderr, flush=True)


def fail(message: str) -> None:
    print(f"{now()} ERROR [runtime     ] {message}", file=sys.stderr, flush=True)
    raise SystemExit(1)


def no_duplicate_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    value: dict[str, Any] = {}
    for key, item in pairs:
        if key in value:
            raise ContractError(f"duplicate JSON field {key!r}")
        value[key] = item
    return value


def decode_request(stream: BinaryIO) -> dict[str, Any]:
    payload = stream.read(MAX_REQUEST_BYTES + 1)
    if len(payload) > MAX_REQUEST_BYTES:
        raise ContractError("runtime request exceeds the size limit")
    try:
        request = json.loads(
            payload,
            object_pairs_hook=no_duplicate_object,
            parse_constant=lambda value: (_ for _ in ()).throw(ContractError(f"invalid number {value}")),
        )
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ContractError(f"invalid runtime request JSON: {error}") from error
    if not isinstance(request, dict):
        raise ContractError("runtime request must be a JSON object")
    return request


def require_fields(value: dict[str, Any], fields: set[str], name: str) -> None:
    actual = set(value)
    if actual != fields:
        raise ContractError(f"{name} fields mismatch: missing={sorted(fields - actual)} unknown={sorted(actual - fields)}")


def require_object(value: Any, name: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ContractError(f"{name} must be an object")
    return value


def require_string(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ContractError(f"{name} must be a non-empty string without surrounding whitespace")
    return value


def require_integer(value: Any, name: str, minimum: int = 0) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise ContractError(f"{name} must be an integer >= {minimum}")
    return value


def require_absolute_file_path(value: Any, name: str) -> Path:
    raw = require_string(value, name)
    path = Path(raw)
    if not path.is_absolute() or path == Path("/") or os.path.normpath(raw) != raw:
        raise ContractError(f"{name} must be a clean absolute file path")
    return path


def validate_actuation(intent: dict[str, Any], state: dict[str, Any]) -> None:
    power_mode = require_string(state["powerMode"], "intent.state.powerMode")
    if "actuation" not in intent:
        if power_mode != "fixed":
            raise ContractError("non-fixed power mode requires actuation")
        return
    if intent["schemaVersion"] != INTENT_SCHEMA:
        raise ContractError("retained intent cannot contain actuation")
    if power_mode == "fixed":
        raise ContractError("fixed power mode cannot contain actuation")
    actuation = require_object(intent["actuation"], "intent.actuation")
    require_fields(actuation, {"powerMode", "actuator", "policyDigest"}, "intent.actuation")
    if require_string(actuation["powerMode"], "intent.actuation.powerMode") != power_mode:
        raise ContractError("intent.actuation.powerMode does not match intent.state.powerMode")
    if not DIGEST_PATTERN.fullmatch(
        require_string(actuation["policyDigest"], "intent.actuation.policyDigest")
    ):
        raise ContractError("intent.actuation.policyDigest must be a canonical SHA-256 digest")
    actuator = require_object(actuation["actuator"], "intent.actuation.actuator")
    kind = require_string(actuator.get("kind"), "intent.actuation.actuator.kind")
    if kind == "JetsonNVPModel":
        require_fields(actuator, {"kind", "jetsonNVPModel"}, "intent.actuation.actuator")
        target = require_object(actuator["jetsonNVPModel"], "intent.actuation.actuator.jetsonNVPModel")
        require_fields(target, {"id"}, "intent.actuation.actuator.jetsonNVPModel")
        require_integer(target["id"], "intent.actuation.actuator.jetsonNVPModel.id")
        return
    if kind == "NvidiaLockedGPUClock":
        require_fields(actuator, {"kind", "nvidiaLockedGPUClock"}, "intent.actuation.actuator")
        target = require_object(
            actuator["nvidiaLockedGPUClock"],
            "intent.actuation.actuator.nvidiaLockedGPUClock",
        )
        fields = {"minMHz", "maxMHz"}
        if "mode" in target:
            fields.add("mode")
        require_fields(target, fields, "intent.actuation.actuator.nvidiaLockedGPUClock")
        minimum = require_integer(
            target["minMHz"], "intent.actuation.actuator.nvidiaLockedGPUClock.minMHz", 1
        )
        maximum = require_integer(
            target["maxMHz"], "intent.actuation.actuator.nvidiaLockedGPUClock.maxMHz", 1
        )
        if maximum < minimum:
            raise ContractError("intent actuation GPU clock range is invalid")
        if "mode" in target and require_integer(
            target["mode"], "intent.actuation.actuator.nvidiaLockedGPUClock.mode"
        ) not in {0, 1}:
            raise ContractError("intent actuation GPU clock mode must be 0 or 1")
        return
    raise ContractError("unsupported intent actuation kind")


def validate_request(request: dict[str, Any]) -> None:
    require_fields(
        request,
        {
            "schemaVersion", "intent", "intentDigest", "podUID", "scheduledNodeName", "modelPath", "observerReadyPaths",
            "workloadWindowStartPath", "measurementStartPath", "workloadWindowReadyPath", "measurementEndPath",
            "observerReadyTimeoutMilliseconds", "maxLatencySamples",
            "maxInputBytes", "maxResultBytes",
        },
        "runtime request",
    )
    if request["schemaVersion"] not in {REQUEST_SCHEMA, RETAINED_REQUEST_SCHEMA}:
        raise ContractError("unsupported runtime request schemaVersion")
    digest = require_string(request["intentDigest"], "intentDigest")
    if not DIGEST_PATTERN.fullmatch(digest):
        raise ContractError("intentDigest must be a canonical SHA-256 digest")
    require_string(request["podUID"], "podUID")
    scheduled_node_name = require_string(request["scheduledNodeName"], "scheduledNodeName")
    require_absolute_file_path(request["modelPath"], "modelPath")
    ready_paths = request["observerReadyPaths"]
    if not isinstance(ready_paths, list) or not ready_paths:
        raise ContractError("observerReadyPaths must be a non-empty array")
    normalized_ready_paths = [require_absolute_file_path(value, "observerReadyPaths item") for value in ready_paths]
    if len(set(normalized_ready_paths)) != len(normalized_ready_paths):
        raise ContractError("observerReadyPaths must be unique")
    require_absolute_file_path(request["workloadWindowStartPath"], "workloadWindowStartPath")
    require_absolute_file_path(request["measurementStartPath"], "measurementStartPath")
    require_absolute_file_path(request["workloadWindowReadyPath"], "workloadWindowReadyPath")
    require_absolute_file_path(request["measurementEndPath"], "measurementEndPath")
    require_integer(request["observerReadyTimeoutMilliseconds"], "observerReadyTimeoutMilliseconds", 1)
    require_integer(request["maxLatencySamples"], "maxLatencySamples", 1)
    require_integer(request["maxInputBytes"], "maxInputBytes", 1)
    require_integer(request["maxResultBytes"], "maxResultBytes", 1)

    intent = require_object(request["intent"], "intent")
    intent_fields = {
        "schemaVersion", "candidate", "state", "images", "targetNode", "cpu",
        "measurement", "metrics", "workload",
    }
    if "actuation" in intent:
        intent_fields.add("actuation")
    require_fields(intent, intent_fields, "intent")
    if intent["schemaVersion"] not in {INTENT_SCHEMA, *RETAINED_INTENT_SCHEMAS}:
        raise ContractError("CPU ORT runtime requires a supported CPU intent schema")
    if request["schemaVersion"] == REQUEST_SCHEMA and intent["schemaVersion"] != INTENT_SCHEMA:
        raise ContractError("current runtime request requires the current intent schema")
    if request["schemaVersion"] == RETAINED_REQUEST_SCHEMA and intent["schemaVersion"] not in RETAINED_INTENT_SCHEMAS:
        raise ContractError("retained runtime request requires a retained intent schema")

    state = require_object(intent["state"], "intent.state")
    require_fields(
        state,
        {"deviceClass", "model", "artifact", "artifactDigest", "artifactPath", "runtimeFamily", "backend", "powerMode"},
        "intent.state",
    )
    if state["runtimeFamily"] != "onnxruntime" or state["backend"] != "CPUExecutionProvider":
        raise ContractError("unsupported runtime family or backend")
    if not DIGEST_PATTERN.fullmatch(require_string(state["artifactDigest"], "intent.state.artifactDigest")):
        raise ContractError("artifactDigest must be a canonical SHA-256 digest")
    require_string(state["model"], "intent.state.model")
    validate_actuation(intent, state)

    target = require_object(intent["targetNode"], "intent.targetNode")
    require_fields(target, {"name", "uid", "resourceVersion", "architecture", "allocatableCPU"}, "intent.targetNode")
    for field in ("name", "uid", "resourceVersion", "architecture", "allocatableCPU"):
        require_string(target[field], f"intent.targetNode.{field}")
    if scheduled_node_name != target["name"]:
        raise ContractError("scheduled Node does not match the frozen target Node")

    cpu = require_object(intent["cpu"], "intent.cpu")
    require_fields(
        cpu,
        {"policy", "policyVersion", "request", "limit", "ortIntraOpThreads", "ortInterOpThreads", "exclusivityClaimed"},
        "intent.cpu",
    )
    if cpu["policy"] != "AllocatableCPUQuota" or cpu["policyVersion"] != "v1" or cpu["exclusivityClaimed"] is not False:
        raise ContractError("unsupported CPU policy")
    require_integer(cpu["ortIntraOpThreads"], "intent.cpu.ortIntraOpThreads", 1)
    if require_integer(cpu["ortInterOpThreads"], "intent.cpu.ortInterOpThreads", 1) != 1:
        raise ContractError("CPU ORT runtime requires one inter-op thread")
    if require_string(cpu["limit"], "intent.cpu.limit") != target["allocatableCPU"]:
        raise ContractError("CPU limit does not match the frozen allocatable CPU")

    measurement = require_object(intent["measurement"], "intent.measurement")
    common_measurement_fields = {
        "batch", "warmupIterations", "durationSeconds", "repetitions",
        "workloadMode", "inputGenerator", "outputValidation",
    }
    if measurement.get("workloadMode") == IDLE_WORKLOAD:
        require_fields(measurement, common_measurement_fields | {"settlingDurationSeconds"}, "intent.measurement")
    else:
        require_fields(measurement, common_measurement_fields, "intent.measurement")
    require_integer(measurement["batch"], "intent.measurement.batch", 1)
    require_integer(measurement["warmupIterations"], "intent.measurement.warmupIterations")
    require_integer(measurement["durationSeconds"], "intent.measurement.durationSeconds", 1)
    if measurement["repetitions"] != 1:
        raise ContractError("CPU ORT runtime requires exactly one repetition")
    if measurement["workloadMode"] == IDLE_WORKLOAD:
        if intent["schemaVersion"] == ACTIVE_ONLY_RETAINED_INTENT_SCHEMA:
            raise ContractError("v1alpha6 intent cannot contain model-loaded idle measurement")
        if (
            measurement["batch"] != 1
            or measurement["warmupIterations"] != 0
            or require_integer(measurement["settlingDurationSeconds"], "intent.measurement.settlingDurationSeconds", 1) < 1
            or measurement["inputGenerator"] != NO_INPUT_GENERATOR
            or measurement["outputValidation"] != NO_OUTPUT_VALIDATION
        ):
            raise ContractError("unsupported model-loaded idle contract")
    elif (
        measurement["workloadMode"] != SUPPORTED_WORKLOAD
        or measurement["inputGenerator"] != SUPPORTED_INPUT_GENERATOR
        or measurement["outputValidation"] != SUPPORTED_OUTPUT_VALIDATION
    ):
        raise ContractError("unsupported measurement implementation contract")


def sha256_file(path: Path) -> str:
    try:
        info = path.lstat()
    except OSError as error:
        raise ContractError(f"inspect model artifact: {error}") from error
    if not stat.S_ISREG(info.st_mode):
        raise ContractError("model artifact is not a regular file")
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1 << 20), b""):
                digest.update(chunk)
    except OSError as error:
        raise ContractError(f"read model artifact: {error}") from error
    return "sha256:" + digest.hexdigest()


def read_required_text(path: Path, name: str) -> str:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError as error:
        raise ContractError(f"read {name}: {error}") from error
    if not value:
        raise ContractError(f"{name} is empty")
    return value


def wait_for_signal(path: Path, timeout_seconds: float) -> None:
    deadline = time.monotonic() + timeout_seconds
    while True:
        try:
            info = path.lstat()
        except FileNotFoundError:
            if time.monotonic() >= deadline:
                raise ContractError(f"timed out waiting for observer readiness at {path}")
            time.sleep(SIGNAL_POLL_SECONDS)
            continue
        except OSError as error:
            raise ContractError(f"inspect observer readiness signal at {path}: {error}") from error
        if not stat.S_ISREG(info.st_mode):
            raise ContractError(f"observer readiness signal at {path} is not a regular file")
        return


def publish_immutable(path: Path, payload: bytes) -> None:
    try:
        path.lstat()
    except FileNotFoundError:
        pass
    except OSError as error:
        raise ContractError(f"inspect output path: {error}") from error
    else:
        raise ContractError("output path already exists")

    temporary_name: str | None = None
    try:
        with tempfile.NamedTemporaryFile(prefix=".chill-runtime-", dir=path.parent, delete=False) as temporary:
            temporary_name = temporary.name
            temporary.write(payload)
            temporary.flush()
            os.fsync(temporary.fileno())
        os.link(temporary_name, path)
    except OSError as error:
        raise ContractError(f"publish immutable output: {error}") from error
    finally:
        if temporary_name is not None:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass


def encode_result(result: dict[str, Any], max_bytes: int) -> bytes:
    payload = json.dumps(
        result, sort_keys=True, separators=(",", ":"), allow_nan=False
    ).encode("utf-8")
    if len(payload) > max_bytes:
        raise ContractError("runtime result exceeds the Job execution contract size budget")
    return payload


def cgroup_cpu_files(root: Path = CGROUP_ROOT) -> tuple[Path, Path, Path | None, Path]:
    v2_quota = root / "cpu.max"
    if v2_quota.is_file():
        return root / "cpu.stat", v2_quota, None, root / "cpuset.cpus.effective"

    v1_cpu = root / "cpu,cpuacct"
    v1_cpuset = root / "cpuset"
    v1_quota = v1_cpu / "cpu.cfs_quota_us"
    if v1_quota.is_file():
        return (
            v1_cpu / "cpu.stat",
            v1_quota,
            v1_cpu / "cpu.cfs_period_us",
            v1_cpuset / "cpuset.cpus",
        )

    raise ContractError("supported cgroup CPU controller is unavailable")


def cpu_stat(root: Path = CGROUP_ROOT) -> dict[str, int]:
    stat_path, _, _, _ = cgroup_cpu_files(root)
    values: dict[str, int] = {}
    for line in read_required_text(stat_path, "cgroup CPU stat").splitlines():
        fields = line.split()
        if len(fields) != 2:
            raise ContractError("cgroup CPU stat is malformed")
        try:
            values[fields[0]] = int(fields[1])
        except ValueError as error:
            raise ContractError("cgroup CPU stat is malformed") from error
    return values


def validate_cpu_contract(
    intent: dict[str, Any], root: Path = CGROUP_ROOT
) -> tuple[str, list[int], str]:
    cpu = intent["cpu"]
    _, quota_path, period_path, cpuset_path = cgroup_cpu_files(root)
    quota_value = read_required_text(quota_path, "cgroup CPU quota")
    if period_path is None:
        cpu_max = quota_value
    else:
        period_value = read_required_text(period_path, "cgroup CPU period")
        cpu_max = f"{quota_value} {period_value}"
    fields = cpu_max.split()
    if len(fields) != 2 or fields[0] == "max":
        raise ContractError("cgroup CPU quota is missing or unlimited")
    try:
        quota, period, expected = int(fields[0]), int(fields[1]), int(cpu["limit"])
    except ValueError as error:
        raise ContractError("cgroup CPU quota or frozen limit is malformed") from error
    if quota < 1 or period < 1 or quota // period != expected or quota % period != 0:
        raise ContractError("cgroup CPU quota does not match the frozen limit")
    if not hasattr(os, "sched_getaffinity"):
        raise ContractError("process CPU affinity is unavailable")
    affinity = sorted(os.sched_getaffinity(0))
    if len(affinity) < cpu["ortIntraOpThreads"]:
        raise ContractError("process CPU affinity is smaller than the ORT thread contract")
    cpuset = read_required_text(cpuset_path, "effective CPU set")
    return cpu_max, affinity, cpuset


def normalize_architecture(value: str) -> str:
    return {"x86_64": "amd64", "aarch64": "arm64"}.get(value.lower().strip(), value.lower().strip())


def percentile(samples: list[float], fraction: float) -> float:
    ordered = sorted(samples)
    position = (len(ordered) - 1) * fraction
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] * (upper - position) + ordered[upper] * (position - lower)


def validate_outputs(outputs: list[Any], batch: int) -> list[list[int]]:
    if not outputs:
        raise ContractError("inference produced no outputs")
    shapes: list[list[int]] = []
    for output in outputs:
        shape = list(output.shape)
        if output.size < 1 or not shape or shape[0] != batch:
            raise ContractError("inference output shape does not preserve the frozen batch")
        shapes.append(shape)
    return shapes


def admitted_input_bytes(shape: list[int], max_bytes: int) -> int:
    input_bytes = math.prod(shape) * 4
    if input_bytes > max_bytes:
        raise ContractError("input tensor exceeds the Job execution contract size budget")
    return input_bytes


def generate_input(numpy: Any, shape: list[int], max_bytes: int) -> Any:
    input_bytes = admitted_input_bytes(shape, max_bytes)
    sample = numpy.empty(shape, dtype=numpy.float32)
    if getattr(sample, "nbytes", None) != input_bytes:
        raise ContractError("generated input tensor does not match its admitted byte size")

    # The v1 generator is defined by float64 standard_normal values cast to
    # float32. Filling one final tensor in bounded chunks preserves that seeded
    # sequence without retaining a full-size float64 temporary beside it.
    flat = sample.reshape(-1)
    generator = numpy.random.default_rng(0)
    for start in range(0, len(flat), INPUT_GENERATION_CHUNK_ELEMENTS):
        stop = min(start + INPUT_GENERATION_CHUNK_ELEMENTS, len(flat))
        flat[start:stop] = generator.standard_normal(stop - start)
    return sample


def measurement_complete(elapsed: float, duration: int, sample_count: int, max_samples: int) -> bool:
    if elapsed >= duration:
        return True
    if sample_count >= max_samples:
        raise ContractError("latency sample budget exhausted before measurement duration")
    return False


def derived_contract(intent: dict[str, Any], intent_digest: str) -> dict[str, Any]:
    cpu = intent["cpu"]
    return {
        "schemaVersion": DERIVED_CONTRACT_SCHEMA,
        "runIntentDigest": intent_digest,
        "backend": intent["state"]["backend"],
        "provider": intent["state"]["backend"],
        "nodeSnapshot": intent["targetNode"],
        "cpu": {
            "budgetSource": "KubernetesNodeStatusAllocatable",
            "exclusivityClaimed": cpu["exclusivityClaimed"],
            "limit": cpu["limit"],
            "observedAllocatable": intent["targetNode"]["allocatableCPU"],
            "policy": cpu["policy"],
            "policyVersion": cpu["policyVersion"],
        },
        "runtimeOptions": {"interOpThreads": cpu["ortInterOpThreads"], "intraOpThreads": cpu["ortIntraOpThreads"]},
        "scheduling": {"cpuRequest": cpu["request"], "status": "RequestedNotExclusive"},
    }


def runtime_cpu_evidence(
    cpu: dict[str, Any],
    cpu_max: str,
    affinity: list[int],
    cpuset: str,
    process_cpu_seconds: float,
    stat_before: dict[str, int],
    stat_after: dict[str, int],
) -> dict[str, Any]:
    return {
        "osCPUCount": os.cpu_count(),
        "cgroupCPUMax": cpu_max,
        "ortIntraOpThreads": cpu["ortIntraOpThreads"],
        "ortInterOpThreads": cpu["ortInterOpThreads"],
        "affinityCPUCount": len(affinity),
        "affinityCPUs": affinity,
        "cpusetCPUsEffective": cpuset,
        "processCPUSeconds": round(process_cpu_seconds, 6),
        "cgroupCPUStatDelta": {key: value - stat_before.get(key, 0) for key, value in stat_after.items()},
    }


def execute_idle(
    request: dict[str, Any],
    ort: Any,
    session: Any,
    available_providers: list[str],
    actual_digest: str,
    architecture: str,
    cpu_max: str,
    affinity: list[int],
    cpuset: str,
    model_load_completed_at: str,
) -> dict[str, Any]:
    intent = request["intent"]
    state, measurement, cpu = intent["state"], intent["measurement"], intent["cpu"]
    for ready_path in request["observerReadyPaths"]:
        wait_for_signal(Path(ready_path), request["observerReadyTimeoutMilliseconds"] / 1000.0)

    publish_immutable(Path(request["workloadWindowStartPath"]), b"signal\n")
    wait_for_signal(
        Path(request["workloadWindowReadyPath"]),
        request["observerReadyTimeoutMilliseconds"] / 1000.0,
    )

    settling_started_at = now()
    settling_started = time.monotonic()
    settling_duration = measurement["settlingDurationSeconds"]
    while True:
        remaining = settling_duration - (time.monotonic() - settling_started)
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    settling_completed_at = now()

    publish_immutable(Path(request["measurementStartPath"]), b"signal\n")
    stat_before = cpu_stat()
    process_before = time.process_time()
    measurement_started_at = now()
    monotonic_started = time.monotonic()
    duration = measurement["durationSeconds"]
    while True:
        remaining = duration - (time.monotonic() - monotonic_started)
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    measurement_elapsed = time.monotonic() - monotonic_started
    measurement_ended_at = now()
    providers_at_end = session.get_providers()
    model_resident_at_end = bool(providers_at_end and providers_at_end[0] == state["backend"])
    publish_immutable(Path(request["measurementEndPath"]), b"signal\n")
    process_cpu_seconds = time.process_time() - process_before
    stat_after = cpu_stat()

    result = {
        "schemaVersion": IDLE_RESULT_SCHEMA,
        "status": "Succeeded",
        "intentDigest": request["intentDigest"],
        "podUID": request["podUID"],
        "nodeName": request["scheduledNodeName"],
        "model": state["model"],
        "artifactDigest": actual_digest,
        "architecture": architecture,
        "runtime": "onnxruntime",
        "runtimeVersion": ort.__version__,
        "backend": state["backend"],
        "provider": providers_at_end[0] if providers_at_end else "",
        "availableProviders": available_providers,
        "modelLoadCompletedAt": model_load_completed_at,
        "settlingStartedAt": settling_started_at,
        "settlingCompletedAt": settling_completed_at,
        "measurementStartedAt": measurement_started_at,
        "measurementEndedAt": measurement_ended_at,
        "measurementElapsedSecondsMonotonic": round(measurement_elapsed, 6),
        "inferenceCountAfterModelLoad": 0,
        "modelResidentAtEnd": model_resident_at_end,
        "cpuContract": runtime_cpu_evidence(
            cpu, cpu_max, affinity, cpuset, process_cpu_seconds, stat_before, stat_after,
        ),
        "derivedExecutionContract": derived_contract(intent, request["intentDigest"]),
    }
    log("result", "completed model-loaded idle measurement", elapsedSeconds=round(measurement_elapsed, 3))
    return result


def execute(request: dict[str, Any], ort: Any, numpy: Any) -> dict[str, Any]:
    validate_request(request)
    intent = request["intent"]
    state, measurement, cpu = intent["state"], intent["measurement"], intent["cpu"]
    model_path = Path(request["modelPath"])

    actual_digest = sha256_file(model_path)
    if actual_digest != state["artifactDigest"]:
        raise ContractError("model artifact digest does not match the frozen intent")
    architecture = platform.machine()
    if normalize_architecture(architecture) != normalize_architecture(intent["targetNode"]["architecture"]):
        raise ContractError("runtime architecture does not match the frozen Node")
    cpu_max, affinity, cpuset = validate_cpu_contract(intent)

    available_providers = ort.get_available_providers()
    if state["backend"] not in available_providers:
        raise ContractError("frozen ONNX Runtime provider is unavailable")
    options = ort.SessionOptions()
    options.intra_op_num_threads = cpu["ortIntraOpThreads"]
    options.inter_op_num_threads = cpu["ortInterOpThreads"]
    try:
        session = ort.InferenceSession(str(model_path), sess_options=options, providers=[state["backend"]])
    except Exception as error:  # runtime-specific failure is normalized at this boundary
        raise ContractError(f"load ONNX model: {error}") from error
    if not session.get_providers() or session.get_providers()[0] != state["backend"]:
        raise ContractError("actual ONNX Runtime provider does not match the frozen backend")
    if measurement["workloadMode"] == IDLE_WORKLOAD:
        return execute_idle(
            request, ort, session, available_providers, actual_digest, architecture,
            cpu_max, affinity, cpuset, now(),
        )
    inputs = session.get_inputs()
    if len(inputs) != 1 or inputs[0].type != "tensor(float)":
        raise ContractError("input generator supports exactly one float32 tensor input")
    shape = [measurement["batch"]] + [dimension if isinstance(dimension, int) and dimension > 0 else 1 for dimension in inputs[0].shape[1:]]
    sample = generate_input(numpy, shape, request["maxInputBytes"])

    for ready_path in request["observerReadyPaths"]:
        wait_for_signal(Path(ready_path), request["observerReadyTimeoutMilliseconds"] / 1000.0)
    log("warmup", "starting frozen warm-up", iterations=measurement["warmupIterations"], inputShape=shape)
    output_shapes: list[list[int]] | None = None
    try:
        for _ in range(measurement["warmupIterations"]):
            outputs = session.run(None, {inputs[0].name: sample})
            output_shapes = validate_outputs(outputs, measurement["batch"])
    except Exception as error:
        raise ContractError(f"execute ONNX Runtime warm-up: {error}") from error
    warmup_completed_at = now()

    publish_immutable(Path(request["workloadWindowStartPath"]), b"signal\n")
    wait_for_signal(
        Path(request["workloadWindowReadyPath"]),
        request["observerReadyTimeoutMilliseconds"] / 1000.0,
    )
    publish_immutable(Path(request["measurementStartPath"]), b"signal\n")
    stat_before = cpu_stat()
    process_before = time.process_time()
    measurement_started_at = now()
    repetition_started_at = measurement_started_at
    monotonic_started = time.monotonic()
    samples: list[float] = []
    try:
        while True:
            inference_started = time.perf_counter_ns()
            outputs = session.run(None, {inputs[0].name: sample})
            samples.append((time.perf_counter_ns() - inference_started) / 1_000_000.0)
            if output_shapes is None:
                output_shapes = validate_outputs(outputs, measurement["batch"])
            if measurement_complete(
                time.monotonic() - monotonic_started,
                measurement["durationSeconds"],
                len(samples),
                request["maxLatencySamples"],
            ):
                break
    except Exception as error:
        raise ContractError(f"execute ONNX Runtime measurement: {error}") from error
    measurement_elapsed = time.monotonic() - monotonic_started
    repetition_ended_at = now()
    measurement_ended_at = repetition_ended_at
    publish_immutable(Path(request["measurementEndPath"]), b"signal\n")
    process_cpu_seconds = time.process_time() - process_before
    stat_after = cpu_stat()
    if output_shapes is None:
        raise ContractError("output validation evidence is missing")

    mean_latency = sum(samples) / len(samples)
    result = {
        "schemaVersion": RESULT_SCHEMA,
        "status": "Succeeded",
        "sweepId": request["intentDigest"],
        "experimentId": request["podUID"],
        "nodeName": request["scheduledNodeName"],
        "model": state["model"],
        "artifactDigest": actual_digest,
        "architecture": architecture,
        "runtime": "onnxruntime",
        "runtimeVersion": ort.__version__,
        "backend": state["backend"],
        "provider": session.get_providers()[0],
        "availableProviders": available_providers,
        "batchSize": measurement["batch"],
        "warmupIterations": measurement["warmupIterations"],
        "warmupCompletedAt": warmup_completed_at,
        "inferenceCount": len(samples),
        "measurementMode": "duration",
        "repetitions": 1,
        "measurementStartedAt": measurement_started_at,
        "measurementEndedAt": measurement_ended_at,
        "measurementElapsedSecondsMonotonic": round(measurement_elapsed, 6),
        "outputValidation": {"method": SUPPORTED_OUTPUT_VALIDATION, "passed": True, "outputShapes": output_shapes},
        "cpuContract": runtime_cpu_evidence(
            cpu, cpu_max, affinity, cpuset, process_cpu_seconds, stat_before, stat_after,
        ),
        "derivedExecutionContract": derived_contract(intent, request["intentDigest"]),
        "repetitionResults": [{
            "repetition": 1,
            "measurementStartedAt": repetition_started_at,
            "measurementEndedAt": repetition_ended_at,
            "latencyMs": [round(value, 6) for value in samples],
        }],
        "completionIndex": 0,
        "inputShape": shape,
        "measurementIterations": 0,
        "targetDurationSecondsPerRepetition": float(measurement["durationSeconds"]),
        "latencyMs": {
            "mean": round(mean_latency, 3),
            "p50": round(percentile(samples, 0.50), 3),
            "p95": round(percentile(samples, 0.95), 3),
            "p99": round(percentile(samples, 0.99), 3),
        },
        "throughputItemsPerSecond": round(measurement["batch"] * 1000.0 / mean_latency, 3),
        "power": None,
        "energyPerRequest": None,
        "bSat": None,
    }
    log("result", "completed frozen CPU ORT measurement", inferences=len(samples), elapsedSeconds=round(measurement_elapsed, 3))
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--result-file", required=True, type=Path)
    return parser.parse_args()


def main() -> None:
    arguments = parse_args()
    try:
        result_path = require_absolute_file_path(str(arguments.result_file), "result-file")
        request = decode_request(sys.stdin.buffer)
        import numpy  # imported only after the bounded process contract is decoded
        import onnxruntime as ort

        result = execute(request, ort, numpy)
        payload = encode_result(result, request["maxResultBytes"])
        publish_immutable(result_path, payload)
    except (ContractError, ImportError, ValueError, TypeError) as error:
        fail(str(error))


if __name__ == "__main__":
    main()
