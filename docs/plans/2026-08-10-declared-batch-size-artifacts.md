# Declared-Batch-Size Artifacts — 결과 요약

- 상태: completed. 하나의 불변 artifact가 `DeclaredBatchSizes`를 선언하고 intent freeze에서 집합 포함 여부를 판정한다.
- 배치 축을 뺀 입력 계약과 공유 `CurvePlanSpec`으로 per-batch artifact 중복을 제거했다. 현행 구현: `api/v1alpha1/modelspec_types.go`, `internal/profiling/campaign/controller/curve_plan_source.go`.
- 계약 권위: [선언 배치 설계](../design/profiling/plans/08-declared-batch-size-artifacts.md). RKNNLite NHWC / ONNX NCHW는 별개 계약이다.
- 원 계획의 별도 범위였던 conversion/device-probe evidence 전달과 BatchSaturation 연결은 [profiling 계획 목록](../design/profiling/README.md)에서 현행 상태를 확인한다. 이 요약으로 미구현을 재등록하지 않는다.
- `SaturatedSerialFixedBatchWorkloadMode`는 한 Run 안에서 batch를 고정한다는 뜻이므로 이름 변경 대상이 아니다.
