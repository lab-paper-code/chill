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

### Evaluation scheduled-time replay

Paired `--workload-slo` and `--gateway-timeout` enable independent scheduled
arrivals. Each frozen arrival starts on its original timeline, including late
arrivals. `--workers` bounds pending feedback per process; inference concurrency
follows the offered trace. `hack/evaluation/producer.py` uses the same dispatcher.
Endpoint/token validation and TLS preparation occur once before replay.

EvaluationRun and EvaluationStage freeze load Job memory requests and limits in
`spec.loadGenerator.resources`, and per-Workload process counts in
`spec.loadGenerator.shards`. Each shard sends indexes
`index % shardCount == shardIndex` against one shared clock. Offsets, record
assignment and offered denominator stay frozen. JSONL rows retain `requestIndex`,
`shardIndex` and `shardCount`. A failed shard prevents a complete drained event;
partial rows remain evidence.

Results stream on completion. Replay drains active requests and feedback even
when scheduling raises. Thread-start failures record `issued=false` and
`GeneratorThreadStartError`, remain in the planned denominator, and do not stop
later arrivals. Generator lateness neither reduces the configured native lifetime
nor suppresses an arrival.

`scheduledMonotonicNS` anchors the request; `latenessNs` records send lateness.
`latencyMs` and `serviceLatencyMs` bracket the caller; `scheduledLatencyMs` is
scheduled-to-completion reference latency. `elapsedNanos` measures send through
full response-body read inside `infer`. Preparation, formatting and feedback are
outside this exact interval. `sentAtUnixNS` and `finishedAtUnixNS` support
request-ID joins; cross-host differences retain clock uncertainty.

Producer JSONL uses stdout; Python exceptions use stderr. Kubernetes logs may
merge them. Evaluation preserves raw lines and reports malformed/non-object
counts. Missing end counters remain missing.


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

The dispatch-occupancy controller freezes warmup, burst duration, workers,
minimum pairs, lifetime, delivery margin, setup, preparation, drain and collection
conditions. Its finite Job budget includes post-window supply extension as well
as burst and drain. Use the server-built task rather than reconstructing its
budget in a client. Lifetime expiry does not interrupt a running physical batch.

ChillServer's `--request-timeout` caps the header and HTTP context.
`--drain-timeout` bounds shutdown drain; configure Pod termination grace to
exceed it. Align dashboard `CHILL_INFER_TIMEOUT_MS` with the server cap; the relay
adds its query budget for delivery.

### Workload-bound evaluation replay

Supply explicit Workload SLO, request lifetime, gateway cap and socket delivery
budget in frozen evaluation input. The CLI requires `--workload-slo` and
`--gateway-timeout` together. If `--request-lifetime` is supplied, it checks
`L >= SLO`, `cap >= L` and `socket > L + delivery-margin`. The paired options
themselves do not require a lifetime. With lifetime supplied, evaluation socket
default is `L + 2*delivery-margin`; ordinary replay uses `L + delivery-margin`.

```sh
chill-loadgen replay \
  --endpoint https://chill.example:8443 --token-file "$CHILL_TOKEN_FILE" \
  --ca-file "$CHILL_CA_FILE" --workload image-classification-tight-slo \
  --dataset-dir vqav2/evaluation --fields image --arrivals poisson \
  --rate 50 --duration 300 --workers 320 --seed 101 \
  --workload-slo 1 --request-lifetime 2 --gateway-timeout 129 \
  --delivery-margin .01 --timeout 2.02
```

The campaign binds declared values to actual Workload UID/generation and gateway
settings. The CLI checks numeric relationships without fetching server settings.



Each request prints one JSON line on stdout:

```json
{"workload": "vqa-service", "record": "262148000", "scheduledOffsetNs": 200000000, "delayed": false, "status": 200, "requestID": "…", "workloadUID": "…", "sentMonotonicNS": 1000000000, "finishedMonotonicNS": 1812400000, "latencyMs": 812.4}
```

`latencyMs` brackets the inference call and transport-error handling. `delayed`
marks a request sent more than 10 ms after its scheduled time.
`elapsedNanos` preserves exact successful full-body duration or the observed
lower bound before a timeout/transport failure. `outcome` is `completed`,
`http_error`, `timeout` or `transport_error`. `error` retains the original
exception class. Any received `status`, `requestID` and `workloadUID` survive
partial-body failures; absent headers stay unknown.
Full non-2xx JSON responses preserve optional string `code`, `stage` and `policy`
as `rejectionCode`, `rejectionStage` and `rejectionPolicy` in the client row.
Parsing is limited to bodies of at most 64 KiB; malformed bodies omit metadata.
These fields do not change `http_error` or the feedback API payload.
Response bodies and tokens are never printed. For a heterogeneous mix, run one
`replay` process per Workload concurrently.

`sentMonotonicNS` and `finishedMonotonicNS` bracket the inference call and
transport-error handling on the same process clock. They are not socket-write,
gateway-receipt or stdout timestamps. Sort send timestamps across workers to
measure issuance gaps; completion/JSONL order is not send order. JSON encoding
runs outside the shared output lock, while one whole line plus flush remains
locked. Workers still wait for their own output before refilling; this is not
an asynchronous logger or a claim that observed long gaps have been cured.

Each issued exchange gets an opaque `observationID` before inference. Replay
queues its immutable evidence after measurement to one reporting worker using
a separate connection and the existing socket timeout. Its bounded queue reuses
the existing `workers` budget in every replay process; there is no new knob.
Enqueue never waits for space. If full, the inference row retains its exact E,
IDs, status and outcome with `feedbackStatus:"not_reported"` and
`feedbackError:"ReporterBackpressure"`; that feedback is not sent or retried.
`ReporterClosed`/`ReporterUnavailable` similarly record an unavailable sink.
Scheduled arrivals and closed-loop refills do not await feedback HTTP.
Shutdown drains inference workers, then the bounded feedback backlog, including
when scheduling raises. Repeated reporter close is safe.
Reporting never changes inference outcome. `feedbackStatus:"queued"` means
queued, not acknowledged; a failed attempt immediately emits a separate
`clientFeedbackEvent:"failed"` JSONL event with `observationID`, `workload`
and `error` through the existing thread-safe output boundary. Normal JSONL
output backpressure still applies. These events are not inference rows.
No retries, backoff, disk spool or extra reporting configuration are used.
Unsent generator work (`issued:false`) produces no inference feedback.

External clients can use `report_observation(workload_id, observation)` on an
independent `new_connection()`. It posts authenticated JSON to
`/api/v1/workloads/{id}/feedback`, where `{id}` is the inference Workload name.
The public body contains `observationID`, integer `elapsedNanos`, `outcome`,
and optional `requestID`, `workloadUID`, `statusCode`. Outcomes are `completed`
(full 2xx), `http_error` (full non-2xx), `timeout`, `transport_error`, `canceled`.
Only include status/identities actually received. A timeout without headers
still reports its observation identity and duration; never invent a server join.
The endpoint returns 204 on acceptance. Existing feedback POST authorization
is required. IDs are opaque; client reports contain no server/native duration.

New offline evaluations use exact `elapsedNanos` from a full successful 2xx
client response for `completedLatencyNS` and deadline success. Existing server
`completed`/`executionComplete`, identity quarantine, observation-gap and
generator-quality guards remain. `gatewayCompletedLatencyNS` retains server
final elapsed as a separate diagnostic. `actualLatencyNS` and scheduled
latency keep their reference meanings; neither substitutes for missing exact E.
`outcomes` now counts client outcomes; `serverOutcomes` keeps server facts.
`clientFailures` includes issued HTTP errors, timeouts, transport errors and
cancellations, including failures without a RequestID. These remain in the
offered denominator, never successful latency samples. Timeout E is a censored
lower bound. The requested SLO quantile uses `quantileNanos:null` and
`quantileStatus:"unbounded"` when its rank reaches the failure tail; finite
quantiles use `"finite"`. Missing exact client evidence stays `"unknown"`,
counted by `clientUnknown`, without synthesizing completion or timeout.
Unsent generator work stays a quality fact. Previously stored judgments and
raw evidence are not rewritten or reinterpreted.


Evaluation retains actual/planned gaps and send lateness as `issuanceFidelity`.
Sparse planned arrivals alone do not imply a stall. Missing timing or incomplete
offered-row evidence stays unknown. See the
[observability design](../docs/design/serving/observability-split.md).

## Workload inference

Use `WorkloadHTTPClient` against the trusted HTTPS origin of the gateway.
The Workload must already be registered through the management API. Its ID,
not a model name, selects the registered model, preprocessor and SLO. A request
is one raw sample: a mapping from every input field name of the Workload's
preprocessor to that field's bytes (a JPEG file or UTF-8 text). The client sends
each field as `BYTES` with base64 data. Routing and batching remain server-side.

Call `encode_inputs` once while loading each record and reuse the immutable body
with `infer`. Replay prepares inputs before its start clock; payload encoding
does not run on arrival threads.

```python
import os
from pathlib import Path

from chill_loadgen.http_client import WorkloadHTTPClient, encode_inputs

client = WorkloadHTTPClient(
    "https://chill.example:8443",
    bearer_token=Path(os.environ["CHILL_TOKEN_FILE"]).read_text().strip(),
    timeout_seconds=30,
    ca_file=os.environ.get("CHILL_CA_FILE"),
)
try:
    # Replace these fields with the registered Workload's preprocessor input contract.
    body = encode_inputs({
        "image": Path("cat.jpg").read_bytes(),
        "question": "What is in the picture?".encode("utf-8"),
    })
    result = client.infer("vqa-service", body)
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
rejections before Workload resolution may have no UID. `elapsed_nanos` holds exact
send/full-read duration. Interrupted HTTP, transport and TLS exchanges raise
`InferenceRequestError`, an `OSError` subclass retaining `elapsed_nanos`, `outcome`,
optional `status_code`/`request_id`/`workload_uid`, and the original exception as
`__cause__`. These remain distinct from full HTTP rejection responses.
The client performs no redirects or retries. Use one client per sequential
worker and close it when finished.

## Development

Run the installation commands from `load-generator/`:

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
