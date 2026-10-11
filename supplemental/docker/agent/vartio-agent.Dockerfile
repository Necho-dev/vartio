# syntax=docker/dockerfile:1
# vartio-agent in a container. Build from the repo root:
#   docker build -f supplemental/docker/agent/vartio-agent.Dockerfile -t vartio-agent .
# Run it with supplemental/docker/agent/compose.agent.yml.

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party third_party
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/vartio-agent ./internal/cmd/agent

FROM alpine:3.22 AS runtime
RUN apk add --no-cache bash ca-certificates tzdata util-linux-misc
COPY --from=build /out/vartio-agent /usr/local/bin/vartio-agent
ENV VARTIO_AGENT_RUNNER=docker VARTIO_AGENT_STATE_DIR=/var/lib/vartio-agent SHELL=/bin/bash
VOLUME /var/lib/vartio-agent
ENTRYPOINT ["/usr/local/bin/vartio-agent"]

# edge follows the hub's signed latest: remote push and auto update are on.
FROM runtime AS edge
ENV VARTIO_AGENT_UPGRADE=on GUEST_UPGRADE=on VARTIO_AGENT_AUTO_UPDATE=on GUEST_AUTO_UPDATE=on

# Default target. Version tags and :latest ship this stage.
FROM runtime AS release
