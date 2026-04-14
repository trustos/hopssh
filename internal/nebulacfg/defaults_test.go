package nebulacfg

import (
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
