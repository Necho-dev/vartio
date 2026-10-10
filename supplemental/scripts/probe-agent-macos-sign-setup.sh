#!/usr/bin/env bash
# One-time setup of a stable code-signing identity for macOS probe-agent builds.
#
# macOS ties privacy grants (Screen Recording) to a binary's designated
# requirement. Ad-hoc signatures pin it to the exact build hash, so every
# upgrade loses the grant. Signing every build with the same certificate and
# identifier keeps the requirement stable and the grant survives upgrades.
#
# Creates a self-signed certificate in a dedicated keychain
# ($PROBE_SIGN_DIR, default ~/.config/probe-agent) protected by a random
# password stored next to it (0600). Nothing is added to the login keychain.
# Keep this directory private and back it up: a new certificate means one more
# Screen Recording grant on every Mac.
set -euo pipefail
[ "$(uname -s)" = Darwin ] || { echo "macOS only" >&2; exit 1; }

dir=${PROBE_SIGN_DIR:-$HOME/.config/probe-agent}
kc=$dir/signing.keychain-db
pass_file=$dir/signing.pass
cn=${PROBE_SIGN_NAME:-probe-agent self-signed}

if [ -f "$kc" ]; then
  echo "already set up: $kc"
  exit 0
fi
mkdir -p "$dir"
chmod 700 "$dir"
umask 077
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

openssl rand -hex 24 >"$pass_file"
pw=$(cat "$pass_file")

cat >"$tmp/cert.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $cn
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 7300 -config "$tmp/cert.cnf" \
  -keyout "$tmp/key.pem" -out "$tmp/cert.pem" 2>/dev/null
legacy=; openssl version | grep -q '^OpenSSL 3' && legacy=-legacy
openssl pkcs12 -export $legacy -inkey "$tmp/key.pem" -in "$tmp/cert.pem" -name "$cn" \
  -out "$tmp/id.p12" -passout "pass:$pw"

security create-keychain -p "$pw" "$kc"
security set-keychain-settings "$kc"
security unlock-keychain -p "$pw" "$kc"
security import "$tmp/id.p12" -k "$kc" -P "$pw" -T /usr/bin/codesign >/dev/null
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$pw" "$kc" >/dev/null

echo "created signing identity \"$cn\" in $kc"
