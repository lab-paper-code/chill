# 워크로드별 풀과 자동제어

활성 풀은 실제 인스턴스의 owner와 binding, 측정 풀은 Node taint에서 도출한다. 별도 membership 저장소나 Pool CRD를 만들지 않는다. 요구사항은 [스케줄러 명세](../../spec/scheduler/SRS.md), [측정 명세](../../spec/00_measurement.md)를 따른다.

## 자동제어와 소유권

`settings.autonomousWorkloads`는 등록 Workload UID 목록이다. settings PUT은 미등록 UID를 제거한다. 동명 재등록은 새 UID로 기본 비활성이다. 제어요소는 scheduler 전체 설정이며 Workload spec은 실행 조건과 SLO를 선언한다.

| 판단 상태 | 의미 |
|---|---|
| `Disabled` | 자동제어 꺼짐. 소유 노드는 keep |
| `MeasurementEvidenceMissing` | 활성화됐으나 option 없음. 다른 Workload 판단은 계속 |
| `Controlled` | 활성화됐고 측정 option 존재. 수요와 SLO 평가 대상 |

계획은 노드당 인스턴스 하나, 인스턴스당 Workload 하나다. 동일 실행 조건의 profile은 재사용할 수 있으며 부하, 대기, SLO와 결과는 UID별로 유지한다.

활성 풀은 `OwnerWorkloadUID` 또는 binding이 해당 Workload인 실물의 Node 집합이다. 준비와 drain, cleanup 중 미반환 점유도 남긴다. Ready 조회 실패를 철수나 반환으로 해석하지 않는다. owner와 binding이 없는 잔류 인스턴스는 정상 정리 proof를 거친다.

## 측정과 전체 풀

`chill.dacs.io/measurement:NoSchedule` taint 값은 승인 root UID다. ChillServer가 같은 변경 권한으로 붙이고 반환 뒤 제거한다. [측정 승인](measurement-admission.md)의 노드별 순서와 hand-back을 따른다.

serving Pod는 nodeName을 쓰므로 capture와 실행 직전 검사에서 격리한다. 일반 prepare와 전력 변경은 `NodeInMeasurement`다. 승인 worker와 runner만 toleration과 owner 범위에서 실행한다. terminal CHILL serving Pod도 삭제 전 승인 거절 근거다. 외부 Pod는 삭제하지 않고 census와 non-serving 예산에 남긴다.

전체 풀은 control-plane label이 없는 모든 Node다. 후보 제외 노드나 측정 노드도 전체 전력 범위에 남는다. 제품 제어 호스트 비용은 [실클러스터 수용 기준](scheduler-operations.md#실클러스터-수용-기준)에 별도로 포함한다.

가용 Node는 측정 밖 Ready이며 serving 실물이 없다. unloaded NodeBaseline 중 최소 W, 동률 이름순으로 최저 모드를 고른다. 빈 노드 새 절전도 같은 H에서 gain과 minimumGain, 개별 payback과 actual proof를 통과해야 한다.

| 사유 | 영향 |
|---|---|
| `PlacementInventoryMissing` | GPU inventory나 예약이 불완전해 배치 제외 |
| `PlacementBudgetUnavailable` | 외부 GPU 소유 불명이나 memory 예산 확인 불가 |
| `PowerModeObservationUnavailable` | 실제 전력모드 readback 불가 |
| `PowerTransitionUnavailable` | 모드는 확인했지만 요청 가능한 전환 근거 없음 |
| `InstanceUnavailable` | 해당 native나 profile 관측 불가 |
| `ExcludedNode` | 운영 제외. keep과 실물 점유 유지 |

전력 세부 사유는 기존 `PowerModeUnavailable`과 함께 노출한다. fixed 전환 비적용과 관측 실패를 구분한다. 한 노드 실패는 독립 노드 판단을 막지 않으며 memory와 physical GPU 제약은 유지한다.

## 프로파일과 비용

catalog는 허용 namespace의 불변 BEO와 검증 raw에서 profile을 도출한다. public profile은 실행 조건별 성공 근거를 보존한다. 현재 측정 선택은 명시 replacement와 셀별 최신 측정을 사용하며 최신 실패를 과거 성공으로 대체하지 않는다.

runtime image와 관측 version, 모델과 전처리, Dataset, Node와 전력모드, actor와 batch 조건을 유지한다. 준비는 frozen image와 memory envelope를 쓴다. CPU quota나 affinity 관측을 자원 보장으로 matching하지 않는다.

동작 비용은 record 자신의 meter와 Node, scope, actual 상태와 방향, native와 격리 근거를 검증한다. 현재 meter 선언으로 과거 provenance를 덮지 않는다. 같은 조건의 최신 유효 측정을 우선한다. timing 추정과 에너지 결정 가능성은 별도다.

비교 에너지는 keep 외 적격 계획이 있는 `ComparedNodes`의 공통 항을 소거한다. 실제 전체와 활성 풀 joules는 기록 meter, 실제 소속과 coverage로 집계한다. 미확인 전력과 공백을0으로 바꾸지 않는다.

## 판단과 적용

수요는 장기와 최근 도착률 중 큰 값에 미실행 대기의 기여를 한 번 더하고 `minimumDemand` 하한을 적용한다. [계획기](analytic-planner.md)는 모든 후보에 같은 수요와 요청 모집단을 사용한다.

분배 후 저비용 탐색과 정밀 재생을 구분한다. 전체 충족 우선이며 미충족은 기한 내 미완료 비율, 유한 성공 초과와 조건부 potential 순이다. 현재 κ̂=0은 단독 배치 거절 근거가 아니다.

Controlled는 무부하에도 최소 인스턴스 하나의 배치 조건을 유지한다. 이동은 remove와 prepare DAG로 실행한다. accepted 요청과 retained 공급, bridge, reserve와 정상 반환을 유지한다.

TOTAL과 POST는 Workload의 관측 SLO percentile에서 구한다. 유효 표본이 없으면 completionMarginMS로 초기화한다. 실제 요청의 POST는 접수 때 동결하며 이후 변경은 소급하지 않는다.

## 조회와 수동 운영

`GET /api/v1/pools[/{id}]`는 실제 소속과 인스턴스, 관측 시각과 issue를 제공한다. 소속, Ready와 routable은 별도 사실이다. `/api/v1/scheduler/state`는 제어 상태, Node 사유, 비용 문제와 비교 집합을, `/api/v1/scheduler/profiles`는 profile을 제공한다.

비자율 Workload 배분은 route, unroute와 release 작업을 사용한다. settings PUT의 staticRouting은 읽기 전용이라 거절된다. 정리는 reset과 actual readback을 사용한다. [수동 원자 실행](scheduler-operations.md#수동-원자-실행)을 따른다.

대량 수집 전 대표 측정의 생성, raw와 projection, planner 소비와 실제 반환을 확인한다. 같은 비교의 trace, seed, 요청 수명, SLO와 최소 수요, 시작 상태를 동결한다. 수치 예측을 실측 수용 결과로 보고하지 않는다.
