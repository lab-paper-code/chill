# CHILL 스케줄러 구현 설계

## 1. 기준과 적용 범위

[스케줄러 SRS](../../spec/scheduler/SRS.md)의 SCH-01~32와
[요구사항·구현 대조](scheduler-srs-audit.md)를 구현한다. 원본 `docs/spec/`의
실행기 경계는 사용자 선택에 따라 firstfloor로 갱신했다. 2026-10-08의 소스에서
확인한 재사용 지점은 기존 Serving Runtime·Journal·Kubernetes observer/manager,
검증된 Profiling evidence, NodeActuation, metricobserver와 powerwindow다.

동료의 `/home/genesis1/ETRI/donghwan/firstfloor`는 그대로 보존한다. 해당 소스의
Go 큐·배처·persistent Python IPC runner를 CHILL의 `internal/firstfloor`,
`cmd/serving-worker`, `runtime/firstfloor`로 가져와 실제 실행기로 연결한다.
이 배처의 순수 선택 함수를 실제 실행과 후보 replay가 함께 사용한다.
CHILL의 ingress Runtime은 Workload 등록·수용·ready 인스턴스 분배와 drain을 맡는다.

이 문서는 구현 계약과 소스의 책임을 정리한다. 완료한 구현과 실행 증거·검증 경계는 10절에 기록한다.

## 2. 책임과 패키지 방향

| 소유자 | 입력과 책임 | 실제 완료 근거 |
|---|---|---|
| `internal/firstfloor` | bounded per-UID queue, frozen request SLO, 같은 UID의 호환 batch, energy-gain 대기, 단일 runner 실행 | 요청별 progress·실제 batch·runner 종료·동기 응답 |
| `internal/serving` | 정의·receipt·수명·ingress 대기, 운영 부하, 후보 replay·SLO/energy 선택, 분배·decision journal | 실제 native 상태와 반환 결과·분배 적용 기록 |
| `internal/serving/firstfloor` | HTTP/metadata/progress의 typed 경계, native span과 client span | prepared metadata, 정확한 request/UID/definition/physical instance 상관 |
| `internal/serving/kube` | 실제 자원 snapshot, profile/cost catalog, Pod·policy·membership 원자 동작, 전환 의존성 | API 반환에 이어 실제 Pod/native readiness·drain·삭제·membership 확인 |
| 기존 Profiling owner | 동일 firstfloor 경로의 fixed-batch 측정·idle·raw evidence·curve | 원래 Run/Job/Node/container/image/actuation와 raw trial binding |
| 기존 NodeActuator | 유일한 호스트 전력모드 변경 | 독립 read-back·settling·Ready/Failed |
| 기존 metric/evidence owner | Node별 wall-power·sample/failure·적분·immutable raw 보존 | 조건·구간·coverage가 확인된 watts/joules |

`firstfloor`는 Serving/Profiler 도메인에 역참조하지 않는 실행기 leaf다.
Serving core는 leaf를 사용하고 Kubernetes/API 호출은 kube owner에 둔다.
프로파일러는 자체 runtime evidence 계약과 firstfloor 실행 경로를 연결한다.
`.go-arch-lint.yml`에 이 방향과 실제 생산 패키지를 함께 반영한다.

## 3. 실행기·요청 계약

### 3.1 요청과 실제 모델

외부 Workload API는 입력 1개, named multi-tensor, 현재 numeric/bool datatype,
개별 응답 계약을 유지한다. native `/v2/models/{model}/infer`는 sample에 batch 축을
붙인다. metadata에는 request ID, UID, 불변 definition identity, 원래 receipt,
개별 percentile/target, 전달 시점 경과 시간, 명시적 수명과 profile 근거를 보낸다.

firstfloor는 model/input/runtime 조건이 고정된 한 runner를 유지한다. 같은 UID의
호환 입력을 배치하며 Workload별 SLO와 결과는 개별 request에 남긴다. ORT CPU,
실제 CUDA provider, RKNNLite는 실제 SDK capability를 검사한다. CUDA는 확인된
물리 GPU 한 장을 사용하고 CPU fallback 조건을 실제 오류로 처리한다. SDK가
지원하는 tensor·batch·layout 범위를 확인하여 범위 밖 입력은 오류로 반환한다.
개발 echo mode는 explicit synthetic 실행으로 분리한다.
ORT가 CPU fallback을 비활성화한 세션에도 default CPU EP를 등록할 수 있다.
CUDA-first에 default CPU EP만 추가된 경우 실제 session 설정이 fallback disabled인지
확인한다. 등록 provider 목록은 그대로 기록하며, CPU-only graph 실행은 계속 거부한다.

IPC는 기존 bounded length-prefix header + binary payload를 재사용한다. tensor별
name/datatype/shape/byte count와 outputs를 담아 여러 numeric/bool tensor를
전달한다. UINT64 등의 JSON 숫자는 정수 정밀도를 유지하고, FP16 변환은 유지되는
라이브러리를 사용한다. profile·tensor·policy는 해당 외부 경계에서 검증하여
실행 중에는 검증된 자료와 immutable request context를 공유한다.

### 3.2 시간과 수명

원래 receipt와 sender의 monotonic elapsed를 보존한다. 서로 다른 호스트의 UTC를
그대로 빼서 요청 나이를 확정하지 않는다. Serving의 완료 오버헤드는
`(exchange finish − receipt − native elapsed) + (response finish − exchange finish)`의 관측 최댓값이다.
단방향 전달 시간이나 미래 지연의 보장 상한이 아니며 완료 시점에 한 번만 반영한다.
최근 관측은 같은 노드·프로파일·모델/입력 실행 조건의 호환 정의에 재사용하며,
측정 인스턴스의 제거나 SLO가 다른 Workload UID만으로 근거를 버리지 않는다.
해당 조건의 관측이 없으면 후보 평가는 Pending이고 실제 Serving 측정이 선행해야 한다.
선택한 preparation의 `completionCalibration`은 실제 측정 후 닫혀 발행된 Journal
raw window의 고정 `from/to`와 metadata/definitions/requests object identity를 명시한다.
owner가 기존 evidence bound로 immutable bytes를 한 번 읽어 complete window, frozen
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
같은 연결 표현을 쓰며 snapshot/journal의 전체 `Source`와 raw references를 자르거나
바꾸지 않는다. 긴 provenance 때문에 SDK가 policy를 거절하는 문제를 상한 확대로 숨기지 않는다.
request window마다 definition을 다시 쓰지 않으므로 과거 definition window도 같은
selector의 `windows`에 포함할 수 있다. 모든 선택 stream의 frozen identity를 합친 뒤
`receivedAt ∈ [from,to)`의 성공 trial만 줄이며 `trialIDs`로 원래 request를 찾는다.
같은 raw cohort를 여러 preparation이 선택하면 owner는 그 refresh 안에서 한 번만 읽고
조건별 typed fact를 연결한다. persistent cache나 현재 arrival 구간 변경은 없다.
도착 시각 예측은 owner 분배 시점이라는 가정으로 명시하며 측정값으로 포장하지 않는다.
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

SLO 위반은 모델의 `deadlinePolicy`(drop/keep/defer, 기본 keep)에 따른다. drop은
expired queued 요청을 active 실행 중에도 제거하고 idle 시 infeasible head를 408로
종료한다. keep은 late 결과를 반환하고 defer는 normal 우선의 후순위 FIFO에서 처리한다.
명시적 timeout/cancel은 두 대기열 모두에 적용한다. active batch는 SLO만으로 취소하지
않으며 실제 completion 또는 runner termination 확인 후 capacity를 해제한다.
기존 Runtime의 backendDone + transportDone drain 의미를 유지한다.

### 3.3 같은 선택 함수의 실행과 replay

순수 batch 선택 함수의 입력은 검증된 batch별 service duration sample·incremental
energy/input, 현재 per-UID 요청의 원래 경과 시간/SLO, queue age, 반환 envelope,
max wait, 관측 요청률이다. 출력은 선택 UID·batch size·대기 시간과 판단 이유다.
actual actor와 discrete-event replay가 이 함수를 공유한다.

1. normal 요청은 stable EDF로 선택하며 같은 UID/불변 정의의 호환 요청만 배치한다.
2. 요청별 p-quantile service 비용과 원래 elapsed·반환 비용으로 여유를 계산한다.
3. SLO 여유를 만족하는 현재 batch 중 energy/input을 비교한다.
4. 더 큰 measured batch의 **엄격한 energy gain**과 관측 요청률로 예상 fill time을
   확인한 경우에만 bounded wait를 선택한다.
5. feasible batch가 없으면 keep은 minimum-C fillable batch(동률은 큰 batch)를 즉시
   실행한다. drop은 head를 제거하고 defer는 head를 후순위 FIFO로 옮긴다.
6. normal 요청이 없으면 deferred FIFO의 호환 prefix를 minimum-C로 실행한다.
7. drop/defer/timeout은 성공 지연 표본과 구분하며 caller lifetime/cancel은 별도로 적용한다.

실제 적용되는 설정·profile과 진행 중 batch의 snapshot을 기록한다. accepted
요청의 정의·SLO를 config 변경 때문에 다시 해석하는 대신 기존 context를 유지한다.

## 4. 측정 계약과 catalog

### 4.1 같은 firstfloor fixed-batch 경로

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
kernel cgroup lifetime peak는 worker와 runner를 포함한 runtime container 범위다.

latest successful profile은 같은 frozen node/model/artifact/runtime/input/resources/
power/protocol 조건별로 선택한다. 명시적 source 선택 범위 안의 최신 성공을
사용하고 독립 후보의 오류·누락 이유를 보존한다. source의 SLO와 무관하게 matching
execution profile을 재사용한다. 기존 ORT/RKNN reference 측정은 고유 실험 owner를
유지하며 firstfloor serving evidence와 실행 조건으로 구별한다.

strict stored schema는 작업 단위에서 한 번 변경하고 caller·fixture·generated
schema를 함께 갱신한다. 이전 testbed 객체의 정리는 ADR 0019의 운영 절차를 따른다.
현재 진행 중 campaign의 stored 계약 변경은 해당 campaign 정리 후 적용한다.

### 4.2 baseline과 실제 원자 동작 측정

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
`pool membership`, `power mode`다. production 실행 owner의 동일 함수를 측정
measurement command가 호출한다. measurement owner는 first successful power sample
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

## 5. 판단 입력과 후보 계산

### 5.1 한 운영 snapshot

운영 observation window·control interval·comparison horizon을 독립 설정한다.
Journal의 보존/발행 구간과 지정 구간의 분석 필터는 운영 부하와 구분한다.
모든 UID의 received/accepted/assigned/completion 사건을 한 번 수집해 해당 사건
구간의 inputs/s를 구하고, 실제 malformed/거절 결과도 전체 유입에 남긴다.
계산 가능한 compute demand와 total inflow는 모집단으로 구별한다.

Runtime의 pending/assigned와 firstfloor 현재 queued/running·running elapsed·batch를
physical instance/UID/definition에 연결한다. assignment와 실제 queue entry를
구별하고 unknown ownership·관측 공백·오래된 자료는 판단의 입력 상태에 표시한다.
현재와 목표의 Node UID·단일 POOL 소속·ready/capacity/resource 상태를 함께 캡처한다.
완료/실패 Pod는 이미 제외하던 자원 예산 캡처와 steady source refresh에서
Kubernetes field selector로 제외하여 종료된 profiling 이력의 반복 전송·해석을 피한다.
source refresh의 최초/재시작 watch 복구는 전체 Pod를 읽고, 이후 빠진 retained
executor는 기존 exact Pod GET과 UID/container 종료 확인을 유지한다.

### 5.2 후보 집합과 재현 가능한 replay

현재 유지 후보와 matching measured configurations의 Pod 개수·자원·배치 정책·
node/pool 배치·power mode·분배를 구성한다. 설정된 bounded candidate/replay 수와
검색 범위를 결과에 고정한다. 제한 도달은 검토 범위로 표시한다. 현재 선택이 이
범위에서 가장 유리한 선택이라는 의미로 사용한다.

현재 queued/running 요청에서 시작해 observation window의 실제 arrival sequence를
comparison horizon에 반복한다. arrival 재사용 가정·raw service sample draws·seed·반복 수를 기록한다.
현재 ingress는 실제 Workload queue 순서, native queued 요청은 actor state의 FIFO
순서를 캡처한다. 원래 receipt 시각은 request 나이와 arrival 가정에 사용한다.
명시적 original lifetime의 남은 시간은 queued/pending 취소와 실행 후 결과 상태에
적용하며, 진행 중 batch의 물리 점유는 실제 completion까지 유지한다.
weighted routing은 actual Runtime과 replay가 같은 deterministic
smooth weighted rule을 사용하며, 한 UID에 여러 ready 대상과 집중 대상 모두를
비교한다. 현재 outstanding·진행 중 batch와 공유 instance의 다른 UID도 포함한다.
모든 후보는 active UID별 완전한 분배 map을 갖는다. keep은 실제 분배를 보존하며,
새 준비 slot과 새로 공유된 UID는 인스턴스 weight로 분배를 구성한다. routing
제어가 꺼진 경우 기존 physical instance가 이미 제공하던 UID의 실제 weight를
보존한다. 집중 후보는 한 UID의 분배만
교체하고 다른 UID의 분배를 유지한다. 적용 시 active UID의 분배 누락은 오류이며
암묵적 weight fallback으로 보완하지 않는다. 준비 slot은 실제 Ready physical ID로
해결하고 replay와 같은 smooth weighted rule로 요청을 분배한다.
UID/input-path별 smooth-weighted credit은 같은 physical ID의 weight가 그대로인
경우에만 유지한다. weight 변경·새 route entry는 0에서 시작하며 replay도 이미
진행한 credit과 snapshot에서 처음 읽는 credit에 같은 reset 조건을 적용한다.

각 반복은 실제 batch selector의 결정을 실행하면서 empirical service sample과
같은 trial의 transition readiness/wall cost를 적용한다. 이미 running인 batch의
잔여시간은 현재 elapsed를 넘는 관측 sample의 조건부 잔여분으로 계산한다.
범위 밖 running elapsed는 profile-range 이유를 남긴다. 각 request의 queue·실행·
전달/반환이 반영된 종료 시각에서 **전체 end-to-end 분포**를 만들고 gonum/stat로
Workload percentile을 계산한다. 평균 batch latency나 단계별 quantile의 합을
end-to-end percentile로 사용하지 않는다.

CPU/GPU/memory는 전체 allocations와 system/비서빙 예약분을 포함한 예산으로
검토한다. CPU 할당/실제 affinity는 분리하고 threads를 allocation에 연결한다.
GPU UUID와 whole-device exclusive allocation을 확인한다. measured peak memory와
Serving 부가 예산은 feasible 제약이다. Pod마다 한 prepared runner와 순차 batch를
사용하며 replica는 Pod 수로 나타낸다.

### 5.3 전환·에너지·선택

모든 affected UID의 최종 상태와 전환 중 ready capacity를 같은 시간축으로 replay한다.
node mode 변경·제거는 해당 노드의 모든 instance와 UID에 영향을 준다. 신규 준비
완료 전에는 기존 ready capacity를 유지하고, 실제 routing 적용 뒤 기존 것을 drain한다.
독립 Node의 안전한 동작은 병렬로, 같은 Node의 비용 구간은 의존 순서대로 반영한다.

동일 comparison horizon에서 모든 POOL Node의 baseline을 한 번 적분한다.
active/inactive/temporary Node를 포함한다. 원자 동작 구간의 wall joules는 그 구간의
baseline 소비를 **대체**하고, 실제 serving execution의 incremental energy만 더한다.
비서빙 Pod의 effective CPU request는 Node별 millicore 합계를 먼저 구한 뒤
한 번만 올림하여 quota 수량 예산으로 기록하고, effective memory request도 합산한다.
이 quota 합계는 OS affinity/exclusivity 관측의 증거가 아니다. 명시적 exclusive CPU
예약이 전체 millicore 합계를 덮지 못하면 Node는 ready가 아니며, 비서빙 Pod의
물리 GPU 소유권 미관측도 계속 차단한다. 현재 native 서빙 인스턴스는 이 합계에서 제외한다.
GPU 소유권의 미관측은 해당 Node의 `ready=false`와 사유로 남기며 그 Node를
사용하는 후보를 Pending으로 둔다. 다른 확인된 CPU Node의 후보를 전역 오류로
막지 않는다. 격리된 Node도 전체 POOL 에너지·meter 범위에서 제외하지 않고,
실제 자원 예산 및 전역 native/profile 관측 공백의 차단은 유지한다.

전환 eta를 사용할 때에는 원래 target baseline subtraction 의미를 맞춘다.
서로 독립된 meter의 Node만 합산한다. 시간 단위의 readiness와 joules를 구별한다.

모든 active UID의 SLO가 feasible한 후보들에서 예상 total energy가 가장 작은 것을
선택한다. 전체 후보가 infeasible한 과부하는 worst normalized SLO 초과를 먼저
줄이고 동률에서 energy를 비교한다. 근거가 부족한 판단은 Pending/reason으로 남긴다.
표본 0의 percentile은 값 없음이다. keep/target/rejected 후보·예상치·설정·근거를
원시 decision record에 남긴다. 실제 요청 완료와 whole-POOL 전력 기록을 같은
관측 구간·UID·decision에 연결하여 분석자가 예측과 실측 차이를 계산한다.

## 6. 실제 적용과 관리 연동

kube owner가 measured target별 worker profile bundle과 작은 mutable policy config를
제공한다. 큰 raw 분포는 immutable store/volume에서 startup에 한 번 로드하고
request마다 복사하거나 ConfigMap에 반복 전송하는 대신 공유한다. actual native
metadata/progress에서 profile·policy 적용과 readiness를 확인한다.

전환 기록은 desired/start/progress/complete/failed/partial과 prerequisite를 가진다.
실제 준비 Pod, NodeActuation, routing 적용, drain·remove·membership 완료를 확인하여
다음 동작을 시작한다. resource resize는 검증된 새 resource Pod를 준비하는 동일한
prepare→route→drain→remove 동작 조합이다. 기존 독립 writer를 중복 생성하는 대신
NodeActuation을 생성해 sole host actuator를 사용한다.

부하·Workload 관리 상태가 바뀌면 같은 snapshot/candidate 로직으로 재판단한다.
이미 수행된 결과는 실제 현재 상태이며, 진행 중 동일 동작은 재사용한다. cancel은
아직 시작 전/중단 가능한 동작에 적용하고 physical effect가 발생한 뒤에는 관측된
상태에서 다시 선택한다. 실패 Node·instance는 route에서 격리하고 실제 recovery
proof에 따라 재사용한다. 기존 관리 admission gate·accepted definition·공유 UID
보호·actual drain과 같은 owner를 사용한다.

## 7. 설정·권한·관측 API

`/api/v1/scheduler`에서 settings/current snapshot/decision/target/actions와 적용
상태를 제공한다. settings 변경은 기존 TokenReview·SubjectAccessReview 경계를
통과해 scheduler ConfigMap write 권한으로 검증하고 before/after·actor·검증 결과·
영향 범위·실제 적용 시간을 기록한다. observation/control/horizon, resource bounds,
replay/candidate limits·seed, measured cost sources·transport envelope, 제어요소 활성
상태를 설정·기록한다. 정적 구성/제어요소 off 실험도 같은 관측을 사용한다.

Journal은 request/management와 더불어 decision/settings/action/power 원시 기록을
보존한다. 수신 모집단과 completion-event rate를 구별하고 unknown/missing과 0을
각각 표현한다. node watts·whole energy·incremental energy·단위·meter/구간/coverage를
연결한다. 모든 오류·거절·timeout/cancel·미완료·late와 요청/batch 수를 각각 제공한다.

### 7.1 구체 설정과 조회 계약

HTTPS owner는 `--serving-scheduler-configmap=<name>`과
`--serving-scheduler-config-key=scheduler.json`으로 Serving namespace의 구성을 읽는다.
빈 ConfigMap 이름은 기존 Workload owner만 실행하며, scheduler 구성에서
`settings.controls.enabled=false`를 선택하면 동일 관측·후보 기록으로 정적 실험을 실행한다.

| 구성 필드 | 의미와 실제 근거 |
|---|---|
| `settings` | nanosecond duration의 observation/control/horizon, search/replay/resource bounds·seed·제어 활성 상태 |
| `cpuInventory`, `gpuInventory`, `reserved` | Node별 실제 CPU IDs·물리 GPU UUIDs와 비서빙 전용 예약. quota는 별도 수량 예산 |
| `preparations` | catalog profile ID별 `PrepareSpec`. 검증된 profile과 immutable service 분포 bundle은 owner가 연결 |
| `preparations[profileID].completionCalibration` | 선택 OPTIONAL: 고정 `from/to`, `windows[].metadata/definitions/requests`의 기존 `ObjectIdentity`; raw 계약·파일은 재작성하지 않음 |
| `staticRouting` | OPTIONAL 전체 현재 Workload UID → physical instance ID → nonnegative int32 weight; UID마다 positive weight 하나 이상. controls OFF의 명시 config revision만 기존 typed ApplyTarget 경로로 적용·physical read-back하며 `manual-static-routing` action의 `sourceConfigRV`에 남김. stale UID/target은 오류; 선언을 유지하는 OFF refresh는 baseline을 보존하며 이후 CHILL은 실제 routing에서 시작 |
| `costReferences`, `powerCostSources` | 실제 원자 동작 raw object와 Complete directed power-mode 측정의 출처 |
| `meters` | 정확한 Node UID·exclusive wall scope·metric series에 연결한 meter 선언 |
| `powerRequests` | Node UID/power mode별 기존 sole actuator의 NodeActuation 요청 |
| `powerObservation` | 기존 sampler의 `Interval/Duration/RequestTimeout/MaxEvents`와 적분 coverage의 `MaximumBoundaryDistance/MinimumCoverage/MaximumGap` |

`staticRouting`의 configuration 수신과 실제 적용은 별개다. 물리 source readiness가
아직 없거나 UID/target 검증이 실패하면 owner listener를 막지 않고 기존 refresh
cadence에서 controls OFF인 동안만 미적용 revision을 다시 평가한다. 실패는 선언
원본·`sourceConfigRV`·실제 routing·오류를 가진 manual action으로 journal과 scheduler
state에 남으며 적용 성공으로 표시하지 않는다. 전체 physical read-back이 일치한
revision은 다시 적용하지 않고, controls ON에서는 미적용 선언도 재시도하지 않는다.

Calibration selector의 정확한 preparation payload 형태는 다음과 같다. 값은 실제
측정과 발행 object identity에서 채우며 Serving lifecycle은 selector를 자동 작성하지 않는다.
campaign은 성공한 실제 측정과 closed/published window 확인 후 selector 단계를 출력한다.

```json
{
  "completionCalibration": {
    "from": "<fixed receipt cohort start RFC3339Nano>",
    "to": "<fixed receipt cohort end RFC3339Nano>",
    "windows": [{
      "metadata": {"store": "<store>", "key": "serving/<windowID>/metadata.json", "size": 123},
      "definitions": {"store": "<store>", "key": "serving/<windowID>/definitions.jsonl", "size": 456},
      "requests": {"store": "<store>", "key": "serving/<windowID>/requests.jsonl", "size": 789}
    }]
  }
}
```

`size` 예시는 실제값이 아니다. 기존 `Journal.Health().windows[].objects`는 Go 내부
조회이며 HTTP `GET /readyz`는 `{ready}`만 노출한다. 발행한 object list는 기존 owner
PVC의 `<observationDirectory>/<windowID>/metadata.json`에 보존된다. immutable S3
metadata는 object list를 붙이기 **전**에 업로드되므로 `published=false`/objects 없음이
정상이다. 따라서 동일 window의 세 raw role을 위 selector로 명시한다. owner의 기존
configured evidence store `Reader.Open(ctx, ObjectIdentity)`가 `serving/<windowID>/...`
bytes를 읽는다. 새 publisher/reader API, digest, schema version 또는 TTL은 추가하지 않는다.

CPU 측정의 `cpuAffinity`와 campaign의 `cpuQuota`는 선택한 physical mask와 threads를
같은 firstfloor runner에서 실측하는 입력이다. frozen `affinityCPUs`와 실제 허용
inventory·적용 affinity를 raw에 연결하며 quota와 mask의 의미를 각각 유지한다.
기존 quota-only reference 측정은 별도 조건으로 유지한다.

| API | 동작 |
|---|---|
| `GET /api/v1/scheduler` | settings와 실제 snapshot·decision·actions·results |
| `GET /api/v1/scheduler/settings`, `/state` | 운영 설정 또는 현재 판단·적용 상태 |
| `PUT /api/v1/scheduler` 또는 `/settings` | 직접 `SchedulerSettings`를 검증하고 named ConfigMap에 저장·적용 |
| `GET /api/v1/observations/load` | 사건 구간의 모든 UID 유입·접수·배정·완료율과 instance assignment 집계 |
| `GET /api/v1/observations/scheduler` | 고정 `from/to`, bounded `limit`, append-order `cursor/nextCursor`의 원시 기록 |
| `GET /api/v1/observations/power` | 같은 고정 `from/to`의 raw 표본을 Node당 한 번 적분한 전체·활성 POOL J/평균 W; 누락은 null과 사유 |

Journal 조회는 기존 writer mutex 아래에서 필요한 stream 경로·byte 길이와 window
상태를 한 번 함께 캡처하고, 파일 읽기·JSON decode·집계는 잠금을 푼 뒤 수행한다.
완료된 append 뒤의 길이를 고정하므로 새 append·rotation을 현재 조회에 섞거나
진행 중 record를 잘린 tail로 오인하지 않는다. 실제 read 오류·잘린 원본은 health와
incomplete 상태에 그대로 남는다. scheduler history는 사건 시각 envelope로 먼저
선택하여 요청 page만 전체 decode하고 다음 일치 record 하나로 continuation을 확인한다.
window 시각은 append 시각이지 사건 시각의 범위가 아니므로 backfill·역순 사건을
버리는 window 필터는 쓰지 않는다. power 조회도 결정·profile 분포 대신 기존 집계에
필요한 실제 표본·meter·coverage·membership만 읽으며 경계와 전체 POOL 범위를 유지한다.

전력 collector의 짧은 기록 구간별 적분 verdict는 원시 기록에 그대로 보존하되,
조회 구간의 coverage 판정으로 재사용하지 않는다. 조회 경계 밖의 실제 표본은 기록된
boundary 거리 안에서만 보조하며, 합친 표본의 원래 gap·값·meter identity·보관 완전성과
조회 구간의 coverage를 검사한다. 조회 중 실제 source 실패와 OOM/재시작 공백은
누락으로 남기며, 이전 구간의 오래된 표본 때문에 정상적인 닫힌 구간을 무효화하지 않는다.
닫힌 Journal window의 겹침은 예정 `EndsAt`보다 이른 실제 `ClosedAt`에서 끝난다.
전력·membership·arrival·완료 overhead 조회가 같은 경계를 사용한다. boundary 표본은
현재 선언의 Node·meter·scope와 source kind/endpoint/metric/labels가 일치한 뒤에만
선택하며, source가 없거나 바뀐 과거 기록을 현재 meter의 실측으로 재해석하지 않는다.

설정 변경은 검증된 identity의 groups/extra까지 사용하여 해당 namespace/name의
`configmaps/update` Resource SAR를 추가 확인한다. decision과 snapshot을 journal에
보존한 뒤 physical action을 시작하고, 실제 시작·완료·실패와 부분 상태를 별도로 기록한다.

worker init은 `serving-entrypoint --materialize-profile`과 기존 store flags,
`--profile-object-key/size/output`으로 bounded immutable bundle을 원자적으로 제공한다.
runtime image는 [firstfloor image recipe](../../../build/docker/firstfloor.Dockerfile)를 사용한다.
measurement command는 제한된 `measurement-*` POOL과 관측된 요청 소유권·native
counter 불변 조건에서 동일 원자 동작을 실행한다. observation directory는 process
exclusive lock으로 소유하며, live Serving traffic과의 병행 측정은 이 측정 조건과 구분한다.

## 8. SRS와 구현 확인 경로

| SRS | 구현 경로 | 확인할 소비자 행동 |
|---|---|---|
| SCH-01~02, 19, 28 | candidate selection + decision record + actual membership | keep/change·SLO-first·energy 비교·overload·Pending 구별 |
| SCH-03~08, 29, 31 | Runtime/Journal/window/native state | UID·frozen context·전체 유입·0/unknown·window 경계·관리 중 drain |
| SCH-09~12 | model/runtime/profile catalog + resource snapshot | latest same-condition·같은 실행기·분포/memory·전체 할당·exclusive resources |
| SCH-13~15 | shared selector + per-request replay | 공유 비선점·현재 running residual·E2E percentile·node-wide impact |
| SCH-16~18 | idle/action/transition measurement + whole-pool integration | inactive/temp Node·wall/delta 의미·중복 제외·같은 horizon·실제 readiness |
| SCH-20~23 | Runtime routing + firstfloor actor | ready 즉시 전달·원래 elapsed·UID compatible batch·energy-gain wait·명시적 수명 |
| SCH-24~27, 30 | kube atomic actions + operation ledger | prepare-route-drain-remove·actual effect·partial failure·in-progress reuse·appliedAt |
| SCH-32 | authenticated settings + control flags + frozen record | 권한·변경 결과/영향/적용·동일 조건의 static/off 비교 |

### Ponytail 검수 반영

- firstfloor 배처의 실제 선택 함수는 actor와 replay의 두 소비자가 재사용한다.
- 공존 조합별 선행 campaign은 제거하고 측정/명시적 additive 예상과 실제 오차를 구분한다.
- 측정은 기존 sampler/store와 원자 동작을 호출하는 command로 구성한다. 별도 측정 CRD·범용 campaign/recovery engine은 추가하지 않는다.
- mutable policy는 작게 유지하고 raw profile은 startup에 로드하여 공유한다. 입력별 profile 복사와 별도 broker/DB는 추가하지 않는다.

## 9. 커밋 단위와 검증

1. 원본 실행기 경계·긍정형 SRS·과소/과대 감사·본 구현 설계를 확정한다.
2. firstfloor 소스를 재사용하여 실제 typed request/SLO/queue/batch/progress 실행 계약을 연결한다.
3. 동일 firstfloor Profiling과 frozen/generated contracts·profile catalog를 cutover한다.
4. 실제 부하/snapshot·공유 policy replay·자원·whole-pool SLO/energy 판단을 연결한다.
5. 원자 Serving 동작·실제 측정·전환 orchestration·관리 연동을 실행한다.
6. 권한 있는 설정/상태/원시 관측 API·배포 구성을 연결하고 전체 smoke/gate를 수행한다.

각 단위는 해당 소비자 행동의 deterministic regression과 실제 실행 경로를 확인한다.
최종 local smoke는 실제 entrypoint→firstfloor→persistent runner의 요청·다중 UID·
개별 SLO·policy 적용·fixed profiling·결정/동작 결과를 관측한다. synthetic 자료와
실제 backend/hardware 측정의 증거를 구별한다. live cluster·외부 소스·이미지 registry의
변경은 별도 승인된 실행으로 다룬다.

최소 gate는 focused Go tests, `make lint test arch-check gopls-check`, API 변경의
`make manifests generate`, runtime contract suites, Helm default/Profiler의
`make helm-lint helm-template`, topology 변경의 `make deployment-graph-check`다.
실제 surface smoke·raw 계산 재현·실행 결과와 실시하지 않은 live hardware 검증을
별도로 기록한다.

## 10. 구현 완료와 실행 증거

2026-10-08 UTC의 최신 소스에서 native 실행·동일 Profiling·후보 replay·
원자 동작/측정·관측/권한 API·Helm·Dashboard를 연결했다. 실제 소스의 소비자
경로와 수용 수준은 [최종 SRS 감사](scheduler-srs-audit.md#5-최종-검증과-판정)에 대응한다.
동료 firstfloor 디렉토리는 보존했고, Git staging·commit은 실행하지 않았다.

### 10.1 실행한 gate

| 명령·surface | 관측 결과 |
|---|---|
| `go test ./internal/firstfloor -run TestNativeLifetimeWitnessKeepsAcceptedDeadlineAcrossPolicyChange` | 변경한 완료 오버헤드와 독립된 원래 accepted deadline 유지 |
| `make lint test` | lint·vet·전체 non-e2e Go·envtest integration 통과 |
| `make manifests generate` | deepcopy·CRD·Helm CRD/RBAC 동기화; envtest에 실제 CRD 설치 성공 |
| runtime suites (`make test`에 포함) | ORT CPU 23, RKNNLite 20, firstfloor 6, artifact tool 6 tests 통과 |
| `make arch-check` | production package 65개가 component 43개에 매핑; dependency 및 negative fixture 통과 |
| `make gopls-check` | pinned Go 1.26.4/gopls 0.23.0, workspace/file errors 0; implementation·definition·reference·framework-derived event gate 통과 |
| `make helm-lint helm-template` | default release `chill`, namespace `chill-system`, 기본 values: resource 29, kubeconform valid 17/skipped 12 |
| `make deployment-graph-check` | default 29, 별도 Profiler profile 42, 추가 resource 13; manifest/resource·operand ref·RBAC gate 통과 |
| 아래 scheduler-owner 명시 렌더 | resource 42, kubeconform valid 30/invalid 0/errors 0/skipped 12 |
| firstfloor/serving-entrypoint Dockerfile의 실제 image build | 두 local image build 통과; registry publish 0건 |
| Dashboard `pnpm lint`, `pnpm typecheck`, `pnpm test`, `pnpm build` | 모두 통과; test 3개, build는 Node 24.21.0 |

architecture/gopls gate에는 임시 경로의 공식 ripgrep 14.1.1 바이너리를 PATH에
추가했다. repository 설정·외부 agent 설정은 변경하지 않았다. deployment SVG는
review view이며 desired manifest/resource 집합과 실제 runtime 상태를 구분한다.

명시 scheduler-owner 렌더는 기본 values에 다음 입력을 적용했다. Profiler render는
별도 profile이며 이 owner render의 자원과 합친 배포 상태로 해석하지 않는다.

```sh
helm template chill-local charts/chill --namespace chill-serving \
  --set evidenceStore.connection.bucket=chill-evidence \
  --set evidenceStore.connection.region=us-east-1 \
  --set servingEntrypoint.enabled=true \
  --set servingEntrypoint.scheduler.configMap=chill-scheduler \
  --set servingEntrypoint.tls.secretName=chill-owner-tls \
  --set servingEntrypoint.scheduler.powerCostSources[0].namespace=chill-measurement \
  --set servingEntrypoint.scheduler.powerCostSources[0].name=measured-power-cost
```

렌더를 `kubeconform -strict -summary -kubernetes-version 1.31.0
-skip CustomResourceDefinition,ChillSystem`에 전달하고 pipeline 실패도 확인했다.
이 입력은 existing ConfigMap·TLS Secret·cost source 참조의 desired-resource 검증이다.

### 10.2 실제 실행 smoke

- 실제 HTTPS entrypoint→SDK→persistent firstfloor→ORT CPU에서 named FP32·UINT64·BOOL 입력을 처리했다. FP32 `[1,2,3,4]`는 `[2,4,6,8]`, UINT64 `18446744073709551615`는 raw JSON에서 정확히 보존했다. 응답의 explicit physical 완료와 원래 request/UID/definition을 확인했다.
- 최종 native serving image에 두 UID의 요청 8개를 동시 전달했다. 모두 실제 추론·finished·완료를 반환했고 accepted/completed/executions 각각 8, 남은 native 요청 0이었다. 이미 소비한 500ms와 0.01ms SLO 목표도 명시적 10초 수명과 독립적으로 처리했다.
- 실제 fixed batch K=1·2·4 실행과 policy cap 1의 적용을 확인했다. immutable maxBatch 4와 실제 policy 적용을 구분했고, 명시적 만료 요청은 native 실행 진입 전에 408로 처리했다.
- 최종 production image의 `measurement.py`는 같은 actor/runner에서 K=4를 1.000254초 측정했다. warmup 3회 제외 후 actual execution 1,682개·input 6,728개, distinct batch ID·client/native/backend/service sample의 상관과 counter를 확인했다.
- 이 측정의 CPU quota는 `100000 100000`, 실제 affinity는 `[0]`, ORT intra/inter thread는 1이었다. memory 60,592,128 bytes는 `cgroup-v1-memory.max_usage_in_bytes:runtime-container-lifetime` 의미로 보존했다. wall-power·energy는 미측정 null이다.
- 실제 측정 종료 smoke에서 동시에 group에 TERM을 보내던 worker/runner 종료 경합을 발견했다. worker에 먼저 종료를 요청하여 owner가 runner를 정리하고 grace timeout에서 group KILL하는 경로로 수정했다. 수정 image의 실제 측정 종료는 exit 0·오류 로그 0건이었다.
- local envtest API server의 실제 TokenReview/SAR와 verified-CA HTTPS를 사용했다. 미인증 추론 401, malformed input 400, authorized 설정 PUT의 전후 정의·validatedAt/appliedAt, 상태와 raw cursor를 확인했다. 접수 전 거절을 포함한 actual load는 received 3·accepted/assigned/completed 1이었다.
- 실제 controller tick은 snapshot·decision을 journal에 보존했다. 미측정 inventory 상태는 `Pending`/`MissingPoolInventory`, action 0건이었다. static control-off에서도 관측과 keep 판단을 유지했다.
- 실제 Next Dashboard에서 인증 후 firstfloor/backend·maxBatchSize/maxQueueSize authoring과 최종 review를 브라우저로 확인했다. 실측 campaign 등록·Pod 준비를 이 화면 확인의 통과 근거로 사용하지 않았다.

### 10.3 수용 경계와 커밋

local service/profile의 Node·image identity는 명시한 fixture다. qualified campaign의
벽전력·물리 Node·frozen image evidence와 구분한다. GPU/RKNN 실제 장비,
독립 wall-meter, host 전력 전환·kubelet readiness를 포함한 full Serving campaign,
hardware SLO/총에너지/공존 예측 오차와 live/kind e2e는 실행하지 않았다.
동료 디렉토리·live cluster·image registry에 변경을 적용하지 않았다.

9절의 6개 논리 단위에 해당하는 소스·tests·docs·generated artifacts는 worktree에
남겨 두었다. `docs/spec/00_measurement.md`는 현재 `docs/spec/*.md` ignore 규칙의
대상이다. 해당 normative 수정은 커밋에 포함할 때 명시적으로 `git add -f
docs/spec/00_measurement.md`로 선택한다. 이 작업에서는 index를 변경하지 않았다.


## 11. 2026-10-09 실클러스터 배포·확인

사용자의 실클러스터 확인 요청에 따라 `kubernetes-admin@kubernetes`의
`chill-system`, Helm release `chill`을 대상으로 실행했다. `fast-deploy`의
기본 reset은 ChillSystem을 삭제하므로 `--skip-deploy`로 현재 소스를 build/push하고,
기존 `helm get values`를 보존한 `helm-release-flow.sh install` upgrade로 적용했다.
image tag는 `scheduler-live-20261009`, Helm revision은 8에서 9로 변경했다.

### 배포 입력과 수정

- `fast-deploy --components all`의 operator·node-discovery·node-actuator·profiler·console과 별도 serving-entrypoint·dashboard를 build/push했다. 기본 `all`의 범위를 실제 script에서 확인했다.
- Docker container builder의 연결 오류와 Docker driver의 multi-platform/HTTP registry 제한을 확인했다. root/owner는 `edge-builder`, 최종 Dashboard는 `chill-builder`로 publish했다. 외부 Docker/agent 설정은 변경하지 않았다.
- 기존 PVC·ChillSystem UID `17722831-d04d-4d8f-9e63-1bc13d5eef07`와 사용자 Model/Workload를 보존했다. 새 stored-contract Journal은 `/data/scheduler-native-alpha2`를 사용한다.
- 기존 owner의 재시작 306회와 read-only `/tmp`의 evidence candidate 생성 실패를 관측했다. read-only root는 유지하고 Helm에 128Mi `/tmp` emptyDir를 연결했다. Journal 경로는 `observationStorage.directory`로 선택한다.
- `serving-scheduler` ConfigMap은 실제 sample의 controls-off 설정으로 생성했다. inventory·profile·cost·meter를 작성한 측정값으로 대신 채우지 않았다.
- 보존한 live values의 preflight는 resource 70개, kubeconform valid 58/invalid 0/errors 0/skipped 12와 CRD ownership을 통과했다. 실제 upgrade의 `--wait`도 통과했다.

### 관측 결과

| surface | 실제 결과 |
|---|---|
| `ChillSystem/chill` | 동일 UID·generation 1, Ready 및 enabled component conditions True |
| node-discovery / node-actuator | 각각 10/10, 1/1 Ready |
| operator·profiler·console·dashboard·serving-entrypoint | 새 tag, 각각 1/1 available |
| serving-entrypoint | `chill-serving-entrypoint-98bb955b8-gh7qh`, 재시작 0; 실제 imageID `sha256:6502be6e9bad336c9b5bf297a2aa33682d72224768d9938ad43ffa6bb7f761c2` |
| verified-CA HTTPS `/readyz` | 200, `ready:true`; window publication 오류 로그 0건 |
| model·workload·load API | 기존 Dashboard bearer로 200 |
| scheduler settings/state/raw | scoped 임시 verifier의 TokenReview/SAR로 200; controls off, `Pending/MissingPoolInventory`, actions 0, raw decision과 nextCursor 확인 |
| 기존 Dashboard/owner bearer의 scheduler 조회 | non-resource 권한의 403; 임의 권한 확장 없이 구분 |
| isolated native real-artifact Model/Workload | 각각 201, frozen Definition 연결, `Waiting/MissingEvidence` |
| isolated Workload 추론 | malformed 입력 400, 실제 계약에 맞는 FP32 `[3,224,224]` 입력은 503 `MissingEvidence`; 실제 추론 완료로 해석하지 않음 |
| Dashboard | 실제 cluster Service tunnel에서 인증·서비스 목록·native 측정 대기 상태의 visual 확인 |

기존 `dashboard-validation-a/b`는 새 owner에서 `AppliedSnapshotUnavailable`/
`AppliedDefinitionUnavailable` 상태로 관측됐다. 기존 Model의 `onnxruntime/
CPUExecutionProvider` 계약과 이전 applied snapshot을 현재 native 등록 계약에
맞춘 운영 전환이 필요하다. 해당 사용자 객체의 spec·UID는 유지했으며 상태를
숨기거나 compatibility reader를 추가하지 않았다.
새 Journal 경로에는 해당 이전 definition identity가 존재하지 않는다. 실제
`Manager.restoreDefinition`은 `RetainedDefinition(identity)`를 조회하고 확보된
snapshot에만 복구를 허용한다. 관측한 실패는 이 경계에서 발생했으며, 새 운영
정의는 explicit 관리 요청으로 적용하는 방식으로 전환한다.

qualified native campaign과 준비된 native Serving Pod는 이 클러스터에 확인되지
않았다. 성공 추론·SLO·총에너지·물리 전환의 종단 통과는 이번 결과에 포함하지 않는다.
동료 firstfloor 디렉토리와 기존 `plan10-serving-jetsono`는 변경하지 않았다.
isolated smoke Model/Workload는 실제 owner DELETE·drain을 통해 정리했고,
임시 verifier SA/RBAC·browser tab·port-forward도 정리했다. 운영 배포와
controls-off ConfigMap, 기존 저장 데이터는 유지한다.

