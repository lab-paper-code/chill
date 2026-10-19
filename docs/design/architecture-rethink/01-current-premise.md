# 01. 현재 전제 (A)

[README](README.md)의 출처 기준점을 따른다. 코드 줄 번호는 main `99621a6` = feat/workload-pools `42a7f98`.

## 한 문단 요약

A의 전제는 "**하나의 중앙 스케줄러가 매 제어 주기마다 클러스터 전체를 정확히 재계획하고, 그 판단의 입력과
후보를 통째로 남긴다**"이다. 네 가지가 곱해져 느리고 복잡해졌다: **정확성**(요청 단위 Monte-Carlo 재생),
**전역성**(전체 POOL 절대 에너지로 비교), **완전 기록**(판단 입력 전체를 기록에 내장),
**다중 writer**(노드 상태를 여러 프로세스가 check-then-act로 씀).

## 구조 상세와 근거

### 1. 판단 주기와 비용

- sample 설정(`config/samples/serving-scheduler.json`): `controlIntervalNanos: 1e10`(10초),
  `observationWindowNanos: 6e10`(60초), `horizonNanos: 6e10`, `replays: 100`, `maxCandidates: 128`,
  `maxReplicas: 8`, `maxInstances: 16`, `maxReplayRequests: 100000`.
- 10초마다 snapshot 하나(`SchedulerSnapshot`, `internal/serving/scheduler.go:172-191`)를 만든다. 내용:
  모든 노드, 꺼진 Workload까지 포함한 모든 인스턴스, 지난 60초의 모든 `Arrivals`, `Outstanding` 요청,
  원시 실행시간 표본 배열(`BatchProfile.DurationNanos`, `types.go:128-131`)을 품은 `Profile`을 가진
  `Options` 전체, `Costs`(trial 포함), `Baselines`.
- 이 snapshot에서 클러스터 전체 후보를 최대 128개 열거하고, 후보마다 100회 재생
  (`scheduler_replay.go:196`의 `for replay := range settings.Replays`)으로 백분위와 에너지를 계산한다.
- 재생은 사건마다 요청과 인스턴스를 선형으로 훑는다(`scheduler_replay.go:630` 주석:
  "ponytail: bounded scan of requests/instances per event; use an event heap only when ... CPU material").
- 50 req/s 기준 판단 1회 ≈ 128 × 100 × 3000 ≈ 3.8×10⁷ 요청 재생이고, 사건당 선형 scan까지 더하면
  초선형이다 `[추론]`.

### 2. 기록 크기

- 결과는 `SchedulerObservation{Decision, Snapshot}` JSON 한 줄로 저장된다
  (`cmd/chill-server/scheduler_service.go:411-416`의 `recordDecision`,
  `internal/serving/observation_log_scheduler.go:42-63`).
- `SchedulerDecision`(`scheduler.go:236-247`)은 `Keep`, `Target`, `Candidates`(최대 128개)를 담고, 각
  `SchedulerCandidate`(`:202-216`)가 전체 `Nodes[]`, `Instances[]`를 담으며, `SchedulerInstance`(`:62-89`)는
  `*Profile` 본문과 `ArrivalAges map[string][]time.Duration`을 담는다. 여기에 snapshot 전체가 붙는다.
- 이것이 관측된 ~10MB다. 10초 주기면 6건/분 → 시간당 ~3.6GB → 8Gi PVC는 약 2.2시간에 찬다
  `[추론, 관측된 PVC 포화와 일치]`.
- 같은 JSONL을 선형 scan하는 이력 조회가 15분을 넘고, 디코드 과정에서 128Mi ChillServer가 OOM으로 죽는다.

### 3. 결합 (명세가 판단을 전역으로 묶음)

- 전력모드는 노드 단위이고(README "노드 자원과 모델 인스턴스"), 꺼진 Workload의 SLO도 평가해야 하고
  (`SCH-15`, `SCH-33`), 가용 노드와 측정 노드를 포함한 전체 POOL의 *절대* 에너지로 비교해야 한다
  (`SCH-16`, 04 "비교할 비용").
- 그래서 Workload 하나를 바꾸는 후보도 전체 클러스터 후보가 된다.
- 노드 하나의 에너지 근거가 비면 모든 판단이 Pending이 된다(ADR 0022 Consequences:
  "A Node without an unloaded baseline leaves total energy unevaluable").
- 이 결합을 맞추려고 workload-pools §3.4는 측정 POOL 노드 소비를 모든 후보에 같은 값으로 더하고,
  가용 노드 내림을 `Action.Fixed`로 비교에서 빼며 assumption을 남기는 장치를 추가했다.

### 4. 쓰는 주체의 분산 (workload-pools §3.3, ADR 0022 기준)

- `edge.dacs.io/measurement-pool` annotation: BatchEnergyOptimization, ModelLoadedIdleBaseline,
  PowerModeTransitionCost 세 controller와 `serving-operation`(4 writer)
- NodeActuation 생성: 스케줄러와 3개 측정 controller(4 생성자)
- serving Pod 생성: ChillServer와 `serving-operation`(2 생성 경로)
- 이들은 check-then-act로 서로를 피하고, ADR 0022는 그 경쟁 구간을 ponytail로 받아들였다
  ("a check and the action it guards are separate API calls...").
- 다른 owner의 반환은 Node를 watch하지 않으므로 10초 `RequeueAfter` polling으로 발견한다.
- 측정 객체가 노드를 잡은 채 삭제되면 annotation은 수동(`kubectl annotate node`) 정리 대상이다.

### 5. 근거 생애주기의 분산

- CRD 13종(`api/v1alpha1/*_types.go`), `cmd/` 바이너리 13개, Go non-test ~64k LOC(test ~56k LOC).
- workload-pools §7 한 branch에서만 저장 계약 version 변경이 10여 건이다.

### 6. 운영 부담

- inventory, meter, powerRequests는 사람이 scheduler ConfigMap JSON(`cpuInventory`, `gpuInventory`,
  `meters`, `powerRequests`)으로 넣는다.
- Go 바이너리 3개(`serving-worker`, `chill-server`, `measurement-worker`)가 수 GB짜리 L4T runtime image
  안에 구워져 있다(`build/docker/firstfloor.Dockerfile:12-14,71-74`; base는 `l4t-jetpack:r35.4.1`,
  `l4t-tensorrt:r8.2.1-runtime`). Go 한 줄만 바꿔도 runtime image 변형을 다시 빌드한다.

## 요약

정확성(요청 단위 재생) × 전역성(전체 POOL 절대 에너지) × 완전 기록(입력 전체 내장) ×
다중 writer(check-then-act)가 곱해진 결과다. 대안은 각각 이 축을 끊는다:
B1은 정확성·전역성·완전 기록, B2는 다중 writer, B3는 전역성 자체.
