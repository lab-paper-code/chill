# 용량표 계획기 (M4)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 용량표 계획기 설계다.
option과 Workload SLO마다 인스턴스 하나가 SLO를 지키는 최대 요청률 κ를 재생으로 계산한다.
ChillServer는 시작과 입력 변경 때 자동제어 Workload의 행을 계산하고,
Tick은 κ 합, 노드 계획과 두 출발점의 이웃 탐색으로 구성을 고른다.
[인스턴스 용량표 근거](capacity-table-evidence.md)와 `hack/kappa`는 계산과 비교의 근거다.
[M0](decision-record-diet.md)와 [M1](observability-split.md)의 기록 경계를 따른다.
본문은 선택 계약이며 실제 source 검증과 배포 실적은 실행 근거로 확인한다.

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

**판정.** option 인스턴스 하나만 있는 keep snapshot에 요청률 λ의 포아송 도착을 넣고 재생 본체를 한 번 돌린 SLO 추정(`schedulerReplayEstimate`)을 읽는다. 재생은 배치마다 profile 실행시간 표본에 `배치 크기 × d`를 더한 시각에 그 배치를 끝내고 다음 배치를 시작한다. 배치 선택(`ChooseBatch`)은 firstfloor와 같은 profile 표본을 쓴다. 재생은 이 저장소의 `internal/firstfloor/policy.go`로 고르고, 배포된 runtime image의 firstfloor는 그보다 앞선 소스로 빌드되어 있다. 두 소스는 같은 배치를 고른다. 분위수는 정렬된 표본의 색인으로 읽어 gonum `stat.Quantile`(Empirical)과 같은 값을 내고, `TestPolicyFastPathsMatchDeployed`가 분위수와 호환 요청 수를 배포 소스의 계산과 비교한다. 백분위 지연(m 포함) ≤ L이고 timeout과 만료가 0이면 그 창은 충족이다. 창 하나는 빈 상태에서 시작하고 요청 수는 `max(capacityMinimumRequests, ⌈100/(1 − p)⌉, ⌈λ × capacityWindow⌉)`다. 100은 백분위 위에 표본 100개를 두는 고정 규칙이다. seed 1~`capacityWindows`의 독립 창 중 `capacityPassingWindows`개 이상이 충족하면 λ는 충족이다. seed의 도착 간격을 λ로 나눠 쓰므로(공통 난수) 판정은 결정적이다.

**κ.** 창 요청 수와 부하에 따른 배칭이 λ와 함께 바뀌므로 판정이 λ에 단조라는 보장이 없다. 그래서 격자 `λ_k = 0.05kμ`(k = 1~20)를 모두 판정하고, 충족 앞부분과 미충족 뒷부분으로 나뉠 때만 κ를 정한다. 마지막 충족 `λ_K`와 다음 격자 사이를 0.5% μ까지 이분 탐색한 값이 κ다. K = 0이면 κ 0, K = 20이면 κ = μ다. 미충족 뒤에 충족이 오면 행은 `NonMonotone`이고 κ가 없으며 그 option은 `CapacityRejected`로 계획에서 빠진다. 디스패치 점유 측정이 없는 행도 `CapacityRejected`(사유 `MissingDispatchOccupancy`)이고 재생 없이 κ가 없다. 격자 결과는 행에 남아 조사에 쓴다. 격자 간격과 이분 탐색 폭은 코드 상수다.

**설정.** `capacityWindowNanos` 60 s, `capacityWindows` 20, `capacityPassingWindows` 19, `capacityMinimumRequests` 1,000. 60 s 창이 필요한 근거: 요청 수 창만 쓰면 빠른 option(genesis2 cuda, 2,000 요청이 14 s)의 κ가 μ에 붙어 relaxed 1.2κ에서 20/20 충족이었고, 60 s 창에서는 0/20이다. 20개 중 19개 기준이 필요한 근거: 창 하나로 정한 κ는 1.0κ에서 6~12/20만 충족했다.

**디스패치 점유.** d는 firstfloor 인스턴스가 요청 하나마다 profile 실행시간 밖에서 더 점유되는 시간이다. 포화한 인스턴스 하나가 배치 b로 낸 지속 처리율 X에서 `d = 1/X − (배치 b의 profile 평균 실행시간)/b`다. d에는 배치 사이 actor 시간(결과 전달, 다음 배치 선택, runner 호출 준비)과 같은 Pod CPU를 쓰는 요청 수신과 응답 처리로 늘어난 runner 실행시간이 함께 들어간다. rasp5 q1(m4 r3 Q2, 15:46~15:51)은 b1로 26.88 req/s, 요청당 37.20 ms였다. Q2 행 전체에서 실측 `serviceMS` 평균은 약 34.5 ms, 실행 기록 사이 간격은 평균 약 3.1 ms다. profile b1 평균은 27.79 ms라 d = 9.41 ms다.
- 기준은 profile 평균이다. 측정한 고정 batch의 `B/(profileMean+B×signed d)`는 X와 같다.
  계획기는 `max(d,0)`를 사용하므로 음수 d를 0으로 투영한 처리율에는 이 등식을 그대로 적용하지 않는다.
  rasp5 q1의 양수 d 사례는 μ 26.88, κ 25.3(m 29 ms)과 25.1(m 139 ms)이다.
  실측 `serviceMS` p50 30.7 ms를 기준으로 잡은 d 6.5 ms는 μ 29.2, κ 27.7로 X를 넘으므로,
  profile 평균과 signed 추정, 정책 투영의 출처를 각각 보존한다.
- 값은 DispatchOccupancyMeasurement가 쓴 record다([measurement-api](measurement-api.md) 3, 4장). `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`에서 `Accepted` record 중 (Node UID, 전력모드, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 골라 snapshot `dispatchOccupancy`(`{node{name, uid}, powerMode, runtimeImage, model, perRequestNanos, source}`)에 싣는다. `perRequestNanos`는 max(d, 0)이고 source는 record key다. 행 입력에 그 항목 전체가 들어가므로 행마다 d와 출처가 남고, 새 측정이 그 행을 다시 계산하게 한다.
- option의 Node UID, profile의 `condition.powerMode`, `condition.runtimeImage`, `condition.logicalModel`이 모두 같은 항목만 쓴다. d의 요청 수신과 응답 처리 비용은 model의 원시 입력과 출력 크기와 전력모드의 CPU 속도에 따라 다르므로 model과 모드마다 잰다. 같은 노드, 모드, runtime image, model의 다른 option(q1-lut, q2, 배치 2)은 같은 d를 쓴다. 맞는 항목이 없는 option의 행은 `MissingDispatchOccupancy`로 κ가 없고, 잴 때까지 계획에서 빠진다. 용량표 행과 재생은 같은 항목을 쓴다.
- 측정은 [measurement-api](measurement-api.md)의 occupancy CR로 수행한다.
  profile/SLO에서 도출한 N/K/L과 창을 동결하고, fixed batch B의 closed-loop 공급 아래
  실제 native batch의 시작 간격으로 X와 signed d를 계산한다.
  HTTP 결과, 물리 연속성, unknown과 idle 상한은 각각 보존한다.
  m은 같은 실행에서 완료 여유의 정의에 맞는 표본으로 별도 계산한다.

**검증된 모양.** 이분 탐색에 쓰지 않은 seed 21~40에서 0.9κ는 모든 option이 20/20, 1.0κ는 16~20/20, 1.2κ는 0/20이다. κ는 절벽의 무릎이다. d 없이 profile 실행시간만 재생한 E1 tight(p95 1 s, m 139 ms) κ: rasp5 q1 34.3, jetsonx 18.3, orangepi 8.1, jetsono 48.1~49.7, orin-nano 38.9~41.3 req/s. rasp5 q1은 실측 처리율 26.9 req/s에서 34.3이 과대임이 드러났고, d를 넣은 값은 위 문단의 25.3이다.

**계산하는 때와 대상.**
- ChillServer의 용량표 worker가 시작할 때 표 전체를 계산한다. 그 뒤에는 Workload의 자동제어가 켜지거나, 켜진 Workload의 SLO, profile, m, 디스패치 점유, 용량표 설정이 바뀔 때 빠진 행을 계산한다. 표는 메모리에만 있고 시작마다 비어 있으므로 재생, 배칭 규칙, κ 계산 코드가 바뀐 image도 그 코드로 계산한 표를 쓴다.
- 대상은 자동제어가 켜진 Workload의 SLO와 그 Workload에 측정 근거가 있는 option 변형이다. 꺼진 Workload의 노드는 keep뿐이라(4장) 그 κ를 쓰지 않는다. E1에서는 tight 하나라 option 변형 38개, 38행이다(SLO 3개 전체는 114행).
- 행 하나가 작업 하나이고 행끼리 독립이라 병렬로 계산한다. ChillServer의 CPU limit은 6(`charts/chill/values.yaml`)이고 `GOMAXPROCS`가 이를 따르므로 서버 안의 병렬은 6 CPU에 묶인다.
- 계산한 행도 기록된 이전 행(계산 시간 문단)도 없으면 그 option은 `CapacityPending`으로 계획에서 빠지고, 켜진 Workload에 κ가 있는 option이 하나도 없으면 판단이 `CapacityPending`이다.
- 행마다 `management` stream에 `capacity-row-built` 사건(행 전체, 계산 시각, 걸린 시간)을 남긴다. 이 사건은 기록이고 판단은 읽지 않는다.

**계산 시간과 이전 행.** 재생은 사건마다 도착했고 끝나지 않은 요청을 훑고,
다음 도착은 도착 순서 목록의 맨 앞에서 읽는다. `Definition.Identity()`는 도착 원형마다 한 번 계산한다.
배치 선택은 정렬된 표본의 분위수 색인과 재사용 작업 공간을 사용한다.
기록된 E1 tight 38행의 단일 코어 계산은 1,386 s였고,
m4/r7의 합성 d 114행 표는 GOMAXPROCS 12에서 132 s, CPU 804 s였다.
이는 해당 입력의 계산 근거다. 최종 E5 입력의 시간은 같은 dump를 사용해 source별로 측정한다.
용량표 worker는 계산 중인 행과 키가 같은 마지막 `capacity-row-built` 기록을
로컬 `management` stream, 보관된 시간 객체의 최근 순으로 읽는다.
이전 행은 `previous`로 표시하고 새 계산이 끝나면 교체한다. 같은 키의 기록이 없으면 `CapacityPending`이다.

**imagenet.** imagenet(p99 100 ms)은 E1 설정 m 139 ms에서 모든 option의 κ가 0이었다(m > 목표 100 ms, `hack/kappa`). 이 Workload는 꺼진 채로 두므로 행을 계산하지 않는다.

## 3. 판정과 순서

**A — 명시적 최소 수요.** `settings.minimumDemand`는 Workload UID별 최소 요청률(req/s)이다.
키는 비어 있지 않아야 하고 값은 유한한 0 이상의 수이며, 생략한 UID의 하한은 0이다.
아래 관측식의 결과를 `λ_observed,w`라 하면 실제 계획률은
`λ_w = max(λ_observed,w, minimumDemand[w])`다.
용량, 전환 대기, 분배와 요청 에너지는 모두 이 유효 요청률을 사용한다.
유휴 제거와 교체 회수의 최소 요청률(`minRate`)에도 같은 하한을 적용한다.
따라서 도착과 대기가 0이어도 하한을 처리하지 못하는 저전력 구성을 고르지 않는다.
이는 예측이나 자동 증가량이 아니라 사용자가 선택한 용량 예약이며 관측 창 보호 규칙을 대체하지 않는다.

구성 c와 route 시각 r에서 켜진 Workload w마다 아래 값을 정확한 κ 합으로 계산한다.

| 값 | 정의 |
|---|---|
| `λ_observed,w` | `max(장기 계산 도착률, 최근 계산 도착률) + q_w / 장기 관측 길이`. 장기는 `observationWindowNanos`, 최근은 `controlIntervalNanos`이며 두 구간의 끝은 `snapshot.observationEnd`다. 장기의 시작은 `max(observationEnd−observationWindow, observedSince)`, 최근의 시작은 `max(장기 시작, observationEnd−controlInterval)`이다. 각 반열린 구간의 `Compute` 또는 `Accepted` 도착 중 불변 정의가 있는 수를 해당 실제 관측 길이로 나눈다. 실패와 취소도 이 도착 정의를 따른다. 미완료 기여는 장기 관측 길이로 한 번 더한다. 장기 관측 길이가 `min(observationWindow, controlInterval)`보다 짧으면 `Pending`(`PartialObservationWindow`)이고, 장기 관측 창이 덜 찬 동안 계획은 확장만 한다 |
| `λ_w` | `max(λ_observed,w, settings.minimumDemand[w])`, 단위 req/s. 이하 용량, 전환 지연, 분배와 요청 에너지가 함께 쓰는 유효 수요다 |
| `κ_new,w` | c의 w 인스턴스 κ 합 |
| `κ_ret,w` | route 전까지 남는 지금 인스턴스의 κ 합. 모드를 바꾸는 노드는 power 동작 전에 인스턴스를 모두 멈추므로 그 인스턴스는 빠진다. 같은 모드의 노드 계획은 분배된 인스턴스를 route 뒤에만 멈춘다(14장 만든 뒤 끊기) |
| `r` | route 전 사슬의 최대 시간(동작마다 측정 trial 최대 시간) |
| `B_w` | `q_w + max(0, λ_w − κ_ret,w) × r`. `q_w`는 지금 대기 수 |

- 최종 충족: `κ_new,w ≥ λ_w`. 초과량 `λ_w / κ_new,w − 1`. λ가 0이어도 켜진 Workload마다 인스턴스 하나 이상이다.
- 대기 충족: `B_w`가 0이면 충족이다. 아니면 EDF가 밀린 요청을 먼저 처리하므로 가장 오래 기다리는 요청의 대기는 `W_w = B_w / κ_new,w`이고, `W_w ≤ L_w − m`이면 충족이다. 초과량 `W_w / (L_w − m) − 1`. keep(r = 0)도 지금 대기 `q_w`로 같은 판정을 받으므로, 측정 λ가 κ 안이어도 줄지 않는 대기는 확장을 부른다(m4 r3: λ 29.5, κ 34.3, 대기 160에서 keep의 W 4.66 s > 0.861 s). κ는 처리율 μ 이하라 이 대기는 실제보다 길다. 밀린 요청이 다 빠지는 시간 `B_w / (κ_new,w − λ_w)`도 기록한다.
- 순서: 모든 w에서 두 충족을 만족하는 구성 중 에너지가 작은 것, keep, 논리 구성 key 사전 순. 그런 구성이 없으면 상태는 `Overload`이고, 모든 w에서 최종 충족(가중치의 κ 검사 포함)을 만족하는 구성이 그렇지 않은 구성보다 앞선다(SLO 우선: 전환 중 대기는 지나가지만 최종 미충족은 남는다). 같은 쪽 안에서는 w와 두 초과량에 걸친 최대 초과량이 작은 것, 에너지 순이다. keep은 r = 0이다. 근거: m4 r5 κ(rasp5 q1 25.3, genesis3 q2 70.9)에서 대기 없는 40 req/s는 keep의 최종 초과량 0.58이 genesis3 추가의 전환 대기 초과량 0.99보다 작아 keep이 골라졌다.
- 근거: 마지막 E1 snapshot에 포아송 40 req/s를 넣으면 에너지만으로는 rasp5 q1 + q1-lut(route 20.2 s)가 나오고 재생에서 전환 중 SLO에서 진다. 위 규칙으로 그 구성은 `B = 5.7 × 20.2 = 115`, `W = 115 / 69.2 = 1.67 s > 0.82 s`(L 1 s, m 177 ms)로 탈락한다. genesis3 q2 추가(route 10.7 s)는 `W = 61 / 144 = 0.42 s`로 충족이고, 재생 계획기와 같은 노드와 option이다.

## 4. 노드 계획과 탐색

**노드 계획.** 노드마다 keep, 그리고 배치 가능한 노드에서 측정 option 변형 하나로 만든 계획 중 그 모드에서 `ValidateNodeBudget`을 만족하는 것. 노드마다 서빙 인스턴스는 하나다. 같은 노드에 인스턴스 둘을 두면 κ가 더해지지 않으므로(m4 r8 D-R8-1: rasp5 q1과 q1-lut의 κ 합이 40 req/s를 덮는다고 판정했지만 둘이 35~38 req/s만 처리했고 SLO가 25.5%로 떨어졌다), 노드에서 더 큰 용량은 CPU 할당이 넓은 측정 option(q2, q4)이 맡는다. 다른 Workload의 인스턴스와 함께 둔 κ와 전력도 재지 않았으므로 이 규칙은 모든 Workload에 같다. 모드를 바꾸는 계획은 목표 모드에서 온라인인 CPU로 예산을 확인한다. 현재 모드는 노드 annotation의 CPU를, 다른 모드는 DeviceClass `powerModes[].onlineCPUs`를 쓰고, 그 필드가 없는 모드의 계획은 `PlacementInventoryMissing`으로 뺀다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이다. 계획마다 Workload별 `κ_new`와 `κ_ret`, 사슬, 전력을 둔다.

**인스턴스와 Workload.** 인스턴스는 Workload 하나에 속하고 κ는 그 Workload와 option의 행이다. 노드마다 서빙 인스턴스는 하나이므로 같은 모델의 Workload들도 노드를 따로 쓴다. 실제 상태에서 인스턴스 둘 이상이 있는 노드는 keep이 그대로 두고, 그 노드의 다른 계획은 인스턴스 하나를 남긴다. 계획기가 만드는 분배에서 인스턴스마다 Workload는 하나다. 서빙 source는 routing annotation이 Workload를 하나 이하로 가리키는 Pod이므로 실제 분배에서도 인스턴스마다 Workload는 하나 이하다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이므로 꺼진 Workload의 SLO는 계획 동작의 영향을 받지 않는다(workload-pools 4.4절의 노드 동작 규칙).

**사슬.** `schedulerPlan`의 순서다. 모드 변경은 power, 모든 기존 remove, prepare가 route 전이다. 같은 모드에서는 자원이 함께 들어가지 않을 때만 remove가 prepare 앞이고, 아니면 route 뒤다. 동작마다 측정 trial의 평균 wall J, 평균 시간, 최대 시간을 쓴다. 비용이 없으면 그 계획은 `MissingOperationCost`로 빠진다. 같은 인스턴스의 배치 상한이나 대기 시간을 바꾸는 계획은 그 인스턴스를 그대로 쓰고 route가 firstfloor policy를 바꾼다(사슬 없음). 서빙 Pod는 측정한 모든 배치를 시작할 때 초기화하고(`batchSizes`, m4 r2의 rasp5 q2 config는 1~16), firstfloor `Scheduler.Update`는 초기화되고 측정된 배치 안에서 policy의 `maxBatchSize`와 `maxBatchWaitMS`를 재시작 없이 바꾸며, route의 `applyPolicy`가 그 값의 native ACK를 기다린다.

target에 인스턴스가 없고 실제로도 빈 노드의 공통 `Fixed` 절전은 다른 노드의 route와 병렬로 계속한다.
target이 사용하는 노드의 `Fixed` 내림, 전력 올리기, remove와 prepare는 같은 노드의 순서를 유지한다.
route는 그 준비와 전력 사슬, 실제 Ready와 소유권, policy ACK와 기존 요청 인계를 유지한다.
공통 절전의 실제 소비와 시간은 전체 POOL 측정에 계속 포함한다.

**실행 중 중간 분배.** 최종 route의 DAG와 분석의 r은 위 전체 준비 사슬을
유지하지만 실제 실행은 그 시각까지 Ready 목적지를 놀리지 않는다. controller는
매 tick에 같은 목표의 실제 Ready 부분집합으로 목표 가중치를 합계 1000으로
재정규화하고, 준비된 목적지가 없는 워크로드의 현재 Ready 분배는 유지하여
한 번의 원자적 route로 적용한다. stop 가능한 인스턴스는 제외한다.
독립 prepare, power, drain 등 어떤 느린 동작도 이 중간 분배의 선행 조건이
아니다. 새 계획 탐색이나 중간 용량의 SLO 충족 보장은 추가하지 않으며,
요청별 측정 지연·수명 admission과 기존 요청 보호는 그대로다.
분석의 B·에너지 예측은 최종 route 시각을 쓰는 기존 식이고, 중간 분배의
실제 효과는 action의 frozen target·before/actual routing 관측으로 구분한다.
구체적인 기록과 실행 계약은 [중간 배분](scheduler-implementation.md#중간-배분)이다.


**비교 기간.** `T = max(comparisonPeriodNanos, 공간의 최장 route 전 사슬 + 최장 route 뒤 remove)`(최대 시간)를 판단마다 하나 정하고 keep과 모든 계획에 쓰며 `decision.capacity.periodNanos`에 남긴다. 모든 사슬이 T 안에 끝난다. 비교 기간을 정하는 방법은 04 명세가 설계에 맡긴다. sample의 `comparisonPeriodNanos`는 사용자 결정으로 10분이다(m4 r5 뒤). T는 에너지 순서만 바꾸고 SLO 판정(3장)은 T와 무관하므로 부하가 오를 때 첫 판단의 확장은 T와 상관없다. m4 r5 기록에서 T만으로는 풀리지 않는 것: 분배되지 않은 genesis3 q2와 orangepi q1은 인스턴스를 띄운 노드 전력(49.80 W, 2.71 W)이 빈 노드(49.97 W, 2.73 W)보다 크지 않아 remove가 어떤 T에서도 에너지 이득이 아니다. 배치 상한 교체(genesis3 b1↔b2)와 SLO 때문에 준비한 인스턴스의 remove는 T 152 s에서 이미 이득(79~1,265 J)이었고 이득은 T에 비례해 커진다. 그래서 14장의 유휴 제거, 교체 회수, Overload 준비 유지 규칙을 둔다.

**에너지.** 노드 계획 하나의 [0, T] 노드 에너지는 keep이면 `P_before × T`, 아니면 `J_pre + P_mid × (r − t_pre) + J_post + P_after × (T − r − t_post)`(t는 평균 시간)다. P는 `schedulerBaseline`의 적재 상태 전력이다. 요청 에너지는 route에서 나눈다: [0, r]은 지금 routing으로 남는 인스턴스가 κ_ret까지 처리하고(`Σ y⁰_i e_i r`), 넘친 `max(0, λ − κ_ret) × r`건과 [r, T]는 target 배정 y로 처리한다(`Σ y_i e_i (T − r)`와 넘친 건수 × y 가중 평균 e). 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**탐색.** 계획 집합(노드마다 계획 하나)은 두 출발점에서 시작하는 이웃 탐색으로 고른다. 출발점은 keep과, 모든 노드를 비운 집합(인스턴스가 없는 계획, 그런 계획이 없는 노드는 keep)이다. 한 걸음은 지금 집합에서 노드 k개 이하의 계획을 바꾼 집합을 모두 평가하고, 순서(충족, 최종 충족, 최대 초과량, 에너지, keep, 계획 key)로 가장 좋은 집합으로 옮긴다. 출발점마다 k = 2에서 더 좋은 이웃이 없으면 그 자리에서 k = 3으로 이어 가고, k = 3에서도 없으면 멈춘다. 두 끝 중 순서가 앞선 집합이 해다. 비운 출발점은 keep에서 한 걸음마다 손해라 막히는, 노드 여럿을 한꺼번에 바꾸는 해에 반대쪽에서 닿는다. 평가는 3장 판정과 위 에너지 식을 정확한 κ 합으로 쓰고, route 시각은 그 집합에서 가장 긴 route 전 사슬이다. 후보에서 빠지는 집합은 셋이다: `maxReplicas`나 `maxInstances`를 넘는 집합(replicas가 꺼져 있으면 Workload별 인스턴스 수가 지금과 다른 집합), 새 인스턴스에 가중치 0을 주는 집합(14장), 관측 창이 덜 찬 동안 분배된 인스턴스를 빼는 집합. 걸음 없이 끝난 비운 출발점도 이 셋을 거친 뒤에만 해가 된다. 순서에서 keep이 앞서므로 완전 동률은 keep이다. 고른 해는 14장의 교체 회수와 최소 이득을 거친다. 한 걸음의 이웃은 `planSearchWorkers`개 goroutine이 첫 번째로 바꾸는 노드 계획을 차례로 나눠 평가한다. 순서가 전순서라 결과는 goroutine 실행 순서와 무관하다. 탐색은 판단 시작부터 `controlIntervalNanos`의 절반 안에 끝난다(k = 2를 두 출발점에서 먼저 마치고 k = 3으로 간다). 그 시각을 넘으면 그때까지 찾은 가장 좋은 집합을 쓰고 기록에 `budgetCut`을 남긴다. 두 출발점 어느 쪽에서도 닿지 않는 해는 놓칠 수 있다. 크기와 근거는 [용량표 근거 7장](capacity-table-evidence.md#7-계획-탐색)이다.

**router 가중치.** 지금 가중치는 그 해의 인스턴스 κ 안에 들면 그대로 둔다. 지금 분배된 인스턴스가 모두 해에 있고, 합이 1,000이며, 모든 인스턴스에서 `(λ_w + B_w / (L_w − m)) × weight / 1,000 ≤ κ_i`이면 그 가중치(새 인스턴스는 0)를 쓴다. 아니면 고른 해의 인스턴스 κ로 e가 작은 인스턴스부터 κ까지 채울 양은 λ이고, 대기가 있는 Workload(`B_w > 0`)에서는 λ에 밀린 요청을 `L − m` 안에 비우는 `B_w / (L_w − m)`을 더한다. 그 양을 채운 몫이나, 양이 κ 합을 넘으면 κ에 비례한 몫을 합 1,000의 정수로 최대 나머지 반올림한다. 반올림 뒤 모든 인스턴스에서 `λ_w × weight / 1,000 ≤ κ_i`를 확인하고, 넘치면 그 인스턴스의 한 단위를 여유가 가장 큰 인스턴스로 옮긴다. 옮길 곳이 없으면 그 해는 `RouteWeightExceedsCapacity`로 최종 미충족이다. 배정 0인 인스턴스는 가중치 0이고, 채울 양이 0이면 같은 가중치다. 요청 에너지의 y는 λ를 가중치대로 나눈 값이다. keep의 가중치가 바뀌면 사슬 없이 route 동작 하나다. 근거: m4 r2의 포아송 40 req/s에서 λ 12.8(관측 창 지연), 대기 160, 남는 인스턴스 없음, 새 인스턴스 넷(κ 42.0, 48.3, 114.6, 144.5)이었고, λ만 채우면 rasp5 q2가 1,000을 받아 대기를 홀로 받았다. B 363.9, `L − m` 0.861 s에서 채울 양 435 req/s가 κ 합 349를 넘으므로 가중치는 120, 138, 328, 414다. m4 r4의 40 req/s에서 판단마다 가중치를 다시 정하면 대기가 있을 때의 비우는 모양과 대기가 없을 때 rasp5 q1을 κ 25.3에 채우는 에너지 모양이 번갈아 75분에 route 동작 181번이었고 rasp5 q1의 1 s 안 응답은 47%였다.

`decision.capacity.workloads[].minimumDemand`는 해당 판단의 명시적 하한이고,
`rate`는 그 하한과 관측 수요 중 큰 값이다. 미설정 하한도 기록에서는 0으로 남긴다.

**판단 기록.** `ExpectedJoules`는 고른 해를 정확한 y로 다시 계산한 위 분석 합이다. `decision.capacity`는 T, Workload별 λ, `κ_new`, `κ_ret`, B, W, 두 초과량, 고른 r, 가중치, 사용한 용량표 행 키, 탐색 범위 `search`(마지막 이웃 크기 `neighborhood`, 평가한 계획 집합 수 `evaluated`, 탐색 시간 `durationNanos`, 시간 예산으로 끝났으면 `budgetCut`), 그리고 노드 계획마다 key, 적격 여부, 제외 사유(`MissingOperationCost`, `CapacityPending`, `CapacityRejected`, `InfeasibleResources`, `FixedWorkload`, `PlacementInventoryMissing`), κ, 사슬 시간, J를 담는다(`SCH-30`). 평가한 계획 집합의 목록은 기록하지 않는다.

`decision.search`의 `windowStart`, `windowEnd`, `recentWindowStart`는 두 구간을 나타낸다.
Workload별 `loads`의 `computeInputs`와 `inputsPerSecond`는 장기 관측값을 유지하고,
`recentComputeInputs`와 `recentInputsPerSecond`는 최근 구간의 계산 도착 수와 요청률이다.
`decision.capacity.workloads`의 `queued`와 `rate`로 미완료 기여를 포함한 최종 λ를 확인한다.
최근 관측률이 장기 관측률보다 큰 Workload의 이미 서빙하던 인스턴스를 지우거나 교체하는 노드 계획은
`DemandIncreasing`으로 빠지고 그 Workload의 분배된 인스턴스는 양수 가중치를 유지한다.
그 Workload의 확장과 다른 Workload의 축소, 교체와 분배 판단은 계속한다.
기존 진행 중 목표의 유지 규칙과 장기 관측 창을 쓰는 유휴 제거, 교체 회수는 유지한다.

**비용.** 한 걸음의 평가 수는 노드 계획 수의 쌍곱(k = 2)과 세 쌍곱(k = 3)에 비례하고 요청 수와 무관하다. 평가 하나는 약 5 µs다. E5 규모의 노드 계획 수, 걸음 수, 탐색 시간과 정확한 최소 대비 에너지 차는 [용량표 근거 7장](capacity-table-evidence.md#7-계획-탐색)이다.

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
- 값(p95, `stat.Empirical`): E1 2 req/s 177.8 ms, m4 r2 고정 2 req/s 28.5 ms, m4 r3 고정 2 req/s 25.8 ms, m4 r3 포아송 40 req/s 포화 29.2 ms(첫 10,000행). κ가 정해지는 부하는 포화 근처이므로 설정값은 29 ms다. 캠페인 뒤 SmolVLM2와 PLC 요청으로 m을 다시 재고, Workload마다 잰 값 중 가장 큰 값으로 정한다.
- E1과 m4의 차이는 인증이다. E1의 received → validated p95가 103.2 ms였고, 요청마다 TokenReview가 client-go 요청 한도에 걸렸다. c829518(허용된 인증 결과를 10 s 재사용) 뒤 m4에서는 12.6~16.1 ms다. 나머지(firstfloor로 전달, ingress p95 약 9.5 ms, 응답 교환 p95 약 5~6 ms)는 두 실행에서 비슷하다. `queueMS` 전체를 빼는 열(E1 169.0 ms, m4 17.9~22.1 ms)은 firstfloor ingress를 m과 재생 어느 쪽에도 넣지 않는다.

## 6. 인스턴스와 Workload 경계

인스턴스 하나는 Workload 하나의 요청을 처리하고, 노드 하나에는 서빙 인스턴스 하나를 둔다.
같은 실행 조건의 Workload는 profile을 재사용하며 부하, 대기, SLO와 결과는 각각 유지한다.
snapshot 인스턴스의 `WorkloadUID`와 Pod owner/routing 참조는 같은 소유권을 나타낸다.
`StaticRoutingTarget`은 인스턴스 하나를 두 Workload에 배정하는 설정을 거절한다.
꺼진 Workload가 소유한 인스턴스의 노드는 keep으로 고정한다.
일반 runtime 큐와 firstfloor 배치 규칙은 유지한다.
같은 모델의 인스턴스 공유 정리는 [M5 경계](../platform/cleanup-m5.md#5-인스턴스-공유-정리)에서 다룬다.

## 7. 설정과 저장 계약

| 대상 | 계약 |
|---|---|
| 최소 수요 | `minimumDemand`는 Workload UID→유한한 비음수 req/s map이다. 빈 UID는 거절하고 미설정 UID는 0이다. `decision.settings`와 `decision.search.bounds`에 선택값을 동결하며 캠페인의 binding/settings 캡처와 summary에 연결한다([저장 경로](measurement-api.md#71-최소-수요와-재현-설정)) |
| 용량표 설정 | `capacityWindowNanos`>0, `capacityWindows`≥1, `capacityPassingWindows`는 1부터 전체 창 수까지, `capacityMinimumRequests`≥1이다 |
| 비교 기간 | `comparisonPeriodNanos`>0이며 sample 하한은 10분이다. 판단은 실제 T를 `decision.capacity.periodNanos`에 남긴다 |
| 디스패치 점유 | snapshot의 `dispatchOccupancy`는 immutable 측정 record에서 도출한다. 행 입력에 Node UID, mode, runtime/model, 값과 source를 포함한다 |
| 탐색 병렬 | `planSearchWorkers`≥1이다. sample은 3이고, 실제 CPU 한도와 gateway/Tick 관측으로 평가한다 |
| 판단 기록 | `decision.capacity`는 구성의 판정, 에너지, 사슬과 `search`를 기록한다. `management`의 `capacity-row-built`는 행과 계산시간을 기록한다 |
| 인스턴스 시간 | `unroutedSince`와 `heldUntil`은 controller의 메모리 상태이며 유휴 제거와 Overload 준비 유지에 사용한다 |
| 최소 이득 | `OperationTrial.energyUncertaintyJoules`와 실제 선택한 `NodeBaseline.wattsUncertainty`를 signed 노출량으로 전파한다. `gainUncertaintyJoules`는 B-only 문턱이다 |

설정 decode는 strict하다. API, ConfigMap과 Dashboard는 같은 설정 계약을 사용한다.
저장 형태는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 single-version reader를 따른다.
Timing이 없는 비용의 미기록 B를 관측한 0으로 취급하지 않는다.
G2의 [소비자 목록](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json)은
catalog legacy 49개와 그중 E5 노드의 ranking baseline 9개를 확인했다.
동결된 E5 후보 에너지 입력의 legacy 0개는 전체 reader 정리의 완료와 구분한다.
승인된 ranking9 재측정은 2 roots, 18 steps이며 실제 최저선정의 조건부 직접 전환을 포함한 상한은 4 roots, 22 steps다.
대체 확인과 검증된 외부 백업 뒤 raw 80개 키와 terminal OCM 6개를 정리하며, old ranking9는 80개에 포함된다.
같은 승인 조건의 대체와 옛 기록 정리는 연속 실행한다.
각 root의 실제 소비, 정상 반환과 첫 BEFORE 전 공통 입력 동결은 [측정 API 저장 계약](measurement-api.md#7-저장-계약-adr-0019)을 따른다.
원 raw/status와 외부 과학 증거는 보존하며 재측정, 삭제와 reader 제거 완료는 실제 근거로 기록한다.

## 8. 측정 입력의 영향 범위

profile, runtime 또는 측정 계약을 바꾸는 변경에는 해당 NodeUID/mode/model의 d,
정확 profile ID를 쓰는 prepare/remove 비용, loaded/unloaded baseline과 전력 전환 비용 목록을 함께 남긴다.
새 profile의 완료와 비용 연결의 완료는 각각 확인한다.
대표 한 건에서 생성, 실행, raw, projection, 계획기 선택과 실물 반환을 확인한 뒤 대량 수집한다.

mobilenet CPU 8개와 GPU 2개는 새 규칙 미재인증 legacy로 소비한다.
새 규칙의 실제 Accepted 대체 전까지 원본과 소비를 보존한다.
D2 뒤 재측정 범위는 CPU 8개, GPU 5개, PLC 12개다.
Smol D04/D09의 새 record는 소비 source를 대체하며 옛 CPU 2개의 raw/status는 보존한다.
모드나 identity가 맞는 d가 없으면 `MissingDispatchOccupancy`, 정확 비용이 없으면 `MissingOperationCost`로 남긴다.

수요 판단과 전환 사슬의 영향 측정은 E1의 무부하에서 유부하 전환, E4의 이동 사슬,
E5-0의 첫 구성과 공통 절전, E5-1의 첫 유입, E5-2와 E5-3의 증가,
E5-4의 과부하, E5-5의 재증가, E5-6의 유지 전력과 E5-S의 선택 구성 비교다.
각 구간은 두 관측률과 최종 λ, 보호된 용량, 실제 route 적용 시각,
준비와 전력 사슬, 모든 유효 offered 결과와 전환 포함 전체 POOL 에너지를 연결한다.
runtime 7종 digest, profile, d, 완료 여유, 비용과 baseline 입력 및 판정은 유지한다.
요청 에너지의 기존 λ 채움 모델과 실제 가중치의 차이는 같은 추정 한계로 보존한다.

## 9. 검사 계약

| 대상 | 확인할 불변식 |
|---|---|
| 재생과 용량표 | 같은 입력의 결정성, 격자의 NonMonotone 거절, 창 요청 수와 통과 창 수 경계, m과 SLO 경계, 자동제어 대상 행만 계산 |
| 입력 갱신 | SLO, profile, m, d와 설정 변경의 행 재계산, 같은 행 키의 이전 기록과 CapacityPending 구분 |
| 전환 판정 | 모든 Workload의 κ_new/κ_ret와 B/W, keep의 대기 판정, 모드 변경의 retained capacity, route 뒤 remove |
| 노드 계획 | 노드당 인스턴스 하나, 자원 예산, FixedWorkload, static routing의 단일 소유권, 전력모드별 CPU inventory |
| 탐색 | 작은 공간의 brute force 대조, k=3과 비운 출발점, replica 상한, 시간 예산과 그때까지의 해, 정렬과 완전 동률 keep |
| 에너지 | 모든 계획의 동일 T, route 전후 요청 에너지, 사슬 전체 비용, nominal 평균 불변 |
| 최소 이득 | 선택 근거의 signed 상쇄, 이득=B keep, 이득>B 전환, keep infeasible 회복, exact baseline과 무관한 profile CI의 독립성 |
| route와 기록 | 가중치 합 1000과 인스턴스별 κ 경계, 배치 policy ACK, 제외 사유, 실제 탐색 범위와 budgetCut |

검사 결과에는 실행한 source SHA, 입력과 실제 결과를 연결한다.
이 표는 수용 조건이며 새 source의 검증 통과나 배포를 뜻하지 않는다.

## 10. d와 불확실성 해석

signed d는 원시 추정으로 보존하고 계획기는 `max(d,0)`를 사용한다.
Smol D04/D09/D14/D23/D33은 profile 포함 95% 근사구간이 0 아래인
known systematic profile-versus-serving bias다.
정확 identity 일치와 harness/warmup/timing envelope/측정 시점의 관측 차이는 기록에 남긴다.
원인별 인과 기여는 미확정이다. E5 해석에 이 한계를 포함하고 bias 보정과 d 추정정책 대안은 M5 후보로 둔다.
[정확 수치와 identity](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.json)와
[관측/가설 구분](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.txt)이 근거다.

비용의 `E±B`, `B=S+G`는 [측정 API](measurement-api.md#21-전력-곡선과-민감도)를 따른다.
minimumGain은 실제 nominal에 선택된 evidence의 B만 전파한다.
profile 통계오차와 BEO idle fallback CI는 이 문턱 밖의 추정 한계이며 global95% gain 보장은 없다.

## 11. 확인

| 확인 | 방법 | 보고할 결과 |
|---|---|---|
| 오프라인 비교 | 동일한 최종 scheduler dump로 source별 `hack/kappa`를 순차 실행 | 입력 provenance, κ/status, 계산시간과 후보 에너지 차이 |
| 실클러스터 비교 | 승인된 rev72 BEFORE와 D2 AFTER의 자율 MNV 및 고정 native8 쌍 | 실제 Tick/search, CPU/GC/mesh, 지연과 처리 결과, G와 보존 identity |
| 디스패치 점유 | 측정 API의 native 원장과 immutable record 대조 | signed d, 판정 사유, max0 projection과 실제 source |
| 에너지 | 판단의 ExpectedJoules와 같은 비교 집합, 전체 POOL을 각각 실측 | 동일 시간창의 예측 차이와 전환 포함 전체 에너지 |
| E5 용량/SLO | 최종 d/G/m에서 정한 Workload SLO와 κ 표 | 실제 유효 요청 전체 분모, 완료/실패/만료/미완, 단계와 1분 관측 |
| 재진입 | 비용이 있는 가용 노드의 decision.capacity | 실제 사슬 전체를 포함하는 T, 비용과 retained capacity |

정량 SLO/m/G/κ/R과 평가 threshold는 D2 뒤 실측 입력으로 정한다.
과거 E1/m4 수치는 해당 입력의 근거로 보존하며 E5 결과는 E5 Run ID와 원시 경로로 연결한다.

## 12. 근거 보존

조건 생성기, 승인 원문, manifest, profile/runtime/NodeUID/source SHA와 원시 위치를 선별해 연결한다.
`.token`, Secret 스냅샷과 전체 캐시 백업은 저장소와 증거 인덱스에서 제외한다.
캐시의 [d 결정](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/d-measurement-decision.md)과
[D2/E5 승인](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/deployment2-e5-proposal-20261024.md)은 본문 계약의 결정 근거다.

## 13. 실행 경계

BEFORE 보존, 유한 진단, D2와 E5의 순서는 [워크로드 POOL 검증 계획](workload-pools.md#9-검증-계획)을 따른다.
runtime 7종의 digest와 기존 자원, evidence store를 유지한다.
제어 rollout은 유한 진단 최대 1회와 최종 D2 최대 1회다.
실제 paired 물리 보존 실패는 unknown/mismatch로 보고하고 paired 주장을 중단한다.
새 조건, 판정 또는 범위 변경은 해당 근거와 함께 승인받는다.

## 14. 구현 규칙

4장의 계획기를 `internal/serving/scheduler_capacity.go`가 다음 규칙으로 구현한다.

- **설정 gate.** placement, resources, batching, membership이 꺼져 있으면 option 변형은 그 Workload의 지금 인스턴스 하나와 노드, CPU와 GPU 선택, 배치 상한과 대기 시간이 같아야 한다(membership은 그 Workload의 인스턴스가 있는 노드만). `maxReplicas`와 `maxInstances`는 POOL 전체의 Workload별 인스턴스 수와 전체 인스턴스 수 상한이고, replicas가 꺼져 있으면 Workload별 인스턴스 수가 지금 수와 같다. 탐색은 이 수를 집합마다 세어 상한을 정확히 지킨다. `maxCPUs`는 인스턴스마다의 CPU 수 상한이다. routing이 꺼져 있으면 남는 인스턴스는 지금 가중치를, 새 인스턴스는 option의 가중치를 쓰고 같은 κ 검사를 받는다.
- **진행 중 전환.** 전역 계획기는 하나다. owner별 예약의 Node·Workload UID·Pod UID·physical ID와 frozen target만 고정하고 예약 밖 후보 평가·확장·준비된 용량 배분은 계속한다. 관측 불명 또는 κ pending Workload도 해당 범위만 고정한다. 실패·효과 소실은 해당 owner DAG에 기록하며 다른 owner의 목표를 버리지 않는다. 실패한 준비는 관측 창 동안 `PreparationFailed`로 제외한다.
- **동작 기한.** prepare는 cost의 측정 trial 최대 시간에 관측 창 하나를 더한 시간 안에 실행된다. 초과하면 `failed`/`ActionDeadlineExceeded`로 실행을 끝내되 원격 효과 미확인 예약은 유지한다. 취소도 같은 계약이다. 이후 효과 확인 또는 reset의 실제 정리 proof 뒤에만 예약을 반환하고 현재 실제 상태에서 재계획한다. 독립 범위의 다른 동작과 판단은 계속한다.
- **가중치 척도.** 지금 가중치의 합이 1,000이 아니면 첫 판단이 route 동작 하나로 1,000 척도의 가중치를 적용한다.
- **제외 사유.** 4장의 사유에 더해 기준 전력이 없으면 `MissingUnloadedNodeBaseline:<노드>:<모드>`, 측정 POOL 노드는 `MeasurementNode`, 준비되지 않은 노드는 `NodeUnavailable`, 비서빙 예산 노드는 `PlacementBudgetUnavailable`을 기록한다.
- **재사용 증거.** 분배되지 않은 준비된 인스턴스는 예약 slot(`targetID`)이나 그 Workload의 소유가 있을 때만 계획이 다시 쓴다.
- **무한 값.** 용량이 없어 대기, 비우는 시간, 초과량이 정해지지 않으면 기록에서 그 필드가 빠지고 후보의 `worstExceedance`는 float 최댓값이다.
- **탐색 비용.** 평가 하나는 노드 8개에서 약 5 µs이고 이웃 크기는 노드 계획 수의 쌍곱(k = 2)과 세 쌍곱(k = 3)이다. 시간 예산은 판단 시작부터 재므로 노드 계획 만들기도 그 안에 든다.
- **유휴 제거.** 관측 창 하나 이상 어느 Workload에서도 가중치가 0인 인스턴스는,
  고른 해도 가중치를 주지 않으면 stop, drain, remove로 정리한다(`IdleInstanceRemoved:<인스턴스>`).
  controller는 가중치를 잃은 시각을 `unroutedSince`에 기록한다.
  대상은 그 인스턴스의 κ 없이도 판정이 성립하는 자동제어 Workload 소유 인스턴스와 소유 Workload가 없는 인스턴스다.
  꺼진 Workload 소유 인스턴스는 유지한다. prepare는 Workload를 Pod owner로 남기고 routing annotation은 route가 기록한다.
  이 규칙은 측정 가능한 빈 노드를 확보한다. 빈 노드는 다음 판단에서 가장 낮은 모드로 내려간다.
  controller가 다시 시작하면 유휴 시각을 처음부터 잰다.
- **준비한 인스턴스의 분배.** 해가 준비하는 새 인스턴스는 모두 그 해의 가중치를 받는다. 새 인스턴스 하나라도 가중치가 0인 해(지금 가중치를 그대로 쓰는 해 포함)는 후보에서 빠지고 교체 회수의 되돌린 해도 같다. 근거: m4 r7에서 rasp5 q1-lut와 q2(κ 25.69 + 29.30)가 지금 가중치 467/533으로 λ 40.35를 받았고, 적재한 genesis3 q2의 노드 전력(49.80 W)이 빈 노드(49.97 W)보다 작아 그 준비가 T 동안 1.5 J 싸게 나왔다. 그 인스턴스는 가중치 0으로 준비되고 약 80 s 뒤 유휴 제거로 지워졌고, 다음 판단이 다시 준비해 2 req/s와 40 req/s에서 약 100 s 주기로 되풀이했다.
- **만든 뒤 끊기.** 모드를 바꾸지 않는 노드 계획에서 분배된 인스턴스(어느 Workload에서든 가중치가 0보다 큰 인스턴스)의 stop, drain, remove는 그 노드 계획의 route 뒤에만 온다. 자원이 겹쳐 prepare 전에 지워야 하는 분배된 인스턴스가 있는 같은 모드의 교체는 `RoutedInstanceStopsBeforeRoute`로 빠진다. 그래서 그 교체 동안 분배된 κ는 지금 값에서 줄지 않고, route 뒤에는 최종 판정을 지나는 `κ_new`가 된다. 그 노드의 교체는 다른 해가 부하를 옮겨 그 인스턴스가 분배에서 빠진 뒤(유휴 제거) 열린다. 모드 변경은 power 동작 전에 노드의 인스턴스를 모두 멈추고 다른 길이 없으므로(노드 하나가 서빙하는 Workload의 모드 올리기) 후보로 남고, 멈춘 인스턴스는 `κ_ret`에서 빠지며 사슬 시간은 r에 들어가 3장 대기 판정을 받는다. 근거: m4 r7 07:08:31에서 genesis3 q2(κ 70.94, 가중치 532)를 같은 CPU의 q1으로 바꾸는 계획이 q2 stop을 첫 동작으로 두었고, 전환 판정은 rasp5 q1 하나(κ_ret 25.69)로 λ 38.6을 받았다. rasp5 prepare가 68 s 걸리는 동안 SLO는 0%로 떨어졌다. 07:00:38의 rasp5 q1 교체와 06:51:28의 genesis2 교체도 같은 순서였다.
- **교체 회수.** 지금 인스턴스를 지우고 다른 인스턴스를 들이거나 배치 policy를 바꾸는 노드 계획(교체)은, 그 노드만 keep으로 되돌린 해도 충족하면 관측 창의 최소 부하에서 T 동안의 에너지로 비교한다. 최소 부하는 관측 창을 제어 주기로 나눈 구간마다의 도착률 중 가장 작은 값이다. 교체가 그 부하에서 keep보다 에너지가 크거나 같으면 keep으로 되돌리고 `ReplacementUnpaid:<노드>`를 남긴다.
- **최소 이득.** 고른 해가 충족하고 노드 하나라도 바꾸며 keep도 충족하면, T 동안 nominal 이득(keep−해)이 선택된 operation과 baseline의 조건부 B 민감도 문턱보다 클 때 해를 쓴다. 같은 immutable reference/trial의 signed 노출량을 먼저 합쳐 공유 근거를 상쇄하고 남은 절댓값을 합산한다. trial 평균은 B/N을 선형 합산하며 baseline watts 민감도는 실제 초를 곱한다. 이득이 문턱과 같거나 작으면 keep과 `TransitionGainWithinUncertainty`를 기록한다. keep infeasible이면 용량/SLO 회복을 유지한다. 문턱은 profile 통계오차와 BEO idle fallback CI 및 시점/센서/모델 오차를 포괄하지 않으며 global95% gain 구간을 뜻하지 않는다. 원시 profile 분산/CI, d의 profile 포함95% 분석과 κ/SLO 계산은 각 추정 계약을 유지한다.
- **부분 관측 창.** ChillServer가 시작한 뒤 관측 창 하나를 다 관측할 때까지 계획은 인스턴스를 더하기만 한다. 지금 인스턴스를 지우거나(그 노드 계획에 남지 않는 인스턴스) 배치 policy를 바꾸는 노드 계획은 `PartialObservationWindow`로 빠진다. 계획은 지금 인스턴스를 노드의 지금 모드에서만 다시 쓰므로, 인스턴스가 있는 노드의 모드 변경은 그 인스턴스를 지우는 계획으로 같이 빠지고 빈 노드의 모드 변경은 확장으로 남는다. 분배된 인스턴스의 가중치를 0으로 만드는 해는 후보에서 빠지며, keep의 가중치가 분배된 인스턴스를 빼면 지금 가중치를 그대로 둔다. 유휴 제거도 쉰다. 근거: m4 r7 07:00:38에서 재시작 19 s 뒤의 관측한 창은 λ 20.6이었다. 부하는 40 req/s였지만 클라이언트가 중단 동안 물러나 있어 ingress가 9.0 req/s(07:00:00~20)와 23.4 req/s(07:00:20~40)만 받았다. 계획은 그 λ로 분배된 rasp5 q1 하나를 지웠다.
- **Overload 준비 유지.** keep이 `Overload`인 판단이 준비한 인스턴스는 Ready가 된 때부터 관측 창 하나 동안(`heldUntil`) 지우거나 교체하는 노드 계획에서 빠지고(`HeldAfterOverload`), 유휴 제거도 받지 않는다. 근거: m4 r5의 40 req/s에서 genesis2를 keep `Overload` 판단(04:28:48)이 준비하고 50 s 뒤 지웠다.
- **계획에서 뺀 노드.** `settings.excludedNodes`(Node 이름 목록, sample은 빈 목록)의 노드는 측정 POOL 노드와 같은 고정 경로를 탄다. capture가 노드 사유 `ExcludedNode`를 붙이고, 그 노드는 keep뿐이며(배치, 교체, 지우기, 전력 변경 없음) 가용 노드 최저 모드 낮춤과 유휴 제거도 받지 않는다. 판단마다 `decision.capacity.plans`에 그 노드의 `ExcludedNode` 계획이 남고 snapshot 노드 `reasons`에도 남는다. 측정 POOL 노드의 인스턴스도 유휴 제거를 받지 않는다.
