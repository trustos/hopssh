**Firewall facts for the dashboard-rules design (read-only, file:line)**

**1. Writers of the `firewall:` section**
- Agent nodes: `internal/client/enroll.go:655-674`, inside `writeNebulaConfig` (`enroll.go:591`). Called from `enroll.go:480` (bundle), `enroll.go:557` (token/device enroll), `internal/client/local_api.go:1191` (local-API enroll).
  - outbound: `port: any, proto: any, host: any`
  - inbound: TCP `port: 41820` `groups: [admin]`; TCP `port: any` `groups: [node]`; `proto: icmp, host: any`
  - No inbound UDP rule.
- Client mode (`hop client join`): `internal/client/cli_join.go:206-214` (written by `writeClientNebulaConfig`, `cli_join.go:155`). Inbound is ICMP only.
- Lighthouse (control plane, `internal/mesh/network_manager.go:startInstance`): `network_manager.go:539-547`. Inbound and outbound are both `port: any, proto: any, host: any`.
- Test only: `internal/e2etest/peer.go:42-44`.
- Nebula semantics: rules are allow-only (`vendor/github.com/slackhq/nebula/firewall.go:27`, `AddRule` has no action). Unmatched traffic gets `ErrNoMatchingRule` (`firewall.go:421,472`). Groups in one rule are AND, separate rules are OR (`firewall.go:856-870`). Groups are matched against the peer's certificate (`firewall.go:425-470`).
- The `admin` rule on 41820 is redundant: every peer cert carries `node`, and the `node` TCP rule covers all ports.

**2. Does anything heal or update the firewall of an existing nebula.yaml?**
- No. The firewall block is written only at enrollment or join.
- `ensureP2PConfig` (`internal/client/renew.go:911`, called from `client.go:272` on every connect) never references `firewall`. It owns pki, listen.port, cipher, timers, punchy, handshakes, tun, routines, preferred_ranges, lighthouse.local_allow_list and relay. It round-trips the whole map (`renew.go:1131`), so the firewall block survives unchanged.
- Other writers touch only their own keys: `mergeNebulaConfig` (`renew.go:821`: useRelays, punchBack, punchDelay, listenPort) and `rewriteNebulaUnsafeRoutes` (`internal/client/routes.go:132`: tun.unsafe_routes).
- A rule change in code reaches an existing node only by re-enrolling, or if someone adds firewall ownership to `ensureP2PConfig`.

**3. Certificate groups**
- Agents always get `["node"]`: `internal/api/enroll.go:180` and `:312` (JoinNetwork), `internal/api/device.go:191`, `internal/api/bundles.go:80`, `internal/api/renew.go:138` (comment: "all nodes use node group").
- The control plane's cert `hopssh-server` gets `["admin","node"]`: `internal/api/networks.go:101`. Its address is `.1` of the subnet (`networks.go:92` comment; `internal/pki/subnet.go:33`).
- `admin` is therefore the control plane's identity in each network. The lighthouse runs on that cert (`network_manager.go:551`). No agent cert ever has `admin`.
- The invite role `admin` (`networks.go:139`, `invites.go:54`) is a membership role, not a cert group.
- `pki.IssueCert` (`internal/pki/pki.go:53`) takes groups as a parameter and has no policy.
- Groups are not stored. There is no group column in `internal/db/migrations` and no groups field on the node struct. Changing a node's groups needs a code change at issuance.

**4. Server-to-agent config path**
- Only pull channels exist. Heartbeat: `internal/api/renew.go:197` (Heartbeat), response built to `:499`. Renew: `internal/api/renew.go:85`, response at `:182-192` (nodeCert, nodeKey, expiresIn, nebulaConfig{useRelays, punchBack, punchDelay}).
- The agent parses the heartbeat at `internal/client/renew.go:258-275` and the renew response at `:735-741`.
- No server-to-agent socket exists. The only websocket dial in the agent is `cmd/agent/proxy.go:113` (a local target). The SSE at `internal/client/local_api.go:613` goes to the desktop app.
- `applyNebulaConfigUpdate` (`renew.go:774`) writes yaml only and does not reload.
- Changes are applied by close-and-start: `reloadNebula` (`renew.go:1230`), called from `renewCert` (`renew.go:680`) and `applyRoutesUpdate` (`routes.go:63`).
- The routes path is the existing template for "heartbeat field, then yaml, then restart": `applyRoutesUpdate` (`routes.go:41`), called from `renew.go:288`.
- Heartbeat cadence is 60 s with jitter (`renew.go:71,81,114`). The comment at `renew.go:58-60` says "every 5 minutes", which is drift. `HeartbeatInterval` (`internal/client/api.go:35,102-103`) is never read.

**5. Vendored Nebula**
- In-place reload exists. `config.ReloadConfig` (`vendor/github.com/slackhq/nebula/config/config.go:152`) re-reads the file and runs callbacks. `ReloadConfigString` (`config.go:175`) takes text. SIGHUP reaches it through `CatchHUP` (`config.go:129`, started by `vendor/.../main.go:121`).
- The firewall callback is `reloadFirewall` (`vendor/.../interface.go:441`, `:462`). It rebuilds only if `c.HasChanged("firewall")`. The new firewall gets rulesVersion+1 and reuses the old conntrack table (`interface.go:499`). The old firewall is destroyed (`:504`). The function closes no tunnels.
- Conntrack: each entry stores rulesVersion (`firewall.go:31-37`). On the next packet of an established flow, `inConns` (`firewall.go:505`) re-matches it against the new table and keeps or deletes it. A version overflow to 0 resets conntrack.
- hopssh keeps no `*config.C`. `startNebula` (`internal/client/nebula.go:66-90`) and `startNebulaKernelTun` (`nebula.go:128-150`) load into a local `var cfg config.C`. `control.go` exposes no config accessor, and nothing calls `ReloadConfig`.
- hopssh handles only SIGINT and SIGTERM (`cmd/agent/main.go:208`). A SIGHUP would reach Nebula's `CatchHUP` in every instance. This is from reading the code only and was not tested.
- So today a rule change means close-and-start, which drops all tunnels and resets conntrack.

**6. Immediate renewal**
- The server cannot trigger one. Neither the heartbeat nor the renew response carries a "renew now" flag.
- Local only: `POST /local/renew` (`internal/client/local_api.go:1347`, 60 s per-enrollment limit at `:1368`) and the Go API `ForceRenew` (`internal/client/api.go:332`). Both call `renewCert`, which restarts Nebula.
- Normal renewal: cert lifetime is 24 h (`internal/api/renew.go:20`, `enroll.go:21`, `device.go:19`, `bundles.go:19`). `renewalTime` (`internal/client/renew.go:631-636`) is NotBefore + half the life ±10%, so about 9.6 to 14.4 h after issue. The poll is every 60 s (`renew.go:492`). Failed renewals retry 12 times, backing off from 1 min to 30 min (`renew.go:537-560`).

**7. Userspace versus kernel TUN**
- Userspace: `service.New` over the gvisor netstack (`nebula.go:66-90`). The mesh listener is on `:41820` via `svc.Listen` (`serverset.go:52`, `client.go:335`). The code states "the OS has no route to mesh addresses" (`nebula.go:101`). Host sockets such as sshd are not reachable from the mesh; only netstack listeners are.
- Kernel TUN: the listener is bound to `meshIP:41820` through an OS `net.Listen` (`nebula.go:166-172`).
- The firewall applies in both modes. Nebula core calls `Drop` on decrypted overlay packets (`vendor/.../outside.go:521` inbound, `inside.go:75` outbound) before the device.
- Outside the firewall: when Nebula is down, the agent serves on `":41820"` on all interfaces via the OS stack (`client.go:266,285`, `serverset.go:74-75`). This is bearer-protected (`cmd/agent/main.go:250-258`).
- Mesh routes behind the bearer token (`cmd/agent/main.go:131-140`): `/health`, `/exec`, `/upload`, `/shell`, `/proxy/{port}` (a reverse proxy to `127.0.0.1:port`, refusing the agent's own port, `cmd/agent/proxy.go:20-60`), and pprof. The token, not the firewall, is what gates exec.

**8. Docs versus code**
- `docs/wiki/open-questions.md:107-113` (UDP blocked) matches the code. It says "firewall rules are roadmap #13", but `docs/roadmap.md:114` #13 is log streaming. Firewall rules are #8 (`roadmap.md:102`).
- `open-questions.md:113` says `ensureP2PConfig` "can heal the rest". Today it cannot (see 2).
- `roadmap.md:102` says "Current Nebula config is wide-open allow-all". That holds for outbound and the lighthouse only. Agent inbound is TCP from nodes plus ICMP, with no UDP. "Certs hardcoded groups node" holds for agents; the server cert also has `admin`.
- `docs/architecture.md:414-448` (Firewall Groups) matches the code. Capabilities are checked in `internal/api/proxy.go` (`:192,245,347,416,534`), not in the agent, which matches `open-questions.md:202`.
- No ADR under `docs/wiki/decisions` mentions the firewall.
- The project CLAUDE.md trust model says "Nebula firewall groups (node/admin) control mesh-level access". That is loose, since the `admin` rule is redundant.