# Roadmap #29 — Multi-Network Per Agent — Phase E evidence

Ran end-to-end on 2026-04-19 across four hosts. All four ended up
enrolled in both `home` (10.42.1.0/24) and `work` (10.42.2.0/24); all
4×4 = 16 possible mesh-peer pings succeed with 0 % loss.

## Test matrix

| Host | Platform | Before v0.10 | After v0.10 | Migration? |
|---|---|---|---|---|
| Mac mini | darwin arm64 | `home` flat layout | `home` + `work` subdirs | yes |
| MacBook Pro | darwin arm64 | `work` flat layout (force-enrolled) | `work` + `home` subdirs | yes |
| Linux VM | Ubuntu 25.10 arm64 | `home` flat layout | `home` + `work` subdirs | yes |
| Windows 11 VM | arm64 | `home` flat layout | `home` + `work` subdirs | yes |

All four migrations preserved the original `nodeId` + cert + TUN mode.

## Mesh topology

```
home (10.42.1.0/24)                  work (10.42.2.0/24)
  10.42.1.7  Mac mini                  10.42.2.2  MacBook
  10.42.1.8  Linux VM                  10.42.2.3  Mac mini
  10.42.1.10 Windows                   10.42.2.4  Linux VM
  10.42.1.11 MacBook                   10.42.2.7  Windows
```

## Platform-specific features verified

- **macOS** `/etc/resolver/`: separate file per domain (`home`, `work`).
  `utun10` + `utun11` (kernel auto-assigns).
- **Linux drop-in merger** (Phase C): `/etc/systemd/resolved.conf.d/hopssh.conf`
  regenerated with `Merged across enrollments: home, work` and both
  DNS servers + domains in one `[Resolve]` block (sorted by
  enrollment name). Per-link DNS on two distinct Nebula interfaces
  `hop-home` + `hop-work`.
- **Windows NRPT** (Phase C): two rules, one per domain, each
  pointing at a dedicated loopback IP (`.home → 127.53.0.1`,
  `.work → 127.53.0.2`). Two DNS proxies listening on those
  loopbacks on port 53 (UDP + TCP).
- **hop-agent leave**: verified end-to-end on MacBook (removed a
  misdirected `work-2` enrollment without touching `work` or the
  service).

## Ping latencies (sample, LAN + relay mix)

| from → to | home | work |
|---|---|---|
| Mac mini | 10.42.1.8 Linux VM: 5–9 ms | 10.42.2.2 MacBook: 5–22 ms |
| MacBook | 10.42.1.7 Mac mini: 6–12 ms | 10.42.2.7 Windows: 2–65 ms |
| Linux VM | 10.42.1.7 Mac mini: 7–87 ms | 10.42.2.2 MacBook: 1–2 ms |
| Windows | 10.42.1.8 Linux VM: ~2 ms | 10.42.2.2 MacBook: ~1 ms |

(First-connect handshakes drive the max tail; steady-state is LAN-RTT
for peers on the same subnet, +relay-hop for cross-subnet.)

## Bugs caught + fixed during Phase E

Three real bugs in the v0.10 code, all fixed and committed:

1. **`ec7371b`** — migrated `nebula.yaml` still referenced the old
   flat-layout PKI paths; Nebula failed to start and the agent fell
   back to OS stack. Fix: `ensureP2PConfig` rewrites
   `pki.ca/cert/key` to match `inst.dir()` every boot.
2. **`6e793d9`** — on Linux, two Nebula instances both tried to
   create a TUN device named `nebula1`; kernel rejected the second.
   Fix: per-enrollment `dev: hop-<name>` (truncated to IFNAMSIZ).
   `dns_linux.go` follows suit and derives the per-instance Nebula
   interface from the instance name. macOS was already safe
   (`utun` auto-allocates).
3. **`6c7c45f`** — `hop-agent install` on Windows os.Exit'd with
   "service already exists" on second enroll. Fix: idempotent —
   if the SCM service is already registered, stop + start it (the
   same effect as macOS's `launchctl bootstrap` path and Linux's
   `systemctl restart`).

## Known transient observed during Phase E

- **Work lighthouse cold-start (~90 s)**: after first ever enrollment
  on a freshly-created network, the control-plane's per-network
  lighthouse was slow to warm up — 90 s of handshake timeouts before
  it began answering. Agents recover without intervention once the
  lighthouse warms. Worth a CLAUDE.md Discovery Log entry.

## Artifact directories

- `mac-mini/`  00-baseline, 01-after-migrate, 02-after-fix, 03-dual-verified
- `linux-vm/`  00-baseline, 01-dual-verified
- `windows-vm/` 01-dual-verified
- `macbook/`   01-dual-verified
