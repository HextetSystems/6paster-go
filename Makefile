GO ?= go
BINARY ?= 6paster
CONFIG ?= config.yaml

.DEFAULT_GOAL := help

.PHONY: help build run migrate test test-race vet fmt check deps clean

help: ## Show available tasks
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <task>\n\nTasks:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the service binary
	$(GO) build -trimpath -o $(BINARY) ./cmd/6paster

run: ## Run the service with CONFIG (default: config.yaml)
	$(GO) run ./cmd/6paster serve --config $(CONFIG)

migrate: ## Apply database migrations using CONFIG
	$(GO) run ./cmd/6paster migrate --config $(CONFIG)

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run all tests with the race detector
	$(GO) test -race ./...

vet: ## Run Go static analysis
	$(GO) vet ./...

fmt: ## Format Go source files
	$(GO) fmt ./...

check: test-race vet ## Run the full local verification suite

deps: ## Download Go module dependencies
	$(GO) mod download

clean: ## Remove generated build and test artifacts (keeps databases)
	$(GO) clean -testcache
	rm -f -- $(BINARY) coverage.out
