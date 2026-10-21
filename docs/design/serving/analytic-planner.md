# 용량표 계획기 (M4)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M4 설계다. 계획기는 두 부분이다. 첫째, option과 Workload SLO마다 인스턴스 하나가 SLO를 지키는 최대 요청률 κ를 기존 재생으로 구한 용량표다. ChillServer가 시작할 때와 그 입력이 바뀔 때 자동제어 Workload의 행만 계산한다. 둘째, Tick은 재생 없이 κ 합, 노드 계획, route 시각별 DP로 구성을 고른다. 근거와 시제품은 [인스턴스 용량표 근거](capacity-table-evidence.md)와 `hack/kappa`다. 사용자가 승인한 10장의 명세 변경 묶음(①, ①', ④, ⑤)은 commit 5가 반영한다. 명세 변경 ②와 ③은 이미 반영되어 있다. [M0](decision-record-diet.md)와 [M1](observability-split.md)을 전제로 한다.

## 1. 측정한 문제

| 문제 | 근거 |
|---|---|
| Tick이 부하에 비례해 500m CPU limit을 채운다 | `replays` 10, `maxCandidates` 32에서 Tick p50이 0.633 req/s 913 ms, 2 req/s 3,475 ms, 5 req/s 7,495 ms, 최대 22,575 ms, CPU 499m(D-16). 기본값 100과 128에서는 Tick 133회가 30 s deadline으로 실패했다(D-1). 판단마다 후보 20개를 10회씩 재생하고, 재생은 사건마다 모든 요청을 훑고(`nextEvent`) 배치마다 O(대기 요청² × 배치 행)인 `firstfloor.ChooseBatch`를 부른다 |
| 재진입 후보에 닿지 못한다(D-22) | 인스턴스가 생긴 뒤 판단 128건 중 127건이 32에서 잘렸고 재진입 후보는 37~48번째였다. 깊이 우선 열거가 노드 UID 순이라 앞 노드의 option과 route 변형이 상한을 쓴다 |
| 재진입 사슬이 horizon을 넘는다 | 빈 노드 전환은 jetsonx 63.5 s, jetsono 134.5 s, orin-nano 197.1 s(settling 60 s 포함, D-20, D-21)이고, horizon 60 s를 넘는 후보는 `TransitionExceedsHorizon`이다. 사용자는 horizon 60 s를 유지하고 M4에서 푸는 것을 골랐다 |
| 지연 공식은 상한이 되지 못한다 | 배치 분위수를 더한 지연 상한(여유율 0.8)에 대해 외부 검토의 포아송 재생에서 실제 p95가 그 값의 1.8~3.6배였다. κ는 공식 대신 같은 재생으로 구한다 |
| 지킬 것 | 에너지 예측 E1 1시간 499,333 J 대 실측 495,741 J(+0.7%), 결정성(정렬된 입력, 고정 seed, 완전 동률은 keep) |

## 2. 용량표

**행.** 키는 option(노드, 모드, 할당, profile ID), 배치 상한 b, SLO(백분위 p, 목표 L), 완료 여유 m, 디스패치 점유 d와 그 출처, 용량표 설정이다. deadlinePolicy는 profile이 정한다. 행은 입력, κ, μ(b 이하 배치의 `b / (평균 실행시간 + b × d)` 최댓값), 격자 판정 결과, 창별 백분위 지연을 담는다.

**판정.** option 인스턴스 하나만 있는 keep snapshot에 요청률 λ의 포아송 도착을 넣고 재생 본체를 한 번 돌린 SLO 추정(`schedulerReplayEstimate`)을 읽는다. 재생은 배치마다 profile 실행시간 표본에 `배치 크기 × d`를 더한 시각에 그 배치를 끝내고 다음 배치를 시작한다. 배치 선택(`ChooseBatch`)은 firstfloor와 같은 profile 표본을 쓴다. 백분위 지연(m 포함) ≤ L이고 timeout과 만료가 0이면 그 창은 충족이다. 창 하나는 빈 상태에서 시작하고 요청 수는 `max(capacityMinimumRequests, ⌈100/(1 − p)⌉, ⌈λ × capacityWindow⌉)`다. 100은 백분위 위에 표본 100개를 두는 고정 규칙이다. seed 1~`capacityWindows`의 독립 창 중 `capacityPassingWindows`개 이상이 충족하면 λ는 충족이다. seed의 도착 간격을 λ로 나눠 쓰므로(공통 난수) 판정은 결정적이다.

**κ.** 창 요청 수와 부하에 따른 배칭이 λ와 함께 바뀌므로 판정이 λ에 단조라는 보장이 없다. 그래서 격자 `λ_k = 0.05kμ`(k = 1~20)를 모두 판정하고, 충족 앞부분과 미충족 뒷부분으로 나뉠 때만 κ를 정한다. 마지막 충족 `λ_K`와 다음 격자 사이를 0.5% μ까지 이분 탐색한 값이 κ다. K = 0이면 κ 0, K = 20이면 κ = μ다. 미충족 뒤에 충족이 오면 행은 `NonMonotone`이고 κ가 없으며 그 option은 `CapacityRejected`로 계획에서 빠진다. 디스패치 점유 측정이 없는 행도 `CapacityRejected`(사유 `MissingDispatchOccupancy`)이고 재생 없이 κ가 없다. 격자 결과는 행에 남아 조사에 쓴다. 격자 간격과 이분 탐색 폭은 코드 상수다.

**설정.** `capacityWindowNanos` 60 s, `capacityWindows` 20, `capacityPassingWindows` 19, `capacityMinimumRequests` 1,000. 60 s 창이 필요한 근거: 요청 수 창만 쓰면 빠른 option(genesis2 cuda, 2,000 요청이 14 s)의 κ가 μ에 붙어 relaxed 1.2κ에서 20/20 충족이었고, 60 s 창에서는 0/20이다. 20개 중 19개 기준이 필요한 근거: 창 하나로 정한 κ는 1.0κ에서 6~12/20만 충족했다.

**디스패치 점유.** d는 firstfloor 인스턴스가 요청 하나마다 profile 실행시간 밖에서 더 점유되는 시간이다. 포화한 인스턴스 하나가 배치 b로 낸 지속 처리율 X에서 `d = 1/X − (배치 b의 profile 평균 실행시간)/b`다. d에는 배치 사이 actor 시간(결과 전달, 다음 배치 선택, runner 호출 준비)과 같은 Pod CPU를 쓰는 요청 수신과 응답 처리로 늘어난 runner 실행시간이 함께 들어간다. rasp5 q1(m4 r3 Q2, 15:46~15:51)은 b1로 26.88 req/s, 요청당 37.20 ms였다. Q2 행 전체에서 실측 `serviceMS` 평균은 약 34.5 ms, 실행 기록 사이 간격은 평균 약 3.1 ms다. profile b1 평균은 27.79 ms라 d = 9.41 ms다.
- 기준은 profile 평균이다. 그래야 재생의 처리율 μ가 실측 X와 같고 κ는 X 아래에 놓인다. rasp5 q1 tight 행은 μ 26.88, κ 25.3(m 29 ms)과 25.1(m 139 ms)이다. 실측 `serviceMS` p50 30.7 ms를 기준으로 잡은 d 6.5 ms는 μ 29.2, κ 27.7로 X를 넘는다.
- 값은 DispatchOccupancyMeasurement가 쓴 record다([measurement-api](measurement-api.md) 3, 4장). `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`에서 `Accepted` record 중 (Node UID, 전력모드, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 골라 snapshot `dispatchOccupancy`(`{node{name, uid}, powerMode, runtimeImage, model, perRequestNanos, source}`)에 싣는다. `perRequestNanos`는 max(d, 0)이고 source는 record key다. 행 입력에 그 항목 전체가 들어가므로 행마다 d와 출처가 남고, 새 측정이 그 행을 다시 계산하게 한다.
- option의 Node UID, profile의 `condition.powerMode`, `condition.runtimeImage`, `condition.logicalModel`이 모두 같은 항목만 쓴다. d의 요청 수신과 응답 처리 비용은 model의 원시 입력과 출력 크기와 전력모드의 CPU 속도에 따라 다르므로 model과 모드마다 잰다. 같은 노드, 모드, runtime image, model의 다른 option(q1-lut, q2, 배치 2)은 같은 d를 쓴다. 맞는 항목이 없는 option의 행은 `MissingDispatchOccupancy`로 κ가 없고, 잴 때까지 계획에서 빠진다. 용량표 행과 재생은 같은 항목을 쓴다.
- 측정은 occupancy CR 하나다. ChillServer가 노드를 승인하고 그 option을 고정 배치 B로 준비한 뒤 load Job으로 profile μ의 `rateFactor`배를 보내고, 포화 창의 행에서 `e1.sql` 포화 표와 같은 식으로 X와 d를 계산한다. 같은 창의 `e1.sql` 완료 여유 열이 포화 부하의 m이다.

**검증된 모양.** 이분 탐색에 쓰지 않은 seed 21~40에서 0.9κ는 모든 option이 20/20, 1.0κ는 16~20/20, 1.2κ는 0/20이다. κ는 절벽의 무릎이다. d 없이 profile 실행시간만 재생한 E1 tight(p95 1 s, m 139 ms) κ: rasp5 q1 34.3, jetsonx 18.3, orangepi 8.1, jetsono 48.1~49.7, orin-nano 38.9~41.3 req/s. rasp5 q1은 실측 처리율 26.9 req/s에서 34.3이 과대임이 드러났고, d를 넣은 값은 위 문단의 25.3이다.

**계산하는 때와 대상.**
- ChillServer의 용량표 worker가 시작할 때 표 전체를 계산한다. 그 뒤에는 Workload의 자동제어가 켜지거나, 켜진 Workload의 SLO, profile, m, 디스패치 점유, 용량표 설정이 바뀔 때 빠진 행을 계산한다. 표는 메모리에만 있고 시작마다 비어 있으므로 재생, 배칭 규칙, κ 계산 코드가 바뀐 image도 그 코드로 계산한 표를 쓴다.
- 대상은 자동제어가 켜진 Workload의 SLO와 그 Workload에 측정 근거가 있는 option 변형이다. 꺼진 Workload의 노드는 keep뿐이라(4장) 그 κ를 쓰지 않는다. E1에서는 tight 하나라 option 변형 38개, 38행이다(SLO 3개 전체는 114행).
- 행 하나가 작업 하나이고 행끼리 독립이라 병렬로 계산한다. ChillServer의 CPU limit은 500m(`charts/chill/values.yaml`)이라 서버 안의 병렬은 0.5 CPU에 묶인다.
- 계산한 행도 기록된 이전 행(계산 시간 문단)도 없으면 그 option은 `CapacityPending`으로 계획에서 빠지고, 켜진 Workload에 κ가 있는 option이 하나도 없으면 판단이 `CapacityPending`이다.
- 행마다 `management` stream에 `capacity-row-built` 사건(행 전체, 계산 시각, 걸린 시간)을 남긴다. 이 사건은 기록이고 판단은 읽지 않는다.

**계산 시간.** 이전 재생은 사건마다 창의 모든 요청을 훑어 요청 수의 제곱으로 느려졌다(창 2,000 요청 0.19 s, 8,000 요청 2.8 s). 60 s 창은 빠른 option에서 8,700 요청이고 option 하나의 검증에 2,135 s가 들었다. commit 1은 사건마다 도착했고 끝나지 않은 요청만 훑고 다음 도착은 도착 순서 목록의 맨 앞에서 읽으며, `Definition.Identity()`를 도착 원형마다 한 번 만든다. 판단은 같은 값이고 1 ms 간격 창의 재생은 2,000 요청 102 ms에서 3.3 ms, 8,000 요청 1,703 ms에서 16.7 ms가 되었다(`BenchmarkSchedulerReplayWindow`). 격자는 이분 탐색보다 판정 횟수가 많지만 미충족 판정은 실패 창 2개에서 멈춘다 `[추론: 행당 이분 탐색의 약 2.5배]`. E1 tight 38행의 시작 계산은 코어 하나(GOMAXPROCS 1)에서 1,386 s였다(행당 4~118 s, `NonMonotone` 0, `hack/kappa -mode table`). 0.5 CPU limit에서는 그 두 배쯤이다 `[추론]`. 목표 몇 분(5분)을 넘으므로 용량표 worker는 계산 중인 행마다 그 키의 마지막 `capacity-row-built` 기록(로컬 `management` stream, 그다음 보관된 시간 객체를 최근 순으로)을 이전 행으로 읽어 쓰고, 새 행이 계산되면 바꾼다. 이전 행은 `previous`로 표시하고, 기록이 없는 행은 `CapacityPending`이다. 행을 별도 Job으로 나누는 단계는 넣지 않았다.

**imagenet.** imagenet(p99 100 ms)은 모든 option에서 κ 0이다(m 139 ms > 목표 100 ms, `hack/kappa`). 이 Workload는 꺼진 채로 두므로 행을 계산하지 않는다.

## 3. 판정과 순서

구성 c와 route 시각 r에서 켜진 Workload w마다 아래 값을 정확한 κ 합으로 계산한다.

| 값 | 정의 |
|---|---|
| `λ_w` | (관측한 창의 계산 도착 수 + 대기 수) / 관측한 창 길이. 도착은 재생이 쓰던 `Compute` 또는 `Accepted` 도착. 관측한 창은 관측 창 중 ChillServer가 시작한 뒤의 부분(`snapshot.observedSince`부터, 판단의 `search.windowStart`)이다. 내려가 있던 동안의 도착은 기록되지 않았으므로 창 전체로 나누면 λ가 작게 나온다(m4 r6: 40 req/s에서 재시작 뒤 λ 4.8로 genesis2, lattepanda, rasp5 q1-lut를 지웠다). 관측한 부분이 제어 주기(관측 창이 더 짧으면 관측 창)보다 짧으면 판단은 `Pending`(`PartialObservationWindow`)이고, 관측 창보다 짧은 동안 계획은 확장만 한다(14장 부분 관측 창) |
| `κ_new,w` | c의 w 인스턴스 κ 합 |
| `κ_ret,w` | route 전까지 남는 지금 인스턴스의 κ 합. 모드를 바꾸는 노드는 power 동작 전에 인스턴스를 모두 멈추므로 그 인스턴스는 빠진다. 같은 모드의 노드 계획은 분배된 인스턴스를 route 뒤에만 멈춘다(14장 만든 뒤 끊기) |
| `r` | route 전 사슬의 최대 시간(동작마다 측정 trial 최대 시간) |
| `B_w` | `q_w + max(0, λ_w − κ_ret,w) × r`. `q_w`는 지금 대기 수 |

- 최종 충족: `κ_new,w ≥ λ_w`. 초과량 `λ_w / κ_new,w − 1`. λ가 0이어도 켜진 Workload마다 인스턴스 하나 이상이다.
- 대기 충족: `B_w`가 0이면 충족이다. 아니면 EDF가 밀린 요청을 먼저 처리하므로 가장 오래 기다리는 요청의 대기는 `W_w = B_w / κ_new,w`이고, `W_w ≤ L_w − m`이면 충족이다. 초과량 `W_w / (L_w − m) − 1`. keep(r = 0)도 지금 대기 `q_w`로 같은 판정을 받으므로, 측정 λ가 κ 안이어도 줄지 않는 대기는 확장을 부른다(m4 r3: λ 29.5, κ 34.3, 대기 160에서 keep의 W 4.66 s > 0.861 s). κ는 처리율 μ 이하라 이 대기는 실제보다 길다. 밀린 요청이 다 빠지는 시간 `B_w / (κ_new,w − λ_w)`도 기록한다.
- 순서: 모든 w에서 두 충족을 만족하는 구성 중 에너지가 작은 것, keep, 논리 구성 key 사전 순. 그런 구성이 없으면 상태는 `Overload`이고, 모든 w에서 최종 충족(가중치의 κ 검사 포함)을 만족하는 구성이 그렇지 않은 구성보다 앞선다(SLO 우선: 전환 중 대기는 지나가지만 최종 미충족은 남는다). 같은 쪽 안에서는 w와 두 초과량에 걸친 최대 초과량이 작은 것, 에너지 순이다. keep은 r = 0이다. 근거: m4 r5 κ(rasp5 q1 25.3, genesis3 q2 70.9)에서 대기 없는 40 req/s는 keep의 최종 초과량 0.58이 genesis3 추가의 전환 대기 초과량 0.99보다 작아 keep이 골라졌다.
- 근거: 마지막 E1 snapshot에 포아송 40 req/s를 넣으면 에너지만으로는 rasp5 q1 + q1-lut(route 20.2 s)가 나오고 재생에서 전환 중 SLO에서 진다. 위 규칙으로 그 구성은 `B = 5.7 × 20.2 = 115`, `W = 115 / 69.2 = 1.67 s > 0.82 s`(L 1 s, m 177 ms)로 탈락한다. genesis3 q2 추가(route 10.7 s)는 `W = 61 / 144 = 0.42 s`로 충족이고, 재생 계획기와 같은 노드와 option이다.

## 4. 노드 계획과 DP

**노드 계획.** 노드마다 keep, 그리고 배치 가능한 노드에서 측정 option 변형(option마다 변형 하나 이하) 집합 중 한 모드와 `ValidateNodeBudget`을 만족하는 것. 모드를 바꾸는 계획은 목표 모드에서 온라인인 CPU로 예산을 확인한다. 현재 모드는 노드 annotation의 CPU를, 다른 모드는 DeviceClass `powerModes[].onlineCPUs`를 쓰고, 그 필드가 없는 모드의 계획은 `PlacementInventoryMissing`으로 뺀다. 같은 노드에서 같은 option 두 개는 CPU affinity가 겹쳐 생기지 않는다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이다. 계획마다 Workload별 `κ_new`와 `κ_ret`, 사슬, 전력을 둔다.

**인스턴스와 Workload.** 인스턴스는 Workload 하나에 속하고 κ는 그 Workload와 option의 행이다. 같은 모델의 Workload들은 한 노드를 함께 쓸 수 있지만 각자 인스턴스를 둔다. 계획기가 만드는 분배에서 인스턴스마다 Workload는 하나다. capture는 실제 분배에서 한 인스턴스가 두 Workload 이상에 들어 있으면 판단을 `SharedInstance`로 대기시킨다(6장의 운영 단계가 전환 전에 푼다). 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이므로 꺼진 Workload의 SLO는 계획 동작의 영향을 받지 않는다(workload-pools 4.4절의 노드 동작 규칙).

**사슬.** `schedulerPlan`의 순서다. 모드 변경은 power, 모든 기존 remove, prepare가 route 전이다. 같은 모드에서는 자원이 함께 들어가지 않을 때만 remove가 prepare 앞이고, 아니면 route 뒤다. 동작마다 측정 trial의 평균 wall J, 평균 시간, 최대 시간을 쓴다. 비용이 없으면 그 계획은 `MissingOperationCost`로 빠진다. 같은 인스턴스의 배치 상한이나 대기 시간을 바꾸는 계획은 그 인스턴스를 그대로 쓰고 route가 firstfloor policy를 바꾼다(사슬 없음). 서빙 Pod는 측정한 모든 배치를 시작할 때 초기화하고(`batchSizes`, m4 r2의 rasp5 q2 config는 1~16), firstfloor `Scheduler.Update`는 초기화되고 측정된 배치 안에서 policy의 `maxBatchSize`와 `maxBatchWaitMS`를 재시작 없이 바꾸며, route의 `applyPolicy`가 그 값의 native ACK를 기다린다.

**비교 기간.** `T = max(comparisonPeriodNanos, 공간의 최장 route 전 사슬 + 최장 route 뒤 remove)`(최대 시간)를 판단마다 하나 정하고 keep과 모든 계획에 쓰며 `decision.capacity.periodNanos`에 남긴다. 모든 사슬이 T 안에 끝난다. 비교 기간을 정하는 방법은 04 명세가 설계에 맡긴다. sample의 `comparisonPeriodNanos`는 사용자 결정으로 10분이다(m4 r5 뒤). T는 에너지 순서만 바꾸고 SLO 판정(3장)은 T와 무관하므로 부하가 오를 때 첫 판단의 확장은 T와 상관없다. m4 r5 기록에서 T만으로는 풀리지 않는 것: 분배되지 않은 genesis3 q2와 orangepi q1은 인스턴스를 띄운 노드 전력(49.80 W, 2.71 W)이 빈 노드(49.97 W, 2.73 W)보다 크지 않아 remove가 어떤 T에서도 에너지 이득이 아니다. 배치 상한 교체(genesis3 b1↔b2)와 SLO 때문에 준비한 인스턴스의 remove는 T 152 s에서 이미 이득(79~1,265 J)이었고 이득은 T에 비례해 커진다. 그래서 14장의 유휴 제거, 교체 회수, Overload 준비 유지 규칙을 둔다.

**에너지.** 노드 계획 하나의 [0, T] 노드 에너지는 keep이면 `P_before × T`, 아니면 `J_pre + P_mid × (r − t_pre) + J_post + P_after × (T − r − t_post)`(t는 평균 시간)다. P는 `schedulerBaseline`의 적재 상태 전력이다. 요청 에너지는 route에서 나눈다: [0, r]은 지금 routing으로 남는 인스턴스가 κ_ret까지 처리하고(`Σ y⁰_i e_i r`), 넘친 `max(0, λ − κ_ret) × r`건과 [r, T]는 target 배정 y로 처리한다(`Σ y_i e_i (T − r)`와 넘친 건수 × y 가중 평균 e). 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**DP.** 서로 다른 r마다 노드 순서 DP를 한 번 돈다. 칸은 (Workload별 배정 부하 칸, r에서 끝나는 사슬이 있는지)이고, 칸마다 (에너지, Workload별 정확한 `κ_new` 합, `κ_ret` 합)의 Pareto 집합을 둔다(에너지는 작을수록, 용량은 클수록 낫다). 부하 칸은 요청 에너지 배정에만 쓰고(계획은 `⌈κ/칸⌉`칸까지 받는다), 충족은 마지막에 Pareto 점의 정확한 합으로 3장 규칙을 적용한다. 그래서 칸 크기는 충족을 바꾸지 않고 에너지 배정의 정밀도만 바꾼다. 칸 수는 Workload 수에 따라 칸 상태가 20,000 이하인 가장 큰 정수(최대 50)다. keep은 DP 결과와 별도로 늘 후보로 판정한다. 분배되지 않은 준비된 인스턴스를 그 Workload에 다시 쓰는 계획은 물리적으로 keep과 같고 에너지도 같지만 κ가 커서 Pareto 집합에서 keep을 지우기 때문이다(m4 r4의 2 req/s에서 그 route 동작 하나의 계획이 28분 동안 22번 나왔다).

**router 가중치.** 지금 가중치는 그 해의 인스턴스 κ 안에 들면 그대로 둔다. 지금 분배된 인스턴스가 모두 해에 있고, 합이 1,000이며, 모든 인스턴스에서 `(λ_w + B_w / (L_w − m)) × weight / 1,000 ≤ κ_i`이면 그 가중치(새 인스턴스는 0)를 쓴다. 아니면 고른 해의 인스턴스 κ로 e가 작은 인스턴스부터 κ까지 채울 양은 λ이고, 대기가 있는 Workload(`B_w > 0`)에서는 λ에 밀린 요청을 `L − m` 안에 비우는 `B_w / (L_w − m)`을 더한다. 그 양을 채운 몫이나, 양이 κ 합을 넘으면 κ에 비례한 몫을 합 1,000의 정수로 최대 나머지 반올림한다. 반올림 뒤 모든 인스턴스에서 `λ_w × weight / 1,000 ≤ κ_i`를 확인하고, 넘치면 그 인스턴스의 한 단위를 여유가 가장 큰 인스턴스로 옮긴다. 옮길 곳이 없으면 그 해는 `RouteWeightExceedsCapacity`로 최종 미충족이다. 배정 0인 인스턴스는 가중치 0이고, 채울 양이 0이면 같은 가중치다. 요청 에너지의 y는 λ를 가중치대로 나눈 값이다. keep의 가중치가 바뀌면 사슬 없이 route 동작 하나다. 근거: m4 r2의 포아송 40 req/s에서 λ 12.8(관측 창 지연), 대기 160, 남는 인스턴스 없음, 새 인스턴스 넷(κ 42.0, 48.3, 114.6, 144.5)이었고, λ만 채우면 rasp5 q2가 1,000을 받아 대기를 홀로 받았다. B 363.9, `L − m` 0.861 s에서 채울 양 435 req/s가 κ 합 349를 넘으므로 가중치는 120, 138, 328, 414다. m4 r4의 40 req/s에서 판단마다 가중치를 다시 정하면 대기가 있을 때의 비우는 모양과 대기가 없을 때 rasp5 q1을 κ 25.3에 채우는 에너지 모양이 번갈아 75분에 route 동작 181번이었고 rasp5 q1의 1 s 안 응답은 47%였다.

**판단 기록.** `ExpectedJoules`는 고른 해를 정확한 y로 다시 계산한 위 분석 합이다. `decision.capacity`는 T, Workload별 λ, `κ_new`, `κ_ret`, B, W, 두 초과량, 고른 r, 가중치, 사용한 용량표 행 키, 그리고 노드 계획마다 key, 적격 여부, 제외 사유(`MissingOperationCost`, `CapacityPending`, `CapacityRejected`, `InfeasibleResources`, `FixedWorkload`, `PlacementInventoryMissing`), κ, 사슬 시간, J를 담는다(`SCH-30`). 계획의 조합은 기록하지 않는다.

**비용.** 노드와 계획 수에 비례하고 요청 수와 무관하다. 시제품(칸 DP)은 E1 기록 판단 498건에서 노드 계획 37개, Tick p50 1.84 ms, p99 3.02 ms(단일 코어)였다. Pareto 집합 크기는 commit 6 test가 기록한다.

## 5. 완료 여유 규칙

사용자가 채택한 규칙(firstfloor 큐와 실행 밖 시간의 백분위)을 firstfloor 단계 기록과 2장의 d에 맞춰 정한다. m은 요청 행의 `elapsedNanos − (nativeElapsedMS − ingress)`의 Workload SLO 백분위다. `ingress = queueMS − (runningNanos − queuedNanos)`는 firstfloor가 HTTP 요청을 받은 때부터 큐에 넣을 때까지(본문 읽기와 decode)이고 `queueMS`의 앞부분이다. `nativeElapsedMS − ingress`는 firstfloor actor가 요청을 큐에 넣은 때부터 실행 기록을 낼 때까지이고, 재생이 큐 대기와 `실행시간 + d`로 대신하는 구간이다. m은 그 밖의 모든 시간이다. `queuedNanos`가 없는 행(m4에서 약 20%, native 로그를 따라 읽을 때 queued 줄이 빠진 행)은 m 표본에서 빠진다. `hack/observations/e1.sql`의 `margin` 열이 이 값을 내고 운영자가 E 실행 결과로 `completionMarginMS`를 바꾼다. m이 바뀌면 2장의 행을 다시 계산한다. κ는 m에 둔감하지만(rasp5 tight d 없이 m 139 ms 34.3, m 179 ms 34.2) 3장의 `L − m`과 짧은 SLO의 κ는 m을 그대로 쓴다.

| 구간 | 기록 | m | 재생 |
|---|---|---|---|
| 수신, 인증(TokenReview), 본문 decode, 입력 계약 검사 | `validatedNanos`(ChillServer received → validated) | 들어감 | 없음 |
| 접수, ChillServer 유입 큐, firstfloor로 전달 | `acceptedNanos`, `dispatchedNanos`, firstfloor 수신까지 | 들어감 | 없음 |
| firstfloor 본문 읽기와 decode | `queueMS − (runningNanos − queuedNanos)` | 들어감 | 없음 |
| firstfloor 큐 대기 | `runningNanos − queuedNanos` | 빠짐 | 재생의 큐 대기 |
| 전처리, 추론, 후처리 | runner의 `preprocessMS`, `backendMS`, `postprocessMS`, 모두 `serviceMS`(runner 교환) 안 | 빠짐 | profile 실행시간 표본(같은 runner 교환 시간) |
| 서빙 부하에서 늘어난 runner 실행시간 | `serviceMS` − profile | 빠짐 | d |
| 배치 준비, runner 응답 뒤 actor가 실행 기록을 내기까지 | `nativeElapsedMS − queueMS − serviceMS` | 빠짐 | d |
| 실행 기록 뒤 다음 배치 시작까지 actor 시간 | 다음 요청들의 큐 대기 | 빠짐 | d |
| 응답 교환(firstfloor → ChillServer → client) | `exchangeNanos`, `elapsedNanos` | 들어감 | 없음 |

- 재생은 요청 자신의 완료도 `실행시간 + 배치 크기 × d` 뒤로 둔다. 그래서 m은 actor 구간 전체를 뺀다. 남는 차이는 보수 쪽이다. 실행 기록 뒤 다음 배치까지의 actor 시간(포화에서 평균 약 0.5 ms)은 그 요청을 늦추지 않는데 재생은 더하고, 가벼운 부하에서는 늘어난 실행시간(m4 2 req/s 평균 2.7 ms)이 d보다 작다.
- 값(p95, `stat.Empirical`): E1 2 req/s 177.8 ms, m4 r2 고정 2 req/s 28.5 ms, m4 r3 고정 2 req/s 25.8 ms, m4 r3 포아송 40 req/s 포화 29.2 ms(첫 10,000행). κ가 정해지는 부하는 포화 근처이므로 m4 값으로는 29 ms가 맞다. 설정값 139 ms는 그대로이고 바꾸는 것은 사용자 결정이다.
- E1과 m4의 차이는 인증이다. E1의 received → validated p95가 103.2 ms였고, 요청마다 TokenReview가 client-go 요청 한도에 걸렸다. c829518(허용된 인증 결과를 10 s 재사용) 뒤 m4에서는 12.6~16.1 ms다. 나머지(firstfloor로 전달, ingress p95 약 9.5 ms, 응답 교환 p95 약 5~6 ms)는 두 실행에서 비슷하다. `queueMS` 전체를 빼는 열(E1 169.0 ms, m4 17.9~22.1 ms)은 firstfloor ingress를 m과 재생 어느 쪽에도 넣지 않는다.

## 6. 인스턴스 공유 정리

같은 모델 인스턴스 공유는 연구 범위 밖이다. M4는 계획기와 그 저장 모델에 부담이 되는 부분만 지우고, 나머지는 M5 후보로 둔다. firstfloor, serving-worker, runtime image는 바꾸지 않는다(Workload와 정의별 큐는 그대로).

**M4에서 지우는 것(commit 6).** `controls.sharing` 설정. 이전 열거기의 공유 cohort 보존과 기존 인스턴스에 UID를 붙이는 후보 경로는 새 계획기로 옮기지 않는다(열거기와 함께 지워진다). 판단 snapshot과 기록의 `SchedulerInstance.WorkloadUIDs`는 `WorkloadUID` 하나가 되고, Tick에서 쓰지 않는 UID별 `arrivalAgesNanos`가 빠진다. 꺼진 Workload를 binding 변경만으로 SLO 판정에 넣는 경로(`schedulerSLOTargets`)를 지우고 노드 동작 규칙은 둔다. `StaticRoutingTarget`은 최종 분배에서 한 인스턴스가 두 UID에 들어가는 설정을 거절한다. `SchedulerOption.CompatibleUIDs`(프로파일 적용 조건)는 그대로다.

**전환 전 운영 단계.** testbed에는 공유 인스턴스가 있다. rasp5 인스턴스(Pod `serving-d42ad89cca2909bb6b02`)의 `edge.dacs.io/serving-routing`이 relaxed(`ae87…`), tight(`c210…`), imagenet(`e06b…`)를 모두 가리키고 세 Workload는 꺼져 있다(e1/report-4.md 5절). 사용자 결정으로 tight만 rasp5 인스턴스에 남고 relaxed와 imagenet은 분배하지 않는다. commit 6 배포 전에 지금 image로 다음을 한다.
1. `/api/v1/pools`, `/api/v1/scheduler/state`, Pod annotation을 `~/.cache/chill-assets/ops/m4-unshare/before/`에 남긴다.
2. tight를 `staticRouting`으로 rasp5 인스턴스에만 분배한다.
3. relaxed와 imagenet은 `staticRouting`에 빈 항목(`{}`)을 주어 분배에서 뺀다. positive weight가 없는 항목은 그 Workload의 routing을 모두 지운다([설정 API](scheduler-api.md)).
4. pools와 annotation에서 모든 인스턴스가 Workload 하나인지 확인한다. 되돌리기는 이전 `staticRouting`이다.

**M5 정리 후보(M4에서는 그대로).** Runtime의 인스턴스별 다중 binding과 route 복원(`runtime.go`, `runtime_scheduler.go`, `InstanceState.Workloads[]`와 UID별 카운터), `staticRouting`의 다중 UID alias(`static_routing.go`, `kube/static_routing.go`), `serving-operation`의 `InstanceShared` 거절(`cmd/serving-operation/main.go:489-518`), `edge.dacs.io/serving-routing` annotation의 reader와 writer와 annotation이 없을 때의 fallback(`kube/observer.go`, `kube/operations.go`), Dashboard의 인스턴스별 여러 서비스 표시(`resources.tsx`, `read-types.ts`), 설계 문서(workload-management, heterogeneous-workloads, scheduler-runtime-measurement)의 공유 문단. 같은 노드 공동 사용, 프로파일 재사용, Bundle은 유지한다.

**그대로 남는 것.** 재생 코드(용량표 계산과 오프라인 검증), `schedulerPlan`, `schedulerMeasuredOptions`, `schedulerBaseline`, 전환 실행기, 가용 노드 최저 모드 규칙, 비교 집합, firstfloor batcher.

## 7. 설정과 저장 계약

| 대상 | 변경 |
|---|---|
| 추가(commit 2) | `capacityWindowNanos`(> 0), `capacityWindows`(≥ 1), `capacityPassingWindows`(1 ≤ x ≤ `capacityWindows`), `capacityMinimumRequests`(≥ 1). 설정 decode가 strict(`scheduler_service.go:118`)라 배포 때 `serving-scheduler` ConfigMap과 Dashboard 설정 화면을 같이 바꾼다 |
| 추가(commit 7) | `dispatchOccupancy`: `{node, runtimeImage, model, perRequestNanos(≥ 0), source}` 목록, 노드, runtime image, model마다 하나. 없거나 비어 있으면 모든 행이 `MissingDispatchOccupancy`다. 배포 전에 ConfigMap에 측정한 노드의 항목을 넣는다(`config/samples/serving-scheduler.json`에 rasp5 mobilenet). Dashboard는 설정 전체를 PUT하므로 항목을 그대로 보존한다 |
| 삭제([measurement-api](measurement-api.md) commit 8) | `dispatchOccupancy`. 값은 snapshot의 측정 record다. strict decode라 배포 때 ConfigMap의 key를 같이 지운다 |
| 이름(commit 8) | 비교 기간 하한은 `comparisonPeriodNanos`(> 0)다. strict decode라 배포 때 ConfigMap의 `horizonNanos`를 이 이름으로 바꾼다(sample 10분). 판단 기록의 `settings`도 이 이름이고, Dashboard는 판단마다 실제 T인 `decision.capacity.periodNanos`를 보여 준다 |
| 삭제(commit 6) | `algorithm`, `replays`, `seed`, `maxCandidates`, `maxReplayRequests`, `controls.sharing`. 같은 방식으로 ConfigMap과 Dashboard를 바꾼다 |
| 판단 기록 | `v1alpha5`에 `decision.capacity`와 `management`의 `capacity-row-built`가 더해지고 `candidates`, `search.truncated`, `policy`가 빠진다. snapshot 인스턴스는 `workloadUID` 하나이고 `arrivalAgesNanos`가 빠진다. 관측 reader는 없는 필드를 빈 값으로 읽으므로 버전을 올리지 않는다(ADR 0019). 전환 전 시간의 기록은 새 reader에서 인스턴스의 Workload가 비므로 전환 시각을 걸치는 `OperatingPower` 구간은 쓰지 않는다 |
| 추가(commit 10) | snapshot 인스턴스의 `unroutedSince`와 `heldUntil`(14장). controller의 메모리 기록이고 없으면 빈 값이다. 판단 사유 `IdleInstanceRemoved`, `ReplacementUnpaid`와 노드 계획 사유 `HeldAfterOverload`가 더해진다 |

## 8. 커밋 단위

| # | commit | 내용 | 의존 |
|---|---|---|---|
| 1 | `perf(serving): replay only arrived requests per event` | 사건마다 도착했고 끝나지 않은 요청만 훑고 다음 도착은 도착 순서 목록에서 읽는다. 생성 도착의 `Definition.Identity()`를 도착 원형마다 한 번. 기존 재생 test의 판단과 같은 값 | 없음 |
| 2 | `feat(serving): build per-option SLO capacity rows` | `internal/serving/capacity.go`(`hack/kappa`의 κ 계산 이동, 격자와 단조 검사), 시작과 입력 변경 때 자동제어 Workload의 행만 계산하는 용량표 worker, 행 병렬, `capacity-row-built`, 네 설정, `CapacityPending`, `CapacityRejected`, `/api/v1/scheduler/capacity` 조회, E1 tight 표의 시작 계산 시간 측정. Tick은 그대로 | 1 |
| 3 | `feat(loadgen): send Poisson arrivals` | `chill_loadgen replay --arrivals poisson --seed`. 지금은 고정 간격뿐이다(`cli.py:55-58`) | 없음 |
| 4 | `chore(hack): report the non-execution latency percentile` | `e1.sql`에 5장의 열 | 없음 |
| 5 | `docs(spec): judge SLO by per-option capacity and drop instance sharing` | 10장 승인 묶음의 추적 문서(README, 03, scheduler, workloads, dashboard SRS). 00과 04는 로컬 사본에 같은 문장 | 사용자 승인 |
| 6 | `feat(serving): plan with capacity sums and node plans` | `internal/serving/scheduler_capacity.go`의 노드 계획, 사슬, T, 전환 판정, Pareto DP, 가중치, `decision.capacity`, `SharedInstance`, 6장의 공유 삭제와 단일 Workload 저장 모델, `StaticRoutingTarget` 거절. `EvaluateScheduler` 교체, 12장 삭제, `hack/kappa` compare가 서버 계획기와 `v1alpha5` 세그먼트를 쓰도록 변경, 설정 삭제, ConfigMap, Dashboard, 문서 | 2, 5, 11장 오프라인 비교, 6장 운영 단계 |
| 7 | `feat(serving): add measured dispatch occupancy to capacity rows` | `dispatchOccupancy` 설정과 검증, 행 입력의 d와 출처, `MissingDispatchOccupancy`, 재생 배치 시간과 μ에 d, `e1.sql`의 5장 m 열과 포화 표, Dashboard 타입과 설정 비교, ConfigMap 예 | 2 |

## 9. 검사

| commit | 검사 |
|---|---|
| 1 | 변경 전후 재생 결과가 기존 scheduler test 전체에서 같음. 8,000 요청 창 시간 기록 |
| 2 | 고정 fixture profile의 κ가 상수와 같음(재생이나 배칭 규칙 변경이 κ를 바꾸면 실패한다). 같은 행을 두 번 계산하면 같은 값. 충족, 미충족, 충족 격자는 `NonMonotone`이고 κ 없음. 창 요청 수가 `λ × capacityWindow` 이상. 19/20 판정 경계. m 139 ms, p99 100 ms에서 κ 0. 시작 때 자동제어 Workload의 행만 계산하고 꺼진 Workload의 행은 없음. 자동제어를 켜거나 SLO, profile, m, 설정이 바뀌면 빠진 행만 계산하고 그 외 변경은 계산하지 않음. 계산 중 판단은 `CapacityPending`. 행마다 `capacity-row-built` 기록. strict decode가 네 필드 누락을 거절 |
| 3 | 같은 seed의 도착 시각이 같고 평균 간격이 1/rate 근처 |
| 6 판정 | 3장 표의 40 req/s 두 예. 모드 변경 계획의 `κ_ret` 0. route 뒤 remove 계획은 keep κ를 남김. `W > L − m`인 계획은 에너지가 작아도 선택되지 않음. 모든 계획이 미충족이면 최대 초과량 순. SLO가 다른 Workload 두 개에서 각자 판정. 실제 분배에서 한 인스턴스가 두 Workload에 있으면 `SharedInstance`. 꺼진 Workload 노드는 keep뿐이고 binding 변경으로 판정 대상이 늘지 않음. `StaticRoutingTarget`이 두 UID alias를 거절. 단일 `workloadUID` 기록 왕복과 `OperatingPower` 소속. 공유 fixture(`scheduler_placement_test.go`, `scheduler_test.go`의 교차 UID 대기, `runtime_scheduler_test.go`, `scheduler_transition_test.go`)는 Workload별 인스턴스로 다시 씀 |
| 6 DP | 무작위 소형 fixture(Workload 1~3개)에서 DP의 충족 판정이 정확한 κ 합 brute force와 같음(예: κ 0.34λ 인스턴스 세 개가 충족). 칸 수를 바꿔도 충족 판정이 같음. 가중치 반올림 뒤 모든 인스턴스가 κ 안(예: κ 99.6과 0.4, λ 100). 배치 상한 변경이 같은 인스턴스의 route 하나. 요청 에너지가 route에서 나뉨 |
| 6 기간과 기록 | 사슬 197 s, horizon 60 s에서 keep과 모든 계획의 T가 같고 trial J 전체 포함, `TransitionExceedsHorizon` 없음. option 40개에서 재진입 계획이 DP에 있음(D-22). 제외된 노드 계획이 사유와 함께 기록됨. 입력 순서를 섞은 두 snapshot의 판단이 `reflect.DeepEqual`. 완전 동률은 keep |
| 7 | 노드, runtime image, model 중 하나라도 다른 항목만 있으면 κ 없음(`MissingDispatchOccupancy`). rasp5 q1 profile 903 표본과 d 9.409 ms, 60 s 창 20/19, m 29 ms의 tight 행이 μ 26.88, 25 ≤ κ ≤ 26.88(값 25.3). d 0인 fixture κ는 상수 그대로 |
| 전체 | `make lint test`, `make arch-check`, `make helm-lint helm-template`, `make dashboard-check` |

## 10. 명세 변경 (승인 묶음)

①, ①', ④(용량)와 ⑤(인스턴스 공유)는 사용자가 승인한 묶음이고 commit 5가 추적 문서에 반영한다. 원문은 ff8fc12 기준이고, 00과 04는 저장소가 추적하지 않는 로컬 사본(`.gitignore:22`)에 같은 문장으로 둔다.

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

**⑤ 인스턴스 공유 (같은 모델 Workload의 노드 공동 사용과 프로파일 재사용은 그대로)**

| 위치 | 현재 | 제안 |
|---|---|---|
| README "노드 POOL과 구성" | "여러 활성 POOL이 한 노드를 함께 쓰는 조건은 그 워크로드들이 같은 등록 모델을 참조하는 것이다." | 끝에 덧붙임: "이때도 워크로드마다 자기 인스턴스를 둔다." |
| README "노드 자원과 모델 인스턴스" | "모델 산출물, 런타임, 전처리 산출물과 데이터셋이 모두 같은 워크로드들은 하나의 인스턴스를 공유할 수 있다. 유입 측과 인스턴스 내부 모두 워크로드별 큐와 SLO를 유지하며, 실행 배치는 워크로드별로 구성한다." | "모델 산출물, 런타임, 전처리 산출물과 데이터셋이 모두 같은 워크로드들은 같은 측정 프로파일을 쓴다. 인스턴스 하나는 워크로드 하나의 요청을 처리한다." |
| 00 | "같은 모델 인스턴스를 공유하는 워크로드들은 SLO가 달라도 동일한 실행 조건의 성능 측정을 재사용한다. 워크로드별 도착·큐 상태와 분배·배칭 규칙에 프로파일을 적용해 요청 지연을 계산하며, 합산 입력량만으로 각 워크로드의 SLO를 판단하지 않는다." | "실행 조건이 같은 워크로드들은 SLO가 달라도 동일한 실행 조건의 성능 측정을 재사용한다. 각 워크로드는 자기 인스턴스에서 처리하고, 프로파일에 배칭과 큐 선택 규칙을 적용한 재생으로 실행 조건과 SLO마다 용량을 계산한다." (뒤 두 문장 그대로) |
| 03 | "같은 모델 인스턴스가 SLO가 다른 워크로드들을 처리할 수 있다. 모델 공유와 실행 배치 혼합은 구분하고, 실행 배치는 워크로드별로 구성한다. 합산 입력량은 인스턴스의 전체 처리 부담을 계산하는 데 사용하되, 워크로드별 부하·대기시간·SLO는 따로 유지한다. 여러 큐 중 다음에 무엇을 실행할지는 각 SLO를 고려해 정한다. 큐를 분리해도 이미 실행 중인 다른 워크로드의 배치 때문에 대기할 수 있으므로, 합산량이나 큐 분리만으로 SLO 충족을 판단하지 않는다." | "SLO가 다른 워크로드들은 같은 실행 조건의 프로파일을 재사용하고, 인스턴스 하나는 워크로드 하나의 요청을 처리한다. 워크로드별 부하, 대기시간, SLO는 따로 유지한다. 이미 실행 중인 배치 때문에 대기할 수 있으므로 평균 처리량만으로 SLO 충족을 판단하지 않는다." (끝 문장 "구체적인 큐 선택 정책은 설계에서 정한다." 그대로) |
| 04 결정 | "결정에는 활성 노드, 서빙 파드별 모델·실행 경로·CPU·메모리 요청량, 공유할 워크로드, 노드 전력모드, …" | ", 공유할 워크로드"를 지운다 |
| 04 에너지 | "공유 인스턴스의 모델 로드·유휴 비용을 연결된 워크로드 수만큼 중복 계산하지 않는다." | "모델 로드와 유휴 비용은 인스턴스마다 한 번 계산한다." |
| `SCH-06` | "공유 인스턴스는 워크로드별 부하와 SLO, 인스턴스와 노드의 합산 부담을 유지하고, 처리 비용은 …" | "인스턴스는 워크로드 하나의 부하와 SLO를 맡고 노드는 그 인스턴스들의 합산 부담을 유지하며, 처리 비용은 …" |
| `SCH-14` | "공유 인스턴스에서 다른 워크로드의 실행 때문에 발생하는 대기를 포함해 각 워크로드의 완료 시각·지연 분포를 평가한다." | "각 워크로드는 자기 인스턴스의 용량으로 평가한다(`SCH-13`)." |
| `SCH-17` | "모델 로드·유휴 비용은 공유 인스턴스당 한 번, …" | "모델 로드와 유휴 비용은 인스턴스당 한 번, …" |
| `SCH-29` | "공유 인스턴스에서는 각 워크로드의 변경과 다른 워크로드의 요청을 구분해 처리한다." | 문장 삭제 |
| `SCH-AC-07` | "같은 모델을 공유하는 다른 SLO의 워크로드 / 근거 재사용과 UID·큐·SLO·결과 분리, 공유 실행 때문에 생기는 대기 반영" | "같은 실행 조건을 쓰는 다른 SLO의 워크로드 / 근거 재사용, 워크로드별 인스턴스, UID, 큐, SLO, 결과 분리" |
| `SCH-AC-10` | "공유 모델·여러 파드·Serving 참여 여부·임시 노드 비용" | "여러 파드, Serving 참여 여부, 임시 노드 비용" |
| `SCH-AC-17` | "접수 당시 정의·수신 시각·등록 건·기존 요청과 공유 Workload의 계약 보존" | "접수 당시 정의, 수신 시각, 등록 건, 기존 요청의 계약 보존" |
| `WM-21`과 수용 행 "공유 인스턴스의 워크로드 삭제" | "공유 인스턴스에서 워크로드를 제거하면 다른 워크로드의 연결과 요청 처리를 유지해야 한다. …" | 요구와 수용 행 삭제 |
| `WM-30` | "공유 인스턴스에서도 워크로드의 정의와 기록을 구분해야 한다." | 문장 삭제 |
| `DB-FR-10` | "여러 활성 POOL이 함께 쓰는 노드와 공유 인스턴스의 관계, 목표 및 실제 자원 조건을 구분해야 한다." | "여러 활성 POOL이 함께 쓰는 노드와 그 위 워크로드별 인스턴스, 목표 및 실제 자원 조건을 구분해야 한다." |
| `DB-FR-14` 추적 | "…의 인스턴스 공유 조건, …" | "…의 프로파일 적용 조건, …" |

그대로 두는 문장: `SCH-15`, `SCH-33`, `SCH-AC-21`, README의 꺼진 워크로드 영향 문장, 05 전체, dashboard SRS의 "공유 모델"(같은 ModelSpec 참조) 문장.

## 11. 확인

| 확인 | 방법 | 목표 |
|---|---|---|
| 공유 없음 | 6장 운영 단계 뒤 pools, scheduler state, Pod annotation | 모든 인스턴스가 Workload 하나 |
| 오프라인 비교 | `hack/kappa -mode compare`, E1 기록 판단 498건 | 완료: target이 있는 457건 모두 일치, 에너지 차 0 J. commit 6 뒤 서버 계획기와 `v1alpha5` 세그먼트로 다시 |
| 에너지 정확도(오프라인) | E1 1시간의 기록 snapshot마다 분석 합을 다시 계산하고, 판단마다 `ExpectedJoules / T`를 다음 판단까지 적분한 합을 실측 비교 집합 495,741 J와 비교 | 차이 ≤ 2%(재생 예측 +0.7%) |
| 실클러스터 단계 | commit 6 배포 뒤 tight 자동제어, 포아송 0.633, 2, 5 req/s 각 5분, rasp5 κ(d 포함 25.3)를 넘는 40 req/s 5분, soak 2 req/s 1시간 | 40 req/s에서 첫 판단이 추가 인스턴스를 고름. 전환 구간을 포함해 단계마다 SLO 충족. 예측 `B`, `W`, 비우는 시간과 관측 큐 비교 |
| 디스패치 점유 | 2장 측정 절차, 노드, runtime image, model마다 | m4 r3 Q2: rasp5 q1 b1 포화 처리율 26.88 req/s, d 9.41 ms. 다른 노드는 측정 전까지 κ 없음 |
| Tick | `evaluationElapsedNanos`, CPU | 40 req/s에서도 Tick p99 ≤ 100 ms, CPU가 500m 아래, 간격 ≤ 15 s |
| 에너지(실측) | 판단마다 `ExpectedJoules / T`를 다음 판단까지 적분한 1시간 합과 `OperatingPower` 비교 집합 실측 | 차이 ≤ 2% |
| 용량표 | 시작 때 worker의 행 수, `NonMonotone` 수, 행별 시간과 전체 표 계산 시간, κ 0.9배와 1.2배 포아송 단계(rasp5) | 0.9κ 충족, 1.2κ 초과, `NonMonotone` 0, 전체 표가 몇 분(5분) 안. 넘으면 2장의 Job 분할과 이전 표 판단을 넣는다 |
| 재진입 | 비용이 있는 가용 노드가 있는 판단의 `decision.capacity` | `TransitionExceedsHorizon` 0, T가 최장 사슬 이상, 재진입 계획이 기록됨 |

## 12. 전환 뒤 삭제

commit 6에서 모두 지운다.

- `scheduler_candidates.go`: `EnumerateScheduler`와 깊이 우선 열거(`visit`, `place`, `emit`, `group`, `replicaLimit`, `sourceIndex`, `candidateNodes`, `canReuseSlot`, `routing`, `controlInstances`, `controlRouting`), route 변형, `Search.Truncated`.
- `scheduler_replay.go`의 Tick 경로: 후보 재생 루프(`EvaluateScheduler` 본문과 `schedulerEvaluateCandidate`의 후보 평가), `schedulerBetter`, `FinalizeSchedulerTarget`의 후보 선택, `transitionComplete`, `TransitionExceedsHorizon`과 그 assumption 문장, `projection` 인자. 인스턴스 하나의 SLO 추정을 내는 재생 본체는 용량표가 쓰므로 남는다.
- `projected-work-history`: `scheduler_policy.go`, `scheduler_projection.go`, `kube/scheduler.go`의 `workInterval`과 `applyWorkPolicy`, work-history 사유 코드, test(`scheduler_policy_test.go`, `scheduler_projection_test.go`, `kube/scheduler_policy_test.go`).
- 설정 여섯 개(7장)와 검증, Dashboard의 행과 타입(`scheduler-settings.tsx`, `read-types.ts`, `scheduler.test.ts`).
- `hack/kappa`의 κ 계산과 계획기 사본(서버 코드를 부른다).
- 공유 cohort 보존과 UID 추가 후보 경로, binding 변경 SLO 판정 경로(6장).
- 열거 상한과 절단 test, `TransitionExceedsHorizon` test, 다중 후보 재생 선택 test.
- 문서: [스케줄러 구현 설계](scheduler-implementation.md)의 후보 열거, replay 선택, horizon, algorithm 문단과 [workload-pools 3.4절](workload-pools.md#34-전체-pool-가용-노드와-전력모드)의 horizon 문단을 이 설계로 다시 쓴다.

## 13. 사용자 결정과 질문

- 결정: κ는 기존 재생, 포아송 도착, 격자와 이분 탐색으로 계산한다. Tick은 κ 합, 노드 계획, Pareto DP이고 재생이 없다. 비교 기간은 `T = max(60 s, 최장 사슬)` 하나다. 전환 중 SLO는 Workload마다 필수 조건이다. 인스턴스 공유는 연구 범위 밖이고 M4는 계획기와 저장 모델의 부담만 지운다(6장). 전환은 shadow 없이 오프라인 비교와 실클러스터 단계로 확인하고 문제가 있으면 이전 image로 되돌린다. 완료 여유는 5장 규칙이다. imagenet은 꺼진 채로 둔다. Workload 여러 개의 칸 크기는 E5 전에 11장 측정으로 다시 검토한다. `projected-work-history`는 전환에서 지운다.
- 결정: 디스패치 점유는 노드, runtime image, model마다 단일 인스턴스 포화에서 재고 재생 실행시간에 더한다. 행마다 값과 출처를 남기고 runtime image가 바뀌거나 model이 더해질 때 다시 잰다. 측정이 없는 노드는 κ를 계산하지 않고 그 option을 뺀다. 다른 노드나 다른 model의 값은 빌리지 않는다.
- 결정: 10장 승인 묶음(①, ①', ④, ⑤)을 반영한다. 공유를 풀 때 tight만 rasp5 인스턴스에 남고 relaxed와 imagenet은 분배하지 않는다.
- 결정: 용량표는 ChillServer가 시작할 때마다 자동제어 Workload의 행만 다시 계산하고, 행 단위로 병렬 계산한다. 남긴 행은 기록이다. 몇 분 안에 끝나지 않으면 이전 표로 판단하며 뒤에서 다시 계산한다(2장).
- 확인: firstfloor는 배치 상한을 재준비 없이 바꾼다(4장 사슬). m4 r5에서 genesis3 q2 b1↔b2는 stop, drain, remove, prepare였고 이제는 route 하나다.

## 14. 구현 규칙

4장의 계획기를 `internal/serving/scheduler_capacity.go`가 다음 규칙으로 구현한다.

- **설정 gate.** placement, resources, batching, membership이 꺼져 있으면 option 변형은 그 Workload의 지금 인스턴스 하나와 노드, CPU와 GPU 선택, 배치 상한과 대기 시간이 같아야 한다(membership은 그 Workload의 인스턴스가 있는 노드만). `maxReplicas`와 `maxInstances`는 POOL 전체의 Workload별 인스턴스 수와 전체 인스턴스 수 상한이고, replicas가 꺼져 있으면 Workload별 인스턴스 수가 지금 수와 같다. DP 점은 이 수를 값으로 가지므로 상한을 정확히 지킨다. 노드마다 가장 큰 계획의 합이 상한 안이면 수 값을 두지 않는다. `maxCPUs`는 인스턴스마다의 CPU 수 상한이다. routing이 꺼져 있으면 남는 인스턴스는 지금 가중치를, 새 인스턴스는 option의 가중치를 쓰고 같은 κ 검사를 받는다.
- **진행 중 전환.** 실행 중인 목표에 Fixed가 아닌 동작이 남아 있으면 판단은 그 목표를 keep으로 이어 가고 사유는 `TransitionInProgress`다. 가용 노드 최저 모드 낮춤만 남은 목표는 공통 상태이므로 계획을 막지 않는다. 목표의 동작이 실패하거나, 끝난 동작의 효과가 사라지면(준비한 인스턴스가 OOMKilled되거나 Ready가 아님) 그 동작은 `failed`(`completed effect lost`)로 action 기록에 남고 목표는 버려지며, 다음 판단은 실제 상태에서 다시 계획한다. 실패한 동작은 관측 창 동안 snapshot `actions`에 남고, 그동안 같은 노드에서 같은 상태를 준비하는 계획은 `PreparationFailed`로 빠진다. 근거: m4 r2에서 rasp5 q2 Pod가 route 뒤 약 15 s마다 OOMKilled되었고, 목표가 `TransitionInProgress`로 남아 prepare와 route를 30 s마다 되풀이했다.
- **동작 기한.** 실행 중인 동작마다 기한은 그 cost의 측정 trial 최대 시간에 관측 창 하나를 더한 값이다(cost가 없는 route, stop, drain은 관측 창). 기한을 넘긴 동작이 있으면 목표는 버려지고 동작 사유는 `ActionDeadlineExceeded`다. 취소할 수 있는 prepare는 취소되어 `failed`로 남으므로 관측 창 동안 같은 준비는 `PreparationFailed`로 빠지고, 취소할 수 없는 동작은 끝날 때까지 돈다. 버린 목표는 그 목표의 다른 실행 중 동작으로도 다시 이어지지 않고, 기한을 넘긴 동작을 담은 새 목표도 버려진다. 다음 판단은 실제 상태에서 다시 계획한다. 근거: m4 r7에서 07:02:05에 시작한 rasp5 q1 prepare(측정 22.7 s)가 07:12:30까지 끝나지 않았고 그동안 판단은 `TransitionInProgress`로 머물렀다.
- **DP 요청 에너지.** DP는 칸 배정의 요청 에너지를 [0, T] 전체에 target 배정으로 센다. 최종 순위와 `ExpectedJoules`는 4장의 route 분할 식으로 정확히 계산한다. e는 profile의 배치 1 입력당 에너지다.
- **가중치 척도.** 지금 가중치의 합이 1,000이 아니면 첫 판단이 route 동작 하나로 1,000 척도의 가중치를 적용한다.
- **제외 사유.** 4장의 사유에 더해 기준 전력이 없으면 `MissingUnloadedNodeBaseline:<노드>:<모드>`, 측정 POOL 노드는 `MeasurementNode`, 준비되지 않은 노드는 `NodeUnavailable`, 비서빙 예산 노드는 `PlacementBudgetUnavailable`을 기록한다.
- **재사용 증거.** 분배되지 않은 준비된 인스턴스는 예약 slot(`targetID`)이나 그 Workload의 소유가 있을 때만 계획이 다시 쓴다.
- **무한 값.** 용량이 없어 대기, 비우는 시간, 초과량이 정해지지 않으면 기록에서 그 필드가 빠지고 후보의 `worstExceedance`는 float 최댓값이다.
- **DP 비용.** route 시각마다 같은 노드에서 에너지, κ 합, 칸별 요청 에너지가 모두 같거나 나쁜 계획은 빠지고, 칸은 Workload 하나씩 차례로 배정한다. 두 처리는 충족 판정을 바꾸지 않는다.
- **유휴 제거.** 관측 창 하나 이상 어느 Workload에서도 가중치가 0인 인스턴스(controller가 가중치를 잃은 시각을 `unroutedSince`로 붙인다)는 고른 해가 그 인스턴스에 가중치를 주지 않으면 target에서 빠지고 stop, drain, remove로 지운다(`IdleInstanceRemoved:<인스턴스>`). 대상은 자동제어 Workload가 소유한 인스턴스(고른 해의 판정이 그 κ 없이도 성립할 때)와 소유 Workload가 없는 인스턴스(아무도 쓰지 않는다)다. 꺼진 Workload가 소유한 인스턴스는 지우지 않는다. 계획기의 prepare는 target 인스턴스의 Workload를 Pod의 owner(`ownerReferences`의 Workload)로 남기므로 분배가 빠져도 인스턴스의 Workload가 남는다. Pod의 정적 routing annotation은 route가 기록할 때까지 비어 있다. 근거: m4 r6에서 genesis3 q2와 orangepi q1은 owner 없이 routing `{}`라 자동제어 Workload의 인스턴스로 보이지 않아 지워지지 않았다. 목적은 에너지가 아니라 측정이다. 서빙 노드는 측정을 받지 않고, 빈 노드는 다음 판단에서 가장 낮은 모드로 내려간다. controller가 다시 시작하면 시각을 처음부터 잰다.
- **준비한 인스턴스의 분배.** 해가 준비하는 새 인스턴스는 모두 그 해의 가중치를 받는다. 새 인스턴스 하나라도 가중치가 0인 해(지금 가중치를 그대로 쓰는 해 포함)는 후보에서 빠지고 교체 회수의 되돌린 해도 같다. 근거: m4 r7에서 rasp5 q1-lut와 q2(κ 25.69 + 29.30)가 지금 가중치 467/533으로 λ 40.35를 받았고, 적재한 genesis3 q2의 노드 전력(49.80 W)이 빈 노드(49.97 W)보다 작아 그 준비가 T 동안 1.5 J 싸게 나왔다. 그 인스턴스는 가중치 0으로 준비되고 약 80 s 뒤 유휴 제거로 지워졌고, 다음 판단이 다시 준비해 2 req/s와 40 req/s에서 약 100 s 주기로 되풀이했다.
- **만든 뒤 끊기.** 모드를 바꾸지 않는 노드 계획에서 분배된 인스턴스(어느 Workload에서든 가중치가 0보다 큰 인스턴스)의 stop, drain, remove는 그 노드 계획의 route 뒤에만 온다. 자원이 겹쳐 prepare 전에 지워야 하는 분배된 인스턴스가 있는 같은 모드의 교체는 `RoutedInstanceStopsBeforeRoute`로 빠진다. 그래서 그 교체 동안 분배된 κ는 지금 값에서 줄지 않고, route 뒤에는 최종 판정을 지나는 `κ_new`가 된다. 그 노드의 교체는 다른 해가 부하를 옮겨 그 인스턴스가 분배에서 빠진 뒤(유휴 제거) 열린다. 모드 변경은 power 동작 전에 노드의 인스턴스를 모두 멈추고 다른 길이 없으므로(노드 하나가 서빙하는 Workload의 모드 올리기) 후보로 남고, 멈춘 인스턴스는 `κ_ret`에서 빠지며 사슬 시간은 r에 들어가 3장 대기 판정을 받는다. 근거: m4 r7 07:08:31에서 genesis3 q2(κ 70.94, 가중치 532)를 같은 CPU의 q1으로 바꾸는 계획이 q2 stop을 첫 동작으로 두었고, 전환 판정은 rasp5 q1 하나(κ_ret 25.69)로 λ 38.6을 받았다. rasp5 prepare가 68 s 걸리는 동안 SLO는 0%로 떨어졌다. 07:00:38의 rasp5 q1 교체와 06:51:28의 genesis2 교체도 같은 순서였다.
- **교체 회수.** 지금 인스턴스를 지우고 다른 인스턴스를 들이거나 배치 policy를 바꾸는 노드 계획(교체)은, 그 노드만 keep으로 되돌린 해도 충족하면 관측 창의 최소 부하에서 T 동안의 에너지로 비교한다. 최소 부하는 관측 창을 제어 주기로 나눈 구간마다의 도착률 중 가장 작은 값이다. 교체가 그 부하에서 keep보다 에너지가 크거나 같으면 keep으로 되돌리고 `ReplacementUnpaid:<노드>`를 남긴다.
- **부분 관측 창.** ChillServer가 시작한 뒤 관측 창 하나를 다 관측할 때까지 계획은 인스턴스를 더하기만 한다. 지금 인스턴스를 지우거나(그 노드 계획에 남지 않는 인스턴스) 배치 policy를 바꾸는 노드 계획은 `PartialObservationWindow`로 빠진다. 계획은 지금 인스턴스를 노드의 지금 모드에서만 다시 쓰므로, 인스턴스가 있는 노드의 모드 변경은 그 인스턴스를 지우는 계획으로 같이 빠지고 빈 노드의 모드 변경은 확장으로 남는다. 분배된 인스턴스의 가중치를 0으로 만드는 해는 후보에서 빠지며, keep의 가중치가 분배된 인스턴스를 빼면 지금 가중치를 그대로 둔다. 유휴 제거도 쉰다. 근거: m4 r7 07:00:38에서 재시작 19 s 뒤의 관측한 창은 λ 20.6이었다. 부하는 40 req/s였지만 클라이언트가 중단 동안 물러나 있어 ingress가 9.0 req/s(07:00:00~20)와 23.4 req/s(07:00:20~40)만 받았다. 계획은 그 λ로 분배된 rasp5 q1 하나를 지웠다.
- **Overload 준비 유지.** keep이 `Overload`인 판단이 준비한 인스턴스는 Ready가 된 때부터 관측 창 하나 동안(`heldUntil`) 지우거나 교체하는 노드 계획에서 빠지고(`HeldAfterOverload`), 유휴 제거도 받지 않는다. 근거: m4 r5의 40 req/s에서 genesis2를 keep `Overload` 판단(04:28:48)이 준비하고 50 s 뒤 지웠다.
- **계획에서 뺀 노드.** `settings.excludedNodes`(Node 이름 목록, sample은 빈 목록)의 노드는 측정 POOL 노드와 같은 고정 경로를 탄다. capture가 노드 사유 `ExcludedNode`를 붙이고, 그 노드는 keep뿐이며(배치, 교체, 지우기, 전력 변경 없음) 가용 노드 최저 모드 낮춤과 유휴 제거도 받지 않는다. 판단마다 `decision.capacity.plans`에 그 노드의 `ExcludedNode` 계획이 남고 snapshot 노드 `reasons`에도 남는다. 측정 POOL 노드의 인스턴스도 유휴 제거를 받지 않는다.
