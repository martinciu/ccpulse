.PHONY: build install seed-dev seed-dev-config seed-dev-cache seed-front-loaded reset-dev test lint snapshot

BIN := ccpulse
INSTALL_DIR := $(HOME)/.local/bin
RELEASE_LDFLAGS := -ldflags="-X main.buildChannel=release"

build:
	go build -o $(BIN) ./cmd/ccpulse

install:
	go build $(RELEASE_LDFLAGS) -o $(INSTALL_DIR)/$(BIN) ./cmd/ccpulse

seed-dev-config:
	./scripts/seed-dev.sh config

seed-dev-cache:
	./scripts/seed-dev.sh cache

seed-dev: seed-dev-config seed-dev-cache

reset-dev:
	./scripts/reset-dev.sh

test:
	go test ./...

lint:
	go vet ./...

snapshot:
	HOMEBREW_TAP_GITHUB_TOKEN=dummy goreleaser release --snapshot --clean

seed-front-loaded: ## Populate usage_samples with a front-loaded shape (issue #170 probe)
	@scripts/seed-front-loaded.sh
