package mesh

import (
	"os"
	"strings"
	"testing"
)

// TestLighthouseLogLevelDefaultsToInfo locks in v0.11.25's bump: the
// per-network lighthouse Nebula instance now logs at InfoLevel by
// default (was WarnLevel pre-Phase-FF). Without Info-level we miss the
// entire relay flow (handleCreateRelayRequest / send CreateRelayResponse
// are Info-level inside Nebula), which made the 2026-05-14 relay-stall
// incident impossible to diagnose from server logs alone.
//
// The check is a source-scan because the actual logger gets constructed
// inside the StartNetwork goroutine, which is hard to test in isolation
// without standing up a real lighthouse instance. Keep this as a
// tripwire so future refactors that drop the env-var or revert to
// WarnLevel are caught at PR time.
func TestLighthouseLogLevelDefaultsToInfo(t *testing.T) {
	src, err := os.ReadFile("network_manager.go")
	if err != nil {
		t.Fatalf("read network_manager.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "loggerLevel := logrus.InfoLevel") {
		t.Error("per-network lighthouse must default loggerLevel = logrus.InfoLevel — pre-Phase-FF WarnLevel suppressed the entire relay flow")
	}
	if !strings.Contains(s, "HOPSSH_LIGHTHOUSE_LOG_LEVEL") {
		t.Error("HOPSSH_LIGHTHOUSE_LOG_LEVEL env-var override missing — operators need a way to dial back log volume without re-releasing")
	}
	if strings.Contains(s, "logger.SetLevel(logrus.WarnLevel)") {
		t.Error("regression: legacy WarnLevel default is back; this silences relay flow logs")
	}
}

// TestVendorPatch26AppliesAndIsHonored locks in the Nebula vendor patch
// that elevates the three silent-drop branches in relay_manager.go's
// handleCreateRelayRequest to Warn-level logs. Pre-patch, the lighthouse
// would drop a CreateRelayRequest into the void (silent return) when
// it had no HostInfo for the target peer or when the peer's remote was
// invalid — giving operators zero visibility into relay-stall causes.
//
// This test re-applies after every `go mod vendor` (the patches dir is
// the source of truth; vendor/ is regenerated). If the vendor file is
// missing the Warn lines, the patch didn't apply — likely a
// `make patch-vendor` skip during a fresh setup.
func TestVendorPatch26AppliesAndIsHonored(t *testing.T) {
	// File lives at <repo>/vendor/...; test runs from internal/mesh/.
	src, err := os.ReadFile("../../vendor/github.com/slackhq/nebula/relay_manager.go")
	if err != nil {
		t.Fatalf("read vendored relay_manager.go: %v (run `make patch-vendor` if vendor was just regenerated)", err)
	}
	s := string(src)

	wantSubstrings := []string{
		"hopssh patch 26",
		"hopssh: dropping relay forward — this lighthouse is not configured as a relay",
		"hopssh: dropping relay forward — no HostInfo for target peer",
		"hopssh: dropping relay forward — target peer has no valid remote address",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(s, want) {
			t.Errorf("vendor patch 26 not applied — missing %q in vendor/github.com/slackhq/nebula/relay_manager.go. Run `make patch-vendor`.", want)
		}
	}
}

// TestLighthouseLogLevelEnvVarHonored locks in the env-var override
// parsing logic — operators flipping the level via env should work
// without code changes. Tests the actual parsing function indirectly
// via source scan since the var is consumed inside StartNetwork.
func TestLighthouseLogLevelEnvVarHonored(t *testing.T) {
	// Sanity: env var doesn't leak into our test runner state.
	if got := os.Getenv("HOPSSH_LIGHTHOUSE_LOG_LEVEL"); got != "" {
		t.Logf("HOPSSH_LIGHTHOUSE_LOG_LEVEL is set in test env: %q (this is OK, just noting)", got)
	}

	src, err := os.ReadFile("network_manager.go")
	if err != nil {
		t.Fatalf("read network_manager.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "logrus.ParseLevel") {
		t.Error("env-var-controlled logger level must use logrus.ParseLevel — accepts: panic|fatal|error|warn|info|debug|trace")
	}
}
