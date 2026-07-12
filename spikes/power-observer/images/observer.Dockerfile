FROM --platform=$BUILDPLATFORM golang:1.22 AS build
# TODO(internal): Pin builder/runtime images by digest and publish one audited
# multi-arch PowerObserver image through the normal CHILL image pipeline.

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/powerobserver/ internal/powerobserver/
COPY spikes/power-observer/ spikes/power-observer/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /power-observer ./spikes/power-observer/cmd/probe

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /power-observer /power-observer
ENTRYPOINT ["/power-observer"]
