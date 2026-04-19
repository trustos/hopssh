# macOS Screen Sharing High-Performance: first-click fix

## Problem

After `hop-agent restart` (or any utun reset) on macOS, the next
Screen Sharing Connect click to a peer fails with:

> This Mac was unable to start a High Performance connection to
> "<peer>". Change the screen sharing type to standard and try again.

A second click ~10 s later succeeds. High-Performance mode works
normally in steady state — the failure is specifically the first
click after the client's tunnel has just come up.

## Root cause (empirically isolated)

macOS `avconferenced` classifies network interfaces for its HP
video-pipeline setup using SystemConfiguration's Service registry
(`SCDynamicStore` + `SCPreferences`). When the peer's IP falls on an
interface that is **not** registered as a Network Service,
avconferenced takes a cold-start path where the first encrypted video
RTP packet from the peer takes ~5 s to arrive. The client's internal
RTCP watchdog aborts at 3 strikes (~5 s) — ~125 ms before SRTP
init completes — and the session is torn down before any video
frame has been received.

Evidence (from the failing attempt):
- Audio RTCP round-trips in <200 ms on the cold attempt → the tunnel
  itself is carrying traffic in both directions fine.
- Video stream's `RTPTransport_ReinitializeStream` fires at
  **5,238 ms** after stream init on cold, vs **136 ms** on warm.
- `VCMediaStream checkRTCPPacketTimeoutAgainstTime: Last RTCP packet
  receive time: nan` fires 4 times on video (audio is unaffected).
- `mediaStreamError delegate called. errorType 3 errorCode -1` is
  the user-facing abort.

The differentiator between hopssh's utun (fails) and Tailscale's
utun (works) is **not** the interface flags — both are identical
`POINTOPOINT, no BROADCAST, no ether`. It is the SystemConfiguration
registration. Tailscale's `NEPacketTunnelProvider` registers its
utun as a full macOS Network Service; a plain `/dev/utun` opened by
a root daemon without SC registration is not in `scutil --nwi`,
and avconferenced's classifier falls through to the cold path.

Verified empirically:
- hopssh raw utun: **0 / 3** HP first-click success
- ZeroTier feth: 2 / 3
- Tailscale NetworkExtension: 3 / 3
- hopssh utun + **manual scutil SC Service injection: 2 / 2** ✓

## Fix

From the macOS hop-agent (root, running via LaunchDaemon), register
a full Network Service entry for each enrolled network's utun via
`SystemConfiguration.framework`. No NetworkExtension bundle,
no developer entitlement, no feth driver — the public SC APIs are
callable from a root daemon.

## Minimum SCDynamicStore / SCPreferences keys (proven sufficient)

Per enrollment (one Service per network):

| Key | Value |
|---|---|
| `Setup:/Network/Service/<UUID>` | `{ UserDefinedName: hopssh-<network-name> }` |
| `Setup:/Network/Service/<UUID>/Interface` | `{ Type: Ethernet, DeviceName: <utunN>, Hardware: Ethernet, UserDefinedName: hopssh-<network-name> }` |
| `Setup:/Network/Service/<UUID>/IPv4` | `{ ConfigMethod: Manual, Addresses: [<mesh-ip>], SubnetMasks: [<mask>], Router: <mesh-ip> }` |
| `State:/Network/Service/<UUID>/IPv4` | `{ Addresses: [<mesh-ip>], DestAddresses: [<mesh-ip>], Router: <mesh-ip>, InterfaceName: <utunN>, ServerAddress: 127.0.0.1 }` |
| `Setup:/Network/Global/IPv4.ServiceOrder` | append `<UUID>` (read-modify-write) |

Skipped vs Tailscale:
- `Setup:/Network/Service/<UUID>/VPN` (NEProviderBundleIdentifier +
  code-sign DesignatedRequirement) — we cannot match this without
  an NE bundle, and the empirical test confirmed it is **not
  required** for the HP fix.
- `State:/Network/Service/<UUID>/DNS` — hopssh already does DNS via
  `/etc/resolver/*`; not needed for HP.

## Code changes

All Darwin-only; no change on Linux or Windows.

| File | Action | Notes |
|---|---|---|
| `cmd/agent/scnetwork_darwin.go` | **NEW** | CGo against `SystemConfiguration.framework`. Exports `scRegister(name, iface, ipv4, mask string, uuid string) error` and `scUnregister(uuid string) error`. Handles: SCPreferences lock/unlock, SCDynamicStore connect, apply, commit, ServiceOrder read-modify-write. |
| `cmd/agent/scnetwork_other.go` | **NEW** | Build-tag `!darwin`. `scRegister` / `scUnregister` no-op returning nil. |
| `cmd/agent/enrollments.go` | **MODIFY** | Add `ScNetworkServiceUUID string` to enrollment struct. Generate via `uuid.New()` on first save; persist in `enrollments.json` so it's stable across agent restarts. |
| `cmd/agent/instance.go` | **MODIFY** | In `meshInstance.close()`: after stopping Nebula, call `scUnregister(enr.ScNetworkServiceUUID)`. |
| `cmd/agent/nebula.go` | **MODIFY** | After `kernelTunMeshService.Start()` and IP assignment: call `scRegister(name=enr.NetworkName, iface=inst.ifname, ipv4=enr.NodeIP, mask=enr.NetworkMask, uuid=enr.ScNetworkServiceUUID)`. |
| `cmd/agent/renew.go` | **MODIFY** | In `reloadNebula` hot-restart path: `scUnregister` before closing old svc; `scRegister` with (potentially new) iface name after new svc starts. |
| `cmd/agent/leave.go` | **MODIFY** | After agent service is stopped: call `scUnregister(uuid)` to clean up the persistent `Setup:` entries. |

## Lifecycle

```
startMeshInstance:
  Nebula.Start → utun up → IP assigned → scRegister(name, iface, ip, mask, uuid)

reloadNebula (cert rotation hot-restart):
  scUnregister(uuid) → close old svc → start new svc → scRegister(name, NEW iface, ip, mask, uuid)

meshInstance.close / hop-agent leave:
  stop Nebula → scUnregister(uuid)
```

The `uuid` persists in `enrollments.json` so re-registration after
an agent restart re-uses the same Service UUID — idempotent, no
stale entries. `scRegister` should be idempotent too: if the Setup
entry for `uuid` already exists, update values rather than insert.

## Testing protocol

Manual end-to-end on the Mac mini ↔ MacBook pair:

1. `make dev-deploy` to push binaries to both Macs.
2. On MacBook: `sudo hop-agent restart`.
3. Verify `scutil --nwi` shows the hopssh utun in the interface list
   with `Router` set to the mesh IP.
4. Click Connect in Screen Sharing to the peer's hopssh IP. Expect
   HP first-click success (no error dialog). Repeat 3 times to
   confirm stability.
5. `hop-agent leave` a test enrollment; verify `scutil --nwi` no
   longer shows that utun and all `Setup:/Network/Service/<UUID>`
   entries are removed.
6. Full multi-network case: enroll on two networks, restart agent,
   verify both utuns appear in `scutil --nwi` with their respective
   service UUIDs. Each independently fixable / leavable.

Unit-testable pieces:
- UUID generation / persistence round-trip in `enrollments.json`.
- `scRegister` / `scUnregister` idempotency against a mocked SC
  session.

## Edge cases and risks

- **SCPreferences commit failures**: the framework can fail mid-
  transaction (rare; usually SIP / permissions / disk errors). On
  failure, log + continue — the agent must still boot and run
  Nebula. A failed SC registration degrades HP first-click to the
  known-broken state; everything else works.
- **`reloadNebula` new interface name**: `utun10 → utun11` kind of
  change. Update Setup's `Interface.DeviceName` and State's
  `InterfaceName` in the atomic re-register. Test with a forced
  cert-rotation path.
- **Stale Setup entries from a crashed agent**: on startup, before
  registering, scan `Setup:/Network/Service/*` for UUIDs owned by
  us (presence of `UserDefinedName` starting with `hopssh-`) that
  are no longer in any enrollment — remove them. This handles crash
  recovery.
- **Out-of-band user deletion**: user runs `sudo scutil` and removes
  our entries. Next heartbeat / network-change tick in
  `watchNetworkChanges` should re-register (add a lightweight "is
  my state still in SC?" check on ticker).
- **User reboots**: `State:` entries are wiped on reboot, `Setup:`
  entries survive. On first startup after reboot, re-create State
  and ensure the Setup entry + ServiceOrder are present; update if
  iface name differs.
- **Multi-network**: one Service + UUID per enrollment. `scRegister`
  is called N times; `ServiceOrder` read-modify-write needs to be
  atomic across concurrent registrations (mutex inside
  `scnetwork_darwin.go`).

## Out of scope

- IPv6 Service entry (nice-to-have; add after shipping the v1 fix).
- `/VPN` Setup entry with `NEProviderBundleIdentifier` — requires a
  System Extension bundle + Apple Developer ID. Deferred.
- Linux and Windows — they do not exhibit this bug; their Nebula
  setup is routed differently.
- Migrating to NetworkExtension-based transport entirely. Proven
  cheaper-fix-first; the NE path remains a future option.

## Effort estimate

**1 - 2 days** for the core Darwin implementation + deploy and
verify on both Macs. No dependency on new Go modules; `SystemConfiguration.framework`
is a standard macOS framework available via CGo.















Implement the macOS HP Screen Sharing first-click fix per
docs/macos-hp-screen-sharing-fix.md.

Start by reading:
- docs/macos-hp-screen-sharing-fix.md  (the plan itself)
- CLAUDE.md                            (coding principles, Discovery Log)

Before writing code, propose an implementation plan via ExitPlanMode.
Call out exact file edits and any open questions about SCPreferences /
SCDynamicStore CGo conventions.

Implementation constraints:
- Darwin-only (build tags: scnetwork_darwin.go + scnetwork_other.go).
- CGo against SystemConfiguration.framework.
- Reuse the parentCtx / stopWatcher / startWatcher lifecycle pattern
  already in cmd/agent/instance.go + cmd/agent/renew.go from the
  v0.10.1 goroutine-leak fix; scnetwork registration/unregistration
  hooks into the same places.
- Persist the Service UUID per enrollment in enrollments.json so
  it's stable across agent restarts; enrollments.json.bak pattern
  from v0.10.2 already in place.
- Idempotent register: if a Setup entry with our UUID already exists,
  update it in place rather than insert.
- Atomic ServiceOrder update (read-modify-write under SCPreferences
  lock).

Verify end-to-end on the paired hosts per the plan's test protocol.
Hosts are in .e2e-connections.md:
  MacBook:  ssh -i ~/.ssh/id_ed25519 yavortenev@192.168.23.18
  Mac mini: the current working dir host; mesh IP 10.42.1.7

Deploy with `make dev-deploy`. Expect HP first-click to go from 0/3
before the fix to ~3/3 after (matching Tailscale's NE baseline).
