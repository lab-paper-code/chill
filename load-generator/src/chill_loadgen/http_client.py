"""HTTP client for CHILL's KServe V2 inference endpoint."""

from __future__ import annotations

from dataclasses import dataclass
from http.client import HTTPConnection
from urllib.parse import quote, urlsplit


@dataclass(frozen=True)
class InferenceResponse:
    status_code: int
    body: bytes


class KServeV2HTTPClient:
    def __init__(self, endpoint: str, *, timeout_seconds: float) -> None:
        parsed = urlsplit(endpoint)
        if parsed.scheme != "http" or not parsed.hostname or parsed.query or parsed.fragment:
            raise ValueError("endpoint must be an HTTP URL without query or fragment")
        if timeout_seconds <= 0:
            raise ValueError("timeout_seconds must be positive")

        self._base_path = parsed.path.rstrip("/")
        self._connection = HTTPConnection(
            parsed.hostname,
            parsed.port,
            timeout=timeout_seconds,
        )

    def infer(self, model: str, body: bytes) -> InferenceResponse:
        if not model:
            raise ValueError("model must not be empty")

        path = f"{self._base_path}/v2/models/{quote(model, safe='')}/infer"
        self._connection.request(
            "POST",
            path,
            body=body,
            headers={"Content-Type": "application/json"},
        )
        response = self._connection.getresponse()
        return InferenceResponse(response.status, response.read())

    def close(self) -> None:
        self._connection.close()
