# 연구 중심 측정·실험 대시보드 5차 구조 재설계

- 설계일: 2026-10-30. 구현 묶음 식별일: 2026-10-31. **설계 확정안이며 코드·배포·SUS 달성 기록이 아니다.**
- 사용자 결정: 평균 SUS **70 이상까지 반복**, 기존 구조 변경 허용, 토큰의 탭 범위 sessionStorage 보관 승인.
- 정본: [화면 설계 §9](../design/dashboard/README.md#9-연구-중심-측정실험-5차-재설계), [API §13](../design/serving/measurement-api.md#13-연구-커버리지-읽기-projection-u1), [SRS](../spec/dashboard/SRS.md) DB-FR-56~65, DB-UX-05~06, DB-SEC-03, DB-NFR-03~04.
- 이전 [UX 계획](2026-10-30-dashboard-measurement-ux.md)의 클라이언트 전수 집계·운영 홈 유지·7단계 유지와 Dashboard 설계 §8의 화면 결정은 이 안으로 대체한다. [B0](2026-10-30-dashboard-api-gaps.md)의 원근거 loader·통계·호환성·불변 저장 계약은 유지한다.

## 1. 왜 구조를 바꾸는가

| 차수 | researcher | advisor | uxexpert | 평균 SUS |
|---|---:|---:|---:|---:|
| 1 | 35 | 27.5 | 20 | 27.5 |
| 2 | 35 | 42.5 | 37.5 | 38.3 |
| 3 | 37.5 | 65 | 30 | 44.2 |
| 4 | 37.5 | 60 | 37.5 | 45.0 |

수치·문구를 보완해도 연구자의 M3(논문 표 판단)·M5(측정 설계)가 풀리지 않았다.
이번에는 객체 목록을 예쁘게 정리하는 대신 **연구 질문 → 조건의 공백 → 근거 한 행 → 필요한 측정** 순서로 바꾼다.
색·폰트·차트 라이브러리 교체, 새로운 CRD·저장 schema·통계 추정기·지원 registry는 하지 않는다.

### 근거와 직접 본 장면

원자료 위치: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/ux-survey-20261030/`.
`SYNTHESIS.md`, `SYNTHESIS-R2.md`, `SYNTHESIS-R3.md`, `SYNTHESIS-R4.md`와
`researcher-r4.md`, `advisor-r4.md`, `uxexpert-r4.md`를 읽었다.

| 직접 확인한 스크린샷 | 관찰 → 결정 |
|---|---|
| `advisor-r4-01.png` | 홈 대부분이 끊긴 운영 보드, 연구 결론은 잘린 한 줄 → 연구 홈과 운영 흐름 경로 분리 |
| `uxexpert-r4-13-coverage-cell-agx-orin.png` | 표보다 필터가 먼저이고 지원 분모 실패, 미확인 RKNN에 요청 링크 → 집계 GET 한 번, 권위 있는 지원 판정과 요청 가능 경로만 노출 |
| `uxexpert-r4-30-measurement-new-step7.png` | 7단계 후 원인 없는 요청 불가, 측정 시간 숨김 → 한 편집 화면과 명시적 검사·제출 |
| `researcher-r4-48-exp-charts.png` | 서비스 한 개만 선택, 표와 차트 단계가 다름 → 전 서비스 소형 다중 차트와 비대응 단계 포함 표 |

지원 불명확을 하드웨어 이름 추측으로 미지원 처리하자는 제안은 채택하지 않는다.
현재 resolver가 입증한 불일치만 지원 안 함이다. 노드 전체 W를 모듈 전력으로, 유휴 차감 J를 총 J로 바꾸지도 않는다.

### 추가 GET-only 단면

2026-10-30 10:47:04 UTC 시작. 지정 CA 검증과 파일 bearer로 HTTPS GET만 수행했다.
토큰·Authorization은 출력하지 않았다. 아래 시간은 이 설계 조사에서 **각 1회**의 클라이언트 경과시간이며 성능 벤치마크가 아니다.

| GET (`/api/v1` 이하) | 응답 | 경과 |
|---|---|---:|
| `/inventory?limit=200` | 200, 모델 3·장치 종류 8, 양쪽 page 끝 | 0.052 s |
| `/model-compatibility-matrix` | 200, 대표 Node 정적 판정 120행 | 7.690 s |
| `/namespaces/chill-system/batch-energy-optimizations` | 200, 요청 88 | 0.070 s |
| `/namespaces/chill-system/profiling-runs?limit=1` | 200, 총 1,155 중 1행만 읽음 | 3.861 s |
| `/scheduler/profiles` | 200, profile 78·insufficiency 2·reuseCheck 1 | 0.198 s |
| `/experiments` | 200, 게시 기록 6 | 0.863 s |

모든 Run/raw를 새로 검증한 조사가 아니다. 서로 다른 GET은 atomic snapshot이 아니다.
행렬 한 번도 7초 이상인 단면은 브라우저 페이지 수만 줄여서는 첫 응답 2초를 보장하지 못함을 보여준다.

## 2. IA와 첫 화면

전역 메뉴는 **연구 홈 / 측정 / 실험 / 서비스 / 운영 / 기록 / 설정**이다.
홈 `/`은 다음 순서의 평평한 연구 작업면이다.

1. 지원 확인 범위의 완료 수와 논문 적격 수, 기준 시각. 요청·회차·profile 건수를 완료율로 쓰지 않는다.
2. 최근 **비교 가능한** 실험 한 건의 선택 단계: 에너지 절감과 모든 서비스의 성공률·p95 손실을 함께 쓴다. 최신 비교 불가 기록도 별도 링크로 남긴다.
3. 다음 할 일: 미해결 실패, 부족 배치, 근거 확인 필요. 서버 reason에 근거한 링크이며 자동 우선순위 점수는 없다.

운영 흐름 보드는 `/operations/flow`로 옮긴다. 끊긴 라이브 채널을 클러스터 유휴라고 단정하지 않는다.
연구 홈은 라이브 연결 성공을 기다리지 않는다. 실험은 기록 링크 모음 아래에 숨기지 않는다.

```text
CHILL   연구 홈 | 측정 | 실험 | 서비스 | 운영 | 기록 | 설정      [접속 관리]
연구 현황                                                   [갱신]
지원 확인 조건 완료 C/N    논문 적격 P    지원 미확인 U    기준 시각
──────────────────────────────────────────────────────────────────
최근 비교 실험: 이름 / 선택 단계 / 실제 부하 / 비교 범위
전체 POOL 에너지 Δ     이미지: 성공률 Δ, p95 Δ     PLC / VQA 득실
[실험 전체 보기]
──────────────────────────────────────────────────────────────────
다음 할 일                      측정 커버리지
실패 원인 한 줄 [근거 확인]       모델       Orin     Xavier    RPi
부족 배치 4,8 [추가 측정]         MobileNet  C/N      C/N       C/N
계측 근거 누락 [조건 검토]        PLC        C/N      C/N       C/N
[운영 흐름 보드]                  [커버리지 열기]
```

측정은 커버리지(첫 탭/기본), 요청, 회차, 프로파일, 동작 비용, 분배 점유의 **보이는 링크 탭 6개**다.
`/measurements`가 커버리지를 렌더링하고 기존 `/measurements/coverage`는 같은 화면의 기존 딥링크로 유지한다.
요청 목록은 `/measurements/requests`, 프로파일은 기존 `/scheduler/profiles`를 유지한다.
계획 검사는 요청 화면의 보조 동작이며 일곱 번째 탭이 아니다.
제목은 화면 H1 한 번, 목록은 breadcrumb 없음, 상세만 `측정 > 요청 > 항목`처럼 사용한다.
상단 shell에 같은 화면명을 다시 제목으로 쓰거나 상태 배지를 눈썹 제목처럼 쌓지 않는다.

## 3. 커버리지와 논문 표

개요는 모델 × 장치 종류, 선택 상세는 전력모드 × 실행 경로로 유지한다.
기억할 시각 요소는 **배치별 공백이 보이는 계측표 하나**다. 중첩 카드·상태 pill 벽·가운뎃점 메타 나열을 없앤다.
셀에는 서버 상태, `측정 배치 n / 목표 m`, 대표 증분 J/건과 **그 배치의** p95를 보여 준다.
목표 batch가 명시되지 않았으면 `요청 범위 n/m`이며 연구 전체 배치 스윕 완료라고 쓰지 않는다.

셀 선택 후 논문 표 행(조건·p95·유휴 W·부하 W·총 J/건·증분 J/건·CI·n·전력 출처)을 먼저 읽는다.
넓은 화면의 상세는 28~36rem, 중간 폭에서는 전체 폭으로 교체하고 작은 화면에서는 전체 화면 Sheet를 쓴다.
숫자·경고·출처를 같은 위치에서 읽고, 긴 이력·원본만 접는다. CSV/LaTeX는 화면의 서버 행을 그대로 복사한다.
미제공 숫자는 사유가 있는 null이고 0이 아니다. 총 J/건을 부하 W/다른 회차 처리량으로 근사하지 않는다.

지원·측정·실행은 같은 축이 아니다. 단일 서버 상태 어휘와 부가 사실의 계약은 API §13이다.
논문 적격은 **명시한 체크리스트 충족**이지 논문 주장·통계적 우열·장치 단독 전력 인증이 아니다.
원본 timestamp/정확 조건/근거가 부족하면 적격 수가 0이어도 이유를 보인다. 임계치를 낮춰 숫자를 만들지 않는다.

## 4. 요청: 표의 빈칸에서 시작

7단계는 폐기한다. `/measurements/new` 한 화면에서 **조건과 측정 방법 편집 → 사전 검사 결과를 같은 화면에서 검토 → 명시적 제출**이다.
셀에서 진입하면 모델·장치·Node·mode·path와 목표 배치가 채워진다. 직접 진입도 모델 × 장치 × mode × batch부터 고른다.
전처리·데이터셋은 필요한 연구 입력이며 서비스는 이를 고르는 프리셋이다. 복수 후보는 임의 선택하지 않는다.

- Compatible인 경로만 기본 목록. Incompatible은 숨기고 ‘지원 안 함과 이유’ 보조 목록에서 조회한다.
- Unknown은 별도 ‘지원 확인 필요’ 목록이다. 확인되지 않은 경로로 곧바로 측정 요청 CTA를 만들지 않는다. 기존 preflight에서 지원 확인 후에만 검토를 진행한다.
- 목표 배치와 이미 측정/진행 중인 정확 조건을 함께 보여 주고 **부족 배치만 기본 선택**한다. 전부 충족이면 ‘추가 측정 없음’; 재측정은 사용자가 목적을 명시해 선택한다.
- 반복, active/idle 시간, warmup, settling, 계측 간격·timeout, CPU와 affinity를 검토 전에 읽는다. 복사값도 실제 값과 출처를 표시한다. 고급 설정은 접을 수 있어도 시간 요약은 숨기지 않는다.
- 이름은 모델·Node·전력모드·경로·CPU·날짜와 짧은 충돌 방지 suffix로 자동 생성한다. Kubernetes 이름 길이 안에서 정규화하되 전체 조건은 별도 표로 보존한다. 이름을 별도 단계로 묻지 않는다.
- 자동 POST 없음. ‘요청 검사’가 기존 dry-run/preflight를 실행하고, 결과에는 거부/권한 없음/통신 실패/지원 미확인을 구별한 원인·해결 행동을 펼친 본문으로 표시한다. 입력 변경은 이전 검사 결과를 무효화한다.
- 제출 응답 미확인 시 자동 재제출하지 않는다. 요청 identity로 상태를 확인한다. 동일 immutable 요청 수정·재사용으로 가장하지 않는다.

## 5. 실험: 전 서비스의 득실을 동시에

실험 첫 화면은 결론 한 문장, 전 단계 표, 부하/공유 전력, **서비스마다 성공률과 p95 소형 차트 두 개** 순서다.
데스크톱에서 서비스 3열, 중간 2열, 모바일 1열이다. 서비스 선택 dropdown으로 다른 서비스를 숨기지 않는다.
성공률 축은 0~100% 공통, 지연은 서비스별 고정 범위와 명시 단위/SLO 기준선을 쓴다. 서로 다른 축 범위를 숨기지 않는다.
SLO/목표가 게시되지 않았으면 임의 기준선을 만들지 않는다.

- 비대응 단계도 처음부터 표·선택기에 있다. A/B 한쪽 없음과 제외 이유, 실제 단계명·기간·offered/기간을 보인다.
- `full-E5-3`과 `static-E5-3-223`을 요청률이 같다는 이유로 자동 대응하지 않는다. 수동 A/B는 설명형 비교와 조건 차이를 유지한다.
- 목록/홈/상세 모두 `CHILL − 정적` 또는 실제 `A − B`를 바로 옆에 적는다. 에너지·지연 음수는 유리한 관측 방향, 성공률 음수는 불리한 방향이며 텍스트도 함께 표시한다.
- 원값으로 차이를 계산한다. 같은 행 A/B/Δ의 표시 자릿수를 맞추고 근삿값임을 표시한다. 최소 표시 단위보다 작은 차이는 `<0.01 ms`처럼 표시하고 0/동일이라고 단정하지 않는다. 정확값과 분모는 펼침/복사에서 보존한다.
- 단계 설명은 동결 metadata의 실제 설명, 예정 α, 관측 평균 요청률을 구분한다. 설명 없으면 ‘단계 설명 미기록’. 평균 증가/감소를 보조 설명할 수 있지만 원인·실험자 의도를 꾸미지 않는다.
- 실제 시간순 레인과 대응 단계 축은 별도다. 비동시 실행의 시간대 교란 가능성을 안내하며 집계점 사이의 초별 변화·회복 시간을 만들지 않는다.

## 6. 토큰과 보안

토큰만 sessionStorage에 보관하고 query 응답·로그·URL·localStorage·IndexedDB·cookie에는 저장하지 않는다.
키는 버전과 접속 system/namespace를 묶는다. hydration 전에는 인증 확인 상태이며 빈 토큰으로 화면을 깜빡이지 않는다.
새로고침과 같은 탭 딥링크에서 복원한다. 새 독립 탭은 로그인하고, 외부 창은 noopener로 연다.
브라우저 복제 탭/세션 복원이 sessionStorage를 복사·복원할 수 있으므로 ‘탭 닫으면 반드시 삭제’라고 보안 보장하지 않는다.

명시적 로그아웃·신원 변경·확정 401·WS 4401은 저장 토큰 삭제, 요청 취소, QueryClient 교체·캐시 비움, socket 종료를 한 전환으로 처리한다.
만료 안내 후 같은 origin 내부 목적 URL을 유지한다. 403/4403은 권한 거부, 503/1013은 인증/연결 장애이며 토큰 만료로 처리하지 않는다.
이전 epoch의 늦은 응답은 새 신원 cache에 들어가지 않는다. sessionStorage 접근 차단 시 메모리 전용으로 동작하고 새로고침 유지 불가를 알린다.
XSS가 있으면 sessionStorage도 읽을 수 있다는 위험은 남는다. 기존 안전 텍스트 렌더링·CSP/전달 allowlist를 약화하지 않는다.

## 7. 세 구현 슬라이스와 파일 소유권

아래 세 작업은 계약을 먼저 고정한 뒤 **한 배치로 병렬** 시작한다. 작업 도중 build/lint/test/formatter는 실행하지 않는다.
코드 경로의 변경은 다음 단계이며 이 설계 커밋에는 없다. Main의 `internal/serving/**` WIP를 가져오거나 덮어쓰지 않는다.

| 슬라이스 | 독점 파일·경계 | 완료 기준·회귀 | 배포 필요물 |
|---|---|---|---|
| **U1 서버 집계·권한** | `internal/operatorconsole/measurement_research*.go`(새 DTO/순수 판정), `internal/operatorconsole/{kube,readapi}/`의 필요한 projection, `cmd/console/**`, `cmd/chill-server/**`, `internal/serving/httpapi/**`, 필요한 `internal/profiling/{derivation,profiler/projection}/` 좁은 읽기 확장, `charts/chill/templates/{serving_entrypoint_rbac,console_viewer_rbac}.yaml`와 해당 테스트 | API §13 전체; 저장 변경 0; 권한 전 cache 접근 금지; 부분 실패·namespace/UID·재사용·batch/active/idle 분리; 모든 상태 서버 판정; 캐시 경합·만료·재시작·취소; 1,155 Run 단면에서 prewarm 뒤 성공 GET p95 <2초; raw 전체 fan-out이 요청 경로에 없음 | console와 chill-server 새 image, 정확 GET RBAC, 기존 CA/store 설정. CRD·raw migration·실험 재게시 불필요 |
| **U2 IA·홈·커버리지·토큰** | `app/page.tsx`, `app/[...route]/page.tsx`, `app/globals.css`, `components/{screen,app-sidebar,dashboard-shell,site-header}.tsx`, 새 `components/measurement-navigation.tsx`, `features/measurement-coverage.tsx`(**coverage만 남김**), 새 `features/research-home.tsx`, `lib/client.tsx`, `lib/relay.ts`+test, `lib/read-types.ts`, `lib/measurement-coverage.ts`+test | 홈/6탭/운영 경로/직접 실험; 집계 GET 1회와 메모리 cache; 기준 시각·부분 실패; 상태 재계산 제거; 390~1440px·키보드·focus; 새로고침/로그아웃/401/403/새 신원 늦은 응답·storage 거부; 같은 탭 URL 복원 | dashboard image. U1을 먼저 배포해 집계 완전성/권한 확인 후 U2+U3 동시 cutover |
| **U3 상태 소비·측정·요청·실험** | 새 `features/measurement-requests.tsx`(기존 목록 이동 대상), `features/{measurement-plans,measurement-runs,measurement-projections,measurement-request,read-pages,scheduler-profiles,experiments}.tsx`, `lib/{measurements,experiments}.ts`+test, 필요한 `components/stage-breakdown.tsx` | 모든 측정 배지가 같은 서버 assessment; 실패 1줄·긴 diff 상세만; 논문 표 CSV/LaTeX; 유휴/총/증분 구분·null/0/CI; 한 화면 요청·부족 batch·Unknown/중복·사전 검사 실패; 두 publisher·비대응/중복 단계·모든 서비스·정밀도/부호·모바일 | U2와 같은 dashboard image. publisher 변경/과거 publication 덮어쓰기 불필요 |

### 공유 인터페이스와 삭제 책임

- Go JSON 정본은 API §13. U1은 서버 DTO·판정·cache만, U2가 `read-types.ts`의 `MeasurementCoverageResponse`, `ResearchAssessment`, `PaperRow` 타입을 그대로 옮긴다. UI가 타입을 편하게 만들려고 서버 null 의미를 바꾸지 않는다.
- U2 `lib/measurement-coverage.ts`: `useMeasurementCoverage(namespace)`는 응답/오류/갱신 함수를 반환한다. `assessmentFor(kind, uid)`는 서버 `entities` lookup만 하며 누락 시 ‘서버 판정 미제공’을 표시한다. 상태를 로컬 계산하지 않는다. 인증 epoch 전환에 cache를 버린다.
- `ResearchStatus({assessment})`, `MeasurementNavigation({active})`는 U2 소유 새 navigation 파일의 단순 표시 컴포넌트다. U3는 서버 label/reasons를 렌더링한다. 따로 상태 label map/CI 판정기를 만들지 않는다.
- U2는 catch-all에서 U3의 `MeasurementRequestsPage`를 `/measurements/requests`에 연결한다. U3는 기존 `MeasurementPlansPage` 목록 구현을 자기 새 파일로 옮기고 U2가 기존 coverage 파일의 옛 목록 export를 삭제한다. 두 파일을 두 작업자가 동시에 편집하지 않는다.
- U2는 `Screen` 기존 props를 유지하고 heading/nav 구조를 고친다. U3는 width/data·header action·breadcrumb props만 사용한다. 전역 CSS/route/read-types/relay는 U2만 편집한다.
- U2가 브라우저 전수 조인·fallback 판정을 삭제한다. U3가 별도 완료/논문 체크 판정과 7단계·서비스 선택 chart·목록 JSON diff를 삭제한다. U1이 서버에서 쓰는 원본 phase와 검증 loader는 삭제 대상이 아니다.

## 8. 통합 수용과 반복

이 문서 작업에서는 테스트·빌드·배포·새 설문을 실행하지 않는다. Main이 세 슬라이스 통합 뒤 한 번 실행한다.
기존 Go package tests와 dashboard test/typecheck/lint/build, route·SAR 회귀를 포함하며 UI 소스 문자열 검사는 수용 근거로 쓰지 않는다.

Main 실행용 최소 명령(이 설계 단계에서는 실행하지 않음):

```sh
go test ./cmd/chill-server ./cmd/console ./internal/operatorconsole/... ./internal/serving/httpapi ./internal/profiling/derivation ./internal/profiling/profiler/projection
pnpm --dir chill-dashboard test
pnpm --dir chill-dashboard typecheck
pnpm --dir chill-dashboard lint
pnpm --dir chill-dashboard build
```

새 DTO/reducer/serializer 회귀는 각 소유 package의 기존 test 파일/명령에 포함한다.
실브라우저·성능·SAR/RBAC·배포·재설문 수용은 위 명령 성공과 별도이며 아래 기준으로 기록한다.

1. U1 GET은 기존 namespace 정책과 권한을 통과한 사용자만 읽는다. 401/403/503·원근거 부재·부분 실패를 각각 주입하고 비권한 사용자에게 오래된 cache가 새지 않는지 확인한다.
2. 실단면 모델 3/class 8/요청 88/Run 1,155/profile 78 및 동시 1·5 사용자, prewarm/TTL 만료/재시작을 구분한다. 성공 200 첫 방문·재방문 p95 <2 s, 인증·BFF 포함 browser 경과와 서버 시간을 따로 기록한다. 냉시작 503을 성공 응답으로 세지 않는다.
3. M1: 1분 안에 지정 모델/장치의 모드별 완료·부족 batch·지원 미확인과 기준 시각을 설명한다. M2: 목록에서 실제 실패 원인과 확인 가능한 후속을 안다.
4. M3: 셀 1회 선택으로 논문 표 행·전력 범위·CI/n·누락·서버 판정 사유를 읽는다. 실패 요청에서 idle 3회를 active 3회 통과로 표시하면 실패다.
5. M5: 모델/장치/mode/batch를 직접 고르고 부족 batch·실제 시간·사전 검사 원인을 안다. 쓰기 승인이 없으면 최종 검토까지만, 검사는 별도 허가한 격리 환경에서 수행한다.
6. E1: 1분 안에 에너지와 모든 서비스의 득실·해석 제한을 말한다. E2: 비대응 단계와 부하 변화·실제 시간순/대응 축 차이를 설명한다.
7. 1440/1024/768/390px, light/dark, 키보드만으로 탭·표 정렬 aria-sort·44px 모바일 타깃·Sheet 닫기 focus·뒤로가기를 확인한다. 표/CTA/긴 JSON이 인접 열과 focus를 가리지 않는다.
8. 같은 세 페르소나의 독립 새 세션에서 표준 SUS 10문항과 M1~M5/E1~E2를 다시 평가한다. **평균 ≥70, M1·M3·M5·E1 전원 성공**을 모두 만족할 때 수용한다. 부분 성공은 성공이 아니다. 미달이면 관찰 실패를 다시 설계·구현·배포·재설문하며 점수 달성을 문서만으로 선언하지 않는다.

## 9. Main의 타 모델 교차 검토 질문 5개

이 초안 작성자는 패널을 호출하지 않았다. Main이 다른 모델 패널에 아래를 검토시키고 결정/반론을 이 계획의 후속 기록에 남긴다.

1. 연구 홈과 6개 측정 탭이 M1/M3/M5를 줄이는가? 중복 heading·breadcrumb·표/패널 전환 때문에 새로 생기는 탐색 부담은 없는가?
2. ChillServer 집계와 Console의 retained 통계/호환성 분리가 기존 소유권을 지키는가? prewarm·TTL·부분 실패 설계가 성공 응답 <2초를 실제로 달성하면서 ‘확인 불가’를 성능 편법으로 남용하지 않는가?
3. 완료/논문 적격/경고와 원본 실행 phase의 차이가 명확한가? 노드 전체 전력·원본 timestamp 부재·공유 idle·재사용 profile에서 적격을 과장하거나 정상 근거를 부당하게 막지 않는가?
4. 한 화면 요청이 빠진 batch만 고르면서 CPU·dataset·protocol이 다른 결과를 중복으로 오인하지 않는가? Unknown 지원과 검사 장애의 복구·명시적 제출이 안전한가?
5. 전 서비스 small multiples·비대응 단계·동일 자릿수·CHILL−정적 부호·탭 sessionStorage 정책에서 과학적 비교나 인증의 잘못된 확신을 주는 지점이 남았는가?
