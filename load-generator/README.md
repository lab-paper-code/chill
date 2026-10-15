# CHILL Load Generator

A dependency-free Python client and open-loop scheduling helpers for CHILL.
The CLI currently prints help; it does not generate traffic.

## Workload inference

Use `WorkloadHTTPClient` against the trusted HTTPS origin of the gateway.
The Workload must already be registered through the management API. Its ID,
not a model name, selects the registered model, preprocessor and SLO. A request
is one raw sample: a mapping from every input field name of the Workload's
preprocessor to that field's bytes (a JPEG file or UTF-8 text). The client sends
each field as `BYTES` with base64 data. Routing and batching remain server-side.

```python
import os
from pathlib import Path

from chill_loadgen.http_client import WorkloadHTTPClient

client = WorkloadHTTPClient(
    "https://chill.example:8443",
    bearer_token=Path(os.environ["CHILL_TOKEN_FILE"]).read_text().strip(),
    timeout_seconds=30,
    ca_file=os.environ.get("CHILL_CA_FILE"),
)
try:
    # Replace these fields with the registered Workload's preprocessor input contract.
    result = client.infer("vqa-service", {
        "image": Path("cat.jpg").read_bytes(),
        "question": "What is in the picture?".encode("utf-8"),
    })
    print(result.status_code, result.request_id, result.workload_uid)
finally:
    client.close()
```

`ca_file` optionally supplies the trusted CA PEM bundle; omitting it uses the
standard system trust store. Certificate and hostname verification are always
enabled. The endpoint must be an HTTPS origin without credentials, a path
prefix, query or fragment. The bearer token must not include the `Bearer `
prefix or header whitespace. Keep tokens and sample payloads out of logs.

`InferenceResponse` retains the raw HTTP status and body, including gateway
rejections such as `401`, `403`, `429`, `503` and `504`. Its optional
`request_id` and `workload_uid` fields expose the gateway correlation headers;
rejections before Workload resolution may have no UID. Transport and TLS errors
remain standard-library exceptions, distinct from HTTP rejection responses.
The client performs no redirects or retries. Use one client per sequential
worker and close it when finished.

## Development

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install .
chill-loadgen
```

Run the dependency-free test suite with:

```sh
PYTHONPATH=src python -m unittest discover -s tests -v
```
