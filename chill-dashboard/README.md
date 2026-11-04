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

운영 예약 상세의 장치 목록·장치 수는 `nodes[].uid`의 고유 장치를 표시하며, 원본 정보 펼침은 중복 참조를 포함한 서버 응답 그대로 보존한다. 회귀 원자료 `lib/fixtures/live-r3-reservation.json`은 회차 3의 예약 원문(참조 12개·고유 장치 4대)이다.

서비스 관측의 “판단 근거 · 선택 당시”는 모달을 열 때의 결정 시각으로 한 번 조회하며, 실시간 결정 갱신으로 과거 근거를 다시 받지 않는다. 다른 서비스·판단을 명시적으로 선택하면 그 선택의 근거를 조회한다.

결정 상세의 선택 계획 용량 근거는 저장된 `decision.capacity.workloads`의 `kappaNew`, `kappaRetained`, `rate`, `scaleDown.windowMax`를 req/s로 표시한다. `rate`는 관측 유입이 아닌 계획 수요이며, 서버가 최근·창 평균 중 큰 값에 대기분을 더하고 `MinimumDemand` 하한을 적용해 기록한 유효 수요다. 보호 수요는 같은 결정의 `settings.scaleDownHeadroomFraction`으로 `max(rate, windowMax) × (1 + fraction)`, 여유는 목표 κ와 보호 수요의 차이만 표시하며 입력 부재는 미기록이다. 판정·평가 입력을 새로 만들지 않는다. 인스턴스의 `capacity`는 `internal/serving/application/profiles.go` 및 `kube/observer.go`에서 `MaxQueueSize`로 공급되는 요청 수용 한도(개)이며 κ(req/s)나 최대 배치가 아니다.


실험 게시 디코딩·타입은 `lib/experiments.ts`, 비교·차트 projection은 `lib/experiment-comparison.ts`, 정밀 scheduler 근거 구간·활동은 `lib/scheduler-history.ts`, 실험 선택 URL·History 변경은 `lib/dashboard-routes.ts`가 소유한다. 호출부는 소유 모듈을 직접 import하며 게시 identity·나노초 경계·null/0·표시 정밀도·URL 매개변수 계약은 그대로 유지한다.

게시 에너지는 MEASURED의 `power.wholePoolJoules`만 풀 전체로, PARTIAL의 `power.observedJoules`는 관측 J(일부 노드 coverage 미달)로 표시한다. 평균 전력도 같은 범위를 따르며 차트는 풀 전체·관측을 별도 계열로 구분한다. UNAVAILABLE은 수치 대신 미기록과 게시 사유를 보여준다. 상태 없는 과거 게시만 기존 최상위 wholePoolJoules를 사용한다. 회귀 원자료 `lib/fixtures/experiment-partial-energy.json`은 control-330의 7-7 r2 게시(`72234778-e8b5-44a2-94c4-5125bad5c69d`) 원문이다.

측정 요청의 계획·기본값·manifest 조립은 `lib/measurement-request.ts`, 프로파일 요청의 검사·제출·접수 결과 미확인 재조회 상태는 `hooks/use-measurement-submission.ts`가 소유한다. `features/measurement-request.tsx`는 폼·표시와 실제 조회 자료를 연결한다. d 요청은 단일 dry-run 응답의 서버 지정 이름·동결 본문과 별도 접수 기록 조회 계약을 사용하므로 프로파일 제출 hook으로 합치지 않는다.

커버리지·inventory·모델 호환성·BEO 전체 페이지·커버리지 상세 프로파일/회차 조회는 `hooks/use-measurement-data.ts`, 공통 URL 필터 UI는 `components/measurement-filters.tsx`가 소유한다. 각 화면은 소유 모듈을 직접 import한다. query key·페이지 전진/취소·권한 오류 처리·UID 비교·summary 정밀 디코딩과 필터 URL 갱신 규칙은 기존 계약을 유지한다.

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

새 프로토콜(`minimumPairs > 0`)은 짧은 버스트의 배치·최소 쌍·예열·측정 구간·동시 송신자·요청 수명·드레인·프로파일 ID를 서버 응답 그대로 표시한다. 공급 방식은 native `closed-loop` 사실이며 사용자가 설정하지 않는다. 새 프로토콜에는 유휴·포화 기준과 `saturationSizing` 행이 없다. 구형 dry-run의 동결된 `conditions.inputsJSON` 포화 산정은 기존 표시를 유지하며 정책·입력 원본은 disclosure에서 확인한다.

커버리지 셀의 Node UID·profileID·배치별 `occupancy[]`는 두 근거를 분리한다. **스케줄러 d**는 `schedulerD` provenance의 측정/DOM/기본값 출처·값·쌍·갱신 시각이고, **관측 d(B)**는 `observedD`의 signed 중앙값·IQR·p10/p90·음수 비율·쌍이다. 단위는 원래 ns/request이며 화면에서 ms/요청으로 변환한다. 음수는 그대로 표시하며 채택·품질 판정을 만들지 않는다. 구형 `d`·`adopted`만으로 스케줄러 사용 값을 복원하지 않는다. DOM 외에도 서버가 제공한 스케줄러/관측 tuple을 그대로 표시하고 Node×배치 항목을 브라우저에서 만들지 않는다.

셀 상세의 기간 조회는 `GET /api/v1/observations/occupancy?from=&to=`만 사용하며 기본 구간은 `schedulerD.window`를 나노초 문자열까지 그대로 전달한다. RFC3339 시작·종료 시각을 직접 바꿀 수 있다. relay와 공유하는 순수 `timestampNanos`로 실제 달력·오프셋을 검증하고 순서·상한을 정확한 ns로 비교하므로 양의 1 ns 구간도 허용하며 상한 1 ns 초과는 거부한다. URL에는 입력 원문을 유지한다. 모델·전력모드·실행 경로 및 Node UID·배치·profileID가 정확히 일치하는 행만 표시하며 runtimeImage는 표시 전용이다. 서버가 구간을 거부하면 원래 HTTP 상태·code/message 본문을 표시하고 이전 적용 구간·결과를 유지한다. 400 본문에 양의 정수 `maximumWindowSeconds`가 있으면 이후 선택에 그 상한을 적용하며, 없으면 임의 기본 상한을 만들지 않는다.

`/measurements/results?kind=dispatch-occupancy`는 대기/측정 중에 갱신한다. v1alpha2/`minimumPairs`/estimate 근거가 있는 새 DOM은 짧은 버스트의 signed 통계·쌍·최소 쌍·완료 배치/요청 사실과 실행·근거 오류 사유를 표시하고, 유휴·포화·scalar d·품질 판정으로 되돌리지 않는다. 정상 `Observed`의 사유는 scalar가 `null`이어도 estimate를 기준으로 “해당 없음”이며, `Failed`·`Insufficient` 원인과 완료 후 근거 누락·불일치의 모든 사유·메시지도 상세에서 직접 확인한다. 구형 evidence 표시는 그대로다. 삭제는 영구 삭제 확인 후 UID 쿼리와 `If-Match: resourceVersion`을 전달한다. Measuring은 비활성이고 409는 최신 상태 조회로 연결한다. 실행 중단/취소 API는 없다.

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
| 서비스 | `/workloads`, `/workloads/[name]?uid=…` 정의·접수·CHILL 자동 운영·변경 작업 진행; 자동 운영 수정은 운영 설정 바로가기 |
| 측정 | `/measurements` 조건·집계/행렬/결과 Sheet, `/measurements/requests` 부모 요청→자식 회차(`?view=runs` 전체 검색), `/measurements/requests/new` 대상→조건→점검·제출, `/measurements/results?kind=throughput\|operation-cost\|dispatch-occupancy` 종류별 결과 |
| 실험 | `/experiments` 실행 목록, `/experiments?view=published&offset=0` 게시 목록, `/experiments/runs/[name]?uid=…` 실행 상세, `/experiments/results?a=…` 게시 상세, `/experiments/load-scenarios` 부하 설정 |
| 기록 | `/records` 판단·변경/서비스 요청/전력, 종류별 실제 범위·페이지·누락 보존 |
| 보조 설정 | `/settings` 운영 설정/모델/입력 처리/데이터셋 |

기록 종류는 `kind=requests|power`와 기본 판단·변경으로 전환한다. 서비스 변경은 `scope=management`, 운영 설정 변경은 `scope=scheduler&filter=settings`, 기존 판단 비교는 `compare=1`이다. 종류 전환은 대상·정확한 기간·limit를 유지하고 API별 cursor·선택은 초기화한다. 같은 종류의 다음 페이지는 제출된 기간·원본 cursor를 유지한다.
전력 조회는 서비스 UID를 API 필터로 보내지 않고 이동 맥락으로만 유지한다. `/changes?scope=scheduler`는 페이지 redirect에서 설정 변경으로 분기한다. 반복 definition/outcome, 인증 변경 전 requests/power 종류, 불완전한 과거 참조의 식별자 오류를 보존한다. 분배 동작(route/unroute/release)의 선택 근거는 선언/실제 비율과 선택된 operation의 이유·행위자·설정 검증 시각을 표시하며 journal 재조회로 대체하지 않는다.
실험 구간의 판단·전환은 후보/기준 각각의 실제 `from/to`로 최근 10개(`order=desc`)를 조회한다. 범위는 선택 서비스가 아닌 `같은 기간 사건 · 클러스터 전체`다. `nextCursor`가 있으면 `일부 기록`과 다음 cursor로 이어지는 `더 보기`를 제공하고, 보존 공백은 서버 `gapUntil`로 표시한다. 현재 페이지를 구간 전체나 사건 총수로 표시하지 않는다.
사건 행은 원본 offset과 kind/at/id/node/startedAt로 정확한 기록을 연다. `/records`의 `returnTo`는 API에 보내지 않는 UI 상태이며 앱 내부 `/experiments` 경로만 허용한다. `실험 구간으로 돌아가기`는 게시 ID, `al/as/b/bs`, 서비스와 목록 offset이 포함된 원래 URL로 돌아가 같은 Sheet를 복원한다. 구간 사건과 성능의 시간 대응은 인과 판정이 아니다.

설정의 정본은 `/settings?tab=configuration|models|preprocessors|datasets`다(기본 운영 설정).
운영 설정은 `section` 범위·`edit` 수정 Sheet 선택을 URL에 보존한다. 분배는 서비스·인스턴스·가중치 표와 접힌 JSON에서 편집하며 기존 차단/기본 복귀/순차 적용 의미를 유지한다. 카탈로그 목록은 필드·파일·실행 방식 수와 연결 서비스 수를 우선하고 긴 주소·해시는 원문에 접는다.
기본 `section=targets`에는 CHILL 자동 운영 서비스(`autonomousWorkloads`), 최소 수요(`minimumDemand`), 계획 제외 장비(`excludedNodes`)가 접히지 않은 표로 모인다. 서비스 목록·상세의 자동 운영 값은 이 화면의 `edit=autonomousWorkloads&workload=<UID>`로 이동하며 직접 PUT하지 않는다. 변경 작업이 없는 목록은 진행 열을 숨기고, 진행 서비스와 함께 보일 때 없는 값은 흐리게 표시한다.

| 운영 설정 범위 | 편집 항목 |
|---|---|
| 서비스·장비 | CHILL 자동 운영 서비스, 서비스별 최소 수요(req/s), 계획 제외 장비 이름 |
| 판단 주기와 범위 | 부하 관측 구간, 운영 판단 주기, 에너지 비교 기간, 교체 후 최소 체류 기간, 초기 용량 대기 시간, 하강 안정화 기간, 전환 예비 비율, 하강 용량 여유, 관측 사용 시간 |
| 자원 한도 | 서비스 실행 메모리(bytes), 복제 수·인스턴스 수 상한 |
| 자동 조정 항목 | 배치 위치, 자원 할당, 복제 수, 요청 분배, 배칭, 전력모드, 실행 그룹 소속 |
| 용량 설정 | 완료 여유(ms), 계산 창 길이·창 수·충족 창 수·최소 요청 수·격자 수·이분 탐색 폭·꼬리 표본 수 |
| 고급 | 구성 탐색 동시 계산 수 |

항목 정의는 `lib/scheduler-settings.ts` 한 곳에 둔다. 실험의 전역·단계 운영 설정 패치도 같은 항목 정의와 ms↔ns 정밀 변환을 사용한다. 최소 수요는 서비스 UID별 0 이상 유한한 req/s이며 상한은 없다. 미설정과 0은 별개이고, 현재 서비스 목록에 없는 UID도 운영자가 해제하기 전에는 유지한다. 초기 용량 대기는 0~1시간이며 ns 정밀도를 보존한다.
실험 운영 설정 패치의 상태·항목 선택·값 변환 UI는 `features/settings-patch-editor.tsx`가 담당한다. 공용 작성 필드와 원문 JSON·이탈 경고·입력 근거 선택(`EvidencePicker`)·작성 도움말(`AuthorHelp`)은 `components/evaluation-author-fields.tsx`에서 실험·부하 작성 화면이 직접 가져오며, 부하 작성 화면은 실험 작성 feature를 import하지 않는다. 설정 항목 정의와 ms↔ns 변환 구현은 기존 `lib/scheduler-settings.ts`·`lib/scheduler.ts`를 그대로 재사용한다.
저장은 작성값 누적 → 현재 설정 재조회 → 변경 전/후 표 → 운영자 확인 → 직전 재조회 → 전체 설정 PUT 순서다. 서비스 편집 Sheet는 첫 입력 전에는 GET과 동기화하고, 첫 입력 시 실제 표시한 설정과 ETag를 함께 고정한다. 이후 설정·버전 변경은 제출을 차단하고 작성값을 유지한다. 최소 수요의 현재값도 서비스명·req/s 소형 표로 표시하며 미설정과 0을 구분한다.
조회 설정과 ETag는 하나의 스냅샷으로 보관한다. 성공 PUT 뒤 재조회한 최신 스냅샷을 이전 PUT 응답의 ETag로 덮어쓰지 않으며, 결과 미확인 복구에서 현재값을 채택할 때도 설정과 ETag를 함께 기준으로 삼는다.
PUT 성공 뒤 GET 재조회가 실패하면 편집 필드는 `change.after`로 전체 교체한다(생략된 편집 필드는 미설정). 이전 스냅샷에서는 GET 전용 `staticRouting`·`powerObservation`·`meters`·`requestTimeout` 네 값만 유지한다. 분배 선언을 아직 GET으로 확인하지 못했으면 `조회 필요`와 비활성 동작을 표시하며, 성공 GET에서 확인한 빈 선언과 구분한다.
편집 UI가 없는 서버 설정 `defaultOperationCost`(prepare/remove/power의 seconds·joules), `defaultIdlePower`(nodeWatts·perInstanceWatts), `defaultDispatchOccupancyNanos`도 조회값 그대로 PUT에 포함하고 충돌 비교에 포함한다. 이 세 필드는 GET 전용 선언이 아니므로 PUT 성공 후에는 `change.after`를 따른다. ns 정수는 기존 bigint 직렬화 경로로 보존한다.
W1 서버 계약: GET `/api/v1/scheduler/settings`의 `ETag: "<ConfigMap resourceVersion>"`를 보관하여 PUT `If-Match`에 그대로 보내며 성공 응답의 새 ETag로 갱신한다. 버전은 ConfigMap 전체의 버전이므로 설정 외 변경에도 바뀐다. 409 `SettingsConflict`는 재조회 후 작성값을 보존한 충돌 비교로 이동하고, 428 `SettingsPreconditionRequired`는 `버전 없음`과 명시적 재조회 동작을 제공한다. 자동 재제출은 없다. ETag 없는 구 서버에는 If-Match 없이 보내며, 그때만 원자적 동시 변경 방지 불가 도움말을 한 곳에 제공한다. 이 대시보드는 W1 서버보다 먼저 또는 함께 배포해야 한다.
PUT 본문 계약은 변경하지 않는다. `internal/serving/scheduler.go:17-51,409-512`의 SchedulerSettings 필드만 보내고 GET 부가 읽기 전용 필드 `staticRouting`·`powerObservation`·`meters`·`requestTimeout`은 절대 보내지 않는다. 원래 전체 PUT·검증·감사·적용·ConfigMap 갱신 근거는 `internal/serving/httpapi/scheduler.go:60-78,99-175,196-215`이며, W1 변경은 버전 헤더와 428/409 계약이다. 요청 분배 변경은 별도 scheduler operations 계약을 유지한다.
구 최상위 목록은 설정 탭으로 이동하며 등록·상세·수정 전용 경로와 변경 충돌/결과 미확인 안전 계약은 유지한다.

카탈로그 조회·변경과 If-Match/UID 기준은 `hooks/use-catalog-resource.ts`, 편집·변경 피드백 UI는 `features/catalog-editor.tsx`, 서비스 목록·상세와 `WorkloadState`는 `features/services.tsx`가 담당한다. `features/management.tsx`에는 모델·카탈로그 상세와 설정 목록만 남긴다. 라우트와 추론 화면은 각 담당 모듈을 직접 import하며 기존 쿼리 키, 충돌 시 작성값 유지, 삭제 대상 UID 고정과 결과 미확인 처리는 그대로 유지한다.

별도 개요·링크 허브·측정 6탭은 폐지한다. 요청 실패와 기존 사용 결과, 현재 사용과 품질, 실행 종료와 측정 통과·정리는 독립이다.
작성·수정은 전용 페이지이며 필수값·출처·변경 요약·점검을 보이고 입력 변경은 점검을 무효화한다. 목록은 breadcrumb 없이, 상세는 상위 맥락 하나다.
실험 작성은 한 단계를 선택해 편집하고 과거 비교 대상을 게시 페이지와 선택 단건 조회로 찾는다. `새 부하`는 기존 `LoadEditor`를 의존 Sheet에서 열며 작성 중인 실험을 유지하고 저장 응답의 새 UID를 해당 단계에 선택한다. 원문 편집은 유지한다. `실험 시작`은 기존 사전 점검 후 생성 POST로 실행을 접수하고 실제 응답의 `/experiments/runs/[name]?uid=…`로 이동한다. 서버 초안 저장이나 별도 시작 API를 추가하지 않는다.
실험·부하 작성의 요청 시각 입력은 `GET /api/v1/evaluations/inputs?kind=trace`만 조회하며 `set`은 부하 시나리오 이름이다. 명시적 `limit=25`·`offset`과 `page.nextOffset`으로 이동한다. 옵션은 서버 `name`(없으면 `key`)과 묶음 그룹을 표시하고 페이지 밖의 선택도 `store/key` 기준으로 보존한다. seed는 evidence 조회가 아니라 워크로드별 uint64 십진 문자열이며 payload는 Dataset 이름·UID 참조다. 무쿼리 전체 반환 계약은 유지한다. 측정 결과 저장 영역은 HTML `v` 플래그 호환 DNS-1123 label 패턴과 최대 길이로 검증한다.
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

`/`는 상태 한 줄 → 서비스 상태(SLO·자율 상태) → 장치 확인 → 진행·사건 순서다.
부하가 없어도 서비스별 행을 유지하고 `부하 없음`을 표시한다. 관측 지연과 목표는 같은 셀에서 비교하며,
자율 상태는 `/scheduler/state?view=summary`의 서비스 UID별 Controlled/Fixed/Disabled와 그 행의 이유만 표시한다. `decision.workloads` 필드가 없는 구 서버 응답에서만 기존 `/scheduler` compact 조회를 사용한다. 명시적인 null·빈 배열은 필드 부재와 구분하며 전체 근거를 내려받지 않는다. 상세 drawer의 선택 당시 근거 조회는 유지한다.
결정 전체 이유를 다른 서비스에 적용하지 않는다. 자율 상태의 관측 시각을 표시하고 화면 일시정지에도 함께 고정한다.
조회 전 부재를 확정하지 않는다. 자율 상태는 summary·구 compact 응답 대기 중 `조회 중`, 받은 결정에 서비스 scope가 없을 때만 `미기록`, 요청 오류는 `조회 실패`다. snapshot 미수신 시 진행 변경·보관 실패·예약과 장치 집계는 `미확인`이며, 응답 후 실제 빈 목록만 0으로 표시한다. 예약·구성·판단 drawer와 전력 이력도 응답 전 `없음`·`미기록`을 표시하지 않으며, 전력 지표 미수신은 `미확인`이다.
장치 확인은 실제 노드 사유·미확인 인스턴스·변경 실패를 연결하며 제외 설정을 구분한다. `/pools`의 인스턴스 사유는 인스턴스 ID·노드 UID가 모두 일치할 때만 사용하고 이름으로 연결하지 않는다. 실행 준비 파드 실패는 짧은 한국어 상태로 표시하며 긴 native stderr는 선택 사건의 원본에 보존한다.
진행·사건은 시각 한 줄, 반복 묶음, 최근 3행과 전체 펼치기를 제공한다. 전체 장치의 서비스 배치·전력모드·전력과 전력 추세는 `풀·장치` 펼치기로 한 단계 아래에 둔다.
서비스 영향과 관측 공백은 독립 상태·aria-live 요약으로 제공하고, 서비스 선택은 공통 Sheet 한 개를 사용한다.
후보·추정·부하·용량 근거는 `/?open=service:UID&evidence=candidates|estimates|load|capacity`로
연결하며, `view=snapshot` 직접 진입도 첫 수신 응답을 고정하고 최신 판단을 명시적으로 채택한다.
`/operations/devices`는 전력 추세와 이름순 장치·배치 표 두 영역이다. 계측 범위와 구성 범위를
구분하고 사양·전력모드·측정/가용/제외 역할·준비/제외 사유·실제 할당은 선택 Sheet에서 조회한다.
구 POOL 상세 링크는 `workload` 범위와 `open` 선택을 분리하며 장치 선택은 `node`로 장치 화면에 매핑한다.
`Feasible`은 `실행 가능`이며 SLO 충족 판정이 아니다. 후보의 서비스 평가 항목이 없으면
`평가 대상 없음`을 표시하고 동일 후보 ID의 유지안·목표는 한 번만 표시한다.
`Preparing`·CHILL 자동 운영 꺼짐·현재 인스턴스 없음이 함께 관측되면 홈·서비스 관측·목록은
`미배치 · CHILL 자동 운영 꺼짐`을 공유한다. 고정(`fixed`) 장치의 `PowerModeUnavailable`은
장치 확인에서 제외하며, 제외 노드와 `PlacementInventoryMissing`은 사유를 유지한다.
진행·사건은 최근 5분 실제 수신 판단 수·유지 수·마지막 시각과 관측된 파드 준비 전이를 표시한다.
운영 재진입 시 `observations/events?from&to&limit=100`으로 최근 15분을 최대 5페이지·500건까지 읽고 실제 사건 identity로 실시간 사건과 합친다. archive에 WebSocket 순번을 생성하지 않는다. 미조회 페이지는 일부 표시, coverage·snapshot.history의 보존 공백은 `보존 공백` 상태어로 유지하며 손실 건수를 추정하지 않는다.
정확한 사건 조회(`observations/events?at`)는 append 시간 범위별 부분 응답의 `nextCursor`를 지원한다. 사건이 아직 없으면 읽은 범위·기록 공백과 `더 찾기`를 표시하고 사용자 조작으로만 다음 범위를 읽는다. 찾았더라도 cursor가 남으면 `이후 기록 미확인`을 표시한다. at cursor는 RFC3339Nano이며 range 조회의 from/to·limit와 혼용하지 않는다. coverage 없는 기존 응답과 조회 시간 초과는 기존 상태를 유지한다.
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
strings, and advanced search fields remain available. GET retains the server ETag;
PUT requires that baseline in If-Match. A 409 preserves the draft and requires
explicit adoption of the new baseline. Runtime application is separate from physical transitions. Permission
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
현재 reader는 [DB-FR-66](../docs/spec/dashboard/SRS.md)에 따라 게시 단건의 `definition.spec.comparisons`와 목록의 `comparisons`로 비교 관계를 읽고 단계 ID·서비스 UID·publicationID로 연결한다. suffix·요청률 유사성 fallback은 사용하지 않는다. 정의 없는 과거 게시는 비교 미기록, 실제 대상 부재는 대상 게시 없음, 권한/서버/timeout은 대상 조회 실패로 구분한다. 정책·적용 SLO는 기록된 identity가 일치하는 정의·실행 결과에서만 읽는다. 비교가 없으면 단독값만 표시하며 빈 Δ 열은 없다. 하위 계획 구간에 단계 전체 실측을 복제하거나 서비스별 에너지를 만들지 않는다.
게시 상세는 게시 판정과 실패 서비스 → 서비스×지표 표 → 부하 part별 성공률 → 에너지·요청 배출 → 차트·구간 근거 순서로 읽는다. D1는 publisher 판정을 사용하며 성공/요청은 `deadlineSuccess/offered`다. 완료·시간 초과·오류·미완료와 미발행은 분리하고 누락 키를 0으로 만들지 않는다. 동결 정의의 선언 SLO와 identity·출처까지 연결된 적용 SLO를 구별하며 ms 정밀도를 유지한다. 풀 평균 전력은 게시된 J를 실측 구간 길이로 나눈 표시값이며 둘 중 하나라도 없으면 미기록이다. 구간 Sheet의 p95 표본 수를 표시하고, 명시적 비교에서는 기존 에너지 상대 차이·성공률 %p 차이·p95 배율을 행렬과 함께 보존한다. 주입은 계획과 실제 기록의 합집합이며 정의가 없는 게시의 기록도 숨기지 않는다. 에너지 상태·노드 사유·최대 계측 공백과 배출 미완료 사유를 함께 표시하며 최신 전력 재계산으로 게시값을 덮어쓰지 않는다. 설정·UUID·비교 조건·원문은 접기 영역에 둔다.
`loadFidelity.issued/unissued`는 수집된 클라이언트 발행 증거/부족 수이며 실제 전송 여부의 전수 확인이 아니다(`hack/evaluation/request_analysis.py`). 최종 결과는 클라이언트 행·누락·초과와 발행 증거 비율을 표시한다. 역사 게시에 gateway 전수·실제 미발행 구분 필드가 없으면 미기록으로 두고 수집 손실을 미발행으로 단정하지 않는다. 요청 배출 판정은 게시 당시 값이다. 정리 실패 게시에서는 게시 당시 정리·잔여 목록과 UID가 일치하는 현재 Run의 PoolAcquired 상태·사유·전이 시각을 분리한다. False면 반환 제안을 숨긴다. residuals=[]는 잔여 없음, null/누락은 미기록이다. `lib/fixtures/experiment-r2b-results.json`은 2026-11-04 조사 GET 저장본이며 수정 없이 렌더 회귀에 사용한다.
에너지 근거는 `power.nodes[]`의 관측 J·공백 비율·최대 간격·읽기 실패·meterComplete·policyMet·sourceIdentity를 노드별 표로 읽는다. 출처/정책의 동일 일반 사유 반복 대신 기록된 두 완전성 축을 분리하며 원인을 네트워크 장애 등으로 추정하지 않는다.
실행 상세는 실행 상태·게시 판정·실패 서비스·결과 링크 → 등록 질문과 실제 워크로드·단계·부하 part·계획 기간 → 활성 단계의 서버 구간 경계(구 서버만 GO 이후 경과·계획상 위치) → 최신 condition 상태와 기록된 절차 시간축·부하 Job → part별 요청률 순서다. 출처의 조회 중·조회 실패·미기록은 확인된 빈 정의와 구별하며, 미취득 동결 정의를 0개·변경 없음으로 표시하거나 현재 정의로 대체하지 않는다. `currentProcedure`, `procedures[]`와 `stages[]`는 기록 순서·childRefs 순서를 보존한다. 시작 시각이 null이면 시작 미기록이다. 종료 시각이 null인 현재 절차만 진행 중·현재까지 소요로 표시하며, 과거 절차의 종료 누락은 종료 미기록·소요 미기록이다. 절차 시간축 유무와 무관하게 풀 확보/반환·실행·정리 등의 최신 condition 상태·사유·시각을 유지한다. 존재하지 않는 procedure.status·stage.phase는 만들지 않는다. 질문은 등록 원문이며 실제 참조 부하와 구별한다. 서비스별 도착 분포와 실행·단계 변경 설정은 표로 읽으며 운영 설정 라벨을 재사용한다. `progress`가 없는 구 서버에서만 GO와 계획 시간·누적 part 기간으로 계획상 구간 위치를 표시한다. 이는 실제 부하 종료나 후처리를 증명하지 않으며, GO 근거가 없으면 계획상 위치도 강조하지 않는다. 조건별 `lastTransitionTime`은 최신 상태이지 전체 전이 이력이 아니며 과거 확보 시각·절차 ETA·실시간 offered 기준 D1를 추정하지 않는다. 이미지 digest·조건 및 정의 원문은 접는다.
활성 단계의 `stages[].progress[]`가 있으면 `부하 → drain → flush → 수집 → 판정`의 관측된 시작·종료 경계를 KST로 기록 순서대로 표시한다(생성기 시계로 재정렬하지 않음). `evaluationProgressOngoing()` 한 순수 함수가 부모 Running·같은 단계·부모 `currentProcedure` 빈 값/미제공, 자식 단계 미종료, 자식 절차 stage/judge, 해당 절차의 가장 최근 `procedures[]` 기록 존재·종료 미기록, 마지막 progress가 그 절차 소유 시작 경계인지 모두 확인한다. 모두 충족할 때만 해당 구간 `진행 중`이다. 부모는 prepare/finish/publish를 소유하며 자식 실행 중에는 절차를 비운다. 부하·drain·flush·수집 시작은 자식 `stage`, 판정 시작은 자식 `judge` 절차다. 절차 결과/실패 기록이 먼저 종료돼도 currentProcedure는 다음 reconcile까지 남을 수 있으므로 currentProcedure만으로 진행 중을 추정하지 않는다. 그 외는 `마지막 관측: 경계 시각`만 표시하며 시작만 남은 경계는 `종료 미기록`, 관측된 종료는 종료 사실로 남긴다. 종료 경계 뒤의 다음 구간을 현재라고 추정하지 않는다. 빈 배열은 `구간 경계 미기록`이며 계획 표시로 되돌리지 않는다. 누락 경계를 GO·계획 기간·Job·절차·판정으로 채우지 않는다. drain 종료의 `producerRowsMissing`는 서비스별 생성기 결과 행 부족 수이며 0도 그대로 표시하고 비영 값을 경고로 강조하지 않는다. 진행 중 요청 수나 최종 reducer 결과와 다르다. 수집 종료의 `bytes`는 저장된 원시 자료(압축 파일 포함) 크기이며 전송량이 아니다. 큰 factor/stage/judge/cleanup·게시 절차의 권위는 계속 `procedures[]`다.
`progress`가 없는 구 서버는 GO 기준 계획 시간이 지나면 남은 시간 0 대신 `계획 종료 후 경과`를 표시한다. 기록된 judge·cleanup-jobs 또는 그 뒤 절차와 부모 finish·publish의 시작 또는 종료가 있으면 `종료 처리: 절차 중 (시작 시각)`으로 표시하며, 종료 기록이 있으면 완료 사실과 종료 시각을 구별한다. 시작 없이 종료만 기록됐으면 `시작 미기록 · 종료 HH:MM:SS`를 표시한다. 절차 필드가 없는 구 서버는 `종료 처리 단계 미기록`, 필드가 있지만 후속 기록이 없으면 `종료 처리 신호 미수신`이다. 실제 부하 종료나 정상 진행을 추정하지 않는다.
자식·부모 절차 시작·종료, 잠정 집계 `provisionalD1.asOf`, 독립적인 `lastProgressAt` 중 유효한 최신 시각으로 `진행 신호 없음 N초`를 표시한다. 시각이 없으면 미기록이며 임계값으로 정체·정상·실패를 판정하지 않는다. flush·수집 등 부하 후 구간은 경계마다 한 번만 신호를 게시하므로 조용함 자체는 멈춤의 근거가 아니다. 각 실행 단계의 `loadJobs`가 있으면 이름·상태(진행 중/완료/실패)·시작·완료·실패 사유를 읽는다. Job UID는 행 식별에만 쓰고 없는 workload·shard는 만들지 않는다. 필드가 없으면 표를 생략하고 빈 배열은 `부하 Job 없음`, 누락 시각·사유는 미기록이다.
활성 실행 단계의 `stages[].provisionalD1`이 있으면 게시 결과와 같은 서비스 표를 잠정 모드로 표시한다. `asOf`와 `closedThrough`(집계 마감), 성공/필요(`deadlineSuccess/requiredSuccessfulRank`), 현재 요청(`offered`), 결과 분류와 `결과 대기·미발행 포함`(`unfinishedOrUnsent`), 서버의 도달 가능/불가(`reachable`), 발행 품질과 구간별 집계를 읽는다. 현재 요청은 집계 마감까지 예정된 도착이며, 결과 대기에는 진행 중 요청이 포함되어 이후 성공으로 바뀔 수 있다. UI는 reachable을 재계산하지 않고 D1 판정은 항상 `진행 중`으로 표시한다. 필드 없는 구 서버·미시작·비활성 단계에는 잠정 표를 만들지 않고, 누락 값은 미기록, 조회 오류는 조회 실패로 남긴다. `lib/fixtures/scheduler-summary-workloads.json`은 서버 `schedulerSummary` wire 계약의 합성 fixture로 운영 보드의 `decision.workloads` 직접 읽기와 compact 조회 생략을 검사한다(실서버 캡처가 아님).
잠정 결과 막대는 `outcomes`만 쌓고, 해당 분류와 겹치는 `unfinishedOrUnsent` 집계는 별도 수치로 표시하여 중복 합산하지 않는다. 생성기 지연 평가는 최종 표와 같은 `generatorQuality.thresholdLabel`을 사용하며 관측 상태(`status`)를 지연 판정으로 바꾸지 않는다.
최종·잠정 서비스 표의 SLO 백분위는 소수 값을 반올림하지 않는다(`p99.5`). 목표 ns는 정밀 JSON의 number·bigint를 모두 받아 기존 ns→ms 표시 helper로 변환하며, 누락 값만 미기록으로 남긴다.
종료 시각이 null인 현재 절차는 실행 종료 시각이 없고 Done 상태가 아닐 때 진행 중으로 표시한다. 예외로 게시 절차는 정리 완료로 실행 종료 시각·Done 상태가 기록된 뒤에도 게시 ID가 없으면 계속 진행 중이다. 그 외 종료 시각 누락은 종료 미기록·소요 미기록으로 남긴다.
준비 실패 게시의 기록된 spec과 명시적으로 비어 있는 `loadScenarios/workloads`는 읽기 경계에서 보존한다. 이때 질문·선언 단계는 읽되 취득하지 못한 부하·워크로드는 미기록으로 남긴다. 한쪽만 빈 snapshot, 잘못된 항목·중복 UID·비어 있지 않은 snapshot의 미해결 참조는 기존 경계 검사를 유지한다.
운영 판단 근거와 기록은 결정 전체 상태·사유와 서비스별 `Controlled/Fixed/Disabled` 상태·이유·대상을 분리한다. 설정 ON이나 전체 Keep으로 서비스 상태를 추정하지 않는다. 기록의 서비스 상태 변화 보기는 조회한 현재 페이지의 첫 상태와 `(uid,state,reason 코드)` 변화만 표시하며 페이지 범위·이전 페이지 비교 불가를 명시하고 전건 자동 조회를 하지 않는다.
실행은 `/experiments/runs/[name]?uid=…`, 부하 설정은 `/experiments/load-scenarios`다. 작성·수정은 각각 `/experiments/new`, `/experiments/runs/[name]/edit?uid=…`, `/experiments/load-scenarios/new`, `/experiments/load-scenarios/[name]/edit` 전용 페이지다. 원본은 `copy` 또는 `publication`, 수정 UID는 `uid` query로 복원한다. 구 `/experiments/[name]/edit?uid=…`는 새 실행 편집 URL로 리다이렉트한다. `/experiments/runs/[name]`은 항상 실행 상세이므로 이름이 정확히 `runs`인 실행의 편집은 새 `/experiments/runs/runs/edit?uid=…` 경로를 사용한다. 실행/게시 목록은 상세를 중첩하지 않고 행에서 이동한다. 구간 Sheet는 단계·후보/기준·실제 기간과 원값 → 같은 기간 사건 → 접힌 비교 조건·규칙·정의 출처·원문 순서다. `a/al/as/b/bs/service`가 차트·구간 버튼·표·Sheet의 공통 선택이며 새로고침과 기록 복귀에도 유지된다. 사건 주체는 기본 후보이고 기준 선택은 `eventSide=baseline`에 보존한다. Sheet가 닫혀 있어도 비교쌍 선택은 URL에 저장한다. 구간 열기는 history push, 내부 서비스·사건 주체 변경과 닫기는 replace다. 닫기는 identity를 유지하고 `sheet=closed`로 개폐만 표시하며, 다시 열면 표식을 제거한다. 기존 표식 없는 구간 딥링크는 Sheet를 열고 닫은 뒤에도 같은 선택 수치·게시·서비스·offset·복귀 문맥을 유지한다. 작성 중 새 부하 Sheet를 닫으면 그 세션의 지연 저장 완료는 부모 초안을 바꾸지 못한다. 예전 `/experiments?name=…&uid=…`와 `/evaluations` 선택 URL은 같은 실행의 canonical 상세로 이동하고, 게시 없는 `/experiments/results`는 게시 목록으로 이동한다.
평가 경계: `운영 / CHILL 자율 제어`는 상시 자율 운영이고 `실험 / 외부 평가`는 서빙 파이프라인을 블랙박스로 평가한다. W1 서버와 함께 절대 요청률·기간·도착 분포·워크로드별 seeds/datasetRefs/traceRefs로 cutover하며 옛 객체는 삭제한다(이주 없음). κ/d/rateBasis/alpha·시작 상태 복원·스냅샷·이미지/profile 대조·내부 제어·장애 주입 입력은 없다. 전역·단계 운영 설정 변경은 운영 설정 화면과 같은 `lib/scheduler-settings.ts` 항목 목록을 쓰는 불투명 `settingsPatch`이고 부하 발생기 `nodeName`을 지정한다. 사전점검은 liveTarget의 resource·실제 이미지 ID·ready를 관측하며 digest 채택 없이 현재 준비 상태로 접수한다. 배치·전력·인스턴스 변화와 사건은 결과의 `관찰`로만 읽는다.
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
