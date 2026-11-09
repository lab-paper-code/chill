# 용량표 계획기 (M4)

[이행 대안](../architecture-rethink/README.md)과 구분한 현행 용량표 계획기 계약이다.
option과 Workload SLO마다 인스턴스 하나가 SLO를 지키는 최대 요청률 κ를 재생으로 계산한다.
ChillServer는 시작과 입력 변경 때 자동제어 Workload의 행을 계산하고,
Tick은 κ 합과 노드 계획을 만들고, `planSearchStrategy`로 고른 v1·v2 탐색으로 구성을 고른다.
[인스턴스 용량표 근거](capacity-table-evidence.md)와 `hack/kappa`는 계산과 비교의 근거다.
[M0](decision-record-diet.md)와 [M1](observability-split.md)의 기록 경계를 따른다.
본문은 선택 계약이며 실제 source 검증과 배포 실적은 실행 근거로 확인한다.

**패키지 경계.** 공통 CHILL 입출력과 수치 adapter는 `internal/serving`에 둔다.
snapshot·κ 행·노드 계획 생성, 정확/증분 수치 평가, 실물 admission·후처리,
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

## 2. 용량표

**행.** 키는 option(노드, 모드, 할당, profile ID), 배치 상한 b, SLO(백분위 p, 목표 L), 적용 total·post 경로 예산, 모델 판단 분위수, 선택한 수치 디스패치 점유 d, 용량표 설정이다. 출처와 관측 창·갱신 사실은 입력에 남기되 수치 키에서 제외한다. deadlinePolicy는 profile이 정한다. 행은 입력, κ, μ(b 이하 배치의 `b / (평균 실행시간 + b × d)` 최댓값), 격자 판정 결과, 창별 판단 분위수 지연을 담는다. 이하 m은 활성 total 예산이며, live와 replay의 배칭에는 같은 활성 입력의 post 예산을 사용한다([시간과 수명](scheduler-runtime-measurement.md#시간과-수명)).

**판정.** option 인스턴스 하나만 있는 keep snapshot에 요청률 λ의 포아송 도착을 넣고 `schedulerReplayEstimate`를 읽는다. 재생은 profile 실행시간 표본에 `배치 크기 × d`를 더하고 firstfloor `ChooseBatch`를 사용한다. 저장소와 배포 runtime의 표본 선택 동등성은 `TestPolicyFastPathsMatchDeployed`로 대조한다. 모델 판단 분위수의 지연(m 포함) ≤ L이고 timeout·만료가0이면 창은 충족이다. native 요청의 SLO p는 유지한다. p<100에서 α=1−p/100의 경로·worker 초과예산을 α/2씩 나누는 기준안의 판단 분위수는 `100−(100−p)/2`다. 모집단 크기·miss rank·최종 quantile·행 입력에는 같은 판단 정책을 사용한다. 요청 수는 `max(capacityMinimumRequests, ceil(capacityTailSamples/(1−판단 분위수/100)), ceil(λ×capacityWindow))`이며 p=100은 기존 별도 분기를 유지한다. seed1~capacityWindows 중 capacityPassingWindows 이상 충족하면 λ가 충족이다. 같은 seed 간격을 λ로 나누므로 판정은 결정적이다.
α/2 정책은 연결·활성화 전에 같은 profile·점유·수요·seed 원입력으로 κ(p)와 κ(1−α/2)의 영향을 비교하고 수용해야 한다. 통계 정책만 바꾸는 공통 모집단 실행과 실제 표본 정책의 비용 실행을 구분하며 κ·선택 구성·동일 비교기간 에너지·불가 판정·계산시간·메모리를 남긴다. 저장된 `PercentileMS`에는 총 모델 지연·조기 실패 -1·미계산 창이 섞이므로 이 열에서 높은 분위수를 복원하지 않는다. α/2의 합집합 경계는 경로·worker 독립성을 요구하지 않지만, 경로 표본 직접 주입 대안에는 결합·조건부 표본 근거가 필요하다.

**κ.** 요청 수와 배칭이 λ에 따라 바뀌므로 단조성을 가정하지 않는다. `G=capacityGridSteps`, `λ_k=(k/G)μ`(k=1~G)를 전부 판정한다. 충족 앞부분/미충족 뒷부분으로 나뉠 때 마지막 충족과 다음 격자 사이를 `capacityBisectionWidth×μ`까지 이분 탐색한다. K=0이면 κ0, K=G이면 κ=μ다. 미충족 뒤 충족이면 NonMonotone으로 κ가 없고 CapacityRejected다. 측정 d가 없으면 설정 d로 계산하며 미측정 자체는 거절 사유가 아니다. 격자 결과는 행에 보존한다. 격자·이분 폭·tail은 코드 상수가 아니라 기존 settings의 추가3개이며 기본20/.005/100을 유지한다. 모두 CapacityInput/용량 행에 포함하고 기본값도 새 입력 키로 재계산한다([설정 설계](configuration-hardcoding.md#3-serving-settings와-요청별-reuse)).

계획·keep·retained·정수 routing과 replay 공급은 선택한 정책 행의 κ를 그대로 쓴다. b1 붕괴는 runtime `56ef4f7b`의 배치 선택 수정으로 해결했으므로 별도 `min(κpolicy, κb1)` envelope는 적용하지 않는다.

**불가 조기 판정.** b 이하 모든 측정 배치의 관측 최소 실행시간 중 최솟값 + m > L이면 백분위와 무관하게 단일 배치 처리만으로 모든 요청이 초과하므로 재생 없이 `Built`, κ 0, `BatchServiceExceedsSLO`와 `minimumServiceNanos`(m·L은 입력)를 기록한다(등호는 재생). 재생 창은 확정 초과 요청이 `N − ⌈판단 분위수×N/100⌉`개를 넘거나 timeout·만료가 하나라도 있으면 즉시 실패(-1, 완전 분위수 없음)로 끝낸다. 일반 grid κ=0은 시험한 최소 rate 아래의 양수 처리율을 인증하지 못했다는 뜻이며 최소 service 불가 증거와 구분한다. 행 취소·적용 전환은 아래 계산 수명 계약을 따른다.

**설정.** `capacityWindowNanos` 60 s, `capacityWindows` 20, `capacityPassingWindows` 19, `capacityMinimumRequests` 1,000. 60 s 창이 필요한 근거: 요청 수 창만 쓰면 빠른 option(genesis2 cuda, 2,000 요청이 14 s)의 κ가 μ에 붙어 relaxed 1.2κ에서 20/20 충족이었고, 60 s 창에서는 0/20이다. 20개 중 19개 기준이 필요한 근거: 창 하나로 정한 κ는 1.0κ에서 6~12/20만 충족했다.

추가 설정의 범위는 capacityGridSteps·capacityTailSamples 정수≥1(Go 표현 범위), capacityBisectionWidth finite (0,1]다. 옛 κ 키 유지용 분기·dual lookup은 없다. 기본 결과 μ1000/κ959.375와 Grid/Bisection 판정은 변경 전 fixture와 대조한다.
`SchedulerSettingsInput`이 생략값 20/.005/100을 한 번 채우고 ConfigMap·HTTP·직접 κ 도구가 같은 경계를 사용한다. 실제 선택값은 Decision.Settings와 CapacityInput에 기록한다. 부동소수점 해상도보다 작은 폭에서는 중점이 더 이상 이동하지 않을 때 종료한다.


**디스패치 점유.** d는 firstfloor 인스턴스가 요청 하나마다 profile 실행시간 밖에서 더 점유되는 시간이다. 포화한 인스턴스 하나가 배치 b로 낸 지속 처리율 X에서 `d = 1/X − (배치 b의 profile 평균 실행시간)/b`다. d에는 배치 사이 actor 시간(결과 전달, 다음 배치 선택, runner 호출 준비)과 같은 Pod CPU를 쓰는 요청 수신과 응답 처리로 늘어난 runner 실행시간이 함께 들어간다. rasp5 q1(m4 r3 Q2, 15:46~15:51)은 b1로 26.88 req/s, 요청당 37.20 ms였다. Q2 행 전체에서 실측 `serviceMS` 평균은 약 34.5 ms, 실행 기록 사이 간격은 평균 약 3.1 ms다. profile b1 평균은 27.79 ms라 d = 9.41 ms다.
- 기준은 profile 평균이며 fixed `B/(profileMean+B×signed d)=X`다. max0 투영에는 등식이 그대로 성립하지 않는다. rasp5 양수d는 μ26.88/κ25.3(m29)/25.1(m139)이고, service p50 30.7ms 기준 d6.5ms는 μ29.2/κ27.7로 X를 넘었다. profile 평균·signed 추정·정책 출처를 보존한다.
- Selection order is rolling measured B median admitted by configured `minimumPairs`, then legacy Accepted DOM, then the configured default. Measured B matches exact Node UID, model, power mode, execution path, batch and profile ID. Raw legacy DOM keeps its previous Node UID/mode/model wildcard match across profiles and batches; runtime image remains historical evidence, not a matching or numerical-key condition.
- `defaultDispatchOccupancyNanos` is one scalar ns/request value for every batch, with no compiled policy default. Only measured B supplies batch-specific values. Regardless of source, throughput uses `b/(profileMean(b)+b×d)` and replay adds `actual batch size×d` once per started batch. Signed B statistics remain untouched; selected operational d is `max(median,0)`.
- `CapacityInput.dispatchOccupancy` and recorded decision `Snapshot.dispatchOccupancy` retain exact selected identities, the evidence `source` key and `provenance{source,pairs,window,updatedAt,value}` (`source`: measured/dom/default; `value`: operational ns/request). Numerical row keys ignore the evidence source key, runtime image and provenance. Refreshed pair counts/windows/timestamps with unchanged d reuse κ, while returned rows expose current provenance.
- Legacy `serving/dispatch-occupancy/` v1alpha1 Accepted records and new `serving/dispatch-occupancy-observations/` v1alpha2 Observed/Insufficient burst schemas stay distinct and are not deleted. Scheduler adoption consumes the separately qualified rolling cache, not every raw Observed record ([measurement-api](measurement-api.md) 3, 4장).

**검증된 모양.** 이분 탐색에 쓰지 않은 seed 21~40에서 0.9κ는 모든 option이 20/20, 1.0κ는 16~20/20, 1.2κ는 0/20이다. κ는 절벽의 무릎이다. d 없이 profile 실행시간만 재생한 E1 tight(p95 1 s, m 139 ms) κ: rasp5 q1 34.3, jetsonx 18.3, orangepi 8.1, jetsono 48.1~49.7, orin-nano 38.9~41.3 req/s. rasp5 q1은 실측 처리율 26.9 req/s에서 34.3이 과대임이 드러났고, d를 넣은 값은 위 문단의 25.3이다.

**계산하는 때와 대상.**
- ChillServer는 현재 계산 계약·수치 입력에 맞는 보관행을 먼저 읽고, 없는 행만 worker로 계산한다. 자동제어 Workload의 SLO, profile, 경로 예산, 디스패치 점유, 용량표 설정 변경은 최신 요구로 병합한다. 관측 변화만으로 실행 중 계산을 반복 취소하거나 같은 요구를 재시작하지 않는다.
- 대상은 자동제어가 켜진 Workload의 SLO와 그 Workload에 측정 근거가 있는 option 변형이다. 꺼진 Workload의 노드는 keep뿐이라(4장) 그 κ를 쓰지 않는다. E1에서는 tight 하나라 option 변형 38개, 38행이다(SLO 3개 전체는 114행).
- 행 하나가 작업 하나이고 행끼리 독립이라 병렬로 계산한다. capacity worker 수는 runtime GOMAXPROCS의 실행 병렬성일 뿐 CPU 예약·limit이 아니다. ChillServer는 CPU request/limit이나 limits.cpu 기반 GOMAXPROCS를 설정하지 않는다.
- 빠진 행은 Workload별 round-robin으로 실행 슬롯에 직접 배정한다. 대기 계산은 예산 상향을 우선하며, 실행 중 작업은 완료시킨다. 대기 행마다 goroutine을 만들지 않는다.
- 같은 불변 정의·판단 분위수의 관측 예산은 활성 창의 신뢰하한을 하강 재계산 기준으로 쓴다. 새 TOTAL·POST 상한 중 하나라도 활성 상한보다 크면 같은 새 창의 상한 쌍 전체를 계산한다. 혼합 방향에서는 다른 좌표의 하강이 겹치거나 하한이 없어도 상향을 막지 않는다.
- 상향이 없는 순수 하강은 감소하는 모든 좌표에서 `새 상한 < 활성 하한`일 때만 계산한다. 등호·겹침·하한 부재는 활성 쌍을 유지하며, 그대로인 좌표에는 분리 조건이 없다. 보류한 다른 수치 쌍은 활성 창·신뢰구간을 덮지 않는다. 같은 수치 쌍은 계산 없이 자기 최신 창·하한을 갱신한다.
- 실행 중 계산은 마치고 최신 대기 요구를 새 활성 쌍과 다시 비교한다. 최초 입력, configured↔observed 전환, 명시적 configured margin 변경, 정의·판단 분위수 변경은 기존 계산 수명 규칙을 따른다. 출처·창·표본 수·하한은 수치 행 key에 넣지 않는다.
- 이 규칙은 재계산 hysteresis이며 결합 95%나 반복 시점의 통계 보장을 뜻하지 않는다. 순수 하강에만 신뢰구간 분리를 요구한다. 혼합 방향의 낮아진 좌표는 새 창의 유효한 단측 상한을 사용하며 분리된 하강이라고 주장하지 않는다. 배치 상관·비정상성·완료 선택의 기존 추정 한계는 유지한다.
- 필요한 행이 완성되면 Workload별 활성 예산·κ·slack·신규 요청 기본값을 함께 전환한다. `Inputs`는 완성된 활성 행 그룹과 그 예산만 반환한다. 첫 완성 전·구조적 무효화 뒤에는 해당 UID의 행과 적용 관측 예산을 모두 생략한다. 신규 요청은 configured 기본값을 쓰고 planner는 UID별 `CapacityPending`을 유지한다. `Rows`는 최신 요구의 Pending·부분 완료를 표시한다. 현재 계산 계약의 archive Built·NonMonotone 행은 수치 캐시로 재사용한다. 일반 오류·부적합 행은 새 계산으로 처리한다. 최신 요구가 높은 동안의 활성 판단을 최신 관측의 검증 결과로 표시하지 않는다.
- Tick은 완료된 수치 입력을 Runtime과 공개 snapshot에 함께 반영한 뒤 판단을 기록한다. 이후 기록 실패·취소는 물리 동작을 막으며 이미 적용한 수치 상태는 유지한다. 중간 Ready 확장과 Continue도 이 행·예산 쌍을 사용한다. Continue의 새 관측은 `pathBudgetObservations`에만 보관하고 `pathBudgets`는 실제 적용값을 유지한다. background 계산 완료만으로 실행 중 handoff의 입력을 바꾸지 않는다.
- 행마다 `management` stream에 `capacity-row-built` 사건(행 전체, 계산 시각, 걸린 시간)을 남긴다. 이 사건은 기록이고 판단은 읽지 않는다.
- 각 Tick의 planner는 병렬 탐색 전에 option/Workload별 행 키·κ·사유와 현재 인스턴스별 κ·행을 한 번 해석한다. 누락·거절 결과도 포함한 이 읽기 전용 표는 그 planner에서만 살며, 후보 평가는 디스패치 점유 조회나 행 키 직렬화·해시를 반복하지 않는다. 다음 Tick은 새 snapshot과 행으로 다시 만든다.

**계산 시간과 행 수명.** 재생은 미완료 도착을 훑고 다음 도착은 정렬목록 선두, Definition.Identity는 도착 원형당1회, 배치는 정렬 분위수 색인·재사용 공간을 쓴다. E1 tight38행 단일코어1,386초; m4/r7 합성d114행은 GOMAXPROCS12에서132초/CPU804초였다. 최종 evaluation는 같은 dump로 source별 측정한다. 구조 변경은 활성 그룹을 철회하되 새 그룹에 필요한 동일 수치 행·실행 중 계산을 유지하고 불필요한 작업만 취소한다. 활성·진행·최신 그룹이 참조하지 않는 rows/asked는 회수한다. 출처 갱신은 현재 입력에 표시하며 κ를 다시 계산하지 않는다.

**κ와 현재 실행 가능성(R1).** BuildCapacityRow는 profile·SLO·선택한 dispatch occupancy·수치 정책과 모의 도착만 사용한다. 현재 Node snapshot/baseline/readiness/CPU inventory/예약/GPU inventory/memory 가용성은 입력이 아니며 Ready를 덮어 통과시키지 않는다. 장치·메모리 부재 중에도 κ는 계산·재사용하고 planner/prepare가 현재 상태로 실행을 거절한다. 복구 뒤 같은 key/κ로 다시 배치 판단한다. NonMonotone/수치 오류와 κ=0의 의미는 유지한다.
수치 key의 `shared-cpu-alpha2-path-budget/` namespace는 공유 CPU 재생·α/2 판단·TOTAL/POST 계산 계약을 나타낸다. 재생·κ·배칭의 계산 의미가 바뀔 때 namespace를 바꾸며 source SHA·빌드·출처만 바뀌면 유지한다. 다른 namespace의 원본 archive는 보존하고 캐시 miss로 처리한다. 이 cutover의 첫 배포는 한 번의 warm 계산이 필요하며, 이후 같은 계약·수치 입력은 재시작해도 재사용한다.

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

- 최종 충족: `κ_new,w ≥ λ_w`. 초과량 `λ_w / κ_new,w − 1`. λ가 0이어도 제어 대상 Workload마다 인스턴스 하나 이상이다. 아래 명시적 모델 불가 격리에는 현재 구성 보존 규칙을 적용한다.
- 대기 충족: `B_w`가 0이면 충족이다. 아니면 EDF가 밀린 요청을 먼저 처리하므로 가장 오래 기다리는 요청의 대기는 `W_w = B_w / κ_new,w`이고, `W_w ≤ L_w − m`이면 충족이다. 초과량 `W_w / (L_w − m) − 1`. keep(r = 0)도 지금 대기 `q_w`로 같은 판정을 받으므로, 측정 λ가 κ 안이어도 줄지 않는 대기는 확장을 부른다(m4 r3: λ 29.5, κ 34.3, 대기 160에서 keep의 W 4.66 s > 0.861 s). κ는 처리율 μ 이하라 이 대기는 실제보다 길다. 밀린 요청이 다 빠지는 시간 `B_w / (κ_new,w − λ_w)`도 기록한다.
- 순서: 제어 대상 모든 w에서 두 충족을 만족하는 구성 중 에너지가 작은 것, keep, 논리 구성 key 사전 순. 그런 구성이 없으면 상태는 `Overload`이고, 제어 대상 모든 w에서 최종 충족(가중치의 κ 검사 포함)을 만족하는 구성이 그렇지 않은 구성보다 앞선다. 같은 쪽 안에서는 Workload마다 최종·전환·route 가중치 초과량 중 큰 값을 구하고 내림차순 정렬한 목록을 사전식 최소화한다(leximin: 최악 동률이면 두 번째 최악, 이어 다음). 모든 값이 같을 때만 에너지, keep, 논리 key 순이다. 일반 용량 없음은 +∞ 그대로 비교한다. 기록의 `worstExceedance`는 목록 첫 값의 기존 요약이다. keep은 r = 0이다.
  제어 격리는 공통 Serving adapter에서만 수행한다. 모든 호환 option의 유효한 행이 `활성 예산 + 최소 관측 실행시간 > SLO`를 입증한 Workload만 현재 인스턴스 구성으로 고정하고 제어 대상 feasibility·초과량 비교에서 제외한다. 빈 후보·Pending·Rejected·일반 grid κ=0은 격리하지 않는다. 기존 `fixCapacityWorkloads`와 고정 구성 보호를 재사용하며 v1/v2 엔진의 protocol·Rank·에너지 식은 유지한다. 격리 중에도 전체 SLO 미충족과 고정 인스턴스의 물리 점유·공급·에너지를 기록하고, 다른 Workload 배치나 Node mode 변경으로 실행 조건을 깨뜨리지 않는다. 경로만의 초과와 경로+최소 service 초과를 구분하며 grid 실패를 `PathBudgetExceedsSLO`로 표시하지 않는다. 매 판단에서 근거를 재확인하고 양수 용량이 확인되면 정상 제어로 복귀한다. 근거가 사라지면 기존 미계산·결손 처리로 전환한다. 격리는 해당 Workload의 축소·이동 회복을 수행하지 않으며 현재 인스턴스가 없으면 없는 상태를 유지한다. 다른 판단·적격성·상위 순위·비교기간이 같을 때 κ=0 복제 추가만으로 더 큰 에너지 후보를 선호하지 않아야 한다. 이를 전체 클러스터 최소 구성 수렴 보장으로 확대하지 않는다.
- 근거: 마지막 E1 snapshot에 포아송 40 req/s를 넣으면 에너지만으로는 rasp5 q1 + q1-lut(route 20.2 s)가 나오고 재생에서 전환 중 SLO에서 진다. 위 규칙으로 그 구성은 `B = 5.7 × 20.2 = 115`, `W = 115 / 69.2 = 1.67 s > 0.82 s`(L 1 s, m 177 ms)로 탈락한다. genesis3 q2 추가(route 10.7 s)는 `W = 61 / 144 = 0.42 s`로 충족이고, 재생 계획기와 같은 노드와 option이다.

## 4. 노드 계획과 탐색

**노드 계획.** 노드마다 keep과 측정 option 변형 하나를 후보로 만들며 현재 snapshot의 readiness/측정·소유 scope, GPU inventory/중복과 memory 예산을 매번 검사한다. CPU 독점 mask·예약 quota·mode별 CPU inventory는 관문이 아니다. 노드마다 서빙 인스턴스는 하나이며 같은 노드의 κ를 단순 합하지 않는다(m4 r8 D-R8-1의 공존 간섭 근거는 과거 관측으로 보존한다). 더 큰 용량은 새 공유 실행 조건의 측정 option으로만 판단한다.

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


**비교 기간 — SCH-58.** `H = settings.comparisonPeriodNanos` 하나를 결정 시점부터 keep과 모든 후보에 고정한다(기존 정책 600s 유지). 후보별 `r + max(post)` 연장은 없다. 아래 T는 H다. 전체 사슬이 H를 넘더라도 기존처럼 SLO 증설 후보로 사용할 수 있다. `scheduler_capacity_nodes.go:105-109`의 `TransitionExceedsHorizon`은 노드 사슬에 대한 진단 표시일 뿐 제외 조건이 아니다. 전체 동작 비용 J는 전부 부과하고 H 밖의 대기·서빙 시간은 계산하지 않으며 음수 꼬리는 없다. H는 SLO 시간·용량표 창·동작 timeout과 별개이고 새 설정 revision은 다음 판단부터 적용한다.

**에너지.** keep의 노드 에너지는 `P_before × H`, 전환은 `J_pre + J_post + P_mid × max(0, min(r,H) − t_pre) + P_after × max(0,H − r − t_post)`(t는 평균 시간)다. 전환 operation J는 완결 비용이므로 사슬이 H를 넘어도 전부 포함한다. bridge의 시작 지연은 H 안에서만 이전 baseline으로 센다. 요청 에너지는 [0,min(r,H)]의 retained 처리와 overflow, [r,H]의 최종 정수 weight 처리로 나눈다. H 뒤 serving은 0이다. 보호수요는 실제 λ로 비례 축소하고 backlog drain을 요청 J에 더하지 않는다. 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**탐색.** `planSearchStrategy`는 `v1` 또는 `v2`(현재 기본)다. 계획 집합은 노드마다 계획 하나를 고르며, 두 탐색은 3장의 충족·최종 충족·초과량 벡터·keep 대비 에너지 차이(`joules - keepJoules`)·keep·논리 key 순서를 따른다. v2도 같은 고정 H와 keep 기준 전력을 사용하며 pending replica는 용량 없이 복제 수 상한에 포함한다. retirement 허가는 원래 계획을 읽는 serving adapter의 정확 `admits`가 판정한다.

v1은 keep과 모든 노드를 비운 집합(인스턴스가 없는 적격 계획, 없으면 keep)에서 시작한다. 각 출발점에서 노드 k개 이하를 바꾼 이웃 중 가장 좋은 집합으로 이동하며, 두 출발점의 k = 2를 마친 뒤 각각의 끝에서 k = 3을 이어 간다. 두 끝 중 순서가 앞선 집합이 해다. 평가는 정확한 κ 합과 위 에너지 식을 쓰며 route 시각은 집합에서 가장 긴 route 전 사슬이다. `maxReplicas`·`maxInstances` 초과(replicas OFF이면 Workload별 현재 수 변경), 새 인스턴스의 가중치 0, 보호 중인 분배 인스턴스의 가중치 제거는 허가하지 않는다. 걸음 없이 끝난 빈 출발점도 같은 허가를 거친다. 이웃은 `planSearchWorkers`개 goroutine이 나눠 평가하고 전순서로 합친다. 두 출발점에서도 닿지 않는 해는 놓칠 수 있으며, 고른 해는 14장의 교체 회수와 최소 이득을 거친다.

v2는 adapter가 만든 노드·option·모드별 계획표를 Tick마다 전처리하고 cap 변형을 κ·논리 key 순으로 묶는다. 양수 수요 Workload에 κ = 0인 인스턴스를 두는 적격 non-keep 계획은 `dominated: ZeroCapacity`로 탐색에서 뺀다. cap 하위 선택은 κ 최대와 최소 충족 cap을 남긴다. Overload라도 `B_w = 0`이면 충족 cap의 초과량이 0으로 같으므로 둘을 유지하고, `B_w > 0`인 Overload에서는 κ 최대만 남긴다([정정 근거 §7.8.2](capacity-table-evidence.md#782-계획표와-cap-하위-선택)). adapter의 증분 평가기는 바뀐 Workload 항·route·노드 에너지를 재계산하고 정확 평가와 같은 정수 가중치·클램프 식·연산 순서를 쓴다. 집합 단위의 timing·예비·bridge·retirement 허가는 정확 깔때기에 남긴다.

출발점은 keep·빈 집합과 선택적 가격 유도 집합(`priced`)이다. `planPricedStart` 기본값은 true이며, searchable cap 전체를 대상으로 가격 반복 100회 뒤 얻은 집합이 앞의 두 집합과 다를 때만 셋째 출발점으로 넣는다. 각 출발점에서 피복은 이미 충족한 Workload를 보존하며 미충족 용량을 채우고, 인스턴스 상한을 넘으면 수와 초과량을 줄인다. 연마는 k = 1·2 이웃의 최급강하를 반복한다. 더 좋은 이웃이 없으면 노드별 한 이동의 ΔJ/Δ피복 순으로 고른 활성 m개 cap 변형에 keep·현재 계획을 더해 분기한정 결합 탐색을 한다. 피복이 양수가 아닌 이동은 ΔJ·key 순으로 뒤에 둔다. `planPolishActive` 기본값은 8이며 0이면 모든 searchable 계획과 cap 변형을 활성화한다.

탐색 중 상위 K개(`planExactFinalists`, 기본 16)와 각 출발점의 끝점을 중복 없이 모아 정확 깔때기로 보낸다. 끝점은 K 밖에서도 보존하므로 최종 후보 수는 K를 넘을 수 있다. v2의 깔때기는 adapter port의 `bounded` → 정확 `evaluate` → `admits` 순서다. **v2 선택은 항상 공통 adapter의 정확 `evaluate`와 `admits`를 통과한다.** 정확 후보가 모두 거절되면 가지치기 전 keep과 적격 cap 전체의 정확 k = 1 탐색(`fallback: exactK1`), 그것도 선택하지 못하면 selector가 연결한 같은 마감의 v1 탐색(`fallback: v1`)으로 이어진다. v2는 v1 package를 직접 호출하지 않는다. `planPostFilterFinalists` 기본값 false에서는 정확 평가 1등을 고른 뒤 교체 회수·최소 이득을 적용한다. true에서는 후보별 adapter의 `payback` → `minimumGain` 뒤 결과를 비교하고 호출부에서 다시 적용하지 않는다. false에서도 후처리 진단은 계산하며, 원래 1등은 keep으로 되돌아가지만 살아남는 대안이 있으면 `postFilterReverts`를 기록한다.

v2의 `planSearchEvaluations` 기본값은 증분 평가 500,000회이며 `delta` 직전에 검사한다. 벽시계 마감은 v1과 같이 판단 시작 + `controlIntervalNanos/2`이고 계획 생성 시간도 포함한다. v2는 출발점 시작, 피복·연마의 반복/노드/이동 묶음 경계와 결합 탐색 재귀 진입에서 마감을 검사한다. 평가 수에 닿으면 `terminationReason: evaluationBudget`을 남기고, 벽시계 마감에 닿으면 `terminationReason: deadline`과 `budgetCut`을 남긴다. 두 경우 모두 그때까지의 후보·끝점을 정확 평가한다. 계획표 전처리·가격 반복·정확 깔때기·정확 k = 1 fallback은 이 증분 평가 수에 포함하지 않으며 그 안에서 마감을 검사하지 않는다. 따라서 벽시계 마감은 탐색 중단 기준이며 판단 반환의 엄격한 시간 상한은 아니다. v2는 단일 goroutine으로 UID·논리 key 순으로 순회하고, 고정 평가 예산과 같은 입력에서는 결정적이다. 벽시계로 잘리는 위치는 실행 환경에 따라 달라질 수 있다.

**router 가중치.** Routing 제어가 켜져 있으면 기존 weight를 보존하거나 e순으로 채우지 않고, λ·backlog와 무관한 minimax 정수 배분으로 `max(weight_i / κ_i)`를 최소화한다. `floor(1,000 × κ_i / Σκ)`에서 시작해 남은 단위를 `(weight_i + 1) / κ_i`가 가장 작은 인스턴스에 하나씩 준다(동률은 입력 index 순). κ가 0인 인스턴스는 받지 않고, Σκ가 0일 때만 같은 몫을 쓴다. λ가 0이거나 κ 합을 넘어도 배분은 같다. 최종 정수 weight로 모든 인스턴스에서 `λ_w × weight_i / 1,000 ≤ κ_i`를 한 번 검사하며, 넘치면 `RouteWeightExceedsCapacity`로 최종 미충족이다. λ에 따라 정수 단위를 옮기지 않으므로 안전한 정수 배분이 존재하면 이 배분도 안전하다. 요청 에너지의 y는 최종 정수 weight로 계산하며, keep의 가중치가 바뀌면 사슬 없이 route 동작 하나다. Routing 제어가 꺼져 있으면 `capacityFixedWeights`로 기존 weight를 유지한다.
근거: m4 r4에서 rasp5 q1을 κ 25.3까지 채웠을 때 1 s 이내 응답은 47%였다. full-E5-3m-12의 κ 10.191/26.498/15.976/36.859와 λ 73.8은 weight 114/296/178/412로 나눠 느린 장치만 κ까지 채우는 불균형을 없앤다.

`decision.capacity.workloads[].minimumDemand`는 해당 판단의 명시적 하한이고,
`rate`는 그 하한과 관측 수요 중 큰 값이다. 미설정 하한도 기록에서는 0으로 남긴다.

**판단 기록.** `ExpectedJoules`는 고른 해를 정확한 y로 다시 계산한 위 분석 합이다. `decision.capacity`는 T, Workload별 λ, `κ_new`, `κ_ret`, B, W, 두 초과량, 고른 r, 가중치, 사용한 용량표 행 키, 탐색 범위 `search`(v1의 마지막 이웃 크기 `neighborhood`, 정확 평가 수 `evaluated`, 탐색 시간 `durationNanos`, 예산 종료 `budgetCut`), 그리고 노드 계획마다 key, 적격 여부, 제외 사유(`MissingOperationCost`, `CapacityPending`, `CapacityRejected`, `InfeasibleResources`, `FixedWorkload`, `PlacementInventoryMissing`), κ, 사슬 시간, J를 담는다(`SCH-30`). 평가한 계획 집합의 목록은 기록하지 않는다.

`search`는 전략 `strategy`, 사유별 가지치기 수 `pruned`, 증분 평가 수 `surrogateEvaluated`, 깔때기로 보낸 후보 수 `finalists`, 정확 평가 수 `exactEvaluated`, 선택 후보의 증분 J − 정확 비교 J인 `surrogateGapJoules`(후처리 비교 ON이면 후처리 뒤 J와의 차), `postFilterReverts`, `fallback`, 출발점 `start`를 해당 경로에서 기록한다. `exactEvaluated`는 후처리 내부 재평가를 세지 않는다. `dualBound`는 가격 반복의 선형화 진단값이며 실제 목적함수의 하한이나 최적성 보증이 아니다. v1 fallback으로 넘어가면 선택해와 정확 탐색 통계는 v1 값으로 교체되고 `strategy: v2`, `fallback: v1`을 남긴다. 증분 걸음·평가 수는 원래 v2 기록을 유지하며 탐색 시간은 두 탐색을 포함한다.

`steps`는 출발점별 origin(`k: 0`, `evaluated: 0`)과 수락된 걸음만 기록한다. v2의 `phase`는 `cover`·`polish`·`combine`이며 origin과 v1에는 없다. v2의 `k`는 실제 바뀐 노드 수이고 `changedNodes`는 같은 출발점의 직전 선택과 비교한다. `evaluated`와 `durationNanos`는 수락한 피복 반복·연마 훑기·결합 탐색의 증분 평가 수와 시간이다. `joules`·`keepJoules`·`feasible`·`final`은 증분 평가 키의 값이며 깔때기의 정확 J와 다를 수 있다. origin의 초기 평가와 수락되지 않은 훑기는 `trailingEvaluated`에 남겨 `Σ steps.evaluated + trailingEvaluated = surrogateEvaluated`를 만족한다. 가격 반복은 증분 평가기를 호출하지 않으므로 이 합에서 제외한다. 수 상한 복구 중인 cover는 충족을 잃을 수 있지만 polish·combine은 엄격 개선만 수락한다. `terminationReason`은 `noImprovement` 또는 `budgetCut`이며 예산 종료는 마지막 기록 걸음에만 표시한다.

`key`와 `joules`는 선택한 공식 탐색의 후처리 전 선택해와 정확 J다. v2의 v1 fallback은 위 `strategy`·`fallback` 기록 규칙을 따른다.

노드 계획의 `dominated`는 적격 여부와 별도로 v2 가지치기 사유(`ZeroCapacity`)를 남긴다.

용량 행의 `CapacityJudgment.energyPerInputJoules`는 판정 재생의 요청 J/요청 수다. 모듈 B 준비용 기록이며 현재 계획기는 이 열을 쓰지 않고 profile의 b1 입력당 에너지를 쓴다.

`decision.search`의 `windowStart`, `windowEnd`, `recentWindowStart`는 두 구간을 나타낸다.
Workload별 `loads`의 `computeInputs`와 `inputsPerSecond`는 장기 관측값을 유지하고,
`recentComputeInputs`와 `recentInputsPerSecond`는 최근 구간의 계산 도착 수와 요청률이다.
`decision.capacity.workloads`의 `queued`와 `rate`로 미완료 기여를 포함한 최종 λ를 확인한다.
모든 후보는 같은 현재 유효 수요로 비교한다. 현재 유효 수요는 max(장기 관측률, 최근 관측률) + outstanding/실제 관측 길이에 minimumDemand 하한을 적용한다.

**비용.** v1 한 걸음의 평가 수는 노드 계획 수의 쌍곱(k = 2)과 세 쌍곱(k = 3)에 비례하고 요청 수와 무관하며, 평가 하나는 약 5 µs다([7장](capacity-table-evidence.md#7-계획-탐색)). v2 증분 평가 중앙값은 약 0.55 µs(555.7 ns, 할당 0)다. 기본 평가 예산 500,000회·m = 8 조건의 최대 하네스 공간 탐색은 0.42~0.54 s이고, 비용 게이트는 제안서의 ≤ 100 ms(0.2 µs 가정) 대신 v2 탐색 시간 ≤ `controlIntervalNanos`/10(샘플 10 s에서 1 s)이다. 같은 호스트의 v1 기준선 최대 공간은 3.0~3.5 s다. 평가 예산 150,000회는 시간을 반으로 줄이지만 노드 8 b1 모드 2 공간에서 한 판단 차를 2.715%까지 벌리므로 쓰지 않는다. 입력·가격 출발점·예산 조건과 품질 비교는 [§7.8](capacity-table-evidence.md#78-v2-계획기-측정-초안-단계별-게이트-수치)(origin/main 재측정은 §7.8.8)에 있다. 이 역사적 하네스 수치는 패키지 추출 뒤 성능이나 live 수용의 검증 기록이 아니다.

## 5. 완료 여유 규칙

Workload UID와 불변 `Definition.Identity()`별로 같은 관측 cutoff의 요청을 집계한다. 경로 경계는 [시간과 수명](scheduler-runtime-measurement.md#시간과-수명)을 따른다.

```text
E      = 서버 수신 → 응답 종료
Aw     = dispatched − accepted
F      = 실제 RequestContext.SenderElapsedMS의 원본 경과
I      = queueMS − 직접 관측한 (NativeRunningAt − NativeQueuedAt)
M      = E − Aw − nativeElapsed + I
M_post = E − F − nativeElapsed
```

M은 재생이 만드는 서버 slot 대기 Aw와 profile service+batch×d가 담당하는 actor 비용을 제외한 total 경로다. I는 decode·입력 검사·framing·actor admission을 포함한다. M_post는 이미 F·local elapsed를 차감하는 firstfloor의 외부 여유다. F 이전 비용을 M_post에 다시 넣지 않는다. 같은 worker의 시각 차이와 서버 monotonic 경과만 사용하며 host간 절대시각을 빼지 않는다.

**관측 상한.** 완료 시각 기준의 사용 가능한 요청 표본에서 M·M_post 각각의 순서통계 단측 95% 신뢰상한을 구한다. α/2 정책 수용 시 경로의 목표 분위수도 `q=1−α/2`다. 목표 분위수 q, 오름차순 표본 X_(k)에 대해 iid 연속 모집단에서는 `P(Q_q ≤ X_(k)) = P(Binomial(n,q) ≤ k−1)`이다. 이 값이 .95 이상인 최소 순위를 사용하며 `n_min = ceil(log(.05)/log(q))`에도 못 미치면 configured로 돌아간다. q=.975의 최소 표본은119개, q=.995는598개다. 각 상한의 95%는 동시 95%나 전체 Tick·운용기간 보장이 아니며, 신뢰오류 .05와 SLO 초과예산 α를 구분한다. p=100은 기존 별도 처리를 유지하고 유한 최댓값을 미래 절대 상한으로 부르지 않는다. 표본 수에 맞는 순위는 기존 통계 의존성을 재사용해 계산하며 관측 표본 정책과 replay의 `capacityTailSamples`를 혼동하지 않는다.

관측 0은 유효한 값이며 결손·역전·비유한 값·실패·취소·미완료와 구분한다. 직접 native queued/running 시각 없이 복원된 시각만 있는 행은 사용 불가로 분류하고 사유·분모에 남긴다. 수신 cohort의 실패·미완료와 완료 시각 표본을 구분한다. 미완료는 성공·완료 지연 표본이 아니며 확인 가능한 경과 하한이 임계치를 넘을 때만 그 임계치 초과로 센다. E 초과만으로 M 초과를 단정하지 않는다. 요청 단위 모집단을 유지하고 배치 최댓값을 독립 요청 표본으로 바꾸지 않는다. 같은 배치·공통 혼잡의 상관, 결손·완료 선택 편향과 대표성 한계를 기록하며 iid 전제가 뒷받침되지 않는 자료를 관측 검증 완료로 표시하지 않는다. 표본 부족을 해결하려고 창을 늘리거나 프로토콜 표본 조건을 낮추지 않는다.

**configured와 한계.** 기동·결손·표본 부족에서는 기존 `completionMarginMS`를 total·post의 configured 추정값으로 사용한다. 과거 값에서 pre/post 성분을 역산하지 않는다. configured에는 F 이전 비용과 native의 F 차감이 중복될 수 있다. 이 한계는 configured를 쓰는 모든 부하에 적용하며 배칭 축소·처리율 저하·backlog 영향을 별도로 검증한다. 관측 부족만으로 serving을 거절하지 않고 configured를 관측 검증 완료나 중복 제거 완료로 표시하지 않는다. 0.3 req/s·60초의 기대18개 표본에서는 configured가 유지된다. Workload 공유 예산은 후보 Node·routing·batch별 경로 차이를 완전히 설명하지 못한다.

**관측 적용과 계산 병렬성.** `observedPathBudgets`는 생략 시 true다. false이면 용량 입력과 신규 요청의 TOTAL·POST에 `completionMarginMS`를 적용한다. 관측 수집·raw 기록은 계속하며 기존 접수 요청의 동결 예산은 유지한다. OFF에서는 관측 변화만으로 행 key나 계산을 바꾸지 않는다. ON→OFF 전환은 기존 직렬 settings acceptance/Tick 경계에서 configured 정책을 적용하고 관측 활성 행이나 관측 전용 진행 계산을 유효 정책으로 유지하지 않는다.

`capacityWorkers=0` 또는 생략은 `max(1, GOMAXPROCS(0)/2)`, 양수는 지정한 용량 행 동시 계산 수다. 줄이면 실행 중 builder는 끝까지 진행하므로 active가 새 한도를 일시적으로 넘을 수 있고, 늘리면 빈 슬롯을 채운다. worker 변경은 수치 cache·결과를 버리거나 유효 계산을 취소하지 않고 과학적 행 key에 들어가지 않는다. CPU 예약·affinity가 아니며 전역 GOMAXPROCS는 변경하지 않는다. 자동값도 CPU 여유를 보장하지 않고 1 CPU에서는 최소 1개 계산이 필요하다.

기존 LoadWindow가 읽은 행을 재사용하며 추가 archive scan·ring·ticker를 만들지 않는다. 정렬·통계는 Runtime 전역 lock 밖에서 수행한다. 판단 기록에는 출처·창·coverage·활성 예산·행·최신 요구 차이와 반영 대기를 작은 요약과 기존 참조로 연결한다. 요청 표본·profile 전체를 복제하지 않는다. 선택 모델의 예상 지연은 기존 offline replay에서 구하고 실제 경로와 구분한다. 물리 service·preceding idle 비교는 `(instance,batchID)`별 canonical 값을 한 번만 사용한다. 온라인 차이는 batch별 service/G로 보고하며 자동 profile 거절·재측정으로 연결하지 않는다. 기존 ProfileReuseCheck의 다른 Node 확인 측정 계약은 유지한다.

### 구현 경계와 offline 비교

`request_path.go`는 시간 분해, `path_budget.go`는 관측 상한을 담당한다.
`capacity_table*.go`는 행의 계산·보관·전환을 맡고, `scheduler_path_budget.go`와
`runtime_path_budget.go`가 활성 입력을 공통 adapter와 접수 요청에 연결한다.
v1/v2 엔진의 protocol·순위·에너지 식은 유지한다.

```sh
go run ./hack/kappa -mode path -scheduler scheduler.json \
  -rows capacity.json -arrivals requests.jsonl
```

`-arrivals`는 assigned/final/native 원시 RequestRow JSONL 또는 gzip 파일이다.
선택 후보 재생과 실제 수신 cohort를 구분하며, 요청별 지연·동결 POST와
물리 batch별 service/G를 비교한다. 기존 CLI의 수치·Workload 선택 옵션을 사용한다.
같은 입력 비교에는 캡처 당시 설정을 지정한다. 결손·실패·미완료와 배치 상관은
진단에 남기며 이 명령은 profile 적격성을 바꾸지 않는다.

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
| 탐색 병렬 | `planSearchWorkers`≥1이다. sample은 3이며 v1·v1 fallback에만 적용한다. v2는 단일 goroutine이다. CPU 예약과 무관한 도구 병렬성이며 실제 경합·gateway/Tick 관측으로 평가한다 |
| 판단 기록 | `decision.capacity`는 구성의 판정, 에너지, 사슬과 `search`를 기록한다. `management`의 `capacity-row-built`는 행과 계산시간을 기록한다 |
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
- 검사 계약: 재생 결정성/NonMonotone/창·m·SLO 경계, 입력별 재계산/보관행 재사용/pending, 현재 memory·장치 부재→복구와 동일 κ 재사용, κ_new/κ_ret/B/W·make-before-break, 노드단일소유·전력모드, brute force/k3/빈 출발/replica/예산·동률, 동일H 에너지·signed 상쇄·문턱 equality/keep infeasible, weight1000·ACK·budgetCut을 확인한다. 이 목록은 새 source의 통과 기록이 아니다.
- 최종 dump의 source별 `hack/kappa`는 provenance·κ/status·시간·에너지 차를 남긴다. 승인 rev72 BEFORE/D2 AFTER는 자율MNV/native8 paired의 Tick/CPU/GC/mesh/요청/physical G를 비교한다. E1 무부하→유부하, E4 이동, E5-0~6/E5-S는 두 관측률·λ·보호κ·route시각·사슬·유효 offered 전체 결과·전환 포함 전체 POOL J를 연결한다.
- 최종 d/G/m에서 SLO/κ/R·threshold를 정한다. 과거 E1/m4와 새 evaluation는 Run ID/원시로 구분한다. 요청 에너지는 κ 비례(λ 무관)로 정한 최종 정수 weight와 일치하며, 모델 κ와 실제 장치 처리율·배치상태의 차이는 추정 한계로 유지한다.
- BEFORE·유한 진단 최대1회·최종D2 최대1회 순서는 [POOL 검증](workload-pools.md)을 따른다. runtime7 digest·기존 자원·evidence store를 유지하며 paired 물리 보존 실패는 unknown/mismatch로 보고하고 paired 주장을 중단한다. 조건·판정·범위 변경은 승인 대상이다.
- 승인/manifest/NodeUID/profile/runtime/source SHA·원시만 선별 보존하고 `.token`/Secret/전체 캐시 백업은 제외한다. 로컬 결정 원문은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/{d-measurement-decision.md,deployment2-e5-proposal-20261024.md}`다.

## 14. 구현 규칙

4장의 탐색 순회는 `internal/scheduler/v1`·`v2`가 소유하고, `internal/serving/scheduler_capacity.go`와 같은 package의 수치·실물 adapter는 다음 공통 규칙을 적용한다.

- **설정 gate.** placement/resources/batching/membership은 현재 Node/GPU 선택/배치 상한/대기 시간에 각각 적용한다. CPU 선택 gate는 없다. maxReplicas/maxInstances는 Workload별·POOL 전체 인스턴스 수 상한이며 replicas OFF이면 현재 수를 유지한다. routing OFF에서는 기존 인스턴스의 현재 가중치와 신규 option 가중치로 같은 κ 검사를 한다.
- **batching OFF의 고정 정책.** ON/OFF 모두 `CapacityVariants`로 측정된 cap 변형을 생성한 뒤 `controlAllows`가 Workload의 현재 `(MaxBatch, MaxWait)` 정책 집합에 속하는 변형만 허가한다. 여러 현재 정책을 임의의 최소/최대 하나로 축약하지 않는다. cap1도 측정·초기화된 변형일 때만 사용하며, 다른 제어가 켜져 있으면 고정 cap 안에서 placement/resources/replicas 후보 평가는 계속한다.
- **진행 중 전환.** 전역 계획기는 하나다. owner별 예약의 Node·Workload UID·Pod UID·physical ID와 frozen target만 고정하고 예약 밖 후보 평가·확장·준비된 용량 배분은 계속한다. 모든 후보의 κ가 미확인인 Workload는 기존 배분·미완료 요청을 보호하며 해당 범위만 고정한다. 실패·효과 소실은 해당 owner DAG에 기록하며 다른 owner의 목표를 버리지 않는다. 실패한 준비는 관측 창 동안 `PreparationFailed`로 제외한다.
- **인스턴스 관측 실패.** 한 인스턴스의 Ready 미확인은 Workload를 `Fixed`로 바꾸지 않는다. 해당 실물은 자원 점유와 소유권을 유지하되 κ_new·κ_ret·목표 배분에서 제외하고 나머지 인스턴스와 독립 Node의 자동제어를 계속한다. routing OFF도 미확인 실물을 서빙 용량으로 세지 않는다. `decision.reasons`는 `UnconfirmedInstance:<physical ID>:<관측 사유>`를 기록한다. Observer는 Pod 변경 유무와 무관하게 다음 refresh에서 native probe와 exact executor 검증을 다시 수행하며 기존 refresh 간격·probe 기한을 유지한다.
- **동작 기한.** prepare는 cost의 측정 trial 최대 시간에 관측 창 하나를 더한 시간 안에 실행된다. 초과하면 `failed`/`ActionDeadlineExceeded`로 실행을 끝내되 원격 효과 미확인 예약은 유지한다. 취소도 같은 계약이다. 이후 효과 확인 또는 reset의 실제 정리 proof 뒤에만 예약을 반환하고 현재 실제 상태에서 재계획한다. 독립 범위의 다른 동작과 판단은 계속한다.
- **가중치 척도.** 지금 가중치의 합이 1,000이 아니면 첫 판단이 route 동작 하나로 1,000 척도의 가중치를 적용한다.
- **제외 사유.** 4장의 사유에 더해 기준 전력이 없으면 `MissingUnloadedNodeBaseline:<노드>:<모드>`, 측정 POOL 노드는 `MeasurementNode`, 준비되지 않은 노드는 `NodeUnavailable`, 비서빙 예산 노드는 `PlacementBudgetUnavailable`을 기록한다.
- **재사용 증거.** 분배되지 않은 준비된 인스턴스는 예약 slot(`targetID`)이나 그 Workload의 소유가 있을 때만 계획이 다시 쓴다.
- **무한 값.** 용량이 없어 대기, 비우는 시간, 초과량이 정해지지 않으면 기록에서 그 필드가 빠지고 후보의 `worstExceedance`는 float 최댓값이다.
- **탐색 비용.** v1 평가는 노드 8개에서 약 5 µs이고 이웃 크기는 노드 계획 수의 쌍곱(k = 2)과 세 쌍곱(k = 3)이다. v2 증분 평가는 약 0.55 µs이며 기본 예산은 500,000회다. m = 8의 최대 하네스 공간은 0.42~0.54 s이고 비용 게이트는 `controlIntervalNanos`/10이다([§7.8](capacity-table-evidence.md#78-v2-계획기-측정-초안-단계별-게이트-수치)). 두 경로의 벽시계 마감은 판단 시작부터 제어 주기의 절반이며 노드 계획 생성도 포함한다. v2의 증분 평가 수는 `delta` 직전에, 마감은 탐색 루프 경계에서 검사한다. 전처리·가격 반복·정확 깔때기·정확 k = 1 fallback 내부에는 같은 마감 검사가 없어 전체 판단 시간의 엄격한 상한으로 해석하지 않는다.
- **Retirement — v2.1.** Idle time is not permission to mutate a selected target.
  Retirement is an enumerated node plan: `schedulerPlan` supplies its measured
  stop/drain/remove chain before the normal capacity, transition, reserve and
  H-energy evaluation. `decide` materializes exactly the evaluated instances.
  A plan removing live occupancy is admitted only when every controlled Workload
  meets the same final/backlog SLO judgment as Keep. When Keep is feasible,
  optional retirement must also exceed the existing common-H gain uncertainty
  bound. When Keep is infeasible and the whole target is feasible, the existing
  higher-energy recovery exception applies, including a replacement whose
  source removal and destination prepare are on different nodes. A node-local
  removal is not by itself proof of optional shrinkage. Overload ranking cannot trade unrelated healthy
  retirement for cheaper energy. Feasible, net-positive retirement remains
  possible with an Unconfirmed sibling: physical confirmation is node-local.
  Disabled ownership, partial observation, hold, bridge and reserve protections apply.
  `unroutedSince` remains captured controller history, not an energy exemption.
  Terminated-only occupancy reclamation is not serving retirement: its κ is
  already zero, and the existing physical stop/drain/remove obligations remain.
- **준비한 인스턴스의 분배.** 해가 준비하는 새 인스턴스는 모두 그 해의 가중치를 받는다. 새 인스턴스 하나라도 가중치가 0인 해는 후보에서 빠지고 교체 회수의 되돌린 해도 같다. 근거: m4 r7에서 rasp5 q1-lut와 q2(κ 25.69 + 29.30)가 지금 가중치 467/533으로 λ 40.35를 받았고, 적재한 genesis3 q2의 노드 전력(49.80 W)이 빈 노드(49.97 W)보다 작아 그 준비가 T 동안 1.5 J 싸게 나왔다. 그 인스턴스는 가중치 0으로 준비되고 약 80 s 뒤 유휴 제거로 지워졌고, 다음 판단이 다시 준비해 2 req/s와 40 req/s에서 약 100 s 주기로 되풀이했다.
- **만든 뒤 끊기.** 모드를 바꾸지 않는 노드 계획에서 분배된 인스턴스(어느 Workload에서든 가중치가 0보다 큰 인스턴스)의 stop, drain, remove는 그 노드 계획의 route 뒤에만 온다. 자원이 겹쳐 prepare 전에 지워야 하는 분배된 인스턴스가 있는 같은 모드의 교체는 `RoutedInstanceStopsBeforeRoute`로 빠진다. 그래서 그 교체 동안 분배된 κ는 지금 값에서 줄지 않고, route 뒤에는 최종 판정을 지나는 `κ_new`가 된다. 그 노드의 교체는 다른 해가 부하를 옮겨 그 인스턴스가 분배에서 빠진 뒤(유휴 제거) 열린다. 모드 변경은 power 동작 전에 노드의 인스턴스를 모두 멈추고 다른 길이 없으므로(노드 하나가 서빙하는 Workload의 모드 올리기) 후보로 남고, 멈춘 인스턴스는 `κ_ret`에서 빠지며 사슬 시간은 r에 들어가 3장 대기 판정을 받는다. 근거: m4 r7 07:08:31에서 genesis3 q2(κ 70.94, 가중치 532)를 같은 CPU의 q1으로 바꾸는 계획이 q2 stop을 첫 동작으로 두었고, 전환 판정은 rasp5 q1 하나(κ_ret 25.69)로 λ 38.6을 받았다. rasp5 prepare가 68 s 걸리는 동안 SLO는 0%로 떨어졌다. 07:00:38의 rasp5 q1 교체와 06:51:28의 genesis2 교체도 같은 순서였다.
- **교체 회수.** 지금 인스턴스를 지우고 다른 인스턴스를 들이거나 배치 policy를 바꾸는 노드 계획(교체)은, 그 노드만 keep으로 되돌린 해도 충족하면 관측 창의 최소 부하에서 T 동안의 에너지로 비교한다. 최소 부하는 관측 창을 제어 주기로 나눈 구간마다의 도착률 중 가장 작은 값이다. 교체가 그 부하에서 keep보다 에너지가 크거나 같으면 keep으로 되돌리고 `ReplacementUnpaid:<노드>`를 남긴다.
- **최소 이득.** 고른 해가 충족하고 노드 하나라도 바꾸며 keep도 충족하면, T 동안 nominal 이득(keep−해)이 선택된 operation과 baseline의 조건부 B 민감도 문턱보다 클 때 해를 쓴다. 같은 immutable reference/trial의 signed 노출량을 먼저 합쳐 공유 근거를 상쇄하고 남은 절댓값을 합산한다. trial 평균은 B/N을 선형 합산하며 baseline watts 민감도는 실제 초를 곱한다. 이득이 문턱과 같거나 작으면 keep과 `TransitionGainWithinUncertainty`를 기록한다. keep infeasible이면 용량/SLO 회복을 유지한다. 문턱은 profile 통계오차와 BEO idle fallback CI 및 시점/센서/모델 오차를 포괄하지 않으며 global95% gain 구간을 뜻하지 않는다. 원시 profile 분산/CI, d의 profile 포함95% 분석과 κ/SLO 계산은 각 추정 계약을 유지한다.
- **부분 관측 창.** ChillServer가 시작한 뒤 관측 창 하나를 다 관측할 때까지 계획은 인스턴스를 더하기만 한다. 지금 인스턴스를 지우거나(그 노드 계획에 남지 않는 인스턴스) 배치 policy를 바꾸는 노드 계획은 `PartialObservationWindow`로 빠진다. 계획은 지금 인스턴스를 노드의 지금 모드에서만 다시 쓰므로, 인스턴스가 있는 노드의 모드 변경은 그 인스턴스를 지우는 계획으로 같이 빠지고 빈 노드의 모드 변경은 확장으로 남는다. 분배된 인스턴스의 가중치를 0으로 만드는 해는 후보에서 빠지며, keep의 가중치가 분배된 인스턴스를 빼면 지금 가중치를 그대로 둔다. 유휴 제거도 쉰다. 근거: m4 r7 07:00:38에서 재시작 19 s 뒤의 관측한 창은 λ 20.6이었다. 부하는 40 req/s였지만 클라이언트가 중단 동안 물러나 있어 ingress가 9.0 req/s(07:00:00~20)와 23.4 req/s(07:00:20~40)만 받았다. 계획은 그 λ로 분배된 rasp5 q1 하나를 지웠다.
- **계획에서 뺀 노드.** `settings.excludedNodes`(Node 이름 목록, sample은 빈 목록)의 노드는 측정 POOL 노드와 같은 고정 경로를 탄다. capture가 노드 사유 `ExcludedNode`를 붙이고, 그 노드는 keep뿐이며(배치, 교체, 지우기, 전력 변경 없음) 가용 노드 최저 모드 낮춤과 유휴 제거도 받지 않는다. 판단마다 `decision.capacity.plans`에 그 노드의 `ExcludedNode` 계획이 남고 snapshot 노드 `reasons`에도 남는다. 측정 POOL 노드의 인스턴스도 유휴 제거를 받지 않는다.

## 15. 현재 수요 후보 비교와 고정 H — SCH-55~58

시간 기반 축소·교체 허가와 관측 TTL·bootstrap 대기 상한을 제거한 현행 계약이다.
이전 두 박자 설계의 근거는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/`의
`intent/two-tempo-questions-20261026.md`와 `intent/checkpoints.md`에 보존한다.

### 현재 수요에 따른 후보 비교

- 모든 Tick에서 분배·증설·축소·교체 후보를 같은 물리 규칙으로 비교한다. v1/v2 모두 실제 용량·대기·SLO·backlog·회복 검사를 유지한다.
- 유입 증가만으로 축소·교체를 금지하지 않는다. 현재 수요를 감당하는 후보 사이에서 기존 leximin·에너지·minimumGain·교체 회수 순서를 유지한다.
- 현재 유효 수요는 max(장기 관측률, 최근 관측률) + outstanding/실제 관측 길이에 minimumDemand 하한을 적용한다. reductionDemand도 이 현재 수요다. 별도 이력 최대값이나 지속 시간 조건은 없다.
- 최종 에너지 하강 route와 retirement 후보는 현재 유효 수요×(1+h)를 양수 정수 weight/각 κ로 검사한다. bridge/reserve·실제 요청 J는 할증하지 않는다.
- 실제 부족 회복, 부분 관측의 실제 가용 수요, unknown·예약·소유권·Ready/native ACK·old+new peak·bridge·reserve·H 검사를 유지한다. 중단형 power 변경은 handoff가 아니다.
- Terminated는 κ=0이며 allocation 반환 증거가 아니다. same-Node stop/drain/remove 의존성을 유지한다.
- 실행 중 owner 예약과 frozen target을 보존한다. 이미 허가된 DAG의 후속 drain/remove는 계속하며 중간 배분은 실제 Ready/ACK·stop 제외·원자적 UID별 route 계약을 따른다.
- 빈 Node의 새 Fixed 절전도 같은 현재 수요·SLO와 gain/minimumGain/payback으로 비교한다. 별도 시간 허가나 이력 상태는 없다.
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
evaluation 실측은 실제 전환 전체와 전체 POOL(예비 노드 포함)을 집계한다. 예비 비율만으로
실제 SLO를 보장했다고 주장하지 않고 AC-43에서 요청 결과로 확인한다.

### 기록과 설정 고정

strict scheduler 설정 decode/검증·설정 API·ConfigMap·Dashboard·sample·Run 캡처는
7장의 같은 계약으로 함께 이행한다. `comparisonPeriodNanos`의 기존 단일 필드를
재사용하고 단일 허용값 enum/하드코딩된 선택지로 제한하지 않는다.
`decision.settings`와 `decision.search.bounds`에 세 설정값을 고정하고,
`decision.capacity.periodNanos`는 사용한 H와 일치시킨다.
결정에는 현재 rate·backlog·가용 κ와 후보의 제외 사유·에너지·gain·minimumGain/payback을 기록한다.
과거 raw archive와 게시 증거는 보존한다. 이전 최대수요 창의 게시 근거는
`O/campaign/v2/dashboard-acceptance-20261030/approved-publication-inputs.json`과 단계별 index에 있다
(`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`).
예비 평가는 Workload별 수요·필요 κ·구간별 가용 κ·부족 이유를 같은 결정에 연결한다.
Run 시작의 `binding.schedulerSettings`, 설정 캡처와 summary는 같은 값을 보존한다.

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
대상 d가 없으면 설정 fallback으로 κ를 계산하며, 정확 비용이 없으면 기존 MissingEvidence를 유지한다.
프로파일 공유가 d·prepare/remove·mode cost의 암묵적 공유나 κ 적격을 뜻하지 않는다.

