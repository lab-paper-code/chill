# Serving: Workload 시간창 부하 집계 구현 계획

**상태:** 스케줄러 구현 설계에 통합 — [확정 설계](../design/serving/scheduler-implementation.md)의 운영 관측과 후보 입력에 이 계획의 지정 구간 분석을 연결한다. 아래는 최초 커밋 후보의 기록이다.

**목표:** 기존 CHILL 관측 기록으로 지정 구간의 Workload 수신·접수·배정 요청 수와 요청률, 인스턴스별 해당 Workload 배정량을 계산해 기존 관측 API에 제공한다.

**예정 커밋:** `feat(serving): expose windowed workload load observations`

**선행 기준:** [스케줄러 SRS](../spec/scheduler/SRS.md)의 입력·출력·책임과 수용 기준을 먼저 검토한다. 이 계획은 지정 구간의 재현 가능한 부하 분석 후보이지 운영 스케줄러 입력 전체를 완성하는 작업이 아니다. SRS에 필요한 관측과 기존 계측의 재사용 범위를 확인한 뒤 구현 범위·순서를 다시 결정한다.

## 1. 판단 근거와 작업 경계

요구사항의 권위는 [시스템 명세](../spec/README.md), [라우팅·배칭 명세](../spec/03_routing.md), [구성 선택 명세](../spec/04_configuration_selection.md)에 있다. 현재 구현 여부는 실제 소스로 확인한다. 설계 문서의 완료·제외 선언으로 spec의 범위를 대체하지 않는다.

다른 세션이 함께 작업하므로 구현을 시작할 때 최신 `git status --short`, 관련 spec·소스·호출자를 다시 확인하고, 파일 수정 직전에 대상 파일을 다시 읽는다. 기존 사용자 변경은 보존한다.

이 커밋 후보는 CHILL 측 지정 구간의 부하 분석 조회를 제공하는 작업이다. 별도 담당자가 작업하는 `/home/genesis1/ETRI/donghwan/firstfloor`와 Triton 측 코드·프로토콜은 변경하지 않으며, 그 작업의 완료를 전제로 하지 않는다.

## 2. 직접 커버하는 spec

| 명세 위치 | 이번 커밋의 기여 | 남는 범위 |
|---|---|---|
| `03_routing.md` — 워크로드 전체 부하와 인스턴스별 부하 | Workload 유입량과 인스턴스별 해당 Workload 배정량을 분리한다 | native 수신 확인, 인스턴스·노드의 모든 Workload 합산 부하, 큐·실행 상태 이력 |
| `03_routing.md` — 부하 단위와 관측값·계산값 구분 | 요청 수와 requests/s, 계산 구간을 함께 제공한다 | 배치 실행·전력·후보 예측값과 결합한 의사결정 |
| `04_configuration_selection.md` — 입력과 결정의 부하 입력 | 구성 선택에 사용할 수 있는 유입·배정 부하 일부를 마련한다 | 요청별 완료 시각·SLO 예측, 후보 비교, 구성 선택 자체 |
| `spec/README.md` — SLO 관측과 집계 | 보존된 원시 기록과 명시적 시간 구간으로 부하를 재계산한다 | 운영 이동 구간을 반복 조회하는 소비자·제어 루프와 그 설정 |

특히 spec의 “서빙 파드에 전달한 요청”을 이번 `assigned` 집계만으로 완전히 충족했다고 주장하지 않는다. 현재 `assigned`는 backend 호출 전에 기록되는 CHILL의 배정 사건이며, native 수신·실행의 증거가 아니다.

## 3. 작성 시 확인한 구현 기반

- [Runtime](../../internal/serving/runtime.go)의 `Receive`, `Accept`, `dispatchLocked`는 각각 `received`, `accepted`, `assigned` 사건을 기록한다.
- 요청 기록에는 Request ID, Workload UID, 사건 시각과 배정 정보가 있다. 접수 전 수신·거절 기록에는 Definition이 없을 수 있다.
- `dispatchLocked`는 배정 사건을 영속화한 뒤 `execute`를 시작한다. 후속 사건에도 같은 Assignment가 복사될 수 있다.
- [관측 구현](../../internal/serving/observation_log.go)의 `collectObservationFacts`는 해당 UID의 요청 기록을 수신 구간·결과 필터와 무관하게 수집한다.
- 기존 `aggregate`는 수신 시각 기준 모집단으로 결과·지연을 계산한다. 이 모집단을 배정 요청률에 그대로 사용하면 구간 전에 수신된 backlog를 놓친다.
- 기존 `ObservationLog.Query`는 전체 집계 후 원시 이벤트를 페이지로 나눈다. 새 집계도 이 순서를 유지한다.
- [Dashboard 응답 타입](../../chill-dashboard/lib/read-types.ts)에도 관측 응답 계약이 선언되어 있어 함께 갱신해야 한다.

새 저장소·캐시·주기적 수집기 없이 이 경로를 재사용한다. 원시 관측 기록 schema와 기존 결과·지연 집계 의미는 바꾸지 않는다.

## 4. 집계 계약

### 사건별 카운터

| 집계 | 포함 사건 | 구간 판단 시각 | 의미 |
|---|---|---|---|
| `received` | `kind == received` | `ReceivedAt` | 해당 Workload UID로 기록된 수신 요청. 이후 거절도 포함 |
| `accepted` | `kind == accepted` | `At` | CHILL이 입력·접수 조건을 확인하고 처리를 맡긴 요청 |
| `assigned` | `kind == assigned`이고 Assignment가 있는 사건 | `Assignment.DispatchedAt` | CHILL이 처리 인스턴스를 배정한 요청 |

- 시간 구간은 `[from, to)`이며 시작 포함, 끝 제외다.
- 요청률은 `사건별 요청 수 / (to - from).Seconds()`로 계산한다.
- 각 Request ID는 각 단계에서 한 번만 센다. 후속 사건에 복사된 Assignment를 다시 세지 않는다.
- 배치 수·배치 크기를 요청 수로 대체하지 않는다. 유효한 단일 입력 요청의 requests/s는 spec의 inputs/s와 같다. 접수 전 거절을 포함하는 `received`는 유효 추론 입력률이라고 부르지 않는다.
- 구간 전에 수신되고 구간 안에 배정된 요청은 수신량에서 제외하고 배정량에 포함한다. 같은 시간창에서 `received >= accepted >= assigned`를 강제하지 않는다.
- 기록되지 않은 사건을 다른 단계의 사건에서 추정해 채우지 않는다.

### 식별과 모집단

- Workload 이름이 아니라 UID로 집계한다. 삭제 후 같은 이름으로 등록한 Workload의 기록을 합치지 않는다.
- 인스턴스 집계 키는 과거 Assignment의 POOL·노드 UID·물리 인스턴스 식별자다. 응답의 노드 이름도 당시 기록을 사용한다.
- `instances`는 해당 UID의 배정량 분해이며, 모든 Workload를 합친 인스턴스 전체 부하가 아니다.
- 새 `load`는 해당 UID의 전체 Definition·전체 outcome을 대상으로 한다. 요청의 `definition`·`outcome` 필터와 ObservationLogConfig의 기본 분석 필터는 기존 분석 필드에만 적용한다.
- 이는 Definition이 없는 접수 전 수신 기록과 실패·거절을 전체 유입량에서 숨기지 않기 위한 계약이다.
- 같은 ObservationLog 상태에서 `limit`·`cursor`를 바꿔도 `load`는 동일하다. 페이지 사이에 새 기록이 생긴 경우까지 동일한 snapshot을 보장하는 기능은 추가하지 않는다.

## 5. 관측 API 변경안

새 endpoint 없이 기존 경로를 사용한다.

```text
GET /api/v1/observations/workloads/{uid}?from=…&to=…
```

기존 응답에 nullable `load` 필드를 추가한다. 아래는 설명용 예시이며 실측 결과가 아니다.

```json
{
  "load": {
    "windowSeconds": 10,
    "received": { "requests": 20, "requestsPerSecond": 2 },
    "accepted": { "requests": 18, "requestsPerSecond": 1.8 },
    "assigned": { "requests": 12, "requestsPerSecond": 1.2 },
    "instances": [
      {
        "pool": "pool-a",
        "node": { "name": "node-a", "uid": "node-uid-a" },
        "instance": "physical-instance-a",
        "assigned": { "requests": 12, "requestsPerSecond": 1.2 }
      }
    ]
  }
}
```

- `from`·`to`가 모두 명시되고 양의 구간이면 `load`를 계산한다. 유효하지 않은 구간은 기존 조회 검증을 따른다.
- 한쪽 경계라도 생략하면 기존 조회는 유지하고 `load: null`을 반환한다. 임의의 lookback이나 분모를 만들지 않는다.
- 정상적으로 관측된 무요청 구간은 카운터·요청률 0, `instances: []`로 표현한다.
- 원시 기록 읽기 오류는 오류로 반환한다. 불완전 기록은 기존 `incompleteWindows` 표시를 유지하며, 이를 감춘 정상 집계로 제시하지 않는다.
- 계산값은 보존된 기록 기준 관측값이다. 기록이 없다는 이유만으로 해당 기간 전체가 정상 관측되었다고 보증하지 않는다. 새로운 복구·coverage 상태 머신은 이 커밋에 넣지 않는다.
- 기존 `total`, `selected`, `outcomes`, `cohorts`, 지연 표본·백분위와 원시 이벤트 페이지의 의미는 유지한다.

## 6. 단일 커밋의 구현 순서와 파일 범위

아래 항목은 하나의 구현 커밋 안에서 수행한다. 현재 모두 미구현이다.

- [ ] 최신 spec·소스·호출자를 확인하고, 기존 테스트 방식에 맞춰 사건 경계와 backlog의 회귀 사례를 추가한다.
- [ ] `collectObservationFacts`의 UID 전체 기록에서 사건별 부하를 계산하고, 원시 이벤트 페이지를 나누기 전에 `ObservationResult.load`에 넣는다. 기존 구간 판정 helper를 재사용한다.
- [ ] Dashboard 응답 타입을 동기화한다. 새 화면·차트는 추가하지 않는다.
- [ ] smoke와 해당 repository gate를 실행하고 기존 API 구현 문서에 계약을 기록한다.

| 파일 | 예정 변경 |
|---|---|
| [internal/serving/observation_log.go](../../internal/serving/observation_log.go) | 응답 타입과 사건별 집계, 기존 Query 연결 |
| [internal/serving/observation_log_test.go](../../internal/serving/observation_log_test.go) | 아래 소비자 관점 회귀 검증 |
| [chill-dashboard/lib/read-types.ts](../../chill-dashboard/lib/read-types.ts) | nullable load 응답 타입 동기화 |
| [docs/design/serving/workload-management.md](../design/serving/workload-management.md) | 5.1·11장의 관측 API·집계 계약 갱신 |

기존 handler의 Query 호출·JSON 응답을 사용한다. 새 라우트·쿼리 인자나 별도 handler 계층은 추가하지 않는다. 구현 중 최신 호출자에 변화가 있으면 필요한 계약 동기화만 포함한다.

## 7. 완료 기준과 검증

다음 소비자에게 보이는 동작을 기존 테스트 안에서 검증한다. 필드 전달만 확인하는 테스트나 source-text 검사는 추가하지 않는다.

- 시작 경계 포함·종료 경계 제외를 각 사건 시각에 적용한다.
- 구간 전 수신·구간 내 배정을 놓치지 않는다.
- 접수 전 거절은 수신량에만 포함하고, 분석 필터로 전체 부하가 바뀌지 않는다. 설정된 기본 분석 필터도 같은 규칙을 따른다.
- 여러 인스턴스의 해당 Workload 배정량을 분리하고, 후속 backend·응답 사건의 Assignment를 중복 집계하지 않는다.
- 같은 이름의 다른 UID를 분리한다.
- 같은 ObservationLog 상태에서 페이지 크기·cursor에 관계없이 동일한 `load`를 반환한다.
- 정상 관측 무요청 구간, 경계 생략 시 null, 읽기 오류·불완전 기록 표시를 구분한다.
- 기존 수신 cohort, 결과 분류와 지연 통계의 동작을 유지한다.

구현 후 실행할 검증이며, 이 계획 문서의 작성만으로 통과했다고 간주하지 않는다.

```bash
go test ./internal/serving/...
make lint
make test
pnpm --dir chill-dashboard typecheck
```

테스트와 별도로 임시 ObservationLog에 수신·접수·배정·응답 기록을 넣고 실제 `ObservationLog.Query`를 호출하는 smoke를 수행한다. backlog·구간 경계·페이지 변경의 반환값을 관찰하고, 임시 파일은 제거한다.

현재 파일 범위에서는 production package·내부 import 경계, Kubernetes API·생성물, Helm·배포 토폴로지를 변경하지 않는다. 이 표면이 실제로 변경되면 repository 지침의 추가 gate를 적용한다. 기존 Go semantic golden query 경로에 영향을 주는 변경이면 `make gopls-check`도 수행한다.

이 작업은 live cluster를 수정하거나 live-cluster 동작을 검증하지 않는다.

## 8. 포함하지 않는 작업

- Triton·firstfloor 코드 또는 native protocol 변경, 실제 native 수신·실행률 집계
- 배치 크기 관측 계약·저장 schema 변경
- 인스턴스의 모든 Workload 합산 부하와 노드 합계
- 큐·실행 상태의 시간 이력, 완료 처리율의 별도 사건 집계
- SLO 예상 완료 시각 계산, 에너지 후보 비교, configuration selection·전환 실행
- 운영 이동 구간을 반복 조회하는 scheduler loop와 임의의 고정 lookback
- CRD·저장 schema·새 생산 패키지·캐시·복구 상태 머신
- Dashboard의 새 부하 화면·차트

이 제외 목록은 이번 단일 커밋의 경계이며, spec 전체에서 해당 요구사항을 삭제하거나 구현 완료로 처리한다는 뜻이 아니다.
