package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

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

// proxyAuthTTL controls how long cached requireNode results are valid.
// Network/node/membership data changes rarely (admin actions only), so
// a 2-minute TTL keeps the proxy hot path off SQLite while ensuring
// capability changes and access revocations take effect promptly.
const proxyAuthTTL = 2 * time.Minute

type proxyAuthEntry struct {
	network *db.Network
	node    *db.Node
	expires time.Time
}

// ProxyHandler proxies requests to agents through the Nebula mesh.
type ProxyHandler struct {
	NetworkManager *mesh.NetworkManager
	ForwardManager *mesh.ForwardManager
	Networks       *db.NetworkStore
	Nodes          *db.NodeStore
	Members        *db.NetworkMemberStore
	Audit          *db.AuditStore
	AllowedOrigins []string // allowed WebSocket origins; empty = same-origin only
	EventHub       *EventHub
	Events         *db.NetworkEventStore

	// authCache caches requireNode results for the proxy hot path.
	// Key: "networkID:nodeID:userID", Value: *proxyAuthEntry.
	// Prevents SQLite contention from Nomad's long-polling requests.
	authCache sync.Map
}

// requireNode validates network access (owner or member) and returns the node.
func (h *ProxyHandler) requireNode(r *http.Request) (*db.Network, *db.Node, error) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	nodeID := chi.URLParam(r, "nodeID")

	network, err := h.Networks.Get(networkID)
	if err != nil {
		log.Printf("[proxy] requireNode: Networks.Get(%s) error: %v", networkID, err)
		return nil, nil, fmt.Errorf("network not found")
	}
	if network == nil {
		return nil, nil, fmt.Errorf("network not found")
	}

	membership, _ := h.Members.GetMembership(networkID, user.ID)
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		log.Printf("[proxy] requireNode: access denied for user %s on network %s", user.ID, networkID)
		return nil, nil, fmt.Errorf("network not found")
	}

	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		log.Printf("[proxy] requireNode: Nodes.Get(%s) error: %v", nodeID, err)
		return nil, nil, fmt.Errorf("node not found")
	}
	if node == nil || node.NetworkID != networkID {
		return nil, nil, fmt.Errorf("node not found")
	}

	return network, node, nil
}

// requireAdmin validates network access and ensures the user is an admin.
func (h *ProxyHandler) requireAdmin(r *http.Request) (*db.Network, *db.Node, error) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	nodeID := chi.URLParam(r, "nodeID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		return nil, nil, fmt.Errorf("network not found")
	}

	membership, _ := h.Members.GetMembership(networkID, user.ID)
	access := authz.CheckAccess(user, network, membership)
	if !access.CanAdmin() {
		return nil, nil, fmt.Errorf("admin access required")
	}

	node, err := h.Nodes.Get(nodeID)
	if err != nil || node == nil || node.NetworkID != networkID {
		return nil, nil, fmt.Errorf("node not found")
	}

	return network, node, nil
}

// cachedRequireNode is like requireNode but caches results for the proxy hot path.
// This prevents SQLite contention from Nomad's frequent long-polling requests.
func (h *ProxyHandler) cachedRequireNode(r *http.Request) (*db.Network, *db.Node, error) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	nodeID := chi.URLParam(r, "nodeID")

	key := networkID + ":" + nodeID + ":" + user.ID

	if v, ok := h.authCache.Load(key); ok {
		entry := v.(*proxyAuthEntry)
		if time.Now().Before(entry.expires) {
			return entry.network, entry.node, nil
		}
		h.authCache.Delete(key)
	}

	// Cache miss — hit DB via requireNode.
	network, node, err := h.requireNode(r)
	if err != nil {
		return nil, nil, err
	}

	h.authCache.Store(key, &proxyAuthEntry{
		network: network,
		node:    node,
		expires: time.Now().Add(proxyAuthTTL),
	})

	return network, node, nil
}

// InvalidateProxyCache removes cached auth entries for a network/node.
// If nodeID is empty, all entries for the network are invalidated.
func (h *ProxyHandler) InvalidateProxyCache(networkID, nodeID string) {
	prefix := networkID + ":"
	if nodeID != "" {
		prefix = networkID + ":" + nodeID + ":"
	}
	h.authCache.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			h.authCache.Delete(key)
		}
		return true
	})
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
	if !node.HasCapability("health") {
		http.Error(w, "health checks not enabled for this node. Enable it in the dashboard.", http.StatusForbidden)
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

	// Update node status on successful health check. Uses the proxy-
	// activity path so it's throttled together with shell / exec /
	// forward interactions, and so the sync.Map doesn't churn if
	// multiple clients probe simultaneously.
	h.Nodes.RecordProxyActivity(node.ID)
	if h.EventHub != nil {
		h.EventHub.Publish(node.NetworkID, Event{Type: "node.status", Data: map[string]string{"nodeId": node.ID, "status": "online"}})
	}
	if h.Events != nil && h.Nodes.StatusTransition(node.ID, "online") {
		targetID := node.ID
		status := "online"
		h.Events.Record(node.NetworkID, "node.status", &targetID, &status, nil)
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
	if !node.HasCapability("terminal") {
		http.Error(w, "terminal not enabled for this node. Enable it in the dashboard.", http.StatusForbidden)
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

	// Successful upgrade to the agent = proof the node is alive.
	// Refresh its last_seen_at so the dashboard stays accurate even if
	// the node's outbound heartbeat is broken. Throttled per-node.
	h.Nodes.RecordProxyActivity(node.ID)

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
	if !node.HasCapability("terminal") {
		http.Error(w, "exec not enabled for this node. Enable terminal in the dashboard.", http.StatusForbidden)
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

	// Agent responded = node is alive. Throttled per-node.
	h.Nodes.RecordProxyActivity(node.ID)

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
	if !node.HasCapability("forward") {
		http.Error(w, "port forwarding not enabled for this node. Enable it in the dashboard.", http.StatusForbidden)
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

	// Verify the user can access this network before allowing stop.
	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}
	membership, _ := h.Members.GetMembership(networkID, user.ID)
	if !authz.CheckAccess(user, network, membership).CanView() {
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

	// Verify the user can access this network.
	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}
	membership, _ := h.Members.GetMembership(networkID, user.ID)
	if !authz.CheckAccess(user, network, membership).CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	forwards := h.ForwardManager.List(networkID)
	if forwards == nil {
		forwards = make([]*mesh.PortForward, 0)
	}
	writeJSON(w, forwards)
}

// NodeProxy reverse-proxies HTTP (and WebSocket) requests to a service on a node.
// The request is proxied through the mesh to the agent's /proxy/{port} endpoint,
// which then forwards to localhost:{port} on the node.
// This allows browser access to node-local services without opening host ports.
func (h *ProxyHandler) NodeProxy(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.cachedRequireNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !node.HasCapability("forward") {
		http.Error(w, "port forwarding not enabled for this node. Enable it in the dashboard.", http.StatusForbidden)
		return
	}

	portStr := chi.URLParam(r, "port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	inst, err := h.getNetworkInstance(node.NetworkID)
	if err != nil {
		http.Error(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}

	nodeIP := agentNodeIP(node)
	networkID := chi.URLParam(r, "networkID")

	// Compute the path to forward: strip the /api/networks/.../proxy/{port} prefix.
	// chi's wildcard "*" captures the remainder.
	forwardPath := chi.URLParam(r, "*")
	if forwardPath == "" || forwardPath[0] != '/' {
		forwardPath = "/" + forwardPath
	}

	user := auth.UserFromContext(r.Context())
	detail := fmt.Sprintf("port=%d path=%s", port, forwardPath)
	h.audit(user.ID, "port_forward.proxy", &networkID, &node.ID, &detail)

	// WebSocket: upgrade both sides, relay through mesh (same pattern as NodeShell).
	if isWSUpgrade(r) {
		h.proxyNodeWebSocket(w, r, node, inst, nodeIP, port, forwardPath)
		return
	}

	// HTTP: reverse proxy through mesh to agent's /proxy/{port}{path} endpoint.
	agentPath := fmt.Sprintf("/proxy/%d%s", port, forwardPath)
	nodeID := chi.URLParam(r, "nodeID")
	proxyPrefix := fmt.Sprintf("/api/networks/%s/nodes/%s/proxy/%d", networkID, nodeID, port)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = fmt.Sprintf("%s:%d", nodeIP, 41820)
			req.URL.Path = agentPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = req.URL.Host
			req.Header.Set("Authorization", "Bearer "+node.AgentToken)
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return inst.Dial(ctx, nodeIP)
			},
		},
		ModifyResponse: func(resp *http.Response) error {
			// A response landing here means the agent replied over the
			// mesh — live enough to refresh last_seen_at. Throttled
			// per-node via RecordProxyActivity.
			h.Nodes.RecordProxyActivity(node.ID)

			// Rewrite Location headers so redirects stay within the proxy prefix.
			if location := resp.Header.Get("Location"); location != "" {
				if strings.HasPrefix(location, "/") {
					resp.Header.Set("Location", proxyPrefix+location)
				} else {
					for _, lhPrefix := range []string{
						fmt.Sprintf("http://127.0.0.1:%d", port),
						fmt.Sprintf("http://localhost:%d", port),
					} {
						if strings.HasPrefix(location, lhPrefix) {
							path := strings.TrimPrefix(location, lhPrefix)
							if path == "" {
								path = "/"
							}
							resp.Header.Set("Location", proxyPrefix+path)
							break
						}
					}
				}
			}

			// Strip security headers that interfere with proxied content.
			// X-Frame-Options: allows loading in our iframe wrapper.
			// CSP: the proxied app's CSP (e.g., Nomad's script-src 'self')
			// breaks in the proxy context. Security is enforced at the
			// hopssh auth layer, not the proxied app's CSP.
			resp.Header.Del("X-Frame-Options")
			resp.Header.Del("Content-Security-Policy")
			resp.Header.Del("Content-Security-Policy-Report-Only")

			// For HTML responses, inject the SW bootstrap + WebSocket patch script.
			// This ensures proxied web apps load correctly even on first visit
			// (no SW active yet) and that WebSocket URLs are rewritten (SW can't
			// intercept WebSocket connections).
			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "text/html") {
				return nil
			}

			// No need to strip CSP — we inject an external <script src> tag
			// which is allowed by script-src 'self' (same-origin).

			// Decompress if needed — we must modify the raw HTML.
			encoding := resp.Header.Get("Content-Encoding")
			var body []byte
			if encoding == "gzip" {
				gr, err := gzip.NewReader(resp.Body)
				if err != nil {
					return nil // can't decompress, skip injection
				}
				body, err = io.ReadAll(gr)
				gr.Close()
				resp.Body.Close()
				if err != nil {
					return err
				}
			} else {
				var err error
				body, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					return err
				}
			}

			// Rewrite absolute paths in HTML attributes so assets load
			// on first visit without needing the SW to be active yet.
			// e.g., src="/ui/assets/app.js" → src="{proxyPrefix}/ui/assets/app.js"
			body = rewriteHTMLPaths(body, proxyPrefix)

			snippet := []byte(proxyBootstrapSnippet(proxyPrefix))
			modified := injectIntoHead(body, snippet)

			// Serve uncompressed — removes Content-Encoding so the browser
			// reads the plain HTML directly.
			resp.Header.Del("Content-Encoding")
			resp.Body = io.NopCloser(bytes.NewReader(modified))
			resp.ContentLength = int64(len(modified))
			resp.Header.Set("Content-Length", strconv.Itoa(len(modified)))

			return nil
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // client disconnected, normal for long-polling
			}
			log.Printf("[proxy] %s → %s:%d: %v", r.Method, nodeIP, port, err)
			http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}

func isWSUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// proxyBootstrapSnippet returns a <script src> tag that loads the external
// bootstrap script. Using an external script (not inline) ensures compatibility
// with any Content-Security-Policy — CSP's script-src 'self' allows same-origin
// external scripts, so this works universally without modifying the app's CSP.
func proxyBootstrapSnippet(proxyPrefix string) string {
	return fmt.Sprintf(`<script src="/sw-bootstrap.js?base=%s"></script>`, proxyPrefix)
}

// injectIntoHead inserts a snippet after the <head> tag in an HTML document.
// Falls back to prepending if no <head> tag is found.
func injectIntoHead(html, snippet []byte) []byte {
	lower := bytes.ToLower(html)
	idx := bytes.Index(lower, []byte("<head>"))
	if idx >= 0 {
		insertAt := idx + len("<head>")
		result := make([]byte, 0, len(html)+len(snippet))
		result = append(result, html[:insertAt]...)
		result = append(result, snippet...)
		result = append(result, html[insertAt:]...)
		return result
	}
	// Also try <head with attributes (e.g., <head lang="en">).
	idx = bytes.Index(lower, []byte("<head"))
	if idx >= 0 {
		closeIdx := bytes.IndexByte(lower[idx:], '>')
		if closeIdx >= 0 {
			insertAt := idx + closeIdx + 1
			result := make([]byte, 0, len(html)+len(snippet))
			result = append(result, html[:insertAt]...)
			result = append(result, snippet...)
			result = append(result, html[insertAt:]...)
			return result
		}
	}
	return append(snippet, html...)
}

// rewriteHTMLPaths rewrites absolute paths in HTML src, href, and action attributes
// to include the proxy prefix. This ensures assets load on first visit before the
// SW is active. Only skips protocol-relative URLs (//) and already-proxied paths.
func rewriteHTMLPaths(html []byte, proxyPrefix string) []byte {
	// Rewrite HTML attributes: src, href, action, srcset.
	for _, attr := range [][]byte{
		[]byte(`src="`), []byte(`src='`),
		[]byte(`href="`), []byte(`href='`),
		[]byte(`action="`), []byte(`action='`),
	} {
		html = rewriteAttrPaths(html, attr, proxyPrefix)
	}
	// srcset has different syntax: comma-separated "url descriptor" pairs.
	for _, attr := range [][]byte{
		[]byte(`srcset="`), []byte(`srcset='`),
	} {
		html = rewriteSrcsetPaths(html, attr, proxyPrefix)
	}
	// Rewrite CSS url() references inside <style> blocks.
	html = rewriteStyleURLs(html, proxyPrefix)
	return html
}

func rewriteAttrPaths(html, attr []byte, prefix string) []byte {
	prefixBytes := []byte(prefix)
	var result []byte
	remaining := html
	for {
		idx := bytes.Index(remaining, attr)
		if idx < 0 {
			result = append(result, remaining...)
			break
		}
		attrEnd := idx + len(attr)
		result = append(result, remaining[:attrEnd]...)
		remaining = remaining[attrEnd:]

		// Rewrite paths starting with / unless they already have the proxy prefix
		// or are protocol-relative (//). No framework-specific skips — this runs
		// on proxied HTML only, so every absolute path should go through the proxy.
		if len(remaining) > 0 && remaining[0] == '/' &&
			!bytes.HasPrefix(remaining, []byte("//")) &&
			!bytes.HasPrefix(remaining, prefixBytes) {
			result = append(result, prefixBytes...)
		}
	}
	return result
}

// rewriteSrcsetPaths rewrites absolute paths inside srcset attributes.
// srcset contains comma-separated entries like "/path/img.png 2x, /other.png 1x".
func rewriteSrcsetPaths(html, attr []byte, prefix string) []byte {
	prefixBytes := []byte(prefix)
	var result []byte
	remaining := html

	for {
		idx := bytes.Index(remaining, attr)
		if idx < 0 {
			result = append(result, remaining...)
			break
		}
		// Determine quote character (last byte of attr).
		quote := attr[len(attr)-1]
		attrEnd := idx + len(attr)
		result = append(result, remaining[:attrEnd]...)
		remaining = remaining[attrEnd:]

		// Find end of attribute value.
		closeIdx := bytes.IndexByte(remaining, quote)
		if closeIdx < 0 {
			result = append(result, remaining...)
			break
		}

		// Extract the srcset value and rewrite each entry.
		srcsetVal := remaining[:closeIdx]
		entries := bytes.Split(srcsetVal, []byte(","))
		for i, entry := range entries {
			if i > 0 {
				result = append(result, ',')
			}
			trimmed := bytes.TrimLeft(entry, " \t\n\r")
			leading := entry[:len(entry)-len(trimmed)]
			result = append(result, leading...)
			if len(trimmed) > 0 && trimmed[0] == '/' &&
				!bytes.HasPrefix(trimmed, []byte("//")) &&
				!bytes.HasPrefix(trimmed, prefixBytes) {
				result = append(result, prefixBytes...)
			}
			result = append(result, trimmed...)
		}
		remaining = remaining[closeIdx:]
	}
	return result
}

// rewriteStyleURLs rewrites url() references inside <style> blocks.
// Handles both url('/path') and url("/path") and url(/path) forms.
// This ensures fonts, background images, and other CSS resources
// load through the proxy on first visit (before the SW is active).
func rewriteStyleURLs(html []byte, prefix string) []byte {
	prefixBytes := []byte(prefix)

	var result []byte
	remaining := html

	for {
		lowerRemaining := bytes.ToLower(remaining)

		// Find next <style block.
		styleOpen := bytes.Index(lowerRemaining, []byte("<style"))
		if styleOpen < 0 {
			result = append(result, remaining...)
			break
		}
		// Find the closing > of the <style...> tag.
		tagClose := bytes.IndexByte(lowerRemaining[styleOpen:], '>')
		if tagClose < 0 {
			result = append(result, remaining...)
			break
		}
		contentStart := styleOpen + tagClose + 1

		// Find </style>.
		styleClose := bytes.Index(lowerRemaining[contentStart:], []byte("</style"))
		if styleClose < 0 {
			result = append(result, remaining...)
			break
		}
		styleEnd := contentStart + styleClose

		// Copy everything before the style content.
		result = append(result, remaining[:contentStart]...)

		// Rewrite url() inside the style content.
		cssContent := remaining[contentStart:styleEnd]
		rewritten := rewriteCSSURLs(cssContent, prefixBytes)
		result = append(result, rewritten...)

		remaining = remaining[styleEnd:]
	}
	return result
}

// rewriteCSSURLs rewrites url(/...) references in CSS content.
func rewriteCSSURLs(css, prefix []byte) []byte {
	var result []byte
	remaining := css

	for {
		idx := bytes.Index(remaining, []byte("url("))
		if idx < 0 {
			result = append(result, remaining...)
			break
		}
		urlStart := idx + 4 // after "url("
		result = append(result, remaining[:urlStart]...)
		remaining = remaining[urlStart:]

		// Skip optional whitespace and quote.
		ws := 0
		for ws < len(remaining) && (remaining[ws] == ' ' || remaining[ws] == '\t') {
			ws++
		}
		result = append(result, remaining[:ws]...)
		remaining = remaining[ws:]

		// Check for quote.
		hasQuote := false
		if len(remaining) > 0 && (remaining[0] == '\'' || remaining[0] == '"') {
			result = append(result, remaining[0])
			remaining = remaining[1:]
			hasQuote = true
			_ = hasQuote // used for documentation clarity
		}

		// Rewrite if path starts with / (not // and not already prefixed).
		if len(remaining) > 0 && remaining[0] == '/' &&
			!bytes.HasPrefix(remaining, []byte("//")) &&
			!bytes.HasPrefix(remaining, prefix) {
			result = append(result, prefix...)
		}
	}
	return result
}

// proxyNodeWebSocket relays a WebSocket connection through the mesh to the agent's proxy endpoint.
func (h *ProxyHandler) proxyNodeWebSocket(w http.ResponseWriter, r *http.Request, node *db.Node, inst *mesh.NetworkInstance, nodeIP string, port int, path string) {
	upgrader := websocket.Upgrader{CheckOrigin: h.checkOrigin}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[proxy] WebSocket upgrade failed: %v", err)
		return
	}
	defer browserConn.Close()

	wsDialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return inst.Dial(ctx, nodeIP)
		},
	}

	agentURL := fmt.Sprintf("ws://%s:%d/proxy/%d%s", nodeIP, 41820, port, path)
	if r.URL.RawQuery != "" {
		agentURL += "?" + r.URL.RawQuery
	}
	headers := http.Header{"Authorization": []string{"Bearer " + node.AgentToken}}

	agentConn, _, err := wsDialer.DialContext(r.Context(), agentURL, headers)
	if err != nil {
		log.Printf("[proxy] agent WebSocket dial failed: %v", err)
		browserConn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer agentConn.Close()

	// Successful upgrade to the agent = node is alive. Throttled.
	h.Nodes.RecordProxyActivity(node.ID)

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
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}
	membership, _ := h.Members.GetMembership(networkID, user.ID)
	if !authz.CheckAccess(user, network, membership).CanView() {
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
			ID:              n.ID,
			NetworkID:       n.NetworkID,
			Hostname:        n.Hostname,
			OS:              n.OS,
			Arch:            n.Arch,
			NebulaIP:        n.NebulaIP,
			AgentRealIP:     n.AgentRealIP,
			NodeType:        n.NodeType,
			ExposedPorts:    n.ExposedPorts,
			DNSName:         n.DNSName,
			Capabilities:    parseCapabilities(n.Capabilities),
			Status:          effectiveStatus(n.Status, n.LastSeenAt),
			LastSeenAt:      n.LastSeenAt,
			CreatedAt:       n.CreatedAt,
			PeersDirect:     n.PeersDirect,
			PeersRelayed:    n.PeersRelayed,
			PeersReportedAt: n.PeersReportedAt,
			AgentVersion:    n.AgentVersion,
			Connectivity:    deriveConnectivity(n.PeersDirect, n.PeersRelayed, n.NodeType),
			Peers:           parsePeerState(n.PeerState),
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
	network, node, err := h.requireAdmin(r)
	if err != nil {
		if err.Error() == "admin access required" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
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
	if h.Events != nil {
		targetID := node.ID
		details := jsonDetails(map[string]any{"name": body.Name, "dnsName": dnsName})
		h.Events.Record(network.ID, "node.renamed", &targetID, nil, details)
	}

	writeJSON(w, map[string]string{"name": body.Name, "dnsName": dnsName})
}

// UpdateCapabilities changes a node's enabled capabilities. Admin only.
// PUT /api/networks/{networkID}/nodes/{nodeID}/capabilities
func (h *ProxyHandler) UpdateCapabilities(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireAdmin(r)
	if err != nil {
		if err.Error() == "admin access required" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Validate capabilities.
	valid := map[string]bool{"terminal": true, "health": true, "forward": true}
	for _, c := range body.Capabilities {
		if !valid[c] {
			http.Error(w, "invalid capability: "+c+". Valid: terminal, health, forward", http.StatusBadRequest)
			return
		}
	}

	if err := h.Nodes.UpdateCapabilities(node.ID, body.Capabilities); err != nil {
		http.Error(w, "failed to update capabilities", http.StatusInternalServerError)
		return
	}

	h.InvalidateProxyCache(node.NetworkID, node.ID)

	if h.EventHub != nil {
		h.EventHub.Publish(node.NetworkID, Event{Type: "node.capabilities", Data: map[string]interface{}{"nodeId": node.ID, "capabilities": body.Capabilities}})
	}
	if h.Events != nil {
		targetID := node.ID
		details := jsonDetails(map[string]any{"capabilities": body.Capabilities})
		h.Events.Record(node.NetworkID, "node.capabilities", &targetID, nil, details)
	}

	writeJSON(w, map[string]interface{}{"capabilities": body.Capabilities})
}

func (h *ProxyHandler) DeleteNode(w http.ResponseWriter, r *http.Request) {
	_, node, err := h.requireAdmin(r)
	if err != nil {
		if err.Error() == "admin access required" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	// Node will be disconnected from the mesh when its cert expires (24h)
	// or immediately when it tries to re-register with the lighthouse.

	// Delete from database.
	if err := h.Nodes.Delete(node.ID); err != nil {
		http.Error(w, "failed to delete node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.InvalidateProxyCache(node.NetworkID, node.ID)

	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	h.audit(user.ID, "node.delete", &networkID, &node.ID, nil)
	if h.EventHub != nil {
		h.EventHub.Publish(networkID, Event{Type: "node.deleted", Data: map[string]string{"nodeId": node.ID}})
	}
	if h.Events != nil {
		targetID := node.ID
		details := jsonDetails(map[string]any{"hostname": node.Hostname})
		h.Events.Record(networkID, "node.deleted", &targetID, nil, details)
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
