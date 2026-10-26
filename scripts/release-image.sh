#!/usr/bin/env bash
# Builds the server image from bin/release. Never pushes. Usage: scripts/release-image.sh <version>
set -euo pipefail
VERSION="${1:?version}"
cd "$(dirname "$0")/.."
R=bin/release
[ -f "$R/haetae-server-linux-amd64" ] || { echo "run make release first" >&2; exit 1; }
CTX=bin/release-ctx
rm -rf "$CTX"; mkdir -p "$CTX/dist" "$CTX/data"
touch "$CTX/data/.keep"
cp "$R"/haetae-server-linux-amd64 "$R"/haetae-server-linux-arm64 "$CTX/"
cp bin/dist/haetae-* bin/dist/SHA256SUMS "$CTX/dist/"
cp scripts/Dockerfile.release "$CTX/Dockerfile"

if docker buildx version >/dev/null 2>&1; then
  echo "buildx available: building linux/amd64+linux/arm64 OCI tarball (not pushed)"
  docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION="$VERSION" \
    --output "type=oci,dest=$R/haetae-server-$VERSION.oci.tar" "$CTX"
  # An OCI multi-arch output cannot be loaded into the classic daemon; also load amd64 for local use/tests.
  docker buildx build --platform linux/amd64 --build-arg VERSION="$VERSION" \
    -t "haetae-server:$VERSION" -t haetae-server:latest --load "$CTX"
else
  echo "docker buildx NOT available: building linux/amd64 image only (no multi-arch manifest, no OCI tarball)"
  DOCKER_BUILDKIT=0 docker build --build-arg VERSION="$VERSION" --build-arg TARGETARCH=amd64 \
    -t "haetae-server:$VERSION" -t haetae-server:latest "$CTX"
fi
docker image ls haetae-server
