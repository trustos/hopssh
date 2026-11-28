package blocklist

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"

	"github.com/trustos/hopssh/internal/pki"
)

// TestFingerprintIsNebulas: the fingerprint is the one Nebula's blocklist
// compares, so a device given it refuses the certificate.
func TestFingerprintIsNebulas(t *testing.T) {
	ca, err := pki.GenerateCA("test", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	node, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, "node", netip.MustParsePrefix("10.42.9.7/24"), []string{"node"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.IssueCert(ca.CertPEM, ca.KeyPEM, "other", netip.MustParsePrefix("10.42.9.8/24"), []string{"node"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fp, notAfter, err := Fingerprint(node.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !validFingerprint(fp) {
		t.Errorf("fingerprint %q is not 64 lowercase hex", fp)
	}
	if d := time.Until(notAfter); d < 59*time.Minute || d > time.Hour {
		t.Errorf("not after %v; want an hour from now", notAfter)
	}
	pool, err := cert.NewCAPoolFromPEM(ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool.BlocklistFingerprint(fp)
	parse := func(pem []byte) cert.Certificate {
		c, _, err := cert.UnmarshalCertificateFromPEM(pem)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, err := pool.VerifyCertificate(time.Now(), parse(node.CertPEM)); !errors.Is(err, cert.ErrBlockListed) {
		t.Errorf("Nebula verifies a blocklisted cert with %v; want %v", err, cert.ErrBlockListed)
	}
	if _, err := pool.VerifyCertificate(time.Now(), parse(other.CertPEM)); err != nil {
		t.Errorf("another cert of the network: %v", err)
	}
	if _, _, err := Fingerprint([]byte("not a cert")); err == nil {
		t.Error("a fingerprint of no cert")
	}
}

func TestCheck(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	ok := []Entry{{fp, 1}}
	if err := Check(ok); err != nil {
		t.Errorf("a good list: %v", err)
	}
	if err := Check(nil); err != nil {
		t.Errorf("an empty list: %v", err)
	}
	for name, list := range map[string][]Entry{
		"upper case": {{strings.ToUpper(fp), 1}},
		"short":      {{fp[:62], 1}},
		"long":       {{fp + "ab", 1}},
		"not hex":    {{strings.Repeat("zz", 32), 1}},
		"empty":      {{"", 1}},
		"no until":   {{fp, 0}},
		"too many":   slices.Repeat(ok, MaxEntries+1),
	} {
		if err := Check(list); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Check(slices.Repeat(ok, MaxEntries)); err != nil {
		t.Errorf("%d entries: %v", MaxEntries, err)
	}
}

func TestActive(t *testing.T) {
	now := time.Unix(1000, 0)
	a, b, own := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	got := Active([]Entry{{b, 2000}, {a, 2000}, {b, 3000}, {own, 2000}, {strings.Repeat("d", 64), 1000}, {strings.Repeat("e", 64), 999}}, now, own)
	if want := []string{a, b}; !slices.Equal(got, want) {
		t.Errorf("Active = %q; want %q (sorted, once each, not own, not expired)", got, want)
	}
	if got := Active(nil, now, own); len(got) != 0 {
		t.Errorf("Active of nothing = %q", got)
	}
}
