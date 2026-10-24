# 선언형 operation 비용 측정과 디스패치 점유 측정

캠페인은 CHILL 측정 API를 사용한다. 노드별 승인과반환은 `/home/genesis1/ETRI/chill-pools/docs/design/serving/measurement-admission.md`를따른다. 사용자label2026-10-24의승인계약은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/implementation-contract-20261024.txt`와통합제안이다. 아래계약의실제구현/단일배포/대표검증상태는 `/home/genesis1/ETRI/chill-pools/docs/handoff.md`의근거로확인한다. firstfloor runtime digest는고정하며원본raw/값/판정은보존한다.

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
| `spec.observation` | `intervalMilliseconds`,`requestTimeoutMilliseconds`,`maximumOperationSeconds`,`maximumGapMilliseconds`와새실행에필수인 `timing`이다. timing은문자열 `changeIntervalP95Seconds`,`maximumRequestSeconds`,`maximumReceiptGapSeconds`,`source`로실측입력을고정한다. `minimumCoverage`,`maximumBoundaryDistanceMilliseconds`는기존기록원계약재현에사용한다. 새timing실행의gap은원구간보정근거다 |
| `spec.steps[]` | 1-32개, `listType=map` key `name`. `name`(DNS label), `kind`(`power`, `prepare`, `idle`, `remove`). `power`는 `powerMode`, `prepare`와 `remove`는 `batchEnergyOptimization`(같은 namespace의 BEO 이름), `idle`은 `idleDurationSeconds`(≥ 1)만 갖는다(CEL) |
| `status` | `phase`(`Measuring`, `Complete`, `Insufficient`, `Failed`), `reason`, `steps[]{name, key, verdict}` |

**실행.** 승인(5장)과 같은 `tickMu` 안에서 노드 run을 등록하고 `phase: Measuring`을 쓴다. 순서 ID는 CR UID다.
1. 단계마다 `MeasurementRequest`를 만든다. `Operation.ID = <UID>.<순번>.<name>`, meter와 store는 비워 노드 설정으로 채운다. `power`는 `nodePowerTargets(node, classes, policies)[powerMode]`와 `powerRequest`(`admission.go:453,458`)로 NodeActuation을 만들고 CR을 owner로 단다. `foreignActuations`(`admission.go:470`)가 루트 UID owner를 건너뛰므로 반환의 무효 판정이 자기 단계를 세지 않는다. `prepare`는 BEO의 현재 UID로 만든 profile ID(`catalog.go:385`)의 preparation(`profiles.go:167-185`), `remove`는 같은 preparation의 인스턴스다.
2. 기존 `Measure`를 차례로 부른다. 기록 key, 형식, witness는 그대로다(`cost.go:33-35`). 단계가 끝날 때마다 `status.steps`에 key와 verdict를 더한다.
3. 실패나 `Accepted`가 아닌 단계에서 멈추고 기존 순서 기록(`serving/operation-sequences/<UID>.json`)을 쓴다. 이어서 기존 반환이 돈다.

결과: 실제operation/effect/격리/zero-serving-inference/원시구조/실제양bracket/확장support를통과한에너지는 `Accepted`다. 정밀도비율은보고전용이며step중단또는Insufficient gate로사용하지않는다. 동작실패/격리오류/실제boundary누락/invalidrecord와기존Failed수명주기는사유와부분근거를보존한다. 실제단계의유효에너지를기존cost흐름으로수집하며원래값/상태를별도계획상한으로대체하지않는다.

### 2.1 전력 곡선과 민감도

실제동작창[a,b]와기록된piecewise-linear전력곡선q에대해측정값은 `Ehat=∫[a,b]q(t)dt`다. `A=p95(전력값변화간격)+최대요청지연+최대유효수신gap`을원십진문자열로고정하며실행시간변환은합을nanosecond로올림한다. Gen3원합1.6079805965s/Plus원합4.518180026700001s다.6미측정Gen3의계열전이를명시하고동일값유지길이/추가1.25배수는사용하지않는다.

새관측은[a−A,b+A]의실제표본을확보한다. `S=maxδ∈[−A,A]|∫[a,b]q(t+δ)dt−Ehat|`를모든knot/구간stationary극값에서정확계산한다. 상수전력S=0,상수offset을더해도S는같다. 실제실패/missed요청을포함하거나명시적maximumGap을넘은valid-to-valid원gap중확장support와교차한구간을선택해**원래전체구간폭**의합집합을한번계산한다. `G=이번record최대관측W×min(b−a,union길이)`, `B=S+G`다. gap폭clipping과gap별A재확장은없다. G는미관측전력이이번관측최대안에있다는경험적조건을명시한다.

기록의선택수치 `energyUncertaintyJoules`,`phaseSensitivityJoules`,`missingGapJoules`는각B/S/G다. `energyUncertaintyRatio`는B/abs(Ehat)보고값이며Ehat=0또는nonfinite비율은생략/undefined로표시한다. `request.timing`과 `operationBudgetNanos`에조건을보존한다. B없는과거기록은원계약적분으로재현한다.10%indicator는상태판정에영향이없고민감도는센서교정/물리95%보장과구분한다.

### 2.2 계획기 불확실성

`OperationTrial.energyUncertaintyJoules`와 `NodeBaseline.wattsUncertainty=B/실제측정초`를기존costprojection에전달한다. nominal평균은유지한다. `SchedulerNodePlan.energyUncertaintyJoules`는chain민감도, `SchedulerCapacity.gainUncertaintyJoules`는실제minimumGain측정오차hurdle을표시한다.

동일immutable reference+trial index의B/N은발생횟수와부호를누적하고keep−best에서동일근거를먼저상쇄한다. 서로다른결정론적evidence의잔여절대값은합산하고profile CI의sqrt(sum squares)에별도로더해minimumGain측정오차hurdle을계산한다. nominal평균은유지한다. unloaded+Σ(loaded−unloaded)는unloaded계수1−instanceCount,정확loadedbaseline은계수1이다. target-baseline-delta복원은같은subtractednominal을한번더하고B는이미wall민감도이므로baselinebound를다시더하지않는다. minimumGain판정변화와nominal평균불변을함께검증한다.

## 3. DispatchOccupancyMeasurement (`occupancy`)

같은 group과 규칙이다. printer column: Phase, Node, Mode, PerRequest(`.status.perRequestNanos`), Reason, Age. `spec.node`도 같다.

| 필드 | 형식 |
|---|---|
| `spec.batchEnergyOptimization` | 잴 option의 BEO. 전력모드, runtime image, model은 그 profile의 `condition.powerMode`, `runtimeImage`, `logicalModel`이다 |
| `spec.workload` | 요청 경로를 줄 Workload 이름. 그 Dataset의 `evaluation` 부분집합을 보낸다 |
| `spec.batch` | 고정 배치 크기 B(≥ 1). 인스턴스를 `fixedBatchSize: B`로 준비한다(`SCH-35`) |
| `spec.supplyMode` | `closed-loop`고정outstanding공급과 `poisson`을구분한다. 승인d는closed-loop,기존E5 Poisson은유지한다. `workers=K`,waitingBatches=q이며계산입력은conditions에고정한다 |
| `spec.warmupSeconds`,`durationSeconds`,`minimumBatches`,`maximumIdleFraction` | 계산부하준비W_load,본창T,표본N과idle계획비율f_plan이다. 하한60s/360s,계획cap3600s를사용한다 |
| `spec.requestLifetimeSeconds`,`deliveryMarginMillis`,`drainSeconds`,`preparationSeconds`,`jobSetupSeconds`,`collectionSeconds`,`plannedElapsedSeconds` | profile/SLO/실제준비/batchtail/전달/수집예산에서계산하며plannedElapsedSeconds≤3600이다. lifetime L≥SLO와native count/decodedbytes공동조건을만족한다 |
| `spec.conditions` | `profileID`,`inputsJSON`,`waitingBatches`,`maximumIdleNanosPerRequest`,`minimumCycles`로정확profile/모든입력/승인계수/계산q/idle1ms/필수cycle을불변고정한다 |
| `status` | `phase`(2장과 같은 넷), `reason`, `powerMode`, `perRequestNanos`, `throughput`(소수 문자열, req/s), `result`(`ProfilingRunEvidenceReference`) |

**실행.** 노드 run 하나가 다음을 차례로 한다. 모든 동작은 measurement 모드 `Operations`(taint toleration)다.
1. 노드가 profile 모드가 아니면 2장의 방법(CR owner NodeActuation)으로 `ChangePower`한다. 비용으로 기록하지 않고, witness 구간은 이 단계 뒤에 시작한다.
2. preparation을 `Owner = Workload`, `fixedBatchSize = B`로 prepare한다. owner가 있는 준비 Pod는 routing annotation `{<Workload UID>: 1}`을 받아 바로 그 Workload를 서빙한다(`operations.go:836-849`, `scheduler_test.go:421-425`). taint 노드에서도 같다 `[추론]`.
3. 계산된공급/수명/준비/본창/drain/수집예산으로loadJob을실행한다. 완료된nativebatch원장을기존JSONL/scanner의d전용집계로읽으며전체cohort배열/전체Query를만들지않는다. Job을정리하고전체조건/모든과학판정사유를record와status에기록한뒤기존반환을수행한다.

**load Job.** release namespace의 `occupancy-<UID hash>`, label `chill.dacs.io/measurement-load: <Node UID>`.
- `backoffLimit:0`과명시Job유한예산을유지한다. deadline은jobSetup/warmup/window/physicaldrain/delivery/collection을함께포함한다. native실행은HTTP만료뒤계속될수있어미완료evidence를unknown으로보존한다. 반환은해당label의Job을Background삭제해부하Pod를정리한다.
- `nodeName`은 ChillServer Pod의 노드(downward API `spec.nodeName`, env `NODE_NAME`)다. `nodeName`은 scheduler를 거치지 않아 NoSchedule taint를 보지 않는다. 그 노드는 gateway가 있는 노드라 부하가 다른 측정 노드의 전력에 섞이지 않는다.
- init `dataset`은 Dataset image에서 `sh -ec 'cp -R "$1"/. /data/' -- <path>/evaluation`(preprocessor 전달과 같은 형태, `operations.go:411`)이다.
- load는Workload/Dataset원시입력을공통TLSgateway로공급한다. 승인d는closed-loop K고정outstanding과L header/socket여유를사용하고E5 Poisson모드는유지한다. 실제FQDN/CA/projectedtoken과기존release SA를사용한다.
- ServiceAccount는 `<fullname>-load-generator`, image는 `load-generator/`를 담은 `build/docker/load-generator.Dockerfile`이다. base는 Python 3.12로 고정한다. live 인증서에 Subject와 Authority Key Identifier가 없어 3.13 이상의 기본 strict 검증이 거절할 수 있다 `[추론]`.

**계산.** `d=연속native batch시작span/(physical cycles×B)−profileMean/B`의full d를유지한다. 물리batch는Node/Pod/container/BatchID와member를연결한다. readiness는B member의queued시각최대,actor대기는 `runningNanos−queuedNanos`다. 직접native timestamp와legacyoffset은별도로보존한다. HTTP실패를이유로이미실행한batch를제거하지않고compute gap전체를idle로빼지않는다.

**공급과근거.** approved 상대계획정밀도1%,양측95%,여유1.25,idle기여1ms/요청을실측profile분산/SLO/준비aggregate에서N/K/L/창으로도출한다. physicalfinished→다음ready의보수idle상한과끝단/hole/누락native를함께평가한다. 필요한원근거가없으면unknown/Insufficient와모든관련사유를보존한다. native waitingcount/decodedbytes/lifetime의공동조건아래K를계산하며전체queue를채우는고정worker수는사용하지않는다.

record.conditions는전체불변Spec이며reasons[]는해당사유전체를보존한다. `idleIdentified=false`일때 `idleUpperNanos`는알려진pair의부분보수합이며전체idle상한으로해석하지않는다. `idleNanosPerRequest`는동일physicalcycle/endpoints를사용한다. `cycleMeanNanos`,`cycleSDNanos`,`cycleRelativeHalfWidth`,`precisionTargetMet`는stationary-iid t근사보고정보다. 실제CI를새거절gate로쓰거나자동N변경을하지않고minimumN/물리완전성/unknown/idle과학gate를유지한다.

RequestRow의 `nativeQueuedAt`,`nativeRunningAt`,`nativeFinishedAt`,`nativeExecutionComplete`,`nativeOutcome`,`nativeError`는직접actor근거이며legacyoffset/genericexecutionComplete와구분한다. HTTP완료뒤늦게받은native는 `kind:native` raw보충으로보존하고HTTP완료를추가하거나member를중복계수하지않는다. 전용scanner는missing identity/physicalbatch만compact집계하며전체finalQuery/cohort배열을생성하지않는다.

결과: 판정통과는 `Complete`이며음수d도record에보존한다. minimumBatches/idle/물리근거/unknown/witness의모든해당사유는 `Insufficient`와record에남긴다. 거절/prepare/remove/Job/watchdog/재시작실패는기존 `Failed`수명주기다. 초기Measuring patch실패와load전power/prepare/Job생성/watchdog실패의terminal status는취소된run과분리한최대30s publication context로저장한다. 실제handback은parent context로수행하고상태저장예산이측정창/조건을확대하지않는다.

**유효 근거와 기존 결과.** d는 firstfloor 시계의 배치 간격으로 계산한다. 큐 상태의 근거는 firstfloor actor 대기(`runningNanos − queuedNanos`)와 물리 batch 연속성이다. native `queueMS`는 ingress가 겹쳐 포함된 지표로 해석한다. 최신 mobilenet d 8개(`genesis2-941`, `genesis3-941`, `jetson-nano-941`, `jetsono-941`, `jetsonx-942`, `lattepanda-941`, `orangepi-942`, `rasp5-941`)는 물리 batch 재계산에서 틈 0과 저장 d와의 차이 0.0006ms 이내를 확인하여 유지한다. 과거의 다른 기록은 연구 표에 사용할 때 같은 방법으로 검증한다. `rasp5-944`는 진단 전용으로 보존하며 d 채택 대상에서 제외한다.

**승인된 실행.** 조건계수1%/95%/1.25/idle1ms/360+60s/cap3600과d/lifetime/창구현/단일배포/대표검증은승인되었다. 최신mobilenetCPU8개를유지하고D01대표대조/SmolD20검증후(a)를진행한다. PLC d는배포2뒤,rasp5-944는진단전용이다. 실제실행은통합gates와CampaignOps fresh안전확인을따른다.

## 4. 계획기의 d 수집

- `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`를 같은 방식(prefix 목록, 불변 identity cache)으로 읽는다. `Accepted` record 중 (Node UID, 전력모드, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 쓴다(동률은 key 순, `latestBy`와 같은 규칙).
- 결과는 `serving.DispatchOccupancy{node{name, uid}, powerMode, runtimeImage, model, perRequestNanos, source}` 목록이고 source는 record key다. `perRequestNanos`는 max(d, 0)이다(사용자 결정 A7). 음수 d는 record와 그 key로 보인다.
- 목록은 `config.Costs`, `config.Baselines`처럼 controller 설정을 거쳐 snapshot `dispatchOccupancy`에 실린다(`kube/scheduler.go:493`). `dispatchOccupancy(i)`는 snapshot으로 옮기고 `i.Node.UID`, `i.Profile.Condition.PowerMode`, runtime image, model이 모두 같은 항목을 찾는다. 두 소비자 `NewCapacityInput`(`capacity.go:102`)과 재생(`scheduler_replay.go:564`)이 이것을 쓴다. 행 key가 항목을 담으므로 새 측정이 그 행을 다시 계산하게 한다.
- 잰 적 없는 모드의 option은 `MissingDispatchOccupancy`다. 같은 노드, 모드, runtime image, model의 option끼리 d를 함께 쓴다(`analytic-planner.md:28`을 이렇게 고친다).

### 4.1 observer와 coherent replacement

`Observer.ObserveUntil(ctx,Request,end,onFirstSample)`는producer실제end까지수집한뒤real finalread를남긴다. Duration은유한watchdog,최대표본예산은floor(watchdog/interval)+3이다. standalone `--end-wait-timeout`은RunConfig.Metrics.DurationSeconds, `--max-result-bytes`는frozenpolicy.MaxMetricResultBytes를사용한다. 기존Observe를유지하며fixed nominal창끝과actualproducerend를구분한다.

profiler의recorded Job존재는별도로주입한 `mgr.GetAPIReader`로조회한다. 실제API NotFound는ExecutionLost,transport오류는retry이며cache의일시NotFound로replacement를생성하지않는다. 새BEO annotation `chill.dacs.io/replaces-batch-energy-optimization`은같은namespace의원본이름을가리킨다. replacement가pending인동안에도원본의catalog사용을제외하며원본raw/status는보존한다. 대체는정상verifiedComplete와동일state/CPU/batch/exacttrial계약을충족한다.

012/017는coherent새BEO를사용하고원trial임의선택/len≥3완화/status patch는없다. gate는actualend가nominal보다늦은local HTTP source와실제standalone실행파일을사용해tail/finalsample/event/resultbytes를확인한다. 이는통합검증담당이실행할check이며현재문서기록만으로PASS를주장하지않는다.

017원본의Granted/Measuring은actualJob0에서도측정taint를보유한다. replacementannotation은catalog선택이며taint정리는첫새orangepi작업인 **stage3 D20직전** 수행한다. CampaignOps가fresh execution0/servingPod0/powerPending0/정확NodeUID/RV/holder와orphan-runtime안전정리뒤oldUID의stale `chill.dacs.io/measurement:NoSchedule`taint만제거한다. root/status/Runs/retainedJobs/raw/admission을보존하고stage4replacement는정상admission으로진입한다. 고정orangepi모드는유지하고자동release/recovery는없다.


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
| operation `serving-operation-measurement.v1alpha2`,순서 `serving-operation-sequence.v1alpha1` | 원형/원판정유지,새timing/별도B/S/G/보고비율선택필드 | timing없는과거Accepted는원적분재현,Insufficient소급승격없음 |
| `serving/dispatch-occupancy/<Node UID>/<CR UID>.json` | 기존불변record에전체공급/시간/계수/identity조건과native과학판정사유보존 | PutImmutable,rawnative/legacy원천분리 |
| record `idleFraction`,spec `maximumIdleFraction` | 물리batch/actor준비근거와idle기여상한,missing/holes/끝단unknown | 승인CPU8개보존과대표D01검증,모든모드일괄재측정없음 |
| 설정 `dispatchOccupancy` | 삭제 | decode가 strict(`scheduler_service.go:118`)라 commit 8 배포에서 ConfigMap의 key를 같이 지운다 |
| 판단 기록 `v1alpha5` | snapshot `dispatchOccupancy` 추가, `settings.dispatchOccupancy` 빠짐, 행 입력 항목에 `powerMode`와 node UID | 관측 reader는 strict가 아니어서 없는 필드를 빈 값으로, 옛 필드를 무시하고 읽으므로 버전을 올리지 않는다(`analytic-planner.md` 7장과 같은 처리) |

## 8. 캠페인 manifest

캠페인조건은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/gen.py`가계산하고승인산출물은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/phase-a/measurement-generalization-20261024/approved/`에둔다. d의N/K/L/준비/창/setup/collection과conditions.inputsJSON을함께고정하고cost는아래timing조건을사용한다. 실제apply는대표검증순서와CampaignOps번호/UTC통지및fresh안전확인을따른다. 아래는Gen3비용조건의형식예시다.

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

`Refused`와 `Failed`는 terminal이라 lane이 다음 루트로 넘어간다. 다시 잴 것은 원인을 지운 뒤 새 이름(새 순번)으로 만든다.

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
| 승인bundle | 상수S=0/offset불변/내부stationary극값/원gapunion와cap/지원구간누락거절/zero비율undefined/10%초과validAccepted/oldAccepted재현,baseline상쇄와nominal불변, `TestCapacityOperationUncertaintyChangesMinimumGain`,실제lateproducerstandalone tail/finalsample/serializedbytes,recordedJob authoritativeNotFound/transport오류를통합검증 |
| 전체 | `make lint test`, `make manifests generate`, `make helm-lint helm-template`(default와 `values-profiler.yaml`), `make dashboard-check`, `make test-loadgen` |

실클러스터대표검증은승인된MNV D01의X/full d/runner/물리coverage와유지값대조,실제비용전체sequence의nominal/S/G/B/relative보고/identity/격리/minimumGain연결,SmolD20의공급과unknown판정이다. 이후(a)잔여를재개하며PLC d는배포2뒤다. 실제runtime digest고정과수집tail/physicalinstance정리및감시를함께확인한다. source/gates/live의완료는담당실제근거를기록한다.

## 11. 캠페인 범위

E5전(a)는Node당기준모드의필수profile/d/비용이다. (b)전력모드확장은E5완료뒤수행한다. 측정이없는모드의option은 `MissingDispatchOccupancy`로제외한다. firstfloor runtime digest는고정하고012/017원본보존/coherentreplacement와단일측정조건배포를유지한다. 사용자추가승인대기는없으며새실제경계가발생하면그부분의근거를보고한다.
