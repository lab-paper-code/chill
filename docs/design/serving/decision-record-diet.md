# 판단 기록 다이어트 (M0)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M0 설계다. 명세 변경 없이 저장하는 스케줄러
판단 기록을 줄여 이력 조회 중 ChillServer 메모리와 관측 PVC 증가를 낮춘다. `SCH-30`은 불변 기록의 ID 참조로 충족한다.
[워크로드별 POOL 4.5절](workload-pools.md#45-적용과-기록)의 저장 profile ID를 전제로 한다.

## 1. 증상과 원인

증상은 판단 기록 1건 ~10MB, 이력 조회 디코드 중 128Mi ChillServer OOM, 관측 PVC 8Gi 포화다
([01](../architecture-rethink/01-current-premise.md#2-기록-크기)).

`recordDecision`(`cmd/chill-server/scheduler_service.go`)은 제어 주기(10초)마다 `SchedulerObservation{Decision, Snapshot}`
한 줄을 `scheduler.jsonl`에 쓴다. 후보(최대 128개)마다 전체 `Nodes`와 `Instances`가 들어 있고, snapshot의 `Arrivals`는
관측 창의 모든 요청을 요청마다 `Definition` 본문과 함께 담는다.

## 2. 현재 기록 구성

실제 타입을 `encoding/json`으로 직렬화한 go test의 결과다. 규모는 노드 8, Workload 3, 후보 128, 후보당 인스턴스 6,
option 16, profile 6 batch × 200 표본, 60초 창 50 req/s(arrival 3000), 처리 중 요청 20이다.

| 부분 | 크기 | 구성 |
|---|---|---|
| profile 본문 | 15.8KB | 표본 1개 약 13B, batch 수 × 표본 수에 비례 |
| 인스턴스 | 16.4KB | profile ID만 담으면 0.5KB |
| arrival | 1.6KB | `Definition` 본문 1.4KB 포함 |
| snapshot | 5.3MB | `Arrivals` 4.9MB, `Options` 263KB, `Instances` 98KB, `Outstanding` 34KB, 나머지 16KB |
| 후보 | 104KB | `Instances` 98KB |
| decision | 13.5MB | `Candidates` 13.3MB |
| 기록 1건 | 18.8MB | 10초 주기에서 시간당 6.8GB |

관측된 10MB는 표본 수나 인스턴스 수가 이보다 적은 경우와 맞는다 `[추론]`. 지배 항은 후보 × 인스턴스와
arrival × `Definition`이다.

## 3. 저장 형태

`ObservationLog.SchedulerRecord`가 저장 직전에 판단과 snapshot을 다음처럼 투영한다. 메모리의 판단과 live
`GET /api/v1/scheduler`, `/scheduler/state` 응답은 Dashboard가 쓰는 전체 형태를 그대로 유지한다.

| 대상 | 저장 형태 |
|---|---|
| `keep`, `target` | 후보 전체. 인스턴스는 `profileID`만 담는다 |
| `candidates` | 평가 순서의 요약 `{id, keep, status, expectedJoules, worstExceedance, reasons, actions}`. keep과 target의 본문은 `keep`, `target` 필드에 있고 목록에서는 `id`로 대응한다. `policy.candidateKeys`와 index가 맞는다 |
| `policy` | `kernels`를 뺀 나머지. 진행한 결과는 `history`에 남는다 |
| `evaluationElapsedNanos` | Tick의 판단 시작(이력 구간 계산과 capture 포함)부터 판단 확정까지의 monotonic 경과 |
| snapshot | 판단마다 저장한다. `arrivals`는 빼고, 창 경계와 개수는 `search`의 `windowStart`, `windowEnd`, `received`, `accepted`, `computeInputs`, `loads`에 있다 |
| `outstanding[]` | `definition` 대신 `definitionID`(`Definition.Identity()`). 정의 본문은 snapshot `workloads`에 있다 |
| replay 입력 | 노드와 인스턴스의 `observedAt`, 인스턴스의 `arrivalAges`, 동작의 `elapsedNanos`, `routingCredits`를 원값으로 유지한다 |

이력 조회(`GET /api/v1/observations/scheduler`)와 Dashboard 이력, A/B 비교는 같은 route로 이 형태를 읽는다. Dashboard는
요약 후보에 없는 필드를 비어 있는 값으로 표시하고, 처리 중 요청의 정의는 `definitionID`로 표시한다.

2장과 같은 규모에서 저장 기록 1건은 약 68KB다(`TestSchedulerRecordStoresCompactDecision`). 10초 주기에서 시간당 약
25MB다.

### 3.1 알려진 한계

- 저장 판단의 replay는 그 판단이 참조한 profile의 측정이 catalog와 evidence store에 남아 있어야 한다.
- 관측 PVC의 보존 한도는 M1이 정한다. 그때까지 PVC는 판단 기록과 요청 stream만큼 계속 증가하므로, E1에서 잰 시간당
  증가로 `observationStorage` 크기를 정한다.
- 저장 계약은 미배포 `v1alpha4`의 형태를 이어서 바꾼다([workload-pools 8장](workload-pools.md#8-커밋-단위)).

## 4. 검사

| 대상 | 검사 |
|---|---|
| 크기와 요약 | 2장 규모 기록이 100KiB 이하, 요약이 `id`, `status`, `expectedJoules`, `worstExceedance`, `actions`를 유지하고 본문 필드 없음 |
| 이력 읽기 | 저장 기록이 `SchedulerObservationPage`로 읽히고 keep과 target이 전체 후보와 `profileID`를 담음, `kernels` 없음 |
| snapshot | `arrivals` 없음, `outstanding`의 `definitionID`, `arrivalAges` 원값 |
| 판단 시간 | Tick 판단의 `evaluationElapsedNanos`가 양수 |
| Dashboard | `make dashboard-check` |

## 5. E1에서 확인할 값

[workload-pools 9.2절](workload-pools.md#92-실클러스터-e2e) E1의 부하 단계마다 기록한다.

| 값 | 방법 | 목표 |
|---|---|---|
| 판단 기록 크기 | evidence store `serving/<windowID>/scheduler.jsonl`의 판단 줄 크기, 단계별 최대 | ≤100KB |
| ChillServer RSS | `container_memory_working_set_bytes` 최대와 재시작 수 | limit 아래, OOM 재시작 0 |
| PVC 증가 | kubelet volume stats의 시간당 증가 | 기록, M1 전까지의 `observationStorage` 크기 근거 |
| Tick p99 | 판단 기록 `evaluationElapsedNanos`의 p99 | 기록(M4 기준선) |

## 6. 검증 기록

구현 후 이 절에 E1 결과를 기록한다.

## 7. M0 범위 밖

- M1: 관측 PVC 보존 한도, 요청 stream 형식(요청당 wide row, 시간 단위 zstd 세그먼트), `/metrics`와 PromQL.
- M4: 계획이 바뀔 때만 쓰는 판단 기록, 분석형 평가기, `kernels` 재설계, live 판단 응답의 후보 축소.
