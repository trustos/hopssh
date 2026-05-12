package client

// Phase EE (v0.11.24): watchdog escalation to process exit.
//
// All three independent watchdogs (Phase DD watcher, v0.10.36 stuck-
// data-plane, Phase P renewal) follow the same pattern when they fire:
//
//   1. Capture a forensic dump
//   2. Call inst.restartFn (= Client.connect(name)) to swap the wedged
//      Nebula svc for a fresh one
//   3. If restartFn returns an error → log it and wait for the next
//      cooldown window (30 min), try again
//
// Step 3 is the gap this file closes. Production evidence (2026-05-11/12,
// MBP home enrollment): a wedged Nebula goroutine kept the kernel UDP
// socket bound for ~24 hours. The watcher-watchdog fired correctly,
// wrote a forensic dump, called restartFn — and restartFn failed with
// "address already in use" because the wedged goroutine still owned the
// kernel UDP socket. No matter how many times we call restartFn, the
// kernel socket stays bound to PID 72829 until that PID exits.
//
// The only recovery was `sudo launchctl kickstart -k system/com.hopssh.agent`.
//
// Phase EE: after N consecutive restartFn failures, the watchdog calls
// osExitFn(75) instead of just logging. launchd's KeepAlive=true (macOS),
// systemd's Restart=on-failure (Linux), and Windows SCM's recovery
// policy all respawn the process within ~5s, releasing every kernel
// UDP socket. The forensic dumps from each trip survive in
// <configDir>/<name>/{watcher,renewal,stuck-state}-*.txt so the
// post-mortem trail isn't lost.
//
// Why exit code 75: EX_TEMPFAIL from sysexits.h — the conventional
// "I failed, please retry me" code. macOS launchd treats any non-zero
// as needing respawn (when SuccessfulExit=false); this code avoids
// confusion with crash codes (the agent isn't crashing — it's
// intentionally cycling itself).
//
// Why N=3: 1 trip is normal (transient wedge that restartFn fixes),
// 2 is concerning, 3 × 30-min cooldown = 90 min of failed self-
// recovery is the point at which "this isn't going to fix itself".

import (
	"log"
	"os"
)

// osExitFn is overridable in tests via injection. Production binds to
// os.Exit; the tripwire tests substitute a fake to assert escalation
// without actually exiting the test runner.
var osExitFn = os.Exit

// escalateAfterN is the consecutive-failure threshold at which a
// watchdog calls osExitFn(75) instead of logging and retrying. Var
// (not const) so tests can lower it to 2 for fast iteration; the
// production default below the 30-min cooldown gives users ~90 min
// of "we tried to fix it 3 times" before process restart.
var escalateAfterN = 3

// escalationExitCode mirrors EX_TEMPFAIL from sysexits.h. Service
// supervisors (launchd, systemd, Windows SCM) respawn on any non-zero
// exit; 75 specifically signals "transient failure, please retry me"
// to distinguish from crashes (e.g., panics → 2, SIGTERM → 143).
const escalationExitCode = 75

// recordRestartFailure increments the per-watchdog consecutive-failure
// counter and escalates to process exit when the counter hits
// escalateAfterN. Returns true if escalation fired (so the caller can
// log and return cleanly; in practice osExitFn(75) never returns in
// production but tests inject a no-op exit so the caller must still
// behave correctly post-call).
//
// Callers pass their own *int counter — each watchdog goroutine has
// its own independent count. A successful restart resets via
// recordRestartSuccess so transient wedges don't accumulate state
// across hours.
func recordRestartFailure(watchdogName, instName string, consecutive *int) bool {
	*consecutive++
	if *consecutive >= escalateAfterN {
		log.Printf("[%s %s] ESCALATING: %d consecutive restartFn failures — calling os.Exit(%d) so the service supervisor (launchd / systemd / SCM) respawns the agent. This releases any wedged kernel UDP sockets that restartFn can't clear via inst.close().",
			watchdogName, instName, *consecutive, escalationExitCode)
		// Final newline-flush so the log line lands in journalctl /
		// the launchd log file before the process disappears.
		log.Printf("[%s %s] forensic dumps preserved in <configDir>/<name>/; post-respawn diagnostics retain the trail",
			watchdogName, instName)
		osExitFn(escalationExitCode)
		return true
	}
	return false
}

// recordRestartSuccess resets the per-watchdog failure counter. Called
// after restartFn returns nil. Without this, a single transient wedge
// over a long-running agent's lifetime would slowly accumulate toward
// the escalation threshold even though each individual recovery
// succeeded.
func recordRestartSuccess(consecutive *int) {
	*consecutive = 0
}
