# Port Mapping for Nebula — design

Companion to `NAT-DIAGNOSIS.md`. Implementation design for custom UPnP / NAT-PMP / PCP client + Nebula integration.

## Scope

**In:**
- NAT-PMP (RFC 6886) client — smallest, simplest, most widely supported modern protocol.
- PCP (RFC 6887) client — NAT-PMP's successor; same wire format as NAT-PMP for the common case, trivially added once NAT-PMP works.
- UPnP-IGD v1+v2 client — most universal legacy support; more complex (SSDP discovery + SOAP over HTTP).
- Coordinator that probes all three in parallel, picks the first success, refreshes before expiry.
- Nebula vendor patch to use the mapping.
- Opt-out config knob (`portmap.enabled: false` to disable).

**Out of scope for v1:**
- IPv6 port mapping (PCP only; the CGNAT case is IPv4 anyway).
- Advertising multiple external ports (only the Nebula listen port).
- Dynamic port change (keep whatever port the router gives us for the lifetime of the mapping).
- UPnP WANIPv6 / firewall management endpoints.
- Windows / Linux extra testing (protocols are OS-agnostic; we'll smoke-test on all three but macOS is the known-broken case we need to fix).

## Package layout

```
internal/portmap/
├── portmap.go           Coordinator — Try(ctx) returns (publicAddr, protocol, ttl, err)
├── natpmp.go            NAT-PMP wire protocol (RFC 6886)
├── pcp.go               PCP wire protocol (RFC 6887). Reuses UDP-4 and port 5351 from NAT-PMP; server replies with a different opcode family if it's PCP.
├── upnp.go              UPnP-IGD client: SSDP discovery (UDP multicast 239.255.255.250:1900) + SOAP over HTTP
├── upnp_ssdp.go         SSDP discovery loop (M-SEARCH + replies parsing)
├── upnp_soap.go         Minimal SOAP/XML request builder + parser (no heavy XML lib — just text/template + regex)
└── gateway.go           Discover default gateway IP (macOS/Linux/Windows specific)
```

Each of NAT-PMP, PCP, UPnP implements the same interface:

```go
type Client interface {
    // Map requests a port mapping. Returns (publicAddr, lifetime) on success.
    // ctx should have a short (~2s) timeout — if the protocol isn't supported,
    // we want to fail fast and move on to the next.
    Map(ctx context.Context, internalPort uint16) (publicAddr netip.AddrPort, ttl time.Duration, err error)

    // Unmap removes the mapping. Called on shutdown.
    Unmap(ctx context.Context, internalPort uint16) error

    // Name returns a short label for logging ("natpmp", "pcp", "upnp").
    Name() string
}
```

The coordinator tries all three concurrently. First success wins. Winner's protocol is remembered and used exclusively for refresh so we don't churn.

## NAT-PMP — smallest subset (120 lines)

Wire format from RFC 6886 §3:

**External-address request (op 0):**
```
[0x00, 0x00]   version=0, opcode=0
```
Sent to UDP `<gateway>:5351`. Response (opcode 0x80):
```
[0x00, 0x80, result_hi, result_lo, epoch(4), public_ip(4)]
```

**UDP map request (op 1):**
```
[0x00, 0x01, 0x00, 0x00, int_port(2), ext_port(2, suggested), lifetime(4, seconds)]
```
Response (opcode 0x81):
```
[0x00, 0x81, result_hi, result_lo, epoch(4), int_port(2), mapped_ext_port(2), lifetime(4)]
```

Implementation:
1. Open UDP socket.
2. Write request to `<gateway>:5351`.
3. Set deadline 250ms. Read response. If timeout, retry once, give up.
4. Parse response. If result code ≠ 0, error out.
5. Ask for external address (op 0) first to confirm gateway speaks NAT-PMP, then map port.
6. Lifetime defaults to 7200s (2h). Refresh at 50% (3600s).

## PCP — ~80 lines on top of NAT-PMP

RFC 6887 §11: same UDP port (5351), version=2. Request is 24 bytes + opcode-specific payload. For our case (MAP request), payload is 36 bytes.

Key behaviour: if we send PCP and gateway only supports NAT-PMP, it responds with "unsupported version" (result code 1). We downgrade to NAT-PMP. If gateway is pure PCP, our NAT-PMP request returns "unsupported version" and we upgrade.

Implementation: separate struct, shared gateway UDP discovery. Keep it simple — only the MAP opcode.

## UPnP-IGD — ~500 lines (largest component)

Two phases:

**Discovery (SSDP):**
1. Send `M-SEARCH * HTTP/1.1` UDP multicast to 239.255.255.250:1900 with
   `ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1`.
2. Read UDP replies for ~2s. Parse `LOCATION:` header — gives us the device description URL.
3. HTTP GET the device description XML. Walk the service tree to find
   `urn:schemas-upnp-org:service:WANIPConnection:1` (or `:2`) or `:WANPPPConnection:1`.
   Extract the `controlURL` and the `SCPDURL`.

**Control (SOAP over HTTP):**
1. POST to the controlURL with a SOAP envelope containing `AddPortMapping` action:
   ```xml
   <?xml version="1.0"?>
   <s:Envelope ...>
     <s:Body>
       <u:AddPortMapping xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
         <NewRemoteHost/>
         <NewExternalPort>4242</NewExternalPort>
         <NewProtocol>UDP</NewProtocol>
         <NewInternalPort>4242</NewInternalPort>
         <NewInternalClient>192.168.23.3</NewInternalClient>
         <NewEnabled>1</NewEnabled>
         <NewPortMappingDescription>hopssh</NewPortMappingDescription>
         <NewLeaseDuration>7200</NewLeaseDuration>
       </u:AddPortMapping>
     </s:Body>
   </s:Envelope>
   ```
2. Parse the SOAP reply. If fault, error. If OK, our internal port is now forwarded.
3. Separately call `GetExternalIPAddress` to learn the public IP.

No XML libs: SOAP envelopes are fixed-shape strings, parsing the response needs just a regex for `<NewExternalIPAddress>(.+?)</NewExternalIPAddress>`. Keeps compile-time clean.

Corner cases:
- Some routers refuse `NewLeaseDuration` and want indefinite. Fall back to duration=0 on error.
- Some routers only support `WANPPPConnection` instead of `WANIPConnection`. Try both.
- Some routers auto-re-map after reboot (they cache), so a stale mapping from a previous session might exist. `AddPortMapping` overwrites atomically — no `DeletePortMapping` needed first.

## Coordinator logic

```go
func (p *Portmap) Start(ctx context.Context, internalPort uint16) error {
    gateway, err := discoverGateway()
    if err != nil { return err }

    var (
        natpmp = NewNATPMP(gateway)
        pcp    = NewPCP(gateway)
        upnp   = NewUPnP() // uses SSDP, no gateway arg
    )

    type result struct {
        addr netip.AddrPort
        ttl  time.Duration
        proto string
    }
    ch := make(chan result, 3)
    tryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
    defer cancel()

    for _, c := range []Client{natpmp, pcp, upnp} {
        go func(client Client) {
            addr, ttl, err := client.Map(tryCtx, internalPort)
            if err == nil {
                select {
                case ch <- result{addr, ttl, client.Name()}:
                default:
                }
            }
        }(c)
    }

    select {
    case r := <-ch:
        cancel() // stop other probes once we have a winner
        p.winner.Store(r)
        go p.refreshLoop(ctx, r)
        return nil
    case <-tryCtx.Done():
        return errors.New("no portmap protocol succeeded")
    }
}
```

Refresh loop wakes at 50% of the lifetime, re-issues `Map`, updates the stored address if the router returns a different external port (rare but possible). On refresh failure, retry at 50% of remaining time, then fall back to re-starting the whole protocol handshake.

## Nebula vendor patch (#11)

File: `patches/11-portmap.patch` (new).

Target: `vendor/github.com/slackhq/nebula/interface.go` — `Main` function.

Pseudo-diff:

```go
// After `f.outside = ...` is initialized and we have f.myVpnAddrs/f.listenPort:

if c.GetBool("portmap.enabled", true) {
    pm := portmap.New(f.l)
    go func() {
        if err := pm.Start(ctx, uint16(f.listenPort)); err != nil {
            f.l.WithError(err).Info("portmap: no protocol available, hole-punching only")
            return
        }
        publicAddr := pm.PublicAddr()
        f.l.WithField("public_addr", publicAddr).Info("portmap: established public mapping")
        // Inject into lighthouse's known endpoints for us:
        f.lightHouse.AddAdvertiseAddr(publicAddr)
    }()
    go func() {
        <-ctx.Done()
        pm.Stop()
    }()
}
```

New method `(*LightHouse).AddAdvertiseAddr(netip.AddrPort)` — adds to the set of endpoints we include in our next lighthouse host update. Lighthouse will then propagate it to peers.

We don't need to change handshake_manager.go at all. Nebula's existing hole-punch receiver already accepts inbound handshakes on the listen socket — once the router forwards the port, the first inbound handshake hits `f.listenOut` and Nebula's existing flow takes over. Zero changes to the hot path.

## Agent-side config surface

New optional field in `internal/nebulacfg/defaults.go`:

```go
const PortmapEnabled = true
```

Rendered into `nebula.yaml` by `cmd/agent/enroll.go::writeNebulaConfig`:

```yaml
portmap:
  enabled: true
```

No dashboard surface for now. Power-user escape hatch only.

## Testing plan

1. **Unit tests** per protocol. Use a mock UDP server replying to NAT-PMP / PCP wire format. UPnP: mock HTTP server returning fixed SOAP response.
2. **macOS integration:** smoke-test against user's TP-Link router. Observe `portmap: got mapping 46.10.240.91:4242` in agent logs. Verify `upnpc -l` (if installed) shows the mapping.
3. **End-to-end:** enroll hopssh on both peers. Without the patch, confirm relay fallback. With the patch, confirm direct P2P.
4. **Regression via benchmark harness:** run `spike/relay-vs-tailscale-evidence/scripts/` to quantify direct-P2P improvement. Should match or beat Tailscale's direct path.
5. **Graceful degradation:** disable UPnP on the router (TP-Link setting); verify the agent still works via hole-punch or relay, with a single info-level log line.

## Estimated effort

- NAT-PMP: half a day
- PCP (bolt-on to NAT-PMP): 2 hours
- UPnP-IGD: 1-1.5 days (SSDP + SOAP is where most bugs live)
- Coordinator + refresh + tests: half a day
- Nebula vendor patch + integration: 2-3 hours
- End-to-end verification + benchmark-based regression: half a day

**Total: ~3-4 focused days for a version we'd ship.**

## Follow-up work (not v1)

- **IPv6 via PCP:** relevant only when both peers have IPv6 AND an intermediary firewall. Most current CGNAT setups don't benefit.
- **IGD:2 pinhole API:** modern UPnP for IPv6. Same reasoning.
- **Graceful Unmap on shutdown:** right now we rely on the router's TTL (7200s). Nicer UX would be to explicitly unmap on `hop-agent leave` or SIGTERM. Easy add later.
- **Portmap telemetry:** surface the mapping info on `hop-agent info` and the dashboard ("Public endpoint: x.y.z.w:42 via NAT-PMP"). Nice for debugging.
