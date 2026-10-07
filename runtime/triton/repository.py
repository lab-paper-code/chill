"""Prepare a native Triton CPU/Python model repository from actual ORT metadata."""
from __future__ import annotations

import argparse
import json
import re
import shutil
from pathlib import Path

import numpy as np
import onnxruntime as ort

from ortcpu.runner import INPUT_DATATYPES, read_cpu_environment

DTYPES = {name: (onnx_type, np.dtype(getattr(np, numpy_type)))
          for name, (onnx_type, numpy_type, _) in INPUT_DATATYPES.items()}
GUARD = "same-workload-uid-v1"
ORT_VERSION = "1.27.0"
# Triton only accepts overrides shorter than its nonzero default timeout.
# Strictly greater than every Go Duration lifetime rounded up to microseconds.
QUEUE_TIMEOUT_CEILING = ((1 << 63) - 1) // 1000 + 2


def session(artifact_path, intra_op_threads, inter_op_threads):
    if ort.__version__ != ORT_VERSION:
        raise ValueError(f"requires onnxruntime {ORT_VERSION}, installed {ort.__version__}")
    for threads in (intra_op_threads, inter_op_threads):
        if type(threads) is not int or threads < 1:
            raise ValueError("explicit positive ORT thread counts are required")
    options = ort.SessionOptions()
    options.intra_op_num_threads = intra_op_threads
    options.inter_op_num_threads = inter_op_threads
    options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
    result = ort.InferenceSession(str(artifact_path), sess_options=options, providers=["CPUExecutionProvider"])
    result.disable_fallback()
    actual = result.get_session_options()
    if result.get_providers() != ["CPUExecutionProvider"] or actual.intra_op_num_threads != intra_op_threads or actual.inter_op_num_threads != inter_op_threads:
        raise ValueError("loaded ORT provider/thread contract mismatch")
    return result


def runtime_parameters(engine):
    cpu_max, affinity, cpuset = read_cpu_environment()
    options = engine.get_session_options()
    return {
        "chill_runtime_family": "triton",
        "chill_provider": ",".join(engine.get_providers()),
        "chill_ort_version": ort.__version__,
        "chill_intra_op_threads": str(options.intra_op_num_threads),
        "chill_inter_op_threads": str(options.inter_op_num_threads),
        "chill_available_providers": json.dumps(ort.get_available_providers(), separators=(",", ":")),
        "chill_affinity_cpus": json.dumps(affinity, separators=(",", ":")),
        "chill_cpuset_effective": cpuset,
        "chill_cgroup_cpu_max": cpu_max,
    }


def model_metadata(engine, tensors, max_batch_size):
    if type(max_batch_size) is not int or max_batch_size < 1:
        raise ValueError("positive max_batch_size required")
    expected = {}
    for tensor in tensors:
        if set(tensor) != {"name", "datatype", "sampleShape"}:
            raise ValueError("tensor declaration must contain name, datatype, sampleShape")
        name, datatype, shape = tensor["name"], tensor["datatype"], tensor["sampleShape"]
        if not isinstance(name, str) or not name or name.strip() != name or name in expected:
            raise ValueError("input names must be nonempty and unique")
        if datatype not in DTYPES:
            raise ValueError(f"unsupported input datatype {datatype}; BYTES and BF16 are not supported")
        if not isinstance(shape, list) or not shape or any(type(dim) is not int or dim < 1 for dim in shape):
            raise ValueError("sampleShape must contain positive fixed dimensions")
        expected[name] = tensor
    if not expected or {item.name for item in engine.get_inputs()} != set(expected):
        raise ValueError("actual model input names differ from declaration")
    inputs = []
    for item in engine.get_inputs():
        tensor = expected[item.name]
        shape = item.shape
        if item.type != DTYPES[tensor["datatype"]][0] or len(shape) != len(tensor["sampleShape"]) + 1:
            raise ValueError(f"actual input type/rank mismatch: {item.name}")
        if isinstance(shape[0], int) and (shape[0] != 1 or max_batch_size != 1):
            raise ValueError(f"input {item.name} has a fixed incompatible batch axis")
        if any(type(actual) is not int or actual != declared for actual, declared in zip(shape[1:], tensor["sampleShape"])):
            raise ValueError(f"actual fixed sample shape mismatch: {item.name}")
        inputs.append({"name": item.name, "data_type": "TYPE_" + tensor["datatype"], "dims": tensor["sampleShape"]})
    outputs = []
    for item in engine.get_outputs():
        datatype = next((key for key, value in DTYPES.items() if value[0] == item.type), None)
        if datatype is None:
            raise ValueError(f"unsupported output type {item.type}; BYTES and BF16 are not supported")
        if not item.shape or (isinstance(item.shape[0], int) and (item.shape[0] != 1 or max_batch_size != 1)):
            raise ValueError(f"output {item.name} must expose a compatible batch axis")
        dims = [dim if type(dim) is int and dim > 0 else -1 for dim in item.shape[1:]]
        declaration = {"name": item.name, "data_type": "TYPE_" + datatype, "dims": dims or [1]}
        if not dims:
            declaration["reshape"] = {"shape": []}
        outputs.append(declaration)
    if not outputs:
        raise ValueError("model must have outputs")
    return inputs, outputs


def prepare_model_repository(root, model_name, artifact_path, tensors, max_batch_size,
                             intra_op_threads, inter_op_threads, priority_levels, queue_limit):
    if not isinstance(model_name, str) or not re.fullmatch(r"[A-Za-z0-9_-]+", model_name):
        raise ValueError("model_name must be a single safe path component")
    if type(priority_levels) is not int or priority_levels < 1:
        raise ValueError("priority_levels must be positive")
    if type(queue_limit) is not int or queue_limit < 1:
        raise ValueError("queue_limit must be positive")
    artifact = Path(artifact_path).resolve(strict=True)
    if not artifact.is_file():
        raise ValueError("artifact must be a regular ONNX file")
    engine = session(artifact, intra_op_threads, inter_op_threads)
    inputs, outputs = model_metadata(engine, tensors, max_batch_size)
    actual_parameters = runtime_parameters(engine)
    del engine
    model = Path(root).resolve() / model_name
    version = model / "1"
    version.mkdir(parents=True, exist_ok=True)
    source = Path(__file__).resolve().parent
    for name in ("model.py", "repository.py"):
        shutil.copyfile(source / name, version / name)
    shutil.copyfile(source / "batchstrategy.so", model / "batchstrategy.so")
    parameters = {
        **actual_parameters, "chill_guard": GUARD,
        "chill_artifact_path": str(artifact), "chill_tensors": json.dumps(tensors, separators=(",", ":")),
        "TRITON_BATCH_STRATEGY_PATH": str(model / "batchstrategy.so"),
    }
    lines = [
        f"name: {json.dumps(model_name)}", 'backend: "python"',
        f"max_batch_size: {max_batch_size}",
        "version_policy { specific { versions: 1 } }",
        "instance_group { kind: KIND_CPU count: 1 }",
        "model_transaction_policy { decoupled: false }",
        "dynamic_batching {",
        "max_queue_delay_microseconds: 0",
        f"preferred_batch_size: [ {max_batch_size} ]",
        f"priority_levels: {priority_levels} default_priority_level: {priority_levels}",
        f"default_queue_policy {{ max_queue_size: {queue_limit} timeout_action: REJECT default_timeout_microseconds: {QUEUE_TIMEOUT_CEILING} allow_timeout_override: true }}",
        "}",
    ]
    for kind, declarations in (("input", inputs), ("output", outputs)):
        for item in declarations:
            reshape = " reshape { shape: [] }" if "reshape" in item else ""
            lines.append(f'{kind} {{ name: {json.dumps(item["name"])} data_type: {item["data_type"]} dims: {json.dumps(item["dims"])}{reshape} }}')
    for key, value in parameters.items():
        lines.append(f"parameters {{ key: {json.dumps(key)} value {{ string_value: {json.dumps(value)} }} }}")
    (model / "config.pbtxt").write_text("\n".join(lines) + "\n", encoding="utf-8")
    return model


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--model-name", required=True)
    parser.add_argument("--artifact-path", required=True)
    parser.add_argument("--tensors", required=True, help="canonical tensor declaration JSON")
    parser.add_argument("--max-batch-size", required=True, type=int)
    parser.add_argument("--intra-op-threads", required=True, type=int)
    parser.add_argument("--inter-op-threads", required=True, type=int)
    parser.add_argument("--priority-levels", required=True, type=int)
    parser.add_argument("--queue-limit", required=True, type=int)
    args = parser.parse_args()
    prepare_model_repository(args.root, args.model_name, args.artifact_path, json.loads(args.tensors),
                             args.max_batch_size, args.intra_op_threads, args.inter_op_threads,
                             args.priority_levels, args.queue_limit)


if __name__ == "__main__":
    main()
