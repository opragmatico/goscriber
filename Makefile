# Goscriber development tasks. Run `make help` for the list.

GO      ?= go
BIN     ?= goscriber
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# golangci-lint is pinned and installed into ./bin on first use, so every
# machine and CI lint with the same version. It must be built with a Go at
# least as new as go.mod's go line, or it refuses to load the module, so it is
# built with the toolchain this module selects (GOTOOLCHAIN would otherwise
# pick the older Go that golangci-lint's own go.mod asks for). That Go version
# is part of the binary's name, so a Go upgrade rebuilds it.
GOLANGCI_LINT_VERSION := v2.15.0
TOOLS_BIN             := $(CURDIR)/bin
GO_TOOLCHAIN          := $(shell $(GO) env GOVERSION)
GOLANGCI_LINT         := $(TOOLS_BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GO_TOOLCHAIN)

# Packages allowed to use CGo: app wiring and the Fyne UI, native platform code,
# and the NeMo-Speech.cpp binding. Everything else must build and test with
# CGO_ENABLED=0 (see test-nocgo); internal/audio does, with its malgo backend
# left out.
CGO_EDGE := /internal/(app|ui|platform|engine/nemo)(/|$$)

# The NeMo-Speech.cpp SDK that `make deps` builds (scripts/build-nemo-speech.sh).
# Its pkg-config directory goes first on PKG_CONFIG_PATH, so the binding's
# `#cgo pkg-config: nemo-speech-asr` finds it. Outside make, export it yourself.
NEMO_SPEECH_PREFIX := $(CURDIR)/.deps/nemo-speech
export PKG_CONFIG_PATH := $(NEMO_SPEECH_PREFIX)/lib/pkgconfig$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH))
# The binding's CGo files need this tag until it is wired in (docs/engine-sdk.md).
NEMO_TAGS := nemo
# `make build` links the engine into ./goscriber, for `goscriber transcribe`,
# once `make deps` has built the SDK; without the SDK it builds without one.
BUILD_TAGS ?= $(if $(wildcard $(NEMO_SPEECH_PREFIX)/lib/pkgconfig/nemo-speech-asr.pc),$(NEMO_TAGS))

.DEFAULT_GOAL := build
.PHONY: help build run app test test-race test-nocgo test-engine engine-version bench vet lint fmt deps tidy clean clean-deps

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build ./goscriber with the version stamped in (and the engine, after make deps)
	$(GO) build -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/goscriber

run: build ## Build and run; pass flags with ARGS="--version"
	./$(BIN) $(ARGS)

# A development bundle for macOS: no icon, no NeMo-Speech.cpp libraries yet
# (the release bundle comes with issue #34). Its Info.plist sets LSUIElement,
# so the app lives in the menu bar with no Dock icon, and carries the
# microphone usage description macOS shows in its prompt.
#
# macOS attaches the microphone and Accessibility permissions to the bundle's
# code signature. The default ad-hoc signature ("-") changes with every
# build, so macOS asks again after each rebuild; sign with a stable identity
# to keep them, such as CODESIGN_IDENTITY="Apple Development: Your Name".
# See docs/permissions.md.
APP_BUNDLE := dist/Goscriber.app
CODESIGN_IDENTITY ?= -
# CFBundleVersion only takes dotted numbers: a vX.Y[.Z] tag loses its "v" and
# any "-commits-gHASH" suffix, and anything else (an untagged build) is 0.0.0.
BUNDLE_VERSION := $(shell echo '$(VERSION)' | sed -nE 's/^v?([0-9]+\.[0-9]+(\.[0-9]+)?)(-.*)?$$/\1/p')

app: build ## Assemble and sign dist/Goscriber.app (macOS development bundle; CODESIGN_IDENTITY=-)
	rm -rf $(APP_BUNDLE)
	mkdir -p $(APP_BUNDLE)/Contents/MacOS
	sed 's/@VERSION@/$(or $(BUNDLE_VERSION),0.0.0)/g' packaging/macos/Info.plist > $(APP_BUNDLE)/Contents/Info.plist
	cp $(BIN) $(APP_BUNDLE)/Contents/MacOS/goscriber
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --force --sign '$(CODESIGN_IDENTITY)' $(APP_BUNDLE); \
	else \
		echo "codesign not found: $(APP_BUNDLE) is not signed"; \
	fi

test: test-race test-nocgo ## Run every test with the race detector, then the CGo-free check

test-race: ## go test -race ./...
	$(GO) test -race ./...

test-nocgo: ## Test the packages outside the CGo edge with CGO_ENABLED=0
	CGO_ENABLED=0 $(GO) test $$($(GO) list ./... | grep -vE '$(CGO_EDGE)')

test-engine: ## Vet and test the NeMo-Speech.cpp binding (needs make deps; GOSCRIBER_TEST_MODELS adds model tests)
	$(GO) vet -tags $(NEMO_TAGS) ./internal/engine/nemo/...
	$(GO) test -race -tags $(NEMO_TAGS) ./internal/engine/nemo/...

bench: build ## Benchmark both models on Metal and the CPU over testdata/audio (needs make deps and the models)
	scripts/bench.sh

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
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN=$(TOOLS_BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS_BIN)/golangci-lint $@
