.PHONY: build install test lint

BIN := ccpulse
INSTALL_DIR := $(HOME)/.local/bin

build:
	go build -o $(BIN) ./cmd/ccpulse

install:
	go build -o $(INSTALL_DIR)/$(BIN) ./cmd/ccpulse

test:
	go test ./...

lint:
	go vet ./...
