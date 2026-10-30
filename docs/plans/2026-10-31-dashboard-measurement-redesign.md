# 운영 중심 측정·실험 대시보드 5차 구조 재설계

- 설계일 2026-10-30, 구현 묶음 식별일 2026-10-31. **교차 모델 검토·U1/U2/U3 코드 반영, 배포·성능·재설문 수용 전**이다.
- 대상 사용자: **엣지 클러스터 운영 담당자**. ‘스케줄러가 쓸 수 있는가/운영에 쓰려면 무엇이 부족한가’를 먼저 답한다.
- 사용자 결정: 평균 SUS **70 이상까지 반복**, 기존 구조 변경 허용, 탭 sessionStorage 승인.
- 정본: [화면 §9](../design/dashboard/README.md#9-운영-중심-측정실험-5차-재설계),
  [API §13](../design/serving/measurement-api.md#13-측정-커버리지-읽기-projection-u1), [SRS](../spec/dashboard/SRS.md).
- 이전 UX 계획의 브라우저 전수 집계·운영 보드 홈·7단계·select 내비를 대체한다.
  B0의 불변 저장·resolver·owner·통계 유도 계약은 유지한다. 구현은 아래 슬라이스 경계로 반영하며 클러스터와 과거 publication은 변경하지 않는다.

- 사전 운영 리뷰 보정: 목표 배치 부족은 서버 공통 판정에서 자식 결과보다 우선한다.
  실패 요청이 없는 선언 충돌도 다음 조치에 포함하며, 조회 실패는 조치 없음으로 표시하지 않는다.
  커버리지 숫자는 사용 가능/지원 확인으로 라벨링하고, 전력모드는 선언에 W 한도가 없으므로
  실측 active W 범위를 표시한다(미측정은 전력 미확인; 장치 한도로 오인하지 않는다).

## 1. 근거와 교차 검토 결론

기존 평가 평균은 27.5→38.3→44.2→45.0이다. 4차 researcher/advisor/uxexpert는
37.5/60/37.5였으며 **과거 평가 역할**이지 제품 사용자/메뉴 이름이 아니다.
원자료는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/ux-survey-20261030/`의
SYNTHESIS*.md와 researcher-r4.md/advisor-r4.md/uxexpert-r4.md다.
직접 본 장면은 `advisor-r4-01.png`(운영 보드가 홈 점유),
`uxexpert-r4-13-coverage-cell-agx-orin.png`(지원 분모 실패),
`uxexpert-r4-30-measurement-new-step7.png`(검사 원인 은폐),
`researcher-r4-48-exp-charts.png`(서비스 단일 차트)다.

2026-10-30 10:47:04 UTC부터 지정 CA/bearer로 GET만 수행한 단면(각 1회, 벤치마크 아님):

| GET (`/api/v1` 이하) | 관측 | 경과 |
|---|---|---:|
| `/inventory?limit=200` | 모델 3/class 8, 양쪽 page 끝 | 0.052s |
| `/model-compatibility-matrix` | 대표 정적 판정 120행 | 7.690s |
| `/namespaces/chill-system/batch-energy-optimizations` | 88 요청 | 0.070s |
| `/namespaces/chill-system/profiling-runs?limit=1` | 총 1,155 중 1행만 조회 | 3.861s |
| `/scheduler/profiles` | profile 78/insufficiency 2/reuseCheck 1 | 0.198s |
| `/experiments` | 게시 기록 6 | 0.863s |

토큰/Authorization은 출력하지 않았다. 모든 raw를 확인한 조사도, atomic snapshot도 아니다.
교차 검토 3건 `R5Rev1/2/3`과 Main 종합 결정을 반영했다.
정상 timestamp 각주를 문제로 올리는 판정, Console 78건 이중 raw 로드, 단순 8개 resolver 병렬화,
고정 batch 산출물의 무조건 부분집합 요청, 빠른 503만으로 성능 통과하는 안은 폐기한다.

## 2. 시스템 중립 어휘와 IA

UI/API/예정 파일 이름을 clean cutover한다. 옛 어휘의 alias/re-export를 남기지 않는다.

| 제품 표현 | 확정 어휘/식별자 |
|---|---|
| 첫 화면 | **개요**, `features/overview.tsx` |
| 측정 최소 체크 충족 | **기준 충족**, `criteria_met`, `criteriaMetCellCount` |
| 결과 체크 | `result-checklist.v1`, `MeasurementAssessment.checklist` |
| 수치 한 행/내보내기 | **결과 행**, `ResultRow`, `resultRows` |
| 보존 사실 | `measurementFacts`, `measurementFactsReason` |
| 공통 상태 표시/서버 파일 | `MeasurementStatus`, `measurement_coverage*.go` |

전역 메뉴는 **개요 / 측정 / 실험 / 서비스 / 운영 / 기록 / 설정**이다.
홈 `/`은 사용 가능 범위·다음 조치·실험 전체 득실을 먼저 보여 준다.
운영 흐름 보드는 `/operations/flow`, 실험은 `/experiments`에 직접 접근한다.
sidebar의 가장 긴 prefix 판정에서 `/operations`는 운영, `/experiments`는 실험으로 바꾸고
`/scheduler/profiles`의 측정 소속과 `/scheduler/history`의 기록 소속은 유지한다.

```text
CHILL   개요 | 측정 | 실험 | 서비스 | 운영 | 기록 | 설정      [접속 관리]
측정·실험 현황                                             [갱신]
사용 가능 C/N   기준 충족 P   재사용 K   지원 미확인 U   기준 시각
──────────────────────────────────────────────────────────────────
최근 비교: 이름 / 비교 목적
대응 단계 전부의 에너지 Δ: E5-1 … E5-2 … E5-4 … E5-5 … E5-6 …
서비스별 가장 낮은 성공률 차이/가장 큰 p95 증가와 해당 단계
[실험 전체 보기]
──────────────────────────────────────────────────────────────────
다음 조치                         모델 × 장치 커버리지
실제 실패 원인 [확인]             MobileNet     C/N       C/N
부족 배치 [요청 범위 확인]        PLC           C/N       C/N
[운영 흐름 보드]                  [커버리지 열기]
```

최신 비교 불가 기록도 남긴다. 가장 유리한 단계 하나를 대표로 고르지 않는다.
대응 단계 전부의 에너지 Δ와 서비스별 최악 단계(성공률 최소 Δ/p95 최대 Δ)를 출처·단위와 함께 보인다.
정상 범위를 실패 요청 이력 수와 혼동하지 않는다.
측정은 **커버리지(기본)/요청/회차/프로파일/동작 비용/분배 점유** 6개 보이는 링크 탭이다.
`/measurements`와 기존 `/measurements/coverage`는 같은 커버리지, 요청은 `/measurements/requests`,
프로파일은 기존 `/scheduler/profiles`다. 계획 검사는 요청의 보조 동작이다.
제목은 H1 한 번, 목록 breadcrumb 없음, 상세 breadcrumb 하나, 중첩 카드/가운뎃점 메타 나열 금지다.

## 3. 커버리지·상태·결과 행

기존 Asta Sans와 semantic 색 토큰을 유지한다. 시각의 주인공은 batch 공백이 보이는 계측표다.
개요 모델×장치 → mode×path 상세를 쓰며 헤더에 **목표 batch 칩**을 두어 URL `batches`와 동기화한다.
여러 variant 셀은 ‘조건 k개’, batchProgress/대표값 null이다. 선택한 variant 하나의 수치만 표시한다.
같은 CPU/dataset/protocol이 아닌 B1/B2를 합쳐 완료로 만들지 않는다.
셀→요청에 `variantId`를 전달하고 조건 변경은 부족 batch 자동 제외를 무효화한다.
1024px 이상에서는 표 아래 전체 폭 상세를 펼쳐 비교 맥락을 유지한다.
768~1023px도 표 아래 상세를 유지하며 **768px 미만만** 단일 Sheet/작업면으로 대체한다.
닫으면 원래 셀 focus, 이전/다음 조건은 같은 URL 맥락을 유지한다.

서버의 화면 상태는 **사용 가능/부족/문제/진행/미측정** 5개다.
지원 안 함·확인 불가는 별도로, 상세의 complete/criteria_met 등은 사유 행과 도움말 한 곳에서 설명한다.
complete는 채택·목표 batch 측정, criteria_met는 그중 결과 체크리스트 충족이다.
timestamp 부재·노드 전체 전력은 두 판정의 gate가 아니라 전력원 옆 고정 각주다.
warning은 실제 catalog 기각·근거 파손·선언 충돌·음수 d 같은 문제만이다.
superseded/scope 밖 정상 결과는 complete+현재 스케줄러 미사용 사실, reuse는 complete일 수 있지만 criteria_met는 아니다.

결과 행은 조건·pooled client p95/N·idle/active W·총/증분 J·증분 CI·active/idle n·전력 출처를 포함한다.
채택 profile의 통계/결과는 **catalog가 이미 읽은 raw를 로드할 때 산출한 메모리 sidecar**에서 읽는다.
Console `/result`는 catalog 밖/단건만 쓴다. CSV/LaTeX는 같은 행을 escape해 내보낸다.
재사용은 대상 confirmation 실측 행과 source 참조 행을 나눠 혼합 총 J를 만들지 않는다.
개별 CI의 중첩만으로 유의성을 판단하지 않는다.

## 4. 요청 범위와 사전 검사

7단계를 한 편집 화면+동일 화면 검사/검토로 바꾼다. 모델×장치×mode×batch부터 직접 고른다.
서비스는 입력 프리셋이며 복수 dataset/preprocessor/protocol은 추측하지 않는다.
Compatible만 기본 노출, Incompatible은 숨긴 이유 목록, Unknown은 이유 코드별 담당 조치와 명시 검사 경로다.
반복·active/idle 시간·warmup/settling·계측 주기/timeout·CPU의 실제 값과 복사 출처는 접지 않고 요약한다.

부분집합을 지원하는 경로만 ‘부족 배치만 요청’이다.
declaredBatchSizes 경로는 batch 칩 읽기 전용, 기존 batchSizes 생략을 유지한다.
preflight의 `batchPlan`에 **요청 가능 범위/실제 실행 예정 범위/측정·진행·부족 batch/
부분집합 가능 여부/조기 종료 가능성/정확 조건 대조 결과**를 표시한다.
셀에서 유효 variantId를 받아 조건을 유지한 때만 missing을 초기 선택하고 나머지는 ‘중복 미확인’이다.
검사 때 최신 서버 대조로 확정하며 요청 spec만 같은 진행 건은 planned-only로 구분한다.
모든 선택 batch 실행을 보장하거나 기존 실행 계약을 바꾸지 않는다.
이름은 모델/Node/mode/path/CPU/날짜, suffix는 **검사 통과 때 고정**한다.
자동 POST/제출 없음, 명시 dryRun/preflight만 허용하며 실패 HTTP/코드·조치가 본문에 보인다.
입력 변경은 검사 무효화, 응답 미확인 뒤는 같은 이름 identity 조회이며 suffix 재생성/자동 재제출 금지다.

## 5. 실험·수치 표시

전 단계 표에 비대응/중복도 처음부터 보인다. suffix가 다르면 같은 req/s라도 자동 대응하지 않는다.
서비스별 성공률/p95 small multiples는 3열/2열/1열, 기본 높이 160~180px로 유지한다.
비대응은 선을 끊고 속 빈 마커와 이유를 쓴다. 공통 부하/POOL W와 실제 시간순 레인은 구분한다.
동결 stage KPI에 목표가 있을 때만 SLO 기준선을 그리며 현재 workload SLO를 소급하지 않는다.
직접 A/B는 실제 이름을 대입해 ‘A가 B보다 낮음/높음’이라 쓰고 부호만으로 CHILL 유리라고 하지 않는다.
단위별 **고정 자릿수**: J/요청 수 정수, W 소수 1자리, %/%p·ms 소수 2자리,
J/건 소수 4자리. Δ는 원값으로 계산하고 표시값끼리 뺀 값과 마지막 자리 차이가 가능함을 한 번 알린다.
`|Δ|<표시단위`는 **방향 없이** 표시한다. 정확값/복사는 원본 정밀도를 유지한다.
기준 B=0이어도 절대 차이/%p는 계산하고 상대 %/배율만 계산 불가다.
공유 에너지 서비스별 합산/배분, 초별 보간, CI 중첩만으로 유의성 주장 금지는 유지한다.

## 6. 토큰·수명

sessionStorage key는 버전+**system 단위**(namespace별 아님), hydration 완료 전 로그인 깜빡임을 막는다.
버튼은 **‘이 탭에서 토큰 지우기’**다. 이 탭의 저장분 삭제이며 bearer/다른 탭을 폐기하지 않는다.
신원 변경·401/WS4401은 storage/요청/QueryClient/socket을 같은 epoch 전환으로 정리한다.
403/4403·503/1013은 만료로 처리하지 않고 같은 origin 목적 URL을 유지한다.
앱이 여는 **모든 새 창**은 opener를 제거한다. 탭 복제/브라우저 복원 가능성과 storage 거부시 메모리 전용을 안내한다.
sessionStorage는 XSS 방어가 아니다. 현재 CSP의 unsafe-inline은 강한 스크립트 격리 근거가 아니며
‘CSP가 토큰을 보호한다’고 주장하지 않는다. 안전 텍스트 렌더링/중계 allowlist를 유지한다.

## 7. 세 병렬 슬라이스·파일 소유권

아래 소유권을 고정해 한 배치로 병렬 시작한다. 도중 검사/빌드/포맷은 하지 않고 Main이 통합 뒤 한 번 수행한다.
아래 예정 코드 파일 이름은 모두 중립 어휘이며 다른 세션의 Go WIP는 소유자와 합친 뒤 편집한다.

| 슬라이스 | 독점 파일·경계 | 완료·회귀/배포 |
|---|---|---|
| **U1 Go 집계·catalog 통계·memo·QPS** | `internal/operatorconsole/measurement_coverage*.go`, 필요한 `internal/operatorconsole/{kube,readapi}/**`, `cmd/console/**`, `cmd/chill-server/**`, `internal/serving/{kube/catalog*.go,httpapi/**}`와 필요한 catalog 메모리 타입, `internal/profiling/{derivation,profiler/projection,profiler/kube}/`의 좁은 통계/호환성 읽기, 해당 tests; `charts/chill/templates/{serving_entrypoint_rbac,console_viewer_rbac,console}.yaml`, `charts/chill/values.yaml` | 메모리 통계 1회·추가 raw I/O 0, catalog 성공시각/오류, memo Get/파싱 1회, QPS/Burst 설정, URL+resource SAR, 기존 list/detail 상태, preflight batchPlan. console/chill-server image+RBAC/config 배포. CRD/저장 migration 없음 |
| **U2 IA·개요·coverage·토큰·공유** | `app/{page.tsx,[...route]/page.tsx,globals.css}`, `components/{screen,app-sidebar,dashboard-shell,site-header,measurement-navigation}.tsx`, `features/{overview,measurement-coverage,live-board,hubs,scheduler}.tsx`, `lib/{client,use-live}.tsx`, `lib/{relay,read-types,measurement-coverage}.ts` 및 관련 tests | 6탭/개요/직접 실험·sidebar prefix, 흐름 보드 이동/POOL 복귀/WS 활성경로, 4401 정리·system storage, Retry-After, 1024 표 아래 상세, polling 제거. U1 뒤 U3와 dashboard 동시 cutover |
| **U3 상태 소비·측정·결과·요청·실험** | 새 `features/measurement-requests.tsx`, `features/{measurement-plans,measurement-runs,measurement-projections,measurement-request,read-pages,scheduler-profiles,experiments}.tsx`, `lib/{measurements,experiments}.ts`+tests, `components/stage-breakdown.tsx` | 5개 화면 상태/실제 원인, 같은 resultRows 내보내기·reuse 분리, 조건별 batchPlan/고정 suffix, 선언형 범위, 전 단계/전 서비스/고정 자릿수. U2와 같은 dashboard image, publisher 변경 없음 |

공유 계약:

- U1 wire 정본(API §13)을 U2 `read-types.ts`가 `MeasurementCoverageResponse/MeasurementAssessment/ResultRow`로 옮긴다.
  `MeasurementStatus({assessment})`/`MeasurementNavigation({active})`는 U2 소유 단순 표시다.
  단건은 자신의 서버 assessment를 사용하며 전체 coverage grant를 강제하지 않는다. 클라이언트 상태 재계산 없음.
- U2 `useMeasurementCoverage(namespace)`는 snapshot/오류/명시 갱신만 제공한다. model 등은 화면 필터,
  서버 query는 batches/node만. runs는 슬림 index, 비용/점유는 기존 list/detail에서 상태를 얻는다.
- U3가 기존 목록을 `MeasurementRequestsPage` 새 파일로 옮기고 U2가 coverage의 옛 export/브라우저
  전수 조인/CoverageProgress를 제거한다. U2가 catch-all route와 live-board의 모든 caller를 함께 바꾼다.
- `lib/use-live.tsx`는 `/operations/flow`와 `/pools`에서만 활성, `/`에서는 비활성이다.
  POOL 복귀/anchor는 새 보드 경로, hubs 중복 회차/실험 링크는 새 IA에 맞춘다.
- httpapi는 자체 transport DTO, cmd adapter가 공통 reducer를 연결한다. `.go-arch-lint.yml`은
  의도적으로 변경 없음이다. 새 package 예외/직접 import를 만들어 빌드 경계를 깨지 않는다.
- 전역 CSS/route/type/relay는 U2만, Go/Helm은 U1만, 개별 결과/요청/실험은 U3만 수정한다.

## 8. 통합 수용·운영 담당자 설문

문서 단계에서는 검사/빌드/배포/새 설문을 실행하지 않는다. Main이 통합 뒤 아래를 한 번 실행한다.

```sh
go test ./cmd/chill-server ./cmd/console ./internal/operatorconsole/... ./internal/serving/httpapi ./internal/serving/kube ./internal/profiling/derivation ./internal/profiling/profiler/projection ./internal/profiling/profiler/kube
pnpm --dir chill-dashboard test
pnpm --dir chill-dashboard typecheck
pnpm --dir chill-dashboard lint
pnpm --dir chill-dashboard build
```

성능은 API §13.6의 세 지표(성공 GET/첫 사용 가능 coverage/선택 채택 결과 판정 각각 p95<2초),
cold process→사용 가능/판정 완료 30초 이내 목표, warming/partial 비율·지속시간을 함께 수용한다.
빠른 503/unknown만으로 통과하지 않는다. 데이터 규모·동시 사용자·catalog/Kube I/O/제어 부하를 기록한다.
URL+namespaced resource SAR, 비용 source 장애 격리, source stale와 무변경 catalog 성공 확인,
다중 variant/재사용/정상 0/null/실패 idle 반복/CI 부재를 회귀에 포함한다.

새 평가는 **엣지 클러스터 운영 담당자 계열 3역할**(측정 실행 담당/클러스터 운영 담당/운영 결과 검토 담당)의
독립 세션이다. 기존 대학원생/교수 점수와 모집단이 바뀌었음을 밝히고 직접 점수 향상으로 단정하지 않는다.
설문 가드는 지정 preflight POST와 `?dryRun=All`을 **허용**, 실제 제출/삭제/설정 변경 POST는 차단한다.
검사 성공/실패 원인을 실제 응답으로 평가하되 측정 제출은 하지 않는다.

- M1: 1분 안에 지원 범위/5개 상태/목표 batch/기준 시각을 설명한다.
- M2: 실제 실패 원인과 후속 조치를 찾는다. M3: **mode/path 셀부터 한 번 선택**해
  선택 variant의 스케줄러 사용 여부·결과 행·출처·부족 기준을 설명한다. 여러 조건이면 variant 선택은 별도 횟수다.
- M5: 직접 조건을 고르고 요청 가능/실제 예정 범위·시간·중복·검사 원인을 설명한다.
- E1: 1분 안에 전 대응 단계 에너지와 서비스 최악 단계·비교 제한을 말한다.
  E2: 비대응/실제 순서·부하/예정 metadata를 구분한다.
- 1440/1024/768/390px/light/dark/키보드/focus/aria-sort/44px 타깃, storage/401/4401/403/새 창을 확인한다.
- **평균 SUS ≥70와 M1·M3·M5·E1 전원 성공** 모두 충족할 때 수용한다. 부분 성공은 실패다.
  미달이면 설계·구현·배포·독립 재설문을 반복하고 문서만으로 달성을 선언하지 않는다.

## 9. 교차 검토 결정 기록

Main이 R5Rev1/2/3 세 모델의 전문을 검토했고 본 개정은 그 종합 결정을 따른다.
1. 사용자/어휘: 평가 페르소나와 제품 분리, 중립 UI·API·예정 파일 이름 확정.
2. 판정: 5개 화면 상태, complete/criteria_met 분리, timestamp/node-wall 각주, 실제 문제만 warning.
3. 비용: 행렬 memo/1회 파싱·QPS/Burst, catalog 메모리 통계 재사용, Console 전체 크롤러 삭제.
4. 정확 조건: 여러 variant 셀 수치 null, 목표 칩/variantId와 경로별 batchPlan, 검사 suffix 고정.
5. 비교/수명: 전체 단계·최악 서비스/비대응 표시, 고정 자릿수, system storage와 탭 토큰 삭제 의미 명시.
검토 결과는 구현/배포/성능/새 SUS 달성 증거가 아니다.

## 10. 배포 전 사전 리뷰 반영 (2026-10-30)

운영 담당자·운영 팀장 페르소나의 스크린샷 사전 리뷰(예상 SUS 50~65)를 반영했다(`8077cea8`..`ab7af392`, `7657e44f`..`086332c2`).
- 목표 배치 미충족은 서버 판정에서 항상 '부족'(사용 가능과 동시 표시 금지). 조회 실패 시 '추가 조치 없음' 대신 실패 사실·재조회를 보인다.
- 내보내기는 CSV만 둔다(LaTeX 제거). 결과 카드는 p95·J/건·W·반복 수를 먼저 보이고 원문은 접는다.
- 실험은 전 대응 단계의 에너지 득실과 서비스 악화를 한 줄 결론으로 묶고, 부하 구간은 운영 말(요청률)로 쓴다.
- 전력모드는 선언 W 필드가 없어 실측 W 범위 또는 '전력 미확인'으로 표시한다.
- 측정 요청은 장치 종류를 먼저 고르고 검사 결과 아래에서 제출한다. 예상 시간·서빙 영향은 API가 제공하지 않아 미제공으로 안내한다.
빌드·검사는 측정 창 동안 genesis2(`tools/g2gate.sh`)에서 실행했다. 배포 브랜치 `deploy/rev-r5-ux` `d7814ce4` 게이트 전체 통과.
