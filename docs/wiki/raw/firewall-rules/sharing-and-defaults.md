# Firewall policy across networks, and default policy for a new network

Research for hopssh firewall-rule design. All pages were read on 2026-11-27 unless marked "search snippet" (seen in search results, page not fetched in full). Dates in brackets are the dates shown on the page itself. Quotes are kept to 15 words or fewer. Where a fact is inferred rather than stated, the text says so.

Caveat: page content was retrieved through a summarising fetch. Exact wording should be checked against the live page before any quote goes into product copy.

---

## 1. Scope and inheritance of policy

### Summary table

| Product | Networks per account | Where policy lives | Reuse across networks | Groups / tags scope | Templates or inheritance | Conflict model |
|---|---|---|---|---|---|---|
| Tailscale | One tailnet per account is normal. Several tailnets are alpha, sales-only, same domain and IdP. | One policy file per tailnet | Not documented | Defined in each tailnet's policy file | None. The allow-all grant example is a "starting point" only. | Union of grants. No deny rules. Deny by tests. |
| NetBird | One account = one mesh. "Networks" are resource containers, not overlays. | Account (policies between peer groups) | Not applicable; account-wide settings | Groups are account objects; scope not stated explicitly | None | Allow-only policies, no ordering, default deny |
| Defined (Managed Nebula) | Several networks per account, each with its own CA and lighthouse | Account holds roles, tags, firewall rules, API keys; networks deploy them | Yes: roles, tags, firewall rules, API keys reusable across networks | Account-level roles and tags | Reuse of definitions only. No override described. | Allow-only rules, union per host. Blocking is a separate mechanism. |
| ZeroTier Central | Organization > Network Groups > Networks | Flow rules per network | Not documented | Not stated | Templated flow rules per network. Network Groups "manage access control for many networks". | Ordered rules: accept, drop, break. Unmatched traffic dropped once rules are on. |
| Headscale (+ Headplane) | One policy per Headscale server (policy.path) | Server config or database | No multi-network notion in pages read | Groups and tagOwners in the policy file | None | Tailscale HuJSON semantics (inferred from format match) |
| Netmaker | Any number of networks on one server | Per network (Access Control tab) | Cross-network rules only for site-to-site egress resources | Not stated | Network creation default (ALLOW or DENY) is fixed at creation | Not stated in pages read |
| Twingate | "Network" is the tenant | Policies attach to Resources (migration); group-specific policy per resource | Not documented | Groups | Default Policy auto-assigned to new Resources | Deny by default; access needs group membership |

### Per product

**Tailscale.** A tailnet has one policy file. Multiple tailnets under one organization are "currently in alpha" and need a sales contact; all must share the same domain and identity provider. Each tailnet has its own policy file. Groups from directory sync can be referenced in each tailnet's policy, but users are not synced across tailnets. [https://tailscale.com/docs/features/multiple-tailnets, read 2026-11-27, last validated Feb 4 2026]. The alpha blog describes each tailnet as keeping "its own distinct policy file, tags, devices, and settings" [https://tailscale.com/blog/multiple-tailnets-alpha, search snippet, 2026-11-27].

Policy evaluation: ACLs are deny-by-default and directional [https://tailscale.com/docs/features/access-control/acls, read 2026-11-27, last validated Oct 29 2026]. Grants are the recommended format for new tailnets and existing deployments, and grants and ACLs can coexist in one file during transition [https://tailscale.com/docs/reference/grants-vs-acls, read 2026-11-27, last validated Aug 1 2025]. Grants add together. There are no deny rules, and no grant can remove or narrow access another grant gives. A tailnet denies all access that no grant permits. To keep access blocked, add a test with a deny entry [https://tailscale.com/docs/features/access-control/grants/grants-explained, read 2026-11-27, last validated Nov 4 2026].

Templates and inheritance: none documented. The allow-all grant example is described as a starting point before restrictive policies [https://tailscale.com/docs/reference/examples/grants, read 2026-11-27, no date shown].

**NetBird.** An account is one overlay. A NetBird "Network" is a configuration object mapping one environment's routed resources; the docs say to create "as many Networks as you have environments" [https://docs.netbird.io/manage/networks, read 2026-11-27, no date shown]. The Networks settings tab "holds network-wide configuration that applies to every peer in your account" [https://docs.netbird.io/manage/settings/networks, read 2026-11-27, updated Aug 19 2026]. Policies connect groups of peers, or groups to resources. The docs do not say explicitly whether policies and groups are account-scoped; the Default policy is described as created "when you first create a NetBird account" [https://docs.netbird.io/manage/access-control, read 2026-11-27, updated Nov 9 2026].

Policy model: each policy has a source group and a destination group, with protocol ALL, ICMP, or TCP/UDP with or without ports. Peer-to-peer policies can be unidirectional or bidirectional; policies to network resources are always unidirectional. No deny rules or ordering are described, and "without policies, no peer can communicate with another peer" [https://docs.netbird.io/manage/access-control, read 2026-11-27, updated Nov 9 2026].

Resources are not members of the built-in All group, so a resource with no policy is reachable by no one [https://docs.netbird.io/manage/networks, read 2026-11-27]. Routes are different: "By default, Routes allow unrestricted access when no access control groups are assigned" [https://docs.netbird.io/manage/network-routes/access-control, search snippet, 2026-11-27].

Templates and inheritance: none documented.

**Defined Networking (Managed Nebula).** Several networks per account are supported. The blog announcing this says the "roles, tags, firewall rules, and API keys" set up in the account "can be reused across networks, but the networks themselves remain separate". Hosts on different networks cannot talk to each other, enforced by Nebula certificates [https://www.defined.net/blog/multiple-networks/, read 2026-11-27, posted Dec 10 2024].

Each network gets its own CA, held by Defined Networking, and a managed lighthouse by default [https://www.defined.net/docs/architecture/, read 2026-11-27, last updated Nov 9 2026; https://www.defined.net/docs/get-started/managed-lighthouses/, search snippet]. A device can be enrolled in several networks, with one host record per network. Only one mobile site can be connected at a time [https://www.defined.net/docs/get-started/mobile-nebula/, read 2026-11-27, last updated Nov 6 2026].

Rule model: firewall rules are inbound only, attached to a role or a tag. A host's inbound set is the union of its role's rules and each tag's rules. Within one rule, role and all listed tags must match (AND) [https://www.defined.net/docs/guides/creating-firewalls-using-roles/, read 2026-11-27, no date shown]. The docs describe allow rules only [https://www.defined.net/docs/glossary/firewall-rules/, read 2026-11-27]. Blocking a host is a separate mechanism: the certificate is put on a blocklist and pushed to every host within 60 seconds [https://www.defined.net/docs/guides/blocking-and-unblocking-hosts/, search snippet, 2026-11-27].

Templates and inheritance: none. Reuse is by shared definition, not by override.

**ZeroTier Central.** Hierarchy: Organization > Network Groups > Networks. A Network Group "allow[s] you to categorize and manage access control for many networks at once" [https://docs.zerotier.com/networks/, read 2026-11-27, dated Oct 6 2026]. Deleting a group deletes all networks in it [same page]. The docs do not say that rules or members are shared across networks in a group; flow rules are described per network ("your network's Flow Rules tab") [https://docs.zerotier.com/rules/, read 2026-11-27, dated Oct 6 2026].

Rule model: flow rules are checked top to bottom. Accept permits and ends evaluation; drop blocks; break stops the base rule set while capabilities still apply. A packet that reaches the end without acceptance is dropped. The default template ends in an explicit accept [https://www.zerotier.com/blog/advanced-access-custom-flow-rules-arrive-in-zerotier-one/, read 2026-11-27, date not visible]. Templated flow rules include block-all, allow selected traffic types, custom ports, and excluded devices [https://docs.zerotier.com/rules/, read 2026-11-27]. Tier wording conflicts within the same page: templated rules are "Essential tier and higher" in one place, and custom flow rules are stated for "Scale and Enterprise" plans elsewhere on the page. Check current pricing before relying on either.

Tag-based segmentation inside one network: "Devices will be able to talk only if they have at least one overlapping role" [https://docs.zerotier.com/microsegmentation/, read 2026-11-27, no date shown].

**Headscale (+ Headplane).** Headscale uses one policy, loaded from policy.path in the server config; reload is needed for changes [https://raw.githubusercontent.com/juanfont/headscale/main/docs/ref/policy.md, read 2026-11-27, no date shown]. The page describes no per-network or multi-tailnet policy. The policy uses the same HuJSON format as Tailscale ACLs [same page].

Headplane's Rules tab edits acls, ssh and hosts; its Tags and Groups tab edits groups and tagOwners. Writes need database policy mode; in file mode the page is read-only [https://headplane.net/features/acls, read 2026-11-27, no date shown].

Templates and inheritance: none.

**Netmaker.** Networks are separate environments; the docs allow "any number of distinct networks" on one server, with no limit stated [https://docs.netmaker.io/docs/netmaker-network-setup-concepts/1-create-networks, read 2026-11-27, no date shown]. The default ACL (ALLOW or DENY) is set when the network is created and cannot be changed afterwards [same page; also https://docs.netmaker.io/docs/netmaker-network-setup-concepts/3-configure-traffic-flow, search snippet]. Access-control rules between egress resources on different networks are supported for site-to-site setups [https://learn.netmaker.io/features/acls-access-controls/network-access-control, read 2026-11-27, no date shown]. Other cross-network policy is not described.

Templates and inheritance: none. The fixed network default is the nearest equivalent.

**Twingate.** Each network has a Default Policy that is automatically assigned to every new Resource. It can be edited, not deleted [https://www.twingate.com/docs/resource-policies, read 2026-11-27, relative date only]. Since the policy-on-resource migration, policies attach to Resources, and a group's access to one Resource can carry a policy different from the Resource's [https://www.twingate.com/docs/policy-on-resource-migration, read 2026-11-27, relative date only]. The page does not say which of the two wins, so precedence is not stated. Groups are how users get access; access requires adding a Resource to a group [https://www.twingate.com/docs/quick-start, read 2026-11-27].

**Reference: Nebula (the base hopssh runs on).** The host firewall defaults to deny all for inbound and outbound. Deny rules cannot be written explicitly [https://nebula.defined.net/docs/config/firewall/, read 2026-11-27, last updated Nov 19 2026].

---

## 2. Defaults for a new network

### Summary table

| Product | Default for a new network | UDP allowed by default? | What the docs recommend next | Stated reasoning |
|---|---|---|---|---|
| Tailscale | New tailnets get an allow-all policy. Without an acls section, the default is allow-all. | Yes. ACLs without proto cover "all TCP and UDP traffic". Grant example uses ip "*". | Grants for new tailnets. Deny-by-default is the stated principle. | Allow-all example: "trusted environment, such as for small teams or personal tailnets". |
| NetBird | "Default" policy: All group to All group, all protocols, all ports | Yes (protocol ALL) | Keep while enrolling; create specific policies, test, then delete Default. Avoid "All" protocol rules. Prefer exact TCP or UDP ports. | "Useful only as a temporary onboarding aid." |
| Defined | New role allows ICMP only, all other inbound dropped | No | Edit role, add firewall rules by role or tag | None stated. Host firewall default drop in both directions per whitepaper. |
| ZeroTier | Flow rules disabled, all traffic allowed | Yes (no filter until rules are enabled) | Enable flow rules; templates (block-all, allow selected) | "The internet doesn't provide trust by default" (blog, reasoning toward restriction) |
| Headscale | No policy: allow all. Policy file without acls or grants: allow all. | Yes | Write a policy file | None stated. Issue #2672 shows users expect defaults to match Tailscale. |
| Netmaker | Network created with ALLOW (all machines reach all) or DENY (no connections). Fixed at creation. | Not stated | "Disable the defaults, then add your own policies." | "so the network is fully connected on first setup" |
| Twingate | Default Policy on new Resources. Nothing reachable until a group is granted. | Yes, once a group has access (TCP and UDP forwarded; ICMP toggle) | Assign groups | Zero Trust: "Twingate denies traffic by default" |

### Per product notes

**Tailscale.** The default tailnet policy "enables communication between all devices within the tailnet" [acls page, read 2026-11-27]. The allow-all grant example (src *, dst *, ip *) is "the most permissive default policy", and is "often used as a starting point" [grants example page, read 2026-11-27]. The Tailscale docs say both ACLs and grants "follow a deny-by-default principle" [grants-vs-acls page, read 2026-11-27]. A third-party field guide describes the starter file as allowing everything from everyone [https://tailscale.nixfred.com/learn/policy-acls-grants, search snippet, 2026-11-27; not an official source].

**NetBird.** Every new account gets a Default policy using the All group as source and destination, allowing "any protocol" between all peers [https://docs.netbird.io/manage/access-control, read 2026-11-27, Nov 9 2026]. The zero-trust guide says NetBird creates it "as a temporary onboarding aid", recommends replacing it with specific policies, and recommends creating and testing at least one specific allow rule before deletion, or all peers lose connectivity [https://docs.netbird.io/use-cases/security/implement-zero-trust, read 2026-11-27, Nov 12 2026]. The same guide warns that "All" protocol policies are broad and recommends exact TCP or UDP ports [same page, section 4.3].

Issue #3932 (opened June 5 2025, closed July 2 2025, referencing PR #3970) asked for a management-server option to stop the all-to-all policy being created, for automation. A maintainer agreed it "should be doable". The issue does not explain why the default exists [https://github.com/netbirdio/netbird/issues/3932, read 2026-11-27]. I did not read PR #3970.

**Defined.** The first-host setup creates a role per host that allows ping from all hosts. "By default, roles allow hosts to ping each other but block all other traffic" [https://www.defined.net/docs/get-started/quick-setup/, read 2026-11-27, Nov 18 2026]. The recommended next step is to edit the role or add a firewall rule, for example for SSH [same page]. The docs state no reasoning for the ICMP-only default.

**ZeroTier.** "By default, flow rules are disabled and all traffic is allowed across the network" [https://docs.zerotier.com/rules/, read 2026-11-27, Oct 6 2026]. The blog says "By default, all traffic is permitted within whatever existing traffic and rules allow" [https://www.zerotier.com/blog/introducing-flow-rules-a-policy-engine-update-in-zerotiers-new-central/, read 2026-11-27, date not visible]. The same post says "the internet doesn't provide trust by default", which supports restrictive policy but does not recommend deny-by-default outright.

**Headscale.** "By default, no policy is loaded which means that Headscale allows all traffic between nodes" [https://raw.githubusercontent.com/juanfont/headscale/main/docs/ref/policy.md, read 2026-11-27]. Issue #2672 (opened July 6 2025, closed August 18 2025 via PR #2734) reported that an empty policy `{}` applied deny-all, contrary to Tailscale. A maintainer added it to the v0.27.0 milestone. Current behaviour should be checked against the installed version [https://github.com/juanfont/headscale/issues/2672, read 2026-11-27].

**Netmaker.** "Every new network is created with default policies that allow unrestricted communication" [https://learn.netmaker.io/features/acls-access-controls/network-access-control, read 2026-11-27, no date shown]. The defaults are All Nodes, All Gateways and All Users. The docs advise: "Default policies grant open access. To restrict traffic, disable them, then add more specific policies" [same page]. A search snippet from the ACL guide describes an example: network with default ALLOW, then a node with default DENY, with the conclusion that all devices are reached through explicit rules [https://docs.netmaker.io/docs/features/acls, search snippet, 2026-11-27; the page section was not reachable by fetch]. Protocol filtering is not described in the pages read.

**Twingate.** Default Policy applies to new Resources. Access requires a group, and the create wizard offers the "Everyone" group only unless custom groups exist [https://www.twingate.com/docs/quick-start, read 2026-11-27]. Resources accept all TCP and UDP ports by default; ICMP "can be toggled to be enabled or blocked", with no default stated [https://www.twingate.com/docs/resources, read 2026-11-27]. "Twingate denies traffic by default, following Zero Trust principles" [same page].

---

## 3. Published reasoning, allow-all versus deny-by-default

- **Allow-all as onboarding or trusted-team convenience:** Tailscale's allow-all grant example names "small teams or personal tailnets" [https://tailscale.com/docs/reference/examples/grants, read 2026-11-27]. NetBird calls its Default policy a temporary onboarding aid [https://docs.netbird.io/use-cases/security/implement-zero-trust, read 2026-11-27]. Netmaker says defaults make the network "fully connected on first setup" [https://learn.netmaker.io/features/acls-access-controls/network-access-control, read 2026-11-27].
- **Deny-by-default as zero trust:** Tailscale: deny-by-default "prevents communication between devices without explicit access" [https://tailscale.com/docs/features/access-control/acls, read 2026-11-27]. NetBird: "nothing is reachable until a policy says so" [https://docs.netbird.io/manage/networks, read 2026-11-27]. Twingate: denies by default "following Zero Trust principles" [https://www.twingate.com/docs/resources, read 2026-11-27]. Defined's default is ICMP-only for the same reason.
- **Middle ground:** Defined allows ping only, so new hosts can be checked and nothing else opens [https://www.defined.net/docs/get-started/quick-setup/, read 2026-11-27].
- I found no published security analysis aimed specifically at small or self-hosted users in the pages read. The reasoning above is the complete set found in those official docs.

---

## 4. Points for hopssh (from evidence only)

- Two models for reuse exist. Defined reuses account-level roles and tags across separate networks, and each network keeps its own CA. Tailscale and ZeroTier keep policy per tailnet or per network, with no documented cross-network reuse. Netmaker documents cross-network rules only for egress resources.
- No product reviewed documents override or inheritance. Each either reuses definitions across scopes or applies one policy per scope. Conflict resolution is union of allows (Tailscale grants, Defined, NetBird), ordered rules with accept, drop and break (ZeroTier), or allow-only rules with no deny (Tailscale, Defined, NetBird).
- Defaults split two ways. Tailscale, NetBird, Headscale, Netmaker (ALLOW) and ZeroTier (rules off) start open and tell the admin to restrict afterwards. Defined (ICMP only) and Twingate (deny until a group is granted) start closed. NetBird documents its open Default policy as temporary.
- UDP is allowed by default in Tailscale (ACLs without proto cover TCP and UDP; allow-all grant covers all protocols), NetBird (protocol ALL), ZeroTier (flow rules off), Headscale (allow all), Netmaker (network ALLOW), and Twingate (once a group is granted). It is not allowed by default in Defined (ICMP only).

---

## 5. Gaps and caveats

- Netmaker ACL protocol handling and the full ACL guide were not read; the ACL example comes from a search snippet.
- ZeroTier advanced-rules blog date is not visible. Templated-rules tier wording conflicts within the rules page.
- Twingate pages show relative dates only ("14 days ago"). Their dates are not stated here.
- NetBird does not state whether policies or groups are account-scoped in so many words. The groups page states "Each group is unique" and that peers can belong to several groups.
- Tailscale multiple tailnets are alpha and sales-only; behaviour may change.
- Headscale's empty-policy behaviour changed between versions (see issue #2672). Check the target version.
- Not reviewed in full: Headscale's ACL samples page, Netmaker's full ACL section, Defined's "Blocking hosts" guide, and Tailscale's policy-file syntax beyond the protocol-default text.

---

## Source list (all read 2026-11-27)

1. https://tailscale.com/docs/features/access-control/acls [last validated Oct 29 2026]
2. https://tailscale.com/docs/reference/examples/grants [no date]
3. https://tailscale.com/docs/features/multiple-tailnets [last validated Feb 4 2026]
4. https://tailscale.com/blog/multiple-tailnets-alpha [search snippet]
5. https://tailscale.com/docs/reference/grants-vs-acls [last validated Aug 1 2025]
6. https://tailscale.com/docs/features/access-control/grants/grants-explained [last validated Nov 4 2026]
7. https://tailscale.com/docs/reference/syntax/policy-file [no date]
8. https://tailscale.nixfred.com/learn/policy-acls-grants [third party, search snippet]
9. https://docs.netbird.io/use-cases/security/implement-zero-trust [updated Nov 12 2026]
10. https://docs.netbird.io/manage/access-control [updated Nov 9 2026]
11. https://docs.netbird.io/manage/access-control/manage-network-access [updated Nov 12 2026]
12. https://docs.netbird.io/manage/networks [no date]
13. https://docs.netbird.io/manage/settings/networks [updated Aug 19 2026]
14. https://docs.netbird.io/manage/network-routes/access-control [search snippet]
15. https://github.com/netbirdio/netbird/issues/3932 [opened Jun 5 2025, closed Jul 2 2025]
16. https://www.defined.net/blog/multiple-networks/ [posted Dec 10 2024]
17. https://www.defined.net/docs/architecture/ [updated Nov 9 2026]
18. https://www.defined.net/docs/get-started/quick-setup/ [updated Nov 18 2026]
19. https://www.defined.net/docs/guides/creating-firewalls-using-roles/ [no date]
20. https://www.defined.net/docs/glossary/firewall-rules/ [no date]
21. https://www.defined.net/docs/get-started/mobile-nebula/ [updated Nov 6 2026]
22. https://www.defined.net/docs/guides/blocking-and-unblocking-hosts/ [search snippet]
23. https://www.defined.net/security/whitepaper/ [search snippet]
24. https://www.defined.net/docs/get-started/managed-lighthouses/ [search snippet]
25. https://nebula.defined.net/docs/config/firewall/ [updated Nov 19 2026]
26. https://docs.zerotier.com/rules/ [dated Oct 6 2026]
27. https://docs.zerotier.com/networks/ [dated Oct 6 2026]
28. https://docs.zerotier.com/microsegmentation/ [no date]
29. https://www.zerotier.com/blog/introducing-flow-rules-a-policy-engine-update-in-zerotiers-new-central/ [date not visible]
30. https://www.zerotier.com/blog/advanced-access-custom-flow-rules-arrive-in-zerotier-one/ [date not visible]
31. https://docs.zerotier.com/new-central/ [no date]
32. https://raw.githubusercontent.com/juanfont/headscale/main/docs/ref/policy.md [no date; fetched via raw URL after github.com returned 503]
33. https://github.com/juanfont/headscale/issues/2672 [opened Jul 6 2025, closed Aug 18 2025]
34. https://headplane.net/features/acls [no date]
35. https://learn.netmaker.io/features/acls-access-controls/network-access-control [no date]
36. https://learn.netmaker.io/features/acls-access-controls [no date]
37. https://docs.netmaker.io/docs/netmaker-network-setup-concepts/1-create-networks [no date]
38. https://docs.netmaker.io/docs/netmaker-network-setup-concepts/3-configure-traffic-flow [search snippet]
39. https://docs.netmaker.io/docs/features/acls [search snippet]
40. https://www.twingate.com/docs/resource-policies [relative date only]
41. https://www.twingate.com/docs/policy-on-resource-migration [relative date only]
42. https://www.twingate.com/docs/quick-start [relative date only]
43. https://www.twingate.com/docs/resources [relative date only]
