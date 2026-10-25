# Scheduler 인스턴스 전달과 수동 원자 실행

physical instance transport와 수동 실행 계약이다. 2026-10-25 배분·reset 개정은
구현 목표이며 코드 위치는 변경할 현재 구현을 가리킨다. 자동 판단과 DAG는
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

### 정책 반영 대기 표시 — SCH-48, AC-34~35

desired/native DecisionID만 다르면 probe는 오류 대신 `PolicyAckPending` 상태를
반환한다. 이번에 검증한 물리 ID·Node·profile·할당·소유·slot·native readiness와
정책·요청 목록을 그대로 유지하며 동일 Pod/container와 단일 executor Service 검증도
생략하지 않는다. scheduler 원본·compact·summary와 관측 기록에는 instance의
`reasons`로, POOL에는 `InstanceState.reason`으로 노출한다. POOL 화면의 **반영 대기**는
새 정책 적용 확인을 기다린다는 뜻이지 인스턴스 부재나 준비 실패가 아니다.
소유/바인딩된 서비스 POOL과 활성 POOL에 계속 남고, 다음 정상 probe의 ACK가
일치하면 표시가 사라진다. 현재 실제 정책을 desired 값으로 덮지 않으며 route
완료에는 기존 native 정책 ACK가 여전히 필요하다. 실제 읽기·metadata·Pod/Service
검증 실패는 기존 관측 실패 경로를 따른다.

실제 읽기 실패로 `InstanceUnavailable`인 Node에서는 같은 Name·UID의 새 prepare와
전력 변경을 보류하고, snapshot 부재를 remove 완료 증거로 사용하지 않는다(SCH-49).
해당 Node의 이유와 identity는 보류된 route/후보 사유에 남고 후속 route·제거도
대기한다. 이미 실행 중인 동작의 실제 상태는 바꾸지 않으며 정상 다른 Node의
독립 prepare·전력 동작은 계속 가능하다. 기존 전체 자원 검증의 전역 Pending은 유지한다.


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

`POST /api/v1/scheduler/operations`는 측정과 별개의 서버 수동 실행이다.
prepare/remove/route/unroute/release는 `autonomousWorkloads` 밖의 Workload 하나를 대상으로 하며,
reset은 명시한 UID들의 자동제어를 먼저 해제한다(`SCH-39`, `SCH-41`).
기존 prepare/remove 본문 `{kind, workloadUID, profileID, node, instance, fixedBatchSize}`를 유지하고
추가 본문은 다음과 같다. 종류별 필드만 strict decode하며 선택 이유 `reason`은 필수다.

| kind | 본문 | 선언과 실제 적용 |
|---|---|---|
| `route` | `{kind:"route", workloadUID, weights:{"<physical instance ID>":1000}, reason}` | 해당 UID 선언만 교체. weight는 nonnegative int32이고 양수 대상이 하나 이상이어야 한다. 현재 UID·Ready·소유권·모델·native policy ACK를 검증한다 |
| `unroute` | `{kind:"unroute", workloadUID, reason}` | 해당 UID를 빈 map으로 유지하여 새 요청 배분을 막는다. 이미 접수한 요청은 기존 수명·drain 보호를 유지한다 |
| `release` | `{kind:"release", workloadUID, reason}` | 해당 UID 선언과 runtime override를 삭제하고 현재 및 이후 Pod 기본 표시를 따른다. 다른 UID는 유지한다 |
| `reset` | `{kind:"reset", workloadUIDs:["<UID>"], reason}` 또는 `{kind:"reset", scope:"all-non-autonomous", reason}` | UID 목록과 scope 중 정확히 하나. scope는 수락 시점 비자율 UID 목록으로 고정한다 |

호출자는 scheduler ConfigMap update 권한과 `/chill/api/v1/scheduler/operations` POST 권한을 가진다.
ChillServer는 `tickMu` 안에서 대상과 실행 충돌을 확인하고 run을 등록한 뒤 잠금 밖에서
기존 `Operations`로 실행한다. UID 범위 reset은 노드별 run과 전역 route 충돌도 차단하며,
자율 해제 후 이미 실행 중인 관련 동작이 있으면 안전 완료를 확인하기 전 다음 단계로 가지 않는다.
응답은 기존 `OperationResult` envelope와 실제 ACK/readback을 사용하고, 배분 작업은 전후 선언과
`actual.routing`을 포함한다. 선언 저장만으로 complete를 반환하지 않는다.

기존 prepare/remove의 사전 조건은 다음과 같다(reset의 자율 해제는 위 별도 계약을 따른다).

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
  Workload의 route 가중치도 0이고 그 instance가 맡은 처리 중 요청이 없다. 먼저 `unroute`의 ACK를 확인한다.

instance는 snapshot의 ID로 고르므로 계획기가 만든 `serving-<hash>` Pod도 대상이 된다. 이는 manual execution 근거이며
automatic scheduler judgment나 power 측정 근거가 아니다.

### reset 순서·실패·멱등

`SCH-41`의 reset은 다음 순서를 하나의 서버 작업으로 실행한다. 응답의
`steps[]`는 `{name, startedAt, endedAt, status, before, actual, error}`이며 기존
`OperationResult`의 `startedAt`/`endedAt`, `status`, `error`와 전체 전후 상태도 유지한다.

1. 대상 UID만 `autonomousWorkloads`에서 제거하고 설정 적용 readback을 확인한다.
2. 대상 UID마다 unroute를 적용하여 새 요청 분배 0의 ACK/readback을 확인한다.
3. 해당 UID의 미완료 요청이 0이 될 때까지 기존 native drain 증거와 함께 기다린다.
4. 해당 UID가 **소유**한 physical instance만 stop/remove한다. 단순 binding을 소유권으로
   간주하지 않으며 타 UID binding/소유권 충돌은 실패다. UID/container 종료 증거,
   자기 executor Service 해제와 실제 Pod 소멸을 확인한다.
5. 대상 UID의 배분 선언과 runtime override만 release하고 삭제 readback을 확인한다.
6. 제거로 비게 된 Node만 DeviceClass/actuation policy가 제공하는 최저 전력모드로
   `ChangePower`한다. 측정 taint·다른 owner·요청이 남은 Node를 변경하지 않고,
   실제 모드 readback과 Ready를 확인한다. 변경 불필요/지원하지 않는 고정 모드는
   현재 상태와 skip 사유를 기록한다.

동일 slot에 선택 Workload 소유의 Pod가 여러 개 있어도 정확한 Pod UID/container ID를
모두 열거하고 remove 직전에 다시 읽어 각각 drain/remove한다(AC-36).
runtime 분배·scheduler 상태 조회·native policy 적용은 exact physical ID를 우선하고,
exact가 없을 때만 유일한 TargetID alias를 허용한다. `A→S`, `B→A`에서 A는 항상
실물 A이며 B도 별도로 정리한다. exact 없는 다중 alias는 여전히 오류다.
타 Workload 소유·바인딩과 measurement Node/다른 Node UID 보호는 유지한다.

오류·timeout·관측 실패는 해당 단계와 실제 부분 상태를 기록하고 즉시 멈춘다.
자동 재시도·되돌림·이후 단계 실행은 없고, 실패한 선언을 refresh가 자동 적용하지 않도록 한다.
다시 호출하면 현재 상태에서 이미 충족된 단계를 변경 없이 완료 처리한다. remove 뒤
release 또는 lower-power가 실패해 Pod가 사라져도, 현재 대상 UID와 호환되는 준비 후보의
동일 Node UID를 다시 확인하고 최저 모드/Ready 복귀가 확인되지 않은 빈 Node를 계속 처리한다.
제외 Node·측정 소유 Node·다른 serving Pod가 남은 Node는 이 빈 후보 검색에서 제외한다.
최저 모드의 실제 readback과 Ready 확인 전에는 성공으로 처리하지 않는다. 정리된 범위의
두 번째 reset은 Pod·선언·전력 요청을 새로 만들지 않고 성공한다(`SCH-AC-33`).
멱등성은 실제 상태 확인으로 얻으며 별도 재개 상태기계나 숨은 client 재시도는 두지 않는다.
기존 전역 분배 작업 또는 대상 Node의 작업이 진행 중이면 설정을 바꾸기 전에 거절한다.
수락 후에는 전역 분배와 대상 Node를 예약하여 다른 작업이 단계 사이에 끼어들지 못하게 한다.
각 단계의 시작과 완료/실패를 기존 action 관측 기록에 남긴다.

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
   "uid": "<Node UID>"}, "fixedBatchSize": 1, "reason": "<준비 이유>"}`를 `serving-operation --execute-config prepare.json --chill-server
   https://<entrypoint> --chill-ca-file ca.crt --chill-token-file token`으로 보낸다. `profileID`는
   `/api/v1/scheduler/profiles`에서 고르고 `fixedBatchSize`는 고정 배치 기준선에서만 준다. 준비한 instance는 그
   Workload의 routing annotation을 받고 적용된 분배에 UID가 없으면 그 seed로 요청을 처리한다.
2. 분배: `{"kind":"route","workloadUID":"<UID>","weights":{"<instance ID>":1000},"reason":"<선택 이유>"}`
   를 같은 operations endpoint로 보낸다. instance ID는 `/api/v1/pools` 또는
   `/api/v1/scheduler/state` snapshot에서 읽고, 응답 ACK와 `actual.routing`,
   해당 인스턴스의 `routable`로 반영을 확인한다(`SCH-36`, `SCH-39`).
3. 배분만 멈추려면 `unroute`, Pod 기본 표시로 되돌리려면 `release`를 보낸다.
   완전 정리는 `{"kind":"reset","workloadUIDs":["<UID>"],"reason":"<정리 이유>"}`를 보낸다.
   설정 PUT으로 `staticRouting`을 수정하거나 클라이언트가 drain/remove/power 순서를 조립하지 않는다.

수동 분배는 프로세스 재시작 시 유지되지 않으며 운영자가 재명시해야 한다.
저장된 선언은 관측 근거이며 서버 재시작이나 설정 refresh가 실행을 재시도하지 않는다.

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

## 구현 위치와 검사

- `internal/serving/httpapi/scheduler.go:Handler.scheduler`는 설정 PUT의 읽기 전용 배분 필드를 거절한다.
  `httpapi/operations.go`와 `kube/measurement_sequence.go:DecodeManualOperation`은 종류별 strict 입력과 권한을 확인한다.
- `internal/serving/kube/static_routing.go:manualRoutingOperationLocked`,
  `persistManualConfiguration`, `executeManualRouting`은 UID별 선언·ACK/readback을 실행한다.
  `measurement_sequence.go:ExecuteManual`은 admission과 작업 예약을 담당하고,
  `manual_reset.go:executeManualReset`은 순서·부분 실패·멱등 상태 확인을 담당한다.
- `internal/serving/kube/operations.go:Operations.Execute`, `StopInstance`,
  `removeInstance`, `ChangePower`, `Capture`: reset의 단계 실행·native drain·종료·전력
  readback을 재사용하고 단계별 결과를 누적한다.
- `internal/serving/runtime_scheduler.go:Runtime.ReleaseSchedulerRouting`는
  빈 override와 UID 삭제를 구별한다. release는 해당 UID key만 지우고 최근
  실제 Pod에서 읽은 seed로 돌아가며, 이후 Ready 인스턴스의 seed도 따른다.
  다른 UID의 override는 보존한다.
  자율 목표 적용도 현재 자율제어 UID만 갱신하여 release한 비자율 UID의 override를 재생성하지 않는다.
  Pod 기본 seed 영속화도 같은 UID 범위만 갱신하여 다른 UID의 수동 override가 기본값으로 굳어지지 않게 한다.
  runtime이 계산한 `InstanceState.routable`을 snapshot/POOL이 공유하고, 새 요청에 대한 선언과 Ready/Stopped·소유권·모델
  필터를 모두 적용한다. 이미 접수한 요청만을 위한 보존 route는 제외한다.
  `TestReleaseSchedulerRoutingFollowsNewReadySeed`는 release 뒤 새 Ready
  인스턴스로 실제 새 요청이 dispatch되고 다른 UID가 유지됨을 검사한다(`SCH-AC-32`).
- 삭제 대상: `hack/e5/e5.py:CPO.clear_owned`, `CPO.finish`의 클라이언트
  unroute/drain/remove/Pod 대기/선언 해제/전력 복귀 조립을 reset 호출·결과 보존으로 바꾼다.
  `CPO.route_ack`의 ConfigMap timestamp/manual-static-routing 추적은 명시적 작업 ACK로,
  `CPO.start`, `CPO.restore`, `CPO.factor`와 `hack/e5/demo.py:resume`의
  `staticRouting={}` 재개 조작은 release ACK 후 기존 자율 설정 적용으로 바꾼다.
  `CPO.put`과 `hack/e5/e5-wire.py:Prepare`의 설정 GET→PUT도 읽기 전용
  `staticRouting`/`staticRoutingValidatedAt`을 제외해 새 설정 입력 계약에 맞춘다.
  실험 부하·관측 수집·입력 설정 복원은 유지하며 Workload 정의 삭제는 reset에 넣지 않는다.
- 검사: route/unroute/release 각각의 선언·실제 readback·타 UID 보존, 잘못된 UID/target·
  자동제어 충돌 거절, release 뒤 새 측정 인스턴스 실제 요청 수신(`SCH-AC-32`),
  reset 순서·요청 0·소유 Pod 소멸·빈 Node 모드 Ready, 각 단계 실패 시 후속 미실행,
  성공한 reset 두 번 호출에서 두 번째 무변경 성공(`SCH-AC-33`)을 확인한다.

ACK 대기/관측 실패 회귀는 다음 production 경로를 검사한다.

- AC-34: `TestPolicyAckPendingPreservesPhysicalOccupancyAcrossTicksAC34`가 실제
  `Probe→Refresh→capture→Tick` 동안 배분 변경의 native ACK를 지연시키고,
  동일 실물·할당·owner/slot·Ready·POOL과 진행 중 목표, prepare/Pod Create 0회를 확인한다.
  `TestPolicyAckPendingRetainsRouteEffectAndReplicaCountAC34` 및 운영 전력의
  `policy-ack-pending-active-pool-AC34`는 완료 효과·현재 replica 수와 같은 meter 구간의
  whole/active joules 불변을 검사한다. compact/summary도 기존 이유를 보존한다.
- SCH-49: `TestInstanceUnavailableDefersOnlyMatchingNodeAC34`는 네 Node guard,
  보류 이유·Node identity·관측 복귀 후 해제와 정상 다른 Node의 독립 준비를 검사한다.
- AC-35: `TestPowerChangeDrainsPendingAndNormalInstancesAC35`는 두 실물의 DAG를,
  `TestPendingNativeRequestsBlockActualPowerUntilStopAndDrainAC35`는 실제 stop·분배 차단,
  native queued/running 동안 전력 요청 0회 및 빈 native 목록·drain 뒤 전력 요청 1회를 검사한다.
- AC-36: `TestResetCleansAllDuplicateSlotExecutorsAC36`는 A만 routed인 `A→S`, `B→A`
  전체 정리, 각각의 native drain·UID/container 종료·Pod/Service/config 소멸,
  타 소유분 보존과 재호출 Pod 쓰기 0회를 검사한다. runtime/scheduler/policy 해석기는
  목록 순서에 관계없는 exact 우선 및 exact 없는 다중 alias 거절도 검사한다.
