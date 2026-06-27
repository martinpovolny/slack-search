BINARY     := slack-search
GO_PKG     := ./cmd/slack-search
UI_DIR     := ./ui
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -ldflags "-X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

.PHONY: all build ui-build run dev clean test

## Build everything: frontend then Go binary
all: ui-build build

## Build the Go binary (requires ui-build to have been run)
build:
	go build $(LDFLAGS) -o bin/$(BINARY) $(GO_PKG)

## Build the Vite frontend
ui-build:
	cd $(UI_DIR) && npm ci && npm run build

## Run the compiled binary
run: all
	./bin/$(BINARY)

## Development mode: start Go backend + Vite dev server concurrently
dev:
	@echo "Starting Go backend on :8088 and Vite dev server on :5173"
	go run $(GO_PKG) & \
	cd $(UI_DIR) && npm run dev

## Run Go tests
test:
	go test ./...

## Cross-compile for Linux ARM64
build-linux-arm64: ui-build
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 $(GO_PKG)

## Remove build artifacts
clean:
	rm -rf bin/ $(UI_DIR)/dist
