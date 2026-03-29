.PHONY: docker-build build build-backend build-dash build-dash0 build-status0 copy-dash copy-dash0 copy-status0 \
	build-cli install-cli clean clean-all run run-test dev-test dev-dash dev-dash0 dev-status0 dev-backend \
	test test-dash lint lint-back lint-dash fmt deps migrate help
.DEFAULT_GOAL := build

APP_NAME := solidping

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
BACK_DIR := server
BACK_RES := $(BACK_DIR)/internal/app/res/
BACK_DASH0_RES := $(BACK_DIR)/internal/app/dash0res/
BACK_STATUS0_RES := $(BACK_DIR)/internal/app/status0res/
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

build: build-dash copy-dash build-dash0 copy-dash0 build-status0 copy-status0 build-backend ## Build complete application

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

run: build ## Build and run the application
	@echo "Running application..."
	@./$(APP_NAME) serve

run-test: build ## Build and run the application in test mode
	@echo "Running application in test mode..."
	@SP_RUNMODE=test ./$(APP_NAME) serve

dev-test: kill ## Run backend, dash0 and status0 in development test mode
	@echo "Running application in development test mode..."
	@mkdir -p $(LOG_DIR)
	@cd $(DASH0_DIR) && bun run dev 2>&1 | tee $(CURDIR)/$(LOG_DIR)/dash0.log &
	@cd $(STATUS0_DIR) && bun run dev 2>&1 | tee $(CURDIR)/$(LOG_DIR)/status0.log &
	@cd $(BACK_DIR) && SP_RUNMODE=test SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0" air 2>&1 | tee $(CURDIR)/$(LOG_DIR)/backend.log

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

test-dash: ## Run dash tests
	@echo "Running dash tests..."
	@cd $(DASH_DIR) && bun test
	@echo "Dash tests complete"

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

dev-backend: ## Start backend development server
	@echo "Starting backend dev server..."
	@mkdir -p $(LOG_DIR)
	@cd $(BACK_DIR) && go run . serve 2>&1 | tee $(CURDIR)/$(LOG_DIR)/backend.log

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
