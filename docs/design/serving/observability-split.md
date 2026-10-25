# 관측 분리 (M1)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M1 설계와 2026-10-25 T4 조회 확장 설계다. ChillServer는 관측을 기록하고 로컬 버퍼 또는 evidence store의 시간 세그먼트에서 조회에 답한다. 측정 표의 오프라인 산출도 같은 세그먼트를 쓴다(spec README "SLO 관측과 집계"). [M0](decision-record-diet.md)를 전제로 하며, T4의 요구사항 원본은 [scheduler SRS](../../spec/scheduler/SRS.md)의 `SCH-42~47`이다. 아래에서 **기존 구현**과 **변경 설계**를 구분한다. T4는 설계 단계이며 코드·배포 완료를 뜻하지 않는다.

## 1. 측정한 문제

근거는 E1 재실행(2026-10-19, rev 35, `v1alpha4`, [workload-pools 9.3절](workload-pools.md#93-검증-기록))의 S3 `serving/` 사본(창 636개, 659 MB), 부하 2 req/s와 부하 0인 1분 창의 필드별 바이트, 그리고 코드다.

| 문제 | 근거 |
|---|---|
| PVC가 273 MiB/h 증가한다 | soak 2 req/s, `du -sk`. 검산: 요청 7,199 × 27.9 KB = 200.6 MB, 판단 360 × 185 KB = 66.7 MB, 전력 7,200 × 1.9 KB = 13.8 MB, 합 268 MiB/h |
| 요청당 27.9 KB | 요청 하나가 사건 약 10줄이다. 줄마다 `assignment`(62%)를 반복하고, 그 안의 `profileEvidence` 참조 19개가 stream의 49%다. `nativeProgress`가 13%다. 표가 쓰는 사실을 요청당 한 행으로 모으면 989 B(gzip 138 B)다 |
| 판단 기록 185 KB | `candidates` 97 KB, snapshot 70 KB(costs 27.7, options 13.6, baselines 12, workloads 11.3), keep 7.7 KB, target 7.7 KB. target이 keep이면 같은 본문이 두 번 있다. 연속 snapshot의 costs, baselines, workloads는 100% 같다 |
| 부하 0에서도 45 MB/h | 판단 87 KB × 360과 전력. 연속 판단은 시각 필드만 다르다. 전력 줄 1.9 KB 중 표본은 763 B이고 나머지는 줄마다 같은 source와 summary다 |
| 로컬 사본을 지우지 않는다(당시) | M1 이전에는 S3 업로드 뒤에도 PVC에 남았다. 현재 M1은 업로드 완료 후 로컬을 지우며, `CapacityRecords`에는 이미 S3 읽기가 있다. T4는 이를 일반 관측 조회로 확장한다 |
| 기동과 조회가 누적 이력에 비례한다 | `NewObservationLog`가 모든 창의 모든 줄을 decode해 census를 만든다(기동 27~31 s `[추론]`). `runtimeFacts`는 이력의 모든 배정 사건을 보유한다. 워크로드 조회는 OOMKill 2회(D-11) 뒤 접수 구간과 겹치는 창만 읽는다(b68570c). `LoadWindow`와 `OperatingPower`는 census로 창을 고른다 |
| 재시작이 기록을 되풀이한다 | firstfloor 로그를 처음부터 다시 읽어 재시작마다 11~12 MB를 다시 썼다. 커밋 `fix(serving): stop re-recording firstfloor progress after restart`가 고친다 |

## 2. 범위

- M1: 요청 1행(3장), 불변 값의 ID 참조(4장), 시간 세그먼트의 S3 보존과 로컬 버퍼(5장), 범위 읽기(6장), 오프라인 표 산출(7장). T4: 보존 위치에 투명한 조회, 삭제된 정의 해석, B1~B5 읽기 projection과 예상 시간(6장, `SCH-42~47`).
- M1 밖: Prometheus, native histogram, ServiceMonitor와 PodMonitor, PromQL relay, Dashboard PromQL. 어느 측정 표도 TSDB 값을 원자료로 쓰지 않는다. Dashboard는 ChillServer 조회 API를 그대로 읽는다.

## 3. 요청 기록

### 3.1 행

`requests` stream의 줄은 행 객체 하나이고 `kind`가 `assigned` 또는 `final`이다. 같은 `requestID`의 `final`은 `assigned`를 대신한다. 모든 reducer는 `requestID`로 합쳐 `final`, 없으면 메모리의 열린 행, 없으면 `assigned`를 쓴다. 열린 행은 같은 요청의 `assigned`에 접수 단계까지 더한 것이다.

| 행 | 내용 | 쓰는 지점 |
|---|---|---|
| `assigned` | `requestID`, `workloadUID`, `definitionID`, `receivedAt`, `lifetimeNanos`, `dispatchedAt`, `node`, `instance`, `profileID`(약 300 B) | 배정. append가 실패하면 지금처럼 배정을 되돌린다 |
| `final` | 아래 표 | Runtime이 호출을 지우는 `releaseLocked`, 살아 있는 호출이 없는 `Response`(접수 전 거절) |

| `final` 묶음 | 필드 |
|---|---|
| 식별 | `requestID`, `workloadUID`, `definitionID`, `node`, `instance`, `profileID`, `decisionID` |
| 결과 | `outcome`, `error`, `executionComplete`, `clientSpanNanos` |
| 시각 | `receivedAt`, `lifetimeNanos`, `elapsedNanos`(응답 종료), 단계 오프셋 `validatedNanos`, `acceptedNanos`, `dispatchedNanos`, `queuedNanos`, `runningNanos`, `executedNanos`, `exchangeNanos`. 오프셋은 `receivedAt` 기준이고 도달하지 않은 단계는 비어 있다. 같은 단계를 두 경로가 알리면(교환 결과와 firstfloor 로그의 `backend_finished`) 먼저 접은 시각을 쓰고, 응답 종료만 마지막 값을 쓴다. `queuedNanos`와 `runningNanos`는 호출이 끝나기 전에 접은 firstfloor 로그의 `backend_queued`, `backend_running` 줄에서 온다. `final`을 쓸 때 비어 있는 것은 열린 행이 간직한 실행한 배치의 마지막 `finished` 기록(교환 결과나 로그)에서 같은 worker 시계로 채운다. 실행 시작은 로그 줄과 같은 `At − nativeElapsedMS + queueMS`다. 큐 진입은 firstfloor 수신 시각 `At − nativeElapsedMS`라 로그 줄보다 본문 읽기와 decode만큼 이르고, 그 행의 `queueMS − (runningNanos − queuedNanos)`는 0이다 |
| 배치 | 마지막 backend 진행의 `batchID`, `batchSize`, `queueMS`, `serviceMS`, `backendMS`, `preprocessMS`, `postprocessMS`, `firstTokenMS`, `units`, `nativeElapsedMS` |

- profile 본문(근거 참조, confidence, 측정 조건의 artifact와 실행 경로)은 `profileID`로, SLO와 워크로드 이름은 `definitionID`로 같은 시간의 `catalog`에서 찾는다(4장, `WM-27`).
- 지연 표본과 `completionResidualNanos`는 `aggregate`의 규칙 그대로 행에서 계산한다. `delivery_failed`, `incomplete`, `canceled`를 빼고, 잔여는 `executionComplete`일 때 `elapsedNanos − dispatchedNanos − nativeElapsedMS`다(`SCH-31`, `WM-28`).
- 구현: `RequestEvent` 호출 지점은 그대로 둔다. log는 사건을 요청별 열린 행(메모리)에 접고 `assigned`에서만 append한다. Runtime은 최종 지점에서 `RequestClosed(id)`를 부르고, log는 `final`을 append한 뒤 열린 행을 지운다. 열린 행이 없는 요청의 사건은 버린다. 열린 행 수는 처리 중 요청 수다. 수신, 검증, 접수는 메모리에만 접으므로 거절 판단은 `observationError`가 맡는다.
- 크기: `final` 989 B(E1 창의 실제 121건으로 만든 행)와 `assigned` 약 300 B, 합해 요청당 약 1.3 KB다.

### 3.2 재시작

- 배정 전 요청(수신, 검증, 큐)은 crash 때 행이 남지 않는다. 사용자가 받아들인 손실이고, load-generator JSONL과 `requestID`로 join하면 드러난다(7장의 `recorded`).
- 배정된 요청은 RestartDrain이 그대로 맡는다. 시작할 때 `runtimeFacts`가 로컬 `requests` 파일에서 `final`이 없는 `assigned` 행을 골라 지금처럼 호출을 복원하고 수신을 닫는다. 복원한 행의 `dispatchedNanos`는 `dispatchedAt − receivedAt`이라 최종 행도 접수와 배정으로 셈한다. 복원한 호출은 backend 완료를 관측한 뒤 `final`(`incomplete`, `EntrypointRestarted`)을 쓴다.
- firstfloor 로그 follow는 인스턴스의 복원한 배정 중 가장 이른 `dispatchedAt`부터 읽는다(없으면 프로세스 시작). 그 사이 다시 읽는 줄 중 열린 행이 없는 요청의 줄은 버려진다.
- `RetainedDefinition`은 지우지 않는 루트 `definitions.jsonl`(정의 수에 비례, KB 단위)에서 읽는다. 재시작 때 다시 게시하는 정의는 같은 identity의 같은 본문이 이미 있으면 쓰지 않는다.
- **기존 구현 — 삭제된 정의(`SCH-43`, API B4):** `observation_log.go:DefinitionRecord`가 이름·모델·적용 정의를 `definitions.jsonl`에 남기고 `NewObservationLog`가 다시 읽는다. `runtimeFacts` → `runtime.go:NewRuntime`/`RetainedDefinition`은 기동 시 적용 snapshot을 복원한다. 시간 세그먼트에는 `appendRowLocked` → `referenceLocked`가 `definitionID` 본문을 `catalog`에 남긴다. 삭제나 로컬 시간 디렉터리 정리는 루트 정의를 지우지 않으므로 새 보존 장치를 만들지 않는다.
- **Dashboard 해석 — 이미 반영된 `7a1d78cb`:** `chill-dashboard/lib/workload-identity.ts:resolveWorkloadIdentities`는 UID별 현재 등록 → 관측 `definitions` → scheduler snapshot → UID를 포함한 이름 미확인 표시 순으로 해석한다. 같은 이름의 새 UID를 옛 UID에 연결하지 않는다. 요청 행의 모델은 UID의 최신 모델이 아니라 그 행의 정확한 `definitionID`에서 읽는다(`features/observations.tsx`). 근거 저장소 조회도 archive의 해당 시간 `catalog`를 풀어 같은 `definitions` 응답에 합친다. `RetainedDefinition`을 이름 검색 API로 바꾸지 않는다.
- 검사(`SCH-43`): 삭제·재시작·로컬 시간 삭제 뒤에도 원래 UID의 이름·모델·정의가 남고, 같은 이름으로 재등록한 새 UID와 섞이지 않는다. 같은 UID에서 모델을 바꾼 뒤에도 이전 요청은 그 요청의 `definitionID` 모델을 표시한다.

## 4. 불변 값의 ID 참조

판단과 전력 기록은 불변 값을 ID로 참조하고, 본문은 `catalog` stream의 행 `{kind, key, at, body}`에 둔다. log는 한 시간 안에서 key를 처음 참조할 때, 그리고 같은 key의 본문이 그 프로세스에서 마지막에 쓴 본문과 다를 때 catalog 행을 쓴다. 그래서 시간 세그먼트 하나가 그 시간의 모든 참조를 담는다. 행의 `at`은 그 행을 쓰게 한 기록의 시각이다. 읽기는 참조한 기록의 시각 이전에서 같은 key의 마지막 catalog 행을 쓰고(DuckDB `ASOF JOIN`), 그보다 앞선 기록은 그 key의 첫 행을 쓴다. 재시작 뒤 다시 쓴 같은 본문은 결과를 바꾸지 않는다.

| kind | key | 참조하는 곳 |
|---|---|---|
| `definition` | `definitionID` | 요청 행, snapshot `workloadIDs`(`workloads` 대신), `outstanding[].definitionID` |
| `profile` | `profileID` | 요청 행, keep과 target과 options의 인스턴스(M0부터 `profileID`) |
| `cost`, `baseline` | `<id>@<reference.key>`(측정 evidence object) | snapshot `costRefs`, `baselineRefs`(`costs`, `baselines` 대신) |
| `meter` | meter ID | 전력 행. 본문은 노드, scope, source, sampling, coverage 정책 |

- target: `target.id == keep.id`이면 target을 `{id, keep: true}`로 저장한다(7.7 KB 절약).
- 전력 행: `{at, startedAt, node, meter, samples, failures, powerJoules, powerError}`. `at`은 관측 끝이고, `failures`는 읽기 실패가 있을 때만 있다. 표본으로 다시 계산되는 `summary`는 쓰지 않는다.
- options는 노드 가용성에 따라 판단마다 다른 계획 입력이라 그대로 둔다. 인스턴스는 이미 `profileID`만 담는다.
- `SchedulerObservationPage`는 참조와 target을 본문으로 채워 돌려준다. API와 Dashboard가 받는 형태는 그대로다.
- `candidates` 요약과 부하 0의 판단을 바뀔 때만 쓰는 일은 M4가 평가기와 함께 정한다.

## 5. 세그먼트와 보존

- 파일: `<directory>/v1alpha5/<YYYYMMDDTHH>/<stream>.<MM>.jsonl`. stream은 `requests`, `scheduler`, `management`, `catalog`이고, 쓰는 시각(UTC)의 분이 파일을 고른다. `DefinitionRecord`는 루트 `<directory>/v1alpha5/definitions.jsonl`에 append한다. 버전은 경로가 담으므로 줄에는 `schemaVersion`과 `window`가 없다.
- 업로드: 1분 archive ticker가 지난 시간 디렉터리마다 stream별로 분 파일을 순서대로 이어 gzip한(`compress/gzip`, 헤더 시각 0) 파일을 `serving/observations.v1alpha5/<YYYYMMDDTHH>/<stream>.jsonl.gz`에 `PutImmutable`로 올린다. 같은 입력의 gzip은 같은 바이트이므로 재시작 뒤 다시 올리면 `CreateOutcomeExisting`이다. process는 모든 stream이 성공한 시간을 메모리에 기억하고 그 시간을 다시 올리지 않는다.
- 업로드 자원: gzip은 scratch emptyDir(`/tmp`, 512Mi)의 `.archive-*` 파일로 흘려 쓰고, S3 store의 staging 파일(`chill-evidence-*`)도 같은 `/tmp`에 둔다. 한 업로드는 압축한 stream-시간 하나를 두 벌 놓으므로 `/tmp`는 가장 큰 압축 stream-시간의 2배 이상이다. staging은 archive가 비우는 PVC 밖에 있어서, S3가 멈춰 PVC가 차도 다음 업로드가 시작된다. 압축한 시간은 메모리에 올라오지 않는다(압축 안 되는 8 MiB 시간 업로드의 할당 21.8 MB → 0.86 MB). 시작할 때 재시작으로 남은 두 staging 파일을 지운다. Flush는 serve loop 밖의 goroutine 하나에서 돌고, 앞선 Flush가 도는 동안의 tick은 건너뛴다. source refresh와 `Advance`는 업로드를 기다리지 않는다. 종료는 돌던 Flush가 끝난 뒤 `Close`의 마지막 archive로 간다.
- 로컬 삭제: 한 archive pass에서 그 시간의 모든 stream이 `Created` 또는 `Existing`을 받고, 시간 디렉터리의 시작부터 설정한 보존 기간이 지났으면 디렉터리를 지운다. `servingEntrypoint.observationStorage.retention` → `--observation-retention` → `ObservationLogConfig.Retention`은 양수 Go duration이며 기본 `2h`로 기존 동작(현재 시간과 직전 시간)을 유지한다. 업로드 주기는 그대로 1분이다. 하나라도 실패하면 그 시간과 이후 시간은 남고, 다음 pass가 처음부터 다시 올린다. 아직 `final`이 없는 요청의 `assigned` 행이 있는 시간과 그 이후 시간도 남아 RestartDrain이 읽는다. 삭제는 `mu` 아래에서 하므로 조회는 한 시간을 통째로 열거나 지워진 것으로 본다. PVC는 설정 기간의 원문과 S3가 멈춘 동안의 미업로드분을 담는 버퍼다. S3가 멈추면 PVC가 차고 append 실패가 `readyz`를 503으로 만든다. 실패한 append는 쓰기 전 크기로 잘라 다음 줄이 개행 경계에서 시작하고(루트 `definitions.jsonl` 포함), 실패한 `final`은 열린 행에 남아 다음 `Flush`가 다시 쓴다.
- 시작: 각 stream의 마지막 분 파일과 루트 `definitions.jsonl`을 마지막 개행까지 자른다(crash 때 쓰다 만 줄). 그 밖에 시작 때 읽는 것은 루트 definitions와 로컬 `requests`뿐이다.
- 읽기는 대상 파일을 `mu` 아래에서 열고 잠금 밖에서 읽는다. 삭제는 열린 fd에 영향이 없다. 쓰는 중인 마지막 줄(개행 없음)은 무시한다.
- **D2 결정(`SCH-42`):** 실험 구간은 사후 재생이 가능한 동안 evidence store에 보존한다. 로컬 삭제는 영구 삭제가 아니며 6.2절의 투명 조회가 이를 따른다. `7ad977d1`의 `--observation-retention`(기본 `2h`, 시연 `24h`)은 PVC 버퍼 크기를 조절하는 **임시 운영 수단**이지 과거 조회 보장의 해결책이 아니다. 보존 기간을 늘리는 것만으로 T4를 완료했다고 판정하지 않는다.

## 6. 조회

### 6.1 범위와 기존 reducer

현재 일반 관측 조회는 로컬 분 파일만 연다. T4는 아래의 모집단·집계 규칙은 유지하고, 파일 선택과 stream 읽기에 6.2절의 archive 경로를 합친다. 파일 이름이 시간 색인이며, 줄은 envelope를 먼저 decode해 선택한 본문만 읽는다. scheduler·power 참조는 기록이 속한 시간의 `catalog`로 푼다.

| 조회 | 읽는 범위 | 규칙 |
|---|---|---|
| `Query`(`/observations/workloads/{uid}`) | `requests`: `from`의 분부터 가장 최근 파일까지, `management`: `[from, to]`, 열린 행 | 행은 접수 뒤에 쓰이므로 `from` 앞은 필요 없고, `final`은 언제든 늦게 쓰일 수 있어 끝까지 읽는다. 모집단은 `[from,to)`에 접수된 요청이다 |
| `LoadWindow`, `SchedulerArrivals` | `requests`: `from`의 분부터 가장 최근 파일까지, 열린 행 | 단계 수는 `receivedAt`과 오프셋의 합이 구간 안인 단계로 센다. 열린 행으로 처리 중 요청도 도착에 들어간다 |
| `OperatingPower` | `scheduler`: `[from − 1분, to + 1분]`, 기준 snapshot은 `from`부터 거꾸로 첫 snapshot이 있는 분 파일까지 | 경계 표본 거리는 설정 검사에서 `maximumBoundaryDistance ≤ 1분`으로 묶는다. meter는 catalog의 coverage 정책을 쓴다 |
| `SchedulerObservationPage` | `scheduler`: `[from, to]` | 지금처럼 limit에서 멈춘다 |

- **기존 한계:** `observationRead.gap`은 로컬 시작보다 이른 `from`에 `gapUntil`을 싣고, `OperatingPower`는 `complete=false`로 만든다. T4에서는 archive를 읽은 **뒤에도 실제 없는 구간**만 공백이다(6.2절). 과거 설계의 `SchedulerHistoryWindow`는 현재 소스에 없으며, 도착 조회 경로는 `SchedulerArrivals` → `LoadWindow`다.
- 비용: Tick의 `SchedulerArrivals(now − 60s, now)`는 분 파일 1~2개, 5 req/s에서 1 MB 이하를 읽는다(지금은 창 1~2개, 3.3~6.7 MB). 조회 메모리는 선택된 요청 수에 비례한다. 기본 보존 2시간 기준 5 req/s의 요청 행은 36k개(약 47 MB)이며 보존 기간을 늘리면 조회 가능한 행 수도 비례해 늘어난다.

### 6.2 투명한 로컬·archive 읽기 (`SCH-42`)

- **재사용 경계:** `observation_log_capacity.go:ObservationArchive`는 `evidencestore.Reader`+`Lister`이고, `CapacityRecords`는 `config.Writer`에서 이를 얻어 `List`와 `readArchivedStream(ctx, archive, object, visit)`를 이미 쓴다. 같은 인터페이스와 gzip scanner를 재사용한다. 대상은 `serving/observations.v1alpha5/YYYYMMDDTHH/{requests,management,scheduler,catalog}.jsonl.gz`이며 새 저장 형식·TSDB·옛 버전 reader는 없다. `cmd/chill-server/server.go:run`이 넘기는 `objects`를 그대로 읽는다.
- **선택·데이터 흐름:** `capture`가 `mu` 아래에서 필요한 로컬 시간 디렉터리와 fd·크기를 한 번 잡는다 → 잠금 밖에서 기존 prefix의 객체를 `List`해 필요한 UTC 시간만 고른다 → 시간 오름차순으로 stream을 scan한다. **시간 디렉터리 단위로 로컬이 우선**이며, 잡힌 로컬 시간이면 이미 업로드된 같은 시간의 객체는 읽지 않는다. 없는 시간만 archive로 읽고 그 시간의 catalog도 함께 푼다. 파일이 열린 뒤 삭제돼도 fd를 계속 쓰므로 조회 중 로컬 삭제가 중복이나 누락을 만들지 않는다. stream 파일이 없다는 이유로 같은 로컬 시간의 archive를 추가하지 않는다.
- **중복·페이지:** source 시간 선택 외에도 `requestRows`/`mergeRequestRow`의 `requestID`별 `final > open > assigned` 병합을 유지한다. archive의 assigned와 로컬의 늦은 final은 요청 하나다. scheduler·management·power는 같은 시간을 두 소스에서 읽지 않아 중복 가산하지 않는다. scheduler page는 source가 아니라 합친 시간순 append 순서에 기존 offset/limit을 적용하며 `errObservationStop`을 archive scanner에서도 정상 종료로 처리한다.
- **응답 모양:** 네 경로의 기존 본문·필터·`nextCursor`를 유지하고 `coverage: {from,to,sources:[{from,to,stream,source}],gaps:[{from,to,stream,reason}]}`를 더한다. 시각은 RFC3339Nano, `source`는 `local|archive`이고 archive 항목에는 `store,key`를 싣는다. coverage의 from/to는 확보된 범위의 외곽(전부 없으면 null), 내부 공백은 gaps로 드러낸다. 로컬 floor만으로 공백을 만들지 않는다. 기존 `gapUntil`은 실제 선두 공백이 있을 때만 남기고 중간 공백은 gaps에 둔다.
- **없는 자료와 실패:** 현재 `upload`는 빈 stream의 객체를 만들지 않는다. 따라서 archive에 필요한 stream·시간이 없으면 관측 0건이라고 단정하지 않고 `StreamUnavailable` 공백으로 표시한다(빈 시간과 유실을 구분할 새 manifest는 만들지 않는다). catalog 참조 누락·잘린 gzip·JSON 오류·store List/Open 실패는 조회 오류이며 성공한 빈 응답으로 숨기지 않는다. power의 경계 표본 또는 기준 snapshot이 없으면 기존 reducer의 `complete=false`와 이유를 유지한다. 읽기 실패는 append 실패가 아니므로 읽기 전용 실패만으로 쓰기 `readyz`를 내리지 않는다.
- **시간 제한:** `Query`, `LoadWindow`, `SchedulerArrivals`, `SchedulerObservationPage`, `OperatingPower`에 `context.Context`를 전달하는 형태로 모든 호출부를 함께 이관한다. HTTP는 `ServeHTTP`의 RequestTimeout·취소를 List/Open/scan에 전달하고 내부 호출은 controller capture의 ctx를 쓴다. 네트워크 읽기는 `mu` 밖이며 기존 `limit`은 유지한다. 최근 도착 창이 로컬에 모두 있으면 archive List/Open 없이 기존 경로로 끝낸다.

| 대상 API·변경 위치(조사 시점 행) | 변경할 데이터 흐름 |
|---|---|
| `GET /api/v1/observations/workloads/{uid}` — `observation_log.go:Query`(996~1058 부근, 현재 1000~1061), `capture`/`observationRead`(1174~1228 부근), `requestRows`, `scanStream` | requests는 from부터 최신까지, management는 요청 구간을 로컬/archive에서 읽고 definitions를 archive catalog와 합친 뒤 기존 aggregate·페이지 적용. `httpapi/handler.go:observations`에서 ctx·coverage 전달 |
| `GET /api/v1/observations/load` — `observation_log_scheduler.go:LoadWindow`/`SchedulerArrivals`(344~422) | 같은 병합 요청 행으로 기존 단계 시각별 집계. `httpapi/scheduler.go:schedulerObservations`와 `kube/scheduler.go:SchedulerController.capture`의 호출도 함께 이관 |
| `GET /api/v1/observations/scheduler` — `observation_log_scheduler.go:SchedulerObservationPage`(427~477), `catalogFor`/`DecodeSchedulerLine` | 각 source 시간의 catalog를 먼저 읽고 판단·작업·snapshot 참조를 복원. `httpapi/scheduler.go:schedulerObservations`의 full·compact 양쪽 응답에 같은 coverage 전달 |
| `GET /api/v1/observations/power` — `observation_log_power.go:OperatingPower`(121~175), `openOlder` | `[from−1분,to+1분]`을 source별로 합치고 기준 snapshot이 나올 때까지 앞선 로컬/archive 시간을 역탐색한다. 앞선 archive snapshot의 catalog도 복원하고 `ReduceOperatingPower`는 그대로 사용. `httpapi/power.go:operatingPower` → `HandlerConfig.OperatingPower` → `cmd/chill-server/scheduler_service.go:operatingPower`에 ctx 전달 |

검사: 같은 시간에 로컬·archive가 모두 있어도 요청 수·단계 수·판단 페이지·J가 한 소스와 같다. 로컬 삭제 전후 같은 실험 구간의 네 응답과 출처를 비교하고, archive assigned/로컬 late-final을 한 번만 센다. 페이지 경계, archive catalog만 남은 삭제 UID, power 앞뒤 표본·더 오래된 기준 snapshot, 내부 누락 시간, store 오류·취소, 조회 중 삭제를 각각 검사한다. 현재 scanner의 취소 확인과 gzip 종료 오류 전달도 `readArchivedStream` 확장 시 검사한다.

### 6.3 측정 CR 두 종류의 읽기 projection (API B1, `SCH-44`, `DB-FR-45`)

- `GET /api/v1/namespaces/{namespace}/operation-cost-measurements[/{name}]`, `.../dispatch-occupancy-measurements[/{name}]`를 추가한다. 목록은 `{items:[projection]}`(이름순, 빈 목록 `[]`), 상세는 projection 하나이며 없으면 404다. projection은 `{kind,metadata:{namespace,name,uid,resourceVersion,creationTimestamp},node,conditions,status,references}`의 명시적 whitelist다. operation-cost의 status는 기존 `{phase,reason,steps:[{name,key,verdict}]}`, occupancy는 `{phase,reason,powerMode,perRequestNanos,throughput,result}`다. 불완전·실패·결과 없음도 원래 상태로 내보내며 0으로 채우지 않는다.
- **안전한 측정 조건:** occupancy의 `conditions`는 `{workload,batchEnergyOptimization,batch,supplyMode,warmupSeconds,durationSeconds,workers}`, operation-cost는 `{steps:[{name,kind,powerMode?,batchEnergyOptimization?,idleDurationSeconds?}]}`다. 기존 CR spec의 해당 필드만 복사하고 참조 이름은 CR namespace 안에서 해석한다. supplyMode 등 원본의 미지정 값은 미지정으로 표시하며 새 측정 조건으로 보정하지 않는다. `conditions.inputsJSON`, 전체 spec·원시 표본은 노출하지 않는다.
- **원본 연결:** `references:{cr:{apiVersion,kind,namespace,name,uid},evidence:[{store,key}]}`로 원본 CR 신원과 측정 근거 객체 위치를 함께 제공한다. operation-cost는 `status.steps[].key`, occupancy는 `status.result`의 실제 보존 참조에서 evidence 목록을 만든다. 아직 결과가 없으면 `evidence:[]`이며 근거 key를 추측하지 않는다. 원본 CR은 name뿐 아니라 UID도 표시해 같은 이름의 재생성과 구분하고, 근거 열기는 기존 권한 경계를 따르는 객체 참조로 연결한다(임의 외부 URL이나 호스트 경로는 반환하지 않음).
- **위치·흐름:** `httpapi/handler.go:NewHandler`에 GET 등록 → `httpapi/measurements.go:measurements`의 bearer·namespace/name 검증과 `AuthorizeResource` 패턴 재사용 → `cmd/chill-server/measurement_service.go:namespace` 및 `list` 패턴으로 CR을 `List`/`Get` → `api/v1alpha1/{operationcostmeasurement,dispatchoccupancymeasurement}_types.go`의 선택한 spec 필드·metadata·status를 위 DTO로 복사한다. `HandlerConfig`/`cmd/chill-server/server.go:run`에 두 종류 읽기 callback을 연결한다.
- 두 리소스 각각 caller의 namespace 범위 `list`/`get` SAR가 필요하다(서버 ServiceAccount 권한으로 대체하지 않음). POST/PUT/DELETE는 추가하지 않고 기존 CR 쓰기 경계를 유지한다. 검사: 두 종류 목록·상세와 조건 whitelist의 원본 일치, 원본 CR namespace/name/UID·결과 key 연결, namespace 거절·list만/get만 허가, 404, Measuring/Failed/Insufficient·결과 미확보, 전체 spec/inputsJSON/raw 비노출.

### 6.4 실험 불변 요약 조회 (API B2, `SCH-45`)

- `GET /api/v1/experiments`는 `{items:[summary]}`(from 내림차순·id 순), `GET /api/v1/experiments/{id}`는 summary 하나다. summary는 `{schemaVersion:"serving-experiment.v1alpha1",id,from,to,status,stages:[{id,from,to,kpis}],sources:[{store,key}]}`다. 단계 KPI는 기존 E5 판정의 요청 수·결과별 수·성공/SLO 분자와 분모·지연 분위수·전체/활성 POOL J를 해당 모집단 설명과 함께 보존한다. 미측정은 null과 이유이며 통과로 바꾸지 않는다. 단계 구간은 `[from,to)`, 출처는 허용된 evidence store의 객체 위치이며 클라이언트가 임의 URL/호스트 파일 경로를 열게 하지 않는다.
- **위치·흐름:** `hack/e5/e5.py:summarize`는 지금 `e5-summary.json`을 로컬에만 쓴다. 실행 종료 시 이 결과와 실제 `phase.json` 경계·KPI·출처를 위 요약으로 투영해 기존 evidence store `PutImmutable` 계약으로 `serving/experiments.v1alpha1/{id}/summary.json`에 올리는 출판 단계를 붙인다. 관측·loadgen 등 원자료를 먼저 보존하고 최종 요약을 마지막에 한 번 올린다. 같은 ID를 수정하지 않고 정정 실행은 새 ID다. `PRIVATE_OPERATIONAL_ONLY`, 미측정·실패·한계 해석은 그대로 남긴다.
- `httpapi/handler.go:NewHandler`에 읽기 경로 등록, `HandlerConfig`와 `cmd/chill-server/server.go:run`의 기존 objects Reader/Lister 주입으로 목록·상세를 제공한다. 서버는 요약을 읽을 뿐 E5를 실행하거나 KPI를 재계산하지 않는다. 기존 `evidencestore/store.go:Reader`/`Lister`를 재사용하고 새 DB·캠페인 관리 API는 만들지 않는다. id는 단일 안전 경로 요소로 검증하고 prefix 밖 접근은 거절한다. 두 경로 모두 기존 `authorize`의 해당 non-resource GET 권한과 HTTP ctx를 따른다.
- 검사: 출판 후 목록/상세의 동일 ID·단계 경계·출처, 같은 ID 덮어쓰기 거절, 요약 없는 실행이 완료 목록에 나타나지 않음, 미측정/실패 보존, 404와 store 실패 구분, 권한 거절·경로 이탈 거절.

### 6.5 축약 상태 조회 (API B3, `SCH-46`)

- `GET /api/v1/scheduler/state?view=summary` → `{at,nodes,instances,routing,decision:{id,at,status,reasons,targetID},actions}`. 노드는 식별·전력모드·가용/준비 상태, 인스턴스는 식별·node·소유/배분 UID·profileID·ready/routable·이유만, actions는 planned/running 작업의 ID·종류·상태·의존·elapsedNanos·예상 시간만 투영한다. `routable`은 T2의 서버 readback을 쓰며 UI가 ready에서 추론하지 않는다.
- **기존 compact와 다름:** `httpapi/scheduler_presentation.go:schedulerReadState`는 원본 struct를 embed하고 큰 profile만 참조화하므로 κ 입력·options·costs 등이 여전히 있다. summary는 그 struct를 재사용하지 않고 명시적 whitelist DTO로 만든다. `schedulerCompactQuery`의 view 파싱을 `scheduler.go:Handler.scheduler`와 함께 확장하되 summary는 `/state`에서만 허용하고 기존 full/compact 의미는 유지한다. `kube/scheduler.go:SchedulerController.State`에서 얻은 같은 상태 한 벌을 투영하며 전체 상태 JSON을 만든 뒤 지우지 않는다. 프로파일 본문·κ 표·후보 목록·과거 results·raw 증거는 제외한다.
- 검사: 같은 상태의 full/summary에서 ID·노드·배분·진행 작업이 일치, profile/κ/중첩 원본 필드 미노출, 대규모 options를 넣어도 summary 크기가 옵션 수에 비례하지 않음, 잘못된/중복 view 거절, 기존 조회 권한 유지.

### 6.6 작업 예상 시간 (API B5, `SCH-47`)

- 기존 action에 `expectedSeconds:number|null`, `expectedSource:{costID,reference,statistic:"mean",trialCount}|null`을 더한다. 선택된 동일 조건 cost의 유효 실측 trial `durationNanos` 산술평균을 초로 변환한다. 근거 없는 route/stop/drain 또는 조건에 맞는 측정이 없는 작업은 둘 다 null(예상 시간 미확보)이며 임의 상수·0을 주지 않는다. 이는 action 자체의 예상 실행 시간이지 DAG 전체 ETA·timeout·실제 경과가 아니다. `elapsedNanos`, `startedAt`, `completedAt`은 기존 실제 진행/완료 필드로 따로 표시한다.
- **위치·흐름:** `scheduler.go:Action` → `scheduler_plan.go:schedulerPlanState.selectCosts`가 고른 `CostID`의 `OperationCost.Trials`/`Reference`로 한 번 산출한다(`scheduler_capacity.go:capacityOperationCost`의 유효 실측·평균 규칙 재사용). 실행 중에는 시작 시의 근거를 고정하고 새 비용으로 남은 시간을 덮어쓰지 않는다. `kube/scheduler.go:actionsLocked`/`scheduledOperation`, `kube/operations.go:Operation`에 같은 필드를 전달하고 `cmd/chill-server/scheduler_service.go:recordAction` → `observation_log_scheduler.go:SchedulerActionObservation`에도 보존한다. 수동 작업은 기존 비용 선택의 동일 조건에 맞는 경우만 채우며 없으면 null이다.
- full/compact/summary 상태, 저장된 scheduler action과 라이브 action은 같은 값을 전달한다(라이브 전송은 Dashboard 설계 소관). 이전 관측의 필드 부재는 null로 해석하고 과거 cost를 새로 대입하지 않는다. 검사: 조건 일치 cost만 선택, 복수 trial 평균·출처·개수, 근거 없음 null, 실행 중 비용 갱신에도 불변, 예상값과 실측 elapsed/완료 시간이 섞이지 않음.


## 7. 측정 표 산출

표의 원자료는 S3 세그먼트와 load-generator JSONL이다. 각 시간 세그먼트가 `catalog`에 그 시간이 참조한 정의, profile, 비용, meter 본문을 담으므로 실행 구간의 시간 prefix만 받으면 된다. `hack/observations/`에 세 가지를 둔다.

| 파일 | 하는 일 |
|---|---|
| `fetch.sh <from시> <to시> <dir>` | evidence store의 S3 endpoint에서 시간 prefix를 `<dir>/obs/<시>/`로 받는다. 전력 경계 표본과 기준 snapshot을 위해 앞뒤 한 시간을 더 받는다. 단계별 load-generator 출력은 실험 스크립트가 `<dir>/loadgen/<단계>.jsonl`로 둔다 |
| `e1.sql` | `duckdb < hack/observations/e1.sql`. 요청 표(완료 여유 m 열은 [용량표 계획기 5장](analytic-planner.md#5-완료-여유-규칙)), 포화 표(인스턴스별 요청당 시간, 디스패치 점유 측정), Tick 표(`decision.evaluationElapsedNanos`) |
| `power/main.go` | `go run ./hack/observations/power --dir <dir>/obs --scheduler-config <file> --from --to`. 전체와 활성 POOL 에너지. meter 집합은 실행 때 수락된 scheduler 설정의 `meters`이고, `[from − 1분, to + 1분]`이 걸친 시간의 segment가 없으면 거절한다 |

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
- POOL 에너지: coverage 정책 적분과 snapshot 소속 재구성은 서버의 `OperatingPower` reducer를 그대로 쓴다. cutover commit이 reducer를 scheduler 기록 iterator를 받는 함수로 떼고(약 40줄), `power/main.go`가 gz 세그먼트를 그 iterator로 넘기고, 서버처럼 scheduler 설정의 meter 집합을 넘긴다. catalog의 meter 행은 기록된 본문을 푸는 데만 쓴다(약 120줄과 test 하나). SQL로 적분을 다시 쓰지 않으므로 서버와 같은 값이 나온다.
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
- 시연용 `values-operating.yaml`은 32Gi PVC에 `retention: 24h`를 임시로 선택한다(`7ad977d1`). 위 5 req/s 추정의 155 MB/2h를 선형 적용하면 약 1.86 GB/24h(기본보다 +1.705 GB, 32Gi의 약 5.4%)이며 실측 용량 보장은 아니다. 시간 디렉터리 단위 삭제와 무관하게 과거 조회의 영구 경로는 evidence store다(6.2절).

## 9. 계약과 버전

| 대상 | 변경 |
|---|---|
| 저장 | `serving-observations.v1alpha4` → `v1alpha5` 한 번([ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)). writer, reader, API, Dashboard를 commit 2 하나에 넣어 `v1alpha5` 시간 key에는 한 형태만 올라간다 |
| 로컬 | `<directory>/v1alpha5/`. 배포 뒤 `<directory>`의 나머지(`v1alpha4` 창 디렉터리)를 지운다 |
| S3 | `serving/observations.v1alpha5/<시>/<stream>.jsonl.gz`. 배포 뒤 `serving/<windowID>/`(`v1alpha4` 창)를 reader 없이 지운다. E1 표는 이미 만들었다 |
| API | `Query`의 `requests[]`는 요청 행(`final`, 없으면 `assigned`). `Query`, `LoadWindow`, `OperatingPower`, scheduler 조회의 `incompleteWindows` 자리에 `gapUntil`. `ObservationLogHealth.windows` 삭제. `ObservationGap` 오류 추가 |
| 설정 | `--observation-window`, Helm `servingEntrypoint.observationWindow` 삭제. scheduler 설정 `powerObservation.coverage.maximumBoundaryDistance ≤ 1분` |

배포 순서:

1. 새 ChillServer를 배포한다. 루트 `definitions.jsonl`은 비어서 시작하고, 적용된 정의는 이전 계약에서 옮기지 않는다(ADR 0019). 그래서 기존 Workload는 `AppliedSnapshotUnavailable`이 된다.
2. 기존 Workload마다 지금 spec 그대로 `PUT /api/v1/workloads/{name}`을 보내 다시 적용한다. 적용이 `Completed`, 접수가 `Ready`가 되고 바인딩과 routing이 돌아오는지 확인한다.
3. 위 표의 이전 로컬 디렉터리와 S3 사본을 지운다.

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
| 2 세그먼트 | 쓰는 시각의 분이 파일을 고름. 지난 시간의 gz를 풀면 분 파일을 이은 줄과 같음. stream 하나의 업로드가 실패하면 그 시간 디렉터리를 지우지 않고, 다음 pass가 `Existing`과 `Created`를 섞어 마친 뒤 지움. 직전 시간은 지우지 않음. 마지막 줄이 잘린 분 파일과 루트 `definitions.jsonl`을 시작이 개행까지 자름. 조회가 6장 범위 밖 파일을 열지 않음. T4에서는 로컬보다 앞선 from도 archive가 있으면 공백 없이 조회하고, 없으면 실제 공백·출처를 표시(6.2절). 조회 중 삭제에서 같은 결과, `-race` |
| 2 요청 | `final` 행 필드와 잔여 지연이 사건 기반 cohort test와 같은 값. 접수 2분 뒤 닫힌 요청의 `final`을 `Query`가 찾음. `assigned`와 `final`이 함께 있는 완료 요청을 `LoadWindow`와 `SchedulerArrivals`가 한 번만 셈. 배정 append 실패가 배정을 되돌림. 모르는 요청의 backend 진행은 줄을 쓰지 않음. 재시작 뒤 `final` 없는 `assigned`가 RestartDrain으로 복원되고 완료 뒤 `incomplete` 행 하나. evidence 참조 19개 규모에서 요청당 1.5 KB 이하 |
| 2 참조 | target이 keep이면 stub 저장. page가 workloads, costs, baselines, target, meter source를 원래 본문으로 돌려줌. 시간 안에서 meter 설정이 바뀌면 이후 기록이 새 본문을 씀. 재시작 뒤 catalog 재기록이 결과를 바꾸지 않음. 각 시간 디렉터리의 catalog만으로 그 시간의 참조가 모두 풀림. 분 경계를 걸친 `OperatingPower`가 같은 J. E1 규모 판단 기록 135 KB 이하 |
| 2 Dashboard | `make dashboard-check` |
| 3 | `power` test가 같은 기록에서 서버 `OperatingPower`와 같은 J |
| 전체 | `make lint test`, `make helm-lint helm-template` |

T4 구현 시에는 3.2절·6.2~6.6절의 `SCH-42~47`별 검사와 HTTP 인증·projection 검사를 추가한다. 이 문서 변경에서는 테스트·빌드·실클러스터 검사를 실행하지 않았으며 기존 M1 검사 통과를 T4 통과로 재사용하지 않는다.

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
- 로컬 보존(D2): 기본 2시간, 시연 24시간은 임시 운영 설정이다(`7ad977d1`). 과거 실험 조회는 근거 저장소를 투명하게 읽고, 실제 없는 기록만 관측 공백으로 보인다(`SCH-42`, 6.2절).
- 질문 2: commit 1은 `maximumBoundaryDistance`를 1분 이하로 묶는다. testbed scheduler ConfigMap의 값이 이 안에 있는지 배포 전에 확인한다(이 문서는 클러스터를 읽지 않았다).
