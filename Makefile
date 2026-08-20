.PHONY: docker-build build build-backend build-dash build-dash0 build-status0 build-docs copy-dash copy-dash0 copy-status0 copy-docs \
	build-cli install-cli clean clean-all run run-test dev dev-test dev-saas dev-dash dev-dash0 dev-status0 dev-docs dev-backend \
	test test-scenario test-dash test-dash0 lint lint-back lint-dash fmt deps migrate help sync-brand-assets build-favicons \
	showcase \
	build-loadgen bench-checks bench-checks-sqlite bench-checks-postgres \
	build-scenario scenario-test
.DEFAULT_GOAL := build

APP_NAME := solidping

# SaaS mode (make dev-saas): the separate solidping-billing service (next door
# at ../solidping-billing, served on :4050) drives plan upgrades. These three
# values wire the two services together for local development:
#   SAAS_BILLING_TOKEN — LEGACY shared secret; the billing service presents it
#     as a bearer token when PUTting resolved entitlements. Must match the
#     billing service's BILLING_SOLIDPING_TOKEN. Superseded by the signing key
#     sets below, but still accepted (see SAAS_ALLOW_LEGACY_TOKEN) until the
#     billing service has stopped sending it.
#   SAAS_UPGRADE_URL   — the dashboard's "Upgrade" link. {org} is replaced with
#     the org slug. Points at the billing service's customer upgrade page.
#   SAAS_SIGNING_KEYS_IN / _OUT — ordered [{"id","secret"}] key sets (newest
#     first) for the signed service channels: _IN verifies the billing service's
#     entitlements push (its BILLING_SIGNING_KEYS_OUTBOUND), _OUT signs our own
#     calls to billing's /api/v1/* (its BILLING_SIGNING_KEYS_INBOUND). One set
#     per direction so a leak of one cannot forge the other.
#   SAAS_ALLOW_LEGACY_TOKEN — whether the legacy bearer above is still accepted.
#     Stays true until billing signs everything; flipping it is a parameter
#     change, not a deploy.
SAAS_BILLING_TOKEN ?= dev-billing-service-token
SAAS_UPGRADE_URL   ?= http://localhost:4050/app/upgrade?org={org}
SAAS_SIGNING_KEYS_IN  ?= [{"id":"dev-in","secret":"dev-billing-to-solidping-key"}]
SAAS_SIGNING_KEYS_OUT ?= [{"id":"dev-out","secret":"dev-solidping-to-billing-key"}]
SAAS_ALLOW_LEGACY_TOKEN ?= true

# Version information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_TIME ?= $(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "unknown")

# Build flags
LDFLAGS := -ldflags "\
	-X 'github.com/fclairamb/solidping/server/internal/version.Version=$(VERSION)' \
	-X 'github.com/fclairamb/solidping/server/internal/version.Commit=$(COMMIT)' \
	-X 'github.com/fclairamb/solidping/server/internal/version.GitTime=$(GIT_TIME)'"

# Directories
DASH_DIR := web/dash
DASH_DIST := $(DASH_DIR)/dist
DASH0_DIR := web/dash0
DASH0_DIST := $(DASH0_DIR)/dist
STATUS0_DIR := web/status0
STATUS0_DIST := $(STATUS0_DIR)/dist
DOCS_DIR := web/docs
DOCS_DIST := $(DOCS_DIR)/build
BACK_DIR := server
BACK_RES := $(BACK_DIR)/internal/app/res/
BACK_DASH0_RES := $(BACK_DIR)/internal/app/dash0res/
BACK_STATUS0_RES := $(BACK_DIR)/internal/app/status0res/
BACK_DOCS_RES := $(BACK_DIR)/internal/app/docsres/
LOG_DIR := logs

# Detect current OS
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

kill:
	lsof -ti :4000 | xargs kill
	lsof -ti :5174 | xargs kill
	lsof -ti :5175 | xargs kill

build: sync-brand-assets build-dash copy-dash build-dash0 copy-dash0 build-status0 copy-status0 build-docs copy-docs build-backend ## Build complete application

sync-brand-assets: ## Copy res/logo.svg + favicon set into web/{dash0,status0}/public/ (favicons under public/assets/)
	@mkdir -p web/dash0/public/assets web/status0/public/assets
	@cp res/logo.svg web/dash0/public/logo.svg
	@cp res/logo.svg web/status0/public/logo.svg
	@cp res/logo.svg web/dash0/public/assets/favicon.svg
	@cp res/logo.svg web/status0/public/assets/favicon.svg
	@cp res/logo.png web/dash0/public/logo.png
	@cp res/logo.png web/status0/public/logo.png
	@if [ -d res/favicons ]; then \
		cp res/favicons/*.png web/dash0/public/assets/; \
		cp res/favicons/*.png web/status0/public/assets/; \
	fi
	@mkdir -p web/docs/static/img
	@cp res/logo.png web/docs/static/img/logo.png
	@echo "Brand assets synced to web/dash0/public/ (favicons in assets/), web/status0/public/ (favicons in assets/), and web/docs/static/"

build-favicons: ## Generate favicon PNG set from res/logo.svg into res/favicons/
	@./scripts/build-favicons.sh

docker-build: ## Build Docker image
	@echo "Building Docker image..."
	@docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg GIT_TIME=$(GIT_TIME) \
		-t $(APP_NAME):$(VERSION) \
		-t $(APP_NAME):latest .
	@echo "Docker image built: $(APP_NAME):$(VERSION) and $(APP_NAME):latest"

build-dash: ## Build dash with bun
	@echo "Building dash..."
	@cd $(DASH_DIR) && bun install && bun run build
	@echo "Dash build complete"

copy-dash: ## Copy dash dist to backend res directory
	echo "Copying dash dist to backend resources..."
	rm -rf $(BACK_RES)
	mkdir -p $(BACK_RES)
	cp -r $(DASH_DIST)/* $(BACK_RES)/
	echo "Dash resources copied to $(BACK_RES)"

build-dash0: ## Build dash0 status page with bun
	@echo "Building dash0..."
	@cd $(DASH0_DIR) && bun install && bun run build
	@echo "Dash0 build complete"

copy-dash0: ## Copy dash0 dist to backend dash0res directory
	@echo "Copying dash0 dist to backend resources..."
	@rm -rf $(BACK_DASH0_RES)
	@mkdir -p $(BACK_DASH0_RES)
	@cp -r $(DASH0_DIST)/* $(BACK_DASH0_RES)/
	@echo "Dash0 resources copied to $(BACK_DASH0_RES)"

build-status0: ## Build status0 public status page with bun
	@echo "Building status0..."
	@cd $(STATUS0_DIR) && bun install && bun run build
	@echo "Status0 build complete"

copy-status0: ## Copy status0 dist to backend status0res directory
	@echo "Copying status0 dist to backend resources..."
	@rm -rf $(BACK_STATUS0_RES)
	@mkdir -p $(BACK_STATUS0_RES)
	@cp -r $(STATUS0_DIST)/* $(BACK_STATUS0_RES)/
	@echo "Status0 resources copied to $(BACK_STATUS0_RES)"

build-docs: ## Build docs site (Docusaurus, incl. generated API ref) with bun
	@echo "Building docs..."
	@cd $(DOCS_DIR) && bun install && bun run build
	@echo "Docs build complete"

copy-docs: ## Copy docs build to backend docsres directory
	@echo "Copying docs build to backend resources..."
	@rm -rf $(BACK_DOCS_RES)
	@mkdir -p $(BACK_DOCS_RES)
	@cp -r $(DOCS_DIST)/* $(BACK_DOCS_RES)/
	@echo "Docs resources copied to $(BACK_DOCS_RES)"

build-backend: ## Build backend Go binary
	@echo "Building backend for $(GOOS)/$(GOARCH)..."
	@cd $(BACK_DIR) && go build $(LDFLAGS) -o ../$(APP_NAME) .
	@echo "Binary created: ./$(APP_NAME)"

build-cli: ## Build standalone CLI (sp) binary (also available as 'solidping client')
	@echo "Building CLI for $(GOOS)/$(GOARCH)..."
	@cd $(BACK_DIR) && go build $(LDFLAGS) -o ../bin/sp ./cmd/sp
	@echo "Binary created: ./bin/sp"
	@echo "Note: CLI commands are also available via './solidping client <command>'"

install-cli: ## Install standalone CLI to GOPATH
	@echo "Installing CLI..."
	@cd $(BACK_DIR) && go install $(LDFLAGS) ./cmd/sp
	@echo "CLI installed to GOPATH"
	@echo "Note: CLI commands are also available via 'solidping client <command>'"

build-loadgen: ## Build the loadgen benchmark client
	@echo "Building loadgen..."
	@cd $(BACK_DIR) && go build -o ../bin/loadgen ./cmd/loadgen
	@echo "Binary created: ./bin/loadgen"

build-scenario: ## Build the scenario driver CLI
	@echo "Building scenario driver..."
	@mkdir -p bin
	@cd $(BACK_DIR) && go build -o ../bin/solidping-scenario ./cmd/scenariodriver
	@echo "Binary created: ./bin/solidping-scenario"

scenario-test: build-scenario ## Run scenario smoke test against a local dev server (make dev-test first)
	@echo "Running scenario test against local dev server..."
	./bin/solidping-scenario run \
		--server http://localhost:4000 \
		--org test \
		--token pat_test \
		--listen :9876 \
		--public-url http://localhost:9876 \
		--scenario server/cmd/scenariodriver/scenarios/incident-open-close.yaml \
		--junit /tmp/scenario-results.xml

# Bench knobs (override per invocation, e.g. `make bench-checks BENCH_CHECKS=500`).
BENCH_CHECKS   ?= 200
BENCH_DURATION ?= 2m
BENCH_PERIOD   ?= 10s
BENCH_PORT     ?= 4001
BENCH_PG_PORT  ?= 5435
BENCH_DATA     := bench-data
BENCH_OUT      := bench-results

bench-checks: bench-checks-sqlite bench-checks-postgres ## Run loadgen against SQLite and PostgreSQL backends (both)
	@echo "Bench complete; reports under $(BENCH_OUT)/"

bench-checks-sqlite: build build-loadgen ## Run loadgen against a SQLite-backed test server
	@echo "==> Bench: SQLite"
	@mkdir -p $(BENCH_DATA)/sqlite $(BENCH_OUT)
	@lsof -ti :$(BENCH_PORT) | xargs kill 2>/dev/null || true
	@SP_RUNMODE=test \
		SP_DB_TYPE=sqlite \
		SP_DB_DATA_DIR=$(BENCH_DATA)/sqlite \
		SP_DB_RESET=true \
		SP_SERVER_LISTEN=127.0.0.1:$(BENCH_PORT) \
		LOG_LEVEL=warn \
		./$(APP_NAME) serve > $(BENCH_OUT)/server-sqlite.log 2>&1 & echo $$! > $(BENCH_OUT)/server.pid; \
	trap "kill `cat $(BENCH_OUT)/server.pid` 2>/dev/null || true; rm -f $(BENCH_OUT)/server.pid" EXIT; \
	./bin/loadgen \
		-api-url http://127.0.0.1:$(BENCH_PORT) \
		-backend sqlite \
		-checks $(BENCH_CHECKS) \
		-duration $(BENCH_DURATION) \
		-period $(BENCH_PERIOD) \
		-output-dir $(BENCH_OUT)

bench-checks-postgres: build build-loadgen ## Run loadgen against a PostgreSQL-backed test server (embedded PG)
	@echo "==> Bench: PostgreSQL (embedded)"
	@mkdir -p $(BENCH_DATA)/pg $(BENCH_OUT)
	@lsof -ti :$(BENCH_PORT) | xargs kill 2>/dev/null || true
	@SP_RUNMODE=test \
		SP_DB_TYPE=postgres-embedded \
		SP_DB_EMBEDDED_DIR=$(BENCH_DATA)/pg \
		SP_DB_PORT=$(BENCH_PG_PORT) \
		SP_DB_RESET=true \
		SP_SERVER_LISTEN=127.0.0.1:$(BENCH_PORT) \
		LOG_LEVEL=warn \
		./$(APP_NAME) serve > $(BENCH_OUT)/server-postgres.log 2>&1 & echo $$! > $(BENCH_OUT)/server.pid; \
	trap "kill `cat $(BENCH_OUT)/server.pid` 2>/dev/null || true; rm -f $(BENCH_OUT)/server.pid" EXIT; \
	./bin/loadgen \
		-api-url http://127.0.0.1:$(BENCH_PORT) \
		-backend postgres \
		-checks $(BENCH_CHECKS) \
		-duration $(BENCH_DURATION) \
		-period $(BENCH_PERIOD) \
		-output-dir $(BENCH_OUT)

run: build ## Build and run the application
	@echo "Running application..."
	@./$(APP_NAME) serve

run-test: build ## Build and run the application in test mode
	@echo "Running application in test mode..."
	@SP_RUNMODE=test ./$(APP_NAME) serve

# devloop supervises all dev processes (backend rebuild loop + the two bun dev
# servers) as one foreground tree, and size-rotates each stream into
# $(LOG_DIR)/<name>.log (.1/.2 backups). No tee, no backgrounded pipelines.
DEVLOOP_LOG_FLAGS := -log-dir $(CURDIR)/$(LOG_DIR)
DEVLOOP_PROCS := -proc "dash0:$(CURDIR)/$(DASH0_DIR):bun run dev" -proc "status0:$(CURDIR)/$(STATUS0_DIR):bun run dev"

dev: kill ## Run backend, dash0 and status0 in development mode
	@echo "Running application in development mode..."
	@cd $(BACK_DIR) && SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0" SP_PROFILER_ENABLED=true \
		SP_DB_MIGRATION_GUARD_MODE=warn \
		go run ./cmd/devloop $(DEVLOOP_LOG_FLAGS) $(DEVLOOP_PROCS)

dev-test: kill ## Run backend, dash0 and status0 in development test mode
	@echo "Running application in development test mode..."
	@cd $(BACK_DIR) && SP_RUNMODE=test SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0" \
		SP_DB_MIGRATION_GUARD_MODE=warn \
		go run ./cmd/devloop $(DEVLOOP_LOG_FLAGS) $(DEVLOOP_PROCS)

dev-saas: kill ## Run backend (SaaS mode) + dash0 + status0 — pairs with ../solidping-billing `make dev`
	@echo "Running application in SaaS mode (billing via ../solidping-billing on :4050)..."
	@echo "  upgrade URL template: $(SAAS_UPGRADE_URL)"
	@cd $(BACK_DIR) && \
		SP_DB_MIGRATION_GUARD_MODE=warn \
		SP_DEPLOYMENT_MODE=saas \
		SP_ENTITLEMENTS_SERVICE_TOKEN="$(SAAS_BILLING_TOKEN)" \
		SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE="$(SAAS_UPGRADE_URL)" \
		SP_ENTITLEMENTS_ADMIN_WRITES_ENABLED=true \
		SP_ENTITLEMENTS_SERVICE_SIGNING_KEYS='$(SAAS_SIGNING_KEYS_IN)' \
		SP_ENTITLEMENTS_OUTBOUND_SIGNING_KEYS='$(SAAS_SIGNING_KEYS_OUT)' \
		SP_ENTITLEMENTS_ALLOW_LEGACY_SERVICE_TOKEN=$(SAAS_ALLOW_LEGACY_TOKEN) \
		SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0" \
		go run ./cmd/devloop $(DEVLOOP_LOG_FLAGS) $(DEVLOOP_PROCS)

clean: ## Remove built binaries and dash artifacts
	@echo "Cleaning build artifacts..."
	@rm -f $(APP_NAME)
	@rm -rf bin/
	@rm -rf $(DASH_DIST)
	@rm -rf $(BACK_RES)
	@rm -rf $(DASH0_DIST)
	@rm -rf $(BACK_DASH0_RES)
	@rm -rf $(STATUS0_DIST)
	@rm -rf $(BACK_STATUS0_RES)
	@rm $(shell find . -name "*.db*")
	@echo "Clean complete"

clean-all: clean ## Remove all generated files including node_modules
	@echo "Cleaning all generated files..."
	@rm -rf $(DASH_DIR)/node_modules $(DASH_DIR)/.bun
	@rm -rf $(DASH0_DIR)/node_modules $(DASH0_DIR)/.bun
	@rm -rf $(STATUS0_DIR)/node_modules $(STATUS0_DIR)/.bun
	@echo "Deep clean complete"

test: ## Run all tests
	@echo "Running backend tests..."
	@cd $(BACK_DIR) && go test ./... -short
	@echo "Tests complete"

test-scenario: ## Run full-pipeline scenario tests (requires Docker / embedded Postgres)
	@echo "Running scenario integration tests..."
	@cd $(BACK_DIR) && go test -v -timeout 120s ./test/integration/scenario/...
	@echo "Scenario tests complete"

test-dash: ## Run dash tests
	@echo "Running dash tests..."
	@cd $(DASH_DIR) && bun test
	@echo "Dash tests complete"

test-dash0: ## Run dash0 unit tests (mirrors the CI step)
	@echo "Running dash0 unit tests..."
	@cd $(DASH0_DIR) && bun run test:unit
	@echo "Dash0 unit tests complete"

showcase: ## Regenerate the docs showcase media (screenshots + AV1 video) from the real dash0 UI
	@echo "Recording showcase media (needs a running SolidPing server)..."
	@cd $(DASH0_DIR) && bunx playwright test --config=showcase/playwright.config.ts
	@echo "Post-processing (trim + AV1 re-encode)..."
	@cd $(DASH0_DIR) && bun run showcase/postprocess.ts
	@echo "Showcase media written to web/docs/static/showcase/ — commit the changed assets."

lint-back: ## Run backend linter
	@echo "Running backend linter..."
	@cd $(BACK_DIR) && golangci-lint run ./...
	@echo "Backend linting complete"

lint-dash: ## Run dash linter
	@echo "Running dash linter..."
	@cd $(DASH_DIR) && bun run lint
	@echo "Dash linting complete"

lint: lint-back lint-dash ## Run all linters

fmt: ## Format code
	@echo "Formatting backend code..."
	@cd $(BACK_DIR) && go fmt ./...
	@echo "Formatting dash code..."
	@cd $(DASH_DIR) && bun run lint --fix || true
	@echo "Code formatting complete"

dev-dash: ## Start dash development server
	@echo "Starting dash dev server..."
	@cd $(DASH_DIR) && bun run dev

dev-dash0: ## Start dash0 development server
	@echo "Starting dash0 dev server..."
	@cd $(DASH0_DIR) && bun run dev

dev-status0: ## Start status0 development server
	@echo "Starting status0 dev server..."
	@cd $(STATUS0_DIR) && bun run dev

dev-docs: ## Start docs (Docusaurus) dev server on :3000
	@echo "Starting docs dev server..."
	@cd $(DOCS_DIR) && bun run gen-api-docs && bun run start

dev-backend: ## Start backend development server (hot reload via cmd/devloop, rotating log)
	@echo "Starting backend dev server..."
	@cd $(BACK_DIR) && go run ./cmd/devloop $(DEVLOOP_LOG_FLAGS)

deps: ## Install all dependencies
	@echo "Installing backend dependencies..."
	@cd $(BACK_DIR) && go mod download
	@echo "Installing dash dependencies..."
	@cd $(DASH_DIR) && bun install
	@echo "Installing dash0 dependencies..."
	@cd $(DASH0_DIR) && bun install
	@echo "Installing status0 dependencies..."
	@cd $(STATUS0_DIR) && bun install
	@echo "Dependencies installed"

migrate: ## Run database migrations
	@echo "Running database migrations..."
	@./$(APP_NAME) migrate
	@echo "Migrations complete"
