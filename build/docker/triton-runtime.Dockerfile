ARG TRITON_IMAGE=nvcr.io/nvidia/tritonserver:26.09-py3@sha256:1f08352ff9318894ca24bb36a532ad450b1289f7a6aeb163ae752a446c400fe6
FROM --platform=${BUILDPLATFORM} golang:1.22 AS measurement-worker
ARG TARGETARCH
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/measurement-worker/ cmd/measurement-worker/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /out/measurement-worker ./cmd/measurement-worker

FROM ${TRITON_IMAGE} AS batch-guard

# Only the public callback headers; no Triton core build or vendored headers.
RUN apt-get update && apt-get install -y --no-install-recommends g++ curl ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /tmp/include/triton/core \
    && curl --fail --location --silent --show-error https://raw.githubusercontent.com/triton-inference-server/core/r26.09/include/triton/core/tritonbackend.h -o /tmp/include/triton/core/tritonbackend.h \
    && curl --fail --location --silent --show-error https://raw.githubusercontent.com/triton-inference-server/core/r26.09/include/triton/core/tritonserver.h -o /tmp/include/triton/core/tritonserver.h
COPY runtime/triton/batchstrategy.cc /tmp/batchstrategy.cc
RUN g++ -std=c++17 -O2 -fPIC -shared -I/tmp/include /tmp/batchstrategy.cc -o /tmp/batchstrategy.so

FROM ${TRITON_IMAGE}

# The CPU engine pin matches measurement-worker.Dockerfile, not a custom runtime.
ARG ONNXRUNTIME_VERSION=1.27.0
ARG NUMPY_VERSION=1.26.4
ENV PYTHONPATH=/opt/chill
RUN python3 -m pip install --no-cache-dir numpy==${NUMPY_VERSION} onnxruntime==${ONNXRUNTIME_VERSION} \
    && python3 -c 'import numpy as np; import onnxruntime as ort; assert np.__version__ == "'"${NUMPY_VERSION}"'"; assert ort.__version__ == "'"${ONNXRUNTIME_VERSION}"'"; assert "CPUExecutionProvider" in ort.get_available_providers()'
COPY --from=batch-guard /tmp/batchstrategy.so /opt/chill/triton/batchstrategy.so
COPY runtime/triton/model.py runtime/triton/repository.py runtime/triton/serve.py /opt/chill/triton/
COPY runtime/ortcpu/runner.py /opt/chill/ortcpu/runner.py
COPY runtime/triton/runner.py /opt/chill/triton/runner.py
COPY --from=measurement-worker /out/measurement-worker /measurement-worker

# Repository preparation and profiling happen in this same runtime container;
# memory.peak therefore includes startup helper and native server/stub processes.
USER 65532:65532
ENTRYPOINT ["python3", "/opt/chill/triton/serve.py"]
CMD ["--model-repository=/models", "--strict-readiness=true", "--allow-grpc=false", "--allow-metrics=false"]
