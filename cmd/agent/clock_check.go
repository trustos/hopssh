package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/nebulacfg"
)

// init wires the configured cert-skew tolerance into the vendored Nebula
// cert package before any CAPool or Certificate is loaded. Patch 25 makes
// cert.Expired() honor this global; with the default 1h tolerance, transient
// clock jumps inside that window stop being fatal to handshakes.
func init() {
	cert.ClockSkewTolerance = nebulacfg.ClockSkewTolerance
}

// Clock-skew tolerances for cert validation.
//   - skewWarn (60s): log a warning, but proceed; well within 24h cert window
//     with comfortable margin.
//   - skewBadStart (30min): try a platform-native NTP resync before starting
//     Nebula so cert validation doesn't reject every handshake.
const (
	skewWarn     = 60 * time.Second
	skewBadStart = 30 * time.Minute
	clockProbeTO = 10 * time.Second
)

// EnsureClockSane probes the control-plane endpoint's HTTP Date header to
// detect local-clock skew. If the local clock is more than skewBadStart from
// the server's, it attempts a platform-native time resync (best-effort) and
// re-probes. Returns the final observed skew so callers can log + decide.
//
// Never fails the agent: a network problem, missing endpoint, or unsupported
// resync command all degrade to a logged message and the agent continues.
//
// Why this matters: Nebula's X.509 cert validation is strict about NotBefore
// and NotAfter; a UTM-suspended VM, sleepy laptop, or board without RTC can
// wake with hours of skew and every handshake will fail with "certificate is
// expired" until the clock is fixed. Tailscale doesn't have this problem
// because WireGuard uses static keys with no time-bounded credential between
// peers. We can't change Nebula's PKI but we can sanity-check the clock
// before starting Nebula.
func EnsureClockSane(ctx context.Context, endpoint string) time.Duration {
	if endpoint == "" {
		return 0
	}
	skew, err := probeClockSkew(ctx, endpoint)
	if err != nil {
		log.Printf("[clock] skew probe failed (continuing): %v", err)
		return 0
	}
	abs := skew
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs <= skewWarn:
		// Healthy — silent.
		return skew
	case abs < skewBadStart:
		log.Printf("[clock] system clock %s off from %s (within tolerance, proceeding)", skew.Round(time.Second), endpoint)
		return skew
	}

	// Beyond skewBadStart — try a resync.
	log.Printf("[clock] system clock %s off from %s — attempting NTP resync", skew.Round(time.Second), endpoint)
	if out, rsErr := attemptClockResync(ctx); rsErr != nil {
		log.Printf("[clock] auto-resync failed: %v (output: %q)", rsErr, out)
	} else if out != "" {
		log.Printf("[clock] resync output: %s", out)
	}

	skew2, err := probeClockSkew(ctx, endpoint)
	if err != nil {
		log.Printf("[clock] re-probe after resync failed: %v", err)
		return skew
	}
	abs2 := skew2
	if abs2 < 0 {
		abs2 = -abs2
	}
	if abs2 < skewBadStart {
		log.Printf("[clock] resync recovered system clock to within %s", skew2.Round(time.Second))
		return skew2
	}
	log.Printf("[clock] WARNING: system clock still %s off after resync. Nebula cert validation will fail. Manual fix required (e.g. `sudo date -u -s ...` on Unix, `w32tm /resync /force` on Windows).", skew2.Round(time.Second))
	return skew2
}

// probeClockSkew issues an HTTP HEAD against endpoint and parses the Date
// response header. Returns localTime - serverTime (positive = local is ahead).
func probeClockSkew(ctx context.Context, endpoint string) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, clockProbeTO)
	defer cancel()
	// Use a fresh transport so we don't pick up stale connection state if
	// this is the very first call after wake (rare but real on macOS).
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: clockProbeTO,
			DisableKeepAlives:     true,
		},
		Timeout: clockProbeTO,
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("HEAD %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	dateStr := resp.Header.Get("Date")
	if dateStr == "" {
		return 0, fmt.Errorf("no Date header in response from %s", endpoint)
	}
	serverTime, err := http.ParseTime(dateStr)
	if err != nil {
		return 0, fmt.Errorf("parse Date %q: %w", dateStr, err)
	}
	return time.Now().Sub(serverTime), nil
}

// attemptClockResync invokes a platform-native NTP step. Best-effort — if
// the binary is missing or the OS rejects, we log and move on. Caller is
// expected to be running with whatever privileges the underlying tool needs
// (the hopssh agent runs as root via launchd/systemd, so this works for the
// common case).
func attemptClockResync(ctx context.Context) (string, error) {
	rsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		// sntp -sS = step + slew. Requires root. Apple ships sntp at /usr/bin/sntp.
		out, err := exec.CommandContext(rsCtx, "/usr/bin/sntp", "-sS", "time.apple.com").CombinedOutput()
		return string(out), err
	case "linux":
		// Prefer chrony if present (steps faster); fall back to timedatectl.
		if _, err := exec.LookPath("chronyc"); err == nil {
			out, err := exec.CommandContext(rsCtx, "chronyc", "makestep").CombinedOutput()
			return string(out), err
		}
		if _, err := exec.LookPath("timedatectl"); err == nil {
			// Force NTP on; if systemd-timesyncd is present, it'll step.
			out, err := exec.CommandContext(rsCtx, "timedatectl", "set-ntp", "true").CombinedOutput()
			return string(out), err
		}
		return "", fmt.Errorf("no resync tool found (install chrony or systemd-timesyncd)")
	case "windows":
		// w32tm /resync /force requires admin token — Windows agent runs
		// under LocalSystem (per CLAUDE.md SCM integration), which has it.
		out, err := exec.CommandContext(rsCtx, "w32tm.exe", "/resync", "/force").CombinedOutput()
		return string(out), err
	default:
		return "", fmt.Errorf("clock resync not implemented for %s", runtime.GOOS)
	}
}
