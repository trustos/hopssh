package mesh

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
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
	baseDNSPort        = 15300 // DNS port = baseDNSPort + (lighthousePort - baseLighthousePort)
	agentAPIPort       = 41820
)

// NetworkInstance represents a running Nebula lighthouse+relay for a single network.
type NetworkInstance struct {
	NetworkID string
	Slug      string
	UDPPort   int
	DNSDomain string

	svc *service.Service
	dns *MeshDNS
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
	if ni.dns != nil {
		ni.dns.Stop()
	}
	ni.svc.Close()
}

// DNS returns the mesh DNS server for this network (for record updates).
func (ni *NetworkInstance) DNS() *MeshDNS {
	return ni.dns
}

// NetworkManager manages persistent Nebula lighthouse+relay instances, one per network.
type NetworkManager struct {
	networks   *db.NetworkStore
	nodes      *db.NodeStore
	dnsRecords *db.DNSRecordStore

	mu        sync.RWMutex
	instances map[string]*NetworkInstance // keyed by network ID
}

// NewNetworkManager creates a NetworkManager and starts lighthouse instances for all existing networks.
func NewNetworkManager(networks *db.NetworkStore, nodes *db.NodeStore, dnsRecords *db.DNSRecordStore) (*NetworkManager, error) {
	nm := &NetworkManager{
		networks:   networks,
		nodes:      nodes,
		dnsRecords: dnsRecords,
		instances:  make(map[string]*NetworkInstance),
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
			continue
		}
		// Load DNS records for this network.
		nm.RefreshDNS(n.ID)
	}

	log.Printf("[mesh] NetworkManager started with %d lighthouse instances", len(nm.instances))
	return nm, nil
}

// StartNetwork starts a lighthouse instance for a network. Called when a network is created.
func (nm *NetworkManager) StartNetwork(n *db.Network) error {
	nm.mu.Lock()
	if _, exists := nm.instances[n.ID]; exists {
		nm.mu.Unlock()
		return fmt.Errorf("lighthouse for network %s already running", n.Slug)
	}
	// Insert a nil sentinel to prevent concurrent StartNetwork for the same ID.
	nm.instances[n.ID] = nil
	nm.mu.Unlock()

	err := nm.startInstance(n)
	if err != nil {
		// Remove sentinel on failure.
		nm.mu.Lock()
		if nm.instances[n.ID] == nil {
			delete(nm.instances, n.ID)
		}
		nm.mu.Unlock()
	}
	return err
}

// GetInstance returns the running NetworkInstance for a network.
// Returns an error if the instance doesn't exist or is still starting.
func (nm *NetworkManager) GetInstance(networkID string) (*NetworkInstance, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	inst, ok := nm.instances[networkID]
	if !ok || inst == nil {
		return nil, fmt.Errorf("no lighthouse running for network %s", networkID)
	}
	return inst, nil
}

// StopNetwork stops and removes a lighthouse instance. Called when a network is deleted.
func (nm *NetworkManager) StopNetwork(networkID string) {
	nm.mu.Lock()
	inst, ok := nm.instances[networkID]
	if ok {
		delete(nm.instances, networkID)
	}
	nm.mu.Unlock()

	if ok && inst != nil {
		inst.Close()
	}
}

// Stop shuts down all lighthouse instances.
func (nm *NetworkManager) Stop() {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	for id, inst := range nm.instances {
		if inst != nil {
			inst.Close()
		}
		delete(nm.instances, id)
	}
	log.Printf("[mesh] NetworkManager stopped")
}

// RefreshDNS reloads DNS records for a network from the database.
// Called after enrollment, node deletion, or DNS record changes.
func (nm *NetworkManager) RefreshDNS(networkID string) {
	nm.mu.RLock()
	inst, ok := nm.instances[networkID]
	nm.mu.RUnlock()
	if !ok || inst.dns == nil {
		return
	}

	var records []DNSRecord

	// Auto-records from nodes (hostname → VPN IP).
	if nm.nodes != nil {
		nodes, err := nm.nodes.ListForNetwork(networkID)
		if err == nil {
			for _, n := range nodes {
				if n.NebulaIP == "" || n.Status == "pending" {
					continue
				}
				ip := parseNodeIPForDNS(n.NebulaIP)
				if ip == nil {
					continue
				}
				// Use dns_name if set, otherwise hostname.
				name := ""
				if n.DNSName != nil && *n.DNSName != "" {
					name = *n.DNSName
				} else if n.Hostname != "" {
					name = n.Hostname
				}
				if name != "" {
					records = append(records, DNSRecord{Name: name, IP: ip})
				}
			}
		}
	}

	// Custom DNS records from the dns_records table.
	if nm.dnsRecords != nil {
		customRecords, err := nm.dnsRecords.ListForNetwork(networkID)
		if err == nil {
			for _, r := range customRecords {
				ip := parseNodeIPForDNS(r.NebulaIP)
				if ip != nil {
					records = append(records, DNSRecord{Name: r.Name, IP: ip})
				}
			}
		}
	}

	inst.dns.SetRecords(records)
	log.Printf("[dns] refreshed %d records for .%s", len(records), inst.DNSDomain)
}

func parseNodeIPForDNS(nebulaIP string) net.IP {
	// Strip CIDR mask if present: "10.42.1.3/24" → "10.42.1.3"
	ipStr := nebulaIP
	if idx := strings.Index(nebulaIP, "/"); idx >= 0 {
		ipStr = nebulaIP[:idx]
	}
	return net.ParseIP(ipStr)
}

// AllocatePort returns the next available lighthouse UDP port.
// Uses gap-filling: reuses ports from deleted networks before allocating new ones.
func (nm *NetworkManager) AllocatePort() (int, error) {
	port, err := nm.networks.FirstAvailableLighthousePort()
	if err != nil {
		return 0, fmt.Errorf("query available lighthouse port: %w", err)
	}
	if port > 65535 {
		return 0, fmt.Errorf("no available UDP ports (max 65535)")
	}
	return port, nil
}

// startInstance starts a Nebula userspace lighthouse+relay for a network.
// Acquires nm.mu internally for the instance map insert. Safe to call without holding the lock.
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

	// Start DNS server for this network on a per-network port.
	// We bind on 0.0.0.0 because the Nebula VPN IP doesn't exist as an OS
	// interface in userspace mode. Each network gets a unique DNS port.
	var meshDNS *MeshDNS
	if n.DNSDomain != "" {
		dnsPort := baseDNSPort + (port - baseLighthousePort)
		meshDNS = NewMeshDNS(dnsPort, n.DNSDomain)
		if err := meshDNS.Start(); err != nil {
			log.Printf("[mesh] DNS server failed to start for .%s: %v", n.DNSDomain, err)
			meshDNS = nil // non-fatal — lighthouse works without DNS
		}
	}

	inst := &NetworkInstance{
		NetworkID: n.ID,
		Slug:      n.Slug,
		UDPPort:   port,
		DNSDomain: n.DNSDomain,
		svc:       svc,
		dns:       meshDNS,
	}

	nm.mu.Lock()
	nm.instances[n.ID] = inst
	nm.mu.Unlock()
	log.Printf("[mesh] lighthouse started for network %s (%s) on UDP :%d, DNS domain: .%s",
		n.Slug, n.ID[:8], port, n.DNSDomain)

	return nil
}

func indentPEM(pem string, spaces int) string {
	prefix := ""
	for i := 0; i < spaces; i++ {
		prefix += " "
	}
	lines := ""
	for _, line := range splitLines(pem) {
		lines += prefix + line + "\n"
	}
	return lines
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
