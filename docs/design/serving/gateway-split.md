# 게이트웨이 제어·데이터 평면 분리 (G1 draft)

**상태: Draft — 2026-10-27, E5 뒤 적용 전제.** 이 문서는 G2 구현과 G3 검증을
위한 설계 초안이며 구현·배포·통과 증거가 아니다. 현재 운영 큐의 rev97 retention
→ canonical SLO cutover·capacity 재생 → G0 확인 → U2 재개 순서는 건드리지 않는다.
E5 중 rollout·측정 조건 변경·호스트 빌드·테스트를 실행하지 않는다.

근거는 `O/intent/gateway-20261027/proposal.md`(목표와 배분표 계약), 같은 폴더의
`arch-map.md`(현행 경로), `research.md`(참고 사례)다.
`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`.
결정은 [ADR 0029](../../adr/0029-gateway-control-data-plane-split.md), 요구사항과
AC는 [scheduler SRS §8](../../spec/scheduler/SRS.md)의 SCH-60~66 / SCH-AC-47~51에 둔다.
현재 [라우팅](../../spec/03_routing.md)·[전환](../../spec/05_transition_control.md)·
[관측](observability-split.md)의 의미는 유지한다. 이 초안은 그 원본들을 소급 변경하지 않는다.

## 1. 모드와 책임 경계

같은 `chill-server` Go 바이너리/이미지에 명시적 `control`/`router` 실행 모드를
두고 서로 다른 Deployment로 실행한다. 이는 G2에서 추가할 모드이며 현행 flag가
이미 있다는 뜻이 아니다. cutover 뒤 새 배치에서 결합 모드나 요청별 중앙 picker를
병행하지 않는다. native runtime 7 이미지와 프로파일은 이 변경으로 재빌드하지 않는다.

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

배분표는 다음 논리 필드를 한 묶음으로 갖는 불변 전체 snapshot이다. wire DTO는
G2에서 기존 타입을 재사용해 표현하되 정의/정책/배분표 identity를 합치지 않는다.

| 정보 | 의미 |
|---|---|
| `planVersion` | 서로 다른 본문에 재사용하지 않는 배분표 identity. control 재시작을 가로질러도 충돌하지 않음 |
| Workload UID, `definitionID`, 정의/요청 수명 | 같은 이름의 다른 등록 건·모델/입력/SLO를 분리하고 최초 수신 기준 예산 유지 |
| Pod UID, container identity, node, 전용 endpoint | 이름 재사용·다른 실물로의 잘못된 전달 방지 |
| 가중치, 정책/프로파일 참조, router별 점유 한도 | 실제 Ready 및 native 정책 ACK된 대상과 router 로컬 분배에 필요한 값 |

기존 native `DecisionID` ACK 후에만 해당 정책에 의존하는 snapshot을 발행한다.
정의·참조·대상·가중치·상한을 모두 검증하고 한 번의 포인터 교체로 적용한다.
요청은 사용할 snapshot을 잡은 뒤 끝까지 그 snapshot을 보유하며 요청 행에
`planVersion`을 남긴다. 이후 새 표가 와도 이미 잡은 요청의 정의·수명·배정 버전을
바꾸지 않는다. 같은 버전으로 안전하게 처리할 수 없는 수락 요청이 있으면 기존
handoff 보호 조건에 따라 전환을 보류하고, 임의로 새 정의/대상에 옮기지 않는다.

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

[Dashboard T5 설계 §4.3~4.5](../dashboard/README.md)의 **TLS WebSocket,
인증/권한 확인, bounded 송수신 큐, 순번/checkpoint, heartbeat, 재연결 시 snapshot
재동기화** 메커니즘을 재사용한다. T5도 설계/구현 상태를 구분해야 하며, 이 문서는
동작 중인 공통 채널이 이미 있다고 주장하지 않는다. 새 broker·gRPC·xDS 서버를
별도로 도입하지 않는다.

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

## 6. G3 테스트 계획 (미실행)

아래는 E5 뒤 G2 구현을 마친 후 main agent/승인된 운영 담당자가 실행할 계획이다.
이번 G1에서는 코드·클러스터 변경, 호스트 빌드·테스트·부하 재실행이 없다.

| 검사 | 입력/실패 주입 | 통과 증거 | 연결 AC |
|---|---|---|---|
| 모드/신뢰 경계 | control 1 + router 2, 인증/입력 오류, 동시 infer 중 control 조회·복원 | router에 계획/관측 복원 루프 없음, control 본문/picker 홉 없음, 기존 오류·수신 기준 시간 의미 유지 | AC-47 |
| snapshot 원자성 | 서로 다른 정의·identity·가중치 v1/v2, in-flight/미배정 동시 처리 | 요청별 한 버전, 부분 혼합 없음, 구버전 요청 보호·새 Pod identity 혼동 없음 | AC-47 |
| ACK 순서/멤버십 | native ACK 지연, B applied 지연·중복·구버전 ACK, C 가입 | native ACK 선행, 모든 현재 참여 router 적용 확인, C 적용 전 gate 닫힘 | AC-48 |
| drain/reset | A source=0, B=1, client timeout·B 단절/재시작·늦은 native 완료 | 미확인/양수면 stop/remove 0회, 이전 세대 포함 모두 0·native 완료 후 정리, 장부/예약 유지 | AC-49 |
| 채널 안전 | 무권한/잘못된 TLS, heartbeat 상실, seq 공백·slow consumer, control 재시작 | 인증 전 데이터 0, 권한 분리, 신규 gate 닫힘·재동기화, 오래된 ACK/0 재사용 없음 | AC-48, AC-50 |
| 기록/reader | 두 router, assigned 실패, late final, local/archive 중복·누락, 삭제/재등록 UID, 저장소 오류 | 배정 전 기록, 합친 원시 집합과 수/지연/점유 일치, 라우터별 coverage, 오류를 0으로 숨기지 않음 | AC-51 |
| 확장·전환·장시간 | router 2개 고정 후 확장/퇴역·전력 전환·재시작·보존 시간 경계 | 실제 가중 분배·점유·SLO와 G, CPU/메모리/기록 공백 공개; 혼합 버전 시각 보존 | AC-47~51 |
| 측정 회귀 | 승인된 동일 조건의 d 측정 1건과 G 재측정, E5 reader 사후 재생 | λ/d·SLO/전체 offered·전체 POOL 에너지가 다중 소스를 한 번씩 반영, 구조/설정/G를 binding에 고정 | AC-51 |

절차/값은 기존 G·d·전환·장시간 시험을 재사용하고 split 구조와 router 수를
고정한다. 숫자 목표를 임의로 새로 만들거나 과거 단일 gateway G를 재사용해
split 성능을 인증하지 않는다. stale 유입, ACK/점유 미확인 또는 필수 원자료
결손은 실패/무효이며 성공한 일부 router 결과로 전체 통과를 대체하지 않는다.

## 7. 적용 순서와 변경 기록

- E5는 현 구조로 완료하고 보고서에 **단일 게이트웨이·측정한 G** 조건을 남긴다.
  이 G1 draft의 존재는 운영 적용 승인이 아니다.
- G2는 모드·T5 전송·버전/ACK·다중 stream/모든 reader·drain 조건을 함께 이행한다.
  한쪽만 옮긴 혼합 구조를 완료로 보고하거나 옛 결합 경로를 호환용으로 남기지 않는다.
- G3는 위 검사를 수행하고 원시 근거를 보존한다. 실제 cutover는 별도 승인된
  작업이며, router 유입 개방 전 현재 표 적용을 확인하고 이전 단일 경로도 신규
  유입 차단·물리 0·보존 완료를 확인한 뒤 닫는다. rollback도 현재 점유/기록을
  건너뛰는 배포 축소가 아니라 동일한 안전 조건의 전환으로 수행한다.
- 2026-10-27: U11 G1 문서 초안 작성. 현 운영·코드·측정 결과는 변경하지 않았다.
