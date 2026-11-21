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

Workload별 TOTAL과 POST는 SLO percentile로 각각 가공한다. POST는 경로(인스턴스)별로도 같은 percentile로 가공해 `PathFeedback.paths`에 둔다. 한 경로의 POST 꼬리가 다른 경로의 판단에 옮겨 가지 않는다. 유효 표본이 없으면 `completionMarginMS`를 초기값으로 쓰고 첫 유효 표본부터 갱신하며, 표본이 없는 경로도 `completionMarginMS`를 쓴다. 추가 표본 gate나 configured margin의 중복 가산은 없다. `PathFeedback.observedMean`은 같은 결합 cohort의 산술평균이다.

실제 요청은 경로에 배정될 때 그 경로의 POST를 동결한다. native에는 실제 server sender elapsed를 전달하며 Tick의 PRE나 TOTAL을 더하지 않는다. 진행 요청은 frozen sender와 POST, captured native elapsed와 남은 수명을 유지한다. 아직 배정되지 않은 요청과 합성 요청은 재생에서 배정될 경로의 POST를 쓴다. returning 요청은 terminal native 경과와 남은 반환 점유로 복원한다. 새 피드백은 합성 요청의 배치 선택도 다시 평가한다.

합성 요청은 `S_model=Q_model+PRE`, `E_model=Q_model+N_model+TOTAL`을 사용한다. client는 응답 뒤 별도 연결로 보고하며 정확한 요청 ID로 결합한다. 결합 cohort와 시간 분해 불가능한 전체 client 실패를 따로 보존한다.

## 수요와 비교 모집단

관측 구간의 끝은 `snapshot.observationEnd`, 진행 요청을 동결한 판단 원점은 `snapshot.at`이다. 워크로드마다 부하 추정이 하나다. 추정은 그 워크로드의 부하 창 `[search.loads[].windowStart, observationEnd)` 안 계산 도착 수 n을 창 길이 T로 나눈 관측 부하 `inputsPerSecond`와, 같은 n의 포아송 도착률 정확 위쪽 신뢰 한계인 계획 부하 `plannedPerSecond = χ²(1 − α; 2(n + 1)) / 2T`다. α는 워크로드 SLO 백분위가 남기는 몫 (1 − 백분위/100)이고, n이 0이면 계획 부하도 0이다. 후보 용량과 SLO, 경로 가중, 재생 도착은 계획 부하를, 에너지와 본전 판단은 관측 부하를 쓴다. 구성을 고르는 판정은 증거가 허용하는 가장 높은 부하에서 통과해야 하기 때문이다. 표본이 작을수록 둘의 차이가 크다(p95, 60초에 28건이면 1.37배, 5,738건이면 1.022배). 현재 Workload가 없는 부하와 백분위가 (0, 100) 밖인 부하(Workload API는 받지 않는다)는 한계가 없으므로 관측 부하로 계획한다.

부하 창은 마지막으로 감지한 도착률 변화 이후의 구간이고, 다음 변화는 그 안에서 찾는다. 변화가 없으면 부하 창은 관측 창 전체다. 감지는 제어 주기 경계마다 그 경계 뒤의 계산 도착 수를 창 전체 평균률이 기대하는 수와 정확 양측 이항 검정으로 비교한다. 유의수준은 판정 증거 수준(아래 SLO 판정), 곧 (1 − 워크로드 SLO 백분위/100)을 관측 창 하나를 보는 판단 수 `ceil(observationWindow / controlInterval)`로 나눈 값을 다시 창 안 제어 주기 경계 수로 나눈 값이다. 판단마다 같은 도착을 다시 검정하므로 한 관측 창을 보는 판단들이 수준을 나눠 쓴다. 검정은 바뀌었는지를 답한다. 기각한 경계 중 p값이 가장 작은 경계를 고르면, 그 경계 앞뒤 한 제어 주기 안(그리고 구간 안)에서 도착 직후의 시점(그 도착이 앞 부하를 닫는다, 시각 +1ns) 가운데 구간 `[시작, observationEnd)`를 두 포아송 부하로 가장 잘 가르는 시점, 곧 `n1·ln(n1/t1) + n2·ln(n2/t2)`이 가장 큰 시점이 새 변화 시각이고 부하 창은 거기서 시작한다. 도착 직후에 시작하는 부분은 도착 사이 간격을 온전히 담으므로 그 수를 길이로 나눈 값이 율을 부풀리지 않는다. 도착 시각에서 시작하는 부분은 짧아질수록 한없이 부풀린다. 또 두 부분은 모두 구간의 평균 도착 간격(구간 길이/도착 수) 이상이어야 한다. 그보다 짧은 부분은 도착 하나가 평균적으로 차지하는 시간보다 짧아 그 율을 판단할 수 없다. 검정 횟수는 늘지 않으므로 유의수준은 그대로다. 판단이 감지한 변화 시각은 `snapshot.loadChangedAt`(UID별)에 남아 다음 판단이 이어받고, 이번 판단이 감지했으면 `search.loads[].changed`가 참이다.

`λ = plannedPerSecond + q / 부하 창 길이`(용량, 경로 가중, 전환 예비와 교량), `λ_관측 = inputsPerSecond + q / 부하 창 길이`(요청 에너지)

q는 service 시작이 아직 관측되지 않은 pending, queued, deferred 요청과 native 관측 없는 assigned 요청 중 남은 수명이 양수이거나 미지정이고 부하 창 시작 전에 받은 요청 수다. 부하 창 안에서 받은 대기 요청은 이미 부하 창의 도착이므로 q에 다시 넣지 않는다. 관측된 running과 returning은 q에 넣지 않는다. 실제 점유와 drain 의무는 별도 장부에 유지한다.

관측된 창 길이(`search.windowEnd − search.windowStart`)가 `min(observationWindow, controlInterval)`보다 짧으면 `PartialObservationWindow`로 대기한다. 전체 관측 창이 차기 전에는 기존 인스턴스 제거, 배치 정책 변경과 기존 양수 분배 철회를 제한하고 확장만 허용한다.

예약된 target이 준비 중인 인스턴스는 이미 건 용량이다. 계획기는 그 인스턴스를 target 인스턴스로서 Ready로 보고 용량과 라우팅을 계산한다. 단 Node가 이미 target 프로파일의 전력 모드에 있을 때만 그렇다. target이 라우팅하는 인스턴스가 모두 Ready로 보일 때만 그 워크로드의 경로가 target으로 바뀐다. 예약은 그대로 두어 그 Node는 계획할 수 없다. 측정과 마지막 경로 판정은 실행 중 구성의 라우팅으로 하고, 준비 중인 인스턴스를 측정 판정의 서빙으로 세지 않는다. 그래서 준비 중인 동안 같은 부족분에 두 번째 준비를 시작하지 않는다.

실행 중 구성이 라우팅하는 워크로드가 마지막 경로를 잃지 않는다는 보호는 Ready이고 멈추지 않은 인스턴스로 가는 경로만 대상이다. 아직 준비 중인 인스턴스로만 라우팅되는 워크로드에는 마지막 경로 보호가 없다. 같은 워크로드를 라우팅하는 target이 둘이면 launch 시각 순으로 늘어놓고 나중에 launch한 target이 이긴다.

직접 재생은 같은 불변 arrival cohort와 현재 outstanding에서 시작한다. 워크로드마다 자기 부하 창의 실제 도착을 관측 부하 ÷ 계획 부하 배율로 시간 압축해(도착의 창 안 시각과 반복 주기 모두) 비교 기간에 반복한다. 재생은 관측된 도착의 무늬를 계획 부하로 제공한다. 합성 요청은 만들지 않는다. 도착이 없으면 재생 모집단은 비고, 후보는 에너지로만 가려진다. 실행 중 구성이 라우팅하는 워크로드는 마지막 경로를 잃지 않는다. 비교 기간 뒤에는 새 도착 없이 cohort의 완료, 실패, 만료와 미완료를 수집한다.

모든 후보는 같은 실행 제외 범위를 사용한다. 실패한 action의 미래 효과와 실행 입력이 없는 executor를 모델 공급에서 제외하되 실물 점유, 예약, owner 의무, 요청 모집단은 보존한다. retained worker가 실제 처리한 만큼만 큐를 줄이고 running batch는 같은 조건부 residual 법칙을 쓴다.

## 분배

profile의 양의 `RoutingWeight`는 빠른 관측 support에서 도출한 상대 분배값이다. 분배가 켜져 있으면 에너지와 physical ID의 안정적인 순서로 합 1000인 정수 weight를 구성한다. 꺼져 있으면 현재 weight와 새 option의 configured weight를 사용한다. 요청 에너지도 이 정수 weight를 소비한다.

### 디스패치 점유

현행 점유 d는 프로파일 세션이 포화 수준에서 모은 배치별 점유 표본(요청 하나가 실행기를 차지한 ns)에서 얻는다.

배치당 선택은 프로파일의 세션 포화 수준 점유 표본 중앙값, 없으면 configured `defaultDispatchOccupancyNanos`다. Node UID, 모델, 전력모드, 실행 경로, batch, profile ID를 맞춘다. 운영값은 0 이상으로 clamp하고 반올림한다. 서빙 관측으로 d를 갱신하지 않는다.

rate 계산의 처리율 추정은 측정 batch별 `b/(profileMean(b)+b×d)`의 최댓값이며 replay는 시작한 실제 batch마다 `batch size×d`를 한 번 더한다. 저비용 service 항은 gap의 조건부 SLO 분위수에 batch 점유를 더한다. 실행 중인 인스턴스의 gap은 현재 idle 나이다. 아직 실행하지 않은 option은 Workload 계획 요청률 λ가 뜻하는 평균 idle 구간 1/λ(Poisson 도착에서 빈 실행기가 기다리는 평균)에서 같은 방식으로 질의한다. λ=0은 그 극한인 가장 긴 gap이다. 분위수가 식별되지 않으면(기록이 표본 하한보다 적음) profile 부재와 같은 `MissingConditionalService`다. 분배값, 처리율 추정과 실제 지연은 각각의 의미를 유지한다.

snapshot과 검사 입력은 선택 tuple, source와 `provenance{source,pairs,updatedAt,value}`(source는 profile 또는 default)를 보존한다. metadata만 달라지고 선택 d와 전체 수치 입력이 같으면 계산을 재사용할 수 있다.

## SLO 판정과 후보 순서

판정은 불변 Definition별 전체 요청 모집단의 설정 percentile을 사용한다. 유한한 성공 완료 지연을 보존하고 drop, 오류, 만료와 미완료는 전체 SLO의 무한 지연 tail로 취급한다. 성공 전용 percentile과 조건부 potential은 별도 진단이다. 성공 표본이 없으면 성공 percentile은 null이다. 모집단 0에는 현재 비교 의무가 없다.

전체 percentile 순위는 `ceil(n×p/100)`이며 목표와 같은 완료시간은 충족이다. percentile이 허용하는 실패 tail에 별도 zero-failure 조건을 추가하지 않는다. JSON에는 무한대나 가짜 완료시간을 저장하지 않는다. 이 순위 규칙은 구성을 고르는 판정이다.

판정 증거 수준. 스케줄러가 이미 정한 serving을 버리는 판정은 유의한 증거가 있어야 한다. 그 판정들의 수준은 (1 − p/100)을 관측 창 하나를 보는 판단 수 `ceil(observationWindow / controlInterval)`로 나눈 값이다(배포 값 p95, 60초, 10초에서 0.05/6). 판단마다 같은 증거를 다시 보기 때문이다. 기한 내 k건, 모집단 n건은 단측 이항 아래 꼬리 P(X ≤ k | X ~ Binomial(n, p/100))이 이 수준보다 작을 때만 미달이다. 꼬리는 정규화 불완전 베타 함수 I_{1−p/100}(n − k, k + 1)로 계산한다. k가 정수이면(창 측정) 정확한 이항 검정이다. k가 실수이면(아래 replay 판정) 그 함수가 주는 아래 꼬리의 연속 확장이고, 정확한 값은 정수 k에서만이다. k는 반올림하지 않는다. 95 %와 구별되지 않는 결과(60초 3,839건 가운데 3,646건)는 충족이다.

창 측정의 판정. 창 측정은 Workload 정의 d(Definition identity: UID와 generation, 모델과 전처리의 UID와 generation)를 실행 중 구성이 serving한 결과다. serving의 원소는 그 Workload에 route된 인스턴스의 profile ID, `MaxBatch`와 route weight다. profile ID가 Node, device, 전력모드와 측정 조건을 고정한다. 인스턴스 ID는 포함하지 않으므로 같은 profile과 cap으로 다시 만든 인스턴스도 같은 원소이고, 같은 profile과 cap의 인스턴스 둘은 원소 둘이다. batch 대기(`MaxWait`)와 할당은 원소에 없으므로 그것만 다른 serving은 같은 serving으로 판정한다.

판단한 결과(keep과 모든 후보)가 워크로드를 실행 중 구성과 같은 serving(같은 원소, 배치 상한과 traffic 비율, weight 전체의 배율만 다르면 같다)으로 실행하고 이번 부하 창에 측정이 있으면 창 측정으로 판정한다. 측정이 없고 결과가 워크로드를 keep과 같은 serving으로 실행하면 replay가 판정하되 판정은 증거 수준의 유의성 검정(연속 확장)이다. keep은 계획기가 계획하는 committed view의 구성, 곧 지금 도는 구성이거나 진행 중인 전환이 있으면 그 전환이 만드는 구성이다. 그 serving은 스케줄러가 이미 정한 것이다. n은 부하 창의 관측 도착 수(`observedArrivals`, 비교 기간에 되풀이하지 않은 수)에 snapshot의 진행 요청 수(`population − replayedArrivals`, replay가 한 번씩만 판정한다)를 더한 값이고(창 안에서 받아 아직 진행 중인 요청은 둘 다에 든다), k는 n × replay의 `onTime / population`이다. 창을 되풀이해도 증거는 늘지 않는다. 부하 창에 도착이 없어도 오래 기다린 진행 요청이 실패하면 그것이 증거다. n과 비율은 워크로드의 모든 Definition identity 요청을 각자의 목표로 세고 백분위는 현재 Definition의 것이다(창 측정과 같다). keep의 두 형태(keep의 경로 그대로인 keep 평가와 모든 Node가 keep 계획인 선택)는 serving이 같으면 같은 판정을 받는다. 워크로드의 serving을 바꾸는 결과는 replay의 순위 규칙으로 판정한다. 모든 Node가 keep 계획이고 모든 Workload가 실행 중 구성과 같은 serving인 결과가 현재 구성 유지(`keep`)다.

측정 구간은 w의 부하 창 안에서 snapshot action의 마지막 `endedAt` 이후 받은 w의 compute 요청이다. 구간은 모든 action이 끝났고(`endedAt`이 있고) w에 route된 모든 인스턴스가 w로 routable한 동안만 있다. 그렇지 않으면 측정할 serving이 정해지지 않았다. client 보고(`ClientObservation`)의 완료 시간(`elapsedNanos`, client 송신부터 응답 수신까지이며 replay의 PRE, 서버 시간, POST가 모사하는 시간)이 목표 이하인 완료가 기한 내다. 늦은 완료와 그 밖의 보고(거절, 오류, timeout)는 놓침이다. capture까지 client 보고도 서버 결과(`outcome`)도 없고 받은 지 목표보다 오래된 요청은 기한 내에 끝날 수 없으므로 놓침이다. 보고가 없는 그 밖의 요청은 세지 않는다. 판정은 위의 증거 수준 검정이고 정수 건수(`onTime`, `population`)라 정확한 이항 검정이다. 측정의 존재 조건은 둘이다. 실행 중 serving이 제어 주기 하나(관측 창이 더 짧으면 관측 창) 이상 정해져 있어야 한다: 측정 구간의 시작(부하 창 시작과 마지막 동작 끝 가운데 늦은 쪽)부터 관측 끝까지가 그 길이 이상이다. 그리고 client 보고 수가 percentile의 표본 하한(`MinimumServiceSamples`, p95에서 59) 이상이어야 한다. 동작이 끝난 직후의 몇 초는 그 동작의 꼬리이고 client 미달은 몰려서 온다. 더 짧게 정해진 serving에서는 한 번의 몰림이 판정을 정하는데, 보고가 독립이라고 가정하는 이항 검정은 그것을 보지 못한다. client 보고가 없는 traffic(평가 client가 없는 운영)에서는 측정이 없고 replay가 판정한다. `onTimeRate`는 기한 내 완료 수를 측정 구간 길이로 나눈 값이다.

측정 구간의 끝은 관측 끝보다 워크로드의 목표 지연 하나 앞이다: `[시작, 관측 끝 − 목표)`. 그 뒤에 받은 요청은 아직 완료할 시간이 없어 판정에 넣지 않는다.

창 측정은 결과의 Workload score에 `measured{definitionID,serving,onTimeRate,since,population,onTime,late,satisfied}`로 붙는다. 충족 여부와 아래 tuple의 미완료 비율은 측정이 있으면 그 counts를 읽는다. 성공 지연 초과, potential의 적용 여부와 값, `worstExceedance`는 replay의 값이다. score의 나머지 필드는 replay 예측 그대로 기록한다. 에너지 계산은 측정과 무관하다. 창 측정은 정밀 판정에 적용하며 surrogate 공급은 아래 재생 공급 보정으로만 바뀐다.

물리적으로 적격인 후보 중 모든 Workload의 SLO를 충족한 후보를 먼저 비교한다. 충족 후보는 `unmet`, 에너지 차이, 현재 구성 유지, 논리 key 순이다. `unmet`은 keep이 SLO를 못 지키는 판단에서 후보의 경로 시각(교량 계획은 교량의 경로 시각)을 제어 주기로 나눠 내림한 값이고, keep이 지키면 모든 후보에서 0이다. 한 주기 안에서 먼저 준비되는 것은 다음 판단이 구별하지 못하므로 주기 단위로 묶는다. 같은 항이 정밀 순서(`capacityResult.better`)와 탐색 순서(`protocol.Rank.Better`)에 있다. 미충족 후보의 Workload별 tuple은 다음 순서다.

1. `1 − OnTime/Population`: 늦은 성공도 포함한 기한 내 미완료 비율.
2. 유한 성공 요청의 상대 지연 초과.
3. 실패가 있을 때의 조건부 potential 초과. 계산할 근거가 없으면 무한대로 비교하고 진단의 결손 이유를 남긴다.

`sloPriorityStrategy=excess`는 나쁜 tuple부터 비교한다. `workload`는 설정 UID 순서를 먼저 적용하고 미지정 UID끼리 worst-first 순서를 쓴다. 빈 순위는 excess와 같다. 미등록 UID는 설정에 보존하고 현재 비교에서 제외한다. tuple 동률 뒤에 `unmet`, 에너지와 keep, key를 적용한다.

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

요청 에너지는 관측 부하(`λ_관측`)로 계산하고 route 전 retained 처리와 overflow, route 뒤 최종 정수 분배로 나눈다. 에너지는 관측된 부하의 기대 비용이고, 계획 부하는 용량과 SLO 판정에만 쓴다. 비교 집합 밖의 동일 노드 항은 소거한다. 비교 집합에는 keep 외 적격 계획이 있는 노드만 포함한다.

노드의 부하 전력은 정착한 유휴 전력에 `깨어 있는 전력 증가 × a + Σ 입력당 줄 × 입력률`을 더한 값이다. 노드의 입력률 합을 λ, 이용률 합을 ρ(1 이하), 유지 시간을 τ라 할 때 `a = 1 − (1−ρ) × exp(−λτ)`다. 한 노드에 구성이 여럿이면 깨어 있는 전력 증가가 가장 큰 구성의 증가와 유지 시간을 함께 쓴다. 입력당 줄과 이용률은 입력률을 감당하는 가장 작은 측정 배치 크기(없으면 가장 큰 측정 배치 크기)의 값을 쓴다. 식이 요구하는 값이 식별되지 않았으면 그 노드의 에너지는 비교하지 않고 누락 셀로 남긴다. 부하가 없으면 부하 항은 0이고 값을 요구하지 않는다.

동작 시간 추정은 exact, 같은 Node, 같은 DeviceClass, 명시 default 순으로 고른다. SLO 회복은 이 추정을 사용할 수 있다. 에너지 이동은 공통 미지 항을 signed 상쇄한 뒤 남은 필수 셀이 모두 결정 가능해야 한다. keep가 SLO 충족이고 새 비-exact operation 셀이 필요한 후보는 replay 전에 `EnergyUndecidable`로 제외할 수 있다. 누락 측정은 결정의 `missingCells`로 연결한다. Overload에서 SLO 초과량이 같으면 `missingCells`가 없는 후보가 앞서고, 줄 값은 `missingCells`가 없는 후보끼리만 비교한다. keep은 `missingCells`가 없으므로 미지 에너지 후보는 같은 초과량에서 이기지 못한다. 이 순서는 후보를 보는 순서와 관계없다. 누락 feedback은 이미 기록된 제외 사유를 덮어쓰지 않는다.

현재 구성이 충족할 때의 에너지 축소는 개별 교체 payback과 minimumGain을 통과해야 한다. 교체를 해당 노드 keep으로 되돌려도 충족하면 최소 관측 부하와 같은 H에서 비교한다. 지불되지 않은 교체는 `ReplacementUnpaid`다. 빈 노드의 새 절전도 같은 개별 검사를 거치며 다른 노드 이득으로 손해를 덮지 않는다.

minimumGain은 선택 operation의 `energyUncertaintyJoules`와 baseline의 `wattsUncertainty`를 signed 노출량으로 전파한다. 같은 immutable reference와 trial의 공통 항을 먼저 상쇄하고 잔여 절댓값을 합한다. nominal 이득이 문턱보다 커야 전환한다. 같으면 keep이다. 이 문턱은 조건부 위상과 결측 민감도다.

`transitionReserveFraction>0`이면 전환 구간마다 실제 Ready와 ACK, owner와 예약상 사용 가능한 공급으로 예비를 검사한다. 같은 공급의 중복 담보를 금지한다. 부족은 후보별 `TransitionReserveInsufficient`이며 f=0은 추가 예비만 끈다. `scaleDownHeadroomFraction`의 하강 보호도 현재 유효 수요에 적용하며 실제 요청 J를 할증하지 않는다.

## 탐색과 예산

판단은 탐색 전에 keep을 평가한다(재생은 memo로 탐색과 공유한다). keep이 SLO를 못 지키면 위 `unmet` 순서로 탐색한다. keep이 지키면 탐색하지 않고 keep을 낸다. 예외는 구성을 아직 정하지 않은 경우다: `snapshot.decidedFor`가 제어 Workload 정의 identity를 정렬해 이은 서명과 다르거나 이번 판단이 부하 변화를 감지했고(`search.loads[].changed`), 진행 중인 동작이 없고(모든 action에 `endedAt`), 예약(`snapshot.reservations`)이 없고, 모든 부하 창이 `min(observationWindow, controlInterval)` 이상이며 관측 창이 온전하면(재시작 뒤 partial이 아니면) 에너지 순서로 한 번 탐색하고 판단의 `decidedFor`에 서명을 적는다. 정한 구성을 유지할 때의 사유는 `LoadUnchangedSinceDecided`, 정하지 않았지만 위 조건을 기다릴 때의 사유는 `EnergyDecisionWaits`다. 탐색 없이 낸 판단이 committed view의 구성(예약된 전환이 만드는 중인 구성)을 target으로 보일 때 그 target은 keep의 Fixed 후속 동작만 가지며 새 동작을 만들지 않는다. 컨트롤러는 부하 변화를 감지한 평가(승인 여부와 무관)와 설정 변경(`SetConfiguration`, `AcceptSettings`)에서 기록을 지우고, `decidedFor`가 있는 판단이 게시되고 그 기록(`OnDecision`)이 저장된 뒤 적으며, 다음 capture의 `snapshot.decidedFor`로 넘긴다. keep이 못 지켜서 탐색한 판단은 기록을 바꾸지 않는다.

이웃 탐색 전략은 keep과 빈 구성에서 두 노드 이웃을 탐색한 뒤 각 끝점에서 세 노드 탐색을 이어 간다. 정밀 평가는 `planSearchWorkers`로 나눠 수행하고 전순서로 합친다.

증분 탐색 전략은 keep, 빈 구성과 선택적 가격 유도 출발점을 사용한다. 피복은 초과와 instance 상한을 줄이고 연마는 한 노드, 두 노드 개선을 반복한다. 이어 활성 계획의 결합을 탐색한다. scalar 평가에서는 demand replay를 실행하지 않는다. scalar 공급은 같은 판단에서 이미 수행한 직접 재생의 판정으로 보정한다.

재생 공급 보정. 정밀 판정(keep seed, finalists와 한 노드 정밀 탐색)이 Workload w를 SLO 미충족으로 판정하고 판정한 모집단 일부를 완료하지 못했을 때만 w의 처리량을 측정한다. 판정은 위 SLO 판정의 결과 그대로다. 증거 수준 검정이 충족으로 본 측정이나 이미 정한 serving의 replay는 부족이 아니다. replay가 판정했으면 이 부족(shortfall)의 측정 처리량은 성공 완료율 `((OnTime+Late)/Population) × λ_w`다. λ_w는 계획 부하로, 그 replay가 실제로 제공한 부하다. 실행 중 serving을 이번 구간의 측정이 판정했으면 그 집합이 실제로 처리한 `onTimeRate`다. w를 모두 완료한 판정은 늦은 완료가 있어도 공급이 λ_w에 이르렀다는 것만 보이므로 처리량을 측정하지 않는다. 무효 재생(InvalidReasons)은 후보를 계획대로 실행하지 않았으므로 아무것도 측정하지 않는다. 계획 P의 부족마다 `r_w(P) = 측정 처리량 / S_w(P)`이고 `S_w(P)`는 P의 보정 전 최종 instance profile throughput 합이다. w의 profile throughput이 없는 P에는 보정할 처리량이 없다. `c_w`는 판단 안의 `r_w` 최솟값이며 1 미만일 때만 적용한다. scalar 표의 w profile throughput(최종, retained, service 합과 부하 분배 몫)은 모두 `c_w`를 곱한 값이다.

keep의 재생은 탐색 전에 읽으므로 keep의 부족이 첫 순회의 표를 보정한다. 이 평가는 엔진이 첫 순회에서 SeedKeep 전에 처음 수행하는 keep 평가와 같은 호출, 같은 계획기 상태이며 재생은 memo로 공유한다. 순회의 재생이 어떤 `c_w`를 낮췄으면 표를 다시 만들고 후보를 다시 생성한다. 어느 후보의 재생에서 나온 부족이든 대리 공급의 오차를 보이기 때문이다. 다시 생성한 순회의 정밀 결과가 지금 최선보다 정밀 순서(`capacityResult.better`)에서 엄밀히 낫지 않으면 반복을 끝낸다. 첫 순회 뒤의 순회는 `c_w`가 낮아진 뒤에만 시작하고(`c_w`는 낮아지기만 한다) 최선을 엄밀히 개선할 때만 이어지므로, 판단의 유한한 계획 집합에서 반복은 끝난다. 각 순회는 첫 순회의 순회 예산, 곧 같은 평가 수와 search 시작부터 마감까지의 시간을 그 순회의 시작부터 받는다. 정밀 funnel은 마감 밖이므로 판단 시간은 순회 수만큼 늘어난다. keep이 충족하고 어느 재생에도 부족이 없으면 판단은 보정 없는 한 번의 순회다. v1 전략은 보정하지 않는다.

알려진 한계. `c_w`는 Workload마다 하나이며 가장 나쁜 계획의 비율이 모든 계획에 적용된다. admission이 처리량 부족과 지연이나 전환 때문의 미충족을 모두 거절로 바꾸므로 Workload 단위 집계로는 둘을 구분하지 못하고, 공급이 수요보다 훨씬 큰 계획의 거절도 `c_w`를 크게 낮춘다. control403~405 상태와 부하 배합 32개 경우의 검증에서 이 한계는 4개 상태에서 더 싼 충족 계획 대신 0.4~5.0 W 큰 계획을 고르게 했고 잘못된 Overload는 만들지 않았다. 구분에는 재생의 instance별 완료나 busy 시간이 필요하다.

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

탐색 벽시계 마감은 순회마다 따로 있다. 첫 순회의 마감은 판단 시작부터 제어 주기 절반이고, 다음 순회는 각자의 시작부터 같은 길이(제어 주기 절반에서 전처리 시간을 뺀 값)를 받는다. 증분 평가 수와 마감은 각 순회의 중단에만 적용하며 정밀 funnel, payback과 투영은 마감 밖이다. 순회 수에는 상한 상수가 없다. 첫 순회 뒤의 각 순회는 직전 순회에서 처음 판정된 계획의 부족이 `c_w`를 낮췄을 때만 시작하므로, 순회 수는 처음 판정되는 계획 수로만 제한된다. 그래서 판단 시간은 제어 주기 절반을 넘을 수 있고 제어 주기도 넘을 수 있다. Tick은 전처리, 가격 반복과 정밀 funnel까지 수행한다. 취소된 계산은 이전 값이나 실패 검사로 대체하지 않는다.

## offline 용량표

`BuildCapacityRow`와 `hack/kappa`의 전체 격자, 이분 탐색과 `NonMonotone`은 명시적 offline 계산이다. online 판단은 행을 쓰지 않고 행 준비를 기다리지 않는다. 창 길이, 창 수와 충족창 수는 offline 도구의 입력이고 최소 요청 수, 격자, 이분 폭과 꼬리 표본은 settings의 capacity 설정이다. 같은 수치 입력의 완료 결과와 불변 raw, random tape는 재사용할 수 있다.

## 기록과 증거 경계

판단에는 settings, 관측 구간, 워크로드별 부하 창 시작과 관측 rate(`inputsPerSecond`)와 계획 rate(`plannedPerSecond`), waiting q, timing, 정수 분배, 선택 사슬, 에너지와 제외 이유를 연결한다. heuristic 결과와 직접 재생 결과를 구분한다. `exactEvaluated`는 keep seed와 replay 생략 판정도 포함한다.

`search`의 출발점, 평가 수, 후보 수, 가지치기, fallback, budget 종료와 시간은 [스케줄러 API](scheduler-api.md)에 둔다. `dualBound`는 가격 반복 진단이다. 전체 평가 구성 목록을 기록하지 않는다.

저장 계약은 [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다. 운영 수용은 [실클러스터 수용 기준](scheduler-operations.md#실클러스터-수용-기준)을 따른다.
