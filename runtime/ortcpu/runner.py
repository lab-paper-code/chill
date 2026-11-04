"""CPU measurement contract helpers shared by the firstfloor measurement."""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import stat
import sys
import tempfile
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, BinaryIO


RUN_CONFIG_SCHEMA = "chill.dacs.io/profiling-run-config.v1alpha19"
RESULT_SCHEMA = "chill.dacs.io/batch-characterization.v1alpha9"
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
    if not path.is_absolute() or path == Path("/") or os.path.normpath(raw) != raw:
        raise ContractError(f"{name} must be a clean absolute file path")
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
        } | ({"datasetPath"} if active else set()),
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
    if active:
        require_absolute_file_path(request["datasetPath"], "datasetPath")
    ready_paths = request["observerReadyPaths"]
    if not isinstance(ready_paths, list) or not ready_paths:
        raise ContractError("observerReadyPaths must be a non-empty array")
    normalized_ready_paths = [
        require_absolute_file_path(value, "observerReadyPaths item")
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
        ({"cadence", "dataset", "sloPercentile", "minServiceSamples", "sloTargetLatencyNanos",
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
        validate_cadence(measurement["cadence"])
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
    validate_candidate(run_config["delivery"])


def validate_cadence(value: Any) -> None:
    """Validate the frozen input, never discover traces or generate runtime draws."""
    cadence = require_object(value, "cadence")
    require_fields(cadence, {"target", "provenance"}, "cadence")
    target = require_object(cadence["target"], "cadence.target")
    require_fields(target, {"loadContext", "boundary", "conditionalModel", "schedule"}, "cadence.target")
    require_string(target["loadContext"], "cadence loadContext")
    if (target["boundary"] != "native-exchange-end-to-next-start" or
            target["conditionalModel"] != "nearest-gap-uniform-physical-trial"):
        raise ContractError("unsupported cadence boundary or conditional model")
    schedule = target["schedule"]
    if not isinstance(schedule, list) or not schedule:
        raise ContractError("cadence schedule must be a nonempty array")
    names: list[str] = []
    for phase in schedule:
        phase = require_object(phase, "cadence target phase")
        require_fields(phase, {"name", "offeredLoad", "sharingContext"}, "cadence target phase")
        for field in phase:
            require_string(phase[field], "cadence phase " + field)
        if phase["name"] in names:
            raise ContractError("cadence target phase names must be unique")
        names.append(phase["name"])
    provenance = require_object(cadence["provenance"], "cadence.provenance")
    require_fields(provenance, {"seed", "drawRule", "sources", "phases", "draws"}, "cadence.provenance")
    seed = provenance["seed"]
    if (isinstance(seed, bool) or not isinstance(seed, int) or not -(1 << 63) <= seed < (1 << 63) or
            provenance["drawRule"] != "uniform-selected-records-with-replacement-frozen-cycle"):
        raise ContractError("cadence requires an integer provenance seed and supported frozen draw rule")
    sources = provenance["sources"]
    if not isinstance(sources, list) or not sources:
        raise ContractError("cadence requires frozen trace sources")
    for source in sources:
        source = require_object(source, "cadence source")
        require_fields(source, {"object", "boundary", "sourceRevision", "loadContext", "workload", "nodeUID",
                                "executionPath", "powerMode", "physicalInstanceID", "trialID", "observedBatch",
                                "windowStart", "windowEnd", "borrowed"}, "cadence source")
        obj = require_object(source["object"], "cadence source object")
        require_fields(obj, {"store", "key", "size"}, "cadence source object")
        for field in ("store", "key"):
            require_string(obj[field], "cadence object " + field)
        require_integer(obj["size"], "cadence object size", 1)
        for field in ("boundary", "sourceRevision", "loadContext", "workload", "nodeUID", "executionPath",
                      "powerMode", "physicalInstanceID", "trialID", "windowStart", "windowEnd"):
            require_string(source[field], "cadence source " + field)
        require_integer(source["observedBatch"], "cadence observedBatch", 1)
        if not isinstance(source["borrowed"], bool):
            raise ContractError("cadence borrowed must be boolean")
        try:
            window = []
            for field in ("windowStart", "windowEnd"):
                match = re.fullmatch(r"([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})"
                                     r"(?:\.([0-9]{1,9}))?(Z|[+-][0-9]{2}:[0-9]{2})", source[field])
                if match is None:
                    raise ValueError("invalid RFC3339 window")
                wall = datetime.fromisoformat(match[1] + match[3].replace("Z", "+00:00"))
                window.append((wall, int((match[2] or "").ljust(9, "0"))))
            if window[1] <= window[0]:
                raise ValueError("invalid window")
        except ValueError as error:
            raise ContractError("cadence source window is invalid") from error
    phases = provenance["phases"]
    if not isinstance(phases, list) or len(phases) != len(names):
        raise ContractError("cadence phases must match the target schedule")
    elements_by_phase: dict[str, list[Any]] = {}
    for phase, name in zip(phases, names):
        phase = require_object(phase, "cadence source phase")
        require_fields(phase, {"name", "elements"}, "cadence source phase")
        if phase["name"] != name:
            raise ContractError("cadence source phases must follow the target schedule")
        elements = phase["elements"]
        if not isinstance(elements, list) or not elements:
            raise ContractError("cadence phase elements must be nonempty")
        for element in elements:
            element = require_object(element, "cadence gap element")
            require_fields(element, {"gapNanos", "sourceIndex", "batchID"}, "cadence gap element")
            gap = require_integer(element["gapNanos"], "cadence gapNanos")
            source_index = require_integer(element["sourceIndex"], "cadence sourceIndex")
            batch_id = require_integer(element["batchID"], "cadence batchID", 1)
            if gap > (1 << 63) - 1 or source_index >= len(sources) or batch_id > (1 << 64) - 1:
                raise ContractError("cadence element exceeds its source or integer range")
        elements_by_phase[name] = elements
    draws = provenance["draws"]
    if not isinstance(draws, list) or not draws:
        raise ContractError("cadence requires explicit frozen draws")
    for draw in draws:
        draw = require_object(draw, "cadence draw")
        require_fields(draw, {"phase", "elementIndex"}, "cadence draw")
        name = require_string(draw["phase"], "cadence draw phase")
        index = require_integer(draw["elementIndex"], "cadence draw elementIndex")
        if name not in elements_by_phase or index >= len(elements_by_phase[name]):
            raise ContractError("cadence draw refers to an absent source element")


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


def measurement_complete(
    elapsed: float, duration: int, sample_count: int, max_samples: int, min_samples: int,
    known_gap_count: int,
) -> bool:
    if elapsed >= duration and known_gap_count >= min_samples:
        return True
    if sample_count >= max_samples:
        raise ContractError(
            "latency sample budget exhausted before measurement stop policy"
        )
    return False


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
) -> dict[str, Any]:
    run_config = request["runConfig"]
    state, measurement = run_config["state"], run_config["measurement"]
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


def execute_loaded(
    request: dict[str, Any],
    session: Any,
    runtime_version: str,
    available_providers: list[str],
    architecture: str,
    cpu_max: str,
    affinity: list[int],
    cpuset: str,
    before_measurement: Any,
    result_provider: str,
    memory_sampler: MemorySampler | None = None,
) -> dict[str, Any]:
    """Measure a loaded session whose startup and warm-up completed before the window."""
    run_config = request["runConfig"]
    state, measurement = run_config["state"], run_config["measurement"]

    for ready_path in request["observerReadyPaths"]:
        wait_for_signal(
            Path(ready_path), request["observerReadyTimeoutMilliseconds"] / 1000.0
        )
    warmup_completed_at = now()

    publish_immutable(Path(request["workloadWindowStartPath"]), b"signal\n")
    wait_for_signal(
        Path(request["workloadWindowReadyPath"]),
        request["observerReadyTimeoutMilliseconds"] / 1000.0,
    )
    before_measurement()
    publish_immutable(Path(request["measurementStartPath"]), b"signal\n")
    stat_before = cpu_stat()
    process_before = time.process_time()
    measurement_started_at = now()
    repetition_started_at = measurement_started_at
    monotonic_started = time.monotonic()
    if memory_sampler is not None:
        memory_sampler.start()
    samples: list[float] = []
    known_gap_count = 0
    try:
        while True:
            session.prepare_next()
            inference_started = time.perf_counter_ns()
            sample = session.run()
            samples.append((time.perf_counter_ns() - inference_started) / 1_000_000.0)
            if sample["precedingIdleNanos"] is not None:
                known_gap_count += 1
            if measurement_complete(
                time.monotonic() - monotonic_started,
                measurement["durationSeconds"],
                len(samples),
                request["maxLatencySamples"],
                measurement["minServiceSamples"],
                known_gap_count,
            ):
                break
    except Exception as error:
        raise ContractError(f"execute {state['runtimeFamily']} measurement: {error}") from error
    measurement_elapsed = time.monotonic() - monotonic_started
    memory = memory_sampler.stop() if memory_sampler is not None else {}
    repetition_ended_at = now()
    measurement_ended_at = repetition_ended_at
    publish_immutable(Path(request["measurementEndPath"]), b"signal\n")
    process_cpu_seconds = time.process_time() - process_before
    stat_after = cpu_stat()

    mean_latency = sum(samples) / len(samples)
    result = {
        "schemaVersion": RESULT_SCHEMA,
        "status": "Succeeded",
        "experimentId": request["podUID"],
        "nodeName": request["scheduledNodeName"],
        "model": state["model"],
        "architecture": architecture,
        "runtime": state["runtimeFamily"],
        "runtimeVersion": runtime_version,
        "backend": state["backend"],
        "provider": result_provider,
        "availableProviders": available_providers,
        "batchSize": measurement["batch"],
        "warmupIterations": measurement["warmupIterations"],
        "warmupCompletedAt": warmup_completed_at,
        "inferenceCount": len(samples),
        "measurementMode": "duration",
        "measurementStartedAt": measurement_started_at,
        "measurementEndedAt": measurement_ended_at,
        "measurementElapsedSecondsMonotonic": round(measurement_elapsed, 6),
        "cpuContract": runtime_cpu_evidence(
            cpu_max,
            affinity,
            cpuset,
            process_cpu_seconds,
            stat_before,
            stat_after,
        ),
        "derivedExecutionContract": derived_contract(run_config),
        "repetitionResults": [
            {
                "repetition": 1,
                "measurementStartedAt": repetition_started_at,
                "measurementEndedAt": repetition_ended_at,
                "latencyMs": [round(value, 6) for value in samples],
            }
        ],
        "completionIndex": 0,
        "inputs": state["preprocessor"]["inputs"],
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
        **memory,
    }
    log(
        "result",
        f"completed {state['runtimeFamily']} CPU measurement",
        inferences=len(samples),
        elapsedSeconds=round(measurement_elapsed, 3),
    )
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--result-file", required=True, type=Path)
    return parser.parse_args()
