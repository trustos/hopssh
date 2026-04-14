package nebulacfg

import (
	"strings"
	"testing"
)

func TestConstants(t *testing.T) {
	if ListenPort <= 0 || ListenPort > 65535 {
		t.Fatalf("ListenPort must be a valid port, got %d", ListenPort)
	}
	if TunMTU < 1280 || TunMTU > 1500 {
		t.Fatalf("TunMTU should be between 1280-1500, got %d", TunMTU)
	}
	if !UseRelays {
		t.Fatal("UseRelays must be true — false disables relay entirely, breaking connectivity behind strict NAT")
	}
	if !PunchBack {
		t.Fatal("PunchBack must be true for NAT traversal")
	}
	if PunchDelay == "" {
		t.Fatal("PunchDelay must be set")
	}
	if RespondDelay == "" {
		t.Fatal("RespondDelay must be set")
	}
}

func TestDetectPhysicalInterface_Loopback(t *testing.T) {
	iface, err := DetectPhysicalInterface("127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iface == "" {
		t.Fatal("expected non-empty interface name")
	}
	// Should be loopback interface (lo0 on macOS, lo on Linux).
	t.Logf("Loopback interface: %s", iface)
}

func TestDetectPhysicalInterface_PublicDNS(t *testing.T) {
	iface, err := DetectPhysicalInterface("8.8.8.8")
	if err != nil {
		t.Skipf("no internet connectivity: %v", err)
	}
	if iface == "" {
		t.Fatal("expected non-empty interface name")
	}
	// Should NOT be loopback.
	if iface == "lo0" || iface == "lo" {
		t.Fatalf("expected non-loopback interface for public DNS, got %s", iface)
	}
	t.Logf("Physical interface: %s", iface)
}

func TestDetectPhysicalInterface_InvalidHost(t *testing.T) {
	_, err := DetectPhysicalInterface("not-a-valid-host.invalid")
	if err == nil {
		t.Fatal("expected error for unresolvable host")
	}
}

func TestLocalAllowListYAML_Empty(t *testing.T) {
	result := LocalAllowListYAML("")
	if result != "" {
		t.Fatalf("expected empty string for empty interface, got %q", result)
	}
}

func TestLocalAllowListYAML_Simple(t *testing.T) {
	result := LocalAllowListYAML("en0")
	if !strings.Contains(result, "local_allow_list") {
		t.Fatal("expected local_allow_list key")
	}
	if !strings.Contains(result, "interfaces") {
		t.Fatal("expected interfaces key")
	}
	if !strings.Contains(result, "en0") {
		t.Fatal("expected interface name en0")
	}
	if !strings.Contains(result, "true") {
		t.Fatal("expected true value")
	}
}

func TestLocalAllowListYAML_SpecialChars(t *testing.T) {
	// Windows interface names can have spaces and special chars.
	// regexp.QuoteMeta escapes regex metacharacters like [ ] ( ) etc.
	result := LocalAllowListYAML("ZeroTier [abc]")
	if !strings.Contains(result, `\[abc\]`) {
		t.Fatalf("expected escaped brackets in %q", result)
	}
}

func TestLocalAllowListYAML_WindowsWiFi(t *testing.T) {
	result := LocalAllowListYAML("Wi-Fi")
	if !strings.Contains(result, "Wi-Fi") {
		t.Fatalf("expected Wi-Fi in %q", result)
	}
}

func TestLocalAllowListYAML_LinuxInterface(t *testing.T) {
	result := LocalAllowListYAML("ens3")
	if !strings.Contains(result, "ens3") {
		t.Fatal("expected ens3 in YAML")
	}
}
