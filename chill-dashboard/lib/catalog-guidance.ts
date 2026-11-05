import type { CatalogKind } from "@/lib/catalog-manifest"

export const catalogGuidance: Record<CatalogKind, { preparation: string; layout: string; example: string }> = {
  models: {
    preparation: "실행할 ONNX 파일과 CPU 실행 방식 하나를 준비하세요. 실행 방식은 관리자가 설치한 Profiler 선언과 일치해야 해요.",
    layout: "/artifact/model.onnx",
    example: `# 구조 예시입니다. <...>를 실제 준비한 값으로 바꾸세요.
apiVersion: edge.dacs.io/v1alpha1
kind: ModelSpec
metadata:
  name: "<model-name>"
spec:
  artifacts:
    - name: "<onnx-artifact-name>"
      format: onnx
      source:
        image: "<registry>/<model-carrier>@sha256:<digest>"
        architecture: "<amd64-or-arm64>"
        files: [/artifact/model.onnx]
  executionPaths:
    - name: "<installed-cpu-path-name>"
      artifact: "<onnx-artifact-name>"
      runtime:
        family: firstfloor
        backend: ort-cpu
        settings:
          firstfloor:
            maxBatchSize: "<max-batch-size>"
            maxQueueSize: "<queue-limit>"
            deadlinePolicy: keep
`,
  },
  preprocessors: {
    preparation: "JPEG 입력을 모델 텐서로 바꾸고 결과를 UTF8 label로 돌려주는 처리 모듈을 준비하세요. target에는 먼저 등록한 모델과 그 모델의 artifact 이름을 적어요.",
    layout: "/artifact/processor/\n  preprocessor.py\n  <필요한 설정·토크나이저 파일>",
    example: `# 구조 예시입니다. <...>를 실제 준비한 값으로 바꾸세요.
apiVersion: edge.dacs.io/v1alpha1
kind: Preprocessor
metadata:
  name: "<preprocessor-name>"
spec:
  input:
    fields:
      - {name: image, format: JPEG, maxBytes: 4194304}
  output:
    fields:
      - {name: label, format: UTF8}
  artifacts:
    - target:
        model: "<model-name>"
        artifact: "<onnx-artifact-name>"
      source:
        image: "<registry>/<preprocessor-carrier>@sha256:<digest>"
        architecture: "<amd64-or-arm64>"
        path: /artifact/processor
`,
  },
  datasets: {
    preparation: "측정용 profiling과 평가용 evaluation 표본을 서로 겹치지 않게 준비하세요. 각 레코드에 처리 모듈의 입력 필드와 같은 이름·형식의 원본 파일을 넣어요.",
    layout: "/artifact/dataset/\n  profiling/\n    <profiling-record-id>/\n      image  # JPEG 원본 바이트\n  evaluation/\n    <evaluation-record-id>/\n      image  # JPEG 원본 바이트",
    example: `# 구조 예시입니다. <...>를 실제 준비한 값으로 바꾸세요.
apiVersion: edge.dacs.io/v1alpha1
kind: Dataset
metadata:
  name: "<dataset-name>"
spec:
  source:
    image: "<registry>/<dataset-carrier>@sha256:<multiarch-index-digest>"
    path: /artifact/dataset
  fields:
    - {name: image, format: JPEG, maxBytes: 4194304}
`,
  },
}

export const carrierDockerfile = `ARG BASE_IMAGE
FROM \${BASE_IMAGE}
COPY artifact/ /artifact/`

export const carrierBuildExample = `# 모든 <...>를 바꾼 뒤 Dockerfile과 artifact/가 있는 디렉터리에서 실행
docker buildx build --platform '<linux/amd64-or-linux/arm64>' \\
  --build-arg BASE_IMAGE='<runnable-base-image>@sha256:<digest>' \\
  --tag '<registry>/<carrier>:<build-id>' --push .
# Dataset은 --platform linux/amd64,linux/arm64로 제작하세요.
# 등록에는 태그 대신 push 결과의 실제 image/index digest를 적으세요.

# 선언한 각 아키텍처와 파일·디렉터리 경로마다 확인
docker run --rm --platform '<linux/amd64-or-linux/arm64>' --entrypoint sh \\
  '<registry>/<carrier>@sha256:<digest>' \\
  -ec 'test -e "$1" && command -v cp' verify '<absolute-path-in-image>'`

export const preprocessorContract = `WARMUP_SAMPLE: dict[str, bytes]
preprocess(samples: list[dict[str, bytes]]) -> tuple[object, list[int | str]]
infer(sessions: dict[str, object], inputs: object, max_new_tokens: int,
      first_token: Callable[[], None]) -> object
postprocess(outputs: object) -> list[dict[str, bytes]]`

export const catalogPreparationSources = [
  { label: "Hugging Face: 파일·폴더 업로드와 인증", href: "https://github.com/huggingface/huggingface_hub/blob/3739729b86489e4ae28b5a62e622be320b141493/docs/source/en/guides/upload.md" },
  { label: "BentoML: 의존성·모델 패키징과 컨테이너 제작", href: "https://github.com/bentoml/BentoML/blob/1ddd7e782f1ce74a0a37f51c70a44474eb58d5e1/docs/source/get-started/packaging-for-deployment.rst" },
  { label: "MLflow: artifact 저장소와 접근 권한", href: "https://github.com/mlflow/mlflow/blob/d393a0c2bd1a2dcd1ffab4784af3579266a78dc0/docs/docs/classic-ml/tracking/artifact-stores/index.mdx" },
  { label: "KServe 0.16: 저장 위치와 인증", href: "https://github.com/kserve/website/blob/f1405c7703fb82db3ca8c3e16c7d380503a24145/versioned_docs/version-0.16/model-serving/storage/overview.md" },
]
