package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// withTempConfigDir swaps the package-level configDir for the duration
// of a test so the migration helpers (which assume a real config tree)
// operate against an isolated tempdir.
func withTempConfigDir(t *testing.T) string {
	t.Helper()
	prev := configDir
	dir := t.TempDir()
	configDir = dir
	t.Cleanup(func() { configDir = prev })
	return dir
}

func writeFakeNebulaYAML(t *testing.T, dir string, port int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"listen": map[string]any{
			"host": "0.0.0.0",
			"port": port,
		},
		"cipher": "aes",
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nebula.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func readListenPortYAML(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "nebula.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	listen, _ := cfg["listen"].(map[string]any)
	if listen == nil {
		return -1
	}
	switch v := listen["port"].(type) {
	case int:
		return v
	default:
		return -1
	}
}

func TestHealListenPortYAML_FixesDriftedPort(t *testing.T) {
	dir := withTempConfigDir(t)
	enrollDir := filepath.Join(dir, "home")
	writeFakeNebulaYAML(t, enrollDir, 4242) // on-disk port doesn't match enrollment

	e := &Enrollment{Name: "home", ListenPort: 4243}
	if err := healListenPortYAML(e); err != nil {
		t.Fatalf("healListenPortYAML: %v", err)
	}
	if got := readListenPortYAML(t, enrollDir); got != 4243 {
		t.Errorf("yaml port after heal: got %d want 4243", got)
	}
}

func TestHealListenPortYAML_NoOpWhenInSync(t *testing.T) {
	dir := withTempConfigDir(t)
	enrollDir := filepath.Join(dir, "home")
	writeFakeNebulaYAML(t, enrollDir, 4242)
	statBefore, err := os.Stat(filepath.Join(enrollDir, "nebula.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	e := &Enrollment{Name: "home", ListenPort: 4242}
	if err := healListenPortYAML(e); err != nil {
		t.Fatalf("healListenPortYAML: %v", err)
	}
	statAfter, err := os.Stat(filepath.Join(enrollDir, "nebula.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("yaml was rewritten despite being in sync (mtime changed)")
	}
}

func TestHealListenPortYAML_PreservesOtherKeys(t *testing.T) {
	dir := withTempConfigDir(t)
	enrollDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(enrollDir, 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`pki:
  ca: /etc/hop-agent/home/ca.crt
listen:
  host: 0.0.0.0
  port: 4242
cipher: aes
firewall:
  outbound:
    - port: any
      proto: any
      host: any
`)
	if err := os.WriteFile(filepath.Join(enrollDir, "nebula.yaml"), original, 0644); err != nil {
		t.Fatal(err)
	}

	e := &Enrollment{Name: "home", ListenPort: 4243}
	if err := healListenPortYAML(e); err != nil {
		t.Fatalf("healListenPortYAML: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(enrollDir, "nebula.yaml"))
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("yaml after heal: %v", err)
	}

	// Listen port updated.
	listen, _ := cfg["listen"].(map[string]any)
	if got, _ := listen["port"].(int); got != 4243 {
		t.Errorf("listen.port: got %d want 4243", got)
	}
	// Other top-level keys still present.
	for _, k := range []string{"pki", "cipher", "firewall"} {
		if _, ok := cfg[k]; !ok {
			t.Errorf("key %q missing after heal", k)
		}
	}
}

func TestHealListenPortYAML_MissingFile(t *testing.T) {
	withTempConfigDir(t)
	e := &Enrollment{Name: "ghost", ListenPort: 4242}
	err := healListenPortYAML(e)
	if err == nil {
		t.Errorf("expected error for missing nebula.yaml, got nil")
	}
}

func TestMigrateListenPorts_AssignsAndHealsAll(t *testing.T) {
	dir := withTempConfigDir(t)

	// Two legacy enrollments: both have ListenPort=0 in registry, both
	// have on-disk nebula.yaml with port 4242 (the pre-fix collision case).
	reg, _ := loadEnrollmentRegistry(dir)
	if err := reg.Add(&Enrollment{Name: "home"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	writeFakeNebulaYAML(t, filepath.Join(dir, "home"), 4242)
	writeFakeNebulaYAML(t, filepath.Join(dir, "work"), 4242)

	migrateListenPorts(reg)

	// Registry got unique ports.
	got := map[string]int{}
	for _, e := range reg.List() {
		got[e.Name] = e.ListenPort
	}
	if got["home"] == 0 || got["work"] == 0 {
		t.Fatalf("ports not assigned: %+v", got)
	}
	if got["home"] == got["work"] {
		t.Fatalf("ports not unique: home=%d work=%d", got["home"], got["work"])
	}

	// Both YAMLs updated to match.
	if p := readListenPortYAML(t, filepath.Join(dir, "home")); p != got["home"] {
		t.Errorf("home yaml port: got %d want %d", p, got["home"])
	}
	if p := readListenPortYAML(t, filepath.Join(dir, "work")); p != got["work"] {
		t.Errorf("work yaml port: got %d want %d", p, got["work"])
	}

	// Persisted: reload registry, ports survive.
	reg2, _ := loadEnrollmentRegistry(dir)
	for _, e := range reg2.List() {
		if e.ListenPort != got[e.Name] {
			t.Errorf("after reload, %s ListenPort=%d want %d", e.Name, e.ListenPort, got[e.Name])
		}
	}
}

func TestMigrateListenPorts_PreservesAlreadyMigrated(t *testing.T) {
	dir := withTempConfigDir(t)
	reg, _ := loadEnrollmentRegistry(dir)
	_ = reg.Add(&Enrollment{Name: "home", ListenPort: 4242})
	_ = reg.Add(&Enrollment{Name: "work", ListenPort: 4243})
	writeFakeNebulaYAML(t, filepath.Join(dir, "home"), 4242)
	writeFakeNebulaYAML(t, filepath.Join(dir, "work"), 4243)

	migrateListenPorts(reg)

	for _, e := range reg.List() {
		if e.Name == "home" && e.ListenPort != 4242 {
			t.Errorf("home port changed: %d", e.ListenPort)
		}
		if e.Name == "work" && e.ListenPort != 4243 {
			t.Errorf("work port changed: %d", e.ListenPort)
		}
	}
}
