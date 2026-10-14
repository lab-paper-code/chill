ARG RUNTIME_IMAGE=python:3.11-slim
FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETARCH
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/serving-worker/ cmd/serving-worker/
COPY cmd/serving-entrypoint/ cmd/serving-entrypoint/
COPY cmd/measurement-worker/ cmd/measurement-worker/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/serving-worker ./cmd/serving-worker \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/serving-entrypoint ./cmd/serving-entrypoint \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/measurement-worker ./cmd/measurement-worker

# CPU is the default. GPU/RKNN builds must supply a vendor-compatible base and
# packages described in docs/design/serving/scheduler-implementation.md.
FROM ${RUNTIME_IMAGE}
ARG NUMPY_VERSION=2.2.6
ARG ONNXRUNTIME_VERSION=1.27.0
ARG ORT_PACKAGE=onnxruntime
ARG PIP_EXTRA_INDEX_URL
ARG CUDA_DRIVER_PACKAGES
RUN python3 -m pip install --no-cache-dir numpy==${NUMPY_VERSION} ${ORT_PACKAGE}==${ONNXRUNTIME_VERSION} ${CUDA_DRIVER_PACKAGES}
COPY --from=builder /out/serving-worker /opt/chill/bin/serving-worker
COPY --from=builder /out/serving-entrypoint /opt/chill/bin/serving-entrypoint
COPY --from=builder /out/measurement-worker /measurement-worker
COPY runtime/firstfloor/ /opt/chill/runtime/firstfloor/
COPY runtime/ortcpu/runner.py /opt/chill/runtime/ortcpu/runner.py
COPY runtime/rknnlite/runner.py /opt/chill/runtime/rknnlite/runner.py
ENV PYTHONDONTWRITEBYTECODE=1
USER 65532:65532
ENTRYPOINT ["/opt/chill/bin/serving-worker"]
