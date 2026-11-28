//go:build !windows

package api_test

import (
	"testing"
	"time"
)

// TestE2E_ProxyBackToBack: 2000 health checks of one node through the server,
// one after another. The server opens a connection per request from a
// random port, and the node closes it first, so the node keeps each one in
// TIME_WAIT for 60 s. Before patch gvisor-time-wait a connection that met one
// of those hung until the 30 s route timeout, in a userspace node (TestE2E_Proxy,
// 2026-11-27). Without the patch this test failed 5 runs of 5 on the Mac, at
// checks 769 to 894, and 5 of 5 in a Linux VM, at 169 to 1426. A count, not a
// time: the 2000 took 0.9 s on the Mac and 4.1 s in the VM. The mechanism
// alone: TestTimeWaitReopen and TestE2E_ConnectAgainAfterTimeWait in
// internal/client.
func TestE2E_ProxyBackToBack(t *testing.T) {
	if raceEnabled {
		// Under -race gVisor takes about 0.5 s for one connection, so the 2 s
		// bar says nothing (see TestE2E_ConnectAgainAfterTimeWait).
		t.Skip("gVisor is too slow under -race for a test of timing")
	}
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	start := time.Now()
	for n := 1; n <= 2000; n++ {
		t0 := time.Now()
		status, _ := h.owner.Do("GET", h.url("/nodes/%s/health", box.NodeID), nil)
		if d := time.Since(t0); status != 200 || d > 2*time.Second {
			t.Fatalf("health check %d, %.2f s in: HTTP %d after %v", n, t0.Sub(start).Seconds(), status, d.Round(time.Millisecond))
		}
	}
	t.Logf("2000 health checks in %v", time.Since(start).Round(time.Millisecond))
}
