#!/usr/bin/env bash
# ADR risk 3: Grype (our grypedb wrapper + real Anchore v6 DB) vs Trivy agreement.
set -euo pipefail
# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"

ensure_helper
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"

DB_ROOT="$CACHE/grype-db"
SBOM_DIR="$CACHE/sboms"
FIND_DIR="$CACHE/grype-findings"
TRIVY_DIR="$CACHE/trivy"
TRIVY_CACHE="$CACHE/trivy-cache"
COMPARE_OUT="$RESULTS/grype-trivy.json"
LISTING_URL="${HAETAE_BENCH_LISTING_URL:-https://grype.anchore.io/databases/v6/latest.json}"

mkdir -p "$DB_ROOT" "$SBOM_DIR" "$FIND_DIR" "$TRIVY_DIR" "$TRIVY_CACHE"

IMAGES=(
  "debian:12-slim"
  "ubuntu:24.04"
  "alpine:3.20"
  "node:22-bookworm"
)

log "fetching Grype v6 DB via grypedb.Refresh (listing=$LISTING_URL)"
HAETAE_BENCH_DB_DIR="$DB_ROOT" \
HAETAE_BENCH_LISTING_URL="$LISTING_URL" \
HAETAE_BENCH_DB_META="$RESULTS/grype-db-meta.json" \
  go test -tags bench -count=1 -timeout 20m -v -run TestFetchGrypeDB ./test/bench/

ACTIVE_DB="$(jq -r .activeDir "$RESULTS/grype-db-meta.json")"
if [[ -z "$ACTIVE_DB" || "$ACTIVE_DB" == "null" ]]; then
  log "FATAL: no active Grype DB dir"
  exit 1
fi
log "active Grype DB: $ACTIVE_DB"

# Pull Trivy image once (removed at end of run-all / this script cleanup if pulled).
TRIVY_IMG="aquasec/trivy:latest"
TRIVY_PULLED=0
if ! image_present "$TRIVY_IMG"; then
  log "pulling $TRIVY_IMG"
  docker pull "$TRIVY_IMG"
  TRIVY_PULLED=1
fi

# Warm Trivy DB once (downloads into cache volume).
log "warming Trivy vulnerability DB"
docker run --rm \
  -v "$TRIVY_CACHE:/root/.cache" \
  "$TRIVY_IMG" image --download-db-only >/dev/null

slugify() { echo "$1" | tr '/:' '__'; }

for img in "${IMAGES[@]}"; do
  slug="$(slugify "$img")"
  pulled=0
  if ! image_present "$img"; then
    log "pulling $img"
    docker pull "$img"
    pulled=1
  fi

  image_id="$(docker image inspect -f '{{.Id}}' "$img")"
  sbom_path="$SBOM_DIR/${slug}.json"
  log "SBOM via haetae-inventory for $img"
  # Host-side inventory (not memory-capped); agreement is about matching, not RSS.
  "$HELPER" --docker-image "$image_id" --out "$sbom_path"

  findings_path="$FIND_DIR/${slug}.json"
  log "Grype match via test/bench for $img"
  HAETAE_BENCH_SBOM="$sbom_path" \
  HAETAE_BENCH_ACTIVE_DB="$ACTIVE_DB" \
  HAETAE_BENCH_FINDINGS_OUT="$findings_path" \
    go test -tags bench -count=1 -timeout 20m -v -run TestMatchSbom ./test/bench/

  trivy_path="$TRIVY_DIR/${slug}.json"
  log "Trivy scan for $img"
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$TRIVY_CACHE:/root/.cache" \
    "$TRIVY_IMG" image --format json --scanners vuln --quiet "$img" >"$trivy_path"

  if [[ "$pulled" == "1" && "$img" != "alpine:3.20" ]]; then
    # Keep node if KEEP_NODE set; otherwise remove non-keep images now to free disk.
    if [[ "$img" == "node:22-bookworm" && "${KEEP_NODE_FOR_COMPARE:-0}" == "1" ]]; then
      :
    else
      safe_rmi "$img"
    fi
  fi
done

log "comparing finding sets"
python3 "$BENCH/compare_grype_trivy.py" \
  --sbom-dir "$SBOM_DIR" \
  --grype-dir "$FIND_DIR" \
  --trivy-dir "$TRIVY_DIR" \
  --images "${IMAGES[*]}" \
  --out "$COMPARE_OUT"

if [[ "$TRIVY_PULLED" == "1" ]]; then
  safe_rmi "$TRIVY_IMG"
fi

# Remove leftover compare images that are not in the keep list.
for img in "debian:12-slim" "ubuntu:24.04" "node:22-bookworm"; do
  if image_present "$img"; then
    safe_rmi "$img"
  fi
done

log "Grype vs Trivy comparison written to $COMPARE_OUT"
