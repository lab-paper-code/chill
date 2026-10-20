# 용량표 계획기 (M4)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M4 설계다. 계획기는 두 부분이다. 첫째, option과 Workload SLO마다 인스턴스 하나가 SLO를 지키는 최대 요청률 κ를 기존 재생으로 미리 구한 용량표다. 프로파일, SLO, 완료 여유, 용량표 설정이 바뀔 때만 다시 계산한다. 둘째, Tick은 재생 없이 κ 합, 노드 계획, route 시각별 DP로 구성을 고른다. 근거와 시제품은 [인스턴스 용량표 근거](capacity-table-evidence.md)와 `hack/kappa`다. 사용자가 명세 변경 ①, ①', ④(10장)를 승인하면 전환한다. 명세 변경 ②와 ③은 이미 반영되어 있다. [M0](decision-record-diet.md)와 [M1](observability-split.md)을 전제로 한다.

## 1. 측정한 문제

| 문제 | 근거 |
|---|---|
| Tick이 부하에 비례해 500m CPU limit을 채운다 | `replays` 10, `maxCandidates` 32에서 Tick p50이 0.633 req/s 913 ms, 2 req/s 3,475 ms, 5 req/s 7,495 ms, 최대 22,575 ms, CPU 499m(D-16). 기본값 100과 128에서는 Tick 133회가 30 s deadline으로 실패했다(D-1). 판단마다 후보 20개를 10회씩 재생하고, 재생은 사건마다 모든 요청을 훑고(`nextEvent`) 배치마다 O(대기 요청² × 배치 행)인 `firstfloor.ChooseBatch`를 부른다 |
| 재진입 후보에 닿지 못한다(D-22) | 인스턴스가 생긴 뒤 판단 128건 중 127건이 32에서 잘렸고 재진입 후보는 37~48번째였다. 깊이 우선 열거가 노드 UID 순이라 앞 노드의 option과 route 변형이 상한을 쓴다 |
| 재진입 사슬이 horizon을 넘는다 | 빈 노드 전환은 jetsonx 63.5 s, jetsono 134.5 s, orin-nano 197.1 s(settling 60 s 포함, D-20, D-21)이고, horizon 60 s를 넘는 후보는 `TransitionExceedsHorizon`이다. 사용자는 horizon 60 s를 유지하고 M4에서 푸는 것을 골랐다 |
| 지연 공식은 상한이 되지 못한다 | 배치 분위수를 더한 지연 상한(여유율 0.8)에 대해 외부 검토의 포아송 재생에서 실제 p95가 그 값의 1.8~3.6배였다. κ는 공식 대신 같은 재생으로 구한다 |
| 지킬 것 | 에너지 예측 E1 1시간 499,333 J 대 실측 495,741 J(+0.7%), 결정성(정렬된 입력, 고정 seed, 완전 동률은 keep) |

## 2. 용량표

**행.** 키는 option(노드, 모드, 할당, profile ID), 배치 상한 b, SLO(백분위 p, 목표 L), 완료 여유 m, 용량표 설정이다. deadlinePolicy는 profile이 정한다. 값은 κ, μ(b 이하 배치의 `b / 평균 실행시간` 최댓값), 창 설정, 창 seed 범위, 창별 백분위 지연, 계산 시간이다.

**판정.** option 인스턴스 하나만 있는 keep snapshot에 요청률 λ의 포아송 도착을 넣고 `EvaluateScheduler`(반복 1회, 자동제어 꺼짐)의 SLO 추정을 읽는다. 백분위 지연(m 포함) ≤ L이고 timeout과 만료가 0이면 그 창은 충족이다. 창 하나는 빈 상태에서 시작하고 요청 수는 `max(capacityMinimumRequests, ⌈100/(1 − p)⌉, ⌈λ × capacityWindow⌉)`다. 100은 백분위 위에 표본 100개를 두는 고정 규칙이다. seed 1~`capacityWindows`의 독립 창 중 `capacityPassingWindows`개 이상이 충족하면 λ는 충족이다.

**κ.** `[0.01μ, μ]`에서 0.5% μ까지 이분 탐색한다. seed의 도착 간격을 λ로 나눠 쓰므로(공통 난수) 결과가 결정적이다. 0.01μ가 충족하지 못하면 κ는 0이다.

**설정.** `capacityWindow` 60 s, `capacityWindows` 20, `capacityPassingWindows` 19, `capacityMinimumRequests` 1,000. 60 s 창이 필요한 근거: 요청 수 창만 쓰면 빠른 option(genesis2 cuda, 2,000 요청이 14 s)의 κ가 μ에 붙어 relaxed 1.2κ에서 20/20 충족이었고, 60 s 창에서는 0/20이다. 20개 중 19개 기준이 필요한 근거: 창 하나로 정한 κ는 1.0κ에서 6~12/20만 충족했다.

**검증된 모양.** 이분 탐색에 쓰지 않은 seed 21~40에서 0.9κ는 모든 option이 20/20, 1.0κ는 16~20/20, 1.2κ는 0/20이다. κ는 절벽의 무릎이다. E1 tight(p95 1 s, m 139 ms) κ: rasp5 q1 34.3, jetsonx 18.3, orangepi 8.1, jetsono 48.1~49.7, orin-nano 38.9~41.3 req/s.

**만드는 곳과 비용.**
- ChillServer의 용량표 worker가 Workload의 SLO가 정해지거나 profile, m, 용량표 설정이 바뀔 때 빠진 행을 만든다. 자동제어를 켜기 전에 만들어지므로 켤 때 기다리지 않는다. 행 하나가 작업 하나이고 `GOMAXPROCS`개를 병렬로 돌린다.
- 행은 evidence store에 `serving/capacity/<profileID>/<b>/<p>-<L>-<m>/<설정>.json`으로 `PutImmutable`하고, 재시작 뒤 읽는다. 키가 입력을 모두 담으므로 같은 키는 같은 값이다.
- 행이 없으면 그 option은 `CapacityPending`으로 계획에서 빠지고, 켜진 Workload에 κ가 있는 option이 하나도 없으면 판단이 `CapacityPending`이다.
- 지금 재생은 요청 수의 제곱으로 느려진다(창 2,000 요청 0.19 s, 8,000 요청 2.8 s). 60 s 창은 빠른 option에서 8,700 요청이고 option 하나의 검증에 2,135 s가 들었다. 그래서 commit 1이 재생의 사건 선택을 heap으로 바꾼다. 행 수는 E1에서 option 변형 38개 × SLO 3개 = 114개다. 단일 코어 2,000 요청 창으로 1,626 s였다.

**imagenet.** imagenet(p99 100 ms)은 114행 모두 κ 0이다. m 139 ms가 목표 100 ms보다 크기 때문이다. 이 Workload를 켜면 판단은 `NoCapacity`다.

## 3. 판정과 순서

- 부하: `λ_w = (관측 창의 계산 도착 수 + 대기 수) / 창 길이`. 도착은 재생이 쓰던 `Compute` 또는 `Accepted` 도착이다.
- SLO: 구성 c는 켜진 Workload마다 `Σ_{i∈c, w} κ_i ≥ λ_w`이면 충족이다. λ가 0이어도 켜진 Workload마다 인스턴스 하나 이상이다.
- 전환 대기: route 시각 r까지 새 용량이 없다(make-before-break). 그때까지 남는 용량은 route 뒤에 지우는 keep 인스턴스의 κ다. 모드를 바꾸는 노드와 자원이 겹쳐 prepare 전에 인스턴스를 지우는 노드는 그 노드의 keep κ를 잃는다. 전환 대기는 `Σ_w max(0, λ_w − 남는 κ_w) × r`(요청 수)다.
- 순서: 충족, 전환 대기가 작은 것, 에너지가 작은 것, keep, 논리 구성 key 사전 순. 충족하는 구성이 없으면 채운 부하가 큰 것을 먼저 두고 상태는 `Overload`다.
- 전환 대기 항이 필요한 근거: 마지막 E1 snapshot에 포아송 40과 60 req/s를 넣으면 에너지만으로는 rasp5 q1 + q1-lut(route 20.2 s)가 나오고, 재생에서는 그 구성이 전환 중 SLO에서 진다. 전환 대기 순서로는 재생 계획기와 같은 genesis3 q2(route 10.7 s)를 고른다.

## 4. 노드 계획과 DP

**노드 계획.** 노드마다 keep, 그리고 배치 가능한 노드에서 측정 option 변형(option마다 변형 하나 이하) 집합 중 한 모드와 `ValidateNodeBudget`을 만족하는 것. 같은 노드에서 같은 option 두 개는 CPU affinity가 겹쳐 생기지 않는다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이다. 계획마다 κ 합, 남는 κ, 사슬, 전력을 둔다.

**사슬.** `schedulerPlan`의 순서다. 모드 변경은 power, 모든 기존 remove, prepare가 route 전이다. 같은 모드에서는 자원이 함께 들어가지 않을 때만 remove가 prepare 앞이고, 아니면 route 뒤다. 동작마다 측정 trial의 평균 wall J, 평균 시간, 최대 시간을 쓴다. 비용이 없으면 그 계획은 `MissingOperationCost`로 빠진다. 같은 인스턴스의 배치 상한을 바꾸는 계획은 remove와 prepare로 센다(교체).

**비교 기간.** `T = max(horizon, 공간의 최장 route 전 사슬 + 최장 route 뒤 remove)`를 판단마다 하나 정하고, keep과 모든 계획에 쓴다. 최대 시간으로 잡으므로 모든 사슬이 T 안에 끝난다. T는 `decision.capacity.comparisonNanos`로 기록한다. 비교 기간을 정하는 방법은 04 명세가 설계에 맡긴다.

**에너지.** 노드 계획 하나의 [0, T] 에너지는 keep이면 `P_before × T`, 아니면 `J_pre + P_mid × (r − t_pre) + J_post + P_after × (T − r − t_post)`다. P는 `schedulerBaseline`의 적재 상태 전력(baseline 또는 profile 적재 유휴)이다. 요청 에너지는 인스턴스별 배정 부하 y와 요청당 증분 e로 `Σ y_i × e_i × T`다. 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**DP.** 서로 다른 route 시각 r마다 노드 순서 DP를 한 번 돈다. 상태는 (Workload별 채운 부하 칸, Workload별 남는 용량 칸, r에서 끝나는 사슬이 있는지)이고 값은 최소 에너지다. 부하는 칸 단위로 올림, κ는 내림이라 보수적이다. 칸 수는 상태 수가 20,000 이하가 되는 가장 큰 정수(최대 50)다. 부하는 e가 작은 인스턴스부터 κ까지 채운다. r마다 3장의 순서로 해를 비교한다.

**router 가중치.** 고른 해의 인스턴스별 배정 부하 y에 비례한 정수(합 100, 인스턴스마다 최소 1)다.

**판단 기록.** `ExpectedJoules`는 위 분석 합이다. `decision.capacity`는 T, λ, 고른 r, 전환 대기, κ 합, 노드 계획 수, r마다의 최선 해(key, κ 합, 전환 대기, J), 사용한 용량표 행 키를 담는다(`SCH-30`의 후보별 평가).

**비용.** 노드와 계획 수에 비례하고 요청 수와 무관하다. E1 기록 판단 498건에서 노드 계획 37개, Tick p50 1.84 ms, p99 3.02 ms(단일 코어)다.

## 5. 완료 여유 규칙 (제안)

κ는 m에 둔감하지만(rasp5 tight m 139 ms 34.3, m 179 ms 34.2) 지연 예측과 짧은 SLO의 κ는 m을 그대로 더한다. E1 2 req/s에서 rasp5 실측 종단 p95는 209.7 ms, 재생 예측은 m 139 ms에서 174.2 ms였다. 차이는 검증과 전처리(p95 103.2 ms)와 응답 교환이 m보다 긴 데서 온다. 제안: m은 요청 행의 `elapsedNanos − (queueMS + serviceMS)`(firstfloor 큐와 실행 밖 시간)의 SLO 백분위로 정한다. E1 값은 약 177 ms다. 운영자가 E 실행의 `hack/observations/e1.sql` 열로 확인하고 설정을 바꾼다. m이 바뀌면 2장의 행이 다시 만들어진다.

## 6. 범위 밖과 그대로

- 그대로: 재생 코드(용량표 계산과 오프라인 검증), `schedulerPlan`, `schedulerMeasuredOptions`, `schedulerBaseline`, 전환 실행기, 가용 노드 최저 모드 규칙, 비교 집합, firstfloor batcher.
- 공유 인스턴스: κ는 Workload 하나의 행이다. 계획기는 공유 인스턴스를 만들지 않고, 이미 있는 공유 인스턴스는 그 노드의 keep으로 둔다.

## 7. 설정과 저장 계약

| 대상 | 변경 |
|---|---|
| 추가(commit 2) | `capacityWindow`(> 0), `capacityWindows`(≥ 1), `capacityPassingWindows`(1 ≤ x ≤ `capacityWindows`), `capacityMinimumRequests`(≥ 1). 설정 decode가 strict(`scheduler_service.go:118`)라 배포 때 `serving-scheduler` ConfigMap과 Dashboard 설정 화면을 같이 바꾼다 |
| 삭제(commit 5) | `algorithm`, `replays`, `seed`, `maxCandidates`, `maxReplayRequests`. 같은 방식으로 ConfigMap과 Dashboard를 바꾼다 |
| 판단 기록 | `v1alpha5`에 `decision.capacity`가 더해지고 `candidates`, `search.truncated`, `policy`가 빠진다. 관측 reader는 없는 필드를 빈 값으로 읽으므로 버전을 올리지 않는다(ADR 0019) |
| evidence store | `serving/capacity/` prefix(새 object, 읽는 곳은 용량표 worker) |

## 8. 커밋 단위

| # | commit | 내용 | 의존 |
|---|---|---|---|
| 1 | `perf(serving): select replay events with a heap` | `nextEvent`의 선형 탐색을 heap으로. 기존 재생 test의 판단과 같은 값 | 없음 |
| 2 | `feat(serving): build per-option SLO capacity rows` | `internal/serving/capacity.go`(`hack/kappa`의 κ 계산 이동), 용량표 worker, evidence store 행, 네 설정, `CapacityPending`, `/api/v1/scheduler/capacity` 조회. Tick은 그대로 | 1 |
| 3 | `feat(loadgen): send Poisson arrivals` | `chill_loadgen replay --arrivals poisson --seed`. 지금은 고정 간격뿐이다(`cli.py:55-58`) | 없음 |
| 4 | `docs(spec): judge SLO with per-option capacity` | 10장의 ①과 ④(SRS). ①'는 로컬 사본에 같은 문장 | 사용자 승인 |
| 5 | `feat(serving): plan with capacity sums and node plans` | `internal/serving/scheduler_capacity.go`의 노드 계획, 사슬, T, DP, 가중치, `decision.capacity`. `EvaluateScheduler` 교체, 12장 삭제, `hack/kappa`의 compare가 서버 계획기와 `v1alpha5` 세그먼트를 쓰도록 변경, 설정 삭제, ConfigMap, Dashboard, 문서 | 2, 4, 11장 오프라인 비교 |

## 9. 검사

| commit | 검사 |
|---|---|
| 1 | heap 전후 재생 결과가 기존 scheduler test 전체에서 같음. 8,000 요청 창 시간 기록 |
| 2 | fixture profile의 κ가 같은 입력의 `hack/kappa` 값과 같음. 같은 행을 두 번 만들면 같은 값(결정성). 창 요청 수가 `λ × capacityWindow` 이상. 19/20 판정 경계. m 139 ms, p99 100 ms에서 κ 0. profile, SLO, m, 설정 변경이 새 키를 만들고 그 외 변경은 만들지 않음. 재시작 뒤 store에서 읽고 다시 계산하지 않음. 행 없음은 `CapacityPending`. strict decode가 네 필드 누락을 거절 |
| 3 | 같은 seed의 도착 시각이 같고 평균 간격이 1/rate 근처 |
| 5 DP | 무작위 소형 fixture에서 DP 결과가 brute force와 같음(Workload 1개와 2개, `hack/kappa` test의 600개를 옮김). 모드 변경 계획의 남는 κ 0과 그에 따른 순서. route 뒤 remove 계획은 keep κ를 남김. 배치 상한 변경이 remove와 prepare. 공유 인스턴스와 꺼진 Workload 노드는 keep뿐 |
| 5 기간과 기록 | 사슬 197 s, horizon 60 s에서 keep과 모든 계획의 T가 같고 trial J 전체 포함, `TransitionExceedsHorizon` 없음. option 40개에서 재진입 계획이 DP에 있음(D-22). 가중치 합 100, 최소 1, 배정 부하 비례. 입력 순서를 섞은 두 snapshot의 판단이 `reflect.DeepEqual`. 완전 동률은 keep |
| 전체 | `make lint test`, `make arch-check`, `make helm-lint helm-template`, `make dashboard-check` |

## 10. 명세 변경 제안

사용자 승인 전에는 효력이 없다. 원문은 713872d 기준이다.

**① `scheduler/SRS.md`**
- `SCH-13` 현재 첫 문장: "실행시간 분포에 현재 요청 도착·대기·진행 상태와 후보의 분배·배칭·큐 선택 규칙을 적용해 요청별 예상 완료 시각과 종단 지연을 계산한다."
- 제안: "실행시간 분포에 firstfloor의 배칭과 큐 선택 규칙을 적용한 인스턴스 재생으로 실행 조건과 SLO마다 SLO를 지키는 최대 요청률(용량)을 미리 계산하고, 후보의 SLO 충족은 그 용량의 합을 현재 요청률과 대기량에 비교해 판정한다."
- `SCH-13` 현재 끝 문장: "SLO 백분위는 계산한 요청별 종단 지연 분포에서 구한다."
- 제안: "용량 계산은 포아송 도착의 독립 창마다 요청별 종단 지연 분포에서 SLO 백분위를 구하고, 설정한 수 이상의 창이 SLO를 지키는 최대 요청률을 용량으로 둔다."
- `SCH-AC-09` 현재 기대 결과: "요청별 예상 완료와 종단 지연 분포를 계산해 SLO 평가"
- 제안: "인스턴스 재생으로 구한 용량과 현재 부하로 SLO 평가"

**①' 로컬 사본 `04_configuration_selection.md` "프로파일에서 요청 지연 계산", `00_measurement.md`**
- 04 현재 첫 문장: "프로파일의 실행시간 분포에 현재 요청 도착·큐·진행 상태와 후보 구성의 분배·배칭·큐 선택 규칙을 적용해 요청별 예상 완료 시각을 계산한다."
- 제안: "프로파일의 실행시간 분포에 배칭과 큐 선택 규칙을 적용한 인스턴스 재생으로 실행 조건과 SLO마다 SLO를 지키는 최대 요청률을 미리 계산하고, 후보 구성은 그 용량의 합과 현재 요청률과 대기량으로 판정한다."
- 04 현재: "이 계산을 위한 별도의 부하별 Serving 성능표는 요구하지 않고, 실제 SLO·에너지와 계산의 오차를 기존 종단 평가에서 확인한다."
- 제안: "용량은 프로파일에서 계산하므로 별도의 부하별 Serving 측정은 요구하지 않고, 실제 SLO와 에너지와 계산의 오차는 기존 종단 평가에서 확인한다."
- 00 현재: "…스케줄러는 현재 도착·대기·진행 상태와 후보 구성의 처리 규칙으로 요청별 완료 시각을 계산한다."
- 제안: "…스케줄러는 후보 구성의 처리 규칙을 적용한 재생으로 실행 조건의 SLO 용량을 계산하고, 그 용량을 현재 요청률과 대기량에 비교한다."
- 그대로 두는 문장: 04 "평균 처리량만으로 지연 백분위를 결정하거나 단계별 지연 백분위를 단순 합산하지 않는다", 00 "추론을 연속 실행한 최대 처리량을 SLO를 지키며 감당할 수 있는 요청률과 동일시하지 않는다". κ는 재생의 지연 분포로 정하고 μ보다 작다.

**④ `SCH-23`**
- 현재: "…모델의 deadlinePolicy(drop/keep/defer, 기본 keep)를 actual/replay에 동일하게 적용한다."
- 제안: "…모델의 deadlinePolicy(drop/keep/defer, 기본 keep)를 actual과 용량 계산 재생에 동일하게 적용한다."

## 11. 확인

| 확인 | 방법 | 목표 |
|---|---|---|
| 오프라인 비교 | `hack/kappa -mode compare`, E1 기록 판단 498건 | 완료: target이 있는 457건 모두 일치, 에너지 차 0 J. commit 5 뒤 `v1alpha5` 세그먼트로 다시 |
| 에너지 정확도(오프라인) | E1 1시간의 기록 snapshot마다 분석 합 `ExpectedJoules`를 다시 계산해 60 s당 평균 × 60과 실측 비교 집합 495,741 J 비교 | 차이 ≤ 2%(재생 예측 +0.7%) |
| 실클러스터 단계 | commit 5 배포 뒤 tight 자동제어, 포아송 0.633, 2, 5 req/s 각 5분, 그리고 rasp5 κ(34.3)를 넘는 40 req/s 5분, soak 2 req/s 1시간 | 40 req/s에서 첫 판단이 추가 인스턴스를 고르고, 단계마다 SLO 충족(전환 구간 제외), 전환 대기 예측과 관측 큐 비교 기록 |
| Tick | 같은 실행의 `evaluationElapsedNanos`, CPU | 40 req/s에서도 Tick p99 ≤ 100 ms, CPU가 500m 아래, 간격 ≤ 15 s |
| 에너지(실측) | 1시간 `ExpectedJoules` 합과 `OperatingPower` 실측 | 차이 ≤ 2% |
| 용량표 | worker의 행 수와 계산 시간, κ 0.9배와 1.2배 포아송 단계(rasp5) | 0.9κ 충족, 1.2κ 초과, 전체 표 계산 시간 기록 |
| 재진입 | 비용이 있는 가용 노드가 있는 판단의 `decision.capacity` | `TransitionExceedsHorizon` 0, T가 최장 사슬 이상, 재진입 계획이 DP에 있음 |

## 12. 전환 뒤 삭제

commit 5에서 모두 지운다.

- `scheduler_candidates.go`: `EnumerateScheduler`와 깊이 우선 열거(`visit`, `place`, `emit`, `group`, `replicaLimit`, `sourceIndex`, `candidateNodes`, `canReuseSlot`, `routing`, `controlInstances`, `controlRouting`), route 변형, `Search.Truncated`.
- `scheduler_replay.go`의 Tick 경로: 후보 재생 루프(`EvaluateScheduler` 본문과 `schedulerEvaluateCandidate`의 후보 평가), `schedulerBetter`, `FinalizeSchedulerTarget`의 후보 선택, `transitionComplete`, `TransitionExceedsHorizon`과 그 assumption 문장, `projection` 인자. 인스턴스 하나의 SLO 추정을 내는 재생 본체는 용량표가 쓰므로 남는다.
- `projected-work-history`: `scheduler_policy.go`, `scheduler_projection.go`, `kube/scheduler.go`의 `workInterval`과 `applyWorkPolicy`, work-history 사유 코드, test(`scheduler_policy_test.go`, `scheduler_projection_test.go`, `kube/scheduler_policy_test.go`).
- 설정 다섯 개(7장)와 검증, Dashboard의 행과 타입(`scheduler-settings.tsx`, `read-types.ts`, `scheduler.test.ts`).
- `hack/kappa`의 κ 계산과 계획기 사본(서버 코드를 부른다).
- 열거 상한과 절단 test, `TransitionExceedsHorizon` test, 다중 후보 재생 선택 test.
- 문서: [스케줄러 구현 설계](scheduler-implementation.md)의 후보 열거, replay 선택, horizon, algorithm 문단과 [workload-pools 3.4절](workload-pools.md#34-전체-pool-가용-노드와-전력모드)의 horizon 문단을 이 설계로 다시 쓴다.

## 13. 사용자 결정과 질문

- 결정: κ는 기존 재생, 포아송 도착, 이분 탐색으로 미리 계산한다. Tick은 κ 합, 노드 계획, DP이고 재생이 없다. 비교 기간은 `T = max(60 s, 최장 사슬)` 하나다. 상위 K 검증, shadow, `hack/planner-ab`, 지연 공식, 여유율은 두지 않는다. `projected-work-history`는 전환에서 지운다.
- 질문 1: 10장 ①, ①', ④ 문장을 승인하는가. ①'의 04와 00은 로컬 사본이라 누가 반영하는가.
- 질문 2: shadow가 없으므로 전환은 오프라인 비교와 commit 5 배포 뒤의 실클러스터 단계로 확인하고, 문제가 있으면 이전 image로 되돌린다. 받아들이는가.
- 질문 3: 완료 여유를 `elapsed − (큐 + 실행)`의 SLO 백분위(E1 약 177 ms)로 정하는 5장 규칙을 채택하는가.
- 질문 4: imagenet(p99 100 ms)은 어떤 option으로도 κ가 0이다. 목표를 바꾸는가, 켜지 않는 Workload로 두는가.
- 질문 5: firstfloor가 배치 상한을 재준비 없이 바꿀 수 있는지 확인하지 않았다. 지금은 교체(remove와 prepare)로 센다. 바꿀 수 있다면 비용이 없는 동작으로 둘 수 있다.
- 질문 6: 계획기는 공유 인스턴스를 만들지 않는다(6장). Workload 두 개 이상이 같은 노드를 쓰려면 각자 인스턴스를 둔다. 받아들이는가.
- 질문 7: 응답 교환 구간에도 인스턴스가 점유된다면 rasp5 κ는 10.8로 1/3이 된다. 이 자료로는 가릴 수 없어 실클러스터 40 req/s 단계가 판정한다.
