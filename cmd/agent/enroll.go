package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultEndpoint = "https://hopssh.com"
	agentAPIPort    = 41820
)

// configDir is the directory where agent config, certs, and keys are stored.
// Set once at startup by resolveConfigDir(). All subcommands use this.
var configDir = resolveConfigDir("")

// resolveConfigDir determines the config directory.
// Priority: explicit override → existing system install → root → user home.
func resolveConfigDir(override string) string {
	if override != "" {
		return override
	}
	// Backward compat: if system-wide install exists, use it.
	if _, err := os.Stat("/etc/hop-agent/node.crt"); err == nil {
		return "/etc/hop-agent"
	}
	// Root → system path.
	if os.Getuid() == 0 {
		return "/etc/hop-agent"
	}
	// User-level paths.
	if runtime.GOOS == "darwin" {
		if home, _ := os.UserHomeDir(); home != "" {
			return filepath.Join(home, "Library", "Application Support", "hopssh")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "hopssh")
	}
	if home, _ := os.UserHomeDir(); home != "" {
		return filepath.Join(home, ".config", "hopssh")
	}
	return "/etc/hop-agent"
}

// skipService is set by --no-service flag during enrollment.
var skipService bool

// enrollTunMode is set by --tun-mode flag during enrollment.
var enrollTunMode = "userspace"

type enrollResponse struct {
	NodeID         string `json:"nodeId"`
	CACert         string `json:"caCert"`
	NodeCert       string `json:"nodeCert"`
	NodeKey        string `json:"nodeKey"`
	AgentToken     string `json:"agentToken"`
	ServerIP       string `json:"serverIP"`
	NebulaIP       string `json:"nebulaIP"`
	LighthousePort int    `json:"lighthousePort"`
	LighthouseHost string `json:"lighthouseHost"`
	DNSDomain      string `json:"dnsDomain"`
}

// runEnroll handles the `hop-agent enroll` subcommand with 4 modes:
//   - device flow (default, interactive)
//   - --token-stdin (read token from stdin)
//   - --token <token> (token as argument)
//   - --bundle <path> (offline tarball)
func runEnroll(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	token := fs.String("token", "", "Enrollment token (visible in ps — prefer --token-stdin)")
	tokenStdin := fs.Bool("token-stdin", false, "Read enrollment token from stdin")
	bundlePath := fs.String("bundle", "", "Path to pre-generated enrollment bundle (.tar.gz)")
	endpoint := fs.String("endpoint", defaultEndpoint, "Control plane URL")
	tunMode := fs.String("tun-mode", "", "TUN mode: kernel (OS interface) or userspace (in-process). Auto-detected if not set: root→kernel, non-root→userspace")
	noService := fs.Bool("no-service", false, "Skip automatic service installation")
	force := fs.Bool("force", false, "Re-enroll: stop service, remove old config, enroll fresh")
	cfgDir := fs.String("config-dir", "", "Override config directory")
	fs.Parse(args)
	skipService = *noService

	// Auto-detect TUN mode: root/admin gets kernel TUN (real OS interface), non-root gets userspace.
	resolved := *tunMode
	if resolved == "" {
		if isPrivileged() {
			resolved = "kernel"
		} else {
			resolved = "userspace"
		}
	}
	if resolved != "userspace" && resolved != "kernel" {
		log.Fatalf("Invalid --tun-mode %q: must be 'userspace' or 'kernel'", resolved)
	}
	enrollTunMode = resolved
	if *cfgDir != "" {
		configDir = resolveConfigDir(*cfgDir)
	}

	// Handle re-enrollment with --force.
	if *force {
		fmt.Println("==> Re-enrolling (--force): cleaning up old config...")
		// Stop existing service.
		if runtime.GOOS == "darwin" {
			exec.Command("launchctl", "unload", "/Library/LaunchDaemons/com.hopssh.agent.plist").Run()
		} else {
			exec.Command("systemctl", "stop", "hop-agent").Run()
		}
		// Safety: don't RemoveAll on paths that are too short.
		if len(configDir) > 5 {
			os.RemoveAll(configDir)
		}
	}

	// Check if already enrolled — prevent accidental re-enrollment.
	if _, err := os.Stat(filepath.Join(configDir, "node.crt")); err == nil {
		fmt.Fprintf(os.Stderr, "Warning: This device is already enrolled (config exists at %s).\n", configDir)
		fmt.Fprintf(os.Stderr, "To re-enroll, use --force:\n")
		fmt.Fprintf(os.Stderr, "  sudo hop-agent enroll --force --endpoint <url>\n\n")
		os.Exit(1)
	}

	switch {
	case *bundlePath != "":
		enrollFromBundle(*bundlePath, *endpoint)
	case *tokenStdin:
		tok := readTokenFromStdin()
		enrollWithToken(tok, *endpoint)
	case *token != "":
		enrollWithToken(*token, *endpoint)
	default:
		enrollDeviceFlow(*endpoint)
	}
}

// enrollDeviceFlow initiates the device authorization flow (RFC 8628).
func enrollDeviceFlow(endpoint string) {
	// Request device code.
	resp, err := http.Post(endpoint+"/api/device/code", "application/json", nil)
	if err != nil {
		log.Fatalf("Failed to request device code: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Failed to request device code (HTTP %d): %s", resp.StatusCode, body)
	}

	var codeResp struct {
		DeviceCode string `json:"deviceCode"`
		UserCode   string `json:"userCode"`
		ExpiresIn  int    `json:"expiresIn"`
		Interval   int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil {
		log.Fatalf("Failed to parse device code response: %v", err)
	}

	fmt.Println()
	fmt.Println("  To enroll this node, open:  " + endpoint + "/device")
	fmt.Println("  Enter code:  " + codeResp.UserCode)
	fmt.Println()
	fmt.Println("  Waiting for authorization...")

	// Poll until authorized or expired.
	interval := time.Duration(codeResp.Interval) * time.Second
	if interval < 2*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(codeResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		hostname, _ := os.Hostname()
		pollBody := fmt.Sprintf(`{"deviceCode":%q,"hostname":%q,"os":%q,"arch":"%s"}`,
			codeResp.DeviceCode, hostname, runtime.GOOS, detectArch())
		pollResp, err := http.Post(endpoint+"/api/device/poll", "application/json",
			strings.NewReader(pollBody))
		if err != nil {
			continue
		}

		if pollResp.StatusCode == http.StatusForbidden {
			bodyBytes, _ := io.ReadAll(pollResp.Body)
			pollResp.Body.Close()
			status := strings.TrimSpace(string(bodyBytes))
			if status == "authorization_pending" {
				continue
			}
			if status == "expired_token" {
				log.Fatal("Device code expired. Run `hop-agent enroll` again.")
			}
			log.Fatalf("Unexpected poll response: %s", status)
		}

		if pollResp.StatusCode == http.StatusOK {
			var er enrollResponse
			if err := json.NewDecoder(pollResp.Body).Decode(&er); err != nil {
				pollResp.Body.Close()
				log.Fatalf("Failed to parse enrollment response: %v", err)
			}
			pollResp.Body.Close()
			installCerts(&er, endpoint)
			return
		}

		pollResp.Body.Close()
	}

	log.Fatal("Device code expired. Run `hop-agent enroll` again.")
}

// enrollWithToken uses a pre-generated enrollment token.
func enrollWithToken(token, endpoint string) {
	hostname, _ := os.Hostname()
	reqBody := fmt.Sprintf(`{"token":%q,"hostname":%q,"os":"linux","arch":"%s"}`,
		token, hostname, detectArch())

	resp, err := http.Post(endpoint+"/api/enroll", "application/json",
		strings.NewReader(reqBody))
	if err != nil {
		log.Fatalf("Enrollment failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Enrollment failed (HTTP %d): %s", resp.StatusCode, body)
	}

	var er enrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		log.Fatalf("Failed to parse enrollment response: %v", err)
	}
	installCerts(&er, endpoint)
}

// enrollFromBundle installs from a pre-generated tarball.
func enrollFromBundle(path, endpoint string) {
	fmt.Printf("Installing from bundle: %s\n", path)

	// Extract tarball to / using exec.Command directly (no shell interpolation).
	cmd := execCommand("tar", "xzf", path, "-C", "/")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Failed to extract bundle: %v\n%s", err, out)
	}
	fmt.Println("  ✓ Bundle extracted to " + configDir)

	// Read config.json from the extracted bundle to generate Nebula config.
	configData, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		log.Fatalf("Failed to read bundle config: %v", err)
	}
	var bundleConfig struct {
		NodeID         string `json:"nodeId"`
		ServerIP       string `json:"serverIP"`
		NebulaIP       string `json:"nebulaIP"`
		LighthousePort int    `json:"lighthousePort"`
		LighthouseHost string `json:"lighthouseHost"`
		DNSDomain      string `json:"dnsDomain"`
		Endpoint       string `json:"endpoint"`
	}
	if err := json.Unmarshal(configData, &bundleConfig); err != nil {
		log.Fatalf("Failed to parse bundle config: %v", err)
	}

	ep := bundleConfig.Endpoint
	if ep == "" {
		ep = endpoint
	}

	// Persist endpoint + nodeID for cert renewal.
	writeFileSecure(filepath.Join(configDir, "endpoint"), []byte(ep), 0600)
	if bundleConfig.NodeID != "" {
		writeFileSecure(filepath.Join(configDir, "node-id"), []byte(bundleConfig.NodeID), 0600)
	}

	// Generate Nebula config from bundle data.
	serverHost := bundleConfig.LighthouseHost
	if serverHost == "" {
		serverHost = extractHost(ep)
	}
	writeNebulaConfig(bundleConfig.ServerIP, serverHost, bundleConfig.LighthousePort, enrollTunMode)
	writeDNSConfig(bundleConfig.DNSDomain, serverHost, bundleConfig.LighthousePort)

	installService()
	fmt.Println("  ✓ Agent started")
}

func readTokenFromStdin() string {
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	log.Fatal("No token provided on stdin")
	return ""
}

func installCerts(er *enrollResponse, endpoint string) {
	os.MkdirAll(configDir, 0700)

	writeFileSecure(filepath.Join(configDir, "ca.crt"), []byte(er.CACert), 0644)
	writeFileSecure(filepath.Join(configDir, "node.crt"), []byte(er.NodeCert), 0644)
	writeFileSecure(filepath.Join(configDir, "node.key"), []byte(er.NodeKey), 0600)
	writeFileSecure(filepath.Join(configDir, "token"), []byte(er.AgentToken), 0600)
	writeFileSecure(filepath.Join(configDir, "endpoint"), []byte(endpoint), 0600)
	if er.NodeID != "" {
		writeFileSecure(filepath.Join(configDir, "node-id"), []byte(er.NodeID), 0600)
	}

	fmt.Printf("\n  ✓ Enrolled (%s)\n", er.NebulaIP)

	serverHost := er.LighthouseHost
	if serverHost == "" {
		serverHost = extractHost(endpoint)
	}
	writeNebulaConfig(er.ServerIP, serverHost, er.LighthousePort, enrollTunMode)
	writeDNSConfig(er.DNSDomain, serverHost, er.LighthousePort)
	installService()
	fmt.Println("  ✓ Agent started")
}

func writeNebulaConfig(serverIP, serverHost string, lighthousePort int, tunMode string) {
	if lighthousePort == 0 {
		lighthousePort = 41820 // fallback for legacy enrollment
	}

	tunConfig := "  user: true"
	if tunMode == "kernel" {
		tunConfig = "  mtu: 1300"
	}

	nebulaConfig := fmt.Sprintf(`pki:
  ca: %s/ca.crt
  cert: %s/node.crt
  key: %s/node.key

static_host_map:
  "%s": ["%s:%d"]

lighthouse:
  am_lighthouse: false
  hosts:
    - "%s"

relay:
  relays:
    - "%s"
  use_relays: true

listen:
  host: 0.0.0.0
  port: 0

punchy:
  punch: true
  respond: true

tun:
%s

firewall:
  outbound:
    - port: any
      proto: any
      host: any
  inbound:
    # Control plane (admin group) can reach agent API
    - port: %d
      proto: tcp
      groups:
        - admin
    # All mesh nodes can reach each other
    - port: any
      proto: tcp
      groups:
        - node
    # ICMP for diagnostics
    - port: any
      proto: icmp
      host: any
`, configDir, configDir, configDir,
		serverIP, serverHost, lighthousePort,
		serverIP,
		serverIP,
		tunConfig,
		agentAPIPort)

	writeFileSecure(filepath.Join(configDir, "nebula.yaml"), []byte(nebulaConfig), 0644)

	// Persist TUN mode so serve knows which mode to use.
	writeFileSecure(filepath.Join(configDir, "tun-mode"), []byte(tunMode), 0644)

	fmt.Printf("  ✓ Nebula config written (lighthouse: %s via %s:%d, tun: %s)\n", serverIP, serverHost, lighthousePort, tunMode)
}

// writeDNSConfig persists DNS configuration so the agent can set up split-DNS
// on serve. The DNS server runs on the control plane at lighthouseHost:dnsPort.
func writeDNSConfig(dnsDomain, lighthouseHost string, lighthousePort int) {
	if dnsDomain == "" {
		return
	}
	// DNS port follows the same offset scheme as the lighthouse port.
	const baseLighthousePort = 42001
	const baseDNSPort = 15300
	dnsPort := baseDNSPort + (lighthousePort - baseLighthousePort)

	writeFileSecure(filepath.Join(configDir, "dns-domain"), []byte(dnsDomain), 0644)
	writeFileSecure(filepath.Join(configDir, "dns-server"), []byte(fmt.Sprintf("%s:%d", lighthouseHost, dnsPort)), 0644)
	fmt.Printf("  ✓ DNS config written (domain: .%s, server: %s:%d)\n", dnsDomain, lighthouseHost, dnsPort)
}

func installService() {
	if skipService {
		fmt.Println("  Skipping service installation (--no-service)")
		fmt.Println("  Start manually: hop-agent serve")
		return
	}
	runAgentInstall(nil)
}

func writeFileSecure(path string, data []byte, mode os.FileMode) {
	if err := os.WriteFile(path, data, mode); err != nil {
		log.Fatalf("Failed to write %s: %v", path, err)
	}
}

func extractHost(endpoint string) string {
	s := endpoint
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.Index(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

func detectArch() string {
	return runtime.GOARCH
}

func runShellCmd(cmd string) (string, error) {
	var out bytes.Buffer
	c := execCommand("sh", "-c", cmd)
	c.Stdout = &out
	c.Stderr = &out
	err := c.Run()
	return out.String(), err
}
