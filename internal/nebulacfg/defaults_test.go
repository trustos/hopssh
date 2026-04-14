package nebulacfg

import (
	"net"
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
}

func TestDetectPhysicalSubnet_Loopback(t *testing.T) {
	subnet, err := DetectPhysicalSubnet("127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subnet == "" {
		t.Fatal("expected non-empty subnet")
	}
	// Should be a valid CIDR.
	_, _, err = net.ParseCIDR(subnet)
	if err != nil {
		t.Fatalf("returned subnet is not valid CIDR: %q: %v", subnet, err)
	}
}

func TestDetectPhysicalSubnet_PublicDNS(t *testing.T) {
	// 8.8.8.8 should route through the default interface.
	subnet, err := DetectPhysicalSubnet("8.8.8.8")
	if err != nil {
		t.Skipf("no internet connectivity: %v", err)
	}
	if subnet == "" {
		t.Fatal("expected non-empty subnet")
	}
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		t.Fatalf("invalid CIDR: %q: %v", subnet, err)
	}
	// Should NOT be loopback.
	if ipNet.IP.IsLoopback() {
		t.Fatalf("expected non-loopback subnet for public DNS target, got %s", subnet)
	}
}

func TestDetectPhysicalSubnet_UnreachableHost(t *testing.T) {
	// RFC 5737 TEST-NET-1 — guaranteed non-routable.
	_, err := DetectPhysicalSubnet("192.0.2.1")
	// This may succeed (OS has a default route) or fail (no route).
	// Either way it should not panic.
	_ = err
}

func TestDetectPhysicalSubnet_InvalidHost(t *testing.T) {
	_, err := DetectPhysicalSubnet("not-a-valid-host-that-exists.invalid")
	if err == nil {
		t.Fatal("expected error for unresolvable host")
	}
}

func TestLocalAllowListYAML_Empty(t *testing.T) {
	result := LocalAllowListYAML("")
	if result != "" {
		t.Fatalf("expected empty string for empty subnet, got %q", result)
	}
}

func TestLocalAllowListYAML_IPv4(t *testing.T) {
	result := LocalAllowListYAML("192.168.23.0/24")
	if !strings.Contains(result, "local_allow_list") {
		t.Fatal("expected local_allow_list key in YAML")
	}
	if !strings.Contains(result, "192.168.23.0/24") {
		t.Fatal("expected subnet in YAML")
	}
	if !strings.Contains(result, "true") {
		t.Fatal("expected true value for subnet")
	}
}

func TestLocalAllowListYAML_IPv6(t *testing.T) {
	result := LocalAllowListYAML("2001:db8::/32")
	if !strings.Contains(result, "2001:db8::/32") {
		t.Fatal("expected IPv6 subnet in YAML")
	}
}

func TestDetectSubnetForNetwork_IPv4(t *testing.T) {
	subnet, err := detectSubnetForNetwork("udp4", "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, err = net.ParseCIDR(subnet)
	if err != nil {
		t.Fatalf("invalid CIDR: %q: %v", subnet, err)
	}
}

func TestDetectSubnetForNetwork_InvalidNetwork(t *testing.T) {
	_, err := detectSubnetForNetwork("invalid", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for invalid network type")
	}
}
