package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// --- Auth middleware ---

func TestAuthMiddleware_ValidToken(t *testing.T) {
	handler := authMiddleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	handler := authMiddleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_WrongToken(t *testing.T) {
	handler := authMiddleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_NoBearerPrefix(t *testing.T) {
	handler := authMiddleware("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("Authorization", "secret-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// --- Health endpoint ---

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var resp healthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status=ok, got %q", resp.Status)
	}
	if resp.OS != runtime.GOOS {
		t.Errorf("expected OS=%s, got %q", runtime.GOOS, resp.OS)
	}
	if resp.Arch != runtime.GOARCH {
		t.Errorf("expected Arch=%s, got %q", runtime.GOARCH, resp.Arch)
	}
	if resp.Hostname == "" {
		t.Error("expected non-empty hostname")
	}
	if resp.Uptime == "" {
		t.Error("expected non-empty uptime")
	}
}

// --- Exec endpoint ---

func TestHandleExec_Success(t *testing.T) {
	body := `{"command":"echo","args":["hello world"]}`
	req := httptest.NewRequest("POST", "/exec", strings.NewReader(body))
	w := httptest.NewRecorder()
	handleExec(w, req)

	resp := w.Result()
	out, _ := io.ReadAll(resp.Body)
	output := string(out)

	if !strings.Contains(output, "hello world") {
		t.Errorf("expected output to contain 'hello world', got %q", output)
	}
	if !strings.Contains(output, "---EXIT:0---") {
		t.Errorf("expected exit code 0, got %q", output)
	}
}

func TestHandleExec_MissingCommand(t *testing.T) {
	body := `{"command":""}`
	req := httptest.NewRequest("POST", "/exec", strings.NewReader(body))
	w := httptest.NewRecorder()
	handleExec(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleExec_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/exec", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	handleExec(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleExec_NonZeroExit(t *testing.T) {
	body := `{"command":"sh","args":["-c","exit 42"]}`
	req := httptest.NewRequest("POST", "/exec", strings.NewReader(body))
	w := httptest.NewRecorder()
	handleExec(w, req)

	resp := w.Result()
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "---EXIT:42---") {
		t.Errorf("expected exit code 42, got %q", string(out))
	}
}

// --- Upload endpoint (path validation) ---

func TestHandleUpload_MissingDestPath(t *testing.T) {
	req := httptest.NewRequest("POST", "/upload", strings.NewReader("data"))
	w := httptest.NewRecorder()
	handleUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleUpload_PathTraversal(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"absolute outside", "/etc/passwd"},
		{"relative escape", "/var/hop-agent/uploads/../../etc/passwd"},
		{"dot dot", "/var/hop-agent/../secret"},
		{"sibling dir", "/var/hop-agent/other/file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/upload", strings.NewReader("data"))
			req.Header.Set("X-Dest-Path", tt.path)
			w := httptest.NewRecorder()
			handleUpload(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("path %q: expected 403, got %d", tt.path, w.Code)
			}
		})
	}
}

// --- WinTun (non-Windows stub) ---

func TestEnsureWinTun(t *testing.T) {
	// On non-Windows platforms, ensureWinTun is a no-op.
	// On Windows, it extracts the DLL (tested via the Windows integration test).
	if err := ensureWinTun(); err != nil {
		t.Fatalf("ensureWinTun failed: %v", err)
	}
}
