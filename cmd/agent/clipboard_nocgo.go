//go:build !cgo

package main

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
