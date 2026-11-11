# Goscriber development tasks. Run `make help` for the list.

GO      ?= go
BIN     ?= goscriber
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# golangci-lint is pinned and installed into ./bin on first use, so every
# machine and CI lint with the same version.
GOLANGCI_LINT_VERSION := v2.15.0
TOOLS_BIN             := $(CURDIR)/bin
GOLANGCI_LINT         := $(TOOLS_BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

# Packages allowed to use CGo: app wiring and the Fyne UI, native platform code,
# and the NeMo-Speech.cpp binding. Everything else must build and test with
# CGO_ENABLED=0 (see test-nocgo).
CGO_EDGE := /internal/(app|ui|platform|engine/nemo)(/|$$)

# The NeMo-Speech.cpp SDK that `make deps` builds (scripts/build-nemo-speech.sh).
# Its pkg-config directory goes first on PKG_CONFIG_PATH, so the binding's
# `#cgo pkg-config: nemo-speech-asr` finds it. Outside make, export it yourself.
NEMO_SPEECH_PREFIX := $(CURDIR)/.deps/nemo-speech
export PKG_CONFIG_PATH := $(NEMO_SPEECH_PREFIX)/lib/pkgconfig$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH))
# The binding's CGo files need this tag until it is wired in (docs/engine-sdk.md).
NEMO_TAGS := nemo

.DEFAULT_GOAL := build
.PHONY: help build run app test test-race test-nocgo test-engine engine-version vet lint fmt deps tidy clean clean-deps

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build ./goscriber with the version stamped in
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/goscriber

run: build ## Build and run; pass flags with ARGS="--version"
	./$(BIN) $(ARGS)

# A development bundle for macOS: unsigned, no icon, no NeMo-Speech.cpp
# libraries yet (the release bundle comes with issue #34). Its Info.plist sets
# LSUIElement, so the app lives in the menu bar with no Dock icon.
APP_BUNDLE := dist/Goscriber.app
# CFBundleVersion only takes dotted numbers: a vX.Y[.Z] tag loses its "v" and
# any "-commits-gHASH" suffix, and anything else (an untagged build) is 0.0.0.
BUNDLE_VERSION := $(shell echo '$(VERSION)' | sed -nE 's/^v?([0-9]+\.[0-9]+(\.[0-9]+)?)(-.*)?$$/\1/p')

app: build ## Assemble dist/Goscriber.app (macOS development bundle)
	rm -rf $(APP_BUNDLE)
	mkdir -p $(APP_BUNDLE)/Contents/MacOS
	sed 's/@VERSION@/$(or $(BUNDLE_VERSION),0.0.0)/g' packaging/macos/Info.plist > $(APP_BUNDLE)/Contents/Info.plist
	cp $(BIN) $(APP_BUNDLE)/Contents/MacOS/goscriber

test: test-race test-nocgo ## Run every test with the race detector, then the CGo-free check

test-race: ## go test -race ./...
	$(GO) test -race ./...

test-nocgo: ## Test the packages outside the CGo edge with CGO_ENABLED=0
	CGO_ENABLED=0 $(GO) test $$($(GO) list ./... | grep -vE '$(CGO_EDGE)')

test-engine: ## Vet and test the NeMo-Speech.cpp binding (needs make deps; GOSCRIBER_TEST_MODELS adds model tests)
	$(GO) vet -tags $(NEMO_TAGS) ./internal/engine/nemo/...
	$(GO) test -race -tags $(NEMO_TAGS) ./internal/engine/nemo/...

engine-version: ## Print the NeMo-Speech.cpp version through CGo (needs make deps)
	@$(GO) run -tags $(NEMO_TAGS) ./internal/engine/nemo/cmd/nemo-version

vet: ## go vet ./...
	$(GO) vet ./...

lint: $(GOLANGCI_LINT) ## Run golangci-lint (linters and format check)
	$(GOLANGCI_LINT) run

fmt: $(GOLANGCI_LINT) ## Format the code with gofumpt and goimports
	$(GOLANGCI_LINT) fmt

deps: ## Download Go modules and build the pinned NeMo-Speech.cpp SDK into .deps/
	$(GO) mod download
	scripts/build-nemo-speech.sh

tidy: ## go mod tidy
	$(GO) mod tidy

clean: ## Remove the binary, the app bundle and the pinned tools
	rm -f $(BIN)
	rm -rf $(TOOLS_BIN) $(APP_BUNDLE)

clean-deps: ## Remove the NeMo-Speech.cpp SDK and its build tree (.deps/)
	rm -rf .deps

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS_BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS_BIN)/golangci-lint $@
