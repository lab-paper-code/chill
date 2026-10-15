# Prepare a model artifact

`ModelSpec` registers an already prepared model artifact. It does not upload,
convert, or inspect a local model file.

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
copies the build context's `artifact/` directory to `/artifact/`:

```sh
mkdir -p /tmp/chill-model-carrier/artifact
cp /path/to/model.onnx /tmp/chill-model-carrier/artifact/model.onnx
docker buildx build \
  --platform linux/arm64 \
  --build-arg BASE_IMAGE=REGISTRY/RUNNABLE-BASE@sha256:DIGEST \
  --file build/docker/derived-model-artifact.Dockerfile \
  --tag REGISTRY/MODEL-ARTIFACT:BUILD-ID \
  --push /tmp/chill-model-carrier
```

Use the digest returned by the registry or build, not `BUILD-ID`, in the
`ModelSpec`. On a host for the declared architecture, verify the immutable
carrier and the exact catalogued path before registration:

```sh
docker run --rm --platform linux/arm64 --entrypoint sh \
  REGISTRY/MODEL-ARTIFACT@sha256:DIGEST \
  -ec 'test -s "$1" && command -v cp >/dev/null' \
  verify /artifact/model.onnx
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
Use [the ModelSpec sample](edge_v1alpha1_modelspec.yaml) as the final manifest.

## Validate and register

Confirm that CHILL has discovered the intended device class. Administrators may
use server-side dry-run solely to inspect CRD structure; this requires Kubernetes
write authorization even though it persists nothing, and is not the manager path:

```sh
kubectl get deviceclasses.edge.dacs.io
kubectl apply --dry-run=server \
  -f config/samples/catalog/edge_v1alpha1_modelspec.yaml
```

Register through the HTTPS owner instead. With `yq` v4 and `jq`, extract only the
accepted API fields from the existing manifest:

```sh
yq -o=json '.' config/samples/catalog/edge_v1alpha1_modelspec.yaml |
  jq '{name: .metadata.name, spec: .spec}' |
  curl --cacert "$OWNER_CA" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' --data-binary @- \
    "$OWNER/api/v1/models"
curl --cacert "$OWNER_CA" -H "Authorization: Bearer $TOKEN" \
  "$OWNER/api/v1/models/mobilenet-v2-050"
```

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
yq -o=json '.' config/samples/catalog/edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml |
  jq '{name: .metadata.name, spec: .spec}' |
  curl --cacert "$OWNER_CA" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' --data-binary @- \
    "$OWNER/api/v1/preprocessors"
```

Structural validation does not prove image access, runtime support or hardware
compatibility. Those facts and measured evidence are resolved by profiling and
native admission. The sample declares matching firstfloor ORT CPU/RKNN paths,
and profiling measures through the same firstfloor paths. Native serving
consumes a completed firstfloor campaign with every initialized measured batch
size.

## Preprocessor, SmolVLM model and dataset carriers

Every carrier uses the carrier Dockerfile above. Stage the build context, build,
push, and replace `PUSHED_DIGEST` in the sample with the pushed digest. A
preprocessor directory holds the `preprocessor.py` module that the firstfloor
runner imports; its libraries come from the firstfloor runtime image. The
staging scripts need `huggingface_hub`; the dataset builder also needs `pyarrow`.

```sh
# mobilenet-imagenet: one carrier holds the NCHW (ONNX) and NHWC (RKNN) directories.
mkdir -p /tmp/chill-mobilenet-pre/artifact
cp -r build/model-artifacts/preprocessors/mobilenet-nchw \
  build/model-artifacts/preprocessors/mobilenet-nhwc /tmp/chill-mobilenet-pre/artifact/
# SmolVLM2: fp32 ONNX files and processor files of one pinned Hugging Face revision.
python3 build/model-artifacts/smolvlm2/stage.py model /tmp/chill-smolvlm2-model/artifact
python3 build/model-artifacts/smolvlm2/stage.py preprocessor \
  /tmp/chill-smolvlm2-pre/artifact/smolvlm2-vqa
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

Build the other contexts like the model carrier above for `linux/arm64`. The
dataset carrier is multi-arch, so its sample uses the image index digest.

The SmolVLM2 model carrier is one artifact, `onnx-fp32` of the
[`smolvlm2-500m-video-instruct` ModelSpec](edge_v1alpha1_modelspec_smolvlm2.yaml),
with three files: `/artifact/vision_encoder.onnx`, `/artifact/embed_tokens.onnx`
and `/artifact/decoder_model_merged.onnx`. The runner keys their sessions
`vision_encoder`, `embed_tokens` and `decoder_model_merged`; the module records
their pinned input and output names. The mobilenet ModelSpec keeps its existing
carriers, whose single files sit at the same `/artifact/<file>` paths.

After the ModelSpecs, Preprocessors and Dataset, register the two Workloads in
[`../manual/edge_v1alpha1_workload_mobilenet.yaml`](../manual/edge_v1alpha1_workload_mobilenet.yaml)
and [`../manual/edge_v1alpha1_workload_smolvlm2.yaml`](../manual/edge_v1alpha1_workload_smolvlm2.yaml)
at `$OWNER/api/v1/workloads`; both use the `vqav2-val-subset` Dataset.

`make test-artifact-tools` runs the module and dataset builder tests. The
SmolVLM module tests skip unless `python3` has the runtime image's preprocessor
libraries; put a virtual environment with them first on `PATH` to run them.
