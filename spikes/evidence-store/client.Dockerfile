FROM --platform=${BUILDPLATFORM} golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY internal/evidencestore/ internal/evidencestore/
COPY spikes/evidence-store/cmd/client/ spikes/evidence-store/cmd/client/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -o /out/evidence-store-client ./spikes/evidence-store/cmd/client

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /out/evidence-store-client /evidence-store-client
USER 65532:65532

ENTRYPOINT ["/evidence-store-client"]
