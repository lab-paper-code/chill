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
    durationToleranceSeconds: "<active-duration-tolerance-seconds>"
    dataset: "<registered-dataset-name>"
    metricObservation:
      intervalMilliseconds: "<active-power-interval-milliseconds>"
      timeoutMilliseconds: "<active-power-timeout-milliseconds>"
      durationSeconds: "<active-power-window-seconds>"
  idleProtocol:
    settlingDurationSeconds: "<idle-settling-seconds>"
    durationSeconds: "<idle-duration-seconds>"
    durationToleranceSeconds: "<idle-duration-tolerance-seconds>"
    metricObservation:
      intervalMilliseconds: "<idle-power-interval-milliseconds>"
      timeoutMilliseconds: "<idle-power-timeout-milliseconds>"
      durationSeconds: "<idle-power-window-seconds>"
  trialsPerBatch: "<trials-per-batch>"
  # 배치는 K=1, 2, 4, ... 순서로 artifact·runtime·예산 경계까지 자동 탐색해요.
  retryLimit: "<retry-limit>"
`

export const measurementPreparationSource = {
  label: "BentoML: 설정 파일·JSON 입력 분리",
  href: "https://github.com/bentoml/BentoML/blob/1ddd7e782f1ce74a0a37f51c70a44474eb58d5e1/src/bentoml_cli/deployment.py#L125-L136",
}
