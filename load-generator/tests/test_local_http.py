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

from chill_loadgen.cli import replay
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
                    # Socket timeout conversion rounds floating seconds up to nanoseconds.
                    self.assertAlmostEqual(129.02, client._connection.sock.gettimeout(), delta=2e-9)
                    self.assertGreater(client._connection.sock.gettimeout(), 129 + 0.01)
                    clients.append(client)
                available = iter(clients)
                output = io.StringIO()
                started = time.monotonic()
                replay(
                    [("one", {"image": b"sample"})], workload="smol", rate=None,
                    duration=0.30, workers=3, arrivals="closed-loop",
                    new_client=lambda: next(available), out=output,
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


if __name__ == "__main__":
    unittest.main()
