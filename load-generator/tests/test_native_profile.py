import base64
import json
import threading
import time
import unittest
from unittest import mock

from chill_loadgen import cli, native_profile


def plan():
    return {"nativeEndpoint": "http://127.0.0.1:8080", "inferPath": "/v2/models/model/infer",
            "model": "model", "fields": ["image"], "batch": 2, "podUID": "physical-pod",
            "sloPercentile": 95, "sloTargetLatencyNanos": 1_000_000_000,
            "workloadResourceVersion": "1", "minServiceSamples": 2,
            "maxQueueRequests": 64, "maxQueueBytes": 1 << 20,
            "maxLatencySamples": 100, "maxResultBytes": 1 << 20,
            "operatingPoints": {"utilizations": [.25, .75], "sparseMeanGapMilliseconds": 2000,
                                "phaseDurationSeconds": 30, "requestLifetimeSeconds": 60,
                                "deliveryMarginMilliseconds": 10, "framedDecodedRequestUpperBytes": 1024}}


def row(request_id, record_id, units):
    progress = {"requestID": request_id, "batchID": 7, "batchSize": 2,
                "batchRequestIDs": ["a", "b"], "batchStartedAt": "2026-11-13T00:00:00.100000001Z",
                "batchFinishedAt": "2026-11-13T00:00:00.102000002Z",
                "serviceSample": {"serviceDurationNanos": 2_000_001, "precedingIdleNanos": 123,
                                  "trialID": "physical-pod"},
                "preprocessMS": .25, "backendMS": 1, "postprocessMS": .25,
                "nativeElapsedMS": 4, "units": units,
                "nativeHTTP": {"handlerEnteredAt": "2026-11-13T00:00:00.000000001Z", "enqueueNanos": 17}}
    return {"requestID": request_id, "recordID": record_id, "arrivalAt": "2026-11-13T00:00:00Z",
            "sentAt": "2026-11-13T00:00:00Z", "completedAt": "2026-11-13T00:00:00.005Z",
            "latencyMs": 5, "response": {"id": request_id, "executionComplete": True, "progress": progress}}


class NativeProfileTest(unittest.TestCase):
    def test_batch_membership_deduplicates_and_retains_actual_spans(self):
        samples, latency = native_profile.native_samples([row("b", "r2", 3), row("a", "r1", 1)], 2, "physical-pod")
        self.assertEqual(len(samples), 1)
        self.assertEqual(samples[0]["recordIDs"], ["r1", "r2"])
        self.assertEqual(samples[0]["units"], [1, 3])
        self.assertEqual(samples[0]["serviceDurationNanos"], 2_000_001)
        self.assertEqual(samples[0]["latestEnqueuedAt"], "2026-11-13T00:00:00.000000018Z")
        self.assertEqual(samples[0]["earliestEnqueuedAt"], "2026-11-13T00:00:00.000000018Z")
        self.assertEqual(samples[0]["startedAt"], "2026-11-13T00:00:00.100000001Z")
        self.assertEqual(latency, [5])
        with self.assertRaisesRegex(ValueError, "membership"):
            native_profile.native_samples([row("a", "r1", 1)], 2, "physical-pod")
        changed = row("b", "r2", 3)
        changed["response"]["progress"]["serviceSample"]["precedingIdleNanos"] = 456
        with self.assertRaisesRegex(ValueError, "disagree"):
            native_profile.native_samples([row("a", "r1", 1), changed], 2, "physical-pod")

    def test_stop_requires_duration_known_gaps_and_complete_batch_tail(self):
        self.assertFalse(native_profile.phase_complete(29, 30, 100, 59, 10, 2))
        self.assertFalse(native_profile.phase_complete(100, 30, 58, 59, 10, 2))
        self.assertFalse(native_profile.phase_complete(100, 30, 59, 59, 11, 2))
        self.assertTrue(native_profile.phase_complete(30, 30, 59, 59, 12, 2))

    def test_plan_phases_use_measured_input_throughput(self):
        specs = native_profile.phase_specs(plan(), 120)
        self.assertEqual([phase["kind"] for phase in specs], ["utilization", "utilization"])
        self.assertEqual([phase["arrivalRatePerSecond"] for phase in specs], [30, 90])
        self.assertEqual([phase["utilization"] for phase in specs], [.25, .75])
        self.assertEqual(len({phase["id"] for phase in specs}), 2)

    def test_native_envelope_has_one_input_and_gateway_context(self):
        body = native_profile.request_body(plan(), "a", [{"name": "image", "data": [base64.b64encode(b"jpeg").decode()]}],
                                           "2026-11-13T00:00:00Z", 3)
        body = json.loads(body)
        self.assertEqual(body["id"], body["context"]["requestID"])
        self.assertEqual(body["context"]["workloadUID"], "physical-pod")
        self.assertEqual(body["context"]["admissionPolicy"], "Delay")
        self.assertEqual(body["context"]["senderElapsedMS"], 3)
        self.assertEqual(body["inputs"], [{"name": "image", "data": ["anBlZw=="]}])
        self.assertNotIn("batchSize", body)
        self.assertNotIn("trialID", body)

    def test_native_profile_cli_fetches_plan_and_uploads_failure(self):
        with mock.patch.object(native_profile, "control_call", side_effect=[plan(), {}]) as control, \
                mock.patch.object(native_profile, "run_profile", side_effect=ValueError("bad native evidence")), \
                mock.patch.object(cli, "load_records", return_value=[("r", {"image": b"jpeg"})]), \
                self.assertRaisesRegex(ValueError, "bad native evidence"):
            native_profile.main("http://worker:8081", "/data")
        self.assertEqual(control.call_args_list[0].args[:3], ("http://worker:8081", "GET", "/measurement/load"))
        self.assertEqual(control.call_args_list[1].args[3], {"status": "Failed", "error": "bad native evidence"})

    def test_sparse_first_uses_native_sizing_and_measurement_node_windows(self):
        calls = []

        def boundary(endpoint, method, path, body):
            calls.append((path, body))
            return {"workers": 4} if path == "/measurement/supply" else {"at": f"native-{len(calls)}"}

        def phase(p, records, spec):
            return dict(spec, completedInputs=120, measurementElapsedSecondsMonotonic=30,
                        nativeSamples=[{"serviceDurationNanos": 1000}])

        with mock.patch.object(native_profile, "control_call", side_effect=boundary), \
                mock.patch.object(native_profile, "run_phase", side_effect=phase):
            result = native_profile.run_profile(plan(), [("r", {"image": b"jpeg"})], "http://worker:8081")
        self.assertEqual([phase["kind"] for phase in result["phases"]], ["sparse", "saturation", "utilization", "utilization"])
        self.assertEqual(result["phases"][0]["arrivalRatePerSecond"], .5)
        self.assertEqual(result["phases"][1]["workers"], 4)
        self.assertEqual(result["phases"][2]["arrivalRatePerSecond"], 1)
        self.assertEqual(result["phases"][3]["arrivalRatePerSecond"], 3)
        self.assertEqual(calls[2][0], "/measurement/supply")
        self.assertEqual(result["phases"][0]["measurementStartedAt"], "native-1")
        self.assertEqual(result["phases"][0]["measurementEndedAt"], "native-2")

    def test_poisson_launches_without_waiting_for_previous_response(self):
        p = plan()
        p.update(batch=1, minServiceSamples=2)
        p["operatingPoints"]["phaseDurationSeconds"] = .001
        both = threading.Event()
        lock = threading.Lock()
        sent = []

        class Client:
            def infer(self, body):
                request = json.loads(body)
                with lock:
                    sent.append(request["id"])
                    if len(sent) >= 2:
                        both.set()
                if not both.wait(2):
                    raise AssertionError("Poisson requests were serialized")
                time.sleep(.005)
                response = row(request["id"], "r", 1)["response"]
                progress = response["progress"]
                progress.update(batchID=int(request["id"].rsplit("-", 1)[1]) + 1, batchSize=1,
                                batchRequestIDs=[request["id"]])
                return response

            def close(self):
                pass

        with mock.patch.object(native_profile.random.Random, "expovariate", side_effect=[.001, .001, 60]):
            phase = native_profile.run_phase(p, [("r", [{"name": "image", "data": ["anBlZw=="]}])],
                                             {"id": "utilization-0", "kind": "utilization", "utilization": .5,
                                              "arrivalRatePerSecond": 1000}, new_client=Client)
        self.assertGreaterEqual(phase["completedInputs"], 2)
        self.assertEqual(len(phase["requests"]), len(phase["nativeSamples"]))
        self.assertLess(phase["measurementElapsedSecondsMonotonic"], 1)

    def test_saturation_refills_until_known_gap_minimum_then_drains(self):
        p = plan()
        p.update(batch=1, minServiceSamples=2)
        p["operatingPoints"]["phaseDurationSeconds"] = .001

        class Client:
            def infer(self, body):
                request = json.loads(body)
                time.sleep(.005)
                index = int(request["id"].rsplit("-", 1)[1])
                response = row(request["id"], "r", 1)["response"]
                progress = response["progress"]
                progress.update(batchID=index + 1, batchSize=1, batchRequestIDs=[request["id"]])
                progress["serviceSample"]["precedingIdleNanos"] = None if index == 0 else 123
                return response

            def close(self):
                pass

        phase = native_profile.run_phase(p, [("r", [{"name": "image", "data": ["anBlZw=="]}])],
                                         {"id": "saturation", "kind": "saturation", "workers": 1}, new_client=Client)
        self.assertEqual(phase["completedInputs"], 3)
        self.assertEqual([sample["precedingIdleNanos"] for sample in phase["nativeSamples"]], [None, 123, 123])

    def test_unrounded_saturation_population_drains_complete_fixed_batches(self):
        p = plan()
        p["operatingPoints"]["phaseDurationSeconds"] = .001
        lock = threading.Lock()
        pending, issued = [], []
        batch_count = 0

        class Client:
            def infer(self, body):
                nonlocal batch_count
                request = json.loads(body)
                entry = {"id": request["id"], "ready": threading.Event()}
                with lock:
                    issued.append(entry["id"])
                    pending.append(entry)
                    if len(pending) == 2:
                        batch_count += 1
                        ids = [item["id"] for item in pending]
                        for item in pending:
                            response = row(item["id"], "r", 1)["response"]
                            response["progress"].update(batchID=batch_count, batchRequestIDs=ids)
                            response["progress"]["serviceSample"]["precedingIdleNanos"] = None if batch_count == 1 else 123
                            item["response"] = response
                            item["ready"].set()
                        pending.clear()
                if not entry["ready"].wait(1):
                    raise AssertionError("fixed-B tail was stranded by a sender stopping early")
                time.sleep(.005)
                return entry["response"]

            def close(self):
                pass

        phase = native_profile.run_phase(p, [("r", [{"name": "image", "data": ["anBlZw=="]}])],
                                         {"id": "saturation", "kind": "saturation", "workers": 3}, new_client=Client)
        self.assertEqual(phase["workers"], 3)
        self.assertEqual(len(issued) % 2, 0)
        self.assertEqual(phase["completedInputs"], len(issued))
        self.assertEqual(phase["completedInputs"], 2 * len(phase["nativeSamples"]))
        self.assertGreaterEqual(sum(sample["precedingIdleNanos"] is not None for sample in phase["nativeSamples"]), 2)
        self.assertEqual(pending, [])


if __name__ == "__main__":
    unittest.main()
