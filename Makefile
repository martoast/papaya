# tailscale.com requires a newer toolchain than some machines have installed as
# their default `go`. GOTOOLCHAIN=auto lets the go command fetch the version
# pinned in go.mod, so the build works regardless of the local default.
export GOTOOLCHAIN := auto

GO      ?= go
BIN     := bin/papaya
PKGS    := ./...

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_.-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- build ---

.PHONY: web
web: ## Build the frontend into web/dist
	npm --prefix web ci
	npm --prefix web run build

.PHONY: build
build: web ## Build the binary with the frontend embedded
	$(GO) build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/papaya

.PHONY: build-go
build-go: ## Build the binary only (uses whatever is already in web/dist)
	$(GO) build -o $(BIN) ./cmd/papaya

.PHONY: run
run: build ## Build and run locally (requires TS_AUTHKEY and PAPAYA_ALLOWED_USERS)
	./$(BIN)

.PHONY: run-env
run-env: build-go ## Run using the gitignored .env file (keeps secrets out of your shell history)
	@test -f .env || { echo "no .env — run: cp .env.example .env && chmod 600 .env"; exit 1; }
	@set -a; . ./.env; set +a; ./$(BIN)

# --- test ---

.PHONY: test
test: ## Unit and integration tests with the race detector
	$(GO) test -race -count=1 -timeout 600s $(PKGS)

.PHONY: test-short
test-short: ## Fast tests only
	$(GO) test -race -short -count=1 $(PKGS)

.PHONY: test-flake
test-flake: ## Run the suite three times; a flake is a failure
	@for i in 1 2 3; do \
		echo "=== run $$i/3 ==="; \
		$(GO) test -race -count=1 -timeout 600s $(PKGS) || exit 1; \
	done

.PHONY: test-integration
test-integration: ## Tests against a real OpenSSH server in Docker
	PAPAYA_INTEGRATION=1 $(GO) test -race -count=1 -tags=integration \
		-timeout 900s ./test/integration/...

.PHONY: test-browser
test-browser: ## Playwright tests
	npm --prefix web run test

# Everything except the tsnet listener, against a real host on your tailnet.
# The machine running this must already be joined to the tailnet.
LIVE_HOST ?=
LIVE_USER ?=

.PHONY: test-live
test-live: ## Real Tailscale SSH host (LIVE_HOST=... LIVE_USER=...)
	PAPAYA_LIVE_HOST=$(LIVE_HOST) PAPAYA_LIVE_USER=$(LIVE_USER) \
		$(GO) test -tags=live -count=1 -timeout 900s -v ./test/live/...

.PHONY: test-voice
test-voice: ## Live transcription (needs PAPAYA_OPENAI_API_KEY and LIVE_HOST)
	PAPAYA_LIVE_HOST=$(LIVE_HOST) PAPAYA_LIVE_USER=$(LIVE_USER) \
		$(GO) test -tags=live -count=1 -run TestLiveVoice -v ./test/live/...

.PHONY: test-tailnet
test-tailnet: ## Real tsnet node; needs TS_AUTHKEY
	$(GO) test -tags=tailnet -count=1 -timeout 300s -v ./test/tailnet/...

.PHONY: fuzz
fuzz: ## Run each fuzz target for 60s
	$(GO) test -run=FuzzSafeName -fuzz=FuzzSafeName -fuzztime=60s ./internal/sshx
	$(GO) test -run=FuzzSanitizeName -fuzz=FuzzSanitizeName -fuzztime=60s ./internal/hub
	$(GO) test -run=FuzzRing -fuzz=FuzzRing -fuzztime=60s ./internal/hub

.PHONY: arch
arch: ## Structural guards (single WS writer, no net.Listen, streaming uploads)
	$(GO) test -count=1 ./test/arch/...

.PHONY: security
security: ## The security test suite
	$(GO) test -race -count=1 -run 'TestS[0-9]+|TestArch|TestCrossOwner|TestHealthz' \
		-timeout 300s ./test/... ./internal/...

# --- quality ---

.PHONY: lint
lint: vet staticcheck vuln ## All static analysis

.PHONY: vet
vet: ## go vet
	$(GO) vet $(PKGS)

# Analysis tools must be *built* with at least the toolchain this module
# targets, or they cannot parse its sources. GOTOOLCHAIN=auto is not enough:
# these tools declare an older go directive, so auto would happily build them
# with an older toolchain than our code needs.
GOBIN_DIR   := $(shell $(GO) env GOPATH)/bin
GO_VERSION  := $(shell awk '/^go /{print $$2; exit}' go.mod)
TOOL_GO     := GOTOOLCHAIN=go$(GO_VERSION)

.PHONY: staticcheck
staticcheck: ## staticcheck
	@test -x $(GOBIN_DIR)/staticcheck || \
		$(TOOL_GO) $(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	$(GOBIN_DIR)/staticcheck $(PKGS)

.PHONY: vuln
vuln: ## govulncheck
	@test -x $(GOBIN_DIR)/govulncheck || \
		$(TOOL_GO) $(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GOBIN_DIR)/govulncheck $(PKGS)

.PHONY: tools
tools: ## (Re)install the analysis tools against the module's toolchain
	$(TOOL_GO) $(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	$(TOOL_GO) $(GO) install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: fmt
fmt: ## Format
	$(GO) fmt $(PKGS)

.PHONY: tidy
tidy: ## Tidy modules
	$(GO) mod tidy

# --- docker ---

.PHONY: domain
domain: ## Point your own domain at papaya and issue its cert (DOMAIN=term.example.com)
	@test -n "$(DOMAIN)" || { echo "usage: make domain DOMAIN=term.example.com"; exit 1; }
	deploy/setup-domain.sh $(DOMAIN)

.PHONY: service-install
service-install: build-go ## Run papaya in the background via launchd (macOS)
	cp deploy/io.papaya.plist $(HOME)/Library/LaunchAgents/
	launchctl unload $(HOME)/Library/LaunchAgents/io.papaya.plist 2>/dev/null || true
	launchctl load $(HOME)/Library/LaunchAgents/io.papaya.plist
	@echo "loaded; logs at /tmp/papaya.log"

.PHONY: service-uninstall
service-uninstall: ## Stop the background service
	launchctl unload $(HOME)/Library/LaunchAgents/io.papaya.plist 2>/dev/null || true
	rm -f $(HOME)/Library/LaunchAgents/io.papaya.plist

.PHONY: docker
docker: ## Build the container image
	docker build -t papaya:local .

.PHONY: up
up: ## Start via compose
	docker compose up --build

.PHONY: down
down: ## Stop compose
	docker compose down -v

.PHONY: clean
clean: ## Remove build output
	rm -rf bin web/dist
	mkdir -p web/dist && touch web/dist/.gitkeep
