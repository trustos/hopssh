# macOS Screen Sharing High-Performance: first-click bug

> **Status (2026-04-20): SUPERSEDED.** The SC-registration fix plan
> originally in this doc was investigated across three implementation
> iterations and proven empirically infeasible. Authoritative entry
> lives in `CLAUDE.md` Discovery Log → "macOS Platform" (2026-04-20).
> This file now captures the **honest root cause** and the **realistic
> workaround options**. Do not re-attempt SC-based approaches.

## Symptom

After `hop-agent restart` (or any other utun reset) on macOS, the next
Screen Sharing Connect click to a peer with High Performance mode
enabled fails with:

> This Mac was unable to start a High Performance connection to
> "<peer>". Change the screen sharing type to standard and try again.

A retry ~10 seconds later succeeds. Steady-state HP works fine; the
failure is specifically the first click after the utun has just come
up.

## Root cause (verified 2026-04-20)

The bug lives at **Network.framework**, not SystemConfiguration.

Log evidence captured during a failing attempt on the MacBook, with a
fresh `hop-agent restart` and a fresh avconferenced:

```
symptomsd:  -[NWInterface initWithInterfaceName:]
            nw_interface_create_with_name(utun11) failed
            nw_interface_create_with_name(utun0) failed

avconferenced:  -[VCTransportSessionSocket
                 initializeInterfaceTypeWithSocket:]:384
                 Not setting unexpected transport type 0

avconferenced:  state[... localInterfaceType=(null)
                remoteInterfaceType=(null) ...]

avconferenced:  _RTPTransport_ReinitializeStream  (audio first)
                checkRTCPPacketTimeoutAgainstTime: nan   (1st)
                ...nan... (2nd) (3rd) (4th)
                _RTPTransport_ReinitializeStream  (video, 5 s later)
```

avconferenced uses the **`nw_interface_*` family** from
Network.framework to classify a socket's local interface type. For our
userspace utun (opened via `AF_SYSTEM` + `UTUN_CONTROL_NAME`), that
call returns `nil`. avconferenced logs `localInterfaceType=(null)` and
falls through to a cold-start path where the first encrypted video
RTP packet takes ~5 s to arrive — longer than the internal RTCP
watchdog (4 strikes), which aborts the session before SRTP init
completes.

### Why `nw_interface_create_with_name` fails for our utun

`ifconfig -v` reveals the kernel-level delta between Tailscale's utun
(which works 3/3 for HP) and ours:

| field | Tailscale utun12 | hopssh utun (any) |
|---|---|---|
| `xflags` | `4010004<NOAUTONX, IS_VPN, INBAND_WAKE_PKT>` | `4000004<NOAUTONX, INBAND_WAKE_PKT>` — **no IS_VPN** |
| Skywalk NetIf agent | registered | **absent** |
| Skywalk FlowSwitch agent | registered | **absent** |
| NetworkExtension VPN agent | registered | **absent** |

The `IS_VPN` extended flag and the three kernel interface agents are
set automatically by `NEPacketTunnelProvider` during tunnel bring-up.
They are NOT settable from a userspace root daemon:

- `IFXF_IS_VPN` (0x00010000) requires the private `SIOCSIFXFLAGS`
  ioctl, which is gated in the kernel by the
  `com.apple.developer.networking.networkextension` entitlement.
- Skywalk agent registration is a closed subsystem
  ([newosxbook.com Darwin Networking chapter]) — no public API, no
  private-but-callable API.

### Why the SC-registration approach doesn't help

avconferenced doesn't read `SCDynamicStore` Setup/State keys to
classify interfaces. It calls Network.framework's `nw_interface_*`
APIs, which read directly from networkd / kernel interface agent
state. Writing SC keys (even perfectly matching Tailscale's live
shape with `PrimaryRank: Never`, `ServiceIndex: 100`, etc.) has zero
effect on `nw_interface_create_with_name`'s outcome.

Three iterations were implemented and empirically failed:

1. **SCPreferences writes** (v0.10.3-dirty pass 1) — keys landed in
   the prefs file but configd doesn't propagate services not in the
   current location's Set; HP stayed broken.
2. **SCDynamicStore direct writes** (pass 2) — replicated the manual
   `scutil set` path; also writing `Setup:.../IPv4` with
   `ConfigMethod: Manual + Router=<self-IP>` broke mesh routing
   (configd reconfigured the utun as a /24 broadcast subnet).
   Regression fixed by dropping the Setup IPv4 key; HP still broken.
3. **Minimal keyset matching Tailscale's live shape verbatim**
   (pass 3) — Service + Interface + top-level State + State IPv4 +
   ServiceOrder all present. HP **identically** broken.

The `SCNetworkInterfaceForceConfigurationRefresh()` public API was
also tested as a Hail Mary — `SCNetworkInterfaceCopyAll()` doesn't
return our utun, so there's no `SCNetworkInterfaceRef` to call
refresh on.

### Why this is universal across userspace macOS VPNs

Every userspace VPN daemon that opens `/dev/utun` without NE faces
the identical limitation:

| Project | Interface | HP first-click status |
|---|---|---|
| hopssh (this project) | /dev/utun | 0/3 (documented) |
| wireguard-go | /dev/utun | 0/3 (inferred, same code path) |
| Tunnelblick / OpenVPN | /dev/utun | 0/3 (inferred) |
| strongSwan | /dev/utun | 0/3 (inferred) |
| NetBird | /dev/utun | 0/3 (inferred) |
| ZeroTier | **feth** (fake Ethernet, L2) | ~2/3 (architecture workaround) |
| Tailscale | **NEPacketTunnelProvider** | 3/3 (NE bundle) |

## Realistic workaround options

### A. User-facing mitigation (no code): retry after 10 seconds

The ~10-second window where a retry succeeds is empirically observed
but unexplained in any public docs. Possible causes: kernel interface
enumeration latency, avconferenced's internal backoff timer, or
configd notification propagation delay. **Document this as "known
limitation, retry HP after 10 s"** in user-facing docs.

Cost: zero code. Works for all existing users. Ship today.

### B. `feth` interface via Nebula vendor patch (partial fix)

ZeroTier uses `feth` (fake Ethernet pair) instead of utun and gets
~2/3 HP success per the original empirical study. `feth` presents as
a layer-2 Ethernet interface, which `nw_interface_create_with_name`
accepts (it doesn't need the IS_VPN kernel flag because it classifies
as `wired`).

Effort:
- Vendor patch on Nebula's `tun_darwin.go` to open `feth`
  (`SIOCSIFCREATE2` with kind="feth") instead of utun
- Handle layer-2 framing (Nebula operates at L3 — need to bridge)
- Manage feth pair + assign MAC + routing
- Roughly 200-400 lines; risk of subtle correctness bugs around
  multicast, ARP, MTU

Trade-off: gains HP first-click reliability; adds L2 complexity and
Nebula vendor diff. Still not 3/3.

### C. NEPacketTunnelProvider — the "right" fix (large effort)

Ship a proper macOS GUI app bundle that contains an
`NEPacketTunnelProvider` system extension. This is how Tailscale
achieves 3/3 HP success.

Requirements:
- Apple Developer ID (annual cost; legal entity)
- NE entitlement request from Apple (typically granted for VPN apps
  on review)
- Swift/ObjC container app + NE extension target
- Code signing infrastructure + notarization
- Major refactor: agent becomes an NE extension's packet handler
  instead of a root LaunchDaemon daemon (IPC via XPC)
- Months of work including Apple's review process

Future strategic option, not a current session's scope.

## Testing protocol (for future workaround attempts)

If someone attempts option B or C, the verification protocol is:

1. `sudo hop-agent restart` on the client Mac (fresh utun).
2. Wait 10 s for tunnel warm-up.
3. Connect via Screen Sharing → `<peer-mesh-IP>` with HP enabled.
4. Expected with fix: first-click succeeds (no error dialog). Repeat
   3 times to confirm stability.
5. Compare against baseline: without fix, 0/3 on this machine +
   network combination.
6. Verify `ifconfig -v <iface>` shows `IS_VPN` in `xflags` OR (for
   feth) `en-type` classification.
7. Verify `sudo log show --last 2m --predicate 'process ==
   "avconferenced"' | grep "transport type"` does NOT show `Not
   setting unexpected transport type 0` during the connection
   attempt.

## References

- CLAUDE.md → Discovery Log → macOS Platform (2026-04-20 entry,
  authoritative)
- `apple-oss-distributions/xnu` bsd/sys/sockio_private.h
  (SIOCGIFXFLAGS = `_IOWR('i', 206, struct ifreq)`; SIOCSIFXFLAGS
  not present in public kernel source)
- [newosxbook.com Darwin Networking chapter] (Skywalk subsystem)
- [ZeroTier: How ZeroTier Eliminated Kernel Extensions on macOS]
  (https://www.zerotier.com/news/how-zerotier-eliminated-kernel-extensions-on-macos/)
- [Apple Developer Docs: NEPacketTunnelProvider]
  (https://developer.apple.com/documentation/networkextension/nepackettunnelprovider)

---

## Update 2026-04-24 — MTU is NOT the HP discriminator (empirical A/B)

Captured `sudo log stream --predicate 'process == "avconferenced" OR
process == "screensharingd" OR process == "ScreenSharing"'` on the
MBP (client) AND Mac mini (server) during deliberate HP attempts at
both `TunMTU=1380` (the v0.10.16/.17 default) and `TunMTU=1420`
(reverted in v0.10.18). User reported: "HP works at 1420, doesn't at
1380." Goal of the capture: see if the avconferenced classification
path actually flips on MTU.

**Empirical signature side-by-side:**

| Signature                                 | MTU 1380 | MTU 1420 |
|---|---|---|
| `localInterfaceType`                      | `(null)` | `(null)` |
| `remoteInterfaceType`                     | `(null)` | `(null)` |
| `Not setting unexpected transport type 0` | 4× | 4× |
| `vcMediaStreamRecommendedMTU`             | `0`      | `0`      |
| `Last RTCP packet receive time:nan`       | 4× | 4× |
| Screen Sharing → `gets standard mode`     | yes (3×) | yes (2×) |

**HP mode does NOT engage at either MTU.** Both fall back to Standard
mode (RFB over TCP through the mesh) for the same documented reason:
avconferenced cannot classify our raw utun (no `IFXF_IS_VPN` xflag,
no Skywalk/NE agents) so `nw_interface_create_with_name("utun5")`
returns nil → `Not setting unexpected transport type 0` → MTU never
even gets read (`vcMediaStreamRecommendedMTU = 0` at both runs).

**The real difference between 1380 and 1420 in user UX is
Standard-mode jitter tolerance**, not HP classification:
- Standard mode is RFB over TCP. Each framebuffer chunk fragments
  into N inner-MTU packets. Loss/delay of any one stalls the whole
  chunk via TCP backoff.
- At 1380: more packets per chunk (≈1.5× vs 1420 for typical chunk
  sizes) → more chances for jittery WiFi to delay one → visible
  stalls.
- At 1420: fewer packets per chunk → fewer stalls → "managed to
  screen share."

**Counter-finding to the v0.10.16 changelog:** the WiFi LAN MTU
sweep that picked 1380 used iperf3 (raw UDP), which doesn't care
about per-packet jitter. Standard-mode RFB-over-TCP cares much
more. iperf3 was the wrong test for the actual screen-share workload.

**Action taken:** v0.10.18 reverts `TunMTU` to 1420 — gives up
~10 % WiFi LAN iperf3 throughput in exchange for usable Standard-mode
screen-share, which is the actual production workload for daily-driver
users.

**HP mode itself remains structurally broken on raw userspace utun**,
and only `NEPacketTunnelProvider` fixes that. MTU choice is a
Standard-mode-quality decision, not an HP-engagement one.
