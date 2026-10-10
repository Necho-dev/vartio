# syntax=docker/dockerfile:1
# probe-hub (monitoring UI embedded). Build from the repo root:
#   docker build -f deploy/docker/probe-hub.Dockerfile -t probe-hub .
# Run it with deploy/docker/compose.hub.yml (PostgreSQL for history).

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party third_party
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/probe-hub ./cmd/probe-hub

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/probe-hub /usr/local/bin/probe-hub
# State files (plugins, forwards) live in the working directory; agent
# releases are offered from /data/dist/probe-agent when mounted there.
WORKDIR /data
VOLUME /data
ENV PROBE_HUB_LISTEN=0.0.0.0:8766
EXPOSE 8766
ENTRYPOINT ["/usr/local/bin/probe-hub"]
