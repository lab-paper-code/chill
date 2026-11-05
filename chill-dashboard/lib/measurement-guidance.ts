export const measurementRequestExample = `# 구조 예시입니다. 모든 <...>를 실제 등록값과 담당자의 측정 조건으로 바꾸세요.
# 숫자 자리표시자는 따옴표까지 지우고 실제 숫자로 바꾸세요. 권장값이 아닙니다.
apiVersion: edge.dacs.io/v1alpha1
kind: BatchEnergyOptimization
metadata:
  name: "<new-request-name>"
  namespace: "<selected-namespace>"
spec:
  state:
    candidate:
      modelSpec: "<registered-model-name>"
      deviceClass: "<registered-device-class-name>"
      executionPath: "<registered-execution-path-name>"
      preprocessor: "<registered-preprocessor-name>"
      powerMode: "<registered-power-mode>"
    targetNode: "<registered-node-name>"
  activeProtocol:
    workload: "<registered-workload-name>"
    warmupIterations: "<warmup-iteration-count>"
    durationSeconds: "<active-duration-seconds>"
    dataset: "<registered-dataset-name>"
    metricObservation:
      intervalMilliseconds: "<active-power-interval-milliseconds>"
      timeoutMilliseconds: "<active-power-timeout-milliseconds>"
      durationSeconds: "<active-power-window-seconds>"
  idleProtocol:
    settlingDurationSeconds: "<idle-settling-seconds>"
    durationSeconds: "<idle-duration-seconds>"
    metricObservation:
      intervalMilliseconds: "<idle-power-interval-milliseconds>"
      timeoutMilliseconds: "<idle-power-timeout-milliseconds>"
      durationSeconds: "<idle-power-window-seconds>"
  trialsPerBatch: "<trials-per-batch>"
  # 모델 artifact가 유한한 배치 집합을 선언하면 batchSizes 항목 전체를 생략하세요.
  # 선언이 없을 때만 담당자가 정한 배치 집합으로 바꾸세요.
  batchSizes: ["<batch-size>"]
  retryLimit: "<retry-limit>"
`

export const measurementPreparationSource = {
  label: "BentoML: 설정 파일·JSON 입력 분리",
  href: "https://github.com/bentoml/BentoML/blob/1ddd7e782f1ce74a0a37f51c70a44474eb58d5e1/src/bentoml_cli/deployment.py#L125-L136",
}
