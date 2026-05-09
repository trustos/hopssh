//go:build !cgo || linux

package client

// This stub catches:
//   - Pure non-CGO cross-compile (CGO_ENABLED=0)
//   - All Linux builds (clipboard.go is darwin/windows only — see
//     its build tag for why). Linux server deployments never have
//     a clipboard to sync; Linux desktop will get a real watcher
//     in a future phase that ships X11/Wayland support together
//     with the CI changes to install the system dev headers.

// Stub for builds without CGO (cross-compile, headless static
// servers). Clipboard sync is no-op on these targets — the OS
// clipboard libraries we'd otherwise bind to all need CGO.

import (
	"context"
	"log"
)

type clipboardSync struct{}

func startClipboardSync(_ context.Context, inst *meshInstance, _, _, _ string) *clipboardSync {
	log.Printf("[clipboard %s] disabled (no-cgo build)", inst.name())
	return nil
}
