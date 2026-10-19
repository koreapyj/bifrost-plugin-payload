# Build and test the payload plugin.
#
# Quick reference:
#   make test                                   go vet + go test (needs `make assets` first if ui/ embeds them)
#   make assets                                 engine.wasm.gz, wasm_exec.js and the web bundle (ui/build.sh)
#   make test-web                               Node test of the wasm engine (ui/web: npm test)
#   make so BIFROST_DIR=../bifrost              payload.so built in a go.work with <bifrost>/transports
#   make bifrost-http BIFROST_DIR=../bifrost    matching dynamically linked bifrost-http
#   make e2e                                    run e2e/run.sh against $(OUT_DIR)/bifrost-http + payload.so
#
# BIFROST_DIR is an extracted Bifrost source tree at a transports/vX.Y.Z tag
# (for example https://codeload.github.com/maximhq/bifrost/tar.gz/refs/tags/transports/v2.3.1).
# payload.so and bifrost-http must come from the same tree, the same go.work and
# the same Go toolchain, otherwise Bifrost refuses to load the plugin.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
export GOFLAGS := -buildvcs=false

ROOT := $(CURDIR)
BUILD_DIR ?= $(ROOT)/.tmp/build
OUT_DIR ?= $(BUILD_DIR)/out
GOWORK_FILE ?= $(BUILD_DIR)/go.work

BIFROST_DIR ?=
BIFROST_ABS = $(abspath $(BIFROST_DIR))
BIFROST_VERSION ?= $(shell cat "$(BIFROST_ABS)/transports/version" 2>/dev/null)
CORE_MODULE := github.com/maximhq/bifrost/core

WEB_DIR ?= ui/web
WEB_DIST ?= $(WEB_DIR)/dist

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-22s %s\n", $$1, $$2}'

# ---------------------------------------------------------------- unit tests

.PHONY: test
test: ## go vet + go test for the plugin module
	$(GO) vet ./...
	$(GO) test ./...

.PHONY: test-web
test-web: wasm ## Node test of the wasm engine (ui/web: npm test)
	cd $(WEB_DIR) && $(NPM) test

# ---------------------------------------------------------------- UI assets

.PHONY: wasm
wasm: ## Build ui/web/dist/engine.wasm.gz and copy wasm_exec.js from GOROOT (ui/build.sh wasm)
	sh ui/build.sh wasm

.PHONY: web
web: ## npm ci + esbuild bundle into ui/web/dist (ui/build.sh web)
	sh ui/build.sh web

.PHONY: assets
assets: ## Rebuild every embedded UI asset from scratch (ui/build.sh all)
	sh ui/build.sh all

# ---------------------------------------------------------------- Bifrost builds

.PHONY: check-bifrost
check-bifrost:
	@[ -n "$(BIFROST_DIR)" ] || { echo "BIFROST_DIR is required (path to a Bifrost source tree)"; exit 1; }
	@[ -f "$(BIFROST_ABS)/transports/go.mod" ] || { echo "$(BIFROST_ABS)/transports/go.mod not found"; exit 1; }

.PHONY: sync-core
sync-core: check-bifrost ## Pin bifrost/core in go.mod to the version transports requires (edits go.mod; CI only)
	version="$$(cd "$(BIFROST_ABS)/transports" && GOWORK=off $(GO) list -m -f '{{.Version}}' $(CORE_MODULE))"; \
	echo "core version: $$version"; \
	$(GO) mod edit -require=$(CORE_MODULE)@$$version

.PHONY: work
work: check-bifrost ## (Re)create $(GOWORK_FILE) with <bifrost>/transports and this module (keeps go.work.sum)
	mkdir -p $(BUILD_DIR)
	rm -f $(GOWORK_FILE)
	GOWORK=$(GOWORK_FILE) $(GO) work init "$(BIFROST_ABS)/transports" "$(ROOT)"
	cat $(GOWORK_FILE)

.PHONY: bifrost-ui
bifrost-ui: check-bifrost ## Build the Bifrost dashboard into <bifrost>/transports/bifrost-http/ui (needs Node)
	rm -rf "$(BIFROST_ABS)/ui/app/enterprise"
	cd "$(BIFROST_ABS)/ui" && $(NPM) ci --no-audit --no-fund && $(NPM) run build

.PHONY: bifrost-ui-placeholder
bifrost-ui-placeholder: check-bifrost ## Local only: placeholder dashboard so bifrost-http compiles without Node
	mkdir -p "$(BIFROST_ABS)/transports/bifrost-http/ui"
	[ -f "$(BIFROST_ABS)/transports/bifrost-http/ui/index.html" ] || \
		echo '<!doctype html><title>Bifrost</title><p>Dashboard not built.</p>' > "$(BIFROST_ABS)/transports/bifrost-http/ui/index.html"

.PHONY: bifrost-http
bifrost-http: work ## Build a dynamically linked bifrost-http into $(OUT_DIR)
	@[ -d "$(BIFROST_ABS)/transports/bifrost-http/ui" ] || { echo "dashboard not built: run 'make bifrost-ui' (or 'make bifrost-ui-placeholder' for local tests)"; exit 1; }
	mkdir -p $(OUT_DIR)
	cd "$(BIFROST_ABS)/transports/bifrost-http" && \
		CGO_ENABLED=1 GOWORK=$(GOWORK_FILE) $(GO) build -trimpath \
		-ldflags "-X main.Version=v$(BIFROST_VERSION)" -o "$(OUT_DIR)/bifrost-http" .

.PHONY: so
so: work ## Build payload.so into $(OUT_DIR) (run `make assets` first)
	mkdir -p $(OUT_DIR)
	CGO_ENABLED=1 GOWORK=$(GOWORK_FILE) $(GO) build -trimpath -buildmode=plugin -o "$(OUT_DIR)/payload.so" .

# ---------------------------------------------------------------- end-to-end

.PHONY: mock-upstream
mock-upstream: ## Build the e2e mock upstream into $(OUT_DIR)
	mkdir -p $(OUT_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o "$(OUT_DIR)/mock-upstream" ./e2e/mock-upstream

BIFROST_HTTP ?= $(OUT_DIR)/bifrost-http
PAYLOAD_SO ?= $(OUT_DIR)/payload.so

.PHONY: e2e
e2e: mock-upstream ## Run e2e/run.sh (BIFROST_HTTP, PAYLOAD_SO default to $(OUT_DIR))
	BIFROST_HTTP="$(BIFROST_HTTP)" PAYLOAD_SO="$(PAYLOAD_SO)" MOCK_UPSTREAM="$(OUT_DIR)/mock-upstream" ./e2e/run.sh

# ---------------------------------------------------------------- housekeeping

.PHONY: clean
clean: ## Remove build output and generated UI assets
	rm -rf $(BUILD_DIR)
	[ ! -d $(WEB_DIST) ] || find $(WEB_DIST) -mindepth 1 ! -name .keep -delete
