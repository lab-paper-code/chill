"""Real local HTTP exchanges for exact duration and independent feedback drain."""

import io
import json
import threading
import time
import unittest
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

from chill_loadgen.cli import prepare_records, replay, replay_open_loop
from chill_loadgen.feedback import ClientObservationReporter
from chill_loadgen.http_client import WorkloadHTTPClient


class ClientFeedbackTest(unittest.TestCase):
    def setUp(self):
        self.mode = "completed"
        self.slow_feedback = False
        self.reject_feedback = False
        self.inferences = []
        self.reports = []
        self.third_inference = threading.Event()
        self.feedback_wait_succeeded = []
        self.feedback_holds = []
        self.feedback_entered = threading.Event()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                if self.path.endswith("/feedback"):
                    owner.reports.append((self.path, json.loads(body), dict(self.headers), self.client_address))
                    owner.feedback_entered.set()
                    index = len(owner.reports) - 1
                    if index < len(owner.feedback_holds):
                        owner.feedback_holds[index].wait(2)
                    if owner.slow_feedback and len(owner.reports) == 1:
                        owner.feedback_wait_succeeded.append(owner.third_inference.wait(2))
                    self.send_response(503 if owner.reject_feedback else 204)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                owner.inferences.append(self.client_address)
                if len(owner.inferences) >= 3:
                    owner.third_inference.set()
                if owner.mode == "timeout":
                    time.sleep(.12)
                    self.close_connection = True
                    return
                self.send_response(503 if owner.mode == "http_error" else 200)
                self.send_header("X-Chill-Request-ID", "opaque-request-" + str(len(owner.inferences)))
                self.send_header("X-Chill-Workload-UID", "workload-uid")
                self.send_header("Content-Length", "4" if owner.mode == "partial" else "2")
                self.end_headers()
                if owner.mode == "partial":
                    self.wfile.write(b"{")
                    self.wfile.flush()
                    time.sleep(.12)
                    self.close_connection = True
                    return
                time.sleep(.03)
                self.wfile.write(b"{}")
                self.wfile.flush()

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.server_thread = threading.Thread(target=self.server.serve_forever)
        self.server_thread.start()
        # TLS validation has its own real HTTPS test. This test exercises actual
        # HTTP framing and body reads without certificate generation overhead.
        self.transport = patch("chill_loadgen.http_client.HTTPSConnection",
                               side_effect=lambda host, port, timeout, context: HTTPConnection(host, port, timeout=timeout))
        self.transport.start()
        self.timeout = 3

    def tearDown(self):
        self.transport.stop()
        self.server.shutdown()
        self.server.server_close()
        self.server_thread.join()

    def new_client(self):
        return WorkloadHTTPClient(f"https://127.0.0.1:{self.server.server_port}",
                                  bearer_token="local-token", timeout_seconds=self.timeout)

    def run_open(self, offsets=(0,)):
        output = io.StringIO()
        replay_open_loop(prepare_records([("one", {"input": b"sample"})]), workload="model",
                         offsets=offsets, start_ns=time.monotonic_ns(), new_client=self.new_client, out=output,
                         workers=len(offsets))
        return [json.loads(line) for line in output.getvalue().splitlines()]

    def test_full_body_duration_and_public_report_use_separate_connection(self):
        for mode in ("completed", "http_error"):
            with self.subTest(mode=mode):
                self.mode = mode
                rows = self.run_open()
                row = rows[0]
                path, report, headers, address = self.reports[-1]
                self.assertEqual(len(rows), 1)
                self.assertGreaterEqual(row["elapsedNanos"], 30_000_000)
                self.assertEqual(report, {"observationID": row["observationID"], "requestID": row["requestID"],
                                          "workloadUID": "workload-uid", "elapsedNanos": row["elapsedNanos"],
                                          "outcome": mode, "statusCode": row["status"]})
                self.assertEqual(path, "/api/v1/workloads/model/feedback")
                self.assertEqual(headers["Authorization"], "Bearer local-token")
                self.assertNotIn("X-Chill-Request-Timeout", headers)
                self.assertNotIn(address, self.inferences)

    def test_slow_reporter_does_not_serialize_arrivals_or_closed_loop_refill(self):
        for closed_loop in (False, True):
            with self.subTest(closed_loop=closed_loop):
                self.slow_feedback = True
                self.inferences.clear()
                self.reports.clear()
                self.third_inference.clear()
                self.feedback_wait_succeeded.clear()
                if closed_loop:
                    output = io.StringIO()
                    replay([("one", {"input": b"sample"})], workload="model", rate=None,
                           duration=.2, workers=1, arrivals="closed-loop", new_client=self.new_client, out=output)
                    rows = [json.loads(line) for line in output.getvalue().splitlines()]
                else:
                    rows = self.run_open((0, 10_000_000, 20_000_000))
                self.assertEqual(self.feedback_wait_succeeded, [True])
                self.assertGreaterEqual(len(self.inferences), 3)
                self.assertEqual(len(rows), len(self.inferences))
                self.assertEqual(len(self.reports), sum(row["feedbackStatus"] == "queued" for row in rows))
                self.assertTrue(all(row.get("feedbackError") == "ReporterBackpressure"
                                    for row in rows if row["feedbackStatus"] == "not_reported"))

    def test_timeout_retains_missing_or_partial_header_evidence(self):
        self.timeout = .05
        for mode in ("timeout", "partial"):
            with self.subTest(mode=mode):
                self.mode = mode
                row = self.run_open()[0]
                report = self.reports[-1][1]
                self.assertEqual(row["error"], "TimeoutError")
                self.assertEqual(row["outcome"], "timeout")
                self.assertGreaterEqual(row["elapsedNanos"], 50_000_000)
                self.assertEqual(report["elapsedNanos"], row["elapsedNanos"])
                if mode == "timeout":
                    self.assertNotIn("requestID", report)
                    self.assertNotIn("workloadUID", report)
                    self.assertNotIn("statusCode", report)
                else:
                    self.assertTrue(report["requestID"].startswith("opaque-request-"))
                    self.assertEqual(report["workloadUID"], "workload-uid")
                    self.assertEqual(report["statusCode"], 200)

    def test_feedback_failure_is_separate_and_never_retried(self):
        self.reject_feedback = True
        rows = self.run_open()
        inference = next(row for row in rows if "scheduledOffsetNs" in row)
        failure = next(row for row in rows if "clientFeedbackEvent" in row)
        self.assertEqual(inference["outcome"], "completed")
        self.assertNotIn("error", inference)
        self.assertEqual(failure["observationID"], inference["observationID"])
        self.assertEqual(len(self.reports), 1)

    def test_schedule_error_drains_feedback_and_preserves_original_exception(self):
        self.reject_feedback = True
        def schedule(requests, submit, **kwargs):
            submit(requests[0], False)
            raise ValueError("original schedule error")
        with patch("chill_loadgen.cli.run_schedule", side_effect=schedule), \
             self.assertRaisesRegex(ValueError, "original schedule error"):
            self.run_open()
        self.assertEqual(len(self.reports), 1)

    def test_backpressure_bounds_reports_and_streams_each_failure_without_losing_inference(self):
        workers, count = 2, 9
        first, tail = threading.Event(), threading.Event()
        all_rows, first_failure, finished = threading.Event(), threading.Event(), threading.Event()
        self.feedback_holds = [first] + [tail] * workers
        self.reject_feedback = True
        errors = []
        seen = []

        class Output(io.StringIO):
            def write(self, text):
                row = json.loads(text)
                seen.append(row)
                if sum("scheduledOffsetNs" in item for item in seen) == count:
                    all_rows.set()
                if row.get("clientFeedbackEvent") == "failed":
                    first_failure.set()
                return super().write(text)

        output = Output()
        prepared_client = self.new_client()

        def schedule(requests, submit, **kwargs):
            submit(requests[0], False)
            if not self.feedback_entered.wait(2):
                raise AssertionError("first report did not enter local HTTP handler")
            for request in requests[1:]:
                submit(request, False)

        def run():
            try:
                replay_open_loop(prepare_records([("one", {"input": b"sample"})]), workload="model",
                                 offsets=[0] * count, workers=workers, start_ns=time.monotonic_ns(),
                                 new_client=prepared_client.new_connection, out=output)
            except BaseException as error:
                errors.append(error)
            finally:
                finished.set()

        caller = threading.Thread(target=run)
        try:
            with patch("chill_loadgen.cli.run_schedule", side_effect=schedule):
                caller.start()
                self.assertTrue(all_rows.wait(2), "slow feedback blocked inference completion")
                self.assertFalse(finished.is_set())
                first.set()
                self.assertTrue(first_failure.wait(2), "failure was retained until shutdown")
                self.assertFalse(finished.is_set())
                tail.set()
                self.assertTrue(finished.wait(2))
        finally:
            first.set()
            tail.set()
            caller.join(5)
            prepared_client.close()
        self.assertFalse(errors, errors)
        rows = [row for row in seen if "scheduledOffsetNs" in row]
        failures = [row for row in seen if row.get("clientFeedbackEvent") == "failed"]
        queued = [row for row in rows if row["feedbackStatus"] == "queued"]
        skipped = [row for row in rows if row["feedbackStatus"] == "not_reported"]
        self.assertEqual(len(rows), count)
        self.assertTrue(all(row["outcome"] == "completed" and row["elapsedNanos"] >= 30_000_000 for row in rows))
        self.assertEqual(len(queued), workers + 1)  # One active report plus the existing worker budget.
        self.assertEqual(len(skipped), count - workers - 1)
        self.assertTrue(all(row["feedbackError"] == "ReporterBackpressure" for row in skipped))
        self.assertEqual(len(self.reports), workers + 1)
        self.assertEqual(len(failures), workers + 1)
        self.assertEqual({row["observationID"] for row in queued}, {row["observationID"] for row in failures})

    def test_repeated_close_does_not_report_again_or_accept_new_work(self):
        reporter = ClientObservationReporter("model", self.new_client, lambda row: self.fail(row), workers=2)
        observation = {"observationID": "one", "elapsedNanos": 1, "outcome": "timeout"}
        self.assertIsNone(reporter.enqueue(observation))
        reporter.close()
        reporter.close()
        self.assertEqual(reporter.enqueue(observation), "ReporterClosed")
        self.assertEqual(len(self.reports), 1)
