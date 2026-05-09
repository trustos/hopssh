package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db"
)

// Routes E2E — exercises Stage 4's full server-side path:
//
//   PUT /api/networks/{id}/nodes/{id}/routes
//        ↓ persists to nodes.routes column
//   POST /api/heartbeat (renew handler)
//        ↓ reads nodes.routes for the requesting node
//        ↓ injects "routes" field into the response
//
// Plus negative-path tests covering the CIDR validation rules
// (0.0.0.0/0 default route, loopback, malformed) so regressions in
// the validation logic surface fast.

// routesTestStores bundles the stores needed across the routes E2E
// tests. Each test gets its own SQLite file (no cross-contamination)
// + a cleanup that closes the DB.
type routesTestStores struct {
	db       *db.DBPair
	users    *db.UserStore
	sessions *db.SessionStore
	networks *db.NetworkStore
	nodes    *db.NodeStore
	members  *db.NetworkMemberStore
	audit    *db.AuditStore
}

func newRoutesTestStores(t *testing.T) (*routesTestStores, func()) {
	t.Helper()
	dir := t.TempDir()
	pair, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(pair.WriteDB); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	stores := &routesTestStores{
		db:       pair,
		users:    db.NewUserStore(pair),
		sessions: db.NewSessionStore(pair),
		networks: db.NewNetworkStore(pair, enc),
		nodes:    db.NewNodeStore(pair, enc),
		members:  db.NewNetworkMemberStore(pair),
		audit:    db.NewAuditStore(pair),
	}
	cleanup := func() { pair.Close() }
	return stores, cleanup
}

// seedNodeForRoutesTest creates a user + network + node, returns the
// canonical IDs the test handlers will reference. The user owns the
// network (admin role) so requireAdmin succeeds for them.
func seedNodeForRoutesTest(t *testing.T, s *routesTestStores) (*db.User, *db.Network, *db.Node) {
	t.Helper()
	user := &db.User{
		ID:    uuid.New().String(),
		Email: "owner@example.com",
		Name:  "Owner",
	}
	if err := s.users.Create(user); err != nil {
		t.Fatal(err)
	}
	netID := uuid.New().String()
	if err := s.networks.Create(&db.Network{
		ID:           netID,
		UserID:       user.ID,
		Name:         "test-net",
		Slug:         "test-net",
		NebulaSubnet: "10.42.99.0/24",
		DNSDomain:    "test",
	}); err != nil {
		t.Fatal(err)
	}
	network, err := s.networks.Get(netID)
	if err != nil {
		t.Fatal(err)
	}
	if network == nil {
		t.Fatal("network not persisted")
	}

	nodeID := uuid.New().String()
	if err := s.nodes.Create(&db.Node{
		ID:                  nodeID,
		NetworkID:           netID,
		Hostname:            "test-host",
		AgentToken:          "test-agent-token",
		EnrollmentToken:     stringPtr("test-enrollment-token"),
		EnrollmentExpiresAt: int64Ptr(time.Now().Add(24 * time.Hour).Unix()),
		NodeType:            "node",
		Status:              "enrolled",
	}); err != nil {
		t.Fatal(err)
	}
	node, err := s.nodes.Get(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node == nil {
		t.Fatal("node not persisted")
	}
	return user, network, node
}

func stringPtr(s string) *string { return &s }
func int64Ptr(i int64) *int64    { return &i }

// authedReqWithChiParams wraps a request with the chi URL params + an
// auth.WithUser context so handlers like ProxyHandler.UpdateRoutes
// (which read from chi.URLParam + auth.UserFromContext) get what they
// expect without spinning up the full router middleware.
func authedReqWithChiParams(t *testing.T, method, path string, body []byte, user *db.User, networkID, nodeID string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("networkID", networkID)
	rctx.URLParams.Add("nodeID", nodeID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithUser(ctx, &db.UserProfile{ID: user.ID, Email: user.Email, Name: user.Name})
	return req.WithContext(ctx)
}

func TestRoutes_Validation_RejectsBadCIDRs(t *testing.T) {
	stores, cleanup := newRoutesTestStores(t)
	defer cleanup()

	user, network, node := seedNodeForRoutesTest(t, stores)
	h := &ProxyHandler{
		Networks: stores.networks,
		Nodes:    stores.nodes,
		Members:  stores.members,
		Audit:    stores.audit,
	}

	cases := []struct {
		name   string
		routes []string
		want   int // expected HTTP status
	}{
		{"valid /16", []string{"10.0.0.0/16"}, http.StatusOK},
		{"valid /24", []string{"192.168.50.0/24"}, http.StatusOK},
		{"multiple valid", []string{"10.0.0.0/16", "172.16.0.0/12"}, http.StatusOK},
		{"empty (clear)", []string{}, http.StatusOK},
		// Validation rejections.
		{"default route", []string{"0.0.0.0/0"}, http.StatusBadRequest},
		{"loopback", []string{"127.0.0.0/8"}, http.StatusBadRequest},
		{"link-local", []string{"169.254.0.0/16"}, http.StatusBadRequest},
		{"multicast", []string{"224.0.0.0/4"}, http.StatusBadRequest},
		{"malformed", []string{"not-a-cidr"}, http.StatusBadRequest},
		{"missing prefix length", []string{"10.0.0.0"}, http.StatusBadRequest},
		// One bad CIDR poisons the whole list (atomic).
		{"good + bad", []string{"10.0.0.0/16", "0.0.0.0/0"}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"routes": tc.routes})
			req := authedReqWithChiParams(t, "PUT",
				"/api/networks/"+network.ID+"/nodes/"+node.ID+"/routes",
				body, user, network.ID, node.ID)
			rec := httptest.NewRecorder()
			h.UpdateRoutes(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d. body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestRoutes_Validation_DedupesAndCanonicalises(t *testing.T) {
	stores, cleanup := newRoutesTestStores(t)
	defer cleanup()

	user, network, node := seedNodeForRoutesTest(t, stores)
	h := &ProxyHandler{
		Networks: stores.networks,
		Nodes:    stores.nodes,
		Members:  stores.members,
		Audit:    stores.audit,
	}

	// Send the same CIDR three times + a non-canonical form (host
	// bits set; canonicalises via Masked() to the network address).
	body, _ := json.Marshal(map[string]any{
		"routes": []string{
			"10.0.0.0/16",
			"10.0.0.0/16",
			"10.0.5.0/16", // non-canonical — host bits set; masked = 10.0.0.0/16
		},
	})
	req := authedReqWithChiParams(t, "PUT",
		"/api/networks/"+network.ID+"/nodes/"+node.ID+"/routes",
		body, user, network.ID, node.ID)
	rec := httptest.NewRecorder()
	h.UpdateRoutes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", rec.Code, rec.Body.String())
	}

	var resp struct{ Routes []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Routes) != 1 {
		t.Errorf("response has %d routes, want 1 (deduped + canonicalised). got=%v", len(resp.Routes), resp.Routes)
	}
	if len(resp.Routes) > 0 && resp.Routes[0] != "10.0.0.0/16" {
		t.Errorf("response route = %q, want 10.0.0.0/16 (canonical)", resp.Routes[0])
	}

	// Verify the persisted DB state matches.
	got, err := stores.nodes.GetRoutes(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("DB has %d routes, want 1", len(got))
	}
	if len(got) > 0 && got[0].Route != "10.0.0.0/16" {
		t.Errorf("DB route = %q, want 10.0.0.0/16", got[0].Route)
	}
}

func TestRoutes_HeartbeatIncludesRoutes(t *testing.T) {
	stores, cleanup := newRoutesTestStores(t)
	defer cleanup()

	_, _, node := seedNodeForRoutesTest(t, stores)

	// Persist routes via the store directly.
	wantRoutes := []db.NodeRoute{
		{Route: "10.0.0.0/16"},
		{Route: "192.168.50.0/24"},
	}
	if err := stores.nodes.UpdateRoutes(node.ID, wantRoutes); err != nil {
		t.Fatal(err)
	}

	renewH := &RenewHandler{
		Networks: stores.networks,
		Nodes:    stores.nodes,
	}
	body := []byte(`{"nodeId":"` + node.ID + `"}`)
	req := httptest.NewRequest("POST", "/api/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+node.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	renewH.Heartbeat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want 200. body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	gotRoutes, ok := resp["routes"].([]any)
	if !ok {
		t.Fatalf("response missing 'routes' field. body=%s", rec.Body.String())
	}
	if len(gotRoutes) != 2 {
		t.Errorf("response has %d routes, want 2", len(gotRoutes))
	}
	wantSet := map[string]bool{"10.0.0.0/16": true, "192.168.50.0/24": true}
	for _, r := range gotRoutes {
		s, _ := r.(string)
		if !wantSet[s] {
			t.Errorf("unexpected route in response: %q", s)
		}
		delete(wantSet, s)
	}
	if len(wantSet) != 0 {
		t.Errorf("routes missing from response: %v", wantSet)
	}
}

func TestRoutes_HeartbeatOmitsRoutesWhenEmpty(t *testing.T) {
	stores, cleanup := newRoutesTestStores(t)
	defer cleanup()

	_, _, node := seedNodeForRoutesTest(t, stores)

	// Don't set any routes.
	renewH := &RenewHandler{
		Networks: stores.networks,
		Nodes:    stores.nodes,
	}
	body := []byte(`{"nodeId":"` + node.ID + `"}`)
	req := httptest.NewRequest("POST", "/api/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+node.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	renewH.Heartbeat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want 200. body=%s", rec.Code, rec.Body.String())
	}

	// "routes" key MUST be absent (not empty array) so older agents
	// that don't decode the field aren't confused by an unexpected
	// JSON shape. Backwards-compat contract.
	if strings.Contains(rec.Body.String(), `"routes"`) {
		t.Errorf("response contains routes field but should omit when DB is empty. body=%s", rec.Body.String())
	}
}
