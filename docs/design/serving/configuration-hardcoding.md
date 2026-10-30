# 하드코딩 정리: 설정 소유권과 기록 경로

상태: active contract (2026-10-30). 완료된 슬라이스2~5 절차는 제거했다. offline 회귀 완료와 배포·live 측정 수용은 다르며 아래는 설정 소유권·동결의 현행 계약이다.
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


### 3-2. 신뢰수준은 측정별 단일 정책

`BEO.spec.pointPolicy.confidenceLevel`은 기존 idle 정책과 같은 (0,1) 십진 문자열이며 생략 기본값은 `"0.95"`다. campaign point, serving catalog idle baseline, incremental energy CI에 같은 선택값을 사용한다. 독립 `ModelLoadedIdleBaseline.spec.pointPolicy.confidenceLevel`은 그 측정 CR의 기존 소유권을 유지한다. 전역 CI 하나로 묶는다는 뜻이 아니다. `Profile.ConfidenceLevel`은 실제 유도값을 기록하고 catalog의 `95%% CI` 메시지도 선택값을 출력한다.

`energyConfidenceLevel`/`profileConfidenceLevel` 전역 settings·CLI/values 별칭·fallback은 없다. immutable BEO spec/실행·결과 참조가 권위이며 baseline/Aggregate CI를 독립 정책으로 연결하지 않는다.

### 3-3. 재사용 정책은 요청 annotation

기존 `chill.dacs.io/profile-reuse` JSON의 sourceProfileID·maximumRelativeDifference 옆에 `minimumMeasurementSeconds`(finite >0, 기본30), `minimumTrials`(정수 ≥2, 기본3)를 추가한다. 창은 active와 idle 각각의 최소값이다. `ProfileReuseCheck`에는 선택 최소값과 실제 active/idle 길이·trial 수를 기록한다. 기존 `reusedProfile` 정책 identity에도 두 값을 넣어 annotation 변경이 옛 immutable startup bundle을 재사용하지 않게 한다. 전역 settings 변경으로 기존 요청 정책을 재정의하지 않는다. trials=2 수용은 §5의 종단 하한 정리와 함께 완료한다.


### 3-4. 유지할 설정과 방법론

power 앞뒤 1분 support는 coverage 상한에 맞는 C다. 새 settings나 Decision 필드는 없다. `powerObservation.sampling/coverage`와 읽기 전용 meters는 기존 소유자 그대로다. source/profile interval은 기동 설정이다.

## 4. 기존 기동 기록 최소 보강

기존 `cluster` stream의 `event:"server-start"` 행을 재사용한다. Pod UID/namespace/name은 기존 필드, process 시작 시각은 `run()`의 실제 `started`다. 추가할 측정 관련 값은 `queueLimit`=1024, `sourceIntervalNanos`=1000000000, `profileIntervalNanos`=60000000000뿐이다. 운영 overlay source2초를 선택하면 2000000000을 기록한다. 기존 append 실패→기동 실패를 유지한다. 새 scheduler startup 행은 없고 scheduler 비활성 때도 별도 행을 만들지 않는다.

startup 전용 값은 pointer/omitempty로 일반·과거 행의 부재를 보존한다. 적용 뒤 후속 append 실패가 rollback을 뜻하지 않는다.

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

하한2는 BEO/idle minimum·attempt limit, 전이 target/attempt limit, baseline 자유도1·결과 재로딩·serving까지 연결하며 attemptLimit≥minimum/target이다. 비영분산active2/idle2도 기존 통계로 처리하고 정책목표3/5와 수학적 양의자유도를 구분한다. durationTolerance는 Run 준비에서 생략.5초/명시0을 정규화하고 동결RunConfig·curve identity·Python 요청에 남긴다. occupancy seed는 Conditions·생성기 인자가 같으며 PCG 두 번째 word는 고정 seed이지 stream selector가 아니다. 양측CI/2·n−1은 설정화하지 않는다. 과거 raw fixture 보존·현재 helper 입력과 구분, 단일decoder를 유지한다. CRD/chart 사본과 배포 구CLI 제거는 함께 이행한다.


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

### 잔존 하네스의 선택값

잔존 인벤토리29행은 유지했고 cutover 후보12행은10제외/2C(support/hour·trapezoid)다. 아래 값은 config→binding.config로 한 번 고르며 e5-wire 승인입력/demo spec도 같은 키를 전달한다.

| 키 | 생략 기본값 | 소비자 |
|---|---|---|
| stages[].id/seconds/alpha/parts | 기존 `normative_stage_specs(burstLowSeconds)`의 8개 E5 row/12개 part: 600,1800,900,1800,900,1200,3600,900초; 기존 α 그대로 | bind → 연속 part trace/producer → judge; 명시 part ID 보존. demo의 필수 stages는 기존대로 |
| seeds | canonical MNV101/PLC202/Smol303 | bind trace·smoke·workload.seed·Job 이름/인자·check |
| arrivals | canonical workload별 poisson | bind trace·workload.arrivals·producer의 모든 segment; fixed도 허용 |
| g0Evidence.durationSeconds / alpha / maximumCPUFraction | 120 / 1.2 / 1 | one-shot G-check schedule·rates·엄격한 CPU fraction 상한 검증; demo bootstrap도 선택 duration/alpha 소비 |
| workloads[].slo.percentile | 기존 필수 Workload SLO 입력, 별도 덮어쓰기 없음(현재 PLC95) | G0는 client requestID로 조인한 raw `elapsedNanos`의 nearest-rank 선택 분위수, judge도 선택 SLO 소비 |
| inputs.maximumIdleNanosPerRequest | 1000000 ns/request | d25 closure 검증, `d25Acceptance` → 분석 eligibility |
| burstLowMaximumSeconds / collection.maximumDurationSeconds | 900 / 4200초 | burst 입력·각 schedule row의 setup+seconds+drain+flush 검사; 최장3600초 리터럴 제거 |
| timeboxes.maxActuationSeconds / nativeCallerSeconds / nativeFutureAllowanceSeconds | 900 / 960 / 15초 | native release/호출 timeout·alert freshness(900+15 유도), caller>actuation |
| producerResources / producerShards | cpu2,memory1Gi / MNV3,PLC1,Smol1 | producer requests/limits·process 분할 |
| poolShares | MNV.45,PLC.1,Smol.45 | workload별 intended/actual rate |
| failureSelectSnapshotTTLSeconds | 15초 | 장애 선택 snapshot age/expiry |
| tokenSafetyMarginSeconds | 60초 | bind·각 실행 token lifetime 사전 점검 |
| timeboxes.finishResetWaitSeconds / powerDownSeconds / factorAckWaitSeconds | 600 / 1800 / 120초 | zero-state reset 대기·reset HTTP 총 예산·복원 ACK |
| timeboxes.apiSeconds | 기존 E5 필수 입력 유지; 일반 정규화 fallback30초 | 각 경로 API 예산; 승인129초를30초로 바꾸지 않음 |
| collection.energyCoverage | maxGapFraction=.01,maxSingleGapSeconds=30 | 에너지 row gap 수용 |
| generatorQuality | latenessToleranceNS=50000000,maxExceedingFraction=.01 | 실제 issuance/품질 수용 |
| failureInjectionRunbook | 누락 때만 기존 example 문서 | 선택 문서를 config와 E5-7 row에 보존, 운영자 주입/분석 |
| workloads[].workers/deliverySeconds/socketSeconds/lifetimeSeconds | 기존 필수 workers/delivery/socket 유지; lifetime 생략 None | 기존 CLI 인자로 동결·재생; None은 request-lifetime 인자 미전달, CLI fallback 유지 |

`fullSeconds`, `staticSeconds`, `factorSeconds` 및 `check`의 시간/seed 대조는 binding schedule/config에서 유도한다. closure 모집단도 선언 target 목록과 비교한다. 새 closure count·G0 count·delay/poll 키는 없다. 현재 E5→실행 helper는 측정 CR을 생성하지 않으므로 슬라이스2 후속 CI/tolerance/occupancy seed 선택 전달 대상은 없다. 기존 운영 입력 sample은 §7의 완전 문서 선택을 따른다.

기존 실제 bind와 새 기본 bind의 stage/part 시간·rates·offset·offered/full11700/static10200/factor2100×N/payload/κ/settings를 offline 대조했다. E5/demo/report/collector self-check, load-generator32검사·serving/kube 검사 및 비기본 stage/fixed/seed7/G0(37초/.8/.75)/PLC p90·freshness 회귀가 근거다. 키/hash/바이트 동등성 관문은 없고 운영 전체 bind/E5·클러스터·SSH·배포는 미실행이다.


## 7. B 환경값 연결

실제 registry·Node/runner·외부 endpoint와 잔존 실행 namespace만 B다. 이미 values에 있는 repository·region·장비 catalog·signature·mode·clock은 D다. /proc·/sys·SA token·내부 TLS/data/evidence mount는 C 또는 기존 D, 내부8080/9000과 이미지 UID/GID65532는 패키징 C다. example-*·repo-relative echo·artifact 내부 경로는 예제/산출물 계약으로 유지한다. 기본 values를 비우지 않는다. 현재 testbed discovery profile과 chart fail 조건을 별도 재설계하지 않는다.

각 행의 이동처는 하나다. 배포값은 기존 해당 Helm values 키, 잔존 E5/helper namespace는 `CHILL_EXPERIMENT_CONFIG.d2.namespace`→bind 시 `binding.config.namespace`다. `e5.py:1316,1519`, `e5-wire.py:105`를 포함한다. 역사 조회2533 등은 cutover 뒤 잔존 여부로 제외한다.

실제 registry·Node를 가진 운영 샘플은 환경 JSON `samples["<기존 상대 파일 경로>"]`에 완전한 입력 문서로 보관하고 기존 등록/측정 요청 절차에서 선택한다. 정적 YAML의 JSON 참조 자동 해석은 없다. immutable runtime declaration은 image와 inspection 사실을 묶은 문서 전체를 이동한다. 주소만 바꿔 같은 검증 사실처럼 꾸미지 않는다.

구현은 catalog8·NVIDIA class/전이2·ARM runtime1의 원본 전체를 private `inputs/environment{,-212}.json` samples에 보존했다(총22문서 offline 필드대조/선택,정적YAML20회 파싱). 정적 예제는 example registry/hardware이며 검증 사실을 일반 image에 붙이지 않도록 runtime declaration 전체를 제거했다. `jq -e '.samples[$path] // error(...)'`로 명시선택 후 기존HTTPS/kubectl에 전달하며 자동YAML 해석은 없다. namespace 생략 `chill-system`, 실제선택 bind동결이다.


## 8. 이행 경계

증거 API 승인배포·1회리허설·우회수집 제거 하네스cutover·대시보드 브라우저수용 기록과 의존캠페인 없음이 live 시작조건이다. rev106시도/rev110결손을 전체PASS로 읽지 않는다.
슬라이스2~5는 측정정책→capacity/reuse→기존기록→잔존하네스/환경값을 구현했다. C정리는 관련변경에 묶고 독립35행 관문은 없다. 공유파일·최종Go/API/Helm검증은 통합소유자, 저장변경은 캠페인 사이1회다.

### 저장 계약

ADR0030에 따라 prefix/version v1alpha5를 유지한다. 새 stream·관측 디렉터리 cutover는 없다. archive/publication #213~#225는 수정·삭제·재게시하지 않는다. 과거 행의 새 필드 부재는 당시 기록되지 않은 사실이며 현재 기본값으로 소급 채우지 않는다. 새 CapacityInput은 새 κ 키를 쓴다. ADR0019의 정리는 읽을 수 없어진 영향 작업용 CR/파생 객체를 열거해 운영자가 수행하며 과학 원시 archive와 구별한다. reader는 단일 버전, 과거 필드 부재의 의미 규칙만 명시한다. dual decoder/migration/alias는 없다.

### 기본값과 검증 근거

settings 생략/명시20/.005/100은 변경 전 fixture의 μ1000/κ959.375·Grid/Bisection판정과 대조한다(키동일성 아님). CI.95의 평균/분산/자유도/음수거절·해상도, reuse30초/3회/상대차, CRtrial3/target5·active2/idle2, 기존 schedule/seed/fallback, timeout30/129초·poll1/2/5초를 유지한다.
배포 검증은 승인overlay/revision/release/namespace와 default/profiler render의 의미 있는 resource/field delta를 명시한다. values-operating.yaml은 rev74캡처이지 현재 배포값이 아니며 offline 완료로 live동일성을 주장하지 않는다.

## 9. 열린 점

기존 ?10행은 자동 API 승격하지 않는다. overload1초/.9, occupancy cap64/64MiB, ready/drain/actuation/admission poll, 미확인 장치1Gi, disk310GB, probe/emptyDir/single-writer 배치가 해당한다. 기본 동작을 유지하며 안전 경계와 측정 입력의 실제 관계로 별도 결정한다.
