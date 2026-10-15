from __future__ import annotations

import io
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest import mock

import runner

REQUEST_SCHEMA = "chill.dacs.io/firstfloor-runtime-request.v1alpha2"


def validate(request: dict[str, object]) -> None:
    runner.validate_request(request, REQUEST_SCHEMA, "ort-cpu")


class FakeSession:
    def __init__(self) -> None:
        self.calls: list[str] = []

    def get_providers(self) -> list[str]:
        return ["CPUExecutionProvider"]

    def prepare_next(self) -> None:
        self.calls.append("prepare")

    def run(self) -> None:
        self.calls.append("run")


def valid_request(model_path: Path) -> dict[str, object]:
    return {
        "schemaVersion": REQUEST_SCHEMA,
        "podUID": "pod-uid",
        "scheduledNodeName": "lattepanda",
        "modelDirectory": "/model",
        "preprocessorPath": "/preprocessor",
        "datasetPath": "/dataset",
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
        "runConfig": {
            "schemaVersion": runner.RUN_CONFIG_SCHEMA,
            "candidate": {},
            "state": {
                "deviceClass": "lattepanda-3-delta-8g",
                "model": "mobilenet-v2-050",
                "artifact": "canonical-onnx",
                "artifactFiles": ["/artifact/mobilenet-v2-050.onnx"],
                "runtimeFamily": "firstfloor",
                "backend": "ort-cpu",
                "powerMode": "fixed",
                "firstfloor": {"maxBatchSize": 1, "maxQueueSize": 1},
                "preprocessor": {
                    "name": "mobilenet-imagenet",
                    "uid": "pre-uid",
                    "image": "registry/preprocessors:1",
                    "architecture": "amd64",
                    "path": "/artifact/mobilenet-nchw",
                    "inputs": ["image"],
                    "outputs": ["label"],
                },
            },
            "images": {},
            "delivery": {"artifactCapability": {}},
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
                "workloadMode": runner.SUPPORTED_WORKLOAD,
                "dataset": {
                    "name": "vqav2",
                    "uid": "dataset-uid",
                    "image": "registry/datasets:1",
                    "path": "/artifact/vqav2",
                },
            },
            "metrics": {},
            "workload": {},
        },
    }


def valid_idle_request(model_path: Path) -> dict[str, object]:
    request = valid_request(model_path)
    del request["datasetPath"]
    request["runConfig"]["measurement"] = {
        "batch": 1,
        "warmupIterations": 0,
        "settlingDurationSeconds": 60,
        "durationSeconds": 30,
        "workloadMode": runner.IDLE_WORKLOAD,
    }
    return request


class RunnerTest(unittest.TestCase):
    def test_raw_sample_contract_fields_are_required_by_mode(self) -> None:
        validate(valid_idle_request(Path("/model")))
        for mutate, message in [
            (lambda r: r.pop("datasetPath"), "missing=\\['datasetPath'\\]"),
            (lambda r: r.update(modelPath="/model/model.onnx"), "unknown=\\['modelPath'\\]"),
            (lambda r: r["runConfig"]["measurement"].pop("dataset"), "missing=\\['dataset'\\]"),
            (lambda r: r["runConfig"]["measurement"].update(inputGenerator="none"), "inputGenerator"),
            (lambda r: r["runConfig"]["state"].update(artifactFiles=[]), "artifactFiles"),
            (lambda r: r["runConfig"]["state"].update(artifactFiles=["/a/m.onnx", "/b/m.onnx"]), "basenames"),
            (lambda r: r["runConfig"]["state"]["preprocessor"].update(inputs=["image", "image"]), "unique"),
            (lambda r: r["runConfig"]["state"]["preprocessor"].update(maxNewTokens=0), "maxNewTokens"),
        ]:
            with self.subTest(message=message):
                request = valid_request(Path("/model"))
                mutate(request)
                with self.assertRaisesRegex(runner.ContractError, message):
                    validate(request)
        idle = valid_idle_request(Path("/model"))
        idle["datasetPath"] = "/dataset"
        with self.assertRaisesRegex(runner.ContractError, "unknown=\\['datasetPath'\\]"):
            validate(idle)

    def test_decode_request_rejects_duplicate_field(self) -> None:
        with self.assertRaisesRegex(runner.ContractError, "duplicate JSON field"):
            runner.decode_request(
                io.BytesIO(b'{"schemaVersion":"a","schemaVersion":"b"}')
            )

    def test_validate_request_rejects_unknown_contract_field(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["unknown"] = True
            with self.assertRaisesRegex(
                runner.ContractError, "unknown=\\['unknown'\\]"
            ):
                validate(request)

    def test_validate_request_accepts_current_cpu_run_config(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            validate(valid_request(model))

    def test_validate_request_accepts_configured_cpu_quota(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["runConfig"]["targetNode"]["allocatableCPU"] = "12"
            request["runConfig"]["cpu"].update(
                {
                    "policy": "ConfiguredCPUQuota",
                    "limit": "4",
                    "ortIntraOpThreads": 4,
                }
            )

            validate(request)
            self.assertEqual(
                runner.derived_contract(request["runConfig"])["cpu"]["budgetSource"],
                "ProfilingRunMeasurementCPUQuota",
            )

    def test_validate_request_binds_current_actuation_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["runConfig"]["state"]["powerMode"] = "MODE_15W_6CORE"
            request["runConfig"]["actuation"] = {
                "powerMode": "MODE_15W_6CORE",
                "actuator": {"kind": "JetsonNVPModel", "jetsonNVPModel": {"id": 2}},
                "policyName": "jetson-mode-15w",
            }
            validate(request)

            request["runConfig"]["actuation"]["actuator"]["jetsonNVPModel"]["id"] = -1
            with self.assertRaisesRegex(runner.ContractError, "jetsonNVPModel.id"):
                validate(request)

    def test_validate_request_rejects_null_actuation_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["runConfig"]["state"]["powerMode"] = "MODE_15W_6CORE"
            request["runConfig"]["actuation"] = None
            with self.assertRaisesRegex(
                runner.ContractError, "actuation must be an object"
            ):
                validate(request)

    def test_validate_request_rejects_unsupported_schemas(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["runConfig"]["schemaVersion"] = "unsupported"
            with self.assertRaisesRegex(
                runner.ContractError, "supported CPU RunConfig schema"
            ):
                validate(request)

            request = valid_request(model)
            request["schemaVersion"] = "unsupported"
            with self.assertRaisesRegex(
                runner.ContractError, "unsupported runtime request"
            ):
                validate(request)

    def test_validate_request_requires_job_timing_binding(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            del request["observerReadyTimeoutMilliseconds"]
            with self.assertRaisesRegex(
                runner.ContractError, "observerReadyTimeoutMilliseconds"
            ):
                validate(request)

    def test_validate_request_rejects_scheduled_node_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            request = valid_request(model)
            request["scheduledNodeName"] = "other-node"
            with self.assertRaisesRegex(runner.ContractError, "scheduled Node"):
                validate(request)

    def test_validate_request_requires_job_resource_budgets(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            for field in ("maxLatencySamples", "maxInputBytes", "maxResultBytes"):
                with self.subTest(field=field):
                    request = valid_request(model)
                    del request[field]
                    with self.assertRaisesRegex(runner.ContractError, field):
                        validate(request)

    def test_validate_request_keeps_active_and_idle_measurement_fields_disjoint(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            model = Path(directory) / "model.onnx"
            model.write_bytes(b"model")
            active = valid_request(model)
            active["runConfig"]["measurement"]["settlingDurationSeconds"] = 60
            with self.assertRaisesRegex(
                runner.ContractError, "unknown=\\['settlingDurationSeconds'\\]"
            ):
                validate(active)

            idle = valid_idle_request(model)
            del idle["runConfig"]["measurement"]["settlingDurationSeconds"]
            with self.assertRaisesRegex(
                runner.ContractError, "missing=\\['settlingDurationSeconds'\\]"
            ):
                validate(idle)

    def test_producer_budgets_fail_before_growth(self) -> None:
        with self.assertRaisesRegex(runner.ContractError, "latency sample budget"):
            runner.measurement_complete(0.5, 1, 10, 10)
        self.assertTrue(runner.measurement_complete(1.0, 1, 10, 10))
        with self.assertRaisesRegex(runner.ContractError, "runtime result exceeds"):
            runner.encode_result({"samples": [1, 2, 3]}, 1)

    def test_optional_affinity_request_and_actual_cpu_contract(self) -> None:
        request = valid_request(Path("/model/model.onnx"))
        cpu = request["runConfig"]["cpu"]
        cpu["affinityCPUs"] = list(range(100, 100 + cpu["ortIntraOpThreads"]))
        validate(request)
        for invalid in (None, [], [True], [-1], [100, 100]):
            with self.subTest(invalid=invalid):
                cpu["affinityCPUs"] = invalid
                with self.assertRaises(runner.ContractError):
                    validate(request)
        run = {"cpu": {"limit": "2", "ortIntraOpThreads": 2, "affinityCPUs": [17, 9]}}
        with mock.patch.object(runner, "read_cpu_environment", return_value=("200000 100000", [9, 17], "9,17,31")):
            self.assertEqual(runner.validate_cpu_contract(run), ("200000 100000", [9, 17], "9,17,31"))
        with mock.patch.object(runner, "read_cpu_environment", return_value=("200000 100000", [9, 17, 31], "9,17,31")):
            with self.assertRaisesRegex(runner.ContractError, "differs from requested"):
                runner.validate_cpu_contract(run)
            del run["cpu"]["affinityCPUs"]
            self.assertEqual(runner.validate_cpu_contract(run)[1], [9, 17, 31])

    def test_cpu_contract_reads_cgroup_v2(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "cpu.max").write_text("400000 100000\n", encoding="utf-8")
            (root / "cpu.stat").write_text("usage_usec 20\n", encoding="utf-8")
            (root / "cpuset.cpus.effective").write_text("0-3\n", encoding="utf-8")
            run_config = {"cpu": {"limit": "4", "ortIntraOpThreads": 4}}
            with mock.patch.object(
                runner.os, "sched_getaffinity", return_value={0, 1, 2, 3}
            ):
                cpu_max, affinity, cpuset = runner.validate_cpu_contract(run_config, root)

            self.assertEqual(cpu_max, "400000 100000")
            self.assertEqual(affinity, [0, 1, 2, 3])
            self.assertEqual(cpuset, "0-3")
            self.assertEqual(runner.cpu_stat(root), {"usage_usec": 20})

    def test_cpu_contract_normalizes_cgroup_v1(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cpu = root / "cpu,cpuacct"
            cpuset = root / "cpuset"
            cpu.mkdir()
            cpuset.mkdir()
            (cpu / "cpu.cfs_quota_us").write_text("1200000\n", encoding="utf-8")
            (cpu / "cpu.cfs_period_us").write_text("100000\n", encoding="utf-8")
            (cpu / "cpu.stat").write_text(
                "nr_periods 7\nnr_throttled 2\nthrottled_time 30\n", encoding="utf-8"
            )
            (cpuset / "cpuset.cpus").write_text("0-11\n", encoding="utf-8")
            run_config = {"cpu": {"limit": "12", "ortIntraOpThreads": 12}}
            with mock.patch.object(
                runner.os, "sched_getaffinity", return_value=set(range(12))
            ):
                cpu_max, affinity, cpuset_value = runner.validate_cpu_contract(
                    run_config, root
                )

            self.assertEqual(cpu_max, "1200000 100000")
            self.assertEqual(affinity, list(range(12)))
            self.assertEqual(cpuset_value, "0-11")
            self.assertEqual(
                runner.cpu_stat(root),
                {"nr_periods": 7, "nr_throttled": 2, "throttled_time": 30},
            )

    def test_cpu_contract_rejects_unknown_cgroup_layout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(
                runner.ContractError, "supported cgroup CPU controller"
            ):
                runner.validate_cpu_contract(
                    {"cpu": {"limit": "4", "ortIntraOpThreads": 4}}, Path(directory)
                )

    def test_publish_immutable_refuses_replacement(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            result = Path(directory) / "result.json"
            runner.publish_immutable(result, b"first")
            self.assertEqual(result.read_bytes(), b"first")
            with self.assertRaisesRegex(runner.ContractError, "already exists"):
                runner.publish_immutable(result, b"second")
            self.assertEqual(result.read_bytes(), b"first")

    def test_execute_loaded_produces_bound_runtime_result(self) -> None:
        request = valid_request(Path("/model/model.onnx"))
        session = FakeSession()
        before = mock.Mock()
        with ExitStack() as stack:
            wait = stack.enter_context(mock.patch.object(runner, "wait_for_signal"))
            publish = stack.enter_context(mock.patch.object(runner, "publish_immutable"))
            stack.enter_context(
                mock.patch.object(runner, "cpu_stat", side_effect=[{"usage_usec": 10}, {"usage_usec": 30}])
            )
            stack.enter_context(
                mock.patch.object(
                    runner,
                    "now",
                    side_effect=[
                        "2026-07-16T00:00:00.100000Z",
                        "2026-07-16T00:00:01.200000Z",
                        "2026-07-16T00:00:01.300000Z",
                        "2026-07-16T00:00:01.400000Z",
                    ],
                )
            )
            stack.enter_context(mock.patch.object(runner.time, "monotonic", side_effect=[0.0, 1.1, 1.1]))
            ticks = iter([0, 5_000_000])
            stack.enter_context(mock.patch.object(
                runner.time, "perf_counter_ns", side_effect=lambda: session.calls.append("timer") or next(ticks)
            ))
            stack.enter_context(mock.patch.object(runner.time, "process_time", side_effect=[2.0, 2.8]))
            result = runner.execute_loaded(
                request, session, "test-ort", ["CPUExecutionProvider"], "x86_64",
                "400000 100000", [0, 1, 2, 3], "0-3", before, "CPUExecutionProvider",
            )

        before.assert_called_once_with()
        self.assertEqual(
            publish.call_args_list,
            [
                mock.call(Path(request["workloadWindowStartPath"]), b"signal\n"),
                mock.call(Path(request["measurementStartPath"]), b"signal\n"),
                mock.call(Path(request["measurementEndPath"]), b"signal\n"),
            ],
        )
        self.assertEqual(
            wait.call_args_list,
            [mock.call(Path(path), 30.0) for path in request["observerReadyPaths"]]
            + [mock.call(Path(request["workloadWindowReadyPath"]), 30.0)],
        )
        self.assertEqual(session.calls, ["prepare", "timer", "run", "timer"])
        self.assertEqual(result["schemaVersion"], runner.RESULT_SCHEMA)
        self.assertEqual(result["inputs"], ["image"])
        self.assertNotIn("outputValidation", result)
        self.assertEqual(result["status"], "Succeeded")
        self.assertEqual(result["experimentId"], "pod-uid")
        self.assertEqual(result["nodeName"], request["scheduledNodeName"])
        self.assertEqual(result["provider"], "CPUExecutionProvider")
        self.assertEqual(result["inferenceCount"], 1)
        self.assertEqual(result["latencyMs"]["mean"], 5.0)
        self.assertEqual(result["cpuContract"]["cgroupCPUStatDelta"], {"usage_usec": 20})
        self.assertEqual(result["derivedExecutionContract"]["nodeSnapshot"]["uid"], "node-uid")

    def test_execute_idle_issues_no_inference(self) -> None:
        request = valid_idle_request(Path("/model/model.onnx"))
        session = FakeSession()
        with ExitStack() as stack:
            wait = stack.enter_context(mock.patch.object(runner, "wait_for_signal"))
            publish = stack.enter_context(mock.patch.object(runner, "publish_immutable"))
            sleep = stack.enter_context(mock.patch.object(runner.time, "sleep"))
            stack.enter_context(
                mock.patch.object(runner, "cpu_stat", side_effect=[{"usage_usec": 10}, {"usage_usec": 15}])
            )
            stack.enter_context(
                mock.patch.object(
                    runner,
                    "now",
                    side_effect=[
                        "2026-07-16T00:00:01.000000Z",
                        "2026-07-16T00:01:01.000000Z",
                        "2026-07-16T00:01:01.100000Z",
                        "2026-07-16T00:01:31.200000Z",
                        "2026-07-16T00:01:31.300000Z",
                    ],
                )
            )
            stack.enter_context(
                mock.patch.object(
                    runner.time, "monotonic", side_effect=[0.0, 0.0, 60.0, 100.0, 100.0, 130.0, 130.0]
                )
            )
            stack.enter_context(mock.patch.object(runner.time, "process_time", side_effect=[2.0, 2.1]))
            result = runner.execute_idle(
                request, "test-ort", session, ["CPUExecutionProvider"], "x86_64",
                "400000 100000", [0, 1, 2, 3], "0-3", "2026-07-16T00:00:00.000000Z", "CPUExecutionProvider",
            )

        self.assertEqual(session.calls, [])
        self.assertEqual(result["schemaVersion"], runner.IDLE_RESULT_SCHEMA)
        self.assertEqual(result["inputs"], ["image"])
        self.assertEqual(result["inferenceCountAfterModelLoad"], 0)
        self.assertTrue(result["modelResidentAtEnd"])
        self.assertEqual(result["settlingStartedAt"], "2026-07-16T00:00:01.000000Z")
        self.assertEqual(result["measurementStartedAt"], "2026-07-16T00:01:01.100000Z")
        self.assertEqual(result["cpuContract"]["cgroupCPUStatDelta"], {"usage_usec": 5})
        self.assertGreaterEqual(sleep.call_count, 2)
        self.assertEqual(
            publish.call_args_list,
            [
                mock.call(Path(request["workloadWindowStartPath"]), b"signal\n"),
                mock.call(Path(request["measurementStartPath"]), b"signal\n"),
                mock.call(Path(request["measurementEndPath"]), b"signal\n"),
            ],
        )
        self.assertEqual(
            wait.call_args_list,
            [mock.call(Path(path), 30.0) for path in request["observerReadyPaths"]]
            + [mock.call(Path(request["workloadWindowReadyPath"]), 30.0)],
        )


if __name__ == "__main__":
    unittest.main()
