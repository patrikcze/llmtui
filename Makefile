# llmtui — build automation

BINARY  := llmtui
MODULE  := github.com/patrikcze/llmtui
MAIN    := ./cmd/llmtui
DIST    := dist
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
INSTALL ?= install

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

TARGET_GOOS   ?= $(shell go env GOOS)
TARGET_GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED   ?= $(if $(filter darwin,$(TARGET_GOOS)),1,0)
TARGET_EXT    := $(if $(filter windows,$(TARGET_GOOS)),.exe,)
TARGET_OUT    := $(DIST)/$(BINARY)_$(VERSION)_$(TARGET_GOOS)_$(TARGET_GOARCH)$(TARGET_EXT)

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

## install: build and install into BINDIR
.PHONY: install
install: build
	$(INSTALL) -d $(DESTDIR)$(BINDIR)
	$(INSTALL) -m 0755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
	@echo "installed $(BINARY) to $(DESTDIR)$(BINDIR)"

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

## dist: build the current native release binary with checksums into dist/
.PHONY: dist
dist:
	@rm -rf $(DIST)
	@$(MAKE) --no-print-directory dist-platform
	@$(MAKE) --no-print-directory dist-checksums

## dist-platform: build one release binary for TARGET_GOOS/TARGET_GOARCH
.PHONY: dist-platform
dist-platform:
	@mkdir -p $(DIST)
	@echo "  building $(TARGET_OUT) (CGO_ENABLED=$(CGO_ENABLED))"
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(TARGET_GOOS) GOARCH=$(TARGET_GOARCH) \
		go build -trimpath -ldflags '$(LDFLAGS)' -o $(TARGET_OUT) $(MAIN)

## dist-checksums: write checksums for existing dist artifacts
.PHONY: dist-checksums
dist-checksums:
	@rm -f $(DIST)/checksums.txt
	@cd $(DIST) && shasum -a 256 * > checksums.txt
	@echo "release artifacts in $(DIST)/"

## clean: remove build artifacts, coverage and dist output
.PHONY: clean
clean:
	rm -f $(BINARY) coverage.out
	rm -rf $(DIST)
	go clean
