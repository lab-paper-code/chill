#!/usr/bin/env python3
"""Fixed-artifact RKNN batch characterization runner."""

# TODO(internal): Consume the same versioned execution-contract object as the
# ORT runner instead of individual environment variables. DeviceClass facts,
# runtime/driver ABI, device interface, layout, core mask, and artifact
# capability must be resolved and validated by the profiler controller.
# TODO(internal): Replace privileged hostPath access with the cluster's RK3588
# device-plugin/resource contract before this leaves the isolated spike.

import hashlib
import json
import os
import time
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
from rknnlite.api import RKNNLite


def now():
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def checked_inference(runtime, sample, batch, stage):
    outputs = runtime.inference(inputs=[sample])
    if outputs is None or not outputs:
        raise SystemExit(f"RKNN {stage} inference returned no output")
    actual_shape = tuple(outputs[0].shape)
    expected_shape = (batch, 1000)
    if actual_shape != expected_shape:
        raise SystemExit(
            f"RKNN {stage} output shape mismatch: expected={expected_shape} actual={actual_shape}"
        )
    return outputs


def human_contract(batch, shape_mode, supported_batches):
    # TODO(internal): Render the controller-produced contract verbatim. The
    # spike manifest currently pins facts observed from one Orange Pi node.
    print("", flush=True)
    print("--- CHILL DERIVED EXECUTION CONTRACT -------------------------", flush=True)
    print(f"policy                  : {os.environ['EXECUTION_POLICY']}", flush=True)
    print(f"contract source         : {os.environ['EXECUTION_CONTRACT_SOURCE']}", flush=True)
    print(f"device class            : {os.environ['DEVICE_CLASS']}", flush=True)
    print(f"accelerator             : {os.environ['ACCELERATOR']}", flush=True)
    print(f"device interface        : {os.environ['ACCELERATOR_DEVICE_INTERFACE']}", flush=True)
    print("expected runtime / ABI  : RKNNLite 2.3.2 / librknnrt 2.3.2", flush=True)
    print("NPU core mask           : NPU_CORE_0", flush=True)
    print("source / runtime layout : NCHW / NHWC", flush=True)
    print(f"artifact shape mode     : {shape_mode}", flush=True)
    print(f"supported batches       : {','.join(map(str, supported_batches))}", flush=True)
    print(f"selected batch          : {batch}", flush=True)
    print("scheduling isolation    : unresolved", flush=True)
    print("---------------------------------------------------------------", flush=True)


def main():
    batch = int(os.environ["BATCH_SIZE"])
    model = Path(os.environ["MODEL_PATH"])
    expected = os.environ["ARTIFACT_DIGEST"]
    duration = float(os.environ.get("MEASUREMENT_DURATION_SECONDS", "30"))
    warmup = int(os.environ.get("WARMUP_ITERATIONS", "20"))
    shape_mode = os.environ["ARTIFACT_SHAPE_MODE"]
    supported_batches = [int(value) for value in os.environ["SUPPORTED_BATCHES"].split(",")]
    if shape_mode not in ("static", "enumerated-dynamic"):
        raise SystemExit(f"unsupported artifact shape mode: {shape_mode}")
    if batch not in supported_batches:
        raise SystemExit(f"batch {batch} is outside artifact capability {supported_batches}")
    if shape_mode == "static" and supported_batches != [batch]:
        raise SystemExit("static artifact must declare exactly its selected batch")
    actual = sha256(model)
    if actual != expected:
        raise SystemExit(f"artifact digest mismatch: expected={expected} actual={actual}")

    runtime = RKNNLite()
    if runtime.load_rknn(str(model)) != 0:
        raise SystemExit("load_rknn failed")
    if runtime.init_runtime(core_mask=RKNNLite.NPU_CORE_0) != 0:
        raise SystemExit("init_runtime failed")
    human_contract(batch, shape_mode, supported_batches)
    # RKNN Runtime exposes this compiled input as NHWC even though the source
    # ONNX graph is NCHW. Supplying NCHW asks RKNNLite to mutate/convert the
    # buffer; reusing that converted buffer with an NCHW declaration makes
    # subsequent calls fail. Keep the steady-state buffer in runtime-native
    # NHWC form instead.
    sample = np.random.default_rng(0).standard_normal((batch, 224, 224, 3)).astype(np.float32)
    print(
        f"{now()} INFO  [runtime] RKNN runtime ready | batch={batch} "
        f"shapeMode={shape_mode} supportedBatches={supported_batches} "
        "sourceLayout=NCHW runtimeInputLayout=NHWC core=NPU_CORE_0",
        flush=True,
    )
    for _ in range(warmup):
        checked_inference(runtime, sample, batch, "warm-up")

    ready = Path(os.environ["OBSERVER_READY_FILE"])
    deadline = time.monotonic() + 30
    while not ready.exists():
        if time.monotonic() >= deadline:
            raise SystemExit("power observer not ready")
        time.sleep(.01)
    Path(os.environ["MEASUREMENT_SIGNAL_FILE"]).write_text("start\n")

    latencies = []
    started_at = now()
    started = time.monotonic()
    while time.monotonic() - started < duration:
        before = time.perf_counter_ns()
        checked_inference(runtime, sample, batch, "measurement")
        latencies.append((time.perf_counter_ns() - before) / 1_000_000)
    ended_at = now()
    runtime.release()
    mean_ms = float(np.mean(latencies))
    result = {
        "status": "Succeeded",
        "schemaVersion": "chill.dacs.io/batch-characterization.v1alpha1",
        "nodeName": os.environ.get("NODE_NAME", "unknown"),
        "model": "mobilenet-v2-100",
        "artifactDigest": actual,
        "runtime": "rknnlite",
        "runtimeVersion": "2.3.2",
        "provider": "RKNPUExecutionProvider",
        "runtimeContract": {
            "targetPlatform": "rk3588",
            "coreMask": "NPU_CORE_0",
            "sourceModelLayout": "NCHW",
            "runtimeInputLayout": "NHWC",
            "outputShape": [batch, 1000],
        },
        "artifactCapability": {
            "shapeMode": shape_mode,
            "supportedBatches": supported_batches,
        },
        "batchSize": batch,
        "inferenceCount": len(latencies),
        "measurementStartedAt": started_at,
        "measurementEndedAt": ended_at,
        "latencyMs": {
            "mean": round(mean_ms, 3),
            "p50": round(float(np.percentile(latencies, 50)), 3),
            "p95": round(float(np.percentile(latencies, 95)), 3),
            "p99": round(float(np.percentile(latencies, 99)), 3),
        },
        "throughputItemsPerSecond": round(batch * 1000 / mean_ms, 3),
        "energyPerRequest": None,
        "bSat": None,
        # TODO(internal): energyPerRequest and bSat are derived/persisted by the
        # profiler controller after power-window validation and repetitions.
    }
    print("EXPERIMENT_RESULT_JSON " + json.dumps(result, sort_keys=True, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    main()
