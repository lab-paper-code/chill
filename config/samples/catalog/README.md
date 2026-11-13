# 카탈로그 등록 준비

`ModelSpec`, `Preprocessor`, `Dataset`은 준비하고 게시한 자산을 선언한다.
자산 작성자가 모델 변환과 OCI 빌드를 완료하고 등록 운영자가 HTTPS API로 선언을 등록한다.

## 처음 등록하는 운영자

### 준비물

| 담당 | 준비할 것 |
|---|---|
| 자산 작성자 | 사용 권한과 고정 revision, 실행 모델 전체, 전처리 모듈과 보조 파일, 분리된 측정 및 평가 레코드 |
| 플랫폼 관리자 | HTTPS 주소와 신뢰 CA, 승인된 계정 권한, 설치된 실행 선언과 DeviceClass, Pod의 사설 이미지 접근 설정 |
| 등록 운영자 | 게시된 OCI digest, 아키텍처, 이미지 내부 경로, 입출력 계약과 등록 문서 |

ChillServer는 TokenReview와 경로별 verb SubjectAccessReview로 권한을 판정한다.
`403`이면 서버 `message`를 확인해 관리자에게 요청한다.
Bearer token, registry 비밀번호, HF token, access key와 Secret 내용은 등록 문서나 carrier에 넣지 않는다.

### 구조 예제

| 종류 | 예제 | 채울 값 |
|---|---|---|
| ModelSpec | [mobilenet 모델](edge_v1alpha1_modelspec.yaml) | 게시된 image, architecture, files와 설치된 runtime |
| Preprocessor | [SmolVLM 입력 처리](edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml) | 대상 모델 산출물, 코드 carrier와 입출력 필드 |
| Dataset | [VQAv2 레코드](edge_v1alpha1_dataset.yaml) | 데이터 carrier, 내부 루트와 레코드 필드 |

`registry.example.invalid` 주소와 예제 digest는 실제 게시 증거가 없다.
`<registry>`, `<published-digest>`, `<architecture>`, `<absolute-path>`와 아래 명령의
`REGISTRY`, `BUILD-ID`, `INDEX-DIGEST`, `/path/to/...`를 실제 값으로 바꾼다.
등록 문서는 `edge.dacs.io/v1alpha1`의 해당 kind, `metadata.name`, `spec`을 사용한다.
API 요청 본문은 `{name,spec}`다.

### 파일과 입력 계약

[carrier Dockerfile](../../../build/docker/derived-model-artifact.Dockerfile)은
빌드 컨텍스트의 `artifact/`를 이미지의 `/artifact/`로 복사한다.

```text
/artifact/
  mobilenet-v2-050.onnx
  mobilenet-nchw/
    preprocessor.py
  vqav2-val-subset/
    profiling/
      <record-id>/
        image
        question
    evaluation/
      <different-record-id>/
        image
        question
```

- 모델이 사용하는 실행 파일 전체를 `source.files`의 clean absolute 경로로 적는다.
  파일 basename과 확장자를 뺀 stem은 각각 유일해야 한다.
- 전처리 디렉터리는 `preprocessor.py`와 필요한 tokenizer/config 또는 `stats.json`을 포함한다.
  [예제 모듈](../../../build/model-artifacts/preprocessors/mobilenet-nchw/preprocessor.py)과
  [runner](../../../runtime/firstfloor/runner.py)를 기준으로 `WARMUP_SAMPLE`, `preprocess`,
  `infer`, `postprocess`를 제공한다. 원시 필드와 후처리 출력값은 `bytes`다.
  모델 session key는 파일 stem이다. 라이브러리는 설치된 firstfloor runtime image가 제공한다.
- Dataset은 `<source.path>/{profiling,evaluation}/<record-id>/<field-name>` 배치다.
  선언과 같은 이름의 파일에 JPEG 또는 UTF8 원문을 저장한다.
  `maxBytes`는 필드 파일 하나의 원문 바이트 상한이며 1 이상이다.
  Dataset에는 전처리가 요구하는 같은 이름과 형식의 입력이 모두 있어야 하며
  각 상한은 전처리 입력 상한 이하여야 한다.
  [VQAv2 builder](../../../build/model-artifacts/vqav2/build_subset.py)는 이미지 상한 1048576,
  질문 상한 512를 사용한다. 작성자가 profiling과 evaluation을 분리한다.

### 게시 확인과 등록 순서

빌드 또는 registry가 반환한 게시 digest를 `source.image`에 넣는다.
모델과 전처리 carrier의 architecture를 대상 `amd64` 또는 `arm64`와 맞춘다.
Dataset은 사용할 아키텍처를 모두 포함한 index digest를 사용한다.
각 플랫폼에서 해당 digest를 pull/run하여 `sh`와 `cp`, 선언한 파일의 존재와 읽기 권한을 확인한다.
실제 Pod의 registry 접근도 확인한다.

ModelSpec, 대상 Preprocessor, Dataset, Workload 순서로 등록하고 별도 측정을 진행한다.
Dataset 자체는 독립적으로 등록할 수 있다.
대시보드 파일 선택과 붙여넣기는 초안 입력이다. 문서를 검토한 뒤 명시적으로 등록하고
목록 또는 단건 GET으로 저장 결과를 확인한다. 응답 유실 시 먼저 조회하고 자동 재제출하지 않는다.
등록 이후 이미지 pull, 코드 적재, 장치 호환성과 측정 결과를 별도로 확인한다.
[대시보드 시작 안내](../../../chill-dashboard/README.md#카탈로그-등록-시작)와
[API 등록 절차](#validate-and-register)를 따른다.

카탈로그의 `source.image`는 OCI carrier 참조를 받는다.
외부 도구로 파일을 확보하고 변환한 뒤 게시한다. 로컬 경로나 HF, S3, GCS, HTTP,
PVC, MLflow 주소를 이 필드에 넣어도 자산을 가져오지 않는다.


## Before registration

1. Select the executable artifact files. For an RKNN artifact, record every
   input tensor's exact name, uppercase datatype, and sample shape. Omit the
   batch axis; CHILL supplies the batch for each measurement. A Preprocessor
   artifact defines the raw inputs of every other format.
2. For a converted artifact, retain its source artifact, target platform,
   supported batch sizes, converter, and converter version. The repository's
   `make rknn-convert-artifact` target writes both the RKNN file and a conversion
   record containing these values.
3. Put the artifact in an OCI carrier image, push it, and retain an immutable
   `repository@sha256:...` reference. The carrier must match the declared
   Kubernetes architecture and provide `sh` and `cp`; every model file must
   exist at an absolute path listed in `source.files`.
4. Select a runtime family and backend already declared by the installed CHILL
   Profiler for an automatically discovered `DeviceClass`. Do not invent a
   runtime name in `ModelSpec`.

For the supported RKNN path, use a Python environment that already contains
the selected `onnx` and `rknn-toolkit2` versions:

```sh
make rknn-convert-artifact \
  RKNN_CONVERTER_PYTHON=/path/to/venv/bin/python \
  RKNN_SOURCE_ONNX=/path/to/model.onnx \
  RKNN_TOOL_WHEEL=/path/to/rknn_toolkit2.whl \
  RKNN_TOOL_VERSION=2.3.2 \
  RKNN_TARGET_PLATFORM=rk3588 \
  RKNN_DECLARED_BATCH_SIZES="1 2 4 8 16" \
  RKNN_OUTPUT=/tmp/model.rknn \
  RKNN_CONVERSION_RECORD=/tmp/model-rknn.json
```

The [carrier Dockerfile](../../../build/docker/derived-model-artifact.Dockerfile)
copies the build context's `artifact/` directory to `/artifact/`. Build ONNX
carriers for both architectures; the image index digest then serves one
`arm64` artifact and one `amd64` artifact in the `ModelSpec`. The RKNN carrier
serves only `arm64`, so build it with `--platform linux/arm64`.

```sh
mkdir -p /tmp/chill-model-carrier/artifact
cp /path/to/model.onnx /tmp/chill-model-carrier/artifact/model.onnx
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg BASE_IMAGE=REGISTRY/RUNNABLE-MULTIARCH-BASE@sha256:INDEX-DIGEST \
  --file build/docker/derived-model-artifact.Dockerfile \
  --tag REGISTRY/MODEL-ARTIFACT:BUILD-ID \
  --push /tmp/chill-model-carrier
```

Use the image index digest returned by the registry or build, not `BUILD-ID`, in
the `ModelSpec`. On a host of each declared architecture, verify the immutable
carrier and the exact catalogued path before registration:

```sh
docker run --rm --platform linux/ARCH --entrypoint sh \
  REGISTRY/MODEL-ARTIFACT@sha256:INDEX-DIGEST \
  -ec 'test -s "$1" && command -v cp >/dev/null' \
  verify /artifact/model.onnx
```

To stage an existing carrier, select its complete environment ModelSpec and
use the image of the intended artifact; do not copy a testbed address from a
structural example:

```sh
set -e
Image=$(jq -er --arg path config/samples/catalog/edge_v1alpha1_modelspec.yaml \
  '.samples[$path].spec.artifacts[] | select(.name == "canonical-onnx-arm64") | .source.image' \
  "${CHILL_EXPERIMENT_CONFIG:?external environment JSON required}")
id=$(docker create --platform linux/arm64 "$Image")
mkdir -p /tmp/chill-model-carrier/artifact
docker cp "$id:/artifact/mobilenet-v2-050.onnx" /tmp/chill-model-carrier/artifact/
docker rm "$id"
```

## Fill the ModelSpec

| Field | Source of the value |
|---|---|
| `metadata.name` | Stable logical model name |
| `artifacts[].format` | Actual artifact representation, such as `onnx` or `rknn` |
| `source.image` | Pushed immutable OCI reference |
| `source.architecture` | Carrier platform and target `DeviceClass` architecture |
| `source.files` | Exact absolute paths inside the carrier; basenames are unique |
| `input.tensors[].name` | RKNN only: exact artifact input name, preserving input order |
| `input.tensors[].datatype` | RKNN only: explicit uppercase datatype, such as `FP32` |
| `input.tensors[].sampleShape` | RKNN only: artifact input dimensions without the batch axis |
| `derivation` | Conversion record; omit for the canonical artifact |
| `executionPaths[].runtime` | Installed `profiling.executionDeclarations` entry |

The [RKNN converter](../../../build/model-artifacts/rknn/export_rknn.py) loads the
canonical ONNX graph without renaming inputs and builds without quantization.
The RKNN sample declares `input` / `FP32` with batch-free dimensions
`[224, 224, 3]`, which RKNN delivery and the runner check before loading.
The `mobilenet-imagenet` Preprocessor defines the raw inputs of both paths.
Use [the ModelSpec sample](edge_v1alpha1_modelspec.yaml) for structure; store the
complete operational document in the environment JSON's `samples` entry.

## Validate and register

Confirm through the deployed CHILL read API/dashboard that the intended
DeviceClass and runtime declaration exist. Register through HTTPS ChillServer,
not a direct Kubernetes managed-resource write. With `jq`, select the complete
environment document by its existing repository-relative path and extract only
the accepted API fields. Selection must succeed before starting the request:

```sh
set -e
Sample=config/samples/catalog/edge_v1alpha1_modelspec.yaml
Request=$(jq -e --arg path "$Sample" \
  '(.samples[$path] // error("environment sample missing: " + $path)) |
   {name: .metadata.name, spec: .spec}' \
  "${CHILL_EXPERIMENT_CONFIG:?external environment JSON required}")
printf '%s\n' "$Request" |
  curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' --data-binary @- \
    "$CHILL_SERVER/api/v1/models"
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  "$CHILL_SERVER/api/v1/models/mobilenet-v2-050"
```

For a newly prepared manifest, the existing
`yq -o=json '.' "$Manifest" | jq '{name: .metadata.name, spec: .spec}'`
conversion still supplies the same request body. The checked-in
`registry.example.invalid` locations are illustrative, not published-image
or inspection evidence. Environment selection never rewrites image references
inside YAML.

POST returns `201`. PUT accepts `{"spec": ...}` and DELETE has no body; both
require the queried ModelSpec `metadata.resourceVersion` in `If-Match`.
ModelSpecs referenced by a Workload or targeted by a Preprocessor cannot be
deleted (`409`), and a change that removes an artifact targeted by a
Preprocessor returns `409 ModelArtifactReferenced`. Changing a shared model
drains all referring Workloads before publishing the new immutable definitions.
Bearer auth uses TokenReview and path/verb SubjectAccessReview. Managers need
HTTP non-resource permissions, not direct managed-resource write permissions.

Preprocessors and Datasets use the same request shape and `If-Match` rule at
`$OWNER/api/v1/preprocessors` and `$OWNER/api/v1/datasets`. Register the target
ModelSpec first: every Preprocessor artifact must name a registered ModelSpec
artifact that is not being deleted. The samples are
[mobilenet-imagenet](edge_v1alpha1_preprocessor_mobilenet-imagenet.yaml),
[smolvlm2-vqa](edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml) and the
[vqav2-val-subset Dataset](edge_v1alpha1_dataset.yaml).

```sh
set -e
Sample=config/samples/catalog/edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml
Request=$(jq -e --arg path "$Sample" \
  '(.samples[$path] // error("environment sample missing: " + $path)) |
   {name: .metadata.name, spec: .spec}' \
  "${CHILL_EXPERIMENT_CONFIG:?external environment JSON required}")
printf '%s\n' "$Request" |
  curl --cacert "$OWNER_CA" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' --data-binary @- \
    "$OWNER/api/v1/preprocessors"
```

Structural validation does not prove image access, runtime support or hardware
compatibility. Those facts and measured evidence are resolved by profiling and
native admission. The samples declare firstfloor ORT CPU and ORT CUDA paths for
each architecture (`firstfloor-ort-cpu-arm64`, `firstfloor-ort-cpu-amd64`,
`firstfloor-ort-cuda-arm64`, `firstfloor-ort-cuda-amd64`), mobilenet adds
`firstfloor-rknnlite`, and SmolVLM adds the int8 CPU paths
`firstfloor-ort-cpu-int8-arm64` and `firstfloor-ort-cpu-int8-amd64`. PLC names
its arm64 CUDA path `firstfloor-ort-cuda-rnn-arm64` and SmolVLM names its fp32
arm64 CUDA path `firstfloor-ort-cuda-vlm-arm64`. The chart's
`profiling.executionPolicies` limits path eligibility by DeviceClass.
Verify execution and result quality through measurements on the actual device.
A path binds one artifact of one architecture; profiling and native serving
use the corresponding firstfloor execution path and retained measurements.

## Preprocessor, SmolVLM model and dataset carriers

Every carrier uses the carrier Dockerfile above. Stage the build context, build,
and push; the environment documents pin the pushed image index digests, so a
rebuilt carrier replaces its digest in that complete document. Each Preprocessor
declares one artifact per
target model artifact, so the `arm64` and `amd64` ONNX artifacts each name the
same multi-arch carrier. A
preprocessor directory holds the `preprocessor.py` module that the firstfloor
runner imports; its libraries come from the firstfloor runtime image. The
staging scripts need `huggingface_hub`; the dataset builder also needs `pyarrow`.

```sh
# mobilenet-imagenet: one carrier holds the NCHW (ONNX) and NHWC (RKNN) directories.
mkdir -p /tmp/chill-mobilenet-pre/artifact
cp -r build/model-artifacts/preprocessors/mobilenet-nchw \
  build/model-artifacts/preprocessors/mobilenet-nhwc /tmp/chill-mobilenet-pre/artifact/
# SmolVLM2: fp32 or int8 ONNX files and processor files of one pinned Hugging Face
# revision. One preprocessor carrier holds the tiled and the one-tile directories.
python3 build/model-artifacts/smolvlm2/stage.py model /tmp/chill-smolvlm2-model/artifact
python3 build/model-artifacts/smolvlm2/stage.py model-int8 /tmp/chill-smolvlm2-int8/artifact
python3 build/model-artifacts/smolvlm2/stage.py preprocessor \
  /tmp/chill-smolvlm2-pre/artifact/smolvlm2-vqa
python3 build/model-artifacts/smolvlm2/stage.py preprocessor-1tile \
  /tmp/chill-smolvlm2-pre/artifact/smolvlm2-vqa-1tile
# VQAv2: disjoint profiling and evaluation records split by question_id.
python3 build/model-artifacts/vqav2/build_subset.py \
  --output /tmp/chill-vqav2/artifact/vqav2-val-subset
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg BASE_IMAGE=REGISTRY/RUNNABLE-MULTIARCH-BASE@sha256:INDEX-DIGEST \
  --file build/docker/derived-model-artifact.Dockerfile \
  --tag REGISTRY/chill-vqav2-val-subset:BUILD-ID \
  --push /tmp/chill-vqav2
```

Build the other contexts the same way for `linux/amd64,linux/arm64`; every
sample carrier except the RKNN model is multi-arch, so the environment
documents use image index digests.

The SmolVLM2 model carrier holds the artifacts `onnx-fp32-arm64` and
`onnx-fp32-amd64` of the
[`smolvlm2-500m-video-instruct` ModelSpec](edge_v1alpha1_modelspec_smolvlm2.yaml),
each with three files: `/artifact/vision_encoder.onnx`, `/artifact/embed_tokens.onnx`
and `/artifact/decoder_model_merged.onnx`. The runner keys their sessions
`vision_encoder`, `embed_tokens` and `decoder_model_merged`; the module records
their pinned input and output names. The int8 carrier holds the artifacts
`onnx-int8-arm64` and `onnx-int8-amd64`: the revision's `*_int8.onnx` files,
which have the same inputs and outputs, staged under the same three names. Their
Preprocessor directory `smolvlm2-vqa-1tile` turns image splitting off, so each
image is one 512 tile of 64 tokens and one unit. The int8 artifacts declare CPU
paths. The mobilenet ONNX carrier holds `/artifact/mobilenet-v2-050.onnx`.

After the ModelSpecs, Preprocessors and Dataset, register the two Workloads in
[`../manual/edge_v1alpha1_workload_mobilenet.yaml`](../manual/edge_v1alpha1_workload_mobilenet.yaml)
and [`../manual/edge_v1alpha1_workload_smolvlm2.yaml`](../manual/edge_v1alpha1_workload_smolvlm2.yaml)
at `$OWNER/api/v1/workloads`; both use the `vqav2-val-subset` Dataset.

## PLC extruder anomaly carriers

The PLC workload scores windows of 60 consecutive `PLC_EXTRUDER_01` records
(`port_type` 9006) from the `edge_poc2.zip` collection. Its raw `records` field
is the JSON array of the window's records exactly as collected. The builders
need `pyarrow`; the training script also needs `torch`, `numpy`, `onnx` and
`onnxruntime`, trains on CPU with a fixed seed, and checks that ONNX Runtime
reproduces the PyTorch scores.

```sh
# Dataset: disjoint profiling and evaluation windows split by measurement date.
python3 build/model-artifacts/plc-anomaly/build_windows.py \
  --output /tmp/chill-plc-dataset/artifact/plc-extruder-01-windows
# Model and statistics: the train split fits the model, mean and std; the
# validation split sets the threshold.
python3 build/model-artifacts/plc-anomaly/train_export.py \
  --model-dir /tmp/chill-plc-model/artifact \
  --preprocessor-dir /tmp/chill-plc-pre/artifact/plc-extruder-anomaly
cp build/model-artifacts/preprocessors/plc-extruder-anomaly/preprocessor.py \
  /tmp/chill-plc-pre/artifact/plc-extruder-anomaly/
```

Build the model, preprocessor and dataset contexts for
`linux/amd64,linux/arm64`, as above. The [`plc-lstm-ae` ModelSpec](edge_v1alpha1_modelspec_plc.yaml)
has one file, `/artifact/plc-lstm-ae.onnx`, mapping `records` f32 `[batch, 60, 10]`
to `score` f32 `[batch]`. The [`plc-extruder-anomaly` Preprocessor](edge_v1alpha1_preprocessor_plc-extruder-anomaly.yaml)
directory holds `preprocessor.py` and its `stats.json`; it outputs `score` and
`anomaly` (`true` above the threshold). Register them with the
[`plc-extruder-01-windows` Dataset](edge_v1alpha1_dataset_plc.yaml), then the
[`plc-anomaly` Workload](../manual/edge_v1alpha1_workload_plc.yaml).

`make test-artifact-tools` runs the module and dataset builder tests. The
SmolVLM module tests skip unless `python3` has the runtime image's preprocessor
libraries; put a virtual environment with them first on `PATH` to run them.
