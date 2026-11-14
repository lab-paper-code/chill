# CHILL Query API

`cmd/chill-query`는 운영과 측정 조회 backend다. Dashboard는 표시와 입력을,
ChillServer는 공개 인증과 권한 및 serving 제어를 소유한다.

| 정본 | 내용 |
|---|---|
| [운영과 CLI](03-dashboard-and-cli.md) | 요청, 상태, 증거 조회와 변경 안전 |
| [Dashboard 요구사항](../../spec/dashboard/SRS.md) | 제품 범위와 수용 기준 |
| [Dashboard 구현](../dashboard/README.md) | 웹, BFF와 라이브 표시 |
| [시스템 명세](../../spec/README.md) | 제어와 관측 의미 |

## 조회 소유권

Query는 profile, coverage, 요청 목록, 결과, preflight와 Evidence Passport를
완성한다. pending과 evidence 오류를 구분하며 native metadata/spec/status를
catalog 장애와 독립적으로 보존한다.

ChillServer 중계는 원 사용자 bearer, TokenReview/SAR, namespace, 전달과 transport
오류를 처리한다. Query도 직접 접근에서 TokenReview와 URL/native SAR을 검증한다.
중계가 backend 장애를 측정 실패로 바꾸거나 Kubernetes로 본문을 수선하지 않는다.
기존 `ConsoleUnavailable`/`ConsoleTimeout`과 wire API version은 공개 계약이다.

Preflight는 요청이 세션으로 풀리는지와 계획한 배치 크기, 최소 표본 수, SLO 분위수를
답한다. 세션은 대상 Node에서 직접 측정하므로 카탈로그나 요청 겹침을 보고하지 않는다.
`internal/queryapi/**`는 Serving을 import하지 않으며 조립점이 catalog를 변환한다.

## Snapshot과 실행 조건

Serving `catalog-read`는 새 catalog/poller 없이 scheduler catalog를 투영한다.
공개 profile 평가는 채택 시점의 `accepted` profile, facts, preparation과
configVersion을 함께 사용한다. Coverage는 최신 reporting snapshot을 사용한다.
동일 profile의 두 시점 facts를 합치거나 결손을 서로 보충하지 않는다.

표시 요약은 bounded 입력만 직렬화한다. 원시 duration 배열과 전체 내부 facts를
중간 HTTP에 싣지 않는다. namespace cache는 30초 fresh, 최대 5분 stale이다.
분리 프로세스만으로 serving 간섭 부재나 성능 수용을 주장하지 않는다.

Profile variant는 Serving과 같은
`profilederivation.PerformanceCondition`을 쓴다. Runtime image/version과 실제
artifact/preprocessor, 입력 집단, Node/backend, memory/power 조건은 신원에 남는다.
Observer/window/meter, warmup/초기화 영역과 수집 절차는 별도 provenance로 보존한다.
실제 RuntimeProvider는 관측으로 남지만 이 semantic key에서는 정규화된다.
실행 전 모르는 version/provider를 이미지에서 추정하지 않는다.

BEO result v1alpha6가 현재 단일 저장 계약이다. CPU allocation 필터는 없고 실제 CPU
환경은 관측이다.

## 구현 경계

- `internal/queryapi`: typed read model, coverage/variant/summary와 evidence 해석.
- `internal/queryapi/kube`: cache, 병렬 refresh, native 조회와 bounded retained read.
- `internal/queryapi/readapi`: query/pagination envelope와 JSON 전송.
- `cmd/chill-query`: 구성, owner-client와 Serving DTO 변환.

접수와 evidence/측정 완료는 구별한다. Passport HTTP 202는 pending metadata와
null measurement/evidence를 보존하며 table도 미완료 값을 0으로 표시하지 않는다.
