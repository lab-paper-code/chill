#!/usr/bin/env python3
"""TensorRT EP fixed-batch characterization runner for Jetson."""

# TODO(internal): Consume a controller-derived Jetson execution contract and a
# runtime-engine artifact/cache reference. Persist engine-build evidence and
# steady-state profiling evidence as separate lifecycle phases.

import hashlib
import json
import os
import time
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
import onnxruntime as ort


def now():
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return "sha256:" + value.hexdigest()


def checked_run(session, input_name, sample, batch, stage):
    outputs = session.run(None, {input_name: sample})
    if not outputs or tuple(outputs[0].shape) != (batch, 1000):
        actual = None if not outputs else tuple(outputs[0].shape)
        raise SystemExit(f"{stage} output mismatch: expected={(batch, 1000)} actual={actual}")


def contract(batch, precision, registered_providers):
    print("", flush=True)
    print("--- CHILL DERIVED EXECUTION CONTRACT -------------------------", flush=True)
    print("policy                  : OnePodPerNodeExclusiveGPU", flush=True)
    print("contract source         : SpikeManifestPinnedFromLiveNodeAndArtifactFacts", flush=True)
    print("device class            : jetson-xavier-nx-8g", flush=True)
    print("accelerator             : nvidia-jetson-xavier-nx", flush=True)
    print("Kubernetes resource     : nvidia.com/gpu=1", flush=True)
    print("power mode              : MODE_15W_6CORE (nvpmodel 2)", flush=True)
    print(f"runtime                 : ONNX Runtime {ort.__version__}", flush=True)
    print("primary provider        : TensorrtExecutionProvider", flush=True)
    print("requested fallback      : CUDAExecutionProvider", flush=True)
    print(f"registered providers    : {' -> '.join(registered_providers)}", flush=True)
    print("compute device          : NVIDIA Xavier NX GPU", flush=True)
    print(f"inference precision     : {precision}", flush=True)
    print("graph partition evidence: not captured", flush=True)
    print("JetPack / L4T           : R35.4.1", flush=True)
    print(f"selected batch          : {batch}", flush=True)
    print("engine cache lifecycle  : Pod-local spike cache", flush=True)
    print("---------------------------------------------------------------", flush=True)


def main():
    model = Path(os.environ["MODEL_PATH"])
    expected = os.environ["ARTIFACT_DIGEST"]
    batch = int(os.environ["BATCH_SIZE"])
    warmup = int(os.environ.get("WARMUP_ITERATIONS", "20"))
    duration = float(os.environ.get("MEASUREMENT_DURATION_SECONDS", "30"))
    precision = os.environ.get("INFERENCE_PRECISION", "fp32").lower()
    if precision != "fp32":
        raise SystemExit(f"unsupported spike precision: {precision}; expected=fp32")
    actual = digest(model)
    if actual != expected:
        raise SystemExit(f"artifact digest mismatch: expected={expected} actual={actual}")
    if "TensorrtExecutionProvider" not in ort.get_available_providers():
        raise SystemExit(f"TensorRT EP unavailable: {ort.get_available_providers()}")

    cache = Path(os.environ.get("TRT_ENGINE_CACHE_PATH", "/engine-cache"))
    cache.mkdir(parents=True, exist_ok=True)
    options = {
        "trt_engine_cache_enable": True,
        "trt_engine_cache_path": str(cache),
        "trt_fp16_enable": False,
        "trt_int8_enable": False,
    }
    build_started = time.monotonic()
    session = ort.InferenceSession(
        str(model), providers=[("TensorrtExecutionProvider", options), "CUDAExecutionProvider"]
    )
    session_build_seconds = time.monotonic() - build_started
    registered_providers = session.get_providers()
    if registered_providers[0] != "TensorrtExecutionProvider":
        raise SystemExit(f"TensorRT EP not selected: {registered_providers}")
    input_meta = session.get_inputs()[0]
    sample = np.random.default_rng(0).standard_normal((batch, 3, 380, 380)).astype(np.float32)
    contract(batch, precision, registered_providers)
    print(f"{now()} INFO  [runtime] TensorRT session ready | buildSeconds={session_build_seconds:.3f}", flush=True)
    for _ in range(warmup):
        checked_run(session, input_meta.name, sample, batch, "warm-up")

    ready = Path(os.environ["OBSERVER_READY_FILE"])
    deadline = time.monotonic() + 30
    while not ready.exists():
        if time.monotonic() >= deadline:
            raise SystemExit("power observer not ready")
        time.sleep(.01)
    Path(os.environ["MEASUREMENT_SIGNAL_FILE"]).write_text("start\n")

    samples = []
    started_at = now()
    started = time.monotonic()
    while time.monotonic() - started < duration:
        before = time.perf_counter_ns()
        checked_run(session, input_meta.name, sample, batch, "measurement")
        samples.append((time.perf_counter_ns() - before) / 1_000_000)
    ended_at = now()
    mean_ms = float(np.mean(samples))
    payload = {
        "status": "Succeeded",
        "schemaVersion": "chill.dacs.io/batch-characterization.v1alpha1",
        "nodeName": os.environ.get("NODE_NAME", "unknown"),
        "model": "efficientnet-b4",
        "artifactDigest": actual,
        "runtime": "onnxruntime",
        "runtimeVersion": ort.__version__,
        # Kept for compatibility with the existing spike analyzers.
        "provider": "TensorrtExecutionProvider",
        "execution": {
            "primaryProvider": "TensorrtExecutionProvider",
            "requestedFallbackProviders": ["CUDAExecutionProvider"],
            "registeredProviderChain": registered_providers,
            "computeDevice": "nvidia-gpu",
            "precision": precision,
            "graphPartitionEvidence": {
                "status": "NotCaptured",
                "tensorrtNodeCount": None,
                "cudaFallbackNodeCount": None,
                "cpuFallbackNodeCount": None,
            },
        },
        "runtimeContract": {
            "jetpackL4T": "R35.4.1",
            "powerMode": "MODE_15W_6CORE",
            "nvpmodelId": 2,
            "kubernetesResource": "nvidia.com/gpu",
            "gpuCount": 1,
            "engineCacheLifecycle": "pod-local-spike",
        },
        "batchSize": batch,
        "sessionBuildSeconds": round(session_build_seconds, 3),
        "inferenceCount": len(samples),
        "measurementStartedAt": started_at,
        "measurementEndedAt": ended_at,
        "latencyMs": {
            "mean": round(mean_ms, 3),
            "p50": round(float(np.percentile(samples, 50)), 3),
            "p95": round(float(np.percentile(samples, 95)), 3),
            "p99": round(float(np.percentile(samples, 99)), 3),
        },
        "throughputItemsPerSecond": round(batch * 1000 / mean_ms, 3),
        "energyPerRequest": None,
        "bSat": None,
    }
    print("EXPERIMENT_RESULT_JSON " + json.dumps(payload, sort_keys=True, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    main()
