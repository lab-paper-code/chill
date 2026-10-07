"""Verified HTTPS client for CHILL's one-sample Workload inference API."""

from __future__ import annotations

import ipaddress
import json
import math
import re
import ssl
from dataclasses import dataclass
from http.client import HTTPSConnection
from typing import Dict, Optional, Sequence
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

        self._authorization = "Bearer " + bearer_token
        self._connection = HTTPSConnection(
            hostname,
            port,
            timeout=timeout_seconds,
            context=ssl.create_default_context(cafile=ca_file),
        )

    def infer(
        self, workload_id: str, inputs: Sequence[Dict[str, object]]
    ) -> InferenceResponse:
        """Send named tensors with sample shapes, never a client batch axis."""
        if not _valid_identifier(workload_id):
            raise ValueError("workload_id must be a Kubernetes DNS subdomain name")
        if not isinstance(inputs, (list, tuple)) or not inputs:
            raise ValueError("inputs must be a nonempty sequence of named sample tensors")
        names = set()
        for tensor in inputs:
            if not isinstance(tensor, dict) or set(tensor) != {
                "name", "datatype", "shape", "data"
            }:
                raise ValueError("each input must contain only name, datatype, shape and data")
            name, datatype = tensor["name"], tensor["datatype"]
            shape, data = tensor["shape"], tensor["data"]
            if not isinstance(name, str) or not name or name in names:
                raise ValueError("input names must be nonempty and unique")
            names.add(name)
            if not isinstance(datatype, str) or datatype not in {
                "BOOL", "BYTES", "INT8", "INT16", "INT32", "INT64",
                "UINT8", "UINT16", "UINT32", "UINT64", "FP16", "FP32", "FP64",
            }:
                raise ValueError("input datatype is not supported by the JSON API")
            if not isinstance(shape, list) or any(
                type(dimension) is not int or not 0 < dimension <= 2147483647
                for dimension in shape
            ):
                raise ValueError("input shape must contain positive int32 sample dimensions")
            count = 1
            for dimension in shape:
                count *= dimension
            if not isinstance(data, list) or len(data) != count or any(
                type(value) not in (bool, str, int, float) for value in data
            ):
                raise ValueError("input data must be a flat array matching its sample shape")
        try:
            body = json.dumps({"inputs": inputs}, allow_nan=False).encode("utf-8")
        except (TypeError, ValueError, UnicodeError):
            raise ValueError("inputs must contain finite JSON values") from None

        self._connection.request(
            "POST",
            f"/api/v1/workloads/{workload_id}/infer",
            body=body,
            headers={
                "Content-Type": "application/json",
                "Authorization": self._authorization,
            },
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
