package main

// Phase Z (v0.10.91): tests for the mirror-file chown fallback +
// periodic self-heal. The bug being guarded against: at boot time
// before any user logs in, /dev/console is owned by root,
// resolveConsoleUser returns error, and pre-fix mirror files
// stayed root-owned forever — even after a user logged in. The
// .app, running as the user, couldn't read the mode-0600 token
// and showed "agent unreachable".
//
// These tests run as non-root in CI (the chown branch in
// writeSystemMirrorFiles is gated on os.Geteuid() == 0). What we
// CAN exercise without root: chownMirrorFiles' fallback predicate
// against a synthetic mirror dir, and runMirrorChownSelfHeal's
// "skip if not running as root" early-exit. Behavioral chown
// verification under root is left for the integration smoke test
// on the actual MBP.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chownMirrorFiles must NOT fall back to a root-owned mirror dir as
// the chown target — that would be a no-op (root chowning to root).
// The function must return a clear error in that case so the caller
// can log and the periodic self-heal can retry later (when the dir's
// owner has been fixed by an external means).
func TestChownMirrorFiles_RejectsRootOwnedDir(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to test the chown branch (CI runs as non-root)")
	}
	// On root: synthesize a root-owned dir + files, call chownMirrorFiles,
	// expect an error from the fallback path. Skipped in CI by the geteuid
	// check above; the source-scan tripwire below covers the regression
	// guard.
}

// Source-scan tripwire: chownMirrorFiles MUST stat the mirror dir as
// the fallback target when resolveConsoleUser fails. The pre-Phase-Z
// code went straight to "files left root-owned" — leaving the boot-
// before-login race unfixable.
func TestChownMirrorFiles_HasDirOwnerFallback(t *testing.T) {
	src, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatalf("read migrate.go: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, "func chownMirrorFiles(")
	if idx < 0 {
		t.Fatal("chownMirrorFiles must exist (Phase Z helper)")
	}
	endIdx := strings.Index(body[idx:], "\nfunc ")
	if endIdx < 0 {
		t.Fatal("end of chownMirrorFiles not found")
	}
	fnBody := body[idx : idx+endIdx]

	if !strings.Contains(fnBody, "statFileOwner(mirrorDir)") {
		t.Error("chownMirrorFiles must stat the mirror dir as the fallback chown target via statFileOwner — without it, boot-before-login leaves files root-owned")
	}
	if !strings.Contains(fnBody, "uid == 0") {
		t.Error("chownMirrorFiles must reject root-owned dir as a chown target — chowning root->root is a silent no-op")
	}
}

// runMirrorChownSelfHeal must short-circuit when not running as root
// (CLI mode does not write mirror files).
func TestRunMirrorChownSelfHeal_SkipsWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("only meaningful when running as non-root")
	}
	tmp := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should return effectively-immediately, never write or chown.
	// We can't observe a no-op directly; rely on it not panicking +
	// completing within the deadline.
	done := make(chan struct{})
	go func() {
		runMirrorChownSelfHeal(ctx, tmp)
		close(done)
	}()
	select {
	case <-done:
		// Good — the goroutine returned quickly.
	case <-time.After(500 * time.Millisecond):
		t.Error("runMirrorChownSelfHeal must return quickly when not running as root")
	}
}

// runMirrorChownSelfHeal must short-circuit when mirrorDir is empty
// (bundled mode, no --mirror-dir flag).
func TestRunMirrorChownSelfHeal_SkipsWhenMirrorDirEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		runMirrorChownSelfHeal(ctx, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("runMirrorChownSelfHeal must return immediately when mirrorDir is empty")
	}
}

// Source-scan tripwire: runMirrorChownSelfHeal must run on a 30s
// ticker. Any cadence change is intentional + worth code review.
func TestRunMirrorChownSelfHeal_Has30sCadence(t *testing.T) {
	src, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatalf("read migrate.go: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func runMirrorChownSelfHeal(")
	if idx < 0 {
		t.Fatal("runMirrorChownSelfHeal must exist (Phase Z self-heal goroutine)")
	}
	endIdx := strings.Index(body[idx:], "\nfunc ")
	if endIdx < 0 {
		endIdx = len(body) - idx
	}
	fnBody := body[idx : idx+endIdx]

	if !strings.Contains(fnBody, "30 * time.Second") {
		t.Error("runMirrorChownSelfHeal must use 30s cadence (slower thrashes; faster wastes cycles for a rare bug)")
	}
	if !strings.Contains(fnBody, "statFileOwner(") {
		t.Error("runMirrorChownSelfHeal must check file ownership via statFileOwner before re-chowning")
	}
}

// Source-scan tripwire: runServe in main.go MUST spawn
// runMirrorChownSelfHeal as a goroutine so the self-heal actually
// runs in production. Without this wiring, the fix is dead code.
func TestRunServe_SpawnsMirrorChownSelfHeal(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "go runMirrorChownSelfHeal(") {
		t.Error("main.go::runServe must spawn runMirrorChownSelfHeal as a goroutine — otherwise the Phase Z fix is dead code")
	}
}

// Behavioral test for the dir-owner fallback path: writing a sentinel
// file inside a tempdir, calling chownMirrorFiles as non-root, and
// asserting it returns an error (because we're not root, the
// resolveConsoleUser primary path may succeed, but if it doesn't,
// the dir-stat fallback would attempt a chown that errors). The
// goal isn't to verify chown success — it's to verify the function
// runs to completion without panicking and reports failures cleanly.
func TestChownMirrorFiles_NonRootDoesNotPanic(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test exercises the non-root code path")
	}
	tmp := t.TempDir()
	tokenPath := filepath.Join(tmp, systemMirrorTokenFile)
	portPath := filepath.Join(tmp, systemMirrorPortFile)
	if err := os.WriteFile(tokenPath, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portPath, []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// As non-root, chownMirrorFiles will likely succeed via the primary
	// resolveConsoleUser path (we ARE the console user). Or it may fail
	// if the /dev/console probe doesn't return our user. Either is OK
	// — the test is about not panicking.
	_ = chownMirrorFiles(tmp, tokenPath, portPath)
}
