.PHONY: build clean install test test-plugins test-quiet test-e2e golden dev build-all help all

# Version generation: uses git commit count + short hash if available, otherwise timestamp
VERSION ?= $(shell git rev-list --count HEAD 2>/dev/null || echo "0").$(shell git rev-parse --short HEAD 2>/dev/null || date +%s)
LDFLAGS = -X main.Version=$(VERSION)

# Build the Go binary for current platform
build:
	go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-$(shell uname -s | tr '[:upper:]' '[:lower:]')-$(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/armv7l/arm/;s/armv8l/arm/') ./cmd/dyno-proxy

# Build binaries for all supported platforms
build-all: clean
	# Darwin AMD64
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-darwin-amd64 ./cmd/dyno-proxy
	# Darwin ARM64
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-darwin-arm64 ./cmd/dyno-proxy
	# Linux AMD64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-linux-amd64 ./cmd/dyno-proxy
	# Linux ARM64
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-linux-arm64 ./cmd/dyno-proxy
	# Linux ARM (32-bit)
	GOOS=linux GOARCH=arm go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-linux-arm ./cmd/dyno-proxy
	# Make all binaries executable
	chmod +x bin/dyno-proxy-*

# Clean build artifacts
clean:
	rm -f bin/dyno-proxy-*

# Install dependencies
install:
	go mod download
	go mod tidy

# Run the entire test suite (unit + render golden + e2e conformance + plugins).
#
# The e2e conformance suite (internal/conformance) boots the real load-balancer binary
# for its live-traffic assertions. Where the traefik binary is
# not installed on the host, those scenarios t.Skip themselves with a clear
# message while the render/golden layers still run, so `go test ./...` always
# passes locally and in CI.
test: test-plugins
	go test ./...

# Each internal plugin is its own Go module (Yaegi needs a self-contained
# module), so `go test ./...` at the repo root skips them; test each in turn.
test-plugins:
	@set -e; for p in internal/plugins/*/; do echo "==> $$p"; (cd "$$p" && go test ./...); done

# Run the test suite quietly (only surface failures and errors).
test-quiet:
	go test -v ./... | grep -iE "(fail|error|panic|fatal)"

# Run only the e2e conformance + golden suite.
test-e2e:
	go test ./internal/conformance/...

# Regenerate the render golden files after an intentional
# rendering change. Review the resulting diff before committing.
golden:
	go test ./internal/conformance/ -run TestRenderGolden -update

# Development: build and run the full test suite.
dev: build test

# Show all available targets
help:
	@echo "Available targets:"
	@echo "  build       - Build binary for current platform"
	@echo "  build-all   - Build binaries for all supported platforms"
	@echo "  clean       - Remove build artifacts"
	@echo "  install     - Install dependencies"
	@echo "  test        - Run the full Go test suite (unit + golden + e2e + plugins)"
	@echo "  test-plugins - Run the internal plugin tests (separate Go modules)"
	@echo "  test-quiet  - Run the test suite quietly (only failures/errors)"
	@echo "  test-e2e    - Run only the e2e conformance + golden suite"
	@echo "  golden      - Regenerate render golden files"
	@echo "  dev         - Build and run the full test suite"
	@echo "  buildpack   - Bundle binaries + plugins and publish the buildpack image"
	@echo "  help        - Show this help"

# Default target
all: install build


TARGET ?= linux/amd64
IMAGE ?= ghcr.io/heroku-examples/dyno-proxy-buildpack:dev

.PHONY: buildpack buildpack-package buildpack-stage buildpack-tarball

# Build and publish the packaged buildpack.
#
# `buildpack` builds the linux binaries itself; `buildpack-package` packages
# binaries that already exist in bin/ (e.g. downloaded from the CI build job).
#
# Staging puts everything bin/build and bin/compile consume into buildpack/:
#
#   1. dyno-proxy binaries: bin/dyno-proxy-linux-* -> buildpack/bin/ (bin/build
#      ships the launcher shim + the current-arch dyno-proxy-linux-<arch>
#      binary, picked via $CNB_TARGET_ARCH).
#   2. Yaegi/Traefik plugin sources: internal/plugins/* -> buildpack/plugins/ (wiped
#      first). Each plugin's go.mod declares its full module path, which
#      bin/build re-homes under bin/plugins/<module> in the layer.
#
buildpack:
	# Build the linux binaries for every arch buildpack.toml declares as a target.
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-linux-amd64 ./cmd/dyno-proxy
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/dyno-proxy-linux-arm64 ./cmd/dyno-proxy
	$(MAKE) buildpack-package

buildpack-stage:
	cp bin/dyno-proxy-linux-amd64 bin/dyno-proxy-linux-arm64 buildpack/bin/
	chmod +x buildpack/bin/dyno-proxy-linux-amd64 buildpack/bin/dyno-proxy-linux-arm64
	# Re-stage the plugin sources (wipe first so removed plugins don't linger).
	rm -rf buildpack/plugins
	mkdir -p buildpack/plugins
	cp -R internal/plugins/* buildpack/plugins/

buildpack-package: buildpack-stage
	pack buildpack package $(IMAGE) --path buildpack --target $(TARGET) --publish

# Classic (non-CNB) buildpack archive, usable with `heroku buildpacks:add <url>`.
buildpack-tarball: buildpack-stage
	tar -czf dyno-proxy-buildpack.tgz -C buildpack .
