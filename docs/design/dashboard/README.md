# CHILL Dashboard 구현 설계

[Dashboard 명세](../../spec/dashboard/SRS.md)가 제품 요구사항과 수용 기준을,
[시스템 명세](../../spec/README.md)가 측정/제어 의미를 소유한다.
여기서는 구현 소유권과 전송/표시 계약을 유지한다. 코드 연결, 격리 배포와 실제
클러스터 수용은 별도로 확인한다.

## 아키텍처와 신뢰 경계

| 경계 | 계약 |
|---|---|
| 배포 | `chill-dashboard/` 독립 Node build/deploy, 별도 Deployment/ClusterIP, Pod token automount=false |
| 스택 | Next.js App Router, React, TypeScript, Tailwind, shadcn/ui; 정확한 버전은 package.json/lockfile |
| HTTP | 브라우저 same-origin `/api/chill/v1/...` → Route Handler → 고정 HTTPS ChillServer `/api/v1/...` |
| 서버 전용 설정 | CHILL_BASE_URL, NODE_EXTRA_CA_CERTS; CA/hostname 검증과 redirect 상태 보존 |
| 공개 진입 | ChillServer의 dashboard-url 중계; chart 기본 ClusterIP |
| WebSocket | WSS `/api/live` → Node live-proxy :3000 → HTTPS `/api/v1/live` |
| Next HTTP | 별도 standalone 자식 프로세스 loopback :3001; 실패/SIGTERM 때 함께 종료 |
| 업무 | CHILL이 인증, 검증, 변경, 판단, 통계와 evidence 소유; 웹은 Kube/firstfloor/S3/Node 직접 접근 없음 |
| 상태 | 탭별 QueryClient cache, 로컬 폼, URL의 대상/UID/기간/필터 |

원본 템플릿의 MIT 고지와 복사 범위를 보존한다. 신원 변경/logout은 socket/timer 종료,
요청 취소, cache clear/QueryClient 교체와 민감 폼 remount를 함께 수행한다.
늦은 callback은 이전 cache에만 작용한다.

## 인증과 변경

Bearer는 탭 메모리와 버전/system별 sessionStorage에 저장한다. 저장 거부는 메모리
전용이며 새 창은 noopener다. BFF는 원 Authorization을 전달하고 ChillServer는
TokenReview와 정확한 verb/path SAR을 수행한다. Query의 재검증에서도 사용자 권한을
서버 SA로 대체하지 않는다.

401/4401은 인증 만료로 정리한다. 403/4403과 서비스 장애는 만료가 아니며 목적 URL을
유지한다. HTTP는 method/path allowlist, 필요한 header만 전달, no-store다.
요청/완전 응답 각각 8 MiB, 응답 header 16 KiB 한도를 유지한다.

폼은 정의/영향 검토 뒤 명시 제출하고 원본을 조회한다. UID와 resourceVersion/
If-Match로 교체/충돌을 차단한다. 201/202와 실제 완료는 구별한다. 응답 미수신은
결과 미확인으로 표시하고 원본 확인 전 자동 재제출하지 않는다.

Settings GET의 ETag를 PUT If-Match로 보낸다. 첫 입력 시 baseline/ETag를 고정하고
검토/적용 전 재조회에서 달라지면 draft를 유지한 충돌 비교로 간다. 409는 최신
baseline 명시 채택, 428은 버전 없음/재조회다. 읽기 전용 필드는 PUT에 넣지 않는다.
PlanSearchStrategy v1/v2와 실험 settingsPatch는 같은 설정 항목 정의를 재사용한다.

일반 오류는 자동 retry하지 않는다. 정확히 503/CoverageWarming만 공통 경계에서 최대
180회, Retry-After 1–5초(기본 2초)로 재시도한다. AbortSignal과 세션 수명을 보존한다.
보이는 운영자 영역만 제한 polling하며 라이브는 push다.

정밀 정수/RFC3339Nano, null과 실제 영을 보존한다. 외부 JSON/문자열은 텍스트로
표시하고 export 사본에서 credential을 제거한다. 원 HTTP/code/reason을 잃지 않는다.

## Live wire와 수명

프레임은 JSON `{type,seq,at}`다. seq는 nonnegative JS-safe integer, at는 원본 UTC다.
Hello/snapshot은 head checkpoint이며 새 순번을 만들지 않는다.
Delta/event/metrics/heartbeat는 전역 순번을 증가시킨다.

| Type | Payload와 의미 |
|---|---|
| hello | chill-live/1, serverAt, retainFrom; 상류 인증 뒤 한 번 |
| snapshot | workloads/nodes/instances/routing/decision/actions/reservations와 선택 history |
| delta | Snapshot 키의 부분집합, identity별 instances/actions upsert와 removed |
| event | Decision, action 또는 기록된 cluster Pod/issue |
| metrics | UTC 초 bucket, workload counts/latency, Node watts, pool watts와 gaps |
| heartbeat | ServerAt; 5초, 관측 자체는 아님 |

Workload는 name/UID/model/definition/SLO, autonomous/admission/pending/readiness를
구분한다. Instance는 owner/profile/Node, ready/stopped/routable, native running/waiting을
구분한다. Routing은 실제 가중치다. Pending은 배정 전 큐, waiting은 배정 뒤 native
queued/deferred다. AssignedUnknown을 실행으로 해석하지 않는다.

Decision identity는 원본 decision.at이다. Action identity는 `[id,node.uid,startedAt]`
JSON tuple이고 expected 시간은 실제 cost/source/statistic/trial 근거와 함께 nullable다.
Partial은 실패 event로 보내되 원 status를 보존한다. Reservations의 빈 배열이 해제이며
action 종료로 해제를 추정하지 않는다. Cluster는 durable 기록 뒤 발행한다.

Instances/actions는 identity별 행 전체 upsert 후 removed를 적용한다. 다른 상태 키는
존재할 때 전체 교체, 생략은 유지, 빈 값은 비우기다. Decision null은 제거한다.
관련 delta 뒤 event를 발행하고 동일 상태/판단/callback 중복은 생략한다.

인증은 정확한 `/api/live`, 승인된 HTTPS Origin/Host와 5초 내 16 KiB 이하 첫 auth
프레임을 요구한다. Query/subprotocol/누락 Origin과 임의 forwarded를 신뢰하지 않는다.
상류 TokenReview/SAR 성공 전 데이터는 없다. 401/403/503은 4401/4403/1013,
인증 뒤 wire 오류는 1002다. 인증/권한 거절은 자동 재연결하지 않는다.

Resume은 보존 `(seq,at)` 일치만 허용한다. 범위 밖/미래/재시작 불일치는 snapshot이다.
Head capture와 구독 등록은 한 lock이고 조기 resume은 하나만 보관한다. Baseline 뒤
중복은 버리고 순번 gap이면 미적용 후 재동기화한다.

Hub는 600메시지/8 MiB, 연결 큐는 602메시지/2 MiB, 프레임은 1 MiB 상한이다.
600개가 10분을 보장하지 않는다. 큰 snapshot은 자르지 않고 1009다. 느린 연결은
metrics drop 뒤 snapshot barrier로 상태를 회복하며 write 정체는 1013이다.
Producer는 socket을 기다리지 않는다. Handshake/write timeout은 10초/5초다.

재연결은 0.5초부터 최대 30초 backoff/jitter다. Baseline+연결+최근 관측 5초 안만
실시간이다. Heartbeat는 수신만 갱신한다. 클라이언트 wire 오류는 4002로 닫고
protocolError에 종류/필드를 보존하며 자동 재접속을 막는다.
숨긴 탭의 수신 중지는 별도 paused-hidden이고 복귀 시 재연결한다.

## 관측 모집단과 공백

UTC 초 bucket의 received는 수신 사건, completed/failed는 종결 사건이다.
분모는 completed+failed이며 zero 분모의 비율/분위수는 null이다. 지연은 같은 종결
집단의 유효 종단 표본과 latencySamples다. Native supplement/중복 final을 새 완료로
세지 않는다. 늦은 callback은 최근 10초 bucket을 전체 대체하고 이후는 gap/REST다.

관측 가능한 무유입 초는 zero counts, 계측 불가는 gaps다. 분위수 평균, 결손 영 채움/
직선 보간을 하지 않는다. Node W는 해당 초의 실제 표본 평균이고 이전 W를 복사하지
않는다. Pool W는 같은 범위/시각/완전성의 UID 중복 없는 합계다. J는 서버 coverage
검증 적분이며 브라우저가 재적분하거나 서비스별 공유 에너지를 배분하지 않는다.

Snapshot history는 요청/보존 sequence 범위와 gaps를 보존한다. 보존 공백을 sequence
차이로 손실 건수/시간에 환산하지 않는다. Events REST seed는 같은 from/to와 opaque
cursor로 최대 5페이지/500건, 최근 15분을 읽고 실제 identity로 live와 dedup한다.
Archive에 live seq를 만들지 않는다. 미조회 페이지와 coverage 출처를 유지한다.

## 화면과 URL 소유권

| 경로 | 소유 정보 |
|---|---|
| `/` | 운영 판독, 서비스 상태, 진행과 사건 |
| `/operations/devices` | 장치별 배치, 사양, 모드와 자원 |
| `/workloads` | 정의/적용/준비, 배치와 연결 |
| `/measurements` | 지원 조합과 범위, 현재 셀과 신선도 |
| `/measurements/requests` | 부모 요청, 자식 회차와 실패, 점검/작성 |
| `/measurements/results` | 처리 성능, 동작 비용 또는 분배 점유시간 |
| `/experiments` | 실행 목록, 게시와 결과 비교, 부하 정의 |
| `/records` | 종류별 실제 기록과 선택 근거 |
| `/settings` | 운영 설정, 모델, 전처리와 데이터셋 |

관련 화면은 대상/UID/namespace/기간을 보존해 연결한다. 같은 KPI를 새 truth로 복제하지
않는다. 서비스 관리는 관리 API로 동작하며 WS가 선행조건이 아니다.
구 화면은 `lib/dashboard-routes.ts`의 명시 매핑으로 이동하고 API 경로는 바꾸지 않는다.

공통 상태는 status.ts, StatusCell과 기존 Sheet/Dialog/DataTable을 사용한다.
한 selected identity에 한 Sheet만 열고 nested Sheet를 만들지 않는다. 핵심값/이유와
건수는 본문에 두며 원문은 접는다. 위험 작업만 Dialog다. 비동기 갱신이 읽던 값을
밀어내거나 자동 정렬하지 않는다. 1440×900에서 주요 영역은 최대 세 개다.

390px/확대 화면은 라벨/값 행으로 재배치해 모든 정보/작업을 보존한다. 키보드 선택,
Esc/focus 복원, 비색상 상태와 접근 가능한 이름을 유지한다. 의미색과 단위/정밀 숫자
format은 공통 owner를 쓴다. 새 상태 엔진, 차트 계산이나 보고서 저장소는 만들지 않는다.

## 측정과 표시 projection

Query는 bounded 목록/coverage/preflight/result/Passport를 소유하고 ChillServer는
인증/변경을 중계한다. Raw는 finalization에서 한 번 검증되고 RetainedLoader는
bounded decode다. 브라우저는 scientific admission이나 profile 유효성을 재판정하지 않는다.

Measured runtime image/version은 현재 semantic condition이다. Collection policy,
observer/window/meter와 warmup domain은 별도 provenance다. 현재 실패/partial,
replacement와 reuse lineage는 별개다. 채택 catalog의 실제 선택이 사용 중의 근거이며
최신 실패를 옛 성공으로 숨기지 않는다.

현재 allocation은 GPUUUID/memoryBytes다. 삭제된 CPU allocation/thread fields를
요구하지 않는다. 실제 CPU 환경 관측은 원본 범위에서 보존한다.

Summary-v1/v2는 표시용 dictionary projection이다. 정밀 값/null/zero와 관계를 복원하고
원래 measurement 정책을 바꾸지 않는다. Decoder는 응답당 2,000,000 logical nodes와
깊이 64를 제한한다. v2의 문자열 연결은 8Mi UTF-16 단위 상한이며 앞선 원문 문자열만
참조한다. 지원 협상 거절 때만 기존 v1로 전환하며 중간 페이지를 중복 합치지 않는다.

목록은 실제 nextOffset을 따라가고 실패에는 직전 완전한 자료와 draft를 보존한다.
요청 실행 실패와 result 파일 오류는 독립이다. Passport 202는 대기이며 null을 0으로
보이지 않는다. 목표 총수가 없으면 진행률/ETA를 만들지 않는다.

DOM은 frozen spec의 closed-loop burst다. Scalar d 부재를 실패로 만들거나 포화 판정을
복원하지 않는다. Signed 관측 median/IQR/분위수/negativeFraction과 실제 scheduler의
채택 d/provenance를 구별한다. Runtime image를 포함한 현재 identity/source로 연결한다.
기간 상한은 서버 값을 따르며 400에 원 code와 직전 적용 기간을 보존한다.

Capacity는 현재 입력/설정과 계산된 μ/κ, grid/bisection 원 순서를 표시한다.
단위 변환만 하고 브라우저에서 재계산하지 않는다. Pending null과 Built zero는 다르다.
Operation 결과의 단계 수를 채택률로 번역하지 않으며 상태/근거의 미매칭도 남긴다.

조건별 raw export, standalone Run 생성, 추가 관리 API가 현재 handler에 없으면
미구현으로 표시한다. SRS 의무와 현재 capability를 구별하며 가짜 endpoint를 만들지 않는다.
범위/품질은 [부재와 오류 상태 구분](../../spec/dashboard/SRS.md#부재와-오류-상태-구분)을 따른다.

## 실험과 과거 기록

실험은 [실행 설계](../profiling/experiment-execution.md)와
[실험 정의 기반 정책과 SLO](../../spec/dashboard/SRS.md#실험-정의-기반-정책과-slo)를 따른다.
절대 rate, 도착/기간, uint64 문자열 seed와 Dataset name/UID를 작성한다. 내부 κ/d,
시작 상태 복원이나 target image/profile 대조를 입력으로 요구하지 않는다.
공개 settingsPatch는 공통 설정 항목을 사용하고 최종 값 검증은 서버가 한다.

실행 생성/preflight target은 삭제/terminal Pod를 제외한 Ready와 observed imageID다.
이는 서버 application의 advisory 확인이며 하네스의 모든 실행 check와 동일하다고
일반화하지 않는다. LiveTarget annotation 결손은 미기록이다.

비교는 선언 comparisons와 동결 definition의 stage/Workload UID를 사용한다.
이름/요청률로 정책/같은 부하를 추정하지 않는다. Facts의 settingsPatchKeys는 키뿐이며
빈 배열은 변경 없음, null/누락은 미기록이다. 상세 patch 값은 definition에 남는다.
목록 항목마다 detail을 전수 hydrate하지 않는다. 명시 과거 대상이 목록에 없을 때만
필요한 불변 단건을 조회한다.

정의 없는 게시도 절대 KPI를 보여준다. 관계/조건 미확인은 참고 비교로 제한한다.
차이는 후보−기준, 성공률은 %p, p95는 배율, 에너지는 J/%다. 기준 zero와 상대 계산
불가를 구별한다. 분자/분모/모집단/표본과 coverage 오차를 유지한다.
단계 KPI를 초별 보간하거나 하위 구간 에너지/절대 경계를 추정하지 않는다.

실행/측정/정리는 독립 축이다. Definition decoding/준비 실패에도 게시 outcome을 남긴다.
URL UID가 조회 UID와 다르면 수정/중지/재실행/반환을 차단하고 명시 신원 채택을 요구한다.
반환 요청은 실제 반환이 아니다. 현재 권한은 실제 403 근거 없이 없음으로 단정하지 않는다.

작성/수정은 전용 페이지, 단계 identity는 배열 위치와 구별한다. Dataset 선택은 name/UID를
함께 고정한다. 미저장 입력 경고와 닫힌 Sheet의 지연 callback 차단을 유지한다.
복사는 알려진 폐기 필드만 제외해 공개하며 지원 확장필드/정밀 JSON을 조용히 버리지 않는다.

게시 선택/구간/서비스/offset은 URL로 보존한다. Sheet 닫기는 선택 identity를 지우지
않는다. 기록은 원래 API 구간/페이지/coverage와 당시 정의를 사용하며 최신 상태로
과거 결손을 채우지 않는다. Reader 준비 뒤 publisher를 활성화한다.
