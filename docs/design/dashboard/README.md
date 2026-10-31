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
| 정보 구조 | 운영·서비스·측정·실험·기록, 보조 설정. 정본 경로와 소유권은 §6. 기존 도메인 계산·API·변경 안전 계약을 유지한다. |
| 전환 상태 | 아래 구조는 승인된 목표이며 단계별 배포 수용과 구별한다. 링크 허브·일반 준비 중 화면은 부활하지 않는다. |

## 2. 인증·중계·변경 계약

| 항목 | 현행 계약 |
|---|---|
| 신원 | 기존 Kubernetes bearer를 탭 메모리와 버전·system 단위 sessionStorage에 보관한다. hydration 후 복원하며 저장 거부는 메모리 전용. 신원 변경·logout·401/4401은 저장분·요청·cache·socket을 같은 epoch로 정리한다. 403/4403·서비스 장애는 만료가 아니며 목적 URL을 유지한다. 복제 탭 토큰은 별도이고 새 창은 noopener다. BFF는 원본 Authorization 전달; ChillServer TokenReview + 정확한 verb/path SAR로 신원을 검증한다. |
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
  별도로 읽는다. `decision:null|{at,state,reason,explanation}`에서 큰 후보·프로파일은 제외한다.
  판단 identity는 원본 RFC3339Nano `decision.at`이다. `SchedulerDecision`에 없는 id 필드를
  새 원본 식별자로 가정하지 않는다. 근거는 선택한 `decision.at`의 journal 구간 조회다.
- `reservations[]:{owner,nodes:[{uid,name}],workloadUIDs:[uid],podUIDs:[uid],instances:[id],reason,explanation}`는
  판단 snapshot의 `SchedulerReservation`(`internal/serving/scheduler.go`)에서 `target`·`routeOnly`를 뺀 것이다.
  `reason`은 원본 코드(미제공 시 빈 문자열), `explanation`은 확인된 예약 사실의 운영 문장이다.
  빈 배열이 예약 해제를 나타낸다. 작업 종결로 해제를 추정하지 않는다.
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
| first auth | 정확한 `/api/live`, 동일 CHILL_PUBLIC_ORIGIN(미설정 시 검증한 HTTPS Host); query/subprotocol/누락·다른 Origin 거부, 임의 forwarded 불신. 5초 내 ≤16 KiB 첫 `{type:"auth",token}`. 상류에 Bearer 변환, URL/cookie/log/디스크 영속 저장 금지. 승인된 탭 sessionStorage 예외는 §2 신원 계약만 허용. |
| 상류 권한 | Upgrade 전 TokenReview와 `get /chill/api/v1/live` SAR, 성공 전 데이터 0개. HTTP 401/403/503→4401/4403/1013, 첫 auth 오류=4401·인증 뒤 계약 오류=1002. 4401/4403 자동 재시도 없음. 전역 feed 권한을 단일 workload 권한에서 확대하지 않음. |
| 수명 | handshake 10초·socket write 5초, 일반 HTTP timeout/deadline 전 정확한 live 분기. 상류 성공 hello→resume→snapshot/재개; head capture+구독 등록은 한 잠금. 조기 resume 한 건만 보관. 양 끝 종료/logout/shutdown은 socket/구독/timer 해제. |
| resume | `{type:"resume",from,at}`; from=0은 baseline(at 생략), 나머지는 보존 `(seq,at)` 일치만 재개. 범위 밖·미래·재시작 불일치면 snapshot. hello는 checkpoint 전진 없음, baseline 뒤 중복 버림·last+1 초과는 미적용 후 재동기화. |
| 상한 | hub 최근 600메시지/8 MiB 먼저 도달, 연결 큐 602메시지/2 MiB, 프레임 1 MiB. 600개는 10분 보장 아님. 큰 snapshot은 자르지 않고 1009. metrics 먼저 drop 후 빠진 seq 뒤 delta/event보다 앞 snapshot barrier; 부족하면 snapshot 하나. write 막힘/Node bufferedAmount>2 MiB는 1013. producer 비차단. |
| 재연결·신선도 | 0.5초→최대30초 지수 backoff+jitter, baseline 뒤 정상. baseline+연결+관측≤5초만 실시간, 그 뒤 오래됨, 종료는 끊김. heartbeat 5초는 수신만 갱신, 관측 아님. 유실은 0/직선 보간 금지. |
| 집계 | UTC 1초 `[bucketAt,bucketAt+1s)`. received=receivedAt 사건률, 나머지는 response_finished.At 종결 집단. completed=성공, failed=timeout/취소/거절/오류/전달 실패 등. N=completed+failed, 성공=completed/N·SLO 안=withinSLO/N·밖=(completed-withinSLO)/N·실패=failed/N. N=0은 비율/분위수 null. |
| 지연·보정 | p50/p95/p99는 같은 종결 집단 유효 종단 지연(실패 포함), latencySamples 병기. 요청 적용 SLO/definitionID 보존; native finished/중복 final은 새 완료 아님. 무유입 정상 초는 0 count, 계측 불가는 gaps. 최근10초 늦은 callback은 원 버킷 전체 대체, 이후 gaps+REST; 원시 요청은 push하지 않음. |
| 전력 | 초 도착 실제 표본 노드별 산술평균, 이전 W 복사 금지. 전체/활성 범위 UID 중복 없이 합산, 필수 표본/소속 모르면 null. J는 coverage 검증 REST 적분, 브라우저 W 합산 금지. |
| producer | cmd/chill-server 조립, internal/serving/live hub; OnDecision은 동일 capture, OnAction은 원본 결과, RequestEvent 반영 성공 뒤 집계, 기존 power Source adapter 사용. callback은 메모리만 갱신, socket/REST/S3 대기·새 수집 루프·서버 REST polling 없음. |
| client | 탭 QueryClient의 chill-live reducer: checkpoint/snapshot/metricsByBucket/events/gaps/lastReceivedAt/lastObservedAt. 최근600초·1000사건과 시작 baseline/bounded delta; 상한 유실 명시. 화면 일시정지는 선택 snapshot·시각을 고정하고 수신 계속, live 복귀 최신 baseline. 숨긴 탭의 의도된 수신 중지는 별도 `paused-hidden` 상태이며 다시 보이면 재연결한다. |

### 3.1 운영 판독표

기본 진입점 `/`는 운영 판독 / 서비스 상태 / 진행·사건의 세 영역이다.
서비스 영향·성능 판정·관측 상태·실험 상태를 분리하며 무부하를 성능 정상으로 답하지 않는다.
서비스 표에 실제 장치를 직접 표시하고 실제 설정된 SLO 분위수를 사용한다.
유휴만 있으면 유입·배치·접수·자동제어 집계로 압축하고 개별 유휴는 같은 표의 보기 전환으로 연다.
장치 화면은 명시적으로 선택하며 빈 장치 표로 자동 전환하지 않는다.
장치는 이름순, 서비스는 선택·읽기 중 자동 재정렬 금지다.
관측 공백·전력 누락·연결 지연을 서비스 장애와 합치지 않고 회복은 후속 유효 관측으로만 확인한다.
사건은 대상·동작·결과·제공 사유를 보이고 유지 묶음의 원본을 보존한다. 임의 작업 생애주기를 만들지 않는다.
선택 근거는 정확한 decision.at·UID·구간을 고정한다. 대기 pending/native waiting과 과거 판단 queued는 다른 집단이다.
지연 분위수 평균·결손 영 채움·공유 전력의 서비스 귀속은 금지한다.

운영 구현은 `features/live-board.tsx`의 `LiveBoardPage`, 장치는 `features/resources.tsx`의
`DevicesPage`다. `boardVerdict.observation`은 서비스 영향과 별도 StatusCell 및 aria-live 축으로
출력한다. 후보·추정·부하·용량은 서비스 Sheet의 `evidence` 선택이며 별도 운영 허브를 만들지 않는다.
장치 표는 inventory·POOL·실제 scheduler 자원과 계측 UID의 합집합을 이름순으로 보존한다.
계측 전체와 구성 범위 차이는 같은 시각·범위·완전성을 확인할 때만 산출한다.
정본 이동은 `lib/dashboard-routes.ts`의 명시적 URL/query 매핑을 공유하며 API 경로는 변경하지 않는다.

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
| `GET, POST /scheduler/profiles`, `DELETE /scheduler/profiles/{namespace}/{name}` | 채택 프로파일과 조건, 채택 결과와 `configVersion` | 측정 결과 처리 성능의 현재 사용·채택·해제에 재사용한다. |
| `GET /namespaces/{namespace}/operation-cost-measurements[/{name}]`, 같은 형태의 `dispatch-occupancy-measurements` | `{items:MeasurementProjection[]}` 또는 단건 안전한 projection. 원본 spec 전체가 아니다. | relay GET 허용됨. 두 측정 화면으로 연결한다. |
| `GET /scheduler/capacity` | `{rows:CapacityRow[]}`. 자동제어 워크로드가 필요로 하는 행과 입력, κ 및 탐색 판정 | relay GET 허용됨, query 없음. 용량 근거 화면과 현재 settings 대조에 연결한다. |
| `GET /observations/load` | 명시적 구간의 수신, 접수, 배정과 완료 사건 수 및 사건률 | from/to를 필수로 전달한다. 완료는 수신 cohort와 별개의 사건 집계다. |
| `GET /observations/scheduler` | 판단, snapshot, 설정 변경, action과 전력 관측의 보관 중인 append-order 페이지 | from/to, bounded limit와 cursor를 사용한다. 응답은 페이지 항목과 nextCursor로 구성된다. relay는 이 경로와 `GET /scheduler`, `/scheduler/state`, `/scheduler/profiles`를 ChillServer `view=compact`로 읽고, 화면 파싱 전에 공유된 `profile`과 `completionMeasurement` `$ref`를 응답 dictionary로 복원해 full 응답과 같은 값을 쓴다. 응답 상한은 그대로다. |
| `GET /observations/power` | 명시적 구간의 전체 및 활성 POOL joules와 평균 W, 노드별 값, `complete`, `reasons`, 로컬 보존보다 앞선 구간의 `gapUntil` | 기록의 전력 조회와 장치 전력 범위에 연결한다. 브라우저에서 적분하지 않는다. |
| `GET /systems`, `GET /inventory` | ChillSystem의 condition과 이유, DeviceClass와 typed 전력모드 actuator, Node | System, Inventory와 Run 목록은 아래 query handler에서 제공한다. |
| `GET /experiments`, `GET /experiments/{id}` | 완료된 불변 게시·단계 KPI·출처와 선택적 `outcome`. 목록은 `definition` 대신 선택적 `comparisons`, 단건은 `definition` 제공 | §8 정의 기반 reader. 목록 판정은 comparisons와 목록 KPI만 사용하며 항목별 단건 GET을 하지 않는다. |

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
| 목록 identity·대상·상태·시각 | `metadata.name`, `metadata.namespace`, `metadata.uid`, `node`, `status.phase`, `metadata.creationTimestamp` | 이름/Node, 상태 문장·기호, KST 시각(UTC tooltip). UID는 상세 원본 identity에 표시 |
| 상세 결론·사유 | `kind`, `status.phase`, `status.reason` | 한국어 요약을 주표기하고 제공된 reason 원문을 펼침 안에 보존. 임의 원인을 확정하지 않음 |
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
| 요청당 분배 점유시간 d | `status.perRequestNanos` | 기존 nanosToMilliseconds 변환 후 3자리 유효숫자 ms/request 주표기. 정확 ns는 원본/내보내기만. 누락과 0 구별 |
| 지속 완료 처리량 X | `status.throughput` | 수치 검증 후 3자리 유효숫자 + req/s. 원 문자열은 원본 보존. 누락/빈 문자열=“결과 없음”. d나 κ 재계산 없음 |
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

## 6. 화면 소유권·정본 IA

기존 개요 → 측정 흐름, 측정 여섯 탭, 장치 중심 운영 보드와 과거 §8~9의 중복 설계를 아래 규약으로 대체한다.
SRS DB-FR-50~52·56·65는 변경, DB-FR-67은 폐기 이력, DB-FR-68~70은 공통 규약이다.

| 정본 경로 | 소유 정보 / 첫 화면 질문·구성 |
|---|---|
| `/` 운영 | 지금 무엇을 확인해야 하는가? 현재 서비스 영향·유입·배치·진행 변경 / 운영 판독·서비스 상태·진행/사건 |
| `/operations/devices` 장치 | 어디에 무엇이 배치됐는가? 전력 추세·장치/배치 표 → 사양·모드·자원 역할 |
| `/workloads` 서비스 | 어떤 정의가 적용되는가? 목록 관리 표; 상세 적용 상태·정의/설정·배치/연결 |
| `/measurements` 범위 | 조건·집계와 선택 모델 행렬. 지원 안 함 조합은 행렬에 보존하고 지원 범위 집계 분모에서는 제외. 서버 기준 시각·신선도·갱신 상태 표시, 목표 미제공 시 진행률 없음. 셀 Sheet는 상태 축→배치 결과→출처·작업·원본 순서 |
| `/measurements/requests` 요청 | 부모 요청 선택 Sheet에 자식 회차·실패 원인·점검·기존 사용 결과. 회차 복귀 링크는 부모 UID 또는 검색 필터·페이지를 보존. `?view=runs` 전체 회차 검색, `?run=…&uid=…` 직접 선택. 작성 `/measurements/requests/new` 대상→조건→점검·제출 |
| `/measurements/results?kind=throughput\|operation-cost\|dispatch-occupancy` 결과 | 종류 전환 틀 하나, API·통계 의미는 독립. 현재 사용/품질 분리, 변경 비용 준비·제거별 행과 실제 통계 열. `profile` 또는 `measurement`·namespace·UID 선택 보존 |
| `/experiments` 실행 | 무엇이 실행·정리 중인가? 실행 표·선택 실행·필요한 정리 확인 |
| `/experiments/results` 결과 | 비교 근거로 어떤 차이를 말할 수 있는가? 비교 판독·구간 비교·선택 서비스 추세 |
| `/experiments/load-scenarios` 부하 설정 | 어떤 부하 정의를 사용하는가? 검색/표; 상세 정의·구간 미리보기·사용 중 실행 |
| `/records` 기록 | 선택 시점에 무엇이 있었는가? 판단·변경 / 서비스 요청 / 전력의 조회 범위·기록·선택 근거 |
| `/settings` 설정 | 어떤 값·카탈로그가 적용되는가? 운영 설정 / 모델 / 입력 처리 / 데이터셋의 선택·현재값·연결/영향 |

설정 구현은 `features/settings.tsx`에서 `tab=configuration|models|preprocessors|datasets`를 선택한다.
운영 설정은 범위 선택·현재값 표·연결 작업의 세 영역과 값 수정 Sheet를 사용한다.
카탈로그는 검색 가능한 표에서 이름·UID를 고정한 Sheet를 열고 현재 정의·연결 영향·수정/삭제/원본을 제공한다.
`/models`, `/preprocessors`, `/datasets`, `/configuration`의 최상위 목록은 설정 탭으로 이동하며 등록·상세·편집 경로는 유지한다.
`Screen`의 설명 문단 API는 제거하며 입력 정의와 변경 제약은 명시적인 도움말로 제공한다.

각 사실은 정본 한 곳만 소유한다. 연관 화면은 같은 KPI 묶음을 복제하지 않고 대상·기간·namespace·UID를 보존한 링크를 제공한다.
서비스 상세의 `현재 관측`은 운영으로 연결하며 접수 허용을 실제 유입으로 번역하지 않는다.
동일 축의 값은 같은 UID·시각일 때만 공통 계산 결과를 공유한다. 서비스 관리는 WebSocket 없이 관리 API로 동작한다.
서비스 구현은 `features/management.tsx`의 목록 관리 표와 통합 상세다. 상세는 적용 상태·정의/설정·배치/연결 세 영역이며, `features/service-hub.tsx`는 관리 API 조회·정의·배치 표시만 소유한다. 옛 세부 화면 export는 제거하고 세부 경로는 통합 영역으로 이동한다. 등록·수정·삭제의 UID/버전·충돌·결과 미확인 계약과 자동제어 설정 재조회 후 변경 계약은 유지한다. 접수 허용 렌더 회귀는 `lib/service-ui.test.ts`가 목록·상세의 무부하 서비스로 확인한다.
서비스 분배 표의 장치·인스턴스·준비·가중치는 동일한 scheduler snapshot에서 읽고 서비스 UID/라우팅 관계로 제한한다. 가중치 영은 보존하며 미제공은 별도로 표시한다. `/pools`의 배치 관측은 자체 관측 시각이 있는 상세로 유지하고 분배 snapshot과 시각 일치를 요구하거나 서로의 행을 결합하지 않는다.
측정 범위는 전체 모델 요약과 선택 행렬을 적층하지 않는다. 조합/배치 분모를 분리하고 미측정 모드·지원 안 함·미확인을 문자로 보존한다.
목표 배치가 없으면 진행률을 만들지 않는다. 요청 실패와 기존 사용 결과는 공존하며 회차 전체 검색·딥링크는 유지한다.
결과 종류의 API·통계는 합치지 않고 변경 비용 준비·제거를 각각 행으로 표시한다.
작성·수정은 전용 페이지의 대상 → 조건·내용 → 점검·제출, 단일 요청은 입력·실행 상태·실제 응답이다.
필수값을 고급 설정에 숨기지 않고 조건 변경 시 점검을 무효화한다. 과거 정의/현재 환경 차이는 명시 반영한다.

## 7. 공통 상태·컴포넌트·상세 규약

기존 폰트·의미색·테마를 재사용한 운영 판독표다. 새 상태 엔진·위젯 체계·기록 저장소·보고서 빌더·차트 의존성은 추가하지 않는다.

| 대상 | 규약 |
|---|---|
| 상태 정본 | `chill-dashboard/lib/status.ts`; 기존 판정의 표시 계층 |
| 부재 종류 | 미기록·미제공·미확인·판정 불가·해당 없음·지원 안 함·결과 미확인·권한 없음·조회 실패·오래된 관측 (DB-INT-06 의미 유지) |
| 축 이름 | 서비스 영향·성능 판정·관측 상태·실험 상태·접수 허용·배치·실제 유입·현재 사용·품질 판정·실행·측정·정리 |
| 상태 셀 | `components/status-cell.tsx`의 `StatusCell({axis,label,tone,reason?})`; 책임 축만 표시, 새 판정 엔진 금지 |
| 부재 해석 | 확인된 영=0, 성공 조회 빈 목록=항목 없음/기록 없음, 지원 조합 근거 부재=미측정. null을 영으로 바꾸지 않음 |
| 반복 압축 | 범위 공통 상태·사유는 머리에 한 번; 필수 열과 선택 상세는 유지 |
| 화면 | 1440×900, 주요 영역 최대 3개(독립 필터 포함), sidebar 184px·header 56px·본문 여백 24px·표 14px·행 44px 목표 |
| 머리 | 명사형 H1 한 번, 목록 breadcrumb 없음, 상세 상위 맥락 하나. 대상·기간·기준 KST·신선도 구조화 |
| 표 | 단위는 헤더, 숫자 오른쪽 정렬·tabular-nums, 이름은 자기 열 안 줄바꿈. 중앙값·최대·표본 수 구분 |
| DetailSheet | 새 래퍼 없이 기존 `components/ui/sheet.tsx` 재사용 규약. 단일 selected 상태로 하나만 열기·중첩 금지·핵심 표 상단·원본 하단 |
| 비동기 | 핵심값 뒤 영역, 로딩·오류·사건 도착이 읽던 값을 밀어내지 않음 |
| Dialog | 삭제·중지·자원 반환 확인 등 짧은 위험 작업만 |
| 수치·설명 | 표·Recharts·CSS만, 결손 보간/영 채움 금지. 한국어 라벨·숫자·상태어; 정의는 툴팁·전역 용어 도움말 한 곳 |
| 제한 | 판단에 필요한 건수·원인·제약은 본문에 표시, tooltip/title에 숨기지 않음 |
| URL·접근성 | 필터·기간·선택·namespace·UID 복원, 외부 return URL 금지. 키보드 행 선택·Esc·focus 복원·비색상 상태 |
| 좁은 폭 | 라벨/값 세로 배열, 행렬은 장치→모드→경로 목록. 모든 필드·작업 보존; 문서 넘침과 내부 잘림 별도 확인 |

## 8. 기록·비교·실험 안전

기록은 범위 선택만 공유한다. 서로 다른 API를 완전한 단일 이력으로 합치지 않으며 실제 제공 구간·누락·서버 상한·페이지 경계를 보존한다.
일별 p95 평균·브라우저 전력 적분·가짜 주간 집계를 만들지 않는다. 목록은 summary, 원본은 선택 append offset의 compact 단건이다.
현재 페이지 내보내기와 선택 원본은 범위를 구분한다. 선택 근거는 과거 시점 원본이며 최신 상태로 대체하지 않는다.
기존 A/B는 각각의 입력 구간·UID·정의·whole-window cohort를 유지한다.

`/records`는 링크 허브 없이 종류 전환·조회 범위·기록 표와 선택 Sheet를 제공한다. 구 `/scheduler/history`, `/observations`, `/changes`, `/transitions[/id]`, `/scheduler/power`, `/scheduler/compare`는 기존 query를 보존해 같은 기록 선택으로 redirect한다. 서비스 요청·관리 변경은 workload 관측 API, 판단·설정 변경·전력은 scheduler journal의 원래 페이지 경계를 유지한다.

`/changes`는 query를 읽는 페이지 redirect로만 처리해 `scope=scheduler`와 서비스 관리 변경을 분리한다. 전력 탭의 workload UID는 왕복 맥락이지 journal 전력 필터가 아니며, 요청·변경 이동은 반복 definition/outcome을 그대로 보존한다. 인증 변경은 requests/power 화면 종류를 유지하고 사건 선택만 초기화한다. `view=history`의 불완전한 참조는 현재 상태로 대체하지 않는다.
`route`·`unroute`·`release` 결과의 선택 근거는 선언/실제 비율을 병렬 표시한다. 선택된 `OperationResult.operation`에 보존된 `reason`·`actorUsername`·`settingsValidatedAt`을 그대로 표시하며, 다른 journal 사건에서 근거를 빌리거나 재탐색하지 않는다. 현재·과거 결과가 같은 렌더러를 사용한다.

실험은 DB-FR-66과 [실험 실행 설계](../profiling/experiment-execution.md)를 따른다.
게시 목록의 `comparisons`, 단건 `definition.spec.comparisons`만 비교 관계로 사용하며 단계는 ID로 연결한다.
suffix·요청률 유사성 fallback은 사용하지 않는다. 정책은 기록 정의, SLO는 기록 정의·실행 결과에서만 읽고 현재 Workload로 소급하지 않는다.
과거 대상은 publicationID·stageID, 서비스는 workloadUID로 연결한다. 정의 없는 과거 게시의 비교는 미기록이다.
목록 판정 때문에 항목별 단건을 전수 조회하지 않는다. 대상이 목록에 없을 때만 필요한 게시 단건을 캐시 조회한다.

| 비교 조건 | 표시 |
|---|---|
| 명시 관계·조건 확인 | 동일 조건 비교·원값·차이 |
| 관계 있으나 조건 다름/일부 미확인 | 참고 비교·제한·원값, 동일 조건 성과 집계와 분리 |
| 정의/관계 없음 | 단계 절대값·비교 미기록 |
| 대상 조회 오류 | 대상 조회 실패·다시 조회 |
| 실제 게시/단계 없음 | 대상 게시 없음 |

비교 상태·제한·일치/참고/대응 없음 건수는 차이보다 앞이다. 단순 흐림이나 tooltip으로 대체하지 않는다.
서비스 선택은 표·차트를 함께 바꾸고 모든 서비스에 접근 가능하다. 구간 상세는 조건 → 원값 → 사건 → 원본이다.
차이는 후보−기준, 비율 차이는 %p, 지연은 ms, 에너지는 J/%이며 기준 0의 절대 차이와 상대 계산 불가를 구분한다.
표시 단위 미만 차이는 방향 없이 표시한다. 공유 에너지는 서비스별 합산/배분하지 않고 단계 KPI를 초별 보간하거나 인과로 꾸미지 않는다.
성공률 분자·분모·모집단, p95 표본, 공백 오차 상한(CI 아님), 출처·원값을 보존한다.

실험 실행·측정·정리는 독립 축이다. 별도 평가 운영자 권한·사전 점검 후 명시 제출·Pending 수정 제한·같은 이름 새 UID 교체,
사용 중 부하 변경/삭제 보호·원본 UID 조건·결과 미확인 원본 조회·자동 재제출 금지를 유지한다.
`자원 반환 확인 요청`은 실제 반환 관측이 아니다. 잔여 자원·사유와 실제 정리 결과를 별도로 확인한다.
주입은 단계 ID와 정확한 대상 일치 범위로만 표시하고 입력 번들은 기존 오프라인 도구가 생성한다.
폼/JSON은 같은 정의를 편집하며 미래 필드·정밀 정수와 원본 출처를 보존한다.

실험 구현의 작성 경로는 `/experiments/new`, `/experiments/[name]/edit`, `/experiments/load-scenarios/new`, `/experiments/load-scenarios/[name]/edit`다.
복사는 `copy`(실행·설정 이름) 또는 `publication`(게시 ID), 수정은 `uid`를 보존하며 새로고침에도 서버 원본을 다시 조회한다.
실행 목록은 KST 시각·단계·풀 상태·작업을, 결과 목록은 비교 상태 선행 표·독립 판정 축·차이·제한을 제공한다. 운영 판독은 `EvaluationStatusCell`을 재사용한다.
선택 실행의 URL UID가 현재 조회 UID와 다르면 `교체됨`으로 표시하고 중지·수정·재실행·자원 반환 작업을 차단한다. `현재 실행 선택`으로 명시 채택한 뒤에만 새 UID 작업을 허용한다. 풀 상태는 `PoolAcquired=True`의 점유 중 / `False`의 반환 관측 / 누락·Unknown의 미확인이며, 실행의 정리 실패 판정과 합치지 않는다.
독립적인 진행 중 실험 조회에도 선택 UID를 전달한다. 이름이 같아도 UID가 다른 활성 실행은 선택 상세에 표시하지 않으며, 중지 확인 도중 선택 UID가 바뀌면 DELETE 제출을 차단한다.

## 9. 삭제·통합·단계별 수용

별도 개요와 전용 계산, 기록/설정 링크 허브, 운영 허브를 제거한다.
후보·부하·추정·용량은 선택 서비스/판단 근거로, 장비·POOL은 장치/배치로, 서비스 관리·상세는 단일 책임으로 통합한다.
회차 기본 탭은 요청 아래로, 프로파일은 처리 성능으로, 비용/점유는 결과 종류로, 긴 작성 모달은 전용 페이지로 옮긴다.
기존 Sheet/Dialog/DataTable/ReadRegion·도메인 계산·인증·중계·라이브 공급·정의 기반 reader·변경 안전 계약은 유지한다.

| 옛 주소 | redirect 정본 |
|---|---|
| `/scheduler`, `/operations/flow` | `/` |
| `/resources`, `/pools` | `/operations/devices` |
| `/scheduler/profiles` | `/measurements/results` 처리 성능 |
| `/evaluations` | `/experiments` |
| `/evaluations/load-scenarios` | `/experiments/load-scenarios` |

기존 실험 선택 query는 결과의 같은 게시·구간, 서비스 세부 경로는 통합 상세 해당 영역으로 명시 매핑한다.
구 화면 호환 UI는 남기지 않는다. 서버 API `/scheduler`·`/experiments` 등은 화면 URL 변경과 별개다.

| 단계 | 변경 / 배포 수용 |
|---|---|
| 1 상태·표·상세 | 공통 규약·문서 확정 / null·영·빈 목록·미기록·권한·실패·결과 불명, 긴 이름·tooltip 폭·느린 사건·키보드/focus |
| 2 운영·서비스 | 운영 진입·서비스 우선·유휴 집계·장치 분리·개요 제거 / 무부하·활성·SLO 위반·전력 누락·연결 지연·변경·예약·실험, 동일 UID/시각/축 일치·관리의 WS 비의존 |
| 3 측정 | 범위·요청→회차·결과·조건 유지 / M1~M5, 분모·미측정 모드·실패/기존 사용 공존·기준 미제공·입력 변경 후 재점검 |
| 4 실험 | 실행·결과·부하·페이지 편집·비교/정리 / DB-FR-66, 정의 유무·단계 이름 변경·대상 없음/오류·UID 교체·결과 불명·권한·실제 반환 |
| 5 기록·설정·전체 | 실제 조회·원본·내보내기·구 화면 정리 / 모든 구 URL/query·뒤로가기·새로고침·선택·1440×900/390px/확대·동일 과업 재평가 |

반복 감점 수용은 SRS §9의 구조 개편 시나리오를 따른다. 평균 SUS만으로 통과하지 않는다.
야간 당직·운영 팀장·신규 SRE별 동일 과업 완료·시간·잘못된 결론·도움 요청을 따로 기록한다.
교차 검토·문서 확정은 구현·배포·접근성·SUS 상승의 증거가 아니다. 각 단계는 해당 화면을 대체해 독립 배포한다.
검사는 통합 담당자가 격리 환경에서 한 번 수행하고 운영 조회는 GET 전용이다. 실험은 reader 준비 후 publisher 활성화 순서를 지킨다.
