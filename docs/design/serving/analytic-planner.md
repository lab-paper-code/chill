# 용량표 계획기 (M4)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M4 설계다. 계획기는 두 부분이다. 첫째, option과 Workload SLO마다 인스턴스 하나가 SLO를 지키는 최대 요청률 κ를 기존 재생으로 미리 구한 용량표다. 프로파일, SLO, 완료 여유, 용량표 설정이 바뀔 때만 다시 계산한다. 둘째, Tick은 재생 없이 κ 합, 노드 계획, route 시각별 DP로 구성을 고른다. 근거와 시제품은 [인스턴스 용량표 근거](capacity-table-evidence.md)와 `hack/kappa`다. 사용자가 10장의 명세 변경 묶음(①, ①', ④, ⑤)를 승인하면 전환한다. 명세 변경 ②와 ③은 이미 반영되어 있다. [M0](decision-record-diet.md)와 [M1](observability-split.md)을 전제로 한다.

## 1. 측정한 문제

| 문제 | 근거 |
|---|---|
| Tick이 부하에 비례해 500m CPU limit을 채운다 | `replays` 10, `maxCandidates` 32에서 Tick p50이 0.633 req/s 913 ms, 2 req/s 3,475 ms, 5 req/s 7,495 ms, 최대 22,575 ms, CPU 499m(D-16). 기본값 100과 128에서는 Tick 133회가 30 s deadline으로 실패했다(D-1). 판단마다 후보 20개를 10회씩 재생하고, 재생은 사건마다 모든 요청을 훑고(`nextEvent`) 배치마다 O(대기 요청² × 배치 행)인 `firstfloor.ChooseBatch`를 부른다 |
| 재진입 후보에 닿지 못한다(D-22) | 인스턴스가 생긴 뒤 판단 128건 중 127건이 32에서 잘렸고 재진입 후보는 37~48번째였다. 깊이 우선 열거가 노드 UID 순이라 앞 노드의 option과 route 변형이 상한을 쓴다 |
| 재진입 사슬이 horizon을 넘는다 | 빈 노드 전환은 jetsonx 63.5 s, jetsono 134.5 s, orin-nano 197.1 s(settling 60 s 포함, D-20, D-21)이고, horizon 60 s를 넘는 후보는 `TransitionExceedsHorizon`이다. 사용자는 horizon 60 s를 유지하고 M4에서 푸는 것을 골랐다 |
| 지연 공식은 상한이 되지 못한다 | 배치 분위수를 더한 지연 상한(여유율 0.8)에 대해 외부 검토의 포아송 재생에서 실제 p95가 그 값의 1.8~3.6배였다. κ는 공식 대신 같은 재생으로 구한다 |
| 지킬 것 | 에너지 예측 E1 1시간 499,333 J 대 실측 495,741 J(+0.7%), 결정성(정렬된 입력, 고정 seed, 완전 동률은 keep) |

## 2. 용량표

**행.** 키는 option(노드, 모드, 할당, profile ID), 배치 상한 b, SLO(백분위 p, 목표 L), 완료 여유 m, 용량표 설정, `capacityVersion`이다. `capacityVersion`은 코드 상수이고 κ 계산, 재생, firstfloor 배칭 규칙이 κ를 바꾸면 올린다. 9장의 고정 κ test가 그 변경을 잡는다. deadlinePolicy는 profile이 정한다. 행 본문은 결정적인 값만 담는다: 입력, κ, μ(b 이하 배치의 `b / 평균 실행시간` 최댓값), 격자 판정 결과, 창별 백분위 지연. 계산 시각과 걸린 시간은 행 대신 `management` stream의 `capacity-row-built` 사건(행 키 포함)에 남긴다.

**판정.** option 인스턴스 하나만 있는 keep snapshot에 요청률 λ의 포아송 도착을 넣고 `EvaluateScheduler`(반복 1회, 자동제어 꺼짐)의 SLO 추정을 읽는다. 백분위 지연(m 포함) ≤ L이고 timeout과 만료가 0이면 그 창은 충족이다. 창 하나는 빈 상태에서 시작하고 요청 수는 `max(capacityMinimumRequests, ⌈100/(1 − p)⌉, ⌈λ × capacityWindow⌉)`다. 100은 백분위 위에 표본 100개를 두는 고정 규칙이다. seed 1~`capacityWindows`의 독립 창 중 `capacityPassingWindows`개 이상이 충족하면 λ는 충족이다. seed의 도착 간격을 λ로 나눠 쓰므로(공통 난수) 판정은 결정적이다.

**κ.** 창 요청 수와 부하에 따른 배칭이 λ와 함께 바뀌므로 판정이 λ에 단조라는 보장이 없다. 그래서 격자 `λ_k = 0.05kμ`(k = 1~20)를 모두 판정하고, 충족 앞부분과 미충족 뒷부분으로 나뉠 때만 κ를 정한다. 마지막 충족 `λ_K`와 다음 격자 사이를 0.5% μ까지 이분 탐색한 값이 κ다. K = 0이면 κ 0, K = 20이면 κ = μ다. 미충족 뒤에 충족이 오면 행은 `NonMonotone`이고 κ가 없으며 그 option은 `CapacityRejected`로 계획에서 빠진다. 격자 결과는 행에 남아 조사에 쓴다. 격자 간격과 이분 탐색 폭은 `capacityVersion`에 속하는 상수다.

**설정.** `capacityWindow` 60 s, `capacityWindows` 20, `capacityPassingWindows` 19, `capacityMinimumRequests` 1,000. 60 s 창이 필요한 근거: 요청 수 창만 쓰면 빠른 option(genesis2 cuda, 2,000 요청이 14 s)의 κ가 μ에 붙어 relaxed 1.2κ에서 20/20 충족이었고, 60 s 창에서는 0/20이다. 20개 중 19개 기준이 필요한 근거: 창 하나로 정한 κ는 1.0κ에서 6~12/20만 충족했다.

**검증된 모양.** 이분 탐색에 쓰지 않은 seed 21~40에서 0.9κ는 모든 option이 20/20, 1.0κ는 16~20/20, 1.2κ는 0/20이다. κ는 절벽의 무릎이다. E1 tight(p95 1 s, m 139 ms) κ: rasp5 q1 34.3, jetsonx 18.3, orangepi 8.1, jetsono 48.1~49.7, orin-nano 38.9~41.3 req/s.

**만드는 곳과 비용.**
- ChillServer의 용량표 worker가 Workload의 SLO가 정해지거나 profile, m, 용량표 설정, `capacityVersion`이 바뀔 때 빠진 행을 만든다. 자동제어를 켜기 전에 만들어지므로 켤 때 기다리지 않는다. 행 하나가 작업 하나이고 `GOMAXPROCS`개를 병렬로 돌린다.
- 행은 evidence store에 `serving/capacity/<capacityVersion>/<profileID>/<b>/<p>-<L>-<m>/<설정>.json`으로 `PutImmutable`하고 재시작 뒤 읽는다. 본문이 결정적이라 다시 올려도 `Existing`이다.
- 행이 없으면 그 option은 `CapacityPending`으로 계획에서 빠지고, 켜진 Workload에 κ가 있는 option이 하나도 없으면 판단이 `CapacityPending`이다.
- 지금 재생은 요청 수의 제곱으로 느려진다(창 2,000 요청 0.19 s, 8,000 요청 2.8 s). 60 s 창은 빠른 option에서 8,700 요청이고 option 하나의 검증에 2,135 s가 들었다. 그래서 commit 1이 재생의 사건 선택을 heap으로 바꾼다. 격자는 이분 탐색보다 판정 횟수가 많지만 미충족 판정은 실패 창 2개에서 멈춘다 `[추론: 행당 이분 탐색의 약 2.5배]`. 행 수는 E1에서 option 변형 38개 × SLO 3개 = 114개다.

**imagenet.** imagenet(p99 100 ms)은 114행 모두 κ 0이다(m 139 ms > 목표 100 ms). 이 Workload는 꺼진 채로 둔다.

## 3. 판정과 순서

구성 c와 route 시각 r에서 켜진 Workload w마다 아래 값을 정확한 κ 합으로 계산한다.

| 값 | 정의 |
|---|---|
| `λ_w` | (관측 창의 계산 도착 수 + 대기 수) / 창 길이. 도착은 재생이 쓰던 `Compute` 또는 `Accepted` 도착 |
| `κ_new,w` | c의 w 인스턴스 κ 합 |
| `κ_ret,w` | route 전까지 남는 지금 인스턴스의 κ 합. 모드를 바꾸는 노드와 자원이 겹쳐 prepare 전에 지우는 인스턴스는 빠진다 |
| `r` | route 전 사슬의 최대 시간(동작마다 측정 trial 최대 시간) |
| `B_w` | `q_w + max(0, λ_w − κ_ret,w) × r`. `q_w`는 지금 대기 수 |

- 최종 충족: `κ_new,w ≥ λ_w`. 초과량 `λ_w / κ_new,w − 1`. λ가 0이어도 켜진 Workload마다 인스턴스 하나 이상이다.
- 전환 중 충족: `λ_w ≤ κ_ret,w`이면 충족이다(남은 인스턴스가 κ 안에서 처리). 아니면 route 뒤 EDF가 밀린 요청을 먼저 처리하므로 가장 오래 기다리는 요청의 대기는 `W_w = B_w / κ_new,w`이고, `W_w ≤ L_w − m`이면 충족이다. 초과량 `W_w / (L_w − m) − 1`. κ는 처리율 μ 이하라 이 대기는 실제보다 길다. 밀린 요청이 다 빠지는 시간 `B_w / (κ_new,w − λ_w)`도 기록한다.
- 순서: 모든 w에서 두 충족을 만족하는 구성 중 에너지가 작은 것, keep, 논리 구성 key 사전 순. 그런 구성이 없으면 w와 두 초과량에 걸친 최대 초과량이 작은 것, 에너지 순이고 상태는 `Overload`다. keep은 r = 0이다.
- 근거: 마지막 E1 snapshot에 포아송 40 req/s를 넣으면 에너지만으로는 rasp5 q1 + q1-lut(route 20.2 s)가 나오고 재생에서 전환 중 SLO에서 진다. 위 규칙으로 그 구성은 `B = 5.7 × 20.2 = 115`, `W = 115 / 69.2 = 1.67 s > 0.82 s`(L 1 s, m 177 ms)로 탈락한다. genesis3 q2 추가(route 10.7 s)는 `W = 61 / 144 = 0.42 s`로 충족이고, 재생 계획기와 같은 노드와 option이다.

## 4. 노드 계획과 DP

**노드 계획.** 노드마다 keep, 그리고 배치 가능한 노드에서 측정 option 변형(option마다 변형 하나 이하) 집합 중 한 모드와 `ValidateNodeBudget`을 만족하는 것. 같은 노드에서 같은 option 두 개는 CPU affinity가 겹쳐 생기지 않는다. 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이다. 계획마다 Workload별 `κ_new`와 `κ_ret`, 사슬, 전력을 둔다.

**인스턴스와 Workload.** 인스턴스는 Workload 하나에 속하고 κ는 그 Workload와 option의 행이다. 같은 모델의 Workload들은 한 노드를 함께 쓸 수 있지만 각자 인스턴스를 둔다. 계획기가 만드는 분배에서 인스턴스마다 Workload는 하나다. capture는 실제 분배에서 한 인스턴스가 두 Workload 이상에 들어 있으면 판단을 `SharedInstance`로 대기시킨다(6장의 운영 단계가 전환 전에 푼다). 꺼진 Workload의 인스턴스가 있는 노드는 keep뿐이므로 꺼진 Workload의 SLO는 계획 동작의 영향을 받지 않는다(workload-pools 4.4절의 노드 동작 규칙).

**사슬.** `schedulerPlan`의 순서다. 모드 변경은 power, 모든 기존 remove, prepare가 route 전이다. 같은 모드에서는 자원이 함께 들어가지 않을 때만 remove가 prepare 앞이고, 아니면 route 뒤다. 동작마다 측정 trial의 평균 wall J, 평균 시간, 최대 시간을 쓴다. 비용이 없으면 그 계획은 `MissingOperationCost`로 빠진다. 같은 인스턴스의 배치 상한을 바꾸는 계획은 remove와 prepare로 센다(교체).

**비교 기간.** `T = max(horizon, 공간의 최장 route 전 사슬 + 최장 route 뒤 remove)`(최대 시간)를 판단마다 하나 정하고 keep과 모든 계획에 쓴다. 모든 사슬이 T 안에 끝난다. 비교 기간을 정하는 방법은 04 명세가 설계에 맡긴다.

**에너지.** 노드 계획 하나의 [0, T] 노드 에너지는 keep이면 `P_before × T`, 아니면 `J_pre + P_mid × (r − t_pre) + J_post + P_after × (T − r − t_post)`(t는 평균 시간)다. P는 `schedulerBaseline`의 적재 상태 전력이다. 요청 에너지는 route에서 나눈다: [0, r]은 지금 routing으로 남는 인스턴스가 κ_ret까지 처리하고(`Σ y⁰_i e_i r`), 넘친 `max(0, λ − κ_ret) × r`건과 [r, T]는 target 배정 y로 처리한다(`Σ y_i e_i (T − r)`와 넘친 건수 × y 가중 평균 e). 비교 집합(`ComparedNodes`) 밖의 노드는 소거한다.

**DP.** 서로 다른 r마다 노드 순서 DP를 한 번 돈다. 칸은 (Workload별 배정 부하 칸, r에서 끝나는 사슬이 있는지)이고, 칸마다 (에너지, Workload별 정확한 `κ_new` 합, `κ_ret` 합)의 Pareto 집합을 둔다(에너지는 작을수록, 용량은 클수록 낫다). 부하 칸은 요청 에너지 배정에만 쓰고(계획은 `⌈κ/칸⌉`칸까지 받는다), 충족은 마지막에 Pareto 점의 정확한 합으로 3장 규칙을 적용한다. 그래서 칸 크기는 충족을 바꾸지 않고 에너지 배정의 정밀도만 바꾼다. 칸 수는 Workload 수에 따라 칸 상태가 20,000 이하인 가장 큰 정수(최대 50)다.

**router 가중치.** 고른 해의 인스턴스 κ로 e가 작은 인스턴스부터 κ까지 λ를 채운 y를, 합 1,000의 정수로 최대 나머지 반올림한다. 반올림 뒤 모든 인스턴스에서 `λ_w × weight / 1,000 ≤ κ_i`를 확인하고, 넘치면 그 인스턴스의 한 단위를 여유가 가장 큰 인스턴스로 옮긴다. 옮길 곳이 없으면 그 해는 `RouteWeightExceedsCapacity`로 최종 미충족이다. 배정 0인 인스턴스는 가중치 0이고, λ가 0이면 같은 가중치다. keep도 같은 규칙으로 가중치를 다시 정하고 route만 바뀌면 사슬 없이 route 동작 하나다.

**판단 기록.** `ExpectedJoules`는 고른 해를 정확한 y로 다시 계산한 위 분석 합이다. `decision.capacity`는 T, Workload별 λ, `κ_new`, `κ_ret`, B, W, 두 초과량, 고른 r, 가중치, 사용한 용량표 행 키, 그리고 노드 계획마다 key, 적격 여부, 제외 사유(`MissingOperationCost`, `CapacityPending`, `CapacityRejected`, `InfeasibleResources`, `FixedWorkload`, `PlacementInventoryMissing`), κ, 사슬 시간, J를 담는다(`SCH-30`). 계획의 조합은 기록하지 않는다.

**비용.** 노드와 계획 수에 비례하고 요청 수와 무관하다. 시제품(칸 DP)은 E1 기록 판단 498건에서 노드 계획 37개, Tick p50 1.84 ms, p99 3.02 ms(단일 코어)였다. Pareto 집합 크기는 commit 6 test가 기록한다.

## 5. 완료 여유 규칙

사용자가 채택했다. m은 요청 행의 `elapsedNanos − (queueMS + serviceMS)`(firstfloor 큐와 실행 밖 시간)의 Workload SLO 백분위로 정한다. E1 2 req/s에서 rasp5 실측 종단 p95 209.7 ms, 재생 예측(m 139 ms) 174.2 ms였고, 차이는 검증과 전처리(p95 103.2 ms)와 응답 교환이 m보다 길어서다. E1 값은 약 177 ms다. `hack/observations/e1.sql`이 이 열을 내고 운영자가 E 실행 결과로 `completionMarginMS`를 바꾼다. m이 바뀌면 2장의 행이 다시 만들어진다. κ는 m에 둔감하지만(rasp5 tight m 139 ms 34.3, m 179 ms 34.2) 3장의 `L − m`과 짧은 SLO의 κ는 m을 그대로 쓴다.

## 6. 인스턴스 공유 정리

같은 모델 인스턴스 공유는 연구 범위 밖이다. M4는 계획기와 그 저장 모델에 부담이 되는 부분만 지우고, 나머지는 M5 후보로 둔다. firstfloor, serving-worker, runtime image는 바꾸지 않는다(Workload와 정의별 큐는 그대로).

**M4에서 지우는 것(commit 6).** `controls.sharing` 설정. 이전 열거기의 공유 cohort 보존과 기존 인스턴스에 UID를 붙이는 후보 경로는 새 계획기로 옮기지 않는다(열거기와 함께 지워진다). 판단 snapshot과 기록의 `SchedulerInstance.WorkloadUIDs`는 `WorkloadUID` 하나가 되고, Tick에서 쓰지 않는 UID별 `arrivalAgesNanos`가 빠진다. 꺼진 Workload를 binding 변경만으로 SLO 판정에 넣는 경로(`schedulerSLOTargets`)를 지우고 노드 동작 규칙은 둔다. `StaticRoutingTarget`은 최종 분배에서 한 인스턴스가 두 UID에 들어가는 설정을 거절한다. `SchedulerOption.CompatibleUIDs`(프로파일 적용 조건)는 그대로다.

**전환 전 운영 단계.** testbed에는 공유 인스턴스가 있다. rasp5 인스턴스(Pod `serving-d42ad89cca2909bb6b02`)의 `edge.dacs.io/serving-routing`이 relaxed(`ae87…`), tight(`c210…`), imagenet(`e06b…`)를 모두 가리키고 세 Workload는 꺼져 있다(e1/report-4.md 5절). commit 6 배포 전에 지금 image로 다음을 한다.
1. `/api/v1/pools`, `/api/v1/scheduler/state`, Pod annotation을 `~/.cache/chill-assets/ops/m4-unshare/before/`에 남긴다.
2. rasp5 인스턴스에 남길 Workload 하나를 `staticRouting`으로 그 인스턴스에만 분배한다.
3. 분배를 유지할 다른 Workload마다 `serving-operation prepare`로 자기 인스턴스를 만들고 `staticRouting`으로 그 인스턴스에 분배한다.
4. 분배하지 않을 Workload는 지금 `staticRouting`으로 뺄 수 없다(선언한 UID마다 양의 가중치가 필요하고 선언하지 않은 UID는 지금 분배를 유지한다, [설정 API](scheduler-api.md)). 그 Workload를 삭제하거나, 빈 분배를 받는 작은 변경을 먼저 넣는다(13장 질문).
5. pools와 annotation에서 모든 인스턴스가 Workload 하나인지 확인한다. 되돌리기는 이전 `staticRouting`과 만든 인스턴스의 remove다.

**M5 정리 후보(M4에서는 그대로).** Runtime의 인스턴스별 다중 binding과 route 복원(`runtime.go`, `runtime_scheduler.go`, `InstanceState.Workloads[]`와 UID별 카운터), `staticRouting`의 다중 UID alias(`static_routing.go`, `kube/static_routing.go`), `serving-operation`의 `InstanceShared` 거절(`cmd/serving-operation/main.go:489-518`), `edge.dacs.io/serving-routing` annotation의 reader와 writer와 annotation이 없을 때의 fallback(`kube/observer.go`, `kube/operations.go`), Dashboard의 인스턴스별 여러 서비스 표시(`resources.tsx`, `read-types.ts`), 설계 문서(workload-management, heterogeneous-workloads, scheduler-runtime-measurement)의 공유 문단. 같은 노드 공동 사용, 프로파일 재사용, Bundle은 유지한다.

**그대로 남는 것.** 재생 코드(용량표 계산과 오프라인 검증), `schedulerPlan`, `schedulerMeasuredOptions`, `schedulerBaseline`, 전환 실행기, 가용 노드 최저 모드 규칙, 비교 집합, firstfloor batcher.

## 7. 설정과 저장 계약

| 대상 | 변경 |
|---|---|
| 추가(commit 2) | `capacityWindow`(> 0), `capacityWindows`(≥ 1), `capacityPassingWindows`(1 ≤ x ≤ `capacityWindows`), `capacityMinimumRequests`(≥ 1). 설정 decode가 strict(`scheduler_service.go:118`)라 배포 때 `serving-scheduler` ConfigMap과 Dashboard 설정 화면을 같이 바꾼다 |
| 삭제(commit 6) | `algorithm`, `replays`, `seed`, `maxCandidates`, `maxReplayRequests`, `controls.sharing`. 같은 방식으로 ConfigMap과 Dashboard를 바꾼다 |
| 판단 기록 | `v1alpha5`에 `decision.capacity`와 `management`의 `capacity-row-built`가 더해지고 `candidates`, `search.truncated`, `policy`가 빠진다. snapshot 인스턴스는 `workloadUID` 하나이고 `arrivalAgesNanos`가 빠진다. 관측 reader는 없는 필드를 빈 값으로 읽으므로 버전을 올리지 않는다(ADR 0019). 전환 전 시간의 기록은 새 reader에서 인스턴스의 Workload가 비므로 전환 시각을 걸치는 `OperatingPower` 구간은 쓰지 않는다 |
| evidence store | `serving/capacity/<capacityVersion>/` prefix. `capacityVersion`을 올리면 이전 prefix는 읽지 않고 지운다 |

## 8. 커밋 단위

| # | commit | 내용 | 의존 |
|---|---|---|---|
| 1 | `perf(serving): select replay events with a heap` | `nextEvent`의 선형 탐색을 heap으로. 기존 재생 test의 판단과 같은 값 | 없음 |
| 2 | `feat(serving): build per-option SLO capacity rows` | `internal/serving/capacity.go`(`hack/kappa`의 κ 계산 이동, 격자와 단조 검사), `capacityVersion`, 용량표 worker, evidence store 행, `capacity-row-built`, 네 설정, `CapacityPending`, `CapacityRejected`, `/api/v1/scheduler/capacity` 조회. Tick은 그대로 | 1 |
| 3 | `feat(loadgen): send Poisson arrivals` | `chill_loadgen replay --arrivals poisson --seed`. 지금은 고정 간격뿐이다(`cli.py:55-58`) | 없음 |
| 4 | `chore(hack): report the non-execution latency percentile` | `e1.sql`에 5장의 열 | 없음 |
| 5 | `docs(spec): judge SLO with per-option capacity and one Workload per instance` | 10장 승인 묶음의 추적 문서(README, 03, scheduler, workloads, dashboard SRS). 00과 04는 로컬 사본에 같은 문장 | 사용자 승인 |
| 6 | `feat(serving): plan with capacity sums and node plans` | `internal/serving/scheduler_capacity.go`의 노드 계획, 사슬, T, 전환 판정, Pareto DP, 가중치, `decision.capacity`, `SharedInstance`, 6장의 공유 삭제와 단일 Workload 저장 모델, `StaticRoutingTarget` 거절. `EvaluateScheduler` 교체, 12장 삭제, `hack/kappa` compare가 서버 계획기와 `v1alpha5` 세그먼트를 쓰도록 변경, 설정 삭제, ConfigMap, Dashboard, 문서 | 2, 5, 11장 오프라인 비교, 6장 운영 단계 |

## 9. 검사

| commit | 검사 |
|---|---|
| 1 | heap 전후 재생 결과가 기존 scheduler test 전체에서 같음. 8,000 요청 창 시간 기록 |
| 2 | 고정 fixture profile의 κ가 상수와 같음(재생이나 배칭 규칙 변경이 κ를 바꾸면 실패하고 `capacityVersion`을 올리라고 알림). 같은 행을 두 번 만들면 같은 본문. 충족, 미충족, 충족 격자는 `NonMonotone`이고 κ 없음. 창 요청 수가 `λ × capacityWindow` 이상. 19/20 판정 경계. m 139 ms, p99 100 ms에서 κ 0. profile, SLO, m, 설정, version 변경이 새 키를 만들고 그 외 변경은 만들지 않음. 재시작 뒤 store에서 읽고 다시 계산하지 않음. strict decode가 네 필드 누락을 거절 |
| 3 | 같은 seed의 도착 시각이 같고 평균 간격이 1/rate 근처 |
| 6 판정 | 3장 표의 40 req/s 두 예. 모드 변경 계획의 `κ_ret` 0. route 뒤 remove 계획은 keep κ를 남김. `W > L − m`인 계획은 에너지가 작아도 선택되지 않음. 모든 계획이 미충족이면 최대 초과량 순. SLO가 다른 Workload 두 개에서 각자 판정. 실제 분배에서 한 인스턴스가 두 Workload에 있으면 `SharedInstance`. 꺼진 Workload 노드는 keep뿐이고 binding 변경으로 판정 대상이 늘지 않음. `StaticRoutingTarget`이 두 UID alias를 거절. 단일 `workloadUID` 기록 왕복과 `OperatingPower` 소속. 공유 fixture(`scheduler_placement_test.go`, `scheduler_test.go`의 교차 UID 대기, `runtime_scheduler_test.go`, `scheduler_transition_test.go`)는 Workload별 인스턴스로 다시 씀 |
| 6 DP | 무작위 소형 fixture(Workload 1~3개)에서 DP의 충족 판정이 정확한 κ 합 brute force와 같음(예: κ 0.34λ 인스턴스 세 개가 충족). 칸 수를 바꿔도 충족 판정이 같음. 가중치 반올림 뒤 모든 인스턴스가 κ 안(예: κ 99.6과 0.4, λ 100). 배치 상한 변경이 remove와 prepare. 요청 에너지가 route에서 나뉨 |
| 6 기간과 기록 | 사슬 197 s, horizon 60 s에서 keep과 모든 계획의 T가 같고 trial J 전체 포함, `TransitionExceedsHorizon` 없음. option 40개에서 재진입 계획이 DP에 있음(D-22). 제외된 노드 계획이 사유와 함께 기록됨. 입력 순서를 섞은 두 snapshot의 판단이 `reflect.DeepEqual`. 완전 동률은 keep |
| 전체 | `make lint test`, `make arch-check`, `make helm-lint helm-template`, `make dashboard-check` |

## 10. 명세 변경 제안 (승인 묶음 하나)

①, ①', ④(용량)와 ⑤(인스턴스 공유)를 한 번에 승인받는다. 사용자 승인 전에는 효력이 없다. 원문은 ff8fc12 기준이고, 00과 04는 저장소가 추적하지 않는 로컬 사본(`.gitignore:22`)이다.

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
| 칸 크기의 과대 배치(오프라인) | E1 snapshot과 Workload 2~3개 합성 snapshot에서 부하를 바꿔 DP 해와 정확한 brute force 최소 에너지 해의 J 차이를 칸 수별로 | 분포 기록. E5 전 사용자 검토의 근거 |
| 실클러스터 단계 | commit 6 배포 뒤 tight 자동제어, 포아송 0.633, 2, 5 req/s 각 5분, rasp5 κ(34.3)를 넘는 40 req/s 5분, soak 2 req/s 1시간 | 40 req/s에서 첫 판단이 추가 인스턴스를 고름. 전환 구간을 포함해 단계마다 SLO 충족. 예측 `B`, `W`, 비우는 시간과 관측 큐 비교 |
| 점유 구간 | 같은 40 req/s 단계의 관측 처리량 | 응답 교환까지 인스턴스가 점유되면 rasp5 κ는 10.8이다. 이 단계가 어느 쪽인지 정한다 |
| Tick | `evaluationElapsedNanos`, CPU | 40 req/s에서도 Tick p99 ≤ 100 ms, CPU가 500m 아래, 간격 ≤ 15 s |
| 에너지(실측) | 판단마다 `ExpectedJoules / T`를 다음 판단까지 적분한 1시간 합과 `OperatingPower` 비교 집합 실측 | 차이 ≤ 2% |
| 용량표 | worker의 행 수, `NonMonotone` 수, 계산 시간, κ 0.9배와 1.2배 포아송 단계(rasp5) | 0.9κ 충족, 1.2κ 초과, `NonMonotone` 0, 전체 표 계산 시간 기록 |
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

- 결정: κ는 기존 재생, 포아송 도착, 격자와 이분 탐색으로 미리 계산한다. Tick은 κ 합, 노드 계획, Pareto DP이고 재생이 없다. 비교 기간은 `T = max(60 s, 최장 사슬)` 하나다. 전환 중 SLO는 Workload마다 필수 조건이다. 인스턴스 공유는 연구 범위 밖이고 M4는 계획기와 저장 모델의 부담만 지운다(6장). 전환은 shadow 없이 오프라인 비교와 실클러스터 단계로 확인하고 문제가 있으면 이전 image로 되돌린다. 완료 여유는 5장 규칙이다. imagenet은 꺼진 채로 둔다. Workload 여러 개의 칸 크기는 E5 전에 11장 측정으로 다시 검토한다. `projected-work-history`는 전환에서 지운다.
- 질문 1: 10장 승인 묶음(①, ①', ④, ⑤)을 승인하는가. 00과 04의 로컬 사본은 누가 반영하는가.
- 질문 2: 공유를 풀 때 rasp5 인스턴스에 남길 Workload와 분배를 유지할 Workload는 무엇인가. 제안: tight가 rasp5 인스턴스에 남고(E1 자동제어 대상), relaxed와 imagenet은 분배하지 않는다.
- 질문 3: 분배하지 않을 Workload는 삭제하는가, 빈 `staticRouting`을 받는 작은 변경을 6장 운영 단계 전에 넣는가. relaxed를 유지한다면 자기 인스턴스를 둘 노드는 어디인가.
- 질문 4: firstfloor가 배치 상한을 재준비 없이 바꿀 수 있는지 확인하지 않았다. 지금은 교체(remove와 prepare)로 센다. 바꿀 수 있다면 비용이 없는 동작으로 둘 수 있다.
- 질문 5: `capacityVersion`은 사람이 올리는 상수이고 고정 κ test가 올릴 때를 알린다. 이 방식으로 충분한가. image마다 다시 계산하면 확실하지만 배포마다 표 전체를 다시 만든다.
