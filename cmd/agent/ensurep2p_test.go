package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// nebulaYAMLForP2P returns a minimal nebula.yaml that ensureP2PConfig
// can mutate. We include the keys the function actually reads (pki,
// listen, punchy, lighthouse, etc.) so we can observe the mutation
// without tripping unrelated heal paths.
func nebulaYAMLForP2P(listenPort int) []byte {
	cfg := map[string]any{
		"pki": map[string]any{
			"ca":   "/old/path/ca.crt",
			"cert": "/old/path/node.crt",
			"key":  "/old/path/node.key",
		},
		"listen": map[string]any{
			"host": "0.0.0.0",
			"port": listenPort,
		},
		"cipher": "aes",
		"punchy": map[string]any{
			"target_all_remotes": true,
		},
	}
	out, _ := yaml.Marshal(cfg)
	return out
}

func readListenPort(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
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

func TestEnsureP2PConfig_HealsListenPortDrift(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nebula.yaml")
	if err := os.WriteFile(cfgPath, nebulaYAMLForP2P(4242), 0644); err != nil {
		t.Fatal(err)
	}

	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4243})
	inst.customDir = dir

	ensureP2PConfig(inst)

	if got := readListenPort(t, cfgPath); got != 4243 {
		t.Errorf("listen.port after ensureP2PConfig: got %d want 4243", got)
	}
}

func TestEnsureP2PConfig_RespectsAlreadyCorrectListenPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nebula.yaml")
	if err := os.WriteFile(cfgPath, nebulaYAMLForP2P(4243), 0644); err != nil {
		t.Fatal(err)
	}

	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4243})
	inst.customDir = dir

	ensureP2PConfig(inst)

	if got := readListenPort(t, cfgPath); got != 4243 {
		t.Errorf("listen.port: got %d want 4243", got)
	}
}

func TestEnsureP2PConfig_LeavesListenPortAloneWhenEnrollmentZero(t *testing.T) {
	// Pre-migration enrollments have ListenPort=0. The heal path is
	// guarded by `> 0`, so an enrollment without the field shouldn't
	// clobber a working listen.port. Behavioural belt-and-suspenders
	// against a future bug where someone forgets the guard.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nebula.yaml")
	if err := os.WriteFile(cfgPath, nebulaYAMLForP2P(4242), 0644); err != nil {
		t.Fatal(err)
	}

	inst := newMeshInstance(&Enrollment{Name: "home"}) // ListenPort=0
	inst.customDir = dir

	ensureP2PConfig(inst)

	if got := readListenPort(t, cfgPath); got != 4242 {
		t.Errorf("listen.port should not be touched; got %d want 4242", got)
	}
}

func TestEnsureP2PConfig_ScrubsBufferKeys(t *testing.T) {
	// Sanity: pre-existing scrub of read_buffer/write_buffer still works
	// alongside the new listen.port heal.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nebula.yaml")
	cfg := map[string]any{
		"pki": map[string]any{},
		"listen": map[string]any{
			"host":         "0.0.0.0",
			"port":         4242,
			"read_buffer":  2 << 20,
			"write_buffer": 2 << 20,
		},
		"cipher": "aes",
	}
	data, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4242})
	inst.customDir = dir

	ensureP2PConfig(inst)

	data, _ = os.ReadFile(cfgPath)
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	listen, _ := got["listen"].(map[string]any)
	if _, ok := listen["read_buffer"]; ok {
		t.Errorf("read_buffer still present after ensureP2PConfig")
	}
	if _, ok := listen["write_buffer"]; ok {
		t.Errorf("write_buffer still present after ensureP2PConfig")
	}
}
