# 용량표 계획기 (M4)

상태: 2026-10-30 **2차 검토(fusion: 패널 3모델+판정) 반영**. 아래 가변 격자·tail 설정은 구현 전 변경 계약이며 현재 코드 완료 주장이 아니다.

[이행 대안](../architecture-rethink/README.md)과 구분한 현행 용량표 계획기 계약이다.
option과 Workload SLO마다 인스턴스 하나가 SLO를 지키는 최대 요청률 κ를 재생으로 계산한다.
ChillServer는 시작과 입력 변경 때 자동제어 Workload의 행을 계산하고,
Tick은 κ 합, 노드 계획과 두 출발점의 이웃 탐색으로 구성을 고른다.
[인스턴스 용량표 근거](capacity-table-evidence.md)와 `hack/kappa`는 계산과 비교의 근거다.
[M0](decision-record-diet.md)와 [M1](observability-split.md)의 기록 경계를 따른다.
본문은 선택 계약이며 실제 source 검증과 배포 실적은 실행 근거로 확인한다.

## 1. 측정한 문제

- 재생 Tick은 `replays=10/maxCandidates=32`에서 0.633/2/5 req/s의 p50 913/3,475/7,495 ms, 최대22,575 ms·CPU499m(D-16); 기본100/128에서는133회30초 deadline 실패(D-1).
- 깊이 우선 후보127/128건이32에서 잘리고 재진입은37~48번째(D-22). 빈 노드 전환63.5/134.5/197.1초(D-20/21)가 당시60초 horizon을 넘었다.
- 포아송 재생 p95는 지연 공식 상한(여유율.8)의1.8~3.6배여서 동일 재생의 κ를 채택했다. 에너지 E1 예측499,333 J/실측495,741 J(+.7%)는 해당 입력의 근거이며 새 모델의 보장이 아니다.
- 정렬·고정 seed·완전 동률 keep을 유지한다. 탐색 선택 근거와 측정 표는 [용량표 근거](capacity-table-evidence.md)에 둔다.

## 2. 용량표

**행.** 키는 option(노드, 모드, 할당, profile ID), 배치 상한 b, SLO(백분위 p, 목표 L), 완료 여유 m, 디스패치 점유 d와 그 출처, 용량표 설정이다. deadlinePolicy는 profile이 정한다. 행은 입력, κ, μ(b 이하 배치의 `b / (평균 실행시간 + b × d)` 최댓값), 격자 판정 결과, 창별 백분위 지연을 담는다.

**판정.** option 인스턴스 하나만 있는 keep snapshot에 요청률 λ의 포아송 도착을 넣고 `schedulerReplayEstimate`를 읽는다. 재생은 profile 실행시간 표본에 `배치 크기 × d`를 더하고 firstfloor `ChooseBatch`를 사용한다. 저장소와 배포 runtime의 표본 선택 동등성은 `TestPolicyFastPathsMatchDeployed`로 대조한다. 백분위 지연(m 포함) ≤ L이고 timeout·만료가0이면 창은 충족이다. 창은 빈 상태에서 시작하고 요청 수는 `max(capacityMinimumRequests, ceil(capacityTailSamples/(1−p/100)), ceil(λ×capacityWindow))`다(p는0~100 백분위). p=100은 현재 별도 분기를 유지한다. seed1~capacityWindows 중 capacityPassingWindows 이상 충족하면 λ가 충족이다. 같은 seed 간격을 λ로 나누므로 판정은 결정적이다.

**κ.** 요청 수와 배칭이 λ에 따라 바뀌므로 단조성을 가정하지 않는다. `G=capacityGridSteps`, `λ_k=(k/G)μ`(k=1~G)를 전부 판정한다. 충족 앞부분/미충족 뒷부분으로 나뉠 때 마지막 충족과 다음 격자 사이를 `capacityBisectionWidth×μ`까지 이분 탐색한다. K=0이면 κ0, K=G이면 κ=μ다. 미충족 뒤 충족이면 NonMonotone으로 κ가 없고 CapacityRejected다. 디스패치 점유가 없으면 MissingDispatchOccupancy로 재생 없이 거절한다. 격자 결과는 행에 보존한다. 격자·이분 폭·tail은 코드 상수가 아니라 기존 settings의 추가3개이며 기본20/.005/100을 유지한다. 모두 CapacityInput/용량 행에 포함하고 기본값도 새 입력 키로 재계산한다([설정 설계](configuration-hardcoding.md#3-serving-settings와-요청별-reuse)).

**불가 조기 판정.** b 이하 모든 측정 배치의 관측 최소 실행시간 중 최솟값 + m > L이면 백분위와 무관하게 단일 배치 처리만으로 모든 요청이 초과하므로 재생 없이 `Built`, κ 0, `BatchServiceExceedsSLO`와 `minimumServiceNanos`(m·L은 입력)를 기록한다(등호는 재생); 재생 창은 확정 초과 요청이 `N − ⌈pN⌉`개를 넘거나 timeout·만료가 하나라도 있으면 즉시 실패(-1, 완전 분위수 없음)로 끝내며, `Ensure`의 최신 대상에서 빠진 queued/running 행은 취소하고 부분 결과와 기록을 버린다.

**설정.** `capacityWindowNanos` 60 s, `capacityWindows` 20, `capacityPassingWindows` 19, `capacityMinimumRequests` 1,000. 60 s 창이 필요한 근거: 요청 수 창만 쓰면 빠른 option(genesis2 cuda, 2,000 요청이 14 s)의 κ가 μ에 붙어 relaxed 1.2κ에서 20/20 충족이었고, 60 s 창에서는 0/20이다. 20개 중 19개 기준이 필요한 근거: 창 하나로 정한 κ는 1.0κ에서 6~12/20만 충족했다.

추가 설정의 범위는 capacityGridSteps·capacityTailSamples 정수≥1(Go 표현 범위), capacityBisectionWidth finite (0,1]다. 옛 κ 키 유지용 분기·dual lookup은 없다. 기본 결과 μ1000/κ959.375와 Grid/Bisection 판정은 변경 전 fixture와 대조한다.
`SchedulerSettingsInput`이 생략값 20/.005/100을 한 번 채우고 ConfigMap·HTTP·직접 κ 도구가 같은 경계를 사용한다. 실제 선택값은 Decision.Settings와 CapacityInput에 기록한다. 부동소수점 해상도보다 작은 폭에서는 중점이 더 이상 이동하지 않을 때 종료한다.


**디스패치 점유.** d는 firstfloor 인스턴스가 요청 하나마다 profile 실행시간 밖에서 더 점유되는 시간이다. 포화한 인스턴스 하나가 배치 b로 낸 지속 처리율 X에서 `d = 1/X − (배치 b의 profile 평균 실행시간)/b`다. d에는 배치 사이 actor 시간(결과 전달, 다음 배치 선택, runner 호출 준비)과 같은 Pod CPU를 쓰는 요청 수신과 응답 처리로 늘어난 runner 실행시간이 함께 들어간다. rasp5 q1(m4 r3 Q2, 15:46~15:51)은 b1로 26.88 req/s, 요청당 37.20 ms였다. Q2 행 전체에서 실측 `serviceMS` 평균은 약 34.5 ms, 실행 기록 사이 간격은 평균 약 3.1 ms다. profile b1 평균은 27.79 ms라 d = 9.41 ms다.
- 기준은 profile 평균이며 fixed `B/(profileMean+B×signed d)=X`다. max0 투영에는 등식이 그대로 성립하지 않는다. rasp5 양수d는 μ26.88/κ25.3(m29)/25.1(m139)이고, service p50 30.7ms 기준 d6.5ms는 μ29.2/κ27.7로 X를 넘었다. profile 평균·signed 추정·정책 출처를 보존한다.
- 값은 DispatchOccupancyMeasurement가 쓴 record다([measurement-api](measurement-api.md) 3, 4장). `CostEvidence.Refresh`가 `serving/dispatch-occupancy/`에서 `Accepted` record 중 (Node UID, 전력모드, runtime image, model)마다 `measuredAt`이 가장 늦은 것을 골라 snapshot `dispatchOccupancy`(`{node{name, uid}, powerMode, runtimeImage, model, perRequestNanos, source}`)에 싣는다. `perRequestNanos`는 max(d, 0)이고 source는 record key다. 행 입력에 그 항목 전체가 들어가므로 행마다 d와 출처가 남고, 새 측정이 그 행을 다시 계산하게 한다.
- option의 Node UID, profile의 `condition.powerMode`, `condition.runtimeImage`, `condition.logicalModel`이 모두 같은 항목만 쓴다. d의 요청 수신과 응답 처리 비용은 model의 원시 입력과 출력 크기와 전력모드의 CPU 속도에 따라 다르므로 model과 모드마다 잰다. 같은 노드, 모드, runtime image, model의 다른 option(q1-lut, q2, 배치 2)은 같은 d를 쓴다. 맞는 항목이 없는 option의 행은 `MissingDispatchOccupancy`로 κ가 없고, 잴 때까지 계획에서 빠진다. 용량표 행과 재생은 같은 항목을 쓴다.
- occupancy CR은 profile/SLO 유래 N/K/L·창을 동결하고 closed-loop fixed B의 native batch 시작 간격으로 X/signed d를 구한다. HTTP 결과·물리 연속성·unknown/idle 상한과 별도 m 표본은 [측정 API](measurement-api.md)를 따른다.

**검증된 모양.** 이분 탐색에 쓰지 않은 seed 21~40에서 0.9κ는 모든 option이 20/20, 1.0κ는 16~20/20, 1.2κ는 0/20이다. κ는 절벽의 무릎이다. d 없이 profile 실행시간만 재생한 E1 tight(p95 1 s, m 139 ms) κ: rasp5 q1 34.3, jetsonx 18.3, orangepi 8.1, jetsono 48.1~49.7, orin-nano 38.9~41.3 req/s. rasp5 q1은 실측 처리율 26.9 req/s에서 34.3이 과대임이 드러났고, d를 넣은 값은 위 문단의 25.3이다.

**계산하는 때와 대상.**
- ChillServer의 용량표 worker가 시작할 때 표 전체를 계산한다. 그 뒤에는 Workload의 자동제어가 켜지거나, 켜진 Workload의 SLO, profile, m, 디스패치 점유, 용량표 설정이 바뀔 때 빠진 행을 계산한다. 표는 메모리에만 있고 시작마다 비어 있으므로 재생, 배칭 규칙, κ 계산 코드가 바뀐 image도 그 코드로 계산한 표를 쓴다.
- 대상은 자동제어가 켜진 Workload의 SLO와 그 Workload에 측정 근거가 있는 option 변형이다. 꺼진 Workload의 노드는 keep뿐이라(4장) 그 κ를 쓰지 않는다. E1에서는 tight 하나라 option 변형 38개, 38행이다(SLO 3개 전체는 114행).
- 행 하나가 작업 하나이고 행끼리 독립이라 병렬로 계산한다. ChillServer의 CPU limit은 6(`charts/chill/values.yaml`)이고 `GOMAXPROCS`가 이를 따르므로 서버 안의 병렬은 6 CPU에 묶인다.
- 빠진 행은 Workload마다 다음 행 하나씩 round-robin으로 공용 semaphore에 투입하여, 행이 많은 Workload 뒤에서 다른 Workload가 굶지 않게 한다.
- 계산한 행도 기록된 이전 행(계산 시간 문단)도 없으면 그 option은 `CapacityPending`으로 계획에서 빠지고, 켜진 Workload에 κ가 있는 option이 하나도 없으면 판단이 `CapacityPending`이다.
- 행마다 `management` stream에 `capacity-row-built` 사건(행 전체, 계산 시각, 걸린 시간)을 남긴다. 이 사건은 기록이고 판단은 읽지 않는다.

**계산 시간과 이전 행.** 재생은 미완료 도착을 훑고 다음 도착은 정렬목록 선두, Definition.Identity는 도착 원형당1회, 배치는 정렬 분위수 색인·재사용 공간을 쓴다. E1 tight38행 단일코어1,386초; m4/r7 합성d114행은 GOMAXPROCS12에서132초/CPU804초였다. 최종 E5는 같은 dump로 source별 측정한다. worker는 동일키 마지막 capacity-row-built를 로컬 management→archive 최신순으로 찾아 previous로 쓰고 계산 후 교체하며 없으면 CapacityPending이다.

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
- 순서: 모든 w에서 두 충족을 만족하는 구성 중 에너지가 작은 것, keep, 논리 구성 key 사전 순. 그런 구성이 없으면 상태는 `Overload`이고, 모든 w에서 최종 충족(가중치의 κ 검사 포함)을 만족하는 구성이 그렇지 않은 구성보다 앞선다. 같은 쪽 안에서는 Workload마다 최종·전환·route 가중치 초과량 중 큰 값을 구하고 내림차순 정렬한 목록을 사전식 최소화한다(leximin: 최악 동률이면 두 번째 최악, 이어 다음). 모든 값이 같을 때만 에너지, keep, 논리 key 순이다. 용량 없음은 +∞ 그대로 비교하며 불가능 Workload를 따로 고정하거나 큰 유한 수로 치환하지 않는다. 기록의 `worstExceedance`는 목록 첫 값의 기존 요약이다. keep은 r = 0이다.
- 근거: 마지막 E1 snapshot에 포아송 40 req/s를 넣으면 에너지만으로는 rasp5 q1 + q1-lut(route 20.2 s)가 나오고 재생에서 전환 중 SLO에서 진다. 위 규칙으로 그 구성은 `B = 5.7 × 20.2 = 115`, `W = 115 / 69.2 = 1.67 s > 0.82 s`(L 1 s, m 177 ms)로 탈락한다. genesis3 q2 추가(route 10.7 s)는 `W = 61 / 144 = 0.42 s`로 충족이고, 재생 계획기와 같은 노드와 option이다.

## 4. 노드 계획과 탐색

**노드 계획.** 노드마다 keep, 그리고 배치 가능한 노드에서 측정 option 변형 하나로 만든 계획 중 그 모드에서 `ValidateNodeBudget`을 만족하는 것. 노드마다 서빙 인스턴스는 하나다. 같은 노드에 인스턴스 둘을 두면 κ가 더해지지 않으므로(m4 r8 D-R8-1: rasp5 q1과 q1-lut의 κ 합이 40 req/s를 덮는다고 판정했지만 둘이 35~38 req/s만 처리했고 SLO가 25.5%로 떨어졌다), 노드에서 더 큰 용량은 CPU 할당이 넓은 측정 option(q2, q4)이 맡는다. 다른 Workload의 인스턴스와 함께 둔 κ와 전력도 재지 않았으므로 이 규칙은 모든 Workload에 같다. 모드를 바꾸는 계획은 목표 모드에서 온라인인 CPU로 예산을 확인한다. 현재 모드는 노드 annotation의 CPU를, 다른 모드는 DeviceClass `powerModes[].onlineCPUs`를 쓰고, 그 필드가 없는 모드의 계획은 `PlacementInventoryMissing`으로 뺀다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이다. 계획마다 Workload별 `κ_new`와 `κ_ret`, 사슬, 전력을 둔다.

**인스턴스와 Workload.** 인스턴스는 Workload 하나에 속하고 κ는 그 Workload와 option의 행이다. 노드마다 서빙 인스턴스는 하나이므로 같은 모델의 Workload들도 노드를 따로 쓴다. 실제 상태에서 인스턴스 둘 이상이 있는 노드는 keep이 그대로 두고, 그 노드의 다른 계획은 인스턴스 하나를 남긴다. 계획기가 만드는 분배에서 인스턴스마다 Workload는 하나다. 서빙 source는 routing annotation이 Workload를 하나 이하로 가리키는 Pod이므로 실제 분배에서도 인스턴스마다 Workload는 하나 이하다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이므로 꺼진 Workload의 SLO는 계획 동작의 영향을 받지 않는다(workload-pools 4.4절의 노드 동작 규칙).

**사슬.** `schedulerPlan`의 순서다. 모드 변경은 power, 모든 기존 remove, prepare가 route 전이다. 같은 모드에서는 자원이 함께 들어가지 않을 때만 remove가 prepare 앞이고, 아니면 route 뒤다. 동작마다 측정 trial의 평균 wall J, 평균 시간, 최대 시간을 쓴다. 비용이 없으면 그 계획은 `MissingOperationCost`로 빠진다. 같은 인스턴스의 배치 상한이나 대기 시간을 바꾸는 계획은 그 인스턴스를 그대로 쓰고 route가 firstfloor policy를 바꾼다(사슬 없음). 서빙 Pod는 측정한 모든 배치를 시작할 때 초기화하고(`batchSizes`, m4 r2의 rasp5 q2 config는 1~16), firstfloor `Scheduler.Update`는 초기화되고 측정된 배치 안에서 policy의 `maxBatchSize`와 `maxBatchWaitMS`를 재시작 없이 바꾸며, route의 `applyPolicy`가 그 값의 native ACK를 기다린다.

target에 인스턴스가 없고 실제로도 빈 노드의 새 `Fixed` 절전은 §15의 최대 수요 창과 일반 교체의 동일 H·minimumGain·payback을 통과한 경우에만 다른 노드의 route와 병렬로 계속한다. 모든 후보에 미리 공통 절전을 넣어 keep 대비 비용을 소거하지 않는다.
target이 사용하는 노드의 `Fixed` 내림, 전력 올리기, remove와 prepare는 같은 노드의 순서를 유지한다.
route는 그 준비와 전력 사슬, 실제 Ready와 소유권, policy ACK와 기존 요청 인계를 유지한다.
공통 절전의 실제 소비와 시간은 전체 POOL 측정에 계속 포함한다.

**실행 중 중간 분배.** 최종 DAG·분석r은 전체 준비 사슬을 유지하되 매 Tick 검증된 current 경로와 독립 Node의 exact identity/native policy ACK된 Ready 목적지만 합친다. `SchedulerReadyExpansion`은 λ/q/slack/정확 row·목적지κ·양수integer weight를 검사하며 끝난 준비는 r0/Bq/W=B/κnew다.
같은 Node κ 중복·current 총κ 감소를 금지하고 불명/미달이면 current 유지, serving없는 bootstrap만 Ready부분집합 weight1000 재정규화한다. stop 가능 실물은 제외한다.
독립 prepare/power/drain·실재 non-Ready sibling은 장벽이 아니다. launch dependenciesReady는 선택 Ready부분집합, 최종route는 전체목표/원DAG를 검사한다. 새 탐색·bootstrap 용량보장은 없고 요청수명/admission·기존요청 보호는 유지한다.
실제 중간효과는 frozen target·before/actual routing으로 분석 예측과 구분한다([중간 배분](scheduler-implementation.md#중간-배분)).


**비교 기간 — SCH-58.** `H = settings.comparisonPeriodNanos` 하나를 keep과 모든 후보에 고정한다. 기존 이름이 이미 시간 단위·설정 API에 있으므로 `energyHorizonNanos` 별칭/중복 필드를 만들지 않는다. 아래 에너지 식의 T는 H다. route 전후 전체 사슬의 최대 시간이 H를 넘는 후보는 `TransitionExceedsHorizon`으로 제외하고 H를 늘리거나 음수 Serving 시간을 계산하지 않는다. 이는 후보별 기간이 아니라 공통 기간 내 전체 전환을 비교하는 계약이다. H를 바꾸면 새 설정 revision으로 다음 판단부터 적용하며 진행 작업의 frozen 설정은 유지한다. H는 SLO 시간·용량표 창·동작 timeout과 별개다. 제안값과 실측 근거는 15장이다.

**에너지.** 노드 계획 하나의 [0, T] 노드 에너지는 keep이면 `P_before × T`, 아니면 `J_pre + P_mid × (r − t_pre) + J_post + P_after × (T − r − t_post)`(t는 평균 시간)다. P는 `schedulerBaseline`의 적재 상태 전력이다. 요청 에너지는 route에서 나눈다: [0, r]은 지금 routing으로 남는 인스턴스가 κ_ret까지 처리하고(`Σ y⁰_i e_i r`), 넘친 `max(0, λ − κ_ret) × r`건과 [r, T]는 κ 비례(λ 무관)로 정한 최종 정수 weight의 `y_i = λ × weight_i / 1,000`으로 처리한다(`Σ y_i e_i (T − r)`와 넘친 건수 × y 가중 평균 e). 보호수요는 실제 λ로 비례 축소하고 backlog drain을 요청 J에 더하지 않는다. 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**탐색.** 계획 집합(노드마다 계획 하나)은 두 출발점에서 시작하는 이웃 탐색으로 고른다. 출발점은 keep과, 모든 노드를 비운 집합(인스턴스가 없는 계획, 그런 계획이 없는 노드는 keep)이다. 한 걸음은 지금 집합에서 노드 k개 이하의 계획을 바꾼 집합을 모두 평가하고, 순서(충족, 최종 충족, 최대 초과량, 에너지, keep, 계획 key)로 가장 좋은 집합으로 옮긴다. 출발점마다 k = 2에서 더 좋은 이웃이 없으면 그 자리에서 k = 3으로 이어 가고, k = 3에서도 없으면 멈춘다. 두 끝 중 순서가 앞선 집합이 해다. 비운 출발점은 keep에서 한 걸음마다 손해라 막히는, 노드 여럿을 한꺼번에 바꾸는 해에 반대쪽에서 닿는다. 평가는 3장 판정과 위 에너지 식을 정확한 κ 합으로 쓰고, route 시각은 그 집합에서 가장 긴 route 전 사슬이다. 후보에서 빠지는 집합은 셋이다: `maxReplicas`나 `maxInstances`를 넘는 집합(replicas가 꺼져 있으면 Workload별 인스턴스 수가 지금과 다른 집합), 새 인스턴스에 가중치 0을 주는 집합(14장), 관측 창이 덜 찬 동안 분배된 인스턴스를 빼는 집합. 걸음 없이 끝난 비운 출발점도 이 셋을 거친 뒤에만 해가 된다. 순서에서 keep이 앞서므로 완전 동률은 keep이다. 고른 해는 14장의 교체 회수와 최소 이득을 거친다. 한 걸음의 이웃은 `planSearchWorkers`개 goroutine이 첫 번째로 바꾸는 노드 계획을 차례로 나눠 평가한다. 순서가 전순서라 결과는 goroutine 실행 순서와 무관하다. 탐색은 판단 시작부터 `controlIntervalNanos`의 절반 안에 끝난다(k = 2를 두 출발점에서 먼저 마치고 k = 3으로 간다). 그 시각을 넘으면 그때까지 찾은 가장 좋은 집합을 쓰고 기록에 `budgetCut`을 남긴다. 두 출발점 어느 쪽에서도 닿지 않는 해는 놓칠 수 있다. 크기와 근거는 [용량표 근거 7장](capacity-table-evidence.md#7-계획-탐색)이다.

**router 가중치.** Routing 제어가 켜져 있으면 기존 weight를 보존하거나 e순으로 채우지 않고, λ·backlog와 무관한 minimax 정수 배분으로 `max(weight_i / κ_i)`를 최소화한다. `floor(1,000 × κ_i / Σκ)`에서 시작해 남은 단위를 `(weight_i + 1) / κ_i`가 가장 작은 인스턴스에 하나씩 준다(동률은 입력 index 순). κ가 0인 인스턴스는 받지 않고, Σκ가 0일 때만 같은 몫을 쓴다. λ가 0이거나 κ 합을 넘어도 배분은 같다. 최종 정수 weight로 모든 인스턴스에서 `λ_w × weight_i / 1,000 ≤ κ_i`를 한 번 검사하며, 넘치면 `RouteWeightExceedsCapacity`로 최종 미충족이다. λ에 따라 정수 단위를 옮기지 않으므로 안전한 정수 배분이 존재하면 이 배분도 안전하다. 요청 에너지의 y는 최종 정수 weight로 계산하며, keep의 가중치가 바뀌면 사슬 없이 route 동작 하나다. Routing 제어가 꺼져 있으면 `capacityFixedWeights`로 기존 weight를 유지한다.
근거: m4 r4에서 rasp5 q1을 κ 25.3까지 채웠을 때 1 s 이내 응답은 47%였다. full-E5-3m-12의 κ 10.191/26.498/15.976/36.859와 λ 73.8은 weight 114/296/178/412로 나눠 느린 장치만 κ까지 채우는 불균형을 없앤다.

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
공유 정리 결과는 [인스턴스 경계](../platform/node-agent.md#5-인스턴스-공유-경계)를 따른다.

## 7. 설정과 저장 계약

| 대상 | 계약 |
|---|---|
| 최소 수요 | `minimumDemand`는 Workload UID→유한한 비음수 req/s map이다. 빈 UID는 거절하고 미설정 UID는 0이다. `decision.settings`와 `decision.search.bounds`에 선택값을 동결하며 캠페인의 binding/settings 캡처와 summary에 연결한다([저장 경로](measurement-api.md#71-최소-수요와-재현-설정)) |
| 용량표 설정 | `capacityWindowNanos`>0, `capacityWindows`≥1, `capacityPassingWindows`는 1부터 전체 창 수까지, `capacityMinimumRequests`≥1이다 |
| 비교 기간 | `comparisonPeriodNanos`>0인 정수 ns; H의 유일한 필드다. 제안 600초. 후보 집합과 무관하며 실제 H를 `decision.capacity.periodNanos`에 남긴다 |
| 디스패치 점유 | snapshot의 `dispatchOccupancy`는 immutable 측정 record에서 도출한다. 행 입력에 Node UID, mode, runtime/model, 값과 source를 포함한다 |
| 탐색 병렬 | `planSearchWorkers`≥1이다. sample은 3이고, 실제 CPU 한도와 gateway/Tick 관측으로 평가한다 |
| 판단 기록 | `decision.capacity`는 구성의 판정, 에너지, 사슬과 `search`를 기록한다. `management`의 `capacity-row-built`는 행과 계산시간을 기록한다 |
| 인스턴스 시간 | `unroutedSince`와 `heldUntil`은 controller의 메모리 상태이며 유휴 제거와 Overload 준비 유지에 사용한다 |
| 최소 이득 | `OperationTrial.energyUncertaintyJoules`와 실제 선택한 `NodeBaseline.wattsUncertainty`를 signed 노출량으로 전파한다. `gainUncertaintyJoules`는 B-only 문턱이다 |
| 두 박자 | `scaleDownStabilizationNanos`≥0인 정수 ns(기본 300초, 시연 120초, 0이면 지속 대기 없음). `transitionReserveFraction`은 유한한 `[0,1]`(기본 0)이며 NaN/±Inf/범위 밖은 거절한다. 값은 코드의 정책 상수가 아니라 scheduler 설정 기본값·sample/binding에서 제공한다 |

설정 decode는 strict하다. API, ConfigMap과 Dashboard는 같은 설정 계약을 사용한다.
저장 형태는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 single-version reader를 따른다.
Timing이 없는 비용의 미기록 B를 관측한 0으로 취급하지 않는다.
저장 reader는 단일 버전이다. G2 인벤토리(`ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json`)의 catalog legacy49/랭킹9와 동결 E5 입력 legacy0은 서로 다른 범위다. 승인 재측정2 roots/18 steps(직접 전환 포함 상한4/22), raw80키·terminal OCM6 정리는 [측정 API](measurement-api.md)의 실제 소비·반환·백업·입력 동결 계약을 따른다. 원 raw/status·과학 증거는 보존하며 실행 완료를 추정하지 않는다.

## 8. 측정·검사와 실행 경계

- profile/runtime/측정 계약 변경은 해당 NodeUID/mode/model의 d, 정확 profile prepare/remove, loaded/unloaded baseline·전력 전환 비용을 열거하고 대표1건의 생성→raw→projection→계획 소비→실물 반환 뒤 대량 수집한다.
- mobilenet CPU8/GPU2의 legacy는 Accepted 대체 전까지 보존·소비한다. D2 뒤 CPU8/GPU5/PLC12 재측정, Smol D04/D09 새 source 대체 뒤에도 옛CPU2 raw/status 보존. d 없으면 MissingDispatchOccupancy, 정확 비용 없으면 MissingOperationCost다.
- signed d와 max0 정책은 구분한다. Smol D04/D09/D14/D23/D33의 profile 포함95% 근사구간<0은 systematic profile-versus-serving bias이며 인과 기여 미확정; identity·harness/warmup/timing/시점 차이는 남기고 보정·추정정책은 M5 후보다. 근거: `ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.{json,txt}`.
- `E±B`, `B=S+G`는 [측정 API](measurement-api.md)를 따른다. minimumGain은 선택 evidence B만 전파하며 profile 통계·BEO fallback CI·시점/센서/모델 오차를 포괄하는 global95% 보장이 아니다.
- 검사 계약: 재생 결정성/NonMonotone/창·m·SLO 경계, 입력별 재계산/previous/pending, κ_new/κ_ret/B/W·make-before-break, 노드단일소유·모드 CPU, brute force/k3/빈 출발/replica/예산·동률, 동일H 에너지·signed 상쇄·문턱 equality/keep infeasible, weight1000·ACK·budgetCut을 확인한다. 이 목록은 새 source의 통과 기록이 아니다.
- 최종 dump의 source별 `hack/kappa`는 provenance·κ/status·시간·에너지 차를 남긴다. 승인 rev72 BEFORE/D2 AFTER는 자율MNV/native8 paired의 Tick/CPU/GC/mesh/요청/physical G를 비교한다. E1 무부하→유부하, E4 이동, E5-0~6/E5-S는 두 관측률·λ·보호κ·route시각·사슬·유효 offered 전체 결과·전환 포함 전체 POOL J를 연결한다.
- 최종 d/G/m에서 SLO/κ/R·threshold를 정한다. 과거 E1/m4와 새 E5는 Run ID/원시로 구분한다. 요청 에너지는 κ 비례(λ 무관)로 정한 최종 정수 weight와 일치하며, 모델 κ와 실제 장치 처리율·배치상태의 차이는 추정 한계로 유지한다.
- BEFORE·유한 진단 최대1회·최종D2 최대1회 순서는 [POOL 검증](workload-pools.md)을 따른다. runtime7 digest·기존 자원·evidence store를 유지하며 paired 물리 보존 실패는 unknown/mismatch로 보고하고 paired 주장을 중단한다. 조건·판정·범위 변경은 승인 대상이다.
- 승인/manifest/NodeUID/profile/runtime/source SHA·원시만 선별 보존하고 `.token`/Secret/전체 캐시 백업은 제외한다. 로컬 결정 원문은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/{d-measurement-decision.md,deployment2-e5-proposal-20261024.md}`다.

## 14. 구현 규칙

4장의 계획기를 `internal/serving/scheduler_capacity.go`가 다음 규칙으로 구현한다.

- **설정 gate.** placement, resources, batching, membership이 꺼져 있으면 option 변형은 그 Workload의 지금 인스턴스 하나와 노드, CPU와 GPU 선택, 배치 상한과 대기 시간이 같아야 한다(membership은 그 Workload의 인스턴스가 있는 노드만). `maxReplicas`와 `maxInstances`는 POOL 전체의 Workload별 인스턴스 수와 전체 인스턴스 수 상한이고, replicas가 꺼져 있으면 Workload별 인스턴스 수가 지금 수와 같다. 탐색은 이 수를 집합마다 세어 상한을 정확히 지킨다. `maxCPUs`는 인스턴스마다의 CPU 수 상한이다. routing이 꺼져 있으면 남는 인스턴스는 지금 가중치를, 새 인스턴스는 option의 가중치를 쓰고 같은 κ 검사를 받는다.
- **batching OFF의 고정 정책.** ON/OFF 모두 `CapacityVariants`로 측정된 cap 변형을 생성한 뒤 `controlAllows`가 Workload의 현재 `(MaxBatch, MaxWait)` 정책 집합에 속하는 변형만 허가한다. 여러 현재 정책을 임의의 최소/최대 하나로 축약하지 않는다. cap1도 측정·초기화된 변형일 때만 사용하며, 다른 제어가 켜져 있으면 고정 cap 안에서 placement/resources/replicas 후보 평가는 계속한다.
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
- **준비한 인스턴스의 분배.** 해가 준비하는 새 인스턴스는 모두 그 해의 가중치를 받는다. 새 인스턴스 하나라도 가중치가 0인 해는 후보에서 빠지고 교체 회수의 되돌린 해도 같다. 근거: m4 r7에서 rasp5 q1-lut와 q2(κ 25.69 + 29.30)가 지금 가중치 467/533으로 λ 40.35를 받았고, 적재한 genesis3 q2의 노드 전력(49.80 W)이 빈 노드(49.97 W)보다 작아 그 준비가 T 동안 1.5 J 싸게 나왔다. 그 인스턴스는 가중치 0으로 준비되고 약 80 s 뒤 유휴 제거로 지워졌고, 다음 판단이 다시 준비해 2 req/s와 40 req/s에서 약 100 s 주기로 되풀이했다.
- **만든 뒤 끊기.** 모드를 바꾸지 않는 노드 계획에서 분배된 인스턴스(어느 Workload에서든 가중치가 0보다 큰 인스턴스)의 stop, drain, remove는 그 노드 계획의 route 뒤에만 온다. 자원이 겹쳐 prepare 전에 지워야 하는 분배된 인스턴스가 있는 같은 모드의 교체는 `RoutedInstanceStopsBeforeRoute`로 빠진다. 그래서 그 교체 동안 분배된 κ는 지금 값에서 줄지 않고, route 뒤에는 최종 판정을 지나는 `κ_new`가 된다. 그 노드의 교체는 다른 해가 부하를 옮겨 그 인스턴스가 분배에서 빠진 뒤(유휴 제거) 열린다. 모드 변경은 power 동작 전에 노드의 인스턴스를 모두 멈추고 다른 길이 없으므로(노드 하나가 서빙하는 Workload의 모드 올리기) 후보로 남고, 멈춘 인스턴스는 `κ_ret`에서 빠지며 사슬 시간은 r에 들어가 3장 대기 판정을 받는다. 근거: m4 r7 07:08:31에서 genesis3 q2(κ 70.94, 가중치 532)를 같은 CPU의 q1으로 바꾸는 계획이 q2 stop을 첫 동작으로 두었고, 전환 판정은 rasp5 q1 하나(κ_ret 25.69)로 λ 38.6을 받았다. rasp5 prepare가 68 s 걸리는 동안 SLO는 0%로 떨어졌다. 07:00:38의 rasp5 q1 교체와 06:51:28의 genesis2 교체도 같은 순서였다.
- **교체 회수.** 지금 인스턴스를 지우고 다른 인스턴스를 들이거나 배치 policy를 바꾸는 노드 계획(교체)은, 그 노드만 keep으로 되돌린 해도 충족하면 관측 창의 최소 부하에서 T 동안의 에너지로 비교한다. 최소 부하는 관측 창을 제어 주기로 나눈 구간마다의 도착률 중 가장 작은 값이다. 교체가 그 부하에서 keep보다 에너지가 크거나 같으면 keep으로 되돌리고 `ReplacementUnpaid:<노드>`를 남긴다.
- **교체 후 최소 체류 — SCH-16/18.** `replacementDwellNanos`는 0 이상 정수 ns(int64 범위)이며 생략/0은 기존 `comparisonPeriodNanos`를 재사용한다. controller가 같은 Node UID·Workload UID의 물리 profile 교체를 전체 prepare→route→stop/drain/remove 완료 시각에 기록하고 `snapshot.replacements`로 전달한다. 그 현재 profile을 다시 교체하려는 해에서 해당 Node만 keep으로 되돌려도 κ/SLO가 충족되면, 완료 뒤 이 기간 미만인 optional energy-only 교체를 `ReplacementDwell:<노드>`로 보류한다. 경계 시각부터 허용하며 기존 최소부하 회수·최소이득 검사도 유지한다. 현재 κ 부족(keep infeasible)이나 실제 고장 복구, route-only 조정·순증설은 막지 않는다. 재시작은 이력을 잃고, UID/profile 변화·미관측/비정상 executor·기존 gap 기준(`MaxObservationAge+ControlInterval`) 초과는 이력을 버려 이력 없으면 허용한다. 이 소규모 controller 이력은 수요 창과 독립하며 교체 선택·완료가 demand history를 reset하지 않는다. 기본 기간은 정책 안정화일 뿐 실측 에너지 본전 회수의 증명이 아니다. 회귀 근거는 `replacement-01`/`full-E5-1-02`의 완료 15–17초 뒤 역방향 선택이며, 실제 손해 J나 요청 오류 감소를 주장하지 않는다.
- **최소 이득.** 고른 해가 충족하고 노드 하나라도 바꾸며 keep도 충족하면, T 동안 nominal 이득(keep−해)이 선택된 operation과 baseline의 조건부 B 민감도 문턱보다 클 때 해를 쓴다. 같은 immutable reference/trial의 signed 노출량을 먼저 합쳐 공유 근거를 상쇄하고 남은 절댓값을 합산한다. trial 평균은 B/N을 선형 합산하며 baseline watts 민감도는 실제 초를 곱한다. 이득이 문턱과 같거나 작으면 keep과 `TransitionGainWithinUncertainty`를 기록한다. keep infeasible이면 용량/SLO 회복을 유지한다. 문턱은 profile 통계오차와 BEO idle fallback CI 및 시점/센서/모델 오차를 포괄하지 않으며 global95% gain 구간을 뜻하지 않는다. 원시 profile 분산/CI, d의 profile 포함95% 분석과 κ/SLO 계산은 각 추정 계약을 유지한다.
- **부분 관측 창.** ChillServer가 시작한 뒤 관측 창 하나를 다 관측할 때까지 계획은 인스턴스를 더하기만 한다. 지금 인스턴스를 지우거나(그 노드 계획에 남지 않는 인스턴스) 배치 policy를 바꾸는 노드 계획은 `PartialObservationWindow`로 빠진다. 계획은 지금 인스턴스를 노드의 지금 모드에서만 다시 쓰므로, 인스턴스가 있는 노드의 모드 변경은 그 인스턴스를 지우는 계획으로 같이 빠지고 빈 노드의 모드 변경은 확장으로 남는다. 분배된 인스턴스의 가중치를 0으로 만드는 해는 후보에서 빠지며, keep의 가중치가 분배된 인스턴스를 빼면 지금 가중치를 그대로 둔다. 유휴 제거도 쉰다. 근거: m4 r7 07:00:38에서 재시작 19 s 뒤의 관측한 창은 λ 20.6이었다. 부하는 40 req/s였지만 클라이언트가 중단 동안 물러나 있어 ingress가 9.0 req/s(07:00:00~20)와 23.4 req/s(07:00:20~40)만 받았다. 계획은 그 λ로 분배된 rasp5 q1 하나를 지웠다.
- **Overload 준비 유지.** keep이 `Overload`인 판단이 준비한 인스턴스는 Ready가 된 때부터 관측 창 하나 동안(`heldUntil`) 지우거나 교체하는 노드 계획에서 빠지고(`HeldAfterOverload`), 유휴 제거도 받지 않는다. 근거: m4 r5의 40 req/s에서 genesis2를 keep `Overload` 판단(04:28:48)이 준비하고 50 s 뒤 지웠다.
- **계획에서 뺀 노드.** `settings.excludedNodes`(Node 이름 목록, sample은 빈 목록)의 노드는 측정 POOL 노드와 같은 고정 경로를 탄다. capture가 노드 사유 `ExcludedNode`를 붙이고, 그 노드는 keep뿐이며(배치, 교체, 지우기, 전력 변경 없음) 가용 노드 최저 모드 낮춤과 유휴 제거도 받지 않는다. 판단마다 `decision.capacity.plans`에 그 노드의 `ExcludedNode` 계획이 남고 snapshot 노드 `reasons`에도 남는다. 측정 POOL 노드의 인스턴스도 유휴 제거를 받지 않는다.

## 15. 두 박자 후보 허가와 고정 H — SCH-55~58

2026-10-26 사용자 결정 ④ 1-A/2-A/3-A/4-B의 구현 계약이다.
controller 상태와 기존 단일 계획기에 연결하며 실제 클러스터 시연은 별도 승인 범위다.
근거는 로컬 `intent/two-tempo-questions-20261026.md`, `intent/checkpoints.md` 1949행,
`intent/structure-redesign-20261026.md` §5와 `audit-20261024/structure-20261026/Research-K4.md`다
(모두 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/` 아래).

### 후보 허가와 지속 하강

- 빠른 박자는 별도 제어 루프가 아니라 매 Tick의 후보 제한이다. fast는 keep、분배 변경、안전한 순증설、물리 제거·정지 없이 κ가 증가하는 정책 변경(용량 회복)이다. 현재 Ready 집합의 중간 분배도 매 주기 계속한다. 수용 한계에서는 허용된 후보 사이에서도 기존 전체 충족→최종 충족→leximin→에너지 순서를 그대로 적용한다.
- slow는 실물 제거·정지、에너지 절감만을 위한 교체(κ 비증가·joules 감소)、추가+제거 혼합 후보이며 하강 안정화가 필요하다. 분류가 겹치면 slow를 우선하여 증설에 제거를 섞어 우회하지 않는다. 느린 박자는 같은 Tick에서 하강 안정화 조건이 열린 범위의 전체 후보를 추가하는 것이다. 후보 허가 뒤 κ/대기/가중치/SLO·leximin·minimumGain·교체 회수 순서는 바꾸지 않는다. 축소/교체에 포함된 모든 Workload가 조건을 만족해야 하며 다른 Workload의 준비 완료나 하강을 기다리는 전역 장벽은 없다.
- `W = settings.scaleDownStabilizationNanos`, `d_w(t)`는 기존 계획기의 유효 수요(queued·minimum demand 포함)다. 하강 판정 수요는 `D_w(t) = max { d_w(s) | t−W < s ≤ t }`다. 왼쪽 경계 관측은 W가 지난 순간 만료한다. 정상 관측이 W 전체를 덮은 경우에만, 현재 실제 Ready/ACK 가용 공급 `C_w(t)`에 대해 `D_w(t) < C_w(t)`이면 slow 후보에 적격이다. 후보의 전환·최종 κ/SLO도 D를 감당해야 하며, 에너지 비교의 실제 부하 가정과 하강 보호용 D를 기록에서 구분한다. 이는 과거 기준률 아래에 머문 시간을 재는 방식이 아니다. 국소 고점, 선택·확장·축소가 창을 재기준하거나 이력을 지우지 않는다.
- **Cycle 5 / SCH-55·56:** 최종 에너지 하강 route와 idle 후처리는 `h=scaleDownHeadroomFraction`(유한 `[0,1]`, 기본0)의 `max(현재 유효수요,D)×(1+h)`를 실제 양수 정수 weight/각 κ로 검사한다. weight0은 공급이 아니다. 공유 `reductionDemand`·bridge·reserve·실제 요청 J는 할증하지 않는다. `.25`는 offline 대조값이지 배포 권고가 아니다.
- **조건부 부족 회복:** Node-local slow 후보를 `requiresRecovery` UID와 함께 전역 조합까지 남긴다. 동일 관측 keep의 해당 UID κ/대기 SLO 미충족과 목표 완전 충족·backlog 보호 때만 해당 UID growing/Allowed timing을 면제한다. 다른 UID 에너지 하강은 독립 판정한다. partial/held·unknown·예약·소유권·Ready/native ACK·old+new peak·bridge·reserve·H는 면제하지 않는다. 중단형 power 변경은 handoff가 아니다. 증명 전 진단 후보는 공개 `eligible=false`이며 target이 아니다. `Observe.Allowed`와 에너지/leximin/payback/dwell은 유지한다.
- **Cycle 6 / SCH-26·50:** growing의 old 양수 ID 보존은 exact physical ID의 `Terminated` 증거에만 면제한다. NotReady·논리 Stopped·관측 소실·TargetID alias는 면제하지 않으며 dead+live 혼합의 live 공급은 보존한다. slow의 완전충족 recovery timing 예외는 그대로다. Terminated는 κ=0이지 allocation 반환이 아니므로 same-Node stop/drain/remove 의존성을 유지한다. 공급0 우선 후보 탐색·목적함수 변경·standby·주기 튜닝은 추가하지 않는다.
- `scheduler_tempo.go`의 `SchedulerTempo.Observe`/`Selected`와 `SchedulerDownState`를 이 계약으로 교체한다. `baselineRate`, `previousRate`, `downSince`, `elapsedNanos`, `globalSince` 및 선택 후 재기준을 삭제하고 UID별 시각·유효 수요 관측 이력과 연속 유효 관측 시작만 유지한다. W 밖 표본은 만료한다. 공백·시각 역행·설정 변경·UID 교체·controller 재시작은 영향 범위 이력을 비우며 새 W를 채우기 전에는 보류한다. W=0이면 현재 유효 수요만 사용하고 기간 대기는 없지만 수요<공급 판정은 남긴다. 불완전 관측 창·`DemandIncreasing`·최소 인스턴스 하나 등 기존 보호도 유지한다. 유휴 제거·배치/자원 축소·교체 회수 후처리도 같은 판정으로 탐색 뒤 우회하지 않는다.
- 표본 시각은 기존 `SchedulerTempo.Observe`의 `snapshot.At`를 재사용하고 정상 Tick마다 기존 `w.rate` 하나를 저장한다. 유효 수요는 기존 계획기의 `max(장기 관측률, 최근 관측률) + outstanding/실제 관측 길이`에 `minimumDemand` 하한을 적용한 값이며 payback용 최소 구간률과 다르다. 기존 gap 판정(시각 역행 또는 Tick 간격>`MaxObservationAge+ControlInterval`)과 정상 관측 판정(snapshot 사유 없음 및 기존 최소 관측 길이 충족)을 재사용한다. 잘못된 관측을 0 수요 표본으로 넣거나 실패 기간을 연속 유효 시간에 포함하지 않는다.
- 실행 중 owner 예약과 frozen target은 박자와 무관하게 보존한다. 이미 허가된 DAG의 후속 drain/remove는 그 작업의 의무이며 매 Tick 새 축소로 취급하지 않는다. 중간 배분은 느린 판단을 기다리지 않고 기존 실제 Ready/ACK·stop 제외·원자적 UID별 route 계약으로 진행한다.
- Workload UID가 없는 빈 Node의 새 Fixed 절전도 동일 D 벡터를 사용한다. 모든 자동제어 Workload에 완전한 유효 창과 `D_w≤C_w`가 있고 하나 이상에 `D_w<C_w`가 있어야 한다. 합계 상쇄·직전 tick 증가 검사·별도 `globalSince` timer는 없다. UID 집합 변경 시 빈 Node 판정의 창 완전성도 다시 확보한다. 제어 Workload가 없으면 하강 근거가 없어 새 절전은 보류한다. 이미 실행 중인 Fixed 작업은 계속한다. `EvaluateScheduler → schedulerCommonNodes → changeNodes`와 target 없는 `Keep.Actions`에도 동일 판정을 적용한다.

고정 H의 구현 연결점은 `scheduler_capacity.go:524-543`의 `p.period` 확장 규칙이다.
이를 폐기하고 `p.period = settings.comparisonPeriodNanos`로 고정한다.
후보 조합별로 `공통 route 시간 + 선택된 노드의 최장 post 사슬 시간 ≤ H`를 확인한다.
개별 노드 사슬이 H 이하여도 조합에서 넘으면 `TransitionExceedsHorizon`으로 제외하며,
정확히 H인 경계는 허용한다. keep 비용·교체 payback·minimumGain과
`decision.capacity.periodNanos` 모두 같은 H를 사용한다. 코드 위치는 변경 전 탐색 앵커다.

**빈 Node 본전 — SCH-59.** `scheduler_plan.go:schedulerCommonNodes/changeNodes`가 새 Fixed 절전을 모든 후보와 keep에 선반영하는 예외를 제거하고, 일반 변경 후보의 기존 `capacityPlanner.payback`/`minimumGain`(`scheduler_capacity.go`)으로 비교한다. Fixed를 이유로 에너지에서 제외하는 경로도 제거하여 같은 H의 keep 원상태와 비교하고 전환 wall joules·전후 baseline을 한 번 포함한다. 별도 절전 계산기나 설정은 없다. H 안 nominal 이득>0, 기존 minimumGain 문턱 및 payback 모두를 통과해야 실행한다. `kube/scheduler.go`의 target 없는 `Keep.Actions`도 이 허가 없이 새 절전을 실행하지 않는다. jetsono MAXN→15W는 H=600초에서 −699J, 손익분기 약 52분이므로 보류하며 이를 위해 H를 늘리지 않는다. 이미 허가·실행 중인 Fixed의 완료 사슬은 보존한다.
구체적으로 `scheduler_capacity.go:687-689`의 빈 Node 후보 누락 경로를 이행하여 절전을 비교 후보로 생성하고 비교 집합에 포함한다. `:1489-1492`의 `plan.replaces` 한정 payback 대상에도 빈 Node 새 절전을 포함한다(위치는 변경 전 앵커). 공통 절전 선반영만 삭제하고 실제 후보 생성·비교·payback을 누락하는 구현은 미완료다. 다른 Node의 큰 이득이 손해 Fixed 절전을 덮는 조합에서도 해당 절전은 기존 개별 교체 payback 경로로 거절해야 한다.

### 전환 예비

`f = transitionReserveFraction`이고 영향받는 Workload마다 전환의 각 용량 변경 구간에서
실제 사용 가능한 Ready/ACK κ 합이 `λ_w × (1+f)` 이상이어야 한다(f>0).
아직 준비 중인 target, stopped/draining 또는 전력 변경 중인 공급은 세지 않는다.
이는 요청 수요를 부풀리는 설정이 아니라 전환 허가 제약이며 λ·요청 에너지의 분모를
바꾸지 않는다. 예비를 예약한 두 작업이 같은 용량을 중복 담보로 쓰지 않도록 공통 범위
예약에서 검사한다. 확보 못하면 `TransitionReserveInsufficient`로 해당 후보만 보류하고
유지·안전한 증설은 계속한다. f=0은 추가 예비 검사만 끄고 기존 B/W·SLO 검사를 유지한다.
예비의 실제 적재 baseline, prepare/remove와 전력 사슬을 동일 H 에너지에 한 번 포함하고
E5 실측은 실제 전환 전체와 전체 POOL(예비 노드 포함)을 집계한다. 예비 비율만으로
실제 SLO를 보장했다고 주장하지 않고 AC-43에서 요청 결과로 확인한다.

### 기록과 설정 고정

strict scheduler 설정 decode/검증·설정 API·ConfigMap·Dashboard·sample·Run 캡처는
7장의 같은 계약으로 함께 이행한다. `comparisonPeriodNanos`의 기존 단일 필드를
재사용하고 단일 허용값 enum/하드코딩된 선택지로 제한하지 않는다.
`decision.settings`와 `decision.search.bounds`에 세 설정값을 고정하고,
`decision.capacity.periodNanos`는 사용한 H와 일치시킨다.
`decision.capacity.workloads[].scaleDown`은 구 timer 형식을 대체하여
`rate`(현재 유효 수요), `windowMax`(D), `windowNanos`(설정 W), `supply`(현재 실제 Ready/ACK 공급 C), `allowed`, `reason`, 선택적 `resetReason`을 기록한다. `baselineRate`/`previousRate`/`downSince`/`elapsedNanos`는 삭제하며 병행 writer/reader나 alias를 두지 않는다. 기존 저장 기록은 당시 schema의 역사 자료로 보존하고 새 창 판정으로 소급 해석하지 않는다. 탐색 전 반환에도 창 불완전/공백 근거를 남긴다. 빈 Node 판정은 같은 UID별 기록의 결합이며 후보에는 `slow` 분류와 창/공급 또는 본전 보류 이유, H·keep/후보 에너지·gain·minimumGain/payback 판정을 연결한다.
원본 raw archive 보존과 현재 API의 typed decode(`observation_log_scheduler.go:DecodeSchedulerLine`)는 별도 경계다. 현재 `scaleDown`은 `windowMax`·`windowNanos`·`supply`의 존재를 검증하며 누락은 조회 오류다. 구 timer 기록을 생략하는 호환 decoder·alias·병행 reader는 두지 않고, 원본 archive와 게시 증거는 그대로 남긴다. 배치3 선행 확인: 최대 수요 창 도입은 `f44f6e36`(2026-10-26T19:15:23+09:00)이며 게시4건의 `raw-source-index.json` 전체 requested/supporting/source hour 최솟값은 #213=`20261028T00`, #216=`20261028T01`, #222-223=`20261029T05`, #222-r3=`20261029T19`로 도입 이전 archive를 읽지 않는다. 확인 정본은 `O/campaign/v2/dashboard-acceptance-20261030/approved-publication-inputs.json`과 그 단계별 index(`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`)다. E5 집계의 기존 search/에너지 계약은 유지하되 새 창 증거와 구 timer 증거를 혼합하지 않는다. Dashboard 설정 도움말의 “하강이 이 기간 유지”도 “지난 기간의 최대 유효 수요로 축소를 판정”으로 바꾸고, 새 창 구조화 표시는 타입과 표시를 함께 이행한다.
예비 평가는 Workload별 수요·필요 κ·구간별 가용 κ·부족 이유를 같은 결정에 연결한다.
Run 시작의 `binding.schedulerSettings`, 설정 캡처와 summary는 같은 값을 보존하며
시연 120초와 운영 기본 300초 결과를 같은 설정의 실험으로 합치지 않는다.

### H 제안과 실제 전환 근거

제안 H=600초(`comparisonPeriodNanos=600000000000`). `ops/pools-deploy/campaign/v2/demo-*/summary.json`의 complete prepare/power `completedAt−startedAt` 근거이며 실패/미완료·Ready/전체 route 지연과 섞지 않는다.
| Run/단계 | 완료 동작 초 |
|---|---|
| demo-20261025-2/B; demo-20261026-2/B | jetsono MAXN150.939/148.854, 후속 MNV prepare12.011/13.693 |
| demo-20261025-2/C; E0 | lattepanda Smol268.867, jetson-nano223.339; genesis3 Smol292.163(최장,285로 대체하지 않음) |
| demo-20261026-2/B; C | genesis3/lattepanda/jetson-nano MNV9.873/12.717/26.745; rasp5 q1-lut/q1 17.796/16.518 |
| demo-20261025-2/C; demo-20261026-2/C | jetsono 하강196.180/168.016 |
292+196≈488초 직렬과 Serving 여유·기존10분 sample이 제안 근거이며 상한 보장/통계적 최적 H는 아니다. 3회차 G/L Ready15~18초/N32초 중간route는 반응성 근거이지 H 표본이 아니다. 미래 H초과는 제외하고 승인 설정으로만 변경한다.

회귀 수용은 AC-42~44를 따른다: strict ns/비율/overflow·설정동결; 일정100+잡음에서 축소없음과 높은 표본의 W경계 만료 뒤 허용; 공백/restart/UID/설정/W0/벡터상쇄·모든 후처리 gate; 창최대110/현재20/공급160에서 후보60 거절·120 허용; f0/SLO 보존·예비 실제구간·중복 담보; 개별≤H/조합>H 거절·정확H 허용; Fixed−699J를 다른 노드 이득으로 덮지 않기; 구timer의 새창 오해석 금지;15/18/32초 Ready 중간route·예약밖확장·빈출발/증설 불변. 운영300초/시연120초는 별도 binding/H로 보고하며 문서가 회귀/live 통과를 뜻하지 않는다.


## 16. 같은 기종 profile 재사용 — PR-01..04

Catalog가 [측정 API §11](measurement-api.md#11-같은-기종-node-프로파일-재사용--pr-0104)의
확인 판정 뒤 대상 Node UID의 행을 만든다. 계획기의 물리 UID guard를 느슨하게 하거나
DeviceClass만 맞는 원본을 후보에 직접 넣지 않는다. 재사용 profile ID는 확인 측정 ID의
`/reuse-<source·허용값의 SHA256>` 파생 ID이며 행 key·불변 startup bundle·정확 비용 참조에도 이 ID를 쓴다.
서비스 분포·증분 에너지는 원본, idle/meter·runtime/GPU UUID는 대상 확인 근거다.
`profile.reuse`에 source/confirmation/result와 실제 차이를 기록한다.
대상 d와 정확 비용이 없으면 기존 MissingDispatchOccupancy/MissingEvidence를 유지한다.
프로파일 공유가 d·prepare/remove·mode cost의 암묵적 공유나 κ 적격을 뜻하지 않는다.

