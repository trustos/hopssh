---
type: concept
title: Firewall rules between devices (proposal)
status: partly built
last_compiled: 2026-11-27
sources:
  - internal/client/enroll.go
  - internal/client/cli_join.go
  - internal/client/keepalive.go
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
- Built so far (2026-11-27, not released): golden tests of every block (P0), and a firewall preset per network that the agent reads once, at enrollment. New networks allow every protocol between agents, older ones keep TCP only. The rest is not built. The research and two reviews are in [raw/firewall-rules](../raw/firewall-rules/).

## Today

- **Where the rules come from.** Each agent's `nebula.yaml` gets its `firewall:` block once, at enrollment: `agentFirewallYAML` in `internal/client/enroll.go`, from the network's preset (`networks.firewall_preset`, migration 009), which the token, device-flow and bundle answers carry as `firewall`.
  - `tcp`: TCP on every port from cert group `node`, TCP 41820 from `admin`, ICMP. Every network created before migration 009 has it, and so does an answer without the field (a server before 009) or with a value the agent does not know.
  - `all`: the same with every protocol from `node`, and `inbound_action: drop` written out. Networks created from 009 on get it (the column's default; `NetworkStore.Create` does not name it).
  - Agents before this change ignore the field and write `tcp`.
- **Nothing changes the block later.** `ensureP2PConfig` and the other writers keep it as it is (`TestFirewall_WritersKeepIt`). A changed preset or rule reaches a device only if it enrolls again. Moving existing networks is a separate decision (item 4 of the plan, later).
- **Clients and the lighthouse.** `hop client join` allows ICMP and, since 2026-11-27, TCP 41820 from `node` inbound (`clientNebulaConfig`, see item 8). It reads no preset. The control plane's lighthouse allows everything (`internal/mesh/network_manager.go:539-547`).
- **Groups.** Every agent cert has the group `node` (`internal/api/renew.go:137`, `enroll.go:180`, `device.go:191`, `bundles.go:80`). Only the control plane's cert has `admin` (`networks.go:101`). No group is stored in the database. The `admin` rule on 41820 adds nothing, because `node` already covers every TCP port and the control plane is in `node` too.
- **Ports are bounded** (2026-11-27). Nebula keeps an entry for every port of a range, for every rule: one rule for 1-65535 took 50 MB on each device, a hundred of them 1.7 GB, a hundred of 1-4096 108 MB (measured on the Mac with vendored Nebula). The ports of a network's own rules and its attached sets together are at most `fwpolicy.MaxPorts` (4096, about 3 MB). The server checks this on a save, on a set's change in every network it is attached to, and on an attach. "any" is one entry.
- **No way to change a running firewall.** The heartbeat answer carries peers and routes. The renewal answer carries three relay and punch settings. A route change closes and restarts Nebula (`routes.go:63`).
- **Nebula's in-place reload is never used.** Nebula has `config.ReloadConfigString`, and its firewall callback (`vendor/.../interface.go:462-510`) keeps the conntrack table. An open flow is checked again on its next packet and cut only if no rule allows it any more. hopssh keeps no `*config.C` after start (`internal/client/nebula.go:67,129`), so nothing calls the reload.
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
8. **Ack.** The next heartbeat carries `firewall: {fingerprint, error}`: the fingerprint it runs (empty for its enrollment block), or why it refused. The tab shows each device as applied, pending, refused, or not enforced (an agent that sends no ack).
   - The same fingerprint and the same text again: nothing is done.
9. It never falls back to close-and-start with an unchecked file: that would leave no mesh at all.
10. **The swap.** Nebula swaps `f.firewall` with a plain pointer write (`interface.go:502`) while the packet threads read it (finding 6 below). Patch `firewall-swap` makes it an `atomic.Pointer`, with a `-race` test that is red without it. A packet may still take the drop action from one firewall and the reject flag from the next; both are valid.

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

**Phases, red test first in each:**

| Phase | What | Gate |
|---|---|---|
| P0 (done 2026-11-27) | golden tests of every block written so far; no change in behaviour | `internal/client/firewall_golden_test.go`: each block in `testdata/firewall/` parses with Nebula's own parser and allows what the test pins; today's agent and client templates and a round-tripped production file give the newest; no rewrite by the agent changes the firewall |
| P1 | table, `GET`/`PUT`, audit, tab with the three choices and the rule table | `TestE2E_Access`, `TestE2E_History` |
| P2 | heartbeat field, validation, in-place reload, ack | E2E with two real Nebula peers: a removed rule stops new and open flows within one heartbeat; ICMP keeps its tunnel with no new handshake; a rejected policy leaves the file byte for byte and tunnels up; under `-race` |
| P3 | device switch, "who can reach" | E2E from the CLI and the local API |
| later | tags and roles in certs, a "renew now" field, outbound rules, export as a file, clients on a heartbeat | — |

`internal/e2etest/peer.go` fixes each test peer's firewall to allow all and keeps no config, so P2 needs that harness extended first.

## Found on the way (not about rules, true today)

Each was checked in the code on 2026-11-27.

1. **(Fixed 2026-11-27: nothing is served while Nebula is down.)** **The fallback listener is open on every interface.** When `nebula.yaml` is missing or every Nebula mode fails, the agent serves its API on `:41820` on the OS stack (`internal/client/client.go:266,285`, `serverset.go:75`), outside any firewall. The bearer token is then the only gate on `/exec`, `/shell` and `/upload`. The fallback exists so renewal can reach the server, and that needs no listener. Fix: bind to `127.0.0.1`, or serve nothing.
2. **(Fixed 2026-11-27: `released_node_ips`, migration 008.)** **A deleted node's address is given out again while its cert still works.** `NextNodeIndex` takes the highest address among the nodes still in the table (`internal/db/nodes.go:409-430`). Deleting the newest node frees its address for the next enrollment, and the deleted node's cert is valid for up to 24 h more (pinned: `KnownGap` in `TestE2E_DeletedPeer`). Two valid certs can then carry one address. Fix: a per-network counter that never goes back; later, revocation with `pki.blocklist`. The coding rule ("MAX, not COUNT") assumed that MAX never goes down.
3. **(Fixed 2026-11-27: every writer holds `lockNebulaYAML` from its read to its write, `atomicWrite` uses a temp file of its own and syncs it before the rename, each write also updates `nebula.yaml.bak`, which `startInstance` restores from when the file is damaged, and routes are saved only after the rewrite; `internal/client/nebula_yaml.go`.)** **The routes writer of `nebula.yaml` takes no lock and is not atomic** (`internal/client/routes.go:173`). Seven call sites write the file, four at runtime; `ensureP2PConfig` and `upgradeTunMode` share `startMu`, the renewal writer has `certInstallMu`, routes has nothing. The boot-time port healer is a second plain `os.WriteFile` (`listenport.go:73`), and `atomicWrite` uses a fixed `<path>.tmp`, so two writers at once share one temporary file. No lock covers the file as such; one has to be made. It runs on the heartbeat goroutine, which starts before `startInstance` is done. A crash during its write leaves a truncated file, and nothing repairs it: the routes are saved first, so later beats see no change and never rewrite. The renewal and routes writers can also undo each other's values. Checked by reading, not reproduced.
4. **(Fixed 2026-11-27: create and delete accept the owner and admin members, plain members get 403; the dashboard shows Add and Delete to admins only.)** **The DNS buttons and the API disagree.** "Add" is shown to role-admin members and "Delete" to every member; create and delete accept only the owner (`internal/api/dns.go:106,170`). The invite text promises admins DNS management.
5. **Much is not audited.** Capabilities, routes and rename (`internal/api/proxy.go`) go to `network_events` only, without who did it. Also missing from `audit_log`: network create and delete, member and role changes, invites, DNS, enrollment, device authorisation, bundle download.
6. **Nebula swaps the firewall with a plain pointer write** (`vendor/.../interface.go:502`), while the packet threads read it (`outside.go:521`, `inside.go:75`). The spike's `-race` run under traffic reports it (10 and 15 reports in two runs, 0 without reloads). Upstream v1.12.0 is the same. Latent today: only a SIGHUP with a changed firewall section reaches it, and hopssh sends neither. P2 needs a patch with `atomic.Pointer`.
7. **(Fixed 2026-11-27: comment corrected, the field marked unused.)** **Stale comment.** `internal/client/renew.go:62` says the heartbeat is "every 5 minutes". It is 60 s ±10 % after the end of the previous POST (`renew.go:71,1210`). The `HeartbeatInterval` config field is read by nothing.

8. **(Fixed 2026-11-27: the client block allows TCP 41820 from `node`; `TestE2E_KeepaliveReachesClient`, two real Nebula peers, was red with succeeded 0 of 1.)** Leaving clients out of the probe was not possible: a client's node has type `node`, its cert group `node` and no `client_type`, as an agent from before v0.10.97 has, so an agent can't tell one from the other; the server would have to mark them, and old clients would still be missed. The opened port exposes nothing, since a client runs no listener on the mesh. A client gets the new block when `hop client join` runs again, which enrolls it afresh each time. **Agents probe clients on a port that clients drop** (read in the code 2026-11-27, then shown). The keepalive dials TCP 41820 on every peer in the hostmap except lighthouses (`internal/client/keepalive.go:338-385`; `pathQualityRecent` always answers false). A `hop client join` device allows inbound ICMP only and runs no listener, so its Nebula drops the SYN and the dial times out after 2 s, which counts as dead (only "refused" counts as alive, `keepalive.go:126`). An agent whose only peers are clients sees every cycle as stuck and restarts the network with the backoff of `recovery.go`. Fix: allow TCP 41820 from `node` on clients (a closed port then answers "refused"), or leave clients out of the probe; a lab test first.

(Done 2026-11-27. Three agent blocks: 80fdbaa, TCP from group `server`; 791ac0b, TCP 41820 from `admin` and TCP from `user` and `agent`; 41ce077 to today, TCP 41820 from `admin` and TCP from `node` (00a4e19 changed only the arguments). One client block, ICMP only, from 3f30d27 to today. The one renderer was left out, as the plan review advised; only the client's text moved into `clientNebulaConfig` so a test can render it.) P0 must parse more than today's agent block: the client block (`cli_join.go`, ICMP only), the round-tripped form in `testdata/`, and older agent blocks with groups `[server]` and `[user, agent]`. `git log -G 'groups:'` gives 169ee66, c524fa2, 80fdbaa, 791ac0b, 3f30d27, 41ce077, 00a4e19 and a94baf7, but c524fa2 and a94baf7 touch no enroll file (the lighthouse's block and the multi-network work). Before a37656f the agent's template lived in `cmd/agent/enroll.go` and before that partly in `internal/api/enroll.go`; golden tests read old revisions with `git show` on those paths.

## Decide

- **Default for new networks.** (Decided 2026-11-27: everything between agents, UDP included, for networks created from migration 009 on; built as above.) Allow everything, UDP too (as Tailscale, ZeroTier, NetBird, Headscale and Netmaker do), named UDP ports only (mosh, QUIC, WireGuard, Screen Sharing), or ping-only roles (as Defined does)? NetBird calls its allow-all policy a start and advises against all-protocol rules. Existing networks keep what they have unless an admin changes it. "New networks only" needs no P2: a network column read at enrollment, sent in the enrollment answer, and rendered by the agent; old agents ignore the field and write today's block. Widening existing agents (a heal in `ensureP2PConfig`) is a separate yes: it needs the writer lock of item 3 first, a classifier tested against every historical block, a way to switch it off, and the firewall version in the heartbeat so the dashboard can tell who has which.
- **What the new default adds.** On a system install, TCP to every port is open between agents today. UDP adds the services that listen on UDP on all interfaces and answer anyone: on Windows LLMNR (5355) and NetBIOS (137, 138); on Linux rpcbind (111) and NFS if enabled; Syncthing (21027, 22000), Jellyfin discovery (7359), WireGuard and QUIC where they run. A unicast packet to such a port arrives; whether multicast crosses the mesh was not checked (`drop_local_broadcast` is about broadcasts, `tun.drop_multicast` is off). Not checked on the machines.
- **Per network or per account.** Roadmap #8 wants per account. Decide before P1. Of the products read ([raw/firewall-rules/sharing-and-defaults.md](../raw/firewall-rules/sharing-and-defaults.md)), only Defined reuses rules across networks: roles, tags and rules live on the account, each network keeps its own CA. None documents inheritance or override at the rule level; Tailscale and NetBird have a device switch that blocks incoming, which does override. With allow-only rules a host gets the union. hopssh has no account object: a network belongs to a user (`networks.user_id`), so "the account's rules" are one user's. To settle with it: who may attach, detach and edit a shared set (an edit changes every network it is attached to at once); rules name nodes by their ID within one network, never by name or address (a hostname is not unique, and addresses come back after 48 h when a network is full); a reference to a missing node is refused when the set is attached; detaching is a removal and cuts open flows like one; the device switch of P3 is a deny that wins over the union.
- **Should UDP be allowed now, in code, before any of this?** One line plus healing in `ensureP2PConfig`. The tab would then show the same default.
