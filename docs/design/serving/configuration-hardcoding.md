# 설정 소유권과 실행 기록

측정값과 판정에 영향을 주는 입력은 해당 측정 또는 실험 정의가 소유한다. 환경값은 배포 설정이 소유하며 선택한 값과 실제 실행 신원을 기존 불변 실행 기록에 동결한다. 저장 계약은 [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md), 설정 경계는 [Configuration Ownership and Freezing Paths](../../adr/0031-configuration-ownership-and-freezing.md)를 따른다.

## 설정 적용과 증거

판단 당시 적용값은 `Decision.Settings`가 권위다. settings PUT은 의도 기록, ConfigMap 저장, 마지막으로 완전히 수락한 typed 입력에 정책 적용, 결과 기록 순서로 수행한다. `appliedAt`은 정책 수락 시각이다. 원격 catalog 전체 수락과 물리 전환 완료는 별도로 확인한다.

| 상태 | 해석 |
|---|---|
| 의도 기록 실패 | ConfigMap 변경 전에 중단 |
| 저장 성공 후 정책 적용 실패 또는 요청 만료 | 저장값과 현재 적용값이 다를 수 있으며 `appliedAt` 없음 |
| 정책 적용 성공 후 결과 기록 실패 | HTTP 오류가 반환돼도 정책은 적용됐을 수 있음 |
| 후속 full refresh의 적용 | 당시 Decision의 실제 settings로 확인 |
| 적용 증거 부족 | 적용 여부 미확인 |

초기값과 외부 ConfigMap 변경은 full refresh가 수락한 뒤 Decision의 전체 settings로 확인한다. ConfigMap 출처는 보존된 참조와 binding 범위에서만 주장한다. 별도 provenance 저장소나 초기 before=after 변경 행을 만들지 않는다.

## Serving 설정과 측정 정책

`SchedulerSettingsInput`이 생략 기본값을 채우며 GET, Decision과 설정 변경 기록은 실제값을 담는다. 범위 오류와 NaN/Inf는 적용 전에 거절한다.

| JSON 필드 | 범위 | 생략 기본값 | 소비 |
|---|---|---:|---|
| `capacityGridSteps` | 정수 ≥1 | 20 | 요청률 격자 |
| `capacityBisectionWidth` | finite (0,1] | 0.005 | width × 처리율에 의한 이분 탐색 종료 |
| `capacityTailSamples` | 정수 ≥1 | 100 | 설정 분위수의 tail 표본 수 |

세 값은 `CapacityInput`에 동결하며 용량 행의 identity에 포함한다. 수치 계약은 [용량표 계획기](analytic-planner.md)를 따른다.

| 소유자 | 입력과 현재 경계 |
|---|---|
| `ProfilingRun.spec.measurement.durationToleranceSeconds` | finite ≥0이며 측정 duration 미만. 생략 기본값 0.5초, 명시한 0은 유지. RunConfig와 측정 조건에 동결 |
| `BatchEnergyOptimization.spec.session` | 필수. 전력계 지연, 최소 전력 관측 시간, 중간 수준의 비율, 저부하 유휴 간격의 평균, 부하 뒤 관측 시간(선택), 전달 여유, 요청 크기 상한과 동작 측정의 관찰 설정을 소유한다. 모두 결과에 함께 남는다. 수준이 끝나는 최소 표본 수는 `activeProtocol.workload`의 분위수에서 정한다. 요청 수명은 소유하지 않고 서빙이 그 워크로드에 적용하는 기한을 쓰며, 그 값을 결과에 남긴다 |
| `BatchEnergyOptimization.spec.session.confidenceLevel` | [0.5, 1) 십진 문자열. 세션 계산의 모든 통계 판단에 사용 |
| `ModelLoadedIdleBaseline.spec.pointPolicy` | 해당 측정이 최소 반복 수와 신뢰수준을 소유. 최소 반복 수 ≥2, attempt limit은 최소 반복 수 이상 |
| 전환 측정의 `acceptedCountTarget` | 방향별 정수 ≥2이며 attempt limit 이하 |

전역 `energyConfidenceLevel` 또는 `profileConfidenceLevel` 별칭은 없다. Profile은 실제 유도에 쓴 신뢰수준을 기록한다.

## 기동 설정

기존 cluster stream의 `server-start`가 실제 프로세스 시작 시각, Pod 신원과 선택한 `sourceIntervalNanos`, `profileIntervalNanos`를 기록한다. chart 기본값은 각각 1초와 1분이며 운영자가 바꾼 값도 실제 선택값으로 남긴다. 기록 실패 시 기동 실패 계약을 유지한다.

gateway 큐 상한은 Workload의 `spec.admission.maxQueuedRequests`에 있고 applied Definition에 동결한다. 기동 전역 queue limit을 두지 않는다. 요청, drain, operation, 수집 주기는 각 소유 경로의 설정을 사용한다. 추론 handler의 별도 시간 상한을 일반 request timeout으로 설명하지 않는다.

Node sampling과 batch cadence의 출처는 배포 설정이다. `sampledAt`과 `receivedAt`은 관측 시각이며 선택한 cadence와 누락 표본을 모두 설명하지 않는다.

## Dispatch occupancy

The scheduler ConfigMap has no occupancy policy block; the strict decoder rejects a top-level `dispatchOccupancy` key. The dispatch occupancy d for an exact Node UID, model, mode, path, batch and profile ID is the median of the profile's session saturation-level occupancy samples, else `settings.defaultDispatchOccupancyNanos` (including zero). The scalar default applies to every batch. Serving observations do not change d.

## 실험과 환경 입력의 동결

현재 evaluation 입력은 불변 실험 정의와 실행에서 선택한다. scenario가 Workload, seed, 도착 분포와 명시 부하 part의 시간 및 요청률을 제공한다. load-generator Node와 memory-only resources, SLO와 품질 판정, 필요한 운영 시간 예산을 기존 input snapshot에 동결한다.

conductor는 이 입력을 `binding.config`와 schedule에 보존하고 실행 및 분석이 같은 binding을 사용한다. 완결 후 binding을 수정하지 않으며 새 실험은 새 binding을 만든다. namespace는 실행에서 선택한 값을 사용한다. 입력 소유권은 [실험 실행 설계](../profiling/experiment-execution.md)와 [실험 SRS](../../spec/experiments/SRS.md)를 따른다.

registry, 실제 Node, runner와 외부 endpoint는 기존 배포/환경 입력으로 제공한다. 런타임 선언은 image와 inspection 사실을 함께 보존한다. 주소만 바꿔 같은 검증 사실로 취급하지 않는다. 정적 YAML의 JSON 참조를 자동 해석하지 않는다.

coverage helper의 목표 선택은 curve와 evidence schema를 비교하며 runtime image를 역사 metadata로 보존한다. 제품 profile 선택은 측정 image와 runtimeVersion을 적용 조건에 포함한다. 두 선택 경계는 각 소비자의 source를 따른다. Query의 현재 측정 참조를 사용하며 과거 variant의 성공을 찾아 현재 셀을 채우지 않는다.

같은 runtime scope의 execution declaration은 문서 순서에서 마지막 matching 선언을 사용한다. 메모리 안전 정책의 제외와 측정 실패 원문은 보존하며, 조건이 달라지거나 미상이면 과거 OOM을 현재 사실로 확정하지 않는다.

## 저장 계약

현재 reader가 수락하는 단일 계약을 사용한다. 과거 행의 필드 부재는 당시 기록되지 않은 사실이며 현재 기본값으로 소급 채우지 않는다. 저장 형식 변경은 측정과 측정 사이에 한 번 수행한다. 읽을 수 없어진 작업용 CR과 파생 객체의 정리는 게시된 과학 원시 archive의 보존과 구분한다.

관측 stream과 API의 경계는 [Observation Evidence API Streams](../../adr/0030-observation-evidence-api-streams.md)와 [관측 설계](observability-split.md), 현재 측정 계약은 [측정 API](measurement-api.md)를 따른다.
