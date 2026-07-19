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

RUN pip install --no-cache-dir numpy==2.2.6 onnxruntime==1.23.2

COPY --from=builder /out/measurement-worker /measurement-worker
COPY runtime/ortcpu/runner.py /opt/chill/runtime/ortcpu/runner.py

USER 65532:65532
ENTRYPOINT ["/measurement-worker"]
