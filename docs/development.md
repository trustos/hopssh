# hopssh — Developer Guide

## Prerequisites

- Go 1.24+ (`go version`)
- Node.js 20+ (`node --version`) — for frontend
- `make` (pre-installed on macOS/Linux)
- Optional: Docker (for containerized builds)
- Optional: `sqlc` (for regenerating query code: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`)

## First-Time Setup

```bash
git clone git@github.com:trustos/hopssh.git
cd hopssh
make setup    # downloads Go dependencies
```

## Building

```bash
# Go binaries only (fast, for backend development):
make build

# Everything — frontend + Go binaries (for production / full testing):
make build-all

# Cross-compile for Linux:
make build-linux                  # linux/amd64
make build-linux GOARCH=arm64     # linux/arm64

# Outputs:
#   ./hop-agent    — agent + client binary
#   ./hop-server   — control plane (API + web UI + lighthouse + relay + DNS)
```

## Running Locally

```bash
# Terminal 1: Start the control plane
./hop-server

# Server starts on :9473 with data in ./data/
# Dashboard at http://localhost:9473
# API at http://localhost:9473/api/*

# Terminal 2: Frontend dev with hot reload (optional)
cd frontend && npm run dev
# Dev server at http://localhost:5173, proxies /api/* to :9473
```

### Testing the mesh locally

For local mesh testing, you need the control plane to have a reachable IP.
Using `localhost` works for the API but not for Nebula mesh connections.

```bash
# Start with your local IP as the endpoint:
./hop-server --endpoint http://192.168.1.100:9473

# Enroll an agent (on the same machine for testing):
echo '<token>' | sudo ./hop-agent enroll --token-stdin --endpoint http://192.168.1.100:9473

# Join as a client:
./hop-agent client join --network <id> --endpoint http://192.168.1.100:9473
```

## Project Structure

```
cmd/
  agent/                Agent + client binary
    main.go             Subcommand dispatch: serve, enroll, client
    nebula.go           meshService interface (userspace + kernel TUN)
    enroll.go           Agent enrollment (device flow, token, bundle)
    renew.go            Certificate auto-renewal goroutine
    dns.go              DNS split-tunnel configuration (shared)
    dns_darwin.go       macOS DNS: /etc/resolver/<domain>
    dns_linux.go        Linux DNS: systemd-resolved / fallback
    client.go           Client join mode (planned)
  server/
    main.go             Control plane entry point

internal/
  api/                  HTTP handlers
    router.go           Route definitions + middleware wiring
    auth.go             Register, login, logout, me
    networks.go         Network CRUD (creates lighthouse instances)
    enroll.go           Node enrollment + token management
    device.go           Device authorization flow (RFC 8628)
    bundles.go          Pre-bundled tarball generation
    proxy.go            Agent proxy: health, shell, exec, port forwards, node delete
    renew.go            Certificate renewal endpoint
    dns.go              DNS record management (planned)
    peers.go            Peer connectivity status (planned)
    types.go            Request/response DTOs + helpers
  auth/
    middleware.go        Session auth middleware
    ratelimit.go         Per-IP rate limiter (token bucket)
  authz/
    authz.go            Authorization checks (CanAccessNetwork — future: teams)
  crypto/
    crypto.go           AES-256-GCM encrypt/decrypt
  db/
    db.go               SQLite connection pools + migration runner
    resilience.go        ResilientDB wrapper (lock retry with backoff)
    users.go            UserStore
    sessions.go          SessionStore (SHA-256 hashed tokens)
    networks.go          NetworkStore (encrypted CA/server keys)
    nodes.go             NodeStore (encrypted tokens, hashed enrollment tokens, atomic claims)
    device_codes.go      DeviceCodeStore (hashed codes, collision retry)
    bundles.go           BundleStore (hashed download tokens, single-use)
    audit.go             AuditStore
    dns_records.go       DNSRecordStore (planned)
    queries/             SQL query files (sqlc source)
    dbsqlc/              Generated Go code (sqlc output — do not edit)
    migrations/          SQL migration files
  frontend/
    embed.go            Embeds built frontend SPA into Go binary
  mesh/
    network_manager.go   Persistent per-network Nebula instances (planned)
    mesh.go              Legacy ephemeral tunnel manager (being replaced)
    forward.go           TCP port forwarding (half-close)
    dns.go               Mesh DNS server (planned)
  pki/
    pki.go              Nebula CA generation + cert issuance
    subnet.go            Subnet/IP allocation helpers

frontend/               Svelte 5 SPA
  src/
    routes/             SvelteKit pages (login, dashboard, network, terminal, device)
    lib/
      api/client.ts     Typed API client (all endpoints)
      stores/            Auth + theme stores (Svelte 5 runes)
      terminal/shell.ts  xterm.js + WebSocket helper
      types/api.ts       TypeScript interfaces matching Go DTOs
      components/        App sidebar, shadcn-svelte components
  svelte.config.js      adapter-static (SPA), runes mode
  vite.config.ts        Dev proxy /api → :9473 (ws: true)
```

## Dependency Management

Dependencies are managed via Go modules (no vendoring).

```bash
# Add/update a dependency:
go get github.com/some/package@v1.2.3
go mod tidy

# Regenerate sqlc code after modifying .sql files:
make generate
```

### Nebula Fork

hopssh uses a fork of Nebula at [github.com/trustos/nebula](https://github.com/trustos/nebula)
(branch: `hopssh`) for performance enhancements and feature additions. The fork
is consumed via a `replace` directive in `go.mod` — no import rewrites needed:

```go
replace github.com/slackhq/nebula => github.com/trustos/nebula v1.10.3-hopssh.1
```

**Changes in the fork** (6 commits on the `hopssh` branch):

| Change | Description |
|--------|-------------|
| Graceful shutdown | Fix `os.Exit(2)` on service close (upstream PR #1375) |
| TUN buffer reuse | Eliminate per-packet allocation + write mutex (macOS) |
| UDP multi-reader | Socket buffer support + `SupportsMultipleReaders()` (macOS) |
| Decoupled routines | Separate TUN/UDP routine counts for macOS SO_REUSEPORT |
| Packet coalescing | Batch UDP sends with length-prefix framing + Linux panic fix |
| PMTUD support | `SetMTU` interface, `SendTestRequest`, `TestReply` callback |

See the fork's [HOPSSH.md](https://github.com/trustos/nebula/blob/hopssh/HOPSSH.md) for
the full maintenance guide, including how to add changes and upgrade upstream.

#### Modifying the Fork

```bash
git clone git@github.com:trustos/nebula.git
cd nebula && git checkout hopssh
# Make changes, commit, tag v1.10.3-hopssh.N, push
# Then in hopssh: update go.mod replace version, go mod tidy
```

#### Upgrading Upstream Nebula

```bash
cd nebula-fork
git fetch upstream
git rebase upstream/v1.11.0
git tag v1.11.0-hopssh.1
git push origin hopssh --tags --force-with-lease
# In hopssh: update replace directive, go mod tidy
```

#### Performance Profiling

```bash
# CPU profile during load (30 seconds)
curl -H "Authorization: Bearer <token>" \
  "http://<mesh-ip>:41820/debug/pprof/profile?seconds=30" > cpu.prof
go tool pprof cpu.prof

# Heap snapshot
curl -H "Authorization: Bearer <token>" \
  "http://<mesh-ip>:41820/debug/pprof/heap" > heap.prof
```

### sqlc

SQL queries live in `internal/db/queries/*.sql`. After editing, regenerate:

```bash
make generate    # runs sqlc
```

Generated code goes to `internal/db/dbsqlc/`. Never edit generated files.

## Docker

```bash
docker build -t hopssh .
docker run -p 9473:9473 -p 42001-42100:42001-42100/udp -v hopssh-data:/data hopssh
```

The Dockerfile has three stages:
1. **Node.js** — builds the Svelte frontend
2. **Go** — downloads deps via module cache, copies frontend, builds static binaries
3. **Debian slim** — minimal runtime with ca-certificates

## CI

GitHub Actions (`.github/workflows/ci.yml`) runs on every push/PR:
- `go mod download` (fetch dependencies)
- `make build` (compile)
- `make vet` (static analysis)
- `make test` (unit tests)
- Cross-compile for linux/amd64 + linux/arm64 with artifact upload

## Code Quality Rules

See the **Coding Principles** section in `CLAUDE.md` for the complete list.
Key highlights:

- Never store secrets in plaintext — encrypt (AES-GCM) or hash (SHA-256)
- Always `http.MaxBytesReader` on request bodies
- Never serialize `*db.Node` or `*db.User` — use DTOs
- Tokens must be single-use, time-bounded, consumed atomically
- No shell interpolation of user input — use `exec.Command` directly
- Check `rows.Err()` after every database iteration
- Authorization through `authz.CanAccessNetwork()` — never inline checks
- All queries through `ResilientDB` wrapper (lock retry)
- SQL in `.sql` files (sqlc), not in Go code
- Rate-limit all public endpoints
- Audit security-significant actions

## Swagger API Docs

```bash
go install github.com/swaggo/swag/v2/cmd/swag@latest
swag init -g cmd/server/main.go -o docs --parseDependency
```

Access at: `http://localhost:9473/swagger/`
