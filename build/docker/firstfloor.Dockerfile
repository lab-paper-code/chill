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

# JetPack 5.1.2 base (L4T r35.4.1, CUDA 11.4, cuDNN 8.6). Its Ubuntu 20.04
# Python is 3.8, so this stage adds a standalone CPython 3.11 and NVIDIA's
# JetPack 5.1.1 onnxruntime-gpu 1.16.0 cp311 wheel (same CUDA/cuDNN); the 1.17
# and 1.18 wheels need GLIBCXX_3.4.29, newer than Ubuntu 20.04's libstdc++.
# Select with RUNTIME_IMAGE=l4t-r35; other builds skip this stage.
FROM scratch AS l4t-r35-downloads
ADD --checksum=sha256:643954c07b04c18e215f812691cd5fa15d924fec1f3a5fdd9c1789484382ac24 \
    https://github.com/astral-sh/python-build-standalone/releases/download/20261013/cpython-3.11.17%2B20261013-aarch64-unknown-linux-gnu-install_only.tar.gz /python.tar.gz
ADD --checksum=sha256:9ec67e08fc25ea34cef4c17efba9fb32b7db970c3d8782323ad95c2997a6cfb1 \
    https://nvidia.box.com/shared/static/g74cjqh8fcyd9faobm7yeif8mmxdvc0g.whl /onnxruntime_gpu-1.16.0-cp311-cp311-linux_aarch64.whl

FROM nvcr.io/nvidia/l4t-jetpack:r35.4.1@sha256:d1c8e971ab994235840eacc31c4ef4173bf9156317b1bf8aabe7e01eb21b2a0e AS l4t-r35
ENV PATH=/opt/python/bin:${PATH}
RUN --mount=from=l4t-r35-downloads,target=/downloads tar -C /opt -xzf /downloads/python.tar.gz \
    && python3 -m pip install --no-cache-dir --no-deps /downloads/onnxruntime_gpu-1.16.0-cp311-cp311-linux_aarch64.whl

# JetPack 4.6 (L4T r32, CUDA 10.2, cuDNN 8.2) for Jetson Nano. The JetPack 4.6.1
# TensorRT runtime image carries the CUDA 10.2 and cuDNN 8.2 user libraries; the
# NVIDIA container runtime mounts only the host driver (l4t.csv). NVIDIA's
# JetPack 4 onnxruntime-gpu 1.11.0 cp38 wheel is the newest there, so this stage
# runs Ubuntu 18.04's Python 3.8 in a venv. Select with RUNTIME_IMAGE=l4t-r32;
# other builds skip this stage.
FROM scratch AS l4t-r32-downloads
ADD --checksum=sha256:127a6f95ba315b8a7a71ac52d1f4f68cd225a4887e77f11ea24091cd0211b185 \
    https://nvidia.box.com/shared/static/2sv2fv1wseihaw8ym0d4srz41dzljwxh.whl /onnxruntime_gpu-1.11.0-cp38-cp38-linux_aarch64.whl

FROM nvcr.io/nvidia/l4t-tensorrt:r8.2.1-runtime@sha256:3ecac9f197c475abeb284211f6be4277ad59cf3e38ddfa3deda203cedffeeba1 AS l4t-r32
ENV PATH=/opt/python/bin:${PATH}
RUN --mount=from=l4t-r32-downloads,target=/downloads apt-get update \
    && apt-get install -y --no-install-recommends python3.8 python3.8-venv && rm -rf /var/lib/apt/lists/* \
    && python3.8 -m venv /opt/python && python3 -m pip install --no-cache-dir pip==24.3.1 \
    && python3 -m pip install --no-cache-dir --no-deps /downloads/onnxruntime_gpu-1.11.0-cp38-cp38-linux_aarch64.whl

# CPU is the default. Vendor variants are the Makefile docker-buildx-firstfloor-*
# targets; see docs/design/code-intelligence/README.md.
FROM ${RUNTIME_IMAGE}
ARG APT_PACKAGES
ARG NUMPY_VERSION=2.2.6
ARG ONNXRUNTIME_VERSION=1.27.0
ARG ORT_PACKAGE=onnxruntime
ARG PIP_EXTRA_INDEX_URL
ARG EXTRA_PACKAGES
# Preprocessor module libraries. transformers 4.50.0 is the first release with
# SmolVLM; its processor module imports without torch (4.57.6 requires torch).
ARG MODULE_PACKAGES="pillow==12.3.0 transformers==4.50.0 num2words==0.5.14 jinja2==3.1.6"
RUN if [ -n "${APT_PACKAGES}" ]; then apt-get update \
    && apt-get install -y --no-install-recommends ${APT_PACKAGES} && rm -rf /var/lib/apt/lists/*; fi
RUN python3 -m pip install --no-cache-dir numpy==${NUMPY_VERSION} ${ORT_PACKAGE}==${ONNXRUNTIME_VERSION} ${EXTRA_PACKAGES} ${MODULE_PACKAGES}
COPY --from=builder /out/serving-worker /opt/chill/bin/serving-worker
COPY --from=builder /out/serving-entrypoint /opt/chill/bin/serving-entrypoint
COPY --from=builder /out/measurement-worker /measurement-worker
COPY runtime/firstfloor/ /opt/chill/runtime/firstfloor/
COPY runtime/ortcpu/runner.py /opt/chill/runtime/ortcpu/runner.py
COPY runtime/rknnlite/runner.py /opt/chill/runtime/rknnlite/runner.py
ENV PYTHONDONTWRITEBYTECODE=1
USER 65532:65532
ENTRYPOINT ["/opt/chill/bin/serving-worker"]
