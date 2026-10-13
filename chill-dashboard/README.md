# CHILL Dashboard

CHILL-internal Next App Router module, built independently and deployed within the `chill` Helm release. The native owner remains authoritative for identity, authorization, declarations, operations, statistics and evidence. This is the approved current core, not completion of every Dashboard SRS producer.

## Fixed upstream source and license

Actual source fork: https://github.com/shadcn-ui/ui.git at commit `e8c3143b1cd191280befcd6c9538284bb43399a8`.

- Application baseline: `templates/next-app` (package, TypeScript/ESLint/PostCSS/Next configuration, layout, theme provider).
- Selected block: `apps/v4/registry/new-york-v4/blocks/dashboard-01` (`app-sidebar`, `site-header`, `section-cards`, `data-table`, `chart-area-interactive`, and page shell composition).
- Retained copied dependency closure: `apps/v4/registry/new-york-v4/ui/{sidebar,button,input,separator,sheet,tooltip,table,chart,dialog}.tsx`, `hooks/use-mobile.ts`; theme tokens from `apps/v4/app/globals.css`. Unused demo primitives are deleted.
- `LICENSE.shadcn` preserves the upstream MIT copyright and permission notice for copied/adapted source. It does not relicense other CHILL code.

The demo records, accounts/avatars, revenue/time series, marketing links, drag-and-drop, sidebar cookies and remote Google fonts are removed. Table uses the copied TanStack Table v9 feature/FlexRender convention; chart adapts the selected ChartContainer/tooltip source with server whole-window statistics only. No fake backend or demo data producer is included.

## Runtime

Node 24.21.0; pnpm 10.33.4. Dependencies are frozen in `pnpm-lock.yaml`. `pnpm install --frozen-lockfile`, then `pnpm lint`, `pnpm typecheck`, `pnpm test`, `pnpm build`. `pnpm start` binds `0.0.0.0:3000`. Next uses `output: standalone`; a standalone image must include `.next/standalone`, `.next/static` and `public` if present and set `HOSTNAME=0.0.0.0`, `PORT=3000`.

Server runtime environment (not browser build-time variables):

| Name | Contract |
| --- | --- |
| `CHILL_BASE_URL` | Required single HTTPS owner origin; no path, credentials, query or fragment |
| `CHILL_NAMESPACE` | Required real namespace |
| `CHILL_SYSTEM_NAME` | Required real System name |
| `CHILL_QUERY_TIMEOUT_MS` | Default `10000` |
| `CHILL_MUTATION_TIMEOUT_MS` | Default `60000` |
| `CHILL_INFER_TIMEOUT_MS` | Default `30000`; must match the owner's native request cap |
| `CHILL_MAX_REQUEST_BYTES` | Default `8388608`; request and response bound, align owner request cap |
| `CHILL_PAGE_SIZE` / `CHILL_MAX_PAGE_SIZE` | Default `50` / `200` |
| `CHILL_MAX_WINDOW_HOURS` | Default `24` |
| `CHILL_POLL_INTERVAL_MS` | Default `5000` |
| `NODE_EXTRA_CA_CERTS` | Native custom CA file, e.g. `/etc/chill/ca/ca.crt`; no trust-any fallback |

`GET /api/config` returns safe display/bound configuration only. `/api/chill/v1/...` relays to `/api/v1/...` at the fixed owner with native Node HTTPS verification, no redirect following, no cookie/identity forwarding, bounded bytes/time and abort propagation. Incomplete responses never appear as successful partial results. Relay failures have distinct Dashboard codes and `unknownOutcome`; `X-Chill-Relay-Timing` covers the full relay including CHILL, not BFF-only overhead or native inference latency. Malformed configuration returns no-store `503 DashboardConfigInvalid` without disclosing environment values.

Inference relay lifetime is `CHILL_INFER_TIMEOUT_MS + CHILL_QUERY_TIMEOUT_MS` (default 40 seconds); the native inference timeout remains capped at 30 seconds by default. This bounded delivery margin allows the owner's native timeout status/body to reach the browser without relabeling it as a Dashboard timeout.

### Scheduler API and caller permissions

The BFF exposes GET `v1/scheduler`, `v1/scheduler/settings`, `v1/scheduler/state`,
PUT `v1/scheduler/settings`, GET `v1/observations/load` and GET
`v1/observations/scheduler`. Both observation routes require explicit RFC3339Nano
`from` and `to`; load has no paging input. Scheduler history accepts bounded
`limit` and nonnegative `cursor` in retained append order, not a total count.
Settings PUT sends the Settings object itself; owner journal and ConfigMap
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

`/`, `/models`, `/models/new`, `/models/[name]`, `/models/[name]/edit`, `/workloads`, `/workloads/new`, `/workloads/[name]`, `/workloads/[name]/edit`, `/workloads/[name]/infer`, `/resources`, `/pools`, `/pools/[id]`, `/measurements`, `/measurements/check`, `/measurements/[kind]/[namespace]/[name]`, `/configuration`, `/transitions`, `/transitions/[id]`, `/observations`, `/experiments`, `/experiments/[id]`, `/changes`.

Real Model/Workload CRUD and version preconditions, one independent named-tensor inference, current POOL state, System/Inventory/ProfilingRun bounded queries, verified Passport, sealed campaign result and read-only preflight are wired. Observation selection preserves UID, original UTC `from`/`to`, repeated definition/outcome filters and cursor; cohort statistics remain the server's whole-window values, separate from raw paging. JSON exports preserve source identity/conditions/units and redact credentials. Changes uses the real per-Workload management journal; absence of a broader feed is disclosed.

Each page has one task: list, detail, create, edit, inference, or one read view. Workloads are displayed as 서비스, POOLs as 실행 그룹. Existing native model/service records are selected by name; dependencies, impact confirmation, advanced input and exact source identities open in accessible dialogs. First-time artifact registration still requires its real source because no artifact catalog producer exists. Login is a dedicated screen; connected identity changes and logout are in 접속 관리.

Resource policy/maintenance writers, measurement writers/options, full operating POOL energy aggregation and experiment producers remain `연결 준비 중` with disabled controls. Scheduler query and settings transport are connected; their UI surfaces are tracked separately. Missing Console owner configuration is an explicit `503`, not a successful empty list. Measurement power is not operating POOL energy.

Browser token exists only in tab memory. Authentication changes abort requests, cancel and clear queries, replace the entire QueryClient, and remount draft forms. Query retry, focus and reconnect refetch are disabled; optional visible polling has a configured bound. Mutations/inference are never replayed. Shared changes and deletion require explicit confirmation; conflict retains drafts. Explicit latest-version adoption updates both the comparison baseline and version without replacing the draft, so before/after confirmation uses the actual latest definition. Response loss is `결과 미확인` and requires original-state lookup before an explicit user resubmission. The public native HTTPS owner gateway serves this private web module on the same origin; the BFF still calls its fixed HTTPS owner origin and validates the native CA.
