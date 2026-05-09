package client

import (
	"os/exec"
	"runtime"
	"strings"
)

// readServiceStatus probes the platform service-manager for hop-agent
// liveness. Used by the local-API status snapshot. CLI status subcommand
// in cmd/agent calls the same logic; both invoke this helper.
//
// Returns a free-form status string ("running (launchd)", "stopped", etc.)
// suitable for direct display.
func readServiceStatus() string {
	switch runtime.GOOS {
	case "darwin":
		if err := exec.Command("launchctl", "list", "com.hopssh.agent").Run(); err == nil {
			return "running (launchd)"
		}
		return "stopped"
	case "windows":
		if out, err := exec.Command("sc.exe", "query", "hop-agent").CombinedOutput(); err == nil && strings.Contains(string(out), "RUNNING") {
			return "running (sc.exe)"
		}
		return "stopped"
	default:
		out, err := exec.Command("systemctl", "is-active", "hop-agent").Output()
		if err == nil {
			return strings.TrimSpace(string(out)) + " (systemd)"
		}
		return "stopped"
	}
}
