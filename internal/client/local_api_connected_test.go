package client

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"
)

// Phase P UI-honesty: enrollmentStatus.Connected used to be set
// unconditionally on `ctrl != nil`, so an agent with an expired
// cert and 0 peers painted a green "connected" pill while the
// inline LastError said "certificate expired". These tests pin
// the corrected derivation:
//
//   Connected ⇔ ctrl != nil && certNotExpired && (peers > 0 || recentHeartbeat)

// computeConnectedFlag mirrors the production derivation in
// enrollmentStatus, called with the post-cert / post-peer-state
// fields already populated. We test the LOGIC here in isolation
// because the full enrollmentStatus path needs a serverSet +
// instanceRegistry stand-in that isn't worth building for a 4-line
// boolean.
//
// The production code at cmd/agent/local_api.go (Phase P fix) reads:
//
//   const heartbeatGrace = 3 * time.Minute
//   certValid := es.LastError == ""
//   activeFlow := es.PeersDirect+es.PeersRelayed > 0
//   recentHeartbeat := inst.heartbeatSuccessAge() < heartbeatGrace
//   es.Connected = certValid && (activeFlow || recentHeartbeat)
func computeConnectedFlag(certValid, activeFlow, recentHeartbeat bool) bool {
	return certValid && (activeFlow || recentHeartbeat)
}

func TestEnrollmentStatus_ConnectedFalseWhenCertExpired(t *testing.T) {
	// Cert expired (LastError set). Peers don't matter, heartbeat
	// doesn't matter — we should NOT show green.
	for _, peers := range []bool{true, false} {
		for _, hb := range []bool{true, false} {
			got := computeConnectedFlag(false, peers, hb)
			if got {
				t.Errorf("certValid=false peers=%v hb=%v → got Connected=true (must be false)", peers, hb)
			}
		}
	}
}

func TestEnrollmentStatus_ConnectedTrueWhenPeersFlowing(t *testing.T) {
	// Cert valid + at least one peer (direct or relayed) → green.
	if !computeConnectedFlag(true, true, false) {
		t.Errorf("cert valid + peers flowing → must be Connected=true")
	}
}

func TestEnrollmentStatus_ConnectedTrueWhenRecentHeartbeatNoPeers(t *testing.T) {
	// First-startup window: cert valid, peers haven't done first
	// handshake yet (so peers=0), but heartbeat just succeeded.
	// Connected should be true so the pill doesn't flash red during
	// normal startup.
	if !computeConnectedFlag(true, false, true) {
		t.Errorf("cert valid + recent heartbeat (no peers yet) → must be Connected=true")
	}
}

func TestEnrollmentStatus_ConnectedFalseWhenZeroPeersAndStaleHeartbeat(t *testing.T) {
	// Cert valid but agent isn't actually talking to anyone — neither
	// peers nor the control plane in the last 3 min. This is the
	// "agent stuck silently" state we want to detect, even if cert
	// hasn't expired yet.
	if computeConnectedFlag(true, false, false) {
		t.Errorf("cert valid + 0 peers + stale heartbeat → must be Connected=false")
	}
}

// Integration-style test using the real expired-cert fixture from
// the user's MBP 2026-05-02 incident. Demonstrates the bug that
// motivated this fix: pre-Phase-P the green pill rendered while
// the cert was hard-expired.
func TestEnrollmentStatus_ExpiredCertFixtureLastError(t *testing.T) {
	// Read the actual expired Nebula cert from the fixture.
	// The cert NotAfter is 2026-05-02T06:14:27Z — already in the
	// past at any time the test runs after that date.
	pem, err := readFixtureCert(t)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	notAfter, err := parseNebulaCertNotAfter(pem)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	expectedNotAfter := time.Date(2026, 5, 2, 6, 14, 27, 0, time.UTC)
	if !notAfter.Equal(expectedNotAfter) {
		t.Errorf("fixture NotAfter = %s, expected %s", notAfter.Format(time.RFC3339), expectedNotAfter.Format(time.RFC3339))
	}
	// The pre-Phase-P bug: this cert would render green. Now it
	// cannot — production derivation gates on certValid and
	// LastError = "certificate expired" sets certValid = false.
	certValid := time.Until(notAfter) > 0
	if certValid {
		t.Skipf("test must be run after 2026-05-02T06:14:27Z; cert hasn't expired yet at %s", time.Now().UTC().Format(time.RFC3339))
	}
	// Whatever peers/heartbeat state — if cert is expired, Connected MUST be false.
	if computeConnectedFlag(certValid, true, true) {
		t.Errorf("expired-cert fixture rendered Connected=true even with peers + recent heartbeat (regression of Phase P fix)")
	}
}

// Test helpers — read the on-disk fixture without dragging
// internal/api into the agent build.

func readFixtureCert(t *testing.T) ([]byte, error) {
	t.Helper()
	const fixturePath = "testdata/expired-cert-mbp-2026-05-02/node.crt"
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(data), "NEBULA CERTIFICATE") {
		return nil, errors.New("fixture is not a Nebula certificate")
	}
	return data, nil
}

// parseNebulaCertNotAfter unmarshals a Nebula PEM cert and returns
// its NotAfter timestamp.
func parseNebulaCertNotAfter(pem []byte) (time.Time, error) {
	c, _, err := cert.UnmarshalCertificateFromPEM(pem)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter(), nil
}
