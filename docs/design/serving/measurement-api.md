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

## 13. 연구 커버리지 읽기 projection (U1)

**2026-10-30 설계, 구현 전.** [5차 계획](../../plans/2026-10-31-dashboard-research-redesign.md)과
[Dashboard §9](../dashboard/README.md#9-연구-중심-측정실험-5차-재설계)가 소비한다.
§12의 정적 resolver·owner·보존 통계 계약을 재사용하며 **CRD/spec/status, result/raw,
frozen bundle, publication, catalog 채택·admission·통계 추정 정책은 바꾸지 않는다.**
아래 schema는 새 HTTP 읽기 DTO이지 새로운 영속 저장 schema가 아니다.

### 13.1 소유권과 데이터 흐름

집계의 소유자는 **ChillServer**다. 이유는 `cmd/chill-server/profiles.go:profilesView`가
이미 채택 catalog의 메모리 snapshot을 갖고, `measurementService`가 namespace 정책·BEO·
동작 비용·점유 측정을 소유하기 때문이다. Console에 catalog/스케줄러를 다시 만들지 않는다.
Console은 현재와 같이 inventory·Run·정적 호환성·retained result/statistics의 소유자로 남는다.

```text
브라우저 GET 1회 → same-origin BFF → ChillServer 인증/SAR → coverage snapshot
                                                        ↑ 읽기 전용 조합
  현재 catalog snapshot + namespaced 측정 CR/Run metadata + inventory identity
  Console의 기존 호환성 resolver 결과 + 정확 result의 retained statistics/품질 요약
```

- `cmd/chill-server`가 composition root에서 기존 Kube reader/`profilesView`/measurement
  읽기와 고정 private Console HTTP client를 연결한다. Console URL은 기존 고정 origin 검증·
  무 redirect·응답 크기 제한을 쓴다. 내부 HTTP self-loop/public bearer 재전송은 없다.
- 정적 지원은 Console의 기존 `model-compatibility-matrix`와 B1 resolver만 사용한다.
  같은 model/path/class/대표 Node/선언 버전의 판정을 mode마다 반복 계산하지 않는다.
  현재 전체 행렬의 순차 resolver 반복은 독립 조합 최대 8개 동시 실행으로 바꾸고 기존 취소·
  fact-source 해석을 유지한다. 브라우저 architecture/backend 추측표는 없다.
- Run은 namespace 전체 LIST 한 번의 snapshot을 UID로 index한다. 공개 API page마다 동일
  전체 LIST를 반복하지 않는다. owner는 §12.2와 같은 정확 controller UID 조건이다.
- 공통 DTO와 순수 표시용 판정은 `internal/operatorconsole/measurement_research*.go`에 둔다.
  이미 ChillServer가 operatorconsole 타입을 읽는 방향을 재사용한다. Go transport에 통계나
  controller 의존성을 넣지 않고 composition-root callback으로 조립한다.
- Console `cmd/console/measurement_statistics.go`의 retained loader →
  `profiler/projection` → `DeriveIdleBaseline`/`VerifyCurvePlan`/`DeriveCurve`를 그대로 쓴다.
  검증된 trial에 이미 있는 wall J/item·idle W·관측창·timestamp 품질의 좁은 summary를
  기존 result 응답의 `researchFacts` 형제로 추가한다. raw/vector/로그를 coverage 응답에 싣지 않는다.
  이 summary는 사실이며 catalog 사용 가능 여부를 Console이 새로 결정하지 않는다.
- 모든 측정 화면은 아래 집계의 `entities`/`cells` assessment를 참조한다. 기존 목록·단건의
  raw phase/conditions는 보존하지만 UI 대표 상태로 직접 번역하지 않는다. result의 새 facts도
  클라이언트가 상태로 재판정하지 않는다. U1의 동일 reducer가 서버에서 assessment를 만든다.

### 13.2 경로·권한·범위

`GET /api/v1/namespaces/{namespace}/measurement-coverage`

기본은 해당 namespace의 전체 커버리지/상태 index다. 선택 query는
`model`, `deviceClass`, `node`, `path`, `powerMode`, `cpu`, `batches`다.
이름은 현재 inventory의 정확 이름, cpu는 양의 정수 요청 개수(affinity 동일성의 대체 아님),
batches는 중복 없는 오름차순 양의 정수 CSV다. 각 query는 최대 한 번이며 unknown/빈/중복/
유효하지 않은 조합은 400 `InvalidCoverageQuery`다. batch는 기존 측정 spec의 허용 범위로 검증한다.
선택 query는 namespace base snapshot의 읽기 projection이며 새 raw scan/cache entry를 만들지 않는다.
`batches`는 명시적 **목표 집합**이며 지원되지 않는 batch를 지원으로 승격하지 않는다.
목표가 없으면 현재 정확 variant의 실제 요청 집합을 쓰고 scope를 `requested`로 표시한다.
미요청 CPU/dataset/image의 무한 조합이나 모델별 임의 최대 batch를 분모로 만들지 않는다.

ChillServer는 기존 TokenReview와 실제 호출자 SAR를 cache를 읽기 **전에** 수행한다.
non-resource grant는 정확 namespace 경로의
`get /chill/api/v1/namespaces/{namespace}/measurement-coverage`다.
이 endpoint는 inventory·namespaced 측정 상태·해당 namespace로 제한한 catalog·보존 통계
요약을 묶는 별도 읽기 권한이다. RBAC에 명시적으로 부여하며 다른 GET grant에서 자동 유추하지 않는다.
서비스 계정으로 읽은 source 사실은 사용자 권한을 대체하지 않는다.
기존 `measurementService.namespace` 허용 namespace 정책도 적용한다.
다른 namespace profile/reuse source의 수치·식별자는 grant 없이 노출하지 않는다.
허용 namespace의 대상 reuse 행과 그 허용 범위의 출처만 표시한다.

| 상황 | HTTP/응답 |
|---|---|
| 토큰 없음/무효 | 401, cache 데이터 0개 |
| SAR 또는 namespace 접근 거부 | 403, cache 데이터 0개 |
| TokenReview/SAR 장애 | 503 `AuthorizationUnavailable`, 이전 cache도 반환하지 않음 |
| 입력 오류 | 400, 조용히 기본 필터로 대체하지 않음 |
| 초기 snapshot 미준비 | 503 `CoverageWarming`, `Retry-After: 2`; 2초 이상 요청을 붙들지 않음 |
| 이미 있던 snapshot의 일부 source 갱신 실패 | 200 `completeness:"partial"`, 아래 source 오류와 영향 범위 명시 |
| identity/필수 inventory 전체를 사용할 수 없고 허용 stale snapshot도 없음 | 503 `CoverageUnavailable` |

응답은 `Cache-Control: no-store`다. BFF/브라우저 HTTP·디스크 cache와 서버 내부 계산 cache는 다르다.
새 route는 ChillServer handler·BFF allowlist·entrypoint viewer RBAC에 정확 GET만 추가한다.
write 권한·wildcard POST·raw export 권한은 추가하지 않는다.

### 13.3 응답 schema

필드는 아래처럼 camelCase를 사용하며 모든 시각은 RFC3339Nano UTC다.
`null` 수치는 `missingReasons`에 이유가 있고 관측 0과 다르다. 유한한 수치만 number로 보낸다.
ns 정수는 기존 lossless decoder 경로를 유지한다. ID는 서버가 만든 불투명 표시용 키이며 저장 key/URL이 아니다.

| 필드 | shape / 의미 |
|---|---|
| `schemaVersion` | `"measurement-coverage.v1"`; HTTP DTO 버전, durable schema 아님 |
| `snapshotId`, `assessmentVersion` | namespace snapshot revision, `"research-checklist.v1"` |
| `dataAsOf`, `readStartedAt`, `readCompletedAt`, `servedAt` | 공통 입력 기준(가장 오래된 필수 source 시각), 조회 시작/완료, 제공 시각. atomic snapshot이라고 주장하지 않음 |
| `freshness` | `"fresh"` / `"stale"`; `refreshing:boolean`, `nextRefreshAt` |
| `completeness` | `"complete"` / `"partial"`; 필수 page/identity 전체 여부. null을 빈 목록으로 바꾸지 않음 |
| `scope` | `{namespace,supportBasis:"representative-node-static",goalBasis:"requested"\|"explicit",targetBatches:[]}`와 선택 필터 |
| `sources[]` | `{kind,readAt,resourceVersions,complete,error:{code,message,retryable}\|null}`. inventory/compatibility/catalog/BEO/Run/researchFacts/operation/occupancy 구별 |
| `summary` | `supportedCellCount`, `completeCellCount`, `paperEligibleCellCount`, `unknownSupportCount`, `unsupportedCellCount`, `countsByStatus`, `requestCount`, `runCount`, `profileCount`, `failedCellCount`, `currentFailedRequestCount`, `historicalFailedRequestCount`. 미확정 수는 null |
| `groups[]` | 모델×class `{model:{name,uid},deviceClass:{name,uid},cellIds:[],complete,supported,unknownSupport}`. inventory 이름순 장치 열 고정 |
| `cells[]` | 아래 Cell. 후보 단위는 model UID × class UID × path × mode. 정확 variant/Node/batch는 자식에서 분리 |
| `entities` | `{requests,runs,profiles,operationCosts,dispatchOccupancies}`. 각각 `{uid,id,assessment,cellIds}` 배열. profile은 실제 opaque profile ID, CR은 namespace/name/UID. list/detail 동일 lookup |
| `nextActions[]` | `{kind,cellId,entityId,reasonCode,message}`. 미해결 실패/부족 batch/근거 확인 필요만, 최대 각 3건. 새 추천 점수·자동 재요청 없음 |

**Cell**

| 필드 | shape / 규칙 |
|---|---|
| `id`, `model`, `deviceClass`, `path`, `powerMode` | 실제 이름과 UID, mode 원문. mode 없는 class에 가짜 mode를 만들지 않음 |
| `support` | `{verdict:"Compatible"\|"Incompatible"\|"Unknown",representativeNode:{name,uid}\|null,reasons:[]}`. 선택 Node가 다르면 해당 Node의 기존 B1 판정을 사용하고 대표 판정을 복제하지 않음 |
| `assessment` | 공통 ResearchAssessment. 셀 요약 범위, 원본 실행 phase와 독립 |
| `batchProgress` | `{basis,target:[],measured:[],eligible:[],missing:[],inProgress:[],unknown:[]}`. 중복/미확인 조건은 완료에 가산하지 않음 |
| `representative` | `{variantId,batch,incrementalJoulesPerItem,p95Milliseconds}` 또는 null. 같은 정확 조건의 실제 측정 배치 중 최저 평균, 최적성 보장 아님 |
| `variants[]` | frozen model/class/Node UID·image·artifact/path·preprocessor/dataset UID·CPU/quota/affinity·protocol·metricSource가 같은 정확 조건. `{id,condition,assessment,batchProgress,paperRows,requestIds,runIds,profileIds}` |
| `failureSummary` | `{code,message,exitCode,batch,requestId,runId,followUpId,relation}` 또는 null. 메시지 최대 160자, 줄바꿈/전체 JSON/토큰 없음. relation은 `verified-follow-up`/`related-candidate`/`none` |
| `history` | 정확 관련 요청 identity와 순서·원본 상태·서버 판정만. 과거 실패 삭제 없음. 긴 로그는 기존 상세 GET |

정확 조건 identity를 입증할 수 없으면 variant를 합치지 않고 `conditionIncomplete` reason을 남긴다.
class 일부 Node 완료는 Node 진척으로 따로 보인다. 서버 검증된 대상 reuse만 해당 Node 완료로 센다.
과거 실패 뒤 정확 후속 유효 결과가 있으면 셀 대표는 완료 가능하지만 실패 요청 자체는 계속 실패다.
후속 이름 접미사/비슷한 조건만으로 해결됨을 만들지 않는다.

### 13.4 단일 상태 어휘와 판정 순서

`ResearchAssessment = {status,label,reasons:[],execution,evidence,paper,assessedAt,scope}`.
`status`는 아래 enum 하나, `label`은 아래 한국어 그대로다.
`execution`은 `{phase,reason,generation,observedGeneration}` 원문 사실,
`evidence`는 `{available,accepted,reasons}`, `paper`는
`{eligible:true|false|null,checklistVersion,checks:[{code,result:"pass"|"warn"|"fail"|"unknown",message,sourceIds}]}`다.
누락 generation/알 수 없는 phase를 성공·진행으로 추정하지 않는다.

아래 순서는 **같은 평가 범위** 안에서 적용한다. 개별 실패 요청과 후속 결과를 포함한 셀은 범위가
다르므로 상태가 달라도 모순이 아니다. UI는 범위와 `최근 시도 실패/재측정 진행` 보조 사실을 함께 쓴다.

| status / label | 판정 |
|---|---|
| `unknown` / **확인 불가** | 필수 source 실패·불완전 목록·identity 모순·알 수 없는 phase로 범위 판정 불가. 지원 Unknown만으로 이미 검증된 과거 결과를 없애지는 않으며 support는 별도 Unknown으로 남김 |
| `unsupported` / **지원 안 함** | resolver의 명시 Incompatible이며 해당 범위의 사용 근거 없음. 저장 근거와 현재 선언이 충돌하면 아래 경고 |
| `warning` / **경고** | 저장 근거/현재 선언 충돌, Complete이나 미채택/불수용/깨진 근거, 유효 결과 뒤 새 실패, 음수·변환 불가 d, 논문 필수 근거의 실제 결손 등 해결할 품질 이유 있음 |
| `partial` / **부분** | 품질 오류 없이 정확 목표 batch 또는 현재 선택 Node 집합 중 일부만 측정. 부족 batch/Node를 반드시 열거 |
| `paper_eligible` / **논문 적격** | 아래 체크리스트 모두 pass, 해당 목표 범위 완료. API/화면에 체크리스트 범위 표기 |
| `complete` / **완료** | 해당 범위 실행·수용 근거/목표 충족. 논문 검토 미완료는 paper.eligible=null이며 검토 미완료 사유 병기. 실행 phase=Complete만으로 이 상태를 만들지 않음 |
| `running` / **진행** | 위 사용 결과가 없고 현재 generation의 비종결 실행·대기가 명시됨. 이전 실패는 이력 |
| `failed` / **실패** | 위 사용 결과가 없고 현재 범위의 실행 실패 명시. 실패 요청의 idle 성공을 active 반복 통과로 세지 않음 |
| `unmeasured` / **미측정** | 필수 목록 완전·지원 Compatible이며 정확 범위에 시도/결과 없음. 지원 Unknown인 빈 조합은 unknown |

최상위 `unknown`은 판정에 필요한 사실의 장애이며 단순 통계 warmup은 완료 근거를 지우지 않는다.
통계 warmup 중 `complete`와 paper=null, 실제 원근거 부재 확인 뒤 `warning`을 구별한다.
여러 자식이 섞인 셀은 미확정 자식이 있으면 unknown, 해결할 경고가 있으면 warning,
일부만 유효하면 partial, 전부 적격이면 paper_eligible, 전부 수용이면 complete 순이다.
전부 미완료면 running > failed > unmeasured를 쓰되 각 상태 수를 보존한다.
미지원 자식은 지원 분모에서 제외하고 support Unknown 수는 별도로 보인다.
완료 수는 complete+paper_eligible, 논문 적격 수는 그 부분집합이다. warning/partial을 완료율에 몰래 더하지 않는다.
동작 비용/점유 측정은 자신의 scope에서 같은 enum을 쓰며 batch/논문 행이 적용되지 않으면 paper=null이다.
원본 판정 `Accepted`/`Insufficient`/`Complete`는 상세 원문에서 계속 읽을 수 있다.

**논문 적격 체크리스트 v1**은 새 과학적 채택 정책이 아닌 읽기용 최소 확인이다.
정확 조건·원시 lineage 확인, 목표 batch 충족, 독립 active와 공유 idle를 분리한
반복 수 각각 `max(3,동결 pointPolicy.MinimumTrials)` 충족, 유한한 기존 CI/level(0폭 허용),
통합 client p95와 호출 N>1, 모든 사용 trial의 유효 전력창·표본/실패·gap 정책 통과,
원본 source timestamp 존재, 실제 전력원/Node/계측 범위 명시가 필수다.
데이터셋·전처리·CPU·runtime metadata도 정확 참조로 확인해야 한다.
현재 settings를 과거 기준으로 소급하지 않는다.
노드 전체 계측은 명시적으로 **노드 전체 측정 표 행**에 적격일 수 있지만 모듈 전력/격리 인증은 아니다.
미관측 격리·발열을 정상으로 표시하지 않는다. timestamp 부재/필수 품질 결손은 warning,
아직 읽지 않은 항목은 unknown check다. 단일 대표 Passport로 전체 trial을 통과시키지 않는다.
CI가 겹친다는 것만으로 근거를 기각하거나 통계 유의성 검정을 새로 만들지 않는다.

### 13.5 논문 표 행과 계산 범위

`paperRows[]`는 정확 variant × batch 한 행이다. API가 제공한 행을 CSV/LaTeX로 옮기며
클라이언트는 포맷 외 수치·상태를 유도하지 않는다.

| 필드 | 의미/근거 |
|---|---|
| `condition` | model/class/Node name·UID, powerMode, executionPath/runtime image·backend, batch, CPU request/quota/affinity, dataset/preprocessor UID, protocol, measuredAt, reuse provenance |
| `p95Milliseconds`, `latencySampleCount` | §12.3의 같은 active batch client 호출 pooled p95·호출 표본 N. native service time/한 회차 p95와 구별 |
| `idleWatts` | 같은 frozen 상태의 `DeriveIdleBaseline.ModelLoadedIdleMeanWallWatts` |
| `activeWatts` | 정확 active trial별 유효 벽전력 적분/실제 창 초의 산술 평균. trial 수와 평균 방식 명시 |
| `totalJoulesPerItem` | 같은 active trial의 `EnergyTrial.ActiveWindowWallJoulesPerItem` 산술 평균 |
| `incrementalJoulesPerItem` | 같은 trial의 기존 평균 증분 J/item. 평균 부하 W/pooled 처리량으로 새로 근사하지 않음 |
| `confidence` | §12.3 PointConfidence, **증분 J/item** 대상만. lower/upper=mean±halfWidth, 음수 하한/0폭 보존 |
| `activeTrialCount`, `idleTrialCount`, `completedItems`, `completedCalls` | 각각 독립 반복/공유 baseline 반복/실제 완료 item/실제 호출. 서로 대체 금지 |
| `powerSource` | metric name·meter identity·Node UID·scope=`node-wall`/실제 기록 범위·sourceTimestampAbsent·표본/최대 gap 요약. 미기록 scope는 unknown |
| `assessment`, `sources`, `missingReasons` | 동일 서버 판정, 실제 사용 `{store,key,size}` 참조, null 필드별 이유 |

총 J와 증분 J의 차이에 유휴 W만 단순 더하지 않는다. 실제 active 시간/item과 같은 baseline이어야 한다.
재사용 profile은 원본 서비스/에너지와 대상 확인 벽전력의 출처를 섞지 않고 원본/대상 행을 구별한다.
하나의 동일 물리 관측으로 위 필드를 구성할 수 없으면 혼합 총 J 행을 만들지 않고 이유 있는 null이다.
retained raw 없음·통계 미기록·권한/transport 오류는 §12.3대로 구분한다.
동작 비용 전체 Node J와 batch J/item은 다른 단위이며 논문 행에 합산하지 않는다.

### 13.6 cache·부분 실패·응답 목표

- namespace별 **프로세스 메모리 snapshot 한 개**를 둔다. Redis/DB/새 publisher는 없다.
  완전한 입력을 index한 뒤 불변 DTO를 atomic 교체한다. catalog refresh/Tick 잠금을 오래 잡지 않는다.
- 시작 시 허용 profile namespace를 background prewarm한다. 기본 30초 fresh TTL,
  stale 허용 최대 5분이다. 만료 GET은 마지막 정상 snapshot을 즉시 제공하고 namespace당
  refresh 한 번만 시작한다. cold GET은 warmup을 시작/합류하되 2초 전 warming 응답을 끝낸다.
  새 상태가 이전 것과 섞이지 않게 `snapshotId`를 한 응답 전체에 적용한다.
- immutable statistics/facts는 정확 result reference `{store,key,size}`와 CR UID·동결
  조건 identity로 재사용한다. 변경 CR/Run RV·catalog configVersion·inventory/선언 UID/RV를
  다시 대조한다. 삭제/identity 변경은 즉시 다음 snapshot에서 제외한다.
  오래된 immutable cache는 현재 snapshot이 참조하는 항목으로 제한한다. token을 cache key에 넣지 않는다.
- background raw/Console 작업 동시 상한 4, 객체별 기존 64MiB/시간·취소 제한을 유지한다.
  **GET 경로에서 78 profile × 전체 Passport/raw를 동기 fan-out하지 않는다.**
  미리 계산 중인 facts는 paper=null로 반환하고 상태를 정상으로 꾸미지 않는다.
  선택한 기존 result GET의 facts가 준비되면 동일 cache의 다음 snapshot으로 반영한다.
- 한 source 갱신 실패 시 이전 정상 snapshot을 새 `dataAsOf`로 포장하지 않는다.
  실패 source·scope·last success를 보존해 partial/stale로 표시한다.
  지원 source가 없으면 분모 null, 요청 source가 불완전하면 미측정/실패 수 null,
  facts만 실패하면 기존 측정 근거는 유지하되 해당 paper 판단은 unknown/경고로 제한한다.
  stale 통계와 새 조건을 결합하지 않는다. 5분을 넘으면 해당 source 값은 판정에서 제외한다.
- browser Query cache는 인증 epoch 안 메모리로만 유지한다. 마지막 정상 snapshot을 즉시
  렌더링하고 재진입/명시적 갱신/진행 중일 때의 가시 탭 30초 갱신만 수행한다.
  401/403 이후 cache 표시 금지. 403을 미측정/0건으로 바꾸지 않는다.
- 목표는 **prewarm 완료 후 최초 방문과 재방문 성공 200의 p95 <2초**(브라우저 BFF 포함).
  3모델/8class/88BEO/1,155Run/78profile, 동시 사용자 1·5, fresh/stale/재시작을
  구분해 측정한다. cold warming 503은 성공 측정에서 제외하되 별도 횟수·준비시간을 보고한다.
  실제 행렬 7.690초/Run 1행 3.861초의 현행 경로를 요청 중 그대로 호출하면 수용 실패다.
  dashboard cutover 전에 prewarm 완료·필수 source complete를 확인한다.
  인증 비용·내부 집계·BFF 시간을 각각 기록하며 2초 목표는 아직 측정된 성과가 아니다.

### 13.7 Console researchFacts 형제 필드

기존 `.../batch-energy-optimizations/{name}/result`의 result/statistics는 그대로다.
새 `researchFacts`는 null 또는 다음 객체, `researchFactsReason`은 null 또는
§12.3의 `NoPublishedResult`/`RetainedEvidenceMissing`/`StatisticsNotRecorded`다.
store 권한/transport/깨진 identity 오류를 이 문자열로 숨기지 않는다.

```text
researchFacts:
  request: {namespace,name,uid}
  resultReference: {store,key,size}
  condition: <13.5의 정확 frozen condition>
  pointPolicy: {version,minimumTrials,confidenceLevel}
  batches:
    - batch
      idleWatts, activeWatts, totalJoulesPerItem, incrementalJoulesPerItem
      confidence, p95Milliseconds, latencySampleCount
      activeTrialCount, idleTrialCount, completedItems, completedCalls
      powerSource
      checks: [{code,result,message,sourceIds}]
      sources: [{store,key,size}]
      missingReasons: {<field>: <reason code>}
```

이 객체의 checks는 loader/동결 정책이 이미 확인한 사실의 요약이며 새 catalog 판정이 아니다.
모든 사용 trial에서 확인하지 못한 quality 항목은 `unknown`, 적용 안 되는 항목은 이유와 함께
명시하고 pass로 꾸미지 않는다. BEO outcome/현재 catalog와 조합하는 대표 assessment는
ChillServer의 공통 reducer 한 곳에서만 만든다. result facts와 catalog의 정확 참조가 다르면
과거 논문 행으로는 보여도 현재 사용 근거로 합치지 않는다.
원본 string/명칭은 안전한 텍스트만, 로그·raw vector·Secret은 제외한다.
합계 response는 기존 BFF 8MiB 한도 안이어야 한다. 한도 초과는 503 `CoverageTooLarge`로
명시하고 일부 entities를 몰래 잘라 정상 snapshot으로 표시하지 않는다.
