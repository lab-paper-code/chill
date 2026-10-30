# CHILL 아키텍처 재검토 제안 — B1·B2·B3

상태: **proposal, 승인 대기**. B1·B2와 M0–M5는 권고안이며 이 문서만으로 채택·명세 변경을 확정하지 않는다. 현행 계약은 [시스템 명세](../../spec/README.md)와 각 도메인 설계가 권위 원본이다.
근거 기준: main `99621a6` / `feat/workload-pools` `42a7f98`, 당시 미커밋 workload-pools·ADR 0022 검토. 관측: 판단 ~10MB, ChillServer 128Mi OOM·이력 조회 15분 초과, PVC 8Gi 포화; 원인은 전역 재생×입력 전체 기록×다중 writer이며 10MB·JSONL scan은 명세 요구가 아니다.

## 대안과 보존할 의도
- **B1**: ms~s router·EDF 배칭과 30–60초(위반 신호 시 조기 호출) 용량 계획 분리. Workload–공유 노드 연결 성분별 계획, 꺼진 Workload 구성 고정·영향 시 SLO 평가; 측정 기반·SLO 우선·전환+Serving 에너지·노드별 유휴분 1회·완료여유·가용 노드 최저 모드는 유지한다. 재생은 삭제하지 않고 용량 계산·상위 K 검증(예: 3×20회)·오프라인 simulator로 남긴다([용량표 계획기](../serving/analytic-planner.md)).
- **B2**: 판단 알고리즘과 직교하는 노드 의도 단일 writer(ChillServer)와 관측 단일 writer(agent), SSA 소유권·grant generation fencing. `chill.dacs.io/measurement:NoSchedule`로 측정 admission, 서빙 노드 거절·terminal/삭제 시 반환; `spec.nodeName` 경로도 ChillServer가 배제한다. `SCH-34`, `SCH-AC-26`, `DB-FR-34` 의미와 측정 Pod 목록·S3 증거는 유지([측정 admission](../serving/measurement-admission.md)).
- **B3**: Workload autoscaler + 노드 전력 governor + 분 단위 consolidator(`ΔE(H)>E_trans`)의 비교 기준선(`SCH-32`). 진동·전역 최적 손실 위험으로 주 정책은 권고하지 않으며, 주 정책화는 `SCH-02/15/16` 약화(⑤)가 필요하다.
- B1 위험은 RPi5·LattePanda·RK3588의 꼬리 지연·열 throttling, SmolVLM2 타일 수 변동과 보수 상한의 과대 프로비저닝이다. shadow 검증 전 재생을 주 평가기로 유지한다. 활성 POOL의 인스턴스 도출과 workload-pools commit 1–5(외부 Pod safety 제거, 완료여유, Workload별 자동제어, 측정 근거 자기 선택, CHILL Pod 기록)는 유지한다.

## M0–M5 이행안
| 단계 | 경계·근거 | 명세 변경 |
|---|---|---|
| M0 | [판단 기록 다이어트](../serving/decision-record-diet.md): Keep/Target 전체·나머지 요약, profile ID 참조, snapshot arrival 제거·재생 시각 원값과 `evaluationElapsedNanos` 유지. 설계 측정 18.8MB→약70KB, 목표 ≤100KB | 불필요: `SCH-30`의 연결 제공 |
| M1 | [관측 분리](../serving/observability-split.md): 요청당 wide row, 불변 ID, 시간별 gzip/S3·2시간 버퍼·구간 조회·오프라인 재집계(`SCH-31`) | 불필요 |
| M2 | [node agent](../platform/node-agent.md): discovery·전력·census DaemonSet 통합, inventory 자동화·수작업 ConfigMap 제거, 실제 전력 적용 로직 유지 | 불필요 |
| M3 | B2 측정 admission·grant·자동 반환, `serving-operation`은 ChillServer 클라이언트, 전환 비용 runner 자동 생성. M3 전 taint는 운영자가 관리 | 불필요 |
| M4 | option·SLO별 재생 용량 κ를 사전 계산, Tick은 κ 합·노드 계획·이웃 탐색. 오프라인/shadow 통과 후 전환; 재생·동작 DAG·prepare/route/drain/remove 유지 | ① `SCH-13/14`, ①' 00·04의 지연 평가, ② `SCH-16`, ③ POOL 에너지, ④ `SCH-23` 승인 필요 |
| M5 | [정리](../platform/cleanup-m5.md): Measurement CRD 통합·NodeActuation 제거(ADR 0019), Go 바이너리/runtime image 분리. Workload·ModelSpec·Dataset·Preprocessor와 firstfloor·runner·S3 유지 | 불필요 |

②는 동일 노드·구간의 공통 에너지 항만 소거하므로 후보 순위는 같고 A에도 적용 가능하다. 전체 POOL 에너지는 실측 보고하며 영향 없는 baseline 누락으로 전체 Pending을 만들지 않는다. ①·①'은 평균 처리량/분위수 단순 합산 금지를 유지하고 측정 기반 보수 평가를 허용, ④는 actual/replay 대신 actual/평가 모델에 같은 deadlinePolicy(drop/keep/defer, 기본 keep)를 적용한다. `SCH-05/15/17/18/25/26/27/30/31/33/34`, 공유 노드의 같은 등록 모델 제약과 동작별 관측은 유지한다.
## 검증과 미해결 승인
수치 문턱은 명세 요구가 아닌 설계 제안이다. 오프라인 target 일치 또는 에너지 차 ≤5%·재생보다 낙관적 SLO 판정 없음, ≥95% 창에서 예측≥실측·에너지 손해 ≤5%, 열 상태별 `l_q(b)` 변동이 SLO 여유 안, 기록 ≤100KB·RSS limit 미만·OOM 0, 측정/prepare 1000회 겹침 0·자동 반환, B3 버스트/급감 진동·에너지 비교가 검증 대상이다. `SCH-AC-24/25/26`은 첫 구성·공존·측정 admission을 확인한다.
P10은 E5→M5→main 병합→해당 main image 빌드→최종 배포 뒤 source commit·image·release revision을 고정해 판정한다. 병합 전 E5는 최종 배포 검증을 대신하지 않는다. E5는 시작·일정·버스트·증가(0.5/0.7/0.9 `R_w`)·한계·급감/재급증·1시간 soak, 총 요청률≤게이트웨이 한계 G; 원시 요청·판단·열·부하·프로파일·설정·seed와 Tick p50/p99·CPU/RSS·저장 증가를 같은 시간축으로 보존한다. testbed 변경은 사용자 요청이 있을 때만 한다.
| 미해결 ID | 승인할 내용 |
|---|---|
| D1 | E5-1/3/5/6의 단계 백분위 또는 1분 창 충족률·문턱, 전환 포함 여부 |
| D2 | PLC·SmolVLM2 SLO 백분위와 L 승인; tight p95 1s는 기존 조건 |
| D3 | 위 5%·95% 예시 문턱 확정과 재생이 Tick에서 빠진 뒤 비교 기준 해(전수 최소/기준선/예측-실측) |
| D4 | 예측 충족을 `κ_new≥λ`·`Overload` 없음으로 둘지(판단별 예측 백분위는 없음) |
| D5 | `budgetCut` 불일치 별도 집계 여부와 낙관성 검증을 실측 비교로 대체할지 |
| D6 | 열 상태(throttle active/latched/온도)·SLO 여유(`L−(l_q(b)+b×d+m)`)·5초 표본 간격 |
| D7 | E5 Tick 목표: E1 기준 p99≤100ms·CPU500m을 유지할지; E5 탐색 예산은 제어 주기 절반 |
| D8 | fake-client `-race -count=1000`을 envtest 대신 인정할지, 실클러스터 agent kill 포함 여부 |
| D9 | B3 구현/정적 구성·제어요소 끄기/생략 중 기준선과 재실행 단계 |
| D10 | `SCH-AC-25`를 kind E2E로 닫을지 실클러스터 E2를 별도 수행할지 |
| D11 | E5-6 세 Workload의 예측-비교 집합 실측 에너지 차 ≤2% 적용 여부 |
