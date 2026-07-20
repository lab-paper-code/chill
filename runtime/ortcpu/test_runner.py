from __future__ import annotations

import hashlib
import io
import json
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest import mock

import runner


class FakeArray:
    shape = (1, 1000)
    size = 1000


class FakeInput:
    name = "input"
    type = "tensor(float)"
    shape = [1, 3, 224, 224]


class FakeSample:
    nbytes = 1 * 3 * 224 * 224 * 4

    def __init__(self) -> None:
        self.flat = FakeFlat(self.nbytes // 4)

    def reshape(self, dimension: int) -> "FakeFlat":
        if dimension != -1:
            raise AssertionError(f"unexpected reshape dimension {dimension}")
        return self.flat


class FakeFlat:
    def __init__(self, length: int) -> None:
        self.length = length

    def __len__(self) -> int:
        return self.length

    def __setitem__(self, key: slice, values: "FakeChunk") -> None:
        if key.start is None or key.stop is None or key.step is not None:
            raise AssertionError(f"unexpected input slice {key}")
        if values.length != key.stop - key.start:
            raise AssertionError("generated chunk length does not match its destination")


class FakeChunk:
    def __init__(self, length: int) -> None:
        self.length = length


class FakeGenerator:
    def __init__(self) -> None:
        self.calls: list[int] = []

    def standard_normal(self, length: int) -> FakeChunk:
        self.calls.append(length)
        return FakeChunk(length)


class FakeNumpy:
    float32 = object()
    last_generator: FakeGenerator | None = None

    @staticmethod
    def empty(shape: list[int], *, dtype: object) -> FakeSample:
        if shape != [1, 3, 224, 224] or dtype is not FakeNumpy.float32:
            raise AssertionError(f"unexpected input allocation shape={shape} dtype={dtype}")
        return FakeSample()

    class random:
        @staticmethod
        def default_rng(seed: int) -> FakeGenerator:
            if seed != 0:
                raise AssertionError(f"unexpected seed {seed}")
            generator = FakeGenerator()
            FakeNumpy.last_generator = generator
            return generator


class FakeSession:
    def __init__(self) -> None:
        self.calls = 0

    def get_providers(self) -> list[str]:
        return ["CPUExecutionProvider"]

    def get_inputs(self) -> list[FakeInput]:
        return [FakeInput()]

    def run(self, _outputs: object, feeds: dict[str, FakeSample]) -> list[FakeArray]:
        if set(feeds) != {"input"}:
            raise AssertionError(f"unexpected feeds {feeds}")
        self.calls += 1
        return [FakeArray()]


class FakeSessionOptions:
    intra_op_num_threads = 0
    inter_op_num_threads = 0


class FakeORT:
    __version__ = "test-ort"
    last_session: FakeSession | None = None

    @staticmethod
    def get_available_providers() -> list[str]:
        return ["CPUExecutionProvider"]

    @staticmethod
    def SessionOptions() -> FakeSessionOptions:
        return FakeSessionOptions()

    @classmethod
    def InferenceSession(cls, _path: str, *, sess_options: FakeSessionOptions, providers: list[str]) -> FakeSession:
        if sess_options.intra_op_num_threads != 4 or sess_options.inter_op_num_threads != 1:
            raise AssertionError("ORT thread options were not frozen")
        if providers != ["CPUExecutionProvider"]:
            raise AssertionError(f"unexpected providers {providers}")
        cls.last_session = FakeSession()
        return cls.last_session


def valid_request(model_path: Path) -> dict[str, object]:
    artifact_digest = "sha256:" + hashlib.sha256(model_path.read_bytes()).hexdigest()
    return {
        "schemaVersion": runner.REQUEST_SCHEMA,
        "intentDigest": "sha256:" + "1" * 64,
        "podUID": "pod-uid",
        "scheduledNodeName": "lattepanda",
        "modelPath": str(model_path),
        "observerReadyPath": str(model_path.parent / "observer-ready"),
        "measurementStartPath": str(model_path.parent / "measurement-start"),
        "observerReadyTimeoutMilliseconds": 30_000,
        "maxLatencySamples": 100_000,
        "maxInputBytes": 1 << 20,
        "maxResultBytes": 8 << 20,
        "intent": {
            "schemaVersion": runner.INTENT_SCHEMA,
            "candidate": {},
            "state": {
                "deviceClass": "lattepanda-3-delta-8g",
                "model": "mobilenet-v2-050",
                "artifact": "canonical-onnx",
                "artifactDigest": artifact_digest,
                "artifactPath": "/artifact/mobilenet-v2-050.onnx",
                "runtimeFamily": "onnxruntime",
                "backend": "CPUExecutionProvider",
                "powerMode": "fixed",
            },
            "images": {},
            "targetNode": {
                "name": "lattepanda",
                "uid": "node-uid",
                "resourceVersion": "10",
                "architecture": "amd64",
                "allocatableCPU": "4",
            },
            "cpu": {
                "policy": "AllocatableCPUQuota",
                "policyVersion": "v1",
                "request": "100m",
                "limit": "4",
                "ortIntraOpThreads": 4,
                "ortInterOpThreads": 1,
                "exclusivityClaimed": False,
            },
            "measurement": {
                "batch": 1,
                "warmupIterations": 2,
                "durationSeconds": 1,
                "repetitions": 1,
                "workloadMode": runner.SUPPORTED_WORKLOAD,
                "inputGenerator": runner.SUPPORTED_INPUT_GENERATOR,
                "outputValidation": runner.SUPPORTED_OUTPUT_VALIDATION,
            },
            "metrics": {},
        },
    }


class RunnerTest(unittest.TestCase):
    def test_decode_request_rejects_duplicate_field(self) -> None:
        with self.assertRaisesRegex(runner.ContractError, "duplicate JSON field"):
            runner.decode_request(io.BytesIO(b'{"schemaVersion":"a","schemaVersion":"b"}'))

    def test_validate_request_rejects_unknown_contract_field(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["unknown"] = True
            with self.assertRaisesRegex(runner.ContractError, "unknown=\\['unknown'\\]"):
                runner.validate_request(request)

    def test_validate_request_requires_job_timing_binding(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            del request["observerReadyTimeoutMilliseconds"]
            with self.assertRaisesRegex(runner.ContractError, "observerReadyTimeoutMilliseconds"):
                runner.validate_request(request)

    def test_validate_request_rejects_scheduled_node_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["scheduledNodeName"] = "other-node"
            with self.assertRaisesRegex(runner.ContractError, "scheduled Node"):
                runner.validate_request(request)

    def test_validate_request_requires_job_resource_budgets(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            for field in ("maxLatencySamples", "maxInputBytes", "maxResultBytes"):
                with self.subTest(field=field):
                    request = valid_request(model)
                    del request[field]
                    with self.assertRaisesRegex(runner.ContractError, field):
                        runner.validate_request(request)

    def test_producer_budgets_fail_before_growth(self) -> None:
        with self.assertRaisesRegex(runner.ContractError, "input tensor"):
            runner.admitted_input_bytes([8, 3, 224, 224], 1024)
        with self.assertRaisesRegex(runner.ContractError, "latency sample budget"):
            runner.measurement_complete(0.5, 1, 10, 10)
        self.assertTrue(runner.measurement_complete(1.0, 1, 10, 10))
        with self.assertRaisesRegex(runner.ContractError, "runtime result exceeds"):
            runner.encode_result({"samples": [1, 2, 3]}, 1)

    def test_publish_immutable_refuses_replacement(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            result = Path(directory) / "result.json"
            runner.publish_immutable(result, b"first")
            self.assertEqual(result.read_bytes(), b"first")
            with self.assertRaisesRegex(runner.ContractError, "already exists"):
                runner.publish_immutable(result, b"second")
            self.assertEqual(result.read_bytes(), b"first")

    def test_execute_produces_bound_runtime_result(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            with ExitStack() as stack:
                stack.enter_context(mock.patch.object(runner.platform, "machine", return_value="x86_64"))
                stack.enter_context(mock.patch.object(
                    runner, "validate_cpu_contract", return_value=("400000 100000", [0, 1, 2, 3], "0-3")
                ))
                stack.enter_context(mock.patch.object(runner, "wait_for_signal"))
                publish = stack.enter_context(mock.patch.object(runner, "publish_immutable"))
                stack.enter_context(mock.patch.object(
                    runner, "cpu_stat", side_effect=[{"usage_usec": 10}, {"usage_usec": 30}]
                ))
                stack.enter_context(mock.patch.object(runner, "now", side_effect=[
                    "2026-07-16T00:00:00.000000Z",
                    "2026-07-16T00:00:00.100000Z",
                    "2026-07-16T00:00:01.200000Z",
                    "2026-07-16T00:00:01.300000Z",
                    "2026-07-16T00:00:01.400000Z",
                ]))
                stack.enter_context(mock.patch.object(runner.time, "monotonic", side_effect=[0.0, 1.1, 1.1]))
                stack.enter_context(mock.patch.object(runner.time, "perf_counter_ns", side_effect=[0, 5_000_000]))
                stack.enter_context(mock.patch.object(runner.time, "process_time", side_effect=[2.0, 2.8]))
                result = runner.execute(request, FakeORT, FakeNumpy)

            publish.assert_called_once_with(Path(request["measurementStartPath"]), b"signal\n")
            self.assertEqual(FakeORT.last_session.calls, 3)
            self.assertEqual(result["status"], "Succeeded")
            self.assertEqual(result["sweepId"], request["intentDigest"])
            self.assertEqual(result["experimentId"], "pod-uid")
            self.assertEqual(result["nodeName"], request["scheduledNodeName"])
            self.assertEqual(result["provider"], "CPUExecutionProvider")
            self.assertEqual(result["inferenceCount"], 1)
            self.assertEqual(result["latencyMs"]["mean"], 5.0)
            self.assertEqual(result["cpuContract"]["cgroupCPUStatDelta"], {"usage_usec": 20})
            self.assertEqual(result["derivedExecutionContract"]["nodeSnapshot"]["uid"], "node-uid")
            self.assertEqual(result["outputValidation"]["outputShapes"], [[1, 1000]])
            generator = FakeNumpy.last_generator
            if generator is None:
                self.fail("input generator was not created")
            self.assertEqual(sum(generator.calls), 1 * 3 * 224 * 224)
            self.assertLessEqual(
                max(generator.calls), runner.INPUT_GENERATION_CHUNK_ELEMENTS
            )


if __name__ == "__main__":
    unittest.main()
