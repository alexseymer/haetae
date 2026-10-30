#!/usr/bin/env bash
# Run M7 ADR risk benchmarks ONE AT A TIME (4 GB RAM host; parallel e2e may be present).
set -euo pipefail
# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"

export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
export KEEP_NODE_FOR_COMPARE=1

BEFORE="$RESULTS/images-before.txt"
AFTER="$RESULTS/images-after.txt"
snapshot_images >"$BEFORE"
log "image snapshot before: $(wc -l <"$BEFORE") tags"
log "host RAM: $(free -h | awk '/Mem:/{print $2}') (expect ~4 GiB VM)"

log "=== 1/3 Syft peak memory ==="
bash "$BENCH/01-syft-peak.sh"

log "=== 2/3 Grype vs Trivy ==="
bash "$BENCH/02-grype-trivy.sh"

# node may still be present from KEEP_NODE; remove now if not in keep list.
if image_present "node:22-bookworm"; then
  safe_rmi "node:22-bookworm"
fi

log "=== 3/3 Agent RSS + conntrack heap ==="
bash "$BENCH/03-agent-rss.sh"

snapshot_images >"$AFTER"
log "image snapshot after: $(wc -l <"$AFTER") tags"
if ! diff -u "$BEFORE" "$AFTER"; then
  log "WARNING: docker image set changed; investigate extras"
  # Remove any new non-keep images that slipped through.
  while read -r img; do
    if ! grep -qxF "$img" "$BEFORE"; then
      log "removing leftover image $img"
      docker rmi -f "$img" >/dev/null 2>&1 || true
    fi
  done <"$AFTER"
  snapshot_images >"$AFTER"
fi

log "all M7 benches finished; fill docs/contributors/benchmarks-m7.md from $RESULTS"
