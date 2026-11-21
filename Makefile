GO ?= go
BIN := bin/roomcord-bridge-host
DATA_DIR ?= ./data

.PHONY: build build-signal test run vet

build:
	$(GO) build -o $(BIN) ./cmd/roomcord-bridge-host

# Signal needs the Rust static library libsignal_ffi.a (docs/networks.md, section signal).
LIBSIGNAL_DIR ?=
build-signal:
	@test -n "$(LIBSIGNAL_DIR)" || (echo "set LIBSIGNAL_DIR to the directory that holds libsignal_ffi.a" >&2; exit 1)
	LIBRARY_PATH=$(LIBSIGNAL_DIR):$$LIBRARY_PATH $(GO) build -tags signal -o $(BIN) ./cmd/roomcord-bridge-host

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

run: build
	@test -n "$$BRIDGE_HOST_TOKEN" || (echo "set BRIDGE_HOST_TOKEN" >&2; exit 1)
	BRIDGE_HOST_DATA_DIR=$(DATA_DIR) ./$(BIN)
