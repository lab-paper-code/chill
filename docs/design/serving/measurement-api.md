# 측정 API와 근거 소비

이 문서는 동작 비용, 디스패치 점유, 프로파일 재사용과 공개 측정 조회를 정한다. 노드별 승인과 실제 반환은 [측정 승인](measurement-admission.md), 실행 조건은 [실행기와 측정](scheduler-runtime-measurement.md), 판단은 [계획기](analytic-planner.md)를 따른다.

## 소유권과 근거 경계

`internal/serving/measurement`가 occupancy intent 해석, 동결 객체와 제출, BEO와 DOM 삭제를 소유한다. producer 종료, 소유 evidence 탐색과 공유 참조 보호, evidence-before-native 삭제를 같은 owner에서 처리한다.

HTTP는 인증과 strict decode, 응답을 맡고 composition root가 기존 mutation mutex와 accepted profile, configuration decoder와 timeout을 주입한다. 별도 측정 writer나 scheduler 권한을 만들지 않는다.

노드 하나의 root는 순차, 독립 Node는 병렬이다. 대표 측정의 생성, 실행, raw와 projection, planner 소비와 실제 반환을 확인한 뒤 대량 실행한다. native phase, evidence 판정, scheduler 채택과 화면 assessment를 별도 사실로 보존한다.

## OperationCostMeasurement

`edge.dacs.io/v1alpha1`의 namespaced CR이며 spec은 불변이다. 공개 Query는 list/detail을 제공하며 실제 측정은 승인된 native CR에서 시작한다.

| 필드 | 계약 |
|---|---|
| `spec.node` | Node 이름,1..253자 |
| `spec.observation.intervalMilliseconds` | 양수 sampler 간격 |
| `requestTimeoutMilliseconds` | 양수이며 interval보다 작음 |
| `maximumOperationSeconds` | pre/post capture를 제외한 physical action 상한 |
| `maximumGapMilliseconds` | 결측 민감도에 사용할 원래 긴 gap |
| `timing` | 필수 empirical timing과 출처 |
| `spec.steps` |1..32개, name을 key로 한 ordered map list |
| step `kind` | power, prepare, idle, remove |
| step별 입력 | powerMode, 같은 namespace BEO, idleDurationSeconds 중 kind의 항목만 |
| status | phase와 reason, 저장 순서의 `{name,key,verdict}` |

timing은 decimal 문자열 `changeIntervalP95Seconds`, `maximumRequestSeconds`, `maximumReceiptGapSeconds`와 source다. 합을 runtime ns로 올림하여 위상 여유 A를 만든다. 동일값 dwell이나 임의 배수를 더하지 않는다.

admission과 같은 제어 경계에서 run과 Measuring을 등록하고 production Measure를 steps 순서로 호출한다. ID는 `<CR UID>.<순번>.<name>`이다. prepare는 현재 검증된 BEO preparation, remove는 그 exact 실물이다. power는 기존 Node power writer와 readback을 쓴다.

첫 오류나 non-Accepted에서 멈추고 개별 원본과 verdict, sequence를 보존한다. 재시작으로 끊긴 run은 ChillServerRestart와 저장 prefix를 남기고 반환한다. terminal 결과를 재측정하지 않는다.

### 에너지와 조건부 민감도

실제 동작창 `[a,b]`와 piecewise-linear 관측 전력 q에서 `Ehat=∫[a,b]q(t)dt`다. `[a−A,b+A]` 실제 support를 확보한다.

- `S=max |∫[a,b]q(t+δ)dt−Ehat|`, `δ∈[−A,A]`. 모든 knot와 내부 stationary 극값을 검사한다.
- 확장 support와 교차하는 실패/누락과 maximumGap 초과 원 gap의 전체 폭 합집합을 한 번 센다.
- `G=최대 관측 W × min(b−a,원 gap 합집합 길이)`, `B=S+G`.

energyUncertaintyJoules, phaseSensitivityJoules와 missingGapJoules는 각각 B, S와 G다. `B/abs(Ehat)`는 보고 비율이며0과 비유한 비율은 undefined다.10%는 Accepted gate가 아니다.

Accepted는 실제 effect와 격리, 무추론, boundary와 확장 support, 유효 sample을 요구한다. 이 민감도는 관측 support와 Pmax 조건부다. 센서 교정이나 전체 물리 오차의95% 구간을 보증하지 않는다.

planner는 nominal 평균을 유지하고 동일 immutable reference/trial의 signed 노출을 먼저 상쇄한다. 잔여 B 절댓값 합보다 이득이 클 때 에너지 이동한다. 같으면 keep이며 keep 미충족 회복은 별도 순서를 따른다. profile 통계 CI를 이 문턱에 더하지 않는다.

## 디스패치 점유 측정

신규 DispatchOccupancyMeasurement는 configured 짧은 closed-loop burst다. legacy Accepted scalar DOM은 별도 저장 계약으로 보존한다.

### 공개 생성과 삭제

`POST /api/v1/namespaces/{namespace}/dispatch-occupancy-measurements`는 다음 intent만 받는다.

```json
{"name":"occupancy-example","node":"target-node","batchEnergyOptimization":"profile-source","workload":"target-workload","batch":1}
```

name은 선택이다. 서버가 같은 수락 configuration revision의 dispatchOccupancy 정책과 accepted profile로 전체 native spec을 동결한다. caller protocol 필드는 거절한다. 불가능한 조건은400 OccupancyInfeasible, 정책 부재나 오류는503 OccupancyPolicyUnavailable이다.

정상 제출은 `202 {request:"Accepted",task:<DOM>}`다. `?dryRun=All`은 native 생성과 physical effect 없이 `200 {request:"NOT_SUBMITTED",task:<동결 DOM>}`을 반환한다. 수락 뒤 측정 완료와 과학적 Accepted를 별도로 판정한다.

DELETE는 항목 URL의 `?uid=<UID>`와 `If-Match` RV가 필요하다. Measuring이나 살아 있는 owned load Job/Pod는409다. producer 종료와 공유 참조를 확인하고 자기 unshared evidence부터 삭제한 뒤 native CR을 삭제한다. partial 삭제 실패는 rollback하지 않는다.

Bearer TokenReview와 정확한 URL SAR, namespace allowlist와 native resource SAR를 모두 적용한다. POST create는 collection scope, DELETE는 URL 이름의 delete다. 삭제 lookup과 evidence 정리는 owner SA가 수행하며 caller get을 추가 권한으로 요구하지 않는다.

BEO 생성과 삭제도 같은 두 권한 층과 producer/evidence 보호를 사용한다. 순차 탐색 실패의 정리는 현재 generation Failed와 MeasurementComplete=False를 확인한다. 이름과 label만으로 소유를 추정하지 않는다.

### 동결 protocol과 실행

batchEnergyOptimization, node, workload와 batch는 exact profile과 현재 물리 Node, Dataset evaluation 입력을 연결한다. supplyMode와 workers, warmupSeconds, durationSeconds와 minimumPairs는 명시 정책이다.

request lifetime과 delivery, preparation과 Job setup, collection과 drain, planned elapsed cap을 동결한다. sample의 burst15초, warmup5초, workers8과 minimumPairs100은 보편적 정밀도 보증이 아니다.

고정 B와 maxBatchWaitMS0의 accepted instance를 준비하고 공통 TLS gateway와 projected token, CA로 부하를 공급한다. native queue와 수명, profile tail과 전체 예산을 대조한다. 공급과 watchdog의 모든 구간이 frozen cap에 맞아야 하며 자동 clamp와 retry, 연장은 없다.

Job의 half-open receipt cohort를 requestID로 결합한 뒤 최초 direct native start에서 warmup을 전진시켜 D초 native window를 정한다. 생성시각을 측정 시작으로 쓰지 않는다. cleanup과 physical drain, 격리 witness를 원본에 보존한다.

### 공통 native-pair estimator

read-only GET과 DOM, rolling 집계는 같은 factual estimator를 사용한다. assigned/final/late-native를 RequestID로 결합하고 충돌하는 identity와 timestamp, 중복 final은 invalid로 처리한다.

physical batch는 exact instance와 BatchID로 식별한다. 전체 unique member가 성공과 execution-complete, 같은 configuration과 시작/종료 identity, 직접 queued/running/finished 시각을 가져야 한다.

인접 batch의 두 start는 native half-open window 안이어야 한다. 같은 profile와 decision/definition, fixed B와 no-wait 정책이 앞 start 전에 ACK됐음을 historical metadata로 확인한다. 모든 다음 member가 앞 finish 전에 queued여야 한다.

`max(next.NativeQueuedAt) ≤ previous.NativeFinishedAt ≤ next.NativeRunningAt`

signed 표본은 다음과 같다.

```text
d_i = (next.NativeRunningAt - previous.NativeRunningAt
       - previous.ServiceSample.ServiceDurationNanos) / previous.BatchSize
```

앞 배치의 실제 Go runner exchange S를 뺀다. profile 평균이나 gateway offset으로 대체하지 않는다. 누락 batch와 gap, restart, overlap과 변경 경계를 건너 pair를 만들지 않는다.

statistics는 signed ns/request의 mean, median, p25/p75와 IQR, p10/p90과 negativeFraction이다. 실제 표본을 정렬하고 `roundToEven((n−1)×p)` index를 사용한다. 보간과 negative censoring은 없다.0 pair는 statistics:null이다.

rawRows와 completeRawRows, enrichedCompleteRawRows, finalRows와 completeFinalRows, joined requests와 complete requests, batches와 completeBatches, eligible pairs를 별도 분모로 남긴다. open snapshot과 supplement를 추가 raw 요청으로 세지 않는다.

burst는 minimumPairs 미만이면 Insufficient/TooFewPairs로 통계를 보존하고 나머지는 Observed와 native Complete다. 포화, idle fraction과 CI gate를 추가하지 않는다. load와 retention, drain과 격리 실패는 실제 사유와 데이터를 남긴다.

## Scheduler의 d 선택

정확한 Node UID, 모델, mode, path, batch와 profile ID별 선택 순서는 qualified rolling median, legacy Accepted DOM, configured `defaultDispatchOccupancyNanos`다. legacy는 기록된 runtime image 문자열도 맞춘다. tag와 digest reference를 문자열 그대로 비교한다.

rolling은 UID 사이 실제 pair 표본을 같은 exact cell에 pool하며 median의 median을 쓰지 않는다. 운영값만 `round(max(median,0))`로 만든다. signed raw와 진단은 그대로다. scalar default는 모든 batch 공통이며 dispatchOccupancy 블록이 이를 shadow하지 않는다.

worker 하나가 Tick과 writer lock 밖에서 scan과 reduce한다. selected-ID cap은 estimator allocation 전에 적용한다. scan은 겹치지 않고 cooldown은 scan+reduce 이상이다. timeout은 cooperative이며 실패와 cap 초과는 partial/stale measured를 폐기하고 DOM/default로 돌아간다.

Tick은 atomic cache만 읽는다. source, pairs와 window, updatedAt과 value의 provenance를 수치 입력과 판단에 남긴다. value 변경은 검사 결과를 무효화하며 metadata-only 변경은 같은 전체 수치 입력의 결과를 재사용할 수 있다.

`GET /api/v1/observations/occupancy?from=...&to=...`는 fresh 진단이다. from과 to 각각 하나, 양수 구간이며 accepted aggregationWindowSeconds 이하여야 한다. 초과는 scan 전400과 정수 maximumWindowSeconds다. 긴 과거 분석은 raw export와 offline reducer를 쓴다.

bounded scan은 수명 cap과 drain allowance의 tail 안 segment만 두 번 읽어 선택 ID와 supplement를 연결한다. raw granularity는 minute나 gzip hour이며 밖 evidence를 가짜 완료로 만들지 않는다. operation timeout과 caller 취소를 지킨다. request count cap은 byte/RSS ceiling이 아니다.

private `GET /api/v1/scheduler/catalog-read`의 accepted.dispatchOccupancy가 실제 d 선택의 권위다. accepted.occupancyEstimates는 signed 관측이며 accepted.occupancy는 legacy evidence다. burst Observed 자체를 scalar Accepted나 scheduler 채택으로 승격하지 않는다.

## 같은 기종 프로파일 재사용

요구사항은 [같은 기종 프로파일 재사용 조건](../../spec/catalog/SRS.md#같은-기종-프로파일-재사용-조건)을 따른다. 대상 BEO의 `chill.dacs.io/profile-reuse` annotation은 JSON이다.

| 필드 | 검증 |
|---|---|
| sourceProfileID | 같은 namespace의 유효 직접 측정 ID |
| maximumRelativeDifference | 필수 유한 `(0,1)` |
| minimumMeasurementSeconds | 선택, 유한 양수, 기본30 |
| minimumTrials | 선택,2 이상, 기본3 |

unknown 필드는 거절한다. 자기 참조와 전이 재사용, 삭제와 replacement로 무효화한 source는 거절한다. 실패 confirmation의 annotation을 지워 짧은 측정을 독립 profile로 승격하지 않는다.

동결 performance condition은 실제 recipe와 runtime image 문자열, 관측 version, DeviceClass와 power/backend, 모델과 전처리, Dataset을 비교한다. Node/meter와 GPU 물리 identity, Workload별 이름/UID/RV와 SLO/count, 창 길이는 대상별 비교에 맞춰 정규화한다. 정규화가 actual physical 검증을 면제하지 않는다.

source의 모든 measured batch가 confirmation에 있어야 한다. 서비스 raw 평균, trial별 활성 wall power의 산술 평균, loaded idle power의 상대 차이를 각각 검사한다. 분모 비양수, 결손과 비유한, 조건 불일치나 허용차 초과는 Rejected다. 기본 허용차 예시0.05를 SLO나 전체 오차 bound로 해석하지 않는다.

Accepted와 Rejected 모두 content-addressed immutable reuse-check를 저장한 뒤 catalog에 발행한다. annotation과 source validity 변경은 새 판정이며 옛 record를 덮지 않는다.

대상 파생 ID는 source ID와 상대차, 최소 측정초와 trial 정책을 포함한다. 대상 Node UID와 meter, GPU UUID와 runtime metadata/delivery, idle은 대상 근거다. 서비스 분포와 증분 J는 source, memory는 양쪽 최대다. d와 prepare/remove/power 비용은 대상별 별도 측정이며 source 비용을 복제하지 않는다.

## 공개 Query와 권한

Query가 집계, 정적 호환성, assessment와 retained result를 소유한다. ChillServer는 인증과 namespace/resource SAR 뒤 완성 응답을 opaque proxy한다. owner catalog의 accepted와 reporting은 별도이며 Query가 새 scheduler 선택을 만들지 않는다.

| GET 경로 | 응답 책임 |
|---|---|
| `/api/v1/model-compatibility` | modelSpec, executionPath, deviceClass, node 각각 하나 필수인 정적 판정 |
| `/api/v1/model-compatibility-matrix` | query 없는 inventory 행렬, 대표 rows와 실제 nodeRows |
| `/api/v1/namespaces/{namespace}/batch-energy-optimizations` | native spec/status와 assessment |
| `/api/v1/namespaces/{namespace}/profiling-runs` | 정확 owner와 제한 labels, 회차와 assessment |
| `/api/v1/namespaces/{namespace}/operation-cost-measurements[/{name}]` | CR와 exact step 결과 |
| `/api/v1/namespaces/{namespace}/dispatch-occupancy-measurements[/{name}]` | 원 phase, ref와 burst/legacy evidence, assessment |
| `/api/v1/namespaces/{namespace}/measurement-coverage` | 현재 측정 셀과 근거 집계 |
| `/api/v1/scheduler/profiles` | 공개 profile와 preparation, 결과 행과 insufficiency |

목록과 단건은 native list/get SAR를 각각 요구한다. coverage는 정확 URL GET에 더해 같은 namespace BEO, ProfilingRun과 DOM list SAR를 요구한다. 인증 오류나 권한 거절에 cache 자료를 반환하지 않는다.

정적 compatibility는 Compatible/Incompatible/Unknown이다. Ready와 측정 완료, admission 성공은 별도로 확인한다. 없거나 무효인 선언은 Unknown과 이유다. 같은 scope의 마지막 matching declaration을 사용하고 앞선 일치는 비차단 진단으로 남긴다. 대표 Node를 다른 Node 지원으로 복제하지 않는다.

Run의 controller owner는 같은 namespace, apiVersion과 kind, exact BEO UID로 결합한다. 이름과 긴 이름의 축약 label만으로 소유를 확정하지 않는다. 원 attempt와 batch/mode를 유지하며 여러 배치를 새 회차 번호로 다시 매기지 않는다.

## 저장 결과의 읽기 projection

BEO result의 sealed detail은 바꾸지 않는다. statistics와 measurementFacts를 형제로 제공하고 retained verified trial과 CurvePlan의 기존 derivation을 사용한다.

batch별 incremental J/item은 독립 trial 추정의 산술 평균이다. 기존 shared-idle와 Student-t CI의 level과 halfWidth, 음수 하한과0폭을 보존한다. latencyP95Milliseconds는 같은 batch의 모든 active client 호출 sample을 pool한 empirical p95다. trial percentile 평균이나 native S로 대체하지 않는다.

NoPublishedResult, RetainedEvidenceMissing과 StatisticsNotRecorded를 구분한다. 실제 권한과 transport, store와 identity 오류를 미기록으로 숨기지 않는다. 단건 통계 때문에 coverage 전체의 raw를 다시 내려받지 않는다.

OCM은 status의 정확 step key만 읽는다. durationNanos는 실제 시작부터 끝까지의 경과다. wallJoules는 저장 Accepted 전체 Node wall J다. 실패에 시간만 있으면 시간만 표시하고 후속 미실행 행을 만들지 않는다. CR별 최대8 projection을 병렬 수행하되 이름순과 step 순서를 유지한다.

ResultRow는 exact variant와 batch의 p95, CI, idle/active W와 total/incremental J, trial과 sample 분모, power source와 원본 ref를 보존한다. active W는 같은 trial의 유효 적분과 창에서 구한다. source 재사용 행과 대상 confirmation 행은 분리하고 혼합 total J를 만들지 않는다.

## 측정 커버리지

경로는 namespaced measurement-coverage다. query는 batches와 node만 허용한다. batches는 중복 없는 오름차순 양수 CSV, node는 inventory의 정확 이름이다. 미지원과 중복, 빈 값은 InvalidCoverageQuery다. 필터는 base snapshot projection이며 새 raw scan을 시작하지 않는다.

schemaVersion은 `measurement-coverage.v1`, assessmentVersion은 `result-checklist.v1`이다. snapshotId, dataAsOf와 fresh/stale, complete/partial, scope와 source 오류, summary와 groups, cells와 entity 요약을 제공한다. dataAsOf는 필수 source 마지막 성공 확인의 최솟값이다.

셀은 model UID × class UID × path × mode다. 현재 측정은 명시 replacement 뒤 measuredAt 최신 하나이며 동률은 작은 ID다. 유효 profile 시각, current-generation terminal condition, zero 순으로 선택하고 creationTimestamp를 대입하지 않는다. 최신 failed/partial은 그대로 현재 사실이며 과거 성공은 history다.

variant는 실제 runtime image reference와 관측 version, recipe를 보존한다. batchProgress와 대표값도 현재 variant의 실제 batch에 한정한다. current 사용, 정적 지원, evidence와 품질은 독립 축이다. 품질 unknown을 실행 거절로 만들지 않는다.

assessment는 status, displayStatus와 label, execution과 evidence, checklist와 scope다. 서버 label을 UI가 다시 판정하지 않는다. checklist는 variant에 한 번만 전체를 두고 동일 항목 sourceIds를 합친다. cell과 result row는 criteriaMet와 version을 유지하며 checks를 비운다. resultRows와 variants를 용량 때문에 자르지 않는다.

체크는 exact lineage, 목표 batch와 독립 active/shared idle 각각 `max(3,MinimumTrials)`, 유한 CI와 pooled client latency, 동결 power policy와 source를 확인한다. 미조회는 unknown이다. PolicyUnset은 정책 미설정을 나타낸다. source timestamp 부재와 node-wall scope를 새 실패 gate로 쓰지 않는다.

failureSummary는 exact owner Run UID와 조건, 실제 exit와 안전한 한 줄 근거를 사용한다. 명시 CUDA OOM과 CPU/host OOM, power timeout, image pull과 runtime 실패를 구분하며 일반 allocator 문구만으로 GPU 원인을 단정하지 않는다. 원시 로그와 미확정 인과를 보존한다.

coverage occupancy의 present는 current exact profile와 Node의 검증 legacy Accepted 또는 burst Observed다. scheduler adopted는 accepted.dispatchOccupancy provenance의 measured/dom이면 true, default와 확인된 선택 부재면 false, catalog나 provenance 미확인이면 null이다.

관측 signed d(B)와 실제 선택 d를 따로 표시한다. DOM 요청 없이 rolling 관측과 선택만 있는 tuple도 생성할 수 있다. 현재/과거 physical UID와 profile을 합치지 않는다. 한 DOM evidence 실패는 해당 행의 문제로 남기며 무관한 profile 결과를 숨기지 않는다.

namespace cache는 fresh30초, stale 최대5분이며 갱신 하나를 공유한다. 초기화는503 CoverageWarming과 Retry-After2다. 일부 source 실패는 마지막 성공 시각과 partial/stale를 유지한다.5분 초과 요청은 같은 in-flight 갱신을 기다린 뒤 유효 입력이 없으면503 CoverageUnavailable이다.

응답은 no-store다.8MiB를 넘으면 오류이며 임의 행 생략은 없다. 실제 배포 성능과 과학적 수용 결과는 이 계약만으로 달성 처리하지 않는다.

## 페이지와 무손실 응답

profiles는 기본25, 최대50의 limit와 offset을 사용한다. 정확 model/deviceClass/profile와 전체 snapshot 필터/정렬 뒤 page를 만든다. 선택지와 resultRows, 출처를 유지하고 raw duration vector는 공개하지 않는다.

BEO native 목록은 limit 또는 offset을 명시할 때만 page를 제공한다. limit1..50, 기본25, offset0..1000000이다. 무페이지 요청은 전체 목록이다. 중간 실패와 취소를 완전 목록 성공으로 표시하지 않는다.

`view=summary&summary=v2`는 명시한 무손실 dictionary 인코딩이며 기본 v1과 구분한다. 정밀 정수와 null, 실제0과 모든 유지 필드를 복원한다. profile `$ref`와 measurement summary dictionary를 같은 계약으로 해석하지 않는다.

## 실험 조회와 저장 계약

`GET /api/v1/experiments[/{id}]`는 불변 게시를 읽고 KPI를 현재 상태로 보충하거나 재판정하지 않는다. 목록 정렬은 from 내림차순과 ID다. 명시 limit/offset은 최대50/1000000, 기본25/0이며 미지정은 전체다.

Evaluation의 공개 procedure와 stage progress, loadJobs는 기록된 사실이다. planned 시간이나 Job phase로 누락 boundary를 합성하지 않는다. closed-minute progress와 drain 시점 producer missing은 최종 SLO 판정과 다르다. 실행 소유권은 [실험 실행 설계](../profiling/experiment-execution.md)를 따른다.

| 근거 | schema와 prefix |
|---|---|
| operation | `chill.dacs.io/serving-operation-measurement.v1alpha2`, `serving/operation-measurements/<Node UID>/<operation ID>.json` |
| 미완료 sequence | `serving-operation-sequence.v1alpha1`, `serving/operation-sequences/<CR UID>.json` |
| legacy scalar DOM | `chill.dacs.io/dispatch-occupancy-measurement.v1alpha1`, `serving/dispatch-occupancy/` |
| burst DOM | `chill.dacs.io/dispatch-occupancy-measurement.v1alpha2`, `serving/dispatch-occupancy-observations/<NodeUID>/<CRUID>.json` |
| reuse check | `chill.dacs.io/profile-reuse-check.v1alpha1`, `serving/profile-reuse/` |
| experiment | `serving-experiment.v1alpha1`, `serving/experiments.v1alpha1/` |

## 저장 계약

신규 burst와 legacy scalar의 reader와 prefix를 합치지 않는다. 누락 Timing과 B를 관측0으로 채우지 않는다. source별 판정, signed 원본과 immutable evidence를 보존한다. [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.
