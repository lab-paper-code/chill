# Node agent와 inventory 자동화

노드별 scheduler 설정을 노드와 측정 근거에서 도출하고, 노드 component의 바이너리·이미지·operator 설정을 하나로 둔다.
[이행 결정](../architecture-rethink/README.md)의 M2와 M5 D2를 반영한 현행 계약이다. NodeActuation CRD·actuation Lease·phase 기계는 삭제되었다.
현재 구현 근거는 `internal/nodeactuator/controller/power_mode.go`의 `PowerModeReconciler`와 `internal/metadata/power_mode.go`다.

## 1. 결정과 근거

- CPU inventory는 `online`이다. `capacity.cpu`의 `0..n-1`은 비연속 ID를 놓치고, `present`는 전력모드가 끈 core를 배치 후보로 낸다.
- GPU inventory는 같은 Node UID에서 검증한 profile의 UUID다. `/proc/driver/nvidia/gpus/*/information`은 Jetson에 없고, agent에 NVML/libcuda를 넣으면 cgo나 host driver mount가 필요하다. 측정하지 않은 GPU는 후보에 나오지 않는다.
- meter 연결은 노드가 발견할 수 없는 사실이므로 Profiler facts에 한 번 선언한다. meter ID를 노드 이름으로 바꾸면 기존 operation 측정이 `MeterUndeclared`가 되어 비용을 다시 재야 한다.
- 전력 요청은 DeviceClass와 policy에서 도출하고 요청 시각에 기록한다. 고정 이름·deadline의 요청 template은 반복 전환을 적용 없이 성공시키거나 기한 뒤 모든 전환을 실패시킨다.
- DaemonSet은 권한 경계로 둘이다. 하나로 합치면 control-plane과 서버에도 `SYS_BOOT`·쓰기 `/sys`가 생기고, Jetson 전용 nvpmodel mount는 다른 노드의 Pod 생성을 막는다.
- Node label 요청과 generation은 [NVIDIA mig-manager](https://github.com/NVIDIA/mig-parted)의 방식이다(D2). 전환마다 CRD object를 만들지 않는다. M5 근거 snapshot의 NodeActuation 69개 중 57개는 owner가 없었고, object의 적용 시각 비교는 수동 변경·재부팅을 놓쳤다.
- agent는 유일한 노드 전력 mutator([ADR 0015](../../adr/0015-node-power-actuation-boundary.md))이며, 도달 판정은 독립 read-back이다([ADR 0017](../../adr/0017-power-mode-reachability-boundary.md)). 시각 순서는 [ADR 0020](../../adr/0020-transition-duration-measurement-boundary.md)을 따른다.

## 2. Inventory와 agent

### 2.1 필드별 출처

| scheduler 사실 | 출처와 실패 규칙 |
|---|---|
| CPU | agent가 `/sys/devices/system/cpu/online`을 읽어 Node annotation `edge.dacs.io/cpus`에 kernel cpuset 표기(예: `0-11`)로 쓴다. capture가 기존 의존성 `k8s.io/utils/cpuset`으로 읽어 `SchedulerNode.CPUs`로 쓴다. 없거나 읽을 수 없거나 비어 있으면 `PlacementInventoryMissing` |
| GPU | ChillServer가 같은 Node UID의 검증된 profile `RuntimeMetadata.GPUUUID`를 모은다. allocatable `nvidia.com/gpu` 수 검사도 유지한다 |
| 요청 가능 전력모드 | Tick capture가 DeviceClass 모드 중 actuator가 `Fixed`가 아니고 `powerActuation.policies`에 policy가 있는 모드를 고른다. refresh 때만 도출하면 Node·DeviceClass 변화를 놓친다. 요청은 3장 |
| wall meter | Profiler `metricSources`마다 `{id: meterID, node: live Node 이름과 UID, scope: node-exclusive-wall, source: 항목의 전력 부분}`을 만든다. meter ID와 source를 유지하므로 기존 비용 근거가 그대로 묶인다 |
| `reserved` | 운영자 정책으로 설정에 남는다. 발견한 inventory 밖의 예약은 `PlacementInventoryMissing` |

- ChillServer는 `--profiler-facts-configmap`의 `metricSources`와 actuation policy를 Profiler와 같은 decoder로 읽고, resourceVersion을 refresh signature에 넣는다. policy 형식 때문에 serving-kube는 `node-actuator-core`에 의존한다(`.go-arch-lint.yml`).
- Profiler를 끈 default Helm profile에는 meter와 전력 요청이 없다.
- CPU 파일은 10초마다 읽고 바뀔 때만 patch한다. 전력 전환 뒤 settle 60초 안에 새 값이 올라온다.
- inventory 검사는 profile refresh의 `checkProfileInventory`가 아니라 Tick live snapshot의 `ExclusiveCPUInfeasible`, `ExclusiveGPUInfeasible` 검사에서 한다.

### 2.2 runtimeAccess

serving Pod는 profile의 측정 `runtimeAccess.supplementalGroups`를 Pod `securityContext.supplementalGroups`로 재현한다(`4cf8f38`).
privileged는 RKNN host delivery에서만 온다. group은 `hack/runtime-declarations.py`가 측정 Job과 같은 신원으로 만든 선언에 고정되어 있고 `samePreparedPod`가 재현 여부를 비교한다.
agent가 장치 파일 GID를 따로 발견하면 측정 조건과 다른 두 번째 출처가 생기므로 하지 않는다.

### 2.3 agent 구성

바이너리 `cmd/node-agent`, 이미지, operator 설정과 reconciler(`internal/operator/nodeagent`)는 각각 하나다.
actuation은 chart `nodeAgent.actuation.enabled`로 켜며 ChillSystem `spec.nodeAgent.actuation`에 전달한다.

| DaemonSet / ServiceAccount | 노드 | 역할·권한 |
|---|---|---|
| `chill-node-agent` | 모든 노드 | hardware signature discovery, `edge.dacs.io/cpus`. 비root, 읽기 전용 `/proc`·`/sys`·`/etc`, Node `get`·`patch` |
| `chill-node-agent-actuation` | policy가 있는 DeviceClass의 `nodeSelector`에 맞는 노드 | `--actuate`, 전력 적용·독립 read-back. hostPID, `SYS_BOOT`, 쓰기 `/sys`, nvpmodel mount, Node `get`·`patch` |

- operator는 두 DaemonSet에 `chill.dacs.io/measurement` toleration을 고정한다(`958c1a8`). DeviceClass를 watch하고 `nodeSelector`를 actuation nodeAffinity term으로 옮긴다. 수동 `edge.dacs.io/node-actuation` label은 쓰지 않는다.
- discovery key와 CPU annotation은 discovery agent가, 전력 관측·상태는 actuation agent가 쓴다. 측정 taint와 grant는 ChillServer가 쓰며 field가 겹치지 않는다.
- 전력 관측(metric-observer), census(workload-observer 및 ChillServer `captureNodePods`)는 agent에 넣지 않는다. Shelly gateway `155.230.35.213:18766`·monitoring namespace의 `edge-metrics-exporter`를 HTTP로 읽거나 Pod LIST/WATCH를 하므로 노드 접근이 필요 없고, 결과는 Run evidence 계약이다.
- metric-observer·workload-observer 이미지는 각각 protocol identity에 있으므로 agent 이미지로 합치지 않는다. 해결되는 문제가 없고 측정 중 이동은 ADR 0019 변경을 만든다.

## 3. 노드 전력모드: Node label과 generation

### 3.1 Node metadata

| key | writer | 값 |
|---|---|---|
| label `chill.dacs.io/power-mode` | 3.3절의 writer 하나 | DeviceClass 모드 이름. 없으면 원하는 모드 없음 |
| annotation `chill.dacs.io/power-mode-requested-at` | 요청 label과 같은 writer·patch | RFC 3339 요청 시각(writer 시계), 기한의 기준 |
| label `chill.dacs.io/power-mode.state` | agent | `pending`, `success`, `failed` |
| annotation `chill.dacs.io/power-mode-generation` | agent | 0부터 시작, 관측한 모드 변경마다 +1 |
| annotation `edge.dacs.io/power-actuator` | agent | `PowerModeActuator` JSON 독립 read-back |
| annotation `chill.dacs.io/power-mode-boot` | agent | 마지막으로 본 `status.nodeInfo.bootID` |
| annotation `chill.dacs.io/power-mode-transition` | agent | `{powerMode, requestedAt, generation, applyStartedAt, verifiedAt, settlingStartedAt, readyAt, failure}`(Node 시계). `requestedAt`이 처리한 요청을 식별하며 state는 현재 요청의 기록에만 붙는다 |

### 3.2 적용·관측·기한

- agent는 자기 Node를 watch한다. 원하는 모드가 read-back과 다르고 `failed`가 아니면 `pending`·`applyStartedAt`을 먼저 patch한다(ADR 0020 변경 전 fence). 기존 adapter로 적용하고 독립 read-back이 목표이면 read-back·generation +1·`verifiedAt`을 한 patch로 쓴다. policy `settlingDurationSeconds` 뒤 `readyAt`·`success`를 쓴다. 실패는 `failed`·`failure`다.
- DeviceClass·모드·policy가 없으면 `failed`, DeviceClass를 읽는 다른 오류는 재시도한다. 이미 목표 모드이면 적용 없이 `success`와 현재 read-back의 `verifiedAt`·`readyAt`을 쓴다. label 삭제 시 state를 지운다.
- 매 pass의 시각은 host read 뒤에 읽어 `applyStartedAt` → `verifiedAt` → `settlingStartedAt` → `readyAt` 순서를 지킨다.
- 10초 read-back에서 actuator annotation이나 bootID가 달라지면 read-back·bootID·generation +1을 한 patch로 쓴다. 수동 모드 변경, 재부팅, 적용 중 멈춤을 센다. 메모리가 아니라 Node annotation과 비교하므로 agent 재시작은 세지 않는다. 10초 안에 바뀌었다 돌아온 모드는 보지 못한다.
- host read 실패 시 read-back은 유지하고 bootID만 비교한다. bootID가 바뀌었으면 한 번 세고 낡은 read-back을 지운다. 단, 아래 재부팅 요청의 `pending`은 먼저 목표 판독을 기다린다.
- NVIDIA는 요청 label이 없으면 `fixed`다. 이때 동적 graphics/SM MHz 변화는 read-back만 갱신하고 generation을 올리지 않는다. 요청한 잠금 모드의 read-back 변경과 bootID 변경은 센다.
- agent는 손으로 바뀐 모드를 원하는 모드로 되돌리지 않는다(Q1). generation과 실제 read-back만 갱신한다. 되돌려도 측정 무효 판정은 같고 변경만 두 번 생긴다.
- writer는 같은 모드라도 `failed`이거나 `success` 기록의 generation이 현재와 다르면 새 요청 시각을 쓴다. agent는 새 요청으로 처리해 적용하거나 이미 목표면 현재 generation으로 `success`를 쓴다. BEO·MLIB는 host가 떠난 `success`를 재요청하고 `failed`는 전환 실패로 닫는다.
- **재부팅 모드:** orin-nano 7W 진입·이탈(`values.yaml:280-283`, nvpmodel `Reboot required`)은 fence 뒤 `--force`로 기록하고 Node를 재시작한다(`jetson/nvpmodel.go:159-170`). Node의 label·`pending`은 남는다. 새 agent가 목표 read-back과 바뀐 bootID를 함께 확인해 generation을 한 번 올리고 verified·settling·success를 쓴다.
- 재부팅 뒤 host를 읽지 못하면 `pending`을 유지하고 Node에 쓰지 않는다. 다음 성공 read에서 bootID와 read-back을 함께 한 번 센다. `applyStartedAt + maxActuationDurationSeconds`까지 못 읽으면 bootID를 세고 `failed`다. 이름은 목표여도 gating mask가 다르면 읽기 오류이므로 같은 기한에 실패한다. `holdsTarget`은 반복 재시작을 막는다.
- **기한:** agent는 위 재부팅 뒤 읽기 실패 `pending`에만 기한을 쓴다. writer는 요청 시각부터 policy `maxActuationDurationSeconds`(live 900 s, 재부팅 약 75 s 포함) 안에 `success`와 목표 read-back을 못 보면 실패한다(계획기 action `failed`, Profiler는 deadline 실패). 적용을 시작하지 않은 노드도 같다. 늦은 적용도 generation을 올려 겹친 측정을 무효화한다. 재시도는 같은 label에 새 요청 시각을 쓴다.
- capture와 serving source 확인은 실제 read-back으로 모드를 판정하며 `Fixed` class는 class 사실을 쓴다. instance의 native/profile 상태가 없으면 그 노드만 `InstanceUnavailable`로 배치·전력 변경에서 빼고 다른 노드의 판단을 계속한다.

### 3.3 요청 writer와 admission

M3 grant가 한 시점에 노드별 writer 하나를 정한다. 읽은 Node resourceVersion으로 optimistic lock patch하므로 grant가 바뀌면 conflict다.

| 동작 | writer | 조건 |
|---|---|---|
| 계획기 power | ChillServer | taint 없는 노드, `tickMu` 안 |
| 반환의 최저 모드 낮춤 | ChillServer | 자기가 반환하는 노드 |
| opcost `power`, occupancy 모드 단계 | ChillServer | 측정 taint가 해당 루트를 가리킴 |
| BEO 전환, ProfilingRun 확인, MLIB, PMTC 시도 | Profiler | 측정 taint가 자기 루트를 가리킴 |

- admission은 `pending`이고 요청 시각 + `maxActuationDurationSeconds` 전인 노드를 승인하지 않는다. 반환 대기도 같은 기한이다(`internal/serving/kube/admission.go` `powerModePending`).
- operator stop safety(`internal/operator/nodeagent/stop_safety.go`)는 `pending`인 노드에서 agent를 멈추지 않는다.
- 요청·완료 판정의 공통 계약은 `internal/metadata/power_mode.go`의 `RequestPowerMode`, `ServesRequest`, `PowerModeLeft`, `PowerModeDeadline`, `PowerModeOutcome`이며 serving 경로는 `internal/serving/kube/operations.go`의 `ChangePower`·`requestPowerMode`다.

### 3.4 측정 무효 판정

각 기록은 구간 시작·끝 generation을 `powerModeGeneration{start, end}`로 남긴다. 기대 차이는 구간 내 자기 전력 변경 수다.

| 기록 | 구간 | 기대 차이 |
|---|---|---|
| ProfilingRun 실행 evidence(BEO·MLIB Run 포함) | 측정 container 시작~끝 | 0 |
| opcost `prepare`·`idle`·`remove`, occupancy 부하 창 | 단계 구간 | 0 |
| opcost `power`, PMTC 시도 | 전환 구간 | 모드가 다르면 1, 같으면 0 |

다른 차이는 `PowerChangedDuringMeasurement`다. 루트 반환의 `foreignActuations` 비교는 없다. agent가 없는 `Fixed` 노드는 generation과 기록을 비운다.

## 4. 저장 계약 (ADR 0019)

| 대상 | 현행 계약과 호환 근거 |
|---|---|
| scheduler ConfigMap | `cpuInventory`, `gpuInventory`, `meters`, `powerRequests` 없음. decoder는 모르는 필드를 거절한다. 설정이며 저장 evidence가 아니다 |
| 판단·snapshot·action | `SchedulerNode.cpus`, `gpuUUIDs`, `reserved`, `Operation.actuation` 형태·version 유지 |
| Profiler metric-sources | `meterID` 추가로 `v1alpha5` → `v1alpha6`. Run evidence `SourceIdentity`에는 `meterID`가 없어 기존 profile 유지 |
| serving-operation `v1alpha2`, transition-cost | meter ID·source 유지로 기존 비용 근거 사용 |
| ProfilingRun 실행 evidence `v1alpha15` `actuation` | `generation{start, end}` 선택 필드. `handoff`도 선택 필드로 남기고 새 기록은 비운다. strict decoder가 기존 evidence를 읽도록 version 유지; loader의 `handoff`↔`status.actuation` 대조와 ProfilingRun `status.actuation`은 없음 |
| PMTC raw `v1alpha6` | marker는 `power-mode-transition`에서 오며 필드는 같고 `generation`은 선택 필드. P7b 기록 유지 |
| operation `v1alpha2`, occupancy `v1alpha1` | `isolation.powerModeGeneration` 선택 필드. reader는 저장된 verdict 사용 |
| Node metadata | CPU·전력 key는 저장 evidence가 아님. NodeActuation CRD·object·참조 reader 없음 |

ADR 0020의 fence·read-back·settling 시작·Ready 네 시각은 transition annotation에서 온다. ADR 0015·0017의 결정은 유지한다.
`handoff`를 버전 올려 지우면 해당 evidence의 모든 profile을 P7b 뒤 다시 재야 하므로 선택 필드로 유지한다(Q2).

## 5. 인스턴스 공유 경계

- 공유는 연구 범위 밖이다. `e018057`(M5 commit 4)은 `edge.dacs.io/serving-routing` annotation을 필수로 만들고 Workload를 0개나 1개만 허용한다. 둘 이상이면 source invalid다. Runtime은 annotation의 분배만 쓴다.
- 이 경계 뒤의 `SharedInstance`·`InstanceShared` 검사는 삭제했다. capture는 Workload를 그대로 기록하고 판단은 공유 사유 없이 진행한다. 수동 remove는 다른 Workload가 소유하거나 binding한 instance를 거절한다.
- `staticRouting` 다중 UID 거절은 유지한다. Runtime의 UID map·`InstanceState.Workloads[]`·UID별 `Waiting`/`Running`도 유지한다. key는 하나 이하이며 줄이면 `runtime.go` 1,058줄의 분배·pools·action 기록 형식을 다시 써야 한다. Dashboard는 서비스 이름 하나를 보인다.
- ADR 0019: Pod annotation을 읽는 규칙과 판단 상태 값 하나만 바뀌고 저장 필드는 같다. 저장된 `SharedInstance` 상태·사유 문자열은 계속 읽으며 Dashboard는 별도 처리하지 않는다.
- 배포 전 기존 image에서 `staticRouting`을 적용해 모든 instance Pod의 annotation을 다시 쓴 뒤, live Pod마다 annotation이 있고 Workload가 하나 이하인지 확인하는 전제는 유지한다. 사용법은 [정적 배치 운영](../serving/scheduler-operations.md#정적-배치-사용법)에 둔다.

## 6. M5의 유지 결정과 미완료 분리

### 6.1 측정 kind와 수동 동작

- D1: BEO, MLIB, 독립 ProfilingRun, PMTC, opcost, occupancy 여섯 kind를 유지한다. 승인·대기·반환은 admission 루트 표 하나, 단계 측정은 `Measure`·witness 하나다.
- 근거 snapshot은 BEO 19, MLIB 7, ProfilingRun 192, PMTC 0이다. profile ID는 BEO UID, profile 유휴 전력은 MLIB 결과에 묶인다. 합치면 기존 object를 지우고 hetero 계획 기준 약 27 h 재측정이 필요하다(`[추론]`).
- opcost `power`는 빈 노드 전환, PMTC는 인스턴스가 있는 전환이다. 계획기는 현재 적재 상태로 비용을 찾는다. MLIB는 profiling Job의 적재 유휴, opcost `idle`은 계획기의 노드 기준선이다.
- `DeviceProfile`·`ClusterEnergyModel`은 `api/` 밖 Go reader/writer와 live object(0개)가 없어 삭제 대상이었다. Workload, ModelSpec, Dataset, Preprocessor, DeviceClass, ChillSystem CRD는 유지한다.
- D3: 수동 prepare·remove·`staticRouting`은 정적 구성 비교와 고정 배치 기준선이다(`SCH-32`, `SCH-AC-20`, `SCH-33`, `SCH-35`, `DB-FR-33`). 측정 전용 `serving-operation --measurement-config`, `POST /api/v1/measurements/operations`, 손 d 측정·`dispatchOccupancy` 설정의 제거는 [측정 API](../serving/measurement-api.md)가 맡는다.

### 6.2 worker image 분리(D4, 미완료)

`build/docker/firstfloor.Dockerfile:9-14,71-74`는 아직 Go 바이너리를 runtime image에 넣는다. 아래는 worker 수정 직전에 이행할 결정이며 완료된 계약이 아니다.

- pools 배포는 `serving-worker` 요청 형식 변경으로 BEO 18개의 profile을 다시 쟀다. runtime image가 profile 실행 상태 identity에 들어가고 instance도 그 image로 뜨기 때문이다. 클러스터 v1.31.13의 ImageVolume은 alpha라 꺼져 있다.
- `chill-worker`는 busybox base, `/opt/chill/bin`의 `serving-worker`·`measurement-worker`, multi-arch `docker-build-worker`다. instance Pod·ProfilingRun 실행 Job의 `chill-bin` init container가 emptyDir에 복사하고 runtime container가 `/opt/chill/bin`에 mount한다. Job 명령도 기존 `/measurement-worker`에서 `/opt/chill/bin/measurement-worker`로 바꾼다.
- `--materialize-profile` init container는 mount 없이 runtime image의 materializer를 쓴다. serving source 확인은 `chill-bin`과 그 image를 허용한다. mount가 기존 바이너리를 가리므로 `pools-ed628c8-*` runtime image와 profile identity를 유지할 수 있다.
- `firstfloor.Dockerfile`의 Go builder를 제거하고 runtime은 Python·ORT·`runtime/` runner가 바뀔 때만 빌드한다. Helm `worker.image` 하나를 ChillServer·Profiler에 준다.
- worker는 metric/workload observer image와 함께 측정 protocol identity에 들어간다. 새 Run은 새 protocol, 기존 profile은 유지한다. occupancy는 `workerImage`를 기록만 하며 d 재측정 여부는 운영자가 정한다(D4).
- ADR 0019: ProfilingRun 고정 설정 `images.worker`, occupancy `workerImage`는 선택 필드이고 identity에서 `omitempty`; protocol identity schema version은 유지한다.
- 측정 캠페인이 없을 때, `serving-worker`·`measurement-worker` 수정 **앞에** 분리를 배포한다. overlay runtime 선언 digest를 유지하면 기존 profile·d를 다시 재지 않는다. 분리 뒤 worker 수정 시 기존 d는 이전 worker의 값이므로 재측정 여부를 다시 정한다.

### 6.3 관측 잠금과 S3(D5)

- `bca2da3`(D-R7-5): `AcquireObservationOwnership`은 blocking `flock(LOCK_EX)`으로 이전 Pod의 관측 잠금을 기다린다. `Recreate`는 rollout에만 적용되고 Pod 삭제 시 이전 Pod가 grace 150 s 동안 drain하므로 즉시 재시작하면 잠금 충돌로 약 14 s 더 멈췄다(D-R6-2도 동일).
- 대기 중 SIGTERM은 SIGKILL에서 처리되고, 정상 대기는 이전 Pod 종료(grace 150 s)에 끝난다. Node 장애는 CephFS MDS session timeout까지 갈 수 있다. `waiting for observation lock`, `observation lock acquired`(대기 시간 포함)만 남기며 즉시 획득하면 log가 없다. ChillServer에는 probe가 없어 잠금을 얻기 전에도 Pod가 산다.
- `bfb4563`(D-R6-7): process마다 모든 stream을 올린 지난 시간 묶음을 기억해 다시 올리지 않는다. 한 stream이라도 실패하면 다음 archive에서 그 시간의 모든 stream을 다시 올리고 기존 stream은 `Existing`이다. key·형식은 그대로이며 재시작 뒤 첫 올리기도 `Existing`이다.
- archive 주기는 1분이며 지난 시간은 불변이다(쓰기는 현재 시간 이후 선택). 종전 PutObject 3회 재시도 오류는 이미 있던 `serving/observations.v1alpha5/<시간>/{requests,scheduler}.jsonl.gz`의 재업로드여서 유실은 없었다. `If-None-Match: *` 요청에 store가 본문 전송 중 412로 연결을 닫는 것이 reset 원인이라는 판단은 `[추론]`이다.

## 7. 미해결 확인과 범위 밖

- E1 기준값은 원문에서 `[E1 결과 대기]`였다: scheduler state의 `PowerModeUnavailable`·`PlacementInventoryMissing` 노드 수, profiles의 `PreparationUnavailable` 수/사유(inventory·runtimeAccess·기타), 관측 PVC 시간당 증가. M2는 PVC에 쓰지 않으므로 증가량이 같아야 한다. 완료 근거가 없어 미해결로 남긴다.
- M2 배포 확인 N1–N6의 결과도 원문에 없었다: N1 당시 10개 노드의 CPU annotation과 손 inventory 일치, N2 policy 있는 Jetson의 `PowerModeUnavailable` 없음·`PlacementInventoryMissing` 0, N3 inventory/runtimeAccess 원인 `PreparationUnavailable` 0·Jetson CUDA 준비, N4 같은 모드 반복 전환의 독립 read-back, N5 측정 taint 아래 두 DaemonSet desired/Ready 일치, N6 `MeterUndeclared` 0·기존 비용 근거 수 유지. N4의 NodeActuation 생성 방식은 3장으로 대체되었다.
- agent `/metrics`(전력·inventory)는 TSDB와 소비자가 필요할 때 추가한다. 모드별 online CPU 집합이 필요하면 profile 조건에서 다룬다.
- agent merge patch를 Server-Side Apply field manager로 바꾸는 M3 준비 항목은 원문에 완료 근거가 없다.
- 다중 Workload 계획 탐색 시간·게이트웨이 간섭, 같은 노드 instance 간섭, 전환 중 SLO의 재검토는 E5 범위이며 이 문서가 해결하지 않는다.
