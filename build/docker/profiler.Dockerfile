FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/profiler/ cmd/profiler/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /out/profiler ./cmd/profiler

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/profiler .
USER 65532:65532

ENTRYPOINT ["/profiler"]
