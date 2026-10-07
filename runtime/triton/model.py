"""Synchronous CPU ONNX execution behind Triton's native scheduler."""
from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
import triton_python_backend_utils as pb_utils

from repository import DTYPES, GUARD, QUEUE_TIMEOUT_CEILING, model_metadata, runtime_parameters, session

MAX_ID_BYTES = 512
PROGRESS_MARKER = "CHILL_PROGRESS "


class InputError(ValueError):
    """A bounded diagnostic authored here, never an engine/payload exception."""


def identity(value):
    if not isinstance(value, str) or not value or len(value.encode("utf-8")) > MAX_ID_BYTES or any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise InputError("invalid bounded request identity")
    return value


def request_metadata(request):
    parameters = json.loads(request.parameters())
    expected = {"chill_workload_uid", "chill_definition", "chill_slo_percentile", "chill_slo_target_ns", "chill_elapsed_ns"}
    if set(parameters) != expected:
        raise InputError("authoritative CHILL request parameters required")
    percentile, target, elapsed = (parameters[key] for key in ("chill_slo_percentile", "chill_slo_target_ns", "chill_elapsed_ns"))
    if type(percentile) not in (int, float) or not 0 < percentile <= 100 or type(target) is not int or target < 1 or type(elapsed) is not int or elapsed < 0:
        raise InputError("invalid authoritative SLO/elapsed parameters")
    metadata = {"requestID": identity(request.request_id()), "workloadUID": identity(parameters["chill_workload_uid"]), "definition": identity(parameters["chill_definition"])}
    parts = metadata["definition"].split("/")
    if len(parts) != 4 or parts[0] != metadata["workloadUID"] or not parts[1].isdecimal() or int(parts[1]) < 1 or not parts[2] or not parts[3].isdecimal() or int(parts[3]) < 1:
        raise InputError("canonical immutable definition identity required")
    return metadata


def progress(metadata, phase):
    record = {**metadata, "phase": phase, "at": datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")}
    # No payloads, credentials, or outcomes; <= 4096 bytes even with escaped IDs.
    line = PROGRESS_MARKER + json.dumps(record, separators=(",", ":"), ensure_ascii=False)
    if len(line.encode("utf-8")) > 4096:
        raise InputError("progress record exceeds bound")
    print(line, flush=True)


class TritonPythonModel:
    def initialize(self, args):
        config = json.loads(args["model_config"])
        parameters = {key: value["string_value"] for key, value in config["parameters"].items()}
        self.max_batch_size = int(config["max_batch_size"])
        self.tensors = json.loads(parameters["chill_tensors"])
        self.engine = session(parameters["chill_artifact_path"], int(parameters["chill_intra_op_threads"]), int(parameters["chill_inter_op_threads"]))
        actual_parameters = runtime_parameters(self.engine)
        if any(parameters.get(key) != value for key, value in actual_parameters.items()) or parameters["chill_guard"] != GUARD:
            raise ValueError("actual underlying runtime does not match published config")
        inputs, outputs = model_metadata(self.engine, self.tensors, self.max_batch_size)
        def declarations(items):
            return {item["name"]: (item["data_type"], [int(dim) for dim in item.get("dims", [])], [int(dim) for dim in item["reshape"].get("shape", [])] if "reshape" in item else None) for item in items}
        if declarations(config["input"]) != declarations(inputs) or declarations(config["output"]) != declarations(outputs):
            raise ValueError("loaded model tensor metadata differs from native config")
        groups = config["instance_group"]
        dynamic = config.get("dynamic_batching", {})
        if len(groups) != 1 or groups[0]["kind"] != "KIND_CPU" or groups[0]["count"] != 1 or args["model_instance_kind"] != "CPU":
            raise ValueError("exactly one CPU instance required")
        if config.get("model_transaction_policy", {}).get("decoupled", False) or "sequence_batching" in config or "ensemble_scheduling" in config:
            raise ValueError("synchronous dynamic batching required")
        lanes = int(dynamic.get("priority_levels", 0))
        policy = dynamic.get("default_queue_policy", {})
        if int(dynamic.get("max_queue_delay_microseconds", 0)) != 0 or dynamic.get("preferred_batch_size") != [self.max_batch_size] or lanes < 1 or int(dynamic.get("default_priority_level", 0)) != lanes or int(policy.get("max_queue_size", 0)) < 1 or not policy.get("allow_timeout_override", False) or int(policy.get("default_timeout_microseconds", 0)) != QUEUE_TIMEOUT_CEILING or policy.get("timeout_action", "REJECT") != "REJECT" or dynamic.get("priority_queue_policy"):
            raise ValueError("bounded fixed native priority lanes with zero additional wait required")
        library = Path(parameters["TRITON_BATCH_STRATEGY_PATH"]).resolve(strict=True)
        model_dir = Path(args["model_repository"])
        if library.parent != model_dir.resolve() or library.name != "batchstrategy.so":
            raise ValueError("batch guard must be installed inside the native model directory")
        # Triton loads all five callbacks from this mandatory path before readiness;
        # the Python stub must not dlopen it (native symbols live in the server).
        if not library.is_file():
            raise ValueError("native batch guard file missing")
        self.output_names = [item.name for item in self.engine.get_outputs()]
        self.output_dtypes = {item["name"]: DTYPES[item["data_type"][5:]][1] for item in outputs}

    def execute(self, requests):
        try:
            if not requests or len(requests) > self.max_batch_size:
                raise InputError("invalid native batch size")
            metadata = [request_metadata(request) for request in requests]
            for item in metadata:
                progress(item, "running")
            if len({item["workloadUID"] for item in metadata}) != 1:
                raise InputError("native batching invariant violated: mixed Workload UIDs")
            sizes = []
            for request in requests:
                first = pb_utils.get_input_tensor_by_name(request, self.tensors[0]["name"])
                if first is None:
                    raise InputError("request input name mismatch")
                shape = first.as_numpy().shape
                if not shape or shape[0] < 1:
                    raise InputError("positive native request batch axis required")
                sizes.append(shape[0])
            actual_batch = sum(sizes)
            if actual_batch > self.max_batch_size:
                raise InputError("native request batch exceeds loaded maximum")
            feed = {}
            for tensor in self.tensors:
                values = []
                for request, size in zip(requests, sizes):
                    if len(request.inputs()) != len(self.tensors):
                        raise InputError("request input count mismatch")
                    native = pb_utils.get_input_tensor_by_name(request, tensor["name"])
                    if native is None:
                        raise InputError("request input name mismatch")
                    value = native.as_numpy()
                    if value.dtype != DTYPES[tensor["datatype"]][1] or value.shape != (size, *tensor["sampleShape"]):
                        raise InputError("native input shape/type mismatch")
                    values.append(value)
                feed[tensor["name"]] = values[0] if len(values) == 1 else np.concatenate(values, axis=0)
            outputs = self.engine.run(self.output_names, feed)
            for name, output in zip(self.output_names, outputs):
                if output.ndim < 1 or output.shape[0] != actual_batch or output.dtype != self.output_dtypes[name]:
                    raise InputError("actual output batch/type violates native model contract")
            responses = []
            offset = 0
            for size in sizes:
                responses.append(pb_utils.InferenceResponse(
                    output_tensors=[pb_utils.Tensor(name, output[offset:offset + size]) for name, output in zip(self.output_names, outputs)],
                    parameters={"chill_actual_batch_size": actual_batch, "chill_actual_batch_requests": len(requests)}))
                offset += size
            return responses
        except Exception as error:
            # Deliberately do not include ORT's exception: it may carry model/input data.
            message = str(error) if isinstance(error, InputError) else "native CPU inference failed"
            return [pb_utils.InferenceResponse(error=pb_utils.TritonError(message[:512])) for _ in requests]
