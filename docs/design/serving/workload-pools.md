# 워크로드별 POOL과 자동제어 구현 설계

## 1. 목적과 결정

요구사항 원본은 [시스템 명세](../../spec/README.md), [측정 POOL](../../spec/00_measurement.md), [scheduler SRS](../../spec/scheduler/SRS.md) `SCH-02,08,09,13,15,16,21,32,33,34`, `SCH-AC-22,24,25,26`, [Workload SRS](../../spec/workloads/SRS.md) `WM-25`, [Dashboard SRS](../../spec/dashboard/SRS.md) `DB-FR-10,33,34`다.
[ADR 0022](../../adr/0022-workload-pool-membership-boundary.md)·[측정 admission](measurement-admission.md)·[Workload 관리](workload-management.md)·[scheduler 구현](scheduler-implementation.md)·[이기종 계약](heterogeneous-workloads.md)을 따른다.

| 결정 | 이유 |
|---|---|
| 자동제어는 settings.autonomousWorkloads UID 목록 | 기존 설정 API/판단 기록 사용, 재등록 UID는 기본 꺼짐 |
| 활성 POOL은 실제 인스턴스 owner/binding에서 산출 | 별도 label/membership 저장/동작 없이 부분 완료도 실제 상태에 반영 |
| 측정 POOL은 ChillServer 단일 writer taint | 서빙 배치·제거·전력 변경 격리 |
| 전체 POOL은 모든 non-control-plane Node | online CPU inventory는 배치 조건일 뿐 전체 전력 집계의 필터 아님 |
| 가용 Node는 항상 최저 unloaded baseline 모드로 | DeviceClass에 모드 순서가 없고 baseline은 에너지 계산에도 필요 |
| 바뀌는 Node만 후보 에너지 비교, 전체 POOL은 실측 | SCH-16 공통 항 소거로 무관 Node의 baseline 부재가 판단을 막지 않음 |
| 완료 측정의 조건별 최신 성공에서 후보/비용 도출 | 운영자 profileCampaigns/preparations/costReferences/powerCostSources 선언 제거 |
| 완료 여유는 settings.completionMarginMS 하나 | 요청마다 firstfloor 전달, 인스턴스 policy 재작성 없음 |
| 외부 Pod 목록/CHILL 역할을 측정에 기록 | 외부 운영 책임과 CHILL 배치 예산 분리 |
| producer/consumer는 한 저장 계약 | [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 reader |

## 3. 표현과 근거

### 3.1 워크로드별 자동제어

`settings.autonomousWorkloads`의 현재 Workload UID만 사용하고 저장 때 존재하지 않는 UID를 제거한다. 이름 재등록은 새 UID로 꺼진 상태다. `PUT /api/v1/scheduler/settings`를 쓰고 판단마다 실제 목록을 보존한다. 제어요소 설정은 시스템 전체 활성 설정, Workload spec은 모델/전처리/데이터셋/SLO 정의다.

### 활성·측정 POOL

W의 활성 POOL은 `InstanceState.OwnerWorkloadUID=W` 또는 binding W인 CHILL serving 인스턴스의 Node 집합이다. prepare/remove가 실제 소속을 바꾸며 별도 membership 동작은 없다(`SCH-AC-22`). 노드당 serving 인스턴스 하나, 같은 모델 Workload도 각각 인스턴스를 두며 조건 동일 profile만 재사용한다. owner/binding 모두 없는 비측정 인스턴스는 다음 판단의 remove로 정리한다.
Cycle 6의 UID별 keep-last-good는 무Ready 관측을 철수 선언으로 바꾸지 않는 제어 안전성이다. headroom/transition reserve/MaxReplicas는 최소 복제나 N+1 장애 가용성을 보장하지 않는다. 이번 변경은 단일 replica energy-down을 금지하거나 redundancy 설정을 새로 도입하지 않으며, 물리 NotReady 동안의 요청 성공을 보장하지 않는다.
측정은 `chill.dacs.io/measurement:NoSchedule`, 값은 노드를 받은 측정이다. ChillServer만 붙이거나 떼며 승인/거절/반환은 [admission](measurement-admission.md)을 따른다.
profiling Job(worker/idle), transition attempt Job, operation 측정 인스턴스·승인 시 준비한 전환 비용 runner는 `operator:Exists,effect:NoSchedule` toleration을 가진다.
serving Pod는 nodeName을 써 kube-scheduler 검사를 우회하므로 capture가 측정 Node를 배치/전력/Fixed내림/remove에서 제외하고 실행 직전 prepare/ChangePower도 Node를 다시 읽어 `NodeInMeasurement`로 거절한다. pools API는 측정 소속을 표시한다. operation은 승인 Node만 사용하고 종료 격리 witness Tainted를 보존한다([원자 동작 격리](scheduler-operations.md)).

### 전체 POOL·가용 노드·전력

전체 POOL은 `node-role.kubernetes.io/control-plane` label 없는 모든 Node. capture는 별도 장비 label로 제외하지 않는다. 측정 Node는 fixed, 가용 Node는 측정 밖 Ready·CHILL serving 인스턴스 없음이다. 설정 부족도 Node 사유로 남긴다.

| 사유 | 조건 / 처리 |
|---|---|
| PlacementInventoryMissing | online CPU 없음/불가, 측정 GPU>allocatable, 예약이 inventory 밖 / 배치만 제외, 전력·에너지는 유지 |
| PlacementBudgetUnavailable | 외부 Pod GPU 소유 불명 또는 CPU 요청>예약 / 같음 |
| PowerModeUnavailable | 현재 모드 불명, 요청 가능 모드 없음(DeviceClass 비고정+actuation policy), 또는 lowest가 요청 불가 / 현재 유지 |
| InstanceUnavailable | native/profile 조회 불가 / 해당 Node 배치·내림·전력 제외, 다른 Node 계획 계속 |

외부 요청은 NonServing 예산, Ready는 Kubernetes NodeReady다. 외부 Pod Node도 가용 전력 집계에 포함한다.
lowest는 unloaded `NodeBaseline`(State empty)의 최소 Watts, 동률 이름순이다. DeviceClass.powerModes에는 순서/전력이 없다.
Controls.Power가 켜지면 keep 포함 모든 DAG는 판단 시점 가용 Node를 시각0부터 lowest에 둔 공통 상태로 계산한다. 현재 모드와 다르면 첫 power를 `Action.Fixed=true`로 넣고 공통 시간/에너지는 비교에서 소거한다. target 없으면 keep Fixed만 실행하며 빈 Node 내림 비용 측정은 불필요하다. 빈 Node 공통 절전은 다른 Node route와 독립이나 사용할 Node는 Fixed→power→prepare 순서/실제 Ready·소유권·policy ACK·기존 요청 인계 후 분배한다. 소비·전환 결과는 전체 실측에 포함한다.
재편입은 lowest→profile mode의 빈 BeforeState power 비용+동일 mode prepare 비용을 비교한다. 재부팅 장비 jetsono도 측정 POOL serving-operation으로 측정하고 annotation 적용/독립 read-back+settlingDurationSeconds(E1 60s) 전체를 잰다. jetsonx 적용≈1.5s도63.5s 창 Accepted이며 settling 에너지를 포함한다. 비용 없으면 `MissingOperationCost`다.
모든 계획의 공통 비교기간은 `T=max(comparisonPeriodNanos,최장 route전 사슬+최장 route뒤 remove)`, 각 동작은 측정 trial 최대시간이다([용량표 계획기](analytic-planner.md)). E1 빈 전환 jetsonx63.5s/jetsono134.5s/orin-nano197.1s가 있는 판단은 하한60s보다 길었고, power/prepare 전체 에너지+남은 적재 baseline을 같은 T로 비교한다.
비교 Node는 keep 외 적격 계획이 있는 집합, `SchedulerSearch.ComparedNodes`에 기록하며 keep/target ExpectedJoules도 이 집합만 합한다. 집합 밖(측정/keep-only) 공통 항을 소거한다. 필요한 unloaded baseline 없으면 그 계획만 `MissingUnloadedNodeBaseline:<node>:<mode>`다. 전체 에너지는 ObservationLog.OperatingPower 실측으로 보고한다.

### 최신 profile·비용 도출

Catalog.Refresh는 profile namespace allowlist의 모든 terminal 성공 BEO에서 latestConditionProfiles를 선택한다. 측정 Node가 후보이며 PrepareSpec/bundle/binding은 profile inventory/preparation에서 도출한다. 실패 하나는 CatalogIssue.PreparationUnavailable, 나머지 refresh 계속; ID 집합 변경은 다음 refresh, 현재 사용하는 profile 측정은 삭제 보호다.
profile 변경은 영향받는 d/비용/baseline 목록을 함께 남긴다. 정확 profile/runtime/NodeUID/mode/적재 상태별 최신 성공을 고르고 다른 조건은 대체하지 않는다.

| 출처 | 수락/최신 조건 |
|---|---|
| serving-operation | 고정 `serving/operation-measurements/<Node UID>/<operation ID>.json`(manifest는 key 안 받음). Verdict Accepted+기존 cost 검증+기록 meter(ID,Node,scope,source)=현재 settings.meters 전체 일치. 일반 비용은 NodeUID/kind/from/to/노드mode별 최신 Result.EndedAt. power는 실제 전후 모드=from/to이고 NodeUID/from/to/BeforeState별; idle baseline은 Node/mode/State별 |
| PowerModeTransitionCost | allowlist Complete+loadPowerCost 검증, 같은 Node/source(prepared.PowerSource)의 현재 meter. NodeUID/from/to/BeforeState(profile/할당)별 받아들인 trial 완료시각의 최대가 최신, 동률 이름순 |

두 출처의 선택 시각을 OperationCost.MeasuredAt에 둔다. planner selectCosts는 prepare/remove에 Node mode, power에 BeforeState를 맞추고 없으면 MissingOperationCost다.
고정 prefix는 evidencestore.Lister/S3 ListObjectsV2 pagination, 불변 객체 identity당1회 cache, 사라진 identity는 제거; PMTC는 UID+result identity. 실패 기록은 CostIssue{Source,Code,Reason}(CostEvidenceInvalid/MeterUndeclared), 나머지 사용. non-Accepted/non-Complete는 issue 없이 skip, listing 실패는 이전 구성 유지, refresh 재사용도 비용 identity 집합을 비교한다.

### 완료 여유·수동 변경

필수 `SchedulerSettings.CompletionMarginMS float64`/JSON completionMarginMS는 유한≥0. decode는 *float64로 생략/0을 구분하여 생략을 `completionMarginMS required`로 거절한다.
완료 예상은 native 완료+m. replay는 CompletionOverhead 대신 설정값, firstfloor `requestSlack=RemainingSLOMS−m−serviceCost`, lifetime에도 m을 한 번 뺀다. senderElapsed는 원래 transport lifetime에서 별도로 뺀다. 관측/calibration Pending을 요구하지 않는다.
`ObservationCohort.completionResidualNanos=종단 ElapsedNanos−전달시점 elapsed−NativeElapsedMS`를 완료 표본에서 수집하고 Dashboard는 설정 m과 분포를 함께 표시한다. MNV29ms는 p95 관측값; D2 뒤 Workload별 최종 SLO percentile residual 추정의 최댓값을 단일 m으로 고른다. relaxed 측정 SLO를 자동 채택하지 않는다.
수동 변경은 꺼진 Workload 하나: staticRouting은 꺼진 UID만 부분 map, 생략은 현 route 유지, 켜진 UID 거절, target 제외는 그 binding만 해제한다. execute-config manifest는 workloadUID 하나, prepare owner는 그 UID, remove는 유일 owner+binding일 때만 허용한다. 공유 사용은 route/binding만 해제하며 action 조건/staticRouting ACK를 확인한다. 후속 현재 작업 API는 [scheduler API](scheduler-api.md)를 따른다.

### 외부 Pod

NodeActuator는 외부 Pod 운영을 소유하지 않는다. 측정 전력은 노드 전체, scheduler는 예산/사유를 기록한다. label `edge.dacs.io/workload-role`: serving-instance(Serving), transition-observer(transition attempt), system(operator의 store/bootstrap/agent/profiler/discovery 및 chart operator/entrypoint/console/dashboard/predelete, runtime-declarations probe). selector는 바꾸지 않는다.
profiling observer도 label을 보존하며 profiling/전환/operation은 구조화한 workloadobserver.Pod 목록을 기록한다. Dashboard는 역할 label 없는 Pod를 비CHILL로 표시한다(`SCH-34`,`DB-FR-34`).

## 4. 스케줄러 판단

snapshot은 전체 POOL/꺼진 인스턴스까지 포함하고 option은 최신 profile×켜진 Workload다.

| SchedulerDecision.Workloads[] 상태(UID,State,Reason) | 처리 |
|---|---|
| Disabled | 마지막 구성 유지; owner/binding 인스턴스 Node는 keep뿐 |
| MeasurementEvidenceMissing | 켜졌으나 option 없음, 해당 Workload만 계획 제외 |
| Controlled | 켜졌고 option 있음, 계획/SLO 판정 대상 |

노드 계획 원본은 [용량표 계획기](analytic-planner.md)다. Controlled만 인스턴스/분배를 바꾸고 Disabled owner/binding 또는 strict fixed-size baseline Node는 `FixedWorkload` keep뿐이다. 노드당 serving1, membership off는 기존 해당 Workload Node만 option이다.
Controlled는 부하0에도 인스턴스≥1이 최종 충족이며 무연결 인스턴스 정리/공통 Fixed절전을 포함한다. 활성 POOL 직접 이동은 같은 Node remove+prepare DAG/비용합, membership 동작 없음. 첫 구성(인스턴스0)은 option 배치를 쓰며 placement/resources/batching/membership control gate가 켜져야 한다.
수요 λ는 max(장기관측률,최근1제어주기 계산도착률)+기존 미완료 기여1회. 실제 관측 범위만 쓰고 용량/지연/route/요청 에너지에 같은 λ를 사용한다. 최근률이 큰 Workload는 기존 인스턴스 제거/교체/route0 보호, 확장·다른 변경은 계속한다. 두 구간/관측/최종λ·진행 target 유지 규칙은 planner 기록에 남긴다.
SLO 판정 대상은 Controlled 전체. Disabled Node는 전력/인스턴스도 불변, 에너지는 비교 Node 집합 합이다. Target이 있으면 적용, 기록은 keep/target/decision.capacity/snapshot. 저장 SchedulerInstance는 profileID만, 본문은 catalog GET에서 제공한다. HTTP 압축은 조회 경계 유지; 결정 축소 M0는 [아키텍처 검토](../architecture-rethink/README.md)·[기록 다이어트](decision-record-diet.md)를 따른다.

## 5. API와 Dashboard

| 표면 | 계약 |
|---|---|
| GET /api/v1/pools[/{id}] | workloads[]{workload,nodes,instances}, measurement[]{node,instances}, available[]{node,ready,powerMode,reasons}, 나머지 전체 Node/사유. 활성은 runtime, 측정은 taint, 가용은 최신 snapshot |
| GET /api/v1/scheduler/state | decision.workloads/Node 사유/CostIssue/비교 집합 |
| PUT /api/v1/scheduler/settings | autonomousWorkloads/completionMarginMS/제어요소 |
| GET /api/v1/scheduler/profiles | 도출 profile/CatalogIssue, 읽기 전용 |

Dashboard는 Workload 활성 POOL/측정/가용·공유 표시·노드 사유, Workload 상세 자동제어/판단/Disabled 유지·수동 결과/residual 분포, 설정 편집, 측정의 외부 Pod/소속·거절 사유, profile evidence/문제를 제공한다(`DB-FR-10`,`33`,`34`).

## 6. 측정·저장 책임과 D2 경계

대량 측정은 대표1건의 조건 생성→실행→raw/projection→planner 소비→실물 반환을 확인한 뒤 진행한다. 실측 profile/SLO/승인 계수로 N/K/L·준비/본창/drain/수집 예산·계수·exact profileID를 Run에 동결하며 generator 계산은 M5 controller 이관 대상이다.
물리 d/signed raw/max0, 비용 E±B/B=S+G/보고용10%는 [측정 API](measurement-api.md) 원본. minimumGain은 선택 근거 B만 signed 상쇄·사슬합성하고 BEO/profile idle 통계 CI는 포함하지 않는다. 미기록 B/Timing 부재는 unknown이지0이 아니다. nominal 평균·keep infeasible 회복은 유지한다.
조건 생성기·승인 원문·manifest·profile/runtime/NodeUID/source SHA·raw 위치만 선별하여 [handoff](../../handoff.md)에 연결한다. token/Secret snapshot/전체 cache backup은 제외한다. 원 raw/값/판정은 외부 과학 증거에 보존한다.
G2 [소비자 감사](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json): catalog legacy49(비용35/baseline14), E5 ranking9, frozen E5 후보 energy legacy0(전체 prefix 정리와 별개). ranking9 unknown/fresh10 입구를 유지한다.
ranking9 재측정 승인2roots/18steps, actual lowest 조건부 직접전환 포함 상한4roots/22steps. root마다 raw/소비/반환 후 다음 root. 대체9 확인+검증 외부 backup 절대경로/개수 대조 뒤 raw80keys(old9 포함)/terminal OCM6 정리를 연속 실행한다. strict reader 로컬 cutover는 병렬 완결, 새 source 적용은 대체/backup/정리/Timing 없는 소비0 뒤이며 첫 BEFORE 전 공통 입력 동결. 완료 근거는 [측정 API 저장 계약](measurement-api.md)에 둔다.
D2는 관측된 gateway tail holder/work, cache/transport/payload/search 병목만 보수하며 durability/physical ownership/HTTP 성공·원 architecture 경계를 유지한다. production mapping/negative fixture, kind SCH-AC24/25/26 범위를 조율한다. runtime7/자원/evidence store 고정, 일반 이미지 분리/CRD통합/NodeActuation정리/추가planner·CI·known bias/d 대안/상시trace/무관cleanup은 M5다.
요청 lifetime는 실제 HandlerConfig 상한과 admission 시작 전에 대조한다. E5 caller는 SLO/cap/L/delivery/socket 명시, `L≥SLO,cap≥L,socket>L+delivery`, native 남은 수명을 일치시킨다. warmup60s/본창360s/cap3600s 승인값은 한 원천에서 generator/API/admission/controller/Run이 소비하고 실측 preparation 예산을 별도로 둔다.
Runtime은 physical capacity 예약 후 소유권 잠금 밖 assignment 저장/native 시작, final row도 호출자 잠금 밖 저장, shutdown은 final 종료를 기다린다. 메모리 fold/디스크 writer 잠금 분리, 완전 행 rollback/저장실패 fail-closed, 동일 불변 profile pointer 시간별 catalog 직렬화 생략을 유지한다.
알려진 수리 뒤 fresh D2 승인1회/최종 rollout≤1회, 동일 AFTER tail/G가 PLC SLO를 만족하면 진단0회, 잔여 tail이 막을 때만 진단 rollout≤1회다. 승인 단계 사이 재승인 대기 없이 새 조건/판정/범위만 승인받는다.
진단은 실제 PRIMARY native8의420s창: `--diagnostic-directory` 새 절대 private 경로, Helm diagnostic.directory/startDelay/traceOffset→동일 flags, 기존 /data mount. 빈 경로 비활성은 요청별 clock/ID변환/할당0. start-delay는 bind→numeric창, trace-offset은0..390s다.
numeric ring16MiB/64byte event, Go trace 저장32MiB/30s. native반환·mutex attempt/acquire/release/owner/request·reply/Wait/Write/Flush/Response·관측 marshal/write/Sync/query/archive 실제 잠금 범위를 기록한다. 종료 후 잠금 밖0700 dir/0600 events.bin/runtime.trace/metadata.json 저장·버퍼해제. overwrite/drop·offset·cap/중단/절단/오류와 실제 mount/readiness/load경계를 보존하며 끊긴 edge는 inconclusive, trace overhead와 runtime 자체 buffer는 별도 보고한다. source 검증/kind/build/offlineκ는 load와 겹치지 않고 source/검증/live SHA를 구분한다.

## 9. 검증 계획

계약 검사는 원래 lifetime−senderElapsed−m1회, native완료+m replay, owner/binding/unowned 소속, 전체 Node/사유·비교집합 공통항, Disabled고정/최소1/Node당1/직접이동, Fixed최저선행·빈재진입비용, 조건별 최신/부분실패/나열실패 유지, 수동route 보호, taint격리·직전거절·toleration/측정runner보호, 역할Pod·외부예산/NodeReady, m누락거부/0수락을 다룬다. 실행 완료/PASS 주장이 아니다.
E1은 최초 구성/무부하/부하/완료여유(`SCH-AC-24`), E2는 Disabled/근거없음/Controlled 공존(`SCH-AC-25`), E3은 측정격리/Pod/evidence/자연반환(`SCH-AC-26`), E4는 직접이동 DAG/비용/소속(`SCH-AC-22`), E5는 혼합 SLO/비교·전체에너지/residual을 확인한다. live는 [AGENTS](../../../AGENTS.md)의 승인 조건, window/결정/측정 상태/메모리/PVC를 보존한다.

### 9.2 실클러스터 E2E

정량 결과의 권위는 [명세 평가 표](../../spec/README.md#실클러스터-검증과-평가)와 [평가 보고서](../../reports/e5-evaluation-20261030.md), 원시 정본이다. 반복된 거대3회차 표 대신 아래 조건/결론/출처를 유지한다. 제어요소12행/rev102 식별력 한계는 [명세 비교](../../spec/README.md#제어요소-비교), 7행 커버리지는 [E5 평가 보고서](../../reports/e5-evaluation-20261030.md)가 원본이다.

#### E5 입력·입구

초기 후보8 Node=genesis2/genesis3/lattepanda/rasp5/orangepi/jetsono/jetsonx/jetson-nano, 제외genesis1(게이트웨이)/orin-nano(DNS 해결 전). 실제 평가는 genesis2 Xid79 제외7 Node다. 노드당1 serving, canonical Workload3은 인스턴스0에서 시작하며 측정용 정리·실제 UID·m을 확인한다.
MNV=image-classification-tight-slo p95≤1s, PLC=plc-anomaly/Dataset plc-extruder-01-windows, Smol=smolvlm2-vqa/vqav2-val-subset. PLC/Smol 목표는 D2후 d/m/dump의 fastest b1/가장 넓은 지연간격으로 제안, κ>0 Node≥절반/가장느린2 κ0를 p95/p99/계산시간으로 확인 후 동결한다. 실제 평가 SLO는 p95≤1000/36/7034ms(MNV/PLC/Smol)였다.
`R_w=최종SLO의 Node별최대κ 합`(hack/kappa -mode slo), 단1인스턴스여서 고립 R을 동시 달성 못한다. `rate_w=α_w×s_w×R_w`, 기본 shares MNV0.45/PLC0.1/Smol0.45, 각유한[0,1]/합1(오차≤1e-9), α∈{0.3,0.5,0.7,0.9,1.2}. 혼합 feasible 보장 아님. `1.2Σ(sR)≤0.8G` 검증, G/출처/R/s/α/rate/trace binding 동결, G미정은 경고이지 개시통과 아님/자동clip없음.
Poisson replay 동시3개, seeds101/202/303/arrival trace/payload ID/profiling·evaluation subset을 사전 고정/paired 재사용한다. 모든 caller는 실제 UID/generation/SLO/cap과 --workload-slo/--gateway-timeout/--request-lifetime/--delivery-margin/--timeout을 명시한다. MNV 예 SLO1/cap129/L2/delivery0.010/socket2.02s; PLC/Smol은 실측으로 고정한다.
G-check는 고정배치 α1.2 총혼합120s 단1회, 계단/최대G탐색/retry없음. PLC p95≤32ms·전체offered deadline32ms성공≥95%, frozen trace 공급, gateway거절/503/timeout/incomplete0, CS throttle/restart/OOM0, 누락없는 CPU/메모리10s표본·frozen limit미만 모두 필요하다. 실패는 원사유, native/client한계 구분; 통과가 최대G/20%여유 자동인증은 아니며 위부등식 별도다.
예정 대체: 현재 로컬 binding으로 입력을 동결하는 실행 경로는 [실험 실행 설계](../profiling/experiment-execution.md) / [실험 SRS](../../spec/experiments/SRS.md)로 대체할 예정이다. 위 과거 평가 입력·수치는 그대로 보존한다.
원래 L/senderElapsed를 전달해 `native remaining=L−senderElapsed−m`, SLO/L 분리·m1회. signed d/max(d,0) 둘 다 보존한다. D04/D09/D14/D23/D33의 profile포함95%구간<0은 known systematic profile-vs-serving bias, 원인별 기여 미확정/Accepted와 max0 유지, 보정/d 대안은 M5다([수치](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.json), [관측/가설](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.txt)).
입력 보수 rev72/e18ac=exact profile13 prepare/remove26steps+empty10창+native up2=8roots/38steps, fresh empty는 기준8+Jetsono/Jetsonx lowest2. sampling1s/request timeout800ms/max raw gap3s/watchdog2250s/empty300s. loaded idle13/전체19mode 확장은 범위 밖이다.
legacy ranking9=Jetsono MODE_30W/MODE_50W, Jetsonx MODE_10W_4CORE/MODE_10W_DESKTOP/MODE_15W_2CORE/MODE_15W_4CORE/MODE_20W_2CORE/MODE_20W_4CORE/MODE_20W_6CORE. ranking 참여/uncertainty unknown을 유지하고 실제 lowest/소비empty가 fresh10인지 확인한다. 밖의 소비/예상밖 handback은 입구 중지, ranking filter/자동추가측정/수동mode강제 없음. loaded/profile idle actual ref/시점 별도다.
입구는 exact profileID/NodeUID/mode/runtime/allocation의 prepare/remove/power, Smol9+D2뒤 CPU8/GPU5/PLC12 d, fresh10/legacy9 한계, 최종SLO/m/L/socket/cap, G/κ/R의 초기·확장·축소·swap witness, terminal/actual execution·action·handback0/readback(원017 비실행 예외)을 확인한다. 대체Accepted 전 legacy CPU8/GPU2는 소비 유지하되 미재인증이다.
각 witness `B_w=q+max(0,λ−κ_ret)r`, `W_w=B_w/κ_new≤SLO−m`, keep/best nominalJ/signed상쇄B 공개. 둘 다 feasible이면 gain>문턱 전환/동률keep, keep infeasible 회복 유지. 자연swap 없으면 차단부등식, 정량SLO/m/G/κ/R/N·최종controls는 실측 고정이다.

#### G2 축소 뒤 재급증의 공유 Node 경계 — SCH-25, SCH-27~28, SCH-55

`control-222-full/full-E5-5-3/obs/20261029T20/scheduler.jsonl.gz:4507`은 관측 장/단기 rate `.1/.1`, 최소 수요 설정 `.3`, queued1에서 `.3 < orangepi κ .372262300732782`로 Smol 이동을 선택했다. 미래 substage의 의도 rate `.5205040034316192`는 이 판단의 예측 입력이 아니다. 재급증 `:5321`의 rate1.2/queued12/model wait32.235335075237344s에서 MNV 소유 예약이 genesis3 Node를 잡지만 Smol 자체는 Controlled다. 예약이 없는 `:5710`에도 MNV q4가 같은 affinity를 차지하고 genesis3 Smol 후보12개는 `RoutedInstanceStopsBeforeRoute`다. 이는 energy/dwell 거절이 아니라 자원 blocker를 먼저 비워야 하는 전환의 표현 경계다.
Node당 최종 인스턴스1개·make-before-break와 이전68s outage 회귀를 유지한다. local veto 삭제나 미래 부하에 맞춘 floor 변경을 회복 근거로 삼지 않는다. 전역 후보는 최종 goal의 독립 prepare/양수 ACK→blocker 유입 이관→원 physical ID stop/drain/remove→막힌 prepare→최종 route를 같은 DAG로 컴파일한다. 불변 중간 `RouteTarget`은 parent 최종 목표와 분리하고 동일 owner/UID receipt·새 목적지 전체 native ACK를 그대로 요구한다. 단계별 κ/queued backlog/SLO/reserve와 실제 제거 전 old+new 예산을 검사하며 기존 growing/slow-window 보호를 보존한다.
bridge는 최종-goal의 독립 prepare에 한정하고 canonical 순서로 증명하므로 임시 variant나 모든 경로를 완전 탐색하지 않는다. 안전한 bridge가 없으면 기존 veto를 유지하며 모든 원 tick의 회복을 보장하지 않는다. `capacity.bridgeStages`는 조건부 예측이지 actual Ready 공급이 아니다. 외부 `analysis/cards-r4/smol-scaleup-01.ticks.jsonl`은52개 결정의 raw `_ref`·Smol 후보/거절/예약/동작 투영이며 원시 증거 자체는 아니다. 원시 수요/설정의 변경이나 배포·실측 복구를 이 로컬 수정의 검증으로 주장하지 않는다.


#### D2 실행·관측 경계

rev72 보수/catalog동결→MNV 자율 BEFORE logical initial8/seed1/λ=min(50,0.3Σκ)/660s(준비60+관측600)·복구→exact8 profile/mode/runtime/batch/allocation PRIMARY native8→old loadgen tightMNV seed1 Poisson workers320/L2/delivery0.010/socket2.02,0부터50req/s씩300s(첫60제외), 마지막min(next50,Σκ)→throttle/incomplete/κ도달중지 순서다.
producer/HTTP/native outstanding/pending/measurement/action/handback0 뒤 frozen Ready IDLE native8만 rollout예외로 보존한다. 수리/새승인 후 D2, startup re-adoption/routeACK·PodUID/containerID/profile/mode/allocation/route동일 확인 후 paired AFTER. physical unknown/mismatch면 paired주장 중단, 과거all-instances0 rollout은 보존증거아님. PRIMARY정리→같은logical8 자율AFTER·복구→d25(CPU8+GPU5+PLC12)/G/m·SLOκRN제안→E5→M5/P11 main·spec이관/main제어image(runtime7고정)/P10이다. U3 D04/D09 소비source교체/옛raw/status보존.
sourceSHA/digest/dump/UID/자원·MNV자율Tick/no-controlTick/혼합3search를 기록한다. G는 전완료·무throttle 마지막 단계, native/client/capacity 선한계는 bound/원인. gateway CPU=(stagecores−idlecores)/rate, 지연/거절/만료/incomplete와 GatewayOverloaded rejected/error+503을 사유별 보고한다.
CS/genesis1 edgemesh cgroup1s/restart15s/thermal5s 전후 동일, 실제 server6CPU/4GiB/GOMEMLIMIT3072MiB/gctrace1/timeout129s/drain164s/grace194s·Flush/scraper cadence를 보존한다. query는 단계경계, drain뒤 같은 archive/Prometheus, source별κ는 load없는 동일dump에서 순차다.

#### E5 단계·분모

| 단계 | 시간/부하 | 관측·요구사항 |
|---|---|---|
| E5-0 | 10분/0 | 첫 인스턴스·나머지lowest·무부하전력, SCH-AC-24 |
| E5-1 | 30분/모두α0.3 | 일정·동시혼합/분포·결과·에너지·예측/관측, SCH-AC-23, [아키텍처 검토](../architecture-rethink/README.md) |
| E5-2 | 15분/Smol0.3→0.9,나머지0.3 | burst·소유권재배치·모든SLO/Jetson지연, SCH-AC-21 |
| E5-3 | 30분/0.5,0.7,0.9 각10분 | 증가·확장·κ_new/B/W/큐, SCH-AC-15 |
| E5-4 | 15분/1.2 | overload/최대초과량·결과/구성, SCH-AC-16 |
| E5-5 | 20분/0.3→0.9 각10분 | 급감/재급증·축소/idle/전환J·U2 KEEP/readback, SCH-AC-15 |
| E5-6 | 60분/0.3 | soak·예측/비교집합/전체에너지·residual |
| E5-7 | 15분/0.3 | 180s Pod삭제/360s cordon+삭제/600s uncordon, SCH-AC-45 |
| E5-S | 170분/E5-1..6 동일seed/rate | E5-3끝α0.9의 실제구성/mode/route 고정 비교, SCH-AC-20 |

full195+static170=365분, 최초 N2(membership-off/replicas-off) E5-2/5 비교70분 포함435분, reset/settling/warmup/drain/flush/추가seed 별도. 실제 control-216 집합12행은 명세를 따른다(최초N2와 구분).
E5-S는 state snapshot의3UID를 autonomous에서 제거+staticRouting/reason 한 PUT, validatedAt=ConfigMap staticRoutingValidatedAt=operation.settingsValidatedAt을 기록한다. E5-1..6 후3UID복원+staticRouting:{}+reason PUT, 선언삭제는 동작 생성 없이 기존분배를 다음 판단이 이어받아 E5-4로 간다.
U2 최소는 jetsono MAXN↔MODE_15W/jetsonx MODE_15W_6CORE↔MODE_15W_4CORE/jetson-nano MAXN↔5W, 세 모델CPUq1/B1. [모드 계획](../../plans/2026-10-27-power-mode-coverage.md)의 근거5종 Accepted만 후보; 부족을κ0/강제mode로 바꾸지 않고 KEEP은 차단근거이지 절감성공아님.
E5-2는 genesis2/3 SmolCUDA OOM제외 유지, 측정 MNV/PLC CUDA 또는 Smol CPU/Jetsono SmolCUDA 비교, CPU유지·GPU승자를 성공조건으로 두지 않는다.
모든 유효 offered가 분모, 전환/1분창 SLO·완료/실패/만료/drain/censoring/incomplete/표본수를 유지한다. 첫준비→첫routeACK와 차단명령→uncordon후 실제Ready/관측/route복구는 전체분모+별도집단/제외집계도 분모·실패와 병기, 미복구는 단계끝, 사후유리창 선택없음.
Tick p50/p99·CS CPU/RSS/GC/PVC·edgemesh CPU/memory/throttle/restart, 검색 neighborhood/evaluated/durationNanos/budgetCut/evaluationElapsedNanos를 기록한다. 예산은 controlIntervalNanos/2(10s→5s), tick cgroup cpuacct.usage/cpu.stat nr_throttled/throttled_time차, 검색안/밖의 gateway(종단−native) p99, 1분SLO/검색유무·workers/최종G/TransitionGainWithinUncertainty keep을 연결한다. workers3/G≈220은 선행값이지 최종G아님.
thermal5s active/latched/unknown, 사전온도bin <60/60–70/70–80/≥80°C, 미관측bin 일반화 제한. placement/resource결합·routing안전/no-op·batching고정B/deadline차·power U2만으로 독립기여를 주장하지 않는다. 사후threshold/seed/집단 선택없음. demo-config capacityR는 최종승인뒤, failure runbook은 운영자 직렬/자동실행없음, preflight는 연구장애증명아님.
예정 대체: 현재 `round.py` 단계 실행·운영자 장애 주입은 [실험 실행 설계](../profiling/experiment-execution.md) / [실험 SRS](../../spec/experiments/SRS.md)로 대체할 예정이며, 위 단계·분모와 과거 평가 기록은 유지한다.

#### 결과와 한계

진도 d9/34·정확비용69/69·명세시나리오연결7/7은 PASS/완전커버리지 아님. 비용은 2026-10-24T12:49:27.350175648Z [69 option key join](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/cost-coverage-after-055.json)이다. E5-0..7+S 연결은1/2회차 각9/9로 별도분모, G2감사도 별도다.
수신p95는 성공·executionComplete HTTP final 완료-only, D1은 전체planned(BackendUnavailable/오류/거절/취소/무발행/incomplete 포함) deadline성공/필요rank다. 결과수 모든종류/상위오류를 보존하며 낮은 완료p95를 D1 PASS로 바꾸지 않는다. generator는 매요청 새TLS/keepalive없음으로 입구지연/CPU에 handshake를 포함한다.
에너지는7관측Node 합(전체구성 meter합과 구분), 동일단계 전환포함 whole/active shared값이지 Workload귀속 아님/반복행합산금지. ±는 공백보간상한, coverage는 gapΣNode/maxgap/maxNode비율, Δ=static−CHILL(양수절감). 표시는 원 tables 소수3자리 반올림/기존판정이다.
1회차 static S5 gap1.637%>1%로 S5/E5-S총에너지/총Δ UNKNOWN. 대응 CHILL170분 whole1,100,896.456J(raw1100896.4557965863). UNKNOWN을0으로 대체하지 않는다. 2회차는 정본 수치연결, descriptive/연결은 PASS아님.
7행연결은 일정1/6·증가3(점진감소미평가)·burst2(Smol만)·급감5·혼합1..7·한계4·실패7. 물리network/power와 모든제어실패는 미측정, 회차별 pairedseed1집합/독립반복없음. failure는 UID조건부 Pod삭제(강제/grace0아님)+cordon/uncordon 대리실험이며 완전복구보장아님. targetUID/UTC/응답/격리/대체/ReadyACK/route/실패시 uncordon readback 보존, 실제관찰 제어실패만 주장한다.
1회차는 수정전 control-213-full,2회차는 rev104 e3f28f23(R1~R6+R4b). 결함카드는 아래 P2-synthesis.md다.2회차는 #222 full0..3/static1..2+#223 static3..6/full4..7 합성, accepted static-replay-restore/fingerprint일치·prepare physicalID재결합이며 연속단일실행아님.
static 선택은 각회차 E5-3끝에서 독립, MNVκ119.601→91.907req/s로 달라4/5 차이를 동일배치 A/B/수정효과로 단정못한다. 수정후3끝 공급κ91.907<수요≈95.5 원인은 조사중(사이클2). static-selection/comparison 정본을 따른다.
2회차 failure 대상은 genesis3/lattepanda→jetson-nano/orangepi(원대상 serving없음)였다. #217collector경합/#218예약잔류→R4b/#221디스크/#222로그혼입 중단은 하네스/환경으로 분리·선택결과에 합산안함/제품판정아님.
3회차는 rev105 5f1dc4f1(R1~R6+R4b+C1+C2)의 fingerprint복원 표적재실행: #225 E5-1/7 tables-3b/comparison-225, #224 E5-5/6 tables-3/comparison-224. 나머지는 미실행/전체회차·독립반복아님. static은2회차재사용/재측정아님. #224 E5-1은하네스release훼손,7은selectorTTL stale snapshot거절로 mutation/후속주입없어 무효, #225만 사용한다.
E5-6/7 복원은2회차 시작예약1+owner-only3을 제외하여 개선을C1에 귀속못한다. C1근거는5에서 신규예약6개 모두 행내반환·Fixedtick0(이전560), 반환은 archived tick최초소멸, full-E5-5-3/c1-reservation-return.json이다.
#225 E5-7은 실제주입180.7/360.7/600.1s 활성Pod/exit0이나 Smol은114 RequestTimeout·D1 FAIL26/133(완료p95 1255.905ms)로 용량실패다. 주입실패와 구분하며 물리장애/완전복구를 주장하지 않는다.
3회차 E5-1 MNV실패67(이전296), E5-5 MNV1059(이전10780)로 줄었으나 Smol5 p95 14092.610ms/D1FAIL300/363은 남았다. Δwhole은3회차1 +8074.368±173.054J/5 −2700.318±15.373J/6 +16477.565±541.077J로 일관 절감이 아니다. 모든 전체 수치/결과수/에너지는 정본에 보존하며 이 문서 갱신은 제품코드/클러스터조작/재측정0이다.

원시 정본은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/` 아래 `control-213-full/{tables.txt,e5-summary.json}`, `control-222-full/{tables-223.txt,comparison-control-213-223.txt/.json,e5-summary.json,static-selection.json,static-replay-restore.json,tables-3.txt,tables-3b.txt,comparison-224.txt/.json,comparison-225.txt/.json,full-E5-1-3b/,full-E5-5-3/,full-E5-6-3/,full-E5-7-3b/}`, `analysis/P2-synthesis.md`다. MEASURED_DESCRIPTIVE/FAIL/OVERLOAD_DESCRIPTIVE/UNMEASURED/INCOMPLETE/UNKNOWN의 원판정을 유지한다.

### 9.3 검증 기록

E1 2026-10-19 UTC의 최초30s timeout은 Tick133실패/첫구성없음/1660요청성공0/p95 30.08s. rev35 pools-e669615(entrypoint/profiler) 재실행은 maxCandidates32/replays10/m139(초기50), 활성화6s뒤 rasp5 mnv2-rasp5-cpu-q1-ed628c8 CPU[3]/Fixed,24s뒤Ready였다.

| 부하 | Tick p50/p99/max ms | 요청p95 ms | 1s완료 |
|---|---|---|---|
| 0.633req/s | 913/1749/1749 | 77.5 | 62/62 |
| 2req/s | 3475/6166/6166 | 140.1 | 300/300 |
| 5req/s | 7495/22575/22575 | 200.2 | 750/750 |
| soak2req/s | 4769/6601/6948 | 161.4 | 7139/7199(99.17%) |
| 전체 | 4574/12655/22575 | 164.5 | 8252/8312(99.28%) |

위 단계 SLO는 충족했다. residual n8312 p50 3.29/p99 138.23/max405.73ms→m139. D-11 query실패로PVC사본계산, API20:10:20–20:35(n1070)/0.633(n62)와일치.1h 비교에너지예측499333J/138.7W,실측495741J/137.7W,whole(control-plane genesis1제외)521589J/144.9W다.

| 결함/환경 ID | 관측과 경계 |
|---|---|
| D-1 | 최초 Tick133 timeout/성공0, 위 최초실행 |
| D-2 | settling≥60s MLIB에서 runner IdleTimeout60s가 연결종료 |
| D-3 | PMTC PodIP edge tunnel실패, Service경유 재실행성공 |
| D-4 | PMTC modes[0] 시작필수/SourceStateMismatch. admission은 profile DeviceClass 기준 readback 불일치시 taint/runner전 Refused:RunnerSourceStateMismatch |
| D-5 환경 | orin-nano DNS127.0.0.53거절/prepare불가 |
| D-6 환경 | jetson-nano MAXN→5W Applying잔류/SSHtimeout |
| D-7 | Fixed절전에 비용요구 결함, rev35는 무비용내림 |
| D-8 | 고정 NodeActuation이 staleReady 재사용 가능/e1r-* 사용 |
| D-9 | ChillServer toleration없어 genesis1 taint에서Pending |
| D-10 | 실패prepare의 finalizer Pod/ConfigMap 수동정리 |
| D-11 | 전체stream읽기→범위필터 query가 CS2회OOM |
| D-12 | jetsono MAXN↔15W재부팅/runner종료/재진입비용미확보 |
| D-13 | jetsonx 전환≈1.5s가 meter≈1s 간격에 비해 짧아 PMTC15회 PowerWindowInsufficient |
| D-14 | 변화증거없는remove Accepted/CostIssue |
| D-15 | decision중앙값185359B>100KB목표(candidates96890/snapshot70394) |
| D-16 | CPU500m에서2req/s Tick3475ms/5req/s7495ms/max22575ms |
| D-17 | assigned1건이Tick Pending:soak37/360,5req/s3/17 |

## 10. 결정과 재현 근거

- d 결정(`O/intent/intent-archive-20261030.tar.gz:d-measurement-decision.md`), D2/E5 승인(`O/intent/intent-archive-20261030.tar.gz:deployment2-e5-proposal-20261024.md`)
- [8roots/38steps](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/e5-input-repair-plan.json), [empty19/legacy9](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/empty-baseline-plan.json)
- [BEFORE/AFTER/native8](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/deployment2-before-plan.json), [Smol 교체/반환](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-d-followthrough.json), [legacy GPU2 provenance](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/legacy-mnv-gpu2-provenance.json)
