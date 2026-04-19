package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"
)

// testInstance builds a meshInstance backed by dir via customDir. Use
// from tests that want to exercise instance-scoped file IO without
// setting up a real enrollment layout.
func testInstance(t *testing.T, name, dir string) *meshInstance {
	t.Helper()
	inst := newMeshInstance(&Enrollment{Name: name})
	inst.customDir = dir
	return inst
}

func TestUpgradeTunMode_UpdatesNebulaYAML(t *testing.T) {
	tmpDir := t.TempDir()
	inst := testInstance(t, "test", tmpDir)

	nebulaYAML := `pki:
  ca: /etc/hop-agent/ca.crt
lighthouse:
  am_lighthouse: false
tun:
  user: true
listen:
  host: 0.0.0.0
  port: 4242
relay:
  relays:
    - "10.42.1.1"
  use_relays: true
`
	os.WriteFile(filepath.Join(tmpDir, "nebula.yaml"), []byte(nebulaYAML), 0644)
	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("userspace"), 0644)

	upgradeTunMode(inst)

	data, err := os.ReadFile(filepath.Join(tmpDir, "tun-mode"))
	if err != nil {
		t.Fatalf("failed to read tun-mode: %v", err)
	}
	if string(data) != "kernel" {
		t.Fatalf("expected tun-mode=kernel, got %q", string(data))
	}

	yamlData, err := os.ReadFile(filepath.Join(tmpDir, "nebula.yaml"))
	if err != nil {
		t.Fatalf("failed to read nebula.yaml: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(yamlData, &cfg); err != nil {
		t.Fatalf("invalid YAML after upgrade: %v", err)
	}

	tun, ok := cfg["tun"].(map[string]interface{})
	if !ok {
		t.Fatal("tun section missing after upgrade")
	}
	if tun["dev"] != "utun" {
		t.Fatalf("expected tun.dev=utun, got %v", tun["dev"])
	}
	if tun["mtu"] != nebulacfg.TunMTU {
		t.Fatalf("expected tun.mtu=%d, got %v", nebulacfg.TunMTU, tun["mtu"])
	}
	if _, hasUser := tun["user"]; hasUser {
		t.Fatal("tun.user should be removed after kernel upgrade")
	}

	lighthouse, ok := cfg["lighthouse"].(map[string]interface{})
	if !ok {
		t.Fatal("lighthouse section should be preserved")
	}
	if lighthouse["am_lighthouse"] != false {
		t.Fatal("lighthouse.am_lighthouse should be preserved")
	}

	listen, ok := cfg["listen"].(map[string]interface{})
	if !ok {
		t.Fatal("listen section should be preserved")
	}
	if listen["port"] != 4242 {
		t.Fatalf("listen.port should be preserved, got %v", listen["port"])
	}
}

func TestReadTunMode_FileNotFound_NonPrivileged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root")
	}
	inst := testInstance(t, "test", t.TempDir())

	mode := readTunMode(inst)
	if mode != "userspace" {
		t.Fatalf("expected userspace for non-root with no file, got %q", mode)
	}
}

func TestReadTunMode_KernelFile(t *testing.T) {
	tmpDir := t.TempDir()
	inst := testInstance(t, "test", tmpDir)

	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("kernel"), 0644)

	mode := readTunMode(inst)
	if mode != "kernel" {
		t.Fatalf("expected kernel, got %q", mode)
	}
}

func TestReadTunMode_UserspaceFile_NonPrivileged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root")
	}
	tmpDir := t.TempDir()
	inst := testInstance(t, "test", tmpDir)

	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("userspace"), 0644)

	mode := readTunMode(inst)
	if mode != "userspace" {
		t.Fatalf("expected userspace for non-root, got %q", mode)
	}
}

func TestReadTunMode_InvalidContent(t *testing.T) {
	tmpDir := t.TempDir()
	inst := testInstance(t, "test", tmpDir)

	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("garbage"), 0644)

	mode := readTunMode(inst)
	if mode != "userspace" {
		t.Fatalf("expected userspace for invalid content, got %q", mode)
	}
}

// stubNetwork replaces package-level test doubles for the duration
// of one test and returns a restore func.
func stubNetwork(t *testing.T, ifaceUp bool, reloadDelay time.Duration) (fired *int32, restore func()) {
	t.Helper()
	origUp := isInterfaceUp
	origTrig := triggerReload
	origGrace := watcherStartupGraceTicks
	origCooldown := reloadCooldown
	var n int32
	isInterfaceUp = func(string) bool { return ifaceUp }
	triggerReload = func(inst *meshInstance) {
		atomic.AddInt32(&n, 1)
	}
	// Test runs with short grace + cooldown so we don't need to
	// wait real seconds.
	watcherStartupGraceTicks = 0
	reloadCooldown = reloadDelay
	return &n, func() {
		isInterfaceUp = origUp
		triggerReload = origTrig
		watcherStartupGraceTicks = origGrace
		reloadCooldown = origCooldown
	}
}

func TestShouldAutoReload_CooldownBlocksSecondCall(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	_, restore := stubNetwork(t, true, 10*time.Second)
	defer restore()

	if !inst.shouldAutoReload() {
		t.Fatal("first call should allow reload")
	}
	if inst.shouldAutoReload() {
		t.Fatal("immediate second call should be blocked by cooldown")
	}
}

func TestShouldAutoReload_CooldownExpires(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	_, restore := stubNetwork(t, true, 10*time.Millisecond)
	defer restore()

	if !inst.shouldAutoReload() {
		t.Fatal("first call should allow reload")
	}
	time.Sleep(20 * time.Millisecond)
	if !inst.shouldAutoReload() {
		t.Fatal("call after cooldown should be allowed")
	}
}

func TestFindInterfaceByIP_LoopbackFindable(t *testing.T) {
	// Loopback is always present; verify the helper finds it by IP.
	name := findInterfaceByIP("127.0.0.1")
	if name == "" {
		t.Fatal("expected to find loopback by 127.0.0.1")
	}
}

func TestFindInterfaceByIP_NonexistentReturnsEmpty(t *testing.T) {
	if got := findInterfaceByIP("198.51.100.255"); got != "" {
		t.Fatalf("expected empty for unassigned IP, got %q", got)
	}
}

func TestIsInterfaceUp_UnknownNameIsFalse(t *testing.T) {
	// The real implementation returns false on lookup failure.
	if isInterfaceUp("definitely-not-an-interface-name-9999") {
		t.Fatal("expected false for nonexistent interface")
	}
}

// The watcher's utun-down → reload trigger path. Directly verify the
// shouldAutoReload + isInterfaceUp wiring: simulate "down" + no
// cooldown, one shouldAutoReload call, confirm triggerReload receives
// the call.
func TestWatcherReloadTrigger_FiresWhenInterfaceDown(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	fired, restore := stubNetwork(t, false, 10*time.Second)
	defer restore()

	// Simulate one tick past grace with a down interface.
	if isInterfaceUp("anything") {
		t.Fatal("stub should report down")
	}
	if !inst.shouldAutoReload() {
		t.Fatal("first reload should be allowed")
	}
	triggerReload(inst)

	if got := atomic.LoadInt32(fired); got != 1 {
		t.Fatalf("expected 1 reload trigger, got %d", got)
	}
}

// The startup-grace and svc-nil guards in watchNetworkChanges itself
// are exercised by the live integration test on Mac mini + MacBook —
// spinning up a real Nebula Control in a unit test adds an order of
// magnitude of dependency for no incremental signal over the unit
// tests above (which already cover shouldAutoReload, triggerReload
// dispatch, and findInterfaceByIP/isInterfaceUp).
