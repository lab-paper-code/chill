"""Persistent numeric/bool runner adopted from firstfloor; stdout is framed IPC only."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import time

MAX_HEADER_BYTES = 64 << 10
MAX_FRAME_BYTES = 64 << 20
MAX_ERROR_BYTES = 1024
DTYPES = {"BOOL": "?", "UINT8": "u1", "INT8": "i1", "UINT16": "<u2", "INT16": "<i2", "UINT32": "<u4", "INT32": "<i4", "UINT64": "<u8", "INT64": "<i8", "FP16": "<f2", "FP32": "<f4", "FP64": "<f8"}
ORT_TYPES = {"tensor(bool)": "BOOL", "tensor(uint8)": "UINT8", "tensor(int8)": "INT8", "tensor(uint16)": "UINT16", "tensor(int16)": "INT16", "tensor(uint32)": "UINT32", "tensor(int32)": "INT32", "tensor(uint64)": "UINT64", "tensor(int64)": "INT64", "tensor(float16)": "FP16", "tensor(float)": "FP32", "tensor(double)": "FP64"}


def read_exact(stream, count, allow_eof=False):
    chunks = bytearray()
    while len(chunks) < count:
        chunk = stream.read(count - len(chunks))
        if not chunk:
            if allow_eof and not chunks:
                return None
            raise ValueError("truncated IPC frame")
        chunks.extend(chunk)
    return bytes(chunks)


def read_frame(stream):
    prefix = read_exact(stream, 4, allow_eof=True)
    if prefix is None:
        return None
    length = struct.unpack(">I", prefix)[0]
    if not 0 < length <= MAX_HEADER_BYTES:
        raise ValueError("invalid IPC header length")
    header = json.loads(read_exact(stream, length))
    if not isinstance(header, dict):
        raise ValueError("IPC header must be an object")
    count = header.get("data_bytes")
    if type(count) is not int or not 0 <= count <= MAX_FRAME_BYTES:
        raise ValueError("invalid IPC payload length")
    return header, read_exact(stream, count)


def write_frame(stream, header, data=b""):
    encoded = json.dumps(dict(header, data_bytes=len(data)), separators=(",", ":"), allow_nan=False).encode()
    if len(encoded) > MAX_HEADER_BYTES or len(data) > MAX_FRAME_BYTES:
        raise ValueError("IPC frame exceeds size limit")
    stream.write(struct.pack(">I", len(encoded)))
    stream.write(encoded)
    stream.write(data)
    stream.flush()


def tensor_elements(shape):
    if not isinstance(shape, list):
        raise ValueError("tensor shape must be a list")
    count = 1
    for dimension in shape:
        if type(dimension) is not int or dimension <= 0:
            raise ValueError("positive tensor dimensions required")
        count *= dimension
        if count > MAX_FRAME_BYTES:
            raise ValueError("tensor exceeds frame limit")
    return count


def read_init(source):
    message = read_frame(source)
    if message is None:
        raise ValueError("missing init frame")
    h, data = message
    if h.get("type") != "init" or data:
        raise ValueError("invalid init frame")
    inputs, batches = h.get("inputs"), h.get("batch_sizes")
    if not isinstance(inputs, list) or not inputs or not isinstance(batches, list) or not batches:
        raise ValueError("named inputs and nonempty initialized batch sizes required")
    names = set()
    for spec in inputs:
        if not isinstance(spec.get("name"), str) or not spec["name"] or spec["name"] in names or spec.get("datatype") not in DTYPES:
            raise ValueError("invalid named input")
        tensor_elements(spec.get("shape"))
        names.add(spec["name"])
    if len(set(batches)) != len(batches) or any(type(k) is not int or k < 1 for k in batches):
        raise ValueError("invalid initialized batch sizes")
    return h


def import_runtime_contract(family):
    path = Path(__file__).resolve().parent.parent / family / "runner.py"
    spec = importlib.util.spec_from_file_location("chill_" + family + "_contract", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def rknn_outputs(helper, candidate, model_path):
    # Reuse maintained ABI structures from CHILL's RKNN evidence owner.
    ct = helper.ctypes
    library_path = next(x["containerPath"] for x in candidate["hostContract"]["libraries"] if x["name"] == "librknnrt")
    library = ct.CDLL(library_path)
    library.rknn_init.argtypes = [ct.POINTER(ct.c_uint64), ct.c_void_p, ct.c_uint32, ct.c_uint32, ct.c_void_p]
    library.rknn_init.restype = ct.c_int
    library.rknn_query.argtypes = [ct.c_uint64, ct.c_int, ct.c_void_p, ct.c_uint32]
    library.rknn_query.restype = ct.c_int
    library.rknn_destroy.argtypes = [ct.c_uint64]
    library.rknn_destroy.restype = ct.c_int
    context = ct.c_uint64()
    if library.rknn_init(ct.byref(context), ct.c_char_p(os.fsencode(model_path)), 0, 0, None) != 0:
        raise ValueError("RKNN output metadata initialization failed")
    try:
        counts = helper.RKNNInputOutputNum()
        if library.rknn_query(context, 0, ct.byref(counts), ct.sizeof(counts)) != 0 or counts.n_output < 1:
            raise ValueError("RKNN output count query failed")
        outputs = []
        for index in range(counts.n_output):
            attr = helper.RKNNTensorAttr(index=index)
            if library.rknn_query(context, 2, ct.byref(attr), ct.sizeof(attr)) != 0:
                raise ValueError("RKNN output metadata query failed")
            shape = list(attr.dims[:attr.n_dims])
            if not shape or type(shape[0]) is not int or shape[0] < 1:
                raise ValueError("RKNN output must have an actual positive batch axis")
            # RKNNLite returns dequantized FP32 outputs; warmup verifies that SDK contract.
            outputs.append({"name": attr.name.decode("utf-8"), "datatype": "FP32", "shape": shape[1:]})
        return outputs
    finally:
        if library.rknn_destroy(context) != 0:
            raise ValueError("RKNN metadata destruction failed")


def run(options, source, destination):
    rknn = None
    batch_id = 0
    try:
        contract = read_init(source)
        if options.cpu_affinity:
            requested = {int(v) for v in options.cpu_affinity.split(",")}
            if not requested or min(requested) < 0 or not requested.issubset(os.sched_getaffinity(0)):
                raise ValueError("requested CPU affinity exceeds actual allocated cpuset")
            os.sched_setaffinity(0, requested)
            if os.sched_getaffinity(0) != requested:
                raise ValueError("actual CPU affinity differs from requested allocation")
        import numpy as np

        if options.runtime == "echo":
            cpu_max, affinity, cpuset = "", sorted(os.sched_getaffinity(0)), ""
        else:
            cpu_max, affinity, cpuset = import_runtime_contract("ortcpu").read_cpu_environment()
        inputs = contract["inputs"]
        batches = contract["batch_sizes"]
        metadata = {"runtime": options.runtime, "runtimeVersion": "synthetic", "numpyVersion": np.__version__,
                    "modelPath": options.model or "", "inputs": inputs, "outputs": [], "providers": [],
                    "device": "synthetic", "cpuAffinity": affinity,
                    "intraOpThreads": options.intra_op_threads, "interOpThreads": 1,
                    "initializedBatches": batches, "warmupIterations": options.warmup_iterations,
                    "synthetic": options.runtime == "echo", "cgroupCPUMax": cpu_max,
                    "cpuSet": cpuset}
        if options.runtime == "echo":
            outputs = [dict(s) for s in inputs]

            def infer(samples):
                time.sleep(options.delay_ms / 1000)
                return samples
        else:
            if not options.model or not Path(options.model).is_file():
                raise ValueError("native model must be a regular local file")
            if options.runtime in ("ort-cpu", "ort-cuda"):
                import onnxruntime as ort

                provider = "CUDAExecutionProvider" if options.runtime == "ort-cuda" else "CPUExecutionProvider"
                available = ort.get_available_providers()
                if provider not in available:
                    raise ValueError(f"required {provider} unavailable")
                settings = ort.SessionOptions()
                settings.intra_op_num_threads = options.intra_op_threads
                settings.inter_op_num_threads = 1
                providers = [provider]
                if options.runtime == "ort-cuda":
                    if not options.gpu_uuid or os.environ.get("CUDA_VISIBLE_DEVICES") != options.gpu_uuid:
                        raise ValueError("CUDA_VISIBLE_DEVICES must pin the explicit physical GPU UUID")
                    actual = subprocess.check_output(["nvidia-smi", "--id=" + options.gpu_uuid, "--query-gpu=uuid", "--format=csv,noheader"], text=True).splitlines()
                    if actual != [options.gpu_uuid]:
                        raise ValueError("selected physical GPU UUID could not be verified")
                    settings.add_session_config_entry("session.disable_cpu_ep_fallback", "1")
                    providers = [(provider, {"device_id": 0})]
                    metadata["gpuUUID"] = options.gpu_uuid
                    metadata["device"] = "cuda"
                else:
                    metadata["device"] = "cpu"
                session = ort.InferenceSession(options.model, sess_options=settings, providers=providers)
                session.disable_fallback()
                selected = session.get_providers()
                # ORT may register its default CPU EP even when CPU assignment is disabled.
                cuda_without_fallback = (options.runtime == "ort-cuda" and
                                         selected == [provider, "CPUExecutionProvider"] and
                                         session.get_session_options().get_session_config_entry("session.disable_cpu_ep_fallback") == "1")
                if selected != [provider] and not cuda_without_fallback:
                    raise ValueError(f"unexpected providers/fallback {selected}")
                actual_inputs = session.get_inputs()
                if len(actual_inputs) != len(inputs):
                    raise ValueError("model input count differs from contract")
                by_name = {s["name"]: s for s in inputs}
                for model_input in actual_inputs:
                    expected = by_name.get(model_input.name)
                    if expected is None or ORT_TYPES.get(model_input.type) != expected["datatype"]:
                        raise ValueError("model input name or datatype differs")
                    for batch in batches:
                        shape = [batch, *expected["shape"]]
                        if len(shape) != len(model_input.shape) or any(isinstance(d, int) and d != v for d, v in zip(model_input.shape, shape)):
                            raise ValueError("model does not support initialized input batch/shape")
                outputs = []
                for output in session.get_outputs():
                    datatype = ORT_TYPES.get(output.type)
                    if datatype is None or not output.shape:
                        raise ValueError("unsupported nonnumeric or unbatched output")
                    shape = output.shape[1:]
                    if options.warmup_iterations == 0 and any(type(d) is not int or d <= 0 for d in shape):
                        raise ValueError("zero-inference idle requires static output sample shapes")
                    outputs.append({"name": output.name, "datatype": datatype, "shape": shape})

                def infer(samples):
                    return session.run(None, {s["name"]: sample for s, sample in zip(inputs, samples)})

                metadata.update(runtimeVersion=ort.__version__, providers=selected, availableProviders=available)
            else:
                from rknnlite.api import RKNNLite

                if len(inputs) != 1 or inputs[0]["datatype"] != "FP32" or len(inputs[0]["shape"]) != 3:
                    raise ValueError("RKNN supports only one FP32 NHWC input in this actor")
                if not options.delivery_json:
                    raise ValueError("RKNN requires actual delivered host/artifact contract")
                helper = import_runtime_contract("rknnlite")
                candidate = json.loads(Path(options.delivery_json).read_text())
                helper.validate_candidate(candidate)
                capability = candidate["artifactCapability"]
                if len(capability["tensors"]) != 1:
                    raise ValueError("RKNN delivery must declare one actual NHWC tensor")
                tensor = capability["tensors"][0]
                if tensor["name"] != inputs[0]["name"] or tensor["datatype"] != "FP32" or tensor["sampleShape"] != inputs[0]["shape"]:
                    raise ValueError("RKNN delivery tensor differs from initialized input")
                evidence = helper.observe_host_contract(candidate, batches[0])
                for batch in batches:
                    helper.validate_runtime_inputs(Path(options.model), candidate, batch)
                outputs = rknn_outputs(helper, candidate, options.model)
                rknn = RKNNLite()
                if rknn.load_rknn(options.model) != 0:
                    raise ValueError("RKNN model load failed")
                evidence["loadSucceeded"] = True
                if rknn.init_runtime(core_mask=RKNNLite.NPU_CORE_0) != 0:
                    raise ValueError("RKNN native initialization failed")
                evidence["initSucceeded"] = True
                metadata.update(runtimeVersion=evidence["package"]["version"], device="npu", rknn=evidence, intraOpThreads=0, interOpThreads=0)

                def infer(samples):
                    return rknn.inference(inputs=samples, data_format=["nhwc"])

        def validate_outputs(values, batch, establish=False):
            if not isinstance(values, (tuple, list)) or len(values) != len(outputs):
                raise ValueError("SDK output count differs from named model outputs")
            for spec, value in zip(outputs, values):
                if not isinstance(value, np.ndarray) or value.dtype != np.dtype(DTYPES[spec["datatype"]]) or value.ndim < 1 or value.shape[0] != batch:
                    raise ValueError("SDK output datatype/batch differs from model contract")
                tensor_elements(list(value.shape))
                if value.dtype.kind == "f" and not np.isfinite(value).all():
                    raise ValueError("nonfinite SDK output")
                if establish and any(type(d) is not int for d in spec["shape"]):
                    spec["shape"] = list(value.shape[1:])
                if list(value.shape[1:]) != spec["shape"]:
                    raise ValueError("SDK output sample shape changed")
            return values

        for batch in batches:
            samples = [np.zeros([batch, *s["shape"]], dtype=DTYPES[s["datatype"]]) for s in inputs]
            if sum(sample.nbytes for sample in samples) > MAX_FRAME_BYTES:
                raise ValueError("initialized input batch exceeds IPC cap")
            for _ in range(options.warmup_iterations):
                validate_outputs(infer(samples), batch, establish=True)
        metadata["outputs"] = outputs
        write_frame(destination, {"type": "ready", "metadata": metadata})
        while True:
            message = read_frame(source)
            if message is None:
                return
            service_start = time.perf_counter_ns()
            header, data = message
            batch_id = header.get("batch_id")
            tensors = header.get("tensors")
            if header.get("type") != "infer" or type(batch_id) is not int or batch_id <= 0 or not isinstance(tensors, list) or len(tensors) != len(inputs):
                raise ValueError("invalid inference frame")
            samples, offset, batch = [], 0, None
            for spec, wire in zip(inputs, tensors):
                shape = wire.get("shape")
                if not isinstance(shape, list) or not shape or shape[0] not in batches or shape[1:] != spec["shape"] or wire.get("name") != spec["name"] or wire.get("datatype") != spec["datatype"]:
                    raise ValueError("inference tensor outside initialized contract")
                if batch is not None and batch != shape[0]:
                    raise ValueError("multi-input batch axes disagree")
                batch = shape[0]
                count = tensor_elements(shape) * np.dtype(DTYPES[spec["datatype"]]).itemsize
                if wire.get("bytes") != count or offset + count > len(data):
                    raise ValueError("tensor shape and payload bytes disagree")
                sample = np.frombuffer(data, dtype=DTYPES[spec["datatype"]], count=tensor_elements(shape), offset=offset).reshape(shape)
                if sample.dtype.kind == "f" and not np.isfinite(sample).all():
                    raise ValueError("nonfinite input")
                samples.append(sample)
                offset += count
            if offset != len(data):
                raise ValueError("unbound input payload bytes")
            backend_start = time.perf_counter_ns()
            values = infer(samples)
            backend_ms = (time.perf_counter_ns() - backend_start) / 1e6
            validate_outputs(values, batch)
            descriptors, chunks = [], []
            total = 0
            for spec, value in zip(outputs, values):
                chunk = value.astype(DTYPES[spec["datatype"]], copy=False).tobytes(order="C")
                total += len(chunk)
                if total > MAX_FRAME_BYTES:
                    raise ValueError("combined outputs exceed IPC cap")
                chunks.append(chunk)
                descriptors.append(dict(spec, shape=list(value.shape), bytes=len(chunk)))
            payload = b"".join(chunks)
            service_ms = (time.perf_counter_ns() - service_start) / 1e6
            write_frame(destination, {"type": "output", "batch_id": batch_id, "tensors": descriptors, "backend_ms": backend_ms, "service_ms": service_ms}, payload)
    except Exception as error:
        header = {"type": "error", "error": str(error).encode("utf-8")[:MAX_ERROR_BYTES].decode("utf-8", "ignore")}
        if type(batch_id) is int and batch_id > 0:
            header["batch_id"] = batch_id
        try:
            write_frame(destination, header)
        except (BrokenPipeError, OSError):
            pass
        raise
    finally:
        if rknn is not None:
            rknn.release()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--echo", action="store_const", dest="runtime", const="echo", help="Explicit synthetic development execution; never a native fallback.")
    mode.add_argument("--runtime", choices=("ort-cpu", "ort-cuda", "rknnlite"))
    parser.add_argument("--model")
    parser.add_argument("--gpu-uuid")
    parser.add_argument("--delivery-json")
    parser.add_argument("--intra-op-threads", type=int, default=1)
    parser.add_argument("--cpu-affinity", help="Exact comma-separated CPU IDs within the allocated cpuset.")
    parser.add_argument("--warmup-iterations", type=int, default=20)
    parser.add_argument("--delay-ms", type=float, default=0)
    options = parser.parse_args()
    if options.delay_ms < 0 or options.intra_op_threads < (0 if options.runtime == "rknnlite" else 1) or options.warmup_iterations < 0:
        parser.error("nonnegative delay/warmup and positive ORT threads required")
    if options.runtime != "echo" and (not options.model or options.delay_ms):
        parser.error("native execution requires model and rejects synthetic delay")
    # Preserve a private IPC descriptor before redirecting native SDK stdout.
    with os.fdopen(os.dup(sys.stdout.fileno()), "wb") as destination:
        os.dup2(sys.stderr.fileno(), sys.stdout.fileno())
        try:
            run(options, sys.stdin.buffer, destination)
        except Exception as error:
            print(f"runner failed: {error}", file=sys.stderr, flush=True)
            sys.exit(1)
