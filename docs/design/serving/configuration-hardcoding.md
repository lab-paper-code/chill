# 하드코딩 정리: 설정 소유권과 기록 경로

상태: 구현 전 설계(2026-10-30). **2차 검토(fusion: 패널 3모델+판정) 반영**. 구현·측정 완료가 아니다.
[계획](../../plans/2026-10-30-configuration-hardcoding.md) · [인벤토리](../../plans/2026-10-30-configuration-hardcoding-inventory.md) · [ADR 0031](../../adr/0031-configuration-ownership-and-freezing.md)

## 1. 경계와 결정

A는 측정·판정 소유자의 기존 입력, B는 실제 환경 입력으로 옮긴다. C는 고정 방법론·프로토콜·패키징 계약, D는 이미 설정된 값이다. 자명한 수식·sentinel·Helm enum까지 이름 붙이지 않는다. 새 설정 서비스·저장소·의존성은 없다. Dashboard UI 재설계는 제외하지만 typed API round-trip은 포함한다. `demo.py`는 offline 회귀가 import하므로 회귀 유지 대상이다. 역사 script와 `numbers.py` 비활성 경로는 제외하며 파일 삭제 승인은 아니다.

## 2. settings 증거와 실패 해석

판단 당시 적용값의 권위는 기존 `Decision.Settings`다. PUT의 사전 append → ConfigMap 저장 → full refresh → 후속 append를 유지한다. 초기·외부 ConfigMap 값은 그 값을 쓴 Decision의 전체 settings로 해석한다. refresh 합성 변경 행, 초기 before=after 가짜 변경 행, 별도 provenance 저장소는 만들지 않는다. 첫 판단 전 모든 적용 이벤트의 완전 복원을 요구하지 않는다. ConfigMap 출처는 실제 보존된 참조·binding 범위에서만 주장하며 과거 UID/RV를 꾸미지 않는다.

| 상태 | 해석 |
|---|---|
| 사전 append 실패 | ConfigMap 변경 전 중단 |
| 저장 성공·refresh 실패 | 저장 요청과 현재 적용값이 다를 수 있음 |
| refresh 성공·후속 append 실패 | HTTP 오류가 미적용을 뜻하지 않음; rollback을 주장하지 않음 |
| 이후 refresh 적용 | 당시 Decision의 실제 settings로 해석 |
| 적용 증거 부족 | 성공·실패 추정 없이 미확인 |

## 3. serving settings와 요청별 reuse

### 3-1. 추가 settings는 3개

기존 필드는 유지하며 이번 **추가 개수만 3개**다. `SchedulerSettingsInput` 한 경로에서 생략 기본값을 채운다. GET·Decision·before/after에는 실제값을 담고 범위 오류·NaN/Inf는 적용 전에 거절한다.

| JSON 필드 | 범위 | 생략 기본값 | 소비·동결 |
|---|---|---:|---|
| capacityGridSteps | 정수 ≥1, Go 표현 범위 내 | 20 | 격자 전체 순회·격자율 → CapacityInput → 용량 행 |
| capacityBisectionWidth | finite (0,1] | 0.005 | width × μ 종료 조건 → CapacityInput → 용량 행 |
| capacityTailSamples | 정수 ≥1, Go 표현 범위 내 | 100 | ceil(tailSamples/(1−p/100)); p=100 현재 분기 유지 → CapacityInput → 용량 행 |

기본값을 명시해도 입력 JSON SHA-256 기반 κ 키는 바뀐다. 옛 키 cache miss와 재계산은 의도된 cutover다. archive의 키 없는 조회는 넓게 탐색할 수 있어 초기 재계산 비용을 인계한다. `omitempty`, 옛 입력 분기, dual-key lookup, 새 캐시 복구 장치는 만들지 않는다. 과거 raw fixture는 원본 보존하고 새 계약 테스트 입력과 구별한다. bootstrap raw replay의 fixture loader도 이 구별에 맞춘다. `analytic-planner.md`의 고정 수식·코드 상수 설명도 함께 갱신한다.

필수 소비자: `static_routing.go:162-168`, `hack/kappa/main.go:89-104` 직접 decode와 `:61-63`의 CapacityMinimumRequests=1000 덮어쓰기, `hack/e5/d2/bind_before.py:24-25,278-283`의 SETTINGS/CapacityInput 대조, Dashboard typed settings·round-trip·fixture, SchedulerSettings 구조체 테스트와 샘플 JSON. 각 호출자에서 새 기본값을 발명하지 않는다. `CPO.put`은 읽기 전용 필드를 빼는 경로이지 GET→PUT whitelist가 아니다.

### 3-2. 신뢰수준은 측정별 단일 정책

`BEO.spec.pointPolicy.confidenceLevel`은 기존 idle 정책과 같은 (0,1) 십진 문자열이며 생략 기본값은 `"0.95"`다. campaign point, serving catalog idle baseline, incremental energy CI에 같은 선택값을 사용한다. 독립 `ModelLoadedIdleBaseline.spec.pointPolicy.confidenceLevel`은 그 측정 CR의 기존 소유권을 유지한다. 전역 CI 하나로 묶는다는 뜻이 아니다. `Profile.ConfidenceLevel`은 실제 유도값을 기록하고 catalog의 `95%% CI` 메시지도 선택값을 출력한다.

`energyConfidenceLevel`·`profileConfidenceLevel` settings는 추가하지 않는다. `--profile-confidence-level`, `servingEntrypoint.profileConfidenceLevel`, 옵션·생성자 인자·템플릿은 삭제하며 별칭·fallback은 없다. 기존 immutable BEO spec과 실행·결과 참조를 재사용하고 별도 snapshot 저장물을 만들지 않는다. 현재 CLI는 metadata에만 남고 baseline과 Aggregate CI가 다르면 정적 경로상 오류 조건이 있으므로 두 CI를 독립 정책으로 연결하지 않는다.

### 3-3. 재사용 정책은 요청 annotation

기존 `chill.dacs.io/profile-reuse` JSON의 sourceProfileID·maximumRelativeDifference 옆에 `minimumMeasurementSeconds`(finite >0, 기본30), `minimumTrials`(정수 ≥2, 기본3)를 추가한다. 창은 active와 idle 각각의 최소값이다. `ProfileReuseCheck`에는 선택 최소값과 실제 active/idle 길이·trial 수를 기록한다. 기존 `reusedProfile` 정책 identity에도 두 값을 넣어 annotation 변경이 옛 immutable startup bundle을 재사용하지 않게 한다. 전역 settings 변경으로 기존 요청 정책을 재정의하지 않는다. trials=2 수용은 §5의 종단 하한 정리와 함께 완료한다.

### 3-4. 유지할 설정과 방법론

power 앞뒤 1분 support는 coverage 상한에 맞는 C다. 새 settings나 Decision 필드는 없다. `powerObservation.sampling/coverage`와 읽기 전용 meters는 기존 소유자 그대로다. source/profile interval은 기동 설정이다.

## 4. 기존 기동 기록 최소 보강

기존 `cluster` stream의 `event:"server-start"` 행을 재사용한다. Pod UID/namespace/name은 기존 필드, process 시작 시각은 `run()`의 실제 `started`다. 추가할 측정 관련 값은 `queueLimit`=1024, `sourceIntervalNanos`=1000000000, `profileIntervalNanos`=60000000000뿐이다. 운영 overlay source2초를 선택하면 2000000000을 기록한다. 기존 append 실패→기동 실패를 유지한다. 새 scheduler startup 행은 없고 scheduler 비활성 때도 별도 행을 만들지 않는다.

request/drain timeout, directory interval, control component scope는 기존 관리·수집 설정 D다. CLI request30/drain120초, Helm129/164초를 통합하지 않는다. 특히 infer handler의 별도129초 때문에 requestTimeout을 모든 handler의 실제 timeout이라고 부르지 않는다.

Node sample/batch cadence와 transitionObserver.activeDeadlineOverheadSeconds=180은 D다. ResourceBatch/resources에 cadence 필드를 신설하지 않고 존재가 확인되지 않은 attempt frozen 입력에도 overhead를 복사하지 않는다. 기존 Job ActiveDeadlineSeconds를 유지한다. sampledAt/receivedAt은 관측 시각이며 선택 cadence·누락 표본을 완전히 설명하지 못한다. cadence 출처는 배포 설정이다.

`cmd/chill-server/live.go:64-80`의 1초 ticker는 resources 저장도 구동한다. 표시용 cadence와 구별하여 증거 API 설계의 고정1초 수집 방법론 C로 유지한다. 별도 loop/settings는 없다.

## 5. 측정 CR와 불변 실행

필수 입력·추천 실험값·코드 fallback·admission default를 구별한다. 기존 immutable spec/실행·결과 참조로 선택값을 연결하며 중복 snapshot 저장물을 만들지 않는다.

| 소유자 | 입력·범위 | 현재값/소비 |
|---|---|---|
| ProfilingRun.spec.measurement.durationToleranceSeconds | finite ≥0, 해당 duration 미만 | 0.5초; BEO active/idle protocol과 독립 idle protocol에서 전달하여 evidence 길이 판정 |
| BEO.spec.pointPolicy.confidenceLevel | (0,1) 십진 문자열 | 생략 "0.95"; §3-2의 단일 유도·CI |
| 기존 BEO.spec.trialsPerBatch | 정수 ≥2 | 현재 실험3 유지; 필수 입력이며 현재 default marker가 있다는 뜻 아님. 새 minimumRuns 없음 |
| 독립 idle.spec.pointPolicy.minimumRuns | 정수 ≥2 | 기존 필드 재사용, 현재3 유지 |
| 전이.spec.acceptedCountTarget | 정수 ≥2, ≤attemptLimit | 현재 목표5 유지, 방향별 적용 |
| occupancy.spec.durationSeconds/rateFactor | 길이 양수; open-loop rate finite >1 | 기존60초/1.2 입력·생성값 유지; minimumDurationSeconds/minimumRateFactor 없음. closed-loop에 open-loop 하한 미적용 |
| occupancy.spec.seed | uint64 | 현재0; 생성기 인자·실행 기록 |

하한2 지원은 비기본 입력 허용 범위 확대이며 현재도 지원된다는 주장이 아니다. BEO·idle minimum/attempt limit, 전이 target/attempt limit을 함께 맞추고 attemptLimit≥minimum/target을 유지한다. 숨은 <3·<5·자유도<2가 남으면 미완료다. 통계 구조 검증은 필요한 신뢰 경계에서 유지하되 양의 자유도라는 수학적 최소와 정책 목표3/5를 구별한다. 새 근사나 무효 fallback은 없다. 비영분산 active2/idle2의 유도→저장→재로딩→serving까지 수용한다.

종단 수정 위치는 인벤토리에 있다. PCG는 고정한 두 번째 seed word이며 stream selector라고 부르지 않는다. 양측 CI /2, n−1 같은 수식은 설정화하지 않는다.

## 6. 하네스: bind에서 한 번 선택

증거 API cutover 후 잔존 실행 경로만 대상으로 한다. effective config를 `binding.config`에 보존하고 실행·분석은 이를 소비한다. binding은 일반 JSON 파일이므로 물리적 write-once가 아니다. 완결 후 수정 금지, 새 실험 새 binding 계약이다. 환경 JSON을 실행 중 다시 읽지 않는다.

| 키/기존 표현 | 단위·범위 | 실행·분석 소비자 |
|---|---|---|
| stages[].id/seconds/alpha/parts | 기존 example 표현; id 식별자, seconds finite >0, alpha finite ≥0, parts도 기존 구조 유지 | bind schedule 생성→producer stage/part rates·offset→analyze 단계 모집단 |
| workloads별 seed/arrivals | uint64; 지원 도착 방식 | trace/producer 생성→binding seed 및 check 대조 |
| 기존 generatorQuality | lateness 시간 ≥0, fraction [0,1] | generator 원시 품질 집계→analyze 수용 |
| g0Evidence.durationSeconds/alpha/maximumCPUFraction | finite >0 / ≥0 / (0,1] | one-shot G-check 실행 입력→선택 입력과 evidence 대조; stageCount 없음 |
| 기존 workload SLO percentile | (0,100] | 요청 지연 원시에서 선택 percentile 계산→G0/분석 수용; p95 필드 이름만 바꾸지 않음 |
| inputs.maximumIdleNanosPerRequest | finite ≥0, ns/request | 입력 closure 검증→분석 eligibility |
| failureInjectionRunbook | 기존 문서 구조 | 입력 config 우선, 누락 때만 example 기본 선택→binding 보존→운영자 주입·분석 |

closure는 선언 target 목록 길이와 실제 모집단을 비교한다. expectedClosureCount는 없다. full/static/factor 시간은 schedule/선택 N에서 유도한다. `e5.py check:2610-2614`의11700·10200·seed 집합·21900도 binding schedule/seed에서 유도한다. 기준은 example만이 아니라 현재 실행 함수의 schedule·fallback이다.

DELAY_TOLERANCE_NS=10ms는 delayed 표시 방법론 C다. --delay-tolerance나 존재하지 않는 단독 CLI metadata를 신설하지 않는다. 기존 arrivals/seed/workers/delivery/socket/lifetime와 generatorQuality를 동결한다. poll·marker poll·종료 대기·단순 command timeout은 측정·수용 영향 근거가 없으면 C, 기존 안전 예산은 D다. 경로별30/129초·1/2/5초를 합치지 않는다. 삭제할 CHILL host-reader/SSH/crictl 수집은 설정화하지 않는다. client 수집·STOP loop·운영자 장애 주입은 유지한다.

`demo.py` offline 회귀와 실제 E5가 호출한 bind_before SETTINGS/eligibility, Conductor API·publication·producer·삭제, native 요청, analyze stats/quality는 유지한다. 독립 D2 main·옛 수집·numbers.py 비활성 경로는 제외하며 helper 전체를 삭제하지 않는다.

## 7. B 환경값 연결

실제 registry·Node/runner·외부 endpoint와 잔존 실행 namespace만 B다. 이미 values에 있는 repository·region·장비 catalog·signature·mode·clock은 D다. /proc·/sys·SA token·내부 TLS/data/evidence mount는 C 또는 기존 D, 내부8080/9000과 이미지 UID/GID65532는 패키징 C다. example-*·repo-relative echo·artifact 내부 경로는 예제/산출물 계약으로 유지한다. 기본 values를 비우지 않는다. 현재 testbed discovery profile과 chart fail 조건을 별도 재설계하지 않는다.

각 행의 이동처는 하나다. 배포값은 기존 해당 Helm values 키, 잔존 E5/helper namespace는 `CHILL_EXPERIMENT_CONFIG.d2.namespace`→bind 시 `binding.config.namespace`다. `e5.py:1316,1519`, `e5-wire.py:105`를 포함한다. 역사 조회2533 등은 cutover 뒤 잔존 여부로 제외한다.

실제 registry·Node를 가진 운영 샘플은 환경 JSON `samples["<기존 상대 파일 경로>"]`에 완전한 입력 문서로 보관하고 기존 등록/측정 요청 절차에서 선택한다. 정적 YAML의 JSON 참조 자동 해석은 없다. immutable runtime declaration은 image와 inspection 사실을 묶은 문서 전체를 이동한다. 주소만 바꿔 같은 검증 사실처럼 꾸미지 않는다.

## 8. 구현 슬라이스와 시작 조건

증거 API 승인 배포·1회 전환 리허설·CHILL 우회 수집 제거를 포함한 하네스 cutover·대시보드 브라우저 수용이 기록으로 완료되고, 의존 측정 캠페인이 없을 때 시작한다. rev106 배포 시도는 완료 조건이 아니다. 후속 rev110/결손 기록도 live 확인이나 전체 PASS로 바꾸어 읽지 않는다.

| 순서 | 소유 범위 | 시작/완료 경계 |
|---|---|---|
| 1 | 전환 후 인벤토리 재산정·인터페이스 확정 | 위 시작 조건 충족; 삭제된 host/SSH/crictl 경로 제거, 잔존 행 확정 |
| 2 | 측정 정책 | 1 뒤; 단일 CI·CR 입력·하한2·결과 저장/재로딩/serving 소비 완결 |
| 3 | serving 용량 settings·요청 reuse | 2의 정책 확정 뒤; typed caller·CapacityInput·캐시·reuse identity 연결 |
| 4 | 기존 기록 최소 보강 | 3 뒤; cluster/server-start와 settings 실패 의미 정리, 새 stream 없음 |
| 5 | 잔존 하네스 A/B | 2~4 계약 확정 뒤; binding→실행→분석과 단일 환경 이동처 완결 |
| 6 | 통합 소유자 단일 검증·인계 | 전체 변경 뒤 한 번 검사; 이 문서 세션은 실행하지 않음 |

C 정리는 관련 변경에 묶고 독립35행 선행 관문으로 만들지 않는다. 같은 파일은 한 소유자, 저장 계약 변경은 캠페인 사이 한 번 모은다.

### 저장 계약

ADR0030에 따라 prefix/version v1alpha5를 유지한다. 새 stream·관측 디렉터리 cutover는 없다. archive/publication #213~#225는 수정·삭제·재게시하지 않는다. 과거 행의 새 필드 부재는 당시 기록되지 않은 사실이며 현재 기본값으로 소급 채우지 않는다. 새 CapacityInput은 새 κ 키를 쓴다. ADR0019의 정리는 읽을 수 없어진 영향 작업용 CR/파생 객체를 열거해 운영자가 수행하며 과학 원시 archive와 구별한다. reader는 단일 버전, 과거 필드 부재의 의미 규칙만 명시한다. dual decoder/migration/alias는 없다.

### 기본값=현재값 증명 (다음 구현 세션의 완료 기준)

| 영역 | 변경 전 기준과 비교 |
|---|---|
| settings | 현재20/.005/100과 기존 fixture 실제 계산을 생략·명시 기본값 각각과 비교; 새 두 경로끼리만 비교하지 않음 |
| κ | TestCapacityRowKappaIsFixedAndRepeatable: μ1000·κ959.375·Grid/Bisection/판정 유지. 키 동일성 요구 없음 |
| CI | 현재 .95 단일 경로의 baseline 평균·분산·자유도·CI·음수 energy 거절/해상도 처리와 실제 계산 비교 |
| reuse | 현재30초/3회/상대 차이 기본 수락·거절 유지; 비기본 정책의 판정·identity 변화 |
| CR | 현재 trial3/target5 결과 유지 + 비영분산 active2/idle2 유도→저장→재로딩→serving |
| 하네스 | 현재 실행 함수 schedule·seed·fallback의 stage/part 시간·rates·offset·offered·full/static/factor 시간·판정 |
| timeout/poll | 각 경로30/129초·1/2/5초 유지 |
| 배포 | 승인 overlay 경로·revision·release·namespace를 구현 착수 기록에 지정하고 default/profiler 별도 render의 의미 있는 resource/field delta 비교. values-operating.yaml은 rev74 캡처이지 현재 배포값 아님 |

Go/API/Helm 및 helper offline 회귀는 통합 소유자가 마지막에 한 번 수행한다. 바이트/hash 동등성 관문은 없다. live 동일성은 별도 증거 없이는 주장하지 않는다.

## 9. 열린 점

기존 ?10행은 자동 API 승격하지 않는다. overload1초/.9, occupancy cap64/64MiB, ready/drain/actuation/admission poll, 미확인 장치1Gi, disk310GB, probe/emptyDir/single-writer 배치가 해당한다. 기본 동작을 유지하며 안전 경계와 측정 입력의 실제 관계로 별도 결정한다.
