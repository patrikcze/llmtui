# llmtui — build automation

BINARY  := llmtui
MODULE  := github.com/patrikcze/llmtui
MAIN    := ./cmd/llmtui
DIST    := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# GOOS/GOARCH pairs built by `make dist`
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.DEFAULT_GOAL := help

## help: list available targets
.PHONY: help
help:
	@echo "llmtui $(VERSION)"
	@echo
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  make /' | column -t -s ':'

## build: compile the binary for the current platform
.PHONY: build
build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) $(MAIN)

## run: build and start the chat TUI
.PHONY: run
run: build
	./$(BINARY) chat

## install: install into GOPATH/bin
.PHONY: install
install:
	go install -ldflags '$(LDFLAGS)' $(MAIN)

## fmt: format all Go sources
.PHONY: fmt
fmt:
	go fmt ./...

## vet: run go vet static analysis
.PHONY: vet
vet:
	go vet ./...

## lint: run golangci-lint when available (skips otherwise)
.PHONY: lint
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed — skipping (https://golangci-lint.run)"; \
	fi

## test: run unit tests
.PHONY: test
test:
	go test ./...

## test-race: run unit tests with the race detector
.PHONY: test-race
test-race:
	go test -race ./...

## cover: run tests with coverage report
.PHONY: cover
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "open with: go tool cover -html=coverage.out"

## check: fmt + vet + lint + race tests (run before committing)
.PHONY: check
check: fmt vet lint test-race

## tidy: sync go.mod/go.sum
.PHONY: tidy
tidy:
	go mod tidy

## dist: cross-compile release binaries with checksums into dist/
.PHONY: dist
dist:
	@rm -rf $(DIST)
	@mkdir -p $(DIST)
	@set -e; for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*}; GOARCH=$${platform#*/}; \
		out=$(DIST)/$(BINARY)_$(VERSION)_$${GOOS}_$${GOARCH}; \
		[ "$$GOOS" = "windows" ] && out=$$out.exe; \
		echo "  building $$out"; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH \
			go build -trimpath -ldflags '$(LDFLAGS)' -o $$out $(MAIN); \
	done
	@cd $(DIST) && shasum -a 256 * > checksums.txt
	@echo "release artifacts in $(DIST)/"

## clean: remove build artifacts, coverage and dist output
.PHONY: clean
clean:
	rm -f $(BINARY) coverage.out
	rm -rf $(DIST)
	go clean
