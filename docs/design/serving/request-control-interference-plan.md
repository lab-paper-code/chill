# 요청·관측·제어 간섭 해소 계획

상태: 구현·독립 리뷰·소비자·저장소 검사 완료. main이 공식 배포 후 같은 E5에서 실수용을 확인한다.
목표와 수치 계약은 [문제 정의](path-aware-problem-statement.md), [설계 답안](path-aware-design-answers-draft.md), [구현 계획](path-aware-implementation-plan.md)을 따른다.

## 1. 목표와 기준선

CHILL이 관측·제어를 수행하면서 추론 요청에 만드는 대기를 줄인다. E2E SLO를 충족하는 배치는 에너지로 비교하고, 미충족 배치는 지연 개선으로 비교하는 기존 목적을 유지한다.

완료 판단은 요청의 E2E 결과, 배치 결정·적용, 전체 제어 비용을 함께 사용한다. 계산시간 감소만으로 SLO 개선을 선언하지 않는다.

- 분석 기준: 배포 `500ea262012af508c528230698cfd200b89014bf`, control371의 실제 E5. 후속 `536af77e5aeef86b1201337e8bc4f5cd115de323`은 reader RBAC만 변경했다.
- 구현은 해당 변경을 포함하는 최신 통합 소스에서 별도 브랜치로 시작한다. 앞선 문서의 Before 커밋으로 되돌리지 않는다. 현재 문서를 저장하는 checkout과 구현 기준선은 구분한다.
- 동일 판정의 성능 기준선은 `02b521a0`이다. 전체 모집단 SLO 수정 `190739fb`와 reader RBAC를 포함한다. 원실험 `500ea262`와 비교할 때 SLO 판정 변경 효과를 성능 효과와 구분한다.

## 2. 확인한 문제와 남은 불확실성

| 구분 | 원자료에서 확인한 내용 | 계획에 반영할 판단 |
|---|---|---|
| 요청 경로 | workload IC의 늦은 완료 6,494건: 평균 E2E 1,393ms, 잠금 대기 710ms, 관측 기록 호출 165ms, native 처리 45ms | 요청 공통 잠금과 무거운 관측 작업의 결합을 먼저 해소한다. |
| 관측 잠금 | assignment의 catalog JSON 생성이 `submitMu`를 점유. 다른 Response가 `runtime.mu`를 잡고 `submitMu`를 기다리고, 후속 요청들이 밀림 | 요청·결과·관측의 공통 경계를 수정한다. 특정 endpoint만 우회하지 않는다. |
| 연산 측 개선 한계 | 위 IC 5,938건은 측정된 큐·native 시간을 빼도 1초 초과 | 나머지 구간을 고정한 산술 결과다. 장치 증설·배치 변경만으로 해결될 것으로 가정하지 않는다. |
| 계산 비용 | 부하창 41개 evaluation 모두 10초 초과. CPU 표본에 exact replay, 관측 JSON, Kubernetes List가 함께 큼 | 요청 경계와 독립적으로 capture·수치 계산의 반복 작업을 줄인다. |
| 예산 거절 정황 | IC 오류 545건, PLC 오류 2,157건은 `Sender + POST ≥ lifetime`; native HTTP 교환이 짧음 | 소스의 사전 거절 조건과 일치한다. 원 native 오류 본문이 없어 확정 원인으로 계수하지 않는다. |
| 외부 잔차 | 늦은 IC의 E2E와 서버 span 차이는 평균 448ms | 네트워크·TLS·client 중 특정 원인으로 귀속하지 않는다. |

TOTAL/POST에는 CHILL 내부 대기도 포함된다. 이 대기가 줄면 정상 관측창을 거쳐 후속 판단·신규 요청에 반영돼야 한다. 이미 접수한 요청의 값은 유지한다.

## 3. 변경할 책임 경계

### 3.1 요청과 관측 — 우선 수정

`internal/serving`은 요청 상태·물리 소유·배정·완료를 관리한다. `internal/serving/observation`은 원본의 접수 순서·저장·오류를 관리한다.

목표 동작:

1. Runtime의 공통 잠금에서는 상태 전이와 필요한 값 스냅샷만 처리한다.
2. 관측의 공통 접수 구간은 짧게 유지한다. 큰 catalog/record 직렬화가 이 잠금의 소유 시간을 지배하지 않게 한다.
3. 기존 FIFO와 저장 worker를 재사용한다. 요청마다 새 goroutine·별도 기록 큐를 만들지 않는다.
4. 불변 profile/definition 본문은 소유 경계에서 준비·재사용한다. 매 요청에서 큰 본문을 다시 만들어 동일성을 검사하는 비용을 제거한다.

현재 `freezeBatchLocked`에는 포인터·본문 재사용이 이미 있다. C1에서 이 경로가 실제로 실패하는 조건을 고정한 뒤 보완한다. 캐시를 하나 더 만드는 것으로 시작하지 않는다.

C2는 `RequestEvent`뿐 아니라 `observationErrLocked → Err → submitMu`도 포함해 공통 잠금 경로를 확인한다. 잠금 밖 final admission 동안 Drain 완료를 막는 기존 `closing/finalizing` 계약을 유지한다.

**기록의 정확성은 다음 소비자 동작으로 보존한다.**

- assignment가 기존 관측 admission을 통과한 뒤에만 native 실행을 시작한다.
- 한 요청의 단계 결합, assignment/final, catalog 참조, 시간별 archive, 늦은 native 보충의 의미를 유지한다.
- raw admission과 디스크 영속 완료를 구분한다. 직렬화 위치를 옮기면서 기존 오류를 성공 응답 뒤로 숨기지 않는다.
- 로그 장애·종료·Flush 시 기존 실패 전달과 정상 drain을 유지한다. 큐를 무제한 늘리거나 기록을 버려 지연을 낮추지 않는다.

### 3.2 Kubernetes 관측 — capture 비용

`serving/kube`가 필요한 물리 사실과 최신성·소유권을 책임진다. Tick 수치 코드가 Kubernetes를 직접 조회하지 않는다.

기존 watch·Observer가 가진 사실의 재사용 범위부터 확인한다. 동일 관측 주기의 중복 목록 조회·decode를 제거하고, 필요한 원문 수집과 제어용 projection을 구분한다. 실제 표본의 큰 Pod List decode를 첫 대상으로 삼는다.

현재 `OnCluster`가 있으면 terminal Pod까지 포함하는 경로가 있다. 조회량을 줄일 때 terminal 컨테이너·옛 executor·관측 공백·Node 소유 확인에 필요한 사실을 누락하지 않는다. 기존 watch가 그 사실을 제공하지 못하면 해당 권위 조회를 유지하고 중복 소비부터 줄인다.

### 3.3 수치 계산 — 동일한 판단을 더 적은 작업으로

`firstfloor`는 service 분포·batch 선택의 계산을, Serving adapter는 후보 평가·replay를, v1/v2는 탐색을 소유한다.

- 같은 quantile 계산에서 trial별 nearest 선택을 반복하지 않도록 정리한다.
- 한 batch 선택 안에서 실제로 같은 분포·분위수·gap을 사용하는 조회를 재사용한다. 요청별 남은 시간과 여유는 별도로 계산한다.
- 기존 Tick-local replay memo를 재사용한다. 같은 평가를 중복 요청하는 호출자가 있으면 그 호출 경계를 정리한다.
- 결과 수명은 분포 또는 해당 평가 호출에 묶는다. 관측값별 전역 캐시를 축적하지 않는다.

표본, trial 가중치, seed, 동률 처리, horizon, SLO 분위수, 실패 모집단, 후보 순위·에너지 식은 유지한다. 성능 변경으로 v1/v2의 선택 의미가 바뀌지 않도록 같은 입력의 소비 결과를 대조한다.

## 4. 작업 단위와 의존성

각 단위는 한 동작·회귀 확인·관련 문서 갱신을 한 커밋으로 묶는다. 구현자는 착수 전에 아래 범위에서 실제 변경 파일을 고정한다.

| 단위 | 변경과 담당 경계 | 완료 확인 |
|---|---|---|
| C1 불변 관측 본문 | observation의 catalog 준비·재사용. `observation_log.go`, `observation_log_requests.go`와 해당 소유 경계 | 동일 본문의 반복 접수가 큰 직렬화를 반복하지 않고, 본문 변경·시간 경계·Flush 후에도 실제 raw와 catalog를 정상 조회함 |
| C2 요청·관측 잠금 분리 | Runtime 이벤트 전달과 observation 접수. `runtime.go`, observation 요청·scheduler 기록 경계 | 무거운 기록 작업과 병행해 다른 요청이 접수·실행·완료됨. 같은 요청의 원문·실패·종료 순서와 admission 오류 의미 보존 |
| C3 capture 중복 제거 | `kube/observer.go`, `scheduler_application_capture.go`의 사실 수집·공유 | 동일 관측 주기에서 공유 가능한 목록의 중복 조회·decode를 제거. 필요한 권위 조회·최신성 및 변경·terminal·watch 재연결·조회 실패의 반영 유지 |
| C4 service 조회 경량화 | `firstfloor/service_distribution.go`, `policy.go` | 같은 실제 분포·gap·분위수에서 quantile과 batch 선택 동일. 경계·동률·trial 가중치 보존, 할당·연산 감소 |
| C5 후보 평가 중복 정리 | 기존 `feedbackCache`·`sync.Once` 재사용 | 실제 후보 소비 경로에 추가 중복이 없어 생산 코드 변경 없이 닫았다. keep·target·v1/v2 소비 회귀로 기존 수치 의미를 확인한다. |
| C6 통합과 수용 자료 | 통합 owner: 요청→관측→Tick→신규 접수 연결, 기존 문서·호출자 정리 | 아래 5·6절의 소비자·품질·실측 기준을 만족하는 정확 source와 실행 자료 완성 |

의존성: `C1 → C2`, `C4 → C5`, `C2 + C3 + C5 → C6`.
C1, C3, C4는 독립 진행한다. C2와 C5는 각각 선행 계약이 고정되면 시작한다.

advisor-2가 통합과 공유 계약을 소유한다. observation, kube capture, firstfloor/replay는 서로 다른 작업자로 나눈다. Runtime·공통 protocol 같은 공유 파일은 한 명만 편집한다. 새 파일은 catalog 준비, 관측 접수, 수치 조회처럼 책임별로 나누고 고정 줄 수 제한은 두지 않는다.

C1/C4의 재사용 변경만으로 충분한지 먼저 확인한다. 후속 단위는 남은 간섭·중복에 한해 구현하고, 이미 소비자 목표를 달성했다면 그 근거로 닫는다. 효과 없는 캐시나 추가 구조를 남기지 않는다.

## 5. 로컬 검증과 코드 품질

### 대표 반례

- raw의 IC 요청 `67489d62dc6d0e7e7da608f29f2687dc`: native 10.4ms, 잠금 567.6ms, 기록 호출 301.4ms.
- 같은 node·POST에서 성공과 실패가 갈린 요청 쌍: `representative-pairs.json`.
- 느린 catalog 준비와 동시에 접수·응답·native 보충을 실행하는 소비자 시나리오.
- 기존 조회·저장 오류 및 정상 종료가 요청 상태와 원문에 반영되는 시나리오.

실제 Runtime·ObservationLog·HTTP 소비 경로를 사용한다. 동시성 회귀는 제어 가능한 동기화 지점으로 검사하고 호스트 속도에 의존하는 sleep 단언을 피한다. 영구 테스트는 순서·누락·오류 전달·선택 결과를 검사한다. CPU·할당·wall time 비교는 별도 재현 실행으로 남긴다.

### 수치·제어 확인

같은 raw 입력, profile, 설정, seed와 판정 기준으로 기준선과 개선본을 비교한다. keep와 선택 후보의 평가, κ, 실패 처리·분위수, v1/v2 선택·후처리를 확인한다. 전체 모집단 분위수 판정을 통합할 경우 양쪽에 동일 적용한다. 시간 마감으로 탐색 범위가 달라진 경우는 수치 불일치와 분리하고 기록한다.

기존 `EvaluationElapsed`는 capture 시작부터 수치 평가 종료까지다. 전체 Tick은 이후 결정 기록·실행 전달까지 포함해 검증 실행기의 호출 전후로 측정한다. native의 장시간 물리 작업 완료를 Tick 시간에 더하지 않는다.

현재 planner deadline은 전체 Tick 상한이 아니다. 이번에는 반복 작업 제거로 비용을 줄인다. 정확 평가를 생략하거나 새 timeout으로 잘라 통과시키지 않는다. 그래도 ControlInterval을 지속 초과하면 계산 예산·탐색 변경을 별도 설계 판단으로 올리고 성능 수용을 보류한다.

### 최종 품질 검토

- **동시성·기록:** 긴 임계구간, lock 순서, mutable 값의 비동기 전달, 종료·오류 경계.
- **메모리·경량화 별도 리뷰:** profile clone, 직렬화 bytes, 큐와 참조의 보유 수명, 관측값 수에 비례해 늘어나는 메모리, 중복 quantile/replay.
- **책임·최소성:** Runtime/observation/kube/수치 계산의 경계, 거대 파일, 중복 상태·설정·우회 경로. 새로운 SHA/digest·encoding equality·불필요한 거절 게이트를 추가하지 않는다.

독립 리뷰어가 위 세 범위를 검토하고 수정 사항을 반영한다. 영향 패키지·동시성 검사를 먼저 수행하고, 통합본에서 저장소의 `make lint`, `make test`, 해당 architecture/gopls 검사를 한 번 수행한다. 생성물·Helm·UI 검사는 실제 변경 범위에 해당할 때 수행한다. 실패·skip·재사용 근거는 별도로 기록한다.

## 6. 배포와 실수용

코드·로컬 검증·독립 리뷰 완료 후 main에 정확 source, 변경 역할, 기존 설정 보존 내용, 검사 결과와 아래 실수용 항목을 한 묶음으로 인계한다. 배포·실장비 실행은 main이 기존 공식 경로로 수행한다.

1. 기존 측정·요청의 정상 경계를 확보하고 배포한다. raw/profile 및 공급·설정을 보존한다.
2. 기존과 같은 E5 입력·부하·정책·하드웨어 조건을 사용한다. 다른 조건이 있으면 차이를 기록한다.
3. IC/PLC의 기한 내 성공, 늦은 완료, HTTP 오류, timeout을 offered 전체 기준으로 비교한다. 사용자 요청인 지정 SLO 분위수 판정의 코드 통합을 별도로 확인한다. 성능 변경에 판정 기준 변경을 섞지 않는다.
4. 잠금·기록 호출·native·서버 밖 구간을 같은 cohort로 분해한다. 큐·CPU·heap·GC와 capture/평가/전체 Tick 비용도 함께 확인한다.
5. 관측 변화→선택 배치→실제 적용→신규 요청 POST를 연결하고, 기존 요청의 동결값이 유지되는지 확인한다. 에너지는 같은 범위·단위로 비교한다.

**수용 기준:** 대표 원문에서 확인한 요청 간 막힘을 재현 소비자에서 해소하고, 실부하의 해당 대기를 줄인다. 대표 입력과 실부하에서 전체 Tick이 ControlInterval 안에 수행되는지 확인하며 초과를 평균으로 숨기지 않는다. 기록·공급·수치 의미를 유지하고, SLO 결과와 남은 실패 원인을 함께 판정한다.

SLO가 계속 미달하면 이번 변경의 성능 효과와 전체 목표 미달을 분리한다. 남은 서버 밖 지연, 예산 거절, 모델 예측 차이를 새 자료로 좁힌다. 관측 누락을 성공으로 바꾸거나 새로운 기능을 덧붙여 원인을 흐리지 않는다.

## 7. 근거와 산출물

- 원실험: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/control-371-path-feedback-20261109/`.
- 원자료 분석: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/path-feedback-raw-dig-20261110/`의 `findings.json`, `request-decomposition.json`, `independent-request-spots.json`, `representative-pairs.json`, `scheduler_timeline.py`, `scheduler-timeline.json`.
- 주요 소스 근거: `runtime.go:456–467,607–628`; `observation_log_requests.go:17–65`; `observation_log.go:423–477`; `kube/observer.go:228–258`; `kube/scheduler.go:273–305`; `scheduler/v2/search.go:12–38`. 경로·행은 `500ea262` 기준이다.
- 최종 산출물: 최소 변경 커밋 묶음, 기준선/개선본의 재현 결과, 독립 품질 리뷰, main용 배포·실수용 인계. 원실험 자료는 유지한다.

### 구현 경계

- `observation_log_prepare.go`: 큰 catalog와 scheduler 본문을 동기식으로 준비한 뒤 기존 FIFO에 접수한다. 같은 불변 본문의 새 source를 채택해 반복 직렬화를 줄인다. 디스크 catalog는 source 객체를 보유하지 않는다.
- `observation_log_requests.go`: 잠금 밖 준비 후 요청 상태를 다시 확인한다. assignment admission, native 실행 순서와 finalizing/drain을 유지한다. Runtime의 상태 전이 코드는 변경하지 않았다.
- `Observer.refresh`: 이번 호출의 전체 Pod 목록을 capture에 전달한다. terminal·exact executor 확인을 유지하면서 뒤따르던 중복 nonterminal 목록 조회를 제거한다.
- `ServiceDistribution`·`ChooseBatch`: 한 quantile의 trial 이웃을 재사용하고, 한 batch 선택의 동일 query만 재사용한다. 요청별 남은 시간·POST와 v1/v2 탐색·순위는 그대로다.

기존 큐와 catalog의 보유 정책은 유지한다. 동시 cold 준비에는 일시적 본문 중복이 가능하며, 모든 직렬화·잠금 비용이 제거된 것은 아니다. 전체 Tick·실부하 heap·GC 수용은 로컬 수치 회귀와 별도로 판단한다.

### 로컬 검증

- 실제 Runtime/ObservationLog의 병행 요청·원문 순서·오류·drain 소비자와 race 검사 통과.
- 보존된 112 profile/508 batch의 quantile 경계와 startup/busy batch 선택 일치. v1/v2 fixture의 선택 key·route·J·feasible·final 일치.
- frozen `b221b128`의 `make test` 통과. lint·architecture·pinned gopls 통과. 기존 SmolVLM preprocessor skip 3건 유지.
- 최종 benchmark-only 변경은 영향 범위에서 별도 확인했다. 생산 코드가 같은 frozen full gate를 재사용한다.

재현 benchmark는 `BenchmarkRetainedServiceSamples`, `BenchmarkNumericalPlannerConsumers`, `BenchmarkRequestCatalogRepublication`, `BenchmarkRuntimeObservationInterference`다. 실제 분포 입력은 `CHILL_NUMERICAL_PROFILES`로 지정한다. `BenchmarkRetainedServiceSamples`의 한 op는 전체 표본 묶음 순회이며, planner fixture·관측 소비자 비용을 실클러스터 전체 Tick으로 확대하지 않는다.

근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/request-control-interference-20261110/verification.md`. main은 `EvaluationElapsed`와 전체 Tick을 구분하고, 호출 전체 span이 없으면 그 수용 항목을 미검증으로 남긴다.
