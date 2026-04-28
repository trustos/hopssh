package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLocalAuthMiddlewareCORSPreflight is the regression test for the
// "agent unreachable" first-launch failure on freshly-downloaded .app.
//
// Background: the desktop app's WebView uses fetch() against the local
// API with an Authorization header. Because the WebView's origin
// (tauri://localhost or http://tauri.localhost) differs from the
// agent's loopback origin, the browser issues a CORS preflight OPTIONS
// request before any actual GET/POST. The preflight by spec does NOT
// carry the Authorization header — it's sent unauthenticated.
//
// Pre-fix: localAuthMiddleware checked Authorization BEFORE the
// OPTIONS short-circuit, so preflights returned 401 → the browser
// blocked the actual GET → SSE subscribeEvents() saw a fetch error
// → agent.online flipped to false → UI showed "agent unreachable".
//
// Post-fix: OPTIONS is answered with 204 + CORS headers BEFORE the
// auth check, matching every CORS-aware service on the planet.
func TestLocalAuthMiddlewareCORSPreflight(t *testing.T) {
	const tok = "test-token-1234"
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	handler := localAuthMiddleware(tok, upstream)

	t.Run("preflight without auth returns 204 + CORS headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/local/status", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("Origin", "http://tauri.localhost")
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "authorization")
		// NOTE: deliberately NO Authorization header — that's exactly
		// how browsers send preflights. If we ever require auth here
		// again, the WebView's actual GET will be blocked.

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Code; got != http.StatusNoContent {
			t.Errorf("preflight without auth: status = %d, want %d", got, http.StatusNoContent)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
		}
		if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
			t.Errorf("Access-Control-Allow-Methods = %q, must include GET", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "authorization") {
			t.Errorf("Access-Control-Allow-Headers = %q, must include Authorization", got)
		}
	})

	t.Run("non-preflight without auth returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/local/status", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusUnauthorized {
			t.Errorf("GET without auth: status = %d, want %d", got, http.StatusUnauthorized)
		}
	})

	t.Run("authenticated GET passes through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/local/status", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusOK {
			t.Errorf("authenticated GET: status = %d, want %d", got, http.StatusOK)
		}
	})

	t.Run("non-loopback origin rejected even on OPTIONS", func(t *testing.T) {
		// The OPTIONS short-circuit must NOT bypass the loopback check —
		// otherwise an external attacker could probe the API for its
		// existence even if they can't auth. Loopback gate stays first.
		req := httptest.NewRequest(http.MethodOptions, "/local/status", nil)
		req.RemoteAddr = "203.0.113.7:51000"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusForbidden {
			t.Errorf("non-loopback OPTIONS: status = %d, want %d", got, http.StatusForbidden)
		}
	})
}
