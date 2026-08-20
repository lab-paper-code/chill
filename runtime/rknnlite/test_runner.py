from __future__ import annotations

import io
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest import mock

import runner


class FakeArray:
    shape = (1, 1000)
    size = 1000


class FakeSample:
    nbytes = 1 * 224 * 224 * 3 * 4

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
            raise AssertionError(
                "generated chunk length does not match its destination"
            )


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
        if shape != [1, 224, 224, 3] or dtype is not FakeNumpy.float32:
            raise AssertionError(
                f"unexpected input allocation shape={shape} dtype={dtype}"
            )
        return FakeSample()

    class random:
        @staticmethod
        def default_rng(seed: int) -> FakeGenerator:
            if seed != 0:
                raise AssertionError(f"unexpected seed {seed}")
            generator = FakeGenerator()
            FakeNumpy.last_generator = generator
            return generator


class FakeRKNNLite:
    NPU_CORE_0 = 1
    last_instance: "FakeRKNNLite | None" = None

    def __init__(self) -> None:
        self.inference_calls = 0
        self.released = False
        FakeRKNNLite.last_instance = self

    def load_rknn(self, path: str) -> int:
        if not path.endswith("model.rknn"):
            raise AssertionError(f"unexpected model path {path}")
        return 0

    def init_runtime(self, *, core_mask: int) -> int:
        if core_mask != self.NPU_CORE_0:
            raise AssertionError(f"unexpected core mask {core_mask}")
        return 0

    def inference(self, *, inputs: list[FakeSample]) -> list[FakeArray]:
        if len(inputs) != 1 or not isinstance(inputs[0], FakeSample):
            raise AssertionError("unexpected inference inputs")
        self.inference_calls += 1
        return [FakeArray()]

    def release(self) -> None:
        self.released = True


def valid_host_evidence() -> dict[str, object]:
    return {
        "package": {"name": "rknn-toolkit-lite2", "version": "2.3.2"},
        "hostLibraries": [
            {
                "name": "librknnrt",
                "path": "/usr/lib/librknnrt.so",
            }
        ],
        "driver": {
            "name": "rknpu",
            "versionPath": "/sys/module/rknpu/version",
            "version": "0.9.6",
        },
        "platform": {
            "compatiblePath": "/proc/device-tree/compatible",
            "compatibleToken": "rockchip,rk3588",
            "matched": True,
        },
        "devices": [
            {"path": "/dev/dri/card0", "characterDevice": True, "opened": True},
            {"path": "/dev/dri/renderD128", "characterDevice": True, "opened": True},
        ],
        "coreMask": runner.CORE_MASK_NAME,
        "inputLayout": runner.SUPPORTED_INPUT_LAYOUT,
        "initSucceeded": False,
        "loadSucceeded": False,
        "selectedBatch": 1,
    }


def valid_request(model_path: Path) -> dict[str, object]:
    host_contract = {
        "libraries": [
            {
                "name": "librknnrt",
                "hostPath": "/usr/lib/librknnrt.so",
                "containerPath": "/usr/lib/librknnrt.so",
            }
        ],
        "driver": {
            "name": "rknpu",
            "versionPath": "/sys/module/rknpu/version",
            "version": "0.9.6",
        },
        "devices": [{"path": "/dev/dri/card0"}, {"path": "/dev/dri/renderD128"}],
        "platform": {
            "compatiblePath": "/proc/device-tree/compatible",
            "compatibleToken": "rockchip,rk3588",
        },
        "allocation": {"resourceName": "", "exclusivityClaimed": False},
    }
    candidate_inputs = {
        "modelSpec": {
            "name": "mobilenet-v2-050",
            "uid": "model-uid",
            "resourceVersion": "10",
        },
        "deviceClass": {
            "name": "orangepi-5-plus-16g",
            "uid": "device-uid",
            "resourceVersion": "20",
        },
        "runtimeDeclaration": {
            "namespace": "chill-system",
            "name": "chill-profiler-facts",
            "uid": "facts-uid",
            "resourceVersion": "30",
            "key": "runtime-declarations.yaml",
            "image": "registry.local/chill/rknnlite-worker:current",
        },
    }
    return {
        "schemaVersion": runner.REQUEST_SCHEMA,
        "podUID": "pod-uid",
        "scheduledNodeName": "orangepi",
        "modelPath": str(model_path),
        "candidate": {
            "artifactCapability": {
                "targetPlatform": "rk3588",
                "declaredBatchSizes": [1, 2, 4],
                "inputSampleShape": [224, 224, 3],
                "inputLayout": "NHWC",
                "inputDataType": "float32",
            },
            "runtimePackages": [{"name": "rknn-toolkit-lite2", "version": "2.3.2"}],
            "hostContract": host_contract,
            "runtimeAccess": {"privileged": True, "supplementalGroups": [44, 110]},
        },
        "observerReadyPaths": [
            str(model_path.parent / "observer-ready"),
            str(model_path.parent / "workload-observer-ready"),
        ],
        "workloadWindowStartPath": str(model_path.parent / "workload-window-start"),
        "measurementStartPath": str(model_path.parent / "measurement-start"),
        "workloadWindowReadyPath": str(model_path.parent / "workload-window-ready"),
        "measurementEndPath": str(model_path.parent / "measurement-end"),
        "observerReadyTimeoutMilliseconds": 30_000,
        "maxLatencySamples": 100_000,
        "maxInputBytes": 1 << 20,
        "maxResultBytes": 8 << 20,
        "intent": {
            "schemaVersion": runner.INTENT_SCHEMA,
            "candidate": {
                "inputs": candidate_inputs,
                "executionPath": "rknnlite",
                "verdict": "Compatible",
            },
            "state": {
                "deviceClass": "orangepi-5-plus-16g",
                "model": "mobilenet-v2-050",
                "artifact": "mobilenet-v2-050-rk3588-bs1",
                "artifactPath": "/artifact/model.rknn",
                "runtimeFamily": "rknnlite",
                "backend": "not-applicable",
                "powerMode": "fixed",
                "hostContract": host_contract,
            },
            "images": {},
            "targetNode": {
                "name": "orangepi",
                "uid": "node-uid",
                "resourceVersion": "10",
                "architecture": "arm64",
                "allocatableCPU": "4",
            },
            "cpu": {
                "policy": "AllocatableCPUQuota",
                "policyVersion": "v1",
                "request": "100m",
                "limit": "4",
                "exclusivityClaimed": False,
            },
            "measurement": {
                "batch": 1,
                "warmupIterations": 2,
                "durationSeconds": 1,
                "workloadMode": runner.SUPPORTED_WORKLOAD,
                "inputGenerator": runner.SUPPORTED_INPUT_GENERATOR,
                "outputValidation": runner.SUPPORTED_OUTPUT_VALIDATION,
            },
            "metrics": {},
            "workload": {},
        },
    }


def valid_idle_request(model_path: Path) -> dict[str, object]:
    request = valid_request(model_path)
    request["intent"]["measurement"] = {
        "batch": 1,
        "warmupIterations": 0,
        "settlingDurationSeconds": 60,
        "durationSeconds": 30,
        "workloadMode": runner.IDLE_WORKLOAD,
        "inputGenerator": runner.NO_INPUT_GENERATOR,
        "outputValidation": runner.NO_OUTPUT_VALIDATION,
    }
    return request


class RunnerTest(unittest.TestCase):
    def test_current_cgroup_v2_directory_resolves_host_visible_membership(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "cgroup"
            membership = Path(directory) / "self-cgroup"
            membership.write_text(
                "0::/kubepods.slice/kubepods-burstable.slice/pod.scope\n",
                encoding="utf-8",
            )
            self.assertEqual(
                runner.current_cgroup_v2_directory(membership, root),
                root / "kubepods.slice/kubepods-burstable.slice/pod.scope",
            )
            membership.write_text("0::/\n", encoding="utf-8")
            self.assertEqual(runner.current_cgroup_v2_directory(membership, root), root)

    def test_current_cgroup_v2_directory_rejects_ambiguous_membership(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            membership = Path(directory) / "self-cgroup"
            membership.write_text("2:cpu:/pod\n", encoding="utf-8")
            with self.assertRaisesRegex(runner.ContractError, "exactly one"):
                runner.current_cgroup_v2_directory(
                    membership, Path(directory) / "cgroup"
                )

    def test_decode_request_rejects_duplicate_field(self) -> None:
        with self.assertRaisesRegex(runner.ContractError, "duplicate JSON field"):
            runner.decode_request(
                io.BytesIO(b'{"schemaVersion":"a","schemaVersion":"b"}')
            )

    def test_validate_request_accepts_current_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            runner.validate_request(valid_request(model))

    def test_validate_request_binds_current_actuation_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["intent"]["state"]["powerMode"] = "MODE_15W_6CORE"
            request["intent"]["actuation"] = {
                "powerMode": "MODE_15W_6CORE",
                "actuator": {"kind": "JetsonNVPModel", "jetsonNVPModel": {"id": 2}},
                "policyName": "jetson-mode-15w",
            }
            runner.validate_request(request)

            request["intent"]["actuation"]["policyName"] = ""
            with self.assertRaisesRegex(runner.ContractError, "policyName"):
                runner.validate_request(request)

    def test_validate_request_rejects_null_actuation_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["intent"]["state"]["powerMode"] = "MODE_15W_6CORE"
            request["intent"]["actuation"] = None
            with self.assertRaisesRegex(
                runner.ContractError, "actuation must be an object"
            ):
                runner.validate_request(request)

    def test_validate_request_rejects_unsupported_schemas(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["intent"]["schemaVersion"] = "chill.dacs.io/profiling-run-intent.v1alpha11"
            with self.assertRaisesRegex(runner.ContractError, "current intent schema"):
                runner.validate_request(request)

            request = valid_request(model)
            request["schemaVersion"] = "chill.dacs.io/rknnlite-runtime-request.v1alpha5"
            with self.assertRaisesRegex(
                runner.ContractError, "unsupported runtime request"
            ):
                runner.validate_request(request)

    def test_validate_outputs_names_a_refused_profile(self):
        with self.assertRaisesRegex(
            runner.ContractError, "does not provide this declared batch size"
        ):
            runner.validate_outputs(None, 1)

    def test_validate_request_requires_candidate_declared_batch_sizes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["candidate"]["artifactCapability"]["declaredBatchSizes"] = []
            with self.assertRaisesRegex(
                runner.ContractError,
                "^candidate declaredBatchSizes must be a non-empty array$",
            ):
                runner.validate_request(request)

    def test_validate_request_requires_positive_candidate_declared_batch_sizes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["candidate"]["artifactCapability"]["declaredBatchSizes"] = [1, 0, 4]
            with self.assertRaisesRegex(
                runner.ContractError,
                "^candidate declaredBatchSizes item must be an integer >= 1$",
            ):
                runner.validate_request(request)

    def test_validate_request_rejects_candidate_tensor_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            runner.validate_request(request)
            request["candidate"]["artifactCapability"]["inputLayout"] = "NCHW"
            with self.assertRaisesRegex(runner.ContractError, "artifact capability"):
                runner.validate_request(request)

    def test_validate_request_keeps_active_and_idle_fields_disjoint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            active = valid_request(model)
            active["intent"]["measurement"]["settlingDurationSeconds"] = 30
            with self.assertRaisesRegex(
                runner.ContractError, "settlingDurationSeconds"
            ):
                runner.validate_request(active)
            idle = valid_idle_request(model)
            runner.validate_request(idle)

    def test_observe_host_contract_rejects_runtime_package_version_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            candidate = valid_request(model)["candidate"]
            with mock.patch.object(
                runner.importlib.metadata, "version", return_value="2.3.1"
            ):
                with self.assertRaisesRegex(runner.ContractError, "package version"):
                    runner.observe_host_contract(candidate, 1)

    def test_execute_active_retains_rknn_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_request(model)
            with ExitStack() as stack:
                stack.enter_context(
                    mock.patch.object(
                        runner.platform, "machine", return_value="aarch64"
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner,
                        "validate_cpu_contract",
                        return_value=("400000 100000", [0, 1, 2, 3], "0-3"),
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner,
                        "observe_host_contract",
                        return_value=valid_host_evidence(),
                    )
                )
                wait = stack.enter_context(mock.patch.object(runner, "wait_for_signal"))
                publish = stack.enter_context(
                    mock.patch.object(runner, "publish_immutable")
                )
                stack.enter_context(
                    mock.patch.object(
                        runner,
                        "cpu_stat",
                        side_effect=[{"usage_usec": 10}, {"usage_usec": 30}],
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner.time, "process_time", side_effect=[1.0, 1.5]
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner.time, "perf_counter_ns", side_effect=[0, 5_000_000]
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner.time, "monotonic", side_effect=[0.0, 1.0, 1.0]
                    )
                )
                result = runner.execute(request, FakeRKNNLite, FakeNumpy)

            instance = FakeRKNNLite.last_instance
            self.assertIsNotNone(instance)
            self.assertEqual(instance.inference_calls, 3)
            self.assertTrue(instance.released)
            self.assertEqual(result["runtime"], "rknnlite")
            self.assertEqual(result["provider"] if "provider" in result else "", "")
            self.assertEqual(result["rknn"]["coreMask"], "NPU_CORE_0")
            self.assertEqual(result["inputShape"], [1, 224, 224, 3])
            self.assertEqual(result["outputValidation"]["outputShapes"], [[1, 1000]])
            self.assertEqual(
                publish.call_args_list,
                [
                    mock.call(Path(request["workloadWindowStartPath"]), b"signal\n"),
                    mock.call(Path(request["measurementStartPath"]), b"signal\n"),
                    mock.call(Path(request["measurementEndPath"]), b"signal\n"),
                ],
            )
            self.assertEqual(len(wait.call_args_list), 3)

    def test_execute_idle_loads_and_initializes_without_inference(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.rknn"
            model.write_bytes(b"model")
            request = valid_idle_request(model)
            with ExitStack() as stack:
                stack.enter_context(
                    mock.patch.object(
                        runner.platform, "machine", return_value="aarch64"
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner,
                        "validate_cpu_contract",
                        return_value=("400000 100000", [0, 1, 2, 3], "0-3"),
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner,
                        "observe_host_contract",
                        return_value=valid_host_evidence(),
                    )
                )
                stack.enter_context(mock.patch.object(runner, "wait_for_signal"))
                stack.enter_context(mock.patch.object(runner, "publish_immutable"))
                stack.enter_context(mock.patch.object(runner.time, "sleep"))
                stack.enter_context(
                    mock.patch.object(
                        runner,
                        "cpu_stat",
                        side_effect=[{"usage_usec": 10}, {"usage_usec": 15}],
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner.time, "process_time", side_effect=[1.0, 1.1]
                    )
                )
                stack.enter_context(
                    mock.patch.object(
                        runner.time,
                        "monotonic",
                        side_effect=[0.0, 60.0, 100.0, 130.0, 130.0],
                    )
                )
                result = runner.execute(request, FakeRKNNLite, FakeNumpy)

            instance = FakeRKNNLite.last_instance
            self.assertIsNotNone(instance)
            self.assertEqual(instance.inference_calls, 0)
            self.assertTrue(instance.released)
            self.assertEqual(result["schemaVersion"], runner.IDLE_RESULT_SCHEMA)
            self.assertEqual(result["inferenceCountAfterModelLoad"], 0)
            self.assertTrue(result["modelResidentAtEnd"])


if __name__ == "__main__":
    unittest.main()
