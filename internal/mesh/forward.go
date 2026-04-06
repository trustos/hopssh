package mesh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// closeWriter is implemented by net.TCPConn and similar types for half-close.
type closeWriter interface {
	CloseWrite() error
}

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

	nebulaIP string // VPN IP of the target node

	listener   net.Listener
	netManager *NetworkManager
	cancel     context.CancelFunc
	connWg     sync.WaitGroup
	active     atomic.Int32
}

// ActiveConns returns the number of active proxied connections.
func (pf *PortForward) ActiveConns() int {
	return int(pf.active.Load())
}

// ForwardManager tracks active port forwards.
type ForwardManager struct {
	netManager *NetworkManager

	mu       sync.Mutex
	forwards map[string]*PortForward
}

func NewForwardManager(netManager *NetworkManager) *ForwardManager {
	return &ForwardManager{
		netManager: netManager,
		forwards:   make(map[string]*PortForward),
	}
}

// Start creates a local TCP listener that proxies to a remote port through the mesh.
func (fm *ForwardManager) Start(networkID, nodeID, nebulaIP string, remotePort, localPort int) (*PortForward, error) {
	if fm.netManager == nil {
		return nil, fmt.Errorf("network manager not available")
	}
	if _, err := fm.netManager.GetInstance(networkID); err != nil {
		return nil, fmt.Errorf("mesh instance: %w", err)
	}

	listenAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	actualPort := ln.Addr().(*net.TCPAddr).Port

	id := generateForwardID()

	ctx, cancel := context.WithCancel(context.Background())

	pf := &PortForward{
		ID:         id,
		NetworkID:  networkID,
		NodeID:     nodeID,
		RemotePort: remotePort,
		LocalPort:  actualPort,
		LocalAddr:  ln.Addr().String(),
		CreatedAt:  time.Now().Unix(),
		nebulaIP:   nebulaIP,
		listener:   ln,
		netManager: fm.netManager,
		cancel:     cancel,
	}

	fm.mu.Lock()
	fm.forwards[id] = pf
	fm.mu.Unlock()

	go pf.acceptLoop(ctx)

	log.Printf("[forward] %s: localhost:%d → node %s port %d", id, actualPort, nodeID, remotePort)
	return pf, nil
}

// StopForNetwork closes a port forward by ID, but only if it belongs to the given network.
func (fm *ForwardManager) StopForNetwork(networkID, id string) error {
	fm.mu.Lock()
	pf, ok := fm.forwards[id]
	if !ok || pf.NetworkID != networkID {
		fm.mu.Unlock()
		return fmt.Errorf("port forward %q not found", id)
	}
	delete(fm.forwards, id)
	fm.mu.Unlock()

	return fm.drain(pf)
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

	return fm.drain(pf)
}

func (fm *ForwardManager) drain(pf *PortForward) error {
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
		log.Printf("[forward] %s: force-closed after drain timeout (%d active)", pf.ID, pf.ActiveConns())
	}

	log.Printf("[forward] %s stopped", pf.ID)
	return nil
}

// StopForNetwork stops all port forwards for a network (used during network deletion).
func (fm *ForwardManager) StopAllForNetwork(networkID string) {
	fm.mu.Lock()
	var toStop []*PortForward
	for id, pf := range fm.forwards {
		if pf.NetworkID == networkID {
			toStop = append(toStop, pf)
			delete(fm.forwards, id)
		}
	}
	fm.mu.Unlock()
	for _, pf := range toStop {
		fm.drain(pf)
	}
}

// StopAll stops all active port forwards (used during graceful shutdown).
func (fm *ForwardManager) StopAll() {
	fm.mu.Lock()
	var toStop []*PortForward
	for id, pf := range fm.forwards {
		toStop = append(toStop, pf)
		delete(fm.forwards, id)
	}
	fm.mu.Unlock()
	for _, pf := range toStop {
		fm.drain(pf)
	}
}

func generateForwardID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return "fwd-" + hex.EncodeToString(b)
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

	inst, err := pf.netManager.GetInstance(pf.NetworkID)
	if err != nil {
		log.Printf("[forward] %s: get mesh instance failed: %v", pf.ID, err)
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	remote, err := inst.DialPort(dialCtx, pf.nebulaIP, pf.RemotePort)
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
		// Signal half-close to remote so the reverse copy gets EOF.
		if tc, ok := remote.(closeWriter); ok {
			tc.CloseWrite()
		}
		close(done)
	}()

	if _, err := io.Copy(local, remote); err != nil {
		log.Printf("[forward] %s: remote→local error: %v", pf.ID, err)
	}
	// Signal half-close to local so the forward copy gets EOF.
	if tc, ok := local.(closeWriter); ok {
		tc.CloseWrite()
	}
	<-done
}
