# 선언형 operation 비용 측정과 디스패치 점유 측정

캠페인은 CHILL 측정 API를 사용한다. 노드별 승인과 반환은 [측정 admission](measurement-admission.md)을 따른다.
이 문서는 측정량, 조건 생성, 판정과 계획기 소비의 계약을 정한다. 실행 결과와 배포 상태는
[handoff](../../handoff.md)의 실행 근거로 확인한다. 원시 자료, signed 값과 원래 판정은 보존한다.

## 1. 측정 원칙

- operation 비용은 노드의 준비, 유휴, 해제와 빈 노드 전력모드 변경을 순서 있는 요청 하나로 측정한다.
  동작마다 불변 기록을 남기고, 각 기록은 그 요청과 실제 physical identity를 가리킨다.
- 디스패치 점유 d는 고정 배치 인스턴스 하나의 물리 실행 간격에서 구한다.
  Node UID, 전력모드, runtime image와 모델이 같은 실행 조건끼리 사용한다.
- 노드 하나의 측정 요청은 요청 순서대로 실행하고, 서로 다른 노드의 측정은 병렬로 진행한다.
  CPU 할당과 가용 자원, profile 근거를 admission에서 확인하고 거절 사유를 기록한다.
- 대량 측정은 대표 한 건의 조건 생성, 실행, 원시 기록, 계획기 선택과 실물 반환을 확인한 뒤 진행한다.
  profile, runtime 또는 측정 계약을 바꾸는 변경에는 영향받는 d, 비용과 baseline 목록을 함께 남긴다.

## 2. OperationCostMeasurement (`opcost`)

`edge.dacs.io/v1alpha1`, Namespaced, status subresource, spec 불변(`self == oldSelf`). printer column: Phase, Node, Reason, Age. `spec.node`는 Node 이름(1-253)이다.

| 필드 | 형식 |
|---|---|
| `spec.observation` | `intervalMilliseconds`, `requestTimeoutMilliseconds`, `maximumOperationSeconds`, `maximumGapMilliseconds`, 필수 `timing`. timing은 문자열 `changeIntervalP95Seconds`, `maximumRequestSeconds`, `maximumReceiptGapSeconds`, `source`로 실측 입력을 고정한다. 원 gap은 결측 민감도 근거다 |
| `spec.steps[]` | 1-32개, `listType=map` key `name`. `name`(DNS label), `kind`(`power`, `prepare`, `idle`, `remove`). `power`는 `powerMode`, `prepare`와 `remove`는 `batchEnergyOptimization`(같은 namespace의 BEO 이름), `idle`은 `idleDurationSeconds`(≥ 1)만 갖는다(CEL) |
| `status` | `phase`(`Measuring`, `Complete`, `Insufficient`, `Failed`), `reason`, `steps[]{name, key, verdict}` |

**실행.** 승인(5장)과 같은 `tickMu` 안에서 노드 run을 등록하고 `phase: Measuring`을 쓴다. 순서 ID는 CR UID다.
1. 단계마다 `MeasurementRequest`를 만든다. `Operation.ID = <UID>.<순번>.<name>`, meter와 store는 비워 노드 설정으로 채운다. `power`는 `nodePowerTargets(node, classes, policies)[powerMode]`와 `powerRequest`(`admission.go:453,458`)로 NodeActuation을 만들고 CR을 owner로 단다. `foreignActuations`(`admission.go:470`)가 루트 UID owner를 건너뛰므로 반환의 무효 판정이 자기 단계를 세지 않는다. `prepare`는 BEO의 현재 UID로 만든 profile ID(`catalog.go:385`)의 preparation(`profiles.go:167-185`), `remove`는 같은 preparation의 인스턴스다.
2. 기존 `Measure`를 차례로 부른다. 기록 key, 형식, witness는 그대로다(`cost.go:33-35`). 단계가 끝날 때마다 `status.steps`에 key와 verdict를 더한다.
3. 실패나 `Accepted`가 아닌 단계에서 멈추고 기존 순서 기록(`serving/operation-sequences/<UID>.json`)을 쓴다. 이어서 기존 반환이 돈다.

실제 operation과 effect, 격리, 서빙 추론 0, 원시 구조, 실제 양쪽 bracket과 확장 support를 확인한 에너지는 `Accepted`다.
정밀도 비율은 보고용이다. 동작 실패, 격리 오류, 실제 boundary 누락과 invalid record는 사유와 부분 근거를 보존한다.
`Failed` 수명주기는 실행 실패를 기록한다. 측정값과 상태는 원래 기록을 유지한다.

### 2.1 전력 곡선과 민감도

실제 동작창 `[a,b]`와 기록된 piecewise-linear 전력곡선 q에서 `Ehat=∫[a,b]q(t)dt`다.
비용은 측정값 `Ehat ± B`로 표현한다. 위상 여유
`A=p95(전력값 변화 간격)+최대 요청 지연+최대 유효 수신 gap`은 원 십진 문자열과 출처로 고정한다.
실행시간 변환은 합을 nanosecond로 올림한다. 승인 실측 추정은 Gen3 1.6079805965 s,
Plus 4.518180026700001 s다. 미측정 Gen3 6개의 계열 전이는 추정의 한계로 남긴다.
동일값 유지 길이와 추가 배수는 A의 입력에 포함하지 않는다.

관측은 `[a−A,b+A]`의 실제 표본을 확보한다.
`S=maxδ∈[−A,A]|∫[a,b]q(t+δ)dt−Ehat|`를 모든 knot와 구간 내부 stationary 극값에서 계산한다.
상수 전력에서는 S=0이고 상수 offset을 더해도 S는 같다.
실패하거나 놓친 요청을 포함한 원 gap과 `maximumGap`을 넘은 valid-to-valid 원 gap 중
확장 support와 교차하는 구간을 골라, 원래 전체 구간폭의 합집합을 한 번 계산한다.
`G=이번 record의 최대 관측 W×min(b−a,합집합 길이)`, `B=S+G`다.
gap의 원래 폭을 유지하며 A 확장은 support에 한 번 적용한다.
G는 미관측 전력이 이번 최대 관측값 안에 있다는 경험적 조건을 가진다.

운영 POOL 전력 창은 원시 성공 표본을 합쳐 기록된 coverage 정책으로 판정한다.
읽기 실패 자체는 불완전 사유가 아니며 성공 표본 사이의 gap으로 남는다.
현재 운영 정책은 MaximumGap 3.0 s, MinimumCoverage 0.75, MaximumBoundaryDistance 1.0 s다.
노드별 `readFailures`는 창 안 실패 횟수, `maximumSampleGapSeconds`는 경계 support를
포함한 인접 실제 표본의 최대 간격이다. 창 요약은 각각 노드 합계와 노드 최대값이다.
따라서 실패가 있어도 정책을 만족하면 complete이며, 정책을 넘는 gap은 불완전이다.
이는 단발 읽기 실패의 실측 공백이 2.52 s 이하이고 연속 두 번 실패 공백은 3.2 s 이상이라는
운영 근거에 따른 설정값이다. 정확 비용 등 다른 측정의 정책은 변경하지 않는다.
오프라인 `hack/observations/power --maximum-gap 3s`는 보존 정책을 수정하지 않고
명시적 재평가만 수행하며 출력의 `policyOverride.maximumGapSeconds`로 이를 표시한다.

`energyUncertaintyJoules`, `phaseSensitivityJoules`, `missingGapJoules`는 각각 B, S, G다.
`energyUncertaintyRatio`는 `B/abs(Ehat)`의 보고값이며 Ehat=0 또는 nonfinite 비율은 undefined로 표시한다.
10% 지표는 보고용이고 Accepted/Insufficient 판정에 사용하지 않는다.
`request.timing`과 `operationBudgetNanos`에 조건을 보존한다.
이 민감도는 관측 support와 Pmax를 전제로 하며, 센서 교정이나 전체 물리 오차의 95% 구간을 보장하지 않는다.

### 2.2 계획기 불확실성

`OperationTrial.energyUncertaintyJoules`와 `NodeBaseline.wattsUncertainty=B/실제 측정초`를 cost projection에 전달한다.
nominal 평균은 유지한다. `SchedulerNodePlan.energyUncertaintyJoules`는 사슬 민감도이고
`SchedulerCapacity.gainUncertaintyJoules`는 B-only minimumGain 문턱이다.

같은 immutable reference와 trial index의 B/N은 발생 횟수와 부호를 누적한다.
keep−best에서 같은 근거를 먼저 상쇄하고, 서로 다른 근거의 잔여 절댓값 합을 B-only 문턱으로 사용한다.
nominal 평균은 유지한다. 이득이 문턱보다 클 때 전환하고 같으면 keep이며, keep infeasible의 회복 경로를 유지한다.
`unloaded+Σ(loaded−unloaded)`의 unloaded 계수는 `1−instanceCount`이고 exact loaded baseline의 계수는 1이다.
target-baseline-delta는 저장된 차감 nominal을 한 번 복원하며 B는 wall 민감도다.
이 문턱은 관측 support/Pmax 조건의 위상-결측 민감도다.
profile 통계오차, BEO idle fallback CI, 시점 drift와 global95% gain 보장은 포함하지 않는다.
원시 profile 분산/CI와 d의 95% 분석은 각각의 추정 계약에 보존한다.

## 3. DispatchOccupancyMeasurement (`occupancy`)

같은 group과 규칙이다. printer column: Phase, Node, Mode, PerRequest(`.status.perRequestNanos`), Reason, Age. `spec.node`도 같다.

| 필드 | 형식 |
|---|---|
| `spec.batchEnergyOptimization` | 잴 option의 BEO. 전력모드, runtime image, model은 그 profile의 `condition.powerMode`, `runtimeImage`, `logicalModel`이다 |
| `spec.workload` | 요청 경로를 줄 Workload 이름. 그 Dataset의 `evaluation` 부분집합을 보낸다 |
| `spec.batch` | 고정 배치 크기 B(≥ 1). 인스턴스를 `fixedBatchSize: B`로 준비한다(`SCH-35`) |
| `spec.supplyMode` | `closed-loop` 고정 outstanding 공급과 `poisson`을 구분한다. d는 closed-loop, E5 부하는 Poisson을 사용한다. `workers=K`, 대기 batch 수 q와 계산 입력을 conditions에 고정한다 |
| `spec.warmupSeconds`, `durationSeconds`, `minimumBatches`, `maximumIdleFraction` | profile에서 계산한 부하 warmup W_load, 본창 T, 표본 N과 식별된 idle의 허용 비율이다. `maximumIdleFraction`은 [0,1] 범위 검사하는 설정값이며 승인값은 0.05다(고정값 검증 아님). 동결 계수 `maximumIdleBaseFraction`의 승인 상한도 0.05이며 profile로 더 엄격한 idle 비율을 역산하지 않는다. closed-loop의 `loadPreparationFloorSeconds`, `windowFloorSeconds`, `plannedElapsedCapSeconds`는 `conditions.inputsJSON.coefficients`에서 한 번 동결해 소비하며 승인값은 60/360/3600 s다. preparation 예산은 별도다. legacy Poisson의 duration 60 s는 독립 구조 계약으로 유지한다 |
| `spec.requestLifetimeSeconds`, `deliveryMarginMillis`, `drainSeconds`, `preparationSeconds`, `jobSetupSeconds`, `collectionSeconds`, `plannedElapsedSeconds` | profile, SLO, 실제 준비, batch tail, 전달과 수집 예산에서 계산한다. L≥SLO, gateway cap≥L과 native count/decoded bytes의 공동 조건을 만족한다. socket은 d와 E5 모두 L+delivery보다 엄격히 커야 하며 최소 1 ms를 더한다 |
| `spec.conditions` | `profileID`, `inputsJSON`, `waitingBatches`, `maximumIdleNanosPerRequest`, `minimumCycles`에 정확한 profile, 모든 입력, 승인 계수, 계산 q, idle 1 ms/요청과 필수 cycle을 불변 고정한다. warmup과 본창 하한, 전체 cap의 설정 원천도 같은 입력에 동결한다 |
| `status` | `phase`(2장과 같은 넷), `reason`, `powerMode`, `perRequestNanos`, `throughput`(소수 문자열, req/s), `result`(`ProfilingRunEvidenceReference`) |

**실행.** 노드 run 하나가 다음을 차례로 한다. 모든 동작은 measurement 모드 `Operations`(taint toleration)다.
1. 노드가 profile 모드가 아니면 2장의 방법(CR owner NodeActuation)으로 `ChangePower`한다. 비용으로 기록하지 않고, witness 구간은 이 단계 뒤에 시작한다.
2. preparation의 `Owner`는 Workload이고 fixed batch B로 prepare한다.
   owner가 있는 Pod는 해당 Workload UID의 routing annotation을 받아 측정 요청을 처리한다.
3. 계산된 공급, 수명, warmup, 본창, drain과 수집 예산으로 load Job을 실행한다.
   완료 native batch 원장은 기존 JSONL의 d 전용 scanner로 집계한다.
   전체 cohort 배열이나 전체 Query를 만들지 않고 조건과 모든 과학 판정 사유를 기록한 뒤 반환한다.
   공유 observation reader는 조회 bounds를 UTC hour key로 정규화하여 호출자의 Local/UTC location 차이가 coverage 공백이나 `ObservationRetentionGap`을 만들지 않게 한다.

**load Job.** release namespace의 `occupancy-<UID hash>`, label `chill.dacs.io/measurement-load: <Node UID>`.
- `backoffLimit:0`과 명시한 유한 Job 예산을 사용한다.
  deadline은 setup, warmup, 본창, physical drain, delivery와 collection을 포함한다.
  closed-loop 공급 시간은 `warmupSeconds + durationSeconds + requestLifetimeSeconds + deliveryMarginMillis/1000`이며,
  generator 시작과 첫 물리 batch 사이의 전달 지연만큼 창 끝 공급이 끊기지 않게 한다.
  Job deadline은 이 공급 시간을 초 단위로 올림한 뒤 setup, drain, collection을 더한다.
  전체 elapsed 합은 `preparation + setup + warmup + duration + ceil(L + deliveryMarginMillis/1000) + drain + collection`
  이며 `plannedElapsedSeconds` 이하이고, 동결된 전체 cap(승인값 3600 s)을 넘지 않아야 한다.
  생성기는 공급 연장 예약 `supplyExtensionSeconds=ceil(L+deliveryMarginMillis/1000)`를 known 합과
  새 `conditions.inputsJSON.computed`에 포함하고, 미관측 setup/collection 잔여에서 그만큼 차감해 합을 3600 s로 맞춘다.
  기존 record/terminal CR와 이미 동결한 입력은 다시 쓰지 않는다.
  물리 batch 기준 warmup/본창 계산과 Poisson 공급 시간은 바꾸지 않는다.
  HTTP 만료 뒤 계속되는 native 실행의 미완료 근거는 unknown으로 보존한다.
  반환은 해당 label의 Job을 Background 삭제해 부하 Pod를 정리한다.
- `nodeName`은 ChillServer Pod의 노드(downward API `spec.nodeName`, env `NODE_NAME`)다. `nodeName`은 scheduler를 거치지 않아 NoSchedule taint를 보지 않는다. 그 노드는 gateway가 있는 노드라 부하가 다른 측정 노드의 전력에 섞이지 않는다.
- init `dataset`은 Dataset image에서 `sh -ec 'cp -R "$1"/. /data/' -- <path>/evaluation`(preprocessor 전달과 같은 형태, `operations.go:411`)이다.
- Workload/Dataset 원시 입력을 공통 TLS gateway로 보낸다.
  d는 closed-loop K와 L header/socket 여유를 사용하고 E5는 Poisson을 사용한다.
  실제 FQDN, CA, projected token과 기존 release ServiceAccount를 사용한다.
- ServiceAccount는 `<fullname>-load-generator`, image는 `load-generator/`를 담은 `build/docker/load-generator.Dockerfile`이다. base는 Python 3.12로 고정한다. live 인증서에 Subject와 Authority Key Identifier가 없어 3.13 이상의 기본 strict 검증이 거절할 수 있다 `[추론]`.

**물리 측정량.** `d=연속 native batch 시작 span/(physical cycles×B)−profileMean/B`의 signed 전체 값을 보존한다.
물리 batch는 Node, Pod UID, container ID, BatchID와 member로 연결한다.
readiness는 B개 member의 queued 시각 중 최댓값이고 actor 대기는 `runningNanos−queuedNanos`다.
직접 native timestamp와 legacy offset은 원천을 구분한다. HTTP 결과와 실제 실행은 각각 기록하며,
HTTP 실패 뒤 실행된 batch도 계산에 포함한다. compute gap 전체를 idle로 차감하지 않는다.

**조건 생성과 동결.** 상대 계획 정밀도 1%, 양측 95%, 공통 여유 1.25와 idle 기여 1 ms/요청을
실측 profile의 batch별 분산, trial 경계, 준비 aggregate와 Workload SLO에 적용해 N, L과 창을 계산한다.
N은 physical batch 수이며 cycle 수는 N−1이다. 같은 batch의 member는 독립 표본으로 중복 계산하지 않는다.
고정 outstanding closed-loop 공급의 workers K는 다음 최대 cap으로 한 번 정한다.
`K=min(64,floor(queueBytesCap/framedDecodedRequestUpperBytes),floor((L−margin)/(B×mu)))`.
B는 profile 배치 크기, mu는 profile의 배치 평균 실행시간(초), L은 기존 수명 계산으로 정한
요청 수명(초), margin은 기존 `deliveryMarginMillis/1000`이다.
`queueBytesCap=67108864`이며 `framedDecodedRequestUpperBytes`는 framing을 포함한 요청별 decoded bytes 상한이다.
K는 worker 수이자 outstanding 요청 수이며 B의 배수로 올림하지 않는다.
기존 validator의 대기 batch 수는 `q=ceil(K/B)−1`로 계산한다.
`conditions.inputsJSON.computed.supplySizing`에는
`{batch,profileMeanBatchSeconds,requestLifetimeSeconds,marginSeconds,workerCap:64,queueBytesCap:67108864,framedDecodedRequestUpperBytes,limitingBound}`를 동결하고
generator, API와 controller는 이 입력으로 K와 q를 정확히 재계산한다.
`limitingBound`는 최소값을 정한 `queue-cap`, `bytes-cap`, `lifetime` 중 하나이며
동률이면 이 순서에서 먼저 나온 항목을 기록한다.
전달 margin은 측정하는 외부 시간이 아니라 기존 전달 예산을 남기는 reserve다.
공급량을 정하기 위한 왕복시간·외부 시간 측정이나 별도 출처 입력은 사용하지 않는다.
L은 이 공급 규칙 적용 전의 기존 수명 그대로이며 K를 늘리기 위해 확장하지 않는다.
기존 q/batch-tail/prefix 수명 검사, native waiting count와 decoded bytes의 공동 조건,
SLO와 gateway 수명 상한을 유지한다. bytes 상한 초과를 이유로 큰 K의 CR을 만든 뒤 거절하는 대신
위 세 상한의 최솟값으로 K를 생성한다. 공급은 K<B일 때만 불가능하며 실행 전에 보고한다.
그 밖의 실행 조건이나 전체 계획 cap을 만족하지 못해도 실행 전에 보고하며 L이나 예산을 늘리지 않는다.
계수와 하한/상한은 한 설정 원천에서 읽고, generator, API, admission과 controller는 동결된 같은 조건을 소비한다.
준비 시간과 부하 warmup의 의미와 예산은 각각 기록한다.

**수명.** admission은 요청 L을 실제 `HandlerConfig.RequestTimeout` 상한과 대조하고 초과 CR을 실행 전에 거절한다.
caller는 L과 delivery 여유를 명시하고 `socketTimeoutMillis >= L*1000+deliveryMarginMillis+1`로 설정한다.
gateway는 원래 L과 sender 경과를 전달하고 native는 `L−senderElapsed−completionMargin`을 사용한다.
완료 여유 m은 한 번 차감한다. 이 규칙은 E5 Poisson 호출에도 적용한다.
기존 유한 physical drain과 전체 3600 s 예산(`e8e018e2`)을 유지하고,
`supplyExtensionSeconds=ceil(L+deliveryMarginMillis/1000)` 예약을 보존한다.
예산을 맞출 수 없으면 명시적으로 보고하며 공급 연장을 줄이거나 cap을 넘겨 실행하지 않는다.
기존 Accepted record와 동결 입력은 불변이며 기존 측정을 묵시적으로 재실행하지 않는다.
새 K는 Accepted10의 K와 같아야 하는 불변량이 아니다. 기존·새 결과 비교는 실제 근거로 수행한다.
유휴 비율은 설정된 `maximumIdleFraction` 이하이어야 한다. 승인값 0.05는
계획 여유 25%(1.25−1)의 1/5이며 signed d의 정의·보존은 바꾸지 않는다.
상한 초과면 producer 또는 요청 경로의 한계로 보고한다. K, L, margin이나 cap을 튜닝하거나
자동 재측정하여 통과시키지 않는다.

**판정.** physical finished에서 다음 ready까지의 보수 idle 상한과 창 끝단, hole, 누락 native를 함께 평가한다.
필요한 원근거가 없으면 unknown과 모든 관련 사유를 남긴다.
`idleIdentified=true`는 physical batch 연속성과 실행 근거가 완전함을 요구한다.
`idleIdentified=false`일 때 `idleUpperNanos`는 알려진 pair의 부분 합이며 통과 근거가 아니다.
unknown, hole, 누락 native와 불완전 실행은 기존대로 탈락한다.
`idleNanosPerRequest`는 같은 physical cycle과 endpoints를 사용하며 기존 1 ms/요청 상한을 유지한다.
식별된 유휴에 대해서만 `dRange=[perRequestNanos−idleNanosPerRequest,perRequestNanos]`를
나노초 단위로, `kappaErrorBound=idleFraction`을 κ 상대 오차 상한으로 기록한다.
미식별이면 두 필드는 생략한다. d에서 유휴를 차감하지 않는다. 원 d는 유휴로 커지고
κ는 작아지는 보수적 방향이며, 범위와 상한은 95% CI나 전체 모델 오차 보장이 아니다.
`cycleMeanNanos`, `cycleSDNanos`, `cycleRelativeHalfWidth`, `precisionTargetMet`는 stationary-iid t 근사의 보고 정보다.
N과 창은 시작 전에 고정한다. 실제 CI로 새 거절 gate를 만들거나 N을 자동 변경하지 않는다.
minimum N, 물리 완전성, unknown과 idle 상한을 판정하고 해당 이유 전체를 `reasons[]`에 보존한다.

RequestRow의 `nativeQueuedAt`, `nativeRunningAt`, `nativeFinishedAt`, `nativeExecutionComplete`,
`nativeOutcome`, `nativeError`는 직접 actor 근거다. native `queueMS`는 ingress를 포함한 지표로 해석한다.
HTTP 완료 뒤의 native는 `kind:native` 보충 raw에 남기고 HTTP 완료나 member를 중복 계수하지 않는다.
전용 scanner는 누락 identity와 physical batch를 compact 집계한다.

판정 통과는 `Complete`이며 음수 d도 record에 보존한다.
minimum batches, idle, 물리 근거, unknown과 witness의 모든 해당 사유는 `Insufficient`와 record에 남긴다.
거절, prepare/remove, Job, watchdog과 재시작 실패는 `Failed` 수명주기를 따른다.
초기 Measuring patch와 부하 시작 전 실패의 terminal status는 취소된 run과 분리한 최대 30 s publication context로 저장한다.
실제 handback은 parent context를 사용하며 status 저장 예산은 측정창을 확대하지 않는다.

**근거 지위.** mobilenet CPU 8개와 GPU 2개는 새 규칙 미재인증 legacy로 표시하고 실제 대체 Accepted 전까지 소비를 유지한다.
U2의 CPU 8개와 GPU 전체 5개, PLC 12개의 새 규칙 측정은 D2 뒤 25개 범위다.
D01의 Insufficient와 `rasp5-944` 진단은 원래 상태로 보존한다.
U3의 D04/D09 새 immutable record는 실제 소비자 source를 대체하며 옛 Smol CPU 2개의 raw와 status는 보존한다.

**Smol 해석.** D04/D09/D14/D23/D33의 profile 포함 95% 근사구간은 0 아래에 있으며,
E5에서 알려진 systematic profile-versus-serving bias로 해석한다.
profile/NodeUID/model/B1/mode/runtime 참조 일치는 관측 사실이다.
profiling과 serving의 harness, warmup, timing envelope와 시간 분리도 관측 차이다.
실효 thread/affinity, 입력과 전처리 경계, 열/주파수/throttling, clock의 원인별 기여는 미확정 가설이다.
signed raw, 원 Accepted와 계획기의 `max(d,0)` 정책을 유지한다.
bias 보정과 d 추정정책 대안은 M5 후보로만 다룬다.
정확한 수치와 원시 연결은 [bias JSON](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.json)과
[해석 근거](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.txt)에 있다.

## 4. 계획기의 d 수집

- `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`를 같은 방식(prefix 목록, 불변 identity cache)으로 읽는다. `Accepted` record 중 (Node UID, 전력모드, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 쓴다(동률은 key 순, `latestBy`와 같은 규칙).
- 결과는 `serving.DispatchOccupancy{node{name, uid}, powerMode, runtimeImage, model, perRequestNanos, source}` 목록이고 source는 record key다. `perRequestNanos`는 max(d, 0)이다(사용자 결정 A7). 음수 d는 record와 그 key로 보인다.
- 목록은 `config.Costs`, `config.Baselines`처럼 controller 설정을 거쳐 snapshot `dispatchOccupancy`에 실린다(`kube/scheduler.go:493`). `dispatchOccupancy(i)`는 snapshot으로 옮기고 `i.Node.UID`, `i.Profile.Condition.PowerMode`, runtime image, model이 모두 같은 항목을 찾는다. 두 소비자 `NewCapacityInput`(`capacity.go:102`)과 재생(`scheduler_replay.go:564`)이 이것을 쓴다. 행 key가 항목을 담으므로 새 측정이 그 행을 다시 계산하게 한다.
- 잰 적 없는 모드의 option은 `MissingDispatchOccupancy`다. 같은 노드, 모드, runtime image, model의 option끼리 d를 함께 쓴다.

### 4.1 observer와 coherent replacement

`Observer.ObserveUntil(ctx,Request,end,onFirstSample)`는 실제 producer end 뒤 final read까지 수집한다.
Duration은 유한 watchdog이고 최대 표본 예산은 `floor(watchdog/interval)+3`이다.
standalone `--end-wait-timeout`은 `RunConfig.Metrics.DurationSeconds`,
`--max-result-bytes`는 frozen policy의 `MaxMetricResultBytes`를 사용한다.

profiler는 별도로 주입한 `mgr.GetAPIReader`로 recorded Job 존재를 조회한다.
실제 API NotFound는 `ExecutionLost`, transport 오류는 retry다.
cache의 일시 NotFound는 replacement 생성 근거로 사용하지 않는다.
BEO annotation `chill.dacs.io/replaces-batch-energy-optimization`은 같은 namespace의 원본 이름을 가리킨다.
replacement pending 동안에도 원본의 catalog 사용을 제외하고 raw/status는 보존한다.
대체 BEO는 정상 verifiedComplete와 같은 state/CPU/batch/exact trial 계약을 충족한다.

012/017의 coherent replacement를 사용하고 원 trial과 status를 보존한다.
원본017의 Granted/Measuring은 비실행 보존 예외다.
실제 execution/serving Pod/power pending 0, 정확 NodeUID/RV/holder와 orphan runtime 상태를 확인한 뒤
CampaignOps가 해당 old UID의 stale 측정 taint만 정리한다.
root, status, Runs, retained Jobs와 raw는 보존하고 replacement는 정상 admission으로 진입한다.
observer 검사는 실제 late producer의 tail/final sample/event/result byte 근거를 연결한다.


## 5. 측정 admission

두 kind는 루트 표(`admission.go:45-92`)에 더하는 측정 루트다. 대상은 `spec.node`, terminal은 `Complete`, `Insufficient`, `Failed`다.

- **노드별 엄격한 순서.** 대기 루트가 어떤 이유로든 기다리면 그 노드를 `claimed`(`:183,199`)로 둔다. 앞 루트가 기다리는 동안 뒤 루트가 승인되지 않는다. 그래서 기다림은 끝이 있어야 한다. 아래 확인은 끝나지 않을 대기를 거절로 바꾼다.
- **측정 Pod.** 승인된 taint 노드의 대기 루트는 측정 인스턴스가 있어도 기다린다.
  `NodeServing`은 taint 밖의 계획기와 수동 실행 Pod에 적용한다.
- **승인 전 확인과 거절 사유.**

| kind | 사유 |
|---|---|
| BEO | `CPUsUnavailable <CPU> <모드>`: `cpuAffinity`가 가용 CPU의 부분집합이 아님. `OnlineCPUsUnknown <모드>` |
| opcost, occupancy | `ProfileMissing <BEO> <사유>`: BEO 없음, terminal이 아님(`NotComplete`, 같은 노드에서 앞 순서면 줄 1이 먼저 기다리게 하므로 여기서는 뒤 순서나 다른 노드의 BEO), `Complete`가 아닌 terminal, catalog의 그 BEO issue 코드(`catalog.go:102-104,115,240,300,420`). catalog에 profile도 issue도 아직 없으면(갱신 지연) 기다린다. `NodeMismatch`: BEO `targetNode` ≠ `spec.node`. `ActuationNamespace`: actuation namespace ≠ CR namespace(owner reference는 같은 namespace만 가리킨다) |
| opcost | `PowerModeUnknown <모드>`, `MeterUndeclared` |
| occupancy | `BatchUnmeasured <B>`(profile batches에 없음, `catalog.go:402-403`), `WorkloadMissing`, `WorkloadAutonomous`(`autonomousWorkloads`에 있음), `WorkloadServing <Pod>`(같은 주기에 읽은 serving Pod 중 그 Workload가 owner이거나 route 가중치를 둔 Pod가 있음), `LoadGeneratorUnconfigured` |

- 모드는 `spec.state.candidate.powerMode`, 가용 CPU는 그 모드의 online CPU에서 설정 `reserved`를 뺀 범위다.
  online CPU는 DeviceClass `powerModes[].onlineCPUs`의 cpuset 목록이다.
  필드가 없으면 현재 read-back 모드에만 Node annotation `edge.dacs.io/cpus`를 사용하고,
  그 밖의 모드는 `OnlineCPUsUnknown`으로 거절한다. admission은 Node와 ConfigMap의 실제 조건을 확인한다.
- **승인과 실행.** 두 kind는 taint를 붙이고 `Granted`를 쓴 뒤 같은 잠금 안에서 run을 등록한다.
  거절이면 ChillServer가 status에 `Failed`와 사유를 쓴다.
- **부하 노드 격리.** ChillServer 노드의 루트와 occupancy run은 서로 격리한다.
  서로 다른 측정 노드의 occupancy는 병렬로 진행한다. gateway의 실제 CPU/메모리 설정은 실행 manifest에 기록한다.
- **재시작.** taint가 살아 있고 `Granted`이며 terminal이 아닌 두 kind의 루트에 등록된 run이 없으면 끊긴 측정이다. `Failed: ChillServerRestart`를 쓰고, opcost는 순서 기록(reason `ChillServerRestart`, `recordRestartedSequence`)을 쓴 뒤 반환한다. terminal 루트는 status를 그대로 두고 반환만 다시 한다. taint만 있고 `Granted`가 없으면 PMTC처럼 반환하고 다시 대기한다. 재개는 하지 않는다. 같은 spec을 새 이름으로 만든다.

## 6. RBAC

- ChillServer는 두 측정 kind의 목록과 status patch 권한, release namespace의 Job create/get/list/delete 권한을 가진다.
  actuation namespace의 NodeActuation 권한을 사용하며 같은 namespace의 owner reference를 기록한다.
- `<fullname>-load-generator` ServiceAccount는 공통 gateway의 Workload 요청 경로에 POST 권한을 가진다.
- 두 측정 kind의 status writer는 ChillServer 하나다.

## 7. 저장 계약 (ADR 0019)

단일 저장 계약은 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.
reader는 한 형태를 엄격히 소비한다. Timing이 없는 기록의 미기록 B를 관측한 0으로 투영하지 않는다.
원래 raw, 값과 판정은 외부 과학 증거로 보존한다.

| 대상 | 계약 |
|---|---|
| operation 기록과 순서 기록 | 실제 timing, B/S/G, 보고 비율, operation identity와 단계별 출처를 불변 저장한다 |
| `serving/dispatch-occupancy/<Node UID>/<CR UID>.json` | 전체 공급, 시간, 계수와 identity 조건, native 원천과 모든 과학 판정 사유를 PutImmutable로 보존한다 |
| d 소비 | snapshot `dispatchOccupancy`가 측정 record를 가리킨다. 수동 설정의 d 목록은 사용하지 않는다 |
| 판단 기록 | 행 입력의 Node UID, 전력모드, runtime/model, d source와 실제 비교 기간 T를 보존한다 |
| 실행과 결과의 scheduler 설정 | 선택한 `SchedulerSettings`와 `minimumDemand`를 `decision.settings` 및 `decision.search.bounds`에 동결하고 scheduler 관측 기록에 보존한다. 캠페인 실행은 binding의 scheduler 입력 참조와 settings 캡처를 결과에 연결한다 |

### 7.1 최소 수요와 재현 설정

별도 Run API나 Run 저장소를 추가하지 않는다. 여기서 실행 기록은 기존 캠페인 binding/summary와 scheduler 판단 및 관측 기록이다.

`minimumDemand`는 Workload **UID**별 최소 요청률(req/s) map이다. 이름이나 모델 ID를 키로 쓰지 않는다.
빈 UID, 음수, NaN과 무한대는 거절한다. 선택 항목이며 map 또는 UID를 생략하면 하한은 0이다.
계획기의 유효 수요는 `max(관측 수요, minimumDemand[UID])`이고,
관측 수요는 장기/최근 도착률과 미완료 요청 기여를 포함한다([계획기 3장](analytic-planner.md#3-판정과-순서)).
유휴 제거와 교체 회수의 최소 요청률에도 이 하한을 적용한다.
무부하에서도 선택 구성은 명시한 하한을 처리할 용량을 유지해야 한다.

다음 JSON은 전체 설정 문서가 아니라 scheduler 캡처와 판단에 보존하는 settings 부분의 예다.
`workload-uid-123`은 실제 대상 UID로 정한다.

```json
{
  "settings": {
    "minimumDemand": {
      "workload-uid-123": 40
    }
  }
}
```

판단은 고른 설정을 `decision.settings`와 `decision.search.bounds`에 동결하므로,
이후 controller 설정 변경이 과거 판단의 하한을 바꾸지 않는다. scheduler 관측 기록은 그 판단을 보존한다.
`hack/e5/e5.py`와 `demo.py`의 새 binding은 scheduler 입력 참조와 함께
`schedulerSettings`에 `minimumDemand`를 포함한 설정 전체를 동결한다.
`e5-summary.json`/demo `summary.json`과 immutable 발행 결과의 단계별
`kpis.schedulerSettings`도 같은 동결값을 담는다. 과거 binding에 값이 없으면
null과 누락 사유를 남기고 현재 설정이나 가짜 0으로 채우지 않는다.
실행의 `initial.json`, 경계별 `pre-settings.json`/`post-settings.json`,
`static-selection.json` 및 `decision.settings`는 실제 실행 중 설정을 저장한다.
binding의 값은 입력 동결 시점이며, 이후 변경은 당시 판단과 경계 기록으로 확인한다.
판단의 `decision.capacity.workloads[]`에는 `minimumDemand`(미설정이면 명시적 0)와
`rate`(하한을 적용한 최종 req/s)가 함께 남는다. 관측 도착률 자체를 하한으로 덮어쓰지는 않는다.

G2는 계획기가 읽는 비용과 baseline의 immutable 참조와 Timing 유무를 조사한다.
B=0 값만으로 계약 세대를 판별하지 않는다.
[실제 소비자 목록](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json)은
선택된 catalog 비용 177개 중 Timing 없는 35개, baseline 80개 중 Timing 없는 14개를 확인했다.
이 49개 중 E5 선택 노드의 empty ranking에 참여하는 legacy는 9개이며 불확실성은 unknown이다.
동결된 E5 identity/mode의 후보 에너지 입력에서는 legacy가 0개다.
이는 실행된 E5 선택이나 전체 저장 prefix의 계약 정리 완료를 뜻하지 않는다.
reader는 최신 선택 전에 객체를 읽으므로 known superseded 참조와 terminal CR 참조도 정확한 정리 목록에 포함한다.

G2 승인 범위는 ranking baseline 9개의 idle과 reach-power를 수집하는 2 roots, 18 steps다.
실제 최저선정에 필요한 조건부 직접 전환을 포함한 상한은 4 roots, 22 steps다.
각 root는 raw, 계획기 소비와 정상 반환을 확인한 뒤 다음 root로 진행한다.
9개 대체를 확인한 뒤 검증된 외부 백업의 실제 절대경로와 raw 80개 키, terminal OCM 6개의 개수 및 목록 대조를 보고하고 작업 저장소의 해당 참조를 정리한다.
old ranking 9개는 raw 80개에 포함된다.
동일한 승인 조건의 legacy 또는 결측 대체, 대체 확인, 검증 백업과 옛 기록 삭제는 연속 실행한다.
새 조건, 판정, 범위 확대와 배포는 별도 승인 경계를 따른다.
strict reader와 전용 obsolete 필드 및 비교 테스트의 제거는 수집과 병렬로 같은 영구 소스 변경에서 완결한다.
새 소스의 검증과 적용은 대체 확인, 검증 백업, 정확한 정리 및 Timing 없는 소비 기록 0건의 실제 근거 뒤에 진행한다.
공통 catalog, d, profile, runtime, lowest와 settings 입력을 동결한 뒤 첫 BEFORE 부하를 시작한다.
원본012/017, Smol d, 새 38단계 기록과 runtime7을 보존하며 재측정, 삭제와 reader 제거 실적은 실제 근거로 기록한다.

## 8. 캠페인 manifest

조건 생성은 캠페인 generator가 맡는다.
실측 profile/SLO와 승인 계수로 d의 N/K/L, 준비, 본창, setup, drain, collection을 계산하고
`conditions.inputsJSON`에 입력 ID, 원시 위치와 계산값을 함께 동결한다.
M5에서는 이 계산 책임을 측정 controller로 옮기고 CR 조건 필드는 명시적 재정의에 사용한다.
계수와 판정 변경은 승인된 변경 범위로 관리한다.

대표 한 건의 생성부터 계획기 소비와 반환까지 확인한 뒤 대량 실행한다.
generator와 승인 manifest는 재현 근거이며, 아래 YAML은 Gen3 비용 조건의 형식 예시다.

```yaml
apiVersion: edge.dacs.io/v1alpha1
kind: OperationCostMeasurement
metadata: {name: jetsono-030-plc-cost-maxn, namespace: chill-system}
spec:
  node: jetsono
  observation:
    intervalMilliseconds: 1000
    requestTimeoutMilliseconds: 800
    maximumOperationSeconds: 2250
    maximumGapMilliseconds: 3000
    timing:
      changeIntervalP95Seconds: "1.0339932335"
      maximumRequestSeconds: "0.24872398"
      maximumReceiptGapSeconds: "0.325263383"
      source: "/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/phase-a/measurement-generalization-20261024/meter-cadence/summary.json#nodes.genesis2; empirical Gen3 transfer"
  steps:
  - {name: up, kind: power, powerMode: MAXN}
  - {name: prepare, kind: prepare, batchEnergyOptimization: jetsono-010-plc-cuda-maxn}
  - {name: idle, kind: idle, idleDurationSeconds: 120}
  - {name: remove, kind: remove, batchEnergyOptimization: jetsono-010-plc-cuda-maxn}
  - {name: down, kind: power, powerMode: MODE_15W}
```

각 `remove`는 해당 profile의 Accepted `prepare` 결과에서 관측한 정확한 Pod UID와 container ID를 사용한다. 이 physical identity는 idle과 power 단계에서도 유지한다. 준비 단계의 `measured-*` 이름은 논리 슬롯으로 사용한다. 교체되었거나 physical source가 없는 경우에는 atomic operation의 exact identity 검증에서 실패하며 기존 immutable record는 보존한다.

`Refused`, `Insufficient`와 `Failed`가 발생하면 해당 노드 레인을 멈추고 부분 근거와 자연 반환을 보존한다.
재측정은 원인과 승인 범위를 확인한 뒤 새 이름의 요청으로 수행한다.

## 9. 검사 계약

검사 결과는 실제 source SHA와 실행 근거를 연결한다. 설계의 수용 조건과 배포/검증 실적을 각각 기록한다.

| 대상 | 확인할 불변식 |
|---|---|
| API와 admission | kind별 필드 조합과 spec 불변, profile/Node/CPU 조건, 실제 gateway cap 초과의 실행 전 거절 |
| 노드별 순서 | 앞 루트가 기다리면 같은 노드 뒤 루트도 대기하고 다른 노드는 진행한다. 측정 taint와 서빙 격리를 유지한다 |
| operation | prepare/idle/remove/power의 실제 identity, effect와 격리, 부분 실패, sequence 출처와 자연 반환 |
| 비용 민감도 | 상수 S=0, offset 불변, 내부 극값, 원 gap 합집합과 cap, 실제 support/bracket, undefined 비율, 10% 초과 유효 에너지의 Accepted |
| 최소 이득 | nominal 불변, 선택 evidence의 signed 상쇄와 사슬 합성, 이득=B에서 keep, 이득>B에서 전환, keep infeasible 회복. exact baseline을 사용할 때 무관한 profile CI가 문턱을 바꾸지 않음 |
| d | 물리 batch/member/연속성, HTTP 실패 뒤 실행, late native 보충, 끝단과 누락 unknown, 설정 idle 비율 경계와 1 ms/요청, 식별된 idle의 nanos dRange/κ 상한과 미식별 생략, 모든 판정 사유, signed raw와 max0 소비 |
| lifetime | L≥SLO, cap≥L, L과 delivery를 포함하는 socket(E5는 socket>L+delivery 명시), sender 경과와 m의 단일 차감 |
| observer | 실제 producer end 뒤 final read, 동결된 event/result byte 예산, authoritative Job 조회의 NotFound와 transport 오류 구분 |
| 소비자 | 최신 Accepted의 정확한 NodeUID/mode/runtime/model 일치, 결측 사유, 실제 source 교체와 반환 |

## 10. 캠페인 범위와 증거

E5 전 기준 범위는 Node당 기준 모드의 필수 profile, d와 정확 비용이다.
전력모드 확장은 E5 뒤의 별도 범위다. runtime 7종의 digest는 고정한다.
원본 012/017의 raw/status와 coherent replacement 출처를 보존한다.
원본 017의 비실행 Measuring/Granted는 명시적 보존 예외다.
실행 순서와 E5 입구 조건은 [워크로드 POOL 검증 계획](workload-pools.md#9-검증-계획)을 따른다.

재현 증거는 조건 생성기, 승인 원문, manifest, profile/runtime/NodeUID/source SHA와 원시 위치를 선별한다.
`.token`, Secret 스냅샷과 전체 캐시 백업은 저장소와 증거 인덱스의 대상에서 제외한다.
다음 경로는 본문 계약의 결정과 재현을 뒷받침하는 근거다.

- [d 결정](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/d-measurement-decision.md)
- [조건 생성기](/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/gen.py)
- [승인 manifest](/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/phase-a/measurement-generalization-20261024/approved/)
- [비용과 d 결정 근거](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/implementation-contract-20261024.txt)
- [D2와 E5 승인 근거](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/deployment2-e5-proposal-20261024.md)

## 11. 같은 기종 Node 프로파일 재사용 — PR-01..04

### 11.1 저장과 판정

새 CRD·CRD spec/status 필드·일반화 엔진을 만들지 않는다. 대상의 짧은 확인 측정은 기존
BatchEnergyOptimization(BEO)→ProfilingRun→불변 result/raw 경로를 그대로 쓴다.
대상 BEO 생성 시 metadata annotation을 함께 선언한다.

```yaml
metadata:
  annotations:
    chill.dacs.io/profile-reuse: '{"sourceProfileID":"chill-system/SOURCE-BEO/SOURCE-UID","maximumRelativeDifference":0.05}'
```

`sourceProfileID`는 같은 namespace의 **직접 측정** catalog profile ID다.
자기 자신·전이적 재사용·무효화/삭제/대체되어 catalog에서 빠진 원본은 거절한다.
annotation의 두 필드는 필수이고 알 수 없는 필드·범위 밖/비유한 허용 차이는 거절한다.
원본은 최신 조건 선택 **후**의 유효 profile이어야 한다. 확인 BEO는 자동으로 일반
독립 profile 후보가 되지 않는다. 실패 시 annotation을 지워 짧은 측정을 독립 측정처럼
승격하지 말고 원인과 원시 근거를 보존한 뒤 새 BEO로 독립 측정한다.

원본과 확인의 frozen condition을 비교하되 plan 이름, Node UID, meter identity,
GPU UUID, 활성/유휴의 측정 창 길이만 비교에서 제외한다. DeviceClass, image digest,
모델·전처리·데이터셋, mode, CPU request/limit/affinity/격리·thread, accelerator
할당·권한, runtime 설정·version과 initialized batches는 일치해야 한다.
확인 창은 활성·유휴 각각 30초 이상이고 BEO의 기존 trial≥3 계약을 유지한다.
warmup·settling·관측 interval/timeout·observer image는 동일해야 한다.
원본의 모든 실측 배치가 확인에 있어야 하며 확인이 빠진 배치를 추정해 추가하지 않는다.

배치별 평균 서비스 시간은 raw RUNNER SERVICE 표본 평균이다. 활성 평균 벽전력은
각 trial의 `activeWindowWallJoulesPerItem / idleBaselineSecondsPerItem`
(동일 창의 총 벽에너지/측정초)를 trial 사이 산술 평균한다. 서비스시간으로
전력을 역산하거나 증분 전력을 전체 벽전력으로 비교하지 않는다.
유휴 평균은 기존 model-loaded idle baseline이다.
각 지표에 `abs(target−source)/source`를 적용하고 모두 설정값 이하이면 Accepted다.
분모≤0, 결측·비유한 값, 조건 불일치와 차이 초과는 Rejected이며 독립 측정을 요구한다.
권고 0.05의 근거와 한계는 [SRS §7](../../spec/catalog/SRS.md#7-u4-같은-기종-프로파일-재사용)을 따른다.

`CatalogView.reuseChecks`는 원본·확인 ID/불변 결과 참조, 양쪽 Node, 허용값,
idle 차이와 배치별 service/active wall 차이 및 Accepted/Rejected·사유를 제공한다.
원시 확인 result/raw와 입력 annotation은 기존 BEO에 보존된다. scheduler service는
Accepted/Rejected 모두 `{schemaVersion:"chill.dacs.io/profile-reuse-check.v1alpha1",check:…}`를
`serving/profile-reuse/<확인 profile ID>/<판정 본문의 SHA256>.json`에 PutImmutable로
보존한 뒤에만 catalog를 발행한다. `reuseChecks[].record`와 Accepted profile의
`reuse.record`가 이 불변 참조를 가리킨다. annotation 변경·원본 무효화 뒤의 거절도
새 content-addressed 기록이므로 앞선 판정을 덮어쓰지 않는다.
service refresh signature는 profile ID뿐 아니라 판정 본문을 포함하여 허용값·사유
변경을 놓치지 않는다. catalog는 클러스터 annotation/status를 쓰지 않는다.

### 11.2 대상 행과 물리 개체 계약

재사용 행의 ID는 `확인 profile ID/reuse-<SHA256(sourceProfileID + NUL + 허용값)>`;
annotation 변경 시 불변 startup bundle을 덮어쓰지 않도록 source·정책도 ID에 포함한다.
`Reference`, `MeasuredAt`, frozen
condition, NodeName/UID, meter, runtime metadata/delivery와 idle baseline은 대상이다.
서비스 분포와 증분 에너지는 원본의 값을 유지하고, 메모리는 원본/대상 관측 최대 중
큰 값을 쓴다. 활성 평균 벽전력은 확인값이다. `Evidence`에 양쪽 result/raw를 연결한다.
GPU UUID는 condition.RuntimeAccess, RuntimeMetadata, runtime delivery 모두
대상 확인 기록에서 가져오며 원본 UUID를 치환해 가장하거나 inventory에 넣지 않는다.
CPU affinity는 기종이 같아도 임의 재배치하지 않고 동일 실측 mask만 허용한다.
대상 Node가 재생성되어 UID가 바뀌면 기존 확인을 재사용하지 못한다.

| Node UID를 소비하는 경로 | 변경 후 계약 |
|---|---|
| catalog latest-condition 선택 | 원본 유효성 선택 후 확인을 판정·대상 행 materialize; 최종 대상별 최신 선택 수행 |
| scheduler_capacity.go bootstrap/nodePlans | UID guard 유지. 대상 UID로 materialize된 행만 대상 option에 연결 |
| kube/observer.go validateSourcePlacement | 물리 Node·DeviceClass/image/resource guard 유지; 원본 Node alias 금지 |
| kube/operations.go validatePreparation | 대상 Profile/UID/runtime delivery로 prepare; source UID 예외 없음 |
| serving/firstfloor/client.go native metadata 검사 | 기존 대상 UID·NodeName와 actual runtime 확인 유지 |
| kube/runner.go runnerPreparation | 대상 UID·mode·model의 최신 준비 행 선택; source UID로 fallback 금지 |
| kube/cost_power.go validatePowerNativeProfiles | 대상 UID·실제 UUID/image 검증 유지 |
| kube/scheduler.go measuredGPUs, manual_reset/measurement_sequence | 대상 행 metadata만 inventory/측정 후보에 포함; source UUID 오염 금지 |
| cost/d/idle 연결 | d와 prepare/remove/power 비용은 기존 대상 Node/profile ID별 별도 측정; source 비용 복제 금지 |

### 11.3 CampaignOps: genesis2→genesis3 한 건 실증 절차

클러스터 변경은 CampaignOps 한 명만 실행한다. 이 절은 절차이며 실증 성공 기록이 아니다.

1. 측정 레인이 비었고 두 Node의 serving/measurement 예약이 없음을 확인한다.
   `kubectl get nodes genesis2 genesis3 -o json`, 해당 DeviceClass와 source BEO/result,
   `/api/v1/scheduler/profiles` 응답을 원시 디렉터리에 보존한다. 정확한 UID, 기종 label,
   image digest, mode, CPU quota/affinity, source profile ID·배치·평균값을 조건표에 쓴다.
   SSH 읽기 전용 `nvidia-smi -q`로 GPU UUID·driver·VBIOS·기본 power limit도 기록한다.
   genesis2/3의 기본 power limit은 250/300W로 관측되어 이름만으로 동등하다고 판정하지 않는다.
2. genesis2의 유효 PLC `ort-cuda` 한 행을 우선 선택한다. U3의 locked mode는 양쪽에서
   같은 모드의 원본 근거가 있을 때만 사용한다. 기존 fixed 원본을 locked 확인에 연결하지 않는다.
   runtime 7 digest를 바꾸지 않는다. 같은 namespace에 새 target BEO 이름을 정하고
   위 annotation의 SOURCE-BEO/SOURCE-UID를 실제 ID로 채운다.
3. source BEO의 spec을 새 객체로 복사하되 state.targetNode만 genesis3으로,
   활성/유휴 durationSeconds를 각각 30으로, metric duration은 창 경계까지 충분히
   덮도록 줄인다. warmup, idle settling, trial≥3, CPU affinity/quota와 배치 도메인은
   유지한다. 객체 UID/resourceVersion/status/ownerReferences는 복사하지 않는다.
   정상 admission으로 제출하고 Complete까지 기다린다. 원본 spec/status는 수정하지 않는다.
4. 새 코드의 catalog refresh 뒤 `/api/v1/scheduler/profiles`를 저장한다. target의
   `reuseChecks`가 Accepted이고 차이 세 종류가 모두≤0.05인지 확인한다.
   Rejected이면 그 사유·차이와 원시 result를 보존하고 **독립 측정**으로 전환한다.
   허용값 확대, 일부 batch 제외, 원본 값 복제나 강제 Complete로 통과시키지 않는다.
5. Accepted 행의 `id=확인ID/reuse-<hash>`, node UID=genesis3, GPU UUID=genesis3 실측 UUID,
   meter=genesis3이고 service samples/energy는 source와 같은지 확인한다.
   profiles 응답의 preparations와 scheduler snapshot의 options에 그 ID가 있는지 확인한다.
   기존 genesis3 독립 profile이 더 최신이라 선택되면 이를 삭제/무효화하지 말고 동일
   조건 최신 선택 결과와 이유를 기록한다. 실증용 새 확인이 최신인 조건을 선택한다.
6. **용량 연결은 별도다.** genesis3의 기존 Accepted d가 동일 UID/mode/runtime/model이면
   연결하고, prepare/remove·baseline·mode cost의 exact profile ID 적용 범위를 점검한다.
   새 ID 비용이 필요하면 정상 OperationCostMeasurement로 잰다. source d/cost는 복제하지 않는다.
   capacity row의 대상 option/profile ID와 d source, κ 상태를 보존한다.
   한 번 prepare→native actual read-back→remove/hand-back을 수행해 대상 UUID·자원 반환을 보존한다.
7. 결과 묶음: 배포 SHA, source/target BEO UID와 annotation, result/raw references,
   profiles(reuseChecks·재사용 행·preparations), snapshot option/row, 실제 Pod/native UUID,
   거절 또는 성공 사유와 반환 증거를 CampaignOps 원시 디렉터리에 저장하고 U2 표에 연결한다.
   짧은 확인을 독립 전체 측정 한 건 절감과 구분해 소요 시간을 보고한다.

회귀는 허용 경계/설정 변경, 계열·image·자원 불일치, source 무효화/최신 선택,
확인 배치/전력 결측과 대상 UUID 보존, source/target 독립성, planner option 연결을 검사한다.
