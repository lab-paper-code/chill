#!/usr/bin/env python3
"""Measure the production firstfloor actor and its production IPC runner."""
from __future__ import annotations

import base64
import http.client
import json
import math
import os
from pathlib import Path
import platform
import random
import socket
import subprocess
import sys
import tempfile
import time
from typing import Any
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from ortcpu import runner as common

REQUEST_SCHEMA = "chill.dacs.io/firstfloor-runtime-request.v1alpha2"
FRAME_BYTES = 64 << 20
PROVIDERS = {"ort-cpu": "CPUExecutionProvider", "ort-cuda": "CUDAExecutionProvider", "rknnlite": ""}
# ponytail: seed is a constant; move it to the active protocol configuration when an experiment varies the seed.
SAMPLE_SEED = 0


def deadline_policy(settings: dict[str, Any]) -> str:
    value = settings.get("deadlinePolicy", "keep")
    if value not in ("drop", "keep", "defer"):
        raise common.ContractError("firstfloor deadlinePolicy must be drop, keep, or defer")
    return value


def initialized_batches(run: dict[str, Any]) -> list[int]:
    maximum = run["state"]["firstfloor"]["maxBatchSize"]
    capability = run["delivery"]["artifactCapability"]
    declared = capability.get("declaredBatchSizes")
    if declared is None:
        declared = range(1, maximum + 1)
    batches = [batch for batch in declared if batch <= maximum]
    if not batches or run["measurement"]["batch"] not in batches:
        raise common.ContractError("configured initialized batches must include the selected fixed K")
    return batches


def validate_request(request: dict[str, Any]) -> None:
    run = common.require_object(request.get("runConfig"), "runConfig")
    state = common.require_object(run.get("state"), "state")
    backend = state.get("backend")
    if backend not in PROVIDERS:
        raise common.ContractError("unsupported firstfloor backend")
    if backend == "rknnlite" and isinstance(run.get("cpu"), dict):
        # Go omits zero ORT thread counts for RKNN RunConfigs.
        run["cpu"].setdefault("ortIntraOpThreads", 0)
        run["cpu"].setdefault("ortInterOpThreads", 0)
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
    if backend == "rknnlite":
        if state.get("hostContract") != run["delivery"].get("hostContract"):
            raise common.ContractError("frozen RKNN host identity differs from delivered host declarations")
        from rknnlite import runner as rknn
        try:
            rknn.validate_candidate(run["delivery"])
        except rknn.ContractError as error:
            raise common.ContractError(str(error)) from error
    elif "hostContract" in run["delivery"]:
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


def native_sample(response: dict[str, Any], batch: int, record_ids: list[str], generative: bool) -> dict[str, Any]:
    common.require_integer(response.get("batchSize"), "actual batchSize", 1)
    if response.get("batchSize") != batch or response.get("executionComplete") is not True:
        raise common.ContractError("actor did not complete exactly one requested fixed-K execution")
    keys = ["batchIDs", "serviceSamplesMS", "preprocessSamplesMS", "backendSamplesMS",
            "postprocessSamplesMS", "nativeSamplesMS"]
    if generative:
        keys.append("firstTokenSamplesMS")
    elif "firstTokenSamplesMS" in response:
        raise common.ContractError("non-generative execution reported a first-token span")
    arrays = [response.get(key) for key in keys]
    if any(not isinstance(values, list) or len(values) != 1 for values in arrays):
        raise common.ContractError("native timing samples and batch IDs must be present and aligned")
    batch_id = common.require_integer(arrays[0][0], "native batch ID", 1)
    if batch_id > (1 << 64) - 1:
        raise common.ContractError("native batch ID exceeds uint64")
    service = milliseconds(arrays[1][0], "native service sample")
    preprocess = milliseconds(arrays[2][0], "native preprocess sample", positive=False)
    backend = milliseconds(arrays[3][0], "native backend sample")
    postprocess = milliseconds(arrays[4][0], "native postprocess sample", positive=False)
    native = milliseconds(arrays[5][0], "native request sample")
    total = milliseconds(response.get("nativeElapsedMS"), "native elapsed")
    units = response.get("units")
    if not isinstance(units, list) or len(units) != batch or len(record_ids) != batch:
        raise common.ContractError("native units and sampled record IDs must align with the fixed batch")
    for value in units:
        common.require_integer(value, "native units item", 1)
    if preprocess + backend + postprocess > service or service > native or native > total:
        raise common.ContractError("native timing spans are not nested")
    sample = {"batchID": batch_id, "batchSize": batch, "recordIDs": list(record_ids), "units": units,
              "runnerServiceMs": service, "preprocessMs": preprocess, "backendCallMs": backend,
              "postprocessMs": postprocess, "nativeRequestMs": native}
    if generative:
        sample["firstTokenMs"] = milliseconds(arrays[6][0], "native first-token sample")
    return sample


def validate_rknn_facts(facts: dict[str, Any], delivery: dict[str, Any]) -> None:
    host = delivery["hostContract"]
    driver, platform_contract = host["driver"], host["platform"]
    expected = {
        "package": delivery["runtimePackages"][0],
        "hostLibraries": [{"name": item["name"], "path": item["containerPath"]} for item in host["libraries"]],
        "driver": driver,
        "platform": dict(platform_contract, matched=True),
        "devices": [dict(item, characterDevice=True, opened=True) for item in host["devices"]],
        "coreMask": "NPU_CORE_0", "inputLayout": "NHWC",
        "initSucceeded": True, "loadSucceeded": True,
    }
    if any(facts.get(key) != value for key, value in expected.items()):
        raise common.ContractError("actual RKNN package/host/driver/platform/device/load/init facts differ from declarations")


def validate_metadata(metadata: dict[str, Any], request: dict[str, Any], cpu_max: str,
                      affinity: list[int], cpuset: str) -> dict[str, Any]:
    run = request["runConfig"]
    state, cpu = run["state"], run["cpu"]
    requested = common.requested_cpu_affinity(cpu)
    if requested is not None and affinity != requested:
        raise common.ContractError("measurement CPU affinity differs from frozen requested CPU mask")
    actual = common.require_object(metadata.get("runtime"), "actual runtime metadata")
    backend = state["backend"]
    policy = common.require_object(metadata.get("policy"), "actual actor policy")
    if (metadata.get("model") != state["model"] or metadata.get("mode") != "profiling" or
            metadata.get("maxQueueRequests") != state["firstfloor"]["maxQueueSize"] or
            metadata.get("maxBatchSize") != state["firstfloor"]["maxBatchSize"] or
            policy.get("deadlinePolicy") != deadline_policy(state["firstfloor"]) or
            metadata.get("modelInstanceCount") != 1 or
            metadata.get("maxQueueBytes") != min(request["maxInputBytes"], FRAME_BYTES) or
            metadata.get("maxRequestBytes") != FRAME_BYTES or
            metadata.get("profileID") or metadata.get("decisionID")):
        raise common.ContractError("actual actor configuration differs from frozen profiling conditions")
    if actual.get("runtime") != backend or actual.get("synthetic") is not False:
        raise common.ContractError("actual runtime must match the real frozen backend")
    common.require_string(actual.get("runtimeVersion"), "actual runtime version")
    common.require_string(actual.get("numpyVersion"), "actual numpy version")
    preprocessor = state["preprocessor"]
    model_files = sorted(str(Path(request["modelDirectory"]) / Path(item).name) for item in state["artifactFiles"])
    if (actual.get("modelFiles") != model_files or actual.get("inputs") != preprocessor["inputs"] or
            actual.get("outputs") != preprocessor["outputs"]):
        raise common.ContractError("actual model files/input/output metadata differs from frozen declarations")
    if (actual.get("intraOpThreads") != cpu["ortIntraOpThreads"] or
            actual.get("interOpThreads") != cpu["ortInterOpThreads"] or
            actual.get("cpuAffinity") != affinity or actual.get("initializedBatches") != initialized_batches(run) or
            actual.get("warmupIterations") != run["measurement"]["warmupIterations"]):
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
    for package in run["delivery"].get("runtimePackages", []):
        actual_version = {"onnxruntime": actual["runtimeVersion"], "onnxruntime-gpu": actual["runtimeVersion"],
                          "numpy": actual["numpyVersion"], "rknn-toolkit-lite2": actual["runtimeVersion"]}.get(package["name"])
        if actual_version != package["version"]:
            raise common.ContractError("actual runtime package differs from frozen declaration")
    if actual.get("cgroupCPUMax") != cpu_max or actual.get("cpuSet") != cpuset:
        raise common.ContractError("actual runner cgroup quota/cpuset differs from measurement container")
    available = actual.get("availableProviders", [])
    if not isinstance(available, list) or (backend != "rknnlite" and PROVIDERS[backend] not in available):
        raise common.ContractError("actual available providers do not support the selected backend")
    if backend == "rknnlite":
        validate_rknn_facts(common.require_object(actual.get("rknn"), "actual RKNN runtime facts"), run["delivery"])
    return actual


class FirstfloorSession:
    """One real profile call per common measurement call, with correlated spans."""
    def __init__(self, port: int, request: dict[str, Any]):
        self.request = request
        self.connection = http.client.HTTPConnection("127.0.0.1", port, timeout=None)
        self.path = "/v2/models/" + quote(request["runConfig"]["state"]["model"], safe="")
        self.samples: list[dict[str, Any]] = []
        self.payload: bytes | None = None
        self.record_ids: list[str] = []
        self.baseline: dict[str, int] = {}
        self.rng = random.Random(SAMPLE_SEED)
        self.records: list[str] = []
        if "datasetPath" in request:
            with os.scandir(request["datasetPath"]) as entries:
                self.records = sorted(entry.name for entry in entries if entry.is_dir())
            if len(self.records) < request["runConfig"]["measurement"]["batch"]:
                raise common.ContractError("dataset has fewer profiling records than the fixed batch")

    def json_call(self, method: str, path: str, payload: bytes | None = None) -> dict[str, Any]:
        self.connection.request(method, path, payload, {"Content-Type": "application/json"})
        response = self.connection.getresponse()
        data = response.read(FRAME_BYTES + 1)
        if len(data) > FRAME_BYTES:
            raise common.ContractError("native response exceeds IPC frame budget")
        if response.status != 200:
            raise common.ContractError(f"native actor {path} HTTP {response.status}: {data[:1024]!r}")
        return common.require_object(json.loads(data, object_pairs_hook=common.no_duplicate_object), "actor response")

    def metadata(self) -> dict[str, Any]:
        return self.json_call("GET", self.path + "/metadata")

    def counts(self) -> dict[str, int]:
        state = self.json_call("GET", self.path + "/state")
        if state.get("ready") is not True or state.get("requests") not in (None, []):
            raise common.ContractError("actor must be ready with no outstanding requests at measurement boundaries")
        return {key: common.require_integer(state.get(key), "actual actor " + key)
                for key in ("acceptedRequests", "completedRequests", "executions")}

    def begin_measurement(self) -> None:
        self.samples.clear()
        self.baseline = self.counts()

    def get_providers(self) -> list[str]:
        return self.metadata()["runtime"]["providers"]

    def prepare_next(self) -> None:
        """Draw the next execution's records and build its body outside the timed call."""
        run = self.request["runConfig"]
        batch, root = run["measurement"]["batch"], Path(self.request["datasetPath"])
        self.record_ids = self.rng.sample(self.records, batch)
        inputs = [{"name": name, "data": [base64.b64encode((root / record / name).read_bytes()).decode("ascii")
                                          for record in self.record_ids]}
                  for name in run["state"]["preprocessor"]["inputs"]]
        self.payload = json.dumps({"batchSize": batch, "inputs": inputs}, separators=(",", ":")).encode()
        if len(self.payload) > FRAME_BYTES:
            raise common.ContractError("native JSON input exceeds worker request capacity")

    def run(self) -> None:
        if self.payload is None:
            raise common.ContractError("native input has not been prepared")
        payload, self.payload = self.payload, None
        response = self.json_call("POST", self.path + "/profile", payload)
        run = self.request["runConfig"]
        sample = native_sample(response, run["measurement"]["batch"], self.record_ids,
                               run["state"]["preprocessor"].get("maxNewTokens", 0) > 0)
        if self.samples and sample["batchID"] <= self.samples[-1]["batchID"]:
            raise common.ContractError("native batch IDs must increase monotonically")
        self.samples.append(sample)


def wait_ready(process: subprocess.Popen, session: FirstfloorSession, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    session.connection.timeout = min(timeout, 1.0)
    while True:
        if process.poll() is not None:
            raise common.ContractError(f"serving-worker exited before readiness: {process.returncode}")
        try:
            session.metadata()
            session.connection.timeout = None
            return
        except (OSError, http.client.HTTPException, common.ContractError):
            session.connection.close()
        if time.monotonic() >= deadline:
            raise common.ContractError("serving-worker model readiness timed out")
        time.sleep(common.SIGNAL_POLL_SECONDS)


def worker_config(request: dict[str, Any]) -> dict[str, Any]:
    run = request["runConfig"]
    backend = run["state"]["backend"]
    preprocessor = run["state"]["preprocessor"]
    command = [sys.executable, str(Path(__file__).with_name("runner.py")), "--runtime", backend,
               "--model-dir", request["modelDirectory"], "--preprocessor", request["preprocessorPath"],
               "--intra-op-threads", str(run["cpu"]["ortIntraOpThreads"]),
               "--warmup-iterations", str(run["measurement"]["warmupIterations"])]
    affinity = common.requested_cpu_affinity(run["cpu"])
    if affinity is not None:
        command += ["--cpu-affinity", ",".join(map(str, affinity))]
    if backend == "ort-cuda":
        command += ["--gpu-uuid", run["delivery"]["runtimeAccess"]["gpuUUID"]]
    config = {"model": run["state"]["model"], "mode": "profiling",
              "inputs": preprocessor["inputs"], "outputs": preprocessor["outputs"],
              "runnerCommand": command, "runnerTimeoutMS": request["observerReadyTimeoutMilliseconds"],
              "maxQueueRequests": run["state"]["firstfloor"]["maxQueueSize"],
              "policy": {"deadlinePolicy": deadline_policy(run["state"]["firstfloor"])},
              "maxQueueBytes": min(request["maxInputBytes"], FRAME_BYTES), "maxRequestBytes": FRAME_BYTES,
              "batchSizes": initialized_batches(run), "maxBatchSize": run["state"]["firstfloor"]["maxBatchSize"]}
    if preprocessor.get("maxNewTokens", 0) > 0:
        config["maxNewTokens"] = preprocessor["maxNewTokens"]
    return config


def execute(request: dict[str, Any], workspace: Path) -> dict[str, Any]:
    validate_request(request)
    run = request["runConfig"]
    architecture = platform.machine()
    if common.normalize_architecture(architecture) != common.normalize_architecture(run["targetNode"]["architecture"]):
        raise common.ContractError("actual architecture differs from frozen Node")
    common.apply_cpu_affinity(run)
    cpu_max, affinity, cpuset = common.validate_cpu_contract(run)
    sampler = memory_sampler(run)
    try:
        return execute_sampled(request, workspace, run, architecture, cpu_max, affinity, cpuset, sampler)
    finally:
        if sampler is not None:
            sampler.close()


def execute_sampled(request: dict[str, Any], workspace: Path, run: dict[str, Any],
                    architecture: str, cpu_max: Any, affinity: Any, cpuset: Any,
                    sampler: common.MemorySampler | None) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="firstfloor-", dir=workspace) as directory:
        config_path = Path(directory) / "worker.json"
        delivery_path = Path(directory) / "delivery.json"
        delivery_path.write_text(json.dumps(run["delivery"], allow_nan=False), encoding="utf-8")
        config = worker_config(request)
        if run["state"]["backend"] == "rknnlite":
            config["runnerCommand"] += ["--delivery-json", str(delivery_path)]
        config_path.write_text(json.dumps(config, allow_nan=False), encoding="utf-8")
        with socket.socket() as address:
            address.bind(("127.0.0.1", 0))
            port = address.getsockname()[1]
        session = FirstfloorSession(port, request)
        child_environment = None
        if run["state"]["backend"] == "ort-cuda":
            child_environment = dict(os.environ, CUDA_VISIBLE_DEVICES=run["delivery"]["runtimeAccess"]["gpuUUID"])
        process = subprocess.Popen(["/opt/chill/bin/serving-worker", "--config", str(config_path),
                                    "--bind-address", f"127.0.0.1:{port}", "--node-name", request["scheduledNodeName"]],
                                   stdout=sys.stderr, stderr=sys.stderr, start_new_session=True, env=child_environment)
        try:
            wait_ready(process, session, request["observerReadyTimeoutMilliseconds"] / 1000.0)
            loaded_at = common.now()
            actual = validate_metadata(session.metadata(), request, cpu_max, affinity, cpuset)
            measurement = run["measurement"]
            if measurement["workloadMode"] == common.IDLE_WORKLOAD:
                session.baseline = session.counts()
                result = common.execute_idle(request, actual["runtimeVersion"], session, actual.get("availableProviders", []),
                                             architecture, cpu_max, affinity, cpuset, loaded_at, PROVIDERS[run["state"]["backend"]],
                                             memory_sampler=sampler)
                executions = 0
            else:
                result = common.execute_loaded(request, session, actual["runtimeVersion"], actual.get("availableProviders", []),
                                               architecture, cpu_max, affinity, cpuset,
                                               session.begin_measurement, PROVIDERS[run["state"]["backend"]],
                                               memory_sampler=sampler)
                executions = result["inferenceCount"]
                if len(session.samples) != executions or len(result["repetitionResults"][0]["latencyMs"]) != executions:
                    raise common.ContractError("native/client timing samples are not aligned")
                result["repetitionResults"][0]["nativeSamples"] = session.samples
                for native, client in zip(session.samples, result["repetitionResults"][0]["latencyMs"]):
                    if native["nativeRequestMs"] > client:
                        raise common.ContractError("native request span exceeds correlated client-call duration")
            observed = session.counts()
            if any(observed[key] - session.baseline[key] != executions for key in observed):
                raise common.ContractError("actual actor execution/request counters disagree with completed fixed-K samples")
            after = validate_metadata(session.metadata(), request, cpu_max, affinity, cpuset)
            if common.validate_cpu_contract(run) != (cpu_max, affinity, cpuset):
                raise common.ContractError("measurement cgroup quota/affinity changed during the window")
            if after != actual or process.poll() is not None:
                raise common.ContractError("native model/runtime conditions changed during measurement")
            result["firstfloor"] = {"runtime": actual, "mode": "profiling", "modelInstanceCount": 1,
                                     "maxBatchSize": run["state"]["firstfloor"]["maxBatchSize"],
                                     "maxQueueSize": run["state"]["firstfloor"]["maxQueueSize"],
                                     "deadlinePolicy": deadline_policy(run["state"]["firstfloor"]),
                                     "executionCount": executions, "inferenceCount": executions * measurement["batch"],
                                     "batchExecutionCounts": {str(measurement["batch"]): executions} if executions else {}}
            if sampler is None:
                result["memoryBytes"], result["memorySource"] = memory_peak()
            result["derivedExecutionContract"] = common.derived_contract(run, PROVIDERS[run["state"]["backend"]])
            if run["state"]["backend"] == "rknnlite":
                facts = actual["rknn"]
                # Active K comes from actual completed actor samples; idle K was actually queried during initialization.
                selected = session.samples[0]["batchSize"] if executions else measurement["batch"]
                result["rknn"] = dict(facts, selectedBatch=selected)
            return result
        finally:
            session.connection.close()
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
        request = common.decode_request(sys.stdin.buffer)
        validate_request(request)
        common.apply_cpu_affinity(request["runConfig"])
        result = execute(request, arguments.result_file.parent)
        common.publish_immutable(arguments.result_file, common.encode_result(result, request["maxResultBytes"]))
    except (common.ContractError, OSError, ValueError, http.client.HTTPException) as error:
        common.fail(str(error))


if __name__ == "__main__":
    main()
