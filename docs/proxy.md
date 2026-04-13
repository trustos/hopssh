# Browser Proxy ("Open in Browser")

Access any web service running on a mesh node directly from the hopssh dashboard.
No port forwarding, no VPN client, no firewall rules. Just click "Open in Browser".

---

## How it Works

```
Browser                    Control Plane                 Agent (node)
  |                            |                            |
  |  GET /proxy/{net}/{node}/{port}                         |
  |--------------------------->|                            |
  |  200 (SPA proxy page)     |                            |
  |<--------------------------|                            |
  |                            |                            |
  |  <iframe src="/api/networks/{net}/nodes/{node}/proxy/{port}/">
  |--------------------------->|                            |
  |                            |  requireNode (auth+authz)  |
  |                            |  GET /proxy/{port}/         |
  |                            |----------[mesh]----------->|
  |                            |                            | localhost:{port}
  |                            |<----------[mesh]-----------|
  |  200 (HTML + injected SW bootstrap)                     |
  |<--------------------------|                            |
  |                            |                            |
  |  [SW + bootstrap rewrite URLs, force credentials]       |
  |  GET /api/.../proxy/{port}/v1/jobs                      |
  |--------------------------->|----------[mesh]----------->|
  |  200 (JSON from proxied app)                            |
  |<--------------------------|<----------[mesh]-----------|
```

### Request Flow

1. **Dashboard** loads the proxy page at `/proxy/{networkID}/{nodeID}/{port}`
2. **Proxy page** renders an iframe pointing to `/api/networks/{net}/nodes/{node}/proxy/{port}/`
3. **Control plane** authenticates the user (session cookie), authorizes access (owner/member),
   and reverse-proxies the request through the Nebula mesh to the agent
4. **Agent** strips the `/proxy/{port}` prefix and forwards to `localhost:{port}`
5. **HTML responses** are intercepted: the control plane injects a `<script>` tag loading
   `sw-bootstrap.js` and rewrites asset paths to include the proxy prefix
6. **Subsequent requests** (API calls, assets) are rewritten by the service worker and
   bootstrap script to include the proxy prefix

### Authentication Chain

```
Browser → [session cookie] → Control Plane → [bearer token] → Agent → localhost
```

- **Browser → Control Plane**: Session cookie (`HttpOnly`, `Secure`, `SameSite=Lax`)
- **Control Plane → Agent**: Bearer token (per-node, AES-256-GCM encrypted at rest)
- **Agent → localhost**: No auth (agent strips the Authorization header)

---

## Service Worker Architecture

The proxy runs on the same origin as the hopssh dashboard (`hopssh.com`). Since we can't
give each proxied service its own subdomain, we use path-based proxying with URL rewriting.

### The Problem

A proxied app (e.g., Nomad at port 4646) expects to be at `/ui/`. But through the proxy,
it's actually at `/api/networks/.../proxy/4646/ui/`. The app's JavaScript makes API calls
to `/v1/jobs` which should go to `/api/networks/.../proxy/4646/v1/jobs`.

### The Solution: Three Layers of URL Rewriting

**Layer 1: Server-side HTML rewriting** (`proxy.go` ModifyResponse)
- Rewrites `src="/..."` and `href="/..."` attributes in HTML responses
- Ensures first-load assets (CSS, JS) load correctly before the SW is active

**Layer 2: Service Worker** (`sw.js`)
- Intercepts all fetch requests from proxy iframe tabs
- Prepends the proxy prefix to same-origin paths
- Forces `credentials: 'include'` on all proxied requests
- Maps client IDs to proxy bases, persisted via Cache API

**Layer 3: Bootstrap script** (`sw-bootstrap.js`)
- Injected into every HTML response via `<script src="/sw-bootstrap.js?base=...">`
- Patches `window.fetch`, `XMLHttpRequest.prototype.open`, and `WebSocket` constructor
- Rewrites URLs *before* they reach the SW (belt-and-suspenders)
- Forces `credentials: 'include'` on rewritten fetch requests
- Forces `withCredentials = true` on rewritten XHR requests
- On first visit (no SW active): calls `window.stop()`, registers SW, reloads
- Strips proxy prefix from `location.pathname` via `replaceState` so the app's router works

### Why Both SW and Bootstrap?

The SW can restart at any time (browser kills idle workers). When it restarts, it loses
in-memory client mappings. The Cache API persistence helps, but there's a race window.
The bootstrap patches are synchronous and guaranteed, providing the primary rewriting.
The SW provides defense-in-depth for requests the bootstrap can't intercept (dynamic
imports, `<link>` preloads, CSS `url()` references).

### Credential Handling

Proxied apps (e.g., Nomad's Ember adapter) often use `credentials: 'omit'` on their
fetch/XHR calls. But the hopssh proxy requires the session cookie for authentication.

Both the SW and bootstrap force `credentials: 'include'` on rewritten requests:
- **fetch**: `init = Object.assign({}, init, { credentials: 'include' })`
- **XHR**: `this.withCredentials = true` (after `open()`)
- **SW**: `credentials: 'include'` in the Request init

---

## Proxy Auth Cache

### Problem

`requireNode()` hits SQLite 3 times per proxy request (network, membership, node lookups).
Proxied apps like Nomad fire 5-10 long-polling requests per page load, each retrying every
few seconds. At scale, this causes `SQLITE_BUSY` contention with heartbeat writes, resulting
in intermittent 404 errors.

### Solution

`ProxyHandler` maintains an in-memory `sync.Map` cache keyed by `networkID:nodeID:userID`.
On cache hit (within 2-minute TTL), the proxy serves directly from memory without touching
SQLite.

```go
const proxyAuthTTL = 2 * time.Minute

// Cache key: "networkID:nodeID:userID"
// Cache value: *proxyAuthEntry{network, node, expires}
```

### Cache Invalidation

The cache is invalidated immediately on:
- **Node delete** (`ProxyHandler.DeleteNode`)
- **Capability update** (`ProxyHandler.UpdateCapabilities`)
- **Network delete** (`NetworkHandler.DeleteNetwork`)
- **Member removal** (`MemberHandler.RemoveMember`)

If `nodeID` is provided, only entries for that specific node are cleared.
If `nodeID` is empty, all entries for the network are cleared (e.g., on network delete).

### Worst Case

A capability toggle takes up to 2 minutes to take effect for users with cached entries.
Access revocation (member removal) also has a 2-minute window. Node and network deletion
invalidate immediately.

---

## Long-Polling and Timeouts

### Nomad Blocking Queries

Nomad uses HTTP blocking queries (`?index=N`) that hold connections open for up to 5 minutes
waiting for data changes. These are normal and expected.

### Handling

- **No write timeout** on the proxy route (registered with `r.HandleFunc`, not `wt` middleware)
- **context.Canceled** errors are silently suppressed in the reverse proxy ErrorHandler
  (client disconnected during long-poll, not an error)
- **Proxy timeout**: The SW has a 30-second timeout for individual requests; the reverse
  proxy has no explicit timeout (relies on context cancellation)

---

## Port Forward vs Browser Proxy

| Aspect | Port Forward (TCP) | Browser Proxy |
|--------|-------------------|---------------|
| Access | `localhost:{port}` | Dashboard iframe |
| Protocol | Raw TCP | HTTP/WebSocket |
| Auth | Per-forward, on creation only | Per-request, session cookie |
| Persistence | Runs until stopped (10min idle timeout) | Per-request, stateless |
| Use case | Database connections, SSH, custom protocols | Web UIs (Nomad, Grafana, etc.) |

---

## Files

| File | Purpose |
|------|---------|
| `internal/api/proxy.go` | Control plane proxy handler (auth, cache, reverse proxy, HTML injection) |
| `cmd/agent/proxy.go` | Agent-side localhost proxy (strips prefix, forwards to service) |
| `frontend/static/sw.js` | Service worker (URL rewriting, credential injection, Cache API persistence) |
| `frontend/static/sw-bootstrap.js` | Bootstrap script (fetch/XHR/WebSocket patching, SW registration) |
| `frontend/src/routes/proxy/[networkID]/[nodeID]/[port]/+page.svelte` | Proxy page (iframe wrapper, pre-check) |
