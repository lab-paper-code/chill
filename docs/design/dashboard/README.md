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
| `GET /experiments`, `GET /experiments/{id}` | 시나리오, 프로파일과 설정, 초기 구성, 제어요소, 요청과 전력 및 전환의 같은 시간축 참조 | 시스템 실험 기록과 조회 projection이 필요하다. |
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

## 7. 남은 연결과 수용 경계

| 항목 | 현재 공백·다음 근거 |
|---|---|
| DB-FR-13 / G2 | Passport는 family/version/provider/architecture 제공. 실제 backend는 검증된 RuntimeResult.Backend, 실행 장치는 firstfloor RuntimeMetadata의 device/providers/gpuUUID/rknn 투영 대기. candidate.executionPath 이름으로 추정 금지. |
| DB-FR-30 / G1 | 실행 경로×DeviceClass Compatible/Incompatible/Unknown·사유는 internal/modelcompat 내부; Inventory 선언과 별개인 조회 필요. |
| DB-FR-32 / G3 | PowerModeTransitionCost 원본의 모드 쌍·방향별 반복 시간/J·판정/사유·관측 구간·경계 불확실성·Serving 지속·참조 API 대기. OperationCostMeasurement는 대체 불가. |
| 라이브/재생 | T5/T6 코드 연결은 승인 배포·실제 WS 수용 완료가 아님. 재생 API/원본 계약은 [T4 관측 설계](../serving/observability-split.md); 제품과 고정 fixture를 구분. fixture는 CHILL_DEMO_REPLAY=1에서만, 인증 우회·upstream·변경·추론 금지. |
| 실제 수용 | qualifying native campaign 이후 Ready 추론 성공, ARM/RKNN hardware, 요청 drain·운영 전력·selector·전환/writer·전체 SRS 시나리오·성능 예산은 전체 완료 근거가 필요. |
| 실제 조회 장애 | scheduler-dashboard-8c7cbec 배포 시 load/journal BFF 504, 직접 1분/limit=1 journal도 30초 무응답·S3 PutObject deadline. 과거 기록/A/B/전력 정상 조회 미검증; timeout/타 컴포넌트 설정 변경 없음. 실제 작업은 GET 한정, 설정 PUT·제어 활성화·인위 전환·전력 실험 아님. |

