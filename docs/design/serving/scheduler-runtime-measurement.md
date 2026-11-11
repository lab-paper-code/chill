# Scheduler 실행기와 측정 계약

현재 실행·시간·측정 계약의 원본이다. 판단·전환은 [구현 설계](scheduler-implementation.md), 사용자 API·등록·drain은 [Workload 관리](workload-management.md), 과거 결과의 범위는 [실행 결과](scheduler-execution-history.md)를 따른다.

## 요청과 실제 모델

외부 API는 입력1개를 Preprocessor 원시 필드(`BYTES`, base64 `data`)로 받고 개별 응답한다. native `/v2/models/{model}/infer`는 필드마다 원소1개를 보내고 firstfloor가 batch 축을 붙인다. metadata는 request ID/UID/불변 definition/원래 receipt/개별 percentile·target/전달 elapsed/명시 수명/profile 근거를 보존한다.
경계는 필드 이름 집합·BYTES datatype·길이1~maxBytes, JPEG 시작 바이트+`image.DecodeConfig`, UTF8 `utf8.Valid`를 검사한다. 화소 복호/모델 입력 생성은 runner 전처리 module 책임이며 [이기종 워크로드](heterogeneous-workloads.md)가 전체 원시 입력 계약이다.
firstfloor는 model/input/runtime 고정 runner 하나에서 같은 UID 호환 입력을 배치하고 개별 SLO/결과를 보존한다. ORT CPU/실제 CUDA/RKNNLite SDK의 tensor/batch/layout capability를 확인하며 범위 밖은 오류다. CUDA는 확인한 물리 GPU1개만 사용하고 CPU fallback/CPU-only graph는 거부한다. provider 목록은 정확히 CUDA EP→default CPU EP(ORT shape 부분 그래프의 CPU 배치는 허용)이며 실제 목록을 profile에 남긴다. echo는 explicit synthetic 실행이다.
IPC는 bounded length-prefix header+binary payload다. frame 길이는 big-endian uint32, 작은 header는 JSON이며 raw 필드별 `{name,datatype:BYTES,shape:[n],bytes}`와 outputs descriptor를 담는다. payload는 필드 순서대로 원소별 little-endian uint32 길이+바이트 n개를 잇는다. 입력 값을 JSON 배열로 보내지 않고 결과는 `output` frame의 요청별 항목/출력 BYTES로 반환한다. profile/input/policy는 외부 경계에서 검증하고 실행 중 validated 자료+immutable request context를 공유한다.

## 시간과 수명

원래 receipt+sender monotonic elapsed를 유지하며 host간 UTC 차이로 나이를 확정하지 않는다. 필수 `settings.completionMarginMS`는 유한≥0이고 ChillServer가 요청별 `RequestContext.completionMarginMS`로 전달한다. 인증/업로드/decode/accept/dispatch/응답의 운영자 non-native 여유이지 측정값이 아니다. 미관측 overhead로 후보를 Pending 처리하지 않으며 실제 cohort `completionResidualNanos`는 분석용으로 보존한다.
- worker `requestSlack = RemainingSLOMS − completionMarginMS − serviceCost(batch,percentile)`. RemainingSLOMS는 frozen sender elapsed+local monotonic queue/service를 뺀 SLO다.
- 명시 수명은 원래 receipt 기준 `lifetimeMS − senderElapsedMS − completionMarginMS`다. sender elapsed는 한 번만 빼고 margin 자체를 줄이지 않는다.
- replay는 queued/running 예상 완료에 margin 한 번, returning에는 `max(0,completionMarginMS−응답 경과)`만 둔다. 실제 span/예상값 출처를 raw context에 연결한다.

| snapshot 시간 | 불변식 |
|---|---|
| `observationEnd` | Tick의 명시적 고정 historical cutoff. arrival/completion 구간 `[observationEnd−observationWindow,observationEnd)`을 현재로 바꾸거나 늘리지 않는다. zero→at fallback 없음. 필드 없는 과거 raw는 이력 reader로만 조회하고 현재 평가로 재해석하지 않는다. |
| `at` | blocking capture I/O 뒤 Runtime mutex 안에서 outstanding/정의/routing/credits와 같은 read로 동결한 decision origin. ChillServer는 이후 blocking I/O 없이 현재 option 호환성과 이미 읽은 완료 근거를 연결한다. |
| 요청 나이 | Elapsed/lifetime/native queue·service·return age를 at에 맞추되 ReceivedAt/불변 SLO/native sender elapsed/source ObservedAt은 덮지 않는다. native arrival history는 원래 age를 유지해 origin으로 환산한다. cutoff 이후 남은 요청은 outstanding에 포함하되 그 사이 완료를 과거 arrival에 넣었다고 주장하지 않는다(전역 atomic snapshot 아님). |
| physical observation | ObservedAt의 미상·미래 시각은 거절한다. 고정 age TTL은 없다. live capture 취소·refresh 실패·Ready/ACK·소유권 검사는 유지한다. SDK ObservedAt은 local receipt, remote UTC는 raw 시각이다. |
| capture source | projected history interval/arrival/completion reducer 뒤 authoritative observer refresh+uncached Node/Pod 읽기. Node ObservedAt은 Nodes.List 직전 local read 시작 하한, native local receipt는 그대로다. cutoff/최종 at로 재표시하지 않으며 읽기 실패는 Pending 근거로 남긴다. fixed-mode fallback은 이미 읽은 Node read-back/resolver를 쓰고 mode 문자열 때문에 Node별 전체 operation/Pod를 재조회하지 않는다. |

Serving 과부하 정책은 `Workload.spec.admission`의 `Reject`(기본)·`Delay`이며 applied Definition과 요청 context에 동결한다. `Reject`는 SLO 안에 끝낼 수 없는 요청을 실행 없이 `DeadlineInfeasible`로 거절한다. `Delay`는 normal 우선 후순위 FIFO로 보내 늦은 완료를 허용한다. gateway는 완료 가능한 실행기를 선택하고 executor는 batch 확정 직전에 최신 monotonic 시각으로 취소·최종 기한·남은 SLO를 재확인한다. 판단 입력 부재는 `AdmissionUnknown`으로 기다린다. fixed-batch는 기존 SLO 판단 예외다. gateway 정상·지연 큐 합은 Workload `maxQueuedRequests`(기본 1024)로 제한하며 native count/byte cap과 HTTP semaphore는 별도 물리 상한이다. 명시 timeout/cancel은 양쪽 queue에 적용하고 active batch는 SLO만으로 취소하지 않으며 실제 completion/runner termination 뒤 capacity를 반환한다. Runtime backendDone+transportDone drain 의미를 유지한다.

ModelSpec firstfloor `deadlinePolicy`(drop/keep/defer, 기본 keep)와 frozen profiling RunConfig는 측정 provenance로 유지하며 serving 정책 선택·일치 검사에는 쓰지 않는다. serving 정책은 요청별 전달이므로 변경에 worker/runner 재시작이 필요 없다. ConfigMap→`WatchConfig`→`Scheduler.Update`는 executor 배칭 설정을 같은 worker/runner에 적용한다. profile·initialized capacity 변경은 재시작을 요구하지만, initialized+measured 범위 안의 dynamic batch cap 증가는 재시작이나 warmup을 다시 거치지 않는다. 따라서 시작 warmup은 현재 cap/fixed K만이 아니라 profile의 전체 measured batch 집합을 포함한다. 이후 cap 증가도 이미 warmup한 집합 안에 머물며 planner/native/ACK의 기존 동작은 유지한다. normal/deferred는 count/byte cap을 공유한다. profiling fixed-K는 SLO로 버리지 않고 mode를 immutable 조건/actual metadata에 남긴다. actor/replay는 같은 [선택 함수](scheduler-implementation.md#33-같은-선택-함수의-실행과-replay)를 쓴다. 소유·동결·cutover 계약은 [ADR 0033](../../adr/0033-overload-admission.md)을 따른다.

Native inference context의 필수 `admissionPolicy`는 해당 요청의 applied Definition에서 동결한 `Reject` 또는 `Delay`다. actor의 측정 `deadlinePolicy`는 serving 배치 선택에 쓰지 않는다. batch 선택 뒤 확정 시각을 새 monotonic read로 갱신해 cancel·최종 기한을 제거하고 SLO 가능성을 다시 판단한다. 제거·거절·지연 전이 뒤에는 남은 큐에서 배치를 다시 고른다.

Executor admission 오류는 `code`·`stage=executor`·`policy`·`remainingSLONanos`를 보존한다. `DeadlineInfeasible`은 HTTP 503이며 correlated finished progress의 batch ID·size·service·backend가 모두 0인 미실행 증명을 요구한다. actor `QueueFull`은 HTTP 429와 같은 identity·미실행 증명을 반환하지만 큐에 들어가지 않았으므로 executor 대기는 없다. HTTP semaphore의 pre-decode `QueueFull`은 `notAdmitted=true`이며 policy·progress·SLO·대기를 만들지 않는다. gateway client는 이 exchange의 동결 dispatch policy로만 정규화한다.

`executorWaitNanos`는 native enqueue부터 running 또는 큐 안 종료까지의 monotonic span이다. receipt부터의 `queueMS`와 다르며 아직 enqueue하지 않은 요청에는 없다. native 최종 기한은 HTTP 408 `RequestTimeout`으로 전달하고, active batch의 timeout 응답은 `executionComplete=false`로 물리 점유를 유지한다. SLO admission 거절은 timeout으로 분류하지 않는다.

## 같은 firstfloor fixed-batch 경로

기존 ProfilingRun/frozen RunConfig/artifact preparation/observer coordinator와 `runtime/ortcpu/runner.py`의 input/warmup/window/output 반복을 재사용한다. profiling mode가 같은 IPC/runner에서 fixed K를 순차 실행하며 fake latency/energy는 필요 없다. Serving policy는 validated measured profile로 만든다.
raw repetition은 client `latencyMs`, runner service/backend call duration, actual K·item/execution 수와 같은 반복 sample 상관을 보존한다. warmup 제외·원래 metric/workload·cgroup peak memory·CPU/thread·actual image/provider/device를 유지한다. Serving queue는 runner service 분포, 완료 overhead는 해당 span을 쓴다.

| 실행/전달 | 계약 |
|---|---|
| measurement | `python3 /opt/chill/runtime/firstfloor/measurement.py --result-file /workspace/runtime-result.json`; stdin frozen request. child `/opt/chill/bin/serving-worker --config <temporary-worker.json> --bind-address 127.0.0.1:<port> --node-name <frozen-node>` |
| image | serving-worker, `runtime/firstfloor/runner.py`, 공통 `runtime/ortcpu/runner.py`, NumPy+선택 SDK. RKNN은 host/input query의 `runtime/rknnlite/runner.py`, CUDA는 nvidia-smi+실제 SDK/provider도 필요. |
| runner | `--runtime ort-cpu\|ort-cuda\|rknnlite --model-dir /model --preprocessor /preprocessor --warmup-iterations <frozen-count>`. ORT SessionOptions는 기본·자동 병렬성을 사용하며 CPU affinity/intra-op-thread 인자를 주입하지 않는다. CUDA는 `--gpu-uuid <frozen-physical-GPU-UUID>`, RKNN은 `--delivery-json <temporary-delivery.json>`을 추가한다. |
| device/resource | CUDA_VISIBLE_DEVICES는 frozen whole-GPU UUID1장. whole-GPU allocation/UUID, RKNN package/host/driver/device와 initialized batch의 실제 신원을 확인한다. CPU affinity/cpuset/cgroup/quota/thread는 실제 관측이며 profile-to-current 할당 matching이 아니다. kernel cgroup lifetime peak는 worker+runner runtime container 범위다. |
| batch domain | Capability initialization and measured-K warmup are separate domains in the common actor Config. Initialize the declared capabilities (otherwise1..max), but warm exactly all measured K for both serving and every profiling actor. A later cap increase stays inside that initialized/measured domain and does not introduce a second startup recipe. Startup warmup is separate from trace-paced active sampling; memory/startup peaks reflect this common sequence. |
| RKNN delivery | raw RunConfig delivery를 preparation에 그대로 사용. ConfigMap delivery.json→`--delivery-json /config/delivery.json`; Job과 같은 `profilingrun.ProjectRuntimeDelivery`로 library/driver/platform/device hostPath·privileged/supplemental group/allocation resource 투영. ORT thread 미사용이므로 측정/배치 threads=0. |

공개 profile 목록은 semantic execution condition별 최신 성공 기록을 유지한다. 실행 선택은 별도로 셀(model, class, path, powerMode)마다 최신 측정 하나이며 failed/partial도 후보다. 최신 실패를 과거 성공으로 대체하지 않는다. `derivation.CurrentMeasurements(candidates, replaced)`를 Serving과 Query가 같이 호출하고, 운영자의 `chill.dacs.io/replaces-batch-energy-optimization`은 recency 전에 적용한다. 새 override나 다중 버전 선택 구조는 없다.
For public profiles, `serving.SelectConditionProfiles` and Query variant/preflight share `profilederivation.PerformanceCondition`. It retains model/artifact/preprocessor/input-population recipes (including artifact/preprocessor images), Node/device/backend and memory/power conditions. Batch rows remain keyed by actual K. Runtime/package version strings, exact runtime/observer images, initialized/warmup domains, planned sweep length, meter/source identity, collection settings, settling and sample-stop/SLO policy remain provenance. Actor mode, dispatch policy, profile path and queue/timeout lifecycle overlays are excluded. These exclusions do not weaken actual inference, tensor/batch/device or physical sample checks. A `replaces` target establishes its semantic-condition recency cutoff before removal; a later unannotated success still wins. Same-namespace parsing, cross-condition operator replacement and non-blocking diagnostics remain; original objects/evidence are not modified.
Lineage exclusion (`serving.ExcludeReplacedConditionProfiles`) is not population deduplication: it removes only explicit targets and rows older than those targets under the same condition and tie-break. All other direct sources and confirmation rows remain intact. `applyProfileReuse` first separates these roles, then selects latest direct sources and validates confirmations; a newer rejected confirmation cannot evict a valid direct source.
CurvePlan v1alpha12 freezes the common actor startup body, runtime packages and active/idle stop provenance; RunConfig v1alpha20 and BEO result v1alpha5 are the same unreleased strict v3 cutover. Native ProfileBundle batches now preserve atomic integer-ns S/G samples and physical trials rather than an independently sorted S-only vector. Current readers reject old S-only bodies; no G0 synthesis, compatibility decoder or second stored-contract cutover is provided. Retained scientific archives remain evidence, not executable new profiles; operator-directed cutover/deletion follows ADR0019.
Catalog는 refresh마다 현재 Campaign native UID/resourceVersion/terminal 적격성을 읽고 Campaign당 검증 entry1개만 유지한다. 같은 immutable source는 raw 재다운로드/재도출하지 않고 caller mutable slice/map/pointer는 분리 반환한다. UID/RV 변경·pending·삭제는 entry를 버리고 필요하면 authoritative reader에서 다시 읽으며, 오류에 이전 success fallback은 없다. control tick/profile ticker/GET의 중복 raw reload만 제거하며 native refresh/시각/freshness는 바꾸지 않는다.
Cold reload의 CompletedTrials는 입력 순서의 최대 8개 window 안에서 Kubernetes Run GET과
execution/raw evidence 읽기를 병렬 처리한다. campaign 결과/plan 읽기는 직렬이다.
loader가 실제 byte cap·strict decode·raw 실행/관측·terminal 소유 관계를 검증한다.
raw RunConfig가 실행 권한이며 mutable spec/status, 중복 Config/provenance,
deterministic key 철자나 선언 Size를 두 번째 실행 계약으로 비교하지 않는다.
Run 소유·current-generation 완료·trial mode/batch·recorded Job/Pod binding은 유지한다.
window 뒤 입력 순서로 중복 검사·profile/facts/lineage를 도출하므로 첫 오류도 결정적이다.
실제 읽기/실행 계약 오류가 있으면 해당 profile을 publish/cache하지 않고 InvalidEvidence를 남긴다.
8은 campaign 수와 무관한 store 동시 GET 상한이다(execution→raw는 trial 안에서 직렬).
전체 campaign 병렬화 대신 작은 window로 shared gateway 연결과 임시 decode 메모리를 제한한다.
기본 maxEvidenceBytes=64MiB에서 window의 execution/raw payload 크기 합은 보수적으로
최대 2×8×64MiB이며 buffer 할당·decoded records·기존 catalog 메모리는 별도다.
profile 전체의 verified record는 유지하지만 추가 worker 결과는 window 하나만 보유한다.
상한을 높이려면 store 부하와 heap을 실측한다. 이는 live 성능 보장이 아니며 R18의 329초
전체가 8배 빨라진다는 뜻도 아니다(결과/plan, 작은 window, CPU 및 이후 초기화는 남는다).
profiling/serving은 [같은 공유 실행 계약](../../spec/00_measurement.md#측정의-단위)을 쓴다. CPU request/limit·quota·명시 affinity/pinning과 quota-threads 결합은 없다. ORT 기본·자동 thread 설정의 실제 readback(0 포함), affinity/cpuset/cgroup quota·사용량·throttling은 관측으로 보존한다. Node/backend/GPU UUID/initialized batch/raw와 메모리 안전 검사는 유지한다. 과거 CPU 통제 profile/κ는 공유 실행 근거로 재사용하지 않고 단일 저장 계약 cutover 뒤 새 조건으로 측정한다.

이미지 버전은 profile 유효 조건이나 digest gate가 아니다(사용자 결정, ADR 0019 예외). 공통 actor startup Config가 선택한 이미지 하나를 고정하며, `PrepareInstance`의 worker와 profile materializer는 그 `Config.RuntimeImage`를 그대로 사용한다. launch 시 현재 declaration을 재조회하지 않는다. 측정 Job과 serving Pod의 실제 runtime container는 같은 `firstfloor.RuntimeContainer(Config)`를 호출하여 image, CPU/memory/accelerator resources, GPU environment와 artifact/preprocessor mounts를 투영한다. lifecycle command/config/profile materializer와 dataset/observers만 caller overlay다. 메모리 request/limit은 CurvePlan 준비 전에 선택·고정한 실제 측정 envelope를 serving도 유지한다. observer는 현재 선언과 실제 이미지의 차이를 기존 관측/교체 진단으로 보존하며, 이 진단은 실행 중인 native 응답을 버리거나 자동 image-only 교체를 약속하지 않는다. exact image는 provenance이지 performance key가 아니며 새 image의 임의 성능 동등성을 보증하지 않는다.

Preparation resolves the current runtime declaration once into `Config.RuntimeImage` and derives the expected instance image from that Config; the historical measured image stays provenance. Native observation uses `Condition.Actor.Model`, and capture/drain/transition-cost consumers use the executor's mounted startup `Config.Model`, never a separate model alias. The shared memory request is a positive scheduling reservation no greater than its limit, not a lower bound on observed peak usage: request128Mi/limit1Gi with peak256Mi is legal for both Job and Pod. Observer peak safety checks use the actual limit and Node memory; preparation retains its queue-headroom bound within that same limit.

### D40 empirical cadence and conditional service

The common Runner measures **actual G** from the previous IPC exchange end to the next exchange start, and **S** around that exchange, using the same monotonic clock. Actor-running, dispatch/interarrival and return timestamps are different boundaries. Profiling adds no wait or target gap. A first exchange without an anchor has null G, not observed zero; physical TrialID remains attached to each sample.

Valid public BEO and standalone requests can make the first measurement without a profile or archived EvaluationRun/Stage/LoadScenario. The existing active mode spelling stays `empirical-cadence-fixed-batch`; no public timing field or cadence body is added. Workload, immutable plan, actual root admission and Node grant remain authoritative. No donor service, energy or idle gap is imported.

Each physical batch trial stops only after the configured elapsed floor **and** the SLO-derived count of known-gap service samples. At p95 this is 59; with a startup exchange, at least 60 exchanges are needed. Natural preparation/round-trip gaps count in active wall/energy, not inside S. The existing single F10 attempt deadline still covers producer, metric/endwatch, final read, handoffs and immutable commit. All configured K, warmup and three trials remain required.

Hydration builds immutable per-physical-trial gap/service indexes once. **1-nearest is algorithm D, not a tunable k**: distance is absolute integer ns `|g−G|`; select a physical trial uniformly, then uniformly among every actual record tied for nearest distance within that trial, including both neighboring gap values. Native quantiles use the identical uniform-trial × uniform-nearest-record mixture CDF, not flattened ties. No dispatch-time vector sorting/copying, service averaging/interpolation or distance threshold is introduced.

Replay's arrivals/queue/native finish already determine g; it never adds donor trace G again. Native-finish and reply-return anchors stay separate; running residuals condition on the gap at exchange start and actual exchange elapsed. Native batch choice occurs before packing/goroutine entry, so its decision-age gap (and projected fill-wait age) is explicitly approximate, not actual G; service completion resets that anchor consistently. Capacity search opens from optimistic empirical support, not a marginal quiet-load mean. Selected-neighbor distance/trial support/outside-support evidence is retained without inventing a new admission wall.

Actor `running` is declared before pack → IPC, not proof that a service
exchange has started. A pending exchange has no consumed native S; replay uses
the known last-exchange-end age as a lower-bound decision-gap approximation and
records that assumption. Only an absent previous anchor selects startup. Once
exchange elapsed is present, replay uses the observed prior G and true elapsed
service for the residual law.

Running residuals first condition the original nearest/trial mixture on `S > elapsed`, retaining posterior trial mass proportional to surviving/original nearest records. Only when every trial's nearest set has zero survivors, apply the same 1-nearest gap rule within each physical trial's `S > elapsed` records: exclude trials without survivors, choose a surviving trial uniformly, then a nearest survivor uniformly, keeping both neighboring gap values on a distance tie. If no record survives, draw a fresh service time from the original same-gap distribution as the remaining time: an explicit `same-gap-service-restart` substitute. Actual remaining time can be shorter or longer; SLO and energy ranking bias is bidirectional, with no conservative bound. Record `RunningResidualBeyondSupport` assumptions and conditional evidence (instance, batch size, elapsed, batch profile maximum, model). Missing original gap support still invalidates replay. Selected-neighbor distance/trial count/outside-support describe the survivor mixture on retry or the restart mixture on substitution. New-batch sampling and native quantiles remain unchanged.

The semantic key retains the actual recipe, gap boundary and conditional model, without a dead target LoadContext or donor provenance. The 59-sample p95 minimum is iid marginal quantile coverage, not precise conditional p95 or κ safety; three nearest trial records are not 59 local tail samples. No-added-wait measurement does not prove wide-gap coverage. Sparse and out-of-support observations remain visible limitations; no optional-gap/two-leg protocol, live-pair/CapacityTable or late-origin changes are included.

공통 `MeasurementSelection.ID`는 `namespace/name/UID`(direct profile ID, `/reuse-` 전)다. Adapter가 BEO `MeasurementComplete.LastTransitionTime`을 투영하고, `MeasurementTime(profileMeasuredAt, completedAt)`은 검증 성공 profile의 `MeasuredAt` → 완료 시각 → zero 순으로 선택한다. failed/partial은 profileMeasuredAt을 zero로 전달한다. `ReplacedMeasurements(sourceID→대상 namespace/name, namespace/name→ID)`가 동일 namespace annotation을 대상 ID 집합으로 바꾼다. Catalog read의 `currentMeasurements`와 Query cell의 `currentMeasurement`는 이 선택을 노출하며 profile 기록·측정값·저장 형식은 바꾸지 않는다.

CHILL은 판단·거절을 최소화하고 선택은 운영자에게 맡긴다. replacement의 spec 차이는 진단으로 표시하고 정상 검증 profile의 사용을 막지 않는다. 대상 부재·자기 참조는 대체 불성립으로 표시하고 무시한다. 동일 scope의 runtime declaration은 ConfigMap 문서 순서의 마지막 matching 항목 하나로 덮어쓰며 모호함 거절은 없다. `ModelCompatibility.Diagnostics`는 앞선 matching 선언 수를 표시하지만 Compatible verdict를 바꾸지 않는다. coverage의 usable/problem 표시는 실행 gate가 아니며 실제 Node/GPU/artifact·write-once·인증·실행 오류 검사는 유지한다. Operation-cost도 검증 profile이 있으면 catalog 진단만으로 거절하지 않는다.

## Strict fixed-size baseline serving

`SCH-35` 비교 실험 설정이며 기준은 `/home/genesis1/ETRI/donghwan/firstfloor` HANDOFF.md(2026-10-14)의 README.md:74-81·internal/serving/fixed_scheduler.go다.
수동 prepare `POST /api/v1/scheduler/operations`의 `fixedBatchSize=B`를 derived firstfloor policy/frozen Run/instance/metadata ACK에 기록한다. 0=dynamic, CLI flag 없음. B는 measured Policy.Batches·initialized BatchSizes·MaxBatchSize·queue cap 안이어야 하고 profiling mode는 거절한다. instance 수명 중 고정이며 B 제거 reload는 거절하고 snapshot 유지, 변경은 instance 교체다.
actor는 sameQueue `(WorkloadUID,Definition)` 중 B개 이상인 그룹을 stable EDF로 정확히 B개 선택한다. B 미만 dispatch 없음. MaxBatchWaitMS/SLO 여유/margin/energy gain/drop-defer는 선택에 쓰지 않고 overdue도 B에 포함·초과 기록한다. caller cancel/명시 lifetime은 queued 슬롯/byte 반환, count/byte 초과는 기존 오류다. shutdown tail은 실행하지 않고 unavailable 종료한다. progress BatchSize=B이며 profiling fixed-K `/profile`과 별개다.
자동제어/replay는 fixedBatchSize>0 instance를 후보로 쓰지 않으며 비교는 같은 부하의 실측 지연/에너지다. 구현 경계는 firstfloor types/config/policy/scheduler, kube operations/scheduler, native client metadata, 자동 후보 필터다. 수용 검사는 B−1 미실행/B+1 정확 B+tail, overdue 실행, cancel/lifetime 반환, shutdown tail 미실행, invalid B/B 제거 reload 거절·ACK 비교다(실행 결과 아님).

## baseline과 실제 원자 동작 측정

profile은 raw service 분포/incremental energy/input/measured memory와 estimator가 차감한 idle watts/원래 idle/raw를 유지한다. Node baseline은 mode·준비 model/profile/resource 집합·background 조건·node-exclusive meter identity에 연결하여 matching 실제 baseline을 우선한다. unloaded idle도 같은 sampler+상태 확인이다.
공존 실측 전에는 unloaded baseline+각 instance의 `(loaded baseline−unloaded baseline)`을 한 번씩 더하는 **명시적 additive 예상**을 사용한다(공유 instance1회). 실제 오차/가정/출처를 기록하며 모든 공존 조합 선행 campaign을 요구하지 않는다.
원자 측정은 prepare/remove/power, resize는 prepare→route→drain→remove 조합이다. measurement taint `chill.dacs.io/measurement:NoSchedule` Node를 scheduler에서 제외하고 command가 production primitive를 호출한다. 첫 successful power sample 뒤 시작하여 actual readiness/drain/removal/read-back 완료를 관측한다.
raw는 before/target/actual·시작/완료·실패/부분 상태·samples/failures·metric source/구간/설정/raw identity, powerwindow wall joules를 보존한다. 짧아 표본 부족이면 Insufficient다. prepare/remove는 before↔actual 차이가 State 정확히1개 추가/제거, power는 양쪽 State 비어 있고 mode가 다를 때만 Accepted이며 비용 조회도 같다. 나머지는 이유+Insufficient다.
과거 비용 reader는 보관된 meter/source와 실제 물리 Node를 사용한다. 현재 meter 선언과 다른 provenance, reference Size 차이 또는 저장 aggregate와 재계산 수치의 exact equality 때문에 과거 비용을 거절하지 않는다. 실제 bounded read, native 완료/격리·Node/방향·샘플 조건, 유한한 에너지와 적용 가능한 비용 도출은 유지한다. 이 변경은 과거 측정값·verdict를 다시 쓰거나 새 측정 승인을 부여하지 않는다.
remove/resize는 삭제 전 SDK native counter를 DrainNative에 남긴다. 사라진 actor는 actual drain+시작 counter 일치가 있어야 zero-inference wall cost다. 삭제/빈 queue만으로 추론 부재를 증명하지 않는다. resize cost는 기존 prepare/remove 측정 조합이다.
Node power의 독립 read-back/directed raw trial과 준비/제거/idle 별도 raw를 재사용한다. delta eta/whole wall joules 범위와 동일 Node/구간 중복을 구별하며 primitive 자체를 campaign/control loop 내부 lifecycle에 묶지 않는다.

## 자원 할당과 공존 경계

일반 자원 계약은 Node:serving Pod=1:N, Pod가 제공하는 모델 인스턴스:Workload=1:N을 허용한다. Pod당 모델 실행 인스턴스1개이며 같은 artifact/runtime/input 조건의 여러 Workload가 공유할 수 있다. 현재 계획기의 단일 UID/Node별 후보 제한은 [구현 설계](scheduler-implementation.md)의 별도 계약이다.
Pod당 persistent runner1개·순차 batch1개다. 실행 중 HTTP 취소도 runner 완료/actual container 종료까지 점유한다. instance 증감=Pod 추가/제거, 선택 자원=메모리와 GPU, 입력 묶음=batch policy다. CPU는 Pod 간 공유하며 병렬성은 runtime 기본·자동 정책이고 실제 적용값을 기록한다.
현재 구성은 actual label/resource/native readiness/연결 관측으로 산출하고 scheduler가 Pod/실측 resource/batch/mode 후보와 전환을 연결한다([전환 명세](../../spec/05_transition_control.md)). memory는 measured 사용+system/Serving 부가 예산의 가용성 제약이지 성능/비용 탐색 변수는 아니다.
공존 안전은 메모리 예산과 필요한 whole physical GPU의 exclusive UUID 할당으로 검사한다. CPU 전용 mask·request quota·reserved CPU 장부는 없으며 ModeCPUs/online CPU를 배치 관문으로 쓰지 않는다. time-slicing/MPS/별도 GPU allocator는 범위 밖이다.
Pod/Node 소유와 물리 반환 증명은 CPU 공유와 별개다. 작업 timeout/논리 종료/Pod 목록 부재만으로 자원을 반환하지 않는다.
Serving runtime과 artifact/preprocessor/profile init container는 CPU request/limit 없이 선택 memory request/limit을 유지한다. whole GPU resource는 runtime container에만 둔다. admission 후 CPU resource 재주입도 source 적격성에서 거절한다. 메모리를 유지하므로 Kubernetes QoS는 Burstable일 수 있으며 BestEffort QoS 자체가 목표가 아니다.
분리 allocation도 cache/memory bandwidth/power/thermal 간섭이 가능하다. resource-feasible 공존을 허용하고 actual latency/power를 관측하되 자원 적합성≠SLO 적합성이다. 무관측 무간섭 보장/모든 조합 예측기는 없으며 Node power 변경은 모든 공존 instance에 영향을 준다.

## Immutable worker bundle

worker init은 `/opt/chill/bin/serving-entrypoint --materialize-profile`, 기존 store flags와 `--profile-object-key/size/output`으로 bounded immutable bundle을 원자 제공한다. runtime image는 [firstfloor image recipe](../../../build/docker/firstfloor.Dockerfile)를 쓴다.

## Direct native-pair occupancy facts

`serving.OccupancyEstimator` reduces the immutable decoded request window shared
with scheduler load capture; selection and estimation do not change scheduler κ/d.
The writer updates active cached segments after complete appends; cold/restart
reads restore the same rows from the log. The accepted scheduler `profileByID`
map is the sole profile source. Each output stratum
retains Node name/UID, Workload UID, logical model, power mode, execution path,
runtime image, batch size, profile ID, immutable reference/evidence identities,
and the exact `Profile.MeanNanos(batch)` runner-exchange mean subtracted.
Backend-only or per-request loaded service is not a replacement baseline.

The reducer first joins assigned/final/late-native rows by RequestID. Conflicting
nonempty identities/timestamps and duplicate finals invalidate execution rather
than silently replacing evidence. Physical batches are keyed by instance and
BatchID; every declared member must be present, successful, execution-complete,
and have identical configuration/start/finish identities. Each member must have
direct worker queued/running/finished timestamps in order. Reconstructed legacy
stage offsets and gateway timestamps never enter this arithmetic.

For adjacent physical IDs on one unchanged instance, both starts must be in
the requested half-open worker window. Frozen native metadata must prove the
same profile/decision and fixed batch size, zero batch wait, and policy
application before the previous start. Missing historical metadata yields no
eligible pair, not a guessed policy. Require
`max(next.NativeQueuedAt) <= previous.NativeFinishedAt <= next.NativeRunningAt`.
Incomplete batches remain sequence markers; gaps, restarts, overlaps and
configuration changes cannot be bridged. The signed residual is exactly:

```
d_i = (next.NativeRunningAt - previous.NativeRunningAt
       - acceptedProfile.MeanNanos(previous.BatchSize)) / previous.BatchSize
```

Per-cell `statistics` reports signed mean, median, p25/p75, IQR, p10/p90 and
negativeFraction in ns/request. Quantiles sort the actual sample and select
`roundToEven((n-1)*p)`, matching the validation reducer's Python `round`; there
is no interpolation, approximate sketch or negative censoring. A fixed-batch
stratum's arithmetic mean is throughput-equivalent; different batch strata are
not silently pooled. Zero eligible pairs returns `statistics: null`, not zero.
Pair counts and complete/total physical batches and joined requests accompany
raw-row, direct-complete-raw-row, enriched-complete-raw-row, final-row and
direct-complete-final-row denominators. In-memory open snapshots can supplement
a request but are not additional raw rows/finals.

The scan captures only `[from-tail,to+tail]` file segments/archive hours, where
`tail = MaximumInferenceRequestTimeout + --drain-timeout` (the existing actual
inference lifetime cap plus configured physical-drain allowance). Its first
streaming pass selects only RequestIDs with an in-window receipt or direct native
start; its second visits just those IDs, preserving raw/final/enriched counters.
Read-side selection markers are not raw rows. The estimator also rejects
unselected out-of-window rows and filters joined requests before creating cells.
Known native starts determine execution-window membership; only requests with an
in-window receipt may contribute unknown-running completeness facts. Neither
whole later history nor unrelated RequestIDs are retained. Minute segments and
compressed archive hours remain the physical read granularity. Missing evidence
beyond the bounded tail is incomplete, never a guessed execution.
The bounded read-only API and historical-policy limitation are documented in
[observation API](observability-split.md). The diagnostic endpoint remains a
fresh read; scheduler admission uses one separate asynchronous rolling cache.
The public query and rolling scan share `aggregationWindowSeconds` (sample120s);
requests above that cap fail with400 before scanning. Long historical analysis
uses raw-export data and an offline reducer, not a long serving-process GET.
That cache pools actual pair samples across workload UIDs within each exact
Node UID/model/mode/execution-path/batch/profile cell, never medians-of-medians.
Configured minimumPairs qualifies measured median before legacy Accepted DOM,
then configured scalar default. Only operational d is clamped and
rounded; signed estimate facts remain unchanged. Tick reads the published cache
without scanning. One worker enforces scan timeout, selected-ID bound and
scan+reduction duty cooldown; failures publish DOM/default, not partial facts.

The short-burst DOM retains its separate `Observed`/`Insufficient` schema/prefix.
The legacy Accepted prefix/type and `CostEvidence` reader remain intact; raw
burst verdicts do not become Accepted DOM. Rolling-cache qualification is the
approved scheduler cutover, not deletion or reinterpretation of stored evidence.
κ inputs, decision records and catalog-read accepted projection expose the exact
selected tuple and `provenance{source,pairs,window,updatedAt,value}`; catalog-read
also exposes signed pooled `occupancyEstimates`. See [measurement API](measurement-api.md#4-계획기의-d-수집).

Initial, pre-review single-pass validation on the permitted 2026-11-03
09:00–10:00 export (793,637,858 bytes, 837,023 raw rows, 348,224 joined requests)
measured its streaming scan plus
RequestID join at **10.062 s**, and full reduction including exact sorting at
**11.319 s**, with `GOMAXPROCS=2`, `-p 1`, nice 19. It yielded 339,321 eligible
pairs and 100% joined-native/batch completeness. The 2,000-request throwaway
slice matched the independent `compute.py` summary (1,998 pairs, mean
188,577.192734531 ns/request, median 219,827.749791588 ns/request). No raw data
or throwaway comparison test is committed. Those timings describe the earlier
implementation, not the current bounded two-pass selection/read or a serving-path
latency promise. Current regressions prove that 10,000 unrelated rows plus later
invalid segments retain only the one selected RequestID, with all requested
denominators preserved. Those pre-cache measurements are not verification of
the subsequently added scheduler cache or its operational CPU/memory envelope.

### Bounded rolling scan: local binary resource and mutex experiment

The final local binary (`e503b4cfd123a98b2be5ca95629b7902e32b0a48dab2aafb15d837e693722297`)
was exercised with 48,000 timestamp-translated replay requests, four synthetic
append workers at 50 HTTPS requests/s, and built-in Diagnostics plus
`GODEBUG=gctrace=1`. The inactive comparison used a 3,600s refresh after its
initial scan; the active case used a 120s window, 20s refresh, 10s timeout and
250,000 selected-ID cap. These accelerated refresh settings are an experiment,
not the sample's 60s refresh/30s timeout defaults.

| Observed metric | Inactive, 40.01s | Rolling, 40.03s |
|---|---:|---:|
| Complete RequestClosed writer-wait samples | 1,999 | 2,002 |
| Wait p50 / p99 / max, ns | 115 / 14,379 / 151,956 | 76.5 / 6,137 / 192,497 |
| Diagnostic drops / overwrites | 32 / 0 | 28 / 0 |
| Process CPU, s | 10.87 | 13.62 |
| Phase sampled RSS maximum, KiB | 244,328 | 214,292 |
| Whole-process high-water RSS, KiB | 341,684 | 337,032 |

The two real rolling captures were 20.00018s apart. Capture mutex hold
p50/p99/max was 6,444/7,737/7,737ns; the capture span was
6,301.5/7,606/7,606ns. No retained file-open/stat event occurred under either
capture owner; file opens followed release. The old implementation performed
directory enumeration, opens and stats under the writer mutex. The new path
copies indexed paths, complete offsets and open-row metadata under that mutex,
then opens, reads, decodes and reduces outside it, bounded by the frozen offsets.

The rolling p99 and drop count improved in this finite sample, while maximum
wait increased. That maximum overlapped recorded RequestClosed/ResourcesRecord
writers, not capture; its remaining delay is unassigned. Nearby GC traces did
not establish GC causation. This is not a universal no-regression guarantee.

A separate 252,000-request, 558,274,559-byte fixture exceeded the production
250,000-ID cap twice without partial publication: 15.78 CPU seconds in 39.97s,
179,900KiB phase RSS maximum and 309,708KiB process high-water RSS. Rejection
occurs before estimator callbacks, so those numbers do not bound a successful
near-cap reduction.

A separate successful near-cap reduction published 248,000 complete requests
and batches from a 549,400,647-byte fixture, with 248,000–250,000 selected IDs
(extra unmatched append IDs are not exposed in the catalog). At window120s,
refresh40s, timeout20s and cap250,000 it used 31.99 CPU seconds in 59.994s;
phase RSS rose from 153,716 to a maximum 535,200KiB and whole-process high-water
RSS reached 559,264KiB. Its initial refresh exceeded20s during concurrent startup;
a later refresh succeeded, so20s was not uniformly sufficient. Wait p50/p99/max
was 56/9,766/293,124ns, with87 drops over60s and0 overwrites. The maximum overlapped
another RequestClosed writer, not capture; capture hold was22,426ns.
This heavier fixture's diagnostic drop rate increased relative to the primary
baseline, so its performance is not reported as equivalent to the48k case.
Pair-value-vector memory remains unmeasured without matching native policies.
Size the pod for measured workload and headroom, or lower the configured cap;
the sample250k limit is not a promise to fit a512MiB pod.
The chart's serving Pod actually defaults to4Gi (the neighboring512Mi belongs
to Dashboard). The retained250k cap estimates399.24MiB incremental state,9.75%
of that4Gi limit; for a512Mi serving override use75000 instead. See the
[cap sizing derivation and required-field contract](configuration-hardcoding.md#selected-request-cap-and-the-serving-pod-memory-budget).

A separate timeout experiment recorded six approximately1s scans with starts
2.0007–2.0024s apart. Timeout cancellation is cooperative; the single worker's
cooldown includes complete scan/reduction time. Request-count limits are not
hard byte/RSS ceilings, and the observations above are not universal maxima.

The fake policy inventory did not match native policy identities, so the
48,000 complete request/batch facts contained zero eligible pairs. This smoke
validates scanning, grouping/sorting, fallback and mutex/resource behavior;
measured-median selection is covered separately by exact-profile unit tests.
Only private observations and loopback services were used.

