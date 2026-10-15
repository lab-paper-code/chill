# CHILL Load Generator

A dependency-free Python client and open-loop dataset replay CLI for CHILL.

## Dataset replay

`chill-loadgen replay` sends a Dataset's `evaluation` subset to one registered
Workload. Each record is a directory `<dataset-dir>/<recordID>/` holding one
file per input field; `--fields` names the Workload preprocessor's input fields.
Records are read once, ordered by record ID and cycled. Requests follow a fixed
open-loop schedule of `--rate` per second for `--duration` seconds, spread
round-robin over `--workers` connections (default 8), so slow responses do not
slow the offered load until every connection is busy.

```sh
chill-loadgen replay \
  --endpoint https://chill.example:8443 --token-file "$CHILL_TOKEN_FILE" \
  --ca-file "$CHILL_CA_FILE" --workload vqa-service \
  --dataset-dir vqav2/evaluation --fields image,question \
  --rate 5 --duration 60 > vqa.jsonl
```

Each request prints one JSON line on stdout:

```json
{"workload": "vqa-service", "record": "262148000", "scheduledOffsetNs": 200000000, "delayed": false, "status": 200, "requestID": "…", "workloadUID": "…", "latencyMs": 812.4}
```

`latencyMs` is client-observed time from send to response. `delayed` marks a
request sent more than 10 ms after its scheduled time; it means the replay fell
behind the schedule. A transport, TLS or timeout failure replaces `status`,
`requestID` and `workloadUID` with `error`, the exception class name.
Response bodies and tokens are never printed. For a heterogeneous mix, run one
`replay` process per Workload concurrently.

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

Run the dependency-free test suite from the repository root with:

```sh
make test-loadgen
```
