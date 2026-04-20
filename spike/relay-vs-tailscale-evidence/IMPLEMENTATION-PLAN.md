# Implementation Plan — Port Mapping for Nebula

Supersedes the high-level `PORT-MAPPING-DESIGN.md`. Read `NAT-DIAGNOSIS.md` first for why this exists.

## Goal in one sentence

When a hopssh agent starts, it should ask its local home router (via NAT-PMP, PCP, or UPnP-IGD — whichever works) to forward a public UDP port to Nebula's listen port, then advertise that public `IP:port` to the lighthouse so peers behind CGNAT can reach us directly instead of via relay.

## Non-goals for v1

- IPv6 port mapping (PCP supports it; we'll add later).
- Dynamic port change if the router rejects our suggested external port (we'll just fail and stick with hole-punching).
- Automatic router-vendor detection / bug workarounds beyond "try the next protocol on failure".
- Dashboard surface. Operator-facing only via logs + `hop-agent info`.

## Package layout

```
internal/portmap/
├── portmap.go       Coordinator: Try(ctx) → picks winning protocol; refresh loop
├── natpmp.go        NAT-PMP client (RFC 6886), ~150 lines
├── pcp.go           PCP client (RFC 6887), ~180 lines — shares UDP socket with natpmp
├── upnp.go          UPnP-IGD coordinator: SSDP discovery → SOAP calls
├── upnp_ssdp.go     SSDP M-SEARCH + reply parsing
├── upnp_soap.go     SOAP envelope build + response parsing
├── gateway.go       Default gateway IP discovery (macOS/Linux/Windows)
├── portmap_test.go  Unit tests with mock servers
└── doc.go           Package-level doc comment

patches/
└── 11-portmap.patch  Vendor patch: LightHouse.AddAdvertiseAddr + injection in main.go

internal/nebulacfg/
└── defaults.go       Add PortmapEnabled const, portmap knob helper
```

Interface shared by the three protocol clients:

```go
// internal/portmap/portmap.go
type Client interface {
    // Map requests a port mapping. Returns the public endpoint and the
    // time until we should refresh. A returned nil error with zero ttl
    // means "got a mapping but can't infer ttl — re-probe at a safe default."
    Map(ctx context.Context, internalPort uint16) (public netip.AddrPort, ttl time.Duration, err error)

    // Unmap removes the mapping. Best-effort; errors logged not propagated.
    Unmap(ctx context.Context, internalPort uint16) error

    // Name — "natpmp" | "pcp" | "upnp". Used in logs + metrics.
    Name() string
}
```

Why one interface: the coordinator runs them in parallel and keeps the one that responded first. On `Unmap` and `Refresh`, we only touch the winner (avoids churning the router's mapping table).

## Protocol 1 — NAT-PMP (RFC 6886)

**Transport:** UDP to `<gateway>:5351`.

### Wire format — all integers network byte order (big-endian)

External-address request (2 bytes):
```
[0x00] [0x00]            version=0, opcode=0 (public-address)
```

External-address response (12 bytes):
```
[0x00] [0x80] [RESULT_CODE:2] [EPOCH:4] [PUB_IPv4:4]
```

UDP MAP request (12 bytes):
```
[0x00] [0x01] [0x00 0x00] [INTERNAL_PORT:2] [SUGGESTED_EXT_PORT:2] [LIFETIME:4]
```
- `SUGGESTED_EXT_PORT = 0` means "any", but we pass Nebula's listen port so the mapping is deterministic.
- `LIFETIME = 7200` (2 h) per RFC default.

UDP MAP response (16 bytes):
```
[0x00] [0x81] [RESULT_CODE:2] [EPOCH:4] [INTERNAL_PORT:2] [ASSIGNED_EXT_PORT:2] [GRANTED_LIFETIME:4]
```

### Result codes

| Code | Meaning | Action |
|---|---|---|
| 0 | Success | — |
| 1 | Unsupported version | Fatal — server only speaks PCP v2 or newer |
| 2 | Not authorized | Fatal |
| 3 | Network failure | Retry with backoff |
| 4 | Out of resources | Retry after delay |
| 5 | Unsupported opcode | Fatal |

### Timeout/retry

Per RFC §3.1: initial 250 ms, then double (500 ms, 1 s, 2 s, … up to 9 attempts ≈ 64 s). If no response after the final retry, treat the server as absent.

### Go skeleton

```go
// internal/portmap/natpmp.go
package portmap

import (
    "context"
    "encoding/binary"
    "errors"
    "fmt"
    "net"
    "net/netip"
    "time"
)

type natPMP struct {
    gateway netip.Addr
}

const (
    natpmpPort     = 5351
    natpmpLifetime = uint32(7200) // 2 hours
)

func (c *natPMP) Name() string { return "natpmp" }

func (c *natPMP) Map(ctx context.Context, internalPort uint16) (netip.AddrPort, time.Duration, error) {
    // 1. Query external address to confirm NAT-PMP is present.
    pub, err := c.queryExternalAddr(ctx)
    if err != nil {
        return netip.AddrPort{}, 0, err
    }
    // 2. Request UDP port mapping.
    assigned, ttl, err := c.requestMap(ctx, internalPort)
    if err != nil {
        return netip.AddrPort{}, 0, err
    }
    return netip.AddrPortFrom(pub, assigned), ttl, nil
}

func (c *natPMP) queryExternalAddr(ctx context.Context) (netip.Addr, error) {
    resp, err := c.roundTrip(ctx, []byte{0x00, 0x00}, 12)
    if err != nil {
        return netip.Addr{}, err
    }
    if resp[0] != 0x00 || resp[1] != 0x80 {
        return netip.Addr{}, fmt.Errorf("natpmp: unexpected header %x%x", resp[0], resp[1])
    }
    if rc := binary.BigEndian.Uint16(resp[2:4]); rc != 0 {
        return netip.Addr{}, fmt.Errorf("natpmp: result=%d", rc)
    }
    var ip4 [4]byte
    copy(ip4[:], resp[8:12])
    return netip.AddrFrom4(ip4), nil
}

func (c *natPMP) requestMap(ctx context.Context, internalPort uint16) (ext uint16, ttl time.Duration, err error) {
    req := make([]byte, 12)
    req[0] = 0x00 // version
    req[1] = 0x01 // UDP MAP opcode
    // bytes 2-3: reserved (zero)
    binary.BigEndian.PutUint16(req[4:6], internalPort)
    binary.BigEndian.PutUint16(req[6:8], internalPort) // suggest same external port
    binary.BigEndian.PutUint32(req[8:12], natpmpLifetime)

    resp, err := c.roundTrip(ctx, req, 16)
    if err != nil {
        return 0, 0, err
    }
    if resp[0] != 0x00 || resp[1] != 0x81 {
        return 0, 0, fmt.Errorf("natpmp: unexpected map response header")
    }
    if rc := binary.BigEndian.Uint16(resp[2:4]); rc != 0 {
        return 0, 0, fmt.Errorf("natpmp: map result=%d", rc)
    }
    if got := binary.BigEndian.Uint16(resp[8:10]); got != internalPort {
        return 0, 0, fmt.Errorf("natpmp: echoed internal port %d ≠ %d", got, internalPort)
    }
    ext = binary.BigEndian.Uint16(resp[10:12])
    granted := binary.BigEndian.Uint32(resp[12:16])
    return ext, time.Duration(granted) * time.Second, nil
}

// roundTrip sends the request and reads one response, implementing the
// RFC §3.1 exponential backoff (250 ms → 500 ms → 1 s → ...).
func (c *natPMP) roundTrip(ctx context.Context, req []byte, respLen int) ([]byte, error) {
    conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
        IP:   c.gateway.AsSlice(),
        Port: natpmpPort,
    })
    if err != nil {
        return nil, err
    }
    defer conn.Close()

    backoff := 250 * time.Millisecond
    resp := make([]byte, respLen)
    for i := 0; i < 9; i++ {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        if _, err := conn.Write(req); err != nil {
            return nil, err
        }
        deadline := time.Now().Add(backoff)
        if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
            deadline = d
        }
        conn.SetReadDeadline(deadline)
        n, _, err := conn.ReadFrom(resp)
        if err == nil && n >= respLen {
            return resp[:n], nil
        }
        backoff *= 2
    }
    return nil, errors.New("natpmp: no response after 9 attempts")
}
```

### Unit test plan

Run a mock NAT-PMP server (loopback UDP goroutine) that accepts the two opcodes and returns canned responses. Test cases: success, result-code != 0, truncated response, gateway unreachable (port closed).

## Protocol 2 — PCP (RFC 6887)

Reuses the UDP socket pattern. Main differences:

### Common header (24 bytes)

```
[VER:1] [R+OP:1] [RESERVED:1] [RESERVED:1] [LIFETIME:4] [CLIENT_IP:16]
```
- `VER = 2`. R-bit (top bit of byte 1) = 0 for request. Opcode = 1 for MAP.
- `CLIENT_IP` is the client's IPv4-mapped IPv6 address (first 10 bytes 0x00, next 2 bytes 0xff 0xff, last 4 bytes IPv4).

### MAP opcode payload (36 bytes → total 60 bytes)

```
[NONCE:12] [PROTO:1] [RESERVED:3] [INTERNAL_PORT:2] [SUGGESTED_EXT_PORT:2] [SUGGESTED_EXT_IP:16]
```
- `PROTO = 17` for UDP.
- `NONCE` is 12 random bytes from `crypto/rand`. Must be echoed in the response. Used to match retries.

### Version negotiation with NAT-PMP-only servers

RFC 6887 §9: if we send PCP v=2 and the gateway is NAT-PMP-only, it will reply with version=0 and result=1 (UNSUPP_VERSION). We treat that as "fall back to NAT-PMP on this gateway" — don't even try PCP again.

Conversely, if we send NAT-PMP v=0 and the server is PCP v=2 only, it replies with version=2 result=1. Client then upgrades to PCP.

The coordinator probes both in parallel anyway, so negotiation isn't strictly required — the one that works returns first.

### Skeleton — reuse

The PCP client shares `natPMP.roundTrip`'s socket-open-send-read-retry loop. Different wire format, same state machine. Estimated ~180 lines.

## Protocol 3 — UPnP-IGD

Two phases, both stdlib-only.

### Phase A — SSDP discovery (UDP multicast)

Send to `239.255.255.250:1900` (well-known SSDP multicast address):

```
M-SEARCH * HTTP/1.1
HOST: 239.255.255.250:1900
MAN: "ssdp:discover"
MX: 2
ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1

```

(Also repeat with `:2` in the ST header to find newer IGDv2 devices.)

Listen for replies on the same socket for 2 s. Each reply is an HTTP-response-shaped UDP packet:

```
HTTP/1.1 200 OK
CACHE-CONTROL: max-age=120
LOCATION: http://192.168.23.1:1900/onqkt/rootDesc.xml
ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1
USN: uuid:...::urn:schemas-upnp-org:device:InternetGatewayDevice:1

```

Extract `LOCATION:` — that's the URL of the device description XML.

### Phase B — SOAP port mapping

1. HTTP GET the `LOCATION` URL. Parse the XML to find a `<service>` whose `<serviceType>` is `urn:schemas-upnp-org:service:WANIPConnection:1` (fallback: `:2`, then `WANPPPConnection:1`). Extract `<controlURL>` (relative to the XML's base URL).

2. HTTP POST to the `controlURL` with an `AddPortMapping` SOAP action:

   Headers:
   ```
   Content-Type: text/xml; charset="utf-8"
   SOAPAction: "urn:schemas-upnp-org:service:WANIPConnection:1#AddPortMapping"
   ```

   Body (fixed template, fill in the parameters):
   ```xml
   <?xml version="1.0"?>
   <s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"
               s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
     <s:Body>
       <u:AddPortMapping xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
         <NewRemoteHost></NewRemoteHost>
         <NewExternalPort>{port}</NewExternalPort>
         <NewProtocol>UDP</NewProtocol>
         <NewInternalPort>{port}</NewInternalPort>
         <NewInternalClient>{lan_ip}</NewInternalClient>
         <NewEnabled>1</NewEnabled>
         <NewPortMappingDescription>hopssh</NewPortMappingDescription>
         <NewLeaseDuration>7200</NewLeaseDuration>
       </u:AddPortMapping>
     </s:Body>
   </s:Envelope>
   ```

   **Parameter order matters** — some routers 402 on reordered children. Keep as above.

3. Parse the 200 OK response. If it's a `<s:Fault>`, extract `<errorCode>` and return a typed error (725 → retry with `NewLeaseDuration=0`; 718 → port already taken, retry with random external port).

4. Separately send a `GetExternalIPAddress` SOAP action to learn the public IPv4.

### Common fault codes we must handle

| Code | Meaning | Our response |
|---|---|---|
| 402 | Invalid args | Bail (log + report as bug in parameter order) |
| 605 | String too long | Truncate description; retry once |
| 715 | Wildcard not allowed in src IP | Bail (our request has empty `<NewRemoteHost>`, should be fine) |
| 718 | Port already mapped | Pick random port, retry once; on second 718 give up |
| 725 | Only permanent leases supported | Retry with `NewLeaseDuration=0` (indefinite) |

### Parsing

SOAP responses are fixed shape; we don't need a real XML parser. Regex for `<NewExternalIPAddress>(.+?)</NewExternalIPAddress>` and `<errorCode>(\d+)</errorCode>` handles all of it.

For the initial device description XML (could be a dozen nested services), use `encoding/xml` with a minimal struct tree — one-time parse cost is fine.

### Estimated size

SSDP + SOAP together: ~500 lines Go. Largest of the three protocols by far.

## Gateway discovery

**macOS:** `exec.Command("route", "-n", "get", "default")` — parse output for `gateway:` line.

**Linux:** read `/proc/net/route`, skip header, find line where `Destination == 00000000` and `Flags & 0x3 != 0` (UP+GATEWAY). Gateway field is little-endian hex. Parse to `netip.Addr`.

**Windows:** avoid `golang.org/x/sys` dependency by running `route print 0.0.0.0` and parsing the single "0.0.0.0 0.0.0.0 GATEWAY IFACE" row. Ugly but stdlib-only.

```go
// internal/portmap/gateway.go
func DiscoverGateway() (netip.Addr, error) {
    switch runtime.GOOS {
    case "darwin":
        return discoverGatewayBSD()
    case "linux":
        return discoverGatewayLinux()
    case "windows":
        return discoverGatewayWindows()
    default:
        return netip.Addr{}, fmt.Errorf("portmap: unsupported OS %q", runtime.GOOS)
    }
}
```

Each OS-specific function is 20-40 lines. Total ~120 lines.

## Coordinator

```go
// internal/portmap/portmap.go
type Manager struct {
    l        *logrus.Logger
    internal uint16
    mu       sync.Mutex
    winner   Client
    current  netip.AddrPort  // public AddrPort assigned by winner
    cancel   context.CancelFunc
}

func (m *Manager) Start(ctx context.Context) error {
    gw, err := DiscoverGateway()
    if err != nil {
        return fmt.Errorf("gateway discovery: %w", err)
    }

    clients := []Client{
        &natPMP{gateway: gw},
        &pcp{gateway: gw},
        &upnp{}, // uses SSDP, no gateway arg
    }

    // Parallel probe. First non-error wins. 3 s overall budget.
    probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
    defer probeCancel()

    type win struct {
        client Client
        addr   netip.AddrPort
        ttl    time.Duration
    }
    ch := make(chan win, len(clients))
    for _, c := range clients {
        go func(c Client) {
            addr, ttl, err := c.Map(probeCtx, m.internal)
            if err == nil {
                ch <- win{c, addr, ttl}
            } else {
                m.l.WithError(err).Debugf("portmap %s: %v", c.Name(), err)
            }
        }(c)
    }

    select {
    case w := <-ch:
        m.winner = w.client
        m.current = w.addr
        m.l.WithFields(logrus.Fields{
            "protocol": w.client.Name(),
            "public":   w.addr.String(),
            "ttl":      w.ttl,
        }).Info("portmap: established public mapping")

        // Spawn refresh loop tied to the parent ctx (not the probe ctx).
        refreshCtx, cancel := context.WithCancel(ctx)
        m.cancel = cancel
        go m.refreshLoop(refreshCtx, w.ttl)
        return nil

    case <-probeCtx.Done():
        return errors.New("portmap: no protocol succeeded")
    }
}

// Current returns the latest public mapping. Returns zero AddrPort if not yet mapped.
func (m *Manager) Current() netip.AddrPort {
    m.mu.Lock()
    defer m.mu.Unlock()
    return m.current
}

// Stop unmaps and terminates the refresh loop.
func (m *Manager) Stop() {
    if m.cancel != nil {
        m.cancel()
    }
    if m.winner != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        _ = m.winner.Unmap(ctx, m.internal)
    }
}

func (m *Manager) refreshLoop(ctx context.Context, initial time.Duration) {
    // Refresh at 50% of ttl. Floor at 60s to avoid busy-loops on
    // misbehaving routers that return lifetime=0.
    for {
        sleep := initial / 2
        if sleep < time.Minute {
            sleep = time.Minute
        }
        select {
        case <-ctx.Done():
            return
        case <-time.After(sleep):
        }
        addr, ttl, err := m.winner.Map(ctx, m.internal)
        if err != nil {
            m.l.WithError(err).Warn("portmap: refresh failed; holding last mapping")
            continue
        }
        m.mu.Lock()
        if addr != m.current {
            m.l.WithField("was", m.current).WithField("now", addr).Info("portmap: external port changed on refresh")
        }
        m.current = addr
        m.mu.Unlock()
        initial = ttl
    }
}
```

## Nebula vendor patch (#11)

### 1. Add a new method on `LightHouse`

File: `vendor/github.com/slackhq/nebula/lighthouse.go`

After the existing `GetAdvertiseAddrs()` (around line 159), add:

```go
// AddAdvertiseAddr appends a dynamically-discovered endpoint to the set of
// addresses we advertise to the lighthouse. Called from e.g. the portmap
// subsystem when a UPnP/NAT-PMP public mapping is established.
// Safe for concurrent use; the next SendUpdate tick picks it up.
func (lh *LightHouse) AddAdvertiseAddr(addr netip.AddrPort) {
    if !addr.IsValid() {
        return
    }
    for {
        cur := lh.advertiseAddrs.Load()
        next := make([]netip.AddrPort, 0, len(*cur)+1)
        next = append(next, *cur...)
        // De-dup
        for _, a := range next {
            if a == addr {
                return
            }
        }
        next = append(next, addr)
        if lh.advertiseAddrs.CompareAndSwap(cur, &next) {
            lh.l.WithField("addr", addr).Info("portmap: advertise_addrs updated")
            return
        }
        // CAS contention; retry.
    }
}

// RemoveAdvertiseAddr is the inverse; used on portmap shutdown.
func (lh *LightHouse) RemoveAdvertiseAddr(addr netip.AddrPort) {
    for {
        cur := lh.advertiseAddrs.Load()
        next := make([]netip.AddrPort, 0, len(*cur))
        changed := false
        for _, a := range *cur {
            if a == addr {
                changed = true
                continue
            }
            next = append(next, a)
        }
        if !changed {
            return
        }
        if lh.advertiseAddrs.CompareAndSwap(cur, &next) {
            return
        }
    }
}
```

### 2. Injection point in `main.go`

File: `vendor/github.com/slackhq/nebula/main.go`

Between line 263 (`lightHouse.ifce = ifce`) and line 271 (`go handshakeManager.Run(ctx)`):

```go
// portmap: if enabled, try to obtain a public port mapping in the background.
// Non-blocking — if no protocol succeeds, the agent continues with
// hole-punching only (lighthouse still advertises the punched endpoints).
if c.GetBool("portmap.enabled", true) {
    pm := portmap.New(l, uint16(port))
    go func() {
        if err := pm.Start(ctx); err != nil {
            l.WithError(err).Info("portmap: no protocol available; falling back to hole-punching only")
            return
        }
        lightHouse.AddAdvertiseAddr(pm.Current())
        // Watch for external-port changes on refresh.
        go pm.WatchChanges(ctx, func(old, new netip.AddrPort) {
            lightHouse.RemoveAdvertiseAddr(old)
            lightHouse.AddAdvertiseAddr(new)
        })
    }()
    // Tear down on shutdown.
    go func() {
        <-ctx.Done()
        pm.Stop()
    }()
}
```

**Package import** at the top of main.go:

```go
import (
    // ... existing imports ...
    "github.com/trustos/hopssh/internal/portmap"
)
```

**Subtle issue:** the vendored Nebula code importing a hopssh-internal package is a circular-dependency smell. Two options:

1. **Import the hopssh package directly** from vendor — works because Go module resolution uses our go.mod. This is what patch 11 does in my skeleton above.
2. **Expose an interface in vendor/nebula and inject from the hopssh agent side** — cleaner but requires more plumbing.

Go with option 1 for v1; it matches the style of our other vendor patches. If option 2 becomes necessary (e.g. we want to upstream), refactor later.

### Race mitigation

The agent's concern: if `SendUpdate` fires BEFORE portmap's goroutine finishes, the first host-update announcement to peers will lack our public port. In practice this is fine — `SendUpdate` ticks every 10 s by default (`lighthouse.interval`), so the next tick after portmap completion (< 3 s) carries the new addr. For users who want zero-gap, we could block `StartUpdateWorker` on portmap completion, but that would delay boot on networks without any portmap protocol by up to 3 s. Not worth it.

## Agent config surface

File: `internal/nebulacfg/defaults.go`

Add:

```go
// PortmapEnabled controls whether the agent tries UPnP/NAT-PMP/PCP at
// startup to obtain a public port mapping. Essential for direct P2P
// across asymmetric CGNAT setups (one peer on home router, one on
// cellular/office CGNAT). Disable only if the router misbehaves
// (ships bad UPnP responses, blacklists our mapping, etc.).
const PortmapEnabled = true
```

File: `cmd/agent/enroll.go::writeNebulaConfig`

Add a `portmap` section to the generated `nebula.yaml`:

```yaml
portmap:
  enabled: true
```

The Nebula vendor patch reads this via `c.GetBool("portmap.enabled", true)`.

`cmd/agent/renew.go::ensureP2PConfig` should be extended to self-heal this stanza on boot (matching the existing pattern for other critical P2P settings).

## Testing plan

### Unit tests

Each protocol client tested in isolation with a mock server:

**natpmp_test.go:**
- `TestNATPMP_MapSuccess` — mock server sends back the expected 16-byte response; client returns the right `netip.AddrPort`.
- `TestNATPMP_MapResultCode` — non-zero result → typed error.
- `TestNATPMP_MapTimeout` — server never replies → after 9 attempts, error.
- `TestNATPMP_MapContextCancel` — cancel parent ctx mid-retry → clean error.

**pcp_test.go:**
- `TestPCP_MapSuccess`.
- `TestPCP_NonceEcho` — different response nonce should be rejected.
- `TestPCP_VersionNegotiation` — server returns v=0 result=1, client should treat as "fall back to natpmp".

**upnp_test.go:**
- `TestUPnP_SSDPParse` — given a canned SSDP response, extract `LOCATION`.
- `TestUPnP_DeviceDescParse` — given a canned XML, extract `WANIPConnection` `controlURL`.
- `TestUPnP_AddPortMappingSOAP` — POST to a mock HTTP server, verify SOAP envelope shape, return a fake 200.
- `TestUPnP_FaultHandling` — server returns UPnPError 718, client retries with different ext port.

**gateway_test.go:**
- Parse canned outputs for `route -n get default` (macOS), `/proc/net/route` (Linux), `route print` (Windows).

### Integration test (user's network)

1. Build a standalone CLI in `cmd/portmap-test/main.go`:
   ```go
   func main() {
       port := flag.Uint("port", 4242, "internal port")
       flag.Parse()
       pm := portmap.New(logrus.StandardLogger(), uint16(*port))
       ctx, cancel := context.WithCancel(context.Background())
       if err := pm.Start(ctx); err != nil { log.Fatal(err) }
       fmt.Println("public:", pm.Current())
       // Wait for Ctrl-C, then unmap.
       sigs := make(chan os.Signal, 1); signal.Notify(sigs, os.Interrupt)
       <-sigs; cancel(); pm.Stop()
   }
   ```
2. Run against the user's TP-Link. Verify:
   - Probes: NAT-PMP wins (we saw this in the diagnostic).
   - Public addr matches the real external IP (`46.10.240.91:4242`).
   - `upnpc -l` shows the mapping in the router's table.
   - MBP can now reach `46.10.240.91:4242` from cellular.
3. Test against a router WITHOUT UPnP/NAT-PMP (we can disable it in TP-Link admin UI) — all three protocols should fail cleanly within 3 s.

### End-to-end test (Nebula direct P2P)

1. Apply patch 11, rebuild hop-agent.
2. Enroll on both Mac mini and MBP.
3. Without intervention: `hop-agent status` on MBP should show peer = Mac mini with `relayed=false` within ~10 s.
4. Verify via `tailscale status`-style output that direct P2P is active.
5. Run the existing benchmark harness (`spike/relay-vs-tailscale-evidence/scripts/run-all.sh`) to quantify the improvement vs the relayed baseline.

## Rollout plan

### Ship order

1. **NAT-PMP only** (PR #1) — behind `portmap.enabled=true` default. Smallest code change; works on the user's TP-Link immediately. Verifiable end-to-end.
2. **PCP** (PR #2) — bolt-on inside the portmap package. Handles IGDv2 routers that retired NAT-PMP.
3. **UPnP-IGD** (PR #3) — larger change; ships last. Handles routers that only support UPnP (most off-the-shelf routers still do).

Each PR is independently verifiable + shippable. If PR #1 alone solves the user's complaint, we ship, observe, and iterate.

### Validation gates

Before merging each PR:
- Unit tests green on macOS + Linux + Windows via existing CI workflow.
- Manual integration test on user's network shows mapping established + Nebula direct P2P verified.
- Benchmark harness run shows direct P2P latency/throughput in line with Tailscale on the same path.

### Revert plan

`portmap.enabled: false` in `nebula.yaml` disables the whole thing. For in-field emergency: push a new release with the default flipped to false, agents self-update from the control plane.

## Risks and open questions

1. **Router UPnP bugs.** Some TP-Link / Netgear / ASUS models have buggy IGD implementations — e.g. accept an `AddPortMapping` but don't actually forward, or expire mappings aggressively. Mitigation: logs show the public addr; user can verify externally. If a router misbehaves reproducibly, blacklist it by firmware string (`SERVER:` header in SSDP reply) — future work.
2. **ISP-owned routers.** Many ISPs disable UPnP by default to prevent malware. In that case, all three protocols fail; we fall back to hole-punching. Acceptable — same as today.
3. **macOS permission prompts.** First-run of the agent may trigger macOS Application Firewall prompt to allow inbound. We saw this during diagnosis — the packet hit the kernel but Python was blocked. `hop-agent` will hit the same prompt; installer / launchd plist should pre-allowlist it. Out of scope for v1 fix; track as a follow-up.
4. **Double-NAT.** A minority of homes have a second NAT (ISP modem + user router). UPnP/NAT-PMP on the user router gets a private IP, not a public one — mapping is useless. Detection: the mapped "external" IP is in a private range. In that case, log a warning and continue with hole-punching.
5. **IPv6.** Completely skipped in v1. Most CGNAT setups are IPv4-only anyway; add later via PCP once v4 is shipping.
6. **Shutdown on crash.** If hop-agent crashes without `Stop()` running, the mapping stays in the router until its TTL expires (2 h). Not a correctness issue; next restart will re-request the mapping and the router will accept. Minor annoyance only.
7. **Circular import.** As noted above, the vendor patch imports `internal/portmap` — Go resolution works because of our go.mod but it's unusual. If this bites, refactor to an interface exposed from vendor side.

## Effort estimate

| Piece | Time |
|---|---|
| NAT-PMP + tests | 0.5 day |
| PCP + tests (bolt-on) | 0.25 day |
| Gateway discovery + tests | 0.5 day |
| Coordinator + refresh loop + tests | 0.5 day |
| UPnP-IGD (SSDP + SOAP + fault handling) + tests | 1.5 days |
| Vendor patch 11 + LightHouse methods | 0.5 day |
| Agent config plumbing + enroll.go | 0.25 day |
| Integration testing + bug fixes | 0.5 day |
| CLAUDE.md + docs | 0.25 day |
| **Total** | **~4.75 days** |

Ship PR #1 (NAT-PMP only) in ~2 days; rest incrementally.

## What happens after this ships

Once port mapping works, most asymmetric-NAT cases will go direct. Two follow-ups become newly interesting:

1. **Birthday-paradox port prediction** for double-symmetric-NAT cases (both sides CGNAT). Tailscale implements this in `magicsock`. Probably ~200 lines of Go on top of our existing handshake manager. Only matters for the 5-10% of users where both peers are on cellular; lower priority.
2. **PeerRelay (coordinated relay)** — when direct P2P genuinely can't be established, pick a healthy peer (e.g. the Mac mini from the home network) as a relay for the CGNAT peer. Tailscale calls this "Peer Relays". This shifts relay load off the lighthouse and onto user-contributed capacity. Medium-priority follow-up.

Neither blocks v1.
