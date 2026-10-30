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
| physical freshness | `0≤at−ObservedAt≤maxObservationAge`; 미래도 거절. HTTP GET snapshot age로 대체하지 않는다. SDK ObservedAt은 local receipt, remote UTC는 raw 시각이다. |
| capture source | projected history interval/arrival/completion reducer 뒤 authoritative observer refresh+uncached Node/Pod 읽기. Node ObservedAt은 Nodes.List 직전 local read 시작 하한, native local receipt는 그대로다. cutoff/최종 at로 재표시하지 않으며 느린 HTTP 시간도 age로 남아 Pending 가능하다. fixed-mode fallback은 이미 읽은 Node read-back/resolver를 쓰고 mode 문자열 때문에 Node별 전체 operation/Pod를 재조회하지 않는다. |

`deadlinePolicy`는 drop/keep/defer, 기본 keep이다. drop은 active 실행 중에도 expired queued를 제거하고 idle infeasible head를408로 종료한다. keep은 late 반환, defer는 normal 우선 후순위 FIFO다. 명시 timeout/cancel은 양쪽 queue, active batch는 SLO만으로 취소하지 않으며 실제 completion/runner termination 뒤 capacity를 반환한다. Runtime backendDone+transportDone drain 의미를 유지한다.
policy 변경은 worker 재시작/instance 교체다. normal/deferred는 count/byte cap을 공유한다. profiling fixed-K는 SLO로 버리지 않고 mode를 immutable 조건/actual metadata에 남긴다. actor/replay는 같은 [선택 함수](scheduler-implementation.md#33-같은-선택-함수의-실행과-replay)를 쓴다.

## 같은 firstfloor fixed-batch 경로

기존 ProfilingRun/frozen RunConfig/artifact preparation/observer coordinator와 `runtime/ortcpu/runner.py`의 input/warmup/window/output 반복을 재사용한다. profiling mode가 같은 IPC/runner에서 fixed K를 순차 실행하며 fake latency/energy는 필요 없다. Serving policy는 validated measured profile로 만든다.
raw repetition은 client `latencyMs`, runner service/backend call duration, actual K·item/execution 수와 같은 반복 sample 상관을 보존한다. warmup 제외·원래 metric/workload·cgroup peak memory·CPU/thread·actual image/provider/device를 유지한다. Serving queue는 runner service 분포, 완료 overhead는 해당 span을 쓴다.

| 실행/전달 | 계약 |
|---|---|
| measurement | `python3 /opt/chill/runtime/firstfloor/measurement.py --result-file /workspace/runtime-result.json`; stdin frozen request. child `/opt/chill/bin/serving-worker --config <temporary-worker.json> --bind-address 127.0.0.1:<port> --node-name <frozen-node>` |
| image | serving-worker, `runtime/firstfloor/runner.py`, 공통 `runtime/ortcpu/runner.py`, NumPy+선택 SDK. RKNN은 host/input query의 `runtime/rknnlite/runner.py`, CUDA는 nvidia-smi+실제 SDK/provider도 필요. |
| runner | `--runtime ort-cpu\|ort-cuda\|rknnlite --model /model/model.onnx\|model.rknn --intra-op-threads <frozen-count> --warmup-iterations <frozen-count>`. CUDA 추가 `--gpu-uuid <frozen-physical-GPU-UUID>`, RKNN `--delivery-json <temporary-delivery.json>` |
| device/resource | CUDA_VISIBLE_DEVICES는 frozen whole-GPU UUID1장. whole-GPU allocation/UUID, RKNN package/host/driver/device, CPU quota/affinity/thread, 모든 initialized batch actual metadata 불일치 시 거절. kernel cgroup lifetime peak는 worker+runner runtime container 범위. |
| batch domain | Serving 초기화/native 최대는 validated runtime metadata, dispatch/policy cap은 measured domain. initialized `[1,2]`, measured `[1]`이면 초기화는 유지하고 선택은 `[1]`만. |
| RKNN delivery | raw RunConfig delivery를 preparation에 그대로 사용. ConfigMap delivery.json→`--delivery-json /config/delivery.json`; Job과 같은 `profilingrun.ProjectRuntimeDelivery`로 library/driver/platform/device hostPath·privileged/supplemental group/allocation resource 투영. ORT thread 미사용이므로 측정/배치 threads=0. |

latest successful profile은 frozen node/model/artifact/runtime/input/resources/power/protocol 조건별 최신 성공이다. source SLO와 무관하게 matching execution profile을 재사용하고 독립 option 오류/누락 이유를 남긴다. ORT/RKNN reference는 고유 owner/실행 조건으로 firstfloor evidence와 구별한다.
strict stored schema 변경은 caller/fixture/generated schema를 작업 단위로 함께 갱신한다. 구 testbed 정리는 ADR0019 절차, 진행 campaign stored 변경은 해당 campaign 정리 후다.
Catalog는 refresh마다 현재 Campaign native UID/resourceVersion/terminal 적격성을 읽고 Campaign당 검증 entry1개만 유지한다. 같은 immutable source는 raw 재다운로드/재도출하지 않고 caller mutable slice/map/pointer는 분리 반환한다. UID/RV 변경·pending·삭제는 entry를 버리고 필요하면 전체 evidence 재검증, 오류에 이전 success fallback 없음. control tick/profile ticker/GET의 중복 raw reload만 제거하며 native refresh/시각/freshness는 바꾸지 않는다.
profiling/serving은 [같은 실행 계약](../../spec/00_measurement.md#측정의-단위)을 쓴다. Node/backend/affinity/GPU UUID/threads/initialized batch/raw를 유지하고 준비·측정 적격성·SLO 평가는 독립이다. CPU `cpuAffinity`/campaign `cpuQuota`는 선택 physical mask+threads의 실제 runner 입력이며 frozen affinityCPUs·허용 inventory·actual affinity를 raw에 연결한다. quota와 mask는 다르고 quota-only reference는 별도 조건이다.

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
remove/resize는 삭제 전 SDK native counter를 DrainNative에 남긴다. 사라진 actor는 actual drain+시작 counter 일치가 있어야 zero-inference wall cost다. 삭제/빈 queue만으로 추론 부재를 증명하지 않는다. resize cost는 기존 prepare/remove 측정 조합이다.
Node power의 독립 read-back/directed raw trial과 준비/제거/idle 별도 raw를 재사용한다. delta eta/whole wall joules 범위와 동일 Node/구간 중복을 구별하며 primitive 자체를 campaign/control loop 내부 lifecycle에 묶지 않는다.

## 자원 할당과 공존 경계

일반 자원 계약은 Node:serving Pod=1:N, Pod가 제공하는 모델 인스턴스:Workload=1:N을 허용한다. Pod당 모델 실행 인스턴스1개이며 같은 artifact/runtime/input 조건의 여러 Workload가 공유할 수 있다. 현재 계획기의 단일 UID/Node별 후보 제한은 [구현 설계](scheduler-implementation.md)의 별도 계약이다.
Pod당 persistent runner1개·순차 batch1개다. 실행 중 HTTP 취소도 runner 완료/actual container 종료까지 점유한다. instance 증감=Pod 추가/제거, instance 자원=Pod allocation+backend 설정, 입력 묶음=batch policy다. 자원 할당/프로파일 실행 구성/serving instance 단위를 맞추고 quota/threads/actual affinity를 각각 확인한다. batch 내부 여러 CPU, Pod간 batch 병렬은 가능하다.
현재 구성은 actual label/resource/native readiness/연결 관측으로 산출하고 scheduler가 Pod/실측 resource/batch/mode 후보와 전환을 연결한다([전환 명세](../../spec/05_transition_control.md)). memory는 measured 사용+system/Serving 부가 예산의 가용성 제약이지 성능/비용 탐색 변수는 아니다.
공존은 순간 사용률이 아닌 **배정 가능한 자원** 기준이다. CPU Pod는 분리 mask, GPU Pod는 서로 다른 whole physical GPU이며 보조 CPU/memory도 센다. Kubernetes 기존 allocation을 사용하고 time-slicing/MPS/별도 allocator는 범위 밖이다.
CPU request/limit만으로 mask 분리가 안 된다. 전용 배정은 CPU Manager static/Guaranteed/정수 CPU 등 플랫폼 조건이 필요하고 profiling 관측 container를 포함한 Pod도 맞춰야 한다. 실제 지원/배정은 배포에서 확인하며 이 문서가 현 클러스터 격리를 입증하지 않는다.
Serving artifact/profile init도 선택 profile CPU request/limit+선택 memory를 쓰고 GPU 요청은 runtime container에만 둔다. 모든 container의 양수 CPU/memory request=limit이면 Guaranteed지만 분수 CPU도 가능하므로 QoS는 exclusive proof가 아니다. CPU 수량을 반올림/request-limit 강제 일치시키지 않는다.
분리 allocation도 cache/memory bandwidth/power/thermal 간섭이 가능하다. resource-feasible 공존을 허용하고 actual latency/power를 관측하되 자원 적합성≠SLO 적합성이다. 무관측 무간섭 보장/모든 조합 예측기는 없으며 Node power 변경은 모든 공존 instance에 영향을 준다.

## Immutable worker bundle

worker init은 `/opt/chill/bin/serving-entrypoint --materialize-profile`, 기존 store flags와 `--profile-object-key/size/output`으로 bounded immutable bundle을 원자 제공한다. runtime image는 [firstfloor image recipe](../../../build/docker/firstfloor.Dockerfile)를 쓴다.
