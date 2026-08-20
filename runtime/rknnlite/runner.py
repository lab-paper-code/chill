#!/usr/bin/env python3
"""Execute one frozen CHILL RKNNLite measurement."""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import math
import os
import platform
import stat
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, BinaryIO


REQUEST_SCHEMA = "chill.dacs.io/rknnlite-runtime-request.v1alpha6"
INTENT_SCHEMA = "chill.dacs.io/profiling-run-intent.v1alpha12"
RESULT_SCHEMA = "chill.dacs.io/batch-characterization.v1alpha4"
IDLE_RESULT_SCHEMA = "chill.dacs.io/model-loaded-idle-runtime.v1alpha3"
DERIVED_CONTRACT_SCHEMA = "chill.dacs.io/rknnlite-execution-contract.v1alpha1"
SUPPORTED_WORKLOAD = "saturated-serial-fixed-batch"
SUPPORTED_INPUT_GENERATOR = "numpy-default-rng-seed-0-fp32-v1"
SUPPORTED_OUTPUT_VALIDATION = "nonempty-shape-batch-v1"
IDLE_WORKLOAD = "idle-model-loaded"
NO_INPUT_GENERATOR = "none"
NO_OUTPUT_VALIDATION = "none"
SUPPORTED_RUNTIME_PACKAGE = "rknn-toolkit-lite2"
SUPPORTED_TARGET_PLATFORM = "rk3588"
SUPPORTED_INPUT_LAYOUT = "NHWC"
SUPPORTED_INPUT_DATA_TYPE = "float32"
CORE_MASK_NAME = "NPU_CORE_0"
MAX_REQUEST_BYTES = 1 << 20
INPUT_GENERATION_CHUNK_ELEMENTS = 8192
SIGNAL_POLL_SECONDS = 0.01


class ContractError(Exception):
    """The frozen RKNNLite request cannot be executed faithfully."""


def now() -> str:
    return (
        datetime.now(timezone.utc)
        .isoformat(timespec="microseconds")
        .replace("+00:00", "Z")
    )


def log(stage: str, message: str, **fields: object) -> None:
    suffix = " ".join(
        f"{key}={json.dumps(value, separators=(',', ':'))}"
        for key, value in fields.items()
    )
    print(
        f"{now()} INFO  [{stage:<12}] {message}" + (f" | {suffix}" if suffix else ""),
        file=sys.stderr,
        flush=True,
    )


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
            parse_constant=lambda value: (_ for _ in ()).throw(
                ContractError(f"invalid number {value}")
            ),
        )
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ContractError(f"invalid runtime request JSON: {error}") from error
    if not isinstance(request, dict):
        raise ContractError("runtime request must be a JSON object")
    return request


def require_fields(value: dict[str, Any], fields: set[str], name: str) -> None:
    actual = set(value)
    if actual != fields:
        raise ContractError(
            f"{name} fields mismatch: missing={sorted(fields - actual)} unknown={sorted(actual - fields)}"
        )


def require_object(value: Any, name: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ContractError(f"{name} must be an object")
    return value


def require_string(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ContractError(
            f"{name} must be a non-empty string without surrounding whitespace"
        )
    return value


def require_integer(value: Any, name: str, minimum: int = 0) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise ContractError(f"{name} must be an integer >= {minimum}")
    return value


def require_boolean(value: Any, name: str) -> bool:
    if not isinstance(value, bool):
        raise ContractError(f"{name} must be a boolean")
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
    if power_mode == "fixed":
        raise ContractError("fixed power mode cannot contain actuation")
    actuation = require_object(intent["actuation"], "intent.actuation")
    require_fields(
        actuation, {"powerMode", "actuator", "policyName"}, "intent.actuation"
    )
    if (
        require_string(actuation["powerMode"], "intent.actuation.powerMode")
        != power_mode
    ):
        raise ContractError(
            "intent.actuation.powerMode does not match intent.state.powerMode"
        )
    require_string(actuation["policyName"], "intent.actuation.policyName")
    actuator = require_object(actuation["actuator"], "intent.actuation.actuator")
    kind = require_string(actuator.get("kind"), "intent.actuation.actuator.kind")
    if kind == "JetsonNVPModel":
        require_fields(
            actuator, {"kind", "jetsonNVPModel"}, "intent.actuation.actuator"
        )
        target = require_object(
            actuator["jetsonNVPModel"], "intent.actuation.actuator.jetsonNVPModel"
        )
        require_fields(target, {"id"}, "intent.actuation.actuator.jetsonNVPModel")
        require_integer(target["id"], "intent.actuation.actuator.jetsonNVPModel.id")
        return
    if kind == "NvidiaLockedGPUClock":
        require_fields(
            actuator, {"kind", "nvidiaLockedGPUClock"}, "intent.actuation.actuator"
        )
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
            "schemaVersion",
            "intent",
            "podUID",
            "scheduledNodeName",
            "modelPath",
            "candidate",
            "observerReadyPaths",
            "workloadWindowStartPath",
            "measurementStartPath",
            "workloadWindowReadyPath",
            "measurementEndPath",
            "observerReadyTimeoutMilliseconds",
            "maxLatencySamples",
            "maxInputBytes",
            "maxResultBytes",
        },
        "runtime request",
    )
    if request["schemaVersion"] != REQUEST_SCHEMA:
        raise ContractError("unsupported runtime request schemaVersion")
    require_string(request["podUID"], "podUID")
    scheduled_node = require_string(request["scheduledNodeName"], "scheduledNodeName")
    require_absolute_file_path(request["modelPath"], "modelPath")
    ready_paths = request["observerReadyPaths"]
    if not isinstance(ready_paths, list) or not ready_paths:
        raise ContractError("observerReadyPaths must be a non-empty array")
    normalized = [
        require_absolute_file_path(value, "observerReadyPaths item")
        for value in ready_paths
    ]
    if len(set(normalized)) != len(normalized):
        raise ContractError("observerReadyPaths must be unique")
    for field in (
        "workloadWindowStartPath",
        "measurementStartPath",
        "workloadWindowReadyPath",
        "measurementEndPath",
    ):
        require_absolute_file_path(request[field], field)
    require_integer(
        request["observerReadyTimeoutMilliseconds"],
        "observerReadyTimeoutMilliseconds",
        1,
    )
    require_integer(request["maxLatencySamples"], "maxLatencySamples", 1)
    require_integer(request["maxInputBytes"], "maxInputBytes", 1)
    require_integer(request["maxResultBytes"], "maxResultBytes", 1)

    intent = require_object(request["intent"], "intent")
    intent_fields = {
        "schemaVersion",
        "candidate",
        "state",
        "images",
        "targetNode",
        "cpu",
        "measurement",
        "metrics",
        "workload",
    }
    if "actuation" in intent:
        intent_fields.add("actuation")
    require_fields(intent, intent_fields, "intent")
    if intent["schemaVersion"] != INTENT_SCHEMA:
        raise ContractError("RKNNLite requires the current intent schema")
    candidate_identity = require_object(intent["candidate"], "intent.candidate")
    require_fields(
        candidate_identity,
        {"inputs", "executionPath", "verdict"},
        "intent.candidate",
    )
    require_string(
        candidate_identity["executionPath"], "intent.candidate.executionPath"
    )
    if candidate_identity["verdict"] != "Compatible":
        raise ContractError("intent candidate does not select RKNNLite")

    state = require_object(intent["state"], "intent.state")
    require_fields(
        state,
        {
            "deviceClass",
            "model",
            "artifact",
            "artifactPath",
            "runtimeFamily",
            "backend",
            "powerMode",
            "hostContract",
        },
        "intent.state",
    )
    if state["runtimeFamily"] != "rknnlite" or state["backend"] != "not-applicable":
        raise ContractError("unsupported runtime family or backend")
    require_string(state["model"], "intent.state.model")
    validate_actuation(intent, state)

    target = require_object(intent["targetNode"], "intent.targetNode")
    require_fields(
        target,
        {"name", "uid", "resourceVersion", "architecture", "allocatableCPU"},
        "intent.targetNode",
    )
    for field in ("name", "uid", "resourceVersion", "architecture", "allocatableCPU"):
        require_string(target[field], f"intent.targetNode.{field}")
    if scheduled_node != target["name"]:
        raise ContractError("scheduled Node does not match the frozen target Node")

    cpu = require_object(intent["cpu"], "intent.cpu")
    require_fields(
        cpu,
        {"policy", "policyVersion", "request", "limit", "exclusivityClaimed"},
        "intent.cpu",
    )
    if (
        cpu["policy"] != "AllocatableCPUQuota"
        or cpu["policyVersion"] != "v1"
        or cpu["exclusivityClaimed"] is not False
        or require_string(cpu["limit"], "intent.cpu.limit") != target["allocatableCPU"]
    ):
        raise ContractError("unsupported RKNNLite CPU quota contract")

    measurement = require_object(intent["measurement"], "intent.measurement")
    common_fields = {
        "batch",
        "warmupIterations",
        "durationSeconds",
        "workloadMode",
        "inputGenerator",
        "outputValidation",
    }
    if measurement.get("workloadMode") == IDLE_WORKLOAD:
        require_fields(
            measurement,
            common_fields | {"settlingDurationSeconds"},
            "intent.measurement",
        )
    else:
        require_fields(measurement, common_fields, "intent.measurement")
    require_integer(measurement["batch"], "intent.measurement.batch", 1)
    require_integer(
        measurement["warmupIterations"], "intent.measurement.warmupIterations"
    )
    require_integer(
        measurement["durationSeconds"], "intent.measurement.durationSeconds", 1
    )
    if measurement["workloadMode"] == IDLE_WORKLOAD:
        if (
            measurement["warmupIterations"] != 0
            or require_integer(
                measurement["settlingDurationSeconds"],
                "intent.measurement.settlingDurationSeconds",
                1,
            )
            < 1
            or measurement["inputGenerator"] != NO_INPUT_GENERATOR
            or measurement["outputValidation"] != NO_OUTPUT_VALIDATION
        ):
            raise ContractError("unsupported RKNNLite model-loaded idle contract")
    elif (
        measurement["workloadMode"] != SUPPORTED_WORKLOAD
        or measurement["inputGenerator"] != SUPPORTED_INPUT_GENERATOR
        or measurement["outputValidation"] != SUPPORTED_OUTPUT_VALIDATION
    ):
        raise ContractError("unsupported RKNNLite measurement implementation contract")

    validate_candidate(request["candidate"])


def validate_candidate(value: Any) -> None:
    candidate = require_object(value, "candidate")
    require_fields(
        candidate,
        {"artifactCapability", "runtimePackages", "hostContract", "runtimeAccess"},
        "candidate",
    )
    capability = require_object(
        candidate["artifactCapability"], "candidate.artifactCapability"
    )
    require_fields(
        capability,
        {"targetPlatform", "declaredBatchSizes", "inputSampleShape", "inputLayout", "inputDataType"},
        "candidate.artifactCapability",
    )
    declared_batch_sizes = capability["declaredBatchSizes"]
    if not isinstance(declared_batch_sizes, list) or not declared_batch_sizes:
        raise ContractError("candidate declaredBatchSizes must be a non-empty array")
    for declared_batch_size in declared_batch_sizes:
        require_integer(declared_batch_size, "candidate declaredBatchSizes item", 1)
    shape = capability["inputSampleShape"]
    if not isinstance(shape, list) or not shape:
        raise ContractError("candidate inputSampleShape must be a non-empty array")
    for value in shape:
        require_integer(value, "candidate inputSampleShape item", 1)
    if (
        capability["targetPlatform"] != SUPPORTED_TARGET_PLATFORM
        or capability["inputLayout"] != SUPPORTED_INPUT_LAYOUT
        or capability["inputDataType"] != SUPPORTED_INPUT_DATA_TYPE
    ):
        raise ContractError("candidate artifact capability is unsupported")

    packages = candidate["runtimePackages"]
    if not isinstance(packages, list) or len(packages) != 1:
        raise ContractError(
            "candidate must declare exactly one RKNNLite runtime package"
        )
    package = require_object(packages[0], "candidate.runtimePackages item")
    require_fields(package, {"name", "version"}, "candidate.runtimePackages item")
    if package["name"] != SUPPORTED_RUNTIME_PACKAGE:
        raise ContractError("candidate runtime package is unsupported")
    require_string(package["version"], "candidate runtime package version")

    host = require_object(candidate["hostContract"], "candidate.hostContract")
    require_fields(
        host,
        {"libraries", "driver", "devices", "platform", "allocation"},
        "candidate.hostContract",
    )
    libraries = host["libraries"]
    if not isinstance(libraries, list) or not libraries:
        raise ContractError("candidate host libraries must be a non-empty array")
    library_names: set[str] = set()
    for item in libraries:
        library = require_object(item, "candidate host library")
        require_fields(
            library, {"name", "hostPath", "containerPath"}, "candidate host library"
        )
        name = require_string(library["name"], "candidate host library name")
        if name in library_names:
            raise ContractError("candidate host library names must be unique")
        library_names.add(name)
        require_absolute_file_path(
            library["hostPath"], "candidate host library hostPath"
        )
        require_absolute_file_path(
            library["containerPath"], "candidate host library containerPath"
        )

    driver = require_object(host["driver"], "candidate host driver")
    require_fields(driver, {"name", "versionPath", "version"}, "candidate host driver")
    require_string(driver["name"], "candidate host driver name")
    require_absolute_file_path(
        driver["versionPath"], "candidate host driver versionPath"
    )
    require_string(driver["version"], "candidate host driver version")
    platform_contract = require_object(host["platform"], "candidate host platform")
    require_fields(
        platform_contract,
        {"compatiblePath", "compatibleToken"},
        "candidate host platform",
    )
    require_absolute_file_path(
        platform_contract["compatiblePath"], "candidate host platform compatiblePath"
    )
    require_string(
        platform_contract["compatibleToken"], "candidate host platform compatibleToken"
    )
    devices = host["devices"]
    if not isinstance(devices, list) or not devices:
        raise ContractError("candidate host devices must be a non-empty array")
    device_paths = [
        str(
            require_absolute_file_path(
                require_object(item, "candidate host device").get("path"),
                "candidate host device path",
            )
        )
        for item in devices
    ]
    for item in devices:
        require_fields(
            require_object(item, "candidate host device"),
            {"path"},
            "candidate host device",
        )
    if len(set(device_paths)) != len(device_paths):
        raise ContractError("candidate host device paths must be unique")
    allocation = require_object(host["allocation"], "candidate host allocation")
    require_fields(
        allocation, {"resourceName", "exclusivityClaimed"}, "candidate host allocation"
    )
    if (
        not isinstance(allocation["resourceName"], str)
        or allocation["resourceName"].strip() != allocation["resourceName"]
    ):
        raise ContractError("candidate host allocation resourceName is invalid")
    require_boolean(
        allocation["exclusivityClaimed"], "candidate host allocation exclusivityClaimed"
    )

    access = require_object(candidate["runtimeAccess"], "candidate.runtimeAccess")
    require_fields(
        access, {"privileged", "supplementalGroups"}, "candidate.runtimeAccess"
    )
    require_boolean(access["privileged"], "candidate.runtimeAccess.privileged")
    groups = access["supplementalGroups"]
    if not isinstance(groups, list):
        raise ContractError("candidate supplementalGroups must be an array")
    normalized_groups = [
        require_integer(group, "candidate supplemental group", 1) for group in groups
    ]
    if len(set(normalized_groups)) != len(normalized_groups):
        raise ContractError("candidate supplementalGroups must be unique")


def validate_regular_file(path: Path, name: str = "file") -> None:
    try:
        info = path.lstat()
    except OSError as error:
        raise ContractError(f"inspect {name}: {error}") from error
    if not stat.S_ISREG(info.st_mode):
        raise ContractError(f"{name} is not a regular file")


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
                raise ContractError(
                    f"timed out waiting for observer readiness at {path}"
                )
            time.sleep(SIGNAL_POLL_SECONDS)
            continue
        except OSError as error:
            raise ContractError(
                f"inspect observer readiness signal at {path}: {error}"
            ) from error
        if not stat.S_ISREG(info.st_mode):
            raise ContractError(
                f"observer readiness signal at {path} is not a regular file"
            )
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
        with tempfile.NamedTemporaryFile(
            prefix=".chill-runtime-", dir=path.parent, delete=False
        ) as temporary:
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
        raise ContractError(
            "runtime result exceeds the Job execution contract size budget"
        )
    return payload


def current_cgroup_v2_directory(
    membership_path: Path = Path("/proc/self/cgroup"),
    cgroup_root: Path = Path("/sys/fs/cgroup"),
) -> Path:
    membership = read_required_text(membership_path, "process cgroup membership")
    unified_paths = []
    for line in membership.splitlines():
        fields = line.split(":", 2)
        if len(fields) == 3 and fields[0] == "0" and fields[1] == "":
            unified_paths.append(fields[2])
    if len(unified_paths) != 1:
        raise ContractError("process does not have exactly one cgroup v2 membership")
    raw_path = unified_paths[0]
    if not raw_path.startswith("/"):
        raise ContractError("cgroup v2 membership path is not absolute")
    relative = Path(raw_path[1:])
    if any(part in {"", ".", ".."} for part in relative.parts):
        raise ContractError("cgroup v2 membership path is not clean")
    return cgroup_root / relative


def cpu_stat() -> dict[str, int]:
    values: dict[str, int] = {}
    stat_path = current_cgroup_v2_directory() / "cpu.stat"
    for line in read_required_text(stat_path, "cgroup CPU stat").splitlines():
        fields = line.split()
        if len(fields) != 2:
            raise ContractError("cgroup CPU stat is malformed")
        try:
            values[fields[0]] = int(fields[1])
        except ValueError as error:
            raise ContractError("cgroup CPU stat is malformed") from error
    return values


def validate_cpu_contract(intent: dict[str, Any]) -> tuple[str, list[int], str]:
    cpu = intent["cpu"]
    cgroup = current_cgroup_v2_directory()
    cpu_max = read_required_text(cgroup / "cpu.max", "cgroup CPU max")
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
    if not affinity:
        raise ContractError("process CPU affinity is empty")
    cpuset = read_required_text(cgroup / "cpuset.cpus.effective", "effective CPU set")
    return cpu_max, affinity, cpuset


def normalize_architecture(value: str) -> str:
    return {"x86_64": "amd64", "aarch64": "arm64"}.get(
        value.lower().strip(), value.lower().strip()
    )


def percentile(samples: list[float], fraction: float) -> float:
    ordered = sorted(samples)
    position = (len(ordered) - 1) * fraction
    lower, upper = math.floor(position), math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] * (upper - position) + ordered[upper] * (position - lower)


def validate_outputs(outputs: list[Any], batch: int) -> list[list[int]]:
    if outputs is None:
        raise ContractError(
            "runtime supplied no result for the frozen batch; "
            "the artifact does not provide this declared batch size"
        )
    if not outputs:
        raise ContractError("inference produced no outputs")
    shapes: list[list[int]] = []
    for output in outputs:
        shape = list(output.shape)
        if output.size < 1 or not shape or shape[0] != batch:
            raise ContractError(
                "inference output shape does not preserve the frozen batch"
            )
        shapes.append(shape)
    return shapes


def generate_input(numpy: Any, shape: list[int], max_bytes: int) -> Any:
    input_bytes = math.prod(shape) * 4
    if input_bytes > max_bytes:
        raise ContractError(
            "input tensor exceeds the Job execution contract size budget"
        )
    sample = numpy.empty(shape, dtype=numpy.float32)
    if getattr(sample, "nbytes", None) != input_bytes:
        raise ContractError(
            "generated input tensor does not match its admitted byte size"
        )
    flat = sample.reshape(-1)
    generator = numpy.random.default_rng(0)
    for start in range(0, len(flat), INPUT_GENERATION_CHUNK_ELEMENTS):
        stop = min(start + INPUT_GENERATION_CHUNK_ELEMENTS, len(flat))
        flat[start:stop] = generator.standard_normal(stop - start)
    return sample


def measurement_complete(
    elapsed: float, duration: int, sample_count: int, max_samples: int
) -> bool:
    if elapsed >= duration:
        return True
    if sample_count >= max_samples:
        raise ContractError(
            "latency sample budget exhausted before measurement duration"
        )
    return False


def observe_host_contract(
    candidate: dict[str, Any], selected_batch: int
) -> dict[str, Any]:
    host = candidate["hostContract"]
    package = candidate["runtimePackages"][0]
    try:
        package_version = importlib.metadata.version(package["name"])
    except importlib.metadata.PackageNotFoundError as error:
        raise ContractError(
            f"runtime package {package['name']} is not installed"
        ) from error
    if package_version != package["version"]:
        raise ContractError(
            "installed RKNNLite package version does not match the candidate"
        )

    libraries: list[dict[str, Any]] = []
    for library in host["libraries"]:
        path = Path(library["containerPath"])
        validate_regular_file(path, f"host library {library['name']}")
        libraries.append({"name": library["name"], "path": str(path)})

    driver = host["driver"]
    driver_version = read_required_text(
        Path(driver["versionPath"]), "RKNN driver version"
    )
    if driver_version != driver["version"]:
        raise ContractError("RKNN driver version does not match the candidate")

    platform_contract = host["platform"]
    compatible_path = Path(platform_contract["compatiblePath"])
    try:
        compatible = compatible_path.read_bytes()
    except OSError as error:
        raise ContractError(f"read platform compatible bytes: {error}") from error
    token = platform_contract["compatibleToken"].encode("utf-8")
    matched = token in compatible.split(b"\x00")
    if not matched:
        raise ContractError("platform compatible token does not match the candidate")

    devices: list[dict[str, Any]] = []
    for device in host["devices"]:
        path = Path(device["path"])
        try:
            info = path.lstat()
        except OSError as error:
            raise ContractError(f"inspect RKNN device {path}: {error}") from error
        character_device = stat.S_ISCHR(info.st_mode)
        if not character_device:
            raise ContractError(f"RKNN device {path} is not a character device")
        descriptor: int | None = None
        try:
            descriptor = os.open(path, os.O_RDWR | os.O_NONBLOCK)
        except OSError as error:
            raise ContractError(f"open RKNN device {path}: {error}") from error
        finally:
            if descriptor is not None:
                os.close(descriptor)
        devices.append({"path": str(path), "characterDevice": True, "opened": True})

    return {
        "package": {"name": package["name"], "version": package_version},
        "hostLibraries": libraries,
        "driver": {
            "name": driver["name"],
            "versionPath": driver["versionPath"],
            "version": driver_version,
        },
        "platform": {
            "compatiblePath": platform_contract["compatiblePath"],
            "compatibleToken": platform_contract["compatibleToken"],
            "matched": matched,
        },
        "devices": devices,
        "coreMask": CORE_MASK_NAME,
        "inputLayout": SUPPORTED_INPUT_LAYOUT,
        "initSucceeded": False,
        "loadSucceeded": False,
        "selectedBatch": selected_batch,
    }


def derived_contract(intent: dict[str, Any]) -> dict[str, Any]:
    cpu = intent["cpu"]
    return {
        "schemaVersion": DERIVED_CONTRACT_SCHEMA,
        "backend": intent["state"]["backend"],
        "nodeSnapshot": intent["targetNode"],
        "cpu": {
            "budgetSource": "KubernetesNodeStatusAllocatable",
            "exclusivityClaimed": cpu["exclusivityClaimed"],
            "limit": cpu["limit"],
            "observedAllocatable": intent["targetNode"]["allocatableCPU"],
            "policy": cpu["policy"],
            "policyVersion": cpu["policyVersion"],
        },
        "runtimeOptions": {"coreMask": CORE_MASK_NAME},
        "scheduling": {"cpuRequest": cpu["request"], "status": "RequestedNotExclusive"},
    }


def runtime_cpu_evidence(
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
        "affinityCPUCount": len(affinity),
        "affinityCPUs": affinity,
        "cpusetCPUsEffective": cpuset,
        "processCPUSeconds": round(process_cpu_seconds, 6),
        "cgroupCPUStatDelta": {
            key: value - stat_before.get(key, 0) for key, value in stat_after.items()
        },
    }


def execute_idle(
    request: dict[str, Any],
    rknn: Any,
    evidence: dict[str, Any],
    architecture: str,
    cpu_max: str,
    affinity: list[int],
    cpuset: str,
    model_load_completed_at: str,
) -> dict[str, Any]:
    intent = request["intent"]
    state, measurement = intent["state"], intent["measurement"]
    for ready_path in request["observerReadyPaths"]:
        wait_for_signal(
            Path(ready_path), request["observerReadyTimeoutMilliseconds"] / 1000.0
        )
    publish_immutable(Path(request["workloadWindowStartPath"]), b"signal\n")
    wait_for_signal(
        Path(request["workloadWindowReadyPath"]),
        request["observerReadyTimeoutMilliseconds"] / 1000.0,
    )
    settling_started_at = now()
    settling_started = time.monotonic()
    while True:
        remaining = measurement["settlingDurationSeconds"] - (
            time.monotonic() - settling_started
        )
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    settling_completed_at = now()

    publish_immutable(Path(request["measurementStartPath"]), b"signal\n")
    stat_before = cpu_stat()
    process_before = time.process_time()
    measurement_started_at = now()
    monotonic_started = time.monotonic()
    while True:
        remaining = measurement["durationSeconds"] - (
            time.monotonic() - monotonic_started
        )
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    measurement_elapsed = time.monotonic() - monotonic_started
    measurement_ended_at = now()
    publish_immutable(Path(request["measurementEndPath"]), b"signal\n")
    process_cpu_seconds = time.process_time() - process_before
    stat_after = cpu_stat()

    result = {
        "schemaVersion": IDLE_RESULT_SCHEMA,
        "status": "Succeeded",
        "podUID": request["podUID"],
        "nodeName": request["scheduledNodeName"],
        "model": state["model"],
        "architecture": architecture,
        "runtime": "rknnlite",
        "runtimeVersion": evidence["package"]["version"],
        "backend": state["backend"],
        "rknn": evidence,
        "modelLoadCompletedAt": model_load_completed_at,
        "settlingStartedAt": settling_started_at,
        "settlingCompletedAt": settling_completed_at,
        "measurementStartedAt": measurement_started_at,
        "measurementEndedAt": measurement_ended_at,
        "measurementElapsedSecondsMonotonic": round(measurement_elapsed, 6),
        "inferenceCountAfterModelLoad": 0,
        "modelResidentAtEnd": rknn is not None,
        "cpuContract": runtime_cpu_evidence(
            cpu_max, affinity, cpuset, process_cpu_seconds, stat_before, stat_after
        ),
        "derivedExecutionContract": derived_contract(intent),
    }
    log(
        "result",
        "completed RKNNLite model-loaded idle measurement",
        elapsedSeconds=round(measurement_elapsed, 3),
    )
    return result


def execute(request: dict[str, Any], rknn_type: Any, numpy: Any) -> dict[str, Any]:
    validate_request(request)
    intent = request["intent"]
    state, measurement = intent["state"], intent["measurement"]
    candidate = request["candidate"]
    model_path = Path(request["modelPath"])
    validate_regular_file(model_path, "RKNN artifact")
    if state["hostContract"] != candidate["hostContract"]:
        raise ContractError("candidate host contract does not match the frozen intent")
    architecture = platform.machine()
    if normalize_architecture(architecture) != normalize_architecture(
        intent["targetNode"]["architecture"]
    ):
        raise ContractError("runtime architecture does not match the frozen Node")
    cpu_max, affinity, cpuset = validate_cpu_contract(intent)
    evidence = observe_host_contract(candidate, measurement["batch"])

    rknn = rknn_type()
    try:
        try:
            load_result = rknn.load_rknn(str(model_path))
        except Exception as error:
            raise ContractError(f"load RKNN artifact: {error}") from error
        if load_result != 0:
            raise ContractError(f"load RKNN artifact returned {load_result}")
        evidence["loadSucceeded"] = True
        try:
            init_result = rknn.init_runtime(core_mask=rknn_type.NPU_CORE_0)
        except Exception as error:
            raise ContractError(f"initialize RKNNLite runtime: {error}") from error
        if init_result != 0:
            raise ContractError(f"initialize RKNNLite runtime returned {init_result}")
        evidence["initSucceeded"] = True
        model_load_completed_at = now()

        if measurement["workloadMode"] == IDLE_WORKLOAD:
            return execute_idle(
                request,
                rknn,
                evidence,
                architecture,
                cpu_max,
                affinity,
                cpuset,
                model_load_completed_at,
            )

        shape = [measurement["batch"], *candidate["artifactCapability"]["inputSampleShape"]]
        sample = generate_input(numpy, shape, request["maxInputBytes"])
        for ready_path in request["observerReadyPaths"]:
            wait_for_signal(
                Path(ready_path), request["observerReadyTimeoutMilliseconds"] / 1000.0
            )
        log(
            "warmup",
            "starting frozen RKNNLite warm-up",
            iterations=measurement["warmupIterations"],
            inputShape=shape,
        )
        output_shapes: list[list[int]] | None = None
        try:
            for _ in range(measurement["warmupIterations"]):
                outputs = rknn.inference(inputs=[sample])
                observed_shapes = validate_outputs(outputs, measurement["batch"])
                if output_shapes is not None and observed_shapes != output_shapes:
                    raise ContractError("RKNNLite output shapes changed during warm-up")
                output_shapes = observed_shapes
        except Exception as error:
            raise ContractError(f"execute RKNNLite warm-up: {error}") from error
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
                outputs = rknn.inference(inputs=[sample])
                samples.append(
                    (time.perf_counter_ns() - inference_started) / 1_000_000.0
                )
                observed_shapes = validate_outputs(outputs, measurement["batch"])
                if output_shapes is not None and observed_shapes != output_shapes:
                    raise ContractError(
                        "RKNNLite output shapes changed during measurement"
                    )
                output_shapes = observed_shapes
                if measurement_complete(
                    time.monotonic() - monotonic_started,
                    measurement["durationSeconds"],
                    len(samples),
                    request["maxLatencySamples"],
                ):
                    break
        except Exception as error:
            raise ContractError(f"execute RKNNLite measurement: {error}") from error
        measurement_elapsed = time.monotonic() - monotonic_started
        repetition_ended_at = now()
        publish_immutable(Path(request["measurementEndPath"]), b"signal\n")
        process_cpu_seconds = time.process_time() - process_before
        stat_after = cpu_stat()
        if output_shapes is None:
            raise ContractError("output validation evidence is missing")

        mean_latency = sum(samples) / len(samples)
        result = {
            "schemaVersion": RESULT_SCHEMA,
            "status": "Succeeded",
            "experimentId": request["podUID"],
            "nodeName": request["scheduledNodeName"],
            "model": state["model"],
            "architecture": architecture,
            "runtime": "rknnlite",
            "runtimeVersion": evidence["package"]["version"],
            "backend": state["backend"],
            "rknn": evidence,
            "batchSize": measurement["batch"],
            "warmupIterations": measurement["warmupIterations"],
            "warmupCompletedAt": warmup_completed_at,
            "inferenceCount": len(samples),
            "measurementMode": "duration",
            "measurementStartedAt": measurement_started_at,
            "measurementEndedAt": repetition_ended_at,
            "measurementElapsedSecondsMonotonic": round(measurement_elapsed, 6),
            "outputValidation": {
                "method": SUPPORTED_OUTPUT_VALIDATION,
                "passed": True,
                "outputShapes": output_shapes,
            },
            "cpuContract": runtime_cpu_evidence(
                cpu_max, affinity, cpuset, process_cpu_seconds, stat_before, stat_after
            ),
            "derivedExecutionContract": derived_contract(intent),
            "repetitionResults": [
                {
                    "repetition": 1,
                    "measurementStartedAt": repetition_started_at,
                    "measurementEndedAt": repetition_ended_at,
                    "latencyMs": [round(value, 6) for value in samples],
                }
            ],
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
            "throughputItemsPerSecond": round(
                measurement["batch"] * 1000.0 / mean_latency, 3
            ),
            "power": None,
            "energyPerRequest": None,
            "bSat": None,
        }
        log(
            "result",
            "completed frozen RKNNLite measurement",
            inferences=len(samples),
            elapsedSeconds=round(measurement_elapsed, 3),
        )
        return result
    finally:
        try:
            rknn.release()
        except Exception as error:
            log("cleanup", "RKNNLite release failed", error=str(error))


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--result-file", required=True, type=Path)
    return parser.parse_args()


def main() -> None:
    arguments = parse_args()
    try:
        result_path = require_absolute_file_path(
            str(arguments.result_file), "result-file"
        )
        request = decode_request(sys.stdin.buffer)
        import numpy
        from rknnlite.api import RKNNLite

        result = execute(request, RKNNLite, numpy)
        payload = encode_result(result, request["maxResultBytes"])
        publish_immutable(result_path, payload)
    except (ContractError, ImportError, ValueError, TypeError) as error:
        fail(str(error))


if __name__ == "__main__":
    main()
