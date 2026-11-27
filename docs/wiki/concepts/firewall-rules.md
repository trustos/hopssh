---
type: concept
title: Firewall rules between devices (proposal)
status: proposed
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
- Nothing of this is built. The research and two reviews are in [raw/firewall-rules](../raw/firewall-rules/); the open decision is in [[../open-questions]], "UDP between devices".

## Today

- **Where the rules come from.** Each agent's `nebula.yaml` gets its `firewall:` block once, at enrollment (`internal/client/enroll.go:655-674`). It allows inbound TCP on every port from cert group `node`, TCP 41820 from `admin`, and ICMP. There is no UDP rule.
- **Nothing changes the block later.** `ensureP2PConfig` and the other writers keep it as it is. A changed rule in the code reaches a device only if it enrolls again.
- **Clients and the lighthouse.** `hop client join` allows only ICMP inbound (`cli_join.go:206-214`). The control plane's lighthouse allows everything (`internal/mesh/network_manager.go:539-547`).
- **Groups.** Every agent cert has the group `node` (`internal/api/renew.go:137`, `enroll.go:180`, `device.go:191`, `bundles.go:80`). Only the control plane's cert has `admin` (`networks.go:101`). No group is stored in the database. The `admin` rule on 41820 adds nothing, because `node` already covers every TCP port and the control plane is in `node` too.
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
- `GET` for members, `PUT` for admins and the owner, with `If-Match: <version>` (409 when stale).
- Gate the `PUT` on `CheckAccess(...).CanAdmin()`; `CanAdmin` on a membership row alone locks the owner out.
- Every `PUT` is audited with the old and new version.
- `TestE2E_Access` must get rows for it, or it fails.

**Delivery and apply.**
1. The heartbeat answer gets `firewall: {version, rules}` when a row exists, like `routes` (`internal/api/renew.go:491-496`).
   - Old agents ignore the field. The tab must show such nodes as "not enforced, agent too old", not as "pending".
2. The server resolves node IDs to addresses, and the agent checks them.
3. The agent validates the policy itself before it touches anything. `ReloadConfigString` does not do it: a rule Nebula rejects (`proto: foo`) only logs an error, the call returns nil and the old rules stay (spike, 2026-11-27). Group names are not checked by Nebula at all. A patch cannot simply make `reloadFirewall` return its error: reload callbacks return nothing (`vendor/.../interface.go:441-445`), and the only error `ReloadConfigString` returns comes from parsing (`config/config.go:175-195`). So: build a scratch `NewFirewallFromConfig` from the new text first and refuse on its error, plus our own check of groups and ports.
4. It builds the new text from what is running, with only `firewall:` replaced, and calls `ReloadConfigString` on the same `*config.C` that was passed to `nebula.Main` (today none is kept: a field on the mesh service, read under the lock `setSvc` uses, so a late reload never runs on a closed instance). The reload runs every callback, not only the firewall's: the PKI one re-reads the cert files from disk each time, and anything else written to `nebula.yaml` since the last load (routes, relay and punch settings) is applied too.
5. Only after success does it rename the new file over `nebula.yaml`.
6. On failure, the running rules and the file stay as they were, and the next heartbeat reports the error.
7. It never falls back to close-and-start with an unchecked file: that would leave no mesh at all.

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
| P0 | one renderer for the agent and client blocks; no change in behaviour | golden tests from today's template and from older templates (`git log -G 'groups:'`), compared as parsed rules, not text |
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

8. **Agents probe clients on a port that clients drop** (read in the code 2026-11-27, not run). The keepalive dials TCP 41820 on every peer in the hostmap except lighthouses (`internal/client/keepalive.go:338-385`; `pathQualityRecent` always answers false). A `hop client join` device allows inbound ICMP only and runs no listener, so its Nebula drops the SYN and the dial times out after 2 s, which counts as dead (only "refused" counts as alive, `keepalive.go:126`). An agent whose only peers are clients sees every cycle as stuck and restarts the network with the backoff of `recovery.go`. Fix: allow TCP 41820 from `node` on clients (a closed port then answers "refused"), or leave clients out of the probe; a lab test first.

P0 must parse more than today's agent block: the client block (`cli_join.go`, ICMP only), the round-tripped form in `testdata/`, and older agent blocks with groups `[server]` and `[user, agent]`. `git log -G 'groups:'` gives 169ee66, c524fa2, 80fdbaa, 791ac0b, 3f30d27, 41ce077, 00a4e19 and a94baf7, but c524fa2 and a94baf7 touch no enroll file (the lighthouse's block and the multi-network work). Before a37656f the agent's template lived in `cmd/agent/enroll.go` and before that partly in `internal/api/enroll.go`; golden tests read old revisions with `git show` on those paths.

## Decide

- **Default for new networks.** Allow everything, UDP too (as Tailscale, ZeroTier, NetBird, Headscale and Netmaker do), named UDP ports only (mosh, QUIC, WireGuard, Screen Sharing), or ping-only roles (as Defined does)? NetBird calls its allow-all policy a start and advises against all-protocol rules. Existing networks keep what they have unless an admin changes it. "New networks only" needs no P2: a network column read at enrollment, sent in the enrollment answer, and rendered by the agent; old agents ignore the field and write today's block. Widening existing agents (a heal in `ensureP2PConfig`) is a separate yes: it needs the writer lock of item 3 first, a classifier tested against every historical block, a way to switch it off, and the firewall version in the heartbeat so the dashboard can tell who has which.
- **What the new default adds.** On a system install, TCP to every port is open between agents today. UDP adds the services that listen on UDP on all interfaces and answer anyone: on Windows LLMNR (5355) and NetBIOS (137, 138); on Linux rpcbind (111) and NFS if enabled; Syncthing (21027, 22000), Jellyfin discovery (7359), WireGuard and QUIC where they run. A unicast packet to such a port arrives; whether multicast crosses the mesh was not checked (`drop_local_broadcast` is about broadcasts, `tun.drop_multicast` is off). Not checked on the machines.
- **Per network or per account.** Roadmap #8 wants per account. Decide before P1. Of the products read ([raw/firewall-rules/sharing-and-defaults.md](../raw/firewall-rules/sharing-and-defaults.md)), only Defined reuses rules across networks: roles, tags and rules live on the account, each network keeps its own CA. None documents inheritance or override at the rule level; Tailscale and NetBird have a device switch that blocks incoming, which does override. With allow-only rules a host gets the union. hopssh has no account object: a network belongs to a user (`networks.user_id`), so "the account's rules" are one user's. To settle with it: who may attach, detach and edit a shared set (an edit changes every network it is attached to at once); rules name nodes by their ID within one network, never by name or address (a hostname is not unique, and addresses come back after 48 h when a network is full); a reference to a missing node is refused when the set is attached; detaching is a removal and cuts open flows like one; the device switch of P3 is a deny that wins over the union.
- **Should UDP be allowed now, in code, before any of this?** One line plus healing in `ensureP2PConfig`. The tab would then show the same default.
