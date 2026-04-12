package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

const agentPort = 41820

// handleProxy reverse-proxies HTTP (and WebSocket) requests to a service
// running on localhost:{port}. The URL format is /proxy/{port}/{path...}.
// This enables the control plane to proxy browser requests to node-local
// services through the mesh without requiring OS firewall rules.
func handleProxy(w http.ResponseWriter, r *http.Request) {
	// Parse /proxy/{port}/... from the path.
	path := strings.TrimPrefix(r.URL.Path, "/proxy/")
	slashIdx := strings.IndexByte(path, '/')
	var portStr, remainingPath string
	if slashIdx >= 0 {
		portStr = path[:slashIdx]
		remainingPath = path[slashIdx:] // includes leading /
	} else {
		portStr = path
		remainingPath = "/"
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	if port == agentPort {
		http.Error(w, "cannot proxy to agent API port", http.StatusForbidden)
		return
	}

	// WebSocket upgrade: relay bidirectionally (same pattern as handleShell).
	if isWebSocketUpgrade(r) {
		proxyWebSocket(w, r, port, remainingPath)
		return
	}

	// HTTP: reverse proxy to localhost:{port}.
	target := &url.URL{
		Scheme:   "http",
		Host:     fmt.Sprintf("127.0.0.1:%d", port),
		Path:     remainingPath,
		RawQuery: r.URL.RawQuery,
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = target
			req.Host = target.Host
			// Remove the agent bearer token — the target service shouldn't see it.
			req.Header.Del("Authorization")
		},
		FlushInterval: -1, // flush immediately for streaming (SSE, chunked responses)
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[proxy] %s localhost:%d: %v", r.Method, port, err)
			http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// proxyWebSocket upgrades both sides and relays messages bidirectionally.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, port int, path string) {
	// Upgrade the incoming connection (from control plane).
	incoming, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[proxy] WebSocket upgrade failed: %v", err)
		return
	}
	defer incoming.Close()

	// Dial the target service on localhost.
	targetURL := fmt.Sprintf("ws://127.0.0.1:%d%s", port, path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Strip auth header for the target service.
	headers := http.Header{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if lk != "authorization" && lk != "connection" && lk != "upgrade" &&
			lk != "sec-websocket-key" && lk != "sec-websocket-version" &&
			lk != "sec-websocket-extensions" && lk != "sec-websocket-protocol" {
			headers[k] = v
		}
	}

	target, _, err := websocket.DefaultDialer.Dial(targetURL, headers)
	if err != nil {
		log.Printf("[proxy] WebSocket dial localhost:%d failed: %v", port, err)
		incoming.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer target.Close()

	done := make(chan struct{})

	// Target → Incoming
	go func() {
		defer close(done)
		for {
			msgType, msg, err := target.ReadMessage()
			if err != nil {
				return
			}
			if err := incoming.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	// Incoming → Target
	go func() {
		for {
			msgType, msg, err := incoming.ReadMessage()
			if err != nil {
				target.Close()
				return
			}
			if err := target.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	<-done
}
