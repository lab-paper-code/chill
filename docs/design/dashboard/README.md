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
| 공개 진입점 | `cmd/chill-server`가 Serving과 인증된 Query API 조회를 조립. `--dashboard-url`은 API/health 외 화면·정적 자산·BFF를 private Dashboard로 중계. ingress 없는 테스트는 HTTPS NodePort 31104; chart 기본은 ClusterIP. |
| WebSocket | WSS `/api/live` → Node `server/live-proxy.mjs` :3000 → 고정 HTTPS `/api/v1/live`; 일반 HTTP는 자식 `.next/standalone/server.js` loopback :3001. Next custom server 없음; 실패/SIGTERM은 두 프로세스 종료. Upgrade의 원래 Origin/Host 보존. |
| 업무 소유권 | 인증·도메인 검증·변경 선언·진행·판단·통계·Evidence는 ChillServer 뒤 CHILL이 소유. 웹은 Kubernetes·firstfloor·S3·Node를 직접 호출하지 않음. |
| 상태 | 사용자별 탭 QueryClient의 서버 cache, 로컬 폼, URL 대상/UID/필터/기간. 신원 변경·logout은 socket/timer 종료 → 요청 취소·cache clear·QueryClient 교체·민감 폼 remount. 늦은 callback은 이전 cache에서 종료. |
| 정보 구조 | 운영·서비스·측정·실험·기록, 보조 설정. 정본 경로와 소유권은 §6. 기존 도메인 계산·API·변경 안전 계약을 유지한다. |
| 전환 상태 | 아래 구조는 승인된 목표이며 단계별 배포 수용과 구별한다. 링크 허브·일반 준비 중 화면은 부활하지 않는다. |

## 2. 인증·중계·변경 계약

| 항목 | 현행 계약 |
|---|---|
| 신원 | 기존 Kubernetes bearer를 탭 메모리와 버전·system 단위 sessionStorage에 보관한다. hydration 후 복원하며 저장 거부는 메모리 전용. 신원 변경·logout·401/4401은 저장분·요청·cache·socket을 같은 epoch로 정리한다. 403/4403·서비스 장애는 만료가 아니며 목적 URL을 유지한다. 복제 탭 토큰은 별도이고 새 창은 noopener다. BFF는 원본 Authorization 전달; ChillServer TokenReview + 정확한 verb/path SAR로 신원을 검증한다. |
| 권한 | 401 누락/무효, 403 실제 SAR 거부, 503 review 통신·권한·evaluation 오류. non-resource path는 `/chill` 접두사. Query API private leg의 서버 identity는 사용자 SAR를 대체하지 않음. |
| HTTP 제한 | method/path allowlist; Authorization·If-Match·Content-Type·추론 timeout·필요 CHILL identity header만 전달. 요청/완전한 응답 각각 8 MiB, 응답 header 16 KiB. 상태·요청 ID·Workload UID 보존, no-store. |
| 초기 웹 설정 | page 50/최대 200, 기본 구간 1시간/최대 24시간, 갱신 5초, 조회 timeout 10초/변경 60초. 추론은 사용자 timeout과 서버 상한 및 중계 여유 사용. WS 연결 수명에 HTTP 한도를 재사용하지 않음. |
| 변경 | 작성→전후 정의·제공된 영향 확인→제출→원본 조회. Model/Workload If-Match=metadata.resourceVersion. 409 최신 baseline 명시 채택 시 draft 유지, 같은 UID만 제출. 201/202/실제 완료 구분; 삭제는 원래 UID 제거 확인. 응답 미수신은 결과 미확인, 원본 조회 후 명시 재제출. |
| 설정 | 전체 SchedulerSettings 마지막 적용값과 서버 ETag가 현재 baseline이다. GET의 ETag를 PUT의 If-Match로 전달하고 409에는 초안을 유지한 채 새 baseline을 명시 채택한다. duration·seed/int64·메모리/uint64는 원문 정밀도를 보존한다. 단계별 settingsPatch도 `lib/scheduler-settings.ts`의 같은 항목 목록·타입을 재사용하며 별도 내부 제어 스키마를 만들지 않는다. |
| 설정 권한 | unbound scheduler-editor ClusterRole과 serving namespace Role을 관리자가 binding. Role은 지정 ConfigMap 하나 update만 허용. |
| 조회 | 일반 오류는 자동 retry하지 않으며 focus/reconnect refetch=false. 공통 조회·변경 경계에서만 HTTP 503이고 `code`가 정확히 `CoverageWarming`인 응답을 최대 180회 재시도한다(Retry-After 1–5초, 기본 2초). 일반 503과 응답 미수신·전송 결과 미확인 변경은 재시도하지 않는다. AbortSignal로 이동/logout 취소. 보이는 운영자 영역만 제한 polling; 라이브는 push. 401/403 payload 숨김. 독립 Query로 미지원·빈 값·실패·오래됨·동작 실패 분리. |
| 원본·안전 | RFC3339Nano·정밀 정수·null/0 구별. 외부 문자열/JSON은 텍스트, export 복사본에서 credential 제거. API code/field/reason 보존; 문장은 `lib/labels.ts`, 미등록 원본도 보존. |
| 입력 | 전처리 input/output의 이름·순서·형식·크기를 모든 모델에 동일 적용. firstfloor 고정, ONNX=ort-cpu/ort-cuda, RKNN=rknnlite(rk3588·선언 batch·FP32 rank-3 입력 하나). 도메인 검증은 서버. |
| 측정·전환 | immutable Run/campaign 요청은 기존 schema/admission/controller 사용. 프로파일은 검증된 성공 Run/campaign, Evidence는 RetainedLoader 검증 경계. 자원 제외는 실제 drain/actuator 계약. 저장 계약 변경은 ADR 0019 원칙. |
| 커버리지 현재 조건 | 이미지 버전은 프로파일 유효성 조건이 아니며 원래 기록된 이미지는 보존한다. 기존 `replaces` annotation의 대상 제외를 먼저 적용한 뒤, 셀(model/class/path/powerMode)마다 `measuredAt`이 가장 최신인 측정 하나를 현재로 선택한다. 최신 측정이 실패·partial이어도 그대로 표시하며 옛 성공으로 fallback하지 않는다. 다른 측정은 모두 history와 단건 원본에 보존한다. usable/problem·checklist 판정은 사실·경고 표시용이며 프로파일 선택이나 측정 요청 진행을 차단하지 않는다. 브라우저는 서버의 현재 조건·판정을 재판정하지 않는다. |
| 시간·성능 경계 | 브라우저 왕복=fetch→완전 본문, X-Chill-Relay-Timing=서버 대기 포함 중계 왕복, BFF 자체 비용 별도. 목표 p95 표시 2초/BFF 100ms; Workload 10·요청 10,000/1시간·동시 5·5초 갱신 기준이며 달성 증거 아님. |
| R19 측정 사용 | 채택 catalog에 존재하며 셀의 currentMeasurement identity와 일치하는 요청·profile만 사용 중이다. 최신 실패는 셀의 현재로 남되 게시 profile이 없으면 사용 중이 아니다. reuse 확인 요청과 원본 참조는 별개다. 이미지 버전은 preflight 기측정 배치 유효 조건에서도 제외한다. |
| R19 목록 밀도 | 실험 목록은 이름·정책·실행/측정·핵심 원값만 기본 표시하고 질문·UID·출처·규칙은 접는다. 이름 없는 링크는 실제 ID다. 정의 출처 표는 기록된 분위수(p90/p95/p99 등)와 목표 지연을 그대로 표시한다. 단계 적용 p95는 기록된 workloadUID·definitionID·identitySource가 동결 정의와 일치할 때만 연결한다. |
| R19 운영 사실 | audit durable commit 뒤 측정·실험 mutation의 WS action을 이력과 같은 projector로 수신한다. 오류 없는 NOT_SUBMITTED는 phase=completed여도 `사전 검사 · 미제출` 중립이며 실패·장치 점검에서 제외한다. 실제 오류는 실패로 남긴다. 비서빙 Pod 사건은 묶고 실제 서비스 문제는 남긴다. 판단 수는 현재 접속의 수신 범위이며 선택 사건→기록은 identity·기간을 유지한다. |
| R19 자료·사유 | 유휴 회차에는 처리 성능 passport를 자동 요청하지 않는다. 전력 경계·결과 제한은 한국어 코드 라벨과 원문 tooltip, J/W null은 그대로다. 최초 오류는 logTail의 첫 구체 오류 줄·기록 시각만 표시하며 다른 실패 코드 라벨·catalog 오류도 중복 제거해 보존한다. |
| R19 경량 조회 | 실험 결과·복사 목록은 offset/limit 페이지와 nextOffset을 소비한다(최대 50, 기본 25). page가 없는 구 서버 items는 전체 목록이다. 전력 bucket은 무손실 format=columns, 1시간·5초도 해상도 변경 없이 UTC bucket 경계의 최대 15분 chunk로 조회한다. 일부 chunk 실패·대기를 전체 자료 성공으로 바꾸지 않으며 columns 외 응답은 형식 불일치로 표시한다. |

## 3. 라이브 API 계약 v1

| 항목 | 계약 |
|---|---|
| 공통 | JSON 텍스트 `{type,seq,at}`. seq≥0 JS safe integer, at=원본 RFC3339Nano UTC. hello/snapshot은 head checkpoint이며 순번 생성 없음; delta/event/metrics/heartbeat는 전역 +1. hello.at=송신, snapshot.at=head 발행(0이면 hub 생성). |
| type | 공통 필드 외 payload | 발행 |
|---|---|---|
| `hello` | `protocol:"chill-live/1"`, `serverAt`, `retainFrom`(재개 가능한 최소 seq, 빈 버퍼면 head+1) | 상류 인증 성공 후 한 번 |
| `snapshot` | `workloads`, `nodes`, `instances`, `routing`, `decision`, `actions`, `reservations` 전체, 선택적 `history` 보존 메타데이터 | 최초 접속, 재동기화 |
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
  판단 snapshot의 `SchedulerReservation`(`internal/serving/scheduler.go`)에서 `target`을 뺀 것이다.
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

snapshot의 `history`는 `reason:fresh|checkpoint-unmatched`, `replayed:false`,
`requested:{afterSeq,throughSeq,at?}`, `retained:{events,power}`와 `gaps`를 보존한다.
retained 범위는 nullable `{fromSeq,toSeq}`(양끝 포함), gap 사유는 `NoRetainedFrames`다.
보존 공백은 상태어 **보존 공백**으로만 표시하고 sequence 차이를 손실 건수·시간으로
환산하지 않는다. 이 메타데이터가 있으면 구 재연결 시각 차이로 센서 공백을 만들지 않는다.
운영 재진입은 `/observations/events?from&to&limit=100`으로 최근 15분을 최대
5페이지·500건 seed한다. 동일 from/to와 불투명 cursor를 사용하고 archive/실시간 사건을
실제 identity로 중복 제거한다. archive에는 실시간 순번을 만들지 않으며 coverage 출처·공백과
미조회 페이지 여부를 유지한다. 전력은 별도의 최대 1시간·900구간 bucket 계약을 사용한다.


### 3.1 운영 판독표

기본 진입점 `/`는 운영 판독 / 서비스 상태 / 진행·사건의 세 영역이다.
서비스 영향·성능 판정·관측 나이·실험 상태를 구분하며 무부하를 성능 정상으로 답하지 않는다.
서비스 표에 실제 장치를 직접 표시하고 실제 설정된 SLO 분위수를 사용한다.
유휴만 있으면 유입·배치·접수·자동제어 집계로 압축하고 개별 유휴는 같은 표의 보기 전환으로 연다.
장치 화면은 명시적으로 선택하며 빈 장치 표로 자동 전환하지 않는다.
장치는 이름순, 서비스는 선택·읽기 중 자동 재정렬 금지다.
관측 공백·전력 누락·연결 지연을 서비스 장애와 합치지 않고 회복은 후속 유효 관측으로만 확인한다.
사건은 대상·동작·결과·제공 사유를 보이고 유지 묶음의 원본을 보존한다. 임의 작업 생애주기를 만들지 않는다.
선택 근거는 정확한 decision.at·UID·구간을 고정한다. 대기 pending/native waiting과 과거 판단 queued는 다른 집단이다.
지연 분위수 평균·결손 영 채움·공유 전력의 서비스 귀속은 금지한다.

운영 구현은 `features/live-board.tsx`의 `LiveBoardPage`, 장치는 `features/resources.tsx`의
`DevicesPage`다. 시각적인 네 축 상태 줄 없이 연결 상태와 관측 나이를 헤더에서 표시한다.
운영 변경 요약은 기존 `boardVerdict`를 사용하는 숨겨진 atomic polite live region으로 알리며,
관측 나이는 포함하지 않고 일시정지 중에는 알림을 끈다.
후보·추정·부하·용량은 서비스 Sheet의 `evidence` 선택이며 별도 운영 허브를 만들지 않는다.
장치 표는 inventory·POOL·실제 scheduler 자원과 계측 UID의 합집합을 이름순으로 보존한다.
계측 전체와 구성 범위 차이는 같은 시각·범위·완전성을 확인할 때만 산출한다.
정본 이동은 `lib/dashboard-routes.ts`의 명시적 URL/query 매핑을 공유하며 API 경로는 변경하지 않는다.

## 4. API 소유권과 연결 현황

Serving 경로 정본: [HTTP handler](../../../internal/serving/httpapi/handler.go), Query API 정본:
[read handler](../../../internal/queryapi/readapi/handler.go). BFF allowlist는 `chill-dashboard/lib/relay.ts`.
아래는 `/api/v1` 하위 경로다. 기능별 지원과 호출 권한은 별도다.
| method와 `/api/v1` 하위 경로 | 현재 제공 범위 | 웹 연결과 필요한 보완 |
|---|---|---|
| `GET, POST /models`, `GET, PUT, DELETE /models/{id}` | ModelSpec 조회와 변경, 버전 전제조건 및 참조 보호. 실행 경로는 `firstfloor`와 산출물 형식별 backend만 통과한다(`internal/modelcatalog/catalog.go:107-123`). | 등록 폼과 영향 확인에 재사용한다. 실행 방식 선택지는 같은 규칙을 따른다(U2). |
| `GET, POST /preprocessors`, `GET, PUT, DELETE /preprocessors/{id}`, 같은 형태의 `/datasets` | 전처리기의 `input.fields`와 `output.fields` 계약, 데이터셋 필드, 참조 보호 | 단계형 등록과 계약 결론 문장, 입력과 결과 화면의 필드 원본이다. |
| `GET, POST /workloads`, `GET, PUT, DELETE /workloads/{id}` | desired, appliedDefinition, supportedInputs, admission, readiness와 사유, operation | 정의, 실제 준비와 접수 및 작업을 구분한다. 준비 사유는 `lib/labels.ts` 문장으로 표시한다. |
| `POST /workloads/{id}/infer` | 단일 원시 표본 입력(Preprocessor 입력 필드의 base64 `BYTES`), 실제 응답과 요청 identity | 단일 추론에 재사용한다. |
| `GET /pools`, `GET /pools/{id}` | 실제 Node와 Workload identity, 인스턴스, 준비 및 대기와 실행 수 | 자원, 모델 경로와 전력모드 및 활성 범위의 실제 관측을 추가한다. |
| `GET /observations/workloads/{uid}` | 구간, 정의 및 outcome 필터, cursor, 전체 선택 구간 cohort와 raw 이벤트 | SLO 초과, 분포, 집계 설정과 archive 조회 계약을 보완한다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence-passport` | 검증된 Run 계보와 제한된 Evidence 요약. 현재 진행 세대에서 근거 참조가 아직 없으면 `202 EvidencePassportPending`, `run/assessment`, `measurement:null/evidence:null` | 발행 대기를 연결 장애로 안내하지 않으며 실패·오래된 세대·잘못된 게시 근거의 오류는 유지한다. |
| `GET /namespaces/{namespace}/batch-energy-optimizations/{name}/result` | 캠페인의 봉인된 결과. 파일 조회·검증 오류는 원 HTTP/code를 유지하면서 별도 `campaign/assessment`, `result:null`, `evidenceError` 제공 | 요청 실행 상태·실패 원인은 결과 파일 오류와 독립 표시한다. 오류를 성공 측정값으로 바꾸지 않는다. |
| `POST /namespaces/{namespace}/batch-energy-optimizations/preflight` | 입력을 해석하고 `resolves`, `reason`, `plan`을 반환하는 읽기 전용 요청 | 측정 요청의 검토 단계와 `/measurements/check`에 사용한다. 캠페인 접수는 별도 POST로 수행한다. |
| `GET, POST /namespaces/{namespace}/batch-energy-optimizations`, `DELETE .../{name}` | 측정 목록, `dryRun=All` 검증과 접수, `uid`와 `If-Match`의 영구 삭제 | 측정 목록, 요청과 삭제에 재사용한다. |
| `GET /scheduler`, `GET /scheduler/settings`, `GET /scheduler/state`, `PUT /scheduler/settings` | 현재 설정과 ETag, 실제 snapshot·판단·전체 설정 적용·행위자 관측 기록. PUT은 If-Match 필수, 누락 428·불일치 409 | 고정 BFF 경로와 운영 설정 편집기에 연결한다. ETag를 보존하고 초안 변경·충돌 재채택 후 다시 명시 제출한다. |
| `GET, POST /scheduler/profiles`, `DELETE /scheduler/profiles/{namespace}/{name}` | 채택 프로파일과 조건, 채택 결과와 `configVersion` | 측정 결과 처리 성능의 현재 사용·채택·해제에 재사용한다. |
| `GET /namespaces/{namespace}/operation-cost-measurements[/{name}]`, 같은 형태의 `dispatch-occupancy-measurements` | `{items:MeasurementProjection[]}` 또는 단건 안전한 읽기 projection. DOM은 최상위 진행·결과 필드를 포함한다. 원본 spec 전체가 아니다. | 두 결과 화면에 연결한다. 아래 DOM 쓰기 계약과 읽기 projection을 구별한다. |
| `POST /namespaces/{namespace}/dispatch-occupancy-measurements[?dryRun=All]`, `DELETE .../{name}?uid=…` | 대상·선택적 이름만 제출, dry-run의 완성 `task.spec`, UID·`If-Match` 영구 삭제, Measuring 409 | 요청·검사·진행·삭제 UI는 서버 DOM API 변경(`ea6899ef`) 위로 rebase되어 통합됐다. 운영 서버·Query 미배포 및 운영 생성·삭제 E2E 미수행과 구별하며 실행 중단/취소는 아니다. 상세는 [측정 API §13.8](../serving/measurement-api.md#138-네이티브-d-요청진행셀-커버리지-2026-11-03). |
| `GET /scheduler/capacity` | `{rows:CapacityRow[]}`. 자동제어 워크로드가 필요로 하는 행과 입력, κ 및 탐색 판정 | relay GET 허용됨, query 없음. 용량 근거 화면과 현재 settings 대조에 연결한다. |
| `GET /observations/load` | 명시적 구간의 수신, 접수, 배정과 완료 사건 수 및 사건률 | from/to를 필수로 전달한다. 완료는 수신 cohort와 별개의 사건 집계다. |
| `GET /observations/occupancy` | `{window:{start,end},rows:[...]}`. signed ns/request 통계·pairs·완전성 counts·profile 근거 | from/to 필수, 현재 aggregationWindowSeconds 상한 준수. 커버리지 셀 상세의 기간 조회와 runtimeImage 표시, 판정 없음 |
| `GET /observations/scheduler` | 판단, snapshot, 설정 변경, action과 전력 관측의 보관 중인 append-order 페이지 | from/to, bounded limit와 cursor를 사용한다. 응답은 페이지 항목과 nextCursor로 구성된다. relay는 이 경로와 `GET /scheduler`, `/scheduler/state`, `/scheduler/profiles`를 ChillServer `view=compact`로 읽고, 화면 파싱 전에 공유된 `profile`과 `completionMeasurement` `$ref`를 응답 dictionary로 복원해 full 응답과 같은 값을 쓴다. 응답 상한은 그대로다. |
| `GET /observations/power` | 명시적 구간의 전체 및 활성 POOL joules와 평균 W, 노드별 값, `complete`, `reasons`, 로컬 보존보다 앞선 구간의 `gapUntil` | 기록의 전력 조회와 장치 전력 범위에 연결한다. 브라우저에서 적분하지 않는다. |
| `GET /systems`, `GET /inventory` | ChillSystem의 condition과 이유, DeviceClass와 typed 전력모드 actuator, Node | System, Inventory와 Run 목록은 아래 query handler에서 제공한다. |
| `GET /experiments`, `GET /experiments/{id}` | 완료된 불변 게시·단계 KPI·출처와 선택적 `outcome`. 목록은 `definition` 대신 `comparisons`, `facts`(이름·질문·단계 row/loadScenarioRef/settingsPatchKeys·SLO 출처·부하 출처), 단건은 `definition` 제공 | §8 정의 기반 reader. 목록 항목별 단건 N+1 조회는 금지한다. 변경 키는 W4 운영 설정 항목명으로 표시하며 []와 null을 구분하고 없는 값은 추론하지 않는다. 목록에 없는 명시적 과거 비교 대상만 필요한 불변 게시 단건을 조회한다. |

결과 상세의 선택 단계와 구간 Sheet는 같은 단계 metadata의 `변경 설정` 행을 표시한다.
목록 facts가 있으면 `settingsPatchKeys`의 W4 한국어 항목명·`[]`의 `변경 없음`·null/누락의
`미기록`을 구분한다. 단건에 facts가 없으면 목록을 5개씩 실제 nextOffset으로 따라가 선택
게시 ID의 facts만 합친 뒤 렌더한다. 중간 실패·조회 중을 `미기록`으로 바꾸지 않으며 다른
게시 단건을 N+1 조회하지 않는다. 과거 definition이 현재 reader에서 거부되어도 목록 facts는
사용한다. 구형 mode/startState 계약을 다시 수용하거나 현재 운영 설정으로 보충하지 않는다.

#### 표시용 요약 읽기 계약

`GET /workloads?view=summary`는 중복 `supportedInputs`를 path·artifact·profiles `{id}`로 줄인다. 개수는 `profiles.length`를 사용하고 중복 count 필드는 보내지 않는다. 전체 조건·출처는 무쿼리/단건에서 보존한다.
`GET /namespaces/{namespace}/measurement-coverage?view=summary&summary=v2`, `GET .../batch-energy-optimizations?view=summary&summary=v2&limit=50&offset=…`, `GET /scheduler/profiles?view=summary&summary=v2&limit=50&offset=…`는 협상된 Query 표시 projection이다. BEO 공통 reader는 실제 relay 허용 목록·범위 검증을 거쳐 50개씩 끝까지 읽어 현재 셀 해석·복사 원본·동결 프로토콜·입력 출처를 보존한다. 재조회 실패에는 직전 완전한 성공 자료와 Funnel 초안·고정 이름·접수 미확인 차단을 유지하고 오류를 함께 표시한다. 인증 캐시 비노출 정책은 그대로다. 선택 프로파일과 요청 결과는 기존 단건 계약을 사용한다.

응답의 `readProjection:{version:"measurement-summary.v1",values,shapes}`에서 `~N`은 `values[N]`, `{"$o":[S,...values]}`는 `shapes[S]` 필드 순서의 객체다. dictionary는 앞선 항목만 참조하며 dictionary 최상위 문자열은 원문이다. 조건·출처·필드명·부모/배치 assessment를 한 번 보내고 참조한다. 정밀 정수·체크 결과·판정·미확인 사유·null·0·페이지는 복원 후 동일하다. 커버리지의 미사용 `entities/groups` 역색인, 요청의 `metadata.managedFields/annotations`, 프로파일의 미사용 `preparations`만 요약에서 제외한다. 커버리지 집계와 셀별 요청·Run·프로파일 관계는 유지한다. 무쿼리 응답, 저장·CRD, 판정/체크 생성은 변경하지 않는다.
`summary=v2` 명시 때만 projection 버전이 `measurement-summary.v2`이며, 경로 문자열을 `{"$s":["~N",...]}`로 앞선 원문 문자열 요소의 `/` 연결로 복원한다. 플래그 없는 v1에서는 `$s`는 일반 객체다. 빈 요소·Unicode·`~N` 원문과 원본 `$s` 객체를 보존한다. 요소 참조는 객체·배열·연결 문자열을 가리킬 수 없고, 연결 문자열 총량은 응답당 8Mi UTF-16 단위 이하다. 신 디코더는 v1과 v2를 모두 지원한다.
디코더는 dictionary 항목별 논리적 노드 수·깊이를 메모하고 각 항목과 최종 응답의 상한을 2,000,000노드·깊이 64로 제한한다. 반복 참조는 매번 합산하며 인라인 과도한 중첩도 재귀 진입 전에 거부한다. 저장된 운영 GET 8개를 Go projection에 적용한 최대 확장 크기는 커버리지 230,362노드/깊이 11, 측정 요청 180,000/9, 프로파일 149,331/11이다.

2026-11-02 당시 로컬 projection 크기(B)는 workloads 17,227,584→422,048(29개), 커버리지 9,218,406→1,893,956, 측정 요청 11,941,797→1,677,154, 프로파일 `limit=50&offset=100&sort=model&order=asc` 8,550,682→921,387로 당시 2,000,000 B 상한을 만족했다.

control-323 운영 GET(2026-11-03)은 BEO 1,785,760 B, coverage 1,210,905 B였다. 같은 원자료의 협상된 v2 로컬 인코더는 BEO 첫 50개 250,190 B, 최대 페이지 294,117 B, 전체 598셀 coverage 887,602 B다. 전체 BEO 재압축만으로는 1,344,715 B여서 페이지도 필요하다. `CHILL_MEASUREMENT_FIXTURE_DIR` 회귀는 coverage/profiles 및 모든 50개 페이지에 1,000,000 B 상한과 유지 필드 round-trip을 검사한다. 요청은 12회→6회이며 로컬 순수 페이지 인코딩 합계 1,683.561ms(단일 측정, 서버 조회·네트워크 제외)다.

구 대시보드/열린 탭은 신 Query에서도 플래그 없는 v1·무페이지 전체 읽기를 받는다. 신 대시보드는 구 Query의 400 협상 거절에서만 v1로 재시도하고 BEO는 기존 전체 읽기를 사용한다. 중간 페이지 다운그레이드에도 중복 합치지 않는다. 권장 배포 순서는 Query→Dashboard이며, 역순도 자료 손상은 없지만 구 Query 기간에는 기존 1MB 초과 응답이 유지된다. 양쪽 갱신 후 1MB 목표를 적용한다. 혼합 배포 회귀·occupancy 분해·페이지별 시간은 [measurement API](../serving/measurement-api.md#control-323-최초-조회-크기-보정)에 기록한다.

DOM은 고정 동시 송신자로 실행하는 짧은 closed-loop burst다. 프로토콜 표는 실제 동결 spec의 배치·송신자·예열·측정 기간·minimumPairs를 표시하며 누락값은 미제공이다. v1alpha2에 없는 saturationSizing·NotSaturated·유휴/포화 항목은 진행 화면에서 제거한다. legacy 원본은 disclosure에 보존하되 새 정책으로 재산정하지 않는다.

DOM `occupancy[].adopted`는 실제 scheduler 선택 `accepted.dispatchOccupancy[].provenance`에 연결된 tri-state다. Node UID·모델·전력모드·실행 경로·배치·profileID의 일치와 실제 선택 출처를 사용하며 `accepted.occupancy`의 legacy scalar 목록만으로 채택을 추론하지 않는다. 새 선택 목록이 없거나 카탈로그 오류이면 `null`=채택 미확인이다. runtimeImage는 유효성 키가 아니며 catalog checkedAt/error는 기존 freshness에 반영한다. 코드 통합은 운영 배포·운영 mutation 검증을 뜻하지 않는다.

셀 상세는 **스케줄러 d**(출처 측정/DOM/기본값·선택값·pairs·window·갱신 시각)와 **관측 d(B)**(signed median·IQR·p10/p90·negativeFraction·pairs)를 독립 표로 표시한다. 관측 통계는 `accepted.occupancyEstimates`에서 읽으며 채택값으로 대체하거나 판정을 추가하지 않는다. 별도 기간 조회 `GET /observations/occupancy?from=&to=`의 기본 구간은 실제 스케줄러 d의 provenance.window이며 상한 숫자를 만들지 않는다. 기간 변경이 400이면 서버 code·message를 표시하고 직전 적용 기간을 유지한다. 400 본문에 양의 정수 `maximumWindowSeconds`가 있으면 이후 선택의 상한으로 기억해 제한·안내하며, 없으면 사유만 표시한다. 응답 행은 Node UID·모델·전력모드·실행 경로·배치·profileID로 해당 셀에 연결하며 runtimeImage는 표시만 한다. null과 측정된 0, 빈 행과 조회 실패를 구별한다. relay는 이 경로의 GET과 from/to만 허용한다.

기간의 선후·상한은 relay와 공유하는 나노초 정밀 RFC3339 파서로 비교하며 URL에는 입력 원문을 유지한다. 정상 Observed의 `d:null`은 “결과 없음” 사유가 아니다. direct 상세는 통계와 별도로 실제 실행·근거 오류를 표시하되 포화/품질 판정을 복원하지 않는다. 일치하는 선택 행에 provenance가 없으면 채택은 `null`이며 확인된 빈 선택·명시 default만 `false`다.

### 연결 예정 API (서버 책임)

|---|---|---|
| `GET /systems`의 `capabilities` | 기능별 `{name, available, reason}` | System 조회는 구현됐다. `capabilities` 필드는 ChillServer 조회 대기다. |
| `GET /profiling/options` | 모델과 경로, 장비와 Node, 전력모드, 입력 및 측정과 관측 정책의 선택 항목과 sourceRef | 기존 선언과 configured fact resolver를 사용하는 Query API 조회가 필요하다. |
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

[측정 공통 projection](../../../internal/serving/httpapi/measurement_projections.go),
[DOM 진행 필드](../../../internal/queryapi/dispatch_occupancy.go),
[DOM evidence projection](../../../cmd/chill-query/measurement_entity_assessment.go),
`internal/serving/capacity.go`, `httpapi/profiles.go:schedulerCapacity`가 wire 필드의 권위다.
두 측정 목록은 `{items:MeasurementProjection[]}`, 단건은 projection 자체, 용량은 `{rows:CapacityRow[]}`.
이 문단의 읽기 projection은 GET·body/API query 없음이다. UI namespace/uid/workload/row query를 upstream에 전달하지 않는다. DOM POST/DELETE는 위 별도 쓰기 계약을 사용한다.
측정 uid 불일치는 다른 객체로 차단, uid 없는 이름 참조는 현재 객체다. namespace별 응답만 클라이언트 정렬/페이지 처리한다.

| 공통 표시 | 정확한 소스 필드 | 표시 규칙 |
|---|---|---|
| 목록 identity·대상·상태·시각 | `metadata.name`, `metadata.namespace`, `metadata.uid`, `node`; DOM `phase`, `createdAt`, 변경 비용 `status.phase`, `metadata.creationTimestamp` | 이름/Node, 상태 문장·기호, KST 시각(UTC tooltip). DOM `phase:null`은 대기이며 구 status로 복원하지 않는다. UID는 상세 원본 identity에 표시 |
| 상세 결론·사유 | `kind`; DOM `phase`, `reason`, `assessment`, 변경 비용 `status.phase`, `status.reason` | 한국어 요약을 주표기하고 제공된 reason 원문을 펼침 안에 보존. 임의 원인을 확정하지 않음 |
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
| 배치·동시 송신자 | `conditions.batch`, `.workers` | 건/batch, workers=명. 공급 방식은 운영 정책 설정 축이 아니며 결과 조건 표에는 반복 표시하지 않는다 |
| 예열·측정 구간 | `conditions.warmupSeconds`, `.durationSeconds` | s, 예열은 측정 시작에서 제외 |
| 결과 전력모드 | `status.powerMode` | 없으면 “결과 없음” |
| 요청당 분배 점유시간 d | `d.perRequestNanos` | signed 정밀 정수를 표시할 때만 ms/request로 변환한다. `d:null`과 측정된 0을 구별하고 `status`로 복원하거나 음수를 clamp하지 않는다. 정확 ns는 원본/내보내기에 보존 |
| legacy 처리량 X·유휴 비율·판정 | legacy `d.throughput`, `d.idleFraction`, `d.verdict` | legacy evidence 원값만 보존. v1alpha2 burst의 scalar `d`와 유휴/포화 값은 null이며 관측 estimate의 signed 통계·pairs를 별도 표시한다 |
| 측정창·경과 | `measurementWindow.{start,end}`, `createdAt`, `phase` | 검증된 evidence의 실제 측정창만 시작·종료로 쓴다. 대기/측정 중 경과는 생성 시각부터 표시할 수 있으나 예정 측정창·진행률은 만들지 않는다 |
| 결과 위치 | `evidence.{store,key,size}`, `references.evidence[]` | 논리 위치 그대로 보존. d/측정창 검증 실패를 원본 native ref 손실로 바꾸지 않는다 |

DOM 목록·단건은 `phase:null/Measuring` 동안 보이는 탭에서만 제한 polling한다. 종료 phase와 401/403에서 중단하며 삭제된 단건의 404도 중단한다. 삭제는 확인 후 UID query와 `If-Match:resourceVersion`을 보내고 Measuring은 비활성화한다. 409는 최신 상태 조회, 응답 미수신은 결과 미확인으로 재삭제를 차단한다.

커버리지의 `cells[].occupancy[]`는 실제 Node UID × profileID × batch tuple을 포함한다. 요청 진행의 `state`, `request`, `phase`, `reason`, `d`, `evidence`와 실제 선택의 `adopted`, 스케줄러 d provenance, 관측 d(B)를 구별한다. 빈 배열에서 가짜 tuple을 만들지 않는다. 상태 우선순위는 present > measuring > failed > none, 동률은 생성 시각 최신이다. 이전 profile은 `none/ProfileSuperseded`, Complete의 근거 검증 실패는 `failed/EvidenceInvalid`, 새 카탈로그 계약 부재는 `adopted:null`이다. 새 burst는 scalar 부재만으로 실패 처리하지 않는다. 셀의 d 요약은 접근 가능한 이름에도 포함한다.

셀은 occupancy 유무와 무관하게 조건을 보존한 d 요청 링크를 제공한다. 호환성 대표 `support.node`를 측정 Node로 추측하지 않으며 요청 화면이 해석한 BEO의 `spec.state.targetNode`를 사용한다. 요청 이름은 선택적이고 검사 identity에 포함된다. 같은 조건 재측정은 새 이름으로 생성해 이전 실패 기록을 보존한다. 측정 종류 선택은 profile 의존 조회 오류와 독립적으로 접근 가능하되 변경 진행·결과 미확인 중에는 전환하지 않는다.

서버 통합 검증(2026-11-03): 실패-먼저 회귀 후 Node 24.14.0 typecheck/eslint/운영 summary 포함 테스트 500개/기본 build가 통과했다. ea6899ef 위 rebase는 충돌 없었고 Go focused Query/Server/HTTP/Kube 테스트, make lint·arch-check·test를 재실행해 통과했다. 로컬 TLS의 실제 handler·builder·service 경로에서 dry-run 200 NOT_SUBMITTED→생성 202 Accepted·동결 spec 동일성과 삭제 fence/Measuring 거절 테스트가 통과했다(인메모리 Kubernetes fixture이며 실제 Job E2E 아님). 로컬 production+운영 GET의 1440×900 렌더에서는 대표 Node를 생략한 조건 링크가 BEO 대상 genesis3로 해석됐고 API 200·콘솔 오류/가로 넘침 없음(Next prefetch abort만 관측)을 확인했다. 운영 Query는 phase/d 미배포라 결과 대기·미제공이며 새 진행값 검증이 아니다. 운영 mutation은 없었고 로컬 클러스터에는 DOM CRD·BEO가 없어 실제 Job 진행 E2E는 남는다. 서버·Query·대시보드는 함께 cutover한다.

서버 리뷰 수정 `392388ff` 반영 후 `dispatchOccupancy.supplyMode` 정책 설정은 없으며 native spec의 closed-loop 고정만 유지한다. 요청 프로토콜 표는 실제 `spec.supplyMode:"closed-loop"` 응답이 있을 때만 고정 사실을 표시하고 누락 시 추정하지 않는다. 접수/삭제 API의 `Accepted`·`DeletionRequested`와 live action `phase:"completed"`는 서로 다른 축이다. 후자는 작업 접수/삭제 처리 완료이며 DOM 측정의 `phase:"Complete"`를 뜻하지 않는다.

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
| d | ns 정밀 정수 보존, 주표기 ms/request 소수 둘째 자리·원본 ns 병기. 누락≠0, X는 문자열 req/s 원값. d·κ 브라우저 재계산 금지. 공급 방식 원자료는 원본 응답에 보존 |
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
| `/operations/devices` 장치 | 어디에 무엇이 배치됐는가? 장치별 배치 우선·전체 전력 이력 접힘 → 사양·모드·자원 역할 |
| `/workloads` 서비스 | 어떤 정의가 적용되는가? 목록 관리 표; 상세 적용 상태·정의/설정·배치/연결 |
| `/measurements` 범위 | 조건·집계와 선택 모델 행렬. 지원 안 함 조합은 행렬에 보존하고 지원 범위 집계 분모에서는 제외. 서버 기준 시각·신선도·갱신 상태 표시, 목표 미제공 시 진행률 없음. 셀 Sheet는 상태 축→배치 결과→출처·작업·원본 순서 |
| `/measurements/requests` 요청 | 부모 요청 선택 Sheet에 자식 회차·실패 원인·점검·기존 사용 결과. 회차 복귀 링크는 부모 UID 또는 검색 필터·페이지를 보존. `?view=runs` 전체 회차 검색, `?run=…&uid=…` 직접 선택. 작성 `/measurements/requests/new` 대상→조건→점검·제출 |
| `/measurements/results?kind=throughput\|operation-cost\|dispatch-occupancy` 결과 | 종류 전환 틀 하나, API·통계 의미는 독립. 현재 사용/품질 분리, 변경 비용 준비·제거별 행과 실제 통계 열. `profile` 또는 `measurement`·namespace·UID 선택 보존 |
| `/experiments` 실행 목록 | 무엇이 실행·정리 중인가? 목록 보기·작업과 실행 표. 행은 `/experiments/runs/[name]?uid=…` 상세로 이동하며 상세는 상태·허용 작업 / 단계 진행·주입 / 조건·풀 상태의 세 영역 |
| `/experiments?view=published&offset=…` 게시 목록 / `/experiments/results?a=…` 게시 상세 | 목록은 현재 범위·페이지와 단일 게시 행, 상세는 비교 맥락 / 요청률·서비스 p95·풀 에너지 공유 차트 / 선택 단계 원값·차이. 조건·출처는 접고 구간은 Sheet로 조회 |
| `/experiments/load-scenarios` 부하 설정 | 어떤 부하 정의를 사용하는가? 검색/표; 상세 정의·구간 미리보기·사용 중 실행 |
| `/records` 기록 | 선택 시점에 무엇이 있었는가? 판단·변경 / 서비스 요청 / 전력의 조회 범위·기록·선택 근거 |
| `/settings` 설정 | 어떤 값·카탈로그가 적용되는가? 운영 설정 / 모델 / 입력 처리 / 데이터셋의 선택·현재값·연결/영향 |

설정 구현은 `features/settings.tsx`에서 `tab=configuration|models|preprocessors|datasets`를 선택한다.
운영 설정은 범위 선택·현재값 표·연결 작업의 세 영역과 값 수정 Sheet를 사용한다. 기본은 `section=targets`이며 `section=targets|cycle|limit|controls|capacity|advanced`, `edit=<설정 key>|controls.<key>`를 URL에 보존한다. 선택 열기는 push, 닫기는 replace다. 서비스·장비 범위에는 CHILL 자동 운영 서비스(`autonomousWorkloads`), 최소 수요(`minimumDemand`), 계획 제외 장비(`excludedNodes`)를 접히지 않은 편집 항목으로 둔다. 최소 수요는 서비스명·req/s 소형 표이며 미설정과 명시적 0을 구분한다.
서비스 편집 Sheet는 첫 입력 전 새 GET을 반영하고 첫 입력 시 표시한 설정·ETag를 함께 고정한다. 검토·적용 직전 재조회에서 설정 또는 버전이 다르면 작성값을 보존한 충돌 비교로 이동한다. W1 GET settings의 `ETag: "<ConfigMap resourceVersion>"`를 PUT `If-Match`로 그대로 보내고 성공 응답의 새 ETag로 갱신한다. ConfigMap 전체 버전이므로 다른 선언 변경에도 충돌할 수 있다. 409 `SettingsConflict`는 재조회·충돌 비교, 428 `SettingsPreconditionRequired`는 `버전 없음`·명시적 재조회이며 자동 재제출하지 않는다. 구 서버에 ETag가 없으면 If-Match를 보내지 않고 동시 변경 한계 도움말을 한 곳에 표시한다. ETag가 있으면 그 한계 설명은 숨긴다. GET의 읽기 전용 `staticRouting`·`powerObservation`·`meters`·`requestTimeout`은 PUT 본문에 포함하지 않는다.
요청 분배는 서비스×인스턴스×int32 가중치 표와 접힌 원문을 함께 편집한다. 새 요청 차단은 빈 객체, 기본 분배 복귀는 서비스 제거이며 순차 적용·첫 실패 중단 계약은 바꾸지 않는다.
카탈로그는 검색 가능한 표에서 모델 파일·실행 방식 수, 입력 처리 대상 모델 수, 데이터셋 필드·연결 서비스 수를 우선 제공한다. 보관 주소·해시는 접힌 원문에 보존하며 이름·UID 고정 Sheet에서 정의·영향·수정/삭제/원본을 제공한다.
설정의 제외 장비는 관측된 이름 선택과 미등록 기존 이름 편집을 함께 제공한다. 분배 조회 실패는 오래된 값과 별도로 HTTP·원본 코드를 표시한다. 카탈로그 상세는 형식·처리 방식 제목, 중복 없는 공통 이미지 참조와 대상별 조건, 실제 배치·대기 한도 표를 제공한다.
`/models`, `/preprocessors`, `/datasets`, `/configuration`의 최상위 목록은 설정 탭으로 이동하며 등록·상세·편집 경로는 유지한다.
`Screen`의 설명 문단 API는 제거하며 입력 정의와 변경 제약은 명시적인 도움말로 제공한다.

각 사실은 정본 한 곳만 소유한다. 연관 화면은 같은 KPI 묶음을 복제하지 않고 대상·기간·namespace·UID를 보존한 링크를 제공한다.
서비스 상세의 `현재 관측`은 운영으로 연결하며 접수 허용을 실제 유입으로 번역하지 않는다.
동일 축의 값은 같은 UID·시각일 때만 공통 계산 결과를 공유한다. 서비스 관리는 WebSocket 없이 관리 API로 동작한다.
서비스 구현은 `features/management.tsx`의 목록 관리 표와 통합 상세다. 상세는 적용 상태·정의/설정·배치/연결 세 영역이며, `features/service-hub.tsx`는 관리 API 조회·정의·배치 표시만 소유한다. 옛 세부 화면 export는 제거하고 세부 경로는 통합 영역으로 이동한다. 등록·수정·삭제의 UID/버전·충돌·결과 미확인 계약은 유지한다. 서비스 화면의 설정 PUT은 제거하며 CHILL 자동 운영 값은 `/settings?section=targets&edit=autonomousWorkloads&workload=<UID>`로 이동한다. 운영 보드·목록·상세·필터는 `CHILL 자동 운영`·`변경 작업 진행`을 공유한다. 진행 중 변경 작업이 없으면 목록 진행 열을 숨기고 혼합 목록의 비진행 값은 흐리게 표시한다. 접수 허용·바로가기·진행 열 렌더 회귀는 `lib/service-ui.test.ts`가 확인한다.
서비스 분배 표의 장치·인스턴스·준비·가중치는 동일한 scheduler snapshot에서 읽고 서비스 UID/라우팅 관계로 제한한다. 가중치 영은 보존하며 미제공은 별도로 표시한다. `/pools`의 배치 관측은 자체 관측 시각이 있는 상세로 유지하고 분배 snapshot과 시각 일치를 요구하거나 서로의 행을 결합하지 않는다.
측정 범위는 전체 모델 요약과 선택 행렬을 적층하지 않는다. ‘측정 있음’·‘문제’ 집계는 셀과 동일한 상태 함수를 사용하며 실패 요청 총수가 아니다. 지원 안 함은 지원 조합 분모에서 제외하되 제외 수를 별도로 표시한다. 현재 사용은 스케줄러 참조, 품질은 독립 판정이다. 조합/배치 분모와 미측정·미확인을 보존한다.
목표 배치 미제공은 범위 집계에서 한 번 표시하고 해당 셀에 진행률을 만들지 않는다. 활성 필터·개별/전체 해제는 접힘 밖에 둔다. 셀 열기는 push로 이력을 남기고 상세 필터는 표를 밀어내지 않는 패널이다. `DataTable`의 `urlState`를 명시한 주 목록만 `page/limit/sort/order/search`를 URL로 복원한다. 상세·편집 표와 자식 회차는 독립 로컬 상태이며 좁은 폭에서는 모든 열을 라벨/값 행으로 재배치한다.
요청 상태는 결과 파일 조회 오류와 독립 제공한다. 같은 결과 ID 또는 프로파일·배치의 현재 사용 결과는 한 표로 합치고, 다른 보관 프로파일은 이 요청의 결과와 별개라는 관계를 표시한다. 회차의 성능·전력·표본 수는 조건보다 먼저 표로 제공한다. 결과 종류의 API·통계는 합치지 않으며 변경 비용 준비·제거를 각각 행으로, 실패 분배 수치는 경고 아이콘과 흐린 미검증 원자료로 표시한다.
처리 성능 목록은 실제 첫 배치를 대표값으로 명시하고 전체 배치 표를 접어 제공한다. 변경 비용은 실제 중앙값–최대 범위와 원수치 표를 함께 유지하며 구조적으로 모델이 없는 전력모드 동작은 해당 없음이다. 경로·부하·사유 코드는 한국어 요약과 원문을 분리한다.
진행 요청의 완료 회차·현재 phase와 셀의 정확히 연결된 조회 회차만 표시한다. 예정 총수·세부 실행 단계가 계약에 없으면 미제공이며 ETA를 생성하지 않는다. 진행 근거의 202 대기는 명시 재조회와 상단 갱신으로 확인한다. 긴 Run 이름은 원문에 남기고 고정 제목으로 성능 표를 우선한다.
이름만 있는 복사 진입도 관측한 namespace·UID를 고정하고 검토에서 출처를 표시하며 교체된 동명 객체는 검사·제출을 차단한다. 실제 측정·기록 창은 표와 CSS 길이 비교로 보이고 예열 횟수를 초로 만들지 않는다. `/measurements/requests/check`는 저장 계획 파일의 검사 전용이며 새 요청 작성과 구분한다.
측정 요청 복사의 Workload 이름은 현재 선택 목록에도 있을 때만 선택값으로 복원한다. 삭제된 이름은 빈 선택으로 남기고 기존 필수 Workload 검증으로 진행을 막는다.
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
평가는 CHILL 서빙 파이프라인을 외부 블랙박스로 평가한다. CHILL의 profile·d·κ·배치·전력모드·인스턴스는 상시 자율 운영 대상이고, 실험은 워크로드·절대 rate·도착 분포·기간·seed·payload를 제공하여 SLO·에너지·요청 결과·생성기 품질·에너지 커버리지를 관찰한다. 내비게이션과 헤더는 `운영 / CHILL 자율 제어`와 `실험 / 외부 평가`를 라벨로 구분한다.
작성·부하의 구조화 폼에는 내부 κ/d/rateBasis/alpha, 시작 상태 복원·스냅샷·이미지/profile 대조·CHILL 제어·장애 주입 입력을 두지 않는다. W1 서버와 같은 cutover이며 옛 LoadScenario/EvaluationRun 객체는 삭제하고 이주·호환 입력을 제공하지 않는다. 부하는 절대 req/s, 워크로드별 uint64 십진 **문자열** `seeds`, Dataset `{name,uid}`의 `datasetRefs`, 선택적 `traceRefs`를 쓴다. 과거 배치·전력·인스턴스 변화와 사건은 결과 Sheet의 `관찰`로 읽고 편집하지 않는다.
실험 전역·단계 `settingsPatch`는 공개 운영 설정 항목을 선택해서 작성하는 불투명 JSON 객체다. 운영 설정 화면과 같은 항목 정의를 사용하며 최종 값 검증은 서버가 한다. 단계의 생략·null은 Run patch 상속, `{}`는 빈 전체 대체다. 부하 발생기의 `nodeName`을 명시하고 image/source는 서버 템플릿·보존 정의에서 읽으며 대상 이미지 대조 입력으로 쓰지 않는다.
요청 시각 입력 조회는 `kind=trace`만 사용하고 `set`은 해당 부하 시나리오 이름이다. seed·payload를 evidence 종류로 조회하지 않는다.
사전점검의 `liveTarget[]`는 `{component,resource,liveImageIDs,ready}` 관측 표다. digest 채택·`specImage`·`match`는 없고 변경된 live image ID는 사용자 입력을 바꾸지 않는다. 접수 서버가 `edge.dacs.io/evaluation-live-target` annotation에 관측을 기록한다. 실행 상세의 `평가 대상 CHILL` 표는 EvaluationView.liveTarget의 기록만 표시하고, 없거나 손상된 annotation의 null은 `미기록`이다.
External-target readiness and observed image IDs cover only non-deleting, non-terminal Pods selected by the target resource; Succeeded/Failed replicas retained from earlier rollouts are excluded. At least one eligible Pod must remain, and every eligible Pod must be Ready. Evaluation preflight and creation share this rule; HTTP 200 from preflight returns advisory checks and does not imply every check passed.
게시 목록의 `comparisons`, 단건 `definition.spec.comparisons`만 비교 관계로 사용하며 단계는 ID로 연결한다.
suffix·요청률 유사성 fallback은 사용하지 않는다. 정책은 기록 정의, SLO는 기록 정의·실행 결과에서만 읽고 현재 Workload로 소급하지 않는다.
과거 대상은 publicationID·stageID, 서비스는 workloadUID로 연결한다. 정의 없는 과거 게시의 비교는 미기록이다.
결과 화면을 포함해 목록 항목별 단건 전수 조회는 하지 않는다. 이름·질문·단계 row·SLO·부하 출처와 `settingsPatchKeys`는 목록 `facts`만 사용한다. 서버가 Run patch 상속을 적용한 변경 키 경로는 운영 설정 항목 정의로 한국어화하고 모르는 키는 원문 그대로 표시한다. `[]`는 `변경 없음`, null·누락은 `미기록`이다. Control/Mode 정책을 추론하거나 요약에서 가짜 EvaluationDefinition을 재구성하지 않는다. 명시적 과거 비교 대상이 목록에 없을 때만 필요한 게시 단건을 캐시 조회한다.
측정 규칙은 실제 게시의 실행기 소스 버전·전력 결손 비율 상한(%)·단일 전력 공백 상한(s)·근거 출처·GC 집계 방식을 한국어 항목/값/단위 표로 표시하고 미등록 규칙과 원문은 게시 JSON에 보존한다. 구간 Sheet의 정책은 단계 요약에 한 번, SLO는 서비스 성능 표에 한 번 표시하며 공유 서비스 정의 표는 식별자·정의만 담는다.
목록 facts는 단계별 변경 키를 담고 설정 값은 담지 않는다. 단건의 동결 settingsPatch와
부하·SLO 출처는 이름/UID별 한국어 행으로 표시한다. 과거 원문은 변경하지 않으며
SLO nanoseconds는 number와 정밀 bigint 모두 공통 시간 formatter로 표시한다.

| 비교 조건 | 표시 |
|---|---|
| 명시 관계·조건 확인 | 동일 조건 비교·원값·차이 |
| 관계 있으나 조건 다름/일부 미확인 | 참고 비교·제한·원값, 동일 조건 성과 집계와 분리 |
| 정의/관계 없음 | 단계 절대값·비교 미기록 |
| 대상 조회 오류 | 대상 조회 실패·다시 조회 |
| 실제 게시/단계 없음 | 대상 게시 없음 |

상단에는 비교 상태를 한 번 표시하고 제한 원인·일치/참고/대응 없음 건수는 접힌 비교 근거 표에 보존한다. 임의 종합 우열 판정은 만들지 않는다. 단독 게시에는 `비교 없음`과 실측값을 표시하며 빈 차이 열·비교 범례를 만들지 않는다.
서비스 선택은 표·차트를 함께 바꾸고 모든 서비스에 접근 가능하다. 게시 상세는 차트 → 선택 원값 → 접힌 근거, 구간 Sheet는 단계·실제 기간·원값 → 같은 기간 사건 → 접힌 조건·규칙·출처·원문 순서다. 요청률/p95/풀 에너지 레인은 공유 구간 선택을 사용하며 1440×900 첫 화면에서 규칙보다 앞에 배치한다.
차이는 후보−기준, 성공률 차이는 %p, p95는 후보÷기준 배율, 에너지는 J/%이며 기준 0의 원값과 상대 계산 불가를 구분한다.
표시 단위 미만 차이는 방향 없이 표시한다. 공유 에너지는 서비스별 합산/배분하지 않고 단계 KPI를 초별 보간하거나 인과로 꾸미지 않는다.
성공률 분자·분모·모집단, p95 표본, 공백 오차 상한(CI 아님), 출처·원값을 보존한다.

정의·비교가 없는 게시도 목록의 게시 행에서 상세로 이동해 단계별 구간·길이·공유 에너지 및 서비스별 성공률·p95 원값을 읽을 수 있다. 목록에 단계·서비스 중첩 표를 만들지 않는다.
하위 `substages` 기록이 있으면 계획 표식은 `계획 오프셋 · 차트 실측은 단계 집계`로 표시한다. 기록이 없을 때만 `구간 실측 미제공`이며, 하위 구간 에너지·절대시간 경계를 새로 추정하지 않는다.
발행 요청률은 게시된 offered / 실제 단계 구간 길이이며 계획 요청률이나 현재 유입이 아니다.
서비스 선택 시 계획 요청률도 같은 서비스의 구간 길이 가중 평균으로 좁힌다. 서비스가 기록되지 않은 구간은 0으로 만들지 않는다.
계획·비교 대상·SLO가 없으면 해당 차트 계열과 범례를 만들지 않고, 이름으로 정책·비교·같은 부하를 복원하지 않는다.

실험 실행·측정·정리는 독립 축이다. 별도 평가 운영자 권한·사전 점검 후 명시 제출·Pending 수정 제한·같은 이름 새 UID 교체,
사용 중 부하 변경/삭제 보호·원본 UID 조건·결과 미확인 원본 조회·자동 재제출 금지를 유지한다.
정의 디코딩 실패·준비 실패·단계 0개 여부와 무관하게 게시된 outcome의 실행·측정·정리 축을 목록과 상세에 유지한다.
권한 조회 API가 없으므로 쓰기 버튼에는 `필요 권한: 평가 운영자 / 현재 권한: 미확인`을 표시하고 실제 403만 권한 없음으로 판정한다.
`자원 반환 확인 요청`은 실제 반환 관측이 아니다. 잔여 자원·사유와 실제 정리 결과를 별도로 확인한다.
과거 주입 결과는 단계 ID와 정확한 대상 일치 범위의 관찰로만 표시하고 입력 번들은 기존 오프라인 도구가 생성한다.
폼은 현행 외부 평가 필드를 패치하고 JSON은 정밀 정수와 출처를 보존한다. 복사에서는 W1 서버에서 삭제된 알려진 필드만 제외하며 `복사에서 제외됨` 목록을 명시한다. 원본 게시·현재 편집 객체는 바꾸지 않고, 지원되는 확장 필드를 임의로 버리거나 누락값·복원 상태·대상 digest를 생성하지 않는다.
작성 도움말은 기존 AuthorHelp 한 곳이다. 패치의 *Nanos 항목도 운영 설정과 같은 ms 입력·정밀 ns 변환을 사용하며, 원문의 비정수·비유한 시간값은 선택 시 값 칸을 비우고 검증 오류로 표시한다. 이전 항목 값은 재사용하지 않는다. 단계의 상속과 빈 전체 대체를 상태어로 구분한다. 입력 누락은 출처표 밖의 요약 표, 복사 질문·입력 저장 위치·정밀 크기는 원문 접기로 분리한다.
입력 API에 사람용 표시명이 없으면 목적·항목 번호만 UI 표식으로 쓰며 의미 있는 이름을 지어내지 않는다.
정의 있는 비교 표도 390px에서는 서비스별 라벨·값으로 재배치하고 비교 제한·건수는 접어 첫 화면의 세 업무 영역을 유지한다.

실험 작성 경로는 `/experiments/new`, `/experiments/runs/[name]/edit?uid=…`, `/experiments/load-scenarios/new`, `/experiments/load-scenarios/[name]/edit?uid=…`다. 구 `/experiments/[name]/edit`는 canonical 실행 편집 경로로 이동하며 `/experiments/runs/[name]`은 항상 실행 상세다.
복사는 `copy`(실행·설정 이름) 또는 `publication`(게시 ID), 수정은 `uid`를 보존한다. 미저장 폼·잘못된 JSON 편집은 `beforeunload` 경고를 연결하며 새로고침을 계속하면 서버 원본을 다시 조회한다. 작성 중 새 부하는 같은 LoadEditor의 Sheet를 재사용한다. Sheet를 닫으면 그 세션의 지연 저장 콜백은 부모 초안을 바꾸지 못하고, 유효한 완료만 최신 초안의 해당 부하 참조를 갱신한다.
단계 편집기 수명은 배열 위치가 아니라 대상 단계 identity로 구분한다. 앞 단계 삭제·이동 후 이전 설정 항목·입력값이 다음 단계에 적용되지 않는다. 데이터셋 참조는 선택기에서 이름·UID를 함께 고정하며 수동 수정은 JSON 탭에 둔다.
실행 목록은 KST 시각·단계·풀 상태를 제공하고 다시 실행·삭제는 UID가 확인된 하위 상세 작업에만 둔다. 게시 목록은 이름·기간·정책·독립 판정 축·비교 상태의 단일 행이며 offset·nextOffset을 보존하고 행별 단건 hydration은 하지 않는다. 운영 판독은 `EvaluationStatusCell`을 재사용한다.
게시 상세의 `a/al/as/b/bs/service`는 공유 선택 identity다. Sheet가 닫혀 있어도 비교쌍 변경은 URL에 저장하며, 닫기는 identity를 제거하지 않고 `sheet=closed`로 개폐만 표시한다. 구간을 다시 열면 이 표식을 제거한다. 표식 없는 기존 구간 딥링크는 Sheet를 열고, 닫은 뒤에도 같은 구간의 수치·서비스·offset을 유지한다. 구간 열기는 push, 내부 선택 변경·닫기는 replace다.
게시→진행 링크도 현재 결과 query를 복사하고 실행 UID만 갱신한다. 왕복 후 비교쌍·비기본 구간·Sheet 개폐·관찰 대상·서비스·offset이 유지된다.
선택 실행의 URL UID가 현재 조회 UID와 다르면 `교체됨`으로 표시하고 중지·수정·재실행·자원 반환 작업을 차단한다. `현재 실행 선택`으로 명시 채택한 뒤에만 새 UID 작업을 허용한다. 풀 상태는 `PoolAcquired=True`의 점유 중 / `False`의 반환 관측 / 누락·Unknown의 미확인이며, 실행의 정리 실패 판정과 합치지 않는다.
진행 표시는 active 조회만 사용하는 읽기 전용이다. 이름이 같아도 UID가 다른 활성 실행은 선택 상세에 표시하지 않는다. 중지·확인·불확실 결과 재조회는 실행 상세의 RunActions 한 곳에만 둔다.

노드 전력 표는 `meterJoules`, `meterComplete`, `gapSeconds`, `gapFraction`, `maximumSampleGapSeconds`, `sourceIdentity`를 서버 값 그대로 표시한다. 계측기 에너지와 전체/활성 풀 에너지는 독립이며 잘린 보존·source 혼합·빈 bucket의 불완전/null을 0·완전으로 바꾸지 않는다. 원시 응답 내보내기를 유지하고 브라우저에서 재적분하지 않는다.

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
