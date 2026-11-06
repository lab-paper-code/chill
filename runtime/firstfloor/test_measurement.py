from __future__ import annotations

import base64
import copy
from contextlib import ExitStack
import json
import os
from pathlib import Path
import random
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import measurement


PREPROCESSOR = {"name": "mobilenet-imagenet", "uid": "pre-uid", "image": "registry/pre:1", "architecture": "amd64",
                "path": "/artifact/mobilenet-nchw", "inputs": ["image"], "outputs": ["label"]}


class MeasurementBoundaryTest(unittest.TestCase):
    def test_selected_deadline_policy_defaults_validates_and_reaches_worker(self):
        actor = {"model": "model", "inputs": ["image"], "outputs": ["label"],
                 "batchSizes": [1, 2, 3, 4], "warmupBatchSizes": [1, 4],
                 "runnerCommand": ["python3", "/opt/chill/runtime/firstfloor/runner.py",
                                   "--runtime", "ort-cpu", "--model-dir", "/model",
                                   "--preprocessor", "/preprocessor", "--warmup-batch", "1",
                                   "--warmup-batch", "4"]}
        request = {"maxInputBytes": 1024, "observerReadyTimeoutMilliseconds": 1000,
                   "runConfig": {"actor": actor, "state": {"firstfloor": {}}}}
        settings = request["runConfig"]["state"]["firstfloor"]
        config = measurement.worker_config(request)
        self.assertEqual(config["policy"]["deadlinePolicy"], "keep")
        self.assertEqual((config["inputs"], config["outputs"]), (["image"], ["label"]))
        self.assertNotIn("maxNewTokens", config)
        command = config["runnerCommand"]
        self.assertEqual(command[command.index("--model-dir") + 1], "/model")
        self.assertEqual(command[command.index("--warmup-batch") + 1], "1")
        self.assertEqual(command[command.index("--preprocessor") + 1], "/preprocessor")
        self.assertNotIn("--model", command)
        self.assertNotIn("--intra-op-threads", command)
        self.assertNotIn("--cpu-affinity", command)
        request["runConfig"]["actor"]["maxNewTokens"] = 32
        self.assertEqual(measurement.worker_config(request)["maxNewTokens"], 32)
        # The runtime consumes the exact frozen command, including every measured
        # warmup K; changing a deciding field cannot be lost to reconstruction.
        self.assertEqual(config["batchSizes"], [1, 2, 3, 4])
        self.assertEqual(config["warmupBatchSizes"], [1, 4])
        actor["runnerCommand"][-1] = "2"
        self.assertEqual(measurement.worker_config(request)["runnerCommand"][-1], "2")
        self.assertEqual(config["runnerCommand"][-1], "4")
        for policy in ("drop", "keep", "defer"):
            settings["deadlinePolicy"] = policy
            self.assertEqual(measurement.worker_config(request)["policy"]["deadlinePolicy"], policy)
        for invalid in ("", "Drop", None, [], True):
            settings["deadlinePolicy"] = invalid
            with self.subTest(invalid=invalid), self.assertRaises(measurement.common.ContractError):
                measurement.worker_config(request)



    def test_sparse_actual_initialized_batches_do_not_fabricate_batch_one(self):
        run = {"actor": {"batchSizes": [2, 4]},
               "measurement": {"batch": 4}}
        self.assertEqual(measurement.initialized_batches(run), [2, 4])
        run["measurement"]["batch"] = 1
        with self.assertRaises(measurement.common.ContractError):
            measurement.initialized_batches(run)


    def test_native_samples_require_actual_aligned_nested_timings(self):
        pair = {"serviceDurationNanos": 2_000_001, "precedingIdleNanos": None, "trialID": "physical-pod"}
        response = {"batchSize": 2, "executionComplete": True, "batchIDs": [9], "serviceSamples": [pair],
                    "preprocessSamplesMS": [0.25], "backendSamplesMS": [1.0], "postprocessSamplesMS": [0.25],
                    "nativeSamplesMS": [3.0], "units": [1, 3], "nativeElapsedMS": 4.0}
        records = ["r1", "r0"]
        sample = measurement.native_sample(response, 2, records, False)
        self.assertEqual(sample["serviceDurationNanos"], 2_000_001)
        self.assertIsNone(sample["precedingIdleNanos"])
        self.assertEqual((sample["batchID"], sample["recordIDs"], sample["units"]), (9, records, [1, 3]))
        generative = measurement.native_sample(dict(response, firstTokenSamplesMS=[1.5]), 2, records, True)
        self.assertEqual(generative["firstTokenMs"], 1.5)
        for key in ("batchIDs", "serviceSamples", "preprocessSamplesMS", "backendSamplesMS",
                    "postprocessSamplesMS", "nativeSamplesMS"):
            for invalid in (None, [], [1, 2]):
                with self.subTest(key=key, invalid=invalid), self.assertRaises(measurement.common.ContractError):
                    measurement.native_sample(dict(response, **{key: invalid}), 2, records, False)
        for changes in ({"backendSamplesMS": [3.0]}, {"preprocessSamplesMS": [1.0]}, {"nativeSamplesMS": [1.0]},
                        {"serviceSamples": [dict(pair, serviceDurationNanos=0)]},
                        {"serviceSamples": [dict(pair, serviceDurationNanos=2.5)]},
                        {"serviceSamples": [dict(pair, precedingIdleNanos=-1)]},
                        {"serviceSamples": [dict(pair, serviceDurationNanos=1 << 63)]},
                        {"serviceSamples": [dict(pair, trialID="")]},
                        {"serviceSamples": [dict(pair, targetIdleNanos=0)]},
                        {"serviceSamples": [dict(pair, traceDrawRef=None)]},
                        {"postprocessSamplesMS": [-0.1]}, {"batchIDs": [True]}, {"batchIDs": [1 << 64]},
                        {"units": [1]}, {"units": [1, 0]}, {"units": [1, True]}, {"units": None},
                        {"firstTokenSamplesMS": [1.0]}, {"batchSize": 1}, {"executionComplete": False},
                        {"nativeElapsedMS": 2.0}):
            with self.subTest(changes=changes), self.assertRaises(measurement.common.ContractError):
                measurement.native_sample(dict(response, **changes), 2, records, False)
        with self.assertRaisesRegex(measurement.common.ContractError, "align"):
            measurement.native_sample(response, 2, ["r1"], False)
        with self.assertRaises(measurement.common.ContractError):
            measurement.native_sample(response, 2, records, True)
        legacy = dict(response, serviceSamplesMS=[2.0])
        legacy.pop("serviceSamples")
        with self.assertRaises(measurement.common.ContractError):
            measurement.native_sample(legacy, 2, records, False)

    def test_sampling_draws_fresh_seeded_records_from_input_fields_only(self):
        with tempfile.TemporaryDirectory() as directory:
            for index in range(5):
                record = Path(directory) / f"q{index}"
                record.mkdir()
                (record / "image").write_bytes(f"jpeg-{index}".encode())
                (record / "question").write_bytes(b"unused field")
            (Path(directory) / "README").write_text("not a record")
            request = {"datasetPath": directory, "podUID": "physical-pod",
                       "runConfig": {"state": {"model": "model", "preprocessor": dict(PREPROCESSOR)},
                                     "measurement": {"batch": 2, "workloadMode": measurement.common.SUPPORTED_WORKLOAD}}}
            session = measurement.FirstfloorSession(1, request)
            self.assertEqual(session.records, [f"q{index}" for index in range(5)])
            expected = random.Random(measurement.SAMPLE_SEED)
            draws = []
            for _ in range(3):
                session.prepare_next()
                draws.append(session.record_ids)
                self.assertEqual(session.record_ids, expected.sample(session.records, 2))
                body = json.loads(session.payload)
                self.assertEqual(body, {"batchSize": 2, "trialID": "physical-pod", "inputs": [{"name": "image", "data": [
                    base64.b64encode(f"jpeg-{record[1:]}".encode()).decode() for record in session.record_ids]}]})
            self.assertGreater(len({tuple(draw) for draw in draws}), 1)
            again = measurement.FirstfloorSession(1, request)
            again.prepare_next()
            self.assertEqual(again.record_ids, draws[0])
            response = {"batchSize": 2, "executionComplete": True, "batchIDs": [1],
                        "serviceSamples": [{"serviceDurationNanos": 2_000_000, "precedingIdleNanos": None,
                                            "trialID": "physical-pod"}],
                        "preprocessSamplesMS": [0.5], "backendSamplesMS": [1.0], "postprocessSamplesMS": [0.5],
                        "nativeSamplesMS": [3.0], "units": [1, 1], "nativeElapsedMS": 4.0}
            with mock.patch.object(again, "json_call", return_value=response):
                again.run()
            self.assertEqual(again.samples[0]["recordIDs"], draws[0])
            self.assertIsNone(again.payload)
            request["runConfig"]["measurement"]["batch"] = 6
            with self.assertRaisesRegex(measurement.common.ContractError, "fewer profiling records"):
                measurement.FirstfloorSession(1, request)

    def test_unpaced_profile_calls_preserve_actual_pairs_through_long_active_wall(self):
        # This fake clock tests collection behavior, not real runtime performance.
        for backend in ("ort-cpu", "rknnlite"):
            for initial_gap, expected_calls in ((None, 60), (123, 59)):
                with self.subTest(backend=backend, initial_gap=initial_gap), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    (root / "record").mkdir()
                    (root / "record/image").write_bytes(b"image")
                    request = {
                        "datasetPath": directory, "podUID": "new-physical-pod", "scheduledNodeName": "node",
                        "observerReadyPaths": [], "observerReadyTimeoutMilliseconds": 1000,
                        "workloadWindowStartPath": str(root / "window"), "workloadWindowReadyPath": str(root / "ready"),
                        "measurementStartPath": str(root / "start"), "measurementEndPath": str(root / "end"),
                        "maxLatencySamples": 1000,
                        "runConfig": {
                            "state": {"model": "model", "runtimeFamily": "firstfloor", "backend": backend,
                                      "preprocessor": dict(PREPROCESSOR)},
                            "targetNode": {"name": "node", "uid": "node-uid"},
                            "measurement": {"batch": 1, "warmupIterations": 0, "durationSeconds": 30,
                                            "minServiceSamples": 59, "workloadMode": measurement.common.SUPPORTED_WORKLOAD},
                        },
                    }
                    clock = [0]
                    received = []
                    session = measurement.FirstfloorSession(1, request)

                    def profile_call(method, path, payload=None):
                        self.assertEqual((method, path), ("POST", "/v2/models/model/profile"))
                        body = json.loads(payload)
                        received.append(body)
                        self.assertEqual(body["trialID"], "new-physical-pod")
                        self.assertEqual(set(body), {"batchSize", "inputs", "trialID"})
                        pair = {"serviceDurationNanos": 2_000_001, "trialID": "new-physical-pod"}
                        if len(received) == 1:
                            pair["precedingIdleNanos"] = initial_gap
                        else:
                            # Input preparation/transport idle is observed, never requested.
                            gap = 7_000_000_017 if len(received) % 2 == 0 else 5_000_000_017
                            pair["precedingIdleNanos"] = gap
                            clock[0] += gap
                        clock[0] += 4_000_000
                        return {"batchSize": 1, "executionComplete": True, "batchIDs": [len(received)],
                                "serviceSamples": [pair], "preprocessSamplesMS": [0.25], "backendSamplesMS": [1.0],
                                "postprocessSamplesMS": [0.25], "nativeSamplesMS": [3.0], "units": [1],
                                "nativeElapsedMS": 4.0}

                    with ExitStack() as stack:
                        stack.enter_context(mock.patch.object(session, "json_call", side_effect=profile_call))
                        stack.enter_context(mock.patch.object(measurement.common, "wait_for_signal"))
                        stack.enter_context(mock.patch.object(measurement.common, "publish_immutable"))
                        stack.enter_context(mock.patch.object(measurement.common, "cpu_stat", return_value={}))
                        stack.enter_context(mock.patch.object(measurement.time, "monotonic", side_effect=lambda: clock[0] / 1e9))
                        stack.enter_context(mock.patch.object(measurement.time, "perf_counter_ns", side_effect=lambda: clock[0]))
                        stack.enter_context(mock.patch.object(measurement.time, "sleep", side_effect=AssertionError("Python must not pace")))
                        result = measurement.common.execute_loaded(
                            request, session, "test-runtime", [], "aarch64", "max 100000", [0], "0",
                            lambda: None, measurement.PROVIDERS[backend])
                    self.assertEqual(result["inferenceCount"], expected_calls)
                    self.assertGreater(result["measurementElapsedSecondsMonotonic"], 166)
                    self.assertEqual(sum(row["precedingIdleNanos"] is not None for row in session.samples), 59)
                    encoded = json.loads(measurement.common.encode_result({"nativeSamples": session.samples}, 100_000))
                    rows = encoded["nativeSamples"]
                    self.assertEqual(rows[0]["precedingIdleNanos"], initial_gap)
                    self.assertNotIn("traceDrawRef", rows[0])
                    self.assertEqual(rows[-1]["serviceDurationNanos"], 2_000_001)
                    self.assertEqual(rows[-1]["trialID"], "new-physical-pod")
                    self.assertEqual(rows[-1]["recordIDs"], ["record"])
                    self.assertIsInstance(rows[-1]["precedingIdleNanos"], int)
                    self.assertNotIn("traceDrawRef", rows[-1])
                    self.assertNotIn("targetIdleNanos", rows[-1])

    def test_actual_runtime_rejects_provider_fallback_and_changed_conditions(self):
        request = {"modelDirectory": "/model", "maxInputBytes": 1024,
                   "runConfig": {"state": {"model": "model", "backend": "ort-cuda",
                                          "firstfloor": {"maxBatchSize": 4, "maxQueueSize": 8},
                                          "artifactFiles": ["/artifact/onnx/vision.onnx", "/artifact/onnx/decoder.onnx"],
                                          "preprocessor": dict(PREPROCESSOR, inputs=["image", "question"], outputs=["answer"])},
                                 "measurement": {"batch": 4, "warmupIterations": 2},
                                 "actor": {"batchSizes": [1, 2, 4], "warmupIterations": 2},
                                 "delivery": {"artifactCapability": {"declaredBatchSizes": [1, 2, 4]},
                                              "runtimeAccess": {"gpuUUID": "GPU-actual", "resources": {"nvidia.com/gpu": "1"}}}}}
        actual = {"runtime": "ort-cuda", "runtimeVersion": "1.20.0", "numpyVersion": "2.1.0",
                  "modelFiles": ["/model/decoder.onnx", "/model/vision.onnx"], "inputs": ["image", "question"],
                  "outputs": ["answer"],
                  "providers": ["CUDAExecutionProvider", "CPUExecutionProvider"], "availableProviders": ["CUDAExecutionProvider", "CPUExecutionProvider"],
                  "device": "cuda", "gpuUUID": "GPU-actual", "cpuAffinity": [0, 1], "intraOpThreads": 2,
                  "interOpThreads": 1, "initializedBatches": [1, 2, 4], "warmupIterations": 2,
                  "cgroupCPUMax": "200000 100000", "cpuSet": "0-1", "synthetic": False}
        metadata = {"model": "model", "mode": "profiling", "maxQueueRequests": 8, "maxBatchSize": 4,
                    "modelInstanceCount": 1, "maxQueueBytes": 1024, "maxRequestBytes": measurement.FRAME_BYTES,
                    "policy": {"deadlinePolicy": "keep"},
                    "runtime": actual}
        self.assertEqual(measurement.validate_metadata(metadata, request, "200000 100000", [0, 1], "0-1")["gpuUUID"], "GPU-actual")
        for policy in ("drop", "keep", "defer"):
            request["runConfig"]["state"]["firstfloor"]["deadlinePolicy"] = policy
            metadata["policy"]["deadlinePolicy"] = policy
            self.assertEqual(measurement.validate_metadata(metadata, request, "200000 100000", [0, 1], "0-1")["device"], "cuda")
            with self.assertRaises(measurement.common.ContractError):
                measurement.validate_metadata(dict(metadata, policy={"deadlinePolicy": "invalid"}),
                                              request, "200000 100000", [0, 1], "0-1")
        request["runConfig"]["state"]["firstfloor"].pop("deadlinePolicy")
        metadata["policy"]["deadlinePolicy"] = "keep"
        self.assertEqual(measurement.validate_metadata(metadata, request, "200000 100000", [0, 1], "0-1")["gpuUUID"], "GPU-actual")
        with self.assertRaisesRegex(measurement.common.ContractError, "actual initialized batches"):
            measurement.validate_metadata(metadata, request, "200000 100000", [0, 1, 2], "0-2")
        for changes in ({"providers": ["CPUExecutionProvider"]}, {"device": "cpu"},
                        {"gpuUUID": "MIG-actual"}, {"gpuUUID": "GPU-other"}, {"synthetic": True},
                        {"initializedBatches": [1, 4]}, {"cpuAffinity": [0]}, {"intraOpThreads": -1},
                        {"cgroupCPUMax": "100000 100000"}, {"modelFiles": ["/model/vision.onnx", "/model/decoder.onnx"]},
                        {"modelFiles": ["/model/vision.onnx"]}, {"inputs": ["question", "image"]}, {"outputs": ["label"]},
                        {"warmupIterations": 0}, {"availableProviders": []}):
            with self.subTest(changes=changes), self.assertRaises(measurement.common.ContractError):
                measurement.validate_metadata(dict(metadata, runtime=dict(actual, **changes)), request, "200000 100000", [0, 1], "0-1")
        cpu_request = copy.deepcopy(request)
        cpu_request["runConfig"]["state"]["backend"] = "ort-cpu"
        cpu_actual = dict(actual, runtime="ort-cpu", device="cpu", gpuUUID="",
                          providers=["CPUExecutionProvider"], availableProviders=["CPUExecutionProvider"])
        measurement.validate_metadata(dict(metadata, runtime=cpu_actual), cpu_request, "200000 100000", [0, 1], "0-1")
        with self.assertRaises(measurement.common.ContractError):
            measurement.validate_metadata(dict(metadata, runtime=dict(cpu_actual, providers=["CPUExecutionProvider", "CUDAExecutionProvider"])),
                                          cpu_request, "200000 100000", [0, 1], "0-1")

    def test_rknn_actual_host_facts_must_match_declarations(self):
        delivery = {"runtimePackages": [{"name": "rknn-toolkit-lite2", "version": "2.3.2"}],
                    "hostContract": {"libraries": [{"name": "rknnrt", "containerPath": "/usr/lib/librknnrt.so"}],
                                     "driver": {"name": "rknpu", "versionPath": "/sys/kernel/debug/rknpu/version", "version": "0.9.8"},
                                     "platform": {"compatiblePath": "/proc/device-tree/compatible", "compatibleToken": "rockchip,rk3588"},
                                     "devices": [{"path": "/dev/dri/renderD128"}]}}
        facts = {"package": delivery["runtimePackages"][0], "hostLibraries": [{"name": "rknnrt", "path": "/usr/lib/librknnrt.so"}],
                 "driver": delivery["hostContract"]["driver"], "platform": dict(delivery["hostContract"]["platform"], matched=True),
                 "devices": [{"path": "/dev/dri/renderD128", "characterDevice": True, "opened": True}],
                 "coreMask": "NPU_CORE_0", "inputLayout": "NHWC", "initSucceeded": True, "loadSucceeded": True}
        measurement.validate_rknn_facts(facts, delivery)
        for key in facts:
            with self.subTest(key=key), self.assertRaises(measurement.common.ContractError):
                measurement.validate_rknn_facts(dict(facts, **{key: None}), delivery)


class RuntimeFailureTest(unittest.TestCase):
    def test_native_http_failure_and_malformed_result_keep_distinct_terminal_causes(self):
        import http.server
        import threading

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                allocation = self.path in ("/allocation-failure", "/allocation-incomplete")
                self.send_response(500 if allocation or self.path == "/execute-failure" else 200)
                self.end_headers()
                if allocation:
                    self.wfile.write(json.dumps({"error": "original allocator diagnostic",
                                                "failureReason": "MemoryAllocationFailed",
                                                "executionComplete": self.path == "/allocation-failure"}).encode())
                else:
                    self.wfile.write(b"native execution failed" if self.path == "/execute-failure" else b"{malformed")

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        for path, expected_stage, expected_cause in (
                ("/execute-failure", "Execution", "ExecutionFailed"),
                ("/allocation-failure", "Execution", "MemoryAllocationFailed"),
                ("/allocation-incomplete", "Execution", "ExecutionFailed"),
                ("/malformed-result", "ResultValidation", "IntegrityError")):
            with self.subTest(path=path), tempfile.TemporaryDirectory() as directory:
                termination = Path(directory) / "termination-log"
                session = measurement.FirstfloorSession(server.server_address[1], {
                    "runConfig": {"state": {"model": "m"},
                                  "measurement": {"workloadMode": measurement.common.IDLE_WORKLOAD}}})
                self.addCleanup(session.connection.close)

                def execute(request, workspace):
                    measurement.record_stage("Execution")
                    try:
                        return session.json_call("GET", path)
                    except Exception as error:
                        # The common measurement loop wraps native exceptions.
                        raise measurement.common.ContractError("measurement failed") from error

                with mock.patch.dict(os.environ, {"CHILL_TERMINATION_MESSAGE_PATH": str(termination)}), \
                        mock.patch.object(measurement.common, "parse_args",
                                          return_value=SimpleNamespace(result_file=Path(directory) / "result.json")), \
                        mock.patch.object(measurement.common, "decode_request", return_value={}), \
                        mock.patch.object(measurement, "validate_request"), \
                        mock.patch.object(measurement, "execute", side_effect=execute), \
                        self.assertRaises(SystemExit):
                    measurement.main()
                report = json.loads(termination.read_text())
                self.assertEqual((report["stage"], report["failureReason"]), (expected_stage, expected_cause))
                self.assertFalse((Path(directory) / "result.json").exists())


class WaitReadyTest(unittest.TestCase):
    def test_readiness_probe_timeout_does_not_stay_on_kept_alive_socket(self):
        import http.server
        import threading
        import time

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_GET(self):
                if self.path.endswith("/slow"):
                    time.sleep(1.5)
                body = b"{}"
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        session = measurement.FirstfloorSession(server.server_address[1],
                                                {"runConfig": {"state": {"model": "m"},
                                                               "measurement": {"workloadMode": measurement.common.IDLE_WORKLOAD}}})
        measurement.wait_ready(SimpleNamespace(poll=lambda: None), session, 5.0)
        self.assertEqual(session.json_call("GET", session.path + "/slow"), {})
        session.connection.close()


if __name__ == "__main__":
    unittest.main()
