// Package blocklist is the list of certificates of deleted devices. The
// server sends it with the heartbeat answer; every device and the server's
// lighthouse put it into Nebula's pki.blocklist, so a device deleted in the
// dashboard is cut off within a heartbeat instead of when its certificate
// expires (docs/wiki/concepts/deleted-devices.md).
package blocklist

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/slackhq/nebula/cert"
)

// Entry is one certificate: its Nebula fingerprint, and the Unix time after
// which it needs no blocking because Nebula refuses it as expired (its
// NotAfter plus the clock-skew tolerance of vendor patch 25).
type Entry struct {
	Fingerprint string `json:"fingerprint"`
	Until       int64  `json:"until"`
}

// MaxEntries bounds a list. A network has at most 254 devices, and a
// deleted one holds at most a few certificates that have not expired.
const MaxEntries = 4096

// Check refuses a list that Nebula must not be given: too long, or with an
// entry that is not a fingerprint.
func Check(list []Entry) error {
	if len(list) > MaxEntries {
		return fmt.Errorf("%d entries, at most %d", len(list), MaxEntries)
	}
	for i, e := range list {
		if !validFingerprint(e.Fingerprint) {
			return fmt.Errorf("entry %d: %q is not a certificate fingerprint", i, e.Fingerprint)
		}
		if e.Until <= 0 {
			return fmt.Errorf("entry %d: no time until which to block it", i)
		}
	}
	return nil
}

// validFingerprint: lowercase hex of a SHA-256, as Nebula writes it. Nebula
// compares the strings, so an upper-case one would block nothing.
func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Active is the fingerprints of list that still need blocking at now,
// sorted and without repeats, and never own: a device's Nebula never
// refuses its own certificate. Every other device still refuses one the
// server lists by mistake.
func Active(list []Entry, now time.Time, own string) []string {
	var out []string
	for _, e := range list {
		if e.Until > now.Unix() && e.Fingerprint != own {
			out = append(out, e.Fingerprint)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Fingerprint is the fingerprint Nebula gives the PEM certificate, and when
// it expires.
func Fingerprint(certPEM []byte) (string, time.Time, error) {
	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return "", time.Time{}, err
	}
	fp, err := c.Fingerprint()
	if err != nil {
		return "", time.Time{}, err
	}
	if fp == "" {
		return "", time.Time{}, errors.New("the certificate has no fingerprint")
	}
	return fp, c.NotAfter(), nil
}
