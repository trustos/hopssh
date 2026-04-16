# QUIC datagram throughput + latency spike

Standalone Go program (`main.go`) that ran the original QUIC viability
benchmarks during the Phase 0 evaluation on 2026-04-16. Four modes:

```
./quic-spike server :4242
./quic-spike client <peer-ip>:4242 <duration-sec> <packet-size-bytes>
./quic-spike rttprobe <peer-ip>:4242 <duration-sec> <interval-ms>
./quic-spike migration <peer-ip>:4242 <duration-sec>
```

No coupling to the rest of the hopssh tree (own `go.mod`). Build with
`go build` from this directory.

## What it proved

- QUIC datagrams (RFC 9221) sustain ~334 Mbps Mac mini ↔ MacBook Pro on raw WiFi
  with zero application-visible drops over a minute (vs ~112 Mbps for the same
  pair through Nebula+TCP via iperf3).
- Migration mode demonstrated `quic-go`'s `Connection.AddPath()` works for
  same-network forced migration (~35ms PATH_CHALLENGE round trip on LAN) but
  **fails to recover real network outages** — after `ifconfig en0 down`, the
  parent connection silently closes and `Probe()` never emits a PATH_CHALLENGE
  frame on the wire.

## Why it's archived

The migration finding led to building `internal/quictransport/session.go`
(transparent reconnect with TLS resumption), which DID survive 30s outages.
But a follow-up baseline test against the existing Nebula transport
(`spike/nebula-baseline-evidence/`) showed Nebula already survives the same
outage with TCP intact and ~3s recovery. Phase 1 (wiring `Session` into the
mesh `udp.Conn`) was therefore closed without implementation.

This spike harness stays in the tree as:
- A reproducible benchmark for any future QUIC work (MASQUE/port-443 fallback,
  DPI evasion).
- The reference implementation that the Discovery Log entry on QUIC
  Connection Migration in `CLAUDE.md` is grounded in.

See:
- `CLAUDE.md` §Discovery Log "QUIC Connection Migration (quic-go)" for the
  measured behavior.
- `~/.claude/plans/purring-chasing-babbage.md` for the strategic decision trail.
- `spike/migration-evidence/` for client qlog + server pcap fixtures from the
  migration tests.
- `spike/nebula-baseline-evidence/` for the test that closed Phase 1.
