import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

from chill_loadgen.cli import arrival_offsets_ns, load_records, main, replay
from chill_loadgen.http_client import InferenceResponse


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
             "status": 200, "requestID": "r1", "workloadUID": "uid", "latencyMs": 0.0},
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

    def test_open_loop_requires_rate_and_rejects_nonfinite_values(self) -> None:
        for extra in ([], ["--rate", "inf"], ["--rate", "1", "--duration", "nan"]):
            with self.subTest(extra=extra), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                main([
                    "replay", "--endpoint", "https://chill.example", "--token-file", "/unused",
                    "--workload", "vqa", "--dataset-dir", "/unused", "--fields", "image",
                    "--duration", "1",
                ] + extra)
