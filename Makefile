# Sluice developer task runner.
# Run `make help` for the list of targets.

# Use bash and fail fast on errors / unset vars / failed pipes.
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO          ?= go
BIN_DIR     := bin
CLI_BIN     := $(BIN_DIR)/sluice
GOLANGCI    := golangci-lint
PKGS        := ./...

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: fmt
fmt: ## Format code with gofmt + goimports
	$(GO) fmt $(PKGS)
	@command -v goimports >/dev/null 2>&1 && goimports -w . || echo "goimports not installed; skipping (run: make tools)"

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(PKGS)

.PHONY: lint
lint: ## Run golangci-lint (install via: make tools)
	@command -v $(GOLANGCI) >/dev/null 2>&1 || { echo "golangci-lint not installed; run: make tools"; exit 1; }
	$(GOLANGCI) run

.PHONY: test
test: ## Run unit tests with race detector
	$(GO) test -race -count=1 $(PKGS)

.PHONY: test-cover
test-cover: ## Run tests and print a coverage summary
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-integration
test-integration: ## Run integration tests (needs Redis); tag: integration
	$(GO) test -race -count=1 -tags=integration $(PKGS)

.PHONY: build
build: ## Build the CLI into bin/sluice
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(CLI_BIN) ./cli

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	$(GO) mod tidy

.PHONY: hooks
hooks: ## Install git pre-commit hook (requires an initialized git repo)
	@test -d .git || { echo "no .git directory; run 'git init' first"; exit 1; }
	cp scripts/hooks/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "installed .git/hooks/pre-commit"

.PHONY: tools
tools: ## Install dev tools (golangci-lint, goimports)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(GO) install golang.org/x/tools/cmd/goimports@latest

.PHONY: ci
ci: vet lint test ## Run everything CI runs

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
