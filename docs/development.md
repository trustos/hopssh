# hopssh — Developer Guide

## Prerequisites

- Go 1.25+ (`go version`)
- `patch` command (pre-installed on macOS/Linux)
- `make` (pre-installed on macOS/Linux)
- Optional: `gh` CLI (for `make check-patches`)
- Optional: Docker (for containerized builds)

## First-Time Setup

```bash
git clone git@github.com:trustos/hopssh.git
cd hopssh
make setup
```

This runs `go mod vendor` and applies the Nebula vendor patch (see below).
You only need to do this once after cloning.

## Building

```bash
# Build both binaries (agent + server) for your platform:
make build

# Outputs:
#   ./hop-agent    — agent binary
#   ./hop-server   — control plane server

# Build for Linux (for deployment):
make build-linux                  # linux/amd64 (default)
make build-linux GOARCH=arm64     # linux/arm64
```

## Running Locally

```bash
# Terminal 1: Start the control plane
./hop-server --addr :8080 --data ./data --endpoint http://localhost:8080

# Terminal 2: Enroll an agent (device flow)
./hop-agent enroll --endpoint http://localhost:8080

# Terminal 2 (alternative): Enroll with a token
# Get a token from: POST http://localhost:8080/api/networks/{id}/nodes
echo "<token>" | ./hop-agent enroll --token-stdin --endpoint http://localhost:8080

# Terminal 2 (alternative): Run agent in serve mode (if already enrolled)
./hop-agent serve --token <test-token>
```

## Project Structure

```
cmd/
  agent/              Agent binary (serve + enroll subcommands)
  server/             Control plane server

internal/
  api/                HTTP handlers (auth, networks, nodes, proxy, device flow, bundles)
  auth/               Middleware (session auth, rate limiting)
  crypto/             AES-256-GCM encryption at rest
  db/                 SQLite stores + migrations
  mesh/               Nebula tunnel manager
  pki/                Nebula CA + cert generation

patches/              Vendor patches (applied via `make vendor`)
scripts/              Maintenance scripts
docs/                 Documentation
```

## Dependency Management

We use **vendored dependencies** with a local patch applied to Nebula.

### Regular workflow

```bash
# Add/update a dependency:
go get github.com/some/package@v1.2.3
make vendor    # re-vendors + re-applies patches

# Build (always use -mod=vendor or `make build`):
make build
```

### The Nebula Patch

We patch `vendor/github.com/slackhq/nebula/interface.go` to fix a critical bug
where `svc.Close()` calls `os.Exit(2)`, crashing the control plane. The patch
adds `io.ErrClosedPipe` to an error guard so the userspace TUN pipe can close
gracefully.

- **Patch file**: `patches/nebula-1031-graceful-shutdown.patch`
- **Upstream issue**: https://github.com/slackhq/nebula/issues/1031
- **Upstream fix PR**: https://github.com/slackhq/nebula/pull/1375 (not yet merged)

The patch is applied automatically by `make vendor`. To re-apply manually:

```bash
make patch-vendor
```

To check if the upstream fix has been merged (run monthly):

```bash
make check-patches   # requires gh CLI
```

When the upstream fix is released:
1. Update nebula version: `go get github.com/slackhq/nebula@<new-version>`
2. Re-vendor: `make vendor`
3. Verify `interface.go` includes the fix natively
4. If fixed: delete `patches/`, simplify Makefile
5. If not: patch will be re-applied automatically

## Docker

```bash
# Build the Docker image:
docker build -t hopssh .

# Run the control plane:
docker run -p 8080:8080 -v hopssh-data:/data hopssh server

# Run with custom flags:
docker run -p 8080:8080 -v hopssh-data:/data hopssh server \
  --addr :8080 --data /data --endpoint https://hopssh.com --trusted-proxy
```

## CI

GitHub Actions runs on every push and PR:
- `make setup` (vendor + patch)
- `make build` (compile both binaries)
- `make vet` (static analysis)
- `make test` (unit tests)
- `make build-linux` + `make build-linux GOARCH=arm64` (cross-compile)

See `.github/workflows/ci.yml`.

## Code Quality Rules

See the **Coding Principles** section in `CLAUDE.md` for the full list of rules
that all code in this project must follow. Key highlights:

- Never store secrets in plaintext — encrypt or hash
- Always `http.MaxBytesReader` on request bodies
- Never serialize `*db.Node` or `*db.User` — use DTOs
- Tokens must be single-use and time-bounded
- No shell interpolation of user input — use `exec.Command` directly
- Check `rows.Err()` after every database iteration
- Rate-limit all public endpoints
- Audit security-significant actions

## Swagger API Docs

The API is documented with Swagger annotations. To regenerate:

```bash
go install github.com/swaggo/swag/v2/cmd/swag@latest
swag init -g cmd/server/main.go -o docs --parseDependency
```

Access at: `http://localhost:8080/swagger/`
