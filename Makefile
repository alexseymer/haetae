VERSION ?= $(shell git describe --tags --match 'v*' --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/alexseymer/haetae/internal/version.Version=$(VERSION) \
           -X github.com/alexseymer/haetae/internal/version.Commit=$(COMMIT)

GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0

.PHONY: build test lint vuln fmt clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test -race -count=1 ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vuln:
	@# GOTOOLCHAIN=local: scan the toolchain we build with; fail instead of silently switching.
	GOTOOLCHAIN=local go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt ./...

clean:
	rm -rf bin/
