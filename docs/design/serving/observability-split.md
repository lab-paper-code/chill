# 관측 저장과 증거 API

ChillServer는 요청과 제어, 자원과 클러스터 사실을 같은 시간 세그먼트에 기록하고 로컬 또는 immutable archive에서 읽는다. 별도 TSDB나 broker, 과거 계약 변환 reader를 두지 않는다. [Observation Evidence API Streams](../../adr/0030-observation-evidence-api-streams.md)와 [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.

## 소유권과 기록 수락

`internal/serving/observation`이 process flock, request와 catalog 기록/복원, capture와 archive, retention과 power reduction을 소유한다. Runtime은 inward `RuntimeObservation` 계약을 사용하고 storage를 import하지 않는다.

모든 stream과 root definitions는 frozen bytes를 무제한 FIFO 하나에 넣고 disk worker 하나가 처리한다. RequestEvent, RequestClosed와 SchedulerRecord 반환은 FIFO 수락을 나타낸다. catalog는 reference보다 먼저 같은 FIFO에 들어간다.

제어 decision/action과 mutation audit도 기록 수락 뒤 live에 발행한다. 이 순서를 매 요청 durable fsync로 해석하지 않는다. lifecycle 성공과 raw/read barrier는 필요한 유한 prefix의 write 또는 Sync를 별도로 기다린다.

지속 disk 지연은 queue 메모리 증가와 OOM, unwritten tail 손실을 일으킬 수 있다. 새 cap과 drop, spool과 retry queue는 없다. live `chill_obslog_queue_length`는 in-flight head 포함 미완료 항목, `chill_obslog_oldest_wait_seconds`는 가장 오래된 항목의 현재 대기다.

worker 실패는 failed head와 후속 bytes를 유지하고 Runtime을 깨워 sticky ObservationUnavailable/503으로 신규 접수를 닫는다. 이미 실행 중인 물리 소유권은 유지한다. explicit Flush/Close는 같은 ordered prefix를 재시도하되 rollback 불명 경계를 성공으로 통과하지 않는다.

## 요청과 불변 catalog

requests는 assigned와 final, late native supplement를 requestID로 결합한다. reducer 우선순위는 final, 현재 열린 행, assigned다. 수신과 검증, 접수는 열린 행에 접고 배정과 최종 경계에서 저장한다.

| 사실 | 보존 |
|---|---|
| 요청 identity | requestID, Workload UID와 definitionID, receipt와 lifetime |
| 배정 | dispatch 시각과 physical instance, Node, profile와 decision |
| 결과 | 실제 outcome/error, executionComplete와 HTTP 교환/전달 결과 |
| 경과 | receivedAt 기준 정수 ns offset, 원 monotonic elapsed |
| batch | 실제 BatchID와 size, service/backend와 pre/post, first token과 units |
| canonical service | 실제 Go IPC S와 nullable G, 물리 TrialID |
| admission | prediction, waits와 당시 source/actual policy. 추정 duration과 receipt elapsed 분리 |

미도달 단계는 비우고 관측0은 유지한다. UTC를 다시 빼 duration을 만들지 않는다. 중앙 serverTiming은 receive와 auth/body/accept, assigned와 HTTP, 결과 lock/reply와 response write/flush의 실제 경계를 남긴다. marker 배열 순서를 goroutine 인과 순서로 해석하지 않는다.

단계 분해는 dispatch 전, HTTP 교환과 반환의 기존 구간 안에서 비중첩 관측과 명시 residual로 맞춘다. connect와 acquire, worker와 lock의 중첩을 다시 더하지 않는다. response 뒤 Runtime 정리를 사용자 지연에 더하지 않는다.

canonical S/G가 없으면 proxy 또는 unknown이다. queued/running offset이나 native UTC로 IPC stopwatch를 합성하지 않는다. supplement는 원 final을 다시 열거나 HTTP outcome을 덮지 않는다.

루트 `definitions.jsonl`은 삭제하지 않는다. 삭제 Workload와 동명 재등록은 UID와 definitionID로 구분한다. 당시 모델과 SLO를 현재 객체로 보충하지 않는다.

catalog는 `{kind,key,at,body}`다. hour의 첫 reference와 본문 변경 때 기록하고 그 기록시각 이하 마지막 body를 연결한다. profile은 profileID, definition은 definitionID, cost와 baseline은 ID와 evidence key, meter는 기록 identity로 참조한다.

## Client feedback과 모집단

`POST /api/v1/workloads/{id}/feedback`는 observationID, 선택 requestID/workloadUID, elapsedNanos와 outcome, 선택 statusCode를 받는다. 서버가 reportedAt과 이름, 검증 actor를 붙이며 trusted telemetry다.

completed와 http_error는 전체 body 수신과 실제 statusCode를 요구한다. timeout, transport_error와 canceled의 elapsed는 중단까지 하한이다. 실패 하한을 유한 성공 latency나 path reserve로 쓰지 않는다.

중복 원본은 clients에 남기고 첫 identity 보고만 집계한다. alias와 만료는 retention horizon을 따르며 재시작이 horizon을 연장하지 않는다. 피드백 수락 중 요청 이력을 읽거나 완료를 기다리지 않는다.

같은 capture에서 정확한 server identity와 결합한다. 결합 표본은 server receivedAt cohort, 미결합과 requestID 없는 보고는 reportedAt cohort다. client-first도 이후 capture에서 결합할 수 있으나 늦은 보고로 창 밖 요청을 되살리지 않는다.

전체 client 결과와 실패, 유한 회계 가능한 PathSamples는 별도 모집단이다. E/Q/S/N와 TOTAL/POST/PRE는 [시간 회계](analytic-planner.md#시간-회계와-요청-동결)를 따른다.

워크로드 집계는 `[from,to)` 수신 요청을 먼저 선택하고 전체 필터 집계 뒤 raw page를 만든다. page 크기로 percentile을 계산하지 않는다. 성공 완료 latency와 error, rejection과 timeout, canceled와 incomplete를 구분한다. 무표본 quantile은 null이다.

## Crash와 종료

flock 아래 async-run.json을 active로 durable 기록한 뒤 repair와 restore, 실행을 시작한다. unclean, missing과 malformed predecessor는 boot-wide unknown이다. 빈 디렉터리만으로 새 물리 설치를 증명하지 않는다.

written final 없는 assigned는 RestartDrain으로 복원하고 요청을 재실행하지 않는다. crash 때 unwritten 접수와 큐 tail은 유실될 수 있으며 source/coverage의 복구 공백으로 남긴다.

각 exact executor는 이번 boot의 verified fresh actor-empty 또는 정확한 종료까지 inference fence를 유지한다. pending과 unreadable, unrouted 실물도 점유다. 정상 sibling은 독립적으로 serving할 수 있다.

actor-empty CLEAR는 현재 inference fence만 푼다. 독립 prepare/power/route/cleanup effect나 과거 late-tail influence를 지우지 않는다. buffered HTTP가 늦게 접수되면 영향은 실제 late 완료까지 연장되고 끝을 모르면 UNKNOWN/open이다. 정확한 종료는 별도 historical 경계를 만들 수 있다.

성공 Create의 API Pod UID는 이미 존재하던 crash root와의 관계를 보존해 새 executor에 옛 영향을 전파하지 않는다. 이후 unclean boot의 새 root와 정상 Ready/owner 검사는 면제하지 않는다.

정상 종료는 신규 admission 중지, producer join, pending final drain, Sync/Close, durable clean marker, flock release 순이다. clean은 memory-only tail 없음만 뜻한다.

## 세그먼트와 archive

현행 경로는 `<directory>/v1alpha7/<YYYYMMDDTHH>/<stream>.<MM>.jsonl`, archive는 `serving/observations.v1alpha7/<YYYYMMDDTHH>/<stream>.jsonl.gz`다. 저장 분은 수락 append frontier의 UTC이며 과학 사건시각은 원본 그대로다.

stream은 requests, scheduler, management, catalog, clients, resources와 cluster다. root definitions는 별도다. torn tail은 마지막 완결 개행으로 복구하며 실패 append는 증명 가능한 이전 크기로 rollback한다.

지난 hour의 minute를 순서대로 gzip하여 immutable multipart로 보낸다. payload buffer는5MiB 하나, 최대10000 part다. gzip과 SDK, 완료 metadata는 별도 메모리다. 이 buffer를 전체 RSS 상한으로 주장하지 않는다.

Complete는 모든 read/close와 gzip.Close 성공 뒤다. immutable complete의412는 Existing,409와 다른 오류는 실패다. 실패와 취소는 분리된30초 context로 Abort하며 원 오류와 raw를 보존한다.

request timeout은 각 SDK 호출의 retry/backoff 포함 상한이다. Flush 전체에 같은 짧은 deadline을 씌우지 않는다. archive는 serve loop 밖에서 실행하고 종료 때 cancel과 join한다. 종료 Close는 로컬 Sync/Close를 하며 실패 raw는 다음 boot에서 게시한다.

retention은 hour 시작 기준 양수 duration, 기본2시간이다. 모든 stream이 Created/Existing이고 미완료 assigned pin이 없을 때만 삭제한다. upload 실패 hour와 이후, 미완료가 참조한 hour를 유지한다. S3 지연이 PVC를 채우면 append 실패와 health 오류로 드러난다.

## Capture와 읽기 일관성

유한 FIFO prefix를 넘은 뒤 writer mutex에서는 committed path와 complete-line offset, open row와 catalog metadata만 복사한다. directory read와 정렬, open/stat과 decode는 그 밖이다. retention captureMu는 open 완료까지 경로 삭제를 막으며 append는 이 잠금을 얻지 않는다.

로컬 hour 디렉터리를 우선하고 해당 hour가 없을 때만 archive와 catalog를 읽는다. 로컬에 없는 stream만 archive로 보충하지 않는다. 같은 hour를 두 source로 더하지 않는다.

고정 offset 뒤 append와 미완결 tail은 제외한다. 읽기는 written prefix와 captured live rows를 대상으로 한다. 열린 fd는 이후 retention 삭제에도 계속 읽는다.

coverage는 captured clock까지만 확정한다. 미래 query end와 boundary-support 검색이 열리지 않은 hour를 결손으로 만들지 않는다. 요청 from/to는 유지하고 coverage.to를 clip하되 실제 power sample을 합성하지 않는다.

읽기 오류, malformed JSON과 gzip, catalog 부재와 store 실패를 빈 성공으로 바꾸지 않는다. read-side 오류는 쓰기 health를 자동 손상시키지 않는다.

decoded request/client cache는 설정 rolling span과 log retention 이내 minute를 보유한다. 고정 row cap은 없다. cold와 suffix scan/decode는 cache lock 밖이며 완전 prefix만 게시한다. 과거 조회가 rolling 보유 기간을 늘리지 않는다.

## 공개 조회

| GET 경로 | 의미 |
|---|---|
| `/api/v1/observations/workloads/{uid}` | 당시 정의, 수신 cohort와 관리 사실 |
| `/api/v1/observations/load` | 모든 UID의 단계별 rate와 assignment |
| `/api/v1/observations/clients` | reportedAt 창의 append-order 원본과 coverage |
| `/api/v1/observations/scheduler` | 사건창의 판단과 action, control 기록 |
| `/api/v1/observations/events` | exact at 또는 최대24시간 범위의 작은 사건 |
| `/api/v1/observations/power` | 기간 적분 또는 UTC bucket |
| `/api/v1/observations/occupancy` | accepted aggregationWindow 이하 fresh pair 진단 |

scheduler full/compact/summary와 filter, uid와 offset은 [설정과 관측 API](scheduler-api.md#이력과-compact-조회)를 따른다. compact의 projection과 omitted 목록은 원본과 다른 공개 범위를 표시한다. stored journal과 원본 상세는 유지한다.

action expectedSeconds와 expectedSource는 선택된 측정 trial의 평균 시간과 근거다. 근거 부재는 null이며 과거 action에 현재 cost를 대입하지 않는다.

### 사건 continuation

at과 from/to는 혼용하지 않는다. 범위는 양수이며 최대24시간, limit은 기본100과 최대500, 더 작은 서버 bound를 따른다.

cursor는 다음 append-hour 또는 `<hour>/<cluster-row-offset>/<scheduler-row-offset>`다. hour 단독은 두 offset0이며 hour/n은 거절한다. consumed row는 비매칭도 포함하고 stream별로 독립이다.

continuation은 append hour와 각 stream의 행 순서다. 사건시각 정렬은 한 응답 page 안에서만 보장한다. 매 호출 현재 prefix를 읽으며 첫 page에서 후속 append를 전역 동결하지 않는다.

빈 page도 nextCursor가 있으면 계속 읽는다. coverage는 이번에 시도한 append-time 범위와 결손이다. continuation 없음은 이번 head까지 읽었다는 뜻이다. 이후 late 사건이 추가될 수 있다.

eventLookupHours 기본1은 호출당 append-hour 예산이다. late-delivery는 이 예산 밖에서도 도착할 수 있다. exact와 범위는 같은 규칙이며 malformed, 미래와 out-of-range offset은 InvalidQuery다.

### 전력 기간과 bucket

실제 meter와 source identity, recorded coverage와 membership으로 사다리꼴 적분한다. whole/active 풀과 meterJoules를 구분하며 node wall을 인스턴스별로 반복 합산하지 않는다.

bucket 미지정은 기간 summary다. bucket은1초..1시간, 최대1시간 query와900개다. UTC duration 배수로 정렬하고 양 끝을 clip한다. 비정렬15분/1초는901개가 될 수 있어 거절한다.

observedSamples는 bucket 안 실제 표본이며 samples에는 경계 support가 포함될 수 있다. 내부 표본이 없으면 J/W와 관련 풀 합계는 null, complete=false다. 관측0은0이다. 이웃 평균을 복제하지 않는다.

meterJoules는 기록 meter 정책의 별도 적분이다. 긴 gap에도 사다리꼴 estimate를 유지하되 meterComplete=false와 원 gap 진단을 남긴다. coverage-qualified 풀 합계와 혼동하지 않는다. source 혼합과 미상, 선택 잘림과 관측 부재는 null과 이유다.

`format=columns`는 bucket과 함께 명시한다. 모든 window와 node 필드를 같은 bucket 순서와 node 순서로 보존하며 null과 optional 부재, 정밀 ns를 무손실 복원한다. 기간 평균을 짧은 표본 여러 개로 늘리지 않는다.

## 원시 내보내기

hour는 안전한 UTC `YYYYMMDDTHH`, kind는 등록 stream이다. filename과 임의 store key, host path를 입력으로 받지 않는다. unknown과 중복 query, 경로 이탈은400이다.

| 경로 | 응답 |
|---|---|
| `GET /api/v1/observations/raw?hour=<hour>` | hour와 index 시각, open/closed-local/archived, kind별 닫힌 minute bytes 또는 archive ref |
| `GET /api/v1/observations/raw/{hour}/{kind}` | 선택 닫힌 local minute 원래 NDJSON, 또는 저장 gzip bytes |
| `GET /api/v1/observations/raw/definitions` | root의 마지막 완결 개행까지 원래 NDJSON |

현재 열린 minute는 크기나 개행 여부와 무관하게 export하지 않는다. local이 남으면 archive보다 우선한다. index와 download는 각각의 capture이며 서버 receipt 세션을 만들지 않는다.

NDJSON은 application/x-ndjson, gzip은 application/gzip과 no Content-Encoding이다. no-store이며 decode/re-encode, 즉석 압축과 필터를 하지 않는다. source가 없으면404, 현재 hour에 닫힌 minute가 아직 없으면409다.

raw capture는 유한 written prefix 뒤 짧은 metadata snapshot을 얻고 fd open과 전송은 producer lock 밖이다. known length를 Content-Length로 주고 늦은 IO 실패는 body를 중단한다. JSON 오류를 gzip이나 NDJSON 뒤에 붙이지 않는다.

처리 상한은 operation timeout이며 write 여유는 request timeout을 더한다. 취소는 archive와 copy까지 전달한다. 별도 무제한 timeout과 digest 대조 gate를 두지 않는다.

raw 전용 caller는 `/chill/api/v1/observations/raw`와 `/chill/api/v1/observations/raw/*` GET 둘 다 필요하다. 일반 Dashboard와 BFF, serving-manager에 raw를 자동 공개하지 않는다.

## 자원과 클러스터 사실

기본 discovery node-agent가 서버가 발급한 exact Pod UID와 containerID의 host cgroup을 읽는다. agent에 Pod get/list/watch를 주지 않는다. hybrid는 v1 controller 우선, unified-only는 v2다. root counter나 같은 Pod 이름으로 대체하지 않는다.

`POST /api/v1/internal/observations/resources`는 `{node:{name,uid},samples:[...]}`를 받아 다음 targets를 반환한다. 최초 빈 handshake는 기록 없이 대상만 얻는다. 현재/직전 발급 identity를 검증하며 교체 UID를 연결하지 않는다.

TLS와 projected token, 정확한 POST SAR를 사용한다. 공유 SA는 Node별 암호학적 identity를 보증하지 않는다. 기본 sample1초와 batch10초는 설정이며 실패 묶음은 버린다. 재전송과 spool, 소급 표본은 없다.

CPU usage와 throttling은 누적 ns, periods는 횟수, memory는 bytes, thermal은 milliCelsius다. 미지원과 읽기 실패는 부재와 errors이며0을 만들지 않는다. sampledAt과 receivedAt을 둘 다 유지하고 시계를 보정하지 않는다.

서버는 기존 ticker에서 GC CPU와 cycle 누적값, 분별 observation directory regular-file 논리 bytes를 기록한다. processStartedAt과 Pod UID를 넘어 누적 차를 빼지 않는다. PVC capacity와 allocated blocks, gctrace 분포와 구분한다.

cluster는 기존 Pod List/Watch와 issue 변화에서 실제 identity와 container 상태, imageID와 restart, 종료 사실을 기록한다. 최초 List는 observed baseline이다. watch 단절을 삭제로 만들지 않는다.

issue는 raised/changed/resolved이고 반복 성공/실패를 새 원인으로 누적하지 않는다. 기록 수락 뒤 같은 projector로 live에 발행하며 실패를 기록 완료로 표시하지 않는다. 별도 outbox와 과거 사건 복구를 약속하지 않는다.

## Live와 평가 소비

live hub는600프레임/8MiB ring,602프레임/2MiB queue와1MiB frame 상한을 사용한다. snapshot history의 retained sequence 범위는 보존 frame metadata다. REST 사건에 가짜 WS sequence를 붙이지 않는다.

mutation의 started/completed/failed와 원 status를 함께 읽는다. 오류 없는 NOT_SUBMITTED, Accepted와 DeletionRequested의 completed는 각각 사전 검사와 접수/삭제 요청의 종결이다. physical 측정 완료와 과학적 수용으로 세지 않는다.

evaluation은 CHILL 내부 사실을 raw, resources와 cluster, accepted meters의 공개 API에서 수집한다. 원시 수집과 대량 과거 조회는 측정 창 밖에서 수행하고 창 안은 live 관측만 허용한다. 외부 producer와 명시 장애 주입의 운영 권한은 별도다.

불변 experiment의 KPI와 당시 definition, identitySource와 출처를 유지한다. 원본에 없는 identity나 예정 boundary를 현재 상태와 관측 전환으로 보충하지 않는다. 계획 부하, closed-minute 진도와 drain 시점 결손, 최종 판정을 구분한다.

실측 판정은 [실클러스터 수용 기준](scheduler-operations.md#실클러스터-수용-기준)을 따른다.
