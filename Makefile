# llmtui — build automation

BINARY  := llmtui
MODULE  := github.com/patrikcze/llmtui
MAIN    := ./cmd/llmtui
DIST    := dist
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
INSTALL_CMD ?= install

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

TARGET_GOOS   ?= $(shell go env GOOS)
TARGET_GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED   ?= $(if $(filter darwin android,$(TARGET_GOOS)),1,0)
TARGET_EXT    := $(if $(filter windows,$(TARGET_GOOS)),.exe,)
TARGET_OUT    := $(DIST)/$(BINARY)_$(VERSION)_$(TARGET_GOOS)_$(TARGET_GOARCH)$(TARGET_EXT)
ARCHIVE_BASE  := $(BINARY)-$(VERSION)-$(TARGET_GOOS)-$(TARGET_GOARCH)
ARCHIVE_EXT   := $(if $(filter windows,$(TARGET_GOOS)),zip,tar.gz)
ARCHIVE_OUT   := $(DIST)/$(ARCHIVE_BASE).$(ARCHIVE_EXT)
ARCHIVE_STAGE := $(DIST)/$(ARCHIVE_BASE)

# Android cross-compilation via the NDK's standalone clang wrappers. Only
# consulted when TARGET_GOOS=android — every other platform build ignores
# these entirely. GOOS=android requires CGO_ENABLED=1 (both purego's
# dlopen shim and Go's DNS resolver need it there) and -buildmode=pie
# (Bionic refuses to exec a non-PIE ELF — see docs/android.md for the
# "unexpected e_type: 2" error this avoids). Building on-device inside
# Termux needs none of this: Termux's own toolchain already targets
# Bionic, so a plain `go build` there just works — see docs/android.md.
ANDROID_API      ?= 24
ANDROID_NDK_HOME ?= $(ANDROID_NDK_ROOT)
ifeq ($(strip $(ANDROID_NDK_HOME)),)
ANDROID_NDK_HOME := $(ANDROID_NDK_LATEST_HOME)
endif
NDK_HOST_TAG := $(shell u=$$(uname -s); if [ "$$u" = "Darwin" ]; then echo darwin-x86_64; elif [ "$$u" = "Linux" ]; then echo linux-x86_64; else echo windows-x86_64; fi)
ifeq ($(TARGET_GOARCH),arm64)
ANDROID_TRIPLE := aarch64-linux-android
else ifeq ($(TARGET_GOARCH),amd64)
ANDROID_TRIPLE := x86_64-linux-android
else ifeq ($(TARGET_GOARCH),arm)
ANDROID_TRIPLE := armv7a-linux-androideabi
else ifeq ($(TARGET_GOARCH),386)
ANDROID_TRIPLE := i686-linux-android
endif
ANDROID_CC  := $(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/$(NDK_HOST_TAG)/bin/$(ANDROID_TRIPLE)$(ANDROID_API)-clang
ANDROID_CXX := $(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/$(NDK_HOST_TAG)/bin/$(ANDROID_TRIPLE)$(ANDROID_API)-clang++
# Recursively expanded (=, not :=): `go list -m` must run after dist-platform
# has forced module extraction, not at Makefile parse time. On a cold module
# cache, evaluating this at parse time (:= ) silently yields an empty string
# and corrupts the LICENSE install paths below.
YZMA_DIR       = $(shell go list -m -f '{{.Dir}}' github.com/hybridgroup/yzma)
PUREGO_DIR     = $(shell go list -m -f '{{.Dir}}' github.com/ebitengine/purego)

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
	$(INSTALL_CMD) -d $(DESTDIR)$(BINDIR)
	$(INSTALL_CMD) -m 0755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
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
	@$(MAKE) --no-print-directory dist-archive
	@$(MAKE) --no-print-directory dist-checksums

## dist-platform: build one release binary for TARGET_GOOS/TARGET_GOARCH
.PHONY: dist-platform
dist-platform:
	@mkdir -p $(DIST)
	@if [ "$(TARGET_GOOS)" = "android" ]; then \
		if [ -z "$(ANDROID_NDK_HOME)" ]; then \
			echo "error: TARGET_GOOS=android needs the Android NDK; set ANDROID_NDK_HOME (or ANDROID_NDK_ROOT/ANDROID_NDK_LATEST_HOME)." >&2; \
			echo "       Building on-device inside Termux needs none of this — see docs/android.md." >&2; \
			exit 1; \
		fi; \
		if [ -z "$(ANDROID_TRIPLE)" ]; then \
			echo "error: no NDK clang triple known for TARGET_GOARCH=$(TARGET_GOARCH) (supported: arm64, amd64, arm, 386)" >&2; \
			exit 1; \
		fi; \
		if [ ! -x "$(ANDROID_CC)" ]; then \
			echo "error: Android NDK clang not found at $(ANDROID_CC)" >&2; \
			echo "       checked ANDROID_API=$(ANDROID_API) under host tag $(NDK_HOST_TAG); pass ANDROID_API=<level> to match your NDK." >&2; \
			exit 1; \
		fi; \
		echo "  building $(TARGET_OUT) (android NDK, API $(ANDROID_API), CGO_ENABLED=1, buildmode=pie)"; \
		CC="$(ANDROID_CC)" CXX="$(ANDROID_CXX)" CGO_ENABLED=1 GOOS=android GOARCH=$(TARGET_GOARCH) \
			go build -trimpath -buildmode=pie -ldflags '$(LDFLAGS)' -o $(TARGET_OUT) $(MAIN); \
	else \
		echo "  building $(TARGET_OUT) (CGO_ENABLED=$(CGO_ENABLED))"; \
		CGO_ENABLED=$(CGO_ENABLED) GOOS=$(TARGET_GOOS) GOARCH=$(TARGET_GOARCH) \
			go build -trimpath -ldflags '$(LDFLAGS)' -o $(TARGET_OUT) $(MAIN); \
	fi

## dist-archive: build a self-contained binary + verified runtime archive
## (android has no upstream llama.cpp release to bundle — see dist-archive-android)
.PHONY: dist-archive
dist-archive:
ifeq ($(TARGET_GOOS),android)
	@$(MAKE) --no-print-directory dist-archive-android
else
	@$(MAKE) --no-print-directory dist-archive-native
endif

## dist-archive-native: binary + verified embedded llama.cpp runtime archive,
## for platforms with an upstream llama.cpp release (see internal/runtime/pin.json)
.PHONY: dist-archive-native
dist-archive-native: dist-platform
	@if [ "$(shell go env GOOS)-$(shell go env GOARCH)" != "$(TARGET_GOOS)-$(TARGET_GOARCH)" ]; then \
		echo "runtime staging must run on native $(TARGET_GOOS)/$(TARGET_GOARCH) to install and verify that platform's llama.cpp binaries" >&2; \
		exit 1; \
	fi
	@if [ "$(TARGET_GOOS)" = "windows" ] && ! command -v 7z >/dev/null 2>&1; then \
		echo "7z is required to build the Windows release archive" >&2; \
		exit 1; \
	fi
	@if [ -z "$(YZMA_DIR)" ] || [ -z "$(PUREGO_DIR)" ]; then \
		echo "error: could not resolve yzma/purego module directories via 'go list -m'; run 'go mod download' first" >&2; \
		exit 1; \
	fi
	@rm -rf $(ARCHIVE_STAGE) $(ARCHIVE_OUT)
	@mkdir -p $(ARCHIVE_STAGE)/lib/llmtui $(ARCHIVE_STAGE)/licenses
	@$(INSTALL_CMD) -m 0755 $(TARGET_OUT) $(ARCHIVE_STAGE)/$(BINARY)$(TARGET_EXT)
	@$(INSTALL_CMD) -m 0644 LICENSE $(ARCHIVE_STAGE)/LICENSE
	@$(INSTALL_CMD) -m 0644 THIRD_PARTY_NOTICES.md $(ARCHIVE_STAGE)/THIRD_PARTY_NOTICES.md
	@$(INSTALL_CMD) -m 0644 $(YZMA_DIR)/LICENSE $(ARCHIVE_STAGE)/licenses/yzma-APACHE-2.0.txt
	@$(INSTALL_CMD) -m 0644 $(PUREGO_DIR)/LICENSE $(ARCHIVE_STAGE)/licenses/purego-APACHE-2.0.txt
	@$(INSTALL_CMD) -m 0644 third_party/ffi/LICENSE $(ARCHIVE_STAGE)/licenses/ffi-MIT.txt
	@go run $(MAIN) runtime install --dest $(ARCHIVE_STAGE)/lib/llmtui/runtime
	@if [ "$(TARGET_GOOS)" = "windows" ]; then \
		cd $(DIST) && 7z a -tzip $(notdir $(ARCHIVE_OUT)) $(ARCHIVE_BASE) >/dev/null; \
	else \
		tar -C $(DIST) -czf $(ARCHIVE_OUT) $(ARCHIVE_BASE); \
	fi
	@rm -rf $(ARCHIVE_STAGE)
	@rm -f $(TARGET_OUT)
	@echo "  packaged $(ARCHIVE_OUT)"

## dist-archive-android: binary-only archive, no embedded llama.cpp runtime —
## llama.cpp publishes no official Android release, so the embedded provider
## is unavailable on this platform; network providers (Ollama, LM Studio,
## any OpenAI-compatible endpoint) work normally. See docs/android.md.
.PHONY: dist-archive-android
dist-archive-android: dist-platform
	@rm -rf $(ARCHIVE_STAGE) $(ARCHIVE_OUT)
	@mkdir -p $(ARCHIVE_STAGE)
	@$(INSTALL_CMD) -m 0755 $(TARGET_OUT) $(ARCHIVE_STAGE)/$(BINARY)$(TARGET_EXT)
	@$(INSTALL_CMD) -m 0644 LICENSE $(ARCHIVE_STAGE)/LICENSE
	@$(INSTALL_CMD) -m 0644 THIRD_PARTY_NOTICES.md $(ARCHIVE_STAGE)/THIRD_PARTY_NOTICES.md
	@$(INSTALL_CMD) -m 0644 docs/android.md $(ARCHIVE_STAGE)/ANDROID.md
	@tar -C $(DIST) -czf $(ARCHIVE_OUT) $(ARCHIVE_BASE)
	@rm -rf $(ARCHIVE_STAGE)
	@rm -f $(TARGET_OUT)
	@echo "  packaged $(ARCHIVE_OUT) (no embedded runtime — see ANDROID.md)"

## dist-checksums: write checksums for existing dist artifacts
.PHONY: dist-checksums
dist-checksums:
	@rm -f $(DIST)/checksums.txt
	@cd $(DIST) && find . -maxdepth 1 -type f ! -name checksums.txt -print | LC_ALL=C sort | xargs shasum -a 256 > checksums.txt
	@echo "release artifacts in $(DIST)/"

## clean: remove build artifacts, coverage and dist output
.PHONY: clean
clean:
	rm -f $(BINARY) coverage.out
	rm -rf $(DIST)
	go clean
