# Recommendation
Yes. Configure rules in the dashboard as a per-network Firewall tab: admins edit, members read. Store the rules as one versioned policy per network. The heartbeat carries the policy to each agent, which renders its own nebula firewall block, validates it, and applies it with an in-place config reload, so tunnels stay up and no re-enrollment is needed. Every existing network keeps today's rules until someone edits them, and the migration writes nothing. The model is allow-only: a rule targets all agents or one node, and its source is any node, the control plane, or one node. Cert groups stay as the control-plane identity, because a group change needs a cert reissue. Tags (roadmap #8) come later on the same policy object. Whatever the server sends, the agent always keeps TCP 41820 and ICMP open from the admin group. A per-device "block incoming from other devices" switch in the desktop app gives the owner a local override the dashboard cannot remove. Main risks: the in-place reload path has never run in hopssh; the dashboard's /proxy path is not covered by the firewall; owner and member devices cannot be told apart until tags exist.

# Design
## 1. Model
- Unit: one versioned policy per network (a JSON row). Not cert groups, and not raw nebula YAML from the server.
- Rule: `{target, proto, port, source}`, allow only (Nebula has no deny; deny is the absence of a rule). `target`: `all-agents` or `node:<id>`. `proto`: `tcp|udp|icmp|any`. `port`: `any`, `N`, or `N-M` (ignored for icmp). `source`: `any-node`, `control-plane`, or `node:<id>`.
- Rendering on the agent: `any-node` becomes `groups: [node]`; `control-plane` becomes `groups: [admin]`; `node:<id>` becomes `cidr: <nodes.nebula_ip>/32`. The overlay IP is bound by the peer's cert, so a per-node source is as strong as the cert.
- Why not groups: agent certs carry only `node` (`internal/api/renew.go:137-138`). A group change waits for the next renewal, about 10 to 14 h away. Groups remain the control-plane identity (`internal/api/networks.go:101`).
- Default: no row means exactly today's rules (`internal/client/enroll.go:655-674`). Migration writes nothing. The agent reloads only when the rendered block differs from the file, so a default-equal policy causes no reload.
- Storage: `network_firewall(network_id PK, version, rules_json, updated_by, updated_at)` and `network_firewall_history(network_id, version, rules_json, actor, at)` for revert. Migration runs in a transaction. Cap: 100 rules.

## 2. Where and who
- Dashboard: a "Firewall" tab beside DNS (tab union `frontend/src/routes/(app)/networks/[id]/+page.svelte:71`, triggers `:737-745`).
- Members read (`authz.CanView`, `internal/authz/authz.go:31`). Admins, including the owner, edit (`CanAdmin`, `authz.go:34`). The UI gate and the API gate must match; the DNS add mismatch is the warning.
- API: `GET /api/networks/{id}/firewall`; `PUT` with `If-Match: <version>` (stale version gives 409); body capped at 1 MB. Every PUT is audited through `h.audit` (action `firewall.update`, old and new version, rule diff). Capability and route writes are not audited today; fix that in the same change.
- Save shows a preview per node: rules added, rules removed, and "N nodes lose access to port X". Removals need a confirm.
- Later: the same JSON exported as YAML for policy-as-code.

## 3. Delivery and apply
- The heartbeat response adds `firewall: {version, rules}` only when a row exists, the same pattern as `routes` (`internal/api/renew.go:491-496`). Unknown fields are ignored by agents (no `DisallowUnknownFields` in `internal/api`). Latency: one heartbeat, about 60 s.
- Agent flow: validate (key whitelist, port range, rule cap) → render → compare with the firewall block in `nebula.yaml` → write the file → `cfg.ReloadConfigString(text)`.
- The agent must keep its `*config.C`. Today it is a local `var cfg config.C` (`internal/client/nebula.go:67,129`). Apply under the instance lock that renewal uses, and check `closed` first (the changelog's startMu rule).
- In vendored Nebula, `reloadFirewall` (`vendor/github.com/slackhq/nebula/interface.go:441,462`) rebuilds the firewall only when the `firewall` key changed and keeps conntrack. From the source, an established flow is re-matched on its next packet, so a removed rule cuts that flow and nothing else. Not tested in hopssh.
- On a reload error, fall back to `reloadNebula` (close and start). It drops tunnels, so it is only the error path.
- Invalid policy: keep the last applied rules, log, and send `firewallError` in the next heartbeat. The dashboard shows per node: applied, pending, error, or "agent too old" (no `firewallVersion` ack).
- Offline start: the agent applies the YAML already on disk, which is the cache.

## 4. Invariants (enforced by the agent whatever the server sends)
1. TCP 41820 from `admin` is always allowed. Shell, exec, proxy and path probes depend on it.
2. ICMP from `admin` is always allowed.
3. No outbound rules are offered; outbound stays `any`.
4. The server cannot express a policy that breaks 1 or 2. The agent re-checks.
5. The local shield can only restrict, never widen.
The control plane can already run commands on every agent through 41820, so the firewall adds no trust in the server. It limits peers, which is the real threat in shared networks.

## 5. Device side
- Per-enrollment switch "Block incoming from other devices" in desktop Settings. Precedent: clipboard sync (`clients/desktop/src/Settings.svelte:627-640` → `local-api.ts:276-281` → `POST /local/clipboard-sync`, `internal/client/local_api.go:131` → `Enrollment.ClipboardSync`, `enrollments.go:49`). The shield blocks node-group traffic and keeps invariants 1 and 2, so the owner can still manage the device remotely. Its state goes in the heartbeat and shows as a badge. Same switch in the CLI.
- Read-only "Who can reach this device" in node detail: the effective inbound list, each row naming its rule. Uses the same renderer.

## 6. Limits the UI must state
- Kernel TUN (root or system service) gates host services such as sshd and Screen Sharing. Userspace gates only netstack listeners (41820 and forwards), because the OS has no route into the mesh (`internal/client/nebula.go:101`, from the brief). Show the mode per node.
- The dashboard's `/proxy/{port}` goes through 41820 and is governed by capabilities, not the firewall. Say so beside the rules.
- Owner and member devices both hold cert group `node`, so groups cannot separate them. Per-node sources are the only tool until tags and users exist.
- `hop client join` has no heartbeat loop (none in `internal/client/cli_join.go`), so clients get no policy and keep ICMP-only inbound (`cli_join.go:206-215`). Lighthouses are out of scope (`internal/mesh/network_manager.go:539-547`).

## 7. Phases, smallest first
- P0, no behavior change: one pure renderer in a shared package, used by `enroll.go` and `cli_join.go`. A golden test pins today's text.
- P1, read only: table, GET, PUT, authz, audit, read-only tab, "who can reach" view. Agents unchanged.
- P2, agent apply: heartbeat field, validation, reload, ack, error.
- P3, editor: rule table, node targets, preview, confirm, revert from history.
- P4, shield: desktop, CLI, heartbeat report, badge.
- Later: tags and roles (roadmap #8) in an account-scoped table, with cert reissue on check-in; outbound rules; policy-as-code; clients on heartbeat.

## 8. Tests that gate each phase (red first)
- `TestE2E_Access`: rows for GET and PUT for owner, admin, member, outsider and anon. No new `gap` rows.
- Golden: the default policy renders today's block; the first heartbeat triggers no reload.
- Invariant: a deny-all policy still renders invariants 1 and 2; no PUT removes them.
- Reload E2E with two real Nebula nodes (`internal/e2etest`): A to B TCP 9000 allowed; rule removed; new connections fail within one heartbeat; the ICMP tunnel stays up with no new handshake; the old flow is cut on its next packet.
- Bad policy keeps the last applied rules and reports the error. Offline start applies the cached YAML.
- `make test-race`; the kernel-TUN test as root on Linux; `scripts/netlab.sh` before P2 ships, because reload touches the hostmap path (netlab rule 1).
- The frontend has no test runner: a source-scan tripwire that write controls match the API gate.

# Code claims
[
 {
  "claim": "Agent firewall block: outbound any; inbound TCP 41820 from groups [admin]; TCP any from groups [node]; ICMP any from host any. Written once at enrollment, not reloaded.",
  "where": "internal/client/enroll.go:655-674"
 },
 {
  "claim": "Agent node certs are issued with group [node] and name node-<first 8 chars of node ID>.",
  "where": "internal/api/renew.go:137-138; internal/api/enroll.go:179-180"
 },
 {
  "claim": "Server (control plane) cert is issued with groups [admin, node].",
  "where": "internal/api/networks.go:101"
 },
 {
  "claim": "Heartbeat response adds optional fields only when present (routes pattern), the model for adding a firewall field.",
  "where": "internal/api/renew.go:491-496"
 },
 {
  "claim": "Heartbeat body is capped by http.MaxBytesReader at 1 MiB; no DisallowUnknownFields is used in internal/api, so older agents ignore new response fields.",
  "where": "internal/api/renew.go:198 (MaxBytesReader); grep of internal/api for DisallowUnknownFields returned no matches"
 },
 {
  "claim": "Nebula config is held only in a local variable at each start; no meshInstance keeps *config.C, so a later reload is not possible today.",
  "where": "internal/client/nebula.go:67 and :129 (var cfg config.C), passed to nebula.Main at :77 and :139"
 },
 {
  "claim": "ReloadConfigString re-parses text, holds the reload lock and runs all registered callbacks.",
  "where": "vendor/github.com/slackhq/nebula/config/config.go:175-190"
 },
 {
  "claim": "The firewall registers reloadFirewall as a reload callback, and reloadFirewall rebuilds the firewall using the certificate's unsafe networks check.",
  "where": "vendor/github.com/slackhq/nebula/interface.go:441 and :462-470"
 },
 {
  "claim": "Nodes table stores the overlay address as nebula_ip, which can back a per-node cidr /32 source rule.",
  "where": "internal/db/migrations/001_initial.sql:41-61"
 },
 {
  "claim": "Authorization helpers: CanView is any role; CanAdmin is role admin. Owner is handled elsewhere in the access check, not in CanAdmin.",
  "where": "internal/authz/authz.go:31 and :34"
 },
 {
  "claim": "Roadmap item 8 (granular firewall rules with tags, compiled to cert groups plus firewall config) is the existing plan this design extends.",
  "where": "docs/roadmap.md:102"
 },
 {
  "claim": "hop client join has no heartbeat code, so client nodes receive no server-pushed policy.",
  "where": "grep for heartbeat in internal/client/cli_join.go returned no matches"
 },
 {
  "claim": "Settings-to-agent precedent for a per-enrollment local switch is clipboard sync, from the desktop UI through the local API to an Enrollment field (from the brief, not re-read this session).",
  "where": "clients/desktop/src/Settings.svelte:627-640; internal/client/local_api.go:131; internal/client/enrollments.go:49"
 }
]

# Open questions
- Does ReloadConfigString keep an ICMP-carrying tunnel up and cut a denied TCP flow on its next packet? This was read from source only. The SIGHUP path was never tested in hopssh, so it must be proven with the reload E2E test first.
- Once the agent keeps *config.C past start, do the other reload callbacks (punchy, hostmap, lighthouse, pki at vendor pki.go:59, connection manager) react to a firewall-only reload with unchanged keys? Check the logs during the test.
- Is the agent's tun mode (kernel or userspace) sent to the server today? If not, the dashboard cannot say which mode a node runs, and the userspace caveat cannot be shown per node.
- Should the desktop shield also block control-plane access on 41820? Recommendation: no. Blocking it leaves only a local undo and makes remote recovery impossible.
- Default for new networks: keep today's allow-all (matches Tailscale, NetBird and Headscale defaults) or ship a default-deny preset? Either way, a default-deny change must never be applied silently to existing networks.
- Does hop client join get a heartbeat loop? If not, client devices stay outside the policy entirely.
- Source identity: cidr nebula_ip/32 or host node-<8 chars>? The IP must be unique and stable across delete and re-enroll, and the 8-character prefix must be collision-free. Prefer the cidr form unless this is shown.
- Does vendored Nebula v1.11.2 reload pki.cert and pki.key in place? If it does, tag-driven certificate changes could skip close-and-start later. Not checked.
- Inbound UDP: the brief says none is allowed today. Should UDP be an opt-in rule (recommended) or part of the default?
- Limits and warnings: the proposed rule cap is 100. Should the editor warn on port any and on portless rules, as NetBird does?
- Where does the dashboard /proxy/{port} path appear in the Who can reach this device view? It is governed by capabilities, not the firewall.
- Should the audit gap for capability and route writes be closed in the same change as the firewall audit?