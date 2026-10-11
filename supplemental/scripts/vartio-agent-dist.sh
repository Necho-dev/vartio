#!/usr/bin/env bash
# Build vartio-agent packages isolated by version and platform:
#   dist/vartio-agent/<ver>/vartio-agent-<ver>-<os>-<arch>/{vartio-agent[.exe], vartio-agent.env}
#   dist/vartio-agent/<ver>/vartio-agent-<ver>-<os>-<arch>.zip
#
# Usage: supplemental/scripts/vartio-agent-dist.sh [os/arch ...]   (default: all targets)
# AGENT_ENV points at the env template (contains the token; never commit it).
set -euo pipefail
cd "$(dirname "$0")/../.."

ver=$(sed -n 's/^var Version = "\(.*\)"/\1/p' vartio.go)
[ -n "$ver" ] || { echo "Version not found in vartio.go" >&2; exit 1; }
if [ -n "${AGENT_ENV:-}" ]; then
  env_tpl=$AGENT_ENV
elif [ -f dist/vartio-agent/vartio-agent.env ]; then
  env_tpl=dist/vartio-agent/vartio-agent.env
else
  env_tpl=dist/vartio-agent/probe-agent.env
fi
out=dist/vartio-agent/$ver
targets=("$@")
[ ${#targets[@]} -gt 0 ] || targets=(windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64)

# macOS keeps Screen Recording grants across upgrades only if every build has the
# same identifier and certificate. The identifier stays probe-agent.
sign_dir=${VARTIO_SIGN_DIR:-${PROBE_SIGN_DIR:-$HOME/.config/probe-agent}}
sign_kc=$sign_dir/signing.keychain-db
sign_name=${VARTIO_SIGN_NAME:-${PROBE_SIGN_NAME:-probe-agent self-signed}}
sign_darwin() {
  if [ -f "$sign_kc" ] && [ -f "$sign_dir/signing.pass" ]; then
    security unlock-keychain -p "$(cat "$sign_dir/signing.pass")" "$sign_kc"
    local list=() line rc=0
    while IFS= read -r line; do
      line=${line#"${line%%[![:space:]]*}"}
      line=${line#\"}
      list+=("${line%\"}")
    done < <(security list-keychains -d user)
    security list-keychains -d user -s "${list[@]}" "$sign_kc"
    codesign --force --sign "$sign_name" --identifier probe-agent --timestamp=none "$1" || rc=$?
    security list-keychains -d user -s "${list[@]}"
    return $rc
  else
    echo "  WARNING: no signing identity, $1 is ad-hoc signed; macOS asks for Screen Recording again after each upgrade" >&2
    codesign --force --sign - --identifier probe-agent "$1"
  fi
}

echo "vartio-agent $ver -> $out"
[ -f "$env_tpl" ] || echo "  (no $env_tpl, packages ship without an env file)"
mkdir -p "$out"
for t in "${targets[@]}"; do
  os=${t%/*} arch=${t#*/}
  name=vartio-agent-$ver-$os-$arch
  dir=$out/$name
  ext=; [ "$os" = windows ] && ext=.exe
  rm -rf "$dir" "$dir.zip"
  mkdir -p "$dir"
  ldflags="-s -w" cgo=0
  [ "$os" = windows ] && ldflags="$ldflags -H windowsgui"
  if [ "$os" = darwin ]; then
    [ "$(uname -s)" = Darwin ] || { echo "darwin/$arch needs a macOS host (cgo for the menu bar icon)" >&2; exit 1; }
    cgo=1
  fi
  CGO_ENABLED=$cgo GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$dir/vartio-agent$ext" ./internal/cmd/agent
  [ "$os" = darwin ] && sign_darwin "$dir/vartio-agent"
  if [ -f "$env_tpl" ]; then
    if [ "$os" = windows ]; then
      tr -d '\r' <"$env_tpl" | sed 's/$/\r/' >"$dir/vartio-agent.env"
    else
      tr -d '\r' <"$env_tpl" >"$dir/vartio-agent.env"
    fi
  fi
  (cd "$out" && zip -qr "$name.zip" "$name")
  echo "  $dir(.zip)"
done
if go run ./internal/cmd/release sign "$out"; then :; else
  echo "  WARNING: $out is unsigned; agents will refuse to upgrade to it (run: go run ./internal/cmd/release keygen)" >&2
fi
ln -sfn "$ver" dist/vartio-agent/latest
