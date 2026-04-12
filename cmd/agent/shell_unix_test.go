//go:build !windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialShell upgrades an httptest.Server to a WebSocket connection on /shell.
func dialShell(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/shell"
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	return conn
}

func TestHandleShell_ConnectAndReceiveOutput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shell", handleShell)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialShell(t, srv)
	defer conn.Close()

	// The shell should produce some output (prompt, motd, etc).
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected shell output, got error: %v", err)
	}
	if len(msg) == 0 {
		t.Fatal("expected non-empty shell output")
	}
}

func TestHandleShell_SendCommandAndReadOutput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shell", handleShell)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialShell(t, srv)
	defer conn.Close()

	// Give the shell a moment to initialize and send the prompt.
	time.Sleep(500 * time.Millisecond)

	// Send a command that produces known output.
	marker := "HOP_TEST_OK_12345"
	cmd := "echo " + marker + "\n"
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(cmd)); err != nil {
		t.Fatalf("write command: %v", err)
	}

	// Read until we see the marker in output.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var output strings.Builder
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		output.Write(msg)
		if strings.Contains(output.String(), marker) {
			return // success
		}
	}
	t.Fatalf("did not find marker %q in output: %q", marker, output.String())
}

func TestHandleShell_Resize(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shell", handleShell)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialShell(t, srv)
	defer conn.Close()

	// Send resize message: prefix=1, rows=40 (0x0028), cols=120 (0x0078).
	resize := []byte{shellResizePrefix, 0x00, 0x28, 0x00, 0x78}
	if err := conn.WriteMessage(websocket.BinaryMessage, resize); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	// Verify the connection is still alive after resize.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err != nil {
		// May timeout if no prompt yet, but should not get a connection error.
		if !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("connection broken after resize: %v", err)
		}
	}
}

func TestHandleShell_CleanShutdownOnClose(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shell", handleShell)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialShell(t, srv)

	// Read a bit of output to confirm the shell started.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	conn.ReadMessage()

	// Close the connection — the handler should clean up without panic.
	conn.Close()

	// Give the handler time to clean up.
	time.Sleep(500 * time.Millisecond)

	// If we get here without panic/hang, cleanup works.
}
