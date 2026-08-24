# Prepare a model artifact

`ModelSpec` registers an already prepared model artifact. It does not upload,
convert, or inspect a local model file.

## Before registration

1. Select the executable artifact and record its format and input sample shape.
   Omit the batch axis; CHILL supplies the batch for each measurement.
2. For a converted artifact, retain its source artifact, target platform,
   supported batch sizes, converter, and converter version. The repository's
   `make rknn-convert-artifact` target writes both the RKNN file and a conversion
   record containing these values.
3. Put the artifact in an OCI carrier image, push it, and retain an immutable
   `repository@sha256:...` reference. The carrier must match the declared
   Kubernetes architecture and provide `sh` and `cp`; the artifact must exist
   at the absolute path recorded in `source.path`.
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
expects one build-context file named `artifact`:

```sh
mkdir -p /tmp/chill-model-carrier
cp /path/to/model.onnx /tmp/chill-model-carrier/artifact
docker buildx build \
  --platform linux/arm64 \
  --build-arg BASE_IMAGE=REGISTRY/RUNNABLE-BASE@sha256:DIGEST \
  --build-arg ARTIFACT_NAME=model.onnx \
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
| `source.path` | Exact absolute path inside the carrier |
| `input.inputSampleShape` | Artifact input dimensions without the batch axis |
| `derivation` | Conversion record; omit for the canonical artifact |
| `executionPaths[].runtime` | Installed `profiling.executionDeclarations` entry |

Input data type and layout are runtime-owned and are not registration fields.
Use [the ModelSpec sample](edge_v1alpha1_modelspec.yaml) as the final manifest.

## Validate and register

Confirm that CHILL has discovered the intended device class, then validate the
API object before creating it:

```sh
kubectl get deviceclasses.edge.dacs.io
kubectl apply --dry-run=server \
  -f config/samples/catalog/edge_v1alpha1_modelspec.yaml
kubectl apply -f config/samples/catalog/edge_v1alpha1_modelspec.yaml
kubectl get modelspecs.edge.dacs.io mobilenet-v2-050 -o yaml
```

Server-side validation proves only that the API object is structurally valid.
Image pull, artifact-path access, runtime support, and device compatibility are
resolved when a profiling request selects this model and execution path.
