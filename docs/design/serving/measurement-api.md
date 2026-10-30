# 선언형 operation 비용 측정과 디스패치 점유 측정

캠페인은 CHILL 측정 API를 사용한다. 노드별 승인·반환은 [측정 admission](measurement-admission.md), 실행·배포 근거는 [handoff](../../handoff.md)를 따른다. 이 문서는 측정량, 조건 생성, 판정과 계획기 소비를 정하며 원시 자료, signed 값과 원래 판정을 보존한다.

## 1. 측정 원칙

- operation 비용은 준비, 유휴, 해제와 빈 노드 전력모드 변경을 순서 있는 요청 하나로 측정한다. 동작별 불변 기록은 요청과 실제 physical identity를 가리킨다.
- 디스패치 점유 d는 고정 배치 인스턴스 하나의 물리 실행 간격이다. Node UID·전력모드·runtime image·모델이 같은 조건끼리 사용한다.
- 같은 노드는 요청 순서대로, 다른 노드는 병렬 측정한다. admission이 CPU·가용 자원·profile을 확인하고 거절 사유를 기록한다.
- 대표 한 건의 조건 생성→실행→원시 기록→계획기 선택→실물 반환을 확인한 뒤 대량 실행한다. profile/runtime/측정 계약 변경에는 영향받는 d·비용·baseline 목록을 남긴다.

## 2. OperationCostMeasurement (`opcost`)

`edge.dacs.io/v1alpha1`, Namespaced, status subresource, spec 불변(`self == oldSelf`). printer column: Phase, Node, Reason, Age. `spec.node`는 Node 이름(1-253)이다.

| 필드 | 형식 |
|---|---|
| `spec.observation` | `intervalMilliseconds`, `requestTimeoutMilliseconds`, `maximumOperationSeconds`, `maximumGapMilliseconds`, 필수 `timing`. timing의 문자열 `changeIntervalP95Seconds`, `maximumRequestSeconds`, `maximumReceiptGapSeconds`, `source`로 실측 입력을 고정한다. 원 gap은 결측 민감도 근거다 |
| `spec.steps[]` | 1-32개, `listType=map` key `name`. DNS label `name`, `kind`(`power`, `prepare`, `idle`, `remove`). `power`는 `powerMode`, `prepare`/`remove`는 같은 namespace BEO 이름 `batchEnergyOptimization`, `idle`은 `idleDurationSeconds`(≥1)만 갖는다(CEL) |
| `status` | `phase`(`Measuring`, `Complete`, `Insufficient`, `Failed`), `reason`, `steps[]{name,key,verdict}` |

**실행.** 승인과 같은 `tickMu`에서 노드 run과 `Measuring`을 등록한다. 순서 ID는 CR UID다.
- 단계별 `MeasurementRequest`의 `Operation.ID=<UID>.<순번>.<name>`이며 meter/store는 비워 노드 설정으로 채운다. `power`는 `nodePowerTargets`/`powerRequest`의 NodeActuation에 CR owner를 달아 반환의 foreign-actuation 검사에서 자기 단계를 제외한다. `prepare`는 BEO 현재 UID의 profile preparation, `remove`는 그 인스턴스다.
- 기존 `Measure`를 순차 호출하며 key·형식·witness를 유지하고 단계별 key/verdict를 `status.steps`에 추가한다. 실패 또는 non-`Accepted`에서 멈추고 `serving/operation-sequences/<UID>.json`을 쓴 뒤 반환한다([admission §2.5](measurement-admission.md#25-operation-비용-측정과-serving-operation)).
- 실제 operation/effect·격리·서빙 추론 0·원시 구조·실제 양쪽 bracket·확장 support를 확인한 에너지는 `Accepted`다. 정밀도 비율은 보고용이다. 동작 실패, 격리 오류, boundary 누락, invalid record의 사유·부분 근거와 원래 값/판정을 보존하며 실행 실패는 `Failed` 수명주기로 기록한다.

### 2.1 전력 곡선과 민감도

실제 동작창 `[a,b]`, 기록된 piecewise-linear 전력곡선 q에서 `Ehat=∫[a,b]q(t)dt`, 비용은 `Ehat ± B`다.
- 위상 여유 `A=p95(전력값 변화 간격)+최대 요청 지연+최대 유효 수신 gap`의 원 십진 문자열·출처를 고정하고 실행시간 변환은 합을 nanosecond로 올림한다. 승인 실측 추정은 Gen3 1.6079805965 s, Plus 4.518180026700001 s다. 미측정 Gen3 6개의 계열 전이는 추정 한계다. 동일값 유지 길이·추가 배수는 A에 포함하지 않는다.
- `[a−A,b+A]`의 실제 표본을 확보한다. `S=maxδ∈[−A,A]|∫[a,b]q(t+δ)dt−Ehat|`를 모든 knot와 구간 내부 stationary 극값에서 계산한다. 상수 전력은 S=0이고 상수 offset에 불변이다.
- 실패/누락 요청을 포함한 원 gap과 `maximumGap` 초과 valid-to-valid 원 gap 중 확장 support와 교차하는 구간의 **원래 전체 폭** 합집합을 한 번 계산한다. `G=이번 record의 최대 관측 W×min(b−a,합집합 길이)`, `B=S+G`. A 확장은 support에 한 번만 적용한다. G는 미관측 전력이 이번 최대 관측값 안이라는 경험적 조건이다.
- `energyUncertaintyJoules=B`, `phaseSensitivityJoules=S`, `missingGapJoules=G`. `energyUncertaintyRatio=B/abs(Ehat)`이며 Ehat=0 또는 nonfinite 비율은 undefined다. 10% 지표는 보고용이며 Accepted/Insufficient gate가 아니다. `request.timing`, `operationBudgetNanos`에 조건을 보존한다.
- 이 민감도는 관측 support/Pmax 조건부이며 센서 교정 또는 전체 물리 오차의 95% 구간을 보장하지 않는다.

**운영 POOL 전력 창.** 원시 성공 표본을 합쳐 기록된 coverage 정책으로 판정한다. 현재 MaximumGap 3.0 s, MinimumCoverage 0.75, MaximumBoundaryDistance 1.0 s다. 읽기 실패 자체가 아니라 성공 표본 사이 gap이 불완전 여부를 정한다.
`readFailures`는 노드별 창 안 실패 횟수, `maximumSampleGapSeconds`는 경계 support를 포함한 인접 실제 표본 최대 간격이며 창 요약은 각각 노드 합계·노드 최대값이다. 정책을 만족하면 실패가 있어도 complete, 초과 gap은 불완전이다.
단발 실패 공백 ≤2.52 s, 연속 두 번 실패 공백 ≥3.2 s의 운영 근거에 따른 설정이며 정확 비용 등 다른 측정 정책은 바꾸지 않는다. 오프라인 `hack/observations/power --maximum-gap 3s`는 보존 정책을 수정하지 않는 명시적 재평가이고 `policyOverride.maximumGapSeconds`로 표시한다.

### 2.2 계획기 불확실성

`OperationTrial.energyUncertaintyJoules`, `NodeBaseline.wattsUncertainty=B/실제 측정초`를 cost projection에 전달하되 nominal 평균은 유지한다. `SchedulerNodePlan.energyUncertaintyJoules`는 사슬 민감도, `SchedulerCapacity.gainUncertaintyJoules`는 B-only minimumGain 문턱이다.
같은 immutable reference/trial index의 B/N은 발생 횟수·부호를 누적한다. keep−best에서 같은 근거를 먼저 상쇄하고 다른 근거의 잔여 절댓값 합을 문턱으로 쓴다. 이득>문턱이면 전환, 같으면 keep이며 keep infeasible 회복 경로를 유지한다.
`unloaded+Σ(loaded−unloaded)`의 unloaded 계수는 `1−instanceCount`, exact loaded baseline 계수는 1이다. target-baseline-delta는 저장된 차감 nominal을 한 번 복원하며 B는 wall 민감도다.
이 문턱은 관측 support/Pmax 조건의 위상-결측 민감도다. profile 통계오차·BEO idle fallback CI·시점 drift·global95% gain 보장은 포함하지 않는다. 원시 profile 분산/CI와 d의 95% 분석은 각각의 추정 계약에 보존한다.

## 3. DispatchOccupancyMeasurement (`occupancy`)

2장과 같은 group·Namespaced·status subresource·spec 불변·`spec.node` 규칙이다. printer column: Phase, Node, Mode, PerRequest(`.status.perRequestNanos`), Reason, Age.

| 필드 | 형식 |
|---|---|
| `spec.batchEnergyOptimization` | option의 BEO. mode/runtime image/model은 profile의 `condition.powerMode`, `runtimeImage`, `logicalModel` |
| `spec.workload` | 요청 경로를 줄 Workload 이름. 그 Dataset의 `evaluation` 부분집합을 전송 |
| `spec.batch` | 고정 B(≥1), 인스턴스를 `fixedBatchSize:B`로 준비(`SCH-35`) |
| `spec.supplyMode` | d는 고정 outstanding `closed-loop`, E5는 `poisson`. `workers=K`, 대기 batch q와 계산 입력을 conditions에 고정 |
| `spec.warmupSeconds`, `durationSeconds`, `minimumBatches`, `maximumIdleFraction` | profile 유도 W_load·본창 T·표본 N·식별된 idle 허용 비율. idle 비율은 [0,1] 설정값, 승인값 0.05(고정값 검증 아님). 동결 `maximumIdleBaseFraction` 승인 상한도 0.05이며 profile로 더 엄격하게 역산하지 않음 |
| closed-loop 시간 하한/상한 | `conditions.inputsJSON.coefficients`의 `loadPreparationFloorSeconds`, `windowFloorSeconds`, `plannedElapsedCapSeconds`를 한 번 동결·소비. 승인값 60/360/3600 s, preparation 예산은 별도. legacy Poisson duration 60 s는 독립 구조 계약 |
| `spec.requestLifetimeSeconds`, `deliveryMarginMillis`, `drainSeconds`, `preparationSeconds`, `jobSetupSeconds`, `collectionSeconds`, `plannedElapsedSeconds` | profile·SLO·실제 준비·batch tail·전달·수집 예산에서 계산. L≥SLO, gateway cap≥L, native count/decoded bytes 공동 조건. d/E5 socket은 L+delivery보다 엄격히 큼(최소 +1 ms) |
| `spec.conditions` | `profileID`, `inputsJSON`, `waitingBatches`, `maximumIdleNanosPerRequest`, `minimumCycles`: 정확한 profile·모든 입력·승인 계수·q·idle 1 ms/요청·필수 cycle 및 하한/cap 설정 원천을 불변 고정 |
| `status` | `phase`(2장과 같은 넷), `reason`, `powerMode`, `perRequestNanos`, `throughput`(십진 문자열 req/s), `result`(`ProfilingRunEvidenceReference`) |

**실행.** 한 노드 run의 모든 동작은 measurement `Operations`(taint toleration)다. 필요하면 CR owner NodeActuation으로 profile mode에 `ChangePower`하되 비용 기록 없이 그 뒤 witness를 시작한다. preparation Owner는 Workload이며 fixed B로 준비하고 Pod에 해당 Workload UID routing annotation을 준다.
계산된 공급·수명·warmup·본창·drain·수집 예산으로 load Job을 실행한다. 전용 JSONL d scanner가 native batch 원장을 compact 집계하며 전체 cohort 배열/전체 Query 없이 조건·모든 과학 판정 사유를 기록하고 반환한다. 공유 observation reader는 조회 bounds를 UTC hour key로 정규화하여 Local/UTC 차이로 coverage 공백/`ObservationRetentionGap`을 만들지 않는다.

**load Job.** release namespace `occupancy-<UID hash>`, label `chill.dacs.io/measurement-load:<Node UID>`, `backoffLimit:0`과 유한 예산을 사용한다.
- closed-loop 공급은 `warmupSeconds+durationSeconds+L+deliveryMarginMillis/1000`이다. generator 시작→첫 물리 batch 전달 지연 때문에 창 끝 공급이 끊기지 않게 한다. Job deadline은 공급 시간을 초로 올림한 뒤 setup·drain·collection을 더한다.
- 전체 elapsed는 `preparation+setup+warmup+duration+ceil(L+deliveryMarginMillis/1000)+drain+collection`이며 `plannedElapsedSeconds` 이하, 동결 cap(승인 3600 s) 이하다. generator는 `supplyExtensionSeconds=ceil(L+deliveryMarginMillis/1000)`를 known 합과 새 `conditions.inputsJSON.computed`에 예약하고 미관측 setup/collection 잔여에서 차감해 합을 3600 s로 맞춘다.
- 기존 record/terminal CR/동결 입력, 물리 batch 기준 warmup·본창, Poisson 공급 시간은 바꾸지 않는다. HTTP 만료 후 native 미완료 근거는 unknown이다. 반환은 label의 Job을 Background 삭제해 부하 Pod를 정리한다.
- `nodeName`은 ChillServer Pod의 노드(downward API `spec.nodeName`, env `NODE_NAME`)다. scheduler/NoSchedule taint를 거치지 않으며 gateway 노드에 두어 다른 측정 노드 전력에 부하를 섞지 않는다.
- init `dataset`은 Dataset image의 `sh -ec 'cp -R "$1"/. /data/' -- <path>/evaluation`이다. Workload/Dataset 원시 입력을 실제 FQDN·CA·projected token·기존 release ServiceAccount의 공통 TLS gateway로 보낸다. d는 closed-loop K와 L header/socket 여유, E5는 Poisson이다.
- ServiceAccount `<fullname>-load-generator`, image는 `load-generator/`를 담은 `build/docker/load-generator.Dockerfile`, base Python 3.12다. live 인증서에 Subject/Authority Key Identifier가 없어 3.13+ 기본 strict 검증이 거절할 수 있다 `[추론]`.

**물리 측정량.** `d=연속 native batch 시작 span/(physical cycles×B)−profileMean/B`의 signed 전체 값을 보존한다. batch는 Node·Pod UID·container ID·BatchID·member로 연결한다. readiness는 B개 member queued 시각의 최댓값, actor 대기는 `runningNanos−queuedNanos`다.
직접 native timestamp/legacy offset의 원천과 HTTP 결과/실제 실행을 구분하고 HTTP 실패 후 실행 batch도 포함한다. compute gap 전체를 idle로 차감하지 않는다.

**조건 생성과 동결.** 상대 계획 정밀도 1%, 양측 95%, 공통 여유 1.25, idle 기여 1 ms/요청을 실측 profile의 batch별 분산·trial 경계·준비 aggregate·Workload SLO에 적용해 N·L·창을 계산한다. N은 physical batch 수, cycle은 N−1이며 같은 batch member를 독립 표본으로 중복 계산하지 않는다.
- `K=min(64,floor(queueBytesCap/framedDecodedRequestUpperBytes),floor((L−margin)/(B×mu_tail)))`를 한 번 정한다. B는 profile batch, `mu_tail`은 같은 B의 profile 최대 관측 batch 실행초, L은 기존 수명초, margin은 `deliveryMarginMillis/1000` reserve다.
- `queueBytesCap=67108864`; `framedDecodedRequestUpperBytes`는 framing 포함 요청별 decoded bytes 상한이다. K는 worker/outstanding 요청 수이며 B 배수로 올림하지 않는다. `q=ceil(K/B)−1`이다.
- `conditions.inputsJSON.computed.supplySizing`에 `{batch,profileMaxBatchSeconds,serviceTimeSource:"profile.maxObservedSeconds",requestLifetimeSeconds,marginSeconds,workerCap:64,queueBytesCap:67108864,framedDecodedRequestUpperBytes,limitingBound}`를 동결한다. generator/API/controller가 K·q를 정확히 재계산한다. 최대 관측값이 없거나 양의 유한값이 아니면 거절하며 mean×2 fallback은 없다.
- `limitingBound`는 `queue-cap`, `bytes-cap`, `lifetime` 중 최솟값을 정한 항목이며 동률은 이 순서다. 공급 sizing용 왕복/외부 시간 측정이나 별도 출처 입력은 쓰지 않는다. L은 기존 계산 그대로이며 K를 늘리려고 확장하지 않는다.
- 기존 q/batch-tail/prefix 수명 검사·native waiting count/decoded bytes 공동 조건·SLO·gateway cap을 유지한다. 큰 K를 생성 후 bytes 초과 거절하지 않고 세 cap의 최솟값을 생성한다. 공급은 K<B일 때만 불가능하며, 다른 실행 조건/전체 cap 불만족도 실행 전에 보고하고 L/예산을 늘리지 않는다.
- 계수·하한/상한은 한 설정 원천에서 읽고 generator/API/admission/controller가 같은 동결 조건을 소비한다. 준비 시간과 부하 warmup의 의미·예산은 별도 기록한다.

**수명·유휴 상한.** admission은 L을 실제 `HandlerConfig.RequestTimeout`과 대조해 초과 CR을 실행 전에 거절한다. caller는 L/delivery를 명시하고 `socketTimeoutMillis>=L*1000+deliveryMarginMillis+1`로 설정한다. gateway가 원래 L/sender 경과를 전달하고 native는 `L−senderElapsed−completionMargin`을 쓰며 m은 한 번 차감한다(E5 Poisson도 동일).
유한 physical drain·전체 3600 s 예산(`e8e018e2`)·공급 연장 예약을 유지한다. 예산 불충족을 보고하며 연장 축소/cap 초과 실행은 하지 않는다. Accepted/동결 입력은 불변이고 묵시적 재실행은 없다. 새 K는 Accepted10의 K와 같은 불변량이 아니며 비교는 실제 근거로 한다.
식별된 idle 비율은 설정 `maximumIdleFraction` 이하여야 한다. 승인 0.05는 계획 여유 25%(1.25−1)의 1/5이며 signed d를 바꾸지 않는다. 초과는 producer/요청 경로 한계로 보고하고 K·L·margin·cap 튜닝/자동 재측정으로 통과시키지 않는다.

**판정·통계.** physical finished→다음 ready의 보수 idle 상한, 창 끝단·hole·누락 native를 함께 평가하며 필요한 원근거가 없으면 unknown과 모든 사유를 남긴다.
- `idleIdentified=true`는 physical batch 연속성과 실행 근거 완전성을 요구한다. false의 `idleUpperNanos`는 알려진 pair 부분 합일 뿐 통과 근거가 아니다. unknown·hole·누락 native·불완전 실행은 탈락한다.
- `idleNanosPerRequest`는 같은 physical cycle/endpoints를 쓰며 1 ms/요청 상한을 유지한다. 식별된 idle에만 nanos `dRange=[perRequestNanos−idleNanosPerRequest,perRequestNanos]`, κ 상대 오차 상한 `kappaErrorBound=idleFraction`을 기록하고 미식별이면 생략한다.
- d에서 idle을 빼지 않는다. 원 d가 커지고 κ가 작아지는 보수 방향이며 범위/상한은 95% CI나 전체 모델 오차 보장이 아니다. `cycleMeanNanos`, `cycleSDNanos`, `cycleRelativeHalfWidth`, `precisionTargetMet`는 stationary-iid t 근사의 보고 정보다.
- N/창은 시작 전 고정한다. 실제 CI로 새 거절 gate/N 자동 변경을 만들지 않는다. minimum N·물리 완전성·unknown·idle 상한을 판정하고 모든 해당 이유를 `reasons[]`에 보존한다.
- RequestRow `nativeQueuedAt`, `nativeRunningAt`, `nativeFinishedAt`, `nativeExecutionComplete`, `nativeOutcome`, `nativeError`는 직접 actor 근거다. native `queueMS`는 ingress 포함 지표다. HTTP 완료 후 native는 `kind:native` 보충 raw로 남겨 HTTP 완료/member를 중복 계수하지 않는다. scanner가 누락 identity/physical batch를 compact 집계한다.
- 통과는 `Complete`이며 음수 d도 보존한다. minimum batches·idle·물리 근거·unknown·witness의 모든 사유는 `Insufficient`/record에 남긴다. 거절·prepare/remove·Job·watchdog·재시작 실패는 `Failed`다.
- 초기 Measuring patch와 부하 시작 전 실패 terminal status는 취소 run과 분리한 최대 30 s publication context로 저장한다. handback은 parent context를 쓰며 status 저장 예산으로 측정창을 확대하지 않는다.

**근거 지위.** mobilenet CPU 8개/GPU 2개는 새 규칙 미재인증 legacy이며 실제 대체 Accepted 전까지 소비한다. U2 CPU 8개/GPU 전체 5개/PLC 12개의 새 규칙 측정은 D2 뒤 25개 범위다. D01 Insufficient와 `rasp5-944` 진단은 보존한다. U3 D04/D09 새 immutable record가 실제 consumer source를 대체하며 옛 Smol CPU 2개 raw/status는 보존한다.
**Smol 해석.** D04/D09/D14/D23/D33의 profile 포함 95% 근사구간은 0 아래이며 E5의 알려진 systematic profile-versus-serving bias다. profile/NodeUID/model/B1/mode/runtime 일치와 harness/warmup/timing envelope/시간 분리는 관측 사실이다. 실효 thread/affinity·입력/전처리 경계·열/주파수/throttling·clock의 원인별 기여는 미확정 가설이다. signed raw·원 Accepted·`max(d,0)`을 유지하고 bias 보정/d 추정정책 대안은 M5 후보다. 수치·원시는 [bias JSON](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.json), [해석 근거](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.txt)를 따른다.

## 4. 계획기의 d 수집

- `CostEvidence.Refresh`는 `serving/dispatch-occupancy/`를 prefix 목록/불변 identity cache로 읽고 `Accepted` 중 (Node UID, mode, runtime image, model)별 최신 `measuredAt`을 선택한다. 동률은 key 순(`latestBy` 규칙)이다.
- `serving.DispatchOccupancy{node{name,uid},powerMode,runtimeImage,model,perRequestNanos,source}`에서 source는 record key, `perRequestNanos=max(d,0)`(사용자 결정 A7)이다. 음수 d는 원 record/key로 조회한다.
- controller 설정을 거쳐 snapshot `dispatchOccupancy`에 싣고 `dispatchOccupancy(i)`는 option의 Node UID/profile mode/runtime/model **모두** 일치하는 항목만 찾는다. `NewCapacityInput`과 replay가 이를 쓰며 행 key에 항목이 포함되어 source 변경 시 재계산한다.
- 미측정 mode option은 `MissingDispatchOccupancy`다. 같은 Node/mode/runtime/model option은 d를 공유한다. 상세 용량 소비는 [계획기](analytic-planner.md)를 따른다.

### 4.1 observer와 coherent replacement

`Observer.ObserveUntil(ctx,Request,end,onFirstSample)`는 실제 producer end 뒤 final read까지 수집한다. Duration은 유한 watchdog, 표본 예산은 `floor(watchdog/interval)+3`이다. standalone `--end-wait-timeout`은 `RunConfig.Metrics.DurationSeconds`, `--max-result-bytes`는 frozen policy `MaxMetricResultBytes`다.
profiler는 별도 `mgr.GetAPIReader`로 recorded Job을 조회한다. 실제 API NotFound는 `ExecutionLost`, transport 오류는 retry이며 cache의 일시 NotFound로 replacement를 만들지 않는다.
BEO annotation `chill.dacs.io/replaces-batch-energy-optimization`은 같은 namespace 원본 이름이다. pending 동안에도 원본을 catalog에서 제외하되 raw/status를 보존한다. 대체는 정상 verifiedComplete의 state/CPU/batch/exact trial 계약을 충족한다.
012/017 coherent replacement와 원 trial/status를 보존한다. 원본017 Granted/Measuring은 비실행 보존 예외다. execution/serving Pod/power pending 0·정확 NodeUID/RV/holder·orphan runtime 확인 후 CampaignOps가 old UID stale 측정 taint만 정리하며 root/status/Runs/retained Jobs/raw는 남기고 대체는 정상 admission을 따른다. observer 검사는 실제 late producer tail/final sample/event/result byte 근거를 연결한다.

## 5. 측정 admission

두 kind는 `spec.node` 대상 측정 루트이며 terminal은 `Complete`, `Insufficient`, `Failed`다. 엄격한 노드별 순서·taint·CPU 가용성·반환의 권위는 [admission §2](measurement-admission.md)다. 앞 루트가 기다리면 `claimed`로 뒤 승인을 막고, 승인 taint 노드의 측정 인스턴스 때문에 뒤 루트를 거절하지 않는다. `NodeServing`은 taint 밖 계획기/수동 Pod에 적용한다.

| kind | 승인 전 확인/사유 |
|---|---|
| BEO | `CPUsUnavailable <CPU> <모드>`, `OnlineCPUsUnknown <모드>`: candidate mode online CPU−reserved와 affinity 조건 |
| opcost, occupancy | `ProfileMissing <BEO> <사유>`: 없음/NotComplete/non-Complete terminal/catalog issue. 같은 노드 앞 BEO는 순서대로 대기하며 catalog profile/issue 모두 없으면 갱신을 기다림. `NodeMismatch`(targetNode≠spec.node), `ActuationNamespace`(owner와 같은 namespace 요구) |
| opcost | `PowerModeUnknown <모드>`, `MeterUndeclared` |
| occupancy | `BatchUnmeasured <B>`, `WorkloadMissing`, `WorkloadAutonomous`, `WorkloadServing <Pod>`(같은 capture의 owner/route 가중치), `LoadGeneratorUnconfigured` |

mode는 `spec.state.candidate.powerMode`, online CPU는 DeviceClass `powerModes[].onlineCPUs` cpuset이다. 없으면 현재 read-back mode에만 `edge.dacs.io/cpus`를 쓰고 다른 mode는 `OnlineCPUsUnknown`이다. admission이 실제 Node/ConfigMap 조건을 확인한다.
승인은 taint/`Granted` 뒤 같은 잠금에서 run 등록, 거절은 ChillServer가 `Failed`/사유를 쓴다. ChillServer 노드 루트와 occupancy run을 격리하고 서로 다른 측정 노드 occupancy는 병렬이다. gateway 실제 CPU/메모리를 manifest에 남긴다.
재시작 시 taint+Granted+nonterminal인데 run이 없으면 `Failed:ChillServerRestart`; opcost는 `recordRestartedSequence`로 같은 reason의 순서 기록 후 반환한다. terminal status는 유지하고 반환만 재시도한다. taint만 있고 Granted가 없으면 PMTC처럼 반환 후 재대기한다. 재개 없이 같은 spec의 새 이름을 쓴다.

## 6. RBAC

ChillServer는 두 kind 목록/status patch, release namespace Job create/get/list/delete, actuation namespace NodeActuation 권한을 가지며 같은 namespace owner reference를 쓴다. `<fullname>-load-generator`는 gateway Workload 경로 POST 권한을 갖는다. 두 kind status writer는 ChillServer 하나다.

## 7. 저장 계약 (ADR 0019)

[ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 형태를 reader가 엄격히 소비한다. Timing 없는 미기록 B를 관측한 0으로 투영하지 않으며 원 raw·값·판정은 외부 과학 증거로 보존한다.

| 대상 | 불변 저장/소비 계약 |
|---|---|
| operation/순서 기록 | 실제 timing·B/S/G·보고 비율·operation identity·단계별 출처 |
| `serving/dispatch-occupancy/<Node UID>/<CR UID>.json` | 전체 공급·시간·계수·identity 조건, native 원천·모든 과학 판정 사유를 PutImmutable |
| d/판단 | 수동 d 목록 없이 snapshot이 record를 참조. 행의 Node UID·mode·runtime/model·d source·실제 비교 T 보존 |
| scheduler 설정 | 선택 `SchedulerSettings`/`minimumDemand`를 `decision.settings`, `decision.search.bounds`, scheduler 관측 기록에 동결. 캠페인 binding의 scheduler 입력 참조/settings 캡처를 결과에 연결 |

### 7.1 최소 수요와 재현 설정

별도 Run API/저장소 없이 기존 캠페인 binding/summary와 scheduler 판단/관측 기록을 쓴다.
`minimumDemand`는 Workload **UID**→최소 req/s map이다(이름/model ID 아님). 빈 UID·음수·NaN·무한대는 거절하며 map/UID 생략은 0이다. 유효 수요는 `max(관측 수요,minimumDemand[UID])`; 관측은 장기/최근 도착률·미완료 기여를 포함한다([계획기 §3](analytic-planner.md#3-판정과-순서)). 유휴 제거·교체 회수에도 하한을 적용하며 무부하에도 그 용량을 유지한다.
선택값은 판단/관측에 동결하여 이후 설정 변경이 과거 하한을 바꾸지 않는다. `hack/e5/e5.py`/`demo.py` 새 binding은 scheduler 입력 참조와 전체 `schedulerSettings`(minimumDemand 포함)를 동결하고 `e5-summary.json`/demo `summary.json` 및 immutable 결과의 단계별 `kpis.schedulerSettings`에 같은 값을 남긴다. 과거 binding 누락은 null/사유이며 현재값/가짜 0으로 채우지 않는다.
실행 중 실제값은 `initial.json`, 경계별 `pre-settings.json`/`post-settings.json`, `static-selection.json`, `decision.settings`에 저장한다. binding은 입력 동결 시점이며 이후 변경은 판단/경계 기록으로 확인한다. `decision.capacity.workloads[]`에 `minimumDemand`(미설정 명시적 0), 최종 하한 적용 `rate`(req/s)를 함께 쓰고 관측 도착률은 덮어쓰지 않는다.

**G2 근거·승인 경계.** B=0만으로 계약 세대를 판별하지 않고 실제 immutable 참조/Timing을 조사한다. [소비자 목록](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json)은 catalog 비용 177개 중 35개, baseline 80개 중 14개에 Timing이 없음을 확인했다. 49개 중 E5 선택 노드 empty ranking legacy 9개의 불확실성은 unknown이다. 동결 E5 identity/mode 후보 에너지 입력 legacy는 0개이나 실행된 선택/전체 prefix 정리 완료를 뜻하지 않는다. 최신 선택 전 객체를 읽으므로 superseded/terminal CR 참조도 정확한 정리 목록에 포함한다.
G2 승인 범위는 ranking baseline 9개의 idle/reach-power 2 roots·18 steps, 조건부 직접 전환 포함 상한 4 roots·22 steps다. root마다 raw·실제 소비·정상 반환 뒤 다음으로 진행한다. 9개 대체 확인 뒤 검증 외부 백업의 실제 절대경로, raw 80개 키(old ranking 9개 포함), terminal OCM 6개 개수/목록을 대조·보고하고 해당 작업 저장소 참조를 정리한다.
같은 승인 조건의 legacy/결측 대체→확인→검증 백업→옛 기록 삭제는 연속 실행하되 새 조건/판정/범위 확대/배포는 별도 승인이다. strict reader·obsolete 필드·비교 테스트 제거는 수집과 병렬로 같은 영구 소스 변경에서 완결한다. 새 소스 검증/적용은 대체·백업·정리·Timing 없는 소비 0건의 실제 근거 뒤다. 첫 BEFORE 전 catalog/d/profile/runtime/lowest/settings를 공통 동결한다. 원본012/017·Smol d·새 38단계 기록·runtime7은 보존하고 재측정/삭제/reader 제거 실적은 실제 근거로 기록한다.

## 8. 캠페인 manifest

generator가 실측 profile/SLO·승인 계수로 d의 N/K/L·준비·본창·setup·drain·collection을 계산하고 `conditions.inputsJSON`에 입력 ID·원시 위치·계산값을 동결한다. M5에서는 계산 책임을 측정 controller로 옮기고 CR 조건 필드를 명시적 재정의에 사용한다. 계수/판정 변경은 승인 범위로 관리한다.
`remove`는 해당 profile의 Accepted prepare에서 관측한 정확한 Pod UID/container ID를 쓴다. idle/power에도 identity를 유지하고 `measured-*`는 논리 슬롯이다. 교체/physical source 누락은 atomic exact-identity 검증 실패이며 immutable record를 보존한다.
`Refused`/`Insufficient`/`Failed`면 해당 노드 레인을 멈춰 부분 근거·자연 반환을 보존한다. 재측정은 원인·승인 범위 확인 후 새 이름 요청으로 한다.

## 9. 검사 계약

검사 근거는 실제 source SHA/실행과 연결하고 설계 수용 조건과 배포·검증 실적을 구분한다.

| 대상 | 확인할 불변식 |
|---|---|
| API/admission/격리 | 필드 조합·spec 불변·profile/Node/CPU·실제 gateway cap 사전 거절·노드 순서·taint/서빙 격리 |
| operation/민감도 | 실제 identity/effect·부분 실패/sequence/반환·상수 S=0/offset 불변/내부 극값·원 gap 합집합/cap·실제 support/bracket·undefined 비율·10% 초과 Accepted |
| minimumGain | nominal 불변·signed 상쇄/사슬 합성·이득=B keep/>B 전환·keep infeasible 회복·exact baseline의 무관한 profile CI 제외 |
| d/소비 | 물리 member/연속성·HTTP 실패 후 실행/late native·끝단/누락 unknown·idle 비율/1 ms 경계·식별 시 nanos dRange/κ 상한/미식별 생략·모든 사유·signed/max0·최신 Accepted identity/source 교체/반환 |
| lifetime/observer | L≥SLO/cap≥L/socket>L+delivery·sender/m 단일 차감·producer end 뒤 final read·동결 event/result byte·authoritative NotFound/transport 구분 |

## 10. 캠페인 범위와 증거

E5 전 범위는 Node당 기준 mode의 필수 profile·d·정확 비용이며 mode 확장은 E5 뒤 별도 범위다. runtime 7종 digest와 원본012/017 raw/status·replacement 출처를 보존한다(017 비실행 Measuring/Granted 예외). 실행 순서/E5 입구는 [POOL 검증 계획](workload-pools.md#9-검증-계획)을 따른다.
조건 생성기·승인 원문·manifest·profile/runtime/NodeUID/source SHA·원시 위치를 선별하며 `.token`·Secret 스냅샷·전체 캐시 백업은 저장소/증거 인덱스에서 제외한다.
- d 결정(`O/intent/intent-archive-20261030.tar.gz:d-measurement-decision.md`)
- 조건 생성기 provenance: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/gen.py` (원래 실행 위치; 현재 파일 부재)
- [승인 manifest](/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/phase-a/measurement-generalization-20261024/approved/)
- 비용과 d 결정 근거(`O/intent/intent-archive-20261030.tar.gz:implementation-contract-20261024.txt`)
- D2와 E5 승인 근거(`O/intent/intent-archive-20261030.tar.gz:deployment2-e5-proposal-20261024.md`)

## 11. 같은 기종 Node 프로파일 재사용 — PR-01..04

### 11.1 저장과 판정

요구사항/허용값 근거는 [catalog SRS §7](../../spec/catalog/SRS.md#7-u4-같은-기종-프로파일-재사용)을 따른다. 새 CRD·spec/status 필드·일반화 엔진 없이 대상의 기존 BEO→ProfilingRun→불변 result/raw를 사용한다.
대상 BEO metadata annotation은 `chill.dacs.io/profile-reuse: '{"sourceProfileID":"chill-system/SOURCE-BEO/SOURCE-UID","maximumRelativeDifference":0.05}'`다. 두 필드 필수, unknown 필드·(0,1) 밖/비유한 허용 차이는 거절한다. 0.05는 계획 여유 25%의 1/5인 screening 권고이지 고정 검증/통계적 SLO·전력 오차 상한이 아니다.
원본은 같은 namespace의 **직접 측정** catalog profile이며 최신 조건 선택 후 유효해야 한다. 자기 자신·전이 재사용·무효화/삭제/대체되어 catalog에서 빠진 원본은 거절한다. 확인 BEO는 일반 독립 후보가 되지 않는다. 실패 annotation을 지워 짧은 측정을 승격하지 않고 사유/raw 보존 후 새 BEO로 독립 측정한다.
원본/확인 frozen condition 비교에서 plan 이름·Node UID·meter identity·GPU UUID·활성/유휴 창 길이만 제외한다. DeviceClass·image digest·모델/전처리/데이터셋·mode·CPU request/limit/affinity/격리/thread·accelerator 할당/권한·runtime 설정/version·initialized batches는 일치해야 한다.
확인 활성/유휴 창 각각 ≥30초, 기존 trial≥3, 같은 warmup/settling/관측 interval/timeout/observer image를 유지한다. 원본의 모든 실측 batch가 확인에 있어야 하며 빠진 batch를 추정 추가하지 않는다.
배치별 서비스 평균은 raw RUNNER SERVICE 표본 평균이다. 활성 평균 벽전력은 trial별 `activeWindowWallJoulesPerItem/idleBaselineSecondsPerItem`(같은 창 총 벽에너지/측정초)의 trial 간 산술 평균이고 유휴는 model-loaded idle baseline이다. 서비스시간으로 전력을 역산하거나 증분 전력을 전체 벽전력으로 비교하지 않는다.
각 지표의 `abs(target−source)/source`가 모두 설정값 이하면 Accepted, 분모≤0·결측/비유한·조건 불일치·차이 초과면 Rejected/독립 측정이다.
`CatalogView.reuseChecks`는 양쪽 ID/불변 결과 참조/Node·허용값·idle 차이·배치별 service/active wall 차이·판정/사유를 제공한다. 확인 result/raw/annotation은 BEO에 보존한다.
scheduler service는 Accepted/Rejected 모두 `{schemaVersion:"chill.dacs.io/profile-reuse-check.v1alpha1",check:…}`를 `serving/profile-reuse/<확인 profile ID>/<판정 본문의 SHA256>.json`에 PutImmutable한 뒤 catalog를 발행한다. `reuseChecks[].record`, Accepted `reuse.record`가 이를 가리킨다. annotation 변경/원본 무효화 거절도 새 content-addressed 기록이며 이전 판정을 덮어쓰지 않는다. refresh signature는 ID뿐 아니라 판정 본문을 포함하고 catalog는 클러스터 annotation/status를 쓰지 않는다.

### 11.2 대상 행과 물리 개체 계약

행 ID는 `확인 profile ID/reuse-<SHA256(sourceProfileID + NUL + 허용값)>`로 source/정책 변경이 immutable startup bundle을 덮어쓰지 않게 한다. `Reference`, `MeasuredAt`, frozen condition·NodeName/UID·meter·runtime metadata/delivery·idle baseline은 **대상**이다.
서비스 분포/증분 에너지는 원본, 메모리는 원본/대상 관측 최대 중 큰 값, 활성 평균 벽전력은 확인값이다. `Evidence`에 양쪽 result/raw를 연결한다. GPU UUID는 Condition.RuntimeAccess/RuntimeMetadata/runtime delivery 모두 대상 확인 기록에서 가져오고 원본 UUID를 가장/대상 inventory에 넣지 않는다. CPU affinity는 임의 재배치 없이 같은 실측 mask만 허용하며 대상 Node UID 재생성 시 확인을 재사용하지 못한다.

| Node UID 소비 경로 | 계약 |
|---|---|
| catalog latest-condition | 원본 유효성 선택→확인 판정/대상 행 materialize→대상별 최신 선택 |
| scheduler_capacity bootstrap/nodePlans | UID guard 유지, 대상 UID 행만 대상 option 연결 |
| observer validateSourcePlacement | 실제 Node·DeviceClass/image/resource guard 유지, 원본 Node alias 금지 |
| operations validatePreparation | 대상 Profile/UID/runtime delivery로 prepare, source UID 예외 없음 |
| firstfloor client/native metadata | 대상 UID·NodeName·actual runtime 검사 유지 |
| runnerPreparation | 대상 UID/mode/model 최신 준비 행, source UID fallback 금지 |
| cost_power validatePowerNativeProfiles | 대상 UID·실제 UUID/image 검사 유지 |
| measuredGPUs/manual_reset/measurement_sequence | 대상 metadata만 inventory/측정 후보, source UUID 오염 금지 |
| opcost measurementProfile(비용/occupancy 공통) | BEO UID 직접 profile ID 또는 `reuse.ConfirmationProfileID`가 같은 Accepted 행으로 해석, 실제 파생 ID로 preparation 조회. 원본 BEO ID alias 금지 |
| cost/d/idle | d·prepare/remove/power 비용은 대상 Node/profile ID별 별도 측정, source 비용 복제 금지 |

### 11.3 CampaignOps: genesis2→genesis3 한 건 실증 절차

CampaignOps 한 명만 클러스터를 변경한다. 아래는 **실증 성공 기록이 아닌** 조건/증거 계약이다.
- 레인이 비고 두 Node serving/measurement 예약이 없는 상태에서 Node/DeviceClass/source BEO/result/catalog 원시 응답과 정확 UID·label·image/mode·quota/affinity·배치/평균을 남긴다. 읽기 전용 `nvidia-smi -q`의 UUID/driver/VBIOS/기본 power limit도 보존한다. genesis2/3 기본 power limit 250/300W 관측 때문에 이름만으로 동등 판정하지 않는다.
- genesis2 유효 PLC `ort-cuda` 한 행을 우선 사용한다. U3 locked는 양쪽 같은 mode 원본이 있을 때만, fixed 원본→locked 확인은 금지다. runtime 7 digest는 유지한다. 새 대상 BEO의 annotation에 실제 source ID를 넣고 targetNode만 genesis3, 활성/유휴 각각 30 s, metric은 경계를 덮도록 한다. warmup/settling/trial≥3/affinity/quota/batch를 유지하며 UID/resourceVersion/status/ownerReferences와 원본 spec/status는 복사/수정하지 않는다.
- 정상 admission/Complete 후 catalog의 `reuseChecks`와 세 종류 차이≤0.05를 확인한다. 거절은 사유/차이/raw를 보존하고 독립 측정하며 허용값 확대·batch 제외·값 복제·강제 Complete로 통과시키지 않는다.
- 대상 파생 ID·genesis3 UID/UUID/meter·원본 service samples/energy, preparations/snapshot option 연결을 확인한다. 더 최신인 기존 독립 profile은 삭제/무효화하지 않고 최신 선택 이유를 기록하며 새 확인이 최신인 조건으로 실증한다.
- 용량 연결은 별도다. 같은 UID/mode/runtime/model 기존 Accepted d와 exact profile ID별 비용 적용 범위를 확인하고 필요 비용은 정상 OCM으로 잰다. source d/cost는 복제하지 않는다. 대상 option/profile ID·d source·κ 및 prepare→native actual read-back→remove/hand-back의 UUID/자원 반환을 보존한다.
- 배포 SHA·양쪽 BEO UID/annotation/result/raw·profiles(reuseChecks/행/preparations)·snapshot option/row·실제 Pod/native UUID·성공/거절 사유·반환을 원시 디렉터리에 저장해 U2 표에 연결한다. 짧은 확인 시간은 독립 전체 측정 한 건 절감과 구분해 보고한다.

회귀 범위는 허용 경계/설정 변경·기종/image/자원 불일치·source 무효화/최신 선택·batch/전력 결측·대상 UUID·양쪽 독립성·planner option·확인 BEO의 비용/occupancy 파생 행 해석 및 실제 preparation 연결이다.

## 12. 대시보드 측정 API 보강 계약 (B0, 2026-10-30)

**설계 확정·구현 전.** [B0 계획](../../plans/2026-10-30-dashboard-api-gaps.md)의 B1/B2가
이 절을 구현한다. 화면 계약은 [Dashboard §8](../dashboard/README.md#8-측정실험-ux와-커버리지-2026-10-30),
요구사항은 [catalog SRS §9](../../spec/catalog/SRS.md#9-측정-커버리지-조회-api)다.
기존 CR, raw/result JSON과 검증·admission 판정은 바꾸지 않는다.

### 12.1 B1 — 권위 있는 정적 runtime 호환성

`GET /api/v1/model-compatibility?modelSpec=<name>&executionPath=<name>&deviceClass=<name>&node=<name>`
한 건을 읽는다. 네 query는 각각 정확히 한 번 필수이며 추가/중복 query, 빈 값·잘못된 이름은 400이다.
Console query 경계가 이름/참조·Node의 class selector membership과 사실 입력의 구조·의미를 검증한다.
ModelSpec/DeviceClass/Node 미존재는 404, 권한 거부는 403, 사실 조회 실패는 503이다.
누락·모순된 runtime 선언은 성공 응답의 `Unknown`이며 빈 지원 목록이나 `Incompatible`로 숨기지 않는다.
설정된 fact-source ConfigMap/키 자체의 부재도 선언 부재로 `Unknown`이다.
strict 선언 해석 실패·불완전한 RKNN 설치 계약은 `RuntimeDeclarationInvalid`로 반환한다.
선택되지 않은 path와 class selector에 속하지 않는 Node 조합은 400이며, API identity가 깨진
저장 객체·권한 외 API 읽기 오류는 판정으로 위장하지 않고 503이다.

응답은 `{readAt,selection,inputs,verdict,reasons}`다.

| 필드 | 계약 |
|---|---|
| `readAt` | RFC3339Nano UTC 조회 시각. 여러 Kubernetes GET을 atomic snapshot으로 주장하지 않음 |
| `selection` | query의 `modelSpec,executionPath,deviceClass,node` 원문 이름 |
| `inputs.modelSpec`, `inputs.deviceClass`, `inputs.node` | 각각 `{name,uid,resourceVersion}` |
| `inputs.runtimeDeclaration` | `{namespace,name,uid,resourceVersion,key,image}` 또는 null. 선택 실패 시 image를 꾸미지 않음 |
| `verdict` | 기존 `modelcompat`의 `Compatible`, `Incompatible`, `Unknown` |
| `reasons[]` | `{kind,code,message}`. 기존 `Conflict`/`Unknown`·reason code 재사용. artifact supply architecture 불일치는 `ArtifactArchitectureMismatch`; 선언 부재/모호함/무효는 `RuntimeDeclarationMissing`/`RuntimeDeclarationAmbiguous`/`RuntimeDeclarationInvalid`. Compatible이면 빈 배열 |

`candidate_resolver.go`와 `execution_facts.go`의 **동일 trusted fact-source, class/Node scope,
strict declaration 해석** 및 `modelcompat.Evaluate`를 재사용한다. 필요한 읽기 부분만 기존 kube
adapter에서 공유하고 Console에는 composition-root callback으로 연결한다. Console/serving이
profiler controller를 import하거나 별도 runtime 지원 registry·backend 이름 추측표를 만들지 않는다.
실행 선언 선택은 기존의 유일한 해당 class/Node/family/backend 규칙을 유지한다. 후보 없음은
Unknown이다. 기존 resolver가 선택한 선언의 명시적 충돌과 artifact architecture 충돌만 Incompatible이다.
같은 class의 여러 Node 판정은 각각 조회하며 Node-bound GPU 선언을 class 전체로 확장하지 않는다.

이 판정은 **현재 선언에 근거한 정적 runtime 지원**이다. 실제 runtime probe, 현재 Node Ready,
전력모드/online CPU 적격성, 전처리·데이터셋 호환성, 측정 완료나 admission 성공은 아니다.
power mode가 다른 열은 같은 정적 결과를 재사용할 수 있지만 별도 eligibility를 합성하지 않는다.
화면은 Compatible을 ‘정적 실행 지원 확인’, Incompatible을 ‘지원 안 함 — 이유’,
Unknown을 ‘지원 확인 불가 — 이유’로 쓰며 증거 상태와 독립 표시한다.
최종 측정 요청은 기존 dry-run/preflight/admission을 그대로 통과해야 한다.

구현: Console query callback은 기존 `KubernetesCandidateResolver.ResolveModelCompatibility`를
연결한다. Profiler preflight와 동일 selector/strict decoder/정적 비교 및 RKNN 선언 요건을
공유하며, 기존 preflight의 성공·거절 흐름은 바꾸지 않는다. public proxy의 정확
`/chill/api/v1/model-compatibility` GET grant만 추가하며 기존 Console fact-source 읽기 권한을 쓴다.

R3 추가: `GET /api/v1/model-compatibility-matrix`는 query 없이 전체 inventory의
모델×실행 경로×DeviceClass를 한 번 조회한다. 응답은 `{readAt,scope,rows}`이며 각 row는
위 B1 응답과 같다. class의 이름순 첫 Node를 대표로 기존 B1 resolver를 호출하고 전력모드 간
판정을 공유한다. Node가 없거나 개별 판정 조회가 실패하면 Unknown row를 보존한다.
대표 Node 지원을 class의 다른 Node로 확장하지 않으며 Node 필터가 대표와 다르면 UI 분모에서 제외한다.
행렬 전체 미제공은 503, 임의 query는 400, Cache-Control은 no-store다.
ChillServer proxy·SAR·RBAC에 정확한 `/chill/api/v1/model-compatibility-matrix` GET만 추가한다.
클라이언트는 Compatible만 분모에 포함하고 Unknown 수를 별도 표시한다. 선택 패널의 개별 B1 GET은
행렬을 바꾸지 않으며 실제 요청 전 기존 preflight를 유지한다.

### 12.2 B1 — 실패 BEO와 소유 Run의 공개 연결

기존 `GET /api/v1/namespaces/{namespace}/profiling-runs`의 `runs[]`에
`ownerReferences:[]`(Kubernetes `metav1.OwnerReference` 원형)와 `labels:{}`를 추가한다.
labels는 기존 `edge.dacs.io/batch-energy-optimization`, `edge.dacs.io/workload-mode`,
`edge.dacs.io/batch`, `edge.dacs.io/attempt` 네 key만 투영한다.
없는 값은 만들어 넣지 않는다. ownerReferences가 없으면 빈 배열이다.
추가 저장·CRD·owner 재작성은 없다.

소유 판정은 같은 namespace에서 `controller=true`, `apiVersion=edge.dacs.io/v1alpha1`,
`kind=BatchEnergyOptimization`, **BEO UID가 정확히 일치**할 때만 한다. 이름/label만으로
소유를 확정하지 않는다. 원래 `spec.measurement.batch/workloadMode`와 attempt label을 표시하며
정수 ≥1이 아닌/없는 attempt는 ‘회차 기록 없음’이다. label 해석은 API projection 경계에서
소유를 판정하는 새 gate가 아니며 원문을 보존한다. parent가 삭제되거나 같은 이름 다른 UID면
옛 UID의 owner 사실만 보여 준다. 전체 목록 page 확인 전 ‘소유 회차 없음’을 확정하지 않는다.

실패 BEO result 응답의 `result:null`은 그대로다. finalized evidence가 없는 Run도 목록의
spec/status/owner를 읽을 수 있고 Passport 성공은 소유 연결의 전제 조건이 아니다.
Run별 조건·attempt를 묶을 뿐 active/idle/배치를 통틀어 새로운 1..N 회차를 만들지 않는다.

### 12.3 B2 — 저장 원시에서 읽는 배치 CI·통합 p95

`GET .../batch-energy-optimizations/{name}/result`의 기존 `campaign/result`는 그대로 두고
형제 필드 `statistics`와 `statisticsReason`을 추가한다. **sealed `result.detail`을 확장하거나
다시 쓰지 않는다.** CI는 기존 유도 과정에 존재하고 latency vector는 raw에 저장돼 있으므로
생성 경로 변경보다 현재 보존 자료의 읽기 projection을 택한다. result.detail에 통계가 없다는
것만으로 재측정을 요구하지 않는다.

`statistics`는 `{batches:[...]}` 또는 null이며 batch 행은 다음과 같다.

| 필드 | 의미·단위 |
|---|---|
| `batch` | 실제 completed active trial의 batch 크기 |
| `incrementalWorkJoulesPerItem` | 기존 `DeriveCurve`의 독립 active trial별 J/item 추정치 산술 평균. 선택 결과의 평균과 다른 새 목적함수 아님 |
| `activeTrialCount`, `idleTrialCount` | 해당 point에 쓰인 독립 active Run 수와 공유 model-loaded idle Run 수 |
| `confidence` | 기존 `PointConfidence` 그대로: `estimatorVersion,method,level,varianceOfMean,effectiveDegreesOfFreedom,halfWidth`. halfWidth는 J/item, variance는 (J/item)². 유효 자유도 부재는 기존 omitempty 의미 유지 |
| `latencySampleCount` | 동일 batch의 완료된 client 추론 호출 latency vector 원소 수. item 수·Run 수와 다름 |
| `latencyP95Milliseconds` | 해당 batch의 모든 active trial **client 호출 지연 표본을 합친** Gonum empirical 0.95 분위, ms. batch 1건 호출 시간이며 batch로 나누지 않음 |
| `sources[]` | 실제 사용한 기존 `{store,key,size}` 원시/실행/curve-plan 식별자. raw 다운로드 URL이 아님 |

CI는 기존 shared-idle variance와 Welch–Satterthwaite/Gonum Student-t 계산을 그대로 사용한다.
양측 구간은 평균±halfWidth다. 하한을 0으로 자르지 않고 variance=0/halfWidth=0은 유효 관측이다.
`level`은 이 BEO의 기존 pointPolicy confidence 해석값이며 전역 settings나 표시용 95%를 덮어쓰지 않는다.
서로 다른 batch/조건, excluded trial, 재사용 확인 trial을 섞지 않는다. trial별 p95 평균이나
기존 point의 ‘trial p99 중 최댓값’을 p95로 이름만 바꾸지 않는다.

Console composition root의 읽기 callback은 **저장된 result.completedTrials와 curvePlan만**
따라 기존 verified evidence loader → `profiler/projection` → `DeriveIdleBaseline`,
`VerifyCurvePlan`, `DeriveCurve`를 재사용한다. `statistics.go`의 percentile을 사용하는
좁은 summary 함수를 같은 derivation package에 둔다. 새 통계 구현·quality gate·검색 재실행,
현재 catalog 최신 profile 선택, CR status/result mutation은 없다.
Console transport가 profiler controller를 import하지 않으며 기존 port/callback 조립 패턴을 따른다.

검증은 저장 자료가 verified 타입으로 들어오는 경계 한 번이다. result의 Run namespace/name/UID,
owner UID·completed reference, plan의 정확 조건과 raw lineage를 기존 검증기로 연결하고 downstream은
그 타입을 소비한다. 새 digest 재계산·문자열 hash 검사·바이트 동일성 gate를 넣지 않는다.
실제 다운로드의 기존 유한 크기 제한은 유지하되 payload 재인코딩을 identity 판정에 쓰지 않는다.
요청 취소/기존 timeout을 지키며 목록·커버리지 전체에 통계를 일괄 로드하지 않는다.

구현(2026-10-30): `cmd/console/measurement_statistics.go`가 retained loader의 동결 store
binding과 기존 Run/원시 검증을 사용한다. `Curve.Statistics()`는 같은 empirical percentile
구현을 재사용하며 공유 idle와 해당 batch의 active 원시·execution, curve-plan만 `sources`에 넣는다.
curve-plan 단일 객체와 retained evidence 객체는 각각 64 MiB로 읽기를 제한한다. 현재 Secret/권한
조회 실패는 원근거 미기록으로 바꾸지 않는다. 공개 경로는 기존 result GET과 ChillServer 프록시를
그대로 사용하므로 새 route·RBAC grant는 없다.

| 상황 | 응답·표시 |
|---|---|
| result reference 없음 | `statistics:null`, `statisticsReason:"NoPublishedResult"`; 기존 result:null 유지 |
| 정상 참조지만 보존 raw/Run이 실제 없음 | `statistics:null`, `statisticsReason:"RetainedEvidenceMissing"`; sealed 결과는 읽히며 ‘통계 원근거 기록 없음’ |
| 이 측정 방법에 client latency/통계 입력이 애초에 없음 | `statistics:null`, `statisticsReason:"StatisticsNotRecorded"`; ‘CI·통합 p95 기록 없음’ |
| 통계 제공 | statisticsReason은 null. 통계가 없는 것을 숫자 0으로 치환하지 않음 |
| 권한·transport/store 실패·깨진 객체·UID/조건 모순 | 기존 오류 전달(403/502/503 등 해당 경계). 기록 없음으로 숨기지 않음 |

### 12.4 B2 — OperationCost 실측 시간·J

기존 list/detail `GET /api/v1/namespaces/{namespace}/operation-cost-measurements[/{name}]`의
`status.steps[]`에 `operationID`, `durationNanos`, `wallJoules`, `reason`을 추가한다.
durationNanos는 nullable 정수 ns(브라우저는 lossless 정수 처리), wallJoules는 nullable number J다.
기존 name/key/verdict, CR 전체 phase/reason, references.evidence는 그대로다.
`durationNanos=record.result.endedAt−startedAt`이며 유효한 실제 양 끝 시각이 없으면 null이다.
planned idleDurationSeconds·watchdog/관측 창 길이를 실측 시간으로 대체하지 않는다.
wallJoules는 저장된 수용된 **전체 Node 벽에너지**이며 증분 J/item이 아니다.
불수용·실패·isolation 미확인에서 기록된 수용 에너지가 없으면 null, 실측 0 J는 0이다.
실패에 실제 시간이 있으면 시간만 제공할 수 있다. 미실행 후속 단계 status 행은 만들지 않는다.

ChillServer measurement service가 CR의 **정확 step key만** 기존 evidence store에서 읽는다.
전체 prefix/latest 비용을 선택하는 `CostEvidence.Refresh`는 실패 이력을 버리므로 재사용하지 않는다.
기존 `serving/kube` operation record decoder/identity 검증을 좁게 재사용하여 CR UID로 만든
operation ID·step kind·Node·request/result identity·실제 창을 한 경계에서 확인하고 안전한 DTO를
`measurement_projections.go`에 전달한다. accepted cost와 idle baseline의 기존 분기를 유지하고,
failed/insufficient history에 Accepted의 완결 조건을 잘못 강제하지 않는다.
raw power·prepare payload/비밀을 HTTP에 복사하지 않는다.
참조 key가 있는데 객체를 못 읽거나 깨졌으면 evidence 오류이지 null/‘미측정’이 아니다.
개별 단계 Accepted를 전체 순서 Complete로 승격하지 않는다.

구현(2026-10-30): `LoadOperationCostSteps`가 저장된 status 순서/이름과 exact key를 읽고,
기존 accepted cost/idle validator를 해당 분기에만 적용한다. list와 detail은 같은 loader를
사용하며 `maxEvidenceBytes`로 객체별 읽기를 제한한다. phase와 verdict는 CR 원문 그대로이고
실패 기록의 actual interval은 보여 주되 accepted 에너지로 승격하지 않는다.
목록은 기존 `errgroup`의 `SetLimit(8)`로 CR별 projection을 최대 8개 동시 실행하고,
미리 정렬한 index에 결과를 기록해 이름 순서를 보존한다. 각 CR의 단계 읽기는 원래 순서이며,
어느 projection이든 실패하면 요청 전체가 오류로 끝나고 나머지 읽기에는 취소가 전달된다.

## 13. 측정 커버리지 읽기 projection (U1)

**2026-10-30 교차 모델 검토 반영, U1 서버 구현. 배포·성능 수용 전.** [5차 계획](../../plans/2026-10-31-dashboard-measurement-redesign.md)과
[Dashboard §9](../dashboard/README.md#9-운영-중심-측정실험-5차-재설계)가 소비한다.
사용자는 엣지 클러스터 운영 담당자다. 화면은 ‘스케줄러가 쓸 수 있는가, 운영에 쓰기 전에 무엇이 부족한가’를 답한다.
§12의 resolver·owner·보존 통계·단일 writer를 재사용한다. CRD/spec/status/result/raw/frozen bundle/
publication·catalog 채택·admission·통계 추정 정책은 변경하지 않는다. 새 필드는 HTTP/메모리 projection이다.

### 13.1 소유권과 중복 I/O 제거

집계는 **ChillServer**, 정적 호환성/개별 보존 결과 조회는 **Console** 소유다.
catalog의 atomic 읽기 snapshot(`measurementCatalogView`)과 기존 namespaced measurement reader를 조합한다.
스케줄러 실제 채택 여부는 별도 기존 `profilesView`의 ID 집합으로 확인한다.
Console에 스케줄러/catalog를 새로 만들거나 ChillServer가 채택 profile 전체를 Console `/result`로 크롤링하지 않는다.

```text
브라우저 GET 1회 → BFF → ChillServer 인증 + URL/namespace SAR → coverage snapshot
  ↑ catalog가 이미 읽은 verified trial의 measurementFacts / resultRows
  ↑ namespace BEO·Run metadata / inventory / Console의 정적 지원 snapshot
catalog 밖 단건 상세만 → 기존 Console /result → retained loader·같은 통계 helper
```

- `internal/serving/kube/catalog.go:readProfile`은 이미 result/curve-plan/active·idle raw를 읽는다.
  **그 로딩 안에서** 기존 DeriveIdleBaseline/Aggregate/통합 client percentile helper를 재사용해
  batch별 CI/p95/W/총·증분 J/반복·품질을 한 번 산출한다. 원시 vector는 산출 후 버리고 복사·재다운로드하지 않는다.
  기존 `catalogBatch.add`가 client vector를 버리기 전에 pooled client p95를 산출해야 하며 native duration으로 대체하지 않는다.
- `CatalogView`의 메모리 sidecar `measurementFacts`(profile/result identity key)에 보존하고
  `ProfilesView` snapshot과 함께 publish한다. immutable NativeBundle/CR/result JSON에는 새 필드를 쓰지 않는다.
  현재 catalog에 없는 superseded·scope 밖 단건은 기존 Console result GET에서 같은 helper로 계산한다.
  이 단건 비용을 전체 coverage 준비의 선행 조건으로 만들지 않는다.
  검증된 catalog 읽기 projection은 비용 source 갱신 전에 publish하여 비용 source 장애가
  BEO 커버리지를 막지 않는다. 이는 스케줄러 채택을 앞당기지 않으며, 아직 채택하지 않은
  검증 결과는 `scope.schedulerUsed:false`로 구분한다.
- catalog 제공 경계에 **마지막 source 확인 성공 시각·마지막 오류·revision**을 추가한다.
  정상 무변경 refresh의 조기 반환도 성공 확인 시각을 갱신하고 실패는 마지막 성공 시각을 유지한다.
  atomic pointer를 지금 읽은 시각이나 configVersion만으로 fresh를 선언하지 않는다.
- Console 호환성 행렬은 한 호출 안에서 inventory LIST 객체를 seed한 **Get memo reader**를 사용한다.
  model/class/Node/ConfigMap의 고유 identity별 Get을 한 번만 수행하고 fact-source ConfigMap은 한 번만 strict 파싱한다.
  기존 selector/`selectExecutionDeclaration`/modelcompat 판정과 취소를 유지하며 **8개 동시 resolver 안은 삭제**한다.
  대표뿐 아니라 해당 inventory의 모든 실제 Node 판정을 같은 입력 snapshot에서 계산해 두므로
  `node` 선택 GET에 새 resolver I/O가 없다. 입력 UID/RV가 달라지면 다시 계산하며 heartbeat를 지원 변경으로 과장하지 않는다.
  기존 공개 matrix의 `rows`는 대표 Node 의미를 유지한다. 같은 응답의 새 `nodeRows[]`는
  B1 응답 shape로 model/path/class/실제 Node 각각의 판정을 제공하며 coverage만 이를 index한다.
- `cmd/console`에 `--kube-api-qps`, `--kube-api-burst`를 추가하고 양수·유한 범위를 검증해 REST config에 적용한다.
  기본값은 기존 ChillServer와 같은 200/400, 배포에서는 실제 설정과 API-server 부하를 기록한다.
  높은 QPS는 memo/단일 파싱의 대체가 아니며 기본 client QPS에 대한 패널 간 다른 추정을 성능 근거로 쓰지 않는다.
  구현 설정은 Helm `console.kubeAPIQPS`/`console.kubeAPIBurst`에 노출한다. 행렬 composition root는
  inventory LIST seed와 fact-source GET의 UID/RV 집합이 같으면 기존 `rows`/`nodeRows`를 재사용한다.
  성공 조회 시각만 바뀐 경우 지원 판정을 재계산하지 않는다. 성능·배포 수용은 §13.6의 별도 실측 대상이다.
- Run은 namespace LIST 한 번에서 UID/owner index를 만든다. 공개 page마다 전체 LIST를 반복하지 않는다.
  owner는 §12.2의 정확 controller UID 규칙이다. operation/occupancy 전체 evidence scan은 coverage에 넣지 않는다.
- 순수 공통 판정/DTO는 `internal/operatorconsole/measurement_coverage*.go`에 둔다.
  `internal/serving/httpapi`는 operatorconsole을 직접 import하지 않는다. `cmd/chill-server` adapter가
  공통 판정을 transport DTO에 연결한다. 기존 package 방향과 `.go-arch-lint.yml`은 변경할 필요가 없다.
  기존 목록·단건에도 동일 reducer의 assessment를 전달하므로 단건 조회가 전체 coverage grant/준비에 종속되지 않는다.

### 13.2 경로·필터·RBAC

`GET /api/v1/namespaces/{namespace}/measurement-coverage`

서버 query는 **`batches`, `node`만** 허용한다. batches는 중복 없는 오름차순 양의 정수 CSV,
node는 현재 inventory의 정확 이름이다. 각 query는 한 번, 빈/중복/unknown key·범위 오류는
400 `InvalidCoverageQuery`다. model/class/path/mode/status/CPU 검색은 클라이언트 표시 필터와 URL 상태다.
선택 query는 namespace base snapshot의 가벼운 projection이며 raw scan을 시작하지 않는다.
node별 지원 입력이 아직 없으면 Unknown과 사유를 반환하고 대표 Node 지원을 복제하지 않는다.
목표 batches는 실제 요청/지원 집합과 별개다. 생략하면 `goalBasis:"requested"`, 명시하면 `"explicit"`이다.
무한 CPU/dataset/image 조합이나 임의 모델 최대 batch를 분모로 만들지 않는다.

cache 접근 전 기존 TokenReview와 정확
`get /chill/api/v1/namespaces/{namespace}/measurement-coverage` URL SAR를 수행한다.
이 grant는 inventory와 해당 namespace의 catalog/측정 요약을 읽는 명시적 권한이며 다른 URL grant에서 유추하지 않는다.
또한 기존 `h.authorizer.AuthorizeResource`로 **같은 namespace의 `edge.dacs.io/v1alpha1`
`batchenergyoptimizations` list와 `profilingruns` list**를 검사한다. 단순 URL ClusterRole만으로 namespaced Role을 우회하지 않는다.
`measurementService.namespace` 허용 범위도 유지한다. 다른 namespace reuse 원본은 별도 권한 없이 노출하지 않는다.

| 상황 | 응답 |
|---|---|
| 토큰 없음/무효 | 401, cache 데이터 없음 |
| URL/namespace resource SAR 거부 | 403, cache 데이터 없음 |
| 인증/SAR 서비스 장애 | 503 `AuthorizationUnavailable`, 오래된 데이터도 제공하지 않음 |
| 초기 필수 입력 없음 | 503 `CoverageWarming`, `Retry-After: 2`; 빠른 오류를 사용 가능 응답으로 세지 않음 |
| 일부 source 갱신 실패 | 200 partial, source 오류/영향 범위·마지막 기준 시각 보존 |
| 유효 stale 범위 밖 필수 입력 없음 | 503 `CoverageUnavailable` |

HTTP `Cache-Control: no-store`, BFF allowlist에 정확 GET 및 응답 `Retry-After` 전달을 추가한다.
write wildcard/자동 재요청/raw export 권한은 추가하지 않는다.

### 13.3 응답 schema

시각 공개 필드는 **`dataAsOf`와 `freshness`만** 둔다. dataAsOf는 필요한 source들의 마지막
성공 확인 시각 중 가장 오래된 값(RFC3339Nano UTC), freshness는 `fresh`/`stale`이다.
source별 마지막 확인 시각은 내부 snapshot에서 보존하고 HTTP에는 version/오류만 보낸다.
atomic 다중 source snapshot이라고 주장하지 않는다. null은 이유 있는 미제공이며 관측 0이 아니다.

| 필드 | shape / 의미 |
|---|---|
| `schemaVersion`, `snapshotId`, `assessmentVersion` | `"measurement-coverage.v1"`, namespace revision, `"result-checklist.v1"` |
| `dataAsOf`, `freshness`, `completeness` | 기준 시각, fresh/stale, complete/partial |
| `scope` | `{namespace,supportBasis:"representative-node-static"\|"selected-node-static",goalBasis:"requested"\|"explicit",targetBatches:[]}` |
| `sources[]` | `{kind,resourceVersions,complete,error:{code,message,retryable}\|null}`. inventory/compatibility/catalog/BEO/Run 구별 |
| `summary` | supportedCellCount, completeCellCount, criteriaMetCellCount, reusedCellCount, unknownSupportCount, unsupportedCellCount, countsByDisplayStatus, requestCount, runCount, profileCount, failedCellCount, currentFailedRequestCount, historicalFailedRequestCount. 미확정 수 null |
| `groups[]` | 모델×class `{model:{name,uid},deviceClass:{name,uid},cellIds:[],complete,supported,unknownSupport}`. 장치 열 이름순 고정 |
| `cells[]` | 아래 Cell |
| `entities` | requests/profiles는 `{uid,id,status,label,cellIds}`, runs는 `{uid,status,label,cellIds}`인 scalar 화면 요약만 제공한다. assessment/checks/resultRows/원문/log 없음 |
| `nextActions[]` | `{kind,cellId,variantId,entityId,reasonCode,message}`. 미해결 실패/부족 batch/근거 확인만 각 최대 3건 |

operationCosts/dispatchOccupancies는 coverage index에서 제외한다. 기존 각각의 list/detail response에
동일 reducer assessment를 붙인다. 이들 source 실패가 BEO coverage를 막지 않아야 한다.
Run 상세도 기존 허용 조회 경로에서 assessment를 얻을 수 있어야 하며 aggregate grant를 강제하지 않는다.
모든 entities의 status는 서버의 화면 displayStatus이며 label도 서버 값이다. 원본 phase/세부 사유는 기존 단건에서 읽는다.
catalog 내부 메모리 facts 타입은 기존 serving 경계에 두고 cmd adapter에서 공통 HTTP DTO로 변환한다.
catalog/kube가 operatorconsole transport를 import하도록 package 예외를 추가하지 않는다.

**Cell**: model UID × class UID × path × mode다. 각 이름/UID와 `support`, `assessment`,
`variantCount`, `variants`, nullable `batchProgress`/`representative`, `failureSummary`, `history`를 갖는다.
support는 `{verdict:"Compatible"|"Incompatible"|"Unknown",node:{name,uid}|null,reasons:[]}`다.
variant는 frozen Node/image/artifact/path/dataset/preprocessor UID·CPU/quota/affinity/protocol/metricSource까지
같은 조건의 `{id,condition,assessment,batchProgress,resultRows,requestIds,runIds,profileIds}`다.
`batchProgress={basis,target,measured,criteriaMet,missing,inProgress,unknown}`이며 각 집합은 batch 정수 배열이다.
**variant가 2개 이상이면 Cell.batchProgress와 representative는 null**, ‘조건 k개’만 표시한다.
합집합/교집합으로 가짜 완성 조건을 만들지 않는다. variant 선택 후에만 그 batch/대표값을 보인다.
대표값은 `{variantId,batch,incrementalJoulesPerItem,p95Milliseconds}`로 같은 실제 batch에 한정한다.
failureSummary는 정확 request/Run identity·batch·code·exitCode·최대 160자 한 줄 원인·후속 relation만,
긴 diff/log는 기존 상세에 둔다. 정확 후속만 verified-follow-up, 유사 조건은 related-candidate다.

### 13.4 단일 판정과 화면 상태

`MeasurementAssessment={status,displayStatus,label,reasons,execution,evidence,checklist,scope}`다.
execution은 원문 phase/generation/observedGeneration, evidence는 available/accepted와 사유,
checklist는 `{criteriaMet:true|false|null,version:"result-checklist.v1",checks:[{code,result,message,sourceIds}]}`다.
check result는 pass/warn/fail/unknown이다. **서버가 displayStatus와 label까지 제공**하며 UI는 다시 매핑하지 않는다.

coverage 응답에서는 **variant.assessment.checklist.checks에만** 전체 체크리스트를 한 번 둔다.
동일 `(code,result,message)` 항목은 variant 안에서 합치고 `sourceIds`는 해당 항목에 기여한
정확한 `ResultRow.id` 집합이다. 원본 기록은 해당 `resultRows[].sources`로 찾아간다.
Cell과 ResultRow의 assessment는 status/displayStatus/label, execution/evidence, reasons/scope와
checklist의 criteriaMet/version을 유지하되 `checks:[]`로 요약한다. 이 compact projection은
coverage 전용이며 기존 단건 assessment의 체크리스트 정책을 바꾸지 않는다.
모든 variant의 condition과 resultRows 전체를 보존하며 행·variant를 잘라 용량을 맞추지 않는다.
scheduler compact 읽기의 `$ref` 복원은 top-level Profile dictionary 전용이다.
coverage는 위 variant 체크리스트 중복 제거를 사용하며 별도 `$ref` parser가 필요하지 않다.

| 세부 status | 화면 displayStatus / label | 판정 |
|---|---|---|
| `complete` | `usable` / **사용 가능** | 채택된 정확 근거와 해당 목표 batch 측정 충족. 실행 phase=Complete만으로 만들지 않음 |
| `criteria_met` | `usable` / **사용 가능** | complete이며 아래 결과 체크리스트도 충족. 상세 사유 행에 **기준 충족** 표시 |
| `partial` | `insufficient` / **부족** | 목표 batch/선택 Node 일부 부족. 실제 부족 집합 명시 |
| `warning` | `problem` / **문제** | 실제 catalog 기각·근거 파손/누락·현재 선언 충돌·음수/비유한 d 등 해결할 문제 |
| `failed` | `problem` / **문제** | 사용 결과 없이 명시적 실행 실패. 사유 행은 ‘실행 실패’와 실제 원인 |
| `running` | `running` / **진행** | 사용 결과 없이 현재 generation의 실행/대기가 명시됨 |
| `unmeasured` | `unmeasured` / **미측정** | 지원 Compatible·필수 목록 완전·해당 시도 없음 |
| `unsupported` | `unsupported` / **지원 안 함** | resolver가 입증한 불일치이며 사용 근거 없음 |
| `unknown` | `unknown` / **확인 불가** | 필수 source/identity/phase 불명. 기존 검증 결과는 지우지 않고 미확인 범위만 남김 |

화면의 기본 상태는 **사용 가능/부족/문제/진행/미측정 5개**, 지원 안 함·확인 불가는 별도다.
9개 세부 상태를 별도 배지 벽으로 노출하지 않고 사유 행/용어 도움말 한 곳에서 설명한다.
superseded와 notInCatalogScope는 정상 측정 결과의 `complete`+보조 사실이며 ‘현재 스케줄러 미사용’을 명시한다.
실제 rejected/깨진 근거와 혼동하지 않는다. 완료 수 complete+criteria_met, 기준 충족 수는 부분집합,
재사용 완료 수는 따로 표시한다. 이전 유효 결과 뒤 새 실패는 기존 complete를 유지하고 최근 시도 실패를 병기한다.
실패 요청 자체는 failed, 후속 포함 셀은 다른 범위임을 명시한다. idle 3회를 active 3회로 세지 않는다.
여러 자식은 실제 문제→부족→전부 기준 충족→전부 complete 순으로 요약하고, 판정 불가 자식은
미확인 수/범위를 보존한다. 단순 통계 미제공이 채택 결과 완료를 지우지 않는다.

**결과 체크리스트 v1**: 정확 조건/lineage, 목표 batch 충족, 독립 active/shared idle 각각
`max(3,동결 MinimumTrials)` 반복, 유한한 기존 CI/level(0폭 허용), pooled client p95·N>1,
모든 사용 trial의 동결 전력창/표본/gap/실패 정책 통과, 실제 전력 출처를 확인한다.
미조회 체크는 unknown이지 pass가 아니다. checklist 미충족 자체를 모두 warning으로 승격하지 않는다.
**sourceTimestampAbsent와 node-wall은 complete/criteria_met 어느 쪽의 판정 게이트도 아니다.**
전력원 바로 옆에 ‘관측 시각은 수집 시각’, ‘노드 벽전력이며 장치 단독 전력 아님’ 고정 각주를 둔다.
미관측 발열/격리를 정상으로 꾸미지 않는다. reuse 행은 complete일 수 있으나 criteria_met는 아니다.
원본 CI 중첩만으로 유의성을 판단하지 않으며 새 검정/품질 gate를 만들지 않는다.

### 13.5 결과 행·measurementFacts·요청 검사

`ResultRow/resultRows[]`는 정확 variant × batch 한 행이다. 조건(model/class/Node UID·mode/path/
image/backend·batch·CPU/quota/affinity·dataset/preprocessor UID·protocol), p95Milliseconds,
latencySampleCount, idleWatts, activeWatts, totalJoulesPerItem, incrementalJoulesPerItem,
confidence, activeTrialCount, idleTrialCount, completedItems, completedCalls, powerSource,
assessment, sources, missingReasons를 포함한다.
catalog가 이미 로드한 verified trial의 동일 shared-idle derivation·산술 평균/pooled client percentile을 사용한다.
CI는 증분 J/item에만 적용하고 평균±halfWidth의 음수 하한/0폭을 보존한다.
총 J/item은 EnergyTrial.ActiveWindowWallJoulesPerItem 평균, active W는 같은 trial 유효 적분/실제 창 평균이다.
서로 다른 회차 처리량으로 W를 나누거나 idle W를 총 J에 단순 더하지 않는다.
재사용은 **대상 confirmation 독립 실측 행 + sourceProfile 참조 행** 두 종류로 구분하고
각각 rowKind/Node/profile/source/reuse 상대 차이를 둔다. 원본 에너지와 대상 W를 섞은 총 J는 null이다.
두 행이 대상의 독립 전체 실측인 것처럼 내보내지 않는다.

catalog 밖 단건 Console `/result`는 기존 result/statistics를 유지하고 nullable
`measurementFacts`와 `measurementFactsReason`을 형제로 제공한다.
facts는 `{request,resultReference,condition,pointPolicy,batches:[ResultRow의 사실 필드]}`다.
NoPublishedResult/RetainedEvidenceMissing/StatisticsNotRecorded와 실제 권한·transport·identity 오류를 구별한다.
채택 profile 상세는 coverage/catalog의 같은 resultRows를 재사용하고 Console 중복 로드를 하지 않는다.
raw/vector/log/Secret은 응답에 넣지 않는다. 기존 8MiB 한도는 유지하며 넘는 자료를 몰래 잘라 정상으로 표시하지 않는다.

구현: 공통 `AssessMeasurement`가 개별 요청/Run/동작 비용/점유와 커버리지의 상태·label을
산출한다. `MeasurementResultRow`는 catalog와 retained 단건의 같은 사실을 투영하며,
공유 idle 반복과 active 반복을 별도로 센다. 저장된 protocol에 표본/gap/실패 **판정 정책**이
없으면 `FrozenPowerWindowPolicy`는 unknown이다. 현재 관측값에 새 정책을 소급해
기준 충족으로 만들지 않는다. timestamp 부재/node-wall은 이 unknown의 원인이 아니다.
정상 superseded의 이미 검증한 메모리 facts는 현재 스케줄러 미사용으로 보존하며,
읽기 전 replacement 제외에는 확인하지 않은 과거 facts를 만들어 넣지 않는다.

기존 preflight 응답에 표시용 `batchPlan`을 추가한다:
`{variantId,conditionMatch,requestedBatches,requestableBatches,plannedBatches,measuredBatches,inProgressBatches,
missingBatches,subsetAllowed,searchMayStopEarly,reasons}`.
conditionMatch는 exact/planned-only/unknown이다. frozen image/조건 미확정은 exact로 승격하지 않는다.
셀에서 variantId를 전달하고 입력을 그대로 유지할 때만 그 missing을 초깃값으로 쓴다.
CPU/dataset/protocol 등 변경·선언 UID 변경은 이전 자동 제외/검사를 무효화한다.
검사에서 서버가 최신 요청 spec/정확 profile을 대조하고 planned-only 중복은 별도 사실로 보인다.
전달은 `POST .../preflight?variantId=<id>`의 선택 query이며 native CR 본문에 새 필드를 넣지 않는다.
preflight exact는 보존 CurvePlanSpec의 실제 identity·immutable image digest와 현재 요청
spec(배치 선택 제외)이 모두 일치할 때만 제공한다. 이 저장 계약에 없는
ModelSpec/DeviceClass/preprocessor 선언 UID를 과거 값으로 추정하지 않으며,
해당 lineage는 확인되지 않은 한계로 reasons에 명시한다.
진행 batch는 UID-bound 동결 curve-plan까지 일치한 현재 실행에서만 세며,
spec만 같은 요청은 planned-only 사유로 분리한다. missing은 측정·정확 진행 집합을 제외한 목표다.
`declaredBatchSizes` 경로는 부분집합 지정 불가: UI batch 칩 읽기 전용, 기존 batchSizes 생략 계약 유지,
‘요청 가능 범위’와 ‘실제 실행 예정 범위’, 중복 가능성·조기 종료 가능성을 펼쳐 확인받는다.
subsetAllowed인 경로에만 **‘부족 배치만 요청’**을 쓴다. 모든 batch 실제 실행을 보장하지 않는다.
지원 Unknown은 이유 코드별 담당 행동(선언 누락이면 관리자 설정 확인)을 제공한다.
이름 suffix는 검사 통과 때 고정하고 응답 미확인 후 자동 재생성/재제출하지 않는다.

### 13.6 cache·신선도·성능 수용

namespace별 프로세스 메모리 base snapshot 한 개, atomic 교체, namespace당 갱신 한 번만 둔다.
진입/명시 갱신에서 30초 fresh TTL을 적용하고 stale 최대 5분, 별도 DB/Console crawler/worker pool은 없다.
지원 snapshot은 입력 identity/RV가 바뀔 때만 재계산한다. catalog는 기존 refresh 흐름과 성공 확인 시각을 쓴다.
동일 서버 namespace TTL 안의 여러 탭은 한 갱신을 공유하고 브라우저 **30초 polling은 하지 않는다**.
재진입/명시적 갱신만 GET한다. 최초 GET이 warming이면 Retry-After 후 한 번의 완료 확인을 허용하되
지속 warming은 오류와 명시 재시도이며 숨은 무기한 polling으로 바꾸지 않는다.
source 실패는 partial/stale와 기존 dataAsOf를 유지한다. 지원 실패면 분모 null,
목록 불완전이면 미측정 수 null, catalog 오래됨이면 현재 사용 판정을 stale로 표시한다.
단건 비용/점유 장애가 무관한 coverage를 막지 않는다. 인증 변경/401/403 후 이전 cache 표시 금지다.

구현의 namespace cache는 처음 준비 중 503/`Retry-After: 2`를 반환하고, source별 성공 입력을
한 번의 갱신에 모아 교체한다. 실패 source는 최대 5분의 기존 입력과 마지막 성공 기준 시각을
유지하며 partial/stale로 보인다. Run LIST는 한 번의 namespace 갱신에서 UID/owner index로
사용하고 공개 page별 전수 LIST를 하지 않는다. 동작 비용/점유 source는 집계에 접근하지 않는다.

수용 데이터는 3모델/8class/88BEO/1,155Run/78profile, 동시 사용자 1·5다.
**① 성공 GET p95 <2초 ② 로그인/경로 진입부터 첫 사용 가능한 커버리지까지 p95 <2초
③ 채택 조건 선택부터 결과 행·기준 판정 완료까지 p95 <2초**를 각각 기록한다.
cold process 시작→첫 사용 가능한 커버리지·채택 행 판정 완료는 **30초 이내** 목표로 별도 수용한다.
warming/partial 비율과 최장 지속시간을 보고하며 정상 source의 안정 운영에서는 warming/partial이 남으면 실패다.
빠른 503/unknown/장기 미판정 200을 성공으로 세지 않는다. catalog 밖 단건은 기존 10초 조회 한도와
미제공 이유를 별도 측정하며 전체 준비를 기다리게 하지 않는다.
서버 catalog 시작/갱신·Kube GET 수·ConfigMap 파싱 수·인증·BFF·집계 CPU/I/O와 serving 제어 지연을 함께 기록한다.
이 시간은 설계 목표이며 달성 관측이 아니다. 기존 행렬 7.690초/Run 1행 3.861초를 그대로 재사용하면 수용 실패다.
