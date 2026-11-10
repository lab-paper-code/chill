# 제어 비용·관측 판정·배치 적용 후속 해결 계획

상태: A1·A2·C1 구현 및 독립 리뷰 진행. 로컬 소비자 검증 후 통합 gate와 main의 배포·실수용을 수행한다.
기준: `036e00a54442b085121f6fc7464b9c37042d8e74`, Helm230, control373의 동일 E5 두 stage.
선행: [요청·관측·제어 간섭 해소](request-control-interference-plan.md). 완료한 C1~C5는 유지하고 남은 경계만 다룬다.

## 1. 목표와 우선순위

CHILL이 요청 처리를 방해하지 않으면서 제어 주기 안에 배치를 판단·전달하고, 실제 적용과 SLO 결과를 정확히 설명하게 한다.

1. **제어 비용:** exact 재생의 조건부 분위수 계산을 먼저 경량화한다. 전체 제어 호출과 후처리 비용을 분리해 개선량을 확인한다.
2. **배치 적용:** executor가 없는 종료 Pod에 stop/drain을 만드는 공통 계획 경계를 수정한다.
3. **공식 관측 판정:** 현행 gap 종료 규칙을 유지하며, 다음 실측 전에 정상 공급 교대·종료로 남은 executor 영향을 닫는다.

수치 경량화와 Pod 정리 수정은 독립 진행한다. 관측 판정에 대한 생산 코드 수정은 이번 근거로 필요하지 않다. SLO 분위수·표본·실패 모집단·에너지 목적은 유지한다.

## 2. 출발점: 사실과 미확정

근거 루트 `P`: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/control-373-request-control-interference-20261110/`.

| 영역 | 확인한 사실 | 아직 판단할 수 없는 것 |
|---|---|---|
| 요청 처리 | offered 78,486건 모두 기한 내 완료, 오류·timeout·late 0. 같은 적격 조건의 잠금 대기 중앙값 약 195/208ms에서 0.000824/0.000822ms로 감소 | 변경 전체의 단독 인과 효과, 다른 부하에서도 같은 개선량 |
| 제어 시간 | capture+evaluation 중앙값 16.117/15.981초, 부하창 88개 decision 중 87개가 10초 초과 | 전체 control round/Tick 시간과 search 밖 비용의 배타적 wall-time 분해 |
| 계산 원인 | 30.11초 CPU 표본에서 v2 Search 11.32 CPU초 중 exact Funnel 10.12초, surrogate Candidates 0.85초 | 최적화 후 전체 Tick 단축량 |
| 자원 | warm catalog hit도 원시 ServiceSamples를 깊게 복사. 분포는 hydration 시 생성·재사용 | 반복 복사가 전체 지연에 미친 비율, 누수 여부 |
| 공식 판정 | 3tuple 모두 동일 containerID로 측정 전후 Running/restart0. CLEAR는 종료 증거가 아니며 exact termination까지 gap 유지 | 최초 predecessor가 unclean이 된 사유. 현재 숨은 요청이 실제 존재했는지 |
| PLC 실행 | lattepanda의 종료 Pod-only 신원에 stop을 보내 거절. monitor에서 서로 다른 stop 31건이 같은 오류 | 모든 원시 decision의 양의 target 전수 분류와 수정 후 물리 전환 결과 |

119개 monitor를 직접 집계했다. 부하창의 서로 다른 decision 38/40개에서 같은 decision의 `EvaluationElapsed − search.duration` 중앙값은 8.265/8.228초다. capture·평가 준비·후처리가 섞인 잔여이며 capture 시간으로 명명하지 않는다. 이 표본을 완전한 native 43/45개 통계로 대체하지 않는다.

## 3. 작업 A — exact 재생 경량화와 전체 비용 확인

### A1. 확인된 호출·시간 경계

소스 경로는 기준 커밋의 `internal/` 아래다.

| 경계 | 소스 | 조사 결과 |
|---|---|---|
| 실제 제어 주기 | `serving/application/scheduler_service.go:394–408` | 시작 시각 → application lock → Admit → Tick → unlock. 남은 주기만 대기하므로 10초가 실행시간의 강제 상한은 아님 |
| Tick 계측 | `serving/kube/scheduler.go:264–313` | EvaluationElapsed는 capture+Evaluate만 포함. lock 대기·Admit·publish·기록·launch는 별도 |
| search와 후처리 | `serving/scheduler_capacity.go:183–258` | Evaluate 시작+ControlInterval/2를 search에 전달. 반환 뒤 payback/minimumGain·nominal-rate 진단·decision 구성 |
| v2 deadline 범위 | `scheduler/v2/search.go:12–50` | surrogate 탐색 뒤 exact Funnel·fallback 수행. BudgetCut은 surrogate 종료 사유이며 전체 search/Tick 상한을 증명하지 않음 |
| 기존 replay 재사용 | `serving/scheduler_capacity_evaluate.go:267–290` | Tick-local feedbackCache와 sync.Once가 같은 후보의 exact replay를 이미 공유 |

CPU 원문을 `go tool pprof -peek`로 다시 분석했다. exact Funnel 10.12 CPU초가 Search 11.32 CPU초의 대부분이다. quantile 6.24 CPU초는 replay 내부에 포함된다. search 뒤 payback 1.53초·nominal-rate 진단 1.37초, capture 3.47초도 관측됐다. 중첩 CPU 표본을 합산하거나 Tick wall time으로 환산하지 않는다.

500,000은 surrogate 평가 수, 16은 해당 대표 decision의 exact finalist 수다. 약 418은 노드별 plan 개수, 약 2,600은 관측 compute arrival 수다. 서로 다른 카운터를 같은 종류의 평가 횟수로 비교하지 않는다.

### A2. 우선 구현: 조건부 분위수의 탐색 대상 축소

`firstfloor/service_distribution.go:211–247`은 실제 CDF 질량을 선택된 nearest-gap 그룹에서 계산하면서, 탐색 후보는 전체 분포의 `d.services`를 사용한다. 현재 질량이 없는 다른 gap의 service 값까지 binary search 후보에 포함된다.

**구현:** 선택된 nearest 그룹의 support가 작으면 해당 값만 후보로 같은 inverse CDF를 계산한다. 큰 support는 기존 전체 후보 탐색을 유지한다. physical trial별 질량과 CDF 계산은 동일하다.

- physical trial 가중치, 정수 거리 동률의 두 그룹, float 누산 순서와 비교식을 유지한다.
- startup·빈 support·분위수 0/100·중복값·큰 그룹을 같은 함수에서 처리한다.
- 작은 선택 집합은 호출 로컬 저장공간을 사용한다. 원시 표본과 immutable 분포는 유지하고 전역 query cache를 추가하지 않는다.
- 큰 선택 집합은 기존 탐색을 재사용한다. 별도 그룹별 탐색은 비용 비교에서 불리해 제거했다.

보존된 112profile/508batch를 실제 Go 소비자로 비교했다. 12,700query의 분위수·support·유효 여부 차이는 0이었다. 5회 benchmark 중앙값에서 전체 quantile은 기준의 0.675배, batch 선택은 1.070배, v1/v2 평가 fixture는 0.980/0.939배였다. 모든 경로의 가속이나 실제 Tick 10초 충족을 뜻하지 않는다.

### A3. 후순위로 좁힌 비용

- `serving/kube/catalog.go:305–307,531–535`: warm cache hit의 깊은 복사는 공개 mutable 반환을 보호한다. 이번에는 유지한다. 독립 메모리 리뷰에서도 소유권을 바꿀 근거가 부족하다고 판단했다.
- `serving/scheduler_capacity_evaluate.go:262–278`: actual-keep의 energy/candidate 구성은 memo 조회 전에 반복될 수 있다. 기존 평가 수명 내 결과 전달로 줄일 수 있으나 지배적인 병목이라는 근거는 없다. A2 이후 남은 비용이 확인될 때만 수정한다.
- `serving/scheduler_capacity_decision.go:137–194`: nominal-rate 검사는 유한 arrival cohort exact replay와 다른 진단이다. keep/selected의 같은 query는 이미 공유한다. 이 검사를 제거하거나 후보 거절 게이트로 바꾸지 않는다.
- capture의 필수 List·LoadWindow와 observation 기록 비용은 전체 시간 분해에서 확인한다. 선행 C1~C5를 다시 구현하거나 두 번째 replay cache를 만들지 않는다.

### A4. 전체 시간 수용

상위 control round와 Tick 호출 전후를 각각 측정한다. capture·입력 준비·search·payback·nominal-rate 진단·기록·전달을 같은 호출에 연결한다. Keep·fallback·오류·중도 반환도 분모에 포함한다. 비동기 영속 완료·물리 전환 완료는 별도로 둔다.

기존 진단 경로에 짧은 시간·개수만 보충하고 큰 상태를 다시 직렬화하지 않는다. 의미 보존 최적화 후에도 10초를 지속 초과하면 남은 지배 비용과 선택 품질의 교환관계를 제시한다. 탐색 범위·예산·근사·제어 주기 변경은 그 근거를 바탕으로 별도 결정한다.

## 4. 작업 B — 관측 차단은 현행 계약과 운영 경계로 해결

**조사 결론:** clearAt 처리 누락이나 닫힌 gap 재개방은 확인되지 않았다. 세 executor는 측정 후에도 같은 Pod/container로 Running이었다.

- `serving/observation/observation_log_run.go:104–173,214–224`: CLEAR는 `ClearAt`만 기록한다. 정확한 executor의 `Terminated`가 들어와야 `To`를 기록하고 `Open=false`로 만든다.
- 같은 파일 `:227–249`: 열린 영향을 각 hour에 잘라 투영한다. 21시 gap은 새 장애 생성 시각을 뜻하지 않는다.
- `hack/evaluation/raw.py:53–58,248`: 열린 영향과 측정 구간의 교차를 소비한다. 이 때문에 이번 공식 판정이 `NOT_EVALUATED`가 됐다.

이 규칙은 actor가 비어도 이전 socket의 미관측 요청 가능성을 종료로 간주하지 않는 기존 계약이다. 현재 숨은 요청이 실제 존재했다는 증거나 현재 offered 요청의 SLO 실패를 뜻하지는 않는다.

**운영 해소:** main이 후속 실측의 공급을 유지하며 기존 정상 unroute/drain/remove·대체 준비 경로로 대상 executor를 교대한다. exact 종료·gap의 유한 종료 시각·새 공급 Ready/route를 확인한 뒤 새 측정창을 연다. 이 조사는 live 교대나 추가 실험을 실행하지 않았다.

보존된 control371에는 같은 jetsono executor의 종료 시각 16:13:58과 요청 시작 16:26:07이 있다. 현재 raw 소비자를 이 원문에 실행해 닫힌 3개 boot-root가 요청창과 겹치지 않음을 확인했다. 따라서 같은 hour의 과거 gap 존재만으로 차단되는 구조는 아니다.

생산 코드 수정 커밋은 만들지 않는다. 향후 정상 종료 후에도 gap이 열린 반례가 생길 때만 Observer 종료 증거 → Runtime 전달 → ledger 저장의 깨진 경계를 수정한다. 기존 published 결과와 raw는 보존한다. CLEAR만으로 종료시키는 정책 변경은 별도 결정이다.

## 5. 작업 C — 종료 Pod-only 재고의 정상 remove 연결

### 확정 원인

`P/evaluation-monitor/000/summary.json`의 `plan-1770fe6328c8`은 PLC를 lattepanda로 weight1000 배치하려 했다. 준비 전에 기존 Pod `fa69342d-6690-4d29-b593-d29907f2ea94`의 stop→drain→remove가 선행한다.

첫 stop은 20:58:17.603873118에 시작해 20:58:18.754457833에 `exact operation Pod/executor identity required`로 끝났다. monitor 119개를 operation ID로 중복 제거하니 같은 Pod의 서로 다른 stop 31건이 모두 같은 오류였다.

대상 Pod는 Failed·삭제 요청·source finalizer 보유 상태이고 containerID가 없다. snapshot도 `ID=PodUID`, `terminated=true`, `stopped=true`다. 이미 종료한 Pod-only 재고를 실행 중 executor처럼 계획한 것이 원인이다.

| 경계 | 소스 | 문제·기존 해결 경로 |
|---|---|---|
| 정리 DAG | `serving/scheduler_plan.go:722–758` | preparation blocker와 retireOld가 무조건 stop/drain/remove 생성 |
| Node 변경의 형제 경로 | 같은 파일 `:542–608` | 같은 재고에도 stop/drain을 생성 |
| operation 변환 | `serving/kube/scheduler_operations_launch.go:403–405` | action.Instance를 그대로 전달하고 Pod-only remove로 변환하지 않음 |
| 정상 primitive | `serving/operations/operations.go:310–329`, `operations_lifecycle.go:51–125` | 기존 remove는 `Instance=\"\"`, 정확한 `PodUID`를 지원하며 Node·물리 상태를 검증 |

### 최소 수정

1. 공통 planner의 세 정리 경로에서 검증된 종료 Pod-only 재고는 stop/drain 없이 remove로 연결한다. 실행 중 executor의 기존 체인은 유지한다.
2. kube adapter는 snapshot의 Node·PodUID·종료 사실을 사용해 해당 remove만 기존 Pod-only operation으로 변환한다. 문자열 모양만으로 Pod UID를 추측하지 않는다.
3. 제거 완료 뒤에만 allocation·finalizer·reservation 반환과 후속 prepare/route를 진행한다. 기존 operations 검증기는 유지한다.

소비자 회귀는 종료 Pod-only → 정상 remove → prepare/route 진행, 살아 있는 executor의 drain 유지, 다른 Pod/Node UID 거부, 미확정 executor의 보수적 취급, 전력 변경의 같은 정리 경로를 포함한다. 수동 Pod 삭제로 이 결함을 숨기지 않는다.

### 선택 집계 정정

rasp5·jetsonx가 `target.instances`에 있는 것만으로 양의 PLC 목적지로 선택된 것은 아니다. 대표 원문에서는 두 인스턴스가 비라우팅 재고이고 PLC route는 jetsono1000이다. 실제 선택은 `target.routing[PLC_UID][instanceID] > 0`으로 판별한다.

lattepanda의 선행 전환 실패와 후속 반복 정리는 확인했다. 모든 비-jetsono inventory를 전환 실패로 세지 않는다. 부하창의 PLC 20,620건은 jetsono에서 실행됐으며, 각 신규 접수는 해당 시점의 적용 route와 연결해 검증한다.

## 6. 최소 커밋과 의존성

조사는 완료했다. 아래는 확인된 경계에 대한 구현 단위다. 한 커밋은 한 책임 변경과 소비자 회귀·관련 문서를 포함한다.

| 단위 | 소유 범위 | 선행 | 완료 산출물 |
|---|---|---|---|
| A1 전체 시간 측정 | application round·kube Tick의 기존 진단 경계 | 없음 | 오류 포함 호출 분모와 search/후처리/기록·전달 분해 |
| A2 exact quantile 경량화 | firstfloor 분포 수치 담당 | 없음 | 선택 support 기반 동일 inverse CDF, 실제 분포·replay 전후 수치와 비용 |
| C1 종료 재고 정리 | 공통 planner·kube operation adapter 단일 owner | 없음 | 기존 Pod-only remove 연결, 세 정리 경로의 동일 소비자 효과 |
| A3 입력 복사·actual-keep 비용 | catalog 또는 adapter의 분리 owner | A1+A2 후 남은 비용 확인 | 필요한 변경만 구현, mutable 소유권·기존 memo 유지 |
| D 통합 | advisor-2 | 필요한 A/C 결과 | 독립 리뷰, 누적 gate, 정확 source·main용 실수용 묶음 |
| B 운영 교대 | main | 정상 공급·정리 경로 확보 | 기존 3tuple 종료 증거·새 공급·새 측정창 확인 |

A1·A2·C1은 파일 소유를 고정해 병렬 진행한다. 공통 protocol·Runtime·adapter 파일은 한 명만 편집한다. B는 관측 정책 변경을 요구하지 않으며 성능 구현의 선행 조건도 아니다. 최종 공식 평가에서는 운영 준비 결과를 반영한다.

책임별 파일 분리를 유지하고 고정 줄 수 제한은 두지 않는다. 불필요해진 임시 우회·중복 구현은 같은 변경에서 제거한다.

## 7. 검증과 최종 수용

### 로컬 검증

- 성능 기준선은 `036e00a5`. 같은 profile·raw·설정·seed·판정 정책에서 전후 비교한다.
- A의 수치 경량화는 v1/v2 평가·후보 순위·선택·routing을 보존한다. 벽시계 마감에 따른 탐색 범위 차이는 수치 오류와 분리한다. C는 막혔던 정리·배치 전환이 정상 진행되는 효과를 확인한다.
- 기존 112profile/508batch 비교와 요청 기록·동시성 회귀를 재사용한다. 실클러스터의 profile 136개와 분모를 혼동하지 않는다.
- 시간·allocation 비교는 재현 실행으로, 소비 의미는 결정적인 회귀로 검증한다. 호스트 속도나 내부 poll 횟수를 영구 단언으로 고정하지 않는다.
- 독립 리뷰는 책임 분리·불필요한 검증/거절, 동시성·기록, **메모리 보유·누수 및 연산 경량화**를 분리해 실시한다.
- 영향 검사 후 통합본에 `make lint`, `make test`, 해당 architecture/gopls gate를 수행한다. 실패·skip·재사용 근거는 구분한다.

### main의 실수용

정확 source·변경 역할·검증 근거를 한 묶음으로 전달한다. 공식 배포와 B의 정상 운영 준비 후 control373과 같은 두 정책의 E5를 실행한다. 교대한 executor·하드웨어·실제 profile·공급 조건의 차이를 비교에 기록한다.
CPU·heap은 같은 stage 상대 시점·표본 길이·GC 조건으로 비교한다. historical scheduler 조회 비용은 제어 Tick과 별도로 분류한다. 성능 기준은 전체 control round와 Tick을 함께 보고한다.

| 수용 항목 | 확인 기준 |
|---|---|
| 요청·SLO | offered 전체의 on-time/late/error/timeout과 설정된 분위수. 이번 100%는 비교 관측값이며 새 SLO 규칙으로 강제하지 않음 |
| 제어 비용 | 전체 Tick 호출 전후, 단계별 시간, ControlInterval 초과 횟수·최대값. 대표 입력과 실부하에서 10초 이내 여부 확인; 지속 초과면 성능 수용 보류 |
| 공식 품질 | 구간에 실제 적용되는 근거로 판정. 정당한 불확실성은 미평가로 유지하고 별도 차단으로 보고 |
| 적용·동결 | 선택→적용 결과→신규 접수/native 연결. 기존 요청의 POST·신원 동결 유지 |
| 기록·자원 | assigned/final·native·archive 연결, 정상 drain/cleanup, heap·working set·GC·queue·재시작 확인 |
| 비교 범위 | 같은 cohort와 에너지 범위 사용. 계산 가속·배치 변화·executor 교대에 따른 관측 준비 차이를 구분 |

요청 경로 개선, 10초 제어 수용, 공식 평가 가능 여부, 실제 배치 적용을 각각 판정한다. 하나의 PASS로 나머지를 대신하지 않는다.

## 8. 조사 근거와 남은 검증

조사 산출물 `I`: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/control-loop-root-investigation-20261110/`.

- `I/control-boundary.json`: gopls로 확인한 상위 round/Admit/Tick 호출 경계.
- `I/cost-findings.json`: 기존 CPU profile의 `pprof -peek` 결과와 정확한 해석 범위.
- `I/conditional-support-probe.json`: 실제 PLC 6batch·270query의 후보 수치 방식 비교. Go 구현·성능 검증은 아님.
- `I/monitor-joined-evidence.json`: 119개 monitor, 같은 decision의 시간 차, 31개 stop 실패, 양의 routing과 원문 경로.
- `I/gap-consumer-evidence.json`: 전후 Pod/container 신원과 기존 raw 소비자의 닫힌 구간 처리.
- `P/final-report.md`, `complete-native-control-cost.json`, `cpu-during-e5.pb.gz`, `observation-quality-blockers.json`: 원 실측·프로파일·판정 보존.

세 독립 조사 결과를 소스 원문으로 검토했고, 부모가 gopls 정의/참조를 확인했다. pinned gopls는 v0.23.0이다. 일부 scout에는 실행/LSP 도구가 없어 해당 부분은 부모가 수행했다.

구현 근거는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/control-loop-implementation-20261110/`에 보존한다. `standalone-consumer-comparison.json`, `numeric-final-comparison.json`, `numeric-review.json`, `continuation-consumer.json`은 각각 실제 수치 소비·비용·독립 리뷰·자동 Continue에 의한 remove→prepare/route→예약 반환을 확인한다.

남은 실수용은 전체 round/Tick wall time, 정상 정리 뒤 물리 전환, executor 교대 뒤 공식 품질이다. 최초 unclean predecessor의 발생 원인은 보존된 run 파일이 없어 확정하지 않았다. 기존 raw와 published 결과는 유지한다.
