# Plan 3: Profile Measurement Derivation

## 결과 요약 — completed
- 결정: scientific admission removed @`788bcf93`. `ProfilingRun`은 execution evidence 저장으로 완료하며 admission policy/status/reconciliation은 없다. Job failure·invalid boundary·storage error는 그대로 오류다.
- `profiler/projection`은 verified raw active/idle evidence를 투영하고 `derivation`은 wall energy, loaded-idle mean power, same-state idle을 뺀 incremental J/item, 반복 통계·confidence interval·batch 비교를 계산한다.
- 전체 runtime-declared window에 선형 보간을 적용하고 양끝 sample을 window boundary까지 연장한다. 서로 다른 power timestamps ≥2개가 필요하다. invalid number/window, denominator 누락, state 불일치, duplicate trial은 오류이며 계산 불가 값을 만들지 않는다.
- sample count/coverage/gap/thermal/contamination 기준으로 trial을 선택·제외하지 않는다. read failures·thermal·workload events는 관측으로 보존하며 환경 악화/누락은 성공 Run의 계산을 막지 않는다. 해석·제외는 experimenter 소유다. 음수 incremental estimate도 보존한다.
- curve는 Loader-verified active/idle을 frozen Node/artifact/SDK/backend/input/power/CPU quota/affinity/mode protocol에 결합한다. standalone idle은 첫 frozen condition에 결합한다. digest는 provenance label이며 gate가 아니다. external environment 차이는 baseline/trial 선택 기준이 아니고 aggregate 환경 label은 첫 trial만 설명한다. memory/duration/latency/power/energy는 identity가 아니다.
- consumer는 completed Runs/evidence를 직접 사용한다. `CompletedRuns`/`CompletedTrials`는 승인된 실험 수가 아니다. batch result는 `batch-energy-optimization-result.v1alpha4`, idle result는 `model-loaded-idle-baseline-result.v1alpha2`; old readers 없이 raw execution schema는 유지한다. active campaign 위에 배포하지 말고 old requests/results를 종료 후 새 측정을 시작한다.
- Serving 경계만: 음수 batch mean에 기존 `Aggregate`의 two-sided **95% Student-t Welch–Satterthwaite** interval을 쓴다. active mean variance + shared idle mean-power variance × mean window seconds/item을 결합하고 shared uncertainty를 active trial 수로 나누지 않는다. 이 95%는 다른 serving confidence 설정과 독립이다.
- interval upper ≥0이면 `energyPerInputJoules: 0`, `energyBelowResolution: true`를 serving records/API/replay에 발행한다(실측 0이라는 주장이 아님). strict native startup bundle에는 기존 `energyPerItemJ: 0`만 보낸다. positive/exact-zero는 변경 없고 marker false/JSON 생략으로 immutable bytes를 유지한다.
- upper <0이면 profile 전체가 `NegativeIncrementalEnergy`로 unavailable이며 batch·signed estimate·interval(J/item)을 진단에 남긴다. 독립 재측정이 필요하고 양수까지 재시도하지 않는다. native admission/replay는 계속 negative energy를 거부하며 raw evidence/schema는 바뀌지 않는다.
- 근거 커밋: `788bcf93` admission 제거, `58348057` confidence/policy input binding. [projection](../../../../internal/profiling/profiler/projection) · [derivation](../../../../internal/profiling/derivation).
- 남은 것: scientific suitability 판정은 CHILL scope 밖이다. [Profiling index](../README.md) · [Plan 7](07-batch-energy-optimization-control-path.md).
