# CHILL Dashboard 구현 설계

## 1. 구현 경계와 결정

[Dashboard SRS](../../spec/dashboard/SRS.md)의 52개 요구사항을 구현 기준으로 사용한다.
제어와 측정 의미는 [시스템 명세](../../spec/README.md)를 따른다.
이 문서는 웹 구현 구조, 실제 API 재사용과 필요한 CHILL 생산자, 전체 수용 검증을 정한다.

- 템플릿은 공식 `shadcn-ui/ui`의 `templates/next-app`과 `dashboard-01` 블록을 사용한다.
- 웹 모듈은 저장소 루트의 `chill-dashboard/`에 두고 독립적으로 빌드하고 배포한다.
- Next.js App Router, React, TypeScript, Tailwind CSS와 shadcn/ui를 사용한다.
- 화면은 컴포넌트 조합으로 구성한다. 서버는 Next.js Route Handler로 고정 CHILL API를 중계한다.
- 인증, 검증, 변경 선언, 작업 진행, 판단, 통계와 Evidence는 CHILL의 해당 생산자가 소유한다.
- 현재 개발은 제공 중인 API 연결과 전체 영역의 화면 틀을 구성한다. 전체 기능의 완료 기준은 SRS와 18개 수용 시나리오다.

### 1.1 현재 개발 범위

| CHILL 준비 상태 | 웹 개발 범위 |
|---|---|
| API 제공 | 모델과 워크로드 관리, 단일 추론, POOL의 제공 필드와 요청 관측을 실제 응답에 연결한다. |
| 인증된 조회 API 제공 | System, Inventory, bounded Run 목록, 검증된 Evidence Passport, 봉인된 campaign result와 read-only preflight를 실제 응답에 연결한다. |
| 스케줄러 조회 API 제공 | 운영 판단, 실제 구성, 유지안과 후보의 예상 SLO 및 J, 명시적 구간의 부하를 독립 화면에 연결한다. |
| 설정 조회와 적용 API 제공 | 모든 SchedulerSettings 필드를 현재 값에서 편집하고 변경 전후, 행위자와 Runtime 적용 결과를 확인한다. |
| 스케줄러 기록 API 제공 | 현재/과거 동작, 판단 A/B, 설정 변경 및 개별 노드 전력 기록을 연결한다. 전체 실행 그룹 에너지 합계, 동작별 인과 귀속, 수동 취소/재시도와 실험 실행 API는 미제공이다. |

연결 준비 영역은 기능 설명과 `연결 준비 중` 상태를 표시한다. 실제 데이터 표와 그래프는
API 응답으로 채우고, 제출 및 제어 버튼은 해당 API와 권한 검사가 연결되면 활성화한다.
화면 틀의 개발 상태와 CHILL의 실제 운영 상태를 구분한다.
전체 SRS의 기능 수용은 각 생산자와 API를 연결한 뒤 실제 흐름으로 검증한다.

운영 판단은 `/scheduler`, 후보 비교는 `/scheduler/candidates`, 실제 부하는
`/scheduler/load`에서 조회한다. 상세는 열 때의 응답을 유지하며 새 관측 시각을
안내한다. load 응답의 arrivals는 owner가 생략하므로 UID와 원래 구간으로 전체
적용 정의의 요청 관측에 연결한다. 후보가 제공한 정의 identity는 그대로 전달한다.
요청 상세는 CHILL 배정과 nativeProgress의 실제 실행 관측을 구분한다.
`/configuration`은 duration 네 필드, Serving 메모리, 반복 비교와 탐색 상한,
seed 및 제어요소 boolean 아홉 필드를 편집한다. 정밀 정수는 문자열로 유지하며
ms를 정확한 ns로 변환한다. 저장 검토는 현재 설정을 재조회하고 입력을 유지한 채
최신 값과 비교한다. 전체 객체의 마지막 적용값이 현재 설정이 되며 버전 조건과
영향 예측은 제공하지 않는다. appliedAt은 설정 저장과 Runtime 적용 시각이다.
응답이 불명확하면 현재 값과 제한된 최근 기록을 조회한 뒤 명시적으로 다시 제출한다.
`/scheduler/history`의 종류 필터와 `/scheduler/power`의 노드 필터는 현재 가져온
페이지에만 적용한다. 제출된 원래 from/to를 cursor 이동에 복사하고 입력 draft는
별도로 유지한다. 선택 기록은 같은 구간의 append offset에 limit=1로 복원하며
종류, at, 대상과 action의 노드/시작 시각이 다르면 다른 사건으로 대체하지 않는다.
A/B 비교는 각 판단의 입력 구간과 미래 horizon을 구분하고 UID/고정 정의별
owner whole-window cohort를 조회한다. 웹은 백분위, 총에너지와 실험 점수를 계산하지 않는다.
동작은 제공된 단계와 before/actual을 표시하며 같은 ID의 다른 시작을 병합하지 않는다.
전력 상세는 단일 기록 모달의 W 시계열과 meter/scope/source/coverage/실패를 표시한다.
표본 시각은 collector 수신 시각이며 값 없음과 0 J는 별개다. 내보내기는 선택 기록
또는 현재 페이지의 출처, 구간과 모집단을 명시한다.


## 2. 최신 조사와 Git 템플릿

### 2.1 선정 근거

조사 기준일은 **2026-10-07 UTC**다. 사용률, 제품 기본값과 Git 저장소 규모를 함께 비교했다.
저장소의 용도가 서로 다르므로 별 수는 채택 규모의 참고값으로 사용한다.

| 항목 | 확인한 근거 | 선택에 반영한 내용 |
|---|---|---|
| 언어 | [GitHub Octoverse](https://github.blog/news-insights/octoverse/octoverse-a-new-developer-joins-github-every-second-as-ai-leads-typescript-to-1/)는 2025년 8월 TypeScript가 GitHub 최다 사용 언어가 됐다고 보고한다. | 웹 코드는 TypeScript를 사용한다. |
| 웹 생태계 | [Stack Overflow 2026](https://survey.stackoverflow.co/2026/technology/data/web-framework)의 전체 응답자 기준 React 41.5%, Next.js 20.6%다. | 보편적인 React 생태계와 Next.js를 사용한다. |
| 바이브코딩 기본값 | [v0](https://v0.app/docs/design-systems-2)의 기본 starter는 Next.js, shadcn/ui, Tailwind다. [Lovable](https://docs.lovable.dev/introduction/faq)의 신규 앱은 2026년 5월 13일부터 TanStack Start를 사용한다. | 공식 shadcn 템플릿을 선택한다. 도구별 기본 framework의 차이는 유지한다. |
| 아키텍처 | [React의 앱 생성 지침](https://react.dev/learn/creating-a-react-app)은 framework 사용을 권장하고 Next.js의 Node 및 Docker 자체 호스팅을 안내한다. [Next.js BFF](https://nextjs.org/docs/app/guides/backend-for-frontend)는 Route Handler 중계를 제공한다. | 하나의 웹서비스에 화면과 얇은 BFF를 둔다. |
| 디자인 패턴 | [Thinking in React](https://react.dev/learn/thinking-in-react)는 컴포넌트 계층, 단방향 데이터 흐름과 최소 상태를 사용한다. | 기능별 화면, props 조합, 서버 상태와 폼 상태의 분리를 사용한다. |

| Git 후보 | 조사 시 별 수 | 검토 결과 |
|---|---:|---|
| [shadcn-ui/ui](https://github.com/shadcn-ui/ui) ([GitHub API](https://api.github.com/repos/shadcn-ui/ui)) | 125,216 | MIT. 공식 Next 앱 템플릿과 [대시보드 블록](https://ui.shadcn.com/blocks)을 선택한다. |
| [satnaing/shadcn-admin](https://github.com/satnaing/shadcn-admin) ([GitHub API](https://api.github.com/repos/satnaing/shadcn-admin)) | 15,617 | MIT. React와 Vite 기반 관리 화면의 대안이다. |
| [Kiranism/next-shadcn-dashboard-starter](https://github.com/Kiranism/next-shadcn-dashboard-starter) ([GitHub API](https://api.github.com/repos/Kiranism/next-shadcn-dashboard-starter)) | 7,104 | MIT. Clerk 조직과 billing, Sentry, 전역 상태와 명령 팔레트 등의 제품 구성이 포함된다. |

CHILL이 필요한 화면 컴포넌트와 API 중계를 갖는 공식 기반을 선택한다.
인증은 기존 Kubernetes identity를, 업무 동작은 CHILL 공통 API를 사용한다.

### 2.2 고정 Git 출처와 재사용

실제 shallow clone으로 다음 commit의 소스와 라이선스를 확인했다.

- 저장소: `https://github.com/shadcn-ui/ui.git`
- 고정 commit: `e8c3143b1cd191280befcd6c9538284bb43399a8`
- 앱 원본: [templates/next-app](https://github.com/shadcn-ui/ui/tree/e8c3143b1cd191280befcd6c9538284bb43399a8/templates/next-app)
- 화면 원본: [dashboard-01](https://github.com/shadcn-ui/ui/tree/e8c3143b1cd191280befcd6c9538284bb43399a8/apps/v4/registry/new-york-v4/blocks/dashboard-01)
- 라이선스: [MIT 원문](https://github.com/shadcn-ui/ui/blob/e8c3143b1cd191280befcd6c9538284bb43399a8/LICENSE.md). 복사한 코드에 저작권과 허가 문구를 보존한다. CHILL 저장소의 라이선스는 기존 정책을 유지한다.

구현 시작 시 해당 commit을 checkout하고 앱 템플릿, 선택한 블록과 그 블록이 참조하는
UI, hook 및 util만 모듈로 가져온다. import 경로를 모듈 내부 경로로 정리하고
출처, commit, 복사 범위를 모듈 README와 MIT 고지에 기록한다.

재사용 대상은 Sidebar, Header, Card, Table, Chart, Dialog, Tabs, Select, 입력과 상태 표시다.
원본 `data.json`, 예제 매출과 desktop/mobile 시계열, 예제 계정과 행 재정렬은 제거한다.
한국어 문서 언어를 사용한다. 글꼴은 한글을 포함한 Asta Sans를 `next/font`로 빌드 때 받아
자체 정적 자산으로 제공하므로 브라우저는 외부 글꼴 서버에 요청하지 않는다.

### 2.3 라이브러리와 버전

[원본 package.json](https://github.com/shadcn-ui/ui/blob/e8c3143b1cd191280befcd6c9538284bb43399a8/templates/next-app/package.json)의
Next `16.3.6`, React 및 React DOM `19.3.0`, TypeScript `^5`, Tailwind `^4`를 초기 기준으로 유지한다.
조사 시 npm 최신값은 [Next 16.4.0](https://registry.npmjs.org/next/latest),
[TypeScript 7.0.2](https://registry.npmjs.org/typescript/latest)였다.
pnpm은 [원본 toolchain](https://github.com/shadcn-ui/ui/blob/e8c3143b1cd191280befcd6c9538284bb43399a8/package.json)의 `10.33.4`를 사용한다.
구현에서 호환성을 확인한 정확한 의존성을 lockfile에 고정한다.
런타임은 [Node.js 24.21.0 LTS](https://nodejs.org/en/about/previous-releases)를 사용한다.

| 역할 | 선택 | 사용 범위 |
|---|---|---|
| UI와 스타일 | shadcn/ui, Radix, Tailwind와 실제 복사한 UI의 util 및 아이콘 의존성 | 원본 접근성 primitive와 반응형 화면을 재사용한다. |
| 표 | 원본 블록의 TanStack Table v9 | 검색, 필터, 정렬과 API pagination 상태를 연결한다. 복사한 v9 API를 기준으로 한다. |
| 그래프 | 원본 블록의 Recharts 3.8.0 | API가 제공한 분포와 시계열을 그린다. 관측 누락은 null과 공백으로 표시한다. |
| 서버 상태 | [TanStack Query 5.104.1](https://registry.npmjs.org/@tanstack%2Freact-query/latest), native `fetch` | cache, 제한된 polling, 요청 취소와 변경 중 상태를 사용한다. |
| 폼과 로컬 상태 | native HTML, React `useState`, URL search parameters | 업무 입력과 서버 필드 오류를 연결한다. 도메인 검증은 CHILL에서 수행한다. |
| 날짜와 시간 | `Intl.DateTimeFormat`, `Date`, 원본 RFC3339Nano 문자열 | 표시와 조회 구간 변환을 수행하고 원본 정밀도를 보존한다. |

의존성은 `next-app`과 복사한 UI의 실제 import를 기준으로 설치한다.
표는 API의 구체적인 TypeScript 타입과 업무에 필요한 검색, 정렬 및 pagination을 사용한다.

## 3. 모듈과 실행 구조

현재 웹 모듈의 실행 구조다. 같은 CHILL Helm release의 별도 Deployment로 배포한다.

```text
브라우저: 화면, 폼, 메모리 인증정보, Query cache
    → HTTPS chill-dashboard: Next.js 화면과 /api/chill Route Handler
    → HTTPS CHILL 공통 관리 진입점
        → Serving의 Model 및 Workload owner와 요청 journal
        → Console query 및 검증된 Evidence 조회
        → 측정, 자원 관리, 설정과 전환의 해당 CHILL 생산자
```

현재 모듈 배치는 다음과 같다.

```text
chill-dashboard/
  app/                     화면 route, layout와 api/chill/[...path]/route.ts
  components/ui/           선택한 공식 UI 코드
  features/                models, workloads, resources, measurements,
                           configuration, transitions, observations, changes
  lib/                     구체적인 API 타입, fetch와 표시 함수
  public/                  자체 정적 자산
  package.json             독립 dev, build, start, lint, typecheck
  pnpm-lock.yaml           검증한 의존성 고정
```

App Router의 layout은 shell과 안전한 웹 설정을 전달한다. 인증된 데이터는 client 화면에서
같은 origin의 BFF를 호출해 조회한다. 서버 데이터는 Query cache에, 작성 중 입력은 폼에,
대상과 필터 및 구간은 URL에 둔다.

공통 HTTPS 진입점은 `cmd/serving-entrypoint`에서 조립한다. 기존 Serving handler를 유지하고
Console 조회는 요청별 사용자 인증 및 SAR 후 고정 private Console origin으로 중계한다.
Console의 기존 Evidence Store binding, retained loader 및 production preflight resolver를 재사용한다.
`--dashboard-url`은 API와 health 경로를 제외한 화면, 정적 자산과 BFF를 private Dashboard로
중계한다. 공개 HTTPS owner → Dashboard BFF → HTTPS owner `/api/v1`의 경로는 서로 분리된다.
Standalone Console과 CLI는 기존 query 및 검증 계약을 유지한다. 별도 ingress controller가
없어도 owner의 HTTPS NodePort로 Dashboard에 접근할 수 있다.

패키지 허용 범위는 [아키텍처 규칙](../../../.go-arch-lint.yml)을 따른다.
Serving과 Console의 handler 연결은 `cmd/**`의 조립 권한을 사용한다.
웹은 CHILL API를 호출하고 Kubernetes, firstfloor, S3와 Node 동작은 해당 CHILL 생산자가 소유한다.

## 4. 인증과 API 중계

### 4.1 기존 신원 전달

기존 Kubernetes bearer token을 인증 화면에서 입력받아 탭의 메모리에 둔다.
토큰 발급과 갱신은 클러스터의 기존 인증 절차를 사용한다.
브라우저는 `Authorization: Bearer`로 같은 origin의 BFF를 호출하고 BFF는 원본 토큰을
CHILL에 전달한다. CHILL이 TokenReview와 대상 및 HTTP 동작의 SubjectAccessReview를 수행한다.

새로고침하면 토큰을 다시 입력한다. 대상과 작업 URL은 유지해 인증 후 실제 상태를 조회한다.
로그아웃과 토큰 변경 때 진행 조회와 변경을 취소하고 cache를 비운 뒤 새 native QueryClient로 교체하며 민감한 폼을 remount한다. 이전 신원의 늦은 continuation은 새 cache에 합류하지 않는다.
토큰의 취급 범위는 탭 메모리와 TLS 요청의 Authorization header다.

현재 [Authorizer](../../../internal/serving/kube/authorizer.go)는 TokenReview로 검증한
username과 UID의 private `Principal`을 반환하고 SAR를 수행한다. handler와 manager는 그
신원만 변경 작업과 management journal에 기록하며 클라이언트 신원 header는 신뢰하지 않는다.

- 인증 누락, 거부 또는 무효 token은 401, 실제 SAR 거부는 403이다.
- review 통신 실패, API 권한 오류와 SAR evaluation 오류는 503이며 변경을 접수하지 않는다.
- SAR의 non-resource path는 외부 HTTP path에 `/chill`을 붙인다. Kubernetes 기본
  `system:discovery`의 `/api/*` 읽기 권한이 CHILL 자료 접근을 허용하지 않도록 분리한다.

Console reader의 Kubernetes client는 서버 identity로 동작한다. 따라서 공통 진입점의
정확한 verb와 path SAR가 실제 사용자 권한 경계다. namespace와 대상이 포함된 경로 및
내보내기 동작에 권한을 부여하고, ClusterRole binding은 클러스터 관리자가 소유한다.
Console private leg에는 사용자 token/cookie/신원 header를 전달하지 않는다. 공개 API mount는 정확한 인증 경계를 거친다.

### 4.2 얇은 BFF 계약

- 중계 대상은 서버 전용 고정 `CHILL_BASE_URL` HTTPS origin과 `NODE_EXTRA_CA_CERTS`로 정한다.
- 허용한 method와 path를 `/api/chill/v1/...`에서 `/api/v1/...`로 연결한다. native Node HTTPS가 CA와 hostname을 검증하고 redirect를 따르지 않는다.
- `Authorization`, `If-Match`, `Content-Type`, 추론 timeout과 필요한 CHILL 식별자 header만 전달한다. 브라우저 cookie나 신원 header는 전달하지 않는다.
- HTTP 상태, 완전한 bounded 응답 본문, 요청 ID와 Workload UID를 보존한다. 요청과 응답은 각각 8 MiB, 응답 header는 16 KiB로 제한하며 미완료 본문을 부분 성공으로 반환하지 않는다.
- 인증된 fetch와 응답에 `no-store`를 적용하고 사용자별 데이터는 해당 탭의 Query cache에서 관리한다.
- 조회, 변경과 추론의 timeout 및 body 한도를 웹 설정으로 제한한다. 추론 timeout은 CHILL 허용 범위와 사용자가 선택한 값을 따른다.
- 변경, 측정과 추론의 응답 미수신은 결과 미확인으로 표시한다. BFF 연결 오류와 timeout은 별도 Dashboard 오류 코드로 표시한다.
- API의 202는 접수로 표시한다. 재제출은 원본 상태 조회와 사용자 확인을 거쳐 수행한다.

변경 재전송은 사용자 확인 후 수행한다. 오류의 API code, 대상, field와 이유를 보존한다.
외부 문자열은 React의 텍스트 렌더링을 사용한다. 원본 JSON도 텍스트로 표시하고
자료 내보내기는 structured 원본의 credential 필드를 제거하며 원본 객체를 변경하지 않는다.

스케줄러의 int64 seed, uint64 메모리와 duration은 native JSON reviver의 원문과
JSON.rawJSON으로 정확하게 유지한다. 지원 브라우저에서만 설정을 저장한다.
원본 보기와 내보내기는 같은 직렬화를 사용하고 기존 credential 제거를 유지한다.
조회는 serving-manager의 정확한 세 scheduler GET 경로를 사용한다.
설정 PUT은 unbound scheduler-editor ClusterRole과 serving namespace의 같은 이름
Role을 관리자가 선택한 계정에 각각 binding한다. Role은 설정된 ConfigMap 하나의
update만 허용한다. Dashboard ServiceAccount에 사용자 변경 권한을 자동 부여하지 않는다.


## 5. 현재 API와 추가 계약

### 5.1 현재 재사용 가능한 표면

아래 준비 상태의 근거는 소스의 HTTP 등록과 조회 구현이다.
Serving 경로는 [현재 handler](../../../internal/serving/entrypoint/handler.go),
Console 경로는 [현재 read handler](../../../internal/operatorconsole/readapi/handler.go)를 기준으로 한다.

| method와 `/api/v1` 하위 경로 | 현재 제공 범위 | 웹 연결과 필요한 보완 |
|---|---|---|
| `GET, POST /models`, `GET, PUT, DELETE /models/{id}` | ModelSpec 조회와 변경, 버전 전제조건 및 참조 보호 | 등록 폼과 영향 확인에 재사용한다. |
| `GET, POST /workloads`, `GET, PUT, DELETE /workloads/{id}` | desired, appliedDefinition, supportedInputs, admission, readiness, operation | 정의, 실제 준비와 접수 및 작업을 구분한다. |
| `POST /workloads/{id}/infer` | 단일 named-tensor 입력, 실제 응답과 요청 identity | 단일 추론에 재사용한다. |
| `GET /pools`, `GET /pools/{id}` | 실제 Node와 Workload identity, 인스턴스, 준비 및 대기와 실행 수 | 자원, 모델 경로와 전력모드 및 활성 범위의 실제 관측을 추가한다. |
| `GET /observations/workloads/{uid}` | 구간, 정의 및 outcome 필터, cursor, 전체 선택 구간 cohort와 raw 이벤트 | SLO 초과, 분포, 집계 설정과 archive 조회 계약을 보완한다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence-passport` | 검증된 Run 계보와 제한된 Evidence 요약 | 공통 HTTPS 경계에 인증을 적용해 재사용한다. |
| `GET /namespaces/{namespace}/batch-energy-optimizations/{name}/result` | 캠페인의 봉인된 결과 | 진행 상세와 구분해 재사용한다. |
| `POST /namespaces/{namespace}/batch-energy-optimizations/preflight` | 입력을 해석하고 계획을 반환하는 읽기 전용 요청 | 조건 안내에 사용한다. 캠페인 접수는 별도 POST로 수행한다. |
| `GET /scheduler`, `GET /scheduler/settings`, `GET /scheduler/state`, `PUT /scheduler/settings` | 현재 설정, 실제 snapshot과 판단, 동작과 결과, 전체 설정 적용 및 행위자 journal | 고정 BFF 경로에 연결한다. 버전 조건과 dry-run 계약은 제공하지 않는다. |
| `GET /observations/load` | 명시적 구간의 수신, 접수, 배정과 완료 사건 수 및 사건률 | from/to를 필수로 전달한다. 완료는 수신 cohort와 별개의 사건 집계다. |
| `GET /observations/scheduler` | 판단, snapshot, 설정 변경, action과 전력 관측의 보관 중인 append-order 페이지 | from/to, bounded limit와 cursor를 사용한다. 총건수와 구간 완전성은 제공하지 않는다. |

System, Inventory와 Run 목록은 기존
[System query](../../../internal/operatorconsole/kube/system_query.go),
[Inventory query](../../../internal/operatorconsole/kube/inventory_query.go),
[Run query](../../../internal/operatorconsole/kube/profiling_runs_query.go)를 재사용하는
[HTTP query handler](../../../internal/operatorconsole/readapi/queries.go)에서 제공한다.
`limit` 1..200, `offset` 0..1,000,000과 bounded `q`를 검증한다.
Run의 `condition=Type=True|False|Unknown`은 원본 condition 필터이며 synthetic phase가 아니다.
Inventory는 각 원본의 resourceVersion과 개별 조회 시각을 유지하는 조회 projection이다.

### 5.2 연결 예정 API 계약

다음은 영역별 연결 계약과 생산자 책임이다. 현재 웹 개발은 제공된 경로를 연결하고
준비 중인 영역은 화면 틀과 계약으로 유지한다. 새 경로는 기존 TokenReview 및
정확한 verb와 path 권한 검사를 통과한 뒤 연결한다.

| 제안 method와 `/api/v1` 하위 경로 | 응답 또는 입력의 핵심 계약 | 책임과 현재 공백 |
|---|---|---|
| `GET /systems`, `GET /inventory` | 원본 view, identity, resourceVersion과 조회 시각 | 인증된 Console HTTP query 구현 완료. System `capabilities` 생산자는 아직 제공하지 않는다. |
| `GET /profiling/options` | 모델과 경로, 장비와 Node, 전력모드, 입력 및 측정과 관측 정책의 선택 항목과 sourceRef | 기존 선언과 configured fact resolver를 사용하는 Console 조회가 필요하다. |
| `GET, POST /namespaces/{namespace}/profiling-runs`, `GET .../profiling-runs/{name}` | 생성 입력 `{name, spec}`, Run identity와 immutable spec, 실제 status와 시각 | bounded GET 목록 구현 완료. exact detail과 인증된 create adapter는 연결 준비 중이다. |
| `GET, POST /namespaces/{namespace}/batch-energy-optimizations`, `GET .../batch-energy-optimizations/{name}` | immutable campaign spec, child Run 및 trial 참조, 실제 진행과 result 참조 | 실제 campaign controller를 재사용한다. 목록, 진행 조회와 create adapter가 필요하다. |
| `GET, POST /namespaces/{namespace}/power-mode-transition-costs`, `GET .../power-mode-transition-costs/{name}` | 대상 Node, 두 전력모드와 설정된 반복 측정 조건, 실제 시도와 결과 참조 | 기존 전환 비용 측정과 NodeActuator를 재사용한다. 조회 및 create adapter가 필요하다. |
| `GET /profiles` | 조건별 성공 Run, 실행시간 분포, 처리량, 메모리, 전력과 에너지, 유휴 기준 및 trial 참조 | 검증된 Run과 campaign 결과의 조회 projection이 필요하다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence/raw`, `GET .../power-mode-transition-costs/{name}/result` | 권한 범위의 구조화된 raw 및 전환 측정 결과, 원본 참조와 단위 | 기존 검증 경계와 봉인된 결과 reader를 재사용하는 전송 경로가 필요하다. |
| `GET, PUT /allowed-resources`, `POST /nodes/{uid}/maintenance` | 허용 Node와 실행 경로 및 전력모드, 제외 또는 복귀, version, 영향, 원본 작업 참조 | 자원 정책과 유지보수 owner가 필요하다. 적용은 CHILL의 실제 드레인과 actuator가 수행한다. |
| `GET /observations/workloads/{uid}/requests/{requestID}` | 원본 적용 정의, 단계와 실제 outcome, CHILL 종단 지연과 SLO 초과 여부 | 기존 journal의 exact 요청 조회 전송 경로가 필요하다. |
| 운영 전력 집계 API (미제공) | 전체 및 활성 POOL, 전환 포함, 전체와 증분 에너지 및 누락 | 개별 node/meter/window 기록은 `GET /observations/scheduler`로 연결했다. POOL 집계와 동작별 귀속 생산자는 없다. |
| `GET /experiments`, `GET /experiments/{id}` | 시나리오, 프로파일과 설정, 초기 구성, 제어요소, 요청과 전력 및 전환의 같은 시간축 참조 | 시스템 실험 기록과 조회 projection이 필요하다. |
| 전체 변경 feed (미제공) | 영역 간 행위자, 대상과 접수 및 적용 결과 | 서비스 management journal과 scheduler settings 사건은 각각 연결했다. 영역 간 통합 feed는 제공되지 않는다. |
| `GET /exports` | 자료 종류, 대상, from과 to 및 필터로 선택한 records, 집계 조건, 단위와 sourceRefs | 해당 생산자의 조회 및 archive를 사용하는 권한 검사와 비밀정보 제거가 필요하다. |

목록에는 제한된 `limit`, opaque `cursor`, 검색 및 지원한 sort와 filter를 제공한다.
시간 조회에는 RFC3339Nano `from`, `to`와 실제 적용한 구간 및 집계 설정을 반환한다.
현재 응답의 필드 이름과 상태 의미를 유지하고 각 영역의 구체적인 DTO를 사용한다.
새 조회에는 identity, 원본별 시각, 실제 범위와 누락 이유를 포함한다.
`capabilities`가 제공되면 `{name: string, available: boolean, reason: string}` 배열을 사용한다.
지원 여부와 사용자의 호출 권한은 별도로 표시한다.
새 DTO의 관측 공백은 null과 이유로 표현한다. sourceRef는 원본 kind, namespace, 이름과 UID,
resourceVersion 또는 봉인된 객체 참조를 사용하고 관측 시각은 RFC3339Nano로 반환한다.
변경 진행은 API가 반환한 원본 operation 또는 대상 identity로 조회한다. 전후 정의와
desired 및 applied version은 해당 생산자의 계약을 사용하고 전환은 실제 남은 상태와 후속 판단을 연결한다.

### 5.3 CHILL 생산자의 책임

- Run과 campaign create는 기존 Kubernetes schema 및 admission을 사용한다. 실제 실행과 progress는 기존 controller의 관측을 사용한다. 스펙 변경은 새 요청으로 제출한다.
- Evidence 조회와 내보내기는 [RetainedLoader](../../../internal/profiling/profiler/evidence/kube/retained_loader.go)의 검증된 타입을 사용한다. 검증은 그 경계에서 수행하고 raw 배열은 별도 bounded 조회 또는 내보내기로 제공한다.
- 프로파일 조회는 검증된 성공 Run과 campaign 결과를 사용하고, 조건에 맞는 최신 성공 선택과 한 Run 내부 trial 집계를 각각 표시한다.
- POOL projection은 실제 Pod와 Node, 모델 및 경로, 자원 할당과 검증된 전력모드 readback을 사용한다. 측정 당시 조건은 provenance로 표시한다.
- 허용 자원과 유지보수 API는 버전 있는 선언, 확인 가능한 영향과 실제 진행을 반환한다. 내부 `MoveNodePool`은 실제 요청 드레인 후 label을 바꾸는 원자 동작으로 재사용한다. 목적지 선택과 유지보수 상태는 자원 관리 owner가 소유한다.
- 구성 선택과 시스템 전환은 [구성 선택 명세](../../spec/04_configuration_selection.md)와 [전환 명세](../../spec/05_transition_control.md)의 생산자가 수행한다. [현재 구현 범위](../../../README.md#chill)에 맞춰 화면과 API의 연결 준비 상태를 표시한다.
- 전력 생산자는 각 Node를 한 번 포함한 전체 POOL과 활성 범위, 실제 전환 소비와 관측 누락을 반환한다. 프로파일링 watts는 해당 측정 조건으로 한정한다.
- 기존 request journal은 전체 선택 구간의 통계를 page 분할 전에 계산한다. 추가 분포와 백분위, SLO 초과 및 archive 조회도 Serving이 계산한다. archive 객체 참조와 실제 archive 조회 능력을 구분한다.
- 변경의 actor와 전후 정의, 접수 및 적용 결과는 도메인 생산자가 보존한다. `changes`는 이 원본 기록의 조회 projection이다. 웹 접근 로그는 중계 진단으로 분리한다.

측정 웹 동작은 immutable 요청 접수와 실제 진행 및 결과 조회로 구성한다.
개입은 해당 API가 제공하는 동작과 조건을 따른다.
저장된 계약 변경은 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 작업 단위와 정리 원칙을 따른다.

## 6. 핵심 화면과 변경 흐름

### 6.1 route와 화면

한 화면은 한 기능만 한다. 질문 하나에 답하거나 동작 하나를 끝내는 화면이며, 결론 문장 하나, 핵심 값
하나, 주 동작 하나로 구성한다. 딸린 정보는 같은 화면에 구역을 더하지 않고 `›` 행으로 다음 화면에
두거나 Dialog로 연다. 여러 값을 받는 입력은 한 단계에 질문 하나인 단계형으로 만들고, 고를 수 있는
원본 목록이 있으면 직접 입력 대신 선택을 받는다. 본문은 가운데 좁은 한 열이다.

| 웹 route | 한 가지 기능 |
|---|---|
| `/` | 지금 확인할 일 목록. 접수 불가 서비스, 준비 안 된 노드, 목표 초과 예상, 실패·진행 중 변경을 심각한 순서로 나열하고 각 행이 해결 화면으로 이동한다. |
| `/workloads`, `/models` | 서비스와 모델 목록. 주 동작은 추가다. |
| `/workloads/[name]` | 서비스 상태의 결론과 다음 할 일 하나. 정의와 적용, 측정 근거, 배치, 최근 운영, 변경 기록은 행으로 각 화면에 연결한다. |
| `/workloads/[name]/{definition,evidence,placement,operation,changes}` | 서비스의 한 측면. 배치는 `v1/pools`와 운영 판단의 요청 분배, 운영과 변경은 표시한 최근 구간의 owner 관측에서 읽는다. |
| `/models/[name]`, `/models/[name]/{files,services}` | 모델 요약, 파일과 실행 방식, 연결된 서비스와 삭제 영향. |
| `/workloads/new`, `/models/new`, `/…/[name]/edit` | 단계형 등록·수정과 전후 검토. 버전 사전 조건, 충돌 시 최신 값 채택, 결과 미확인 처리를 유지한다. |
| `/workloads/[name]/infer` | 입력 선택 → 확인 → 결과. 실행 batch는 보내지 않는다. |
| `/scheduler` | 지금 판단. 현재 구성과 선택 후보의 예상 에너지와 이유. 예상 J와 SLO를 실측으로 표시하지 않는다. |
| `/scheduler/candidates`, `/scheduler/estimates` | 후보 목록과 서비스별 응답 목표 예상. 상세는 고른 시점의 응답을 유지하는 Dialog다. |
| `/scheduler/load` | 명시한 구간의 사건 시각 부하. 조건은 Dialog에서 고르고 원래 nano 구간을 URL에 보존한다. |
| `/transitions`, `/transitions/[id]` | 현재 변경 동작 목록과 동작 하나의 상세. 노드·시작 identity로 읽고 계획·before/actual·실패·부분 적용을 표시한다. 취소/재시도는 관측 정보다. |
| `/resources`, `/pools`, `/pools/[id]` | 장비 목록, 실행 그룹 목록, 그룹 하나. 장비 사양은 실행 적합성·가용 용량 보장이 아니다. |
| `/records`, `/settings` | 기록과 설정의 목차. |
| `/observations`, `/changes` | 고른 서비스의 요청 목록과 변경 목록. 서비스 선택은 목록에서 고르고, 조회 조건과 구간 통계, 개별 기록은 Dialog다. |
| `/scheduler/history`, `/changes?scope=scheduler`, `/transitions?view=history` | 판단·운영 설정·변경 동작 기록 목록. local 종류 필터, 원래 nano 구간과 append offset을 보존하고 A/B 선택은 비교 화면으로 이어진다. |
| `/scheduler/power` | 노드·계측 범위별 전력 기록. 전체 POOL 합계를 만들지 않는다. |
| `/scheduler/compare` | 두 판단의 조건 일치 여부와 비교. 실험 실행이나 인과 귀속이 아니다. |
| `/measurements`, `/measurements/check`, `/measurements/[kind]/[namespace]/[name]` | 측정 목록, 기존 계획의 읽기 전용 조건 확인, 측정 하나의 결과와 근거. |
| `/configuration` | 운영 설정. 행마다 값 하나를 Dialog에서 고치고, 모은 변경을 검토해 전체 SchedulerSettings를 한 번에 적용한다. 최신 값 재조회, 권한 오류와 결과 불명확 시 재조회를 유지한다. |
| `/experiments` | 실험 API 준비 상태와 판단 비교 링크. |

목록 및 상세의 참조는 kind, namespace, 이름과 UID 또는 원본 evidence identity를 보존한다.
Workload 관측과 과거 변경은 등록 UID를 사용한다. 상세 URL에 기대 UID를 보존해
같은 이름의 재등록을 별도 대상으로 표시한다. UID, 내부 코드, nano 값과 원본 JSON은 본문에
쓰지 않고 `원본 자료 보기` Dialog에만 둔다. 메뉴는 홈, 서비스, 운영, 기록, 설정 다섯 개다.
로그아웃 상태는 접속 폼만, 접속 뒤 신원 변경과 로그아웃은 `접속 관리` 모달만 사용한다.

화면의 색은 의미를 가진다. 서리색 바탕 위에 동작과 초점은 냉각수 파랑, 에너지와 주의는
열 주황으로 표시하며, 상태는 색만으로 구분하지 않고 아이콘과 문구를 함께 쓴다.

### 6.2 안내형 입력

- 모델: 실제 등록 모델의 설정 재사용과 파일·입력·실행 방식 선택을 제공한다. 최초 파일 보관 주소는 catalog 생산자가 없어 직접 입력이 필요하며 가짜 선택지를 만들지 않는다. credential은 기존 Secret 참조로 취급한다.
- 워크로드: 등록된 모델을 이름으로 선택하고 `0 < p ≤ 100`인 백분위 및 유한한 양의 목표 시간을 입력한다. 보통 시간은 숫자와 단위로, 원래 복합 Go duration은 그대로 작성하며 직접 Go parser를 만들지 않는다.
- 추론: 실제 지원 입력 선택과 입력 한 건에 집중한다. 근거가 없으면 제출을 비활성화하며 예시 생성으로 실제 지원을 만들어내지 않는다.
- 측정: 선언된 모델, 경로, DeviceClass, Node와 전력모드를 선택한다. 입력과 자원 및 스레드는 선택한 실행 설정과 연결하고 batch, warmup, 반복과 측정 및 관측 구간을 안내한다.
- 활성과 유휴는 기존 Run 및 campaign 조건을, 전환은 기존 전환 비용 측정 조건을 사용한다. 정책 선택의 실제 값을 제출하고 사용한 조건은 읽기 전용으로 표시한다.
- 고급 JSON은 제출 preview로 제공한다. 기본 폼은 사용자 업무 필드를 사용하고 Job, Pod와 내부 실행 설정은 CHILL이 구성한다.
- native label과 단위, 필수 항목 및 허용 범위를 표시한다. 서버의 field 오류를 해당 입력에 연결하고 거부 후 작성한 값을 유지한다.

### 6.3 확인, 충돌과 작업

변경은 `작성 → 대상과 전후 정의 및 확인 가능한 영향 → 제출 → 원본 상태 조회`로 진행한다.
삭제, 공유 모델 변경과 자원 제외에는 명시적 확인을 사용한다.
영향 정보는 API가 제공하는 평가 결과와 평가 가능 여부를 함께 표시한다.

Model과 Workload의 `If-Match`는 원본 metadata.resourceVersion을 사용한다.
다른 변경도 해당 API의 version을 전달한다. 409에서 최신 상태와 작성안을 함께 보여주고
명시적 채택 시 원본 비교 기준과 version을 함께 갱신하되 작성안은 유지한다. 재확인의 변경 전
정의는 채택한 실제 최신 상태다. 같은 이름의 다른 UID에는 채택·제출을 허용하지 않는다.

제출 중 버튼은 비활성화하고 mutation은 `retry: false`로 설정한다. 201 등록, 202 접수와 실제 완료를
서로 구분한다. accepted 작업과 원본 대상의 URL로 이동해 실제 operation을 조회한다.
새로고침 후에는 인증과 해당 원본 조회를 복원한다. 삭제는 원래 UID의 실제 제거를 확인한다.
응답 미수신은 결과 미확인으로 표시하고 조회 가능한 원본과 명시적 후속 행동을 제공한다.

## 7. 관측, 상태와 화면 안전

각 영역을 독립 Query로 조회하고 지원 여부, 권한 없음, 데이터 없음, 오래된 관측,
조회 실패와 동작 실패를 각각 표시한다. 실패한 영역에도 마지막 원본 관측 시각을 남기고
다른 영역의 결과를 유지한다. capability는 실제 지원을, health와 readiness는 해당 원본의
판정을 표시한다. `/readyz`의 journal health는 해당 저장소 준비로 표시한다.

[Query 기본값](https://tanstack.com/query/latest/docs/framework/react/guides/important-defaults)을 다음과 같이 설정한다.
조회와 mutation은 `retry: false`, focus와 reconnect refetch는 `false`로 설정한다.
갱신은 화면이 보이는 동안 설정된 주기를 사용한다. 401과 403 이후에는 해당 인증 또는
권한 문제를 해결한 뒤 조회한다.
권한이 거부된 조회의 payload는 숨기고 마지막 관측 시각과 오류를 표시한다.
Query key는 대상, namespace, UID, 기간과 필터로 구성한다.
[AbortSignal](https://tanstack.com/query/latest/docs/framework/react/guides/query-cancellation)을
fetch에 전달해 이동과 로그아웃 때 조회를 취소한다. 지원한 정렬과 pagination은 API에서 수행한다.

요청 관측은 완료, 오류, 거절, timeout, 취소, 미완료와 전달 실패 및 SLO 초과를 구분한다.
종단 지연과 백분위, 표본 수와 모집단은 전체 선택 구간의 API 집계값을 사용한다.
빈 구간은 표본 0과 값 없음으로 표시하고 raw page는 개별 기록 탐색에 사용한다.

전력과 에너지는 watts와 joules, Node와 POOL 범위, 기간 및 기준을 표시한다.
누락 값은 null, 그래프는 끊어진 구간으로 표시한다. 현재 상태, 예측과 실측은 별도 표기한다.
실험 비교는 원본이 제공한 같은 구간과 시간축을 사용하고 분석자가 해석한다.

브라우저 왕복은 fetch 시작부터 완전한 응답 본문 수신까지 `performance.now()`로 측정한다.
`X-Chill-Relay-Timing`은 CHILL 대기와 실행을 포함한 웹 중계 왕복이며 BFF 자체 처리 비용이 아니다.
CHILL 종단 시간은 journal의 실제 요청 기록을 사용한다. 각 경계는 그 경계의 같은 시계를 사용한다.

원본 시간 문자열과 입력값은 그대로 보존하고 표시에는 명시한 시간대와 `Intl`을 사용한다.
표에는 가로 스크롤을, 핵심 폼에는 좁은 화면의 세로 배치를 사용한다. Dialog의 focus,
키보드 이동과 복원을 유지하고 상태에는 텍스트, 제출 진행에는 `aria-live`를 제공한다.
외부 문자열은 안전한 텍스트로 표시하고 인증정보는 진단자료에서 제거한다.

## 8. 독립 배포와 웹 설정

[Next standalone 출력](https://nextjs.org/docs/app/api-reference/config/next-config-js/output)을
사용한 Node 컨테이너로 배포한다. `public`과 `.next/static`도 runtime image에 포함한다.
웹은 별도 Deployment와 ClusterIP Service를 가지며 공통 owner의 HTTPS gateway 또는
명시적으로 설정한 TLS Ingress로 공개한다. chart 기본 Service 유형은 ClusterIP다.
테스트용 NodePort는 해당 release의 `servingEntrypoint.service.type=NodePort`에서만 선택한다.

| 설정 | 주입과 사용 |
|---|---|
| CHILL API 주소와 CA | 서버 전용 `CHILL_BASE_URL`, `NODE_EXTRA_CA_CERTS`. native TLS 검증을 수행한다. |
| 웹 제한 | page 기본값과 최대값, 조회 구간, polling, 조회 및 변경 timeout과 body 상한을 배포 설정에서 주입한다. |
| 표시 설정 | CHILL 대상 설명, 기본 namespace와 표시 시간대. 안전한 값만 layout의 client props로 전달한다. |
| 사용자 인증 | 브라우저의 원본 bearer를 요청별 전달한다. 인증정보는 해당 사용자의 요청 범위에서 취급한다. |
| 저장소와 API credential | CHILL 생산자의 기존 Secret binding과 Pod에서 관리한다. 웹 Pod는 CHILL API 주소와 CA를 사용한다. |

웹 Pod는 `automountServiceAccountToken: false`로 배포한다. API 원본의 설정과 작업은 CHILL이 유지하므로
Dashboard 종료와 재시작 동안 Serving 및 관리 작업의 수명은 기존 계약을 따른다.
공통 HTTPS mount에는 Console의 조회 RBAC와 owner의 기존 Evidence credential 참조를 연결한다.
Console 조회 권한은 read-only와 필요한 이름 범위로 제한한다. 측정 생성 adapter에는
허용 namespace의 Run, campaign 및 전환 비용 측정 객체에 필요한 create 권한만 추가한다.
사용자 역할에는 새 HTTP 경로의 정확한 verb와 대상 권한을 추가하고 기존 CLI 및
Standalone Console 권한과 구분한다. 읽기 전용 preflight의 POST 권한은 해당 경로에 한정한다.

초기 검증 설정은 page 50개, 최대 200개, 기본 구간 1시간, 최대 24시간, 갱신 5초,
조회 timeout 10초, 변경 timeout 60초, 요청 및 응답 body 최대 8 MiB다. owner와 웹 설정의 한도를 맞춘다.
단일 추론 timeout은 사용자 값과 CHILL 상한을 사용하고 BFF의 전달 및 응답 여유를 별도로 설정한다.
raw 및 내보내기는 producer의 제한된 page 또는 stream을 사용한다. 설정 변경은 실제 입력 규모를
측정한 결과로 결정한다. 이 값은 웹 검증의 초기 설정이며 측정 및 제어 정책은 CHILL 설정을 사용한다.

## 9. SRS 추적성과 구현 순서

| 요구사항 | 구현 위치와 API | 수용 근거 |
|---|---|---|
| DB-SEC-01~04 | 4절의 bearer 전달, CHILL 권한 및 actor와 인증 장애 구분 | 권한과 만료 |
| DB-INT-01~02 | 2, 3, 8절의 고정 Git, 독립 모듈과 배포 | 독립 모듈과 템플릿 |
| DB-INT-03~06 | 4, 5, 7절의 원본 DTO, 도메인 writer, capability와 영역별 상태 | POOL과 유지보수, 데이터와 API 공백 |
| DB-UX-01~04 | 6절의 폼, 필드 오류, 목록, URL identity와 preview | 안내형 등록, 충돌과 연결 단절 |
| DB-FR-01~03 | System과 Inventory 및 개요, Workload 관측과 운영 POOL 전력 | POOL과 유지보수, 에너지와 실험 |
| DB-FR-04~07 | 기존 Model과 Workload 관리 API, 정의와 operation, 영향 및 참조 보호 | 안내형 등록, 근거 부족, 처리 중 수정과 삭제, 공유 모델 변경 |
| DB-FR-08~09 | 기존 infer와 exact 요청 관측, timeout 및 지연 경계 | 모델 공유와 추론 |
| DB-FR-10~11 | POOL 실제 projection, Inventory, allowed-resources와 maintenance | POOL과 유지보수 |
| DB-FR-12~14 | 측정 options, immutable create, 실제 status, profiles와 Passport 및 raw | 근거 부족, 측정과 실패 |
| DB-FR-15~18 | `/scheduler` 실제/유지/목표와 근거, `/configuration` 모든 Settings 및 제어요소 편집 | 현재 owner 연결 검증. 허용 자원 전체 편집, 정적 실험 구성과 영향 예측 생산자는 미제공 |
| DB-FR-19~21 | `/transitions` 계획·의존 동작과 before/actual, 상태·실패·partial, 판단 기록 링크 | 제공된 관측의 조회 검증. 수동 개입과 동작별 실측 에너지 귀속은 미제공 |
| DB-FR-22~23 | UID journal, 전체 구간 cohort와 분포, SLO 초과 및 archive 조회 | 빈 구간과 집계 |
| DB-FR-24 | `/scheduler/power` 개별 node/meter/window W/J, coverage와 누락 | 개별 기록 표시 검증. 전체/활성 POOL 및 증분 에너지 집계는 미제공 |
| DB-FR-25 | `/scheduler/compare` A/B 조건과 예측, 입력 구간의 실제 cohort 및 전력 링크 | 판단 기록 비교 연결. 실험 생성·실행·수명주기와 인과 비교는 미제공 |
| DB-FR-26~27 | 선택 기록/현재 페이지 export와 서비스·scheduler 설정 변경, 비밀 제거 및 검증 actor | 실제 설정 journal 조회와 원본 scope 보존. 영역 간 통합 feed는 미제공 |
| DB-OP-01~05 | 4, 6절의 영향 확인, If-Match, 원본 작업 조회, 제출 잠금과 명시적 재제출 | 처리 중 수정과 삭제, 공유 모델 변경, 충돌과 연결 단절 |
| DB-NFR-01~02 | 6, 7절의 native 입력, 접근성, 반응형, 시간 및 단위와 값 보존 | 안내형 등록, 접근성과 화면 안전 |
| DB-NFR-03~04 | 7, 8, 10절의 bounded 조회와 별도 중계 비용 및 성능 예산 | 조회 부하와 재시작 |
| DB-NFR-05~06 | 2, 4, 7, 8절의 독립 배포, secret 취급, 실제 데이터와 안전한 렌더링 | 독립 모듈과 템플릿, 접근성과 화면 안전 |

현재 제공 API에 대한 웹 구현은 다음과 같다.

1. 공식 고정 Git source와 MIT 원문을 보존한 독립 모듈, 전체 메뉴 및 route와 standalone image.
2. 원본 bearer 및 native TLS 중계, 검증 actor, Model/Workload CRUD, 지원 입력 기반 infer, POOL과 journal.
3. 인증된 System/Inventory/Run 목록, Passport, 봉인된 result와 read-only preflight.
4. native QueryClient 신원 교체, If-Match 충돌 draft 보존, 201/200/202/204 구분, GET 404 및 새 UID 구분.
5. 정책/측정 writer, selector, 시스템 전환, 운영 power와 실험은 실제 생산자 부재를 표시하고 비활성화한다.

검증은 연결된 API와 준비 화면을 대상으로 수행했다. 실제 추론 성공이나 미개발 생산자를 만들어내지 않는다.
전체 Dashboard 기능 완료는 52개 요구사항과 18개 수용 시나리오가 실제 API 흐름에서 충족되면 판정한다.

## 10. 검증 계획과 이번 단계의 증거

### 10.1 웹 모듈과 기능 수용 검증

현재 웹 개발은 실제로 연결한 기능의 브라우저 흐름과 준비 중인 영역의 화면 틀을 검증한다.
준비 중인 화면은 메뉴와 직접 링크, 기능 설명, 상태 표시와 비활성 동작을 확인한다.
이 상태의 데이터 및 제어 경로는 실제 API가 연결되면 활성화한다.
미개발 API의 기능 수용 시나리오는 `연결 준비 중`으로 기록한다.

[SRS 9절](../../spec/dashboard/SRS.md#9-수용-검증-시나리오)의 18개 시나리오는 API가 준비된 영역별로 실제 API와 브라우저에서 실행한다.
각 실행에는 template commit, lockfile, CHILL source, release와 namespace, 사용자 권한,
원본 UID, 선택 구간과 웹 설정 및 결과를 기록한다.

| 시나리오 묶음 | 필요한 실행과 관측 |
|---|---|
| 독립 모듈과 템플릿, 권한과 만료 | 빌드와 기동, 허용 및 거부 token, review 서비스 장애, 도메인 변경 기록의 실제 actor. |
| 안내형 등록, 근거 부족, 모델 공유와 추론 | 폼만으로 등록과 p99.5 보존, admission 전후, 다른 SLO 공유 및 실제 단일 결과와 Request ID. |
| 처리 중 수정과 삭제, 공유 모델 변경, POOL과 유지보수 | 처리 중인 요청과 드레인, 참조 거부, 원래 UID 삭제, 실제 Node 및 POOL 관계와 작업 완료. |
| 측정과 실패, 구성과 설정 변경, 전환 일부 실패 | 고정 사용 조건과 원시 trial, 실제 config version 및 판단 근거, 일부 실패와 남은 실제 상태. |
| 빈 구간과 집계, 에너지와 실험, 내보내기와 이력 | 표본 0, page와 무관한 모집단, 전체 Node 단일 집계, 같은 시간축, 구조화된 export와 secret 제거. |
| 충돌과 연결 단절, 데이터와 API 공백, 접근성과 화면 안전 | 실제 409, 결과 미확인과 명시적 재제출, 독립 영역 오류, 키보드 및 좁은 화면과 안전한 문자열. |
| 조회 부하와 재시작 | 아래 조건과 웹 중계 비용, 웹 종료 및 재기동 동안 CHILL 동작과 원본 작업의 진행. |

초기 성능 검증은 실제 snapshot의 Node 수, Workload 10개, 선택 구간 1시간의 요청 10,000건,
동시 사용자 5명과 갱신 5초를 사용한다. 실제 Node 수와 입력 규모를 실행 기록에 고정한다. 일반 조회 화면의 사용 가능한 표시 p95를 2초,
BFF 자체 처리의 p95를 100 ms 이하로 둔다. 상위 대기와 CHILL 실행시간을 별도 기록하고
API 오류 및 결과 누락을 함께 보고한다. 큰 raw 자료와 추론은 해당 API 시간 및 전송량으로 검증한다.

웹 변경은 `pnpm install --frozen-lockfile`, `pnpm lint`, `pnpm typecheck`, `pnpm build` 후
실제 기동과 브라우저 주요 흐름을 검증한다. Go 변경은 focused `go test`와 `make lint`,
`make test`를 수행하고 새 production package 또는 import는 `make arch-check`를 통과한다.
semantic 경로 변경은 pinned gopls와 해당 golden gate를, Helm 변경은 `make helm-lint helm-template`과
기본 및 Profiler render를 적용한다. 배포 topology 변경은 `make deployment-graph-check`도 수행한다.

### 10.2 구현 및 실제 배포 증거 (2026-10-07 UTC)

공식 source commit `e8c3143b1cd191280befcd6c9538284bb43399a8`와 frozen lockfile을 사용한다.
Go semantic evidence는 pinned gopls `v0.23.0` 및 반환 소스, 실제 dependency는 `go list`,
허용 방향은 `.go-arch-lint.yml`을 사용했다. Node 웹은 Go production package 범위 밖이다.

실제 대상은 `kubernetes-admin@kubernetes`, release `chill`, namespace `chill-system`이다.
06:46:03Z에 기존 상태를 캡처한 뒤 사용자 승인으로 구계약 Model 1개, campaign 1개 및 Run 6개를
삭제했다. compatibility reader를 만들지 않았으며 Evidence Store/S3와 Bound PVC는 보존했다.
최신 operator, profiler, Console, owner, Dashboard 및 ORT/RKNN/관측 worker image를 registry에
publish했다. hardware 접근과 metric/actuation/execution 정책은 기존 설정을 유지했다.
`fast-deploy --skip-deploy --push-mode registry-direct`로 build/push하고
`helm upgrade --reset-then-reuse-values --wait`로 기존 release를 rev 5까지 갱신했다.
uninstall이나 root 재생성은 수행하지 않았다.

07:32:09Z 캡처: CHILL 6개 Deployment는 각각 available 1, System Ready=True,
기존 System UID `17722831-d04d-4d8f-9e63-1bc13d5eef07`와 Evidence Store PVC UID
`e3d6c066-8f6b-4d83-a8a2-116c37ace771`가 유지됐다. 기존 node-discovery/node-actuator 및
별도 plan10-serving Deployment는 교체하지 않았다.
실제 ingress controller는 없어 HTTPS owner의 NodePort `https://155.230.35.213:31104/`로
Dashboard를 열었다. NodePort는 사용자 요청의 테스트 배포 값이며 chart 기본값은 변경하지 않았다.
개발용 자체 서명 인증서와 내부 CA Secret을 사용한다. 브라우저 검증 도구만 개발 인증서를
허용했으며 BFF의 실제 owner CA/hostname 검증은 계속 활성화했다.

| 실제 cluster에서 실행한 경계 | 관측 |
|---|---|
| 인증 및 신원 변경 | operator/reader GET 200, reader DELETE 403, 미권한 GET 403, 누락/무효/폐기 token 401. 신원 변경 후 draft 초기화, 403 후 이전 UID 및 폼 숨김, storage/cookie에 token 없음. |
| 안내형 등록 및 공유 | 실제 불변 MobileNet artifact로 Model 201, Workload A 201의 p99.5/137.5ms 보존, 공유 Workload B의 p95.25/220ms. 참조 Model DELETE 409 ModelReferenced. |
| 동시 수정 | 실제 annotation으로 resourceVersion 변경. PUT 409 뒤 97.25/211.75ms draft 유지, 명시적 최신 버전 채택 및 재제출 200, generation 2 적용과 검증된 SA username/UID 기록. |
| 추론과 관측 | 유효한 batch-free FP32 입력에 native 503 MissingEvidence, Request ID `a992d39ec7d8c140c53fa4c862ce677e` 및 원래 Workload UID 보존. UI 제출 비활성. 같은 구간 limit 1/50의 전체 cohort 및 모집단 동일, 무표본 quantile=null. |
| 삭제와 등록 identity | Workload DELETE 202 후 실제 GET 404, 동일 이름 재등록의 새 UID와 이전 URL 변경 잠금. 참조 없는 Model DELETE 204 후 실제 GET 404. |
| export/화면 | 브라우저가 받은 source JSON 파일의 UID 보존 및 bearer 없음. 데스크톱 1440/1440, 모바일 390/390 document width, native sidebar/Escape 및 skip link의 main-content focus. 외부 Origin POST 403. |

별도 `kind-chill-dashboard-e2e`는 격리 검증이다. 그 환경에서 review permission 제거의
503 AuthorizationUnavailable과 복원 후 200도 확인했으며 실제 testbed의 review 권한을
중단하지 않았다. kind의 결과를 실제 cluster 실행으로 합치지 않는다.

실행된 gate: manifests/generate, focused Go tests, `make lint test arch-check gopls-check`,
`make helm-lint helm-template deployment-graph-check`, frozen pnpm install, lint/typecheck,
native Node tests 3개와 build. 최종 실제 values render는 70 resources, valid 58, skipped
CRD/ChillSystem 12, invalid/errors 0; 기본 및 Profiler render도 별도로 검증했다.

미실행/미제공: 실제 qualifying native campaign 이후 Ready 추론 성공, ARM/RKNN hardware
측정, 처리 중 요청의 드레인, 운영 전력/selector/전환/writer 생산자, 전체 18개 시나리오 및
10,000건·동시 5명 성능 예산. 중계 왕복 값은 BFF 자체 처리 p95 100ms 증거가 아니다.
따라서 이 기록은 승인된 현재 core의 구현 및 실제 배포 증거이며 전체 SRS 완료 판정은 아니다.

### 10.3 사용자 중심 UI 개편과 실제 재검증

최종 image는 `dashboard-live-20261007-ux-ready`이며 실제 release는 rev 8이다.
08:33:51Z 캡처에서 6개 CHILL Deployment가 각각 available 1이며, 기존 System 및
Evidence Store PVC UID가 유지됐다. owner NodePort 31104와 private Dashboard ClusterIP도 유지됐다.
실제 Dashboard image ID는 `sha256:c1990cfcedeea3ffeb57047bd8e4eccb37f3a5f121f70bbaad4db14d4367687d`다.

| 새 UI에서 실제 실행한 흐름 | 관측 |
|---|---|
| 목록·안내형 등록·의존 모달 | 실제 MobileNet 설정을 선택해 임시 모델 등록. 실제 모델 이름 선택과 서비스 등록에서 p98.125/123.75ms 보존. 상세에는 편집 입력이 없고 수정은 별도 route. |
| 동시 수정과 전후 확인 | 실제 spec을 다른 writer로 변경해 409 재현. 처음에는 채택 후 변경 전 표시가 오래된 값인 문제를 관측했다. 수정 후 실제 최신 p99.75/200ms와 유지된 p96.5/1m30.5s 작성안을 같은 확인 모달에서 관측하고 저장했다. |
| 원본과 삭제 | 브라우저 source 파일의 원래 UID와 복합 duration 보존, bearer 없음. 확인 모달로 임시 서비스와 모델을 삭제한 뒤 각각 native GET 404 확인. 검증용 SA도 제거. |
| 조회 구조 | 장비 8종, 실제 빈 실행 그룹 및 Run 목록을 각각 독립 페이지로 조회. preflight는 독립 읽기 전용 route. 설정·전환·실험은 `연결 준비 중`, 입력 0개 및 disabled 동작. |
| 관측과 이력 | 실제 서비스 이름으로 1시간 조회, 15분/24시간 선택지 확인. 삭제된 UID, 원래 nanosecond `from`/`to` 보존 및 같은 기간의 이력 이동. 무표본은 값 없음, 이력에는 변경 기록만 표시. |
| 인증과 화면 | 실제 미권한 SA 403 및 무효 token 401에서 이전 payload와 입력 숨김, 신원 복구 200. 로그아웃 뒤 접속 폼 1개, storage/cookie 비어 있음. 최종 desktop 1440/1440, mobile 390/390, 메뉴 자동 닫힘과 skip-link main focus. |
| 실패 응답 경계 | 유효한 batch-free FP32 입력에 native 503 MissingEvidence, Request ID `c3acab9abf3ebe38a4d8d2f6aad71d5d`, 기존 Workload UID와 relay 202.9ms 보존. 이는 Ready 추론 성공 또는 BFF 자체 비용 증거가 아니다. |

최종 소스에서 lint/typecheck, native Node 테스트 3개, Node 24.21.0·pnpm 10.33.4 image build,
실제 values의 Helm lint/render를 통과했다(70 resources, valid 58, skipped 12, invalid/errors 0).
기존 native HTTPS load-generator caller도 실제 CA로 owner에 요청해 503 MissingEvidence와
동일 Workload UID 및 Request ID를 관측했으며 경계 테스트 6개를 통과했다.
호스트 기본 Node는 strip-types를 지원하지 않아 테스트 명령이 실패했으며 지원 Node 24로 실행 완료했다.
Unused 카드·탭·선택 위젯은 삭제하고 native 선택 및 기존 Dialog를 사용했다.
브라우저 회귀는 실제 testbed에서 수행했으며 별도 영구 browser-test framework를 추가하지 않았다.
10.2의 미실행 범위와 전체 SRS 완료 제한은 그대로다.

종합 커밋 범위는 웹만이 아니라 선행 native Workload owner, typed 입력 계약과 관련
profiling/worker/HTTPS caller 및 생성 CRD·chart·sample·계약 테스트를 함께 포함한다.
선별 index만 별도 디렉터리에 checkout해 `go test ./... -run '^$'`의 전체 패키지 compile,
owner/serving/Console/modelcatalog/modelcompat focused tests와 실제 owner CLI `--help`를 통과했다.
기존 사용자 문서 변경인 ADR 0006, operator-console 설계 6개 및 plan 10의 링크 수정은
이 구현 커밋에 포함하지 않고 worktree에 보존한다.

### 10.4 스케줄러 연동 검증 (2026-10-13 UTC)

격리된 `kind-chill`의 `scheduler-dashboard-smoke` owner와 실제 Next 화면에서
Pending/제어요소 off, 정밀 seed·메모리, 설정 PUT의 실제 ConfigMap 및 intent/result
journal을 확인했다. 중복 제출 1건, ConfigMap update 권한 403의 draft 유지,
실제 PUT 이후 응답 단절의 결과 재조회와 동시 변경의 최신 baseline 검토를 관측했다.
서비스 이력 기본 동작을 유지하고 설정 사건의 검증 actor와 5→6 변경을 실제 화면에서 읽었다.
빈 filtered page의 nextCursor와 입력 중 draft/원래 nano 구간 분리, 직접 기록 복원,
로그인 bookmark 유지 및 신원 변경의 선택 초기화를 확인했다.

실제 owner에 없는 동작 상태와 전력 기록은 저장소에 남기지 않는 브라우저 interception으로
별도 확인했다: 계획/실행/완료/실패/취소, partial actual, 같은 ID의 시작 identity 거절,
null/0 J, 0 samples, coverage 실패, 단일 W chart, A/B 정밀 수치 및 whole-window 무표본 cohort.
이를 실제 물리 전환이나 계측 실험의 증거로 사용하지 않는다.
Ponytail 중간 리뷰에서 중복 원본 renderer, 순서 index identity, 범용 deep-encoding 비교와
단일 호출 wrapper를 제거했다. 입력 경계, 정밀 정수, 권한, 결과 미확인과 접근성 검사는 유지했다.
전체 SRS 완료 제한과 미제공 생산자 범위는 유지한다.

Node 24.21.0/pnpm 10.33.4의 `make dashboard-check`는 사용자 worktree와 커밋된
격리 배포 소스에서 각각 통과했다(회귀 검사 10개, lint/typecheck/build).
실제 standalone 서버의 새 읽기 route와 A/B 선택을 390px에서 열었고 document 폭은
390px였다. 설정 검토는 키보드 제출/Escape 후 저장 검토 버튼으로 focus를 복귀한다.
정밀 JSON 미지원 환경은 편집을 제공하지 않으며 실제 미권한 사용자 403은 두 polling
주기 동안 요청 1건과 payload 없음으로 확인했다.
기본/Profiler Helm 렌더는 29/42개 자원, invalid/errors 0이며 graph delta는 13개다.
graph의 C 정렬 입력에는 `LC_ALL=C`를 사용했다. 격리 소스 검사에서 발견한 scheduler
GET grant의 문서 경계 오류는 manager rules 안으로 이동해 수정했다.
배포 전 server-resolved 렌더의 변경은 Dashboard image, 기존 manager GET와 unbound
scheduler editor ClusterRole/Role뿐이며 lookup으로 정해지는 기존 API server 설정은 유지한다.


### 10.5 스케줄러 Dashboard 실제 배포와 조회 경계

`fast-deploy --components dashboard --platforms linux/amd64 --push-mode registry-direct
--skip-deploy`로 격리된 커밋 소스의 Dashboard만 publish했다. 기존 rev 9의 chart와
values를 캡처하고 필요한 RBAC만 반영한 `helm-release-flow.sh install` upgrade로
`chill/chill-system` rev 10을 배포했다. uninstall과 root 재생성은 수행하지 않았다.
실제 Dashboard image는 `scheduler-dashboard-8c7cbec`, image ID는
`sha256:b408d4913814e8836378b07c1743fb8657bcc211c5cad48e139f3ab08ad09641`이며 ready replica는 1이다.
System UID `17722831-d04d-4d8f-9e63-1bc13d5eef07` 및 spec, `serving-scheduler` UID/data,
기존 ConfigMap, 다른 Deployment template, PVC/Service UID와 Profiler 값을 보존했다.
Helm values의 변경은 Dashboard image뿐이며 새 scheduler editor grants는 unbound다.

기존 `chill-dashboard-user`의 단기 token으로 HTTPS NodePort 31104에서 GET만 확인했다.
scheduler/settings/state는 no-store 200, 자동 적용 off, Pending, 동작 0개였다.
현재 판단은 owner가 보관한 `2026-10-10T13:35:30.019186337Z`이며 새 물리 관측으로 해석하지 않는다.
후보, 현재 변경 진행과 운영 설정을 실제 값으로 열고 실험/비교의 생산자 및 선택 경계도 확인했다.
load와 scheduler journal은 BFF 504로, 관련 화면은 오류를 표시하고 빈 성공 표를 만들지 않았다.
명시적 1분 구간/limit=1의 직접 owner journal GET도 BFF 없이 30초 동안 응답하지 않았고
owner 로그에는 S3 PutObject deadline 초과가 관측됐다. live 과거 기록/A/B 및 전력 데이터의
정상 조회는 이 owner 장애로 검증하지 못했다. timeout 확대나 다른 컴포넌트 변경은 하지 않았다.
실클러스터의 설정 PUT, 제어 활성화, 인위 전환과 전력 실험은 수행하지 않았다.
로컬 실제 ConfigMap/journal 및 별도 UI fixture의 증거를 live 제어 증거와 구분한다.
