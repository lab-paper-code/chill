# 선언형 operation 비용 측정과 디스패치 점유 측정

캠페인은 CHILL 측정 API만으로 돈다. 측정마다 CR 하나를 만들고, [M3 admission](measurement-admission.md)이 노드마다
만든 순서대로 승인하며, ChillServer가 노드별 run으로 실행한다. host driver는 없다. 코드 근거는 `c539e14`이고,
실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 0. 명세 변경 제안 (승인 묶음)

`00_measurement.md`는 저장소가 추적하지 않는 로컬 사본이라 같은 문장을 로컬에 넣는다. 나머지는 commit 1이 반영한다.

| 위치 | 현재 | 제안 |
|---|---|---|
| `00_measurement.md` 측정 표(14-18행) | 행 셋 | 행 추가: "디스패치 점유 측정 \| 고정 배치 크기 인스턴스 하나에 그 처리율보다 많은 요청을 보낸 포화 구간에서 지속 처리율을 재고, 요청 하나가 배치 실행시간 밖에서 인스턴스를 점유하는 시간을 장비, runtime, 모델마다 산출" |
| `00_measurement.md` 87-89행 | "프로파일에 포함되지 않은 전달·응답 처리 시간은 설정 여유로 반영하고 Serving 관측으로 그 여유를 검증한다. 이 계산에 별도의 부하별 Serving 프로파일링 캠페인을 선행 조건으로 두지 않는다." | "프로파일에 포함되지 않은 시간 중 인스턴스를 점유하는 부분은 디스패치 점유 측정으로 용량 계산에 더한다. 나머지 전달과 응답 처리 시간은 설정 여유로 반영하고 Serving 관측으로 그 여유를 검증한다. 디스패치 점유는 장비, runtime, 모델마다 한 번 재며, 이 계산에 부하별 Serving 프로파일링 캠페인을 선행 조건으로 두지 않는다." |
| `00_measurement.md` 측정 POOL(29-33행) 끝, SRS `SCH-34` 끝 | 없음 | "한 노드의 측정 요청은 요청 순서대로 하나씩 실행하고, 서로 다른 노드의 측정은 동시에 진행한다. 측정이 노드에 둔 파드는 서빙으로 보지 않는다. 측정 조건의 CPU 할당이 그 전력모드의 온라인 CPU에서 서빙 예약을 뺀 범위에 들지 않으면 요청을 거절하고 사유를 남긴다." |
| `00_measurement.md` 109-111행 앞 | 없음 | "운영자는 한 노드의 준비, 유휴, 해제, 빈 노드 전력모드 변경을 순서 있는 측정 요청 하나로 선언한다. 동작마다 기록을 남기고 기록은 그 요청을 가리킨다." |
| SRS `SCH-13` 셋째 문장 | "유입·전달·응답 비용은 시스템 전체에 하나로 둔 완료 여유 설정으로 반영한다." | "인스턴스가 요청마다 배치 실행시간 밖에서 점유되는 시간은 노드, runtime image, 모델마다 측정한 디스패치 점유를 재생의 배치 시간에 더해 반영하고, 그 측정이 없는 실행 조건은 용량을 계산하지 않는다. 그 밖의 유입, 전달, 응답 비용은 시스템 전체에 하나로 둔 완료 여유 설정으로 반영한다." |
| SRS 4장 | `SCH-AC-27`까지 | `SCH-AC-28` "한 노드에 측정 요청 여럿, 여러 노드의 동시 요청, 측정 파드가 있는 노드의 대기 요청, 서빙 예약과 겹치는 CPU 할당 \| 노드마다 요청 순서대로 실행하고 노드끼리 동시 진행, 측정 파드로 거절하지 않음, CPU 할당 거절과 사유 \| SCH-34". `SCH-AC-29` "디스패치 점유 측정 전후의 같은 실행 조건 \| 측정 전에는 용량 없음과 사유, 측정 뒤 최신 측정으로 계산한 용량과 그 출처 \| SCH-08, SCH-13" |
| `docs/spec/README.md:102` | "프로파일링과 전환 비용 측정을 수행하는 노드 집합." | "프로파일링, 전환 비용과 디스패치 점유 측정을 수행하는 노드 집합." |

## 1. 측정한 문제

| # | 문제 | 근거 |
|---|---|---|
| 1 | operation 비용 순서는 동기 HTTP 요청이다. 바쁜 노드는 409 `NodeBusy`로 끝나 host driver가 재시도해야 하고, power 단계는 client가 NodeActuation 전체를 만든다 | `measurement_sequence.go:84-113`, `operations.go:274-275`, `paused.md` |
| 2 | d는 ExecuteManual prepare, `staticRouting`, host loadgen, `e1.sql`, 설정 PUT을 사람이 잇는다 | `analytic-planner.md:29-34` |
| 3 | d run의 serving Pod가 taint 없는 노드에 있는 동안 판단된 대기 루트가 `Refused: NodeServing`(terminal)이 된다 | `admission.go:186-188`, `paused.md` |
| 4 | 모든 online CPU가 서빙 예약인 모드(jetsonx `*_2CORE`), 예약 뒤 CPU가 모자란 할당(jetsono `MODE_15W` q4, genesis2 q8)의 측정이 계획기가 배치할 수 없는 option을 만든다 | `paused.md`, `profiles.go:178-181`(할당 = 측정 affinity) |

## 2. OperationCostMeasurement (`opcost`)

`edge.dacs.io/v1alpha1`, Namespaced, status subresource, spec 불변(`self == oldSelf`). printer column: Phase, Node, Reason, Age. `spec.node`는 Node 이름(1-253)이다.

| 필드 | 형식 |
|---|---|
| `spec.observation` | `intervalMilliseconds`, `requestTimeoutMilliseconds`(< interval), `maximumOperationSeconds`, `maximumBoundaryDistanceMilliseconds`, `minimumCoverage`(소수 문자열, (0, 1]), `maximumGapMilliseconds`. `MeasurementRequest`의 `sampling`(`MaxEvents = Duration/Interval + 2`, `metricobserver/observer.go:52`)과 `coverage`가 된다 |
| `spec.steps[]` | 1-32개, `listType=map` key `name`. `name`(DNS label), `kind`(`power`, `prepare`, `idle`, `remove`). `power`는 `powerMode`, `prepare`와 `remove`는 `batchEnergyOptimization`(같은 namespace의 BEO 이름), `idle`은 `idleDurationSeconds`(≥ 1)만 갖는다(CEL) |
| `status` | `phase`(`Measuring`, `Complete`, `Insufficient`, `Failed`), `reason`, `steps[]{name, key, verdict}` |

**실행.** 승인(5장)과 같은 `tickMu` 안에서 노드 run을 등록하고 `phase: Measuring`을 쓴다. 순서 ID는 CR UID다.
1. 단계마다 `MeasurementRequest`를 만든다. `Operation.ID = <UID>.<순번>.<name>`, meter와 store는 비워 노드 설정으로 채운다.
   `power`는 `nodePowerTargets(node, classes, policies)[powerMode]`와 `powerRequest`(`admission.go:453,458`)로 NodeActuation을 만든다.
   `prepare`는 BEO의 현재 UID로 만든 profile ID(`catalog.go:385`)의 preparation(`profiles.go:167-185`), `remove`는 같은 preparation의 인스턴스다.
2. 기존 `Measure`를 차례로 부른다. 기록 key, 형식, witness는 그대로다(`cost.go:33-35`). 단계가 끝날 때마다 `status.steps`에 key와 verdict를 더한다.
3. 실패나 `Accepted`가 아닌 단계에서 멈추고 기존 순서 기록(`serving/operation-sequences/<UID>.json`)을 쓴다. 이어서 기존 반환이 돈다.

결과: 모든 단계 `Accepted`면 `Complete`. 저장된 단계 기록이 `Accepted`가 아니면(coverage, `PowerChangedDuringMeasurement`) `Insufficient`와
그 기록의 reason. 거절(5장), 단계 실행 오류, ChillServer 재시작은 `Failed`와 거절 사유, 오류 첫 줄, `ChillServerRestart`다.
비용 수집(`CostEvidence.Refresh`)은 같은 prefix를 읽으므로 그대로이고, 끝나지 않은 순서의 기록은 `SequenceIncomplete`와 함께 쓰인다.

## 3. DispatchOccupancyMeasurement (`occupancy`)

같은 group과 규칙이다. printer column: Phase, Node, PerRequest(`.status.perRequestNanos`), Reason, Age. `spec.node`도 같다.

| 필드 | 형식 |
|---|---|
| `spec.batchEnergyOptimization` | 잴 option의 BEO. runtime image와 model은 그 profile의 `condition.runtimeImage`, `condition.logicalModel`이다 |
| `spec.workload` | 요청 경로를 줄 Workload 이름. 그 Dataset의 `evaluation` 부분집합을 보낸다 |
| `spec.batch` | 고정 배치 크기 B(≥ 1, profile에 있는 batch). 인스턴스를 `fixedBatchSize: B`로 준비한다(`SCH-35`) |
| `spec.rateFactor` | 소수 문자열 ≥ 1.2. 보내는 요청률 = rateFactor × B / (profile 배치 B 평균 실행시간) |
| `spec.warmupSeconds`, `durationSeconds`, `minimumBatches`, `maximumIdleFraction` | 창 앞에서 뺄 시간, 창 길이(≥ 60), 창 안 배치 수 하한, 유휴 배치 비율 상한(소수 문자열) |
| `spec.workers` | load generator `--workers`. 큐가 차 있으면 요청 지연이 `maxQueueSize / X`이므로 rate × 그 지연 이상이어야 한다 |
| `status` | `phase`(2장과 같은 넷), `reason`, `perRequestNanos`, `throughput`(소수 문자열, req/s), `result`(`ProfilingRunEvidenceReference`) |

**실행.** 노드 run 하나가 다음을 차례로 한다. 모든 동작은 measurement 모드 `Operations`(taint toleration)다.
1. 노드가 profile 모드가 아니면 2장의 방법으로 `ChangePower`한다(비용으로 기록하지 않는다).
2. preparation을 `Owner = Workload`, `fixedBatchSize = B`로 prepare한다. owner가 있는 준비 Pod는 routing annotation
   `{<Workload UID>: 1}`을 받아 바로 그 Workload를 서빙한다(`operations.go:836-849`, `scheduler_test.go:421-425`). taint 노드에서도 같다 `[추론]`.
3. load Job을 만들고 끝날 때까지 기다린다(상한 `--operation-timeout`). 그동안 ChillServer는 자기가 쓰는 requests stream의
   final 행(`observation_log.go:54-95`) 중 그 인스턴스의 행을 run 안에 모은다. 끝나면 인스턴스를 remove하고 Job을 지운 뒤
   record와 status를 쓴다. 이어서 기존 반환이 돈다.

**load Job.** release namespace의 `occupancy-<UID hash>`, label `chill.dacs.io/measurement-load: <Node UID>`. 반환이 그 label의 Job을
지우므로(runner Service와 같은 방식) CR이 삭제되어도 남지 않는다. Pod는 측정 toleration이 없어 측정 노드에 가지 않는다. init `dataset`은
Dataset image에서 `sh -ec 'cp -R "$1"/. /data/' -- <path>/evaluation`(preprocessor 전달과 같은 형태, `operations.go:411`)이고, `load`는
`chill-load-generator` image의 `chill-loadgen replay --endpoint https://<serving entrypoint Service>:<port> --token-file <projected token>
--ca-file <entrypoint TLS Secret의 tls.crt> --workload <workload> --dataset-dir /data --fields <Dataset fields> --rate <요청률>
--duration <warmup + duration> --arrivals poisson --seed 0 --workers <workers>`다. ServiceAccount는 `<fullname>-load-generator`, image는
`load-generator/`를 담은 `build/docker/load-generator.Dockerfile`, Helm 값은 `loadGenerator.{image, nodeSelector, resources}`다.

**계산.** 배치 시작 = `receivedAt + runningNanos`. 창은 첫 배치 시작 + warmup부터 마지막 배치 시작까지다. 창 안에서 `e1.sql`
포화 표(`:26-37`)와 같은 식을 쓴다: 실행 수 = (최대 batchID − 최소 batchID) × B, X = 실행 수 / 창 초,
`d = 1/X − mean(profile batches[B].durationNanos)/B`. 유휴 배치는 다음 배치 행 중 가장 이른 `receivedAt + queuedNanos`가
그 배치 행의 가장 늦은 `receivedAt + executedNanos`보다 뒤인 배치다. 두 시각이 없는 쌍은 세지 않는다.

결과: 판정을 통과하면 `Complete`이고 d < 0도 그대로 기록한다. 창 배치 수 < `minimumBatches`, 유휴 비율 > `maximumIdleFraction`,
witness 오류는 `Insufficient`와 `TooFewBatches <n>`, `NotSaturated <비율>`, `PowerChangedDuringMeasurement`이고 record를 쓴다.
거절(5장), prepare나 remove 오류, Job 실패나 시간 초과(`LoadFailed <Job 상태>`), ChillServer 재시작은 `Failed`이고 record가 없다.

## 4. 계획기의 d 수집

- `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`를 같은 방식(prefix 목록, 불변 identity cache)으로 읽는다. `Accepted` record 중
  (Node UID, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 쓴다(동률은 key 순, `latestBy`와 같은 규칙).
- 결과는 지금 행 입력 형식 `serving.DispatchOccupancy{node, runtimeImage, model, perRequestNanos, source}`이고 source는 record key다.
  `perRequestNanos`는 max(d, 0)이다(사용자 결정 A7). 음수 d는 record와 그 key로 보인다.
- `NewCapacityInput`(`capacity.go:102`)은 이 목록에서 찾는다. 행 key가 항목을 담으므로 새 측정이 그 행을 다시 계산하게 한다.
  `settings.dispatchOccupancy`와 그 검증, sample, Dashboard 타입은 지운다(9장).

## 5. M3 admission 변경

두 kind는 루트 표(`admission.go:45-92`)에 더하는 측정 루트다. 대상은 `spec.node`, terminal은 `Complete`, `Insufficient`, `Failed`다.

- **노드별 엄격한 순서.** 대기 루트가 어떤 이유로든 기다리면 그 노드를 `claimed`(`:183,199`)로 둔다. 앞 루트가 기다리는 동안 뒤 루트가 승인되지 않는다.
- **측정 Pod.** 거절 줄 3(`NodeServing`)은 측정 taint toleration이 없는 serving Pod, 즉 계획기와 수동 실행 Pod만 센다. taint 없는 노드에 toleration Pod가 있으면(운영자가 taint를 손으로 뗀 경우) Admit가 그 노드의 반환 run을 시작해 지우고, 대기 루트는 기다린다.
- **승인 전 확인(새 거절 사유).**

| kind | 사유 |
|---|---|
| BEO, MLIB | `CPUsUnavailable <CPU> <모드>`: `cpuAffinity`가 가용 CPU의 부분집합이 아니거나, affinity가 없으면 `cpuQuota`가 가용 CPU 수보다 큼 |
| opcost | `ProfileMissing <BEO>`(BEO가 없거나 `Complete` 밖의 terminal. `Complete`인데 catalog에 아직 profile이 없으면 기다린다), `PowerModeUnknown <모드>`, `MeterUndeclared` |
| occupancy | `ProfileMissing <BEO>`(opcost와 같음), `WorkloadMissing`, `WorkloadAutonomous`(`autonomousWorkloads`에 있음), `WorkloadServing <인스턴스>`(그 Workload의 인스턴스가 있음), `LoadGeneratorUnconfigured` |

- 모드는 `spec.state.candidate.powerMode`, 가용 CPU = 그 모드의 online CPU − 설정 `reserved`. 모드의 online CPU는 DeviceClass `powerModes[].onlineCPUs`(새 선택 필드, cpuset 목록)이고, 없으면 Node annotation `edge.dacs.io/cpus`다. Jetson DeviceClass는 모드마다 이 필드를 갖는다. CEL은 Node와 ConfigMap을 읽지 못하므로 확인은 admission이 한다. 캠페인 생성기는 걸러 내지 않는다. 거절은 일을 시작하기 전의 terminal이고 사유가 남는다.
- **승인과 실행.** 두 kind는 taint를 붙이고 `Granted`를 쓴 뒤 같은 잠금 안에서 run을 등록한다. 거절이면 ChillServer가 status도 `Failed`와 사유로 쓴다(다른 reconciler가 없다).
- **동시 부하 하나.** occupancy는 다른 occupancy run이 돌고 있으면 기다린다. 요청이 모두 ChillServer gateway(CPU limit 500m)를 지나므로 동시 부하는 서로의 포화를 깬다. `ponytail:` 클러스터에 부하 하나. gateway CPU가 동시 부하에서 `NotSaturated`를 내지 않음을 재면 푼다.
- **재시작.** taint가 살아 있고 `Granted`인 두 kind의 루트에 등록된 run이 없으면 끊긴 측정이다. `Failed: ChillServerRestart`를 쓰고, opcost는 순서 기록(reason `ChillServerRestart`, `recordRestartedSequence`)을 쓴 뒤 반환한다. taint만 있고 `Granted`가 없으면 PMTC처럼 반환하고 다시 대기한다. 재개는 하지 않는다. 같은 spec을 새 이름으로 만든다.

## 6. RBAC

- ChillServer(`serving_entrypoint_rbac.yaml`): ClusterRole에 두 kind `list`, profile namespace Role(`:200-202` 옆)에 두 kind와 `/status`의
  `patch`, release namespace Role에 `jobs` `create, get, list, delete`를 더한다. nonResourceURL `/chill/api/v1/measurements/operations`를 지운다.
- `<fullname>-load-generator` ServiceAccount와 ClusterRole(nonResourceURL `/chill/api/v1/workloads/*` `post`), binding을 더한다.
- Profiler RBAC은 그대로다. 두 kind의 writer는 ChillServer 하나다.

## 7. 저장 계약 (ADR 0019)

| 대상 | 변경 | 처리 |
|---|---|---|
| CRD `operationcostmeasurements`, `dispatchoccupancymeasurements`, DeviceClass `powerModes[].onlineCPUs` | 새 kind `v1alpha1`, 선택 필드 | 기존 DeviceClass object 그대로 읽힌다 |
| operation 측정 `serving-operation-measurement.v1alpha2`, 순서 `serving-operation-sequence.v1alpha1` | 그대로. 순서 ID가 CR UID | 기존 `seq-` 기록도 같은 reader로 읽힌다 |
| `serving/dispatch-occupancy/<Node UID>/<CR UID>.json` | 새 불변 object `chill.dacs.io/dispatch-occupancy-measurement.v1alpha1`: `{node, runtimeImage, model, profileID, batch, profileMeanNanos, offeredRate, window{start, end}, batches, throughput, perRequestNanos, idleFraction, loadJob{name, image}, isolation, verdict, reason, measuredAt}` | `PutImmutable` |
| 설정 `dispatchOccupancy`, 판단 기록 `v1alpha5` 행 입력 | 설정 삭제, 행 입력 형식 그대로(source가 record key) | decode가 strict(`scheduler_service.go:118`)라 같은 배포에서 ConfigMap의 key를 지운다 |

## 8. 캠페인 manifest

캠페인은 lane 파일마다 `kubectl apply -f` 한 번이다. 노드끼리는 ChillServer의 노드 run이 동시에 돈다. 대기 순서는
`creationTimestamp`(1 s 해상도), 이름 순이므로 이름을 `<노드>-<세 자리 순번>-<내용>`으로 붙인다. 감시는
`kubectl get beo,idlebase,opcost,occupancy -w`와 annotation `chill.dacs.io/admission`이다. 아래는 `jetsono-010-plc-cuda-maxn`
BEO(지금 BEO spec 그대로) 뒤에 같은 파일에 이어지는 두 루트다.

```yaml
apiVersion: edge.dacs.io/v1alpha1
kind: DispatchOccupancyMeasurement
metadata: {name: jetsono-020-plc-occupancy, namespace: chill-system}
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

## 9. 커밋 단위와 삭제

| # | commit | 내용 | 선행 |
|---|---|---|---|
| 1 | `docs(spec): measure dispatch occupancy and queue measurement requests per node` | 0장(00은 로컬 사본) | 사용자 승인 |
| 2 | `feat(api): add operation-cost and dispatch-occupancy measurement kinds` | 두 type, DeviceClass `onlineCPUs`, `make manifests generate`, Helm CRD와 RBAC | - |
| 3 | `feat(serving): queue measurements per node and refuse unallocatable CPUs` | 5장의 엄격한 순서, toleration Pod 규칙, `CPUsUnavailable` | - |
| 4 | `feat(serving): run declared operation-cost measurements` | 루트 등록, 2장 run과 status, 재시작, HTTP route와 `--measurement-config` 삭제 | 2, 3 |
| 5 | `build(loadgen): package the load generator image` | Dockerfile, `make docker-build-load-generator`, Helm `loadGenerator`, ServiceAccount와 RBAC | - |
| 6 | `feat(serving): measure dispatch occupancy at saturation` | 3장 run, load Job, 계산, record, status, 동시 부하 하나 | 2, 3, 5 |
| 7 | `feat(serving): plan with measured dispatch occupancy` | 4장 수집, 설정 삭제, ConfigMap sample, Dashboard | 6 |

- 지우는 것(4): `POST /api/v1/measurements/operations`, `MeasureSequence`, `seq-` 순서 ID와 `c.sequences`, `serving-operation --measurement-config`
  (CLI는 `--execute-config`만 남는다). (7): `SchedulerSettings.DispatchOccupancy`, `dispatchOccupancy()`와 검증(`internal/serving/scheduler.go:41-71,346-353`),
  `config/samples/serving-scheduler.json:16-24`, Dashboard `read-types.ts:39`와 `scheduler.test.ts:24-29`, `scheduler_service_test.go:100-125`의 설정 case.
- 각 commit이 자기가 바꾼 문서를 고친다: `measurement-admission.md` 2.1절 표와 2.5절, `scheduler-operations.md:117-151`,
  `analytic-planner.md:27-34,127,143,155`, `scheduler-implementation.md:111-112`, 이 문서의 색인 `README.md`. 4-7은 배포 하나다. 7의 strict decode 때문에 ConfigMap에서 `dispatchOccupancy`를 같이 지우고, live Jetson DeviceClass에 `onlineCPUs`를 넣는다.

## 10. 검사

| commit | 검사 |
|---|---|
| 2 | CEL: kind별 필드 조합, spec 불변, `rateFactor < 1.2` 거절 |
| 3 | 앞 루트가 `ProfileMissing` 대기면 같은 노드 뒤 루트 대기, 다른 노드 승인. toleration Pod만 있는 노드에서 `NodeServing` 없음, toleration 없는 Pod면 거절. `cpuAffinity` [2,3,4,5]와 `onlineCPUs` 0-3 거절, affinity 없는 quota 4와 예약 [0,1], online 0-5 승인, `onlineCPUs` 없는 모드는 annotation 사용 |
| 4 | power, prepare, idle, remove, power 순서가 한 번 승인되고 key가 `<UID>.<n>.<name>`, status 단계 다섯, `Complete`. 둘째 단계 `Insufficient`면 멈춤, 순서 기록, phase `Insufficient`. 재시작 뒤 `Failed: ChillServerRestart`와 순서 기록, 반환. `ProfileMissing`, `PowerModeUnknown` 거절과 status `Failed`. route 404 |
| 6 | fake 행으로 X와 d(rasp5 26.88 req/s, profile 27.79 ms에서 9.41 ms), warmup 제외, batchID 차이 계산이 빠진 행에 무관, 유휴 쌍 판정, `TooFewBatches`, `NotSaturated`. Workload 요청이 taint 노드의 측정 인스턴스에 도달, Job spec(init, args, toleration 없음, label), Job 실패 `LoadFailed`, 반환이 label Job 삭제, 두 occupancy 중 하나만 실행 |
| 7 | 같은 key의 두 record 중 최신, `Insufficient` 무시, d < 0이면 0과 source key, 맞는 record 없으면 `MissingDispatchOccupancy`, `dispatchOccupancy` 있는 ConfigMap decode 거절 |
| 전체 | `make lint test`, `make manifests generate`, `make helm-lint helm-template`(default와 `values-profiler.yaml`), `make dashboard-check`, `make test-loadgen` |

실클러스터: rasp5 mobilenet q1 occupancy가 m4 r3의 d 9.41 ms와 κ 25.3을 재현한다. 이어서 lane 셋을 동시에 apply하고 노드별 순서, `NodeServing` 0, 재시작 한 번의 `ChillServerRestart`와 다음 루트 진행을 기록한다.

## 11. 사용자에게 묻는 것

1. live의 d 9개는 설정에서 사라진다. 캠페인 d 32개에 이 9개의 occupancy를 더할까? 하나에 약 8분이다 `[추론]`.
2. PLC의 포화 요청률은 수백 req/s다 `[추론]`. gateway CPU 500m에서 하나씩 돌려도 `NotSaturated`가 나면 캠페인 동안 ChillServer CPU limit을 올릴까?
3. load Job은 entrypoint TLS Secret의 `tls.crt`를 CA로 쓴다. live 인증서가 자기 서명이고 SAN에 in-cluster Service 이름이 있는지 확인이 필요하다.
4. 계획기도 모드를 바꾸는 계획의 CPU를 `onlineCPUs`로 확인해야 한다. 지금은 현재 모드의 annotation만 읽는다(`kube/scheduler.go:621`). M4 쪽에서 다룰까?
