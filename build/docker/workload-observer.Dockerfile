FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/workload-observer/ cmd/workload-observer/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /out/workload-observer ./cmd/workload-observer

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/workload-observer .
USER 65532:65532

ENTRYPOINT ["/workload-observer"]
