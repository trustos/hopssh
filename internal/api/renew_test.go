package api

import (
	"testing"
)

// parseCapabilitiesForRenew is the local helper in renew.go that
// turns the on-disk JSON capability blob into a set. The heartbeat
// response uses it to answer "does THIS node have the relay
// capability?" — getting it wrong silently breaks Pillar 3 (peer
// relay discovery), so test the corner cases explicitly.

func TestParseCapabilitiesForRenew_Empty(t *testing.T) {
	got := parseCapabilitiesForRenew("")
	if len(got) != 0 {
		t.Errorf("empty input should yield empty set, got %v", got)
	}
}

func TestParseCapabilitiesForRenew_TerminalSet(t *testing.T) {
	got := parseCapabilitiesForRenew(`["terminal","health","forward"]`)
	for _, want := range []string{"terminal", "health", "forward"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if got["relay"] {
		t.Errorf("unexpected relay in %v", got)
	}
}

func TestParseCapabilitiesForRenew_RelayPresent(t *testing.T) {
	got := parseCapabilitiesForRenew(`["terminal","relay"]`)
	if !got["relay"] {
		t.Errorf("expected relay=true; got %v", got)
	}
}

func TestParseCapabilitiesForRenew_MalformedReturnsEmpty(t *testing.T) {
	for _, in := range []string{
		"not json",
		`{"oops": true}`, // object, not array
		`[1,2,3]`,        // wrong element type
		`[`,              // truncated
	} {
		got := parseCapabilitiesForRenew(in)
		if len(got) != 0 {
			t.Errorf("malformed %q should yield empty set, got %v", in, got)
		}
	}
}

func TestParseCapabilitiesForRenew_DuplicatesCollapse(t *testing.T) {
	// Set semantics — duplicates aren't an error, they collapse.
	got := parseCapabilitiesForRenew(`["relay","relay","terminal","relay"]`)
	if len(got) != 2 {
		t.Errorf("expected 2 unique entries, got %d: %v", len(got), got)
	}
	if !got["relay"] || !got["terminal"] {
		t.Errorf("missing values: %v", got)
	}
}
