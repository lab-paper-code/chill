# 피드백 기반 계획기

상태: [승인 M0](path-aware-implementation-plan.md#3-수치시간-계약--m0)를 반영한 구현 계약이다. 실배포·실측은 별도 검증한다.

option의 raw profile로 routing을 먼저 구성하고, 저비용 heuristic으로 탐색한 뒤 keep와 최종 후보를 직접 replay해 비교한다.
현재 입력에서 검사한 rate의 최대 통과값 κ̂는 검사 근거와 함께 기록하며 완성된 용량으로 부르지 않는다.
전체 격자 κ는 `hack/kappa` 등 명시적 offline 계산에만 남긴다.
[용량표 근거](capacity-table-evidence.md)의 completed 결과는 당시 입력·알고리즘의 역사적 증거다.
[기록 M0](decision-record-diet.md)와 [관측](observability-split.md)의 저장 경계는 유지한다.

**패키지 경계.** 공통 CHILL 입출력과 수치 adapter는 `internal/serving`에 둔다.
snapshot·profile routing·노드 계획 생성, 저비용 탐색 항과 직접 후보 평가, 실물 admission·후처리,
선택 결과의 decision·action DAG 변환은 같은 adapter를 재사용한다.

| 패키지 | 소유 책임 |
|---|---|
| `internal/scheduler/protocol` | 중립 scalar `Problem`·`Plan`·`Workload`, 후보·순위·평가와 상태/기록 port |
| `internal/scheduler/v1` | keep/empty 출발점, K2/K3 이웃 순회와 sweep |
| `internal/scheduler/v2` | cover/polish/combine/priced 순회, 평가 예산·finalists·정확 깔때기 |
| `internal/scheduler` | `planSearchStrategy` 전역 선택과 v1 fallback 연결 |

의존성은 `serving → scheduler → {v1,v2} → protocol`이며 serving과 selector도 protocol을 사용한다.
v1/v2는 protocol만 참조하고 서로나 serving을 import하지 않는다.
정확 깔때기는 adapter의 기존 수치·실물 admission port를 호출한다.
`protocol`은 runtime IPC 프로토콜이 아니며 CHILL package를 참조하지 않는다.
기존 snapshot capture·요청 수명·물리 lifecycle 정책과 잠금·예약 권한은 바꾸지 않는다.

`settings.planSearchStrategy`는 모든 자동제어 Workload에 적용하는 공식 선택 하나다.
허용값은 `v1`과 `v2`(기본)이며 기존 설정 수락 경계와 판단의 settings 기록을 사용한다.
선택하지 않은 구현을 비교 목적으로 함께 실행하거나 별도 비교 진단을 남기지 않는다.

## 1. 측정한 문제

- 재생 Tick은 `replays=10/maxCandidates=32`에서 0.633/2/5 req/s의 p50 913/3,475/7,495 ms, 최대22,575 ms·CPU499m(D-16); 기본100/128에서는133회30초 deadline 실패(D-1).
- 깊이 우선 후보127/128건이32에서 잘리고 재진입은37~48번째(D-22). 빈 노드 전환63.5/134.5/197.1초(D-20/21)가 당시60초 horizon을 넘었다.
- 포아송 재생 p95는 지연 공식 상한(여유율.8)의1.8~3.6배여서 동일 재생의 κ를 채택했다. 에너지 E1 예측499,333 J/실측495,741 J(+.7%)는 해당 입력의 근거이며 새 모델의 보장이 아니다.
- 정렬·고정 seed·완전 동률 keep을 유지한다. 탐색 선택 근거와 측정 표는 [용량표 근거](capacity-table-evidence.md)에 둔다.

## 2. RoutingWeight와 현재 tested-rate κ̂

**온라인 입력.** raw conditional service, profile·배치 정책·할당, 선택한 디스패치 점유 d, SLO, 현재 Workload별 TOTAL·POST를 함께 사용한다. routing은 profile의 fastest observed support에서 얻은 양의 상대 `RoutingWeight`로 구성한다. 이 값은 SLO 충족 처리율이 아니며 κ̂와 독립이다. 수식과 입력 의미는 [M0 §3.1~3.3](path-aware-implementation-plan.md#3-수치시간-계약--m0)을 따른다.

**현재 검사 근거.** keep와 최종 선택의 positive nominal per-instance rate를 중복 제거해 기존 Poisson·window·percentile 판정으로 검사한다. 같은 option·현재 timing·rate의 완료된 결과만 재사용한다. κ̂는 그 검사에서 통과한 최대 rate이며, 0은 lower witness가 없다는 뜻이다. 검사하지 않은 option은 0과 빈 tests를 유지하며 후보에서 제거하지 않는다. rate의 실패는 선택 후보를 다시 거절하는 gate가 아니다.

**계산 경계.** Tick은 전체 grid를 기다리지 않는다. online `CapacityTable` 준비 여부·이전 행·`CapacityPending`으로 후보를 고정하거나 제거하지 않는다. TOTAL·POST가 달라지면 이전 pass/CDF/κ̂를 현재값으로 내보내지 않는다. raw service와 결정적 random tape는 재사용할 수 있다. 취소는 기존 Tick 취소로 처리하며 실패 검사나 이전 Tick 값으로 대체하지 않는다.

**명시적 offline 계산.** `BuildCapacityRow`의 전체 grid·이분 탐색·`NonMonotone`과 `hack/kappa` 결과는 별도 계산 범위다. `capacityGridSteps`·`capacityBisectionWidth`는 이 범위에만 적용하고 `capacityTailSamples` 등 rate 판정 설정은 현재 검사에도 사용한다. 기존 설정의 기본값·strict 범위와 offline 입력 기록은 유지한다([설정 설계](configuration-hardcoding.md#3-serving-settings와-요청별-reuse)). 이 결과를 online κ̂나 현재 E2E로 표시하지 않는다.


**디스패치 점유.** d는 firstfloor 인스턴스가 요청 하나마다 profile 실행시간 밖에서 더 점유되는 시간이다. 포화한 인스턴스 하나가 배치 b로 낸 지속 처리율 X에서 `d = 1/X − (배치 b의 profile 평균 실행시간)/b`다. d에는 배치 사이 actor 시간(결과 전달, 다음 배치 선택, runner 호출 준비)과 같은 Pod CPU를 쓰는 요청 수신과 응답 처리로 늘어난 runner 실행시간이 함께 들어간다. rasp5 q1(m4 r3 Q2, 15:46~15:51)은 b1로 26.88 req/s, 요청당 37.20 ms였다. Q2 행 전체에서 실측 `serviceMS` 평균은 약 34.5 ms, 실행 기록 사이 간격은 평균 약 3.1 ms다. profile b1 평균은 27.79 ms라 d = 9.41 ms다.
- 기준은 profile 평균이며 fixed `B/(profileMean+B×signed d)=X`다. max0 투영에는 등식이 그대로 성립하지 않는다. rasp5 양수d는 μ26.88/κ25.3(m29)/25.1(m139)이고, service p50 30.7ms 기준 d6.5ms는 μ29.2/κ27.7로 X를 넘었다. profile 평균·signed 추정·정책 출처를 보존한다.
- Selection order is rolling measured B median admitted by configured `minimumPairs`, then legacy Accepted DOM, then the configured default. Measured B matches exact Node UID, model, power mode, execution path, batch and profile ID. Raw legacy DOM keeps its previous Node UID/mode/model wildcard match across profiles and batches; runtime image remains historical evidence, not a matching or numerical-key condition.
- `defaultDispatchOccupancyNanos`는 모든 batch에 쓰는 단일 ns/request 설정이며 batch별 값은 measured B만 제공한다. rate 판정·offline throughput은 `b/(profileMean(b)+b×d)`를 사용하고 replay는 시작한 batch마다 `actual batch size×d`를 한 번 더한다. signed B 원표본은 유지하고 운영 d는 `max(median,0)`이다. profile routing의 `RoutingWeight`와 이 처리율은 구분한다.
- `CapacityInput.dispatchOccupancy`와 `Snapshot.dispatchOccupancy`는 exact identity, evidence `source`, `provenance{source,pairs,window,updatedAt,value}`를 유지한다. 출처·pair 수·시각만 바뀌고 선택 d가 같으면 수치 결과를 재사용할 수 있으나 option·SLO·정책·TOTAL·POST 등 전체 수치 입력은 같아야 한다.
- Legacy `serving/dispatch-occupancy/` v1alpha1 Accepted records and new `serving/dispatch-occupancy-observations/` v1alpha2 Observed/Insufficient burst schemas stay distinct and are not deleted. Scheduler adoption consumes the separately qualified rolling cache, not every raw Observed record ([measurement-api](measurement-api.md) 3, 4장).

**수치와 물리 실행 가능성.** no-profile·raw support 부재·수치 invalid를 낮은 지연으로 표시하지 않는다. Node readiness·예약·GPU inventory·memory 예산·실물 소유권은 현재 snapshot의 별도 admission 조건이다. 수치 결과가 Ready를 만들거나 자원 부재를 덮지 않는다. 물리 상태가 복구되어도 현재 timing과 demand로 다시 후보를 평가한다. 원 profile·raw·이전 offline 계산 기록은 보존한다.

## 3. 판정과 순서

**A — 명시적 최소 수요.** `settings.minimumDemand`는 Workload UID별 최소 요청률(req/s)이다.
키는 비어 있지 않아야 하고 값은 유한한 0 이상의 수이며, 생략한 UID의 하한은 0이다.
아래 관측식의 결과를 `λ_observed,w`라 하면 실제 계획률은
`λ_w = max(λ_observed,w, minimumDemand[w])`다.
용량, 전환 대기, 분배와 요청 에너지는 모두 이 유효 요청률을 사용한다.
유휴 제거와 교체 회수의 최소 요청률(`minRate`)에도 같은 하한을 적용한다.
따라서 도착과 대기가 0이어도 하한을 처리하지 못하는 저전력 구성을 고르지 않는다.
이는 예측이나 자동 증가량이 아니라 사용자가 선택한 용량 예약이며 관측 창 보호 규칙을 대체하지 않는다.
직접 replay의 arrival population은 기존 관측 패턴을 반복한다. minimumDemand가 그 집단보다 크면 [M0](path-aware-implementation-plan.md#33-처리율탐색최종-평가)의 부족분만 균등한 합성 요청으로 보충하고 모든 후보에 같은 집단을 쓴다. 이 보충은 모델 가정이며 raw/actual 요청이 아니다.

구성 c와 전환 사슬의 route 시각 r에서 모든 Controlled Workload에 같은 현재 수요를 적용한다.

| 값 | 정의 |
|---|---|
| `λ_observed,w` | `max(장기 계산 도착률, 최근 계산 도착률) + q_w / 장기 관측 길이`. 장기는 `observationWindowNanos`, 최근은 `controlIntervalNanos`이며 두 구간의 끝은 `snapshot.observationEnd`다. 장기의 시작은 `max(observationEnd−observationWindow, observedSince)`, 최근의 시작은 `max(장기 시작, observationEnd−controlInterval)`이다. 각 반열린 구간의 `Compute` 또는 `Accepted` 도착 중 불변 정의가 있는 수를 해당 실제 관측 길이로 나눈다. 실패와 취소도 이 도착 정의를 따른다. 미완료 기여는 장기 관측 길이로 한 번 더한다. 장기 관측 길이가 `min(observationWindow, controlInterval)`보다 짧으면 `Pending`(`PartialObservationWindow`)이고, 장기 관측 창이 덜 찬 동안 계획은 확장만 한다 |
| `λ_w` | `max(λ_observed,w, settings.minimumDemand[w])`, 단위 req/s. 탐색·직접 후보 평가·nominal rate 검사·요청 에너지에 사용하는 유효 수요다 |
| `r` | route 전 사슬의 최대 시간(동작마다 측정 trial 최대 시간) |

저비용 탐색은 Tick당 profile 항을 한 번 가공하고, move마다 routing/fluid rate·retained rate·가중 service의 scalar 합만 갱신한다. [M0 §3.3](path-aware-implementation-plan.md#33-처리율탐색최종-평가)의 heuristic은 실제 E2E·tail bound·capacity certificate가 아니다. `State.Delta`에서 demand replay나 실패율 추정을 실행하지 않는다.

`q_w` counts requests not yet observed to have started service: gateway
`phasePending`, native `phaseQueued`/`phaseDeferred`, and `phaseAssigned` without
native observation, provided `SchedulerRequest.RemainingLifetime` is nil or
positive. Assignment reserves a physical slot before native dispatch; it is not
proof of service start. Observed running and returning work is not backlog.
The exact deadline accessor is `SchedulerRequest.RemainingLifetime`, projected
from `Call.receipt.Deadline` at the snapshot clock and tightened by native
`RemainingLifetimeMS` when present. Expired ownership is not live SLO demand.
`Outstanding`, occupied slots, current capacity, completion/drain/removal proofs,
route ownership and handoff fences are unchanged. Arrival history is unchanged;
only q and its q/window contribution to λ use this waiting-demand projection.

keep와 exact finalists는 현재 outstanding, 실제 transition schedule, 정수 routing과 비교기간의 finite arrival population을 replay한다. 비교기간 뒤 신규 도착 없이 완료·drop·만료·미완료를 수집한다. retained worker가 실제 처리한 만큼만 queue를 줄이며 running batch는 원 worker의 conditional residual을 유지한다. λ/κ̂나 backlog/κ̂를 완료 지연으로 사용하지 않는다.

최종 순서는 물리 eligibility 다음 전체 Workload의 SLO 충족 여부다. 충족 후보끼리는 기존 에너지·keep·논리 key 순으로 비교한다. 미충족 후보는 Workload별 실패 비율, 성공 요청의 상대 지연 초과, 조건부 potential 초과 순서의 tuple을 비교한다. potential은 실제 실패의 완료시간이 아니며 성공 표본이 없으면 성공 percentile은 null이다. 모집단이 0인 Workload에는 현재 비교 의무가 없다.

충족 판정은 불변 Definition별 전체 요청 n건에서 `ceil(n × percentile / 100)`번째 지연을 선택한다. 완료 지연과 함께 drop·실패·만료·미완료를 수학적 +∞로 포함하며, 늦은 완료와 실패가 같은 허용 tail을 쓴다. 목표와 같은 지연은 충족한다. p99/1,000건은 목표 이내 990건과 miss 10건이면 충족하고 989건이면 미충족이다. p100은 miss를 허용하지 않는다. 별도의 실패 0건·만료 0건 gate는 없으며 직접 replay, 현재 rate 검사와 offline capacity window가 같은 판정을 사용한다. window의 조기 종료도 허용 miss 수를 넘었을 때만 가능하다.

`SuccessQuantileNanos`·`SuccessExcess`는 성공 완료만의 진단값이다. `SchedulerEstimate.ExpectedNanos`는 전체 모집단의 선택 순위가 유한한 완료에 닿을 때만 숫자로 남기고, 실패·미완료 tail에 닿거나 모집단이 없으면 null이다. `Samples`는 성공 완료 표본 수다. 원 outcome별 수와 실패 비율은 그대로 보존하며 실패에 완료시간을 만들거나 JSON +∞를 기록하지 않는다. v1/v2는 이 공통 충족 판정 뒤 충족 후보끼리 에너지를 비교한다.

Workload 순서는 [M0 §3.4](path-aware-implementation-plan.md#34-비교-정책)의 두 정책을 사용한다. 기본 `excess`는 나쁜 tuple부터 비교한다. `workload`는 설정 UID 순서, 미지정 UID의 worst-first 순서로 비교한다. 빈 순위는 `excess`와 같고 저장된 미등록 UID는 보존하되 현재 판단에서는 제외한다. tuple 동률 뒤에만 기존 에너지·keep·key를 사용하며 old `Final` 우선순위를 앞에 두지 않는다.

## 4. 노드 계획과 탐색

**노드 계획.** 노드마다 keep과 측정 option 변형 하나를 후보로 만들며 현재 snapshot의 readiness/측정·소유 scope, GPU inventory/중복과 memory 예산을 매번 검사한다. CPU 독점 mask·예약 quota·mode별 CPU inventory는 관문이 아니다. 노드마다 서빙 인스턴스는 하나이며 같은 노드의 공급을 중복 합산하지 않는다(m4 r8 D-R8-1의 공존 간섭 근거는 과거 관측으로 보존한다). 새 공유 실행 조건에 맞는 측정 option을 사용한다.

**인스턴스와 Workload.** 인스턴스는 Workload 하나에 속하고 profile·SLO·timing·검사 결과도 그 UID에 연결한다. 노드마다 서빙 인스턴스는 하나이므로 같은 모델의 Workload들도 노드를 따로 쓴다. 실제 상태에서 인스턴스 둘 이상이 있는 노드는 keep이 그대로 두고, 그 노드의 다른 계획은 인스턴스 하나를 남긴다. 계획기가 만드는 분배에서 인스턴스마다 Workload는 하나다. 서빙 source는 routing annotation이 Workload를 하나 이하로 가리키는 Pod이므로 실제 분배에서도 인스턴스마다 Workload는 하나 이하다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이므로 꺼진 Workload의 SLO는 계획 동작의 영향을 받지 않는다.

**사슬.** `schedulerPlan`의 순서다. 모드 변경은 power, 모든 기존 remove, prepare가 route 전이다. 같은 모드에서는 자원이 함께 들어가지 않을 때만 remove가 prepare 앞이고, 아니면 route 뒤다. 동작마다 측정 trial의 평균 wall J, 평균 시간, 최대 시간을 쓴다. 비용이 없으면 그 계획은 `MissingOperationCost`로 빠진다. 같은 인스턴스의 배치 상한이나 대기 시간을 바꾸는 계획은 그 인스턴스를 그대로 쓰고 route가 firstfloor policy를 바꾼다(사슬 없음). 서빙 Pod는 측정한 모든 배치를 시작할 때 초기화하고(`batchSizes`, m4 r2의 rasp5 q2 config는 1~16), firstfloor `Scheduler.Update`는 초기화되고 측정된 배치 안에서 policy의 `maxBatchSize`와 `maxBatchWaitMS`를 재시작 없이 바꾸며, route의 `applyPolicy`가 그 값의 native ACK를 기다린다.

target에 인스턴스가 없고 실제로도 빈 노드의 새 `Fixed` 절전은 §15의 현재 수요와 일반 교체의 동일 H·minimumGain·payback을 통과한 경우에만 다른 노드의 route와 병렬로 계속한다. 모든 후보에 미리 공통 절전을 넣어 keep 대비 비용을 소거하지 않는다.
target이 사용하는 노드의 `Fixed` 내림, 전력 올리기, remove와 prepare는 같은 노드의 순서를 유지한다.
route는 그 준비와 전력 사슬, 실제 Ready와 소유권, policy ACK와 기존 요청 인계를 유지한다.
공통 절전의 실제 소비와 시간은 전체 POOL 측정에 계속 포함한다.

**실행 중 중간 분배.** 최종 DAG·분석 r은 전체 준비 사슬을 유지하되 매 Tick 검증된 current 경로와 독립 Node의 exact identity/native policy ACK된 Ready 목적지만 합친다. `SchedulerReadyExpansion`도 현재 timing·수요·정수 routing으로 직접 후보 평가를 사용한다. old 공급과 accepted work를 보존하며 같은 Node 공급을 중복 합산하지 않는다.
물리 상태·profile support·수치 평가를 확인할 수 없으면 current를 유지한다. serving 없는 bootstrap만 Ready 부분집합의 weight를 1000으로 재정규화하며 stopped·retiring 실물은 제외한다. κ̂=0이나 old row pending은 단독 거절 사유가 아니다.
독립 prepare/power/drain·실재 non-Ready sibling은 장벽이 아니다. launch dependenciesReady는 선택 Ready 부분집합, 최종 route는 전체 목표/원 DAG를 검사한다. 새 탐색·bootstrap SLO 보장은 없고 요청 수명/admission·기존 요청 보호는 유지한다.
실제 중간효과는 frozen target·before/actual routing으로 분석 예측과 구분한다([중간 배분](scheduler-implementation.md#중간-배분)).


**비교 기간 — SCH-58.** `H = settings.comparisonPeriodNanos` 하나를 결정 시점부터 keep과 모든 후보에 고정한다(기존 정책 600s 유지). 후보별 `r + max(post)` 연장은 없다. 아래 T는 H다. 전체 사슬이 H를 넘더라도 SLO 증설 후보로 사용할 수 있으며 `TransitionExceedsHorizon`은 진단이다. 전체 동작 비용 J는 전부 부과하고 H 밖 Serving 에너지는 계산하지 않으며 음수 꼬리는 없다. 유한 demand replay는 H 이후 신규 도착 없이 cohort를 drain한다. H는 SLO 시간·rate 판정 창·동작 timeout과 별개이고 새 설정 revision은 다음 판단부터 적용한다.

**에너지.** keep의 노드 에너지는 `P_before × H`, 전환은 `J_pre + J_post + P_mid × max(0, min(r,H) − t_pre) + P_after × max(0,H − r − t_post)`(t는 평균 시간)다. 전환 operation J는 완결 비용이므로 사슬이 H를 넘어도 전부 포함한다. bridge의 시작 지연은 H 안에서만 이전 baseline으로 센다. 요청 에너지는 [0,min(r,H)]의 retained 처리와 overflow, [r,H]의 최종 정수 weight 처리로 나눈다. H 뒤 serving은 0이다. 보호수요는 실제 λ로 비례 축소하고 backlog drain을 요청 J에 더하지 않는다. 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**탐색.** `planSearchStrategy`는 `v1` 또는 `v2`(기본)다. 계획 집합은 노드마다 계획 하나를 고른다. 공통 adapter가 직접 SLO 평가·물리 admission·에너지·후처리를 소유하며 v1/v2의 순회 구조와 설정 예산은 유지한다. pending replica는 Ready 공급 없이 복제 수 상한에 포함한다. retirement도 원래 계획을 읽는 정확 `admits`를 거친다.

v1은 keep과 모든 노드를 비운 집합(인스턴스가 없는 적격 계획, 없으면 keep)에서 시작한다. 각 출발점에서 노드 k개 이하를 바꾼 이웃 중 가장 좋은 집합으로 이동하며, 두 출발점의 k = 2를 마친 뒤 각각의 끝에서 k = 3을 이어 간다. 두 끝 중 순서가 앞선 집합이 해다. 공통 직접 평가와 위 에너지 식을 쓰며 route 시각은 집합에서 가장 긴 route 전 사슬이다. `maxReplicas`·`maxInstances` 초과(replicas OFF이면 Workload별 현재 수 변경), 새 인스턴스의 가중치 0, 보호 중인 분배 인스턴스의 가중치 제거는 허가하지 않는다. 걸음 없이 끝난 빈 출발점도 같은 허가를 거친다. 이웃은 `planSearchWorkers`개 goroutine이 나눠 평가하고 전순서로 합친다. 두 출발점에서도 닿지 않는 해는 놓칠 수 있으며, 고른 해는 14장의 교체 회수와 최소 이득을 거친다.

v2는 adapter가 만든 노드·option·모드별 계획표를 Tick마다 전처리한다. cap 변형의 `ZeroCapacity` 제거·κ-only 축약과 새 heuristic에 성립하지 않는 κ 전용 bound는 제거한다. κ̂=0이어도 물리적으로 적격인 개선 후보를 탐색한다. 증분 평가기는 바뀐 Workload의 scalar 항·route·노드 에너지만 갱신하며 정확 replay와 다른 근사 단계다. timing·예비·bridge·retirement 허가는 정확 깔때기에 남긴다.

출발점은 keep·빈 집합과 선택적 가격 유도 집합(`priced`)이다. `planPricedStart` 기본값은 true이며, searchable cap 전체를 대상으로 가격 반복 100회 뒤 얻은 집합이 앞의 두 집합과 다를 때만 셋째 출발점으로 넣는다. 피복은 heuristic 초과를 줄이고 인스턴스 상한을 복구한다. 연마는 k = 1·2 이웃의 최급강하를 반복한다. 더 좋은 이웃이 없으면 기존 활성 m개 cap 변형과 keep·현재 계획의 결합 탐색을 수행한다. `planPolishActive` 기본값은 8이며 0이면 모든 searchable 계획과 cap 변형을 활성화한다. κ-only 피복·에너지 하한을 새 heuristic의 증명 없이 가지치기에 사용하지 않는다.

탐색 중 상위 K개(`planExactFinalists`, 기본 16)와 각 출발점의 끝점을 중복 없이 모아 정확 깔때기로 보낸다. 끝점은 K 밖에서도 보존하므로 최종 후보 수는 K를 넘을 수 있다. **v2 선택은 공통 adapter의 직접 `evaluate`와 물리 `admits`를 통과한다.** 최종 순서는 3장의 직접 demand replay 결과가 결정하며 heuristic `Final`이나 κ̂로 앞서 거절하지 않는다. 정확 후보가 모두 물리적으로 거절되면 가지치기 전 keep과 적격 cap 전체의 정확 k = 1 탐색(`fallback: exactK1`), 그것도 선택하지 못하면 selector가 연결한 같은 마감의 v1 탐색(`fallback: v1`)으로 이어진다. v2는 v1 package를 직접 호출하지 않는다. `planPostFilterFinalists` 기본값 false에서는 정확 평가 1등을 고른 뒤 교체 회수·최소 이득을 적용한다. true에서는 후보별 adapter의 `payback` → `minimumGain` 뒤 결과를 비교하고 호출부에서 다시 적용하지 않는다. false에서도 후처리 진단은 계산하며, 원래 1등은 keep으로 되돌아가지만 살아남는 대안이 있으면 `postFilterReverts`를 기록한다.

v2의 `planSearchEvaluations` 기본값은 증분 평가 500,000회이며 `delta` 직전에 검사한다. 벽시계 마감은 v1과 같이 판단 시작 + `controlIntervalNanos/2`이고 계획 생성 시간도 포함한다. v2는 출발점 시작, 피복·연마의 반복/노드/이동 묶음 경계와 결합 탐색 재귀 진입에서 마감을 검사한다. 평가 수에 닿으면 `terminationReason: evaluationBudget`을 남기고, 벽시계 마감에 닿으면 `terminationReason: deadline`과 `budgetCut`을 남긴다. 두 경우 모두 그때까지의 후보·끝점을 정확 평가한다. 계획표 전처리·가격 반복·정확 깔때기·정확 k = 1 fallback은 이 증분 평가 수에 포함하지 않으며 그 안에서 마감을 검사하지 않는다. 따라서 벽시계 마감은 탐색 중단 기준이며 판단 반환의 엄격한 시간 상한은 아니다. v2는 단일 goroutine으로 UID·논리 key 순으로 순회하고, 고정 평가 예산과 같은 입력에서는 결정적이다. 벽시계로 잘리는 위치는 실행 환경에 따라 달라질 수 있다.

**router 가중치.** Routing ON은 기존 1000 단위 정수 allocator에 양의 profile `RoutingWeight`를 넣는다. 안정적인 입력 순서는 기존 per-input energy·physical ID 순서다. allocator 식은 [M0 §3.3](path-aware-implementation-plan.md#33-처리율탐색최종-평가)의 profile routing 계약을 따른다. Routing OFF는 현재 instance weight와 신규 option의 configured weight를 유지한다. nominal share가 RoutingWeight나 κ̂를 넘는다는 이유만으로 거절하지 않고 demand replay에서 overload를 평가한다. 요청 에너지는 같은 최종 정수 weight를 사용하며 keep의 weight만 바뀌면 사슬 없이 route 하나다.

`decision.capacity.workloads[].minimumDemand`는 해당 판단의 명시적 하한이고,
`rate`는 그 하한과 관측 수요 중 큰 값이다. 미설정 하한도 기록에서는 0으로 남긴다.

**판단 기록.** `ExpectedJoules`는 최종 정수 weight로 계산한 기존 에너지 합이다. 판단에는 현재 rate·queued·timing 입력, route·전환 사슬·에너지, 물리 적격성·제외 사유, heuristic과 exact 단계의 결과를 구분해 연결한다(`SCH-30`). κ̂는 실제 검사 rate·outcome·전체 수치 입력과 함께 제공하고 완료된 전체 용량표로 표시하지 않는다. failure/finite-success/potential의 의미는 [M0](path-aware-implementation-plan.md#34-비교-정책), 외부 필드 projection은 [scheduler API](scheduler-api.md)를 따른다. 평가한 모든 plan-set 목록은 기록하지 않는다.
Workload projection은 `routingRate`·`retainedRoutingRate`·`tests`·`keepTests`·`weights`, Node plan은 `routingRate`·`retainedRoutingRate`를 사용한다. 전환 예비는 `requiredRate`·`beforeRouteRate`·`afterRouteRate`, bridge는 `routingRate`로 profile-rate estimate를 드러낸다. 이 필드를 SLO κ나 대기 완료시간으로 해석하지 않는다.

`search`는 전략 `strategy`, 사유별 가지치기 수 `pruned`, 증분 평가 수 `surrogateEvaluated`, 깔때기로 보낸 후보 수 `finalists`, 정확 평가 수 `exactEvaluated`, 선택 후보의 증분 J − 정확 비교 J인 `surrogateGapJoules`(후처리 비교 ON이면 후처리 뒤 J와의 차), `postFilterReverts`, `fallback`, 출발점 `start`를 해당 경로에서 기록한다. `exactEvaluated`는 후처리 내부 재평가를 세지 않는다. `dualBound`는 가격 반복의 선형화 진단값이며 실제 목적함수의 하한이나 최적성 보증이 아니다. v1 fallback으로 넘어가면 선택해와 정확 탐색 통계는 v1 값으로 교체되고 `strategy: v2`, `fallback: v1`을 남긴다. 증분 걸음·평가 수는 원래 v2 기록을 유지하며 탐색 시간은 두 탐색을 포함한다.

`steps`는 출발점별 origin(`k: 0`, `evaluated: 0`)과 수락된 걸음만 기록한다. v2의 `phase`는 `cover`·`polish`·`combine`이며 origin과 v1에는 없다. v2의 `k`는 실제 바뀐 노드 수이고 `changedNodes`는 같은 출발점의 직전 선택과 비교한다. `evaluated`와 `durationNanos`는 수락한 피복 반복·연마 훑기·결합 탐색의 증분 평가 수와 시간이다. `joules`·`keepJoules`·`feasible`·`final`은 증분 평가 키의 값이며 깔때기의 정확 J와 다를 수 있다. origin의 초기 평가와 수락되지 않은 훑기는 `trailingEvaluated`에 남겨 `Σ steps.evaluated + trailingEvaluated = surrogateEvaluated`를 만족한다. 가격 반복은 증분 평가기를 호출하지 않으므로 이 합에서 제외한다. 수 상한 복구 중인 cover는 충족을 잃을 수 있지만 polish·combine은 엄격 개선만 수락한다. `terminationReason`은 `noImprovement` 또는 `budgetCut`이며 예산 종료는 마지막 기록 걸음에만 표시한다.

`key`와 `joules`는 선택한 공식 탐색의 후처리 전 선택해와 정확 J다. v2의 v1 fallback은 위 `strategy`·`fallback` 기록 규칙을 따른다.

rate 판정 replay의 요청 J/요청 수로 기존 planner energy를 교체하지 않는다. planner는 기존 profile의 b1 입력당 에너지와 4장의 분석식을 유지한다.

`decision.search`의 `windowStart`, `windowEnd`, `recentWindowStart`는 두 구간을 나타낸다.
Workload별 `loads`의 `computeInputs`와 `inputsPerSecond`는 장기 관측값을 유지하고,
`recentComputeInputs`와 `recentInputsPerSecond`는 최근 구간의 계산 도착 수와 요청률이다.
`decision.capacity.workloads`의 `queued`와 `rate`로 미완료 기여를 포함한 최종 λ를 확인한다.
모든 후보는 같은 현재 유효 수요로 비교한다. 현재 유효 수요는 max(장기 관측률, 최근 관측률) + outstanding/실제 관측 길이에 minimumDemand 하한을 적용한다.

**계산 비용의 과거 근거.** 기존 하네스의 v1 평가 약 5 µs, v2 scalar 평가 555.7 ns·할당 0, 기본 예산·m = 8에서 탐색 0.42~0.54 s는 [§7.8](capacity-table-evidence.md#78-v2-계획기-측정-초안-단계별-게이트-수치)에 보존한다. 이 수치를 새 exact replay·rate 검사·Tick 총비용으로 사용하지 않는다. 새 통합본의 CPU·메모리·할당·Tick 시간은 별도 검증 대상이다.

## 5. E2E 피드백과 요청 동결

시간 수식·표본 결합·관측창 가공은 [M0 §3.1~3.2](path-aware-implementation-plan.md#31-시간-회계)를 단일 원본으로 사용한다. Workload별 TOTAL·POST는 SLO percentile로 가공하고 유효 표본이 없으면 기존 `completionMarginMS`로 초기화한다. 첫 표본부터 갱신하며 새 sample gate나 configured margin의 중복 가산은 없다.

실제 요청은 accept 때 POST를 동결하고 native sender에는 실제 server monotonic elapsed를 전달한다. Tick의 PRE·TOTAL을 실제 sender에 더하지 않는다. 진행 중 native 요청은 frozen sender·POST와 captured native elapsed·remaining을 사용한다. returning은 terminal native elapsed를 유지한다. 합성 요청의 chooser도 POST를 소비하므로 새 피드백에서 기존 CDF에 시간 차이만 더하지 않는다.

POST는 외부 왕복을 포함한 회계상 reserve이며 물리적 편도 반환시간이 아니다. canonical service·queue·d는 native 전체 시간을 근사하며 실제 client E2E tail을 보장하지 않는다. 실패 포함 client 모집단과 시간 분해가 가능한 결합 표본 수를 구분한다. 과거 E1/m4의 고정 m 결과는 기존 [용량표 근거](capacity-table-evidence.md)·[POOL 검증 기록](workload-pools.md#93-검증-기록)에 보존한다.

실측 지연 구성의 `PathFeedback.observedMean`은 동일한 유한 결합 cohort의 산술평균이다. 구성 요소별 분위수를 합산하지 않는다. 경험 TOTAL·POST 분위수는 planner budget 입력으로 따로 유지하며 실패·미결합 client 모집단과 구분한다.

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
| rate 판정 설정 | `capacityWindowNanos`>0, `capacityWindows`≥1, `capacityPassingWindows`는 1부터 전체 창 수까지, `capacityMinimumRequests`≥1이다. grid·이분 설정은 명시적 offline 계산에만 사용한다 |
| 비교 기간 | `comparisonPeriodNanos`>0인 정수 ns; H의 유일한 필드다. 제안 600초. 후보 집합과 무관하며 실제 H를 `decision.capacity.periodNanos`에 남긴다 |
| 디스패치 점유 | snapshot의 `dispatchOccupancy`는 immutable 측정 record에서 도출한다. 행 입력에 Node UID, mode, runtime/model, 값과 source를 포함한다 |
| 탐색 병렬 | `planSearchWorkers`≥1이다. sample은 3이며 v1·v1 fallback에만 적용한다. v2는 단일 goroutine이다. CPU 예약과 무관한 도구 병렬성이며 실제 경합·gateway/Tick 관측으로 평가한다 |
| 판단 기록 | 구성의 demand 판정·현재 tested rates·에너지·사슬·search를 연결한다. 과거 `capacity-row-built`는 역사적 offline 행 기록이며 online 준비 gate가 아니다 |
| 인스턴스 시간 | `unroutedSince`는 controller 유휴 이력이며 `heldUntil`은 Overload 준비 유지에 사용한다. 이력은 retirement의 SLO·H 판정을 면제하지 않는다 |
| 최소 이득 | `OperationTrial.energyUncertaintyJoules`와 실제 선택한 `NodeBaseline.wattsUncertainty`를 signed 노출량으로 전파한다. `gainUncertaintyJoules`는 B-only 문턱이다 |
| 정책 시간 | ControlInterval, ObservationWindow, ComparisonPeriod 세 시간만 사용한다. 전환 예비는 실제 Ready/ACK 공급으로 검사한다 |

설정 decode는 strict하다. API, ConfigMap과 Dashboard는 같은 설정 계약을 사용한다.
저장 형태는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 single-version reader를 따른다.
Timing이 없는 비용의 미기록 B를 관측한 0으로 취급하지 않는다.
저장 reader는 단일 버전이다. G2 인벤토리(`ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json`)의 catalog legacy49/랭킹9와 동결 E5 입력 legacy0은 서로 다른 범위다. 승인 재측정2 roots/18 steps(직접 전환 포함 상한4/22), raw80키·terminal OCM6 정리는 [측정 API](measurement-api.md)의 실제 소비·반환·백업·입력 동결 계약을 따른다. 원 raw/status·과학 증거는 보존하며 실행 완료를 추정하지 않는다.

## 8. 측정·검사와 실행 경계

- profile/runtime/측정 계약 변경은 해당 NodeUID/mode/model의 d, 정확 profile prepare/remove, loaded/unloaded baseline·전력 전환 비용을 열거하고 대표1건의 생성→raw→projection→계획 소비→실물 반환 뒤 대량 수집한다.
- 과거 mobilenet CPU8/GPU2 및 D2/Smol 원시·status·publication은 그대로 보존한다. CPU request/quota/affinity를 선택했던 실행은 새 공유 실행과 동등한 profile/κ로 소비하지 않는다. 의존 캠페인 종료 뒤 strict 계약을 단일 전환하고 새 조건에서 측정한다. d 없으면 설정 fallback을 쓰고, 정확 비용 없으면 MissingOperationCost다.
- signed d와 max0 정책은 구분한다. Smol D04/D09/D14/D23/D33의 profile 포함95% 근사구간<0은 systematic profile-versus-serving bias이며 인과 기여 미확정; identity·harness/warmup/timing/시점 차이는 남기고 보정·추정정책은 M5 후보다. 근거: `ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.{json,txt}`.
- `E±B`, `B=S+G`는 [측정 API](measurement-api.md)를 따른다. minimumGain은 선택 evidence B만 전파하며 profile 통계·BEO fallback CI·시점/센서/모델 오차를 포괄하는 global95% 보장이 아니다.
- 검사 계약: feedback 변경 시 chooser 재실행, tested-rate 근거/빈 tests, κ̂=0 후보 보존, scalar Delta와 exact 재순위·두 정책, frozen POST, finite success/실패/potential 분리, memory·장치·Ready·소유권·make-before-break, replica·예산·동률, 동일 H clamp·signed 상쇄·문턱 equality·회복, weight1000·ACK·budgetCut을 확인한다. offline grid/NonMonotone 검사는 별도다. 이 목록은 새 source의 통과 기록이 아니다.
- 최종 dump의 source별 `hack/kappa`는 provenance·κ/status·시간·에너지 차를 남긴다. 승인 rev72 BEFORE/D2 AFTER는 자율MNV/native8 paired의 Tick/CPU/GC/mesh/요청/physical G를 비교한다. E1 무부하→유부하, E4 이동, E5-0~6/E5-S는 두 관측률·λ·보호κ·route시각·사슬·유효 offered 전체 결과·전환 포함 전체 POOL J를 연결한다.
- 과거 d/G/m·SLO/κ/R 결과와 새 evaluation는 Run ID/원시로 구분한다. 요청 에너지는 profile routing의 최종 정수 weight와 일치시킨다. RoutingWeight·κ̂·직접 모델 결과와 실제 장치 처리율·E2E·배치상태의 차이는 구분해 보고한다.
- BEFORE·유한 진단 최대1회·최종D2 최대1회 순서는 [POOL 검증](workload-pools.md)을 따른다. runtime7 digest·기존 자원·evidence store를 유지하며 paired 물리 보존 실패는 unknown/mismatch로 보고하고 paired 주장을 중단한다. 조건·판정·범위 변경은 승인 대상이다.
- 승인/manifest/NodeUID/profile/runtime/source SHA·원시만 선별 보존하고 `.token`/Secret/전체 캐시 백업은 제외한다. 로컬 결정 원문은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/{d-measurement-decision.md,deployment2-e5-proposal-20261024.md}`다.

## 14. 구현 규칙

4장의 탐색 순회는 `internal/scheduler/v1`·`v2`가 소유하고, `internal/serving/scheduler_capacity.go`와 같은 package의 수치·실물 adapter는 다음 공통 규칙을 적용한다.

- **설정 gate.** placement/resources/batching/membership은 현재 Node/GPU 선택/배치 상한/대기 시간에 각각 적용한다. CPU 선택 gate는 없다. maxReplicas/maxInstances는 Workload별·POOL 전체 인스턴스 수 상한이며 replicas OFF이면 현재 수를 유지한다. routing OFF는 현재/신규 configured weight를 고정한 같은 직접 후보 평가를 사용한다.
- **batching OFF의 고정 정책.** ON/OFF 모두 `CapacityVariants`로 측정된 cap 변형을 생성한 뒤 `controlAllows`가 Workload의 현재 `(MaxBatch, MaxWait)` 정책 집합에 속하는 변형만 허가한다. 여러 현재 정책을 임의의 최소/최대 하나로 축약하지 않는다. cap1도 측정·초기화된 변형일 때만 사용하며, 다른 제어가 켜져 있으면 고정 cap 안에서 placement/resources/replicas 후보 평가는 계속한다.
- **진행 중 전환.** 전역 계획기는 하나다. owner별 예약의 Node·Workload UID·Pod UID·physical ID와 frozen target만 고정하고 예약 밖 후보 평가·확장·준비된 공급 배분은 계속한다. 수치 κ̂ 미검사만으로 UID를 고정하지 않는다. 실패·효과 소실은 해당 owner DAG에 기록하며 다른 owner의 목표를 버리지 않는다. 준비 실패의 분류·재시도 계약은 [scheduler 구현](scheduler-implementation.md#53-전환에너지선택)을 따른다.
- **인스턴스 관측 실패.** 한 인스턴스의 Ready 미확인은 Workload를 `Fixed`로 바꾸지 않는다. 해당 실물은 자원 점유와 소유권을 유지하되 Ready 공급·목표 배분에서 제외하고 나머지 인스턴스와 독립 Node의 자동제어를 계속한다. routing OFF도 미확인 실물을 공급으로 세지 않는다. `decision.reasons`는 `UnconfirmedInstance:<physical ID>:<관측 사유>`를 기록한다. Observer는 Pod 변경 유무와 무관하게 다음 refresh에서 native probe와 exact executor 검증을 다시 수행하며 기존 refresh 간격·probe 기한을 유지한다.
- **동작 기한.** prepare는 cost의 측정 trial 최대 시간에 관측 창 하나를 더한 시간 안에 실행된다. 초과하면 `failed`/`ActionDeadlineExceeded`로 실행을 끝내되 원격 효과 미확인 예약은 유지한다. 취소도 같은 계약이다. 이후 효과 확인 또는 reset의 실제 정리 proof 뒤에만 예약을 반환하고 현재 실제 상태에서 재계획한다. 독립 범위의 다른 동작과 판단은 계속한다.
- **가중치 척도.** 지금 가중치의 합이 1,000이 아니면 첫 판단이 route 동작 하나로 1,000 척도의 가중치를 적용한다.
- **제외 사유.** 4장의 사유에 더해 기준 전력이 없으면 `MissingUnloadedNodeBaseline:<노드>:<모드>`, 측정 POOL 노드는 `MeasurementNode`, 준비되지 않은 노드는 `NodeUnavailable`, 비서빙 예산 노드는 `PlacementBudgetUnavailable`을 기록한다.
- **재사용 증거.** 분배되지 않은 준비된 인스턴스는 예약 slot(`targetID`)이나 그 Workload의 소유가 있을 때만 계획이 다시 쓴다.
- **미정 수치.** 성공 표본 없음은 percentile null, 계산할 실행 수단·support가 없는 potential은 null과 invalid reason으로 남긴다. 실패 replay의 -1이나 큰 유한 벌점을 실제 지연으로 사용하지 않는다.
- **탐색 비용.** 기존 증분 평가 예산과 벽시계 마감의 의미는 4장을 따른다. 정확 깔때기는 scalar 평가 예산 밖이며 전체 Tick의 엄격한 시간 상한을 뜻하지 않는다. 새 replay 비용은 과거 하네스 비용과 분리해 측정한다.
- **Retirement.** 유휴 시간은 target 변경 허가가 아니다. 측정 stop/drain/remove 사슬을 가진 일반 노드 계획으로 직접 demand·전환·예비·동일 H 에너지를 평가하고 평가한 instance만 실행한다. 전체 SLO 미충족만으로 개선 후보의 retirement를 일괄 거절하지 않으며 3장의 exact 순서를 admission까지 유지한다. keep가 충족할 때의 선택적 에너지 축소는 기존 minimumGain·payback을 거친다. keep 미충족 회복은 더 높은 에너지라도 SLO 개선 순서를 유지한다. 단일 Node의 제거만으로 optional shrinkage라 분류하지 않는다. Disabled 소유·부분 관측·hold·bridge·reserve와 node-local 확인·정상 handback은 유지한다. `unroutedSince`는 관측 이력이며 에너지 면제가 아니다. terminal 점유 정리도 기존 stop/drain/remove proof를 생략하지 않는다.
- **준비한 인스턴스의 분배.** 해가 준비하는 새 인스턴스는 모두 그 해의 가중치를 받는다. 새 인스턴스 하나라도 가중치가 0인 해는 후보에서 빠지고 교체 회수의 되돌린 해도 같다. 근거: m4 r7에서 rasp5 q1-lut와 q2(κ 25.69 + 29.30)가 지금 가중치 467/533으로 λ 40.35를 받았고, 적재한 genesis3 q2의 노드 전력(49.80 W)이 빈 노드(49.97 W)보다 작아 그 준비가 T 동안 1.5 J 싸게 나왔다. 그 인스턴스는 가중치 0으로 준비되고 약 80 s 뒤 유휴 제거로 지워졌고, 다음 판단이 다시 준비해 2 req/s와 40 req/s에서 약 100 s 주기로 되풀이했다.
- **만든 뒤 끊기.** 같은 모드의 routed source는 새 목적지의 실제 Ready/ACK·handoff 뒤 stop/drain/remove한다. 자원이 겹쳐 기존 routed source를 먼저 지워야 하면 기존 `RoutedInstanceStopsBeforeRoute`와 검증된 bridge 계약을 적용한다. 모드 변경의 사전 정지는 retained 공급에서 제외하고 실제 사슬을 replay한다. SLO 미충족 개선 허가는 accepted ingress의 소유·물리 drain·정상 반환 proof를 면제하지 않는다. 과거 m4의 68초 준비 중 outage는 [용량표 근거](capacity-table-evidence.md)의 당시 실행 근거다.
- **교체 회수.** 지금 인스턴스를 지우고 다른 인스턴스를 들이거나 배치 policy를 바꾸는 노드 계획(교체)은, 그 노드만 keep으로 되돌린 해도 충족하면 관측 창의 최소 부하에서 T 동안의 에너지로 비교한다. 최소 부하는 관측 창을 제어 주기로 나눈 구간마다의 도착률 중 가장 작은 값이다. 교체가 그 부하에서 keep보다 에너지가 크거나 같으면 keep으로 되돌리고 `ReplacementUnpaid:<노드>`를 남긴다.
- **최소 이득.** 고른 해가 충족하고 노드 하나라도 바꾸며 keep도 충족하면, T 동안 nominal 이득(keep−해)이 선택된 operation과 baseline의 조건부 B 민감도 문턱보다 클 때 해를 쓴다. 같은 immutable reference/trial의 signed 노출량을 먼저 합쳐 공유 근거를 상쇄하고 남은 절댓값을 합산한다. trial 평균은 B/N을 선형 합산하며 baseline watts 민감도는 실제 초를 곱한다. 이득이 문턱과 같거나 작으면 keep과 `TransitionGainWithinUncertainty`를 기록한다. keep infeasible이면 용량/SLO 회복을 유지한다. 문턱은 profile 통계오차와 BEO idle fallback CI 및 시점/센서/모델 오차를 포괄하지 않으며 global95% gain 구간을 뜻하지 않는다. 원시 profile 분산/CI, d의 profile 포함95% 분석과 κ/SLO 계산은 각 추정 계약을 유지한다.
- **부분 관측 창.** ChillServer가 시작한 뒤 관측 창 하나를 다 관측할 때까지 계획은 인스턴스를 더하기만 한다. 지금 인스턴스를 지우거나(그 노드 계획에 남지 않는 인스턴스) 배치 policy를 바꾸는 노드 계획은 `PartialObservationWindow`로 빠진다. 계획은 지금 인스턴스를 노드의 지금 모드에서만 다시 쓰므로, 인스턴스가 있는 노드의 모드 변경은 그 인스턴스를 지우는 계획으로 같이 빠지고 빈 노드의 모드 변경은 확장으로 남는다. 분배된 인스턴스의 가중치를 0으로 만드는 해는 후보에서 빠지며, keep의 가중치가 분배된 인스턴스를 빼면 지금 가중치를 그대로 둔다. 유휴 제거도 쉰다. 근거: m4 r7 07:00:38에서 재시작 19 s 뒤의 관측한 창은 λ 20.6이었다. 부하는 40 req/s였지만 클라이언트가 중단 동안 물러나 있어 ingress가 9.0 req/s(07:00:00~20)와 23.4 req/s(07:00:20~40)만 받았다. 계획은 그 λ로 분배된 rasp5 q1 하나를 지웠다.
- **계획에서 뺀 노드.** `settings.excludedNodes`(Node 이름 목록, sample은 빈 목록)의 노드는 측정 POOL 노드와 같은 고정 경로를 탄다. capture가 노드 사유 `ExcludedNode`를 붙이고, 그 노드는 keep뿐이며(배치, 교체, 지우기, 전력 변경 없음) 가용 노드 최저 모드 낮춤과 유휴 제거도 받지 않는다. 판단마다 `decision.capacity.plans`에 그 노드의 `ExcludedNode` 계획이 남고 snapshot 노드 `reasons`에도 남는다. 측정 POOL 노드의 인스턴스도 유휴 제거를 받지 않는다.

## 15. 현재 수요 후보 비교와 고정 H — SCH-55~58

시간 기반 축소·교체 허가와 관측 TTL·bootstrap 대기 상한을 제거한 현행 계약이다.
이전 두 박자 설계의 근거는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/`의
`intent/two-tempo-questions-20261026.md`와 `intent/checkpoints.md`에 보존한다.

### 현재 수요에 따른 후보 비교

- 모든 Tick에서 분배·증설·축소·교체 후보를 같은 물리 규칙으로 비교한다. v1/v2는 공통 직접 demand 평가·SLO·회복과 기존 에너지 검사를 사용한다.
- 유입 증가만으로 축소·교체를 금지하지 않는다. 현재 후보의 exact SLO 순서와 기존 에너지·minimumGain·교체 회수를 사용한다.
- 현재 유효 수요는 max(장기 관측률, 최근 관측률) + outstanding/실제 관측 길이에 minimumDemand 하한을 적용한다. reductionDemand도 이 현재 수요다. 별도 이력 최대값이나 지속 시간 조건은 없다.
- 최종 에너지 하강 route와 retirement의 `현재 유효 수요×(1+h)` 보호 조건과 설정 h는 유지한다. 이를 완료된 κ 또는 κ̂-only 보장으로 해석하지 않고 현재 routing·수요 평가에 연결한다. bridge/reserve·실제 요청 J는 할증하지 않는다.
- 실제 부족 회복, 부분 관측의 실제 가용 수요, unknown·예약·소유권·Ready/native ACK·old+new peak·bridge·reserve·H 검사를 유지한다. 중단형 power 변경은 handoff가 아니다.
- Terminated는 Ready 공급이 아니며 allocation 반환 증거도 아니다. same-Node stop/drain/remove 의존성을 유지한다.
- 실행 중 owner 예약과 frozen target을 보존한다. 이미 허가된 DAG의 후속 drain/remove는 계속하며 중간 배분은 실제 Ready/ACK·stop 제외·원자적 UID별 route 계약을 따른다.
- 빈 Node의 새 Fixed 절전도 같은 현재 수요·SLO와 gain/minimumGain/payback으로 비교한다. 별도 시간 허가나 이력 상태는 없다.
keep 비용·교체 payback·minimumGain과 `decision.capacity.periodNanos` 모두 같은 H를 사용한다. 사슬이 H를 넘는 경우 4장의 진단·에너지 clamp를 적용하며 H를 후보에 맞춰 늘리지 않는다.

**빈 Node 본전 — SCH-59.** `scheduler_plan.go:schedulerCommonNodes/changeNodes`가 새 Fixed 절전을 모든 후보와 keep에 선반영하는 예외를 제거하고, 일반 변경 후보의 기존 `capacityPlanner.payback`/`minimumGain`(`scheduler_capacity.go`)으로 비교한다. Fixed를 이유로 에너지에서 제외하는 경로도 제거하여 같은 H의 keep 원상태와 비교하고 전환 wall joules·전후 baseline을 한 번 포함한다. 별도 절전 계산기나 설정은 없다. H 안 nominal 이득>0, 기존 minimumGain 문턱 및 payback 모두를 통과해야 실행한다. `kube/scheduler.go`의 target 없는 `Keep.Actions`도 이 허가 없이 새 절전을 실행하지 않는다. jetsono MAXN→15W는 H=600초에서 −699J, 손익분기 약 52분이므로 보류하며 이를 위해 H를 늘리지 않는다. 이미 허가·실행 중인 Fixed의 완료 사슬은 보존한다.
구체적으로 `scheduler_capacity.go:687-689`의 빈 Node 후보 누락 경로를 이행하여 절전을 비교 후보로 생성하고 비교 집합에 포함한다. `:1489-1492`의 `plan.replaces` 한정 payback 대상에도 빈 Node 새 절전을 포함한다(위치는 변경 전 앵커). 공통 절전 선반영만 삭제하고 실제 후보 생성·비교·payback을 누락하는 구현은 미완료다. 다른 Node의 큰 이득이 손해 Fixed 절전을 덮는 조합에서도 해당 절전은 기존 개별 교체 payback 경로로 거절해야 한다.

### 전환 예비

`f = transitionReserveFraction`의 기존 범위·기본값은 유지한다. 양수이면 전환 구간마다 실제 Ready·ACK·ownership·예약상 사용 가능한 공급만으로 수요 대비 예비를 확인한다. 이 공급 수치는 profile-rate estimate이며 완료된 SLO 용량이나 κ̂ 합의 보장이 아니다. SLO는 별도 직접 replay로 판정한다.

아직 준비 중인 target, stopped/draining 또는 전력 변경 중인 공급은 세지 않는다. 같은 공급을 두 작업이 중복 담보로 쓰지 않도록 기존 범위 예약을 검사한다. 확보하지 못하면 `TransitionReserveInsufficient`로 해당 후보만 보류하고 유지·안전한 증설은 계속한다. f=0은 추가 예비 검사만 끄며 직접 SLO 평가·물리 조건은 유지한다. λ·요청 에너지의 분모를 바꾸지 않는다.

예비의 실제 적재 baseline·prepare/remove·전력 사슬은 동일 H 에너지에 한 번 포함한다. evaluation은 실제 전환 전체와 전체 POOL을 집계하며 예비 비율을 실제 SLO 보장으로 표시하지 않는다.

### 기록과 설정 고정

strict scheduler 설정 decode/검증·설정 API·ConfigMap·Dashboard·sample·Run 캡처는
7장의 같은 계약으로 함께 이행한다. `comparisonPeriodNanos`의 기존 단일 필드를
재사용하고 단일 허용값 enum/하드코딩된 선택지로 제한하지 않는다.
`decision.settings`와 `decision.search.bounds`에 세 설정값을 고정하고,
`decision.capacity.periodNanos`는 사용한 H와 일치시킨다.
결정에는 현재 rate·backlog·Ready profile-rate estimate·현재 tested rates와 후보의 제외 사유·에너지·gain·minimumGain/payback을 기록한다.
과거 raw archive와 게시 증거는 보존한다. 이전 최대수요 창의 게시 근거는
`O/campaign/v2/dashboard-acceptance-20261030/approved-publication-inputs.json`과 단계별 index에 있다
(`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`).
예비 평가는 Workload별 수요·필요 예비·구간별 Ready 공급 추정과 부족 이유를 같은 결정에 연결한다.
Run 시작의 `binding.schedulerSettings`, 설정 캡처와 summary는 같은 값을 보존한다.

### H 제안과 실제 전환 근거

제안 H=600초(`comparisonPeriodNanos=600000000000`). `ops/pools-deploy/campaign/v2/demo-*/summary.json`의 complete prepare/power `completedAt−startedAt` 근거이며 실패/미완료·Ready/전체 route 지연과 섞지 않는다.
| Run/단계 | 완료 동작 초 |
|---|---|
| demo-20261025-2/B; demo-20261026-2/B | jetsono MAXN150.939/148.854, 후속 MNV prepare12.011/13.693 |
| demo-20261025-2/C; E0 | lattepanda Smol268.867, jetson-nano223.339; genesis3 Smol292.163(최장,285로 대체하지 않음) |
| demo-20261026-2/B; C | genesis3/lattepanda/jetson-nano MNV9.873/12.717/26.745; rasp5 q1-lut/q1 17.796/16.518 |
| demo-20261025-2/C; demo-20261026-2/C | jetsono 하강196.180/168.016 |
292+196≈488초 직렬과 Serving 여유·기존10분 sample이 제안 근거이며 상한 보장/통계적 최적 H는 아니다. 3회차 G/L Ready15~18초/N32초 중간route는 반응성 근거이지 H 표본이 아니다. H는 승인 설정으로만 변경한다.

회귀 수용은 AC-42~44와 [M0 통합 검증](path-aware-implementation-plan.md#7-통합-검증과-배포-순서)을 따른다. 현재 수요·두 정책·물리 handoff·profile-rate 예비·동일 H clamp·Fixed 본전·예약 밖 확장을 연결한다. 운영300초/시연120초는 별도 binding/H로 보고한다. 새 통합본의 메모리·연산 리뷰는 push 전에 수행한다.


## 16. 같은 기종 profile 재사용 — PR-01..04

Catalog가 [측정 API §11](measurement-api.md#11-같은-기종-node-프로파일-재사용--pr-0104)의
확인 판정 뒤 대상 Node UID의 profile option을 만든다. 계획기의 물리 UID guard를 느슨하게 하거나
DeviceClass만 맞는 원본을 후보에 직접 넣지 않는다. 재사용 profile ID는 확인 측정 ID의
`/reuse-<source·허용값의 SHA256>` 파생 ID이며 수치 입력·불변 startup bundle·정확 비용 참조에도 이 ID를 쓴다.
서비스 분포·증분 에너지는 원본, idle/meter·runtime/GPU UUID는 대상 확인 근거다.
`profile.reuse`에 source/confirmation/result와 실제 차이를 기록한다.
대상 d가 없으면 설정 fallback으로 replay하며 정확 비용이 없으면 기존 MissingEvidence를 유지한다.
프로파일 공유가 d·prepare/remove·mode cost의 암묵적 공유나 SLO 충족을 뜻하지 않는다.

