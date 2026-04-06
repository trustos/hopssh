package mesh

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
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
	idleTimeout   = 5 * time.Minute
	nebulaUDPPort = 41820
	agentTCPPort  = 41820
)

// Tunnel wraps a userspace Nebula mesh connection to a single node.
type Tunnel struct {
	svc       *service.Service
	networkID string
	nodeID    string
	agentAddr string // e.g. "10.42.1.2:41820"
	token     string
	lastUsed  time.Time
	mu        sync.Mutex
}

// Manager creates and caches on-demand Nebula tunnels per node.
type Manager struct {
	networks *db.NetworkStore
	nodes    *db.NodeStore

	mu         sync.Mutex
	tunnels    map[string]*Tunnel // keyed by nodeID
	connecting sync.Map           // nodeID → chan (serializes connection attempts)
	done         chan struct{}
}

func NewManager(networks *db.NetworkStore, nodes *db.NodeStore) *Manager {
	m := &Manager{
		networks: networks,
		nodes:    nodes,
		tunnels:  make(map[string]*Tunnel),
		done:     make(chan struct{}),
	}
	go m.reaper()
	return m
}

// GetTunnelForNode returns a cached or freshly created Nebula tunnel to a node.
// If the Nebula handshake fails (e.g., the agent's old session hasn't expired
// after a server restart), the tunnel is destroyed and retried up to 3 times.
func (m *Manager) GetTunnelForNode(nodeID string) (*Tunnel, error) {
	m.mu.Lock()
	if t, ok := m.tunnels[nodeID]; ok {
		t.mu.Lock()
		t.lastUsed = time.Now()
		t.mu.Unlock()
		m.mu.Unlock()
		return t, nil
	}
	m.mu.Unlock()

	// Serialize connection attempts per node.
	lockCh, _ := m.connecting.LoadOrStore(nodeID, make(chan struct{}, 1))
	ch := lockCh.(chan struct{})
	ch <- struct{}{}
	defer func() {
		<-ch
		// Clean up the entry if the channel is empty (no waiters).
		// This prevents the sync.Map from growing unbounded.
		select {
		case ch <- struct{}{}:
			// Channel was empty — safe to remove.
			<-ch
			m.connecting.Delete(nodeID)
		default:
			// Another goroutine is waiting — leave it.
		}
	}()

	// Re-check cache after acquiring lock.
	m.mu.Lock()
	if t, ok := m.tunnels[nodeID]; ok {
		t.mu.Lock()
		t.lastUsed = time.Now()
		t.mu.Unlock()
		m.mu.Unlock()
		return t, nil
	}
	m.mu.Unlock()

	node, err := m.nodes.Get(nodeID)
	if err != nil || node == nil {
		return nil, fmt.Errorf("load node %q: %w", nodeID, err)
	}
	if node.AgentRealIP == nil || *node.AgentRealIP == "" {
		return nil, fmt.Errorf("no agent real IP for node %q — agent not connected yet", nodeID)
	}

	network, err := m.networks.Get(node.NetworkID)
	if err != nil || network == nil {
		return nil, fmt.Errorf("load network for node %q: %w", nodeID, err)
	}

	// Retry with delays to handle agent session expiration after server restart.
	retryDelays := []time.Duration{0, 15 * time.Second, 30 * time.Second}
	var lastErr error
	for attempt, delay := range retryDelays {
		if delay > 0 {
			log.Printf("[mesh] handshake probe failed for node %s (attempt %d), retrying in %s...", nodeID, attempt, delay)
			time.Sleep(delay)
		}

		t, err := m.connectNode(network, node)
		if err != nil {
			lastErr = err
			continue
		}

		// Probe: TCP dial to verify handshake completes.
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 12*time.Second)
		probeConn, probeErr := t.Dial(probeCtx)
		probeCancel()

		if probeErr == nil {
			probeConn.Close()
			log.Printf("[mesh] tunnel to node %s established (attempt %d)", nodeID, attempt+1)

			m.mu.Lock()
			if existing, ok := m.tunnels[nodeID]; ok {
				m.mu.Unlock()
				t.close()
				return existing, nil
			}
			m.tunnels[nodeID] = t
			m.mu.Unlock()
			return t, nil
		}

		log.Printf("[mesh] handshake probe timeout for node %s (attempt %d): %v", nodeID, attempt+1, probeErr)
		t.close()
		lastErr = probeErr
	}

	return nil, fmt.Errorf("tunnel to node %s failed after %d attempts: %w", nodeID, len(retryDelays), lastErr)
}

func (m *Manager) connectNode(network *db.Network, node *db.Node) (*Tunnel, error) {
	// Parse node's Nebula IP (CIDR form, e.g. "10.42.1.3/24").
	agentIP, _, err := net.ParseCIDR(node.NebulaIP)
	if err != nil {
		agentIP = net.ParseIP(node.NebulaIP)
		if agentIP == nil {
			return nil, fmt.Errorf("parse node nebula IP %q: %w", node.NebulaIP, err)
		}
	}

	realIPRaw := *node.AgentRealIP
	udpHost, portStr, splitErr := net.SplitHostPort(realIPRaw)
	udpPort := nebulaUDPPort
	if splitErr == nil {
		if p, _ := strconv.Atoi(portStr); p > 0 {
			udpPort = p
		}
	} else {
		udpHost = realIPRaw
	}
	staticMap := fmt.Sprintf("'%s': ['%s:%d']", agentIP.String(), udpHost, udpPort)

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
static_host_map:
  %s
listen:
  host: 0.0.0.0
  port: 0
lighthouse:
  am_lighthouse: false
  hosts: []
punchy:
  punch: true
  respond: true
firewall:
  outbound:
    - port: any
      proto: any
      host: any
  inbound:
    - port: 8080
      proto: tcp
      host: any
    - port: any
      proto: icmp
      host: any
`,
		indentPEM(string(network.NebulaCACert), 4),
		indentPEM(string(network.ServerCert), 4),
		indentPEM(string(network.ServerKey), 4),
		staticMap,
	)

	var cfg config.C
	if err := cfg.LoadString(cfgStr); err != nil {
		return nil, fmt.Errorf("load nebula config for node: %w", err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	label := fmt.Sprintf("hopssh-%s-%s", network.Slug, node.ID[:8])
	ctrl, err := nebula.Main(&cfg, false, label, logger, overlay.NewUserDeviceFromConfig)
	if err != nil {
		return nil, fmt.Errorf("start nebula for node: %w", err)
	}

	svc, err := service.New(ctrl)
	if err != nil {
		log.Printf("[mesh] service.New failed for node %s: %v (nebula goroutines may leak)", node.ID, err)
		return nil, fmt.Errorf("create nebula service for node: %w", err)
	}

	return &Tunnel{
		svc:       svc,
		networkID: network.ID,
		nodeID:    node.ID,
		agentAddr: fmt.Sprintf("%s:%d", agentIP.String(), agentTCPPort),
		token:     node.AgentToken,
		lastUsed:  time.Now(),
	}, nil
}

// Dial opens a TCP connection to the agent through the Nebula mesh.
func (t *Tunnel) Dial(ctx context.Context) (net.Conn, error) {
	t.mu.Lock()
	t.lastUsed = time.Now()
	t.mu.Unlock()
	return t.svc.DialContext(ctx, "tcp", t.agentAddr)
}

// DialPort opens a TCP connection to an arbitrary port on the agent's Nebula IP.
func (t *Tunnel) DialPort(ctx context.Context, port int) (net.Conn, error) {
	t.mu.Lock()
	t.lastUsed = time.Now()
	t.mu.Unlock()
	addr := fmt.Sprintf("%s:%d", t.AgentNebulaIP(), port)
	return t.svc.DialContext(ctx, "tcp", addr)
}

// HTTPClient returns an http.Client that routes through the Nebula tunnel.
func (t *Tunnel) HTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return t.Dial(ctx)
			},
		},
	}
}

// AgentURL returns the base HTTP URL for the agent on the mesh.
func (t *Tunnel) AgentURL() string {
	return "http://" + t.agentAddr
}

// AgentNebulaIP returns the Nebula VPN IP of the agent (without the port).
func (t *Tunnel) AgentNebulaIP() string {
	host, _, _ := net.SplitHostPort(t.agentAddr)
	return host
}

// Token returns the per-node auth token.
func (t *Tunnel) Token() string {
	return t.token
}

// close shuts down the tunnel's Nebula service.
//
// VENDOR PATCH: This works because of the 1-line patch applied to
// vendor/github.com/slackhq/nebula/interface.go (see patches/).
// Without the patch, svc.Close() triggers os.Exit(2).
//
// When upstream merges PR #1375 and releases a new version:
//   1. Run: scripts/check-nebula-patch.sh
//   2. Update to the new upstream version
//   3. Remove the vendor patch
//
// Upstream tracking:
//   - Issue: https://github.com/slackhq/nebula/issues/1031
//   - PR:    https://github.com/slackhq/nebula/pull/1375
func (t *Tunnel) close() {
	log.Printf("[mesh] closing tunnel for node %s", t.nodeID)
	t.svc.Close()
}

// CloseTunnel removes a node's tunnel from the cache.
func (m *Manager) CloseTunnel(nodeID string) {
	m.mu.Lock()
	t, ok := m.tunnels[nodeID]
	if ok {
		delete(m.tunnels, nodeID)
	}
	m.mu.Unlock()
	if ok {
		t.close()
	}
}

// Stop shuts down all tunnels and the reaper goroutine.
func (m *Manager) Stop() {
	close(m.done)
	m.mu.Lock()
	for id, t := range m.tunnels {
		t.close()
		delete(m.tunnels, id)
	}
	m.mu.Unlock()
}

func (m *Manager) reaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.mu.Lock()
			for id, t := range m.tunnels {
				t.mu.Lock()
				idle := time.Since(t.lastUsed) > idleTimeout
				t.mu.Unlock()
				if idle {
					log.Printf("[mesh] closing idle tunnel for node %s", id)
					t.close()
					delete(m.tunnels, id)
				}
			}
			m.mu.Unlock()
		}
	}
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
