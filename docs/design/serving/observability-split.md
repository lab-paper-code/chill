# 관측 분리 (M1)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M1 설계다. 명세 변경은 없다. ChillServer는 관측을 기록하고 최근 2시간의 조회에만 답한다. 영구 보존은 S3의 시간 세그먼트가 맡고, 측정 표는 그 세그먼트로 오프라인에서 만든다(spec README "SLO 관측과 집계"). [M0](decision-record-diet.md)를 전제로 한다.

## 1. 측정한 문제

근거는 E1 재실행(2026-10-19, rev 35, `v1alpha4`, [workload-pools 9.3절](workload-pools.md#93-검증-기록))의 S3 `serving/` 사본(창 636개, 659 MB), 부하 2 req/s와 부하 0인 1분 창의 필드별 바이트, 그리고 코드다.

| 문제 | 근거 |
|---|---|
| PVC가 273 MiB/h 증가한다 | soak 2 req/s, `du -sk`. 검산: 요청 7,199 × 27.9 KB = 200.6 MB, 판단 360 × 185 KB = 66.7 MB, 전력 7,200 × 1.9 KB = 13.8 MB, 합 268 MiB/h |
| 요청당 27.9 KB | 요청 하나가 사건 약 10줄이다. 줄마다 `assignment`(62%)를 반복하고, 그 안의 `profileEvidence` 참조 19개가 stream의 49%다. `nativeProgress`가 13%다. 표가 쓰는 사실을 요청당 한 행으로 모으면 989 B(gzip 138 B)다 |
| 판단 기록 185 KB | `candidates` 97 KB, snapshot 70 KB(costs 27.7, options 13.6, baselines 12, workloads 11.3), keep 7.7 KB, target 7.7 KB. target이 keep이면 같은 본문이 두 번 있다. 연속 snapshot의 costs, baselines, workloads는 100% 같다 |
| 부하 0에서도 45 MB/h | 판단 87 KB × 360과 전력. 연속 판단은 시각 필드만 다르다. 전력 줄 1.9 KB 중 표본은 763 B이고 나머지는 줄마다 같은 source와 summary다 |
| 로컬 사본을 지우지 않는다 | 닫힌 창을 S3에 올린 뒤에도 PVC에 남는다. S3 사본을 읽는 코드는 없다 |
| 기동과 조회가 누적 이력에 비례한다 | `NewObservationLog`가 모든 창의 모든 줄을 decode해 census를 만든다(기동 27~31 s `[추론]`). `runtimeFacts`는 이력의 모든 배정 사건을 보유한다. 워크로드 조회는 OOMKill 2회(D-11) 뒤 접수 구간과 겹치는 창만 읽는다(b68570c). `LoadWindow`와 `OperatingPower`는 census로 창을 고른다 |
| 재시작이 기록을 되풀이한다 | firstfloor 로그를 처음부터 다시 읽어 재시작마다 11~12 MB를 다시 썼다. 커밋 `fix(serving): stop re-recording firstfloor progress after restart`가 고친다 |

## 2. 범위

- M1: 요청 1행(3장), 불변 값의 ID 참조(4장), 시간 세그먼트의 S3 보존과 로컬 버퍼(5장), 범위 읽기(6장), 오프라인 표 산출(7장).
- M1 밖: Prometheus, native histogram, ServiceMonitor와 PodMonitor, PromQL relay, Dashboard PromQL. 어느 측정 표도 TSDB 값을 원자료로 쓰지 않는다. Dashboard는 ChillServer 조회 API를 그대로 읽는다.

## 3. 요청 기록

### 3.1 행

`requests` stream의 줄은 행 객체 하나이고 `kind`가 `assigned` 또는 `final`이다. 같은 `requestID`의 `final`은 `assigned`를 대신한다. 모든 reducer는 `requestID`로 합쳐 `final`, 없으면 `assigned`, 없으면 메모리의 열린 행을 쓴다.

| 행 | 내용 | 쓰는 지점 |
|---|---|---|
| `assigned` | `requestID`, `workloadUID`, `definitionID`, `receivedAt`, `lifetimeNanos`, `dispatchedAt`, `node`, `instance`, `profileID`(약 300 B) | 배정. append가 실패하면 지금처럼 배정을 되돌린다 |
| `final` | 아래 표 | Runtime이 호출을 지우는 `releaseLocked`, 살아 있는 호출이 없는 `Response`(접수 전 거절) |

| `final` 묶음 | 필드 |
|---|---|
| 식별 | `requestID`, `workloadUID`, `definitionID`, `node`, `instance`, `profileID`, `decisionID` |
| 결과 | `outcome`, `error`, `executionComplete`, `clientSpanNanos` |
| 시각 | `receivedAt`, `lifetimeNanos`, `elapsedNanos`(응답 종료), 단계 오프셋 `validatedNanos`, `acceptedNanos`, `dispatchedNanos`, `queuedNanos`, `runningNanos`, `executedNanos`, `exchangeNanos`. 오프셋은 `receivedAt` 기준이고 도달하지 않은 단계는 비어 있다 |
| 배치 | 마지막 backend 진행의 `batchID`, `batchSize`, `queueMS`, `serviceMS`, `backendMS`, `preprocessMS`, `postprocessMS`, `firstTokenMS`, `units`, `nativeElapsedMS` |

- profile 본문(근거 참조, confidence, 측정 조건의 artifact와 실행 경로)은 `profileID`로, SLO와 워크로드 이름은 `definitionID`로 같은 시간의 `catalog`에서 찾는다(4장, `WM-27`).
- 지연 표본과 `completionResidualNanos`는 `aggregate`의 규칙 그대로 행에서 계산한다. `delivery_failed`, `incomplete`, `canceled`를 빼고, 잔여는 `executionComplete`일 때 `elapsedNanos − dispatchedNanos − nativeElapsedMS`다(`SCH-31`, `WM-28`).
- 구현: `RequestEvent` 호출 지점은 그대로 둔다. log는 사건을 요청별 열린 행(메모리)에 접고 `assigned`에서만 append한다. Runtime은 최종 지점에서 `RequestClosed(id)`를 부르고, log는 `final`을 append한 뒤 열린 행을 지운다. 열린 행이 없는 요청의 사건은 버린다. 열린 행 수는 처리 중 요청 수다. 수신, 검증, 접수는 메모리에만 접으므로 거절 판단은 `observationError`가 맡는다.
- 크기: `final` 989 B(E1 창의 실제 121건으로 만든 행)와 `assigned` 약 300 B, 합해 요청당 약 1.3 KB다.

### 3.2 재시작

- 배정 전 요청(수신, 검증, 큐)은 crash 때 행이 남지 않는다. 사용자가 받아들인 손실이고, load-generator JSONL과 `requestID`로 join하면 드러난다(7장의 `recorded`).
- 배정된 요청은 RestartDrain이 그대로 맡는다. 시작할 때 `runtimeFacts`가 로컬 `requests` 파일에서 `final`이 없는 `assigned` 행을 골라 지금처럼 호출을 복원하고 수신을 닫는다. 복원한 호출은 backend 완료를 관측한 뒤 `final`(`incomplete`, `EntrypointRestarted`)을 쓴다.
- firstfloor 로그 follow는 인스턴스의 복원한 배정 중 가장 이른 `dispatchedAt`부터 읽는다(없으면 프로세스 시작). 그 사이 다시 읽는 줄 중 열린 행이 없는 요청의 줄은 버려진다.
- `RetainedDefinition`은 지우지 않는 루트 `definitions.jsonl`(정의 수에 비례, KB 단위)에서 읽는다.

## 4. 불변 값의 ID 참조

판단과 전력 기록은 불변 값을 ID로 참조하고, 본문은 `catalog` stream의 행 `{kind, key, at, body}`에 둔다. log는 한 시간 안에서 key를 처음 참조할 때, 그리고 같은 key의 본문이 그 프로세스에서 마지막에 쓴 본문과 다를 때 catalog 행을 쓴다. 그래서 시간 세그먼트 하나가 그 시간의 모든 참조를 담는다. 읽기는 참조한 기록의 시각 이전에서 같은 key의 마지막 catalog 행을 쓴다(DuckDB `ASOF JOIN`). 재시작 뒤 다시 쓴 같은 본문은 결과를 바꾸지 않는다.

| kind | key | 참조하는 곳 |
|---|---|---|
| `definition` | `definitionID` | 요청 행, snapshot `workloadIDs`(`workloads` 대신), `outstanding[].definitionID` |
| `profile` | `profileID` | 요청 행, keep과 target과 options의 인스턴스(M0부터 `profileID`) |
| `cost`, `baseline` | `<id>@<reference.key>`(측정 evidence object) | snapshot `costRefs`, `baselineRefs`(`costs`, `baselines` 대신) |
| `meter` | meter ID | 전력 행. 본문은 노드, scope, source, sampling, coverage 정책 |

- target: `target.id == keep.id`이면 target을 `{id, keep: true}`로 저장한다(7.7 KB 절약).
- 전력 행: `{at, node, meter, samples[{observedAt, watts}], powerJoules, powerError}`. 표본으로 다시 계산되는 `summary`는 쓰지 않는다.
- options는 노드 가용성에 따라 판단마다 다른 계획 입력이라 그대로 둔다. 인스턴스는 이미 `profileID`만 담는다.
- `SchedulerObservationPage`는 참조와 target을 본문으로 채워 돌려준다. API와 Dashboard가 받는 형태는 그대로다.
- `candidates` 요약과 부하 0의 판단을 바뀔 때만 쓰는 일은 M4가 평가기와 함께 정한다.

## 5. 세그먼트와 보존

- 파일: `<directory>/v1alpha5/<YYYYMMDDTHH>/<stream>.<MM>.jsonl`. stream은 `requests`, `scheduler`, `management`, `catalog`이고, 쓰는 시각(UTC)의 분이 파일을 고른다. `DefinitionRecord`는 루트 `<directory>/v1alpha5/definitions.jsonl`에 append한다. 버전은 경로가 담으므로 줄에는 `schemaVersion`과 `window`가 없다.
- 업로드: 1분 archive ticker가 지난 시간 디렉터리마다 stream별로 분 파일을 순서대로 이어 gzip한(`compress/gzip`, 헤더 시각 0) 파일을 `serving/observations.v1alpha5/<YYYYMMDDTHH>/<stream>.jsonl.gz`에 `PutImmutable`로 올린다. 같은 입력의 gzip은 같은 바이트이므로 재시작 뒤 다시 올리면 `CreateOutcomeExisting`이다. 그래서 업로드 여부를 저장하지 않는다.
- 로컬 삭제: 한 archive pass에서 그 시간의 모든 stream이 `Created` 또는 `Existing`을 받고, 그 시간이 끝난 지 1시간이 지났으면 디렉터리를 지운다. 하나라도 실패하면 그 시간은 다음 pass에서 처음부터 다시 올린다. 로컬에는 현재 시간과 직전 시간이 남는다. PVC는 이 2시간과 S3가 멈춘 동안의 미업로드분을 담는 버퍼다. S3가 멈추면 PVC가 차고 append 실패가 `readyz`를 503으로 만든다.
- 시작: 각 stream의 마지막 분 파일과 루트 `definitions.jsonl`을 마지막 개행까지 자른다(crash 때 쓰다 만 줄). 그 밖에 시작 때 읽는 것은 루트 definitions와 로컬 `requests`뿐이다.
- 읽기는 대상 파일을 `mu` 아래에서 열고 잠금 밖에서 읽는다. 삭제는 열린 fd에 영향이 없다. 쓰는 중인 마지막 줄(개행 없음)은 무시한다.

## 6. 조회

조회는 아래 범위의 분 파일 중 로컬에 있는 것만 연다. 파일 이름이 시간 색인이라 목록 조회도 필요 없다. 줄은 b68570c처럼 envelope만 먼저 decode하고 선택된 줄만 전체 decode한다. 참조는 그 시간의 `catalog` 파일(시간당 수백 KB 이하)로 푼다.

| 조회 | 읽는 범위 | 규칙 |
|---|---|---|
| `Query`(`/observations/workloads/{uid}`) | `requests`: `from`의 분부터 가장 최근 파일까지, `management`: `[from, to]`, 열린 행 | 행은 접수 뒤에 쓰이므로 `from` 앞은 필요 없고, `final`은 언제든 늦게 쓰일 수 있어 끝까지 읽는다. 모집단은 `[from,to)`에 접수된 요청이다 |
| `LoadWindow`, `SchedulerArrivals`, `SchedulerHistoryWindow` | `requests`: `from`의 분부터 가장 최근 파일까지, 열린 행 | 단계 수는 `receivedAt`과 오프셋의 합이 구간 안인 단계로 센다. 열린 행으로 처리 중 요청도 도착에 들어간다 |
| `OperatingPower` | `scheduler`: `[from − 1분, to + 1분]`, 기준 snapshot은 `from`부터 거꾸로 첫 snapshot이 있는 분 파일까지 | 경계 표본 거리는 설정 검사에서 `maximumBoundaryDistance ≤ 1분`으로 묶는다. meter는 catalog의 coverage 정책을 쓴다 |
| `SchedulerObservationPage` | `scheduler`: `[from, to]` | 지금처럼 limit에서 멈춘다 |

- 관측 공백: `from`이 가장 오래된 로컬 분 파일보다 앞이면 결과에 `gapUntil`(그 파일의 시작)을 싣고 남은 구간만 집계한다. `OperatingPower`는 `complete=false`와 이유를 싣는다. `SchedulerHistoryWindow`는 `from`이 그 시작이나 프로세스 시작보다 앞이면 `ObservationGap`을 낸다(지금의 incomplete 창과 같은 오류 경로, `SCH-08` 공백 표시). 서버는 S3를 읽지 않는다. 2시간보다 오래된 구간은 7장의 오프라인 경로다.
- 비용: Tick의 `SchedulerArrivals(now − 60s, now)`는 분 파일 1~2개, 5 req/s에서 1 MB 이하를 읽는다(지금은 창 1~2개, 3.3~6.7 MB). 조회 메모리는 선택된 요청 수에 비례하고, 읽는 양의 최대치는 5 req/s 2시간의 행 36k개(약 47 MB)다.

## 7. 측정 표 산출

표의 원자료는 S3 세그먼트와 load-generator JSONL이다. 각 시간 세그먼트가 `catalog`에 그 시간이 참조한 정의, profile, 비용, meter 본문을 담으므로 실행 구간의 시간 prefix만 받으면 된다. `hack/observations/`에 세 가지를 둔다.

| 파일 | 하는 일 |
|---|---|
| `fetch.sh <from시> <to시> <dir>` | evidence store의 S3 endpoint에서 시간 prefix를 `<dir>/obs/<시>/`로 받는다. 단계별 load-generator 출력은 실험 스크립트가 `<dir>/loadgen/<단계>.jsonl`로 둔다 |
| `e1.sql` | `duckdb < hack/observations/e1.sql`. 요청 표와 Tick 표(`decision.evaluationElapsedNanos`) |
| `power/main.go` | `go run ./hack/observations/power --dir <dir>/obs --from --to`. 전체와 활성 POOL 에너지 |

E1 단계 표(요청 p95, 1 s 이내 완료, 잔여 지연)의 질의다.

```sql
CREATE MACRO empirical(xs, p) AS list_sort(xs)[greatest(1, ceil(p * len(xs)))::INT]; -- gonum stat.Empirical
CREATE VIEW r AS SELECT * FROM read_json_auto('obs/*/requests.jsonl.gz', union_by_name = true) WHERE kind = 'final';
CREATE VIEW c AS SELECT *, regexp_extract(filename, '([^/]+)\.jsonl$', 1) AS stage
  FROM read_json_auto('loadgen/*.jsonl', filename = true);
WITH j AS (
  SELECT c.stage, r.outcome, r.elapsedNanos AS e2e, r.executionComplete AS done,
    r.elapsedNanos - r.dispatchedNanos - r.nativeElapsedMS * 1e6 AS residual,
    r.outcome NOT IN ('delivery_failed', 'incomplete', 'canceled') AND r.elapsedNanos > 0 AS sample
  FROM c LEFT JOIN r ON c.requestID = r.requestID)
SELECT stage, count(*) AS sent, count(outcome) AS recorded,
  empirical(list(e2e) FILTER (WHERE sample), 0.95) / 1e6 AS p95_ms,
  count(*) FILTER (WHERE outcome = 'completed' AND e2e < 1e9) AS within_1s,
  empirical(list(residual) FILTER (WHERE sample AND done), 0.50) / 1e6 AS residual_p50_ms,
  empirical(list(residual) FILTER (WHERE sample AND done), 0.99) / 1e6 AS residual_p99_ms,
  max(residual) FILTER (WHERE sample AND done) / 1e6 AS residual_max_ms
FROM j GROUP BY stage ORDER BY stage;
```

- `sent − recorded`가 서버 행이 없는 요청 수다. 표의 분모는 `sent`다. SLO 열은 `obs/*/catalog.jsonl.gz`의 `definition` 행과 `definitionID`로 join한다.
- POOL 에너지: coverage 정책 적분과 snapshot 소속 재구성은 서버의 `OperatingPower` reducer를 그대로 쓴다. cutover commit이 reducer를 scheduler 기록 iterator를 받는 함수로 떼고(약 40줄), `power/main.go`가 gz 세그먼트를 그 iterator로 넘기고 catalog의 meter 행으로 meter를 만든다(약 120줄과 test 하나). SQL로 적분을 다시 쓰지 않으므로 서버와 같은 값이 나온다.
- 검증은 구조 검사(줄 단위 JSON, 필드 형식)뿐이다. digest, 재인코딩 비교, byte 비교는 두지 않는다.

## 8. 예상 크기

| 조건 | 지금 | M1 뒤 원문 | M1 뒤 gzip(S3) `[추론]` |
|---|---|---|---|
| 2 req/s | 273 MiB/h(실측) | 요청 9.3 + 판단 46.9 + 전력 6.3 + catalog 0.2 ≈ 63 MB/h | 약 5.5 MB/h |
| 5 req/s | 약 582 MB/h(추정) | 23.2 + 46.9 + 6.3 + 0.2 ≈ 77 MB/h | 약 7.4 MB/h |
| 부하 0 | 45 MB/h(실측) | 판단 11.6 + 전력 6.3 + catalog 0.2 ≈ 18 MB/h | 약 1.9 MB/h |

- 요청: 1.29 KB × 7,200건(2 req/s), × 18,000건(5 req/s).
- 판단: 185 KB에서 target 7.7 KB를 빼고 workloads, costs, baselines 51 KB를 참조 약 4 KB로 바꾸면 130 KB, × 360 = 46.9 MB다. 부하 0은 87 − 7.7 − 47 = 32 KB, × 360 = 11.6 MB다.
- 전력: 줄 1.9 KB에서 표본 763 B와 노드, meter, 시각 약 110 B가 남아 0.87 KB × 7,200 = 6.3 MB다.
- catalog: 시간마다 비용과 baseline 약 40 KB, 정의 수 KB, profile 약 16 KB × 사용 중인 수.
- gzip: 요청 행 138 B(실측)와 배정 행 약 40 B, scheduler stream은 실측 비율 12.6배(부하)와 9.5배(부하 0)로 계산했다. 중복을 지운 뒤라 비율이 낮아질 수 있어 E 확인에서 잰다.
- PVC는 2시간 원문, 5 req/s에서 약 155 MB를 담는다. 32Gi는 S3가 멈춘 동안 약 17일을 버틴다. S3는 5 req/s 연속에서 하루 약 180 MB이고 32Gi evidence store로 약 180일이다.

## 9. 계약과 버전

| 대상 | 변경 |
|---|---|
| 저장 | `serving-observations.v1alpha4` → `v1alpha5` 한 번([ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)). writer, reader, API, Dashboard를 commit 2 하나에 넣어 `v1alpha5` 시간 key에는 한 형태만 올라간다 |
| 로컬 | `<directory>/v1alpha5/`. 배포 뒤 `<directory>`의 나머지(`v1alpha4` 창 디렉터리)를 지운다 |
| S3 | `serving/observations.v1alpha5/<시>/<stream>.jsonl.gz`. 배포 뒤 `serving/<windowID>/`(`v1alpha4` 창)를 reader 없이 지운다. E1 표는 이미 만들었다 |
| API | `Query`의 `requests[]`는 요청 행(`final`, 없으면 `assigned`). `Query`, `LoadWindow`, `OperatingPower`, scheduler 조회의 `incompleteWindows` 자리에 `gapUntil`. `ObservationLogHealth.windows` 삭제. `ObservationGap` 오류 추가 |
| 설정 | `--observation-window`, Helm `servingEntrypoint.observationWindow` 삭제. scheduler 설정 `powerObservation.coverage.maximumBoundaryDistance ≤ 1분` |

## 10. 삭제

무한 로컬 이력을 빠르게 읽으려고 생긴 장치를 지운다.

- `observation_log.go`: `ObservationWindow`(`overlaps`, `Incomplete`, `Published`, `Objects`, `ClosedAt`), 창 metadata 첫 줄과 `metadata.json`, `saveWindow`, `writeWindowMetadata`, `start`의 `.starting-` staging과 rename, `NewObservationLog`의 창 scan과 census 구성, `observationCountingWriter`, `ObservationLog`의 `windows`, `active`, `schedulerCensus`, `requestCensus`, `*Writer` 필드, `archive`의 창별 5 object 업로드, `observationRecord.Window`, `ObservationLogHealth.Windows`, `definitions` stream(루트 파일과 catalog로 대체), `requestFacts.events`와 `addEvent`의 사건 재조립, `runtimeFacts`의 전체 이력 scan.
- `observation_log_read.go`: `observationTimeBounds`, `schedulerWindowCensus`, `requestWindowCensus`, `observationStreamPrefix`의 prefix bytes 신뢰, `selectLoadWindow`, `selectReceiptWindow`, `incomplete`.
- `observation_log_scheduler.go`: `loadWindowCompleteness`, `windowLoadFact`의 사건 접기, `LoadWindow.IncompleteWindows`.
- `observation_log_power.go`: `selectOperatingPower`, `operatingPowerCompleteness`의 창 판정, `sameOperatingSource`(meter ID로 대체), `OperatingPowerWindow.IncompleteWindows`.
- `cmd/chill-server`: `observationWindow` 옵션과 flag. archive ticker는 1분 상수다.
- 지운 장치를 검사하는 test: `TestSchedulerHistoryWindowScopesIncompleteDependencies`, `TestRestartIgnoresUnpublishedWindowStaging`, `TestFailedRotationPreservesCompletedWindow`, `TestObservationWindowOverlapUsesActualClose`, `TestOperatingPowerCensusKeepsPhysicalNeighborsAndLatestMembership`, `TestRequestCensusKeepsLateFactsAcrossWindows`, `TestRequestCensusKeepsUnknownPrefixesAndIncompleteMetadata`, `TestDefinitionCensusLoadsAppendsToPreviouslyEmptyPrefixes`, `TestObservationReadPrefixReportsActualTruncation`, `TestObservationLogAppendFailurePublishesIncompleteWindow`. `TestObservationLogArchiveFailureAndInterruptedWindow`는 시간 세그먼트 업로드로 다시 쓰고, 나머지 observation test는 분 파일 위에서 다시 쓴다.
- 문서: [M0 3.1절](decision-record-diet.md#31-알려진-한계)의 보존 한도 항목과 [워크로드 관리](workload-management.md)의 창별 게시 문단은 commit 2에서 이 설계로 다시 쓴다.

## 11. 커밋 단위

| # | commit | 내용 | 의존 |
|---|---|---|---|
| 1 | `fix(serving): bound power coverage boundary distance to one minute` | `validatePower`의 상한 한 줄과 test. 저장 계약은 그대로다 | 없음 |
| 2 | `feat(serving): store observations as hourly request rows and catalog references` | `v1alpha5` cutover 전체: 분 파일, 루트 definitions, catalog와 참조, target 대체, 전력 행, 요청 행과 `RequestClosed`, 꼬리 자르기, 시간 gzip 업로드와 삭제 조건, 범위 읽기와 `gapUntil`, `runtimeFacts`, `OperatingPower` reducer iterator, 10장의 삭제, flag와 Helm 값, Dashboard의 모든 소비자(`observations.tsx`, `inference.tsx`, `service-hub.tsx`, `scheduler-evidence.tsx`, `overview.tsx`, `scheduler.tsx`, `lib/read-types.ts`), 문서 | 1 |
| 3 | `chore(hack): add offline observation tables` | `hack/observations/fetch.sh`, `e1.sql`, `power/main.go`와 test, `hack/README.md` 한 줄 | 2 |

`fix(serving): stop re-recording firstfloor progress after restart`가 먼저 들어간다. commit 2를 배포한 뒤 9장의 이전 기록을 지운다. commit 3은 배포와 무관하다.

## 12. 검사

| commit | 검사 |
|---|---|
| 1 | 1분을 넘는 `maximumBoundaryDistance` 설정 거절 |
| 2 세그먼트 | 쓰는 시각의 분이 파일을 고름. 지난 시간의 gz를 풀면 분 파일을 이은 줄과 같음. stream 하나의 업로드가 실패하면 그 시간 디렉터리를 지우지 않고, 다음 pass가 `Existing`과 `Created`를 섞어 마친 뒤 지움. 직전 시간은 지우지 않음. 마지막 줄이 잘린 분 파일과 루트 `definitions.jsonl`을 시작이 개행까지 자름. 조회가 6장 범위 밖 파일을 열지 않음. 로컬보다 앞선 `from`에 `gapUntil`. 조회 중 삭제에서 같은 결과, `-race` |
| 2 요청 | `final` 행 필드와 잔여 지연이 사건 기반 cohort test와 같은 값. 접수 2분 뒤 닫힌 요청의 `final`을 `Query`가 찾음. `assigned`와 `final`이 함께 있는 완료 요청을 `LoadWindow`와 `SchedulerArrivals`가 한 번만 셈. 배정 append 실패가 배정을 되돌림. 모르는 요청의 backend 진행은 줄을 쓰지 않음. 재시작 뒤 `final` 없는 `assigned`가 RestartDrain으로 복원되고 완료 뒤 `incomplete` 행 하나. evidence 참조 19개 규모에서 요청당 1.5 KB 이하 |
| 2 참조 | target이 keep이면 stub 저장. page가 workloads, costs, baselines, target, meter source를 원래 본문으로 돌려줌. 시간 안에서 meter 설정이 바뀌면 이후 기록이 새 본문을 씀. 재시작 뒤 catalog 재기록이 결과를 바꾸지 않음. 각 시간 디렉터리의 catalog만으로 그 시간의 참조가 모두 풀림. 분 경계를 걸친 `OperatingPower`가 같은 J. E1 규모 판단 기록 135 KB 이하 |
| 2 Dashboard | `make dashboard-check` |
| 3 | `power` test가 같은 기록에서 서버 `OperatingPower`와 같은 J |
| 전체 | `make lint test`, `make helm-lint helm-template` |

## 13. E 확인

E1 soak(2 req/s, 1시간)과 부하 0 한 시간을 다시 돌린다.

| 확인 | 방법 | 목표 |
|---|---|---|
| PVC | helper pod `du -sk`를 시간마다 | 2시간 원문 이하, 2 req/s에서 약 130 MB |
| S3 증가 | `serving/observations.v1alpha5/<시>/` object 크기 | 2 req/s 약 6 MB/h, 부하 0 약 2 MB/h, 기록 |
| 요청 행 | `requests` 원문 바이트 ÷ 요청 수 | 1.5 KB 이하 |
| 판단 기록 | `scheduler` 판단 줄 크기 중앙값 | 135 KB 이하 |
| 부하 0 | 원문 시간당 증가 | 20 MB/h 이하 |
| 재시작 | 2시간이 찬 PVC에서 재시작부터 ready까지, 처리 중 요청이 있는 상태 | 기록(지금 27~31 s), 요청마다 `final` 하나, RestartDrain 종료 |
| 조회 메모리 | 2분, 1시간, 2시간 `/observations/workloads/{uid}` 동안 RSS | OOM 0 |
| 표 재현 | `e1.sql`, `power`와 같은 구간의 `/observations/workloads/{uid}`, `/observations/power` | 수, p95, 잔여 지연, 전체와 활성 POOL J 일치 |
| 누락 | `sent − recorded` | crash가 없으면 0 |

## 14. 사용자 결정과 질문

- 결정: Prometheus, native histogram, monitor, PromQL은 M1에서 뺀다(2장). crash 때 배정 전 요청의 행 손실을 받아들인다(3.2절). RestartDrain은 유지하고 배정 행이 요청당 약 300 B를 더한다(3.1절). 압축은 stdlib gzip이다(5장).
- 질문 1: 로컬 보존이 2시간이라 Dashboard에서 그보다 앞선 구간은 관측 공백으로 보인다. 이 길이로 충분한가.
- 질문 2: commit 1은 `maximumBoundaryDistance`를 1분 이하로 묶는다. testbed scheduler ConfigMap의 값이 이 안에 있는지 배포 전에 확인한다(이 문서는 클러스터를 읽지 않았다).
