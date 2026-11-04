# 선언형 operation 비용 측정과 디스패치 점유 측정

캠페인은 CHILL 측정 API를 사용한다. 노드별 승인·반환은 [측정 admission](measurement-admission.md), 실행·배포 근거는 [handoff](../../handoff.md)를 따른다. 이 문서는 측정량, 조건 생성, 판정과 계획기 소비를 정하며 원시 자료, signed 값과 원래 판정을 보존한다.

`internal/serving/measurement` owns occupancy intent resolution, frozen-object
construction, native measurement submission, and BEO/occupancy retirement.
Producer-shutdown checks, source-owned evidence traversal, shared-reference
guards, and evidence-before-native deletion ordering stay together in that owner.
`cmd/chill-server` only constructs its clients, bounded configuration and QueryAPI
transport, and supplies the scheduler's existing mutation mutex, accepted-profile
snapshot loader, full configuration decoder and gateway timeout explicitly.
The measurement owner does not reach into scheduler service internals or acquire
a separate mutation authority. HTTP keeps authentication, decoding and response
mapping; its intent/result types come from the measurement owner without aliases.

## 1. 측정 원칙

- operation 비용은 준비, 유휴, 해제와 빈 노드 전력모드 변경을 순서 있는 요청 하나로 측정한다. 동작별 불변 기록은 요청과 실제 physical identity를 가리킨다.
- 디스패치 점유 d는 인접한 고정 배치의 native 실행 시작 간격에서 profile 평균 실행시간을 뺀 signed ns/request다. 관측·채택 연결 키는 Node UID·모델·전력모드·실행 경로·배치·profileID이며 runtime image는 기록·표시만 한다.
- 같은 노드는 요청 순서대로, 다른 노드는 병렬 측정한다. admission이 CPU·가용 자원·profile을 확인하고 거절 사유를 기록한다.
- 대표 한 건의 조건 생성→실행→원시 기록→계획기 선택→실물 반환을 확인한 뒤 대량 실행한다. profile/runtime/측정 계약 변경에는 영향받는 d·비용·baseline 목록을 남긴다.

## 2. OperationCostMeasurement (`opcost`)

`edge.dacs.io/v1alpha1`, Namespaced, status subresource, spec 불변(`self == oldSelf`). printer column: Phase, Node, Reason, Age. `spec.node`는 Node 이름(1-253)이다.

| 필드 | 형식 |
|---|---|
| `spec.observation` | `intervalMilliseconds`, `requestTimeoutMilliseconds`, `maximumOperationSeconds`, `maximumGapMilliseconds`, 필수 `timing`. timing의 문자열 `changeIntervalP95Seconds`, `maximumRequestSeconds`, `maximumReceiptGapSeconds`, `source`로 실측 입력을 고정한다. 원 gap은 결측 민감도 근거다 |
| `spec.steps[]` | 1-32개, `listType=map` key `name`. DNS label `name`, `kind`(`power`, `prepare`, `idle`, `remove`). `power`는 `powerMode`, `prepare`/`remove`는 같은 namespace BEO 이름 `batchEnergyOptimization`, `idle`은 `idleDurationSeconds`(≥1)만 갖는다(CEL) |
| `status` | `phase`(`Measuring`, `Complete`, `Insufficient`, `Failed`), `reason`, `steps[]{name,key,verdict}` |

**실행.** 승인과 같은 `tickMu`에서 노드 run과 `Measuring`을 등록한다. 순서 ID는 CR UID다.
- 단계별 `MeasurementRequest`의 `Operation.ID=<UID>.<순번>.<name>`이며 meter/store는 비워 노드 설정으로 채운다. `power`는 `nodePowerTargets`/`powerRequest`의 NodeActuation에 CR owner를 달아 반환의 foreign-actuation 검사에서 자기 단계를 제외한다. `prepare`는 BEO 현재 UID의 profile preparation, `remove`는 그 인스턴스다.
- 기존 `Measure`를 순차 호출하며 key·형식·witness를 유지하고 단계별 key/verdict를 `status.steps`에 추가한다. 실패 또는 non-`Accepted`에서 멈추고 `serving/operation-sequences/<UID>.json`을 쓴 뒤 반환한다([admission §2.5](measurement-admission.md#25-operation-비용-측정과-serving-operation)).
- 실제 operation/effect·격리·서빙 추론 0·원시 구조·실제 양쪽 bracket·확장 support를 확인한 에너지는 `Accepted`다. 정밀도 비율은 보고용이다. 동작 실패, 격리 오류, boundary 누락, invalid record의 사유·부분 근거와 원래 값/판정을 보존하며 실행 실패는 `Failed` 수명주기로 기록한다.

### 2.1 전력 곡선과 민감도

실제 동작창 `[a,b]`, 기록된 piecewise-linear 전력곡선 q에서 `Ehat=∫[a,b]q(t)dt`, 비용은 `Ehat ± B`다.
- 위상 여유 `A=p95(전력값 변화 간격)+최대 요청 지연+최대 유효 수신 gap`의 원 십진 문자열·출처를 고정하고 실행시간 변환은 합을 nanosecond로 올림한다. 승인 실측 추정은 Gen3 1.6079805965 s, Plus 4.518180026700001 s다. 미측정 Gen3 6개의 계열 전이는 추정 한계다. 동일값 유지 길이·추가 배수는 A에 포함하지 않는다.
- `[a−A,b+A]`의 실제 표본을 확보한다. `S=maxδ∈[−A,A]|∫[a,b]q(t+δ)dt−Ehat|`를 모든 knot와 구간 내부 stationary 극값에서 계산한다. 상수 전력은 S=0이고 상수 offset에 불변이다.
- 실패/누락 요청을 포함한 원 gap과 `maximumGap` 초과 valid-to-valid 원 gap 중 확장 support와 교차하는 구간의 **원래 전체 폭** 합집합을 한 번 계산한다. `G=이번 record의 최대 관측 W×min(b−a,합집합 길이)`, `B=S+G`. A 확장은 support에 한 번만 적용한다. G는 미관측 전력이 이번 최대 관측값 안이라는 경험적 조건이다.
- `energyUncertaintyJoules=B`, `phaseSensitivityJoules=S`, `missingGapJoules=G`. `energyUncertaintyRatio=B/abs(Ehat)`이며 Ehat=0 또는 nonfinite 비율은 undefined다. 10% 지표는 보고용이며 Accepted/Insufficient gate가 아니다. `request.timing`, `operationBudgetNanos`에 조건을 보존한다.
- 이 민감도는 관측 support/Pmax 조건부이며 센서 교정 또는 전체 물리 오차의 95% 구간을 보장하지 않는다.

**운영 POOL 전력 창.** 원시 성공 표본을 합쳐 기록된 coverage 정책으로 판정한다. 현재 MaximumGap 3.0 s, MinimumCoverage 0.75, MaximumBoundaryDistance 1.0 s다. 읽기 실패 자체가 아니라 성공 표본 사이 gap이 불완전 여부를 정한다.
`readFailures`는 노드별 창 안 실패 횟수, `maximumSampleGapSeconds`는 경계 support를 포함한 인접 실제 표본 최대 간격이며 창 요약은 각각 노드 합계·노드 최대값이다. 정책을 만족하면 실패가 있어도 complete, 초과 gap은 불완전이다.
단발 실패 공백 ≤2.52 s, 연속 두 번 실패 공백 ≥3.2 s의 운영 근거에 따른 설정이며 정확 비용 등 다른 측정 정책은 바꾸지 않는다. 오프라인 `hack/observations/power --maximum-gap 3s`는 보존 정책을 수정하지 않는 명시적 재평가이고 `policyOverride.maximumGapSeconds`로 표시한다.

#### 운영 전력 시간 구간 API

`GET /api/v1/observations/power?from=<RFC3339Nano>&to=<RFC3339Nano>&bucket=5s`
는 `{from,to,bucketNanos,buckets: OperatingPowerWindow[]}`를 반환한다.
`bucketNanos`는 Go duration의 정수 나노초 값이다. `bucket`을 생략한 요청은 기존
기간 요약 `OperatingPowerWindow` 하나를 반환하며 시간축 데이터가 아니다.

`format=columns`를 bucket과 함께 명시하면 무손실
`{from,to,bucketNanos,format:"columns",windows,nodes}`를 반환한다. windows의 각 열은
bucket index, nodes의 각 열은 bucket index 안의 원래 node 순서다. 값·null/빈 배열·complete·
reasons·coverage는 원래 bucket과 같다. 시각은 `max(from, truncateUTC(from,bucket)+i*bucket)`부터
`min(to, truncateUTC(from,bucket)+(i+1)*bucket)`까지 정수 나노초로 복원한다.
columns 요청에 구 서버의 buckets 응답이나 다른 형식이 오면 `형식 불일치` 오류를 표시한다.
없는 windows를 빈 배열로 바꿔 빈 차트 성공으로 처리하지 않는다.
라이브 추세도 decodeResponse를 거친 `ApiError.body`의 보존된 파싱 원인으로 이를 표시한다.
공통 ApiError.message의 응답 미수신 문구로 형식 오류를 판별하지 않으며 원인은 tooltip에 보존한다.

- 시간 구간 요청은 `from < to`, 최대 1시간, bucket 폭 1초~1시간, 최대 900구간이다.
  경계는 UTC duration 배수에 맞추고 첫·끝 구간은 요청 범위로 자른다. 따라서
  정렬되지 않은 15분/1초 요청은 901구간이 되어 거절될 수 있다.
  잘못된 폭·범위·개수 또는 전체 raw observation bound 초과는 `InvalidQuery`다.
- 각 bucket은 실제 `from`, `to`, `scope`, `nodes`, `wholePoolJoules`,
  `wholePoolMeanWatts`, `activePoolJoules`, `activePoolMeanWatts`, `complete`,
  `readFailures`, `maximumSampleGapSeconds`, `reasons`, `coverage`, 선택적 `gapUntil`을 가진다.
  `coverage`는 `{from,to,sources,gaps}`이며 source·gap의 원래 시간과 stream을 보존한다.
  노드 행은 `node{name,uid}`, `meter`, `scope`, `joules`, `meanWatts`, `samples`,
  `meterJoules`, `meterComplete`, `gapCount`, `gapSeconds`, `gapFraction`, `sourceIdentity`,
  `readFailures`, `maximumSampleGapSeconds`, `sourceTimestampAbsent`, `reasons`와
  bucket 전용 `observedSamples`, 선택적 `firstObservedAt`, `lastObservedAt`을 가진다.
- `samples`는 적분 경계를 지지하는 창 밖 표본을 포함할 수 있다.
  `observedSamples`는 **실제 수신 시각이 `[bucket.from,bucket.to)` 안인 표본 수**다.
  `firstObservedAt`/`lastObservedAt`은 그 실제 표본의 시각이며 평균값의 측정 시각을
  새로 생성하지 않는다. 경계 support만 있고 내부 표본이 없으면 `observedSamples=0`,
  노드 `joules`/`meanWatts`와 전체·활성 집계는 `null`, `complete=false`,
  사유는 `no observed samples in bucket`이다. 관측 0 W/J는 숫자 0으로 유지한다.
- 기간 요약과 bucket은 같은 meter·coverage·사다리꼴 적분·물리 membership 규칙을 쓴다.
  불완전 여부·실패 횟수와 실제 숫자는 각각 보존하며 `null`을 0으로 치환하지 않는다.
  `meterJoules`는 물리 계측 범위의 별도 값이며 풀 membership의 joules와 같다고 가정하지 않는다.
  보존 잘림·source 혼합·source identity 결손·빈 bucket이면 `meterComplete=false`와
  해당 null/사유를 보존한다. 대시보드는 서버의 gap 초·비율·최대 간격·source identity를
  노드별 표에 표시하고 전체/활성 풀 수치와 합성하거나 null을 0으로 바꾸지 않는다.
  A sample gap beyond the recorded policy keeps the trapezoidal `meterJoules`
  estimate and reports `meterComplete=false`, its reason, and `gapCount`.
  This serving-only coverage diagnostic does not change profiling acceptance.


대시보드 운영/장치 추세는 진입 때 최근 15분·5초 평균을 한 번 조회하고,
실시간 1초 구간을 이어 붙인다. 실시간 구간과 겹치는 이력 구간은 제외하며,
각 구간의 실제 폭·시각을 유지한다. 5초 평균을 1초 표본 다섯 개로 복제하지 않는다.
관측 공백과 누락 시간은 선을 끊는다. 보관하는 실시간 구간도 15분·900개 이하로 제한한다.
기록 화면의 시간축은 bucket API로 조회하며 기간 요약/장치별 요약 표와 독립이다.
화면의 폭 선택은 1·5·30·60·300·3600초 중 해당 기간에 허용되는 것만 선택할 수 있다.
1시간 밖의 기간 기록/요약은 유지하되 시간축 요청은 보내지 않고 기간 축소를 안내한다.
시간축 bucket 요청은 `format=columns`를 사용한다. 1시간·5초 해상도도 유지하며 UTC bucket
경계에서 최대 15분 요청으로 나눈다. 첫·끝 clipped bucket을 중복하거나 나누지 않고 각 응답을
합친다. 1시간 폭처럼 15분보다 큰 단일 bucket은 쪼개지 않는다. 일부 chunk의 오류·대기는 전체
시간축의 오류·대기로 표시하며 성공한 chunk만 전체 자료인 것처럼 표시하지 않는다.
운영의 최근 15분 seed와 별도 기간 요약의 의미는 바꾸지 않는다.
relay는 power endpoint에만 숫자형 Go duration(`s`, `m`, `h`; 소수 허용)의 `bucket`을
허용하고 같은 폭·UTC 정렬 개수·범위 제한을 적용한다. load endpoint에는 허용하지 않는다.

#### W1 외부 블랙박스 평가 cutover

EvaluationRun/LoadScenario와 대시보드는 W1 배포 묶음에서 같이 전환한다. 옛 객체는
삭제하며 alpha·κ/d 참조·seed/payload evidence를 새 스키마로 변환하지 않는다.
평가는 워크로드·절대 req/s·도착 분포·기간·seed·Dataset/trace를 선언하고 외부 결과를 읽는다.
LoadScenario는 `parts[].rates`, 워크로드별 `seeds`(uint64 십진 문자열),
`datasetRefs:{workload:{name,uid}}`, 선택적 `traceRefs`만 사용한다.
요청 시각 입력 API는 `kind=trace`만 허용하고 `set`은 LoadScenario 이름이다.

EvaluationRun 단계는 `id,row,loadScenarioRef`와 선택적 불투명 `settingsPatch`로 작성한다.
mode·startState·control이나 CHILL 이미지 대조 입력은 없고, `loadGenerator.nodeName`은 필수다.
전역·단계 settingsPatch는 공개 운영 설정과 같은 항목 목록으로 선택하며 서버의
공개 설정 patch 검증 경계가 실제 필드·값을 검증한다. 새 단계에 복원 상태나 내부 모드를
자동 주입하지 않는다.

preflight의 `liveTarget[]`는 `{component,resource,liveImageIDs,ready}`다. `specImage`·`match`와
digest 채택은 없고 실제 target readiness로 검사한다. POST 접수는 현재 관측을
`edge.dacs.io/evaluation-live-target` annotation에 남긴다. EvaluationView.liveTarget은
그 기록을 투영하며 annotation 누락·손상은 null이다. 실행 상세는 `평가 대상 CHILL` 표에서
기록된 자원·이미지 ID·준비 상태만 표시하고 null이면 `미기록`으로 남긴다.
복사 요청의 대상 이미지 변경은 입력 채택이나 JSON 수정을 요구하지 않는다.
활성 단계 응답에도 mode는 없다. 불변 게시의 기존 원문·명시적 비교·정밀 정수는 그대로
보존하며 현재 CHILL 상태로 과거 결과를 보충하지 않는다.
목록 facts는 Control/Mode 대신 단계별 `settingsPatchKeys`를 제공한다. Run patch 상속은
서버가 적용하며 `[]`는 변경 없음, null은 기록 없음이다. 대시보드는 운영 설정 정의의
한국어 항목명으로 키 경로를 표시하고 미등록 경로는 원문으로 보존한다. 목록의 변경값을
추측하거나 모든 게시의 단건을 추가 조회하지 않는다.

Evaluation list/detail GETs additionally project the Run's recorded `currentProcedure`
and `procedures[]` (`step,startedAt,finishedAt,reason,measurementVerdict`), plus
`stages[]` (`id,name,uid,conditions,measurementVerdict,currentProcedure,procedures,startedAt,finishedAt,goAt`).
Optional `stages[].provisionalD1` passes through the latest typed Stage status snapshot unchanged; it is closed-minute progress, not a pre-drain D1 verdict.
Its offered denominator includes frozen requests whose producer start + scheduled offset is at or before `closedThrough`; `unfinishedOrUnsent` is an `asOf` observation including in-flight or missing-result requests, not settled losses. Final D1 uses HTTP-receipt-origin elapsed time, so later results/finals may increase deadline successes.
The evaluation service reads child EvaluationStages through its existing reader,
in recorded `childRefs` order, matching name and UID; unreferenced, deleted, or
replaced children are not projected. Procedure order is preserved as recorded.
Collection reads list child Stages once for the namespace and index them by name,
checking the referenced UID, rather than issuing a GET for every Run's child.
Single-Run reads fetch only its referenced children.
Absent procedure histories, conditions, and timestamps remain null; absent
`currentProcedure`, `reason`, and `measurementVerdict` are omitted. The CRDs do
not record procedure `status` or stage `phase`, so those fields are not exposed.
Recorded procedure/stage `measurementVerdict` and stage `conditions` are
projected under their CRD field names, never derived from times or conditions.
The existing Run-level `phase` projection is unchanged.

Stage list/detail reads also expose recorded `progress[]` and `lastProgressAt`,
and a server read projection `loadJobs[]`. These are facts, not a stalled/running
judgment or a new condition. For example:
```json
{"progress":[{"phase":"loadStarted","at":"2026-11-04T03:46:10Z"},{"phase":"loadEnded","at":"2026-11-04T04:16:10Z"},{"phase":"drainStarted","at":"2026-11-04T04:16:10Z"},{"phase":"drainEnded","at":"2026-11-04T04:22:13Z","producerRowsMissing":{"plc-anomaly":2}},{"phase":"flushStarted","at":"2026-11-04T04:22:13Z"},{"phase":"collectStarted","at":"2026-11-04T04:22:18Z"},{"phase":"collectEnded","at":"2026-11-04T04:23:20Z","bytes":4096}],"lastProgressAt":"2026-11-04T04:23:20Z","loadJobs":[{"name":"load-example","uid":"job-uid","status":"Failed","startedAt":"2026-11-04T03:45:50Z","reason":"BackoffLimitExceeded"}]}
```

`progress` is append-only in harness observation order, not sorted by producer
clocks. Only observed boundaries are appended; absent boundaries are not filled
from planned durations, GO, Job state, or a measurement verdict.

| Phase | Owning step and observation |
|---|---|
| `loadStarted` | `conductor.CPO.run`: first producer JSONL `evaluationEvent=start`; uses that producer's `at`, not GO |
| `loadEnded` | `conductor.CPO.run`: all created producers emitted `window-end`; latest recorded producer `at` |
| `drainStarted` | `conductor.CPO.run`: that observed window-end boundary starts the drain-only interval |
| `drainEnded` | `conductor.CPO.run`: exits the producer drain/terminal-check/partial cleanup path and records `drainedUTC`, including partial drain |
| `flushStarted` | `conductor.CPO.run`: enters the existing `flushSeconds` sleep |
| `collectStarted`, `collectEnded` | `conductor.CPO.run`: immediately before/after `archive.collect`; the end includes `bytes`, the sum of saved raw artifact byte sizes (compressed files, not transferred bytes or all workspace files) |
| `judgeStarted`, `judgeEnded` | `evaluation.Harness.judge`: before judgment and after verdict plus generator-barrier handling; an exception leaves no end boundary |

`drainEnded.producerRowsMissing` records per-workload frozen planned requests
minus observed producer final rows at drain time (bounded below by zero), or zero
when that producer emitted `drained`. This is **drain-time producer-row evidence**,
not an in-flight count or the reducer's gateway-final-based `unfinishedOrUnsent`.
No later enrichment rewrites a boundary; final reducer numbers belong to judged
publication evidence.
The producer-row source is the same evaluator disk stream used by
`phase.producerCapture`, sampled before the final writer join/disk-tail recount.
The phase counters are then finalized from the joined stream and may include
later rows or a later drained marker. Do not replace or enrich the recorded
boundary with those final counters, `collectionLoss`, or reducer outcomes.
Run/stage `procedures[]` remain the authority for the broader factor/stage/judge/
cleanup execution and publication boundaries. No separate `judged` alias is added;
the requested finer judgment interval is explicitly `judgeStarted`/`judgeEnded`.
The harness logs each boundary as `EVALUATION_PROGRESS <JSON>` in its stage/judge
Pod. Producer event handlers only append to an in-memory progress list: no
Kubernetes call runs inside JSONL boundary handling. The existing `flushSeconds`
producer poll publishes the entire progress list and `lastProgressAt` together
through the same UID-tested status patch path as `provisionalD1`. Re-publication
replaces the full array (RFC 6902 object-member `add`), so the first write needs
no initial array and a failed write cannot lose or duplicate pending records.
No progress status publication runs before GO. The first heartbeat publication
occurs after release, so optional status IO cannot consume a producer Ready barrier.
The publication also saves `stage-progress.json` in the shared stage workspace
so the separate judge process retains prior boundaries. After ingestion ends,
drain/flush/collect/judge boundaries each publish once.
`lastProgressAt` is independent of `provisionalD1.asOf`; blocking flush/collection
calls do not invent periodic signals, and no additional heartbeat timer exists.
Producer stdout is independently appended/flushed by one reader thread per
producer. A blocked heartbeat/status publisher pauses the main-loop disk parser,
not these pipe readers; the regression holds the status call for 60 seconds
while all 134,156 producer request rows reach disk.
Status publication errors are logged as `EVALUATION_PROGRESS_ERROR`; this
diagnostic write does not stop or invalidate an otherwise observable measurement.

`loadJobs` comes only from namespace Job objects selected by the existing
`edge.dacs.io/workload-role=load-generator`, `evaluation-run-uid` and
`evaluation-stage` labels, joined to the exact controller EvaluationStage UID.
Collection reads list Jobs once and index them for all Runs; detail reads list
once with the Run UID selector. A replaced stage's old Jobs and other Runs'
Jobs cannot supply facts. `name`/`uid`, nullable `startedAt` (`status.startTime`),
optional `completedAt` (`status.completionTime`) and terminal condition `reason`
come directly from Kubernetes. `Complete=True` means `Succeeded`, `Failed=True`
means `Failed`, otherwise `Active` means nonterminal (including not-yet-started),
not a claim that a Pod is running. A failed Pod count alone is not a failed Job.
Workload/shard fields are not exposed: existing Job metadata has no such labels.
Deleted Jobs are absent; no harness history is fabricated from their old names.
Empty progress/Job lists and an unrecorded heartbeat are omitted.

RBAC is unchanged: GET evaluation projections run under serving-entrypoint,
which already has Job `get,list` in its configured profile namespaces.
Dashboard-reader does not need a native Job grant. Existing EvaluationStage
read grants and the harness's status patch grant are unchanged. No live grant
or deployment is performed by this change.

#### 실험 게시물 페이지·적용 SLO 출처

`GET /api/v1/experiments?offset=0&limit=5`는 기존 항목(facts 포함)을 유지한
`{items,page:{offset,limit,total,nextOffset?}}`다. 서버는 offset 0~1,000,000,
limit 1~50(기본 25)을 허용하고 둘 중 하나라도 명시하면 page를 제공한다.
마지막/범위 밖 페이지에는 nextOffset이 없다. 대시보드 결과·복사 목록은 5개씩 조회하며
실제 nextOffset을 따라가고 선택한 게시·명시적 과거 비교 단건만 조회한다.
page가 없는 구 서버의 items는 전체 목록으로 취급하고 가짜 nextOffset을 만들지 않는다.

serving-experiment.v1alpha1의 요청 KPI에 기록된 workloadUID·definitionID·
identitySource `{store,key}`가 모두 있어야 단계 적용 SLO를 연결한다. identitySource는
게시 sources에 있고 UID와 definitionID가 동결 정의의 모델/전처리기 세대까지 일치해야 한다.
단계 적용 표의 p95 제한은 유지한다. 과거 null identity는 이름이나 현재 구성으로 보충하지 않는다.
별도 정의 출처 표는 기록된 분위수·목표 지연을 p90/p99도 포함해 그대로 보여 준다.

#### 운영 사건 범위 조회·재연결 보존 상태

`GET /api/v1/observations/events?from=<RFC3339Nano>&to=<RFC3339Nano>&limit=100`
는 `[from,to)`의 보존 사건을 `{events,from,to,coverage,nextCursor?}`로 반환한다.
기존 정확 시점 `?at=<RFC3339Nano>`는 유지하며 범위 인자와 혼용하지 않는다.
정확 시점 조회도 `coverage={from,to,sources,gaps}`와 `nextCursor?`를 반환한다.
이 coverage는 event-time이 아닌 이번 호출이 읽은 append-time 구간이다.
빈 정확 시점 페이지는 기존 배열 계약대로 `events:[]`이며 오류가 아니다. `nextCursor`가 있으면
동일 `at`와 `cursor=<nextCursor>`로 계속 읽는다. 찾은 페이지에도 continuation을
유지하므로 같은 시점의 다른 사실·3시간 지연 append를 복구할 수 있다.
정확 시점 cursor는 다음 append-hour RFC3339Nano이며 아래 범위 cursor와 혼용할 수 없다.
기본 1시간의 호출 예산은 `servingEntrypoint.observationStorage.eventLookupHours`
(`--observation-event-lookup-hours`)로 정한다. [비용 근거와 상세 계약](observability-split.md#exact-event-recovery).
범위는 양수·최대 24시간, `limit`은 기본 100·최대 500이며 서버 `MaxQueryEvents`가
더 작으면 그 값이 상한이다. 잘못된 인자·범위·cursor는 `400 InvalidQuery`다.
`coverage={from,to,sources,gaps}`의 출처·공백과 원래 timestamp를 보존한다.

사건은 시각·stream·append 위치 순이다. `nextCursor`는 불투명 base64url 값이며
다음 요청에 동일 `from/to`와 함께 전달한다. 첫 조회의 committed prefix를 고정하므로
페이지 사이 새 append가 기존 페이지에 끼어들지 않는다. 새 사실을 포함하려면 새 조회를
시작한다. 대시보드 seed는 유한 페이지 상한을 두며 상한에 도달한 응답을 전체 기록으로
표시하지 않는다. archive 사건에 WebSocket `seq`가 없으므로 가짜 sequence를 부여하지
않고, 실제 사건 identity로 실시간 사건과 중복 제거한다.

`chill-live/1`의 snapshot은 선택적 `history`를 제공한다:
`{reason,replayed:false,requested:{afterSeq,throughSeq,at?},retained:{events,power},gaps}`.
`reason`은 `fresh` 또는 `checkpoint-unmatched`이며 unmatched 요청의 `at`은 그대로
유지한다. `retained.events/power`는 `{fromSeq,toSeq}` 또는 `null`이고 양끝을 포함하는
보존 frame 범위다. `gaps[]`는 `{fromSeq,toSeq,scope,reason:"NoRetainedFrames"}`다.
이 범위는 연속 event-time/전력 표본 coverage나 손실 사건 수를 뜻하지 않는다.
snapshot에 과거 사건·전력 bucket을 재생했다는 뜻도 아니다. 클라이언트는 이 필드가 있는
프레임을 수용·보존하고 gap을 **보존 공백** 상태어로만 표시한다. sequence 차이를
손실 건수·미관측 초 수로 환산하거나 전력 공백을 생성하지 않는다.
측정·실험 mutation의 audit는 durable commit 뒤 같은 projector로 WS action event에도
발행한다. 생성·삭제는 validated 시작과 종결 사건을 각각 보낸다. phase는
started/completed/failed를 유지하며, 오류 없는 NOT_SUBMITTED는 completed와
`status/result.status:"NOT_SUBMITTED"`다. completed만으로 성공을 세지 않고
`사전 검사 · 미제출` 중립 상태로 표시한다. 실제 error가 있으면 failed를 유지한다.



### 2.2 계획기 불확실성

`OperationTrial.energyUncertaintyJoules`, `NodeBaseline.wattsUncertainty=B/실제 측정초`를 cost projection에 전달하되 nominal 평균은 유지한다. `SchedulerNodePlan.energyUncertaintyJoules`는 사슬 민감도, `SchedulerCapacity.gainUncertaintyJoules`는 B-only minimumGain 문턱이다.
같은 immutable reference/trial index의 B/N은 발생 횟수·부호를 누적한다. keep−best에서 같은 근거를 먼저 상쇄하고 다른 근거의 잔여 절댓값 합을 문턱으로 쓴다. 이득>문턱이면 전환, 같으면 keep이며 keep infeasible 회복 경로를 유지한다.
`unloaded+Σ(loaded−unloaded)`의 unloaded 계수는 `1−instanceCount`, exact loaded baseline 계수는 1이다. target-baseline-delta는 저장된 차감 nominal을 한 번 복원하며 B는 wall 민감도다.
이 문턱은 관측 support/Pmax 조건의 위상-결측 민감도다. profile 통계오차·BEO idle fallback CI·시점 drift·global95% gain 보장은 포함하지 않는다. 원시 profile 분산/CI와 d의 95% 분석은 각각의 추정 계약에 보존한다.

## 3. DispatchOccupancyMeasurement (`occupancy`)

Native routes, CRD names, namespace/SAR, immutable spec and measurement
admission remain unchanged. **New creation now records a configured short
closed-loop burst, not a six-minute saturation verdict.** Legacy accepted DOM
records and their scheduler consumer remain a separate retained contract.

| Native field | Meaning |
|---|---|
| `batchEnergyOptimization`, `node`, `workload`, `batch` | exact accepted profile, current Node UID and Workload/Dataset evaluation inputs; fixed B |
| `supplyMode`, `workers` | closed-loop outstanding K, explicitly configured, never auto-sized to hide idle |
| `warmupSeconds`, `durationSeconds`, `minimumPairs` | configured warmup, burstSeconds and valid-pair count requirement |
| lifetime/delivery/preparation/setup/collection/drain/plannedElapsed | explicit policy allowances and profile-tail-derived safety budget; exact formulas and sample bases in [configuration](configuration-hardcoding.md#native-dispatch-occupancy-policy-w2) |
| `conditions` | profileID, waitingBatches, immutable inputsJSON with policy/configuration/source identities |
| status | phase/reason/powerMode/result; legacy scalar d/throughput are not populated by bursts |

**Execution boundary.** Prepare the accepted instance with fixedBatchSize=B,
maxBatchWaitMS=0 and Workload ownership/routing. The existing load Job uses
the configured load Node, Dataset evaluation input, common TLS gateway,
projected token and CA. Supply lasts W+D+L+delivery, its deadline includes
setup/drain/collection, and the watchdog includes preparation too. All segments
must fit the frozen cap; no padding, silent clamping, retry or auto-extension.
Cleanup uses the existing load label and Background deletion. Physical drain
captures native metadata keyed by physical instance. W6 slots, global generator
locks, request persistence and evaluation Poisson behavior are not modified.

**Shared estimator versus protocol.** `serving.OccupancyEstimator` is the same
factual reducer used by read-only observation queries. The DOM controller first
joins sparse rows by RequestID, then selects the earliest direct native start
among requests received in this Job's half-open receipt cohort. A late native
supplement with no receipt timestamp inherits cohort membership from its joined
HTTP row; unrelated earlier requests cannot establish the warmup origin.
Configured warmup advances that start into an exact half-open D-second worker-native
window. It streams selected rows through `ScanOccupancyRows` into `Add`;
`Results` supplies the exact cell's `estimate`.
The accepted profile pointer supplies the **mean full Go runner-exchange service**
at previous batch size—not BackendMS, stage sums or serving ServiceMS.

For adjacent batches i,j on one unchanged physical instance:

```
signed d_i = (nativeRunningAt_j - nativeRunningAt_i - Profile.MeanNanos(B_i))/B_i
```

Both starts must be inside the window. Full unique membership, direct
Q/S/F timestamps, successful complete execution, consecutive BatchIDs, unchanged
profile/decision/definition and worker-clock ordering are required. Every next
member must already be queued (`max(Q_j)<=F_i<=S_j`). Drain metadata must prove
fixedBatchSize=B, maxBatchWaitMS=0 and matching profile/decision applied before
the previous start. Missing batches/invalid evidence never get bridged.
Sparse native supplements join by RequestID rather than becoming extra requests.
Gateway receipt timestamps select cohorts, never sub-ms worker ordering.

The evidence `estimate` reports node/workloadUID/model/powerMode/executionPath/
runtimeImage/batch, profile ID/reference/evidence/actual mean, window, raw-row,
request and batch completeness counts, eligible pair count and signed
mean/median/p25/p75/IQR/p10/p90/negativeFraction (ns/request). Exact sorted-index
quantiles use round-to-even `round((n-1)*p)`. Zero pairs has `statistics:null`.
Negative samples remain negative; no operational clamping or precision guarantee
is added. Loaded runner slowdown relative to the profile remains in d.

**Assessment belongs to the burst protocol, not the estimator.** Pair count
below configured minimum is `Insufficient/TooFewPairs`; retain all statistics.
Otherwise verdict is `Observed`, native phase Complete—not `Accepted`. Load,
retention, physical-drain or isolation failures retain their operational reasons
and observed data. There is no idle-identification requirement, NotSaturated
verdict, estimated-idle planning gate or achieved-CI gate.

**Stored boundary.** `BurstOccupancyRecord` uses schema
`chill.dacs.io/dispatch-occupancy-measurement.v1alpha2` and separate prefix
`serving/dispatch-occupancy-observations/<NodeUID>/<CRUID>.json`.
It contains `estimate`, frozen conditions, load/drain/isolation and factual
verdict. It does not contain a fallback scalar d. Existing
`DispatchOccupancyRecord` v1alpha1 under `serving/dispatch-occupancy/` remains
unchanged for `CostEvidence`; raw `Observed` burst records do not themselves
authorize κ/replay. Scheduler adoption now uses the separately qualified rolling
native-pair cache described below. Legacy optional spec fields remain solely for
decoding old accepted objects. Old accepted objects and CRs are **not deleted**;
the legacy reader remains the fallback consumer, not a v1alpha2 burst reader.

The burst sample is an experimental protocol, not proof that100pairs/15seconds
is sufficient for every cell. The retrospective fixed-batch PLC result supports
the algebra and joined-native feasibility; its negative fraction, large IQR and
minute drift do not establish universal stationarity or scheduler admission.


**근거 지위.** mobilenet CPU 8개/GPU 2개는 새 규칙 미재인증 legacy이며 실제 대체 Accepted 전까지 소비한다. U2 CPU 8개/GPU 전체 5개/PLC 12개의 새 규칙 측정은 D2 뒤 25개 범위다. D01 Insufficient와 `rasp5-944` 진단은 보존한다. U3 D04/D09 새 immutable record가 실제 consumer source를 대체하며 옛 Smol CPU 2개 raw/status는 보존한다.
**Smol 해석.** D04/D09/D14/D23/D33의 profile 포함 95% 근사구간은 0 아래이며 evaluation의 알려진 systematic profile-versus-serving bias다. profile/NodeUID/model/B1/mode/runtime 일치와 harness/warmup/timing envelope/시간 분리는 관측 사실이다. 실효 thread/affinity·입력/전처리 경계·열/주파수/throttling·clock의 원인별 기여는 미확정 가설이다. signed raw·원 Accepted·`max(d,0)`을 유지하고 bias 보정/d 추정정책 대안은 M5 후보다. 수치·원시는 [bias JSON](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.json), [해석 근거](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.txt)를 따른다.

## 4. 계획기의 d 수집

- `CostEvidence.Refresh`는 `serving/dispatch-occupancy/`를 prefix 목록/불변 identity cache로 읽는다. `Accepted`이면서 기록의 `profileID`가 현재 catalog의 검증된 공유 실행 profile인 근거만 (Node UID, mode, runtime image, model)별 최신 `measuredAt` 선택에 참여한다. 과거 quota/thread 제어 profile의 d는 tuple이 같아도 새 κ 입력이 되지 않는다. 동률은 key 순(`latestBy` 규칙)이며 원 기록은 보존한다.
- Scheduler selection is **rolling measured B median meeting configured `minimumPairs` > legacy Accepted DOM > configured default**. Measured matches exact Node UID/model/power mode/execution path/batch/profile ID; runtime image remains historical evidence. Legacy DOM retains Node UID/mode/model wildcard matching across profiles/batches.
- The sole default authority is scalar `settings.defaultDispatchOccupancyNanos` (sample 2,000,000), shared by every batch. Public settings GET/PUT and canonical PATCH validation retain this value; `dispatchOccupancy` cannot shadow it. Only measured B supplies batch-specific values. That separate block explicitly configures `minimumPairs` (100), `aggregationWindowSeconds` (120), `refreshIntervalSeconds` (60), `scanTimeoutSeconds` (30), and `maximumRequests` (250,000), alongside burst policy. These are sample choices, not Go policy defaults or universal precision guarantees.
- One asynchronous worker scans/reduces outside Tick and writer locks. Selected request IDs are capped before estimator allocation; scans do not overlap. Idle cooldown is at least total scan+reduction time, and errors/timeouts/cap overflow discard partial/stale measured output in favor of DOM/default. Tick reads only the atomic published cache.
- The public occupancy GET uses the same `aggregationWindowSeconds` cap (sample120) as rolling aggregation. Over-limit requests return `400 InvalidQuery` before scanning. Longer historical analysis uses raw observation export and offline reduction; extending a manual serving-process GET is not the offline path.
- `serving.DispatchOccupancy` keeps the evidence record key in `source` and adds `provenance{source:measured|dom|default,pairs,window,updatedAt,value}`; `value` is operational ns/request, `round(max(median,0))` for measured B. Raw signed statistics remain untouched. κ input and recorded decisions retain the selected exact-cell tuple and provenance; record→read→capacity preserves resolved sources rather than relabeling defaults as DOM.
- `/api/v1/catalog-read` exposes the actual selected list in `accepted.dispatchOccupancy` and signed pooled-cell facts in `accepted.occupancyEstimates`. Pair samples pool across workload UIDs, never medians-of-medians; eligibility still requires unchanged workload/config across adjacent physical batches. Metadata-only refreshes reuse numerical κ; value changes invalidate it. Throughput and replay add actual batch size × d once. 상세 용량 소비는 [계획기](analytic-planner.md)를 따른다.

### 4.0 Query·Dashboard의 관측/채택 분리

`GET /api/v1/observations/occupancy?from=<RFC3339>&to=<RFC3339>`는
`{window:{start,end},rows:[...]}`를 반환한다. 기간은 현재
`dispatchOccupancy.aggregationWindowSeconds` 이하이며 초과는
`400 InvalidQuery`다. 웹은 400의 선택적 정수 `maximumWindowSeconds`를 상한으로
기억하고 없으면 서버 사유만 표시한다(정상 응답 형태는 동일). 각 행은 `node:{name,uid}`, `workloadUID`, `model`,
`powerMode`, `executionPath`, `runtimeImage`, `batch`,
`profile:{profileID,reference,evidence,meanNanos}`, `window`와 `pairs`,
`batches`, `completeBatches`, `requests`, `completeRequests`, `rawRows`,
`completeRawRows`, `enrichedCompleteRawRows`, `finalRows`, `completeFinalRows`를
보존한다. `statistics`는 signed ns/request의
`median,p25,p75,iqr,p10,p90,mean,negativeFraction`이며 표본이 없으면 `null`이다.
API와 웹은 합격/불합격·포화 판정을 추가하지 않는다.

커버리지는 실제 스케줄러가 선택한 d와 관측 d(B)를 별도로 연결한다.
스케줄러 d의 유일한 채택 근거는
`accepted.dispatchOccupancy[].provenance`의 `source`, `value`, `pairs`,
`window`, `updatedAt`이다. 출처는 `measured`/`dom`/`default`이며 기본값도
명시된 실제 선택 사실이다. legacy `accepted.occupancy`만으로 채택 여부나 값을
추정하지 않는다. `accepted.occupancyEstimates`의 signed 통계·pairs는 관측 사실이고
선택값으로 바꾸지 않는다. 카탈로그 필드 누락/조회 오류와, 일치하는 선택 행의
provenance 누락은 `adopted:null`이다. 확인된 빈 선택 목록·일치 선택 없음 또는
명시된 default 출처만 `false`이며 measured/DOM 출처는 `true`다.

Query의 DOM reader는 경로와 schema를 함께 구분한다. legacy v1alpha1은
기존 scalar를 읽고 새 v1alpha2는 `estimate`와 `Observed`/`Insufficient`를 읽는다.
새 evidence에 scalar가 없다는 이유로 mismatch/0을 만들지 않으며 `d:null`을
유지한다. 정상 Observed의 scalar 부재는 오류 사유가 아니며 웹 사유는
`해당 없음`이다. Failed·Insufficient의 원인과 evidence 읽기/검증 오류는 통계와
별도로 상세에 표시한다. 새 burst에는 saturation/idle/saturationSizing 근거가
없으므로 해당 projection은 `null`이고 진행 화면에도 포화 판정·유휴 비율을
표시하지 않는다.
웹의 셀 상세는 스케줄러 d와 관측 d(B)를 분리하고, 선택 기간의 집계 행은
Node UID·모델·전력모드·실행 경로·배치·profileID로 연결한다. 이미지가 다르다는
이유로 행을 숨기지 않는다.
Query API의 estimate/window/profile/statistics는 `internal/queryapi` 소유 read 타입이다.
`cmd/chill-query` 구성 루트 한 곳에서 Serving 값을 변환하며 Query 패키지는
Serving 구현을 import하지 않는다. 모든 수치·null·근거 참조·ns 시각의 JSON 형태를
변환 회귀로 보존하고 기존 architecture 의존 경계를 완화하지 않는다.


### 4.1 observer와 coherent replacement

`Observer.ObserveUntil(ctx,Request,end,onFirstSample)`는 실제 producer end 뒤 final read까지 수집한다. Duration은 유한 watchdog, 표본 예산은 `floor(watchdog/interval)+3`이다. standalone `--end-wait-timeout`은 `RunConfig.Metrics.DurationSeconds`, `--max-result-bytes`는 frozen policy `MaxMetricResultBytes`다.
profiler는 별도 `mgr.GetAPIReader`로 recorded Job을 조회한다. 실제 API NotFound는 `ExecutionLost`, transport 오류는 retry이며 cache의 일시 NotFound로 replacement를 만들지 않는다.
BEO annotation `chill.dacs.io/replaces-batch-energy-optimization`은 같은 namespace 원본 이름이다. 최신 시각 선택 전에 대상을 현재 측정에서 제외하되 raw/status·이력 증거는 보존한다. 대상 부재·자기참조·측정 조건 변경은 비차단 진단이며 운영자 선택을 별도 품질 거절로 바꾸지 않는다. 실제 실행·증거의 정상 안전 계약은 유지한다.
012/017 coherent replacement와 원 trial/status를 보존한다. 원본017 Granted/Measuring은 비실행 보존 예외다. execution/serving Pod/power pending 0·정확 NodeUID/RV/holder·orphan runtime 확인 후 CampaignOps가 old UID stale 측정 taint만 정리하며 root/status/Runs/retained Jobs/raw는 남기고 대체는 정상 admission을 따른다. observer 검사는 실제 late producer tail/final sample/event/result byte 근거를 연결한다.

## 5. 측정 admission

두 kind는 `spec.node` 대상 측정 루트이며 terminal은 `Complete`, `Insufficient`, `Failed`다. 엄격한 노드별 순서·taint·CPU 가용성·반환의 권위는 [admission §2](measurement-admission.md)다. 앞 루트가 기다리면 `claimed`로 뒤 승인을 막고, 승인 taint 노드의 측정 인스턴스 때문에 뒤 루트를 거절하지 않는다. `NodeServing`은 taint 밖 계획기/수동 Pod에 적용한다.

| kind | 승인 전 확인/사유 |
|---|---|
| BEO | CPU 공유 실행은 affinity/quota/online−reserved 거절을 사용하지 않는다. 전력 상태·물리 Node·memory/GPU·소유/반환 증명은 유지한다. |
| opcost, occupancy | `ProfileMissing <BEO> <사유>`: 없음/NotComplete/non-Complete terminal/catalog issue. 같은 노드 앞 BEO는 순서대로 대기하며 catalog profile/issue 모두 없으면 갱신을 기다림. `NodeMismatch`(targetNode≠spec.node), `ActuationNamespace`(owner와 같은 namespace 요구) |
| opcost | `PowerModeUnknown <모드>`, `MeterUndeclared` |
| occupancy | `BatchUnmeasured <B>`, `WorkloadMissing`, `WorkloadAutonomous`, `WorkloadServing <Pod>`(같은 capture의 owner/route 가중치), `LoadGeneratorUnconfigured` |

mode는 `spec.state.candidate.powerMode`, online CPU는 DeviceClass `powerModes[].onlineCPUs` cpuset이다. 없으면 현재 read-back mode에만 `edge.dacs.io/cpus`를 쓰고 다른 mode는 `OnlineCPUsUnknown`이다. admission이 실제 Node/ConfigMap 조건을 확인한다.
승인은 taint/`Granted` 뒤 같은 잠금에서 run 등록, 거절은 ChillServer가 `Failed`/사유를 쓴다. ChillServer 노드 루트와 occupancy run을 격리하고 서로 다른 측정 노드 occupancy는 병렬이다. gateway 실제 CPU/메모리를 manifest에 남긴다.
재시작 시 taint+Granted+nonterminal인데 run이 없으면 `Failed:ChillServerRestart`; opcost는 `recordRestartedSequence`로 같은 reason의 순서 기록 후 반환한다. terminal status는 유지하고 반환만 재시도한다. taint만 있고 Granted가 없으면 PMTC처럼 반환 후 재대기한다. 재개 없이 같은 spec의 새 이름을 쓴다.

## 6. RBAC

ChillServer는 두 kind 목록/status patch, release namespace Job create/get/list/delete, actuation namespace NodeActuation 권한을 가지며 같은 namespace owner reference를 쓴다. `<fullname>-load-generator`는 gateway Workload 경로 POST 권한을 갖는다. 두 kind status writer는 ChillServer 하나다.

## 7. 저장 계약 (ADR 0019)

[ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 형태를 reader가 엄격히 소비한다. Timing 없는 미기록 B를 관측한 0으로 투영하지 않으며 원 raw·값·판정은 외부 과학 증거로 보존한다.
direct-d는 rolling 측정값을 실제 scheduler 선택에 연결하되, 신규 burst 원본과
legacy scalar 원본의 저장 계약을 합치지 않는다. 신규 burst는 별도 prefix/schema의
`Observed` 사실이며 구 reader의 목록에 들어가지 않는다.
기존 `serving/dispatch-occupancy/` 객체·CR·타입은 **지금 삭제하지 않는다**.

| 대상 | 불변 저장/소비 계약 |
|---|---|
| operation/순서 기록 | 실제 timing·B/S/G·보고 비율·operation identity·단계별 출처 |
| legacy `serving/dispatch-occupancy/<Node UID>/<CR UID>.json` | 현재 scheduler가 소비하는 기존 Accepted scalar 계약을 그대로 보존 |
| burst `serving/dispatch-occupancy-observations/<Node UID>/<CR UID>.json` | estimator 결과·minimumPairs가 포함된 동결 조건·실제 native/drain/격리 근거, Observed 또는 Insufficient; burst 원본 자체는 scheduler 채택 증명이 아님 |
| d/판단 | 실제 선택 provenance와 source를 보존. signed B 통계와 operational 선택값을 구분하며 선택은 measured > DOM > default |
| scheduler 설정 | 선택 `SchedulerSettings`/`minimumDemand`를 `decision.settings`, `decision.search.bounds`, scheduler 관측 기록에 동결. 캠페인 binding의 scheduler 입력 참조/settings 캡처를 결과에 연결 |

### 7.1 최소 수요와 재현 설정

별도 Run API/저장소 없이 기존 캠페인 binding/summary와 scheduler 판단/관측 기록을 쓴다.
`minimumDemand`는 Workload **UID**→최소 req/s map이다(이름/model ID 아님). 빈 UID·음수·NaN·무한대는 거절하며 map/UID 생략은 0이다. 유효 수요는 `max(관측 수요,minimumDemand[UID])`; 관측은 장기/최근 도착률·미완료 기여를 포함한다([계획기 §3](analytic-planner.md#3-판정과-순서)). 유휴 제거·교체 회수에도 하한을 적용하며 무부하에도 그 용량을 유지한다.
선택값은 판단/관측에 동결하여 이후 설정 변경이 과거 하한을 바꾸지 않는다. `hack/evaluation/conductor.py`/`demo.py` 새 binding은 scheduler 입력 참조와 전체 `schedulerSettings`(minimumDemand 포함)를 동결하고 `e5-summary.json`/demo `summary.json` 및 immutable 결과의 단계별 `kpis.schedulerSettings`에 같은 값을 남긴다. 과거 binding 누락은 null/사유이며 현재값/가짜 0으로 채우지 않는다.
실행 중 실제값은 `initial.json`, 경계별 `pre-settings.json`/`post-settings.json`, `static-selection.json`, `decision.settings`에 저장한다. binding은 입력 동결 시점이며 이후 변경은 판단/경계 기록으로 확인한다. `decision.capacity.workloads[]`에 `minimumDemand`(미설정 명시적 0), 최종 하한 적용 `rate`(req/s)를 함께 쓰고 관측 도착률은 덮어쓰지 않는다.

**G2 근거·승인 경계.** B=0만으로 계약 세대를 판별하지 않고 실제 immutable 참조/Timing을 조사한다. [소비자 목록](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json)은 catalog 비용 177개 중 35개, baseline 80개 중 14개에 Timing이 없음을 확인했다. 49개 중 E5 선택 노드 empty ranking legacy 9개의 불확실성은 unknown이다. 동결 E5 identity/mode 후보 에너지 입력 legacy는 0개이나 실행된 선택/전체 prefix 정리 완료를 뜻하지 않는다. 최신 선택 전 객체를 읽으므로 superseded/terminal CR 참조도 정확한 정리 목록에 포함한다.
G2 승인 범위는 ranking baseline 9개의 idle/reach-power 2 roots·18 steps, 조건부 직접 전환 포함 상한 4 roots·22 steps다. root마다 raw·실제 소비·정상 반환 뒤 다음으로 진행한다. 9개 대체 확인 뒤 검증 외부 백업의 실제 절대경로, raw 80개 키(old ranking 9개 포함), terminal OCM 6개 개수/목록을 대조·보고하고 해당 작업 저장소 참조를 정리한다.
같은 승인 조건의 legacy/결측 대체→확인→검증 백업→옛 기록 삭제는 연속 실행하되 새 조건/판정/범위 확대/배포는 별도 승인이다. strict reader·obsolete 필드·비교 테스트 제거는 수집과 병렬로 같은 영구 소스 변경에서 완결한다. 새 소스 검증/적용은 대체·백업·정리·Timing 없는 소비 0건의 실제 근거 뒤다. 첫 BEFORE 전 catalog/d/profile/runtime/lowest/settings를 공통 동결한다. 원본012/017·Smol d·새 38단계 기록·runtime7은 보존하고 재측정/삭제/reader 제거 실적은 실제 근거로 기록한다.

## 8. 캠페인 manifest

신규 DOM API는 서버가 선택한 `dispatchOccupancy`의 burst/workers/minimumPairs·예산을 동결하고
실측 accepted profile/SLO·native queue/lifetime 범위를 대조한다. 기존 외부 campaign generator의
legacy N/K/L·idle/precision 유도 형식을 신규 burst에 적용하지 않는다.
`remove`는 해당 profile의 Accepted prepare에서 관측한 정확한 Pod UID/container ID를 쓴다. idle/power에도 identity를 유지하고 `measured-*`는 논리 슬롯이다. 교체/physical source 누락은 atomic exact-identity 검증 실패이며 immutable record를 보존한다.
`Refused`/`Insufficient`/`Failed`면 해당 노드 레인을 멈춰 부분 근거·자연 반환을 보존한다. 재측정은 원인·승인 범위 확인 후 새 이름 요청으로 한다.

## 9. 검사 계약

검사 근거는 실제 source SHA/실행과 연결하고 설계 수용 조건과 배포·검증 실적을 구분한다.

| 대상 | 확인할 불변식 |
|---|---|
| API/admission/격리 | 필드 조합·spec 불변·profile/Node/CPU·실제 gateway cap 사전 거절·노드 순서·taint/서빙 격리 |
| operation/민감도 | 실제 identity/effect·부분 실패/sequence/반환·상수 S=0/offset 불변/내부 극값·원 gap 합집합/cap·실제 support/bracket·undefined 비율·10% 초과 Accepted |
| minimumGain | nominal 불변·signed 상쇄/사슬 합성·이득=B keep/>B 전환·keep infeasible 회복·exact baseline의 무관한 profile CI 제외 |
| d/소비 | 새 B: RequestID join·물리 full membership·동일 configuration·연속/backlogged native pair·정확 분위수·signed 음수 보존·minimumPairs 사실. 기존 scheduler: legacy Accepted scalar·signed/max0·identity/source 선택을 유지 |
| lifetime/observer | L≥SLO/cap≥L/socket>L+delivery·sender/m 단일 차감·producer end 뒤 final read·동결 event/result byte·authoritative NotFound/transport 구분 |

## 10. 캠페인 범위와 증거

evaluation 전 범위는 Node당 기준 mode의 필수 profile·d·정확 비용이며 mode 확장은 evaluation 뒤 별도 범위다. runtime 7종 digest와 원본012/017 raw/status·replacement 출처를 보존한다(017 비실행 Measuring/Granted 예외). 실행 순서/evaluation 입구는 [POOL 검증 계획](workload-pools.md#9-검증-계획)을 따른다.
조건 생성기·승인 원문·manifest·profile/runtime/NodeUID/source SHA·원시 위치를 선별하며 `.token`·Secret 스냅샷·전체 캐시 백업은 저장소/증거 인덱스에서 제외한다.
- d 결정(`O/intent/intent-archive-20261030.tar.gz:d-measurement-decision.md`)
- 조건 생성기 provenance: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/gen.py` (원래 실행 위치; 현재 파일 부재)
- [승인 manifest](/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/phase-a/measurement-generalization-20261024/approved/)
- 비용과 d 결정 근거(`O/intent/intent-archive-20261030.tar.gz:implementation-contract-20261024.txt`)
- D2와 E5 승인 근거(`O/intent/intent-archive-20261030.tar.gz:deployment2-e5-proposal-20261024.md`)

## 11. 같은 기종 Node 프로파일 재사용 — PR-01..04

### 11.1 저장과 판정

요구사항/허용값 근거는 [catalog SRS §7](../../spec/catalog/SRS.md#7-u4-같은-기종-프로파일-재사용)을 따른다. 새 CRD·spec/status 필드·일반화 엔진 없이 대상의 기존 BEO→ProfilingRun→불변 result/raw를 사용한다.
대상 BEO metadata annotation은 `chill.dacs.io/profile-reuse: '{"sourceProfileID":"chill-system/SOURCE-BEO/SOURCE-UID","maximumRelativeDifference":0.05}'`다. 두 필드 필수, unknown 필드·(0,1) 밖/비유한 허용 차이는 거절한다. 0.05는 계획 여유 25%의 1/5인 screening 권고이지 고정 검증/통계적 SLO·전력 오차 상한이 아니다.
원본은 같은 namespace의 **직접 측정** catalog profile이며 최신 조건 선택 후 유효해야 한다. 자기 자신·전이 재사용·무효화/삭제/대체되어 catalog에서 빠진 원본은 거절한다. 확인 BEO는 일반 독립 후보가 되지 않는다. 실패 annotation을 지워 짧은 측정을 승격하지 않고 사유/raw 보존 후 새 BEO로 독립 측정한다.
원본/확인 frozen condition 비교에서 plan 이름·Node UID·meter identity·GPU UUID·활성/유휴 창 길이만 제외한다. DeviceClass·image digest·모델/전처리/데이터셋·mode·accelerator 할당/권한·runtime version·initialized batches는 일치해야 한다. CPU allocation은 frozen 계약에 없고 실제 affinity/cgroup/cpuset/thread 관측값을 자원 보장으로 matching하지 않는다.
같은 캠페인의 active trial끼리와 idle/active actual runner 비교도 CPU 관측값만 제외하며, 물리 Node·GPU identity·artifact 및 나머지 runtime 사실 검증은 유지한다. 원시 CPU 관측값은 그대로 보존한다.
확인 활성/유휴 창 각각 ≥30초, 기존 trial≥3, 같은 warmup/settling/관측 interval/timeout/observer image를 유지한다. 원본의 모든 실측 batch가 확인에 있어야 하며 빠진 batch를 추정 추가하지 않는다.
배치별 서비스 평균은 raw RUNNER SERVICE 표본 평균이다. 활성 평균 벽전력은 trial별 `activeWindowWallJoulesPerItem/idleBaselineSecondsPerItem`(같은 창 총 벽에너지/측정초)의 trial 간 산술 평균이고 유휴는 model-loaded idle baseline이다. 서비스시간으로 전력을 역산하거나 증분 전력을 전체 벽전력으로 비교하지 않는다.
각 지표의 `abs(target−source)/source`가 모두 설정값 이하면 Accepted, 분모≤0·결측/비유한·조건 불일치·차이 초과면 Rejected/독립 측정이다.
`CatalogView.reuseChecks`는 양쪽 ID/불변 결과 참조/Node·허용값·idle 차이·배치별 service/active wall 차이·판정/사유를 제공한다. 확인 result/raw/annotation은 BEO에 보존한다.
scheduler service는 Accepted/Rejected 모두 `{schemaVersion:"chill.dacs.io/profile-reuse-check.v1alpha1",check:…}`를 `serving/profile-reuse/<확인 profile ID>/<판정 본문의 SHA256>.json`에 PutImmutable한 뒤 catalog를 발행한다. `reuseChecks[].record`, Accepted `reuse.record`가 이를 가리킨다. annotation 변경/원본 무효화 거절도 새 content-addressed 기록이며 이전 판정을 덮어쓰지 않는다. refresh signature는 ID뿐 아니라 판정 본문을 포함하고 catalog는 클러스터 annotation/status를 쓰지 않는다.

### 11.2 대상 행과 물리 개체 계약

행 ID는 `확인 profile ID/reuse-<SHA256(sourceProfileID + NUL + 허용값)>`로 source/정책 변경이 immutable startup bundle을 덮어쓰지 않게 한다. `Reference`, `MeasuredAt`, frozen condition·NodeName/UID·meter·runtime metadata/delivery·idle baseline은 **대상**이다.
서비스 분포/증분 에너지는 원본, 메모리는 원본/대상 관측 최대 중 큰 값, 활성 평균 벽전력은 확인값이다. Evidence에 양쪽 result/raw를 연결한다. GPU UUID는 Condition.RuntimeAccess/RuntimeMetadata/runtime delivery 모두 대상 확인 기록에서 가져오며 원본 UUID를 대상 inventory에 넣지 않는다. CPU 관측도 대상 기록을 보존하고 대상 Node UID 재생성 시 확인을 재사용하지 못한다.

| Node UID 소비 경로 | 계약 |
|---|---|
| catalog latest-condition | 원본 유효성 선택→확인 판정/대상 행 materialize→대상별 최신 선택 |
| scheduler_capacity bootstrap/nodePlans | UID guard 유지, 대상 UID 행만 대상 option 연결 |
| observer validateSourcePlacement | 실제 Node·DeviceClass/image/resource guard 유지, 원본 Node alias 금지 |
| operations validatePreparation | 대상 Profile/UID/runtime delivery로 prepare, source UID 예외 없음 |
| firstfloor client/native metadata | 대상 UID·NodeName·actual runtime 검사 유지 |
| runnerPreparation | 대상 UID/mode/model 최신 준비 행, source UID fallback 금지 |
| cost_power validatePowerNativeProfiles | 대상 UID·실제 UUID/image 검사 유지 |
| measuredGPUs/manual_reset/measurement_sequence | 대상 metadata만 inventory/측정 후보, source UUID 오염 금지 |
| opcost measurementProfile(비용/occupancy 공통) | BEO UID 직접 profile ID 또는 `reuse.ConfirmationProfileID`가 같은 Accepted 행으로 해석, 실제 파생 ID로 preparation 조회. 원본 BEO ID alias 금지 |
| cost/d/idle | d·prepare/remove/power 비용은 대상 Node/profile ID별 별도 측정, source 비용 복제 금지 |

### 11.3 CampaignOps: genesis2→genesis3 한 건 실증 절차

CampaignOps 한 명만 클러스터를 변경한다. 아래는 **실증 성공 기록이 아닌** 조건/증거 계약이다.
- 레인이 비고 두 Node serving/measurement 예약이 없는 상태에서 Node/DeviceClass/source BEO/result/catalog 원시 응답과 정확 UID·label·image/mode·quota/affinity·배치/평균을 남긴다. 읽기 전용 `nvidia-smi -q`의 UUID/driver/VBIOS/기본 power limit도 보존한다. genesis2/3 기본 power limit 250/300W 관측 때문에 이름만으로 동등 판정하지 않는다.
- genesis2 유효 PLC `ort-cuda` 한 행을 우선 사용한다. U3 locked는 양쪽 같은 mode 원본이 있을 때만, fixed 원본→locked 확인은 금지다. runtime 7 digest는 유지한다. 새 대상 BEO의 annotation에 실제 source ID를 넣고 targetNode만 genesis3, 활성/유휴 각각 30 s, metric은 경계를 덮도록 한다. warmup/settling/trial≥3/affinity/quota/batch를 유지하며 UID/resourceVersion/status/ownerReferences와 원본 spec/status는 복사/수정하지 않는다.
- 정상 admission/Complete 후 catalog의 `reuseChecks`와 세 종류 차이≤0.05를 확인한다. 거절은 사유/차이/raw를 보존하고 독립 측정하며 허용값 확대·batch 제외·값 복제·강제 Complete로 통과시키지 않는다.
- 대상 파생 ID·genesis3 UID/UUID/meter·원본 service samples/energy, preparations/snapshot option 연결을 확인한다. 더 최신인 기존 독립 profile은 삭제/무효화하지 않고 최신 선택 이유를 기록하며 새 확인이 최신인 조건으로 실증한다.
- 용량 연결은 별도다. 같은 UID/mode/runtime/model 기존 Accepted d와 exact profile ID별 비용 적용 범위를 확인하고 필요 비용은 정상 OCM으로 잰다. source d/cost는 복제하지 않는다. 대상 option/profile ID·d source·κ 및 prepare→native actual read-back→remove/hand-back의 UUID/자원 반환을 보존한다.
- 배포 SHA·양쪽 BEO UID/annotation/result/raw·profiles(reuseChecks/행/preparations)·snapshot option/row·실제 Pod/native UUID·성공/거절 사유·반환을 원시 디렉터리에 저장해 U2 표에 연결한다. 짧은 확인 시간은 독립 전체 측정 한 건 절감과 구분해 보고한다.

회귀 범위는 허용 경계/설정 변경·기종/image/자원 불일치·source 무효화/최신 선택·batch/전력 결측·대상 UUID·양쪽 독립성·planner option·확인 BEO의 비용/occupancy 파생 행 해석 및 실제 preparation 연결이다.

## 12. 대시보드 측정 API 보강 계약 (B0, 2026-10-30)

**설계 확정·구현 전.** [B0 계획](../../plans/2026-10-30-dashboard-api-gaps.md)의 B1/B2가
이 절을 구현한다. 화면 계약은 [Dashboard §8](../dashboard/README.md#8-측정실험-ux와-커버리지-2026-10-30),
요구사항은 [catalog SRS §9](../../spec/catalog/SRS.md#9-측정-커버리지-조회-api)다.
기존 CR, raw/result JSON과 검증·admission 판정은 바꾸지 않는다.

### 12.1 B1 — 권위 있는 정적 runtime 호환성

`GET /api/v1/model-compatibility?modelSpec=<name>&executionPath=<name>&deviceClass=<name>&node=<name>`
한 건을 읽는다. 네 query는 각각 정확히 한 번 필수이며 추가/중복 query, 빈 값·잘못된 이름은 400이다.
Query API query 경계가 이름/참조·Node의 class selector membership과 사실 입력의 구조·의미를 검증한다.
ModelSpec/DeviceClass/Node 미존재는 404, 권한 거부는 403, 사실 조회 실패는 503이다.
누락·모순된 runtime 선언은 성공 응답의 `Unknown`이며 빈 지원 목록이나 `Incompatible`로 숨기지 않는다.
설정된 fact-source ConfigMap/키 자체의 부재도 선언 부재로 `Unknown`이다.
strict 선언 해석 실패·불완전한 RKNN 설치 계약은 `RuntimeDeclarationInvalid`로 반환한다.
선택되지 않은 path와 class selector에 속하지 않는 Node 조합은 400이며, API identity가 깨진
저장 객체·권한 외 API 읽기 오류는 판정으로 위장하지 않고 503이다.

응답은 `{readAt,selection,inputs,verdict,reasons,diagnostics}`다.

| 필드 | 계약 |
|---|---|
| `readAt` | RFC3339Nano UTC 조회 시각. 여러 Kubernetes GET을 atomic snapshot으로 주장하지 않음 |
| `selection` | query의 `modelSpec,executionPath,deviceClass,node` 원문 이름 |
| `inputs.modelSpec`, `inputs.deviceClass`, `inputs.node` | 각각 `{name,uid,resourceVersion}` |
| `inputs.runtimeDeclaration` | `{namespace,name,uid,resourceVersion,key,image}` 또는 null. 선택 실패 시 image를 꾸미지 않음 |
| `verdict` | 기존 `modelcompat`의 `Compatible`, `Incompatible`, `Unknown` |
| `reasons[]` | `{kind,code,message}`. 기존 `Conflict`/`Unknown`·reason code 재사용. artifact supply architecture 불일치는 `ArtifactArchitectureMismatch`; 선언 부재/무효는 `RuntimeDeclarationMissing`/`RuntimeDeclarationInvalid`. Compatible이면 빈 배열 |
| `diagnostics[]` | 비차단 문자열 진단. 같은 scope의 선언이 여러 개면 ConfigMap document의 마지막 일치 entry를 선택하고 이 사실을 표시한다. reasons·verdict·요청 가능 여부는 바꾸지 않음 |

`candidate_resolver.go`와 `execution_facts.go`의 **동일 trusted fact-source, class/Node scope,
strict declaration 해석** 및 `modelcompat.Evaluate`를 재사용한다. 필요한 읽기 부분만 기존 kube
adapter에서 공유하고 Query API에는 composition-root callback으로 연결한다. Query API/serving이
profiler controller를 import하거나 별도 runtime 지원 registry·backend 이름 추측표를 만들지 않는다.
실행 선언은 해당 class/Node/family/backend scope에서 선택한다. 여러 entry가 맞으면 ConfigMap document의
마지막 entry가 현재 선언이며 비차단 diagnostics로 알린다. 후보 없음은 Unknown이다.
선택한 선언의 명시적 충돌과 artifact architecture 충돌만 Incompatible이다.
같은 class의 여러 Node 판정은 각각 조회하며 Node-bound GPU 선언을 class 전체로 확장하지 않는다.

이 판정은 **현재 선언에 근거한 정적 runtime 지원**이다. 실제 runtime probe, 현재 Node Ready,
전력모드/online CPU 적격성, 전처리·데이터셋 호환성, 측정 완료나 admission 성공은 아니다.
power mode가 다른 열은 같은 정적 결과를 재사용할 수 있지만 별도 eligibility를 합성하지 않는다.
화면은 Compatible을 ‘정적 실행 지원 확인’, Incompatible을 ‘지원 안 함 — 이유’,
Unknown을 ‘지원 확인 불가 — 이유’로 쓰며 증거 상태와 독립 표시한다.
최종 측정 요청은 기존 dry-run/preflight/admission을 그대로 통과해야 한다.

구현: Query API query callback은 기존 `KubernetesCandidateResolver.ResolveModelCompatibility`를
연결한다. Profiler preflight와 동일 selector/strict decoder/정적 비교 및 RKNN 선언 요건을
공유하며, 기존 preflight의 성공·거절 흐름은 바꾸지 않는다. public proxy의 정확
`/chill/api/v1/model-compatibility` GET grant만 추가하며 기존 Query API fact-source 읽기 권한을 쓴다.

R3 추가: `GET /api/v1/model-compatibility-matrix`는 query 없이 전체 inventory의
모델×실행 경로×DeviceClass를 한 번 조회한다. 응답은 `{readAt,scope,rows}`이며 각 row는
위 B1 응답과 같다. class의 이름순 첫 Node를 대표로 기존 B1 resolver를 호출하고 전력모드 간
판정을 공유한다. Node가 없거나 개별 판정 조회가 실패하면 Unknown row를 보존한다.
대표 Node 지원을 class의 다른 Node로 확장하지 않으며 Node 필터가 대표와 다르면 UI 분모에서 제외한다.
행렬 전체 미제공은 503, 임의 query는 400, Cache-Control은 no-store다.
ChillServer proxy·SAR·RBAC에 정확한 `/chill/api/v1/model-compatibility-matrix` GET만 추가한다.
클라이언트는 Compatible만 분모에 포함하고 Unknown 수를 별도 표시한다. 선택 패널의 개별 B1 GET은
행렬을 바꾸지 않으며 실제 요청 전 기존 preflight를 유지한다.

### 12.2 B1 — 실패 BEO와 소유 Run의 공개 연결

기존 `GET /api/v1/namespaces/{namespace}/profiling-runs`의 `runs[]`에
`ownerReferences:[]`(Kubernetes `metav1.OwnerReference` 원형)와 `labels:{}`를 추가한다.
labels는 기존 `edge.dacs.io/batch-energy-optimization`, `edge.dacs.io/workload-mode`,
`edge.dacs.io/batch`, `edge.dacs.io/attempt` 네 key만 투영한다.
없는 값은 만들어 넣지 않는다. ownerReferences가 없으면 빈 배열이다.
추가 저장·CRD·owner 재작성은 없다.

소유 판정은 같은 namespace에서 `controller=true`, `apiVersion=edge.dacs.io/v1alpha1`,
`kind=BatchEnergyOptimization`, **BEO UID가 정확히 일치**할 때만 한다. 이름/label만으로
소유를 확정하지 않는다. 원래 `spec.measurement.batch/workloadMode`와 attempt label을 표시하며
정수 ≥1이 아닌/없는 attempt는 ‘회차 기록 없음’이다. label 해석은 API projection 경계에서
소유를 판정하는 새 gate가 아니며 원문을 보존한다. parent가 삭제되거나 같은 이름 다른 UID면
옛 UID의 owner 사실만 보여 준다. 전체 목록 page 확인 전 ‘소유 회차 없음’을 확정하지 않는다.

실패 BEO result 응답의 `result:null`은 그대로다. finalized evidence가 없는 Run도 목록의
spec/status/owner를 읽을 수 있고 Passport 성공은 소유 연결의 전제 조건이 아니다.
Run별 조건·attempt를 묶을 뿐 active/idle/배치를 통틀어 새로운 1..N 회차를 만들지 않는다.

### 12.3 B2 — 저장 원시에서 읽는 배치 CI·통합 p95

`GET .../batch-energy-optimizations/{name}/result`의 기존 `campaign/result`는 그대로 두고
형제 필드 `statistics`와 `statisticsReason`을 추가한다. **sealed `result.detail`을 확장하거나
다시 쓰지 않는다.** CI는 기존 유도 과정에 존재하고 latency vector는 raw에 저장돼 있으므로
생성 경로 변경보다 현재 보존 자료의 읽기 projection을 택한다. result.detail에 통계가 없다는
것만으로 재측정을 요구하지 않는다.

`statistics`는 `{batches:[...]}` 또는 null이며 batch 행은 다음과 같다.

| 필드 | 의미·단위 |
|---|---|
| `batch` | 실제 completed active trial의 batch 크기 |
| `incrementalWorkJoulesPerItem` | 기존 `DeriveCurve`의 독립 active trial별 J/item 추정치 산술 평균. 선택 결과의 평균과 다른 새 목적함수 아님 |
| `activeTrialCount`, `idleTrialCount` | 해당 point에 쓰인 독립 active Run 수와 공유 model-loaded idle Run 수 |
| `confidence` | 기존 `PointConfidence` 그대로: `estimatorVersion,method,level,varianceOfMean,effectiveDegreesOfFreedom,halfWidth`. halfWidth는 J/item, variance는 (J/item)². 유효 자유도 부재는 기존 omitempty 의미 유지 |
| `latencySampleCount` | 동일 batch의 완료된 client 추론 호출 latency vector 원소 수. item 수·Run 수와 다름 |
| `latencyP95Milliseconds` | 해당 batch의 모든 active trial **client 호출 지연 표본을 합친** Gonum empirical 0.95 분위, ms. batch 1건 호출 시간이며 batch로 나누지 않음 |
| `sources[]` | 실제 사용한 기존 `{store,key,size}` 원시/실행/curve-plan 식별자. raw 다운로드 URL이 아님 |

CI는 기존 shared-idle variance와 Welch–Satterthwaite/Gonum Student-t 계산을 그대로 사용한다.
양측 구간은 평균±halfWidth다. 하한을 0으로 자르지 않고 variance=0/halfWidth=0은 유효 관측이다.
`level`은 이 BEO의 기존 pointPolicy confidence 해석값이며 전역 settings나 표시용 95%를 덮어쓰지 않는다.
서로 다른 batch/조건, excluded trial, 재사용 확인 trial을 섞지 않는다. trial별 p95 평균이나
기존 point의 ‘trial p99 중 최댓값’을 p95로 이름만 바꾸지 않는다.

Query API composition root의 읽기 callback은 **저장된 result.completedTrials와 curvePlan만**
따라 기존 verified evidence loader → `profiler/projection` → `DeriveIdleBaseline`,
`VerifyCurvePlan`, `DeriveCurve`를 재사용한다. `statistics.go`의 percentile을 사용하는
좁은 summary 함수를 같은 derivation package에 둔다. 새 통계 구현·quality gate·검색 재실행,
현재 catalog 최신 profile 선택, CR status/result mutation은 없다.
Query API transport가 profiler controller를 import하지 않으며 기존 port/callback 조립 패턴을 따른다.

검증은 저장 자료가 verified 타입으로 들어오는 경계 한 번이다. result의 Run namespace/name/UID,
owner UID·completed reference, plan의 정확 조건과 raw lineage를 기존 검증기로 연결하고 downstream은
그 타입을 소비한다. 새 digest 재계산·문자열 hash 검사·바이트 동일성 gate를 넣지 않는다.
실제 다운로드의 기존 유한 크기 제한은 유지하되 payload 재인코딩을 identity 판정에 쓰지 않는다.
요청 취소/기존 timeout을 지키며 목록·커버리지 전체에 통계를 일괄 로드하지 않는다.

구현(2026-10-30): `cmd/chill-query/measurement_statistics.go`가 retained loader의 동결 store
binding과 기존 Run/원시 검증을 사용한다. `Curve.Statistics()`는 같은 empirical percentile
구현을 재사용하며 공유 idle와 해당 batch의 active 원시·execution, curve-plan만 `sources`에 넣는다.
curve-plan 단일 객체와 retained evidence 객체는 각각 64 MiB로 읽기를 제한한다. 현재 Secret/권한
조회 실패는 원근거 미기록으로 바꾸지 않는다. 공개 경로는 기존 result GET과 ChillServer 프록시를
그대로 사용하므로 새 route·RBAC grant는 없다.

| 상황 | 응답·표시 |
|---|---|
| result reference 없음 | `statistics:null`, `statisticsReason:"NoPublishedResult"`; 기존 result:null 유지 |
| 정상 참조지만 보존 raw/Run이 실제 없음 | `statistics:null`, `statisticsReason:"RetainedEvidenceMissing"`; sealed 결과는 읽히며 ‘통계 원근거 기록 없음’ |
| 이 측정 방법에 client latency/통계 입력이 애초에 없음 | `statistics:null`, `statisticsReason:"StatisticsNotRecorded"`; ‘CI·통합 p95 기록 없음’ |
| 통계 제공 | statisticsReason은 null. 통계가 없는 것을 숫자 0으로 치환하지 않음 |
| 권한·transport/store 실패·깨진 객체·UID/조건 모순 | 기존 오류 전달(403/502/503 등 해당 경계). 기록 없음으로 숨기지 않음 |

### 12.4 B2 — OperationCost 실측 시간·J

기존 list/detail `GET /api/v1/namespaces/{namespace}/operation-cost-measurements[/{name}]`의
`status.steps[]`에 `operationID`, `durationNanos`, `wallJoules`, `reason`을 추가한다.
durationNanos는 nullable 정수 ns(브라우저는 lossless 정수 처리), wallJoules는 nullable number J다.
기존 name/key/verdict, CR 전체 phase/reason, references.evidence는 그대로다.
`durationNanos=record.result.endedAt−startedAt`이며 유효한 실제 양 끝 시각이 없으면 null이다.
planned idleDurationSeconds·watchdog/관측 창 길이를 실측 시간으로 대체하지 않는다.
wallJoules는 저장된 수용된 **전체 Node 벽에너지**이며 증분 J/item이 아니다.
불수용·실패·isolation 미확인에서 기록된 수용 에너지가 없으면 null, 실측 0 J는 0이다.
실패에 실제 시간이 있으면 시간만 제공할 수 있다. 미실행 후속 단계 status 행은 만들지 않는다.

ChillServer measurement service가 CR의 **정확 step key만** 기존 evidence store에서 읽는다.
전체 prefix/latest 비용을 선택하는 `CostEvidence.Refresh`는 실패 이력을 버리므로 재사용하지 않는다.
기존 `serving/kube` operation record decoder/identity 검증을 좁게 재사용하여 CR UID로 만든
operation ID·step kind·Node·request/result identity·실제 창을 한 경계에서 확인하고 안전한 DTO를
`measurement_projections.go`에 전달한다. accepted cost와 idle baseline의 기존 분기를 유지하고,
failed/insufficient history에 Accepted의 완결 조건을 잘못 강제하지 않는다.
raw power·prepare payload/비밀을 HTTP에 복사하지 않는다.
참조 key가 있는데 객체를 못 읽거나 깨졌으면 evidence 오류이지 null/‘미측정’이 아니다.
개별 단계 Accepted를 전체 순서 Complete로 승격하지 않는다.

구현(2026-10-30): `LoadOperationCostSteps`가 저장된 status 순서/이름과 exact key를 읽고,
기존 accepted cost/idle validator를 해당 분기에만 적용한다. list와 detail은 같은 loader를
사용하며 `maxEvidenceBytes`로 객체별 읽기를 제한한다. phase와 verdict는 CR 원문 그대로이고
실패 기록의 actual interval은 보여 주되 accepted 에너지로 승격하지 않는다.
목록은 기존 `errgroup`의 `SetLimit(8)`로 CR별 projection을 최대 8개 동시 실행하고,
미리 정렬한 index에 결과를 기록해 이름 순서를 보존한다. 각 CR의 단계 읽기는 원래 순서이며,
어느 projection이든 실패하면 요청 전체가 오류로 끝나고 나머지 읽기에는 취소가 전달된다.

## 13. 측정 커버리지 읽기 projection (U1)

**2026-10-30 교차 모델 검토 반영, U1 서버 구현. 배포·성능 수용 전.** [5차 계획](../../plans/2026-10-31-dashboard-measurement-redesign.md)과
[Dashboard §9](../dashboard/README.md#9-운영-중심-측정실험-5차-재설계)가 소비한다.
사용자는 엣지 클러스터 운영 담당자다. 화면은 ‘스케줄러가 쓸 수 있는가, 운영에 쓰기 전에 무엇이 부족한가’를 답한다.
§12의 resolver·owner·보존 통계·단일 writer를 재사용한다. CRD/spec/status/result/raw/frozen bundle/
publication·catalog 채택·admission·통계 추정 정책은 변경하지 않는다. 새 필드는 HTTP/메모리 projection이다.

### 13.1 소유권과 중복 I/O 제거

집계·공개 profile 조립·목록 assessment·정적 호환성·보존 결과 조회는 **Query API** 소유다.
ChillServer는 인증·권한·namespace 제한 후 완성된 응답을 opaque 중계한다.
Serving의 atomic catalog/adopted profile 사실은 인증된 `/api/v1/scheduler/catalog-read`에서
`serving/kube.CatalogRead`로 제공한다. raw duration·전체 measurementFacts를 보내지 않고
명시적 profile 요약·필요한 bounded reporting input·source 시각/revision·adopted ID만 넘긴다.
Query API는 scheduler/catalog를 새로 만들거나 채택 profile 전체의 `/result`를 크롤링하지 않는다.

```text
브라우저 GET → BFF → ChillServer TokenReview + URL/namespace SAR → Query API 완성 응답
  ↑ serving owner typed catalog read / namespace BEO·Run metadata / 기존 inventory·정적 지원
catalog 밖 단건 → Query API /result → retained loader·같은 통계 helper
```

- `internal/serving/kube/catalog.go:readProfile`은 이미 result/curve-plan/active·idle raw를 읽는다.
  **그 로딩 안에서** 기존 DeriveIdleBaseline/Aggregate/통합 client percentile helper를 재사용해
  batch별 CI/p95/W/총·증분 J/반복·품질을 한 번 산출한다. 원시 vector는 산출 후 버리고 복사·재다운로드하지 않는다.
  기존 `catalogBatch.add`가 client vector를 버리기 전에 pooled client p95를 산출해야 하며 native duration으로 대체하지 않는다.
- `CatalogView`의 메모리 sidecar `measurementFacts`(profile/result identity key)에 보존하고
  `ProfilesView` snapshot과 함께 publish한다. immutable NativeBundle/CR/result JSON에는 새 필드를 쓰지 않는다.
  현재 catalog에 없는 superseded·scope 밖 단건은 기존 Query API result GET에서 같은 helper로 계산한다.
  이 단건 비용을 전체 coverage 준비의 선행 조건으로 만들지 않는다.
  검증된 catalog 읽기 projection은 비용 source 갱신 전에 publish하여 비용 source 장애가
  BEO 커버리지를 막지 않는다. 이는 스케줄러 채택을 앞당기지 않으며, 아직 채택하지 않은
  검증 결과는 `scope.schedulerUsed:false`로 구분한다.
- catalog 제공 경계에 **마지막 source 확인 성공 시각·마지막 오류·revision**을 추가한다.
  정상 무변경 refresh의 조기 반환도 성공 확인 시각을 갱신하고 실패는 마지막 성공 시각을 유지한다.
  atomic pointer를 지금 읽은 시각이나 configVersion만으로 fresh를 선언하지 않는다.
- Query API 호환성 행렬은 한 호출 안에서 inventory LIST 객체를 seed한 **Get memo reader**를 사용한다.
  model/class/Node/ConfigMap의 고유 identity별 Get을 한 번만 수행하고 fact-source ConfigMap은 한 번만 strict 파싱한다.
  기존 selector/`selectExecutionDeclaration`/modelcompat 판정과 취소를 유지하며 **8개 동시 resolver 안은 삭제**한다.
  대표뿐 아니라 해당 inventory의 모든 실제 Node 판정을 같은 입력 snapshot에서 계산해 두므로
  `node` 선택 GET에 새 resolver I/O가 없다. 입력 UID/RV가 달라지면 다시 계산하며 heartbeat를 지원 변경으로 과장하지 않는다.
  기존 공개 matrix의 `rows`는 대표 Node 의미를 유지한다. 같은 응답의 새 `nodeRows[]`는
  B1 응답 shape로 model/path/class/실제 Node 각각의 판정을 제공하며 coverage만 이를 index한다.
- `cmd/chill-query`에 `--kube-api-qps`, `--kube-api-burst`를 추가하고 양수·유한 범위를 검증해 REST config에 적용한다.
  기본값은 기존 ChillServer와 같은 200/400, 배포에서는 실제 설정과 API-server 부하를 기록한다.
  높은 QPS는 memo/단일 파싱의 대체가 아니며 기본 client QPS에 대한 패널 간 다른 추정을 성능 근거로 쓰지 않는다.
  구현 설정은 Helm `query.kubeAPIQPS`/`query.kubeAPIBurst`에 노출한다. 행렬 composition root는
  inventory LIST seed와 fact-source GET의 UID/RV 집합이 같으면 기존 `rows`/`nodeRows`를 재사용한다.
  성공 조회 시각만 바뀐 경우 지원 판정을 재계산하지 않는다. 성능·배포 수용은 §13.6의 별도 실측 대상이다.
- Run은 namespace LIST 한 번에서 UID/owner index를 만든다. 공개 page마다 전체 LIST를 반복하지 않는다.
  owner는 §12.2의 정확 controller UID 규칙이다. operation 전체 evidence scan은 coverage에 넣지 않는다. DOM은 namespace LIST와 각 CR의 정확한 result ref만 읽고 prefix/raw scan은 하지 않는다(§13.8).
- 순수 공통 판정/DTO는 `internal/queryapi/measurement_coverage*.go`에 둔다.
  `internal/serving/httpapi`는 queryapi을 직접 import하지 않는다. `cmd/chill-query` adapter가
  공통 판정을 transport DTO에 연결한다. 기존 package 방향과 `.go-arch-lint.yml`은 변경할 필요가 없다.
  기존 목록·단건에도 동일 reducer의 assessment를 전달하므로 단건 조회가 전체 coverage grant/준비에 종속되지 않는다.

### 13.2 경로·필터·RBAC

`GET /api/v1/namespaces/{namespace}/measurement-coverage`

서버 query는 **`batches`, `node`만** 허용한다. batches는 중복 없는 오름차순 양의 정수 CSV,
node는 현재 inventory의 정확 이름이다. 각 query는 한 번, 빈/중복/unknown key·범위 오류는
400 `InvalidCoverageQuery`다. model/class/path/mode/status/CPU 검색은 클라이언트 표시 필터와 URL 상태다.
선택 query는 namespace base snapshot의 가벼운 projection이며 raw scan을 시작하지 않는다.
node별 지원 입력이 아직 없으면 Unknown과 사유를 반환하고 대표 Node 지원을 복제하지 않는다.
목표 batches는 실제 요청/지원 집합과 별개다. 생략하면 `goalBasis:"requested"`, 명시하면 `"explicit"`이다.
무한 CPU/dataset/image 조합이나 임의 모델 최대 batch를 분모로 만들지 않는다.

cache 접근 전 기존 TokenReview와 정확
`get /chill/api/v1/namespaces/{namespace}/measurement-coverage` URL SAR를 수행한다.
이 grant는 inventory와 해당 namespace의 catalog/측정 요약을 읽는 명시적 권한이며 다른 URL grant에서 유추하지 않는다.
또한 기존 `h.authorizer.AuthorizeResource`로 **같은 namespace의 `edge.dacs.io/v1alpha1`
`batchenergyoptimizations`, `profilingruns`, `dispatchoccupancymeasurements` list**를 검사한다. 단순 URL ClusterRole만으로 namespaced Role을 우회하지 않는다.
`measurementService.namespace` 허용 범위도 유지한다. 다른 namespace reuse 원본은 별도 권한 없이 노출하지 않는다.

| 상황 | 응답 |
|---|---|
| 토큰 없음/무효 | 401, cache 데이터 없음 |
| URL/namespace resource SAR 거부 | 403, cache 데이터 없음 |
| 인증/SAR 서비스 장애 | 503 `AuthorizationUnavailable`, 오래된 데이터도 제공하지 않음 |
| 초기 필수 입력 없음 | 503 `CoverageWarming`, `Retry-After: 2`; 빠른 오류를 사용 가능 응답으로 세지 않음 |
| 일부 source 갱신 실패 | 200 partial, source 오류/영향 범위·마지막 기준 시각 보존 |
| 유효 stale 범위 밖 필수 입력 없음 | 503 `CoverageUnavailable` |

HTTP `Cache-Control: no-store`, BFF allowlist에 정확 GET 및 응답 `Retry-After` 전달을 추가한다.
write wildcard/자동 재요청/raw export 권한은 추가하지 않는다.

### 13.3 응답 schema

시각 공개 필드는 **`dataAsOf`와 `freshness`만** 둔다. dataAsOf는 필요한 source들의 마지막
성공 확인 시각 중 가장 오래된 값(RFC3339Nano UTC), freshness는 `fresh`/`stale`이다.
source별 마지막 확인 시각은 내부 snapshot에서 보존하고 HTTP에는 version/오류만 보낸다.
atomic 다중 source snapshot이라고 주장하지 않는다. null은 이유 있는 미제공이며 관측 0이 아니다.

| 필드 | shape / 의미 |
|---|---|
| `schemaVersion`, `snapshotId`, `assessmentVersion` | `"measurement-coverage.v1"`, namespace revision, `"result-checklist.v1"` |
| `dataAsOf`, `freshness`, `completeness` | 기준 시각, fresh/stale, complete/partial |
| `scope` | `{namespace,supportBasis:"representative-node-static"\|"selected-node-static",goalBasis:"requested"\|"explicit",targetBatches:[]}` |
| `sources[]` | `{kind,resourceVersions,complete,error:{code,message,retryable}\|null}`. inventory/compatibility/catalog/BEO/Run/DOM/input-contract 구별 |
| `summary` | supportedCellCount, completeCellCount, criteriaMetCellCount, reusedCellCount, unknownSupportCount, unsupportedCellCount, countsByDisplayStatus, requestCount, runCount, profileCount, failedCellCount, currentFailedRequestCount, historicalFailedRequestCount. 미확정 수 null |
| `groups[]` | 모델×class `{model:{name,uid},deviceClass:{name,uid},cellIds:[],complete,supported,unknownSupport}`. 장치 열 이름순 고정 |
| `cells[]` | 아래 Cell |
| `entities` | requests/profiles는 `{uid,id,status,label,cellIds}`, runs는 `{uid,status,label,cellIds}`인 scalar 화면 요약만 제공한다. assessment/checks/resultRows/원문/log 없음 |
| `nextActions[]` | `{kind,cellId,variantId,entityId,reasonCode,message}`. 미해결 실패/부족 batch/근거 확인만 각 최대 3건 |

operationCosts는 coverage index에서 제외한다. dispatch occupancy는 셀의 `occupancy[]`로 제공하며,
기존 각각의 list/detail response에는 동일 reducer assessment를 붙인다. DOM 근거 읽기 실패는 원래 assessment 사유로 남고 BEO profile 판정을 바꾸지 않는다.
Run 상세도 기존 허용 조회 경로에서 assessment를 얻을 수 있어야 하며 aggregate grant를 강제하지 않는다.
모든 entities의 status는 서버의 화면 displayStatus이며 label도 서버 값이다. 원본 phase/세부 사유는 기존 단건에서 읽는다.
catalog 내부 메모리 facts 타입은 기존 serving 경계에 두고 cmd adapter에서 공통 HTTP DTO로 변환한다.
catalog/kube가 queryapi transport를 import하도록 package 예외를 추가하지 않는다.

**Cell**: model UID × class UID × path × mode다. 각 이름/UID와 `support`, `assessment`,
`variantCount`, `variants`, nullable `currentMeasurement`/`batchProgress`/`representative`, `failureSummary`, `history`를 갖는다.
support는 `{verdict:"Compatible"|"Incompatible"|"Unknown",node:{name,uid}|null,reasons:[],diagnostics:[]}`다.
기본 현재 선택은 셀(model/class/path/powerMode)별 `measuredAt` 최신 하나(A)다.
`currentMeasurement={id,model,deviceClass,executionPath,powerMode,measuredAt}`는 공통
`derivation.CurrentMeasurements` 결과 그대로이며 후보 부재 시 null이다. 동률은 ID 사전순 최소다.
`currentMeasurement.id`는 namespace/name/BEO UID이고, `variant.id`는 preflight에 전달할 동결 조건
fingerprint다. `variant.requestIds`가 두 identity를 연결하며 같은 조건의 반복 측정도 선택 전에는 합치지 않는다.
현재 variant `{id,condition,measuredAt,assessment,batchProgress,resultRows,requestIds,runIds,profileIds}`만
셀 상태·대표값·배치 집계에 사용한다. 최신이 failed/partial이면 그대로 표시하며 과거 성공으로 대체하지 않는다.
`batchProgress={basis,target,measured,criteriaMet,missing,inProgress,unknown}`은 현재 측정의 batch 정수 배열이다.
대표값 `{variantId,batch,incrementalJoulesPerItem,p95Milliseconds}`도 같은 실제 batch에 한정한다.
이미지 버전은 profile 유효 조건이 아니다. runtime·artifact·preprocessor·observer 이미지의
당시 원문은 보존하되 variant fingerprint와 preflight 기측정 배치 비교에서 제외한다.
CPU quota/affinity는 현재 선택 키가 아니다. 과거 측정은 `history`와 기존 profile/raw 단건 조회에 남고,
공개 profile 기록과 셀의 현재 선택은 분리한다. 현재/이전 이미지 선택 분기는 없다.
시각은 검증된 Complete profile의 raw 실행 완료 시각, 없으면 현재 generation terminal BEO의
`MeasurementComplete` condition LastTransitionTime이며 둘 다 없으면 zero다. 생성 시각을 대신 쓰지 않는다.
`chill.dacs.io/replaces-batch-energy-optimization`의 같은 namespace 대상은 최신 선택보다 먼저 제외한다.
coverage 캡처에 없는 annotation은 기존 BEO 목록의 `metadata.annotations`에서 읽는다.
선언 미독 시 support Unknown을 보존하고 확인된 지원 선언 충돌은 `CurrentDeclarationConflict`로 표시한다.
usable/problem 평가는 사실 표시이며 서빙·profile 선택·요청 진행의 추가 거절 조건으로 쓰지 않는다.
worker 실행에는 현재 class/node/backend 선언의 최신 이미지를 쓴다.
failureSummary는 정확 request/Run identity·batch·code·exitCode·최대 160자 한 줄 원인·후속 relation,
선택적 `firstErrorLine`(ANSI 제거·최대 160자)과 `firstErrorTime`(로그에 적힌 시각 원문)을 제공한다.
첫 ERROR/FATAL/panic/error= 줄만 보여 주며 경고를 원인으로 추정하거나 시각·시간대를 생성하지 않는다.
긴 diff/log는 기존 상세에 둔다. 정확 후속만 verified-follow-up, 유사 조건은 related-candidate다.

6차 보정: Run UID는 `runId`, 직접 상세 링크는 `runNamespace`/`runName`과 함께 사용한다.
정확 controller 소유 회차만 최신순으로 결합하며 runtime 종료 로그의 오류 한 줄(최대 160자)과
exitCode를 셀·요청 entity·요청 목록 assessment·Query API 요청 상세 assessment의 `failureSummary`에 제공한다.
실패 요청의 미게시 결과 사유는 `RequestFailed`이며 대기 상태로 해석하지 않는다.

`GET /api/v1/scheduler/profiles`는 `limit`(기본 25, 최대 50), `offset`, 정확 이름
`model`/`deviceClass`, 정확 ID `profile`을 서버에서 먼저 적용하고 `page`와 전체 선택지
`filters:{models,deviceClasses}`를 반환한다. `resultRows`/assessment/동결 조건/출처는 유지하되
원시 `durationNanos`와 내부 `measurementFacts` 전체 맵은 공개 응답과 중간 직렬화에서 제외한다. `preparations` restore 계약은 보존한다.
스케줄러 메모리·원본 표본은 변경하지 않는다. 프로파일 선택은 페이지와 별도 `profile` 조회로 가능하다.
회차 목록도 q/condition 외 model/deviceClass/node/path/powerMode/status/parentUID/group을
전체 snapshot에 적용한 뒤 페이지를 나눈다. group은 batch를 제외한 기존 조건 JSON의 의미 비교다.

#### control-323 최초 조회 크기 보정

Query BEO 목록은 **명시적 `limit` 또는 `offset`이 있을 때만** 이름순으로 페이지를
나눈 뒤 해당 요청의 전체 native spec/status와 assessment를 투영한다. `limit`은 1~50
(기본 25), `offset`은 0~1,000,000이며 반복·알 수 없는 query는 거부한다.
`page:{offset,limit,total,nextOffset?}`의 마지막/범위 밖 페이지에는 다음 offset이 없다.
무쿼리 native와 페이지 인자 없는 `view=summary`는 기존 전체 목록 의미를 유지한다.
대시보드 공통 BEO reader는 **50개씩** 실제 nextOffset을 끝까지 따라간다. 정지한 페이지·
중간 실패·취소를 부분 목록 성공으로 바꾸지 않으며 재조회 실패에는 직전 완전한 성공 자료와
작성 Funnel의 초안·고정 이름·접수 미확인 제출 차단 상태를 유지하고 갱신 실패를 표시한다.
401/403의 기존 인증 캐시 비노출 정책은 유지한다.
d 요청은 현재 coverage 셀의 measurement ID→BEO/Node와 전체 셀 선택에, 프로파일 요청은
복사 원본·동결 프로토콜·동일 조건 기본값·입력 출처에 전체 목록이 필요하므로 앞부분을
자르지 않는다. 요청 목록·상세의 실패 복구·삭제 결과 재확인도 같은 reader를 사용한다.

`view=summary&summary=v2`를 명시한 요청만 `measurement-summary.v2`로 인코딩하여
경로 문자열의 `/` 구분 요소를 공유한다. 플래그가 없으면 기존 v1 인코딩이다.
`{"$s":["~N",...]}`은 v2에서만 앞선 **원문 문자열** dictionary 항목을 `/`로 연결한다.
빈 요소·Unicode·문자열 `~123`도 원문대로 복원하며 객체/배열/이미 연결된 문자열을 요소로
참조할 수 없다. 원본 `$s` 객체는 `$o` shape로 이스케이프한다. 기존 values/shapes 형식과
정밀 정수·null·0·모든 유지 필드의 의미는 같다. 디코더는 기존 노드/깊이 상한 외에 연결로
할당하는 문자열을 응답당 8Mi UTF-16 단위로 제한한다. 셀·해상도·값 생략은 추가하지 않는다.

운영 HTTPS **GET만** 수집한 2026-11-03 원자료를 로컬 인코더에 넣은 비교다.
배포 후 실측이나 wire 전송량이 아니라 디코딩된 body 바이트의 배포 전 추정치다.

| 최초 진입 응답 | 운영 GET (B) | 로컬 새 인코더 (B) |
|---|---:|---:|
| BEO summary 전체 279개 → 첫 50개 페이지 | 1,785,760 | 250,190 |
| coverage summary 전체 598셀 유지 | 1,210,905 | 887,602 |
| profiles summary 50개, offset=100 | 974,265 | 751,151 |

BEO 전체를 새 dictionary로만 바꾸면 1,344,715 B여서 페이지도 필요하다. 6페이지 모두
1,000,000 B 미만인지 운영 fixture 회귀가 확인한다. coverage는 dictionary 원문 문자열
8,753개가 688,377 B, 객체가 409,469 B, 배열이 108,656 B를 차지했다. 복원된 variants가
셀 본문의 4,293,506/5,035,075 B로 대부분이며, 경로 요소 공유만으로 전체 셀을 보존한다.
같은 원자료에서 occupancy를 제거한 구 인코더는 1,202,160 B, 포함하면 1,210,903 B
(운영 body와 2 B 차이)여서 occupancy의 압축 후 증분은 8,743 B다. 직전 관측
1,165,766 B 대비 증가 45,139 B 전부를 occupancy 추가로 단정할 수 없다.
DOM의 포화 판정·idle 필드와 측정 프로토콜은 이 보정에서 변경하지 않는다.

대시보드 relay는 BEO의 limit(1~50)·offset(0~1,000,000), BEO/coverage/profiles의
`summary=v2`를 실제 allowlist에서 검증한다. 신 대시보드는 v1과 v2를 모두 읽고, 구 Query의
400 `InvalidQuery`/`InvalidCoverageQuery`에만 v2를 제거해 재시도한다. BEO는 구 Query에
limit/offset도 제거한 **전체 v1** 읽기를 사용한다. 중간 페이지에서 구 Query로 바뀌면 받은
부분 페이지를 전체 v1과 중복 합치지 않는다. 권한 오류·503·취소는 이 fallback 대상이 아니다.

| 배포 조합 | 보존 동작 |
|---|---|
| 구 대시보드·열린 구 탭 + 신 Query | 플래그 없는 v1 및 무페이지 전체 279건 유지 |
| 신 대시보드 + 구 Query | 명시적 협상 거절 후 기존 v1 계약으로 읽기; 값·279건 유지 |
| 신 대시보드 + 신 Query | v2 + 명시적 50개 페이지, 전체 598 coverage 셀 유지 |

**권장 순서: Query 먼저, Dashboard 나중.** 어느 순서도 자료/작성 상태를 손상하지 않지만
구 Query fallback 기간에는 기존 1MB 초과 응답이 유지된다. 1MB 목표는 양쪽 신버전 적용 후다.
실제 e00f891e 디코더에 신 Query의 v1 운영자료 인코딩을 넣고 coverage/BEO/profiles의 모든
유지 값과 failure 링크 처리를 확인했다. 신 Dashboard의 실제 relayPlan 경로 회귀는 구/신
Query 계약 및 중간 다운그레이드, 400 협상 거절과 403/503 비재시도를 검사한다.

운영 BEO 279건의 50개 페이지 로컬 인코딩 단일 측정(HTTP·목록/Run/catalog 읽기 시간 제외):

| offset | 항목 수 | v2 body (B) | 인코딩 (ms) |
|---:|---:|---:|---:|
| 0 | 50 | 250,190 | 276.023 |
| 50 | 50 | 294,117 | 401.841 |
| 100 | 50 | 252,476 | 351.701 |
| 150 | 50 | 234,342 | 286.625 |
| 200 | 50 | 208,585 | 230.450 |
| 250 | 29 | 100,292 | 136.921 |

요청 수는 25개 기준 12회에서 6회, 로컬 순수 인코딩 합계는 1,683.561ms다.
이는 배포된 화면 전체 지연 측정이 아니다. 각 페이지의 서버 목록·Run·catalog 읽기와
네트워크 왕복은 별도이므로 운영 지연에 문제가 없다고 단정하지 않는다.


분배 점유는 한 보존 파일의 누락·해석 실패가 목록 전체 503이 되지 않는다.
해당 행·단건을 `OccupancyEvidenceMissing`/`OccupancyEvidenceUnavailable`/`OccupancyEvidenceInvalid`
문제로 명시하고 채택 여부는 false로 유지하며 원본 native 수치·위치를 보존한다.
실제 6차 조사에서 과거 Complete 두 건의 S3 GET `NoSuchKey`(404)가 전체 목록 503을 유발했고,
최근 보존된 단건은 200이었다. 누락 파일을 재생성하거나 상태를 성공으로 덮어쓰지 않는다.

Query API 사전 검사의 cluster-scope 권한은 기존 ModelSpec/DeviceClass/Node GET에
Preprocessor/Dataset GET·LIST와 namespaced 비용/점유 GET·LIST를 조회 소유권에 맞게 둔다. fact-source ConfigMap과 BEO 목록 권한은
기존 제한 범위를 재사용하며 쓰기 권한은 추가하지 않는다. Serving catalog read는 전용 정확 non-resource GET grant를 사용한다.

### 13.4 단일 판정과 화면 상태

`MeasurementAssessment={status,displayStatus,label,reasons,execution,evidence,checklist,scope}`다.
execution은 원문 phase/generation/observedGeneration, evidence는 available/accepted와 사유,
checklist는 `{criteriaMet:true|false|null,version:"result-checklist.v1",checks:[{code,result,reason?,message,sourceIds}]}`다.
check result는 pass/warn/fail/unknown이다. **서버가 displayStatus와 label까지 제공**하며 UI는 다시 매핑하지 않는다.

coverage 응답에서는 **variant.assessment.checklist.checks에만** 전체 체크리스트를 한 번 둔다.
동일 `(code,result,reason,message)` 항목은 variant 안에서 합치고 `sourceIds`는 해당 항목에 기여한
정확한 `ResultRow.id` 집합이다. 원본 기록은 해당 `resultRows[].sources`로 찾아간다.
Cell과 ResultRow의 assessment는 status/displayStatus/label, execution/evidence, reasons/scope와
checklist의 criteriaMet/version을 유지하되 `checks:[]`로 요약한다. 이 compact projection은
coverage 전용이며 기존 단건 assessment의 체크리스트 정책을 바꾸지 않는다.
모든 variant의 condition과 resultRows 전체를 보존하며 행·variant를 잘라 용량을 맞추지 않는다.
scheduler compact 읽기의 `$ref` 복원은 top-level Profile dictionary 전용이다.
coverage는 위 variant 체크리스트 중복 제거를 사용하며 별도 `$ref` parser가 필요하지 않다.
직렬화 회귀는 1,200회차·90요청·80프로파일·240결과 행의 실제 projection을 만들고
8MiB 응답 한도의 절반인 **4MiB 미만**과 모든 entity/행/수치/출처의 보존을 함께 검사한다.

| 세부 status | 화면 displayStatus / label | 판정 |
|---|---|---|
| `complete` | `usable` / **사용 가능** | 채택된 정확 근거와 해당 목표 batch 측정 충족. 실행 phase=Complete만으로 만들지 않음 |
| `criteria_met` | `usable` / **사용 가능** | complete이며 아래 결과 체크리스트도 충족. 상세 사유 행에 **기준 충족** 표시 |
| `review` | `review` / **검토 필요** | 채택 근거는 있으나 판정 가능한 입력 계약·품질 체크에 fail이 있음. unknown만으로 만들지 않음 |
| `partial` | `insufficient` / **부족** | 목표 batch/선택 Node 일부 부족. 실제 부족 집합 명시 |
| `warning` | `problem` / **문제** | 실제 catalog 기각·근거 파손/누락·현재 선언 충돌·음수/비유한 d 등 해결할 문제 |
| `failed` | `problem` / **문제** | 사용 결과 없이 명시적 실행 실패. 사유 행은 ‘실행 실패’와 실제 원인 |
| `running` | `running` / **진행** | 사용 결과 없이 현재 generation의 실행/대기가 명시됨 |
| `unmeasured` | `unmeasured` / **미측정** | 지원 Compatible·필수 목록 완전·해당 시도 없음 |
| `unsupported` | `unsupported` / **지원 안 함** | resolver가 입증한 불일치이며 사용 근거 없음 |
| `unknown` | `unknown` / **확인 불가** | 필수 source/identity/phase 불명. 기존 검증 결과는 지우지 않고 미확인 범위만 남김 |

화면의 기본 상태는 **사용 가능/검토 필요/부족/문제/진행/미측정**, 지원 안 함·확인 불가는 별도다.
세부 실행 상태는 별도 배지 벽으로 노출하지 않고 사유 행/용어 도움말 한 곳에서 설명한다.
superseded와 notInCatalogScope는 정상 측정 결과의 `complete`+보조 사실이며 ‘현재 스케줄러 미사용’을 명시한다.
실제 rejected/깨진 근거와 혼동하지 않는다. 완료 수 complete+criteria_met, 기준 충족 수는 부분집합,
재사용 완료 수는 따로 표시한다. 새 실패가 최신 현재 측정이면 셀도 그 실패를 유지한다.
이전 성공은 history에 남기며 현재 사용으로 승격하지 않는다. idle 3회를 active 3회로 세지 않는다.
여러 자식은 실제 문제→부족→검토 필요→전부 기준 충족→전부 complete 순으로 요약하고, 판정 불가 자식은
미확인 수/범위를 보존한다. unknown 통계·시각 부재만으로 채택 결과의 상태를 내리지 않는다.

처리 성능 결과 목록·상세의 `scope.schedulerUsed`는 catalog의 `currentMeasurements[].id`와
정확히 일치하는 profile에만 true다. reuse는 `confirmationProfileID`로 대조한다.
조건이 같은 과거 profile, 최신 실패 때문에 선택되지 않은 과거 성공은 false이며 화면에는
`과거 측정`으로 표시한다. 상세는 서버의 `label`(예: 사용 가능)을 그대로 측정 상태에 두고,
품질 판정·현재 사용을 독립 축으로 표시한다. 품질 unknown을 서버 사용 가능의 대체 판정으로 쓰지 않는다.

요청 목록과 단건 `/batch-energy-optimizations/{name}/result`의 현재 사용은 accepted catalog에
해당 게시 profile이 존재하고 `currentMeasurements[].id`가 요청 `namespace/name/UID`와 정확히
일치할 때만 true다. 최신 실패가 현재로 선택되어도 게시 profile이 없으면 false다. 재사용은
확인 요청 자신의 identity로 대조하며 원본 참조 행의 현재 사용을 상속하지 않는다.
catalog/선택 정보를 읽을 수 없으면 `scope.schedulerUsed:null`이며 조건 일치로 false/true를
추정하지 않는다. 이는 조회 시점의 선택 사실이고 봉인된 결과·그 기준 시각은 바꾸지 않는다.

**결과 체크리스트 v1**: 정확 조건/lineage, 목표 batch 충족, 독립 active/shared idle 각각
`max(3,동결 MinimumTrials)` 반복, 유한한 기존 CI/level(0폭 허용), pooled client p95·N>1,
모든 사용 trial의 동결 전력창/표본/gap/실패 정책 통과, 실제 전력 출처를 확인한다.
미조회 체크는 unknown이지 pass가 아니다. fail은 review이며 unknown/warn만으로 상태를 내리지 않는다.
`DatasetInputContract`는 현재 Dataset UID·동결 전처리기 이미지/경로·모델 산출물 연결이 확인될 때
기존 raw 필드 호환성(이름·형식·상한)을 대조한다. 추가 필드나 데이터셋 이름으로 용도 불일치를 추정하지 않는다.
**sourceTimestampAbsent와 node-wall은 complete/criteria_met 어느 쪽의 판정 게이트도 아니다.**
전력원 바로 옆에 ‘관측 시각은 수집 시각’, ‘노드 벽전력이며 장치 단독 전력 아님’ 고정 각주를 둔다.
미관측 발열/격리를 정상으로 꾸미지 않는다. reuse 행은 complete일 수 있으나 criteria_met는 아니다.
원본 CI 중첩만으로 유의성을 판단하지 않으며 새 검정/품질 gate를 만들지 않는다.

### 13.5 결과 행·measurementFacts·요청 검사

`ResultRow/resultRows[]`는 정확 variant × batch 한 행이다. 조건(model/class/Node UID·mode/path/
image/backend·batch·공유 runtime·dataset/preprocessor UID·protocol), p95Milliseconds,
latencySampleCount, idleWatts, activeWatts, totalJoulesPerItem, incrementalJoulesPerItem,
confidence, activeTrialCount, idleTrialCount, completedItems, completedCalls, powerSource,
assessment, sources, missingReasons를 포함한다.
catalog가 이미 로드한 verified trial의 동일 shared-idle derivation·산술 평균/pooled client percentile을 사용한다.
CI는 증분 J/item에만 적용하고 평균±halfWidth의 음수 하한/0폭을 보존한다.
총 J/item은 EnergyTrial.ActiveWindowWallJoulesPerItem 평균, active W는 같은 trial 유효 적분/실제 창 평균이다.
서로 다른 회차 처리량으로 W를 나누거나 idle W를 총 J에 단순 더하지 않는다.
재사용은 **대상 confirmation 독립 실측 행 + sourceProfile 참조 행** 두 종류로 구분하고
각각 rowKind/Node/profile/source/reuse 상대 차이를 둔다. 원본 에너지와 대상 W를 섞은 총 J는 null이다.
두 행이 대상의 독립 전체 실측인 것처럼 내보내지 않는다.

catalog 밖 단건 Query API `/result`는 기존 result/statistics를 유지하고 nullable
`measurementFacts`와 `measurementFactsReason`을 형제로 제공한다.
facts는 `{request,resultReference,condition,pointPolicy,batches:[ResultRow의 사실 필드]}`다.
NoPublishedResult/RetainedEvidenceMissing/StatisticsNotRecorded와 실제 권한·transport·identity 오류를 구별한다.
채택 profile 상세는 coverage/catalog의 같은 resultRows를 재사용하고 Query API 중복 로드를 하지 않는다.
raw/vector/log/Secret은 응답에 넣지 않는다. 기존 8MiB 한도는 유지하며 넘는 자료를 몰래 잘라 정상으로 표시하지 않는다.

구현: 공통 `AssessMeasurement`가 개별 요청/Run/동작 비용/점유와 커버리지의 상태·label을
산출한다. `MeasurementResultRow`는 catalog와 retained 단건의 같은 사실을 투영하며,
공유 idle 반복과 active 반복을 별도로 센다. 현재 frozen `RunConfig`와 curve-plan의 protocol에는
수집 설정만 있고 표본/gap/실패 **판정 정책**이 없다. 공통 quality 응답 projection은
`policyPassed:null, policyReason:"PolicyUnset"`으로 **정책 미설정**을 명시하며
`FrozenPowerWindowPolicy`는 unknown을 유지한다. 이는 기준이 있는데 평가를 생략했다는 뜻이나
불합격이 아니다. 저장 계약·원본 evidence·관측 수치는 바꾸지 않으며, 현재 관측값에 새 정책을
소급해 기준 충족으로 만들지 않는다. timestamp 부재/node-wall은 이 unknown의 원인이 아니다.
이 `policyReason`은 Query 체크의 `reason`으로 전달한다. `PolicyUnset`은
`result:"unknown",reason:"PolicyUnset"`이며 화면의 `정책 미설정`이다.
사유 없는 null 판정은 일반 미확인, 명시적 false는 기준 미충족으로 유지한다.
coverage 집계는 측정 근거가 있는 조합만 품질 분모에 넣고 정책 미설정·그 밖의 품질 미확인을
구분한다. 측정 근거 없는 조합은 별도 수로 표시한다(예: 근거 있는 48 중 정책 미설정 48,
측정 근거 없음 5). 체크가 없다는 이유로 기준·반복·표본 전체가 없다고 추정하지 않는다.
정상 superseded의 이미 검증한 메모리 facts는 현재 스케줄러 미사용으로 보존하며,
읽기 전 replacement 제외에는 확인하지 않은 과거 facts를 만들어 넣지 않는다.

기존 preflight 응답에 표시용 `batchPlan`을 추가한다:
`{variantId,conditionMatch,requestedBatches,requestableBatches,plannedBatches,measuredBatches,inProgressBatches,
missingBatches,subsetAllowed,searchMayStopEarly,reasons}`.
conditionMatch는 exact/planned-only/unknown이다. 이미지 외 측정 조건 미확정은 exact로 승격하지 않는다.
셀에서 variantId를 전달하고 입력을 그대로 유지할 때만 그 missing을 초깃값으로 쓴다.
CPU/dataset/protocol 등 변경·선언 UID 변경은 이전 자동 제외/검사를 무효화한다.
검사에서 서버가 최신 요청 spec/정확 profile을 대조하고 planned-only 중복은 별도 사실로 보인다.
전달은 `POST .../preflight?variantId=<id>`의 선택 query이며 native CR 본문에 새 필드를 넣지 않는다.
preflight exact는 보존 CurvePlanSpec의 이미지 이외 실제 identity와 현재 요청
spec(배치 선택 제외)이 모두 일치할 때 제공한다. 이미지 tag/digest 차이나 불변 digest 부재로
측정 배치를 무효화하지 않는다. 이 저장 계약에 없는
ModelSpec/DeviceClass/preprocessor 선언 UID를 과거 값으로 추정하지 않으며,
해당 lineage는 확인되지 않은 한계로 reasons에 명시한다.
진행 batch는 UID-bound 동결 curve-plan까지 일치한 현재 실행에서만 세며,
spec만 같은 요청은 planned-only 사유로 분리한다. missing은 측정·정확 진행 집합을 제외한 목표다.
`declaredBatchSizes` 경로는 부분집합 지정 불가: UI batch 칩 읽기 전용, 기존 batchSizes 생략 계약 유지,
‘요청 가능 범위’와 ‘실제 실행 예정 범위’, 중복 가능성·조기 종료 가능성을 펼쳐 확인받는다.
subsetAllowed인 경로에만 **‘부족 배치만 요청’**을 쓴다. 모든 batch 실제 실행을 보장하지 않는다.
지원 Unknown은 이유 코드별 담당 행동(선언 누락이면 관리자 설정 확인)을 제공한다.
이름 suffix는 검사 통과 때 고정하고 응답 미확인 후 자동 재생성/재제출하지 않는다.

### 13.6 cache·신선도·성능 수용

namespace별 프로세스 메모리 base snapshot 한 개, atomic 교체, namespace당 갱신 한 번만 둔다.
진입/명시 갱신에서 30초 fresh TTL을 적용하고 stale 최대 5분, 별도 DB/Query API crawler/worker pool은 없다.
지원 snapshot은 입력 identity/RV가 바뀔 때만 재계산한다. catalog는 기존 refresh 흐름과 성공 확인 시각을 쓴다.
동일 서버 namespace TTL 안의 여러 탭은 한 갱신을 공유하고 브라우저 **30초 polling은 하지 않는다**.
재진입/명시적 갱신만 GET한다. 공통 조회·변경 경계에서는 HTTP 503이며 정확히
`CoverageWarming` 코드인 응답만 `Retry-After`(1–5초, 기본 2초)로 최대 180회 재시도한다.
변경 요청은 실행 전 초기화 gate의 명시적 거부인 이 응답만 대상이며 일반 503과
응답이 불확실한 mutation은 자동 재시도하지 않는다. 컴포넌트 unmount 또는 session
변경은 대기 구간을 포함한 요청 수명을 취소하고 다음 HTTP 시도 전에 동일 token/session을
다시 확인한다. 한도 소진 후에는 초기화 중 상태와 명시 재조회를 제공하며 무기한 polling하지 않는다.
운영 화면의 기간 사건 seed도 같은 warming 정책을 적용한다. 재시도 중에는 `초기화 중`을
표시하고, 고정 from/to/cursor와 성공 페이지 최대 5개를 유지한다. 이동·세션 변경은 같은
AbortSignal로 HTTP와 Retry-After 대기를 함께 취소한다.
사건 seed의 오류·초기화 재시도 소진 안내에는 `다시 조회` 동작을 제공한다. 사용자가 실행하면
이전 seed 수명을 취소하고 새 최근 구간 seed를 시작한다. 각 seed 안의 고정 범위·cursor,
5페이지 상한과 유한 warming 재시도는 그대로 유지한다.
source 실패는 partial/stale와 기존 dataAsOf를 유지한다. 지원 실패면 분모 null,
목록 불완전이면 미측정 수 null, catalog 오래됨이면 현재 사용 판정을 stale로 표시한다.
단건 비용/점유 장애가 무관한 profile 판정을 바꾸지 않는다. DOM 목록 source 실패는 coverage의 partial/source 오류로 표시한다. 인증 변경/401/403 후 이전 cache 표시 금지다.

구현의 namespace cache는 처음 준비 중 503/`Retry-After: 2`를 반환하고, source별 성공 입력을
한 번의 갱신에 모아 교체한다. 실패 source는 최대 5분의 기존 입력과 마지막 성공 기준 시각을
유지하며 partial/stale로 보인다. Run LIST는 한 번의 namespace 갱신에서 UID/owner index로
사용하고 공개 page별 전수 LIST를 하지 않는다. DOM도 namespace LIST와 정확 ref 검증을 같은 base에 보존하고 동작 비용 source는 집계에 접근하지 않는다.

5분을 넘긴 snapshot을 읽은 요청은 namespace의 동일 in-flight 갱신 완료를 기다린 뒤
새 snapshot으로 응답한다. 갱신 시작 직후 오래된 지역 변수만 보고 503을 반환하지 않는다.
5분 이내는 갱신 중 `freshness:stale`로 즉시 제공하고, 5분 초과 입력은 갱신 실패·시간 초과
또는 성공 후에도 실제 source 확인 시각이 유효하지 않은 경우 503으로 남긴다.
query의 batches/Node는 cache key가 아니라 같은 namespace snapshot의 projection이다.

수용 데이터는 3모델/8class/88BEO/1,155Run/78profile, 동시 사용자 1·5다.
**① 성공 GET p95 <2초 ② 로그인/경로 진입부터 첫 사용 가능한 커버리지까지 p95 <2초
③ 채택 조건 선택부터 결과 행·기준 판정 완료까지 p95 <2초**를 각각 기록한다.
cold process 시작→첫 사용 가능한 커버리지·채택 행 판정 완료는 **30초 이내** 목표로 별도 수용한다.
warming/partial 비율과 최장 지속시간을 보고하며 정상 source의 안정 운영에서는 warming/partial이 남으면 실패다.
빠른 503/unknown/장기 미판정 200을 성공으로 세지 않는다. catalog 밖 단건은 기존 10초 조회 한도와
미제공 이유를 별도 측정하며 전체 준비를 기다리게 하지 않는다.
서버 catalog 시작/갱신·Kube GET 수·ConfigMap 파싱 수·인증·BFF·집계 CPU/I/O와 serving 제어 지연을 함께 기록한다.
이 시간은 설계 목표이며 달성 관측이 아니다. 기존 행렬 7.690초/Run 1행 3.861초를 그대로 재사용하면 수용 실패다.

### 13.7 7차 실패 분류·요청 채움·목록 계약 (2026-10-31)

`MeasurementFailure`와 `MeasurementReason`은 `code`, 한국어 `label`, 안전한 고정 한국어
`message`를 제공한다. 요청·coverage.failureSummary·Run assessment는 **같은 서버 함수**를
사용한다. 클라이언트는 로그를 다시 분류하지 않는다. 현재 generation의 False 조건과
보존 runtime termination의 종료 코드/로그만 근거로 쓰며 ANSI를 제거하고 대소문자를 무시한다.
첫 일치 규칙이 우선한다. exit 137만으로 GPU 원인을 확정하지 않는다.

| 우선순위 | code / label | 결정적 근거 |
|---|---|---|
| 1 | `GPUMemoryExhausted` / GPU 메모리 부족 | CUDA OOM, GPU OOM, cuBLAS/cuDNN allocation failure, 또는 명시된 CUDA 실행 경로 + BFCArena의 `Failed to allocate memory for requested buffer` |
| 2 | `HostOOM` / 호스트 메모리 부족 | runtime 종료 137, OOMKilled, 또는 명시된 CPU 실행 경로 + 위 BFCArena 할당 실패 |
| 3 | `PowerObservationTimeout` / 전력 관측 시간 초과 | 같은 줄에 metric observation/result, metric-observer, power observation/observe power와 timeout/timed out/deadline exceeded |
| 4 | `ImagePullFailed` / 이미지 가져오기 실패 | ImagePullBackOff, ErrImagePull, image pull failed, failed to pull (and unpack) image |
| 5 | `RuntimeFailed` / 실행 오류 | 보존된 nonzero runtime exit 또는 명시 runtime failed/RuntimeError/segmentation fault/illegal instruction 등 |
| 6 | `Other` / 기타 오류 | 위 결정적 근거 없음. 일반 allocator 문구만으로 GPU 원인을 추측하지 않음 |

원문 로그와 조건은 상세의 명시적인 원문 펼치기에만 둔다. 분류와 기존 Run UID/namespace/name,
batch/종료 코드/owner 연결은 함께 보존한다. 후속·유사 조건 관계를 분류 코드로 바꾸지 않는다.

측정 요청 URL은 `/measurements/new?namespace=...&model=...&deviceClass=...&node=...&powerMode=...&path=...&batches=1,2,4`다.
model/deviceClass/path/powerMode는 카탈로그의 정확 이름이고 node는 선택 이름이다. batches는
양의 정수 CSV를 중복 제거·오름차순으로 해석하며 malformed·중복 query key는 오류다.
variantId와 안전한 상대 returnTo는 선택 사항이다. variant가 없는 미측정 칸도 이 계약으로 요청한다.
서버에 공통 protocol 기본값 API/설정은 현재 없다. 따라서 같은 모델/class/선택 Node/mode/path의
최신 BEO spec, 없으면 같은 조건 catalog profile의 보존 protocol을 출처와 함께 사용한다.
같은 조건 근거도 없으면 같은 모델의 최근 Complete BEO, 그 다음 같은 실행 경로의 최근
Complete BEO에서 **장치와 독립적인 protocol·dataset만** 가져오고 이름과 원본 장치를 표시한다.
다른 장치의 batch를 복사하지 않으며 CPU allocation 입력 자체를 제공하지 않는다. 실제 affinity/cgroup/thread 관측은 표시용이다.
catalog가 보존하지 않는 재시도 정책, 위 순서의 근거가 없는 protocol은 미제공으로 두고 직접
입력을 요구한다. 반복 3회는 화면 기본값으로 구분하며 CPU 수 기본값은 없다.
조건 변경은 protocol·dataset·선택 batches를 초기화하지 않으며,
서버 사전 검사와 exact 자동 제외만 무효화한다. 실제 제출은 사용자 동작이다.

요청·회차·프로파일·동작 비용·분배 점유 목록은 필터를 표 위, 정렬을 열 머리글,
페이지 크기(10/25/50)·이전/다음을 표 아래에 둔다. 서버 회차/프로파일 목록은 **필터·정렬 후**
page를 계산하며 현재 page만 클라이언트 정렬하는 기능은 없다.
Run sort는 name/created/model/powerMode/status, profile sort는
model/deviceClass/node/powerMode/path/cpu/created/status, order는 asc/desc다.
같은 정렬값은 identity로 결정한다. profile의 q 및 Node/mode/path/CPU/status 필터도 전체에 적용한다.

동작 비용 목록 총 시간은 계획한 각 단계의 실제 duration이 정확히 하나씩 있을 때만 합하며,
총 벽에너지는 모든 단계가 Accepted이고 각 wallJoules가 있을 때만 합한다.
부분 결과는 전체 합계로 올리지 않고 미제공으로 둔다. 유휴를 포함한 전체 순서임을 표시한다.
점유 목록 d는 ms, 처리량은 req/s이며 결측에는 단위를 붙인 가짜 수치를 만들지 않는다.
음수 duration/점유·비유한 값은 값 오류, 결측은 미제공, 관측 0은 0으로 구별한다.

### 13.8 네이티브 d 요청·진행·셀 커버리지 (2026-11-03)

공개 생성은 `POST /api/v1/namespaces/{ns}/dispatch-occupancy-measurements[?dryRun=All]`,
본문은 `{name?,node,batchEnergyOptimization,workload,batch}`뿐이다. 셀에서 선택하면 그 셀의
BEO 이름을 보낸다. 서버가 scheduler ConfigMap의 `dispatchOccupancy` 정책으로 완성·동결한
DOM을 `{request:"Accepted"|"NOT_SUBMITTED",task:<DOM>}`으로 돌려준다. 프로토콜 필드를
본문에 넣으면 400, 불가능한 조건은 `400 OccupancyInfeasible`, 설정 미제공/오류는
`503 OccupancyPolicyUnavailable`이며 UI 기본값으로 대체하지 않는다.
삭제는 `DELETE .../dispatch-occupancy-measurements/{name}?uid=...`와
`If-Match: resourceVersion`이다. Measuring이면 409이고 실행 취소 API는 없다.

**진행 projection.** Query의 기존 목록 `{items:[...]}`과 단건 GET은
`kind,metadata,conditions,status,references,assessment,node`를 그대로 유지하며 다음 최상위
필드를 추가한다. `metadata.resourceVersion`도 유지하여 삭제의 precondition에 쓴다.

```text
name, uid, node, batchEnergyOptimization, workload, batch, profileID,
phase: null | Measuring | Complete | Insufficient | Failed,
reason, createdAt,
measurementWindow: {start,end} | null,
d: {perRequestNanos: signed int64 | null, throughput: number | null,
    idleFraction: number | null, verdict: string} | null,
evidence: {store,key,size} | null
```

`createdAt`은 CR 생성 시각이다. 측정 시작/종료는 검증된 해당 evidence의 `window`만 사용한다.
Measuring에 생성 시각/예상 duration으로 가짜 측정창이나 퍼센트를 만들지 않는다.
`profileID`는 동결 조건 또는 일치 확인한 record에서 읽는다. d가 status에만 있으면 signed 값과
유효 throughput은 보존하되 idleFraction은 null, verdict는 빈 문자열이다. evidence 읽기/identity
검증에 실패해도 목록 전체를 숨기지 않고 기존 assessment와 원래 status·ref를 보존한다.
음수 d를 0으로 바꾸지 않으며 정확 정수 파서를 사용하는 클라이언트가 int64를 무손실로 소비한다.

**커버리지 projection.** 무쿼리 GET과 `view=summary`의 모든 셀에
`occupancy: [{node:{name,uid},profileID,batch,state,request:{name,uid}|null,
phase,reason,d,evidence,adopted}]`를 추가한다. `d/evidence`는 위와 같은 형식이다.
`adopted`는 인증된 private `CatalogRead.accepted.occupancy[]`의 실제 스케줄러 선택만 사용한다.
Node 이름·물리 UID × profileID × batch와 evidence의 store/key가 모두 일치하고,
선택의 model/powerMode/executionPath가 현재 셀과 일치하면 true, 선택 목록은 제공됐지만
일치 항목이 없으면 false다. `runtimeImage`와 evidence size는 채택 유효성 키가 아니다.
profile의 schedulerUsed나 DOM phase/Accepted verdict만으로 d 채택을 추측하지 않는다.
accepted 또는 occupancy 필드가 누락/null이면 null이며, 명시적 `occupancy:[]`는 false다.
private JSON 읽기와 coverage base는 이 부재/null 대 빈 배열 구분을 보존한다.
catalog 읽기 실패·sourceError·확인 시각 부재도 null이며 이전 성공 선택을 현재 채택으로 재사용하지 않는다.
catalog의 sourceCheckedAt/sourceError는 기존 source freshness에 함께 반영한다.
성공한 무변경 refresh도 확인 시각을 전진시키며 실패는 마지막 성공 확인 시각과 보존 보고 자료를
유지하되 coverage를 stale, catalog source를 incomplete/error로 표시한다.

- 단위는 Node UID × profileID × batch다. 같은 Node/batch에서 현재 profile의 present > measuring > failed > none 순으로 선택·정렬하며 같은 상태는 createdAt 최신이 우선한다. 과거 profile tuple 원자료도 별도 항목으로 유지한다.
- `present`: 현재 셀의 `currentMeasurement`에 속한 profileID이고, 현재 물리 Node UID의
  일치 확인된 Accepted evidence가 있다. 더 새로운 Failed/Insufficient 요청보다 이 값이 우선한다.
- `failed`: phase Failed/Insufficient 또는 Complete인데 evidence 누락/불일치다.
  후자는 reason을 `EvidenceInvalid`로 표시하고 구체적인 누락·읽기·해석·identity 오류는
  원래 DOM assessment에 보존한다. `measuring`: phase 미기록 또는 Measuring.
- `none`: 논리적으로 현재 d가 없음을 뜻한다. **요청도 evidence도 없는 tuple은 항목을 만들지 않는다.**
  셀의 `occupancy:[]` 또는 해당 tuple 부재가 d 없음이며 UI는 요청 화면에서 Node/batch를 선택한다.
  원자료가 있는 과거 profile tuple만 `state:"none",reason:"ProfileSuperseded"`로 남기고
  request/phase/d/evidence 원자료를 보존한다.
- Node query는 해당 Node만 포함한다. 과거 물리 UID와 새 물리 UID를 합치지 않는다.
  현재 profile 선택은 기존 결정 A를 그대로 쓰므로 최신 실패 profile을 옛 성공으로 대체하지 않는다.
- DOM 목록은 namespace base snapshot의 독립 source이며 SAR에 같은 namespace DOM list가 필요하다.
  chart의 Query Role/추가 namespace Role(`templates/query.yaml`)과 viewer ClusterRole
  (`templates/query_viewer_rbac.yaml`)은 이미 DOM get/list를 허용하므로 chart 변경은 없다.

**크기 영향 추정(실행 검증 아님).** 셀×Node×batch의 Cartesian product를 생성하지 않는다.
항목 수는 **실제 DOM 요청이 가리키는 distinct Node UID/profileID/batch tuple 수 이하**이며
최악에도 DOM 요청 수에 비례한다. 요청 없는 390셀은 빈 배열 390개뿐이다.
보존 운영 `deployB/refactor-cutover-20261103/cutover-crs.json`에는 DOM 150개,
동결 `(node 이름,profileID,batch)` tuple 51개가 있다. phase별 Complete 89,
Failed 23, Insufficient 38이며 result ref는 127개다. 같은 이름의 물리 Node 교체를
검증하기 전까지 51개를 정확한 UID tuple 수라고 단정하지 않고, 150개를 보수적 항목 수로 쓴다.

대표 Node UID/profileID/ref 문자열의 present 항목은 원본 JSON 569 B/22 논리 노드다.
문자열 길이에 따라 달라지며 실제 record/full protocol/raw는 응답에 넣지 않는다.
`evidence`는 `{store,key,size}` ref만, `d`는 signed 값·처리량·idle 비율과 원래 verdict만 담는다.

| 가정 | 항목 수 | present 원본 항목 증가 추정 | baseline 포함 논리 노드 추정 |
|---|---:|---:|---:|
| 보존 snapshot의 이름 기반 tuple마다 물리 UID가 동일 | 51 | 29,019 B | 231,874 |
| 모든 보존 DOM 요청이 서로 다른 물리 tuple | 150 | 85,350 B | 234,052 |

논리 노드 추정은 기존 운영 디코더 실측 baseline 230,362에 항목당 22와 셀 배열 390을 더한 값이다.
빈 배열 필드의 원본 JSON 증가는 셀당 15 B, 390셀 약 5,850 B다(항목 byte 추정과 별도).
기존 상한 2,000,000노드/깊이 64와 서버 응답 8MiB는 올리지 않는다.
summary는 기존 `measurement-summary.v1` values/shapes dictionary를 그대로 쓰며 Node·profileID·
상태 shape·ref를 공유한다. 위 byte 수는 **dictionary 압축 후 실측이 아닌 원본 항목 추정**이다.
`TestOccupancySummaryRetainsSignedTuplesWithinDecoderBudget`는 390셀에 실제 요청 수 기준
150항목만 배치해 summary 256KiB, 확장 노드/깊이, signed/null/tuple round-trip을 검사한다.
`TestCoverageOccupancyExecutionStates`의 공통 fixture는 요청 없는 모든 셀이 빈 배열임을 검사한다.
`TestCoverageOccupancyEqualPriorityUsesNewestCreatedAt`는 입력 순서와 무관한 동률 최신 선택,
`TestCoverageOccupancyInvalidEvidenceReason`은 해석·identity 검증 실패의 `failed/EvidenceInvalid`를 검사한다.
서버 배포 후 실제 물리 tuple 수·summary byte 증가·cold refresh 시간은 별도로 수용한다.

**통합 검증 범위(2026-11-03).** Node 24.14.0의 offline frozen 설치는 479개 재사용·다운로드 0개였다.
Go는 gofmt, `go test ./internal/queryapi/... ./cmd/chill-query/...`, `make lint`,
`make arch-check`(67/67 production packages), `make test`(envtest 1.31.0 포함)가 통과했다.
대시보드 `tsc --noEmit`, `eslint .`, 운영 summary 디렉터리를 지정한 전체 테스트 487개와
기본 `next build`가 통과했다. 빌드의 Asta Sans fallback 경고는 남는다.
로컬 production 대시보드에서 운영 HTTPS GET만 사용해 DOM 결과 25개·결과 Sheet·기존 커버리지와
상세를 1440×900으로 확인했다. API GET은 200, 브라우저 콘솔 오류와 문서 가로 넘침은 없었으며
Next 링크 prefetch의 `net::ERR_ABORTED`만 관측했다. 스크린샷은 외부 운영 근거
`dom-e2e/render-{results,result-detail,coverage,coverage-detail}.png`에 보존한다.
운영 Query에는 새 occupancy projection이 아직 없으므로 이 렌더는 기존 읽기 화면 회귀이며,
표시된 `d 없음`으로 운영 evidence 부재를 판정하지 않는다.
DOM POST·DELETE는 실행하지 않았다. 서버·Query 배포 후 dry-run의 정책 동결·불가능 조건·권한,
실제 제출→Job→evidence→진행/종료 polling, 셀 d 상태·우선순위·이전 profile 보존,
UID/If-Match 삭제·Measuring 409와 삭제 후 자체 evidence 정리를 실서버에서 수용해야 한다.

**서버 통합 후 재검증(2026-11-03).** `ea6899ef` 위로 충돌 없이 rebase했다. 생산 조립
`server.go`의 Build/Submit/DeleteOccupancy 주입, 공통 게이트웨이의 native handler 전달,
`httpapi/handler.go`의 DOM POST/DELETE 등록이 연결돼 있다. Query/Server/HTTP/Kube focused
테스트와 `make lint`, `make arch-check`(67/67), `make test`가 통과했다. 대시보드는
Node 24.14.0 tsc/eslint/운영 summary 포함 500개 테스트/기본 build가 통과했다.
이 검증 당시 legacy UI는 `inputsJSON.computed.saturationSizing`의 workers·제한 요인·예상 idle을
읽기 전용으로 표시했다. 신규 burst evidence에는 saturationSizing/예상 idle이 없으며
설정된 workers와 minimumPairs 및 실제 pair 통계를 사용한다(§7의 분리된 stored 계약).
DELETE는 계속 UID와 따옴표 있는 `If-Match:"<resourceVersion>"`을 전송한다.

`TestOccupancyLocalHTTPSDryRunThenCreate`는 실제 로컬 TLS handler→builder→service에서
200 NOT_SUBMITTED→202 Accepted, 생성 전 dry-run 무효과와 동결 spec 동일성을 확인했다.
`TestOccupancyDeleteFencesAndOnlyRemovesOwnedEvidence`는 UID/RV·Measuring 거절·자체 evidence
정리를 확인했다. 이 테스트의 Kubernetes·evidence는 인메모리 fixture이며 실제 부하 Job 실행이 아니다.
운영 쓰기는 수행하지 않았다. 기존 로컬 dashboard-e2e 클러스터에는 DOM CRD와 BEO가 없음을
읽기 전용으로 확인했으며 해당 클러스터를 변경하지 않았다. 실제 Job→evidence→진행의
클러스터 E2E는 이 전제와 runtime·측정 입력을 준비한 뒤 수행해야 한다.
