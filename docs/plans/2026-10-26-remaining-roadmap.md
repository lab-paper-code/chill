# 남은 길: 최종 시연에서 E5·원격 반영까지

기준: **2026-10-26 07:34Z**(갱신 08:40Z). 운영 배포는 Helm rev85(`41286c0f`: ChillServer·측정 컨트롤러·대시보드)이며 BEO 정책 불일치 종결 수정이 포함됐다. main 통합과 spec 전체 추적은 완료됐고 원격 push는 하지 않았다. 새 규칙 dispatch 점유 d10/34, 정확 prepare/remove 비용69/69, 평가0/7이다.

이 문서는 남은 것만 정리한다. 완료된 최소 수요·수동 작업 API·요청 수명·장부·관측 보존·결과 조회·상태 summary·작업 예상시간·UID selector 성능 수정·두 박자 제어 구현을 다시 할 일로 등록하지 않는다. 정본은 [scheduler SRS](../spec/scheduler/SRS.md), [구현 설계](../design/serving/scheduler-implementation.md), [측정 API](../design/serving/measurement-api.md)다.

## 1. 즉시 운영 순서

| 순서 | 남은 일 | 의존과 완료 근거 |
|---|---|---|
| 1 | ~~rev85에 BEO 정책 불일치 수정 배포~~ 완료(rev85, 08:3xZ) | 근거 `deployB/control-178-20261027/readiness-and-preservation.json` |
| 2 | ~~orangepi-017 Measuring→Failed 종결 확인~~ 완료 | 08:35:46Z `Failed/EnergyObjectivePolicyMismatch`, completedTrials=16, Run16·증거 불변, 성공 result 없음, 반복 오류 0 |
| 3 | ~~PLC orangepi 후보 근거·재측정 판정~~ 완료 | 성공 coherent 대체 `orangepi-950-plc-cpu-q4-replacement`(647aef41, 21 trial)가 planner 옵션에 연결, 실패 원본 미소비 → 재측정 불필요(`plc-catalog-report.md`) |
| 4 | ~~측정 컨트롤러 이중 실행 확인~~ 완료 | Deployment1/Pod1, 두 reconciler가 공유 Lease·worker1, 전환 컨트롤러 별도 Pod 0; 과거 creator Pod는 로그 만료로 미확정(`controller-ownership.md`) |
| 5 | 최종 제어부로 시연4회차 | 첫 구성→확장→축소, 준비·분배 시각, 전환 실패, PLC 지연, 정리·예약 반환을 같은 실행으로 보존. 극단 G 탐색 선행 불필요 |
| 6 | E5와 대시보드 실행 순서 결정 | 아래 두 계획은 병렬 준비 가능하나 실제 측정·부하와 빌드/검사 충돌은 피함 |
| 7 | E5 입력·숫자 승인→일곱 평가 실행 | [E5 계획](2026-10-26-e5-evaluation.md) |
| 8 | 최종 산출물·main 원격 push | 자격 증명·원시 대용량 제외, 배포와 소스 일치 기록, 원격 반영 결과 보존. 이 문서 작성에서는 git add/commit/push 없음 |

orangepi-017 네 번째 trial의 생성 경위는 로그 부재로 미확정이다. 이력 복원을 기다려 명시적 실패 종결을 막지 않는다. [시연 결과](../reports/demos/README.md), [구조 검토](../reports/structure-review-20261026.md), [성능 원인](../reports/gateway-performance-20261026.md)을 후속 근거로 읽는다.

## 2. E5 앞에 남은 측정과 입력

- **d24개:** 새 규칙10/34에서 남은 것은 MNV(MobileNetV2) CPU8 + GPU5 + PLC11개다. 초기 후속 목록의 PLC12 중 genesis2 D06 새 Accepted1개가 완료돼24가 남았다. 실제 profile/Node UID/runtime/모드가 맞는 원시 terminal과 소비 snapshot으로 갱신하며 숫자만 맞추지 않는다.
- MNV CPU8은 현재 소비를 유지하는 legacy이며 새 규칙 인증 완료가 아니다. GPU5에는 승인된 legacy GPU2 재측정을 포함한다. 기존 원본을 새 Accepted 전 삭제하지 않는다.
- d 창 경계와 요청 동시성 문제가 남은 측정의 방해 여부를 현재 계약으로 확인한다. 조건표(실제 batch·대기 깊이·수명·표본N·창·허용 유휴)를 대상별 실측에서 계산하며 판정 완화로 Accepted를 만들지 않는다.
- d 계수는 유휴1%, 양측95%, 여유1.25, 요청당 유휴1ms, 본창360초/준비60초 하한/cap3600초의 승인값을 유지한다. 상세와 실제 경계 처리는 [측정 명세](../spec/00_measurement.md)와 [측정 설계](../design/serving/measurement-api.md)를 따른다.
- 정확 비용69/69를 모든 전환 사슬의 SLO·에너지 이득 인증으로 해석하지 않는다. 최종SLO/m로 κ/R·전환 witness를 새로 맞춘다.
- PLC/Smol 최종 분위수·SLO, 공통 완료 여유m, 요청 수명·socket·gateway cap, 혼합 부하 통과 범위, 정적 기준선·제어요소N은 [E5 결정 대기 표](2026-10-26-e5-evaluation.md#아직-결정측정이-필요한-숫자)를 채운다.
- 게이트웨이 증설안은 철회됐다. 처리 한계 자동 측정 API나 극단 한계 캠페인은 추가하지 않는다.

## 3. 미확정 환경 사건과 증거 완전성

### edge-metrics 간헐 읽기 시간 초과

04:35:45–46Z에 rasp5/jetsonx/jetson-nano/lattepanda의 공통 edge-metrics endpoint 읽기 실패5개가 있었다. 첫4개 감지 시각은27.966µs 범위, jetsonx 추가 실패는0.759초 뒤다. 약700ms timeout은 원천 장치 고장 시작 시각이 아니라 관찰자 반환 시각이다.

- 다음 진단은 관찰자 스케줄링·공통 네트워크/서비스·개별 장치 중 어느 경계인지 구분할 동시 자료에 집중한다. 단일 endpoint 재조회 성공으로 원인을 종결하지 않는다.
- 후속04:40:57Z HTTP200(21–60ms)은 그 시점 회복만 뜻하며 누락 전력 구간을 복구하지 않는다.
- E5에서 `tolerateArchive`는 금지된다. 재시도·timeout 상향을 근거 없이 추가하거나 전력 결측을0으로 넣지 않는다.

### rasp5 끊김

03:00:31Z NodeNotReady→03:05:12Z Ready, boot 불변. 원인은 미확정이다. 전력 읽기 timeout보다 약90분 먼저 회복해 알려진 구간은 겹치지 않는다. 동일 호스트 원인 가능성은 열려 있으나 공통 원인으로 단정하지 않는다. 호스트·네트워크·kubelet 근거가 추가로 필요하다.

노드 끊김 구간 표시와 별도 제외 집계는 E5 사전 결정 항목이다. 사후 유리한 구간만 골라내지 않고 전체 offered 결과를 유지한다.

### 측정 Job·Pod 정리의 남은 경계

기존 성공 Job1,012개는 옛 retained loader가 Job의 evidence-store binding을 읽기 때문에 삭제 보류다. 끝난 Pod만 삭제해1,049→73으로 줄였고 표본5개 읽기는 성공했다. 신규 Run의 증거 확정 후 Background Job 삭제·실패 보존기본86,400초는 이미 구현됐다. 옛 Job 전체 삭제를 다음 작업으로 되살리지 않는다. 추가 정리가 필요하면 loader 독립성 근거와 사용자 승인을 먼저 확보한다.

## 4. 구조 검토에서 남은 국소 수정

각 항목의 근거·외부 조사·발견 당시 file:line은 [구조 검토](../reports/structure-review-20261026.md)를 따른다. 아래는 구현 완료 주장 없이 남은 위험을 분리한 목록이며, 모두를 E5 앞 필수 재설계로 묶지 않는다. 실제 차단/안전 영향이 확인되면 선행하고 그 외는 E5 뒤로 둔다.

| 우선 | 항목 | 의존·경계 |
|---|---|---|
| 국소 안전 수정 후보 | K2-2 movingNodes 누수 | detach 오류 반환보다 앞서 해제 등록. 새로운 실행 엔진 불필요 |
| 국소 정합 수정 후보 | K2-1 정책 이중 쓰기 부분 성공 | ConfigMap/Pod 권위·미완료 책임 정의 후 재조정. 객체별 잠금만으로 해결했다고 하지 않음 |
| 국소 정리 수정 후보 | K2-3 Pod 뒤 ConfigMap 잔재 | cleanup 책임 주소를 먼저 없애지 않음. 기존 finalizer/ownerReference 재사용 |
| 복구 경계 후보 | K1-2 종료 사건 유실, K4-5 기록 오류 래치 | 완전한 현재 상태·실행 신분 또는 final 기록/sync 완료가 해제 근거; timeout으로 소유권 삭제 금지 |
| 관측 경계 후보 | K1-3 전력 신선도, K4-6 초기 메타데이터 실패 결합, K1-5 로그 통로 장애 | 원천 시각·대상별 품질과 물리 실재를 분리. K1-4 native probe 병렬화는 이미 반영됐으므로 제외 |
| 진행성 후보 | K4-2 유한 작업/RPC deadline | timeout 뒤 효과 불명·예약 유지 처리 함께 설계 |
| 제어 계산 후보 | K5-1 부분 가중치, K5-2 요청 나이, K5-3 backlog 중복 | 현재Ready source+새 목적지의 실제 용량, 잔여compute·수명, ingress/Q 구분 |
| 예약 복구 한계 | release의 Before.Native 미저장 | Pod 유지 중 효과 재확인 해제 제한. route 복구 완료와 별개 |

**승인 대기 구조 변경:** 같은 진행 owner 아래 additive prepare+Ready subset 합류로 해제 Node 보충 확장; Tick 재진입 대기 축소; 순차 정책ACK 병렬화. 첫54초 분해에서 Tick4.3초·prepare26.7초·부분 장벽10.4초·ACK15.6초·관측/적용약3초가 관측됐다. 부분 목표 의존성 장벽은 이미 수정됐으므로 다시 미구현으로 세지 않는다.

**보류:** κ 대기 Workload의 Node 선점 금지(B안), reset 자동 재시도/rollback, ObservationLog writer marshal 잠금 변경, 통계 신뢰구간 교정·known bias 원인조사·추가 전체 감사. 기존 안전·증거 계약을 바꾸는 것은 별도 결정이다.

## 5. 대시보드와 저장소 재현성

- T5 WebSocket 서버·앞단·브라우저와 T6 라이브 무대·재생·운영자는 [별도 재개 계획](2026-10-26-dashboard-live-stage.md)을 따른다. 장부 예약 표시 요구를 포함한다. 보존 WIP는 그대로 적용하지 않는다.
- 기록 재생은 근거 저장소 조회이며 라이브 갱신을 폴링으로 구현하지 않는다. 첫 메시지 인증이 사용자 결정이다.
- 옛 병합 GAP의 main fast-forward·spec 추적·handoff 증류는 이번 이관으로 중복 계획하지 않는다. 운영 values/이미지별 digest·runtime worker 소스 정합·비공개 입력 생성 절차는 최종 배포 재현성 확인 시 현행 배포 문서와 대조한다.
- `feat/loadgen-async` 포함 여부와 runtime worker 재빌드 필요성은 옛 미결 목록에 있으므로 실제 E5 도구의 현재 동작/이미지 계약에 근거해 판단한다. 지연 응답 때문에 offered trace가 줄면 유효 평가가 아니며 임의 branch 병합을 해결로 단정하지 않는다.
- 빌드 대상·CRD 설치 목록·EdgeMesh 선언 등 옛 GAP은 현행 코드에 이미 반영된 항목을 다시 구현하지 않는다. 실제 재설치/배포의 남은 차이만 별도 기록한다.

원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 `merge-gap-and-remaining-20261025.md`, `workflow-plan-20261025.md`, `api-extension-proposal-20261025.md`, `d-condition-proposal.md`, `d-code-impact.md`, `d-measurement-decision.md`, `e5-numbers-proposal-20261025.md`, `edge-metrics-timeout-20261027.md`, `checkpoints.md` 1950–2005행. 과거 계획은 후속 사용자 결정·현재 코드로 대체했으며 원시 목록·CSV·Secret은 옮기지 않았다.
