#!/usr/bin/env bash
# Build probe-agent packages isolated by version and platform:
#   dist/probe-agent/<ver>/probe-agent-<ver>-<os>-<arch>/{probe-agent[.exe], probe-agent.env}
#   dist/probe-agent/<ver>/probe-agent-<ver>-<os>-<arch>.zip
#
# Usage: scripts/probe-agent-dist.sh [os/arch ...]   (default: all targets)
# AGENT_ENV points at the probe-agent.env template (contains GUEST_TOKEN; never commit it).
set -euo pipefail
cd "$(dirname "$0")/.."

ver=$(sed -n 's/^var agentVersion = "\(.*\)"/\1/p' cmd/probe-agent/main.go)
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
  # Windows: GUI subsystem, so the tray agent never opens a console window.
  # macOS: the menu bar icon needs cgo (Cocoa), so darwin builds only on a Mac.
  ldflags="-s -w" cgo=0
  [ "$os" = windows ] && ldflags="$ldflags -H windowsgui"
  if [ "$os" = darwin ]; then
    [ "$(uname -s)" = Darwin ] || { echo "darwin/$arch needs a macOS host (cgo for the menu bar icon)" >&2; exit 1; }
    cgo=1
  fi
  CGO_ENABLED=$cgo GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$dir/probe-agent$ext" ./cmd/probe-agent
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
# Agents only apply remote upgrades that carry a signature from the release key.
if go run ./cmd/probe-release sign "$out"; then :; else
  echo "  WARNING: $out is unsigned; agents will refuse to upgrade to it (run: go run ./cmd/probe-release keygen)" >&2
fi
# probe-hub offers the version this link points at as "latest".
ln -sfn "$ver" dist/probe-agent/latest
