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

executorWaitNanos는 native enqueue부터 running이나 큐 종료까지다. queueMS는 receipt부터 센다. 실행 완료는 backendDone으로 판정한다. HTTP timeout과 전송 실패는 transportDone에만 기록한다. backendDone과 transportDone을 모두 유지한다.

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

memory request/limit은 측정 요청을 해석할 때 동결한 Actor envelope를 유지한다. peak는 실제 측정값을 사용한다. queue는 같은 limit의 measured peak 뒤 양의 headroom 안에 둔다. worker와 init에는 CPU resource를 주입하지 않는다.

ORT는 기본 자동 병렬성을 사용한다. affinity와 cgroup, quota와 실제 thread 값은 관측이다. GPU UUID, RKNN delivery와 host library, initialized batch와 실제 runtime identity는 검증한다.

capability initialization과 measured batch warmup은 별도 domain이다. 서빙 인스턴스는 실제 측정 K 전체를 같은 순서로 warmup하며 cap/fixed K만 warmup하지 않는다. 측정 세션의 인스턴스는 자기 고정 배치 하나만 초기화하고 warmup한다. initialized/measured 안의 정책 cap 증가는 재시작 없이 ACK로 적용한다. profile와 initialized capacity 변경은 재시작한다.

## 조건과 현재 측정 선택

public profile은 semantic execution condition별 최신 성공을 보존한다. 현재 측정은 model/class/path/mode 셀별 최신 항목이며 failed/partial도 현재 사실이다. explicit replacement를 recency 전에 반영하고 최신 실패를 과거 성공으로 대체하지 않는다.

PerformanceCondition은 실제 모델과 전처리, 표본 recipe, runtime image와 관측 version, Node/backend와 memory/power 조건을 유지한다. collection과 lifecycle overlay는 성능 조건과 구분하고 원래 provenance에 보존한다.

catalog는 현재 source UID/RV와 terminal 적격성을 읽는다. immutable source의 검증 raw는 재사용하고 변경과 삭제, pending은 cache에서 제거한다. 오류에 이전 success로 fallback하지 않는다.

catalog는 측정 요청마다 저장한 세션 결과 객체 하나를 읽는다. 실제 byte cap을 검증하고 읽을 수 없는 결과는 issue로 남기며 invalid profile을 publish하지 않는다. 배치 크기는 서비스 기록 둘 이상, 메모리 최댓값, 유효한 포화 수준과 식별된 입력당 줄을 모두 가질 때 profile에 싣는다. 하나도 싣지 못하면 `MeasurementEvidenceMissing`이다.

## 실제 S와 G의 조건부 법칙

S는 Go runner IPC 교환 전체, G는 직전 교환 종료부터 다음 교환 시작까지 같은 monotonic clock의 경과다. runner는 초기화한 모든 batch 크기를 warmup한 뒤 ready를 보내므로 ready 시각이 첫 anchor다. 첫 교환도 G를 가진다. G가 없는 옛 기록(시작 후 첫 실행)은 hydration에서 버리고 그 수를 센다. 각 표본은 측정 요청 UID와 결합을 유지한다.

ready actor는 ready 직전에 warmup service를 마쳤으므로 마지막 service 종료 age를 항상 가진다. native runner는 ready 시각을 마지막 종료로 두고, observer와 ChillServer 재시작 뒤 runtime은 native가 보고한 age를 그대로 쓴다. ready인데 age가 없는 instance와 G가 없는 실행 중 교환은 `UnknownNativeServiceEnd` 사유가 되어 측정 없음과 같은 경로로 처리된다. gap을 대신 고르지 않는다. 아직 실행 중이 아닌 option과 준비 중 instance는 Workload 부하가 정하는 gap(도착률 λ에서 1/λ)으로 질의한다. replay에서 prepare가 끝나면 그 시각이 마지막 종료이고, capacity 창의 합성 actor는 창 replay 시작에 ready다.

프로파일 측정은 세션이 서빙의 준비 동작과 Service 전달로 주는 부하다. 포화, 중간과 배치 크기 1의 저부하 수준의 실제 S/G와 공급, 부하 뒤 관측을 구분한다. 원래 target gap으로 실제 G를 대체하거나 donor trace를 import하지 않는다.

profile은 batch 크기마다 세션의 모든 수준 기록을 하나로 모아 gap 순서로 한 번 정렬한다. 수준 사이 편차도 스케줄러가 마주하는 분포의 일부다. 모형 이름은 `log-gap-nearest-sample-floor`다. gap g의 분위수 p는 `ln(1+g)` 거리로 가장 가까운 기록 N개의 경험 분위수다. N은 p의 표본 하한 `ceil(ln(1−p)/ln(p))`(95분위 59)다. gap은 native monotonic clock의 정수 나노초이며 그 단위 1을 더해 0 gap도 로그 척도에 둔다. N개 창은 gap 순서의 연속 구간이다. 거리 동률이면 작은 gap을 고르고, 같은 gap의 기록은 g를 향한 쪽부터 저장 순서로 고른다. N은 창의 최댓값이 신뢰도 p로 분위수를 넘는 개수이므로 창의 경험 분위수는 점추정이지 상한이 아니다. 기록이 N보다 적으면 분위수는 식별되지 않는다. 질의는 unknown이고 각 소비자는 profile 부재와 같은 경로로 처리한다. p=100은 받지 않는다.

로그 거리를 쓰는 근거: gap은 1 ms 미만부터 1 s 이상까지 여섯 자릿수에 걸치고 처리 시간의 gap 의존도 자릿수 단위로 바뀐다(GPU host b1은 10 ms 이상에서 약 2.7배). 저장된 229개 측정의 trial 단위 교차검증에서 절대 거리와 로그 거리의 95분위 적중률과 pinball 손실은 모든 gap 자릿수에서 1 % 안쪽으로 같았다. 이 데이터에서 둘은 구별되지 않으므로 척도 문제에 맞는 로그 거리를 쓴다.

분위수마다 모든 창의 값을 처음 쓸 때 한 번 계산해 최댓값 segment tree로 둔다. 한 gap 질의와 gap 범위 최댓값 질의는 이진 탐색과 tree 질의이며 heap 할당이 없다. 기록 n개, 창 n−N+1개에 대해 분위수 하나당 16(n−N+1) byte이고 150,000개 기록에서 약 2.4 MB다. 생존 조건 잔여와 표본 추출은 같은 창에서 계산한다.

replay의 arrival과 실행 완료가 g를 결정하므로 측정 G를 지연에 다시 더하지 않는다. actor는 pack과 IPC 전 running이 될 수 있어 그 decision-age gap은 실제 G의 근사다. exchange elapsed가 있으면 실제 시작 G와 경과를 residual에 사용한다.

running residual은 같은 N개 창을 `S > elapsed`로 조건화한다. 창은 넓히지 않는다. 창 안에 생존 기록이 없으면 잔여 분위수는 unknown이다.

모든 표본이 이미 elapsed 이하이면 원래 같은-gap service를 남은 시간으로 새로 뽑는 `same-gap-service-restart` 대체를 사용한다. 실제 잔여는 더 짧거나 길 수 있고 순위 bias도 양방향이다. RunningResidualBeyondSupport와 조건부 근거를 기록한다. 원래 gap support 부재는 이 대체로 숨기지 않는다.

gateway가 관측한 service 종료는 HTTP 시작/dispatch와 최초 완료 수신 사이에 있다. native lastServiceEndAge의 하한/상한을 유지하고 admission은 범위 전체의 조건부 분위수 최댓값을 쓴다. gap 단조성은 가정하지 않는다. fill wait는 양 끝을 전진시키고 가상 service 완료 뒤 gap을 재설정한다.

표본 수의 marginal quantile coverage를 정밀 conditional tail이나 κ 안전 보장으로 해석하지 않는다. sparse와 밖 support, 선택 거리와 trial 수를 남긴다. 원시 sample과 SLO를 수정해 공백을 숨기지 않는다.

## Strict fixed-size baseline serving

수동 prepare의 `fixedBatchSize=B`를 frozen policy와 instance, metadata ACK에 남긴다.0이나 생략은 dynamic이다. B는 measured/initialized와 MaxBatch, queue cap 안이어야 하며 profiling mode는 거절한다. instance 수명 중 fixed B 변경은 교체다.

같은 UID/Definition의 B개를 stable EDF로 정확히 실행한다. B 미만은 실행하지 않는다. max wait, SLO 여유와 energy gain, drop/defer를 선택에 쓰지 않으며 overdue도 포함한다. cancel과 최종 lifetime은 적용하고 shutdown tail은 실행하지 않는다.

fixed instance는 자동 후보와 replay에서 제외한다. baseline 비교는 같은 부하의 실제 지연과 에너지다. profiling fixed K와 Serving strict baseline은 다른 실행 mode다.

## Baseline과 원자 측정

profile은 노드 전력의 네 값(정착한 유휴 전력, 깨어 있는 전력 증가, 유지 시간, 배치 크기별 입력당 줄)과 메모리 최댓값, 서비스 기록을 보존한다. 노드 전력은 `유휴 전력 + 깨어 있는 전력 증가 × 깨어 있는 시간 비율 + 입력당 줄 × 입력률`이다. 구하지 못한 값은 식별되지 않음으로 남고, 그 값이 필요한 에너지는 비교하지 않는다. NodeBaseline은 실제 Node/mode와 loaded state, node-exclusive meter에 연결한다. 공존 additive 예상은 실측 co-loaded baseline과 구분한다.

원자 측정은 production prepare/remove/power와 idle이다. resize는 기존 primitive의 조합이다. 실제 Ready, drain과 remove, power readback과 원시 wall sample을 사용하며 표본 부족은 Insufficient다.

prepare/remove는 정확히 한 state의 추가/제거와 무추론 proof, 빈 power는 실제 다른 mode와 양쪽 empty state를 요구한다. before/target/actual과 partial, meter와 interval, sample/failure와 timing을 보존한다.

과거 비용은 보관 meter/source와 실제 Node로 읽는다. 현재 선언과 provenance 차이, reference Size나 aggregate의 byte-exact equality로 과거값을 재판정하지 않는다. 실제 bounded read, native/격리, 방향과 finite 값 검증은 유지한다.

## 자원과 저장

실물 장부는 같은 slot의 중복과 준비/정리 실물을 모두 보존한다. 현행 계획과 route는 노드당 인스턴스 하나와 인스턴스당 UID 하나다. generic 공존 표현을 현재 다중 Workload 공유 허가로 해석하지 않는다.

memory와 whole GPU UUID는 물리 안전 제약이다. CPU는 공유한다. SLO 적합성은 요청 재생으로 별도 판정한다. 할당 반환은 실제 실물 종료와 정리 증거로 확인한다.

과부하 정책과 큐 한도는 applied Definition과 요청의 저장 계약이다. 저장 Definition의 정책 부재를 현재 기본 Reject/1024로 소급 채우지 않는다. protocol과 replay, 관측과 client, 평가 소비자는 같은 계약을 사용한다.

저장 형식 변경은 측정과 측정 사이에 한 번 적용하고 영향받는 작업용 객체를 정리한다. 게시된 과학 원문은 변경하지 않는다. observation 현행 버전은 `v1alpha7`이며 profiling과 bundle의 현재 schema는 각 owner source를 따른다. [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)에 따른 단일 reader다.

native-pair 통계와 rolling d 채택은 [측정 API](measurement-api.md#디스패치-점유-측정), 원시 조회와 crash 경계는 [관측 설계](observability-split.md)에 둔다.
