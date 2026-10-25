VERSION ?= $(shell git describe --tags --match 'v*' --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/alexseymer/haetae/internal/version.Version=$(VERSION) \
           -X github.com/alexseymer/haetae/internal/version.Commit=$(COMMIT)

GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

.PHONY: web build test test-integration test-e2e test-load lint vuln fmt generate clean

# Builds the web interface and embeds it into the next server build.
web:
	cd web && npm ci && npm run build
	find internal/webui/app -mindepth 1 ! -name .gitkeep -delete
	cp -r web/dist/. internal/webui/app/

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
test-load:
	go test -count=1 -tags 'integration load' -timeout 30m -run TestAgentMemoryUnderLoad -v ./test/integration/ | grep -E 'MEASURE|^(--- |ok|FAIL|PASS)'

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
