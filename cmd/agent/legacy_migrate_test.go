package main

import (
	"os"
	"path/filepath"
	"testing"
)

// legacyFixture writes a minimal legacy config layout to dir with the
// given DNS domain. Set dnsDomain to "" to simulate an enrollment with
// no mesh DNS.
func legacyFixture(t *testing.T, dir, dnsDomain string) {
	t.Helper()
	files := map[string]string{
		"ca.crt":     "-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n",
		"node.crt":   "-----BEGIN CERTIFICATE-----\nFAKENODE\n-----END CERTIFICATE-----\n",
		"node.key":   "-----BEGIN PRIVATE KEY-----\nFAKEKEY\n-----END PRIVATE KEY-----\n",
		"token":      "agent-token-xyz",
		"endpoint":   "https://hopssh.com",
		"node-id":    "node-123",
		"nebula.yaml": "pki: {}\n",
		"tun-mode":   "kernel",
	}
	if dnsDomain != "" {
		files["dns-domain"] = dnsDomain
		files["dns-server"] = "192.0.2.10:15300"
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestMigrateLegacy_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	e, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if e != nil {
		t.Fatalf("expected nil enrollment, got %+v", e)
	}
	if _, err := os.Stat(filepath.Join(dir, enrollmentsFile)); !os.IsNotExist(err) {
		t.Fatalf("enrollments.json should not exist after migrate on fresh install: err=%v", err)
	}
}

func TestMigrateLegacy_AlreadyMigrated(t *testing.T) {
	dir := t.TempDir()
	// Simulate an already-migrated dir by writing an empty registry.
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "existing"}); err != nil {
		t.Fatal(err)
	}
	// Also drop a legacy file that should NOT trigger migration.
	_ = os.WriteFile(filepath.Join(dir, "node.crt"), []byte("stale"), 0600)

	e, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatalf("already migrated: %v", err)
	}
	if e != nil {
		t.Fatalf("expected nil when already migrated, got %+v", e)
	}
	// The stale legacy node.crt was left alone — we don't touch it.
	if _, err := os.Stat(filepath.Join(dir, "node.crt")); err != nil {
		t.Fatalf("stale node.crt should still be there: %v", err)
	}
}

func TestMigrateLegacy_DNSDomainAsName(t *testing.T) {
	dir := t.TempDir()
	legacyFixture(t, dir, "home")

	e, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if e == nil {
		t.Fatal("expected enrollment")
	}
	if e.Name != "home" {
		t.Errorf("expected name=home, got %q", e.Name)
	}
	if e.NodeID != "node-123" {
		t.Errorf("expected nodeId=node-123, got %q", e.NodeID)
	}
	if e.Endpoint != "https://hopssh.com" {
		t.Errorf("endpoint: got %q", e.Endpoint)
	}
	if e.TunMode != "kernel" {
		t.Errorf("tunMode: got %q", e.TunMode)
	}
	if e.DNSDomain != "home" {
		t.Errorf("dnsDomain: got %q", e.DNSDomain)
	}
	if e.CAFingerprint == "" || len(e.CAFingerprint) != 12 {
		t.Errorf("caFingerprint: got %q", e.CAFingerprint)
	}

	// Files moved into subdir.
	subdir := filepath.Join(dir, "home")
	for _, name := range []string{"ca.crt", "node.crt", "node.key", "token", "endpoint", "node-id", "nebula.yaml", "tun-mode", "dns-domain", "dns-server"} {
		if _, err := os.Stat(filepath.Join(subdir, name)); err != nil {
			t.Errorf("expected %s in subdir: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed from top level: err=%v", name, err)
		}
	}
	// enrollments.json at top level.
	if _, err := os.Stat(filepath.Join(dir, enrollmentsFile)); err != nil {
		t.Errorf("enrollments.json missing: %v", err)
	}
}

func TestMigrateLegacy_NoDNSDomainUsesFingerprint(t *testing.T) {
	dir := t.TempDir()
	legacyFixture(t, dir, "") // no dns-domain

	e, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if e == nil {
		t.Fatal("expected enrollment")
	}
	// Name should match the CA fingerprint.
	if e.Name != e.CAFingerprint {
		t.Errorf("expected name=fingerprint, got name=%q fingerprint=%q", e.Name, e.CAFingerprint)
	}
	if len(e.Name) != 12 {
		t.Errorf("expected 12-char name, got %q", e.Name)
	}
	subdir := filepath.Join(dir, e.Name)
	if _, err := os.Stat(filepath.Join(subdir, "node.crt")); err != nil {
		t.Errorf("expected node.crt in subdir: %v", err)
	}
}

func TestMigrateLegacy_OptionalFilesMissing(t *testing.T) {
	dir := t.TempDir()
	// Write only the required files — skip the optional ones.
	required := map[string]string{
		"ca.crt":   "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n",
		"node.crt": "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n",
		"node.key": "-----BEGIN PRIVATE KEY-----\nFAKE\n-----END PRIVATE KEY-----\n",
		"token":    "x",
		"endpoint": "https://hopssh.com",
	}
	for name, content := range required {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	e, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if e == nil {
		t.Fatal("expected enrollment")
	}
	// Name falls back to fingerprint since dns-domain is absent.
	if e.Name != e.CAFingerprint {
		t.Errorf("unexpected name=%q fp=%q", e.Name, e.CAFingerprint)
	}
	if e.DNSDomain != "" {
		t.Errorf("dnsDomain should be empty, got %q", e.DNSDomain)
	}
}

func TestMigrateLegacy_RequiredFileMissing(t *testing.T) {
	dir := t.TempDir()
	// Only node.crt (triggers migration) but missing ca.crt (required for fingerprint).
	if err := os.WriteFile(filepath.Join(dir, "node.crt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := migrateLegacyLayout(dir)
	if err == nil {
		t.Fatal("expected error when ca.crt is missing")
	}
}

func TestMigrateLegacy_Idempotent(t *testing.T) {
	dir := t.TempDir()
	legacyFixture(t, dir, "work")

	e1, err := migrateLegacyLayout(dir)
	if err != nil || e1 == nil {
		t.Fatalf("first migrate: e=%v err=%v", e1, err)
	}
	e2, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if e2 != nil {
		t.Fatalf("second migrate should be no-op, got %+v", e2)
	}
	// Registry still has exactly one enrollment.
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 1 {
		t.Fatalf("expected 1 enrollment, got %d", reg.Len())
	}
}

func TestMigrateLegacy_RegistryReflectsMigration(t *testing.T) {
	dir := t.TempDir()
	legacyFixture(t, dir, "prod")

	_, err := migrateLegacyLayout(dir)
	if err != nil {
		t.Fatal(err)
	}

	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 1 {
		t.Fatalf("expected 1 enrollment in registry, got %d", reg.Len())
	}
	e := reg.Get("prod")
	if e == nil {
		t.Fatal("expected 'prod' enrollment in registry")
	}
	if e.NodeID != "node-123" || e.Endpoint != "https://hopssh.com" {
		t.Errorf("registry entry mismatch: %+v", e)
	}
}
