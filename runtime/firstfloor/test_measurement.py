from __future__ import annotations

import copy
import os
from pathlib import Path
import subprocess
import sys
import unittest
from types import SimpleNamespace
from unittest import mock

import measurement


class MeasurementBoundaryTest(unittest.TestCase):
    def test_selected_deadline_policy_defaults_validates_and_reaches_worker(self):
        request = {"modelPath": "/model/model.onnx", "maxInputBytes": 1024,
                   "observerReadyTimeoutMilliseconds": 1000,
                   "runConfig": {"state": {"model": "model", "backend": "ort-cpu",
                                          "firstfloor": {"maxBatchSize": 4, "maxQueueSize": 8}},
                                 "measurement": {"batch": 1, "warmupIterations": 0},
                                 "cpu": {"ortIntraOpThreads": 1},
                                 "delivery": {"artifactCapability": {"tensors": [
                                     {"name": "x", "datatype": "FP32", "sampleShape": [3]}]}}}}
        settings = request["runConfig"]["state"]["firstfloor"]
        self.assertEqual(measurement.worker_config(request)["policy"]["deadlinePolicy"], "keep")
        for policy in ("drop", "keep", "defer"):
            settings["deadlinePolicy"] = policy
            self.assertEqual(measurement.worker_config(request)["policy"]["deadlinePolicy"], policy)
        for invalid in ("", "Drop", None, [], True):
            settings["deadlinePolicy"] = invalid
            with self.subTest(invalid=invalid), self.assertRaises(measurement.common.ContractError):
                measurement.worker_config(request)

    def test_explicit_cpu_mask_validates_actual_inventory_and_applies_exactly(self):
        common = measurement.common
        run = {"cpu": {"ortIntraOpThreads": 2, "affinityCPUs": [17, 9]}}
        self.assertEqual(common.requested_cpu_affinity(run["cpu"]), [9, 17])
        for invalid in (None, [], [9, 9], [-1, 9], [True, 9], ["9", 17], [9]):
            with self.subTest(invalid=invalid), self.assertRaises(common.ContractError):
                common.requested_cpu_affinity(dict(run["cpu"], affinityCPUs=invalid))
        with mock.patch.object(common.os, "sched_getaffinity", side_effect=[{9, 17, 31}, {9, 17}]), \
                mock.patch.object(common.os, "sched_setaffinity") as apply:
            common.apply_cpu_affinity(run)
            apply.assert_called_once_with(0, [9, 17])
        with mock.patch.object(common.os, "sched_getaffinity", return_value={9, 31}), \
                mock.patch.object(common.os, "sched_setaffinity") as apply:
            with self.assertRaisesRegex(common.ContractError, "actual allowed CPU inventory"):
                common.apply_cpu_affinity(run)
            apply.assert_not_called()
        with mock.patch.object(common, "os", SimpleNamespace()):
            with self.assertRaisesRegex(common.ContractError, "unavailable"):
                common.apply_cpu_affinity(run)
            common.apply_cpu_affinity({"cpu": {"ortIntraOpThreads": 2}})
        with mock.patch.object(common.os, "sched_getaffinity", return_value={9, 17, 31}), \
                mock.patch.object(common.os, "sched_setaffinity"):
            with self.assertRaisesRegex(common.ContractError, "differs from requested"):
                common.apply_cpu_affinity(run)

    @unittest.skipUnless(hasattr(os, "sched_getaffinity"), "Linux CPU affinity required")
    def test_explicit_cpu_mask_applies_in_real_child_only(self):
        before = os.sched_getaffinity(0)
        script = (
            "import os; import measurement; "
            "selected = [max(os.sched_getaffinity(0))]; "
            "run = {'cpu': {'ortIntraOpThreads': 1, 'affinityCPUs': selected}}; "
            "measurement.common.apply_cpu_affinity(run); "
            "assert sorted(os.sched_getaffinity(0)) == selected"
        )
        subprocess.run([sys.executable, "-c", script],
                       cwd=Path(measurement.__file__).parent, check=True)
        self.assertEqual(os.sched_getaffinity(0), before)


    def test_sparse_actual_initialized_batches_do_not_fabricate_batch_one(self):
        run = {"state": {"firstfloor": {"maxBatchSize": 8}},
               "delivery": {"artifactCapability": {"declaredBatchSizes": [2, 4, 16]}},
               "measurement": {"batch": 4}}
        self.assertEqual(measurement.initialized_batches(run), [2, 4])
        run["measurement"]["batch"] = 1
        with self.assertRaises(measurement.common.ContractError):
            measurement.initialized_batches(run)

    def test_native_samples_require_actual_aligned_nested_timings(self):
        response = {"batchSize": 4, "executions": 1, "items": 4, "executionComplete": True,
                    "batchIDs": [9], "serviceSamplesMS": [2.0], "backendSamplesMS": [1.0],
                    "nativeSamplesMS": [3.0], "nativeElapsedMS": 4.0}
        sample = measurement.native_sample(response, 4)
        self.assertEqual(sample, {"batchID": 9, "batchSize": 4, "runnerServiceMs": 2.0,
                                  "backendCallMs": 1.0, "nativeRequestMs": 3.0})
        for key in ("batchIDs", "serviceSamplesMS", "backendSamplesMS", "nativeSamplesMS"):
            for invalid in (None, [], [1, 2]):
                with self.subTest(key=key, invalid=invalid):
                    bad = dict(response, **{key: invalid})
                    with self.assertRaises(measurement.common.ContractError):
                        measurement.native_sample(bad, 4)
        for changes in ({"backendSamplesMS": [3.0]}, {"nativeSamplesMS": [1.0]},
                        {"serviceSamplesMS": [float("nan")]}, {"serviceSamplesMS": [0.0]},
                        {"batchIDs": [True]}, {"batchIDs": [1 << 64]}, {"items": 1},
                        {"batchSize": 1}, {"executions": True}, {"executionComplete": False}, {"nativeElapsedMS": 2.0}):
            with self.subTest(changes=changes), self.assertRaises(measurement.common.ContractError):
                measurement.native_sample(dict(response, **changes), 4)

    def test_actual_runtime_rejects_provider_fallback_and_changed_conditions(self):
        request = {"modelPath": "/model/model.onnx", "maxInputBytes": 1024,
                   "runConfig": {"state": {"model": "model", "backend": "ort-cuda",
                                          "firstfloor": {"maxBatchSize": 4, "maxQueueSize": 8}},
                                 "measurement": {"batch": 4, "warmupIterations": 2},
                                 "cpu": {"ortIntraOpThreads": 2, "ortInterOpThreads": 1},
                                 "delivery": {"artifactCapability": {"tensors": [{"name": "x", "datatype": "FP32", "sampleShape": [3]}],
                                                                    "declaredBatchSizes": [1, 2, 4]},
                                              "runtimeAccess": {"gpuUUID": "GPU-actual", "resources": {"nvidia.com/gpu": "1"}}}}}
        actual = {"runtime": "ort-cuda", "runtimeVersion": "1.20.0", "numpyVersion": "2.1.0",
                  "modelPath": "/model/model.onnx", "inputs": [{"name": "x", "datatype": "FP32", "shape": [3]}],
                  "outputs": [{"name": "y", "datatype": "FP32", "shape": [2]}],
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
        request["runConfig"]["cpu"]["affinityCPUs"] = [1, 0]
        self.assertEqual(measurement.validate_metadata(metadata, request, "200000 100000", [0, 1], "0-1")["gpuUUID"], "GPU-actual")
        with self.assertRaisesRegex(measurement.common.ContractError, "frozen requested"):
            measurement.validate_metadata(metadata, request, "200000 100000", [0, 1, 2], "0-2")
        for changes in ({"providers": ["CPUExecutionProvider"]}, {"device": "cpu"},
                        {"gpuUUID": "MIG-actual"}, {"gpuUUID": "GPU-other"}, {"synthetic": True},
                        {"initializedBatches": [1, 4]}, {"cpuAffinity": [0]}, {"intraOpThreads": 1},
                        {"cgroupCPUMax": "100000 100000"}, {"modelPath": "/other.onnx"},
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


if __name__ == "__main__":
    unittest.main()
