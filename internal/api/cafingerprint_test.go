package api

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// caFingerprint MUST stay byte-for-byte identical to
// cmd/agent/enrollments.go::caFingerprint — both sides exchange these
// values via F2's existingNetworkCAs header. Drift = orphan-node-guard
// silently breaks. Lock the algorithm.

func TestCAFingerprint_ServerMatchesAgentAlgorithm(t *testing.T) {
	// Reference computation: SHA-256(input) → hex → first 12 chars.
	// This is the exact algorithm from cmd/agent/enrollments.go:491-494.
	input := []byte("-----BEGIN CERTIFICATE-----\nfake test cert bytes\n-----END CERTIFICATE-----\n")
	sum := sha256.Sum256(input)
	want := hex.EncodeToString(sum[:])[:12]

	got := caFingerprint(input)
	if got != want {
		t.Errorf("caFingerprint(%q) = %q, want %q", input, got, want)
	}
	if len(got) != 12 {
		t.Errorf("caFingerprint must return exactly 12 hex chars, got %d (%q)", len(got), got)
	}
}

func TestCAFingerprint_Deterministic(t *testing.T) {
	input := []byte("same input")
	a := caFingerprint(input)
	b := caFingerprint(input)
	if a != b {
		t.Errorf("non-deterministic: %q vs %q", a, b)
	}
}

func TestCAFingerprint_DifferentInputsDifferentOutputs(t *testing.T) {
	a := caFingerprint([]byte("network A CA"))
	b := caFingerprint([]byte("network B CA"))
	if a == b {
		t.Errorf("collision on different inputs: both = %q", a)
	}
}

func TestCAFingerprint_Empty(t *testing.T) {
	// SHA-256("") is well-known: e3b0c44298fc1c149afbf4c8996fb924...
	got := caFingerprint([]byte{})
	want := "e3b0c44298fc"
	if got != want {
		t.Errorf("caFingerprint(empty) = %q, want %q", got, want)
	}
}
