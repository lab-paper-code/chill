# 하드코딩 정리: 설정 소유권과 기록 경로

상태: 구현 전 설계(2026-10-30). 이 문서는 현재 구현 완료를 뜻하지 않는다.
원본은 [계획](../../plans/2026-10-30-configuration-hardcoding.md),
[행 인벤토리](../../plans/2026-10-30-configuration-hardcoding-inventory.md),
[ADR 0031](../../adr/0031-configuration-ownership-and-freezing.md)이다.

## 1. 경계와 결정

A(측정·판정)와 B(환경)만 소유자의 기존 설정으로 옮긴다. C는 상수, D는 기존 설정을 유지한다.
새 전역 설정 파일·서비스·registry·provenance framework·의존성을 만들지 않는다.
`SchedulerSettings`, 측정 CR spec, 실험 config/binding, 환경 values/JSON이라는 기존 네 집을 쓴다.
표시용 분위수·시각화 bin·live 정정 창은 저장 원시와 수용 판정을 바꾸지 않으므로 C다.
고정 p99/p95라는 이름의 요약 필드를 가변 분위수로 바꾸지 않는다. 실제 SLO는 Workload percentile이다.

현재 실행 경로는 `internal/**`, `cmd/**`, `api/**`, `charts/**`, `load-generator/**`와
E5 실행기 및 그 helper다. `config/samples/**`는 환경 결합을 제거하는 데 필요한 범위만 포함한다.
Dashboard 구현, Makefile/개발·배포 도구의 일반화, 독립 demo·옛 측정 script는 범위 밖이다.
범위 밖이라고 파일이 안전하게 삭제된다는 뜻은 아니다. 삭제는 별도 승인 대상이다.

## 2. 기록을 동결로 쓸 수 있는 근거와 한계

- `internal/serving/scheduler.go:20-51`: 기존 settings 소유자. 같은 파일의
  `SchedulerDecision.Settings`가 판단에 사용한 설정을 담는다.
- `internal/serving/httpapi/scheduler.go:140-170`: PUT은 actor·before/after·validatedAt·affected를
  구성하여 **ConfigMap 변경 전에** `SchedulerRecord(Settings: &change)`를 쓴다.
  persist + full refresh 뒤 appliedAt 또는 error를 넣어 한 번 더 쓴다. 사전 append 실패면 변경하지 않는다.
- `internal/serving/observation_log_scheduler.go:17-31,50-81,106-111,183`:
  settings 변경 몸체가 `scheduler` stream의 JSONL 행으로 저장된다.
  따라서 변경 전후 **실제 값과 적용 성공 여부의 기록**은 이미 있다. 단순 HTTP 접근 로그가 아니다.
- 이 기록은 불변 `ProfilingRun.spec`와 동일하지 않다. serving의 동결은 초기 적용 snapshot,
  성공/실패 변경 행과 판단 Settings를 시간순 보존하여 당시 적용값을 해석하는 계약이다.
  `validatedAt`만 있는 사전 행은 적용 완료 증거가 아니다. appliedAt이 없거나 error가 있으면
  성공한 설정으로 사용하지 않는다. 변경 후 기록 실패도 성공으로 보고하지 않는 기존 동작을 유지한다.
- 현재 PUT 기록만으로 시작값·CLI의 실제값·ConfigMap 직접 수정의 출처까지 모두 안다고 할 수 없다.
  구현은 초기 적용 설정 및 full refresh가 실제 수락한 외부 ConfigMap 변경도 기록한다.
  ConfigMap namespace/name/UID/resourceVersion/key와 서버 Pod UID·프로세스 시작 시각을 함께 남긴다.
  API 변경은 기존 두 행을 유지하고 같은 적용을 중복 변경으로 세지 않는다.
- `hack/e5/e5.py:1132-1146,1264-1294`: bind가 config를 정상화하고 config/schedulerSettings/
  source refs/하네스 commit/schedule/seed/offset을 binding JSON에 넣는다.
  기존 `save`는 일반 파일 쓰기다. 불변 Kubernetes Run 또는 변조 방지 저장소라고 주장하지 않는다.
  실험의 동결은 bind 종료 뒤 해당 binding을 변경하지 않고 실행·분석이 그것만 소비하는 계약이다.
  새 campaign은 새 binding으로 한다. config 경로만 기록하거나 실행 중 환경 JSON을 다시 읽지 않는다.

## 3. serving settings 확장

정확한 필드명·단위·범위·기본값은 [SCH-71 표](../../spec/scheduler/SRS.md#9-설정-소유권과-기록-활성-요구사항)를 따른다.
8개 필드는 기존 GET/PUT의 `SchedulerSettings`/`SchedulerSettingsInput`에 함께 추가한다.
생략 입력은 표의 현재값으로 정상화하며 GET·before/after·Decision에는 항상 실제 숫자가 나온다.
NaN/Inf를 포함한 범위 오류는 기존 InvalidBody 오류로 거절하고 적용/ConfigMap 저장을 하지 않는다.

호출 연결:

1. κ 격자·이분 폭·tail 목표는 CapacityInput 및 용량표 계산 입력에 포함한다.
   같은 profile/SLO라도 이 값이 바뀌면 기존 κ 캐시를 재사용하지 않는다. 별도 hash 비교 검증 관문은 없다.
2. `energyConfidenceLevel`은 catalog의 energy PointPolicy `.95`를 대체한다.
   `profileConfidenceLevel`은 idle baseline에 쓰던 CLI `.95`를 대체한다. 두 신뢰수준은 독립이다.
   catalog는 refresh가 수락한 settings snapshot으로 다시 유도한다. 진행 중 판단이 서로 다른 버전의
   confidence·profile·capacity를 섞지 않게 기존 full refresh 경계를 재사용한다.
3. 재사용 active/idle 창과 trial 하한은 `compareReuseProfiles`에 전달한다.
   신뢰구간 계산의 수학적 최소 2회 아래는 허용하지 않는다.
4. power support 창은 `observation_log_power.go`의 앞뒤 1분 선택 범위에 전달한다.
   시각별 재분석 결과가 선택된 창을 식별하도록 결과/판단 기록에도 선택값을 남긴다.
5. `--profile-confidence-level`, chart `servingEntrypoint.profileConfidenceLevel` 및 관련 템플릿 인자는
   새 settings 필드로 완전히 교체한다. 샘플 scheduler JSON·테스트 입력·Dashboard typed 설정 및
   하네스 GET→PUT whitelist 등 **모든 소비자**를 같은 변경에서 갱신한다. 별칭·fallback shim은 두지 않는다.
   Dashboard UI 재설계는 하지 않고 기존 typed round-trip 계약만 맞춘다.
6. `sourceInterval`/`profileInterval`은 조사 API 후보라도 사용자 결정에 따라 **기동 설정 유지**다.
   `powerObservation.sampling/coverage`는 이미 별도 configuration 필드이고 관측의 meter catalog에
   저장되므로 settings로 중복 이동하지 않는다. meters는 계속 읽기 전용이다.

## 4. 기동 실제값 한 행

새 stream은 만들지 않는다. 기존 `scheduler` stream에 `startup` 몸체를 가진 JSONL 행을
**프로세스당 정확히 한 번**, options 검증·실제 timeout 산출 후, 요청 수락/첫 scheduler tick 전에 append한다.
scheduler가 비활성인 서버에서도 기록한다. append 실패면 기동 실패로 끝내며 메모리-only 적용 성공은 없다.
관측 저장 계약 변경 시 schedulerLine/원시·가공 reader/export와 하네스 reader를 함께 바꾼다.

행 형태(필드명 확정; 아래 숫자는 Helm 기본 예시이며 CLI 기본과 혼동하지 않는다):

```json
{"at":"2026-10-30T00:00:00Z","startup":{"podUID":"<actual-pod-uid>","processStartedAt":"2026-10-30T00:00:00Z","requestTimeoutNanos":129000000000,"drainTimeoutNanos":164000000000,"sourceIntervalNanos":1000000000,"profileIntervalNanos":60000000000,"directoryIntervalNanos":60000000000,"queueLimit":1024,"controlComponents":["<effective-components>"],"source":{"kind":"process-options","arguments":["<non-secret-policy-flags-only>"]}}}
```

`requestTimeoutNanos`는 실제 handler/gateway에 적용한 값이다. CLI 기본은 request30s/drain120s,
Helm 기본은129s/164s이고 운영 overlay sourceInterval은2s다. 어느 쪽도 덮어써 같게 만들지 않는다.
인자에는 이 필드들의 명시/기본 선택 출처만 담고 token·credential·전체 argv/env는 담지 않는다.
파일/overlay를 서버가 모르면 그 경로를 꾸며 쓰지 않는다. Pod identity와 실제 options를 권위로 삼는다.
초기 settings는 기존 settings 행에 `before=after=실제 초기값`, appliedAt, source identity를 기록한다.
startup 행에 settings를 또 복사할 필요는 없다. 재시작은 새 processStartedAt의 새 행이다.

Node resource sampleInterval/batchInterval은 node agent가 실제 적용값을 기존 resource 전송 몸체에
포함하고 서버가 `resources` 행에 함께 보존한다. 이 두 필드는 서버 startup 값으로 사칭하지 않는다.
기존 resources stream만 사용한다. 전달 payload 검증 경계에서 한 번 검증한다.
transition observer overhead180s는 기존 배포값을 유지하되 attempt Job의 기존 frozen 실행 입력에
선택값·출처를 보존한다. profiler 소유이며 serving settings에 넣지 않는다.

## 5. 측정 CR와 불변 실행

아래는 **추가/변경할 계약**이다. 기본값을 CR admission/defaulting에서 채우고 immutable spec 및
기존 frozen Run/attempt 입력에 선택값과 부모 UID/generation·선택 policy Source를 복사한다.
기존 결과를 새 정책으로 조용히 재해석하지 않는다. 부모 BEO/idle CR도 실행이 시작되면
해당 실행에 사용한 정책 snapshot을 고정하고 child Run·집계 모두 그 snapshot만 사용한다.

| 소유자/경로 | 기본값 | 허용 범위·관계 | 소비자 |
|---|---:|---|---|
| ProfilingRun.spec.measurement.durationToleranceSeconds; BEO activeProtocol/idleProtocol의 동명 필드 | 0.5초 | finite ≥0, < 해당 durationSeconds | active/idle evidence 길이 수용; idle-baseline protocol에도 전달 |
| BEO.spec.pointPolicy.confidenceLevel | 0.95 | finite (0,1) | curve_deriver 및 energy objective; 기존 point estimator version 유지 |
| BEO.spec.pointPolicy.minimumRuns | 3 | 정수 ≥2, ≤trialsPerBatch | point/baseline/energy 유도; 기존 idle pointPolicy.minimumRuns 재사용 |
| 기존 BEO.spec.trialsPerBatch | 현재 지정값 유지 | 하한3을 수학적 최소2로 변경; default3 | 새 minimumRuns와 함께 확인 |
| 기존 PowerModeTransitionCost.spec.acceptedCountTarget | 현재 지정값 유지(기본5) | 정수 ≥2, ≤attemptLimit; attemptLimit 하한도2 | summary의 별도 floor5 제거, 양방향 각각 target 적용 |
| DispatchOccupancyMeasurement.spec.minimumDurationSeconds | 60초 | 정수 ≥1; open-loop durationSeconds ≥선택 하한 | 코드/CRD의 숨은60 제거; closed-loop에 강제하지 않음 |
| DispatchOccupancyMeasurement.spec.minimumRateFactor | 1.2 | finite >1; open-loop rateFactor ≥선택 하한 | schema의 숨은1.2 제거; closed-loop 미사용 |
| DispatchOccupancyMeasurement.spec.seed | 0 | uint64 | load generator 실행 인자·실행 기록 |
| attempt의 기존 frozen 실행 입력: transitionObserverActiveDeadlineOverheadSeconds | 180초 | 정수 ≥0, Job deadline 정수 표현 범위 내 | 실제 Job deadline 산출·실행 출처 기록 |

`minimumRuns=2`는 새 통계 근사를 추가한다는 뜻이 아니다. 기존 통계 primitive가 해당 정의역을
지원하지 않으면 조용한 근사 대신 명시 오류를 내고, 수학적 지원 범위만 허용한다.
고정 방법론 p99 요약, PCG stream selector1, 양측 CI의 /2와 n−1은 이름 있는 상수/수식이다.
값 하나만 받는 필드를 새로 만들지 않는다. 기존 occupancy `workerCap==64` 등 ? 항목의
단일값 mirror 문제는 열린 점에서 분리하며 이번 변경을 이유로 새 단일값 필드를 추가하지 않는다.

## 6. 하네스: bind에서 한 번 선택

인벤토리 슬라이스6의 키는 기존 experiment config 안에 넣는다. `stages`, `workloads`, `timeboxes`,
`collection`, `g0Evidence`, `inputs` 등 기존 소유 객체를 확장하고 별도 settings API는 만들지 않는다.
기본값은 각 행의 현재값이다. default를 채운 **effective config 전체**를 binding.config에 저장한다.
실행/분석 helper에는 필요한 값만 명시 인자로 전달한다. 모듈 전역 환경 JSON 재조회·문자열 replace로
리터럴을 바꾸는 경로(`REMOTE` cadence/namespace 등)는 없애고 기존 템플릿 인자를 사용한다.

- 시간·cadence·timeout은 초 단위 finite >0, 개수/동시성은 정수 ≥1, seed는 uint64,
  rate·alpha는 finite ≥0, 허용 fraction은 [0,1], percentile은 (0,100]이다.
  maxCPUFraction은 (0,1], idle 오차는 finite ≥0. collector 전체 시간은 구성된 최대 시간 이하여야 한다.
  subprocess 호출 상한은 maxActuationSeconds 이상, API/수집 예산 등 기존 관계식도 입력 경계에서 검증한다.
- G0의120초/alpha1.2, d25의25개/1ms, native max900초를 값 하나만 허용하는 검증에서
  해제하고 선택된 config와 증거가 맞는지 확인한다. 현재 E5 기본 실험 정의는 그대로 보존한다.
  E5 실험의 의미가 바뀌는 config는 새 binding/새 실험으로 해석한다.
- full/static/factorSeconds는 schedule/선택 N에서 유도하며11700/10200/2100×N을 따로 설정하지 않는다.
  closure 모집단은 선언된 입력 목록과 count를 비교하고 목록을 잘라 맞추지 않는다.
- loadgen의10ms delayed 기준은 기존 CLI에 `--delay-tolerance`(초, 기본0.01, finite ≥0)를 추가하고
  scheduler와 결과 delayed 계산 양쪽에 같은 값을 쓴다. E5는 workload의 실제 arrivals/seed/workers/
  delivery/socket/lifetime와 이 기준을 binding에 기록한다. 단독 CLI는 기존 출력 metadata에 실제
  실행 인자를 남긴다. 요청별 결과 필드의 의미를 바꾸거나 가짜 immutable Run을 만들지 않는다.
- 기본값 충돌을 통합하지 않는다. E5 apiSeconds가 명시되면 그것을 쓰고, 기존 독립 함수의 fallback30/129,
  auth fallback10 등은 해당 경로가 실제 사용한 경우에만 정상화한다. 전체 비활성 D2 CLI를 수정하지 않는다.
- source refs와 기존 digest는 출처 정보로 유지하되 새 바이트/해시 equality 검증 관문은 추가하지 않는다.

호출 증거: `e5.py:1172,1219`는 bind_before의 SETTINGS/eligibility,
`1310,1333,1460,1631,1673,1755,1805`는 Conductor의 실제 api/publication/producer/삭제 메서드,
`1489`는 resolve_control_alert, `1497`은 sampler REMOTE, `2031`은 native_mode_request 실행,
`2093-2176`는 analyze_before의 stats/quality/telemetry와 archive 수집/분석을 사용한다.
따라서 helper 파일 전체를 역사 코드로 버리면 안 된다. 반대로 bind_before main의 SLO/계단 부하,
conduct_before run_load/main의 seed320/600초 같은 값과 analyze_before main의 p95/throttle gate는
E5가 호출하지 않으므로 현재 실행 대상에서 제외한다. e5의 `tables` 분기는 tables.py를 호출한다.

## 7. B 환경값 연결

Go 내부 값이므로 values로 옮길 수 없다는 이유로 남겨두지 않는다. 기존 배포 values/Options를 통해
생성자와 Job/Pod spec에 전달한다. 포트는 Service targetPort·container port·probe·endpoint 생성자가
같은 값을 받아야 한다. UID/GID/fsGroup은 이미지 사용자와 맞춘 환경값으로 전달하고 보안 동작은 유지한다.
host systemd 경로·staging/data/TLS mount는 실제 volumeMount와 플래그를 함께 바꾼다.
이미지 저장소·장비 catalog·signature·모드·clock·namespace·사설 IP·Node·샘플의 artifact 경로는
values-operating.yaml/환경 JSON이 소유한다. digest·wire schema·API/RBAC vocabulary는 이동하지 않는다.
기본 values는 환경 없는 구조(빈 catalog/explicit required binding), 샘플은 환경 JSON/overlay에서
주입하는 참조를 사용하며 특정 클러스터 주소를 새 가짜 주소로 치환하지 않는다.
환경 입력을 안 준 경우 필요한 연결에서 명시 오류를 낸다. 값이 있는 척 fallback하지 않는다.

## 8. 구현 슬라이스와 병렬 경계

순서는1→2→3→4→5→6이다. 독립 영역은 병렬 가능하되 공유 파일 소유자를 한 명으로 정한다.
아래 검사는 **다음 구현 세션이 실행할 회귀**이며 이 문서 작업에서 실행하지 않았다.

| 번호 | 파일 영역/소유자 | 전달 인터페이스·공유 경계 | 완료 기준 및 기본값 회귀 |
|---|---|---|---|
| 1 C 상수화 | internal/cmd/api의 이름 없는 C, loadgen·E5 helper C, chart helpers | 의미가 같은 기존 계약만 공유; 3/5와 같은 Go 파일은1 종료 후 인계 | 리터럴에 의미 이름; API 필드 추가 없음; 기존 경계값·오류·직렬화 의미 검사 |
| 2 B 제거 | charts values/templates, config/samples; Go 생성자/Options·Job spec; E5 환경 매핑 | Go B/CR 생성 파일은5와 직렬; charts serving template/options는3/4와 직렬 | 운영 overlay를 주면 현재 port/path/user/catalog와 동일, 환경 없는 기본·샘플에 실클러스터 binding 없음; Helm default/profiler 별도 render |
| 3 serving settings | internal/serving/{scheduler,capacity,observation_log_power}·kube catalog/reuse·httpapi, cmd service/options, chart confidence 인자, JSON·typed caller | 8필드 typed settings를4/6에 인계; settings/capacity cache/consumer 일괄 변경 | GET/PUT 정상·범위 오류·비기본값 소비·before/after 로그·캐시 재계산; 기본 κ 탐색 입력/창/CI/재사용 판정 수치 유지 |
| 4 기동 기록 | server.go·observation_log_scheduler/resources·resource 전송 및 reader/export | 3의 settings 동결형;5/6과 scheduler/resources 저장 계약 cutover 한 번 통합 | CLI30/120와 Helm129/164, operating source2s 각각 실제값; 프로세스당 startup1행, 재시작 새행, append 실패 기동 거부, 초기/외부 settings 적용 기록 |
| 5 측정 CR | api/types·CRD/generated·profiling run/campaign/derivation/transition·occupancy | 2의 Job 환경 wiring 종료 후; 3의 catalog 계산과 공유 정책 인터페이스 합의 | 기본0.5/.95/3/5/60/1.2/seed0로 기존 수용 결과; 비기본값이 CR→frozen Run→수집→집계에 전달; 불변성·범위/관계 검증 |
| 6 하네스 config | hack/e5 실제 helper·load-generator; 예제 config의 해당 정책 키 | 3의 settings 필드,4/5의 저장 계약 확정 후 consumer 통합; 환경 B는2가 소유 | bind에 effective default 모두 존재; 실행·judge는 binding만 소비; 기본 schedule/offer수·판정·timeout 의미 동일, 대체 config로 숫자 gate 해제 확인 |

같은 파일을 여러 slice가 동시에 편집하지 않는다. 저장 형식 통합 소유자는4 담당자이며
모든 새 필드를 모아 캠페인 사이 한 번 cutover한다. ADR0019에 따라 reader는 새 계약 하나만 지원한다.
구버전 reader·migration·alias·digest 재검증은 만들지 않는다. 영향을 받는 객체는 운영자가 정리한다.
기본값 증명은 구조적 필드/계산 결과/수용 결과 비교이며 파일 바이트나 hash 동등성 관문이 아니다.
최종 구현 소유자가 모든 slice 종료 뒤 해당 surface의 AGENTS.md 검사들을 한 번 실행한다.

## 9. 열린 점

인벤토리의 ?10행은 근거 없이 API knob로 승격하거나 삭제하지 않는다. 기본 동작은 유지한다.
Go의 overload1s/.9, occupancy captured cap64/64MiB, ready/drain/actuation/admission poll,
미확인 장치1Gi fallback; E5 disk310GB; chart probe/emptyDir·single-writer/base 배치가 해당한다.
1Gi fallback은 실제 장치 사실과 구분해야 하며 이 문서는 근거 없는 대체 용량을 새로 정하지 않는다.
각 행은 측정 구간 안인지/안전 계약인지 확인 후 별도 결정한다. 기존 한 값만 허용하는 cap mirror는
AGENTS 규칙과 긴장 관계이므로 변경 전에 실제 native queue 계약과 맞춰 상수화 또는 범위화를 결정한다.
source/profile interval을 동적 API로 바꾸자는 조사 의견은 있으나 사용자 선택(기동 유지)을 따른다.
Node command timeout은 이미 설정 가능한 전송·안전 envelope(D)로 유지한다.
이 문서는 live 상태나 실제 동결 산출물을 확인했다는 주장을 하지 않는다.
