#!/usr/bin/env python3
"""Load one ONNX artifact, require one EP, and emit structured evidence."""

from __future__ import annotations

import hashlib
import json
import os
import platform
import time
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
import onnxruntime as ort


def log(level: str, stage: str, message: str, **fields: object) -> None:
    timestamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    suffix = " ".join(f"{key}={json.dumps(value, separators=(',', ':'))}" for key, value in fields.items())
    print(f"{timestamp} {level:<5} [{stage:<10}] {message}" + (f" | {suffix}" if suffix else ""), flush=True)


def result(payload: dict[str, object]) -> None:
    print("", flush=True)
    print("--- CHILL MODEL RUNTIME RESULT --------------------------------", flush=True)
    for key in (
        "status", "model", "artifactDigest", "architecture", "runtime",
        "runtimeVersion", "requestedProvider", "actualProvider", "batchSize",
        "inputShape", "modelLoadMs", "inferenceCount", "latencyMeanMs",
        "latencyP95Ms",
    ):
        if key in payload:
            print(f"{key:<20}: {payload[key]}", flush=True)
    print("----------------------------------------------------------------", flush=True)
    print("RESULT_JSON " + json.dumps(payload, sort_keys=True, separators=(",", ":")), flush=True)


def fail(stage: str, reason: str, **details: object) -> None:
    log("ERROR", stage, reason, **details)
    result({"status": "Failed", "stage": stage, "reason": reason, **details})
    raise SystemExit(1)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def main() -> None:
    model_path = Path(os.environ["MODEL_PATH"])
    model_name = os.environ["MODEL_NAME"]
    expected_digest = os.environ["ARTIFACT_DIGEST"]
    requested_provider = os.environ["RUNTIME_PROVIDER"]
    batch_size = int(os.environ.get("BATCH_SIZE", "1"))
    warmup = int(os.environ.get("WARMUP", "3"))
    iterations = int(os.environ.get("ITERATIONS", "10"))

    print("=== CHILL Model Runtime Spike ================================", flush=True)
    print(f"model               : {model_name}", flush=True)
    print(f"model path          : {model_path}", flush=True)
    print(f"expected digest     : {expected_digest}", flush=True)
    print(f"requested provider  : {requested_provider}", flush=True)
    print(f"batch / warmup / run: {batch_size} / {warmup} / {iterations}", flush=True)
    print("================================================================", flush=True)

    log("INFO", "artifact", "verifying model artifact")
    if not model_path.is_file():
        fail("artifact", "ArtifactMissing", path=str(model_path))
    actual_digest = sha256(model_path)
    if actual_digest != expected_digest:
        fail("artifact", "ArtifactDigestMismatch", expected=expected_digest, actual=actual_digest)
    log("INFO", "artifact", "digest verified", digest=actual_digest, bytes=model_path.stat().st_size)

    log("INFO", "runtime", "checking requested execution provider")
    available = ort.get_available_providers()
    if requested_provider not in available:
        fail("runtime", "RuntimeProviderUnavailable", requested=requested_provider, available=available)
    log("INFO", "runtime", "provider available", runtimeVersion=ort.__version__, available=available)

    log("INFO", "load", "creating inference session")
    started = time.perf_counter()
    try:
        session = ort.InferenceSession(str(model_path), providers=[requested_provider])
    except Exception as error:
        fail("load", "ModelLoadFailed", error=repr(error))
    load_ms = (time.perf_counter() - started) * 1000
    actual_provider = session.get_providers()[0]
    if actual_provider != requested_provider:
        fail("load", "RuntimeProviderMismatch", requested=requested_provider, actual=actual_provider)
    log("INFO", "load", "model loaded", provider=actual_provider, elapsedMs=round(load_ms, 3))

    input_meta = session.get_inputs()[0]
    shape = [batch_size] + [dimension if isinstance(dimension, int) else 1 for dimension in input_meta.shape[1:]]
    sample = np.random.default_rng(0).standard_normal(shape).astype(np.float32)

    try:
        log("INFO", "warmup", "starting warm-up", iterations=warmup, inputShape=shape)
        for _ in range(warmup):
            session.run(None, {input_meta.name: sample})
        log("INFO", "warmup", "warm-up completed", iterations=warmup)
        log("INFO", "inference", "starting measured inference loop", iterations=iterations)
        latencies_ms: list[float] = []
        for _ in range(iterations):
            before = time.perf_counter()
            session.run(None, {input_meta.name: sample})
            latencies_ms.append((time.perf_counter() - before) * 1000)
    except Exception as error:
        fail("inference", "InferenceFailed", error=repr(error))

    latency_mean = round(float(np.mean(latencies_ms)), 3)
    latency_p95 = round(float(np.percentile(latencies_ms, 95)), 3)
    log("INFO", "inference", "inference completed", count=iterations, meanMs=latency_mean, p95Ms=latency_p95)

    result({
        "status": "Succeeded",
        "model": model_name,
        "artifactDigest": actual_digest,
        "architecture": platform.machine(),
        "runtime": "onnxruntime",
        "runtimeVersion": ort.__version__,
        "requestedProvider": requested_provider,
        "actualProvider": actual_provider,
        "availableProviders": available,
        "batchSize": batch_size,
        "inputShape": shape,
        "warmupCompleted": True,
        "inferenceCount": iterations,
        "modelLoadMs": round(load_ms, 3),
        "latencyMeanMs": latency_mean,
        "latencyP95Ms": latency_p95,
    })


if __name__ == "__main__":
    main()
