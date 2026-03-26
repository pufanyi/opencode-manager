.PHONY: build run dev clean setup tidy dashboard web lint

BINARY := opencode-manager
BUILD_DIR := ./bin

setup: build
	$(BUILD_DIR)/$(BINARY) setup

dashboard:
	cd dashboard && pnpm run build -- --output-path ../internal/web/dist

web:
	cd web && pnpm ng build --output-path ../internal/web/dist

build: dashboard
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/opencode-manager

run: build
	$(BUILD_DIR)/$(BINARY)

dev:
	@mkdir -p internal/web/dist/browser
	@[ -f internal/web/dist/browser/index.html ] || echo '<!doctype html><html><body>dev mode</body></html>' > internal/web/dist/browser/index.html
	go run ./cmd/opencode-manager -dev

lint:
	cd web && pnpm biome check src/
	golangci-lint run ./...

clean:
	rm -rf $(BUILD_DIR) internal/web/dist

tidy:
	go mod tidy
