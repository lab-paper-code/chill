# 05. 채택과 단계적 이행

## 결정

**사용자가 B1과 B2, 아래 M0–M5를 채택했고, B3는 비교 기준선으로 둔다.**

- B2는 상태 소유(누가 노드를 쓰는가), B1은 판단 알고리즘과 데이터(무엇을 어떻게 평가·기록하는가)를 다루므로
  서로 직교한다. 하나만 먼저 해도 된다.
- 순서는 효과가 즉시 나고 명세 변경이 필요 없는 것부터다(M0 → M3), 명세 변경이 필요한 B1 전환(M4)은 shadow
  검증 뒤에 한다.

## 단계

| 단계 | 내용 | 명세 변경 | 그대로 남는 코드 |
|---|---|---|---|
| **M0 출혈 차단** (며칠) | `recordDecision`(`cmd/chill-server/scheduler_service.go:364-369`) 다이어트, 설계는 [판단 기록 다이어트](../serving/decision-record-diet.md):<br>• 구성 기록(snapshot)은 구성이 바뀔 때와 window 첫 판단에만 저장, 판단은 그 시각을 참조<br>• 후보는 Keep/Target만 상세, 나머지는 요약(ID, status, J, worst exceedance, reasons, 동작 수)<br>• `SchedulerInstance.Profile`은 profile ID로 대체(workload-pools §4.5 확장)<br>• `Arrivals`는 저장하지 않고 창 경계와 개수(`search`)로 충분(원본은 요청 stream에 있음)<br>• `ArrivalAges`는 인스턴스당 Workload별 값 2개 이하라 원값 유지<br>• 판단 stream 분리, 크기 기반 retention(게시된 window만 삭제, S3에 보존)<br><br>18.8MB → 약 50KB(설계 2, 3장의 측정과 추정) | **불필요.** `SCH-30`은 "연결해 제공"이라 불변 근거의 ID 참조로 충족 | 전부 |
| **M1 관측 분리** | ChillServer·firstfloor `/metrics`(native histogram), VictoriaMetrics 또는 Prometheus 하나, Dashboard 시계열을 PromQL로 전환, 요청당 wide row + 시간 단위 zstd 세그먼트(S3) | 불필요 | observation log의 원시 보존 역할(형식만 변경) |
| **M2 node agent** | discovery·전력·census 통합 DaemonSet, inventory 자동화, ConfigMap 수작업 필드 삭제 | 불필요 | NodeActuator의 실제 전력 적용 로직 |
| **M3 B2 측정 admission** | workload-pools §3.3의 taint `chill.dacs.io/measurement`를 운영자 대신 단일 writer가 측정 객체에 따라 붙이고 뗌, 서빙 노드 거절, fencing generation, `serving-operation`을 ChillServer 클라이언트로, 전환 비용 runner 자동 생성 | 불필요 | 측정 controller의 측정 본체, Job, S3 evidence, taint key와 toleration, ChillServer의 taint 노드 제외 |
| **M4 B1 shadow → 전환** | 분석형 평가기를 재생과 나란히 돌려 불일치를 기록. 기존 10MB 기록으로 오프라인 A/B. 통과하면 탐색은 분석형, 재생은 상위 K 검증기로 | **필요**: 06 문서 ①, ①', ②, ③, ④ | `scheduler_replay.go`(검증기·오프라인 simulator), 후보의 동작 DAG, `internal/serving/kube/operations.go`의 prepare/route/drain/remove |
| **M5 정리** | Measurement CRD 통합, NodeActuation CRD 삭제, Go 바이너리와 runtime image 분리 | 불필요 | Workload, ModelSpec, Dataset, Preprocessor CRD |

명세 변경 ②(공통 항 소거)는 A에도 적용되며 workload-pools 설계에 반영되어 있다.

## workload-pools 설계(feat/workload-pools, 미커밋)와의 관계

commit 번호는 workload-pools.md §8 기준.

| commit | 판단 | 이유 |
|---|---|---|
| 1 `feat(nodeactuator): actuate regardless of external pods` | 유지 | 외부 Pod safety 삭제. B2와 일관 |
| 2 `refactor(serving): replace completion calibration with a configured margin` | 유지 | 수동 calibration 부트스트랩 제거, B1의 완료여유와 같음 |
| 3 `feat(serving): derive active pools and control autonomy per workload` | 유지 | 인스턴스에서 활성 POOL 도출(level-based), Workload별 자동제어 |
| 4 `feat(serving): derive candidates and costs from completed measurements` | 유지 | 측정 근거 자기 선택, 채택·비용 선언 삭제 |
| 5 `feat(measurement): record CHILL-placed pods in measurement results` | 유지 | 측정 결과의 노드 Pod 목록과 CHILL 배치 여부(`SCH-34`). B2와 독립 |

- §3.3의 측정 POOL은 taint `chill.dacs.io/measurement:NoSchedule`이고, M3 전까지 운영자가 붙이고 뗀다. 측정 Pod는
  toleration을 갖고, ChillServer는 taint 노드에 배치, 내림, 전력 변경을 하지 않는다. `edge.dacs.io/serving-pool` label과
  membership은 commit 3에서 삭제된다. M3는 taint의 추가와 제거, 서빙 노드 거절, 전환 비용 runner 자동 생성을 맡는다.
- §3.4는 명세 변경 ②에 따라 후보를 상태가 바뀌는 노드의 차이로 비교하고, 가용 노드 내림(`Fixed`)은 공통 항으로
  비교에서 빠진다. 측정 POOL 노드 공통 항과 내림 제외 assumption은 두지 않는다.
- §4.5의 "저장 기록은 profileID만"은 M0의 일부로 확장한다.

## 그대로 남는 것 (전체)

firstfloor batcher(EDF, deadlinePolicy)와 Python runner, Go serving-worker, ChillServer ingress·router 가중치,
profiling Job과 profile derivation(curve plan), S3 evidence store, 전환 실행기(prepare/route/drain/remove),
재생 코드, Dashboard, 완료 여유 설정, 측정 근거 자기 선택, 인스턴스 기반 활성 POOL 도출.
