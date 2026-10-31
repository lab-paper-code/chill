# CHILL Dashboard

CHILL-internal Next App Router module, built independently and deployed within the `chill` Helm release. ChillServer remains authoritative for identity, authorization, declarations, operations, statistics and evidence. This is the approved current core, not completion of every Dashboard SRS producer.

## Fixed upstream source and license

Actual source fork: https://github.com/shadcn-ui/ui.git at commit `e8c3143b1cd191280befcd6c9538284bb43399a8`.

- Application baseline: `templates/next-app` (package, TypeScript/ESLint/PostCSS/Next configuration, layout, theme provider).
- Selected block: `apps/v4/registry/new-york-v4/blocks/dashboard-01` (`app-sidebar`, `site-header`, `data-table`, `chart-area-interactive`, and page shell composition). The `section-cards` block is replaced by the CHILL overview board.
- Retained copied dependency closure: `apps/v4/registry/new-york-v4/ui/{sidebar,button,input,separator,sheet,tooltip,table,chart,dialog}.tsx`, `hooks/use-mobile.ts`; theme tokens from `apps/v4/app/globals.css`. Unused demo primitives are deleted.
- `LICENSE.shadcn` preserves the upstream MIT copyright and permission notice for copied/adapted source. It does not relicense other CHILL code.

The upstream demo records, accounts/avatars, revenue/time series, marketing links, drag-and-drop and sidebar cookies are removed. Asta Sans (Hangul-capable) is fetched by `next/font` at build time and self-hosted under `/_next/static/media`, so the CSP keeps `font-src 'self'` and the browser makes no font requests to third parties. Table retains TanStack Table v9. Charts use processed ChillServer statistics, live 1-second buckets or samples of one selected power record. Synthetic S2 acceptance scenes are explicitly labelled 검사용 and gated by `CHILL_DEMO_REPLAY=1`; they are not experimental evidence.

## Visual system

Tokens live in `app/globals.css`: a cool frost canvas, coolant blue for actions and focus, and heat amber for energy and caution. Status is never color-only: `StatusBadge` pairs each tone with an icon and text. Every screen does one function, built from `components/screen.tsx`: a back link, one plain-Korean conclusion as the headline, one key value or list, `›` rows that open the next screen or a dialog, and at most one primary action pinned at the bottom of a narrow centered column. Multi-field input is a step funnel; the scheduler settings are rows edited one value per dialog and applied after one review. Internal codes, UIDs and nanoseconds appear only in 원본 자료 dialogs; times display in local time. Signed-out visitors see only the sign-in screen. Light, dark and 390 px layouts were reviewed against fixture-backed ChillServer responses; no live ChillServer was written.

Experiment interval drawers restore their stage pair from `a/as/b/bs` URL parameters and clear selection parameters on close. Comparison limits are marked beside interval identifiers, with load, service identity and measurement-rule states in a condition table. Differences below 0.01 display units remain neutral in interval cells and overview chips. Measurement evidence preserves failed/unknown power-source checks; rejected request guidance appears once with numeric constraints in tables and short field errors.

## Runtime

Node 24.21.0; pnpm 10.33.4. Dependencies are frozen in `pnpm-lock.yaml`. `pnpm install --frozen-lockfile`, then `pnpm lint`, `pnpm typecheck`, `pnpm test`, `pnpm build`. `pnpm start` binds `0.0.0.0:3000`. Next uses `output: standalone`; a standalone image must include `.next/standalone`, `.next/static` and `public` if present and set `HOSTNAME=0.0.0.0`, `PORT=3000`.

Server runtime environment (not browser build-time variables):

| Name | Contract |
| --- | --- |
| `CHILL_BASE_URL` | Required single HTTPS ChillServer origin; no path, credentials, query or fragment |
| `CHILL_NAMESPACE` | Required real namespace |
| `CHILL_SYSTEM_NAME` | Required real System name |
| `CHILL_QUERY_TIMEOUT_MS` | Default `10000` |
| `CHILL_MUTATION_TIMEOUT_MS` | Default `60000` |
| `CHILL_INFER_TIMEOUT_MS` | Default `30000`; must match ChillServer's native request cap |
| `CHILL_MAX_REQUEST_BYTES` | Default `8388608`; request and response bound, align ChillServer request cap |
| `CHILL_PAGE_SIZE` / `CHILL_MAX_PAGE_SIZE` | Default `50` / `200` |
| `CHILL_MAX_WINDOW_HOURS` | Default `24` |
| `CHILL_POLL_INTERVAL_MS` | Default `5000` |
| `CHILL_DEMO_REPLAY` | Default disabled; exact `1` enables authenticated synthetic live scene controls |
| `NODE_EXTRA_CA_CERTS` | Native custom CA file, e.g. `/etc/chill/ca/ca.crt`; no trust-any fallback |

`GET /api/config` returns safe display/bound configuration only. `/api/chill/v1/...` relays to `/api/v1/...` at the fixed ChillServer with native Node HTTPS verification, no redirect following, no cookie/identity forwarding, bounded bytes/time and abort propagation. Incomplete responses never appear as successful partial results. Relay failures have distinct Dashboard codes and `unknownOutcome`; `X-Chill-Relay-Timing` covers the full relay including CHILL, not BFF-only overhead or native inference latency. Malformed configuration returns no-store `503 DashboardConfigInvalid` without disclosing environment values.

Inference relay lifetime is `CHILL_INFER_TIMEOUT_MS + CHILL_QUERY_TIMEOUT_MS` (default 40 seconds); the native inference timeout remains capped at 30 seconds by default. This bounded delivery margin allows ChillServer's native timeout status/body to reach the browser without relabeling it as a Dashboard timeout.

### Scheduler API and caller permissions

The BFF exposes GET `v1/scheduler`, `v1/scheduler/settings`, `v1/scheduler/state`,
PUT `v1/scheduler/settings`, POST `v1/scheduler/operations`, GET
`v1/observations/load` and GET `v1/observations/scheduler`. Both observation routes require explicit RFC3339Nano
`from` and `to`; load has no paging input. Scheduler history accepts bounded
`limit` and nonnegative `cursor` in retained append order, not a total count.
The relay reads GET `v1/scheduler`, `/state`, `/profiles` and scheduler history
with ChillServer `view=compact` (the only accepted `view`), and `parseSchedulerJSON`
restores the shared `profile` `$ref`s into the full
response before any screen reads it; a missing reference is an error.
Settings PUT sends the Settings object itself; ObservationLog and ConfigMap
storage remain authoritative. There is no version precondition or dry-run.
Manual operations accept only POST with no query parameters and use
`CHILL_MUTATION_TIMEOUT_MS`. The JSON request and upstream status/body pass through
unchanged; operation results (including failed/partial statuses) remain distinct
from API errors and relay failures with `unknownOutcome`.

Use an existing administrator-approved account, not the Dashboard ServiceAccount.
`chill-serving-manager` grants the exact scheduler GET paths and observation
reads. The unbound `chill-scheduler-editor` ClusterRole grants the settings
PUT and manual operations POST paths. When `servingEntrypoint.scheduler.configMap`
is selected, a same-name unbound Role in the serving namespace grants `update`
of that named ConfigMap only. An administrator explicitly binds the read
ClusterRole, editor ClusterRole and named ConfigMap Role to the chosen account.
Both writes need both editor grants; a path grant alone cannot update the ConfigMap.
For a ServiceAccount, obtain a short-lived token with
`kubectl -n <account-namespace> create token <existing-account> --duration=1h`
after the administrator has confirmed its bindings. Tokens expire and are kept
only in this browser tab. No automatic bindings or permanent token are created.

Scheduler decoding uses native JSON reviver source and JSON.rawJSON to preserve
int64 seed, uint64 memory and durations exactly. Editing requires a browser with
these native features; there is no rounded Number fallback. Original exports
retain integer literals while removing credentials.


## Public routes and current core

`/`, `/workloads`, `/workloads/new`, `/workloads/[name]`, `/workloads/[name]/{definition,evidence,placement,operation,changes}`, `/workloads/[name]/edit`, `/workloads/[name]/infer`, `/models`, `/models/new`, `/models/[name]`, `/models/[name]/{files,services}`, `/models/[name]/edit`, `/scheduler`, `/scheduler/candidates`, `/scheduler/estimates`, `/scheduler/load`, `/scheduler/profiles`, `/transitions`, `/transitions/[id]`, `/resources`, `/pools`, `/pools/[id]`, `/records`, `/observations`, `/changes`, `/scheduler/history`, `/scheduler/power`, `/scheduler/compare`, `/measurements`, `/measurements/new`, `/measurements/batch-energy-optimizations/[namespace]/[name]`, `/measurements/runs`, `/measurements/group`, `/measurements/check`, `/measurements/profiling-runs/[namespace]/[name]`, `/experiments`, `/settings`, `/configuration`.

The menu has seven sections: 개요, 측정, 실험, 서비스, 운영, 기록, 설정. Home summarizes server-assessed measurement coverage, every paired experiment stage and next actions; the live flow board is `/operations/flow`. Measurement defaults to coverage (`/measurements`, also `/measurements/coverage`) with six visible links: coverage, requests (`/measurements/requests`), runs, profiles, operation costs and dispatch occupancy. Requests use one editing surface and explicit same-page preflight/dry-run review. A service page states its readiness and offers the next step; its definition, evidence, placement, last-hour operation and changes are separate screens.

Bearer tokens use versioned, system-scoped sessionStorage in the current tab; storage denial falls back to memory. Refresh restores the session after configuration hydration. “이 탭에서 토큰 지우기”, identity changes and HTTP 401 / WS 4401 clear that tab's token, queries, requests and live connection. HTTP 403 / WS 4403 and service failures retain the destination URL and do not expire the token. Duplicated/restored tabs may inherit storage; clearing a tab does not revoke the bearer or other tabs.

Coverage consumes the exact namespaced aggregate GET with only `batches` and `node` upstream; other filters stay in the URL/browser. Entry and explicit refresh fetch snapshots without periodic polling. A first `CoverageWarming` response permits one Retry-After completion check. The server's five display statuses and exact variant result rows are shown without browser status derivation or all-Run crawling. At 768px and above details remain below the matrix; smaller screens use one dialog and device/path selectors.

Real Model/Workload CRUD and version preconditions, one independent raw-sample inference (JPEG fields from a file, UTF8 fields from text, sent as base64 `BYTES` and answered with output fields decoded by format), current POOL state, System/Inventory/ProfilingRun bounded queries, verified Passport with per-batch dataset record IDs, units and stage times, sealed campaign result and read-only preflight are wired. Observation selection preserves UID, original UTC `from`/`to`, repeated definition/outcome filters and cursor; cohort statistics remain the server's whole-window values, separate from raw paging. JSON exports preserve source identity/conditions/units and redact credentials. Changes uses the real per-Workload management observations; absence of a broader feed is disclosed.

The Model editor preserves firstfloor queue/batch limits and selects
`deadlinePolicy=drop|keep|defer` (omitted means `keep`). Changing this frozen
setting replaces the native instance; it is not an in-place scheduler policy
update. Deferred requests remain waiting, not running or completed. The actual
local editor was exercised with all three values and existing limits preserved;
the browser used an isolated ChillServer-contract fixture and performed no live writes.

`/scheduler` shows only the current judgment; candidates, per-service estimates,
event-time load and actions are separate screens. A candidate detail dialog
freezes the response it was opened on and announces newer decisions.
Automatic application and evaluated judgment are separate; keep/target J and SLO are
predictions, not measurements. Load preserves the submitted window in the page URL
independently of the draft. ChillServer omits arrivals from load responses, so load
links query all definitions for the original UID/window; candidate SLO links
include the provided immutable definition. Request details separately display
CHILL dispatch and native progress, including exact uint64 batch identity.
Service labels share a UID resolver across observations, load, overview and routing.
Deleted registrations use retained observation definitions or scheduler snapshot
workloads; unresolved labels show `이름 미확인` with the first eight UID characters.
Load reads definitions with the existing per-workload observation API (one request
row per UID, no server change). Each request's model comes only from its exact
`definitionID`; a changed current model never rewrites historical request details.
`/configuration` edits every ChillServer-supported setting from the current baseline.
Duration fields use exact ns conversion from ms, memory and seed retain integer
strings, and advanced search fields remain available. Review re-reads settings,
preserves the draft on concurrent changes and names whole-object last-write
semantics. Runtime application is separate from physical transitions. Permission
failure retains input; response loss requires current settings and bounded
history lookup before explicit resubmission.
`/transitions` separates current actions/results from bounded historical records.
Exact record links retain submitted from/to, append offset and expected identity;
same action IDs with different node/start times are not merged. `/scheduler/history`
selects A/B decisions, `/scheduler/compare` restores those exact records and queries
ChillServer whole-window request cohorts by UID/definition/input window. It does not
recompute percentiles, sum energy or assign experimental scores.
`/scheduler/power` requests ChillServer-calculated whole/active POOL joules and mean watts
from `GET /api/v1/observations/power?from=…&to=…` over the exact selected interval.
ChillServer integrates retained raw samples with recorded coverage policies, includes
configured inactive Nodes, counts each Node once, and uses historical actual
Serving membership for the active subtotal. Missing samples/history remain null
with explicit reasons; measured 0 is not missing. Raw node/meter/scope records and
one-record W charts remain separate from these totals. `/changes` keeps
Workload history by default; `scope=scheduler` selects settings actor/before/after
and runtime results. Kind/node filters apply only to the fetched page; an empty
filtered page can still advance. Exports name the selected-record/current-page
scope. Identity changes clear selections; first login retains bookmarked records.

`/experiments` reads immutable published runs/stages from `GET /api/v1/experiments`
and `/{id}`. The A/B selectors retain two runs and two stages in the URL. Only
publisher `kpis.workloads` (demo) and `kpis.requests` (E5) shapes are interpreted;
original KPIs, numerator/denominator, populations, sources and missing reasons
remain available. Empty values stay blank, including identities absent from old
publications; names are never rejoined to current Workloads. Only matching recorded
UID/definition rows align across runs. Offered counts, server receipt cohorts and
live terminal buckets are explicitly distinct. Shared POOL energy is not summed
across rows; published gap bounds render as `value ± bound J`, with energyCoverage.
Different measurementRules retain numbers and display “측정 규칙 다름”.
Evidence lookup uses the exact nanosecond `decision.at` interval through the
processed scheduler observations API, then selects the recorded judgment identity;
it never falls back to the latest state. No-record and timeout states are distinct.
These reads do not poll. Serving-manager grants include experiment GETs and both
measurement projections' GET plus native get/list; chart grants remain unbound.

### S2 live flow board

`/` shows the fixed CHILL data/control paths, one lane per workload UID, six
independent instance symbols grouped by Node, and a shared decision/action/cluster
event lane. `/pools/[uid]` opens the complete metric card and four aligned lanes:
load, success/latency, shared POOL power and events. `?open=inst:<id>`,
`?open=node:<uid>` and `?open=reservations` retain level-2 expansions;
`?at=<original decision.at>` retains the nonmodal evidence aside. Closing restores
the opener's focus, with the heading as the refresh fallback. The back-to-board
link restores the selected POOL lane.

The stage uses a dark neutral machine-flow surface with blue data paths, amber
power and shape-plus-text instance states. Narrow screens stack the paths and
evidence; keyboard focus remains visible and reduced motion disables emphasis.
The common strip reports connection/baseline freshness, each workload's autonomous
setting, received versus observed time, elapsed age, and explicit UTC/KST.
Heartbeat receipt alone never makes an old observation fresh.

The authenticated `/api/live` WebSocket sends the token only in its first auth
frame. The persistent per-session QueryClient owns the projection and bounded
browser-received timeline. Route changes between board and POOL do not discard
that timeline. Reconnect uses the exact `(seq, at)` checkpoint; an out-of-range
snapshot restores current state and leaves the missing interval blank. Logout
closes the socket/timers and clears the identity's cache. There is no REST polling
fallback on any live stage route.

Only processed source fields from design §6.1 are displayed. Success is
`completed / (completed + failed)` and SLO-in is `withinSLO / (completed + failed)`;
`N=0` is 비율 없음, observed `received=0` is 무유입, missing values remain blank.
Node power is from metrics, never node state. Pre-assignment `pending` and native
post-assignment `waiting` stay separate. Completing an action never releases a
reservation; the next reservation projection is authoritative.

The last ten minutes contain only received one-second buckets, capped at 600
buckets and 1000 events. Pre-connection, resync and measurement gaps are not
interpolated or zero-filled. The fixture selector exercises empty → first
configuration → expansion → residual execution during shrink; all are synthetic
검사용 scenes, not a product recording replay or E5 measurements.
Current SLO is shown as an explicit reference, not drawn backward through old
buckets whose messages do not carry definition IDs. Explicit gap hatching is
scope-specific: workload gaps affect request lanes, power/Node gaps affect the
power lane, and resynchronization gaps affect all lanes.

For local browser acceptance, run `pnpm fixture:live` with Node 24. It binds only
loopback, prints its temporary CA path, and serves a TLS fixture hub behind the
real `server/live-proxy.mjs` authentication boundary on `https://localhost:3445`.
In a second terminal use the production standalone runtime on port 3131, with
the normal CSP (the fixture front deliberately does not expose development HMR):

```sh
export CHILL_BASE_URL=https://127.0.0.1:3446 CHILL_NAMESPACE=fixture CHILL_SYSTEM_NAME=fixture
export CHILL_DEMO_REPLAY=1 NODE_EXTRA_CA_CERTS='<printed CA path>'
pnpm build
cp -R .next/static .next/standalone/.next/
HOSTNAME=127.0.0.1 PORT=3131 node .next/standalone/server.js
```

Use the local synthetic token `s2-local-token`; it has no cluster authority.
Local-only POST controls `/__fixture/scene?name=empty|first|expand|shrink`,
`/__fixture/resync` and `/__fixture/cluster` exercise push updates, zero-retention
resynchronization, and an E5-7-shaped issue event. `/__fixture/status` reports
authentication/resume counts and active upstream sockets without credentials.
No cluster or deployment is involved. Exit the fixture process to remove its
temporary certificate/key. Run `pnpm test`, `pnpm typecheck` and `pnpm lint` for
the repeatable reducer, boundary, transport lifecycle and display regressions.

Local S2 acceptance rehearsal (2026-10-30): Node 24, production standalone
Next server, normal production CSP, loopback TLS hub and the real authenticated
WebSocket front; no browser fetch interception, cluster access or deployment.
The full Node suite passed 49/49 before the additional Upgrade-reset regression;
the updated proxy suite passed 3/3. Typecheck, lint and production build passed.
The build retained the existing Asta Sans fallback-metrics warning.
The rejected-Upgrade reset found during this rehearsal now destroys the socket
instead of terminating the proxy, and has a runnable proxy regression test.

| Criterion | Exercised evidence |
| --- | --- |
| S2-2 | Different received/terminal denominators, observed zero/비율 없음, null power and absent buckets; display/reducer tests and POOL browser card |
| S2-3 | All four labelled synthetic scenes followed from L0; `fixture-{empty,first,expand,shrink}.png` |
| S2-4 | Processed projection only; blank whole/Node power, independent pending/waiting, shared instance symbols; no raw or polling REST reads in the browser trace |
| S2-5 | Keyboard instance expansion, Node/reservation source facts, query refresh after reauthentication, pinned evidence through a newer decision and refresh, opener/heading/board-lane focus restoration |
| S2-6 | First-frame auth reached the TLS upstream; a zero-retention reconnect resumed from its exact old checkpoint then received a snapshot and explicit gap; L0/L1 navigation kept the socket; logout removed live payload/DOM and left zero upstream sockets |
| S2-7 | An event-only E5-7-shaped observer issue appeared immediately with source/reason/previousReason; this is a synthetic local rehearsal, not actual cluster E5-7 acceptance |
| S2-8 | 390×844 viewport had scroll width 390; evidence stacked below the board, native Enter/Esc worked, reduced-motion matched with animation `none`, and UTC/KST switched explicitly |

Screenshots from this rehearsal are in `/tmp/chill-dashboard-s2/`, including
`live-authenticated.png`, `pool-reservations.png`, `decision-evidence.png`,
`cluster-live-event.png`, `resync-gap.png`, `narrow-reduced-motion.png`,
`narrow-live-board.png`, `evidence-refresh-retained.png` and `logout-cleared.png`.

History, load and custom observation dates use the device's local time zone.
Only edited fields are converted to UTC; untouched submitted RFC3339Nano strings
remain exact, including seconds and fractional precision. Empty, partial or invalid
date edits stay unsubmitted and show the existing range validation on apply without
replacing the submitted interval or its records.


Workloads are displayed as 서비스, POOLs as 실행 그룹. Existing native model/service records are picked from lists; dependencies, impact confirmation, filters, advanced input and exact source identities open in accessible dialogs. First-time artifact registration still requires its real source because no artifact catalog producer exists. Login is a dedicated screen; connected identity changes and logout are in 접속 관리.

The POOL list accepts the processed API's null collections as empty lists. POOL detail now uses the live projection, not the former manual REST polling view.

Model authoring and applied workload definitions use `firstfloor` settings with positive int32 `maxBatchSize` and per-workload-UID `maxQueueSize` bounds. Native backends are `ort-cpu` / `ort-cuda` for ONNX and `rknnlite` for RKNN. The API validates artifact/backend compatibility, requires these settings for the `firstfloor` family and rejects them for other families. Each artifact lists every model file in `source.files`; only RKNN artifacts declare an `input` tensor contract, and the Preprocessor targeting an artifact defines the raw input of every other format.

Resource policy/maintenance writers and measurement writers/options remain unavailable. Scheduler judgment, candidate/load, settings, transitions, history, whole/active POOL power, A/B comparison and immutable experiment publications are connected to existing producers. FULL labels enabled plus all eight controls, not successful physical application or SLO satisfaction; the compact control section links measured candidate evidence, current observations, actual transitions and the same input-window request/power results. Experiment reads do not execute experiments or judge their success. Missing Console configuration in ChillServer is an explicit `503`, not a successful empty list. Measurement power is not operating POOL energy.

Browser token exists only in tab memory. Authentication changes abort requests, cancel and clear queries, replace the entire QueryClient, and remount draft forms. Query retry, focus and reconnect refetch are disabled; optional visible polling has a configured bound. Mutations/inference are never replayed. Shared changes and deletion require explicit confirmation; conflict retains drafts. Explicit latest-version adoption updates both the comparison baseline and version without replacing the draft, so before/after confirmation uses the actual latest definition. Response loss is `결과 미확인` and requires original-state lookup before an explicit user resubmission. The public native HTTPS ChillServer gateway serves this private web module on the same origin; the BFF still calls its fixed HTTPS ChillServer origin and validates the native CA.
