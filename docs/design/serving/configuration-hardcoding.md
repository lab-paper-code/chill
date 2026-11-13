# 설정 소유권과 실행 기록

측정값과 판정에 영향을 주는 입력은 해당 측정 또는 실험 정의가 소유한다. 환경값은 배포 설정이 소유하며 선택한 값과 실제 실행 신원을 기존 불변 실행 기록에 동결한다. 저장 계약은 [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md), 설정 경계는 [Configuration Ownership and Freezing Paths](../../adr/0031-configuration-ownership-and-freezing.md)를 따른다.

## 설정 적용과 증거

판단 당시 적용값은 `Decision.Settings`가 권위다. settings PUT은 의도 기록, ConfigMap 저장, 마지막으로 완전히 수락한 typed 입력에 정책 적용, 결과 기록 순서로 수행한다. `appliedAt`은 정책 수락 시각이다. 원격 catalog 전체 수락과 물리 전환 완료는 별도로 확인한다.

| 상태 | 해석 |
|---|---|
| 의도 기록 실패 | ConfigMap 변경 전에 중단 |
| 저장 성공 후 정책 적용 실패 또는 요청 만료 | 저장값과 현재 적용값이 다를 수 있으며 `appliedAt` 없음 |
| 정책 적용 성공 후 결과 기록 실패 | HTTP 오류가 반환돼도 정책은 적용됐을 수 있음 |
| 후속 full refresh의 적용 | 당시 Decision의 실제 settings로 확인 |
| 적용 증거 부족 | 적용 여부 미확인 |

초기값과 외부 ConfigMap 변경은 full refresh가 수락한 뒤 Decision의 전체 settings로 확인한다. ConfigMap 출처는 보존된 참조와 binding 범위에서만 주장한다. 별도 provenance 저장소나 초기 before=after 변경 행을 만들지 않는다.

## Serving 설정과 측정 정책

`SchedulerSettingsInput`이 생략 기본값을 채우며 GET, Decision과 설정 변경 기록은 실제값을 담는다. 범위 오류와 NaN/Inf는 적용 전에 거절한다.

| JSON 필드 | 범위 | 생략 기본값 | 소비 |
|---|---|---:|---|
| `capacityGridSteps` | 정수 ≥1 | 20 | 요청률 격자 |
| `capacityBisectionWidth` | finite (0,1] | 0.005 | width × 처리율에 의한 이분 탐색 종료 |
| `capacityTailSamples` | 정수 ≥1 | 100 | 설정 분위수의 tail 표본 수 |

세 값은 `CapacityInput`에 동결하며 용량 행의 identity에 포함한다. 수치 계약은 [용량표 계획기](analytic-planner.md)를 따른다.

| 소유자 | 입력과 현재 경계 |
|---|---|
| `ProfilingRun.spec.measurement.durationToleranceSeconds` | finite ≥0이며 측정 duration 미만. 생략 기본값 0.5초, 명시한 0은 유지. RunConfig와 측정 조건에 동결 |
| `BatchEnergyOptimization.spec.trialsPerBatch` | 활성 및 독립 유휴 관측 각각 3회로 고정 |
| `BatchEnergyOptimization.spec.pointPolicy.confidenceLevel` | (0,1) 십진 문자열, 생략 `"0.95"`. 해당 측정의 유휴 기준과 증분 에너지 CI에 사용 |
| `ModelLoadedIdleBaseline.spec.pointPolicy` | 해당 측정이 최소 반복 수와 신뢰수준을 소유. 최소 반복 수 ≥2, attempt limit은 최소 반복 수 이상 |
| 전환 측정의 `acceptedCountTarget` | 방향별 정수 ≥2이며 attempt limit 이하 |
| profile reuse annotation | 확인 창의 최소 길이와 최소 trial 수 |

전역 `energyConfidenceLevel` 또는 `profileConfidenceLevel` 별칭은 없다. Profile은 실제 유도에 쓴 신뢰수준을 기록한다.

`chill.dacs.io/profile-reuse` JSON은 `sourceProfileID`, `maximumRelativeDifference`, `minimumMeasurementSeconds`, `minimumTrials`를 받는다. 최소 측정 길이는 finite >0이고 생략 기본값은 30초다. 최소 trial 수는 정수 ≥2이고 기본값은 3이다. 길이 조건은 active와 idle 각각에 적용한다. 선택값과 실제 길이 및 trial 수는 `ProfileReuseCheck`에 기록하며 reuse identity에도 포함한다. 전역 settings 변경은 기존 reuse 요청의 정책을 바꾸지 않는다.

## 기동 설정

기존 cluster stream의 `server-start`가 실제 프로세스 시작 시각, Pod 신원과 선택한 `sourceIntervalNanos`, `profileIntervalNanos`를 기록한다. chart 기본값은 각각 1초와 1분이며 운영자가 바꾼 값도 실제 선택값으로 남긴다. 기록 실패 시 기동 실패 계약을 유지한다.

gateway 큐 상한은 Workload의 `spec.admission.maxQueuedRequests`에 있고 applied Definition에 동결한다. 기동 전역 queue limit을 두지 않는다. 요청, drain, operation, 수집 주기는 각 소유 경로의 설정을 사용한다. 추론 handler의 별도 시간 상한을 일반 request timeout으로 설명하지 않는다.

Node sampling과 batch cadence의 출처는 배포 설정이다. `sampledAt`과 `receivedAt`은 관측 시각이며 선택한 cadence와 누락 표본을 모두 설명하지 않는다.

## Native dispatch occupancy policy

The scheduler ConfigMap has one strictly decoded top-level `dispatchOccupancy` block. Its maintained example is [serving-scheduler.json](../../../config/samples/serving-scheduler.json). The chart references an external ConfigMap; operators must update that actual object.

Omission disables rolling aggregation and makes native measurement POST return `503 OccupancyPolicyUnavailable`. The sole scalar fallback is `settings.defaultDispatchOccupancyNanos`, including zero. The block cannot override that field; duplicate fallback knobs are rejected.

POST reads the current ConfigMap. The accepted profile snapshot must have the same resourceVersion. A settings-only PUT leaves the full-acceptance resourceVersion unchanged, so native measurement creation can remain unavailable until profile refresh accepts the complete configuration.

Native creation uses a configured closed-loop burst. The sample values are experimental choices and do not guarantee saturation, precision or stationarity.

| Field | Bounds and meaning |
|---|---|
| `aggregationWindowSeconds` | int32 ≥ `burstSeconds`; rolling scan and public query window cap |
| `refreshIntervalSeconds` | int32 ≥1 |
| `scanTimeoutSeconds` | int32 ≥1 and ≤ half the refresh interval |
| `maximumRequests` | Required positive selected-request-ID cap; overflow rejects the whole refresh |
| `warmupSeconds` | Explicit int32 ≥0, excluded from the native measurement window |
| `burstSeconds` | Explicit int32 ≥1, frozen as native `durationSeconds` |
| `workers` | Explicit int32 from batch size through 64 |
| `minimumPairs` | Explicit int32 ≥1; a population requirement |
| `requestLifetimeSeconds` | int32 ≥1; covers SLO and outstanding profile-tail batches within the gateway and Workload lifetime cap |
| `deliveryMarginMillis` | int32 >0 |
| `preparationSeconds` | int32 ≥1 |
| `jobSetupSeconds`, `collectionSeconds` | Explicit int32 ≥0 |
| `plannedElapsedCapSeconds` | Finite positive value ≤ int32 maximum; contains every segment |
| `seed` | Explicit uint64, including zero |

For fixed batch size B, configured workers K, lifetime L, delivery margin m in seconds, maximum measured service time t, and decoded length-framed request bytes R:

```text
Q = min(native maxQueueBytes, 64MiB)
require B <= K <= 64 and K*R <= Q
q = ceil(K/B)-1
require B*q <= native maxQueueRequests and B*q*R <= Q
require L >= ceil(SLO seconds) and L >= ceil((q+1)*t+m)
require L <= effective gateway/Workload lifetime cap
drain = ceil(L+t+m)
extension = L+ceil(deliveryMarginMillis/1000)
elapsed = preparationSeconds+jobSetupSeconds+collectionSeconds
          +warmupSeconds+burstSeconds+extension+drain
require elapsed <= floor(plannedElapsedCapSeconds)
```

An infeasible plan returns `400 OccupancyInfeasible`. It never reduces configured workers, duration, minimum pairs or allowances to fit.

The exact block, ConfigMap identity/resourceVersion/key, Node UID, accepted profile identity/evidence/raw population and Workload/SLO identity are frozen in `Conditions.InputsJSON`. Computed facts include service tail, decoded request bytes, queue caps, minimum lifetime and supply extension. Dry-run and create use the same validated object.

The actor uses fixed batch size B with zero batch-fill wait. Frozen native metadata proves policy/profile/decision/applied-at identity. Worker-native timestamps bound the actual half-open burst window. Too few valid pairs yields `Insufficient/TooFewPairs` with statistics retained. Zero pairs has null statistics.

The factual observation under `serving/dispatch-occupancy-observations/` remains separate from accepted legacy scalars under `serving/dispatch-occupancy/`. The rolling cache reads serving observations directly without merging the two stored evidence populations.

### Rolling aggregation and memory

One dedicated worker scans outside Tick and request-writer callbacks. It briefly captures accepted profiles and native metadata, then releases the scheduler lock. Each scan is bounded by time and selected request IDs. Failed, timed-out or over-cap scans publish no partial or stale measured result; selection returns to accepted legacy measurements and the configured fallback.

The first decision uses legacy/default values without waiting for aggregation. Each profile/batch receives an exact resolved entry. A batch median can supersede legacy measurements only after the configured minimum pair count is met. Signed measured values stay separate from the nonnegative operational value.

After each attempt the worker idles for at least its scan and reduction elapsed time, including reduction beyond the scanner deadline. This limits nominal scan duty. Go GC, runtime activity and other workers still consume process CPU.

The chart's serving memory limit is 4Gi and its `goMemLimit` is 3GiB. The sample selected-request cap is 250000. Reassess both memory settings and the explicit request cap when overriding the Pod limit. Count and cooperative-time limits do not establish byte-allocation or process-CPU ceilings; measure RSS and CPU under the intended workload.

## 실험과 환경 입력의 동결

현재 evaluation 입력은 불변 실험 정의와 실행에서 선택한다. scenario가 Workload, seed, 도착 분포와 명시 부하 part의 시간 및 요청률을 제공한다. load-generator Node와 memory-only resources, SLO와 품질 판정, 필요한 운영 시간 예산을 기존 input snapshot에 동결한다.

conductor는 이 입력을 `binding.config`와 schedule에 보존하고 실행 및 분석이 같은 binding을 사용한다. 완결 후 binding을 수정하지 않으며 새 실험은 새 binding을 만든다. namespace는 실행에서 선택한 값을 사용한다. 입력 소유권은 [실험 실행 설계](../profiling/experiment-execution.md)와 [실험 SRS](../../spec/experiments/SRS.md)를 따른다.

registry, 실제 Node, runner와 외부 endpoint는 기존 배포/환경 입력으로 제공한다. 런타임 선언은 image와 inspection 사실을 함께 보존한다. 주소만 바꿔 같은 검증 사실로 취급하지 않는다. 정적 YAML의 JSON 참조를 자동 해석하지 않는다.

coverage helper의 목표 선택은 curve와 evidence schema를 비교하며 runtime image를 역사 metadata로 보존한다. 제품 profile 선택은 측정 image와 runtimeVersion을 적용 조건에 포함한다. 두 선택 경계는 각 소비자의 source를 따른다. Query의 현재 측정 참조를 사용하며 과거 variant의 성공을 찾아 현재 셀을 채우지 않는다.

같은 runtime scope의 execution declaration은 문서 순서에서 마지막 matching 선언을 사용한다. 메모리 안전 정책의 제외와 측정 실패 원문은 보존하며, 조건이 달라지거나 미상이면 과거 OOM을 현재 사실로 확정하지 않는다.

## 저장 계약

현재 reader가 수락하는 단일 계약을 사용한다. 과거 행의 필드 부재는 당시 기록되지 않은 사실이며 현재 기본값으로 소급 채우지 않는다. 저장 형식 변경은 campaign 사이에 한 번 수행한다. 읽을 수 없어진 작업용 CR과 파생 객체의 정리는 게시된 과학 원시 archive의 보존과 구분한다.

관측 stream과 API의 경계는 [Observation Evidence API Streams](../../adr/0030-observation-evidence-api-streams.md)와 [관측 설계](observability-split.md), 현재 측정 계약은 [측정 API](measurement-api.md)를 따른다.
