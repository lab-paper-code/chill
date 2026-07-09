FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/controller/ internal/controller/
COPY internal/deviceclasscatalog/ internal/deviceclasscatalog/
COPY internal/discoverycontroller/ internal/discoverycontroller/
COPY internal/labels/ internal/labels/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /out/manager cmd/main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
