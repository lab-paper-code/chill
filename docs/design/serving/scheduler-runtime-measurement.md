# Scheduler 실행기와 측정 계약

실행 조건과 요청 시간, 조건부 service와 측정 근거의 계약이다. 수치와 후보 비교는 [계획기](analytic-planner.md), 제어와 장부는 [구현 설계](scheduler-implementation.md), 외부 접수는 [워크로드 관리](workload-management.md)를 따른다.

## 요청과 실행기

원시 필드 집합 한 개가 요청 표본이다. native `/v2/models/{model}/infer`는 필드별 원소 하나를 받고 firstfloor가 batch 축을 붙인다. UID와 Definition, request ID, 개별 SLO와 수명, sender elapsed와 profile 근거를 유지한다.

gateway는 BYTES, 필드와 길이, JPEG header와 UTF8을 검증한다. 전처리와 tensor는 runner 책임이다. [이기종 입력과 IPC](heterogeneous-workloads.md)를 따른다.

Pod당 persistent runner 하나와 순차 batch 하나다. 같은 UID와 정의의 호환 요청만 묶는다. CUDA는 실제 whole GPU UUID 한 개와 정확한 provider를 확인한다. echo는 명시 synthetic 실행이며 모델 측정으로 취급하지 않는다.

## 시간과 수명

원래 receipt와 sender monotonic elapsed를 유지한다. 실제 요청은 accept 때 POST를 동결하고 native sender에서 이미 흐른 시간을 한 번 전달한다.

`completionMarginMS`는 유한 비음수 필수 초기값이다. 유효한 client 결합 표본이 생기면 Workload SLO percentile의 TOTAL과 POST를 사용한다. 실제 요청에는 frozen POST 한 번만 적용한다. 새 Tick의 PRE나 TOTAL, 현재 configured margin을 중복 가산하지 않는다.

native slack은 남은 SLO에서 frozen completion reserve와 조건부 service 비용을 뺀다. 최종 수명도 원래 lifetime에서 sender와 local elapsed, reserve를 한 번 뺀다. returning은 native service로 다시 실행하지 않고 남은 반환과 HTTP 점유로 복원한다.

| 시간 | 계약 |
|---|---|
| observationEnd | 명시 historical cutoff.0이나 현재 at으로 대입하지 않음 |
| at | 외부 blocking I/O 뒤 Runtime lock에서 outstanding과 정의, route와 credits를 함께 동결 |
| source ObservedAt | 원래 local receipt. cutoff나 at으로 다시 표시하지 않음 |
| nativeSpanAt | 완료 supplement의 별도 span anchor. 기존 probe 시각과 actual ACK를 덮지 않음 |

요청 나이를 at으로 전진시키되 immutable receipt와 native sender, source 시각은 보존한다. 미상과 미래 anchor, 역행과 뒤집힌 age 범위는 거절한다. 고정 age TTL로 실제 관측을 대신하지 않는다.

## 과부하 정책과 physical 완료

`Workload.spec.admission`의 Reject와 Delay, maxQueuedRequests를 Definition과 요청에 동결한다. Reject는 SLO 불가능 head를 `DeadlineInfeasible`로 실행 없이 거절한다. Delay는 후순위 FIFO로 옮겨 normal 뒤 늦은 완료를 허용한다.

입력과 support를 판단할 수 없으면 AdmissionUnknown이다. batch 확정 직전 최신 monotonic 시각으로 cancel, 최종 기한과 SLO를 다시 검사하고 제거와 지연 전이 뒤 다시 선택한다. fixed-batch는 별도 예외다.

gateway normal과 deferred 합은 maxQueuedRequests 기본1024다. native count/byte와 HTTP semaphore는 별도다. timeout/cancel은 큐에 적용하되 active batch는 실제 완료나 runner 종료까지 점유한다.

| executor 결과 | 계약 |
|---|---|
| DeadlineInfeasible |503, stage와 policy, remainingSLONanos, correlated 미실행 progress |
| actor QueueFull |429, 같은 identity의 미실행 proof. enqueue 전이므로 executor wait 없음 |
| pre-decode semaphore QueueFull | notAdmitted=true. 가짜 policy와 progress를 만들지 않음 |
| 최종 수명 만료 |408 RequestTimeout. active이면 executionComplete=false와 점유 유지 |

executorWaitNanos는 native enqueue부터 running이나 큐 종료까지다. receipt부터 queueMS와 다르다. HTTP timeout이나 전송 실패는 실행 완료가 아니다. backendDone과 transportDone을 모두 유지한다.

완료와 미실행 proof는 exact executor, Node와 요청 identity를 요구한다. multi-member batch는 마지막 member의 완료 proof까지 running이다. IPC 전 running이나 실행 여부 없는 완료는 unknown과 idleUnknown으로 유지한다.

serviceSample은 실제 IPC 시작과 종료에서 직접 측정한다. batch ID와 finished만으로 실행 시작을 추정하지 않는다. 완료 identity는 그 뒤 probe가 부재를 확인할 때까지 유지해 다른 probe가 완료 요청을 되살리지 못하게 한다.

native가 HTTP보다 먼저 완료되면 outstanding은 returning이다. 열린 HTTP의 returnElapsed는0이며 dispatch 점유를 즉시 반환하지 않는다.

## Admission 근거와 transport

assigned/final은 허용 당시 admissionPrediction과 exact instance/profile, actual policy, source와 span anchor, outstanding과 normal/deferred, running/IPC를 동결한다. prediction은 판단부터 예상 완료까지이며 receipt부터 elapsed와 구분한다.

admissionWaits는 eligibility와 queue class가 바뀔 때 실제 실패 사유와 입력을 남긴다. support 부재를 최종 수명 불가능으로 대체하지 않는다. gateway와 native deferred 시각은 다른 local clock이다.

workerHTTP의 connection acquire, connect, request write와 first byte는 기존 body-read exchange 안의 관측이다. connect는 acquire와 겹친다. nativeHTTP는 handler-entry anchor와 로컬 received/decode/enqueue/finished/write-start offset이다. write-start는 응답 쓰기 시작을 나타낸다.

두 로컬 duration의 차이는 residual이다. 원격 UTC로 편도 지연을 만들거나 미분리 network, encoding과 read 비용을 임의 귀속하지 않는다. supplement는 HTTP outcome과 client SLO 판정을 덮지 않는다.

## 공통 actor와 frozen recipe

Serving과 Profiling은 같은 serving-worker, persistent IPC runner와 firstfloor RuntimeContainer를 사용한다. init은 모델 files, 전처리와 immutable profile bundle을 materialize한다.

Actor의 measured runtime image reference와 실제 관측 version이 실행 조건이다. 태그와 digest reference 모두 plain string으로 허용하며 pinning과 imageID gate는 없다. 현재 declaration image로 frozen image를 대입하지 않는다.

memory request/limit은 CurvePlan 준비 전에 동결한 Actor envelope를 유지한다. request는 peak의 하한이 아니다. queue는 같은 limit의 measured peak 뒤 양의 headroom 안에 둔다. worker와 init에는 CPU resource를 주입하지 않는다.

ORT는 기본 자동 병렬성을 사용한다. affinity와 cgroup, quota와 실제 thread 값은 관측이다. GPU UUID, RKNN delivery와 host library, initialized batch와 실제 runtime identity는 검증한다.

capability initialization과 measured batch warmup은 별도 domain이다. 실제 측정 K 전체를 같은 순서로 warmup하며 cap/fixed K만 warmup하지 않는다. initialized/measured 안의 정책 cap 증가는 재시작 없이 ACK로 적용한다. profile와 initialized capacity 변경은 재시작한다.

## 조건과 현재 측정 선택

public profile은 semantic execution condition별 최신 성공을 보존한다. 현재 측정은 model/class/path/mode 셀별 최신 항목이며 failed/partial도 현재 사실이다. explicit replacement를 recency 전에 반영하고 최신 실패를 과거 성공으로 대체하지 않는다.

PerformanceCondition은 실제 모델과 전처리, 표본 recipe, runtime image와 관측 version, Node/backend와 memory/power 조건을 유지한다. collection과 lifecycle overlay는 성능 조건과 구분하고 원래 provenance에 보존한다.

catalog는 현재 source UID/RV와 terminal 적격성을 읽는다. immutable source의 검증 raw는 재사용하고 변경과 삭제, pending은 cache에서 제거한다. 오류에 이전 success로 fallback하지 않는다.

cold trial load는 입력 순서의 작은 window에서 최대8개의 execution/raw read를 병렬 처리한다. 완료 뒤 원래 순서로 도출하고 첫 오류도 결정적이다. 실제 byte cap과 실행/관측, Run/Job/Pod binding을 검증하며 invalid profile을 publish하지 않는다. 이 동시성은 전체 profile heap 상한이 아니다.

## 실제 S와 G의 조건부 법칙

S는 Go runner IPC 교환 전체, G는 직전 교환 종료부터 다음 교환 시작까지 같은 monotonic clock의 경과다. 첫 anchor가 없으면 G는 null이다. 각 표본은 물리 TrialID와 결합을 유지한다.

현재 active 측정은 frozen operatingPoints와 off-node producer를 사용한다. sparse, saturation과 utilization의 실제 S/G와 공급, post-load idle을 구분한다. 원래 target gap으로 실제 G를 대체하거나 donor trace를 import하지 않는다.

profile은 불변 trial별 gap/service index를 한 번 만든다. gap 거리는1-nearest로 선택한다. trial을 균등 선택하고 각 trial에서 가장 가까운 모든 실제 record를 균등 선택한다. 양쪽 gap 동률도 포함한다. quantile은 같은 mixture CDF이며 tie를 평탄화하거나 평균/보간하지 않는다.

replay의 arrival과 실행 완료가 g를 결정하므로 측정 G를 지연에 다시 더하지 않는다. actor는 pack과 IPC 전 running이 될 수 있어 그 decision-age gap은 실제 G의 근사다. exchange elapsed가 있으면 실제 시작 G와 경과를 residual에 사용한다.

running residual은 원래 nearest/trial mixture에서 `S > elapsed`로 조건화하고 생존 비율의 posterior trial mass를 유지한다. nearest 집합에 생존이 전혀 없을 때만 trial별 생존 표본 안의 같은 nearest 규칙으로 재선택한다.

모든 표본이 이미 elapsed 이하이면 원래 같은-gap service를 남은 시간으로 새로 뽑는 `same-gap-service-restart` 대체를 사용한다. 실제 잔여는 더 짧거나 길 수 있고 순위 bias도 양방향이다. 보수 bound가 아니다. RunningResidualBeyondSupport와 조건부 근거를 기록한다. 원래 gap support 부재는 이 대체로 숨기지 않는다.

gateway가 관측한 service 종료는 HTTP 시작/dispatch와 최초 완료 수신 사이에 있다. native lastServiceEndAge의 하한/상한을 유지하고 admission은 범위 전체의 조건부 분위수 최댓값을 쓴다. gap 단조성은 가정하지 않는다. fill wait는 양 끝을 전진시키고 가상 service 완료 뒤 gap을 재설정한다.

표본 수의 marginal quantile coverage를 정밀 conditional tail이나 κ 안전 보장으로 해석하지 않는다. sparse와 밖 support, 선택 거리와 trial 수를 남긴다. 원시 sample과 SLO를 수정해 공백을 숨기지 않는다.

## Strict fixed-size baseline serving

수동 prepare의 `fixedBatchSize=B`를 frozen policy와 instance, metadata ACK에 남긴다.0이나 생략은 dynamic이다. B는 measured/initialized와 MaxBatch, queue cap 안이어야 하며 profiling mode는 거절한다. instance 수명 중 fixed B 변경은 교체다.

같은 UID/Definition의 B개를 stable EDF로 정확히 실행한다. B 미만은 실행하지 않는다. max wait, SLO 여유와 energy gain, drop/defer를 선택에 쓰지 않으며 overdue도 포함한다. cancel과 최종 lifetime은 적용하고 shutdown tail은 실행하지 않는다.

fixed instance는 자동 후보와 replay에서 제외한다. baseline 비교는 같은 부하의 실제 지연과 에너지다. profiling fixed K와 Serving strict baseline은 다른 실행 mode다.

## Baseline과 원자 측정

profile은 idle을 차감한 incremental J/input과 차감한 idle, raw와 memory를 보존한다. NodeBaseline은 실제 Node/mode와 loaded state, node-exclusive meter에 연결한다. 공존 additive 예상은 실측 co-loaded baseline과 구분한다.

원자 측정은 production prepare/remove/power와 idle이다. resize는 기존 primitive의 조합이다. 실제 Ready, drain과 remove, power readback과 원시 wall sample을 사용하며 표본 부족은 Insufficient다.

prepare/remove는 정확히 한 state의 추가/제거와 무추론 proof, 빈 power는 실제 다른 mode와 양쪽 empty state를 요구한다. before/target/actual과 partial, meter와 interval, sample/failure와 timing을 보존한다.

과거 비용은 보관 meter/source와 실제 Node로 읽는다. 현재 선언과 provenance 차이, reference Size나 aggregate의 byte-exact equality로 과거값을 재판정하지 않는다. 실제 bounded read, native/격리, 방향과 finite 값 검증은 유지한다.

## 자원과 저장

실물 장부는 같은 slot의 중복과 준비/정리 실물을 모두 보존한다. 현행 계획과 route는 노드당 인스턴스 하나와 인스턴스당 UID 하나다. generic 공존 표현을 현재 다중 Workload 공유 허가로 해석하지 않는다.

memory와 whole GPU UUID는 물리 안전 제약이다. CPU는 공유이며 자원 적합성이 SLO 적합성을 보장하지 않는다. timeout과 논리 stop은 할당 반환 proof가 아니다.

과부하 정책과 큐 한도는 applied Definition과 요청의 저장 계약이다. 저장 Definition의 정책 부재를 현재 기본 Reject/1024로 소급 채우지 않는다. protocol과 replay, 관측과 client, 평가 소비자는 같은 계약을 사용한다.

계약 변경은 campaign 밖에서 한 번에 적용하고 영향받는 작업용 객체를 정리한다. 게시된 과학 원문은 변경하지 않는다. observation 현행 버전은 `v1alpha7`이며 profiling과 bundle의 현재 schema는 각 owner source를 따른다. [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)에 따른 단일 reader다.

native-pair 통계와 rolling d 채택은 [측정 API](measurement-api.md#디스패치-점유-측정), 원시 조회와 crash 경계는 [관측 설계](observability-split.md)에 둔다.
