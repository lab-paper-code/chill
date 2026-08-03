import hashlib
import json
import os
import time
from pathlib import Path

import numpy as np
import onnxruntime as ort


def emit(event, **fields):
    payload = {"event": event, "epochNs": time.time_ns(), **fields}
    print(
        "TRANSITION_EVENT "
        + json.dumps(payload, sort_keys=True, separators=(",", ":")),
        flush=True,
    )


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return "sha256:" + value.hexdigest()


def cache_facts(path):
    files = [item for item in path.rglob("*") if item.is_file()]
    return {"files": len(files), "bytes": sum(item.stat().st_size for item in files)}


model = Path(os.environ["MODEL_PATH"])
model_name = os.environ["MODEL_NAME"]
expected = os.environ["ARTIFACT_DIGEST"]
cache = Path("/engine-cache")
cache.mkdir(parents=True, exist_ok=True)
emit(
    "process_started",
    model=model_name,
    pid=os.getpid(),
    runtimeVersion=ort.__version__,
    providers=ort.get_available_providers(),
    cache=cache_facts(cache),
)

verify_started = time.monotonic()
actual = digest(model)
if actual != expected:
    raise SystemExit(f"artifact digest mismatch: expected={expected} actual={actual}")
emit(
    "artifact_verified",
    model=model_name,
    digest=actual,
    bytes=model.stat().st_size,
    seconds=time.monotonic() - verify_started,
)

options = {
    "trt_engine_cache_enable": True,
    "trt_engine_cache_path": str(cache),
    "trt_fp16_enable": False,
    "trt_int8_enable": False,
}
session_started = time.monotonic()
emit("session_create_started", model=model_name, cache=cache_facts(cache))
session = ort.InferenceSession(
    str(model),
    providers=[("TensorrtExecutionProvider", options), "CUDAExecutionProvider"],
)
emit(
    "session_created",
    model=model_name,
    seconds=time.monotonic() - session_started,
    providers=session.get_providers(),
    cache=cache_facts(cache),
)

input_meta = session.get_inputs()[0]
shape = [1] + [
    dimension if isinstance(dimension, int) else 1
    for dimension in input_meta.shape[1:]
]
sample = np.random.default_rng(0).standard_normal(shape).astype(np.float32)
first_started = time.monotonic()
emit(
    "first_inference_started",
    model=model_name,
    inputShape=shape,
    cache=cache_facts(cache),
)
outputs = session.run(None, {input_meta.name: sample})
if not outputs:
    raise SystemExit("first inference returned no outputs")
emit(
    "first_inference_completed",
    model=model_name,
    seconds=time.monotonic() - first_started,
    cache=cache_facts(cache),
)

warmup_started = time.monotonic()
for _ in range(3):
    session.run(None, {input_meta.name: sample})
emit(
    "warmup_completed",
    model=model_name,
    iterations=3,
    seconds=time.monotonic() - warmup_started,
)
Path("/tmp/model-ready").write_text("ready\n")
emit(
    "ready",
    model=model_name,
    totalSeconds=time.monotonic() - session_started,
    cache=cache_facts(cache),
)

window_index = 0
while True:
    window_index += 1
    deadline = time.monotonic() + 1.0
    latencies = []
    while time.monotonic() < deadline:
        before = time.perf_counter_ns()
        session.run(None, {input_meta.name: sample})
        latencies.append((time.perf_counter_ns() - before) / 1_000_000)
    emit(
        "service_window",
        model=model_name,
        index=window_index,
        inferences=len(latencies),
        meanMs=float(np.mean(latencies)),
        p95Ms=float(np.percentile(latencies, 95)),
        p99Ms=float(np.percentile(latencies, 99)),
    )
