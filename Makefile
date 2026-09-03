# Local Device Monitor — build targets.
#
# The Go engine cross-compiles to Windows from any host. The Tauri GUI and the
# Inno Setup installer do not: those are built on a windows-latest CI runner.

MODULE  := github.com/gkgraphite/device-status-monitor
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST    := dist

.PHONY: help build build-windows run seed test test-race vet fmt lint clean tidy

help:
	@grep -E '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the engine for this host
	go build -ldflags "$(LDFLAGS)" -o $(DIST)/monitor-service ./cmd/monitor-service

build-windows: ## Cross-compile the engine for Windows x64
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
		go build -ldflags "$(LDFLAGS)" -o $(DIST)/monitor-service.exe ./cmd/monitor-service

run: ## Run the engine in the foreground against ./.dev-data
	go run ./cmd/monitor-service -dev -log-level debug

seed: ## Seed example groups and devices, then run
	go run ./cmd/monitor-service -dev -seed -log-level debug

test: ## Run the test suite
	go test ./...

test-race: ## Run the test suite with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go source
	gofmt -w ./cmd ./internal

lint: vet ## Vet plus a gofmt check
	@out=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy: ## Tidy go.mod
	go mod tidy

clean: ## Remove build output and the dev database
	rm -rf $(DIST) .dev-data
