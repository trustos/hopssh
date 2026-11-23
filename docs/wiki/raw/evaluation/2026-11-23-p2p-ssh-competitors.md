# P2P SSH landscape (2026-11-23)

Web research by a subagent from primary sources. Pricing was read through a summariser, so spot-check numbers before publishing.

P2P SSH landscape, researched 2026-11-23 from primary docs, GitHub issues and changelogs fetched today. Pricing pages were read through a summariser, so spot-check numbers before publishing. Bracketed numbers map to the source list at the end.

COMPARISON

| Product | SSH model | UDP-blocked path | Resilience / known breakage | Browser terminal, recording | Expiry / outage | Price, licence |
|---|---|---|---|---|---|---|
| Tailscale | Own SSH server, not sshd. Keyless via tailnet identity. Server on Linux and macOS CLI build only. Port 22 only [1] | Peer relay (UDP), then DERP over HTTPS 443 [5][6] | Restarting tailscaled ends SSH sessions [1]. macOS wake bugs open [16] | Console is beta, admin-only, all traffic via DERP [3]. Recorder deprecated, support ends Aug 2027 [2] | Keys expire after 180 days by default, range 1-180 [4]. Netmap caching is opt-in, client 1.104+ [8] | Personal free, up to 6 users. Standard $8, Premium $18 per user/month [9][10] |
| NetBird | Embedded server, port 22 redirected to 22022, OIDC JWT per session, OpenSSH clients only [17][18] | QUIC over UDP, WebSocket over TCP 443 fallback [20] | Issue #5917 (open): SSH stalls up to 60 min after JWT expiry [19] | Browser client via relays. RDP Windows-only, no SFTP. No recording documented [21] | Peer login expiry 24 h default, 1 h-180 d. Setup-key peers exempt [22] | Free 5 users. Team EUR6, Business EUR12 per user [23]. Server AGPL-3.0, client BSD-3 [24] |
| ZeroTier | No SSH feature. System sshd over ZT IP (inference) | UDP hole punch. Hosted TCP relay documented as "slow". Self-host via pylon [28] | Not verified | None found | Not verified | Personal free, 10 devices, non-commercial. Essential $18/month [29] |
| Headscale / Headplane | Headscale runs no SSH; tailscaled on nodes does (inference) | Inherits client | Inherits | Headplane browser SSH needs Headscale 0.28+, OIDC. Broken on 0.29 beta through 0.29.1 [25] | node.expiry defaults to 0 in docs. Auth-key default-expiry bug closed Apr 8 2026 [26] | OSS |
| Cloudflare | Access-for-Infrastructure short-lived certs. No port forwarding, ProxyJump or agent forwarding [33] | Tunnel is outbound. Mesh always transits Cloudflare, not P2P [31] | Access session 24 h default. Gateway SSH max 10 h [33] | Browser SSH via public hostname, no client [32]. Command logs encrypted with your key [33] | n/a | Free up to 50 users (third-party only) [34] |
| Teleport | Own SSH server | n/a | n/a | Recording exists; Community status unverified | n/a | Community binaries free only for companies under 100 staff and $10M revenue, since Teleport 16 [35] |
| Twingate | Connector model. SSH privileged access is a paid add-on with recording [36] | Connectors reach relays | n/a | Recording is an add-on [36] | n/a | Starter free, 5 users. Teams $5, Business $10 per user [36] |
| iroh-ssh / pigeons | System sshd over QUIC. Reachable by endpoint ID. No allowlist [38] | Relay fallback [37] | Not verified | None | Endpoint ID is the only gate | MIT/Apache. Hobby-scale (339 and 208 stars) |
| Pangolin | Browser SSH via site connector. CLI over WireGuard P2P [39] | Not described | Not described | Recording not described | Standard SSH mode certs last 5 min [39] | Licence not verified |
| Octelium | Claims keyless SSH with per-connection authz and recording [41] | n/a | n/a | Vendor claim only | n/a | FOSS, founded 2025 |
| Netmaker | No SSH found | WireGuard | n/a | n/a | n/a | Advisory: network-scoped users could read WireGuard private keys, through 1.7.0 [40] |
| Mosh / ET | Mosh is UDP 60000-61000 [42]. ET is TCP 2022, bootstraps via ssh [43] | Mosh needs UDP. ET is TCP only | Both survive sleep and IP change. Mosh syncs visible screen only [42][43] | None | n/a | OSS. ET has telemetry on by default and stores reattach credentials in plaintext [43] |
| Nebula / Defined | No SSH feature found | Not verified | Not verified | None found | Not verified | Free up to 100 hosts. Pro $1/host/month [30] |

Security context: Tailscale published four SSH and serve bulletins on Oct 26 2026. They concern peers already authorised to SSH escalating privilege. Fixed in 1.104.4. Interim mitigation is TS_SSH_DISABLE_FORWARDING=1 [11][12]. Headscale 0.29.7 followed on Oct 30 2026 [27].

(a) TABLE STAKES, LATE 2026
1. Identity-bound SSH with a re-auth step: Tailscale check mode (12 h default), NetBird per-session JWT, Cloudflare certs, Pangolin 5-minute certs.
2. A TCP/443 fallback. Tailscale has DERP and NetBird has WebSocket. Your UDP-only relay is the largest gap in this list. Treat it as must-fix, not a differentiator.
3. A browser terminal. Tailscale (beta), NetBird, Cloudflare, Pangolin and Headplane all ship one.
4. Expiry with a remote recovery path. Tailscale's only documented fix is a 30-minute "Temporarily extend key" window [4].
5. Identity-linked audit logs. Nobody gives this cleanly for free. Tailscale deprecated its recorder, Teleport's free binaries exclude larger companies, Cloudflare needs key management, NetBird doesn't document it.
6. Fast security response. Tailscale's Oct 2026 bulletins show that well-funded teams ship SSH bugs too.

(b) UNMET NEEDS A SMALL PRODUCT COULD BEAT
1. Unattended nodes that cannot be silently locked out. Tailscale #19785 (opened May 18 2026, open, no maintainer reply) [13]. Open requests exist for headless auto-renewal (#16566) and expiry warnings (#14647) [13]. Our renewal authenticates with a bearer token, not the expired cert (per CLAUDE.md). So "unattended nodes never expire silently" plus a remote re-enrol path is credible. Verify our own expiry behaviour before claiming it.
2. Sessions that survive sleep and roaming. NetBird #5917 stalls SSH for up to an hour after token expiry [19]. Tailscale users report DNS and internet loss after macOS wake [16]. Mosh and ET show demand for resumable sessions [42][43]. A resumable SSH transport inside the mesh is worth prototyping. I found no benchmark, so measure first.
3. Free, self-hosted, identity-tagged session recording. You already own the PTY behind the web terminal, so an asciinema-style recorder with a per-user index is feasible. Tailscale's recorder ends Aug 2027 [2]. Teleport's free licence excludes larger companies [35].
4. Control-plane outage tolerance by default. Tailscale's netmap caching is opt-in, and a cached client cannot reach new peers [8]. A cached-state restart on by default would be a clear step up, once I check what our restart path does today.
5. A browser terminal with no beta gate, no public hostname and no version traps. Cloudflare needs a public hostname [32]. Tailscale's console is admin-only beta [3]. Headplane broke on Headscale 0.29 beta [25]. Our existing web terminal can be the default, and routing it through the mesh is a real difference (inference).

(c) WHERE COMPETING IS HOPELESS SOLO
- Global relay fleets. DERP and NetBird's public relays are run at scale. Self-host relays and do not promise uptime.
- Windows SSH server and macOS GUI parity. Tailscale has tracked Windows SSH since 2022 (#4697). PR #21312 was sent Sep 23 2026 and is still unreleased as of Nov 13 [14].
- Enterprise PAM, SCIM, posture checks and compliance attestations.
- Mobile background networking. CLAUDE.md already notes this needs an Apple Developer ID and the NE entitlement.
- Maintaining a patched Nebula fork at scale. Our 25 patches already need manual checks (CLAUDE.md). That is a warning sign, not a plan (inference).

COULD NOT VERIFY
Tailscale session survival across IP change (no official doc). NetBird and ZeroTier controller-outage behaviour. ZeroTier key expiry. Cloudflare's official free-tier user count (third-party says 50). Teleport Community recording. Pangolin licence. Octelium claims (vendor only). Nebula/Defined SSH features. NetBird pricing conflicts: official page says EUR6/EUR12, Capterra says $5/$12. I used the official page. ZeroTier "2.0": no GA found. The current line is 1.16.x [44].

SOURCES (date as shown on page, or "undated")
[1] https://tailscale.com/docs/features/tailscale-ssh (validated Jan 5 2026)
[2] https://tailscale.com/docs/features/tailscale-ssh/tailscale-ssh-session-recording (validated Jan 5 2026)
[3] https://tailscale.com/docs/features/tailscale-ssh/tailscale-ssh-console (undated)
[4] https://tailscale.com/docs/features/access-control/key-expiry (undated)
[5] https://tailscale.com/docs/reference/connection-types (undated)
[6] https://tailscale.com/docs/reference/faq/firewall-ports (undated)
[8] https://tailscale.com/docs/reference/client-side-netmap-caching (undated)
[9] https://tailscale.com/pricing (fetched Nov 23 2026)
[10] https://tailscale.com/blog/pricing-v4 (Apr 8 2026)
[11] https://tailscale.com/security-bulletins (Oct 26 2026)
[12] https://tailscale.com/ssh-investigation-update (updated Oct 27 2026)
[13] https://github.com/tailscale/tailscale/issues/19785 (opened May 18 2026). #16566 and #14647 are undated.
[14] https://github.com/tailscale/tailscale/issues/4697 (updated Nov 13 2026)
[15] https://github.com/tailscale/tailscale/issues/16550 (Jul 13-23 2025)
[16] https://github.com/tailscale/tailscale/issues/14867 and /issues/17727 (dates not captured)
[17] https://docs.netbird.io/manage/peers/ssh (undated)
[18] https://github.com/netbirdio/netbird/releases/tag/v0.60.0 ("18 Nov", year not shown; likely 2025 by version sequence)
[19] https://github.com/netbirdio/netbird/issues/5917 (opened Apr 18 2026, updated Oct 27 2026)
[20] https://docs.netbird.io/selfhosted/maintenance/scaling/set-up-external-relays (undated)
[21] https://netbird.io/knowledge-hub/browser-client-ssh-rdp (Oct 23 2025)
[22] https://docs.netbird.io/manage/settings/enforce-periodic-user-authentication (undated)
[23] https://netbird.io/pricing (fetched Nov 2026)
[24] https://netbird.io/knowledge-hub/netbird-agpl-announcement (undated)
[25] https://headplane.net/features/ssh and https://headplane.net/CHANGELOG (0.7.1, Aug 27 2026)
[26] https://github.com/juanfont/headscale/issues/1711 (closed Apr 8 2026)
[27] https://github.com/juanfont/headscale/blob/main/CHANGELOG.md (0.29.7, Oct 30 2026)
[28] https://docs.zerotier.com/relay/ and https://docs.zerotier.com/cli (undated)
[29] https://www.zerotier.com/pricing/ (fetched Nov 2026)
[30] https://www.defined.net/compare/nebula-vs-tailscale (undated)
[31] https://developers.cloudflare.com/mesh/concepts/ (modified Sep 16 2026); announcement at https://developers.cloudflare.com/changelog/post/2026-04-14-cloudflare-mesh/ (Apr 14 2026)
[32] https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-browser-rendering/ (undated)
[33] https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/use-cases/ssh/ssh-infrastructure-access/ (undated)
[34] https://zerometric.net/research/cloudflare-zero-trust-free-plan-limits-2026 (Jul 18 2026, third-party)
[35] https://goteleport.com/blog/teleport-community-license (Mar 8 2024)
[36] https://www.twingate.com/pricing (page dated Nov 20 2026)
[37] https://www.iroh.computer/blog/the-road-to-iroh-1-0 (Jul 9 2026)
[38] https://github.com/rustonbsd/iroh-ssh and https://github.com/n0-computer/pigeons (undated)
[39] https://pangolin.net/news/how-to-ssh-with-pangolin (Jun 14 2026)
[40] https://github.com/advisories/GHSA-f2gq-2gr7-rjjc (date not captured)
[41] https://octelium.com/solutions/zero-trust-ssh (vendor, undated)
[42] https://mosh.org/ (latest release 1.4.0, Oct 31 2022)
[43] https://github.com/MisterTea/EternalTerminal (undated)
[44] https://www.zerotier.com/blog/a-cleaner-core-zerotier-1-16-2-update-is-here (undated, about May 2026)
