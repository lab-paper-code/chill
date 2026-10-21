# M5 정리

[05 이행 계획](../architecture-rethink/05-recommendation-migration.md)의 M5와 그동안 쌓인 정리 후보를 항목마다 근거, 판정, [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md) 영향, commit으로 정한다. 코드 근거는 `4064f09`에 [측정 API](../serving/measurement-api.md) 설계가 반영된 상태이고, live 근거는 2026-10-21 읽기 전용 snapshot이다. 실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 1. 요약

| # | 항목 | 판정 | ADR 0019 | commit |
|---|---|---|---|---|
| 1 | 측정 kind 여섯(BEO, MLIB, ProfilingRun, PMTC, opcost, occupancy) | 각자 유지 | 없음 | - |
| 2 | `DeviceProfile`, `ClusterEnergyModel` | 삭제 | object 0개 | 3 |
| 3 | NodeActuation CRD | 유지 | 없음 | 7(문서) |
| 4 | Go 바이너리와 runtime image | worker image로 분리, 기존 runtime image 유지 | Run 기록과 occupancy 기록에 필드 하나씩 | 6 |
| 5 | routing annotation 없음 fallback, `SharedInstance`, `InstanceShared` | 삭제 | Pod annotation만 바뀐다 | 4 |
| 6 | Runtime 다중 UID binding, Dashboard 서비스 표시 | 유지 | 없음 | - |
| 7 | `serving-operation`, 수동 동작, `staticRouting` | 삭제 제안(D3) | ConfigMap key | 5 |
| 8 | 재시작 관측 잠금 CrashLoop(D-R7-5) | 잠금을 기다림 | 없음 | 1 |
| 9 | S3 PutObject 재설정(D-R6-7) | 시간 단위 묶음을 process마다 한 번 올림 | 없음 | 2 |

## 2. 측정 kind

**근거.**
- 측정 루트는 BEO, MLIB, 독립 ProfilingRun, PMTC이고 측정 API가 opcost와 occupancy를 더한다. 승인, 대기, 반환은 이미 한 코드다: 루트 표(`internal/serving/kube/admission.go:46-94`)가 kind마다 노드와 terminal 여부만 뽑는다. 단계 측정은 `Measure`와 witness 하나를 쓴다([측정 API](../serving/measurement-api.md) 2장).
- live object: BEO 19, MLIB 7, ProfilingRun 192, PMTC 0(`kubectl get`).
- profile ID는 BEO UID를 담고(`kube/catalog.go:385`), profile의 유휴 전력은 MLIB 결과다(`:387`). kind를 하나로 합치면 이 object를 지우게 되어(ADR 0019) 모든 profile을 다시 재야 한다(hetero 계획 기준 약 27 h `[추론]`). 그 대가로 채워지는 표는 없다.
- 겹쳐 보이는 쌍도 다른 양을 잰다. opcost `power`는 빈 노드 전환, PMTC는 인스턴스가 있는 전환이고 계획기의 power 동작은 그 순간의 적재 상태로 비용을 찾는다(`scheduler_plan.go:350-352`, `kube/cost.go:174`). MLIB는 profiling Job 실행 경로의 적재 유휴이고 profile의 유휴 전력이 된다. opcost `idle`은 계획기의 노드 기준선이다 `[추론: 실행 process가 달라 값이 다를 수 있다]`.

**판정.** 여섯 kind를 그대로 둔다. 공통 부분은 이미 루트 표와 `Measure`로 하나다.

**삭제.** `DeviceProfile`, `ClusterEnergyModel`은 `api/` 밖의 Go reader와 writer가 없고 live object가 0개다. type, `config/crd/bases`와 chart의 CRD, `config/crd/kustomization.yaml:7-8`, `PROJECT:32,40`, 이 type을 owner로 쓰는 `test/integration/operator/ownerref_scope_test.go`, [ADR 0005](../../adr/0005-crd-scope.md)의 두 항목을 지운다. chart의 `templates/crds`에 있는 CRD는 Helm이 관리하므로 upgrade가 CRD를 지운다 `[추론]`.

## 3. NodeActuation CRD

**근거.**
- writer: ChillServer 전력 동작(`kube/scheduler.go:995-999`, `kube/operations.go:762-810`), Profiler controller 넷(campaign `transition_builder.go`, profiler `execution_actuation.go`, idlebaseline `transition.go`, transitioncost `attempt_materializer.go`), opcost `power` 단계(측정 API 2장).
- reader: capture(`kube/scheduler.go:508,653`), serving source 확인(`kube/observer.go:526`), 측정 무효 판정 `PowerChangedDuringMeasurement`(`kube/admission.go:392,470`, `kube/measurement_sequence.go:262`), `chill-node-agent-actuation`.
- live 69개: owner 없는 ChillServer 동작 57, BEO 5, MLIB 7.
- [Node agent](node-agent.md) 3장은 이것을 노드 의도 `spec.powerMode`로 바꾸려 했다. 그 동기였던 문제 5(deadline)와 9(재부팅 뒤 `Failed`)는 M2가 동작마다 새 object와 annotation read-back으로 풀었다.

**판정.** 유지한다. 전력 변경마다 남는 object(owner, 요청 시각, 결과)가 측정 무효 판정이 읽는 기록이다. 노드 의도 필드는 마지막 값만 가지므로 같은 기록을 위해 다시 object를 만들게 된다. ADR 0015, 0017은 그대로다.

owner 없는 object는 계획기 전력 동작마다 하나씩 쌓이고 capture가 Tick마다 모두 읽는다. `evaluationElapsedNanos`에 그 비용이 보이면 측정 창 밖의 오래된 object를 지운다(ponytail 상한).

## 4. Go 바이너리와 runtime image

**근거.**
- `build/docker/firstfloor.Dockerfile:9-14,71-74`가 `serving-worker`, materializer(`chill-server`), `measurement-worker`를 runtime image에 넣는다.
- profile의 실행 상태 identity가 runtime image를 담고(`profiler/projection/identity.go:44,50`), 인스턴스는 profile의 runtime image로 뜬다(`kube/operations.go:412-413`). 그래서 Go 변경이 profile을 바꾼다. pools 배포에서는 `serving-worker`의 요청 형식 변경 때문에 BEO 18개의 profile이 서빙 불가가 되어 다시 쟀다(`p4-deploy-plan`). Go 배포마다 runtime 변형 7개를 덮어써 만든다.
- 클러스터는 v1.31.13이고 ImageVolume은 alpha라 꺼져 있다.

**설계.**
- 새 image `chill-worker`: busybox base, `/opt/chill/bin`에 `serving-worker`와 `measurement-worker`. Makefile `docker-build-worker`(multi-arch). 두 바이너리의 의존 package가 바뀔 때만 다시 만든다(`go list -deps`로 확인).
- 인스턴스 Pod와 ProfilingRun 실행 Job에 init container `chill-bin`(worker image)을 둔다. `cp /opt/chill/bin/* /chill-bin/`으로 emptyDir에 복사하고 runtime container가 그 emptyDir를 `/opt/chill/bin`에 mount한다. `measurement-worker` 명령은 `/opt/chill/bin/measurement-worker`다(`execution_job_spec.go:211`).
- profile init container(`--materialize-profile`)는 ChillServer image로 돈다. Python을 쓰지 않는 Go 단계다 `[추론]`. serving source 확인(`kube/observer.go:445,465`)은 init container 둘과 각 image를 받는다.
- mount가 image 안의 바이너리를 가리므로 live의 overlay runtime image(`pools-ed628c8-*`)를 그대로 쓴다. 기존 profile은 identity가 같아 다시 재지 않는다.
- `firstfloor.Dockerfile`은 Go builder 단계를 지운다. runtime image는 Python, ORT, `runtime/` runner가 바뀔 때만 만든다.
- worker image(digest)는 Helm `worker.image` 하나로 ChillServer와 Profiler에 준다.
- 기록: `measurement-worker`는 측정 도구이므로 worker image가 측정 protocol identity에 metric observer, workload observer image와 함께 들어간다(`identity.go:64-71`). 새 run은 새 protocol이고 기존 profile은 그대로 쓰인다. occupancy 기록은 `workerImage`를 담는다. d는 `serving-worker`의 전달 경로에 달려 있다 `[추론]`.
- [Node agent](node-agent.md) 끝의 metric observer, workload observer image 합치기는 하지 않는다. 두 image가 protocol identity에 있어 합치면 이후 모든 run의 protocol이 바뀌고, 풀리는 문제가 없다(node-agent 2.4절).

**ADR 0019.** ProfilingRun 고정 설정의 `images.worker`와 occupancy 기록의 `workerImage`는 선택 필드로 한 번 더한다. 측정 캠페인이 없을 때 배포한다. reader가 없는 필드를 빈 값으로 읽지 못하는 기록은 지우고 다시 잰다(occupancy 하나 약 8분).

## 5. 인스턴스 공유 정리

[용량표 계획기](../serving/analytic-planner.md) 6장의 M5 후보다. 공유는 연구 범위 밖이고, 공유를 막는 검사는 한 경계에 하나만 둔다.

| 후보 | 근거 | 판정 |
|---|---|---|
| annotation 없음 fallback | 서빙 source가 `edge.dacs.io/serving-routing`을 선택으로 읽고(`kube/observer.go:372-376`), 없으면 Runtime이 그 인스턴스의 `Weight`를 모든 Workload에 준다(`runtime_scheduler.go:39-47`). writer는 prepare와 route에서 항상 쓴다(`kube/operations.go:840-848,980-1001`) | 삭제. annotation은 필수이고 Workload는 0개나 1개다. 둘 이상이면 source가 invalid다 |
| `SharedInstance` | capture가 두 Workload에 든 인스턴스를 사유로 남기고(`kube/scheduler.go:729`) 판단이 기다린다(`scheduler_capacity.go:20-22,133`) | 삭제. 위 경계 뒤에는 생기지 않는다 |
| `InstanceShared` | 수동 remove가 공유 인스턴스를 거절한다(`kube/measurement_sequence.go:411-412`) | 삭제(D3이면 수동 동작과 함께) |
| `staticRouting` 다중 UID | `StaticRoutingTarget`이 이미 거절한다(`static_routing.go:33-36`) | 7장 D3 |
| Runtime 다중 binding | `runtimeInstance.Routing`이 UID별 map, `InstanceState.Workloads[]`와 UID별 `Waiting`, `Running`(`types.go:195-211`) | 유지. 경계 뒤에는 key가 하나 이하다. 단일 필드로 줄이면 `runtime.go`(1,058줄)의 분배와 pools, action 기록 형식을 다시 쓰는데 측정 이득이 없다 |
| Dashboard | `features/resources.tsx:59-60`이 서비스 이름을 이어 붙인다 | 유지. 이름 하나를 보인다 |
| 설계 문서 | `workload-management.md:357,415,458,688,691,697`, `heterogeneous-workloads.md:28` | 명세 ⑤ 문장(analytic-planner 10장)으로 다시 쓴다 |

`scheduler_capacity.go`는 계획기 전환 작업이 고치는 파일이라 commit 4 전에 그 작업자와 순서를 맞춘다. 배포 전에 live serving Pod마다 annotation이 있고 Workload가 하나 이하인지 확인한다. 없는 Pod는 다시 prepare한다.

## 6. 재시작 관측 잠금 (D-R7-5)

**근거.** r7에서 ChillServer Pod를 지우자 새 Pod가 `observation directory already owned`로 두 번 종료했고 약 14 s 더 멈췄다. r6 D-R6-2도 같다. `AcquireObservationOwnership`이 `LOCK_NB` 실패에서 바로 오류를 낸다(`internal/serving/observation_lock.go:24-25`, `cmd/chill-server/server.go:51`). Deployment는 `Recreate`이지만(`charts/chill/templates/serving_entrypoint.yaml:49-50`) 이것은 rollout에만 적용된다. Pod 삭제에서는 ReplicaSet이 새 Pod를 바로 만들고, 이전 Pod는 grace 150 s(`values.yaml:467`) 동안 drain하며 같은 RWO volume의 잠금을 쥔다. ChillServer에는 probe가 없으므로 기다리는 Pod는 잠금을 얻을 때까지 그대로 산다.

**판정.** 잠금은 두 writer가 한 directory를 쓰는 것을 막으므로 유지한다. 획득을 context가 끝날 때까지 1 s마다 `LOCK_NB`로 다시 시도하고, 처음 기다릴 때 한 번 기록한다. test: 첫 소유자가 닫으면 두 번째 획득이 성공하고, context가 끝나면 오류다.

## 7. 수동 동작과 `serving-operation` (D3)

**근거.** 측정 API commit 5 뒤 `serving-operation`에는 `--execute-config`만 남고 `POST /api/v1/scheduler/operations`(`internal/serving/httpapi/handler.go:104`)로 꺼진 Workload 하나를 prepare나 remove한다.
- 제약: 동기 HTTP 요청이 `--timeout`(기본 30분, `cmd/serving-operation/main.go:52`) 동안 host 세션에 묶인다. 호출마다 Workload 하나다. 수동 remove는 공유 인스턴스를 거절한다.
- 쓰임: d 측정(occupancy kind가 대신한다), 운영자 부트스트랩(사용자 원칙에서 수동 부트스트랩은 설계 결함), 수동 인스턴스 분배(`staticRouting`, hetero 계획 4장).
- PMTC runner와 occupancy 인스턴스는 `Operations`를 직접 부르므로 이 경로와 무관하다.

**제안.** CLI(`cmd/serving-operation`), 수동 route와 `ExecuteManual`, `staticRouting` 설정, `static_routing.go`, `kube/static_routing.go`, `manual-static-routing` 동작, Dashboard `scheduler-transitions.tsx`의 해당 표시를 지운다. 꺼진 Workload는 인스턴스를 갖지 않고, 실험은 자동제어와 측정 kind로 돈다. `staticRouting`이 있는 ConfigMap은 strict decode가 거절하므로 배포 때 key를 같이 지운다.

## 8. S3 PutObject 재설정 (D-R6-7)

**근거.**
- 오류는 `serving/observations.v1alpha5/<시간>/{requests,scheduler}.jsonl.gz`의 PutObject `write: connection reset by peer`, 시도 3번이다. r6에서 T04 시간에 05:42, 05:43, 06:02-06:04, r7에서 T06 시간에 07:01:59, 07:02:53에 났다.
- 읽기 전용 store 목록에서 T04 object는 05:00:32-34(9.8 MB, 11.7 MB), T06 object는 07:00:07-10에 이미 만들어져 있었다. 모든 재설정은 있는 object를 다시 올린 요청이다. 잃은 기록은 없다.
- archive는 1분마다(`server.go:156`) 아직 지우지 않은 지난 시간을 모두 다시 올리고, 시간 시작 + 2 h 뒤 올리기가 성공하면 지운다(`observation_log.go` `archive`). 올리기는 `If-None-Match: *`이고(`internal/evidencestore/s3/store.go:46-52`) 있는 object는 precondition으로 `Existing`이 된다.
- 재설정은 store가 본문 전송 중에 412로 답하고 연결을 닫을 때 생긴다 `[추론]`. 실패한 시간은 다음 성공까지 지워지지 않는다.

**판정.** `ObservationLog`가 이 process에서 올린 (시간, stream)을 메모리에 두고 다시 올리지 않는다. 재시작 뒤 첫 올리기는 지금처럼 `Existing`이다. key와 형식은 그대로다. test: 같은 시간을 두 번 archive하면 `PutImmutable`이 stream마다 한 번 불린다.

## 9. 범위 밖

점검 기록의 E5 전 재검토 항목(다중 Workload DP 칸 크기, 같은 노드 인스턴스 간섭, 전환 중 SLO)은 E5 재검토가 맡는다. Workload, ModelSpec, Dataset, Preprocessor, DeviceClass, ChillSystem CRD는 그대로다.

## 10. 커밋 단위

| # | commit | 내용 | 선행 | 검사 |
|---|---|---|---|---|
| 1 | `fix(serving): wait for the observation lock after a restart` | 6장 | - | lock test, live Pod 삭제 한 번에 CrashLoop 0 |
| 2 | `fix(serving): archive each observation hour once per process` | 8장 | - | archive test, 한 시간 동안 PutObject 오류 0 |
| 3 | `refactor(api): delete unused DeviceProfile and ClusterEnergyModel kinds` | 2장 삭제 목록, `make manifests generate` | D1 | 생성물 drift, `make helm-lint helm-template` |
| 4 | `refactor(serving): require one Workload per routing annotation` | 5장, 표의 삭제 셋, 공유 fixture 정리 | 계획기 전환 작업 | annotation 없음과 두 UID가 invalid source, 한 UID와 빈 annotation은 정상 |
| 5 | `refactor(serving): delete manual operations and static routing` | 7장, ConfigMap sample, Dashboard 타입, [설정 API](../serving/scheduler-api.md), [운영 문서](../serving/scheduler-operations.md) CLI 절 | D3, 측정 API commit 5 | `staticRouting` ConfigMap decode 거절, route 404, `make dashboard-check` |
| 6 | `build(serving): split Go binaries from the runtime image` | 4장, Dockerfile, Makefile, Helm, Pod와 Job spec, source 확인, protocol identity, occupancy `workerImage` | D4, 측정 API commit 7 | Pod와 Job spec의 init container와 mount, identity test, default와 Profiler render, live 기존 profile 하나로 prepare |
| 7 | `docs(design): record M5 decisions` | 05의 M5 행, node-agent 3장과 끝 문단, 5장의 설계 문서 | D1-D4 | 문서 link |

- 1과 2는 독립이고 작아서 E5 전에 넣을 수 있다(D5). 3-7은 E5 뒤, 최종 검증 전, 측정 캠페인이 없을 때 배포한다.
- 모든 commit: focused test, `make lint`, `make test`. 3과 6은 `make arch-check`도 돈다.

## 11. 결정 요청

- **D1.** 측정 kind 여섯은 각자 두고, 05의 "Measurement CRD 통합"은 쓰지 않는 두 kind의 삭제로 채운다.
- **D2.** NodeActuation CRD를 유지한다(05는 삭제, node-agent 3장은 노드 의도로 교체를 적었다).
- **D3.** `serving-operation`, 수동 prepare와 remove, `staticRouting`을 지운다. 지우면 꺼진 Workload를 손으로 서빙하는 길이 없다.
- **D4.** Go 분리 뒤 `serving-worker`와 `measurement-worker` 변경은 기존 profile을 무효로 만들지 않는다(새 run은 새 protocol identity). d는 `workerImage`를 기록만 하고, worker가 바뀔 때 다시 잴지는 운영자가 정한다. 다른 선택은 d 행 key에 worker image를 넣어 바뀌면 그 option이 `MissingDispatchOccupancy`가 되는 것이다.
- **D5.** commit 1과 2를 E5 전에 배포한다.
