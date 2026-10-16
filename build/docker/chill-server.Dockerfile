FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/chill-server/ cmd/chill-server/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
	go build -a -o /out/chill-server ./cmd/chill-server

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/chill-server .
USER 65532:65532

ENTRYPOINT ["/chill-server"]
