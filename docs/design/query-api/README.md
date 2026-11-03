# CHILL Query API

상태: **active 설계**. 독립 `cmd/chill-query` / `chill-query`는 운영·측정 조회 backend다. Dashboard는 표시·입력, ChillServer는 공개 인증·권한과 serving 제어를 소유한다.

| 문서 | 내용·상태 |
|---|---|
| [운영 의미와 현재 단면](03-dashboard-and-cli.md) | active — 역할·운영 흐름/ID 추적, 현재 measurement/profile CLI 계약, ProfilingRun 조회·요청·증거 계보 경계 |
| [Dashboard SRS](../../spec/dashboard/SRS.md) | active — 웹 제품 범위·기능·안전·수용 요구사항의 권위 원본 |
| [Dashboard 구현 설계](../dashboard/README.md) | active — 웹 기술·화면·API 소유권과 구현 준비도 |
| [시스템 명세](../../spec/README.md) | active — 알고리즘·제어·SLO 관측 규칙의 권위 원본 |

규범 목록을 재정의하지 않는다. 명세에 확정되지 않은 Observe/Supervised/Autonomous, T1/T2/T3·D1/D2, 중단 규칙은 설계 의도이지 필수 제품 계약이 아니다.
Dashboard·CLI는 같은 권한·검증·비동기 결과 의미를 사용하되 각 현재 capability를 사실대로 표시한다. 첫 ProfilingRun 단면은 전체 Dashboard 요구사항이나 별도 BEO CLI 계약을 대체하지 않는다.

## 조회 소유 경계 (2026-11-02)

- Query API가 profile·coverage·요청 목록·측정 결과·preflight·Evidence Passport의 정상/pending/evidence 오류 응답을 완성한다. 기존 도메인 판정과 verified evidence reader를 재사용한다.
- CLI runs show는 Query의 EvidencePassportState를 공유해 HTTP 202의 pending code·native Run metadata와 evidence/measurement의 null을 JSON에 보존한다. table은 pending 상태를 표시하고 미완료 측정을 0으로 출력하지 않는다.
- BEO 목록의 native metadata/spec/status는 catalog 의존성 실패와 독립적으로 반환한다. owner 미설정·transport·configuration 오류는 evidence를 승인하지 않는 unavailable assessment reason으로 표시하며 native 사실이나 실행 실패를 지우지 않는다.
- ChillServer의 조회 중계는 TokenReview/SAR·namespace 제한·전달·transport 오류만 처리한다. backend 장애를 pending/측정 실패로 바꾸거나 Kubernetes를 재조회해 본문을 수선하지 않는다.
- Serving의 인증된 `GET /api/v1/scheduler/catalog-read`는 `serving/kube.CatalogRead`만 제공한다. 공유 scheduler/catalog의 조회 projection이며 새 catalog·poller·장부가 아니다. 공개 profiles 경로와 달라 순환 중계가 없다.
- `ProfileSummary`와 bounded reporting input은 공개 필드/요약값만 읽는다. 원시 duration 배열·전체 내부 measurementFacts는 어느 HTTP 중간에도 직렬화하지 않는다. preparations의 restore 필드는 typed instance/profile 요약으로 보존하고 scheduler 원본은 변경하지 않는다.
- catalog-read는 최신 verified reporting snapshot과 독립된 `accepted` snapshot을 운반한다. `accepted`에는 채택된 profile 본문·configVersion·Insufficiencies(PreparationUnavailable 포함)·reuse·preparations·해당 시점의 bounded reporting facts를 함께 보존한다. 공개 profiles의 기존 진단·resultRows·assessment는 accepted에서만 계산하고, coverage의 facts·source 진단은 최신 snapshot만 사용한다.
- 새 `accepted` 필드는 내부 owner→Query 전송에만 필요하며 공개 profiles 응답에는 새 필드를 추가하지 않는다. 단일 profile ID에 이전/최신 facts가 충돌하거나 어느 한쪽에서 빠져도 합치거나 fallback하지 않는다. BEO 목록의 채택된 요청 평가는 accepted facts를 쓰고, 미채택 요청의 retained evidence는 현재 reporting에서만 찾는다.
- 공개 오류 코드는 계약이므로 `ConsoleUnavailable`/`ConsoleTimeout`과 기존 `console.chill.dacs.io/v1alpha1` wire API version을 유지한다. 실행·배포·설정·Go 이름이나 alias는 아니다.
- Query API는 Service 직접 접근도 TokenReview·URL/native SAR로 검증한다. ChillServer는 원 사용자 bearer만 전달하고 identity header는 전달·신뢰하지 않는다. CLI runs show도 기존 ownerClient HTTPS·bearer 경로를 재사용한다. native detail은 이름이 `preflight`여도 `get NAME` SAR을 요구하며 `list` 예외는 POST campaign preflight 경로에만 적용한다.
- namespace coverage cache는 기존 30초 fresh/최대 5분 stale 정책을 그대로 Query API에 둔다. 큰 조회의 CPU/RSS/응답 크기와 serving 영향은 live 성능 수용이 필요하며 프로세스 분리만으로 간섭 부재를 주장하지 않는다.
- 공유 CPU cutover 뒤 ProfilingRun 목록과 profile 목록은 CPU quota/request/affinity 필터·정렬 입력을 받지 않는다. `cpu` 쿼리는 InvalidQuery이며 CPU 환경은 실행 관측으로만 보존한다. `cmd/chill`에는 해당 CPU 필터 플래그가 없다. Query evidence 검증은 Node·mode·dataset/protocol·메모리/GPU·원시 신원을 유지하고 삭제된 frozen CPU 계약을 다시 요구하지 않는다.

## Go 구현 소유권 (2026-11-04)

- `internal/queryapi`는 serving 타입과 무관한 cell·variant 생성, 요청/run 소유 UID 연결, 측정 시각, summary·checklist compaction을 소유한다. 입력은 기존 query DTO·native CR·source 실패 map이며 serving DTO를 복제하지 않는다.
- 기존 `internal/queryapi/kube`는 native BEO 목록 조회·빈 목록 초기화·정렬을 소유한다. composition root의 namespace 권한 판정 callback을 먼저 실행하므로 기존 금지 오류와 HTTP 의미를 유지한다.
- 기존 `internal/queryapi/readapi`는 measurement query 해석·pagination envelope·JSON encoding을 소유한다. 기존 목록용 `Page` DTO를 공유하며 옛 함수·re-export는 남기지 않는다.
- **경계상 cmd 어댑터로 유지:** serving catalog-read·profile/operation/occupancy record 변환과 판정, serving 의존 coverage snapshot·cache/병렬 refresh·셀/variant source 연결, typed preflight/result adapter·route 등록, campaign statistics. `internal/queryapi`와 하위 패키지는 `internal/serving`을 import하지 않으며 `.go-arch-lint.yml`의 기존 의존 방향을 그대로 유지한다.
- coverage의 30초 refresh·5분 stale 허용, namespace 공유 cache/lock·deadline·실패 보존, query key·pagination·summary 크기 제한·HTTP 계약은 이동 전 의미를 유지한다. 순수 helper의 회귀는 구현 소유 패키지로 이동하고 serving 경계·HTTP·동시성 회귀는 composition root에 유지한다.

