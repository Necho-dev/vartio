# syntax=docker/dockerfile:1
# probe-agent in a container. Build from the repo root:
#   docker build -f deploy/docker/probe-agent.Dockerfile -t probe-agent .
# Run it with deploy/docker/compose.agent.yml (host pid/network, docker.sock, host / at /hostfs).

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party third_party
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/probe-agent ./cmd/probe-agent

FROM alpine:3.22 AS runtime
# bash for the terminal, nsenter (util-linux-misc) for GUEST_HOST_NS=1.
RUN apk add --no-cache bash ca-certificates tzdata util-linux-misc
COPY --from=build /out/probe-agent /usr/local/bin/probe-agent
ENV PROBE_AGENT_RUNNER=docker GUEST_STATE_DIR=/var/lib/probe-agent SHELL=/bin/bash
VOLUME /var/lib/probe-agent
ENTRYPOINT ["/usr/local/bin/probe-agent"]

# edge follows the hub's signed latest: remote push and auto update are on.
# Recreating the container returns to the binary baked into this image.
FROM runtime AS edge
ENV GUEST_UPGRADE=on GUEST_AUTO_UPDATE=on

# Default target. Version tags and :latest ship this stage, so the running
# binary stays the one in the image until the container is recreated.
FROM runtime AS release
