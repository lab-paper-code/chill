# CHILL Dashboard

CHILL-internal Next App Router module, built independently and deployed within the `chill` Helm release. ChillServer remains authoritative for identity, authorization, declarations, operations, statistics and evidence. This is the approved current core, not completion of every Dashboard SRS producer.

## Fixed upstream source and license

Actual source fork: https://github.com/shadcn-ui/ui.git at commit `e8c3143b1cd191280befcd6c9538284bb43399a8`.

- Application baseline: `templates/next-app` (package, TypeScript/ESLint/PostCSS/Next configuration, layout, theme provider).
- Selected block: `apps/v4/registry/new-york-v4/blocks/dashboard-01` (`app-sidebar`, `site-header`, `data-table`, `chart-area-interactive`, and page shell composition). The `section-cards` block is replaced by the CHILL overview board.
- Retained copied dependency closure: `apps/v4/registry/new-york-v4/ui/{sidebar,button,input,separator,sheet,tooltip,table,chart,dialog}.tsx`, `hooks/use-mobile.ts`; theme tokens from `apps/v4/app/globals.css`. Unused demo primitives are deleted.
- `LICENSE.shadcn` preserves the upstream MIT copyright and permission notice for copied/adapted source. It does not relicense other CHILL code.

The demo records, accounts/avatars, revenue/time series, marketing links, drag-and-drop and sidebar cookies are removed. Asta Sans (Hangul-capable) is fetched by `next/font` at build time and self-hosted under `/_next/static/media`, so the CSP keeps `font-src 'self'` and the browser makes no font requests to third parties. Table retains TanStack Table v9; stable body cells preserve open details during polling. Charts use ChillServer whole-window statistics or the samples of one selected power record. No fake backend or demo data producer is included.

## Visual system

Tokens live in `app/globals.css`: a cool frost canvas, coolant blue for actions and focus, and heat amber for energy and caution. Status is never color-only: `StatusBadge` pairs each tone with an icon and text. Every screen does one function, built from `components/screen.tsx`: a back link, one plain-Korean conclusion as the headline, one key value or list, `›` rows that open the next screen or a dialog, and at most one primary action pinned at the bottom of a narrow centered column. Multi-field input is a step funnel; the scheduler settings are rows edited one value per dialog and applied after one review. Internal codes, UIDs and nanoseconds appear only in 원본 자료 dialogs; times display in local time. Signed-out visitors see only the sign-in screen. Light, dark and 390 px layouts were reviewed against fixture-backed ChillServer responses; no live ChillServer was written.

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
| `NODE_EXTRA_CA_CERTS` | Native custom CA file, e.g. `/etc/chill/ca/ca.crt`; no trust-any fallback |

`GET /api/config` returns safe display/bound configuration only. `/api/chill/v1/...` relays to `/api/v1/...` at the fixed ChillServer with native Node HTTPS verification, no redirect following, no cookie/identity forwarding, bounded bytes/time and abort propagation. Incomplete responses never appear as successful partial results. Relay failures have distinct Dashboard codes and `unknownOutcome`; `X-Chill-Relay-Timing` covers the full relay including CHILL, not BFF-only overhead or native inference latency. Malformed configuration returns no-store `503 DashboardConfigInvalid` without disclosing environment values.

Inference relay lifetime is `CHILL_INFER_TIMEOUT_MS + CHILL_QUERY_TIMEOUT_MS` (default 40 seconds); the native inference timeout remains capped at 30 seconds by default. This bounded delivery margin allows ChillServer's native timeout status/body to reach the browser without relabeling it as a Dashboard timeout.

### Scheduler API and caller permissions

The BFF exposes GET `v1/scheduler`, `v1/scheduler/settings`, `v1/scheduler/state`,
PUT `v1/scheduler/settings`, GET `v1/observations/load` and GET
`v1/observations/scheduler`. Both observation routes require explicit RFC3339Nano
`from` and `to`; load has no paging input. Scheduler history accepts bounded
`limit` and nonnegative `cursor` in retained append order, not a total count.
The relay reads GET `v1/scheduler`, `/state`, `/profiles` and scheduler history
with ChillServer `view=compact` (the only accepted `view`), and `parseSchedulerJSON`
restores the shared `profile`/`completionMeasurement` `$ref`s into the full
response before any screen reads it; a missing reference is an error.
Settings PUT sends the Settings object itself; ObservationLog and ConfigMap
storage remain authoritative. There is no version precondition or dry-run.

Use an existing administrator-approved account, not the Dashboard ServiceAccount.
`chill-serving-manager` grants the exact scheduler GET paths and observation
reads. The unbound `chill-scheduler-editor` ClusterRole grants only the settings
PUT path. When `servingEntrypoint.scheduler.configMap` is selected, a same-name
unbound Role in the serving namespace grants `update` of that named ConfigMap
only. An administrator explicitly binds the read ClusterRole, editor ClusterRole
and named ConfigMap Role to the chosen account. A settings PUT needs both editor
grants; a path grant alone cannot update the ConfigMap.
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

The menu has six sections: 홈, 서비스, 운영, 측정, 기록, 설정. 측정 lists measurement plans by state, requests new ones through a step funnel that must pass the ChillServer dry-run, and shows one plan with its conclusion, adoption, unadoption and permanent deletion. Home is only the list of things to check now (services not admitting, unready nodes, predicted SLO exceedance, failed or open changes), each row opening the screen that resolves it. A service page states its readiness and offers the next step; its definition, evidence, placement, last-hour operation and changes are separate screens.

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

History, load and custom observation dates use the device's local time zone.
Only edited fields are converted to UTC; untouched submitted RFC3339Nano strings
remain exact, including seconds and fractional precision. Empty, partial or invalid
date edits stay unsubmitted and show the existing range validation on apply without
replacing the submitted interval or its records.


Workloads are displayed as 서비스, POOLs as 실행 그룹. Existing native model/service records are picked from lists; dependencies, impact confirmation, filters, advanced input and exact source identities open in accessible dialogs. First-time artifact registration still requires its real source because no artifact catalog producer exists. Login is a dedicated screen; connected identity changes and logout are in 접속 관리.

POOL list and detail views accept the current API's null collections as empty
node, workload or instance lists. An idle POOL or an executor awaiting policy
acknowledgement must not crash the page; its actual readiness remains visible.

Model authoring and applied workload definitions use `firstfloor` settings with positive int32 `maxBatchSize` and per-workload-UID `maxQueueSize` bounds. Native backends are `ort-cpu` / `ort-cuda` for ONNX and `rknnlite` for RKNN. The API validates artifact/backend compatibility, requires these settings for the `firstfloor` family and rejects them for other families. Each artifact lists every model file in `source.files`; only RKNN artifacts declare an `input` tensor contract, and the Preprocessor targeting an artifact defines the raw input of every other format.

Resource policy/maintenance writers, measurement writers/options and experiment producers remain unavailable. Scheduler judgment, candidate/load, settings, transitions, history, whole/active POOL power and A/B comparison are connected to existing producers. FULL labels enabled plus all eight controls, not successful physical application or SLO satisfaction; the compact control section links measured candidate evidence, current observations, actual transitions and the same input-window request/power results. Experiments link to judgment comparison without claiming an experiment lifecycle. Missing Console configuration in ChillServer is an explicit `503`, not a successful empty list. Measurement power is not operating POOL energy.

Browser token exists only in tab memory. Authentication changes abort requests, cancel and clear queries, replace the entire QueryClient, and remount draft forms. Query retry, focus and reconnect refetch are disabled; optional visible polling has a configured bound. Mutations/inference are never replayed. Shared changes and deletion require explicit confirmation; conflict retains drafts. Explicit latest-version adoption updates both the comparison baseline and version without replacing the draft, so before/after confirmation uses the actual latest definition. Response loss is `결과 미확인` and requires original-state lookup before an explicit user resubmission. The public native HTTPS ChillServer gateway serves this private web module on the same origin; the BFF still calls its fixed HTTPS ChillServer origin and validates the native CA.
