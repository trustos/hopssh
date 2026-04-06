package mesh

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"
	"github.com/trustos/hopssh/internal/db"
)

const (
	baseLighthousePort = 42001
	agentAPIPort       = 41820
)

// NetworkInstance represents a running Nebula lighthouse+relay for a single network.
type NetworkInstance struct {
	NetworkID string
	Slug      string
	UDPPort   int
	DNSDomain string

	svc    *service.Service
	cancel context.CancelFunc
}

// Dial opens a TCP connection to a node's agent API through the mesh.
func (ni *NetworkInstance) Dial(ctx context.Context, nebulaIP string) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", nebulaIP, agentAPIPort)
	return ni.svc.DialContext(ctx, "tcp", addr)
}

// DialPort opens a TCP connection to an arbitrary port on a mesh IP.
func (ni *NetworkInstance) DialPort(ctx context.Context, nebulaIP string, port int) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", nebulaIP, port)
	return ni.svc.DialContext(ctx, "tcp", addr)
}

// HTTPClient returns an http.Client that dials through the mesh to a specific node.
func (ni *NetworkInstance) HTTPClient(nebulaIP string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return ni.Dial(ctx, nebulaIP)
			},
		},
	}
}

// Close shuts down the Nebula instance gracefully.
func (ni *NetworkInstance) Close() {
	log.Printf("[mesh] stopping lighthouse for network %s (port %d)", ni.Slug, ni.UDPPort)
	ni.cancel()
	ni.svc.Close()
}

// NetworkManager manages persistent Nebula lighthouse+relay instances, one per network.
type NetworkManager struct {
	networks *db.NetworkStore

	mu        sync.RWMutex
	instances map[string]*NetworkInstance // keyed by network ID
}

// NewNetworkManager creates a NetworkManager and starts lighthouse instances for all existing networks.
func NewNetworkManager(networks *db.NetworkStore) (*NetworkManager, error) {
	nm := &NetworkManager{
		networks:  networks,
		instances: make(map[string]*NetworkInstance),
	}

	// Start lighthouses for all existing networks.
	allNetworks, err := networks.ListAll()
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}

	for _, n := range allNetworks {
		if n.LighthousePort == nil {
			log.Printf("[mesh] network %s (%s) has no lighthouse port, skipping", n.Slug, n.ID)
			continue
		}
		if err := nm.startInstance(n); err != nil {
			log.Printf("[mesh] failed to start lighthouse for network %s: %v", n.Slug, err)
			// Continue — don't fail startup because one network's lighthouse fails
		}
	}

	log.Printf("[mesh] NetworkManager started with %d lighthouse instances", len(nm.instances))
	return nm, nil
}

// StartNetwork starts a lighthouse instance for a network. Called when a network is created.
func (nm *NetworkManager) StartNetwork(n *db.Network) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if _, exists := nm.instances[n.ID]; exists {
		return fmt.Errorf("lighthouse for network %s already running", n.Slug)
	}

	return nm.startInstance(n)
}

// StopNetwork stops and removes a lighthouse instance. Called when a network is deleted.
func (nm *NetworkManager) StopNetwork(networkID string) {
	nm.mu.Lock()
	inst, ok := nm.instances[networkID]
	if ok {
		delete(nm.instances, networkID)
	}
	nm.mu.Unlock()

	if ok {
		inst.Close()
	}
}

// GetInstance returns the running NetworkInstance for a network.
func (nm *NetworkManager) GetInstance(networkID string) (*NetworkInstance, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	inst, ok := nm.instances[networkID]
	if !ok {
		return nil, fmt.Errorf("no lighthouse running for network %s", networkID)
	}
	return inst, nil
}

// Stop shuts down all lighthouse instances.
func (nm *NetworkManager) Stop() {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	for id, inst := range nm.instances {
		inst.Close()
		delete(nm.instances, id)
	}
	log.Printf("[mesh] NetworkManager stopped")
}

// AllocatePort returns the next available lighthouse UDP port.
func (nm *NetworkManager) AllocatePort() (int, error) {
	maxPort, err := nm.networks.MaxLighthousePort()
	if err != nil {
		return 0, fmt.Errorf("query max lighthouse port: %w", err)
	}
	if maxPort == 0 {
		return baseLighthousePort, nil
	}
	return maxPort + 1, nil
}

// startInstance starts a Nebula userspace lighthouse+relay for a network.
// Must be called with nm.mu held.
func (nm *NetworkManager) startInstance(n *db.Network) error {
	if n.LighthousePort == nil {
		return fmt.Errorf("network %s has no lighthouse port", n.Slug)
	}
	port := int(*n.LighthousePort)

	cfgStr := fmt.Sprintf(`
tun:
  user: true
pki:
  ca: |
%s
  cert: |
%s
  key: |
%s
listen:
  host: 0.0.0.0
  port: %d
lighthouse:
  am_lighthouse: true
relay:
  am_relay: true
punchy:
  punch: true
  respond: true
firewall:
  outbound:
    - port: any
      proto: any
      host: any
  inbound:
    - port: any
      proto: any
      host: any
`,
		indentPEM(string(n.NebulaCACert), 4),
		indentPEM(string(n.ServerCert), 4),
		indentPEM(string(n.ServerKey), 4),
		port,
	)

	var cfg config.C
	if err := cfg.LoadString(cfgStr); err != nil {
		return fmt.Errorf("load nebula config: %w", err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	label := fmt.Sprintf("hopssh-lighthouse-%s", n.Slug)
	ctrl, err := nebula.Main(&cfg, false, label, logger, overlay.NewUserDeviceFromConfig)
	if err != nil {
		return fmt.Errorf("start nebula: %w", err)
	}

	svc, err := service.New(ctrl)
	if err != nil {
		return fmt.Errorf("create nebula service: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx // context reserved for future use (DNS server, monitoring)

	inst := &NetworkInstance{
		NetworkID: n.ID,
		Slug:      n.Slug,
		UDPPort:   port,
		DNSDomain: n.DNSDomain,
		svc:       svc,
		cancel:    cancel,
	}

	nm.instances[n.ID] = inst
	log.Printf("[mesh] lighthouse started for network %s (%s) on UDP :%d, DNS domain: .%s",
		n.Slug, n.ID[:8], port, n.DNSDomain)

	return nil
}
