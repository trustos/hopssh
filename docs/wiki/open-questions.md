# hopssh wiki — open questions

Cross-cutting unresolved items. Acts as a handoff state machine across sessions: when a session ends with "next session should do X", file it here, not in chat.

Per-incident open items live on the incident page itself (`## Open` section). This file is for items that span multiple pages or that don't have a natural home yet.

## 2026-11-26 — after the Nebula upgrade: what is left

- **v1.12.0** needs Go 1.27.3 and a much newer gvisor. There `linux-udp-close.patch` can go (upstream waits for the reader); `linux-tun.patch` has to be shown unnecessary with the kernel-TUN test first.
- **Log level.** Nebula v1.11 no longer sets the level of the logger it is given. Before, `nebula.Main` did, so the agent's "warn" and `HOPSSH_LIGHTHOUSE_LOG_LEVEL` never had an effect. Now `nebulacfg.Logger` applies the config's `logging.level` and the server variable works for the first time (default info). Anything that reads the logs by pattern has to expect `level=INFO msg="…" key=value`.
- **`TestPeerCache_SaveSkipsWhenUnchanged` is flaky** (1 of about 30): it compares modification times, and two saves that straddle a tick of the clock look like a rewrite. Older than the upgrade.
- **CI job `check-patches`** calls `make check-patches`, which does not exist; `continue-on-error` hides it. Remove the job or write the target.
- **`TestUncleanShutdownRaceLoser`** of upstream's e2e does not end with `paths.patch` (explained in `patches/README.md`); upstream's e2e are still in no script.
- **Windows** was only compiled and vetted, not run.
- **Nebula now watches the network itself on macOS** (`listen.rebind_on_network_change`, on by default): a message on the routing socket, one second to settle, then the same rebind our `watchNetworkChanges` does every 5 s, without closing tunnels. On the Macs it fired once at start (another network's utun appeared) and did no harm. Not looked at: whether our 5 s look is still needed on macOS, and what the two do together when the Wi-Fi changes.

Source / context: `patches/README.md`, `docs/wiki/raw/nebula-upgrade/`

Format:

```
## YYYY-MM-DD — short title

What's open. Why it matters. Where to start.

Source / context: link to wiki page or external URL.
```

Move closed items to a "Resolved" section at the bottom rather than deleting — the resolution rationale is itself useful.

---

## Open

## 2026-11-24 — v0.11.27: prove it on devices

The server runs it, no device does. Open until then: a device renews at half its certificate's life and stays online for 24 h (E); an in-app network on a Mac and a system-wide one; a machine that was off for a week comes back (B, L). Steps 7 and 8 of [[runbooks/release-v0.11.27]].

## 2026-11-24 — the database file relies on its directory's mode

`hop-server` creates a missing data directory with 0700, which keeps other local users out. An existing directory (a container volume made by something else) keeps its mode, and `hopssh.db`, `-wal` and `-shm` are created with 0644; only `encryption.key` is 0600. Secrets in the database are encrypted or hashed, the rest (users, networks, audit log) is not. **Action:** create the database with 0600 and tighten existing files at start, so that it doesn't depend on the directory.

## 2026-11-24 — dashboard: no security headers, and a dead link

`hopssh.com` sends no `Strict-Transport-Security`, no `Content-Security-Policy` and no `X-Frame-Options`. `SameSite=Lax` keeps the session out of frames of other sites, which limits clickjacking, but HSTS and `frame-ancestors` are missing. And "Download the DMG" on the login page leads back to the login page when signed out (`routes/+layout.svelte` sends `/download` there). Both older than v0.11.27.

## 2026-11-24 — releases are not gated by the tests

`release.yml` and `release-desktop.yml` start on any `v*` tag and run no test; `make release` tags whatever is checked out and pushes only the tag, so `main` can stay behind. The Docker image is built for the first time there. **Action:** a `test` job that the build jobs need. Until then: push `main`, wait for CI, then tag ([[runbooks/release-v0.11.27]]).

## 2026-11-24 — a lighthouse that fails to come back is silent

The idle reaper closes a network's lighthouse after 2 h without a device; the next heartbeat starts it again through `GetInstance`. `renew.go` throws that error away (`netInst, _ =`), so a lighthouse that cannot start (V did that in production, see [[incidents/2026-11-18-mbp-all-certs-expired]]) leaves no line in the log, and the network has no relay until a later heartbeat succeeds. The reaper's own line prints a pointer (`last activity: 0x…`). Also: a device that *enrolls* into a reaped network finds no lighthouse until its first heartbeat. **Action:** log the error; consider starting the lighthouse at enrollment.

## 2026-11-24 — five type errors in the dashboard

`npm run check` in `frontend/`: three in `network-topology.svelte`, two in `networks/[id]/+page.svelte` (`role` as a plain string; `network` possibly null). Known since May 2026. A sixth was a real bug: the "Clean up" button called a function that does not exist, so it deleted the devices and then showed an error (fixed for v0.11.27). **Action:** fix them and run the check in CI.

## 2026-11-23 — a proxied page runs as the dashboard (R)

`NodeProxy` serves a node's web service under the dashboard's own origin and strips its CSP. A script on any node's page can call `/api/…` with the viewer's login. The session cookie no longer reaches the node (Q), and a node can no longer set it (T), but same-origin requests still work. **Action:** serve proxied pages from a separate host name (best), or with `Content-Security-Policy: sandbox` without `allow-same-origin` (breaks apps that need storage). Pinned by `TestE2E_Proxy`.

## 2026-11-23 — API gaps pinned by the E2E tests

Each has a `KnownGap` in the test named, which fails once the gap is closed, so the marker can't be forgotten. Not fixed because each is a product decision or a change of its own.

| Gap | Test |
|---|---|
| A deleted device can join the mesh until its cert expires (≤ 24 h): nothing distributes a blocklist | `TestE2E_DeletedPeer` |
| A deleted node's name keeps resolving: `DeleteNode` doesn't call `RefreshDNS` | `TestE2E_DNS` |
| An outsider gets 403 instead of 404 on admin routes, so they can tell which network IDs exist (9 routes) | `TestE2E_Access` |
| A member who opens an invite uses it up for the person it was meant for | `TestE2E_Invites` |
| Removing a member leaves no audit trace. Nor do `StopPortForward`, `DeleteNetwork`, invite create/delete and `JoinNetwork` (read in the code, not pinned) | `TestE2E_History` |
| Every renewal ends the open sessions of an in-app network | `TestE2E_Lifecycle` |
| Any member runs commands on every node; the audit log doesn't say which command | `TestE2E_MemberShell` |
| What a device copies (up to 8 KB) is sent to every open dashboard of the network, where nothing uses it | `TestE2E_Clipboard` |
| A clip of 198 to 256 KB is refused: the limit for the request body forgets that the content is base64 | `TestE2E_Clipboard` |

Seen while writing the tests, not pinned: any member can start port forwards (they listen on 127.0.0.1 of the control-plane host only) and stop anyone's; CORS allow-methods lacks PUT and PATCH; an invite accepts `maxUses: -1`; a DNS record may point outside the network; `Enroll` and device `Poll` use up the token before the 409 check; `NetworkManager.StopNetwork` during a start in flight is untested.

Clipboard, seen while writing `TestE2E_Clipboard`, not pinned: `since` has seconds, so the second of two clips in one second is skipped for a device that polled in between; the limit is 60 requests a minute per address and every device polls 30 times a minute, so three devices behind one router run into it after about four minutes; a database error answers 401 (as renew and heartbeat did, a).

## 2026-11-24 — UDP between devices is blocked by our own firewall: allow it? (P)

Proven in [[concepts/netlab]] (`TestUDPBetweenDevices`, pinned): `writeNebulaConfig` allows inbound TCP and ICMP between nodes, nothing else. mosh, QUIC, WireGuard and Screen Sharing's media cannot work between peers. **Decide:** allow UDP for all, per network, or with the firewall rules of the roadmap. The change itself is one line; existing agents need their `nebula.yaml` rewritten.

## 2026-11-24 — what Tailscale has and the lab shows that we don't

1. **A way out of a network without UDP** (`TestNoUDP`, pinned): a relay on TCP 443. Hotels, offices, some mobile networks.
2. **The right packet size per path** (`TestSmallPackets/told_false`, pinned): on a line that takes less than 1480 bytes and does not say so, big packets vanish. A login works, a listing hangs. It needs DPLPMTUD (never built) or MSS clamping.

## 2026-11-24 — path fixes: what is still unproven

Patches 33 to 36 and `path_watch.go` are on the branch `lab/network`, not released. The whole lab passes (37 tests), and 39 of 47 planted bugs were noticed; the 8 others are listed in [[concepts/netlab]] with the reason, and so are 25 more, all noticed. **Not done:** real devices (macOS and Windows agents, a real phone carrier). The rebind after a network change is the one of the 8 that matters: on macOS only a real laptop can show whether it still works. An agent of v0.11.27 or older on the other end does not answer patch 35's question, so a path that works in one direction only can still be interrupted until both ends are updated.

`ownNets` (`path_watch.go`) counts the networks of every interface, also of another VPN (all three reviews of the pull request say so). **Harm not shown:** only an address that the peer itself reports is tried, the peer's `local_allow_list` and `selfEndpoints` hold back those of its other interfaces, and Nebula prefers every private address anyway since `preferred_ranges` works. What is left is a peer that can't tell its physical interface and then reports all of them. Two machines that are in one ZeroTier network would show it; the lab has none.

## 2026-11-24 — data races inside Nebula v1.10.3

`go test -race ./internal/client` fails in `TestE2E_QuestionMovesNobody` (8 of 8 runs) and sometimes in `TestE2E_NewTunnelStays` (2 of 8); in the one run of everything the rest was clean, and so is `-race` on the vendored `udp` package. Four pairs, all between lines that are the same in upstream v1.10.3, none in our patches or in `internal/client`:

1. `HostInfo.remote`: written by `SetRemote` (UDP thread), read by `sendNoMetrics` (TUN thread, two places) and by `copyHostInfo` (`ListHostmapHosts`, `GetHostInfoByVpnAddr`) without a lock. A torn read can mix an old address with a new port, so one packet goes astray or the dashboard shows a wrong address for one second. Our `path_watch.go` reads it once a second and at worst asks one question too many.
2. `RemoteList.relays`: sorted in place under its lock, its length read without it by `handleOutbound`. For a moment it looks empty, then one handshake attempt doesn't try the relays; the next one, a second later, does.

**Fixed 2026-11-25** (branch `fix/peer-address-race`): patch 37 makes the address an atomic value, patch 38 gives the handshake code a copy of the relay list taken under the lock. Each has a test in the vendored package for `-race`: the one of 37 is red without the patch, the one of 38 is red when the lock is taken out of `copyRelays` (without the patch it does not compile). Upstream v1.11.2 has the same fix as patch 37 (seen in its source), so 37 goes on an upgrade; the code of patch 38 was moved there (`relayManager.StartRelays`) and has to be looked at. **Still open:** CI doesn't use `-race`; both races were invisible to `go test ./...`.

## 2026-11-25 — certificates and paths: cases no test covers yet

From an audit of the tests against the ways a certificate or a path can fail. Covered: a fixed renewal instant, a start with an expired certificate, a deleted node (401), a renewal goroutine that dies, a lost path (lab), and since today a running agent whose renewals fail until after expiry (`TestE2E_RenewalFailsPastExpiry`: the peer drops it, and it is back 0.1 to 0.2 s after the server answers again). **Not covered:**

1. *(done 2026-11-25)* The peer's view of a machine whose certificate ran out, and that the peer gets through again by itself once it renewed: now part of `TestE2E_RenewalFailsPastExpiry`. In the lab it can't be shown; the server gives out 24-hour certificates only.
2. *(done 2026-11-25)* A renewal on the relay, of one and of both in the same moment: lab `TestRenewalOnTheRelay`, silent 0.7 to 0.85 s in 3 runs.
3. A new CA, or changed groups or address, arriving with a renewal.
4. A disk that is full or read-only when the new certificate is written.
5. A sleep longer than the life of the certificate, as one test from sleep to answer.
6. An open TCP session across a reload: pinned as a known gap, it is cut.

## 2026-11-25 — two short silences on both networks at once (field recorder)

`hop-field` (pull request 3) ran 4 hours between the two Macs: always connected, always direct, 599 of 600 probe answers per minute. Twice the probe was silent on `home` and `work` in the same instant (1.0 s at 17:38:30, 3.4 s at 18:04:11), no session lost, nothing in either agent's log. *Likely* the line under the mesh; not proven. A probe that runs beside it outside the mesh would tell.

## 2026-11-25 — a router that comes back is sometimes noticed late (`TestRouterReboot`, 1 of 12)

**Why open:** In the full lab run before v0.11.28, `TestRouterReboot` failed once: the session was silent for 60.9 s where the router was away for 30 s (limit 45 s). The other 11 runs of that day gave 30.6 to 31.0 s, among them 4 with this branch and 4 with main, alternating, right after. From the log of the slow run: the tunnel was declared dead 20 s into the outage, and Nebula then asks for a new one at growing gaps (`try_interval` times the number of the try: 1, 4, 8, 12 s). The router was back at :32, the next try came at :34, TCP's own retry had fallen at 30.5 s after the cut, just before, and its next one is at 61 s. Not shown: why only this run declared the tunnel dead early enough to matter (it was the only one inside a full run, so load is a candidate), and whether main does it too (0 of 4, too few to say).
**What would close it:** asking again at once when a packet from the peer's network arrives or the path loop sees the way out return, instead of waiting for the next growing gap; and a test that puts the router's return right after a try. The limit of the test stays at 45 s.

## 2026-11-24 — the network lab is not in CI

It takes about 9 minutes on an arm64 VM with 8 cores and needs root, `nft` and `/dev/net/tun`. GitHub's ubuntu runners have all three (the kernel-TUN test already uses `sudo unshare`). Not tried there; a busy runner may break the time limits. Start with `workflow_dispatch` and look at ten runs before it can block anything.

## 2026-11-23 — kernel TUN: what is still unproven

`TestKernelTunClose_ReleasesDeviceAndPort` (root, Linux) covers V, W and X. CI runs it in its own network namespace (`.github/workflows/ci.yml`); that step ran in CI for the first time on 2026-11-24 and passed; before that only the same commands in a local VM. **Not run:** the macOS half of patch 32 (needs root), and throughput of a Linux system-wide network with the device opened non-blocking (patch 31; the same thing wireguard-go does). **Action:** `iperf3` through a Linux system-wide network before and after, at the next dev deploy.

## 2026-11-23 — creating a network answers 201 although its lighthouse did not start

`CreateNetwork` only logs `[networks] failed to start lighthouse for …`. Seen on Linux because of V, which is fixed; any other cause (a port in use) still looks like success. **Action:** answer 500 and roll back, or show the network as "not running".

## 2026-11-23 — logs wanted from the MBP (the original trigger is not proven)

Why the certs ran out in that machine's case is inferred from released code, not from its logs. **Action:** before reinstalling, save `/var/log/hop-agent.log`, `/etc/hop-agent/*/{renewal-stuck,stuck-state,watcher-stuck}-*.txt`, `hop-agent status`, and `pmset -g log`.

## 2026-11-23 — reload the certificate in place instead of restarting Nebula

Every renewal (about every 12 h since the fix, 24 h before) closes the network and starts it again, which drops all tunnels, in-app connections and the web terminal's path. Nebula can swap certs through its config reload callbacks; we can't use it because `startNebula*` keeps `config.C` local. **Action:** keep the `config.C`, write the new cert, call `ReloadConfig`. Fall back to the restart if the reload is rejected. The test exists: `TestE2E_Lifecycle/force_renewal` pins today's behaviour with an open shell.

Source / context: `internal/client/renew.go::reloadNebula`, `internal/client/nebula.go`

## 2026-11-23 — do the macOS send bugs (I, J) explain earlier symptoms?

Idle tunnels dying, Phase G, the v0.10.35 keepalive, post-sleep "handshake sent" without a packet (patch 24), the 2026-05-14 same-CGNAT relay stall. All fit; none was re-tested. **Action:** re-test with patch 28 on two Macs before working on any of them; consider removing workarounds that turn out to be for this.

Source / context: [[concepts/macos-udp-send]]

## 2026-11-23 — macOS code is never tested in CI

CI is ubuntu-only, so all Darwin Nebula patches and their tests, the macOS part of the agent, and the 44 desktop `#[test]`s never run. `go test ./...` also skips vendored tests. **Action:** add a macos-latest job running `go test -mod=vendor -race ./internal/client ./internal/api github.com/slackhq/nebula/udp` and `cargo test`. Add govulncheck and `-race` on ubuntu. Fix first: `TestPeerCache_SaveSkipsWhenUnchanged` (flaky, about 1 in 40). On Windows the client's tests don't even compile: `renew_sleep_test.go` uses `syscall.SIGSTOP` without a build tag.

## 2026-11-23 — authorization: every network member gets a shell on every node

`requireNode` (`internal/api/proxy.go:68`) only needs `CanView`, so any member can open a terminal and run commands, as root on an installed service. Capabilities default to all (`terminal,health,forward`) and the agent never checks them. `NodeExec` audits no command. The mesh firewall is flat. Pinned by `TestE2E_MemberShell`. **Action:** terminal and exec need admin or an explicit per-node grant; check the capability in the agent too; audit the command.

Source / context: `docs/wiki/raw/evaluation/2026-11-23-code-and-ssh-review.md`

## 2026-11-23 — updates are unsigned; the install scripts still fail open

The agent's own update now refuses when the checksums can't be fetched, read or don't list the file (`TestE2E_Update`). Still open: the install scripts skip an empty checksum (`internal/api/distribution.go:502`, `scripts/install.sh:83`: `… SHA256SUMS … || true`); the checksums come from the same place as the binary; nothing is signed; `tauri.conf.json:42` ships `PLACEHOLDER_REPLACE_AT_RELEASE_TIME`. Whoever controls the control plane's downloads gets root on every node. **Action:** make the scripts fail closed (small), then sign with minisign or cosign and pin the key in the agent.

## 2026-11-23 — hardening notes from the evaluation

- Browser shell uses a predictable `$TMPDIR/hop-shell` (`cmd/agent/shell_unix.go`).
- Mesh DNS binds `0.0.0.0` (`internal/mesh/dns.go:88`).
- `encryption.key` sits beside the database (`cmd/server/main.go:107`).
- `features.md:36` says "end-to-end encrypted"; the browser terminal is plaintext in the control plane's memory.

## 2026-11-23 — agent loose ends

- In-app mode: `warmTunnel`, `warmPeersFromHeartbeat` and `probePeersOnce` dial through the OS, so they never reach the mesh.
- A lost `nebula.yaml` is never regenerated, and some writers use a non-atomic `os.WriteFile`.
- `renew.go` (heartbeat `networkId` path) loads and saves its own copy of the enrollment registry: a concurrent save can be lost.
- An agent started with a cert past half its life restarts Nebula right after starting it.
- The error "another hop-agent on this device is using the network port" is shown for our own orphans too.

## 2026-11-18 — `renewNow` retry loop can mask a stuck renewal from the Phase P watchdog

`renewNow` retries up to 12 times with backoff reaching 30 min, stamping `lastRenewalActivityAt` on each attempt. While it is retrying (for example on 429s), the watchdog sees "activity" even though no renewal succeeds, and can't tell busy-failing from healthy. Renewals now have about 12 h of margin and the recovery loop covers the expired-cert end state, but a watchdog predicate on *last successful renewal age* versus cert NotAfter would catch it hours earlier. **Action:** consider stamping success separately, and alarm when `NotAfter - now < 2h` with no success in the last N attempts.

Source / context: `internal/client/renew.go::renewNow`; [[concepts/watchdog]]

## 2026-11-18 — `/local/renew` (force renew) returns 503 when there is no live instance

`handleForceRenew` needs an instance, so it can't help exactly when it's most needed (an enrollment down because of its cert). The Phase HH recovery loop makes this mostly moot, but it is a cheap improvement to fall back to `fetchAndInstallCert` + `connectAndSupervise` when there is no instance.

Source / context: `internal/client/local_api.go::handleForceRenew`

## 2026-11-18 — npm dev-tooling vulnerabilities need major upgrades

After `npm audit fix`, the dashboard frontend's **production** dependencies are clean, but its dev/build tooling still has 13 issues (8 high, 5 low) in @sveltejs/kit, vite, vite-plugin-svelte, svelte-check, picomatch, fdir, tinyglobby, cookie and bits-ui. The desktop client has 7 high (appdmg, image-size, vite, svelte-check, ...). All are build-time only, but they need `npm audit fix --force`-class **major** upgrades (SvelteKit / vite majors), which should be a dedicated, tested change. Cargo: 0 vulnerabilities; unmaintained/unsound warnings only (glib 0.18 via Tauri's gtk-rs, chrono, proc-macro-error, zbus/zvariant), which clear when Tauri moves its gtk stack.

## 2026-05-14 — Nebula relay path silently fails for same-CGNAT peers across different private subnets

Mini + MBP on home network both behind same Yettel BG CGNAT (`46.10.240.91`), but on DIFFERENT private LANs (mini `192.168.23.x`, MBP `172.17.132.x`). Direct P2P fails due to hairpin NAT (well-documented in CLAUDE.md Discovery Log `preferred_ranges`). Relay fallback through the lighthouse (`use_relays: true, relays: [10.42.1.1]`) SHOULD bridge them — but in this incident relay also timed out: MBP's log shows continuous `Attempt to relay through hosts relays=[10.42.1.1]` + `CreateRelayRequest` followed by `Handshake timed out` for vpnAddr 10.42.1.7. Manual `launchctl kickstart` on BOTH agents was the only recovery — kickstarting one alone wasn't enough. **Three hypotheses to test next session**: (1) Lighthouse's UDP source-addr cache for the mini was stale because the Nebula-UDP flow timed out on CGNAT while the HTTPS heartbeat channel stayed warm. Trace lighthouse logs on hopssh.com around 09:39 (2026-05-14) to see what destination addr it used when forwarding relay packets. (2) Yettel CGNAT hairpin restriction applies to UDP-from-lighthouse-public-IP-back-to-Yettel-internal-host too (single-NAT-but-three-hop relay). Compare with a peer on a different ISP. (3) Nebula's relay implementation has a stale-endpoint retry bug — read `vendor/github.com/slackhq/nebula/relay.go` flow. **Action**: when this recurs, capture the lighthouse-side log first (server-side `journalctl -u hopssh` filtered for `relayFrom=10.42.1.6 relayTo=10.42.1.7` or vice versa). The agent-side artifacts (kickstart logs + nebula.yaml + lighthouse: replaced static host map entry lines) are preserved in this session's transcript for reference.

**2026-11-24:** the same layout works in [[concepts/netlab]] (`TestSameProvider`: one provider that has both homes behind one address, forgets after 30 s and does not let them meet there; the relay answers within 0.4 s, also after two quiet minutes). So the layout is not the cause. *Likely*, not proven: what was found there since — after the server restarts, a relayed connection was dead for 72 to 84 s and both sides went on sending; and one packet from a stranger could close a relayed tunnel (patch 33). Both are fixed on the branch that has the lab. Look again if it happens with a release that has them.

Source / context: plan-of-record (`~/.claude/plans/we-re-starting-phase-valiant-wave.md` from this session); Discovery Log `preferred_ranges` note; v0.10.22 Phase G (HTTPS-distributed self-endpoints); v0.10.27.1 Layer 4 endpoint-probe disabled.

## 2026-05-07 — which vendor Nebula call wedged in the May 7 watcher incident

The watchNetworkChanges goroutine deadlocked at `cmd/agent/nebula.go:272-273` — either inside `ctrl.RebindUDPServer()` or `ctrl.CloseAllTunnels(true)`. We couldn't determine which from logs alone (no CRITICAL/PANICKED, no goroutine dump pre-fix). Phase DD's watchdog will produce a forensic goroutine dump on the next occurrence (`<configDir>/<name>/watcher-stuck-<ts>.txt`) — that dump will pinpoint the lock contention and inform whether to file an upstream Nebula issue or extend our vendor patches. **Action**: when the next watcher-stuck dump appears in the wild, walk the goroutine stack and identify the held mutex.

Source / context: [[incidents/2026-05-07-mbp-watcher-wedge]]

## 2026-05-07 — DNS resolution flap during MBP network changes

`dial udp4: lookup hopssh.com: no such host` fires intermittently during network-change events on MBP (observed at 17:18:56, 18:00:45 today; also in earlier sessions on 2026-05-05, 2026-05-06). Heartbeat goroutine recovers within seconds (separate goroutine, separate DNS resolution). Could be Go runtime cgo_resolver cache invalidation during interface flap, or macOS DNSExtensionConfig transitioning. Unrelated to the wedge itself (DNS error returns cleanly), but worth understanding if it correlates with the May 7 wedge timing or recurs as a primary failure mode. **Action**: low priority; investigate if a DNS-flap-driven incident appears.

Source / context: [[incidents/2026-05-07-mbp-watcher-wedge]] § "Open questions"

---

## Resolved

## 2026-11-18 — upgrade vendored Nebula v1.10.3 → v1.11.2 (resolved 2026-11-26, not released)

Upstream releases since our pin are v1.11.0, v1.11.1, v1.11.2 and v1.12.0 (v1.12.0 needs Go 1.27.3 and a gvisor jump of about 2.5 years, so it is a separate project). v1.11.2 brings:

- two firewall bypass fixes with no GHSA (IPv6 extension-header parsing #1789; SCTP/GRE classified as TCP/UDP #1840), plus nonce-reuse and relay replay-window hardening;
- a relay re-establishment fix (#1805), the same symptom class as the 2026-05-14 relay stall above;
- hostmap/roam race fixes (#1752, #1755, #1773, #1788, #1790);
- `Control.Wait` (#1794).

Patch impact:

- **Drop:** 01 (fixed upstream by PR #1661) and 03.
- **Probably drop:** 02, pending a test.
- **Rewrite:** 08 against upstream's `ListenOut` flush callback.
- **Real rework:** the macOS perf stack (04, 06, 07, 09, 12, 17, 19, 24), the control API (11, 20–22, which have no upstream equivalent), 15 and 26.
- **Keep as-is:** 14 and 25 (upstream `Expired()` still has no clock-skew tolerance).

Known advisory GHSA-69x3-g4r3-p962 is already patched in our v1.10.3 (and only affects P256; we use Curve25519).

**Action:** make `patch-vendor` fail loudly → bump → re-port patches one by one → run the full test suite plus the Phase E multi-host smoke test (mini + MBP + Linux + Windows), with cellular and WiFi perf A/B against `results/perf-20260425-003810/`.

Source / context: `docs/wiki/raw/nebula-upgrade/2026-11-18-upstream-survey.md`

**Resolved 2026-11-26** (branch `upgrade/nebula-1.11.2`, not merged, not released). The 34 patches became 10 files, one theme each (`patches/README.md`). Dropped: 01, 02, 03, 12, 13, 27, 30, 37. **Not** dropped although upstream looked the same: 31. Upstream's close wakes the reader and returns; on the Mac all was green, in the Linux VM the port or the device was still taken (kernel-TUN close test 4 of 5 red, userspace close every time). With waiting restored: 10 of 10. Tested: suite, `-race` on the Nebula packages and `internal/client`, `internal/mesh`, upstream's own e2e, `TestE2E_Upgrade` from v0.11.28, the lab (see `log.md`). Not done: Windows on a device, cellular.

## 2026-11-18 — vendor patch 12 never applies (silently, even in CI) (resolved 2026-11-26)

**2026-11-23: its code is in no clean build.** `listenInWorker` and `inBatch` exist only in the test patch 13 adds, so `go vet github.com/slackhq/nebula` fails, and release and Docker builds use the same steps. Published macOS binaries very likely never had the pipelined `listenIn` (not checked on a binary), and the 2.7× downlink numbers in `docs/performance.md` and `docs/macos-pipelined-listenin.md` describe dev builds. **Decide:** re-measure with and without it on the current tree, then port it properly or drop 12 + 13. Patch 28's `interface.go` hunk is made against a tree without 12.

Patch 12 fails on a blank-line context mismatch and `make patch-vendor` carries on: the Makefile runs each `patch` with `|| true` and then deletes `*.rej`, so **any** failed patch is silently ignored, in CI too. Either refresh patch 12 against vendored Nebula v1.10.3 or fold it into the Nebula upgrade below. Make `patch-vendor` **fail** on any patch that doesn't apply, and do that *before* the upgrade.

**2026-11-24: the Mac and the release build get different trees.** GNU patch also refuses hunk 3 of patch 19, which adds `applyOSThreadHints()` to the function patch 12 would have added. Apple's `patch` accepts it in the wrong place, `listenIn`, which then makes the same call twice. Nothing else differs and the call does nothing off macOS, so v0.11.27 is what was tested. But a test on a Mac does not prove that a patch applies on Linux, and "it applied" does not prove it went to the right place: check with `patch` of both kinds, with `--fuzz=0` where possible (2, 19 and 26 apply only with fuzz).

Source / context: `patches/`, `Makefile` `patch-vendor` target. `scripts/check-nebula-patch.sh` does not exist despite older docs.

**Resolved 2026-11-26** (branch `upgrade/nebula-1.11.2`): `make patch-vendor` stops at a patch that does not apply, and says "already applied" for one that is. Patches 12 and 13 are gone: the pipelined `listenIn` was in no build, so what was measured and shipped is the batch read of patch 07, which is kept (`read-loop.patch`). One thing learned on the way: Apple's `patch -R --dry-run` succeeds on a patch that is *not* applied, so the forward try has to come first.

## 2026-11-23 — before releasing the server: three checks (resolved 2026-11-24)

1. The MBP is **not** listed. No device is: the sweep (L) deleted the last two on 2026-08-07, seven days after they went quiet. Every device has to leave and enroll again. `HOPSSH_NODE_PRUNE_AGE_HOURS=0` is set in production since 2026-11-24 (log: `auto-prune disabled`), so nothing more is deleted before the new server runs.
2. The data volume is a local ext4 disk on the one worker, not NFS.
3. The database was copied with SQLite's online backup (`integrity_check: ok`; 3 orphan rows; `journal_mode: delete`, which confirms N on production).

## 2026-11-23 — what drains the production login rate limiter? (resolved 2026-11-24)

Devices that the server itself had deleted (L). A released agent that gets 401 gives up and is started again; one such device sent 3 renewals and 3 heartbeats about every 14 s. Between 2026-11-06 and 2026-11-23 the server answered 105,260 heartbeats and 102,721 renewals with 401 and 347,149 more with 429. Other devices behind the same address share that bucket. That it was this address is inferred, not shown. The fixed agent asks once an hour after a 401, and renewals have their own limiter.

## 2026-11-23 — the E2E tests had not run on Linux (resolved 2026-11-23)

Run in a local Fedora VM (arm64, kernel 6.12). Six tests failed because of one product bug (V, the UDP port outlives the network); a new root test for kernel TUN found two more (W, X). Fixed by vendor patches 31 and 32; everything passes there since. See [[incidents/2026-11-18-mbp-all-certs-expired]] V, W, X.

## 2026-11-18 — `TestTicker_SurvivesSIGSTOP` has a data race (resolved 2026-11-23)

Its tick counter is an atomic now; `internal/client` and `cmd/agent` pass with `-race`. Running the E2E tests with `-race` found one in the product (U).

## 2026-11-18 — does production pass `--trusted-proxy` behind Traefik? (resolved 2026-11-18: yes)

`oci_nomad_cluster/jobs/hopssh.nomad.hcl` sets `HOPSSH_TRUSTED_PROXY = "true"`, so rate-limit buckets are per the address in `X-Forwarded-For`. **Corrected 2026-11-23:** this does not show that buckets are per household, nor what exhausted one; see "what drains the production login rate limiter?" above. Caveat: `internal/auth/ratelimit.go::clientIP` takes the **leftmost** `X-Forwarded-For` entry. That is safe only while Traefik replaces client-supplied `X-Forwarded-For` (its default for untrusted sources). If a CDN or another proxy is ever put in front, revisit this, because leftmost-XFF becomes client-spoofable and the limiter can be bypassed.

## 2026-05-09 — Linux desktop client stuck onboarding + "Load failed" on Connect (resolved 2026-05-10)

Resolved in v0.11.19 (commit 10ff0c9). Root cause: `cmd/agent/main.go` skipped `c.Start` on the no-enrollments branch (fresh installs), but `client.StartLocalAPI` was always called — the local API's auto-connect handler then panicked on nil `c.runCtx` during `context.WithCancel`. Phase NN regression: pre-extraction the equivalent closure used the always-live `renewCtx`. Fix is two-layer (always Start before StartLocalAPI; defensive nil guard in connect). Live-verified on the aarch64 Ubuntu VM. See [[incidents/2026-05-09-linux-desktop-stuck-onboarding]] § Resolution for the captured panic + lessons.
