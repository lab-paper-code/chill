FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/node-agent/ cmd/node-agent/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /out/node-agent ./cmd/node-agent

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/node-agent .
USER 65532:65532

ENTRYPOINT ["/node-agent"]
