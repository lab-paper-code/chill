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

With the evaluation contract (`--workload-slo` and `--gateway-timeout`), every frozen
arrival starts an independent request on the original timeline; `--workers`
caps pending feedback reports per process, not in-flight evaluation requests.
The injected `hack/evaluation/producer.py` uses
the same dispatcher for its frozen segmented trace. Late arrivals are still
sent and drained, not silently dropped at the issue-window boundary.
Endpoint/token validation and TLS context/CA loading occur once before replay
or the E5 release barrier. Independent per-request HTTPS connections reuse this
immutable context; TLS preparation is not repeated on arrival threads.
EvaluationRun and EvaluationStage freeze the load Job's memory requests and limits
in `spec.loadGenerator.resources` (for example, `requests.memory: 1Gi` and
`limits.memory: 1Gi`); change those quantities in the Run input, not the Job template.
`spec.loadGenerator.shards` maps each workload name to its count of
independent process shards, initialized before its ready event. The parent
releases every shard against one shared start clock. Each shard sends only
global indexes `index % shardCount == shardIndex` from the already-frozen
trace; offsets, record assignment and offered denominator never change.
Shard JSONL rows are merged under one process-shared output lock, retaining
`requestIndex`, `shardIndex` and `shardCount`. Rebuild the load-generator image
for the shared shard dispatcher; runtime images are unchanged.
Results are written and flushed as JSONL on completion, without retaining rows
or completed request threads. A condition counter tracks active requests and
drains them even when scheduling raises; the arrival path never joins a thread.
The counter has its own lock, so blocked output does not block later arrivals.
Workers are daemon threads to avoid Python 3.8 interpreter shutdown-lock
retention, but replay always waits for their output and connection cleanup.
Inference bookkeeping retains concurrent requests, not completed request threads.
The feedback FIFO retains at most `workers` pending reports plus one active
exchange per process; failures stream directly to JSONL without a retained
failure list. The frozen offset trace, per-shard schedule and dataset remain resident.
A failed process shard prevents a complete `drained` event: its missing planned
requests cannot be silently removed from the SLO denominator; already streamed
rows from every shard remain evidence.
`scheduledMonotonicNS` anchors the request; `latenessNs` is actual send minus
scheduled time, a generator-quality metric rather than SLO time.
`latencyMs` and `serviceLatencyMs` retain the caller-bracket duration;
`scheduledLatencyMs` is scheduled-to-completion reference latency only.
`elapsedNanos` is the exact client E2E duration measured inside `infer`, immediately
before sending the prepared request through full response-body read. Preparation,
result formatting and feedback reporting are outside that interval.
`sentAtUnixNS` and `finishedAtUnixNS` support joining server `receivedAt` by
request ID. Send→receipt retains cross-host clock uncertainty; server elapsed
is a separate diagnostic, not the client SLO interval.
Existing fields remain. Offered is the entire planned trace, including failures
and missing issuance. Generator lateness tolerance and allowed exceedance
fraction belong to the frozen experiment config; exceeding them invalidates
generator quality, not the workload SLO.
The native lifetime header always carries the configured `L`: lateness neither
reduces it nor suppresses a send. Thread creation failures produce
`issued=false`, `GeneratorThreadStartError` rows and do not stop later arrivals
or the producer's drained event. They remain in the planned denominator and
fail full-supply judging.

Producer JSONL uses stdout; Python exceptions/tracebacks use stderr. Kubernetes
`logs -f` merges the container's two streams, even when kubectl's own stderr is
captured separately. E5 retains those original client-log lines, skips malformed
or non-object JSON rows during collection/judging, and reports their count and
first line number/content (first 80 characters) in `judgment.json`'s `coverage`.
Missing end counters remain missing evidence; they are never synthesized.

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

Call `encode_inputs` once when loading each record, then reuse its immutable body
with `infer`. Replay consumes the raw record list during preparation before its
start clock; evaluation retains field sizes/hashes, drops raw bytes, and prepares
all bodies before fork so children share the bytes copy-on-write. No base64 or
JSON serialization of sample payloads runs in request threads (which would retain
large glibc malloc arenas under overload). Input fields are validated once during
preparation; JSON field order, spacing and base64 wire bytes are unchanged.

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
