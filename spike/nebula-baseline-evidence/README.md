# Nebula baseline reliability test (2026-04-16)

Empirical answer to: "does the existing Nebula transport survive a 30s
network outage on its own?"

## Test setup

- **Mac mini** (Ethernet, home network) Nebula overlay IP: `10.42.1.7`
- **MacBook Pro** (cellular hotspot via en0 → iPhone, public IP via Yettel BG carrier) overlay IP: `10.42.1.6`
- Both running `hop-agent v0.9.3-10-g8017382` enrolled in same hopssh network
- Nebula tunnel between peers goes via lighthouse-relay (cellular CGNAT prevents
  direct P2P), ~80-150ms RTT
- Test method: 30-second `sudo ifconfig en0 down` + bring back up while a
  long-lived TCP connection AND a 1Hz ping are running

## Files

- `01-laptop-ping.log` — first run, ping-only (TCP probe used one-shot `nc -l` and self-broke); 220 pings, 186 received
- `02-laptop-ping.log` — second run, ping; 220 sent, 186 received, 34 timeouts
- `02-nc-recv-server.log` — server-side TCP receive log; 215 of 220 MSGs delivered (only 216-220 missing because we killed the test before they were sent)
- `02-server-tcp5202.pcap` — wire trace of the TCP/5202 connection, full lifecycle through the outage

## Result

**Nebula survived the 30s outage with no application-visible disruption.**

| Metric | Value |
|---|---|
| TCP connection survival | ✅ stayed alive entire 220s |
| Application data loss across outage | 0 bytes — TCP send buffer queued during outage, retransmitted on recovery |
| ICMP packets lost during outage | 30 (= the inevitable 1Hz × 30s) |
| ICMP recovery time after `en0 up` | ~3 seconds (first successful ping at T+48 after outage ended at T+45) |
| Nebula tunnel reconnect mechanism | Existing 5s polling in `cmd/agent/nebula.go::watchNetworkChanges` + `RebindUDPServer` + `CloseAllTunnels(true)` |

## Strategic conclusion

This puts the result in **OUTCOME #1** of the plan
(`~/.claude/plans/purring-chasing-babbage.md` §"Strategic check before
Phase 1"): Nebula already does what we'd be building. Phase 1 (wiring
`Session` into `udp.Conn`) is unnecessary for the headline benefit
(connection survival across mobile network outages).

The QUIC `Session` work in `internal/quictransport/session.go` is preserved
as a validated reconnect-pattern reference for any future MASQUE / port-443
fallback / DPI-evasion feature.
