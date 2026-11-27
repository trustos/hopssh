package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const closeHelperEnv = "HOPSSH_TEST_CLOSE_HELPER"

// TestUserspaceClose_DoesNotExit: closing one "in-app" network must not end
// the agent. Without vendor patch 27 Nebula's TUN reader can see its pipe
// close before Nebula is marked closed and call os.Exit(2), so this runs in a
// child process.
func TestUserspaceClose_DoesNotExit(t *testing.T) {
	if testing.Short() {
		t.Skip("real Nebula")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUserspaceCloseHelper$", "-test.v")
	cmd.Env = append(os.Environ(), closeHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 2000 {
			out = out[len(out)-2000:]
		}
		t.Fatalf("closing a userspace network ended the process: %v\n%s", err, out)
	}
}

func TestUserspaceCloseHelper(t *testing.T) {
	if os.Getenv(closeHelperEnv) == "" {
		t.Skip("child process of TestUserspaceClose_DoesNotExit")
	}
	m := newE2EMesh(t)
	m.enroll("home", e2eSubnet+"6", freeUDPPort(t), time.Hour, nil)
	cfg := filepath.Join(m.dir, "home", "nebula.yaml")
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		svc, err := startNebula(cfg)
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		// Keep the hostmap lock busy, as peers and status polls do: it
		// widens the window between cancel and close.
		ctrl, stop := svc.NebulaControl(), make(chan struct{})
		for j := 0; j < 4; j++ {
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
						ctrl.ListHostmapIndexes(false)
					}
				}
			}()
		}
		time.Sleep(2 * time.Millisecond)
		svc.Close()
		close(stop)
		// The port is free again. Nebula's close gives its readers a second
		// each before closing the socket, so this is a wait and not a glance:
		// on a loaded runner the reader can still be in its syscall when
		// Close returns. An orphan never frees it, so the teeth stay.
		port := m.c.enrolls.Get("home").ListenPort
		within(t, 10*time.Second, "the UDP port to be released", func() bool { return !udpPortHeld(port) })
	}
	// Without vendor patch 29 every close leaves a netstack behind, with a
	// goroutine per CPU; each certificate renewal closes one.
	within(t, 10*time.Second, "the closed networks' goroutines to end", func() bool { return runtime.NumGoroutine() < before+50 })
}
