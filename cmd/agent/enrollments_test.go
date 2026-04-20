package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentRegistry_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if reg.Len() != 0 {
		t.Fatalf("expected empty registry, got %d", reg.Len())
	}

	e1 := &Enrollment{Name: "home", NodeID: "node-1", Endpoint: "https://hopssh.com", TunMode: "kernel", DNSDomain: "home", EnrolledAt: time.Now().UTC().Truncate(time.Second)}
	e2 := &Enrollment{Name: "work", NodeID: "node-2", Endpoint: "https://hopssh.com", TunMode: "userspace", DNSDomain: "work", EnrolledAt: time.Now().UTC().Truncate(time.Second)}

	if err := reg.Add(e1); err != nil {
		t.Fatalf("add e1: %v", err)
	}
	if err := reg.Add(e2); err != nil {
		t.Fatalf("add e2: %v", err)
	}

	reg2, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reg2.Len() != 2 {
		t.Fatalf("expected 2 enrollments after reload, got %d", reg2.Len())
	}
	if got := reg2.Get("home"); got == nil || got.NodeID != "node-1" {
		t.Fatalf("reloaded home mismatch: %+v", got)
	}
	if got := reg2.Get("work"); got == nil || got.TunMode != "userspace" {
		t.Fatalf("reloaded work mismatch: %+v", got)
	}
}

func TestEnrollmentRegistry_AddRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "home", NodeID: "a"}); err != nil {
		t.Fatal(err)
	}
	err = reg.Add(&Enrollment{Name: "home", NodeID: "b"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if reg.Len() != 1 {
		t.Fatalf("expected 1 enrollment after duplicate, got %d", reg.Len())
	}
}

func TestEnrollmentRegistry_AddRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	cases := []string{"", "UPPER", "has space", "dots.are.out", "slash/is", "toolong-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "-leading-hyphen"}
	for _, name := range cases {
		if err := reg.Add(&Enrollment{Name: name}); err == nil {
			t.Errorf("expected reject for name %q", name)
		}
	}
}

func TestEnrollmentRegistry_Remove(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home"})
	_ = reg.Add(&Enrollment{Name: "work"})

	if err := reg.Remove("missing"); err == nil {
		t.Fatal("expected error removing missing enrollment")
	}
	if err := reg.Remove("home"); err != nil {
		t.Fatalf("remove home: %v", err)
	}
	if reg.Get("home") != nil {
		t.Fatal("expected home to be gone")
	}
	if reg.Get("work") == nil {
		t.Fatal("expected work to remain")
	}

	reg2, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reg2.Len() != 1 {
		t.Fatalf("expected 1 enrollment on reload, got %d", reg2.Len())
	}
}

func TestEnrollmentRegistry_Names_Sorted(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "work"})
	_ = reg.Add(&Enrollment{Name: "home"})
	_ = reg.Add(&Enrollment{Name: "pi"})

	names := reg.Names()
	want := []string{"home", "pi", "work"}
	if len(names) != len(want) {
		t.Fatalf("names len: got %d want %d", len(names), len(want))
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("names[%d]=%q want %q", i, names[i], n)
		}
	}
}

func TestEnrollmentRegistry_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, enrollmentsFile)
	// Write a document with a future version number.
	bad := `{"version":99,"enrollments":[]}`
	if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEnrollmentRegistry(dir)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported version error, got %v", err)
	}
}

func TestEnrollmentRegistry_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, enrollmentsFile)
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEnrollmentRegistry(dir)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestEnrollmentRegistry_FileFormat(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home", NodeID: "abc", Endpoint: "https://hopssh.com", TunMode: "kernel"})

	data, err := os.ReadFile(filepath.Join(dir, enrollmentsFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc enrollmentRegistrySchema
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse on-disk doc: %v", err)
	}
	if doc.Version != enrollmentRegistryVersion {
		t.Fatalf("on-disk version %d", doc.Version)
	}
	if len(doc.Enrollments) != 1 || doc.Enrollments[0].Name != "home" {
		t.Fatalf("on-disk enrollments: %+v", doc.Enrollments)
	}
}

func TestEnrollmentRegistry_NextAvailableListenPort_Empty(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	got := reg.NextAvailableListenPort(4242)
	if got != 4242 {
		t.Fatalf("empty registry should return base port; got %d", got)
	}
}

func TestEnrollmentRegistry_NextAvailableListenPort_Sequential(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	if err := reg.Add(&Enrollment{Name: "home", ListenPort: 4242}); err != nil {
		t.Fatal(err)
	}
	if got := reg.NextAvailableListenPort(4242); got != 4243 {
		t.Errorf("after 4242, want 4243, got %d", got)
	}
	if err := reg.Add(&Enrollment{Name: "work", ListenPort: 4243}); err != nil {
		t.Fatal(err)
	}
	if got := reg.NextAvailableListenPort(4242); got != 4244 {
		t.Errorf("after 4242+4243, want 4244, got %d", got)
	}
}

func TestEnrollmentRegistry_NextAvailableListenPort_FillsHole(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	// Simulate user enrolled then removed the middle one — the hole at
	// 4243 should be reusable on the next enroll.
	_ = reg.Add(&Enrollment{Name: "home", ListenPort: 4242})
	_ = reg.Add(&Enrollment{Name: "lab", ListenPort: 4244})
	if got := reg.NextAvailableListenPort(4242); got != 4243 {
		t.Errorf("with hole at 4243, want 4243, got %d", got)
	}
}

func TestEnrollmentRegistry_NextAvailableListenPort_IgnoresZero(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	// ListenPort=0 means "not yet assigned" — it shouldn't reserve 4242.
	_ = reg.Add(&Enrollment{Name: "legacy"})
	if got := reg.NextAvailableListenPort(4242); got != 4242 {
		t.Errorf("zero ListenPort shouldn't block 4242; got %d", got)
	}
}

func TestEnrollmentRegistry_AssignMissingListenPorts(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "legacy1"})
	_ = reg.Add(&Enrollment{Name: "legacy2"})
	_ = reg.Add(&Enrollment{Name: "modern", ListenPort: 4242}) // already assigned

	updated, err := reg.AssignMissingListenPorts(4242)
	if err != nil {
		t.Fatalf("AssignMissingListenPorts: %v", err)
	}
	if updated != 2 {
		t.Errorf("updated count: got %d want 2", updated)
	}

	// All ports must be unique and >= base.
	seen := map[int]bool{}
	for _, e := range reg.List() {
		if e.ListenPort < 4242 {
			t.Errorf("%s ListenPort=%d below base", e.Name, e.ListenPort)
		}
		if seen[e.ListenPort] {
			t.Errorf("duplicate port %d on enrollment %s", e.ListenPort, e.Name)
		}
		seen[e.ListenPort] = true
	}

	// Persisted to disk: reload and re-check.
	reg2, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, e := range reg2.List() {
		if e.ListenPort == 0 {
			t.Errorf("after reload, %s still has ListenPort=0", e.Name)
		}
	}
}

func TestEnrollmentRegistry_AssignMissingListenPorts_Idempotent(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home"})

	first, err := reg.AssignMissingListenPorts(4242)
	if err != nil || first != 1 {
		t.Fatalf("first call: updated=%d err=%v", first, err)
	}
	second, err := reg.AssignMissingListenPorts(4242)
	if err != nil || second != 0 {
		t.Errorf("second call should be no-op; updated=%d err=%v", second, err)
	}
}

func TestValidateEnrollmentName(t *testing.T) {
	valid := []string{"a", "home", "work-prod", "abc123", "zero", strings.Repeat("a", 32)}
	for _, name := range valid {
		if err := validateEnrollmentName(name); err != nil {
			t.Errorf("valid name %q rejected: %v", name, err)
		}
	}
	invalid := []string{"", "A", "Home", "has space", "has.dot", "has/slash", "-leading", strings.Repeat("a", 33), "enrollments.json", "con", "nul", "com1", "lpt9", "aux", "prn"}
	for _, name := range invalid {
		if err := validateEnrollmentName(name); err == nil {
			t.Errorf("invalid name %q accepted", name)
		}
	}
}

func TestCAFingerprint_Deterministic(t *testing.T) {
	pem := []byte("-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----\n")
	a := caFingerprint(pem)
	b := caFingerprint(pem)
	if a != b {
		t.Fatalf("caFingerprint not deterministic: %q vs %q", a, b)
	}
	if len(a) != 12 {
		t.Fatalf("caFingerprint len %d want 12", len(a))
	}
}

func TestDefaultEnrollmentName(t *testing.T) {
	// Valid DNS domain → returned as-is.
	if got := defaultEnrollmentName("home", "abcdef123456"); got != "home" {
		t.Errorf("dns domain path: got %q", got)
	}
	// DNS domain invalid as enrollment name → fall back to fingerprint.
	if got := defaultEnrollmentName("Has.Dots", "abcdef123456"); got != "abcdef123456" {
		t.Errorf("fingerprint fallback: got %q", got)
	}
	// Empty DNS domain → fingerprint.
	if got := defaultEnrollmentName("", "abcdef123456"); got != "abcdef123456" {
		t.Errorf("empty dns: got %q", got)
	}
}

func TestEnrollmentDir(t *testing.T) {
	if got := enrollmentDir("/etc/hop-agent", "home"); got != "/etc/hop-agent/home" {
		t.Errorf("got %q", got)
	}
}

func TestEnrollmentRegistry_BackupWrittenOnSave(t *testing.T) {
	dir := t.TempDir()
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "home", NodeID: "abc"}); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, enrollmentsBackupFile)
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup not written after Add: %v", err)
	}
	// Backup content should match main after a save.
	mainBytes, _ := os.ReadFile(filepath.Join(dir, enrollmentsFile))
	bakBytes, _ := os.ReadFile(backupPath)
	if string(mainBytes) != string(bakBytes) {
		t.Fatal("backup bytes differ from main immediately after save")
	}
}

func TestEnrollmentRegistry_FallbackToBackupOnCorruptMain(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home", NodeID: "abc"})
	_ = reg.Add(&Enrollment{Name: "work", NodeID: "def"})

	// Truncate main, leave backup intact.
	mainPath := filepath.Join(dir, enrollmentsFile)
	if err := os.WriteFile(mainPath, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}

	recovered, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got err: %v", err)
	}
	if recovered.Len() != 2 {
		t.Fatalf("expected 2 enrollments from backup, got %d", recovered.Len())
	}
	if recovered.Get("home") == nil || recovered.Get("work") == nil {
		t.Fatal("recovered registry missing expected entries")
	}
}

func TestEnrollmentRegistry_FallbackWhenMainMissingButBackupPresent(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home", NodeID: "abc"})

	// Simulate main.go getting clobbered (manual rm, etc.) while backup survives.
	if err := os.Remove(filepath.Join(dir, enrollmentsFile)); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Len() != 1 || recovered.Get("home") == nil {
		t.Fatalf("expected recovery from backup, got %d entries", recovered.Len())
	}
}

func TestEnrollmentRegistry_BothCorruptReturnsMainError(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home"})

	// Both files exist but both are corrupt.
	if err := os.WriteFile(filepath.Join(dir, enrollmentsFile), []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, enrollmentsBackupFile), []byte("also-garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEnrollmentRegistry(dir)
	if err == nil {
		t.Fatal("expected error when both main and backup are corrupt")
	}
}

func TestExistingEnrollmentForNetwork(t *testing.T) {
	dir := t.TempDir()
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Add(&Enrollment{Name: "home", Endpoint: "https://hopssh.com", CAFingerprint: "aaaa1111"})
	_ = reg.Add(&Enrollment{Name: "work", Endpoint: "https://hopssh.com", CAFingerprint: "bbbb2222"})

	// Same endpoint + same fingerprint → match (caught as duplicate).
	if got := existingEnrollmentForNetwork(reg, "https://hopssh.com", "aaaa1111"); got == nil || got.Name != "home" {
		t.Errorf("expected match on home, got %v", got)
	}
	// Different fingerprint on same endpoint → no match (different network).
	if got := existingEnrollmentForNetwork(reg, "https://hopssh.com", "cccc3333"); got != nil {
		t.Errorf("expected no match, got %v", got)
	}
	// Different endpoint with matching fingerprint → no match (different control plane).
	if got := existingEnrollmentForNetwork(reg, "https://other.example.com", "aaaa1111"); got != nil {
		t.Errorf("expected no match across control planes, got %v", got)
	}
	// Empty fingerprint → never matches (defensive — legacy migrations
	// might produce entries without fingerprint if ca.crt was absent).
	if got := existingEnrollmentForNetwork(reg, "https://hopssh.com", ""); got != nil {
		t.Errorf("expected no match on empty fingerprint, got %v", got)
	}
}
