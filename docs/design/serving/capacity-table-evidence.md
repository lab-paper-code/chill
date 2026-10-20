# 인스턴스 용량표 근거 (M4 재설계 시제품)

M4 재설계의 두 부품을 E1 입력으로 잰 결과다. 하나는 option과 SLO마다 인스턴스 하나가 SLO 백분위를 지키는 최대 요청률 κ를 기존 재생으로 미리 구한 표이고, 다른 하나는 Tick에서 κ 합과 노드별 DP로 구성을 고르는 계획기다. 도구는 `hack/kappa`이고 `serving.EvaluateScheduler`를 그대로 부른다(재생 코드 복사 없음). 클러스터 변경은 없다.

입력은 `~/.cache/chill-assets/ops/pools-deploy/` 아래 E1 사본이다.

| 입력 | 내용 |
|---|---|
| `e1/scheduler-pre.json`, `e1/profiles-pre.json` | option 19개(배치 변형 38개), profile 19개, Workload 3개 |
| `e1r/records.jsonl` | E1(2026-10-19 20:10~21:33) 판단 행 498개와 snapshot. `e1/scheduler-records.jsonl`은 전력 행 14,380개와 설정 행 6개, 동작 행 2개만 담고 판단 행이 없다. `v1alpha5` 시간 세그먼트는 로컬에 없다 |
| `/tmp/sft/busy_requests.jsonl` | E1 2 req/s 창(21:30:50~21:31:50) 요청 행 121건 |

## 1. κ 정의와 계산

- 행 키: option(노드, 모드, 할당, profile), 배치 상한 b, Workload SLO(백분위 p, 목표 L), 완료 여유 m. deadlinePolicy는 profile이 정한다(E1은 모두 `keep`).
- 판정: option 인스턴스 하나만 있는 keep snapshot에 포아송 도착을 넣고 `EvaluateScheduler`(반복 1회, 자동제어 꺼짐)의 SLO 추정을 읽는다. 백분위 지연(m 포함) ≤ L이고 timeout과 만료가 0이면 그 창은 충족이다.
- 창: 빈 상태에서 시작하는 요청 `max(1000, 100/(1 − p))`개(p95 2,000개, p99 10,000개). seed 1~20의 독립 창 20개 중 19개 이상이 충족하면 그 요청률은 충족이다.
- κ: `[0.01μ, μ]`에서 0.5% μ까지 이분 탐색한다. μ는 b 이하 배치 크기의 `b / 평균 실행시간` 최댓값이다. 같은 seed의 도착 간격을 요청률로 나눠 쓰므로(공통 난수) 결과가 결정적이다.
- 다시 계산하는 때: profile, SLO, m이 바뀔 때.

## 2. 용량표 (m 139 ms, 현재 live 설정)

`GOMAXPROCS=1 go run ./hack/kappa -mode table -scheduler e1/scheduler-pre.json -margin 139`. 38개 변형 × SLO 3개 = 114행, 단일 코어 1,626 s(변형당 22~87 s).

| option | b | μ | κ relaxed (p90 1.5 s) | κ tight (p95 1 s) |
|---|---|---|---|---|
| rasp5 q1 | 1 / 2 | 36.0 | 35.8 | 34.3 |
| rasp5 q1-lut | 1 / 2 | 36.4 | 36.3 | 34.9 |
| rasp5 q2 | 1 / 2 | 43.7 | 43.7 / 42.5 | 41.8 / 41.0 |
| jetsonx 15W_6CORE | 1 / 2 | 20.3 / 21.3 | 19.3 / 19.2 | 18.3 |
| jetsono MAXN | 1 / 2 | 49.1 / 51.1 | 49.1 / 51.1 | 48.1 / 49.7 |
| jetson-nano MAXN | 1 / 2 | 13.1 / 13.5 | 12.0 / 12.3 | 11.1 / 11.6 |
| orin-nano cuda q1 | 1 / 2 | 40.3 / 44.7 | 40.3 / 44.2 | 38.9 / 41.3 |
| orin-nano cuda q2 | 1 / 2 | 38.1 / 41.0 | 38.1 / 40.2 | 35.3 / 38.8 |
| lattepanda q1 | 1 / 2 | 35.9 / 36.6 | 35.6 | 34.1 / 34.2 |
| lattepanda q2 | 1 / 2 | 47.9 / 49.9 | 47.9 / 49.9 | 46.6 / 48.7 |
| orangepi q1 | 1 / 2 | 10.0 | 8.9 | 8.1 |
| genesis2 cpu q1 | 1 / 2 | 104.1 / 107.3 | = μ | = μ |
| genesis2 cuda q1 | 1 / 2 | 138.8 / 145.4 | = μ | = μ |
| genesis3 cpu q1 / q2 | 1 / 2 | 97.5~116.0 | = μ | = μ |
| genesis1 (4개, 배치 불가 노드) | 1 / 2 | 95.0~129.8 | = μ | μ 또는 μ의 96~97% |

- imagenet(p99 100 ms)은 114행 모두 κ 0이다. m 139 ms가 목표 100 ms보다 크다.
- E1 판단의 m 50 ms로 만든 tight 표(비교에 씀)는 38행 1,022.6 s이고 값은 위와 1 req/s 이내다(rasp5 q1 34.6, genesis3 q2 110.9/116.0).
- 비용의 대부분은 재생의 `nextEvent`가 사건마다 모든 요청을 훑는 데서 온다. 창 하나가 2,000 요청이면 0.19 s, 4,000이면 0.7 s, 8,000이면 2.8 s다(rasp5, 단일 코어).

## 3. κ 검증

`-mode validate`: κ의 0.8, 0.9, 1.0, 1.2배에서 이분 탐색에 쓰지 않은 seed 21~40의 창 20개를 재생한다. 칸은 SLO 충족 창 수 / 20이고 괄호는 백분위 지연의 중앙값(ms)이다.

| option | SLO | κ | 0.8κ | 0.9κ | 1.0κ | 1.2κ |
|---|---|---|---|---|---|---|
| rasp5 q1 b1 | tight | 34.3 | 20 (308) | 20 (392) | 16 (631) | 0 (6,473) |
| rasp5 q1 b1 | relaxed | 35.8 | 20 (295) | 20 (393) | 20 (710) | 0 (4,296) |
| jetsonx b2 | tight | 18.3 | 20 (399) | 20 (502) | 17 (732) | 0 (7,158) |
| jetsonx b2 | relaxed | 19.2 | 20 (374) | 20 (488) | 20 (829) | 0 (5,665) |
| orangepi q1 b1 | tight | 8.1 | 20 (567) | 20 (679) | 16 (870) | 0 (2,682) |
| orangepi q1 b1 | relaxed | 8.9 | 20 (548) | 20 (704) | 18 (1,040) | 0 (6,702) |
| genesis2 cuda b2 | tight | 145.4 | 20 (195) | 20 (224) | 20 (395) | 0 (2,666) |
| genesis2 cuda b2 | relaxed | 145.4 | 20 (185) | 20 (216) | 20 (309) | **20** (1,191) |
| genesis2 cuda b2, 창 60 s 이상 | tight | 144.3 | 20 (197) | 20 (240) | 17 (622) | 0 (13,949) |
| genesis2 cuda b2, 창 60 s 이상 | relaxed | 145.4 | 20 (186) | 20 (228) | 18 (735) | 0 (13,724) |

- κ는 절벽의 무릎이다. 0.9κ까지 모든 창이 충족하고 백분위 지연 중앙값은 목표의 20~70%다. 1.0κ에서 충족 창은 80~100%이고, 1.2κ에서 0%다.
- 창 하나로 정한 κ(`-seeds 1`)는 무릎보다 높다. 1.0κ 충족 창이 rasp5 tight 9/20, jetsonx tight 10/20, jetsonx relaxed 6/20, orangepi relaxed 12/20이다. 20개 중 19개 기준이 이것을 고친다.
- 빠른 option(genesis 계열)은 2,000 요청 창이 14~21 s라 μ에서도 큐가 목표를 넘을 만큼 자라지 않고 κ가 μ에 붙는다. genesis2 cuda relaxed가 1.2κ에서 20/20 충족하는 것이 그 증거다. 창을 60 s 도착 이상으로 늘리면(`-window 60`) κ는 거의 같고(144.3~145.4) 1.2κ는 0/20이 된다. 그 대신 창 하나가 8,700 요청이 되어 option 하나의 검증에 2,135 s가 들었다.

## 4. rasp5 profile과 실측

E1 2 req/s 창의 rasp5 q1 요청 121건을 요청 행의 시각으로 나눴다.

| 구간 | p50 (ms) | p95 (ms) |
|---|---|---|
| received → validated (ChillServer 검증과 전처리) | 14.7 | 103.2 |
| assigned → backend_queued (전달) | 7.2 | 13.7 |
| backend_running → 첫 backend_finished (native 실행) | 30.9 | **32.8** |
| backend_running → backend_exchange_finished | 32.0 | **86.8** |
| 종단 (response_finished) | 55.1 | 209.7 |

- profile b1 실행시간은 p50 27.9 ms, p95 30.9 ms(표본 903개)다. 실측 native 실행 p95 32.8 ms는 profile보다 6% 길다. 86.8 ms는 실행 뒤 응답 교환까지 포함한 구간이다.
- 같은 부하의 재생 예측 p95는 m 50(E1 설정)에서 85.2 ms, m 139에서 174.2 ms다. 실측 209.7 ms보다 각각 124 ms, 36 ms 낮다. 지연 예측의 낙관은 실행 밖 구간(검증과 전처리 p95 103 ms, 응답 교환)이 m보다 길어서 생긴다.
- κ는 m에 둔감하다. tight κ는 m 139에서 34.3, m 179(실측 종단 p95 − profile p95)에서 34.2다. 무릎에서는 큐 대기가 지연을 정하기 때문이다.
- κ는 인스턴스 점유 시간에 비례해 줄어든다. 실행 6% 차이는 κ를 약 6% 낮춘다 `[INFERENCE]`. 응답 교환 구간까지 인스턴스를 점유한다면(실행시간 × 2.818) tight κ는 10.8, relaxed κ는 11.7로 profile 값의 1/3이 되고, 그 κ도 검증에서 같은 모양(0.9κ 20/20, 1.0κ 17/20, 1.2κ 0/20)이다. 교환 구간 동안 firstfloor가 다음 배치를 시작하는지는 이 자료로 가릴 수 없다 `[INFERENCE: 2 req/s에서 backend_queued → backend_running p95가 0.1 ms라 대기가 없었다]`.

## 5. 기록 판단과 비교

계획기(`-mode compare`)는 Tick 안에서 재생하지 않는다.

- 부하 `λ̂ = (창의 계산 도착 수 + 대기 수) / 창 길이`.
- 노드 계획: 노드마다 keep, 그리고 배치 가능한 노드에서 측정 option 변형(option마다 하나 이하)의 집합 중 한 모드와 `ValidateNodeBudget`을 만족하는 것. 계획마다 κ 합, 비교 기간 T의 노드 에너지(baseline 또는 profile 적재 유휴 전력, 비용표 trial 평균 wall J), 사슬 길이를 둔다. 사슬 순서는 `schedulerPlan`과 같다(power, 자원을 비우는 remove, prepare, route, 나머지 remove).
- T = max(60 s, 공간의 최장 route 전 사슬 + 최장 route 뒤 remove).
- 선택: 서로 다른 route 시각 r마다 노드 순서 DP(부하 50칸, 상태에 "사슬 하나가 r에서 끝남" 표시)를 돌려 κ 합 ≥ λ̂인 해 중 전환 대기 `max(0, λ̂ − keep κ) × r`가 가장 작고 그다음 에너지가 가장 작은 해를 고른다. 완전 동률은 keep이다. 부하는 e가 작은 인스턴스부터 κ까지 채운다. 무작위 소형 fixture 600개에서 전수 결과와 같다(`go test ./hack/kappa`).

E1 판단(κ는 m 50 tight 표, `GOMAXPROCS=1`):

| 항목 | 값 |
|---|---|
| 판단 행 | 498. snapshot 사유로 모든 후보가 막힌 5행은 두 계획기 모두 판단이 없다 |
| target이 있는 판단 | 457(첫 구성 1, keep 456). **일치 457/457**, 에너지 차 0 J |
| target이 없는 판단 | 36(당시 리비전의 `NativeOwnershipPending`). κ 계획기는 rasp5 q1 keep |
| 첫 구성(λ̂ 0) | 두 계획기 모두 rasp5 q1 b1(route 19.3 s) |
| Tick 시간 | p50 1.84 ms, p99 3.02 ms, 최대 3.27 ms. 노드 계획 37개 |
| T | 493행 모두 60 s. jetsonx와 jetsono 재진입은 power 비용이 없고 orin-nano는 배치할 수 없어 60 s를 넘는 사슬이 공간에 없다 |

E1 부하(≤ 5 req/s)는 rasp5 κ의 15% 이하라 이 일치는 계획기를 가르지 못한다. 그래서 마지막 keep snapshot(21:33:52, rasp5 q1)의 도착을 포아송으로 바꿔 재생 계획기(`EvaluateScheduler`, 기록 설정 `replays` 10, `maxCandidates` 32)와 나란히 돌렸다(`-sweep`).

| 부하 (req/s) | κ 계획기 | 재생 계획기 | 재생 상태 | 재생 시간 (단일 코어) |
|---|---|---|---|---|
| 5 | keep | keep | Keep | 3.8 s |
| 20 | keep | keep | Keep | 59.2 s |
| 34 | keep | keep | Keep | 204.1 s |
| 40 | rasp5 q1 유지 + genesis3 q2 b1 (route 10.7 s) | genesis3 q2 b1, rasp5 제거 | Overload | 379.1 s |
| 60 | rasp5 q1 유지 + genesis3 q2 b1 (route 10.7 s) | genesis3 q2 b2, rasp5 제거 | Overload | 1,539.9 s |

- 40과 60 req/s에서는 어떤 전환도 prepare 동안 rasp5 혼자 과부하이므로 재생 계획기는 모든 후보를 초과로 보고 최악 초과량이 가장 작은 후보를 고른다. 그 후보는 prepare가 가장 짧은 genesis3 q2(10.7 s)이고, κ 계획기도 전환 대기 순서로 같은 노드와 option을 고른다(배치 상한은 60 req/s에서 κ 계획기 b1, 재생 계획기 b2). 다른 점은 rasp5 유지 여부다. κ 계획기는 rasp5 q1(e 0.071 J)에 κ까지 부하를 두고 나머지를 genesis3 q2(e 0.323 J)에 두는 쪽이 에너지가 작다고 본다. 재생 계획기의 후보 32개에는 그 구성의 재생 에너지가 없다.
- 전환 대기 항을 빼고 에너지만으로 고르면 40과 60 req/s에서 rasp5 q1 + q1-lut(route 20.2 s)가 나오고, 재생 에너지로는 그 구성이 target보다 590.6 J, 908.1 J 낮지만 전환 중 SLO에서 진다. 전환 대기 항이 필요하다는 근거다.
- 재생 열은 seed가 고정된 결정적 값이다. 60 req/s 행의 재생 열은 같은 도구의 앞선 실행 값이다(재생 쪽 코드는 같다).

## 6. 판단

진행을 권한다. 근거: Tick이 재생 없이 3 ms 이내이고(재생 계획기는 같은 snapshot에서 4 s~1,540 s), 기록 판단과 모두 일치하며, 부하를 올린 snapshot에서도 재생 계획기가 고른 노드와 option을 같은 순서 규칙으로 고른다. κ는 같은 재생 코드로 구하므로 지연 공식과 ρ가 없다.

analytic-planner.md를 다시 쓸 때 바꿀 것:

| 장 | 내용 |
|---|---|
| 3 용량표 | union bound, `l_q`, ρ를 지우고 1장의 κ 정의로 바꾼다. 창은 요청 `max(1000, 100/(1 − p))`개 **그리고** 60 s 도착 이상(관측 창)이어야 빠른 option의 κ가 μ에 붙지 않는다. 충족 기준은 독립 창 20개 중 19개다. 이 셋(창 길이, 창 수, 충족 비율)은 판정을 정하는 값이므로 설정에 두고 표 행에 기록한다(AGENTS.md). 표 행: profile ID, b, p, L, m, deadlinePolicy, κ, μ, 창 설정, seed, 계산 시간 |
| 3 계산 비용 | 60 s 창은 빠른 option에서 창 하나 8,700 요청이고 지금 재생은 요청 수의 제곱으로 느려진다. 표를 자주 다시 만들려면 재생의 사건 선택을 heap으로 바꾸거나(이미 `ponytail` 주석이 그 상한을 적어 둠) option별 병렬로 만든다. 표는 Tick 밖이다 |
| 4 평가 | 최종 SLO는 κ 합 ≥ λ̂. 전환 중은 make-before-break로 keep κ가 route까지 남는다고 보고 전환 대기 `max(0, λ̂ − keep κ) × r`를 SLO 다음 순서로 둔다. 모드 변경처럼 route 전에 인스턴스를 지우는 계획은 남는 κ가 줄어든다(시제품은 이 감소를 넣지 않았다). 에너지는 노드 baseline 또는 profile 적재 유휴 + 비용표 trial 평균 + λ × e이고 재생 기대값이 아니다. 기록하는 `ExpectedJoules`가 분석 합이 되므로 E1의 예측 정확도(+0.7%)는 다시 재야 한다 |
| 4 가중치 | router 가중치는 DP가 준 인스턴스별 배정 부하(e가 작은 인스턴스부터 κ까지) |
| 5 탐색 | 공간 세기, 전수와 beam, `exhaustiveConfigurations`를 지우고 노드 계획 + route 시각별 DP로 바꾼다. 같은 노드에서 같은 option 두 개는 CPU affinity가 겹쳐 생기지 않는다. 같은 인스턴스의 배치 상한 변경을 교체로 셌다(실제로 재준비가 필요한지 정해야 한다) |
| 6 비교 기간 | 유지. E1 자료에서는 T가 항상 60 s라 긴 사슬의 효과는 검증되지 않았다. jetsonx와 jetsono power 비용이 생긴 뒤 다시 본다 |
| 7, 9, 10 | 상위 K 재생, shadow, `hack/planner-ab`를 지운다. 오프라인 비교는 `hack/kappa -mode compare`(입력을 `v1alpha5` 세그먼트로 바꿈)이고, 확인 실험은 rasp5 κ를 넘는 포아송 부하 단계(예: 40 req/s)를 포함해야 한다. E1 부하는 κ의 15% 이하라 판단을 가르지 못한다 |
| 11 명세 | `SCH-13`은 "후보의 SLO 충족은 실행시간 분포와 firstfloor의 배칭과 큐 선택 규칙을 적용한 인스턴스 재생으로 미리 구한 option별 SLO 용량(κ)과 현재 요청률과 대기량으로 판정한다"로, `SCH-23`은 "actual과 용량 계산 재생"으로. "보수적 지연 상한" 문장은 쓰지 않는다 |
| 12 설정 | `capacityHeadroom`, `verifiedCandidates`, `exhaustiveConfigurations`를 넣지 않는다. 용량표 창 설정 셋을 넣는다 |
| 완료 여유 m | κ에는 거의 영향이 없지만 지연 예측에는 그대로 더해진다. E1 실측 실행 밖 구간 p95가 약 177 ms라 m 139도 예측을 36 ms 낮춘다. m을 관측으로 정하는 규칙(실행 밖 구간의 SLO 백분위)을 함께 적는다 |
