# 분석형 계획기 (M4)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M4 설계이고, 알고리즘은 [B1 두 시간척도 계획기](../architecture-rethink/02-b1-two-timescale-planner.md)다. 계획기는 프로파일에서 만든 용량표로 논리 구성 공간을 분석식으로 평가하고, 상위 K개만 요청 단위 재생으로 검증해 target을 고른다. 요청 처리(router 가중치, firstfloor EDF batcher)는 그대로다. shadow로 기존 재생 계획기와 나란히 돌려 일치율과 에너지 차이를 기록하고, 오프라인 A/B와 shadow 결과가 [07의 기준](../architecture-rethink/07-validation.md)을 통과하고 사용자가 명세 변경 ①, ①', ④(11장)를 승인하면 전환한다. 명세 변경 ②와 ③은 이미 반영되어 있다(`SCH-16`, spec README). [M0](decision-record-diet.md)와 [M1](observability-split.md)을 전제로 한다.

## 1. 측정한 문제

근거는 E1(2026-10-19, rev 35, [workload-pools 9.3절](workload-pools.md#93-검증-기록))과 그 뒤 20분 확인의 결함 기록(D-20~D-22), 그리고 코드다.

| 문제 | 근거 |
|---|---|
| Tick이 부하에 비례해 500m CPU limit을 채운다 | `replays` 10, `maxCandidates` 32에서 Tick p50이 0.633 req/s 913 ms, 2 req/s 3,475 ms, 5 req/s 7,495 ms, 5 req/s 최대 22,575 ms이고 CPU는 499m이다(D-16). 기본값 100과 128에서는 Tick 133회가 30 s deadline으로 실패해 첫 구성이 없었다(D-1). 판단 하나가 후보 × 반복 횟수만큼 이산 사건 재생을 하고, 재생은 배치 결정마다 `firstfloor.ChooseBatch`를 부른다. 그 비용은 O(대기 요청² × 배치 행)이다(`internal/firstfloor/policy.go:87`). 과부하 후보는 재생 중 큐가 자라므로 가장 비싼 재생이 선택될 수 없는 후보의 재생이다 `[추론]` |
| 재진입 후보에 닿지 못한다(D-22) | 인스턴스가 생긴 뒤 판단 128건 중 127건이 32에서 잘렸고, probe에서 재진입 후보는 128개 중 37~48번째였다. 열거는 노드 UID 순 깊이 우선이고(`schedulerOptionLess`, `scheduler_candidates.go:767-785`) 상한을 `visit`, `emit`, `place`에서 검사하므로 앞선 노드의 option과 route 변형이 상한을 다 쓴다 |
| 재진입 사슬이 horizon을 넘는다 | 빈 노드 전환은 jetsonx 63.5 s, jetsono 134.5 s, orin-nano 197.1 s다(settling 60 s 포함, D-20, D-21). 사슬이 horizon 60 s를 넘는 후보는 `TransitionExceedsHorizon`으로 평가 대기다(a2f0073, `scheduler_replay.go:216-220`). 사용자는 horizon 60 s를 유지하고 M4에서 푸는 것을 골랐다 |
| 지켜야 할 에너지 예측 정확도 | E1 1시간 비교 집합 예측 499,333 J, 실측 495,741 J(+0.7%). 전체 POOL 실측은 521,589 J다 |
| 지켜야 할 결정성 | 재생은 `(seed, 반복 번호)` PCG를 쓰고 모든 입력을 정렬하며, 완전 동률은 keep이 이긴다(`scheduler_replay.go:205-230,417-424`) |

## 2. 범위

- M4: 용량표(3장), 분석형 평가(4장), 구성 탐색(5장), 비교 기간(6장), 상위 K 재생 검증(7장), shadow(9장), 오프라인 A/B(10장), 명세 변경(11장), 전환과 삭제(16장).
- 그대로: 후보의 동작 DAG(`schedulerPlan`), 측정 option 도출(`schedulerMeasuredOptions`), baseline 선택(`schedulerBaseline`), 재생 코드(검증기와 오프라인 simulator), 전환 실행기, 가용 노드 최저 모드 규칙, 비교 집합(`ComparedNodes`), 완료 여유.
- M4 밖: firstfloor batcher의 O(대기 요청²) 탐색. 실제 큐는 SLO가 지켜지는 동안 짧다.

## 3. 용량표

판단마다 snapshot의 option(노드, 모드, 할당, profile)과 켜진 Workload w(SLO 백분위 p, 목표 L, 완료 여유 m)마다 행을 만든다. profile의 `BatchProfile`은 측정한 배치 크기 b마다 실행시간 표본 `DurationNanos`와 `EnergyPerInputJoules`를 담는다(`internal/serving/types.go:128-153`). 별도 성능 측정은 없다.

| 값 | 정의 |
|---|---|
| `l_q(b)` | b의 실행시간 표본의 q 분위수. firstfloor가 쓰는 `serviceCost`(gonum `stat.Empirical`)를 내보내 같은 값을 쓴다 |
| `l̄(b)` | b의 실행시간 평균 |
| `e(b)` | 요청당 증분 에너지 `EnergyPerInputJoules(b)` |

**지연 상한.** firstfloor는 Workload마다 큐를 두고 EDF로 배치를 고른다. 인스턴스 i를 공유하는 Workload 집합을 `W_i`, 각 Workload j의 배치 크기를 `b_j`라 하자. 큐마다 기다리는 배치가 하나 이하이면, w의 요청은 실행 중인 배치 하나, 다른 Workload마다 배치 하나, 자기 배치 하나를 거친다. 항의 수는 `n = |W_i| + 1`이다. 합의 p 분위수를 보수적으로 잡기 위해 union bound를 쓴다: 각 항을 `q = 1 − (1 − p)/n` 분위수로 잡으면 합이 그 값들의 합을 넘을 확률은 `1 − p` 이하다. 그래서 w의 상한은

`U_w = max_j l_q(b_j; j의 profile) + Σ_{j ≠ w} l_q(b_j; j의 profile) + l_q(b_w; w의 profile) + m ≤ L_w`

이고 실행 중인 배치 항은 공유 Workload 중 가장 느린 배치다. 공유 Workload마다 자기 큐의 배치 크기와 표본을 쓴다. 단일 Workload(n = 2, p95)는 각 항을 p97.5로 잡는다.

**배치 선택과 용량.** `W_i`의 배치 크기 조합(측정 크기의 곱, E1은 Workload 3개와 크기 2개로 8개 이하) 중 모든 w의 상한과 사용률 `Σ_j λ_{j,i} × l̄(b_j)/b_j ≤ ρ`를 만족하는 조합에서 `Σ_j λ_{j,i} × e(b_j)`가 가장 작은 것을 고른다(동률은 사전 순으로 작은 크기). firstfloor가 SLO 여유와 에너지 이득으로 배치를 정하는 것(`SCH-23`)과 같은 방향이다. ρ는 설정 `capacityHeadroom`이다. 조합이 없으면 그 인스턴스는 초과다.

**상한을 쓰지 않는 경우.** 다음이면 상한이 보수적이라고 말할 수 없으므로 그 후보는 `ReplayRequired`이고 충족으로 표시하지 않는다: 어떤 항의 표본 수가 `⌈1/(1 − q)⌉`보다 적음(예: imagenet p99, n = 2이면 q = 0.995에 200개, orangepi profile은 128개), 공유 Workload 중 deadlinePolicy가 `drop` 또는 `defer`, 고정 배치 크기 인스턴스(`SCH-35`). `ReplayRequired` 후보는 7장의 순위에 들어가고 재생 결과로만 판정된다.

E1 profile(`e1/profiles-pre.json`)로 만든 tight Workload(p95, 1 s, m 139 ms, ρ 0.8) 단일 행의 예다. 측정한 배치는 1과 2이고 부하 0에서 용량이 가장 큰 b를 보인다.

| option | `l_97.5` b=1 / 2 (ms) | `l̄` b=1 / 2 (ms) | 용량 최대 b | μ (req/s) | `e` (J/요청) | 최소 표본 |
|---|---|---|---|---|---|---|
| rasp5 fixed ort-cpu q1 | 31.6 / 65.5 | 27.8 / 60.3 | 1 | 28.8 | 0.07 | 427 |
| jetsonx MODE_15W_6CORE | 56.7 / 103.6 | 49.3 / 93.9 | 2 | 17.0 | 0.07 | 268 |
| jetsono MAXN | 22.9 / 42.6 | 20.4 / 39.1 | 2 | 40.9 | 0.05 | 651 |
| orin-nano 25W ort-cuda | 28.8 / 50.6 | 24.8 / 44.7 | 2 | 35.8 | 0.05 | 505 |
| genesis2 fixed ort-cuda q1 | 9.2 / 16.5 | 7.2 / 13.8 | 2 | 116.3 | 0.26 | 1,610 |
| orangepi fixed ort-cpu q1 | 107.4 / 213.4 | 100.4 / 203.0 | 1 | 8.0 | 0.10 | 128 |

## 4. 분석형 평가

- 부하: `λ_w`는 관측 창의 계산 도착 수(재생이 쓰는 것과 같은 `Compute` 또는 `Accepted` 도착)와 현재 대기 요청 수의 합을 창 길이로 나눈 값이다. Workload의 인스턴스에는 router 가중치 비율로 나눈다. 새 인스턴스의 가중치는 그 인스턴스 용량을 반올림한 정수(최소 1)다.
- SLO: 모든 공유 Workload가 3장의 상한과 사용률을 만족하면 충족이다. 아니면 초과량을 `max(사용률/ρ, U_w/L_w) − 1`로 두고, 선택 순서는 재생과 같은 `schedulerBetter`(충족, 최악 초과량, 에너지)다.
- 전환 중: 동작 DAG는 make-before-break(prepare, route, drain, remove)이므로 전환 중 용량은 이전과 이후 중 작은 쪽에서 동시에 빠지는 인스턴스를 뺀 값이다. 그 용량으로 같은 사용률 검사를 한다(`SCH-25`).
- 정상 전력: 비교 집합 노드마다 `schedulerBaseline`의 적재 상태 전력, 그리고 인스턴스마다 `Σ_j λ_{j,i} × e(b_j)`.
- 전환 에너지와 시간: 판단 시작 때 노드마다 (현재 상태, 목표 상태)의 동작 사슬 표를 만든다. 사슬은 `schedulerPlan`이 그 노드에 두는 동작(power, prepare, drain, remove)이고, 에너지는 측정 trial 평균 wall 에너지, 시간은 평균과 최댓값을 둘 다 둔다. 후보의 사슬 길이 `d_c`는 새 노드 사슬의 최댓값(최댓값 시간), route, 이전 노드 사슬의 최댓값의 합이다. 노드는 첫 동작 전에는 이전 전력, 마지막 동작 뒤에는 이후 전력이고, 요청 증분은 route가 끝날 때까지 이전 인스턴스에 붙는다. 비용이 없으면 지금처럼 `MissingOperationCost`다. 상위 K는 `schedulerPlan`의 실제 DAG로 재생한다.
- 비용은 구성마다 노드와 인스턴스 수에 비례하고 요청 수와 무관하다.

## 5. 구성 탐색

**논리 구성 공간.** 켜진 Workload마다 option 다중집합(인스턴스 1개에서 `maxReplicas`개), 전체 `maxInstances` 이하, 노드마다 한 모드, `schedulerValidateResources`를 통과하는 자원, `Controls.Sharing`이 켜지면 같은 모델 Workload의 공유 여부. 가중치는 4장에서 정하므로 route 변형은 공간에 없다. 꺼진 Workload의 인스턴스는 고정 제약이다(workload-pools 4.4절). `Controls`가 꺼진 항목은 keep의 값으로 고정한다.

- 크기 세기: option을 `schedulerOptionLess` 순으로 도는 깊이 우선 열거로 평가 없이 세고, 설정 `exhaustiveConfigurations` + 1에서 멈춘다.
- 전수: 크기가 상한 이하이면 모든 구성을 평가한다. 재진입을 포함한 모든 구성이 순위에 들어가므로 D-22가 사라지고, 한 단계로는 나빠지는 두 인스턴스 교체 같은 구성도 평가한다. 상한의 sample 값은 10장 A/B가 보고하는 E1 공간 크기다 `[추론: 노드당 인스턴스 하나만 세면 E1 option으로 25,919개]`.
- beam: 크기가 상한을 넘으면 keep에서 출발해 한 동작(추가, 제거, option 바꾸기, 공유 묶기와 풀기)씩 깊이를 늘리며, 깊이마다 나아지지 않은 구성도 포함해 4장의 순서로 상위 `B`개를 남긴다. `B = ⌊exhaustiveConfigurations / 한 구성의 이웃 수 / maxInstances⌋`(최소 1)라 평가 수가 상한 근처에 머문다. 깊이는 `maxInstances`까지다. 모든 깊이에서 평가한 구성이 7장 순위에 들어간다.
- 같은 논리 구성(`SchedulerLogicalConfigKey`)은 한 번만 평가한다. keep은 늘 공간에 있다.
- 후보의 동작은 keep에서 그 구성까지 `schedulerPlan`이 만든다.

## 6. 비교 기간

- 판단마다 비교 기간 하나 `T = max(H, max_c d_c)`를 먼저 정한다. `max_c d_c`는 공간 전체의 최장 사슬이고, 노드별 사슬 표의 최댓값 시간으로 평가 전에 계산한다. keep의 `d_c`는 진행 중 동작의 남은 길이다.
- keep을 포함한 모든 후보의 분석형 에너지 `E_c(T)`를 이 T에서 계산하고, 그 값으로 순위와 상위 K를 정한다. 모든 후보가 같은 시간축과 같은 Serving 시간을 가지며(`SCH-16`), power와 prepare의 측정 에너지 전체가 들어간다.
- 검증 재생도 같은 T를 horizon으로 쓴다. 사슬 길이를 trial 최댓값으로 잡았으므로 재생에서 표본으로 뽑은 사슬도 T 안에 끝나고 `TransitionExceedsHorizon`은 생기지 않는다.
- horizon 설정 H는 60 s 그대로이고 비교 기간의 최솟값이다. 판단 기록은 `analytic.comparisonNanos`에 T를 남긴다(`SCH-18`, `SCH-30`). 비교 기간을 정하는 방법은 04 명세가 설계에 맡긴다("비교 기간, 그동안의 부하를 다루는 방법, 후보 탐색 알고리즘은 설계에서 정한다").
- E1 값: 비용이 있는 가용 노드가 있으면 T는 그 노드의 사슬로 정해진다. jetsonx만 있으면 power 63.5 s에 prepare(E1 rasp5 prepare 17.6 s)를 더한 규모이고, orin-nano가 있으면 197.1 s 이상이다. jetsonx 재진입은 측정한 전력 단계 597 J 전체를 낸다(D-20의 keep 대비 +6.4 J 과소 평가가 사라진다).

## 7. 상위 K 재생 검증

- 평가한 구성을 `schedulerBetter` 순서(분석형 값)로 줄 세워 K개를 고른다. `ReplayRequired`는 이 순서에서 충족으로 취급하므로 에너지가 낮으면 K개에 들어가 재생으로 판정된다. K는 설정 `verifiedCandidates`다.
- keep과 그 K개를 기존 `schedulerEvaluateCandidate`로 T에서 재생한다(반복 `replays`, `seed`). 분석형이 상한을 쓸 수 있는 경우에 초과로 판정한 후보는 keep이라도 재생하지 않고 그 판정을 쓴다. 그래서 큐가 자라는 재생이 없다.
- target은 재생 결과에 `schedulerBetter`를 적용해 고른다. 분석형 판정만으로 target이 되는 후보는 없다. 기록하는 `ExpectedJoules`는 재생 에너지이므로 E1의 예측 정확도(+0.7%)를 그대로 잇는다.
- 재생 수는 판단마다 최대 (K + 1) × `replays`다. K 3, `replays` 10이면 40회로 E1의 320회보다 적다. 재생 하나의 요청 수는 T에 비례한다 `[추론: Tick 시간은 15장에서 잰다]`.

## 8. 결정성

- 분석형 평가와 탐색은 난수가 없다. option은 `schedulerOptionLess`, 인스턴스는 ID 순으로 돌고 실수 합도 그 순서다. beam의 자르기도 같은 순서다.
- 동률은 keep, 그다음 논리 구성 key의 사전 순이 이긴다. 재생은 지금의 seed 규칙 그대로다.
- 같은 snapshot과 설정은 같은 판단을 만든다. 14장의 test가 입력 순서를 섞어 확인한다.

## 9. shadow

- 전환 전 Tick은 지금 계획기로 판단하고, 기록하고(`OnDecision`), launch한다. 그 순서와 시간은 그대로다(`kube/scheduler.go:240-276`).
- launch 뒤 Tick은 그 snapshot과 설정의 복사본, 실행한 판단의 시각과 target key를 shadow goroutine에 넘긴다. shadow는 Tick context와 무관한 자기 context(제한 시간 `controlInterval`)로 5~7장 전체를 돌리고, 끝나면 scheduler stream에 shadow 기록 하나를 append한다. controller 상태는 바꾸지 않는다. 앞선 shadow가 아직 돌면 이번 shadow는 건너뛰고 기록의 `skipped`를 센다.
- shadow 기록: `{at, decisionAt, analytic}`. `analytic`은 `comparisonNanos`, `configurations`(평가 수), `search`(`exhaustive` 또는 `beam`), `verified`(K개와 keep의 key, 분석형 J, 재생 J, 상태, 최악 초과량), `targetKey`, `agree`(실행 target과 논리 구성 key가 같은지), `replayJoulesDelta`, `optimistic`(분석형 충족인데 재생 초과인 후보 수), `replayRequired`, `evaluationElapsedNanos`, `skipped`다.
- 불일치하면 실행 target도 T에서 한 번 재생해 `replayJoulesDelta = 재생 J(분석형 target) − 재생 J(실행 target)`를 기록한다. 같은 재생기와 같은 기간이라 바로 비교한다.
- 전환 뒤에는 판단 기록 자체가 같은 `analytic` 묶음을 담고 shadow 기록은 쓰지 않는다. 그때 `agree`는 분석형 1위가 검증 뒤 target과 같은지이고, delta는 둘의 재생 J 차이다.

## 10. 오프라인 A/B

입력은 M1 배포 뒤의 `v1alpha5` 시간 세그먼트(재생 계획기의 판단 기록과 shadow 기록)다. `hack/observations/fetch.sh`로 받고, `DecodeSchedulerLine`과 시간의 `catalog`로 참조를 풀고, 도착은 `requests` 행에서 `ReduceSchedulerArrivals`로 만든다. 이 reducer는 `LoadWindow`의 `requestArrival` 접기를 행 iterator를 받는 함수로 뗀 것이다(`ReduceOperatingPower`와 같은 방식).

`hack/planner-ab/main.go`가 판단마다 세 계획기를 돌린다: 재생 계획기(운영 값, `replays` 10, `maxCandidates` 32), 상한을 없앤 재생 계획기(`maxCandidates` 4096, 오프라인에는 CPU 제한이 없다), 분석형 계획기. 결과는 판단마다 JSONL 행(시각, 세 target key, 일치, 각 target의 T 재생 J, `optimistic`, `replayRequired`, 공간 크기, 계획기별 시간)이고 DuckDB로 요약한다. live shadow 기록은 같은 표에 그대로 합친다.

- 기준(07의 1번): 판단마다 분석형 target이 재생 target과 같거나 재생 J 차이가 5% 이하, 무절단 재생 target 대비 5% 이하, `optimistic` 0. 실패하면 ρ를 조정하고, 반복 실패하면 재생 계획기를 유지한다.
- 공간 크기 열의 E1 최댓값이 `exhaustiveConfigurations`의 sample 값이 된다.

## 11. 명세 변경 제안

사용자 승인 전에는 효력이 없다. 원문은 6cc63d7 기준이다.

**① `scheduler/SRS.md` `SCH-13`** 첫 문장과 끝 문장을 바꾼다.
- 현재 첫 문장: "실행시간 분포에 현재 요청 도착·대기·진행 상태와 후보의 분배·배칭·큐 선택 규칙을 적용해 요청별 예상 완료 시각과 종단 지연을 계산한다."
- 제안: "후보의 SLO 충족은 측정한 배치 실행시간의 SLO 백분위 값과 현재 요청률과 대기량으로 계산한 보수적 지연 상한으로 판정하고, 선택할 후보는 요청 단위 재생으로 검증한다."
- 현재 끝 문장: "SLO 백분위는 계산한 요청별 종단 지연 분포에서 구한다."
- 제안: "Serving 관측은 지연 상한의 보수성을 확인하는 데에도 사용한다."
- 같은 취지로 `SCH-14` 끝 문장 "…각 워크로드의 완료 시각·지연 분포를 평가한다."를 "…각 워크로드의 지연 상한을 평가한다."로, `SCH-AC-09`의 기대 결과 "요청별 예상 완료와 종단 지연 분포를 계산해 SLO 평가"를 "배치 실행시간 분위수와 부하로 지연 상한을 계산해 SLO 평가, 선택 후보는 재생으로 검증"으로 바꾼다.

**①' `04_configuration_selection.md` "프로파일에서 요청 지연 계산", `00_measurement.md`**
- 04 현재 첫 문장: "프로파일의 실행시간 분포에 현재 요청 도착·큐·진행 상태와 후보 구성의 분배·배칭·큐 선택 규칙을 적용해 요청별 예상 완료 시각을 계산한다."
- 제안: "프로파일의 배치 실행시간 분포와 현재 요청률과 대기량으로 후보 구성의 워크로드별 지연 상한을 계산하고, 선택할 후보는 요청 단위 재생으로 검증한다."
- 04 현재: "평균 처리량만으로 지연 백분위를 결정하거나 단계별 지연 백분위를 단순 합산하지 않는다."
- 제안: "평균 처리량만으로 지연 백분위를 결정하지 않는다. 대기와 배치 실행의 백분위 값은 보수적 상한으로 합성하고, 실제 관측으로 그 보수성을 확인한다."
- 00 현재: "…스케줄러는 현재 도착·대기·진행 상태와 후보 구성의 처리 규칙으로 요청별 완료 시각을 계산한다."
- 제안: "…스케줄러는 배치 실행시간의 백분위 값과 현재 요청률과 대기량으로 후보 구성의 보수적 지연 상한을 계산한다."
- 두 파일은 저장소가 추적하지 않는 로컬 사본이다(`.gitignore:22`).

**④ `SCH-23`**
- 현재: "…모델의 deadlinePolicy(drop/keep/defer, 기본 keep)를 actual/replay에 동일하게 적용한다."
- 제안: "…모델의 deadlinePolicy(drop/keep/defer, 기본 keep)를 actual과 검증 재생에 동일하게 적용한다." 분석형 상한은 `keep`에서만 쓰고, `drop`과 `defer`는 재생으로 판정한다(3장).

## 12. 설정과 저장 계약

| 대상 | 변경 |
|---|---|
| scheduler 설정(commit 2) | `capacityHeadroom` ρ(0 < ρ ≤ 1, sample 0.8), `verifiedCandidates` K(≥ 1, sample 3), `exhaustiveConfigurations`(≥ 1, sample은 10장의 E1 공간 크기) 추가. 설정 decode가 strict(`scheduler_service.go:118`)이므로 배포 때 `serving-scheduler` ConfigMap과 Dashboard 설정 화면을 같이 바꾼다 |
| scheduler 설정(commit 5) | `algorithm`, `maxCandidates` 삭제. 같은 방식으로 ConfigMap과 Dashboard를 바꾼다 |
| scheduler stream | shadow 기록 종류를 더한다(commit 2). 전환에서 판단 기록이 `analytic`을 담고 shadow 기록, `search.truncated`, 두 설정 필드가 빠진다(commit 5). 관측 reader는 없는 필드를 빈 값으로 읽으므로 버전을 올리지 않는다(ADR 0019 "a bump that no reader needs is pure cost") |

## 13. 커밋 단위

| # | commit | 내용 | 의존 |
|---|---|---|---|
| 1 | `feat(serving): add analytic capacity planner` | `internal/serving/scheduler_analytic.go`: 용량표, 공유 상한, 분석형 평가, 노드별 사슬 표, 공간 세기와 전수와 beam, 비교 기간, 상위 K 검증 함수. firstfloor `serviceCost` 내보내기. 연결하지 않는다 | 없음 |
| 2 | `feat(serving): record analytic planner decisions in shadow` | launch 뒤 비동기 shadow, shadow 기록, 세 설정과 Dashboard 설정 행, sample 설정. shadow 결과는 기존 판단 조회 API와 오프라인 표로 본다 | 1 |
| 3 | `chore(hack): add offline planner A/B` | `hack/planner-ab/`, `ReduceSchedulerArrivals` 분리, `hack/README.md` 한 줄 | 1 |
| 4 | `docs(spec): judge SLO with a conservative latency bound` | 11장의 ①과 ④(SRS). ①'는 로컬 사본에 같은 문장 | 사용자 승인 |
| 5 | `feat(serving): plan analytically and verify the top candidates by replay` | `EvaluateScheduler`를 분석형과 검증으로 교체, 16장 삭제(`projected-work-history` 포함), ConfigMap과 Dashboard, 문서(scheduler-implementation, workload-pools 3.4절 horizon 문단, architecture-rethink 02 상태) | 2, 3의 A/B 통과, 15장 shadow 통과, 4 |

## 14. 검사

| commit | 검사 |
|---|---|
| 1 용량 | 3장 표의 rasp5 q1 행(b 1, μ 28.8)을 fixture profile로 재현. 공유 인스턴스: SLO가 다른 두 Workload(p95 1 s와 p90 1.5 s)와 배치 크기가 다른 두 profile에서 느린 공유 Workload의 배치가 빠른 Workload의 상한에 들어감, `n`에 따른 q, 독립 표본을 섞은 Monte Carlo에서 상한을 넘는 비율이 `1 − p` 이하. 표본 부족, `drop`, `defer`, 고정 배치는 `ReplayRequired`이고 충족으로 표시되지 않음. 사용률이 ρ를 넘으면 초과이고 재생 생략 |
| 1 탐색 | 상한 이하의 무작위 소형 fixture에서 전수 결과가 test 안의 brute force 최소와 같음. beam 폭이 공간 크기 이상이면 전수와 같음. 한 단계 교체는 둘 다 나빠지고 두 인스턴스 교체만 나은 fixture를 전수와 beam이 모두 찾음. option 40개에서 재진입 구성이 순위에 있음(D-22). 공간 세기가 상한 + 1에서 멈춤 |
| 1 기간과 결정성 | 사슬 197 s, H 60 s에서 keep과 모든 후보의 T가 197 s 이상으로 같고 power와 prepare trial J 전체 포함, `TransitionExceedsHorizon` 없음. 검증에서 1위가 재생 초과면 다음 후보 선택. 완전 동률은 keep. option과 인스턴스 순서를 섞은 두 입력의 판단이 `reflect.DeepEqual` |
| 2 | shadow 유무에서 기록과 launch의 순서와 내용이 같음. Tick context 취소가 shadow를 멈추지 않고 shadow가 controller 상태를 바꾸지 않음(`-race`). 앞선 shadow가 돌 때 건너뜀. 일치 판정이 `SchedulerLogicalConfigKey` 기준이고 불일치 때만 실행 target 재생. shadow 기록 decode 왕복. strict 설정 decode가 세 필드 누락을 거절. `make dashboard-check` |
| 3 | fixture `v1alpha5` 시간 디렉터리 하나에서 판단 행 생성. `ReduceSchedulerArrivals`가 같은 파일의 `LoadWindow` 도착과 같음 |
| 5 | 재진입 horizon test(`scheduler_pools_test.go:335-370`)를 6장 규칙으로 다시 씀. 남는 재생 test는 그대로 |
| 전체 | `make lint test`, `make arch-check`(hack 새 패키지), `make helm-lint helm-template` |

## 15. E 확인

| 확인 | 방법 | 목표 |
|---|---|---|
| 오프라인 A/B | 10장 | 10장 기준. 일치율, 재생 J 차이 분포, `optimistic`, `replayRequired` 비율, 공간 크기, 계획기별 시간 |
| shadow | commit 2 배포 뒤 E1 순서(0.633, 2, 5 req/s 각 5분, soak 2 req/s 1시간), tight 자동제어, 운영 설정 그대로 | `agree` 또는 delta ≤ 5%인 판단 ≥ 95%, `optimistic` 0, shadow 시간 p99와 `skipped` 기록, Tick p99가 E1 값보다 늘지 않음 |
| 전환 Tick | commit 5 배포 뒤 같은 순서 | 5 req/s Tick p99 ≤ 2 s, CPU가 500m 아래, Tick 간격 최대 ≤ 15 s |
| 에너지 | 1시간 비교 집합 예측(재생 J)과 `OperatingPower` 실측 | 차이 ≤ 2%(E1 +0.7%) |
| SLO | E1 단계 표(`hack/observations/e1.sql`) | 단계마다 충족. 창마다 예측 백분위 ≥ 관측 백분위인 창 ≥ 95% |
| 재진입 | 가용 노드와 비용이 있는 판단의 `analytic` | `TransitionExceedsHorizon` 0, 재진입 구성이 매 판단 평가됨, T와 고른 경우 사슬 전체의 J |

## 16. 전환 뒤 삭제

commit 5에서 모두 지운다.

- `scheduler_candidates.go`: `EnumerateScheduler`와 깊이 우선 열거(`visit`, `place`, `emit`, `group`, `replicaLimit`, `sourceIndex`, `candidateNodes`, `canReuseSlot`, `routing`, `controlInstances`, `controlRouting`), route 변형, `Search.Truncated`. 공간 열거는 `optionAllowed`, `inActivePool`, `schedulerMeasuredOptions`, `schedulerPlan`을 다시 쓴다.
- `scheduler_replay.go`: 모든 후보 재생 루프(`EvaluateScheduler` 본문), `transitionComplete`, `TransitionExceedsHorizon`과 그 assumption 문장, `schedulerReplayModel`의 `projection` 인자.
- `projected-work-history`: `scheduler_policy.go`, `scheduler_projection.go`, `kube/scheduler.go`의 `workInterval`과 `applyWorkPolicy`, `RetainedLogicalConfigurationsExceedMaxCandidates`와 다른 work-history 사유 코드, test(`scheduler_policy_test.go`, `scheduler_projection_test.go`, `kube/scheduler_policy_test.go`), Dashboard의 algorithm 선택.
- 설정: `algorithm`, `maxCandidates`와 검증, Dashboard의 두 행과 타입(`scheduler-settings.tsx`, `read-types.ts`, `scheduler.test.ts`).
- shadow goroutine과 shadow 기록 writer.
- 열거 상한과 절단을 검사하는 test, `TransitionExceedsHorizon` test.
- 문서: [스케줄러 구현 설계](scheduler-implementation.md)의 후보 열거, horizon, algorithm 문단과 [workload-pools 3.4절](workload-pools.md#34-전체-pool-가용-노드와-전력모드)의 horizon 문단을 이 설계로 다시 쓴다.

## 17. 사용자 결정과 질문

- 결정: horizon은 60 s로 두고, 판단마다 비교 기간 하나 `T = max(H, 최장 사슬)`을 keep과 모든 후보에 쓴다(6장). 기록하는 예상 에너지는 재생 값이다(7장). 공유 상한은 union bound이고, 상한을 쓸 수 없는 후보는 재생으로만 판정한다(3장). 공간은 상한 이하에서 전수, 넘으면 beam이다(5장). shadow는 launch 뒤 비동기로 돈다(9장). `projected-work-history`는 전환에서 지운다. A/B 입력은 `v1alpha5`와 shadow 기록이다.
- 질문 1: 11장 ①, ①', ④ 문장을 승인하는가. ①'의 04와 00은 로컬 사본이라 누가 반영하는가.
- 질문 2: 비용이 있는 가용 노드가 있으면 T가 그 사슬(orin-nano면 197.1 s 이상)로 늘어나 모든 후보의 Serving 시간이 길어진다. 이 결과로 전환이 지금보다 쉽게 이득으로 보일 수 있다. 받아들이는가.
- 질문 3: ρ 0.8과 K 3으로 시작하는가. ρ는 shadow의 `optimistic`과 창별 백분위 비교로 조정한다.
- 질문 4: union bound로 단일 Workload도 각 항을 p97.5로 잡아 지금 재생보다 보수적이다. 과대 프로비저닝 에너지는 shadow의 delta로 잰다. 받아들이는가.
