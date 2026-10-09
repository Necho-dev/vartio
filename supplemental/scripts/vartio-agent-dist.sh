#!/usr/bin/env bash
# Build probe-agent packages isolated by version and platform:
#   dist/probe-agent/<ver>/probe-agent-<ver>-<os>-<arch>/{probe-agent[.exe], probe-agent.env}
#   dist/probe-agent/<ver>/probe-agent-<ver>-<os>-<arch>.zip
#
# Usage: scripts/probe-agent-dist.sh [os/arch ...]   (default: all targets)
# AGENT_ENV points at the probe-agent.env template (contains GUEST_TOKEN; never commit it).
set -euo pipefail
cd "$(dirname "$0")/.."

ver=$(sed -n 's/^const agentVersion = "\(.*\)"/\1/p' cmd/probe-agent/main.go)
[ -n "$ver" ] || { echo "agentVersion not found in cmd/probe-agent/main.go" >&2; exit 1; }
env_tpl=${AGENT_ENV:-dist/probe-agent/probe-agent.env}
out=dist/probe-agent/$ver
targets=("$@")
[ ${#targets[@]} -gt 0 ] || targets=(windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64)

echo "probe-agent $ver -> $out"
[ -f "$env_tpl" ] || echo "  (no $env_tpl, packages ship without probe-agent.env)"
mkdir -p "$out"
for t in "${targets[@]}"; do
  os=${t%/*} arch=${t#*/}
  name=probe-agent-$ver-$os-$arch
  dir=$out/$name
  ext=; [ "$os" = windows ] && ext=.exe
  rm -rf "$dir" "$dir.zip"
  mkdir -p "$dir"
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$dir/probe-agent$ext" ./cmd/probe-agent
  if [ -f "$env_tpl" ]; then
    if [ "$os" = windows ]; then
      tr -d '\r' <"$env_tpl" | sed 's/$/\r/' >"$dir/probe-agent.env"
    else
      tr -d '\r' <"$env_tpl" >"$dir/probe-agent.env"
    fi
  fi
  (cd "$out" && zip -qr "$name.zip" "$name")
  echo "  $dir(.zip)"
done
ln -sfn "$ver" dist/probe-agent/latest
