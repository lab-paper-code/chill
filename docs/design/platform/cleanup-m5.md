# M5 정리

[05 이행 계획](../architecture-rethink/05-recommendation-migration.md)의 M5와 그동안 쌓인 정리 후보를 항목마다 근거, 판정, [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md) 영향, commit으로 정한다. 판정은 사용자 결정(D1-D5)이다. 코드 근거는 `bfb4563`과 [측정 API](../serving/measurement-api.md) 설계, live 근거는 2026-10-21 읽기 전용 snapshot이다. 실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 1. 요약

| # | 항목 | 판정 | ADR 0019 | commit |
|---|---|---|---|---|
| 1 | 측정 kind 여섯(BEO, MLIB, ProfilingRun, PMTC, opcost, occupancy) | 각자 유지(D1) | 없음 | - |
| 2 | `DeviceProfile`, `ClusterEnergyModel` | 삭제(D1) | object 0개 | 3 |
| 3 | NodeActuation CRD | Node label과 generation으로 교체(D2) | 기록에 선택 필드, NodeActuation object 삭제 | 5-7 |
| 4 | Go 바이너리와 runtime image | worker image로 분리, 기존 runtime image 유지(D4) | 기록에 선택 필드 | 8 |
| 5 | routing annotation 없음 fallback, `SharedInstance`, `InstanceShared` | 삭제 | Pod annotation만 바뀐다 | 4 |
| 6 | Runtime 다중 UID binding, Dashboard 서비스 표시 | 유지 | 없음 | - |
| 7 | 수동 prepare와 remove, `staticRouting` | 유지(D3). 측정 전용 경로는 측정 API가 지운다 | 없음 | 측정 API 5, 8 |
| 8 | 재시작 관측 잠금 CrashLoop(D-R7-5) | 잠금을 기다림(D5) | 없음 | 1 |
| 9 | S3 PutObject 재설정(D-R6-7) | 지난 시간 묶음을 process마다 한 번 올림(D5) | 없음 | 2 |

## 2. 측정 kind

- 측정 루트는 BEO, MLIB, 독립 ProfilingRun, PMTC이고 측정 API가 opcost와 occupancy를 더한다. 승인, 대기, 반환은 이미 한 코드다: 루트 표(`internal/serving/kube/admission.go:46-94`)가 kind마다 노드와 terminal 여부만 뽑는다. 단계 측정은 `Measure`와 witness 하나를 쓴다(측정 API 2장).
- live object: BEO 19, MLIB 7, ProfilingRun 192, PMTC 0. profile ID는 BEO UID를 담고(`kube/catalog.go:385`), profile의 유휴 전력은 MLIB 결과다(`:387`). kind를 합치면 이 object를 지우고 모든 profile을 다시 재야 한다(hetero 계획 기준 약 27 h `[추론]`).
- 겹쳐 보이는 쌍도 다른 양을 잰다. opcost `power`는 빈 노드 전환, PMTC는 인스턴스가 있는 전환이고 계획기의 power 동작은 그 순간의 적재 상태로 비용을 찾는다(`scheduler_plan.go:350-352`, `kube/cost.go:174`). MLIB는 profiling Job 경로의 적재 유휴로 profile의 유휴 전력이 되고, opcost `idle`은 계획기의 노드 기준선이다.

**삭제.** `DeviceProfile`, `ClusterEnergyModel`은 `api/` 밖의 Go reader와 writer가 없고 live object가 0개다. type, `config/crd/bases`와 chart의 CRD, `config/crd/kustomization.yaml:7-8`, `PROJECT:32,40`, 이 type을 owner로 쓰는 `test/integration/operator/ownerref_scope_test.go`, [ADR 0005](../../adr/0005-crd-scope.md)의 두 항목을 지운다.

## 3. 노드 전력모드: Node label과 generation

NVIDIA mig-manager([mig-parted](https://github.com/NVIDIA/mig-parted))의 방식이다. 원하는 모드는 Node label, 적용은 node agent, 결과는 상태 label이다. CRD, controller, object 정리가 없다.

**근거.** NodeActuation은 전력 변경마다 object 하나다. writer는 ChillServer 셋과 Profiler 넷(아래 표), reader는 capture(`kube/scheduler.go:508,653`), serving source 확인(`kube/observer.go:526`), 측정 무효 판정(`kube/admission.go:392,470`), PMTC 관측(`transitioncost/kube/observer.go:216`), Run 확인(`profiler/controller/state_confirmation.go`)이다. live 69개 중 57개는 owner가 없어 계속 쌓이고 capture가 Tick마다 모두 읽는다. 측정 무효 판정은 object의 `applyStartedAt`을 측정 구간과 비교하므로 손이나 재부팅으로 바뀐 모드는 보지 못한다.

| Node metadata | writer | 값 |
|---|---|---|
| label `chill.dacs.io/power-mode` | 3.2절의 writer 하나 | DeviceClass 모드 이름. 없으면 원하는 모드가 없다 |
| label `chill.dacs.io/power-mode.state` | node agent | `pending`, `success`, `failed` |
| annotation `chill.dacs.io/power-mode-generation` | node agent | 0부터 세는 정수. 관측한 모드가 바뀔 때마다 1 증가 |
| annotation `edge.dacs.io/power-actuator` | node agent | 지금처럼 actuator read-back(node-agent 2.5절) |
| annotation `chill.dacs.io/power-mode-boot` | node agent | 마지막으로 본 `status.nodeInfo.bootID` |
| annotation `chill.dacs.io/power-mode-transition` | node agent | 마지막 적용의 `{powerMode, generation, applyStartedAt, verifiedAt, settlingStartedAt, readyAt, failure}`(Node 시계) |

### 3.1 node agent

- `chill-node-agent-actuation`이 자기 Node를 watch한다. 원하는 모드가 read-back과 다르고 state가 `failed`가 아니면: `pending`과 `applyStartedAt`을 patch하고(ADR 0020의 변경 전 fence), 기존 adapter로 적용하고, 독립 read-back이 목표면 read-back, generation + 1, `verifiedAt`을 한 patch로 쓴다. DeviceClass 모드의 policy(`powerActuation.policies`, agent 설정에 이미 있음) `settlingDurationSeconds`를 기다린 뒤 `readyAt`과 `success`를 쓴다. 실패는 `failed`와 `failure`다. 원하는 모드가 이미 read-back과 같으면 `success`만 쓴다. label이 지워지면 state를 지운다.
- 10초 read-back(node-agent 2.5절)에서 관측한 actuator가 annotation 값과 다르거나 bootID가 `power-mode-boot`와 다르면 두 annotation과 generation + 1을 한 patch로 쓴다. 손으로 바꾼 모드, 재부팅, 적용 중 멈춤이 모두 여기서 센다. agent 재시작은 annotation과 비교하므로 세지 않는다. 비교 기준이 Node annotation이라 agent 메모리에 의존하지 않는다. 10초 안에 바뀌었다 돌아온 모드는 보지 못한다.
- agent는 원하는 모드를 다시 적용하지 않는다. 손으로 바뀐 모드는 그대로 두고 read-back이 실제 모드다. capture는 지금처럼 read-back으로 판단한다.
- **재부팅 모드**(orin-nano 7W 진입과 이탈, `values.yaml:280-283`, nvpmodel `Reboot required`): agent가 `pending`과 `applyStartedAt`을 쓴 뒤 `--force`로 기록하고 Node를 재시작한다(`jetson/nvpmodel.go:159-170`). label은 Node object에 있어 재부팅 뒤에도 `pending`이다. 다시 뜬 agent가 read-back이 목표이고 bootID가 바뀐 것을 보고 generation을 한 번 올리고 `verifiedAt`, settling, `success`를 쓴다. 목표 이름이지만 gating mask가 다르면 `failed`다(`holdsTarget`이 반복 재시작을 막는다).
- **기한.** agent에는 기한이 없다. writer가 label을 쓴 시각부터 policy `maxActuationDurationSeconds`(live 900 s, 재부팅 약 75 s 포함) 안에 `success`와 목표 read-back을 보지 못하면 그 동작을 실패로 판정한다(계획기는 action `failed`, Profiler는 지금의 deadline 실패와 같은 결과). 늦게 끝난 적용도 generation을 올리므로 겹친 측정은 무효가 된다. 같은 모드를 다시 시도하려면 writer가 label을 지운 뒤 다시 쓴다.
- Lease와 phase 기계는 없다. 노드마다 agent 하나가 유일한 전력 mutator다(ADR 0015). 독립 read-back은 그대로다(ADR 0017).

### 3.2 desired label의 writer

writer는 M3 grant가 정한다. 한 시점에 노드마다 writer 하나다. writer는 Node를 읽은 resourceVersion으로 optimistic lock patch를 하므로 그 사이 grant가 바뀌면 conflict로 끝난다.

| 지금 NodeActuation을 만드는 곳 | label writer | 조건 |
|---|---|---|
| 계획기 power 동작(`kube/operations.go:762`) | ChillServer | taint 없는 노드, `tickMu` 안 |
| 반환의 최저 모드 낮춤(`kube/admission.go:443-465`) | ChillServer | 자기가 반환하는 노드 |
| opcost `power` 단계, occupancy 모드 단계(측정 API 2, 3장) | ChillServer | 그 루트가 grant를 가진 노드 |
| BEO 상태 전환(`campaign/controller/transition_builder.go`), ProfilingRun 확인(`profiler/controller/execution_actuation.go`, `state_confirmation.go`), MLIB(`idlebaseline/controller/transition.go`), PMTC 시도(`transitioncost/controller/attempt_materializer.go`) | Profiler | taint가 있고 admission annotation `chill.dacs.io/admission`의 grant가 자기 루트인 노드 |

- admission은 state가 `pending`이고 `applyStartedAt + maxActuationDurationSeconds`가 지나지 않은 노드를 승인하지 않는다(`pendingActuation`, `admission.go:226-236`을 대신한다).
- operator의 stop safety(`operator/nodeagent/stop_safety.go`)는 `pending`인 노드에서 agent를 멈추지 않는다.

### 3.3 측정 무효 판정

측정 기록마다 구간 시작과 끝에 Node의 generation을 읽어 `powerModeGeneration{start, end}`로 남긴다. 기대 차이는 구간 안 자기 전력 변경 수다.

| 기록 | 구간 | 기대 차이 |
|---|---|---|
| ProfilingRun 실행 evidence(BEO, MLIB의 Run 포함) | 측정 container 시작부터 끝 | 0 |
| opcost `prepare`, `idle`, `remove` 단계, occupancy 부하 창 | 단계 구간 | 0 |
| opcost `power` 단계, PMTC 시도 | 전환 구간 | 모드가 다르면 1, 같으면 0 |

다른 차이는 `PowerChangedDuringMeasurement`다. 판정은 기록마다 하므로 반환의 루트 단위 비교(`foreignActuations`)는 없다. `Fixed` 노드는 agent가 없어 generation이 없고 기록도 비운다.

### 3.4 저장 계약 (ADR 0019)

| 대상 | 변경 | 처리 |
|---|---|---|
| ProfilingRun 실행 evidence `v1alpha15` `actuation` | `generation{start, end}` 선택 필드 추가. `handoff`는 선택 필드가 되고 새 기록은 비운다. loader의 `handoff`와 `status.actuation` 대조(`evidence/kube/retained_loader.go:177-183`)와 ProfilingRun `status.actuation`을 지운다 | decode가 strict(`evidence/codec.go:33`)라 필드를 남겨야 기존 evidence가 읽힌다. 버전을 올리지 않는다. 기존 profile은 그대로 쓰인다 |
| PMTC raw `v1alpha6` | marker 출처가 `power-mode-transition` annotation, 필드 같음. `generation` 선택 필드 추가 | P7b의 PMTC 기록 그대로 |
| operation 측정 `v1alpha2`, occupancy `v1alpha1` | `isolation.powerModeGeneration` 선택 필드 추가 | reader는 저장된 verdict를 쓴다 |
| NodeActuation CRD와 object | 삭제 | object를 모두 지운 뒤 CRD를 지운다. ProfilingRun의 참조는 이름뿐이라 깨지는 reader가 없다 |
| Node label과 annotation | 새 key | 저장 evidence가 아니다 |

ADR 0020의 구간(fence, read-back, settling 시작, Ready)은 같은 네 시각이 annotation에서 온다. ADR 0015, 0017은 그대로다.

### 3.5 지우는 것과 시점

- 지우는 것: `api/v1alpha1/nodeactuation_types.go`, CRD와 chart RBAC, `internal/nodeactuator/controller`의 reconciler, lease, projection과 `lifecycle.go`의 phase, `powerRequest`, `foreignActuations`, `pendingActuation`, Profiler의 NodeActuation 생성과 목록 조회, opcost의 owner NodeActuation(측정 API 2장).
- 시점: P7b 캠페인이 끝나고 모든 측정 루트가 terminal이며 측정 taint가 없을 때 5-7을 한 번에 배포한다. 배포 뒤 Jetson agent가 generation 0과 read-back을 쓰고, 원하는 모드 label은 처음 쓰는 writer가 만든다.

## 4. Go 바이너리와 runtime image

- `build/docker/firstfloor.Dockerfile:9-14,71-74`가 `serving-worker`, materializer, `measurement-worker`를 runtime image에 넣는다. profile의 실행 상태 identity가 runtime image를 담고(`profiler/projection/identity.go:44,50`), 인스턴스는 profile의 runtime image로 뜬다(`kube/operations.go:412-413`). pools 배포에서는 `serving-worker`의 요청 형식 변경으로 BEO 18개의 profile을 다시 쟀다. 클러스터 v1.31.13의 ImageVolume은 alpha라 꺼져 있다.
- 새 image `chill-worker`: busybox base, `/opt/chill/bin`에 `serving-worker`와 `measurement-worker`, Makefile `docker-build-worker`(multi-arch). 인스턴스 Pod와 ProfilingRun 실행 Job의 init container `chill-bin`이 emptyDir로 복사하고 runtime container가 `/opt/chill/bin`에 mount한다(`execution_job_spec.go:211`의 명령은 `/opt/chill/bin/measurement-worker`). profile init container(`--materialize-profile`)는 ChillServer image로 돈다 `[추론: Python을 쓰지 않는 Go 단계]`. serving source 확인(`kube/observer.go:445,465`)은 init container 둘과 각 image를 받는다.
- mount가 image 안의 바이너리를 가리므로 live runtime image(`pools-ed628c8-*`)를 그대로 쓰고 기존 profile identity가 같다. `firstfloor.Dockerfile`은 Go builder 단계를 지우고, runtime image는 Python, ORT, `runtime/` runner가 바뀔 때만 만든다. worker image는 Helm `worker.image` 하나로 ChillServer와 Profiler에 준다.
- worker image는 측정 protocol identity에 metric observer, workload observer image와 함께 들어간다(`identity.go:64-71`). 새 run은 새 protocol이고 기존 profile은 그대로 쓰인다. occupancy 기록은 `workerImage`를 기록만 한다. worker가 바뀐 뒤 d를 다시 잴지는 운영자가 정한다(D4).
- metric observer, workload observer image는 node agent image에 합치지 않는다. 두 image가 protocol identity에 있고 합쳐서 풀리는 문제가 없다(node-agent 2.4절).
- ADR 0019: ProfilingRun 고정 설정 `images.worker`와 occupancy `workerImage`는 선택 필드다. 측정 캠페인이 없을 때 배포한다.

## 5. 인스턴스 공유 정리

[용량표 계획기](../serving/analytic-planner.md) 6장의 M5 후보다. 공유는 연구 범위 밖이고, 공유를 막는 검사는 한 경계에 하나만 둔다.

| 후보 | 근거 | 판정 |
|---|---|---|
| annotation 없음 fallback | 서빙 source가 `edge.dacs.io/serving-routing`을 선택으로 읽고(`kube/observer.go:372-376`), 없으면 Runtime이 인스턴스 `Weight`를 모든 Workload에 준다(`runtime_scheduler.go:39-47`). writer는 prepare와 route에서 항상 쓴다(`kube/operations.go:840-848,980-1001`) | 삭제. annotation은 필수이고 Workload는 0개나 1개다. 둘 이상이면 source가 invalid다 |
| `SharedInstance` | capture가 두 Workload에 든 인스턴스를 사유로 남기고(`kube/scheduler.go:729`) 판단이 기다린다(`scheduler_capacity.go:20-22,133`) | 삭제. 위 경계 뒤에는 생기지 않는다 |
| `InstanceShared` | 수동 remove가 공유 인스턴스를 거절한다(`kube/measurement_sequence.go:411-412`) | 삭제. 같은 이유 |
| `staticRouting` 다중 UID | `StaticRoutingTarget`이 거절한다(`static_routing.go:33-36`) | 그대로 |
| Runtime 다중 binding | `runtimeInstance.Routing`이 UID별 map, `InstanceState.Workloads[]`와 UID별 `Waiting`, `Running`(`types.go:195-211`) | 유지. 경계 뒤에는 key가 하나 이하다. 줄이면 `runtime.go`(1,058줄)의 분배, pools, action 기록 형식을 다시 쓴다 |
| Dashboard | `features/resources.tsx:59-60`이 서비스 이름을 이어 붙인다 | 유지. 이름 하나를 보인다 |
| 설계 문서 | `workload-management.md:357,415,458,688,691,697`, `heterogeneous-workloads.md:28` | 명세 ⑤ 문장(analytic-planner 10장)으로 다시 쓴다 |

`scheduler_capacity.go`는 계획기 전환 작업이 고치는 파일이라 commit 4 전에 그 작업자와 순서를 맞춘다. 배포 전에 live serving Pod마다 annotation이 있고 Workload가 하나 이하인지 확인하고, 없는 Pod는 다시 prepare한다.

## 6. 재시작 관측 잠금 (D-R7-5)

r7에서 ChillServer Pod를 지우자 새 Pod가 `observation directory already owned`로 두 번 종료했고 약 14 s 더 멈췄다(r6 D-R6-2도 같다). Deployment `Recreate`(`serving_entrypoint.yaml:49-50`)는 rollout에만 적용되고, Pod 삭제에서는 새 Pod가 바로 뜨며 이전 Pod가 grace 150 s 동안 drain하면서 같은 RWO volume의 잠금을 쥔다. commit 1(`bca2da3`)은 잠금을 유지하고, `AcquireObservationOwnership`은 blocking `flock(LOCK_EX)`으로 이전 Pod가 잠금을 놓을 때까지 기다린다. 기다리는 동안 받은 SIGTERM은 SIGKILL에서 처리된다. ChillServer에는 probe가 없어 기다리는 Pod는 잠금을 얻을 때까지 산다.

## 7. 수동 동작과 정적 배치

수동 prepare와 remove, `staticRouting`은 명세의 정적 구성 비교와 고정 배치 기준선의 경로다(spec README 214행, `SCH-32`, `SCH-AC-20`, `SCH-33`, `SCH-35`, `DB-FR-33`). 측정에만 쓰던 경로는 측정 API가 지운다: `serving-operation --measurement-config`와 `POST /api/v1/measurements/operations`(측정 API commit 5), 손으로 하는 d 측정 절차와 `dispatchOccupancy` 설정(commit 8, `analytic-planner.md:29-34`). M5에 따로 지울 것은 없다.

**정적 배치 사용법.** 대상 Workload는 `autonomousWorkloads`에 없어야 한다.
1. prepare: `serving-operation --execute-config prepare.json --chill-server https://<entrypoint> --chill-ca-file ca.crt --chill-token-file token`, manifest `{"kind": "prepare", "workloadUID": "<UID>", "profileID": "<ID>", "node": {"name": "rasp5", "uid": "<Node UID>"}, "fixedBatchSize": 1}`. `profileID`는 `/api/v1/scheduler/profiles`에서, `fixedBatchSize`는 고정 배치 기준선(`SCH-35`)일 때만 준다. 준비한 인스턴스는 그 Workload의 routing annotation을 받는다.
2. 분배: `serving-scheduler` ConfigMap의 `staticRouting`에 `{"<Workload UID>": {"<instance ID>": 1000}}`. 인스턴스 ID는 `/api/v1/pools`에서 읽는다. 빈 항목(`{}`)은 그 Workload의 routing을 지운다([설정 API](../serving/scheduler-api.md)).
3. remove: 가중치를 0으로 바꾸고 처리 중 요청이 끝난 뒤 `{"kind": "remove", "workloadUID": "<UID>", "node": {...}, "instance": "<instance ID>"}`.

## 8. S3 PutObject 재설정 (D-R6-7)

- 오류는 `serving/observations.v1alpha5/<시간>/{requests,scheduler}.jsonl.gz`의 PutObject `write: connection reset by peer`, 시도 3번이다(r6 T04 05:42-06:04, r7 T06 07:01:59, 07:02:53).
- 읽기 전용 store 목록에서 T04 object는 05:00:32-34, T06 object는 07:00:07-10에 이미 있었다. 모든 재설정은 있는 object를 다시 올린 요청이고 잃은 기록은 없다.
- archive는 1분마다(`server.go:156`) 지우지 않은 지난 시간을 모두 다시 올렸다. 올리기는 `If-None-Match: *`다(`evidencestore/s3/store.go:46-52`). 재설정은 store가 본문 전송 중에 412로 답하고 연결을 닫을 때 생긴다 `[추론]`.
- commit 2(`bfb4563`)는 이 process가 모든 stream을 올린 시간을 기억해 다시 올리지 않는다. 한 stream이라도 실패한 시간은 다음 archive에서 모든 stream을 다시 올리고, 이미 있는 stream은 `Existing`이다. 지난 시간은 더 바뀌지 않는다(쓰기는 지금 시간 이후를 고른다). key와 형식은 그대로이고 재시작 뒤 첫 올리기는 `Existing`이다.

## 9. 범위 밖

점검 기록의 E5 전 재검토 항목(다중 Workload DP 칸 크기, 같은 노드 인스턴스 간섭, 전환 중 SLO)은 E5 재검토가 맡는다. Workload, ModelSpec, Dataset, Preprocessor, DeviceClass, ChillSystem CRD는 그대로다.

## 10. 커밋 단위

| # | commit | 내용 | 선행 | 검사 |
|---|---|---|---|---|
| 1 | `fix(serving): wait for the observation lock after a restart` | 6장, `bca2da3` | - | 잠금 대기와 context 종료 test. 다음 배포에서 Pod 삭제 한 번에 CrashLoop 0 |
| 2 | `fix(serving): archive each observation hour once per process` | 8장, `bfb4563` | - | 실패한 stream만 다시 올림 test. 배포 뒤 한 시간 동안 PutObject 오류 0 |
| 3 | `refactor(api): delete unused DeviceProfile and ClusterEnergyModel kinds` | 2장 삭제 목록, `make manifests generate` | - | 생성물 drift, `make helm-lint helm-template` |
| 4 | `refactor(serving): require one Workload per routing annotation` | 5장의 삭제 셋, 공유 fixture 정리 | 계획기 전환 작업 | annotation 없음과 두 UID가 invalid source, 한 UID와 빈 annotation은 정상 |
| 5 | `feat(nodeagent): apply power modes from Node labels` | 3.1절: label watch, state, generation, bootID, transition annotation | - | 적용 성공과 실패, 같은 모드 `success`만, read-back 변화와 bootID 변화가 generation + 1, agent 재시작은 0, 재부팅 모드가 재시작 뒤 한 번 + 1, label 삭제 |
| 6 | `feat(serving): request power modes through Node labels` | 3.2, 3.3절: ChillServer와 Profiler writer, admission 대기, stop safety, 기록의 generation, PMTC marker 출처 | 5, 측정 API 8 | writer 표마다 조건 밖 patch 없음, conflict, 기한 실패, 표의 기대 차이와 `PowerChangedDuringMeasurement`, 기존 evidence fixture decode |
| 7 | `refactor(api): delete NodeActuation` | 3.5절 삭제, `make manifests generate`, chart | 6 | `make arch-check`, default와 Profiler render |
| 8 | `build(serving): split Go binaries from the runtime image` | 4장 | 측정 API 7 | init container와 mount, identity test, render, live 기존 profile 하나로 prepare |
| 9 | `docs(design): record M5 decisions` | 05의 M5 행, node-agent 3장과 끝 문단, ADR 0015와 0020의 근거 문장, 5장의 설계 문서, 운영 문서 CLI 절 | 3-8 | 문서 link |

- 1과 2는 다음 배포에 들어간다. 5-7은 P7b 캠페인 뒤 한 번에, 나머지는 E5 뒤 측정 캠페인이 없을 때 배포한다.
- 모든 commit: focused test, `make lint`, `make test`. 3, 7, 8은 `make arch-check`도 돈다.

## 11. 남은 질문

- **Q1.** agent는 손으로 바뀐 모드를 원하는 모드로 되돌리지 않고 generation만 올린다(3.1절). 되돌리면 측정 하나에 변경이 둘 생기고 판정은 같다.
- **Q2.** ProfilingRun 실행 evidence는 기존 profile을 살리려고 `handoff` 필드를 선택 필드로 남긴다(3.4절). 버전을 올려 지우면 그 evidence의 모든 profile을 P7b 뒤 다시 잰다.
