SHELL := /bin/sh

GO ?= go
PROTOC ?= protoc

BIN_DIR ?= $(CURDIR)/bin
TMP_DIR ?= $(CURDIR)/.tmp
DATABASE_URL ?= postgres://nic@localhost/gitslice_dev?sslmode=disable
TEST_DATABASE_URL ?= $(DATABASE_URL)
OBJECT_STORE_ROOT ?= $(TMP_DIR)/object-store
GIT_CACHE_ROOT ?= $(TMP_DIR)/git-cache
GRPC_ADDR ?= 127.0.0.1:50051
HTTP_ADDR ?= 127.0.0.1:8080
GIT_HTTP_ADDR ?= 127.0.0.1:8081
LOAD_WORKERS ?= 8
LOAD_STATUS_ITERATIONS ?= 4

GO_PACKAGES := ./...
GO_FILES := $(shell find cmd internal server service tests -name '*.go' -type f)
PROTO_FILES := $(wildcard proto/core/v1/*.proto)

.DEFAULT_GOAL := help

.PHONY: help deps fmt test build check install dev-install run-server server functional load proto clean

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Download Go module dependencies.
	$(GO) mod download

fmt: ## Format Go source files.
	gofmt -w $(GO_FILES)

test: ## Run the default Go test suite.
	$(GO) test $(GO_PACKAGES)

build: ## Build local gs and gitslice-server binaries into ./bin.
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/gs ./cmd/gs
	$(GO) build -o $(BIN_DIR)/gitslice-server ./cmd/gitslice-server

check: fmt test build ## Format, test, and build.

install: ## Install CLI and server binaries into the active Go install path.
	$(GO) install ./cmd/...

dev-install: ## Install CLI and server binaries into ./bin for local development.
	mkdir -p $(BIN_DIR)
	GOBIN=$(BIN_DIR) $(GO) install ./cmd/...
	@printf "installed gs and gitslice-server into %s\\n" "$(BIN_DIR)"

run-server: ## Run the local server with PostgreSQL and filesystem object storage.
	mkdir -p $(OBJECT_STORE_ROOT) $(GIT_CACHE_ROOT)
	GITSLICE_DATABASE_URL="$(DATABASE_URL)" \
	GITSLICE_OBJECT_STORE_ROOT="$(OBJECT_STORE_ROOT)" \
	GITSLICE_GRPC_ADDR="$(GRPC_ADDR)" \
	GITSLICE_HTTP_ADDR="$(HTTP_ADDR)" \
	GITSLICE_GIT_HTTP_ADDR="$(GIT_HTTP_ADDR)" \
	GITSLICE_GIT_CACHE_ROOT="$(GIT_CACHE_ROOT)" \
	$(GO) run ./cmd/gitslice-server

server: run-server ## Alias for run-server.

functional: ## Run real-Postgres functional tests.
	GITSLICE_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -count=1 ./tests/functional -v

load: ## Run opt-in load tests against local PostgreSQL.
	GITSLICE_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
	GITSLICE_LOAD_WORKERS="$(LOAD_WORKERS)" \
	GITSLICE_LOAD_STATUS_ITERATIONS="$(LOAD_STATUS_ITERATIONS)" \
	$(GO) test -count=1 -tags load ./tests/load -v

proto: ## Regenerate protobuf, gRPC, and grpc-gateway Go files.
	$(PROTOC) --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative --go-grpc_opt=require_unimplemented_servers=false $(PROTO_FILES)
	$(PROTOC) --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative --grpc-gateway_opt=generate_unbound_methods=true $(PROTO_FILES)

clean: ## Remove local build and development scratch directories.
	rm -rf $(BIN_DIR) $(TMP_DIR)
