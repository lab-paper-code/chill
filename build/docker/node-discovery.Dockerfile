FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/node-discovery/ cmd/node-discovery/
COPY internal/kubeconfig/ internal/kubeconfig/
COPY internal/metadata/ internal/metadata/
COPY internal/nodeprobe/ internal/nodeprobe/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /out/node-discovery ./cmd/node-discovery

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/node-discovery .
USER 65532:65532

ENTRYPOINT ["/node-discovery"]
