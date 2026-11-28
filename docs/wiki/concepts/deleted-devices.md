---
type: concept
title: Deleted devices are refused (pki.blocklist)
status: built, not released
last_compiled: 2026-11-28
sources:
  - internal/blocklist/blocklist.go
  - internal/db/node_certs.go
  - internal/db/migrations/013_node_certs.sql
  - internal/api/renew.go
  - internal/mesh/network_manager.go
  - internal/client/blocklist.go
  - internal/client/nebula.go
  - internal/client/firewall_apply.go
  - internal/client/lifecycle.go
  - raw/blocklist/
---

# Deleted devices are refused (pki.blocklist)

A device deleted in the dashboard keeps a certificate that is valid for up
to 24 hours, plus the hour of clock-skew tolerance of patch `cert-skew`.
Until 2026-11-28 every other device and the server's lighthouse accepted
it for that whole time, because nothing told them. A stolen laptop that
was deleted could reach every device of its network until the next day.

Now the server knows every certificate it issued, the heartbeat answer
carries the certificates of the network's deleted devices, and Nebula is
given them as `pki.blocklist`. A deleted device is cut off from the
lighthouse at once and from every device within one heartbeat. Design and
what was decided before the code: [raw/blocklist/design-2026-11-28.md](../raw/blocklist/design-2026-11-28.md).

## How it works

**The server records every certificate it issues.** Table `node_certs`
(migration 013): fingerprint, network, node (no reference to the node, so
the row outlives it), `not_after`. It is written in the transaction that
writes the node's cert (`Create`, `CompleteEnrollment`, `UpdateCert`), so
no path that issues a cert can forget it. At start the server records the
cert each node holds now (`RecordHeldCerts`); older certs of a node from
before migration 013 are not known.

**A network's blocklist** is the rows whose node no longer exists and whose
cert has not expired (`not_after` plus the skew tolerance): `NodeStore.Blocklist`.
It is derived from the node row being gone, so every path that deletes a
node is covered (the API, the prune, a DELETE by hand). Newest first, cut at
`blocklist.MaxEntries` (4096) with a log line.

**A node holds at most 16 unexpired certs** (`MaxHeldCerts`). A renewal over
that is answered 429 with `Retry-After: 3600`. Found in review: without the
bound, a device that renews in a loop before it is deleted could push its
own certs off the 4096 entries.

**The lighthouse** reloads its config in place with the list when a node is
deleted or pruned through the API, every 30 s (`StartBlocklistRefresher`:
entries that expired, and deletes the API did not see, such as the server's
hourly prune when `HOPSSH_NODE_PRUNE_AGE_HOURS` is set), and at every start.
An open tunnel to it ends 1 to 1.5 s after the reload, so a deleted device
learns no addresses and gets no relay. A reload that has not returned
blocks the next one, and a closed network is never reloaded. A delete and
the API's prune refresh the network's DNS names too, so a deleted device's
name stops resolving at once (the hourly prune does not: open question).

**The heartbeat answer** has `blocklist: [{fingerprint, until}]`, present
(maybe empty) whenever the server could read it. `until` is the Unix time
after which Nebula refuses the cert as expired anyway.

**The device** (`internal/client/blocklist.go`):

- decodes the list apart from the rest of the answer, so a list it refuses
  changes nothing else (the firewall policy in the same answer still
  applies); the other way round it is not so: an answer that does not
  decode, for a wrong type anywhere in it, is dropped whole, the list too;
- checks it (`blocklist.Check`: at most 4096 entries, each 64 lowercase hex
  and a time) and refuses a bad one whole;
- keeps it in `<enrollment>/blocklist.json`, written only when it changed;
- gives Nebula the entries that are still active and never its own cert
  (`blocklist.Active`), so a device never refuses itself. That is all it
  protects: a mistake on the server that lists a device's cert still has
  every other device refuse it;
- applies a changed list by an in-place reload of the text Nebula loaded,
  with only `pki.blocklist` changed (the machinery of the firewall rules,
  [[firewall-rules]]): under the start lock, which it only tries to take
  (a start, a renewal, a firewall apply or the "Block incoming" switch
  under way means the next beat does it), on the Nebula that runs once it
  holds the lock, and not when `nebula.yaml` changed since Nebula loaded it;
- compares with what Nebula's CA pool refuses (`Control.CABlocklist`, patch
  `control-api`), not with the text it loaded. A reload that can't read the
  CA keeps the old pool and only logs, and Nebula's config then has a list
  that is not in force; the device says so ("waits for the next
  heartbeat") and the next beat reloads again, which loads the CA again;
- puts the saved list in at every start (`loadNebulaConfig`), and takes the
  one in the start's own heartbeat (`warmPeersFromHeartbeat`) before the
  warm-up dials anyone. A renewal's restart has no warm-up heartbeat, so
  after it (and after a retry that starts Nebula) the device gives the new
  Nebula the list saved at that moment: a heartbeat during the restart
  saves its list and leaves the Nebula alone.

One in-place reload runs at a time (`reloadInPlace`, for the firewall, the
switch and the list alike). One that does not return within 5 s goes on
alone, and its text is recorded when it does; until then the next is
refused, and after it one built from the text before it is refused too,
since it would undo it. Without that, a heartbeat that read the config
before the user turned "Block incoming" on could turn it back off. A
switch refused for this is saved, and the next heartbeat gives it to
Nebula ([[firewall-rules]]).

`nebula.yaml` never has `pki.blocklist`: the agent owns it, like the
firewall of a device that blocks incoming. So the comparison of the loaded
text with the file ignores both (`sameApartFromOverlays`); without that,
every firewall apply after the first list would wait for ever. An absent or
`null` list keeps what the device has (an older server, or one that could
not read its database). A bad `blocklist.json` never keeps a network from
starting: it starts without a list, and the next heartbeat brings it.

## Measured

- Two real Nebula peers (`internal/client`'s `TestE2E_Blocklist`): the open
  tunnel ends 5 to 8 s after the reload with "Remote certificate is
  blocked, tearing down the tunnel" (the connection manager's next tick);
  new handshakes are refused both ways. An unrelated entry leaves the
  tunnel alone. Once the entry is removed or expired, the peer is back
  within about 3 s.
- Real server, real agent, a device of the network that is in the mesh and
  talks to the agent (`cmd/agent`'s `TestE2E_DeletedPeer`): cut off 67 s
  after the deletion, which is the next heartbeat (60 s ± 10 %) plus the
  teardown. It stays out, and the control plane still reaches the agent.
- The lighthouse (`internal/mesh`'s `TestLighthouseRefusesDeletedNodes`):
  the deleted device's tunnel to it ends 1 to 1.5 s after the refresh.

## What it does not cover

- Agents of v0.11.31 and older ignore the field and accept the device until
  its cert expires. Only the lighthouse refuses it for them.
- `hop client join` devices have no heartbeat and get no list.
- A device that was down while the node was deleted, and has nothing saved,
  accepts it from Nebula's start until the heartbeat of its own start
  (before the warm-up, so a few hundred milliseconds). When that heartbeat
  fails, until the next one that gets through.
- Deleting a device does not stop anyone who may enroll devices in the
  network (every member: `CanEnrollNode`) from enrolling it again with a
  new token. The block is of certificates, not of people.
- Certs a node held before migration 013 other than its current one (a
  renewal in the 25 hours before the upgrade) are not known.
- A server whose clock runs ahead drops entries early.
- A stolen cert of a device that is not deleted is not revoked; that needs
  a "revoke this certificate" action, a product decision of its own.

## Tests

- `internal/client`: `TestLoadBlocklist`, `TestLoadNebulaConfig_Blocklist`,
  `TestSameApartFromOverlays`, `TestTakeBlocklist_Saves`,
  `TestReloadInPlace_OneAtATime` (5 s), and `TestE2E_Blocklist` (two real
  peers: an unrelated entry, the CA unreadable during a reload, the block,
  its own cert, a firewall policy while blocked, a null list, three bad
  lists with a policy beside them, a restart from the saved list, a
  renewal's restart and a retried one with a list saved meanwhile, a start
  with nothing saved, an expired entry, an empty list). About 100 s.
- The vendored `cert` package: `TestCAPoolBlocklist`.
- `internal/api`'s `TestE2E_Blocklist` and `TestE2E_DNS` (real server),
  `internal/db`'s node-cert tests, `internal/mesh`'s
  `TestLighthouseRefusesDeletedNodes`.
- `cmd/agent`'s `TestE2E_DeletedPeer`, a known gap until 2026-11-28: the
  device starts before the deletion and reaches the agent, so only the
  agent can cut it off; the lighthouse plays no part in a tunnel that is
  open.
- Planted bugs: [[e2e-tests]], "Deleted devices".
