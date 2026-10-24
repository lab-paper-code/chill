import json
import ssl
import unittest
from unittest.mock import Mock, patch

from chill_loadgen.http_client import InferenceResponse, WorkloadHTTPClient


class WorkloadHTTPClientTest(unittest.TestCase):
    @patch("chill_loadgen.http_client.ssl.create_default_context")
    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_authenticated_sample_and_correlated_http_results(
        self, connection_type: Mock, context_factory: Mock
    ) -> None:
        connection = connection_type.return_value
        connection.sock = None  # never-idle connection
        response = connection.getresponse.return_value
        body = b'{"code":"QueueFull"}'
        response.read.return_value = body
        response.getheader.side_effect = lambda name: {
            "X-Chill-Request-ID": "request-1",
            "X-Chill-Workload-UID": "uid-1",
        }.get(name)
        inputs = {"image": b"\xff\xd8\xff\xe0", "question": "무엇?".encode("utf-8")}
        client = WorkloadHTTPClient(
            "https://chill.example:8443/",
            bearer_token="token",
            timeout_seconds=5.0,
            ca_file="/trusted/ca.pem",
        )

        context_factory.assert_called_once_with(cafile="/trusted/ca.pem")
        connection_type.assert_called_once_with(
            "chill.example", 8443, timeout=5.0, context=context_factory.return_value
        )
        for status in (200, 302, 401, 403, 429, 502, 503, 504):
            with self.subTest(status=status):
                response.status = status
                result = client.infer("image-service", inputs)
                self.assertEqual(
                    InferenceResponse(status, body, "request-1", "uid-1"), result
                )
                args, kwargs = connection.request.call_args
                self.assertEqual(("POST", "/api/v1/workloads/image-service/infer"), args)
                self.assertEqual(
                    {"inputs": [
                        {"name": "image", "datatype": "BYTES", "data": "/9j/4A=="},
                        {"name": "question", "datatype": "BYTES", "data": "66y07JeHPw=="},
                    ]},
                    json.loads(kwargs["body"]),
                )
                self.assertEqual(
                    {"Content-Type": "application/json", "Authorization": "Bearer token"},
                    kwargs["headers"],
                )
        self.assertEqual(8, connection.request.call_count)  # No redirects or retries.
        client.close()
        connection.close.assert_called_once_with()

    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_default_context_verifies_certificates_and_hostname(
        self, connection_type: Mock
    ) -> None:
        WorkloadHTTPClient(
            "https://[::1]:8443", bearer_token="token", timeout_seconds=5
        )
        context = connection_type.call_args.kwargs["context"]
        self.assertEqual(ssl.CERT_REQUIRED, context.verify_mode)
        self.assertTrue(context.check_hostname)

    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_rejects_unsafe_endpoint_without_connecting(self, connection_type: Mock) -> None:
        for endpoint in (
            "http://chill.example", "https://", "https://user:secret@chill.example",
            "https://chill.example/gateway", "https://chill.example/../other",
            "https://chill.example/%2fother", "https://chill.example?next=other",
            "https://chill.example#", "https://chill.example?",
            "https://chill.example\\@other", "https://chill.example\n",
            " https://chill.example", "https://chill.example:0",
            "https://chill.example:65536", "https://chill.example:",
            "https://[::1]other", "https://%65xample.com",
        ):
            with self.subTest(endpoint=endpoint), self.assertRaisesRegex(ValueError, "endpoint"):
                WorkloadHTTPClient(endpoint, bearer_token="token", timeout_seconds=5)
        connection_type.assert_not_called()

    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_rejects_header_injection_and_invalid_timeouts(
        self, connection_type: Mock
    ) -> None:
        for token in ("", "Bearer secret", "secret\r\nX-Header: injected", "secret\x00", "秘密"):
            with self.subTest(token=token), self.assertRaisesRegex(ValueError, "bearer_token") as error:
                WorkloadHTTPClient(
                    "https://chill.example", bearer_token=token, timeout_seconds=5
                )
            if token:
                self.assertNotIn(token, str(error.exception))
        for timeout in (0, -1, float("nan"), float("inf"), True):
            with self.subTest(timeout=timeout), self.assertRaisesRegex(ValueError, "timeout_seconds"):
                WorkloadHTTPClient(
                    "https://chill.example", bearer_token="token", timeout_seconds=timeout
                )
        connection_type.assert_not_called()

    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_request_lifetime_header_and_independent_socket_budget(self, connection_type: Mock) -> None:
        connection = connection_type.return_value
        connection.sock = None
        client = WorkloadHTTPClient(
            "https://chill.example", bearer_token="token",
            timeout_seconds=129.01, request_lifetime_seconds=129,
        )
        client.infer("smol", {"question": b"what?"})
        self.assertEqual(
            "129.000000000s",
            connection.request.call_args.kwargs["headers"]["X-Chill-Request-Timeout"],
        )
        self.assertEqual(129.01, connection_type.call_args.kwargs["timeout"])
        for lifetime in (0, -1, float("nan"), float("inf"), True, 130, 1e-10):
            with self.subTest(lifetime=lifetime), self.assertRaisesRegex(ValueError, "request_lifetime_seconds"):
                WorkloadHTTPClient(
                    "https://chill.example", bearer_token="token",
                    timeout_seconds=130, request_lifetime_seconds=lifetime,
                )

    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_rejects_unsafe_identity_and_non_sample_input_before_sending(
        self, connection_type: Mock
    ) -> None:
        client = WorkloadHTTPClient(
            "https://chill.example", bearer_token="token", timeout_seconds=5
        )
        sample = {"image": b"\xff\xd8\xff"}
        for workload_id in ("", "../model", "a/b", "a%2fb", "a?next=other", "a#b", "a\r\n", "a..b", "A", "a" * 64):
            with self.subTest(workload_id=workload_id), self.assertRaisesRegex(ValueError, "workload_id"):
                client.infer(workload_id, sample)
        for inputs in (
            {}, [("image", b"\xff")], b'{"inputs":[]}', {"": b"x"}, {1: b"x"},
            {"image": ""}, {"image": b""}, {"image": "text"}, {"image": bytearray(b"x")},
        ):
            with self.subTest(inputs=inputs), self.assertRaises(ValueError):
                client.infer("image-service", inputs)
        connection_type.return_value.request.assert_not_called()

    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_transport_failures_remain_distinct_from_http_rejection(
        self, connection_type: Mock
    ) -> None:
        client = WorkloadHTTPClient(
            "https://chill.example", bearer_token="token", timeout_seconds=5
        )
        connection_type.return_value.sock = None
        connection_type.return_value.request.side_effect = TimeoutError()
        with self.assertRaises(TimeoutError):
            client.infer("image-service", {"value": b"1"})
        connection_type.return_value.request.assert_called_once()


if __name__ == "__main__":
    unittest.main()


class StaleConnectionTest(unittest.TestCase):
    @patch("chill_loadgen.http_client.HTTPSConnection")
    def test_reopens_peer_closed_idle_connection_before_sending(self, connection_type: Mock) -> None:
        import socket

        local, peer = socket.socketpair()
        peer.close()  # server idle timeout closed the kept-alive connection
        connection = connection_type.return_value
        connection.sock = local
        events = []
        connection.close.side_effect = lambda: events.append("close")
        connection.request.side_effect = lambda *a, **k: events.append("request")
        connection.getresponse.return_value.status = 200
        client = WorkloadHTTPClient("https://chill.example", bearer_token="t", timeout_seconds=1)

        client.infer("vqa", {"image": b"a"})
        self.assertEqual(["close", "request"], events)

        connection.sock = None  # fresh connection: nothing to check
        events.clear()
        client.infer("vqa", {"image": b"a"})
        self.assertEqual(["request"], events)
        local.close()
