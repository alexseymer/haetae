#!/usr/bin/env bash
# ADR risk 2: Syft / haetae-inventory peak memory inside a 256 MB (and 128 MB) Docker memory limit.
# Runs ONE image at a time; removes pulled images that were not pre-existing.
set -euo pipefail
# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"

ensure_helper

OUT="$RESULTS/syft-peak.jsonl"
: >"$OUT"

run_one() {
  local image_ref="$1"
  local mem_limit="$2" # e.g. 256m
  local name="haetae-m7-syft-$$"
  local image_id
  image_id="$(docker image inspect -f '{{.Id}}' "$image_ref")"

  log "inventory $image_ref ($image_id) under --memory $mem_limit"

  local started ended wall_s exit_code oom=false
  local stdout_file stderr_file
  stdout_file="$(mktemp)"
  stderr_file="$(mktemp)"
  started="$(date +%s.%N)"

  # Detached so we can inspect OOMKilled after exit; helper is a static binary.
  set +e
  docker run -d --name "$name" \
    --memory "$mem_limit" --memory-swap "$mem_limit" --cpus 1 \
    -v "$HELPER:/haetae-inventory:ro" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    alpine:3.20 \
    /haetae-inventory --docker-image "$image_id" >"$CACHE/docker-create-$name.id" 2>"$stderr_file"
  local create_rc=$?
  set -e
  if [[ $create_rc -ne 0 ]]; then
    log "docker run create failed: $(cat "$stderr_file")"
    rm -f "$stdout_file" "$stderr_file"
    return 0
  fi

  # Wait for exit; capture docker stats samples while running.
  local stats_peak_mib=0
  local cgroup_peak=""
  while docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null | grep -q true; do
    local sample
    sample="$(docker stats --no-stream --format '{{.MemUsage}}' "$name" 2>/dev/null || true)"
    # MemUsage like "123.4MiB / 256MiB"
    if [[ "$sample" =~ ([0-9.]+)([KMGi]+) ]]; then
      local val="${BASH_REMATCH[1]}"
      local unit="${BASH_REMATCH[2]}"
      local mib
      case "$unit" in
        KiB|KB) mib="$(awk -v v="$val" 'BEGIN{printf "%.3f", v/1024}')" ;;
        MiB|MB) mib="$val" ;;
        GiB|GB) mib="$(awk -v v="$val" 'BEGIN{printf "%.3f", v*1024}')" ;;
        *) mib="0" ;;
      esac
      awk -v a="$stats_peak_mib" -v b="$mib" 'BEGIN{if(b+0>a+0) print b; else print a}' >/dev/null
      if awk -v a="$stats_peak_mib" -v b="$mib" 'BEGIN{exit !(b+0>a+0)}'; then
        stats_peak_mib="$mib"
      fi
    fi
    # Best-effort cgroup memory.peak (cgroup v2 path under docker).
    local cid
    cid="$(docker inspect -f '{{.Id}}' "$name" 2>/dev/null || true)"
    if [[ -n "$cid" ]]; then
      for p in \
        "/sys/fs/cgroup/system.slice/docker-${cid}.scope/memory.peak" \
        "/sys/fs/cgroup/docker/${cid}/memory.peak" \
        "/sys/fs/cgroup/memory/docker/${cid}/memory.peak"; do
        if [[ -r "$p" ]]; then
          cgroup_peak="$(cat "$p" 2>/dev/null || true)"
          break
        fi
      done
    fi
    sleep 0.2
  done

  docker wait "$name" >/dev/null 2>&1 || true
  ended="$(date +%s.%N)"
  wall_s="$(awk -v a="$started" -v b="$ended" 'BEGIN{printf "%.3f", b-a}')"
  exit_code="$(docker inspect -f '{{.State.ExitCode}}' "$name")"
  oom="$(docker inspect -f '{{.State.OOMKilled}}' "$name")"
  docker logs "$name" >"$stdout_file" 2>>"$stderr_file" || true
  docker rm -f "$name" >/dev/null 2>&1 || true

  local peak_rss=0 packages=0 tool_version="" parse_ok=false
  if [[ -s "$stdout_file" ]] && jq -e . >/dev/null 2>&1 <"$stdout_file"; then
    parse_ok=true
    peak_rss="$(jq -r '.peakRssBytes // 0' "$stdout_file")"
    packages="$(jq -r '.packages | length' "$stdout_file")"
    tool_version="$(jq -r '.toolVersion // ""' "$stdout_file")"
  fi

  local agent_report=""
  if [[ "$oom" == "true" ]] || [[ "$exit_code" -ne 0 && "$oom" == "true" ]]; then
    # Launcher maps cgroup OOM to status=oom, error="inventory out of memory";
    # UI: "Scan failed: inventory out of memory" (status oom). Evidence phrasing: "scan failed: oom".
    agent_report="scan failed: oom"
  elif [[ "$exit_code" -ne 0 ]]; then
    agent_report="scan failed"
  else
    agent_report="ok"
  fi

  jq -nc \
    --arg image "$image_ref" \
    --arg imageId "$image_id" \
    --arg memLimit "$mem_limit" \
    --argjson exitCode "$exit_code" \
    --argjson oomKilled "$([[ "$oom" == "true" ]] && echo true || echo false)" \
    --argjson wallSeconds "$wall_s" \
    --argjson peakRssBytes "$peak_rss" \
    --argjson packageCount "$packages" \
    --arg toolVersion "$tool_version" \
    --arg dockerStatsPeakMiB "$stats_peak_mib" \
    --arg cgroupMemoryPeakBytes "$cgroup_peak" \
    --argjson parseOk "$parse_ok" \
    --arg agentWouldReport "$agent_report" \
    --arg hostRamBytes "$(host_ram_bytes)" \
    --arg stderr "$(tail -c 2000 "$stderr_file" | tr '\n' ' ')" \
    '{
      image: $image,
      imageId: $imageId,
      memLimit: $memLimit,
      exitCode: $exitCode,
      oomKilled: $oomKilled,
      wallSeconds: $wallSeconds,
      peakRssBytes: $peakRssBytes,
      packageCount: $packageCount,
      toolVersion: $toolVersion,
      dockerStatsPeakMiB: $dockerStatsPeakMiB,
      cgroupMemoryPeakBytes: $cgroupMemoryPeakBytes,
      parseOk: $parseOk,
      agentWouldReport: $agentWouldReport,
      hostRamBytes: ($hostRamBytes|tonumber),
      hostRamNote: "VM is 4 GB",
      stderrTail: $stderr
    }' | tee -a "$OUT"

  rm -f "$stdout_file" "$stderr_file"
  log "done $image_ref @$mem_limit exit=$exit_code oom=$oom peakRss=$peak_rss pkgs=$packages report=$agent_report"
}

# --- alpine (already present) ---
run_one "alpine:3.20" "256m"
run_one "alpine:3.20" "128m"

# --- node:22-bookworm (~1.1 GB) ---
if ! image_present "node:22-bookworm"; then
  log "pulling node:22-bookworm"
  docker pull node:22-bookworm
  NODE_PULLED=1
else
  NODE_PULLED=0
fi
run_one "node:22-bookworm" "256m"
run_one "node:22-bookworm" "128m"
# Keep node for grype-trivy step if that runs next in the same session; run-all removes after.
# For standalone 01, remove if we pulled it — coordinator run-all keeps until 02 finishes.
if [[ "${KEEP_NODE_FOR_COMPARE:-0}" != "1" && "$NODE_PULLED" == "1" ]]; then
  safe_rmi "node:22-bookworm"
fi

# --- python:3.12 if disk allows (~1 GB compressed layers; need ~2+ GB free) ---
FREE_KB="$(df -Pk / | awk 'NR==2{print $4}')"
if [[ "$FREE_KB" -gt 3000000 ]]; then
  if ! image_present "python:3.12"; then
    log "pulling python:3.12 (disk free ${FREE_KB} KiB)"
    docker pull python:3.12
    PY_PULLED=1
  else
    PY_PULLED=0
  fi
  run_one "python:3.12" "256m"
  run_one "python:3.12" "128m"
  if [[ "$PY_PULLED" == "1" ]]; then
    safe_rmi "python:3.12"
  fi
else
  log "skipping python:3.12: only ${FREE_KB} KiB free (need >3 GiB margin)"
  jq -nc --arg reason "insufficient disk (${FREE_KB} KiB free)" \
    '{image:"python:3.12", skipped:true, reason:$reason}' | tee -a "$OUT"
fi

log "Syft peak results written to $OUT"
