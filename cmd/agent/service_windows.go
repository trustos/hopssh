//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	winServiceDisplayName = "hopssh mesh agent"
	winServiceDescription = "Encrypted mesh networking agent (hopssh). https://hopssh.com"
)

// svcIntegrateIfNeeded returns true if the process was launched by the
// Windows Service Control Manager. In that case it redirects logging
// to a file, spins up the SCM handler goroutine, and wires the SCM
// Stop/Shutdown control codes into cancel(). Returns false in console
// mode — the caller then relies on SIGINT/SIGTERM the usual way.
func svcIntegrateIfNeeded(cancel context.CancelFunc) bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		log.Printf("[svc] IsWindowsService: %v (continuing in console mode)", err)
		return false
	}
	if !isSvc {
		return false
	}

	redirectLogsToFile()
	log.Printf("[svc] running under Service Control Manager")

	go func() {
		if err := svc.Run(agentServiceName, &winServiceHandler{cancel: cancel}); err != nil {
			log.Printf("[svc] svc.Run: %v", err)
			cancel()
		}
	}()
	return true
}

// redirectLogsToFile points log.Printf output at %ProgramData%\hopssh\
// hop-agent.log when running as a Windows service. SCM discards the
// service process's stderr/stdout by default — without this redirect,
// `log.Printf` writes vanish into the void.
func redirectLogsToFile() {
	dir := filepath.Join(os.Getenv("ProgramData"), "hopssh")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	path := filepath.Join(dir, "hop-agent.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	// Keep stderr/stdout too so anything that bypasses log.Printf
	// (panics, direct fmt.Fprintln(os.Stderr, ...)) also lands in the
	// file.
	os.Stdout = f
	os.Stderr = f
}

// winServiceHandler implements svc.Handler. It reports StartPending
// → Running to SCM, then waits for Stop/Shutdown and cancels the
// top-level context so the main goroutine executes its existing
// graceful-shutdown block.
type winServiceHandler struct {
	cancel context.CancelFunc
}

func (h *winServiceHandler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for req := range r {
		switch req.Cmd {
		case svc.Interrogate:
			s <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			s <- svc.Status{State: svc.StopPending, WaitHint: 10000}
			h.cancel()
			return false, 0
		default:
			log.Printf("[svc] unexpected control request: %d", req.Cmd)
		}
	}
	return false, 0
}

// cleanupOldBinary removes <exe>.old left behind by a previous
// self-update on Windows. Best-effort; silent on any failure.
func cleanupOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err == nil {
		_ = os.Remove(old)
	}
}

// installAgentWindows registers hop-agent as a Windows service and
// starts it. Idempotent — if the service already exists, prints how
// to uninstall first and exits non-zero.
func installAgentWindows() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot resolve executable path: %v\n", err)
		os.Exit(1)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot resolve absolute path: %v\n", err)
		os.Exit(1)
	}

	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to Service Control Manager: %v\n", err)
		fmt.Fprintf(os.Stderr, "Run as Administrator.\n")
		os.Exit(1)
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(agentServiceName); err == nil {
		existing.Close()
		fmt.Fprintf(os.Stderr, "Error: service %q already exists.\n", agentServiceName)
		fmt.Fprintf(os.Stderr, "Uninstall first:  hop-agent uninstall\n")
		os.Exit(1)
	}

	// Pass through the currently-resolved config directory so the
	// service under LocalSystem reads the same configs the caller
	// enrolled with (typically C:\Users\<user>\.config\hopssh).
	// LocalSystem has read access to any user's home directory.
	//
	// CreateService composes BinaryPathName from (exepath + args...);
	// anything in Config.BinaryPathName is ignored. So pass the
	// subcommand + flag as trailing args.
	cfg := mgr.Config{
		ServiceType:  0x10, // SERVICE_WIN32_OWN_PROCESS
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  winServiceDisplayName,
		Description:  winServiceDescription,
		// ServiceStartName empty = LocalSystem (needed for WinTun).
	}
	s, err := m.CreateService(agentServiceName, exe, cfg, "serve", "--config-dir", configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: CreateService: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	// Recovery: restart on failure. First+second failure after 5s,
	// third+ after 30s; reset the counter after 24h of healthy runtime.
	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(recovery, uint32(86400)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SetRecoveryActions: %v\n", err)
	}

	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: service registered but failed to start: %v\n", err)
		fmt.Fprintf(os.Stderr, "Start manually:  sc.exe start %s\n", agentServiceName)
		os.Exit(1)
	}

	fmt.Println("==> hop-agent service installed and started.")
	fmt.Printf("    Config:    %s\n", configDir)
	fmt.Printf("    Logs:      %s\n", filepath.Join(os.Getenv("ProgramData"), "hopssh", "hop-agent.log"))
	fmt.Printf("    Status:    sc.exe query %s\n", agentServiceName)
	fmt.Printf("    Stop:      hop-agent stop\n")
	fmt.Printf("    Restart:   hop-agent restart\n")
	fmt.Printf("    Uninstall: hop-agent uninstall\n")
}

// uninstallAgentWindows stops and removes the service. Idempotent —
// succeeds if the service isn't present.
func uninstallAgentWindows() {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to Service Control Manager: %v\n", err)
		os.Exit(1)
	}
	defer m.Disconnect()

	s, err := m.OpenService(agentServiceName)
	if err != nil {
		// Service doesn't exist; nothing to do.
		fmt.Println("==> hop-agent service not installed (nothing to remove).")
		return
	}
	defer s.Close()

	// Best-effort stop before delete — SCM requires stop first.
	if _, err := s.Control(svc.Stop); err != nil {
		// Could already be stopped; log but don't abort.
		fmt.Fprintf(os.Stderr, "Note: stop control returned: %v\n", err)
	}
	// Wait briefly for the service to reach Stopped state.
	waitForServiceStop(s, 10*time.Second)

	if err := s.Delete(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: DeleteService: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("==> hop-agent service uninstalled.")
}

// restartAgentWindows is the Windows branch of the `hop-agent restart`
// subcommand. Uses `sc.exe` for simplicity — equivalent to Control(Stop)
// + Start() via mgr, but a simple shell-out is less code + matches what
// users would run by hand. Fails loudly if the service isn't installed.
func restartAgentWindows() {
	if err := runSCCmd("stop"); err != nil {
		// Already stopped is OK.
	}
	waitForServiceState(agentServiceName, svc.Stopped, 10*time.Second)
	if err := runSCCmd("start"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: sc.exe start failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("==> hop-agent restarted.")
}

func stopAgentWindows() {
	if err := runSCCmd("stop"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: sc.exe stop failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("==> hop-agent stopped.")
}

func runSCCmd(action string) error {
	out, err := exec.Command("sc.exe", action, agentServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(out))
	}
	return nil
}

// waitForServiceStop blocks until the service reports Stopped or the
// timeout fires. Used during uninstall between Control(Stop) and
// Delete() — deleting a running service fails.
func waitForServiceStop(s *mgr.Service, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			return
		}
		if status.State == svc.Stopped {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForServiceState re-opens the service by name and polls Query()
// until the state matches or the timeout fires. Used after a shell
// sc.exe stop to block until the service really stopped before issuing
// start.
func waitForServiceState(name string, want svc.State, timeout time.Duration) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return
	}
	defer s.Close()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if status, err := s.Query(); err == nil && status.State == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
