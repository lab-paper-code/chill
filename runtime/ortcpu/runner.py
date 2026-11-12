"""CPU measurement contract helpers shared by the firstfloor measurement."""

from __future__ import annotations

import argparse
import json
import math
import os
import stat
import sys
import tempfile
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, BinaryIO


RUN_CONFIG_SCHEMA = "chill.dacs.io/profiling-run-config.v1alpha21"
RESULT_SCHEMA = "chill.dacs.io/batch-characterization.v1alpha12"
DERIVED_CONTRACT_SCHEMA = "chill.dacs.io/cpu-execution-contract.v1alpha3"
SUPPORTED_WORKLOAD = "empirical-cadence-fixed-batch"
IDLE_WORKLOAD = "idle-model-loaded"
IDLE_RESULT_SCHEMA = "chill.dacs.io/model-loaded-idle-runtime.v1alpha7"
MAX_REQUEST_BYTES = 1 << 20
SIGNAL_POLL_SECONDS = 0.01


def own_cgroup_root(root: Path = Path("/sys/fs/cgroup"), self_cgroup: Path = Path("/proc/self/cgroup")) -> Path:
    """Return this process's cgroup v2 directory. A privileged container (RKNN)
    shares the host cgroup namespace, so /sys/fs/cgroup is the host root, which
    has no cpu.max; its own cgroup is the 0:: path of /proc/self/cgroup."""
    # ponytail: v2 only; a privileged container on a cgroup v1 host keeps the root view.
    if (root / "cpu.max").is_file() or not (root / "cgroup.controllers").is_file():
        return root
    try:
        lines = self_cgroup.read_text().splitlines()
    except OSError:
        return root
    own = next((root / line[3:].lstrip("/") for line in lines if line.startswith("0::")), root)
    return own if own.is_dir() else root


CGROUP_ROOT = own_cgroup_root()


class ContractError(Exception):
    """The runtime request cannot be executed faithfully."""


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


def require_absolute_file_path(value: Any, name: str) -> Path:
    raw = require_string(value, name)
    path = Path(raw)
    if not path.is_absolute() or os.path.normpath(raw) == path.anchor:
        raise ContractError(f"{name} must be an absolute file path")
    return path


def validate_actuation(run_config: dict[str, Any], state: dict[str, Any]) -> None:
    power_mode = require_string(state["powerMode"], "run_config.state.powerMode")
    if "actuation" not in run_config:
        if power_mode != "fixed":
            raise ContractError("non-fixed power mode requires actuation")
        return
    if power_mode == "fixed":
        raise ContractError("fixed power mode cannot contain actuation")
    actuation = require_object(run_config["actuation"], "run_config.actuation")
    require_fields(
        actuation, {"powerMode", "actuator", "policyName"}, "run_config.actuation"
    )
    if (
        require_string(actuation["powerMode"], "run_config.actuation.powerMode")
        != power_mode
    ):
        raise ContractError(
            "run_config.actuation.powerMode does not match run_config.state.powerMode"
        )
    require_string(actuation["policyName"], "run_config.actuation.policyName")
    actuator = require_object(actuation["actuator"], "run_config.actuation.actuator")
    kind = require_string(actuator.get("kind"), "run_config.actuation.actuator.kind")
    if kind == "JetsonNVPModel":
        require_fields(
            actuator, {"kind", "jetsonNVPModel"}, "run_config.actuation.actuator"
        )
        target = require_object(
            actuator["jetsonNVPModel"], "run_config.actuation.actuator.jetsonNVPModel"
        )
        require_fields(target, {"id"}, "run_config.actuation.actuator.jetsonNVPModel")
        require_integer(target["id"], "run_config.actuation.actuator.jetsonNVPModel.id")
        return
    if kind == "NvidiaLockedGPUClock":
        require_fields(
            actuator, {"kind", "nvidiaLockedGPUClock"}, "run_config.actuation.actuator"
        )
        target = require_object(
            actuator["nvidiaLockedGPUClock"],
            "run_config.actuation.actuator.nvidiaLockedGPUClock",
        )
        fields = {"minMHz", "maxMHz"}
        if "mode" in target:
            fields.add("mode")
        require_fields(target, fields, "run_config.actuation.actuator.nvidiaLockedGPUClock")
        minimum = require_integer(
            target["minMHz"], "run_config.actuation.actuator.nvidiaLockedGPUClock.minMHz", 1
        )
        maximum = require_integer(
            target["maxMHz"], "run_config.actuation.actuator.nvidiaLockedGPUClock.maxMHz", 1
        )
        if maximum < minimum:
            raise ContractError("run_config actuation GPU clock range is invalid")
        if "mode" in target and require_integer(
            target["mode"], "run_config.actuation.actuator.nvidiaLockedGPUClock.mode"
        ) not in {0, 1}:
            raise ContractError("run_config actuation GPU clock mode must be 0 or 1")
        return
    raise ContractError("unsupported run_config actuation kind")


def require_names(value: Any, name: str) -> list[str]:
    if not isinstance(value, list) or not value:
        raise ContractError(f"{name} must be a non-empty array")
    names = [require_string(item, f"{name} item") for item in value]
    if len(set(names)) != len(names):
        raise ContractError(f"{name} must be unique")
    return names


def validate_preprocessor(value: Any) -> None:
    preprocessor = require_object(value, "run_config.state.preprocessor")
    require_fields(
        preprocessor,
        {"name", "uid", "image", "architecture", "path", "inputs", "outputs"}
        | ({"maxNewTokens"} if "maxNewTokens" in preprocessor else set()),
        "run_config.state.preprocessor",
    )
    for field in ("name", "uid", "image", "architecture"):
        require_string(preprocessor[field], f"run_config.state.preprocessor.{field}")
    require_absolute_file_path(preprocessor["path"], "run_config.state.preprocessor.path")
    if "maxNewTokens" in preprocessor:
        require_integer(preprocessor["maxNewTokens"], "run_config.state.preprocessor.maxNewTokens", 1)
    require_names(preprocessor["inputs"], "run_config.state.preprocessor.inputs")
    require_names(preprocessor["outputs"], "run_config.state.preprocessor.outputs")


def validate_request(
    request: dict[str, Any],
    request_schema: str,
    backend: str,
) -> None:
    run_config = require_object(request.get("runConfig"), "runConfig")
    measurement = require_object(run_config.get("measurement"), "run_config.measurement")
    active = measurement.get("workloadMode") != IDLE_WORKLOAD
    require_fields(
        request,
        {
            "schemaVersion",
            "runConfig",
            "podUID",
            "scheduledNodeName",
            "modelDirectory",
            "preprocessorPath",
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
    if request["schemaVersion"] != request_schema:
        raise ContractError("unsupported runtime request schemaVersion")
    require_string(request["podUID"], "podUID")
    scheduled_node_name = require_string(
        request["scheduledNodeName"], "scheduledNodeName"
    )
    require_absolute_file_path(request["modelDirectory"], "modelDirectory")
    require_absolute_file_path(request["preprocessorPath"], "preprocessorPath")
    ready_paths = request["observerReadyPaths"]
    if not isinstance(ready_paths, list) or not ready_paths:
        raise ContractError("observerReadyPaths must be a non-empty array")
    normalized_ready_paths = [
        os.path.normpath(require_absolute_file_path(value, "observerReadyPaths item"))
        for value in ready_paths
    ]
    if len(set(normalized_ready_paths)) != len(normalized_ready_paths):
        raise ContractError("observerReadyPaths must be unique")
    require_absolute_file_path(
        request["workloadWindowStartPath"], "workloadWindowStartPath"
    )
    require_absolute_file_path(request["measurementStartPath"], "measurementStartPath")
    require_absolute_file_path(
        request["workloadWindowReadyPath"], "workloadWindowReadyPath"
    )
    require_absolute_file_path(request["measurementEndPath"], "measurementEndPath")
    require_integer(
        request["observerReadyTimeoutMilliseconds"],
        "observerReadyTimeoutMilliseconds",
        1,
    )
    require_integer(request["maxLatencySamples"], "maxLatencySamples", 1)
    require_integer(request["maxInputBytes"], "maxInputBytes", 1)
    require_integer(request["maxResultBytes"], "maxResultBytes", 1)

    run_config_fields = {
        "schemaVersion",
        "candidate",
        "state",
        "images",
        "actor",
        "delivery",
        "targetNode",
        "measurement",
        "metrics",
        "workload",
    }
    if "actuation" in run_config:
        run_config_fields.add("actuation")
    if active:
        run_config_fields.update({"loadOriginNode", "loadOriginGatewayPod"})
    require_fields(run_config, run_config_fields, "runConfig")

    if run_config["schemaVersion"] != RUN_CONFIG_SCHEMA:
        raise ContractError("CPU ORT runtime requires a supported CPU RunConfig schema")

    state = require_object(run_config["state"], "run_config.state")
    actor = require_object(run_config["actor"], "run_config.actor")
    require_string(actor.get("model"), "actor.model")
    command = actor.get("runnerCommand")
    if not isinstance(command, list) or not command or any(not isinstance(item, str) or not item for item in command):
        raise ContractError("actor.runnerCommand must be a nonempty string array")
    for field in ("batchSizes", "warmupBatchSizes"):
        values = actor.get(field)
        if not isinstance(values, list) or not values:
            raise ContractError("actor." + field + " must be a nonempty batch domain")
        for batch in values:
            require_integer(batch, "actor." + field, 1)
    require_fields(
        state,
        {
            "deviceClass",
            "model",
            "artifact",
            "artifactFiles",
            "runtimeFamily",
            "backend",
            "powerMode",
            "firstfloor",
            "preprocessor",
        } | ({"hostContract"} if backend == "rknnlite" else set()),
        "run_config.state",
    )
    if (
        state["runtimeFamily"] != "firstfloor"
        or state["backend"] != backend
    ):
        raise ContractError("unsupported runtime family or backend")
    require_string(state["model"], "run_config.state.model")
    files = state["artifactFiles"]
    if not isinstance(files, list) or not files:
        raise ContractError("run_config.state.artifactFiles must be a non-empty array")
    basenames = {require_absolute_file_path(item, "run_config.state.artifactFiles item").name for item in files}
    if len(basenames) != len(files):
        raise ContractError("run_config.state.artifactFiles basenames must be unique")
    validate_preprocessor(state["preprocessor"])
    validate_actuation(run_config, state)

    target = require_object(run_config["targetNode"], "run_config.targetNode")
    require_fields(
        target,
        {"name", "uid", "resourceVersion", "architecture"} |
        ({"allocatableCPU"} if "allocatableCPU" in target else set()),
        "run_config.targetNode",
    )
    for field in target:
        require_string(target[field], f"run_config.targetNode.{field}")
    if scheduled_node_name != target["name"]:
        raise ContractError("scheduled Node does not match the RunConfig target Node")
    if active:
        origin = require_string(run_config["loadOriginNode"], "run_config.loadOriginNode")
        if origin == target["name"]:
            raise ContractError("profiling load origin must differ from measurement Node")
        gateway = require_object(run_config["loadOriginGatewayPod"], "run_config.loadOriginGatewayPod")
        require_fields(gateway, {"namespace", "name", "uid"}, "run_config.loadOriginGatewayPod")
        for field in gateway:
            require_string(gateway[field], "run_config.loadOriginGatewayPod." + field)


    common_measurement_fields = {
        "batch",
        "warmupIterations",
        "durationSeconds",
        "durationToleranceSeconds",
        "workloadMode",
    }
    require_fields(
        measurement,
        common_measurement_fields | ({"warmupBatchSizes"} if "warmupBatchSizes" in measurement else set()) |
        ({"startupWarmupIterations"} if "startupWarmupIterations" in measurement else set()) |
        ({"dataset", "sloPercentile", "minServiceSamples", "sloTargetLatencyNanos", "operatingPoints",
          "workloadName", "workloadUID"} |
         ({"workloadResourceVersion"} if "workloadResourceVersion" in measurement else set())
         if active else {"settlingDurationSeconds"}),
        "run_config.measurement",
    )
    require_integer(measurement["batch"], "run_config.measurement.batch", 1)
    require_integer(
        measurement["warmupIterations"], "run_config.measurement.warmupIterations"
    )
    require_integer(
        measurement["durationSeconds"], "run_config.measurement.durationSeconds", 1
    )
    tolerance = measurement["durationToleranceSeconds"]
    if (
        isinstance(tolerance, bool)
        or not isinstance(tolerance, (int, float))
        or not math.isfinite(tolerance)
        or not 0 <= tolerance < measurement["durationSeconds"]
    ):
        raise ContractError("run_config.measurement.durationToleranceSeconds must be finite, nonnegative and less than duration")
    if not active:
        if (
            measurement["warmupIterations"] != 0
            or require_integer(
                measurement["settlingDurationSeconds"],
                "run_config.measurement.settlingDurationSeconds",
                1,
            )
            < 1
        ):
            raise ContractError("unsupported model-loaded idle contract")
    elif measurement["workloadMode"] != SUPPORTED_WORKLOAD:
        raise ContractError("unsupported measurement implementation contract")
    else:
        percentile = measurement["sloPercentile"]
        if (isinstance(percentile, bool) or not isinstance(percentile, (int, float)) or
                not math.isfinite(percentile) or not 0 < percentile < 100):
            raise ContractError("run_config.measurement.sloPercentile must be in (0, 100)")
        minimum = require_integer(measurement["minServiceSamples"], "measurement.minServiceSamples", 1)
        if minimum != math.ceil(math.log(0.05) / math.log(percentile / 100)):
            raise ContractError("measurement.minServiceSamples must derive from the Workload SLO percentile")
        if measurement["durationSeconds"] < 30:
            raise ContractError("active measurement requires at least 30 seconds")
        if request["maxLatencySamples"] < minimum + 1:
            raise ContractError("latency sample budget cannot cover startup and the requested stop policy")
        for field in ("workloadName", "workloadUID"):
            require_string(measurement[field], "measurement." + field)
        require_integer(measurement["sloTargetLatencyNanos"], "measurement.sloTargetLatencyNanos", 1)
        dataset = require_object(measurement["dataset"], "run_config.measurement.dataset")
        require_fields(dataset, {"name", "uid", "image", "path"}, "run_config.measurement.dataset")
        for field in ("name", "uid", "image"):
            require_string(dataset[field], f"run_config.measurement.dataset.{field}")
        require_absolute_file_path(dataset["path"], "run_config.measurement.dataset.path")
        points = require_object(measurement["operatingPoints"], "measurement.operatingPoints")
        require_fields(points, {"utilizations", "sparseMeanGapMilliseconds", "phaseDurationSeconds",
                              "requestLifetimeSeconds", "deliveryMarginMilliseconds",
                              "framedDecodedRequestUpperBytes", "queueBytesCap"}, "measurement.operatingPoints")
        utilizations = points["utilizations"]
        if not isinstance(utilizations, list) or not utilizations:
            raise ContractError("operatingPoints.utilizations must be a nonempty array")
        for value in utilizations:
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or not 0 < value < 1:
                raise ContractError("operatingPoints.utilizations must contain finite values in (0, 1)")
        for field in ("sparseMeanGapMilliseconds",):
            value = points[field]
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value <= 0:
                raise ContractError("operatingPoints." + field + " must be finite and positive")
        require_integer(points["phaseDurationSeconds"], "operatingPoints.phaseDurationSeconds", 30)
        for field in ("requestLifetimeSeconds", "deliveryMarginMilliseconds", "framedDecodedRequestUpperBytes"):
            require_integer(points[field], "operatingPoints." + field, 1)
        queue_cap = require_integer(points["queueBytesCap"], "operatingPoints.queueBytesCap", 1)
        if queue_cap != min(request["maxInputBytes"], 64 << 20):
            raise ContractError("frozen queue byte cap differs from runtime request capacity")
        if measurement["durationSeconds"] != points["phaseDurationSeconds"] * (len(utilizations) + 2):
            raise ContractError("active measurement duration must cover its exact named phases")
    validate_candidate(run_config["delivery"])


def validate_candidate(value: Any) -> None:
    candidate = require_object(value, "run_config.delivery")
    optional_fields = {"runtimePackages", "hostContract", "runtimeAccess"} & set(candidate)
    require_fields(candidate, {"artifactCapability"} | optional_fields, "run_config.delivery")
    if "runtimePackages" in candidate:
        packages = candidate["runtimePackages"]
        if not isinstance(packages, list):
            raise ContractError("runtimePackages must be an array")
        names: set[str] = set()
        for package in packages:
            package = require_object(package, "runtime package")
            require_fields(package, {"name", "version"}, "runtime package")
            name = require_string(package["name"], "runtime package name")
            require_string(package["version"], "runtime package version")
            if name in names:
                raise ContractError("runtime package names must be unique")
            names.add(name)
    capability = require_object(
        candidate["artifactCapability"], "candidate.artifactCapability"
    )
    # Tensors are declared for RKNN artifacts only; the RKNN validator checks them.
    optional_fields = {"targetPlatform", "declaredBatchSizes", "tensors"} & set(capability)
    require_fields(capability, optional_fields, "candidate.artifactCapability")
    if "targetPlatform" in capability:
        require_string(capability["targetPlatform"], "candidate targetPlatform")
    if "declaredBatchSizes" in capability:
        batches = capability["declaredBatchSizes"]
        if not isinstance(batches, list) or not batches:
            raise ContractError("candidate declaredBatchSizes must be a non-empty array")
        for batch in batches:
            require_integer(batch, "candidate declaredBatchSizes item", 1)
        if batches != sorted(set(batches)):
            raise ContractError("candidate declaredBatchSizes must be strictly increasing")


def read_required_text(path: Path, name: str) -> str:
    try:
        with path.open(encoding="utf-8") as stream:
            value = stream.read(4097)
        if len(value) > 4096:
            raise ContractError(f"{name} exceeds the controller-file size limit")
        value = value.strip()
    except (OSError, UnicodeError) as error:
        raise ContractError(f"read {name}: {error}") from error
    if not value:
        raise ContractError(f"{name} is empty")
    return value


MEMORY_CURRENT_SOURCE = "cgroup-v2-memory.current-samples:measurement-window"


class MemorySampler:
    """Timestamped whole-container memory.current samples inside one measurement window.

    Used only where the kernel has no memory.peak; the result is the observed maximum of
    the retained samples, never a kernel high-water mark. The interval is only a scheduling
    target: the sampled maximum can miss spikes between samples, and the actual cadence is
    the observed gaps between the retained sample timestamps.
    """

    def __init__(self, interval_milliseconds: int, root: Path = CGROUP_ROOT):
        self.interval = interval_milliseconds / 1000.0
        self.interval_milliseconds = interval_milliseconds
        self.path = root / "memory.current"
        self.samples: list[dict[str, Any]] = []
        self.error: ContractError | None = None
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.loop, daemon=True)

    def sample(self) -> None:
        try:
            value = int(read_required_text(self.path, "cgroup memory current"))
        except ValueError as error:
            raise ContractError("cgroup memory current is malformed") from error
        if value < 1:
            raise ContractError("cgroup memory current must be positive")
        self.samples.append({"at": now(), "bytes": value})

    def loop(self) -> None:
        try:
            while not self.stopped.wait(self.interval):
                self.sample()
        except ContractError as error:
            self.error = error

    def start(self) -> None:
        self.sample()
        self.thread.start()

    def close(self) -> None:
        """Stop and join the sampler thread; safe on any exit path, before or after start."""
        self.stopped.set()
        if self.thread.ident is not None:
            self.thread.join()

    def stop(self) -> dict[str, Any]:
        """Take the closing sample; call before the window end timestamp is taken."""
        self.close()
        if self.error is not None:
            raise self.error
        self.sample()
        return {
            "memoryBytes": max(item["bytes"] for item in self.samples),
            "memorySource": MEMORY_CURRENT_SOURCE,
            "memorySamples": self.samples,
        }


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


def cgroup_cpu_files(root: Path = CGROUP_ROOT) -> tuple[Path, Path, Path | None, Path]:
    v2_quota = root / "cpu.max"
    if v2_quota.is_file():
        return root / "cpu.stat", v2_quota, None, root / "cpuset.cpus.effective"

    v1_cpu = root / "cpu,cpuacct"
    if not (v1_cpu / "cpu.cfs_quota_us").is_file():
        v1_cpu = root / "cpu"
    v1_cpuset = root / "cpuset"
    v1_effective = v1_cpuset / "cpuset.effective_cpus"
    if not v1_effective.is_file():
        v1_effective = v1_cpuset / "cpuset.cpus"
    v1_quota = v1_cpu / "cpu.cfs_quota_us"
    if v1_quota.is_file():
        return (
            v1_cpu / "cpu.stat",
            v1_quota,
            v1_cpu / "cpu.cfs_period_us",
            v1_effective,
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


def read_cpu_environment(root: Path = CGROUP_ROOT) -> tuple[str, list[int], str]:
    """Read actual CPU quota, process affinity and effective CPUs on v1/v2."""
    _, quota_path, period_path, cpuset_path = cgroup_cpu_files(root)
    cpu_max = read_required_text(quota_path, "cgroup CPU quota")
    if period_path is not None:
        cpu_max += " " + read_required_text(period_path, "cgroup CPU period")
    fields = cpu_max.split()
    if len(fields) != 2:
        raise ContractError("cgroup CPU quota/period is malformed")
    try:
        period = int(fields[1])
        quota = None if fields[0] in {"max", "-1"} else int(fields[0])
    except ValueError as error:
        raise ContractError("cgroup CPU quota/period is malformed") from error
    if period < 1 or (quota is not None and quota < 1):
        raise ContractError("cgroup CPU quota/period must be positive or unlimited")
    cpuset = read_required_text(cpuset_path, "effective CPU set")
    for item in cpuset.split(","):
        parts = item.split("-")
        if not 1 <= len(parts) <= 2 or any(not part.isascii() or not part.isdigit() for part in parts):
            raise ContractError("effective CPU set is malformed")
        if int(parts[0]) > int(parts[-1]):
            raise ContractError("effective CPU set range is reversed")
    if not hasattr(os, "sched_getaffinity"):
        raise ContractError("process CPU affinity is unavailable")
    return cpu_max, sorted(os.sched_getaffinity(0)), cpuset



def normalize_architecture(value: str) -> str:
    return {"x86_64": "amd64", "aarch64": "arm64"}.get(
        value.lower().strip(), value.lower().strip()
    )


def percentile(samples: list[float], fraction: float) -> float:
    ordered = sorted(samples)
    position = (len(ordered) - 1) * fraction
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] * (upper - position) + ordered[upper] * (position - lower)




def derived_contract(
    run_config: dict[str, Any], provider: str = "CPUExecutionProvider"
) -> dict[str, Any]:
    return {
        "schemaVersion": DERIVED_CONTRACT_SCHEMA,
        "backend": run_config["state"]["backend"],
        "provider": provider,
        "nodeSnapshot": run_config["targetNode"],
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
    runtime_version: str,
    session: Any,
    available_providers: list[str],
    architecture: str,
    cpu_max: str,
    affinity: list[int],
    cpuset: str,
    model_load_completed_at: str,
    expected_provider: str,
    memory_sampler: MemorySampler | None = None,
    phase: Any = None,
) -> dict[str, Any]:
    run_config = request["runConfig"]
    state, measurement = run_config["state"], run_config["measurement"]
    if phase is not None:
        phase("Observer")
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
    settling_duration = measurement["settlingDurationSeconds"]
    while True:
        remaining = settling_duration - (time.monotonic() - settling_started)
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    settling_completed_at = now()

    publish_immutable(Path(request["measurementStartPath"]), b"signal\n")
    if phase is not None:
        phase("Execution")
    stat_before = cpu_stat()
    process_before = time.process_time()
    measurement_started_at = now()
    monotonic_started = time.monotonic()
    if memory_sampler is not None:
        memory_sampler.start()
    duration = measurement["durationSeconds"]
    while True:
        remaining = duration - (time.monotonic() - monotonic_started)
        if remaining <= 0:
            break
        time.sleep(min(remaining, 0.1))
    measurement_elapsed = time.monotonic() - monotonic_started
    if phase is not None:
        phase("ResultValidation")
    memory = memory_sampler.stop() if memory_sampler is not None else {}
    measurement_ended_at = now()
    providers_at_end = session.get_providers()
    model_resident_at_end = bool(
        (providers_at_end and providers_at_end[0] == expected_provider)
        or (not providers_at_end and expected_provider == "")
    )
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
        "runtime": state["runtimeFamily"],
        "runtimeVersion": runtime_version,
        "backend": state["backend"],
        "provider": providers_at_end[0] if providers_at_end else "",
        "availableProviders": available_providers,
        "inputs": state["preprocessor"]["inputs"],
        "modelLoadCompletedAt": model_load_completed_at,
        "settlingStartedAt": settling_started_at,
        "settlingCompletedAt": settling_completed_at,
        "measurementStartedAt": measurement_started_at,
        "measurementEndedAt": measurement_ended_at,
        "measurementElapsedSecondsMonotonic": round(measurement_elapsed, 6),
        "inferenceCountAfterModelLoad": 0,
        "modelResidentAtEnd": model_resident_at_end,
        "cpuContract": runtime_cpu_evidence(
            cpu_max,
            affinity,
            cpuset,
            process_cpu_seconds,
            stat_before,
            stat_after,
        ),
        "derivedExecutionContract": derived_contract(run_config),
        **memory,
    }
    log(
        "result",
        "completed model-loaded idle measurement",
        elapsedSeconds=round(measurement_elapsed, 3),
    )
    return result




def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--result-file", required=True, type=Path)
    return parser.parse_args()
