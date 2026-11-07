.PHONY: help test test-race fuzz vet fmt deps build install clean

PLUGIN_NAME := prompt-optimizer
OUTPUT_DIR  := build
OUTPUT      := $(OUTPUT_DIR)/$(PLUGIN_NAME).so

# Packages that do not import Bifrost. They build and test anywhere.
PURE_PKGS := ./optimizer/... ./stats/... ./dashboard/...

# Set this to the core version your gateway was built with, for example:
#   make deps CORE_VERSION=v1.14.1
# Find it with: go version -m /path/to/bifrost-http | grep bifrost/core
CORE_VERSION ?= latest

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

test: ## Run the unit tests (no Bifrost needed)
	go test $(PURE_PKGS)

test-race: ## Run the unit tests with the race detector (needs gcc)
	CGO_ENABLED=1 go test -race $(PURE_PKGS)

fuzz: ## Fuzz the optimizer for 30 seconds
	go test ./optimizer -run '^$$' -fuzz FuzzOptimize -fuzztime 30s

vet: ## Run go vet on everything that does not import Bifrost
	go vet $(PURE_PKGS)

fmt: ## Format all Go files
	gofmt -w .

deps: ## Add the Bifrost core dependency (needs internet)
	go get github.com/maximhq/bifrost/core@$(CORE_VERSION)
	go mod tidy

build: ## Build build/prompt-optimizer.so (Linux/macOS only; run `make deps` first)
	@mkdir -p $(OUTPUT_DIR)
	CGO_ENABLED=1 go build -buildmode=plugin -o $(OUTPUT) .
	@echo "Built $(OUTPUT)"

install: build ## Copy the plugin to ~/.bifrost/plugins/
	@mkdir -p $(HOME)/.bifrost/plugins
	cp $(OUTPUT) $(HOME)/.bifrost/plugins/
	@echo "Installed to $(HOME)/.bifrost/plugins/$(PLUGIN_NAME).so"

clean: ## Remove build output
	rm -rf $(OUTPUT_DIR)
