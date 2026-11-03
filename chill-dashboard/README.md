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

기존 `app/globals.css` 폰트·테마·의미색을 재사용한 운영 판독표를 사용한다. 한국어 명사형 H1 한 번, 라벨·숫자·상태어가 기본이며 수치는 표·Recharts·CSS로 제공한다. 정의 설명은 툴팁·전역 용어 도움말 한 곳에 두되 판단에 필요한 건수·원인·제약은 숨기지 않는다. 1440×900 첫 화면은 필터 포함 주요 영역 최대 세 개다.

상태 정본은 `lib/status.ts`, 표시 계층은 `components/status-cell.tsx`의 `StatusCell({axis,label,tone,reason?})`다. 기존 판정을 재사용하며 새 상태 엔진을 만들지 않는다. 부재는 미기록·미제공·미확인·판정 불가·해당 없음·지원 안 함·결과 미확인·권한 없음·조회 실패·오래된 관측을 구분한다. 축 이름은 서비스 영향·성능 판정·관측 상태·실험 상태·접수 허용·배치·실제 유입·현재 사용·품질 판정·실행·측정·정리다.

표의 단위는 헤더, 숫자는 오른쪽 정렬·tabular-nums, 이름은 자기 열 안 줄바꿈이다. DetailSheet는 새 래퍼가 아니라 기존 `components/ui/sheet.tsx` 재사용 규약이다: 하나만 열기·중첩 금지·핵심 표 상단·원본 하단·비동기 영역은 핵심값 뒤. Esc·키보드 선택·focus 복원을 유지한다. 좁은 폭에서도 라벨/값 재배치로 모든 필드·작업에 접근할 수 있어야 한다.

로컬 상세 표는 App Router 없이 검색·정렬·페이지를 유지한다. `urlState`를 명시한 목록만 URL 상태를 읽고 갱신한다. 편집형 측정 필터는 실행 경로도 입력·후보 목록을 사용하며, 실행 경로 별칭에는 원 식별자를 함께 표시한다. 품질 기준·표본의 부재는 제공 계약의 필드별 미제공 표로 표시하고 기준값을 만들지 않는다.

Scheduler history keeps default/ascending unfiltered reads bounded, including explicit `filter=all`, and returns original append offsets. Current-page counts appear only in the summary table. Live incident order preserves nanoseconds and uses descending sequence for equal timestamps; shared power remains explicitly labelled cluster-wide in service details.

### 표시용 읽기 projection

- 서비스 목록은 `GET /api/v1/workloads?view=summary`를 사용한다. `supportedInputs`는 path·artifact·profiles `{id}`만 전송하며 프로파일 수는 `profiles.length`로 계산한다. 원 조건·근거는 무쿼리 목록/단건 조회에 남는다.
- 커버리지, 측정 요청 목록, 처리 성능 페이지는 각각 `measurement-coverage?view=summary`, `batch-energy-optimizations?view=summary`, `scheduler/profiles?view=summary&limit=50&offset=…`를 사용한다. 필터·정렬·페이지는 기존 순서로 적용하며 선택 프로파일은 기존 `profile=<id>&limit=1`, 요청 결과는 기존 `/{name}/result` 단건으로 읽는다. 쓰기 URL과 무쿼리 응답은 바뀌지 않는다.
- 측정 요약의 `readProjection`은 `{version:"measurement-summary.v1",values,shapes}`이다. `~N`은 `values[N]`, `{"$o":[S,...values]}`는 `shapes[S]` 필드 순서의 객체다. dictionary 항목은 앞선 항목만 참조하고 최상위 dictionary 문자열은 원문이다(`~123` 같은 원문도 손실 없이 보존). `lib/measurement-summary.ts`가 정밀 정수 파싱 후 한 번 복원한다. 조건·출처·체크·부모/배치 판정을 공유하며 체크 결과·미확인 사유·null·0·페이지 정보는 그대로다.
- 디코더는 dictionary 항목별 논리적 확장 노드 수·깊이를 메모하고 각 항목과 최종 응답이 **2,000,000노드 / 깊이 64**를 넘으면 반환 전에 거부한다(컨테이너·원시값 각각 1노드, 루트 깊이 1, 반복 참조는 매번 합산). 저장된 운영 GET 8개를 같은 Go projection에 통과시켜 측정한 최대치는 커버리지 **230,362 / 11**, 측정 요청 **180,000 / 9**, 프로파일 **149,331 / 11**로, 노드 약 8.7배·깊이 약 5.8배 여유다. `CHILL_MEASUREMENT_SUMMARY_DIR`에 Go가 생성한 summary JSON들을 두면 `lib/measurement-summary.test.ts`가 실제 응답 통과 회귀를 실행한다.
- 측정 summary 디코딩 실패는 공통 `decodeResponse` 경계에서 `ApiError(0, 원문 메시지, {code:"MeasurementSummaryInvalid",message})`로 변환한다. 커버리지·측정 요청·프로파일(요청 작성의 페이지 조회 포함)은 같은 오류 계약을 사용하며 화면 첫 오류 문장에 거부 원인을 표시한다. 요청 작성의 프로파일 페이지 순회가 실패하면 빈 이력으로 양식을 열지 않고 조회 오류를 표시한다. 커버리지 상세의 측정 시각 조회 실패도 디코딩 거부 원인을 표시하며 이미 받은 수치 근거는 유지한다.
- 요약만 제외하는 미사용 필드: 커버리지 `entities/groups` 역색인(집계·셀별 관계는 유지), 측정 요청 `metadata.managedFields/annotations`, 프로파일 `preparations`. 원본 조회·저장 계약·판정 계산은 변경하지 않는다. relay는 해당 GET 목록에만 정확히 `view=summary`를 허용한다.
- 이미지 버전은 프로파일 유효성 조건이 아니며 원래 기록된 이미지는 보존한다. 기존 `replaces` annotation의 대상 제외를 먼저 적용한 뒤, 셀(model/class/path/powerMode)마다 `measuredAt`이 가장 최신인 측정 하나를 현재로 선택한다. 최신 측정이 실패·partial이어도 그대로 표시하며 옛 성공으로 fallback하지 않는다. 다른 측정은 모두 history와 단건 원본에 보존한다. usable/problem·checklist 판정은 사실·경고 표시용이며 프로파일 선택이나 측정 요청 진행을 차단하지 않는다. 브라우저는 서버의 현재 조건·판정을 다시 판단하지 않는다.
- 셀의 `currentMeasurement`는 서버가 선택한 `{id,model,deviceClass,executionPath,powerMode,measuredAt}` 또는 `null`이다. 상세는 이 객체의 현재 측정 ID·시각을 그대로 표시하고, variant의 `profileIds`나 프로파일 조회 결과에서 현재 선택을 추정하지 않는다.
- 측정 요청 목록은 실행 실패 사유가 있어도 `CatalogUnavailable`을 별도의 `카탈로그 상태: 카탈로그 조회 불가`로 표시한다. 카탈로그 조회 실패를 요청 실행 실패로 바꾸거나 숨기지 않는다.
- 2026-11-02 운영 GET을 로컬 production projection에 적용한 UTF-8 본문 크기: workloads **17,227,584 → 422,048 B**(29개), 커버리지 **9,218,406 → 1,893,956 B**, 측정 요청 **11,941,797 → 1,677,154 B**, 프로파일 `limit=50&offset=100&sort=model&order=asc` **8,550,682 → 921,387 B**. 배포 후 수치가 아니라 같은 운영 원문에 대한 로컬 projection 결과다.

### d · 분배 점유시간

측정은 서비스와 함께 사이드바의 **운영 영역**에 속한다. `/measurements/requests/new?kind=dispatch-occupancy`에서 셀에 대응하는 BEO·워크로드·Node·배치만 선택한다. `POST /api/v1/namespaces/{ns}/dispatch-occupancy-measurements?dryRun=All` 응답의 완성된 `task.spec`를 읽기 전용 표로 보여 주며, 프로토콜 필드를 요청 본문에 넣지 않는다. 검사 입력 identity가 바뀌면 제출을 막는다. `NOT_SUBMITTED`는 미제출, `OccupancyInfeasible`은 측정 조건 불충족, `OccupancyPolicyUnavailable`은 측정 정책 사용 불가다. 응답 미수신은 자동 재제출하지 않고 동일 이름의 접수 기록을 조회한다.

읽기 전용 프로토콜 표는 동결된 `task.spec.conditions.inputsJSON` 문자열을 `parseSchedulerJSON`으로 정밀 파싱한 `computed.saturationSizing`을 표시한다. 선택/필요 송신자, 송신자·바이트·수명 한도와 제한 요인, 가득 찬 대기 배치, 재충전·잔여 유휴 시간, 예상/최대 유휴 비율을 그대로 읽으며 큰 정수도 손실 없이 유지한다. 누락·null·읽을 수 없는 입력은 `미제공`이고 현재 정책으로 재산정하지 않는다. 출처 메타데이터는 기존 정책·입력 근거 원본 disclosure에 남고 입력·제출 계약은 바뀌지 않는다.

커버리지 셀의 `occupancy[]`는 Node UID·profileID·배치별 없음/측정 중/실패/있음을 표시하고 없음·실패에서 해당 조건의 d 요청으로 이동한다. `adopted:null`은 채택 미확인으로 유지한다. `/measurements/results?kind=dispatch-occupancy`의 목록·Sheet는 phase·사유·evidence 측정 구간·경과와 signed d(ms/요청)·처리량·유휴 비율·판정을 표시하며 대기/측정 중에 주기 갱신한다. 삭제는 영구 삭제 확인 후 UID 쿼리와 `If-Match: resourceVersion`을 전달한다. Measuring은 비활성이고 409는 최신 상태 조회로 연결한다. 실행 중단/취소 API는 없다.

`occupancy[]`는 실제 DOM tuple만 포함한다. 빈 배열은 d 없음이며 Node×배치 항목을 브라우저에서 만들지 않는다. `ProfileSuperseded`는 원자료가 있는 이전 프로파일, `EvidenceInvalid`는 Complete 근거 검증 실패다. 같은 tuple은 서버의 present > measuring > failed > none 및 동률 생성 시각 최신 선택을 그대로 표시한다.

채택 tri-state는 서버의 실제 선택 카탈로그와 연결된다. Node UID·profileID·배치·evidence store/key와 현재 셀의 모델·전력모드·실행 경로 identity가 일치하면 `true`(채택), 조회 가능한 카탈로그의 빈 선택은 `false`(미채택), 필드 누락·카탈로그 오류는 `null`(채택 미확인)이다. runtimeImage는 유효성 키가 아니며 카탈로그 checkedAt/error는 기존 freshness에 반영한다.

셀별 d 요청은 부분 커버리지에서도 항상 제공하며 호환성 대표 Node를 넘기지 않는다. 요청 화면은 BEO의 실제 `targetNode`로 Node를 제한한다. 선택적 요청 이름도 검사 identity에 포함되므로 같은 조건의 재측정은 새 이름으로 검사해 과거 실패를 보존한다. profile 의존 조회가 실패해도 종류 선택은 가능하지만 진행 중·결과 미확인 요청에서는 전환을 막는다.

DOM `phase`·`d`는 필수 nullable projection이며 구 `status`를 복원하는 fallback은 없다. 삭제된 단건의 404, 권한 오류 401/403, 종료 상태에서는 해당 polling을 중단한다. 서버 DOM API 변경(`392388ff`) 위로 rebase되어 실제 POST·DELETE 계약과 통합된 상태이며, 운영 서버·Query 미배포 상태 및 운영 생성·삭제 E2E 미수행과는 구별한다.

`dispatchOccupancy` 정책 설정에는 `supplyMode`가 없다. 새 dry-run은 native `spec.supplyMode`가 실제 `closed-loop`일 때만 “공급 방식 (서버 고정)” 사실로 표시하며 응답에 없으면 `미제공`이다. 결과 조건 표의 일반 공급 방식 표시와 helper는 제거했고 원본 응답은 그대로 보존한다. 접수/삭제의 API 결과 `Accepted`·`DeletionRequested`는 유지되며 live action의 `phase:"completed"`는 해당 mutation 완료로 표시한다. DOM 측정 자체의 `Complete`와 혼동하지 않는다.

`392388ff` 재통합 게이트: focused `go test ./internal/queryapi/... ./cmd/chill-query/...`, `make lint`, Node 24.14.0 tsc/eslint/운영 summary 포함 테스트 **502/502**, 기본 Next build 통과. closed-loop 미응답·다른 값에서 고정 사실을 만들어 내지 않는 회귀와 DOM 접수/삭제 action `completed`→완료 표시 회귀를 포함한다.

2026-11-03 서버 통합 검증: Node 24.14.0 offline frozen 의존성(479개 재사용·다운로드 0)을 유지하며 typecheck·eslint·운영 summary 포함 테스트 500개·기본 build가 통과했다. ea6899ef 위 rebase 후 Go focused Query/Server/HTTP/Kube 테스트, make lint·arch-check·test도 통과했다. 로컬 TLS 서버 테스트는 실제 handler·builder·service의 dry-run 200 NOT_SUBMITTED→생성 202 Accepted와 동결 spec 동일성을 검사했다(인메모리 Kubernetes fixture이며 실제 Job E2E 아님). 로컬 production + 운영 GET의 요청·결과·커버리지 렌더는 1440×900으로 확인했다. 운영 Query에는 최상위 phase/d가 아직 없어 대기·미제공으로 보이므로 Query와 대시보드는 동시 cutover한다. 운영 POST·DELETE는 실행하지 않았다. 로컬 클러스터는 DOM CRD·BEO가 없어 실제 Job 진행 E2E를 수행하지 않았으며 배포 후 수용 범위는 [측정 API §13.8](../docs/design/serving/measurement-api.md#138-네이티브-d-요청진행셀-커버리지-2026-11-03)에 남긴다.

### CPU 공유 실행·전력 표본 계약

측정 요청·프로파일 조건은 CPU 예약량·상한·코어 고정을 받거나 재생성하지 않는다. 요청 작성·복사·조건 그룹과 필터에서도 이 축을 제거한다. 스케줄러 설정의 CPU 수 상한과 할당의 CPU·스레드 표시는 없으며 GPU·메모리 계약만 사용한다. 평가 정의의 부하 발생기 자원 입력은 요청·한도 메모리만 제공한다. 과거 게시·관측 fixture 원문은 변경하지 않는다.

전력 수집 정책은 간격·수집 기간·요청 제한 시간으로 표시하며 삭제된 `MaxEvents` 상한은 타입과 화면에서 사용하지 않는다.


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


## 정본 경로와 탐색

아래는 승인된 구조 개편 목표다. 문서 확정과 단계별 구현·배포·수용은 별개이며 [SRS](../docs/spec/dashboard/SRS.md)와 [설계](../docs/design/dashboard/README.md)가 수용 기준을 소유한다.

| 업무 | 정본 경로·책임 |
|---|---|
| 운영 | `/` 현재 서비스 영향·유입·배치·진행 변경, `/operations/devices` 장치·전력·배치 |
| 서비스 | `/workloads`, `/workloads/[name]?uid=…` 정의·접수·자동제어·관리; 현재 관측은 운영 링크 |
| 측정 | `/measurements` 조건·집계/행렬/결과 Sheet, `/measurements/requests` 부모 요청→자식 회차(`?view=runs` 전체 검색), `/measurements/requests/new` 대상→조건→점검·제출, `/measurements/results?kind=throughput\|operation-cost\|dispatch-occupancy` 종류별 결과 |
| 실험 | `/experiments` 실행, `/experiments/results` 비교 결과, `/experiments/load-scenarios` 부하 설정 |
| 기록 | `/records` 판단·변경/서비스 요청/전력, 종류별 실제 범위·페이지·누락 보존 |
| 보조 설정 | `/settings` 운영 설정/모델/입력 처리/데이터셋 |

기록 종류는 `kind=requests|power`와 기본 판단·변경으로 전환한다. 서비스 변경은 `scope=management`, 운영 설정 변경은 `scope=scheduler&filter=settings`, 기존 판단 비교는 `compare=1`이다. 종류 전환은 대상·정확한 기간·limit를 유지하고 API별 cursor·선택은 초기화한다. 같은 종류의 다음 페이지는 제출된 기간·원본 cursor를 유지한다.
전력 조회는 서비스 UID를 API 필터로 보내지 않고 이동 맥락으로만 유지한다. `/changes?scope=scheduler`는 페이지 redirect에서 설정 변경으로 분기한다. 반복 definition/outcome, 인증 변경 전 requests/power 종류, 불완전한 과거 참조의 식별자 오류를 보존한다. 분배 동작(route/unroute/release)의 선택 근거는 선언/실제 비율과 선택된 operation의 이유·행위자·설정 검증 시각을 표시하며 journal 재조회로 대체하지 않는다.

설정의 정본은 `/settings?tab=configuration|models|preprocessors|datasets`다(기본 운영 설정).
운영 설정은 `section` 범위·`edit` 수정 Sheet 선택을 URL에 보존한다. 분배는 서비스·인스턴스·가중치 표와 접힌 JSON에서 편집하며 기존 차단/기본 복귀/순차 적용 의미를 유지한다. 카탈로그 목록은 필드·파일·실행 방식 수와 연결 서비스 수를 우선하고 긴 주소·해시는 원문에 접는다.
구 최상위 목록은 설정 탭으로 이동하며 등록·상세·수정 전용 경로와 변경 충돌/결과 미확인 안전 계약은 유지한다.

별도 개요·링크 허브·측정 6탭은 폐지한다. 요청 실패와 기존 사용 결과, 현재 사용과 품질, 실행 종료와 측정 통과·정리는 독립이다.
작성·수정은 전용 페이지이며 필수값·출처·변경 요약·점검을 보이고 입력 변경은 점검을 무효화한다. 목록은 breadcrumb 없이, 상세는 상위 맥락 하나다.
실험·부하 작성의 증거 입력은 `GET /api/v1/evaluations/inputs`에 `kind`·`set` 서버 필터와 명시적 `limit=25`·`offset`을 보내고 `page.nextOffset`으로 이동한다. 입력 묶음은 현재 페이지 후보 선택 또는 직접 입력하며 필터 변경은 첫 페이지로 돌아간다. 옵션은 서버 `name`(없으면 `key`)과 묶음 그룹을 표시하고, 페이지 밖의 선택도 `store/key` 기준으로 보존한다. relay는 `kind`·`set`·`offset`·`limit` 단일값과 기존 숫자·길이 제한만 검사하며 값의 정본은 서버다. 무쿼리 전체 반환 계약은 유지한다. 측정 결과 저장 영역은 HTML `v` 플래그 호환 DNS-1123 label 패턴과 최대 길이로 검증한다.
연관 이동은 KPI를 복제하지 않고 대상·기간·필터·선택·namespace·UID를 보존한다. 외부 return URL은 허용하지 않는다.

redirect: `/scheduler`, `/operations/flow` → `/`; `/resources`, `/pools` → `/operations/devices`; `/scheduler/profiles` → `/measurements/results` 처리 성능; `/evaluations` → `/experiments`; `/evaluations/load-scenarios` → `/experiments/load-scenarios`.
기존 실험 `a/as/b/bs` 선택은 새 결과의 같은 게시·구간으로, 서비스 세부 경로는 통합 상세 해당 영역으로 명시 매핑한다. 구 화면 호환 UI는 남기지 않는다.
후보·부하·추정·용량은 선택 근거, 장비·POOL은 장치/배치, 회차는 요청 아래로 통합한다. 원본·검색·CRUD·도메인 계산은 보존한다.
이 절 이후 옛 화면 경로는 기존 연결·검증 기록이며 새 탐색 책임은 위 표가 대체한다. API 경로는 화면 redirect와 무관하다.

Bearer tokens use versioned, system-scoped sessionStorage in the current tab; storage denial falls back to memory. Refresh restores the session after configuration hydration. “이 탭에서 토큰 지우기”, identity changes and HTTP 401 / WS 4401 clear that tab's token, queries, requests and live connection. HTTP 403 / WS 4403 and service failures retain the destination URL and do not expire the token. Duplicated/restored tabs may inherit storage; clearing a tab does not revoke the bearer or other tabs.

Coverage consumes the exact namespaced aggregate GET with only `batches` and `node` upstream; other filters stay in the URL/browser. Entry and explicit refresh fetch snapshots without periodic polling. All reads share bounded `503 CoverageWarming` retries (up to 180 retries; numeric Retry-After clamped to 1–5 seconds, default 2 seconds) and the `초기화 중` status; other 503 responses are not retried. The exact pre-handler warm-up rejection also permits mutation retries; transport-uncertain mutations are never retried. Server assessments and exact variant result rows are preserved without browser rejudgment or all-Run crawling. The selected result follows the common DetailSheet contract.
Mutation retries remain bound to the mounted hook and the original token/session epoch. Unmount, logout or identity replacement cancels the complete retry lifetime, including Retry-After waits; the retry predicate and the next HTTP attempt both check that lifetime.
범위의 측정 있음·문제 집계는 셀과 같은 정의를 쓰고 지원 안 함 제외 수를 별도로 표시한다. 현재 사용(스케줄러 참조)과 품질 판정은 독립이다. 목표 부재는 상단 한 번, 활성 필터·전체 해제는 접힘 밖에 표시한다. 셀 선택은 뒤로가기로 닫히며, `urlState`를 명시한 주 목록의 `page/limit/sort/order/search`만 새로고침에도 유지된다. 상세·편집 표는 독립 로컬 상태다. 390px에서는 집계·목록을 라벨/값 행으로 재배치한다. 같은 ID/프로파일·배치 결과는 중복하지 않으며 다른 보관 결과는 별도 객체다. 회차 상세는 고정 제목과 성능 표를 우선하고 202 근거 대기에서 재조회할 수 있다. 실제 첫 배치를 대표값으로 명시하며 전체 배치는 접힌 표로 보존한다. 변경 비용은 실제 중앙–최대 범위와 수치 표, 실패 분배는 미검증 원자료로 제공한다.
복사 출처 namespace·UID를 관측값으로 고정하고 동명 교체를 차단한다. 측정/기록 창은 실제 시간값만 시각화하고 예정 회차·세부 단계 계약 부재는 미제공이다. 저장 계획 검사는 새 요청 제출과 구별한다. 카탈로그 상세의 내부 식별자는 원문에, 형식·실행 한도·연결 서비스의 장치/접수는 독립 행에 표시한다.
새 요청의 접수(dry-run) 검사와 실행 계획(preflight) 검사는 각각의 HTTP 수신 상태·결과를 표시한다. HTTP 0은 만들지 않으며 응답 미수신은 해당 검사에만 귀속한다. 제출은 두 검사가 같은 입력에서 통과해야 가능하다. 복사한 측정 방식은 전력 수집 설정뿐 아니라 활성/유휴 `durationToleranceSeconds`와 `pointPolicy`도 보존한다. 자동 상속 방식의 재선택은 실제 대상 변경에만 적용한다. 명시 복사한 프로토콜과 직접 수정한 방식은 유지하며, 이름·배치·반복 편집으로 최신 방식에 교체하지 않는다. 다른 장치의 방식은 대상 조건이 아니라 `측정 방식 출처`로 표시한다. 실패 목록의 같은 정규화 사유는 한 행에서 한 번만 표시하며 원문은 보존한다.

Real Model/Workload CRUD and version preconditions, one independent raw-sample inference (JPEG fields from a file, UTF8 fields from text, sent as base64 `BYTES` and answered with output fields decoded by format), current POOL state, System/Inventory/ProfilingRun bounded queries, verified Passport with per-batch dataset record IDs, units and stage times, sealed campaign result and read-only preflight are wired. Observation selection preserves UID, original UTC `from`/`to`, repeated definition/outcome filters and cursor; cohort statistics remain the server's whole-window values, separate from raw paging. JSON exports preserve source identity/conditions/units and redact credentials. Changes uses the real per-Workload management observations; absence of a broader feed is disclosed.

The Model editor preserves firstfloor queue/batch limits and selects
`deadlinePolicy=drop|keep|defer` (omitted means `keep`). Changing this frozen
setting replaces the native instance; it is not an in-place scheduler policy
update. Deferred requests remain waiting, not running or completed. The actual
local editor was exercised with all three values and existing limits preserved;
the browser used an isolated ChillServer-contract fixture and performed no live writes.

`/`는 운영 판독·서비스 상태·진행/사건의 세 영역이다. 서비스 영향과 관측 공백을
독립 상태 셀과 aria-live 요약으로 함께 제공한다. 전부 유휴이면 집계 표로 압축하고
보기 전환으로 개별 서비스에 접근한다. 서비스 선택은 공통 Sheet 한 개를 사용한다.
후보·추정·부하·용량 근거는 `/?open=service:UID&evidence=candidates|estimates|load|capacity`로
연결하며, `view=snapshot` 직접 진입도 첫 수신 응답을 고정하고 최신 판단을 명시적으로 채택한다.
`/operations/devices`는 전력 추세와 이름순 장치·배치 표 두 영역이다. 계측 범위와 구성 범위를
구분하고 사양·전력모드·측정/가용/제외 역할·준비/제외 사유·실제 할당은 선택 Sheet에서 조회한다.
구 POOL 상세 링크는 `workload` 범위와 `open` 선택을 분리하며 장치 선택은 `node`로 장치 화면에 매핑한다.
`Feasible`은 `실행 가능`이며 SLO 충족 판정이 아니다. 후보의 서비스 평가 항목이 없으면
`평가 대상 없음`을 표시하고 동일 후보 ID의 유지안·목표는 한 번만 표시한다.
`Preparing`·자동제어 꺼짐·현재 인스턴스 없음이 함께 관측되면 홈·서비스 관측·목록은
`미배치 · 자동제어 꺼짐`을 공유한다. 고정(`fixed`) 장치의 `PowerModeUnavailable`은
장치 확인에서 제외하며, 제외 노드와 `PlacementInventoryMissing`은 사유를 유지한다.
진행·사건은 최근 5분 실제 수신 판단 수·유지 수·마지막 시각과 관측된 파드 준비 전이를 표시한다.
운영 재진입 시 `observations/events?from&to&limit=100`으로 최근 15분을 최대 5페이지·500건까지 읽고 실제 사건 identity로 실시간 사건과 합친다. archive에 WebSocket 순번을 생성하지 않는다. 미조회 페이지는 일부 표시, coverage·snapshot.history의 보존 공백은 `보존 공백` 상태어로 유지하며 손실 건수를 추정하지 않는다.
전력 추세는 `observations/power?from&to&bucket=5s`의 최근 15분·실제 5초 구간 평균을 실시간 1초 관측에 연결한다. null은 선을 끊고 실제 0은 0으로 유지하며 5초 평균을 1초 표본으로 복제하지 않는다.
기록 전력 탭은 bucket 시간축과 기간 에너지·평균 W/노드별 요약을 분리한다. 시계열 범위는 최대 1시간·900구간이며 더 긴 기간의 요약과 원자료는 유지한다.
현재 페이지의 원자료 표·페이지 이동은 접는다. 페이지 J를 전체 구간 에너지로 합산하지 않는다.
Automatic application and evaluated judgment are separate; keep/target J and SLO are
predictions, not measurements. Load preserves the submitted window in the page URL
independently of the draft. ChillServer omits arrivals from load responses, so load
links query all definitions for the original UID/window; candidate SLO links
include the provided immutable definition. Request details separately display
CHILL dispatch and native progress, including exact uint64 batch identity.
Service labels share a UID resolver across observations, load and routing.
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

`/experiments/results` reads immutable published runs/stages from `GET /api/v1/experiments`
and `/{id}`. The A/B selectors retain two runs and two stages in the URL. Only
publisher `kpis.workloads` (demo) and `kpis.requests` (evaluation) shapes are interpreted;
original KPIs, numerator/denominator, populations, sources and missing reasons
remain available. Empty values stay blank, including identities absent from old
publications; names are never rejoined to current Workloads. Only matching recorded
UID/definition rows align across runs. Offered counts, server receipt cohorts and
live terminal buckets are explicitly distinct. Shared POOL energy is not summed
across rows; published gap bounds render as `value ± bound J`, with energyCoverage.
Different measurementRules retain numbers and display “측정 규칙 다름”.
R18: 목록은 선택적 `facts`의 이름·정책·SLO·부하 출처를 직접 표시하고 없으면 미기록이다. 목록 항목별 상세 hydration은 하지 않으며 명시적 과거 비교 대상이 목록에 없을 때만 그 publication을 조회한다. 비교 상대가 없으면 차이 대신 측정 원값을 먼저 표시하며, 계획 부하는 구간×서비스 표(소수 둘째 자리)와 하위 구간별 막대로 표시한다. 단계 전체 p95·발행률을 하위 구간의 실측값으로 복제하지 않는다. `SLOFailed`는 `SLO 미충족`이고 수집 실패와 구분한다. 과거 게시에 실제 없는 정의는 추정하지 않는다.
현재 reader는 [DB-FR-66](../docs/spec/dashboard/SRS.md)에 따라 게시 단건의 `definition.spec.comparisons`와 목록의 `comparisons`로 비교 관계를 읽고 단계 ID·서비스 UID·publicationID로 연결한다. suffix·요청률 유사성 fallback은 사용하지 않는다. 정의 없는 과거 게시는 비교 미기록, 실제 대상 부재는 대상 게시 없음, 권한/서버/timeout은 대상 조회 실패로 구분한다. 정책·SLO는 기록된 정의·실행 결과에서만 읽는다. 비교 상태·제한·일치/참고/대응 없음 건수는 차이보다 먼저 표시하며 참고 비교는 동일 조건 성과 집계와 분리한다. 상세는 조건 → 원값 → 사건 → 원본이다.
실행은 `/experiments`, 부하 설정은 `/experiments/load-scenarios`다. 작성·수정은 각각 `/experiments/new`, `/experiments/[name]/edit`, `/experiments/load-scenarios/new`, `/experiments/load-scenarios/[name]/edit` 전용 페이지다. 원본은 `copy` 또는 `publication`, 수정 UID는 `uid` query로 복원한다. 목록 시각은 KST이며 결과 표는 비교 상태·구간 수·실행/측정/정리·차이·제한을 한 행에 제공한다. 구간 Sheet의 사건은 조건·원값 뒤에 배치한다.
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

The request metric reducer keeps received one-second buckets for ten minutes, capped at 600,
and retains at most 1000 live events. Power charts independently merge bounded 15-minute
archive intervals with actual live intervals. No pre-connection, resync or measurement gap
is interpolated or zero-filled. The fixture selector exercises empty → first
configuration → expansion → residual execution during shrink; all are synthetic
검사용 scenes, not a product recording replay or evaluation measurements.
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
resynchronization, and a failure-shaped issue event. `/__fixture/status` reports
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
| S2-7 | An event-only failure-shaped observer issue appeared immediately with source/reason/previousReason; this is a synthetic local rehearsal, not actual cluster failure acceptance |
| S2-8 | 390×844 viewport had scroll width 390; evidence stacked below the board, native Enter/Esc worked, reduced-motion matched with animation `none`, and UTC/KST switched explicitly |

Screenshots from this rehearsal are in `/tmp/chill-dashboard-s2/`, including
`live-authenticated.png`, `pool-reservations.png`, `decision-evidence.png`,
`cluster-live-event.png`, `resync-gap.png`, `narrow-reduced-motion.png`,
`narrow-live-board.png`, `evidence-refresh-retained.png` and `logout-cleared.png`.

History dates use KST (24-hour); load and custom observation dates use the device's local time zone.
Only edited fields are converted to UTC; untouched submitted RFC3339Nano strings
remain exact, including seconds and fractional precision. Empty, partial or invalid
date edits stay unsubmitted and show the existing range validation on apply without
replacing the submitted interval or its records.


Workloads are displayed as 서비스, POOLs as 실행 그룹. Existing native model/service records are picked from lists; dependencies, impact confirmation, filters, advanced input and exact source identities open in accessible dialogs. First-time artifact registration still requires its real source because no artifact catalog producer exists. Login is a dedicated screen; connected identity changes and logout are in 접속 관리.

The POOL list accepts the processed API's null collections as empty lists. POOL detail now uses the live projection, not the former manual REST polling view.

Model authoring and applied workload definitions use `firstfloor` settings with positive int32 `maxBatchSize` and per-workload-UID `maxQueueSize` bounds. Native backends are `ort-cpu` / `ort-cuda` for ONNX and `rknnlite` for RKNN. The API validates artifact/backend compatibility, requires these settings for the `firstfloor` family and rejects them for other families. Each artifact lists every model file in `source.files`; only RKNN artifacts declare an `input` tensor contract, and the Preprocessor targeting an artifact defines the raw input of every other format.

Resource policy/maintenance writers and measurement writers/options remain unavailable. Scheduler judgment, candidate/load, settings, transitions, history, whole/active POOL power, A/B comparison and immutable experiment publications are connected to existing producers. FULL labels enabled plus all eight controls, not successful physical application or SLO satisfaction; the compact control section links measured candidate evidence, current observations, actual transitions and the same input-window request/power results. Experiment reads do not execute experiments or judge their success. Missing Query API configuration in ChillServer is an explicit `503`, not a successful empty list. Measurement power is not operating POOL energy.

Browser tokens follow the tab-memory/sessionStorage contract above. Authentication changes abort requests, cancel and clear queries, replace the entire QueryClient, and remount draft forms. Query retry, focus and reconnect refetch are disabled; optional visible polling has a configured bound. Mutations/inference are never replayed. Shared changes and deletion require explicit confirmation; conflict retains drafts. Explicit latest-version adoption updates both the comparison baseline and version without replacing the draft. Response loss is `결과 미확인` and requires original-state lookup before explicit resubmission. The public native HTTPS ChillServer gateway serves this private web module on the same origin; the BFF calls its fixed HTTPS ChillServer origin and validates the native CA.
