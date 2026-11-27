# Vendor patches: Nebula and gVisor

Applied on top of `slackhq/nebula` **v1.11.2**, and of `gvisor.dev/gvisor` **v0.0.0-20240423190808-9d7a357edefe** (the TCP/IP stack of Nebula's userspace service, at the version Nebula brings), by `make patch-vendor` (or `make vendor`, which vendors first).

- Each patch is one theme, and they apply in any order: one can be dropped without touching the others. A file may be in two patches when their hunks and the lines around them are apart. `make patch-check` shows it: against a pristine tree from the module cache (offline), each patch applies alone, all apply in order and in reverse order with no fuzz and give the same files, and `vendor/` is what they make. It fails on a patch that needs another, and on a hand edit in `vendor/`.
- `make patch-vendor` stops at a patch that does not apply. Until November 2026 it carried on in silence, and one patch (the old 12) was in no build for months.
- `go mod vendor` removes them all. Always run `make patch-vendor` after it.
- `vendor/` is gitignored, except `nebula/control.go` and `nebula/overlay/tun_darwin.go`, which are tracked in their patched form.

## Inventory

"Was" is the number of the patch against v1.10.3 (see `git log` before the upgrade for those files). A patch written since has none; its number in the docs of the day is in brackets.

| File | What | Was |
|---|---|---|
| `cert-skew.patch` | `cert.ClockSkewTolerance` (1 h): a cert slightly outside its validity is accepted. Upstream `Expired()` has no tolerance. | 25 |
| `control-api.patch` | What the agent calls on `*nebula.Control`: advertise addresses from port mapping (`AddAdvertiseAddr`, `RemoveAdvertiseAddr`), peer endpoints from the control plane (`AddStaticHostMap`, `ReplaceStaticHostMap`), `SetInboundObserver`, `ProbeEndpoint`, `SendStuckFor`, and the counters `ReceivedDirect` / `ReceivedRelayed` in `ControlHostInfo`. | 11, 20, 21, 22, 34 |
| `darwin-udp.patch` | macOS UDP: `sendmsg_x` / `recvmsg_x` batches, control packets ahead of data, a reader and a worker for inbound, QoS class of the threads, a write outside a batch is sent at once, one refused destination does not drop the others, a send waits at most 1 s in the kernel, `SendStuckFor` says since when every send has run into that limit. Adds `udp.Flush(conn)`, which does nothing for a conn that has no batches. With its tests. | 04, 05, 08, 09, 10, 17, 18, 19, 24, 28 |
| `darwin-tun.patch` | macOS TUN: `ReadBatch` with `recvmsg_x` on the utun descriptor; the descriptor is close-on-exec. | 06, 32 |
| `read-loop.patch` | `listenIn` reads the TUN in batches and flushes the UDP queue after each; packet threads are locked and get the QoS hint; the inbound observer's slot. | 07, 19, 22 |
| `paths.patch` | Which address a peer is sent to: roaming back to an address is suppressed for 10 s instead of 2 (14), the LAN is preferred over a public address (15), a stray RecvError does not close a relayed tunnel (33), direct and relayed packets are counted (34), nobody moves to another path before an answer came back on it (35, `path_question.go`), a new tunnel stays the one in use (36). With tests. | 14, 15, 16, 33, 34, 35, 36 |
| `relays.patch` | The relay list is read under its lock (a data race); the lighthouse logs relay requests it drops. With a test. | 26, 38 |
| `service-netstack.patch` | Closing a userspace `Service` closes its netstack. Without it every close leaks a goroutine per CPU and over 1 MB. | 29 |
| `linux-udp-close.patch` | `Close` waits until the reader has left the kernel. Upstream v1.11.2 wakes it and returns, and the port stays taken for a moment. Upstream waits from v1.12.0. | 31 (UDP half) |
| `linux-tun.patch` | The same for the TUN device (`tunWaiters`); `/dev/net/tun` is opened close-on-exec, so programs started from the web terminal don't inherit it. | 31 (TUN half), 32 |
| `gvisor-time-wait.patch` | gVisor TCP: a SYN (not a SYN+ACK) that may reopen a connection in TIME_WAIT goes to the stack's forwarder when no socket listens (`Stack.TransportProtocolHandler`). The forwarder accepts it, or Nebula's service answers with RST where nothing listens on that port any more. Upstream reopens only for a listening socket and otherwise drops the SYN and every retry until the TIME_WAIT ends, 60 s later; Nebula's userspace service accepts only through a forwarder. A new connection that met one of the node's TIME_WAITs hung, one in a few hundred. Left as upstream: a second copy of that SYN already queued behind it (the stack about a second behind) gets an RST, as every SYN queued at a closing endpoint does when no listening socket is there. | new (39) |

## Dropped in the upgrade to v1.11.2

| Was | Why |
|---|---|
| 01, 27 | Upstream no longer calls `os.Exit` from a read loop: `Control.Stop()` has states and the loops end on a closed interface (PR #1661). `TestReapIdleInstances` passes 40 of 40 without them. |
| 02 | Upstream fixed the test-reply panic. |
| 03 | Upstream's macOS TUN reads without the per-packet allocation. |
| 12, 13 | The pipelined `listenIn`. It never applied, so it was in no build, and the batch read of patch 07 is what was measured. |
| 30 | The global it protected is gone. |
| 37 | `HostInfo.GetRemote()` / `SetRemote()` are atomic upstream. |

## What to run after touching them

```bash
make vendor && make patch-check && go build -mod=vendor ./... && go vet -mod=vendor ./...
```

```bash
go test -mod=vendor -race github.com/slackhq/nebula github.com/slackhq/nebula/udp ./internal/client ./internal/mesh
```

- **Linux** (the close patches run only there): the close tests and the kernel-TUN test as root, see `docs/wiki/concepts/e2e-tests.md`. On a Mac they pass whatever the Linux code does.
- **Paths** (`paths.patch`, `control-api.patch`): `make netlab`, see `docs/wiki/concepts/netlab.md`.
- **gVisor** (`gvisor-time-wait.patch`): `TestTimeWaitReopen` (`./internal/client`, two bare gVisor stacks, the same port twice) is in the race run above and fails every time without the patch. Then `go test -mod=vendor -run TestE2E_ConnectAgainAfterTimeWait -count 3 ./internal/client` and `go test -mod=vendor -run TestE2E_ProxyBackToBack -count 3 ./internal/api`, through real Nebula, without `-race`: under `-race` a connection through Nebula takes about 0.5 s, and both skip themselves there.
- **macOS speed** (`darwin-*.patch`, `read-loop.patch`): `scripts/perf_compare.sh` on two Macs against the last baseline in `results/`.
- **Upstream's own end-to-end tests** on the patched tree (in a checkout of Nebula with the patches applied): `go test -tags e2e_testing ./e2e`, without `-race`. With `-race` they fail on untouched v1.11.2 too. `TestUncleanShutdownRaceLoser` does not end with `paths.patch`: it waits for a dead tunnel to go away, and the extra packet of "ask before moving" has removed it before the test starts to wait. Skip it with `-skip`.

## Next upgrade

Upstream is at v1.12.0 (needs Go 1.27.3). There `linux-udp-close.patch` can go; check `linux-tun.patch` against upstream's close with the kernel-TUN test before dropping its waiting half.

gVisor comes with Nebula, so it changes when Nebula does. On 2026-11-27 `handleTimeWaitSegments` was the same on gVisor's `go` branch, and Nebula's master still accepted through a forwarder, so `gvisor-time-wait.patch` stays: drop it only once its three tests pass without it.
