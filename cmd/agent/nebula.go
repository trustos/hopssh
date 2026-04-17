package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/cert"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"
	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"
)

// meshService abstracts the Nebula mesh connection.
// Two implementations: userspace (gvisor netstack) and kernel TUN (OS interface).
type meshService interface {
	Listen(network, address string) (net.Listener, error)
	Close()
	NebulaControl() *nebula.Control
}

// currentNebula is the running embedded Nebula instance.
// Protected by nebulaMu for concurrent access from main and renewal goroutines.
var (
	currentNebula meshService
	nebulaMu      sync.Mutex
)

// onNebulaRestart is called after Nebula is successfully restarted (cert renewal).
// Set by runServe() to recreate the mesh HTTP listener.
var onNebulaRestart func(svc meshService)

// activeDNSConfig holds the current split-DNS configuration (kernel TUN mode).
// Set during Nebula startup (runServe or reloadNebula cold-start), cleaned up
// on agent shutdown. Package-level so reloadNebula() can configure DNS when
// starting Nebula for the first time after a cert-expired boot.
var activeDNSConfig *dnsConfig

// --- Userspace mode (gvisor netstack) ---

// userspaceMeshService wraps an embedded Nebula userspace instance.
type userspaceMeshService struct {
	svc *service.Service
}

// startNebula starts an embedded Nebula instance in userspace mode.
// The agent joins the mesh as a regular node (not lighthouse).
func startNebula(configPath string) (*userspaceMeshService, error) {
	var cfg config.C
	if err := cfg.Load(configPath); err != nil {
		return nil, fmt.Errorf("load nebula config %s: %w", configPath, err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	ctrl, err := nebula.Main(&cfg, false, "hop-agent", logger, overlay.NewUserDeviceFromConfig)
	if err != nil {
		return nil, fmt.Errorf("start nebula: %w", err)
	}

	svc, err := service.New(ctrl)
	if err != nil {
		return nil, fmt.Errorf("create nebula service: %w", err)
	}

	return &userspaceMeshService{svc: svc}, nil
}

// Listen creates a TCP listener on the Nebula mesh's userspace network stack.
func (u *userspaceMeshService) Listen(network, address string) (net.Listener, error) {
	return u.svc.Listen(network, address)
}

func (u *userspaceMeshService) NebulaControl() *nebula.Control { return nil }

// Close shuts down the Nebula instance gracefully.
func (u *userspaceMeshService) Close() {
	log.Printf("[agent] stopping Nebula mesh connection (userspace)")
	u.svc.Close()
}

// --- Kernel TUN mode (real OS network interface) ---

// kernelTunMeshService wraps Nebula with a kernel TUN device.
// The mesh IP is routable at the OS level (utun on macOS, tun on Linux).
type kernelTunMeshService struct {
	ctrl   *nebula.Control
	meshIP string
}

// startNebulaKernelTun starts Nebula with a kernel TUN device.
// This creates a real network interface with the mesh IP assigned.
func startNebulaKernelTun(configPath string) (*kernelTunMeshService, error) {
	var cfg config.C
	if err := cfg.Load(configPath); err != nil {
		return nil, fmt.Errorf("load nebula config %s: %w", configPath, err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	ctrl, err := nebula.Main(&cfg, false, "hop-agent", logger, overlay.NewDeviceFromConfig)
	if err != nil {
		return nil, fmt.Errorf("start nebula (kernel TUN): %w", err)
	}

	ctrl.Start()

	meshIP, err := readMeshIPFromCert(configPath)
	if err != nil {
		ctrl.Stop()
		return nil, fmt.Errorf("read mesh IP from cert: %w", err)
	}

	log.Printf("[agent] kernel TUN interface created (mesh IP: %s)", meshIP)
	return &kernelTunMeshService{ctrl: ctrl, meshIP: meshIP}, nil
}

// Listen creates a TCP listener on the OS network stack bound to the mesh IP.
func (k *kernelTunMeshService) Listen(network, address string) (net.Listener, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse address %q: %w", address, err)
	}
	addr := net.JoinHostPort(k.meshIP, port)
	return net.Listen(network, addr)
}

func (k *kernelTunMeshService) NebulaControl() *nebula.Control { return k.ctrl }

// Close shuts down the Nebula instance and destroys the TUN interface.
func (k *kernelTunMeshService) Close() {
	log.Printf("[agent] stopping Nebula mesh connection (kernel TUN)")
	k.ctrl.Stop()
}

// --- Network Change Detection ---

// watchNetworkChanges polls local network interfaces and triggers a Nebula
// rebind when the active interface or IP changes. This handles WiFi↔cellular
// switches — without it, Nebula stays on a stale relay tunnel until the
// connection times out (minutes).
func watchNetworkChanges(ctrl *nebula.Control, endpoint string) {
	host := extractHost(endpoint)
	if host == "" {
		return
	}

	lastIface, _ := nebulacfg.DetectPhysicalInterface(host)
	lastAddrs := getLocalAddrs()
	lastTick := time.Now()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		// Detect sleep/wake: if the ticker fires and the gap since the
		// last tick is >15s (3× the 5s interval), the process was suspended
		// — almost certainly a macOS/Windows sleep cycle. Force a rebind
		// even if the network fingerprint hasn't changed, because the
		// underlying UDP sockets are stale (NAT mappings expired, lighthouse
		// handshakes will fail on the old socket).
		sleptAndWoke := now.Sub(lastTick) > 15*time.Second
		lastTick = now

		currentIface, _ := nebulacfg.DetectPhysicalInterface(host)
		currentAddrs := getLocalAddrs()

		addrChanged := currentIface != lastIface || currentAddrs != lastAddrs

		if addrChanged || sleptAndWoke {
			reason := "network change"
			if sleptAndWoke && !addrChanged {
				reason = fmt.Sprintf("sleep/wake detected (tick gap %v)", now.Sub(lastTick).Round(time.Second))
			}
			log.Printf("[agent] %s detected (iface: %s→%s), rebinding Nebula", reason, lastIface, currentIface)
			ctrl.RebindUDPServer()
			closed := ctrl.CloseAllTunnels(true)
			if closed > 0 {
				log.Printf("[agent] closed %d tunnels to force re-handshake on new network", closed)
			}
			lastIface = currentIface
			lastAddrs = currentAddrs
		}
	}
}

// getLocalAddrs returns a string fingerprint of current local IPv4 addresses.
func getLocalAddrs() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var s string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			s += addr.String() + ","
		}
	}
	return s
}

// --- Helpers ---

// readMeshIPFromCert extracts the VPN IP from the node certificate.
func readMeshIPFromCert(nebulaConfigPath string) (string, error) {
	// Derive cert path from config directory (same directory as nebula.yaml).
	dir := filepath.Dir(nebulaConfigPath)
	certPath := filepath.Join(dir, "node.crt")

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf("read cert %s: %w", certPath, err)
	}

	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return "", fmt.Errorf("parse cert: %w", err)
	}

	networks := c.Networks()
	if len(networks) == 0 {
		return "", fmt.Errorf("cert has no networks")
	}

	return networks[0].Addr().String(), nil
}

// readTunMode determines the TUN mode for the current run.
// Auto-upgrades to kernel mode when running as root, even if enrollment
// happened as non-root. Kernel TUN uses a real OS network interface (utun)
// with near-zero per-packet overhead. Userspace mode (gvisor netstack) adds
// ~4ms latency per packet, which degrades VNC/Screen Sharing and similar workloads.
func readTunMode() string {
	data, err := os.ReadFile(filepath.Join(configDir, "tun-mode"))
	if err != nil {
		if isPrivileged() {
			return "kernel"
		}
		return "userspace"
	}

	mode := strings.TrimSpace(string(data))

	// Auto-upgrade: if persisted mode is userspace but we're running as root,
	// switch to kernel mode for better performance. This handles the common case
	// where the agent was enrolled as a regular user but later installed as a
	// root system service.
	if mode == "userspace" && isPrivileged() {
		log.Printf("[agent] upgrading to kernel TUN mode (running as root)")
		upgradeTunMode()
		return "kernel"
	}

	if mode == "kernel" {
		return "kernel"
	}
	return "userspace"
}

// upgradeTunMode switches the persisted TUN mode from userspace to kernel
// and updates nebula.yaml accordingly. Preserves all other config (including
// which may update nebula.yaml via upgradeTunMode).
func upgradeTunMode() {
	// Update the persisted tun-mode file.
	tunModePath := filepath.Join(configDir, "tun-mode")
	if err := os.WriteFile(tunModePath, []byte("kernel"), 0644); err != nil {
		log.Printf("[agent] WARNING: failed to update tun-mode file: %v", err)
	}

	// Update nebula.yaml: replace tun section only.
	configPath := filepath.Join(configDir, "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return
	}

	// Only update the tun section — preserve everything else.
	cfg["tun"] = map[string]interface{}{
		"dev": "utun",
		"mtu": nebulacfg.TunMTU,
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return
	}

	if err := atomicWrite(configPath, out, 0644); err != nil {
		log.Printf("[agent] WARNING: failed to update nebula.yaml for kernel TUN: %v", err)
	}
}
