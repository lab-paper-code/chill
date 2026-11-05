from __future__ import annotations

import io
import copy
import json
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
        self.executions = 0

    def get_providers(self) -> list[str]:
        return ["CPUExecutionProvider"]

    def prepare_next(self) -> None:
        self.calls.append("prepare")

    def run(self) -> dict[str, object]:
        self.calls.append("run")
        self.executions += 1
        return {"precedingIdleNanos": None if self.executions == 1 else 0}


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
            "actor": {
                "model": "mobilenet-v2-050", "mode": "profiling",
                "runnerCommand": ["python3", "/opt/chill/runtime/firstfloor/runner.py",
                                  "--model-dir", "/model", "--preprocessor", "/preprocessor",
                                  "--warmup-batch", "1", "--warmup-iterations", "2"],
                "batchSizes": [1], "warmupBatchSizes": [1], "warmupIterations": 2,
                "maxBatchSize": 1, "maxQueueRequests": 1, "maxQueueBytes": 1024,
                "maxRequestBytes": 1024, "runnerTimeoutMS": 30000,
                "inputs": ["image"], "outputs": ["label"], "policy": {"deadlinePolicy": "keep"},
            },
            "delivery": {"artifactCapability": {}},
            "targetNode": {
                "name": "lattepanda",
                "uid": "node-uid",
                "resourceVersion": "10",
                "architecture": "amd64",
                "allocatableCPU": "4",
            },
            "measurement": {
                "batch": 1,
                "warmupIterations": 2,
                "startupWarmupIterations": 2,
                "durationSeconds": 30,
                "sloPercentile": 95,
                "minServiceSamples": 59,
                "sloTargetLatencyNanos": 1_000_000_000,
                "workloadName": "mobilenet",
                "workloadUID": "workload-uid",
                "workloadResourceVersion": "1",
                "durationToleranceSeconds": 0.5,
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
        "durationToleranceSeconds": 0.5,
        "workloadMode": runner.IDLE_WORKLOAD,
    }
    return request


class RunnerTest(unittest.TestCase):
    def test_stop_policy_uses_actual_percentile_and_rejects_false_counts(self) -> None:
        request = valid_request(Path("/model"))
        validate(request)
        del request["runConfig"]["measurement"]["workloadResourceVersion"]
        validate(request)
        request["runConfig"]["measurement"].update(sloPercentile=99, minServiceSamples=299)
        validate(request)
        for changes in ({"minServiceSamples": 58}, {"sloPercentile": 100},
                        {"sloPercentile": True}, {"durationSeconds": 29}):
            bad = valid_request(Path("/model"))
            bad["runConfig"]["measurement"].update(changes)
            with self.subTest(changes=changes), self.assertRaises(runner.ContractError):
                validate(bad)

    def test_real_loop_requires_time_and_samples_in_every_restarted_trial(self) -> None:
        for seconds_per_call, expected_calls in ((0.25, 120), (2.0, 60)):
            for batch in (1, 2, 4):
                for trial in range(3):
                    clock = [0.0]
                    session = FakeSession()
                    request = valid_request(Path("/model"))
                    request["runConfig"]["measurement"]["batch"] = batch
                    def complete_call():
                        session.calls.append("run")
                        clock[0] += seconds_per_call
                        session.executions += 1
                        return {"precedingIdleNanos": None if session.executions == 1 else 0}
                    session.run = complete_call
                    with ExitStack() as stack:
                        stack.enter_context(mock.patch.object(runner, "wait_for_signal"))
                        stack.enter_context(mock.patch.object(runner, "publish_immutable"))
                        stack.enter_context(mock.patch.object(runner, "cpu_stat", return_value={}))
                        stack.enter_context(mock.patch.object(runner.time, "monotonic", side_effect=lambda: clock[0]))
                        stack.enter_context(mock.patch.object(runner.time, "perf_counter_ns", side_effect=lambda: int(clock[0] * 1e9)))
                        result = runner.execute_loaded(
                            request, session, "test-ort", ["CPUExecutionProvider"], "x86_64",
                            "max 100000", [0], "0", lambda: None, "CPUExecutionProvider")
                    with self.subTest(seconds_per_call=seconds_per_call, batch=batch, trial=trial):
                        self.assertEqual(result["inferenceCount"], expected_calls)
                        self.assertGreaterEqual(result["inferenceCount"] - 1, 59)
                        self.assertGreaterEqual(result["measurementElapsedSecondsMonotonic"], 30)
                        self.assertEqual(len(result["repetitionResults"][0]["latencyMs"]), expected_calls)

    def test_active_reader_accepts_no_pacing_and_rejects_old_schema_and_donor_fields(self) -> None:
        valid = valid_request(Path("/model"))
        validate(valid)
        request = copy.deepcopy(valid)
        request["runConfig"]["schemaVersion"] = "chill.dacs.io/profiling-run-config.v1alpha19"
        with self.assertRaises(runner.ContractError):
            validate(request)
        for field in ("cadence", "cadenceDraw", "targetIdleNanos", "traceDrawRef"):
            request = copy.deepcopy(valid)
            request["runConfig"]["measurement"][field] = None
            with self.subTest(field=field), self.assertRaises(runner.ContractError):
                validate(request)

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

    def test_own_cgroup_root_follows_proc_self_cgroup_in_host_namespace(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "cgroup.controllers").write_text("cpu memory\n")
            own = root / "kubepods.slice" / "pod.slice" / "cri.scope"
            own.mkdir(parents=True)
            (own / "cpu.max").write_text("200000 100000\n")
            self_cgroup = root / "self-cgroup"
            self_cgroup.write_text("0::/kubepods.slice/pod.slice/cri.scope\n")
            self.assertEqual(runner.own_cgroup_root(root, self_cgroup), own)
            # A namespaced container already sees its own cgroup at the root.
            (root / "cpu.max").write_text("max 100000\n")
            self.assertEqual(runner.own_cgroup_root(root, self_cgroup), root)

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

    def test_shared_request_rejects_old_cpu_contract(self) -> None:
        request = valid_request(Path("/model/model.onnx"))
        del request["runConfig"]["targetNode"]["allocatableCPU"]
        validate(request)
        request["runConfig"]["cpu"] = {"limit": "4"}
        with self.assertRaisesRegex(runner.ContractError, "unknown=.*cpu"):
            validate(request)

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
            request["runConfig"]["schemaVersion"] = "chill.dacs.io/profiling-run-config.v1alpha18"
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
            runner.measurement_complete(0.5, 1, 10, 10, 10, 9)
        self.assertTrue(runner.measurement_complete(1.0, 1, 10, 10, 10, 10))
        with self.assertRaisesRegex(runner.ContractError, "runtime result exceeds"):
            runner.encode_result({"samples": [1, 2, 3]}, 1)


    def test_cpu_contract_reads_cgroup_v2(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "cpu.max").write_text("400000 100000\n", encoding="utf-8")
            (root / "cpu.stat").write_text("usage_usec 20\n", encoding="utf-8")
            (root / "cpuset.cpus.effective").write_text("0-3\n", encoding="utf-8")
            with mock.patch.object(
                runner.os, "sched_getaffinity", return_value={0, 1, 2, 3}
            ):
                cpu_max, affinity, cpuset = runner.read_cpu_environment(root)

            self.assertEqual(cpu_max, "400000 100000")
            self.assertEqual(affinity, [0, 1, 2, 3])
            self.assertEqual(cpuset, "0-3")
            self.assertEqual(runner.cpu_stat(root), {"usage_usec": 20})
            for quota in ("max 100000", "150000 100000"):
                (root / "cpu.max").write_text(quota + "\n", encoding="utf-8")
                with mock.patch.object(runner.os, "sched_getaffinity", return_value={0, 1, 2, 3}):
                    self.assertEqual(runner.read_cpu_environment(root), (quota, [0, 1, 2, 3], "0-3"))
            for quota in ("max 0", "-2 100000", "max invalid", "400000"):
                (root / "cpu.max").write_text(quota + "\n", encoding="utf-8")
                with self.assertRaises(runner.ContractError):
                    runner.read_cpu_environment(root)

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
            with mock.patch.object(
                runner.os, "sched_getaffinity", return_value=set(range(12))
            ):
                cpu_max, affinity, cpuset_value = runner.read_cpu_environment(root)

            self.assertEqual(cpu_max, "1200000 100000")
            self.assertEqual(affinity, list(range(12)))
            self.assertEqual(cpuset_value, "0-11")
            self.assertEqual(
                runner.cpu_stat(root),
                {"nr_periods": 7, "nr_throttled": 2, "throttled_time": 30},
            )
            (cpu / "cpu.cfs_quota_us").write_text("-1\n", encoding="utf-8")
            with mock.patch.object(runner.os, "sched_getaffinity", return_value=set(range(12))):
                self.assertEqual(runner.read_cpu_environment(root)[0], "-1 100000")

    def test_cpu_contract_rejects_unknown_cgroup_layout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(
                runner.ContractError, "supported cgroup CPU controller"
            ):
                runner.read_cpu_environment(Path(directory))

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
            stack.enter_context(mock.patch.object(runner.time, "monotonic", side_effect=[0.0] + [30.1] * 61))
            ticks = iter(value for index in range(60) for value in (index * 10_000_000, index * 10_000_000 + 5_000_000))
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
        self.assertEqual(session.calls, ["prepare", "timer", "run", "timer"] * 60)
        self.assertEqual(result["schemaVersion"], runner.RESULT_SCHEMA)
        self.assertEqual(result["inputs"], ["image"])
        self.assertNotIn("outputValidation", result)
        self.assertEqual(result["status"], "Succeeded")
        self.assertEqual(result["experimentId"], "pod-uid")
        self.assertEqual(result["nodeName"], request["scheduledNodeName"])
        self.assertEqual(result["provider"], "CPUExecutionProvider")
        self.assertEqual(result["inferenceCount"], 60)
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
