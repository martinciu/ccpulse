# govulncheck is pinned so `make vulncheck` and the CI job scan identically.
# The pinned version's own go directive must stay <= the one in go.mod: CI runs
# this with GOTOOLCHAIN=local, so a newer tool simply fails to resolve. x/vuln
# v1.8.0 requires go 1.26.0; v1.7.0 is the last release on go 1.25.0.
GOVULNCHECK_VERSION ?= v1.7.0

.PHONY: build install seed-dev seed-dev-config seed-dev-cache seed-front-loaded reset-dev test lint lint-fix fmt vulncheck snapshot demo

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
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

fmt:
	golangci-lint fmt ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

snapshot:
	HOMEBREW_TAP_GITHUB_TOKEN=dummy goreleaser release --snapshot --clean

seed-front-loaded: ## Populate usage_samples with a front-loaded shape (issue #170 probe)
	@scripts/seed-front-loaded.sh

demo: ## Record the README demo GIF (synthetic fixtures, no real data, no network)
	@rm -rf demo/.cache-dev demo/.projects
	@mkdir -p demo/.cache-dev demo/.projects
	go build $(RELEASE_LDFLAGS) -o $(BIN) ./cmd/ccpulse
	go run ./scripts/seedyear --cache-dir demo/.cache-dev --profile heavy --seed 1 --days 7
	vhs demo/ccpulse.tape
