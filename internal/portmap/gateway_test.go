package portmap

import (
	"runtime"
	"testing"
)

func TestDiscoverGateway_Smoke(t *testing.T) {
	// Non-hermetic — exercises the real OS command/file. Skip on CI where
	// the route table may be empty or restricted.
	if testing.Short() {
		t.Skip("skipping non-hermetic gateway discovery in short mode")
	}
	gw, err := DiscoverGateway()
	if err != nil {
		t.Fatalf("DiscoverGateway: %v", err)
	}
	if !gw.IsValid() || !gw.Is4() {
		t.Fatalf("expected valid IPv4 gateway, got %v", gw)
	}
	t.Logf("%s default gateway: %s", runtime.GOOS, gw)
}

func TestParseLinuxGatewayHex(t *testing.T) {
	// /proc/net/route gives gateway in little-endian hex.
	// 0100A8C0 = bytes 01,00,A8,C0 → IP 192.168.0.1 (a common
	// home-router default).
	gw, err := parseLinuxGatewayHex("0100A8C0")
	if err != nil {
		t.Fatalf("parseLinuxGatewayHex: %v", err)
	}
	want := "192.168.0.1"
	if got := gw.String(); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestParseLinuxGatewayHex_Bad(t *testing.T) {
	for _, in := range []string{"", "XX", "0117A8", "0117A8C0FF"} {
		if _, err := parseLinuxGatewayHex(in); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}

func TestParseGatewayString(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"0.0.0.0", true},    // unspecified
		{"127.0.0.1", true},  // loopback
		{"::1", true},        // IPv6 loopback — we only accept IPv4
		{"2001:db8::1", true}, // v6
		{"not-an-ip", true},
	} {
		_, err := parseGatewayString(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseGatewayString(%q): err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}
