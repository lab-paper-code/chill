# CHILL Load Generator

A dependency-free Python client and open-loop or fixed-outstanding dataset replay CLI for CHILL.

## Image

`make docker-build-load-generator` builds `build/docker/load-generator.Dockerfile`
(Python 3.12). ChillServer runs it as the load Job of a
DispatchOccupancyMeasurement.

## Dataset replay

`chill-loadgen replay` sends a Dataset's `evaluation` subset to one registered
Workload. Each record is a directory `<dataset-dir>/<recordID>/` holding one
file per input field; `--fields` names the Workload preprocessor's input fields.
Records are read once, ordered by record ID and cycled. Requests follow an
open-loop schedule of `--rate` per second for `--duration` seconds, spread
round-robin over `--workers` connections (default 8), so slow responses do not
slow the offered load until every connection is busy. No request is sent after
`--duration` seconds, so a sender behind schedule ends on time. `--arrivals fixed`
(default) sends at a constant interval; `--arrivals poisson` draws exponential
gaps of mean `1/--rate` from `--seed` (default 0), so the same seed repeats the
same send times.

### E5 scheduled-time replay

With the E5 contract (`--workload-slo` and `--gateway-timeout`), every frozen
arrival starts an independent request on the original timeline; `--workers`
does not cap in-flight E5 requests. The injected `hack/e5/e5-producer.py` uses
the same dispatcher for its frozen segmented trace. Late arrivals are still
sent and drained, not silently dropped at the issue-window boundary.
`scheduledMonotonicNS` anchors the request; `latenessNs` is actual send minus
scheduled time, a generator-quality metric rather than SLO time.
`latencyMs` and `serviceLatencyMs` retain actual-send HTTP duration;
`scheduledLatencyMs` is scheduled-to-completion reference latency only.
`sentAtUnixNS` and `finishedAtUnixNS` support joining actual server `receivedAt`
by request ID: send→receipt is entrance delay (cross-host clocks require
uncertainty evidence), while receipt→response is the routing SLO interval.
Existing fields remain. Offered is the entire planned trace, including failures
and missing issuance. Generator lateness tolerance and allowed exceedance
fraction belong to the frozen experiment config; exceeding them invalidates
generator quality, not the workload SLO.
The native lifetime header always carries the configured `L`: lateness neither
reduces it nor suppresses a send. Thread creation failures produce
`issued=false`, `GeneratorThreadStartError` rows and do not stop later arrivals
or the producer's drained event. They remain in the planned denominator and
fail full-supply judging.

The `feat/loadgen-async` branch was inspected before this cutover: it dispatches
independent asyncio tasks, but caps attempts at `workers`, marks excess arrivals
`not_issued`, stops late sends at the issue boundary, and measures latency from
actual issue time. Its aiohttp/scenario API also removes the current occupancy
and E5 lifetime contract. The minimal port retains its independent-timeline
dispatch design using stdlib request threads, without importing that incompatible
client/API or its cap. Closed-loop occupancy replay is unchanged.
Inspection baseline: branch tip `f419192e`; `main...feat/loadgen-async`
contains 47 changed files (+7411/-610). A three-way merge against base
`9c29bf96` reports conflicts in README, CLI, HTTP client, CLI tests and HTTP
client tests. No branch merge or compatibility shim is introduced.

Rebuild and publish the **load-generator image** after this change; producer
injection alone cannot install the new dispatcher. No runtime-7 image rebuild
is required.

```sh
chill-loadgen replay \
  --endpoint https://chill.example:8443 --token-file "$CHILL_TOKEN_FILE" \
  --ca-file "$CHILL_CA_FILE" --workload vqa-service \
  --dataset-dir vqav2/evaluation --fields image,question \
  --rate 5 --duration 60 > vqa.jsonl
```

### Fixed outstanding measurement supply and lifetime

For dispatch-occupancy measurements, `--arrivals closed-loop --workers K` keeps
one sequential request per connection and immediately replaces each completed
response, including HTTP rejections or transport failures. There is no arrival
schedule, rate or seed in this mode; `--rate` is rejected. The ordinary fixed
and Poisson paths retain their existing scheduling and defaults.

```sh
chill-loadgen replay \
  --endpoint https://chill.example:8443 --token-file "$CHILL_TOKEN_FILE" \
  --ca-file "$CHILL_CA_FILE" --workload smol-service \
  --dataset-dir smol/evaluation --fields image,question \
  --arrivals closed-loop --workers 2 --duration 420 \
  --request-lifetime 75 --delivery-margin .01 > smol-d.jsonl
```

`--duration` bounds new sends, not the last response: all outstanding requests
are joined after sending stops. Closed-loop `scheduledOffsetNs` is its actual
refill offset and `delayed` is false. This is a supply shape, not proof of native
saturation or physical completion; rejected requests and missing native evidence
remain visible to the measurement controller.

`--request-lifetime L` (seconds, optional) sends the existing
`X-Chill-Request-Timeout` Go-duration header on every request.
`--delivery-margin` defaults to 0.01 seconds. Without `--timeout`, the socket
budget is `L + delivery-margin`; an explicit timeout must cover that sum.
Without a lifetime the existing 30-second socket default and absent header are
unchanged. The Python API exposes the same header as
`request_lifetime_seconds`; `timeout_seconds` must exceed it.

The DispatchOccupancyMeasurement controller consumes the frozen CR conditions:
`workers=K`, `warmupSeconds=W_load`, `durationSeconds=T`,
`requestLifetimeSeconds=L`, `deliveryMarginMillis`, `drainSeconds=D`,
`preparationSeconds=ceil(P)`, `jobSetupSeconds` and `collectionSeconds`.
It sends for `W_load + T` after preparation/readiness, records only the
measurement interval after priming, and gives the Job the finite sum
`setup + preparation + W_load + T + D + collection`. The profile/SLO calculation
and empirical tail assumptions belong to the immutable CR/evidence, not a
second calculation in this CLI. A socket or lifetime expiry is not an
interruption of an already running native physical batch.

Existing serving consumers need configuration alignment, not a wire change:
ChillServer's `--request-timeout` caps the explicit header and its outer HTTP
context; the existing native adapter already forwards the selected lifetime,
sender elapsed time, margin and separate SLO. `--drain-timeout` bounds manager
and process shutdown drain; Pod termination grace must exceed it. For the
approved largest scenario, cap 129s and drain at least 164s must be reflected in
the serving chart. The dashboard's existing `CHILL_INFER_TIMEOUT_MS` accepts
129000ms; its relay adds the unchanged query budget for delivery and the browser
has no independent 30/60-second inference cap. Metadata query and asynchronous
management-mutation budgets remain independent. No firstfloor/runtime change
or rebuild is required.

### Workload-bound E5 Poisson replay

E5 uses the same executable with explicit frozen Workload SLO, request
lifetime, gateway cap and socket delivery budget. The paired
`--workload-slo` and `--gateway-timeout` options require an explicit
`--request-lifetime`. Before loading records or sending traffic the CLI checks
`L >= SLO`, `cap >= L` and `socket > L + delivery-margin`. When omitted, the
E5 socket default is `L + 2*delivery-margin`. Measurement replay retains its
frozen `L + delivery-margin` default.

```sh
chill-loadgen replay \
  --endpoint https://chill.example:8443 --token-file "$CHILL_TOKEN_FILE" \
  --ca-file "$CHILL_CA_FILE" --workload image-classification-tight-slo \
  --dataset-dir vqav2/evaluation --fields image --arrivals poisson \
  --rate 50 --duration 300 --workers 320 --seed 101 \
  --workload-slo 1 --request-lifetime 2 --gateway-timeout 129 \
  --delivery-margin .01 --timeout 2.02
```

The campaign freezes these declared values against the actual Workload
UID/generation and deployed gateway settings. The CLI validates their
relationship; it does not fetch or infer server configuration. Every full,
static and control-element comparison reuses the same bound lifetime tuple.
PLC/Smol values come from their approved E5 inputs. Generic replay without the
E5 options keeps its existing defaults. Native delivery forwards original L,
sender elapsed, separate SLO and m; remaining lifetime subtracts sender elapsed
and m once.



Each request prints one JSON line on stdout:

```json
{"workload": "vqa-service", "record": "262148000", "scheduledOffsetNs": 200000000, "delayed": false, "status": 200, "requestID": "…", "workloadUID": "…", "sentMonotonicNS": 1000000000, "finishedMonotonicNS": 1812400000, "latencyMs": 812.4}
```

`latencyMs` is client-observed time from send to response. `delayed` marks a
request sent more than 10 ms after its scheduled time; it means the replay fell
behind the schedule. A transport, TLS or timeout failure replaces `status`,
`requestID` and `workloadUID` with `error`, the exception class name.
Response bodies and tokens are never printed. For a heterogeneous mix, run one
`replay` process per Workload concurrently.

`sentMonotonicNS` and `finishedMonotonicNS` bracket the inference call and
transport-error handling on the same process clock. They are not socket-write,
gateway-receipt or stdout timestamps. Sort send timestamps across workers to
measure issuance gaps; completion/JSONL order is not send order. JSON encoding
runs outside the shared output lock, while one whole line plus flush remains
locked. Workers still wait for their own output before refilling; this is not
an asynchronous logger or a claim that observed long gaps have been cured.

E5 retains boundary-inclusive actual/planned gaps, intervals over 50 ms, and
send lateness as `issuanceFidelity` in its judgment, campaign summary and
published KPIs. Intended sparse Poisson/zero-load gaps are not automatically
stalls; missing timing or incomplete offered-row evidence is unknown.
See [observability design §16](../docs/design/serving/observability-split.md#16-u6-부하-발행-공백과-trace-충실도)
for the retained batch-3 findings and interpretation limits. Using the changed
CLI requires rebuilding the **load-generator image** and updating its pinned
digest/receipt; it does **not** require rebuilding the seven runtime images.

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

The real-local-HTTP smoke uses an ephemeral trusted TLS certificate generated
by `openssl` and a stdlib HTTPS server. It checks three concurrent outstanding
requests, prompt refill, the 129s header/socket contract and completion of the
last response beyond the send window. It needs no cluster or image build:

```sh
PYTHONPATH=load-generator/src python3 -m unittest discover \
  -s load-generator/tests -p test_local_http.py
```
