FROM --platform=$BUILDPLATFORM golang:1.22 AS build
# TODO(internal): Pin builder/runtime images by digest and publish one audited
# multi-arch MetricObserver image through the normal CHILL image pipeline.

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/metricobserver/ internal/metricobserver/
COPY spikes/metric-observer/ spikes/metric-observer/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /metric-observer ./spikes/metric-observer/cmd/probe

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /metric-observer /metric-observer
ENTRYPOINT ["/metric-observer"]
