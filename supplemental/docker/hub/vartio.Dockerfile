# syntax=docker/dockerfile:1
# vartio hub (monitoring UI embedded). Build from the repo root:
#   docker build -f supplemental/docker/hub/vartio.Dockerfile -t vartio .
# Run it with supplemental/docker/hub/compose.hub.yml (PostgreSQL for history).

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party third_party
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/vartio ./internal/cmd/hub

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/vartio /usr/local/bin/vartio
WORKDIR /data
VOLUME /data
ENV VARTIO_LISTEN=0.0.0.0:8766
EXPOSE 8766
ENTRYPOINT ["/usr/local/bin/vartio"]
