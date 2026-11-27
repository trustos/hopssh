package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Enrolling a second network writes it to disk and installs the service
// again. On Linux that ran `systemctl start`, which does nothing to a unit
// that is already running, so the new network stayed down until the machine
// or the service was restarted. macOS unloads and loads its job, and Windows
// stops and starts the service; both bring the new network up.

func TestServiceStartCmd_RestartsARunningService(t *testing.T) {
	if got := serviceStartCmd(true); got[1] != "restart" {
		t.Fatalf("a running service: %v, want a restart", got)
	}
	if got := serviceStartCmd(false); got[1] != "start" {
		t.Fatalf("a stopped service: %v, want a start", got)
	}
	for _, got := range [][]string{serviceStartCmd(true), serviceStartCmd(false)} {
		if got[0] != "systemctl" || got[2] != agentServiceName {
			t.Fatalf("wrong command: %v", got)
		}
	}
}

// The installer must ask whether the unit is running and use the answer; a
// plain `systemctl start` in its place is the bug.
func TestInstallAgentSystemd_RestartsWhatIsRunning(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	src := filepath.Join(filepath.Dir(thisFile), "service.go")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	body := func() string {
		i := strings.Index(string(b), "func installAgentSystemd() {")
		if i < 0 {
			t.Fatal("installAgentSystemd not found")
		}
		j := strings.Index(string(b)[i:], "\n}\n")
		return string(b)[i : i+j]
	}()
	if !strings.Contains(body, "is-active") {
		t.Fatal("the installer never asks whether the service is running")
	}
	if !strings.Contains(body, "serviceStartCmd(") {
		t.Fatal("the installer does not use serviceStartCmd")
	}
	if strings.Contains(body, `"systemctl", "start"`) {
		t.Fatal("the installer still starts the service plainly")
	}
}
