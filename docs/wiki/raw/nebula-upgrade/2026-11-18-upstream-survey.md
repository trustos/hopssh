# slackhq/nebula upstream survey — 2026-11-18

Pinned today: `github.com/slackhq/nebula v1.10.3` (go.mod:13, vendor/modules.txt:152).
Local Go toolchain: `go1.27.4 darwin/arm64`. hopssh `go.mod` directive: `go 1.25.8`.

All facts below were read from primary sources (upstream CHANGELOG.md, GitHub
releases/issues/PRs/advisories API, raw files at the release tags). Anything not
verifiable that way is marked **unverified**.

---

## 1. Releases after v1.10.3

v1.10.3 was published 2026-02-06. The GitHub releases API (newest first) returns
**v1.12.0 (2026-10-29), v1.11.2 (2026-09-21), v1.11.1 (2026-08-21), v1.11.0
(2026-07-23)** — so **v1.12.0 is the newest release as of today**; nothing newer
in the 3 weeks since.

Source: <https://raw.githubusercontent.com/slackhq/nebula/master/CHANGELOG.md>,
<https://api.github.com/repos/slackhq/nebula/releases>

### v1.11.0 — 2026-07-23 (milestone 25)

Security-relevant / correctness fixes we care about:
- **IPv6 firewall classifier fixes**: an IPv6 extension-header length overflow that
  "could make the firewall parse the wrong protocol and ports" (#1789); reject
  malformed handshakes incl. invalid ed25519 key lengths (#1601, #1756).
- **Relay fixes**: advance the replay window on relayed packets so a relay drops
  replayed frames instead of re-forwarding (#1751); fix a race in relay state
  handling (#1753); lock replay window updates (#1802); **fix relay
  re-establishment when a handshake arrives over a relay entry a one-sided
  teardown left `Disestablished`, which silently dropped every send until dead
  tunnel detection forced a re-handshake (#1805)** — same symptom class as our
  2026-05-14 mini↔MBP relay stall that motivated patch 26.
- **Hostmap**: fix hostmap corruption when a host has multiple overlay addresses
  (each address now gets its own list) (#1788, #1790); fix a data race on a
  host's remote address that could send packets to the wrong address during a
  roam (#1773).
- **Tunnels that could permanently escape connection manager monitoring** (#1752).
- **Don't call the packet reader after a UDP error on Darwin** (#1755).
- Properly handle `closetunnel` packets (#1638).
- **`Control` is now safe to stop and wait on from any lifecycle state; new
  `Control.Wait` blocks until nebula has fully stopped and returns the first
  fatal reader error. Failed starts release the udp sockets and tun fd instead
  of leaking them** (#1794) — this is the graceful-shutdown work (see §2).
- **New `listen.rebind_on_network_change` (default `true`, not reloadable):
  rebind the listener and re-query lighthouses on macOS when the underlay
  network changes** (#1816). This is upstream now doing what our agent's
  `watchNetworkChanges` + `RebindUDPServer` + `CloseAllTunnels(true)` does.
- Trigger an immediate lighthouse update when reconnecting to / adding a
  lighthouse (#1645). Reload the firewall when unsafe networks in the cert
  change (#1719).

Breaking:
- **logrus → `slog`.** Levels upper-case, trace → `DEBUG-4`, timestamps always
  RFC3339Nano, `logging.timestamp_format` ignored, messages reworded. API break:
  constructors now take `*slog.Logger`. This hits us directly — hopssh passes a
  logrus logger into `nebula.Main` (`internal/client/nebula.go:76,130`,
  `internal/mesh/network_manager.go:582`) and greps/parses agent logs.
- `firewall.inbound_action` / `outbound_action` were applied to the opposite
  direction; now corrected.
- Windows: WFP PERMIT filters installed by default (`tun.windows_bypass_wdf`,
  `listen.windows_bypass_wdf`); device set to `private` network category.
- Reject packets now use ICMP code 13 instead of 3.
- sshd profiling commands confined to `sshd.sandbox_dir`.
- Build against go 1.26 (#1818).

### v1.11.1 — 2026-08-21

- **Security (no GHSA filed): IPv6 packets whose next header is a protocol
  nebula does not parse (SCTP, GRE, IP-in-IP, …) are now classified as that
  protocol with no ports, "closing a firewall bypass where a crafted payload
  could steer the classifier into reading one as TCP/UDP and matching a
  TCP/UDP rule"** (#1840). Upgrading changes which rules allow such traffic —
  only `proto: any` covers them now.
- ICMPv6 type read from the wrong byte when classifying (echo identifier never
  picked up for conntrack) (#1840).
- **Enforce outbound message counter limits so a tunnel is rehandshaked before
  the counter can wrap, preventing nonce reuse** (#1841) — defense in depth.
- `nebula-cert ca` OOM on 32-bit with encrypted keys (#1834).
- Tolerate `ErrDumpInterrupted` listing tun addresses on Linux (#1835).
- Dropped unmaintained `github.com/cyberdelia/go-metrics-graphite` (#1832).

### v1.11.2 — 2026-09-21

- Windows NLMTU tracked per address family (IPv6 MTU was stuck at 65535
  regardless of `tun.mtu`); IPv6 overlay with `tun.mtu` < 1280 now refuses to
  start (#1871).
- Android 11+ `bind()` on netlink route sockets denied → node advertised no
  underlay addresses (#1881).
- Recompute transport checksum for packets a host sends to its own overlay
  address (macOS hardware offload left it unfinished → TCP/UDP to own IPv6
  overlay address dropped) (#1862).

### v1.12.0 — 2026-10-29 (milestone 29) — large, breaking

Added:
- **Linux udp/tun offloads (TSO/USO, UDP GSO/GRO), off by default** via
  `tun.use_offloads` / `listen.udp_offloads`; **batch outbound UDP sends up to
  128 packets per `sendmmsg`**; thread pinning + CPU affinity
  (`tun.pin_threads`, `tun.cpu_affinity`). Off by default, "enabled by default
  in a future release".
- Native Go FIPS 140-3 builds (`make fips140`); boringcrypto builds deprecated.
- Local APIs under `local_api` (control socket + `nebula ctl`, opt-in info
  socket serving `/v1/identity`).
- Experimental `dns` stanza (split DNS / whole-host resolver), macOS + Windows
  only, needs root/admin — overlaps our own per-network DNS work.
- Embedders: on macOS, `overlay.NewFdDeviceFromConfig` to hand nebula a utun fd
  the host already configured (relevant to our macOS utun / NE story).

Changed / fixed relevant to us:
- Certs in `pki.ca` / `pki.cert` from a *newer* nebula are skipped with a
  warning instead of failing startup (#1999).
- Relay hosts batch the packets they forward (#1971); **a relay drops a relay
  request that names a different source than the sending peer** (#1980).
- Lighthouse queries never block the caller; `handshakes.query_buffer` default
  1024 (was 64) (#1910). `static_host_map` hostnames keep last-good addresses on
  lookup failure, resolve in parallel, re-resolve on handshake;
  `static_map.lookup_timeout` default 5s (was 250ms) (#1982).
- `tun.tx_queue` default 5000 (was 500) (#1928).
- macOS: utun gets its vpn address as /32 or /128 and nebula routes the vpn
  network to it explicitly, "so other VPNs can't take over the overlay route"
  (#1931); unsafe-route handling for routes another interface already owns
  (#1925, #1942); don't delete routes nebula never installed (#1933).
- Inbound packets arriving through a peer's unsafe network with a source inside
  our own vpn networks are dropped (#1986).
- A node with an IPv4 UDP socket no longer handshakes/roams/punches to a peer's
  IPv6 addresses or advertises its own IPv6 interface addrs (#1991).
- `remote_allow_list` applied before capping a host's addresses at max remotes
  (#1985).
- **A tunnel now requests a fresh hole punch after every UDP rebind — it could
  miss one after an exact multiple of 256 rebinds** (#1820).
- On Linux, shutdown waits for the udp and tun readers to exit before closing
  their fds (#1979).
- ~10 MB less memory: gopacket no longer linked outside tests (#1864).
- Failing sends logged at most once/second with a skipped count (#1915);
  blocklisted/invalid inbound packets logged at debug (#1961, #1969, #1970).
- Crash fixes: `group: []` / non-string `groups` no longer crashes on startup or
  reload (#1912); `firewall.conntrack` timeout ≤ 0 no longer crashes (#1984);
  `create-tunnel -address <ip:port>` no longer crashes (#1909); v2 cert signing
  now rejects empty/oversized name or empty group (#1967).

Breaking (this is the expensive part):
- **Building or embedding now requires go v1.27.3** (#1940, #2028).
- Firewall `group` YAML typing tightened; `pki.blocklist` entries must be hex
  SHA-256 (compared case-insensitively) (#1964); Linux refuses a `tun.dev` that
  already exists, including a pre-created persistent device (#1704); `tun.mtu`
  capped at 8937 (#1899); ssh debug commands exit 1 on refusal; nebula refuses
  to start on a non-flag argument.
- **Embedder API breaks**: `config.C.RegisterReloadCallback` signature change and
  `ReloadConfig*` returns `config.ReloadResult` (#1935); **`overlay.Device` no
  longer embeds `io.ReadWriteCloser`, `NewMultiQueueReader` /
  `SupportsMultiqueue` replaced by `Queues` returning `tio.Queue`s** (#1704);
  **`udp.Conn` gained `WriteBatch`, `IPv4Only`; `ListenOut` takes a flush
  callback; `ReloadConfig` takes `*config.Reload`; `udp.NewListener` /
  `udp.NewGenericListener` take `udp.Settings`** (#1704, #1935, #1971, #1991);
  ssh debug commands moved to a `commands` package, `sshd.NewSSHServer` takes
  `*commands.Registry`; `firewall.ProtoTCP/UDP/ICMP` → `iputil.IPProtocol*`
  (#1958, #1864).
- Removed sshd profiling commands and `sshd.sandbox_dir` (#1965).

---

## 2. Issue #1031 / PR #1375 — is patch 01 droppable?

**Yes. Patch 01 can be dropped at v1.11.0 or later.**

- Issue #1031 is **closed**. A collaborator (nbrownus) stated on 2026-05-06 that
  it was resolved by **PR #1661**.
  <https://github.com/slackhq/nebula/issues/1031>
- **PR #1375 (the PR CLAUDE.md and patches/README.md cite) was CLOSED without
  being merged.** It was superseded by:
- **PR #1661 — "Remove more os.Exit calls and give a more reliable wait for stop
  function (attempt 3)" — MERGED 2026-04-20, milestone v1.11.0.** Touches
  `control.go`, `interface.go`, `udp/udp_generic.go`, `tun_linux.go`,
  `cmd/nebula*/main.go`; makes tun I/O non-blocking with a poll/shutdown fd so
  readers unblock at shutdown instead of `os.Exit`.
  <https://github.com/slackhq/nebula/pull/1661>
- Verified in the raw source: at **v1.11.2 and v1.12.0, `interface.go` contains
  no `os.Exit` call and no `io.ErrClosedPipe` reference.** Read errors are now
  guarded by lifecycle state instead of error identity:
  `if err != nil && !f.closed.Load() && f.ctx.Err() == nil { … f.onFatal(err) }`
  (listenOut) and the equivalent guard in listenIn. A closed-socket read during
  teardown therefore does not become fatal.
  <https://raw.githubusercontent.com/slackhq/nebula/v1.12.0/interface.go>
- Consequence: patch 01's hunk (which edits
  `if errors.Is(err, os.ErrClosed) && f.closed.Load()` at old line 297) **no
  longer exists upstream** — the patch must be *deleted*, not rebased.
  Companion win: `Control.Wait` (#1794, v1.11.0) gives us the "block until fully
  stopped" primitive our cert-reload close-before-start dance currently
  hand-rolls.

**Action for CLAUDE.md:** the "Vendor Patch: Nebula" section is stale on two
counts — PR #1375 is closed-unmerged (the real fix is #1661 in v1.11.0), and
`scripts/check-nebula-patch.sh` **does not exist** in this repo (`ls scripts/`
has no such file). Also note the patch set is 01–26 (25 files), not "1 vendor
patch" / "patches 01-10" as CLAUDE.md and patches/README.md describe; README's
inventory stops at 22 and omits 24, 25, 26.

---

## 3. Patch-by-patch assessment (v1.10.3 → v1.12.0)

Method: patch headers read locally; upstream file state read from raw files at
tag `v1.12.0`. "Conflict" = the hunk context or the surrounding API changed
enough that `patch` will reject it; "still needed" = upstream has no equivalent.

| # | Patch | Upstream file(s) | Upstream state at v1.12.0 | Verdict |
|---|---|---|---|---|
| 01 | graceful-shutdown | `interface.go` | no `os.Exit`, no `io.ErrClosedPipe`; guards are `!f.closed.Load() && f.ctx.Err() == nil` | **Superseded — drop** (#1661 in v1.11.0) |
| 02 | testreply-panic-fix (copy `d` before `send` to avoid buffer-overlap panic) | `outside.go` | test-reply path rewritten around a receive context: `f.send(header.Test, header.TestReply, hostinfo.ConnectionState, hostinfo, out, rxc.nb, rxc.scratch[:0])` — separate `rxc.scratch` buffer | **Likely superseded — unverified.** Needs a targeted test; the aliasing our patch worked around may be gone |
| 03 | tun-darwin-read-buffer (cache the `make([]byte, len+4)`) | `overlay/tun_darwin.go` | `func (t *tun) Read(to []byte) (int, error)` — caller supplies the buffer, only a 4-byte `head` on the stack | **Superseded — drop** (API change makes the alloc site disappear) |
| 04 | batch-udp-darwin (`sendmsg_x`/`recvmsg_x`) | `udp/udp_darwin.go` | **no batch syscalls at all** ("Just read one packet at a time"); `StdConn` fields are now `*net.UDPConn, isV4, sysFd, l`; `WriteBatch` exists but loops `WriteTo`; `ListenOut` takes a `flush func()` | **Conflicts — full re-port** |
| 05 | tests for 04 | new `udp/udp_darwin_batch_test.go` | n/a | Applies (tests our code; update to new types) |
| 06 | batch-tun-darwin (`recvmsg_x` on utun) | `overlay/tun_darwin.go` | new `tio.Queue` device model (`Queues()` → `tio.NewSingleQueue(t, defaultBatchBufSize)`); `NewMultiQueueReader`/`SupportsMultiqueue` gone | **Conflicts — re-port against `tio.Queue`** |
| 07 | batch-listenin (`batchReader` + `Flush()` in `listenIn`) | `interface.go` | `listenIn(queues[i], i)` / `listenOut(i)` restructured; no `batchReader` | **Conflicts** |
| 08 | conn-flush-interface (`Flush() error` on `udp.Conn`) | `udp/conn.go`, `udp_generic.go`, `udp_linux.go`, `udp_rio_windows.go`, `udp_tester.go` | upstream added a **`flush func()` callback parameter to `ListenOut`** plus `WriteBatch`/`IPv4Only` on `udp.Conn` | **Partly superseded — rewrite against upstream's flush callback** rather than our interface method |
| 09 | priority-queue-darwin | `udp/udp_darwin.go` | no priority lanes, no batch buffers upstream | **Conflicts — re-port** |
| 10 | tests for 09 | new test file | n/a | Applies |
| 11 | portmap-advertise-addr (`AddAdvertiseAddr`) | `control.go`, `lighthouse.go` | **no `AddAdvertiseAddr`**; only `GetAdvertiseAddrs()`, `TriggerUpdate()`; advertise_addrs set via config `reload` | **Still needed — conflicts** |
| 12 | pipeline-listenin-darwin | `interface.go` | listenIn/listenOut are still single goroutines but restructured (queues, per-reader index) | **Conflicts — re-port** |
| 13 | tests for 12 | new `interface_pipeline_test.go` | n/a | Applies |
| 14 | roam-suppress-window (2 → 10) | `hostmap.go` | `const RoamingSuppressSeconds = 2` — unchanged | **Still needed — likely applies cleanly** |
| 15 | roam-prefer-ranges in `handleHostRoaming` | `outside.go` | `handleHostRoaming` still does **not** consult `preferred_ranges` (uses `remote_allow_list` + time window + `hostinfo.SetRemote`). Upstream preference lives only at handshake time (`SetRemoteIfPreferred`, which does check `preferredRanges`) | **Still needed — conflicts** (hunk context changed: `remote_allow_list` check inserted, `rxc` refactor) |
| 16 | tests for 14+15 | new `hostmap_roam_test.go` | n/a | Applies |
| 17 | pipeline-listenout-darwin | `udp/udp_darwin.go` | `ListenOut(flush func())` signature changed; no reader/worker split upstream | **Conflicts — re-port** |
| 18 | tests for 17 | new test file | n/a | Applies |
| 19 | qos-class-darwin | `interface.go`, `udp/udp_darwin.go` + 4 new files | new files fine; call sites in both restructured files | **Call sites conflict** |
| 20 | static-hostmap-inject (`AddStaticHostMap`) | `control.go`, `lighthouse.go` | **no such method**; nearest is `QueryCache(vpnAddrs) *RemoteList`; add logic is unexported (`addStaticRemotes`) | **Still needed — conflicts** |
| 21 | replace-static-hostmap (`ReplaceStaticHostMap`) | `lighthouse.go` | **no such method** | **Still needed — conflicts** |
| 22 | endpoint-probe (`SetInboundObserver`, `ProbeEndpoint`) | `control.go`, `interface.go`, `outside.go` | no inbound observer; no `sendTo`-to-specific-remote (test replies go through `f.send(..., hostinfo, ...)`) | **Still needed — conflicts** |
| 24 | flush-error-propagation | `handshake_manager.go`, `interface.go`, `udp/udp_darwin.go` | all three changed; upstream only reduced send-error log spam (#1915) and stopped calling the reader after a UDP error on Darwin (#1755) | **Conflicts — re-port** |
| 25 | cert-skew-tolerance (`ClockSkewTolerance`) | `cert/cert_v1.go`, `cert_v2.go` + new `cert/skew_tolerance.go` | `Expired(t)` is still exactly `notBefore.After(t) \|\| notAfter.Before(t)`; **no clock-skew tolerance anywhere upstream** | **Still needed — likely applies** (one-line hunks, unchanged functions) |
| 26 | relay-manager-warn-on-drop | `relay_manager.go` | still silent on `am_relay == false`, missing HostInfo, and invalid remote; **but field access changed to `peer.GetRemote()`** and a new `#1980` source-check block was inserted in the same function | **Still needed — conflicts** |

Summary counts: **2 droppable (01, 03)**, 1 probably droppable pending a test
(02), 1 partly superseded and worth rewriting against upstream's new seam (08),
**10 needing a real re-port** (04, 06, 07, 09, 11, 12, 15, 17, 19, 20, 21, 22,
24, 26 — the darwin perf stack plus every lighthouse/hostmap injection patch),
6 test-only patches that apply but must track the re-ported code, and 2 that
should still apply nearly as-is (14, 25).

### Hazard in the current patch pipeline

`Makefile:33-39` applies patches with
`patch -p1 --forward --silent < "$p" 2>/dev/null || true` and then
`find vendor -name '*.rej' -delete`. **A patch that fails to apply is silently
ignored and its reject file deleted.** On a version bump this produces a build
that looks fine and is missing perf/behavior patches (and, for 11/20/21/22,
fails to compile — those add methods our agent calls). Before any upgrade, change
this to fail loudly.

---

## 4. Security advisories affecting v1.10.3

- The only GitHub Security Advisory published for the repo is
  **GHSA-69x3-g4r3-p962 — "Blocklist Bypass possible via ECDSA Signature
  Malleability", severity high, published 2026-02-06, vulnerable range
  `>= 1.7.0, <= 1.10.2`, first patched `1.10.3`.**
  <https://api.github.com/repos/slackhq/nebula/security-advisories>,
  <https://github.com/slackhq/nebula/security/advisories/GHSA-69x3-g4r3-p962>
  → **hopssh on v1.10.3 is NOT affected by any published GHSA.**
  (Note the advisory's forward-looking caveat: nebula "will assert the low-s
  signature form when validating certificates in a future version" — newly
  issued P256 certs are clamped. We use `cert.Curve_CURVE25519` everywhere, so
  this does not touch us.)
- **Un-advisory'd security fixes we are missing** (fixed upstream, no GHSA):
  the v1.11.1 IPv6 firewall classifier bypass (#1840) and the IPv6
  extension-header length overflow firewall mis-parse fixed in v1.11.0 (#1789).
  Both are overlay-firewall bypasses. We run a nebula allow-list, so these are
  reachable in principle. Also #1841 (message-counter wrap / nonce-reuse defense)
  and #1751/#1802 (relay replay-window correctness).
- Go vuln DB: `https://pkg.go.dev/vuln/search?q=github.com/slackhq/nebula`
  returned HTTP 404 via WebFetch, so **the Go vuln DB listing could not be
  verified from a primary source.** The GitHub advisory API above is the
  authoritative upstream source and shows exactly one advisory.

---

## 5. Go version and dependency bumps

| nebula | `go` directive | gvisor.dev/gvisor | x/crypto | x/net | x/sys | x/sync | x/term |
|---|---|---|---|---|---|---|---|
| hopssh today (nebula v1.10.3) | `go 1.25.8` | `v0.0.0-20240423190808-9d7a357edefe` | v0.49.0 | v0.51.0 | v0.42.0 | v0.19.0 | v0.41.0 |
| nebula v1.11.0 / v1.11.2 | **`go 1.26.0`** | `v0.0.0-20240423190808-9d7a357edefe` (same as ours) | v0.54.0 | v0.57.0 | v0.47.0 | v0.22.0 / v0.24.0 | v0.45.0 / v0.47.0 |
| nebula v1.12.0 | **`go 1.27.3`** | **`v0.0.0-20261014193016-04d31b385980`** | v0.58.0 | v0.61.0 | v0.49.0 | v0.24.0 | v0.47.0 |

Sources: raw `go.mod` at each tag.

- Our local toolchain is already `go1.27.4`, so the *machine* can build either.
  Our **CI cannot**: `.github/workflows/ci.yml` pins `go-version: '1.24'` (3
  places), `release.yml` pins `'1.25'`, `release-desktop.yml` pins `'1.23'`, and
  `Dockerfile:16` builds on `golang:1.25.8`. All must move to ≥1.26 (for
  v1.11.x) or ≥1.27.3 (for v1.12.0).
- gvisor is unchanged between our pin and v1.11.2 — **no userspace-netstack
  risk on the v1.11.x path**. v1.12.0 jumps gvisor ~2.5 years, which is a real
  risk surface for our userspace (gvisor netstack) mode on all three OSes.
- v1.11.0's logrus→slog switch is the sleeper dependency change: hopssh injects
  a logrus logger into `nebula.Main` and its agents' log-parsing/forensic dumps
  key off logrus formatting.

---

## 6. Recommendation

**Target v1.11.2** (2026-09-21) as the upgrade; treat **v1.12.0 as a separate,
larger project** (go 1.27.3 + `overlay.Device`/`tio.Queue`/`udp.Conn` rework +
gvisor jump + our entire macOS perf stack re-ported).

Rationale for v1.11.2: it carries the graceful-shutdown fix that lets us delete
patch 01, the relay re-establishment fix (#1805) that matches a real production
incident we already debugged, the hostmap multi-address corruption fix (#1788,
#1790), the roam data-race fix (#1773), the tunnel-escapes-connection-manager
fix (#1752), the two firewall-bypass fixes (#1789, #1840), the nonce-reuse
defense (#1841), and native macOS underlay-change rebind (#1816) — with gvisor
unchanged and only two of our 25 patches droppable-but-not-conflicting.

Costs to budget: slog migration in agent + log-parsing, `go 1.26` in CI and
Dockerfile, and re-porting patches 04/06/07/09/11/12/15/17/19/20/21/22/24/26.
Patch 25 (clock-skew tolerance) is **not** superseded — upstream `Expired()` is
still strict, so our "certificate expired on a sleepy laptop" mitigation stays
ours, and the v1.11.x cert-reload/`Control.Wait` changes (#1794) are worth
evaluating against our own close-before-start reload dance in
`internal/client/renew.go::reloadNebula`.
