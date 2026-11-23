package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKernelTunClose_ReleasesDeviceAndPort: a "system-wide" network must be able
// to start again right after it was closed, as a certificate renewal does, also
// while a program started from the web terminal (tmux, nohup) is still running.
// Needs root, and a network namespace of its own so that it can't touch the
// machine's:
//
//	go test -mod=vendor -c -o client.test ./internal/client
//	sudo unshare -n sh -c 'ip link set lo up && ./client.test -test.run KernelTunClose -test.v'
func TestKernelTunClose_ReleasesDeviceAndPort(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("a TUN device needs root")
	}
	m := newE2EMesh(t)
	port := freeUDPPort(t)
	m.enroll("home", e2eSubnet+"6", port, time.Hour, nil)
	cfg := filepath.Join(m.dir, "home", "nebula.yaml")
	yaml, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kernel := strings.Replace(string(yaml), "tun: {user: true}", "tun: {dev: hop-test, mtu: 1420}", 1)
	if err := os.WriteFile(cfg, []byte(kernel), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		svc, err := startNebulaKernelTun(cfg)
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if svc.DevName() != "hop-test" {
			t.Fatalf("start %d: device %q", i, svc.DevName())
		}
		if i == 0 {
			child := exec.Command("sleep", "60")
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { child.Process.Kill(); child.Wait() }()
		}
		if i%2 == 1 {
			time.Sleep(200 * time.Millisecond) // both readers are waiting
		}
		svc.Close()
		if udpPortHeld(port) {
			t.Fatalf("port %d still held after close %d", port, i)
		}
		if dev := findInterfaceByIP(e2eSubnet + "6"); dev != "" {
			t.Fatalf("device %q still there after close %d", dev, i)
		}
	}
}
