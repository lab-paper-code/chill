FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/measurement-worker/ cmd/measurement-worker/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /out/measurement-worker ./cmd/measurement-worker

FROM python:3.11-slim

ARG NUMPY_VERSION=2.2.6
ARG ONNXRUNTIME_VERSION=1.27.0

RUN pip install --no-cache-dir \
    numpy==${NUMPY_VERSION} \
    onnxruntime==${ONNXRUNTIME_VERSION} \
    && python3 -c 'import onnxruntime as ort; assert ort.__version__ == "'"${ONNXRUNTIME_VERSION}"'"; assert "CPUExecutionProvider" in ort.get_available_providers()'

COPY --from=builder /out/measurement-worker /measurement-worker
COPY runtime/ortcpu/runner.py /opt/chill/runtime/ortcpu/runner.py

USER 65532:65532
ENTRYPOINT ["/measurement-worker"]
