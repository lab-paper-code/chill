# Scheduler 설정과 관측 API

현재 설정·권한·조회 및 관측 기록 처리 계약이다. 판단·제어 불변식은
[스케줄러 구현 설계](scheduler-implementation.md), 실행기·측정과 calibration의
해석은 [실행기와 측정 계약](scheduler-runtime-measurement.md#시간과-수명)을 따른다.
외부 Workload HTTP·인증·리소스 변경은 [Workload 관리](workload-management.md#5-api와-권한),
기본 JSONL/S3 보존·집계 모집단은 [관측과 보존](workload-management.md#11-관측과-보존)이 원본이다.

## 구성 입력

HTTPS ChillServer는 `--serving-scheduler-configmap=<name>`과
`--serving-scheduler-config-key=scheduler.json`으로 Serving namespace의 구성을 읽는다.
빈 ConfigMap 이름은 기존 Workload 관리·추론만 실행하며, scheduler 구성에서
`settings.controls.enabled=false`를 선택하면 동일 관측·후보 기록으로 정적 실험을 실행한다.

| 구성 필드 | 의미와 실제 근거 |
|---|---|
| `settings` | nanosecond duration의 observation/control/horizon, search/replay/resource bounds·seed·제어 활성 상태 |
| `settings.completionMarginMS` | 필수, 유한한 0 이상의 ms. firstfloor 처리 완료에 요청마다 더하는 완료 여유이고 ChillServer가 dispatch 때 요청에 넣는다. sample의 `50`은 잠정 값이며 E1의 `completionResidualNanos` 분포로 정한다([워크로드별 POOL](workload-pools.md#36-완료-여유)) |
| `cpuInventory`, `gpuInventory`, `reserved` | Node별 실제 CPU IDs·물리 GPU UUIDs와 비서빙 전용 예약. quota는 별도 수량 예산 |
| `preparations` | catalog profile ID별 `PrepareSpec`. 검증된 profile과 immutable service 분포 bundle은 ChillServer가 연결 |
| `preparations[profileID].completionCalibration` | 선택 OPTIONAL: 고정 `from/to`, `windows[].metadata/definitions/requests`의 기존 `ObjectIdentity`; raw 계약·파일은 재작성하지 않음 |
| `staticRouting` | OPTIONAL 전체 현재 Workload UID → physical instance ID → nonnegative int32 weight; UID마다 positive weight 하나 이상. controls OFF의 명시 config revision만 기존 typed ApplyTarget 경로로 적용·physical read-back하며 `manual-static-routing` action의 `sourceConfigRV`에 남김. stale UID/target은 오류; 선언을 유지하는 OFF refresh는 baseline을 보존하며 이후 CHILL은 실제 routing에서 시작 |
| `costReferences`, `powerCostSources` | 실제 원자 동작 raw object와 Complete directed power-mode 측정의 출처 |
| `meters` | 정확한 Node UID·exclusive wall scope·metric series에 연결한 meter 선언 |
| `powerRequests` | Node UID/power mode별 기존 sole actuator의 NodeActuation 요청 |
| `powerObservation` | 기존 sampler의 `Interval/Duration/RequestTimeout/MaxEvents`와 적분 coverage의 `MaximumBoundaryDistance/MinimumCoverage/MaximumGap` |

`staticRouting`의 configuration 수신과 실제 적용은 별개다. 물리 source readiness가
아직 없거나 UID/target 검증이 실패하면 ChillServer listener를 막지 않고 기존 refresh
cadence에서 controls OFF인 동안만 미적용 revision을 다시 평가한다. 실패는 선언
원본·`sourceConfigRV`·실제 routing·오류를 가진 manual action으로 관측 기록과 scheduler
state에 남으며 적용 성공으로 표시하지 않는다. 전체 physical read-back이 일치한
revision은 다시 적용하지 않고, controls ON에서는 미적용 선언도 재시도하지 않는다.

## Completion calibration selector와 발행 객체

Calibration selector의 정확한 preparation payload 형태는 다음과 같다. 값은 실제
측정과 발행 object identity에서 채우며 Serving lifecycle은 selector를 자동 작성하지 않는다.
campaign은 성공한 실제 측정과 closed/published window 확인 후 selector 단계를 출력한다.

```json
{
  "completionCalibration": {
    "from": "<fixed receipt cohort start RFC3339Nano>",
    "to": "<fixed receipt cohort end RFC3339Nano>",
    "windows": [{
      "metadata": {"store": "<store>", "key": "serving/<windowID>/metadata.json", "size": 123},
      "definitions": {"store": "<store>", "key": "serving/<windowID>/definitions.jsonl", "size": 456},
      "requests": {"store": "<store>", "key": "serving/<windowID>/requests.jsonl", "size": 789}
    }]
  }
}
```

`size` 예시는 실제값이 아니다. 기존 `ObservationLog.Health().windows[].objects`는 Go 내부
조회이며 HTTP `GET /readyz`는 `{ready}`만 노출한다. 발행한 object list는 기존 ChillServer
PVC의 `<observationDirectory>/<windowID>/metadata.json`에 보존된다. immutable S3
metadata는 object list를 붙이기 **전**에 업로드되므로 `published=false`/objects 없음이
정상이다. 따라서 동일 window의 세 raw role을 위 selector로 명시한다. ChillServer의 기존
configured evidence store `Reader.Open(ctx, ObjectIdentity)`가 `serving/<windowID>/...`
bytes를 읽는다. 새 publisher/reader API, digest, schema version 또는 TTL은 추가하지 않는다.

## API와 compact projection

| API | 동작 |
|---|---|
| `GET /api/v1/scheduler` | settings와 실제 snapshot·decision·actions·results |
| `GET /api/v1/scheduler/settings`, `/state` | 운영 설정 또는 현재 판단·적용 상태 |
| `PUT /api/v1/scheduler` 또는 `/settings` | 직접 `SchedulerSettings`를 검증하고 named ConfigMap에 저장·적용 |
| `GET /api/v1/observations/load` | 사건 구간의 모든 UID 유입·접수·배정·완료율과 instance assignment 집계 |
| `GET /api/v1/observations/scheduler` | 고정 `from/to`, bounded `limit`, append-order `cursor/nextCursor`의 원시 기록 |
| `GET /api/v1/observations/power` | 같은 고정 `from/to`의 raw 표본을 Node당 한 번 적분한 전체·활성 POOL J/평균 W; 누락은 null과 사유 |

현재 `/scheduler`, `/scheduler/state`, `/scheduler/profiles`와 scheduler history는 선택적으로
`view=compact`를 받는다. 생략하면 기존 full raw 응답이며 저장 관측 기록·archive와
page/cursor 선택은 바꾸지 않는다. 다른 `view` 값은 `InvalidQuery`로 거절한다.
compact 응답은 같은 envelope에 `profiles`와
`completionMeasurements` dictionary를 추가하고, snapshot의 instances/options/
inProgressTarget 및 decision의 keep/target/candidates 안 인스턴스의 `profile`과
`completionMeasurement`만 `{"$ref":"응답 내 key"}`로 치환한다. nil은 그대로다.
dictionary에는 원본의 전체 측정 객체를 보존한다. profile ID와 completion의
profile/Node UID/측정 구간이 key의 기준이며 같은 식별자의 다른 보존 값은 별도
suffix key로 남긴다. 같은 값의 반복만 공유하며 equality는 admission/validation
gate가 아니다. 소비자는 이 두 필드만 dictionary로 해석한 뒤 기존 화면 계약을
사용한다. SLO·정책·설정·후보 순서·물리/선택 인스턴스 신원·사유·출처와 action
acknowledgement 원본은 바꾸지 않는다. 응답 한 page의 얕은 DTO만 만들며 별도
history scan, raw evidence 복사, decoded cache 또는 fallback reader는 없다.
`GET /scheduler/profiles`도 같은 state projection을 사용하며 configVersion과
selections의 persisted/loaded 의미는 그대로다. POST 선택·DELETE 제거 응답은
projection하지 않는다.

power 조회는 controller가 수락한 ChillServer configuration 하나를 atomic하게 읽어 meter
구성을 고정한다. 긴 refresh·control Tick의 scheduler mutex를 기다리지 않으며, 새 설정은
controller 적용 성공 뒤에만 게시한다. 거절된 refresh는 이전 설정을 그대로 유지한다.

## 관측 조회의 일관성·모집단·제외 규칙

ObservationLog 조회는 기존 writer mutex 아래에서 필요한 stream 경로·byte 길이와 window
상태를 한 번 함께 캡처하고, 파일 읽기·JSON decode·집계는 잠금을 푼 뒤 수행한다.
완료된 append 뒤의 길이를 고정하므로 새 append·rotation을 현재 조회에 섞거나
진행 중 record를 잘린 tail로 오인하지 않는다. 실제 read 오류·잘린 원본은 health와
incomplete 상태에 그대로 남는다. 기존 시작 시 전체 stream 검증·decode pass와 성공한
scheduler/request/definition append에서 window당 작은 in-memory census를 채우고, 동일 잠금 아래 값과
검증된 prefix byte 수를 캡처한다. 별도 저장 index나 decoded history cache는 없다.
실제 파일 길이가 census prefix와 다르거나 census를 신뢰할 수 없으면 해당 prefix
전체를 보수적으로 읽는다. window metadata 전체는 gap·완전성 판정용으로 유지한다.
scheduler history는 실제 `SchedulerObservation.At` min/max가 `[from,to)`와 만날 수
있는 window를 append 순서로 읽고, 사건 시각 envelope로 요청 page만 전체 decode한다.
다음 일치 record 하나로 continuation을 확인하며 matching-offset cursor 의미를 유지한다.
window wall time은 append 시각이지 사건 시각의 범위가 아니므로 필터에 쓰지 않는다.
power는 실제 기록 구간의 겹침(표본 0개 포함), coverage boundary 거리로 확장한 실제
sample 시각 범위, 모든 nil-policy window, membership transition과 최신 prior baseline의
후보 window를 합쳐 읽는다. baseline 동률·시각 범위를 가로지르는 window도 보존한다.
정확한 Node·meter·scope·source와 가장 가까운 유효 표본 선택은 기존 reducer가 담당하며,
서로 다른 series의 전역 최근접 표본으로 nil-policy window를 제거하지 않는다.
결정·profile 분포 대신 집계에 필요한 실제 표본·meter·coverage·membership만 decode한다.
capture의 arrivals/load는 receipt 사건의 실제 `ReceivedAt`(없으면 `At`)와 나머지 사건의
`At` 범위로 request prefix를 선택한다. 선택될 receipt가 있을 수 있는 최초 prefix 뒤는
`ReceivedAt` 없는 늦은 definition/accepted/outcome 사실도 합치도록 끝까지 유지한다.
완료 overhead는 모든 request의 실제 `ReceivedAt` 범위를 사용하며 실행 조건 검증과
누락 제외는 기존 reducer에 둔다. 두 조회 모두 전체 definitions와 window metadata를
보존하고, 오래된 append window·역순 시각·알 수 없는 prefix를 빈 성공으로 바꾸지 않는다.
definition은 기존 validation/counting pass에서 기록 유무만 보존하며, prefix byte 수가
일치하고 신뢰되는 빈 stream만 열지 않는다. 사건 시각과 무관하게 모든 nonempty 과거
definition prefix는 유지한다. 정의 추가·알 수 없는/길이가 바뀐 prefix는 다시 읽으며,
Runtime의 retained-definition registry를 복제하거나 별도 정의 cache를 만들지 않는다.
두 작은 projection은 기존 jsoniter 의존성으로 읽되, 건너뛴 subtree의 잘못된
숫자·문자열·구분자도 표준 JSON 구문 검사로 거절한다. power·coverage의 작은 실제
측정 block과 선택된 history 원문은 계속 표준 decoder로 읽어 float·정수·duration·
시각 의미를 보존한다. 큰 decision·profile payload와 원본 기록은 변경하지 않는다.
각 stream 조회는 window가 바뀌어도 성장한 Scanner buffer 하나를 재사용하고 newline
경계를 유지한다. 같은 크기의 후속 record마다 fragment·전체 line·window별 큰 buffer를
새로 할당·복사하지 않는다.

전력 collector의 짧은 기록 구간별 적분 verdict는 원시 기록에 그대로 보존하되,
조회 구간의 coverage 판정으로 재사용하지 않는다. 조회 경계 밖의 실제 표본은 기록된
boundary 거리 안에서만 보조하며, 합친 표본의 원래 gap·값·meter identity·보관 완전성과
조회 구간의 coverage를 검사한다. 조회 중 실제 source 실패와 OOM/재시작 공백은
누락으로 남기며, 이전 구간의 오래된 표본 때문에 정상적인 닫힌 구간을 무효화하지 않는다.
닫힌 ObservationLog window의 겹침은 예정 `EndsAt`보다 이른 실제 `ClosedAt`에서 끝난다.
전력·membership·arrival·완료 overhead 조회가 같은 경계를 사용한다. boundary 표본은
현재 선언의 Node·meter·scope와 source kind/endpoint/metric/labels가 일치한 뒤에만
선택하며, source가 없거나 바뀐 과거 기록을 현재 meter의 실측으로 재해석하지 않는다.

## 설정 권한과 적용 기록

설정 변경은 검증된 identity의 groups/extra까지 사용하여 해당 namespace/name의
`configmaps/update` Resource SAR를 추가 확인한다. decision과 snapshot을 관측 기록에
보존한 뒤 physical action을 시작하고, 실제 시작·완료·실패와 부분 상태를 별도로 기록한다.
