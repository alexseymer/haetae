#!/usr/bin/env bash
# Agent resident memory (scan + security active) + conntrack fake-source heap.
# Real nf_conntrack RSS is not measured on this VM.
set -euo pipefail
# shellcheck source=lib.sh
source "$(cd "$(dirname "$0")" && pwd)/lib.sh"

export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
mkdir -p "$RESULTS"

log "conntrack fake-source high-rate heap benchmark (20k events/s path)"
# BenchmarkHighRateHeap feeds FakeSource at ~20k events/s for 10s and reports heap growth.
set +e
go test -count=1 -bench=BenchmarkHighRateHeap -benchtime=1x -run=^$ -v ./internal/agent/conntrack/ 2>&1 | tee "$RESULTS/conntrack-heap.txt"
CT_RC=${PIPESTATUS[0]}
set -e
if [[ $CT_RC -ne 0 ]]; then
  log "WARNING: conntrack bench exit $CT_RC (see $RESULTS/conntrack-heap.txt)"
fi

# Probe whether real conntrack is usable for the agent.
CONNTRACK_NOTE="real conntrack RSS could not be measured here"
if [[ ! -e /proc/net/nf_conntrack ]]; then
  CONNTRACK_NOTE="real conntrack RSS could not be measured here: /proc/net/nf_conntrack missing (nf_conntrack not usable in this VM)"
elif [[ "$(id -u)" -ne 0 ]]; then
  CONNTRACK_NOTE="real conntrack RSS could not be measured here: agent runs unprivileged and netlink conntrack typically needs CAP_NET_ADMIN; /proc/net/nf_conntrack may exist but listener reports unavailable/degraded"
fi
printf '%s\n' "$CONNTRACK_NOTE" >"$RESULTS/conntrack-note.txt"
log "$CONNTRACK_NOTE"

log "agent RSS with scan scheduler + security checks (3 minutes, few containers)"
# Mirrors test/integration.TestAgentMemoryUnderLoad (M2) via test/bench.
HAETAE_BENCH_RSS_DURATION="${HAETAE_BENCH_RSS_DURATION:-3m}" \
HAETAE_BENCH_RSS_OUT="$RESULTS/agent-rss.json" \
  go test -tags bench -count=1 -timeout 30m -v -run TestAgentRSSWithScanSecurity ./test/bench/ 2>&1 | tee "$RESULTS/agent-rss.log"

log "agent RSS + conntrack notes written under $RESULTS"
