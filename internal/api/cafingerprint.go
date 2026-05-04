package api

import (
	"crypto/sha256"
	"encoding/hex"
)

// caFingerprint returns the first 12 hex chars of SHA-256(caCertPEM).
// MUST stay byte-for-byte identical to cmd/agent/enrollments.go::
// caFingerprint — both sides exchange these values via the F2 conflict-
// detection header (existingNetworkCAs), and a single-byte mismatch in
// the algorithm would silently break the orphan-node guard.
//
// If you change this, change the agent helper in lockstep AND audit the
// stored Enrollment.CAFingerprint field on every persisted enrollment
// (changing the algorithm without a migration would cause the duplicate
// check to fire-as-mismatch on every restart).
func caFingerprint(caCertPEM []byte) string {
	sum := sha256.Sum256(caCertPEM)
	return hex.EncodeToString(sum[:])[:12]
}
