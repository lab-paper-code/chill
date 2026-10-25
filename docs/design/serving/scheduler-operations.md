# Scheduler 인스턴스 전달과 수동 원자 실행

현재 physical instance transport와 수동 실행 계약이다. 자동 판단과 DAG는
[스케줄러 구현 설계](scheduler-implementation.md), 측정 적격성은
[실행기와 측정 계약](scheduler-runtime-measurement.md#baseline과-실제-원자-동작-측정),
과거 네트워크·배포 관측은 [실행 이력](scheduler-execution-history.md)에 둔다.
수동 실행 성공을 automatic 판단·power 측정·FULL 수용으로 해석하지 않는다.

## Why concealment forces routing

Some configuration changes cannot be made underneath live traffic. A power mode
applies to a whole node, and on a memory-constrained device a new model cannot
be resident before the old one leaves.

The [transition specification](../../spec/05_transition_control.md) requires
planning request handling while a node changes state. Routing can move traffic
to other ready nodes when the changing node cannot continue serving.

### 목표 전환 중 Ready 부분집합 분배

최종 route의 prepare/power/drain 의존성은 유지한다. 다만 매 제어 tick에
controller는 같은 목표에서 실제 snapshot과 native 상태 모두 Ready인 목적지를
고르고, 워크로드별 목표 가중치를 그 부분집합에 합계 1000으로 재정규화해
별도 route 동작으로 적용한다. 다른 prepare뿐 아니라 전력 전환, 드레인 등
독립적인 느린 동작을 기다리지 않는다. 아직 Ready 목적지가 없는 워크로드는
현재 Ready 분배를 유지하며, 이미 Stopped이거나 이번 tick에 stop 가능한
인스턴스는 중간 분배로 되살리지 않는다.

이 동작도 `Operations.ApplyTarget`의 측정 profile/native policy ACK, 소유권과
현재 Workload 검증, 원자적 runtime 분배를 거친다. 기존에 수락한 요청은
원래 실행 계약과 drain 보호를 유지한다. 준비된 일부 용량만으로 전체 부하의
SLO를 보장한다는 뜻은 아니며, 요청별 측정 지연·수명 admission은 그대로다.
전환 중 목표를 새로 탐색하지 않고 최종 handoff 및 제거 조건도 바꾸지 않는다.

중간 동작의 ID는 `<목표 ID>-ready-<부분집합 hash>`이고 `Action.from`은
`ready-subset`이다. 기존 action/operation 관측 기록에 시작과 완료,
`operation.target`의 동결된 분배, `before.routing`과 `actual.routing`을 남긴다.
후속 부분집합이 이전 분배를 대체하므로 중간 route는 최종 목표 DAG의
완료 조건이나 `TransitionInProgress` 목표 자체가 아니다. 캠페인은 이
scheduler 관측 원본을 Run 기록에 함께 보존한다.


## Where a request branches

EdgeMesh's proxy is a DaemonSet. It intercepts through the iptables rules of the
node the client runs on and selects an endpoint there. No point exists through
which all requests pass.

An external consumer has no such node, so its request enters the cluster
somewhere, and that entry is the one point every external request crosses.

## 현재 인스턴스 전달 경로

고정 노드 테이블을 복원하지 않고, native physical instance의 현재 Kubernetes
Pod를 단일 목적지 Service에 연결한다.

Choosing a backend and reaching it pull in opposite directions: calling a shared
Service hands the choice to EdgeMesh, which applies no weight, and calling a pod
address directly does not resolve across the tunnel.

Each actual serving Pod gets `chill-executor-<Pod UID>`, a ClusterIP Service owned
by that Pod. Its selector is only `edge.dacs.io/serving-pod-uid=<actual Pod UID>`,
installed after Kubernetes assigns the UID; its sole TCP port is named `http`,
8080, targeting the isolated firstfloor container's named `http` port. The Service
carries `service.edgemesh.kubeedge.io/service-proxy-name: ""`, matching the
existing EdgeMesh proxy filter. One endpoint leaves EdgeMesh nothing to select:
CHILL chooses the physical instance, EdgeMesh transports the request.

```
entry point ── physical-instance choice ──▶ chill-executor-<Pod UID>:8080
                                                    │  EdgeMesh tunnel
                                                    ▼
                                       that Pod's firstfloor container
```

The decision is CHILL's and the transport is EdgeMesh's, which is the division
that already governs power mode.

This is an internal execution-instance transport, not a user-facing inference
service. Workload names describe the inference service and its SLO role; model
names identify the model artifact, not firstfloor or another execution engine.
The executor Service name is shared by all Workloads using that physical Pod.

목적지는 실제 Pod UID와 container identity를 구분한다. 생성 동작은 실제 UID label과
소유권·selector·port가 일치하는 Service만 생성/재사용한다. 같은 이름의 다른 binding은
수정하지 않고 거부한다. Observer와 Capture, policy read-back, native drain은 모두 이
Service의 ClusterIP를 사용하며 직접 PodIP·정적 노드 route fallback은 없다.
UID label은 실제 UID와 현재 label을 test하는 JSON Patch로 추가한다. CNI·kubelet의
status/resourceVersion 갱신은 이 metadata-only binding을 막지 않는다. 다른 label 변경이나
Pod 교체는 거부하며 재시도하지 않는다. 기존 preparation의 CPU·memory quantity는
stdlib `maps.EqualFunc(resource.Quantity.Equal)`로 값만 비교한다. API의 quantity 표기나
내부 문자열 cache 차이를 다른 자원 할당으로 취급하지 않는다.
GET으로 현재 Service의 소유권·selector·EdgeMesh label·port를 검증하고, selector가
현재 Pod/container 하나만 선택하는지 namespace Pod LIST로 확인한다. HTTP probe 뒤
Pod/container를 다시 읽어 교체된 executor를 이전 인스턴스로 게시하지 않는다.
Pod는 `RestartPolicyNever`여야 한다. 같은 Pod UID 뒤에서 container가 재시작하여
이미 게시한 physical instance의 실행 목적지가 바뀌는 것을 허용하지 않는다.
native model/state와 immutable profile/runtime evidence의 준비 검증은 그대로 유지한다.
Pod Ready나 Service 생성만으로 추론 준비를 완료하지 않는다.

prepare는 저장된 현재 정책을 유지하며 오래된 Pod 정책 annotation을 고친다.
annotation 수정 자체를 native acknowledgment로 간주하지 않는다.

제거는 native drain과 실제 container 종료 증거를 확인한 뒤 자기 Pod 소유의 Service를
resourceVersion/UID precondition으로 삭제하고 Pod finalizer 및 config를 해제한다.
이때는 namespace·이름·Pod owner identity만 확인한다. 자기 Service의 selector나 proxy
label drift가 종료된 Pod의 cleanup을 막게 하지 않는다. 다른 Pod 소유의 Service는
삭제하지 않는다. Pod owner reference는 Kubernetes GC에도 같은 수명을 연결한다.
Workload cleanup도 종료 증거 이후 자기 Pod의 Service를 해제한다.
Pod source finalizer의 추가/해제는 immutable Pod UID를 포함한 Kubernetes
strategic-merge primitive-list delta로 자기 항목만 변경한다. 다른 finalizer를
보존하며 unrelated kubelet status/resourceVersion 갱신은 충돌 원인이 아니다.
UID 변경이나 API의 실제 metadata 제약 오류는 그대로 반환한다. 해제 전
UID/container-bound 실제 종료 증거와 Pod 종료 확인은 유지한다.
기존 EdgeMesh 설정이나 다른 workload는 변경하지 않는다.
namespace의 ChillServer Role에는 Service GET/DELETE를 기본으로 허용하며,
scheduler/native operation 경로가 켜진 경우 CREATE를 추가한다.

## 수동 원자 실행 계약

`POST /api/v1/scheduler/operations`는 측정과 별개의 수동 원자 실행이며 `autonomousWorkloads`에 없는
Workload 하나를 대상으로 한다. 몸체는 `{kind, workloadUID, profileID, node, instance, fixedBatchSize}`이고,
`kind`는 `prepare` 또는 `remove`다. 호출자는 scheduler ConfigMap update 권한과 `/chill/api/v1/scheduler/operations`
POST 권한을 가진다. ChillServer는 `tickMu` 안에서 다음을 확인하고 그 Node의 run을 등록한 뒤 잠금 밖에서 scheduler와
같은 `Operations`로 실행하며, 응답은 완전한 `OperationResult`다.

- Workload가 마지막 snapshot의 현재 Workload이고 자동제어 밖이다.
- Node가 있고 측정 taint, 실행 중인 run, 진행 중인 Node label 전력 요청이 없다(`NodeBusy`).
- prepare: `profileID`가 그 Node의 도출된 preparation이다. 대상 Workload가 instance owner다. 양수
  `fixedBatchSize` B는 preparation의 firstfloor `Policy.fixedBatchSize`와 instance에 함께 기록하고 native config
  검증이 B가 측정되고 초기화된 batch인지 확인한다([strict fixed-size baseline](scheduler-runtime-measurement.md#strict-fixed-size-baseline-serving)).
  fresh Node와 모든 Pod 요청을 `ValidatePreparationCapacity`가 마지막 snapshot의 inventory와 예약으로 확인하며,
  설정된 serving namespace의 기존 serving Pod, 미확인 GPU 소유권, CPU 예약 부족, memory 초과와 MemoryPressure는
  거절한다.
  적용된 분배에 UID가 있는 Workload는 그 분배를 따른다. 빈 분배와 nil 값도 요청 분배 0을 유지한다.
  적용된 분배에 UID가 없는 Workload는 Pod의 owner별 routing annotation을 따른다. 수동 준비와 측정 준비는
  Ready, 소유권, native 설정 ACK를 확인한 인스턴스의 seed를 사용한다. planner 준비의 seed는 빈 분배다.
- remove: 대상이 snapshot의 instance이고 다른 Workload가 소유하거나 binding하지 않는다. 어느
  Workload의 route 가중치도 0이고 그 instance가 맡은 처리 중 요청이 없다. 분배는 먼저 `staticRouting`으로 바꾼다.

instance는 snapshot의 ID로 고르므로 계획기가 만든 `serving-<hash>` Pod도 대상이 된다. 이는 manual execution 근거이며
automatic scheduler judgment나 power 측정 근거가 아니다.

## serving-operation CLI

`serving-operation`은 위 route에 JSON 파일을 보내고 ChillServer의 응답을 stdout에 쓰는 클라이언트다. flags는
`--execute-config FILE`, `--chill-server URL`(HTTPS origin 하나), `--chill-ca-file`, `--chill-token-file`,
`--timeout`(기본 30분)이다. 명시한 CA와 token만 쓰고 redirect를 따르지 않는다. manifest는 strict decode로 확인한 뒤
보낸다. 응답의 `error`가 있으면 0이 아닌 값으로 끝난다. ChillServer는 이 route를 `--operation-timeout`(기본 30분)까지
기다린다.

## 정적 배치 사용법

정적 구성 비교와 고정 배치 기준선(`SCH-35`)은 자동제어 밖 Workload를 손으로 배치한다. 대상 Workload는
`autonomousWorkloads`에 없어야 한다(`GET /api/v1/scheduler/settings`로 확인).

1. prepare: manifest `{"kind": "prepare", "workloadUID": "<UID>", "profileID": "<ID>", "node": {"name": "<Node>",
   "uid": "<Node UID>"}, "fixedBatchSize": 1}`를 `serving-operation --execute-config prepare.json --chill-server
   https://<entrypoint> --chill-ca-file ca.crt --chill-token-file token`으로 보낸다. `profileID`는
   `/api/v1/scheduler/profiles`에서 고르고 `fixedBatchSize`는 고정 배치 기준선에서만 준다. 준비한 instance는 그
   Workload의 routing annotation을 받고 적용된 분배에 UID가 없으면 그 seed로 요청을 처리한다.
2. 분배: `GET /api/v1/scheduler/settings` 응답에 `"staticRouting": {"<Workload UID>": {"<instance ID>": 1000}}`과
   `"reason": "<선택 이유>"`를 더해 `PUT /api/v1/scheduler/settings`로 보낸다. Dashboard 운영 설정의 "요청 분배
   바꾸기"도 같은 요청이다. `staticRouting`은 선언 전체이고, 선언이 바뀌면 `reason`이 필수다. instance ID는
   `/api/v1/pools` 또는 `/api/v1/scheduler/state`의 snapshot에서 읽는다. 응답의 설정 변경 기록은 바꾼 사람, 전후
   설정과 선언, 선택 이유, `validatedAt`을 담는다. 반영은 `manual-static-routing` 동작의 `actual.routing`과
   `/api/v1/scheduler/state`의 snapshot routing으로 확인하고, 그 동작의 `operation.settingsValidatedAt`과 ConfigMap 구성의
   `staticRoutingValidatedAt`이 이 설정 변경의 `validatedAt`이다([설정 API](scheduler-api.md)).
3. remove: 그 Workload 항목을 `{}`로 바꾼 선언을 같은 방법으로 보내 routing을 지우고, snapshot routing이 비면
   instance마다 `{"kind": "remove", "workloadUID": "<UID>", "node": {...}, "instance": "<instance ID>"}`를 보낸다.
   끝나면 그 항목을 뺀 선언을 이유와 함께 보낸다. 남은 항목이 없으면 `"staticRouting": {}`가 선언을 지운다.

r7 뒤 남은 계획기 Pod 다섯 개를 이 순서로 지웠다. routing을 `{}`로 바꾸고 약 5 s 뒤 snapshot routing이 비었고,
remove 다섯 번이 모두 첫 시도에 `complete`로 끝났다. instance ID로 고르므로 계획기가 만든 `serving-<hash>` Pod도
같은 방법으로 지운다.

## 원자 동작 측정의 격리

측정 POOL은 taint `chill.dacs.io/measurement:NoSchedule`이 있는 Node다. ChillServer가 측정 객체에 따라
taint를 붙이고 뗀다([측정 admission](measurement-admission.md)).

scheduler는 이 Node를 배치 대상에서 제외하고, 이 Node의 Workload prepare와 `ChangePower`는
`NodeInMeasurement`로 실패한다. OperationCostMeasurement(`opcost`)는 한 Node의 원자 동작 순서를 선언하고, ChillServer가
그 순서를 한 번 승인해 각 단계의 `operation.id`를 `<CR UID>.<순번>.<단계 이름>`으로 두고 차례로 측정한 뒤 Node를 반환한다
([측정 admission](measurement-admission.md) 2.5절). meter와 store는 그 Node의 설정 meter와 ChillServer store이고, prepare는
catalog 검증 profile에 연결한다. 측정 instance는 taint의 toleration을 갖고 ChillServer의 runtime과 observer로 관측한다. taint가 ChillServer를
이 Node에서 떼어 두므로 같은 model을 쓰는 Workload가 있어도 동일 원자 동작을 실행한다. 측정 끝의 격리 witness
`isolation`은 Node, taint 유무 `Tainted`, 그 model을 쓰는 Workload 목록을 기록한다. Node가 다르거나 taint가 없거나 그
측정이 요청하지 않은 전력 변경이 측정 중 적용되었으면(`PowerChangedDuringMeasurement`) 기록은 `Insufficient`로 남는다.
기록은 ChillServer store의 고정 key `serving/operation-measurements/<Node UID>/<operation ID>.json`에 immutable로 쓴다.
단계가 실패하거나 `Accepted`가 아니면 순서가 멈추고, 끝나지 않은 순서는 `serving/operation-sequences/<CR UID>.json`에
기록된다. CR status는 단계마다 `{name, key, verdict}`와 phase(`Complete`, `Insufficient`, `Failed`)다.

`power` 단계는 DeviceClass와 actuation policy에 따라 Node의 desired power-mode label을 쓰는 scheduler와 같은 `ChangePower`를 실행한다.
측정 실행은 taint가 있는 Node에서만 동작을 허용하므로 power도 측정 prepare와 같은 경로로 진행한다. runner가 없는 빈
Node를 가장 낮은 모드에서 profile 모드로 올린 기록이 가용 노드를 다시 넣는 power 비용이 된다. 기록의 측정 전후 모드가
비용의 from과 to이고 비용의 적재 상태는 빈 상태다. power 기록은 측정 전후 State가 모두 비어 있을 때 `Accepted`이며, runner를
적재한 전환은 PowerModeTransitionCost가 측정한다. power 측정 구간은 Node annotation에 기록된 적용과 독립 read-back 확인에 settling
(`settlingDurationSeconds`)을 더한 구간이고, 비용에는 목표 모드의 settling 에너지가 들어간다. 그래서 적용이 meter 표본
간격보다 짧은 전환(jetsonx 약 1.5 s)도 E1의 60 s settling으로 coverage 검사를 통과한다. 측정 구간 전체가 meter 표본
간격보다 짧으면 coverage 검사가 기록을 `Insufficient`로 남긴다.
