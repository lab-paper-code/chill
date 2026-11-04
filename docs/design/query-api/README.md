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
- retained BEO 통계는 `internal/profiling/campaign/evidence.Reader.LoadPlan`·`ReadTrials`로 frozen plan·completed Run·evidence 신원을 공통 검증한다. Query는 Serving 상태 수용을 요구하는 `ReadPlan` 대신 `LoadPlan`을 사용하며, 통계·point policy·quality facts·빈 입력 판정은 계속 Query가 소유한다. `query-api` → `campaign-evidence` 의존만 허용하고 `internal/serving` 의존은 추가하지 않는다. Query adapter는 owner Kind/APIVersion 검사, missing Run과 retained Job/Secret 오류 구분, 기존 HTTP reason 매핑을 보존한다.
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
- Profile variants and preflight measured-batch matching use the same `profilederivation.PerformanceCondition` as Serving. Runtime exact image, observer/window/meter and stop-policy changes preserve identity; input-population, artifact/preprocessor recipe (including their images), Node/backend/runtime-package, actor CPU/thread/memory/power and initialized/warmup execution changes do not. Raw conditions retain all provenance. Current failed/partial measurement selection, replacement lineage and reuse confirmation remain distinct from this success-condition grouping; collection changes never revive a replaced sibling. CurvePlan v1alpha11 and BEO result v1alpha5 are strict single-version stored contracts; old source evidence is unavailable until regenerated, never silently promoted to a new profile.
- Completed profile/reporting conditions additionally carry `runtimeVersion` and `runtimeProvider` from Loader-verified active runtime facts; the shared key retains them even when an ORT declaration has no package-version entry. The frozen pre-execution plan leaves these observations unknown. Preflight cannot claim exact measured-condition identity for an unknown runtime version/provider; it does not infer either from an image reference or add an image-equality admission gate.

## Go 구현 소유권 (2026-11-04)

- `internal/queryapi`는 Query 소유 catalog/profile/occupancy 입력으로 coverage cell·variant·현재 측정 선택·summary·checklist compaction을 조립한다. 요청/profile/operation/occupancy evidence의 판정도 순수 함수로 소유하며 저장된 UID·동결 조건·signed 수치·nil/0 의미를 바꾸지 않는다.
- 기존 `internal/queryapi/kube`는 namespace coverage cache·병렬 source refresh·실패 보존·native measurement 조회·bounded evidence 읽기와 preflight/result selection use case를 소유한다. `CatalogSource.Read`는 coverage에 필요한 Query 소유 읽기 타입만 반환한다. namespace 권한 callback, 30초 fresh·5분 stale, 단일 namespace refresh의 락·deadline·실패 보존 순서는 이전과 같다.
- 기존 `internal/queryapi/readapi`는 measurement query 해석·pagination envelope·JSON encoding을 소유한다. 목록용 `Page` DTO를 공유하며 옛 함수·re-export·미사용 pagination 상수는 남기지 않는다.
- `cmd/chill-query`는 구성·조립·namespace/HTTP 오류 및 serving 응답의 어댑터만 소유한다. serving catalog 클라이언트의 필요한 필드를 `CoverageCatalogRead`로 변환하고, profile/operation/occupancy HTTP envelope를 기존 계약으로 변환한다. occupancy source decoder 포트는 원 serving record를 한 번 완전히 디코딩한 뒤 판정에 필요한 필드만 반환하므로 사용하지 않는 필드의 잘못된 타입도 이전처럼 거부한다. serving 타입 전체를 복제하지 않으며 `internal/queryapi/**`는 serving을 import하지 않는다. `.go-arch-lint.yml` 의존 방향도 그대로다.
- campaign statistics evidence 조회인 `cmd/chill-query/measurement_statistics.go`는 별도 campaign evidence Reader 계약 cutover인 #4에 속하므로 이번 이동에서 제외한다. query key·pagination·응답 크기·HTTP 계약은 유지하며 cache/판정 회귀는 내부 소유 패키지, serving 변환의 JSON·값 동일성 회귀는 composition root에서 검증한다.

