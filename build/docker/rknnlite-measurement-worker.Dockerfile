FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/measurement-worker/ cmd/measurement-worker/
COPY internal/ internal/
RUN test "${TARGETARCH}" = "arm64" \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o /out/measurement-worker ./cmd/measurement-worker

FROM python:3.10-slim

ARG NUMPY_VERSION=2.2.6
ARG RKNN_TOOLKIT_LITE2_VERSION=2.3.2

RUN pip install --no-cache-dir \
    numpy==${NUMPY_VERSION} \
    rknn-toolkit-lite2==${RKNN_TOOLKIT_LITE2_VERSION} \
    && python3 -c 'import importlib.metadata; from rknnlite.api import RKNNLite; assert importlib.metadata.version("rknn-toolkit-lite2") == "'"${RKNN_TOOLKIT_LITE2_VERSION}"'"; assert hasattr(RKNNLite, "NPU_CORE_0")'

COPY --from=builder /out/measurement-worker /measurement-worker
COPY runtime/rknnlite/runner.py /opt/chill/runtime/rknnlite/runner.py

USER 65532:65532
ENTRYPOINT ["/measurement-worker"]
