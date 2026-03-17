.PHONY: build run dev clean setup tidy

BINARY := opencode-manager
BUILD_DIR := ./bin
CONFIG := opencode-manager.yaml

setup: build
	$(BUILD_DIR)/$(BINARY) setup

build:
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager

run: build
	$(BUILD_DIR)/$(BINARY) -config $(CONFIG)

dev:
	@echo "Building and running..."
	@go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager && $(BUILD_DIR)/$(BINARY) -config $(CONFIG)

clean:
	rm -rf $(BUILD_DIR)

tidy:
	go mod tidy
