# CHILL Dashboard 구현 설계

[Dashboard SRS](../../spec/dashboard/SRS.md)가 요구사항 ID와 수용 기준의 정본이고,
[시스템 명세](../../spec/README.md)가 제어·측정 의미의 정본이다. 이 문서는 현재 구조, API 소유권과 연결 공백만 유지한다.
전체 SRS 완료와 코드 연결·fixture·격리 배포·실클러스터 수용은 서로 다른 상태다.

## 1. 현재 아키텍처

| 경계 | 결정과 근거 |
|---|---|
| 모듈 | 루트 `chill-dashboard/`, 독립 build/deploy, 같은 Helm release의 별도 Deployment·ClusterIP. 웹 Pod는 `automountServiceAccountToken: false`. |
| 스택 | Next.js App Router·React·TypeScript·Tailwind·shadcn/ui. 공식 `templates/next-app`/`dashboard-01`, MIT commit `e8c3143b1cd191280befcd6c9538284bb43399a8`; 복사 범위와 저작권 고지 보존. |
| 버전 기준 | Next 16.3.6, React/DOM 19.3.0, TypeScript ^5, Tailwind ^4, Node 24.21.0, pnpm 10.33.4. 실제 정확한 의존성은 lockfile. TanStack Table v9, Recharts 3.8.0, Query 5.104.1, Go coder/websocket v1.8.13, Node ws 8.18.3. |
| HTTP | 브라우저 → same-origin `/api/chill/v1/...` Route Handler → 고정 HTTPS ChillServer `/api/v1/...`. `CHILL_BASE_URL`, `NODE_EXTRA_CA_CERTS`는 서버 전용. CA·hostname 검증, redirect 상태 보존. |
| 공개 진입점 | `cmd/chill-server`가 Serving과 인증된 Console 조회를 조립. `--dashboard-url`은 API/health 외 화면·정적 자산·BFF를 private Dashboard로 중계. ingress 없는 테스트는 HTTPS NodePort 31104; chart 기본은 ClusterIP. |
| WebSocket | WSS `/api/live` → Node `server/live-proxy.mjs` :3000 → 고정 HTTPS `/api/v1/live`; 일반 HTTP는 자식 `.next/standalone/server.js` loopback :3001. Next custom server 없음; 실패/SIGTERM은 두 프로세스 종료. Upgrade의 원래 Origin/Host 보존. |
| 업무 소유권 | 인증·도메인 검증·변경 선언·진행·판단·통계·Evidence는 ChillServer 뒤 CHILL이 소유. 웹은 Kubernetes·firstfloor·S3·Node를 직접 호출하지 않음. |
| 상태 | 사용자별 탭 QueryClient의 서버 cache, 로컬 폼, URL 대상/UID/필터/기간. 신원 변경·logout은 socket/timer 종료 → 요청 취소·cache clear·QueryClient 교체·민감 폼 remount. 늦은 callback은 이전 cache에서 종료. |
| 정보 구조 | 흐름 보드 → UID별 POOL → 인스턴스·Node·예약 → 선택 당시 근거. 고정 구조는 CSS grid/inline SVG, 시계열은 Recharts; 자동 배치 그래프 의존성 없음. 키보드·focus 복원·reduced-motion·텍스트 상태·가로 스크롤 유지. |
| 운영자 | 카탈로그·설정·측정 요청/근거 유지. 과거 `/records`, `/settings` 링크 모음과 일반 미지원 측정 준비 페이지는 부활하지 않음. 지원 종류만 명시하고 나머지는 notFound. |

## 2. 인증·중계·변경 계약

| 항목 | 현행 계약 |
|---|---|
| 신원 | 기존 Kubernetes bearer를 탭 메모리에만 보관, 새로고침 후 재입력. BFF는 원본 Authorization 전달. ChillServer TokenReview + 정확한 verb/path SAR, 검증된 username/UID만 actor로 기록. |
| 권한 | 401 누락/무효, 403 실제 SAR 거부, 503 review 통신·권한·evaluation 오류. non-resource path는 `/chill` 접두사. Console private leg의 서버 identity는 사용자 SAR를 대체하지 않음. |
| HTTP 제한 | method/path allowlist; Authorization·If-Match·Content-Type·추론 timeout·필요 CHILL identity header만 전달. 요청/완전한 응답 각각 8 MiB, 응답 header 16 KiB. 상태·요청 ID·Workload UID 보존, no-store. |
| 초기 웹 설정 | page 50/최대 200, 기본 구간 1시간/최대 24시간, 갱신 5초, 조회 timeout 10초/변경 60초. 추론은 사용자 timeout과 서버 상한 및 중계 여유 사용. WS 연결 수명에 HTTP 한도를 재사용하지 않음. |
| 변경 | 작성→전후 정의·제공된 영향 확인→제출→원본 조회. Model/Workload If-Match=metadata.resourceVersion. 409 최신 baseline 명시 채택 시 draft 유지, 같은 UID만 제출. 201/202/실제 완료 구분; 삭제는 원래 UID 제거 확인. 응답 미수신은 결과 미확인, 원본 조회 후 명시 재제출. |
| 설정 | 전체 SchedulerSettings 마지막 적용값이 현재 설정. duration 네 필드·Serving 메모리·반복 비교/탐색 상한·seed·boolean 아홉 필드. seed/int64·메모리/uint64·duration은 reviver 원문/JSON.rawJSON으로 보존, ms→ns 정확 변환. 지원 브라우저만 저장. appliedAt은 저장/Runtime 적용 시각; 버전 조건·영향 예측은 서버 대기. |
| 설정 권한 | unbound scheduler-editor ClusterRole과 serving namespace Role을 관리자가 binding. Role은 지정 ConfigMap 하나 update만 허용. |
| 조회 | retry/focus/reconnect refetch=false, AbortSignal로 이동/logout 취소. 보이는 운영자 영역만 제한 polling; 라이브는 push. 401/403 payload 숨김. 독립 Query로 미지원·빈 값·실패·오래됨·동작 실패 분리. |
| 원본·안전 | RFC3339Nano·정밀 정수·null/0 구별. 외부 문자열/JSON은 텍스트, export 복사본에서 credential 제거. API code/field/reason 보존; 문장은 `lib/labels.ts`, 미등록 원본도 보존. |
| 입력 | 전처리 input/output의 이름·순서·형식·크기를 모든 모델에 동일 적용. firstfloor 고정, ONNX=ort-cpu/ort-cuda, RKNN=rknnlite(rk3588·선언 batch·FP32 rank-3 입력 하나). 도메인 검증은 서버. |
| 측정·전환 | immutable Run/campaign 요청은 기존 schema/admission/controller 사용. 프로파일은 검증된 성공 Run/campaign, Evidence는 RetainedLoader 검증 경계. 자원 제외는 실제 drain/actuator 계약. 저장 계약 변경은 ADR 0019 원칙. |
| 시간·성능 경계 | 브라우저 왕복=fetch→완전 본문, X-Chill-Relay-Timing=서버 대기 포함 중계 왕복, BFF 자체 비용 별도. 목표 p95 표시 2초/BFF 100ms; Workload 10·요청 10,000/1시간·동시 5·5초 갱신 기준이며 달성 증거 아님. |

## 3. 라이브 API 계약 v1

| 항목 | 계약 |
|---|---|
| 공통 | JSON 텍스트 `{type,seq,at}`. seq≥0 JS safe integer, at=원본 RFC3339Nano UTC. hello/snapshot은 head checkpoint이며 순번 생성 없음; delta/event/metrics/heartbeat는 전역 +1. hello.at=송신, snapshot.at=head 발행(0이면 hub 생성). |
| type | 공통 필드 외 payload | 발행 |
|---|---|---|
| `hello` | `protocol:"chill-live/1"`, `serverAt`, `retainFrom`(재개 가능한 최소 seq, 빈 버퍼면 head+1) | 상류 인증 성공 후 한 번 |
| `snapshot` | `workloads`, `nodes`, `instances`, `routing`, `decision`, `actions`, `reservations` 전체 | 최초 접속, 재동기화 |
| `delta` | snapshot 키의 부분집합; `removed?:{instances?:[id],actions?:[actionKey]}` | 상태 변화 |
| `event` | `kind:"decision"`와 `decision`, `kind:"action"`와 `action`, 또는 `kind:"cluster"`와 `cluster` | 판단, 작업 시작·종결, 기록된 Pod·observer issue 사건 |
| `metrics` | `bucketAt`(UTC 초 시작), `workloads:{uid:Metric}`, `nodes:{uid:{watts:number|null,observedAt:string|null}}`, `pool:{wholeWatts:number|null,activeWatts:number|null}`, `gaps?:[{from,to,scope,reason}]` | 매초, 같은 버킷 수정 시 전체 대체 |
| `heartbeat` | `serverAt` | 5초 |

snapshot의 작은 DTO는 다음과 같다. 아래 `?`는 생략 가능, `null`은 확인할 값 없음이다.

- `workloads[]:{uid,name,model,definitionID:string|null,slo:{percentile,targetLatencyNanos},autonomous,admission,pending,readiness:{ready,reason,observedAt}}`. `pending`은 배정 전 유입 큐 길이(runtime `WorkloadState.Pending`).
  적용 정의가 있으면 그 이름/모델/SLO를, 아직 없으면 등록 정의를 표시하고 definitionID는
  null, admission/readiness.ready는 false다. 동일 이름의 새 UID와 합치지 않는다.
- `nodes[]:{uid,name,powerMode,ready,measurement,reasons?,observedAt}`. 전력 W는 `metrics` 프레임에만 있다.
- `instances[]:{id,node:{uid,name},ownerWorkloadUID,profileID,ready,stopped,routable:{uid:boolean},running:{uid:number},waiting:{uid:number},observedAt}`.
  단일 `workloadUID`와 binding 수로 추정한 `state`는 없다. 준비(`ready`), 배분 가능 여부(`routable`),
  실제 배분(`routing`), native 실행(`running`)과 작업(`actions`)은 독립 사실이다.
  `waiting`은 배정 뒤 native queued/deferred이며 `AssignedUnknown`을 포함하지 않는다.
- `routing:{workloadUID:{instanceID:weight}}`는 실제 배분이다. 선언과 목표는 근거 REST에서
  별도로 읽는다. `decision:null|{at,state,reason}`에서 큰 후보·프로파일은 제외한다.
  판단 identity는 원본 RFC3339Nano `decision.at`이다. `SchedulerDecision`에 없는 id 필드를
  새 원본 식별자로 가정하지 않는다. 근거는 선택한 `decision.at`의 journal 구간 조회다.
- `reservations[]:{owner,nodes:[{uid,name}],workloadUIDs:[uid],podUIDs:[uid],instances:[id],reason}`는
  판단 snapshot의 `SchedulerReservation`(`internal/serving/scheduler.go`)에서 `target`·`routeOnly`를 뺀 것이다.
  reason 미제공은 `"미확인"`이며, 빈 배열이 예약 해제를 나타낸다. 작업 종결로 해제를 추정하지 않는다.
- `actions[]:{id,kind,node:{uid,name},workloadUID,status,startedAt,completedAt?,expectedSeconds:number|null,expectedSource:{costID,reference,statistic:"mean",trialCount}|null}`.
  identity인 `actionKey`는 JSON 튜플 `[id,node.uid,startedAt]`이다. 이벤트의 `action`에는
  `phase:"started|completed|failed"`와 작은 `result?:{status,error?}`가 추가된다.
  `starting/running`만 진행 중이며 `complete/failed/partial`은 종결이다(`OperationResult.Status`,
  `internal/serving/kube/operations.go:344-348`). `partial`은 `phase:"failed"`로 보내되 원본 status를 유지한다. 예상 시간·출처는
  [T4 관측 설계](../serving/observability-split.md)를 따른다.
- `cluster:{kind:"pod"|"issue",at,event,pod?:{namespace?,name?,uid,node?,phase?,ready?:string},source?,reason?,previousReason?}`.
  원본 `ClusterObservation`의 작은 projection이다. Pod의 ready는 원본 condition 문자열이며 boolean으로 재판정하지 않는다.
  `event.kind="cluster"`는 기록 성공 뒤에만 발행하고, 상태 변화가 없으면 앞에 빈 delta를 붙이지 않는다.

한 runtime capture의 `workloads/instances/routing/removed`와 한 판단의
`workloads/nodes/reservations/decision`은 각각 한 delta다. 동일 상태의 재발행과 동일 판단·작업 callback은 생략한다.
runtime workload readiness의 읽기 시각만 바뀐 것은 상태 변화로 세지 않는다.
- `Metric:{received,completed,failed,withinSLO,latencySamples,p50ms:number|null,p95ms:number|null,p99ms:number|null}`.
  모든 count는 0 이상의 정수다. 관측 가능한 각 UID는 무유입 초에도 0 count로 보내고,
  계측 불가 구간은 0을 만들지 않고 `gaps`로 보낸다. 의미와 분모는 아래 집계 계약을 따른다.

| 처리 | 계약 |
|---|---|
| delta | instances/actions identity별 행 전체 upsert 후 removed. workloads/nodes/routing/reservations는 키 존재 시 전체 대체, 생략 유지·빈 값 비우기. decision:null은 제거. 상태 권위는 snapshot/delta, event는 이력; 관련 delta 뒤 event, cluster는 event만. 같은 capture/판단은 한 delta, 동일 상태/callback 중복 생략. |
| identity | decision.at 원본; actionKey=JSON 튜플 `[id,node.uid,startedAt]`. 삭제 정의도 시간축 사전에 보존. ready·routable·routing·native running·waiting·actions는 독립 사실이며 AssignedUnknown은 실행 증거 아님. |
| first auth | 정확한 `/api/live`, 동일 CHILL_PUBLIC_ORIGIN(미설정 시 검증한 HTTPS Host); query/subprotocol/누락·다른 Origin 거부, 임의 forwarded 불신. 5초 내 ≤16 KiB 첫 `{type:"auth",token}`. 상류에 Bearer 변환, URL/cookie/log/영속 저장 금지. |
| 상류 권한 | Upgrade 전 TokenReview와 `get /chill/api/v1/live` SAR, 성공 전 데이터 0개. HTTP 401/403/503→4401/4403/1013, 첫 auth 오류=4401·인증 뒤 계약 오류=1002. 4401/4403 자동 재시도 없음. 전역 feed 권한을 단일 workload 권한에서 확대하지 않음. |
| 수명 | handshake 10초·socket write 5초, 일반 HTTP timeout/deadline 전 정확한 live 분기. 상류 성공 hello→resume→snapshot/재개; head capture+구독 등록은 한 잠금. 조기 resume 한 건만 보관. 양 끝 종료/logout/shutdown은 socket/구독/timer 해제. |
| resume | `{type:"resume",from,at}`; from=0은 baseline(at 생략), 나머지는 보존 `(seq,at)` 일치만 재개. 범위 밖·미래·재시작 불일치면 snapshot. hello는 checkpoint 전진 없음, baseline 뒤 중복 버림·last+1 초과는 미적용 후 재동기화. |
| 상한 | hub 최근 600메시지/8 MiB 먼저 도달, 연결 큐 602메시지/2 MiB, 프레임 1 MiB. 600개는 10분 보장 아님. 큰 snapshot은 자르지 않고 1009. metrics 먼저 drop 후 빠진 seq 뒤 delta/event보다 앞 snapshot barrier; 부족하면 snapshot 하나. write 막힘/Node bufferedAmount>2 MiB는 1013. producer 비차단. |
| 재연결·신선도 | 0.5초→최대30초 지수 backoff+jitter, baseline 뒤 정상. baseline+연결+관측≤5초만 실시간, 그 뒤 오래됨, 종료는 끊김. heartbeat 5초는 수신만 갱신, 관측 아님. 유실은 0/직선 보간 금지. |
| 집계 | UTC 1초 `[bucketAt,bucketAt+1s)`. received=receivedAt 사건률, 나머지는 response_finished.At 종결 집단. completed=성공, failed=timeout/취소/거절/오류/전달 실패 등. N=completed+failed, 성공=completed/N·SLO 안=withinSLO/N·밖=(completed-withinSLO)/N·실패=failed/N. N=0은 비율/분위수 null. |
| 지연·보정 | p50/p95/p99는 같은 종결 집단 유효 종단 지연(실패 포함), latencySamples 병기. 요청 적용 SLO/definitionID 보존; native finished/중복 final은 새 완료 아님. 무유입 정상 초는 0 count, 계측 불가는 gaps. 최근10초 늦은 callback은 원 버킷 전체 대체, 이후 gaps+REST; 원시 요청은 push하지 않음. |
| 전력 | 초 도착 실제 표본 노드별 산술평균, 이전 W 복사 금지. 전체/활성 범위 UID 중복 없이 합산, 필수 표본/소속 모르면 null. J는 coverage 검증 REST 적분, 브라우저 W 합산 금지. |
| producer | cmd/chill-server 조립, internal/serving/live hub; OnDecision은 동일 capture, OnAction은 원본 결과, RequestEvent 반영 성공 뒤 집계, 기존 power Source adapter 사용. callback은 메모리만 갱신, socket/REST/S3 대기·새 수집 루프·서버 REST polling 없음. |
| client | 탭 QueryClient의 chill-live reducer: checkpoint/snapshot/metricsByBucket/events/gaps/lastReceivedAt/lastObservedAt. 최근600초·1000사건과 시작 baseline/bounded delta; 상한 유실 명시. pause는 cursor만 바꾸고 수신 계속, live 복귀 최신 baseline. |

## 4. API 소유권과 연결 현황

Serving 경로 정본: [HTTP handler](../../../internal/serving/httpapi/handler.go), Console 정본:
[read handler](../../../internal/operatorconsole/readapi/handler.go). BFF allowlist는 `chill-dashboard/lib/relay.ts`.
아래는 `/api/v1` 하위 경로다. 기능별 지원과 호출 권한은 별도다.
| method와 `/api/v1` 하위 경로 | 현재 제공 범위 | 웹 연결과 필요한 보완 |
|---|---|---|
| `GET, POST /models`, `GET, PUT, DELETE /models/{id}` | ModelSpec 조회와 변경, 버전 전제조건 및 참조 보호. 실행 경로는 `firstfloor`와 산출물 형식별 backend만 통과한다(`internal/modelcatalog/catalog.go:107-123`). | 등록 폼과 영향 확인에 재사용한다. 실행 방식 선택지는 같은 규칙을 따른다(U2). |
| `GET, POST /preprocessors`, `GET, PUT, DELETE /preprocessors/{id}`, 같은 형태의 `/datasets` | 전처리기의 `input.fields`와 `output.fields` 계약, 데이터셋 필드, 참조 보호 | 단계형 등록과 계약 결론 문장, 입력과 결과 화면의 필드 원본이다. |
| `GET, POST /workloads`, `GET, PUT, DELETE /workloads/{id}` | desired, appliedDefinition, supportedInputs, admission, readiness와 사유, operation | 정의, 실제 준비와 접수 및 작업을 구분한다. 준비 사유는 `lib/labels.ts` 문장으로 표시한다. |
| `POST /workloads/{id}/infer` | 단일 원시 표본 입력(Preprocessor 입력 필드의 base64 `BYTES`), 실제 응답과 요청 identity | 단일 추론에 재사용한다. |
| `GET /pools`, `GET /pools/{id}` | 실제 Node와 Workload identity, 인스턴스, 준비 및 대기와 실행 수 | 자원, 모델 경로와 전력모드 및 활성 범위의 실제 관측을 추가한다. |
| `GET /observations/workloads/{uid}` | 구간, 정의 및 outcome 필터, cursor, 전체 선택 구간 cohort와 raw 이벤트 | SLO 초과, 분포, 집계 설정과 archive 조회 계약을 보완한다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence-passport` | 검증된 Run 계보와 제한된 Evidence 요약. `measurement.runtime`은 family, version, provider, architecture를 담는다(`internal/operatorconsole/evidence_passport_view.go:66-83`). | 공통 HTTPS 경계에 인증을 적용해 재사용한다. |
| `GET /namespaces/{namespace}/batch-energy-optimizations/{name}/result` | 캠페인의 봉인된 결과 | 진행 상세와 구분해 재사용한다. |
| `POST /namespaces/{namespace}/batch-energy-optimizations/preflight` | 입력을 해석하고 `resolves`, `reason`, `plan`을 반환하는 읽기 전용 요청 | 측정 요청의 검토 단계와 `/measurements/check`에 사용한다. 캠페인 접수는 별도 POST로 수행한다. |
| `GET, POST /namespaces/{namespace}/batch-energy-optimizations`, `DELETE .../{name}` | 측정 목록, `dryRun=All` 검증과 접수, `uid`와 `If-Match`의 영구 삭제 | 측정 목록, 요청과 삭제에 재사용한다. |
| `GET /scheduler`, `GET /scheduler/settings`, `GET /scheduler/state`, `PUT /scheduler/settings` | 현재 설정, 실제 snapshot과 판단, 동작과 결과, 전체 설정 적용 및 행위자 관측 기록 | 고정 BFF 경로에 연결한다. 버전 조건과 dry-run 계약은 ChillServer 조회 대기다. |
| `GET, POST /scheduler/profiles`, `DELETE /scheduler/profiles/{namespace}/{name}` | 채택 프로파일과 조건, 채택 결과와 `configVersion` | 측정 화면의 채택과 해제, `/scheduler/profiles` 목록에 재사용한다. |
| `GET /namespaces/{namespace}/operation-cost-measurements[/{name}]`, 같은 형태의 `dispatch-occupancy-measurements` | `{items:MeasurementProjection[]}` 또는 단건 안전한 projection. 원본 spec 전체가 아니다. | relay GET 허용됨. 두 측정 화면으로 연결한다. |
| `GET /scheduler/capacity` | `{rows:CapacityRow[]}`. 자동제어 워크로드가 필요로 하는 행과 입력, κ 및 탐색 판정 | relay GET 허용됨, query 없음. 용량 근거 화면과 현재 settings 대조에 연결한다. |
| `GET /observations/load` | 명시적 구간의 수신, 접수, 배정과 완료 사건 수 및 사건률 | from/to를 필수로 전달한다. 완료는 수신 cohort와 별개의 사건 집계다. |
| `GET /observations/scheduler` | 판단, snapshot, 설정 변경, action과 전력 관측의 보관 중인 append-order 페이지 | from/to, bounded limit와 cursor를 사용한다. 응답은 페이지 항목과 nextCursor로 구성된다. relay는 이 경로와 `GET /scheduler`, `/scheduler/state`, `/scheduler/profiles`를 ChillServer `view=compact`로 읽고, 화면 파싱 전에 공유된 `profile`과 `completionMeasurement` `$ref`를 응답 dictionary로 복원해 full 응답과 같은 값을 쓴다. 응답 상한은 그대로다. |
| `GET /observations/power` | 명시적 구간의 전체 및 활성 POOL joules와 평균 W, 노드별 값, `complete`, `reasons`, 로컬 보존보다 앞선 구간의 `gapUntil`(`internal/serving/httpapi/power.go:10-32`) | `/scheduler/power`의 실행 그룹 전체 전력에 연결했다(`chill-dashboard/features/scheduler.tsx:251-267`). |
| `GET /systems`, `GET /inventory` | ChillSystem의 condition과 이유, DeviceClass와 typed 전력모드 actuator, Node | System, Inventory와 Run 목록은 아래 query handler에서 제공한다. |
| `GET /experiments`, `GET /experiments/{id}` | 저장된 불변 실행·단계 envelope와 publisher별 KPI·출처. 실제 GET 목록 6개 확인 | 이미 공개된 조회다. §8.6의 설명형 비교·단위·단계 연결에 재사용하며 미게시 α/identity를 추정하지 않는다. |

### 연결 예정 API (서버 책임)

|---|---|---|
| `GET /systems`의 `capabilities` | 기능별 `{name, available, reason}` | System 조회는 구현됐다. `capabilities` 필드는 ChillServer 조회 대기다. |
| `GET /profiling/options` | 모델과 경로, 장비와 Node, 전력모드, 입력 및 측정과 관측 정책의 선택 항목과 sourceRef | 기존 선언과 configured fact resolver를 사용하는 Console 조회가 필요하다. |
| `GET, POST /namespaces/{namespace}/profiling-runs`, `GET .../profiling-runs/{name}` | 생성 입력 `{name, spec}`, Run identity와 immutable spec, 실제 status와 시각 | bounded GET 목록 구현 완료. exact detail과 인증된 create adapter는 연결 준비 중이다. |
| `GET .../batch-energy-optimizations/{name}` | immutable campaign spec, child Run 및 trial 참조, 실제 진행과 result 참조 | 목록, 접수와 삭제는 Serving에서 제공한다. 단건 진행 조회가 필요하다. |
| `GET, POST /namespaces/{namespace}/power-mode-transition-costs`, `GET .../power-mode-transition-costs/{name}` | DB-FR-32 조회와 같은 원본, 측정 요청 입력 | 기존 전환 비용 측정과 NodeActuator를 재사용한다. 조회와 create adapter가 필요하다. |
| `GET /profiles` | 조건별 성공 Run, 실행시간 분포, 처리량, 메모리, 전력과 에너지, 유휴 기준 및 trial 참조 | 채택 프로파일은 `/scheduler/profiles`가 제공한다. 조건별 전체 목록은 검증된 Run과 campaign 결과의 조회 projection이 필요하다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence/raw` | 권한 범위의 구조화된 raw, 원본 참조와 단위 | 기존 검증 경계와 봉인된 결과 reader를 재사용하는 전송 경로가 필요하다. |
| `GET, PUT /allowed-resources`, `POST /nodes/{uid}/maintenance` | 허용 Node와 실행 경로 및 전력모드, 제외 또는 복귀, version, 영향, 원본 작업 참조 | ChillServer의 자원 정책과 유지보수 관리 API가 필요하다. 적용은 CHILL의 실제 드레인과 actuator가 수행한다. |
| `GET /observations/workloads/{uid}/requests/{requestID}` | 원본 적용 정의, 단계와 실제 outcome, CHILL 종단 지연과 SLO 초과 여부 | 기존 관측 기록의 exact 요청 조회 전송 경로가 필요하다. |
| 증분 에너지와 동작별 전환 에너지 귀속 | 전체 대비 증분 에너지, 동작별 전환 소비와 누락 | 전체와 활성 POOL 집계는 `GET /observations/power`가 제공한다. 증분 에너지와 동작별 귀속은 ChillServer 조회 대기다. |
| 영역 간 변경 feed | 영역 간 행위자, 대상과 접수 및 적용 결과 | 서비스 관리 관측 기록과 scheduler settings 사건은 각각 연결했다. 영역 간 통합 feed는 ChillServer 조회 대기다. |
| `GET /exports` | 자료 종류, 대상, from과 to 및 필터로 선택한 records, 집계 조건, 단위와 sourceRefs | 해당 조회 및 archive를 사용하는 권한 검사와 비밀정보 제거가 필요하다. |

새 조회는 bounded limit/cursor 또는 offset, RFC3339Nano from/to·원본별 시각·실제 범위·누락 이유를 보존한다.
sourceRef는 kind/namespace/name/UID/resourceVersion 또는 봉인 참조다. capabilities는 `{name,available,reason}` 배열.
System/Inventory/Run 목록은 limit 1..200, offset 0..1,000,000·bounded q; Run condition은 원본 Type=True|False|Unknown.

## 5. 측정 projection과 용량 필드 계약

[측정 projection](../../../internal/serving/httpapi/measurement_projections.go)과
`internal/serving/capacity.go`, `httpapi/profiles.go:schedulerCapacity`가 wire 필드의 권위다.
두 측정 목록은 `{items:MeasurementProjection[]}`, 단건은 projection 자체, 용량은 `{rows:CapacityRow[]}`.
모두 GET·body/API query 없음; UI namespace/uid/workload/row query를 upstream에 전달하지 않는다.
측정 uid 불일치는 다른 객체로 차단, uid 없는 이름 참조는 현재 객체다. namespace별 응답만 클라이언트 정렬/페이지 처리한다.

| 공통 표시 | 정확한 소스 필드 | 표시 규칙 |
|---|---|---|
| 목록 identity·대상·상태·시각 | `metadata.name`, `metadata.namespace`, `metadata.uid`, `node`, `status.phase`, `metadata.creationTimestamp` | 이름/Node, 상태 문장·기호, UTC/KST 시각. UID는 상세 원본 identity에 표시 |
| 상세 결론·사유 | `kind`, `status.phase`, `status.reason` | 결론 아래 제공된 `status.reason` 원문 표시. `lib/labels.ts` 등록 코드는 문장 안내를 함께 표시하고 미등록 코드·자유 문장은 공통 폴백으로 대체하지 않음 |
| CR 출처 | `metadata.resourceVersion`; `references.cr.{apiVersion,kind,namespace,name,uid}` | “원본 CR 식별자”. 전체 CR 수신 주장·Kubernetes 직접 링크 없음 |
| 근거 위치 | `references.evidence[].{store,key}` | 논리 위치 복사/JSON 내보내기. raw HTTP URL·Passport 경로 생성 없음 |

| 표시 | 소스 필드 | 단위·연결 |
|---|---|---|
| 단계 이름·종류 | `conditions.steps[].name`, `.kind` | power=전력모드 변경, prepare=준비, idle=유휴, remove=제거 |
| 단계별 조건 | `.powerMode`, `.batchEnergyOptimization`, `.idleDurationSeconds` | 해당 kind 필드만 표시. 유휴=s; BEO 참조는 같은 namespace의 기존 BEO 상세 링크 |
| 저장 결과 | `status.steps[].name`, `.key`, `.verdict` | name으로 조건과 연결, 없는 단계는 “아직 기록된 결과가 없어요”. status만의 미매칭 단계도 “조건 연결 없음”으로 보존 |
| 근거 위치 | `references.evidence[]` | key가 같은 위치만 단계에 연결하고 그 위치에서 key를 한 번 표시. 연결된 위치가 없을 때만 결과 key와 “위치 없음”을 표시; 원문 key·verdict도 원본에 보존 |

| 표시 | 소스 필드 | 단위·누락 규칙 |
|---|---|---|
| 워크로드·측정 프로파일 참조 | `conditions.workload`, `conditions.batchEnergyOptimization` | 각각 기존 workload 상세, 같은 ns BEO 상세 연결. UID 없는 참조는 현재 이름 기준임을 명시 |
| 배치·공급 방식·동시 송신자 | `conditions.batch`, `.supplyMode`, `.workers` | 건/batch, closed-loop=고정 동시 요청, poisson=포아송 공급, 미제공=“기록되지 않았어요”; workers=명 |
| 예열·측정 구간 | `conditions.warmupSeconds`, `.durationSeconds` | s, 예열은 측정 시작에서 제외 |
| 결과 전력모드 | `status.powerMode` | 없으면 “결과 없음” |
| 요청당 분배 점유시간 d | `status.perRequestNanos` | 목록·상세 모두 기존 `nanosToMilliseconds` 변환의 ms/request를 소수 둘째 자리까지 반올림해 주표기(예: `3.81ms/request`), 정확한 ns/request 원값을 바로 아래 보조 표기. 누락=“결과 없음”; 0=`0ms/request`와 `0 ns/request`. 원본 응답·export의 정밀 ns 정수는 변경하지 않음 |
| 지속 완료 처리량 X | `status.throughput` | 문자열 원값 + req/s, 누락/빈 문자열=“결과 없음”. 브라우저에서 d나 κ 재계산 없음 |
| 결과 위치 | `status.result.{store,key}`, `references.evidence[]` | 논리 위치 그대로, 중복은 한 번 표시; 빈 evidence=“아직 저장된 근거 위치가 없어요” |

| 구역 | 정확한 필드 | 표시 단위·의미 |
|---|---|---|
| 결과 | `key`, `status`, `reason`, `mu`, `kappa`, `minimumServiceNanos`, `builtAt`, `elapsedNanos`, `previous` | μ·κ=req/s(μ는 처리량 상한, κ는 계산 용량); minimumServiceNanos=ns; elapsedNanos=ns와 보조 ms; builtAt=UTC/KST, zero 시각은 “계산 시각 없음”. optional 누락은 “값 없음” |
| 대상·SLO | `input.workloadUID`, `input.slo.percentile`, `input.slo.targetLatencyNanos`, `input.node.{name,uid}`, `input.powerMode`, `input.profileID` | percentile=백분위 p(0~100), 목표=ns와 보조 ms, identity 보존 |
| 실행 자원 | `input.allocation.{cpus,gpuUUID,memoryBytes,threads,cpuCount,cpuReserved}`, `input.maxBatch` | cpus=코어 ID 목록, GPU UUID, memoryBytes=bytes, threads/cpuCount=개, cpuReserved=예약 여부, maxBatch=건/batch; null/빈 목록 보존 |
| 완료 여유·계산 구간 | `input.completionMarginMS`, `input.capacityWindowNanos`, `input.capacityWindows`, `input.capacityPassingWindows`, `input.capacityMinimumRequests` | ms, ns(보조 s), 창 개수, 필요한 합격 창 개수, 창당 최소 요청 수 |
| 탐색 입력 | `input.capacityGridSteps`, `input.capacityBisectionWidth`, `input.capacityTailSamples` | 격자 분할 수; μ에 대한 이분탐색 폭 비율(무차원); 백분위 위 꼬리 목표 표본 수(개). 달성 정밀도로 표현하지 않음 |
| d와 출처 | `input.dispatchOccupancy.{node.name,node.uid,powerMode,runtimeImage,model,perRequestNanos,source}` | d=ns/request, source는 측정 기록 식별 문자열 그대로. 누락=“분배 점유시간 근거가 없어요”. namespace/name 추정 파싱·링크 생성 없음 |
| 탐색 판정 표 | `grid[]`, `bisection[]` 각각의 `rate`, `requests`, `passed`, `met`, `percentileMS[]` | 실행 순번, req/s, **창당** 요청 수, 합격 창 수, 서버의 충족/미충족, 검사한 창별 SLO 백분위 지연(ms, 완료 여유 포함) |

| 해석 | 보존 규칙 |
|---|---|
| 동작 비용 | power/prepare/idle/remove의 계획·기록 단계 수일 뿐 채택률 아님. name으로 연결하되 미매칭도 보존, evidence는 key 일치만 연결. Accepted만 채택, Complete로 재판정 금지. projection에 시간/J/모드 쌍/반복 사유 없음. |
| d | ns 정밀 정수 보존, 주표기 ms/request 소수 둘째 자리·원본 ns 병기. 누락≠0, X는 문자열 req/s 원값. d·κ 브라우저 재계산 금지. 공급 방식 미지정과 결과 없음 구분. |
| κ | 현재 자동제어 계산 대상이며 역사/실제 유입/SLO 실측 아님. key 고정, 삭제된 key 최신 fallback 금지. grid/bisection 응답 순서 보존; requests=창당 수, passed=합격 창 수, percentileMS=-1은 미충족/지연값 없음. 조기 종료·빈 탐색 정상. |
| settings 대조 | 행 input과 별도 settings 최상위 completionMarginMS/capacityWindowNanos/capacityWindows/capacityPassingWindows/capacityMinimumRequests/capacityGridSteps/capacityBisectionWidth/capacityTailSamples를 정밀 비교. 행 값/현재 값/같음·다름 및 두 조회 시각; draft는 적용값 아님. settings 실패가 행을 지우지 않음. |
| 상태·출처 | status.reason 원문 보존(없는 message 추정 금지). CapacityPending의 누락 κ는 0 아님, Built κ=0은 실제0, previous는 재계산 중 이전 결과. Measuring/Pending/previous만 보이는 동안 갱신, terminal/미확인은 수동. export는 projection·identity·필터·조회 시각이며 전체 CR/raw Evidence 아님. |
| 전환 비용 옛 링크 | power-mode-transition-costs 직접 링크는 별도 API 미제공 안내와 측정 목록 이동만 제공. 원래 identity를 operation-cost API로 보내거나 자동 redirect하지 않음. |
| 판단 근거 | 선택 decision.at의 `[at,at+1ns)` journal을 페이지 탐색해 원본 시각 일치 확인; 없으면 기록 없음. 최신 scheduler/state로 덮지 않음. load arrivals 생략 시 UID/원래 구간/전체 적용 정의의 요청 관측 사용. |
| 이력·비교 | history 종류/power Node 필터는 받은 페이지에만 적용, cursor는 제출 원래 from/to 보존. append offset/limit=1 복원은 kind/at/대상/action node·시작 일치 필요. A/B는 각 입력 구간·미래 horizon과 UID/정의 whole-window cohort 구분. |
| 재생 분모 | T4 immutable summary·stages·sources·coverage(local/archive/gaps) 사용. 원시 수신 cohort를 UID/requestID 중복 제거·final 우선, receivedAt+elapsedNanos ns 종결 초로 재집계(재생만). exact definitionID SLO, 미확인/이전 구간은 공백. 단계 KPI를 초별 보간하지 않음; 공유 POOL J 행 합산 금지. |

## 6. SRS 추적성

ID 정본은 Dashboard SRS다. U는 기존 구현 단위, T는 라이브/재생 통합, G는 서버 조회 선행 단위이며 완료 판정이 아니다.

| 요구사항 | 구현 위치와 API | 작업 단위 | 수용 시나리오 |
|---|---|---|---|
| DB-SEC-01, DB-SEC-03 | 이 문서의 bearer 전달, TLS, credential 제거 | 기존 | 권한과 만료, 내보내기와 이력 |
| DB-SEC-02, DB-SEC-04 | 이 문서. BFF는 설정된 ChillServer origin 하나로 중계하고 ChillServer가 신원을 검증한다(`lib/server-config.ts`, `lib/relay.ts`). | U6 | 권한과 만료 |
| DB-INT-01~02 | 현재 아키텍처의 고정 Git, 독립 모듈과 배포 | 기존 | 독립 모듈과 템플릿 |
| DB-INT-03 | API 원본 DTO와 원본 식별자 보존 | U6 | 데이터와 API 공백 |
| DB-INT-04 | 도메인 writer와 If-Match | 기존 | POOL과 유지보수 |
| DB-INT-05 | API 연결 현황과 남은 조회, 독립 Query의 영역별 미지원/공백 표시 | D0, T6, G1~G3 | 데이터와 API 공백, 시스템 개요 |
| DB-INT-06 | 이 문서의 영역별 상태와 마지막 관측 시각 | 기존 | 데이터와 API 공백 |
| DB-UX-01~02, DB-UX-04 | 단계형 폼, 필드 오류, preview와 원본 identity | 기존 | 안내형 등록, 충돌과 연결 단절 |
| DB-UX-03 | 이 문서의 목록 행 직접 이동과 조회 범위 문장 | D0 | 안내형 등록, 데이터와 API 공백 |
| DB-FR-01 | System, Inventory, Workload 상태 구분 | 기존 | POOL과 유지보수, 시스템 개요 |
| DB-FR-02 | `/resources` 장비 Dialog와 `/measurements/new` 전력모드 단계의 같은 제어 방식 문장, `GET /inventory` typed actuator | U0, U3 | 장비 전력모드 |
| DB-FR-03 | `/` 개요의 독립 요약 행: 구성요소별 condition 상태, 이유와 시각(`GET /systems`, `ComponentsDisabled` 별도 문장), 서비스별 SLO와 실제 요청 결과(`GET /workloads`, `GET /observations/workloads/{uid}`), 부하와 큐(`GET /observations/load`, `GET /scheduler`), 전체 및 활성 POOL W와 J, 구간과 관측 공백(`GET /observations/power`), 진행 작업 | U1, U5 | 시스템 개요, 에너지와 실험 |
| DB-FR-04 | 모델 실행 방식 단계와 파일 화면의 사용자 용어, `internal/modelcatalog/catalog.go:107-123`과 같은 선택 규칙 | U2, U1 | 안내형 등록, 공유 모델 변경 |
| DB-FR-05, DB-FR-07 | 기존 Model과 Workload 관리 API, operation, 영향 및 참조 보호 | 기존 | 안내형 등록, 처리 중 수정과 삭제, 공유 모델 변경 |
| DB-FR-06 | `/workloads/[name]`의 준비 사유 문장과 다음 행동, 이 문서 표 | U1, U2 | 근거 부족, 오류 안내 |
| DB-FR-28 | 전처리기와 데이터셋 계약 결론 문장의 필드 이름 | U2 | 전처리기와 데이터셋 관리, 계약 기반 입력과 결과 |
| DB-FR-08 | `/workloads/[name]/infer`의 단일 입력 제출 | U2 | 모델 공유와 추론, 계약 기반 입력과 결과 |
| DB-FR-09 | 기존 infer와 exact 요청 관측, timeout 및 지연 경계 | 기존 | 모델 공유와 추론 |
| DB-FR-29 | `/workloads/[name]/infer`의 계약 기반 입력과 결과, 이 문서 | U2 | 계약 기반 입력과 결과 |
| DB-FR-10 | POOL 실제 projection과 Inventory | 기존 | POOL과 유지보수 |
| DB-FR-11 | 이 문서의 허용 자원과 유지보수 계약(ChillServer 관리 API 대기) | D0 | POOL과 유지보수 |
| DB-FR-12 | `/measurements/new` 검토 단계와 `/measurements/check`의 preflight 결론과 계획 요약, 전력모드 문장 | U0, U3 | 측정과 실패, 장비 전력모드 |
| DB-FR-13 | 측정 상세의 결과와 실패 문장 및 다음 행동, 측정 회차의 Passport 제공 필드(family, version, provider, architecture). 실제 backend와 실행 장치는 이 문서 조회 이후 | U1, U3, G2 | 측정과 실패 |
| DB-FR-14 | `/scheduler/profiles`와 후보 상세의 측정 조건 네 행, 조건이 다른 프로파일 구분 | U0, U4 | 프로파일 조건 |
| DB-FR-30 | `/workloads/[name]/coverage`, 측정 요청 장비 단계의 사유 | G1 | 측정 가능 범위 |
| DB-FR-32 | `/measurements/transition-costs`와 상세, 후보 전환 비용 연결 | G3 | 전환 비용 원본 |
| DB-FR-15 | 이 문서 현재 판단 띠와 실제/목표/후보 구분 | T6 | 구성과 설정 변경 |
| DB-FR-16 | 이 문서 선택 당시 근거와 예측/실측 구분 | T6 | 구성과 설정 변경, 전환 비용 원본 |
| DB-FR-17 | `/configuration`의 SchedulerSettings 편집, 예측 영향은 ChillServer 조회 대기 | U4 | 구성과 설정 변경 |
| DB-FR-18 | 이 문서 근거의 선언/실제 분배, T2 scheduler-api의 route/unroute/release/reset 기록. 예전 `manual-static-routing` 이력은 원본 보기로 유지하며 새 설정 쓰기로 재사용하지 않는다. | T2/T6 | 설정 기반 요청 분배 |
| DB-FR-19~21 | 이 문서 시간축의 계획/실제 작업, 의존관계·실패·부분 적용과 선택 근거 | T6 | 전환 일부 실패 |
| DB-FR-22 | UID 관측 기록과 단계별 요청 항목 | 기존 | 빈 구간과 집계 |
| DB-FR-23 | `/observations`의 전체 구간 cohort, 표본 0의 0개와 빈 통계 값(기존 화면 유지) | D0 | 빈 구간과 집계 |
| DB-FR-24 | `/scheduler/power`의 개별 기록과 `GET /observations/power` 전체 및 활성 POOL 집계. 증분 에너지는 ChillServer 조회 대기 | 기존 | 에너지와 실험 |
| DB-FR-25 | `/scheduler/compare`의 A/B 조건, 예측과 같은 구간 실제 cohort | 기존 | 에너지와 실험 |
| DB-FR-26~27 | 선택 기록과 현재 페이지 export, 설정 변경의 검증 actor | 기존 | 내보내기와 이력 |
| DB-OP-01~04 | 영향 확인, If-Match, 원본 작업 조회, 제출 잠금과 명시적 재제출 | 기존 | 처리 중 수정과 삭제, 공유 모델 변경, 충돌과 연결 단절 |
| DB-OP-05 | 이 문서 표, 공통 오류 영역, 관리 변경과 추론 오류의 의미와 다음 행동 | U1, U2 | 오류 안내, 권한과 만료 |
| DB-NFR-01~06 | 접근성, 값 보존, bounded 조회, 성능 예산, 독립 배포와 안전한 렌더링 | 기존 | 접근성과 화면 안전, 조회 부하와 재시작 |
| SCH-37, DB-INT-07~08 | 라이브 계약의 first auth→Bearer, producer, snapshot/delta/seq/집계 | T5 | 라이브 인증과 재동기화 |
| DB-NFR-07 | 라이브 계약의 gap/마지막 수신·관측/신선도 | T5/T6 | 라이브 인증과 재동기화 |
| DB-FR-35~39, DB-FR-50~52 | 흐름 보드·POOL 계층·시간축·선택 당시 근거; 세부 기준은 SRS | T6 | 라이브 무대의 연속 흐름 |
| DB-FR-40~44 | 이 문서 재생·분모·비교·정의·불변 결과와 archive | T4/T6 | 재생 결과의 모집단과 에너지 |
| DB-FR-45~49 | 이 문서 운영자 측정 근거와 폐기 route | T4/T6 | 운영자 조회와 폐기 화면 |
| DB-FR-53~54 | 이 문서 동작 비용/분배 점유시간 목록·상세, 실제 투영과 UID·출처 보존 | API coverage 계획 | 동작 비용의 실제 API와 원본 identity, 분배 점유시간의 조건과 누락 결과 |
| DB-FR-55 | 이 문서 `/scheduler/capacity`의 전체 입력·판정과 독립 settings 대조 | API coverage 계획 | 용량 입력과 계산 판정 및 현재 설정 대조 |
| DB-FR-56~60 | §8.2~8.4의 선언 후보 행렬·목록·조건별 회차와 최소 요청 prefill, 기존 GET 조인 | S1 | 선언 후보 커버리지와 조회 범위, 조건 목록과 회차 연결, 미리 채운 요청과 중복 안내 |
| DB-FR-61~63 | §8.5의 숫자·실패/근거 분리·d 포맷과 비용 미제공 안내 | S2 | 상세 수치의 범위와 품질, 실패 회차와 후속 측정 |
| DB-FR-64~65 | §8.6의 게시 shape·동명 설명형 비교·단계 레인 | S3 | 실험의 결론·동명 비교와 단계 |
| DB-UX-05~06 | §8.1~8.2·8.7의 data Screen·tabular 숫자·breadcrumb/returnTo | S2(공용), S1/S3(적용) | 데이터 폭·탐색과 재설문 |

## 7. 남은 연결과 수용 경계

| 항목 | 현재 공백·다음 근거 |
|---|---|
| DB-FR-13 / G2 | Passport는 family/version/provider/architecture 제공. 실제 backend는 검증된 RuntimeResult.Backend, 실행 장치는 firstfloor RuntimeMetadata의 device/providers/gpuUUID/rknn 투영 대기. candidate.executionPath 이름으로 추정 금지. |
| DB-FR-30 / G1 | 실행 경로×DeviceClass Compatible/Incompatible/Unknown·사유는 internal/modelcompat 내부; Inventory 선언과 별개인 조회 필요. |
| DB-FR-32 / G3 | PowerModeTransitionCost 원본의 모드 쌍·방향별 반복 시간/J·판정/사유·관측 구간·경계 불확실성·Serving 지속·참조 API 대기. OperationCostMeasurement는 대체 불가. |
| 라이브/재생 | T5/T6 코드 연결은 승인 배포·실제 WS 수용 완료가 아님. 재생 API/원본 계약은 [T4 관측 설계](../serving/observability-split.md); 제품과 고정 fixture를 구분. fixture는 CHILL_DEMO_REPLAY=1에서만, 인증 우회·upstream·변경·추론 금지. |
| 실제 수용 | qualifying native campaign 이후 Ready 추론 성공, ARM/RKNN hardware, 요청 drain·운영 전력·selector·전환/writer·전체 SRS 시나리오·성능 예산은 전체 완료 근거가 필요. |
| 실제 조회 장애 | scheduler-dashboard-8c7cbec 배포 시 load/journal BFF 504, 직접 1분/limit=1 journal도 30초 무응답·S3 PutObject deadline. 과거 기록/A/B/전력 정상 조회 미검증; timeout/타 컴포넌트 설정 변경 없음. 실제 작업은 GET 한정, 설정 PUT·제어 활성화·인위 전환·전력 실험 아님. |

## 8. 측정·실험 UX와 커버리지 (2026-10-30)

상태는 **코드 연결·격리 GET 확인 완료, 배포·재설문 전**이다. [계획](../../plans/2026-10-30-dashboard-measurement-ux.md)의
설문·GET 관측과 [SRS](../../spec/dashboard/SRS.md) DB-FR-56~65, DB-UX-05~06을 구현한다.
이번 변경은 측정·실험 영역과 공용 데이터 폭만 다룬다. 홈의 라이브 보드·사이드바·로그인 정체성은 유지한다.

### 8.1 시각 방향: 계획 → 브리프 재검토 → 확정

**1차 계획.** CHILL을 ‘연구 계측표’로 읽히게 한다. 첫 화면의 주인공은 큰 숫자 카드가 아니라
조합의 빈칸과 비교 열이다. 차원명은 왼쪽, 수치/단위는 오른쪽, 상태 기호는 일정 위치에 놓는다.
8/12/16/24/32px 간격과 기존 테두리·모서리를 재사용하며 동일 카드 88개를 반복하지 않는다.

**브리프 대비 재검토.** uxexpert §8.2의 ice/petrol/ink와 master-detail 방향은 채택한다.
그러나 제안의 새 petrol `#0D5C75`, amber `#D97706`, crimson `#C81E1E`로 기존 토큰을
덮지 않는다. `globals.css:55-98`의 실제 토큰이 정본이다. 매 화면에 어두운 KPI 배너를 붙이는 것도
또 다른 템플릿이므로 버렸다. 라이트 화면은 조용한 표와 한 줄 결론, 다크 모드는 기존 `.dark` 대응값을 쓴다.
‘모든 숫자는 monospace’ 제안도 수정했다. 기존 Asta Sans를 유지하고 수치에만 tabular-nums를 적용한다.
Fira/JetBrains/Pretendard를 새로 가져오지 않는다. 전치 토글은 판단을 어렵게 하므로 행=모델→경로로 고정한다.

**확정 팔레트(6색, 새 전역 테마 아님).**

| 이름 | 기존 hex / 토큰 | 용도 |
|---|---|---|
| Ice canvas | `#E3EAF0` / sidebar | 기존 크롬과 화면의 연결. 본문 배경은 기존 background, 표는 기존 card를 그대로 사용 |
| Instrument ink | `#0F2230` / foreground | 제목·결론·주요 수치 |
| Coolant petrol | `#0D6A8C` / primary, chart-1 | 선택 셀·focus·요청 CTA·A 계열 |
| Verified green | `#17784A` / ok | 근거가 채택된 완료 상태만. 실행 phase만으로 녹색 금지 |
| Calibration amber | `#B5530B` / heat, chart-4 | 품질 경고. B 시리즈는 점선/명시 이름도 병기해 경고와 혼동 방지 |
| Fault red | `#B42318` / fail, destructive | 실행 실패. 원문 오류와 연결 |

색은 컴포넌트에 hex로 복사하지 않고 semantic 변수로 참조한다. 보조면은 기존 ok/heat/fail/info/idle-soft,
흰색 card와 border를 사용한다. 미지원은 사선+‘지원 안 함’, 미측정은 점선+빈 원,
미확인은 물음표+문구로 구별한다. Lucide 아이콘은 장식 처리하고 접근 가능한 상태 이름을 제공한다.
대비는 일반 텍스트 4.5:1, focus는 3:1 이상을 수용 시 확인하며 색만으로 판정하지 않는다.

타입: Asta Sans 제목 26px/700/1.3, 소제목 18px/600/1.4, 본문 16px/1.6,
표 14px/1.5, 보조 12~13px. **모든 수치 셀·KPI·날짜·배치에 `tabular-nums`**와
`font-variant-numeric: tabular-nums`를 명시한다. body의 기존 `"tnum"` 선언만으로 모든 상속을 가정하지 않는다.
monospace는 원본 ID/JSON에만 쓴다. 설명문은 65~80자 이내 폭으로 제한하되 데이터 표는 제한하지 않는다.

도구 근거(2026-10-30, 설치된 ui-ux-pro-max `scripts/search.py` 실행; 아래 채택 규칙만 적용):

| 질의 | 채택한 규칙 한 줄 |
|---|---|
| `"research data dashboard" --design-system --density 8` | Data-Dense Dashboard의 높은 정보 밀도·필터 우선·불필요한 장식 회피를 채택 |
| `"coverage matrix heatmap" --domain chart` | 색 외에 셀 값·기호·패턴을 제공하고 읽을 수 있는 표를 기본으로 사용 |
| `"comparison table" --domain chart` | 정확 수치 비교는 가시적인 정렬 가능 표와 짧은 비교 요약을 함께 제공 |
| `"color blind status" --domain ux` | 상태는 아이콘+문구, 일반 텍스트 최소 4.5:1 대비 |
| `"breadcrumb back navigation" --domain ux` | 브라우저 이력과 예측 가능한 뒤로가기, 키보드 순서·focus 유지 |
| `"shadcn table sticky header" --stack shadcn` | TableHeader/Body/Row/Head/Cell의 의미론 구조와 기존 TanStack Table 패턴 재사용 |

design-system 결과의 Enterprise Gateway/Contact Sales는 업무와 맞지 않아 폐기했다.
`"research analytics dashboard" --design-system --density 8`로 한 번 좁혀도 같은 패턴이어서
랜딩 패턴·신규 색·폰트는 미채택이다. sticky header 자체는 검색 결과의 보장이 아니라 이 설계의 CSS 결정이다.
frontend-design의 두 단계 검토 결과, 동일 카드·그라데이션·장식용 번호 대신 **공백이 보이는 행렬 한 가지**에 시각적 강점을 집중한다.

### 8.2 화면과 넓은 레이아웃

공용 `Screen`에 `width="data"`(최대 80rem, 가용 폭 100%, min-width:0),
`actionPlacement="header"`와 optional `breadcrumbs`를 추가한다.
기존 호출의 기본값은 narrow/기존 동작을 유지하되, sticky CTA가 있는 화면에는 마지막 내용과 focus가
가려지지 않도록 footer가 흐름 공간을 차지하게 한다. 데이터 화면은 CTA를 상단에 놓아 하단 overlay를 없앤다.
본문이 좁은 폼·추론 화면은 전면 확장하지 않는다.

1440px에서는 sidebar를 뺀 본문 폭을 사용하고, 1280px 이상에서 행렬과 20~24rem 우측 패널을 나란히 놓는다.
그 이하에서는 패널을 같은 페이지 Dialog/Sheet로 열고 닫으면 셀에 focus를 돌린다.
390px 개요는 장치 종류 하나를, 상세는 실행 경로 하나를 선택해 읽는다. 모든 전력모드 행은 유지한다.
고정 폭 거대 표 대신 가용 폭에 맞춘 table-fixed·줄바꿈과 명시적인 닫기 버튼을 둔다.
페이지 전체 가로 넘침이나 `overflow:hidden`으로 마지막 열을 숨기지 않는다.

```text
측정 > 커버리지                                  [새 측정 요청]
[커버리지] [요청 목록] [프로파일] [개별 회차] [동작 비용] [분배 점유]
전체 조회 완료 전 로딩 / 완료 C / 유효 후보 N / 7상태 수
모델 [ ] 장치 [ ] 경로 [ ] 모드 [ ] 상태 [ ] [지원 안 함도 표시]
개요: 모델       | 장치 A: 완료/후보, 실패 | 장치 B: 완료/후보, 실패
      MobileNet  | 3/12                  | 2/6
선택 상세:
전력모드 | 실행 경로 CPU        | 실행 경로 CUDA        | 선택 조건 패널
MAXN     | 완료, 최저 J/배치    | 완료, 최저 J/배치     | Node/정확 조건
15W      | 품질 경고            | 미측정, 조건 요청     | CI/지연/회차/시도
상태 범례 / 선언 기준과 읽은 범위 / 마지막 조회 / 원본
```

```text
측정 > 요청 목록                                 [새 측정 요청]
커버리지 / 종류별 탐색 (목록 아래에 숨기지 않음)
모델[ ] 장치[ ] Node[ ] 모드[ ] 경로[ ] 상태[ ] 날짜[ ] 이름 검색[ ]
모델 | 장치 종류 | Node | 전력모드 | 실행 경로 | 상태/후속 결과 | 날짜
... 요청 한 건 한 행, 25행씩 ...                [이전] [다음]

측정 > 요청 목록 > 선택 측정
모델 / 장치 종류 / Node / 모드 / 경로 / CPU       [조건 복사해 새 요청]
실행 상태와 증거 상태 + 한 줄 결론 (단일 배치면 비교 없음)
배치 | 증분 J/건 | 오차 제공 여부 | p95/회차 범위 | 유효 반복 | 근거
배치별 에너지 곡선 + 정확 수치 표                 조건/품질/원본
실패·제외 회차 / 후속 유효 결과 / 실제 원본 참조
```

```text
기록 > 실험 목록: 이름 / 날짜 / 연구 질문 / 비교 정책 / 첫 대응 단계의 관측 차이
캠페인 선택 → CHILL(full-E5-*) ↔ 정적 비교군(static-E5-*) 자동 대응 단계
결론: 서비스별 기한 내 성공률 / 성공 응답 p95 / 공유 POOL J와 차이
조건 검토: 측정 규칙·구간 길이 차이와 미기록 항목
단계: 실제 시간 비례 구간 막대 + 번호·전체 단계명·기간·평균 req/s 목록
같은 단계 번호 축: 평균 req/s / 공유 에너지 막대 / 서비스별 성공률·p95 점
수치·분모·출처 자세히 [기본 접힘] / 당시 판단 근거 [기본 접힘]
```

목록에서 한 번 선택하면 대응 단계와 요약이 열린다. 직접 A/B 비교는 보조 펼침이다.
단계 클릭은 URL·선택 결과와 요약 제목 focus를 즉시 갱신하며, 목록으로 이동은 상단에 유지한다.
원시 필드·식별 정보·세부 계획은 기본 접힘이고 모바일에서도 전체 단계명과 번호를 읽는다.

### 8.3 커버리지 데이터와 셀 판정

**명칭과 축.** URL은 `/measurements/coverage`, 제목은 ‘측정 커버리지’,
설명은 ‘모델과 장치를 고르면 전력모드별 측정 현황을 볼 수 있어요.’다.
개요는 모델(행) × 장치 종류(열), 셀은 완료/유효 후보와 실패 수다. 클릭하면
해당 모델×장치의 전력모드(행) × 실행 경로(열)를 연다. 상세 셀 키는 `(model UID, path name, class UID, powerMode name)`이다.
모드 없는 class에 가짜 fixed를 만들지 않는다. 삭제된 모델/class의 과거 시도는 ‘현재 선언 밖 이력’으로 별도 보존한다.

전체 선언 후보 수는 `Σmodel |executionPaths| × Σclass |powerModes|`다. 유효 후보 분모는
명시적 지원 불일치를 제외하며 실행 지원 확정이나 연구 목표 완료율을 뜻하지 않는다.
지원 안 함은 기본 숨김(토글), 집계는 필터 범위의 7상태를 구분한다.
측정 홈 상단은 동일 분모의 커버리지 %와 최근 실험 결론 진입을 제공하며 홈에는 이 요약 링크만 둔다.
1440px에서 두 표는 가로 스크롤 없이, 모바일 개요는 장치 선택으로 열을 줄인다.
UI UX Pro Max `Loading Indicators` 권고를 채택: inventory·요청·프로파일·전체 회차가 모두 성공적으로
조회될 때까지 `aria-busy`와 고정 높이 안내만 표시하고 중간 상태/카운터는 노출하지 않는다.

| 원본 GET (`/api/v1` 이하) | 조인 필드와 역할 | 범위/오류 규칙 |
|---|---|---|
| `/inventory` | `models[].{name,uid,spec}`, `classes[].{name,uid,architecture,accelerator,powerModes,nodes}` | classPage/modelPage 각각 nextOffset 끝까지, UID로 중복 제거. resourceVersion 세 종류와 readAt 보존 |
| `/namespaces/{ns}/batch-energy-optimizations` | `metadata.{namespace,name,uid,creationTimestamp}`, `spec.state.candidate.{modelSpec,deviceClass,executionPath,powerMode}`, `.targetNode`, CPU/protocol, `status.{phase,outcome,conditions,result}` | namespace 명시. 목록에 있는 요청의 phase이지 채택 품질이 아님 |
| `/scheduler/profiles` | `profiles[].condition.{logicalModel,deviceClass,nodeUID,powerMode,artifact.executionPath,...}`, `id`, `reference`, `batches`, `measuredAt`, `reuse` | 현재 사용 가능한 증거. profile id 문자열을 추측 파싱해 요청 UID를 만들지 않음 |
| 같은 응답 `insufficiencies[]` | `source.{Namespace,Name}`, `code`, `reason`를 BEO namespace/name과 연결 | `ReplacementSelection`은 대체된 이력이며 다른 유효 profile을 무효화하지 않음. 연결 불가 issue는 ‘연결 안 된 경고’로 보존 |
| `/namespaces/{ns}/profiling-runs` | `spec.candidate`, `targetNode`, `measurement.{batch,cpuQuota,cpuAffinity,workloadMode,dataset}`, `status.conditions`, `status.evidence` | 시작 시 page 끝까지 모으고 집계. 선택 패널 재마운트만으로 다시 조회하지 않음. 수동 새로 고침은 전체 재조회 |

profile 정확 variant는 `condition`에서 `name`과 `plannedBatchSizes`만 뺀 frozen 조건과 metricSource다
(`internal/serving/kube/catalog.go:200-230`). Node UID·runtime image·artifact·preprocessor·dataset
UID·CPU/affinity·protocol·runtime backend가 다르면 합쳐 정상 완료 하나로 만들지 않는다.
BEO 요청 spec만으로 확인할 수 없는 frozen 항목은 ‘조건 일부 미확인’으로 둔다.
같은 class의 미측정 Node를 다른 Node profile로 채우지 않는다. `reuse`로 서버가 검증한 대상 Node만 재사용으로 표시한다.
Node 필터가 없으면 class의 현재 Node 각각을 평가해 ‘완료 Node C/N’을 표시한다.
일부 Node만 완료면 대표는 품질 경고(일부 측정)이고, 다른 Node를 완료로 칠하지 않는다.
CPU·dataset·이미지의 아직 요청되지 않은 무한 조합을 가상 분모로 만들지 않는다.
여기서 variant 집합은 실제 BEO/Run/profile에 나타난 조건이며, 후보 셀 수와 variant 수는 별도다.

**정적 지원 판정은 증거 상태와 분리한다.** 선택 셀의 model/path/class/Node에 한정해
`GET /model-compatibility`를 Node별 직렬 조회하고 전력모드 사이 Query cache를 공유한다.
`Compatible`·`Incompatible`·`Unknown`은 선택 패널의 현재 지원 확인으로만 표시한다.
선택 때문에 같은 경로의 다른 전력모드 상태가 변하던 원인은 지원 조회 결과를 행렬 판정에
다시 주입한 것이었다. 행렬은 선택과 독립된 선언/측정 목록으로 계산한다.
현재 inventory UID와 응답 입력 UID/selection이 다르면 연결하지 않는다. 조회 시각은 따로 표시한다.
실제 실행·Ready·전력모드 적격성·측정 완료·admission 성공을 보장하지 않는다.
미조회/구형 서버에서는 artifact/class의 명시 architecture 비교와 ‘선언상 후보 · 실제 runtime
지원 미확인’을 유지한다. accelerator/backend 이름별 브라우저 추측표는 제거했다.
현재 정적 지원과 저장된 실행/근거 상태는 독립이며 Unknown/조회 오류가 이미 확인된 실패나
완료를 덮지 않는다. 최종 측정 요청은 기존 dry-run/preflight를 그대로 통과해야 한다.

**대표 상태는 아래 순서대로 계산하며 보조 사실을 지우지 않는다.**
먼저 필수 목록 조회의 완전성과 identity 모순을 확인한다. 정적 불일치인데 해당 셀에 현재 증거가 있으면
‘품질 경고 — 현재 정적 선언과 저장된 측정 근거가 달라요’로 각각 확인하게 한다.
이후 exact variant별 상태를 만들고 상위 셀은 다음 규칙으로 요약한다.

| 셀 상태 | 구체적인 판정 규칙 | 셀/패널에 남길 보조 사실 |
|---|---|---|
| **확인 불가** | 필요한 inventory/BEO/profiles 조회 실패·권한 거부·일부 page, identity 미확인, 선언 모순 또는 정적 판정 입력 불명 | 실패 API/조회 범위·readAt, 알려진 결과는 별도 표시. 403을 미측정/지원 안 함으로 바꾸지 않음 |
| **지원 안 함** | artifact/class의 명시 architecture 불일치이고 측정 이력이 없음 | 사유와 양쪽 선언값, 해칭. 요청 CTA 비활성. 서버의 선택 조건 지원 판정은 패널에서 별도로 표시 |
| **품질 경고** | 사용 가능한 variant와 경고/미완료 variant가 섞임, Complete인데 연결 profile 없음, source insufficiency, 완료 Run의 검증·근거 누락, 실제 조회한 근거의 오류/404·수치 이상 | 실제로 조회한 해당 셀만 경고 cache에 기록해 개요에도 반영. 지원 확인 기능의 404는 측정 품질과 분리 |
| **측정 완료** | 현재 catalog가 exact variant의 profile을 제공하고 그 범위에 알려진 미해결 경고가 없음 | 에너지 측정 완료이며 CI·지연·반복·계측 품질의 논문용 검토는 별도 |
| **진행 중** | 유효 profile/품질 경고가 없고 BEO `phase=Measuring` 또는 현재 generation Run `ExecutionComplete=Unknown` 등 명시된 비종결 진행 상태 | 이전 실패/대기 사유. 알 수 없는 phase를 진행으로 추측하지 않음 |
| **실패** | 위 상태가 없고 BEO `phase=Failed` 또는 현재 Run `ExecutionComplete=False`가 명시됨 | reason/message와 후속 결과 링크. 실행 성공 뒤 검증 불충분은 품질 경고로 분리하고 원본 상태 유지 |
| **미측정** | 선언상 후보이고 필요한 전 범위 목록에 해당 키의 BEO/profile/Run 이력이 없음. Run 전 범위 확인 전에는 ‘이력 확인 중’, 실패 시 확인 불가 | **‘측정 이력 없음 · 실행 지원 미확인’**. 정적 일치만으로 지원됨이라고 쓰지 않음 |
위 규칙에 들지 않는 알 수 없는 phase·condition 조합의 기본값은 확인 불가다.
전체 회차 조회 중에는 로딩만 표시한다. 조회 오류를 미측정으로 해석하거나 중간 수치를 확정 집계하지 않는다.

과거 실패만 있고 후속 유효 profile이 있으면 대표는 완료, 보조는 ‘실패 이력·재측정됨’이다.
유효 profile 뒤의 새 실패는 이전 증거를 지우지 않고 ‘사용 가능 결과 있음 · 최근 시도 실패’ 품질 경고다.
완료 뒤 진행 중 요청은 ‘완료 · 재측정 진행’으로 병기한다. 경고가 없는 완료를 유지할 수 있는
과거 이력과 현재 사용 조건의 경고를 구별한다. 조회하지 않은 Run 상태를 정상이라고 가정하지 않으며
초기 완료는 ‘catalog 근거 기준’임을 명시하고 상세에서 새 경고가 발견되면 같은 cache의 판정을 갱신한다.

**재측정됨의 의미.** 서로 다른 immutable 요청의 조건 일치와 더 늦은 유효 결과만으로
관측된 후속 측정을 안내한다. 결과 reference의 store/key가 정확히 이어지는 profile, 명시적
ReplacementSelection 사유 등 확인 가능한 연결은 원본과 함께 표시한다. BEO spec의
model/class/path/mode/targetNode/preprocessor/dataset/CPU request/quota/affinity/active·idle protocol이
하나라도 다르거나 frozen 이미지·UID 일치를 입증 못하면 ‘관련 재측정 후보’다.
이때 자동 ‘해결됨’/실패 숨김/완료율 가산을 하지 않는다. 이름 접두사/195→195b로 자동 연결하지 않는다.

**셀 값·필터·패널.** 모델/class/Node/path/mode/status/CPU/배치 필터와 이름 검색을 URL에 보존한다.
셀은 상태와 실제 배치 범위·측정 집합 내 최저 J/건만 간결하게 표시한다.
최저값은 최적 조건 추천이 아니며 정확 조건별 수치는 패널에서 구별한다.
미측정/실패 셀에는 ‘이 조건으로 측정 요청’을 바로 제공한다. 요청 수와 회차 수는 패널에서 구분한다.
패널은 Node/조건 선택 → 배치 표·에너지 곡선(제공된 오차만) → 실패/제외/후속 이력 →
측정 상세/회차/원본 링크 → 요청 CTA 순이다. p95가 없으면 ‘미제공’ 또는 회차별 범위로 표시한다.

‘논문용 검토만’ 필터는 제거한다. 목록에 CI가 없어 항상 0건이던 동작 대신 검토 기준
(CI·지연 표본·반복 수·계측 품질)을 안내하고 상세의 보존 근거로 검토한다.
GET 확인: `u2-o-15w-m-beo-195/result`는 200, 정확 active 회차의
`profiling-runs/u2-o-15w-m-beo-195-active-b1-a1/evidence-passport`는 404
`EvidencePassportUnavailable`였다. 정상 URL의 보존 근거 부재이므로 해당 셀에 품질 경고와 재측정 안내를 남긴다.

**클라이언트 집계와 상한.** 현재 88 BEO/78 profiles는 Map 조인으로 충분하다.
inventory·BEO·profiles는 독립 Query cache, 새로 고침 취소/인증 변경 cache 초기화는 기존 계약을 쓴다.
Run 전체 페이지는 첫 미측정 확정/선택 이력에서 지연 조회하고 동일 namespace cache로 재사용한다.
1155 Run/200은 최대 여섯 페이지 규모다. 개별 셀마다 전체 목록을 다시 받지 않으며 모든 Passport를 fan-out하지 않는다.
선택 Result와 정확 참조 Run Passport만 요청한다. 자동 retry/focus refetch를 새로 켜지 않는다.
목록을 주기적으로 통째 재집계하는 새 polling을 추가하지 않고 기존 수동 갱신과 명시된 진행 갱신만 쓴다.

F4는 [측정 API §12](../serving/measurement-api.md#12-대시보드-측정-api-보강-계약-b0-2026-10-30)의
호환성 GET와 기존 응답의 optional owner/statistics/비용 필드를 소비한다.
별도 coverage endpoint·지원 registry·통계 알고리즘·전체 셀 통계 fan-out은 추가하지 않는다.

### 8.4 요청 prefill과 목록·그룹

커버리지 CTA는 아래처럼 **초깃값만** 전달한다. POST/자동 제출·자동 preflight는 하지 않는다.

```text
/measurements/new?namespace=<ns>&model=<model-name>&deviceClass=<class-name>
  &node=<node-name>&path=<execution-path-name>&powerMode=<raw-mode-name>
  &returnTo=<encoded-local-coverage-URL>
```

모든 값은 URLSearchParams로 encode/decode한다. 같은 키 중복은 오류로 표시하고 임의 마지막 값을 쓰지 않는다.
namespace는 현재 접근 범위로 확인한다. 모델/class는 현재 선언에 다시 resolve하고
node는 해당 class 소속인지, path/mode는 실제 목록에 있는지 확인한다.
삭제/변경/비호환 값은 ‘미리 채운 조건을 확인해 주세요’와 원값을 표시하고 제출 잠금한다.
Node가 하나면 명시 선택한 값으로 채울 수 있지만 여러 Node면 선택을 남긴다.
workload/preprocessor/dataset/CPU/batch/반복/protocol은 이 네 축에서 추측하지 않는다.
모델을 참조하는 workload만 먼저 제시하고 사용자가 고르게 한다. 다중 후보 자동 선택은 하지 않는다.
선택한 workload가 prefill 모델을 바꾸면 차이를 안내하고 기존 조합을 조용히 유지하지 않는다.

기존 `?from=<BEO-name>` 복사 진입은 유지하며 새 키와 동시에 오면 충돌로 안내한다.
복사 원본의 namespace/UID를 확인하고 mode·Node·CPU·dataset 등 달라진 조건을 검토에 표시한다.
경로 단계와 최종 확인에 ‘같은 조건 완료/진행/실패·관련 후보’를 구별하고 기존 상세 링크를 둔다.
정적 비호환은 이유와 함께 비활성, 미확인 runtime은 미확인 안내 후 기존 사용자 검토 시
dry-run+preflight로 확인한다. 기존 7단계 순서·제출 전 검토 계약을 전면 바꾸지 않는다.

목록 기본 열은 모델·장치 종류·Node·전력모드·경로·상태·날짜다.
BEO spec에서 읽고 제목/slug를 해독하지 않는다. profile 목록에도 같은 축과 CPU/실제 batch를 보인다.
전력모드는 원본 이름을 보존하며 MODE_15W처럼 명확한 값에만 ‘15 W (MODE_15W)’ 보조 표기를 허용한다.
날짜는 기본 시간대를 명시한 짧은 값, 상세/tooltip에 UTC/KST 원본을 보존한다.
날짜 필터는 요청 생성 시각이며 profile 측정 시각과 섞지 않는다. 표는 정렬/25행 페이지,
필터는 `namespace,model,deviceClass,node,path,powerMode,status,from,to,q,page`다.
UI용 필터를 지원하지 않는 upstream API에 넘기지 않는다.

Run 그룹 키는 최소 model/class/targetNode/path/mode/preprocessor/dataset/CPU request/quota/affinity/
workloadMode·측정 protocol을 포함한 조건이다. active와 idle은 같은 실행 조건 아래 별도 구역으로 표시한다.
배치·시도 번호는 원본 참조의 batch/attempt만 사용하고 서로 다른 전력모드/부모 요청을 1~N회차로 재번호하지 않는다.
소유 요청 UID를 알 수 없으면 번호 대신 Run name과 생성일을 표시한다.
BEO의 저장 result가 있으면 `completedTrials[].run`과 `excludedTrials[].run`의 namespace/name/uid가
정확한 결과 참조다. 실패 `result:null`도 전체 namespace Run 목록의 `ownerReferences`에서
namespace + controller=true + apiVersion=edge.dacs.io/v1alpha1 + kind=BatchEnergyOptimization +
부모 UID 정확 일치만 소유 Run으로 연결한다. 이름·labels는 소유 증거가 아니다.
원 spec의 mode/batch와 양의 정수 attempt label만 표시하고 누락/잘못된 회차는 ‘회차 기록 없음’이다.
전체 page 조회 전에는 소유 회차 없음을 확정하지 않고 과거 owner 미기록은 ‘소유 연결 기록 없음’이다.
조건/이름 부분일치 후보를 소유 회차로 세지 않는다.

### 8.5 측정 상세·실패·품질

**2차 설문 R2 결정(2026-10-30).** 기존 청록색/서체 토큰을 유지하며 전력모드 칩만 강조한다.
상세 상단에 모델·장치 종류·Node·전력모드·실행 경로·배치·CPU·데이터셋을 항상 노출한다.
실패는 정확 owner로 연결된 최신 실패 회차의 로그 발췌·종료 코드·다음 행동을 최상단 alert로 요약하며,
owner 기록이 없는 과거 요청은 이름으로 추정하지 않고 요청 자체의 사유만 표시한다.
`runtimeTermination`/Job/실제 실행의 JSON 문자열은 객체로 해석하고 사실 목록으로 표시한다.
UI/UX Pro Max `error message actionable`의 Error Messages 규칙을 채택하여 오류를 `role=alert`로 알린다.
요청 목록은 모델/장치·Node/경로·전력모드/배치·상태·생성일 5열로 묶고 개별 축 정렬은 별도 선택으로 유지한다.
상태는 완료/측정 중/실패로 짧게 구분하고 완료의 근거 주의는 목록에서 한 번만 안내한다.
측정 상세·회차·비용 화면에서도 측정 서브 내비를 유지하며 삭제 진입부터 위험색 버튼과 영구 삭제 설명을 쓴다.

| 표시할 값 | 실제 제공 필드 / 정직한 대안 |
|---|---|
| 배치별 증분 에너지 | BEO `result.detail.incrementalWorkByBatch[].{batch,incrementalWorkJoulesPerItem}`, bestBatch, measuredBatchSizes. 평균만 제공 |
| 배치 에너지 오차 | BEO 응답 형제 `statistics.batches[].confidence`의 level/halfWidth로 평균±halfWidth 표시. 음수 하한·0폭 보존. 부재는 CI 미제공; 유휴 W CI를 J/건 오차로 재사용 금지 |
| p50/p95/p99·표본 | 통합 p95는 `statistics.batches[].latencyP95Milliseconds`, 같은 배치 client 호출 표본 수는 latencySampleCount. 부재 시 정확 Run Passport의 회차 p95 범위/개별 값만 표시하며 평균을 전체 p95라 하지 않음 |
| profile 지연 | `batches[].durationNanos`는 표본이며 게시된 p95 summary가 아니다. 별도 통계 알고리즘을 UI에 추가하지 않음 |
| 반복/배치 범위 | BEO completed/excluded exact 참조와 detail.trialsPerBatch, measuredBatchSizes. plannedBatchSizes 모두 측정했다고 단정 금지 |
| 품질 사실 | Passport 전력 samples/failures/gap/sourceTimestampAbsent, thermal observationAvailableSamples/issues, 실제 runtime/Node/동거 Pod·정책·dataset. 미관측 발열=정상 금지 |
상세 첫 화면은 서버가 보존 자료로 계산한 독립 active/shared-idle trial CI와 통합 p95를 표시한다.
통계 미제공 서버/객체에서만 기존 exact completed active Passport를 최대 3개 동시 조회해
회차 p95 범위와 각 회차 표본 수를 제공한다. idle p95를 active 지연에 합치지 않는다.
`NoPublishedResult`, `RetainedEvidenceMissing`, `StatisticsNotRecorded`는 각각 게시 결과 없음,
통계 원근거 기록 없음, CI·통합 p95 기록 없음이며 권한/transport 오류와 구별한다.
커버리지는 선택 Result만 읽고 catalog 표에는 같은 store/key 결과 참조인 경우만 통계를 연결한다.

소수 둘째 자리 고정 대신 3~4 유효숫자(에너지 기본 4)를 쓰되 0.09261/0.08682 J처럼 차이가 보이게 한다.
회차 지연은 ms, 전력 W, 에너지 J/건. 반복 수와 **지연 표본 수 N은 다른 수**다.
CI를 제공하는 원본만 신뢰 수준과 종류를 표시한다. CI 겹침만으로 ‘유의하지 않음’/유의함을 판정하지 않는다.
단일 배치면 ‘배치 1 측정 결과 · 다른 배치와 비교하지 않았어요’,
여러 배치면 ‘측정한 배치 {…} 중 에너지 평균이 가장 낮은 배치’다.
지연 표본 count=1은 ‘꼬리 지연을 판단하기에 표본이 부족해요’이며 반복 1회와 혼동하지 않는다.

**실패 Passport의 원인과 처리.** `kube/evidence_passport_query.go:61-76`은
Run의 `status.evidence=nil`에서 `verified evidence is not available`을 반환하고,
readapi handler는 이 일반 오류를 502 `EvidencePassportUnavailable`로 보낸다.
같은 코드는 저장소/loader 실패에도 쓰이므로 502 하나만으로 미생성/실행 실패를 확정하지 않는다.
기존 `MeasurementDetail`의 Passport 단일 QueryGate를 없애고 Run 상태와 Passport를 독립 표시한다.

raw Run 단건 GET은 공개되지 않는다. 기존 namespaced 목록 `q=<name>&limit<=200`의 모든
nextOffset을 읽고 **정확 namespace/name/uid**를 선택한다(q는 오직 수집 축소용 부분일치).
목록 실패/미완료는 404가 아니다. 상태는 conditions의 observedGeneration과 Run generation을
비교해 ExecutionComplete/EvidenceVerified를 독립 표시한다.
실패 reason/message, 제공된 `status.runtimeTermination.{exitCode,logTail}`, job/actualExecution을 보여 준다.
로그 없는 Run에 가짜 OOM/timeout 진단을 쓰지 않는다. evidence가 없고 종결 실패가 명시되면
Passport를 자동 호출하지 않고 ‘검증 근거가 생성되지 않았어요’로 안내한다.
evidence 참조가 있는데 로드가 실패하면 ‘근거 조회 실패’와 오류 코드·명시적 재조회 동작을 제공한다.
둘 모두 Run 본문과 뒤로가기는 유지한다.

실패 원인이 `RetryLimitExhausted`뿐이면 ‘재시도 한도에 도달했어요 · 상세 원인 미제공’이며
retryLimit=0을 여러 번 재시도했다고 표현하지 않는다. 상태 reason/message 원문을 본문에 보존한다.
새 유효 결과가 확인되면 첫 CTA는 ‘후속 측정 결과 보기’, 복사는 보조다.
미해결 실패의 CTA는 본문과 동일하게 **‘조건을 복사해 새 이름으로 요청’**이며 제출하지 않고 폼으로 이동한다.
동일 이름 재제출/기존 immutable 요청 수정이라고 표현하지 않는다.

**d 품질 경고와 근본 포맷 수정.** 서버 d는 `round(1e9/X - profileMeanNanos/batch)`이므로
음수도 저장될 수 있다(`internal/serving/kube/occupancy.go:327-332,670-677`).
UI `lib/scheduler.ts:nanosToMilliseconds`의 음수 remainder 문자열은 잘못된 소수 문자열을 만들어
`occupancyDuration`의 Number 변환에서 NaN이 될 수 있다. 공유 변환 함수에서 부호와 절댓값을
한 번 처리해 모든 호출자를 고친다. 음수/NaN/Infinity/비숫자 결과는 **품질 경고·사용 확인 필요**로
덮어 표시하되 원래 phase와 원값을 보존한다. 음수를 몰래 0으로 바꾸지 않는다.
누락은 결과 없음, 정상적인 0은 0으로 남긴다. throughput은 유한한 0 이상 문자열인지 표시용 확인만 하며
d/X를 브라우저에서 재계산하지 않는다. 정상 여부가 불명확한 문자열을 0으로 대체하지 않는다.
운영 consumer의 음수 clamp와 raw 측정은 다른 계약이다. 음수 d는 숫자 대신 품질 경고와 이유를
표시하고 원값은 원본 자료/내보내기에 보존한다.

**동작 비용.** optional step operationID/durationNanos/wallJoules/reason을 표시한다.
durationNanos는 lossless JSON 정수로 읽어 실제 ms와 원래 ns를 함께 보존한다. 실제 0 J는 0이며
null/미제공 J는 ‘수용된 에너지 없음’, 시간은 ‘실측 시간 기록 없음’이다. 원 조회 오류는 숨기지 않는다.
실측 J는 전체 Node 벽에너지이며 증분 J/item이 아니다. 계획 idleDurationSeconds를 실측으로 대신하지 않는다.
전체 실패/불수용과 일부 단계의 수용·실측 시간은 함께 표시하고 name/key/verdict·근거 위치를 보존한다.

### 8.6 실험의 게시 데이터와 설명형 비교

GET `/experiments`는 `{items:Experiment[]}`, `/{id}`는 단건이다.
envelope는 `schemaVersion/id/from/to/status/stages[{id,from,to,kpis}]/sources`이고
`kpis`는 `json.RawMessage`다(`internal/serving/httpapi/experiments.go:19-43`).
필드가 없으면 미제공이지 0/같은 조건이 아니다. query filter API는 없으므로 화면 필터는 클라이언트 전용이다.

| 의미 | `kpis.workloads[name]` shape | `kpis.requests[name]` shape |
|---|---|---|
| 기록 이름·identity | map key name; `workloadUID??uid`, `definitionID??definition` | 동일. UID/definition 미제공인 실제 campaign도 있음 |
| HTTP 성공률 | `successFraction`, successNumerator/successDenominator | 별도 의미 없는 경우 미제공 |
| SLO/기한 내 성공률 | `withinSLOFraction`, withinSLONumerator/withinSLODenominator | `deadlineSuccessFraction`, successNumerator/successDenominator |
| 성공 응답 p95 | `latencyMS.p95`, `.samples` (ms) | `completedLatencyNS.p95`, `.samples` (ns→ms) |
| 모집단·결손 | `population`, `missingReasons`, `outcomes`, offered | 동일 + rawPopulation/offeredResponsePopulation/issuanceFidelity |

ratio가 누락됐지만 명시적 성공 분자·분모가 존재할 때만 해당 같은 의미의 비율을 계산할 수 있다.
분모 0이면 비율 없음이며 `deadlineSuccess` 건수만으로 분모/HTTP 성공률을 추측하지 않는다.
요약의 첫 성공 지표는 ‘기한 내 성공 / 보낸 요청’으로 명확히 쓰고 HTTP 성공은 별도다.
서비스별 p95를 평균내 전체 p95를 만들거나 서비스별 %를 단순 평균내지 않는다.
전체 성공률은 동일 의미/집단의 분자·분모가 모두 있을 때만 합계로 제공한다.

같은 기록 이름을 A/B 한 행에 놓되 **‘동명 기록의 설명형 비교’**다.
UID/definition 같음·다름·미제공을 각 열에 보존한다. 현재 workload API로 과거 identity를 보충하지 않는다.
하나의 이름에 여러 후보가 있으면 임의 `.find()` 대신 후보를 구별하고 비교 선택을 요구한다.
이름이 다른 행은 한쪽 없음으로 남긴다. 다른 UID라는 이유만으로 같은 서비스명을 수십 개 행으로 찢지 않는다.
측정 규칙 차이는 기존 `differentMeasurementRules`를 사용하고 시간 길이·조건·SLO 미제공도 경고한다.
경고가 있는 차이는 ‘관측값 차이’이지 통제 실험/인과/안전성/통계적 승리 판정이 아니다.

차이 방향은 화면 전체 **A−B**로 고정한다. A/B 레이블은 사용자가 선택한 게시 기록 이름이다.
비율은 `100*(A-B)` %p, 지연·J는 A-B 원단위, 상대 에너지 변화는 `100*(A-B)/B` %다.
B=0/값 없음/지표 의미가 다르면 상대 차이는 계산 불가다. 원값이 다르면 반올림된 0 차이를
‘동일’이라고 단정하지 않고 ‘표시 정밀도 미만’으로 안내한다.
단위는 지연 ms, 비율 소수 2자리 %, J·W와 기타 수치는 공통 `significant`의 3 유효숫자다.
원본 정밀도와 분자/분모·표본·출처는 펼침으로 보존한다.

에너지는 stage `wholePoolJoules/activePoolJoules`와 `gapErrorBoundJoules`,
energyPopulation/energyCoverage/missingReasons.energy를 사용한다.
오차 bound는 **관측 공백 오차 상한**이지 95% CI가 아니다. 공유 POOL J는 서비스별로 나누거나 더하지 않는다.
실행 구간 길이/부하가 다르면 총 J가 낮다는 것만으로 효율 개선이라고 하지 않는다.
2차 설문의 명시적 E5 명명 계약을 적용한다: `full-E5-*`는 CHILL, `static-E5-*`는 정적 비교군.
같은 게시 기록 안에서 접두사를 제외한 전체 suffix가 정확히 일치하고 양쪽이 유일할 때만
자동 짝짓는다. 반복 suffix 불일치·중복·한쪽 부재는 임의 대응하지 않는다. 다른 접두사는 정책 미기록이다.
이 정책 라벨은 frozen 조건의 동일성이나 통제 성공을 인증하지 않는다.

단계 레인은 실제 `[from,to)` 순서/폭으로 그리고 클릭하면 해당 단계 KPI와 근거 범위를 함께 바꾼다.
두 publisher의 optional workloadUID/definitionID/identitySource와 substages를 타임라인 펼침 및
비교표에 표시한다. α=0을 보존하고 direct-rate null α와 과거 미기록은 missingReasons로 구별한다.
예정 offset은 lossless ns, from/to는 해당 workload의 실제 producer 시작에 고정한 예정 구간이다.
관측된 전환 사건이라고 부르지 않으며 anchor 부재는 stage 전체 시각으로 채우지 않는다.
명시 null identity는 legacy alias나 현재 Workload identity로 대체하지 않는다.
offered와 실제 기간이 있으면 ‘단계 평균 보낸 요청률’=offered/기간을 표시할 수 있지만 목표 부하나 α가 아니다.
초별 지연/에너지 선형 보간·가짜 이벤트는 금지한다. 게시 단계 집계 표/막대와 선택 당시 journal을 사용한다.

3차 개선은 전체 suffix 대응 단계(E5-1, E5-2, 반복 suffix 포함)를 같은 축에 놓고
CHILL과 정적군을 고정 색·실선/점선·원/사각 마커와 범례로 구분한다. 한쪽만 있는 단계도 남기며
중복 정책/단계는 임의 선택하지 않고 결손으로 표시한다. W 막대는 J/실제 구간 초로 정규화하고
관측 공백 오차 상한/초를 오차막대로 표시한다(CI 아님). 원 J 및 J/전체 보낸 요청은 수치 표에 보존한다.
요청 수 결손·동명 publisher 중복·0 요청에서는 J/요청을 만들지 않는다. 공유 에너지를 서비스에 배분하지 않는다.
상단 전체 대응 단계 표는 req/s·W·원 J 차이와 서비스별 성공률 Δ(%p)·p95 Δ(ms, 배율)를 제공한다.
목록과 선택 단계 모두 에너지 절감과 서비스 손실을 병기하며 단일 단계를 캠페인 우열로 승격하지 않는다.
차트는 서비스 선택 한 개, 상세는 서비스 선택·하위 계획·원본 필드별 접힘으로 긴 문서를 제한한다.
UI UX Pro Max `Compare Categories`의 그룹 막대·비색상 구별·접근 가능한 수치 표 권고를 채택했다:
정책 비교가 목적이므로 정책별 시리즈와 키보드 접근 표를 두고 실제 시간순 레인은 별도 접힘으로 유지한다.
선택 URL은 Next의 native history 연동으로 동기 갱신하며 각 동작이 최신 location.search를
읽는다. A/B 연속 선택은 이전 렌더의 searchParams 스냅샷으로 서로 덮어쓰지 않는다.
없는 이유는 미제공으로 남기며 FAIL/NOT_READY/MEASURED_DESCRIPTIVE는 각각 게시 판정·준비·설명형 결과로 구분한다.
성능 실패와 공개 불가/미평가를 같은 빨간 배지로 처리하지 않는다.

### 8.7 탐색 계약과 구현 소유권

Breadcrumb는 `측정 > 커버리지/요청 목록 > 측정 > 회차`, `기록 > 실험`을 따른다.
필터/선택한 셀과 parent UID가 있는 내부 `returnTo`를 링크에 유지한다.
허용 returnTo는 `/measurements` 계층, `/scheduler/profiles`, `/experiments`의 same-origin 상대 URL뿐이다.
`//host`, scheme, 제어문자, 다른 origin은 무시하고 안전한 상위 목록으로 돌아간다.
브라우저 뒤로가기는 history를 유지한다. 직접 진입도 URL만으로 선택/필터가 복원되고 parent가 없으면
유효한 기본 breadcrumb를 쓴다. `/scheduler/profiles`는 기존 경로를 유지하되 sidebar의 측정으로 소속을 맞춘다.

| 소유자 | 파일 소유권과 공유 계약 |
|---|---|
| S1 | 새 `features/measurement-coverage.tsx`(coverage와 `MeasurementPlansPage`), `features/measurement-runs.tsx`(`MeasurementRunsPage/MeasurementGroupPage`), `lib/measurement-coverage.ts`+test. 기존 request/scheduler-profiles, **read-types.ts와 catch-all page.tsx 독점** |
| S2 | 기존 measurement-plans(상세만)/read-pages(Run 상세·PreflightAnswer)/measurement-projections, measurements.ts+test, **scheduler.ts의 nanosToMilliseconds와 scheduler.test.ts**, Screen/sidebar/globals.css 독점 |
| S3 | experiments.tsx, lib/experiments.ts+test 독점. URL a/as/b/bs 유지, route/CSS/type 파일은 수정하지 않음 |

Screen 공유 인터페이스는 `width?: "narrow"|"data"`, `actionPlacement?: "footer"|"header"`,
`breadcrumbs?: {href?:string;label:string}[]`다. 마지막 breadcrumb는 현재 위치, back은 기존 호출과 호환한다.
S1/S3는 이 props만 사용하고 CSS는 기존 Tailwind/semantic 토큰을 이용한다.
S1은 새 목록 함수를 route에 연결하고 S2는 옮겨진 옛 목록·그룹 구현을 제거한다.
함수 alias/중복 export·새 공용 framework는 남기지 않는다. PreflightAnswer/preflightConclusion은 기존 caller가 있어 유지한다.

S1 구현은 `/measurements/coverage`와 새 목록/정확 조건 Run 그룹을 별도 소유 파일에 둔다.
inventory 양쪽 페이지와 namespace Run 전체 페이지는 탭 Query cache에 한 번 모으며,
선택 Result와 정확 참조 Passport만 추가로 읽는다. 미조회 Run은 미측정으로 바꾸지 않는다.
`논문용 검토만`은 현재 catalog에 CI가 없어 검토 보류(0개)로 남긴다. CI를 만들어내거나
`confidenceLevel`을 에너지 CI로 취급하지 않는다. 복사 진입은 기존 `from`에
`fromNamespace`/`fromUID`가 제공되면 정확히 확인하며, 조합 prefill과 함께 오면 잠근다.
이 구현 상태는 runtime 지원 확정·논문 적격·재설문 수용 완료를 뜻하지 않는다.

실제 순수 함수의 최소 회귀는 [계획 §5](../../plans/2026-10-30-dashboard-measurement-ux.md#5-검증과-통합-완료-조건)를 따른다.
특히 nanosToMilliseconds 모든 caller의 음수·정밀 정수, 이름 partial-match 충돌,
누락 CI/단일 배치/분모0/UID 불일치를 테스트한다. UI 구조를 문자열로 검색하는 테스트는 새 회귀 근거가 아니다.
모든 슬라이스 후 통합 담당자가 한 번 test/typecheck/lint/build와 브라우저 수용을 실행한다.
완료는 M1·E1 세 페르소나 전원 성공과 평균 SUS ≥68까지이며, 이 문서 작성은 그 달성 증거가 아니다.

### 8.8 F4 새 API 소비 검증 (2026-10-30)

B1 `340f96ee`, B2 `2963ee11` 실제 DTO 및 B3 `80ea2196`의 두 publication shape를 대조했다.
relay에는 정확 `GET /model-compatibility`와 필수 단일 query 네 개만 추가했다.
새 라이브러리·Go·클러스터·저장 객체·publication 변경은 없다.

Node 24.0.0 / nice 19 최종 typecheck·lint·전체 80 tests·production build 통과.
build의 Asta Sans fallback font-metrics 경고는 남아 있다. 마지막 코드 수정 뒤 통합 검사를 실행했고,
그 전 초기 검사도 통과했다. 검사는 로컬 구현 회귀이며 배포 수용의 증거는 아니다.

`/tmp/uxf4`, 포트 3204의 production 사본에서 GET-only 가드를 로그인 전에 설치했다.
실서버의 statistics/step 실측/실험 metadata 부재를 확인하고 기존 미제공·기록 없음 화면을 캡처했다.
새 필드는 명시 `f4-fixture-*` route mock으로 CI 음수 하한·0폭, 호출 표본/독립 trial,
정확 owner UID, 실패 전체+0 J/정밀 ns, 두 publisher α=0/direct-rate/identity/예정 구간을 확인했다.
coverage와 request prefill을 여는 전체 과정의 compatibility GET는 각 한 번(총 2회)이었다.
1440×900 및 390×844 캡처에서 기존 토큰·표·모바일 가로 스크롤 방향을 유지했다.

증거 경로: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/ux-survey-20261030/`.
`f4-live-{coverage,statistics-fallback,operation-fallback,experiment-fallback}.png`는 실데이터,
`f4-fixture-{statistics,failed-owner,coverage,request-prefill,operation,experiment-timeline,experiment-comparison,experiment-mobile}.png`는 합성 응답이다.
검사 원로그는 같은 폴더의 `f4-{typecheck,lint,test,build}.log`다.
보조 캡처는 `f4-live-owner-fallback.png`, `f4-fixture-operation-exact-nanos.png`,
`f4-fixture-experiment-{substages,alpha-comparison}.png`다. 브라우저 탭과 로컬 서비스는 종료하고
자기 사본 `/tmp/uxf4`는 삭제했다. 토큰·원시 export·쓰기 요청·서버 설정 변경은 없다.
운영 B1/B2 배포와 B3 신규 publication·세 페르소나 재설문은 수행하지 않았으며 별도 승인 수용이다.
