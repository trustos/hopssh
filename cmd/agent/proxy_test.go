package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleProxy_InvalidPort(t *testing.T) {
	tests := []struct {
		name string
		path string
		code int
	}{
		{"zero", "/proxy/0/", http.StatusBadRequest},
		{"negative", "/proxy/-1/", http.StatusBadRequest},
		{"too high", "/proxy/99999/", http.StatusBadRequest},
		{"not a number", "/proxy/abc/", http.StatusBadRequest},
		{"empty", "/proxy//", http.StatusBadRequest},
		{"agent port blocked", "/proxy/41820/", http.StatusForbidden},
		{"path traversal", "/proxy/8080/../../../etc/passwd", http.StatusBadRequest},
		{"path traversal encoded", "/proxy/8080/foo/..%2f..%2fetc/passwd", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()
			handleProxy(w, req)
			if w.Code != tt.code {
				t.Fatalf("path %s: expected %d, got %d (%s)", tt.path, tt.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleProxy_ProxiesToLocalhost(t *testing.T) {
	// Start a local HTTP server to act as the target service.
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "path=%s query=%s", r.URL.Path, r.URL.RawQuery)
	}))
	// Bind to 127.0.0.1 explicitly (handleProxy only proxies to localhost).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	target.Listener = ln
	target.Start()
	defer target.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	tests := []struct {
		name     string
		urlPath  string
		wantBody string
	}{
		{"root path", fmt.Sprintf("/proxy/%d/", port), "path=/ query="},
		{"subpath", fmt.Sprintf("/proxy/%d/v1/jobs", port), "path=/v1/jobs query="},
		{"with query", fmt.Sprintf("/proxy/%d/search?q=hello", port), "path=/search query=q=hello"},
		{"deep path", fmt.Sprintf("/proxy/%d/a/b/c", port), "path=/a/b/c query="},
		{"no trailing slash", fmt.Sprintf("/proxy/%d", port), "path=/ query="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.urlPath, nil)
			w := httptest.NewRecorder()
			handleProxy(w, req)

			resp := w.Result()
			body, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
			}
			if string(body) != tt.wantBody {
				t.Errorf("expected %q, got %q", tt.wantBody, string(body))
			}
		})
	}
}

func TestHandleProxy_StripsAuthHeader(t *testing.T) {
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("Authorization header should be stripped, got %q", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	target.Listener = ln
	target.Start()
	defer target.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	req := httptest.NewRequest("GET", fmt.Sprintf("/proxy/%d/", port), nil)
	req.Header.Set("Authorization", "Bearer secret-agent-token")
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleProxy_TargetNotListening(t *testing.T) {
	// Use a port that's very unlikely to have anything listening.
	req := httptest.NewRequest("GET", "/proxy/19999/", nil)
	w := httptest.NewRecorder()
	handleProxy(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleProxy_PostWithBody(t *testing.T) {
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "method=%s body=%s", r.Method, string(body))
	}))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	target.Listener = ln
	target.Start()
	defer target.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	req := httptest.NewRequest("POST", fmt.Sprintf("/proxy/%d/api/submit", port), strings.NewReader(`{"key":"value"}`))
	w := httptest.NewRecorder()
	handleProxy(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	want := `method=POST body={"key":"value"}`
	if string(body) != want {
		t.Errorf("expected %q, got %q", want, string(body))
	}
}
