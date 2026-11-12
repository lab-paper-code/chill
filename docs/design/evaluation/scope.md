# Evaluation 범위·원칙·소유 계약

상태: 기준 문서. §2 B1~B3과 §4 순서는 2026-11-04 사용자 결정이며, 결정된 구현은 모두 보류 중이다.
정본 요구사항은 [실험 SRS](../../spec/experiments/SRS.md) EXP-01~15이고, 실행 구조는
[EvaluationRun 실행](../profiling/experiment-execution.md)과 [ADR 0032](../../adr/0032-experiment-execution-boundary.md)가 정한다.
이 문서는 둘을 반복하지 않고 evaluation 영역이 무엇을 보장하고 무엇을 쓰지 않는지만 정한다.

## 1. 목표와 완료 기준

evaluation은 CHILL을 외부 평가자처럼 실행·판정·게시해 `docs/spec/` 측정 표를 채운다.
다음이 관찰 가능하게 성립하면 완료다.

| 보장 | 관찰 기준 |
|---|---|
| 같은 입력이면 같은 판정 | 판정은 동결된 binding·client 행·원시 증거만의 순수 함수다(`hack/evaluation/judgment.py` `reduce_requests`). 같은 증거로 다시 판정하면 같은 verdict·수치가 나온다. |
| client 행 = 계획 행 | 요청마다 평가자 client 행이 하나 있다. 수집이 계획보다 적으면 `collectionLoss`로 따로 세고 `unfinishedOrUnsent`에 섞지 않는다. 분모(frozen offered)와 D1 식은 바뀌지 않는다. |
| 사람 개입 없는 종결 | 정리가 확인되면 Run이 `CleanupComplete=True`, `PoolAcquired=False/Returned`가 된다. 실제 잔여가 없는데 pool을 잡는 것은 결함이다. 실제 잔여가 있거나 정리 완료를 확인하지 못하면 pool을 유지한다. 수동 `POST .../release`는 종료된 Blocked Run에서 운영자가 원복·잔여 정리를 확인한 뒤 쓰는 복구 경로이며, cleanup 실패 기록은 남는다. |
| 재현 가능 | 게시물(`serving-experiment.v1alpha1`)에 정의·LoadScenario·seed·harness 이미지·sourceSHA·동결 설정·증거 참조가 있어 같은 Run을 다시 제출할 수 있다. 단일 seed 묶음은 반복 독립성을 주장하지 않는다(binding `limitations`). |

단계에서 전체 요약·게시로 올라갈 때 측정 판정은 `INVALID > NOT_EVALUATED > FAIL > PASS` 순서와 해당 원인을 보존한다. workload별 서술적 SLO 실패가 측정 무효를 덮지 않으며, execution·cleanup 상태와 원시 결과는 별도로 유지한다.

## 2. 블랙박스 경계

평가자는 CHILL을 시험 대상으로만 본다. 내부 상태를 판정·보정·시작 조건에 쓰지 않는다. 예외는 하나다. 실험 시작 조건에 한해 B3 gate의 점검 대상을 고를 때 CHILL이 공개한 결정(인스턴스 Ready·routed 여부)을 쓸 수 있다. 이 값은 판정·보정에는 쓰지 않는다.

### 쓸 수 있는 입력

| 입력 | 경로 | 용도 |
|---|---|---|
| 평가자가 쓴 선언 | EvaluationRun, LoadScenario(절대 rate, inline seed, datasetRefs, traceRefs) | 부하·단계·판정 조건 |
| 평가자가 낸 부하와 그 응답 | load generator client JSONL(`requestIndex`, 송신·수신 시각, `requestID`, outcome) | 발행 품질, 요청 존재, 응답 결합 |
| 공개 워크로드 정의 | `GET /api/v1/workloads/<name>` | 신원(name·uid·generation)과 적용 SLO |
| 공개 운영 설정 | `GET/PUT /api/v1/scheduler/settings`(If-Match) | 단계 조건 patch와 종료 시 원복 |
| 요청 HTTP final | `GET /api/v1/observations/raw` | 현재 D1 지연 출처. B1 적용 뒤에는 진단 참고값 |
| 선언된 전력계 | 현재 `GET /api/v1/observations/power`(`powerMeasurement.meters[].sourceIdentity`). B2 적용 뒤에는 평가자가 전력계 게이트웨이를 직접 읽음 | 에너지 적분, 공백 기록 |
| Kubernetes API(선언·실행·환경 확인) | Dataset·Workload·Preprocessor 정의, 참여 Node 신원·readiness, 참여 Node의 Pod 환경, 자기 Job/Pod(UID 기록), 대상 Pod readiness·imageID, 장애 주입 대상 | 입력 검증·preflight·환경 확인·실행·정리·주입. 판정 보정에는 쓰지 않음 |
| CHILL이 공개한 인스턴스 결정(예외) | 공개 scheduler API의 인스턴스 Ready·routed 여부 | B3 gate의 점검 대상 선택에만 씀. 판정·보정에는 쓰지 않음 |

### 쓰지 않는 입력

- κ, d, 계획기 결정·후보·Fixed/Pending 사유, scheduler state/snapshot, profile 값. 단, 위 예외(시작 조건의 점검 대상 선택)는 허용한다.
- 시작 상태 복원·내부 모드 주입(W1 cutover로 `mode`·`startState`·`control` 삭제, [measurement-api §W1](../serving/measurement-api.md#w1-외부-블랙박스-평가-cutover)).
- gateway·observer 관측으로 평가자 증거를 보충·보정하는 것.
  - 사례(2026-11-04): 한 단계에서 IC producer stdout 꼬리가 수집 중 끊겼다. 같은 요청 전체가 gateway raw에는 있었다. 결정은 gateway 행으로 client 행을 복원하지 않는 것이다. 손실은 `collectionLoss`로 드러내고, 분모와 판정식은 그대로 둔다. 원인은 conductor 수집 경로에서 고친다.

진단 기록은 허용한다. CHILL이 공개한 상태(scheduler 결정, Fixed/Pending 사유, 인스턴스·노드 상태)를 Run 근거로 남길 수 있지만, 판정·보정에는 쓰지 않는다.

### 결정 (2026-11-04 사용자)

| # | 결정 | 적용 |
|---|---|---|
| B1 | D1 지연을 client 측정으로 바꾼다. 기준은 MLPerf LoadGen 방식으로, 요청을 보내기로 예정된 시각부터 응답을 받은 시각까지다([MLPerf inference rules](https://github.com/mlcommons/inference_policies/blob/master/inference_rules.adoc)). 늦게 보낸 요청의 발행 지연도 지연에 들어간다. gateway HTTP final `elapsedNanos`는 진단 참고값으로만 남긴다. 현재 구현은 `request_analysis.py` `request_timing`이 gateway 값을 SLO 지연으로 쓴다. | 판정 규칙 변경이다. 회차 3 이후 Run부터 적용하고 judgment 계약 버전을 한 번 올린다(ADR 0019). 회차 1·2 결과는 이전 규칙으로 보존하며 다시 판정하지 않는다. 구현 보류 |
| B2 | 평가자가 전력계 게이트웨이를 직접 읽는 독립 경로를 추가한다. CHILL observation API를 거치지 않는다. | 구현 보류 |
| B3 | executor gate를 허용한다. 조건은 정확 일치 유형이 아니고, 실험과 무관한 실패로 시작 전체를 막지 않는 것이다. 현재 응답만 보고, CHILL 동작을 바꾸지 않으며, 제외한 인스턴스는 사유를 기록한다. 점검 대상은 CHILL이 공개한 Ready·routed 인스턴스로 고른다(§2 예외, 공개 `GET /api/v1/scheduler`). Node NotReady는 참여 노드(`loadGenerator.nodeName` ∪ 선언된 전력계 노드 ∪ Run의 대상 워크로드 인스턴스가 있는 노드)만 시작을 막고, 그 밖의 NotReady 노드는 기록만 한다. 점검 대상 executor가 실패하면 시작만 보류하고, 다시 새로 확인한 뒤 시작한다. | harness stage 단계 시작에서 실행하고 시도마다 `start-gate.json`을 stage 근거로 남긴다. 재확인 주기·상한은 timeboxes `watchSeconds`·`gateSeconds`, 상한을 넘기면 부하 없이 `TargetUnavailable`로 끝난다 |

## 3. 소유 계약

| 계약 | 식별자 | 위치 |
|---|---|---|
| 선언 CR | `edge.dacs.io/v1alpha1` EvaluationRun·LoadScenario·EvaluationStage(spec 불변) | `api/v1alpha1/evaluationrun_types.go`, `loadscenario_types.go`, `evaluationstage_types.go` |
| HTTP | `/api/v1/evaluations`(목록·상세·생성·Pending 교체(PUT)·삭제, `preflight`, `{name}/release`, `active`, `template`, `inputs`, `load-scenarios`(+`preflight`)), stage 읽기 projection | `internal/serving/httpapi/handler.go:162-170`, `evaluation_load.go`, `internal/evaluation/service` |
| 단계 결과 | `evaluation-step.v1alpha1` | `hack/evaluation/evaluation.py` `result_record`; controller `jobs.go` `validateResult` |
| 입력 동결 | `evaluation-snapshot.v2alpha1` / `evaluation-inputs.v2alpha1` | `hack/evaluation/input_snapshot.py` |
| binding | `e5-binding.v4` | `hack/evaluation/conductor.py` `bind`, `evidence_io.load_binding` |
| 판정 | `e5-judgment.v4` | `hack/evaluation/judgment.py` `judge` |
| 요약 | `e5-summary.v5` | `judgment.py`, `tables.py` |
| 실패 정책 | `failurePolicy: v2`(부분 관측 보존, 하나 이상하면 전체 폐기 금지) | binding·phase·archive·judgment 공통 |
| 게시 | `serving-experiment.v1alpha1`(정의 `evaluation-definition.v1alpha1`) | `hack/evaluation/publication.py` |

저장 계약 변경과 작업용 객체 정리는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md), 과거 과학 기록 보존은 [ADR 0032 Consequences](../../adr/0032-experiment-execution-boundary.md#consequences), 게시 정정은 [실험 SRS EXP-14](../../spec/experiments/SRS.md#3-요구사항)를 따른다.
additive optional 필드(예: stage `progress`·`lastProgressAt`·`loadJobs`, `collectionLoss`)는 버전을 올리지 않는다.

V1 예측 정합은 judge Job 밖에서 오프라인으로 계산한다. Q1의 compact 비교 노드 수집·기존 표는 유지하며, V1을 위한 추가 scheduler/power 서버 조회·게시 KPI는 없다. 입력은 campaign이 이미 보존한 hydrated `scheduler-observations/*.json.gz`와 fetch한 raw `obs/<hour>/{catalog,scheduler}.jsonl.gz`다. 분석기는 직전 판단을 파일에서 찾고 stage로 자른 구간·동작 구간을 중복 제거한다. power CLI는 raw를 한 번 decode하고 구간마다 기존 `ReduceOperatingPower`를 호출한다. Python은 기존 `meter_energy`와 동결 binding의 meter/coverage 정책으로 결과를 투영한다.

`$STAGES`는 `[{"id":"workload","from":"2026-11-12T02:34:30Z","to":"2026-11-12T02:49:30Z"}]` 형태다. `$PAGES`는 기존 hydrated 페이지 디렉터리, `$BINDING`은 해당 stage의 동결 binding, `$SETTINGS`/`$STATE`는 같은 실행의 accepted meter 설정/실제 Node snapshot이다. 이미 fetch했다면 첫 명령은 생략한다.

```sh
hack/observations/fetch.sh "$FROM_HOUR" "$TO_HOUR" "$OUT"
python3 hack/evaluation/prediction_analysis.py windows --scheduler "$PAGES" --stages "$STAGES" --out "$OUT/windows.json"
systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 env GOTOOLCHAIN=go1.26.4 GOFLAGS=-p=1 \
  go run ./hack/observations/power --dir "$OUT/obs" --settings "$SETTINGS" --state "$STATE" \
  --windows "$OUT/windows.json" > "$OUT/power-windows.json"
python3 hack/evaluation/prediction_analysis.py report --scheduler "$PAGES" --stages "$STAGES" \
  --binding "$BINDING" --power "$OUT/power-windows.json" --out "$OUT/prediction-analysis.json" --table "$OUT/prediction-tables.txt"
```

미리 빌드한 power 실행 파일이 있으면 `prediction_analysis.py run --scheduler "$PAGES" --stages "$STAGES" --binding "$BINDING" --out "$OUT" --power-cli /absolute/path/to/power --raw "$OUT/obs" --settings "$SETTINGS" --state "$STATE" --table "$OUT/prediction-tables.txt"`가 windows 작성·CLI 한 번 호출·결합을 수행한다. 직전 판단·meter·coverage·실제 phase가 불명확하면 UNKNOWN이며, route 뒤 post 제거 대기를 after로 취급하지 않는다. 예측 초/J는 exact `actionCosts`만 사용한다. 노드별 구간 J 오차 절댓값 합의 §6 판정은 keep SLO 충족·에너지 비교 decidable·전환 ΔJ>0일 때만 적용하고 keep·SLO 복구는 별도로 보고한다. v1alpha6에는 예측 항이 없어 정합 검증은 UNKNOWN이며 power 구간 적분만 검증할 수 있다.
Scheduler gap의 원래 `[from,to)` 범위는 보존하고 각 판단·동작 구간과 겹칠 때만 해당 행을 UNKNOWN으로 처리한다. 다른 stage에만 있는 gap은 완전한 stage의 정합 판정에 영향을 주지 않는다. gap 범위 자체가 누락·잘못됐거나 페이지 수집이 불완전하면 전체 coverage는 UNKNOWN이다.
전환 action의 `dependsOn: null`과 `dependsOn: []`는 모두 빈 의존성이다. 최종 route부터 의존성 체인을 순회할 때 같은 규칙을 적용한다.

C1(b) serving–profile 대조도 judge Job 밖의 진단 보고다.
`hack/evaluation/profile_comparison.py`는 이미 fetch한 raw request segment와
저장한 profile catalog JSON(`profiles[]`, batch별 `serviceSamples[]`)과 선택적 `--profile-raw` 측정 raw를 읽는다.
profile 값은 이 보고의 참고분포에만 쓰며, D1·에너지 판정·profile 선택을 바꾸지 않는다.
노드 전력 대조는 위 V1 경로를 그대로 쓴다.

```sh
CONTROL380=/home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/control-380-decision-fidelity-20261112
MEASURED="$CONTROL380/delay/evidence/stages/e5-feedback-excess/stage/artifacts/measured"
/home/genesis1/.local/bin/python3.11 hack/evaluation/profile_comparison.py \
  --raw "$MEASURED/obs" --clients "$MEASURED" \
  --profiles /tmp/df-l1/stored-profiles.json \
  --idle-bin-edges-ms 0,1,5,20,100,1000 \
  --out /tmp/control380-profile-comparison.json
```

`--raw`는 `obs/<hour>/requests.jsonl.gz`가 있는 obs 디렉터리 또는 개별
request JSONL(.gz)이며 여러 번 지정할 수 있다. `--clients`는 선택 사항이다.
지정하면 client JSONL 또는 measured 디렉터리의 `*-client.jsonl` requestID로
출력 배치 cohort를 고른다. 실행 중 유입은 cohort 밖을 포함한 같은 instance의
관측 요청으로 센다. provisional-inputs를 입력에 섞지 않는다.

final과 늦은 native supplement를 요청별로 합친 뒤 `(instance,batchID)`로
배치를 중복 제거한다. service 표본이 없는 부분 기록도 같은 배치의 신원·실행 시각 복구에 쓴다.
receive는 `nativeFinishedAt − nativeElapsedMS`이며, 같은 instance의
`[nativeRunningAt,nativeFinishedAt)` 안에 들어온 다른 배치 요청을 센다.
자기 배치 요청은 제외한다. 완료·elapsed가 없는 요청의 receive는 복원하지 않는다.
따라서 유입 없음은 관측된 완료 근거에 한정되고 인과를 뜻하지 않는다.
실행이 길수록 도착과 겹칠 확률도 높다(length bias). 유입별 분포 차이는 이 선택 효과를 포함한다.

출력 `cells[]`는 profile ID·배치 크기·직전 유휴 구간·실행 중 관측 유입 여부별
serving n/p50/p95와 profile 참고분포 n/p50/p95, p50/p95 비율을 보존한다.
단위는 ms, p50은 median, p95는 nearest-rank다. 구간은 `[하한,상한)`,
마지막 상한은 무한대다. 입력 edges는 `idleBinEdgesNanos`의 정수 ns로 정확히 기록한다.
`idleBinEdgesMS`는 ms 표시값이며, 셀은 반올림 없는 구간 문자열과 정수 `idleBinIndex`를 가진다.
구간 key는 정수 index다. edges는 분석 매개변수이며 스케줄러 설정이 아니다.
유휴 누락은 `unknown`·index null로 남기고 0으로 넣지 않는다.
`profileIdleBinSupported`는 해당 profile·배치·유휴 구간의 표본 존재만 뜻한다.
unknown 유휴는 측정된 유휴 구간의 support를 세우지 않는다.
`--profile-raw`가 있으면 catalog trialID·배치의 실제 service/idle 쌍을 보존하며,
native 도달 `dispatchedAt`과 `[startedAt,finishedAt)` 실행 시각으로
profile도 다른 배치 유입 여부별로 나눈다. 같은 physical trial만 사용한다.
시각 없는 과거 표본은 `profileUnconditioned`에 남고 **미조건화**로 명시한다.
`profileIngressConditioned`와 `profileIngressSupported`가 조건 일치·표본 존재를 구분하며,
`sameConditionsComparison`은 profile·배치·측정 유휴·유입 셀의 일치만 뜻한다.
해당 유휴 구간 표본이 없으면 profile n=0, 분위수·비율은 null이다.
실제 serving 이미지 등 나머지 조건의 동등성은 이 profile ID 결합으로 증명하지 않는다.

`marginalByIngress[]`는 유휴 구간을 합쳐 계획 §2.1 표를 확인하는 별도 요약이다.
위 control380 Delay excess의 genesis2 CUDA 배치 2 결과는 유입 없음
n=2,292·p50=13.6308845ms, 유입 있음 n=2,647·p50=40.010576ms,
profile n=4,819·p50=13.965722ms다. 원 prototype은 첫 요청 사본에 running 시각이
없는 16배치를 제외해 13.639424/40.090535ms를 보고했다. 새 분석기는 같은 배치의
다른 요청에서 그 시각을 복구하므로 표본 수와 중앙값이 조금 다르다.
전체 4,939배치 중 4,924배치는 유휴 1ms 미만이지만 profile에는 그 유휴 구간
표본이 없어 `profileIdleBinSupported=false`다.
보고에는 pass/fail·자동 profile 배제·보정 계수가 없다.

C1(a) profiling holdout도 오프라인 진단이다. `profile_holdout.py`는 현재
active raw v1alpha14를 같은 실행 조건의 campaign Run 집합으로 읽는다.
배치별로 묶어 각 `(batch, phase)`의 leave-one-phase-out과 physical Pod trial 전체의
leave-one-trial-out을 실행한다. 다른 배치의 training 근거도 catalog처럼 하나의 profile에 조립한다.
후자는 trial이 둘 이상일 때만 존재한다. 이용률 단계가 하나뿐이면 그 단계를 제외한
fold는 EPI 학습 자료가 없어 profile=null과 사유를 보고한다.
유휴 W와 EPI 기준은 제품 catalog가 게시하는 독립 loaded-idle v1alpha8 trial이다.
`--idle-raw`는 필수이며 active trial과 같은 실행·노드·runtime/artifact image·runtimeVersion을 요구한다.
post-load는 같은 actor의 모델 상주와 `countersBefore == countersAfter`를 확인하는 정착 진단만 쓴다.
Node `resourceVersion` 같은 출처 메타데이터는 실행 조건 비교에서 제외한다.
Python 환경에는 기존 측정 도구와 같은 SciPy가 필요하다. `scipy.stats.ks_2samp`를 사용하며 자체 검정 근사를 만들지 않는다.

```sh
systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 env GOTOOLCHAIN=go1.26.4 GOFLAGS=-p=1 \
  go build -o /tmp/chill-profile-holdout ./hack/evaluation/profile-holdout
python3 hack/evaluation/profile_holdout.py \
  --raw /archive/trial-1/raw.json --raw /archive/trial-2/raw.json --raw /archive/trial-3/raw.json \
  --idle-raw /archive/idle-1/raw.json --idle-raw /archive/idle-2/raw.json \
  --go-cli /tmp/chill-profile-holdout \
  --scheduler-settings /archive/scheduler-settings.json \
  --meter-uncertainty-watts 0.5 --rolling-window-seconds 10 --final-idle-seconds 30 \
  --bootstrap-replicates 1000 --confidence 0.95 --seed 1 \
  --out /tmp/profile-holdout-report.json
```

위 uncertainty·시간창 수치는 실행 예시이며 기기 허용치가 아니다. 전력계의 선언된
절대 불확도와 분석 시간창을 명시한다. 안정화는 `runtime.postLoad.startedAt`부터
`endedAt`까지 같은 측정 노드의 `metrics.samples`만 사용한다.
모델 상주·무추론 증거가 없는 raw는 거절하며, 관측이 부족하면 정착 시간은 null과 사유로 남긴다.
전력창은 측정 노드의 첫 native receive(`handlerEnteredAt + receivedNanos`)부터
monotonic offering exposure까지로 투영한다. 부하 노드 UTC와 meter UTC를 직접 비교하지 않는다.
이 수신 기준 창은 load offering과 전달 지연만큼 다를 수 있다.
batch·phase별 held-out service quantiles와 training profile samples의 양측 KS를 보고하고,
physical trial bootstrap으로 quantile 오차와 KS statistic CI를 낸다. batch 종속성 때문에
sample-level KS p-value는 기술 통계이며 자동 배제 기준이 아니다.

evaluation은 CHILL 시스템 밖의 작업이다. `hack/evaluation/profile_holdout.py`와
Go bridge는 보존한 raw만 읽으며 제품에 evaluation 전용 export나 trace 수집 경로를
추가하지 않는다. Python은 raw field 투영·trial bootstrap·표시를 맡는다.
Go bridge는 기존 공개 derivation API인 `MeasureIdle`, `IncrementalWorkTrial`,
`MeasurePhasePower`, `SaturationDispatchOccupancy`와 기존 `serving.EvaluateScheduler`를 재사용한다.
phase 제외 후에도 남은 native ServiceSamples의 실제 service/preceding-idle 쌍을
보존한다. EPI와 유휴 W는 trial별 추정치의 산술평균이다. d는 적격 포화 배치 쌍의
signed residual 평균이며 operational d만 0 이상으로 clamp한다.
`--scheduler-settings`는 같은 실행의 동결된 공개 `SchedulerSettings` JSON이다.
분석기가 검색·용량·동작 비용 설정을 임의로 만들지 않는다.

`profile-holdout-report.v3`는 다음 비교를 보고한다.
- held-out client 도착 `scheduledAt`, 고정 batch, native slot 수를 반영한 scheduler keep 지연 분위수와 실측 `completedAt − scheduledAt`의 같은 분위수. 긴 phase는 동결 observation window의 연속 창으로 재생하며 창별 결과와 경계 한계를 남긴다.
- phase별 실측 노드 W와 `유휴 W + EPI × scheduled rate`
- 포화 배치 쌍의 실측 cycle과 `앞 배치의 실제 S + B × d`
- 부하 중단 뒤 원시 전력 표본·rolling mean과 안정화 시간
- batch·phase condition별 held-out service와 training profile service samples의 quantiles·KS statistic·trial bootstrap CI

전력 적분은 측정 노드 수신 기준 투영창을 쓰고, rate는
`scheduledInputs / issueElapsedSecondsMonotonic`를 쓴다. 부하 노드 offering UTC를 meter와 혼합하지 않는다.
native admission FIFO 대기는 `scheduledAt`~`dispatchedAt`이며 client 지연에 포함한다.
request latency는 동결 scheduler 설정이 지원하는 여러 분위수에서 `EvaluateScheduler`로 계산한다.
설정이 제한하면 SLO 분위수만 보고하고 사유를 남긴다. 전체 replay 표본을 공개하는 새 export는 없다.
현재 scheduler 검증은 batch 1 service 근거를 요구한다. campaign의 실제 batch 1을 포함해 입력한다.
batch 1이 정말 없는 입력은 이를 합성하지 않으며 지연 예측을 null·`MissingSingleBatchEvidence` 사유로 보고한다.
신뢰구간은 독립 trial
전체를 복원추출한다. phase holdout은 같은 physical trial의 training·held-out 단계를
같은 trial draw로 함께 뽑는다. 독립 loaded-idle trial은 별도 복원추출하며 post-load를 섞지 않는다.
trial holdout의 training·held-out은 독립적으로 뽑는다. 요청이나 batch를 독립 표본처럼 재추출하지 않는다.
training trial 하나의 예측 CI와 held-out trial
하나의 실측 CI는 null이다. CI 포함 여부만 표시하며 pass/fail·배제·보정 임계값은 없다.
전력 정착은 마지막 지정 시간창의 시간가중 평균을 최종 유휴로 잡고,
rolling 시간가중 평균이 전력계 불확도 이내에 남는 가장 이른 관측 시각을 보고한다.
관측 끝까지 적어도 rolling 시간창 하나의 지속 근거를 요구하며 이후의 안정은 주장하지 않는다.

Synthetic raw 회귀: 위 Go bridge를 빌드한 뒤
`C1_PROFILE_CLI=/tmp/chill-profile-holdout python3 -m unittest discover -s hack/evaluation -p test_profile_holdout.py`.

D44 recovery gaps from each raw-hour inventory are retained verbatim in
`raw-source-index.json.gaps[]`, with API endpoint/hour provenance. Collection
`complete` means the inventory and downloads were captured, not gap-free observation.
Raw request completeness also requires no gap overlapping the requested interval.
Final judgment and publication expose `observationGaps[]` (including `[]` when
unaffected). Any overlap with a half-open offered stage or started substage window
makes that D1 `NOT_EVALUATED` / `ObservationUnavailable`; surviving finals cannot
turn unknown evidence into an SLO failure or success. Zero-offer workloads remain
`NOT_OFFERED`. `open:true` influence has no end at `clearAt`, which remains
provenance only. Non-overlapping gaps, ordinary missing finals and observed SLO
failures retain their existing judgments. Meter energy remains independently
evaluated from its own coverage. Provisional status exposes typed request metrics;
recovery provenance remains in raw snapshots and final artifacts.

The overload-admission cutover reads only judgment v4 and summary v5. Historical
artifacts retain their original meaning and are not rewritten. `outcomes` remains
the client diagnostic count. `outcomeClasses` partitions the frozen offered
population into `onTime`, `late`, `rejected`, `timeout`, `failed`, and `incomplete`.
Both successful classes require a client 2xx full-body response and a server
`completed` final with `executionComplete`. A client socket timeout followed by
native completion remains a timeout. `timeoutCauses` partitions timeouts into
`serverFinalDeadline` (when evidenced) and `clientSocket`.

`onTimeSuccessRate` uses offered as denominator. `goodputRequestsPerSecond` uses
each Workload's load length, including on-time drain completions from that arrival
cohort. Provisional `loadLengthSeconds` is clipped at that Workload's
`closedThrough` cutoff; neither drain time nor the cross-Workload wall window
extends it. `completedLatencyNS` reports only onTime+late and never decides D1.
An unstarted zero-offer Workload has zero provisional load length and unavailable
goodput. An empty started cohort retains its measured cutoff length.
`rejections[]` groups explicit `policy × stage × code`, with queue wait statistics
per group. `gatewayWaitNS` and `executorWaitNS` also report cohort queue waits.
Missing waits have no sample; actual zero waits remain samples with value zero. Missing
admission metadata never receives a retroactive default.

Producer release uses one future UTC instant written atomically into every Pod's
release file after all producers are ready. Serial execs receive two seconds per
Pod of transport lead; this is not a measurement or skew threshold. Producers wait
until that instant within the existing barrier deadline and record actual wall
and monotonic starts. A release write finishing after the instant fails with
`BarrierTimeout`. `goUTC` remains the conductor's GO time; producer observations
determine `startUTC` and the existing skew judgment remains unchanged.


ChillServer의 `--evaluation-store-name`, `--evaluation-store-endpoint`, `--evaluation-store-bucket`, `--evaluation-store-region`과 `EVALUATION_STORE_ACCESS_KEY_ID` / `EVALUATION_STORE_SECRET_ACCESS_KEY`는 함께 설정하거나 모두 생략한다.
설정하면 실험 목록·상세, evaluation template의 저장소·최근 게시, LoadScenario 입력 목록·traceRefs 조회만 전용 저장소를 사용하며 이전 저장소를 병행 조회하지 않는다.

## 4. 확장 방향

순서는 c → a → b → d로 확정했다(사용자 결정). 상태는 보류이며, E5 회차 결과가 안정된 뒤 사용자가 착수를 지시한다. 각 항목은 측정 표 하나를 채울 때만 진행한다.

| 순서 | 후보 | 현재 사실 | 필요한 것 |
|---|---|---|---|
| 1 | (c) 고정 배치 기준선 비교 | `spec.comparisons`(stage/historical 참조)는 선언·게시만 되고 harness가 비교를 계산하지 않는다. 에너지 상태는 MEASURED/PARTIAL/UNAVAILABLE 기록일 뿐 기준선 대비 판정이 없다. | 같은 LoadScenario에 고정 구성 단계(settingsPatch)를 두고 에너지·D1 차이를 게시. 에너지 판정의 전제 |
| 2 | (a) 반복 측정·편차 | binding이 단일 seed 묶음이라 반복 독립성 주장이 없다. | seed만 바꾼 반복 단계와 편차 표 |
| 3 | (b) ablation | 공개 설정 patch로 끌 수 있는 기능만 대상 | 기능 하나씩 끈 단계와 비교 |
| 4 | (d) 장애 주입 반복 | 단계당 주입 계획 실행은 구현됨(EXP-12) | 같은 주입의 반복·자동 집계 |

범위 밖:
- 빈 장비 실측, 큰 batch 측정 → profiling 영역([Profiling 색인](../profiling/README.md)).
- CHILL 배포·업그레이드, 다중 풀 → SRS §1.

## 5. 결정 권한

| eval이 정함 | 사용자 결정 |
|---|---|
| 판정식을 바꾸지 않는 harness·conductor·loadgen 결함 수정(수집, 정리, 진행 신호, 메모리) | D1 식·분모·percentile 의미, 판정 지연의 출처(B1) |
| additive 관측 필드·게시 진단(예: `collectionLoss`, stage `progress`) | 에너지 상태 규칙과 coverage 정책의 의미 변경 |
| 실행 실패 분류, residual 판정, release 조건 구현 | 블랙박스 경계 변경(§2 입력 추가·삭제, B1~B3) |
| Run spec 작성(단계, settingsPatch, timebox 값 선택) | 측정 방식 자체 변경, 노드 제외, 재측정 예외 |
| 증거·게시 포맷 내부 정리(버전 불변 범위) | 캠페인 중 저장 계약 버전 변경, 게시된 결과의 재해석 |
| §4 항목의 설계안 작성 | §4 착수 여부와 순서 |

## 6. 경계

| 상대 | eval → 상대 | 상대 → eval |
|---|---|---|
| advisor-2(serving·scheduler·profiling·kube) | 서버 기능이 필요하면 eval이 계약(요구사항·필드·검증 위치)을 정해 요청한다. 본체 코드는 직접 고치지 않는다. | 본체 변경이 공개 API·관측 의미를 바꾸면 eval에 알린다. |
| advisor-3(대시보드 실험 화면) | 화면에 필요한 데이터 계약(필드, 의미, 결측 처리)을 직접 맞춘다. | 표시 요구를 계약으로 요청한다. 판정·추론은 화면에서 하지 않는다. |
| main(실클러스터 실행·배포) | 수정 hash와 검증 결과를 전달한다. 배포는 단계 사이에만 한다. | 실행 결과·증거 경로·결함을 기록한다. |
