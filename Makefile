.PHONY: build run dev clean setup tidy web

BINARY := opencode-manager
BUILD_DIR := ./bin
CONFIG := opencode-manager.yaml

setup: build
	$(BUILD_DIR)/$(BINARY) setup

web:
	cd web && npx ng build --output-path ../internal/web/dist

build: web
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager

run: build
	$(BUILD_DIR)/$(BINARY) -config $(CONFIG)

dev:
	@echo "Building frontend..."
	@cd web && npx ng build --output-path ../internal/web/dist 2>&1 | tail -5
	@echo "Building and running..."
	@go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager && $(BUILD_DIR)/$(BINARY) -config $(CONFIG)

clean:
	rm -rf $(BUILD_DIR) internal/web/dist

tidy:
	go mod tidy
