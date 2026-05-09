package client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNormaliseRoutes covers dedup + sort + trim behaviour. The on-disk
// state file uses normalised form so route-equality can be a
// position-by-position string compare.
func TestNormaliseRoutes(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"single", []string{"10.0.0.0/16"}, []string{"10.0.0.0/16"}},
		{
			"sorted output",
			[]string{"192.168.50.0/24", "10.0.0.0/16"},
			[]string{"10.0.0.0/16", "192.168.50.0/24"},
		},
		{
			"dedupe",
			[]string{"10.0.0.0/16", "10.0.0.0/16", "10.0.0.0/16"},
			[]string{"10.0.0.0/16"},
		},
		{
			"trim whitespace",
			[]string{"  10.0.0.0/16  ", "192.168.50.0/24"},
			[]string{"10.0.0.0/16", "192.168.50.0/24"},
		},
		{
			"drop empty",
			[]string{"", " ", "10.0.0.0/16"},
			[]string{"10.0.0.0/16"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normaliseRoutes(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normaliseRoutes(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRoutesEqual checks the change-detection helper. Equal-content
// sorted slices = equal; any mismatch = not equal.
func TestRoutesEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"nil vs empty", nil, []string{}, true},
		{"same order", []string{"10.0.0.0/16"}, []string{"10.0.0.0/16"}, true},
		{"different length", []string{"10.0.0.0/16"}, []string{"10.0.0.0/16", "192.168.0.0/16"}, false},
		{"different content", []string{"10.0.0.0/16"}, []string{"10.1.0.0/16"}, false},
		// We assume both inputs are normalised. Non-normalised inputs
		// are caller's bug — no test for that.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routesEqual(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("routesEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestPersistRoutes_RoundTrip writes routes to a tempdir's enrollment
// dir, then reads them back. Verifies the disk format survives a
// process restart cleanly.
func TestPersistRoutes_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir // routes live under inst.dir()

	want := []string{"10.0.0.0/16", "192.168.50.0/24"}
	if err := persistRoutes(inst, want); err != nil {
		t.Fatalf("persistRoutes: %v", err)
	}

	got := loadPersistedRoutes(inst)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loadPersistedRoutes = %v, want %v", got, want)
	}

	// Empty persist removes the file (next-startup-loads-nil contract).
	if err := persistRoutes(inst, nil); err != nil {
		t.Fatalf("persistRoutes(nil): %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.dir(), routesStateFile)); !os.IsNotExist(err) {
		t.Errorf("routes.json still exists after persistRoutes(nil): err=%v", err)
	}
}

// TestRewriteNebulaUnsafeRoutes_AddsBlock verifies adding routes to a
// vanilla nebula.yaml inserts the expected unsafe_routes structure
// without disturbing existing keys. Regression test against a future
// refactor that might serialize the block in a way Nebula doesn't
// accept.
func TestRewriteNebulaUnsafeRoutes_AddsBlock(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	// Minimal Nebula config that our rewriter must preserve verbatim.
	original := `
pki:
  ca: /tmp/ca.crt
  cert: /tmp/node.crt

cipher: aes

tun:
  user: false
  dev: utun99
  mtu: 1300
`
	if err := os.WriteFile(filepath.Join(dir, "nebula.yaml"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := rewriteNebulaUnsafeRoutes(inst, []string{"10.0.0.0/16", "192.168.50.0/24"}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(dir, "nebula.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output yaml unparseable: %v\n%s", err, out)
	}
	tunBlock, ok := cfg["tun"].(map[string]any)
	if !ok {
		t.Fatal("tun: block missing")
	}
	// Original keys preserved.
	if tunBlock["dev"] != "utun99" {
		t.Errorf("tun.dev = %v, want utun99", tunBlock["dev"])
	}
	if tunBlock["mtu"] != 1300 {
		t.Errorf("tun.mtu = %v, want 1300", tunBlock["mtu"])
	}
	// New unsafe_routes block.
	routes, ok := tunBlock["unsafe_routes"].([]any)
	if !ok {
		t.Fatalf("unsafe_routes missing or wrong type: %T", tunBlock["unsafe_routes"])
	}
	if len(routes) != 2 {
		t.Fatalf("len(unsafe_routes) = %d, want 2", len(routes))
	}
	for i, want := range []string{"10.0.0.0/16", "192.168.50.0/24"} {
		entry := routes[i].(map[string]any)
		if entry["route"] != want {
			t.Errorf("unsafe_routes[%d].route = %v, want %s", i, entry["route"], want)
		}
	}
}

// TestRewriteNebulaUnsafeRoutes_RemovesBlock verifies that an empty
// route list strips the unsafe_routes key from nebula.yaml entirely.
// Without this, a node that was a gateway and is later un-gatewayed
// would keep the stale unsafe_routes block in its config.
func TestRewriteNebulaUnsafeRoutes_RemovesBlock(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	withRoutes := `
tun:
  dev: utun99
  unsafe_routes:
    - route: 10.0.0.0/16
`
	if err := os.WriteFile(filepath.Join(dir, "nebula.yaml"), []byte(withRoutes), 0644); err != nil {
		t.Fatal(err)
	}

	if err := rewriteNebulaUnsafeRoutes(inst, nil); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	out, _ := os.ReadFile(filepath.Join(dir, "nebula.yaml"))
	var cfg map[string]any
	_ = yaml.Unmarshal(out, &cfg)
	tunBlock, _ := cfg["tun"].(map[string]any)
	if _, present := tunBlock["unsafe_routes"]; present {
		t.Errorf("unsafe_routes still present after empty rewrite: %v", tunBlock)
	}
}
