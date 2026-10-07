#!/usr/bin/env python3
"""Measure the native Triton Python/CPU-ORT backend, never standalone ORT."""
from __future__ import annotations

import http.client
import json
import math
import platform
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from ortcpu import runner as common

REQUEST_SCHEMA = "chill.dacs.io/triton-cpu-runtime-request.v1alpha15"
MODEL_NAME = "chill_model"


def validate_request(request: dict[str, Any]) -> None:
    common.validate_request(request, REQUEST_SCHEMA, "triton", "python")
    config = request["runConfig"]
    settings = common.require_object(config["state"]["triton"], "state.triton")
    common.require_fields(settings, {"maxBatchSize", "priorityLevels", "maxQueueSize"}, "state.triton")
    for name, value in settings.items():
        common.require_integer(value, f"state.triton.{name}", 1)
        if value > 2147483647:
            raise common.ContractError(f"state.triton.{name} exceeds int32")
    if config["measurement"]["batch"] > settings["maxBatchSize"]:
        raise common.ContractError("selected batch exceeds frozen Triton maxBatchSize")
    if config["measurement"]["workloadMode"] == common.IDLE_WORKLOAD and config["measurement"]["batch"] != 1:
        raise common.ContractError("idle Triton measurement requires batch 1 without inference")


def memory_peak(root: Path = common.CGROUP_ROOT) -> tuple[int, str]:
    v2 = (root / "cpu.max").is_file() or (root / "memory.peak").is_file()
    path = root / "memory.peak" if v2 else root / "memory" / "memory.max_usage_in_bytes"
    source = "cgroup-v2-memory.peak" if v2 else "cgroup-v1-memory.max_usage_in_bytes"
    raw = common.read_required_text(path, "cgroup memory peak")
    if raw == "max":
        raise common.ContractError("cgroup memory peak is unavailable or unlimited")
    try:
        peak = int(raw)
    except ValueError as error:
        raise common.ContractError("cgroup memory peak is malformed") from error
    if peak < 1:
        raise common.ContractError("cgroup memory peak is missing positive evidence")
    return peak, source + ":runtime-container-lifetime"


def get_json(port: int, path: str) -> dict[str, Any]:
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    try:
        connection.request("GET", path)
        response = connection.getresponse()
        payload = response.read()
        if response.status != 200:
            raise common.ContractError(f"Triton {path} returned HTTP {response.status}: {payload[:1024]!r}")
        value = json.loads(payload, object_pairs_hook=common.no_duplicate_object)
        return common.require_object(value, f"Triton {path}")
    finally:
        connection.close()


def ready(port: int) -> bool:
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=1)
    try:
        connection.request("GET", f"/v2/models/{MODEL_NAME}/versions/1/ready")
        response = connection.getresponse()
        response.read()
        return response.status == 200
    except (OSError, http.client.HTTPException):
        return False
    finally:
        connection.close()


def wait_ready(process: subprocess.Popen, port: int, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while True:
        if process.poll() is not None:
            raise common.ContractError(f"native Triton exited before model readiness: {process.returncode}")
        if ready(port):
            return
        if time.monotonic() >= deadline:
            raise common.ContractError("native Triton model readiness timed out")
        time.sleep(common.SIGNAL_POLL_SECONDS)


def validate_config(config: dict[str, Any], request: dict[str, Any], cpu_max: str,
                    affinity: list[int], cpuset: str) -> dict[str, Any]:
    from repository import QUEUE_TIMEOUT_CEILING
    run = request["runConfig"]
    settings, cpu = run["state"]["triton"], run["cpu"]
    dynamic = common.require_object(config.get("dynamic_batching"), "native dynamic_batching")
    queue = common.require_object(dynamic.get("default_queue_policy"), "native queue policy")
    timeout = count(queue.get("default_timeout_microseconds"), "native default queue timeout")
    groups = config.get("instance_group")
    if (config.get("name") != MODEL_NAME or config.get("backend") != "python" or
            config.get("max_batch_size") != settings["maxBatchSize"] or
            not isinstance(groups, list) or len(groups) != 1 or
            groups[0].get("kind") != "KIND_CPU" or groups[0].get("count") != 1 or
            dynamic.get("max_queue_delay_microseconds", 0) not in (0, "0") or
            dynamic.get("priority_levels") != settings["priorityLevels"] or
            dynamic.get("default_priority_level") != settings["priorityLevels"] or
            queue.get("max_queue_size") != settings["maxQueueSize"] or
            timeout != QUEUE_TIMEOUT_CEILING or queue.get("allow_timeout_override") is not True or
            queue.get("timeout_action", "REJECT") != "REJECT" or
            dynamic.get("priority_queue_policy") or dynamic.get("preferred_batch_size") != [settings["maxBatchSize"]] or
            config.get("model_transaction_policy", {}).get("decoupled", False)):
        raise common.ContractError("native model config differs from the frozen Triton CPU condition")
    inputs = config.get("input", [])
    declared = run["delivery"]["artifactCapability"]["tensors"]
    observed = {item["name"]: item for item in inputs}
    if len(observed) != len(inputs) or set(observed) != {item["name"] for item in declared}:
        raise common.ContractError("native model input names differ from declaration")
    for tensor in declared:
        actual = observed[tensor["name"]]
        if (actual.get("data_type") != "TYPE_" + tensor["datatype"] or
                actual.get("dims") != tensor["sampleShape"] or actual.get("reshape") or
                actual.get("allow_ragged_batch", False)):
            raise common.ContractError("native model input datatype/shape differs from declaration")
    parameters = config.get("parameters", {})
    values = {key: item.get("string_value") for key, item in parameters.items()}
    expected = {
        "chill_runtime_family": "triton", "chill_provider": "CPUExecutionProvider",
        "chill_guard": "same-workload-uid-v1",
        "chill_intra_op_threads": str(cpu["ortIntraOpThreads"]),
        "chill_inter_op_threads": str(cpu["ortInterOpThreads"]),
        "chill_affinity_cpus": json.dumps(affinity, separators=(",", ":")),
        "chill_cpuset_effective": cpuset, "chill_cgroup_cpu_max": cpu_max,
        "chill_artifact_path": str(Path(request["modelPath"]).resolve()),
    }
    if any(values.get(key) != value for key, value in expected.items()):
        raise common.ContractError("loaded Triton backend software/provider/CPU metadata mismatch")
    version = common.require_string(values.get("chill_ort_version"), "actual backend ORT version")
    providers = json.loads(common.require_string(values.get("chill_available_providers"), "actual available providers"))
    if not isinstance(providers, list) or "CPUExecutionProvider" not in providers:
        raise common.ContractError("actual CPUExecutionProvider unavailable")
    strategy = values.get("TRITON_BATCH_STRATEGY_PATH", "")
    if not isinstance(strategy, str) or not Path(strategy).is_file():
        raise common.ContractError("native same-Workload-UID batch strategy is unavailable")
    return {"ortVersion": version, "modelInstanceCount": 1,
            "maxBatchSize": config["max_batch_size"], "maxQueueDelayMicroseconds": 0,
            "preferredBatchSizes": dynamic["preferred_batch_size"],
            "priorityLevels": dynamic["priority_levels"], "maxQueueSize": queue["max_queue_size"],
            "defaultQueueTimeoutMicroseconds": timeout,
            "timeoutAction": queue.get("timeout_action", "REJECT"),
            "allowTimeoutOverride": queue["allow_timeout_override"],
            "workloadUIDGuard": values["chill_guard"], "availableProviders": providers}


def statistics(port: int) -> dict[str, Any]:
    entries = get_json(port, f"/v2/models/{MODEL_NAME}/versions/1/stats").get("model_stats", [])
    if len(entries) != 1 or entries[0].get("name") != MODEL_NAME or entries[0].get("version") != "1":
        raise common.ContractError("native statistics do not identify the measured model version")
    return entries[0]


def count(value: Any, name: str) -> int:
    if isinstance(value, str) and value.isascii() and value.isdigit():
        value = int(value)
    return common.require_integer(value, name)


def statistics_delta(before: dict[str, Any], after: dict[str, Any], batch: int,
                     expected_executions: int) -> dict[str, Any]:
    delta = {}
    for key in ("execution_count", "inference_count"):
        delta[key] = count(after[key], key) - count(before[key], key)
    before_batches = {str(count(item["batch_size"], "native batch size")):
                      count(item["compute_infer"]["count"], "native batch count")
                      for item in before.get("batch_stats", [])}
    after_batches = {str(count(item["batch_size"], "native batch size")):
                     count(item["compute_infer"]["count"], "native batch count")
                     for item in after.get("batch_stats", [])}
    batches = {size: after_batches.get(size, 0) - before_batches.get(size, 0)
               for size in after_batches.keys() | before_batches.keys()}
    if (delta["execution_count"] != expected_executions or
            delta["inference_count"] != expected_executions * batch or
            batches.get(str(batch), 0) != expected_executions or
            any(value != 0 for size, value in batches.items() if size != str(batch))):
        raise common.ContractError("native statistics do not prove exactly the fixed actual batch K")
    return {"executionCount": delta["execution_count"], "inferenceCount": delta["inference_count"],
            "batchExecutionCounts": batches}


class TritonSession:
    """Persistent native HTTP session implementing the shared measurement calls."""
    def __init__(self, port: int, request: dict[str, Any], metadata: dict[str, Any], numpy: Any):
        self.port, self.request, self.metadata, self.numpy = port, request, metadata, numpy
        # The existing worker attempt context, not a second inference timeout, bounds execution.
        self.connection = http.client.HTTPConnection("127.0.0.1", port, timeout=None)
        self.payload = None
        self.header_size = 0
        self.response_limit = request["maxResultBytes"]

    def get_providers(self) -> list[str]:
        if not ready(self.port):
            raise common.ContractError("loaded native model lost readiness")
        return ["CPUExecutionProvider"]

    def prepare_inputs(self, feeds: dict[str, Any]) -> None:
        run = self.request["runConfig"]
        uid = "profiling-" + self.request["podUID"]
        definition = f'{uid}/1/{run["candidate"]["inputs"]["modelSpec"]["uid"]}/1'
        inputs, binary = [], []
        for tensor in run["delivery"]["artifactCapability"]["tensors"]:
            value = feeds[tensor["name"]]
            inputs.append({"name": tensor["name"], "datatype": tensor["datatype"],
                           "shape": list(value.shape), "parameters": {"binary_data_size": value.nbytes}})
            binary.append(value.tobytes(order="C"))
        header = {"id": uid + "~" + definition, "parameters": {
            "priority": 1, "chill_workload_uid": uid, "chill_definition": definition,
            "chill_slo_percentile": 100.0, "chill_slo_target_ns": run["measurement"]["durationSeconds"] * 1000000000,
            "chill_elapsed_ns": 0}, "inputs": inputs,
            "outputs": [{"name": item["name"], "parameters": {"binary_data": True}}
                        for item in self.metadata["outputs"]]}
        encoded = json.dumps(header, separators=(",", ":"), allow_nan=False).encode()
        self.header_size = len(encoded)
        self.payload = b"".join([encoded, *binary])

    def run(self, output_names: Any, feeds: dict[str, Any]) -> list[Any]:
        if self.payload is None:
            raise common.ContractError("native inference input was not prepared")
        self.connection.request("POST", f"/v2/models/{MODEL_NAME}/versions/1/infer", self.payload,
                                {"Content-Type": "application/octet-stream",
                                 "Inference-Header-Content-Length": str(self.header_size)})
        response = self.connection.getresponse()
        payload = response.read(self.response_limit + 1)
        if len(payload) > self.response_limit:
            raise common.ContractError("native inference response exceeds the runtime result budget")
        if response.status != 200:
            raise common.ContractError(f"native inference failed HTTP {response.status}: {payload[:1024]!r}")
        length = count(response.getheader("Inference-Header-Content-Length"), "native response header length")
        if length < 1 or length > len(payload):
            raise common.ContractError("native inference binary header is invalid")
        header = common.require_object(json.loads(payload[:length], object_pairs_hook=common.no_duplicate_object), "native inference response")
        parameters = header.get("parameters", {})
        batch = self.request["runConfig"]["measurement"]["batch"]
        if parameters.get("chill_actual_batch_size") != batch or parameters.get("chill_actual_batch_requests") != 1:
            raise common.ContractError("backend did not execute the requested single fixed batch K")
        declared = {item["name"]: item for item in self.metadata["outputs"]}
        outputs = header.get("outputs", [])
        if len(outputs) != len(declared) or {item["name"] for item in outputs} != set(declared):
            raise common.ContractError("native output names differ from actual model metadata")
        offset, arrays = length, []
        for output in outputs:
            name, datatype, shape = output["name"], output["datatype"], output["shape"]
            expected = declared[name]
            expected_shape = expected["shape"]
            if (datatype not in common.INPUT_DATATYPES or datatype != expected["datatype"] or
                    not isinstance(shape, list) or not shape or shape[0] != batch or
                    any(type(dim) is not int or dim < 1 for dim in shape) or
                    len(shape) != len(expected_shape) or
                    any(actual != wanted for actual, wanted in zip(shape[1:], expected_shape[1:]) if wanted > 0)):
                raise common.ContractError("native output dtype/shape differs from actual model metadata")
            size = math.prod(shape) * common.INPUT_DATATYPES[datatype][2]
            if output.get("parameters", {}).get("binary_data_size") != size or offset + size > len(payload):
                raise common.ContractError("native output binary byte count is invalid")
            dtype = self.numpy.dtype(common.INPUT_DATATYPES[datatype][1]).newbyteorder("<")
            arrays.append(self.numpy.frombuffer(payload, dtype=dtype, count=math.prod(shape), offset=offset).reshape(shape))
            offset += size
        if offset != len(payload):
            raise common.ContractError("native inference response contains unbound binary bytes")
        return arrays


def execute(request: dict[str, Any], numpy: Any, workspace: Path) -> dict[str, Any]:
    validate_request(request)
    run = request["runConfig"]
    settings, measurement, cpu = run["state"]["triton"], run["measurement"], run["cpu"]
    common.validate_model_file(Path(request["modelPath"]))
    architecture = platform.machine()
    if common.normalize_architecture(architecture) != common.normalize_architecture(run["targetNode"]["architecture"]):
        raise common.ContractError("actual architecture differs from the frozen Node")
    cpu_max, affinity, cpuset = common.validate_cpu_contract(run)
    memory_peak()  # Reject missing kernel evidence before executing traffic.
    from repository import prepare_model_repository
    with tempfile.TemporaryDirectory(prefix="triton-", dir=workspace) as root:
        prepare_model_repository(root, MODEL_NAME, request["modelPath"], run["delivery"]["artifactCapability"]["tensors"],
                                 settings["maxBatchSize"], cpu["ortIntraOpThreads"], cpu["ortInterOpThreads"],
                                 settings["priorityLevels"], settings["maxQueueSize"])
        with socket.socket() as address:
            address.bind(("127.0.0.1", 0))
            port = address.getsockname()[1]
        process = subprocess.Popen([sys.executable, str(Path(__file__).with_name("serve.py")), "--model-repository=" + root,
                                    "--model-control-mode=explicit", "--load-model=" + MODEL_NAME,
                                    "--strict-readiness=true", "--allow-grpc=false", "--allow-metrics=false",
                                    "--http-address=127.0.0.1", f"--http-port={port}"],
                                   stdout=sys.stderr, stderr=sys.stderr)
        session = None
        try:
            wait_ready(process, port, request["observerReadyTimeoutMilliseconds"] / 1000.0)
            loaded_at = common.now()
            native = validate_config(get_json(port, f"/v2/models/{MODEL_NAME}/config"), request, cpu_max, affinity, cpuset)
            server = get_json(port, "/v2")
            if server.get("name") != "triton":
                raise common.ContractError("actual server metadata does not identify Triton")
            version = common.require_string(server.get("version"), "actual Triton version")
            for package in run["delivery"].get("runtimePackages", []):
                actual = {"onnxruntime": native["ortVersion"], "tritonserver": version}.get(package["name"])
                if actual != package["version"]:
                    raise common.ContractError("actual runtime package differs from frozen declaration")
            metadata = get_json(port, f"/v2/models/{MODEL_NAME}/versions/1")
            session = TritonSession(port, request, metadata, numpy)
            baseline = statistics(port)
            if measurement["workloadMode"] == common.IDLE_WORKLOAD:
                result = common.execute_idle(request, version, session, native["availableProviders"],
                                             architecture, cpu_max, affinity, cpuset, loaded_at)
                executions = 0
            else:
                feeds = common.generate_inputs(numpy, run["delivery"]["artifactCapability"]["tensors"],
                                               measurement["batch"], request["maxInputBytes"])
                session.prepare_inputs(feeds)
                def begin_measurement():
                    nonlocal baseline
                    baseline = statistics(port)
                result = common.execute_loaded(request, session, numpy, version, native["availableProviders"],
                                               architecture, cpu_max, affinity, cpuset, feeds, begin_measurement)
                executions = result["inferenceCount"]
            result["triton"] = {key: value for key, value in native.items() if key != "availableProviders"}
            result["triton"].update(statistics_delta(baseline, statistics(port), measurement["batch"], executions))
            result["memoryBytes"], result["memorySource"] = memory_peak()
            if process.poll() is not None or not ready(port):
                raise common.ContractError("native model did not remain ready through measurement")
            return result
        finally:
            if session is not None:
                session.connection.close()
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def main() -> None:
    arguments = common.parse_args()
    try:
        result_path = common.require_absolute_file_path(str(arguments.result_file), "result-file")
        request = common.decode_request(sys.stdin.buffer)
        import numpy
        result = execute(request, numpy, result_path.parent)
        common.publish_immutable(result_path, common.encode_result(result, request["maxResultBytes"]))
    except (common.ContractError, ImportError, ValueError, TypeError, KeyError, OSError, http.client.HTTPException) as error:
        common.fail(str(error))


if __name__ == "__main__":
    main()
