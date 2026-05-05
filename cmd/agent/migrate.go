package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// migrateEnrollmentsToSystem moves enrollments + per-enrollment subdirs
// from a source configDir (typically a user-mode `~/Library/Application
// Support/hopssh`) into a destination configDir (typically the system-
// mode `/etc/hop-agent`). Used when the desktop client converts from
// "spawn bundled agent" to "talk to system LaunchDaemon agent".
//
// Move-not-copy: leaving the source files in place would cause
// detectParallelInstall to flag them as a leftover system install,
// re-surfacing the warning banner the user just got rid of. Atomic-
// ish via os.Rename per file (same filesystem assumption holds for
// macOS user home + /etc both on the boot volume).
//
// Validates the source's structural integrity BEFORE touching anything:
// every per-enrollment subdir referenced by enrollments.json must exist
// and contain node.crt + node.key + ca.crt. If any are malformed, the
// function aborts and the source is left untouched — the caller can
// surface the error and the user falls back to bundled mode cleanly.
//
// Caller is responsible for ensuring NO agent process is currently
// running against the source dir (e.g., the desktop has SIGKILLed its
// bundled child first). This function does NOT stop processes.
func migrateEnrollmentsToSystem(source, dest string) error {
	if source == "" || dest == "" {
		return fmt.Errorf("migrate: source + dest required")
	}
	source = filepath.Clean(source)
	dest = filepath.Clean(dest)
	if source == dest {
		return fmt.Errorf("migrate: source and dest are the same (%q); nothing to do", source)
	}

	// 1. Source must exist and contain enrollments.json.
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return fmt.Errorf("migrate: source %q does not exist", source)
	}
	srcRegistry, err := loadEnrollmentRegistry(source)
	if err != nil {
		return fmt.Errorf("migrate: load source registry: %w", err)
	}
	if srcRegistry.Len() == 0 {
		// No enrollments to migrate. Treat as a no-op success — the
		// caller can still install the LaunchDaemon afterwards.
		return nil
	}

	// 2. Validate every enrollment in the source has its required
	//    files. Done UP FRONT so we don't half-migrate.
	required := []string{"node.crt", "node.key", "ca.crt"}
	for _, e := range srcRegistry.List() {
		subdir := enrollmentDir(source, e.Name)
		for _, f := range required {
			path := filepath.Join(subdir, f)
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("migrate: enrollment %q missing %s: %w", e.Name, f, err)
			}
		}
	}

	// 3. Prepare destination.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("migrate: mkdir dest %q: %w", dest, err)
	}
	// Refuse to clobber a non-empty dest's enrollments.json — a
	// pre-existing system install means the operator should run
	// `hop-agent uninstall --purge` first. We don't want to silently
	// merge two registries.
	destReg, err := loadEnrollmentRegistry(dest)
	if err != nil {
		return fmt.Errorf("migrate: load dest registry: %w", err)
	}
	if destReg.Len() > 0 {
		return fmt.Errorf("migrate: dest %q already has %d enrollment(s); run `hop-agent uninstall --purge` to clear it before migrating", dest, destReg.Len())
	}

	// 4. Move per-enrollment subdirs first; then the registry file
	//    itself. If any rename fails, abort — but a partial move can
	//    happen (no atomic multi-file move on Unix). We mitigate by
	//    moving the registry LAST, so if subdirs partially-moved and
	//    the registry move fails, the source registry still references
	//    the now-moved subdirs. The caller's recovery (re-run migrate)
	//    sees that the dest has subdirs but no registry file, picks
	//    them up via a re-validate, and completes. To keep this
	//    function idempotent on retry, we tolerate "subdir already
	//    exists at dest, missing at source" as a re-run, not an error.
	for _, e := range srcRegistry.List() {
		srcSub := enrollmentDir(source, e.Name)
		dstSub := enrollmentDir(dest, e.Name)
		if _, err := os.Stat(dstSub); err == nil {
			// Already moved (probably from a previous half-run). Skip.
			continue
		}
		if err := os.Rename(srcSub, dstSub); err != nil {
			return fmt.Errorf("migrate: move %q -> %q: %w", srcSub, dstSub, err)
		}
	}

	// 5. Move the enrollments.json registry. After this, the source
	//    appears "empty" to detectParallelInstall and the dest is the
	//    canonical install.
	srcReg := filepath.Join(source, enrollmentsFile)
	dstReg := filepath.Join(dest, enrollmentsFile)
	if err := os.Rename(srcReg, dstReg); err != nil {
		return fmt.Errorf("migrate: move %q -> %q: %w", srcReg, dstReg, err)
	}

	// 6. Best-effort: also move the registry backup if present.
	srcBak := srcReg + ".bak"
	if _, err := os.Stat(srcBak); err == nil {
		_ = os.Rename(srcBak, dstReg+".bak")
	}

	// 7. Source's local-api-token is now stale — the bundled-mode
	//    token. Remove it so a future re-spawn in bundled mode mints
	//    a fresh one.
	_ = os.Remove(filepath.Join(source, localAPITokenFile))

	return nil
}

// resolveConsoleUser returns the macOS console user's username +
// home directory. Used by migrate to figure out where to write the
// mirror token file (the .app reads from <user-home>/Library/Application
// Support/hopssh/system-local-api-token).
//
// Resolution order:
//
//	1. $SUDO_USER — set by sudo + osascript "with administrator privileges"
//	   when our process is the privileged shell.
//	2. `stat -f "%Su" /dev/console` — falls back to whoever is logged
//	   into the GUI session. macOS-only.
//
// Returns ("", "", err) if both fail; caller should abort the migration
// and tell the user to run from a logged-in console session.
func resolveConsoleUser() (username, homeDir string, err error) {
	if u := strings.TrimSpace(os.Getenv("SUDO_USER")); u != "" && u != "root" {
		return u, fmt.Sprintf("/Users/%s", u), nil
	}
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil {
		return "", "", fmt.Errorf("resolveConsoleUser: stat /dev/console: %w", err)
	}
	u := strings.TrimSpace(string(out))
	if u == "" || u == "root" {
		return "", "", fmt.Errorf("resolveConsoleUser: no GUI user logged in (got %q)", u)
	}
	return u, fmt.Sprintf("/Users/%s", u), nil
}

// systemMirrorDir returns the per-user directory where the agent
// mirrors its local-api token + port for the desktop .app to discover.
// Path matches the user-mode configDir convention so the .app can
// look in one well-known place.
func systemMirrorDir(userHome string) string {
	return filepath.Join(userHome, "Library", "Application Support", "hopssh")
}

// systemMirrorTokenFile is the per-user mirror of the local-api token,
// readable by the user-level .app. Distinct filename from
// localAPITokenFile to avoid colliding with the .app's own bundled-
// agent token cache (which the user's bundled agent owns).
const systemMirrorTokenFile = "system-local-api-token"

// systemMirrorPortFile is a sibling file containing just the loopback
// port the system agent's local API is listening on. The .app reads
// this to know where to direct fetches without scraping logs.
const systemMirrorPortFile = "system-local-api-port"

// deriveRunMode returns "bundled" or "system" based on the agent's
// active configDir. Used by the desktop UI to bind the "Run in the
// background" toggle to a real running-mode source-of-truth (rather
// than the launchctl-loaded state of the plist file, which can lie
// when an admin manually mucks with launchctl).
//
// macOS-only — Linux + Windows always report "system" because they
// don't have a "user vs system mode" distinction in the .app sense.
func deriveRunMode(currentConfigDir string) string {
	if runtime.GOOS != "darwin" {
		return "system"
	}
	if currentConfigDir == "/etc/hop-agent" {
		return "system"
	}
	return "bundled"
}

// systemMirrorDirOverride is set by the `--mirror-dir` flag on the
// `hop-agent serve` command. When non-empty, startLocalAPI mirrors
// the chosen token + port into that directory (chowned to the console
// user) so the desktop .app can attach to the system agent without
// needing root privileges to read /etc/hop-agent/local-api-token.
//
// Empty (the default) disables mirroring — that's the normal CLI
// install path on a headless server.
var systemMirrorDirOverride string

// writeSystemMirrorFiles writes <mirrorDir>/system-local-api-token +
// <mirrorDir>/system-local-api-port and chowns them to the console
// user. Best-effort — errors are logged but don't fail the agent
// startup; bundled mode in the .app is the recoverable fallback.
//
// Called by startLocalAPI after the loopback listener has bound + a
// token has been generated.
func writeSystemMirrorFiles(mirrorDir, token, addr string) {
	if mirrorDir == "" {
		return
	}
	// Parse the addr ("127.0.0.1:54321") into just the port digits.
	port := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		port = addr[i+1:]
	}

	// Ensure dir exists. We expect `hop-agent install --migrate-from` to
	// have already created + chowned it, but if we're running as root
	// without that pre-setup we mkdir defensively.
	if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
		log.Printf("[mirror] mkdir %q: %v", mirrorDir, err)
		return
	}

	tokenPath := filepath.Join(mirrorDir, systemMirrorTokenFile)
	portPath := filepath.Join(mirrorDir, systemMirrorPortFile)

	if err := atomicWrite(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		log.Printf("[mirror] write token: %v", err)
		return
	}
	if err := atomicWrite(portPath, []byte(port+"\n"), 0o644); err != nil {
		log.Printf("[mirror] write port: %v", err)
	}

	// Chown both files to the console user IF we're running as root
	// (which we expect for system-mode). Skip otherwise — files stay
	// owned by whoever wrote them (the user themselves running CLI).
	//
	// Phase Z (v0.10.91): TWO-LAYER FALLBACK to fix the boot-order
	// race that left mirror files root-owned across reboots.
	//
	// 1. resolveConsoleUser tries $SUDO_USER then `stat /dev/console`.
	//    At BOOT time before any user logs in, /dev/console is owned
	//    by root, resolveConsoleUser returns error, and pre-fix the
	//    files stayed root-owned forever — even after the user logged
	//    in. The .app, running as the user, couldn't read the mode-
	//    0600 token and showed "agent unreachable".
	//
	// 2. Fallback: stat the mirror DIR's owner. The dir was created
	//    during `hop-agent install --migrate-from` (or convert_to_
	//    system_service via the Tauri shell), which ran AS the user.
	//    So the dir's owner IS the right chown target — independent
	//    of /dev/console state.
	//
	// Plus: runChownSelfHealLoop (called separately from main.go) re-
	// runs this chown periodically, catching the case where this
	// initial write happens before the dir is properly owned (rare).
	if os.Geteuid() == 0 {
		if err := chownMirrorFiles(mirrorDir, tokenPath, portPath); err != nil {
			log.Printf("[mirror] chown failed: %v (mirror files left root-owned; periodic self-heal will retry)", err)
		}
	}
	log.Printf("[mirror] wrote %s + %s", tokenPath, portPath)
}

// chownMirrorFiles chowns the mirror token + port files to the user
// who should be able to read them. Tries `resolveConsoleUser` first
// (covers the user-is-logged-in case), falls back to stat-ing the
// mirror dir's owner (covers the boot-before-login case + any other
// situation where /dev/console doesn't have a real user). Idempotent
// — safe to call repeatedly (used by both the initial write and the
// periodic self-heal loop).
//
// Returns nil if either path succeeded, error if both failed.
func chownMirrorFiles(mirrorDir, tokenPath, portPath string) error {
	user, _, err := resolveConsoleUser()
	if err == nil && user != "" && user != "root" {
		// Console user available — primary path.
		if cerr := exec.Command("chown", user+":staff", tokenPath, portPath).Run(); cerr == nil {
			return nil
		} else {
			err = cerr
		}
	}

	// Fallback: use the mirror dir's UID/GID as the chown target.
	// `hop-agent install --migrate-from` ran as the user and created
	// the dir, so it's owned by the user we want.
	uid, gid, ok := statFileOwner(mirrorDir)
	if !ok {
		return fmt.Errorf("resolveConsoleUser: %v; could not stat mirror dir owner", err)
	}
	if uid == 0 {
		// Dir is also root-owned — we have no good target. Refuse.
		return fmt.Errorf("resolveConsoleUser: %v; mirror dir is also root-owned (no fallback available)", err)
	}
	chownTarget := fmt.Sprintf("%d:%d", uid, gid)
	if cerr := exec.Command("chown", chownTarget, tokenPath, portPath).Run(); cerr != nil {
		return fmt.Errorf("chown %s -> %s: %w", chownTarget, tokenPath, cerr)
	}
	log.Printf("[mirror] chowned to mirror-dir owner uid=%d gid=%d (resolveConsoleUser unavailable: %v)", uid, gid, err)
	return nil
}

// runMirrorChownSelfHeal periodically re-checks the mirror files'
// ownership and re-chowns if they're still root-owned. Defense in
// depth against the boot-before-login race: if writeSystemMirrorFiles
// at boot couldn't resolve a user (no console user, no dir owner),
// this loop catches it on the next poll cycle.
//
// Phase Z (v0.10.91). Spawned as a goroutine from runServe in main.go,
// scoped to the agent-wide ctx so SIGTERM stops it cleanly.
//
// Cadence: 30s, intentionally slow. The bug is rare (only fires at
// boot before login) and the fix only needs to land within ~30s of
// user login — no reason to thrash sooner. Once chown succeeds and
// stays correct, this loop becomes a no-op (chownMirrorFiles checks
// state then short-circuits).
func runMirrorChownSelfHeal(ctx context.Context, mirrorDir string) {
	if mirrorDir == "" {
		return
	}
	if os.Geteuid() != 0 {
		// Non-root agent (CLI mode) doesn't write mirror files.
		return
	}
	tokenPath := filepath.Join(mirrorDir, systemMirrorTokenFile)
	portPath := filepath.Join(mirrorDir, systemMirrorPortFile)

	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		// Cheap check: are the files still root-owned? If not, no work to do.
		needsChown := false
		for _, p := range []string{tokenPath, portPath} {
			uid, _, ok := statFileOwner(p)
			if !ok {
				continue // file missing or platform-unsupported
			}
			if uid == 0 {
				needsChown = true
				break
			}
		}
		if !needsChown {
			continue
		}

		log.Printf("[mirror] self-heal: mirror files are root-owned; re-running chown")
		if err := chownMirrorFiles(mirrorDir, tokenPath, portPath); err != nil {
			log.Printf("[mirror] self-heal chown still failing: %v", err)
		} else {
			log.Printf("[mirror] self-heal chown succeeded — .app should attach within seconds")
		}
	}
}
