.PHONY: all setup vendor patch-vendor build build-all build-linux vet test \
       generate check-patches clean clean-vendor frontend frontend-embed \
       run dev

# Default: build Go binaries only (assumes frontend already built or not needed).
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
patch-vendor:
	@echo "==> Applying vendor patches..."
	@cd vendor && patch -p1 --forward --silent < ../patches/nebula-1031-graceful-shutdown.patch 2>/dev/null || true
	@find vendor -name '*.rej' -delete 2>/dev/null || true
	@echo "==> Done."

# Build Go binaries.
build:
	@test -d vendor || (echo "Run 'make setup' first." && exit 1)
	go build -mod=vendor -o hop-agent ./cmd/agent
	go build -mod=vendor -o hop-server ./cmd/server

# Build frontend (SvelteKit SPA).
frontend:
	cd frontend && npm ci && npm run build

# Copy frontend build into Go embed location.
frontend-embed: frontend
	rm -rf internal/frontend/dist
	mkdir -p internal/frontend/dist
	cp -r frontend/build/* internal/frontend/dist/

# Build everything: frontend + Go binaries.
build-all: frontend-embed build

# Build for a specific Linux platform.
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

# Build everything and run the server (frontend embedded in binary).
run: build-all
	./hop-server

# Development mode: run Go backend + SvelteKit dev server with hot reload.
# Backend on :9473, frontend on :5173 (proxies /api to backend).
dev:
	@echo "==> Starting backend + frontend dev servers..."
	@$(MAKE) build
	@./hop-server & echo $$! > .hop-server.pid
	@cd frontend && npm run dev &
	@echo ""
	@echo "  Backend:  http://localhost:9473"
	@echo "  Frontend: http://localhost:5173 (hot reload)"
	@echo "  Press Ctrl+C to stop."
	@echo ""
	@trap 'kill $$(cat .hop-server.pid 2>/dev/null) 2>/dev/null; rm -f .hop-server.pid; exit' INT TERM; wait

# Remove build artifacts.
clean:
	rm -f hop-agent hop-server
	rm -f hop-agent-linux-* hop-server-linux-*

# Remove vendor directory (re-run `make setup` to restore).
clean-vendor:
	rm -rf vendor/
