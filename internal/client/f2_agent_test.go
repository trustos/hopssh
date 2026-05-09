package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// F2 (v0.10.85+) agent side: every device-flow poll and token enroll
// includes the deduplicated set of CA fingerprints from the local
// enrollment registry. The server short-circuits with HTTP 409 if the
// would-be network's CA fingerprint is in the list, preventing orphan
// node rows. The agent translates the 409 into a structured error the
// desktop UI can render.
//
// These tests verify both halves of the contract:
//   1. The agent body always includes existingNetworkCAs sourced from
//      the registry's CAFingerprints() helper.
//   2. A 409 from the server becomes an "already enrolled in <network>"
//      message at the desktop API surface.

// TestF2_TokenEnroll_BodyIncludesExistingCAs verifies the request body
// the agent posts to /api/enroll includes the existingNetworkCAs field
// populated from the registry.
func TestF2_TokenEnroll_BodyIncludesExistingCAs(t *testing.T) {
	var captured atomic.Pointer[map[string]any]
	cpane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		captured.Store(&got)
		// Return a valid enrollment so the handler completes successfully.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"nodeId":"test-f2-node-1",
			"caCert":"-----BEGIN NEBULA CA-----\nFAKE-NEW-NETWORK\n-----END NEBULA CA-----\n",
			"nodeCert":"-----BEGIN NEBULA CERT-----\nFAKE\n-----END NEBULA CERT-----\n",
			"nodeKey":"-----BEGIN NEBULA X25519 PRIVATE KEY-----\nFAKE\n-----END NEBULA X25519 PRIVATE KEY-----\n",
			"agentToken":"tok-f2-1",
			"serverIP":"10.42.99.1",
			"nebulaIP":"10.42.99.5/24",
			"lighthousePort":42099,
			"lighthouseHost":"127.0.0.1",
			"dnsDomain":"f2-existing-1"
		}`))
	}))
	defer cpane.Close()

	srv, h, _ := newTestLocalAPIServer(t, func(string) error { return nil }, nil)

	// Pre-seed the registry with one enrollment so CAFingerprints()
	// returns one entry. The agent should include it in the request body.
	preexisting := &Enrollment{
		Name:          "preexisting",
		NodeID:        "preexisting-node",
		Endpoint:      "https://hopssh.com",
		TunMode:       "userspace",
		CAFingerprint: "deadbeef0001",
		EnrolledAt:    time.Now().UTC(),
	}
	if err := srv.enrolls().Add(preexisting); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	body := []byte(`{"endpoint":"` + cpane.URL + `","name":"f2-existing-1","tunMode":"userspace"}`)
	rec, _ := authedReq(t, h, "POST", "/local/enroll/token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("token enroll returned %d: %s", rec.Code, rec.Body.String())
	}

	got := captured.Load()
	if got == nil {
		t.Fatal("control-plane handler never received a request body")
	}
	cas, ok := (*got)["existingNetworkCAs"].([]any)
	if !ok {
		t.Fatalf("existingNetworkCAs missing or wrong type in body: %v", *got)
	}
	if len(cas) != 1 {
		t.Errorf("expected exactly one CA fingerprint, got %d: %v", len(cas), cas)
	} else if s, _ := cas[0].(string); s != "deadbeef0001" {
		t.Errorf("CA fingerprint = %q, want %q", s, "deadbeef0001")
	}
}

// TestF2_TokenEnroll_409Translation verifies that when the server
// returns 409 with a JSON body, the agent surfaces a user-friendly
// error including the network name.
func TestF2_TokenEnroll_409Translation(t *testing.T) {
	cpane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":       "this device is already enrolled in this network",
			"networkName": "home",
		})
	}))
	defer cpane.Close()

	srv, h, _ := newTestLocalAPIServer(t, func(string) error { return nil }, nil)

	body := []byte(`{"endpoint":"` + cpane.URL + `","name":"home","tunMode":"userspace"}`)
	rec, _ := authedReq(t, h, "POST", "/local/enroll/token", body)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 from agent (translating server's 409), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already enrolled") {
		t.Errorf("response body should mention 'already enrolled', got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "home") {
		t.Errorf("response body should mention the network name 'home', got: %s", rec.Body.String())
	}
	// Critical invariant: NO enrollment was persisted to the registry,
	// because the server rejected with 409 before issuing any cert.
	if len(srv.enrolls().List()) != 0 {
		t.Errorf("conflict response must NOT persist an enrollment; registry has %d entries: %v",
			len(srv.enrolls().List()), srv.enrolls().Names())
	}
}

// TestRegistry_CAFingerprints_DedupAndSort verifies the helper used by
// F2 to build the existingNetworkCAs list. Defends against duplicates
// silently inflating the list and against unstable order making the
// request body non-deterministic.
func TestRegistry_CAFingerprints_DedupAndSort(t *testing.T) {
	reg := &enrollmentRegistry{path: t.TempDir() + "/enrollments.json"}
	_ = reg.Add(&Enrollment{Name: "a", Endpoint: "https://hopssh.com", CAFingerprint: "ffff2222"})
	_ = reg.Add(&Enrollment{Name: "b", Endpoint: "https://other.com", CAFingerprint: "aaaa1111"})
	_ = reg.Add(&Enrollment{Name: "c", Endpoint: "https://hopssh.com", CAFingerprint: "ffff2222"}) // dup of a's CA
	_ = reg.Add(&Enrollment{Name: "d", Endpoint: "https://other.com", CAFingerprint: ""})           // empty — must be skipped

	got := reg.CAFingerprints()
	want := []string{"aaaa1111", "ffff2222"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, fp := range want {
		if got[i] != fp {
			t.Errorf("got[%d] = %q, want %q (full slice: %v)", i, got[i], fp, got)
		}
	}
}
