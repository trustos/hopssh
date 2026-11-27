---
type: decision
title: Firewall rules — a policy per network plus rule sets the owner shares
status: accepted (2026-11-27); P1 (stored, edited, audited) built, P2 (agents apply them) next
last_compiled: 2026-11-27
sources:
  - internal/authz/authz.go
  - internal/db/networks.go
  - internal/client/enroll.go
  - ../concepts/firewall-rules.md
  - ../raw/firewall-rules/sharing-and-defaults.md
---

# Firewall rules: a policy per network, and rule sets the owner shares

The user approved the model on 2026-11-27 ("rule sets the owner owns, attached to networks; the union of what they allow; presets first, tags later") and asked for five points to be settled before it is built. This page settles them. The design of delivery and reload is in [[../concepts/firewall-rules]].

## What a network allows

The **union** of:

1. what hopssh always keeps: TCP 41820 from `admin` and from `node`, ICMP;
2. the network's **preset**: `all` (every protocol from every node), `tcp` (TCP on every port from every node, the block of every network created before migration 009) or `ping` (nothing more than 1);
3. the network's **own rules**;
4. the rules of every **rule set** attached to the network.

A rule only allows: `{proto, port, sourceNodeId, targetNodeId, description}`. `proto` is `tcp`, `udp`, `icmp` or `any`; `port` is `any`, `N` or `N-M` (ICMP: `any` only). An empty `sourceNodeId` is any node of the network; an empty `targetNodeId` is every agent. There are no deny rules, so the order of rules never matters (as in NetBird, Defined and Tailscale's grants).

## The five points

1. **Who may edit what.**
   - The network's preset and own rules: its owner and its admin members (`CheckAccess(...).CanAdmin()`). Plain members read them.
   - A rule set: **only the user who owns it.** An edit changes every network the set is attached to at once, and only its owner has a say over all of them.
   - Attaching and detaching: **only the network's owner, and only their own sets.** So every network a set reaches belongs to the set's owner. An admin member of one network cannot change another network through a set, and cannot see the owner's sets that are not attached to their network.
2. **Rules name nodes only by node ID**, within one network. Never by hostname (not unique) or by address (given out again after 48 h when a network is full, see migration 008). Node IDs are UUIDs and are never given out again.
3. **A reference to a node that is not in the network is refused** (400, naming the IDs):
   - on a change of the network's own rules;
   - when a set is attached, for that network;
   - when an attached set is changed, for every network it is attached to.
   Only IDs that the stored rules do not name yet are checked. A node deleted afterwards leaves a reference that matches nothing (node IDs are never given out again), and the rules around it can still be changed without removing it first; the dashboard shows it as a deleted device.
4. **Detaching a set is a removal.** Once agents apply rules (P2), an open flow that no remaining rule allows stops at its next packet, as for a removed rule (spike, [[../concepts/firewall-rules]]). A set that is attached anywhere cannot be deleted (409): detach it first, so that a deletion never changes networks the user did not look at.
5. **The device's switch wins.** "Block incoming" (P3) is applied on the device after the union and keeps only point 1, so the owner can still manage the device. Nothing the server sends can lift it.

## Versions

The network's policy and each set have a version that starts at 1 with the first change; a network without a stored policy is version 0. Attaching a set to a network without one stores an empty one at version 0: from then on the network is managed (`managed` in the answer), and phase 2 sends its rules to its devices; a network that is not keeps each device's enrollment block ([[../concepts/firewall-rules]], "Delivery and apply"). `GET` answers with `ETag: "<version>"`; a change needs `If-Match: "<version>"`: 428 without it, 412 when it is stale (RFC 9110 §13.1.1 requires 412 for a false `If-Match`; first built as 409, changed after the review because the tab must tell a stale version from a rule-set name the user already has, which is 409). Attaching or detaching changes neither version; the effective policy that agents get (P2) carries its own fingerprint.

A change is read strictly: a field the API does not know is refused (400), so a misspelt `source` cannot turn into "from any node". Field names match without regard to case, as Go's JSON decoder does, so `sourceNodeID` is `sourceNodeId`. At most 100 rules per policy or set, 200 characters of description, 64 of set name, and 4096 ports covered by the rules that reach a device, its network's and its attached sets' together (`fwpolicy.MaxPorts`: Nebula keeps an entry of about 800 bytes per port of a range, per rule). `TestE2E_Firewall` (internal/api, the real server) pins all of this.

## Audit

`firewall.update` (network; from and to version, from and to preset, number of rules), `firewall_set.create` (set, name, rules), `firewall_set.update` (set, name, from and to version, rules, networks it is attached to), `firewall_set.delete` (set, name; no network), `firewall_set.attach` (network; set, name, version) and `firewall_set.detach` (network; set, name). An attach of a set already attached, or a detach of one that was not, writes nothing. `TestE2E_Audit` fails for a route that is in none of its three lists (`auditedRoutes`, `notAudited`, `readOnly`).

## What P1 does and does not do

- Stores and checks all of the above, with the API, the audit entries and a Firewall tab.
- A new enrollment gets the network's preset, as since migration 009; the agent learns `ping` (TCP 41820 from `admin` and from `node`, ICMP: the keepalive of other agents dials 41820, and without it their stuck-data-plane watchdog would trip). Agents of v0.11.31 and before ignore the preset and always write `tcp`; a later one that does not know a preset writes `tcp` with a warning. Both are wider than `ping` asks: the tab must not claim the preset is enforced (P2 reports what each agent runs).
- Own rules and sets reach no agent before P2 (the heartbeat field and the in-place reload). Until then the tab says so.

## Not chosen

- **One policy per account, no per-network rules** (roadmap #8 read literally). Admin members could then change nothing, and a node ID only means something inside one network.
- **Inheritance with override** (a network narrowing a shared set). None of the products read documents it ([[../raw/firewall-rules/sharing-and-defaults]]); with allow-only rules it would need deny rules, which Nebula cannot express (`nebula.defined.net/docs/config/firewall`: no explicit deny).
- **Sets editable by admins of any network they are attached to.** An admin of network A would change network B.
