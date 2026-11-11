# CHILL 유지보수성 개선 후보: Before / After

상태: 검토용 제안. 기준일: 2026-11-11. 조사 HEAD: `f90f9d24e21c8b14166adf2b962ad06ee07abc29`.
[외부 프로젝트 참고 문서][R1]에서 추출한 분리 방식과 네이밍을 CHILL의 현재 코드에 대입했다.
Before는 소스에서 확인한 구조다. After의 경로와 이름, 우선순위, 기대 효과는 **[INFERENCE]**인 설계 제안이다.
구현 범위와 최종 이름은 각 후보를 작업으로 선택할 때 확정한다. C3와 C4의 After는 보류 중인 재검토안이다.

## 1. 후보 요약

| 후보 | 리뷰 반영 판정 | 실제 개선 효과 | 작업 시점 |
|---|---|---|---|
| C2 | 검증 계획 보완 후 우선 추진 | 화면의 직접 조회 책임을 기존 hook으로 이동 | 첫 적용 후보 |
| C1 | 분리 범위 수정 | catalog 코드의 탐색성 개선 | catalog 변경 작업에 병행 |
| C5 | 유지, 단위 표기 조정 | 내부 이름에서 단위와 fallback 동작 표현 | 관련 코드 수정 시 병행 |
| C3 | 보류 | 전송 코드의 독립된 읽기 위치 | 전송 변경 수요와 소비자 전환 비용을 확인할 때 재검토 |
| C4 | 보류 | 전달용 인터페이스 하나 제거 | client 연결 방식과 공유 fixture를 함께 정리할 때 재검토 |

C2는 기존 조회 소유자로 책임을 모으는 효과가 구체적이다. C1은 같은 Manager 안에서 읽기 위치를 나눈다.
C3와 C4는 넓은 소비자 전환에 비해 현재 확인한 이점이 작아 보류한다.
외부 사례는 배치 방식의 참고자료로 사용하고, 우선순위는 CHILL의 실제 변경 경로와 검증 부담으로 판단한다.

## 2. C1: catalog 변경을 같은 Manager의 역할 파일로 분리

### 현재 근거

[`management/manager.go`][S1]에는 Manager 상태, Workload 변경, operation 진행, catalog CRUD와 참조 보호가 함께 있다.
915행 이후는 Model, Preprocessor, Dataset 변경과 참조 검사다. `referenceWorkloads`는 삭제 확인 중인 객체까지 참조 보호에 포함한다.
`startSharedUpdate`는 그룹 admission을 닫고 accepted 상태를 기록하며, `advanceGroup`이 drain 이후 적용을 진행한다.
이 코드들은 같은 Manager 상태와 lock을 공유한다.

### Before / After

| Before: 현재 | After: 제안 |
|---|---|
| `manager.go`: 상태, 복구, Workload 변경, operation 진행, catalog 변경 | `manager.go`: 기존 상태 소유자, 복구, Workload 변경, 공유 업데이트 접수와 진행 |
| catalog CRUD와 참조 보호가 같은 파일의 후반부에 위치 | 신규 `manager_catalog.go`: catalog CRUD와 참조 검사 |
| `projection.go`, `workload_view.go` | 기존 위치와 역할 유지 |

`manager_catalog.go`는 같은 `management` 패키지에서 기존 `*Manager` 메서드를 나눈다.
이동 단위는 다음과 같이 정한다.

- `manager_catalog.go`: Model, Preprocessor, Dataset CRUD와 `holds*`, `referencing`, `workloadNames`, `referenceWorkloads`, catalog 검증 및 참조 보호 helper.
- `manager.go`: `sharedGroup`, `settled`, `startSharedUpdate`, `sharedUpdate`, `sameGroup`, `advanceGroup`을 포함한 공유 업데이트 접수와 진행.
- `manager.go`: Workload 등록과 catalog 경로가 함께 사용하는 `catalogBusy`, `loadReferences`, `validatedReferences`, `checkReference`, `catalogRefs`.

**기대 효과:** catalog CRUD와 참조 검사를 찾는 위치가 명확해진다.
**구조 변화:** 파일 탐색성 개선. 공유 업데이트의 상태와 호출 관계, 패키지 import 방향은 현재와 같다.

### 보존할 경계

- `mu`, `failed`, `deletionChecks`는 기존 Manager가 소유하고, 상태 갱신 메서드의 lock 규칙을 유지한다.
- 공유 업데이트 접수와 진행은 `manager.go`에 함께 두고, catalog CRUD에서 기존 메서드를 호출한다.
- 삭제 확인 중인 참조 보호, 전체 drain 이후 definition 적용, actor 기록을 보존한다.
- 현재 `NewManager` 호출자와 공개 메서드 signature를 유지한다.

### 검증 범위

[`kube/manager_test.go`][S2]에서 확인한 보장 범위는 다음과 같다.

- 401-475행과 660-722행: 공유 변경 barrier와 drain, actor 기록 및 preprocessor 변경 보호.
- 559-592행과 603-657행: 일반 catalog 참조와 모델 산출물 보호.
- 724-800행: Workload 삭제 reservation과 404 확인.

삭제 확인 중인 Workload를 `referenceWorkloads`가 다시 합치는 조건은 위 reservation 테스트의 검증 범위 밖이다.
해당 보존 조건은 목록에서 사라진 Workload의 확인 GET 실패, 그동안의 catalog 삭제 거부, 404 확인 후 참조 해제를 직접 다루는 시나리오로 보완한다.
참고한 외부 특징: [Volcano의 같은 상태 소유자에 대한 역할별 파일 분할][R2].

## 3. C2: 요청 작성 화면의 프로파일 조회를 기존 hook으로 이동

### 현재 근거

[`features/measurement-request.tsx`][S3]의 85-96행은 요청 기본값에 사용할 프로파일을 전체 페이지 순회로 읽는다.
같은 화면이 `Context`를 조립해 `Funnel`에 전달한다.
[`hooks/use-measurement-data.ts`][S4]에는 이미 `readPage`, 측정 요청 목록, 셀별 프로파일 조회가 있다.
요청 작성용 조회와 셀별 조회는 서로 다른 query key와 조회 범위를 사용한다.

### Before / After

| Before: 현재 | After: 제안 |
|---|---|
| `features/measurement-request.tsx`: 폼, 프로파일 전체 페이지 조회, Context 조립 | 화면: 폼, 조회 hook 호출, Context 조립 |
| `hooks/use-measurement-data.ts`: 기존 측정 조회와 `readPage` | 같은 파일에 `useRequestProfileHistory()` 추가 |
| `lib/measurement-request.ts`: 입력값 기반 draft와 manifest 계산 | 기존 계산과 공유 소비자 유지 |

요청 작성용 query를 기존 hook 파일로 옮기고 `readPage`를 재사용한다.
셀별 `useCoverageProfiles`와 요청 작성용 이력 조회는 각각의 query 함수로 유지한다.

**기대 효과:** 측정 데이터의 페이지 순회, Signal 전달, 정밀 디코딩을 기존 조회 소유 파일에서 찾을 수 있다.
**구조 변화:** 화면의 직접 HTTP 책임을 기존 조회 hook으로 모은다. Context와 계산의 기존 경계는 유지한다.

### 보존할 경계

- 요청 이력 key `request-profile-history`와 셀별 key `coverage-profile-times`를 각각 유지한다.
- summary 조회 조건, `limit=50`, offset 전진 검사와 한 요청의 AbortSignal 전달을 보존한다.
- 후속 페이지 실패는 조회 실패로 전달하고, Context와 draft의 상태 소유자는 화면에 둔다.
- `lib/measurement-request.ts`의 공유 helper는 catalog 화면과 다른 측정 화면도 사용하므로 기존 위치를 유지한다.

### 검증 범위

| 기존 테스트 | 확인한 범위 | 추출 시 필요한 연결 |
|---|---|---|
| [측정 조회 테스트][S5] 11-38행 | `useMeasurementPlans`의 합산, offset 정체, 후속 실패와 같은 Signal 전달 | 새 요청 이력 hook의 직접 검증과 구분 |
| [요약 응답 오류 테스트][S6] 85-132행 | 요청 화면의 두 번째 프로파일 페이지 디코딩 실패와 오류 표시 | loader에서 새 hook과 실제 query를 연결 |
| [제출 hook 테스트][S7] 304-326행 | profiles 실패 중에도 측정 종류 선택 유지 | `useQuery`의 profiles 실패 주입을 새 hook 경계로 이동 |

추출한 `useRequestProfileHistory`에는 정상 두 페이지 합산, offset 정체, 후속 실패, 동일 Signal 전달을 직접 검증한다.
S5의 `aborted` 분기는 일반 오류를 던진다. 실제 취소 보장이 필요하면 AbortController를 취소하는 경로와 후속 요청 중단을 검증한다.
이번 범위는 hook 추출과 기존 `readPage` 재사용이다. 두 query는 각각의 key와 조회 조건을 유지한다.
참고한 외부 특징: [Kueue의 조회 결과와 계산 입력 구분][R3]. C2에서 바뀌는 경계는 화면과 조회 hook 사이의 책임 배치다.

## 4. C3: HTTP 전송을 세션 상태와 분리

**판정: 보류.** 전송 관련 변경에서 분리 수요와 전체 소비자 및 테스트 전환 범위가 구체화될 때 재검토한다.

### 현재 근거

[`lib/client.tsx`][S8]는 `ApiError`, `decodeResponse`, `relay`와 함께 SessionProvider, query 및 mutation hook, 계정 UI를 담고 있다.
전송 함수는 token과 AbortSignal을 인자로 받고, 세션 작성과 query 취소는 SessionProvider와 hook이 소유한다.
검토한 [측정 조회 hook][S4], [live hook][S25], [실험 결과 화면][S26]은 전송 이름과 세션 또는 query hook 이름을 함께 가져온다.

### Before / After: 보류 중인 검토안

| Before: 현재 | After: 제안 |
|---|---|
| `client.tsx`: 전송, 응답 및 오류 해석, 세션 상태, query/mutation hook, 계정 UI | `client.tsx`: 기존 세션 상태, query/mutation hook, 계정 UI |
| 전송과 세션 관련 이름을 같은 모듈에서 import | 신규 `client-transport.ts`: `RelayResponse`, `ApiError`, `errorDetails`, `decodeResponse`, `relay`, warming 판단과 정책 함수 |
| 소비자가 단일 모듈에서 여러 책임을 가져옴 | 소비자가 전송 모듈과 세션 모듈에서 필요한 이름을 직접 import |

두 파일은 기존 `lib` 디렉토리에 둔다. 파일명에 대상과 역할을 함께 표현한다.
이동한 export의 모든 소비자를 새 소유 파일로 전환한다.

**기대 효과:** HTTP 오류와 응답 해석의 소유 파일을 따로 읽을 수 있다.
**현재 비용:** 검토한 소비자는 세션 모듈도 계속 사용하며, 역할별 import와 수동 module map 전환이 추가된다.
전송 코드는 decoder identity와 측정 summary 협상 규칙도 포함한다. 이 업무 규칙의 소유 위치를 분리안에 함께 명시한다.

### 보존할 경계

- token, signals, 세션 epoch, QueryClient와 identity 변경 시 취소 순서는 `client.tsx`에 남긴다.
- mutation의 AbortController와 재시도 수명도 기존 hook에서 관리한다.
- `ApiError`는 새 소유 파일의 한 정의를 사용하고, `instanceof`와 decoder 함수 identity를 보존한다.
- 결과 미확인, 토큰 가림, 원래 HTTP 상태와 헤더, RTT, summary 협상 재호출을 유지한다.
- `client.tsx`를 직접 변환하는 테스트의 loader와 module map은 새 전송 모듈을 실제로 연결한다.

### 검증 범위

- [mutation 수명 테스트][S9] 13-35행: 세션 객체를 수동 변경해 unmount와 logout 이후 추가 HTTP 요청 중단을 확인한다.
- [결과 전송 테스트][S10] 71-101행: 구독 해제와 재구독 시 취소와 결과 복원을 확인한다.
- [warming 및 조건부 전송 테스트][S21] 20-39행과 81-99행: warming 재시도, Retry-After, ETag와 조건부 요청을 확인한다.
- [summary 협상 테스트][S22] 23-70행: 실제 relay 경로의 협상과 기존 응답 처리, 권한 및 다른 오류의 전달을 확인한다.

S9와 S10은 `SessionProvider.changeIdentity`의 취소와 캐시 교체 순서를 실행하지 않는다.
Provider 순서는 별도 확인 항목으로 두고, 전송 분리 시 위 실제 전송 계약과 loader, 단일 `ApiError` 정의를 함께 연결한다.
참고한 외부 특징: [KEDA의 개별 구현과 공통 지원 책임 분리][R4].

## 5. C4: Operations의 실제 객체 client 의존성 명시

**판정: 보류.** 전달용 계약 제거의 이점과 공유 client 및 테스트 fixture 전환 비용을 함께 비교한 뒤 재검토한다.

### 현재 근거

[`operations/dependencies.go`][S11]의 `objectStore`는 `Client() client.Client` 한 메서드를 선언한다.
[`Operations`][S12]는 이 계약을 보관하고 실제 객체 접근에는 `o.Store.Client()`를 사용한다.
Pod 생성, UID 조건 삭제, routing 저장 등은 이미 Operations 역할 파일에서 수행한다.
이 계약이 노출하는 능력은 반환된 Kubernetes 객체 client의 능력이다.

### Before / After: 보류 중인 검토안

| Before: 현재 | After: 제안 |
|---|---|
| 생성자 인자 `store objectStore` | 생성자 인자 `kubeObjects client.Client` |
| `Operations.Store` | `Operations.KubeObjects` |
| `o.Store.Client().Patch(...)` | `o.KubeObjects.Patch(...)` |
| 조립 위치에서 `store` 전달 | 같은 위치에서 `store.Client()` 전달 |

재검토안은 forwarding 계약을 제거하고 직접 client 인스턴스를 받는다.
`KubeObjects`는 Kubernetes 객체 client를 뜻하며, 조립 코드의 S3 `objects`와 구분한다.
`Kube` typed client, `removalState`, `sourceObserver`, Runtime과 reservation 소유권은 유지한다.

**기대 효과:** 생성자와 필드에서 실제 외부 의존성을 읽을 수 있고, 전달 전용 계약 하나가 줄어든다.
**구조 변화:** 기술 의존성과 객체 전달 방식의 명료화. 객체 client의 접근 권한은 현재와 같다.

### 보존할 경계

- [조립 위치][S13]에서 기존 client 인스턴스를 전달한다.
- 생성자와 `Operations{Store: ...}` 직접 초기화, 실제 역할 파일, 테스트 소비자를 함께 전환한다.
- [routing 저장 실패 테스트][S14] 646-660행은 생성 이후 Store 내부 client를 교체한다.
- [reservation tick 테스트][S23] 87-106행과 [측정 순서 테스트][S24] 134-170행도 기존 fixture의 Store client를 바꿔 Pod 생성 interceptor와 로그를 연결한다.
- After에서는 생성 시 보관한 client와 이후 Store에 지정한 client가 달라질 수 있다. Operations, Store, Observer가 사용하는 연결과 interceptor가 적용되는 위치를 fixture 단위로 함께 맞춘다.
- 교체 예시는 실제 읽은 테스트 범위다. 전체 호출자 조사에는 생성 후 Store client 대입도 포함하며, 실행 영향은 구현 후 확인한다.
- typed-nil 거부, UID precondition, optimistic patch, finalizer와 404 확인, Runtime retirement 순서를 유지한다.

전달용 인터페이스 제거는 단순화 효과가 있다. 현재 객체 접근 권한과 Kubernetes 의존성은 그대로이며, 테스트의 연결 방식까지 바꾸는 비용을 포함해 보류한다.
참고한 외부 특징: [KEDA의 생성 위치와 실제 의존성 전달][R4]. 현재 조립 위치는 이미 명시적이며, 이 사례는 배치 방식의 참고다.

## 6. C5: 내부 이름에 시간 단위와 선택 동작 표현

### 현재 근거

[`Runtime`][S15]의 `completionMargin float64`는 setter의 `ms` 값을 저장하고 `Dispatch.CompletionMarginMS`로 전달한다.
[`scheduler.go`][S16]의 비공개 타입 `second`는 v2가 오류 없이 결과를 찾지 못했을 때 v1으로 fallback한다.
gopls 참조 조회에서 margin 필드는 선언, dispatch 전달, setter에 나타났고 `second`는 같은 파일의 선언과 사용에 나타났다.

### Before / After

| Before: 현재 | After: 제안 |
|---|---|
| `completionMargin float64` | `completionMarginMS float64` |
| 비공개 selector 구현 타입 `second` | `v2WithV1Fallback` |

**기대 효과:** 필드의 단위와 구현 타입의 동작을 선언에서 읽을 수 있다.
**구조 변화:** 내부 이름만 정리하며 기존 수치 표현과 전달 경로를 유지한다.

시간의 숫자 변환, JSON 필드, 설정 이름, 검색 전략 값, 결과와 diagnostics는 현재 계약을 유지한다.
기존 `Dispatch.CompletionMarginMS`와 같은 단위 표기를 사용한다. 관련 코드 수정에 병행하고, fallback은 [기존 회귀 테스트][S17]의 native 결과와 v2 종료 사유 보존을 확인한다.
참고한 외부 특징: [외부 사례의 범위별 네이밍][R1].

## 7. 유지할 구조와 구현 시 확인 범위

- 기존 기능별 패키지 배치와 [승인된 import 방향][S18]을 유지한다. 새 역할 파일은 현재 패키지 안에 둔다.
- `scheduler/protocol`의 독립 계약과 v1/v2의 방향을 유지한다. 현재 `go list`에서도 두 구현의 CHILL 내부 import는 protocol뿐이다.
- Runtime의 요청, instance, routing 상태와 lock, observation 경계를 같은 소유권 아래 유지한다.
- API와 저장 형태, 불변 실행 조건, 측정과 결과 판정의 의미를 보존한다. 저장 변경은 [ADR 0019][S19]를 따른다.
- 대시보드의 업무 계산, 제출 상태, 세션 수명은 각 기존 소유자가 관리한다.

Go 구현 변경은 관련 focused test와 저장소의 lint, test, architecture 규칙을 적용한다.
대시보드 구현 변경은 기존 typecheck, lint, test와 영향 화면의 실제 동작을 확인한다.
현재 [테스트 명령][S20]은 최상위 테스트 파일을 지정하므로 후보의 테스트 위치도 유지한다.

이 문서 작성에서는 소스, 관련 테스트 본문, gopls 참조와 선택 패키지의 `go list`를 확인했다.
동작 테스트와 lint, 화면 및 live cluster 검증은 구현 작업에서 수행한다.
외부 사례 문서의 범위와 원문은 그대로 유지한다.

[R1]: external-maintainability-patterns.md
[R2]: external-maintainability-patterns.md#3-volcano-실행-단계와-정책을-구분한다
[R3]: external-maintainability-patterns.md#2-kueue-상태의-수명과-계산-입력을-구분한다
[R4]: external-maintainability-patterns.md#4-keda-공급자-구현을-공통-계약으로-연결한다
[S1]: ../../../internal/serving/management/manager.go
[S2]: ../../../internal/serving/kube/manager_test.go
[S3]: ../../../chill-dashboard/features/measurement-request.tsx
[S4]: ../../../chill-dashboard/hooks/use-measurement-data.ts
[S5]: ../../../chill-dashboard/hooks/use-measurement-data.test.ts
[S6]: ../../../chill-dashboard/lib/measurement-summary-errors.test.ts
[S7]: ../../../chill-dashboard/hooks/use-measurement-submission.test.ts
[S8]: ../../../chill-dashboard/lib/client.tsx
[S9]: ../../../chill-dashboard/lib/client-mutation-lifecycle.test.ts
[S10]: ../../../chill-dashboard/lib/experiment-result-transport.test.ts
[S11]: ../../../internal/serving/operations/dependencies.go
[S12]: ../../../internal/serving/operations/operations.go
[S13]: ../../../cmd/chill-server/scheduler_wiring.go
[S14]: ../../../internal/serving/kube/operations_test.go
[S15]: ../../../internal/serving/runtime.go
[S16]: ../../../internal/scheduler/scheduler.go
[S17]: ../../../internal/scheduler/scheduler_test.go
[S18]: ../../../.go-arch-lint.yml
[S19]: ../../adr/0019-durable-stored-contract-compatibility.md
[S20]: ../../../chill-dashboard/package.json
[S21]: ../../../chill-dashboard/lib/client-warming.test.ts
[S22]: ../../../chill-dashboard/lib/measurement-compatibility.test.ts
[S23]: ../../../internal/serving/kube/reservation_tick_test.go
[S24]: ../../../internal/serving/kube/measurement_sequence_test.go
[S25]: ../../../chill-dashboard/lib/use-live.tsx
[S26]: ../../../chill-dashboard/features/experiments-conclusion.tsx
