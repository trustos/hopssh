.PHONY: all setup vendor patch-vendor build vet test check-patches clean generate

# Default: build everything.
all: build

# First-time setup: vendor dependencies and apply patches.
setup: vendor
	@echo ""
	@echo "==> Setup complete. Run 'make build' to compile."

# Vendor dependencies and apply patches.
vendor:
	go mod tidy
	go mod vendor
	$(MAKE) patch-vendor

# Apply patches to vendored dependencies.
# Run this after `go mod vendor` to re-apply local fixes.
patch-vendor:
	@echo "==> Applying vendor patches..."
	@cd vendor && patch -p1 --forward --silent < ../patches/nebula-1031-graceful-shutdown.patch 2>/dev/null || true
	@find vendor -name '*.rej' -delete 2>/dev/null || true
	@echo "==> Done."

# Build all binaries.
build:
	@test -d vendor || (echo "Run 'make setup' first." && exit 1)
	go build -mod=vendor -o hop-agent ./cmd/agent
	go build -mod=vendor -o hop-server ./cmd/server

# Build for a specific platform.
# Usage: make build-linux GOARCH=amd64
build-linux:
	@test -d vendor || (echo "Run 'make setup' first." && exit 1)
	GOOS=linux GOARCH=$(or $(GOARCH),amd64) go build -mod=vendor -trimpath -ldflags='-s -w' -o hop-agent-linux-$(or $(GOARCH),amd64) ./cmd/agent
	GOOS=linux GOARCH=$(or $(GOARCH),amd64) go build -mod=vendor -trimpath -ldflags='-s -w' -o hop-server-linux-$(or $(GOARCH),amd64) ./cmd/server

# Run go vet.
vet:
	go vet -mod=vendor ./...

# Regenerate sqlc code from .sql query files.
generate:
	$(GOPATH)/bin/sqlc generate || sqlc generate

# Run tests.
test:
	go test -mod=vendor ./...

# Check if vendor patches are still needed (requires gh CLI).
check-patches:
	@./scripts/check-nebula-patch.sh

# Remove build artifacts.
clean:
	rm -f hop-agent hop-server
	rm -f hop-agent-linux-* hop-server-linux-*

# Remove vendor directory (re-run `make setup` to restore).
clean-vendor:
	rm -rf vendor/
