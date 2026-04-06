package mesh

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// PortForward represents an active local TCP listener that proxies connections
// through a Nebula tunnel to a remote port on a specific node.
type PortForward struct {
	ID         string `json:"id"`
	NetworkID  string `json:"networkId"`
	NodeID     string `json:"nodeId"`
	RemotePort int    `json:"remotePort"`
	LocalPort  int    `json:"localPort"`
	LocalAddr  string `json:"localAddr"`
	CreatedAt  int64  `json:"createdAt"`

	listener net.Listener
	manager  *Manager
	cancel   context.CancelFunc
	connWg   sync.WaitGroup
	active   atomic.Int32
}

// ActiveConns returns the number of active proxied connections.
func (pf *PortForward) ActiveConns() int {
	return int(pf.active.Load())
}

// ForwardManager tracks active port forwards.
type ForwardManager struct {
	meshManager *Manager

	mu       sync.Mutex
	forwards map[string]*PortForward
	nextID   int
}

func NewForwardManager(meshManager *Manager) *ForwardManager {
	return &ForwardManager{
		meshManager: meshManager,
		forwards:    make(map[string]*PortForward),
	}
}

// Start creates a local TCP listener that proxies to a remote port through the mesh.
func (fm *ForwardManager) Start(networkID, nodeID string, remotePort, localPort int) (*PortForward, error) {
	if fm.meshManager == nil {
		return nil, fmt.Errorf("mesh manager not available")
	}
	if _, err := fm.meshManager.GetTunnelForNode(nodeID); err != nil {
		return nil, fmt.Errorf("mesh tunnel: %w", err)
	}

	listenAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	actualPort := ln.Addr().(*net.TCPAddr).Port

	fm.mu.Lock()
	fm.nextID++
	id := fmt.Sprintf("fwd-%d", fm.nextID)
	fm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	pf := &PortForward{
		ID:         id,
		NetworkID:  networkID,
		NodeID:     nodeID,
		RemotePort: remotePort,
		LocalPort:  actualPort,
		LocalAddr:  ln.Addr().String(),
		CreatedAt:  time.Now().Unix(),
		listener:   ln,
		manager:    fm.meshManager,
		cancel:     cancel,
	}

	fm.mu.Lock()
	fm.forwards[id] = pf
	fm.mu.Unlock()

	go pf.acceptLoop(ctx)

	log.Printf("[forward] %s: localhost:%d → node %s port %d", id, actualPort, nodeID, remotePort)
	return pf, nil
}

// Stop closes a port forward by ID with a 3-second drain timeout.
func (fm *ForwardManager) Stop(id string) error {
	fm.mu.Lock()
	pf, ok := fm.forwards[id]
	if !ok {
		fm.mu.Unlock()
		return fmt.Errorf("port forward %q not found", id)
	}
	delete(fm.forwards, id)
	fm.mu.Unlock()

	pf.cancel()
	pf.listener.Close()

	done := make(chan struct{})
	go func() {
		pf.connWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		log.Printf("[forward] %s: force-closed after drain timeout (%d active)", id, pf.ActiveConns())
	}

	log.Printf("[forward] %s stopped", id)
	return nil
}

// List returns all active port forwards, optionally filtered by network.
func (fm *ForwardManager) List(networkID string) []*PortForward {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	var result []*PortForward
	for _, pf := range fm.forwards {
		if networkID == "" || pf.NetworkID == networkID {
			result = append(result, pf)
		}
	}
	return result
}

func (pf *PortForward) acceptLoop(ctx context.Context) {
	for {
		conn, err := pf.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[forward] %s: accept error: %v", pf.ID, err)
				return
			}
		}

		pf.connWg.Add(1)
		pf.active.Add(1)
		go func() {
			defer pf.connWg.Done()
			defer pf.active.Add(-1)
			pf.handleConn(ctx, conn)
		}()
	}
}

func (pf *PortForward) handleConn(ctx context.Context, local net.Conn) {
	defer local.Close()

	tunnel, err := pf.manager.GetTunnelForNode(pf.NodeID)
	if err != nil {
		log.Printf("[forward] %s: get tunnel failed: %v", pf.ID, err)
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	remote, err := tunnel.DialPort(dialCtx, pf.RemotePort)
	if err != nil {
		log.Printf("[forward] %s: dial remote port %d failed: %v", pf.ID, pf.RemotePort, err)
		return
	}
	defer remote.Close()

	done := make(chan struct{})
	go func() {
		if _, err := io.Copy(remote, local); err != nil {
			log.Printf("[forward] %s: local→remote error: %v", pf.ID, err)
		}
		close(done)
	}()

	if _, err := io.Copy(local, remote); err != nil {
		log.Printf("[forward] %s: remote→local error: %v", pf.ID, err)
	}
	<-done
}
