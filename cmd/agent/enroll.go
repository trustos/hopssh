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

	"github.com/trustos/hopssh/internal/nebulacfg"
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
	// Backward compat: if system-wide install exists, use it. We accept
	// either the legacy flat layout (node.crt at the root) or the
	// v0.10+ registry layout (enrollments.json at the root).
	if _, err := os.Stat("/etc/hop-agent/node.crt"); err == nil {
		return "/etc/hop-agent"
	}
	if _, err := os.Stat("/etc/hop-agent/" + enrollmentsFile); err == nil {
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

// enrollName is the --name override for the enrollment being created.
// Empty string means "auto-pick from DNS domain or CA fingerprint".
var enrollName string

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
	name := fs.String("name", "", "Local name for this enrollment (default: mesh DNS domain or CA fingerprint)")
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

	enrollName = strings.TrimSpace(*name)
	if enrollName != "" {
		if err := validateEnrollmentName(enrollName); err != nil {
			log.Fatalf("Invalid --name: %v", err)
		}
	}

	// Migrate any pre-v0.10 flat layout into the subdir layout before we
	// read the registry. After this call, either the registry exists or
	// the configDir is empty of our files.
	if _, err := migrateLegacyLayout(configDir); err != nil {
		log.Fatalf("Legacy config migration failed: %v", err)
	}

	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		log.Fatalf("Load enrollments: %v", err)
	}

	if *force {
		if err := handleForce(reg, enrollName); err != nil {
			log.Fatalf("%v", err)
		}
		// Force may have removed entries; reload so downstream logic
		// sees the post-force registry.
		reg, err = loadEnrollmentRegistry(configDir)
		if err != nil {
			log.Fatalf("Reload enrollments after --force: %v", err)
		}
	}

	// If --name was given, pre-check collision so we fail before doing
	// the network round-trip to the control plane.
	if enrollName != "" && reg.Get(enrollName) != nil {
		fmt.Fprintf(os.Stderr, "Enrollment %q already exists.\n", enrollName)
		fmt.Fprintf(os.Stderr, "To replace it: hop-agent enroll --force --name %s --endpoint <url>\n", enrollName)
		fmt.Fprintf(os.Stderr, "Or pick a different --name.\n")
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

// handleForce implements the --force semantics against the new registry:
//   - Empty registry → no-op.
//   - --name targets one specific enrollment.
//   - No --name + exactly one enrollment → wipe it.
//   - No --name + multiple enrollments → error (ambiguous).
func handleForce(reg *enrollmentRegistry, targetName string) error {
	if reg.Len() == 0 {
		return nil
	}
	stopAgentService()

	if targetName != "" {
		if reg.Get(targetName) == nil {
			return nil
		}
		subdir := enrollmentDir(configDir, targetName)
		if len(subdir) > 5 {
			_ = os.RemoveAll(subdir)
		}
		return reg.Remove(targetName)
	}

	if reg.Len() == 1 {
		only := reg.List()[0]
		fmt.Println("==> Re-enrolling (--force): cleaning up old config...")
		subdir := enrollmentDir(configDir, only.Name)
		if len(subdir) > 5 {
			_ = os.RemoveAll(subdir)
		}
		return reg.Remove(only.Name)
	}

	return fmt.Errorf("--force without --name is ambiguous: %d enrollments exist (%v). Specify --name <enrollment> to target one.", reg.Len(), reg.Names())
}

// stopAgentService stops the running agent service so --force can
// safely wipe its config. Best-effort: errors (e.g. service not
// installed) are swallowed.
func stopAgentService() {
	if runtime.GOOS == "darwin" {
		exec.Command("launchctl", "unload", "/Library/LaunchDaemons/com.hopssh.agent.plist").Run()
	} else {
		exec.Command("systemctl", "stop", "hop-agent").Run()
	}
}

// chooseEnrollmentName resolves the local name for a new enrollment.
// Priority: explicit --name → preferred (DNS domain or CA fingerprint)
// with -2/-3/… suffix if the preferred name collides with an existing
// entry. Returns an error only for truly impossible cases.
func chooseEnrollmentName(reg *enrollmentRegistry, explicit, preferred string) (string, error) {
	if explicit != "" {
		if err := validateEnrollmentName(explicit); err != nil {
			return "", err
		}
		if reg.Get(explicit) != nil {
			return "", fmt.Errorf("enrollment %q already exists", explicit)
		}
		return explicit, nil
	}
	if err := validateEnrollmentName(preferred); err != nil {
		return "", fmt.Errorf("preferred enrollment name %q is not valid: %w", preferred, err)
	}
	if reg.Get(preferred) == nil {
		return preferred, nil
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", preferred, i)
		if validateEnrollmentName(candidate) != nil {
			continue
		}
		if reg.Get(candidate) == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free enrollment name derived from %q", preferred)
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
//
// Bundles ship with a legacy flat layout (files at the top of
// configDir). We extract in place, then fold the files into a
// per-network subdir and register the enrollment. Only supported on
// fresh installs (registry empty) in v0.10.0 — adding a second network
// via bundle would require the bundle generator to know the target
// subdir name, which isn't plumbed through the server.
func enrollFromBundle(path, endpoint string) {
	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		log.Fatalf("Load enrollments: %v", err)
	}
	if reg.Len() > 0 {
		log.Fatalf("Bundle enrollment only supports fresh installs in v0.10.0 (existing enrollments: %v). Use token-based enrollment to add networks, or `hop-agent leave` first.", reg.Names())
	}

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

	// Determine enrollment name from bundle metadata (DNS domain fallback CA fingerprint).
	caCertPEM, err := os.ReadFile(filepath.Join(configDir, "ca.crt"))
	if err != nil {
		log.Fatalf("Read bundle ca.crt: %v", err)
	}
	fingerprint := caFingerprint(caCertPEM)
	preferred := defaultEnrollmentName(bundleConfig.DNSDomain, fingerprint)
	name, err := chooseEnrollmentName(reg, enrollName, preferred)
	if err != nil {
		log.Fatalf("%v", err)
	}

	enrollDir := enrollmentDir(configDir, name)
	if err := os.MkdirAll(enrollDir, 0700); err != nil {
		log.Fatalf("Create enrollment dir %s: %v", enrollDir, err)
	}

	// Move the files the bundle delivered from the flat layout into the subdir.
	bundledFiles := []string{"ca.crt", "node.crt", "node.key", "token", "config.json"}
	for _, f := range bundledFiles {
		src := filepath.Join(configDir, f)
		dst := filepath.Join(enrollDir, f)
		if _, err := os.Stat(src); err != nil {
			continue // file wasn't in the bundle
		}
		if err := os.Rename(src, dst); err != nil {
			log.Fatalf("Move %s → %s: %v", src, dst, err)
		}
	}

	// Persist endpoint + nodeID for cert renewal.
	writeFileSecure(filepath.Join(enrollDir, "endpoint"), []byte(ep), 0600)
	if bundleConfig.NodeID != "" {
		writeFileSecure(filepath.Join(enrollDir, "node-id"), []byte(bundleConfig.NodeID), 0600)
	}

	// Generate Nebula config from bundle data.
	serverHost := bundleConfig.LighthouseHost
	if serverHost == "" {
		serverHost = extractHost(ep)
	}
	// Bundle path only supports first enrollment (checked above), so
	// always use the primary listen port.
	writeNebulaConfig(enrollDir, bundleConfig.ServerIP, serverHost, bundleConfig.LighthousePort, enrollTunMode, nebulacfg.ListenPort)
	writeDNSConfig(enrollDir, bundleConfig.DNSDomain, serverHost, bundleConfig.LighthousePort)

	if err := reg.Add(&Enrollment{
		Name:          name,
		NodeID:        bundleConfig.NodeID,
		Endpoint:      ep,
		TunMode:       enrollTunMode,
		CAFingerprint: fingerprint,
		DNSDomain:     bundleConfig.DNSDomain,
		EnrolledAt:    time.Now().UTC(),
	}); err != nil {
		log.Fatalf("Register enrollment: %v", err)
	}

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
	if err := os.MkdirAll(configDir, 0700); err != nil {
		log.Fatalf("Create config dir %s: %v", configDir, err)
	}

	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		log.Fatalf("Load enrollments: %v", err)
	}

	fingerprint := caFingerprint([]byte(er.CACert))
	preferred := defaultEnrollmentName(er.DNSDomain, fingerprint)
	name, err := chooseEnrollmentName(reg, enrollName, preferred)
	if err != nil {
		log.Fatalf("%v", err)
	}

	enrollDir := enrollmentDir(configDir, name)
	if err := os.MkdirAll(enrollDir, 0700); err != nil {
		log.Fatalf("Create enrollment dir %s: %v", enrollDir, err)
	}

	writeFileSecure(filepath.Join(enrollDir, "ca.crt"), []byte(er.CACert), 0644)
	writeFileSecure(filepath.Join(enrollDir, "node.crt"), []byte(er.NodeCert), 0644)
	writeFileSecure(filepath.Join(enrollDir, "node.key"), []byte(er.NodeKey), 0600)
	writeFileSecure(filepath.Join(enrollDir, "token"), []byte(er.AgentToken), 0600)
	writeFileSecure(filepath.Join(enrollDir, "endpoint"), []byte(endpoint), 0600)
	if er.NodeID != "" {
		writeFileSecure(filepath.Join(enrollDir, "node-id"), []byte(er.NodeID), 0600)
	}

	fmt.Printf("\n  ✓ Enrolled %q (%s)\n", name, er.NebulaIP)

	serverHost := er.LighthouseHost
	if serverHost == "" {
		serverHost = extractHost(endpoint)
	}
	// First enrollment keeps the fixed Nebula listen port for NAT
	// mapping stability; additional enrollments use port 0 (OS-assigned)
	// since we can't bind two instances to the same UDP port.
	listenPort := nebulacfg.ListenPort
	if reg.Len() > 0 {
		listenPort = 0
	}
	writeNebulaConfig(enrollDir, er.ServerIP, serverHost, er.LighthousePort, enrollTunMode, listenPort)
	writeDNSConfig(enrollDir, er.DNSDomain, serverHost, er.LighthousePort)

	if err := reg.Add(&Enrollment{
		Name:          name,
		NodeID:        er.NodeID,
		Endpoint:      endpoint,
		TunMode:       enrollTunMode,
		CAFingerprint: fingerprint,
		DNSDomain:     er.DNSDomain,
		EnrolledAt:    time.Now().UTC(),
	}); err != nil {
		log.Fatalf("Register enrollment: %v", err)
	}

	installService()
	fmt.Println("  ✓ Agent started")
}

func writeNebulaConfig(enrollDir, serverIP, serverHost string, lighthousePort int, tunMode string, listenPort int) {
	// Detect physical interface to prevent advertising overlay IPs.
	physicalIface, err := nebulacfg.DetectPhysicalInterface(serverHost)
	if err != nil {
		log.Printf("  Warning: could not detect physical interface: %v", err)
	} else {
		fmt.Printf("  ✓ Detected physical interface: %s\n", physicalIface)
	}

	if lighthousePort == 0 {
		lighthousePort = 41820 // fallback for legacy enrollment
	}

	tunConfig := "  user: true"
	if tunMode == "kernel" {
		// Each enrollment gets its own utun/wintun interface. macOS and
		// WinTun both assign a unique device at create time; on Linux
		// we use a per-enrollment name to avoid collisions when one
		// agent is enrolled in N networks.
		tunConfig = fmt.Sprintf("  dev: nebula1\n  mtu: %d", nebulacfg.TunMTU)
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
%s
%s
relay:
  relays:
    - "%s"
  use_relays: %t

cipher: %s

listen:
  host: 0.0.0.0
  port: %d

routines: %d

handshakes:
  try_interval: %s

punchy:
  punch: true
  respond: true
  punch_back: %t
  delay: %s
  respond_delay: %s
  target_all_remotes: true

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
`, enrollDir, enrollDir, enrollDir,
		serverIP, serverHost, lighthousePort,
		serverIP,
		nebulacfg.LocalAllowListYAML(physicalIface),
		nebulacfg.PreferredRangesYAML,
		serverIP, nebulacfg.UseRelays,
		nebulacfg.Cipher,
		listenPort,
		nebulacfg.Routines,
		nebulacfg.HandshakeTryInterval,
		nebulacfg.PunchBack, nebulacfg.PunchDelay, nebulacfg.RespondDelay,
		tunConfig,
		agentAPIPort)

	writeFileSecure(filepath.Join(enrollDir, "nebula.yaml"), []byte(nebulaConfig), 0644)

	// Persist TUN mode so serve knows which mode to use.
	writeFileSecure(filepath.Join(enrollDir, "tun-mode"), []byte(tunMode), 0644)

	fmt.Printf("  ✓ Nebula config written (lighthouse: %s via %s:%d, tun: %s)\n", serverIP, serverHost, lighthousePort, tunMode)
}

// writeDNSConfig persists DNS configuration so the agent can set up split-DNS
// on serve. The DNS server runs on the control plane at lighthouseHost:dnsPort.
func writeDNSConfig(enrollDir, dnsDomain, lighthouseHost string, lighthousePort int) {
	if dnsDomain == "" {
		return
	}
	// DNS port follows the same offset scheme as the lighthouse port.
	const baseLighthousePort = 42001
	const baseDNSPort = 15300
	dnsPort := baseDNSPort + (lighthousePort - baseLighthousePort)

	writeFileSecure(filepath.Join(enrollDir, "dns-domain"), []byte(dnsDomain), 0644)
	writeFileSecure(filepath.Join(enrollDir, "dns-server"), []byte(fmt.Sprintf("%s:%d", lighthouseHost, dnsPort)), 0644)
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
