# CHILL Dashboard 구현 설계

## 1. 구현 경계와 결정

[Dashboard SRS](../../spec/dashboard/SRS.md)의 56개 요구사항을 구현 기준으로 사용한다.
제어와 측정 의미는 [시스템 명세](../../spec/README.md)를 따른다.
이 문서는 웹 구현 구조, ChillServer 조회의 재사용과 ChillServer가 추가로 제공할 조회, 전체 수용 검증을 정한다.

- 템플릿은 공식 `shadcn-ui/ui`의 `templates/next-app`과 `dashboard-01` 블록을 사용한다.
- 웹 모듈은 저장소 루트의 `chill-dashboard/`에 두고 독립적으로 빌드하고 배포한다.
- Next.js App Router, React, TypeScript, Tailwind CSS와 shadcn/ui를 사용한다.
- 화면은 컴포넌트 조합으로 구성한다. 서버는 Next.js Route Handler로 고정 ChillServer API를 중계한다.
- 인증, 검증, 변경 선언, 작업 진행, 판단, 통계와 Evidence는 ChillServer와 그 뒤의 CHILL 구성요소가 결정한다. Dashboard는 그 결과를 표시한다.
- Dashboard는 모든 워크로드와 모델에 같은 화면 규칙을 적용한다. 입력과 결과 화면은 전처리기 계약의 필드 이름, 형식과 크기로 구성한다.
- 자동 제어는 꺼진 상태를 유지한다. 프로파일의 `loaded`는 운영 판단 반영으로 표시한다.
- 전체 기능의 완료 기준은 SRS와 27개 수용 시나리오다.

### 1.1 현재 개발 범위

| ChillServer 준비 상태 | 웹 개발 범위 |
|---|---|
| 관리 API 제공 | 모델, 전처리기, 데이터셋과 워크로드 관리, 단일 추론, POOL의 제공 필드와 요청 관측을 실제 응답에 연결한다. |
| 인증된 Console 조회 제공 | System, Inventory, bounded Run 목록, 검증된 Evidence Passport, 봉인된 campaign result와 읽기 전용 preflight를 실제 응답에 연결한다. |
| 스케줄러 조회 제공 | 운영 판단, 실제 구성, 유지안과 후보의 예상 SLO 및 J, 명시적 구간의 부하와 채택 프로파일을 독립 화면에 연결한다. |
| 설정 조회와 적용 제공 | 모든 SchedulerSettings 필드를 현재 값에서 편집하고 변경 전후, 행위자와 Runtime 적용 결과를 확인한다. |
| 스케줄러 기록과 운영 전력 제공 | 현재와 과거 동작, 판단 A/B, 설정 변경과 개별 노드 전력 기록, `GET /observations/power`의 전체 및 활성 POOL 에너지를 연결한다. |
| 측정 요청 제공 | 측정 목록, 단계형 측정 요청, 프로파일 채택과 해제, 측정 삭제를 연결한다. |
| ChillServer 조회 대기 | 측정 결과의 실제 backend와 실행 장치, DeviceClass별 측정 가능 여부, 전환 비용 원본은 5.3절의 셸 설계로 둔다. 허용 자원과 유지보수, 실험 실행, 영역 간 변경 feed, 동작별 에너지 귀속과 수동 취소 및 재시도는 5.4절의 계약으로 둔다. |

연결 준비 영역은 기능 설명과 `연결 준비 중` 상태를 표시한다. 실제 데이터 표와 그래프는
API 응답으로 채우고, 제출 및 제어 버튼은 해당 API와 권한 검사가 연결되면 활성화한다.
화면 틀의 개발 상태와 CHILL의 실제 운영 상태를 구분한다.
전체 SRS의 기능 수용은 각 ChillServer 조회를 연결한 뒤 실제 흐름으로 검증한다.

운영 판단은 `/scheduler`, 후보 비교는 `/scheduler/candidates`, 실제 부하는
`/scheduler/load`에서 조회한다. 상세는 열 때의 응답을 유지하며 새 관측 시각을
안내한다. load 응답의 arrivals는 ChillServer가 생략하므로 UID와 원래 구간으로 전체
적용 정의의 요청 관측에 연결한다. 후보가 제공한 정의 identity는 그대로 전달한다.
요청 상세는 CHILL 배정과 nativeProgress의 실제 실행 관측을 구분한다.
`/configuration`은 duration 네 필드, Serving 메모리, 반복 비교와 탐색 상한,
seed 및 제어요소 boolean 아홉 필드를 편집한다. 정밀 정수는 문자열로 유지하며
ms를 정확한 ns로 변환한다. 저장 검토는 현재 설정을 재조회하고 입력을 유지한 채
최신 값과 비교한다. 전체 객체의 마지막 적용값이 현재 설정이 되며 버전 조건과
영향 예측은 ChillServer 조회 대기다. appliedAt은 설정 저장과 Runtime 적용 시각이다.
응답이 불명확하면 현재 값과 제한된 최근 기록을 조회한 뒤 명시적으로 다시 제출한다.
`/scheduler/history`의 종류 필터와 `/scheduler/power`의 노드 필터는 현재 가져온
페이지에 적용한다. 제출된 원래 from/to를 cursor 이동에 복사하고 입력 draft는
별도로 유지한다. 선택 기록은 같은 구간의 append offset에 limit=1로 복원하며
종류, at, 대상과 action의 노드 및 시작 시각이 모두 같은 사건만 같은 기록으로 복원한다.
A/B 비교는 각 판단의 입력 구간과 미래 horizon을 구분하고 UID와 고정 정의별
ChillServer whole-window cohort를 조회한다. 백분위, 총에너지와 실험 점수는 ChillServer 값을 그대로 표시한다.
동작은 제공된 단계와 before/actual을 표시하며 같은 ID라도 시작이 다르면 별개의 동작으로 표시한다.
전력 상세는 단일 기록 모달의 W 시계열과 meter, scope, source, coverage와 실패를 표시한다.
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
인증은 기존 Kubernetes identity를, 업무 동작은 ChillServer 공통 API를 사용한다.

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
자체 정적 자산으로 제공하므로 브라우저는 같은 origin에서 글꼴을 받는다.

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
| 표 | 원본 블록의 TanStack Table v9 | 행 링크, 정렬과 API pagination 상태를 연결한다. 복사한 v9 API를 기준으로 한다. |
| 그래프 | 원본 블록의 Recharts 3.8.0 | API가 제공한 분포와 시계열을 그린다. 관측 누락은 null과 공백으로 표시한다. |
| 서버 상태 | [TanStack Query 5.104.1](https://registry.npmjs.org/@tanstack%2Freact-query/latest), native `fetch` | cache, 제한된 polling, 요청 취소와 변경 중 상태를 사용한다. |
| 폼과 로컬 상태 | native HTML, React `useState`, URL search parameters | 업무 입력과 서버 필드 오류를 연결한다. 도메인 검증은 ChillServer에서 수행한다. |
| 날짜와 시간 | `Intl.DateTimeFormat`, `Date`, 원본 RFC3339Nano 문자열 | 표시와 조회 구간 변환을 수행하고 원본 정밀도를 보존한다. |

의존성은 `next-app`과 복사한 UI의 실제 import를 기준으로 설치한다.
표는 API의 구체적인 TypeScript 타입을 사용하고, 목록의 각 행은 상세와 작업으로 바로 이동한다.

## 3. 모듈과 실행 구조

현재 웹 모듈의 실행 구조다. 같은 CHILL Helm release의 별도 Deployment로 배포한다.

```text
브라우저: 화면, 폼, 메모리 인증정보, Query cache
    → HTTPS chill-dashboard: Next.js 화면과 /api/chill Route Handler
    → HTTPS ChillServer 공통 관리 진입점
        → Serving의 Model, 전처리기, 데이터셋과 Workload 관리, 요청 관측 기록, 스케줄러와 측정 요청
        → Console query 및 검증된 Evidence 조회
        → 자원 관리, 설정과 전환의 해당 CHILL 구성요소
```

현재 모듈 배치는 다음과 같다.

```text
chill-dashboard/
  app/                     화면 route, layout와 api/chill/[...path]/route.ts
  components/ui/           선택한 공식 UI 코드
  features/                기능별 화면
  lib/                     구체적인 API 타입, fetch, 코드 문장 표와 표시 함수
  public/                  자체 정적 자산
  package.json             독립 dev, build, start, lint, typecheck
  pnpm-lock.yaml           검증한 의존성 고정
```

App Router의 layout은 shell과 안전한 웹 설정을 전달한다. 인증된 데이터는 client 화면에서
같은 origin의 BFF를 호출해 조회한다. 서버 데이터는 Query cache에, 작성 중 입력은 폼에,
대상과 필터 및 구간은 URL에 둔다.

공통 HTTPS ChillServer는 `cmd/chill-server`에서 조립한다. 기존 Serving handler를 유지하고
Console 조회는 요청별 사용자 인증 및 SAR 후 고정 private Console origin으로 중계한다.
Console의 기존 Evidence Store binding, retained loader 및 production preflight resolver를 재사용한다.
`--dashboard-url`은 API와 health 경로 외의 화면, 정적 자산과 BFF를 private Dashboard로
중계한다. 공개 HTTPS ChillServer → Dashboard BFF → HTTPS ChillServer `/api/v1`의 경로는 서로 분리된다.
Standalone Console과 CLI는 기존 query 및 검증 계약을 유지한다. ingress controller가
없는 클러스터에서도 ChillServer의 HTTPS NodePort로 Dashboard에 접근할 수 있다.

패키지 허용 범위는 [아키텍처 규칙](../../../.go-arch-lint.yml)을 따른다.
Serving과 Console의 handler 연결은 `cmd/**`의 조립 권한을 사용한다.
웹은 ChillServer API를 호출한다. Kubernetes, firstfloor, S3와 Node 동작은 ChillServer 뒤의 CHILL 구성요소가 수행한다.

## 4. 인증과 API 중계

### 4.1 기존 신원 전달

기존 Kubernetes bearer token을 인증 화면에서 입력받아 탭의 메모리에 둔다.
토큰 발급과 갱신은 클러스터의 기존 인증 절차를 사용한다.
브라우저는 `Authorization: Bearer`로 같은 origin의 BFF를 호출하고 BFF는 원본 토큰을
ChillServer에 전달한다. ChillServer가 TokenReview와 대상 및 HTTP 동작의 SubjectAccessReview를 수행한다.

새로고침하면 토큰을 다시 입력한다. 대상과 작업 URL은 유지해 인증 후 실제 상태를 조회한다.
로그아웃과 토큰 변경 때 진행 조회와 변경을 취소하고 cache를 비운 뒤 새 native QueryClient로 교체하며 민감한 폼을 remount한다. 이전 신원의 늦은 continuation은 이전 cache에서 끝난다.
토큰의 취급 범위는 탭 메모리와 TLS 요청의 Authorization header다.

현재 [Authorizer](../../../internal/serving/kube/authorizer.go)는 TokenReview로 검증한
username과 UID의 private `Principal`을 반환하고 SAR를 수행한다. handler와 manager는 그
검증된 신원만 변경 작업과 관리 관측 기록에 남긴다.

- 인증 누락, 거부 또는 무효 token은 401, 실제 SAR 거부는 403이다.
- review 통신 실패, API 권한 오류와 SAR evaluation 오류는 503이며 변경은 거부된다.
- SAR의 non-resource path는 외부 HTTP path에 `/chill`을 붙인다. 이 접두사로 Kubernetes 기본
  `system:discovery`의 `/api/*` 읽기 권한과 CHILL 자료 권한을 분리한다.

Console reader의 Kubernetes client는 서버 identity로 동작한다. 따라서 공통 진입점의
정확한 verb와 path SAR가 실제 사용자 권한 경계다. namespace와 대상이 포함된 경로 및
내보내기 동작에 권한을 부여하고, ClusterRole binding은 클러스터 관리자가 관리한다.
Console private leg는 서버 identity만 사용한다. 공개 API mount는 정확한 인증 경계를 거친다.

### 4.2 얇은 BFF 계약

- 중계 대상은 서버 전용 고정 `CHILL_BASE_URL` HTTPS origin 하나와 `NODE_EXTRA_CA_CERTS`로 정한다. 코드와 설정에서 이 origin을 ChillServer로 부른다.
- 허용한 method와 path를 `/api/chill/v1/...`에서 `/api/v1/...`로 연결한다. native Node HTTPS가 CA와 hostname을 검증하고 redirect는 받은 상태 그대로 반환한다.
- `Authorization`, `If-Match`, `Content-Type`, 추론 timeout과 필요한 CHILL 식별자 header만 전달한다.
- HTTP 상태, 완전한 bounded 응답 본문, 요청 ID와 Workload UID를 보존한다. 요청과 응답은 각각 8 MiB, 응답 header는 16 KiB로 제한하며 완료된 본문만 성공으로 반환한다.
- 인증된 fetch와 응답에 `no-store`를 적용하고 사용자별 데이터는 해당 탭의 Query cache에서 관리한다.
- 조회, 변경과 추론의 timeout 및 body 한도를 웹 설정으로 제한한다. 추론 timeout은 ChillServer 허용 범위와 사용자가 선택한 값을 따른다.
- 변경, 측정과 추론의 응답 미수신은 결과 미확인으로 표시한다. BFF 연결 오류와 timeout은 별도 Dashboard 오류 코드로 표시한다.
- API의 202는 접수로 표시한다. 재제출은 원본 상태 조회와 사용자 확인을 거쳐 수행한다.

변경 재전송은 사용자 확인 후 수행한다. 오류의 API code, 대상, field와 이유를 보존한다.
외부 문자열은 React의 텍스트 렌더링을 사용한다. 원본 JSON도 텍스트로 표시하고
자료 내보내기는 복사본에서 credential 필드를 제거하며 원본 객체는 그대로 둔다.

스케줄러의 int64 seed, uint64 메모리와 duration은 native JSON reviver의 원문과
JSON.rawJSON으로 정확하게 유지한다. 지원 브라우저에서만 설정을 저장한다.
원본 보기와 내보내기는 같은 직렬화를 사용하고 기존 credential 제거를 유지한다.
조회는 serving-manager의 정확한 세 scheduler GET 경로를 사용한다.
설정 PUT은 unbound scheduler-editor ClusterRole과 serving namespace의 같은 이름
Role을 관리자가 선택한 계정에 각각 binding한다. Role은 설정된 ConfigMap 하나의
update만 허용한다. 사용자 변경 권한은 관리자가 binding한 계정에만 부여된다.


## 5. ChillServer 조회와 추가 계약

### 5.1 현재 재사용 가능한 표면

아래 준비 상태의 근거는 소스의 HTTP 등록과 조회 구현이다.
Serving 경로는 [현재 handler](../../../internal/serving/httpapi/handler.go),
Console 경로는 [현재 read handler](../../../internal/operatorconsole/readapi/handler.go)를 기준으로 한다.
relay 허용 목록은 `chill-dashboard/lib/relay.ts:47-104`다.

| method와 `/api/v1` 하위 경로 | 현재 제공 범위 | 웹 연결과 필요한 보완 |
|---|---|---|
| `GET, POST /models`, `GET, PUT, DELETE /models/{id}` | ModelSpec 조회와 변경, 버전 전제조건 및 참조 보호. 실행 경로는 `firstfloor`와 산출물 형식별 backend만 통과한다(`internal/modelcatalog/catalog.go:107-123`). | 등록 폼과 영향 확인에 재사용한다. 실행 방식 선택지는 같은 규칙을 따른다(U2). |
| `GET, POST /preprocessors`, `GET, PUT, DELETE /preprocessors/{id}`, 같은 형태의 `/datasets` | 전처리기의 `input.fields`와 `output.fields` 계약, 데이터셋 필드, 참조 보호 | 단계형 등록과 계약 결론 문장, 입력과 결과 화면의 필드 원본이다. |
| `GET, POST /workloads`, `GET, PUT, DELETE /workloads/{id}` | desired, appliedDefinition, supportedInputs, admission, readiness와 사유, operation | 정의, 실제 준비와 접수 및 작업을 구분한다. 준비 사유는 6.4절의 문장으로 표시한다. |
| `POST /workloads/{id}/infer` | 단일 원시 표본 입력(Preprocessor 입력 필드의 base64 `BYTES`), 실제 응답과 요청 identity | 단일 추론에 재사용한다. |
| `GET /pools`, `GET /pools/{id}` | 실제 Node와 Workload identity, 인스턴스, 준비 및 대기와 실행 수 | 자원, 모델 경로와 전력모드 및 활성 범위의 실제 관측을 추가한다. |
| `GET /observations/workloads/{uid}` | 구간, 정의 및 outcome 필터, cursor, 전체 선택 구간 cohort와 raw 이벤트 | SLO 초과, 분포, 집계 설정과 archive 조회 계약을 보완한다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence-passport` | 검증된 Run 계보와 제한된 Evidence 요약. `measurement.runtime`은 family, version, provider, architecture를 담는다(`internal/operatorconsole/evidence_passport_view.go:66-83`). | 공통 HTTPS 경계에 인증을 적용해 재사용한다. |
| `GET /namespaces/{namespace}/batch-energy-optimizations/{name}/result` | 캠페인의 봉인된 결과 | 진행 상세와 구분해 재사용한다. |
| `POST /namespaces/{namespace}/batch-energy-optimizations/preflight` | 입력을 해석하고 `resolves`, `reason`, `plan`을 반환하는 읽기 전용 요청 | 측정 요청의 검토 단계와 `/measurements/check`에 사용한다. 캠페인 접수는 별도 POST로 수행한다. |
| `GET, POST /namespaces/{namespace}/batch-energy-optimizations`, `DELETE .../{name}` | 측정 목록, `dryRun=All` 검증과 접수, `uid`와 `If-Match`의 영구 삭제 | 측정 목록, 요청과 삭제에 재사용한다. |
| `GET /scheduler`, `GET /scheduler/settings`, `GET /scheduler/state`, `PUT /scheduler/settings` | 현재 설정, 실제 snapshot과 판단, 동작과 결과, 전체 설정 적용 및 행위자 관측 기록 | 고정 BFF 경로에 연결한다. 버전 조건과 dry-run 계약은 ChillServer 조회 대기다. |
| `GET, POST /scheduler/profiles`, `DELETE /scheduler/profiles/{namespace}/{name}` | 채택 프로파일과 조건, 채택 결과와 `configVersion` | 측정 화면의 채택과 해제, `/scheduler/profiles` 목록에 재사용한다. |
| `GET /observations/load` | 명시적 구간의 수신, 접수, 배정과 완료 사건 수 및 사건률 | from/to를 필수로 전달한다. 완료는 수신 cohort와 별개의 사건 집계다. |
| `GET /observations/scheduler` | 판단, snapshot, 설정 변경, action과 전력 관측의 보관 중인 append-order 페이지 | from/to, bounded limit와 cursor를 사용한다. 응답은 페이지 항목과 nextCursor로 구성된다. relay는 이 경로와 `GET /scheduler`, `/scheduler/state`, `/scheduler/profiles`를 ChillServer `view=compact`로 읽고, 화면 파싱 전에 공유된 `profile`과 `completionMeasurement` `$ref`를 응답 dictionary로 복원해 full 응답과 같은 값을 쓴다. 응답 상한은 그대로다. |
| `GET /observations/power` | 명시적 구간의 전체 및 활성 POOL joules와 평균 W, 노드별 값, `complete`, `reasons`, 로컬 보존보다 앞선 구간의 `gapUntil`(`internal/serving/httpapi/power.go:10-32`) | `/scheduler/power`의 실행 그룹 전체 전력에 연결했다(`chill-dashboard/features/scheduler.tsx:251-267`). |
| `GET /systems`, `GET /inventory` | ChillSystem의 condition과 이유, DeviceClass와 typed 전력모드 actuator, Node | System, Inventory와 Run 목록은 아래 query handler에서 제공한다. |

System, Inventory와 Run 목록은 기존
[System query](../../../internal/operatorconsole/kube/system_query.go),
[Inventory query](../../../internal/operatorconsole/kube/inventory_query.go),
[Run query](../../../internal/operatorconsole/kube/profiling_runs_query.go)를 재사용하는
[HTTP query handler](../../../internal/operatorconsole/readapi/queries.go)에서 제공한다.
`limit` 1..200, `offset` 0..1,000,000과 bounded `q`를 검증한다.
Run의 `condition=Type=True|False|Unknown`은 원본 condition 필터다.
Inventory는 각 원본의 resourceVersion과 개별 조회 시각을 유지하는 조회 projection이다.

### 5.2 SRS 변경분의 조회 현황

SRS `25f7edd`의 변경 항목이 쓰는 조회다. 모두 ChillServer에 이미 있으며, 소비 상태와 반영 단위(9.2절)를 표시한다.

| 조회 항목 | ChillServer 제공 위치 | 현재 Dashboard 소비 | 반영 |
|---|---|---|---|
| `decision.policy` | `GET /scheduler`, `/scheduler/state`, `/observations/scheduler`의 decision: 작업 이력, 후보 key, 판단 구간, 전환 kernel(`internal/serving/scheduler.go:234-249`, `internal/serving/scheduler_policy.go:17-28`) | 소비 전. 판단 원본 보기에 그대로 포함된다(`lib/read-types.ts:49`). | SRS 변경분에 해당 요구가 없으므로 원본 보기로 유지한다. |
| typed actuator | `GET /inventory`의 `classes[].powerModes[].actuator`: `kind`가 `Fixed`, `JetsonNVPModel`(`jetsonNVPModel.id`), `NvidiaLockedGPUClock`(`nvidiaLockedGPUClock.minMHz`, `maxMHz`, `mode`)(`api/v1alpha1/deviceclass_types.go:34-94`, `internal/operatorconsole/inventory_view.go:26-52`) | 소비 중, 타입은 `unknown`이고 원본 JSON으로 표시(`lib/read-types.ts:6`, `features/resources.tsx:24-39`). 측정 요청은 모드 이름만 표시(`features/measurement-request.tsx:107,312-313`). | U0 타입, U3 화면 |
| profile condition | `GET /scheduler`, `/scheduler/profiles`의 `profile.condition`(CurvePlanSpec: 모델 산출물, runtime, 전처리 산출물, `activeProtocol.dataset`)(`internal/serving/types.go:137-150`, `internal/profiling/derivation/curve_plan.go:74-126`) | 소비 중, 타입은 `unknown`이고 원본 JSON으로 표시(`lib/read-types.ts:39`, `features/scheduler.tsx:50-51`) | U0 타입, U4 화면 |
| preflight result | `POST .../batch-energy-optimizations/preflight`의 `resolves`, `reason`, `plan`(`internal/operatorconsole/batch_energy_optimization_preflight_view.go:31-39`) | `/measurements/check`에서 원본 JSON으로 소비(`features/read-pages.tsx:137-169`). 측정 요청 화면은 `dryRun=All`만 사용. | U0 타입, U3 화면 |
| operation `staticRouting`, `sourceConfigRV` | scheduler state와 기록의 `results[].operation`(`internal/serving/kube/operations.go:66-77`). 실제 반영은 `results[].actual.routing`, `status`, `error`, `progress`(`internal/serving/kube/static_routing.go:36-97`) | 소비 전. 동작 이름만 표시하고 두 필드는 원본에 남는다(`features/scheduler-transitions.tsx:50-62`, `lib/read-types.ts:50-51`). | U0 타입, U4 화면 |
| readiness reasons | `GET /workloads`의 준비 상태와 사유: `Preparing`, `Changing`, `AppliedDefinitionUnavailable`, `RestartDrain`, `RestartUnconfirmed`, `InstanceStopped`, `NodeDrained`, `NoCurrentReadiness`, `ConditionChangedBeforeDrain` 등(`internal/serving/kube/workload_view.go:45-77`, `internal/serving/runtime.go:117-128,488-530,701-715`). `GET /systems`의 구성요소 condition 이유(`internal/operator/system/status.go:17-45`) | 일부만 문장. 나머지 CamelCase 값은 "확인이 필요해요"로 합쳐진다(`components/read-region.tsx:33-50`). 서비스 화면의 결론과 다음 할 일은 사유별 분기로 직접 쓴다(`features/management.tsx:411-412`). | U1 표, U2 서비스 화면, U5 개요 |
| error codes | Serving `{code, field, message}`: `IncompatibleDataset`, `AppliedSnapshotUnavailable`, `AppliedStateUnconfirmed`, `EntrypointRestarted`, `BackendError`, `PreprocessFailed`, `SLODeadlineDrop`, `RequestCanceled` 등(`internal/serving/kube/manager.go:340-341,418-426`, `internal/serving/firstfloor/client.go:206-212`, `internal/serving/runtime.go:282-285`). Console `{code}`: `*Unavailable`, `*ProjectionFailed`, `BatchEnergyOptimizationPreflightRequestInvalid`(`internal/operatorconsole/readapi/queries.go:94-108,125-135,169-181`, `internal/operatorconsole/readapi/handler.go:54-119`) | HTTP 상태별 문장과 일부 code만 처리(`components/read-region.tsx:27-32,68`, `features/management.tsx:325-347`) | U1 표, U2 관리와 추론 오류 |
| measurement outcomes | campaign `status.outcome`: `MinimumFound/LocalMinimum`, `MinimumFound/SearchSpaceExhausted`(`internal/profiling/campaign/controller/curve_deriver.go:22-24`). 실패 이유: `TransitionFailed`, `RetryLimitExhausted`, 재시도 중인 `CurvePlanResolutionFailed`(`internal/profiling/campaign/controller/reconciler.go:27-29,108-113`) | 접두사 없는 결과 두 개와 실패 두 개만 문장(`features/measurement-plans.tsx:23-34`) | U1 표, U3 화면 |
| `/systems` | `GET /systems`(`internal/operatorconsole/readapi/queries.go:87-117`), relay 허용(`lib/relay.ts:59`) | 화면 소비 전 | U5 |
| 운영 전력 집계 | `GET /observations/power`(`internal/serving/httpapi/power.go:10-32`) | 소비 중(`features/scheduler.tsx:251-267`) | 유지 |
| runtime 계열과 backend | `GET /models`의 실행 경로. ChillServer 검증은 family `firstfloor`, ONNX는 `ort-cpu`와 `ort-cuda`, RKNN은 `rknnlite`(대상 `rk3588`, 선언 batch 크기, FP32 rank-3 입력 하나)를 통과시킨다(`internal/modelcatalog/catalog.go:107-123`). | 편집기가 검증 밖의 family 선택지를 제공하고 backend를 원본 코드로 표시(`features/management.tsx:170-173`) | U2 |
| 측정의 runtime, backend와 provider | Evidence Passport `measurement.runtime`은 family, version, provider, architecture를 제공한다(`internal/operatorconsole/evidence_passport_view.go:66-83`). 검증된 원본의 backend는 `RuntimeResult.Backend`(`internal/profiling/run/evidence.go:35`)에 있고 Passport 투영은 G2의 선행 조건이다. backend 표시는 이 검증된 값만 근거로 삼는다(Run의 `candidate.executionPath`는 사용자가 정한 이름이다). | provider 세 개만 이름으로 표시(`features/read-pages.tsx:178,190-194`) | U1 표, U3 화면(제공 필드), G2(backend와 장치) |
| 전처리기 계약 | `GET /preprocessors`와 workload `appliedDefinition.preprocessor`의 `input`, `output` 필드(`chill-dashboard/lib/domain.ts:19-21,34`) | 샘플 필드 이름표가 계약 이름을 바꿔 표시(`features/management.tsx:50-52`, `features/inference.tsx:20-21,127,191`) | U2 |

### 5.3 ChillServer가 제공할 조회 (셸 설계)

SRS 8절의 "ChillServer가 제공할 조회"는 아래 셋이다. 각 기능은 route와 빈 상태를 이 문서에 설계해 두고,
해당 조회가 ChillServer에 생긴 뒤 9.2절의 G 단위로 구현한다. 그때 DB-INT-05에 따라 해당 기능만
활성화하고, 다른 조회 결과와 기능별 지원 여부는 각 영역의 독립 Query로 계속 표시한다.

| SRS | ChillServer가 제공할 조회 | 설계한 화면 | 조회 결과가 비었을 때 |
|---|---|---|---|
| DB-FR-13 | 측정 결과마다 실제 backend와 실행 장치. 기존 Evidence Passport의 `EvidencePassportRuntime`(`internal/operatorconsole/evidence_passport_view.go:66-83`)에 검증된 `RuntimeResult.Backend`(`internal/profiling/run/evidence.go:35`)와 firstfloor 장치 필드를 함께 투영한다. 장치 근거 값은 firstfloor `RuntimeMetadata`의 `device`, `providers`, `gpuUUID`, `rknn`이다(`internal/firstfloor/types.go:136-155`). | `/measurements/profiling-runs/[namespace]/[name]`의 `실행 backend`와 `실행 장치` 행. 응답의 backend, provider, device를 직접 쉬운 말(예: CPU에서 ONNX 실행, CUDA GPU, NPU)로 표시하고 원본은 원본 보기에 둔다. | backend나 장치 기록이 빈 측정은 "이 측정에는 실행 backend와 장치 기록이 비어 있어요"를 보인다. |
| DB-FR-30 | 실행 경로 × DeviceClass 호환 판정 `Compatible`, `Incompatible`, `Unknown`과 사유 코드(`internal/modelcompat/types.go:10-37`). 현재 Inventory는 선언만 반환하고 판정은 `internal/modelcompat` 내부에 있다(`internal/operatorconsole/inventory_view.go:70-72`). | `/workloads/[name]/coverage`: 실행 경로별 DeviceClass 측정 가능 여부와 사유. 서비스 화면의 `측정 가능 범위` 행에서 들어간다. 측정 요청의 장비 단계는 고를 수 없는 장비에 같은 사유를 표시한다. | 판정이 빈 서비스는 "아직 판정한 장비가 없어요"를 보인다. |
| DB-FR-32 | PowerModeTransitionCost 원본: 출발과 도착 전력모드, 방향별 반복의 소요 시간과 J, 관측 구간, 측정 상태, 반복별 검증 판정과 사유, 경계 불확실성, 전환 중 Serving 지속 여부, 원본 참조 | `/measurements/transition-costs` 목록과 `/measurements/transition-costs/[namespace]/[name]` 상세. `/records` 목차의 `전환 비용 원본` 행과 후보 상세의 전환 비용 행(`OperationCost.reference`)에서 들어간다. | 기록이 빈 목록은 "아직 잰 전환 비용이 없어요. 판단에 쓴 전환 비용은 후보 상세에서 볼 수 있어요"를 보인다. |

route, 진입 행과 빈 상태는 해당 G 단위가 한 commit에서 추가한다. 조회가 생기기 전까지 Dashboard 코드는 그대로다.

### 5.4 연결 예정 API 계약

다음은 영역별 연결 계약과 ChillServer의 책임이다. 현재 웹 개발은 제공된 경로를 연결하고
준비 중인 영역은 화면 틀과 계약으로 유지한다. 새 경로는 기존 TokenReview 및
정확한 verb와 path 권한 검사를 통과한 뒤 연결한다.

| 제안 method와 `/api/v1` 하위 경로 | 응답 또는 입력의 핵심 계약 | 책임과 현재 공백 |
|---|---|---|
| `GET /systems`의 `capabilities` | 기능별 `{name, available, reason}` | System 조회는 구현됐다. `capabilities` 필드는 ChillServer 조회 대기다. |
| `GET /profiling/options` | 모델과 경로, 장비와 Node, 전력모드, 입력 및 측정과 관측 정책의 선택 항목과 sourceRef | 기존 선언과 configured fact resolver를 사용하는 Console 조회가 필요하다. |
| `GET, POST /namespaces/{namespace}/profiling-runs`, `GET .../profiling-runs/{name}` | 생성 입력 `{name, spec}`, Run identity와 immutable spec, 실제 status와 시각 | bounded GET 목록 구현 완료. exact detail과 인증된 create adapter는 연결 준비 중이다. |
| `GET .../batch-energy-optimizations/{name}` | immutable campaign spec, child Run 및 trial 참조, 실제 진행과 result 참조 | 목록, 접수와 삭제는 Serving에서 제공한다. 단건 진행 조회가 필요하다. |
| `GET, POST /namespaces/{namespace}/power-mode-transition-costs`, `GET .../power-mode-transition-costs/{name}` | 5.3절의 DB-FR-32 조회와 같은 원본, 측정 요청 입력 | 기존 전환 비용 측정과 NodeActuator를 재사용한다. 조회와 create adapter가 필요하다. |
| `GET /profiles` | 조건별 성공 Run, 실행시간 분포, 처리량, 메모리, 전력과 에너지, 유휴 기준 및 trial 참조 | 채택 프로파일은 `/scheduler/profiles`가 제공한다. 조건별 전체 목록은 검증된 Run과 campaign 결과의 조회 projection이 필요하다. |
| `GET /namespaces/{namespace}/profiling-runs/{name}/evidence/raw` | 권한 범위의 구조화된 raw, 원본 참조와 단위 | 기존 검증 경계와 봉인된 결과 reader를 재사용하는 전송 경로가 필요하다. |
| `GET, PUT /allowed-resources`, `POST /nodes/{uid}/maintenance` | 허용 Node와 실행 경로 및 전력모드, 제외 또는 복귀, version, 영향, 원본 작업 참조 | ChillServer의 자원 정책과 유지보수 관리 API가 필요하다. 적용은 CHILL의 실제 드레인과 actuator가 수행한다. |
| `GET /observations/workloads/{uid}/requests/{requestID}` | 원본 적용 정의, 단계와 실제 outcome, CHILL 종단 지연과 SLO 초과 여부 | 기존 관측 기록의 exact 요청 조회 전송 경로가 필요하다. |
| 증분 에너지와 동작별 전환 에너지 귀속 | 전체 대비 증분 에너지, 동작별 전환 소비와 누락 | 전체와 활성 POOL 집계는 `GET /observations/power`가 제공한다. 증분 에너지와 동작별 귀속은 ChillServer 조회 대기다. |
| `GET /experiments`, `GET /experiments/{id}` | 시나리오, 프로파일과 설정, 초기 구성, 제어요소, 요청과 전력 및 전환의 같은 시간축 참조 | 시스템 실험 기록과 조회 projection이 필요하다. |
| 영역 간 변경 feed | 영역 간 행위자, 대상과 접수 및 적용 결과 | 서비스 관리 관측 기록과 scheduler settings 사건은 각각 연결했다. 영역 간 통합 feed는 ChillServer 조회 대기다. |
| `GET /exports` | 자료 종류, 대상, from과 to 및 필터로 선택한 records, 집계 조건, 단위와 sourceRefs | 해당 조회 및 archive를 사용하는 권한 검사와 비밀정보 제거가 필요하다. |

목록에는 제한된 `limit`과 opaque `cursor` 또는 offset을 사용하고 실제 조회 범위를 함께 표시한다.
시간 조회에는 RFC3339Nano `from`, `to`와 실제 적용한 구간 및 집계 설정을 반환한다.
현재 응답의 필드 이름과 상태 의미를 유지하고 각 영역의 구체적인 DTO를 사용한다.
새 조회에는 identity, 원본별 시각, 실제 범위와 누락 이유를 포함한다.
`capabilities`가 제공되면 `{name: string, available: boolean, reason: string}` 배열을 사용한다.
지원 여부와 사용자의 호출 권한은 별도로 표시한다.
새 DTO의 관측 공백은 null과 이유로 표현한다. sourceRef는 원본 kind, namespace, 이름과 UID,
resourceVersion 또는 봉인된 객체 참조를 사용하고 관측 시각은 RFC3339Nano로 반환한다.
변경 진행은 API가 반환한 원본 operation 또는 대상 identity로 조회한다. 전후 정의와
desired 및 applied version은 ChillServer의 계약을 사용하고 전환은 실제 남은 상태와 후속 판단을 연결한다.

### 5.5 ChillServer와 CHILL 구성요소의 책임

- Run과 campaign create는 기존 Kubernetes schema 및 admission을 사용한다. 실제 실행과 progress는 기존 controller의 관측을 사용한다. 스펙 변경은 새 요청으로 제출한다.
- Evidence 조회와 내보내기는 [RetainedLoader](../../../internal/profiling/profiler/evidence/kube/retained_loader.go)의 검증된 타입을 사용한다. 검증은 그 경계에서 수행하고 raw 배열은 별도 bounded 조회 또는 내보내기로 제공한다.
- 프로파일 조회는 검증된 성공 Run과 campaign 결과를 사용하고, 조건에 맞는 최신 성공 선택과 한 Run 내부 trial 집계를 각각 표시한다.
- POOL projection은 실제 Pod와 Node, 모델 및 경로, 자원 할당과 검증된 전력모드 readback을 사용한다. 측정 당시 조건은 provenance로 표시한다.
- 허용 자원과 유지보수 API는 버전 있는 선언, 확인 가능한 영향과 실제 진행을 반환한다. 내부 `MoveNodePool`은 실제 요청 드레인 후 label을 바꾸는 원자 동작으로 재사용한다. 목적지 선택과 유지보수 상태는 자원 관리 구성요소가 관리한다.
- 구성 선택과 시스템 전환은 [구성 선택 명세](../../spec/04_configuration_selection.md)와 [전환 명세](../../spec/05_transition_control.md)의 구성요소가 수행한다. [현재 구현 범위](../../../README.md#chill)에 맞춰 화면과 API의 연결 준비 상태를 표시한다.
- 운영 전력은 각 Node를 한 번 포함한 전체 POOL과 활성 범위, 관측 누락을 `GET /observations/power`로 반환한다. 프로파일링 watts는 해당 측정 조건으로 한정한다.
- 기존 요청 관측 기록은 전체 선택 구간의 통계를 page 분할 전에 계산한다. 추가 분포와 백분위, SLO 초과 및 archive 조회도 Serving이 계산한다. archive 객체 참조와 실제 archive 조회 능력을 구분한다.
- 변경의 actor와 전후 정의, 접수 및 적용 결과는 해당 도메인이 보존한다. `changes`는 이 원본 기록의 조회 projection이다. 웹 접근 로그는 중계 진단으로 분리한다.

측정 웹 동작은 immutable 요청 접수와 실제 진행 및 결과 조회로 구성한다.
개입은 해당 API가 제공하는 동작과 조건을 따른다.
저장된 계약 변경은 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 작업 단위와 정리 원칙을 따른다.

## 6. 핵심 화면과 변경 흐름

### 6.1 route와 화면

한 화면은 한 기능만 한다. 질문 하나에 답하거나 동작 하나를 끝내는 화면이며, 결론 문장 하나, 핵심 값
하나, 주 동작 하나로 구성한다. 딸린 정보는 `›` 행으로 다음 화면에 두거나 Dialog로 연다. 여러 값을
받는 입력은 한 단계에 질문 하나인 단계형으로 만들고, 고를 수 있는 원본 목록이 있으면 선택으로 받는다.
본문은 가운데 좁은 한 열이다. 목록의 각 행은 상세나 작업 화면으로 바로 이동하고, 목록 위에는
CHILL 대상, namespace와 시간 구간 등 실제 조회 범위를 한 문장으로 적는다(DB-UX-03).

`변경 SRS` 열은 SRS `25f7edd`로 바뀌는 화면이다. `(조회 대기)` 행은 5.3절의 셸 설계이며 G 단위에서 구현한다.

| 웹 route | 한 가지 기능 | 변경 SRS |
|---|---|---|
| `/` | 지금 CHILL 상태. 기준 구간은 최근 5분이며(ChillServer 전력 집계가 15분 구간에 약 11초 걸려 조회 제한 10초를 넘는다) 화면 위에 실제 from과 to를 적는다. 아래 요약 행은 각각 독립 조회이고, 한 조회의 실패는 그 행에만 오류와 마지막 관측 시각으로 보이며, 각 행은 기존 상세 화면으로 이동한다. 지금 있는 조회만 사용한다. (1) 시스템 상태(`GET /systems`): 결론은 `Ready` condition의 status와 reason 문장이고, `Ready=True`의 `ComponentsDisabled`는 "관리 구성요소를 모두 꺼 둔 상태예요"라는 별도 문장이다. True를 포함한 모든 구성요소 condition을 상태, 이유 문장과 시각의 행으로 보이고 원본 condition은 원본 보기에 둔다. (2) 서비스(`GET /workloads`, 서비스별 같은 구간의 `GET /observations/workloads/{uid}?limit=1`): 서비스마다 접수 상태, SLO 백분위와 목표 시간, 적용 정의 cohort의 실제 백분위 지연과 목표 충족 여부, outcome별 요청 수를 보이고 `/workloads/[name]/operation`으로 이동한다. (3) 부하와 큐(같은 구간의 `GET /observations/load`, `GET /scheduler`의 `snapshot.outstanding`과 관측 시각): 서비스별 수신, 접수, 완료 사건 수와 현재 대기 요청 수를 보이고 `/scheduler/load`로 이동한다. (4) 전력과 에너지(같은 구간의 `GET /observations/power`): 전체와 활성 POOL의 J와 평균 W, 집계 구간, `complete`와 관측 공백(`reasons`, `gapUntil`)을 보이고 `/scheduler/power`로 이동한다. (5) 진행 작업(`GET /scheduler`의 actions와 results, `GET /workloads`의 operation, 측정 목록의 `Measuring`): 진행 중이거나 실패한 변경과 측정을 심각한 순서로 보이고 `/transitions`, `/workloads/[name]/definition`, 측정 상세로 이동한다. | DB-FR-03, DB-INT-05 |
| `/workloads`, `/models` | 서비스와 모델 목록. 주 동작은 추가다. | |
| `/workloads/[name]` | 서비스 상태의 결론과 다음 할 일 하나. 결론은 준비 상태와 사유의 문장(6.4절)이며 다음 할 일은 같은 표의 다음 행동이다. 지금의 사유별 분기(`features/management.tsx:411-412`)는 이 표 조회로 바꾼다. 원본 상태와 사유 코드는 원본 보기에 둔다. 정의와 적용, 측정 근거, 배치, 최근 운영, 변경 기록은 행으로 각 화면에 연결한다. | DB-FR-06 |
| `/workloads/[name]/{definition,evidence,placement,operation,changes}` | 서비스의 한 측면. 배치는 `v1/pools`와 운영 판단의 요청 분배, 운영과 변경은 표시한 최근 구간의 ChillServer 관측에서 읽는다. | |
| `/workloads/[name]/coverage` (조회 대기) | 이 서비스를 실행 경로별로 어느 DeviceClass에서 잴 수 있는지와 그 사유. | DB-FR-30 |
| `/models/[name]`, `/models/[name]/{files,services}` | 모델 요약, 파일과 실행 방식, 연결된 서비스와 삭제 영향. 실행 방식은 "CPU에서 ONNX 실행", "NVIDIA GPU(CUDA)에서 ONNX 실행", "Rockchip NPU에서 RKNN 실행"처럼 사용자 용어로 쓰고 원본 family와 backend는 원본 보기에 둔다. | DB-FR-04 |
| `/preprocessors`, `/datasets`, `/{preprocessors,datasets}/[name]`, `/…/new`, `/…/[name]/edit` | 전처리기와 데이터셋 목록, 하나의 계약(받는 입력과 돌려주는 출력, 레코드 필드)을 결론 문장으로 보여주는 상세, 단계형 등록과 수정. 결론 문장의 필드는 계약의 이름을 그대로 쓰고 형식만 쉬운 말(JPEG는 사진, UTF8은 글)로 쓴다. 쓰는 서비스가 있으면 삭제를 막고 서비스로 이동하게 하며, ChillServer의 409 `PreprocessorReferenced`, `DatasetReferenced`, `ModelReferenced`, `ModelArtifactReferenced`는 메시지의 이름 목록으로 서비스와 전처리기 행을 보여준다. 서비스 편집은 ChillServer 규칙(대상 모델 파일과 실행 방식, 필드 이름, 형식과 크기 상한)에 맞는 전처리기와 데이터셋을 고르게 하고 규칙 밖의 항목은 이유와 함께 비활성화한다. 새 서비스는 첫 호환 데이터셋을 미리 고른다. 최종 검증은 ChillServer가 한다. | DB-FR-28 |
| `/workloads/new`, `/models/new`, `/…/[name]/edit` | 단계형 등록과 수정, 전후 검토. 모델의 실행 방식 단계는 runtime 계열 `firstfloor`를 고정 값으로 보이고, backend는 고른 모델 파일 형식에 맞는 값에서 고른다(6.2절). 버전 사전 조건, 충돌 시 최신 값 채택, 결과 미확인 처리를 유지한다. | DB-FR-04 |
| `/workloads/[name]/infer` | 추론 한 건. 입력 단계는 적용된 전처리기의 `input.fields` 순서대로 필드마다 입력 하나를 둔다: 제목은 계약의 필드 이름, JPEG는 미리보기가 있는 사진 선택과 형식 및 크기 사전 확인, UTF8은 바이트 수를 보여주는 글 입력이다. 결과 화면의 제목은 공통 문장("추론이 끝났어요")이고, 출력 개수와 형식에 관계없이 모든 `output.fields`를 계약 순서대로 `필드 이름 / 쉬운 형식 / 값` 행으로 보인다. 대기, 전처리, 추론(생성), 후처리 단계 시간, 첫 토큰과 처리 단위를 보여준다. 응답 목표가 화면 최대 대기 시간(`CHILL_INFER_TIMEOUT_MS`)보다 길면 미리 알린다. 실행 batch는 firstfloor가 구성한다. 실패는 6.4절의 오류 의미와 다음 행동으로 보인다. | DB-FR-08, DB-FR-29, DB-OP-05 |
| `/scheduler` | 지금 판단. 판단 결과가 있으면 현재 구성과 선택 후보의 예상 에너지와 이유를, 첫 판단 전이면 "첫 판단을 기다리고 있어요"와 적용 조건을 보인다. 예상 J와 SLO는 예측으로 표시한다. | DB-FR-15 |
| `/scheduler/candidates`, `/scheduler/estimates` | 후보 목록과 서비스별 응답 목표 예상. 상세는 고른 시점의 응답을 유지하는 Dialog다. 후보 인스턴스의 프로파일은 측정 조건 네 행(모델 파일, 실행 방식, 전처리기, 데이터셋)을 보인다. | DB-FR-14 |
| `/scheduler/profiles` | 운영 판단이 쓰는 측정 프로파일 목록. 행마다 서비스와 측정 조건 요약을 두고, 같은 서비스에서 모델 파일, 실행 방식, 전처리기나 데이터셋이 다른 프로파일은 다른 행으로 구분한다. 상세 Dialog는 조건 네 행과 원본 보기를 둔다. 채택(`POST /api/v1/scheduler/profiles`)과 해제(`DELETE /api/v1/scheduler/profiles/{ns}/{name}`, `If-Match: configVersion`)는 측정 화면에서 확인 창을 거친다. 채택 완료는 `Persisted`일 때, 해제 완료는 `Removed`이면서 `loaded=false`일 때다. HTTP 200의 `Pending`과 `ProfileUnloadFailed`는 반영 대기, 409 `ProfileInUse`와 `ProfileControlsEnabled`는 거절로 표시한다. `loaded`는 운영 판단 반영만 뜻한다. | DB-FR-14 |
| `/scheduler/load` | 명시한 구간의 사건 시각 부하. 조건은 Dialog에서 고르고 원래 nano 구간을 URL에 보존한다. | |
| `/transitions`, `/transitions/[id]` | 현재 변경 동작 목록과 동작 하나의 상세. 노드와 시작 identity로 읽고 계획, before/actual, 실패와 부분 적용을 표시한다. 취소와 재시도는 관측 정보다. `manual-static-routing` 동작의 상세는 설정한 분배와 실제 반영 결과를 나란히 두는 화면이다: 결론은 "설정한 분배가 반영됐어요" 또는 반영 대기와 실패이고, 실패 이유와 출처 설정 revision(`sourceConfigRV`)은 원본 보기에 둔다. 표는 서비스별 인스턴스 비율의 선언(`operation.staticRouting`)과 실제(`actual.routing`)다. 진행 기록과 오류 원문은 원본 보기에 둔다. | DB-FR-18 |
| `/resources`, `/pools`, `/pools/[id]` | 장비 목록, 실행 그룹 목록, 그룹 하나. 장비 하나의 Dialog는 선언된 사양과 전력모드를 보인다. 전력모드마다 제어 방식과 목표를 쉬운 말로 쓴다: `Fixed`는 "장비 기본 상태 그대로 써요", `JetsonNVPModel`은 "Jetson 전력 모드 N번으로 바꿔요", `NvidiaLockedGPUClock`은 "GPU 클럭을 a~b MHz로 고정해요". 장비 사양은 선언값이며 실행 적합성은 측정 근거와 DB-FR-30 조회로 판단한다. | DB-FR-02 |
| `/records`, `/settings` | 기록과 설정의 목차. | |
| `/observations`, `/changes` | 고른 서비스의 요청 목록과 변경 목록. 서비스는 목록에서 고르고, 조회 조건과 구간 통계, 개별 기록은 Dialog다. 표본 수가 0인 구간은 0개와 빈 통계 값으로 표시한다. | DB-FR-23 |
| `/scheduler/history`, `/changes?scope=scheduler`, `/transitions?view=history` | 판단, 운영 설정, 변경 동작 기록 목록. local 종류 필터, 원래 nano 구간과 append offset을 보존하고 A/B 선택은 비교 화면으로 이어진다. | |
| `/scheduler/power` | 노드와 계측 범위별 전력 기록. 전체와 활성 POOL 합계는 `GET /observations/power`의 ChillServer 집계를 그대로 표시한다. | |
| `/scheduler/compare` | 두 판단의 조건 일치 여부와 비교. 기록된 판단의 조건과 결과를 나란히 두며, 실험 실행과 인과 귀속은 `/experiments` 범위다. | |
| `/measurements` | 측정(BatchEnergyOptimization) 목록. `GET /api/v1/namespaces/{ns}/batch-energy-optimizations`를 진행 중, 끝내지 못한 측정, 끝난 측정 순으로 나누고, 진행 중이 있으면 자동 갱신한다. 목록 응답은 진행 상태까지 담으므로 결론은 상세에서 보인다. | |
| `/measurements/new` | 단계형 측정 요청. 모델, 장비와 실행 방식은 `v1/models`와 `v1/inventory`에서 고르고, 측정 방식은 같은 조건의 직전 측정에서 가져온다. 전력모드 단계는 `/resources`와 같은 제어 방식 문장을 보인다. 검토 단계는 같은 요청 본문으로 ChillServer preflight를 호출해 결론 문장(측정할 수 있어요, 또는 `reason`의 이유)과 "이렇게 측정해요" 계획 요약(batch 크기, 반복 수, 측정 구간)을 보인다. `resolves=true`이고 `?dryRun=All`이 통과하면 제출하며, 202 접수 뒤 측정 화면으로 간다. 이름 충돌과 결과 미확인은 사용자가 명시적으로 다시 제출한다. 장비 단계의 측정 가능 사유는 DB-FR-30 조회 이후 G1에서 추가한다. | DB-FR-02, DB-FR-12 |
| `/measurements/batch-energy-optimizations/[namespace]/[name]` | 측정 하나. 진행 중이면 회차 수, 실패면 실패 이유의 문장과 다음 행동(같은 조건 재요청 등), 완료면 결과 문장과 결과 `detail`의 가장 효율적인 크기와 크기별 증분 에너지, 채택과 채택 해제. 결과와 실패 문장은 6.4절의 표에서 오고 원본 outcome은 원본 보기에 둔다. 영구 삭제는 진행 중이거나 채택 중이면 막고, `?uid=`와 `If-Match: resourceVersion`으로 요청한다. | DB-FR-13 |
| `/measurements/runs`, `/measurements/group`, `/measurements/profiling-runs/[namespace]/[name]` | 개별 측정 회차 목록과 상세. 상세의 `측정한 실행 환경` 행은 Passport가 지금 제공하는 runtime family, version, provider와 architecture를 쉬운 말로 보이고 원본은 원본 보기에 둔다. `실행 backend`와 `실행 장치` 행은 G2에서 추가한다. | DB-FR-13 |
| `/measurements/check` | 계획 파일의 읽기 전용 사전 확인. 결과는 `/measurements/new` 검토 단계와 같은 결론 문장과 계획 요약이다. 원본 `plan`은 원본 보기에 둔다. | DB-FR-12 |
| `/measurements/transition-costs`, `/measurements/transition-costs/[namespace]/[name]` (조회 대기) | 전력모드 전환 비용 원본 목록과 하나의 상세. 상세는 출발과 도착 전력모드, 방향별 반복의 시간과 에너지, 반복별 검증 판정과 사유, 경계 불확실성, 전환 중 Serving 지속 여부와 판단에 쓴 전환 비용 연결이다. | DB-FR-32 |
| `/configuration` | 운영 설정. 행마다 값 하나를 Dialog에서 고치고, 모은 변경을 검토해 전체 SchedulerSettings를 한 번에 적용한다. 최신 값 재조회, 권한 오류와 결과 불명확 시 재조회를 유지한다. `설정한 요청 분배` 행은 `GET /scheduler/state`의 `results` 중 가장 최근 `manual-static-routing` 결과의 `/transitions/[id]` 상세로 이동한다. 해당 결과가 비어 있으면 행은 "설정 기반 분배 기록이 비어 있어요"를 보인다. | DB-FR-17, DB-FR-18 |
| `/experiments` | 실험 API 준비 상태와 판단 비교 링크. | |

목록 및 상세의 참조는 kind, namespace, 이름과 UID 또는 원본 evidence identity를 보존한다.
Workload 관측과 과거 변경은 등록 UID를 사용한다. 상세 URL에 기대 UID를 보존해
같은 이름의 재등록을 별도 대상으로 표시한다. UID, 내부 코드, nano 값과 원본 JSON은
`원본 자료 보기` Dialog에 둔다. 6.4절의 표에 없는 코드도 본문에는 공통 안내를 표시하고 원본 보기에 코드를 둔다.
메뉴는 홈, 서비스, 운영, 측정, 기록, 설정 여섯 개다.
로그아웃 상태는 접속 폼만, 접속 뒤 신원 변경과 로그아웃은 `접속 관리` 모달만 사용한다.

화면의 색은 의미를 가진다. 서리색 바탕 위에 동작과 초점은 냉각수 파랑, 에너지와 주의는
열 주황으로 표시하며, 상태는 색, 아이콘과 문구를 함께 쓴다.

### 6.2 안내형 입력

- 모델: 실제 등록 모델의 설정 재사용과 파일, 입력과 실행 방식 선택을 제공한다. 실행 방식은 ChillServer 검증(`internal/modelcatalog/catalog.go:107-123`)과 같은 규칙으로 고른다: runtime 계열은 `firstfloor` 고정, ONNX 파일은 `ort-cpu`(CPU에서 ONNX 실행)나 `ort-cuda`(NVIDIA GPU에서 ONNX 실행), RKNN 파일은 `rknnlite`(Rockchip NPU에서 RKNN 실행)다. RKNN 파일은 대상 플랫폼 `rk3588`, 선언 batch 크기와 FP32 rank-3 입력 하나가 필요하다는 안내를 선택 전에 보인다. 형식별 backend 표는 `lib/domain.ts`에 한 번 둔다. 최초 파일 보관 주소는 고를 원본 목록이 생길 때까지 직접 입력한다. credential은 기존 Secret 참조로 취급한다.
- 워크로드: 등록된 모델을 이름으로 선택하고 `0 < p ≤ 100`인 백분위 및 유한한 양의 목표 시간을 입력한다. 보통 시간은 숫자와 단위로, 원래 복합 Go duration은 그대로 작성하며 검증은 ChillServer parser가 한다.
- 추론: 입력과 결과는 전처리기 계약만으로 구성한다. 필드 이름은 계약의 이름을 그대로 표시하고 쉬운 말은 형식과 입력 크기 설명에 쓴다. 출력 필드는 계약 순서대로 표시하며 모든 워크로드와 모델에 같은 규칙을 적용한다. 측정 근거가 있는 서비스에서만 제출을 활성화한다.
- 측정: 선언된 모델, 경로, DeviceClass, Node와 전력모드를 선택한다. 전력모드 선택지는 장비 화면과 같은 제어 방식 문장을 함께 보인다. 입력과 자원 및 스레드는 선택한 실행 설정과 연결하고 batch, warmup, 반복과 측정 및 관측 구간을 안내한다. 검토 단계는 ChillServer preflight의 결론과 계획 요약을 보인다.
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
정의는 채택한 실제 최신 상태다. 채택과 제출은 같은 UID의 대상에만 허용한다.

제출 중 버튼은 비활성화하고 mutation은 `retry: false`로 설정한다. 201 등록, 202 접수와 실제 완료를
서로 구분한다. accepted 작업과 원본 대상의 URL로 이동해 실제 operation을 조회한다.
새로고침 후에는 인증과 해당 원본 조회를 복원한다. 삭제는 원래 UID의 실제 제거를 확인한다.
응답 미수신은 결과 미확인으로 표시하고 조회 가능한 원본과 명시적 후속 행동을 제공한다.

### 6.4 코드와 문장 표

준비 상태와 사유, ChillServer 오류 코드, 측정 결과와 실패 이유, backend와 실행 provider의 쉬운 문장은
`chill-dashboard/lib/labels.ts` 한 곳에 둔다. 모듈은 표 하나와 조회 함수 하나다.

```ts
// 코드 → [쉬운 의미, 다음 행동]. 근거: 아래 표의 ChillServer 파일.
export const codeText: Record<string, readonly [string, string]> = { /* … */ }
// 표에 없는 코드는 공통 문장을 돌려주고, 원본 코드는 호출한 화면의 원본 보기에 둔다.
export const explain = (code: string) => codeText[code] ?? ["새 상태의 설명이 준비 중이에요.", "원본 보기에서 자세한 내용을 확인해 주세요."] as const
```

| 코드 묶음 | 근거 | 사용하는 화면 |
|---|---|---|
| 워크로드 준비 상태와 사유 | `internal/serving/kube/workload_view.go:45-77`, `internal/serving/runtime.go:117-128,488-530,701-715`, 기존 `MissingEvidence`, `MissingDataset`, `MissingRuntime`, `NativeNotReady`(`components/read-region.tsx:36-37`) | `/workloads/[name]`, 개요 |
| 구성요소 condition 이유 | `internal/operator/system/status.go:17-45`(`ComponentsAvailable`, `ComponentsDisabled`, `ComponentsNotReady`, `ObservationFailed`, 구성요소별 `Ready`, `Missing`, `Pending`, `Progressing`, `Degraded`) | `/` 개요의 시스템 상태 행 |
| ChillServer 오류 코드 | 5.2절의 Serving과 Console 근거, 409 참조 코드, `SchedulerUnavailable`, `InvalidQuery` | 공통 오류 영역(`QueryState`), 관리 변경과 추론 오류 |
| 측정 결과와 실패 이유 | `internal/profiling/campaign/controller/curve_deriver.go:22-24`, `internal/profiling/campaign/controller/reconciler.go:27-29,108-113` | 측정 상세 |
| backend와 실행 provider | `internal/modelcatalog/catalog.go:107-123`, Passport `measurement.runtime.provider`, G2 이후 Passport의 backend | 모델 파일, 측정 회차 상세 |

규칙:

- 본문은 의미 문장과 다음 행동을 보이고 원본 코드는 원본 보기에 둔다. 표에 없는 코드도 본문에는 공통 안내를 표시하고 원본 보기에 코드를 둔다.
- 상태 배지의 짧은 단어(`Ready`, `Waiting` 등)는 `components/read-region.tsx`의 `statusLabel`에 그대로 둔다. CamelCase 사유의 문장은 `explain`에서 가져온다.
- HTTP 상태만 있는 오류(본문에 code가 없는 401, 403, 404, 409, 429, 502, 503, 504)는 `read-region.tsx`의 상태별 문장을 계속 쓴다.
- `measurement-plans.tsx`의 `failureLabels`와 `outcomeLabels`, `read-pages.tsx`의 `providers` 표는 이 모듈로 옮긴다. 측정 결과는 저장된 원본 값(`MinimumFound/LocalMinimum` 등)을 그대로 키로 쓴다. `CurvePlanResolutionFailed`의 문장은 ChillServer가 계속 다시 시도한다는 뜻을 담는다.

## 7. 관측, 상태와 화면 안전

각 영역을 독립 Query로 조회하고 지원 여부, 권한 없음, 데이터 없음, 오래된 관측,
조회 실패와 동작 실패를 각각 표시한다. 실패한 영역에도 마지막 원본 관측 시각을 남기고
다른 영역의 결과를 유지한다. capability는 실제 지원을, health와 readiness는 해당 원본의
판정을 표시한다. 시스템 구성요소 상태는 `GET /systems`의 condition을 표시하며 `/readyz`는 중계 대상 밖이다.
오류 영역은 ChillServer code가 있으면 6.4절의 의미와 다음 행동을 보이고, API가 제공한 대상, 항목과 사유를 함께 보인다.

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
표본 수가 0인 구간은 0개와 빈 통계 값으로 표시하고 raw page는 개별 기록 탐색에 사용한다.

전력과 에너지는 watts와 joules, Node와 POOL 범위, 기간 및 기준을 표시한다.
누락 값은 null, 그래프는 끊어진 구간으로 표시한다. 현재 상태, 예측과 실측은 별도 표기한다.
실험 비교는 원본이 제공한 같은 구간과 시간축을 사용하고 분석자가 해석한다.

브라우저 왕복은 fetch 시작부터 완전한 응답 본문 수신까지 `performance.now()`로 측정한다.
`X-Chill-Relay-Timing`은 ChillServer 대기와 실행을 포함한 웹 중계 왕복이다. BFF 자체 처리 비용은 별도로 측정한다.
CHILL 종단 시간은 실제 요청 관측 기록을 사용한다. 각 경계는 그 경계의 같은 시계를 사용한다.

원본 시간 문자열과 입력값은 그대로 보존하고 표시에는 명시한 시간대와 `Intl`을 사용한다.
표에는 가로 스크롤을, 핵심 폼에는 좁은 화면의 세로 배치를 사용한다. Dialog의 focus,
키보드 이동과 복원을 유지하고 상태에는 텍스트, 제출 진행에는 `aria-live`를 제공한다.
외부 문자열은 안전한 텍스트로 표시하고 인증정보는 진단자료에서 제거한다.

## 8. 독립 배포와 웹 설정

[Next standalone 출력](https://nextjs.org/docs/app/api-reference/config/next-config-js/output)을
사용한 Node 컨테이너로 배포한다. `public`과 `.next/static`도 runtime image에 포함한다.
웹은 별도 Deployment와 ClusterIP Service를 가지며 공통 ChillServer의 HTTPS gateway 또는
명시적으로 설정한 TLS Ingress로 공개한다. chart 기본 Service 유형은 ClusterIP다.
테스트용 NodePort는 해당 release의 `servingEntrypoint.service.type=NodePort`에서만 선택한다.

| 설정 | 주입과 사용 |
|---|---|
| ChillServer 주소와 CA | 서버 전용 `CHILL_BASE_URL`, `NODE_EXTRA_CA_CERTS`. native TLS 검증을 수행한다. 환경 변수 이름은 그대로 유지한다. |
| 웹 제한 | page 기본값과 최대값, 조회 구간, polling, 조회 및 변경 timeout과 body 상한을 배포 설정에서 주입한다. |
| 표시 설정 | CHILL 대상 설명, 기본 namespace와 표시 시간대. 안전한 값만 layout의 client props로 전달한다. |
| 사용자 인증 | 브라우저의 원본 bearer를 요청별 전달한다. 인증정보는 해당 사용자의 요청 범위에서 취급한다. |
| 저장소와 API credential | ChillServer와 CHILL 구성요소의 기존 Secret binding과 Pod에서 관리한다. 웹 Pod는 ChillServer 주소와 CA를 사용한다. |

웹 Pod는 `automountServiceAccountToken: false`로 배포한다. API 원본의 설정과 작업은 ChillServer가 유지하므로
Dashboard 종료와 재시작 동안 Serving 및 관리 작업의 수명은 기존 계약을 따른다.
공통 HTTPS mount에는 Console의 조회 RBAC와 ChillServer의 기존 Evidence credential 참조를 연결한다.
Console 조회 권한은 read-only와 필요한 이름 범위로 제한한다. 측정 생성 adapter에는
허용 namespace의 Run, campaign 및 전환 비용 측정 객체에 필요한 create 권한만 추가한다.
사용자 역할에는 새 HTTP 경로의 정확한 verb와 대상 권한을 추가하고 기존 CLI 및
Standalone Console 권한과 구분한다. 읽기 전용 preflight의 POST 권한은 해당 경로에 한정한다.

초기 검증 설정은 page 50개, 최대 200개, 기본 구간 1시간, 최대 24시간, 갱신 5초,
조회 timeout 10초, 변경 timeout 60초, 요청 및 응답 body 최대 8 MiB다. ChillServer와 웹 설정의 한도를 맞춘다.
단일 추론 timeout은 사용자 값과 ChillServer 상한을 사용하고 BFF의 전달 및 응답 여유를 별도로 설정한다.
raw 및 내보내기는 ChillServer의 제한된 page 또는 stream을 사용한다. 설정 변경은 실제 입력 규모를
측정한 결과로 결정한다. 이 값은 웹 검증의 초기 설정이며 측정 및 제어 정책은 CHILL 설정을 사용한다.

## 9. SRS 추적성과 구현 순서

### 9.1 추적성

`작업 단위` 열은 9.2절의 단위다. `기존`은 이전 단계에서 구현했고 이번 SRS 변경이 없는 항목이다.

| 요구사항 | 구현 위치와 API | 작업 단위 | 수용 시나리오 |
|---|---|---|---|
| DB-SEC-01, DB-SEC-03 | 4절의 bearer 전달, TLS, credential 제거 | 기존 | 권한과 만료, 내보내기와 이력 |
| DB-SEC-02, DB-SEC-04 | 4절. BFF는 설정된 ChillServer origin 하나로 중계하고 ChillServer가 신원을 검증한다(`lib/server-config.ts`, `lib/relay.ts`). | U6 | 권한과 만료 |
| DB-INT-01~02 | 2, 3, 8절의 고정 Git, 독립 모듈과 배포 | 기존 | 독립 모듈과 템플릿 |
| DB-INT-03 | 4, 5절의 ChillServer 원본 DTO와 원본 식별자 보존 | U6 | 데이터와 API 공백 |
| DB-INT-04 | 4, 6절의 도메인 writer와 If-Match | 기존 | POOL과 유지보수 |
| DB-INT-05 | 5.2, 5.3절의 조회 현황과 ChillServer가 제공할 조회, 7절의 영역별 독립 Query, 개요의 독립 요약 행 | D0, U5, G1~G3 | 데이터와 API 공백, 시스템 개요 |
| DB-INT-06 | 7절의 영역별 상태와 마지막 관측 시각 | 기존 | 데이터와 API 공백 |
| DB-UX-01~02, DB-UX-04 | 6.2, 6.3절의 단계형 폼, 필드 오류, preview와 원본 identity | 기존 | 안내형 등록, 충돌과 연결 단절 |
| DB-UX-03 | 6.1절의 목록 행 직접 이동과 조회 범위 문장 | D0 | 안내형 등록, 데이터와 API 공백 |
| DB-FR-01 | System, Inventory, Workload 상태 구분 | 기존 | POOL과 유지보수, 시스템 개요 |
| DB-FR-02 | `/resources` 장비 Dialog와 `/measurements/new` 전력모드 단계의 같은 제어 방식 문장, `GET /inventory` typed actuator | U0, U3 | 장비 전력모드 |
| DB-FR-03 | `/` 개요의 독립 요약 행: 구성요소별 condition 상태, 이유와 시각(`GET /systems`, `ComponentsDisabled` 별도 문장), 서비스별 SLO와 실제 요청 결과(`GET /workloads`, `GET /observations/workloads/{uid}`), 부하와 큐(`GET /observations/load`, `GET /scheduler`), 전체 및 활성 POOL W와 J, 구간과 관측 공백(`GET /observations/power`), 진행 작업 | U1, U5 | 시스템 개요, 에너지와 실험 |
| DB-FR-04 | 모델 실행 방식 단계와 파일 화면의 사용자 용어, `internal/modelcatalog/catalog.go:107-123`과 같은 선택 규칙 | U2, U1 | 안내형 등록, 공유 모델 변경 |
| DB-FR-05, DB-FR-07 | 기존 Model과 Workload 관리 API, operation, 영향 및 참조 보호 | 기존 | 안내형 등록, 처리 중 수정과 삭제, 공유 모델 변경 |
| DB-FR-06 | `/workloads/[name]`의 준비 사유 문장과 다음 행동, 6.4절 표 | U1, U2 | 근거 부족, 오류 안내 |
| DB-FR-28 | 전처리기와 데이터셋 계약 결론 문장의 필드 이름 | U2 | 전처리기와 데이터셋 관리, 계약 기반 입력과 결과 |
| DB-FR-08 | `/workloads/[name]/infer`의 단일 입력 제출 | U2 | 모델 공유와 추론, 계약 기반 입력과 결과 |
| DB-FR-09 | 기존 infer와 exact 요청 관측, timeout 및 지연 경계 | 기존 | 모델 공유와 추론 |
| DB-FR-29 | `/workloads/[name]/infer`의 계약 기반 입력과 결과, 6.2절 | U2 | 계약 기반 입력과 결과 |
| DB-FR-10 | POOL 실제 projection과 Inventory | 기존 | POOL과 유지보수 |
| DB-FR-11 | 5.4절의 허용 자원과 유지보수 계약(ChillServer 관리 API 대기) | D0 | POOL과 유지보수 |
| DB-FR-12 | `/measurements/new` 검토 단계와 `/measurements/check`의 preflight 결론과 계획 요약, 전력모드 문장 | U0, U3 | 측정과 실패, 장비 전력모드 |
| DB-FR-13 | 측정 상세의 결과와 실패 문장 및 다음 행동, 측정 회차의 Passport 제공 필드(family, version, provider, architecture). 실제 backend와 실행 장치는 5.3절 조회 이후 | U1, U3, G2 | 측정과 실패 |
| DB-FR-14 | `/scheduler/profiles`와 후보 상세의 측정 조건 네 행, 조건이 다른 프로파일 구분 | U0, U4 | 프로파일 조건 |
| DB-FR-30 | `/workloads/[name]/coverage`, 측정 요청 장비 단계의 사유 | G1 | 측정 가능 범위 |
| DB-FR-32 | `/measurements/transition-costs`와 상세, 후보 전환 비용 연결 | G3 | 전환 비용 원본 |
| DB-FR-15 | `/scheduler`의 판단 결과 제공 여부와 적용 조건(기존 화면 유지) | D0 | 구성과 설정 변경 |
| DB-FR-16 | `/scheduler`의 판단 근거, 예측과 실측 구분 | 기존 | 구성과 설정 변경, 전환 비용 원본 |
| DB-FR-17 | `/configuration`의 SchedulerSettings 편집, 예측 영향은 ChillServer 조회 대기 | U4 | 구성과 설정 변경 |
| DB-FR-18 | `/transitions/[id]`의 `manual-static-routing` 선언(`staticRouting`, `sourceConfigRV`)과 실제(`actual.routing`), 반영 대기와 실패 이유, `operation.settingsValidatedAt`의 설정 변경 기록에서 읽은 선택 이유와 바꾼 사람, `/configuration`의 `설정한 요청 분배` 행에서 최신 결과로 이동, `요청 분배 바꾸기`의 선언과 선택 이유 입력 | U0, U4 | 설정 기반 요청 분배 |
| DB-FR-19~21 | `/transitions`의 계획과 의존 동작, before/actual, 상태와 실패, 판단 기록 링크 | 기존 | 전환 일부 실패 |
| DB-FR-22 | UID 관측 기록과 단계별 요청 항목 | 기존 | 빈 구간과 집계 |
| DB-FR-23 | `/observations`의 전체 구간 cohort, 표본 0의 0개와 빈 통계 값(기존 화면 유지) | D0 | 빈 구간과 집계 |
| DB-FR-24 | `/scheduler/power`의 개별 기록과 `GET /observations/power` 전체 및 활성 POOL 집계. 증분 에너지는 ChillServer 조회 대기 | 기존 | 에너지와 실험 |
| DB-FR-25 | `/scheduler/compare`의 A/B 조건, 예측과 같은 구간 실제 cohort | 기존 | 에너지와 실험 |
| DB-FR-26~27 | 선택 기록과 현재 페이지 export, 설정 변경의 검증 actor | 기존 | 내보내기와 이력 |
| DB-OP-01~04 | 4, 6절의 영향 확인, If-Match, 원본 작업 조회, 제출 잠금과 명시적 재제출 | 기존 | 처리 중 수정과 삭제, 공유 모델 변경, 충돌과 연결 단절 |
| DB-OP-05 | 6.4절 표, 공통 오류 영역, 관리 변경과 추론 오류의 의미와 다음 행동 | U1, U2 | 오류 안내, 권한과 만료 |
| DB-NFR-01~06 | 2, 4, 6, 7, 8, 10절의 접근성, 값 보존, bounded 조회, 성능 예산, 독립 배포와 안전한 렌더링 | 기존 | 접근성과 화면 안전, 조회 부하와 재시작 |

### 9.2 구현 순서와 작업 단위

각 단위는 독립적으로 리뷰할 수 있는 commit 하나다. 파일 경로는 `chill-dashboard/` 기준이다.
U0을 먼저 commit한 뒤 U1을 commit하고, U2부터 U6은 서로 다른 파일만 고치므로 병렬로 진행한다.
U2~U5는 자기 파일에 남은 옛 백엔드 명칭(주석과 화면 문구)을 ChillServer로 함께 바꾼다. U4는 `features/scheduler-settings.tsx`의 명칭도 바꾼다.
build와 test는 모든 단위가 merge된 뒤 10.1절에서 한 번 실행한다.

| 단위 | 소유 파일 | SRS | 수용 확인 |
|---|---|---|---|
| D0 설계 문서 | `docs/design/dashboard/README.md` | DB-INT-05(셸 설계), DB-UX-03, DB-FR-11, DB-FR-15, DB-FR-23 | 문서에 가운뎃점과 옛 백엔드 명칭이 없고, 긍정형 문장 원칙 위반이 0건이다. SRS `25f7edd`의 변경 ID가 모두 9.1절에 있다. |
| U0 공유 타입 | `lib/read-types.ts` | DB-FR-02, DB-FR-12, DB-FR-14, DB-FR-18의 타입 | `InventoryClass.powerModes[].actuator`가 `kind`와 두 target의 union이다. `SchedulerProfile.condition`에 모델 산출물, runtime, 전처리 산출물, `activeProtocol.dataset`이 있다. `SchedulerOperation`에 `staticRouting`과 `sourceConfigRV`, `OperationState`에 `routing`이 있다. preflight 응답 `{resolves, reason?, plan?}` 타입이 있다. typecheck가 통과하고 화면 변경은 0이다. |
| U1 코드 문장 표 | `lib/labels.ts`(신규), `components/read-region.tsx` | DB-FR-03(이유 문장), DB-FR-06, DB-FR-13(결과 문장), DB-OP-05 | 6.4절의 코드 묶음이 근거 파일의 코드를 모두 담는다. fixture에서 준비 사유와 오류 code가 문장과 다음 행동으로 보이고 원본 보기에 code가 있다. 표에 없는 code의 fixture에서 본문에는 공통 문장, 원본 보기에는 실제 code가 보인다. |
| U2 계약 기반 입력과 결과, 실행 방식, 서비스 상태 문장 | `features/management.tsx`, `features/inference.tsx`, `lib/domain.ts` | DB-FR-04, DB-FR-06(서비스 화면), DB-FR-08, DB-FR-28, DB-FR-29, DB-OP-05(관리 변경과 추론 오류) | `features/management.tsx:50-52`의 `nouns`와 `features/inference.tsx:20-21`의 `fieldNames`, `:127`의 "질문" 제목, `:191`의 질문 placeholder가 삭제된다. 계약이 서로 다른 두 워크로드 fixture가 같은 규칙으로 계약 이름과 순서를 보인다. UTF8 출력이 하나인 fixture에서도 결과 제목은 공통 문장이고 그 출력은 `필드 이름 / 쉬운 형식 / 값` 행으로 보인다. 실행 방식 편집은 `firstfloor`와 파일 형식별 backend만 고르게 하며, 그 선택지는 `internal/modelcatalog/catalog.go:107-123`이 통과시키는 조합과 같다(ONNX와 RKNN 모델 fixture 각 하나). 서비스 화면의 결론과 다음 할 일이 6.4절 표에서 온다. |
| U3 장비와 측정 | `features/resources.tsx`, `features/measurement-request.tsx`, `features/measurement-plans.tsx`, `features/read-pages.tsx` | DB-FR-02, DB-FR-12, DB-FR-13 | 장비 Dialog와 측정 요청 전력모드 단계가 같은 함수의 문장을 보인다(세 actuator 종류 fixture). 측정 요청 검토 단계와 `/measurements/check`가 preflight의 `resolves=true`와 `false` fixture에서 결론과 계획 요약을 보이고 `false`에서 제출을 막는다. 측정 상세가 `MinimumFound/LocalMinimum`과 실패 이유 fixture에서 문장과 다음 행동을 보인다. 측정 회차 상세는 Passport의 family, version, provider와 architecture만 보인다. backend는 G2에서 Passport 응답 필드로만 표시한다. |
| U4 운영 판단 | `features/scheduler.tsx`, `features/scheduler-profiles.tsx`, `features/scheduler-transitions.tsx`, `features/scheduler-settings.tsx` | DB-FR-14, DB-FR-17, DB-FR-18 | 실제 compact `/scheduler/profiles` fixture에서 같은 서비스의 조건이 다른 프로파일이 다른 행으로 보이고 조건 네 행이 원본과 일치한다. `manual-static-routing` 결과 fixture(완료, 진행, 실패)에서 선언 비율과 `sourceConfigRV`, 실제 비율이 나란히 보이고 반영 대기와 실패 이유가 보인다. `/configuration`의 `설정한 요청 분배` 행이 최신 결과의 상세로 이동하고, 결과가 빈 fixture에서 빈 상태 문장을 보인다. |
| U5 개요 | `features/overview.tsx` | DB-FR-03, DB-INT-05 | 실제 fixture로 시스템 상태, 서비스, 부하와 큐, 전력과 에너지, 진행 작업 행이 구간과 함께 보인다. 모든 구성요소가 True인 fixture에서 구성요소별 상태와 이유 행이 보이고, `Ready=True/ComponentsDisabled` fixture에서 별도 문장이 보이며, condition 하나가 False인 fixture에서 그 구성요소의 이유와 다음 행동이 보인다. `/systems`, `/observations/workloads/{uid}`, `/observations/load`, `/scheduler`, `/observations/power` 각각의 403 또는 504 fixture에서 그 행만 오류를 보이고 다른 행은 그대로다. `gapUntil`이 있는 전력 fixture에서 관측 공백이 보인다. |
| U6 ChillServer 명칭 | `lib/server-config.ts`, `lib/relay.ts`, `lib/relay.test.ts`, `features/scheduler-evidence.tsx`, `features/service-hub.tsx` | DB-SEC-02, DB-SEC-04, DB-INT-03 | `ServerConfig`의 백엔드 URL 필드와 relay 인자가 `chillServer`다. 환경 변수 `CHILL_BASE_URL`은 그대로다. `chill-dashboard/`에서 옛 백엔드 명칭 검색 결과가 0건이다. relay 회귀 검사가 통과하고 동작 변경은 0이다. |

ChillServer 조회가 생긴 뒤의 단위다. 각 단위는 Phase 1 단위가 merge된 뒤 시작하는 별도 commit이며,
그 시점의 소유 파일을 단독으로 가진다.

| 단위 | 선행 ChillServer 조회 | 소유 파일 | SRS | 수용 확인 |
|---|---|---|---|---|
| G1 측정 가능 범위 | 실행 경로 × DeviceClass 판정과 사유 | 신규 `features/coverage.tsx`, `app/[...route]/page.tsx`, `features/management.tsx`, `features/measurement-request.tsx`, `lib/relay.ts`, `lib/read-types.ts`, `lib/labels.ts` | DB-FR-30, DB-INT-05 | 실제 응답 fixture에서 실행 경로별 DeviceClass 판정과 사유가 보이고, 측정 요청의 고를 수 없는 장비에 같은 사유가 보인다. |
| G2 실제 backend와 실행 장치 | Evidence Passport `EvidencePassportRuntime`의 검증된 `RuntimeResult.Backend`와 firstfloor 장치 투영 | `features/read-pages.tsx`, `lib/read-types.ts` | DB-FR-13 | CPU, CUDA GPU, NPU 측정 Passport fixture에서 측정 상세가 응답의 backend, provider, device를 모두 직접 표시한다. |
| G3 전환 비용 원본 | PowerModeTransitionCost 원본 | 신규 `features/transition-costs.tsx`, `app/[...route]/page.tsx`, `features/scheduler.tsx`, `features/hubs.tsx`, `lib/relay.ts`, `lib/read-types.ts` | DB-FR-32, DB-INT-05 | 원본 fixture에서 방향별 반복의 시간과 J, 검증 판정과 사유, 경계 불확실성, Serving 지속 여부가 보이고 후보 상세의 전환 비용에서 원본으로 이동한다. |

## 10. 검증 계획과 단계별 증거

### 10.1 이번 변경의 검증 계획

[SRS 9절](../../spec/dashboard/SRS.md#9-수용-검증-시나리오)의 27개 시나리오는 API가 준비된 영역별로 실제 API와 브라우저에서 실행한다.
각 실행에는 template commit, lockfile, CHILL source, release와 namespace, 사용자 권한,
원본 UID, 선택 구간과 웹 설정 및 결과를 기록한다. 조회 대기 기능의 시나리오(측정 가능 범위, 전환 비용 원본,
실제 backend와 실행 장치)는 해당 G 단위에서 실행하고 그 전까지 `연결 준비 중`으로 기록한다.

1. 실제 응답 수집(읽기 전용). `kubectl -n chill-system port-forward svc/chill-serving-entrypoint 18443:8080`으로
   ChillServer에 연결하고 사용자 읽기 token과 ChillServer CA로 GET만 보낸다. 수집 대상은 `/api/v1/workloads`, `/models`,
   `/preprocessors`, `/datasets`, `/inventory?limit=200`, `/systems`, `/scheduler?view=compact`,
   `/scheduler/profiles?view=compact`, `manual-static-routing` 결과가 있는 구간의 `/observations/scheduler?view=compact`,
   `/observations/power`, 측정 목록과 result 하나, profiling Run 목록과 Evidence Passport 하나다. 읽기 전용 preflight POST는
   기존 측정 spec을 새 이름으로 보내 `resolves=true`와 `false` 응답을 각각 받는다. 미등록 이름의 Workload GET으로 실제 404 code도 받는다.
   파일은 저장소 밖의 `/tmp`에 둔다.
2. fixture 화면 확인. dev server를 띄우고 브라우저 interception으로 `/api/chill/v1/**`에 수집 파일을 돌려준다.
   9.2절의 단위별 수용 확인을 desktop 1440px, 390px과 다크 모드에서 본다. 변형 fixture(False condition,
   실패 outcome, 미등록 code, 실패한 static routing)는 수집 파일의 값 하나를 바꿔 만든다.
   실제 응답으로 확인한 code와 변형으로만 확인한 code를 나누어 기록한다.
3. `npm exec --yes --package=node@24.21.0 -- make dashboard-check`를 모든 단위가 merge된 뒤 한 번 실행한다.
4. Dashboard image만 배포한다. `fast-deploy --components dashboard --platforms linux/amd64 --push-mode registry-direct --skip-deploy`로
   image를 publish하고 `kubectl -n chill-system set image deploy/chill-dashboard dashboard=<registry>/chill/chill-dashboard:<tag>`와
   `kubectl -n chill-system rollout status deploy/chill-dashboard`로 교체한다. 배포 전후에 `chill-serving-entrypoint`
   Deployment의 image와 resources, PVC `chill-serving-entrypoint`와 `chill-evidence-store`의 UID를 캡처해 같음을 확인한다.
   Helm upgrade는 이 배포 범위 밖이다.
5. 실제 브라우저 확인. HTTPS NodePort 31104로 열어 개요의 다섯 요약 행, 서비스 준비 사유, 모델 실행 방식 표시,
   추론 입력 화면(제출 전 단계까지), 장비 전력모드, `/measurements/check`, 측정 상세, 프로파일 조건, 변경 동작 상세를
   읽기로 확인한다. 추론 제출, 설정 PUT, 측정 제출, 프로파일 채택과 해제처럼 쓰기에 해당하는 확인은 사용자 승인 뒤에만 수행한다.

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
삭제했다. compatibility reader 없이 진행했으며 Evidence Store/S3와 Bound PVC는 보존했다.
최신 operator, profiler, Console, ChillServer, Dashboard 및 ORT/RKNN/관측 worker image를 registry에
publish했다. hardware 접근과 metric/actuation/execution 정책은 기존 설정을 유지했다.
`fast-deploy --skip-deploy --push-mode registry-direct`로 build/push하고
`helm upgrade --reset-then-reuse-values --wait`로 기존 release를 rev 5까지 갱신했다.
기존 release와 root는 그대로 유지했다.

07:32:09Z 캡처: CHILL 6개 Deployment는 각각 available 1, System Ready=True,
기존 System UID `17722831-d04d-4d8f-9e63-1bc13d5eef07`와 Evidence Store PVC UID
`e3d6c066-8f6b-4d83-a8a2-116c37ace771`가 유지됐다. 기존 node-discovery, node-actuator와
별도 plan10-serving Deployment는 그대로 유지했다.
실제 클러스터에 ingress controller가 없어 HTTPS ChillServer의 NodePort `https://155.230.35.213:31104/`로
Dashboard를 열었다. NodePort는 사용자 요청의 테스트 배포 값이며 chart 기본값은 그대로 두었다.
개발용 자체 서명 인증서와 내부 CA Secret을 사용한다. 브라우저 검증 도구만 개발 인증서를
허용했으며 BFF의 실제 ChillServer CA와 hostname 검증은 계속 활성화했다.

| 실제 cluster에서 실행한 경계 | 관측 |
|---|---|
| 인증 및 신원 변경 | operator/reader GET 200, reader DELETE 403, 미권한 GET 403, 누락/무효/폐기 token 401. 신원 변경 후 draft 초기화, 403 후 이전 UID 및 폼 숨김, storage/cookie에 token 없음. |
| 안내형 등록 및 공유 | 실제 불변 MobileNet artifact로 Model 201, Workload A 201의 p99.5/137.5ms 보존, 공유 Workload B의 p95.25/220ms. 참조 Model DELETE 409 ModelReferenced. |
| 동시 수정 | 실제 annotation으로 resourceVersion 변경. PUT 409 뒤 97.25/211.75ms draft 유지, 명시적 최신 버전 채택 및 재제출 200, generation 2 적용과 검증된 SA username/UID 기록. |
| 추론과 관측 | 유효한 batch-free FP32 입력에 native 503 MissingEvidence, Request ID `a992d39ec7d8c140c53fa4c862ce677e` 및 원래 Workload UID 보존. UI 제출 비활성. 같은 구간 limit 1/50의 전체 cohort 및 모집단 동일, 무표본 quantile=null. |
| 삭제와 등록 identity | Workload DELETE 202 후 실제 GET 404, 동일 이름 재등록의 새 UID와 이전 URL 변경 잠금. 참조 없는 Model DELETE 204 후 실제 GET 404. |
| export/화면 | 브라우저가 받은 source JSON 파일의 UID 보존 및 bearer 없음. 데스크톱 1440/1440, 모바일 390/390 document width, native sidebar/Escape 및 skip link의 main-content focus. 외부 Origin POST 403. |

별도 `kind-chill-dashboard-e2e`는 격리 검증이다. 그 환경에서 review permission 제거의
503 AuthorizationUnavailable과 복원 후 200도 확인했으며 실제 testbed의 review 권한은
그대로 유지했다. kind의 결과는 실제 cluster 실행과 분리해 기록한다.

실행된 gate: manifests/generate, focused Go tests, `make lint test arch-check gopls-check`,
`make helm-lint helm-template deployment-graph-check`, frozen pnpm install, lint/typecheck,
native Node tests 3개와 build. 최종 실제 values render는 70 resources, valid 58, skipped
CRD/ChillSystem 12, invalid/errors 0; 기본 및 Profiler render도 별도로 검증했다.

당시 범위 밖으로 남은 항목: 실제 qualifying native campaign 이후 Ready 추론 성공, ARM/RKNN hardware
측정, 처리 중 요청의 드레인, 운영 전력, selector, 전환과 writer 생산자, 전체 18개 시나리오와
10,000건 및 동시 5명 성능 예산. 중계 왕복 값은 ChillServer 대기를 포함하므로 BFF 자체 처리 p95 100ms의 증거는 별도로 필요하다.
따라서 이 기록은 승인된 당시 core의 구현 및 실제 배포 증거이며, 전체 SRS 완료 판정은 전체 범위 검증 후에 한다.

### 10.3 사용자 중심 UI 개편과 실제 재검증

최종 image는 `dashboard-live-20261007-ux-ready`이며 실제 release는 rev 8이다.
08:33:51Z 캡처에서 6개 CHILL Deployment가 각각 available 1이며, 기존 System 및
Evidence Store PVC UID가 유지됐다. ChillServer NodePort 31104와 private Dashboard ClusterIP도 유지됐다.
실제 Dashboard image ID는 `sha256:c1990cfcedeea3ffeb57047bd8e4eccb37f3a5f121f70bbaad4db14d4367687d`다.

| 새 UI에서 실제 실행한 흐름 | 관측 |
|---|---|
| 목록, 안내형 등록과 의존 모달 | 실제 MobileNet 설정을 선택해 임시 모델 등록. 실제 모델 이름 선택과 서비스 등록에서 p98.125/123.75ms 보존. 상세는 조회 전용이고 수정은 별도 route. |
| 동시 수정과 전후 확인 | 실제 spec을 다른 writer로 변경해 409 재현. 처음에는 채택 후 변경 전 표시가 오래된 값인 문제를 관측했다. 수정 후 실제 최신 p99.75/200ms와 유지된 p96.5/1m30.5s 작성안을 같은 확인 모달에서 관측하고 저장했다. |
| 원본과 삭제 | 브라우저 source 파일의 원래 UID와 복합 duration 보존, bearer 없음. 확인 모달로 임시 서비스와 모델을 삭제한 뒤 각각 native GET 404 확인. 검증용 SA도 제거. |
| 조회 구조 | 장비 8종, 실제 빈 실행 그룹 및 Run 목록을 각각 독립 페이지로 조회. preflight는 독립 읽기 전용 route. 설정, 전환과 실험은 `연결 준비 중`, 입력 0개 및 disabled 동작. |
| 관측과 이력 | 실제 서비스 이름으로 1시간 조회, 15분/24시간 선택지 확인. 삭제된 UID, 원래 nanosecond `from`/`to` 보존 및 같은 기간의 이력 이동. 무표본은 값 없음, 이력에는 변경 기록만 표시. |
| 인증과 화면 | 실제 미권한 SA 403 및 무효 token 401에서 이전 payload와 입력 숨김, 신원 복구 200. 로그아웃 뒤 접속 폼 1개, storage/cookie 비어 있음. 최종 desktop 1440/1440, mobile 390/390, 메뉴 자동 닫힘과 skip-link main focus. |
| 실패 응답 경계 | 유효한 batch-free FP32 입력에 native 503 MissingEvidence, Request ID `c3acab9abf3ebe38a4d8d2f6aad71d5d`, 기존 Workload UID와 relay 202.9ms 보존. 이 값은 실패 응답 경계의 증거이며 Ready 추론 성공과 BFF 자체 비용은 별도 검증 대상이다. |

최종 소스에서 lint/typecheck, native Node 테스트 3개, Node 24.21.0과 pnpm 10.33.4의 image build,
실제 values의 Helm lint/render를 통과했다(70 resources, valid 58, skipped 12, invalid/errors 0).
기존 native HTTPS load-generator caller도 실제 CA로 ChillServer에 요청해 503 MissingEvidence와
동일 Workload UID 및 Request ID를 관측했으며 경계 테스트 6개를 통과했다.
호스트 기본 Node에는 strip-types 지원이 없어 테스트 명령이 실패했고, 지원 Node 24로 실행을 완료했다.
사용처가 없는 카드, 탭과 선택 위젯은 삭제하고 native 선택 및 기존 Dialog를 사용했다.
브라우저 회귀는 실제 testbed에서 수행했으며 영구 browser-test framework 없이 진행했다.
10.2의 당시 범위와 전체 SRS 완료 제한은 그대로다.

종합 커밋 범위는 웹과 함께 선행 native Workload 관리(ChillServer), typed 입력 계약과 관련
profiling/worker/HTTPS caller 및 생성 CRD, chart, sample과 계약 테스트를 포함한다.
선별 index만 별도 디렉터리에 checkout해 `go test ./... -run '^$'`의 전체 패키지 compile,
ChillServer, serving, Console, modelcatalog와 modelcompat focused tests와 실제 ChillServer CLI `--help`를 통과했다.
기존 사용자 문서 변경인 ADR 0006, operator-console 설계 6개 및 plan 10의 링크 수정은
이 구현 커밋 밖에서 worktree에 보존한다.

### 10.4 스케줄러 연동 검증 (2026-10-13 UTC)

격리된 `kind-chill`의 `scheduler-dashboard-smoke` ChillServer와 실제 Next 화면에서
Pending과 제어요소 off, 정밀 seed와 메모리, 설정 PUT의 실제 ConfigMap 및 intent/result
journal을 확인했다. 중복 제출 1건, ConfigMap update 권한 403의 draft 유지,
실제 PUT 이후 응답 단절의 결과 재조회와 동시 변경의 최신 baseline 검토를 관측했다.
서비스 이력 기본 동작을 유지하고 설정 사건의 검증 actor와 5→6 변경을 실제 화면에서 읽었다.
빈 filtered page의 nextCursor와 입력 중 draft/원래 nano 구간 분리, 직접 기록 복원,
로그인 bookmark 유지 및 신원 변경의 선택 초기화를 확인했다.

실제 ChillServer에 없던 동작 상태와 전력 기록은 저장소 밖의 브라우저 interception으로
별도 확인했다: 계획/실행/완료/실패/취소, partial actual, 같은 ID의 시작 identity 거절,
null/0 J, 0 samples, coverage 실패, 단일 W chart, A/B 정밀 수치 및 whole-window 무표본 cohort.
이 결과는 화면 검증의 증거로만 사용하며, 실제 물리 전환과 계측 실험은 별도 검증 대상이다.
Ponytail 중간 리뷰에서 중복 원본 renderer, 순서 index identity, 범용 deep-encoding 비교와
단일 호출 wrapper를 제거했다. 입력 경계, 정밀 정수, 권한, 결과 미확인과 접근성 검사는 유지했다.
전체 SRS 완료 제한과 당시 ChillServer 조회 대기 범위는 유지한다.

Node 24.21.0/pnpm 10.33.4의 `make dashboard-check`는 사용자 worktree와 커밋된
격리 배포 소스에서 각각 통과했다(회귀 검사 10개, lint/typecheck/build).
실제 standalone 서버의 새 읽기 route와 A/B 선택을 390px에서 열었고 document 폭은
390px였다. 설정 검토는 키보드 제출/Escape 후 저장 검토 버튼으로 focus를 복귀한다.
정밀 JSON 지원 환경에서만 편집을 제공하며, 실제 미권한 사용자 403은 두 polling
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
`chill/chill-system` rev 10을 배포했다. 기존 release와 root는 그대로 유지했다.
실제 Dashboard image는 `scheduler-dashboard-8c7cbec`, image ID는
`sha256:b408d4913814e8836378b07c1743fb8657bcc211c5cad48e139f3ab08ad09641`이며 ready replica는 1이다.
System UID `17722831-d04d-4d8f-9e63-1bc13d5eef07` 및 spec, `serving-scheduler` UID/data,
기존 ConfigMap, 다른 Deployment template, PVC/Service UID와 Profiler 값을 보존했다.
Helm values의 변경은 Dashboard image뿐이며 새 scheduler editor grants는 unbound다.

기존 `chill-dashboard-user`의 단기 token으로 HTTPS NodePort 31104에서 GET만 확인했다.
scheduler/settings/state는 no-store 200, 자동 적용 off, Pending, 동작 0개였다.
현재 판단은 ChillServer가 보관한 `2026-10-10T13:35:30.019186337Z`의 과거 판단이며 새 물리 관측과 구분한다.
후보, 현재 변경 진행과 운영 설정을 실제 값으로 열고 실험/비교의 조회 경계와 선택 경계도 확인했다.
load와 scheduler journal은 BFF 504였고, 관련 화면은 빈 성공 표 대신 오류를 표시했다.
명시적 1분 구간/limit=1의 직접 ChillServer journal GET도 BFF 없이 30초 동안 응답이 없었고
ChillServer 로그에는 S3 PutObject deadline 초과가 관측됐다. live 과거 기록/A/B 및 전력 데이터의
정상 조회는 이 ChillServer 장애로 검증하지 못했다. timeout과 다른 컴포넌트 설정은 그대로 유지했다.
실클러스터 작업은 GET 조회로 한정했고 설정 PUT, 제어 활성화, 인위 전환과 전력 실험은 범위 밖이었다.
로컬 실제 ConfigMap/journal 및 별도 UI fixture의 증거를 live 제어 증거와 구분한다.
