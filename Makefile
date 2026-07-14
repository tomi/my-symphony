# Symphony — developer commands.
# Run `make` or `make help` to list targets.

GO        ?= go
BIN_DIR   ?= bin
PKGS      ?= ./...
WORKFLOW  ?= WORKFLOW.md

.DEFAULT_GOAL := help

## help: Show this help.
.PHONY: help
help:
	@echo "Symphony — available make targets:"
	@grep -E '^## [a-zA-Z_-]+:' $(MAKEFILE_LIST) | sed 's/## //' | awk -F': ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## build: Compile all packages and binaries into ./bin.
.PHONY: build
build:
	$(GO) build -o $(BIN_DIR)/symphony ./cmd/symphony
	$(GO) build -o $(BIN_DIR)/symphony-lineargql-mcp ./cmd/symphony-lineargql-mcp

## check-build: Type-check/compile every package (no output binaries).
.PHONY: check-build
check-build:
	$(GO) build $(PKGS)

## run: Run the daemon against WORKFLOW (override with WORKFLOW=path).
.PHONY: run
run:
	$(GO) run ./cmd/symphony $(WORKFLOW)

## test: Run the full test suite.
.PHONY: test
test:
	$(GO) test $(PKGS)

## test-race: Run the full test suite with the race detector.
.PHONY: test-race
test-race:
	$(GO) test -race $(PKGS)

## cover: Run tests and write a coverage profile to coverage.out.
.PHONY: cover
cover:
	$(GO) test -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

## fmt: Format all Go files in place.
.PHONY: fmt
fmt:
	gofmt -w .

## fmt-check: Fail if any Go file is not gofmt-formatted.
.PHONY: fmt-check
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		echo "$$unformatted"; \
		echo "Run 'make fmt' to fix."; \
		exit 1; \
	fi

## vet: Run go vet across all packages.
.PHONY: vet
vet:
	$(GO) vet $(PKGS)

## lint: Alias for vet (the CI lint step).
.PHONY: lint
lint: vet

## tidy: Run go mod tidy.
.PHONY: tidy
tidy:
	$(GO) mod tidy

## tidy-check: Fail if go.mod/go.sum are not tidy.
.PHONY: tidy-check
tidy-check:
	$(GO) mod tidy
	@if ! git diff --quiet -- go.mod go.sum; then \
		echo "go.mod / go.sum are not tidy. Run 'make tidy' and commit the result."; \
		git --no-pager diff -- go.mod go.sum; \
		exit 1; \
	fi

## ci: Run the same checks as the CI workflow.
.PHONY: ci
ci: fmt-check tidy-check check-build vet test-race

## clean: Remove build and coverage artifacts.
.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out
	$(GO) clean
