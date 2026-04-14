package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"
)

func TestUpgradeTunMode_UpdatesNebulaYAML(t *testing.T) {
	// Create a temp config directory.
	tmpDir := t.TempDir()
	origConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = origConfigDir }()

	// Write a userspace nebula.yaml.
	nebulaYAML := `pki:
  ca: /etc/hop-agent/ca.crt
lighthouse:
  am_lighthouse: false
  local_allow_list:
    "192.168.23.0/24": true
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

	// Run upgrade.
	upgradeTunMode()

	// Verify tun-mode file updated.
	data, err := os.ReadFile(filepath.Join(tmpDir, "tun-mode"))
	if err != nil {
		t.Fatalf("failed to read tun-mode: %v", err)
	}
	if string(data) != "kernel" {
		t.Fatalf("expected tun-mode=kernel, got %q", string(data))
	}

	// Verify nebula.yaml updated.
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
	// user: true should be gone (replaced by dev+mtu).
	if _, hasUser := tun["user"]; hasUser {
		t.Fatal("tun.user should be removed after kernel upgrade")
	}

	// Verify other sections preserved.
	lighthouse, ok := cfg["lighthouse"].(map[string]interface{})
	if !ok {
		t.Fatal("lighthouse section should be preserved")
	}
	lal, ok := lighthouse["local_allow_list"].(map[string]interface{})
	if !ok {
		t.Fatal("local_allow_list should be preserved after TUN upgrade")
	}
	if lal["192.168.23.0/24"] != true {
		t.Fatal("local_allow_list subnet should be preserved")
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
	tmpDir := t.TempDir()
	origConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = origConfigDir }()

	// No tun-mode file exists. Non-root → userspace.
	// (We're running tests as non-root typically.)
	if os.Getuid() == 0 {
		t.Skip("test requires non-root")
	}
	mode := readTunMode()
	if mode != "userspace" {
		t.Fatalf("expected userspace for non-root with no file, got %q", mode)
	}
}

func TestReadTunMode_KernelFile(t *testing.T) {
	tmpDir := t.TempDir()
	origConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = origConfigDir }()

	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("kernel"), 0644)

	mode := readTunMode()
	if mode != "kernel" {
		t.Fatalf("expected kernel, got %q", mode)
	}
}

func TestReadTunMode_UserspaceFile_NonPrivileged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root")
	}

	tmpDir := t.TempDir()
	origConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = origConfigDir }()

	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("userspace"), 0644)

	mode := readTunMode()
	// Non-root + file says userspace → stays userspace (no upgrade).
	if mode != "userspace" {
		t.Fatalf("expected userspace for non-root, got %q", mode)
	}
}

func TestReadTunMode_InvalidContent(t *testing.T) {
	tmpDir := t.TempDir()
	origConfigDir := configDir
	configDir = tmpDir
	defer func() { configDir = origConfigDir }()

	os.WriteFile(filepath.Join(tmpDir, "tun-mode"), []byte("garbage"), 0644)

	mode := readTunMode()
	// Invalid content should default to userspace.
	if mode != "userspace" {
		t.Fatalf("expected userspace for invalid content, got %q", mode)
	}
}
