# CHILL Dashboard

CHILL-internal Next App Router module, built independently and deployed in the
`chill` Helm release. ChillServer owns identity, authorization, declarations,
operations, statistics and evidence. The Dashboard reads and writes only through
ChillServer and has no Kubernetes, S3 or node access of its own.

[Dashboard 요구사항](../docs/spec/dashboard/SRS.md),
[구현 설계](../docs/design/dashboard/README.md)와
[리뉴얼 설계](../docs/design/dashboard/renewal.md)(화면 구조, 화면별 내용, 공용 부품)가
이 모듈의 계약을 소유한다.

## Upstream source and license

Source fork: https://github.com/shadcn-ui/ui.git at commit
`e8c3143b1cd191280befcd6c9538284bb43399a8`.
The application derives from `templates/next-app`. `LICENSE.shadcn` preserves the
upstream MIT copyright and permission notice for copied/adapted code.

Components in `components/custom/` are copied from the `@avara-ui` shadcn registry
(`components.json`, `https://avaraui.com/r/{name}.json`) under its MIT license.
`components/ui/` holds the remaining shadcn/ui primitives. Other CHILL code retains
its own licensing status.

TanStack Query and Table supply data fetching and tables, Recharts displays
server-provided statistics. Pretendard Variable (`app/fonts/`) is self-hosted
through `next/font/local`.

## Routes

Routes live in `app/(app)/`. Each `page.tsx` renders one screen from `screens/`.

| URL | Screen |
|---|---|
| `/` | 개요 |
| `/services`, `/services/new` | 서비스 목록, 등록 |
| `/services/[name]`, `/services/[name]/edit` | 서비스 상세 (`?tab=` 현황, 요청, 정의, 테스트), 수정 |
| `/devices`, `/devices/[name]` | 장치 목록과 전력, 장치 상세 |
| `/scheduler` | 판단으로 이동 |
| `/scheduler/decisions`, `/scheduler/actions`, `/scheduler/settings` | 판단 기록, 작업과 예약과 사건, 스케줄러 설정 |
| `/measurements` | 측정 범위로 이동 |
| `/measurements/coverage` | 측정 범위 |
| `/measurements/requests`, `/measurements/requests/new`, `/measurements/requests/[name]` | 측정 요청 목록, 작성, 상세 |
| `/measurements/runs/[name]` | 측정 회차 상세 |
| `/measurements/results` | 측정 결과 (`?kind=` 처리 성능, 전환 비용, 분배 점유시간) |
| `/experiments` | 실험 실행으로 이동 |
| `/experiments/runs`, `/experiments/runs/new`, `/experiments/runs/[name]`, `/experiments/runs/[name]/edit` | 실험 실행 목록, 작성, 상세, 수정 |
| `/experiments/results`, `/experiments/results/[id]` | 게시된 결과 목록, 결과 상세 |
| `/experiments/scenarios`, `/experiments/scenarios/new`, `/experiments/scenarios/[name]`, `/experiments/scenarios/[name]/edit` | 부하 시나리오 목록, 작성, 상세, 수정 |
| `/catalog` | 모델로 이동 |
| `/catalog/[kind]` (`models`, `preprocessors`, `datasets`), `/catalog/[kind]/new`, `/catalog/[kind]/[name]`, `/catalog/[kind]/[name]/edit` | 카탈로그 목록, 등록, 상세, 수정 |
| `/kit` | 공용 부품 갤러리 (개발용) |

Detail and edit URLs accept `?uid=`; mutations are blocked when the URL UID differs
from the fetched UID. API routes: `/api/chill/[...path]` (relay), `/api/config`.

## Code layout

| Path | Content |
|---|---|
| `app/(app)/` | Route files; thin server components |
| `screens/<area>/` | One client component per screen. Areas: `overview`, `services`, `devices`, `scheduler`, `measurements`, `experiments`, `catalog` |
| `components/kit/` | Shared building blocks every screen is assembled from; usage in `components/kit/README.md` |
| `components/custom/` | Avara UI blocks copied from the registry |
| `components/ui/`, `components/shell/` | shadcn/ui primitives; sidebar, top bar and sign-in |
| `lib/` | Data helpers, relay, session token, live stream client, status labels, formatting; pure helpers have `*.test.ts` next to them |
| `hooks/` | Shared React hooks |
| `server/` | Live proxy and the local replay server |

## Runtime

Node 24.21.0; pnpm 10.33.4. The image uses Next `output: standalone` and includes
`.next/standalone`, `.next/static`, `public`, `server/` and the `ws` runtime
dependency. `node server/live-proxy.mjs` (also `pnpm start`) serves
`0.0.0.0:3000` and starts the standalone Next child on loopback `3001`.
`build/docker/dashboard.Dockerfile` assembles the layout; a bare source checkout
lacks the root standalone `server.js`.

Server runtime environment:

| Name | Contract |
|---|---|
| `CHILL_BASE_URL` | Required HTTPS ChillServer origin with no credentials, path, query or fragment |
| `CHILL_NAMESPACE` | Required real namespace |
| `CHILL_SYSTEM_NAME` | Required real system name |
| `CHILL_QUERY_TIMEOUT_MS` | Default `10000` |
| `CHILL_MUTATION_TIMEOUT_MS` | Default `60000` |
| `CHILL_INFER_TIMEOUT_MS` | Default `30000`; align with server request cap |
| `CHILL_MAX_REQUEST_BYTES` | Default `8388608`; request/response bound |
| `CHILL_PAGE_SIZE` / `CHILL_MAX_PAGE_SIZE` | Default `50` / `200` |
| `CHILL_MAX_WINDOW_HOURS` | Default `24` |
| `CHILL_POLL_INTERVAL_MS` | Default `5000`; minimum `1000` |
| `CHILL_PUBLIC_ORIGIN` | Optional HTTPS origin the browser uses; the WebSocket `Origin` must equal it. Without it the request `Host` decides |
| `NODE_EXTRA_CA_CERTS` | Trusted custom CA PEM file |
| `HOSTNAME` / `PORT` | Live proxy bind address and port; default `0.0.0.0` / `3000` |

`GET /api/config` exposes safe display/bound settings.
`/api/chill/v1/...` relays to fixed-origin `/api/v1/...` with TLS verification,
bounded time and bytes, cancellation, no redirects and no cookie forwarding.
Malformed configuration returns no-store `503 DashboardConfigInvalid`.
Relay failure retains a Dashboard error code and `unknownOutcome`.
`X-Chill-Relay-Timing` covers the complete relay.
Inference delivery timeout is infer budget plus query budget.

### Caller permissions and sessions

Use an administrator-approved account. The Dashboard ServiceAccount does not
represent the human caller. Administrators bind the roles the account needs;
scheduler settings writes need both the API path grant and update on the
configured scheduler ConfigMap (the chart ships a `<release>-scheduler-editor`
Role for the ConfigMap).

```sh
kubectl -n <account-namespace> create token <existing-account> --duration=1h
```

The tab keeps a versioned, system-scoped sessionStorage token, falling back to
memory if storage is denied. Refresh restores it after configuration hydration.
Identity changes, explicit clearing, HTTP 401 or WS 4401 clear the tab's token,
queries, drafts and live connection. HTTP 403/WS 4403 retain the session.
Duplicated tabs may inherit storage; clearing one tab does not revoke the bearer.
Tokens are sent in the first WebSocket auth frame.

Uncertain mutations and inference are never automatically replayed.
Explicit pre-handler `503 CoverageWarming` allows bounded retries tied to the
mounted hook and original session. Logout, unmount or identity changes cancel
that lifetime. Other 503s do not receive warming retries.
Shared changes and deletion require confirmation. Conflict preserves drafts.
Response loss requires original-state lookup before explicit resubmission.

## Local development

Node 24. The replay server serves the dashboard against a captured ChillServer and
a recorded live stream without a cluster; see `server/REPLAY.md` for the capture
format and mutation behaviour.

```sh
pnpm install --frozen-lockfile
pnpm replay --capture <dir> --port 3600
```

Open `https://localhost:3600/` and accept the self-signed certificate. The Bearer
token picks the scene: `active` (three services serving, actions and an evaluation
running) or `captured` (the idle cluster as recorded); any other token uses
`--scene`. Mutations change only the replay server's memory. Check each screen in
both scenes, light and dark, at 1440px and 390px.

## Checks

From `chill-dashboard/`: `pnpm lint`, `pnpm typecheck`, `pnpm test`, `pnpm build`.
From the repository root, `make dashboard-check` runs the frozen install and all four.
