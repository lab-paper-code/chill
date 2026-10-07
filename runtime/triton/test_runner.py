import tempfile
import unittest
from pathlib import Path

import runner
import serve


class NativeEvidenceTest(unittest.TestCase):
    def test_completed_native_trace_retains_queue_entry_and_missing_compute(self):
        entries = [
            {"id": 1, "request_id": "request~uid/2/model/1"},
            {"id": 1, "timestamps": [{"name": "QUEUE_START", "ns": 100}]},
            {"id": 1, "timestamps": [{"name": "COMPUTE_START", "ns": 140}]},
        ]
        records = list(serve.queue_records(entries))
        self.assertEqual(records[0]["definition"], "uid/2/model/1")
        self.assertEqual((records[0]["queueStartNanos"], records[0]["computeStartNanos"]), ("100", "140"))
        entries.pop()
        self.assertEqual(list(serve.queue_records(entries))[0]["computeStartNanos"], "0")
        entries.append({"id": 1, "timestamps": [{"name": "COMPUTE_START", "ns": 99}]})
        with self.assertRaises(ValueError):
            list(serve.queue_records(entries))

    def test_native_request_release_finishes_rejected_requests_without_execution(self):
        entries = [
            {"id": 1, "request_id": "expired~uid/2/model/1"},
            {"id": 1, "timestamps": [{"name": "QUEUE_START", "ns": 100},
                                    {"name": "REQUEST_END", "ns": 150}]},
        ]
        records = list(serve.queue_records(entries))
        self.assertEqual(records[0]["computeStartNanos"], "0")
        self.assertEqual(records[1]["phase"], "finished")
        self.assertEqual(records[1]["requestID"], "expired~uid/2/model/1")
        entries[1]["timestamps"].pop(0)
        self.assertEqual([record["phase"] for record in serve.queue_records(entries)], ["finished"])

    def test_unrelated_native_trace_does_not_hide_admitted_request_release(self):
        entries = [
            {"id": 1, "request_id": "ordinary-native-id"},
            {"id": 1, "timestamps": [{"name": "QUEUE_START", "ns": 100},
                                    {"name": "REQUEST_END", "ns": 150}]},
            {"id": 2, "request_id": "accepted~uid/2/model/1"},
            {"id": 2, "timestamps": [{"name": "REQUEST_END", "ns": 160}]},
        ]
        self.assertEqual([(record["requestID"], record["phase"]) for record in serve.queue_records(entries)],
                         [("accepted~uid/2/model/1", "finished")])

    def test_native_statistics_exclude_warmup_but_reject_mixed_batches(self):
        before = {"execution_count": "5", "inference_count": "5",
                  "batch_stats": [{"batch_size": "1", "compute_infer": {"count": "5"}}]}
        after = {"execution_count": "7", "inference_count": "9",
                 "batch_stats": [{"batch_size": "1", "compute_infer": {"count": "5"}},
                                 {"batch_size": "2", "compute_infer": {"count": "2"}}]}
        result = runner.statistics_delta(before, after, 2, 2)
        self.assertEqual(result["executionCount"], 2)
        self.assertEqual(result["inferenceCount"], 4)
        # Same item count, but four serial requests are not two native batches K=2.
        after["execution_count"] = "9"
        after["batch_stats"] = [{"batch_size": "1", "compute_infer": {"count": "9"}}]
        with self.assertRaises(runner.common.ContractError):
            runner.statistics_delta(before, after, 2, 2)
        with self.assertRaises(runner.common.ContractError):
            runner.statistics_delta(before, after, 1, 0)
        del after["execution_count"]
        with self.assertRaises(KeyError):
            runner.statistics_delta(before, after, 1, 0)

    def test_kernel_memory_peak_is_required_not_artifact_or_process_size(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(runner.common.ContractError):
                runner.memory_peak(root)
            (root / "memory.peak").write_text("123456\n")
            self.assertEqual(runner.memory_peak(root), (123456, "cgroup-v2-memory.peak:runtime-container-lifetime"))
            for invalid in ("0", "-1", "max", ""):
                (root / "memory.peak").write_text(invalid)
                with self.assertRaises(runner.common.ContractError):
                    runner.memory_peak(root)
            (root / "memory.peak").unlink()
            (root / "memory").mkdir()
            peak = root / "memory" / "memory.max_usage_in_bytes"
            peak.write_text("654321\n")
            self.assertEqual(runner.memory_peak(root), (654321, "cgroup-v1-memory.max_usage_in_bytes:runtime-container-lifetime"))
            peak.write_text("1" * 4097)
            with self.assertRaises(runner.common.ContractError):
                runner.memory_peak(root)

    def test_v1_cpu_reader_preserves_exact_quota_and_rejects_unlimited(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cpu, cpuset = root / "cpu", root / "cpuset"
            cpu.mkdir()
            cpuset.mkdir()
            (cpu / "cpu.cfs_quota_us").write_text("400000\n")
            (cpu / "cpu.cfs_period_us").write_text("100000\n")
            (cpuset / "cpuset.cpus").write_text("0-3\n")
            quota, affinity, effective = runner.common.read_cpu_environment(root)
            self.assertEqual((quota, effective), ("400000 100000", "0-3"))
            self.assertGreater(len(affinity), 0)
            (cpuset / "cpuset.effective_cpus").write_text("1-2\n")
            self.assertEqual(runner.common.read_cpu_environment(root)[2], "1-2")
            (cpu / "cpu.cfs_quota_us").write_text("-1\n")
            with self.assertRaises(runner.common.ContractError):
                runner.common.read_cpu_environment(root)


if __name__ == "__main__":
    unittest.main()
