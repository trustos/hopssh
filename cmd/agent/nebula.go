package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	// DevName returns the OS-level interface name for kernel TUN mode
	// (e.g. "utun10" on macOS, "hop-home" on Linux). Returns "" for
	// userspace mode (gvisor has no OS interface). watchNetworkChanges
	// uses this to detect when the local tunnel has dropped its UP flag.
	DevName() string
}

// Per-instance Nebula state (currentNebula, heartbeatTrigger,
// onNebulaRestart, activeDNSConfig) used to live here as package-level
// globals back when one agent process held one enrollment. Multi-
// network-per-agent (roadmap #29) promoted all of that to fields on
// meshInstance — see instance.go. Keep this note so future readers
// don't go looking for the old globals.

// --- Userspace mode (gvisor netstack) ---

// userspaceMeshService wraps an embedded Nebula userspace instance.
type userspaceMeshService struct {
	svc  *service.Service
	ctrl *nebula.Control // kept so NebulaControl() can expose peer state
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

	return &userspaceMeshService{svc: svc, ctrl: ctrl}, nil
}

// Listen creates a TCP listener on the Nebula mesh's userspace network stack.
func (u *userspaceMeshService) Listen(network, address string) (net.Listener, error) {
	return u.svc.Listen(network, address)
}

// NebulaControl exposes the underlying Nebula control for read-only
// inspection (peer state, host map). The returned *Control is the same
// one service.Service wraps; safe to call concurrently with normal
// mesh traffic.
func (u *userspaceMeshService) NebulaControl() *nebula.Control { return u.ctrl }

// DevName returns empty — userspace mode has no OS interface.
func (u *userspaceMeshService) DevName() string { return "" }

// Close shuts down the Nebula instance gracefully.
func (u *userspaceMeshService) Close() {
	log.Printf("[agent] stopping Nebula mesh connection (userspace)")
	u.svc.Close()
}

// --- Kernel TUN mode (real OS network interface) ---

// kernelTunMeshService wraps Nebula with a kernel TUN device.
// The mesh IP is routable at the OS level (utun on macOS, tun on Linux).
type kernelTunMeshService struct {
	ctrl    *nebula.Control
	meshIP  string
	devName string // OS interface name (e.g. "utun10", "hop-home")
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

	// Discover the OS-level interface name by finding which interface
	// got the mesh IP assigned. On macOS the kernel auto-assigns utunN
	// (the tun.dev field in nebula.yaml is ignored), so we can't
	// predict the name — we have to look it up.
	devName := findInterfaceByIP(meshIP)
	if devName == "" {
		log.Printf("[agent] kernel TUN interface created (mesh IP: %s, name: unknown)", meshIP)
	} else {
		log.Printf("[agent] kernel TUN interface created (mesh IP: %s, name: %s)", meshIP, devName)
	}
	return &kernelTunMeshService{ctrl: ctrl, meshIP: meshIP, devName: devName}, nil
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

// DevName returns the OS-level interface name (e.g. "utun10") owned
// by this kernel-TUN service. Empty if discovery failed at startup.
func (k *kernelTunMeshService) DevName() string { return k.devName }

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
//
// Scoped to one meshInstance — each instance runs its own watcher
// against its own Control, pokes its own heartbeat channel. Exits
// when ctx is cancelled so `hop-agent leave` and cert-renewal reloads
// don't leak the goroutine or keep it calling methods on a stopped
// Nebula Control.
func watchNetworkChanges(ctx context.Context, inst *meshInstance, ctrl *nebula.Control) {
	host := extractHost(inst.endpoint())
	if host == "" {
		return
	}

	lastIface, _ := nebulacfg.DetectPhysicalInterface(host)
	lastAddrs := getLocalAddrs(lastIface)
	lastTick := time.Now()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	tickCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		tickCount++
		now := time.Now()

		// Detect sleep/wake: if the ticker fires and the gap since the
		// last tick is >15s (3× the 5s interval), the process was suspended
		// — almost certainly a macOS/Windows sleep cycle. Force a rebind
		// even if the network fingerprint hasn't changed, because the
		// underlying UDP sockets are stale (NAT mappings expired, lighthouse
		// handshakes will fail on the old socket).
		tickGap := now.Sub(lastTick)
		sleptAndWoke := tickGap > 15*time.Second
		lastTick = now

		currentIface, _ := nebulacfg.DetectPhysicalInterface(host)
		currentAddrs := getLocalAddrs(currentIface)

		addrChanged := currentIface != lastIface || currentAddrs != lastAddrs

		if addrChanged || sleptAndWoke {
			reason := "network change"
			if sleptAndWoke && !addrChanged {
				reason = fmt.Sprintf("sleep/wake detected (tick gap %v)", tickGap.Round(time.Second))
			}
			log.Printf("[agent %s] %s detected (iface: %s→%s), rebinding Nebula", inst.name(), reason, lastIface, currentIface)
			ctrl.RebindUDPServer()
			closed := ctrl.CloseAllTunnels(true)
			if closed > 0 {
				log.Printf("[agent %s] closed %d tunnels to force re-handshake on new network", inst.name(), closed)
			}
			// Re-run the portmap probe. A network change may mean:
			//   (a) we moved to a router that supports a different
			//       mapping protocol, so the current winner is dead;
			//   (b) our old router reassigned our external IP;
			//   (c) we're now on cellular with no portmap at all.
			// In all three cases holding the stale mapping produces a
			// lighthouse advertise_addr that peers can't reach.
			if inst.portmap != nil {
				inst.portmap.ReProbe()
			}
			// Poke the heartbeat goroutine so the dashboard learns the
			// node's real state within seconds instead of waiting up to
			// one full heartbeat interval.
			inst.signalHeartbeat()
			lastIface = currentIface
			lastAddrs = currentAddrs
		}

		// Independent of addrChanged: detect when OUR OWN kernel-TUN
		// device has dropped its UP flag. Rebind + CloseAllTunnels
		// above can't recover this — the TUN device itself needs to
		// be recreated, which only reloadNebula does.
		//
		// Skipped during startup grace (Nebula's TUN bring-up is not
		// instantaneous) and in userspace mode (no OS interface).
		if tickCount > watcherStartupGraceTicks {
			svc := inst.currentSvc()
			if svc != nil {
				if devName := svc.DevName(); devName != "" && !isInterfaceUp(devName) {
					if inst.shouldAutoReload() {
						log.Printf("[agent %s] kernel TUN %s has lost UP flag; reloading Nebula to recover", inst.name(), devName)
						triggerReload(inst)
						// Our watcher's ctx will be cancelled by the reload
						// (stopWatcher). Exit now; the new watcher reloadNebula
						// spawns will take over against the fresh ctrl.
						return
					}
					log.Printf("[agent %s] kernel TUN %s is DOWN but within reload cooldown; skipping", inst.name(), devName)
				}
			}
		}
	}
}

// findInterfaceByIP returns the name of the interface that has the
// given IPv4 address assigned, or "" if none. Used to discover the
// macOS-assigned utun name at startup (tun.dev is ignored on macOS).
func findInterfaceByIP(ip string) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var host string
			switch v := addr.(type) {
			case *net.IPNet:
				host = v.IP.String()
			case *net.IPAddr:
				host = v.IP.String()
			}
			if host == ip {
				return iface.Name
			}
		}
	}
	return ""
}

// isInterfaceUp reports whether the named interface currently has the
// FlagUp bit set. Returns false on lookup error (e.g. interface
// deleted) — the caller treats that the same as "down" for recovery
// purposes.
//
// Exposed as a var so tests can stub this without a real OS interface.
var isInterfaceUp = func(name string) bool {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return false
	}
	return iface.Flags&net.FlagUp != 0
}

// triggerReload kicks off reloadNebula in its own goroutine. Exposed
// as a var so tests can stub the side-effecting reload call without
// having to bring up a real Nebula instance.
//
// Assigned in init() because a self-referential package-var initializer
// (via reloadNebula → startWatcher → watchNetworkChanges → triggerReload)
// would be a Go "initialization cycle" error.
var triggerReload func(inst *meshInstance)

func init() {
	triggerReload = func(inst *meshInstance) {
		go reloadNebula(inst)
	}
}

// watcherStartupGraceTicks is the number of ticks the watcher ignores
// the utun-UP check after (re)starting. Gives Nebula a window to bring
// up the kernel TUN without us racing it with a reload. Exposed as a
// var so tests can shrink it.
var watcherStartupGraceTicks = 3

// getLocalAddrs returns a string fingerprint of the physical interface's
// IPv4 state. Intentionally narrow: we want to detect a REAL network
// change (WiFi↔cellular swap, DHCP renewal, Ethernet unplug) and ignore
// irrelevant churn.
//
// Why only the physical interface's IPv4:
//
//   - macOS laptops accumulate transient utun interfaces from
//     conferencing/VPN apps (Zoom, Slack, Teams, work VPN, etc.).
//     Each comes up with an IPv6 link-local (`fe80::.../64`) that can
//     flap whenever the app is backgrounded, a call ends, or the
//     route monitor re-enumerates. Counting those as "network change"
//     caused ~40 spurious rebinds per day on a developer laptop vs
//     ~10 on a desktop — each rebind tears every Nebula tunnel AND
//     triggers macOS `SCDynamicStore` notifications which Chrome's
//     NetworkChangeNotifier reads as a reason to flush its socket
//     pool, manifesting as ERR_NETWORK_CHANGED in the browser.
//   - IPv6 link-local/SLAAC addresses on our primary WiFi/Ethernet
//     interface also churn (temporary privacy addresses rotate),
//     without any real routing change. Drop those too.
//   - We pass the physical interface in as a parameter so the caller
//     can use the same DetectPhysicalInterface result it already
//     computed for the iface-change check — one system call, not two.
func getLocalAddrs(physicalIface string) string {
	if physicalIface == "" {
		// DetectPhysicalInterface failed this tick. Return a sentinel
		// that tracks the failure state rather than "all addresses",
		// so we don't react to IPv6 churn during brief connectivity
		// hiccups. The iface-change branch above already covers the
		// "" → en0 recovery.
		return ""
	}
	iface, err := net.InterfaceByName(physicalIface)
	if err != nil {
		return ""
	}
	if iface.Flags&net.FlagUp == 0 {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	var s string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		s += ip4.String() + "/" + ipNet.Mask.String() + ","
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
func readTunMode(inst *meshInstance) string {
	data, err := os.ReadFile(filepath.Join(inst.dir(), "tun-mode"))
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
		log.Printf("[agent %s] upgrading to kernel TUN mode (running as root)", inst.name())
		upgradeTunMode(inst)
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
func upgradeTunMode(inst *meshInstance) {
	dir := inst.dir()
	// Update the persisted tun-mode file.
	tunModePath := filepath.Join(dir, "tun-mode")
	if err := os.WriteFile(tunModePath, []byte("kernel"), 0644); err != nil {
		log.Printf("[agent %s] WARNING: failed to update tun-mode file: %v", inst.name(), err)
	}

	// Update nebula.yaml: replace tun section only.
	configPath := filepath.Join(dir, "nebula.yaml")
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
