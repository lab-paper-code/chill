# syntax=docker/dockerfile:1.7
# power-source context MUST be git archive d39c9576f5b2f4ee6a48034e6b95eba28e43cd4e.
# This is the source in bin/observation-power-source-d39c9576.receipt.json,
# not the current observation reducer or any runtime7 image.
FROM --platform=$BUILDPLATFORM golang:1.23 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY hack/e5/publish/ hack/e5/publish/
COPY cmd/evaluation-evidence/ cmd/evaluation-evidence/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/e5-publish ./hack/e5/publish && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/evaluation-evidence ./cmd/evaluation-evidence
COPY --from=power-source / /power/
WORKDIR /power
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/observation-power-source-d39c9576 ./hack/observations/power

FROM python:3.12-slim-bookworm
ARG SOURCE_SHA
ARG TARGETARCH=amd64
# Must stay within one minor of the destination cluster (currently Kubernetes 1.31).
ARG KUBECTL_VERSION=v1.31.14
LABEL org.opencontainers.image.revision=$SOURCE_SHA
ENV PYTHONDONTWRITEBYTECODE=1 HOME=/tmp CHILL_HARNESS_SOURCE_SHA=$SOURCE_SHA
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client curl && rm -rf /var/lib/apt/lists/* && curl -fsSL https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl -o /usr/bin/kubectl && chmod 0755 /usr/bin/kubectl
WORKDIR /opt/e5
COPY hack/e5/ hack/e5/
COPY load-generator/ load-generator/
COPY docs/spec/README.md docs/spec/README.md
COPY docs/design/serving/workload-pools.md docs/design/serving/workload-pools.md
COPY --from=builder /out/ bin/
RUN pip install --no-cache-dir ./load-generator && python3 -c 'import hashlib,json,pathlib; p=pathlib.Path("/opt/e5/bin/observation-power-source-d39c9576"); p.with_suffix(".receipt.json").write_text(json.dumps({"sourceSHA":"d39c9576f5b2f4ee6a48034e6b95eba28e43cd4e","executable":str(p),"sha256":hashlib.sha256(p.read_bytes()).hexdigest(),"buildExitCode":0,"buildArgv":["go","build","./hack/observations/power"]}))'
USER 65532:65532
ENTRYPOINT ["python3", "/opt/e5/hack/e5/evaluation.py"]
