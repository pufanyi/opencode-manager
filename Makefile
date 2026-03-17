.PHONY: build run dev clean setup tidy web lint

BINARY := opencode-manager
BUILD_DIR := ./bin
CONFIG := opencode-manager.yaml

setup: build
	$(BUILD_DIR)/$(BINARY) setup

web:
	cd web && pnpm ng build --output-path ../internal/web/dist

build: web
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager

run: build
	$(BUILD_DIR)/$(BINARY) -config $(CONFIG)

dev:
	@echo "Building frontend..."
	@cd web && pnpm ng build --output-path ../internal/web/dist 2>&1 | tail -5
	@echo "Building and running..."
	@go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager && $(BUILD_DIR)/$(BINARY) -config $(CONFIG)

lint:
	cd web && pnpm biome check src/
	golangci-lint run ./...

clean:
	rm -rf $(BUILD_DIR) internal/web/dist

tidy:
	go mod tidy
