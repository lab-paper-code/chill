"""Verified HTTPS client for CHILL's one-sample Workload raw inference API."""

from __future__ import annotations

import base64
import ipaddress
import json
import math
import re
import select
import ssl
from dataclasses import dataclass
from http.client import HTTPSConnection
from typing import Mapping, Optional
from urllib.parse import urlsplit


def _valid_identifier(value: str) -> bool:
    return (
        isinstance(value, str)
        and 0 < len(value) <= 253
        and all(
            re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label)
            for label in value.split(".")
        )
    )


@dataclass(frozen=True)
class InferenceResponse:
    status_code: int
    body: bytes
    request_id: Optional[str] = None
    workload_uid: Optional[str] = None


class WorkloadHTTPClient:
    def __init__(
        self,
        endpoint: str,
        *,
        bearer_token: str,
        timeout_seconds: float,
        ca_file: Optional[str] = None,
        request_lifetime_seconds: Optional[float] = None,
    ) -> None:
        if (
            not isinstance(endpoint, str)
            or any(
                character.isspace() or ord(character) < 32 or ord(character) == 127
                for character in endpoint
            )
            or any(character in endpoint for character in "\\?#")
        ):
            raise ValueError("endpoint must be an HTTPS origin")
        try:
            parsed = urlsplit(endpoint)
            hostname, port = parsed.hostname, parsed.port
            valid_host = bool(hostname) and _valid_identifier(hostname)
            if hostname and ":" in hostname:
                ipaddress.IPv6Address(hostname)
                valid_host = True
            authority = f"[{hostname}]" if hostname and ":" in hostname else hostname
            if port is not None:
                authority = f"{authority}:{port}"
        except ValueError:
            raise ValueError("endpoint must be an HTTPS origin") from None
        if (
            parsed.scheme != "https"
            or not valid_host
            or parsed.username is not None
            or parsed.password is not None
            or "%" in parsed.netloc
            or parsed.path not in ("", "/")
            or parsed.netloc.lower() != authority
            or port == 0
        ):
            raise ValueError("endpoint must be an HTTPS origin")
        if not isinstance(bearer_token, str) or not re.fullmatch(
            r"[A-Za-z0-9._~+/-]+=*", bearer_token
        ):
            raise ValueError("bearer_token must be a nonempty bearer credential")
        if (
            isinstance(timeout_seconds, bool)
            or not isinstance(timeout_seconds, (int, float))
            or not math.isfinite(timeout_seconds)
            or timeout_seconds <= 0
        ):
            raise ValueError("timeout_seconds must be finite and positive")
        if request_lifetime_seconds is not None:
            if (
                isinstance(request_lifetime_seconds, bool)
                or not isinstance(request_lifetime_seconds, (int, float))
                or not math.isfinite(request_lifetime_seconds)
                or request_lifetime_seconds < 1e-9
                or timeout_seconds <= request_lifetime_seconds
            ):
                raise ValueError("request_lifetime_seconds must be finite, positive and below the socket timeout")
        self._request_lifetime = (
            f"{request_lifetime_seconds:.9f}s" if request_lifetime_seconds is not None else None
        )

        self._authorization = "Bearer " + bearer_token
        self._connection = HTTPSConnection(
            hostname,
            port,
            timeout=timeout_seconds,
            context=ssl.create_default_context(cafile=ca_file),
        )

    def infer(self, workload_id: str, inputs: Mapping[str, bytes]) -> InferenceResponse:
        """Send one raw sample: every preprocessor input field name mapped to its bytes."""
        if not _valid_identifier(workload_id):
            raise ValueError("workload_id must be a Kubernetes DNS subdomain name")
        if not isinstance(inputs, Mapping) or not inputs:
            raise ValueError("inputs must be a nonempty mapping of field name to bytes")
        fields = []
        for name, data in inputs.items():
            if not isinstance(name, str) or not name:
                raise ValueError("input field names must be nonempty strings")
            if not isinstance(data, bytes) or not data:
                raise ValueError("input field data must be nonempty bytes")
            fields.append({"name": name, "datatype": "BYTES", "data": base64.b64encode(data).decode("ascii")})
        body = json.dumps({"inputs": fields}).encode("utf-8")

        # An idle keep-alive socket readable before we send means the peer closed it
        # (ChillServer's idle timeout); reopen once here, never after the request is sent.
        sock = self._connection.sock
        if sock is not None and select.select([sock], [], [], 0)[0]:
            self._connection.close()
        headers = {
            "Content-Type": "application/json",
            "Authorization": self._authorization,
        }
        if self._request_lifetime is not None:
            headers["X-Chill-Request-Timeout"] = self._request_lifetime
        self._connection.request(
            "POST",
            f"/api/v1/workloads/{workload_id}/infer",
            body=body,
            headers=headers,
        )
        response = self._connection.getresponse()
        return InferenceResponse(
            response.status,
            response.read(),
            request_id=response.getheader("X-Chill-Request-ID"),
            workload_uid=response.getheader("X-Chill-Workload-UID"),
        )

    def close(self) -> None:
        self._connection.close()
