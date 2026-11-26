# What the others do: expiry, long-offline devices, start at boot, network change

Collected 2026-11-26 from the vendors' own pages. "Unconfirmed" means not found in a primary source.

## Credential lifetime

| Product | Default | Can it be switched off | Renewal |
|---|---|---|---|
| Tailscale (node key) | 180 days ("By default, new domains are set with an expiry period of 180 days", tailscale.com/kb/1028/key-expiry) | Yes, per device; tagged devices have it off by default | Expired: connections to and from the device stop. `tailscale up --force-reauth` renews. Needs a person |
| NetBird (peer login) | 24 h for new accounts (docs.netbird.io/manage/settings/enforce-periodic-user-authentication) | Yes, per account or per peer | Only for peers added by SSO login; setup-key peers are exempt. Expired peer shows "Login required" |
| Defined Networking (host cert) | Not stated | Unconfirmed | dnclient "rotates certificates before they expire" (defined.net/docs/glossary/nebula-certificate). Offline past expiry: unconfirmed |
| ZeroTier | Membership credential with a moving time window, no re-authorisation after time offline found (docs.zerotier.com/protocol) | - | - |
| Plain Nebula | Operator's choice at signing | - | None. `pki.disconnect_invalid` (default true) drops a peer whose cert expired |

## Removal of quiet devices

- Tailscale: only ephemeral nodes, 30 to 60 minutes after the last activity (tailscale.com/kb/1111/ephemeral-nodes).
- Defined Networking: only ephemeral hosts, within 1 to 2 hours.
- ZeroTier, NetBird: nothing found that removes a member for being quiet.

## Start at boot

- Tailscale on macOS: the standalone app does not run before login; the open-source `tailscaled` does (tailscale.com/kb/1065/macos-variants).
- Defined Networking: `dnclient` is a service. Before login: unconfirmed.
- NetBird, ZeroTier: unconfirmed in what was read.

## Network change

- Tailscale: a network monitor notices the move; connections start on a relay (DERP) and are upgraded to direct (tailscale.com/docs/reference/connection-types).
- NetBird: on a change of the underlying network the direct path is marked dead and the relay connection is renewed; a guard retries with growing waits and never gives up (client/internal/peer/conn.go, guard/guard.go).
- ZeroTier, Defined Networking: unconfirmed.

## Pattern

1. Where the identity is a person's login, it expires on purpose and a person has to log in again. Server-style devices (tags, setup keys) are exempt.
2. Nobody removes an ordinary device for being quiet. hopssh did, from v0.10.51 to v0.11.26.
3. hopssh: the cert lasts 24 h, the agent's token does not expire, so a machine that was off for months renews by itself at start. That is the server-style case for every device; there is no "log in again after N days" for laptops.
