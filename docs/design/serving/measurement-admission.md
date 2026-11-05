# 측정 admission과 단일 writer (M3)

상태: active — `SCH-34`, `SCH-AC-26`, `DB-FR-34`의 측정/서빙 격리 계약이다. [이행 대안](../architecture-rethink/README.md)과 구분한다. `3dd5902`가 M3 도입 근거이며 당시 2026-10-21 snapshot은 현재 source의 live 수용 기록이 아니다.
ChillServer가 `chill.dacs.io/measurement:NoSchedule` taint를 소유한다. NodeActuation 기반 중복 설계는 제거되었으며 현행 전력 요청·무효판정은 [node-agent](../platform/node-agent.md#3-노드-전력모드-node-label과-generation)가 권위다.

## 1. 경계와 이유

운영자 taint/runner 준비와 serving-operation의 host 내부주소 의존을 없애고, 측정 시작과 서빙 전력/배치를 같은 writer로 직렬화한다. 서빙 노드를 drain해서 측정하는 안은 `SCH-34` 거절 요구와 기존 SLO를 깨므로 채택하지 않았다. CHILL이 배치하지 않은 Pod는 지우지 않고 결과 census에 남긴다.

## 2. 결정

### 2.1 writer와 요청

- scheduler loop는 Tick 앞에 `Admit`를 호출한다. `tickMu`가 승인·Tick·수동동작을 직렬화하며 Node/serving Pod/DeviceClass/측정루트와 범위예약을 읽는다. `c.mu`는 configuration/run 조회·등록에 짧게 사용한다.
- 시간이 걸리는 runner/반환/operation/수동동작은 `tickMu` 안에서 범위예약된 `actionRun`으로 등록한 뒤 잠금 밖 goroutine에서 실행하고 기존 OnAction으로 기록한다. 같은 예약범위에는 Admit/Tick이 손대지 않는다.
| 루트 | 대상 | terminal |
|---|---|---|
| BatchEnergyOptimization | spec.state.targetNode | Complete, Failed(Outcome CurvePlanResolutionFailed 제외) |
| ModelLoadedIdleBaseline | spec.state.targetNode | Complete, Insufficient |
| 독립 ProfilingRun | spec.targetNode | ExecutionComplete True 또는 False |
| PowerModeTransitionCost | spec.targetNode | Complete, Failed |
| OperationCostMeasurement | spec.node | Complete, Insufficient, Failed |
| DispatchOccupancyMeasurement | spec.node | Complete, Insufficient, Failed |
BEO/MLIB 자식 ProfilingRun은 루트 승인 뒤 생성하므로 별도 요청하지 않는다.
- BEO의 결정적 curve-plan 오류(불가능한 batch set·Run 입력)는 기존 Failed/InvalidRunSpec, MeasurementComplete=False로 종료한다. CPU 할당량 선택이나 allocatable 검사는 없다. execution/actuation policy 오류의 terminal 의미와 정상 물리 반환 절차는 유지한다. Kubernetes/Evidence Store 일시 읽기·쓰기 오류를 측정 조건 자체의 실패로 바꾸지 않는다.

### 2.2 승인과 거절

노드당 승인1개다. 대기루트는 creationTimestamp, namespace/name 순이며 기다리는 앞 루트가 그 노드 줄을 잡는다. 거절은 terminal이고 다음 루트를 같은 주기에 평가한다.
| 우선순위 | 조건 | 결과 |
|---|---|---|
| 1 | taint·예약run·전력pending·앞루트 대기 | 다음 주기 대기 |
| 2 | Node 없음 | Refused: NodeMissing |
| 3 | nonterminal CHILL serving Pod 있음 | Refused: NodeServing와 namespace/name, taint 없음 |
| 4 | CPU 공유 실행 | affinity/quota/online−reserved 검사 없음. CPU 관측의 누락은 측정 승인 거절 사유가 아니다 |
| 5 | PMTC runner 부적격 | 아래 runner 사유 |
| 6 | 그 밖 | taint값=루트UID, Granted RFC3339 시각; PMTC는 runner 준비 뒤 Granted |
- DeviceClass power mode와 Node readback은 실제 전력 상태를 판정한다. online CPU 목록은 관측이며 예약 mask나 배치 가능성 proof가 아니다.
- taint/전력pending을 먼저 보므로 앞 측정의 runner 때문에 뒤 루트가 NodeServing으로 거절되지 않는다. 서빙 Pod 거절은 소유 Workload/자동제어와 무관하다.
- taint는 resourceVersion merge patch다. atomic taints 목록을 SSA manager로 나눌 수 없고 충돌은 다음 주기에 재시도한다.
- 승인 후 capture/operations의 admittedNode가 해당 노드를 서빙 배치·전력변경에서 제외한다. 전력pending은 Node state/requested-at/policy 기한으로 판단한다. 정책·시각이 없으면 기한을 추정하지 않고 대기한다(`admission.go:powerModePending`).

### 2.3 반환과 측정 무효

terminal/삭제 루트, 살아 있는 루트·run이 아닌 taint(운영자=true/끊긴 준비), operation 종료는 노드 반환run을 만든다.
1. 진행 전력pending을 정책기한까지 기다린다.
2. 해당 load Job과 남은 CHILL serving Pod, NodeUID label의 runner Service를 제거한다.
3. 실제 NodeUID와 taint holder를 다시 확인한다. 요청 가능한 최저모드가 있으면 낮추고 독립readback을 확인한다. Fixed/baseline없음은 전력단계를 건너뛴다.
4. 현재 resourceVersion으로 taint를 제거하고 예약을 반환한다.
실패하면 taint를 유지하고 다음주기 반환을 다시 시작한다. 루트삭제는 `Interrupted <holder>`로 남긴다. 재시작은 level-based이며 살아 있는 nonterminal 루트 taint는 유지하고 끊긴 PMTC 준비는 반환 후 다시 대기한다.
현재 무효판정은 각 측정 record의 powerModeGeneration(start/end)과 자기 전환수다. 반환이 외부 NodeActuation을 재판정하지 않는다([무효판정 권위](../platform/node-agent.md#34-측정-무효-판정)). MeasurementInvalid 근거는 catalog/cost/baseline/curve 소비에서 제외하고 issue로 드러낸다.

### 2.4 전환 비용 runner

- 승인 전 `RunnerProfileMissing`(NodeUID/modelSpec/executionPath/시작모드 일치 준비가능 profile 없음), `RunnerContainerUnsupported`(firstfloor 아님), `RunnerNamespace`(serving namespace 다름)을 거절한다. 일치 profile은 최신측정, 시작 전력모드도 일치해야 한다.
- taint 후 measurement모드로 `runner-<PMTC UID hash>`를 준비하고 spec.servingService Service를 `chill.dacs.io/measurement-runner:<NodeUID>`로 만든다. selector는 정확 serving PodUID다. PMTC namespace observer가 Pod/Service를 읽는다.
- 성공은 `chill.dacs.io/runner-profile:<profileID>`와 Granted시각, 준비/result의 runnerProfileID로 고정한다. 실패는 RunnerPrepareFailed를 남기고 같은run에서 반환한다. 루트가 삭제돼도 NodeUID로 runner/Service를 회수한다.

### 2.5 operation 비용 측정과 serving-operation

상세 spec/판정은 [측정 API](measurement-api.md)가 권위다.
- opcost는 한 노드의 power/prepare/idle/remove 순서다. 승인 전 catalog profile의 ProfileMissing/NodeMismatch, ActuationNamespace, PowerModeUnknown, MeterUndeclared를 검사한다. profile/issue 모두 아직 없으면 대기하며 거절annotation과 Failed status는 같은 사유다.
- 승인과run등록은 같은 tickMu 구간이다. runtime/observer의 measurement Operations로 각 단계를 Measure하고 반환한다. Operation.ID=`<CR UID>.<순번>.<단계이름>`, 저장key/record가 그 CR을 가리키며 prepare/remove는 정확 profile preparation/instance다.
- 단계오류·non-Accepted에서 중단한다. 진행측정의 취소결과도 WithoutCancel로 기록한다. 미완료순서는 `serving/operation-sequences/<CR UID>.json`, schema `serving-operation-sequence.v1alpha1`의 sequenceID/node/requested/saved/reason을 남긴다. 저장된 개별 verdict는 유지하며 비용은 유효기록을 소비하되 SequenceIncomplete issue를 단다.
- ChillServer 재시작은 Granted/nonterminal opcost를 Failed: ChillServerRestart로 닫고 UID-prefix 저장key로 saved 목록을 복원해 순서기록 후 반환한다. terminal status는 바꾸지 않고 반환만 한다.
- isolation witness의 예상외 power generation은 PowerChangedDuringMeasurement/Insufficient와 MeasurementInvalid다.
- `POST /api/v1/scheduler/operations`의 prepare/remove는 live capture로 자동제어밖 Workload·비예약범위·실제 Node/Pod inventory를 확인한다. prepare는 ValidatePreparationCapacity, remove는 removableBy·route weight0·처리중요청없음이 필요하다. snapshot physical ID이므로 serving-<hash>도 제거 가능하다.
- route/unroute/release/reset의 typed 입력·ACK/readback은 [수동 원자 실행](scheduler-operations.md#수동-원자-실행-계약)을 따른다. 기존 TokenReview/SAR, operation-timeout 기본30분이며 `serving-operation --execute-config`는 manifest를 보내고 응답을 stdout으로 낸다.

### 2.6 측정 controller의 대기

BEO/MLIB/독립ProfilingRun/PMTC reconciler는 Granted 전 child/Job을 만들지 않고 5초 requeue한다. Refused는 BEO Failed, MLIB Insufficient, ProfilingRun ExecutionComplete=False, PMTC Failed이며 annotation 첫단어가 사유다. 필요한 ChillServer 측정kind patch/list·Node patch·serving Service create/delete 권한을 사용하며 Profiler에 새 서빙 writer를 만들지 않는다.

### 2.7 실행 전 입력 오류의 정상 반환

옛 BEO 이름을 label 값으로 쓰는 계약에서 이름이 label 제약을 벗어나면 namespace 조회로
controller UID가 같은 Run을 확인한다. 저장 plan이 명시적으로 없고 owned Run과
게시 result도 없을 때만 `Failed/InvalidRunSpec`으로 종료한다. 원 spec·UID를
보존하며 plan 생성, runtime 준비, 새 trial 없이 2.3의 정상 반환으로 이어진다.
기존 plan·owned Run·result 또는 조회 오류는 이 종료 판정에서 제외한다.
반환 뒤 기존 public DELETE의 UID/resourceVersion·terminal·소유 증거 검사를 적용한다.

repair 배포의 읽기 전용 검사는 같은 저장 위치·Evidence Store resolver를 사용한다.

```sh
go build -buildvcs=false -o /tmp/pretrial-retirement-check ./hack/pretrial-retirement-check
/tmp/pretrial-retirement-check --kubeconfig <path> --context <context> \
  --namespace <namespace> --system-name <ChillSystem> --name <BEO> --uid <UID>
```

종료 코드 0과 `eligible=true`는 현재 generation의 미시작 요청, 위 부재 조건,
Granted 시각, 실제 Node UID와 정확 요청 UID의 taint holder를 함께 확인한 결과다.
`NoSuchBucket`·권한 오류·조회 실패는 부재 증거로 인정하지 않는다. JSON에는
request/Node identity와 resourceVersion, 부재 판정, 검사 시각을 남긴다.
이 검사는 상태를 바꾸지 않는다. 배포 전 재검사와 실제 반환·삭제 확인은 실행자가 맡는다.

## 3. 저장 계약 (ADR 0019)

Node taint/admission/runner annotation은 stored science evidence가 아니다. PMTC runnerProfileID 도입의 result v1alpha4와 당시 Failed PMTC5개 정리는 역사적 cutover이며 현재 PMTC raw/선택필드 버전은 [node-agent 저장표](../platform/node-agent.md#4-저장-계약-adr-0019)를 따른다. operation v1alpha2·occupancy v1alpha1의 generation 선택필드와 미완료 sequence 원형을 유지하고 단일버전 reader를 쓴다.

## 4. 수용과 열린 경계

- 검사 불변식: 노드별 생성순서·거절시taint없음, 예약/전력pending경합, 반환실패taint유지·재시작복구, 예상외generation 무효, 수동작업/자동제어격리, runner exactidentity·회수, 순서실패·취소·재시작 기록, strict권한을 확인한다. 이 목록은 현재source의 통과기록이 아니다.
- live E-A1~8: MLIB 승인/최저모드반환 지연; serving BEO거절/SLO보호; 비CHILL Pod보존; PMTC runner/시도목표; opcost3기록; host serving-<hash> remove; 루트삭제/재시작회수; invalid/MeasurementInvalid 수와 출처를 확인한다. 결과는 [live evidence](../../plans/2026-10-13-scheduler-live-evidence-campaign.md)가 권위다.
- Measurement CRD 통합은 미채택 M5 제안이다. watch승인은 polling진입지연(E-A1)이 문제가 될 때의 대안이며 현재 구현약속이 아니다. POOL 측정owner/Dashboard 선택불가(DB-FR-34)는 해당 UI수용과 구분한다.
- 측정밖 재부팅·미확인 전력은 노드만 제외하고 독립 판단을 계속하는 [node-agent 계약](../platform/node-agent.md)을 따른다. 별도 전역 Pending 질문으로 유지하지 않는다.
