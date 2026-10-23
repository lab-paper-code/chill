# 선언형 operation 비용 측정과 디스패치 점유 측정

캠페인은 CHILL 측정 API만으로 돈다. 측정마다 CR 하나를 만들고, [M3 admission](measurement-admission.md)이 노드마다 만든 순서대로 승인하며, ChillServer가 노드별 run으로 실행한다. host driver는 없다. 코드 근거는 `c539e14`, live 근거는 2026-10-21 snapshot이다. 실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 0. 명세 변경 제안 (승인 묶음)

`00_measurement.md`는 저장소가 추적하지 않는 로컬 사본이라 같은 문장을 로컬에 넣는다. 나머지는 commit 1이 반영한다.

| 위치 | 현재 | 제안 |
|---|---|---|
| `00_measurement.md` 측정 표(14-18행) | 행 셋 | 행 추가: "디스패치 점유 측정 \| 고정 배치 크기 인스턴스 하나에 그 처리율보다 많은 요청을 보낸 포화 구간에서 지속 처리율을 재고, 요청 하나가 배치 실행시간 밖에서 인스턴스를 점유하는 시간을 장비, 전력모드, runtime, 모델마다 산출" |
| `00_measurement.md` 87-89행 | "프로파일에 포함되지 않은 전달·응답 처리 시간은 설정 여유로 반영하고 Serving 관측으로 그 여유를 검증한다. 이 계산에 별도의 부하별 Serving 프로파일링 캠페인을 선행 조건으로 두지 않는다." | "프로파일에 포함되지 않은 시간 중 인스턴스를 점유하는 부분은 디스패치 점유 측정으로 용량 계산에 더한다. 나머지 전달과 응답 처리 시간은 설정 여유로 반영하고 Serving 관측으로 그 여유를 검증한다. 디스패치 점유는 장비, 전력모드, runtime, 모델마다 한 번 재며, 이 계산에 부하별 Serving 프로파일링 캠페인을 선행 조건으로 두지 않는다." |
| `00_measurement.md` 측정 POOL(29-33행) 끝, SRS `SCH-34` 끝 | 없음 | "한 노드의 측정 요청은 요청 순서대로 하나씩 실행하고, 서로 다른 노드의 측정은 동시에 진행한다. 측정이 노드에 둔 파드는 서빙으로 보지 않는다. 측정 조건의 CPU 할당이 그 전력모드의 온라인 CPU에서 서빙 예약을 뺀 범위에 들지 않거나, 요청이 가리키는 측정 근거가 쓸 수 없으면 요청을 거절하고 사유를 남긴다." |
| `00_measurement.md` 109-111행 앞 | 없음 | "운영자는 한 노드의 준비, 유휴, 해제, 빈 노드 전력모드 변경을 순서 있는 측정 요청 하나로 선언한다. 동작마다 기록을 남기고 기록은 그 요청을 가리킨다." |
| SRS `SCH-13` 셋째 문장 | "유입·전달·응답 비용은 시스템 전체에 하나로 둔 완료 여유 설정으로 반영한다." | "인스턴스가 요청마다 배치 실행시간 밖에서 점유되는 시간은 노드, 전력모드, runtime image, 모델마다 측정한 디스패치 점유를 재생의 배치 시간에 더해 반영하고, 그 측정이 없는 실행 조건은 용량을 계산하지 않는다. 그 밖의 유입, 전달, 응답 비용은 시스템 전체에 하나로 둔 완료 여유 설정으로 반영한다." |
| SRS 4장 | `SCH-AC-27`까지 | `SCH-AC-28` "한 노드에 측정 요청 여럿, 여러 노드의 동시 요청, 측정 파드가 있는 노드의 대기 요청, 서빙 예약과 겹치는 CPU 할당, 쓸 수 없는 측정 근거를 가리키는 요청 \| 노드마다 요청 순서대로 실행하고 노드끼리 동시 진행, 측정 파드로 거절하지 않음, 거절과 사유 뒤 다음 요청 진행 \| SCH-34". `SCH-AC-29` "디스패치 점유를 한 전력모드에서만 잰 노드의 같은 실행 조건 \| 잰 모드는 최신 측정으로 계산한 용량과 출처, 다른 모드는 용량 없음과 사유 \| SCH-08, SCH-13" |
| `docs/spec/README.md:102` | "프로파일링과 전환 비용 측정을 수행하는 노드 집합." | "프로파일링, 전환 비용과 디스패치 점유 측정을 수행하는 노드 집합." |

## 1. 측정한 문제

캠페인 준비 기록은 저장소 밖 `~/.cache/chill-assets/ops/pools-deploy/campaign/paused.md`(아래 paused)다.

| # | 문제 | 근거 |
|---|---|---|
| 1 | operation 비용 순서는 동기 HTTP 요청이다. 바쁜 노드는 409 `NodeBusy`로 끝나 host driver가 재시도해야 하고, power 단계는 client가 NodeActuation 전체를 만든다 | `measurement_sequence.go:84-113`, `operations.go:274-275`, paused |
| 2 | d는 ExecuteManual prepare, `staticRouting`, host loadgen, `e1.sql`, 설정 PUT을 사람이 잇는다. 그 d run의 serving Pod는 taint 없는 노드에 있어 그동안 판단된 대기 루트가 `Refused: NodeServing`(terminal)이 된다 | `analytic-planner.md:29-34`, `admission.go:186-188`, paused |
| 3 | d는 모드를 키로 갖지 않아(`internal/serving/scheduler.go:66`) 기준 모드에서 잰 값이 다른 모드 option에 쓰이고, 낮은 모드 κ를 과대평가할 수 있다 | `hetero-plan` A6 |
| 4 | 모든 online CPU가 서빙 예약인 모드(jetsonx `*_2CORE`), 예약 뒤 CPU가 모자란 할당(jetsono `MODE_15W` q4, genesis2 q8)의 측정이 계획기가 배치할 수 없는 option을 만든다 | paused, `profiles.go:178-181`(할당 = 측정 affinity) |

## 2. OperationCostMeasurement (`opcost`)

`edge.dacs.io/v1alpha1`, Namespaced, status subresource, spec 불변(`self == oldSelf`). printer column: Phase, Node, Reason, Age. `spec.node`는 Node 이름(1-253)이다.

| 필드 | 형식 |
|---|---|
| `spec.observation` | `intervalMilliseconds`, `requestTimeoutMilliseconds`(< interval), `maximumOperationSeconds`, `maximumBoundaryDistanceMilliseconds`, `minimumCoverage`(소수 문자열, (0, 1]), `maximumGapMilliseconds`. `MeasurementRequest`의 `sampling`(`MaxEvents = Duration/Interval + 2`, `metricobserver/observer.go:52`)과 `coverage`가 된다 |
| `spec.steps[]` | 1-32개, `listType=map` key `name`. `name`(DNS label), `kind`(`power`, `prepare`, `idle`, `remove`). `power`는 `powerMode`, `prepare`와 `remove`는 `batchEnergyOptimization`(같은 namespace의 BEO 이름), `idle`은 `idleDurationSeconds`(≥ 1)만 갖는다(CEL) |
| `status` | `phase`(`Measuring`, `Complete`, `Insufficient`, `Failed`), `reason`, `steps[]{name, key, verdict}` |

**실행.** 승인(5장)과 같은 `tickMu` 안에서 노드 run을 등록하고 `phase: Measuring`을 쓴다. 순서 ID는 CR UID다.
1. 단계마다 `MeasurementRequest`를 만든다. `Operation.ID = <UID>.<순번>.<name>`, meter와 store는 비워 노드 설정으로 채운다. `power`는 `nodePowerTargets(node, classes, policies)[powerMode]`와 `powerRequest`(`admission.go:453,458`)로 NodeActuation을 만들고 CR을 owner로 단다. `foreignActuations`(`admission.go:470`)가 루트 UID owner를 건너뛰므로 반환의 무효 판정이 자기 단계를 세지 않는다. `prepare`는 BEO의 현재 UID로 만든 profile ID(`catalog.go:385`)의 preparation(`profiles.go:167-185`), `remove`는 같은 preparation의 인스턴스다.
2. 기존 `Measure`를 차례로 부른다. 기록 key, 형식, witness는 그대로다(`cost.go:33-35`). 단계가 끝날 때마다 `status.steps`에 key와 verdict를 더한다.
3. 실패나 `Accepted`가 아닌 단계에서 멈추고 기존 순서 기록(`serving/operation-sequences/<UID>.json`)을 쓴다. 이어서 기존 반환이 돈다.

결과: 모든 단계 `Accepted`면 `Complete`. 저장된 단계 기록이 `Accepted`가 아니면(coverage, `PowerChangedDuringMeasurement`) `Insufficient`와 그 기록의 reason. 거절(5장), 단계 실행 오류, ChillServer 재시작은 `Failed`와 거절 사유, 오류 첫 줄, `ChillServerRestart`다. 비용 수집(`CostEvidence.Refresh`)은 같은 prefix를 읽으므로 그대로이고, 끝나지 않은 순서의 기록은 `SequenceIncomplete`와 함께 쓰인다.

## 3. DispatchOccupancyMeasurement (`occupancy`)

같은 group과 규칙이다. printer column: Phase, Node, Mode, PerRequest(`.status.perRequestNanos`), Reason, Age. `spec.node`도 같다.

| 필드 | 형식 |
|---|---|
| `spec.batchEnergyOptimization` | 잴 option의 BEO. 전력모드, runtime image, model은 그 profile의 `condition.powerMode`, `runtimeImage`, `logicalModel`이다 |
| `spec.workload` | 요청 경로를 줄 Workload 이름. 그 Dataset의 `evaluation` 부분집합을 보낸다 |
| `spec.batch` | 고정 배치 크기 B(≥ 1). 인스턴스를 `fixedBatchSize: B`로 준비한다(`SCH-35`) |
| `spec.rateFactor` | 소수 문자열 ≥ 1.2. 보내는 요청률 = rateFactor × B / (profile 배치 B 평균 실행시간) |
| `spec.warmupSeconds`, `durationSeconds`, `minimumBatches`, `maximumIdleFraction` | 창 앞에서 뺄 시간, 창 길이(≥ 60), 창 안 배치 수 하한, 유휴 시간 비율 상한(소수 문자열) |
| `spec.workers` | load generator `--workers`. 큐가 차 있는 동안 열린 요청은 약 `maxQueueSize`개이므로 그 이상 |
| `status` | `phase`(2장과 같은 넷), `reason`, `powerMode`, `perRequestNanos`, `throughput`(소수 문자열, req/s), `result`(`ProfilingRunEvidenceReference`) |

**실행.** 노드 run 하나가 다음을 차례로 한다. 모든 동작은 measurement 모드 `Operations`(taint toleration)다.
1. 노드가 profile 모드가 아니면 2장의 방법(CR owner NodeActuation)으로 `ChangePower`한다. 비용으로 기록하지 않고, witness 구간은 이 단계 뒤에 시작한다.
2. preparation을 `Owner = Workload`, `fixedBatchSize = B`로 prepare한다. owner가 있는 준비 Pod는 routing annotation `{<Workload UID>: 1}`을 받아 바로 그 Workload를 서빙한다(`operations.go:836-849`, `scheduler_test.go:421-425`). taint 노드에서도 같다 `[추론]`.
3. load Job을 만들고 끝날 때까지 기다린다. 끝나면 `ObservationLog.Query(Workload UID, Job 시작, 지금, …)`(`observation_log.go:807`)의 final 행 중 그 노드의 행을 읽고, Job을 지운 뒤 record와 status를 쓴다. 이어서 기존 반환이 돈다. 측정 인스턴스는 반환의 `clearNode`가 노드의 다른 serving Pod와 함께 지운다.

**load Job.** release namespace의 `occupancy-<UID hash>`, label `chill.dacs.io/measurement-load: <Node UID>`.
- `backoffLimit: 0`, `activeDeadlineSeconds` = warmup + duration + 120. run과 반환은 그 label의 Job을 `propagationPolicy: Background`로 지운다. 기본 삭제는 Pod를 남겨 반환 뒤에도 부하가 이어진다 `[추론: Kubernetes 기본값]`.
- `nodeName`은 ChillServer Pod의 노드(downward API `spec.nodeName`, env `NODE_NAME`)다. `nodeName`은 scheduler를 거치지 않아 NoSchedule taint를 보지 않는다. 그 노드는 gateway가 있는 노드라 부하가 다른 측정 노드의 전력에 섞이지 않는다.
- init `dataset`은 Dataset image에서 `sh -ec 'cp -R "$1"/. /data/' -- <path>/evaluation`(preprocessor 전달과 같은 형태, `operations.go:411`)이다.
- `load`는 `chill-load-generator` image의 `chill-loadgen replay --endpoint https://<fullname>-serving-entrypoint.<release ns>.svc:<port> --token-file <projected token> --ca-file /ca/tls.crt --workload <workload> --dataset-dir /data --fields <Workload Preprocessor의 spec.input.fields> --rate <요청률> --duration <warmup + duration> --arrivals poisson --seed 0 --workers <workers>`다. live 인증서는 자기 서명이고 SAN에 `.svc` 이름이 있으며 짧은 Service 이름은 검증에 실패한다. CA는 `servingEntrypoint.tls.secretName`의 `tls.crt` item만 projected volume으로 넣는다(`tls.key` 없음).
- ServiceAccount는 `<fullname>-load-generator`, image는 `load-generator/`를 담은 `build/docker/load-generator.Dockerfile`이다. base는 Python 3.12로 고정한다. live 인증서에 Subject와 Authority Key Identifier가 없어 3.13 이상의 기본 strict 검증이 거절할 수 있다 `[추론]`.

**계산.** 배치 시작 = `receivedAt + runningNanos`. 창 배치는 시작이 [첫 배치 시작 + warmup, 그 + duration]인 배치다. `e1.sql` 포화 표(`:26-37`)와 같은 식으로 X = (창 최대 batchID − 창 최소 batchID) × B / (두 배치의 시작 차이), `d = 1/X − mean(profile batches[B].durationNanos)/B`다. 창 배치마다 유휴 간격은 그 배치 행의 가장 늦은 `receivedAt + executedNanos`에서 다음 배치 행 중 가장 이른 `receivedAt + queuedNanos`(창 끝에서 자름)까지이고, 다음 요청이 먼저 왔으면 0이다. 유휴 비율 = 유휴 간격 합 / duration이다. 두 시각이 없는 쌍은 더하지 않는다. 창 안의 긴 멈춤 하나도 그 길이만큼 비율에 들어간다. `runningNanos`와 `queuedNanos`는 firstfloor 진행 로그가 늦게 와도 교환 결과의 `finished` 기록으로 채워지므로 PLC처럼 실행이 ms 단위인 요청도 배치로 센다([관측 분리 3.1절](observability-split.md#31-행)).

결과: 판정을 통과하면 `Complete`이고 d < 0도 그대로 기록한다. 창 배치 수 < `minimumBatches`, 유휴 비율 > `maximumIdleFraction`, witness 오류는 `Insufficient`와 `TooFewBatches <n>`, `NotSaturated <비율>`, `PowerChangedDuringMeasurement`이고 record를 쓴다. gateway가 포화를 막으면 `NotSaturated`로 드러난다. 거절(5장), prepare나 remove 오류, Job 실패나 시간 초과(`LoadFailed <Job 상태>`), ChillServer 재시작은 `Failed`이고 record가 없다.

## 4. 계획기의 d 수집

- `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`를 같은 방식(prefix 목록, 불변 identity cache)으로 읽는다. `Accepted` record 중 (Node UID, 전력모드, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 쓴다(동률은 key 순, `latestBy`와 같은 규칙).
- 결과는 `serving.DispatchOccupancy{node{name, uid}, powerMode, runtimeImage, model, perRequestNanos, source}` 목록이고 source는 record key다. `perRequestNanos`는 max(d, 0)이다(사용자 결정 A7). 음수 d는 record와 그 key로 보인다.
- 목록은 `config.Costs`, `config.Baselines`처럼 controller 설정을 거쳐 snapshot `dispatchOccupancy`에 실린다(`kube/scheduler.go:493`). `dispatchOccupancy(i)`는 snapshot으로 옮기고 `i.Node.UID`, `i.Profile.Condition.PowerMode`, runtime image, model이 모두 같은 항목을 찾는다. 두 소비자 `NewCapacityInput`(`capacity.go:102`)과 재생(`scheduler_replay.go:564`)이 이것을 쓴다. 행 key가 항목을 담으므로 새 측정이 그 행을 다시 계산하게 한다.
- 잰 적 없는 모드의 option은 `MissingDispatchOccupancy`다. 같은 노드, 모드, runtime image, model의 option끼리 d를 함께 쓴다(`analytic-planner.md:28`을 이렇게 고친다).

## 5. M3 admission 변경

두 kind는 루트 표(`admission.go:45-92`)에 더하는 측정 루트다. 대상은 `spec.node`, terminal은 `Complete`, `Insufficient`, `Failed`다.

- **노드별 엄격한 순서.** 대기 루트가 어떤 이유로든 기다리면 그 노드를 `claimed`(`:183,199`)로 둔다. 앞 루트가 기다리는 동안 뒤 루트가 승인되지 않는다. 그래서 기다림은 끝이 있어야 한다. 아래 확인은 끝나지 않을 대기를 거절로 바꾼다.
- **측정 Pod.** 측정 인스턴스는 승인된 taint 노드에만 있고, 줄 1(taint면 대기)이 줄 3(`NodeServing`)보다 먼저라 뒤 루트는 기다린다. `NodeServing`은 taint 없는 노드의 계획기와 수동 실행 Pod에만 걸린다. 따로 바꿀 코드는 없다.
- **승인 전 확인(새 거절 사유).**

| kind | 사유 |
|---|---|
| BEO | `CPUsUnavailable <CPU> <모드>`: `cpuAffinity`가 가용 CPU의 부분집합이 아님. `OnlineCPUsUnknown <모드>` |
| opcost, occupancy | `ProfileMissing <BEO> <사유>`: BEO 없음, terminal이 아님(`NotComplete`, 같은 노드에서 앞 순서면 줄 1이 먼저 기다리게 하므로 여기서는 뒤 순서나 다른 노드의 BEO), `Complete`가 아닌 terminal, catalog의 그 BEO issue 코드(`catalog.go:102-104,115,240,300,420`). catalog에 profile도 issue도 아직 없으면(갱신 지연) 기다린다. `NodeMismatch`: BEO `targetNode` ≠ `spec.node`. `ActuationNamespace`: actuation namespace ≠ CR namespace(owner reference는 같은 namespace만 가리킨다) |
| opcost | `PowerModeUnknown <모드>`, `MeterUndeclared` |
| occupancy | `BatchUnmeasured <B>`(profile batches에 없음, `catalog.go:402-403`), `WorkloadMissing`, `WorkloadAutonomous`(`autonomousWorkloads`에 있음), `WorkloadServing <Pod>`(같은 주기에 읽은 serving Pod 중 그 Workload가 owner이거나 route 가중치를 둔 Pod가 있음), `LoadGeneratorUnconfigured` |

- 모드는 `spec.state.candidate.powerMode`, 가용 CPU = 그 모드의 online CPU − 설정 `reserved`. 모드의 online CPU는 DeviceClass `powerModes[].onlineCPUs`(새 선택 필드, cpuset 목록)다. 필드가 없으면 그 모드가 지금 read-back 모드일 때만 Node annotation `edge.dacs.io/cpus`를 쓰고(annotation은 현재 모드의 값, `nodeprobe/probe.go:84`), 아니면 `OnlineCPUsUnknown`이다. live Jetson DeviceClass에는 commit 3 배포 때 모드마다 이 필드를 넣는다. CEL은 Node와 ConfigMap을 읽지 못하므로 확인은 admission이 하고, 캠페인 생성기는 걸러 내지 않는다.
- **승인과 실행.** 두 kind는 taint를 붙이고 `Granted`를 쓴 뒤 같은 잠금 안에서 run을 등록한다. 거절이면 ChillServer가 status도 `Failed`와 사유로 쓴다(다른 reconciler가 없다).
- **부하 노드 격리.** ChillServer 노드의 루트는 occupancy run이 도는 동안 기다리고, occupancy 루트는 ChillServer 노드에 측정 taint가 있는 동안 기다린다. live에서 그 노드는 genesis1이고 측정 lane에서 빠져 있다(`hetero-plan` A8). occupancy끼리는 동시에 돈다. live ChillServer CPU limit은 4다.
- **재시작.** taint가 살아 있고 `Granted`이며 terminal이 아닌 두 kind의 루트에 등록된 run이 없으면 끊긴 측정이다. `Failed: ChillServerRestart`를 쓰고, opcost는 순서 기록(reason `ChillServerRestart`, `recordRestartedSequence`)을 쓴 뒤 반환한다. terminal 루트는 status를 그대로 두고 반환만 다시 한다. taint만 있고 `Granted`가 없으면 PMTC처럼 반환하고 다시 대기한다. 재개는 하지 않는다. 같은 spec을 새 이름으로 만든다.

## 6. RBAC

- ChillServer(`serving_entrypoint_rbac.yaml`): ClusterRole에 두 kind `list`, profile namespace Role(`:200-202` 옆)에 두 kind와 `/status`의 `patch`, release namespace Role에 `jobs` `create, get, list, delete`를 더한다. nonResourceURL `/chill/api/v1/measurements/operations`를 지운다. actuation namespace Role의 `nodeactuations`는 그대로다(owner reference는 `blockOwnerDeletion` 없이 단다).
- `<fullname>-load-generator` ServiceAccount와 ClusterRole(nonResourceURL `/chill/api/v1/workloads/*` `post`), binding을 더한다.
- Profiler RBAC은 그대로다. 두 kind의 writer는 ChillServer 하나다.

## 7. 저장 계약 (ADR 0019)

| 대상 | 변경 | 처리 |
|---|---|---|
| CRD `operationcostmeasurements`, `dispatchoccupancymeasurements`, DeviceClass `powerModes[].onlineCPUs` | 새 kind `v1alpha1`, 선택 필드 | 기존 DeviceClass object 그대로 읽힌다 |
| operation 측정 `serving-operation-measurement.v1alpha2`, 순서 `serving-operation-sequence.v1alpha1` | 그대로. 순서 ID가 CR UID | 기존 `seq-` 기록도 같은 reader로 읽힌다 |
| `serving/dispatch-occupancy/<Node UID>/<CR UID>.json` | 새 불변 object `chill.dacs.io/dispatch-occupancy-measurement.v1alpha1`: `{node{name, uid}, powerMode, runtimeImage, model, profileID, batch, profileMeanNanos, offeredRate, window{start, end}, batches, throughput, perRequestNanos, idleFraction, loadJob{name, image}, isolation, verdict, reason, measuredAt}` | `PutImmutable` |
| record `idleFraction`, spec `maximumIdleFraction` | 유휴 시간 비율(3장 계산) | 버전을 올리지 않는다. 배포 뒤 모든 노드와 모드의 d를 다시 재고, 계획기는 `measuredAt`이 가장 늦은 record를 쓴다 |
| 설정 `dispatchOccupancy` | 삭제 | decode가 strict(`scheduler_service.go:118`)라 commit 8 배포에서 ConfigMap의 key를 같이 지운다 |
| 판단 기록 `v1alpha5` | snapshot `dispatchOccupancy` 추가, `settings.dispatchOccupancy` 빠짐, 행 입력 항목에 `powerMode`와 node UID | 관측 reader는 strict가 아니어서 없는 필드를 빈 값으로, 옛 필드를 무시하고 읽으므로 버전을 올리지 않는다(`analytic-planner.md` 7장과 같은 처리) |

## 8. 캠페인 manifest

캠페인은 lane 파일마다 `kubectl apply -f` 한 번이다. 노드끼리는 ChillServer의 노드 run이 동시에 돈다. 대기 순서는 `creationTimestamp`(1 s 해상도), 이름 순이므로 이름을 `<노드>-<세 자리 순번>-<내용>`으로 붙인다. 감시는 `kubectl get beo,idlebase,opcost,occupancy -w`와 annotation `chill.dacs.io/admission`이다. 아래는 `jetsono-010-plc-cuda-maxn` BEO(지금 BEO spec 그대로) 뒤에 같은 파일에 이어지는 두 루트다. d는 모드마다 재므로 다른 모드 단계에도 occupancy를 둔다.

```yaml
apiVersion: edge.dacs.io/v1alpha1
kind: DispatchOccupancyMeasurement
metadata: {name: jetsono-020-plc-occupancy-maxn, namespace: chill-system}
spec: {node: jetsono, batchEnergyOptimization: jetsono-010-plc-cuda-maxn, workload: plc, batch: 1, rateFactor: "1.2", warmupSeconds: 60, durationSeconds: 360, minimumBatches: 120, maximumIdleFraction: "0.01", workers: 128}
---
apiVersion: edge.dacs.io/v1alpha1
kind: OperationCostMeasurement
metadata: {name: jetsono-030-plc-cost-maxn, namespace: chill-system}
spec:
  node: jetsono
  observation: {intervalMilliseconds: 1000, requestTimeoutMilliseconds: 800, maximumOperationSeconds: 900, maximumBoundaryDistanceMilliseconds: 1500, minimumCoverage: "0.9", maximumGapMilliseconds: 3000}
  steps:
  - {name: up, kind: power, powerMode: MAXN}
  - {name: prepare, kind: prepare, batchEnergyOptimization: jetsono-010-plc-cuda-maxn}
  - {name: idle, kind: idle, idleDurationSeconds: 120}
  - {name: remove, kind: remove, batchEnergyOptimization: jetsono-010-plc-cuda-maxn}
  - {name: down, kind: power, powerMode: MODE_15W}
```

`Refused`와 `Failed`는 terminal이라 lane이 다음 루트로 넘어간다. 다시 잴 것은 원인을 지운 뒤 새 이름(새 순번)으로 만든다.

For each `remove`, the controller uses the exact Pod UID/container ID observed
in the accepted `prepare` result for that profile, retaining it across idle and
power steps. The preparation's `measured-*` name identifies the logical slot.
Replacement or missing physical sources fail the atomic operation's
exact-identity validation. Existing immutable records remain unchanged.

## 9. 커밋 단위, 배포와 삭제

| # | commit | 내용 | 선행 |
|---|---|---|---|
| 1 | `docs(spec): measure dispatch occupancy per power mode and queue measurement requests per node` | 0장(00은 로컬 사본) | 사용자 승인 |
| 2 | `feat(api): add operation-cost and dispatch-occupancy measurement kinds` | 두 type, DeviceClass `onlineCPUs`, `make manifests generate`, Helm CRD와 RBAC | - |
| 3 | `feat(serving): queue measurements per node and refuse unallocatable CPUs` | 5장의 엄격한 순서, `CPUsUnavailable`, `OnlineCPUsUnknown` | 2 |
| 4 | `feat(serving): check mode-changing node plans against the target mode's CPUs` | M4 노드 계획(`analytic-planner.md` 4장)이 모드를 바꾸는 계획의 CPU 예산을 목표 모드의 `onlineCPUs`로 확인. 필드가 없고 목표가 현재 모드가 아니면 그 계획은 `PlacementInventoryMissing`(지금은 현재 모드 annotation, `kube/scheduler.go:621`) | 2 |
| 5 | `feat(serving): run declared operation-cost measurements` | 루트 등록, 2장 run, owner NodeActuation, status, 재시작, HTTP route와 `--measurement-config` 삭제 | 2, 3 |
| 6 | `build(loadgen): package the load generator image` | Dockerfile(Python 3.12), `make docker-build-load-generator`, Helm `loadGenerator.image`, ServiceAccount와 RBAC | - |
| 7 | `feat(serving): measure dispatch occupancy at saturation` | 3장 run, load Job, 계산, record, status, 부하 노드 격리 | 2, 3, 6 |
| 8 | `feat(serving): plan with measured per-mode dispatch occupancy` | 4장 수집, snapshot 필드, 모드와 Node UID 일치, 설정 삭제, ConfigMap sample, Dashboard | 7 |

- 배포 A는 2-7과 live Jetson DeviceClass의 `onlineCPUs`다. 그 뒤 live d 9개를 occupancy로 다시 재고(사용자 결정, 노드끼리 동시), 배포 B(8)와 ConfigMap의 `dispatchOccupancy` 삭제를 같이 한다. 그래야 자동제어 Workload가 `CapacityPending`으로 비는 구간이 없다. 긴 lane은 배포 B 뒤에 시작한다(재시작이 lane을 끊는다).
- 지우는 것(5): `POST /api/v1/measurements/operations`, `MeasureSequence`, `seq-` 순서 ID와 `c.sequences`, `serving-operation --measurement-config` (CLI는 `--execute-config`만 남는다). (8): `SchedulerSettings.DispatchOccupancy`와 설정의 `dispatchOccupancy()`, 검증(`internal/serving/scheduler.go:41-71,346-353`), `config/samples/serving-scheduler.json:16-24`, Dashboard `read-types.ts:39`와 `scheduler.test.ts:24-29`, `scheduler_service_test.go:100-125`의 설정 case.
- 각 commit이 자기가 바꾼 문서를 고친다: `measurement-admission.md` 2.1절 표와 2.5절, `scheduler-operations.md:117-151`, `analytic-planner.md` 4장과 `:27-34,127,143,155`, `scheduler-implementation.md:111-112`.

## 10. 검사

| commit | 검사 |
|---|---|
| 2 | CEL: kind별 필드 조합, spec 불변, `rateFactor < 1.2` 거절 |
| 3 | 앞 루트가 catalog 지연으로 대기면 같은 노드 뒤 루트 대기, 다른 노드 승인. 측정 taint 노드의 대기 루트는 측정 인스턴스가 있어도 대기, taint 없는 노드의 계획기 Pod는 `NodeServing`. `cpuAffinity` [2,3,4,5]와 `onlineCPUs` 0-3 거절. `onlineCPUs` 없는 모드는 read-back 모드와 같으면 annotation, 다르면 `OnlineCPUsUnknown` |
| 4 | jetsonx `*_2CORE`(`onlineCPUs` 0-1, 예약 [0,1])로 바꾸는 계획이 CPU 예산에서 빠짐. 필드 없는 다른 모드 계획이 `PlacementInventoryMissing`. 현재 모드 계획은 그대로 |
| 5 | power, prepare, idle, remove, power 순서가 한 번 승인되고 key가 `<UID>.<n>.<name>`, status 단계 다섯, `Complete`, 단계 NodeActuation owner가 CR이고 반환 뒤 `measurement-invalid` 없음. 둘째 단계 `Insufficient`면 멈춤, 순서 기록, phase `Insufficient`. 재시작 뒤 terminal이 아닌 루트는 `Failed: ChillServerRestart`와 순서 기록, `Complete` 루트는 status 그대로 반환. `ProfileMissing`(없음, `NotComplete`, `Failed`, catalog issue), `NodeMismatch`, `ActuationNamespace`, `PowerModeUnknown` 거절과 다음 루트 승인. route 404 |
| 7 | fake 행으로 X와 d(rasp5 26.88 req/s, profile 27.79 ms에서 9.41 ms), warmup 제외, 창 배치의 시작 차이로 나눔, batchID 차이 계산이 빠진 행에 무관, 유휴 시간 비율(창 안 140 s 멈춤 포함), `TooFewBatches`, `NotSaturated`, record의 `powerMode`. `BatchUnmeasured` 거절. Workload 요청이 taint 노드의 측정 인스턴스에 도달. Job spec(init, args의 `.svc` endpoint, `tls.crt` item만, `nodeName`, `backoffLimit` 0, deadline, label), Background 삭제 뒤 Pod 0, Job 실패 `LoadFailed`, 반환의 label Job 삭제. ChillServer 노드 루트와 occupancy의 상호 대기, 다른 노드 occupancy 둘 동시 실행 |
| 8 | 같은 key의 두 record 중 최신, `Insufficient` 무시, d < 0이면 0과 source key, 다른 모드나 다른 Node UID의 record만 있으면 `MissingDispatchOccupancy`, 재생과 행이 같은 항목을 씀, `dispatchOccupancy` 있는 ConfigMap decode 거절 |
| 전체 | `make lint test`, `make manifests generate`, `make helm-lint helm-template`(default와 `values-profiler.yaml`), `make dashboard-check`, `make test-loadgen` |

실클러스터: rasp5 mobilenet q1 occupancy가 m4 r3의 d 9.41 ms와 κ 25.3을 재현하고, load Pod가 `.svc` 이름으로 TLS 검증을 통과한다. 이어서 live d 9개 occupancy를 동시에 돌린다. 그 뒤 lane 셋을 동시에 apply하고 노드별 순서, `NodeServing` 0, 재시작 한 번의 `ChillServerRestart`와 다음 루트 진행을 기록한다.

## 11. 사용자에게 묻는 것

1. d를 모드마다 재면 Jetson의 occupancy가 모드 수만큼 는다(jetsono 4, jetsonx는 CPU 할당 가능한 6, jetson-nano 2 모드 × 노드의 runtime image와 model 수, 하나에 약 8분) `[추론]`. 모든 모드에서 잴까, 계획에 쓸 모드만 잴까? 재지 않은 모드의 option은 `MissingDispatchOccupancy`로 계획에서 빠진다.
