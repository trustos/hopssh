# Verification of the findings and the proposal (2026-11-27)

Four agents: findings 3-7 checked against the code, proposal claims A-H checked against the code and vendored Nebula, a reload spike with two userspace Nebula peers on localhost (scratch copy, single runs), research on sharing and defaults (`sharing-and-defaults.md`). Paths of the scratch copy are left out.

## Findings 3-7

### Claim 3: four functions write per-network nebula.yaml and share no lock; the routes writer (routes.go ~173) writes without atomicWrite, so a crash can truncate the file

**Verdict:** partly

Seven writer call sites, not four. Runtime writers (the four the claim likely means): rewriteNebulaUnsafeRoutes routes.go:145-173 (ReadFile 147, os.WriteFile 173 truncates before writing, no lock; runs on the heartbeat goroutine via applyRoutesUpdate routes.go:54 <- sendHeartbeat renew.go:288; goroutine started client.go:253); applyNebulaConfigUpdate renew.go:774-812 (atomicWrite 812, under certInstallMu renew.go:747-748 only; writeCertPair takes and releases startMu at renew.go:658 before this runs); ensureP2PConfig renew.go:911-1136 (atomicWrite 1136, under startMu held in startInstance client.go:217, called client.go:271); upgradeTunMode nebula.go:586-621 (atomicWrite 617, under startMu via readTunMode client.go:270). Boot/enrollment writers: healListenPortYAML listenport.go:73 (boot only, migrateListenPorts api.go:144 before connect goroutines); writeNebulaConfig enroll.go:591/689 -> writeFileSecure enroll.go:722-723 (new enrollment dirs); writeClientNebulaConfig cli_join.go:155/227. Lock map: ensureP2PConfig and upgradeTunMode share startMu; applyNebulaConfigUpdate uses certInstallMu; the routes writer takes no lock. Overlaps that can happen: (a) the heartbeat goroutine starts at client.go:253 and sends its first beat at once (renew.go:77), so it can rewrite nebula.yaml while startInstance is in its startMu section (client.go:270-271); (b) the renewal writer (renew.go:812) can interleave with the routes writer's read (routes.go:147) and write (routes.go:173), reverting relay/punchy values set at renew.go:829-846 until the next renewal; (c) reloadNebula's startNebula reads the file (nebula.go:66-67) with no lock the routes writer takes; (d) across instances, atomicWrite uses a fixed nebula.yaml.tmp (renew.go:1216-1222) with no shared lock, needing a renewal in flight during a restart (judged unlikely, not measured). Truncation is real: routes.go:173 truncates before writing. Nothing repairs it: persistRoutes runs first (routes.go:50), so routesEqual (routes.go:47) returns early on later beats and the rewrite is not retried; when the route list next changes, parseNebulaYAML at routes.go:151 fails on the empty file and it stays empty. No code regenerates nebula.yaml; the comment at legacy_migrate.go:27 ('regenerated on demand') has no matching code. Code-read only, not reproduced.

**Correction:** The writer count is seven call sites (four runtime). 'Share no lock' is true only for the routes writer against the others: ensureP2PConfig and upgradeTunMode share startMu, and applyNebulaConfigUpdate uses certInstallMu. Truncation risk and the permanent unrepaired state are valid.

### Claim 4: the DNS tab shows Add/Delete to admin-role members, but the API (dns.go ~106, ~170) allows only the owner

**Verdict:** confirmed

Frontend frontend/src/routes/(app)/networks/[id]/+page.svelte: line 87 isAdmin = $derived(network?.role === 'admin'); the DNS tab is the branch at 1199 ({:else if activeTab === 'dns'}) with no role gate; only the 'Add Record' button is inside {#if isAdmin} (1204-1211); the Delete button (1252-1257) is gated only by {#if record.source === 'custom'}; the invite role copy at 1453 reads 'Admin — full access, can manage nodes, DNS, invites'. API: ListDNSRecords dns.go:59-60 uses CheckAccess + CanView, so members can read; CreateDNSRecord dns.go:106-107 and DeleteDNSRecord dns.go:170-171 return 404 when !authz.CanAccessNetwork(user, network); CanAccessNetwork authz.go:38-43 is network.UserID == user.ID (owner only). The role the page reads comes from CheckAccess via networks.go:257 and networks.go:365.

**Correction:** Delete is shown to every member who can view the DNS tab (any role), not only admins. Add is shown to admin-role members only. Both return 404 to every non-owner. The invite copy at page.svelte:1453 promises DNS management the API does not allow.

### Claim 5: node capability and subnet-route writes (proxy.go ~1120, ~1181) are not audited

**Verdict:** confirmed

internal/api/proxy.go: UpdateCapabilities at 1120 (requireAdmin 1121, h.Events.Record 1162, no h.audit call); UpdateRoutes at 1181 (requireAdmin 1182, h.Events.Record 1249, no h.audit call); RenameNode at 1077 (Events.Record 1112, no audit). DeleteNode (1283) and PruneOfflineNodes (1338) do call h.audit, so the inconsistency is inside proxy.go. Non-test audit calls in internal/api: proxy.go 258, 354, 447, 564, 1283, 1338; auth.go 94-95, 146-147; auth_oauth.go 196-197. None in networks.go, members.go, invites.go, dns.go, enroll.go, device.go, renew.go, bundles.go. Storage: audit_log.user_id NOT NULL (internal/db/migrations/001_initial.sql:94-102); AuditStore.Log internal/db/audit.go:78 drops entries when its buffer is full; network_events has no user column and NetworkEventStore.Record (internal/db/network_events.go:83) takes no actor and drops on overflow.

**Correction:** The three handlers are absent from audit_log, but they are recorded in network_events with no actor. The gap is wider than the claim: rename, network create and delete, member add/remove/role change, invites, DNS create and delete, enrollment and device authorisation, and bundle download are not in audit_log either.

### Claim 6: Nebula's f.firewall = fw plain pointer write (vendor interface.go ~502) races with packet-goroutine reads (outside.go ~521, inside.go ~75, ~228); state whether it is a Go data race, whether a lock or atomic protects it, and whether upstream v1.12.0 still does it

**Verdict:** confirmed

Write: vendor/github.com/slackhq/nebula/interface.go:62 firewall *Firewall is a plain field; reloadFirewall interface.go:462-506 reads oldFw := f.firewall (484), takes conntrack.Lock() (486), and writes f.firewall = fw (502); registered at interface.go:441. It runs from ReloadConfig (config/config.go:152-170), called by the CatchHUP goroutine (config.go:129-146, signal.Notify(SIGHUP) at 135), registered by vendor main.go:121. Unsynchronised reads: outside.go:521 f.firewall.Drop; inside.go:75, 91, 107, 228. Synchronisation: the only lock is conntrack taken by the writer after it has read oldFw; readers load the pointer before Drop's internal conntrack use, so nothing orders the write before the reads. Atomics in interface.go (lines 76-82, 98, 105) cover other fields. Go memory model (go.dev/ref/mem: the data race definition; single-word reads observe a written value): no torn pointer is possible, but the accesses are a data race by the spec and may be reported as one. Upstream v1.12.0 (scratch copy): interface.go:567 oldFw := f.firewall, interface.go:585 f.firewall = fw; readers outside.go:491, inside.go:113, 244, 260, 384; no atomic added. Reachability in hopssh: nebula.Main(..., false, ...) at internal/client/nebula.go:77 and 139, so CatchHUP is registered; no SIGHUP handling or sending in cmd/ or internal/ (non-test); sshd is not configured, so ssh.go:1056 is unreachable; reloadFirewall returns early (interface.go:473-476) unless the firewall section or the certificate's unsafe networks changed; renew.go has no firewall reference, and the firewall section is written only at enrollment (enroll.go:655, cli_join.go:206).

**Correction:** No correction to the claim. Reachability note: the write runs only on a SIGHUP together with a changed firewall section, which hopssh does not write at runtime. Not exercised with the race detector.

### Claim 7: renew.go ~62 says heartbeat is every 5 minutes; find the real interval including jitter

**Verdict:** wrong

renew.go:62 comment 'Normal mode: every 5 minutes.' contradicts renew.go:71 normalInterval = 60 * time.Second. First beat is immediate (renew.go:77); the timer is armed with addJitter(next) (86) and re-armed with addJitter(normalInterval) after each success (114). addJitter (renew.go:1210-1212) is d + d*0.1*(2r-1), i.e. ±10%, so 54-66 s. The period runs from the end of one POST to the next (timer.Reset follows sendHeartbeat returning); the POST client has Timeout 10 s (renew.go:216). Failure path: initialRetry 5 s (72), maxRetry 2 min (73, 101-106), 429 floor rateLimitedMinRetry 60 s (47-54). The heartbeatTrigger (135-138) drains and re-arms the timer; it does not add periodic beats. Server: internal/api/types.go:37-40 says 60s and nodeStaleThreshold is 3 min; internal/api/renew.go sends no interval. internal/client/api.go:35 HeartbeatInterval (default 60 s, api.go:102-103) has no reader outside tests.

**Correction:** The real interval is 60 s ±10% (54-66 s) measured from the end of one POST to the start of the next, so the period is that plus the POST duration (up to 10 s). The renew.go:62 comment is stale. The HeartbeatInterval config field is dead.

## Proposal claims A-H

### A: Nebula's own parser validates the proposed firewall policy before the file is touched, and a rejected policy comes back as an error (proposal steps 3 and 6).

**Verdict:** wrong

ReloadConfigString (vendor/github.com/slackhq/nebula/config/config.go:175-195) returns only the error from LoadString, which covers YAML syntax. Callbacks return nothing, so a semantic rejection cannot reach the caller. reloadFirewall (vendor/github.com/slackhq/nebula/interface.go:478-482) logs a NewFirewallFromConfig error and returns, so ReloadConfigString returns nil and the old rules stay. Invalid proto or port values are rejected inside AddFirewallRulesFromConfig (firewall.go proto switch, about 362-383), which is reached only through that logged path. Unknown group names are not rejected at all (convertRule around 944 errors only for group arrays longer than one).

**Correction:** A YAML syntax error does return an error and runs no callbacks. A semantically bad policy returns nil from ReloadConfigString, so proposal step 5 (rename the new file over nebula.yaml) would run on a policy the running firewall rejected. The file would then hold a policy that may not start on the next boot (not verified). Validation needs an explicit check before step 4: a scratch NewFirewallFromConfig call (needs a cert state; not verified that it can run standalone) or a patch that returns the error from reloadFirewall. Unknown group names need a separate check against the groups the certs carry.

### B: A firewall reload keeps open flows. Conntrack is carried over, and an open flow is re-checked on its next packet and dropped only if no rule matches any more.

**Verdict:** confirmed

reloadFirewall (vendor/github.com/slackhq/nebula/interface.go:462-504) runs only when HasChanged("firewall") or the cert unsafe networks changed (473-476). It builds the new firewall (478), sets rulesVersion to old+1 (489), and copies the old Conntrack into the new firewall (499) unless rulesVersion wrapped to 0 (492-497, which resets conntrack). The swap is at 502 and the old firewall is destroyed at 504. inConns (firewall.go, about 505-559) detects an entry whose rulesVersion differs from the current one (527), re-runs table.match, deletes the entry if no rule matches (about 540-545), and otherwise sets c.rulesVersion to the current value (559). Drop (firewall.go:425-470) calls inConns before the table match. The callback touches neither hostmap nor tunnels.

**Correction:** None needed for the core claim. Note the wrap-to-zero case, where conntrack is reset and every open flow is re-checked as new.

### C: A firewall-only reload does not disturb tunnels.

**Verdict:** partly

The firewall callback itself touches only the firewall and conntrack (interface.go:462-504). But ReloadConfigString runs every registered callback (config.go:175-195). RegisterConfigChangeCallbacks (interface.go:440-450) registers reloadSendRecvError, reloadAcceptRecvError, reloadDisconnectInvalid (guarded, 452-460), reloadMisc (guarded) and each UDP writer's ReloadConfig. The PKI reload is registered without a guard (vendor/github.com/slackhq/nebula/pki.go:59-70). pki.reload has no HasChanged check (pki.go:82 onward) and re-reads the cert, key and CA files (newCertStateFromConfig at pki.go:290, loadCAPoolFromConfig at pki.go:525); a changed network or curve is rejected with a logged error. On Linux, StdConn.ReloadConfig re-applies listen.read_buffer, listen.write_buffer and listen.so_mark when they are set (vendor/github.com/slackhq/nebula/udp/udp_linux.go:268 onward). hopssh sets none of them, and ensureP2PConfig deletes read_buffer and write_buffer (internal/client/renew.go, ensureP2PConfig from about 911).

**Correction:** The claim holds for the firewall part of the reload. A full-text reload also re-runs the PKI callback, which re-reads the cert files on disk, so a renewed cert written since startup is swapped in during the firewall reload. Whether an unchanged cert causes any disturbance is not verified. Describe the reload as firewall-only in its diff, not in the callbacks it runs.

### D: The agent already holds the *config.C it started Nebula with, so the reload in step 4 needs no new state.

**Verdict:** wrong

startNebula declares `var cfg config.C` as a local (internal/client/nebula.go:67), loads it (68), and passes it to nebula.Main (77) without storing it. startNebulaKernelTun does the same (nebula.go:129, 139). The userspaceMeshService and kernelTunMeshService structs (nebula.go:59-62 and about 158-163) hold svc, ctrl, meshIP and devName, not the config. A grep of internal and cmd (non-test) finds no ReloadConfig or RegisterReloadCallback call in the client code. The other config.C sites are internal/mesh/network_manager.go:559 and internal/e2etest/peer.go:34, both local variables.

**Correction:** Add a cfg *config.C field, set at nebula.go:67-77 and 129-139. It must be the same pointer passed to nebula.Main, because Nebula registers its callbacks on that pointer (interface.go:440-450). The reload must check closed under the same lock that setSvc uses (instance.go:26 holds svc). Otherwise a late reload would run callbacks on a dead Interface.

### E: A peer that firewalls TCP 41820 from the node group makes the keepalive count that peer as stuck, and three cycles trip the watchdog.

**Verdict:** partly

The keepalive probes every non-lighthouse peer's mesh IP on TCP 41820 (keepaliveOneCycle, internal/client/keepalive.go:338-385; lighthouses are skipped via readLighthouseAddrs). A dropped packet times out after keepaliveDialTimeout (2 s, about line 50), so the probe fails. The stuck predicate is `peers > 0 && probed > 0 && succeeded == 0` (keepalive.go about 168-178), and the trip occurs at watchdogStuckThreshold = 3 (line 73). The counter resets on any success (178). keepaliveProbe counts a 'refused' error as success (about 111-130), so a peer that answers with a reset does not trip the watchdog.

**Correction:** The trip needs every probed non-lighthouse peer to fail in the same cycle. A single firewalled peer alongside one reachable peer keeps succeeded above zero and does not trip. The trip happens when the firewalled peer is the only probed peer, or when all probed peers are firewalled. Whether a reject-mode firewall answers with a reset (which would count as success) is not verified.

### F: A stuck peer makes the watchdog restart the network about every 4.5 minutes (the wiki's figure).

**Verdict:** partly

The first probe is at keepaliveInterval/2, about 45 s with jitter (keepalive.go:152), then every 90 s (keepalive.go:50, 155). The first trip is on the third stuck cycle, about 225 s after start. The counter resets on each trip (keepalive.go:175), so trips recur every three cycles, which is 270 s = 4.5 min (the diagnostic dump at keepalive.go:315 prints the same product). Restarts are not every 4.5 min: watchdogTrip checks restartCoolingDown, and noteWatchdogRestart and watchdogRestartNotBefore (internal/client/recovery.go:283-321) impose a 5 min wait that doubles with each restart, capped at 6 h, and the streak resets only after 12 h without a restart. client.go:97 sets inst.restartNotBefore. The restart happens on the probing agent (restartFn in watchdogTrip), not on the firewalled peer.

**Correction:** Detection repeats about every 4.5 min while the peer stays stuck, but restarts are spaced by the backoff: at least 5 min after the first restart, then 10, 20 min and so on up to 6 h. Say that restarts back off, not that they repeat every 4.5 min. recovery.go:303-305 already notes this exact case.

### G: The device firewall block comes from one template, so one renderer covers every device (phase P0).

**Verdict:** wrong

Current templates: the agent block (internal/client/enroll.go:655-674: tcp 41820 from admin, tcp any from node, icmp) and the client block (internal/client/cli_join.go:206-214: inbound icmp only). The round-tripped form is in internal/client/testdata/expired-cert-mbp-2026-05-02/nebula.yaml. History (git log -G 'groups:'): 169ee66 (tcp 8080 from any), c524fa2 and 80fdbaa (groups [server] and cert group agent), 791ac0b (groups admin, user and agent), 3f30d27 (client icmp only), 41ce077 (node group), 00a4e19 and a94baf7 (the current agent block with fixtures).

**Correction:** Phase P0 needs at least the current agent and client blocks, the round-tripped form, and the legacy [server] and [user, agent] forms with cert group agent. Those legacy forms must parse; golden tests should compare parsed rules, not text.

### H: Roadmap item 8 describes account-scoped tags and rules with per-network opt-in.

**Verdict:** confirmed

docs/roadmap.md:102 (item 8, Granular firewall rules): "Tags, roles, and rules should be reusable across multiple networks in the same account (matches DN's model where a user's rule library applies to all their networks) — schema keys rules to the account/team, not the network, with per-network opt-in." The same row adds: "tag changes require cert renewal (24h cycle, or add immediate renewal endpoint)". docs/roadmap.md:67 says: "No granular firewall rules". Item 20 (docs/roadmap.md:154) builds on #8.

**Correction:** None. The proposal's per-network policy row is narrower than item 8, which the proposal says it defers (see its Decide section).

### Extra

 Claim wording for A to H was reconstructed from an earlier summary, not copied from the original task text. Match each verdict to the original claim before using it; the labels are A to H in the order given.

Hazard for the proposal's step 4 (not a claim, but it breaks the design). ReloadConfigString diffs the new text against the last loaded settings and runs every callback, so anything written to nebula.yaml since the last load is applied on the next reload. The writers are applyNebulaConfigUpdate (internal/client/renew.go:774-812, atomicWrite at 812, called at 756; the excerpt read does not show a restart), routes.go:145-175 (non-atomic write at 173, then reloadNebula at 63), listenport.go:49-73 (write at 73), ensureP2PConfig (renew.go about 911 onward, atomicWrite at about 1136) and upgradeTunMode (nebula.go:586, atomicWrite at 617). The relay and punch keys are guarded callbacks (hostmap.go:337, relay_manager.go:31, punchy.go:70), so a full-text reload would apply them. Whether each writer restarts Nebula afterwards was not confirmed for renew.go:756.

Not verified: whether an unchanged cert causes a disturbance in the PKI reload; the rest of the punchy reload; tun_windows.go:104 reload; whether reject-mode firewall returns a TCP reset; whether NewFirewallFromConfig can run standalone for a scratch check; whether a rejected policy written by step 5 stops the next start; the 14.4 h figure (renew.go:631-636); the fallback listener and firewall pointer-swap findings in the document's "Found on the way" section (not re-checked in this pass, though recorded as confirmed earlier); the e2etest harness.

## Reload spike

**Firewall reload spike: Nebula v1.11.2 (vendored, with hopssh patches), two userspace nodes on localhost, single runs**

Test file (scratch copy only, repo untouched): (scratch copy). It keeps the `*config.C` (unlike `peer.go`'s `StartPeer`, which drops it and uses a lighthouse). The logs of the functional run and the four race runs were kept outside the repo.

**1. Baseline.** Flow A→B:9000 up in 302–403 ms, including handshake. 96 of 96 writes echoed. B's hostmap for A: localIdx=2793267667, remoteIdx=784675516, currentRemote=127.0.0.1:63818.

**2. Reload B with inbound TCP 9001 only.** ReloadConfigString returned nil in 0.7 ms.
- The flow stops at the first packet after the reload. The first unechoed write was at T0+4.4 ms. No echo arrived at or after T0 (0 of 145 writes echoed in 3 s).
- Counters over 3 s: A sent 107 packets, and B's Nebula counted all 107 before the firewall. All 107 were dropped with "no matching rule". The drop is silent, with no RST. A stopped transmitting about 2.2 s after the reload.
- Dial to :9000 failed with deadline exceeded at 3.00 s.
- Dial to :9001 succeeded in 0.31 ms. Hostmap indexes and currentRemote were unchanged, so there was no new handshake.

**3. Invalid rule (`proto: foo`).** ReloadConfigString returned nil, not an error. The failure appears only as a log line: ERROR "Error while creating firewall during reload" error="firewall.inbound rule #0; proto was not understood; `foo`". The previous firewall stayed in effect: :9000 still timed out and :9001 still passed. The config object now holds the bad text, so the next reload diffs against it.

**2b. Restore the full rules.** Installed as rulesVersion=2. The old flow did not recover in 30 s: 0 echoes. A sent 5 packets, B's firewall took 6 with no drops, and B's stack sent 5. A fresh dial to :9000 succeeded in 0.36 ms with the same indexes. A flow killed by a firewall change stays dead after the rule returns, but a new connection works. I did not establish the TCP-level cause.

**5. Identical text.** Returned nil in 0.4 ms. DEBUG "No firewall config change detected". No "New firewall has been installed" and no handshake (indexes unchanged, msgCounter 208 to 210). The PKI reload callback still runs on every reload: INFO "Client certificate(s) refreshed from disk" re-reads the cert files.

**4. Race (`go test -race`, 10 reloads under traffic, each with different text).** Yes. The detector reports races on Interface.firewall and the test FAILs with "race detected during execution of test". Two runs gave 10 and 15 reports. A control with the same traffic and no reloads gave 0. Key pair:
```
Read at ... (*Interface).handleOutsideMessagePacket() outside.go:521   [f.firewall.Drop]
Previous write at ... (*Interface).reloadFirewall() interface.go:502   [f.firewall = fw]
```
The same pair appears with consumeInsidePacket() at inside.go:75. The detector also reports the new *Firewall's fields: written in NewFirewall (firewall.go:167) and read in Drop (firewall.go:454) and inConns (firewall.go:511), plus bart table nodes. The pointer is published with no synchronisation. I saw no wrong drop decision. Without -race: 10 reloads gave 10 installs, 0 drops, and a longest echo gap of 2.6 ms, against 2.5 ms with no reloads.

**Implications**
- A firewall change applies from the next packet. Flows whose port is no longer allowed die at once and do not come back when the rule returns. The tunnel itself is untouched.
- ReloadConfigString does not report firewall errors. The caller only sees them in logs.
- hopssh does not call ReloadConfig or ReloadConfigString today (grep over internal/ and cmd/). Production reloads restart the instance (reloadNebula), so the race is latent.

**Caveats**
- Single runs on localhost. Timings are bounded by the 20 ms writer cadence.
- The Debug line "dropping old conntrack entry" never appeared, although the flow had been allowed earlier. I did not work out why. Packets were dropped from the first one regardless.
- The ICMP rule is in the config but not exercised, because the service API has no ICMP.
- Why the old TCP flow does not revive (A stalls after about 2.2 s, then sends 5 packets in 30 s) is probably TCP backoff in gvisor. I did not verify this.

report.md was not written, because the agent rules forbid report .md files. This text is the report.
