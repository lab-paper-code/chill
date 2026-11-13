# CHILL Dashboard

CHILL-internal Next App Router module, built independently and deployed in the
`chill` Helm release. ChillServer owns identity, authorization, declarations,
operations, statistics and evidence.

## 카탈로그 등록 시작

[카탈로그 준비 안내](../config/samples/catalog/README.md#처음-등록하는-운영자)에서
게시된 OCI 자산, 내부 파일 배치, 아키텍처와 입출력 계약을 확인한다.
외부 작성자가 자산을 만들고 게시하며 플랫폼 관리자가 API 권한과 Pod의 이미지 접근을 준비한다.

1. `/settings?tab=models|preprocessors|datasets`에서 종류와 신규 등록을 선택한다.
2. 준비한 단일 YAML/JSON을 파일 선택 또는 붙여넣기로 읽는다.
   문서는 해당 `edge.dacs.io/v1alpha1` kind의 `metadata.name`과 `spec`,
   또는 기존 API JSON `{name,spec}`를 사용한다.
3. 자리표시자를 게시된 digest, architecture, 내부 경로와 계약으로 바꾸고 문서를 검토한다.
4. 명시적인 등록 버튼에서 `POST /api/v1/{models|preprocessors|datasets}`를 보낸다.
   ModelSpec과 대상 Preprocessor를 먼저 등록하고 Dataset을 연결한 Workload를 만든다.
5. 목록 또는 단건 GET으로 저장 결과를 확인한다. 응답 유실 시 자동 재제출하지 않는다.

파일 선택과 검토는 로컬 초안 단계다. 실제 image pull, 코드 적재, 장치 호환성과
측정 및 서빙 준비 상태를 별도로 확인한다. 등록 문서에 token, 비밀번호나 Secret 내용을 넣지 않는다.
`403`은 서버 `message`로 원인을 확인하고 관리자에게 요청한다.

구조 예제:
[ModelSpec](../config/samples/catalog/edge_v1alpha1_modelspec.yaml),
[Preprocessor](../config/samples/catalog/edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml),
[Dataset](../config/samples/catalog/edge_v1alpha1_dataset.yaml).
예제 registry 주소와 digest는 게시 증거가 없다. 대시보드는 모델 파일 업로드, 변환,
registry 탐색, 외부 URL 가져오기와 Kubernetes 직접 쓰기를 수행하지 않는다.

### 프로파일 측정 요청 준비

`/measurements/requests/new`에서 직접 작성, YAML/JSON 가져오기 또는 기존 요청 복사를 선택한다.
실제 Workload를 선택하면 현재 정의의 모델, 전처리기와 Dataset을 가져온다.
Workload 미선택은 폼 오류다. 서버가 Workload 존재, 입력 조합, SLO와 표본 수를 최종 검증한다.
복사 후 입력 조합을 바꾸면 상속한 Workload 이름을 지운다.

- DeviceClass, Node, 실행 경로와 전력 모드는 실제 카탈로그와 장비 가용성을 확인해 선택한다.
- `activeProtocol.operatingPoints`의 이용률, 희소 요청 간격, 운영점 측정 시간,
  부하 종료 후 전력 기록 시간, 요청 수명, 전달 여유와 프레임 바이트 상한을 채운다.
  누락된 측정값은 직접 입력하며 임의 기본값을 만들지 않는다.
- 운영점 시간은 30초 이상이며 `postLoadDurationSeconds`는 양의 정수다.
  전체 활성 구간과 metric 기록 창은
  `(이용률 개수 + 2) × 운영점별 측정 시간 + postLoadDurationSeconds`를 포함한다.
- 배치 크기는 1부터 자동 탐색한다. 공개 BEO에는 `batchSizes`가 없고
  완료한 낮은 배치 결과를 보존한다. 배치별 반복은 정확히 3회다.
  `retryLimit`은 재시도 가능한 인프라 오류에 적용된다.
- 로컬 문서 검토는 서버 수명주기 metadata와 status를 제외하고 spec을 보존한다.
  `?dryRun=All`과 `/preflight`는 선택 진단이다. 유효한 로컬 입력은 최종 POST로 접수하며
  이 진단의 성공을 제출 전제조건으로 삼지 않는다. 입력 변경은 이전 진단을 지운다.
- 중복 제출을 막고 응답 유실을 결과 미확인으로 표시한다. 같은 이름의 접수 기록부터 조회한다.

요청 문서 다운로드는 재사용할 metadata와 spec을 보존한다.
원문에 민감정보가 있으면 다운로드에도 포함되므로 입력 전에 제거한다.
복사는 원본 namespace와 UID를 고정한다. CPU 예약량, quota와 affinity는 입력하지 않는다.
구조는 [BEO 예제](../config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml)와
[BEO 타입](../api/v1alpha1/batchenergyoptimization_types.go)을 따른다.

### 분배 점유시간 측정

`/measurements/requests/new?kind=dispatch-occupancy`에서 BEO, Workload, 실제 target Node와
배치를 선택한다. 서버 dry-run이 제공한 이름과 동결 `task.spec`를 확인한 뒤 제출한다.
프로토콜 입력은 서버가 소유하며 검사 identity가 바뀌면 제출을 막는다.
응답 유실 시 같은 이름의 기록부터 조회한다.

서버가 제공한 warmup, burst, minimumPairs, workers, lifetime과 drain을 그대로 표시한다.
공급 방식은 실제 `spec.supplyMode`가 `closed-loop`일 때만 고정 사실로 표시한다.
커버리지의 `schedulerD`와 signed `observedD`는 별도 근거다.
관측 중앙값, IQR, p10/p90과 음수 비율을 보여 주되 브라우저가 채택 판정을 만들지 않는다.

기간 조회는 `/api/v1/observations/occupancy?from=&to=`를 사용하며 exact Node UID,
profileID, 배치와 입력 조건이 일치하는 행만 표시한다.
시간 범위는 정확한 ns로 검증하고 서버가 제공한 상한을 적용한다.
실행 실패와 근거 오류는 원래 사유를 보존한다.
삭제는 영구 삭제 확인 후 UID와 `If-Match`를 전달한다.
Measuring 상태는 삭제를 막고 409는 최신 조회로 연결한다.

## Fixed upstream source and license

Source fork: https://github.com/shadcn-ui/ui.git at commit
`e8c3143b1cd191280befcd6c9538284bb43399a8`.
The application derives from `templates/next-app` and the `dashboard-01` block.
`LICENSE.shadcn` preserves the upstream MIT copyright and permission notice for
copied/adapted code. Other CHILL code retains its own licensing status.

TanStack Table supplies tables and Recharts displays server-provided statistics.
Asta Sans is fetched at build time and self-hosted. Synthetic live fixtures are
labelled 검사용 and require `CHILL_DEMO_REPLAY=1`.

## Runtime

Node 24.21.0; pnpm 10.33.4. From `chill-dashboard/`:

```sh
pnpm install --frozen-lockfile
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

The image uses Next `output: standalone`. It includes `.next/standalone`,
`.next/static`, `public`, `server/` and the `ws` runtime dependency.
From the packaged runtime root, `node server/live-proxy.mjs` starts the proxy
on `0.0.0.0:3000` and its standalone Next child on loopback.
`pnpm start` invokes the same proxy entry point. The Dockerfile assembles the
required layout; a bare source checkout lacks its root standalone `server.js`.

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
| `CHILL_POLL_INTERVAL_MS` | Default `5000` |
| `CHILL_DEMO_REPLAY` | Exact `1` enables authenticated synthetic scene controls |
| `NODE_EXTRA_CA_CERTS` | Trusted custom CA PEM file |

`GET /api/config` exposes safe display/bound settings.
`/api/chill/v1/...` relays to fixed-origin `/api/v1/...` with TLS verification,
bounded time and bytes, cancellation, no redirects and no cookie forwarding.
Malformed configuration returns no-store `503 DashboardConfigInvalid`.
Relay failure retains a Dashboard error code and `unknownOutcome`.
`X-Chill-Relay-Timing` covers the complete relay.
Inference delivery timeout is infer budget plus query budget.

### Caller permissions and sessions

Use an administrator-approved account. The Dashboard ServiceAccount does not
represent the human caller. `chill-serving-manager` grants reads;
`chill-scheduler-editor` grants scheduler settings PUT and operations POST.
The same-name namespace Role grants update to the configured scheduler ConfigMap.
Administrators bind the required roles explicitly. Path and ConfigMap grants
are both required for scheduler writes.

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

## 정본 경로와 탐색

[Dashboard 요구사항](../docs/spec/dashboard/SRS.md)과
[설계](../docs/design/dashboard/README.md)가 화면 계약을 소유한다.

| 업무 | 경로와 책임 |
|---|---|
| 운영 | `/` 서비스 영향과 실제 배치, `/operations/devices` 장치와 전력 |
| 서비스 | `/workloads`, `/workloads/[name]?uid=…` 정의와 운영 상태 |
| 측정 | `/measurements`, `/measurements/requests`, `/measurements/requests/new`, `/measurements/results` |
| 실험 | `/experiments`, `/experiments/runs/[name]?uid=…`, `/experiments/results`, `/experiments/load-scenarios` |
| 기록 | `/records` 판단, 변경, 요청과 전력의 실제 범위 조회 |
| 설정 | `/settings?tab=configuration|models|preprocessors|datasets` |

목록은 URL에 선언한 필터, 정렬과 페이지를 유지한다. 연관 이동은 대상 UID,
namespace, 기간과 선택을 보존한다. 외부 return URL은 허용하지 않는다.
기록의 페이지 건수는 해당 페이지 범위다. 원본 cursor와 append offset을 보존한다.
사건의 같은 시각은 인과관계 증거로 해석하지 않는다.

### 운영 설정과 변경

`section=targets`에서 자동 운영 Workload, 최소 수요와 제외 Node를 편집한다.
`planSearchStrategy`는 `v1` 또는 `v2`를 선택한다. 서비스별 `autonomousWorkloads`는
계획 참여 여부다. 실험 settingsPatch도 같은 `lib/scheduler-settings.ts` 정의를 사용한다.
자원 한도는 GPU와 메모리 계약을 따르며 CPU 전용 할당을 만들지 않는다.

설정 GET의 ETag와 본문을 하나의 기준으로 보관하고 PUT `If-Match`에 보낸다.
428은 버전 재조회, 409는 초안을 유지한 충돌 비교로 처리한다.
현재값 조회, 변경 비교와 최종 확인 뒤 전체 설정 PUT을 수행한다.
임시 변경 목록에 담는 동작은 서버에 저장하지 않는다.
미적용 변경의 이탈 확인을 유지한다. 지원하지 않는 브라우저의 같은 문서
뒤로/앞으로 이동에서는 이 확인이 없을 수 있다.

GET 전용 `staticRouting`, `powerObservation`, `meters`, `requestTimeout`은 PUT에서 제외한다.
편집 UI가 없는 `defaultOperationCost`, `defaultIdlePower`,
`defaultDispatchOccupancyNanos`는 조회값을 보존한다.
분배 변경은 별도 operations POST를 사용하며 서버가 경로 호환성을 판정한다.
서비스의 `Reject`/`Delay`, 대기 상한과 선택적 요청 최종 기한은 SLO와 별도 입력이다.
실제 적용 정의와 원하는 정의를 구분해 읽는다.

### 관측과 실험 해석

운영 보드는 실제 live `snapshot.routing`을 사용한다.
REST-only 인스턴스에서 현재 분배를 추정하지 않는다.
연결 상태와 지표 관측 나이를 구분하고 heartbeat로 오래된 지표를 새것으로 만들지 않는다.
현재 서비스별 제어 상태는 서버의 `Controlled/Fixed/Disabled`와 해당 사유를 표시한다.
설정 ON이나 전체 Keep으로 개별 서비스 상태를 추정하지 않는다.
용량 판단의 요청률, 검사 결과와 유지/선택안은 예측 근거이며 현재 처리량 측정과 구분한다.

라이브 완료율과 SLO 안 비율은 종료 요청을 분모로 사용한다.
실험은 예정 요청 전체와 서버가 게시한 SLO 충족 판정을 사용한다.
완료 표본 분위수에 실패를 성공 표본으로 섞지 않는다.
비교 출처는 [실험 정의 기반 정책과 SLO](../docs/spec/dashboard/SRS.md#실험-정의-기반-정책과-slo)를 따른다.
정의와 UID가 일치하는 게시만 연결하며 이름, suffix나 현재 Workload로 과거를 복원하지 않는다.
누락 필드는 미기록으로 남긴다.

MEASURED의 `power.wholePoolJoules`는 풀 전체, PARTIAL의 `power.observedJoules`는
부분 관측 에너지다. UNAVAILABLE은 사유를 표시한다.
공유 전력을 서비스별로 중복 합산하거나 페이지 에너지를 전체 기간으로 확대하지 않는다.
잠정 SLO 집계는 서버의 `provisionalD1`을 그대로 읽고 진행 중으로 표시한다.
`unfinishedOrUnsent`는 별도 수치이며 outcome 막대와 중복 합산하지 않는다.
실행, 측정 품질, drain, 정리와 풀 반환 상태는 독립적으로 읽는다.
절차와 progress의 실제 시작/종료 기록만 표시하고 조용함만으로 정체를 판정하지 않는다.

### 읽기와 표시 경계

서비스, 커버리지, 측정 요청과 프로파일 목록은 명시적 summary projection을 사용한다.
`lib/measurement-summary.ts`가 정밀 정수를 보존하며 응답을 복원한다.
디코딩 오류는 `MeasurementSummaryInvalid`로 표시하고 빈 목록으로 바꾸지 않는다.
현재 측정은 서버의 `currentMeasurement`를 읽는다. 최신 실패를 과거 성공으로 교체하지 않는다.
카탈로그 조회 오류와 측정 실행 오류도 따로 표시한다.

관측의 결측, 실측 0, 미기록, 권한 거부와 조회 실패를 구분한다.
`lib/status.ts`와 `components/status-cell.tsx`를 재사용한다.
상세는 한 Sheet만 열고 키보드 선택, Esc와 focus 복원을 유지한다.
좁은 폭에서는 라벨/값을 재배치한다. 원본과 긴 식별자는 접힌 영역에서 보존한다.
JSON 내보내기는 정밀 수치와 출처를 보존하며 인증정보를 제거한다.
요청 문서 다운로드는 입력 원문이므로 민감정보가 없는지 별도로 확인한다.

## Local live fixture

From `chill-dashboard/`, run `pnpm fixture:live`. It binds loopback and prints a
temporary CA path. Its TLS hub/proxy serves `https://localhost:3445`, upstream
uses port 3446, and standalone Next uses port 3131.

In a second terminal:

```sh
export CHILL_BASE_URL=https://127.0.0.1:3446 CHILL_NAMESPACE=fixture CHILL_SYSTEM_NAME=fixture
export CHILL_DEMO_REPLAY=1 NODE_EXTRA_CA_CERTS='<printed CA path>'
pnpm build
cp -R .next/static .next/standalone/.next/
HOSTNAME=127.0.0.1 PORT=3131 node .next/standalone/server.js
```

Use synthetic token `s2-local-token`; it has no cluster authority.
Local controls `/__fixture/scene?name=empty|first|expand|shrink`,
`/__fixture/resync`, `/__fixture/cluster` and `/__fixture/status` exercise scenes,
reconnect and issue events. Fixture settings GET/PUT checks If-Match and resets
on restart. Stop the fixture to remove its temporary certificate/key.
These are synthetic inputs without cluster or deployment access.
