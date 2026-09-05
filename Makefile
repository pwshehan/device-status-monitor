# Local Device Monitor — build targets.
#
# The Go engine cross-compiles to Windows from any host. The Tauri GUI and the
# Inno Setup installer do not: those are built on a windows-latest CI runner.

MODULE  := github.com/pwshehan/device-status-monitor
# The single source of truth for the release version. The release workflow
# refuses to build unless the tag agrees with this file.
VERSION ?= $(shell tr -d '[:space:]' < VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST    := dist

.PHONY: help build build-windows run seed test test-race vet fmt lint clean tidy \
        ui-install ui-dev ui-build ui-test ui-lint \
        app-dev app-build app-lint installer release-local check

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

ui-install: ## Install the UI's dependencies
	cd ui && npm ci

ui-dev: ## Run the UI dev server against the running service on :49215
	cd ui && npm run dev

ui-build: ## Type-check and build the UI into ui/dist
	cd ui && npm run build

ui-test: ## Run the UI unit tests
	cd ui && npm test

ui-lint: ## Type-check and lint the UI
	cd ui && npm run typecheck && npm run lint

app-dev: ## Run the desktop shell against the dev server (Windows, needs Rust)
	cd ui && npm run tauri:dev

app-build: ## Build the desktop app and its installer (Windows only)
	cd ui && npm run tauri:build

app-lint: ## Format check and clippy on the shell
	cd ui/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings

installer: ## Build the Windows installer from whatever is already in dist/
	iscc /DAppVersion=$(VERSION) installer/setup.iss

release-local: build-windows app-build ## Build both exes and the installer (Windows)
	cp ui/src-tauri/target/release/local-monitor-gui.exe $(DIST)/monitor-gui.exe
	iscc /DAppVersion=$(VERSION) installer/setup.iss

check: lint test ui-lint ui-test ## Everything CI runs, minus the cross-builds

clean: ## Remove build output and the dev database
	rm -rf $(DIST) .dev-data ui/dist
