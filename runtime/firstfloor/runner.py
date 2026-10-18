"""Persistent firstfloor runner executing preprocess, infer and postprocess per batch; stdout is framed IPC only."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import struct
import uuid
import sys
import time

MAX_HEADER_BYTES = 64 << 10
MAX_FRAME_BYTES = 64 << 20
MAX_ERROR_BYTES = 1024
MODULE_NAMES = ("WARMUP_SAMPLE", "preprocess", "infer", "postprocess")


class BatchError(Exception):
    """The preprocessor module broke its result contract for one batch; the runner keeps serving."""


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


def bounded(text):
    return text.encode("utf-8")[:MAX_ERROR_BYTES].decode("utf-8", "ignore") or "unspecified failure"


def frame_elements(values):
    return b"".join(struct.pack("<I", len(v)) + v for v in values)


def unframe(data, start, end, count):
    """Split count BYTES elements, each a uint32 little-endian length plus bytes, from data[start:end]."""
    elements = []
    while start < end and len(elements) < count:
        if start + 4 > end:
            raise ValueError("truncated BYTES element length")
        size = struct.unpack_from("<I", data, start)[0]
        start += 4
        if start + size > end:
            raise ValueError("truncated BYTES element")
        elements.append(data[start:start + size])
        start += size
    if len(elements) != count or start != end:
        raise ValueError("BYTES element count differs from shape")
    return elements


def field_names(value, label):
    if not isinstance(value, list) or not value or len(set(value)) != len(value) or any(not isinstance(n, str) or not n for n in value):
        raise ValueError(f"unique nonempty {label} field names required")
    return value


def read_init(source):
    message = read_frame(source)
    if message is None:
        raise ValueError("missing init frame")
    h, data = message
    if h.get("type") != "init" or data:
        raise ValueError("invalid init frame")
    field_names(h.get("inputs"), "input")
    field_names(h.get("outputs"), "output")
    batches, tokens = h.get("batch_sizes"), h.get("max_new_tokens")
    if not isinstance(batches, list) or not batches or len(set(batches)) != len(batches) or any(type(k) is not int or k < 1 for k in batches):
        raise ValueError("invalid initialized batch sizes")
    if type(tokens) is not int or tokens < 0:
        raise ValueError("nonnegative max_new_tokens required")
    return h


def import_runtime_contract(family):
    path = Path(__file__).resolve().parent.parent / family / "runner.py"
    spec = importlib.util.spec_from_file_location("chill_" + family + "_contract", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_preprocessor(directory, inputs):
    spec = importlib.util.spec_from_file_location("chill_preprocessor", Path(directory) / "preprocessor.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    missing = [name for name in MODULE_NAMES if not hasattr(module, name)]
    if missing:
        raise ValueError("preprocessor module lacks " + ", ".join(missing))
    sample = module.WARMUP_SAMPLE
    if not isinstance(sample, dict) or set(sample) != set(inputs) or any(not isinstance(v, bytes) for v in sample.values()):
        raise ValueError("WARMUP_SAMPLE must hold bytes for exactly the raw input fields")
    return module


def model_files(directory):
    files = sorted(p for p in Path(directory).iterdir() if p.is_file())
    if not files:
        raise ValueError("model directory holds no model files")
    if len({p.stem for p in files}) != len(files):
        raise ValueError("model file names must differ without extension")
    return files


def execute(module, sessions, samples, outputs, max_new_tokens, delay_s):
    """Run one execution unit; any module contract violation raises BatchError."""
    try:
        start = time.perf_counter_ns()
        model_inputs, reports = module.preprocess(samples)
        timings = {"preprocess_ms": (time.perf_counter_ns() - start) / 1e6, "backend_ms": 0.0, "postprocess_ms": 0.0}
        if not isinstance(reports, list) or len(reports) != len(samples):
            raise BatchError("preprocess report count differs from batch size")
        items = []
        for report in reports:
            if type(report) is int and report >= 1:
                items.append({"units": report})
            elif isinstance(report, str) and report:
                items.append({"error": bounded(report)})
            else:
                raise BatchError("preprocess report must be a unit count of at least one or a failure reason")
        successes = sum("units" in item for item in items)
        columns = {name: [] for name in outputs}
        if not successes:
            return items, columns, timings
        tokens = []

        def first_token():
            tokens.append(time.perf_counter_ns())

        backend_start = time.perf_counter_ns()
        if delay_s:
            time.sleep(delay_s)
        values = module.infer(sessions, model_inputs, max_new_tokens, first_token)
        backend_end = time.perf_counter_ns()
        if max_new_tokens > 0:
            if len(tokens) != 1:
                raise BatchError(f"generative infer called first_token {len(tokens)} times; exactly once required")
            timings["first_token_ms"] = (tokens[0] - backend_start) / 1e6
        results = module.postprocess(values)
        timings["backend_ms"] = (backend_end - backend_start) / 1e6
        timings["postprocess_ms"] = (time.perf_counter_ns() - backend_end) / 1e6
        if not isinstance(results, list) or len(results) != successes:
            raise BatchError("postprocess result count differs from successful requests")
        for result in results:
            if not isinstance(result, dict) or set(result) != set(outputs) or any(not isinstance(v, bytes) for v in result.values()):
                raise BatchError("postprocess result must map exactly the raw output fields to bytes")
            for name in outputs:
                columns[name].append(result[name])
        return items, columns, timings
    except BatchError:
        raise
    except Exception as error:
        raise BatchError(f"preprocessor module raised {type(error).__name__}: {error}") from error


def ort_sessions(options, files, metadata):
    import onnxruntime as ort

    provider = "CUDAExecutionProvider" if options.runtime == "ort-cuda" else "CPUExecutionProvider"
    available = ort.get_available_providers()
    if provider not in available:
        raise ValueError(f"required {provider} unavailable")
    settings = ort.SessionOptions()
    settings.intra_op_num_threads = options.intra_op_threads
    settings.inter_op_num_threads = 1
    providers, expected = [provider], [provider]
    if options.runtime == "ort-cuda":
        if not options.gpu_uuid or os.environ.get("CUDA_VISIBLE_DEVICES") != options.gpu_uuid:
            raise ValueError("CUDA_VISIBLE_DEVICES must pin the explicit physical GPU UUID")
        # The libcuda driver API keeps these v1 entry points from CUDA 10.2 through 12.x.
        import ctypes

        try:
            cu = ctypes.CDLL("libcuda.so.1")
        except OSError as error:
            raise ValueError("CUDA driver initialization failed") from error
        if cu.cuInit(0) != 0:
            raise ValueError("CUDA driver initialization failed")
        count, device, actual = ctypes.c_int(), ctypes.c_int(), ctypes.create_string_buffer(16)
        if cu.cuDeviceGetCount(ctypes.byref(count)) != 0 or count.value != 1:
            raise ValueError("explicit GPU allocation must expose exactly one CUDA device")
        if cu.cuDeviceGet(ctypes.byref(device), 0) != 0:
            raise ValueError("selected CUDA device could not be obtained")
        if cu.cuDeviceGetUuid(actual, device) != 0 or "GPU-" + str(uuid.UUID(bytes=actual.raw)) != options.gpu_uuid:
            raise ValueError("selected physical GPU UUID could not be verified")
        # ORT always places shape subgraphs on the CPU EP; the measured profile records the actual provider chain.
        providers, expected = [(provider, {"device_id": 0})], [provider, "CPUExecutionProvider"]
        metadata.update(gpuUUID=options.gpu_uuid, device="cuda")
    else:
        metadata["device"] = "cpu"
    sessions = {}
    for path in files:
        session = ort.InferenceSession(str(path), sess_options=settings, providers=providers)
        session.disable_fallback()
        selected = session.get_providers()
        if selected != expected:
            raise ValueError(f"unexpected providers/fallback {selected} for {path.name}")
        sessions[path.stem] = session
    metadata.update(runtimeVersion=ort.__version__, providers=expected, availableProviders=available)
    return sessions


def rknn_session(options, files, batches, metadata):
    from rknnlite.api import RKNNLite

    if len(files) != 1 or files[0].suffix != ".rknn":
        raise ValueError("RKNNLite requires exactly one .rknn model file")
    if not options.delivery_json:
        raise ValueError("RKNN requires actual delivered host/artifact contract")
    model = files[0]
    helper = import_runtime_contract("rknnlite")
    candidate = json.loads(Path(options.delivery_json).read_text())
    helper.validate_candidate(candidate)
    tensors = candidate["artifactCapability"]["tensors"]
    if len(tensors) != 1 or tensors[0]["datatype"] != "FP32":
        raise ValueError("RKNN delivery must declare one FP32 NHWC tensor")
    evidence = helper.observe_host_contract(candidate, batches[0])
    for batch in batches:
        helper.validate_runtime_inputs(model, candidate, batch)
    rknn = RKNNLite()
    if rknn.load_rknn(str(model)) != 0:
        rknn.release()
        raise ValueError("RKNN model load failed")
    evidence["loadSucceeded"] = True
    if rknn.init_runtime(core_mask=RKNNLite.NPU_CORE_0) != 0:
        rknn.release()
        raise ValueError("RKNN native initialization failed")
    evidence["initSucceeded"] = True
    metadata.update(runtimeVersion=evidence["package"]["version"], device="npu", rknn=evidence, intraOpThreads=0, interOpThreads=0)
    return {model.stem: rknn}


def run(options, source, destination):
    sessions = {}
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
        inputs, outputs = contract["inputs"], contract["outputs"]
        batches, max_new_tokens = contract["batch_sizes"], contract["max_new_tokens"]
        module = load_preprocessor(options.preprocessor, inputs)
        files = [] if options.runtime == "echo" else model_files(options.model_dir)
        metadata = {"runtime": options.runtime, "runtimeVersion": "synthetic", "numpyVersion": np.__version__,
                    "modelFiles": [os.path.abspath(p) for p in files], "inputs": inputs, "outputs": outputs,
                    "providers": [], "device": "synthetic", "cpuAffinity": affinity,
                    "intraOpThreads": options.intra_op_threads, "interOpThreads": 1,
                    "initializedBatches": batches, "warmupIterations": options.warmup_iterations,
                    "synthetic": options.runtime == "echo", "cgroupCPUMax": cpu_max, "cpuSet": cpuset}
        if options.runtime in ("ort-cpu", "ort-cuda"):
            sessions = ort_sessions(options, files, metadata)
        elif options.runtime == "rknnlite":
            sessions = rknn_session(options, files, batches, metadata)
        delay_s = options.delay_ms / 1000
        for batch in batches:
            for _ in range(options.warmup_iterations):
                items, _, _ = execute(module, sessions, [dict(module.WARMUP_SAMPLE) for _ in range(batch)], outputs, max_new_tokens, delay_s)
                if any("error" in item for item in items):
                    raise ValueError("WARMUP_SAMPLE failed preprocessing")
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
            columns, offset, batch = [], 0, None
            for name, wire in zip(inputs, tensors):
                shape = wire.get("shape") if isinstance(wire, dict) else None
                if shape is None or wire.get("name") != name or wire.get("datatype") != "BYTES" or not isinstance(shape, list) or len(shape) != 1 or shape[0] not in batches:
                    raise ValueError("inference tensor outside initialized contract")
                if batch is not None and batch != shape[0]:
                    raise ValueError("input batch axes disagree")
                batch, size = shape[0], wire.get("bytes")
                if type(size) is not int or size < 0 or offset + size > len(data):
                    raise ValueError("tensor bytes exceed payload")
                columns.append(unframe(data, offset, offset + size, batch))
                offset += size
            if offset != len(data):
                raise ValueError("unbound input payload bytes")
            samples = [{name: column[i] for name, column in zip(inputs, columns)} for i in range(batch)]
            try:
                items, results, timings = execute(module, sessions, samples, outputs, max_new_tokens, delay_s)
                chunks = [frame_elements(results[name]) for name in outputs]
                if sum(len(chunk) for chunk in chunks) > MAX_FRAME_BYTES:
                    raise BatchError("postprocess outputs exceed IPC frame cap")
            except BatchError as error:
                write_frame(destination, {"type": "batch_error", "batch_id": batch_id, "error": bounded(str(error))})
                continue
            successes = len(results[outputs[0]])
            descriptors = [{"name": name, "datatype": "BYTES", "shape": [successes], "bytes": len(chunk)} for name, chunk in zip(outputs, chunks)]
            timings["service_ms"] = (time.perf_counter_ns() - service_start) / 1e6
            write_frame(destination, dict(timings, type="output", batch_id=batch_id, items=items, tensors=descriptors), b"".join(chunks))
    except Exception as error:
        header = {"type": "error", "error": bounded(str(error))}
        if type(batch_id) is int and batch_id > 0:
            header["batch_id"] = batch_id
        try:
            write_frame(destination, header)
        except (BrokenPipeError, OSError):
            pass
        raise
    finally:
        if options.runtime == "rknnlite":
            for session in sessions.values():
                session.release()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--echo", action="store_const", dest="runtime", const="echo", help="Explicit synthetic development execution without a model; never a native fallback.")
    mode.add_argument("--runtime", choices=("ort-cpu", "ort-cuda", "rknnlite"))
    parser.add_argument("--model-dir", help="Directory whose regular files are all loaded as model sessions.")
    parser.add_argument("--preprocessor", required=True, help="Directory holding preprocessor.py.")
    parser.add_argument("--gpu-uuid")
    parser.add_argument("--delivery-json")
    parser.add_argument("--intra-op-threads", type=int, default=1)
    parser.add_argument("--cpu-affinity", help="Exact comma-separated CPU IDs within the allocated cpuset.")
    parser.add_argument("--warmup-iterations", type=int, default=20)
    parser.add_argument("--delay-ms", type=float, default=0, help="Synthetic sleep inside the echo infer stage.")
    options = parser.parse_args()
    if options.delay_ms < 0 or options.intra_op_threads < (0 if options.runtime == "rknnlite" else 1) or options.warmup_iterations < 0:
        parser.error("nonnegative delay/warmup and positive ORT threads required")
    if (options.runtime == "echo") == bool(options.model_dir) or options.runtime != "echo" and options.delay_ms:
        parser.error("native execution requires --model-dir and rejects synthetic delay; echo takes no model")
    # Preserve a private IPC descriptor before redirecting native SDK stdout.
    with os.fdopen(os.dup(sys.stdout.fileno()), "wb") as destination:
        os.dup2(sys.stderr.fileno(), sys.stdout.fileno())
        try:
            run(options, sys.stdin.buffer, destination)
        except Exception as error:
            print(f"runner failed: {error}", file=sys.stderr, flush=True)
            sys.exit(1)
