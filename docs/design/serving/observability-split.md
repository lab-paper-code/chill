# 관측 분리와 E5 증거 API

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M1 설계와 2026-10-25 T4 조회 확장 설계다. ChillServer는 관측을 기록하고 로컬 버퍼 또는 evidence store의 시간 세그먼트에서 조회에 답한다. 측정 표의 오프라인 산출도 같은 세그먼트를 쓴다(spec README "SLO 관측과 집계"). [M0](decision-record-diet.md)를 전제로 하며, T4의 요구사항 원본은 [scheduler SRS](../../spec/scheduler/SRS.md)의 `SCH-42~47`이다. 6장의 조회 확장은 코드에 반영하며, 배포·클러스터 검증 완료를 뜻하지 않는다.

2026-10-30 E5 증거 API 계약은 [17장](#17-e5-증거-api-2026-10-30)이다(SCH-67~70, SCH-AC-52~55, [ADR 0030](../../adr/0030-observation-evidence-api-streams.md)). 앞의 M1/T4 이행 기록은 보존한다. E5에서는 기존 `v1alpha5`를 유지하며, 9장의 과거 버전 삭제 절차나 7장·15장의 호스트/S3 직접 회수 절차를 새 수집 경로로 사용하지 않는다. 이 문서 변경은 코드·배포·리허설을 수행하지 않는다.

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
- 재시작 메모리: 요청별 `*RequestRow` 맵에서 `final`은 nil tombstone으로 남겨 완료 payload를 버리고 늦은 assigned를 막는다. native는 무시하고 마지막 assigned만 보유해 스캔 후 미완료 요청만 복원한다. ID 맵은 전체 요청 수, 보유 payload는 미완료 수에 비례하며 torn-line 복구는 파일 끝부터 4 KiB씩 읽는다.
- 읽기는 대상 파일을 `mu` 아래에서 열고 잠금 밖에서 읽는다. 삭제는 열린 fd에 영향이 없다. 쓰는 중인 마지막 줄(개행 없음)은 무시한다.
- **D2 결정(`SCH-42`):** 실험 구간은 사후 재생이 가능한 동안 evidence store에 보존한다. 로컬 삭제는 영구 삭제가 아니며 6.2절의 투명 조회가 이를 따른다. `7ad977d1`의 `--observation-retention`(기본 `2h`, 시연 `24h`)은 PVC 버퍼 크기를 조절하는 **임시 운영 수단**이지 과거 조회 보장의 해결책이 아니다. 보존 기간을 늘리는 것만으로 T4를 완료했다고 판정하지 않는다.

### 5.1 E5 전 운영 보존·기동 예산 결정 (2026-10-27)

**권고와 설정:** `values-operating.yaml`의 로컬 보존을 `24h`에서 기본 `2h`로 복원한다. `e659ccb1`의 24h는 시연 2회차에서 `SCH-42` 검증을 기다리는 임시값이었다. 현재 공통 reader의 `prepare/selectSources`는 없는 로컬 시간을 S3에서 읽고, UTC hour 정규화 수정(`81ea4c59`, `6b2755ec`)도 반영됐다. `TestObservationArchiveTransparentReadsAndCatalog`는 로컬 삭제 뒤 Query·LoadWindow·scheduler page의 동일 원자료·정의 해석을 검사한다(이번 조사에서 재실행하지 않음).

**E5 집계 영향:** `hack/e5/e5-archive.py:collect`는 drain+1분 지원 표본을 기다린 뒤 retained 시간의 fd를 먼저 열고, **로컬에 없는 과거 시간은 exact-hour S3 List → `_fetch` → gzip EOF/CRC 확인**으로 수집한다. 현재 시간은 로컬에서 필수이며, required scheduler 부재·S3 실패는 `NOT_READY`이지 0/성공 대체가 아니다. `e5-raw.py`와 기존 power reducer는 수집한 `obs`를 읽으므로 180분 E5나 170분 static의 앞부분이 2h를 넘어도 이 경로 자체는 바뀌지 않는다. archive 성공 전 삭제 금지·미완료 assigned 시간 보호는 유지한다. 따라서 정상 S3 보존·접근과 collection deadline 안의 다운로드가 전제일 때 **원자료/집계 의미 손실은 없다**. 긴 창은 S3 읽기량·소요가 늘 수 있으므로 짧은 E5-0 성공을 350분 수집 시간 검증으로 대체하지 않는다. 6h/24h는 이 정확성에 필요하지 않다.

**기동은 압축량을 통째로 heap에 올리지 않는다.** `34ec34f7` 이후 `restore`는 모든 로컬 requests JSONL을 순차 decode하되 완료 payload는 버리고, **완료 ID의 nil tombstone은 스캔 끝까지 보유**한다. “map이 미완료만 보유”는 payload에만 맞고 ID에는 맞지 않는다. 비용은 `definitions 바이트 + Σ로컬 requests 원문 바이트`의 I/O·decode와 O(전체 고유 요청 ID) 맵 + O(미완료 payload) 메모리다. scanner는 64KiB/최대 한 줄, torn-tail은 4KiB씩 읽는다. scheduler/management/catalog 원문 전체는 constructor에서 읽지 않으며, 별도 catalog/Kubernetes 초기화 비용도 남는다. `Retention`은 constructor cutoff가 아니라 **기동 후 archive 삭제 조건**이라 최초 2h 적용 rollout도 기존 24h를 먼저 복원한다. 수동 삭제·새 빈 디렉터리 전환은 하지 않는다.

읽기 전용 파일 stat(2026-10-27T10:03:32Z, 실제 serving Pod UID `e8dc048a-91f6-4be9-851c-7c95ac2a8933`)의 비교다. 2h/6h는 업로드 성공·미완료 pin 없음의 가상 삭제 후 값이지 실제 삭제 결과가 아니다. 루트 definitions 416,375B는 별도다.

| 로컬 기간 | 시간/파일 수 | 전체 원문 | 기동 requests 읽기 | requests 읽기만 50 MB/s 가정 |
|---|---:|---:|---:|---:|
| 24h(실제) | 24 / 2,022 | 14.251 GB | 11.123 GB | 222.45s |
| 6h(가상) | 6 / 428 | 0.927 GB | 0.430 GB | 8.60s |
| 2h(가상) | 2 / 74 | 0.152 GB | 0.00937 GB | 0.19s |

시간 열은 **[INFERENCE] 바이트/가정 처리율**이며 측정 ETA가 아니다. JSON decode·GC·메타데이터·후속 초기화는 별도이고 cache/CPU/요청 밀도에 따라 달라진다. rev93 실제 로그는 시작 후 147.043s에 `456→459→456 MiB`, 153.159s에 `880→880→15 MiB`, Ready 약294s였다. 이를 순수 restore 시간이나 향후 상한으로 해석하지 않는다. 또한 재시작 시 `archived` 성공 맵은 비어 있어 첫 archive pass가 지난 시간의 **모든 stream을 다시 gzip·PutImmutable(Existing 가능)**한다. 위 실제 자료에서 지난 시간 원문 약14.241 GB를 추가로 읽으므로 constructor requests와 합한 로컬 원문 읽기만 약25.363 GB이며 gzip staging 읽기/쓰기·S3 시간은 별도다. 24h 유지에는 payload 수정 뒤에도 이런 비용이 남는다.

크기 예상은 사용자 기준 **150 MB gzip/h**의 단순 시간 곱이다(MB=10⁶B, GiB=2³⁰B). 압축물은 S3 크기이며 PVC는 JSONL 원문이다. 최근 보존 manifest(`preflight-control-195-u2x/smoke-MIX`, 08시 전체+09시 일부)에서 stream 합계 32,869,396B gz / 158,205,468B 원문 = 4.813배, requests만은 12.673배였다. 다음 원문 열은 **[INFERENCE] 그 혼합비가 지속된다는 조건부 추정**이지 E5의 보장 압축비가 아니다.

| 구간/보존 | gzip 누적 | 혼합비 4.813배의 원문 예상 |
|---|---:|---:|
| E5 180분 | 450 MB | 2.166 GB |
| static 170분 | 425 MB | 2.046 GB |
| 합계 350분 | 875 MB (834.5 MiB) | 4.212 GB |
| 정상 2h 버퍼 | ≤300 MB 상당 | ≤1.444 GB |
| 정상 6h 버퍼 | ≤900 MB 상당 | ≤4.332 GB |
| 정상 24h 버퍼 | ≤3,600 MB 상당 | ≤17.327 GB (16.14 GiB) |

버퍼의 ≤는 일정 발생률·정상 업로드·분 단위 삭제·pin 없음의 시간 버킷 근사이며 실제 상한이 아니다. 합계350분은 24h 유지라면 기존 데이터에 더해지지만, 정상 2h에서는 앞 시간부터 S3로 옮겨져 전량이 로컬에 누적되지 않는다. 모든 gzip 바이트가 requests와 같은 12.673배라고 놓는 민감도 시나리오는 24h 원문45.624 GB로 PVC32Gi도 넘는다. 현재 24h 원문13.27GiB가 작다는 사실이나 gzip3.6GB만으로 디스크/메모리 안전을 인증하지 않는다. 가장 큰 stream-시간을 gzip150MB로 놓으면 scratch 두 벌300MB=286.1MiB로 512Mi 안이지만, stream 하나가268.4MB를 넘으면 두 벌만으로 512Mi를 넘는다.

**메모리 판정:** `GOMEMLIMIT=3072MiB`, container limit `4Gi`를 유지한다(실제 rev96 Pod 설정 확인). rev93의 관측 heap 최대880MiB를 기준으로 soft target까지2,192MiB, hard limit까지3,216MiB의 산술 차이가 있어 **복원 수정의 기동 여유는 관측됐다**. 하지만 heap은 RSS/charged peak가 아니고 1,024MiB의 target↔limit 차이는 runtime·stack·page cache·scratch 등 전체 비heap 사용 보장이 아니다. 미완료 payload, 요청 body/queue, 후속 catalog, 긴 archive 조회·집계 및 전체 ID 맵은 별도다. 150MB/h나 부분합 κ를 E5 요청 수/전체 heap 상한으로 환산하지 않으며 **E5 전체 메모리 여유는 아직 미인증**이다. 추가 메모리 증설 없이 2h로 불필요한 복원량을 줄이고, CampaignOps의 직렬 적용·E5-0에서는 실제 cgroup 메모리/GC·Ready/restart와 archive 출처·공백을 보존한다. 측정 중 재시작·호스트 빌드나 이번 조사에서의 부하 검증은 하지 않았다.

원시/계산 근거: `O/intent/observation-retention-20261027.json`(`O=~/.cache/chill-assets/ops/pools-deploy`), `O/campaign/v2/d-20261027/control-192/{startup.log,gc-heap.json}`, `O/deployB/control-195-20261027/pods-ready.stdout`. 라이브 적용은 CampaignOps 소유이며 이 설정 커밋은 적용/검증 완료 주장이 아니다.

## 6. 조회

### 6.1 범위와 기존 reducer

일반 관측 조회는 로컬 분 파일과 로컬에 없는 시간의 archive를 함께 읽는다. 아래 모집단·집계 규칙은 유지하고 파일 선택과 stream 읽기는 6.2절의 공통 경로를 쓴다. 파일 이름이 시간 색인이며, 줄은 envelope를 먼저 decode해 선택한 본문만 읽는다. scheduler·power 참조는 기록이 속한 시간의 `catalog`로 푼다.

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
- **시간 제한:** `Query`, `LoadWindow`, `SchedulerArrivals`, `SchedulerObservationPage`, `OperatingPower`, `ScanOccupancyRows`는 첫 인자로 `context.Context`를 받는다. HTTP는 `ServeHTTP`의 RequestTimeout·취소를 List/Open/scan에 전달하고 내부 호출은 controller capture의 ctx를 쓴다. 네트워크 읽기는 `mu` 밖이며 기존 `limit`은 유지한다. 최근 도착 창이 로컬에 모두 있으면 archive List/Open 없이 기존 경로로 끝낸다.

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
- **출판 도구:** `go run ./hack/e5/publish --config <private-store.json> --manifest <private-manifest.json>`은 기존 S3 `PutImmutable`로 manifest의 `objects`를 먼저 올리고 `summary`를 마지막에 올린다. 설정은 `logicalStore,bucket,endpoint,region,accessKeyID,secretAccessKey`(선택 `sessionToken,tempDir`)이며 외부 공개 응답에 넣지 않는다. manifest 항목은 `{store,key,localPath}`이고 동일 실험 prefix의 원자료와 요약만 허용한다. 이미 존재하는 요약은 덮어쓰지 않고 실패한다.
- **부분 출판 충돌:** 원자료도 `CreateOutcomeCreated`만 허용한다. 원자료 일부가 저장된 뒤 중단되었으면 같은 ID 재출판은 `Existing`에서 즉시 충돌로 거절하고 새 ID를 요구한다. 원자료나 요약의 digest·내용 비교로 같은 실행임을 추정하지 않는다.
- 검사: 출판 후 목록/상세의 동일 ID·단계 경계·출처, 같은 ID 덮어쓰기 거절, 요약 없는 실행이 완료 목록에 나타나지 않음, 미측정/실패 보존, 404와 store 실패 구분, 권한 거절·경로 이탈 거절.

### 6.5 축약 상태 조회 (API B3, `SCH-46`)

- `GET /api/v1/scheduler/state?view=summary` → `{at,nodes,instances,routing,decision:{id,at,status,reasons,targetID},actions}`. 노드는 식별·전력모드·가용/준비 상태, 인스턴스는 식별·node·소유/배분 UID·profileID·ready/routable·이유만, actions는 planned/running 작업의 ID·종류·상태·의존·elapsedNanos·예상 시간만 투영한다. `routable`은 T2의 서버 readback을 쓰며 UI가 ready에서 추론하지 않는다.
- **기존 compact와 다름:** `httpapi/scheduler_presentation.go:schedulerReadState`는 원본 struct를 embed하고 큰 profile만 참조화하므로 κ 입력·options·costs 등이 여전히 있다. summary는 그 struct를 재사용하지 않고 명시적 whitelist DTO로 만든다. `schedulerCompactQuery`의 view 파싱을 `scheduler.go:Handler.scheduler`와 함께 확장하되 summary는 `/state`에서만 허용하고 기존 full/compact 의미는 유지한다. `kube/scheduler.go:SchedulerController.State`에서 얻은 같은 상태 한 벌을 투영하며 전체 상태 JSON을 만든 뒤 지우지 않는다. 프로파일 본문·κ 표·후보 목록·과거 results·raw 증거는 제외한다.
- **구현 식별:** `httpapi/scheduler_summary.go`는 독립 whitelist DTO를 사용한다. `decision.id`는 기존 판단 시각의 UTC RFC3339Nano 표현이며 새 판단 ID를 만들지 않는다. `instances[].routable`은 workload UID별 서버 readback map이다. 노드는 `ready`, `measurement`, `reasons`를 그대로 표시하고 별도 가용성을 추정하지 않는다. BFF는 summary를 `/state`에서만 전달하고 기존 기본 compact를 유지한다.
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

## 15. U5: 직접 native 진행 기록 결손 진단 (`SCH-31`, `SCH-42`)

명세의 관측 완전성과 `00_measurement.md`의 native 누락=Insufficient 규칙을 그대로 쓴다. 새 합격 조건·타임스탬프 추정·진행 재시도는 추가하지 않는다. `ProgressLoss27`·`ContinuityReplay27` 조사에서 D05(batch 92866)의 running, D10(batch 63056)의 queued 직접 stamp가 보존된 requests archive에도 없었다. 닫힌 요청은 supplement로 기록하고 finished 선도착도 다른 phase를 버리지 않으므로, 현재 근거로 생성·stdout/CRI 전달·follow/parse·지속화 중 한 단계를 확정할 수 없다.

### 15.1 실패 진단

- `internal/firstfloor/scheduler.go:emitProgress`는 marshal 오류, 4096-byte 한도 초과, stdout 쓰기 오류마다 `requestID`, `phase`, 오류(길이 초과는 실제 bytes/limit)를 남긴다. 정상 progress를 별도 로그로 복제하지 않는다.
- `ParseProgress`는 거절하더라도 decode된 식별자를 진단용으로 반환한다. `Observer.scanLogs`는 parse 및 `ObserveBackend`의 runtime/context·late-supplement 실패를 기존 `native source issue`에 `requestID`, `phase`, 정확한 `physicalID=PodUID/containerID`와 함께 기록한다. 오류가 있는 반환값은 runtime으로 전달하지 않는다. 깨진 JSON 등 decode가 불가능한 경우 requestID/phase는 빈 문자열로 드러낸다.
- native 실행기 자체에는 Pod UID/containerID 입력이 없으므로 물리 식별자를 hostname 등으로 꾸미지 않는다. native 오류는 아래 exact Pod/container 보존 manifest와 join하고, observer 오류에는 실물 식별자를 직접 남긴다. 현재 issue는 다음 정상 행에서 지워지므로 `/pools`의 빈 issue가 과거 오류 부재를 뜻하지 않는다. 반드시 서버 원문 로그를 보존한다.
- 배포 경계: observer 진단은 ChillServer 재빌드 대상이다. emitter 진단은 firstfloor 실행 파일에 포함되므로 새 runtime 이미지가 필요하며, **현재 runtime 7 digest 동결을 깨서 배포하지 않는다**. 기존 runtime으로도 아래 read-only 원본 보존은 가능하다. 이 변경만으로 기존 D05/D10 소실 원인 확정이나 3회 무재발을 주장하지 않는다.

### 15.2 원본 보존과 판정

`occupancy.go`는 load Job의 Complete/Failed를 본 즉시 `deleteLoadJobs`를 호출한 뒤 drain·관측 집계·결과 보존으로 간다. 따라서 CR의 최종 Complete/Insufficient나 `pods-return.json`을 기다렸다가 `kubectl logs`를 시작하면 늦다. native Pod finalizer는 종료 증거용이지 stdout archive 보장용이 아니다.

`hack/measure/capture-progress.py`를 **운영 담당이 다음 측정 CR 생성 전에** 시작한다. 기존 측정 조건·TTL·finalizer·정리 순서는 바꾸지 않는다. 시작부터 해당 Node의 firstfloor stdout을 `kubectl logs --timestamps --follow`로 외부 결과 디렉터리에 흘리고, exact CR UID의 hashed load Job/Pod도 보존한다. Job 종료 직후에는 해당 실물의 timestamp 포함 전체 로그 snapshot을 추가로 시도한다. 이미 빠르게 삭제됐다면 사전 follow를 보존하고 snapshot 실패/identity 불확실을 명시하며 완전 보존으로 표시하지 않는다. 실제 명령·산출물은 스크립트의 `--help`와 manifest를 따른다.

```sh
python3 hack/measure/capture-progress.py \
  --node genesis3 --measurement genesis3-next-occ-plc-cpu \
  --namespace chill-system --output-dir /tmp/genesis3-next-progress \
  --capture-load --timeout 1800
```

`READY` 출력 뒤 운영 담당이 같은 이름의 CR을 생성한다. 기본 kubectl context/`KUBECONFIG`를 사용하며 필요한 경우 `--kubectl 'kubectl --context CONTEXT --kubeconfig /path/to/config'`로 명시한다. 산출물은 새 외부 디렉터리의 `manifest.json`과 실물별 `follow.log`, `follow.stderr`, 별도 `snapshot.log`다. follow/snapshot은 겹치는 원문이므로 서로 이어 붙여 사건을 이중 계산하지 않는다. 서버 로그는 기존 캠페인 수집 경로로 함께 보존한다.

READY 전에 최초 Pod 조회와 알려진 컨테이너의 follow 시작·현재 프로세스 생존을 확인한다. follow가 생성되지 않았거나 이미 종료됐으면 실패로 끝내며 READY를 내보내지 않는다. READY 뒤 연결 종료 가능성까지 보장하는 것은 아니므로 stderr/종료 코드와 보존 범위 확인은 그대로 필요하다.

1. Pod JSON(UID, containerID, imageID, restartCount, Node), follow 원문/오류, 종료 직후 snapshot, 측정 CR·load Job 신원과 서버 로그를 같은 외부 run 디렉터리에 보존한다. 원문은 민감 운영 자료로 git/public index에 넣지 않는다.
2. 누락 requestID/phase를 exact container 원문과 요청 archive에서 대조한다. 원문에 완전한 행이 있으면 emission 이후 수집·거절·지속화 구간으로 좁힌다. malformed/partial 행 또는 `write native progress`는 출력/framing 경계의 증거다. 서버 parse/context/supplement 오류와 동일 ID·phase·physicalID로 대조한다.
3. 원문도 없으면 먼저 회전·삭제·follow 실패·보존 시작/끝 공백을 확인한다. `--previous`는 삭제된 Pod나 모든 회전 로그 복구가 아니다. 필요하면 운영 담당이 실제 kubelet/edged 설정과 해당 Node의 CRI 원문/회전 파일을 읽기 전용으로 회수한다. 파일 부재만으로 미출력을 단정하지 않는다.
4. direct stamp가 확보된 경우만 원래 physical identity로 재생·재판정한다. queued/running을 legacy offset이나 QueueMS로 합성하지 않는다. 회수 불가면 Insufficient 유지·재측정이며 runtime의 닫힘/finished 처리나 occupancy 판정을 완화하지 않는다.

검사: emitter 세 실패의 식별자 보존, parser의 거절 시 진단 식별자, observer issue의 세 키를 회귀 검사한다. 보존 스크립트의 offline self-check는 cluster를 변경하지 않는다. 실제 다음 측정의 보존·원인 확정·3회 무재발은 별도 운영 근거다.

## 16. U6: 부하 발행 공백과 trace 충실도

실험 도구 변경이므로 별도 spec·SRS를 만들지 않는다. 기존 `SCH-31`의 모집단/원시 재계산과 `SCH-45`의 불변 실험 요약에 연결한다. 발행 충실도는 SLO 결과의 분모를 바꾸거나 미발행을 성공으로 만드는 조건이 아니다.

### 16.1 보존 원시의 관측 사실

`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`. batch1-plc-3의 `raw.json.loadJob.name`을 `O/campaign/v2/loadpods/<Job>-<Pod suffix>.jsonl`과 연결했다. 원시는 run 하위 `loadpods`가 아니라 이 공통 디렉터리에 있다. 아래는 완료 8개의 **전체 421.010초 생성기 로그(워밍업·tail 포함)**이며 360초 물리 측정 창만의 수치가 아니다. closed-loop `scheduledOffsetNs`를 정렬한 인접 차이를 사용했다. 이는 submit 직전 표식이지 socket write 시각이나 stdout flush 시각이 아니다. 원시 표의 분위수는 정렬 후 zero-based `floor(N×p)`이고 E5의 기존 `stats`는 nearest-rank를 쓴다.

| 대상 | 요청 행 | >50ms 공백 | 전체 gap 중 % | p50 / p95 / p99 (ms) | 최대 (ms) |
|---|---:|---:|---:|---|---:|
| D05 genesis2 CUDA | 197,757 | 99 | 0.0501 | 1.421 / 4.714 / 23.652 | 772.353 |
| D11 genesis3 CPU | 99,368 | 1,130 | 1.1372 | 1.444 / 7.957 / 50.773 | 525.441 |
| D10 genesis3 CUDA | 120,094 | 141 | 0.1174 | 2.294 / 8.653 / 31.156 | 450.794 |
| D15 lattepanda CPU | 147,933 | 67 | 0.0453 | 2.271 / 6.018 / 15.889 | 959.400 |
| D18 rasp5 CPU | 123,642 | 74 | 0.0599 | 2.251 / 5.474 / 35.628 | 873.821 |
| D21 orangepi CPU | 53,912 | 103 | 0.1911 | 5.586 / 32.679 / 39.309 | 313.049 |
| D26 jetsono CPU | 137,308 | 1,839 | 1.3393 | 1.443 / 5.008 / 51.443 | 657.648 |
| D25 jetsono CUDA | 81,429 | 88 | 0.1081 | 5.095 / 5.806 / 8.221 | 551.913 |

총 **961,435 gap 중 3,541개**가 50ms를 넘었다. `scheduledOffsetNs+latencyMs`로 추정한 응답 완료 간격도 비슷하게 길다(정확한 완료 시각은 아님). D11 최대 공백은 offset `388431281812→388956723072ns`, D26은 `146217337659→146874985548ns`이고, 각각 공백 시작에 약64개 미완료·공백 중 추정 완료1개다. **관측된 client 완료 경계에서 응답이 부족한 closed-loop 공급**이며 이를 생산자 lock 원인 확정으로 해석하지 않는다.

- GC/CPU: `O/campaign/v2/{gctrace,cgmon}.log`. 물리 window.start−60초는 정확한 생산자 UTC 시작이 아니므로 아래 시각 대응은 근사다. D11 최대 공백 부근 GC clock `0.085+4.6+0.003`, `0.12+4.2+0.016ms`, gateway CPU1.49→0.29core·throttle0; D26은 `0.17+18+0.014`, `0.10+4.9+0.031`, `0.079+3.3+0.002ms`, CPU1.04→0.49core·throttle0였다. 525–658ms STW나 gateway cgroup throttle을 보여주지 않는다. GC 가운데 concurrent clock 항을 STW로 합치지 않으며 다른 gateway 지연을 배제하지 않는다.
- 같은 구간 gateway 확인: S3 `serving/observations.v1alpha5/20261026T19/requests.jsonl.gz`를 read-only로 `.../d-20261027/raw-19/`에 보존했다(143,588,423bytes, SHA256 `97a16157e0e705ff48adfcf01eed4a9e37b9c0298aea1ebaadf03d3e38739846`, 원격쓰기0). D11 **99,368개 전부 exact requestID로 final과 연결**했다. 단일 지표 `exchangeNanos−dispatchedNanos−clientSpanNanos`(assignment 기록 대기·직렬화/실행 스케줄/응답 decode 잔여, 순수 mutex 시간이 아님)의 전체 p50/p95/p99/max는 **0.198/0.633/0.932/445.510ms**, >50ms는164개다. 하지만 최대 공백 시 미완료64개 코호트의 p50/p95/max는 **0.239/0.512/0.588ms**, >50ms는0이다. 코호트 선택의 완료 시각만 위 client 추정값이며 잔여는 서버 원시 단조 시각이다.
- 결론: 이 최대 공백을 ObservationLog assignment writer 체류 급증으로 귀속할 근거는 없다. native HTTP span·전달·native 실행·client 스케줄/응답 처리 중 정확한 원인은 **미확정**이다([INFERENCE]). loader CPU/throttle·lock/flush 시간과 exact UTC/monotonic anchor가 없어 원인 단계를 더 좁힐 수 없다. 과거 pprof의 writer 경합은 다른 창의 후보 근거이지 이 공백의 인과 증거가 아니다.
- 재현/상세: `.../d-20261027/u6-issuance-gaps.json`, `raw-19/{receipt.json,u6-gateway-residual.json,analyze-u6.py}`. 원문·요청 ID·인프라 경로는 외부 private 근거이며 공개 요약에 복제하지 않는다.

### 16.2 최소 구현과 E5 결과

- CLI `replay`와 주입 E5 producer 모두 **JSON 직렬화만 stdout 공유 lock 밖**으로 옮긴다. 완전한 한 줄의 write+flush는 기존 lock으로 보호해 interleaving을 막는다. worker는 여전히 flush 뒤에 다음 요청을 보낸다. 이 변경은 알려진 불필요 critical-section 연산을 줄이는 것이지 위 공백의 원인 확정·해결 증명이 아니다. writer thread/queue와 실패·drain 수명주기는 이 증거로 추가하지 않는다.
- 일반 CLI 행에 `sentMonotonicNS`, `finishedMonotonicNS`를 기록한다. 각각 `infer` 호출 직전과 응답/transport 오류 처리 직후이며 같은 프로세스의 단조 시각이다. stdout 시간·패킷 발행·gateway 수신 시각이라고 부르지 않는다.
- E5 `issuanceFidelity`는 실제 producer start와 send 시각을 써 worker 완료/로그 순서가 아닌 실제 발행 순서로 정렬한다. 시작·끝 경계 포함 gap의 p50/p95/p99/max, **>50ms 개수·비율·구간(offset와 안의 planned arrival 수)**, 같은 frozen trace의 planned gap 분포, 실제 send lateness 분포·>50ms 요청 수를 `judgment.json`, `e5-summary.json`과 출판 KPI에 넣는다. Poisson의 자연 공백·0부하 구간은 자동 정지/실패로 판정하지 않는다. 미발행/시간 결손을 0으로 채우지 않는다(관측 timestamp 없음=`UNKNOWN`/NotReady, offered0=`NOT_OFFERED`). 기존 10ms `delayed` 표식과 50ms 보고 기준은 별개다.
- `demo.py run`을 사용하는 E5-0~7도 같은 계산을 쓴다. 환경 설정 없이 재판정하는 경로를 유지하기 위해 기존 함수를 `d2/analyze_before.py`로 옮기고 `e5.py judge`와 `demo.py summarize`가 함께 호출한다. demo의 단계/전체 `summary.json`, `summary.md` 한 줄과 publish의 workload KPI에도 그대로 보존한다. 결손은 `UNKNOWN`과 사유로 전달하며 관측하지 못한 공백을 0으로 채우지 않는다.
- 다음 측정은 §15의 timestamp 포함 load Pod 로그도 함께 보존한다. 다음 E5는 기존 producer start/monotonic anchor·client/server/edgemesh cgroup과 서버 GC 기록을 함께 보존한다. 바뀐 CLI를 쓰려면 **load-generator 이미지 재빌드·새 digest/receipt가 필요**하다. 주입 producer 코드만의 변경은 주입 파일로 전달되지만 CLI 변경을 새 이미지 없이 적용됐다고 주장하지 않는다. **U6 때문에 runtime 7 재빌드는 필요 없다.**

### 16.3 별도 writer 개선의 설계 경계(미구현)

현재 같은 구간 지표가 최대 공백의 writer 체류 가설을 지지하지 않으므로 U6에서 `observation_log.go`를 바꾸지 않는다. Main이 별도 담당에게 개선을 배정할 경우 기존 immutable request snapshot을 재사용해 JSON marshal만 writer mutex 밖으로 옮긴다. 새 비동기 장부·generic queue는 필요 없다.

1. requestMu 아래 snapshot/closing 표식을 얻고 해제한 뒤 marshal한다. writer mutex 아래에서 closed·현재 row/참조를 다시 확인하고 catalog reference→request row를 같은 commit-time hour에 append한다. JSON 완료 순서가 아니라 기존 writer mutex의 append 순서로 직렬화하며 assignment append 성공 전 native 시작을 허용하지 않는다.
2. final append 실패는 열린 closing row를 유지해 Flush가 재시도한다. 그 사이 같은 request에 phase가 접혔다면 현재 pointer 비교를 유지해 더 최신 row를 삭제하지 않는다. 동일 final의 Runtime/Flush 경쟁·close·hour 전환과 catalog-first 계약을 함께 보존해야 한다.
3. Flush는 이미 writer mutex를 잡고 `closeRowLocked`를 부르므로 helper 안에서 잠깐 unlock만 하는 수정으로는 충분하지 않다. 이 재시도 경로와 모든 assigned/final/native caller를 함께 이행하고, 기존 diagnostic marshal mark를 lock 밖 span에 맞춘다. 약50줄을 넘는 cutover나 순서 보장 변경은 별도 설계·회귀 검사로 다루며 이번 작은 생산자 수정에 섞지 않는다.

## 17. E5 증거 API (2026-10-30)

[계획 §1~6](../../plans/2026-10-28-e5-evidence-api.md)의 구현 계약이다. 원본 요구는 [Serving 관측](../../spec/00_measurement.md#측정의-단위), [scheduler SRS](../../spec/scheduler/SRS.md)의 SCH-67~70 / SCH-AC-52~55다. 코드 이행과 실제 배포·실험 수용은 구분하며 구현 커밋은 17.7에 남긴다. 기존 `ObservationLog`, `ObservationArchive`/Reader/Lister, 로컬 우선 시간 선택, `append`/archive/health, TokenReview/SAR, live hub를 재사용한다. 별도 저장소·TSDB·broker·다중 버전 reader·router 파라미터를 만들지 않는다.

### 17.1 범위와 수집 경계

하네스가 아는 CHILL 내부 사실은 CHILL API에서만 얻는다. CHILL 대상 sudo/crictl, `/proc` 직접 읽기, SSH cgroup 수집, S3 다운로드, kubectl readback, observer 로그 복사를 cutover 때 제거한다. `raw-source-index.json`은 판정 입력으로 유지하며 API 수집기가 만든다. 원시 전송·과거 조회는 측정 대상 서버의 CPU/I/O를 쓰므로 **E5 측정 창 밖**에서 실행한다. 라이브 보기만 창 안에서 허용한다(운영 규칙, 앱의 새 강제 관문은 없음).

| 사실 | 단일 출처와 제외 경계 |
|---|---|
| 요청·판단·정의·관리 원시 | 아래 raw API. 대시보드용 `/observations/workloads`, `/scheduler` 가공 조회는 유지 |
| CHILL 컨테이너 CPU/throttle/메모리·thermal | `resources`. `client` 컨테이너 표본·시작 관문·생산자 해제는 외부 장비라 유지 |
| GC·관측 디렉터리 바이트 | 서버 in-process `resources`. gctrace 강제 요구·pre/post PVC host 수집 제거 |
| Pod 신원/상태·제어 image digest·observer 오류 | `cluster`. E5-7 사후 readback은 `scheduler`와 `cluster`; 운영자 파일에는 장애 주입 명령만 남김 |
| Node 참조·POOL Node 목록 | 기존 state/scheduler의 Node 이름·UID. 합성 NodeList나 새 Node/cordon stream 없음 |
| 전력계↔Node 선언 | settings `meters`; 전력 정책은 기존 `powerObservation`. 실제 전력은 기존 edge-metrics API 유지 |
| 수집 영수증 | capture-identity, s3-hour-list-*, upload.log, collector-status, *-collector.stderr와 이를 읽는 판정 코드 삭제 |
| 운영 안전 | `control-alerts` STOP 루프, 생성기 품질 관문, Job 조작과 E5-7 운영자 kubectl 명령은 그대로 유지 |

판정 수식과 기존 publication은 바꾸지 않는다. GC는 사이클 분포가 아니라 창 안 누적 차, E5-7 recovery는 운영자 readback이 아니라 scheduler/cluster 출처임을 새 보고서에 명시한다. 다음 새 publication의 `measurementRules`에 `evidenceSource=api`, `gcMeaning=runtime-metrics-delta`를 기록한다. #213~#225를 수정·재게시하거나 비교 가능성 판정 관문을 추가하지 않는다.

### 17.2 원시 내보내기 (`SCH-67`)

`hour`는 UTC의 안전한 단일 경로 요소 `YYYYMMDDTHH`다. 누락·중복 hour, 잘못된 시각, 허용되지 않은 kind, 추가 경로/쿼리·경로 이탈은 400으로 거절한다. 파일명·store/key를 요청 입력으로 받지 않는다. kind는 `requests`, `scheduler`, `catalog`, `management`, `resources`, `cluster` 여섯 개뿐이다.

| 요청 | 응답 계약 |
|---|---|
| `GET /api/v1/observations/raw?hour=<hour>` | JSON `{hour,at,status,kinds:[{kind,available,segments:[{minute,bytes}],archive:null 또는 {store,key,bytes}}]}`. `at`은 목록을 잡은 서버 시각, `minute`는 `00`~`59`. 로컬 segments는 닫힌 분만, 분/종류 오름차순이다. archive 출처면 segments는 빈 배열이며 저장된 gzip 객체 위치·크기를 싣는다. 토큰·호스트 경로·자격증명 URL은 노출하지 않는다 |
| `GET /api/v1/observations/raw/{hour}/{kind}` | 로컬 시간은 요청 시작 시 확정한 닫힌 분 파일을 분 순으로 이어 원래 JSONL 바이트를 전송(`application/x-ndjson`). 로컬에 없는 과거 시간은 해당 stream의 보관된 gzip 바이트를 그대로 전송(`application/gzip`). gzip에는 `Content-Encoding: gzip`을 붙이지 않아 HTTP client가 자동 해제하지 않게 한다. 둘 다 `Cache-Control: no-store`; 즉석 gzip/해제·JSON decode/re-encode·필터·집계 없음 |
| `GET /api/v1/observations/raw/definitions` | 루트 `v1alpha5/definitions.jsonl`을 요청 시작 시 확정한 마지막 개행까지 원래 JSONL로 전송. 마지막 미완결 행 제외. 시간별 catalog의 대체물이 아니며 빈 완결 기록은 길이 0 |

시간 상태는 다음처럼 source와 진행을 구분한다. `open`은 현재 UTC 시간으로 닫힌 분이 일부 있어도 open이다. `closed-local`은 지난 시간이고 로컬 디렉터리가 남은 상태다. `archived`는 로컬이 없고 기존 prefix에 보관본이 있는 시간이다. 업로드됐어도 로컬이 남으면 로컬이 우선이다. 기본 보존 `2h`와 archive 성공 전 삭제 금지·미완료 assigned pin을 그대로 지키므로 경과 2시간만으로 archive 사용을 강제하지 않는다.

- 로컬 선택은 기존 시간 디렉터리 단위 우선순위다. 파일이 없는 stream만 archive에서 보충하지 않는다. 같은 시간을 두 소스에서 합치지 않는다.
- `mu` 아래에서 시간·현재 분 경계·선택한 fd·길이를 잡고 잠금 밖에서 한정된 길이만 복사한다. 원본 fd가 열렸으면 archive 삭제와 충돌하지 않는다. 현재 열린 분은 파일 크기나 개행 유무와 관계없이 제외한다. 목록과 다운로드는 각각 독립 snapshot이며 목록 시각을 고정하는 서버 영속 receipt/manifest나 재시도 세션은 만들지 않는다.
- `definitions`도 mutex 아래에서 fd와 마지막 완결 행 끝을 확보하고 이후 append를 전송에 섞지 않는다. 기존 시작 시 torn-tail 복구와 append rollback을 유지한다.
- 시간 자체가 양쪽에 없거나 선택한 stream이 없으면 404(`ObservationUnavailable`), 현재 시간에 아직 내보낼 닫힌 분이 없으면 409(`ObservationNotReady`)다. 목록의 kind별 `available=false`는 부재이지 0건의 관측을 뜻하지 않는다. 열린 시간의 빈 segments도 수집 완료가 아니다.
- 새 stream은 [ADR 0030](../../adr/0030-observation-evidence-api-streams.md)대로 같은 prefix에 추가한다. 배포 이전 resources/cluster 부재는 결손이 아니며 #213의 기존 네 stream 조회를 막지 않는다. 적용 이후 필요한 stream의 부재는 소비자가 수집 적용 구간·실제 표본 시각으로 결손을 드러낸다. 새 stream 여섯 개의 존재를 모든 시간의 필수 조건으로 삼지 않는다.
- archive List/Open 오류는 5xx, 처리 마감은 기존 timeout 오류다. 읽기 오류를 빈 200/404로 바꾸지 않는다. 전송 시작 뒤 실패는 응답을 중단하며 JSON 오류를 JSONL/gzip 뒤에 덧붙이지 않는다. 알려진 길이는 Content-Length로 제공해 조기 종료가 드러나게 한다. 하네스는 HTTP 완료·JSONL 개행/구조·gzip 종료 오류를 검사하고 불완전 수집을 성공으로 판정하지 않는다. digest/바이트 대조 관문은 만들지 않는다.

**마감:** `httpapi.Handler.ServeHTTP`의 `longOperation` 선택을 확장하여 raw 목록/전송/definitions 및 가공 `GET /api/v1/observations/scheduler`에 기존 `OperationTimeout`을 처리 상한으로 적용한다. 별도 무제한 timeout이나 원시 전용 서버를 만들지 않는다. Request context의 처리 마감·취소는 로컬 복사와 archive List/Open/read까지 전달한다. 쓰기 마감은 기존 방식 `처리 deadline + RequestTimeout`으로 별도 설정한다. 추가 쓰기 시간은 오류 전달 여유이며 처리/read context를 연장하지 않는다. archive 네트워크와 대용량 복사는 ObservationLog mutex 밖이다. 기존 가공 조회의 catalog 해석·페이지·coverage는 유지한다.

**권한:** 기존 `Handler.authorize` → `kube.Authorizer`의 Bearer TokenReview/SAR를 통과한다. SAR path는 기존처럼 `/chill`을 실제 요청 경로 앞에 붙인다. 따라서 하네스 전용 ClusterRole의 `nonResourceURLs`에는 **`/chill/api/v1/observations/raw`와 `/chill/api/v1/observations/raw/*` 둘 다**, verb `get`을 넣는다. 아래표의 `/raw`, `/raw/*`는 이 전체 경로의 축약이다. 루트 목록은 wildcard 하나로 보호/허용되지 않는다. Dashboard/BFF·일반 serving-manager·공개 앞단의 allowlist에는 raw를 추가하지 않으며 토큰을 공유하지 않는다. 앞단 비노출과 별개로 서버 직접 호출도 SAR로 거절한다.

### 17.3 자원 기록 (`SCH-68`)

**수집 위치:** 기존 기본 discovery node-agent에 채집을 붙인다. actuation DaemonSet은 쓰지 않는다. 모든 대상 Node(genesis1 포함)에 기본 agent가 있어야 하고 현재 exclude/selector가 서버 Node를 빼지 않는지 배포 때 확인한다. 서버 컨테이너도 genesis1 agent가 밖에서 잰다. 서버가 자기 cgroup을 읽으면 GC/잠금 정지 때 자기 측정도 멈추므로 그 경로는 없다.

**Pod 권한 없는 대상 발견:** 서버는 기존 Kubernetes 읽기 권한으로 CHILL 서빙 인스턴스와 제어 Pod의 현재 Node·Pod UID·containerID를 고른다. 범위는 해당 ChillSystem 소유 서빙 Pod와 릴리스의 CHILL 제어 Pod이며 load-generator/client·다른 tenant 컨테이너는 제외한다. node-agent에는 Pod get/list/watch를 추가하지 않는다. 내부 POST 응답의 `targets`가 해당 Node의 정확한 `{podUID,containerID}` 집합을 다음 묶음에 공급한다. 첫 POST는 `samples:[]`로 대상만 얻으며 이 handshake 자체는 resources 행을 만들지 않는다. 새 Pod를 알기 전 구간은 소급 수집하지 않는다.

agent는 호스트 `/proc/<pid>/cgroup`, mount 정보와 읽기 전용 `/sys`를 사용해 받은 identity에 해당하는 실제 cgroup과 v1 controller/v2 mount를 찾는다. systemd/cgroupfs 경로 문자열을 정해 놓고 맞추지 않는다. 경로가 확인된 실물만 읽으며 같은 Pod 이름/slot을 containerID 대신 쓰지 않는다. Pod 종료·재시작·image 판정은 하지 않는다(cluster 소유).

**전송 경계:** `POST /api/v1/internal/observations/resources` 한 개, 요청 `{node:{name,uid},samples:[{sampledAt,containers:[...],thermal:[...]}]}`, 응답 `{targets:[{podUID,containerID}]}`다. 서버는 요청 크기를 기존 MaxRequestBytes로 제한하고 Node identity·배열 크기·시각·counter/온도 형식과 target membership을 경계에서 검증한다. 컨테이너 교체 중 이미 채집한 표본은 서버가 그 agent에 직전 응답으로 공급한 identity에 속하면 허용한다. 새 UID를 옛 UID로 바꿔 연결하지 않는다. 응답 후 다음 묶음은 새 targets를 사용한다. 서버 재시작으로 이전 targets를 확인할 수 없으면 묶음을 거절하고 agent가 다음 빈 handshake로 재동기화한다. 과거 표본 재전송은 하지 않는다.

- TLS 서버 신뢰 CA·고정 내부 URL·projected SA token 파일을 기본 agent 설정으로 전달한다. 토큰 rotation을 읽고 검증을 끄지 않는다. 기존 TokenReview/SAR를 재사용하고 agent SA에 정확한 `/chill/api/v1/internal/observations/resources`의 `post`만 추가한다. raw GET·settings PUT·Pod 권한은 주지 않는다. HTTP 헤더나 본문의 username을 신뢰하지 않는다. 공유 DaemonSet SA는 Node별 암호학적 신원을 증명하지 않으므로 본문 Node를 TokenReview가 보증한 것으로 표시하지 않는다.
- 표본 주기 기본 `1s`, 묶음 주기 기본 `10s`를 수집 설정으로 제공하고 실행 조건에 고정한다. Node당 한 POST/한 resources 행에 묶음 표본을 넣는다. 주기마다 새 묶음을 보내며 요청은 유한 timeout을 갖는다. **실패 묶음은 버리고 재전송·spool·backoff queue를 만들지 않는다.** 실패 후 다음 주기는 새 표본/handshake만 보낸다.
- 비어 있지 않은 묶음은 서버 append 성공일 때만 200과 다음 targets를 보낸다. 빈 handshake는 append 없이 targets를 반환한다. append 실패는 기존 log health와 오류 응답으로 드러낸다. 응답만 유실되어도 같은 묶음을 다시 보내지 않는다. 수신 순서로 기록하며 Node 표본 시각으로 저장 분을 역선택하지 않는다.

**저장 행:** 여섯 stream은 동일 ObservationLog writer를 쓰며 다음 새 resources 행은 `<write-hour>/resources.<write-minute>.jsonl`에 들어간다. Node 묶음은 `kind:"node"` 한 행이고 서버 누적값은 별도 `kind:"server"`다.

| 행/필드 | 의미·단위 |
|---|---|
| node: `node`, `receivedAt`, `samples[]` | Node 이름/UID, 서버 수신 UTC 시각, 원래 묶음. 각 `sampledAt`은 Node 시계 RFC3339Nano. 두 시각을 남기되 시계 보정 없음 |
| samples의 `containers[]`: `podUID`, `containerID`, `cgroupVersion` | 샘플 대상 실물 identity. containerID는 runtime 접두어를 포함한 원래 식별자, cgroupVersion은 1 또는 2 |
| `cpuUsageNanos`, `throttledNanos`, `periods`, `throttledPeriods` | cgroup 누적 counter. v1/v2 원 단위를 각각 ns/횟수로 명시 변환하며 CPU core 사용률로 미리 집계하지 않음 |
| `memoryCurrentBytes`, `memoryPeakBytes` | cgroup 현재/최고 사용 bytes. 지원하지 않거나 읽지 못한 값은 필드 부재와 해당 `errors` 사유; peak를 current로 대체하지 않음 |
| `thermal[]:{sensor,milliCelsius}`, `errors[]` | host sensor의 실제 식별(thermal zone 이름/경로와 type), 원래 온도 단위를 milli°C로 맞춤. 센서 없음/읽기 실패를 0°C로 만들지 않음. 부분 채집 실패는 대상·필드·이유로 남김 |
| server: `at`, `podUID`, `processStartedAt`, `gcCPUSeconds`, `gcCycles` | 서버 시각·Pod UID·프로세스 시작 시각, `/cpu/classes/gc/total:cpu-seconds`와 `/gc/cycles/total:gc-cycles` 누적값. 재시작을 가로질러 빼지 않음 |
| server: `observationDirectoryBytes`, `directoryObservedAt` | 분 1회 관측 디렉터리 전체 regular file 논리 크기 합(bytes, 루트 definitions 포함). PVC capacity/allocated blocks/압축 archive 크기와 다르며 `/tmp` scratch는 제외. 측정한 tick에만 싣고 매초 복제하지 않음. stat 실패는 값 대신 이유 |

서버 누적값은 기존 live/runtime **1초 ticker**에 붙이고 다른 1초 loop를 만들지 않는다. 분 단위 디렉터리 순회는 writer mutex 밖에서 수행하며 동시 append/삭제가 있는 시점 관측이라는 의미를 유지한다. `processStartedAt`과 Pod UID로 창 안 누적 차를 구하고 gcCycles 감소를 임의 0으로 보정하지 않는다. 원 gctrace 로그와 지표 이름/의미가 같다고 주장하지 않는다.

하네스 수집 가능 시점은 drain 후 **측정 창 끝 + 묶음 주기 + 다음 분 경계** 이후다. resources 행은 서버 수신 시각의 분에 쓰이므로 이 여유와 늦게 받은 묶음의 실제 수신 시각을 함께 고려해 원시를 받는다. 관측 시계 차이는 두 시각으로 사후 확인하며 새 동기화 프로토콜은 없다.

### 17.4 클러스터 기록과 라이브 callback (`SCH-69`)

기존 server-side Pod LIST/WATCH와 observer issue 변경 지점을 사용한다. 서빙 Pod뿐 아니라 CHILL 제어 Pod도 포함하고 terminal Pod를 제외하는 기존 서빙 조회만으로 전체 기록을 대신하지 않는다. 초기 LIST는 `observed` baseline이며 생성 사건을 보았다고 꾸미지 않는다. 이후 add/change/delete와 상태 변화를 기록한다. 읽기 오류·watch 단절을 Pod 삭제로 합성하지 않는다. server 시작에는 Downward API로 받은 자기 Pod UID를 `server-start` 한 행으로 먼저 남긴다.

| cluster 행 | 필수 내용 |
|---|---|
| `{kind:"pod",at,event,pod:{...}}` | `event`는 `observed`, `created`, `changed`, `deleted`, `server-start` 중 하나. Pod namespace/name/UID, Node 참조, creationTimestamp·deletionTimestamp, phase·Ready 상태, init/일반 container별 name·containerID·imageID·restartCount·현재/직전 terminated reason/exitCode/시각. 아직 없는 필드는 미확정으로 남김. image tag를 imageID/digest로 대신하지 않음 |
| `{kind:"issue",at,event,source,reason,...}` | `event`는 `raised`, `changed`, `resolved` 중 하나. 기존 ObserverIssue의 정확한 source identity(Pod UID/containerID를 포함한 source면 유지), 이유, 감지 서버 시각. changed는 이전 이유도, resolved는 해소한 이전 이유도 남김 |

issue의 동일 source/이유 반복은 발생 시각을 매 refresh 갱신하며 새 사건으로 쌓지 않는다. 없던 issue→raised, 이유 변경→changed, 실제 성공 확인→resolved다. source 읽기에 실패한 상태를 임의 해소하지 않는다. 기록 hook 자체의 오류를 다시 issue hook으로 넣는 재귀 경로도 만들지 않는다.

**공유 인터페이스:** core에 `ClusterObservation` DTO와 `(*ObservationLog).ClusterRecord(ClusterObservation) error`를 둔다. `kube.ObserverConfig.OnCluster func(serving.ClusterObservation) error`로 Pod·issue 사실을 composition root에 넘긴다. 그 root의 `recordCluster`는 **ClusterRecord 성공 → livePublisher.cluster(같은 행)** 순서이며 기존 `schedulerService.recordDecision/recordAction`과 같다. core는 live DTO나 kube 타입을 import하지 않는다. callback에서 observer/controller를 다시 조회하지 않는다.

append 실패는 기존 ObservationLog health·호출 오류/로그로 노출하고 라이브를 발행하지 않는다. 관측의 실제 상태 메모와 마지막 기록된 projection을 구분하여 실패한 append를 기록 완료로 표시하지 않는다. 다음 정상 관측에서 확인된 상태를 기록할 수 있으나 별도 영속 outbox·재전송 큐·누락 사건 복구를 약속하지 않는다. 초기 baseline이 과거 이벤트 이력의 완전성을 보장하지도 않는다. hub 발행 실패 때문에 이미 저장된 행을 재append하지 않는다.

라이브 wire event 형태·화면은 [대시보드 계획 §3~4](../../plans/2026-10-26-dashboard-live-stage.md#3-s2-라이브-무대) 소유다. 이 슬라이스는 전달 지점과 DTO 투영만 연결한다. Node의 기존 scheduler 기록, live seq/resync/heartbeat·인증은 재사용하고 resources 원시 전체를 브라우저에 보내거나 raw 권한을 대시보드에 주지 않는다.

### 17.5 읽기 전용 settings `meters` (`SCH-70`)

`GET /api/v1/scheduler/settings`에 `meters:[]`를 추가한다. 값은 scheduler service가 metric-sources와 실제 Node 이름/UID로 **수락한 configuration snapshot**의 `[]MeterDeclaration`이다. 항목은 기존 `{id,node:{name,uid},scope,source}`를 그대로 투영하고 선언이 없으면 빈 배열이다. source는 수락한 전력 출처 선언(kind/nodeName/endpoint/metric/metricLabels)이지 토큰·비밀 설정이나 실시간 표본이 아니다. API가 ConfigMap의 원문을 별도로 읽어 두 번째 선언 원본을 만들지 않는다.

- `HandlerConfig.SchedulerMeters func() []servingkube.MeterDeclaration`를 root에서 accepted configuration 조회에 연결한다. settings GET에서만 읽기 projection에 붙이고 `SchedulerSettingsInput`, scheduler 저장 ConfigMap, settings mutation 본문에는 넣지 않는다. 기존 `powerObservation` 경계는 바꾸지 않는다.
- `meters`가 든 PUT은 기존 strict unknown-field 거절로 실패시킨다. 서버가 조용히 버리거나 쓰기 별칭으로 수락하지 않는다.
- 같은 구현 1에서 `hack/e5/e5.py:CPO.put`, `chill-dashboard/features/scheduler-settings.tsx:putSchedulerSettings`가 GET 결과에서 `meters`를 제거한 뒤 PUT하도록 고친다. 기존 staticRouting/staticRoutingValidatedAt 제거와 CPO의 powerObservation 처리도 보존한다. 대시보드 read type에는 읽기 전용 선언을 추가한다.
- 기존 `hack/e5/d2/conduct_before.py`는 과거 before 실행 기록 도구이며 신규 API 수집·설정 쓰기 경로로 재활성화하지 않는다. 클라이언트 두 곳을 고치지 않은 채 GET 확장만 배포하지 않는다.

### 17.6 병렬 구현 3개: 파일 영역과 인터페이스

이 표는 세 슬라이스의 **소유권 계약**이다. 신규 파일명은 구현 시 만들 위치이며 이 문서 단계에서 코드/패키지를 만들지 않는다. 두 생산자는 기존 core package 안의 별도 파일을 쓰므로 새 production package·허용 의존성 추가가 필요 없다. 공통 파일은 구현 1의 통합 소유자가 요청된 wiring만 적용한다. 구현 2/3은 해당 patch 지점을 전달하고 공통 파일을 동시에 편집하지 않는다. 구현 1→2→3의 직렬 기능 의존성이나 중간 dummy/stub은 없다.

| 슬라이스 | 독점 파일/패키지 영역 | 제공·소비 인터페이스 | 공통 파일 통합 요청 |
|---|---|---|---|
| **1. 원시 내보내기 + meters** | `internal/serving/observation_log_raw.go`(신규), `httpapi/observations_raw.go`(신규), 기존 `httpapi/scheduler.go`, `httpapi/operations.go`, `cmd/chill-server/scheduler_service.go`, `hack/e5/e5.py:CPO.put`, `chill-dashboard/features/scheduler-settings.tsx`·`lib/read-types.ts`, 각 인접 focused test | core `RawHour(ctx,hour) (RawHourIndex,error)`, `OpenRaw(ctx,hour,kind) (RawObservation,error)`, `OpenRawDefinitions(ctx) (RawObservation,error)`. RawObservation은 `{Reader io.ReadCloser,ContentType,Bytes}`이며 fd/객체 수명은 caller가 Close. 실제 export 함수는 ObservationLog method다. HandlerConfig.SchedulerMeters는 17.5 계약 | `observation_log.go` stream 상수/등록에 resources·cluster 두 개 추가. `httpapi/handler.go`에 raw route·공유 deadline 선택·callback config 등록. `server.go`/`live.go`에 다른 두 슬라이스의 생성/종료·ticker/callback wiring 통합 |
| **2. resources** | `internal/serving/observation_log_resources.go`(행 DTO·append, 신규), `httpapi/resources.go`(수신, 신규), `cmd/chill-server/resources.go`(대상 선정·process/디렉터리 관측, 신규), `cmd/node-agent/resources.go`(수집·전송, 신규), 기존 `cmd/node-agent/{discovery,options}.go`, `internal/nodeprobe`의 관련 host 조회 파일, `internal/operator/nodeagent`의 config/DaemonSet, `charts/chill/templates/{node_agent_config,node_agent_rbac}.yaml`, 필요한 values, 인접 test | `ResourcesRecord(ResourceObservation) error`는 ObservationLog method. HTTP 수신 DTO는 17.3의 request/reply. HandlerConfig `ReceiveResources func(context.Context,serving.ResourceBatch) ([]serving.ResourceTarget,error)`를 root에 연결: 현재/직전 발급 target 검증→서버 receivedAt 부여→비어 있지 않은 묶음 append→다음 targets 반환. 이 callback이 대상 발급 메모도 소유한다. collector는 typed ResourceObservation만 append하고 raw API를 호출하지 않음 | `handler.go`에 POST 등록/수신 callback, `server.go`에 ReceiveResources·프로세스 관측 연결, 기존 live/runtime ticker에 1초 GC·분별 디렉터리 hook. 공유 설정 option 추가가 필요하면 구현 1 통합 소유자에게 필드/인자를 넘김 |
| **3. cluster + live callback** | `internal/serving/observation_log_cluster.go`(행 DTO·append, 신규), `internal/serving/kube/observer_cluster.go`(Pod/issue hook, 신규), 기존 `kube/observer.go`의 ObserverConfig·issue 전이, `cmd/chill-server/cluster.go`(기록 후 projection, 신규), `internal/serving/live/types.go`의 대시보드 합의 event DTO, 인접 test | `ClusterRecord(ClusterObservation) error`, `ObserverConfig.OnCluster func(ClusterObservation) error`. `recordCluster`→append 성공→publisher.cluster 순서. target 선정은 resources가 소유하고 cluster의 저장 성공을 자원 채집의 전제로 삼지 않음 | `server.go`에서 observer callback·자기 Pod UID/시작 기록 연결, `live.go`에 cluster projection hook. Downward API UID 전달·제어 Pod 조회 범위의 배포 변경은 구현 1 통합 소유자에게 요청 |

모든 위 `observation_log_*.go` 경로는 `internal/serving/` 기준, `httpapi/`는 `internal/serving/httpapi/`, `kube/`는 `internal/serving/kube/` 기준이다. 리소스/클러스터 행 DTO는 각 생산자 슬라이스만 정의하며 구현 1은 stream 이름만 등록한다. 중복 schema 파일·generic event bus·단일 구현 interface를 만들지 않는다. 기존 잠금·append rollback·health를 이용하고 raw reader가 producer를 import하거나 기다리지 않는다.

권한/배포 공통 변경(`charts/chill/templates/serving_entrypoint_rbac.yaml`, serving Pod UID env, 라이브·S1 권한, 서버·대시보드·node-agent 이미지)은 통합 소유자 한 명이 맡는다. raw 권한은 하네스 전용, 수신 POST 권한은 기본 agent 전용이다. 일반 serving-manager의 `/observations/*`, `/api/v1/*` 같은 광범위 권한이 raw나 내부 수신 POST까지 열지 않도록 관련 grant를 가공/관리 경로별로 좁힌다. 실제 승인 배포 전에 RBAC를 확인하며 단순히 새 narrow role을 추가했다고 기존 wildcard 허용이 사라지는 것은 아니다.

### 17.7 이행·수용·미확인 경계

1. **문서:** spec → SCH-67~70 / SCH-AC-52~55 → 이 설계와 ADR 0030(`e699801c` 문서 계약).
2. **구현:** 위 3개를 병렬로 붙이고 통합 소유자가 shared wiring·stream 등록·권한 경계를 완성한다. `e947cd5e` 미사용 분리 코드를 정리한 단일 ChillServer 소스를 전제로 하며 gateway 재개는 하지 않는다.
3. **승인 배포 1회:** raw RBAC·node-agent 내부 POST 권한 및 [대시보드 계획 §4](../../plans/2026-10-26-dashboard-live-stage.md#4-증거-api와의-경계)의 라이브 RBAC·S1 권한·서버/대시보드 이미지 변경을 묶는다. node-agent 수집 설정·mount/보안·genesis1 포함과 edge→server TLS 도달도 이 단위에서 확인한다.
4. **하네스 cutover:** 17.1의 대체/유지 표대로 수집과 판정 입력을 한 번에 옮긴다. 중복 영수증 파일과 이를 읽는 판정 코드·옛 CHILL 우회 수집을 제거하되 client·STOP·운영자 장애 주입은 보존한다.
5. **전환 리허설 단 1회:** 같은 리허설에서 옛 수집과 새 수집을 함께 돌리고 #213 보관본 1시간도 API로 받아 판정한다. D1·지연·전력 J가 같아야 한다. resources는 식별자·단위·결손을 확인하고 GC 의미 변경은 별도 표시한다. ChillServer CPU에서 라이브 hub·node-agent 수신·raw 전송·대시보드 조회가 더하는 몫을 기록한다. 끝나면 옛 경로를 삭제하며 이 비교를 상시 검증 관문으로 남기지 않는다.

| 구현 시 남길 최소 검사 | 연결 수용 기준 |
|---|---|
| 닫힌 분만 export·정의 torn tail 제외·fd 확보 뒤 archive 삭제·기존 #213 prefix 조회·과거 두 stream 부재·404/409/store 오류/취소/긴 전송·root/wildcard RBAC 분리 | SCH-AC-52 |
| cgroup v1/v2 실제 mount/identity 탐색·counter 단위·미지원 값·Node 시계 차이·전송 실패 무재전송·Pod 권한 없음·container 교체·GC process reset·분별 directory bytes | SCH-AC-53 |
| resources 구현 커밋: `SCH-68` 자원 수집·수신·누적 기록(이 행을 추가한 커밋). 공통 등록·ticker·배포 설정은 구현 1 통합 커밋; 테스트 파일은 각 생산자 인접 `resources_test.go` | 코드 이행, 배포·환경 실측 수용과 구분 |
| 초기 baseline/자기 Pod UID·Pod/컨테이너 상태 변화·issue 발생/변경/해소·동일 반복 억제·append 실패 시 live 미발행·저장 성공 후 동일 callback | SCH-AC-54 |
| accepted meters projection·GET 후 CPO/대시보드 PUT에서 제외·직접 meters PUT 거절·기존 읽기 전용 필드 경계 유지 | SCH-AC-55 |
| 원시 내보내기·meters 구현 커밋: 이 행을 추가한 `feat(serving): expose raw evidence and accepted meters (SCH-67 SCH-70)` 커밋. `observation_log_raw.go`·`httpapi/observations_raw.go`, 원래 prefix/버전의 두 stream 등록, raw/scheduler 처리·쓰기 마감, accepted meters와 두 PUT client를 연결. focused 검사는 `observation_log_raw_test.go`·`httpapi/observations_raw_test.go`·settings/service 인접 test·`e5.py self-check`에 추가했으며 worker는 실행하지 않음 | SCH-AC-52·55 코드 이행, 실제 #213 조회·배포·리허설 수용과 구분 |
| 공통 통합 구현 커밋: 위 원시 내보내기 커밋에서 resources 수신·기존 live 1초 ticker·cluster callback/자기 Pod UID를 root에 연결. `resourceObservation` bounds·주기·release/system/control scope는 Helm values/flags, TLS URL/CA/token은 기본 discovery agent config로 전달. raw는 별도 `evidence-collector` ClusterRole(`/raw`와 `/raw/*`), 수신 POST는 기본 agent SA에만 부여하며 둘 다 일반 manager/BFF에 추가하지 않음 | SCH-AC-53·54 공통 wiring; 클러스터 접근·배포 0 |

구현 2의 **배포 시 1회 확인**할 환경 사실은 (a) 비root 기본 agent가 필요한 cgroup 파일을 읽을 수 있는지, (b) edge Node에서 서버 내부 TLS endpoint까지 닿는지, (c) 작은 Node의 1초 표본 오버헤드다. 이번 코드 이행에서는 클러스터 접근·배포·실측을 수행하지 않는다. 확인 전 비root 가능·네트워크 도달·허용 오버헤드를 기정사실로 쓰지 않는다. 실패 시 수집을 가짜 0으로 통과시키거나 actuation 특권을 통째로 넘기지 않고 필요한 읽기 권한/배포 조건과 실측 비용을 명시한다. 최종 완료는 CHILL 사실의 API 단일 출처와 네 수용 시나리오·리허설 증거이며, 문서·코드 이행·배포·실험 수용은 서로 구분한다.
