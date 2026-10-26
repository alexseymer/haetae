#!/usr/bin/env bash
# Builds release artifacts into bin/release (called by `make release`).
# Expects bin/dist (make dist) and internal/webui/app (make web) to be ready.
# Usage: scripts/release.sh <version> <commit>
set -euo pipefail
VERSION="${1:?version}"
COMMIT="${2:?commit}"
cd "$(dirname "$0")/.."
OUT=bin/release
LDFLAGS="-s -w -X github.com/alexseymer/haetae/internal/version.Version=${VERSION} -X github.com/alexseymer/haetae/internal/version.Commit=${COMMIT}"

[ -f internal/webui/app/index.html ] || { echo "internal/webui/app/index.html missing: run make web first" >&2; exit 1; }
rm -rf "$OUT"
mkdir -p "$OUT"
printf '%s\n' "$VERSION" > "$OUT/VERSION"

for arch in amd64 arm64; do
  # agent and inventory come from make dist (same binaries the server serves at /download/).
  cp "bin/dist/haetae-agent-linux-$arch" "bin/dist/haetae-inventory-linux-$arch" "$OUT/"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" \
    -o "$OUT/haetae-server-linux-$arch" ./cmd/haetae-server

  name="haetae-${VERSION}-linux-${arch}"
  stage="$OUT/.stage/$name"
  mkdir -p "$stage"
  cp "$OUT/haetae-agent-linux-$arch" "$stage/haetae-agent"
  cp "$OUT/haetae-inventory-linux-$arch" "$stage/haetae-inventory"
  cp "$OUT/haetae-server-linux-$arch" "$stage/haetae-server"
  cp LICENSE README.md "$stage/"
  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='2000-01-01 00:00:00Z' \
    -C "$OUT/.stage" -cf - "$name" | gzip -n -9 > "$OUT/$name.tar.gz"
done
rm -rf "$OUT/.stage"

(cd "$OUT" && sha256sum \
  haetae-agent-linux-amd64 haetae-agent-linux-arm64 \
  haetae-inventory-linux-amd64 haetae-inventory-linux-arm64 \
  haetae-server-linux-amd64 haetae-server-linux-arm64 \
  "haetae-${VERSION}-linux-amd64.tar.gz" "haetae-${VERSION}-linux-arm64.tar.gz" > SHA256SUMS)
ls -l "$OUT"
