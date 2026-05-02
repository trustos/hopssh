package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// Phase T (v0.10.83): the dashboard's per-peer drill-down replaced the
// always-"unknown" Handshake column with an RTT column. The agent has
// been sending RTTms via cmd/agent/peerstate.go::PeerDetail since well
// before that, but the SERVER side parsePeerState mirror was dropping
// the field on deserialization (no rttMs json tag). v0.10.83 added it.
//
// LastHandshakeSec was simultaneously DROPPED from agent + server
// because Nebula's public API doesn't expose handshake timestamps and
// the field was a perpetual stub.
//
// These tests are the regression boundary: any future change that
// re-removes rttMs from parsePeerState's mirror struct breaks them.

// TestParsePeerState_RTTRoundTrip — agent's JSON shape (with rttMs)
// must round-trip through parsePeerState into PeerDetail.RTTms.
func TestParsePeerState_RTTRoundTrip(t *testing.T) {
	raw := `[
		{"vpnAddr":"10.42.1.7","direct":true,"remoteAddr":"46.10.240.91:4242","rttMs":32},
		{"vpnAddr":"10.42.1.1","direct":true,"remoteAddr":"132.145.232.64:42001","rttMs":47}
	]`
	got := parsePeerState(&raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 peers, got %d: %+v", len(got), got)
	}
	if got[0].VpnAddr != "10.42.1.7" || got[0].RTTms != 32 || !got[0].Direct {
		t.Errorf("peer[0] mismatch: %+v", got[0])
	}
	if got[1].VpnAddr != "10.42.1.1" || got[1].RTTms != 47 {
		t.Errorf("peer[1] mismatch: %+v", got[1])
	}
}

// TestParsePeerState_RelayedPeerHasZeroRTT — when an agent reports a
// relayed peer it doesn't include rttMs (it's only sampled for direct
// peers via runPathQuality). parsePeerState must default to 0 and not
// fail on the missing key. The dashboard renders 0 as em-dash.
func TestParsePeerState_RelayedPeerHasZeroRTT(t *testing.T) {
	raw := `[{"vpnAddr":"10.42.1.5","direct":false,"remoteAddr":""}]`
	got := parsePeerState(&raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(got))
	}
	if got[0].Direct {
		t.Errorf("expected relayed (direct=false), got direct=true")
	}
	if got[0].RTTms != 0 {
		t.Errorf("expected RTTms=0 for relayed peer (no rttMs key in JSON), got %d", got[0].RTTms)
	}
}

// TestParsePeerState_OldAgentLastHandshakeIgnored — pre-v0.10.83 agents
// in the field still send `lastHandshakeSec` in their heartbeat body.
// The server's PeerDetail no longer has that field, so json.Unmarshal
// must silently drop the unknown key, not error. Without this guard a
// fleet rolling-upgrade from < v0.10.83 to >= v0.10.83 would wipe peer
// state on every heartbeat from old agents.
func TestParsePeerState_OldAgentLastHandshakeIgnored(t *testing.T) {
	raw := `[{"vpnAddr":"10.42.1.7","direct":true,"remoteAddr":"46.10.240.91:4242","lastHandshakeSec":1714600000,"rttMs":42}]`
	got := parsePeerState(&raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 peer despite stale lastHandshakeSec key, got %d", len(got))
	}
	if got[0].RTTms != 42 {
		t.Errorf("expected RTTms=42, got %d", got[0].RTTms)
	}
}

// TestParsePeerState_NilOrEmptyReturnsNil — caller-contract: nil/empty/
// "null" JSON means "no data reported yet"; the UI renders nothing
// rather than mis-displaying an empty slice as "0 peers".
func TestParsePeerState_NilOrEmptyReturnsNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  *string
	}{
		{"nil pointer", nil},
		{"empty string", strPtr("")},
		{"literal null", strPtr("null")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePeerState(tc.raw); got != nil {
				t.Errorf("expected nil, got %+v", got)
			}
		})
	}
}

// TestPeerDetail_NoLastHandshakeSecField — source-scan tripwire. Any
// future PR that re-adds LastHandshakeSec to the API mirror struct
// (e.g. by mass-mirroring cmd/agent/peerstate.go::PeerDetail) breaks
// this. We dropped it in v0.10.83 because Nebula's public API doesn't
// expose handshake timestamps; the dashboard rendered it as the
// literal string "unknown" forever, eroding user trust. Don't bring
// it back without first wiring up real data via a vendor patch.
func TestPeerDetail_NoLastHandshakeSecField(t *testing.T) {
	// Encode an empty PeerDetail and confirm the JSON output never
	// contains "lastHandshakeSec". Catches both struct-tag and field
	// regressions (since omitempty would hide a 0 value but a
	// non-omitempty field is unconditionally serialized).
	b, err := json.Marshal(PeerDetail{VpnAddr: "10.42.1.7", Direct: true, RTTms: 32})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "lastHandshakeSec") {
		t.Errorf("PeerDetail must not serialize lastHandshakeSec — Nebula's public API doesn't expose it; v0.10.83 dropped the dead column. Got: %s", b)
	}
}

func strPtr(s string) *string { return &s }
