package client

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/slackhq/nebula/config"
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

// preferredRanges is what Nebula gets out of a config file.
func preferredRanges(t *testing.T, path string) []string {
	t.Helper()
	c := config.NewC(slog.New(slog.DiscardHandler))
	if err := c.Load(path); err != nil {
		t.Fatal(err)
	}
	if c.IsSet("lighthouse.preferred_ranges") {
		t.Error("preferred_ranges is still under lighthouse, where Nebula does not look")
	}
	return c.GetStringSlice("preferred_ranges", nil)
}

var wantRanges = []string{"192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}

// From v0.9.1 to v0.11.27 the ranges were written where Nebula doesn't read them,
// so two machines of one LAN could stay on the way around through the router.
func TestPreferredRanges_NewConfig(t *testing.T) {
	dir := t.TempDir()
	writeNebulaConfig(dir, "10.42.1.1", "127.0.0.1", 42001, "kernel", 4242)
	path := filepath.Join(dir, "nebula.yaml")
	if got := preferredRanges(t, path); !slices.Equal(got, wantRanges) {
		t.Errorf("enrollment wrote preferred ranges %v", got)
	}
	var cfg map[string]any
	data, _ := os.ReadFile(path)
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	lighthouse, _ := cfg["lighthouse"].(map[string]any)
	if hosts, _ := lighthouse["hosts"].([]any); len(hosts) != 1 {
		t.Errorf("lighthouse hosts: %v", lighthouse["hosts"])
	}
	if relay, _ := cfg["relay"].(map[string]any); relay["use_relays"] != true {
		t.Errorf("relay: %v", cfg["relay"])
	}
}

func TestPreferredRanges_OldConfig(t *testing.T) {
	own := []string{"172.20.0.0/16"}
	for _, c := range []struct {
		name, yaml string
		want       []string
	}{
		{"where it used to be", "lighthouse:\n  hosts: [10.42.1.1]\n  preferred_ranges: [192.168.0.0/16, 172.16.0.0/12, 10.0.0.0/8]\n", wantRanges},
		{"somebody's own, where it used to be", "lighthouse:\n  hosts: [10.42.1.1]\n  preferred_ranges: [172.20.0.0/16]\n", own},
		{"nothing, where it used to be", "lighthouse:\n  hosts: [10.42.1.1]\n  preferred_ranges:\n", wantRanges},
		{"somebody's own", "preferred_ranges: [172.20.0.0/16]\n", own},
		{"in both places", "preferred_ranges: [172.20.0.0/16]\nlighthouse:\n  preferred_ranges: [192.168.0.0/16]\n", own},
		{"nowhere", "lighthouse:\n  hosts: [10.42.1.1]\n", wantRanges},
		{"no lighthouse", "cipher: aes\n", wantRanges},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "nebula.yaml")
			if err := os.WriteFile(path, []byte(c.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4242})
			inst.customDir = dir
			for _, when := range []string{"first", "second"} {
				ensureP2PConfig(inst)
				if got := preferredRanges(t, path); !slices.Equal(got, c.want) {
					t.Errorf("%s start: preferred ranges %v, want %v", when, got, c.want)
				}
			}
		})
	}
}

// TestEnsureP2PConfig_DropsLocalBroadcast: macOS sends broadcasts into the
// mesh after every network change. Nebula has nobody to deliver them to and
// tries a handshake with x.x.x.255, three log lines a second until it gives up.
func TestEnsureP2PConfig_DropsLocalBroadcast(t *testing.T) {
	for name, tun := range map[string]map[string]any{
		"kernel":    {"dev": "hop-home", "mtu": 1420},
		"userspace": {"user": true},
		"was off":   {"dev": "hop-home", "drop_local_broadcast": false},
	} {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "nebula.yaml")
		data, _ := yaml.Marshal(map[string]any{"pki": map[string]any{}, "tun": tun})
		if err := os.WriteFile(cfgPath, data, 0644); err != nil {
			t.Fatal(err)
		}
		inst := newMeshInstance(&Enrollment{Name: "home"})
		inst.customDir = dir
		ensureP2PConfig(inst)

		c := config.NewC(slog.New(slog.DiscardHandler))
		if err := c.Load(cfgPath); err != nil {
			t.Fatal(err)
		}
		// The key Nebula reads (main.go).
		if !c.GetBool("tun.drop_local_broadcast", false) {
			t.Errorf("%s: tun.drop_local_broadcast is off", name)
		}
	}
}
