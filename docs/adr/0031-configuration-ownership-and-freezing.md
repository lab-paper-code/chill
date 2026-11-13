# Configuration Ownership and Freezing Paths

## Status

Accepted

## Decision

측정 조건과 판단 정책은 실제 소유자의 설정에 두고, 선택한 값과 출처를 불변 실행 입력에 동결한다. registry, Node, 외부 endpoint와 namespace는 기존 환경 입력을 사용한다. 프로토콜, 자명한 수식, sentinel과 enum을 운영 설정으로 기계 치환하지 않는다.

SchedulerSettings는 `capacityGridSteps`, `capacityBisectionWidth`, `capacityTailSamples`를 소유하며 기본값은 각각 20, 0.005, 100이다. 용량 계산과 기록에 적용값을 전달하고 입력이 달라지면 해당 용량 키로 재계산한다. 판단에 사용한 값의 권위는 `Decision.Settings`다.

설정 변경은 사전 관측 기록, ConfigMap 저장, 실행 설정 적용, 후속 기록 순서로 처리한다. 사전 기록 실패는 변경을 막는다. 저장이나 적용 뒤 실패하면 실제 상태가 이미 바뀌었을 수 있으므로 후속 판단과 실제 관측으로 해석한다. 충분한 증거가 없으면 미확인으로 남긴다.

서버 시작 기록은 Pod 신원, 시작 시각과 `sourceIntervalNanos`, `profileIntervalNanos`를 보존한다. gateway 정책과 큐 상한은 Workload Definition에 동결하며 [Workload-Owned Overload Admission](0033-overload-admission.md)을 따른다.

`BatchEnergyOptimization.spec.pointPolicy.confidenceLevel`은 해당 측정의 활성 에너지, 유휴 기준과 에너지 신뢰구간을 소유한다. 십진 문자열이며 생략값은 `"0.95"`다. 독립 유휴 측정은 자체 정책을 유지한다. 결과와 메시지는 실제 적용값을 기록한다.

반복 수와 시도 한도는 측정 종류별 실제 계약을 따른다.

- BatchEnergyOptimization은 배치당 세 trial로 고정하며 공통 유휴 기준도 세 trial이다.
- ModelLoadedIdleBaseline의 `minimumRuns`는 2 이상이고 `attemptLimit`은 2부터 100까지다.
- PowerModeTransitionCost는 방향별 `acceptedCountTarget`과 `attemptLimit`이 각각 2 이상이며, 시도 한도는 목표 수 이상이다.
- 프로파일 재사용 확인의 기본 `minimumMeasurementSeconds`는 30, `minimumTrials`는 3이다. 실제 허용 하한은 양의 측정 길이와 trial 두 번이다.

평가 하네스는 선택한 설정, 출처와 실행 순서를 실행 입력에 동결한다. 실행 중 재조회로 측정 조건을 바꾸지 않는다. 완료 binding은 해당 실험에서 수정하지 않고 다음 실행에 새 binding을 사용한다. 파일 저장을 물리적 write-once 보장으로 해석하지 않는다.

## Consequences

기존 소유자와 입력 경로를 재사용한다. CLI와 Helm 기본값이 다르면 각각의 실제값을 보존한다. 저장하지 않은 ConfigMap 신원이나 resourceVersion을 출처로 주장하지 않는다.

계약 변경은 의존 캠페인 사이에 한 번 모으고 [Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md)를 따른다. 과거 필드 부재에 현재 기본값을 소급 적용하지 않으며 과학 원시와 불변 게시물을 변경하지 않는다. 관측 저장은 [Observation Evidence API Streams](0030-observation-evidence-api-streams.md)가 소유한다.

