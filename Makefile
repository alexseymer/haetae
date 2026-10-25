VERSION ?= $(shell git describe --tags --match 'v*' --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/alexseymer/haetae/internal/version.Version=$(VERSION) \
           -X github.com/alexseymer/haetae/internal/version.Commit=$(COMMIT)

GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

.PHONY: build test test-integration test-e2e test-load test-selfhost web dist docker-image lint vuln fmt generate clean

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

# Agent memory under load (spec S10): 20 noisy containers, RSS sampled every second.
# HAETAE_LOAD_DURATION overrides the default 2m.
# pipefail: a failing go test must fail the target even though grep is last.
test-load:
	bash -o pipefail -c "go test -count=1 -tags 'integration load' -timeout 30m -run TestAgentMemoryUnderLoad -v ./test/integration/ | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"

# Self-hosted install (spec S11 / M4): needs Docker. Builds the image once inside the test.
test-selfhost:
	bash -o pipefail -c "go test -count=1 -tags selfhost -timeout 30m -v ./test/selfhost/ | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'"
# Build the web UI into internal/webui/app for embedding.
web:
	cd web && npm ci && npm run build
	find internal/webui/app -mindepth 1 ! -name .gitkeep -delete
	cp -r web/dist/. internal/webui/app/
	touch internal/webui/app/.gitkeep

# Cross-compile agent binaries published at /download/.
dist:
	mkdir -p bin/dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist/haetae-agent-linux-amd64 ./cmd/haetae-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist/haetae-agent-linux-arm64 ./cmd/haetae-agent
	cd bin/dist && sha256sum haetae-agent-linux-amd64 haetae-agent-linux-arm64 > SHA256SUMS

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
