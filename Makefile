VERSION ?= $(shell git describe --tags --match 'v*' --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/alexseymer/haetae/internal/version.Version=$(VERSION) \
           -X github.com/alexseymer/haetae/internal/version.Commit=$(COMMIT)

GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

.PHONY: build test test-integration test-e2e test-deploy test-alerts test-security test-operator test-load test-selfhost bench-m7 web dist docker-image release release-image test-release bench-release-rss lint vuln fmt generate clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test -race -count=1 ./...

# Needs Docker, or HAETAE_TEST_DATABASE_URL pointing at a PostgreSQL admin connection.
test-integration:
	go test -race -count=1 -tags integration -timeout 15m ./...

# Real-browser acceptance against the embedded UI (needs Docker, Chromium, Playwright).
test-e2e:
	go test -count=1 -tags e2e -timeout 20m ./test/e2e/

# M5 deploy acceptance: real server + agent + Docker (needs Docker, cached busybox/caddy).
test-deploy:
	bash -o pipefail -c "go test -count=1 -tags e2e_deploy -timeout 20m -v ./test/deploy/ 2>&1 | tee deploy-e2e.log | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# M6 alerts/chat acceptance: real server + agent + Docker + fake Telegram (needs Docker, busybox).
test-alerts:
	bash -o pipefail -c "go test -count=1 -tags e2e_alerts -timeout 20m -v ./test/alerts/ 2>&1 | tee alerts-e2e.log | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# M7 security acceptance: real server + agent + inventory + crafted Grype DB (needs Docker).
test-security:
	bash -o pipefail -c "go test -count=1 -tags e2e_security -timeout 40m -v ./test/security/ 2>&1 | tee security-e2e.log | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# M8 AI operator acceptance: real server + agent + scripted OpenAI-compatible provider (needs Docker).
test-operator:
	bash -o pipefail -c "go test -count=1 -tags e2e_operator -timeout 30m -v ./test/operator/ 2>&1 | tee operator-e2e.log | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# Agent memory under load (spec S10): 20 noisy containers, RSS sampled every second.
# HAETAE_LOAD_DURATION overrides the default 2m.
# pipefail: a failing go test must fail the target even though grep is last.
test-load:
	bash -o pipefail -c "go test -count=1 -tags 'integration load' -timeout 30m -run TestAgentMemoryUnderLoad -v ./test/integration/ | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# Self-hosted install (spec S11 / M4): needs Docker. Builds the image once inside the test.
test-selfhost:
	bash -o pipefail -c "go test -count=1 -tags selfhost -timeout 30m -v ./test/selfhost/ 2>&1 | tee selfhost-full.log | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# M7 ADR risks 2–3 + agent RSS: sequential Docker/Go benches (see bench/m7/README.md).
# Needs Docker, ~4 GB+ free disk for transient pulls; writes bench/m7/results (gitignored).
bench-m7:
	bash bench/m7/run-all.sh

# Build the web UI into internal/webui/app for embedding.
web:
	cd web && npm ci && npm run build
	find internal/webui/app -mindepth 1 ! -name .gitkeep -delete
	cp -r web/dist/. internal/webui/app/
	touch internal/webui/app/.gitkeep

# Cross-compile agent + inventory helper binaries published at /download/.
dist:
	mkdir -p bin/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist/haetae-agent-linux-amd64 ./cmd/haetae-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist/haetae-agent-linux-arm64 ./cmd/haetae-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist/haetae-inventory-linux-amd64 ./cmd/haetae-inventory
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist/haetae-inventory-linux-arm64 ./cmd/haetae-inventory
	cd bin/dist && sha256sum haetae-agent-linux-amd64 haetae-agent-linux-arm64 haetae-inventory-linux-amd64 haetae-inventory-linux-arm64 > SHA256SUMS

# Release version: git tag (v*) if present, else 0.1.0-<shortsha>[-dirty]. Override with RELEASE_VERSION=.
RELEASE_VERSION ?= $(shell v=$$(git describe --tags --match 'v*' --dirty 2>/dev/null); if [ -n "$$v" ]; then echo "$${v\#v}"; else echo "0.1.0-$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)$$(git diff --quiet HEAD 2>/dev/null || echo -dirty)"; fi)

# Release artifacts in bin/release: agent, inventory, server per arch, tar.gz per arch, SHA256SUMS.
release:
	$(MAKE) web
	$(MAKE) dist VERSION=$(RELEASE_VERSION) COMMIT=$(COMMIT)
	bash scripts/release.sh $(RELEASE_VERSION) $(COMMIT)

# Server image from the release binaries; OCI tarball when buildx exists. Never pushes.
release-image:
	bash scripts/release-image.sh $(RELEASE_VERSION)

# Verifies bin/release and the image (needs Docker; run make release release-image first).
test-release:
	HAETAE_RELEASE_DIR=$(CURDIR)/bin/release HAETAE_RELEASE_VERSION=$$(cat bin/release/VERSION) HAETAE_DIST_DIR=$(CURDIR)/bin/dist \
	  go test -count=1 -tags release -timeout 10m -v ./test/release/

# Agent RSS on the release binary (spec S10), 3 minutes. Needs Docker. Writes bench/m7/results-release/.
bench-release-rss:
	mkdir -p bench/m7/results-release
	HAETAE_BENCH_AGENT_BIN=$(CURDIR)/bin/release/haetae-agent-linux-amd64 \
	HAETAE_BENCH_INV_BIN=$(CURDIR)/bin/release/haetae-inventory-linux-amd64 \
	HAETAE_BENCH_RSS_DURATION=$${HAETAE_BENCH_RSS_DURATION:-3m} \
	HAETAE_BENCH_RSS_OUT=$(CURDIR)/bench/m7/results-release/agent-rss.json \
	  go test -tags bench -count=1 -timeout 30m -v -run TestAgentRSSWithScanSecurity ./test/bench/ 2>&1 | tee bench/m7/results-release/agent-rss.log

docker-image:
	DOCKER_BUILDKIT=0 docker build --build-arg VERSION=$(VERSION) -t haetae-server:dev .

# Regenerates internal/gen from proto/ (needs buf, protoc-gen-go, protoc-gen-connect-go on PATH).
generate:
	buf lint && buf generate

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vuln:
	@# GOTOOLCHAIN=local: scan the toolchain we build with; fail instead of silently switching.
	GOTOOLCHAIN=local go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt ./...

clean:
	rm -rf bin/
