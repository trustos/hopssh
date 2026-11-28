**Short answer:** Tailscale, ZeroTier and Headscale put a text policy language under their web UI. Defined Networking and NetBird expose only forms. All five use a web admin UI as the main surface. Sources were read on 2026-11-27. "Not in sources" means the pages I read did not cover it, not that the feature is absent.

## Tailscale
- **Where:** Access controls page of the admin console, GitOps, or the API. "Edit your tailnet policy file by using the Access controls page of the admin console" (https://tailscale.com/docs/features/tailnet-policy-file/manage-tailnet-policies). A visual editor went to beta on 2025-08-05. "Your policy continues to be stored as the familiar HuJSON" (https://tailscale.com/blog/visual-editor-beta).
- **Default:** Allow-all file, then deny-by-default once you write rules. "The default tailnet policy file enables communication between all devices" (https://tailscale.com/docs/features/access-control/acls). "Tailscale access rules deny access by default" (https://tailscale.com/docs/reference/syntax/policy-file).
- **Model:** Users, `group:`, tags, autogroups. Only tag owners can apply tags (https://tailscale.com/kb/1068/acl-tags). A rule has src, dst `host:ports`, and proto. Grants are the newer syntax, and they "combined ports and protocols into a" single `ip` field (https://tailscale.com/blog/grants-ga). "ACLs can only determine if a connection is allowed at the network level" (https://tailscale.com/docs/reference/migrate-acls-grants).
- **Delivery:** "Tailscale validates the whole policy file when you save it" (https://tailscale.com/docs/features/access-control/grants/grants-explained). Clients "respond to the new rules within seconds" (https://github.com/tailscale/tailscale/issues/16169, quoting the KB). Open TCP connections are not dropped on ACL updates (https://github.com/tailscale/tailscale/issues/859). SSH sessions are: "terminates any active connections and sessions that no longer match" (https://github.com/tailscale/tailscale/blob/main/ssh/tailssh/tailssh.go).
- **Safety:** Preview rules lists a user's destinations "and the line number that defines that rule" (https://tailscale.com/docs/features/tailnet-policy-file/manage-tailnet-policies). Embedded tests: "Tailscale rejects the updated tailnet policy file with an error" (https://tailscale.com/docs/reference/syntax/policy-file). Rollback via Configuration logs to "a previous date and time", not under GitOps (same manage page). Lockout protection: not in sources.
- **Device override:** `tailscale set --shields-up`, "Block incoming connections from other devices" (https://tailscale.com/docs/reference/tailscale-cli/up). Admins can force it with the `AllowIncomingConnections` system policy (https://tailscale.com/docs/features/tailscale-system-policies). Device-owner view of applicable rules: not in sources.
- **Complaint:** Feature request "graphical rule management or a wizard" asks for "Setup ACL rules easier than needing a background in programming" (https://github.com/tailscale/tailscale/issues/7006).

## ZeroTier
- **Where:** Central, "Navigate to your network's Flow Rules tab" (https://docs.zerotier.com/rules/). Templated flow rules are on Essential and up. The custom rules engine needs Scale or Enterprise (same page). New Central's isolation UI is mentioned in https://docs.zerotier.com/faq/rulestags/.
- **Default:** "By default, flow rules are disabled and all traffic is allowed" (https://docs.zerotier.com/rules/). Once enabled, "If no permissive actions are taken... the packet is discarded" (same page).
- **Model:** Tags are "32-bit numeric key-value pair credentials" issued to members. Capabilities are "a small rule set bundled into a credential object" (same page). Match fields: ethertype, IP protocol, ports, addresses, tags.
- **Delivery:** Rules are "defined once in Central and distributed cryptographically to each node" (https://www.zerotier.com/blog/introducing-flow-rules-a-policy-engine-update-in-zerotiers-new-central/). Enforcement is "by both the senders and the receivers of packets" (https://docs.zerotier.com/rules/). Timing and effect on live flows: not in sources.
- **Safety and lockout:** Preview, tests, history: not in sources.
- **Device override:** The `zerotier-cli set` settings `allowManaged`, `allowGlobal`, `allowDefault` and `allowDNS` "does not affect the rest of the network" (https://docs.zerotier.com/cli/). They govern IP and route management, not inbound blocking (https://github.com/zerotier/ZeroTierOne/blob/dev/service/README.md).

## Defined Networking (Managed Nebula)
- **Where:** Admin panel Roles page, then "Add firewall rule", then "Save role". Tag rules are on the Tags page (https://www.defined.net/docs/guides/creating-firewalls-using-roles/). "Firewall rules apply to inbound traffic" (same page).
- **Default:** "A newly created role permits only ICMP and denies all other traffic" (https://www.defined.net/docs/architecture/). In the Nebula layer, "it is not possible to explicitly define a deny rule" (https://nebula.defined.net/docs/config/firewall/).
- **Model:** "Every host, lighthouse, and relay is assigned exactly one role" (https://www.defined.net/docs/guides/creating-firewalls-using-roles/). Tags are key:value and many per host. A rule matches role, tags, or both, and a rule has protocol, port range and allowed peers. The cert carries "its role and tags" (https://www.defined.net/docs/glossary/nebula-certificate/).
- **Delivery and certificates:** "Config and firewall changes reach hosts within a minute" (https://www.defined.net/). Changing a role or tag means the host "receives a new certificate... the next time DNClient checks in" (https://www.defined.net/docs/guides/managing-hosts/).
- **Existing connections:** "Connection tracking re-validates established flows when the rule set version changes" (https://www.defined.net/security/whitepaper/).
- **Safety:** Every admin-panel or API change "is recorded in the audit log" (https://www.defined.net/docs/glossary/admin-panel/). Rows show state before and after (https://www.defined.net/docs/guides/using-audit-logs/). Preview, tests, rollback and lockout: not in sources.
- **Device override:** Not in sources.

## NetBird
- **Where:** "Networking → Access Control → Policies" (https://docs.netbird.io/manage/access-control/manage-network-access). Also the policies API (https://docs.netbird.io/api/resources/policies).
- **Default:** Allow-all, created with the account: "very permissive because it allows communication between all peers" (same page). The docs say "the Default policy completely undermines zero trust" (https://docs.netbird.io/manage/access-control).
- **Model:** Source and destination groups, a bidirectional flag, protocol ALL/ICMP/TCP/UDP, and ports including ranges since 0.48. "Because you can only create ALLOW policies, there is no processing order" (manage page). Posture checks run "when a peer connects or logs in" and again on metadata change (https://docs.netbird.io/manage/access-control/posture-checks).
- **Wide rules:** "'All' protocol policies are always bidirectional regardless of your direction setting" (https://netbird.io/knowledge-hub/implement-zero-trust). "Portless rules are effectively 'all ports'" (https://docs.netbird.io/use-cases/security/implement-zero-trust).
- **Delivery:** "Peers receive the change without reconnecting" (https://docs.netbird.io/manage/access-control). Existing connections: not in sources.
- **Safety:** Audit events are "enabled by default for every NetBird network" and cover policy edits (https://docs.netbird.io/manage/activity). Preview, tests, rollback: not in sources.
- **Device override:** `--block-inbound` "overrides any policies received from the management service" (https://docs.netbird.io/get-started/cli). It can be pushed by MDM as `blockInbound` (https://docs.netbird.io/client/mdm-integration).

## Headscale and its UIs
- **Default:** "By default, no policy is loaded which means that Headscale allows all traffic" (https://github.com/juanfont/headscale/blob/main/docs/ref/policy.md).
- **Where:** A HuJSON file, or `policy.mode: database`. In file mode, "Headscale needs to be reloaded" for changes (same page). ACLs and grants are both supported. Device posture and IP sets are not (same page).
- **UIs:** Headplane writes only in database mode (https://headplane.net/features/acls). It has a structured editor, a raw editor and "Preview changes" as a diff. "The structured editors regenerate the policy text, which drops comments" (same page). headscale-admin has a visual ACL builder, but only when the ACL is stored in the database (https://faun.dev/toolbox/headscale-admin/).
- **Not in sources:** Propagation timing, effect on live connections, tests, rollback, lockout, device override.

## Patterns worth copying
1. **Default allow-all, with one visible lock-down step.** Four of the five products do this. Defined's default-deny with a ping-only new role is the safer alternative.
2. **Allow-only rules, no ordering.** NetBird and Defined have no deny rules, which removes rule-order bugs.
3. **Identify peers by role or tag, not IP.** Defined and NetBird group by role/tag, and Tailscale limits who can apply a tag with `tagOwners`.
4. **Visual builder over one canonical text file.** Say when a form rewrites the file and will drop comments, as Headplane does.
5. **"Who can reach X, and why" preview.** Tailscale's per-user list with the defining line is the best "why is this allowed" answer here.
6. **Save-time tests.** Tailscale rejects a bad policy on save. No other product here showed this.
7. **Revert plus an audit log with before/after state.** Tailscale, Defined and NetBird all have one.
8. **Say what happens to live sessions.** Defined re-checks tracked flows, Tailscale SSH kills sessions, and open TCP survives. The UI should state which applies.
9. **A labelled local "block all inbound" switch** that overrides central policy (Tailscale shields-up, NetBird `--block-inbound`), with an admin push option.
10. **Warn on wide rules** such as all-protocol and portless rules.
11. **Groups in the certificate mean a role change needs a reissue.** Defined reissues on next check-in. Nebula's changelog says that on a new cert "we now rehandshake with all existing tunnels" (https://github.com/slackhq/nebula/blob/v1.9.5/CHANGELOG.md).