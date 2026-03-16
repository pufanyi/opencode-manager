.PHONY: build run clean setup tidy

BINARY := opencode-manager
BUILD_DIR := ./bin

setup: build
	$(BUILD_DIR)/$(BINARY) setup

build:
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager

run: build
	$(BUILD_DIR)/$(BINARY) -config opencode-manager.yaml

clean:
	rm -rf $(BUILD_DIR)

tidy:
	go mod tidy
