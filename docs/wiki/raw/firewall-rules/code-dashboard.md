**1. Dashboard** (`frontend/src/routes/(app)/networks/[id]/+page.svelte`, one 1630-line file)
- Tabs: Join, Nodes, Topology (hidden below `sm`), DNS, Members, Activity. Triggers at :737-745, active-tab union at :71. A Firewall tab means a new union member, a Trigger and a `{:else if activeTab}` block.
- Header (:718-733) shows name, subnet and `.dnsDomain`, with admin-only "Delete Network" (:725-731).
- No UI edits network name or DNS domain. `dnsDomain` is set only at create (`internal/api/networks.go:60-69`), and there is no network PATCH route (`router.go:190-191`).
- Existing write patterns: dialogs (Add DNS ~:1540, Add Node, Create Invite), typed-name confirm for delete (:477, :1594-1598), inline admin pills for capabilities (:952-970; toggle :687-697).
- Gating: `isAdmin = network.role === 'admin'` (:87).

**2. Per-node attributes** (no tags or roles anywhere)
- `capabilities` JSON, default `["terminal","health","forward"]` (`internal/db/migrations/001_initial.sql:57`, `internal/db/nodes.go:43`). The API accepts `relay` too (`internal/api/proxy.go:1120-1145`), but the dashboard toggles only the three defaults (:957). `relay` sets `amRelay` in renew/heartbeat (`internal/api/renew.go:413-420, 482-484`).
- `node_type` node|lighthouse (`nodes.go:40`), `client_type` desktop|cli (migration 006; set by heartbeat, `renew.go:213-217, 282-283`), `dns_name` (`nodes.go:566` Rename), `exposed_ports`, `peer_state` (003), `agent_version` (005).
- Subnet routes (migration 007; `nodes.go:506-560`) have an API (`router.go:203`, `frontend/src/lib/api/client.ts:99`) but no UI caller.

**3. DB schema**
- Account-scoped: `users` (001:1), `sessions` (:10), and `api_keys` (:17), which no Go code reads.
- Network-scoped: `networks` (:26; owner is `user_id`, `dns_domain` :37), `nodes` (:41), `dns_records` (:86), `network_members` (:105, role admin|member, unique per network), `network_invites` (:115), `device_codes`, `network_events` (004). `audit_log` (:94) is per user.
- There are no teams and no account-level object to reuse. Roadmap #8 wants rules keyed to an account with per-network opt-in (`docs/roadmap.md:102`), so that needs a new table.
- Owner is an implicit admin (`internal/authz/authz.go:17-28`).

**4. API, authz, events, agent push**
- Routes: `/api/networks/{networkID}/...` sits behind auth (`router.go:177-250`). Write routes get only a 30 s write timeout (`router.go:117`), with no per-route rate limit. Agent endpoints use `agentRL` (`router.go:157-158`).
- Checks: `authz.CheckAccess` returns the role; `CanView` (`authz.go:31`), `CanAdmin` (:34), owner-only `CanAccessNetwork` (:38-43). Handler helpers are `requireNode` (`proxy.go:69-100`, view) and `requireAdmin` (:103-126, admin).
- Owner-only exceptions: DNS create/delete (`dns.go:106, 170`), DeleteNetwork (`networks.go:385`), bundles (`bundles.go:46`).
- Audit: `h.audit` (`proxy.go:31-36`) covers shell, exec, port-forward, node.delete (:1283) and prune (:1338). Capability and route writes are not audited; they write only `network_events` and EventHub (`proxy.go:1155-1162, 1242-1250`).
- Dashboard events: `EventHub.Publish` (`internal/api/events.go:63`) feeds the WebSocket at `events.go:109-124`. History is `network_events.go:60-61` (`db/network_events.go:83`).
- Caches: `proxyAuthTTL` is 2 minutes (`proxy.go:42`) and is invalidated on capability writes.
- Agent push: nothing besides the heartbeat response. It carries `networkId`, `peers`, `relays`, `peerEndpoints`, `peerInfo`, `amRelay` and `routes` (`renew.go:464-497`). Renew returns the cert and `nebulaConfig` (`renew.go:182-190`). Clipboard is agent-pulled (`clipboard.go:262`).
- Cert groups: node certs are always `["node"]` (`renew.go:137-138`, `enroll.go:312`, `device.go:191`). The server cert is `["admin","node"]` (`networks.go:101`).
- Agent inbound Nebula rules are static in `nebula.yaml` (`internal/client/enroll.go:655-674`): TCP 41820 from `admin`, any TCP from `node`, ICMP.
- The control plane reaches agents over the mesh for shell, exec and proxy (`proxy.go:239`, `:1367`), so a rule must keep that path open.

**5. Tests**
- `TestE2E_Access` is `internal/api/e2e_test.go:92`. Its table (:131-190) lists role outcomes for owner, admin, member, outsider and anon. Rows with a `gap` field are pinned known bugs, e.g. outsiders get 403 instead of 404 on admin routes (:144-148, :177, :180-182).
- The coverage loop (:218-224) builds routes from `routerRoutes` (:226). A new `/api` route must appear in exactly one of `publicRoutes` (:63) or the table, or the test fails.
- DNS add is owner-only (:168-175), yet the dashboard shows it to admins (`page.svelte:1204`).
- Frontend has no test runner, only `svelte-check` (`frontend/package.json:11`). Source-scan tripwires live in `internal/api/d3_source_scan_test.go` and `f2_source_scan_test.go`.

**6. Desktop client** (`clients/desktop/src`, `src-tauri/src`)
- Settings sections: Preferences (`Settings.svelte:438`), Networks (:599), Danger zone (:708), Updates (:877).
- The only per-enrollment toggle is clipboard sync: `Settings.svelte:627-640` → `local-api.ts:276-281` → `POST /local/clipboard-sync` (`internal/client/local_api.go:131`, handler :1410-1440) → `Enrollment.ClipboardSync` (`enrollments.go:49, 421`). This is the precedent for a local device switch.
- Per-peer rows in `Connected.svelte` (lighthouse check :353, Terminal button :373, `openTerminalForPeer` :21-48).
- Local API routes (`local_api.go:121-132`): GET health, status, peers, events; POST enroll (device-flow start/poll, token), connect, disconnect, leave, clipboard-sync, renew. Bearer check at :229. Peer fields at :551-575.
- No block-incoming or firewall UI or route. A local block would mean rewriting the `nebula.yaml` firewall (`enroll.go:591`, :655) and reloading (`internal/client/renew.go:1230`). `applyNebulaConfigUpdate` (`renew.go:774`) applies only server fields.

**7. `hop client join`** (`internal/client/cli_join.go`)
- Same `nodes` row. `JoinNetwork` creates NodeType "node", status "enrolled", cert group "node" (`internal/api/enroll.go:249-312`). `Authorize` does the same (`device.go:182-191`). No separate type, and the join path never sets `client_type`.
- Local side: writes `/etc/hop-client` (:103-109), uses synthetic instance "hop-client" (:121-130) and starts only `runCertRenewal` (:127-128). I found no heartbeat loop in this file, so the node reports no peers, clientType or version. Renewal still records last-seen (`renew.go:147`).
- Client inbound firewall is ICMP only (`cli_join.go:206-215`), versus the agent's rules at `enroll.go:661-674`.
- Dashboard Join tab shows only `hop-agent enroll` and the install script (:1280-1292), not `hop client join`.

**Where rules would plug in:** an admin-only route under `/api/networks/{id}/...` gated by `CanAdmin`, with a `router.go` entry and a `TestE2E_Access` row. Delivery is either a new key beside `routes` in the heartbeat response (`renew.go:491-497`) or cert groups, which only change at renewal. Tags need an account-scoped table that does not exist yet.