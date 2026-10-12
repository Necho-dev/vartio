.PHONY: vartio vartio-build vartio-webcheck vartio-agent vartio-agent-headless vartio-agent-dist vartio-agent-windows test docker-agent docker-hub docker-images docker-push

# Hub（不连 ESXi）
vartio:
	go run ./internal/cmd/hub

# Agent（出站连 Hub :8766）。macOS 默认带 cgo，菜单栏可用。
vartio-agent:
	go run ./internal/cmd/agent

# 无 cgo 的轻量后台进程：采集与带 cgo 的构建相同（含 GPU），没有菜单栏，也没有本机画面。
vartio-agent-headless:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/vartio-agent-headless ./internal/cmd/agent

# 分发包按版本 / 平台隔离：dist/vartio-agent/<ver>/vartio-agent-<ver>-<os>-<arch>/
vartio-agent-dist:
	supplemental/scripts/vartio-agent-dist.sh

vartio-agent-windows:
	supplemental/scripts/vartio-agent-dist.sh windows/amd64 windows/arm64

vartio-build:
	mkdir -p dist
	go build -trimpath -o dist/vartio ./internal/cmd/hub

vartio-webcheck:
	npx --yes -p typescript@5.6 tsc -p internal/site

test:
	go test ./...

DOCKER_TAG ?= $(shell sed -n 's/^var Version = "\(.*\)"/\1/p' vartio.go)
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

docker-agent:
	docker build --target release -f supplemental/docker/agent/vartio-agent.Dockerfile -t vartio-agent:$(DOCKER_TAG) -t vartio-agent:latest .
	docker build --target edge -f supplemental/docker/agent/vartio-agent.Dockerfile -t vartio-agent:edge .

docker-hub:
	docker build -f supplemental/docker/hub/vartio.Dockerfile -t vartio:$(DOCKER_TAG) -t vartio:latest .

docker-images: docker-agent docker-hub

docker-push:
	@test -n "$(REGISTRY)" || (echo "REGISTRY=... required" && exit 1)
	docker buildx build --target release --platform $(DOCKER_PLATFORMS) -f supplemental/docker/agent/vartio-agent.Dockerfile \
		-t $(REGISTRY)/vartio-agent:$(DOCKER_TAG) -t $(REGISTRY)/vartio-agent:latest --push .
	docker buildx build --target edge --platform $(DOCKER_PLATFORMS) -f supplemental/docker/agent/vartio-agent.Dockerfile \
		-t $(REGISTRY)/vartio-agent:edge --push .
	docker buildx build --platform $(DOCKER_PLATFORMS) -f supplemental/docker/hub/vartio.Dockerfile \
		-t $(REGISTRY)/vartio:$(DOCKER_TAG) -t $(REGISTRY)/vartio:latest --push .
