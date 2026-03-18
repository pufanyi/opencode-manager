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
	@mkdir -p internal/web/dist/browser
	@[ -f internal/web/dist/browser/index.html ] || echo '<!doctype html><html><body>dev mode</body></html>' > internal/web/dist/browser/index.html
	go run ./cmd/opencode-manager -config $(CONFIG) -dev

lint:
	cd web && pnpm biome check src/
	golangci-lint run ./...

clean:
	rm -rf $(BUILD_DIR) internal/web/dist

tidy:
	go mod tidy
