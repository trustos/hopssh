# Load .env if it exists
-include .env
export

.PHONY: all setup vendor patch-vendor build build-all build-linux vet test \
       generate clean clean-vendor frontend frontend-embed \
       run dev release wifi-snapshot

# Version injection via ldflags.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  = -s -w -X github.com/trustos/hopssh/internal/buildinfo.Version=$(VERSION) -X github.com/trustos/hopssh/internal/buildinfo.Commit=$(COMMIT)

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
	@for p in patches/*.patch; do \
		cd vendor && patch -p1 --forward --silent < "../$$p" 2>/dev/null || true; cd ..; \
	done
	@find vendor -name '*.rej' -delete 2>/dev/null || true
	@echo "==> Done."

# Build Go binaries.
build:
	@test -d vendor || (echo "Run 'make setup' first." && exit 1)
	go build -mod=vendor -ldflags='$(LDFLAGS)' -o hop-agent ./cmd/agent
	go build -mod=vendor -ldflags='$(LDFLAGS)' -o hop-server ./cmd/server

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
	GOOS=linux GOARCH=$(or $(GOARCH),amd64) go build -mod=vendor -trimpath -ldflags='$(LDFLAGS)' -o hop-agent-linux-$(or $(GOARCH),amd64) ./cmd/agent
	GOOS=linux GOARCH=$(or $(GOARCH),amd64) go build -mod=vendor -trimpath -ldflags='$(LDFLAGS)' -o hop-server-linux-$(or $(GOARCH),amd64) ./cmd/server

# Deploy agent to both local Macs (build + copy + restart).
dev-deploy:
	@./scripts/dev-deploy.sh

# Deploy control plane to remote server (cross-compile + scp + restart).
# Requires DEPLOY_HOST, DEPLOY_USER, DEPLOY_KEY in .env
dev-deploy-server:
	@./scripts/dev-deploy-server.sh

# Run go vet.
vet:
	go vet -mod=vendor ./...

# Regenerate sqlc code from .sql query files.
generate:
	$(GOPATH)/bin/sqlc generate || sqlc generate

# Run tests.
test:
	go test -mod=vendor ./...

# Build everything and run the server (frontend embedded in binary).
run: build-all
	./hop-server

# Development mode: run Go backend + SvelteKit dev server with hot reload.
dev:
	@./scripts/dev.sh

SSH_CMD = ssh -o StrictHostKeyChecking=no -i $(DEPLOY_KEY) $(or $(DEPLOY_USER),ubuntu)@$(DEPLOY_HOST)

# SSH into the remote control plane server.
ssh:
	@test -n "$(DEPLOY_HOST)" || (echo "Set DEPLOY_HOST in .env" && exit 1)
	$(SSH_CMD)

# Deploy hopssh to a remote server.
deploy:
	@test -n "$(DEPLOY_HOST)" || (echo "Set DEPLOY_HOST in .env" && exit 1)
	cat deploy/install.sh | $(SSH_CMD) 'sudo bash -s'

# Run a command on the remote server.
remote-exec:
	@test -n "$(DEPLOY_HOST)" || (echo "Set DEPLOY_HOST in .env" && exit 1)
	$(SSH_CMD) '$(CMD)'

# Create a new release.
BUMP ?= patch
release:
	@LATEST=$$(git tag --sort=-v:refname | grep '^v' | head -1); \
	if [ -z "$$LATEST" ]; then \
		NEXT="v0.1.0"; \
	else \
		MAJOR=$$(echo $$LATEST | sed 's/^v//' | cut -d. -f1); \
		MINOR=$$(echo $$LATEST | sed 's/^v//' | cut -d. -f2); \
		PATCH=$$(echo $$LATEST | sed 's/^v//' | cut -d. -f3); \
		case "$(BUMP)" in \
			major) MAJOR=$$((MAJOR+1)); MINOR=0; PATCH=0 ;; \
			minor) MINOR=$$((MINOR+1)); PATCH=0 ;; \
			patch) PATCH=$$((PATCH+1)) ;; \
			*) echo "Invalid BUMP=$(BUMP). Use: patch, minor, major" && exit 1 ;; \
		esac; \
		NEXT="v$$MAJOR.$$MINOR.$$PATCH"; \
	fi; \
	echo "==> Releasing $$NEXT (previous: $${LATEST:-none})"; \
	git tag "$$NEXT" && git push origin "$$NEXT"

# Remove build artifacts.
clean:
	rm -f hop-agent hop-server
	rm -f hop-agent-linux-* hop-server-linux-*

# Remove vendor directory (re-run `make setup` to restore).
clean-vendor:
	rm -rf vendor/

# Capture a path-state snapshot into the running wifi_comparison.sh
# results dir. Use during a perceived "buggy" window so the spike can
# be correlated against pings.tsv afterwards.
wifi-snapshot:
	@LATEST=$$(ls -td results/wifi-comparison-* 2>/dev/null | head -1); \
	if [ -z "$$LATEST" ]; then \
	    echo "no results/wifi-comparison-* dir found — start with scripts/wifi_comparison.sh first"; \
	    exit 1; \
	fi; \
	STAMP=$$(date +%H%M%S); \
	SNAP="$$LATEST/snapshot-$$STAMP.txt"; \
	{ \
	    echo "=== ts=$$(date +%s) wall=$$(date) ==="; \
	    echo "## last 60s ping summary per VPN"; \
	    awk -F'\t' -v cutoff=$$(($$(date +%s)-60)) 'NR>1 && $$1>cutoff {c[$$2]++; if($$3!="LOSS"){s[$$2]++; n[$$2]+=$$3; if($$3>m[$$2])m[$$2]=$$3}} END{for(k in c) printf "  %-12s n=%d ok=%d max=%.1fms mean=%.1fms\n", k, c[k], s[k], m[k], s[k]?n[k]/s[k]:0}' "$$LATEST/pings.tsv"; \
	    echo; echo "## tailscale status"; tailscale status 2>&1 | head -5; \
	    echo; echo "## ZeroTier MBP peer"; sudo zerotier-cli peers 2>&1 | grep "5a0c0c6ebd" || echo "no MBP peer"; \
	    echo; echo "## WiFi (wdutil)"; sudo wdutil info 2>&1 | sed -n '/^WIFI/,/^[A-Z][A-Z]/p' | head -40; \
	} > "$$SNAP" 2>&1; \
	echo "snapshot saved to: $$SNAP"
