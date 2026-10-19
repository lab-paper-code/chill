# 판단 기록 다이어트 (M0)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M0 설계다. 명세 변경 없이 스케줄러 판단
기록의 크기, ChillServer 메모리와 관측 PVC 사용량을 줄인다. `SCH-30`은 불변 기록의 ID와 시각 참조로 충족한다.
[워크로드별 POOL 4.5절](workload-pools.md#45-적용과-기록)의 저장 profile ID(commit 4)를 전제로 한다.

## 1. 증상과 원인

증상은 판단 기록 1건 ~10MB, 이력 조회 디코드 중 128Mi ChillServer OOM, 관측 PVC 8Gi 포화다
([01](../architecture-rethink/01-current-premise.md#2-기록-크기)).

- `recordDecision`(`cmd/chill-server/scheduler_service.go:364-369`)이 제어 주기(10초)마다
  `SchedulerObservation{Decision, Snapshot}` 한 줄을 `scheduler.jsonl`에 쓴다. 같은 stream에 설정, 동작, 전력 기록이
  함께 있다(`internal/serving/observation_log_scheduler.go:40-61`).
- 후보(최대 128개)마다 전체 `Nodes`와 `Instances`를 담고, 인스턴스마다 `Profile` 본문(원시 실행시간 표본 배열)이
  직렬화된다. `Keep`과 `Target`은 `Candidates`에도 들어 있다(`scheduler_replay.go:148-151`).
- snapshot의 `Arrivals`는 관측 창의 모든 요청이고, 요청마다 `Definition` 본문(모델 경로 포함)이 펼쳐진다.
- 게시한 window(`observation_log.go:411-463`)도 PVC에 계속 남는다. 기록이 작아져도 PVC는 결국 찬다.

## 2. 현재 기록 구성

실제 타입을 `encoding/json`으로 직렬화한 일회성 go test의 결과다. 규모는 노드 8, Workload 3(모델 경로 3개),
후보 128, 후보당 인스턴스 6, option 16, profile 6 batch × 200 표본, 60초 창 50 req/s(arrival 3000), 처리 중 요청 20이다.

| 부분 | 크기 | 구성 |
|---|---|---|
| profile 본문 | 15.8KB | 표본 1개 약 13B, batch 수 × 표본 수에 비례 |
| 인스턴스 | 16.4KB | profile ID만 담으면 0.5KB |
| arrival | 1.6KB | `Definition` 본문 1.4KB 포함 |
| snapshot | 5.3MB | `Arrivals` 4.9MB, `Options` 263KB, `Instances` 98KB, `Outstanding` 34KB, 나머지 16KB |
| 후보 | 104KB | `Instances` 98KB |
| decision | 13.5MB | `Candidates` 13.3MB |
| 기록 1건 | 18.8MB | 10초 주기에서 시간당 6.8GB |

관측된 10MB는 표본 수나 인스턴스 수가 이보다 적은 경우와 맞는다 `[추론]`. 지배 항은 후보 × 인스턴스 × profile 본문과
arrival × `Definition`이다. `ArrivalAges`는 firstfloor가 Workload마다 값 2개 이하로 보내므로
(`internal/firstfloor/scheduler.go:328-337`) 원값을 유지한다. `Policy.Kernels`는 `projected-work-history`에서 보존 구성 수의
제곱만큼 생기고, sample 설정의 `fixed-horizon`(`config/samples/serving-scheduler.json`)에서는 비어 있다.

## 3. 설계

### 3.1 저장 형태

window 디렉터리에 `decisions.jsonl`을 더한다. `decisions.jsonl`은 판단 기록만 담고, `scheduler.jsonl`은 구성 기록,
설정, 동작, 전력 기록을 담는다. 판단 기록은 다음 필드를 가진다.

| 필드 | 내용 |
|---|---|
| `at`, `status`, `reasons`, `settings`, `search`, `workloads`, `references` | 현재 값 그대로 |
| `snapshotAt` | 이 판단이 쓴 구성 기록의 `at` |
| `keep`, `target` | 후보 전체. 인스턴스는 4.5절대로 `profileID`만 담는다 |
| `candidates` | 평가 순서의 요약 `{id, status, expectedJoules, worstExceedance, reasons, actions}`, `actions`는 동작 수. keep과 target도 요약으로 들어가므로 `policy.candidateKeys`와 index가 맞는다 |
| `policy` | `kernels`를 뺀 나머지. 진행한 결과는 `history`에 남는다 |
| `inputs` | 주기마다 바뀌는 replay 입력: `observationEnd`, `outstanding`(`definition`은 identity 문자열), `routingCredits`, `arrivalAges`(인스턴스 ID별) |

구성 기록은 snapshot에서 `at`, `observationEnd`, `arrivals`, `outstanding`, `routingCredits`, 노드와 인스턴스의
`observedAt`, 인스턴스의 `arrivalAges`, 동작의 `elapsedNanos`를 뺀 형태(구성 형태)에 기록 시각 `at`을 붙인 것이다.
arrival 원본은 요청 stream에 있고, 창 경계와 개수는 `search`의 `windowStart`, `windowEnd`, `received`, `accepted`,
`computeInputs`, `loads`에 있다. `SchedulerSnapshot.Arrivals`는 JSON에서 빠지므로(`json:"-"`) live
`GET /api/v1/scheduler`와 `/scheduler/state` 응답도 arrival 본문 없이 작아진다. Dashboard는 이 필드를 읽지 않는다.

M4 오프라인 A/B는 구성 기록, `inputs`, evidence store window의 요청 stream `[windowStart, windowEnd)`,
catalog의 profile로 replay 입력을 다시 만든다. profile 본문은 측정이 남아 있는 동안 catalog에서 조회한다.

### 3.2 구성 기록 규칙

`ObservationLog.DecisionRecord(decision, snapshot)`이 writer 잠금 한 번 안에서 다음을 수행한다.

1. 활성 window를 확인하고 필요하면 rotation한다.
2. 구성 형태가 마지막으로 쓴 구성 형태와 다르거나(`reflect.DeepEqual`) 이 window에 구성 기록이 아직 없으면
   `at = decision.at`으로 구성 기록을 `scheduler.jsonl`에 쓴다.
3. `snapshotAt`을 채운 판단 기록을 `decisions.jsonl`에 쓴다.

판단과 그 구성 기록은 항상 같은 window에 있어 window 단위 삭제가 참조를 끊지 않는다. 마지막 구성 형태는 메모리에만 두고,
재시작 뒤 첫 판단이 다시 쓴다. 비교는 중복 저장을 거르는 용도이며 검증 gate로 쓰지 않는다. hash는 두지 않는다.

전력 조회의 membership(`observation_log_power.go:66-125`, `:132-160`)은 구성 기록의
`instances[].{node, ready, stopped, workloadUIDs}`를 지금처럼 읽는다. 이 필드가 구성 형태에 들어 있어 소속이 바뀐 판단마다
새 구성 기록이 생기고, 구간 시작의 기준 snapshot은 window 첫 구성 기록으로 같은 window 안에서 찾는다.

### 3.3 예상 크기

2장과 같은 규모에서 판단 기록은 약 50KB(keep과 target 각 8.5KB, 요약 128 × 160B = 21KB, `inputs` 8KB, 나머지 3KB),
구성 기록은 약 28KB다. 10초 주기와 1분 window에서 판단과 구성 기록은 시간당 약 20MB다. 요청 stream은 요청마다
사건 여러 개(각 약 350B)를 남겨 50 req/s에서 시간당 수백 MB다 `[추론]`. PVC 상한은 3.4절의 보존 한도가 정한다.

### 3.4 보존 한도

`--observation-retain-bytes`(Helm `servingEntrypoint.observationStorage.retainBytes`)를 둔다. `archive`가 게시를 마치면
게시된 window의 `objects[].size` 합과 활성 window 크기를 더해 한도를 넘는 동안 가장 오래된 게시 window의 디렉터리를
지우고 목록에서 뺀다(활성 index 보정 포함). 게시되지 않은 window와 활성 window는 남긴다. 지운 window는 evidence store의
`serving/<windowID>/`에 그대로 있다. ChillServer 조회는 PVC에 남은 window를 대상으로 한다. 기존 window별 census가 시간
범위로 prefix를 거르므로 별도 시간 index는 두지 않는다.

### 3.5 API와 Dashboard

| 표면 | 계약 |
|---|---|
| `GET /api/v1/observations/decisions` | `/observations/scheduler`와 같은 `from`, `to`, `limit`, `cursor`, `view=compact`. `events[]{at, decision, snapshot}`, `snapshot`은 같은 window의 `snapshotAt` 구성 기록 |
| `GET /api/v1/observations/scheduler`(`httpapi/handler.go:110`) | 구성, 설정, 동작, 전력 기록. `serving-operation`의 동작 확인(`cmd/serving-operation/main.go:779-822`)은 그대로 동작한다 |
| `GET /api/v1/scheduler`, `/scheduler/state` | 메모리의 전체 판단과 snapshot(arrival 제외) |

Dashboard는 다음을 바꾼다.

- `lib/relay.ts:54,74`: `v1/observations/decisions`를 scheduler history와 같은 query 조건과 compact로 허용한다.
- `lib/read-types.ts`: 저장 판단(`candidates` 요약, `snapshotAt`, `inputs`)과 구성 기록 타입.
- `lib/scheduler-history.ts:5-26`: 판단 ref의 경로를 decisions route로 만든다.
- `features/scheduler-history.tsx`: 판단 목록은 decisions route에서 읽고, 상세는 join된 구성과 `inputs.outstanding`을 보인다.
- `features/scheduler-evidence.tsx:89-183`: A/B 비교가 decisions route를 읽고, 서비스 목록을 keep과 target의 `slo`에서 만든다.
- live 화면(`features/scheduler.tsx`, `overview.tsx:124`)은 현재 계약을 그대로 쓴다.

## 4. 저장 계약

| 계약 | 값 | 정리 대상 |
|---|---|---|
| `observationSchema`(`observation_log.go:21`) | window에 `decisions.jsonl`, 판단과 구성 기록의 3.1절 형태 | PVC의 serving 관측 window와 evidence store의 `serving/<windowID>/` |

version은 commit 2에서 한 번 올린다. workload-pools와 같은 배포로 나가면 미배포 `v1alpha4`의 형태를 이어서 바꾸고,
workload-pools가 먼저 배포되었으면 `v1alpha5`로 올린다([ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md),
[workload-pools 8장](workload-pools.md#8-커밋-단위)). 배포 때 Helm `observationStorage.directory`를 새 경로로 바꾸고 이전 window를
지운다. 이전 형태의 reader는 만들지 않는다.

## 5. 커밋 단위

모든 commit은 단독으로 build되고 `make lint`와 `make test`를 통과하며, producer와 모든 consumer를 함께 바꾼다.

| # | 제목 | 파일 | 선행 | 추가 검사 |
|---|---|---|---|---|
| 1 | `feat(serving): bound retained observation windows by size` | `observation_log.go`(`archive` 뒤 삭제), `cmd/chill-server/options.go`, `server.go`, chart values와 `serving_entrypoint.yaml`, [설정과 관측 API](scheduler-api.md) | 없음 | `go test ./internal/serving/... ./cmd/chill-server/...`, `make helm-lint helm-template` |
| 2 | `refactor(serving): record scheduler decisions in their own stream` | `observation_log*.go`(stream, census, `DecisionRecord`, decision page), `scheduler_service.go`, `httpapi`의 decisions route, Dashboard relay와 history, A/B 화면, version | 1, workload-pools 4 | `go test ./internal/serving/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make dashboard-check` |
| 3 | `feat(serving): summarize unselected candidates in decision records` | 저장 판단 형태(`candidates` 요약, `kernels` 제외), Dashboard 판단 타입과 화면 | 2 | 위와 같음 |
| 4 | `feat(serving): store the scheduler configuration only when it changes` | 3.1절 `inputs`와 구성 형태, 3.2절 규칙, `SchedulerSnapshot.Arrivals`의 `json:"-"`, decision page join, 전력 조회 test, Dashboard 상세 | 3 | 위와 같음 |
| 5 | `docs(design): record decision record diet verification` | 이 문서 8장 | 4와 E1 | 문서 링크 확인 |

1은 workload-pools와 독립이라 바로 진행한다. 2-4는 순서대로 진행한다.

## 6. 검사

| 대상 | 검사 |
|---|---|
| 보존 한도 | 게시 window 합이 한도를 넘으면 가장 오래된 게시 window부터 지움, 게시되지 않은 window와 활성 window는 크기와 무관하게 남음, 삭제 뒤 조회와 재시작이 남은 window만 읽음 |
| 판단 stream | 판단 기록은 `decisions.jsonl`에만 있음, `decisions.jsonl`이 게시 객체에 포함됨, decision page의 `limit`과 `cursor`가 scheduler page와 같은 계약, scheduler page에 동작과 전력 기록이 그대로 남음 |
| 후보 요약 | keep과 target이 전체 후보와 `profileID`를 담음, 요약이 평가 순서이고 `policy.candidateKeys`와 index가 맞음, `kernels` 없음, live `/scheduler/state`의 후보는 전체 |
| 구성 기록 | 같은 구성의 연속 판단은 window마다 구성 기록 1건, `ready`, `workloadUIDs`, routing, option 변화는 새 구성 기록, `observedAt`, `arrivalAges`, `routingCredits`, `outstanding`, `elapsedNanos`만 바뀌면 새 기록 없음, 새 window의 첫 판단이 구성 기록을 씀, 재시작 뒤 첫 판단이 구성 기록을 씀, decision page의 `snapshot`이 `snapshotAt`의 구성 기록 |
| 전력 조회 | 변경 시 저장한 구성 기록으로 계산한 membership과 활성 POOL 에너지가 기존 주기별 snapshot 결과와 같음, 구간 시작 기준 snapshot을 같은 window에서 찾음 |
| JSON | snapshot JSON에 `arrivals` 없음, 저장 `outstanding`의 `definition`이 identity 문자열 |
| 크기 | 2장 규모 fixture의 저장 판단 기록이 100KB 이하 |
| Dashboard | relay가 decisions route를 history 조건으로 허용, history 상세와 A/B 비교가 요약 후보와 join된 구성을 표시 |

## 7. E1에서 확인할 값

[workload-pools 9.2절](workload-pools.md#92-실클러스터-e2e) E1의 부하 단계마다 기록한다.

| 값 | 방법 | 목표 |
|---|---|---|
| 판단 기록 크기 | evidence store `serving/<windowID>/decisions.jsonl` 크기 ÷ 줄 수, 단계별 최대 | ≤100KB |
| 판단과 구성 기록 증가 | `decisions.jsonl`과 `scheduler.jsonl` 크기 합의 시간당 값 | ≤25MB/h(10초 주기) |
| stream별 PVC 증가 | window 객체 크기의 stream별 시간당 값 | 기록(요청 stream은 M1 입력) |
| PVC 사용량 | kubelet volume stats | `retainBytes`와 활성 window 합 이하에서 평탄 |
| ChillServer RSS | `container_memory_working_set_bytes` 최대와 재시작 수 | 128Mi 아래, OOM 재시작 0 |
| 이력 조회 | 1시간 구간 `GET /observations/decisions` 전체 page 소요 | ≤10초 |
| 판단 지연 p99 | Tick 소요 | 기록(M4 기준선) |

## 8. 검증 기록

구현 후 이 절에 E1 결과를 기록한다.

## 9. M0 범위 밖

- M1: 요청 stream 형식(요청당 wide row, 시간 단위 zstd 세그먼트), `/metrics`와 PromQL, evidence store 용량 관리.
- M4: 계획이 바뀔 때만 쓰는 판단 기록과 heartbeat, 분석형 평가기, `kernels` 재설계, live 판단 응답의 후보 축소.
