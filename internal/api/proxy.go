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
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/mesh"
)

func (h *ProxyHandler) audit(userID, action string, networkID, nodeID *string, details *string) {
	if h.Audit == nil {
		return
	}
	h.Audit.Log(uuid.New().String(), userID, action, networkID, nodeID, details)
}

// ProxyHandler proxies requests to agents through the Nebula mesh.
type ProxyHandler struct {
	NetworkManager *mesh.NetworkManager
	ForwardManager *mesh.ForwardManager
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
	Audit          *db.AuditStore
	AllowedOrigins []string // allowed WebSocket origins; empty = same-origin only
	EventHub       *EventHub
}

// requireNode validates network ownership and returns the node.
func (h *ProxyHandler) requireNode(r *http.Request) (*db.Network, *db.Node, error) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	nodeID := chi.URLParam(r, "nodeID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
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

	inst, err := h.getNetworkInstance(node.NetworkID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	resp, err := agentRequest(r.Context(), inst, node, "GET", "/health", nil)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Update node status on successful health check.
	h.Nodes.UpdateLastSeen(node.ID)
	if h.EventHub != nil {
		h.EventHub.Publish(node.NetworkID, Event{Type: "node.status", Data: map[string]string{"nodeId": node.ID, "status": "online"}})
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

	inst, err := h.getNetworkInstance(node.NetworkID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	h.audit(user.ID, "shell.connect", &networkID, &node.ID, nil)

	// Upgrade browser connection to WebSocket with origin validation.
	upgrader := websocket.Upgrader{CheckOrigin: h.checkOrigin}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[proxy] WebSocket upgrade failed: %v", err)
		return
	}
	defer browserConn.Close()

	// Connect to agent's /shell WebSocket through the mesh.
	nodeIP := agentNodeIP(node)
	wsDialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return inst.Dial(ctx, nodeIP)
		},
	}

	agentURL := fmt.Sprintf("ws://%s:%d/shell", nodeIP, 41820)
	headers := http.Header{"Authorization": []string{"Bearer " + node.AgentToken}}

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

	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	h.audit(user.ID, "exec", &networkID, &node.ID, nil)

	inst, err := h.getNetworkInstance(node.NetworkID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	resp, err := agentRequest(r.Context(), inst, node, "POST", "/exec", r.Body)
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

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		RemotePort int `json:"remotePort"`
		LocalPort  int `json:"localPort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RemotePort == 0 {
		http.Error(w, "remotePort is required", http.StatusBadRequest)
		return
	}
	if body.RemotePort < 1 || body.RemotePort > 65535 {
		http.Error(w, "remotePort must be 1-65535", http.StatusBadRequest)
		return
	}
	if body.LocalPort < 0 || body.LocalPort > 65535 {
		http.Error(w, "localPort must be 0-65535", http.StatusBadRequest)
		return
	}
	// Prevent binding privileged ports on the control plane host.
	if body.LocalPort > 0 && body.LocalPort < 1024 {
		http.Error(w, "localPort must be 0 (auto) or >= 1024", http.StatusBadRequest)
		return
	}

	networkID := chi.URLParam(r, "networkID")
	user := auth.UserFromContext(r.Context())
	detail := fmt.Sprintf("remote:%d local:%d", body.RemotePort, body.LocalPort)
	h.audit(user.ID, "port_forward.start", &networkID, &node.ID, &detail)

	pf, err := h.ForwardManager.Start(networkID, node.ID, agentNodeIP(node), body.RemotePort, body.LocalPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSONStatus(w, http.StatusCreated, pf)
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
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	fwdID := chi.URLParam(r, "fwdID")

	// Verify the user owns this network before allowing stop.
	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	if err := h.ForwardManager.StopForNetwork(networkID, fwdID); err != nil {
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
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	// Verify the user owns this network.
	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	forwards := h.ForwardManager.List(networkID)
	if forwards == nil {
		forwards = make([]*mesh.PortForward, 0)
	}
	writeJSON(w, forwards)
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
	if err != nil || network == nil || !authz.CanAccessNetwork(user, network) {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	nodes, err := h.Nodes.ListForNetwork(networkID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Map to safe DTO — never expose AgentToken, EnrollmentToken, or keys.
	result := make([]NodeResponse, 0, len(nodes))
	for _, n := range nodes {
		result = append(result, NodeResponse{
			ID:           n.ID,
			NetworkID:    n.NetworkID,
			Hostname:     n.Hostname,
			OS:           n.OS,
			Arch:         n.Arch,
			NebulaIP:     n.NebulaIP,
			AgentRealIP:  n.AgentRealIP,
			NodeType:     n.NodeType,
			ExposedPorts: n.ExposedPorts,
			DNSName:      n.DNSName,
			Status:       n.Status,
			LastSeenAt:   n.LastSeenAt,
			CreatedAt:    n.CreatedAt,
		})
	}

	writeJSON(w, result)
}

// DeleteNode removes a single node, closing its tunnel and any port forwards.
// @Summary      Delete node
// @Description  Permanently deletes a node from the network, closes its mesh tunnel and any active port forwards.
// @Tags         nodes
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Param        nodeID path string true "Node ID"
// @Success      204
// @Failure      404 {object} ErrorResponse
// @Router       /api/networks/{networkID}/nodes/{nodeID} [delete]
// RenameNode updates a node's display name and DNS name.
// PATCH /api/networks/{networkID}/nodes/{nodeID}
func (h *ProxyHandler) RenameNode(w http.ResponseWriter, r *http.Request) {
	network, node, err := h.requireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	dnsName := sanitizeDNSName(body.Name)
	if err := h.Nodes.Rename(node.ID, body.Name, dnsName); err != nil {
		http.Error(w, "failed to rename node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if h.NetworkManager != nil {
		h.NetworkManager.RefreshDNS(network.ID)
	}
	if h.EventHub != nil {
		h.EventHub.Publish(network.ID, Event{Type: "node.renamed", Data: map[string]string{"nodeId": node.ID, "name": body.Name}})
	}

	writeJSON(w, map[string]string{"name": body.Name, "dnsName": dnsName})
}

func (h *ProxyHandler) DeleteNode(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Node will be disconnected from the mesh when its cert expires (24h)
	// or immediately when it tries to re-register with the lighthouse.

	// Delete from database.
	if err := h.Nodes.Delete(node.ID); err != nil {
		http.Error(w, "failed to delete node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	h.audit(user.ID, "node.delete", &networkID, &node.ID, nil)
	if h.EventHub != nil {
		h.EventHub.Publish(networkID, Event{Type: "node.deleted", Data: map[string]string{"nodeId": node.ID}})
	}

	w.WriteHeader(http.StatusNoContent)
}

// checkOrigin validates the WebSocket Origin header against allowed origins.
// If no AllowedOrigins are configured, falls back to same-origin check.
func (h *ProxyHandler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients
	}
	// Check explicitly allowed origins.
	for _, allowed := range h.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	// Default: same-origin check (Origin must match Host).
	host := r.Host
	return origin == "http://"+host || origin == "https://"+host
}

// getNetworkInstance returns the running Nebula lighthouse for a node's network.
func (h *ProxyHandler) getNetworkInstance(networkID string) (*mesh.NetworkInstance, error) {
	return h.NetworkManager.GetInstance(networkID)
}

// agentNodeIP extracts the IP (without CIDR mask) from the node's NebulaIP.
func agentNodeIP(node *db.Node) string {
	ip, _, err := net.ParseCIDR(node.NebulaIP)
	if err != nil {
		ip = net.ParseIP(node.NebulaIP)
	}
	if ip == nil {
		return node.NebulaIP
	}
	return ip.String()
}

// agentRequest makes an HTTP request to the agent through the mesh.
func agentRequest(ctx context.Context, inst *mesh.NetworkInstance, node *db.Node, method, path string, body io.Reader) (*http.Response, error) {
	nodeIP := agentNodeIP(node)
	client := inst.HTTPClient(nodeIP)
	url := fmt.Sprintf("http://%s:%d%s", nodeIP, 41820, path)

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+node.AgentToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return client.Do(req)
}
