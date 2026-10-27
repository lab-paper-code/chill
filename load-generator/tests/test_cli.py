import io
import importlib.util
import json
import tempfile
import unittest
import threading
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch

from chill_loadgen.cli import arrival_offsets_ns, load_records, main, replay, replay_open_loop, shard_schedule
from chill_loadgen.http_client import InferenceResponse, WorkloadHTTPClient


class CLITest(unittest.TestCase):
    def test_empty_invocation_prints_help(self) -> None:
        output = io.StringIO()

        with redirect_stdout(output):
            exit_code = main([])

        self.assertEqual(exit_code, 0)
        self.assertIn("open-loop inference workload", output.getvalue())

    def test_replay_rejects_missing_field_before_traffic(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "q1").mkdir()
            (root / "q1" / "image").write_bytes(b"jpeg")
            (root / "token").write_text("secret\n")

            with redirect_stderr(io.StringIO()) as errors, self.assertRaises(SystemExit):
                main([
                    "replay", "--endpoint", "https://chill.example", "--token-file", str(root / "token"),
                    "--workload", "vqa", "--dataset-dir", str(root), "--fields", "image,question",
                    "--rate", "1", "--duration", "1",
                ])

        self.assertIn("record q1 has no field question", errors.getvalue())

    def test_e5_prepares_one_tls_context_before_independent_connections(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "q1").mkdir()
            (root / "q1" / "image").write_bytes(b"jpeg")
            token = root / "token"
            token.write_text("secret")
            output = io.StringIO()
            with patch("chill_loadgen.http_client.ssl.create_default_context") as context, \
                 patch("chill_loadgen.http_client.HTTPSConnection") as connection, \
                 redirect_stdout(output):
                connection.return_value.host = "chill.example"
                connection.return_value.port = 443
                connection.return_value.timeout = 30
                connection.return_value.sock = None
                response = connection.return_value.getresponse.return_value
                response.status = 200
                response.read.return_value = b"{}"
                response.getheader.return_value = None
                def schedule(requests, submit, **kwargs):
                    context.assert_called_once_with(cafile=None)
                    for request in requests:
                        submit(request, False)
                with patch("chill_loadgen.cli.run_schedule", side_effect=schedule):
                    self.assertEqual(main([
                        "replay", "--endpoint", "https://chill.example",
                        "--token-file", str(token), "--dataset-dir", str(root),
                        "--workload", "vqa", "--fields", "image", "--rate", "3",
                        "--duration", "1", "--workload-slo", ".1",
                        "--gateway-timeout", "2",
                    ]), 0)
                context.assert_called_once_with(cafile=None)
                self.assertEqual(connection.call_count, 4)
                self.assertTrue(all(call.kwargs["context"] is context.return_value
                                    for call in connection.call_args_list))
            self.assertEqual(len(output.getvalue().splitlines()), 3)


class LoadRecordsTest(unittest.TestCase):
    def test_reads_requested_fields_ordered_by_record_id(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for record in ("q2", "q1"):
                (root / record).mkdir()
                (root / record / "image").write_bytes(record.encode() + b"-jpeg")
                (root / record / "question").write_bytes(b"what?")

            records = load_records(root, ["image"])

        self.assertEqual([("q1", {"image": b"q1-jpeg"}), ("q2", {"image": b"q2-jpeg"})], records)


class FakeClient:
    def __init__(self) -> None:
        self.sent = []
        self.closed = 0

    def infer(self, workload_id, inputs):
        self.sent.append((workload_id, inputs))
        if inputs["image"] == b"bad":
            raise TimeoutError()
        return InferenceResponse(200, b"{}", request_id="r%d" % len(self.sent), workload_uid="uid")

    def close(self) -> None:
        self.closed += 1


class ReplayTest(unittest.TestCase):
    def test_shards_partition_frozen_global_indexes_without_overlap(self):
        offsets = [2, 2, 7, 11, 15, 20, 22]
        parts = [shard_schedule(offsets, shard, 3) for shard in range(3)]
        indexes = [{int(request.request_id) for request in part} for part in parts]
        self.assertEqual(set.union(*indexes), set(range(len(offsets))))
        for left in range(3):
            for right in range(left):
                self.assertFalse(indexes[left] & indexes[right])
        for shard, part in enumerate(parts):
            self.assertTrue(all(int(request.request_id) % 3 == shard for request in part))
            self.assertEqual([request.scheduled_offset_ns for request in part], offsets[shard::3])

    def test_process_shards_merge_full_planned_population_on_one_start(self):
        path = Path(__file__).resolve().parents[2] / "hack/e5/e5-producer.py"
        spec = importlib.util.spec_from_file_location("test_sharded_producer", path)
        producer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(producer)
        offsets = [0] * 7
        binding = {"shards": 3, "barrierSeconds": 2, "payload": [],
                   "arrivalSHA256": "frozen"}
        with tempfile.TemporaryFile(mode="w+") as output:
            with patch.object(producer.sys, "stdout", output), \
                 patch.object(producer.Path, "exists", return_value=True), \
                 patch("chill_loadgen.cli.arrival_offsets_ns", side_effect=AssertionError("trace regenerated")):
                producer.replay_shards(
                    [("q1", {"image": b"a"})], offsets, 0, binding,
                    {"workload": "vqa", "new_client": FakeClient},
                )
            output.seek(0)
            lines = [json.loads(line) for line in output]
        rows = [row for row in lines if "requestIndex" in row]
        self.assertEqual(len(rows), len(offsets))
        self.assertEqual({row["requestIndex"] for row in rows}, set(range(len(offsets))))
        self.assertEqual(sum(row["status"] == 200 for row in rows), len(offsets))
        self.assertEqual({row["shardIndex"] for row in rows}, {0, 1, 2})
        starts = [row["monotonicNS"] for row in lines if row.get("e5Event") == "start"]
        self.assertEqual(len(starts), 1)
        self.assertEqual({row["scheduledMonotonicNS"] for row in rows}, set(starts))
        self.assertEqual(sum(row.get("e5Event") == "drained" for row in lines), 1)

    def test_e5_lateness_does_not_reduce_lifetime_or_prevent_send(self):
        for delay in (250_000_000, 2_000_000_000):
            with self.subTest(delay=delay), patch("chill_loadgen.http_client.HTTPSConnection") as connection:
                connection.return_value.sock = None
                response = connection.return_value.getresponse.return_value
                response.status = 200
                response.read.return_value = b"{}"
                response.getheader.return_value = None
                output = io.StringIO()
                replay_open_loop(
                    [("q1", {"image": b"a"})], workload="vqa", offsets=[0],
                    new_client=lambda: WorkloadHTTPClient(
                        "https://chill.example", bearer_token="token",
                        timeout_seconds=2, request_lifetime_seconds=1),
                    out=output, start_ns=0, clock_ns=lambda: delay,
                )
                row = json.loads(output.getvalue())
                self.assertEqual(row["requestIndex"], 0)
                self.assertEqual(connection.return_value.request.call_args.kwargs["headers"]["X-Chill-Request-Timeout"], "1.000000000s")
                self.assertTrue(row["issued"])
                self.assertEqual(row["latenessNs"], delay)
                self.assertIn("sentAtUnixNS", row)
                self.assertIn("finishedAtUnixNS", row)

    def test_e5_thread_start_failure_records_plan_and_continues_draining(self):
        original_start = threading.Thread.start
        attempts = []
        def start(thread):
            attempts.append(thread)
            if len(attempts) == 1:
                raise RuntimeError("can't start new thread")
            original_start(thread)
        output = io.StringIO()
        with patch.object(threading.Thread, "start", start):
            replay_open_loop(
                [("q1", {"image": b"a"})], workload="vqa", offsets=[0, 0, 0],
                new_client=FakeClient, out=output, start_ns=0, clock_ns=lambda: 0,
            )
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual({row["requestIndex"] for row in rows}, {0, 1, 2})
        self.assertEqual(rows[0]["error"], "GeneratorThreadStartError")
        self.assertFalse(rows[0]["issued"])
        self.assertEqual(sum(row.get("status") == 200 for row in rows), 2)

    def test_e5_dispatch_has_no_worker_cap_and_counts_scheduler_lateness(self) -> None:
        # All three calls must enter infer before any can finish, even workers=1.
        entered = threading.Barrier(3, timeout=2)
        now = [0]
        class ConcurrentClient(FakeClient):
            def infer(self, workload, inputs):
                entered.wait()
                return super().infer(workload, inputs)
        output = io.StringIO()
        def sleep(seconds):
            now[0] += round(seconds * 1e9) + 20_000_000
        replay(
            [("q1", {"image": b"a"})], workload="vqa", rate=3, duration=1,
            workers=1, new_client=ConcurrentClient, out=output, open_loop=True,
            clock_ns=lambda: now[0], sleep=sleep,
        )
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(3, len(rows))
        self.assertEqual({0, 1, 2}, {row["requestIndex"] for row in rows})
        for row in rows:
            self.assertEqual(row["sentMonotonicNS"] - row["scheduledMonotonicNS"], row["latenessNs"])
            self.assertEqual((row["finishedMonotonicNS"] - row["scheduledMonotonicNS"]) / 1e6, row["scheduledLatencyMs"])
            self.assertEqual(row["latencyMs"], row["serviceLatencyMs"])
            self.assertAlmostEqual(row["serviceLatencyMs"] + row["latenessNs"] / 1e6, row["scheduledLatencyMs"])

    def test_cycles_records_at_fixed_rate_and_reports_each_request(self) -> None:
        now = [0]
        sleeps = []

        def sleep(seconds: float) -> None:
            sleeps.append(seconds)
            now[0] += round(seconds * 1_000_000_000)

        client = FakeClient()
        output = io.StringIO()
        replay(
            [("q1", {"image": b"a"}), ("q2", {"image": b"bad"})],
            workload="vqa",
            rate=2,
            duration=1.5,
            workers=1,
            new_client=lambda: client,
            out=output,
            clock_ns=lambda: now[0],
            sleep=sleep,
        )

        lines = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual([0.5, 0.5], sleeps)
        self.assertEqual(["q1", "q2", "q1"], [line["record"] for line in lines])
        self.assertEqual([0, 500_000_000, 1_000_000_000], [line["scheduledOffsetNs"] for line in lines])
        self.assertEqual(
            {"workload": "vqa", "record": "q1", "scheduledOffsetNs": 0, "delayed": False,
             "status": 200, "requestID": "r1", "workloadUID": "uid", "latencyMs": 0.0,
             "sentMonotonicNS": 0, "finishedMonotonicNS": 0},
            lines[0],
        )
        self.assertEqual("TimeoutError", lines[1]["error"])
        self.assertNotIn("status", lines[1])
        self.assertEqual(2, client.closed)  # after the failure and at the end

    def test_stops_sending_when_duration_elapses_behind_schedule(self) -> None:
        # Each request takes 0.4 s at 10 req/s: the sender falls behind and must
        # stop at 1 s instead of draining the whole 10-request schedule.
        now = [0]

        class SlowClient(FakeClient):
            def infer(self, workload, inputs):
                now[0] += 400_000_000
                return super().infer(workload, inputs)

        output = io.StringIO()
        replay(
            [("q1", {"image": b"a"})], workload="vqa", rate=10, duration=1, workers=1,
            new_client=SlowClient, out=output, clock_ns=lambda: now[0],
            sleep=lambda seconds: now.__setitem__(0, now[0] + round(seconds * 1e9)),
        )

        self.assertEqual(3, len(output.getvalue().splitlines()))  # sent at 0, 0.4 and 0.8 s

    def test_closed_loop_refills_until_duration_and_keeps_final_response(self) -> None:
        now = [0]

        class SlowClient(FakeClient):
            def infer(self, workload, inputs):
                now[0] += 400_000_000
                return super().infer(workload, inputs)

        output = io.StringIO()
        replay(
            [("q1", {"image": b"a"}), ("q2", {"image": b"b"})],
            workload="vqa", rate=None, duration=1, workers=1, arrivals="closed-loop",
            new_client=SlowClient, out=output, clock_ns=lambda: now[0],
            sleep=lambda _: self.fail("closed-loop must not wait for an arrival schedule"),
        )
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(["q1", "q2", "q1"], [row["record"] for row in rows])
        self.assertEqual([0, 400_000_000, 800_000_000], [row["scheduledOffsetNs"] for row in rows])
        self.assertTrue(all(row["status"] == 200 for row in rows))
        self.assertEqual(1_200_000_000, now[0])
        self.assertEqual([0, 400_000_000, 800_000_000], [row["sentMonotonicNS"] for row in rows])
        self.assertEqual([400_000_000, 800_000_000, 1_200_000_000], [row["finishedMonotonicNS"] for row in rows])

    def test_poisson_arrivals_repeat_per_seed_and_average_one_over_rate(self) -> None:
        offsets = arrival_offsets_ns("poisson", 50, 200, 7)
        self.assertEqual(offsets, arrival_offsets_ns("poisson", 50, 200, 7))
        self.assertNotEqual(offsets, arrival_offsets_ns("poisson", 50, 200, 8))
        self.assertEqual(sorted(offsets), offsets)
        self.assertTrue(0 <= offsets[0] and offsets[-1] < 200_000_000_000)
        mean_gap_s = (offsets[-1] - offsets[0]) / (len(offsets) - 1) / 1e9
        self.assertAlmostEqual(1 / 50, mean_gap_s, delta=0.001)


if __name__ == "__main__":
    unittest.main()


class MainTest(unittest.TestCase):
    def test_replay_receives_workers(self) -> None:
        from unittest.mock import patch

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "q1").mkdir()
            (root / "q1" / "image").write_bytes(b"jpeg")
            (root / "token").write_text("secret\n")
            with patch("chill_loadgen.cli.WorkloadHTTPClient"), patch("chill_loadgen.cli.replay") as run:
                main([
                    "replay", "--endpoint", "https://chill.example", "--token-file", str(root / "token"),
                    "--workload", "vqa", "--dataset-dir", str(root), "--fields", "image",
                    "--rate", "1", "--duration", "1", "--workers", "3",
                ])

        self.assertEqual(3, run.call_args.kwargs["workers"])

    def test_closed_loop_derives_socket_budget_and_forwards_lifetime(self) -> None:
        from unittest.mock import patch

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "q1").mkdir()
            (root / "q1" / "image").write_bytes(b"jpeg")
            token = root / "token"
            token.write_text("secret")
            args = [
                "replay", "--endpoint", "https://chill.example", "--token-file", str(token),
                "--workload", "smol", "--dataset-dir", str(root), "--fields", "image",
                "--arrivals", "closed-loop", "--workers", "2", "--duration", "420",
                "--request-lifetime", "129",
            ]
            with patch("chill_loadgen.cli.WorkloadHTTPClient") as client, patch("chill_loadgen.cli.replay") as run:
                self.assertEqual(0, main(args))
                self.assertEqual(129.01, client.call_args.kwargs["timeout_seconds"])
                self.assertEqual(129, client.call_args.kwargs["request_lifetime_seconds"])
                self.assertIsNone(run.call_args.kwargs["rate"])
                self.assertEqual("closed-loop", run.call_args.kwargs["arrivals"])
            for extra in (["--timeout", "129"], ["--rate", "10"]):
                with self.subTest(extra=extra), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    main(args + extra)

    def test_e5_poisson_binds_slo_lifetime_gateway_cap_and_socket(self) -> None:
        from unittest.mock import patch

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "q1").mkdir()
            (root / "q1" / "image").write_bytes(b"jpeg")
            token = root / "token"
            token.write_text("secret")
            args = [
                "replay", "--endpoint", "https://chill.example", "--token-file", str(token),
                "--workload", "mnv", "--dataset-dir", str(root), "--fields", "image",
                "--arrivals", "poisson", "--rate", "50", "--duration", "300",
                "--workload-slo", "1", "--request-lifetime", "2", "--gateway-timeout", "129",
            ]
            with patch("chill_loadgen.cli.WorkloadHTTPClient") as client, patch("chill_loadgen.cli.replay") as run:
                self.assertEqual(0, main(args))
                timeout = client.call_args.kwargs["timeout_seconds"]
                self.assertGreater(timeout, 2.01)
                run.call_args.kwargs["new_client"]()
                self.assertEqual(timeout, client.call_args.kwargs["timeout_seconds"])
                self.assertEqual(2, client.call_args.kwargs["request_lifetime_seconds"])
                self.assertEqual("poisson", run.call_args.kwargs["arrivals"])
            for extra in (
                ["--timeout", "2.01"], ["--request-lifetime", "0.5"],
                ["--gateway-timeout", "1.5"],
            ):
                with self.subTest(extra=extra), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    main(args + extra)
            missing_cap = args[:-2]
            with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                main(missing_cap)
            lifetime_flag = args.index("--request-lifetime")
            optional = args[:lifetime_flag] + args[lifetime_flag + 2:]
            with patch("chill_loadgen.cli.WorkloadHTTPClient") as client, patch("chill_loadgen.cli.replay") as run:
                self.assertEqual(0, main(optional))
                self.assertIsNone(client.call_args.kwargs["request_lifetime_seconds"])
                self.assertEqual(30, client.call_args.kwargs["timeout_seconds"])
                run.call_args.kwargs["new_client"]()
                self.assertIsNone(client.call_args.kwargs["request_lifetime_seconds"])

    def test_open_loop_requires_rate_and_rejects_nonfinite_values(self) -> None:
        for extra in ([], ["--rate", "inf"], ["--rate", "1", "--duration", "nan"]):
            with self.subTest(extra=extra), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                main([
                    "replay", "--endpoint", "https://chill.example", "--token-file", "/unused",
                    "--workload", "vqa", "--dataset-dir", "/unused", "--fields", "image",
                    "--duration", "1",
                ] + extra)
