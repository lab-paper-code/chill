# 측정 커버리지와 측정·실험 UX 개편 계획

- 날짜: 2026-10-30. 상태: **코드 연결·격리 GET 확인 완료, 배포·재설문 전**.
- 요구사항 정본: [Dashboard SRS](../spec/dashboard/SRS.md) DB-FR-56~65, DB-UX-05~06.
- 화면·필드·판정·파일 소유권 정본: [Dashboard 설계 §8](../design/dashboard/README.md#8-측정실험-ux와-커버리지-2026-10-30).
- 문서 확정 뒤 S1~S3 구현으로 진행했다. 클러스터 변경과 새 라이브러리 설치는 하지 않는다.

## 1. 해결할 문제와 성공 기준

설문 세 페르소나 평균 SUS는 **27.5**(researcher 35, advisor 27.5, uxexpert 20)다.
공통 실패는 ‘예쁜가’보다 **조합의 공백, 숫자의 신뢰 범위, 비교 결론을 찾지 못함**이다.

| 과업 | 결정 | 구현 뒤 수용 기준 |
|---|---|---|
| M1 어떤 조합을 더 재야 하나 | 측정 홈 최상단에 `/measurements/coverage` 진입, 모델→실행 경로 × 장치 종류→전력모드 행렬 | 세 페르소나 모두 1분 안에 지정 모델·장치의 완료/경고/실패/진행/미측정/미지원/미확인을 구별하고 근거를 가리킨다. 모드명을 파일 이름에서 추측하지 않는다. |
| M2 실패 원인과 다음 행동 | 실패 원문·해당 회차 상태·후속 유효 결과 연결, 근거 미생성과 통신 오류 분리 | 실패 회차가 Passport 없이도 열리고, 후속 결과가 있는 실패에서 중복 요청을 권하지 않는다. |
| M3 논문 표에 쓸 수 있나 | 첫 화면에 배치별 에너지·오차 제공 여부·지연·반복 수·정확 조건 | 단일 배치와 여러 배치를 구별한다. CI 미제공을 0 오차나 논문 적격으로 포장하지 않는다. |
| M4 비용과 점유시간 해석 | NaN·음수는 품질 경고, 동작 비용 시간/J 미제공은 상단 명시 | 녹색 정상 완료와 이상 수치가 공존하지 않는다. 원값과 출처를 보존한다. |
| M5 조건을 정해 요청 준비 | 커버리지 prefill, 기존 측정 표시, 명확한 비호환 경로 비활성만 | 빈 셀에서 조건을 유지해 폼으로 이동한다. 실제 제출은 사용자만 수행한다. 기존 7단계 전체 재설계는 하지 않는다. |
| E1 1분 내 실험 결론 | 상단 요약·서비스명 A/B 나란히 비교·%, ms, J | 세 페르소나 모두 1분 내 성공률·p95·에너지 차이와 비교 제한을 말한다. |
| E2 부하 변화와 결과 | 단계 타임라인과 단계별 결과 연결 | 단계 경계/구간·부하 α 제공 여부·결과를 같은 선택 상태로 읽는다. 미제공 α나 초별 시계열을 만들지 않는다. |

재설문은 같은 세 페르소나, 같은 SUS 10문항/환산식, 1440×900와 390×844,
같은 M1~M5/E1~E2 과업과 GET-only 관찰 조건을 사용한다. **평균 SUS ≥68 및
M1·E1 전원 성공**을 모두 충족해야 UX 수용이다. 평균만 올리거나 부분 성공을 성공으로 바꾸지 않는다.
클릭 수·실제 측정 시간·확신도·막힌 화면과 스크린샷을 다시 남긴다.

## 2. 근거와 관측 범위

원자료 폴더: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/ux-survey-20261030/`.
`SYNTHESIS.md`, `researcher.md`, `advisor.md`, `uxexpert.md`를 읽었다.
직접 본 스크린샷은 `uxexpert-13-occupancy-list.png`(d/X 오른쪽 잘림),
`uxexpert-18b-exp-compare-mid.png`(세로로 쪼개진 원시 수치),
`researcher-06-profile-result.png`(0.09 J 두 막대인데 최저 단정)다.
이 관찰은 오류 재현을 위해 실행을 다시 요청한 결과가 아니다.

CoverageScout 조사(`agent://CoverageScout`)를 소스 위치로 확인하며 사용했다.
2026-10-30 GET 조사에서 inventory `readAt=2026-10-30T07:09:54.766789878Z`,
class/model 양쪽 page에 nextOffset 없음, **DeviceClass 8 / Node 10 / 모델 3 / class별 모드 합 26**을 확인했다.
BEO **88(Complete 80, Failed 8)**, `(모델, 경로, class, 모드)`가 다른 요청 **40조합**,
scheduler profiles **78**, insufficiencies **2**, 실험 목록 **6개**다.
Run 첫 페이지는 `limit=200,total=1155,nextOffset=200`이었다. 이 조사에서 모든 Run을 읽었다고 주장하지 않는다.
조회들은 서로 다른 시각의 GET이며 atomic snapshot이 아니다. 토큰·CA는 기존 배포 자산을 사용했고 토큰은 출력하지 않았다.

- `GET /inventory?limit=200`: 선언 후보, class별 모드와 Node identity. **지원 조합 API가 아니다.**
- `GET /namespaces/chill-system/batch-energy-optimizations`: 요청·진행·실패.
- `GET /scheduler/profiles`: 현재 채택 증거와 insufficiency. 78/88은 커버리지 비율이 아니다.
- `GET .../batch-energy-optimizations/{name}/result`: 성공 두 배치의 0.092607…/0.086819… J/건과 실패의 `result:null` 확인.
- `GET /experiments[/{id}]`: 게시된 단계와 요청/에너지 집계. 실제 `measurementRules`에 α가 없는 기록도 확인.
- `GET .../profiling-runs?limit=200`: Passport 없이 볼 수 있는 Run spec/status와 페이지 계약.

## 3. 범위와 하지 않을 일

**포함:** 선언 후보 대비 측정 현황, 조건별 목록/상세, 최소 요청 진입 보완, 실험 해석,
공용 넓은 데이터 레이아웃·수치 정렬·뒤로가기·CTA 가림 해소.

**제외:** 홈/서비스/운영 전체 리브랜딩, 측정 알고리즘·채택 정책 변경, 요청 마법사 전체 통합,
모델→워크로드 자동 매핑, 가짜 CI·통계적 유의성 판정, 미지원 근거 추측, 새 DB/서버 집계/지원 registry,
다섯 종류 근거를 모두 갖췄다는 ‘완전 커버리지’ 인증, 새 차트·표·폰트 라이브러리.
기존 Asta Sans, semantic CSS 토큰, shadcn Table/Dialog, TanStack Query/Table, Recharts, Lucide를 쓴다.

**서버 API 결정:** 이번 범위는 기존 GET의 클라이언트 조인으로 구현한다.
88 BEO/78 profile 규모에 별도 집계 서버는 필요하지 않다. inventory 양쪽 페이지와 Run 페이지는 끝까지
확인하고, 미완료/실패 범위는 ‘확인 불가’로 남긴다. 전체 Run은 한 번에 시작 화면을 막지 않고
선택 조건의 이력·상세를 열 때 읽는다. 상세 Result/Passport를 모든 셀에서 일괄 호출하지 않는다.
권위 있는 runtime 지원 판정과 미제공 원시 비용 숫자는 이 UI가 해결했다고 주장하지 않는다.
향후 그것이 필요하면 기존 resolver/catalog를 재사용하는 bounded GET projection 하나가 최소안이다(설계 §8.3).

## 4. 구현 순서와 세 슬라이스

아래는 병렬 구현의 파일 소유권이다. 공유 파일은 지정 소유자만 편집한다.
같은 파일을 함수 단위로 나눠 두 작업자가 동시에 고치지 않는다.

| 슬라이스 | 소유 파일(모두 `chill-dashboard/` 아래) | 결과·완료 기준 |
|---|---|---|
| **S1 커버리지·목록·요청 최소 보완** | 새 `features/measurement-coverage.tsx`, 새 `features/measurement-runs.tsx`, 새 `lib/measurement-coverage.ts`와 그 test; 기존 `features/measurement-request.tsx`, `features/scheduler-profiles.tsx`, **`lib/read-types.ts`, `app/[...route]/page.tsx`** | 7상태 행렬/패널·필터·분모/조회 범위, 열이 있는 목록, 조건 그룹과 정확 회차 연결, prefill/중복 안내/호환 경로 제한. route가 새 목록 exports를 쓰도록 전환. |
| **S2 상세·품질·공용 레이아웃** | 기존 `features/measurement-plans.tsx`, `features/read-pages.tsx`, `features/measurement-projections.tsx`, `lib/measurements.ts`와 test, **`lib/scheduler.ts`의 수치 변환과 `lib/scheduler.test.ts`**, `components/screen.tsx`, `components/app-sidebar.tsx`, **`app/globals.css`** | 첫 화면에 정직한 지표/실패·재측정 이력, Passport 없는 Run 표시, 공유 음수 변환의 근본 수정과 d 이상값 경고, 비용 미제공 안내. Screen data 폭/상단 CTA/breadcrumb 계약 제공. S1로 옮긴 기존 목록/그룹 함수는 제거하되 PreflightAnswer exports는 유지. |
| **S3 실험** | 기존 `features/experiments.tsx`, `lib/experiments.ts`와 test | 상단 비교 선택과 결론, workload name 행 매칭·정의 차이 표시, 단위·분모·공유 에너지, 단계 타임라인. 기존 URL `a/as/b/bs` 유지. |

공유 계약은 설계 §8.7에서 고정한다. S1은 `MeasurementPlansPage`, `MeasurementRunsPage`,
`MeasurementGroupPage` 이름을 새 소유 파일에서 export하고 route import를 바꾼다.
S2는 기존 파일에 이 이름의 중복 구현/재-export를 남기지 않는다.
S2의 공용 Screen 변경을 기다리느라 다른 슬라이스를 직렬화하지 않고 확정된 props를 사용한다.
S3는 CSS 전역 파일이나 route를 고치지 않는다. 실험 화면은 현재 catch-all 경로로 충분하다.
공유 read-types 보완이 필요하면 S1에 정확한 필드만 전달한다. 별도 중복 DTO를 만들지 않는다.

### S1 구현 기록 (2026-10-30)

`features/measurement-coverage.tsx`가 선언 후보 행렬·URL 필터·조건 패널과 요청 목록을 제공한다.
`lib/measurement-coverage.ts`는 정적 비호환/미확인, 목록 완전성, catalog 근거와
실행 이력의 7상태를 판정한다. Node가 일부만 완료면 경고이며 다른 CPU·동결 이미지를
하나의 최저 에너지로 합치지 않는다. inventory 양쪽 페이지와 namespace Run을 끝까지 모아 cache하며
미조회·실패를 미측정으로 바꾸지 않는다. 선택 Result와 정확 참조 Passport만 추가 조회한다.
`features/measurement-runs.tsx`는 실제 조건을 전부 포함한 그룹 키와 원본 Run/batch를 사용하며
`195`/`195b` 부분일치에 회차 번호를 붙이지 않는다. 요청 prefill은 현재 선언·namespace·중복 키를
확인하고, 워크로드·CPU·protocol을 임의 선택하지 않는다. CI가 없는 현재 catalog의
논문용 검토 필터는 검토 보류로 남긴다. 이 기록은 배포·runtime 지원 보장·재설문 완료 증거가 아니다.

### S3 구현 기록 (2026-10-30)

`features/experiments.tsx`는 비교 선택·결론을 목록 앞에 두고 같은 기록 이름의 A/B를 한 표에 놓는다.
서로 다른 UID/정의와 미제공 identity는 그대로 보존하며, workloads/requests에 동명 후보가 함께 있으면
사용자가 후보를 선택하기 전 차이를 계산하지 않는다. `lib/experiments.ts`의 실제 표시 함수가
ns→ms, 비율→%, A−B %p/ms/J와 B 대비 에너지 변화율을 처리한다.
전체 성공률·p95를 서비스 평균으로 생성하지 않고 원값·표본·모집단·규칙·공백은 펼침에 보존한다.
단계 레인은 실제 기간에 비례하며 선택 시 URL·결과 focus·A 단계의 판단 근거 구간을 함께 갱신한다.
α는 미제공이며 offered/기간은 관측 평균 요청률로만 표기한다. 배포·재설문 수용 완료를 뜻하지 않는다.


## 5. 검증과 통합 완료 조건

문서 단계에서는 코드 검사·빌드·테스트·배포·새 설문을 실행하지 않는다.
구현 작업자도 도중에 build/lint/tests/formatter를 실행하지 않고, 통합 담당자가 모두 합친 뒤 한 번 수행한다.

| 소유자 | 실제 제품 코드의 순수 함수 회귀 항목 |
|---|---|
| S1 | 정적 비호환/미확인, incomplete page≠미측정, Complete≠profile 적격, 최신 실패+이전 사용 가능 결과, 정확 조건/Node·mode 분리, 재측정 조건 비교, prefill decode·유효하지 않은 참조, 부분일치 이름 충돌(195/195b) |
| S2 | 기존 `nanosToMilliseconds`의 음수 부호·정밀 정수와 모든 호출자 영향, `occupancyDuration`/`measurementState`를 확장한 실제 품질 분기: 음수 bigint/number·NaN·Infinity·누락·정상 0, 원값 보존; 단일 배치 결론·오차 누락; 조회 실패≠미생성·UID 불일치 |
| S3 | 실제 `experimentRows`/비교 함수: workloads/requests 두 shape, 같은 이름 다른 UID·정의, 중복 이름/한쪽 없음, 0분모/0기준 차이, ms↔ns, 비율↔%p, 결손 에너지, 다른 measurementRules, α 미제공 |

가짜 테스트 전용 함수, 파일 문자열 검색 테스트, JSX 문구만 확인하는 검사는 회귀 증거로 쓰지 않는다.
기존 `node --experimental-strip-types --test lib/*.test.ts server/*.test.mjs` 체계를 재사용한다.
통합 담당자의 실행 항목은 `pnpm test`, `pnpm typecheck`, `pnpm lint`, `pnpm build`와
1440/1024/768/390px 브라우저 수용(키보드, 필터 복원, 마지막 행 focus/CTA 가림, 표 가로 이동, 원본/단위)이다.
GET-only 재설문에서 dry-run/preflight POST가 차단된 것은 제품의 제출 장애로 집계하지 않는다.

2026-10-30 통합 검사: Node 24와 `nice -n 19`로 `pnpm test` **73/73**,
`pnpm typecheck`, `pnpm lint`, `pnpm build`를 통과했다.
첫 lint의 실험 focus effect/미사용 import는 각 소유자가 수정한 뒤 실패한 gate부터 이어서 확인했다.
기존 Asta Sans fallback metadata 경고는 남으며 새 폰트나 라이브러리를 추가하지 않았다.
격리 `/tmp/uxs1`, GET-only 실데이터에서 선언 후보 390 / BEO 88 / profile 78 / Run 1155를
각각 다른 모집단으로 확인했고 1440/1024/768/390px, Dialog 닫기 후 셀 focus,
prefill 무제출·미선택과 브라우저 뒤로가기의 조건/셀 복원을 관찰했다.
390px 표는 페이지 전체가 아니라 내부에서 가로 이동한다. 실제 idle Run Passport 조회 실패는
그 영역의 읽기 오류로 남겼으며 catalog 결과나 Run을 지우거나 0 지연으로 대신하지 않았다.
이 검사는 승인 배포·세 페르소나 재설문/SUS 수용의 대체가 아니다.

## 6. 남는 사실상의 한계

- inventory는 runtime declaration·Node 실행 적격성을 주지 않는다. ‘확인 불가’는 필수 결과이며 실패가 아니다.
- BEO/Run 목록만으로 완전한 frozen 조건 일치를 증명할 수 없는 후속 시도는 ‘관련 재측정 후보’이지 자동 대체가 아니다.
- 배치 에너지 CI와 합쳐진 요청 지연 분포가 없는 응답에 새 숫자를 만들지 않는다. 논문용 필터는 검토 보조이지 인증이 아니다.
- OperationCost projection에 시간/J가 없으므로 이번 작업의 정직한 완료는 ‘미제공’과 근거 위치를 드러내는 것이다.
- 게시된 실험에 α·시간열·조건 설명이 없으면 미제공으로 남긴다. 2차 개선은 명시 요청된
  E5 `full`=CHILL / `static`=정적군 명명과 같은 전체 suffix만 자동 연결하며, 조건 동일성·α는 추측하지 않는다.

이 한계들은 구현 미완료 약속이 아니라 현재 API 범위의 명시적 계약이다.
