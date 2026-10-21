# Node agent와 inventory 자동화 (M2)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M2 설계다. 명세 변경은 없다. 사람이 손으로
쓰는 노드별 scheduler 설정 필드를 노드와 측정 근거에서 도출하고, 노드 쪽 component를 agent 하나로 합친다. M3 단일
writer의 노드 쪽 준비이기도 하다([B2](../architecture-rethink/03-b2-single-writer-node-state.md)).

코드 근거는 `e3867f5`(workload-pools, M0, M1, M4 κ 계획기)이다. live 근거는 2026-10-19 15:00 UTC에 `kubectl get`으로
읽은 snapshot("snapshot")과 M4 live 시험에서 본 사실("M4 live")이다. 실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 1. 측정한 문제

| # | 문제 | 근거 | 영향 |
|---|---|---|---|
| 1 | `powerRequests`가 비어 있어 모든 노드가 `PowerModeUnavailable` | 사용자 보고, snapshot `powerRequests: {}`, 조건 `kube/scheduler.go:438-440` | 가용 노드 판정(`scheduler_plan.go:56-59`)에서 모두 빠져 가장 낮은 모드로 내리지 못하고, 현재 모드와 다른 모드의 측정 근거는 option이 되지 못한다(`scheduler_capacity.go:469`) |
| 2 | `cpuInventory`가 6개 노드만 있었음 | 사용자 보고. snapshot 시점에는 10개 노드이고 모두 `0..capacity-1`과 같음 | 빠진 노드는 `PlacementInventoryMissing`, 그 노드 profile은 `PreparationUnavailable`(`cmd/chill-server/profiles.go:89-101`) |
| 3 | `gpuInventory`가 GPU 노드 7개 중 3개(genesis1, genesis2, orin-nano) | snapshot. genesis3, jetson-nano, jetsono, jetsonx는 allocatable `nvidia.com/gpu=1` | 그 노드의 CUDA profile은 `PreparationUnavailable`(`profiles.go:102-105`) |
| 4 | Jetson CUDA profile의 측정 `supplementalGroups: [44]`를 serving이 거절 | 배포 값 `values-profiler.yaml:82,90,98,106` | 해결됨(`4cf8f38`, 2.2절) |
| 5 | `powerRequests` 항목이 이름, `stageRequestedAt`, `deadline`이 고정된 NodeActuation 전체 | `ChangePower`가 복사본을 만들고 `AlreadyExists`를 받아들임(`kube/operations.go:786-794`). M4 live: deadline이 지난 뒤 모든 전력 동작이 실패했고 손으로 2026-10-28로 갱신했다. 같은 모드로 다시 전환하면 이전 Ready 객체를 재사용했다 | 전력 동작이 deadline 이후 전부 실패하고, 반복 전환은 적용 없이 성공으로 끝난다 |
| 6 | 노드 DaemonSet이 측정 taint를 tolerate하지 않음 | snapshot: 10개 노드 모두 `chill.dacs.io/measurement`, `chill-node-discovery` desired 0 misscheduled 10, `chill-node-actuator` desired 0 misscheduled 4 | 해결됨(`958c1a8`) |
| 7 | NodeActuator 노드를 손으로 붙인 label `edge.dacs.io/node-actuation=enabled`로 고름 | `values.yaml` `nodeActuator.nodeSelector`, snapshot의 Jetson 4대 | 새 Jetson마다 수동 단계 |
| 8 | 같은 meter 사실을 두 곳에 씀 | snapshot의 `meters` 10개가 Profiler `metricSources`의 전력 부분(kind, endpoint, metric, metricLabels)과 모두 같음. Node UID는 손으로 복사 | 노드 재가입 때 UID 불일치 |
| 9 | 재부팅 뒤 확인되지 않은 전력 상태가 모든 판단을 멈춤 | M4 live: jetson-nano가 적용 중 멈췄다가 재부팅해 최신 NodeActuation이 `Failed`로 남았고 실제 모드는 MAXN이었다. 전력 확인은 최신 NodeActuation의 Ready만 받는다(`kube/observer.go:546-548`). 그 노드 instance의 source가 빠지고, capture는 snapshot 사유 `actual native/profile snapshot unavailable`을 남긴다(`kube/scheduler.go:524-527`) | 모든 scheduler 판단이 `Pending`. 새 MAXN NodeActuation을 손으로 만들어 풀었다 |

## 2. 결정

### 2.1 필드별 출처

| scheduler 설정 필드 | 출처 | 기각한 대안 |
|---|---|---|
| `cpuInventory` | agent가 `/sys/devices/system/cpu/online`을 읽어 Node annotation `edge.dacs.io/cpus`(kernel cpuset 표기, 예 `0-11`)에 쓴다. ChillServer capture가 `k8s.io/utils/cpuset`(이미 의존성)로 읽어 `SchedulerNode.CPUs`로 쓴다. 읽을 수 없거나 비어 있으면 `PlacementInventoryMissing` | Node `capacity.cpu`에서 `0..n-1`: 코드는 더 적지만 비연속 ID에서 조용히 틀린다. `present`: nvpmodel 모드가 끈 core를 배치 후보로 낸다. 새 CRD status: 소비자가 하나뿐이다 |
| `gpuInventory` | ChillServer가 그 노드(같은 Node UID)의 검증된 profile `RuntimeMetadata.GPUUUID`를 모은다. allocatable `nvidia.com/gpu` 수 검사(`kube/scheduler.go:429`)는 그대로 둔다 | agent가 `/proc/driver/nvidia/gpus/*/information`: dGPU에만 있고 Jetson에는 없다. agent에 NVML이나 libcuda: cgo나 host driver mount가 필요하다 |
| `powerRequests` | Tick capture가 노드의 DeviceClass(`nodeClassMatches`, `kube/operations.go:1082`)에서 actuator가 `Fixed`가 아니고 `powerActuation.policies`에 policy가 있는 모드를 요청 가능 모드로 정한다. 전력 동작을 시작할 때 NodeActuation을 그 시각으로 만든다(새 이름, `stageRequestedAt = now`, `deadline = now + maxActuationDurationSeconds`). 만드는 방식은 `campaign/controller/transition_builder.go:83-100`과 같다 | 설정의 요청 template: 문제 5. refresh 때 한 번 도출: refresh는 ConfigMap과 측정 출처가 바뀔 때만 돌아 Node와 DeviceClass 변화를 놓친다 |
| `meters` | Profiler `metricSources` 항목에 `meterID`를 더하고, ChillServer가 `{id: meterID, node: live Node 이름과 UID, scope: node-exclusive-wall, source: 항목의 전력 부분}`을 만든다 | meter ID를 노드 이름으로: 저장된 operation 측정이 `MeterUndeclared`(`kube/cost.go:115`)가 되어 비용을 다시 재야 한다 |
| `reserved` | 설정에 남는다(운영자 정책). 발견한 CPU 밖의 예약 CPU는 capture에서 그 노드를 `PlacementInventoryMissing`으로 만든다 | - |

- 어느 Shelly가 어느 노드에 꽂혔는지는 노드에서 발견할 수 없는 유일한 노드별 사실이라 `metricSources`에 한 번 선언한다.
  meter ID와 source 값이 지금 값과 같으므로 기존 비용 근거가 그대로 묶인다(`kube/cost.go:257`).
- ChillServer는 Profiler facts ConfigMap(`--profiler-facts-configmap`)의 `metricSources`와 actuation policy를 Profiler와 같은
  decoder로 읽는다. 그 ConfigMap의 resourceVersion을 refresh signature에 넣는다. serving-kube는 policy 형식 때문에
  `node-actuator-core`에 의존한다(`.go-arch-lint.yml`).
- `checkProfileInventory`(`profiles.go:88-111`)는 지운다. Tick의 후보 검사(`ExclusiveCPUInfeasible`,
  `ExclusiveGPUInfeasible`)가 live snapshot으로 같은 조건을 본다.
- agent는 CPU 파일을 10초마다 읽고 바뀔 때만 patch한다. 전력모드 전환 뒤 settle(60초) 안에 새 값이 올라온다.

### 2.2 runtimeAccess

serving Pod는 profile의 측정 `runtimeAccess.supplementalGroups`를 Pod `securityContext.supplementalGroups`로 재현한다.
privileged는 지금처럼 RKNN host delivery에서만 온다. group 값은 측정 조건에 고정되어 있고(`hack/runtime-declarations.py`가
측정 Job과 같은 신원으로 만든 선언), 재현 여부는 이미 `samePreparedPod`가 비교한다(`kube/operations.go:333`).

기각: agent가 장치 파일 GID를 발견해 쓰는 것. 측정 조건과 다를 수 있는 두 번째 출처가 생긴다.

### 2.3 agent 구성

- 바이너리 `cmd/node-agent` 하나, 이미지 하나, operator 설정과 reconciler 하나(`internal/operator/nodeagent`).
- DaemonSet과 ServiceAccount는 권한 경계를 따라 둘이다. actuation DaemonSet은 chart `nodeAgent.actuation.enabled`로
  켜고, chart가 이 값을 ChillSystem `spec.nodeAgent.actuation`에 쓴다.

| DaemonSet | 노드 | 하는 일 | 권한 |
|---|---|---|---|
| `chill-node-agent` | 모든 노드 | hardware signature discovery, `edge.dacs.io/cpus` | 비root, 읽기 전용 `/proc`, `/sys`, `/etc`, Node `get`, `patch` |
| `chill-node-agent-actuation` | `powerActuation.policies`에 나오는 DeviceClass의 `nodeSelector`에 맞는 노드 | NodeActuation 적용(`--actuate`) | hostPID, `SYS_BOOT`, 쓰기 `/sys`, nvpmodel mount, Node `get`, `patch`, NodeActuation과 Lease 권한 |

- operator가 두 DaemonSet에 `chill.dacs.io/measurement` toleration을 고정으로 붙인다. actuation 노드 선택은 DeviceClass
  `nodeSelector`를 nodeAffinity term으로 옮겨 만들고, operator는 DeviceClass를 watch한다. 손 label은 지운다.
- 기각: DaemonSet 하나. 한 Pod template은 모든 노드에 같은 mount와 capability를 주므로 control-plane genesis1과 서버에도
  `SYS_BOOT`와 쓰기 `/sys`가 생기고, nvpmodel mount 경로가 Jetson에만 있어 다른 노드에서 Pod 생성이 실패한다.
- 기각: 지금처럼 두 component 유지. 이미지, operator 설정, reconciler가 둘씩 남는다.

### 2.4 agent에 넣지 않는 것

전력 관측(metric-observer)과 census(workload-observer)는 지금 자리에 둔다. metric-observer는 Shelly gateway
(`155.230.35.213:18766`)와 monitoring namespace의 `edge-metrics-exporter`를 HTTP로 읽고, workload-observer와 ChillServer
census(`captureNodePods`)는 Pod LIST/WATCH다. 노드 접근이 필요 없고, 결과는 Run evidence 계약이라 측정이 진행 중일 때 옮기면
ADR 0019 변경이 생긴다. 1장의 문제 중 이것으로 풀리는 것은 없다. 그래서 05 표의 "전력, census 통합"은 agent 범위에 넣지 않는다(11장).

### 2.5 현재 전력모드 read-back

- `chill-node-agent-actuation`이 10초마다 actuator의 현재 값을 읽어(NodeActuation 적용 때 쓰는 독립 read-back과 같은
  adapter) Node annotation `edge.dacs.io/power-actuator`(`PowerModeActuator` JSON)에 쓴다. 바뀔 때만 patch한다.
- ChillServer의 전력모드 판정은 한 함수다. 최신 NodeActuation이 Ready면 그 read-back, 진행 중이면 모름, `Failed`이거나
  없으면 annotation 값과 actuator가 같은 DeviceClass 모드다. `Fixed` class는 지금처럼 class 사실이다. capture, operation
  capture, serving source 확인이 이 함수를 쓴다.
- serving instance의 native나 profile 상태를 읽지 못하면 그 노드에 사유 `InstanceUnavailable`을 붙인다. 그 노드는 배치와
  전력 변경에서 빠지고 다른 노드의 판단은 계속된다.

## 3. NodeActuator에 남는 것

- apply, 독립 read-back, settle, Lease, phase(`internal/nodeactuator/lifecycle.go`), nvpmodel adapter, boot ordering,
  NodeActuation CRD. ADR 0015(유일한 노드 전력 mutator)와 ADR 0017(read-back으로 도달 확인)은 그대로다.
- M2가 바꾸는 것은 바이너리 위치(`node-agent --actuate`), 노드 선택(DeviceClass), toleration이다.
- NodeActuation CRD와 phase 기계는 M5에서 노드 의도 `spec.powerMode`로 바뀐다. 그때 2.5절의 annotation이 노드 상태가 된다.

## 4. M3 준비

- 노드 상태 writer는 node agent(바이너리 하나, DaemonSet과 ServiceAccount 둘)뿐이다. Node metadata의 discovery key와
  `edge.dacs.io/cpus`는 `chill-node-agent`가, NodeActuation status는 `chill-node-agent-actuation`이 쓴다. M3의 taint와
  grant는 ChillServer가 쓰고 field가 겹치지 않는다. M3가 Server-Side Apply field manager로 이 분담을 강제할 때 agent의
  merge patch를 apply로 바꾼다.
- 전력 요청을 만드는 곳이 ChillServer 한 곳(동작 시각)이다. M3는 여기에 grant generation을 싣고 actuator는 오래된
  generation을 거절한다.
- agent가 측정 taint를 tolerate하므로 M3 writer가 어느 노드에 taint를 붙여도 actuator가 남는다.
- inventory가 Tick snapshot에 live로 들어오므로 M3의 "노드가 비었는가" 확인과 배치 판단이 같은 snapshot을 읽는다.

## 5. 저장 계약 (ADR 0019)

| 대상 | 변경 | 처리 |
|---|---|---|
| scheduler ConfigMap JSON | `cpuInventory`, `gpuInventory`, `meters`, `powerRequests` 삭제. decoder는 모르는 필드를 거절 | 배포 때 새 sample로 한 번 다시 쓴다. 저장 evidence가 아니다 |
| 판단 기록, snapshot, action 기록 | 형태 그대로(`SchedulerNode.cpus`, `gpuUUIDs`, `reserved`, `Operation.actuation`) | version 유지 |
| serving-operation 측정 `v1alpha2`, transition-cost evidence | 그대로. meter ID와 source 값이 같다 | 기존 비용 근거 유지 |
| Profiler metric-sources | `meterID` 추가, `v1alpha5`에서 `v1alpha6`으로 | 설정이다. Run evidence의 `SourceIdentity`에는 `meterID`가 들어가지 않아 기존 profile이 그대로다 |
| Node annotation `edge.dacs.io/cpus` | 새 key | 저장 evidence가 아니다 |
| NodeActuation CRD | 그대로 | - |

## 6. 지우는 것

- `schedulerConfiguration`의 네 필드, `validateInventory`의 inventory 부분, `validatePower`의 meter 부분,
  `SchedulerControllerConfig.PowerRequests`, `config/samples/serving-scheduler.json`의 네 필드,
  [설정과 관측 API](../serving/scheduler-api.md) 설정 표의 해당 행
- `checkProfileInventory`
- `cmd/node-discovery`, `cmd/node-actuator`, `internal/operator/nodediscovery`, `internal/operator/nodeactuator`(nodeagent로 합침),
  chart `node_discovery_*`, `node_actuator_*` template과 values `nodeDiscovery`, `nodeActuator`(`nodeAgent` 하나로),
  Makefile의 이미지 target 둘(하나로), label `edge.dacs.io/node-actuation`
- [워크로드별 POOL 7장](../serving/workload-pools.md#7-저장-계약) merge 절차의 "inventory, meter, powerRequests를 다시 넣는다"

## 7. 커밋 단위

| # | commit | 내용 | 선행 |
|---|---|---|---|
| 1 | `fix(operator): node daemonsets tolerate the measurement taint` | 완료(`958c1a8`) | - |
| 2 | `fix(serving): reproduce measured supplemental groups on serving pods` | 완료(`4cf8f38`) | - |
| 3 | `feat(serving): derive GPU inventory from measured profiles` | capture에서 profile GPU UUID 수집, `gpuInventory`와 `checkProfileInventory` GPU 부분 삭제 | - |
| 4 | `feat(node-discovery): publish online CPUs` | `nodeprobe`가 online CPU를 읽어 annotation, 10초 주기, 바뀔 때만 patch | - |
| 5 | `feat(serving): read CPU inventory from nodes` | capture가 annotation을 읽음, `cpuInventory`와 `checkProfileInventory` 삭제, 예약 검사를 capture로 | 4 |
| 6 | `feat(serving): request power modes from device classes and policies` | ChillServer가 Profiler facts를 읽음, 요청 가능 모드 도출, 동작 시각에 NodeActuation 생성, `powerRequests` 삭제 | - |
| 7 | `feat(serving): derive wall meters from metric sources` | `metricSources.meterID`, schema `v1alpha6`, values, `meters` 삭제 | 6 |
| 8 | `refactor(node-agent): merge node discovery and actuation` | 완료. `cmd/node-agent`(기본은 discovery, `--actuate`는 actuation), `internal/operator/nodeagent`, DeviceClass 기반 actuation 노드 선택, chart `nodeAgent`와 Makefile 이미지 target `node-agent`, 손 label 삭제. `.go-arch-lint.yml`의 `entrypoints`와 `operator` component가 새 package를 덮는다 | 4 |
| 9 | `feat(node-agent): publish the read-back power actuator` | 2.5절. actuation agent annotation, 전력모드 판정 함수 하나, `InstanceUnavailable` 노드 사유 | 8 |

배포는 두 번이다. 3-7은 scheduler ConfigMap을 한 번 다시 쓰는 배포 하나다. 8-9는 그 뒤 따로 배포한다. 각 commit은 자기가
바꾼 문서를 같이 고친다.

## 8. 검사

| commit | 검사 |
|---|---|
| 1 | DaemonSet 생성 test가 두 DaemonSet의 `chill.dacs.io/measurement` toleration을 확인 |
| 2 | `profiles_test`: group 있는 CUDA profile이 준비됨, privileged non-RKNN은 거절. operations test: Pod group이 측정 group과 같음 |
| 3 | capture test: 노드 GPU 목록이 그 노드 profile의 UUID, 다른 Node UID profile은 제외, allocatable보다 많으면 `PlacementInventoryMissing` |
| 4 | `nodeprobe` test: fixture `online`(`0-3,6`)이 annotation 값으로, 파일 없음은 annotation 없음 |
| 5 | capture test: annotation 없음, 잘못된 표기, 예약 CPU가 밖에 있음이 각각 `PlacementInventoryMissing` |
| 6 | capture test: policy 있는 비고정 모드만 요청 가능, `Fixed` 노드는 `PowerModeUnavailable`. 같은 모드로 두 번 전환하면 서로 다른 이름의 NodeActuation 두 개, deadline이 동작 시각 기준 |
| 7 | 도출 meter가 snapshot의 10개 선언과 같은 값(fixture), 기존 operation 측정 fixture가 `MeterUndeclared` 없이 비용으로 묶임 |
| 8 | operator test: actuation DaemonSet nodeAffinity가 DeviceClass `nodeSelector`에서 나옴, 권한이 지금 NodeActuator와 같음. `make arch-check`, `make helm-lint helm-template`(default와 `values-profiler.yaml`), `make deployment-graph-check` |
| 9 | `Failed` 최신 NodeActuation과 annotation MAXN이면 모드 MAXN, 진행 중이면 모름. native 상태 없는 instance는 그 노드만 `InstanceUnavailable`이고 판단은 `Pending`이 아님 |

## 9. 실클러스터 확인

### 9.1 E1 기준값 `[E1 결과 대기]`

E1이 끝나면 M2 전 기준값으로 채운다.

| 값 | 방법 | 값 |
|---|---|---|
| 노드 사유별 노드 수(`PowerModeUnavailable`, `PlacementInventoryMissing`) | `GET /api/v1/scheduler/state` snapshot | `[E1 결과 대기]` |
| `PreparationUnavailable` 수와 사유 분류(inventory, runtimeAccess, 기타) | `GET /api/v1/scheduler/profiles` | `[E1 결과 대기]` |
| 관측 PVC 시간당 증가 | kubelet volume stats(M0 설계 5장). M2는 PVC에 쓰지 않으므로 M2 뒤 같은 값이어야 한다 | `[E1 결과 대기]` |

### 9.2 M2 배포 뒤 (E2 전)

| # | 확인 | 통과 기준 |
|---|---|---|
| N1 | 모든 노드의 `edge.dacs.io/cpus` | 10개 노드 모두 있고 snapshot의 손 inventory와 같음 |
| N2 | scheduler state 노드 사유 | policy가 있는 Jetson은 `PowerModeUnavailable` 없음, `PlacementInventoryMissing` 노드 0 |
| N3 | scheduler profiles | inventory와 runtimeAccess 사유의 `PreparationUnavailable` 0, Jetson CUDA profile 준비됨 |
| N4 | E2의 전력 변경을 같은 모드로 두 번 | 매번 새 NodeActuation, 둘 다 독립 read-back으로 Ready |
| N5 | 모든 노드에 측정 taint가 있는 상태에서 agent DaemonSet rollout | 두 DaemonSet의 desired가 대상 노드 수, Pod가 모두 다시 Ready |
| N6 | 비용 근거 | `MeterUndeclared` 0, 배포 전과 같은 비용 근거 수 |

## 10. 범위 밖

- agent `/metrics`(전력, inventory). M1의 TSDB가 생기고 소비자가 생기면 더한다.
- 모드별 online CPU 모델. 후보가 CPU를 끄는 모드로 바꿀 때 그 모드의 CPU 집합을 알아야 하면 M4에서 profile 조건으로 다룬다.
- metric-observer, workload-observer 이미지를 agent 이미지에 합치는 것은 M5 이미지 정리에서 한다.

## 11. 결정

- DaemonSet은 권한 경계로 둘이다(2.3절).
- CPU inventory는 `online`이다(전력모드가 끈 core 제외).
- GPU inventory는 측정 profile에서 온다. 측정하지 않은 GPU는 후보에 나오지 않는다.
- meter와 전력 요청은 Profiler facts에서 온다. Profiler를 끈 default Helm profile에는 meter와 전력 요청이 없다.
- agent는 discovery, CPU inventory, 전력 적용과 read-back만 맡는다. 전력 관측과 census는 Run Job과 ChillServer에
  둔다(2.4절).
