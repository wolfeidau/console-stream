# Console Stream Makefile
# Maintenance tasks for the console-stream library

.PHONY: help test test-race test-coverage lint lint-fix build build-examples check quality clean examples all docs

# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

# Test targets
test: ## Run all tests
	go test -v ./...

test-race: ## Run tests with race detector
	go test -race -v ./...

test-coverage: ## Run tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Lint targets
lint: ## Run golangci-lint
	golangci-lint run ./...

lint-fix: ## Run golangci-lint with auto-fix
	golangci-lint run --fix ./...

# Build targets
build: ## Build all commands
	go build -o bin/runner ./cmd/runner
	go build -o bin/tester ./cmd/tester

build-examples: ## Build all examples
	@mkdir -p bin/examples
	go build -o bin/examples/simple ./example/simple
	go build -o bin/examples/stream ./example/stream
	go build -o bin/examples/burst ./example/burst
	go build -o bin/examples/pty ./example/pty
	go build -o bin/examples/buffering ./example/buffering
	go build -o bin/examples/asciicast ./example/asciicast
	go build -o bin/examples/logging ./example/logging

# Development targets
check: ## Run basic checks (mod tidy)
	go mod tidy
	@if [ -n "$$(git status --porcelain go.mod go.sum)" ]; then \
		echo "go.mod or go.sum has uncommitted changes after 'go mod tidy'"; \
		exit 1; \
	fi

# Quality targets
quality: check lint test ## Run all quality checks

# Clean targets
clean: ## Clean build artifacts and test files
	rm -rf bin/
	rm -f coverage.out coverage.html
	rm -f *.cast

# Meta targets
all: clean check lint test build build-examples ## Run all tasks

# Documentation
docs: ## Generate documentation
	@echo "Generating documentation..."
	go doc -all . > docs.txt
	@echo "Documentation generated: docs.txt"