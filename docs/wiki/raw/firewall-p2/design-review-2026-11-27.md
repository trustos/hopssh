# Firewall phase 2: two designs and two judges (2026-11-27)

Read-only design workflow over vendored Nebula v1.11.2 and the agent. Three designers were asked; one (`upstream-and-robust`) failed its output schema five times, so two designs were judged. Line numbers are as of commit c718780. Raw material, not the decision: the decision is in [[../../concepts/firewall-rules]], "Delivery and apply".

## Facts read from the code

**Firewall internals of vendored Nebula v1.11.2** (vendor/github.com/slackhq/nebula; read-only, nothing changed)

**(a) reloadFirewall, interface.go:462-510**
- Returns early unless `HasChanged("firewall")` is true or the cert's unsafe networks changed (471-476). The unsafe-network path rebuilds from whatever `c.Settings` holds.
- `NewFirewallFromConfig` runs before any lock (478). On error it logs and returns, keeping the old firewall (479-482). `ReloadConfigString` still returns nil.
- Holds `conntrack.Lock()` for the rest of the function (485-487), so `inConns` callers wait during the swap.
- Copies from the old object only `Conntrack` (499, only if the new rulesVersion is non-zero) and `rulesVersion+1` (489). Rule tables, timeouts, reject flags, networks, rule text and defaultLocalCIDRAny all come from the new build.
- The TimerWheel lives inside Conntrack, so its tick and size are fixed at boot (timeout.go:83). Timeouts above the boot-time max are clamped (timeout.go:168-170) and `evict` re-arms from `conn.Expires` (firewall.go:619-626). Nothing expires early.
- On rulesVersion wrap to 0, conntrack is not carried over (492-497) and every flow is re-evaluated.
- `Destroy()` is empty (firewall.go:491-493).
- Open flows are not swept at reload. `inConns` re-checks a conn only when `conn.rulesVersion != f.rulesVersion` (527), against the table chosen by `conn.incoming` (530-532), which is the direction that opened the flow.
  - Failing: the conn is deleted (545) and `Drop` matches the current table (464-473). No match means droppedNoRule.
  - Passing: rulesVersion is updated (559) and expiry refreshed (562-569).
- A removed rule therefore ends a flow at its next packet, either direction. Both directions share one key (local/remote address and ports; outside.go:367 orients inbound packets). A restored rule re-admits the next packet (no conn, so table match then addConn at 476). Conntrack alone does not keep a flow dead; any longer stall is TCP above it (not measured here).
- The routine-local cache (firewall.go:506-510, 573-575) is checked before rulesVersion and is not reset on reload. It is active only when `firewall.conntrack.routine_cache_timeout` > 0 (main.go:110-116: default 0, 1 s when routines > 1). The agent renders `routines: 1` (internal/nebulacfg/defaults.go:193), so it is off.

**(b) Readers and captures**
- Reads of `f.firewall`: inside.go:75 (Drop), 91 and 107 (reject flags), 228 (Drop in sendMessageNow); outside.go:521 (Drop); interface.go:471, 484, 589. Writes: interface.go:189 (constructor) and 502 (reload).
- Each read is a fresh load, so one packet can call Drop on one firewall and read reject flags from another after a swap.
- The only long-lived holder is `Interface.firewall` (interface.go:62). main.go:59 builds it, main.go:228 passes it to InterfaceConfig, and nothing else keeps a reference.
- The handshake cache stores the method value `f.sendMessageNow` (inside.go:57), which binds `*Interface`, not the firewall. Queued packets are therefore checked against the firewall current at send time (inside.go:228).
- ConntrackCacheTicker (firewall/cache.go) holds no firewall. Control, lighthouse and handshake code hold none.
- Straddle: a packet that loaded the old pointer before the swap is judged by the old rulesVersion and tables. It can lower `conn.rulesVersion` (559) or delete the conn (545). The next packet corrects it.

**(c) Mutation after construction**
- Construction only: InRules/OutRules and the rules text (AddRule, firewall.go:252-299), timeouts (174-176), reject flags (216-236), routable/assigned/unsafe networks (154-165), defaultLocalCIDRAny (214), metrics. Read without locks.
- Conntrack (firewall.go:81-86), guarded by its embedded mutex: Conns and TimerWheel are touched in inConns (512-571), addConn (594-606), evict (caller holds the lock, 611) and EmitStats (496-499). conn.rulesVersion and Expires are written under the lock (559, 564-568, 602-604).
- Firewall.rulesVersion: set once per object at 489, before publication, then read without a lock.
- Firewall.Conntrack pointer: replaced only at 499.

**(d) NewFirewallFromConfig versus the exported pair**
- NewFirewallFromConfig adds to NewFirewall + AddFirewallRulesFromConfig: a CertState (panics if none, 196-203), conntrack durations 12m/3m/10m (205-209), default_local_cidr_any (214), and inbound/outbound action (216-236). Action values other than reject or drop only warn and mean drop.
- It returns errors only from AddFirewallRulesFromConfig, outbound first (238) then inbound (243). For the same cert and text, the exported pair rejects the same rules, with two exceptions:
  - Durations are not checked. GetDuration returns the default for unparseable text (config.go:295-302). `udp_timeout: 0s` gives tmin=0 (firewall.go:148-152) and NewTimerWheel divides by it (timeout.go:83). That panics, and a negative value panics in make(). A validator that uses default timeouts accepts what reloadFirewall panics on.
  - defaultLocalCIDRAny is unexported (firewall.go:68). It changes local_cidr semantics (899-909), not validity.
- Rejected by AddFirewallRulesFromConfig (319-411): code and port together (343); no host, group, cidr, local_cidr, ca_name or ca_sha (347); proto other than any/tcp/udp/icmp (362-381); a missing port on any/tcp/udp (parsePortValue on "", 1108-1117); bad cidr or local_cidr (386-398); range start above end (656-657).
- Accepted and wrong: port 0 or "0-100" means any port (1091-1093; PortAny is 0, firewall/packet.go:18). Group, host, ca_name and ca_sha are never checked against the cert or CA pool (AddRule stores strings; matching at 755-766 and 859-882). Bad action strings only warn. A port on icmp is ignored with a warning (272-274, 371-378). "1-65535" allocates 65,535 FirewallCA entries per table (660-671), with no cap.
- Panics, not errors, in convertRule (944-1009): `group: []` (979, `v[0]`), `groups:` with null (985, `reflect.TypeOf(nil).Kind()`), and a non-string item in `groups` (990). Nothing on the reload path recovers them.
- Side effects: convertRule writes into the config map (979). Validate on a scratch config.C (config.NewC plus LoadString, config.go:33-38, 64-73). AddRule logs every rule at Info (294-296), so pass a discard logger.

**(e) Upstream after v1.11.2**
- Not checkable from vendor.
- The repo survey (docs/wiki/raw/nebula-upgrade/2026-11-18-upstream-survey.md) lists these v1.12.0 items, unverified here: non-string or empty groups crash fixed (138, #1912); conntrack timeout ≤ 0 crash fixed (139, #1984); group YAML typing tightened (145); RegisterReloadCallback signature change (150).
- The survey's v1.11.x items are already in vendor: cert-unsafe firewall reload (51; interface.go:469-471) and the inbound/outbound action fix (60; firewall.go:216-236).
- firewall-rules.md:121 says v1.12.0 keeps the plain pointer write. Not verified here.

----

**Facts only. No repo file was written. One `git status` ran early, which can only refresh `.git/index` stat data. Scratch trees (pristine, forward, reverse) were made in a temporary directory.**

**(a) Patches and the reload area** (line numbers in the patched vendor tree)

- `interface.go`: only `read-loop.patch`. Hunks cover 7-17, 91-101 (includes the `inboundObserver` atomic.Pointer at l.98), 314-322, 346-372 and 374-426. Not touched: field `firewall` l.62, `reloadFirewall` l.462-510 (write l.502, reads l.471 and l.484), `emitStats` read l.589. No overlap.
- `outside.go`: only `paths.patch`. Hunks cover 132-156, 197-203, 280-286, 290-325, 342-360, 579-593. The read at l.521 (in `handleOutsideMessagePacket`, l.511) is outside every hunk. No overlap.
- `inside.go` (l.75, 91, 107, 228), `firewall.go` and `config/config.go`: no patch touches them.
- `control.go`: only `control-api.patch` (hunks 9-15, 65-72, 218-232, 397-404, 433-549). The `Control` struct (l.45-60) is untouched.
- The 11 patches name 30 distinct paths, and none is in two patches.
- Any change to the field, `reloadFirewall`, or the readers lands in `interface.go` (owned by read-loop) and `outside.go` (owned by paths), and the `inside.go` readers have no owner. Keeping to the README rules would need either one new patch that co-owns `interface.go` and `outside.go` (breaks "no file in two patches") or hunks added to read-loop and paths (breaks "one theme per patch").

**(b) Applying**

- `Makefile:26-30` (`vendor`): `go mod tidy`, `go mod vendor`, then `patch-vendor`. `patch-vendor` (l.33-44) loops `patches/*.patch` in glob order. Each patch: `patch -p1 --forward --dry-run` in `vendor/`, then apply; else `-R --dry-run` gives "already applied"; else exit 1.
- Nothing checks independence or order. Scratch test: each patch applies alone to pristine v1.11.2 (plus the two gvisor files). Glob order and reverse order give identical trees (`diff -rq` empty). All 30 touched files match the repo's `vendor/` byte for byte. Forward dry-run on the patched tree exits 1 for every patch, so re-runs take the reverse branch.
- Order matters only if two patches touch one file: the second is matched against the first's output (offsets and fuzz tolerate line shifts, not changed context).
- `internal/mesh/lighthouse_log_test.go:48-65` only checks that `relay_manager.go` contains patch 26's text. CI runs `make setup` (`.github/workflows/ci.yml:29,71`). `preflight` is `vet test test-race` (`Makefile:110`).

**(c) Reload callbacks**

`ReloadConfigString` (`config/config.go:175-195`) holds `reloadLock` (l.176-177) and runs `LoadString`, which sets `c.Settings` (`parseRaw`, l.381) before any callback runs. Callbacks are `func(*C)` with no return value (l.75), so the call returns nil even when a callback fails. Only empty text or a YAML parse error is returned (l.64-69). A text with a rejected firewall still applies its other keys.

Every callback, with a firewall-only change:

- PKI (`pki.go:59`, registered first, `main.go:54`): unconditional. `reloadCerts` (pki.go:97) re-reads cert and key from disk and swaps the state. `reloadCAPool` re-reads the CA file. Errors are logged and the old state kept. Not fatal.
- Lighthouse (`lighthouse.go:139`, reload l.366): conditional on its own keys. No-op. Advertise DNS lookups run only when that key changed.
- HostMap (`hostmap.go:337`), Punchy (`punchy.go:70`), relay (`relay_manager.go:31`), connection manager (`connection_manager.go:60`): conditional. No-op.
- Stats (`stats.go:92`): restarts only when its config differs. No-op.
- DNS (`dns_server.go:72`): on a non-lighthouse node it clears its records on each reload (l.117-120). No effect on agents.
- Interface (`interface.go:441-448`): `reloadFirewall` (l.473 skips when `HasChanged("firewall")` is false; rejection at l.478-482 logs and returns). The other four are conditional. Per-writer UDP `ReloadConfig`: Linux (`udp_linux.go:268`) sets buffers only if `listen.read_buffer`/`write_buffer` are set, and hopssh strips them (`renew.go:968-973`); Darwin and generic are empty.
- TUN (`tun_linux.go:374`, `tun_darwin.go:166`): `getAllRoutesFromConfig` (`overlay/tun.go:44-47`) returns no change unless routes changed. Linux also checks `tun.mtu` (l.390). No-op.
- SSH (`ssh.go:61`): `sshd.enabled` false leads to `ssh.Stop()` on a nil listener (`sshd/server.go:266`). hopssh writes no sshd keys. If sshd were enabled, every reload would rerun `configSSH` and start `sshRun` again.

Ignored until restart, since no callback reads them: `listen.port` (read once at `main.go:141`, `lighthouse.go:93`), logging (logger built once from cfg, `nebula.go:74`, `:136`), and cipher (kept, `pki.go` reloadCerts).

Also: `oldFw.Destroy()` is a no-op (`firewall.go:491-493`), so readers holding the old pointer stay memory-safe. The race is the plain write at `interface.go:502`. `CatchHUP` (`main.go:121`, `config.go:129`) makes a SIGHUP to the agent process reload `nebula.yaml` and run every callback. No Go code in the repo sends or catches SIGHUP.

**(d) Control**

`control-api.patch` adds to `*Control`: `SendStuckFor` (control.go:223), `AddAdvertiseAddr` (442), `RemoveAdvertiseAddr` (452), `AddStaticHostMap` (466), `ReplaceStaticHostMap` (482), `SetInboundObserver` (504), `ProbeEndpoint` (532). It also adds `ReceivedDirect`/`ReceivedRelayed` to `ControlHostInfo` and patches `RebindUDPServer` (l.200). Upstream methods remain: Start, State, Context, Stop, Wait, ShutdownBlock, RebindUDPServer, ListHostmapHosts, ListHostmapIndexes, GetCertByVpnIp, CreateTunnel, PrintTunnel, QueryLighthouse, GetHostInfoByVpnAddr, SetRemoteForTunnel, CloseTunnel, CloseAllTunnels, Device.

`Control` holds `f *Interface` (l.45-60), which its methods use throughout (l.92-338). No exported method returns `f`, `Interface.firewall` (interface.go:62), a `*config.C` or a `CertState`. `Device()` (l.387) returns `c.f.inside`. `Interface.GetCertState` (interface.go:624) is exported but reachable only through `Interface`.

A reload needs no `Control` method: the callbacks are registered inside `Main` (`interface.go:440-441`, `main.go:257`) and fire from `ReloadConfigString` on the pointer the agent passed in. Validation needs more. `NewFirewallFromConfig` (`firewall.go:195`) takes a `*CertState`, and the constructors are unexported (`pki.go:290`).

**(e) Agent**

`internal/client` keeps no `*config.C`. `startNebula` declares `var cfg config.C` locally (`nebula.go:67`, Load l.68, `Main` l.77). `startNebulaKernelTun` does the same (l.129, l.139). The services keep only the Control: `userspaceMeshService{svc, ctrl}` (l.59-62) and `kernelTunMeshService{ctrl, meshIP, devName}` (l.120-124). `meshService` (l.25) has `Listen`, `Close`, `NebulaControl` and `DevName`, with no config and no reload. Every start reloads `nebula.yaml` from disk through `startNebulaByMode` (`renew.go:1403`).

`meshInstance` holds `svc` (`instance.go:26`) under `svcMu` (l.25). `startMu` is at l.41, `control()` at l.419, and `setSvc` at l.434 refuses once closed. `"firewall"` is in `nebulaYAMLKeys` (`nebula_yaml.go:43`).

## Design: firewall-p2-in-place-reload

### summary

The server puts each agent's effective policy in every heartbeat answer: the preset plus the rules that target that node, with sources resolved to /32 overlay addresses, and one version hash. The agent renders the firewall block from that answer and checks it in two steps: its own grammar and caps, then a scratch Nebula build through a new Control.CheckFirewall. It applies the block to the running Nebula with ReloadConfigString, under startMu and lockNebulaYAML, taking svcMu only around the reload and the live-hash check. It writes nebula.yaml only after the reload is verified, and if the write fails it reloads the old text. The plain pointer write at interface.go:502 is fixed by a new patch, firewall-swap.patch, that makes f.firewall an atomic.Pointer[Firewall] (about 12 changed lines in interface.go, inside.go and outside.go). That patch shares interface.go with read-loop.patch and outside.go with paths.patch, so the one-file-per-patch rule has to be amended to hunk-level disjointness with a check in the Makefile. The apply path never closes or restarts Nebula, so no session ends.

### vendor changes

1. control-api.patch (keeps its owner, control.go): add to control.go
   func (c *Control) CheckFirewall(cfg *config.C) (hash string, err error)
   - defer recover() turns any panic into an error. convertRule panics on malformed group lists (firewall.go:944-1009).
   - cs := c.f.pki.getCertState(); fw, err := NewFirewallFromConfig(c.l, cs, cfg); return fw.GetRuleHash() (firewall.go:302).
   - It only builds a Firewall and never touches the live one, so it needs no atomic and applies alone.
   Control struct: control.go:45-60 (f *Interface, l *slog.Logger). Interface.pki is interface.go:58.

2. firewall-swap.patch (new; theme: the firewall is swapped atomically):
   - interface.go: l.62 becomes `firewall atomic.Pointer[Firewall]`. In the constructor (l.183-189) remove `firewall: c.Firewall` from the literal and add `ifce.firewall.Store(c.Firewall)` before the return. In reloadFirewall: l.471 `f.firewall.Load().unsafeNetworks`; l.484 `oldFw := f.firewall.Load()`; l.502 `f.firewall.Store(fw)`. emitStats l.589 `f.firewall.Load().EmitStats()`.
   - New method, placed in interface.go and not control.go so that control.go stays in control-api.patch alone: `func (c *Control) FirewallRuleHash() string { return c.f.firewall.Load().GetRuleHash() }`.
   - inside.go: l.75, 91, 107, 228 read `f.firewall.Load().` (each packet loads once).
   - outside.go: l.521 `f.firewall.Load().Drop(...)`.
   - Not changed: config/config.go, pki.go, firewall.go. No existing patch owns them.
   Overlap check: in interface.go our hunks are at 62, 183-189, 471-505 and 589. read-loop.patch has its inboundObserver field at 98 and its hunks at 314-322, 346-372 and 374-426, so by the line numbers of the patched tree they are disjoint. In outside.go our line 521 lies between paths.patch hunks at 342-360 and 579-593. These line numbers are not a proof: the both-orders check in patch_rule_impact is.

### patch rule impact

Amend patches/README.md. Replace "no file is in two patches" with: "a file may be in two patches only when their hunks neither overlap nor touch (at least three unchanged lines between them, in any order); make patch-vendor checks each such pair." Keep "one theme per patch" and "applies in any order; one can be dropped alone".
Shared files after the change: interface.go (read-loop, firewall-swap) and outside.go (paths, firewall-swap). The Makefile check should, for each such pair, apply both orders to a pristine copy of nebula v1.11.2 and compare the resulting trees, and also confirm each patch alone. Also confirm that patch-vendor's reverse dry-run still recognises an already-applied firewall-swap hunk on a tree where read-loop or paths is applied. That is not shown for shared files today. Run the check before merging.
Fallback if the amendment is refused, with no README change: put the inside.go and interface.go hunks into read-loop.patch (it already owns interface.go, and the read loop is the packet reader that needs the atomic), and the outside.go line into paths.patch. That stretches each patch's theme, but nothing else changes.
Upgrade note: upstream v1.12.0 keeps the plain pointer write (per the task, not verified here). The patch will need a rebase at the next upgrade.

### agent changes

A. internal/client/nebula.go
1. meshService (l.25-47) gains `ReloadConfig(raw string) error`. userspaceMeshService (l.59-62) and kernelTunMeshService (l.120-124) implement it as `s.cfg.ReloadConfigString(raw)`. Each gets a field `cfg *config.C`, set in startNebula (l.66-88; pass &cfg to nebula.Main as today) and in startNebulaKernelTun (l.128-163). Every start builds a new struct, so the pointer is replaced together with svc under svcMu (setSvc, instance.go:434-442). Nothing else keeps the pointer.
2. Test fakes gain ReloadConfig: internal/client/local_api_lifecycle_test.go, renew_reload_invariants_test.go, v0_10_34_test.go.
3. The text last passed to Nebula is kept on the same struct (field `loaded string`), set at start and updated by ReloadConfig. It is needed for the drift guard in step 6e.

B. internal/client/firewall_policy.go (new, pure, no I/O)
4. type firewallPolicy {Version, Preset string; Rules []firewallRule}; type firewallRule {Proto, Port, Group, CIDR string}. Parsed from the heartbeat answer.
5. validateFirewallPolicy(p firewallPolicy, meshNet netip.Prefix) error. It does not trust the server:
   - preset is tcp, all or ping. An unknown preset is an error here. Enrollment falls back to tcp for unknown values (enroll.go:684), but an answer the agent does not understand must not change the file.
   - at most fwpolicy.MaxRules (100) rules.
   - each rule through fwpolicy.Rule{Proto, Port}.Validate(). The grammar is reused; nothing is copied.
   - exactly one of Group == "node" or CIDR set. Group admin is refused.
   - CIDR parses, is IPv4, is /32, and lies inside meshNet (inst.meshSubnet(), instance.go:391-398).
   - total port span over all rules at most 4096. addRule loops over every port in a range (firewall.go:655-660).
   - proto any with a port other than any is refused until the open item in risks is settled.
6. renderFirewallSection(preset, rules) produces the firewall value. With empty rules, for each of the three presets, it must give the same bytes as agentFirewallYAML (enroll.go:613-686). Preferred: move the preset lists into presetFirewall(preset) (inbound, outbound, inbound_action), used by both agentFirewallYAML and the renderer, so the enrollment writer is not forked. The invariants (TCP 41820 from admin, TCP 41820 from node, ICMP any, inbound_action drop) stay in presetFirewall for all three presets, as today. Custom rules are appended to inbound as {port, proto, group or cidr}.

C. internal/client/firewall_apply.go (new)
7. applyFirewallPolicy(inst *meshInstance, p firewallPolicy) error, in this order:
   a. validate, render newText.
   b. inst.startMu.Lock(); if inst.isClosed(), return errClosed.
   c. read inst.svc under svcMu (instance.go:26). If nil, return errNotRunning and leave the file untouched. There is no file-only path, because nothing would check the text.
   d. lockNebulaYAML(path) (nebula_yaml.go:22-27). Lock order: startMu, then lockNebulaYAML, then svcMu. Every existing path takes startMu before the file lock, and none takes startMu while holding the file lock (checked by reading routes.go:140-180, renew.go:658-671 and 775-820).
   e. old := read the file. Drift guard: if the non-firewall keys of old differ from inst's loaded text, return errPendingRestart and leave the file untouched. Routes and relay changes are written and then applied only at the next restart (routes.go:63, renew.go reloadNebula), and ReloadConfigString would apply them now as well.
   f. if newText == old, return nil (already applied).
   g. sc := config.C{}; sc.LoadString(newText); hash, err := svc.NebulaControl().CheckFirewall(&sc). On error, return with the file untouched. Use the service method here, not inst.control(), which takes svcMu and would deadlock in step h.
   h. inst.svcMu.Lock(); if inst.closed or inst.svc != svc, unlock and return errClosed. Then svc.ReloadConfig(newText); live := svc.NebulaControl().FirewallRuleHash(). If live != hash, call svc.ReloadConfig(old), unlock, and return errRejected. Nebula logs the rejection, keeps the running rules, and returns nil from ReloadConfigString (config/config.go:175-195), so the hash comparison is the only signal.
   i. writeNebulaYAML(path, newText) (nebula_yaml.go:32-37, which also writes nebula.yaml.bak). The caller holds the file lock, as in rewriteNebulaUnsafeRoutes. On error, svc.ReloadConfig(old), unlock, return the error.
   j. svcMu.Unlock(); return nil.
   Store the outcome (applied version, or error text) on the instance under activityMu, for the next beat.
   Nothing in this path calls reloadNebula, setSvc or close.
8. Hook: sendHeartbeat (renew.go), right after the decode at l.276: `if body.Firewall != nil { inst.setFirewallAck(body.Firewall.Version, applyFirewallPolicy(inst, *body.Firewall)) }`. The request map (l.154-199) gains `"firewall": {"appliedVersion": ..., "error": ...}` from the stored outcome. The heartbeat goroutine can wait on startMu during a start, which is acceptable.
9. The outcome is not persisted on the agent. After a restart, the first answer is applied again. An identical rendered section is a no-op, so the reported ack is correct again.
10. Clients: the server sends firewall only to nodes with client_type agent (see item 13). A hop client keeps the block from clientNebulaConfig (cli_join.go) until the "clients on a heartbeat" phase.

D. Server
11. internal/api/firewall_policy.go (new): effectiveFirewall(networkID, node) (policy, error).
   - preset: db.FirewallStore.NetworkFirewall (firewall.go:76). If there is no row, use networks.firewall_preset.
   - rules: own rules plus the rules of AttachedSets (firewall.go:319). This is a union. Keep a rule if TargetNodeID is empty or equals node.ID.
   - source: SourceNodeID empty gives Group "node". A node ID gives that node's current overlay address as CIDR addr/32. The node's address field must be confirmed in internal/db/nodes.go; the allocator is NextNodeIndex (nodes.go:409-430). A source that is unknown or deleted drops the rule (fail closed).
   - version: sha256 hex of canonical JSON of {preset, resolved rules}. It changes when an address changes, so a re-resolved rule is re-applied.
12. internal/api/renew.go Heartbeat: the body gains `Firewall *struct{AppliedVersion, Error string}`, decoded under the existing MaxBytesReader. The response map (l.470-500, next to routes at l.491-496) gains `firewall` when the node is an agent. RenewHandler (renew.go:56-67) gains the stores. cmd/server/main.go:209 wires them.
13. Ack storage: migration 012_node_firewall_ack.sql adds nodes.firewall_applied_version and nodes.firewall_error (TEXT, default ''). They are written through the heartbeat coalescing buffer (internal/db/nodes.go RecordHeartbeat, flushed every 5 s), so the heartbeat adds no transaction.
14. Dashboard (P1 territory): show "not enforced, agent too old" when the last beat had no firewall key, and "pending" when the acked version differs from the current one.

### validation

1. Golden (P0 style): TestRenderFirewall_EmptyRulesMatchesEnrollBlock. For each preset, renderFirewallSection(preset, nil) equals agentFirewallYAML(preset) byte for byte. The existing firewall_golden_test.go then pins that the output parses with Nebula's parser.
2. Table test: TestValidateFirewallPolicy_Table. Cases: bad proto; port 0, 0-5, 70000, 9-3, 01; icmp with a port; CIDR that is not /32, is IPv6, or lies outside meshNet; group admin; 101 rules; span above 4096; unknown preset; proto any with a port.
3. Scratch check: CheckFirewall on a hand-built bad local_cidr returns an error, not a panic. The test compares sha256 of nebula.yaml before and after: unchanged.
4. Live rejection: TestApplyFirewall_RejectedKeepsFileAndRules sends a bypassed text containing an unknown proto straight to svc.ReloadConfig. ReloadConfig returns nil, the live hash is unchanged, and the test checks that the apply path returns errRejected, the file is byte-identical, and a ping across the tunnel still succeeds.
5. Every property of the P2 gate has its own two-agent test (see tests). Each one passes through applyFirewallPolicy, never through a direct write of the file.
6. Server: effectiveFirewall table tests (target filter, /32 resolution, deleted source dropped, version changes when an address changes, no rules gives the preset only), the ack decode under MaxBytesReader (source-scan tripwire in the style of the existing ones), and TestE2E_Access and the e2etest server suites still green with the new response field.
7. Final gate before release: make preflight (make release runs it), plus go build -mod=vendor ./... and go vet -mod=vendor ./..., and the race run from patches/README.md.

### open flows

- A removed rule stops an open flow at its next packet, silently. The tunnel stays, with the same indexes and no new handshake. This is the observed behaviour of the spike (2026-11-27) and the design claim the P2 gate tests.
- A restored rule admits new connections at once. The old flow stays dead: its conntrack entry was deleted on failure and is re-judged only by the current table.
- Keepalive and the invariants are unaffected. TCP 41820 from node and admin, ICMP and inbound_action drop stay in every preset, so the watchdogs and the terminal still work during and after a change.
- Race window: a packet that loaded the old pointer before the Store is judged by the old rules. That is at most one packet per reader thread, and it was in flight before the change.
- Conntrack: rulesVersion is uint16 and wraps; on wrap reloadFirewall resets the conntrack table (interface.go:492-497), which drops open flows once. This is existing Nebula behaviour, and the apply path can hit it after 65,535 changes.
- Apply holds svcMu during ReloadConfigString. That blocks status calls (control(), setSvc) for the duration of the reload. The reload also runs every callback, including the PKI one, which re-reads the cert files, so the duration is milliseconds, but it is unmeasured.
- Relay-routed and punched tunnels are unaffected. The lighthouse is not an agent and allows everything (internal/mesh/network_manager.go:539-547).
- Clients keep their own block, so a policy change never reaches a client's Nebula in this phase.
- Enrollment still writes the preset and the first heartbeat applies the rules, so a new agent may be reconfigured seconds after it comes up.

### deviations from approved plan

1. Validation uses a new Control.CheckFirewall in control-api.patch, not a scratch NewFirewallFromConfig in package client. The client cannot build one: NewFirewallFromConfig needs an unexported *CertState (pki.go:33). The exported pair nebula.NewFirewall plus AddFirewallRulesFromConfig would work without a patch, but it skips the inbound and outbound actions and the conntrack timeouts (firewall.go:195-236) and duplicates construction logic.
2. The one-file-per-patch rule is amended to hunk-level disjointness with a check, not kept. The atomic change touches interface.go (read-loop.patch) and outside.go (paths.patch), so the rule as written cannot hold. A fallback without the amendment is given in patch_rule_impact.
3. FirewallRuleHash lives in interface.go, not control.go, so control.go stays owned by control-api.patch alone.
4. A post-reload hash check is added. ReloadConfigString returns nil even when the firewall is rejected (config/config.go:175-195), so the approved plan's step 6 cannot rely on its error.
5. No explicit mode field. The server sends the preset and the rules every beat and the agent renders from them, so "back to preset" happens by itself when the rules are cleared. This closes the gap the approved plan left open without adding a field.
6. No file-only apply. When the instance is not running, the apply is refused and the file stays. The approved plan's "write the file if svc is nil" would write text that nothing checked.
7. A drift guard is added (step 6e). Routes and relay values written to nebula.yaml but not yet applied are not pushed by an in-place reload. The apply waits for the next restart.
8. The policy is sent to agents only (client_type agent). The approved plan did not separate clients, and a client's block is ICMP plus TCP 41820 from node, not the agent template.
9. The heartbeat always carries the policy when the network has one. There is no version negotiation: a few KB per minute per node is accepted, in exchange for simpler self-healing.
10. internal/e2etest/peer.go is not extended for the P2 gate. The gate runs on two real agents (firewallPair, firewall_e2e_test.go:43). The extension is needed only if a server-side test wants a far peer that applies rules.

### risks

1. The any-protocol table: fwpolicy accepts proto any with a port. addRule stores it in the AnyProto table through the same per-port code (firewall.go:277, 655-660), so it may be honoured, but nothing was tested here. Until a test proves it, the agent refuses it (validation table) and the dashboard does not offer it.
2. Config reads outside the reload callbacks: the design assumes Nebula reads config only in constructors and reload callbacks, which run under reloadLock. ssh.go and udp_linux.go ReloadConfig were not checked. A read in a background goroutine would be a data race that the -race test must catch.
3. Silent rejection: covered by the hash check. A rejection that changes the hash in a different way (partial apply) is not covered: Nebula builds the whole firewall before swapping, so a partial apply is not expected, but it is unproven.
4. Makefile check: patch-vendor's reverse dry-run may misreport an already-applied hunk on a shared file. Unproven; see patch_rule_impact.
5. go vet copylocks: atomic.Pointer carries noCopy. Interface is used by pointer, but no code was checked for a by-value copy. Run go vet -mod=vendor ./... after the patch.
6. Port span: a range allocates one entry per port (firewall.go:655-660). The 4096 total cap is a proposal, and the server must enforce the same cap at PUT, or the agent refuses a policy that the server accepted.
7. Released addresses: a deleted node's address can be given out again after 48 h while its cert is valid for up to 24 h more (the released_node_ips rule in the coding guide). Rules resolve through the current address and a deleted source drops the rule, so a new node that inherits an address cannot match a rule written for the old one, as long as the server recomputes every beat.
8. Lock order: apply takes startMu, then lockNebulaYAML, then svcMu. The renewal path takes certInstallMu, then startMu (writeCertPair), then lockNebulaYAML, and the routes path holds lockNebulaYAML without startMu. No cycle was found by reading; the lock-order test should be added with the race test.
9. Heartbeat goroutine and startMu: the heartbeat can block on startMu during a start. Not a deadlock by reading; a hang would stop heartbeats, so the watcher and watchdog tests should run the start path with a beat in flight.
10. Upgrade to v1.12.0: the plain pointer write is kept upstream (per the task; unverified here), so firewall-swap.patch will need a rebase at the next upgrade.
11. Old agents ignore the firewall key and keep their block. The dashboard must not show them as enforced (item 14).
12. Items not checked in the code: runWithTimeout (named in the discovery log for cmd/agent) was not found in internal/client by grep on 2026-11-27, so any timeout around ReloadConfigString would be new. No timeout is proposed: a hung reload under svcMu blocks status, which is the trade-off to accept with the race test.
13. The node's address field in internal/db/nodes.go was not read here. The resolution in item 11 depends on it.

### tests

Red first, each failing on the unpatched tree or before the apply exists. Two-agent tests use firewallPair (internal/client/firewall_e2e_test.go:43).
1. internal/client/firewall_golden_test.go: TestRenderFirewall_EmptyRulesMatchesEnrollBlock (renderer missing, red).
2. internal/client/firewall_policy_test.go: TestValidateFirewallPolicy_Table (cases in validation item 2).
3. internal/client/firewall_apply_test.go, single-agent, using the fakes: TestApplyFirewall_NotRunningLeavesFile, TestApplyFirewall_ClosedInstanceNoReload, TestApplyFirewall_DriftDefersApply (routes key written to the file without a reload: errPendingRestart, file untouched), TestApplyFirewall_WriteFailureRollsBack (directory made read-only after the reload: live hash equals the old one).
4. internal/client/firewall_e2e_test.go, two real agents: TestE2E_FirewallRemovedRuleStopsNewAndOpenFlows (open TCP flow through a rule, apply without it: the next packet fails, the tunnel still passes ICMP, the handshake counter is unchanged); TestE2E_FirewallIcmpKeepsTunnelNoHandshake (continuous ICMP across an apply, handshake counter unchanged); TestE2E_FirewallRestoredRuleAdmitsNewConnections; TestE2E_FirewallRejectedKeepsFileAndRules (validation item 4: file sha256 unchanged, ping works); TestE2E_FirewallReloadUnderTraffic, run with -race, 200 alternating applies while UDP ping-pong and TCP echo run in both directions. This is the gate that is red on unpatched vendor (DATA RACE, firewall.go/interface.go:502) and green with firewall-swap.patch.
5. Tripwire: TestFirewallSwap_ReaderSites: source scan of inside.go, outside.go and interface.go, failing on any bare f.firewall use that is not Load() or Store(), so a future reader cannot bring the race back.
6. Server, internal/api/firewall_policy_test.go: TestEffectiveFirewall_TargetFilter, _ResolvesSourceToSlash32, _DropsDeletedSource, _VersionChangesWithAddress, _NoRulesIsPresetOnly; TestHeartbeat_FirewallAckStored (flush then read the node row); TestE2E_Access and the e2etest server suites still green.
7. Patch checks: each new or changed patch applies alone to pristine nebula v1.11.2, both orders of each shared-file pair give identical trees, go build -mod=vendor ./... and go vet -mod=vendor ./... pass, and the race run from patches/README.md passes.
8. Release gate: make preflight, plus the race run for ./internal/client ./internal/api.

## Design: firewall-phase-2-apply

### summary

Agents apply the server's firewall policy in place, without closing Nebula. The heartbeat answer carries firewall {version, fingerprint, rules} whenever the network has a policy row (version >= 1). The rules are the network's own rules plus those of every attached set, merged, filtered to this node as target, and with the source node ID already resolved to its mesh address. Descriptions stay on the server. The agent validates the candidate with Nebula's own rule loader on a scratch firewall (exported NewFirewall + AddFirewallRulesFromConfig, under recover) plus its own group, port, CIDR and action checks. It then renders the candidate nebula.yaml from the file on disk with only the firewall block replaced, calls ReloadConfigString on the *config.C that the mesh service keeps, checks the running rule hash against the scratch hash, and only then writes the file. Lock order: startMu (TryLock, so the heartbeat never blocks), then svcMu around the reload, with lockNebulaYAML around the file read and write. One new vendor patch, firewall-swap.patch, makes Interface.firewall an atomic.Pointer and adds Control.FirewallRuleHash() in a new file. It touches interface.go and outside.go, which other patches own, so the README rule needs amending to hunk-disjointness. Estimate: about a day and a half. Build the patch and the validator first, since both can be tested alone, then the heartbeat and the two-peer E2E.

### vendor changes

One new patch, patches/firewall-swap.patch (theme: the firewall pointer is swapped and read atomically).
- interface.go: field l.62 `firewall *Firewall` becomes `firewall atomic.Pointer[Firewall]`. The literal at l.189 (`firewall: c.Firewall`) becomes a Store right after the struct literal. Reads at l.471 (`f.firewall.Load().unsafeNetworks`), l.484 (`oldFw := f.firewall.Load()`) and l.589 (`f.firewall.Load().EmitStats()`). Write at l.502 (`f.firewall.Store(fw)`). interface.go already imports sync/atomic for inboundObserver (l.98, read-loop.patch), so check whether this patch needs its own import hunk, and test both application orders.
- inside.go (in no patch today): l.75, 91, 107, 228 read `i.firewall`, which becomes `i.firewall.Load()`.
- outside.go (in paths.patch): l.521 becomes `f.firewall.Load()`. It is clear of paths.patch's hunks (342-360 and 579-593).
- New file firewall_state.go in package nebula, in the same patch: `func (c *Control) FirewallRuleHash() string` returns `c.f.firewall.Load().GetRuleHash()`, with a nil guard that returns "". Control.f is *Interface (control.go:49). Keeping the accessor in a new file leaves control.go, which control-api.patch owns, untouched. GetRuleHash is at firewall.go:302 and hashes f.rules.
- No change to reloadFirewall's logic, only to how it reaches the pointer.
- (*Firewall).Destroy is a no-op with a TODO (firewall.go:491-493). A reader that still holds the old pointer after Store is therefore safe today. Re-check this on any Nebula upgrade.
- Before writing the patch, grep `\.firewall\b` across vendor/github.com/slackhq/nebula to confirm the reader list. The vendored tests do not mention firewall (checked 2026-11-27).

### patch rule impact

Breaks the README rule "no file is in two patches" for two files: interface.go (read-loop.patch) and outside.go (paths.patch). inside.go is in no patch and stays with this one.
Proposed amendment to patches/README.md line 5: "Each patch is one theme. A file may be in two patches only if their hunks in it do not overlap, context lines included. The patches then still apply in any order, and each can be dropped alone." That keeps the property the rule protects, but verify it rather than assume it. Add `make patch-check`. On a clean tree it applies each patch alone and each pair that shares a file in both orders, forward and reverse dry-run, and fails on any reject. Today that means the pairs (firewall-swap, read-loop) and (firewall-swap, paths).
Glob order in the Makefile puts firewall-swap before paths and read-loop. Those two then apply to a tree whose lines have moved. patch matches on context, so this is fine as long as no context overlaps the changed lines, but make patch-vendor from a clean tree must confirm it.
If the amendment is refused, fold the hunks into read-loop.patch and paths.patch. That is the worse option: dropping paths.patch would then also drop the firewall swap, which breaks "drop one alone".

### agent changes

1. internal/client/nebula.go. The meshService interface (l.25-47) gains `ReloadConfig(raw string) error`. userspaceMeshService (l.59-62) and kernelTunMeshService (l.120-124) gain a `cfg *config.C` field. startNebula (l.66-88) and startNebulaKernelTun (l.128-163) store the `var cfg config.C` they already build (l.67, l.129). ReloadConfig is `s.cfg.ReloadConfigString(raw)`, with an error when cfg is nil. Each start builds a new cfg, so after reloadNebula (renew.go:1255) through startNebulaByMode (l.1403) the new service holds its own pointer. The fakes at renew_reload_invariants_test.go:69, v0_10_34_test.go:206 and local_api_lifecycle_test.go:261 get a stub.
2. Preset kept per enrollment. Enrollment (enrollments.go:22-51) gains `FirewallPreset string json:"firewallPreset,omitempty"`. It is set where the enrollment is saved, after writeNebulaConfig(…, firewallPreset) (enroll.go:688), on all three paths: token, device flow and bundle. Empty means an enrollment from before the field. At the first boot under phase 2, presetOf(file's firewall block) renders the three presets with agentFirewallYAML (enroll.go:613), compares them as rule lists using the firewallRules helper (moved from firewall_golden_test.go into a non-test file), and persists the result. Without a match the apply is refused, with ack error "firewall preset not recognised, enroll again". This must never narrow silently: an enrollment written with "all" that defaulted to tcp would lose UDP at the first beat.
3. Rendering. agentFirewallYAML(preset) stays as a wrapper. renderFirewallBlock(preset string, extra []firewallRule) (string, error) builds the block as a Go map and marshals it with yaml, never with Sprintf. It appends the extra inbound rules and always appends the invariants: TCP 41820 from admin, TCP 41820 from node, ICMP, inbound_action drop. No preset or server rule can remove them. Descriptions are never written into nebula.yaml.
4. Heartbeat. The response struct in sendHeartbeat (renew.go:254-276) gains `Firewall *firewallPolicy json:"firewall,omitempty"`. Right after applyRoutesUpdate (renew.go:289) the code calls applyFirewallPolicy(inst, body.Firewall). nil means do nothing, which covers old servers and networks without a row. The request struct, next to selfEndpoints, gains `FirewallApplied *firewallAck json:"firewallApplied,omitempty"`. It carries the version and fingerprint last applied, and the error for the version the server offered most recently.
5. New file internal/client/firewall_apply.go, about 200 lines with the moved recorder.
   - Types: firewallPolicy {Version int64, Fingerprint string, Rules []firewallRule}; firewallRule {Proto, Port, SourceAddr string}; firewallAck {Version, Fingerprint, Error string}. The agent never receives descriptions.
   - applyFirewallPolicy(inst, p): defer recover, which logs CRITICAL and sets the ack error. Fast path: if p.Fingerprint equals inst.fwApplied.fingerprint, return. Then `if !inst.startMu.TryLock() { return }`. When busy, skip the beat; the server resends the policy on every beat.
   - Steps: (1) under lockNebulaYAML, read the file and parse it with parseNebulaYAML (renew.go:886). (2) Build the candidate by replacing only the firewall key and marshalling the map. Generated files carry no comments, so reformatting is acceptable on the accepted path. (3) If the rendered firewall block equals the file's, record success and stop; this keeps PKI from being re-read. (4) Validate (see validation). (5) Under svcMu, check inst.closed and that inst.svc is the service this apply started from, then call svc.ReloadConfig(candidate). (6) Check FirewallRuleHash() against the scratch hash, and the running inbound and outbound actions against the candidate. On a mismatch, call ReloadConfig(fileText) to restore c.Settings, record the ack error, and stop. (7) Write the candidate to nebula.yaml with the unlocked write, while still holding lockNebulaYAML. Check nebula_yaml.go:32 first: if writeNebulaYAML takes the lock itself, add an unlocked variant. A failure here is safe to retry, because the next beat reloads an identical candidate and does nothing. (8) On success, set inst.fwApplied and clear the error. Store fwApplied and the last error under a new fwMu.
6. Locks. close() (instance.go:597-615) sets closed under svcMu and closes the service after releasing it. The reload runs under svcMu with the closed check, so it never runs on a closing Control. reloadNebula holds startMu for the whole swap (renew.go:1256-1257), and writeCertPair (renew.go:659) takes startMu too. So no apply overlaps a restart or a cert install. The order is startMu, then lockNebulaYAML, then svcMu. The routes writer (routes.go:148) takes lockNebulaYAML alone and returns before reloadNebula, so there is no cycle.
7. Server. internal/api/renew.go, Heartbeat (l.197): read `firewallApplied` from the request body and store it, either with the peer state or in a migration 010 column pair. Pick the first if the peer-state buffer can carry it. In the answer, next to routes (l.491-496), add `firewall` when NetworkFirewall (internal/db/firewall.go:76) reports version >= 1. Build the rules from the network's own rules plus every AttachedSets entry (firewall.go:319) whose target is empty or this node. Resolve each source node ID to that node's mesh address in this network, and drop the rule when the node is gone. Strip descriptions. fingerprint is sha256 over the canonical JSON of the resulting rules. The version is informational only: attach and detach do not change it, so the fingerprint is the key.
8. Dashboard: not in phase 2. A node with no ack shows "not enforced, agent too old". The tab reads the ack, so the ack is the data it needs.

### validation

Nothing changes until all of these pass. Failure means refuse, record the ack error, and leave the file byte for byte.
(1) Shape, in the agent: proto is tcp, udp, icmp or any. port is any, N or N-M with 1 <= N <= M <= 65535, and icmp takes only any. sourceAddr is an IPv4 /32 inside this enrollment's /24 (the same test as the L3 filter in injectPeerEndpoints). At most 100 rules. Non-empty version and fingerprint.
(2) Groups: every group in the rendered block must be one that certificates carry (node, admin, user, agent, server). Nebula does not check group names, so this check is ours.
(3) Nebula's own loader: config.NewC(l).LoadString(candidate) must succeed. Then, on a scratch nebula.NewFirewall (firewall.go:136) built with the agent's cert and the timeouts of NewFirewallFromConfig (firewall.go:195), call nebula.AddFirewallRulesFromConfig for inbound and outbound. Its errors (firewall.go:319-411) reject. Run all of it under recover, since convertRule panics (firewall.go:979, 985, 990) must become rejects. Keep the scratch GetRuleHash for step 6 of the apply.
(4) Actions: inbound_action and outbound_action must be drop or reject. NewFirewallFromConfig only warns on other values, so this check is ours.
(5) Invariants: the scratch rule list must contain TCP 41820 from admin, TCP 41820 from node and ICMP. The recorder from firewall_golden_test.go gives the rule list.
(6) Only the firewall block differs: the candidate minus its firewall key must equal the file minus its firewall key.
What Nebula would reject that this misses: nothing on this path. The scratch build runs Nebula's own rule loader on the exact text. Group names, action strings and the invariants are not checked by Nebula, and checks (2), (4) and (5) cover them. Open point: the rule hash covers f.rules. If the default actions are not part of f.rules, an action-only change is caught by the action comparison in apply step 6, not by the hash. Confirm this in NewFirewallFromConfig (firewall.go:195) before relying on the hash alone.

### open flows

A removed rule stops an open flow at its next packet, silently, and the tunnel stays up with the same indexes and no new handshake (spike, 2026-11-27). A re-added rule lets new connections through; the old flow stays dead. An allowed flow survives a rule addition: reloadFirewall bumps rulesVersion, and inConns (interface.go/firewall.go:505-571) rechecks each tracked connection on its next packet at firewall.go:527, which the rules still allow. ICMP is not affected by the rule change, because the handshake and keepalive packets are not checked against the rules the same way as data. Identical text changes nothing.
Straddling packet: a packet that loaded the old pointer before Store is judged by the old rules once more. That matches "the next packet" and is not a data race any more.
ReloadConfigString runs every callback, not just the firewall's. PKI re-reads the cert files, and routes, relay, punch and the other keys in the candidate are applied too. The candidate is built from the file under lockNebulaYAML, so those values equal the file's. Test (6) enforces this.
After a silent rejection c.Settings holds the rejected text. The restore call runs the firewall callback once more with the old rules. Open flows that those rules still allow stay open. The restore also re-reads PKI, which is harmless.
Inbound only: outbound rules stay the preset's, as in the concept page.
SIGHUP: CatchHUP reloads nebula.yaml from the file (main.go:121). The file always equals the applied rules, because it is written only after success, so a SIGHUP cannot bring back rejected rules.

### deviations from approved plan

(a) Validation uses exported nebula.NewFirewall plus nebula.AddFirewallRulesFromConfig under recover, not NewFirewallFromConfig. NewFirewallFromConfig needs an unexported *CertState (pki.go:33), and the package cannot construct one without a patch. No extra patch is needed for validation.
(b) The apply is serialized on startMu with TryLock, then svcMu for the reload. The plan named the setSvc lock. reloadNebula releases svcMu before closing the old service (renew.go:1344-1347) and holds startMu for the whole swap, so startMu is the lock that actually excludes a restart. TryLock means a busy beat is skipped rather than blocking heartbeats.
(c) The server sends own and attached rules only, already filtered to this node and with addresses resolved. The preset stays on the agent and is stored per enrollment as firewallPreset. A preset cannot change on a running device, as the concept page says. Enrollments from before the field are classified once from their file.
(d) patches/README.md is amended from file-disjoint to hunk-disjoint for interface.go and outside.go. Otherwise the swap would have to be folded into two other patches, which breaks drop-alone.
(e) The ack travels as firewallApplied in the next heartbeat request, because the answer cannot carry it.
(f) An identical rendered firewall block skips ReloadConfigString. Otherwise every beat re-reads PKI.
(g) The post-apply check uses the existing (*Firewall).GetRuleHash through a one-line accessor in a new vendor file in the same patch. control.go stays out of it.
(h) internal/e2etest/peer.go is not extended for this gate. The two-real-peer E2E uses firewallPair (firewall_e2e_test.go:43), which is already two real agents with firewalls. Extend e2etest.Peer only if a server test later needs a non-agent peer with rules.
(i) The candidate is built from the file on disk, not from c.Settings, because after a rejection c.Settings holds the rejected text.

### risks

1. runHeartbeat (renew.go:70-142) has no recover, so the apply must recover on its own. The validator and the apply each get a defer recover.
2. The atomic patch touches two files that other patches own. The amendment in patch_rule_impact must pass patch-check. If it does not, the fold option breaks drop-alone.
3. The hash check depends on f.rules covering everything that matters. Confirm the default-action handling in NewFirewallFromConfig before relying on the hash alone (see validation).
4. Destroy is a no-op today (firewall.go:491-493). A Nebula upgrade that adds teardown to Destroy would make the old pointer unsafe to hold past Store. The upgrade to v1.12.0 needs this checked.
5. Routines and other non-reloadable keys in nebula.yaml: a reload applies every callback with the candidate's values. The candidate equals the file except for the firewall block (test 6), so this only matters if the file itself was changed by hand. I did not re-verify the routines default in internal/nebulacfg/defaults.go (around l.190-193); check it before relying on a stable routine count across reloads.
6. Legacy enrollments whose block matches no preset (a hand edit) refuse to apply. The agent reports the error and keeps the running rules. That is deliberate, but the dashboard will show such nodes as failing until they re-enroll.
7. The kernel-TUN path (startNebulaKernelTun) must get the same cfg field and ReloadConfig method. The kernel-TUN E2E needs root on Linux (see e2e-tests.md), so it cannot be run on the Mac alone.
8. SKIP on a busy startMu: a long startInstance on a slow network delays the policy by one beat at most. The server resends it, so no state is lost.
9. Older agents send no ack, so the dashboard must show "not enforced" for them and never "pending". Read this into the phase 1 tab before P2 ships.
10. Packet overhead of the scratch build is negligible, since it runs only when the candidate differs. The rules hash check and the restore add one reload on the rejection path only.

### tests

Vendor, first: make patch-check (new) on a clean tree, with each patch alone and the firewall-swap pairs in both orders. Then go test -mod=vendor -race github.com/slackhq/nebula and go build -mod=vendor ./... as in patches/README.md.
Unit, internal/client, red first:
- validator table: each rejection in validation steps 1-6 (proto foo, port 0, port 70000, reversed range, icmp with a port, unknown group, sourceAddr outside the /24, sourceAddr not /32, bad YAML, inbound_action "allow", a missing invariant, a changed non-firewall key). For each: the file's sha256 is unchanged and the fake service's ReloadConfig is called zero times.
- presetOf: every historical block under testdata/firewall classifies to its preset. The unknown case refuses.
- renderFirewallBlock: each preset with extra rules parses with the golden recorder and includes the invariants. A description containing quotes and newlines cannot change the rendered keys.
- Race test for the atomic swap in the package nebula tree, if the vendored tests are run.
E2E, internal/client with firewallPair, two real agents, under -race, red without the patch:
- a removed rule stops new connections and the open UDP flow within one heartbeat (drive it with inst.signalHeartbeat, instance.go:412, against the fake control plane). ICMP keeps the tunnel with no new handshake: the hostmap index is unchanged.
- re-adding the rule: new connections pass, the old flow stays dead.
- a rejected policy (proto "foo", sent past the server's validation): the file's sha256 is unchanged, tunnels stay up, the next heartbeat carries the ack error.
- empty rules: TCP 41820 and ICMP still work.
- an identical second beat calls ReloadConfigString zero times (count with a RegisterReloadCallback on the cfg).
- the reload race: two flows run continuously while 50 reloads alternate the rules. The spike saw 10 and 15 race reports on the untouched tree.
- the same set on the kernel-TUN path as root on Linux.
Server, internal/api, TestE2E_Firewall extended: the heartbeat carries firewall only at version >= 1, filtered to this node and with source addresses resolved. A deleted node's rule is dropped. A detached set leaves the payload. firewallApplied is stored.

## Judge 1

Ranking: firewall-p2-in-place-reload, firewall-phase-2-apply

### Fatal flaws

- {"design": "both", "flaw": "The race fix cannot stay inside the current patch layout. The atomic.Pointer swap needs edits to interface.go (owned by read-loop.patch), outside.go (owned by paths.patch) and inside.go (in no patch). A new firewall patch touching those files breaks the README rule that no file is in two patches and that any patch can be dropped alone. make patch-vendor stops at a patch that does not apply, so dropping read-loop.patch would break the firewall patch. The design has to resolve this explicitly: amend the rule with a named exception, or fold the edits into the existing patches.", "evidence": "patches/README.md:5 and :6; read-loop.patch lists interface.go; paths.patch lists outside.go; interface.go:62 (plain field), interface.go:502 (plain write); readers outside.go:521, inside.go:75/91/107/228. Race reproduced under traffic with reloads: docs/wiki/concepts/firewall-rules.md:121 (10 and 15 reports, 0 without reloads)."}
- {"design": "firewall-phase-2-apply", "flaw": "No rollback when the nebula.yaml write fails after the in-place apply. Running rules move to the new set while the file keeps the old one. The next heartbeat with the same preset is a no-op if the check compares against live state, so the file is never rewritten. The divergence lasts until restart, when the old file rules come back. This contradicts the design's own 'file byte for byte' claim. Step 7 ('safe to retry') does not fix it, because a retry sees no change.", "evidence": "Design step 7 as summarized, with the write after the apply; the no-op compares the live hash (firewall.go:302 GetRuleHash hashes live f.rules). Restart rebuilds from the file via NewFirewallFromConfig (firewall.go:195)."}
- {"design": "firewall-p2-in-place-reload", "flaw": "The no-op gate is the rule hash, and GetRuleHash does not cover everything that changes a firewall. Actions (InboundSendReject / OutboundSendReject), defaultLocalCIDRAny and the conntrack timeouts are not in the hash, so a change to any of them with the same rules is not applied. Nebula does not reject a bad action string: it warns and falls back to drop, so the action check must compare intent, and it needs an accessor the design does not have. Live impact is nil today because the rendered block fixes the actions and the timeouts are not preset, so this bites when presets or timeouts change.", "evidence": "firewall.go:302-306 (sha256 of f.rules only); firewall.go:214 (defaultLocalCIDRAny); firewall.go:207-209 (conntrack timeouts); firewall.go:216-238 (invalid action warns and defaults to drop, no error)."}
- {"design": "both", "flaw": "The apply runs inside the heartbeat goroutine with no recover and no deadline. runHeartbeat has no recover(). NewFirewallFromConfig panics when no certificate is loaded. convertRule asserts that each group entry is a string, so a non-string group panics; the rendered block never produces one. A panic in the heartbeat goroutine ends the agent process. A wedged apply holds startMu, and writeCertPair takes startMu for certificate renewal, so renewal for that enrollment stops too. The repo rule (CLAUDE.md, Phase DD) says blocking calls in a loop body need hard timeouts, because recover only catches panics.", "evidence": "renew.go:70 (runHeartbeat); grep for recover() in renew.go and client.go finds none; firewall.go:201-202 (panic without a certificate); convertRule slice case uses .Interface().(string); renew.go:659-660 (writeCertPair takes startMu); CLAUDE.md Phase DD paragraph."}
- {"design": "firewall-phase-2-apply", "flaw": "The validation is not the live builder. It builds the scratch firewall with NewFirewall plus AddFirewallRulesFromConfig, which skips the defaultLocalCIDRAny read and the action block that the live NewFirewallFromConfig runs. The 'validated equals applied' claim therefore holds only while the rendered block never sets those keys. That is true today, so this is a risk for future presets, not a live fault.", "evidence": "firewall.go:214 (defaultLocalCIDRAny); firewall.go:216-238 (action switch, warn and default); firewall.go:327 AddFirewallRulesFromConfig (exported, no actions)."}
- {"design": "firewall-phase-2-apply", "flaw": "The ICMP argument mixes what the firewall checks with what it does not. Drop (firewall.go:425) runs on tunnel data packets; handshake and keepalive packets do not reach it. A conclusion that ICMP stays open because the invariants keep it open is a claim about the rule set and needs its own test. It is not a firewall-reach argument.", "evidence": "firewall.go:425 (Drop); outside.go:521 (Drop call on the receive path); design text as summarized."}

### Ideas to graft

- TryLock on startMu for the firewall apply, skip and retry on the next beat (Design 2). The heartbeat already blocks on startMu for route changes (renew.go:289 -> routes.go:64 -> reloadNebula at renew.go:1256), so this avoids extending the stall rather than removing an existing one.
- No-op only when both the hash and the rendered firewall text match (Design 2). This closes the action and timeout gap in GetRuleHash.
- Drift check at every start: compare the nebula.yaml firewall section with the live rules and re-apply on mismatch. This repairs a failed write from either design.
- Same-builder validation through Control.CheckFirewall (Design 1), so validated equals applied by construction.
- Rollback on write failure (Design 1): if the write fails, re-apply the previous rendered block before returning.
- recover() plus a deadline around the apply, following the Phase DD runWithTimeout pattern (CLAUDE.md), so a panic or wedge cannot end the process or hold startMu.
- Race regression test: run under -race with traffic and reloads, using the reproduction in docs/wiki/concepts/firewall-rules.md:121 (10 and 15 reports before the fix, 0 without reloads). It must pass before merge.
- Stop the file-based SIGHUP path (CatchHUP, vendor main.go:121) or route it through the same apply. No SIGHUP sender exists in the repo (grep finds it only in docs), so this is latent, but an operator's kill -HUP reaches it.

### Recommendation

Build firewall-p2-in-place-reload with the grafts above. Its validation runs the live builder (Control.CheckFirewall), so validated and applied match by construction, and its rollback on a failed write covers the gap in the other design. Conditions before merge: (1) resolve the one-file-per-patch rule in patches/README.md explicitly for the atomic.Pointer swap, by amending it or naming the exception; (2) make the race reproduction in docs/wiki/concepts/firewall-rules.md a -race test under traffic with reloads, and require it to pass; (3) apply under TryLock(startMu), inside recover(), with a deadline; (4) treat the no-op as unchanged only when the hash and the rendered text both match, and add an action and timeout accessor to control-api.patch (control.go already belongs to it); (5) add a drift check at start; (6) stop the file-based SIGHUP reload (CatchHUP) or send it through the same apply.

firewall-phase-2-apply is second. Its TryLock and its text-based no-op are the best ideas in it. Its write-failure path leaves running rules diverged from the file, and its scratch build is not the live builder.

Checked and holding: the rulesVersion straddle (inConns re-checks on a version mismatch, firewall.go:527-558); copylocks (Interface already holds atomic fields, interface.go:77-83); lock order (startWatcher and stopWatcher take only watcherMu, and lifetimeCtx takes no lock, instance.go:460-525). The heartbeat's startMu wait is not new, as noted above.

## Judge 2

Ranking: firewall-phase-2-apply, firewall-p2-in-place-reload

### Fatal flaws

- {"design": "firewall-p2-in-place-reload", "flaw": "The network's live preset is sent in every beat and rendered by the agent. An owner or admin can change the preset at any time, so a PUT, or the first P2 beat, rewrites running agents without the separate yes the decision requires. Agents of v0.11.31 and earlier always wrote tcp whatever the preset, so on an 'all' network the first beat widens them silently.", "evidence": "docs/wiki/decisions/firewall-rule-sets.md:57 (a new enrollment gets the preset; agents up to v0.11.31 always write tcp; the tab must not claim enforcement); docs/wiki/concepts/firewall-rules.md:34 (a changed preset reaches a device only by re-enrolling) and :130 (widening existing agents is a separate yes); internal/api/firewall.go:126-174 (owner and admins can PUT a preset at any time); design 1 deviation 5 and server item 11 (db.FirewallStore.NetworkFirewall every beat)."}
- {"design": "firewall-phase-2-apply", "flaw": "'Source: any node' cannot be expressed. The agent rule carries SourceAddr only, and validation requires a /32 inside the enrollment /24, so the concept's 'source is any node or one node' is refused or dropped. The server has no field for a group source.", "evidence": "Design 2 agent_changes item 5 (firewallRule {Proto, Port, SourceAddr}) and validation item 1; docs/wiki/concepts/firewall-rules.md:60; docs/wiki/decisions/firewall-rule-sets.md:23 (preset all means every protocol from every node)."}
- {"design": "firewall-phase-2-apply", "flaw": "The validator is not the production construction path. The exported NewFirewall plus AddFirewallRulesFromConfig pair skips the action and conntrack setup that NewFirewallFromConfig does, while the concept asks for a scratch NewFirewallFromConfig. The live action check (apply step 6) needs a running-action accessor that the vendor change list does not add. GetRuleHash covers only the rule text, so action flags are not verified by the hash.", "evidence": "vendor/github.com/slackhq/nebula/firewall.go:195-243 (NewFirewallFromConfig sets timeouts, default_local_cidr_any, inbound and outbound actions); firewall.go:302-305 (GetRuleHash hashes f.rules only); docs/wiki/concepts/firewall-rules.md:80; cert.UnmarshalCertificateFromPEM at vendor/.../cert/pem.go:88 shows the exported pair is buildable without a patch, so the gap is fidelity, not feasibility."}
- {"design": "firewall-phase-2-apply", "flaw": "Three gaps in the apply path. (a) No total port-span cap: 1-65535 is allowed per rule, and addRule allocates one entry per port, so 100 such rules cost millions of entries on every agent. (b) A write failure after a successful reload leaves the running rules ahead of the file, with no rollback; a restart inside that window reloads the old file. (c) Validation step 6 is true by construction, because the candidate is built from the file, so a drift between file and running config (values written but not loaded) is never refused.", "evidence": "Design 2 validation items 1 and 6, apply step 7 ('safe to retry'); design 1 risk 6 (the 4096 cap) and its step e (drift guard) and step i (rollback); internal/client/routes.go:56-64 (persist, then reloadNebula); vendor firewall.go:655-660 (one entry per port)."}
- {"design": "both designs", "flaw": "Both drop the approved item to extend internal/e2etest/peer.go, which the concept calls a prerequisite for P2. Both also rely on amending the one-file-per-patch rule for interface.go and outside.go, and the proposed patch-check does not exist yet. The Makefile checks neither independence nor order today.", "evidence": "docs/wiki/concepts/firewall-rules.md:110; patches/README.md:5 (no file is in two patches); patch-vendor per the code map (no independence or order check)."}

### Ideas to graft

- From the in-place design: validate with an in-package Control.CheckFirewall that calls NewFirewallFromConfig under recover. Put it in a new firewall_state.go inside firewall-swap.patch, so control-api.patch and the tracked control.go stay untouched, and the scratch build is the production path the concept asks for (firewall-rules.md:80).
- From the in-place design: a drift guard. Refuse with errPendingRestart when the non-firewall keys of the file differ from the text last loaded, and write nothing.
- From the in-place design: rollback. If the write of nebula.yaml fails after a successful reload, reload the old text before returning, so running rules and the file agree.
- From the in-place design: the group model (group node or a /32, admin refused) and a total port-span cap of 4096 across all rules, enforced at the server PUT as well.
- Compute FirewallRuleHash over the rule text plus the inbound and outbound action flags (and default_local_cidr_any), in one accessor in firewall_state.go. The scratch and the live check then use the same function, and no second accessor is needed.
- Keep from the phase-2 design: startMu TryLock so a beat never waits on a start, the fingerprint fast path, a rules-only heartbeat, and the preset stored per enrollment with a presetOf classifier tested on every historical block (testdata/firewall). The classifier is what stops a silent widening of legacy agents.
- The ack should carry the enrollment's preset as well as version and fingerprint, so the dashboard can report what each agent runs (firewall-rules.md:57). Count ReloadConfigString calls with our own wrapper rather than RegisterReloadCallback, which the upstream survey lists as changing in v1.12.0.

### Recommendation

Build firewall-phase-2-apply (Design 2), with the grafts above. The decisive point is preset semantics. Design 1 puts the network's live preset in every beat, and an owner or admin can change it at any time (internal/api/firewall.go:126-174). A PUT, or the first P2 beat, would then rewrite running agents without the separate yes the decision requires (firewall-rules.md:130). Agents up to v0.11.31 always wrote tcp, so an 'all' network would widen them silently. Design 2 keeps the enrollment-time preset, at the cost of one field and a classifier that legacy blocks need anyway.

Changes before building: (1) the validator is Control.CheckFirewall in a new firewall_state.go inside firewall-swap.patch, calling NewFirewallFromConfig under recover, so control-api.patch and the tracked control.go stay untouched; (2) the candidate's non-firewall keys must equal the text last loaded, else refuse (drift guard), and a failed write reloads the old text; (3) a source is group node or a /32, admin is refused, and total ports are capped at 4096; (4) FirewallRuleHash covers the rule text and the action flags, so the live check needs no second accessor; (5) keep TryLock and the fingerprint fast path, and have the ack carry the enrollment's preset.

Before merge, for both designs: build patch-check and run it in both orders for interface.go and outside.go; ask the user to approve dropping the peer.go extension that the concept calls a prerequisite (firewall-rules.md:110); and decide explicitly whether a preset change should ever reach a running agent (my default is no, until a separate yes).
