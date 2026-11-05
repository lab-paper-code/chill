# 카탈로그 등록 준비

`ModelSpec`·`Preprocessor`·`Dataset`은 **이미 준비하고 게시한 자산의 선언**이다.
CHILL은 로컬 파일을 업로드하거나 모델을 변환·빌드하지 않는다.
아래 안내는 해당 기능이 포함된 ChillServer와 대시보드가 실제 배포된 뒤 사용한다.
브랜치 변경이나 `main` 병합만으로 운영 화면·서버가 바뀌지는 않는다.

## 처음 등록하는 운영자

### 1. 담당자에게 준비물을 받기

| 담당 | 등록 전에 준비할 것 |
|---|---|
| 모델·입력 처리·데이터 작성자 | 사용할 권한이 있는 원본, 고정 revision, 실행 가능한 모델 파일, `preprocessor.py`와 보조 파일, 분리된 측정/평가 레코드. 모델 변환과 OCI 빌드·push는 외부 도구로 완료한다. |
| 플랫폼 관리자 | 배포된 HTTPS API/대시보드 주소와 신뢰 CA, 승인된 계정과 기존 경로·verb 권한, 설치된 firstfloor 실행 선언과 발견된 DeviceClass. 사설 registry 접근과 필요한 image pull 자격증명은 실제 init/실행 Pod가 이미지를 받을 수 있도록 클러스터 측에서 구성한다. |
| 카탈로그 등록 운영자 | 작성자에게 받은 게시 완료 OCI digest·아키텍처·이미지 내부 경로·입출력 계약과 등록 문서. 아래 순서로 검토·등록하고 GET으로 저장 결과를 확인한다. |

이는 준비 책임의 구분이지 새 역할·권한 규칙이 아니다. 실제 허용 여부는 기존
ChillServer TokenReview/경로·verb SubjectAccessReview가 결정한다.
`403`이면 서버의 원래 `message`를 확인해 관리자에게 요청한다.
화면이나 이 문서에서 별도 역할을 추정하지 않는다.
Bearer token, registry 비밀번호, HF token, access key, Secret 내용은 YAML/JSON에
붙여넣거나 carrier에 넣지 않는다. 인증은 승인된 로그인·빌드 환경·클러스터 설정으로 분리한다.

### 2. 세 종류의 예제에서 시작하기

| 종류 | 기존 구조 예제 | 직접 채울 값 |
|---|---|---|
| 모델 `ModelSpec` | [mobilenet 모델](edge_v1alpha1_modelspec.yaml) | 실제 `artifacts[].source.image`, `architecture`, `files`와 설치된 `executionPaths[].runtime` |
| 모델 입출력 처리 `Preprocessor` | [SmolVLM 입력 처리](edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml) | 등록된 모델/산출물 `target`, 코드 carrier, 입력/출력 필드 |
| 데이터셋 `Dataset` | [VQAv2 레코드](edge_v1alpha1_dataset.yaml) | 데이터 carrier, 루트 `source.path`, 레코드 `fields` |

**예제는 실행 준비된 자산이 아니다.** 체크인된 `registry.example.invalid` 주소와
digest는 게시·접근·실행을 증명하지 않는다. 실행 경로도 설치 환경과 대조해야 한다.
새 문서의 `<registry>/<repository>@sha256:<published-digest>`,
`<architecture>`, `<absolute-path>`는 모두 **교체 필수 자리표시자**이며 유효한 값이 아니다.
아래 기존 빌드 명령의 `REGISTRY`, `BUILD-ID`, `INDEX-DIGEST`, `/path/to/...`도
사용자 환경의 실제 값으로 바꾼다. 예제 값을 그대로 제출하지 않는다.
문서는 `apiVersion: edge.dacs.io/v1alpha1`, 위 세 `kind`, `metadata.name`, `spec`을
사용한다. API 본문은 기존 `{name,spec}`이며 새로운 업로드 필드는 없다.

### 3. 파일과 코드가 실제로 있는지 확인하기

다음은 [기존 carrier Dockerfile](../../../build/docker/derived-model-artifact.Dockerfile)이
빌드 컨텍스트의 `artifact/`를 `/artifact/`로 복사한 뒤의 내부 배치다.
서로 다른 carrier에 들어갈 수 있으며, 컴퓨터의 로컬 경로가 아니다.

```text
/artifact/
  mobilenet-v2-050.onnx                 # ModelSpec source.files의 파일
  mobilenet-nchw/
    preprocessor.py                    # Preprocessor source.path의 디렉터리
  vqav2-val-subset/                     # Dataset source.path의 루트
    profiling/
      <record-id>/
        image                          # 확장자 없는 JPEG 원문 파일
        question                       # 확장자 없는 UTF-8 원문 파일
    evaluation/
      <different-record-id>/
        image
        question
```

- 모델은 사용할 ONNX/RKNN **실행 파일 전체**를 `source.files`의 clean absolute
  경로로 적는다. 파일 basename과 확장자를 뺀 stem은 각각 중복되지 않아야 한다.
  RKNN 변환은 [아래 기존 변환 target 안내](#before-registration)를 재사용한다.
- 입력 처리 파일명은 `processor.py`가 아니라 **`preprocessor.py`**다.
  [실제 예제 모듈](../../../build/model-artifacts/preprocessors/mobilenet-nchw/preprocessor.py),
  [runner의 적재·결과 검사](../../../runtime/firstfloor/runner.py),
  [정확한 함수 계약](../../../docs/design/serving/heterogeneous-workloads.md#nativeipc)을 따른다:
  `WARMUP_SAMPLE`, `preprocess`, `infer`, `postprocess`가 필요하다.
  원시 필드 값은 `bytes`, 전처리 보고는 요청별 성공 단위/실패 사유,
  후처리 결과는 성공 요청별 출력 필드→`bytes`다. 모델 session key는 파일 stem이다.
  tokenizer/config·`stats.json` 같은 보조 파일도 같은 디렉터리에 넣는다.
  라이브러리는 carrier가 아니라 설치된 firstfloor runtime image에서 제공한다.
- Dataset은 `<source.path>/{profiling,evaluation}/<record-id>/<field-name>`이다.
  필드명은 선언과 파일명이 같아야 하며 JPEG 또는 UTF8 원문을 저장한다
  (base64 JSON이나 CSV 목록이 아님). `maxBytes`는 **필드 파일 하나의 원문 바이트 상한**이며
  1 이상이다. [VQAv2 builder](../../../build/model-artifacts/vqav2/build_subset.py)는
  `image: JPEG, maxBytes: 1048576`, `question: UTF8, maxBytes: 512`를 사용한다.
  대상 전처리의 같은 이름·형식 입력이 모두 있어야 하고 Dataset 상한은 전처리 입력 상한
  이하여야 한다(예제 입력 상한은 각각 4194304/1024). 출력 `answer`는 입력 레코드 파일이 아니다.
  작성자가 profiling/evaluation을 분리한다. 등록만으로 중복·내용·품질을 검사하지 않는다.

### 4. 게시 증거를 받은 뒤 등록하기

작성자는 [아래 기존 carrier 빌드·확인 절차](#before-registration)와
[전처리/데이터 준비 절차](#preprocessor-smolvlm-model-and-dataset-carriers)를 사용한다.
빌드/push 또는 registry가 반환한 **게시된 digest**를 받아 `source.image`에 기록한다.
태그만 기록하거나 임의 digest를 만들지 않는다. 모델·전처리의 `architecture`는 실제
carrier와 대상 Kubernetes 아키텍처(`amd64`/`arm64`)에 맞춘다. Dataset은 사용할 양쪽
아키텍처를 담은 multi-arch index digest를 사용한다.
각 플랫폼에서 해당 digest를 pull/run하여 `sh`·`cp`, 선언된 파일/디렉터리와 읽기 권한을
확인한다. init container는 이 도구로 자산을 복사하므로 파일만 있는 scratch image는 충분하지 않다.
사설 이미지의 로컬 pull 성공과 클러스터 pull 가능 여부도 별도로 확인한다.

등록 순서는 **ModelSpec → 해당 모델 산출물을 가리키는 Preprocessor → Dataset →
Workload → 별도 측정 요청·결과 확인**이다. Dataset 자체는 앞의 두 선언과 독립적으로
등록할 수 있지만 Workload 연결 전에 입력 계약을 맞춘다.
대시보드의 파일 선택·붙여넣기·예제 불러오기는 초안 입력일 뿐이다.
자리표시자를 바꾸고 **문서 검토 → 명시적인 등록 → 목록/단건 GET으로 저장 확인**을 거친다.
응답 유실 시 자동 재제출하지 말고 기존 결과 조회 동작을 사용한다.
등록 성공은 이미지 pull·코드 import·장치 호환·측정 완료·서빙 준비 성공이 아니다.
CHILL은 이후 실제 준비와 측정 근거를 통해 판정한다.
[대시보드 시작 안내](../../../chill-dashboard/README.md#카탈로그-등록-시작)와
[기존 API 등록 절차](#validate-and-register)를 사용하며 Kubernetes 직접 쓰기로 우회하지 않는다.

### 참고한 외부 패턴과 CHILL 경계

다음 링크는 `external_repo`에서 확인한 소스의 고정 commit이다. 설치 의존성이나 CHILL
입력 형식이 아니라 **자산 준비·게시와 선언 등록을 분리하는 참고 근거**다.

- [Hugging Face Hub 파일/폴더 업로드](https://github.com/huggingface/huggingface_hub/blob/3739729b86489e4ae28b5a62e622be320b141493/docs/source/en/guides/upload.md): 로컬 경로·저장소 내부 경로·repo/revision을 구분한다. HF에서 자료를 구한다면 외부 작성 단계에서 revision을 고정하고 준비한다.
- [KServe v0.16 storage](https://github.com/kserve/website/blob/f1405c7703fb82db3ca8c3e16c7d380503a24145/versioned_docs/version-0.16/model-serving/storage/overview.md): 저장 위치·인증·initializer가 별도 책임이다. KServe의 `storageUri`, S3/GCS/PVC/HTTP/HF 지원을 CHILL 기능으로 해석하지 않는다.
- [BentoML packaging](https://github.com/bentoml/BentoML/blob/1ddd7e782f1ce74a0a37f51c70a44474eb58d5e1/docs/source/get-started/packaging-for-deployment.rst): 코드·모델·환경을 준비한 뒤 패키징하고 OCI containerize하는 단계를 구분한다. Bento를 CHILL에 직접 넣는 기능은 없다.
- [MLflow ModelVersion identity](https://github.com/mlflow/mlflow/blob/d393a0c2bd1a2dcd1ffab4784af3579266a78dc0/mlflow/entities/model_registry/model_version.py): 등록 이름/버전과 원본 `source`/`run_id`를 분리한다. MLflow URI를 CHILL에서 해석하지 않는다.

CHILL 카탈로그는 현재 OCI carrier의 image와 내부 경로를 받는다.
`hf://`, `s3://`, `gs://`, HTTP 다운로드 주소, PVC 경로, MLflow URI나 로컬 모델 경로를
`source.image`에 넣어도 가져오지 않는다. 필요하면 외부 도구로 파일을 확보·변환·게시한 뒤 등록한다.

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
arm64 CUDA path `firstfloor-ort-cuda-vlm-arm64`; the chart's
`profiling.executionPolicies` list only the DeviceClasses on which those paths
produce correct results (design 7.1). A path binds one artifact of one
architecture; profiling measures through the same firstfloor paths. Native
serving consumes a completed firstfloor campaign with every initialized
measured batch size.

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
image is one 512 tile of 64 tokens and one unit. On ORT 1.27 CPU with 4 threads
the int8 one-tile runner peaks at 1.0 GiB resident for one sample and 1.8 GiB for
four, which fits `jetson-nano-4g`. ORT 1.23.2 CUDA cannot initialize the int8
`vision_encoder`, so the int8 artifacts have CPU paths only. The mobilenet ONNX
carrier holds its single file at `/artifact/mobilenet-v2-050.onnx`.

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
