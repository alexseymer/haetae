#!/usr/bin/env bash
# Runs every acceptance suite in sequence (they share Docker and 2 cores, so never in parallel)
# and writes acceptance-results/: one log per step, summary.txt (PASS/FAIL per step) and
# measures.txt (every MEASURE line). docs/acceptance.md cites these numbers.
# Needs Docker, Chromium/Playwright, cached busybox/caddy/alpine/nginx images and no other load.
set -u
cd "$(dirname "$0")/.."
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
OUT=${ACCEPTANCE_OUT:-acceptance-results}
mkdir -p "$OUT"
: > "$OUT/summary.txt"
: > "$OUT/measures.txt"

step() { # name command...
  local name=$1; shift
  local start=$(date +%s)
  echo "== $name"
  "$@" > "$OUT/$name.log" 2>&1
  local rc=$?
  local secs=$(( $(date +%s) - start ))
  if [ $rc -eq 0 ]; then st=PASS; else st=FAIL; fi
  printf '%-22s %s  (%ss)\n' "$name" "$st" "$secs" | tee -a "$OUT/summary.txt"
  grep -h 'MEASURE' "$OUT/$name.log" | sed "s/^ */[$name] /" >> "$OUT/measures.txt"
}

step build            go build ./...
step vet-all-tags     go vet -tags 'integration deploy e2e e2e_security e2e_deploy e2e_alerts e2e_operator bench selfhost load release' ./...
step lint             make lint
step vuln             make vuln
step web-unit         bash -c 'cd web && npx tsc --noEmit && npx vitest run'
step unit-integration go test -race -count=1 -tags integration -v -timeout 40m ./...
step load-rss         make test-load
step selfhost         make test-selfhost
step web-e2e          make test-e2e
step deploy-e2e       make test-deploy
step alerts-e2e       make test-alerts
step security-e2e     make test-security
step operator-e2e     make test-operator
if grep -q '^release:' Makefile; then
  step release        make release
  step release-verify make test-release
  step release-rss    make bench-release-rss
fi

echo
echo "ACCEPT-DONE"
echo "$(grep -c PASS "$OUT/summary.txt") passed, $(grep -c FAIL "$OUT/summary.txt") failed"
