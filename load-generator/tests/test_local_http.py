"""Real HTTPS smoke: no gateway, Kubernetes, runtime image or external network needed."""

import io
import json
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from chill_loadgen.cli import prepare_records, replay, replay_open_loop
from chill_loadgen.http_client import WorkloadHTTPClient


class LocalHTTPSmoke(unittest.TestCase):
    def test_fixed_outstanding_refills_and_drains_with_lifetime_header(self) -> None:
        lock = threading.Lock()
        active = 0
        maximum = 0
        starts = []
        finishes = []
        refills = []
        lifetimes = []
        previous_finish = {}

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                nonlocal active, maximum
                self.rfile.read(int(self.headers["Content-Length"]))
                if self.path.endswith("/feedback"):
                    self.send_response(204)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                now = time.monotonic()
                with lock:
                    starts.append(now)
                    lifetimes.append(self.headers.get("X-Chill-Request-Timeout"))
                    refill = self.client_address in previous_finish
                    if refill:
                        refills.append(now - previous_finish[self.client_address])
                    active += 1
                    maximum = max(maximum, active)
                time.sleep(0.35 if refill else 0.02)
                with lock:
                    active -= 1
                    finishes.append(time.monotonic())
                    previous_finish[self.client_address] = finishes[-1]
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.send_header("X-Chill-Request-ID", str(now))
                self.end_headers()
                self.wfile.write(b"{}")
                self.wfile.flush()

        with tempfile.TemporaryDirectory() as directory:
            cert, key = Path(directory) / "cert.pem", Path(directory) / "key.pem"
            subprocess.run([
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                "-keyout", str(key), "-out", str(cert), "-days", "1",
                "-subj", "/CN=localhost", "-addext", "subjectAltName=IP:127.0.0.1",
            ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            server.socket = context.wrap_socket(server.socket, server_side=True)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            clients = []
            try:
                for _ in range(3):
                    client = WorkloadHTTPClient(
                        f"https://127.0.0.1:{server.server_port}", bearer_token="local",
                        timeout_seconds=129.02, request_lifetime_seconds=129, ca_file=str(cert),
                    )
                    client._connection.connect()
                    self.assertGreater(client._connection.sock.gettimeout(), 129 + 0.01)
                    clients.append(client)
                def new_client():
                    return WorkloadHTTPClient(
                        f"https://127.0.0.1:{server.server_port}", bearer_token="local",
                        timeout_seconds=129.02, request_lifetime_seconds=129, ca_file=str(cert),
                    )
                output = io.StringIO()
                started = time.monotonic()
                replay(
                    [("one", {"image": b"sample"})], workload="smol", rate=None,
                    duration=0.30, workers=3, arrivals="closed-loop",
                    new_client=new_client, out=output,
                )
                ended = time.monotonic()
                rows = [json.loads(line) for line in output.getvalue().splitlines()]
                self.assertEqual(3, maximum)
                self.assertGreaterEqual(len(rows), 6)
                self.assertEqual(len(starts), len(rows))
                self.assertTrue(all(row.get("status") == 200 for row in rows), rows)
                self.assertEqual({"129.000000000s"}, set(lifetimes))
                self.assertTrue(refills)
                self.assertLess(max(refills), 0.10)  # no rate-based sleep after completion
                self.assertLess(max(starts) - started, 0.30)
                self.assertGreater(max(finishes) - started, 0.30)
                self.assertGreaterEqual(ended, max(finishes))  # join, never abandon the tail
                self.assertEqual(0, active)
            finally:
                for client in clients:
                    client.close()
                server.shutdown()
                server.server_close()
                thread.join()

    def _open_loop_rows(self, offsets_ms, drop_request=None):
        """Replay sequential arrivals; return rows and the client ports the server saw per inference."""
        ports = []

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                status = 204 if self.path.endswith("/feedback") else 200
                if status == 200:
                    ports.append(self.client_address[1])
                    if len(ports) - 1 == drop_request:
                        self.close_connection = True  # Hang up without a response.
                        return
                self.send_response(status)
                self.send_header("Content-Length", "0")
                self.end_headers()

        with tempfile.TemporaryDirectory() as directory:
            cert, key = Path(directory) / "cert.pem", Path(directory) / "key.pem"
            subprocess.run([
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                "-keyout", str(key), "-out", str(cert), "-days", "1",
                "-subj", "/CN=localhost", "-addext", "subjectAltName=IP:127.0.0.1",
            ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            server.socket = context.wrap_socket(server.socket, server_side=True)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                output = io.StringIO()
                replay_open_loop(
                    prepare_records([("one", {"image": b"sample"})]), workload="smol",
                    offsets=[ms * 1_000_000 for ms in offsets_ms], workers=1, out=output,
                    start_ns=time.monotonic_ns(),
                    new_client=lambda: WorkloadHTTPClient(
                        f"https://127.0.0.1:{server.server_port}", bearer_token="local",
                        timeout_seconds=5, ca_file=str(cert)),
                )
            finally:
                server.shutdown()
                server.server_close()
                thread.join()
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        return sorted((row for row in rows if "requestIndex" in row), key=lambda row: row["requestIndex"]), ports

    def test_open_loop_reuses_idle_connections(self) -> None:
        rows, ports = self._open_loop_rows([0, 100, 200, 300, 400])
        self.assertEqual(5, len(rows))
        self.assertTrue(all(row.get("status") == 200 for row in rows), rows)
        self.assertLess(len(set(ports)), len(rows))
        self.assertEqual([False] + [True] * 4, [row["connectionReused"] for row in rows])

    def test_open_loop_never_reuses_a_failed_connection(self) -> None:
        rows, ports = self._open_loop_rows([0, 100, 200], drop_request=1)
        self.assertIn("error", rows[1])
        self.assertTrue(rows[1]["connectionReused"])
        self.assertEqual(200, rows[2]["status"])
        self.assertFalse(rows[2]["connectionReused"])
        self.assertNotEqual(ports[1], ports[2])


if __name__ == "__main__":
    unittest.main()
