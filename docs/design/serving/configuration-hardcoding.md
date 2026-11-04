# 하드코딩 정리: 설정 소유권과 기록 경로

상태: active contract (2026-10-30). 완료된 슬라이스2~5 절차는 제거했다. offline 회귀 완료와 배포·live 측정 수용은 다르며 아래는 설정 소유권·동결의 현행 계약이다.
[계획](../../plans/2026-10-30-configuration-hardcoding.md) · [인벤토리](../../plans/2026-10-30-configuration-hardcoding-inventory.md) · [ADR 0031](../../adr/0031-configuration-ownership-and-freezing.md)

## 1. 경계와 결정

A는 측정·판정 소유자의 기존 입력, B는 실제 환경 입력으로 옮긴다. C는 고정 방법론·프로토콜·패키징 계약, D는 이미 설정된 값이다. 자명한 수식·sentinel·Helm enum까지 이름 붙이지 않는다. 새 설정 서비스·저장소·의존성은 없다. Dashboard UI 재설계는 제외하지만 typed API round-trip은 포함한다. `demo.py`는 offline 회귀가 import하므로 회귀 유지 대상이다. 역사 script는 제외하며 파일 삭제 승인은 아니다. 비활성 숫자 탐색 CLI는 배치3 승인으로 삭제했고 승인된 외부 numeric input과 기존 게시 증거는 보존한다.

## 2. settings 증거와 실패 해석

판단 당시 적용값의 권위는 기존 `Decision.Settings`다. PUT의 사전 append → ConfigMap 저장 → full refresh → 후속 append를 유지한다. 초기·외부 ConfigMap 값은 그 값을 쓴 Decision의 전체 settings로 해석한다. refresh 합성 변경 행, 초기 before=after 가짜 변경 행, 별도 provenance 저장소는 만들지 않는다. 첫 판단 전 모든 적용 이벤트의 완전 복원을 요구하지 않는다. ConfigMap 출처는 실제 보존된 참조·binding 범위에서만 주장하며 과거 UID/RV를 꾸미지 않는다.

| 상태 | 해석 |
|---|---|
| 사전 append 실패 | ConfigMap 변경 전 중단 |
| 저장 성공·refresh 실패 | 저장 요청과 현재 적용값이 다를 수 있음 |
| refresh 성공·후속 append 실패 | HTTP 오류가 미적용을 뜻하지 않음; rollback을 주장하지 않음 |
| 이후 refresh 적용 | 당시 Decision의 실제 settings로 해석 |
| 적용 증거 부족 | 성공·실패 추정 없이 미확인 |

## 3. serving settings와 요청별 reuse

### 3-1. 추가 settings는 3개

기존 필드는 유지하며 이번 **추가 개수만 3개**다. `SchedulerSettingsInput` 한 경로에서 생략 기본값을 채운다. GET·Decision·before/after에는 실제값을 담고 범위 오류·NaN/Inf는 적용 전에 거절한다.

| JSON 필드 | 범위 | 생략 기본값 | 소비·동결 |
|---|---|---:|---|
| capacityGridSteps | 정수 ≥1, Go 표현 범위 내 | 20 | 격자 전체 순회·격자율 → CapacityInput → 용량 행 |
| capacityBisectionWidth | finite (0,1] | 0.005 | width × μ 종료 조건 → CapacityInput → 용량 행 |
| capacityTailSamples | 정수 ≥1, Go 표현 범위 내 | 100 | ceil(tailSamples/(1−p/100)); p=100 현재 분기 유지 → CapacityInput → 용량 행 |

기본값을 명시해도 입력 JSON SHA-256 기반 κ 키는 바뀐다. 옛 키 cache miss와 재계산은 의도된 cutover다. archive의 키 없는 조회는 넓게 탐색할 수 있어 초기 재계산 비용을 인계한다. `omitempty`, 옛 입력 분기, dual-key lookup, 새 캐시 복구 장치는 만들지 않는다. 과거 raw fixture는 원본 보존하고 새 계약 테스트 입력과 구별한다. bootstrap raw replay의 fixture loader도 이 구별에 맞춘다. `analytic-planner.md`의 고정 수식·코드 상수 설명도 함께 갱신한다.


### 3-2. 신뢰수준은 측정별 단일 정책

`BEO.spec.pointPolicy.confidenceLevel`은 기존 idle 정책과 같은 (0,1) 십진 문자열이며 생략 기본값은 `"0.95"`다. campaign point, serving catalog idle baseline, incremental energy CI에 같은 선택값을 사용한다. 독립 `ModelLoadedIdleBaseline.spec.pointPolicy.confidenceLevel`은 그 측정 CR의 기존 소유권을 유지한다. 전역 CI 하나로 묶는다는 뜻이 아니다. `Profile.ConfidenceLevel`은 실제 유도값을 기록하고 catalog의 `95%% CI` 메시지도 선택값을 출력한다.

`energyConfidenceLevel`/`profileConfidenceLevel` 전역 settings·CLI/values 별칭·fallback은 없다. immutable BEO spec/실행·결과 참조가 권위이며 baseline/Aggregate CI를 독립 정책으로 연결하지 않는다.

### 3-3. 재사용 정책은 요청 annotation

기존 `chill.dacs.io/profile-reuse` JSON의 sourceProfileID·maximumRelativeDifference 옆에 `minimumMeasurementSeconds`(finite >0, 기본30), `minimumTrials`(정수 ≥2, 기본3)를 추가한다. 창은 active와 idle 각각의 최소값이다. `ProfileReuseCheck`에는 선택 최소값과 실제 active/idle 길이·trial 수를 기록한다. 기존 `reusedProfile` 정책 identity에도 두 값을 넣어 annotation 변경이 옛 immutable startup bundle을 재사용하지 않게 한다. 전역 settings 변경으로 기존 요청 정책을 재정의하지 않는다. trials=2 수용은 §5의 종단 하한 정리와 함께 완료한다.


### 3-4. 유지할 설정과 방법론

power 앞뒤 1분 support는 coverage 상한에 맞는 C다. 새 settings나 Decision 필드는 없다. `powerObservation.sampling/coverage`와 읽기 전용 meters는 기존 소유자 그대로다. source/profile interval은 기동 설정이다.

## 4. 기존 기동 기록 최소 보강

기존 `cluster` stream의 `event:"server-start"` 행을 재사용한다. Pod UID/namespace/name은 기존 필드, process 시작 시각은 `run()`의 실제 `started`다. 추가할 측정 관련 값은 `queueLimit`=1024, `sourceIntervalNanos`=1000000000, `profileIntervalNanos`=60000000000뿐이다. 운영 overlay source2초를 선택하면 2000000000을 기록한다. 기존 append 실패→기동 실패를 유지한다. 새 scheduler startup 행은 없고 scheduler 비활성 때도 별도 행을 만들지 않는다.

startup 전용 값은 pointer/omitempty로 일반·과거 행의 부재를 보존한다. 적용 뒤 후속 append 실패가 rollback을 뜻하지 않는다.

request/drain timeout, directory interval, control component scope는 기존 관리·수집 설정 D다. CLI request30/drain120초, Helm129/164초를 통합하지 않는다. 특히 infer handler의 별도129초 때문에 requestTimeout을 모든 handler의 실제 timeout이라고 부르지 않는다.

Node sample/batch cadence와 transitionObserver.activeDeadlineOverheadSeconds=180은 D다. ResourceBatch/resources에 cadence 필드를 신설하지 않고 존재가 확인되지 않은 attempt frozen 입력에도 overhead를 복사하지 않는다. 기존 Job ActiveDeadlineSeconds를 유지한다. sampledAt/receivedAt은 관측 시각이며 선택 cadence·누락 표본을 완전히 설명하지 못한다. cadence 출처는 배포 설정이다.

`cmd/chill-server/live.go:64-80`의 1초 ticker는 resources 저장도 구동한다. 표시용 cadence와 구별하여 증거 API 설계의 고정1초 수집 방법론 C로 유지한다. 별도 loop/settings는 없다.

## 5. 측정 CR와 불변 실행

필수 입력·추천 실험값·코드 fallback·admission default를 구별한다. 기존 immutable spec/실행·결과 참조로 선택값을 연결하며 중복 snapshot 저장물을 만들지 않는다.

| 소유자 | 입력·범위 | 현재값/소비 |
|---|---|---|
| ProfilingRun.spec.measurement.durationToleranceSeconds | finite ≥0, 해당 duration 미만 | 0.5초; BEO active/idle protocol과 독립 idle protocol에서 전달하여 evidence 길이 판정 |
| BEO.spec.pointPolicy.confidenceLevel | (0,1) 십진 문자열 | 생략 "0.95"; §3-2의 단일 유도·CI |
| 기존 BEO.spec.trialsPerBatch | 정수 ≥2 | 현재 실험3 유지; 필수 입력이며 현재 default marker가 있다는 뜻 아님. 새 minimumRuns 없음 |
| 독립 idle.spec.pointPolicy.minimumRuns | 정수 ≥2 | 기존 필드 재사용, 현재3 유지 |
| 전이.spec.acceptedCountTarget | 정수 ≥2, ≤attemptLimit | 현재 목표5 유지, 방향별 적용 |
| occupancy.spec.durationSeconds/minimumPairs | New native bursts freeze configured duration and pair-count facts; retained legacy open-loop fields are decode-only |
| occupancy.spec.seed | uint64 | Native API freezes the configured seed into Conditions and the generator arguments; no compiled scientific default |

하한2는 BEO/idle minimum·attempt limit, 전이 target/attempt limit, baseline 자유도1·결과 재로딩·serving까지 연결하며 attemptLimit≥minimum/target이다. 비영분산active2/idle2도 기존 통계로 처리하고 정책목표3/5와 수학적 양의자유도를 구분한다. durationTolerance는 Run 준비에서 생략.5초/명시0을 정규화하고 동결RunConfig·curve identity·Python 요청에 남긴다. occupancy seed는 Conditions·생성기 인자가 같으며 PCG 두 번째 word는 고정 seed이지 stream selector가 아니다. 양측CI/2·n−1은 설정화하지 않는다. 과거 raw fixture 보존·현재 helper 입력과 구분, 단일decoder를 유지한다. CRD/chart 사본과 배포 구CLI 제거는 함께 이행한다.

### Native dispatch occupancy policy (W2)

The scheduler ConfigMap JSON has one top-level `dispatchOccupancy` block,
strictly decoded in `internal/serving/application/scheduler_service.go`. Its maintained
example is [`config/samples/serving-scheduler.json`](../../../config/samples/serving-scheduler.json).
The chart references an **external** ConfigMap: changing unrelated Helm values
does not install this block. Operators must update that actual configuration.
Omission disables rolling aggregation and makes DOM POST fail closed with
`503 OccupancyPolicyUnavailable`. With or without that block, the sole default
authority is explicit scalar `settings.defaultDispatchOccupancyNanos` (zero
permitted). Public settings GET/PUT and canonical PATCH validation use this
same field. The aggregation block cannot override it; duplicate
`defaultPerRequestNanos` knobs are rejected.
Invalid blocks are rejected, and POST reads the current ConfigMap rather than
falling back to a previously accepted policy. The accepted profile snapshot
must have that same ConfigMap resourceVersion.

Native DOM creation uses a short **configured closed-loop burst**, not a
saturation test. No Go scientific defaults or hidden duration/worker derivation
exist. The sample is illustrative, not a calibrated guarantee.

| Field | Bounds / meaning |
|---|---|
| `aggregationWindowSeconds` | int32 ≥burstSeconds; shared rolling scan and public query window cap, sample120 |
| `refreshIntervalSeconds` | int32 ≥1; sample60 |
| `scanTimeoutSeconds` | int32 ≥1 and ≤refreshIntervalSeconds/2; sample30 |
| `maximumRequests` | required positive selected-RequestID cap when the block exists; no runtime default; sample250000, exceeded cap rejects the whole refresh |
| `warmupSeconds` | explicit int32 ≥0, excluded from native measurement window |
| `burstSeconds` | explicit int32 ≥1, frozen as native `durationSeconds` |
| `workers` | explicit int32 in [batch,64], never silently clamped |
| `minimumPairs` | explicit int32 ≥1, a reported population requirement, not a confidence claim |
| `requestLifetimeSeconds` | int32 ≥1; covers ceil(SLO) and configured outstanding batches, within actual gateway/Workload cap |
| `deliveryMarginMillis` | int32 >0; rounded up independently for elapsed supply extension |
| `preparationSeconds` | int32 ≥1 |
| `jobSetupSeconds`, `collectionSeconds` | explicit int32 ≥0 |
| `plannedElapsedCapSeconds` | finite positive ≤int32 maximum; all segments must fit |
| `seed` | explicit uint64, including zero |

Sample `warmupSeconds=5`, `burstSeconds=15`, `workers=8`, `minimumPairs=100`
are explicit experimental choices, not a universal precision/stationarity
policy. `requestLifetimeSeconds=8` covers only Workloads whose effective lifetime
cap permits8s and whose SLO and profile-tail budget fit. Margin25ms exercises
ceil(25/1000)=1s in the extension; it is not a measured transport guarantee.
P2s/J10s/C10s and cap120s are configured allowances, not padded elapsed time.
No host campaign YAML/JSON or prior DOM value supplies these settings.

Rolling aggregation runs in one dedicated worker, never a scheduler Tick or
request-writer callback. It captures accepted canonical profiles and current
native metadata briefly, releases the scheduler lock, and uses the bounded
two-pass observation scanner over the configured rolling window and delivery
tail. A failed, timed-out or over-cap scan publishes no partial measurements:
selection returns to accepted legacy DOM, then the configured fallback.
The first computation also uses DOM/default rather than blocking initialization.
Each accepted profile/batch receives an exact resolved entry; only a B median
with at least configured `minimumPairs` can supersede DOM. Signed rolling facts
remain separate from the nonnegative operational value.

The sample `settings.defaultDispatchOccupancyNanos=2000000` is a conservative heuristic relative
to the one known cell's roughly0.2ms B median and0.27ms DOM, not a guarantee
across cells or demand. The scalar applies per request to every batch; only
measured B supplies batch-specific values. A120s window at the observed
roughly805 requests/s contains about97k requests; cap250000 leaves roughly2.6×
headroom for that reference workload, not universal demand coverage or a byte-allocation bound.
Timeout30s/refresh60s and one non-overlapping worker bound nominal scan duty to
50% of one core's wall-time budget. After every attempt the worker also idles
at least its entire scan+exact-reduction elapsed time, including a reduction
that crosses the scanner deadline. This is not a process CPU ceiling: Go GC,
runtime activity and other workers may use additional `GOMAXPROCS` capacity.
Empirical CPU/heap and serving-path acceptance are separate measurements.


For fixed batch B, K=configured workers, L=configured request lifetime,
m=deliveryMarginMillis/1000, t_max=maximum accepted profile service seconds,
and R_bytes=Σ(maxBytes(field)+4) for decoded length-framed fields:

```
Q_bytes = min(native maxQueueBytes, 64MiB)
require B <= K <= 64 and K*R_bytes <= Q_bytes
q = ceil(K/B)-1
require B*q <= native maxQueueRequests and B*q*R_bytes <= Q_bytes
require L >= ceil(SLO seconds) and L >= ceil((q+1)*t_max+m)
require L <= effective gateway/Workload lifetime cap
W = configured warmupSeconds
D = configured burstSeconds
R = ceil(L+t_max+m)
S = L+ceil(deliveryMarginMillis/1000)
E = preparationSeconds+jobSetupSeconds+collectionSeconds+W+D+S+R
require E <= floor(plannedElapsedCapSeconds)
```

Failure returns `400 OccupancyInfeasible`; it never decreases configured workers,
duration, minimumPairs or allowances to fit. `maximumIdleFraction`,
`estimatedIdleFraction`, refill bounds, idle-error gates and precision planning
coefficients no longer participate in creation or assessment.

The exact block, ConfigMap identity/resourceVersion/key, Node UID, accepted
profile pointer identity/reference/evidence/raw population and Workload/SLO
identity are frozen in `Conditions.InputsJSON`. Computed facts include tail,
decoded request bytes, queue caps, minimum lifetime and supply extension.
`ValidateConditions` checks this object once at creation; dry-run and create
share that same object. Native admission rejects changed accepted identities.

The actor runs fixedBatchSize=B with maxBatchWaitMS=0. After drain, its frozen
native metadata proves policy/profile/decision/AppliedAt for the shared
`serving.OccupancyEstimator`; receipt scans only gather rows, worker-native
starts bound the actual half-open burst window. Too few valid pairs yields
`Insufficient/TooFewPairs` **with statistics retained**, including signed values.
Zero pairs has null statistics, never a numeric fallback.

Evidence is a new factual `v1alpha2` record under
`serving/dispatch-occupancy-observations/`, verdict `Observed` (native phase
Complete) or `Insufficient`. It is **not** an accepted legacy DOM scalar. Existing
`serving/dispatch-occupancy/` v1alpha1 accepted objects and `CostEvidence` remain
active and are not deleted. The scheduler's rolling B cache reads serving
observations directly; it does not reinterpret the new burst schema or merge
the two evidence populations.

The configured load-generator Node reservation, W6 slots/locks, request writer
and evaluation arrival protocol are unchanged.

### Selected-request cap and the serving Pod memory budget

The chart's actual `servingEntrypoint.resources.limits.memory` is **4Gi**, with
`servingEntrypoint.goMemLimit: 3GiB`; `serving_entrypoint.yaml` renders that
resource block directly. The nearby **512Mi** limit belongs to `dashboard`,
not ChillServer. Recalculate these settings together when overriding the serving
Pod's memory limit.

The successful near-cap local experiment measured 248,000 complete requests,
153,716KiB phase-start RSS and 559,264KiB whole-process high-water RSS. Using
the smaller known request count gives a conservative sizing coefficient for
this fixture: `(559264 - 153716) * 1024 / 248000 = 1674.52 bytes/request`.
This combines process measurements, not an isolated allocator measurement or
a guaranteed linear memory bound; eligible pair-vector memory was not measured.

Allowing at most roughly25% of the **4Gi** limit for the selected-request state
gives `floor((4Gi / 4) / 1674.52) = 641223` requests. Do not raise the sample
to that estimated ceiling: its explicit **250000** cap is retained, implying
approximately399.24MiB incremental state, **9.75%** of the4Gi limit. With the
sample120s window this covers nominally **2083 selected IDs/s** in aggregate,
not2083/s per cell; late supplements, in-flight cohorts and unmatched requests
also use the cap, so leave rate headroom.

For a deployment overriding the serving limit to **512Mi**, the same25%
calculation gives80152 requests; use the more conservative explicit
`dispatchOccupancy.maximumRequests: 75000` (approximately119.77MiB incremental
state, nominally625 selected IDs/s over120s), and adjust `goMemLimit` with the
Pod limit (384MiB retains the chart's75% ratio). Do not use the250000 sample
unchanged for a512Mi serving Pod. Both recommendations need workload-specific
RSS/CPU measurement; count and cooperative-time limits are not byte/CPU ceilings.

`maximumRequests` is **required if `dispatchOccupancy` is present**: omission,
null, zero and negative values are rejected, without a Go fallback to250000.
Omitting the entire block disables rolling B aggregation; configured legacy
DOM/default selection remains available. Exceeding an explicit cap publishes
no partial measured facts and returns selection to DOM/default.

## 6. 하네스: bind에서 한 번 선택

증거 API cutover 후 잔존 실행 경로만 대상으로 한다. effective config를 `binding.config`에 보존하고 실행·분석은 이를 소비한다. binding은 일반 JSON 파일이므로 물리적 write-once가 아니다. 완결 후 수정 금지, 새 실험 새 binding 계약이다. 환경 JSON을 실행 중 다시 읽지 않는다.

| 키/기존 표현 | 단위·범위 | 실행·분석 소비자 |
|---|---|---|
| stages[].id/seconds/alpha/parts | 기존 example 표현; id 식별자, seconds finite >0, alpha finite ≥0, parts도 기존 구조 유지 | bind schedule 생성→producer stage/part rates·offset→analyze 단계 모집단 |
| workloads별 seed/arrivals | uint64; 지원 도착 방식 | trace/producer 생성→binding seed 및 check 대조 |
| 기존 generatorQuality | lateness 시간 ≥0, fraction [0,1] | generator 원시 품질 집계→analyze 수용 |
| g0Evidence.durationSeconds/alpha | finite >0 / ≥0 | one-shot shared-CPU G-check 실행 입력→선택 입력과 evidence 대조; CPU fraction 정책 없음 |
| 기존 workload SLO percentile | (0,100] | 요청 지연 원시에서 선택 percentile 계산→G0/분석 수용; p95 필드 이름만 바꾸지 않음 |
| inputs.maximumIdleNanosPerRequest | finite ≥0, ns/request | 입력 closure 검증→분석 eligibility |
| failureInjectionRunbook | 기존 문서 구조 | 입력 config 우선, 누락 때만 example 기본 선택→binding 보존→운영자 주입·분석 |

closure는 선언 target 목록 길이와 실제 모집단을 비교한다. expectedClosureCount는 없다. full/static/factor 시간은 schedule/선택 N에서 유도한다. `conductor.py check:2610-2614`의11700·10200·seed 집합·21900도 binding schedule/seed에서 유도한다. 기준은 example만이 아니라 현재 실행 함수의 schedule·fallback이다.

DELAY_TOLERANCE_NS=10ms는 delayed 표시 방법론 C다. --delay-tolerance나 존재하지 않는 단독 CLI metadata를 신설하지 않는다. 기존 arrivals/seed/workers/delivery/socket/lifetime와 generatorQuality를 동결한다. poll·marker poll·종료 대기·단순 command timeout은 측정·수용 영향 근거가 없으면 C, 기존 안전 예산은 D다. 경로별30/129초·1/2/5초를 합치지 않는다. 삭제할 CHILL host-reader/SSH/crictl 수집은 설정화하지 않는다. client 수집·STOP loop·운영자 장애 주입은 유지한다.
예정 대체: 현재 로컬 `binding.config` 동결·운영자 장애 주입은 유지하되, [실험 실행 설계](../profiling/experiment-execution.md) / [실험 SRS](../../spec/experiments/SRS.md)에 따른 정의·독립 실행으로 대체할 예정이다.

`demo.py` offline 회귀와 실제 E5가 호출한 bind_before SETTINGS/eligibility, Conductor API·publication·producer·삭제, native 요청, analyze stats/quality는 유지한다. 독립 D2 main·옛 수집 비활성 경로는 제외하며 helper 전체를 삭제하지 않는다.

### 잔존 하네스의 선택값

잔존 인벤토리29행은 유지했고 cutover 후보12행은10제외/2C(support/hour·trapezoid)다. 아래 값은 config→binding.config로 한 번 고르며 e5-wire 승인입력/demo spec도 같은 키를 전달한다.

| 키 | 생략 기본값 | 소비자 |
|---|---|---|
| stages[].id/seconds/alpha/parts | 기존 `normative_stage_specs(burstLowSeconds)`의 8개 evaluation row/12개 part: 600,1800,900,1800,900,1200,3600,900초; 기존 α 그대로 | bind → 연속 part trace/producer → judge; 명시 part ID 보존. demo의 필수 stages는 기존대로 |
| seeds | canonical MNV101/PLC202/Smol303 | bind trace·smoke·workload.seed·Job 이름/인자·check |
| arrivals | canonical workload별 poisson | bind trace·workload.arrivals·producer의 모든 segment; fixed도 허용 |
| g0Evidence.durationSeconds / alpha | 120 / 1.2 | one-shot G-check schedule·rates 검증; CPU peak는 absolute 관측으로만 보고, 판정에 쓰지 않음 |
| workloads[].slo.percentile | 기존 필수 Workload SLO 입력, 별도 덮어쓰기 없음(현재 PLC95) | G0는 client requestID로 조인한 raw `elapsedNanos`의 nearest-rank 선택 분위수, judge도 선택 SLO 소비 |
| inputs.maximumIdleNanosPerRequest | 1000000 ns/request | d25 closure 검증, `d25Acceptance` → 분석 eligibility |
| burstLowMaximumSeconds / collection.maximumDurationSeconds | 900 / 4200초 | burst 입력·각 schedule row의 setup+seconds+drain+flush 검사; 최장3600초 리터럴 제거 |
| timeboxes.maxActuationSeconds / nativeCallerSeconds / nativeFutureAllowanceSeconds | 900 / 960 / 15초 | native release/호출 timeout·alert freshness(900+15 유도), caller>actuation |
| producerResources / producerShards | memory1Gi / MNV3,PLC1,Smol1 | producer memory requests/limits·process 분할; CPU key 불가. EvaluationRun은 동결된 memory resources 사용 |
| poolShares | MNV.45,PLC.1,Smol.45 | workload별 intended/actual rate |
| failureSelectSnapshotTTLSeconds | 15초 | 장애 선택 snapshot age/expiry |
| tokenSafetyMarginSeconds | 60초 | bind·각 실행 token lifetime 사전 점검 |
| timeboxes.finishResetWaitSeconds / powerDownSeconds / factorAckWaitSeconds | 600 / 1800 / 120초 | zero-state reset 대기·reset HTTP 총 예산·복원 ACK |
| timeboxes.apiSeconds | 기존 evaluation 필수 입력 유지; 일반 정규화 fallback30초 | 각 경로 API 예산; 승인129초를30초로 바꾸지 않음 |
| collection.energyCoverage | maxGapFraction=.01,maxSingleGapSeconds=30 | 에너지 row gap 수용 |
| generatorQuality | latenessToleranceNS=50000000,maxExceedingFraction=.01 | 실제 issuance/품질 수용 |
| failureInjectionRunbook | 누락 때만 기존 example 문서 | 선택 문서를 config와 failure row에 보존, 운영자 주입/분석 |
| workloads[].workers/deliverySeconds/socketSeconds/lifetimeSeconds | 기존 필수 workers/delivery/socket 유지; lifetime 생략 None | 기존 CLI 인자로 동결·재생; None은 request-lifetime 인자 미전달, CLI fallback 유지 |

`fullSeconds`, `staticSeconds`, `factorSeconds` 및 `check`의 시간/seed 대조는 binding schedule/config에서 유도한다. closure 모집단도 선언 target 목록과 비교한다. 새 closure count·G0 count·delay/poll 키는 없다. 현재 evaluation→실행 helper는 측정 CR을 생성하지 않으므로 슬라이스2 후속 CI/tolerance/occupancy seed 선택 전달 대상은 없다. 기존 운영 입력 sample은 §7의 완전 문서 선택을 따른다.

기존 실제 bind와 새 기본 bind의 stage/part 시간·rates·offset·offered/full11700/static10200/factor2100×N/payload/κ/settings를 offline 대조했다. E5/demo/report/collector self-check, load-generator32검사·serving/kube 검사 및 비기본 stage/fixed/seed7/G0(37초/.8/.75)/PLC p90·freshness 회귀가 근거다. 키/hash/바이트 동등성 관문은 없고 운영 전체 bind/E5·클러스터·SSH·배포는 미실행이다.


## 7. B 환경값 연결

실제 registry·Node/runner·외부 endpoint와 잔존 실행 namespace만 B다. 이미 values에 있는 repository·region·장비 catalog·signature·mode·clock은 D다. /proc·/sys·SA token·내부 TLS/data/evidence mount는 C 또는 기존 D, 내부8080/9000과 이미지 UID/GID65532는 패키징 C다. example-*·repo-relative echo·artifact 내부 경로는 예제/산출물 계약으로 유지한다. 기본 values를 비우지 않는다. 현재 testbed discovery profile과 chart fail 조건을 별도 재설계하지 않는다.

각 행의 이동처는 하나다. 배포값은 기존 해당 Helm values 키, 잔존 E5/helper namespace는 `CHILL_EXPERIMENT_CONFIG.d2.namespace`→bind 시 `binding.config.namespace`다. `e5.py:1316,1519`, `e5-wire.py:105`를 포함한다. 역사 조회2533 등은 cutover 뒤 잔존 여부로 제외한다.

실제 registry·Node를 가진 운영 샘플은 환경 JSON `samples["<기존 상대 파일 경로>"]`에 완전한 입력 문서로 보관하고 기존 등록/측정 요청 절차에서 선택한다. 정적 YAML의 JSON 참조 자동 해석은 없다. immutable runtime declaration은 image와 inspection 사실을 묶은 문서 전체를 이동한다. 주소만 바꿔 같은 검증 사실처럼 꾸미지 않는다.

구현은 catalog8·NVIDIA class/전이2·ARM runtime1의 원본 전체를 private `inputs/environment{,-212}.json` samples에 보존했다(총22문서 offline 필드대조/선택,정적YAML20회 파싱). 정적 예제는 example registry/hardware이며 검증 사실을 일반 image에 붙이지 않도록 runtime declaration 전체를 제거했다. `jq -e '.samples[$path] // error(...)'`로 명시선택 후 기존HTTPS/kubectl에 전달하며 자동YAML 해석은 없다. namespace 생략 `chill-system`, 실제선택 bind동결이다.


## 8. 이행 경계

증거 API 승인배포·1회리허설·우회수집 제거 하네스cutover·대시보드 브라우저수용 기록과 의존캠페인 없음이 live 시작조건이다. rev106시도/rev110결손을 전체PASS로 읽지 않는다.
슬라이스2~5는 측정정책→capacity/reuse→기존기록→잔존하네스/환경값을 구현했다. C정리는 관련변경에 묶고 독립35행 관문은 없다. 공유파일·최종Go/API/Helm검증은 통합소유자, 저장변경은 캠페인 사이1회다.

### 저장 계약

ADR0030에 따라 prefix/version v1alpha5를 유지한다. 새 stream·관측 디렉터리 cutover는 없다. archive/publication #213~#225는 수정·삭제·재게시하지 않는다. 과거 행의 새 필드 부재는 당시 기록되지 않은 사실이며 현재 기본값으로 소급 채우지 않는다. 새 CapacityInput은 새 κ 키를 쓴다. ADR0019의 정리는 읽을 수 없어진 영향 작업용 CR/파생 객체를 열거해 운영자가 수행하며 과학 원시 archive와 구별한다. reader는 단일 버전, 과거 필드 부재의 의미 규칙만 명시한다. dual decoder/migration/alias는 없다.

CPU 공유 실행 cutover는 `e5-binding.v2`, `demo-e5-binding.v2`, `d2-before-binding.v2`만 실행한다. 이전 binding reader/alias는 없으며 원시·publication·기존 판정 기록은 수정하거나 재해석하지 않는다. CPU request/limit/affinity와 독점 matching은 제거하되 메모리/GPU/Node UID/물리 반환·producer cgroup usage/throttling 관측은 유지한다.

G0 기록은 `schema: g0-verdict.v2`다. 기존 workload supply/rate/quality/receiptLatencyMS/withinSLOFraction과 `cpuCoresPeak`/`memoryBytesPeak`를 보존한다. `server`는 같은 실행에서 기록한 `memoryLimitBytes`, `missingSamples`, `restarts`, `oomKills`; `gateway`는 원시 요청의 `rejected`, `unavailable`(503), `timeouts`, `incomplete` 수를 담는다. `cpu10sSamples`는 순서대로 `bin`, 양수 `samples`, 실제 `podUID`/`containerID`, `cpuCores`, `memoryBytesMax`, `nrThrottledDelta`, `throttledNanosDelta`를 담는다. 관측 누락/정체성 누락/메모리 한계 도달/재시작/OOM/게이트웨이 실패/CS throttling은 거절한다. `csCPULimitCores`, `maximumCPUFraction`, `csCPUFraction`은 폐기했고 absolute CPU peak의 크기는 합격 조건이 아니다. 새 G0는 이 기록을 새 shared binding과 함께 남겨야 하며 과거 G0로 채우지 않는다.

Coverage generator의 대상 선택·요청 재사용·timing 범위는 curve/evidence 버전을 비교하며 `runtimeImage`는 기록만 남긴다(사용자 결정, ADR 0019 예외). 완료는 curve-plan v1alpha11/finalized evidence v1alpha17(내부 RunConfig v1alpha19, raw active10/idle8)로 확인한다. strict reader는 이전 형식을 해석하지 않는다. 진행 중 요청의 `chill.dacs.io/coverage-execution-conditions` 형식은 유지하지만 이미지 값은 key 조건이 아니다. CPU 통제/버전 없는 과거 실행은 공유 실행을 생략할 근거가 아니며 Failed는 재계획한다. model/device/path/powerMode/backend/batch 범위와 v2 replaces 계보는 유지한다. `coverage-plan.v2`의 메모리 제외는 별도 안전 정책으로 원래 cap/원시 실패/보수적 운영 제외 근거를 보존하며, image·경로·worker limit이 알려지고 같을 때만 적용한다. 메모리 조건 변경·미상은 옛 OOM 확정 대신 정책 검토로 표시한다. 공통 actor Config의 envelope는 기존 execution declaration과 `min(runtimeMemoryLimitBytes, int64(float64(실 Node allocatable bytes) × profiler measurementMemoryFraction))`에서 준비 시 한 번 동결하며 serving Pod와 measurement Job이 그 값에서 파생된다. 과거 frozen plan/raw는 변경하거나 삭제하지 않는다.

현재 셀 판정은 Query `currentMeasurement`(id/model/class/path/powerMode/measuredAt)만 참조하고, 과거 variants/profileIds에서 성공을 찾지 않는다. 필드가 없는 옛 응답은 판정 불가로 셀을 보류하며 `unknown-support.json.selectionReason`에 기록한다. `usable`/`problem`/`insufficient`는 표시이고 실행 거절 조건이 아니다. 실제 schema·Node/GPU/artifact·메모리 안전 조건은 별도다.

executionDeclarations는 같은 scope(class/Node/family/backend)에서 문서 순서 마지막 matching 선언 하나가 현재 값이다. duplicate/overlap은 새 버전 목록을 만들거나 RuntimeDeclarationAmbiguous로 거절하지 않고 마지막 값으로 덮어쓰며, compatible 조회에는 superseded 선언 진단만 남긴다. generator도 같은 순서를 사용한다.

### 기본값과 검증 근거

settings 생략/명시20/.005/100은 변경 전 fixture의 μ1000/κ959.375·Grid/Bisection판정과 대조한다(키동일성 아님). CI.95의 평균/분산/자유도/음수거절·해상도, reuse30초/3회/상대차, CRtrial3/target5·active2/idle2, 기존 schedule/seed/fallback, timeout30/129초·poll1/2/5초를 유지한다.
배포 검증은 승인overlay/revision/release/namespace와 default/profiler render의 의미 있는 resource/field delta를 명시한다. values-operating.yaml은 rev74캡처이지 현재 배포값이 아니며 offline 완료로 live동일성을 주장하지 않는다.

## 9. 열린 점

기존 ?10행은 자동 API 승격하지 않는다. overload1초/.9, occupancy cap64/64MiB, ready/drain/actuation/admission poll, 미확인 장치1Gi, disk310GB, probe/emptyDir/single-writer 배치가 해당한다. 기본 동작을 유지하며 안전 경계와 측정 입력의 실제 관계로 별도 결정한다.
