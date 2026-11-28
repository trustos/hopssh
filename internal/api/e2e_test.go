//go:build !windows

package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/miekg/dns"
	"github.com/slackhq/nebula/cert"

	"github.com/trustos/hopssh/internal/api"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/e2etest"
)

// These tests run the real hop-server (see internal/e2etest) and use its API
// as a browser, an agent or an attacker would.

type M = e2etest.M

type household struct {
	s                              *e2etest.Server
	owner, admin, member, outsider *e2etest.Client
	anon                           *e2etest.Client
	net                            e2etest.Network
}

// newHousehold is one network with an owner, an invited admin and an invited
// member, plus a user who has nothing to do with it.
func newHousehold(t *testing.T, env ...string) household {
	t.Helper()
	s := e2etest.Start(t, env...)
	h := household{s: s, owner: s.Register("owner"), admin: s.Register("admin"), member: s.Register("member"),
		outsider: s.Register("outsider"), anon: s.Anonymous()}
	h.net = h.owner.CreateNetwork("home")
	h.owner.Join(h.admin, h.net.ID, "admin")
	h.owner.Join(h.member, h.net.ID, "member")
	return h
}

func (h household) url(format string, args ...any) string {
	return "/api/networks/" + h.net.ID + fmt.Sprintf(format, args...)
}

// Routes that need no session, with what protects them instead.
var publicRoutes = map[string]string{
	"GET /api/auth/status":          "nothing to protect",
	"POST /api/auth/register":       "rate limit",
	"POST /api/auth/login":          "password",
	"GET /api/auth/oauth/status":    "nothing to protect",
	"GET /api/auth/github/start":    "OAuth state",
	"GET /api/auth/github/callback": "OAuth state",
	"POST /api/enroll":              "enrollment token",
	"POST /api/device/code":         "rate limit",
	"POST /api/device/poll":         "device code",
	"POST /api/renew":               "agent token",
	"POST /api/heartbeat":           "agent token",
	"POST /api/clipboard/announce":  "agent token",
	"GET /api/clipboard/poll":       "agent token",
	"GET /api/clipboard/{clipId}":   "agent token",
	"GET /api/bundles/{token}":      "bundle token",
	"GET /api/invites/{code}":       "invite code",
	"GET /healthz":                  "nothing to protect",
	"GET /version":                  "nothing to protect",
	"GET /install.sh":               "nothing to protect",
	"GET /install-mac.sh":           "nothing to protect",
	"GET /download/SHA256SUMS":      "nothing to protect",
	"GET /download/{binary}":        "nothing to protect",
	"GET /download/desktop/{asset}": "nothing to protect",
	"GET /swagger/doc.json":         "nothing to protect",
	"GET /swagger/*":                "nothing to protect",
}

// TestE2E_Access asks every authenticated route as every kind of user. A route
// that is added to the router and not listed here (or above) fails the test.
func TestE2E_Access(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()

	node := func(suffix string) func() string {
		return func() string { return h.url("/nodes/%s%s", box.NodeID, suffix) }
	}
	at := func(p string) func() string { return func() string { return p } }
	freshNode := func() string { id, _ := h.owner.EnrollToken(h.net.ID); return h.url("/nodes/%s", id) }
	freshNetwork := 0
	userCode := func() string {
		var dc struct{ UserCode string }
		h.s.Anonymous().Want(200, "POST", "/api/device/code", nil, &dc)
		return dc.UserCode
	}
	invite := func() (id, code string) {
		var inv struct{ ID, Code string }
		h.owner.Want(201, "POST", h.url("/invites"), M{}, &inv)
		return inv.ID, inv.Code
	}
	extraMember := func() string {
		u := h.s.Register(fmt.Sprintf("extra%d", time.Now().UnixNano()))
		h.owner.Join(u, h.net.ID, "member")
		return h.url("/members/%s", memberID(h.owner, h.net.ID, u.ID))
	}
	names := 0
	ownerSet := func() string {
		names++
		var set struct{ ID string }
		h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": fmt.Sprint("set", names)}, &set)
		return set.ID
	}
	covered := map[string]bool{}

	// want: the status for owner, admin, member, outsider and an anonymous
	// caller. gap: what it is today where that is wrong.
	for _, row := range []struct {
		route string
		path  func() string
		body  any
		want  string
		gap   string
	}{
		{route: "GET /api/auth/me", path: at("/api/auth/me"), want: "200 200 200 200 401"},
		{route: "GET /api/audit", path: at("/api/audit"), want: "200 200 200 200 401"},
		{route: "GET /api/networks", path: at("/api/networks"), want: "200 200 200 200 401"},
		{route: "POST /api/networks", path: at("/api/networks"), body: func() any { freshNetwork++; return M{"name": fmt.Sprint("n", freshNetwork)} }, want: "201 201 201 201 401"},

		{route: "GET /api/networks/{networkID}", path: at(h.url("")), want: "200 200 200 404 401"},
		{route: "GET /api/networks/{networkID}/audit", path: at(h.url("/audit")), want: "200 200 200 404 401"},
		{route: "GET /api/networks/{networkID}/events", path: at(h.url("/events")), want: "400 400 400 404 401"}, // 400: not a WebSocket
		{route: "GET /api/networks/{networkID}/events/history", path: at(h.url("/events/history")), want: "200 200 200 404 401"},

		{route: "GET /api/networks/{networkID}/nodes", path: at(h.url("/nodes")), want: "200 200 200 404 401"},
		{route: "POST /api/networks/{networkID}/nodes", path: at(h.url("/nodes")), body: M{}, want: "201 201 201 404 401"},
		{route: "POST /api/networks/{networkID}/join", path: at(h.url("/join")), body: M{"hostname": "laptop"}, want: "200 200 200 404 401"},
		{route: "PATCH /api/networks/{networkID}/nodes/{nodeID}", path: node(""), body: M{"name": "box"}, want: "200 200 403 404 401", gap: "200 200 403 403 401"},
		{route: "PUT /api/networks/{networkID}/nodes/{nodeID}/capabilities", path: node("/capabilities"), body: M{"capabilities": []string{"terminal", "health", "forward"}}, want: "200 200 403 404 401", gap: "200 200 403 403 401"},
		{route: "PUT /api/networks/{networkID}/nodes/{nodeID}/routes", path: node("/routes"), body: M{"routes": []string{}}, want: "200 200 403 404 401", gap: "200 200 403 403 401"},
		{route: "DELETE /api/networks/{networkID}/nodes/{nodeID}", path: freshNode, want: "204 204 403 404 401", gap: "204 204 403 403 401"},
		{route: "POST /api/networks/{networkID}/nodes/prune", path: at(h.url("/nodes/prune")), body: M{"olderThanSeconds": 86400}, want: "200 200 403 404 401", gap: "200 200 403 403 401"},

		{route: "GET /api/networks/{networkID}/nodes/{nodeID}/health", path: node("/health"), want: "200 200 200 404 401"},
		// TestE2E_MemberShell in cmd/agent pins that a plain member may run
		// commands. Whether they should is a product decision.
		{route: "GET /api/networks/{networkID}/nodes/{nodeID}/shell", path: node("/shell"), want: "400 400 400 404 401"}, // 400: not a WebSocket
		{route: "POST /api/networks/{networkID}/nodes/{nodeID}/exec", path: node("/exec"), body: M{"command": "true"}, want: "200 200 200 404 401"},
		{route: "* /api/networks/{networkID}/nodes/{nodeID}/proxy/{port}/*", path: node("/proxy/8080/"), want: "200 200 200 404 401"},
		{route: "POST /api/networks/{networkID}/nodes/{nodeID}/port-forwards", path: node("/port-forwards"), body: M{"remotePort": 8080}, want: "201 201 201 404 401"},
		{route: "GET /api/networks/{networkID}/port-forwards", path: at(h.url("/port-forwards")), want: "200 200 200 404 401"},
		{route: "DELETE /api/networks/{networkID}/port-forwards/{fwdID}", path: func() string {
			var pf struct{ ID string }
			h.owner.Want(201, "POST", h.url("/nodes/%s/port-forwards", box.NodeID), M{"remotePort": 8080}, &pf)
			return h.url("/port-forwards/%s", pf.ID)
		}, want: "204 204 204 404 401"},

		{route: "GET /api/device/verify/{code}", path: func() string { return "/api/device/verify/" + userCode() }, want: "200 200 200 200 401"},
		{route: "POST /api/device/authorize", path: at("/api/device/authorize"), body: func() any { return M{"userCode": userCode(), "networkId": h.net.ID} }, want: "200 200 200 404 401"},

		{route: "GET /api/networks/{networkID}/dns", path: at(h.url("/dns")), want: "200 200 200 404 401"},
		{route: "POST /api/networks/{networkID}/dns", path: at(h.url("/dns")), body: func() any { names++; return M{"name": fmt.Sprint("r", names), "nebulaIP": "10.42.1.9"} }, want: "201 201 403 404 401"},
		{route: "DELETE /api/networks/{networkID}/dns/{recordID}", path: func() string {
			names++
			var rec struct{ ID string }
			h.owner.Want(201, "POST", h.url("/dns"), M{"name": fmt.Sprint("r", names), "nebulaIP": "10.42.1.9"}, &rec)
			return h.url("/dns/%s", rec.ID)
		}, want: "204 204 403 404 401"},

		// Owner only: an invited admin gets 404, and the dashboard offers it.
		{route: "POST /api/networks/{networkID}/bundles", path: at(h.url("/bundles")), want: "201 404 404 404 401"},

		{route: "GET /api/networks/{networkID}/members", path: at(h.url("/members")), want: "200 200 200 404 401"},
		{route: "DELETE /api/networks/{networkID}/members/{memberID}", path: extraMember, want: "204 204 403 404 401", gap: "204 204 403 403 401"},
		{route: "GET /api/networks/{networkID}/invites", path: at(h.url("/invites")), want: "200 200 403 404 401", gap: "200 200 403 403 401"},
		{route: "POST /api/networks/{networkID}/invites", path: at(h.url("/invites")), body: M{}, want: "201 201 403 404 401", gap: "201 201 403 403 401"},
		{route: "DELETE /api/networks/{networkID}/invites/{inviteID}", path: func() string { id, _ := invite(); return h.url("/invites/%s", id) }, want: "204 204 403 404 401", gap: "204 204 403 403 401"},

		// Firewall rules. 428: no If-Match (TestE2E_Firewall sends it). A rule
		// set is its owner's alone; attaching is for the network's owner.
		{route: "GET /api/networks/{networkID}/firewall", path: at(h.url("/firewall")), want: "200 200 200 404 401"},
		{route: "GET /api/networks/{networkID}/nodes/{nodeID}/firewall", path: node("/firewall"), want: "200 200 200 404 401"},
		{route: "PUT /api/networks/{networkID}/firewall", path: at(h.url("/firewall")), body: M{"preset": "all", "rules": []any{}}, want: "428 428 403 404 401"},
		{route: "GET /api/firewall/sets", path: at("/api/firewall/sets"), want: "200 200 200 200 401"},
		{route: "POST /api/firewall/sets", path: at("/api/firewall/sets"), body: func() any { names++; return M{"name": fmt.Sprint("s", names)} }, want: "201 201 201 201 401"},
		{route: "PUT /api/firewall/sets/{setID}", path: func() string { return "/api/firewall/sets/" + ownerSet() }, body: M{"name": "x"}, want: "428 404 404 404 401"},
		{route: "DELETE /api/firewall/sets/{setID}", path: func() string { return "/api/firewall/sets/" + ownerSet() }, want: "204 404 404 404 401"},
		{route: "PUT /api/networks/{networkID}/firewall/sets/{setID}", path: func() string { return h.url("/firewall/sets/%s", ownerSet()) }, want: "204 403 403 404 401"},
		{route: "DELETE /api/networks/{networkID}/firewall/sets/{setID}", path: func() string { return h.url("/firewall/sets/%s", ownerSet()) }, want: "204 403 403 404 401"},

		// Last: the outsider gets in, the owner deletes it, everyone signs out.
		{route: "POST /api/invites/{code}/accept", path: func() string { _, code := invite(); return "/api/invites/" + code + "/accept" }, want: "409 409 409 201 401"},
		{route: "DELETE /api/networks/{networkID}", path: func() string { return "/api/networks/" + h.owner.CreateNetwork("other").ID }, want: "204 404 404 404 401"},
		{route: "POST /api/auth/logout", path: at("/api/auth/logout"), want: "200 200 200 200 401"},
	} {
		covered[row.route] = true
		var got []string
		for _, c := range []*e2etest.Client{h.owner, h.admin, h.member, h.outsider, h.anon} {
			body := row.body
			if fn, ok := body.(func() any); ok {
				body = fn()
			}
			method, _, _ := strings.Cut(row.route, " ")
			if method == "*" {
				method = "GET"
			}
			status, _ := c.Do(method, row.path(), body)
			got = append(got, fmt.Sprint(status))
		}
		switch g := strings.Join(got, " "); {
		case row.gap != "" && g == row.want:
			t.Errorf("%s: FIXED, remove its gap", row.route)
		case row.gap != "" && g == row.gap:
			t.Logf("KNOWN GAP: %s: %s, should be %s (owner, admin, member, outsider, anonymous)", row.route, g, row.want)
		case g != row.want:
			t.Errorf("%s: %s, want %s (owner, admin, member, outsider, anonymous)", row.route, g, row.want)
		}
	}

	// Nothing on a network is reachable after signing out.
	if status, _ := h.owner.Do("GET", h.url(""), nil); status != 401 {
		t.Errorf("after logout: HTTP %d, want 401", status)
	}

	for route := range routerRoutes(t) {
		if covered[route] == (publicRoutes[route] != "") {
			t.Errorf("%s: list it in exactly one of publicRoutes and TestE2E_Access", route)
		}
	}
}

// routerRoutes is every route the server has, as "METHOD /path".
func routerRoutes(t *testing.T) map[string]bool {
	t.Helper()
	r := api.NewRouter(nil, nil, &api.AuthHandler{}, nil, nil, nil, nil, nil, nil, &api.RenewHandler{},
		nil, nil, nil, nil, nil, nil, nil, &api.ClipboardHandler{}, nil)
	routes := map[string]bool{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasSuffix(route, "/proxy/{port}/*") {
			method = "*" // every method
		}
		routes[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) < 55 {
		t.Fatalf("only %d routes found", len(routes))
	}
	return routes
}

func memberID(c *e2etest.Client, networkID, userID string) string {
	c.T.Helper()
	var list []struct{ ID, UserID string }
	c.Want(200, "GET", "/api/networks/"+networkID+"/members", nil, &list)
	for _, m := range list {
		if m.UserID == userID {
			return m.ID
		}
	}
	c.T.Fatalf("user %s is not a member", userID)
	return ""
}

// fakeAgent is a node with a real tunnel and, in place of hop-agent, a server
// that records what the control plane sends.
type fakeAgent struct {
	t    *testing.T
	h    household
	node e2etest.Enrollment

	open atomic.Int64 // connections
	mu   sync.Mutex
	last http.Header // of the last proxied request
	html string      // what a proxied page answers
}

func newFakeAgent(t *testing.T, h household, node e2etest.Enrollment) *fakeAgent {
	t.Helper()
	a := &fakeAgent{t: t, h: h, node: node}
	l := e2etest.StartPeer(t, node).Listen(t, 41820)
	srv := &http.Server{Handler: a, ConnState: func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			a.open.Add(1)
		case http.StateClosed, http.StateHijacked:
			a.open.Add(-1)
		}
	}}
	t.Cleanup(func() { srv.Close() })
	go srv.Serve(l)
	return a
}

func (a *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+a.node.AgentToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/health":
		fmt.Fprint(w, `{"status":"ok"}`)
	case r.URL.Path == "/shell" || r.URL.Path == "/proxy/8080/ws":
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		a.record(r)
		for {
			kind, msg, err := conn.ReadMessage()
			if err != nil || conn.WriteMessage(kind, msg) != nil {
				return
			}
		}
	case strings.HasPrefix(r.URL.Path, "/proxy/"):
		a.record(r)
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.Header().Add("Set-Cookie", "session=of-the-node; Path=/")
		w.Header().Add("Set-Cookie", "grafana=2; Path=/; HttpOnly")
		fmt.Fprint(w, a.html)
	default:
		io.Copy(w, r.Body)
	}
}

func (a *fakeAgent) record(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = r.Header.Clone()
}

func (a *fakeAgent) lastHeader() http.Header {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// waitReady waits for the tunnel between the server and the agent.
func (a *fakeAgent) waitReady() {
	a.t.Helper()
	e2etest.Within(a.t, 40*time.Second, "the server to reach the agent", func() bool {
		status, _ := a.h.owner.Do("GET", a.h.url("/nodes/%s/health", a.node.NodeID), nil)
		return status == 200
	})
}

// wsDial opens a WebSocket as c, from a page on the given origin.
func wsDial(c *e2etest.Client, path, origin string) (*websocket.Conn, int, error) {
	head := http.Header{}
	c.Headers(head)
	if origin != "" {
		head.Set("Origin", origin)
	}
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(c.URL, "http")+path, head)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	return conn, status, err
}

// TestE2E_IDsOfAnotherNetwork: a plain member of home owns a network of their
// own, and uses its URLs, where they are the owner, with IDs of home.
func TestE2E_IDsOfAnotherNetwork(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	theirs := h.member.CreateNetwork("theirs")
	var rec, inv, pf struct{ ID string }
	h.owner.Want(201, "POST", h.url("/dns"), M{"name": "nas", "nebulaIP": "10.42.1.9"}, &rec)
	h.owner.Want(201, "POST", h.url("/invites"), M{}, &inv)
	h.owner.Want(201, "POST", h.url("/nodes/%s/port-forwards", box.NodeID), M{"remotePort": 8080}, &pf)

	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/nodes/" + box.NodeID + "/health", nil},
		{"GET", "/nodes/" + box.NodeID + "/shell", nil},
		{"POST", "/nodes/" + box.NodeID + "/exec", M{"command": "true"}},
		{"GET", "/nodes/" + box.NodeID + "/proxy/8080/", nil},
		{"POST", "/nodes/" + box.NodeID + "/port-forwards", M{"remotePort": 8080}},
		{"PATCH", "/nodes/" + box.NodeID, M{"name": "stolen"}},
		{"PUT", "/nodes/" + box.NodeID + "/capabilities", M{"capabilities": []string{}}},
		{"PUT", "/nodes/" + box.NodeID + "/routes", M{"routes": []string{"192.168.1.0/24"}}},
		{"DELETE", "/nodes/" + box.NodeID, nil},
		{"DELETE", "/port-forwards/" + pf.ID, nil},
		{"DELETE", "/dns/" + rec.ID, nil},
		{"DELETE", "/invites/" + inv.ID, nil},
		{"DELETE", "/members/" + memberID(h.member, h.net.ID, h.admin.ID), nil},
		{"DELETE", "/members/" + memberID(h.member, h.net.ID, h.owner.ID), nil},
	} {
		status, body := h.member.Do(r.method, "/api/networks/"+theirs.ID+r.path, r.body)
		nothingToDelete := r.method == "DELETE" && status == 204 // what counts is below
		if status != 404 && !nothingToDelete {
			t.Errorf("%s %s of home through their own network: HTTP %d %s, want 404", r.method, r.path, status, body)
		}
	}

	// Nothing of home changed.
	if n, ok := h.owner.Nodes(h.net.ID)[box.NodeID]; !ok || n.Hostname != "box" || len(n.Capabilities) != 3 {
		t.Errorf("box: %+v", n)
	}
	agent.waitReady()
	for _, r := range []struct{ path, id string }{
		{"/dns", rec.ID}, {"/invites", inv.ID}, {"/port-forwards", pf.ID}, {"/members", h.admin.ID}, {"/members", h.owner.ID},
	} {
		if _, body := h.owner.Do("GET", h.url("%s", r.path), nil); !strings.Contains(string(body), r.id) {
			t.Errorf("%s: %s is gone: %s", r.path, r.id, body)
		}
	}
	h.admin.Want(200, "GET", h.url(""), nil, nil)
}

// TestE2E_Enrollment: every way to get a node certificate, and every way it
// must fail.
func TestE2E_Enrollment(t *testing.T) {
	h := newHousehold(t)
	enroll := func(token string) (int, e2etest.Enrollment) {
		var e e2etest.Enrollment
		status, body := h.s.Anonymous().Do("POST", "/api/enroll", M{"token": token, "hostname": "box", "os": "linux", "arch": "arm64"})
		if status == 200 {
			if err := json.Unmarshal(body, &e); err != nil {
				t.Fatal(err)
			}
		}
		return status, e
	}

	t.Run("token", func(t *testing.T) {
		nodeID, token := h.owner.EnrollToken(h.net.ID)
		if n := h.s.Exec("UPDATE nodes SET os = os WHERE id = ? AND (enrollment_token = ? OR agent_token = ?)", nodeID, token, token); n != 0 {
			t.Error("the enrollment token is stored in plaintext")
		}
		statuses := make([]int, 12)
		certs := make([]e2etest.Enrollment, len(statuses))
		var wg sync.WaitGroup
		for i := range statuses {
			wg.Add(1)
			go func() { defer wg.Done(); statuses[i], certs[i] = enroll(token) }()
		}
		wg.Wait()
		won := 0
		for i, s := range statuses {
			switch s {
			case 200:
				won++
				if certs[i].NodeID != nodeID {
					t.Errorf("enrolled as %s, want %s", certs[i].NodeID, nodeID)
				}
				checkCert(t, certs[i].CACert, certs[i].NodeCert, certs[i].NodeKey, certs[i].NebulaIP)
			case 401, 409:
			default:
				t.Errorf("HTTP %d", s)
			}
		}
		if won != 1 {
			t.Errorf("%d of %d parallel enrollments with one token succeeded, want 1: %v", won, len(statuses), statuses)
		}
		if s, _ := enroll(token); s != 401 && s != 409 {
			t.Errorf("used token: HTTP %d", s)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		nodeID, token := h.owner.EnrollToken(h.net.ID)
		h.s.Exec("UPDATE nodes SET enrollment_expires_at = ? WHERE id = ?", time.Now().Add(-time.Second).Unix(), nodeID)
		if s, _ := enroll(token); s != 401 {
			t.Errorf("HTTP %d, want 401", s)
		}
	})

	t.Run("lifetime", func(t *testing.T) {
		nodeID, _ := h.owner.EnrollToken(h.net.ID)
		late, early := time.Now().Add(11*time.Minute).Unix(), time.Now().Add(8*time.Minute).Unix()
		if n := h.s.Exec("UPDATE nodes SET os = os WHERE id = ? AND enrollment_expires_at BETWEEN ? AND ?", nodeID, early, late); n != 1 {
			t.Error("the token is not valid for about 10 minutes")
		}
	})

	t.Run("bad requests", func(t *testing.T) {
		if s, _ := enroll("no-such-token"); s != 401 {
			t.Errorf("unknown token: HTTP %d, want 401", s)
		}
		if s, _ := enroll(""); s != 400 {
			t.Errorf("no token: HTTP %d, want 400", s)
		}
		if s, _ := h.s.Anonymous().Do("POST", "/api/enroll", `{"token":`); s != 400 {
			t.Errorf("broken JSON: HTTP %d, want 400", s)
		}
		// Just over the limit: the server hangs up on what it did not read, and
		// a client still sending then gets no answer.
		if s, _ := h.s.Anonymous().Do("POST", "/api/enroll", M{"token": strings.Repeat("x", 1<<20)}); s != 400 {
			t.Errorf("body over 1 MB: HTTP %d, want 400", s)
		}
	})

	t.Run("join", func(t *testing.T) {
		var e e2etest.Enrollment
		h.member.Want(200, "POST", h.url("/join"), M{"hostname": "laptop", "os": "darwin", "arch": "arm64"}, &e)
		checkCert(t, e.CACert, e.NodeCert, e.NodeKey, e.NebulaIP)
	})

	t.Run("device flow", func(t *testing.T) {
		type code struct{ DeviceCode, UserCode string }
		start := func() (c code) {
			h.s.Anonymous().Want(200, "POST", "/api/device/code", M{"hostname": "laptop", "os": "darwin", "arch": "arm64"}, &c)
			return c
		}
		poll := func(deviceCode string) (int, string) {
			s, body := h.s.Anonymous().Do("POST", "/api/device/poll", M{"deviceCode": deviceCode, "hostname": "laptop", "os": "darwin", "arch": "arm64"})
			return s, string(body)
		}
		c := start()
		if s, body := poll(c.DeviceCode); s != 403 || !strings.Contains(body, "authorization_pending") {
			t.Errorf("before authorization: HTTP %d %s", s, body)
		}
		// The code the user reads out must not be enough to get the certificate.
		if s, body := poll(c.UserCode); s == 200 || s == 403 {
			t.Errorf("poll with the user code: HTTP %d %s", s, body)
		}
		h.outsider.Want(404, "POST", "/api/device/authorize", M{"userCode": c.UserCode, "networkId": h.net.ID}, nil)
		if s, _ := poll(c.DeviceCode); s != 403 {
			t.Errorf("after an outsider tried to authorize: HTTP %d, want 403", s)
		}
		h.member.Want(200, "POST", "/api/device/authorize", M{"userCode": c.UserCode, "networkId": h.net.ID}, nil)

		var won atomic.Int64
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if s, body := poll(c.DeviceCode); s == 200 {
					won.Add(1)
					var e e2etest.Enrollment
					if err := json.Unmarshal([]byte(body), &e); err != nil {
						t.Error(err)
					}
					checkCert(t, e.CACert, e.NodeCert, e.NodeKey, e.NebulaIP)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Errorf("%d polls got the certificate, want 1", won.Load())
		}

		c = start()
		h.s.Exec("UPDATE device_codes SET expires_at = 1")
		h.member.Want(400, "POST", "/api/device/authorize", M{"userCode": c.UserCode, "networkId": h.net.ID}, nil)
		if s, body := poll(c.DeviceCode); s != 403 || !strings.Contains(body, "expired") {
			t.Errorf("expired: HTTP %d %s", s, body)
		}
		if s, _ := poll("no-such-code"); s != 400 {
			t.Errorf("unknown code: HTTP %d, want 400", s)
		}
	})

	// One subnet, no address twice.
	ips := map[string]string{}
	for id, n := range h.owner.Nodes(h.net.ID) {
		if other, dup := ips[n.NebulaIP]; dup {
			t.Errorf("nodes %s and %s both have %s", id, other, n.NebulaIP)
		}
		ips[n.NebulaIP] = id
	}
}

// checkCert verifies a node certificate as another node would.
func checkCert(t *testing.T, caPEM, certPEM, keyPEM, nebulaIP string) cert.Certificate {
	t.Helper()
	ca, err := cert.NewCAPoolFromPEM([]byte(caPEM))
	if err != nil {
		t.Error(err)
		return nil
	}
	c, _, err := cert.UnmarshalCertificateFromPEM([]byte(certPEM))
	if err != nil {
		t.Error(err)
		return nil
	}
	if _, err := ca.VerifyCertificate(time.Now(), c); err != nil {
		t.Errorf("certificate: %v", err)
	}
	key, _, curve, err := cert.UnmarshalPrivateKeyFromPEM([]byte(keyPEM))
	if err != nil || c.VerifyPrivateKey(curve, key) != nil {
		t.Errorf("certificate and key don't belong together (%v)", err)
	}
	if got := c.Networks()[0].String(); got != nebulaIP {
		t.Errorf("certificate is for %s, want %s", got, nebulaIP)
	}
	if g := c.Groups(); len(g) != 1 || g[0] != "node" {
		t.Errorf("groups %v, want only node", g)
	}
	if d := time.Until(c.NotAfter()); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("valid for %s, want 24h", d)
	}
	return c
}

// TestE2E_RenewAndHeartbeat is the contract agents in the field depend on.
func TestE2E_RenewAndHeartbeat(t *testing.T) {
	h := newHousehold(t)
	box, nas := h.owner.Enroll(h.net.ID, "box"), h.owner.Enroll(h.net.ID, "nas")
	far := h.outsider.CreateNetwork("far")
	farBox := h.outsider.Enroll(far.ID, "farbox")
	as := func(e e2etest.Enrollment) *e2etest.Client { c := h.s.Anonymous(); c.Token = e.AgentToken; return c }

	for _, path := range []string{"/api/renew", "/api/heartbeat"} {
		for what, c := range map[string]struct {
			token string
			body  any
			want  int
		}{
			"no token":                {"", M{"nodeId": box.NodeID}, 401},
			"wrong token":             {"wrong", M{"nodeId": box.NodeID}, 401},
			"another node's token":    {nas.AgentToken, M{"nodeId": box.NodeID}, 401},
			"another network's token": {farBox.AgentToken, M{"nodeId": box.NodeID}, 401},
			"a user's session":        {h.owner.Token, M{"nodeId": box.NodeID}, 401},
			"unknown node":            {box.AgentToken, M{"nodeId": "no-such-node"}, 401},
			"no node":                 {box.AgentToken, M{}, 400},
			"broken JSON":             {box.AgentToken, `{"nodeId":`, 400},
		} {
			a := h.s.Anonymous()
			a.Token = c.token
			if s, body := a.Do("POST", path, c.body); s != c.want {
				t.Errorf("%s with %s: HTTP %d %s, want %d", path, what, s, body, c.want)
			}
		}
	}

	var renewed struct {
		NodeCert, NodeKey string
		ExpiresIn         int
	}
	_, raw := as(box).Do("POST", "/api/renew", M{"nodeId": box.NodeID})
	if err := json.Unmarshal(raw, &renewed); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	c := checkCert(t, box.CACert, renewed.NodeCert, renewed.NodeKey, box.NebulaIP)
	if c != nil && (time.Until(c.NotAfter())-time.Duration(renewed.ExpiresIn)*time.Second).Abs() > time.Minute {
		t.Errorf("expiresIn %d, certificate ends %s", renewed.ExpiresIn, c.NotAfter())
	}
	if renewed.NodeKey == box.NodeKey {
		t.Error("the renewed certificate reuses the key")
	}
	// Agents apply what the server pushes: these two broke networks before.
	for _, never := range []string{"listenPort", "mtu"} {
		if strings.Contains(string(raw), never) {
			t.Errorf("renew pushes %s: %s", never, raw)
		}
	}
	// The token survives: a machine that was off for months has only that.
	as(box).Want(200, "POST", "/api/renew", M{"nodeId": box.NodeID}, nil)

	beat := func(e e2etest.Enrollment, endpoint string) (out struct {
		Peers         []string
		PeerEndpoints map[string][]string
		NetworkID     string
	}) {
		as(e).Want(200, "POST", "/api/heartbeat", M{"nodeId": e.NodeID, "peersDirect": 1, "peersRelayed": 0, "agentVersion": "v9.9.9",
			"peers":         []M{{"vpnAddr": "10.42.1.3", "direct": true, "remoteAddr": "203.0.113.5:4242"}},
			"selfEndpoints": []string{endpoint}, "selfEndpointLifetimesSec": []int{3600}}, &out)
		return out
	}
	seen := func(after int64) func() bool { // heartbeats reach the database within 5s
		return func() bool {
			n := h.member.Nodes(h.net.ID)[box.NodeID]
			return n.Status == "online" && n.LastSeenAt != nil && *n.LastSeenAt > after
		}
	}
	beat(box, "203.0.113.7:4242")
	e2etest.Within(t, 20*time.Second, "the heartbeat to reach the database", seen(time.Now().Add(-time.Minute).Unix()))
	if n := h.member.Nodes(h.net.ID)[box.NodeID]; n.PeersDirect == nil || *n.PeersDirect != 1 {
		t.Errorf("after a heartbeat: %+v", n)
	}
	out := beat(nas, "203.0.113.8:4243")
	if out.NetworkID != h.net.ID {
		t.Errorf("networkId %q, want %q", out.NetworkID, h.net.ID)
	}
	boxIP := strings.TrimSuffix(box.NebulaIP, "/24")
	if got := strings.Join(out.PeerEndpoints[boxIP], " "); !strings.Contains(got, "203.0.113.7:4242") {
		t.Errorf("nas is not told where box is: %+v", out)
	}
	if got, _ := json.Marshal(beat(farBox, "203.0.113.9:4242")); strings.Contains(string(got), "203.0.113.7") || strings.Contains(string(got), h.net.ID) {
		t.Errorf("another network is told about home: %s", got)
	}
	// A node seen too long ago is shown offline, until its next heartbeat.
	hourAgo := time.Now().Add(-time.Hour).Unix()
	h.s.Exec("UPDATE nodes SET last_seen_at = ? WHERE id = ?", hourAgo, box.NodeID)
	if n := h.owner.Nodes(h.net.ID)[box.NodeID]; n.Status == "online" {
		t.Errorf("last seen an hour ago: %+v", n)
	}
	beat(box, "203.0.113.7:4242")
	e2etest.Within(t, 20*time.Second, "box to be online again", seen(hourAgo))

	// 401 tells an agent it was deleted. A server that can't read its database
	// must not say that.
	h.s.Exec("ALTER TABLE nodes RENAME TO nodes_unreadable")
	for _, path := range []string{"/api/renew", "/api/heartbeat"} {
		if s, body := as(box).Do("POST", path, M{"nodeId": box.NodeID}); s != 500 {
			t.Errorf("%s without a database: HTTP %d %s, want 500", path, s, body)
		}
	}
	h.s.Exec("ALTER TABLE nodes_unreadable RENAME TO nodes")
	as(box).Want(200, "POST", "/api/renew", M{"nodeId": box.NodeID}, nil)

	h.owner.Want(204, "DELETE", h.url("/nodes/%s", box.NodeID), nil, nil)
	for _, path := range []string{"/api/renew", "/api/heartbeat"} {
		if s, _ := as(box).Do("POST", path, M{"nodeId": box.NodeID}); s != 401 {
			t.Errorf("%s of a deleted node: HTTP %d, want 401", path, s)
		}
	}
	if out := beat(nas, "203.0.113.8:4243"); out.PeerEndpoints[boxIP] != nil {
		t.Errorf("nas is still told about the deleted box: %+v", out)
	}

	// Another node's data in the bodies of requests that need an agent token.
	for _, path := range []string{"/api/clipboard/poll?nodeId=" + nas.NodeID, "/api/clipboard/x?nodeId=" + nas.NodeID} {
		for _, token := range []string{"", "wrong", farBox.AgentToken} {
			c := h.s.Anonymous()
			c.Token = token
			if s, body := c.Do("GET", path, nil); s != 401 && s != 400 && s != 403 {
				t.Errorf("GET %s with token %q: HTTP %d %s", path, token, s, body)
			}
		}
	}
}

// TestE2E_HeartbeatCorrectsPlatform: only enrollment ever wrote a node's OS,
// and enrollment guesses — scripts/install.sh posts "linux" whatever machine
// it runs on. A Windows node enrolled that way is listed with the Linux icon
// forever. The heartbeat carries the agent's own runtime, and that is where
// the row learns the truth. A beat from an agent that has no opinion
// (v0.11.30 and older, which omit the fields) must not erase it again.
func TestE2E_HeartbeatCorrectsPlatform(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box") // the harness enrolls as linux/arm64
	agent := h.s.Anonymous()
	agent.Token = box.AgentToken
	beat := func(body M) { agent.Want(200, "POST", "/api/heartbeat", body, nil) }

	beat(M{"nodeId": box.NodeID, "os": "windows", "arch": "amd64"})
	e2etest.Within(t, 20*time.Second, "the heartbeat to correct the machine", func() bool {
		n := h.member.Nodes(h.net.ID)[box.NodeID]
		return n.OS == "windows" && n.Arch == "amd64"
	})

	beat(M{"nodeId": box.NodeID})
	time.Sleep(2 * time.Second)
	if n := h.member.Nodes(h.net.ID)[box.NodeID]; n.OS != "windows" || n.Arch != "amd64" {
		t.Errorf("a beat without os/arch undid it: os %q, arch %q", n.OS, n.Arch)
	}
}

// TestE2E_RateLimits: many machines behind one address renew without being
// refused, and guessing passwords from that address doesn't take them down.
// (Up to v0.11.26 they shared the login limiter: 10 per minute.)
func TestE2E_RateLimits(t *testing.T) {
	h := newHousehold(t)
	house := h.s.Anonymous()
	var agents []e2etest.Enrollment
	for i := range 5 {
		agents = append(agents, h.owner.Enroll(h.net.ID, fmt.Sprint("node", i)))
	}
	// A node holds at most db.MaxHeldCerts certs (TestE2E_Blocklist), so
	// each renews in the first rounds only: 14 times in the two runs.
	all := func(when string) {
		t.Helper()
		for round := range 20 {
			for _, a := range agents {
				for _, path := range []string{"/api/renew", "/api/heartbeat"} {
					if path == "/api/renew" && round >= (db.MaxHeldCerts-2)/2 {
						continue
					}
					if s, body := house.As(a.AgentToken).Do("POST", path, M{"nodeId": a.NodeID}); s != 200 {
						t.Fatalf("%s, round %d: %s: HTTP %d %s", when, round, path, s, body)
					}
				}
			}
		}
	}
	all("at first")
	tries := 0
	for ; tries < 60; tries++ {
		s, _ := house.Do("POST", "/api/auth/login", M{"email": h.owner.Email, "password": "guess"})
		if s == 429 {
			break
		}
		if s != 401 {
			t.Fatalf("wrong password: HTTP %d, want 401", s)
		}
	}
	if tries < 10 || tries > 40 {
		t.Errorf("password guessing was stopped after %d tries, want about 20", tries)
	}
	all("while the login limit is used up")
	if s, _ := h.s.Anonymous().Do("POST", "/api/auth/login", M{"email": h.owner.Email, "password": e2etest.Password}); s != 200 {
		t.Errorf("the owner can't log in from elsewhere: HTTP %d", s)
	}
	// Another address used up the same way must not help: the limit is per address.
	if s, _ := house.Do("POST", "/api/auth/login", M{"email": h.owner.Email, "password": e2etest.Password}); s != 429 {
		t.Errorf("after a burst: HTTP %d, want 429", s)
	}
	for _, c := range []struct {
		method, path string
		most         int
	}{
		{"GET", "/api/auth/status", 40},
		{"POST", "/api/auth/register", 40},
		{"POST", "/api/auth/login", 40},
		{"GET", "/api/auth/oauth/status", 40},
		{"GET", "/api/auth/github/start", 40},
		{"GET", "/api/auth/github/callback", 40},
		{"POST", "/api/enroll", 40},
		{"POST", "/api/device/code", 40},
		{"GET", "/api/invites/guess", 40},
		{"POST", "/api/device/poll", 200},
		{"POST", "/api/clipboard/announce", 200},
		{"GET", "/api/clipboard/poll", 200},
		{"GET", "/api/clipboard/guess", 200},
		{"POST", "/api/renew", 2000},
		{"POST", "/api/heartbeat", 2000},
	} {
		who, n := h.s.Anonymous(), 0
		for ; n <= c.most; n++ {
			if s, _ := who.Do(c.method, c.path, M{}); s == 429 {
				break
			}
		}
		if n < 10 || n > c.most {
			t.Errorf("%s %s: one address got %d tries, want 10 to %d", c.method, c.path, n, c.most)
		}
	}
}

// TestE2E_Accounts: registration, login, sessions and their cookie.
func TestE2E_Accounts(t *testing.T) {
	h := newHousehold(t)
	for what, c := range map[string]struct {
		path string
		body any
		want int
	}{
		"taken address":  {"register", M{"email": h.owner.Email, "name": "x", "password": e2etest.Password}, 409},
		"short password": {"register", M{"email": "a@example.test", "name": "x", "password": "short"}, 400},
		"long password":  {"register", M{"email": "a@example.test", "name": "x", "password": strings.Repeat("x", 100)}, 400},
		"no address":     {"register", M{"name": "x", "password": e2etest.Password}, 400},
		"wrong password": {"login", M{"email": h.owner.Email, "password": "wrong password"}, 401},
		"unknown user":   {"login", M{"email": "nobody@example.test", "password": e2etest.Password}, 401},
	} {
		if s, body := h.s.Anonymous().Do("POST", "/api/auth/"+c.path, c.body); s != c.want {
			t.Errorf("%s: HTTP %d %s, want %d", what, s, body, c.want)
		}
	}

	cookie := login(t, h.owner, "")
	if !cookie.HttpOnly || cookie.SameSite == http.SameSiteNoneMode || cookie.Secure {
		t.Errorf("cookie over http: %+v", cookie)
	}
	if c := login(t, h.owner, "https"); !c.Secure || !c.HttpOnly {
		t.Errorf("cookie over https: %+v", c)
	}
	withCookie := func() int {
		req, _ := http.NewRequest("GET", h.s.URL+h.url(""), nil)
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := withCookie(); s != 200 {
		t.Errorf("with the cookie: HTTP %d", s)
	}
	for _, token := range []string{cookie.Value, h.owner.Token} {
		if n := h.s.Exec("UPDATE sessions SET expires_at = expires_at WHERE token = ?", token); n != 0 {
			t.Error("the session token is stored in plaintext")
		}
	}
	h.owner.As(cookie.Value).Want(200, "POST", "/api/auth/logout", nil, nil)
	if s := withCookie(); s != 401 {
		t.Errorf("after logout: HTTP %d, want 401", s)
	}
	h.owner.Want(200, "GET", h.url(""), nil, nil) // the other session lives on
	h.s.Exec("UPDATE sessions SET expires_at = 1")
	h.owner.Want(401, "GET", h.url(""), nil, nil)
}

// login signs c in again and returns the session cookie.
func login(t *testing.T, c *e2etest.Client, proto string) *http.Cookie {
	t.Helper()
	req, _ := http.NewRequest("POST", c.URL+"/api/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, c.Email, e2etest.Password)))
	req.Header.Set("X-Forwarded-For", c.IP)
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == "session" {
			return ck
		}
	}
	t.Fatalf("login: HTTP %d without a session cookie", resp.StatusCode)
	return nil
}

// TestE2E_Invites: who gets in with a code, how often, and for how long.
func TestE2E_Invites(t *testing.T) {
	h := newHousehold(t)
	type invite struct{ ID, Code, Role string }
	create := func(body M) (i invite) {
		h.owner.Want(201, "POST", h.url("/invites"), body, &i)
		return i
	}
	accept := func(c *e2etest.Client, code string) int {
		s, _ := c.Do("POST", "/api/invites/"+code+"/accept", nil)
		return s
	}
	users := 0
	user := func() *e2etest.Client { users++; return h.s.Register(fmt.Sprint("guest", users)) }
	show := func(code string) int {
		s, _ := h.s.Anonymous().Do("GET", "/api/invites/"+code, nil)
		return s
	}

	once := create(M{"maxUses": 1})
	guests := []*e2etest.Client{user(), user(), user(), user(), user()}
	var won atomic.Int64
	var wg sync.WaitGroup
	for _, g := range guests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if accept(g, once.Code) == 201 {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("%d users accepted a single-use invite, want 1", won.Load())
	}
	if s := show(once.Code); s != 410 {
		t.Errorf("used-up invite: HTTP %d, want 410", s)
	}

	short := create(M{"expiresIn": 3600})
	if s := show(short.Code); s != 200 {
		t.Errorf("valid invite: HTTP %d, want 200", s)
	}
	h.s.Exec("UPDATE network_invites SET expires_at = 1 WHERE id = ?", short.ID)
	if s := show(short.Code); s != 410 {
		t.Errorf("expired invite: HTTP %d, want 410", s)
	}
	if s := accept(user(), short.Code); s == 201 {
		t.Error("an expired invite was accepted")
	}

	revoked := create(M{})
	h.owner.Want(204, "DELETE", h.url("/invites/%s", revoked.ID), nil, nil)
	if s := show(revoked.Code); s != 404 {
		t.Errorf("revoked invite: HTTP %d, want 404", s)
	}
	if s := accept(user(), revoked.Code); s == 201 {
		t.Error("a revoked invite was accepted")
	}

	for _, body := range []M{{}, {"role": ""}, {"role": "owner"}, {"role": "root"}} {
		if i := create(body); i.Role != "member" {
			t.Errorf("an invite for %v makes a %q", body, i.Role)
		}
	}

	e2etest.KnownGap(t, "a member who opens an invite uses it up for the person it was meant for", func(g *e2etest.Gap) {
		i := create(M{"maxUses": 1})
		if s := accept(h.member, i.Code); s != 409 {
			g.Errorf("member: HTTP %d, want 409", s)
		}
		if s := accept(user(), i.Code); s != 201 {
			g.Errorf("the invited person: HTTP %d, want 201", s)
		}
	})

	// Removing someone ends their access at once, also what the proxy cached.
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	page := h.url("/nodes/%s/proxy/8080/", box.NodeID)
	h.member.Want(200, "GET", page, nil, nil)
	h.owner.Want(204, "DELETE", h.url("/members/%s", memberID(h.owner, h.net.ID, h.member.ID)), nil, nil)
	for _, path := range []string{h.url(""), h.url("/nodes"), page} {
		if s, _ := h.member.Do("GET", path, nil); s != 404 {
			t.Errorf("GET %s after removal: HTTP %d, want 404", path, s)
		}
	}
}

func resolve(t *testing.T, n e2etest.Network, name string) (ips []string) {
	t.Helper()
	m := new(dns.Msg).SetQuestion(name+".", dns.TypeA)
	in, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, fmt.Sprintf("127.0.0.1:%d", 15300+n.LighthousePort-42001))
	if err != nil {
		t.Errorf("DNS %s: %v", name, err)
		return nil
	}
	for _, rr := range in.Answer {
		if a, ok := rr.(*dns.A); ok {
			ips = append(ips, a.A.String())
		}
	}
	return ips
}

// TestE2E_DNS changes names through the API and asks the real DNS server.
func TestE2E_DNS(t *testing.T) {
	h := newHousehold(t)
	want := func(name, ips string) {
		t.Helper()
		if got := strings.Join(resolve(t, h.net, name), " "); got != ips {
			t.Errorf("%s = %q, want %q", name, got, ips)
		}
	}
	box := h.owner.Enroll(h.net.ID, "box")
	boxIP := strings.TrimSuffix(box.NebulaIP, "/24")
	want("box.hop", boxIP)
	want("nobox.hop", "")
	want("box.example.com", "")

	var rec struct{ ID string }
	h.owner.Want(201, "POST", h.url("/dns"), M{"name": "nas", "nebulaIP": "10.42.1.9"}, &rec)
	want("nas.hop", "10.42.1.9")
	for what, c := range map[string]struct {
		body M
		want int
	}{
		"taken name":     {M{"name": "nas", "nebulaIP": "10.42.1.8"}, 409},
		"no name":        {M{"nebulaIP": "10.42.1.9"}, 400},
		"not an address": {M{"name": "x", "nebulaIP": "nas"}, 400},
	} {
		if s, b := h.owner.Do("POST", h.url("/dns"), c.body); s != c.want {
			t.Errorf("%s: HTTP %d %s, want %d", what, s, b, c.want)
		}
	}
	h.owner.Want(204, "DELETE", h.url("/dns/%s", rec.ID), nil, nil)
	want("nas.hop", "")

	h.owner.Want(200, "PATCH", h.url("/nodes/%s", box.NodeID), M{"name": "Media Box"}, nil)
	want("media-box.hop", boxIP)

	// Another network's names are its own, on its own port.
	far := h.outsider.CreateNetwork("far")
	h.outsider.Enroll(far.ID, "farbox")
	if ips := resolve(t, far, "farbox.hop"); len(ips) != 1 {
		t.Errorf("farbox.hop on far: %v", ips)
	}
	want("farbox.hop", "")

	// A deleted device's name stops resolving at once, not at the next
	// refresh of the network's names.
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", box.NodeID), nil, nil)
	if ips := resolve(t, h.net, "media-box.hop"); len(ips) != 0 {
		t.Errorf("a deleted node's name still resolves: media-box.hop = %v", ips)
	}
}

// TestE2E_Bundles: the tarball with a private key is given out once, over
// HTTPS, while its link is fresh.
func TestE2E_Bundles(t *testing.T) {
	h := newHousehold(t)
	create := func() (path, nodeID string) {
		var out struct{ BundleURL, NodeID string }
		h.owner.Want(201, "POST", h.url("/bundles"), nil, &out)
		return strings.TrimPrefix(out.BundleURL, h.s.URL), out.NodeID
	}
	get := func(path, proto string) (int, []byte) {
		req, _ := http.NewRequest("GET", h.s.URL+path, nil)
		req.Header.Set("X-Forwarded-Proto", proto)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	path, _ := create()
	if s, _ := get(path, "http"); s != 403 {
		t.Errorf("over http: HTTP %d, want 403", s)
	}
	var won atomic.Int64
	var tarball []byte
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s, b := get(path, "https"); s == 200 && won.Add(1) == 1 {
				tarball = b
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d downloads of one bundle, want 1", won.Load())
	}
	files := map[string]string{}
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		t.Fatal(err)
	}
	for tr := tar.NewReader(gz); ; {
		f, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[strings.TrimPrefix(f.Name, "etc/hop-agent/")] = string(b)
		if strings.HasSuffix(f.Name, "node.key") && f.Mode&0o077 != 0 {
			t.Errorf("node.key has mode %o", f.Mode)
		}
	}
	var cfg struct{ NodeID, AgentToken, NebulaIP string }
	if err := json.Unmarshal([]byte(files["config.json"]), &cfg); err != nil {
		t.Fatalf("config.json: %v in %s", err, files["config.json"])
	}
	checkCert(t, files["ca.crt"], files["node.crt"], files["node.key"], cfg.NebulaIP)
	c := h.s.Anonymous()
	c.Token = files["token"]
	c.Want(200, "POST", "/api/renew", M{"nodeId": cfg.NodeID}, nil)

	path, _ = create()
	h.s.Exec("UPDATE enrollment_bundles SET expires_at = 1")
	if s, _ := get(path, "https"); s != 404 {
		t.Errorf("expired: HTTP %d, want 404", s)
	}
	if s, _ := get("/api/bundles/no-such-token", "https"); s != 404 {
		t.Errorf("unknown: HTTP %d, want 404", s)
	}
}

// TestE2E_FirewallPreset: every answer that hands an agent its certificate
// names the network's firewall preset, which the agent turns into its
// firewall block: "all" for a network created now, "tcp" for one from before
// migration 009. The client join answer has none; `hop client join` writes
// its own block.
func TestE2E_FirewallPreset(t *testing.T) {
	h := newHousehold(t)
	answers := func() map[string]string {
		got := map[string]string{}
		var e struct{ Firewall string }
		_, token := h.owner.EnrollToken(h.net.ID)
		h.s.Anonymous().Want(200, "POST", "/api/enroll", M{"token": token, "hostname": "box", "os": "linux", "arch": "arm64"}, &e)
		got["token"] = e.Firewall

		var c struct{ DeviceCode, UserCode string }
		h.s.Anonymous().Want(200, "POST", "/api/device/code", M{"hostname": "laptop", "os": "darwin", "arch": "arm64"}, &c)
		h.owner.Want(200, "POST", "/api/device/authorize", M{"userCode": c.UserCode, "networkId": h.net.ID}, nil)
		e.Firewall = ""
		h.s.Anonymous().Want(200, "POST", "/api/device/poll", M{"deviceCode": c.DeviceCode, "hostname": "laptop", "os": "darwin", "arch": "arm64"}, &e)
		got["device flow"] = e.Firewall

		var out struct{ BundleURL string }
		h.owner.Want(201, "POST", h.url("/bundles"), nil, &out)
		req, _ := http.NewRequest("GET", out.BundleURL, nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		for tr := tar.NewReader(gz); ; {
			f, err := tr.Next()
			if err != nil {
				break
			}
			if strings.HasSuffix(f.Name, "config.json") {
				e.Firewall = ""
				if err := json.NewDecoder(tr).Decode(&e); err != nil {
					t.Fatal(err)
				}
				got["bundle"] = e.Firewall
			}
		}
		return got
	}

	for _, want := range []string{"all", "tcp"} {
		if want == "tcp" {
			h.s.Exec("UPDATE networks SET firewall_preset = 'tcp' WHERE id = ?", h.net.ID)
		}
		got := answers()
		for _, flow := range []string{"token", "device flow", "bundle"} {
			if got[flow] != want {
				t.Errorf("%s answer of a network with preset %q names %q", flow, want, got[flow])
			}
		}
	}
}

// TestE2E_Prune: the "clean up" button takes what it says, of this network
// only, and never a node that is online.
func TestE2E_Prune(t *testing.T) {
	h := newHousehold(t)
	far := h.outsider.CreateNetwork("far")
	seen := func(e e2etest.Enrollment, ago time.Duration) e2etest.Enrollment {
		c := h.s.Anonymous()
		c.Token = e.AgentToken
		c.Want(200, "POST", "/api/heartbeat", M{"nodeId": e.NodeID}, nil)
		if ago > 0 {
			at := time.Now().Add(-ago).Unix()
			e2etest.Within(t, 20*time.Second, "the heartbeat to reach the database", func() bool {
				return h.s.Exec("UPDATE nodes SET last_seen_at = ?, status = 'offline' WHERE id = ? AND last_seen_at > ?", at, e.NodeID, at) == 1
			})
		}
		return e
	}
	online := seen(h.owner.Enroll(h.net.ID, "online"), 0)
	hour := seen(h.owner.Enroll(h.net.ID, "hour"), time.Hour)
	month := seen(h.owner.Enroll(h.net.ID, "month"), 30*24*time.Hour)
	theirs := seen(h.outsider.Enroll(far.ID, "theirs"), 30*24*time.Hour)

	prune := func(body any) int {
		var out struct{ DeletedCount int }
		h.owner.Want(200, "POST", h.url("/nodes/prune"), body, &out)
		return out.DeletedCount
	}
	if n := prune(M{"olderThanSeconds": 7 * 24 * 3600}); n != 1 {
		t.Errorf("older than a week: %d deleted, want 1", n)
	}
	nodes := h.owner.Nodes(h.net.ID)
	for name, e := range map[string]e2etest.Enrollment{"online": online, "hour": hour} {
		if _, ok := nodes[e.NodeID]; !ok {
			t.Errorf("%s was deleted", name)
		}
	}
	if _, ok := nodes[month.NodeID]; ok {
		t.Error("month is still there")
	}
	if n := prune(nil); n != 1 {
		t.Errorf("all offline: %d deleted, want 1", n)
	}
	if _, ok := h.owner.Nodes(h.net.ID)[online.NodeID]; !ok {
		t.Error("the online node was deleted")
	}
	if _, ok := h.outsider.Nodes(far.ID)[theirs.NodeID]; !ok {
		t.Error("another network's node was deleted")
	}
}

// TestE2E_AddressNotReused: a deleted or pruned node's certificate is valid
// for up to 24 h more, so its mesh address must not go to the next node.
func TestE2E_AddressNotReused(t *testing.T) {
	h := newHousehold(t)
	h.owner.Enroll(h.net.ID, "first")
	deleted := h.owner.Enroll(h.net.ID, "deleted")
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", deleted.NodeID), nil, nil)
	if next := h.owner.Enroll(h.net.ID, "next"); next.NebulaIP == deleted.NebulaIP {
		t.Errorf("after a delete the next node got %s, the deleted node's address", next.NebulaIP)
	}

	pruned := h.owner.Enroll(h.net.ID, "pruned")
	monthAgo := time.Now().Add(-30 * 24 * time.Hour).Unix()
	if n := h.s.Exec("UPDATE nodes SET last_seen_at = ?, status = 'offline' WHERE id = ?", monthAgo, pruned.NodeID); n != 1 {
		t.Fatalf("%d rows updated", n)
	}
	var out struct{ DeletedCount int }
	h.owner.Want(200, "POST", h.url("/nodes/prune"), M{"olderThanSeconds": 7 * 24 * 3600}, &out)
	if out.DeletedCount != 1 {
		t.Fatalf("prune deleted %d, want 1", out.DeletedCount)
	}
	if next := h.owner.Enroll(h.net.ID, "after-prune"); next.NebulaIP == pruned.NebulaIP {
		t.Errorf("after a prune the next node got %s, the pruned node's address", next.NebulaIP)
	}
}

// TestE2E_NetworkFull: when a network has no address left, every way of
// adding a node says so with 409, not as a server error.
func TestE2E_NetworkFull(t *testing.T) {
	h := newHousehold(t)
	prefix := h.net.Subnet[:strings.LastIndex(h.net.Subnet, ".")]
	h.s.Exec(`INSERT INTO released_node_ips (network_id, nebula_ip, released_at)
		WITH RECURSIVE o(n) AS (SELECT 2 UNION ALL SELECT n + 1 FROM o WHERE n < 254)
		SELECT ?, printf('%s.%d/24', ?, n), unixepoch() FROM o`, h.net.ID, prefix)

	// The device flow takes the address when the device polls, after the
	// user has approved it.
	var dc struct{ DeviceCode, UserCode string }
	h.s.Anonymous().Want(200, "POST", "/api/device/code", nil, &dc)
	h.owner.Want(200, "POST", "/api/device/authorize", M{"userCode": dc.UserCode, "networkId": h.net.ID}, nil)
	for _, c := range []struct {
		who          *e2etest.Client
		method, path string
		body         any
	}{
		{h.owner, "POST", h.url("/nodes"), M{}},
		{h.owner, "POST", h.url("/join"), M{"hostname": "laptop"}},
		{h.owner, "POST", h.url("/bundles"), nil},
		{h.s.Anonymous(), "POST", "/api/device/poll", M{"deviceCode": dc.DeviceCode, "hostname": "laptop"}},
	} {
		s, body := c.who.Do(c.method, c.path, c.body)
		var e struct{ Error string }
		json.Unmarshal(body, &e)
		if s != 409 || !strings.Contains(e.Error, "no free mesh address") {
			t.Errorf("%s %s on a full network: HTTP %d %s, want 409 with a JSON error", c.method, c.path, s, strings.TrimSpace(string(body)))
		}
	}
}

// TestE2E_Capabilities: what an admin switches off on a node is off at once,
// for the owner too.
func TestE2E_Capabilities(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	node := func(p string) string { return h.url("/nodes/%s%s", box.NodeID, p) }
	set := func(caps ...string) {
		h.admin.Want(200, "PUT", node("/capabilities"), M{"capabilities": append([]string{}, caps...)}, nil)
	}
	use := map[string][]struct {
		method, path string
		body         any
	}{
		"terminal": {{"GET", node("/shell"), nil}, {"POST", node("/exec"), M{"command": "true"}}},
		"health":   {{"GET", node("/health"), nil}},
		"forward":  {{"GET", node("/proxy/8080/"), nil}, {"POST", node("/port-forwards"), M{"remotePort": 8080}}},
	}
	for _, on := range []string{"terminal", "health", "forward", ""} {
		// Through the cache, if there is one.
		h.owner.Do("GET", node("/proxy/8080/"), nil)
		if on == "" {
			set()
		} else {
			set(on)
		}
		for capability, uses := range use {
			for _, u := range uses {
				s, body := h.owner.Do(u.method, u.path, u.body)
				if (s == 403) != (capability != on) {
					t.Errorf("only %q on: %s %s: HTTP %d %s", on, u.method, u.path, s, body)
				}
			}
		}
	}
	h.admin.Want(400, "PUT", node("/capabilities"), M{"capabilities": []string{"root"}}, nil)

	for routes, want := range map[string]int{
		"192.168.1.0/24": 200, "10.0.5.9/16": 200, "0.0.0.0/0": 400, "127.0.0.0/8": 400, "169.254.1.0/24": 400, "224.0.0.0/4": 400, "nonsense": 400,
	} {
		if s, body := h.admin.Do("PUT", node("/routes"), M{"routes": []string{routes}}); s != want {
			t.Errorf("route %s: HTTP %d %s, want %d", routes, s, body, want)
		}
	}
}

// TestE2E_History: what happened is recorded, for those who may see it.
func TestE2E_History(t *testing.T) {
	h := newHousehold(t)
	live, _, err := wsDial(h.member, h.url("/events"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	for who, c := range map[string]*e2etest.Client{"outsider": h.outsider, "anonymous": h.anon} {
		if conn, status, err := wsDial(c, h.url("/events"), ""); err == nil {
			conn.Close()
			t.Errorf("%s follows the events", who)
		} else if status != 404 && status != 401 {
			t.Errorf("%s: HTTP %d", who, status)
		}
	}
	// A page on another site must not be able to use the user's cookie.
	if conn, status, err := wsDial(h.owner, h.url("/events"), "https://evil.example"); err == nil {
		conn.Close()
		t.Error("events: a page on another site was let in")
	} else if status != 403 {
		t.Errorf("events from another site: HTTP %d, want 403", status)
	}

	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	if conn, status, err := wsDial(h.owner, h.url("/nodes/%s/shell", box.NodeID), "https://evil.example"); err == nil {
		conn.Close()
		t.Error("shell: a page on another site was let in")
	} else if status != 403 {
		t.Errorf("shell from another site: HTTP %d, want 403", status)
	}
	shell, _, err := wsDial(h.admin, h.url("/nodes/%s/shell", box.NodeID), h.s.URL)
	if err != nil {
		t.Fatal(err)
	}
	shell.WriteMessage(websocket.BinaryMessage, []byte("ls"))
	if _, msg, err := shell.ReadMessage(); err != nil || string(msg) != "ls" {
		t.Errorf("shell: %q, %v", msg, err)
	}
	shell.Close()
	h.admin.Want(200, "POST", h.url("/nodes/%s/exec", box.NodeID), M{"command": "true"}, nil)
	h.admin.Want(201, "POST", h.url("/nodes/%s/port-forwards", box.NodeID), M{"remotePort": 8080}, nil)
	h.admin.Want(200, "GET", h.url("/nodes/%s/proxy/8080/x", box.NodeID), nil, nil)
	h.admin.Want(204, "DELETE", h.url("/nodes/%s", box.NodeID), nil, nil)

	e2etest.Within(t, 15*time.Second, "the audit log", func() bool {
		var log []struct {
			Action, UserID string
			NodeID         *string
		}
		h.owner.Want(200, "GET", h.url("/audit"), nil, &log)
		got := map[string]bool{}
		for _, e := range log {
			if e.UserID == h.admin.ID && e.NodeID != nil && *e.NodeID == box.NodeID {
				got[e.Action] = true
			}
		}
		return got["shell.connect"] && got["exec"] && got["port_forward.start"] && got["port_forward.proxy"] && got["node.delete"]
	})
	e2etest.Within(t, 15*time.Second, "the event history", func() bool {
		_, body := h.member.Do("GET", h.url("/events/history"), nil)
		return strings.Contains(string(body), "node.enrolled") && strings.Contains(string(body), "node.deleted")
	})
	seen := ""
	live.SetReadDeadline(time.Now().Add(10 * time.Second))
	for !strings.Contains(seen, "node.enrolled") || !strings.Contains(seen, "node.deleted") {
		_, msg, err := live.ReadMessage()
		if err != nil {
			t.Fatalf("live events: %v after %s", err, seen)
		}
		seen += string(msg)
	}
	// Removing a member, and the rest of what changes a network: TestE2E_Audit.
}

// TestE2E_DeleteNetwork: nothing of a deleted network is left, running or
// stored.
func TestE2E_DeleteNetwork(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	h.owner.Want(201, "POST", h.url("/dns"), M{"name": "nas", "nebulaIP": "10.42.1.9"}, nil)
	h.owner.Want(201, "POST", h.url("/invites"), M{}, nil)
	h.owner.Want(201, "POST", h.url("/bundles"), nil, nil)
	h.owner.Want(201, "POST", h.url("/nodes/%s/port-forwards", box.NodeID), M{"remotePort": 8080}, nil)
	h.owner.With("If-Match", `"0"`).Want(200, "PUT", h.url("/firewall"), M{"preset": "ping", "rules": []any{}}, nil)
	var set struct{ ID string }
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "web"}, &set)
	h.owner.Want(204, "PUT", h.url("/firewall/sets/%s", set.ID), nil, nil)
	a := h.s.Anonymous()
	a.Token = box.AgentToken
	a.Want(200, "POST", "/api/heartbeat", M{"nodeId": box.NodeID, "firewall": M{"fingerprint": "", "error": "refused"}}, nil)
	reports := func() int {
		pair, _ := h.s.DB()
		defer pair.Close()
		var n int
		if err := pair.ReadDB.QueryRow(`SELECT COUNT(*) FROM node_firewall WHERE node_id = ?`, box.NodeID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if reports() != 1 {
		t.Fatal("the box's firewall report was not stored")
	}
	e2etest.Within(t, 15*time.Second, "events in the database", func() bool { return count(h, "network_events") > 0 })

	h.owner.Want(204, "DELETE", h.url(""), nil, nil)

	for _, c := range []*e2etest.Client{h.owner, h.admin, h.member} {
		c.Want(404, "GET", h.url(""), nil, nil)
	}
	a.Want(401, "POST", "/api/renew", M{"nodeId": box.NodeID}, nil)
	if n := reports(); n != 0 {
		t.Errorf("%d firewall reports of the network's devices left", n)
	}
	time.Sleep(3 * time.Second) // buffered events of the deletion
	for _, table := range []string{"nodes", "dns_records", "network_members", "network_invites", "network_events", "network_firewall", "network_firewall_sets"} {
		if n := count(h, table); n != 0 {
			t.Errorf("%d rows of the network left in %s", n, table)
		}
	}
	// The owner's rule set stays, attached nowhere.
	h.owner.Want(204, "DELETE", "/api/firewall/sets/"+set.ID, nil, nil)
	if n := h.s.Exec("UPDATE enrollment_bundles SET expires_at = expires_at"); n != 0 {
		t.Errorf("%d bundles left", n)
	}
	p, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", h.net.LighthousePort))
	if err != nil {
		t.Errorf("the lighthouse still runs: %v", err)
	} else {
		p.Close()
	}
	// And the server goes on: a new network works.
	again := h.owner.CreateNetwork("again")
	h.owner.Enroll(again.ID, "box")
	if ips := resolve(t, again, "box.hop"); len(ips) != 1 {
		t.Errorf("box.hop on the new network: %v", ips)
	}
}

func count(h household, table string) int {
	pair, _ := h.s.DB()
	defer pair.Close()
	var n int
	if err := pair.ReadDB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE network_id = ?", h.net.ID).Scan(&n); err != nil {
		h.s.T().Fatal(err)
	}
	return n
}

// TestE2E_ManyAgents: agents renew and report while users work in the
// dashboard, and every request is answered. A renewal that meets a write must
// wait for it: an agent takes 401 for "you were deleted". A node holds at
// most db.MaxHeldCerts certs, its enrollment's among them; a renewal past
// that is answered 429, after the same write of the node it rolls back.
func TestE2E_ManyAgents(t *testing.T) {
	h := newHousehold(t)
	var agents []e2etest.Enrollment
	for i := range 20 {
		agents = append(agents, h.owner.Enroll(h.net.ID, fmt.Sprint("node", i)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	bad := map[string]int{}
	requests := 0
	run := func(c *e2etest.Client, want int, method, path string, body any) {
		s, b := c.Do(method, path, body)
		mu.Lock()
		defer mu.Unlock()
		requests++
		if s != want {
			bad[fmt.Sprintf("%s %s: HTTP %d %s", method, path, s, strings.TrimSpace(string(b)))]++
		}
	}
	var wg sync.WaitGroup
	loop := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				f(i)
			}
		}()
	}
	for _, a := range agents {
		c := h.s.Anonymous()
		c.Token = a.AgentToken
		renewed := 0
		loop(func(i int) {
			if i%4 == 0 {
				c.IP = h.s.Anonymous().IP
				want := 200
				if renewed++; renewed >= db.MaxHeldCerts {
					want = 429
				}
				run(c, want, "POST", "/api/renew", M{"nodeId": a.NodeID})
			} else {
				run(c, 200, "POST", "/api/heartbeat", M{"nodeId": a.NodeID, "peersDirect": i})
			}
		})
	}
	loop(func(int) { run(h.owner, 200, "GET", h.url("/nodes"), nil) })
	loop(func(int) { run(h.member, 200, "GET", h.url("/events/history"), nil) })
	loop(func(i int) {
		run(h.admin, 200, "PATCH", h.url("/nodes/%s", agents[0].NodeID), M{"name": fmt.Sprint("name", i)})
	})
	loop(func(int) { run(h.owner, 201, "POST", h.url("/invites"), M{}) })
	wg.Wait()

	if requests < 200 {
		t.Errorf("only %d requests in 20s", requests)
	}
	var kinds []string
	for k, n := range bad {
		kinds = append(kinds, fmt.Sprintf("%d × %s", n, k))
	}
	sort.Strings(kinds)
	if len(kinds) > 0 {
		t.Errorf("of %d requests failed:\n%s", requests, strings.Join(kinds[:min(len(kinds), 15)], "\n"))
	}
}

// TestE2E_Proxy: a web page of a node, seen through the dashboard.
func TestE2E_Proxy(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	agent.mu.Lock()
	agent.html = "<html><head></head><body>hi</body></html>"
	agent.mu.Unlock()
	page := h.url("/nodes/%s/proxy/8080/", box.NodeID)
	cookie := login(t, h.owner, "")
	open := func(path string, head http.Header) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", h.s.URL+path, nil)
		for k, v := range head {
			req.Header[k] = v
		}
		req.Header.Set("Cookie", "theme=dark; session="+cookie.Value+"; grafana=1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := open(page, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hi") {
		t.Fatalf("HTTP %d %s", resp.StatusCode, body)
	}
	// The node gets the cookies of its own app, never the viewer's session:
	// whoever runs the app could use it to act as the viewer.
	if got := agent.lastHeader().Get("Cookie"); strings.Contains(got, cookie.Value) || !strings.Contains(got, "theme=dark") || !strings.Contains(got, "grafana=1") {
		t.Errorf("the node received the cookies %q", got)
	}
	// And it cannot replace that session with one of its own.
	if got := resp.Header["Set-Cookie"]; len(got) != 1 || got[0] != "grafana=2; Path=/; HttpOnly" {
		t.Errorf("the viewer's browser received the cookies %q", got)
	}
	open(page, http.Header{"Authorization": {"Bearer " + h.owner.Token}}).Body.Close()
	if got := fmt.Sprint(agent.lastHeader()); strings.Contains(got, h.owner.Token) || strings.Contains(got, cookie.Value) {
		t.Errorf("the node received the viewer's session: %s", got)
	}
	ws, _, err := wsDial(h.owner, page+"ws", h.s.URL)
	if err != nil {
		t.Fatal(err)
	}
	ws.WriteMessage(websocket.TextMessage, []byte("echo"))
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "echo" {
		t.Errorf("WebSocket: %q, %v", msg, err)
	}
	ws.Close()
	if got := fmt.Sprint(agent.lastHeader()); strings.Contains(got, h.owner.Token) {
		t.Errorf("the node received the viewer's session over WebSocket: %s", got)
	}

	e2etest.KnownGap(t, "a node's page runs on the dashboard's own origin, so its scripts can act as the viewer", func(g *e2etest.Gap) {
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") {
			g.Errorf("Content-Security-Policy %q", csp)
		}
	})

	leftOpen := func(what string, request func()) {
		before := agent.open.Load()
		for range 30 {
			request()
		}
		time.Sleep(time.Second)
		if n := agent.open.Load() - before; n > 3 {
			t.Errorf("30 %s left %d connections to the node open", what, n)
		}
	}
	leftOpen("page views", func() {
		resp := open(page, nil)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	})
	leftOpen("health checks", func() { h.member.Want(200, "GET", h.url("/nodes/%s/health", box.NodeID), nil, nil) })
	leftOpen("commands", func() { h.owner.Do("POST", h.url("/nodes/%s/exec", box.NodeID), M{"command": "true"}) })
}

// TestE2E_Clipboard: a copied text reaches the other devices of the same network
// and nobody else.
func TestE2E_Clipboard(t *testing.T) {
	h := newHousehold(t)
	box, nas := h.owner.Enroll(h.net.ID, "box"), h.owner.Enroll(h.net.ID, "nas")
	far := h.outsider.Enroll(h.outsider.CreateNetwork("far").ID, "far")
	as := func(token string) *e2etest.Client { c := h.s.Anonymous(); c.Token = token; return c }
	announce := func(want int, token string, body M) {
		t.Helper()
		as(token).Want(want, "POST", "/api/clipboard/announce", body, nil)
	}
	poll := func(e e2etest.Enrollment, since int64) (clip struct {
		ClipID, Originator string
		Content            []byte
		TS                 int64
	}) {
		t.Helper()
		status, body := as(e.AgentToken).Do("GET", fmt.Sprintf("/api/clipboard/poll?nodeId=%s&since=%d", e.NodeID, since), nil)
		if status == 200 {
			if err := json.Unmarshal(body, &clip); err != nil {
				t.Fatal(err)
			}
		} else if status != 204 {
			t.Errorf("poll: HTTP %d %s", status, body)
		}
		return clip
	}
	fetch := func(want int, token, nodeID, clipID string) {
		t.Helper()
		as(token).Want(want, "GET", "/api/clipboard/"+clipID+"?nodeId="+nodeID, nil, nil)
	}
	text := func(e e2etest.Enrollment, id string, content []byte) M {
		return M{"nodeId": e.NodeID, "clipId": id, "hash": "h", "content": content}
	}

	live, _, err := wsDial(h.member, h.url("/events"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	announce(204, box.AgentToken, text(box, "one", []byte("hunter2")))
	e2etest.KnownGap(t, "what a device copies is sent to every open dashboard of the network, where nothing uses it", func(g *e2etest.Gap) {
		live.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var ev struct {
				Type string
				Data map[string]any
			}
			if err := live.ReadJSON(&ev); err != nil {
				t.Fatalf("no clipboard.announce event: %v", err)
			}
			if ev.Type == "clipboard.announce" {
				if text, ok := ev.Data["content"]; ok {
					g.Errorf("a member's dashboard received %v", text)
				}
				return
			}
		}
	})
	clip := poll(nas, 0)
	if clip.ClipID != "one" || clip.Originator != box.NodeID || string(clip.Content) != "hunter2" {
		t.Errorf("nas received %+v", clip)
	}
	fetch(200, nas.AgentToken, nas.NodeID, "one")
	for who, got := range map[string]string{
		"the sender":                          poll(box, 0).ClipID,
		"a device that already has it":        poll(nas, clip.TS).ClipID,
		"a device of somebody else's network": poll(far, 0).ClipID,
	} {
		if got != "" {
			t.Errorf("%s received %q", who, got)
		}
	}
	fetch(404, far.AgentToken, far.NodeID, "one")

	// Somebody else's token or node ID, or a person's session.
	for _, token := range []string{"", "wrong", far.AgentToken, nas.AgentToken, h.owner.Token} {
		announce(401, token, text(box, "fake", []byte("x")))
		fetch(401, token, box.NodeID, "one")
		as(token).Want(401, "GET", "/api/clipboard/poll?nodeId="+box.NodeID, nil, nil)
	}

	announce(400, box.AgentToken, M{"nodeId": box.NodeID, "clipId": "image", "hash": "h", "type": "image"})
	announce(204, box.AgentToken, text(box, "big", bytes.Repeat([]byte("x"), 100<<10)))
	if got := poll(nas, 0); got.ClipID != "big" || len(got.Content) != 100<<10 {
		t.Errorf("100 KB: nas received %q, %d bytes", got.ClipID, len(got.Content))
	}
	if s, _ := as(box.AgentToken).Do("POST", "/api/clipboard/announce", text(box, "huge", bytes.Repeat([]byte("x"), 256<<10+1))); s != 400 && s != 413 {
		t.Errorf("more than 256 KB: HTTP %d", s)
	}
	e2etest.KnownGap(t, "a clip of 198 to 256 KB is refused: the limit for the body forgets that it is base64", func(g *e2etest.Gap) {
		if s, body := as(box.AgentToken).Do("POST", "/api/clipboard/announce", text(box, "large", bytes.Repeat([]byte("x"), 250<<10))); s != 204 {
			g.Errorf("250 KB: HTTP %d %s", s, bytes.TrimSpace(body))
		}
	})
	if got := poll(nas, 0); got.ClipID != "big" {
		t.Errorf("after the refused ones nas received %q", got.ClipID)
	}

	h.owner.Want(204, "DELETE", h.url("/nodes/%s", nas.NodeID), nil, nil)
	fetch(401, nas.AgentToken, nas.NodeID, "big")
	as(nas.AgentToken).Want(401, "GET", "/api/clipboard/poll?nodeId="+nas.NodeID, nil, nil)
}

// TestE2E_Restart: a crash and a restart of the server lose nothing.
func TestE2E_Restart(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	agent := newFakeAgent(t, h, box)
	agent.waitReady()
	h.owner.Want(201, "POST", h.url("/dns"), M{"name": "nas", "nebulaIP": "10.42.1.9"}, nil)
	_, token := h.owner.EnrollToken(h.net.ID)

	for range 2 {
		h.s.Kill()
		h.s.Run()
		agent.waitReady() // same CA, same lighthouse, existing tunnel re-made
		a := h.s.Anonymous()
		a.Token = box.AgentToken
		a.Want(200, "POST", "/api/renew", M{"nodeId": box.NodeID}, nil)
		if ips := resolve(t, h.net, "nas.hop"); len(ips) != 1 {
			t.Errorf("nas.hop: %v", ips)
		}
		h.member.Want(200, "GET", h.url("/nodes"), nil, nil)
	}
	h.s.Anonymous().Want(200, "POST", "/api/enroll", M{"token": token, "hostname": "late", "os": "linux", "arch": "arm64"}, nil)
}

// TestE2E_OldDatabase: the database as servers up to v0.11.26 leave it. None of
// the settings were in effect then: no WAL, and without foreign keys a deleted
// network's rows stayed behind.
func TestE2E_OldDatabase(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	gone := h.owner.CreateNetwork("gone")
	lost := h.owner.Enroll(gone.ID, "lost")
	h.owner.Join(h.member, gone.ID, "member")
	h.owner.Want(201, "POST", "/api/networks/"+gone.ID+"/dns", M{"name": "nas", "nebulaIP": "10.42.2.9"}, nil)
	h.s.Stop()

	old, err := sql.Open("sqlite", filepath.Join(h.s.Dir, "hopssh.db"))
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := old.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("journal mode %q, %v", mode, err)
	}
	if _, err := old.Exec("DELETE FROM networks WHERE id = ?", gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	h.s.Run()
	newFakeAgent(t, h, box).waitReady()
	as := func(e e2etest.Enrollment) *e2etest.Client { c := h.s.Anonymous(); c.Token = e.AgentToken; return c }
	for range 3 { // the deleted network's device never stops asking
		for _, path := range []string{"/api/renew", "/api/heartbeat"} {
			as(lost).Do("POST", path, M{"nodeId": lost.NodeID})
			as(box).Want(200, "POST", path, M{"nodeId": box.NodeID}, nil)
		}
	}
	for _, c := range []*e2etest.Client{h.owner, h.admin, h.member} {
		var list []struct{ ID string }
		c.Want(200, "GET", "/api/networks", nil, &list)
		if len(list) != 1 || list[0].ID != h.net.ID {
			t.Errorf("networks: %+v, want only home", list)
		}
		c.Want(404, "GET", "/api/networks/"+gone.ID, nil, nil)
	}
	e2etest.Within(t, 20*time.Second, "box online", func() bool { return h.owner.Nodes(h.net.ID)[box.NodeID].Status == "online" })
	again := h.owner.CreateNetwork("again")
	h.owner.Enroll(again.ID, "box")
	h.owner.Want(204, "DELETE", "/api/networks/"+again.ID, nil, nil)
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", box.NodeID), nil, nil)
}
