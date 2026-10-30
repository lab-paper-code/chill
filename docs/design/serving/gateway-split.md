# 게이트웨이 제어·데이터 평면 분리

**상태: Deferred — 정리 완료, 설계만 보존(2026-10-30): 미사용 G2 구현(`e947cd5e`)·빌드 태그 제거, T5 live 보존; 운영 채택·G3 미승인.**
이 문서는 재개 판단과 필요한 안전 불변식을 남기는 설계 기록이며, E5 직후 실행할 작업 목록이 아니다.
재개 조건과 순서는 [ADR 0029](../../adr/0029-gateway-control-data-plane-split.md)가 정한다.

근거는 `O/intent/gateway-20261027/proposal.md`(목표와 배분표 계약), 같은 폴더의
`arch-map.md`(현행 경로), `research.md`(참고 사례)다.
`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`.
결정은 [ADR 0029](../../adr/0029-gateway-control-data-plane-split.md), 요구사항과
AC는 [scheduler SRS §8](../../spec/scheduler/SRS.md)의 SCH-60~66 / SCH-AC-47~51에 둔다.
현재 [라우팅](../../spec/03_routing.md)·[전환](../../spec/05_transition_control.md)·
[관측](observability-split.md)의 의미는 유지한다. 이 초안은 그 원본들을 소급 변경하지 않는다.

## 0. 보류 범위와 재개 시 최소안

- **첫 검증 구성은 control 1 + router 1**이다. 같은 Pod의 별도 컨테이너로 두는 안부터
  검토한다. 요청 기록은 router, scheduler/management 기록은 control이 소유하며 디렉터리
  writer를 분리한다. 두 컨테이너의 CPU·메모리·GC와 합계 비용을 모두 관측한다. 이 구성은
  Node·Pod 장애 격리나 control 장애 중 무중단을 보장하지 않으며, 꼬리 개선도 보장하지
  않는다. 배치 가능성과 격리 효과는 재개 때 확인한다.
- **범위 밖(기본안 아님)**: 온라인 router 수 변경·가입, 영속 membership, 이전 프로세스
  세대 custody·자동 Pod 교체 복구, router별 quota 분할, 정확한 전역 quota, 실행 전 거절
  뒤 자동 재배정, 소비자 없는 도착 카운터(§4 7·§4.1의 `ArrivalCounts`). 장애 시 신규
  유입을 닫고 캠페인 실패와 미확인 물리 점유를 보존한다. 실패를 성공으로 만들기 위한
  timeout 해제나 증거 합성은 하지 않는다.
- **축소하지 않는 것**: 원자 적용·요청별 버전 고정, native ACK → 실제 applied → 퇴역 source
  신규 예약 차단 → 물리 drain 순서, timeout/cancel ≠ 점유 0, assigned 선행 기록과 기록
  실패 시 native 미호출, coverage 오류 표시, 안전한 reset.
- **관측 제공**: 원시 요청 증거와 제어 기록은 [evidence API](../../plans/2026-10-28-e5-evidence-api.md)의
  CHILL raw API로 제공한다. 하네스의 host/S3 우회 수집을 다중 router용으로 확장하지
  않는다. source가 실제로 여럿 생기는 cutover에서만 API source 목록·coverage·
  `resources`·`cluster`·T5 모집단을 함께 바꾼다(ADR 0019, 캠페인 사이 1회).
- 아래 §1~§5의 다중 router 문구는 재개 때 이 절의 범위로 다시 쓴다.

## 1. 모드와 책임 경계

제어는 같은 서버 조립 소스의 **별도 실행 파일**로 빌드한다:
`go build -tags gatewaycontrol -o chill-control ./cmd/chill-server`.
데이터 평면은 `go build -o chill-router ./cmd/chill-router`다. 기본 `chill-server`는
E5에서 측정한 기존 단일 게이트웨이 조립을 유지한다. default-off rollout 플래그나
차트 연결은 추가하지 않으며, 새 control 실행 파일에는 추론 body/picker 경로가 없다.
배포 cutover 뒤에는 단일 게이트웨이를 호환 경로로 병행하지 않는다. native runtime
7 이미지와 프로파일은 이 변경으로 재빌드하지 않는다.

| 책임 | control (1 replica) | router (N replicas) |
|---|---|---|
| 판단/운영 | 전역 계획기, κ·전환 판단, 실물 장부·범위 예약, native 정책/Pod/전력 작업 | 실행하지 않음 |
| API | 관리·측정·관측 조회·Dashboard T5 | 추론 API와 자기 health/readiness, 내부 배분표 구독 |
| 요청 경로 | 본문을 받거나 전달하지 않음 | 최초 수신·인증/SAR·bounded 원시 입력 검증·미배정 큐·SWRR·점유·native 왕복·응답 |
| 관측 | native 상태/로그 관측, 정확한 소유 router로 진행·완료 증거 전달, 다중 stream 조회·집계 | 자기 assigned/final·catalog 기록, 점유/도착 상태 보고, 기존 archive writer 재사용 |
| 상태 | 목표 버전·라우터별 applied/참여/신선도·source별 점유와 작업 진행 | 현재 불변 배분표·로컬 SWRR/큐·요청별 물리/transport/응답 상태 |

```text
client ── infer Service ── router-1 / router-2 / ... ── Pod 전용 Service ── native
                              ▲          │                              │
                  planVersion push       │ applied, outstanding, arrivals│
                              │          ▼                              │
                         control(1) ◄──── native state / progress logs ───┘
                              │
                         관리·조회·T5
```

router는 기존 [ADR 0021](../../adr/0021-raw-sample-preprocessing-boundary.md)의
신뢰 경계를 유지한다. 요청마다 control RPC를 하지 않으며 기존 Kubernetes 신원
검증과 허용 cache 경로를 재사용한다. Service는 router 중 진입점만 고른다.
실행 대상은 각 router가 배분표로 고르고 기존 Pod UID 전용 Service/EdgeMesh를
사용한다. 공유 native Service에 Workload 가중 선택을 위임하지 않는다.

## 2. 배분표 계약

배분표는 다음 논리 필드를 한 묶음으로 갖는 불변 전체 snapshot이다.
wire DTO는 `serving.GatewayPlan`/`GatewayTarget`으로 표현하며 정의/정책/배분표 identity를 합치지 않는다.

| 정보 | 의미 |
|---|---|
| `planVersion` | 서로 다른 본문에 재사용하지 않는 배분표 identity. control 재시작을 가로질러도 충돌하지 않음 |
| Workload UID, `definitionID`, 정의/요청 수명 | 같은 이름의 다른 등록 건·모델/입력/SLO를 분리하고 최초 수신 기준 예산 유지 |
| Pod UID, container identity, node, 전용 endpoint | 이름 재사용·다른 실물로의 잘못된 전달 방지 |
| 가중치, 정책/프로파일 참조, router별 점유 한도 | 실제 Ready 및 native 정책 ACK된 대상과 router 로컬 분배에 필요한 값 |
| `admission`, `observationWindowNanos` | Workload별 신규 유입 gate와 동일 관측 창. 이미 수락한 요청의 drain 경로와 새 유입 허용을 구분한다. |

기존 native `DecisionID` ACK 후에만 해당 정책에 의존하는 snapshot을 발행한다.
정의·참조·대상·가중치·상한을 모두 검증하고 한 번의 포인터 교체로 적용한다.
요청은 사용할 snapshot을 Receive에서 잡은 뒤 Accept와 처리 종료까지 그 snapshot을
보유하며 요청 행에 `planVersion`을 남긴다. **무관한 표 갱신 자체를 이유로 요청을
거절하지 않는다.** 이후 새 표가 와도 이미 잡은 요청의 정의·수명·배정 버전을 바꾸지
않는다. 같은 버전으로 안전하게 처리할 수 없는 수락 요청이 있으면 기존 handoff 보호
조건에 따라 전환을 보류하고, 임의로 새 정의/대상에 옮기지 않는다. 현재 G2 코드는
포인터가 바뀌면 `GatewayPlanChanged`로 거절한다(`internal/serving/runtime.go:306-307`).
이 거절을 단순 삭제하는 것으로는 부족하며, 구표 요청 보호와 퇴역 gate를 함께 만족해야 한다.

첫 배정의 snapshot 선택·물리 점유 예약은 배분표 교체와 같은 로컬 동기화 경계에서
수행한다. applied 뒤에는 구표를 새로 잡아 퇴역 source의 점유를 늘릴 수 없다.
이미 구표로 예약한 호출은 전달 시작 전이라도 outstanding에 포함해 drain을 막는다.
구버전의 재배정도 현재 퇴역 gate를 통과해야 하며, 닫힌 대상을 다시 예약하거나
점유 0 보고 뒤 뒤늦게 실행해서는 안 된다.

`applied`는 실제 교체 뒤의 `{routerID,planVersion,appliedAt}`이다. `sent`,
`received`, native ACK, Dashboard seq와 구분한다. 같은 표 재전송은 동일 적용
상태를 다시 확인할 뿐 새 전환이 아니다. 오래된 연결의 ACK/상태가 새 router나
현재 버전을 승인하지 않도록 연결 세대와 현재 배포 대상을 대조한다.
control 재시작 후에는 이전 ACK를 추정 복원하지 않고 현재 표와 router를 재동기화한다.

## 3. 배분표 채널 = T5 WebSocket 메커니즘

[Dashboard T5 설계 §4.3~4.5](../dashboard/README.md)의 인증된 TLS WebSocket,
유한 송수신 큐, 순번/checkpoint, heartbeat와 재연결 snapshot 방식을 재사용한다.
`internal/serving/live`의 `Encode`/`Read`/`Write`와 frame/write/queue 상한을
공유하고 내부 채널은 `internal/serving/gateway`가 소유한다. 새 broker·gRPC·xDS
서버를 별도로 도입하지 않는다. 이 구현은 G3/운영 통과 증거가 아니다.

- router가 고정된 control 주소로 직접 WSS를 연결한다. 브라우저/BFF를 경유하지
  않는다. T5의 서버 간 leg처럼 TLS CA/hostname과 기존 Kubernetes bearer 신원을
  검증하고 Upgrade 전 TokenReview/SAR를 통과시킨다. token은 URL·로그에 넣지
  않는다. 브라우저 first-auth 변환을 router에 억지로 추가하지 않는다.
- 같은 전송부를 쓰되 내부 router 메시지/권한은 Dashboard의 읽기 전용
  `/api/v1/live` feed와 분리한다. Dashboard 사용자는 배분표/ACK/점유를 발행할
  수 없고, router 권한도 관리 API 변경 권한으로 확장하지 않는다.
- control → router: 전체 배분표와 소유 요청의 native 진행/물리 완료 증거.
  router → control: 실제 `applied`, Pod UID/container별 물리 outstanding,
  도착·검증 카운터와 관측 시각. heartbeat와 ACK 자체는 물리 완료 증거가 아니다.
- 전송 seq/checkpoint는 채널 누락 탐지용이고 `planVersion`은 배분 의미다.
  재연결 시 현재 전체 표를 받아 재적용/확인하고 점유 snapshot을 재보고한다.
  재개 불가·순번 공백 때는 완료/점유 이벤트를 조용히 건너뛰지 않고 native
  상태/기록과 재대조한다. 단절 전 실행 중 요청은 별도로 추적한다.
- 느린 소비자·큐 포화는 연결을 닫아 재동기화한다. 제어 전송이 요청 handler나
  배분표 포인터 교체 잠금을 붙들지 않는다. 배분표/ACK/물리 증거를 화면 지표처럼
  drop/coalesce해 적용 완료나 0을 합성하지 않는다.
- 연결 상실 또는 heartbeat 신선도 기한 초과는 router 자신이 신규 유입 gate와
  readiness를 닫는다. control이 끊어진 router에 철회 메시지를 보낼 수 있다고
  가정하지 않는다. 기존 연결의 새 infer도 gate를 검사한다. 재동기화·현재 버전
  적용 확인 뒤에만 다시 연다. timeout/queue 크기는 T5 전송부의 유한한 설정을
  사용하고 G3 실행 조건에 고정한다. 일반 HTTP 요청 timeout을 연결 전체 수명으로
  재사용하지 않는다.

## 4. 요청 기록·보관·reader 경로

기존 [관측 설계 §3~6](observability-split.md)의 두 행·catalog·시간 세그먼트와
근거 저장소를 그대로 사용하고 **router 소스 차원**을 추가한다.

1. `routerID`는 Pod UID와 프로세스 세대를 구분하는 고유 identity다. `requestID`는
   기존 전역 충돌 방지 ID를 유지하고 assigned/final에 `routerID`, `planVersion`,
   정확한 Pod/container를 함께 남긴다. 최초 배정 전 assigned append가 실패하면
   native를 호출하지 않는다. final 실패는 기존 closing-row 재시도와 readiness
   실패로 드러내며 성공/완료 행을 합성하지 않는다.
2. 경로 초안은 로컬 `<router-buffer>/<routerID>/v1alpha5/<UTC-hour>/<stream>.<MM>.jsonl`,
   archive `serving/observations.v1alpha5/routers/<routerID>/<UTC-hour>/<stream>.jsonl.gz`다.
   `requests`와 해당 `catalog`·루트 정의를 같은 소유 단위로 보존한다. control의
   scheduler/management 기록은 기존 control 소스에 한 번만 쓴다. 공유 디렉터리의
   다중 writer나 같은 PutImmutable key 덮어쓰기는 없다.
3. router별 기존 archive writer가 시간별 gzip/불변 업로드·retention을 수행한다.
   해당 시간 모든 stream의 업로드 성공 전 로컬을 지우지 않고, final 없는
   assigned 시간과 이후 시간은 보호한다. router 교체/종료로 미업로드 버퍼나
   미완료 기록을 폐기하지 않도록 보존 볼륨과 이전 소스 복구를 배치에 연결한다.
   새 프로세스는 새 세대 ID를 쓰되 이전 소스의 미완료를 복구 대상으로 유지한다.
4. control은 참여 routerID와 가입·제외 시각을 관리/전환 관측 및 실험 binding에
   기록한다. reader는 이 목록의 현재/퇴역 소스를 모두 찾는다. 로컬 우선·없는
   시간 archive 읽기는 **(routerID, UTC hour, stream)**별로 적용한다. A의 로컬
   시간이 있다고 B의 같은 시간 archive를 건너뛰어서는 안 된다.
5. 기존 `requestID`별 `final > open > assigned` 병합을 유지한다. archive assigned와
   늦은 로컬 final, 재시작 후 재전송은 한 요청이다. 열린 행은 각 소유 router가
   제공하고, 읽기에서 그 관측 시각/공백을 표시한다. catalog는 정확한 소스·시각의
   참조로 풀며 같은 이름의 새 Workload UID나 다른 정의로 대체하지 않는다.
6. 모든 소비자를 함께 이행한다: `Query`, `LoadWindow`/`SchedulerArrivals`,
   `ScanOccupancyRows`/d, scheduler/power 조회의 source/catalog 선택, E5 archive
   수집·raw reducer, 관측 SQL/도구와 Dashboard 응답. control 전력/판단은 router
   수만큼 중복 가산하지 않는다. SLO 백분위는 router별 백분위의 평균이 아니라
   합친 요청 모집단에서 계산한다.
7. λ의 빠른 경로는 router별 동일 시간창의 도착/검증 카운터 합이다. 사건 종류를
   구분하고 `(routerID, window)`의 보고 교체로 재전송을 중복 가산하지 않는다.
   재시작은 새 세대이며 공백을 0으로 채우지 않는다. 스트림 집계와 카운터를 같은
   모집단에 더하지 않는다. d·E5는 병합한 원시 요청으로 재계산 가능해야 한다.

출처/coverage에는 routerID·local/archive·시간 범위·공백 이유를 포함한다.
누락 stream, catalog 오류, gzip/JSON/store 실패는 관측 불완전/오류이며 무유입
0이 아니다. load-generator의 전체 offered 분모는 서버 기록 유무와 무관하게
유지한다. 과거 단일 gateway archive는 원래 소스로 읽되 `routerID`/`planVersion`
부재를 새 split 적용 증거로 해석하거나 임의 값으로 채우지 않는다.

### 4.1 G2 reader·E5 수집기 연결 계약

- 요청 행의 추가 필드는 `routerID`, `planVersion`, `podUID`, `containerID`다.
  assigned/final의 `requestID`로 먼저 병합하고 원래 `definitionID`와 해당 source의
  catalog를 사용한다. 원래 단일 게이트웨이 행의 필드 부재는 그대로 보존한다.
- writer는 `ObservationLogConfig.RouterID`와
  `<router-buffer>/<routerID>/v1alpha5/<UTC-hour>/<stream>.<MM>.jsonl`을 사용한다.
  archive key는 `serving/observations.v1alpha5/routers/<routerID>/<UTC-hour>/<stream>.jsonl.gz`다.
  각 router는 `<router-buffer>/.owners/<PodUID>`의 독립 ownership lock을 잡는다.
  같은 Pod의 이전 프로세스 세대만 복구하며 다른 router 파일은 쓰지 않는다.
- control reader는 `RouterSources() []RouterObservationSource`로 현재와 이전 세대를
  모두 받는다. 필드는 `RouterID`, `Directory`, `JoinedAt`, `LeftAt`, `ObservedAt`,
  `Open`이다. `--router-buffer-root`는 각 source의 private buffer를 읽기 전용으로
  볼 수 있는 공통 mount를 전제하지만, 관측 PVC는 RWO(`charts/chill/templates/serving_entrypoint.yaml:30`)라
  다중 Node 분산을 보장하지 않는다. 재개 시 §0의 같은 Pod 구성 또는 raw API 제공으로
  대체한다. 경로를 제공하지 못한 현재 시간은 archive로 완전하다고
  추정하지 않고 `LiveBufferUnavailable` coverage gap으로 표시한다.
- `requests`/`catalog`의 로컬 우선순위는 source별 UTC hour에 적용한다.
  `Query`, `LoadWindow`/`SchedulerArrivals`, `ScanOccupancyRows`는 합친 원시 요청을
  사용하고 scheduler/power/management control stream은 한 번만 읽는다.
  source별 관측 시각과 누락 이유는 coverage의 `routerID`로 보존한다.
- 보고의 `arrivals[]`는 `{from,to,workloadUID,received,validated}`이며 outer 보고의
  `routerID`와 함께 identity를 이룬다. `ArrivalCounts(from,to)`는 같은 창의 보고
  교체를 합치는 빠른 count reader이고, 재생에 필요한 개별 도착 시각은 계속
  병합 원시 stream에서 읽는다. 두 경로를 같은 λ 모집단에 더하지 않는다.
- 참여·퇴역/연결 세대는 control의 `gateway-membership.json`과
  `gateway-router-membership` scheduler 관측에 남긴다. E5 뒤 split 배포를 연결할
  담당자가 이 source 목록을 실험 binding과 `hack/e5` collector/SQL 입력에 고정한다.
  이 G2 작업은 운영 중인 `hack/e5`나 현재 E5 binding을 수정하지 않는다.

control의 스케줄러는 현재 router들의 `ObservedAt` 최솟값에서 끝난 UTC 초까지만
원시 도착 창을 읽는다. 마지막 보고 뒤의 open/미배정 요청을 관측 0으로 읽지 않는다.
reader는 그 뒤 구간을 `RouterReportUnavailable`로 표시하고, 실제 owner가 만든
빈 requests/catalog segment가 있는 관측 prefix만 무유입으로 해석한다.


## 5. 전환·퇴역 안전성

전환의 기준 순서는 다음과 같다. 기존 범위 예약·SLO·준비된 부분집합 배분과
독립 범위 진행은 유지하며 전체 클러스터 작업 완료 장벽을 새로 만들지 않는다.

1. 기존 장부/예약 경로에서 작업 범위를 허가한다. 새 대상의 실제 Ready와
   native 정책 `DecisionID` ACK를 확인한다. 수락된 미배정 요청의 처리 경로도
   검사한 뒤 퇴역 source를 제외한 새 배분표를 발행한다.
2. 전환 참여 router 집합을 기록하고 **각 router의 실제 applied**를 확인한다.
   가입 router는 현재 표를 적용하기 전에는 Service 유입에 넣지 않는다.
   전환 도중 가입/제외를 반영하고, 늦은 구버전 ACK로 장벽을 통과시키지 않는다.
3. source에 배정할 수 있었던 router 전체에서 그 source의 물리 outstanding이
   0인지 확인한다. 여기에는 readiness에서 빠진 router와 재시작 전 세대도
   포함된다. 새 표는 source 신규 배정을 막지만 이전 snapshot을 잡은 요청은
   남을 수 있으므로 그 점유까지 추적한다. 단순히 최신 snapshot의 대상 목록만
   보고 source별 점유 기록을 버리지 않는다.
4. 클라이언트 timeout/cancel이나 transport 종료만으로 점유를 풀지 않는다.
   control 관측기의 정확한 Pod UID/container·requestID native 진행/상태 증거를
   소유 router에 반영한다. router가 소실돼도 native 재대조와 보존 assigned/final로
   물리 점유를 해소할 때까지 미확인으로 남긴다. 임의 timeout 후 0은 금지다.
5. source 신규 유입 차단·모든 소유 router 0·native 물리 완료가 확인되면 drain
   완료를 기록하고 기존 stop/remove·Pod 소멸 확인으로 진행한다. reset도 같은
   순서를 사용하고 실제 효과 미확인 예약을 조기 해제하지 않는다.

적용하지 않은 router는 유입 gate/readiness를 닫아 격리하되, **유입 제외와 기존
점유 해제는 다른 사건**이다. 미확인 상태에서는 의존 퇴역만 보류하고 예약 밖의
안전한 계획/확장은 계속한다. router별 적용 시각, 버전별 요청 수와 혼합 구간을
보존하며 전역 원자 전환이나 항상 무중단을 주장하지 않는다.

native admission 상한이 최종 권위다. 초기에는 고정 참여 router 수 N에 대해
인스턴스 상한을 N개로 보수적으로 나누고 합이 상한을 넘지 않게 한다. 정수 나눔의
0 한도를 임의로 1로 올리지 않는다. N 변경도 새 표/ACK와 기존 점유를 고려하며,
새 router를 먼저 유입시켜 상한을 순간 초과시키지 않는다. native가 **실행 전
admission 거절임을 명시적으로 확인한 경우에만** 같은 버전의 다른 대상으로 1회
재배정하고 그 시도·거절을 보존한다. timeout/전달 결과 불명은 재실행하지 않는다.
정확한 전역 quota lease·새 조정 서비스는 이번 범위 밖이다.

### G3에서 확인할 native incarnation 경계

표·요청 행·진행 증거의 Pod/container identity는 정확히 고정한다. 다만 현재 native
요청/응답 계약은 Pod/container incarnation 자체를 ACK하지 않는다. 동일 Pod의
컨테이너 재시작과 마지막 관측 뒤 전달 사이 TOCTOU를 원자적으로 차단했다고
주장하지 않는다. 기존 native 이미지 변경 없이 이 경계를 더 강하게 증명할 수
없으며, G3의 재시작 실패 주입에서 명시적으로 확인해야 한다. 이를 닫으려면 native의
incarnation 검증 또는 실행 세대에 묶인 전달 endpoint 계약이 선행돼야 한다.

## 6. G3 최소 완료 기준 (재개 시, 미실행)

G3는 "안전하게 분리됐는가"와 "분리할 가치가 있었는가"를 따로 판정한다.

1. **조건 동결**: 단일 gateway 기준선과 split의 trace, native 이미지·정책·자원, SLO,
   배치, 제어 작업, 관측 창, control/router 자원·채널 한도, source 목록·볼륨 접근·수집
   종료 조건을 binding에 남긴다.
2. **요청·전환 안전**: Receive~Accept 중 표 교체와 수락된 대기 요청에서 정의·수명·버전이
   보존된다. native ACK → 실제 applied → 해당 source 신규 예약 차단 → 물리 drain 순서를
   확인한다. client timeout·채널 단절을 outstanding 0으로 바꾸지 않는다. 기록 실패 요청은
   native에 전달되지 않는다.
3. **측정 회귀**: SCH-AC-46의 G 확인 절차·판정을 재사용한다(통과점을 최대 G로 부르지
   않는다). d 1건은 기록 경로 회귀 확인으로 한정한다. raw API 자료로 요청 수·지연·점유·
   전체 offered·에너지 모집단을 재계산할 수 있어야 하고, T5의 received/finished/지연
   모집단도 같은 요청 집합을 반영해야 한다.
4. **분리 효과**: 같은 제어 작업 조건에서 탐색 중/밖 `gatewayTailNS`와 전체 지연·성공률을
   단일 gateway와 비교하고, control+router 합계 CPU·메모리·GC·기록 I/O를 공개한다.
   재개를 정당화한 문제를 해소하지 못하면 "기능 분리 완료, 효과 미입증"이며 cutover
   통과가 아니다. #213의 다른 단계 수치 하나를 임의 합격선으로 삼지 않는다.
5. **장애와 보존**: 채널 상실 시 신규 gate/readiness 차단, 미확인 점유 보존, 안전한
   종료·reset을 확인한다. 자동 Pod 교체 복구를 지원하지 않으므로 실패 종료가 합격
   동작이다. retention 경계를 지난 archive 조회와 필수 원자료 완전성을 확인한다. native
   incarnation TOCTOU는 기존 이미지로 닫혔다고 주장하지 않는다(§5).

N=2는 별도 승인 단계다. 그때 두 router의 실제 유입 참여, 고정 참여 집합 ACK, source 병합,
quota 분할의 유휴 효과를 추가한다. 연결 하나의 스모크나 ACK만으로 N=2 통과를 선언하지 않는다.

## 7. 적용 순서와 변경 기록

- 순서는 ADR 0029를 따른다: E5 완료·표 기입 → `e947cd5e` 미사용 분리 코드 정리와 단일
  source evidence API 이행 → 재개 조건 판단 → 승인된 최소 분리 검증(§6) → 별도 cutover.
  E5 보고서에는 **단일 게이트웨이·G0 통과점(최대 G 아님)** 조건을 남긴다.
- `e947cd5e`의 분리 전용 실행 경로·패키지·Runtime 분기·callback·router reader·도착 집계 정리 완료(2026-10-30).
  T5와 `internal/serving/live`, 기본 모드의 `Routable` 용량 양수 조건·live 경로 필터,
  이전 native 컨테이너의 PodUID 보존은 기존 라우팅·퇴역 안전 의미를 유지하므로 남긴다.
- 실제 cutover는 별도 승인 작업이며, router 유입 개방 전 현재 표 적용을 확인하고 이전
  단일 경로도 신규 유입 차단·물리 0·보존 완료를 확인한 뒤 닫는다. rollback도 같은 안전
  조건의 전환으로 수행한다.
- 2026-10-27: U11 G1 문서 초안 작성. 현 운영·코드·측정 결과는 변경하지 않았다.
- 2026-10-28: T5 전송을 재사용한 별도 control/router 코드, 불변 버전 적용,
  router별 기록/reader와 물리 점유 장벽을 추가했다. 기본 E5 실행 파일과 차트는
  분리 구조로 배선하지 않았다. 로컬 검사 결과는 아래 구현 기록에만 남긴다.
- 2026-10-28: 2차 검토(fusion, 패널 3모델 + 판정)로 Deferred. §0 보류 범위·최소안 추가,
  §2 Receive~Accept snapshot 보유를 명시하고 현 코드의 `GatewayPlanChanged` 거절과의
  불일치를 기록, §4.1 공통 mount 전제의 RWO 한계 기록, §6을 최소 완료 기준으로 교체,
  §7 순서를 ADR 0029와 통일. §8은 역사적 로컬 검사 기록이며 G3 통과 근거가 아니다.

## 8. G2 로컬 구현 검증 기록 (2026-10-28)

운영자의 #203 CPU 보호 창 종료 통보 뒤 다음 검사만 수행했다.

```sh
nice -n 19 env GOMAXPROCS=2 go test -p 1 \
  ./internal/serving/gateway ./internal/serving/httpapi ./internal/serving \
  ./internal/serving/kube ./cmd/chill-router ./cmd/chill-server
nice -n 19 env GOMAXPROCS=2 go test -p 1 -tags gatewaycontrol ./cmd/chill-server
nice -n 19 env GOMAXPROCS=2 go test -p 1 -count=1 -v \
  ./internal/serving/gateway -run '^TestTwoProcessSmoke$'
```

- 위 패키지 회귀와 별도 control 빌드 경로 검사가 모두 통과했다.
- 실제 로컬 TLS WebSocket의 두 OS 프로세스 스모크 출력은
  `control PID=470199 applied=smoke-plan; router PID=470218 applied=smoke-plan`이었다.
  helper 프로세스는 production coordinator/router/Runtime 적용 경로를 사용하며
  클러스터나 외부 API를 호출하지 않는다.
- 검사에는 인증 상태 구분, 재동기화/순번, 실제 적용 뒤 ACK, 느린 채널,
  15초 heartbeat 상실/Runtime 작업 중 만료, 이전 세대 custody/물리 완료,
  native ACK-only source, 원자 표 교체, 수락 요청의 버전/수명 유지,
  quota 0, source별 catalog와 local/archive 병합·누락 표시가 포함됐다.
- 최초 구현에서는 scoped `go list`로 새 package import 방향을 확인했다.
  전체 test, 아키텍처 전체 gate, 이미지 빌드, 클러스터 접근·배포 및 G3 실물/성능
  캠페인은 실행하지 않았다. 이 로컬 결과로 native incarnation 경계나 분리 뒤 G/d를
  인증하지 않는다.

### 후속 정적 검사 수정

T5/G2의 종료·정리 오류 처리, 기존 상수 재사용, 긴 표현식과 검증/채널/초기화
책임 분리를 정리했다. linter 설정이나 억제 주석은 추가하지 않았고 판정 규칙과
배포 배선은 유지한다. 승인된 빈 클러스터 창에서
`nice -n 19 env GOMAXPROCS=2 make lint` 전체 PASS를 확인했다.
동일 nice/GOMAXPROCS 조건의 `go test -p 1`은 `serving/live`, `gateway`, `httpapi`,
`serving`, `serving/kube`, `cmd/chill-router`, `cmd/chill-server`와 별도
`-tags gatewaycontrol ./cmd/chill-server` 경로 모두 통과했다.
