# Dashboard API coverage: 측정 두 종류와 용량 계산 근거

날짜: 2026-10-30. 상태: **문서 설계 완료, 화면 구현 전**.
이 계획은 API 대조 항목 2~4만 다룬다. 본 변경은 코드·배포·클러스터를 바꾸지 않는다.

## 1. 범위와 계약

요구사항은 [Dashboard SRS](../spec/dashboard/SRS.md)의 DB-FR-45, DB-FR-53~55 및
9절의 세 추가 수용 시나리오다. 상세 화면 계약은
[구현 설계 6.5절](../design/dashboard/README.md#65-측정-api와-용량-행-연결-설계-2026-10-30-구현-전)을 따른다.

| 단위 | 사용자 경로 | 서버 API(`/api/v1` 하위) | 결과 |
|---|---|---|---|
| 동작 비용 | `/measurements/operation-cost-measurements?namespace=<ns>` 및 `/[namespace]/[name]?uid=<uid>` 상세 | `GET /namespaces/{ns}/operation-cost-measurements[/{name}]` | 목록 `{items:[MeasurementProjection]}` / 단건 projection |
| 분배 점유시간 d | `/measurements/dispatch-occupancy-measurements?namespace=<ns>` 및 `/[namespace]/[name]?uid=<uid>` 상세 | `GET /namespaces/{ns}/dispatch-occupancy-measurements[/{name}]` | 목록 `{items:[MeasurementProjection]}` / 단건 projection |
| 용량 κ | `/scheduler/capacity?workload=<uid>&row=<key>` | `GET /scheduler/capacity`, 대조용 `GET /scheduler/settings` | `{rows:[CapacityRow]}` / 최상위 SchedulerSettings 필드 |

세 API 모두 이미 서버와 relay에 있고 **API query는 없다**. 위 UI query는 브라우저의
선택·필터·identity용으로 upstream에 전달하지 않는다. 목록의 서버 pagination도 없다.
브라우저 BFF 경로는 `/api/chill/v1/...`, upstream은 `/api/v1/...`다.
새 상위 메뉴 없이 `/measurements`의 기존 더 보기, `/configuration`의 용량 설정 구역,
기존 scheduler 근거에서 연결한다. 기존 hubs가 유지되는 동안도 같은 하위 경로를 쓴다.

### '전환 비용' 결론

`OperationCostMeasurement`와 `PowerModeTransitionCost`는 다르다. 전자는 한 Node에서
power/prepare/idle/remove를 순서대로 재고 단계 key·verdict를 보존한다. 후자는
Serving이 있는 상태에서 두 전력모드의 양방향 반복과 방향별 시도·채택 수를 다룬다.
현재 동작 비용 API로 후자의 시간·J·경계 불확실성·Serving 지속 여부를 제공할 수 없다.

따라서 자료 찾기의 잘못된 `power-mode-transition-costs` / “전환 비용” 선택을
`operation-cost-measurements` / “동작 비용 측정”으로 교체하고 d 선택을 추가한다.
옛 PowerModeTransitionCost URL은 다른 측정으로 redirect하지 않고 종류가 다름과
API 미제공을 안내한다. DB-FR-32는 별도 API가 필요하며 완료로 주장하지 않는다.

## 2. 소스 근거와 설계 원칙

- `internal/serving/httpapi/handler.go`: 두 측정 목록/단건과 scheduler capacity의 GET 등록.
- `httpapi/measurement_projections.go`: whitelist metadata/node/conditions/status/references,
  두 측정의 실제 필드·optional 결과. `httpapi/measurements.go`: `{store,key}` wire 위치와
  MeasurementUnavailable. projection은 전체 CR이 아니다.
- `api/v1alpha1/operationcostmeasurement_types.go`, `powermodetransitioncost_types.go`,
  `dispatchoccupancymeasurement_types.go`: 측정 의미·단위와 투영되지 않은 필드의 경계.
- `httpapi/profiles.go:schedulerCapacity`: 실제 capacity handler는 scheduler.go가 아니라
  profiles.go이며 `{rows:...}`를 반환한다. `httpapi/scheduler.go`: settings 응답 형식.
- `internal/serving/capacity.go`: CapacityInput/CapacityRow/CapacityJudgment, ns/ms/req/s,
  κ 누락과 0, 조기 종료, -1 지연 sentinel, previous 행과 입력별 key.
- `internal/serving/scheduler.go`: SLO·할당·d의 중첩 필드, 현재 설정의 세 탐색 필드.
- `chill-dashboard/lib/relay.ts:relayPlan`: 세 GET 허용, query 없음.
- `features/read-pages.tsx:MeasurementDetail`, `measurement-plans.tsx`, scheduler 화면과
  `components/read-region.tsx`: Passport 순서, UID 차단, 독립 Query와 오류/원본 패턴.
- 저장소 pinned `hack/gopls-chill.sh definition`으로 CapacityInput과 occupancyStatus 정의를
  확인하고 반환 소스와 대조했다. Graphify는 stale 가능성이 있는 방향 탐색으로만 사용했다.

기존 `Screen`, `Facts`, `Rows`, `RowDialog`, `DataTable`, `SourceJSON`, `SourceExport`,
`QueryGate`, `useChillQuery`, `read-types.ts`를 재사용한다. read-only이므로 새 mutation,
공통 화면 factory, 전역 store, 다운로드 API, 새 UI 라이브러리는 추가하지 않는다.
UI 근거는 설계 6.5.6절의 ui-ux-pro-max 채택 규칙 네 줄이며 기존 디자인 규칙이 우선한다.

## 3. 구현 순서와 소유 경계

1. **wire 타입·명시 route**: `lib/read-types.ts`에 실제 투영과 capacity 중첩 타입을 추가하고
   기존 정밀 정수 처리로 ns/bytes를 보존한다. catch-all에 지원 kind/목록/capacity를 명시한다.
   일반 준비 중 분기와 잘못된 선택지를 제거한다. 기존 Passport/BEO route는 유지한다.
2. **동작 비용 목록·상세**: 기존 read-pages의 Passport 순서를 따라 결론, 순서 있는 단계
   조건과 name별 결과, CR identity·논리 근거 위치, 원본 내보내기를 연결한다.
   목록 진입점과 UID deep link를 같은 변경에서 완성한다.
3. **d 목록·상세**: 같은 기존 패턴에 workload/BEO, batch, supplyMode, warmup/duration,
   workers, powerMode, d(ns/request), 문자열 X(req/s), result 위치를 연결한다.
   공급 방식 누락과 d 누락/0을 별도로 처리한다. CRD-only 필드를 화면에 만들지 않는다.
4. **κ 목록·행 Dialog**: 전체 CapacityInput(새 3필드 포함), 결과와 μ/κ, grid/bisection의
   실제 실행 순서·창당 표본·판정·백분위 지연을 표시한다. settings는 독립 조회하여
   8개 계산 설정을 행 입력/현재 적용값으로 대조한다. `/configuration?section=capacity`의
   기존 설정 구역 focus와 양방향 링크까지 연결한다. 값이 없는 key를 최신 행으로 대체하지 않는다.
5. **통합 후 한 번 검사**: 구현 main agent만 아래 완료 기준을 실행한다. 작업 중인 다른
   세션과 동일 파일을 동시에 수정하지 않고 공유 route·타입·진입점은 통합 소유자가 합친다.

## 4. 완료 기준과 실행할 검사

이 문서 commit의 완료는 SRS·설계·계획 세 파일에 소스 계약과 구현 수용 기준을 기록하는 것이다.
아래는 **향후 화면 구현 완료 기준**이며 이 문서 변경에서 실행·통과한 검사가 아니다.

- 세 화면이 정확한 GET API를 호출하고 UI query가 upstream으로 새지 않는다.
  두 측정의 목록과 단건이 모두 열리며 새 상위 내비게이션 없이 진입할 수 있다.
- SRS 9절의 “동작 비용의 실제 API와 원본 identity”, “분배 점유시간의 조건과 누락 결과”,
  “용량 입력과 계산 판정 및 현재 설정 대조”를 충족한다. 설계 6.5절의 모든 필드·문구가 보인다.
- 최소 runnable 회귀 검사를 기존 `lib/*.test.ts` 패턴에 남긴다: 지원 경로·query 경계와
  UID 불일치, d 누락/0·미지정 공급, capacity 누락/0·없는 key·설정 대조·판정 표시를 보호한다.
  화면에서 공유 순수 함수를 쓰면 그 실제 함수만 검사하고 테스트 전용 재구현은 하지 않는다.
- 실제 응답 형태의 fixture에서 Measuring/Complete/Insufficient/Failed, 빈 목록,
  401/403/404/503/timeout, Built/CapacityPending/CapacityRejected/previous,
  NonMonotone/MissingDispatchOccupancy/BatchServiceExceedsSLO, grid/bisection 미제공,
  percentileMS=-1·조기 종료와 settings만 실패한 상태를 확인한다.
- Node 24를 사용해 `chill-dashboard/`에서 통합 후 한 번 실행하고 결과를 기록한다:
  `nice -n 19 npx -y node@24 node_modules/typescript/bin/tsc --noEmit`,
  `nice -n 19 npx -y node@24 node_modules/eslint/bin/eslint.js .`,
  `nice -n 19 npx -y node@24 --experimental-strip-types --test lib/*.test.ts server/*.test.mjs`.
  필요한 production build도 `nice -n 19 npx -y node@24 node_modules/next/dist/bin/next build`로 실행한다.
- 허용된 읽기 전용 API와 브라우저에서 세 deep link의 새로고침·공유·뒤로가기,
  Dialog 키보드 focus 복원, 좁은 화면 표 가로 스크롤, 오류 `role=alert`,
  원본 export의 identity·단위·시각과 비밀정보 제거를 확인한다. 미실행 항목은 그대로 보고한다.
- 클러스터 변경·배포·측정 생성·설정 저장·재계산 요청은 실행하지 않는다.

## 5. 제외 항목과 이유

| 제외 | 이유 |
|---|---|
| 수동 route/unroute/release `POST /scheduler/operations` relay 수정(P1) | 별도 동시 작업. 이 문서 assignment는 항목 2~4만 담당 |
| raw observations 및 raw Evidence 다운로드 연결 | raw observations는 의도적으로 금지되고 테스트로 고정. 논리 store/key를 HTTP URL로 바꾸지 않음 |
| `GET /systems`, `PUT /api/v1/scheduler` | 이번 API 대조에서 명시 제외한 표면 |
| PowerModeTransitionCost 원본과 create adapter(DB-FR-32) | 현 GET operation-cost-measurements와 다른 계약, 실제 HTTP API 없음. 전환 비용 결과로 위장 불가 |
| 두 측정의 생성·삭제·재시도 UI | 현재 대상은 GET 목록/단건뿐. BEO mutation을 다른 측정에 재사용하지 않음 |
| capacity 서버 query·역사 조회·재계산·실측 검증 | 현 API는 현재 rows만 제공. 원래 판단 기록과 key는 보존하되 새 API나 통계 계산을 발명하지 않음 |
| 전체 라이브 무대 재설계, 새 내비 체계, 클러스터 배포 | 기존 운영자·측정/scheduler 진입점 재사용으로 충분하며 본 변경은 문서만 |

## 6. 이번 문서 작업의 확인과 전달

Go 소스·현재 화면·relay 정적 대조와 pinned gopls 정의 탐색을 수행했다.
ui-ux-pro-max는 UX 두 질의와 Next.js 질의/구체 재질의까지 총 4회 수행했다.
Next.js의 첫 일반 질의는 0건, 구체 `error loading` 질의는 결과를 반환했다.
빌드·테스트·lint·브라우저·실클러스터 검증은 문서 전용 assignment이므로 실행하지 않는다.
경로 한정 commit은 `docs(dashboard): cover measurement and capacity APIs`로 하고
이 계획, `docs/spec/dashboard/SRS.md`, `docs/design/dashboard/README.md`만 포함한다.
사용자 미커밋 파일과 `docs/intent/decision-log.md`는 건드리지 않는다.
