# Scheduler 실행기와 측정 계약

현재 실행·시간·측정 세부 계약의 원본이다. 판단·전환 알고리즘은
[스케줄러 구현 설계](scheduler-implementation.md), 사용자 API·등록·드레인은
[Workload 관리](workload-management.md), 실행 결과는
[실행 이력](scheduler-execution-history.md)에서 구분한다.

## 요청과 실제 모델

외부 Workload API는 입력 1개를 Preprocessor 원시 입력 필드(`BYTES`, base64 `data`)로 받고
개별 응답 계약을 유지한다. native `/v2/models/{model}/infer`는 필드마다 원소 1개를 보내고
firstfloor가 batch 축을 붙인다. metadata에는 request ID, UID, 불변 definition identity, 원래 receipt,
개별 percentile/target, 전달 시점 경과 시간, 명시적 수명과 profile 근거를 보낸다.

입력 경계는 필드 이름 집합, `BYTES` datatype, 1 이상 `maxBytes` 이하의 길이를 검사한다.
`JPEG`는 시작 바이트와 `image.DecodeConfig` 헤더를, `UTF8`은 `utf8.Valid`를 확인한다.
화소 복호와 모델 입력 생성은 runner 안의 전처리 module이 수행한다. 계약 전체는
[이기종 워크로드 4.1절](heterogeneous-workloads.md#41-원시-표본-요청)이 원본이다.

firstfloor는 model/input/runtime 조건이 고정된 한 runner를 유지한다. 같은 UID의
호환 입력을 배치하며 Workload별 SLO와 결과는 개별 request에 남긴다. ORT CPU,
실제 CUDA provider, RKNNLite는 실제 SDK capability를 검사한다. CUDA는 확인된
물리 GPU 한 장을 사용하고 CPU fallback 조건을 실제 오류로 처리한다. SDK가
지원하는 tensor·batch·layout 범위를 확인하여 범위 밖 입력은 오류로 반환한다.
개발 echo mode는 explicit synthetic 실행으로 분리한다.
CUDA session의 provider 목록은 정확히 CUDA EP 다음 default CPU EP다. ORT는 shape
부분 그래프를 항상 CPU EP에 두며, 측정 profile은 실제 provider 목록을 기록한다.
CPU-only graph 실행은 계속 거부한다.

IPC는 기존 bounded length-prefix header + binary payload를 재사용한다. 원시 필드별
`{name, datatype: BYTES, shape: [n], bytes}` descriptor와 outputs를 header에 담고, payload는
원소마다 little-endian uint32 길이와 바이트로 framing한 n개 원소를 필드 순서대로 잇는다.
profile·입력·policy는 해당 외부 경계에서 검증하여
실행 중에는 검증된 자료와 immutable request context를 공유한다.
frame 길이는 big-endian uint32, 작은 header는 JSON이며 입력 값을 IPC의 JSON 배열로
전달하지 않는다. 결과는 `output` frame의 요청별 항목 결과와 출력 필드 `BYTES`로 돌아온다.

## 시간과 수명

원래 receipt와 sender의 monotonic elapsed를 보존한다. 서로 다른 호스트의 UTC를
그대로 빼서 요청 나이를 확정하지 않는다. Serving의 완료 오버헤드는
`(exchange finish − receipt − native elapsed) + (response finish − exchange finish)`의 관측 최댓값이다.
단방향 전달 시간이나 미래 지연의 보장 상한이 아니며 완료 시점에 한 번만 반영한다.
최근 관측은 같은 노드·프로파일·모델/입력 실행 조건의 호환 정의에 재사용하며,
측정 인스턴스의 제거나 SLO가 다른 Workload UID만으로 근거를 버리지 않는다.
해당 조건의 관측이 없으면 후보 평가는 Pending이고 실제 Serving 측정이 선행해야 한다.
선택한 preparation의 `completionCalibration`은 실제 측정 후 닫혀 발행된 관측 기록
raw window의 고정 `from/to`와 metadata/definitions/requests object identity를 명시한다.
ChillServer가 기존 evidence bound로 immutable bytes를 한 번 읽어 complete window, frozen
definition/assignment와 성공한 native exchange/response span을 검증하고 같은 reducer로
관측 최댓값을 만든다. 이 측정 사실의 수명은 최근 arrival 60s 구간과 별개이며 arrival
구간을 늘리지 않는다. 현재 live 관측과 선택한 calibration 중 호환 조건의 더 큰 값을
사용한다. selector를 제거하면 다음 config refresh에서 calibration은 사라진다.
`completionMeasurement`는 고정 구간·성공 request 수·raw `references`·`source`를 보존하고
instance/option/candidate에 연결한다. 설정의 `completionObserved=true`만으로는 근거가
되지 않으며 누락·미완료·다른 profile/Node/runtime/CPU/GPU 조건은 Pending이다.
consumer는 raw 구조를 다시 검증하지 않고 typed fact의 관측값과 사용 scalar가 같으며
Node/profile/model/input/runtime/CPU/GPU 실행 조건이 여전히 matching인지 연결한다.
proof를 가진 instance라도 이 값이나 조건만 변경하면 Pending으로 돌아간다.
unbound 완료 근거는 자원/전환 불가 사유도 함께 보존하되 후보 상태를 Pending으로 둔다.
이미 관측한 action 실패의 ExecutionError 우선순위는 유지한다.
native policy의 `envelopeSource`는 기존 256-byte 제한 안에서 관측량·고정 구간·trial 수와
Serving `completionMeasurement` 출처 연결만 전달한다. preparation, route 적용과 replay가
같은 연결 표현을 쓰며 snapshot/관측 기록의 전체 `Source`와 raw references를 자르거나
바꾸지 않는다. 긴 provenance 때문에 SDK가 policy를 거절하는 문제를 상한 확대로 숨기지 않는다.
request window마다 definition을 다시 쓰지 않으므로 과거 definition window도 같은
selector의 `windows`에 포함할 수 있다. 모든 선택 stream의 frozen identity를 합친 뒤
`receivedAt ∈ [from,to)`의 성공 trial만 줄이며 `trialIDs`로 원래 request를 찾는다.
같은 raw cohort를 여러 preparation이 선택하면 ChillServer는 그 refresh 안에서 한 번만 읽고
조건별 typed fact를 연결한다. persistent cache나 현재 arrival 구간 변경은 없다.
도착 시각 예측은 ChillServer 분배 시점이라는 가정으로 명시하며 측정값으로 포장하지 않는다.
인증·업로드·decode·accept·dispatch를 포함한 모든 non-native receipt→response 시간을 포함한다.
queued/running의 예상 완료 및 worker의 SLO slack/수명 예약에는
`max(0, observed joint overhead − actual frozen sender elapsed)`만 남겨 둔다.
이미 소모한 dispatch 이전 시간은 원래 receipt 경과와 sender context에 포함되어 있으므로
다시 더하지 않는다. running에도 실제 sender witness가 없으면 평가를 Pending으로 둔다.
returning 요청은 receipt→exchange의 non-native 잔여 span과 실제 exchange finish 이후
응답 시간을 각각 한 번 빼고 남은 시간만 더한다. SDK finished callback은 exchange finish가
아니므로 exchange milestone 전에는 returning으로 전환하지 않으며 native finished 확인은 보존한다.
필요한 실제 milestone이 없거나 잘못됐으면 Pending이다.
worker의 local monotonic queue/service 경과 시간과 원래 receipt 이후 경과 시간을
사용해 추가 대기 여유를 판단하며 실제 span과 예상값의 출처를 raw context에 연결한다.

Scheduler snapshot의 `observationEnd`는 Tick에 전달한 고정 historical cutoff다.
arrival·completion 관측 구간은 `[observationEnd−observationWindow, observationEnd)`이며
현재 시각으로 끝을 바꾸거나 구간을 늘리지 않는다. `at`는 blocking capture I/O가 끝난 뒤
기존 Runtime mutex 안에서 현재 outstanding·Workload 정의·routing/credits와 함께 읽은 decision origin이다.
`SchedulerInputs`는 이 in-process 필드만 같은 read로 동결하며, ChillServer는 그 뒤 blocking I/O 없이
현재 정의로 option 호환성과 이미 읽은 완료 근거를 연결한다. 요청의
Elapsed·remaining lifetime·native queue/service/return age는 이 같은 시각에 맞추고 원래
ReceivedAt·불변 SLO·native sender elapsed·실제 source ObservedAt은 덮어쓰지 않는다.
native arrival history는 원래 source의 age를 유지한 채 replay origin으로 환산한다.
physical source freshness는 `0 ≤ at−ObservedAt ≤ maxObservationAge`이며 미래 source도
거절한다. HTTP GET 시점의 snapshot age를 이 gate로 대체하지 않는다.
cutoff 이후 접수되어 아직 남은 요청은 현재 outstanding에 포함되지만 그 사이 완료된 요청까지
과거 arrival 구간에 포함했다고 주장하지 않는다. 이는 전역 atomic snapshot 계약이 아니다.
현재 평가 입력에는 명시 `observationEnd`가 필수이며 zero→at fallback은 없다.
필드가 없는 과거 raw 기록은 기존 이력 reader로 보존·조회하되 현재 평가 입력으로 재해석하지 않는다.

projected history interval·arrival·completion reducer를 먼저 끝낸 뒤 같은 capture 경로에서
Observer의 authoritative native/instance refresh와 uncached Node·Pod 읽기를 수행한다.
Node `ObservedAt`는 실제 `Nodes.List` 요청 직전의 local 시각(보수적 read 시작 하한)이다.
Observer가 native probe 응답에서 기록한 local receipt 시각은 그대로 보존한다.
어느 source도 historical cutoff나 최종 `at`로 재표시하지 않으며, live HTTP 자체가 느리면
그 시간은 source age에 남아 기존 freshness gate에서 Pending이 된다.
고정 전력모드 fallback은 이미 읽은 Node·NodeActuation과 기존 fixed-mode resolver를 재사용한다.
모드 문자열 하나를 얻기 위해 노드마다 전체 operation snapshot·Pod 목록을 다시 읽지 않는다.

SLO 위반은 모델의 `deadlinePolicy`(drop/keep/defer, 기본 keep)에 따른다. drop은
expired queued 요청을 active 실행 중에도 제거하고 idle 시 infeasible head를 408로
종료한다. keep은 late 결과를 반환하고 defer는 normal 우선의 후순위 FIFO에서 처리한다.
명시적 timeout/cancel은 두 대기열 모두에 적용한다. active batch는 SLO만으로 취소하지
않으며 실제 completion 또는 runner termination 확인 후 capacity를 해제한다.
기존 Runtime의 backendDone + transportDone drain 의미를 유지한다.

`deadlinePolicy` 변경은 worker 재시작/인스턴스 교체로 적용한다. normal/deferred는
같은 요청 수·byte 한도를 공유한다. 고정-K profiling은 SLO로 요청을 버리지 않으며
선택한 mode를 불변 측정 조건과 실제 metadata에 기록한다. actor와 replay는 같은
[선택 함수](scheduler-implementation.md#33-같은-선택-함수의-실행과-replay)를 사용한다.
SDK `ObservedAt`은 local receipt이고 remote UTC는 원시 관측 시각으로 보존한다.

## 같은 firstfloor fixed-batch 경로

기존 ProfilingRun 생성·frozen RunConfig·artifact preparation·observer coordinator와
`runtime/ortcpu/runner.py`의 input/warmup/window/output validation 반복을 재사용한다.
firstfloor session은 **profiling mode**에서 같은 IPC/runner에 선택된 fixed K를
순차 실행한다. 이 mode는 아직 측정 전인 가짜 latency/energy 값을 요구하지 않는다.
Serving은 검증된 measured profile로 policy를 구성한다.

raw repetition은 기존 client call `latencyMs`와 별도로 runner service duration,
backend call duration, 실제 K·item/execution 수를 유지한다. 같은 반복의 sample을
상관시키며, warmup 제외·원래 metric/workload 관측·cgroup peak memory·CPU/thread·
실제 image/provider/device 조건을 보존한다. Serving queue 점유 계산에는 runner
service 분포를 사용하고, 완료 오버헤드에는 해당 관측 span을 사용한다.

실제 측정 entrypoint는 `python3 /opt/chill/runtime/firstfloor/measurement.py
--result-file /workspace/runtime-result.json`이며 stdin으로 frozen request를 받는다.
이 entrypoint는 `/opt/chill/bin/serving-worker --config <temporary-worker.json>
--bind-address 127.0.0.1:<port> --node-name <frozen-node>`를 실행한다.
이미지에는 serving-worker, 같은 `runtime/firstfloor/runner.py`, 공통
`runtime/ortcpu/runner.py`, NumPy와 선택 backend의 실제 SDK가 함께 있어야 한다.
RKNN 이미지는 host/input query를 재사용하는 `runtime/rknnlite/runner.py`도 포함한다.
runner는 `--runtime ort-cpu|ort-cuda|rknnlite --model /model/model.onnx|model.rknn
--intra-op-threads <frozen-count> --warmup-iterations <frozen-count>`로 실행되며,
CUDA에는 `--gpu-uuid <frozen-physical-GPU-UUID>`, RKNN에는
`--delivery-json <temporary-delivery.json>`을 추가한다. CUDA 이미지는 GPU driver를
조회하는 `nvidia-smi`와 실제 CUDA SDK/provider가 필요하며, child process의
`CUDA_VISIBLE_DEVICES`는 frozen whole-GPU UUID 한 장으로 고정한다. whole-GPU
allocation·UUID, RKNN package/host/driver/device 선언, CPU quota/affinity/thread와
모든 initialized batch의 실제 metadata가 일치하지 않으면 결과를 거부한다.
Serving의 초기화 batch domain과 native 최대 용량은 검증된 runtime metadata에
맞춘다. 초기화가 `[1,2]`이고 측정 곡선이 `[1]`인 경우에도 초기화 조건은
그대로 유지하며, dispatch와 policy cap은 측정한 `[1]` 안에서만 선택한다.
kernel cgroup lifetime peak는 worker와 runner를 포함한 runtime container 범위다.
Serving preparation은 measured profile의 raw evidence에 담긴 RunConfig delivery를 그대로 사용한다.
RKNNLite Pod는 그 delivery를 preparation ConfigMap의 `delivery.json`으로 받아 runner에
`--delivery-json /config/delivery.json`을 전달한다. profiling Job과 같은 projection
(`profilingrun.ProjectRuntimeDelivery`)으로 host library, driver, platform, device hostPath와
privileged, supplemental group, allocation resource를 받는다. RKNNLite는 ONNX Runtime thread를 쓰지
않으므로 thread 수 0으로 측정되고 같은 값으로 배치된다.

latest successful profile은 같은 frozen node/model/artifact/runtime/input/resources/
power/protocol 조건별로 선택한다. 명시적 source 선택 범위 안의 최신 성공을
사용하고 독립 후보의 오류·누락 이유를 보존한다. source의 SLO와 무관하게 matching
execution profile을 재사용한다. 기존 ORT/RKNN reference 측정은 고유 실험 owner를
유지하며 firstfloor serving evidence와 실행 조건으로 구별한다.

strict stored schema는 작업 단위에서 한 번 변경하고 caller·fixture·generated
schema를 함께 갱신한다. 이전 testbed 객체의 정리는 ADR 0019의 운영 절차를 따른다.
현재 진행 중 campaign의 stored 계약 변경은 해당 campaign 정리 후 적용한다.

Catalog는 매 refresh에서 현재 Campaign의 native UID/resourceVersion·terminal 적격성을
읽고 선택된 이름마다 현재 검증 entry 하나만 유지한다. 같은 source의 immutable profile은
재다운로드·재도출하지 않으며 caller가 바꿀 수 있는 slice/map/pointer는 분리해서 반환한다.
UID/RV 변경·pending·삭제·선택 해제는 기존 entry를 버리고 필요한 경우 원래 전체 evidence
검증을 다시 수행한다. 새 검증 오류에서 이전 success로 돌아가지 않는다. 이 lifecycle 재사용은
control tick/profile ticker/profile GET의 반복 raw reload를 제거하지만 native 관측은 계속
실제로 갱신하며 snapshot 시각이나 freshness 기준을 바꾸지 않는다.

profiling과 serving은 같은 [실행 계약](../../spec/00_measurement.md#측정의-단위)을 사용한다.
같은 실행 조건은 SLO가 다른 Workload에도 재사용하며, actual Node·backend·affinity·
GPU UUID·threads·batch 초기화 조건과 raw 근거를 유지한다. 현재 준비·측정 적격성·
SLO 평가는 각각의 판단이다.

CPU 측정의 `cpuAffinity`와 campaign의 `cpuQuota`는 선택한 physical mask와 threads를
같은 firstfloor runner에서 실측하는 입력이다. frozen `affinityCPUs`와 실제 허용
inventory·적용 affinity를 raw에 연결하며 quota와 mask의 의미를 각각 유지한다.
기존 quota-only reference 측정은 별도 조건으로 유지한다.

## Strict fixed-size baseline serving

`SCH-35`의 비교 기준 실험 설정이다. 기준 동작은 상류 snapshot
`/home/genesis1/ETRI/donghwan/firstfloor`(HANDOFF.md 기준일 2026-10-14)의
`README.md:74-81`과 `internal/serving/fixed_scheduler.go`다.

운영자는 `serving-operation --execute-config` prepare manifest의 `fixedBatchSize`로 B를 설정하며
이 값은 derived preparation의 firstfloor policy에 들어간다. 0은 dynamic이다.
B는 frozen Run/instance 설정과 worker metadata ack에 기록하며 CLI flag는 두지 않는다.
worker 시작 시 B는 측정된 `Policy.Batches`, 초기화된 `BatchSizes`, `MaxBatchSize`,
큐 용량 안에 있어야 하고 profiling mode에서는 거절한다. B는 instance 수명 동안 고정이며,
B의 profile을 제거하는 reload는 거절하고 현재 snapshot을 유지한다. B 변경은 instance 교체다.

actor의 선택 함수는 `sameQueue`의 `(WorkloadUID, Definition)` 그룹 중 B개 이상 대기한
그룹에서 기존 stable EDF 순서로 정확히 B개를 고른다. B개 미만이면 dispatch하지 않는다.
`MaxBatchWaitMS` 타이머, SLO 여유, completion margin, energy gain, `deadlinePolicy`의
drop/defer는 선택에 쓰지 않는다. SLO가 지난 요청도 B에 포함해 실행하고 초과로 기록한다.
caller cancel과 명시적 lifetime은 대기 요청을 제거해 슬롯과 byte를 돌려주고, 큐 count/byte
cap은 기존 오류로 거절한다. 종료 시 남은 tail은 실행하지 않고 대기 요청을 unavailable로
끝낸다. progress의 `BatchSize`는 B이며 profiling fixed-K `/profile` 경로와는 별개다.

자동제어와 replay는 `fixedBatchSize>0` instance를 후보로 쓰지 않으므로 replay는 이 선택을
재현하지 않는다. 비교는 같은 부하에서 관측 기록의 실측 지연·에너지로 한다.

구현 단위의 파일은 `internal/firstfloor/types.go`(Policy와 metadata 필드),
`config.go`(시작 검증), `policy.go`(B 선택 분기), `scheduler.go`(SLO 경로 분리, reload 거절),
`internal/serving/kube/operations.go`, `scheduler.go`(전달과 ack 비교, 변경 시 교체),
`internal/serving/firstfloor/client.go`(metadata 검증), 자동제어 후보 필터다.
검사는 `internal/firstfloor/worker_test.go`의 B-1 미실행, B+1 정확히 B 실행과 tail 대기,
SLO 경과 후 실행, cancel/lifetime 용량 회수, shutdown tail 미실행, 잘못된 B와 B 제거 reload
거절, 그리고 `internal/serving/kube` ack 비교 테스트다.

## baseline과 실제 원자 동작 측정

profile은 raw service 분포, incremental energy/input, measured memory뿐 아니라
그 incremental estimator가 차감한 idle watts와 원래 idle/raw evidence를 보존한다.
node의 측정 baseline은 power mode·준비된 model/profile/resource 집합·background 관측
조건·node-exclusive meter identity에 연결한다. matching 실제 상태의 baseline을
우선 사용한다. unloaded idle은 같은 wall-power sampler와 실제 상태 확인으로 얻는다.
공존 상태의 측정값이 확보되기 전에는 unloaded node baseline에 각 instance의
`loaded baseline - unloaded baseline`을 한 번씩 더하는 명시적 additive 예상값을
사용한다. 공유 instance는 한 번 더하고, 실제 공존 측정과의 오차·가정·출처를 기록한다.
이는 전체 공존 조합의 선행 campaign을 요구하는 조건과 구별된다.

Serving 원자 동작은 `prepare`, `remove`, `resize`(prepare→route→drain→remove),
`pool membership`, `power mode`다. production 실행 경계의 동일 함수를 측정
measurement command가 호출한다. measurement service는 first successful power sample
후 원자 동작을 시작하고 실제 readiness/drain/removal/read-back 완료를 관측한다.
Raw record에 before/target/actual state, 시작/완료, 실패·부분 상태, samples/failures,
metric source·구간·설정·raw identity를 보존한다. powerwindow로 wall joules를 적분하며
짧은 동작의 충분하지 않은 샘플은 `Insufficient`로 남긴다.
`remove`와 `resize`는 동일한 제거 경로에서 삭제 전 SDK native counter를
`DrainNative`에 보존한다. 사라진 actor는 이 실제 drain 관측과 시작 counter가
일치해야 zero-inference wall cost로 인정하며, 삭제나 빈 queue만으로 추론 부재를
증명하지 않는다. resize cost는 기존 prepare/remove primitive 측정의 조합이다.

현재 NodeActuation와 transition-cost의 독립 read-back·raw directed trial은 그대로
재사용한다. 준비·제거·idle state 측정은 해당 실제 동작/상태를 측정하는 별도 raw
record다. delta eta와 whole wall joules의 범위, 같은 노드·구간의 중복을 구별한다.
원자 동작 자체는 측정 campaign이나 control loop의 내부 lifecycle에 묶지 않는다.

## 자원 할당과 공존 경계

노드와 서빙 파드는 1:N, 하나의 서빙 파드가 제공하는 모델 인스턴스와 워크로드도
1:N 관계를 허용한다. 노드 하나를 모델 하나나 워크로드 하나의 전용 장비로 취급하지 않는다.
한 파드는 하나의 모델 실행 인스턴스를 제공하며, 같은 산출물·런타임·입력 조건으로
처리할 수 있는 여러 워크로드가 이를 공유할 수 있다.

worker Pod 하나는 persistent runner 하나와 순차 batch 실행 하나를 제공한다.
실행 중 취소된 HTTP 교환도 runner 완료 또는 actual container 종료까지 물리 자원을 점유한다.

| 조절 대상 | CHILL의 제어 단위 |
|---|---|
| 전체 모델 실행 인스턴스 수 | 서빙 Pod 추가·제거 |
| 인스턴스 하나에 줄 실행 자원 | Pod별 자원 할당과 그에 연동되는 backend 설정 |
| 한 번에 묶어 실행할 입력 수 | 배칭 정책 |

CHILL의 자원 할당 단위·프로파일 실행 구성 단위·서빙 인스턴스를 일치시킨다.
CPU quota·threads와 실제 affinity를 각각 확인하며, 한 배치 내부에서 여러 CPU를 사용한다.
여러 Pod의 인스턴스는 각자 배치 하나씩 병렬 실행할 수 있다.

현재 구성은 실제 Node label과 자원, native 준비와 연결 관측으로 산출한다.
파드 수·실측 자원 조건·배치 정책·전력모드의 후보 계산과 실제 전환은 스케줄러가 연결한다.
자원 변경과 전환 계약은 [전환 명세](../../spec/05_transition_control.md)의 기준으로 구분한다.
메모리는 프로파일에서 관측한 사용량으로 필요한 예산을 산정하는 가용성 제약이다.
시스템 예약분과 프로파일에 포함되지 않은 서빙 부가 사용량을 고려하며,
메모리 할당량 자체를 성능·비용 조절용 탐색 변수로 사용하지 않는다.

공존의 기준은 순간 CPU/GPU 사용률이 아니라 **배정 가능한 자원**이다. 이미 다른
서빙 파드에 할당한 연산 자원을 중복 할당하지 않는다. CPU 파드끼리는 분리된 CPU
할당, GPU 파드끼리는 서로 다른 물리 GPU를 사용한다. GPU 파드의 보조 CPU와
메모리도 계산하므로, CPU 파드와 GPU 파드의 공존에도 자원 배정이 필요하다.

별도 장치 할당기를 만들지 않고 Kubernetes의 기존 자원 할당을 사용한다. GPU는
운영자가 마련한 장치 자원 노출·할당을 활용하며, 한 물리 GPU를 여러 모델 파드가
time-slicing이나 MPS로 나누는 구성은 범위 밖이다. CPU의 request/limit만으로는
CPU 집합이 분리되지 않는다. 전용 CPU 배정에는 CPU Manager의 static 정책,
Guaranteed Pod와 정수 CPU 할당 등 플랫폼 조건이 필요하다. 프로파일링 Job의
관측 컨테이너를 포함한 Pod 자원 설정도 이 조건과 맞아야 한다. 실제 지원 여부와
배정 결과는 배포 시 확인하며, 이 문서가 현재 클러스터의 격리를 입증하지는 않는다.

Serving 준비 파드의 artifact/profile init 컨테이너도 선택한 프로파일의 CPU
request/limit과 선택한 메모리 예산을 사용한다. GPU 장치 요청은 실행 컨테이너에만
둔다. 모든 컨테이너의 CPU·메모리 request/limit이 양수이며 같으면 Guaranteed
조건을 충족하지만, 분수 CPU도 Guaranteed가 될 수 있으므로 QoS만으로 전용 CPU
배정을 입증하지 않는다. CPU 수량은 반올림하거나 request/limit을 강제로 맞추지 않는다.

할당을 분리해도 캐시·메모리 대역폭·노드 전력·열은 영향을 주고받을 수 있다.
우선 자원이 맞는 공존 후보를 허용하고 실제 지연·전력 차이를 관측한다. 자원 적합성과
SLO 적합성은 별개의 판단이며, 관측 없이 무간섭을 보장하거나 모든 조합의 간섭
예측기를 만들지 않는다. 노드 전력모드 변경은 공존 중인 모든 인스턴스에 영향을 준다.

## Immutable worker bundle

worker init은 `/opt/chill/bin/serving-entrypoint --materialize-profile`과 기존 store flags,
`--profile-object-key/size/output`으로 bounded immutable bundle을 원자적으로 제공한다.
runtime image는 [firstfloor image recipe](../../../build/docker/firstfloor.Dockerfile)를 사용한다.
