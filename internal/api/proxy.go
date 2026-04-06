package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/mesh"
)

// ProxyHandler proxies requests to agents through the Nebula mesh.
type ProxyHandler struct {
	MeshManager    *mesh.Manager
	ForwardManager *mesh.ForwardManager
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
}

// requireNode validates network ownership and returns the node.
func (h *ProxyHandler) requireNode(r *http.Request) (*db.Network, *db.Node, error) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	nodeID := chi.URLParam(r, "nodeID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || network.UserID != user.ID {
		return nil, nil, fmt.Errorf("network not found")
	}

	node, err := h.Nodes.Get(nodeID)
	if err != nil || node == nil || node.NetworkID != networkID {
		return nil, nil, fmt.Errorf("node not found")
	}

	return network, node, nil
}

// NodeHealth proxies a health check to the agent through the mesh tunnel.
// @Summary      Node health check
// @Description  Proxies a health check to the agent on the node via Nebula mesh. Returns hostname, OS, architecture, and uptime. Updates the node's last-seen timestamp.
// @Tags         nodes
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Param        nodeID path string true "Node ID"
// @Success      200 {object} HealthResponse
// @Failure      404 {object} ErrorResponse "Node not found"
// @Failure      502 {object} ErrorResponse "Agent unreachable"
// @Router       /api/networks/{networkID}/nodes/{nodeID}/health [get]
func (h *ProxyHandler) NodeHealth(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	tunnel, err := h.MeshManager.GetTunnelForNode(node.ID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	resp, err := agentRequest(r.Context(), tunnel, "GET", "/health", nil)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Update node status on successful health check.
	h.Nodes.UpdateLastSeen(node.ID)
	if node.AgentRealIP == nil {
		// First health check — record the real IP.
		if node.AgentRealIP == nil || *node.AgentRealIP == "" {
			// Real IP was set during enrollment or discovery; just update status.
		}
	}

	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

// NodeShell proxies a WebSocket terminal session to the agent through the mesh.
// @Summary      Web terminal
// @Description  Upgrades to WebSocket and relays a PTY shell session to the agent. Supports terminal resize via binary control frames. Uses xterm-256color.
// @Tags         nodes
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Param        nodeID path string true "Node ID"
// @Success      101 "WebSocket upgrade"
// @Failure      404 {object} ErrorResponse "Node not found"
// @Failure      502 {object} ErrorResponse "Agent unreachable"
// @Router       /api/networks/{networkID}/nodes/{nodeID}/shell [get]
func (h *ProxyHandler) NodeShell(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	tunnel, err := h.MeshManager.GetTunnelForNode(node.ID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Upgrade browser connection to WebSocket.
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[proxy] WebSocket upgrade failed: %v", err)
		return
	}
	defer browserConn.Close()

	// Connect to agent's /shell WebSocket through the mesh tunnel.
	wsDialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return tunnel.Dial(ctx)
		},
	}

	agentURL := fmt.Sprintf("ws://%s/shell", tunnel.AgentNebulaIP()+":41820")
	headers := http.Header{"Authorization": []string{"Bearer " + tunnel.Token()}}

	agentConn, _, err := wsDialer.DialContext(r.Context(), agentURL, headers)
	if err != nil {
		log.Printf("[proxy] agent WebSocket dial failed: %v", err)
		browserConn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer agentConn.Close()

	// Bidirectional relay.
	done := make(chan struct{})

	// Agent → Browser
	go func() {
		defer close(done)
		for {
			msgType, msg, err := agentConn.ReadMessage()
			if err != nil {
				return
			}
			if err := browserConn.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	// Browser → Agent
	go func() {
		for {
			msgType, msg, err := browserConn.ReadMessage()
			if err != nil {
				agentConn.Close()
				return
			}
			if err := agentConn.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	<-done
}

// NodeExec proxies a command execution request with streaming output.
// @Summary      Execute command
// @Description  Executes a command on the node via the agent. Output is streamed as text/plain. The response ends with a ---EXIT:N--- marker containing the exit code.
// @Tags         nodes
// @Security     BearerAuth
// @Accept       json
// @Produce      plain
// @Param        networkID path string true "Network ID"
// @Param        nodeID path string true "Node ID"
// @Param        body body ExecRequest true "Command to execute"
// @Success      200 {string} string "Streaming command output"
// @Failure      404 {object} ErrorResponse "Node not found"
// @Failure      502 {object} ErrorResponse "Agent unreachable"
// @Router       /api/networks/{networkID}/nodes/{nodeID}/exec [post]
func (h *ProxyHandler) NodeExec(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	tunnel, err := h.MeshManager.GetTunnelForNode(node.ID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	resp, err := agentRequest(r.Context(), tunnel, "POST", "/exec", r.Body)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
}

// StartPortForward creates a local TCP port forward through the mesh tunnel.
// @Summary      Start port forward
// @Description  Creates a local TCP listener that proxies connections through the Nebula mesh to a remote port on the node. Similar to kubectl port-forward.
// @Tags         port-forwards
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Param        nodeID path string true "Node ID"
// @Param        body body StartPortForwardRequest true "Port forward config"
// @Success      201 {object} PortForwardResponse
// @Failure      400 {object} ErrorResponse "remotePort required"
// @Failure      404 {object} ErrorResponse "Node not found"
// @Router       /api/networks/{networkID}/nodes/{nodeID}/port-forwards [post]
func (h *ProxyHandler) StartPortForward(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	var body struct {
		RemotePort int `json:"remotePort"`
		LocalPort  int `json:"localPort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RemotePort == 0 {
		http.Error(w, "remotePort is required", http.StatusBadRequest)
		return
	}

	networkID := chi.URLParam(r, "networkID")
	pf, err := h.ForwardManager.Start(networkID, node.ID, body.RemotePort, body.LocalPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(pf)
}

// StopPortForward closes a port forward. Active connections are drained with a 3-second timeout.
// @Summary      Stop port forward
// @Description  Closes an active port forward. Active connections are given 3 seconds to drain before force-close.
// @Tags         port-forwards
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Param        fwdID path string true "Port forward ID"
// @Success      204
// @Failure      404 {object} ErrorResponse "Port forward not found"
// @Router       /api/networks/{networkID}/port-forwards/{fwdID} [delete]
func (h *ProxyHandler) StopPortForward(w http.ResponseWriter, r *http.Request) {
	fwdID := chi.URLParam(r, "fwdID")
	if err := h.ForwardManager.Stop(fwdID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListPortForwards returns all active port forwards for a network.
// @Summary      List port forwards
// @Description  Returns all active TCP port forwards for a network.
// @Tags         port-forwards
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {array} PortForwardResponse
// @Router       /api/networks/{networkID}/port-forwards [get]
func (h *ProxyHandler) ListPortForwards(w http.ResponseWriter, r *http.Request) {
	networkID := chi.URLParam(r, "networkID")
	forwards := h.ForwardManager.List(networkID)
	if forwards == nil {
		forwards = make([]*mesh.PortForward, 0)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(forwards)
}

// ListNodes returns all nodes for a network.
// @Summary      List nodes
// @Description  Returns all nodes in a network with their status, hostname, OS, and last-seen time.
// @Tags         nodes
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {array} NodeResponse
// @Failure      404 {object} ErrorResponse "Network not found"
// @Router       /api/networks/{networkID}/nodes [get]
func (h *ProxyHandler) ListNodes(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || network.UserID != user.ID {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	nodes, err := h.Nodes.ListForNetwork(networkID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if nodes == nil {
		nodes = make([]*db.Node, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

// agentRequest makes an HTTP request to the agent through the mesh tunnel.
func agentRequest(ctx context.Context, tunnel *mesh.Tunnel, method, path string, body io.Reader) (*http.Response, error) {
	client := tunnel.HTTPClient()
	url := tunnel.AgentURL() + path

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tunnel.Token())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return client.Do(req)
}
