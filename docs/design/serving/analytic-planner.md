# 피드백 기반 계획기

계획기는 측정 프로파일로 분배를 구성하고 저비용 탐색 뒤 현재 구성과 최종 후보를 직접 재생해 비교한다. 수치와 물리 admission의 공통 adapter는 `internal/serving`, 탐색 순회는 `internal/scheduler`가 소유한다.

| 패키지 | 책임 |
|---|---|
| `internal/scheduler/protocol` | 중립 문제, 계획, 순위와 평가 port |
| `internal/scheduler/v1` | 현재 구성과 빈 구성에서 두 노드, 세 노드 이웃 탐색 |
| `internal/scheduler/v2` | 피복, 연마, 결합, 가격 유도 출발점과 정밀 후보 평가 |
| `internal/scheduler` | 공식 전략 선택과 이웃 탐색 fallback |

의존 방향은 `serving → scheduler → {v1,v2} → protocol`이다. 탐색 패키지는 Serving이나 Kubernetes를 import하지 않는다. `settings.planSearchStrategy`는 `v1` 또는 기본 `v2`이며 모든 자동제어 Workload에 같은 선택을 적용한다.

## 시간 회계와 요청 동결

시간은 각 프로세스의 monotonic 경과로 측정한다. client와 server의 UTC 차이를 편도 전송시간으로 계산하지 않는다. 유효한 client 보고와 정확한 요청 identity가 결합되고 native 실행과 HTTP 교환이 완료된 표본에 다음 회계를 적용한다.

| 기호 | 의미 |
|---|---|
| E | client의 전송 시작부터 전체 응답 수신까지의 경과 |
| Q | server의 accepted부터 assigned까지의 ingress 대기 |
| S | server 수신부터 native sender elapsed를 동결한 시점까지의 경과 |
| N | native가 보고한 전체 요청 경과 |
| TOTAL | `E − Q − N` |
| POST | `E − S − N` |
| PRE | `TOTAL − POST` |

POST는 외부 왕복도 포함하는 회계상 reserve다. 음수 POST나 TOTAL<POST, 잘못된 단계 순서, 결합할 수 없는 identity는 유한 시간 표본에서 제외하고 원본 실패와 결손은 보존한다.

Workload별 TOTAL과 POST는 SLO percentile로 각각 가공한다. 유효 표본이 없으면 `completionMarginMS`를 초기값으로 쓰고 첫 유효 표본부터 갱신한다. 추가 표본 gate나 configured margin의 중복 가산은 없다. `PathFeedback.observedMean`은 같은 결합 cohort의 산술평균이다.

실제 요청은 accept 때 POST를 동결한다. native에는 실제 server sender elapsed를 전달하며 Tick의 PRE나 TOTAL을 더하지 않는다. 진행 요청은 frozen sender와 POST, captured native elapsed와 남은 수명을 유지한다. returning 요청은 terminal native 경과와 남은 반환 점유로 복원한다. 새 피드백은 합성 요청의 배치 선택도 다시 평가한다.

합성 요청은 `S_model=Q_model+PRE`, `E_model=Q_model+N_model+TOTAL`을 사용한다. client는 응답 뒤 별도 연결로 보고하며 정확한 요청 ID로 결합한다. 결합 cohort와 시간 분해 불가능한 전체 client 실패를 따로 보존한다.

## 수요와 비교 모집단

관측 구간의 끝은 `snapshot.observationEnd`, 진행 요청을 동결한 판단 원점은 `snapshot.at`이다. 도착률은 각각 실제로 관측한 장기 구간과 최근 한 제어 주기의 계산 도착 수로 구한다.

`λ_observed = max(장기 도착률, 최근 도착률) + q / 장기 관측 길이`

q는 service 시작이 아직 관측되지 않은 pending, queued, deferred 요청과 native 관측 없는 assigned 요청 중 남은 수명이 양수이거나 미지정인 요청 수다. 관측된 running과 returning은 q에 넣지 않는다. 실제 점유와 drain 의무는 별도 장부에 유지한다.

`λ = max(λ_observed, settings.minimumDemand[Workload UID])`

`minimumDemand`는 유한한 비음수 req/s이며 생략한 UID는 0이다. 빈 UID와 음수, 비유한 값은 거절한다. 교체 회수의 최소 관측 부하에도 같은 하한을 적용한다. 이 하한은 사용자가 선택한 용량 예약이다.

장기 관측 길이가 `min(observationWindow, controlInterval)`보다 짧으면 `PartialObservationWindow`로 대기한다. 전체 관측 창이 차기 전에는 기존 인스턴스 제거, 배치 정책 변경과 기존 양수 분배 철회를 제한하고 확장만 허용한다.

직접 재생은 같은 불변 arrival cohort와 현재 outstanding에서 시작한다. 관측 패턴을 비교 기간에 반복하며 minimumDemand의 부족분은 모든 후보에 같은 균등 합성 요청으로 보충한다. 합성 요청은 실제 관측으로 표시하지 않는다. 비교 기간 뒤에는 새 도착 없이 cohort의 완료, 실패, 만료와 미완료를 수집한다.

모든 후보는 같은 실행 제외 범위를 사용한다. 실패한 action의 미래 효과와 실행 입력이 없는 executor를 모델 공급에서 제외하되 실물 점유, 예약, owner 의무, 요청 모집단은 보존한다. retained worker가 실제 처리한 만큼만 큐를 줄이고 running batch는 같은 조건부 residual 법칙을 쓴다.

## 분배와 현재 요청률 검사

profile의 양의 `RoutingWeight`는 빠른 관측 support에서 도출한 상대 분배값이다. 분배가 켜져 있으면 에너지와 physical ID의 안정적인 순서로 합 1000인 정수 weight를 구성한다. 꺼져 있으면 현재 weight와 새 option의 configured weight를 사용한다. 요청 에너지도 이 정수 weight를 소비한다.

현재 구성과 선택 구성의 인스턴스별 양수 nominal rate를 중복 제거해 Poisson 창으로 검사한다. κ̂는 그 검사에서 통과한 최대 rate다. 0이나 빈 tests는 통과 근거 부재를 나타낸다. 이 값이나 검사 실패만으로 후보를 제거하지 않는다.

`BuildCapacityRow`와 `hack/kappa`의 전체 격자, 이분 탐색과 `NonMonotone`은 명시적 offline 계산이다. online 판단은 행 준비를 기다리지 않는다. TOTAL이나 POST가 바뀌면 이전 pass, CDF와 κ̂를 현재값으로 재사용하지 않는다. 같은 수치 입력의 완료 결과와 불변 raw, random tape는 재사용할 수 있다.

### 디스패치 점유

현행 native-pair 측정은 인접한 고정 배치의 시작 간격에서 앞 배치의 실제 Go runner exchange S를 빼고 배치 크기로 나눈 signed ns/request다. 원 signed 통계와 운영 선택값은 구분한다.

운영 선택은 minimumPairs를 충족한 rolling median, 정확한 legacy Accepted DOM, configured `defaultDispatchOccupancyNanos` 순이다. Node UID, 모델, 전력모드, 실행 경로, batch, profile ID를 맞춘다. legacy DOM은 기록된 runtime image 문자열도 맞춰야 한다. 운영 median은 0 이상으로 clamp하고 반올림한다.

rate 계산의 처리율 추정은 측정 batch별 `b/(profileMean(b)+b×d)`의 최댓값이며 replay는 시작한 실제 batch마다 `batch size×d`를 한 번 더한다. 저비용 service 항은 현재 gap의 조건부 SLO 분위수에 batch 점유를 더한다. 분배값, 처리율 추정, 현재 κ̂와 실제 지연은 각각의 의미를 유지한다.

snapshot과 검사 입력은 선택 tuple, source와 `provenance{source,pairs,window,updatedAt,value}`를 보존한다. metadata만 달라지고 선택 d와 전체 수치 입력이 같으면 계산을 재사용할 수 있다. legacy scalar와 신규 burst evidence의 저장 계약은 [측정 API](measurement-api.md)에 둔다.

## SLO 판정과 후보 순서

판정은 불변 Definition별 전체 요청 모집단의 설정 percentile을 사용한다. 유한한 성공 완료 지연을 보존하고 drop, 오류, 만료와 미완료는 전체 SLO의 무한 지연 tail로 취급한다. 성공 전용 percentile과 조건부 potential은 별도 진단이다. 성공 표본이 없으면 성공 percentile은 null이다. 모집단 0에는 현재 비교 의무가 없다.

전체 percentile 순위는 `ceil(n×p/100)`이며 목표와 같은 완료시간은 충족이다. percentile이 허용하는 실패 tail에 별도 zero-failure 조건을 추가하지 않는다. JSON에는 무한대나 가짜 완료시간을 저장하지 않는다.

물리적으로 적격인 후보 중 모든 Workload의 SLO를 충족한 후보를 먼저 비교한다. 충족 후보는 에너지 차이, 현재 구성 유지, 논리 key 순이다. 미충족 후보의 Workload별 tuple은 다음 순서다.

1. `1 − OnTime/Population`: 늦은 성공도 포함한 기한 내 미완료 비율.
2. 유한 성공 요청의 상대 지연 초과.
3. 실패가 있을 때의 조건부 potential 초과. 계산할 근거가 없으면 무한대로 비교하고 진단의 결손 이유를 남긴다.

`sloPriorityStrategy=excess`는 나쁜 tuple부터 비교한다. `workload`는 설정 UID 순서를 먼저 적용하고 미지정 UID끼리 worst-first 순서를 쓴다. 빈 순위는 excess와 같다. 미등록 UID는 설정에 보존하고 현재 비교에서 제외한다. tuple 동률 뒤에 에너지와 keep, key를 적용한다.

전체 SLO 미충족만으로 개선 후보의 retirement를 일괄 거절하지 않는다. [SLO 미충족 후보 비교](../../spec/scheduler/SRS.md#slo-미충족-후보-비교)는 accepted 요청의 소유권이나 물리 handoff를 면제하지 않는다.

## 노드 계획과 전환

노드마다 keep과 측정 option 변형 하나를 후보로 만든다. 노드당 serving 인스턴스 하나, 인스턴스당 Workload 하나다. 같은 모델의 Workload도 각각의 부하와 SLO를 유지한다. 비자율 Workload 소유 노드는 keep으로 고정한다. 실제 중복 실물은 점유로 모두 보존하고 정상 반환 전 예산에서 빼지 않는다.

배치에는 Node Ready, 측정 격리, owner와 예약, 실제 memory 예산, whole-GPU inventory와 UUID 중복 검사가 필요하다. `maxReplicas`와 `maxInstances`에는 준비 중 실물과 미생성 예약 target도 한 번만 센다. Ready 공급은 Ready로 확인한 실물만 센다.

placement, resources, replicas, routing, batching, power, membership은 각 제어 범위를 제한한다. batching이 꺼져 있으면 현재 `(MaxBatch,MaxWait)` 정책 집합 안의 측정 변형만 허가한다. membership이 꺼져 있으면 해당 Workload의 기존 노드 범위에서 선택한다.

새 인스턴스는 선택 구성에서 양수 weight를 받아야 한다. 같은 인스턴스의 initialized/measured 범위 안 batch cap이나 wait 변경은 재시작 없이 route의 native 정책 ACK로 반영한다.

서로 다른 노드의 준비는 병렬 진행한다. 같은 노드의 자원 blocker는 stop, drain, remove 후 prepare한다. routed source를 먼저 멈춰야 하면 최종 목표에 포함된 독립 목적지로 검증된 bridge와 ACK handoff가 필요하다. 관측된 재부팅 방향은 old actor 제거 후 power, prepare 순서이며 다른 방향은 [전환 실행 계약](scheduler-operations.md)을 따른다.

진행 owner의 frozen target과 실제 예약 범위만 고정한다. 밖의 후보 평가와 독립 확장은 계속한다. 한 인스턴스의 관측 실패는 해당 실물을 Ready 공급에서 빼지만 allocation과 owner를 지우지 않는다. 정확한 종료 Pod-only inventory만 executor stop/drain 없이 기존 remove로 연결한다.

### 준비된 부분 용량

최종 prepare가 남아 있어도 검증된 Ready 목적지를 먼저 편입할 수 있다. current 공급이 있으면 기존 공급을 보존하고 독립 노드 공급만 더한다. 빈 출발에서는 Ready 부분집합 weight를 1000으로 정규화한다. stopped, retiring이나 이번 Tick에 stop 가능한 경로는 되살리지 않는다.

부분 배분도 현재 수요와 timing의 직접 재생, 정확한 identity, native 정책 ACK와 accepted work 보호를 거친다. 미확인 물리 상태나 support이면 current를 유지한다. 최종 목표 완료와 전체 SLO 충족은 전체 목표 구성에서 별도로 판정한다.

## 고정 비교 기간과 에너지

`H = comparisonPeriodNanos`는 양수이며 한 판단의 keep, 후보, payback과 minimumGain에 공통 적용한다. sample은 600초다. 후보의 긴 사슬이나 미선택 후보 때문에 H를 늘리지 않는다. H는 요청 수명이나 operation timeout을 대신하지 않는다.

keep의 노드 에너지는 `P_before × H`다. 전환은 전체 pre/post operation wall J와 각 상태의 baseline을 합한다.

`J_pre + J_post + P_mid × max(0,min(r,H)−t_pre) + P_after × max(0,H−r−t_post)`

동작 평균 시간을 baseline 구간에, trial 최대 시간을 전환 schedule에 사용한다. operation은 완결 비용을 전부 부과하며 H 뒤 Serving baseline과 요청 에너지는 0이다. 사슬이 H를 넘으면 `TransitionExceedsHorizon`을 기록한다. bridge 시작 전 대기는 H 안에서 이전 baseline으로 센다.

요청 에너지는 route 전 retained 처리와 overflow, route 뒤 최종 정수 분배로 나눈다. 비교 집합 밖의 동일 노드 항은 소거한다. 비교 집합에는 keep 외 적격 계획이 있는 노드만 포함한다.

동작 시간 추정은 exact, 같은 Node, 같은 DeviceClass, 명시 default 순으로 고른다. SLO 회복은 이 추정을 사용할 수 있다. 에너지 이동은 공통 미지 항을 signed 상쇄한 뒤 남은 필수 셀이 모두 결정 가능해야 한다. keep가 SLO 충족이고 새 비-exact operation 셀이 필요한 후보는 replay 전에 `EnergyUndecidable`로 제외할 수 있다. 누락 측정은 결정의 `missingCells`로 연결한다.

현재 구성이 충족할 때의 에너지 축소는 개별 교체 payback과 minimumGain을 통과해야 한다. 교체를 해당 노드 keep으로 되돌려도 충족하면 최소 관측 부하와 같은 H에서 비교한다. 지불되지 않은 교체는 `ReplacementUnpaid`다. 빈 노드의 새 절전도 같은 개별 검사를 거치며 다른 노드 이득으로 손해를 덮지 않는다.

minimumGain은 선택 operation의 `energyUncertaintyJoules`와 baseline의 `wattsUncertainty`를 signed 노출량으로 전파한다. 같은 immutable reference와 trial의 공통 항을 먼저 상쇄하고 잔여 절댓값을 합한다. nominal 이득이 문턱보다 커야 전환한다. 같으면 keep이다. 이 문턱은 조건부 위상과 결측 민감도다.

`transitionReserveFraction>0`이면 전환 구간마다 실제 Ready와 ACK, owner와 예약상 사용 가능한 공급으로 예비를 검사한다. 같은 공급의 중복 담보를 금지한다. 부족은 후보별 `TransitionReserveInsufficient`이며 f=0은 추가 예비만 끈다. `scaleDownHeadroomFraction`의 하강 보호도 현재 유효 수요에 적용하며 실제 요청 J를 할증하지 않는다.

## 탐색과 예산

이웃 탐색 전략은 keep과 빈 구성에서 두 노드 이웃을 탐색한 뒤 각 끝점에서 세 노드 탐색을 이어 간다. 정밀 평가는 `planSearchWorkers`로 나눠 수행하고 전순서로 합친다.

증분 탐색 전략은 keep, 빈 구성과 선택적 가격 유도 출발점을 사용한다. 피복은 초과와 instance 상한을 줄이고 연마는 한 노드, 두 노드 개선을 반복한다. 이어 활성 계획의 결합을 탐색한다. scalar 평가에서는 demand replay를 실행하지 않는다.

각 출발점은 남은 평가 예산을 남은 출발점 수로 나눈 올림 몫을 받고 미사용 몫을 이월한다. shortlist는 같은 physical placement의 batch 변형을 중복 압축하여 후보 수를 한 배치 정책에 소모하지 않는다. 출발점 끝점은 shortlist 크기 밖에서도 보존한다.

정밀 평가는 keep seed와 finalists에 같은 직접 재생, admission과 후처리를 적용한다. worker는 예상 pre 시간이 긴 후보부터 공유 큐에서 가져오되 결과의 관측, 후처리와 비교는 원래 후보 순서로 수행한다. 완료 순서가 선택을 바꾸지 않는다.

| 설정 | 기본값과 경계 |
|---|---|
| `planSearchEvaluations` | 증분 평가 500000회, 양수 |
| `planExactFinalists` | 16, 양수. 끝점 때문에 실제 정밀 후보 수는 더 클 수 있음 |
| `planPolishActive` | 8, 0이면 모든 searchable 변형 |
| `planPricedStart` | true, 다른 출발점과 다를 때 가격 유도 집합 추가 |
| `planPostFilterFinalists` | false, true이면 후보별 payback/minimumGain 뒤 비교 |
| `planSearchWorkers` | 양수. 이웃 탐색과 정밀 funnel의 병렬 수 |

모든 정밀 후보가 거절되면 전체 적격 변형의 한 노드 정밀 탐색을 시도하고, 이어 공통 selector가 이웃 탐색 fallback을 연결한다. 후처리를 이미 적용한 결과에 호출부가 다시 적용하지 않는다.

탐색 벽시계 마감은 판단 시작부터 제어 주기 절반이다. 증분 평가 수와 마감은 순회 중단에 적용한다. Tick은 전처리, 가격 반복과 정밀 funnel까지 수행한다. 취소된 계산은 이전 값이나 실패 검사로 대체하지 않는다.

## 기록과 증거 경계

판단에는 settings, 관측 구간, 최소 수요와 유효 rate, waiting q, timing, 정수 분배, 현재 tested rates, 선택 사슬, 에너지와 제외 이유를 연결한다. heuristic 결과와 직접 재생 결과를 구분한다. `exactEvaluated`는 keep seed와 replay 생략 판정도 포함한다.

`search`의 출발점, 평가 수, 후보 수, 가지치기, fallback, budget 종료와 시간은 [스케줄러 API](scheduler-api.md)에 둔다. `dualBound`는 가격 반복 진단이다. 전체 평가 구성 목록을 기록하지 않는다.

저장 계약은 [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다. 프로파일 재사용은 source와 확인 정책을 포함한 대상 파생 ID를 사용하며, 대상 Node 비용이나 d를 암묵적으로 복사하지 않는다. 운영 수용은 [실클러스터 수용 기준](scheduler-operations.md#실클러스터-수용-기준)을 따른다.
