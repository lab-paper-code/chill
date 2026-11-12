from __future__ import annotations

import copy
from contextlib import ExitStack
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import measurement


PREPROCESSOR = {"name": "mobilenet-imagenet", "uid": "pre-uid", "image": "registry/pre:1", "architecture": "amd64",
                "path": "/artifact/mobilenet-nchw", "inputs": ["image"], "outputs": ["label"]}


def coordination_request(root):
    return {"podUID": "physical-pod", "scheduledNodeName": "node", "maxInputBytes": 1 << 20,
            "maxResultBytes": 1 << 20, "maxLatencySamples": 100,
            "observerReadyTimeoutMilliseconds": 1000, "observerReadyPaths": [str(root / "observer")],
            "workloadWindowStartPath": str(root / "window"), "workloadWindowReadyPath": str(root / "ready"),
            "measurementStartPath": str(root / "start"), "measurementEndPath": str(root / "end"),
            "runConfig": {"state": {"model": "model", "runtimeFamily": "firstfloor", "backend": "ort-cpu",
                                    "firstfloor": {"maxBatchSize": 2, "maxQueueSize": 64},
                                    "preprocessor": dict(PREPROCESSOR)},
                          "actor": {"model": "model", "maxQueueRequests": 64, "runnerCommand": ["runner"]},
                          "delivery": {}, "targetNode": {"name": "node", "uid": "node-uid"},
                          "measurement": {"batch": 2, "warmupIterations": 0, "durationSeconds": 30,
                                          "workloadMode": measurement.common.SUPPORTED_WORKLOAD,
                                          "sloPercentile": 95, "sloTargetLatencyNanos": 1_000_000_000,
                                          "minServiceSamples": 2,
                                          "operatingPoints": {"utilizations": [.5], "sparseMeanGapMilliseconds": 1000,
                                                              "phaseDurationSeconds": 30, "requestLifetimeSeconds": 60,
                                                              "deliveryMarginMilliseconds": 10,
                                                              "framedDecodedRequestUpperBytes": 1024}}}}


def phase_results(plan):
    phases = []
    batch_id = 0
    for index, (phase_id, kind) in enumerate((("sparse", "sparse"), ("saturation", "saturation"), ("utilization-0", "utilization"))):
        phase = {"id": phase_id, "kind": kind,
                 "measurementStartedAt": f"2026-11-13T00:{index:02d}:00Z",
                 "measurementEndedAt": f"2026-11-13T00:{index:02d}:30Z",
                 "measurementElapsedSecondsMonotonic": 30, "completedInputs": 4,
                 "requests": [], "nativeSamples": [], "latencyMs": [5, 5]}
        if kind == "saturation":
            phase["workers"] = 4
        elif kind == "sparse":
            phase["arrivalRatePerSecond"] = 1
        else:
            phase.update(utilization=.5, arrivalRatePerSecond=.5 * (4 / 30))
        if kind != "saturation":
            phase.update(achievedArrivalRatePerSecond=4 / 20, issueElapsedSecondsMonotonic=20,
                         issueLagToleranceMs=10,
                         issueLagMs={"count": 4, "min": 0, "mean": 0, "p50": 0, "p95": 0, "p99": 0, "max": 0})
        for offset in range(2):
            batch_id += 1
            ids = [f"{phase_id}-{offset}-{item}" for item in range(2)]
            pair = {"serviceDurationNanos": 2_000_000, "precedingIdleNanos": 123, "trialID": plan["podUID"]}
            for item, request_id in enumerate(ids):
                phase["requests"].append({"requestID": request_id, "recordID": f"r{item}",
                                          "arrivalAt": "2030-01-01T00:00:00.000000001Z",
                                          "sentAt": "2030-01-01T00:00:00.000000002Z",
                                          "completedAt": "2030-01-01T00:00:00.005000002Z", "latencyMs": 5,
                                          "response": {"id": request_id, "executionComplete": True,
                                                       "progress": {"requestID": request_id, "batchID": batch_id,
                                                                    "batchSize": 2, "nativeElapsedMS": 3, "units": item + 1}}})
            phase["nativeSamples"].append(dict(pair, batchID=batch_id, batchSize=2, requestIDs=ids,
                                                recordIDs=["r0", "r1"], units=[1, 2],
                                                runnerServiceMs=2, preprocessMs=.25, backendCallMs=1,
                                                postprocessMs=.25, nativeRequestMs=3,
                                                earliestEnqueuedAt=f"2026-11-13T00:{index:02d}:{offset * 2:02d}.000Z",
                                                latestEnqueuedAt=f"2026-11-13T00:{index:02d}:{offset * 2:02d}.001Z",
                                                startedAt=f"2026-11-13T00:{index:02d}:{offset * 2:02d}.002Z",
                                                finishedAt=f"2026-11-13T00:{index:02d}:{offset * 2:02d}.004Z"))
        phases.append(phase)
    return phases


class ExternalLoadConsumerTest(unittest.TestCase):
    def make_plan(self):
        request = coordination_request(Path("/coordination"))
        with mock.patch.dict(os.environ, {"POD_IP": "10.0.0.7"}):
            plan = measurement.load_plan(request)
        return request, plan

    def test_sparse_sizing_retains_native_nanoseconds_with_python38_iso_parser(self):
        _, plan = self.make_plan()
        samples = phase_results(plan)[0]["nativeSamples"]
        samples[0].update(earliestEnqueuedAt="2026-11-13T00:00:00.000000000Z",
                          startedAt="2026-11-13T00:00:00.000000001Z",
                          finishedAt="2026-11-13T00:00:00.002000001Z")
        samples[1].update(serviceDurationNanos=9_000_000,
                          earliestEnqueuedAt="2026-11-13T00:00:00.002000000Z",
                          startedAt="2026-11-13T00:00:00.002000002Z",
                          finishedAt="2026-11-13T00:00:00.011000002Z")
        datetime = measurement.datetime

        def microsecond_isoformat(value):
            fraction = value.split(".", 1)[1].split("+", 1)[0]
            if len(fraction) != 6:
                raise ValueError("Python 3.8 requires a supported fractional precision")
            return datetime.fromisoformat(value)

        with mock.patch.object(measurement, "datetime") as parser:
            parser.fromisoformat.side_effect = microsecond_isoformat
            for digits in range(1, 10):
                fraction = "123456789"[:digits]
                for timezone in ("Z", "+00:00"):
                    parsed, nanos = measurement.utc_timestamp("2026-11-13T00:00:00." + fraction + timezone, "native enqueue")
                    self.assertEqual(parsed.microsecond * 1000 + nanos, int(fraction.ljust(9, "0")))
            before = measurement.utc_timestamp("2026-11-13T00:00:00.123456789Z", "native enqueue")
            after = measurement.utc_timestamp("2026-11-13T00:00:00.123456790Z", "native start")
            self.assertLess(before, after)
            self.assertEqual(measurement.sparse_max_seconds(samples, plan), .002)
            samples[1]["earliestEnqueuedAt"] = samples[0]["finishedAt"]
            self.assertEqual(measurement.sparse_max_seconds(samples, plan), .009)

    def test_frozen_plan_reaches_other_node_without_local_dataset_or_profile_driver(self):
        request, plan = self.make_plan()
        self.assertEqual(plan["nativeEndpoint"], "http://10.0.0.7:8080")
        self.assertEqual(plan["inferPath"], "/v2/models/model/infer")
        self.assertEqual(plan["fields"], ["image"])
        self.assertNotIn("workers", plan)
        self.assertNotIn("maxBatchServiceSeconds", plan["operatingPoints"])
        self.assertEqual(plan["maxQueueRequests"], 64)
        self.assertFalse(hasattr(measurement.FirstfloorSession, "run"))
        self.assertFalse(hasattr(measurement.FirstfloorSession, "prepare_next"))
        self.assertFalse(hasattr(measurement.common, "execute_loaded"))

    def test_phase_reader_accepts_distinct_host_clocks_and_rejects_incomplete_evidence(self):
        _, plan = self.make_plan()
        phases = phase_results(plan)
        measurement.validate_phases(phases, plan, {"workers": 4})
        for mutate in (
                lambda p: p.reverse(),
                lambda p: p[0].update(measurementElapsedSecondsMonotonic=29),
                lambda p: p[0]["nativeSamples"][0].update(precedingIdleNanos=None),
                lambda p: p[1].update(workers=3),
                lambda p: p[2].update(arrivalRatePerSecond=1),
                lambda p: p[0]["requests"].pop(),
                lambda p: p[0]["nativeSamples"][0].update(recordIDs=["wrong", "r1"])):
            changed = copy.deepcopy(phases)
            mutate(changed)
            with self.assertRaises(measurement.common.ContractError):
                measurement.validate_phases(changed, plan, {"workers": 4})

    def test_compact_membership_rejects_missing_duplicate_cross_batch_and_repeated_proof(self):
        _, plan = self.make_plan()
        for mutate in (
                lambda p: p[0]["nativeSamples"][0].pop("requestIDs"),
                lambda p: p[0]["nativeSamples"][0].update(requestIDs=["sparse-0-0", "sparse-0-0"]),
                lambda p: p[0]["nativeSamples"][0].update(requestIDs=["sparse-0-0", "sparse-1-0"]),
                lambda p: p[0]["nativeSamples"][0].update(units=[2, 1]),
                lambda p: p[0]["requests"][0]["response"]["progress"].update(batchRequestIDs=["sparse-0-0", "sparse-0-1"]),
                lambda p: p[0]["requests"][0]["response"]["progress"].update(serviceSample={})):
            with self.subTest(mutate=mutate):
                phases = phase_results(plan)
                mutate(phases)
                with self.assertRaises(measurement.common.ContractError):
                    measurement.validate_phases(phases, plan, {"workers": 4})

    def test_poisson_issue_evidence_requires_finite_complete_timely_sends(self):
        _, plan = self.make_plan()
        for mutate in (
                lambda p: p[0].pop("issueLagMs"),
                lambda p: p[0]["issueLagMs"].update(count=3),
                lambda p: p[0]["issueLagMs"].update(max=10.001),
                lambda p: p[0]["issueLagMs"].update(mean=float("nan")),
                lambda p: p[0]["issueLagMs"].update(min=-1),
                lambda p: p[0]["issueLagMs"].update(p95=2, p99=1, max=3),
                lambda p: p[0].update(issueElapsedSecondsMonotonic=0),
                lambda p: p[0].update(issueElapsedSecondsMonotonic=31),
                lambda p: p[0].update(achievedArrivalRatePerSecond=1),
                lambda p: p[0].update(issueLagToleranceMs=11),
                lambda p: p[1].update(issueLagToleranceMs=10)):
            with self.subTest(mutate=mutate):
                phases = phase_results(plan)
                mutate(phases)
                with self.assertRaises(measurement.common.ContractError):
                    measurement.validate_phases(phases, plan, {"workers": 4})

    def test_sparse_sizing_uses_only_no_backlog_observations_and_shared_go_formula(self):
        _, plan = self.make_plan()
        samples = phase_results(plan)[0]["nativeSamples"]
        samples[1]["serviceDurationNanos"] = 9_000_000
        samples[1]["earliestEnqueuedAt"] = samples[0]["startedAt"]
        self.assertEqual(measurement.sparse_max_seconds(samples, plan), .002)
        completed = SimpleNamespace(returncode=0, stdout=json.dumps({"workers": 4, "maxBatchServiceSeconds": .002}), stderr="")
        with mock.patch.object(measurement.subprocess, "run", return_value=completed) as sizing:
            self.assertEqual(measurement.size_supply(plan, samples)["workers"], 4)
        self.assertEqual(sizing.call_args.args[0], ["/opt/chill/bin/serving-worker", "--size-saturation"])
        frozen = json.loads(sizing.call_args.kwargs["input"])
        self.assertEqual(frozen["maxBatchServiceSeconds"], .002)
        self.assertEqual(frozen["maxQueueRequests"], 64)
        with self.assertRaises(measurement.common.ContractError):
            measurement.sparse_max_seconds([], plan)

    def test_active_worker_waits_for_upload_and_keeps_observer_memory_handoff(self):
        request, plan = self.make_plan()
        phases = phase_results(plan)
        session = mock.Mock()
        session.counts.return_value = {"acceptedRequests": 0, "completedRequests": 0, "executions": 0}
        control = mock.Mock()
        def upload(process):
            control.final_observation = control.finalize_measurement()
            return {"phases": phases, "supplySizing": {"workers": 4}}

        control.wait_result.side_effect = upload
        sampler = mock.Mock()
        sampler.stop.return_value = {"memoryBytes": 123, "memorySource": "observed"}
        with ExitStack() as stack:
            stack.enter_context(mock.patch.object(measurement.common, "wait_for_signal"))
            publish = stack.enter_context(mock.patch.object(measurement.common, "publish_immutable"))
            stack.enter_context(mock.patch.object(measurement.common, "cpu_stat", return_value={}))
            stack.enter_context(mock.patch.object(measurement.time, "monotonic", side_effect=[10, 110]))
            result = measurement.execute_active(request, mock.Mock(), session, control, {"availableProviders": ["CPUExecutionProvider"]},
                                                "amd64", "max 100000", [0], "0", sampler)
        self.assertEqual(result["schemaVersion"], "chill.dacs.io/batch-characterization.v1alpha12")
        self.assertEqual(result["inferenceCount"], 6)
        self.assertEqual(result["phases"], phases)
        self.assertNotIn("repetitionResults", result)
        self.assertEqual(result["memoryBytes"], 123)
        sampler.start.assert_called_once_with()
        sampler.stop.assert_called_once_with()
        control.ready.set.assert_called_once_with()
        self.assertEqual([call.args[0] for call in publish.call_args_list],
                         [Path(request[key]) for key in ("workloadWindowStartPath", "measurementStartPath", "measurementEndPath")])
        session.run.assert_not_called()

    def test_last_phase_freezes_sampled_or_kernel_memory_before_upload_decode(self):
        import http.client

        for sampled in (True, False):
            with self.subTest(sampled=sampled):
                request, plan = self.make_plan()
                phases = phase_results(plan)
                events = []
                with mock.patch.dict(os.environ, {"POD_IP": "10.0.0.7"}):
                    control = measurement.LoadControl(request, ("127.0.0.1", 0))
                try:
                    control.phase_windows = [{key: phase[key] for key in
                                              ("id", "measurementStartedAt", "measurementEndedAt")} for phase in phases]
                    control.phase_windows[-1].pop("measurementEndedAt")
                    control.supply_sizing = {"workers": 4, "maxBatchServiceSeconds": .002}
                    sampler = mock.Mock() if sampled else None
                    if sampler is not None:
                        sampler.stop.side_effect = lambda: events.append("memory") or {
                            "memoryBytes": 123, "memorySource": "sampled"}
                    session, process = mock.Mock(), mock.Mock()
                    process.poll.return_value = None
                    original_wait = control.wait_result
                    original_loads = json.loads

                    def decode(payload, **kwargs):
                        if isinstance(payload, bytes) and payload.startswith(b'{"status"'):
                            self.assertIsNotNone(control.final_observation)
                            self.assertIn("end", events)
                            self.assertEqual(events[-1], "cpu")
                            events.append("decode")
                        return original_loads(payload, **kwargs)

                    def upload(process):
                        client = http.client.HTTPConnection("127.0.0.1", control.server.server_port, timeout=2)
                        try:
                            client.request("POST", "/measurement/phase",
                                           json.dumps({"id": "utilization-0", "action": "end"}))
                            response = client.getresponse()
                            self.assertEqual(response.status, 200, response.read())
                            self.assertEqual(events[-3:], ["memory", "end", "cpu"])
                            body = json.dumps({"status": "Succeeded", "phases": phases,
                                               "supplySizing": control.supply_sizing})
                            client.request("POST", "/measurement/load-result", body)
                            response = client.getresponse()
                            self.assertEqual(response.status, 200, response.read())
                        finally:
                            client.close()
                        return original_wait(process)

                    def publish(path, body):
                        if path == Path(request["measurementEndPath"]):
                            events.append("end")

                    with ExitStack() as stack:
                        stack.enter_context(mock.patch.object(measurement.common, "wait_for_signal"))
                        stack.enter_context(mock.patch.object(measurement.common, "publish_immutable", side_effect=publish))
                        stack.enter_context(mock.patch.object(measurement.common, "now", return_value=phases[-1]["measurementEndedAt"]))
                        stack.enter_context(mock.patch.object(measurement.common, "cpu_stat", side_effect=lambda: events.append("cpu") or {}))
                        kernel = stack.enter_context(mock.patch.object(measurement, "memory_peak",
                                                     side_effect=lambda: events.append("memory") or (123, "kernel")))
                        stack.enter_context(mock.patch.object(measurement.json, "loads", side_effect=decode))
                        stack.enter_context(mock.patch.object(control, "wait_result", side_effect=upload))
                        result = measurement.execute_active(request, process, session, control, {}, "amd64",
                                                            "max 100000", [0], "0", sampler)
                    self.assertEqual(result["memoryBytes"], 123)
                    self.assertEqual(result["measurementEndedAt"], phases[-1]["measurementEndedAt"])
                    self.assertEqual(events[-1], "decode")
                    if sampled:
                        sampler.stop.assert_called_once_with()
                        kernel.assert_not_called()
                    else:
                        kernel.assert_called_once_with()
                finally:
                    control.close()

    def test_control_failure_preserves_phase_issue_summary(self):
        import http.client

        request, _ = self.make_plan()
        with mock.patch.dict(os.environ, {"POD_IP": "10.0.0.7"}):
            control = measurement.LoadControl(request, ("127.0.0.1", 0))
        self.addCleanup(control.close)
        control.ready.set()
        issue = {"id": "sparse", "kind": "sparse", "arrivalRatePerSecond": 1,
                 "issueElapsedSecondsMonotonic": 0, "achievedArrivalRatePerSecond": 0,
                 "issueLagToleranceMs": 10,
                 "issueLagMs": {"count": 1, "min": 11, "mean": 11, "p50": 11, "p95": 11, "p99": 11, "max": 11}}
        client = http.client.HTTPConnection("127.0.0.1", control.server.server_port, timeout=2)
        try:
            client.request("POST", "/measurement/load-result", json.dumps(
                {"status": "Failed", "error": "Poisson issue lag exceeds schedule tolerance 10ms", "issueEvidence": issue}))
            response = client.getresponse()
            self.assertEqual(response.status, 200, response.read())
        finally:
            client.close()
        with self.assertRaisesRegex(measurement.ExecutionError, "issueEvidence=") as error:
            control.wait_result(mock.Mock())
        self.assertEqual(error.exception.issue_evidence, issue)
        self.assertEqual(error.exception.failure_reason, "ExecutionFailed")

    def test_control_readiness_and_partial_upload_have_finite_timeouts(self):
        import http.client

        request, _ = self.make_plan()
        request["runConfig"]["measurement"]["operatingPoints"]["requestLifetimeSeconds"] = .05
        with mock.patch.dict(os.environ, {"POD_IP": "10.0.0.7"}):
            control = measurement.LoadControl(request, ("127.0.0.1", 0))
        self.addCleanup(control.close)
        client = http.client.HTTPConnection("127.0.0.1", control.server.server_port, timeout=2)
        try:
            with mock.patch.object(control.ready, "wait", return_value=False) as ready:
                client.request("GET", "/measurement/load")
                response = client.getresponse()
                self.assertEqual(response.status, 503, response.read())
            ready.assert_called_once_with(4)
            control.ready.set()
            client.request("POST", "/measurement/load-result", b"{", {"Content-Length": "100"})
            response = client.getresponse()
            self.assertEqual(response.status, 400, response.read())
            self.assertTrue(control.finished.is_set())
        finally:
            client.close()

    def test_active_sampled_execution_keeps_frozen_kernel_peak_after_upload(self):
        with tempfile.TemporaryDirectory() as directory:
            request = coordination_request(Path(directory))
            zero = {"acceptedRequests": 0, "completedRequests": 0, "executions": 0}
            session, process = mock.Mock(), mock.Mock()
            session.baseline = zero
            session.counts.return_value = {"acceptedRequests": 4, "completedRequests": 4, "executions": 2}
            process.poll.return_value = None
            frozen = {"inferenceCount": 2, "cpuContract": {}, "memoryBytes": 123, "memorySource": "frozen"}
            with ExitStack() as stack:
                stack.enter_context(mock.patch.object(measurement, "FirstfloorSession", return_value=session))
                stack.enter_context(mock.patch.object(measurement, "LoadControl"))
                stack.enter_context(mock.patch.object(measurement.subprocess, "Popen", return_value=process))
                stack.enter_context(mock.patch.object(measurement, "wait_ready"))
                stack.enter_context(mock.patch.object(measurement, "validate_metadata",
                                                     return_value={"intraOpThreads": 1, "interOpThreads": 1}))
                stack.enter_context(mock.patch.object(measurement, "execute_active", return_value=frozen))
                kernel = stack.enter_context(mock.patch.object(measurement, "memory_peak"))
                result = measurement.execute_sampled(request, Path(directory), request["runConfig"],
                                                     "amd64", "max 100000", [0], "0", None)
            self.assertEqual(result["memoryBytes"], 123)
            self.assertEqual(result["memorySource"], "frozen")
            kernel.assert_not_called()

    def test_idle_counts_cover_settling_and_window_and_native_listener_is_network_bound(self):
        for changed in (None, "acceptedRequests", "completedRequests", "executions"):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as directory:
                request = coordination_request(Path(directory))
                request["runConfig"]["measurement"]["workloadMode"] = measurement.common.IDLE_WORKLOAD
                baseline = {"acceptedRequests": 0, "completedRequests": 0, "executions": 0}
                final = dict(baseline)
                if changed:
                    final[changed] = 1
                session = mock.Mock()
                session.counts.side_effect = [baseline, final]
                process = mock.Mock()
                process.poll.return_value = None
                with ExitStack() as stack:
                    stack.enter_context(mock.patch.object(measurement, "FirstfloorSession", return_value=session))
                    control = stack.enter_context(mock.patch.object(measurement, "LoadControl"))
                    popen = stack.enter_context(mock.patch.object(measurement.subprocess, "Popen", return_value=process))
                    stack.enter_context(mock.patch.object(measurement, "wait_ready"))
                    stack.enter_context(mock.patch.object(measurement, "validate_metadata", return_value={"intraOpThreads": 1, "interOpThreads": 1}))
                    stack.enter_context(mock.patch.object(measurement.common, "execute_idle", return_value={"cpuContract": {}}))
                    stack.enter_context(mock.patch.object(measurement, "memory_peak", return_value=(1024, "observed")))
                    if changed:
                        with self.assertRaisesRegex(measurement.common.ContractError, "counters changed across idle"):
                            measurement.execute_sampled(request, Path(directory), request["runConfig"], "amd64", "max 100000", [0], "0", None)
                    else:
                        result = measurement.execute_sampled(request, Path(directory), request["runConfig"], "amd64", "max 100000", [0], "0", None)
                        self.assertEqual(result["firstfloor"]["inferenceCount"], 0)
                self.assertIn("0.0.0.0:8080", popen.call_args.args[0])
                control.assert_not_called()

    def test_control_get_waits_and_phase_timestamps_come_from_measurement_node(self):
        import http.client
        import threading

        request, _ = self.make_plan()
        with mock.patch.dict(os.environ, {"POD_IP": "10.0.0.7"}):
            control = measurement.LoadControl(request, ("127.0.0.1", 0))
        self.addCleanup(control.close)
        entered, delivered = threading.Event(), threading.Event()
        values = []

        def fetch():
            client = http.client.HTTPConnection("127.0.0.1", control.server.server_port, timeout=2)
            entered.set()
            try:
                client.request("GET", "/measurement/load")
                values.append(json.loads(client.getresponse().read()))
                delivered.set()
            finally:
                client.close()

        thread = threading.Thread(target=fetch)
        thread.start()
        self.assertTrue(entered.wait(1))
        self.assertFalse(delivered.wait(.02))
        control.ready.set()
        thread.join(2)
        self.assertTrue(delivered.is_set())
        self.assertEqual(values[0]["nativeEndpoint"], "http://10.0.0.7:8080")
        client = http.client.HTTPConnection("127.0.0.1", control.server.server_port, timeout=2)
        with mock.patch.object(measurement.common, "now", return_value="2026-11-13T00:00:00.123456Z"):
            client.request("POST", "/measurement/phase", json.dumps({"id": "sparse", "action": "start"}))
            response = client.getresponse()
            self.assertEqual(response.status, 200)
            self.assertEqual(json.loads(response.read()), {"at": "2026-11-13T00:00:00.123456Z"})
        client.close()


class MeasurementBoundaryTest(unittest.TestCase):
    def test_selected_deadline_policy_defaults_validates_and_reaches_worker(self):
        actor = {"model": "model", "inputs": ["image"], "outputs": ["label"],
                 "batchSizes": [1, 2, 3, 4], "warmupBatchSizes": [1, 4],
                 "runnerCommand": ["python3", "/opt/chill/runtime/firstfloor/runner.py",
                                   "--runtime", "ort-cpu", "--model-dir", "/model",
                                   "--preprocessor", "/preprocessor", "--warmup-batch", "1",
                                   "--warmup-batch", "4"]}
        request = {"maxInputBytes": 1024, "observerReadyTimeoutMilliseconds": 1000,
                   "runConfig": {"actor": actor, "state": {"firstfloor": {}}, "measurement": {"batch": 4}}}
        settings = request["runConfig"]["state"]["firstfloor"]
        config = measurement.worker_config(request)
        self.assertEqual(config["policy"]["deadlinePolicy"], "keep")
        self.assertEqual(config["policy"]["fixedBatchSize"], 4)
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
                    "policy": {"deadlinePolicy": "keep", "fixedBatchSize": 4},
                    "runtime": actual}
        request["runConfig"]["delivery"]["runtimePackages"] = [
            {"name": "onnxruntime-gpu", "version": "1.21.0"}, {"name": "numpy", "version": "2.2.0"}]
        self.assertEqual(measurement.validate_metadata(metadata, request, "200000 100000", [0, 1], "0-1")["gpuUUID"], "GPU-actual")
        actual.pop("runtimeVersion")
        actual.pop("numpyVersion")
        measurement.validate_metadata(metadata, request, "200000 100000", [0, 1], "0-1")
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
