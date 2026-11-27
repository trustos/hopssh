package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForFileChange reports whether path's content differs from have within d.
func waitForFileChange(t *testing.T, path string, have []byte, d time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		now, err := os.ReadFile(path)
		if err == nil && string(now) != string(have) {
			return true
		}
	}
	return false
}

// TestRenewalWaitsForTheStartToReadThePair: the renewal loop is started
// before Nebula opens node.crt and node.key, and a cert already past its
// renewal instant renews on the loop's first pass. Nebula reads the two
// files one after the other, so a write in that window leaves it with the
// new cert and the old key, and it refuses to start:
//
//	[renew home] cert past renewal threshold, attempting renewal POST
//	[agent] WARNING: Nebula userspace failed: start nebula: Could not load
//	        client cert (map[]): private key is not a pair with public key
//	        in nebula cert (falling back to OS stack)
//
// That is not a corner: it is every machine that was off for more than half
// its cert's life, which is 12 h of the 24 h we issue. The whole start fails,
// the network runs on the OS stack, and the retry loop brings Nebula up 2 s
// later. certStartProblem already treats "mismatched" as a state to recover
// from after a crash between the two writes; the same state happens without
// a crash, from a read in the middle.
func TestRenewalWaitsForTheStartToReadThePair(t *testing.T) {
	m := newE2EMesh(t)
	m.enroll("home", e2eSubnet+"6", freeUDPPort(t), 15*time.Hour, nil)
	dir := filepath.Join(m.dir, "home")
	before, err := os.ReadFile(filepath.Join(dir, "node.crt"))
	if err != nil {
		t.Fatal(err)
	}

	orig := startMeshFn
	startMeshFn = func(cfgPath, tunMode string) (meshService, error) {
		// Nebula opens the pair here. The renewal that startInstance
		// spawned must not write it before this returns.
		if waitForFileChange(t, filepath.Join(dir, "node.crt"), before, 300*time.Millisecond) {
			t.Error("the renewal wrote a new cert while the start was reading the pair")
		}
		return orig(cfgPath, tunMode)
	}
	t.Cleanup(func() { startMeshFn = orig })

	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	within(t, 30*time.Second, "home to be live", func() bool { return m.live("home") })

	// Waiting for the write, not just for the network: the fix moves the
	// renewal after the start, it must not stop it. And it keeps the instance
	// dir from being removed under a renewal still in flight.
	if !waitForFileChange(t, filepath.Join(dir, "node.crt"), before, 15*time.Second) {
		t.Fatal("the cert past its renewal instant was never renewed")
	}
}
