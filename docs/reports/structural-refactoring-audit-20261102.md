# CHILL 구조적 리팩토링 후보 조사·수정 세션 인수인계

## 1. 목적·상태·기준점

- 조사일: 2026-11-02. **조사 완료, 수정 미실행. CPU best-effort 공유 실행과 Query API로의 조회 책임 분리는 사용자의 핵심 방향이며, R1–R8은 그 방향 아래 검토할 구조 개선 후보**다. 이 문서는 구현·배포·live 검증 완료를 의미하지 않는다.
- 사용자 요청: CHILL을 전반적으로 살펴 장기 유지보수·확장성을 떨어뜨리는 임시 덧대기 의심 코드/커밋, Config·상수·특정 case에 의존하지만 구조적으로 개선 가능한 지점을 탐색하고 도구를 적극 활용한다.
- 전달 목적: 다른 수정 담당 세션이 조사 의도·근거·불확실성·보존 계약을 잃지 않고 작업 범위를 정할 수 있게 한다. 요청은 문서화이며 코드 수정·배포·외부 세션 연락은 포함하지 않는다.
- 조사 시작 HEAD: `ce9495ea`; 조사 중 `29fe9046`이 추가됐다. 문서화 시 확인한 HEAD: `29fe90467f0e22b734e33ba3ea9b796f79a66d0c` (`test(evaluation): assert summary reads without fake response limits`). 작업 트리가 동시에 변경되고 있으므로 아래는 해당 조사 시점의 관측이다.
- 초기 사용자 변경: `internal/evaluation/controller/scheduler_summary_test.go`, 미추적 `kappa`. 문서화 직전에는 §14에 적은 다른 변경이 관측됐다. 조사자가 사용자 변경을 수정·되돌리거나 `kappa`를 실행하지 않았다.
- 이 보고서는 [기존 구조 검토](structure-review-20261026.md)의 K1–K5 전체를 재판정하거나 대체하지 않는다. 기존 보고서의 미해결 항목을 이번 실행 검증 결과로 읽지 않는다.

### Core — 모든 연산의 CPU best-effort 공유 실행

CHILL의 모든 연산은 **CPU를 예약·제한·고정하지 않는 best-effort 공유 실행**을 기본으로 한다. Profiling·serving·부하 생성·관측 등 실행 경로 전반에서 장비별 CPU 할당량을 지정하는 구조를 제거한다. CPU 동결표 유지나 allocatable 기반 동적 할당표로의 대체가 목표가 아니다.

- **제거 대상:** device-mode CPU 동결표, CPU request, CPU quota/limit, 명시적 CPU affinity/pinning, 독점 CPU 코어 배정과 그 전제의 matching·admission·검증. CPU 생략을 양수 기본 request·임의 quota·전체 코어 mask로 채우는 fallback도 두지 않는다.
- **분리 대상:** runtime thread 수는 CPU 자원 보장과 다른 실행 병렬성이다. quota/할당 코어 수와의 결합을 제거하고 runtime의 기본·자동 병렬성 정책을 기준으로 실행한다. `threads=1` 같은 새 일괄 하드코딩으로 치환하지 않는다. 실제 적용 정책·thread 설정은 기록한다.
- **유지 대상:** 실제 cpuset/affinity, cgroup quota·throttling, CPU 사용량과 관측 가능한 경합 조건은 실행 사실로 기록한다. CPU를 통제하지 않는 것과 실행 조건을 기록하지 않는 것은 다르다. 전력모드·online CPU 변화도 관측 사실이지 CPU 예약 권한이 아니다.
- **성능 해석:** κ는 공유 CPU의 관측 조건에서 얻은 용량 추정이며 자원 보장이 아니다. CPU 경합에 따라 지연·처리량이 달라지는 전제에서 profiling과 serving의 실행 정책을 맞추고 SLO 충족 여부·예측 오차를 실측한다. 경합 예측 framework나 고정 여유 계수를 먼저 추가하지 않는다.
- **전환 경계:** quota·affinity를 지정했던 profile/κ를 공유 실행과 동등한 근거로 자동 재사용하지 않는다. 의존 캠페인 사이에 실행·검증·catalog·planner·생성기 계약을 한 번에 전환하고 새 조건에서 측정한다. 기존 frozen 실행 기록·과학 원시는 변경하지 않는다.
- **안전 경계:** CPU 독점 장부는 제거하되 Pod/Node 소유·반환 증명, 필요한 GPU 할당·장치 신원, 메모리 안전 경계를 함께 삭제하지 않는다. CPU 공유를 이유로 효과 불명 작업의 예약을 자동 반환하지 않는다.
- **용어:** 여기서 best-effort는 CPU 공유 실행 원칙이다. Kubernetes `BestEffort` QoS는 모든 container 및 Pod 수준의 CPU·memory request/limit이 없어야 한다. 메모리 request/limit을 유지하는 Pod는 `Burstable`일 수 있으며, QoS 이름을 맞추려고 메모리 제한까지 제거하지 않는다([공식 기준](https://kubernetes.io/docs/concepts/workloads/pods/pod-qos/)).
- **완료 기준:** 모든 실행 경로의 선언·기본값·admission 적용 후에도 CPU request/limit과 명시 pinning이 없고, quota/affinity 필수 검증이나 CPU 독점 matching 때문에 실행이 막히지 않는다. Profiling/serving은 같은 공유 실행 정책을 사용하며 실제 실행 조건과 성능을 증거로 남긴다.

### Core — Query API가 운영·측정 조회를 끝까지 소유

**Console은 화면이 아니라 운영·측정 조회 backend다. 독립 프로세스를 유지하고 `CHILL Query API` / `chill-query`로 명명하며, ChillServer에 분산된 조회 조립·응답 해석 책임을 가져온다.** Dashboard는 표시·입력, Query API는 완성된 조회 응답, ChillServer는 공개 인증·권한 경계와 serving 실행·제어를 맡는다.

목표는 이름 변경이나 서버 수 축소가 아니라 **ChillServer가 조회 응답 본문을 해석·수선하지 않도록 소유권을 정리하는 것**이다. 기존 도메인 계산·검증과 원본 상태의 소유자는 유지한다. Query API를 새 실행기·범용 query engine·두 번째 serving 장부로 만들지 않는다. 구체적인 이관 범위·이름·삭제 대상·수용 기준은 R5를 따른다.

**핵심 결론:** 상수의 개수보다 **같은 사실의 판단 책임이 여러 경계로 나뉘고, 다른 수명의 상태를 같은 결과로 취급하는 구조**가 우선 문제다. 후보 8개 중 R1–R3은 실제 구현을 호출한 오프라인 smoke로 현상까지 확인했다. 나머지는 소스·기존 테스트·이력에 근거한 구조 후보이며 확인하지 않은 운영 장애를 주장하지 않는다.

## 2. 조사 범위·방법·권위

### 범위

- Serving: κ cache/build/replay, planner 소비, kube capture·예약·bridge·reset·admission, firstfloor actor/policy.
- Profiling·platform: campaign/idle-baseline, Run projection·execution policy, metric/workload observer, discovery·device class·NVIDIA actuation 등. transition-cost는 관련 준비/검증 경로 중심이며 전체 controller의 전수 감사는 아니다.
- Evaluation: controller·하네스·producer resource 수집·scheduler summary 변경 이력.
- API·Console: profile/coverage/result/passport projection, proxy, live/recovery 연결. Dashboard 프로토콜·relay 소비는 조사했지만 브라우저 시각 검증은 하지 않았다.
- 캠페인 도구: `hack/measure/gen.py`, `coverage-plan.json`, 관련 배포·runtime entry point. 전체 저장소의 모든 줄을 전수 검토했다는 뜻은 아니다.

### 사용한 도구와 증거 구분

- inline 조사 후 독립 영역 4개를 read-only scout로 병렬 조사하고, 주요 후보를 부모 세션에서 재확인했다. scout의 commit subject만으로 인과를 확정하지 않고 아래 주요 관련 커밋은 `git show`/`blame`으로 확인했다.
- Graphify: 기존 `graphify-out/graph.json`은 2026-10-29 생성본이며 삭제된 gateway/router도 포함했다. query는 방향 탐색에만 사용했고 인덱스를 새로 만들지 않았다.
- Go 의미 탐색: repository wrapper `./hack/gopls-chill.sh`, pinned `gopls v0.23.0`의 references. 잘못 지정한 위치의 조회 실패는 위치를 바로잡아 재조회했다.
- 실제 패키지 의존성: `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/serving/... ./internal/evaluation/... ./internal/profiling/... ./cmd/chill-server` 성공. 허용 경계는 `.go-arch-lint.yml`과 설계로 별도 확인했다.
- 동작: 현재 구현 함수/타입의 throwaway smoke. 기존 테스트는 읽었지만 테스트 suite를 실행하지 않았다. live cluster snapshot·Helm render·브라우저 검증은 없다.
- 최근 관련 경로를 포함한 250개 커밋의 파일별 변경 집중도도 보았지만, churn/파일 길이만으로 후보를 선정하지 않았다.

참조 권위: [code intelligence](../design/code-intelligence/README.md), [설정 소유권](../design/serving/configuration-hardcoding.md), [ADR 0031](../adr/0031-configuration-ownership-and-freezing.md), [실험 실행 설계](../design/profiling/experiment-execution.md), [ADR 0032](../adr/0032-experiment-execution-boundary.md). 아래 줄 번호는 조사 당시 위치이며 수정 착수 전 symbol과 현행 소스를 다시 확인한다.

## 3. R1 — 일시적인 노드 문제를 κ 계산의 영구 실패로 저장

**우선순위: 높음. 실행 확인됨.**

**핵심: “지금 이 노드에서 실행할 수 없다”를 “이 측정 조건의 처리 용량을 계산할 수 없다”로 저장하고, 노드가 복구돼도 과거 실패를 재사용하는 문제다.** 여기서 영구란 같은 캐시 키가 해당 table에 남아 있는 동안 다시 계산하지 않는다는 뜻이다.

- **κ 계산:** “이 모델·측정 조건에서 SLO를 만족하며 초당 몇 요청을 처리할 수 있나?” 같은 측정 조건이면 계산 결과를 재사용할 수 있다.
- **현재 실행 가능 여부:** “그 실행 조건을 지금 이 노드에 적용할 수 있나?” 노드 상태가 바뀌면 다시 확인해야 한다.

### 근거·실제 경로

- [`capacity.go:98`](../../internal/serving/capacity.go#L98) `NewCapacityInput`/`Key`: profile, SLO, allocation, occupancy, 수치 정책을 식별한다. 현재 `ModeCPUs`, 예약, 다른 Pod 자원, node memory/GPU inventory는 키에 없다.
- [`capacity.go:328`](../../internal/serving/capacity.go#L328) `capacityReplay.window`: 전달받은 노드의 `AtMode`와 일반 replay 검증을 사용한다.
- [`scheduler_replay.go:92`](../../internal/serving/scheduler_replay.go#L92), `ValidateNodeBudget`: resource applicability를 검사한다. CPU 독점/예약·GPU·메모리 상태가 결과에 영향을 준다.
- [`capacity.go:428`](../../internal/serving/capacity.go#L428): 이미 `t.rows[key]`가 있으면 원인과 무관하게 재계산하지 않는다. [`:519`](../../internal/serving/capacity.go#L519)는 취소 이외 build error를 `CapacityRejected`로 저장한다.
- 실제 소비: [`kube/scheduler.go:291`](../../internal/serving/kube/scheduler.go#L291)에서 capture 후 `Ensure` → `Rows` → `EvaluateScheduler`.

### 구조 문제와 관측

현재 코드는 이 두 판단을 같은 build 경로에서 수행한다. 그런데 캐시 키에는 측정 조건만 들어가고, 계산 도중 검사하는 현재 노드 자원 상태는 모두 들어가지 않는다. 그 상태 때문에 실패해도 캐시는 측정 조건 자체의 실패처럼 저장한다.

오프라인 smoke에서 다음 순서를 실제로 확인했다.

1. 목표 전력모드의 CPU 정보(`ModeCPUs`)를 제거하고 κ 계산을 요청했다.
2. `PlacementInventoryMissing` 실패가 캐시에 저장됐다.
3. 노드 정보를 원래대로 복원하고 같은 table에 다시 계산을 요청했다.
4. 같은 키의 결과가 이미 있다는 이유로 과거 실패와 build 시각이 그대로 반환됐다. 반면 복원된 노드로 새로 계산하면 `Built`, κ 959.375였다.

전체 출력은 §11에 있다. 실제 클러스터 발생을 주장하지 않는다. CPU 공유 실행에서는 예시의 명시적 CPU 할당 검사를 제거한다. 이 사례의 요점은 그 검사를 유지하자는 것이 아니라, 남는 장치·메모리 조건에서도 **일시적인 실행 불가를 재사용할 κ 결과와 섞지 말자**는 것이다.

### 관련 변경·최소 개선

- `d788bd11`의 diff는 label-only mode 투영을 `AtMode` inventory 투영으로 고치고 option만으로 만든 fallback inventory도 제거한다. 이력은 현행 실패 의존성의 근거이며, CPU affinity·독점 할당을 유지해야 한다는 요구가 아니다.
- `7af95dd4`는 obsolete 작업 취소·불가능 replay 조기 중단을 보강하지만 완료된 거절 행의 가변 의존성은 분리하지 않는다.
- 권고: κ 캐시는 측정 조건에 따른 용량 결과를 소유하고, 현재 노드에서 실행 가능한지는 현재 상태로 따로 판단한다. 노드의 일시적인 실행 불가가 같은 키의 κ 계산을 계속 막는 실패로 저장되지 않게 한다.
- 금지할 증상 패치: TTL/retry knob, 전체 changing snapshot 해싱, Ready 강제 통과. CPU 공유 실행으로 불필요해지는 affinity·quota·독점 코어 검사는 제거하고, 남는 장치·메모리·소유권 안전 검사는 보존한다.

### 수정 수용 기준

- 노드의 현재 실행 가능 여부와 κ 계산 결과를 분리한다. 남아 있는 유효한 장치·메모리 조건이 복구되면 과거 거절에 고정되지 않고 다시 판단해야 한다. CPU mode별 코어 inventory를 새 CPU 할당 관문으로 유지하지 않는다.
- 같은 공유 실행 측정 조건의 수치 방법론·캐시 재사용은 유지한다. quota/affinity 실행에서 얻은 κ와의 수치 동등성은 요구하지 않고 새 조건으로 재측정한다.
- exclusive CPU·reserved CPU quota 장부는 제거한다. 필요한 GPU 할당, memory safety, 물리 소유·반환 증명은 계속 검사한다.
- 기존 `capacity_test.go`의 mode CPU 검사와 캐시 테스트는 현행 의존성을 파악하는 근거다. CPU 공유 실행과 충돌하는 기대는 유지·재고정하지 말고, 남는 유효한 조건의 복구 전이와 공유 실행의 성능 평가를 검증한다.

## 4. R2 — 재측정이 필요하다고 판단하고도 과거 요청 때문에 생략

**우선순위: 높음. 실행 확인됨.**

**핵심: 앞 단계에서는 “현재 실행 조건으로 다시 측정해야 한다”고 골라 놓고, 뒤 단계에서는 “같은 칸에 과거 요청이 있다”며 새 측정을 건너뛴다.** 같은 측정표 칸에 이력이 있다는 사실과 지금 필요한 조건으로 측정했다는 사실은 다르다.

### 실제로 확인한 흐름

여기서 셀(cell)은 모델·장비·실행 경로·전력모드로 구분하는 측정표의 한 칸이다. 오프라인 smoke는 같은 셀에 old-runtime으로 완료된 요청이 있고, 현재 필요한 image는 new-runtime인 상황을 구성했다.

1. **재측정 대상 선택:** 기존 profile이 old-runtime이므로 stale로 분류하고 재측정 대상으로 고른다.
2. **기존 요청 검색:** 같은 셀의 CPU 조건을 만족하는 과거 `Complete` 요청을 찾는다. 이 요청이 현재 필요한 runtime을 측정했는지는 확인하지 않는다.
3. **생성 생략:** 과거 요청이 있다는 이유로 새 요청 생성을 건너뛴다. 결과적으로 new-runtime 측정은 시작되지 않는다.

실제 함수 호출 결과는 stale target 1개이면서 기존 Complete 요청도 선택됨이었다. generator는 이 결과를 받으면 `existing.append` 후 `continue`한다. 전체 입력·출력은 §11에 있다. 실제 클러스터 발생을 주장하지 않는다.

### 코드·이력 근거

- [`gen.py:494`](../../hack/measure/gen.py#L494) `coverage_targets`: 현재 runtime으로 측정했는지 판단한다.
- [`gen.py:633`](../../hack/measure/gen.py#L633) `coverage_existing`: cell ID/후보 식별·CPU 조건·Failed 여부로 과거 요청을 찾으며, 앞 단계와 같은 실행 조건 비교를 하지 않는다.
- [`gen.py:847`](../../hack/measure/gen.py#L847): 기존 요청을 찾으면 새 측정 요청 생성을 생략한다.
- `49155374`는 current-runtime 분류와 fallback candidate matching을 함께 추가했다. `7b603f6f`는 Failed 요청 제외, `63d119da`는 problem 셀 재계획을 추가했다. 상태 예외 누적만으로 두 판단의 조건 차이가 해결되지는 않는다.

### 개선 방향·수용 기준

기존 요청을 찾을 때는 **“이 칸을 전에 측정했나?”가 아니라 “지금 필요한 실행 조건으로 완료했거나 측정 중인가?”**를 판단한다. 재측정 대상 선택과 요청 재사용은 같은 명시적 실행 조건 비교를 사용한다.

- **현재 조건으로 완료:** 유효한 측정 증거를 재사용한다.
- **현재 조건으로 진행 중:** 중복 요청을 만들지 않는다.
- **과거의 다른 조건으로 완료:** 현재 조건의 새 측정을 막지 않는다. Failed 요청의 재계획 처리도 유지한다.
- **CPU 공유 실행 전환:** CPU 크기·quota·affinity matching은 제거하되, 과거 CPU 통제 실행과 새 공유 실행의 증거는 구분한다. CPU 항목만 비교에서 빼고 과거 완료를 새 조건의 완료로 인정하면 안 된다. 새로운 digest/byte equality 관문은 만들지 않는다.
- 기존 `coverage_self_check`는 stale target과 existing request를 따로 확인한다. 수정 검증은 두 함수를 잇는 실제 생성 판단까지 포함해야 한다.

## 5. R3 — 기록 개수 설정 대신 관측 조건과 실제 자원 한도로 실행 판단

**우선순위: 높음. 현행 문제는 실행 확인됨. 개선 구조의 자원 수용 검증은 미실행.**

**핵심: 보호해야 할 것은 관측기의 메모리·결과 크기이지 임의의 기록 개수 자체가 아니다.** 관측 시간·간격이 이미 정해져 있는데 사용자가 별도의 `MaxEvents`까지 맞춰 입력할 필요는 없다. 필요한 기록 수는 관측기가 실행 조건에서 도출하고, 실행 가능 여부는 실제 자원 한도로 판단한다.

### 현재 구조와 확인한 문제

- 관측기는 전력 표본과 실패 기록을 메모리 배열에 보관한다. 한 번의 정상 읽기는 표본 1개지만, 전력 성공·온도 실패는 전력 표본 1개와 온도 실패 기록 1개를 남긴다. **읽기 횟수와 기록 개수는 다르다.**
- [`observer.go:40`](../../internal/metricobserver/observer.go#L40) `Request.Validate`는 `duration/interval+2`, [`:101`](../../internal/metricobserver/observer.go#L101)은 final-read `+3`을 검사하지만, [`:221`](../../internal/metricobserver/observer.go#L221)에서는 온도 실패 때문에 기록이 두 개씩 생길 수 있다.
- [`execution_policy.go:71`](../../internal/profiling/profiler/execution_policy.go#L71) `DeriveExecutionPlan`이 그 내부 표현을 알고 `2*(duration/interval+3)`을 따로 계산해 보정한다. `93c7b814` diff는 온도 실패 보존·final-read retry와 함께 profiler 공식도 수정한다.
- smoke: power=5W 성공·thermal=context deadline error인 합성 Source, interval=20ms, duration=200ms, request timeout=1ms, MaxEvents=12. Validate는 승인했지만 sample 6개·failure 6개를 남긴 뒤 120ms에 event budget error로 중단됐다. 전체 출력은 §11에 있다.
- 현재 profiler 경로는 큰 공식으로 보호된다. 모든 production 관측이 실패한다는 뜻은 아니다. 관측기의 사전 검사와 실제 기록량이 다르고, profiler가 이를 별도로 보정해야 한다는 문제다. `gopls`로 scheduler config·transition preparation·serving measurement 등의 Validate 소비도 확인했다.

### 개선 방향

1. **필요량은 관측기에서 도출한다.** 기간·간격·읽기당 최대 기록 수·마지막 읽기와 retry 일정으로 필요한 기록량을 산정한다. 개수는 내부 계산값이지 독립적인 실험 설정이 아니다. Profiler가 필요량을 알아야 한다면 같은 계산을 사용하며 observer의 저장 방식을 다시 구현하지 않는다.
2. **실제 자원 한도와 비교한다.** 기존 실행 환경의 메모리·결과 크기 한도를 기준으로 관측을 수용할 수 있는지 시작 전에 판단한다. `MaxEvents`/`MaxMetricEvents`를 근거 없는 독립 상한으로 유지하기보다 해당 입력·검증·전달 경로를 제거하는 것을 목표로 한다. 내부 도출값에 대한 방어 검사는 별개다.
3. **기록 개수와 바이트를 동일시하지 않는다.** 실패 메시지 등 기록의 크기가 달라질 수 있으므로, 건수만 제한하거나 근거 없는 bytes/event 상수를 곱해 메모리 안전을 보장한다고 주장하지 않는다. 보관·직렬화의 실제 크기 경계와 산정 근거를 확인하고 필요한 최소 보호를 둔다.
4. **수용할 수 없으면 시작 전에 거절한다.** 부족한 자원을 맞추려고 요청한 기간·간격을 몰래 바꾸거나 표본·실패 기록을 잘라내지 않는다. 실행 중 실제 관측·저장 오류나 예상 밖 자원 초과가 발생하면 명시적으로 실패하고 수집한 증거를 보존한다. 사전 산정이 모든 runtime 장애의 부재를 보장하는 것은 아니다.

### 수정 수용 기준

- 사용자는 기간·간격과 별도로 기록 개수 설정을 조율하지 않는다. Profiler·observer에 중복된 event accounting과 불필요한 설정 경로가 남지 않는다.
- 정상 읽기·source 실패·전력 성공/온도 실패·producer-end final read/retry에서 필요한 기록량이 올바르게 도출된다. 기록 개수 계산 차이만으로 사전 승인한 창이 중간에 잘리지 않는다.
- 자원 수용/거절 근거가 기존 메모리·결과 크기 한도에 연결된다. 가변 길이 기록과 직렬화 비용을 고려하며, 건수 검사를 바이트 안전의 증명으로 대체하지 않는다.
- 실제 자원 한도, 요청한 관측 일정, power 표본과 오류 증거의 의미는 보존한다. 무제한 배열 적재, silent truncation, 임의 상수 증설, 새 범용 자원 관리 framework로 치환하지 않는다.
- 기존 `TestObserveThermalFailureRetainsPowerWithinEventCap`, `until_test.go`, `execution_policy_test.go`는 현재 동작의 근거다. 수정 검증은 기존 `MaxEvents` 값/공식의 재고정이 아니라 필요량 도출·자원 경계·완전한 관측 창과 실패 증거 보존을 확인한다.

## 6. R4 — 내부 데이터 전체를 변환한 뒤 덜어내지 말고 외부 응답을 직접 구성

**우선순위: 높음. 구조는 소스 확인, 실제 함수 allocation smoke 미완료.**

**핵심: 요약 응답을 보내려고 상세 원본 전체를 JSON으로 바꾼 뒤 버리지 않는다. 외부에 필요한 항목만 처음부터 응답 구조에 담는다.** 단순한 JSON 속도 개선이 아니라 **내부 데이터 구조와 외부 API 계약을 분리하는 리팩토링**이다. 원시 측정 데이터를 삭제하거나 응답 필드를 임의로 줄이는 작업이 아니다.

### 현재 구조와 원인

- 위치: [`measurement_entity_assessment.go:286`](../../cmd/chill-server/measurement_entity_assessment.go#L286) `decorateProfiles`, 특히 334–350행. [`httpapi/profiles.go:18`](../../internal/serving/httpapi/profiles.go#L18) `ProfilesView`는 내부 CatalogView를 embed한다.
- 현재 흐름은 요약 profile 구성 → 원본 catalog 전체 JSON 변환 → 필드별 RawMessage map으로 다시 읽기 → profiles를 요약으로 교체하고 measurementFacts 삭제 → 다시 JSON 변환이다. `BatchProfile.DurationNanos`도 직렬화 대상이므로 최종 응답에서 빠질 상세 표본까지 중간에 처리한다.
- 공개 범위도 “필요한 필드만 포함”이 아니라 “내부 데이터를 모두 포함하되 일부 삭제”로 정해진다. 내부 필드를 추가하면 API 계약까지 의도치 않게 달라질 수 있다. 실제 정보 노출 사고가 관측됐다는 주장은 아니다.
- 이력: `7dd21bd7`은 bounded read/raw 제외를 보강하고 `5db9d104`는 summary slice를 사전 할당한다. **`d4a933bf`는 소비자가 필요로 하는 preparations 등 필드가 빠진 것을 복구하려고 명시적 응답 구조를 전체 JSON round-trip으로 바꾼다.** 일부 필드를 다시 빼는 것으로 끝내면 같은 누락 문제가 반복될 수 있다.

### 개선 방향

1. **외부 응답 계약을 명시한다.** 기존 소비자가 필요로 하는 catalog 필드와 profile 요약을 typed public response에 정의하고, R5의 Query API가 조회용 응답 구성을 소유한다. `preparations`를 포함한 restore 계약을 확인하고 누락 없이 반영한다.
2. **필요한 값만 직접 담는다.** Catalog 소유자가 공개할 필드와 요약값을 명시적 읽기 계약으로 제공하고 Query API가 이를 응답으로 조립한다. 원본 전체를 JSON으로 변환한 뒤 map에서 삭제·교체하는 중간 경로를 없앤다. 프로세스 사이에서 내부 raw catalog 전체를 전달한 뒤 덜어내는 구조로 옮기지 않는다. 각 HTTP 경계는 필요한 구조만 한 번 직렬화한다.
3. **내부 원본은 보존한다.** raw samples/measurementFacts와 scheduler 원본은 그대로 유지한다. 내부 필드 추가가 자동으로 외부 응답에 포함되지 않게 한다.
4. **소비 경로를 끝까지 확인한다.** production의 undecorated 응답 경로가 필요한지는 references와 실제 소비자로 판단한다. 필요하면 같은 명시적 공개 계약을 사용하고, 불필요함이 확인된 경로만 제거한다. 새 범용 serializer나 projection framework는 필요하지 않다.

### 수정 수용 기준·검증 경계

- 기존 public 필드·필터·paging 의미와 `preparations` 등 restore 계약을 보존한다. 내부 raw duration/measurementFacts는 응답에 포함하지 않으며 scheduler 원본을 변경하지 않는다.
- 최종 응답에서 빠질 raw 배열은 중간에도 직렬화하지 않는다. final payload 크기만 작다는 것으로 완료 처리하지 않는다.
- 내부 catalog에 필드를 추가하는 것과 공개 API에 필드를 추가하는 것이 구분된다. 공개 범위를 삭제 목록이 아니라 명시적 응답 계약이 결정한다.
- [`measurement_r6_test.go:18`](../../cmd/chill-server/measurement_r6_test.go#L18)는 raw duration 100,000개로 최종 응답 제외·preparations 유지·원본 무변경을 확인하지만 중간 allocation은 검사하지 않는다. 수정 후에는 실제 응답 경로에서 계약 보존과 불필요한 중간 처리가 사라졌는지 함께 확인한다.
- 실제 `decorateProfiles` 호출 Go overlay smoke는 §11의 작업 트리 컴파일 오류로 실행에 도달하지 못했다. allocation/CPU 개선 수치나 성능 benchmark 결과는 없다.

## 7. R5 — Console을 Query API로 재정의하고 조회 응답의 소유권을 모음

**우선순위: 높음. 사용자 방향: 독립 조회 backend 유지·책임 강화·명칭 정리. 현행 경로는 소스 확인, 이관·배포 검증은 미실행.**

**핵심: 조회 서버가 있는데도 ChillServer가 데이터를 다시 읽어 응답을 고치고 있다. Query API가 조회를 완성하고, ChillServer는 그 응답의 도메인 내용을 해석하지 않게 한다.** Dashboard가 생겼으므로 조회 기능이 불필요해진 것이 아니다. 화면과 조회 backend의 역할은 다르다.

### 현재 역할·문제의 근거

- [`cmd/console/run.go:163`](../../cmd/console/run.go#L163) `buildHandler`는 system/inventory·모델 호환성·ProfilingRun·Evidence Passport·측정 결과/통계·preflight 조회를 조립한다. [`main.go:41`](../../cmd/console/main.go#L41)는 이를 독립 HTTP 서버로 실행한다.
- [`http_routes.go:92`](../../cmd/chill-server/http_routes.go#L92)는 일부 공개 조회를 Console로 중계한다. Dashboard도 [`relay.ts:57`](../../chill-dashboard/lib/relay.ts#L57) 등의 경로로 그 기능을 사용하며, CLI의 [`evidence_passport_query.go:47`](../../cmd/chill/evidence_passport_query.go#L47)는 Console Service를 직접 경유한다.
- Console query는 evidence 부재/읽기 오류에서 native metadata snapshot도 잃는다: [`evidence_passport_query.go:65`](../../internal/operatorconsole/kube/evidence_passport_query.go#L65), [`batch_energy_optimization_result_query.go:74`](../../internal/operatorconsole/kube/batch_energy_optimization_result_query.go#L74).
- 이를 보완하려고 [`measurement_reads.go:77`](../../cmd/chill-server/measurement_reads.go#L77) `servePendingPassport`와 `addMeasurementResultMetadata`/`enrichMeasurementResultError` 등이 Kubernetes를 다시 읽고 같은 passport/result 응답·assessment·null 규칙을 재구성한다. `80373c67`이 이 response repair 계층을 추가한다.
- 정상·pending·evidence error·transport error의 응답 구성이 갈라져 있어 필드·의미 변경이 여러 경로로 전파된다. 문제는 서버 분리 자체가 아니라 조회 응답을 끝까지 책임지는 소유자가 불명확하다는 점이다.

### 목표 책임 경계

| 소유자 | 책임 | 넘지 않을 경계 |
|---|---|---|
| ChillServer | 공개 진입점의 인증·권한·라우팅, 추론 접수/전달, serving 제어·변경 실행 | 조회 응답을 위해 별도 Kubernetes 재조회·assessment 재판정·본문 수선하지 않음 |
| Query API | 운영·측정 조회 조립, evidence 읽기·검증 연결, 통계 조회, read-only preflight, 공개 조회 모델과 pending/error 표현 | 추론·노드 제어·측정 실행·원본 변경을 소유하지 않음 |
| 기존 도메인 패키지 | 측정 판정·통계·호환성·evidence 검증 규칙의 권위 있는 구현 | HTTP·화면별 응답 때문에 규칙을 복제하지 않음 |
| Dashboard | 표시·입력·사용자 상호작용 | Kubernetes/S3 직접 접근·도메인 계산 재구현하지 않음 |

Preflight는 POST여도 실제 변경을 일으키지 않는 조회다. 반면 요청 생성·삭제·실행은 별도 변경 경계에 남긴다. Serving runtime의 요청·routing·예약 상태와 immutable evidence는 기존 소유자가 정본이며 Query API가 재구성한 두 번째 장부로 대체하지 않는다. Live 기록·발행·순서·recovery 계약은 이 작업에 편승해 옮기지 않는다.

### 이관·이름·삭제 범위

1. **독립 조회 backend를 유지하고 명명한다.** 제품/역할 이름은 `CHILL Query API`, 실행·배포 이름은 `chill-query`, 실행 root는 `cmd/chill-query`를 목표로 한다. `cmd/chill` CLI와 Dashboard는 별개다. `internal/operatorconsole` 계열은 필요한 view/query/adapter를 보존하며 Query API 책임에 맞는 패키지 이름으로 정리한다. 이름만 바꾸고 기능 전체를 삭제하지 않는다.
2. **조회 시작부터 최종 응답까지 책임진다.** Pending passport·evidence 오류의 native metadata·실패/진행 assessment를 Query API의 typed builder/query가 구성한다. Evidence가 없거나 읽히지 않아도 관측한 실행 상태는 잃지 않고, 없는 증거를 합성하지 않는다.
3. **ChillServer의 조회 조립을 이관한다.** `measurement_reads.go`의 도메인 응답 수선, 측정 결과·coverage 조회 조립, R4의 public profile 응답 구성이 대상이다. `measurement_*` 파일을 일괄 이동하지 말고 변경 실행·admission과 조회를 분리한다. 계산은 기존 Go 도메인 함수를 재사용한다.
4. **원본은 명시적 읽기 계약으로 받는다.** Serving 소유 사실은 기존 공개 읽기 경로를 재사용하고 R4처럼 필요한 projection만 제공한다. Query API 경로를 ChillServer가 다시 Query API로 보내는 순환 호출을 만들지 않는다. 새 polling/cache·snapshot 저장소·일반 query protocol을 선제 도입하지 않는다.
5. **응답 수선과 중복 fallback을 삭제한다.** ChillServer에 남는 중계는 인증·전달·transport 오류 처리만 한다. Query API 접속 실패를 이유로 Kubernetes를 재조회해 도메인 응답을 만드는 fallback은 두지 않는다. Backend 연결 실패와 원본 evidence 실패를 구분한다.
6. **한 번에 이름과 소비자를 이행한다.** 바이너리/image·Helm Service/Deployment·SA/RBAC·설정/flags·build/deploy 도구·CLI Service 조회·테스트·문서를 함께 정리한다. 불필요한 Console 전용 이름·중복 경로·alias/shim은 제거한다. 공개 API path·응답 schema·오류 계약은 단순 rename에 편승해 변경하지 않는다. 권한은 소유 책임에 맞게 재배치하며 맹목적으로 넓히지 않는다.

### 수정 수용 기준

- 이름 변경만이 아니라 ChillServer의 조회 재조회·도메인 본문 수정이 사라지고 Query API가 정상/pending/evidence 오류 응답을 일관되게 완성한다.
- Pending의 202, stale/invalid/published evidence의 verified reader 사용, evidence 오류의 원 status/reason, 실패 campaign 신원·진행·조건, raw 미합성을 보존한다. Transport 실패를 측정 실패나 pending으로 꾸미지 않는다.
- TokenReview/SAR·namespace 제한과 사용자 권한 의미를 유지한다. Private backend에 도달했다는 사실을 사용자 권한 승인으로 간주하지 않고, 임의 identity header를 신뢰하지 않는다.
- Dashboard와 CLI가 같은 조회 의미를 사용하고 Console Service·기존 실행 이름의 잔존 의존성이 없다. 기존 `measurement_reads_test.go` 등의 의미 있는 시나리오는 새 소유 경계에서 검증한다.
- Serving runtime·evidence·도메인 규칙의 정본이 중복되지 않는다. 큰 evidence/통계 조회의 자원 소비는 독립 조회 backend에서 관측하고 serving 영향과 장애 경계를 확인한다. 분리만으로 자원 간섭이 없다고 주장하지 않는다.
- Go import 방향·architecture mapping, default/Profiler Helm profile, 명시적 Query API 경유 조회, 인증 거부·backend 불능·evidence 불능·pending 경로와 CLI 소비를 검증한다. 현재는 `go list`·`gopls` 참조·`go run ./cmd/console --help`로 기존 역할과 실행 진입점만 확인했으며 live 통합 검증 결과는 없다.

## 8. R6 — 측정 역할과 실제 실행 batch를 섞어 이름으로 의미를 복원

**우선순위: 중간. 소스·후속 수정 diff 확인. 운영 장애 실행 재현 없음.**

**핵심: “실험에서 무엇을 위한 측정인가”와 “실제로 어떤 batch 조건으로 실행했는가”는 다른 정보다.** 현재는 두 의미를 같은 batch 식별로 다루다가 값이 달라지자 Run 이름에서 실험상의 의미를 복원한다.

### 직관적인 예와 현재 경로

Active 측정은 실제 추론할 때의 전력·성능을, idle 측정은 모델을 올려두고 추론하지 않을 때의 기본 전력을 관측한다. 두 관측으로 추론 때문에 추가된 에너지를 구분한다.

캠페인의 logical batch가 4인 idle trial을 예로 들면, **실험상의 식별은 batch 4에 연결되지만 ORT의 실제 idle 실행 조건은 batch 1**이다. 현재 코드는 Run의 실제 batch를 1로 설정한 뒤, 결과를 캠페인 trial로 되돌릴 때 이름의 `idle-b4` 부분을 읽어 logical batch 4를 복원한다. 이름이 단순한 객체 식별자뿐 아니라 실제 batch와 다른 실험 의미의 저장소가 된 셈이다.

- [`run_builder.go:73`](../../internal/profiling/campaign/controller/run_builder.go#L73)는 execution path를 보고 ORT idle batch=1/RKNN 예외를 결정한다.
- [`curve_plan.go:145`](../../internal/profiling/profiler/projection/curve_plan.go#L145)는 runtime family/backend를 보고 같은 batch 규칙을 다시 판단한다.
- [`campaign/controller/projection.go:79`](../../internal/profiling/campaign/controller/projection.go#L79)는 Run 이름에서 logical batch를 복원한다.
- `188c4ed6`은 idle batch=1과 이름 기반 복원을 추가하고 `be1c543e`는 curve projection을 후속 수정한다. 생성 규칙·검증 규칙·실험 식별 복원을 함께 맞춰야 하는 변경 전파의 근거다.

### 개선 방향

1. **측정 역할과 실행 조건을 구분한다.** 캠페인의 idle baseline/trial identity와 실제 실행 batch를 명확히 다룬다. 실제 batch 하나에 두 의미를 얹거나, 빠진 의미를 이름에서 추측해 보충하지 않는다. 그렇다고 독립 생명주기 없는 새 CR·범용 실험 framework를 추가할 필요는 없다.
2. **Idle 실행 batch 규칙을 한 곳에서 결정한다.** Runtime/capability 계약을 기준으로 생성과 검증이 같은 규칙을 사용한다. ORT CPU/CUDA의 idle batch 1과 고정 batch artifact를 사용하는 RKNN의 필요한 차이는 유지한다.
3. **이름은 결정적인 객체 식별에 사용한다.** 재조회·중복 생성 방지를 위한 이름의 역할은 유지하되, 실험상의 측정 역할을 복원하는 별도 정본으로 삼지 않는다. Mutable label을 대신 정본으로 만드는 것도 피한다.

### 수정 수용 기준

- 실험상의 baseline/trial 식별과 실제 실행 batch가 구분되며, Run 생성·캠페인 결과 조립·evidence 검증이 같은 의미를 사용한다.
- 새로운 execution path를 추가할 때 서로 다른 분류식과 이름 parser를 각각 수정하지 않아도 된다. ORT/RKNN의 실제 측정 조건과 evidence 검증을 약화하지 않는다.
- 기존 `TestBuildIdleRunSeparatesMeasurementBatchFromTrialIdentity`, `TestCurvePlanIdleBatchPolicy`는 현재 의미를 확인하는 근거다. 수정 검증은 이름 문자열 형식의 재고정보다 측정 역할·실제 batch·결정적 재조회·검증의 일치를 확인한다.
- 저장 계약 변경이 필요하면 의존 캠페인 사이 단일 cutover로 한정한다. 기존 과학 원시·실행 기록을 수정하거나 호환 reader/alias를 추가하지 않는다.

## 9. R7 — 메모리 실패 근거보다 넓게 적용되는 제외표

**우선순위: 중간. 적용 범위는 실행 확인, 새 조건의 실행 가능성은 미확인.**

- 위치: [`coverage-plan.json:22`](../../hack/measure/coverage-plan.json#L22) `memoryMaxBatch`, [`gen.py:728`](../../hack/measure/gen.py#L728) `coverage_batch_plan`.
- 키는 model + device class, 또는 device class/execution path다. 근거는 특정 runtime·메모리 limit에서 관측한 OOM/warmup/start 실패지만, 적용 판단은 image·memory envelope·근거 유효 범위를 입력으로 사용하지 않는다.
- `c9cc020c` 장비별 batch cap → `f7394f55` path별 예외 → `89de4f85`/`5dc8abb2` 조합 전체 제외(0)로 확장됐다.
- smoke는 새 runtime과 다른 memoryLimitBytes를 나타내는 추가 필드를 가진 셀에도 기존 cap이 적용돼 batch 1/2 모두 제외됨을 확인했다. **이 추가 필드가 현행 API의 공식 지원 입력이라는 뜻도, 새 runtime/16GiB에서 실행 가능하다는 뜻도 아니다.** 함수가 그 조건을 사용하지 않는다는 관측이다.
- 권고: 진행 중 캠페인의 frozen policy는 유지한다. 다음 캠페인에서는 기존 실패 관측을 해당 image/경로/자원 조건과 연결하고, 조건이 바뀌면 재평가할 수 있도록 제외 범위를 명시한다. 기존 값 자동 해제·임의 메모리 추정·자동 재측정은 제안하지 않는다.
- 수용 기준: 같은 조건에서 확인된 제외는 유지하고, 다른 조건에 옛 실패가 무조건 전파되지 않는다. OOM/원시 실패와 운영상 보수적 제외를 구분해 근거를 보존하며 실행하지 않은 조합을 성공 가능으로 꾸미지 않는다.

## 10. R8 — ProfilingRun terminal 의미를 부모별 문자열 목록으로 복제

**우선순위: 중간. 현재 정체 실행 재현 없음.**

- 위치: [`campaign/controller/projection.go:126`](../../internal/profiling/campaign/controller/projection.go#L126) `isTerminalReason`, [`idlebaseline/controller/reconciler.go:222`](../../internal/profiling/idlebaseline/controller/reconciler.go#L222) `runTerminal`.
- 같은 일곱 reason을 중복 소유한다: `InvalidRunSpec`, `ExecutionPolicyInvalid`, `ActuationPolicyInvalid`, `ExecutionLost`, `JobFailed`, `RequiredStateNotConfirmed`, `EvidenceInvalid`.
- blame와 diff: 과거 batchsaturation 소비자에 `b8ea7e64`가 EvidenceInvalid, `530b90e3`가 RequiredStateNotConfirmed를 반영했다. `ec63d406`은 idle-baseline controller 도입이며 같은 분류가 두 소비자에 남는다. 다른 hash의 비슷한 subject를 정확한 현행 줄의 도입 커밋으로 혼동하지 않는다.
- 권고: Run의 terminal condition 해석만 기존 profiling 계약 경계 한 곳에 둔다. campaign/idle-baseline의 서로 다른 lifecycle은 합치지 않는다. generation/Job 존재를 다루는 더 좁은 `terminalPreJobFailureCurrent`까지 무조건 같은 helper로 바꾸지 않는다.
- 위험은 새 reason 추가 때 한쪽이 pending으로 남거나 retry/exclusion/result publication이 달라지는 변경 전파다. 현재 운영 정체를 관측했다는 뜻은 아니다.
- 수용 기준: producer가 정의한 terminal 조건이 두 부모에서 일관되게 해석되며, evidence 우선 처리와 pending/nonterminal·generation 의미를 보존한다. source 문자열 복사 여부를 검사하는 테스트 대신 상태 전이를 검증한다.

## 11. 실제 실행 증거·재구성 방법·실패한 검증

### 오프라인 smoke 결과

임시 smoke 소스는 조사 후 제거했다. 아래 명령은 당시 실행 기록이며 삭제된 경로가 현재 존재한다는 뜻이 아니다. 수정 세션은 해당 입력·절차로 현행 코드를 다시 exercise한다. cluster·외부 endpoint는 사용하지 않았다.

1. **R1:** `go run ./.audit-capacity`. `scheduler_test.go`의 `schedulerFixture`/`capacity_test.go`의 `capacityFixture`와 같은 유효한 작은 option을 public serving 타입으로 구성했다. 현재 node mode=current/CPU{0,1}, target-mode CPU{0,1,2,3}, affinity{3}, reserved{0,1}, reserved non-serving quota=1, memory=10000, profile samples={0.8,1,1.2}ms, p95/20ms, occupancy 일치. 유효 fresh build를 확인한 뒤 target ModeCPUs=nil → Ensure/Wait → 원래 node 복원 → 같은 table Ensure/Wait를 실행했다.

```json
{"freshKappa":959.375,"freshRestoredStatus":"Built","initialReason":"capacity replay: PlacementInventoryMissing:n1","restoredReason":"capacity replay: PlacementInventoryMissing:n1","sameBuildTimestamp":true,"sameKey":true}
```

2. **R2:** Eval에서 현재 `gen.py`를 import하고 `coverage_targets` → `coverage_existing`을 호출했다. 임시 최소 `CHILL_EXPERIMENT_CONFIG`는 measure의 capturedValues/namespace/authority와 paths.campaign만 제공하고 환경 변수를 복원했다. 셀은 mobilenet-v2-050/raspberry-pi-5-8g/CPU path/fixed, CPU request=quota=3, old-runtime profile과 동일 cell annotation의 Complete 요청이며 current image는 new-runtime이었다.

```json
{"staleRuntimeTargetCount":1,"staleCount":1,"existingRequest":"coverage-old-runtime","existingPhase":"Complete","generationBranch":"existing.append + continue"}
```

3. **R3:** `go run ./.audit-observer`. §5의 Source/Request로 `New`, `Request.Validate`, `Observe`를 직접 호출했다. 관측 시간 120ms는 단일 실행 관측이며 benchmark/엄밀한 스케줄 상한이 아니다.

```json
{"elapsedMillis":120,"failures":6,"observationError":"metric observation event budget exceeded","requestedMillis":200,"samples":6,"validationAccepted":true}
```

4. **R7:** 같은 gen.py import로 xavier Smol CPU path 셀과 batch {1,2}, timings 빈 목록, 저장된 coverage policy를 `coverage_batch_plan`에 전달했다. 셀에 runtimeImage=new-runtime, memoryLimitBytes=17179869184를 추가했지만 적용 제외는 같았다.

```json
{"runtimeImage":"new-runtime","memoryLimitBytes":17179869184,"chosenBatches":[],"reason":"측정 불가(메모리)","excludedBatches":[1,2]}
```

### 완료하지 못한 검증과 미실행 항목

- R4는 `/tmp`의 entrypoint/overlay로 실제 `decorateProfiles`를 rawSamples=0/100000 입력에서 실행하려고 `go run -overlay /tmp/chill-audit-profile-overlay.json ./cmd/chill-server`를 시도했다. 당시 `measurement_coverage_view.go`의 `operatorconsole.MeasurementAssessmentSummary`, `ResultRowSummary`, `SummaryMeasurementAssessment`, `SummaryMeasurementResultRow` undefined 오류로 compile이 중단됐다. 이 변경은 수정하지 않았으며 동일 오류 확인만을 위한 재실행도 하지 않았다.
- 최초 R1 smoke는 derivation의 실제 package name이 profilederivation인 것을 alias하지 않아 compile 오류가 났고, 임시 파일 import alias 수정 후 위 실행에 성공했다. production code 수정은 아니다.
- evaluation module 첫 import는 CHILL_EXPERIMENT_CONFIG 부재로 실패했고, 명시적인 임시 환경 입력으로 재실행했다. 관측 결과를 환경 무의존 CLI 동작으로 주장하지 않는다.
- 전체 `make lint`, `make test`, arch/gopls golden gate, manifests/generate, Helm render, browser, live cluster 검증은 실행하지 않았다. 조사 수행의 한계이며 수정 후 통과했다고 읽지 않는다.
- 당시 API package compile 상태와 무관하게 성공한 serving/observer/coverage smoke 결과를 보존한다. 전체 repository build 성공 주장은 없다.

## 12. 후순위 관찰·이번 우선 목록에서 제외한 추가 후보

### Producer resource projection: 좁은 최적화 후보

- [`e5.py:2423`](../../hack/e5/e5.py#L2423) `ProducerResources.poll`은 해당 hour의 resources를 전부 materialize한 뒤 `resource_telemetry`에서 전체 container/thermal projection을 만들고 client만 남긴다. 소비할 producer Pod UID/load containerID는 이미 알고 있다.
- 혼합 합성 입력을 실제 `resource_telemetry`에 전달한 출력: inputContainers=2, projectedCgroupRows=2, projectedThermalRows=1, producerRowsRetained=1, roles=[client,unknown]. 이 호출은 poll의 network/closed-minute 경로를 재현한 것이 아니다.
- `a95b4f15` API-only cutover, `01ff0a02` ObservationNotReady 대기, `6549d94f` polled rows 보존, `5375651c` 증거 경로 필수화의 목적은 유지한다. 측정된 성능 병목/실패 근거가 없어 R1–R8보다 후순위다. 필요하면 client 선택을 projection 앞에 두되 sourceRef와 모든 필수 counter/증거는 유지한다.
- API-only·닫힌 분 polling·405초 setup 자체는 승인된 계약이다. 이를 제거하거나 SSH 수집으로 되돌리는 제안이 아니다.

### Live/recovery event projection: 소스 수준 후보

- `cmd/chill-server/live.go`의 action projection과 `internal/serving/httpapi/observation_events.go`의 durable recovery projection은 같은 live DTO를 다른 원본(OperationResult/committed observation)에서 조립한다. 필드/시각/terminal phase 규칙의 변경 전파 가능성이 있다.
- 실시간 publication도 committed observation 표현의 pure projector를 재사용하는 방향을 고려할 수 있다. commit-before-publish·정확한 timestamp·동시 event·bounded replay/snapshot barrier를 보존해야 한다.
- 현재 재접속 불일치를 실행 재현하지 않았고 관련 커밋의 도입 인과도 이번 주요 후보만큼 diff 검증하지 않았다. 우선 수정 대상으로 확정하지 않는다.

## 13. 의도적 정책·이미 해결된 항목: 재도입/과잉 정리 금지

- **장비 조건:** hardware signature, 실제 clock readback, settling, runtime host contract, node별 memory envelope는 필요한 입력이다. NVIDIA locked range를 조회할 수 없는 제약에서 관측과 의도를 구별하는 코드도 필요하다.
- **측정 policy:** confidence·reuse·window·seed·chosen method는 실제 소유자의 frozen 입력으로 유지한다. protocol/packaging constant까지 전부 Config로 옮기지 않는다. Gonum 기반 통계를 자작 근사로 대체하지 않는다.
- **Producer 수집:** ADR 0032의 2026-11-02 개정은 API-only resources, 정확한 Pod UID/load containerID, 닫힌 분 지연과 API 예산을 근거로 setup=405초를 승인했다. 단순 timeout 부풀리기·E5 전용 서버 라벨로 분류하지 않는다.
- **Fixed-only reset:** `120165f8`이 readiness 검사 전에 적용 불가능한 lowering을 제외했다. 현행 미수정 버그로 다시 등록하지 않는다.
- **제거된 debt:** ksafe/replay shim은 `e9400973`, cleanup reservation bypass는 `3a8b2e89`에서 제거됐다. 삭제된 코드를 현행 후보로 집계하거나 되살리지 않는다.
- **Scheduler summary:** `6267972a`/`ce9495ea`/`29fe9046`의 summary projection과 테스트 변경을 읽었다. scheduler 응답의 4MiB cap 문제를 현행 미수정 장애로 주장하지 않는다.
- **예약·반환 증명:** owner/UID/container/readback·route receipt는 서로 다른 물리 사실을 보호한다. terminal status·timeout·Pod 목록 부재만으로 반납시키지 않는다.
- **Bridge/ACK:** 긴 후속 수정 이력만으로 남은 부채라고 판단하지 않았다. 현행 dependenciesComplete/handoffScope·completed consumer 기반 생략 bridge 판단에 이미 정리가 반영돼 있다.
- **범용화 금지:** workload observer end fence와 metric polling을 같은 lifecycle로 통합하거나, 새로운 service/workflow engine/epoch/fence/recovery framework를 이 보고서 근거만으로 도입하지 않는다. 승인 대기 [architecture rethink](../design/architecture-rethink/README.md)는 현행 채택 계약이 아니다.

## 14. 수정 담당 세션의 실행 경계·완료 조건

1. 먼저 `git status --short`와 해당 symbol의 현행 source/references를 확인한다. 조사 후 다른 세션이 수정한 후보는 중복 수정하지 말고 결과·증거를 대조한다. 줄 번호나 과거 subject만으로 현행 결함을 단정하지 않는다.
2. Core CPU 공유 실행과 Query API 조회 책임 분리를 각 관련 slice의 우선 계약으로 삼는다. R1·R2는 CPU table/YAML만 지우고 quota·affinity 검증·독점 CPU 장부를 남기면 완료가 아니다. R4·R5는 조회 projection·응답 수선을 Query API 소유로 정리하고, ChillServer로 합치거나 이름만 바꾸는 부분 이행을 하지 않는다. 독립적인 수정은 작은 책임 경계로 끝내며 전체 재작성·통합 설정 계층·새 프레임워크로 확대하지 않는다.
3. bug 경로는 변경 전/후 같은 소비자 시나리오로 확인한다. 상수·배선·source text를 재고정하는 테스트 대신 복구 전이·조건 일치·경계 수용·오류 보존을 확인한다. 테스트만으로 실제 경로 smoke를 대체하지 않는다.
4. Go 동작 수정은 focused test 후 `make lint`, `make test`; production package/internal import 변화는 `make arch-check`; golden semantic 경로 변경은 `make gopls-check` 또는 해당 gate를 적용한다. API/generated 계약은 `make manifests generate`, Helm은 명시적 profile render 등 저장소 규칙을 따른다.
5. 저장 evidence/object 계약을 바꿔야 한다면 의존 캠페인 없는 단일 cutover로 한정한다. 원시 archive·과거 publication·실행 기록을 수정/삭제/재게시하지 않고 dual reader/alias/shim/digest 재검증 관문을 추가하지 않는다.
6. 각 slice는 이 보고서의 보존 계약과 수용 기준을 충족하고 기존 설계/문서를 같은 변경에서 갱신한다. 이 보고서의 제안·운영 검증 여부를 실제 수행 결과와 구분해 기록한다.
7. live cluster 변경·image publication·외부 agent 설정 변경은 별도 사용자 요청 없이는 하지 않는다. 예약 해제·reset/복원·자동 재측정 권한을 리팩토링에 끼워 넣지 않는다.

### 문서화 직전 작업 트리 관측

문서화 시작의 `git status --short`는 다음 사용자 변경을 보였다. 조사 때의 초기 상태와 다르며 조사자가 만든 수정이 아니다. 이후 상태를 보장하는 목록은 아니다.

```text
 M cmd/chill-server/evaluation_load_service.go
 M cmd/chill-server/evaluation_load_service_test.go
 M docs/plans/2026-11-01-evaluationrun-dashboard-handoff.md
 M internal/serving/httpapi/evaluation_load.go
 M internal/serving/httpapi/evaluation_load_test.go
?? kappa
```

조사 과정에서 production code·설정·기존 사용자 변경은 수정하지 않았다. `.audit-observer/`, `.audit-capacity/`, `/tmp/chill-audit-profile-main.go`, `/tmp/chill-audit-profile-overlay.json`은 제거를 확인했다. 이번 문서화의 산출물은 본 보고서와 reports 색인 링크뿐이다. 삭제 예상 LOC·dependency 절감량·성능 개선량은 구현 전이므로 산정하지 않았다.
