---
type: concept
title: Firewall rules between devices (proposal)
status: partly built
last_compiled: 2026-11-27
sources:
  - internal/client/enroll.go
  - internal/client/cli_join.go
  - internal/client/keepalive.go
  - internal/client/firewall_apply.go
  - internal/fwpolicy/delivery.go
  - internal/api/firewall.go
  - internal/client/serverset.go
  - internal/api/renew.go
  - internal/api/networks.go
  - internal/db/nodes.go
  - vendor/github.com/slackhq/nebula/interface.go
  - vendor/github.com/slackhq/nebula/firewall.go
  - raw/firewall-rules/
---

# Firewall rules between devices

**TL;DR.**
- Rules are set in the dashboard: one Firewall tab per network, with three choices on top ("devices talk freely", "only ping and admin", "custom") and a rule table behind "custom".
- The server sends the policy in the heartbeat answer. The agent writes its own `firewall:` block and loads it with Nebula's in-place reload, so tunnels stay up.
- Whatever the server sends, the agent keeps the rules that hopssh itself needs.
- A switch on the device ("block incoming") can only narrow access.
- Built so far (2026-11-27, not released): golden tests of every block (P0); a firewall preset per network that the agent reads at enrollment (new networks allow every protocol between agents, older ones keep TCP only); the Firewall tab with rules and shared rule sets (P1); and delivery (P2): a managed network's heartbeat answer carries each device's policy, the agent applies it in place and reports what it runs, and the tab shows it per device. P3 (2026-11-28) adds the device switch "Block incoming" and the "Who can reach this device" list. The research and two reviews are in [raw/firewall-rules](../raw/firewall-rules/).

## Today

- **Where the rules come from.** Each agent's `nebula.yaml` gets its `firewall:` block once, at enrollment: `agentFirewallYAML` in `internal/client/enroll.go`, from the network's preset (`networks.firewall_preset`, migration 009), which the token, device-flow and bundle answers carry as `firewall`.
  - `tcp`: TCP on every port from cert group `node`, TCP 41820 from `admin`, ICMP. Every network created before migration 009 has it, and so does an answer without the field (a server before 009) or with a value the agent does not know.
  - `all`: the same with every protocol from `node`, and `inbound_action: drop` written out. Networks created from 009 on get it (the column's default; `NetworkStore.Create` does not name it).
  - Agents before this change ignore the field and write `tcp`.
- **Only a managed network changes the block later** (P2, below). Otherwise `ensureP2PConfig` and the other writers keep it as it is (`TestFirewall_WritersKeepIt`). A network that nobody saved rules for or attached a set to sends nothing, and each device keeps the block it got at enrollment. Moving existing networks without that is a separate decision (item 4 of the plan, later).
- **Clients and the lighthouse.** `hop client join` allows ICMP and, since 2026-11-27, TCP 41820 from `node` inbound (`clientNebulaConfig`, see item 8). It reads no preset. The control plane's lighthouse allows everything (`internal/mesh/network_manager.go:539-547`).
- **Groups.** Every agent cert has the group `node` (`internal/api/renew.go:137`, `enroll.go:180`, `device.go:191`, `bundles.go:80`). Only the control plane's cert has `admin` (`networks.go:101`). No group is stored in the database. The `admin` rule on 41820 adds nothing, because `node` already covers every TCP port and the control plane is in `node` too.
- **Ports are bounded** (2026-11-27). Nebula keeps an entry for every port of a range, for every rule: one rule for 1-65535 took 50 MB on each device, a hundred of them 1.7 GB, a hundred of 1-4096 108 MB (measured on the Mac with vendored Nebula). The ports of a network's own rules and its attached sets together are at most `fwpolicy.MaxPorts` (4096, about 3 MB). The server checks this on a save, on a set's change in every network it is attached to, and on an attach. "any" is one entry.
- **Changing a running firewall** (P2). The heartbeat answer carries peers, routes and, for a managed network, `firewall`. A route change still closes and restarts Nebula (`routes.go:63`); a firewall change does not. The agent keeps the `*config.C` it started Nebula with (`loadNebulaConfig`, `internal/client/nebula.go`) and calls `ReloadConfigString` on it. Nebula's firewall callback (`vendor/.../interface.go`) keeps the conntrack table: an open flow is checked again on its next packet and cut only if no rule allows it any more.
- **System install vs in-app.** The firewall applies in both modes, before a packet reaches the device. The mode decides what there is to reach. With a system install (kernel TUN) every host service is reachable: sshd, Screen Sharing, anything on TCP. In-app (userspace) only the agent's own listeners are.

## What the others do

Sources and quotes: [raw/firewall-rules/competitors.md](../raw/firewall-rules/competitors.md), read 2026-11-27.

| | Where | Default | Live? | Device switch |
|---|---|---|---|---|
| Tailscale | admin console: JSON policy file, visual editor (beta since 2025-08) | allow all, until you write rules | seconds; open TCP survives | `--shields-up` |
| ZeroTier | Central, "Flow Rules" tab, its own language | allow all, until rules are enabled | not stated | none for inbound |
| Defined (Managed Nebula) | admin panel: roles and tags, a form per rule, inbound only | a new role allows only ICMP | within a minute; role or tag change means a new cert at next check-in | not stated |
| NetBird | dashboard, Access Control → Policies, form | a "Default" policy that allows all | without reconnecting | `--block-inbound` |
| Headscale | a policy file; Headplane and headscale-admin edit it in database mode | allow all with no policy | reload needed in file mode | as Tailscale |

All five configure rules in a web UI. NetBird is the closest match: self-hosted, a dashboard, forms instead of a policy language, and allow-only rules, so their order never matters.

## Proposal

**Model.**
- One policy per network, stored as a versioned row. A rule allows only: `{target, proto, port, source}`.
- `proto` is `tcp`, `udp`, `icmp` or `any`; `port` is `any`, `N` or `N-M`.
- To start, `target` is all agents or one node, and `source` is any node or one node.
- No row means today's rules, and the migration writes nothing.
- Tags and roles come later on the same row. Roadmap #8 wants rules shared across networks of an account, so decide the storage before building (see "Decide").

**Why not cert groups first.**
- A group sits in the cert. Changing one waits for the next renewal: up to about 14.4 h (`internal/client/renew.go:631-636`), with no way for the server to ask for one sooner.
- Rules in `nebula.yaml` take effect on the next heartbeat (60 s).
- Groups are the path for tags later, together with an "renew now" field in the heartbeat answer.

**Where and who.**
- A Firewall tab beside DNS (`frontend/src/routes/(app)/networks/[id]/+page.svelte:71,737-745`).
- `GET` for members, `PUT` for admins and the owner, with `If-Match: <version>` (412 when stale; built so in phase 1, see [[../decisions/firewall-rule-sets]]).
- Gate the `PUT` on `CheckAccess(...).CanAdmin()`; `CanAdmin` on a membership row alone locks the owner out.
- Every `PUT` is audited with the old and new version.
- `TestE2E_Access` must get rows for it, or it fails.

**Delivery and apply** (phase 2, decided 2026-11-27; two designs and two reviews in [raw/firewall-p2](../raw/firewall-p2/design-review-2026-11-27.md)).

1. **Which networks.** The heartbeat answer carries `firewall` only for a managed network: one with a `network_firewall` row. A save in the tab creates the row, and so does attaching a rule set (at version 0; the versions of [[../decisions/firewall-rule-sets]] don't change). The API says it as `managed`; in place since 2026-11-27, pinned by `TestE2E_Firewall`. A network without a row sends nothing, and each device keeps the block it got at enrollment.
   - So a device changes only after its owner or an admin saved the tab or attached a set, never because its agent was updated. Agents up to v0.11.31 wrote `tcp` whatever the preset, and an `all` network would otherwise widen them on the first beat.
   - Once managed, always managed: after the last set is detached, the preset goes out with no rules.
   - Old agents ignore the field.
2. **What.** Resolved for each node: `{fingerprint, preset, rules: [{proto, port, source}]}`.
   - Only rules whose target is empty or this node go out.
   - `source` is the source node's mesh address, or empty for any node.
   - A rule naming a deleted node is left out: it matches nothing, and the address may go to another node later.
   - The fingerprint is SHA-256 of preset and rules.
3. **The agent checks before it touches anything.** The preset must be known, each rule's proto and port must pass `fwpolicy`, a source must be empty or an address of its own network, and `fwpolicy.Ports` must be at most `MaxPorts`.
   - It renders its own template for the preset (`agentFirewallYAML`) plus one entry per rule: `group: node` or `cidr: <source>/32`, never `admin`.
   - No text from the server goes into the file, so what the agent always keeps (below) is always there.
4. **Nebula's own builder.** It replaces only the `firewall:` block of the text it last loaded. It builds a candidate with `NewFirewallFromConfig` through a new `Control.CheckFirewall`, under `recover`, and refuses on any error. `ReloadConfigString` would not report one: a rule Nebula rejects (`proto: foo`) only logs, the call returns nil and the old rules stay (spike, 2026-11-27). Reload callbacks return nothing (`vendor/.../interface.go:441-445`).
5. **Drift.** If the file outside `firewall:` differs from the text last loaded, the agent refuses and waits for the next start. A reload runs every callback, not only the firewall's: the PKI one re-reads the cert from disk, and routes, relay and punch settings written since the last load would be applied too.
6. **Reload.** `ReloadConfigString` runs on the `*config.C` that was passed to `nebula.Main`. It is kept on the mesh service and read under the lock `setSvc` uses, so a late reload never runs on a closed instance.
   - With `startMu.TryLock`: a beat that meets a start skips, and the next beat tries again.
   - With a 5 s deadline, as `runWithTimeout` of Phase DD.
7. **Then the file.** Only after the reload is `nebula.yaml` written, under `lockNebulaYAML`. If the write fails, the previous text is loaded again, so the file and the running rules agree.
8. **Ack.** The next heartbeat carries `firewall: {fingerprint, error}`: the fingerprint it runs (empty for its enrollment block), or why it refused. The tab shows each device as applied, pending, refused, or "no report" (an agent of v0.11.31 or older, or one not online since).
   - The same fingerprint and the same text again: nothing is done.
   - The server keeps one report per node (`node_firewall`, migration 012), with the time it was first made; a report that is not one (a fingerprint that is not 64 lower-case hex) is ignored, and the heartbeat still answers. A reason is cut to 300 characters, control characters become spaces.
9. It never falls back to close-and-start with an unchecked file: that would leave no mesh at all.
10. **The swap.** Nebula swapped `f.firewall` with a plain pointer write while the packet threads read it (finding 6 below). Patch `firewall-swap` (2026-11-27) makes it an `atomic.Pointer` and adds `Control.CheckFirewall` / `Control.FirewallRuleHash` for points 4 and 8. `TestFirewallSwapIsSafeToRead` failed 5 of 5 under `-race` without it, and passed 30 of 30 with it; `TestCheckFirewall` pins the silent reload of point 4. A packet may still take the drop action from one firewall and the reject flag from the next; both are valid.

**Built (2026-11-27, not released).** Server: `fwpolicy.Resolve`, `Delivered.Sum`, `Delivered.Check` (`internal/fwpolicy/delivery.go`), the heartbeat field and the reports (`internal/api/renew.go`, `internal/db/firewall.go`), the devices in the firewall answer (`internal/api/firewall.go`). Agent: `applyFirewall` in `internal/client/firewall_apply.go`, in the order of points 3 to 8. Each beat ends in one of three:
- **applied**: running and in the file; the report is the policy's fingerprint;
- **refused** (a check, Nebula's builder, a write of `nebula.yaml` that failed, after which the rules it ran are reloaded again): nothing changes; the report is the fingerprint it runs and the reason; the same policy is not tried again until it changes or Nebula restarts;
- **later** (a start in progress, `nebula.yaml` changed since the last load, a reload still in flight): nothing changes and the report stays; the next beat tries again. A reload over 5 s is later too, but it is not undone: it goes on alone and its rules apply when it returns. No other reload starts before that, and one built from the text before it is refused (`reloadInPlace`, `TestReloadInPlace_OneAtATime`, found in the review of [[deleted-devices]]).

When only the backup (`nebula.yaml.bak`) can't be written, the file and Nebula both have the policy: applied, with a line in the log.

`CheckFirewall` before the reload and the rule hash after it are defence in depth: no policy that passes `Delivered.Check` was found that Nebula's builder refuses, so no test fails without them.

Tests: `TestE2E_FirewallDelivery` in `internal/api` (the real server: who gets which rules, sets, reports, a deleted source) and in `internal/client` (two real Nebula peers: an added rule opens within one beat, a removed one stops a new and an open flow, three refusals leave `nebula.yaml` byte for byte, a start in progress and a changed file wait, a write that fails gives the old rules back, a backup that fails does not, the tunnel keeps its index, the keepalive still reaches the device), `TestManagedFirewall`, `TestE2E_FirewallApplied` in `cmd/agent` (the real server and an agent process: a saved rule reaches the device's `nebula.yaml` and its report reads applied), and `frontend/tests/firewall-tab.test.mjs` for the tab's statuses.

Known gap: the fingerprint an agent runs is kept in memory. After a restart of the agent it reports "" for a policy it refuses until one is applied again, although its file may still hold an earlier managed policy. The next policy it accepts (the same one included: the rule hash matches and nothing is reloaded) makes it known again.

**What a reload does to open flows** (spike, two userspace peers, 2026-11-27): a removed rule stops an open flow at its next packet, silently (no reset), and the tunnel stays (same indexes, no handshake). When the rule comes back, the old flow stays dead and a new connection works. Identical text changes nothing.

**What the agent always keeps, whatever the server sends.**
1. TCP 41820 from `admin`. The terminal, exec, proxy and port forwards reach the agent there.
2. TCP 41820 from `node`. The mesh keepalive of every peer dials it (`internal/client/keepalive.go:379`). Without it, a device whose only probed peers are all firewalled sees the network as stuck every ~4.5 min and restarts it, with the backoff of `recovery.go` (5 min, doubling to 6 h). One reachable peer is enough to prevent it; a refused dial counts as alive.
3. ICMP.
4. `inbound_action: drop`.

The server could already run commands on every agent through 41820, so a policy from it adds no trust. What a policy limits is other devices, and that is where the risk is in a shared network.

**On the device.**
- A switch "Block incoming from other devices", per network, in the desktop app and the CLI. It keeps the invariants above, so the owner can still manage the device.
- A read-only "Who can reach this device" list in the dashboard's node view, with the rule behind each line.

**Built (2026-11-28, not released).**
- **The switch** is the file `block-incoming` in the enrollment's directory (`internal/client/firewall_block.go`). Missing means off; one that can't be looked at counts as on. `hop-agent block-incoming on|off [--network <name>]` (`cli_block.go`), the desktop app's Settings → Networks → "Block incoming", and `POST /local/block-incoming` set it. The CLI finds the running agent through `local-api-addr` (0600) and its token file.
- **While it is on**, Nebula runs the "ping" preset without rules: what the agent always keeps. `nebula.yaml` keeps the network's policy. `loadNebulaConfig` puts the blocked section in place at every start, and a policy that comes meanwhile goes to the file only. Turning the switch on or off saves first, then reloads in place with the checks of P2, so no tunnel and no terminal session ends. A network that isn't running gets it at its next start. One in-place reload runs at a time, so a switch while an earlier one has not finished is saved and refused ("Saved, not in effect yet"). Every heartbeat gives Nebula the switch when it runs the other state (`reapplyIncomingBlocked`, before the report); until 2026-11-28 nothing did before Nebula's next start (found in review).
- **The report.** While the switch is on, every heartbeat carries `firewall: {fingerprint, error, blocked}`, also before any policy came. The server takes a heartbeat without a report to mean off (`FirewallStore.NoReport`), so an agent of v0.11.31 or older never shows as blocked. **Every heartbeat an agent sends must carry the report.** The one sent at start-up (`warmPeersFromHeartbeat`) didn't. For one beat after every restart the dashboard said "not blocked". The real-process test found it (2026-11-28); the client test's `silent` counter pins it.
- **Who can reach it.** `GET /api/networks/{id}/nodes/{nodeId}/firewall` needs the view right. It returns lines, each with its origin: always, preset, network or set. They are the rules `fwpolicy.Reaching` gives that device, so a deleted source is left out as in the policy. The dashboard shows them in a node's expanded row (`node-reach.svelte`; the logic is in `frontend/src/lib/firewall-reach.ts`). For a device that blocks incoming, everything but hopssh's own lines is struck through. The Firewall tab shows such a device as "Blocks incoming".
- **Tests:**
  - `TestE2E_BlockIncoming` in `internal/client`, with two real Nebula peers: on, with its own heartbeat at once; a policy that comes while on; a restart; switched off while `nebula.yaml` has a rule Nebula refuses (the switch is off, Nebula keeps blocking, and the report says blocked: it follows what Nebula runs, not the file); off; switched on while down; requests it refuses; and no heartbeat that says off while it is on.
  - `TestE2E_BlockIncoming_AfterBusyReload`: the switch on, then off, while a reload of over 5 s has not finished; each is refused, and the next heartbeat gives it to Nebula. Two planted bugs, both caught: no retry from the heartbeat, and one that only ever switches on.
  - `TestE2E_BlockIncoming` in `cmd/agent`, with the real server and an agent process driven by the CLI.
  - `TestE2E_FirewallReach` in `internal/api`.
  - `frontend/tests/firewall-reach.test.mjs`, and the tab's test.
- **Accepted:**
  - A rule "from X" with no target shows on X's own list, although X never reaches itself over the network.
  - Right after a restart the agent reports fingerprint "" until its first beat has checked the policy again.

**Phases, red test first in each:**

| Phase | What | Gate |
|---|---|---|
| P0 (done 2026-11-27) | golden tests of every block written so far; no change in behaviour | `internal/client/firewall_golden_test.go`: each block in `testdata/firewall/` parses with Nebula's own parser and allows what the test pins; today's agent and client templates and a round-tripped production file give the newest; no rewrite by the agent changes the firewall |
| P1 (done 2026-11-27) | table, `GET`/`PUT`, audit, tab with the three choices and the rule table | `TestE2E_Access`, `TestE2E_History`, `TestE2E_Firewall` |
| P2 (done 2026-11-27) | heartbeat field, validation, in-place reload, ack | `TestE2E_FirewallDelivery` (server, and two real Nebula peers): a removed rule stops new and open flows within one heartbeat; the tunnel keeps its index, no new handshake; a rejected policy leaves the file byte for byte and the rules it ran; under `-race` |
| P3 (done 2026-11-28) | device switch, "who can reach" | `TestE2E_BlockIncoming` (two real Nebula peers through the local API; the real server and an agent process through the CLI), `TestE2E_FirewallReach` |
| later | tags and roles in certs, a "renew now" field, outbound rules, export as a file, clients on a heartbeat | — |

`internal/e2etest/peer.go` still fixes each test peer's firewall to allow all. P2's two-peer test uses `firewallMesh` of `internal/client` instead, with the heartbeat answered by the test (`e2eMesh.onHeartbeat`).

## Found on the way (not about rules, true today)

Each was checked in the code on 2026-11-27.

1. **(Fixed 2026-11-27: nothing is served while Nebula is down.)** **The fallback listener is open on every interface.** When `nebula.yaml` is missing or every Nebula mode fails, the agent serves its API on `:41820` on the OS stack (`internal/client/client.go:266,285`, `serverset.go:75`), outside any firewall. The bearer token is then the only gate on `/exec`, `/shell` and `/upload`. The fallback exists so renewal can reach the server, and that needs no listener. Fix: bind to `127.0.0.1`, or serve nothing.
2. **(Fixed 2026-11-27: `released_node_ips`, migration 008.)** **A deleted node's address is given out again while its cert still works.** `NextNodeIndex` takes the highest address among the nodes still in the table (`internal/db/nodes.go:409-430`). Deleting the newest node frees its address for the next enrollment, and the deleted node's cert is valid for up to 24 h more (pinned: `KnownGap` in `TestE2E_DeletedPeer`). Two valid certs can then carry one address. Fix: a per-network counter that never goes back; later, revocation with `pki.blocklist`. The coding rule ("MAX, not COUNT") assumed that MAX never goes down.
3. **(Fixed 2026-11-27: every writer holds `lockNebulaYAML` from its read to its write, `atomicWrite` uses a temp file of its own and syncs it before the rename, each write also updates `nebula.yaml.bak`, which `startInstance` restores from when the file is damaged, and routes are saved only after the rewrite; `internal/client/nebula_yaml.go`.)** **The routes writer of `nebula.yaml` takes no lock and is not atomic** (`internal/client/routes.go:173`). Seven call sites write the file, four at runtime; `ensureP2PConfig` and `upgradeTunMode` share `startMu`, the renewal writer has `certInstallMu`, routes has nothing. The boot-time port healer is a second plain `os.WriteFile` (`listenport.go:73`), and `atomicWrite` uses a fixed `<path>.tmp`, so two writers at once share one temporary file. No lock covers the file as such; one has to be made. It runs on the heartbeat goroutine, which starts before `startInstance` is done. A crash during its write leaves a truncated file, and nothing repairs it: the routes are saved first, so later beats see no change and never rewrite. The renewal and routes writers can also undo each other's values. Checked by reading, not reproduced.
4. **(Fixed 2026-11-27: create and delete accept the owner and admin members, plain members get 403; the dashboard shows Add and Delete to admins only.)** **The DNS buttons and the API disagree.** "Add" is shown to role-admin members and "Delete" to every member; create and delete accept only the owner (`internal/api/dns.go:106,170`). The invite text promises admins DNS management.
5. **Much is not audited.** Capabilities, routes and rename (`internal/api/proxy.go`) go to `network_events` only, without who did it. Also missing from `audit_log`: network create and delete, member and role changes, invites, DNS, enrollment, device authorisation, bundle download.
6. **(Fixed 2026-11-27: patch `firewall-swap`.)** **Nebula swaps the firewall with a plain pointer write** (`vendor/.../interface.go:502`), while the packet threads read it (`outside.go:521`, `inside.go:75`). The spike's `-race` run under traffic reports it (10 and 15 reports in two runs, 0 without reloads). Upstream v1.12.0 is the same. Latent today: only a SIGHUP with a changed firewall section reaches it, and hopssh sends neither. P2 needs a patch with `atomic.Pointer`.
7. **(Fixed 2026-11-27: comment corrected, the field marked unused.)** **Stale comment.** `internal/client/renew.go:62` says the heartbeat is "every 5 minutes". It is 60 s ±10 % after the end of the previous POST (`renew.go:71,1210`). The `HeartbeatInterval` config field is read by nothing.

8. **(Fixed 2026-11-27: the client block allows TCP 41820 from `node`; `TestE2E_KeepaliveReachesClient`, two real Nebula peers, was red with succeeded 0 of 1.)** Leaving clients out of the probe was not possible: a client's node has type `node`, its cert group `node` and no `client_type`, as an agent from before v0.10.97 has, so an agent can't tell one from the other; the server would have to mark them, and old clients would still be missed. The opened port exposes nothing, since a client runs no listener on the mesh. A client gets the new block when `hop client join` runs again, which enrolls it afresh each time. **Agents probe clients on a port that clients drop** (read in the code 2026-11-27, then shown). The keepalive dials TCP 41820 on every peer in the hostmap except lighthouses (`internal/client/keepalive.go:338-385`; `pathQualityRecent` always answers false). A `hop client join` device allows inbound ICMP only and runs no listener, so its Nebula drops the SYN and the dial times out after 2 s, which counts as dead (only "refused" counts as alive, `keepalive.go:126`). An agent whose only peers are clients sees every cycle as stuck and restarts the network with the backoff of `recovery.go`. Fix: allow TCP 41820 from `node` on clients (a closed port then answers "refused"), or leave clients out of the probe; a lab test first.

(Done 2026-11-27. Three agent blocks: 80fdbaa, TCP from group `server`; 791ac0b, TCP 41820 from `admin` and TCP from `user` and `agent`; 41ce077 to today, TCP 41820 from `admin` and TCP from `node` (00a4e19 changed only the arguments). One client block, ICMP only, from 3f30d27 to today. The one renderer was left out, as the plan review advised; only the client's text moved into `clientNebulaConfig` so a test can render it.) P0 must parse more than today's agent block: the client block (`cli_join.go`, ICMP only), the round-tripped form in `testdata/`, and older agent blocks with groups `[server]` and `[user, agent]`. `git log -G 'groups:'` gives 169ee66, c524fa2, 80fdbaa, 791ac0b, 3f30d27, 41ce077, 00a4e19 and a94baf7, but c524fa2 and a94baf7 touch no enroll file (the lighthouse's block and the multi-network work). Before a37656f the agent's template lived in `cmd/agent/enroll.go` and before that partly in `internal/api/enroll.go`; golden tests read old revisions with `git show` on those paths.

## Decide

- **Default for new networks.** (Decided 2026-11-27: everything between agents, UDP included, for networks created from migration 009 on; built as above.) Allow everything, UDP too (as Tailscale, ZeroTier, NetBird, Headscale and Netmaker do), named UDP ports only (mosh, QUIC, WireGuard, Screen Sharing), or ping-only roles (as Defined does)? NetBird calls its allow-all policy a start and advises against all-protocol rules. Existing networks keep what they have unless an admin changes it. "New networks only" needs no P2: a network column read at enrollment, sent in the enrollment answer, and rendered by the agent; old agents ignore the field and write today's block. Widening existing agents (a heal in `ensureP2PConfig`) is a separate yes: it needs the writer lock of item 3 first, a classifier tested against every historical block, a way to switch it off, and the firewall version in the heartbeat so the dashboard can tell who has which.
- **What the new default adds.** On a system install, TCP to every port is open between agents today. UDP adds the services that listen on UDP on all interfaces and answer anyone: on Windows LLMNR (5355) and NetBIOS (137, 138); on Linux rpcbind (111) and NFS if enabled; Syncthing (21027, 22000), Jellyfin discovery (7359), WireGuard and QUIC where they run. A unicast packet to such a port arrives; whether multicast crosses the mesh was not checked (`drop_local_broadcast` is about broadcasts, `tun.drop_multicast` is off). Not checked on the machines.
- **Per network or per account.** Roadmap #8 wants per account. Decide before P1. Of the products read ([raw/firewall-rules/sharing-and-defaults.md](../raw/firewall-rules/sharing-and-defaults.md)), only Defined reuses rules across networks: roles, tags and rules live on the account, each network keeps its own CA. None documents inheritance or override at the rule level; Tailscale and NetBird have a device switch that blocks incoming, which does override. With allow-only rules a host gets the union. hopssh has no account object: a network belongs to a user (`networks.user_id`), so "the account's rules" are one user's. To settle with it: who may attach, detach and edit a shared set (an edit changes every network it is attached to at once); rules name nodes by their ID within one network, never by name or address (a hostname is not unique, and addresses come back after 48 h when a network is full); a reference to a missing node is refused when the set is attached; detaching is a removal and cuts open flows like one; the device switch of P3 is a deny that wins over the union.
- **Should UDP be allowed now, in code, before any of this?** One line plus healing in `ensureP2PConfig`. The tab would then show the same default.
