package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// Close shuts down the Nebula instance and destroys the TUN interface.
func (k *kernelTunMeshService) Close() {
	log.Printf("[agent] stopping Nebula mesh connection (kernel TUN)")
	k.ctrl.Stop()
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
// and updates nebula.yaml accordingly.
func upgradeTunMode() {
	// Update the persisted tun-mode file.
	tunModePath := filepath.Join(configDir, "tun-mode")
	if err := os.WriteFile(tunModePath, []byte("kernel"), 0644); err != nil {
		log.Printf("[agent] WARNING: failed to update tun-mode file: %v", err)
	}

	// Update nebula.yaml: replace tun.user=true with tun.dev+mtu.
	configPath := filepath.Join(configDir, "nebula.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return
	}

	cfg["tun"] = map[string]interface{}{
		"dev": "nebula1",
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
