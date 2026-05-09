# clients/mobile-go — gomobile bindings (Phase NN+1)

This module wraps `internal/client.Client` in a gomobile-compatible API
surface and produces `.aar` (Android) + `.xcframework` (iOS) artifacts
on demand. **The artifacts are NOT shipped today** — they're a
build-tripwire that proves the FFI surface introduced in Phase NN
(2026-05-09) is genuinely consumable by gomobile.

## Architecture

```
clients/mobile-go/
  mobilehop/
    mobilehop.go       — Session wrapper around *client.Client
                         with mobile-friendly types (string returns
                         instead of slice-of-struct, JSON for snapshots,
                         int64 subscription handles, EventListener
                         interface for callbacks).
  Makefile             — build targets (android-aar, ios-xcframework,
                         all, verify, clean).
  README.md            — this file.
```

The wrapper is a **sub-package of the main module** (no separate
`go.mod`). This lets it inherit the main module's vendored dependencies
including the patched `slackhq/nebula` — which gomobile can resolve via
`-mod=vendor` semantics when invoked against a sub-package.

## Why a wrapper at all?

`internal/client.Client` already satisfies the FFI constraint rules
(no `chan` / `func` / `interface{}` in exported struct fields,
regression-tested via `internal/client/api_constraints_test.go`). But
some method shapes are gomobile-friendlier than others:

| Original `*client.Client` method | mobilehop wrapper | Why reshaped |
|---|---|---|
| `Subscribe() <-chan Event` | hidden | gomobile can't bind channels |
| `SubscribeCallback(EventCallback) SubscriptionID` | `SubscribeEvents(EventListener) int64` | int64 handles bind cleaner than struct values across gomobile-Java/Swift |
| `Status() Snapshot` (struct with slice) | `StatusJSON() (string, error)` | mobile decodes via Foundation/org.json, sidestepping gomobile's slice-of-struct quirks |
| `Peers(name) ([]PeerInfo, error)` | `PeersJSON(name) (string, error)` | same |
| `EnrollmentNames() []string` | `EnrollmentNamesCSV() string` | gomobile binds simple strings cleaner than `[]string` across all targets |

`Connect`/`Disconnect`/`Enroll`/`Leave`/`ForceRenew` map 1:1 to the
underlying `*client.Client` — they already use gomobile-clean types.

The wrapper also collapses `Config` to scalar args: mobile callers pass
`configDir` + `userAgent` + `heartbeatSeconds` directly to `NewSession`
rather than constructing a struct (gomobile rejects `Config.LogWriter
io.Writer`, the one stdlib-interface field). Logging in mobile builds
goes to `os.Stderr` by default, captured by the platform's logging
infrastructure (logcat / NSLog).

## Prerequisites

- Go 1.25.8+ (matches the main module).
- `gomobile`:
  ```bash
  go install golang.org/x/mobile/cmd/gomobile@latest
  gomobile init                         # downloads shim sources
  ```
- For **Android**: Android SDK level 21+ via Android Studio or the
  `cmdline-tools` package; export `ANDROID_HOME`.
- For **iOS**: full **Xcode** with iOS SDK installed (Command Line Tools
  alone is not sufficient).

## Build

```bash
cd clients/mobile-go
make verify           # compile-time check; doesn't need platform SDKs
make android-aar      # → dist/mobilehop.aar
make ios-xcframework  # → dist/MobileHop.xcframework
make all              # both
make clean
```

`make verify` is the always-runnable check; the platform-targeted
builds need their respective SDKs.

## Mobile API surface (after binding)

### Java / Kotlin (Android)

```kotlin
import com.hopssh.mobilehop.Mobilehop
import com.hopssh.mobilehop.Session
import com.hopssh.mobilehop.EventListener

val session = Mobilehop.newSession(
    /* configDir = */ context.filesDir.absolutePath + "/hopssh",
    /* userAgent = */ "hopssh-android/1.0",
    /* heartbeatSeconds = */ 60L,
)
session.start()
session.enroll("https://hopssh.com", "home", "<token-from-dashboard>")

val handle = session.subscribeEvents(object : EventListener {
    override fun onEvent(at: Long, type: String, enrollment: String, severity: String, message: String) {
        Log.i("hopssh", "[$severity] $type ($enrollment): $message")
    }
})

// later
session.unsubscribe(handle)
session.stop()
```

### Swift (iOS)

```swift
import MobileHop

let session = HopsshNewSession(
    /* configDir: */ FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0].path + "/hopssh",
    /* userAgent: */ "hopssh-ios/1.0",
    /* heartbeatSeconds: */ 60,
    &error
)
session?.start()
try session?.enroll("https://hopssh.com", name: "home", token: "<token-from-dashboard>")

class Listener: NSObject, HopsshEventListener {
    func onEvent(_ at: Int64, type: String?, enrollment: String?, severity: String?, message: String?) {
        print("[\(severity ?? "")] \(type ?? "") (\(enrollment ?? "")): \(message ?? "")")
    }
}
let handle = session?.subscribeEvents(Listener())

// later
session?.unsubscribe(handle ?? 0)
session?.stop()
```

## What this does NOT do

This phase (NN+1) builds the **Go-side** binding only. The native VPN
extension scaffolding lives in:

- **NN+2** — iOS NEPacketTunnelProvider Swift target inside
  `clients/desktop/src-tauri/` (gated on Apple Developer Program +
  Network Extension entitlement).
- **NN+3** — Android VpnService Kotlin scaffolding inside
  `clients/desktop/src-tauri/gen/android/` (gated on Google Play
  Console signup).

Both ADRs at `docs/wiki/decisions/client-{ios,android}-architecture.md`
describe the full mobile architecture; this module is the shared
substrate the platform-specific scaffolds will link against.

## CI

The GitHub Actions `release` workflow (and a new gomobile-bind tripwire
job) runs `make all` on every push. Failures here mean either:

1. The wrapper's API surface drifted away from gomobile-clean shapes
   (someone added a `chan` / `func` / `interface{}` field to an
   exported type in `internal/client/api.go`), OR
2. `internal/client/` calls a vendored Nebula method that doesn't exist
   upstream (we patch some — they need to be present in the build
   path; sub-package status ensures vendor mode works).

Both classes of failure should land in PR review, not in a release.

## Backlinks

- `docs/wiki/phases/phase-nn.md` — Phase NN substrate extraction (parent of NN+1).
- `docs/wiki/decisions/client-ios-architecture.md` — iOS ADR (status: substrate-built).
- `docs/wiki/decisions/client-android-architecture.md` — Android ADR (status: substrate-built).
- `docs/wiki/concepts/client-strategy.md` — overall 5-platform delivery strategy.
