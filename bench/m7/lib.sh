# Shared helpers for M7 ADR risk benchmarks. Sourced by numbered scripts.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BENCH="$ROOT/bench/m7"
CACHE="$BENCH/.cache"
RESULTS="$BENCH/results"
HELPER="${HELPER:-$ROOT/bin/haetae-inventory}"
AGENT_BIN="${AGENT_BIN:-$ROOT/bin/haetae-agent}"
SERVER_BIN="${SERVER_BIN:-$ROOT/bin/haetae-server}"

KEEP_IMAGES=(
  "htest-5dd804d5-bad-web:latest"
  "htest-5dd804d5-sec-web:latest"
  "htest-f12d16f5-bad-web:latest"
  "htest-f12d16f5-sec-web:latest"
  "haetae-server:dev"
  "nginx:alpine"
  "postgres:17-alpine"
  "alpine:3.20"
  "hello-world:latest"
  "caddy:2.10.2-alpine"
  "curlimages/curl:8.11.1"
  "busybox:1.37"
  "gcr.io/distroless/static-debian12:nonroot"
  "registry:2"
)

mkdir -p "$CACHE" "$RESULTS"

log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

host_ram_bytes() {
  awk '/MemTotal:/ {print $2 * 1024}' /proc/meminfo
}

image_present() {
  docker image inspect "$1" >/dev/null 2>&1
}

# Remove an image only if it was not in the pre-bench keep set and not alpine:3.20.
safe_rmi() {
  local ref="$1"
  local keep
  for keep in "${KEEP_IMAGES[@]}"; do
    if [[ "$ref" == "$keep" ]]; then
      log "keeping pre-existing image $ref"
      return 0
    fi
  done
  # Also keep by ID match against keep list repos we may have retagged.
  if [[ "$ref" == "alpine:3.20" ]]; then
    return 0
  fi
  log "removing image $ref"
  docker rmi -f "$ref" >/dev/null 2>&1 || true
}

ensure_helper() {
  if [[ ! -x "$HELPER" ]]; then
    log "building haetae-inventory"
    (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o bin/haetae-inventory ./cmd/haetae-inventory)
  fi
}

ensure_agent_server() {
  if [[ ! -x "$AGENT_BIN" ]]; then
    (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o bin/haetae-agent ./cmd/haetae-agent)
  fi
  if [[ ! -x "$SERVER_BIN" ]]; then
    (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o bin/haetae-server ./cmd/haetae-server)
  fi
}

# Snapshot of Repository:Tag currently present (for restore checks).
snapshot_images() {
  docker image ls --format '{{.Repository}}:{{.Tag}}' | sort -u
}
