#!/usr/bin/env python3
"""Measure fixed-batch inference latency and emit timestamped raw evidence."""

# TODO(internal): Replace environment-variable assembly with a versioned
# execution-contract object mounted by the profiler controller. The internal
# reconciler must persist raw evidence, join power by timestamps, and update
# DeviceProfile status; this spike intentionally only emits log records.

from __future__ import annotations

import hashlib
import json
import os
import platform
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
import onnxruntime as ort


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def log(level: str, stage: str, message: str, **fields: object) -> None:
    suffix = " ".join(f"{key}={json.dumps(value, separators=(',', ':'))}" for key, value in fields.items())
    print(f"{now()} {level:<5} [{stage:<10}] {message}" + (f" | {suffix}" if suffix else ""), flush=True)


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return "sha256:" + value.hexdigest()


def fail(stage: str, reason: str, **details: object) -> None:
    payload = {"status": "Failed", "stage": stage, "reason": reason, **details}
    log("ERROR", stage, reason, **details)
    print("EXPERIMENT_RESULT_JSON " + json.dumps(payload, sort_keys=True, separators=(",", ":")), flush=True)
    raise SystemExit(1)


def percentile(samples: list[float], value: float) -> float:
    return round(float(np.percentile(samples, value)), 3)


def human_result(payload: dict[str, object]) -> None:
    latency = payload["latencyMs"]
    cpu = payload["cpuContract"]
    stat = cpu["cgroupCPUStatDelta"]
    print("", flush=True)
    print("--- CHILL BATCH CHARACTERIZATION RESULT ----------------------", flush=True)
    print(f"status                  : {payload['status']}", flush=True)
    print(f"node                    : {payload['nodeName']}", flush=True)
    print(f"batch size              : {payload['batchSize']}", flush=True)
    print(f"completed inferences    : {payload['inferenceCount']}", flush=True)
    print(f"latency mean            : {latency['mean']} ms", flush=True)
    print(f"latency p95             : {latency['p95']} ms", flush=True)
    print(f"latency p99             : {latency['p99']} ms", flush=True)
    print(f"throughput              : {payload['throughputItemsPerSecond']} items/s", flush=True)
    print(f"cgroup CPU max          : {cpu['cgroupCPUMax']}", flush=True)
    print(f"ORT intra / inter       : {cpu['ortIntraOpThreads']} / {cpu['ortInterOpThreads']}", flush=True)
    print(f"CPU used                : {stat.get('usage_usec', 0) / 1_000_000:.3f} CPU-s", flush=True)
    print(f"CPU throttled           : {stat.get('throttled_usec', 0) / 1_000_000:.3f} CPU-s", flush=True)
    print("power result            : see power-observer container", flush=True)
    print("---------------------------------------------------------------", flush=True)


def human_contract(contract: dict[str, object]) -> None:
    cpu = contract["cpu"]
    runtime = contract["runtimeOptions"]
    scheduling = contract["scheduling"]
    print("", flush=True)
    print("--- CHILL DERIVED EXECUTION CONTRACT -------------------------", flush=True)
    print(f"policy                  : {cpu['policy']}", flush=True)
    print(f"budget source           : {cpu['budgetSource']}", flush=True)
    print(f"observed allocatable    : {cpu['observedAllocatable']} CPU", flush=True)
    print(f"derived CPU limit       : {cpu['limit']} CPU", flush=True)
    print(f"ORT intra / inter       : {runtime['intraOpThreads']} / {runtime['interOpThreads']}", flush=True)
    print(f"scheduling request      : {scheduling['cpuRequest']}", flush=True)
    print(f"scheduling status       : {scheduling['status']}", flush=True)
    print("---------------------------------------------------------------", flush=True)


def read_text(path: str) -> str | None:
    try:
        return Path(path).read_text().strip()
    except OSError:
        return None


def cpu_stat() -> dict[str, int]:
    values: dict[str, int] = {}
    raw = read_text("/sys/fs/cgroup/cpu.stat")
    if not raw:
        return values
    for line in raw.splitlines():
        key, value = line.split(maxsplit=1)
        values[key] = int(value)
    return values


def stat_delta(before: dict[str, int], after: dict[str, int]) -> dict[str, int]:
    return {key: after[key] - before.get(key, 0) for key in after}


def cgroup_cpu_limit_cores() -> float | None:
    raw = read_text("/sys/fs/cgroup/cpu.max")
    if not raw:
        return None
    quota, period = raw.split()
    return None if quota == "max" else int(quota) / int(period)


def cpuset_effective() -> str | None:
    return read_text("/sys/fs/cgroup/cpuset.cpus.effective") or read_text("/sys/fs/cgroup/cpuset.cpus")


def validate_outputs(outputs: list[np.ndarray], batch_size: int) -> dict[str, object]:
    if not outputs:
        fail("output", "InferenceOutputMissing", batchSize=batch_size)
    shapes = [list(output.shape) for output in outputs]
    if any(output.size == 0 for output in outputs):
        fail("output", "InferenceOutputEmpty", batchSize=batch_size, shapes=shapes)
    if any(output.ndim < 1 or output.shape[0] != batch_size for output in outputs):
        fail("output", "InferenceOutputBatchMismatch", batchSize=batch_size, shapes=shapes)
    return {"method": "nonempty-shape-batch-v1", "passed": True, "outputShapes": shapes}


def main() -> None:
    model_path = Path(os.environ["MODEL_PATH"])
    expected_digest = os.environ["ARTIFACT_DIGEST"]
    provider = os.environ["RUNTIME_PROVIDER"]
    batches = [int(value) for value in os.environ["BATCH_SIZES"].split(",")]
    index = int(os.environ["JOB_COMPLETION_INDEX"])
    warmup = int(os.environ["WARMUP_ITERATIONS"])
    iterations = int(os.environ["MEASUREMENT_ITERATIONS"])
    repetitions = int(os.environ["REPETITIONS"])
    duration_seconds = float(os.environ.get("MEASUREMENT_DURATION_SECONDS", "0"))
    intra_op = os.environ.get("ORT_INTRA_OP")
    inter_op = os.environ.get("ORT_INTER_OP")
    observer_ready_file = os.environ.get("OBSERVER_READY_FILE")
    measurement_signal_file = os.environ.get("MEASUREMENT_SIGNAL_FILE")
    execution_contract_file = os.environ.get("EXECUTION_CONTRACT_FILE")
    derived_contract = (
        json.loads(Path(execution_contract_file).read_text())
        if execution_contract_file else None
    )
    if index < 0 or index >= len(batches):
        fail("protocol", "CompletionIndexOutOfRange", index=index, batchCount=len(batches))
    batch_size = batches[index]
    experiment_id = os.environ.get("EXPERIMENT_ID") or str(uuid.uuid4())
    sweep_id = os.environ.get("SWEEP_ID", "unknown")

    if derived_contract:
        expected_limit = float(derived_contract["cpu"]["limit"])
        expected_intra = int(derived_contract["runtimeOptions"]["intraOpThreads"])
        expected_inter = int(derived_contract["runtimeOptions"]["interOpThreads"])
        actual_limit = cgroup_cpu_limit_cores()
        mismatches = []
        if actual_limit != expected_limit:
            mismatches.append({"field": "cpuLimit", "expected": expected_limit, "actual": actual_limit})
        if intra_op is None or int(intra_op) != expected_intra:
            mismatches.append({"field": "ortIntraOpThreads", "expected": expected_intra, "actual": intra_op})
        if inter_op is None or int(inter_op) != expected_inter:
            mismatches.append({"field": "ortInterOpThreads", "expected": expected_inter, "actual": inter_op})
        if mismatches:
            fail("contract", "ExecutionContractMismatch", mismatches=mismatches)
        log("INFO", "contract", "derived execution contract verified")
        human_contract(derived_contract)

    log("INFO", "artifact", "verifying immutable model artifact", batchSize=batch_size)
    if not model_path.is_file():
        fail("artifact", "ArtifactMissing", path=str(model_path))
    actual_digest = digest(model_path)
    if actual_digest != expected_digest:
        fail("artifact", "ArtifactDigestMismatch", expected=expected_digest, actual=actual_digest)

    available_providers = ort.get_available_providers()
    if provider not in available_providers:
        fail("runtime", "RuntimeProviderUnavailable", requested=provider, available=available_providers)
    session_options = ort.SessionOptions()
    if intra_op is not None:
        session_options.intra_op_num_threads = int(intra_op)
    if inter_op is not None:
        session_options.inter_op_num_threads = int(inter_op)
    try:
        session = ort.InferenceSession(str(model_path), sess_options=session_options, providers=[provider])
    except Exception as error:
        fail("load", "ModelLoadFailed", error=repr(error))
    actual_provider = session.get_providers()[0]
    if actual_provider != provider:
        fail("load", "RuntimeProviderMismatch", requested=provider, actual=actual_provider)

    input_meta = session.get_inputs()[0]
    shape = [batch_size] + [dimension if isinstance(dimension, int) else 1 for dimension in input_meta.shape[1:]]
    sample = np.random.default_rng(0).standard_normal(shape).astype(np.float32)

    log("INFO", "warmup", "starting warm-up", iterations=warmup, inputShape=shape)
    try:
        validation_outputs = session.run(None, {input_meta.name: sample})
        output_validation = validate_outputs(validation_outputs, batch_size)
        for _ in range(max(0, warmup - 1)):
            session.run(None, {input_meta.name: sample})
    except Exception as error:
        fail("warmup", "WarmupFailed", batchSize=batch_size, error=repr(error))
    warmup_completed_at = now()

    repetition_results: list[dict[str, object]] = []
    all_samples: list[float] = []
    if observer_ready_file:
        deadline = time.monotonic() + 30
        while not Path(observer_ready_file).exists():
            if time.monotonic() >= deadline:
                fail("coordination", "PowerObserverNotReady", path=observer_ready_file)
            time.sleep(0.01)
        log("INFO", "coordination", "power observer ready")
    if measurement_signal_file:
        Path(measurement_signal_file).write_text("start\n")
        log("INFO", "coordination", "published measurement-start signal", path=measurement_signal_file)
    cpu_stat_before = cpu_stat()
    process_cpu_before = time.process_time()
    measurement_started = now()
    measurement_monotonic_started = time.monotonic()
    try:
        for repetition in range(repetitions):
            started_at = now()
            samples: list[float] = []
            repetition_started = time.monotonic()
            while True:
                started = time.perf_counter_ns()
                session.run(None, {input_meta.name: sample})
                samples.append((time.perf_counter_ns() - started) / 1_000_000)
                if duration_seconds > 0:
                    if time.monotonic() - repetition_started >= duration_seconds:
                        break
                elif len(samples) >= iterations:
                    break
            ended_at = now()
            all_samples.extend(samples)
            repetition_results.append({
                "repetition": repetition + 1,
                "measurementStartedAt": started_at,
                "measurementEndedAt": ended_at,
                "latencyMs": [round(value, 6) for value in samples],
            })
            log("INFO", "measure", "repetition completed", repetition=repetition + 1,
                inferences=len(samples), elapsedSeconds=round(time.monotonic() - repetition_started, 3),
                meanMs=round(float(np.mean(samples)), 3), p99Ms=percentile(samples, 99))
    except Exception as error:
        fail("measure", "InferenceFailed", batchSize=batch_size, error=repr(error))
    measurement_elapsed_seconds = time.monotonic() - measurement_monotonic_started
    measurement_ended = now()
    process_cpu_seconds = time.process_time() - process_cpu_before
    cpu_stat_after = cpu_stat()

    mean_ms = round(float(np.mean(all_samples)), 3)
    payload = {
        "status": "Succeeded",
        "schemaVersion": "chill.dacs.io/batch-characterization.v1alpha1",
        "sweepId": sweep_id,
        "experimentId": experiment_id,
        "completionIndex": index,
        "nodeName": os.environ.get("NODE_NAME", "unknown"),
        "model": os.environ["MODEL_NAME"],
        "artifactDigest": actual_digest,
        "architecture": platform.machine(),
        "runtime": "onnxruntime",
        "runtimeVersion": ort.__version__,
        "provider": actual_provider,
        "availableProviders": available_providers,
        "outputValidation": output_validation,
        "cpuContract": {
            "osCPUCount": os.cpu_count(),
            "affinityCPUCount": len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else None,
            "affinityCPUs": sorted(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else None,
            "cpusetCPUsEffective": cpuset_effective(),
            "cgroupCPUMax": read_text("/sys/fs/cgroup/cpu.max"),
            "ortIntraOpThreads": int(intra_op) if intra_op is not None else "default",
            "ortInterOpThreads": int(inter_op) if inter_op is not None else "default",
            "processCPUSeconds": round(process_cpu_seconds, 6),
            "cgroupCPUStatDelta": stat_delta(cpu_stat_before, cpu_stat_after),
        },
        "derivedExecutionContract": derived_contract,
        "batchSize": batch_size,
        "inputShape": shape,
        "warmupIterations": warmup,
        "warmupCompletedAt": warmup_completed_at,
        "measurementIterations": iterations,
        "inferenceCount": len(all_samples),
        "measurementMode": "duration" if duration_seconds > 0 else "iterations",
        "targetDurationSecondsPerRepetition": duration_seconds if duration_seconds > 0 else None,
        "repetitions": repetitions,
        "measurementStartedAt": measurement_started,
        "measurementEndedAt": measurement_ended,
        "measurementElapsedSecondsMonotonic": round(measurement_elapsed_seconds, 6),
        "latencyMs": {
            "mean": mean_ms,
            "p50": percentile(all_samples, 50),
            "p95": percentile(all_samples, 95),
            "p99": percentile(all_samples, 99),
        },
        "throughputItemsPerSecond": round(batch_size * 1000.0 / mean_ms, 3),
        "repetitionResults": repetition_results,
        "power": None,
        "energyPerRequest": None,
        "bSat": None,
    }
    log("INFO", "result", "batch measurement completed")
    human_result(payload)
    print("EXPERIMENT_RESULT_JSON " + json.dumps(payload, sort_keys=True, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    main()
