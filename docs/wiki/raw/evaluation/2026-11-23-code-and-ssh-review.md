# Code, SSH path and security review (2026-11-23)

Read-only review by a subagent; key claims re-checked by hand are noted on the synthesis pages. **Wrong in it:** the first line of E (the server does close idle lighthouses, `NetworkManager.StartIdleReaper`). **Outdated since:** C5's self-update part, and D's "No E2E". Repo at HEAD bac0c68 plus the uncommitted fixes.

hopssh critical review (read-only; no builds or tests run). Paths are relative to the repository root. "VERIFIED" means I read the code path. "DOC" means the doc claim is not in code or is contradicted.

A. SSH TODAY
- No SSH protocol code. golang.org/x/crypto is used only for curve25519, ed25519 and bcrypt (internal/pki/pki.go, internal/api/auth.go). No ssh/scp/sftp subcommand in cmd/agent/main.go:42-89. No embedded server. VERIFIED.
- Only shell path: browser PTY. Route router.go:211 -> ProxyHandler.NodeShell (internal/api/proxy.go:238) -> WebSocket to agent /shell over Nebula with the node's bearer token (proxy.go:276-277) -> PTY in cmd/agent/shell_unix.go (ConPTY in shell_windows.go).
- Desktop terminal: Connected.svelte "Terminal" opens a Tauri webview on the dashboard /terminal page (clients/desktop/src-tauri/src/lib.rs:493-540), then the same WebSocket.
- Brokered by the control plane. Terminal bytes are plaintext in control-plane memory (proxy.go:260-321). Only the Nebula leg can be P2P. features.md:36 "End-to-end encrypted" is wrong.
- OS user: root. No User= in the systemd unit (internal/client/service.go:25-32), no UserName in the launchd plist, LocalSystem on Windows (service_windows.go:170). Not configurable.
- Authorization: any member can use it. CheckAccess gives members Role "member" and CanView is Role != "" (internal/authz/authz.go:31). requireNode (proxy.go:84) and NodeShell/NodeExec (proxy.go:239-244, 341-346) only check CanView plus the node's `terminal` capability. Default capabilities are ["terminal","health","forward"] (internal/db/migrations/001_initial.sql:57). No per-user ACL, no SSH keys.
- Drop kills the session. Browser read error closes the agent conn (proxy.go:310-321); agent defer kills the PTY (shell_unix.go:113-116). The frontend "reconnect" opens a new WebSocket, which is a new shell (frontend/src/lib/terminal/shell.ts:167-183). No tmux-like resume.
- Audit: "shell.connect" (proxy.go:257) and "exec" with nil details (proxy.go:353). No command text, no session end, no recording.
- OS `ssh user@mesh-ip` works only with kernel TUN and a sshd on the node. DOC: architecture.md:123-125, features.md:131.

B. P2P RELIABILITY
- Transport is Nebula UDP only. No TCP/443 or WebSocket fallback (roadmap.md:125 N1 "Planned"). internal/quictransport is a migration probe, not a data path (probe.go header; HOPSSH_QUIC_PORT is off by default, cmd/server/main.go:79).
- Single lighthouse per network: one control-plane host (internal/client/enroll.go:618-620 hosts list; internal/mesh/network_manager.go:527). Peer relays (internal/api/renew.go:442-451) add capacity only.
- Control plane down over 24h: certs are 24h (renew.go:19). Renewal authenticates with the bearer token, not the cert (client/renew.go:611), with backoff capped at 30 min (renew.go:512-528). Nebula verifies cert expiry at handshake (vendor cert/ca_pool.go:193-197). pki.disconnect_invalid is never set (vendor connection_manager.go:482), so live tunnels probably survive expiry while new handshakes fail. Not runtime-verified. The failure already happened (docs/wiki/incidents/2026-11-18-mbp-all-certs-expired.md:17).
- IPv4 underlay only: listen host 0.0.0.0 (network_manager.go:521, enroll.go). No IPv6 overlay: IssueCert takes one prefix (internal/pki/pki.go:53).
- NAT-PMP, PCP and UPnP are wired (internal/portmap/*, client/instance.go:441, patch 11). VERIFIED.
- Telemetry: per-node peer counts (api/types.go:380), EWMA RTT over TCP probes (client/path_quality.go:33,43). No /metrics endpoint and no success-rate data.
- internal/burstsock has no importers, so double-symmetric NAT (N4/N8) is unwired. open-questions.md:72-77 records an open relay stall for same-CGNAT peers.

C. SECURITY (worst first)
1. Compromised control plane means root on every node. It decrypts and holds every agent token (internal/db/nodes.go:267-272) and the CA key (db/networks.go:41). The agent accepts any holder of the bearer (cmd/agent/main.go:250-260) and never checks capabilities, so the per-node terminal toggle is advisory against a compromised control plane.
2. Any member gets root shell and arbitrary exec by default. handleExec runs any command and env (cmd/agent/main.go:296-323). Exec is unlogged (proxy.go:353). Any member can read the network audit log (internal/api/audit.go:124-126).
3. Flat firewall. Nodes accept any TCP from any `node`-group peer (enroll.go:661-665). The admin rule for 41820 (enroll.go:656-660) is redundant. Client-joined laptops get group node (api/enroll.go:311-312), despite the "user cert" comment at router.go:226. Lighthouse inbound is any/any (network_manager.go:537-545).
4. Encryption key sits next to the data. Default is <data>/encryption.key, same directory as SQLite (cmd/server/main.go:105-119). A DB copy yields CA and node keys.
5. Update path is fail-open. Self-update fetches the checksum from the same origin, then warns and installs if the fetch fails or the entry is missing (internal/selfupdate/selfupdate.go:199-205, 224-225). There is no signature (.github/workflows/release.yml:172). install.sh is the same (internal/api/distribution.go:502-503). The macOS installer has no checksum (distribution.go:327-333). Tauri updater pubkey is "PLACEHOLDER" (tauri.conf.json:42).
6. Mesh DNS binds 0.0.0.0 UDP with no source filter (internal/mesh/dns.go:88, 120-150), so anyone can resolve mesh hostnames to mesh IPs.
7. Predictable temp path. The root agent writes to os.TempDir()/hop-shell (shell_unix.go:43-44, 76-77). On multi-user Linux, a pre-created attacker dir could inject .zprofile, which `zsh -l` sources. Not runtime-verified.

D. TEST AND CI
- ci.yml:33-40 runs build, `make vet` and `make test` on ubuntu only. Makefile:93-94 is `go test -mod=vendor ./...`. No -race, no govulncheck, no golangci-lint.
- Frontend `check` script (frontend/package.json:11) is not in CI. Zero frontend test files. Desktop has 44 #[test] in lib.rs, never run (release-desktop.yml has no cargo test).
- ci.yml's check-patches step runs `make check-patches`, which does not exist, with continue-on-error.
- No test files: internal/auth (RequireAuth, rate limiter), internal/pki, internal/crypto, internal/selfupdate, internal/quictransport, cmd/server. Nothing references NodeShell, NodeExec or HasCapability.
- No E2E. Nothing starts a control plane plus Nebula nodes. routes_e2e_test.go is httptest only.
- Source-scan: 55 of 541 Test funcs read source text, plus 10 tripwire-named tests. 486 are behavioral.
- Over 1000 lines: internal/client/renew.go (1586), local_api.go (1459), api/proxy.go (1391), recovery_test.go (1032); clients/desktop/src-tauri/src/lib.rs (3226); networks/[id]/+page.svelte (1632); Settings.svelte (1033).

E. DRIFT (DOC-ONLY or contradicted)
- features.md:15 "idle network reaper" for Nebula instances: only a port-forward reaper exists (mesh/forward.go:198-204).
- features.md:221 "pre-warming blocks until Noise handshakes complete": warmPeers is fire-and-forget TCP probes (renew.go:267, 457-465).
- features.md:237 patch check in CI: target missing.
- architecture.md:647 says patch 12 applies. open-questions.md:35-39 says it silently fails, since Makefile:33-41 ignores patch errors.
- architecture.md:576 "No inbound ports": Nebula listens on UDP 0.0.0.0 and portmap opens public UDP.
- architecture.md:652 cites scripts/check-nebula-patch.sh, which does not exist. architecture.md:260 says 22 Tauri commands; lib.rs has 28.
- Shipped but roadmap says absent: subnet routing (API and agent present at proxy.go:1173-1200 and client/routes.go, no dashboard UI; roadmap.md:70, 92). GitHub OAuth (api/auth_oauth.go, router.go:127-129; roadmap.md:90 says not started).
- Absent: SSH keys or certs, RBAC beyond admin/member, session recording, ACL or tags, exit nodes (proxy.go:1170 rejects 0.0.0.0/0), scoped API keys (api_keys table in 001_initial.sql:17, no code), SSO/OIDC, webhooks, Terraform, IPv6, CA rotation, mobile apps, Windows and Linux desktop builds (release-desktop.yml is macOS only). Windows agent service exists.

RANKED GAPS (against "reliable p2p SSH that beats Tailscale/ZeroTier/NetBird")
1. No SSH surface: no server or client, no keys or certs, no `hop ssh`, no scp or sftp. Native ssh only with kernel TUN and sshd. L
2. Authorization: members get root shell by default, with no ACL, no RBAC, no command audit, no recording. M
3. Control plane as single trust and failure domain (tokens, CA, lighthouse, relay, DNS, one process). L
4. No TCP/443 fallback, so UDP-blocked networks fail. Double-symmetric NAT still relays and N4 is unwired. L
5. Shell sessions not resumable; any WebSocket drop kills the shell. M
6. Flat firewall: any TCP from the node group, and laptops are in that group. M
7. Unsigned, fail-open update and install path; Tauri updater not configured. S
8. Test and CI: no multi-node E2E, auth/pki/crypto untested, no race or vuln scanning, silent patch failures. M
