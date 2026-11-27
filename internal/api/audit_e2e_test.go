package api_test

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/e2etest"
)

// auditedRoutes: every route that changes something, with the action it
// writes to the audit log. A route that changes nothing (GET) is left out,
// except the ones that do something.
var auditedRoutes = map[string]string{
	"POST /api/auth/register":       "register",
	"POST /api/auth/login":          "login",
	"GET /api/auth/github/callback": "login.github",

	"POST /api/networks":               "network.create",
	"DELETE /api/networks/{networkID}": "network.delete",

	// Each way a node comes in. A machine enrolls without a session, so its
	// entry names the user who gave it the credential.
	"POST /api/networks/{networkID}/nodes":   "node.create",
	"POST /api/enroll":                       "node.enroll",
	"POST /api/device/authorize":             "device.authorize",
	"POST /api/device/poll":                  "node.enroll",
	"POST /api/networks/{networkID}/join":    "node.join",
	"POST /api/networks/{networkID}/bundles": "bundle.create",
	"GET /api/bundles/{token}":               "bundle.download",

	"PATCH /api/networks/{networkID}/nodes/{nodeID}":              "node.rename",
	"PUT /api/networks/{networkID}/nodes/{nodeID}/capabilities":   "node.capabilities",
	"PUT /api/networks/{networkID}/nodes/{nodeID}/routes":         "node.routes",
	"DELETE /api/networks/{networkID}/nodes/{nodeID}":             "node.delete",
	"POST /api/networks/{networkID}/nodes/prune":                  "nodes.prune",
	"GET /api/networks/{networkID}/nodes/{nodeID}/shell":          "shell.connect",
	"POST /api/networks/{networkID}/nodes/{nodeID}/exec":          "exec",
	"* /api/networks/{networkID}/nodes/{nodeID}/proxy/{port}/*":   "port_forward.proxy",
	"POST /api/networks/{networkID}/nodes/{nodeID}/port-forwards": "port_forward.start",
	"DELETE /api/networks/{networkID}/port-forwards/{fwdID}":      "port_forward.stop",

	"POST /api/networks/{networkID}/dns":              "dns.create",
	"DELETE /api/networks/{networkID}/dns/{recordID}": "dns.delete",

	"DELETE /api/networks/{networkID}/members/{memberID}": "member.remove",
	"POST /api/networks/{networkID}/invites":              "invite.create",
	"DELETE /api/networks/{networkID}/invites/{inviteID}": "invite.revoke",
	"POST /api/invites/{code}/accept":                     "invite.accept",
}

// notAudited: routes that change something and write no entry, and why.
var notAudited = map[string]string{
	"POST /api/auth/logout":        "ends the caller's own session",
	"POST /api/device/code":        "anonymous; the code does nothing until a user approves it (device.authorize)",
	"POST /api/renew":              "machine traffic, a few times a day per node",
	"POST /api/heartbeat":          "machine traffic, every 30 s per node",
	"POST /api/clipboard/announce": "machine traffic",
	"GET /api/networks/{networkID}/nodes/{nodeID}/health": "liveness only: an answer marks the node online, " +
		"and a change of status goes to the network's events",
}

// readOnly: routes that change nothing. A new route must be put in one of
// auditedRoutes, notAudited and readOnly; a GET is not taken on trust.
var readOnly = map[string]bool{
	"GET /swagger/*":                               true,
	"GET /healthz":                                 true,
	"GET /version":                                 true,
	"GET /install.sh":                              true,
	"GET /install-mac.sh":                          true,
	"GET /download/SHA256SUMS":                     true,
	"GET /download/desktop/{asset}":                true,
	"GET /download/{binary}":                       true,
	"GET /api/auth/status":                         true,
	"GET /api/auth/oauth/status":                   true,
	"GET /api/auth/github/start":                   true, // sets the state cookie of the login it starts
	"GET /api/auth/me":                             true,
	"GET /api/audit":                               true,
	"GET /api/networks/{networkID}/audit":          true,
	"GET /api/networks":                            true,
	"GET /api/networks/{networkID}":                true,
	"GET /api/networks/{networkID}/nodes":          true,
	"GET /api/networks/{networkID}/port-forwards":  true,
	"GET /api/networks/{networkID}/dns":            true,
	"GET /api/networks/{networkID}/members":        true,
	"GET /api/networks/{networkID}/invites":        true,
	"GET /api/networks/{networkID}/events":         true,
	"GET /api/networks/{networkID}/events/history": true,
	"GET /api/invites/{code}":                      true,
	"GET /api/device/verify/{code}":                true,
	"GET /api/clipboard/poll":                      true,
	"GET /api/clipboard/{clipId}":                  true,
}

// auditedElsewhere: actions TestE2E_Audit does not perform itself.
var auditedElsewhere = map[string]string{
	"login.github": "not tested: the tests have no GitHub",
}

type auditWant struct {
	who                   *e2etest.Client
	action, network, node string
	details               e2etest.M
}

type auditGot struct {
	Action, UserID    string
	NetworkID, NodeID *string
	Details           *string
}

func (w auditWant) String() string {
	return fmt.Sprintf("%s by %s (network %q, node %q, details %v)", w.action, w.who.Email, w.network, w.node, w.details)
}

func (w auditWant) matches(e auditGot) bool {
	if e.Action != w.action || e.UserID != w.who.ID {
		return false
	}
	if w.network != "" && (e.NetworkID == nil || *e.NetworkID != w.network) {
		return false
	}
	if w.node != "" && (e.NodeID == nil || *e.NodeID != w.node) {
		return false
	}
	if len(w.details) == 0 {
		return true
	}
	var d map[string]any
	if e.Details == nil || json.Unmarshal([]byte(*e.Details), &d) != nil {
		return false
	}
	for k, v := range w.details {
		if fmt.Sprint(d[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// TestE2E_Audit: everything that changes a network or lets someone in is in
// the audit log, under the user who did it, and no entry holds a secret.
func TestE2E_Audit(t *testing.T) {
	h := newHousehold(t)
	var wants []auditWant
	expect := func(who *e2etest.Client, action, network, node string, details e2etest.M) {
		wants = append(wants, auditWant{who, action, network, node, details})
	}
	// counts: entries that must be there exactly n times. A delete that
	// finds nothing writes no entry, and of deletes that race only the one
	// that deleted writes one.
	type counted struct {
		auditWant
		n int
	}
	var counts []counted
	expectN := func(n int, who *e2etest.Client, action, network string, details e2etest.M) {
		counts = append(counts, counted{auditWant{who, action, network, "", details}, n})
	}
	// burst sends the same DELETE several times at once; each answers 204.
	// The connections are opened first, so that the DELETEs reach the
	// server together.
	burst := func(c *e2etest.Client, path string) {
		t.Helper()
		const n = 16
		client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: n}}
		defer client.CloseIdleConnections()
		do := func(method string) int {
			req, err := http.NewRequest(method, c.URL+path, nil)
			if err != nil {
				t.Error(err)
				return 0
			}
			c.Headers(req.Header)
			resp, err := client.Do(req)
			if err != nil {
				t.Error(err)
				return 0
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return resp.StatusCode
		}
		run := func(method string) []int {
			var wg sync.WaitGroup
			start := make(chan struct{})
			statuses := make([]int, n)
			for i := range statuses {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					statuses[i] = do(method)
				}()
			}
			close(start)
			wg.Wait()
			return statuses
		}
		run("OPTIONS")
		statuses := run("DELETE")
		for _, s := range statuses {
			if s != 204 {
				t.Errorf("DELETE %s: HTTP %v, want 204 each time", path, statuses)
				break
			}
		}
	}
	const noSuchID = "00000000-0000-4000-8000-000000000000"
	var secrets []string

	// Accounts.
	guest := h.s.Register("guest")
	expect(guest, "register", "", "", nil)
	h.s.Anonymous().Want(200, "POST", "/api/auth/login", e2etest.M{"email": guest.Email, "password": e2etest.Password}, nil)
	expect(guest, "login", "", "", nil)

	// Networks.
	lab := h.owner.CreateNetwork("lab")
	expect(h.owner, "network.create", lab.ID, "", e2etest.M{"name": "lab"})
	h.owner.Want(204, "DELETE", "/api/networks/"+lab.ID, nil, nil)
	expect(h.owner, "network.delete", lab.ID, "", e2etest.M{"name": "lab"})

	// Members and invites.
	var inv struct{ ID, Code string }
	h.admin.Want(201, "POST", h.url("/invites"), e2etest.M{"role": "member"}, &inv)
	secrets = append(secrets, inv.Code)
	expect(h.admin, "invite.create", h.net.ID, "", e2etest.M{"inviteId": inv.ID, "role": "member"})
	guest.Want(201, "POST", "/api/invites/"+inv.Code+"/accept", nil, nil)
	expect(guest, "invite.accept", h.net.ID, "", e2etest.M{"inviteId": inv.ID, "role": "member"})
	gm := memberID(h.admin, h.net.ID, guest.ID)
	burst(h.admin, h.url("/members/%s", gm))
	expect(h.admin, "member.remove", h.net.ID, "", e2etest.M{"userId": guest.ID, "role": "member"})
	expectN(1, h.admin, "member.remove", h.net.ID, e2etest.M{"memberId": gm})
	h.admin.Want(204, "DELETE", h.url("/members/%s", noSuchID), nil, nil)
	expectN(0, h.admin, "member.remove", h.net.ID, e2etest.M{"memberId": noSuchID})
	var inv2 struct{ ID, Code string }
	h.owner.Want(201, "POST", h.url("/invites"), e2etest.M{"role": "admin"}, &inv2)
	secrets = append(secrets, inv2.Code)
	expect(h.owner, "invite.create", h.net.ID, "", e2etest.M{"inviteId": inv2.ID, "role": "admin"})
	burst(h.owner, h.url("/invites/%s", inv2.ID))
	expect(h.owner, "invite.revoke", h.net.ID, "", e2etest.M{"inviteId": inv2.ID})
	expectN(1, h.owner, "invite.revoke", h.net.ID, e2etest.M{"inviteId": inv2.ID})
	h.owner.Want(204, "DELETE", h.url("/invites/%s", noSuchID), nil, nil)
	expectN(0, h.owner, "invite.revoke", h.net.ID, e2etest.M{"inviteId": noSuchID})

	// An enrollment token, made by the admin and used by a machine.
	nodeID, token := h.admin.EnrollToken(h.net.ID)
	secrets = append(secrets, token)
	expect(h.admin, "node.create", h.net.ID, nodeID, nil)
	machine := h.s.Anonymous()
	var enrolled struct{ AgentToken string }
	machine.Want(200, "POST", "/api/enroll", e2etest.M{"token": token, "hostname": "box", "os": "linux", "arch": "arm64"}, &enrolled)
	secrets = append(secrets, enrolled.AgentToken)
	expect(h.admin, "node.enroll", h.net.ID, nodeID, e2etest.M{"hostname": "box", "via": "token", "ip": machine.IP})

	// A client device joined by a member.
	var joined struct{ NodeID, AgentToken string }
	h.member.Want(200, "POST", h.url("/join"), e2etest.M{"hostname": "laptop"}, &joined)
	secrets = append(secrets, joined.AgentToken)
	expect(h.member, "node.join", h.net.ID, joined.NodeID, e2etest.M{"hostname": "laptop"})

	// A device code, approved by a member.
	var dc struct{ DeviceCode, UserCode string }
	h.s.Anonymous().Want(200, "POST", "/api/device/code", e2etest.M{"hostname": "phone", "os": "darwin", "arch": "arm64"}, &dc)
	secrets = append(secrets, dc.DeviceCode, dc.UserCode)
	h.member.Want(200, "POST", "/api/device/authorize", e2etest.M{"userCode": dc.UserCode, "networkId": h.net.ID}, nil)
	expect(h.member, "device.authorize", h.net.ID, "", nil)
	poller := h.s.Anonymous()
	var polled struct{ NodeID, AgentToken string }
	poller.Want(200, "POST", "/api/device/poll", e2etest.M{"deviceCode": dc.DeviceCode, "hostname": "phone", "os": "darwin", "arch": "arm64"}, &polled)
	secrets = append(secrets, polled.AgentToken)
	expect(h.member, "node.enroll", h.net.ID, polled.NodeID, e2etest.M{"hostname": "phone", "via": "device", "ip": poller.IP})

	// A bundle, made by the owner and fetched by whoever has its URL.
	var bundle struct{ BundleURL, NodeID string }
	h.owner.Want(201, "POST", h.url("/bundles"), nil, &bundle)
	secrets = append(secrets, path.Base(bundle.BundleURL))
	expect(h.owner, "bundle.create", h.net.ID, bundle.NodeID, nil)
	fetcher := h.s.Anonymous()
	req, _ := http.NewRequest("GET", bundle.BundleURL, nil)
	fetcher.Headers(req.Header)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("bundle download: HTTP %d", resp.StatusCode)
	}
	secrets = append(secrets, bundleAgentToken(t, resp))
	expect(h.owner, "bundle.download", h.net.ID, bundle.NodeID, e2etest.M{"ip": fetcher.IP})

	// What an admin changes on a node.
	h.admin.Want(200, "PATCH", h.url("/nodes/%s", nodeID), e2etest.M{"name": "nas"}, nil)
	expect(h.admin, "node.rename", h.net.ID, nodeID, e2etest.M{"from": "box", "to": "nas"})
	h.admin.Want(200, "PUT", h.url("/nodes/%s/capabilities", nodeID), e2etest.M{"capabilities": []string{"health"}}, nil)
	expect(h.admin, "node.capabilities", h.net.ID, nodeID, e2etest.M{"capabilities": []string{"health"}})
	h.admin.Want(200, "PUT", h.url("/nodes/%s/routes", nodeID), e2etest.M{"routes": []string{"192.168.77.0/24"}}, nil)
	expect(h.admin, "node.routes", h.net.ID, nodeID, e2etest.M{"routes": []string{"192.168.77.0/24"}})
	h.admin.Want(200, "POST", h.url("/nodes/prune"), e2etest.M{"olderThanSeconds": 86400}, nil)
	expect(h.admin, "nodes.prune", h.net.ID, "", e2etest.M{"olderThanSeconds": 86400})

	// DNS names. Deletes that race each other show only now and then, so
	// three records.
	for i, name := range []string{"files", "media", "backup"} {
		ip := fmt.Sprintf("10.42.1.%d", 9+i)
		var rec struct{ ID string }
		h.admin.Want(201, "POST", h.url("/dns"), e2etest.M{"name": name, "nebulaIP": ip}, &rec)
		expect(h.admin, "dns.create", h.net.ID, "", e2etest.M{"recordId": rec.ID, "name": name, "ip": ip})
		burst(h.owner, h.url("/dns/%s", rec.ID))
		expect(h.owner, "dns.delete", h.net.ID, "", e2etest.M{"recordId": rec.ID, "name": name, "ip": ip})
		expectN(1, h.owner, "dns.delete", h.net.ID, e2etest.M{"recordId": rec.ID})
	}
	h.owner.Want(204, "DELETE", h.url("/dns/%s", noSuchID), nil, nil)
	expectN(0, h.owner, "dns.delete", h.net.ID, e2etest.M{"recordId": noSuchID})

	// A port forward, started by the admin and stopped by a member.
	box := h.owner.Enroll(h.net.ID, "web")
	newFakeAgent(t, h, box).waitReady()
	var pf struct{ ID string }
	h.admin.Want(201, "POST", h.url("/nodes/%s/port-forwards", box.NodeID), e2etest.M{"remotePort": 8080}, &pf)
	expect(h.admin, "port_forward.start", h.net.ID, box.NodeID, nil)
	h.member.Want(204, "DELETE", h.url("/port-forwards/%s", pf.ID), nil, nil)
	expect(h.member, "port_forward.stop", h.net.ID, box.NodeID, e2etest.M{"forwardId": pf.ID})

	// A terminal, a command and a page of the node, then the node's delete.
	shell, _, err := wsDial(h.admin, h.url("/nodes/%s/shell", box.NodeID), h.s.URL)
	if err != nil {
		t.Fatal(err)
	}
	shell.Close()
	expect(h.admin, "shell.connect", h.net.ID, box.NodeID, nil)
	h.admin.Want(200, "POST", h.url("/nodes/%s/exec", box.NodeID), e2etest.M{"command": "true"}, nil)
	expect(h.admin, "exec", h.net.ID, box.NodeID, nil)
	h.member.Want(200, "GET", h.url("/nodes/%s/proxy/8080/x", box.NodeID), nil, nil)
	expect(h.member, "port_forward.proxy", h.net.ID, box.NodeID, nil)
	h.admin.Want(204, "DELETE", h.url("/nodes/%s", box.NodeID), nil, nil)
	expect(h.admin, "node.delete", h.net.ID, box.NodeID, nil)

	// The log is written in batches, every 2 s.
	var missing []auditWant
	logs := map[*e2etest.Client][]auditGot{}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		for _, c := range []*e2etest.Client{h.owner, h.admin, h.member, guest} {
			var log []auditGot
			c.Want(200, "GET", "/api/audit?limit=1000", nil, &log)
			logs[c] = log
		}
		missing = missing[:0]
		for _, w := range wants {
			found := false
			for _, e := range logs[w.who] {
				found = found || w.matches(e)
			}
			if !found {
				missing = append(missing, w)
			}
		}
		if len(missing) == 0 || time.Now().After(deadline) {
			break
		}
	}
	for _, c := range counts {
		n := 0
		for _, e := range logs[c.who] {
			if c.matches(e) {
				n++
			}
		}
		if n != c.n {
			t.Errorf("%d entries %s, want %d", n, c.auditWant, c.n)
		}
	}
	for _, w := range missing {
		var near []string
		for _, e := range logs[w.who] {
			if e.Action == w.action {
				b, _ := json.Marshal(e)
				near = append(near, string(b))
			}
		}
		t.Errorf("no entry %s; entries with that action: %v", w, near)
	}

	// No entry holds a token, a code or a key.
	var all []auditGot
	h.owner.Want(200, "GET", h.url("/audit?limit=1000"), nil, &all)
	for _, c := range []*e2etest.Client{h.owner, h.admin, h.member, guest} {
		all = append(all, logs[c]...)
	}
	for _, e := range all {
		b, _ := json.Marshal(e)
		for _, s := range secrets {
			if s != "" && strings.Contains(string(b), s) {
				t.Errorf("an entry holds a secret: %s", b)
			}
		}
	}

	// Every route that changes something is listed, and every listed action
	// is checked.
	routes := routerRoutes(t)
	for route := range routes {
		_, audited := auditedRoutes[route]
		_, exempt := notAudited[route]
		lists := 0
		for _, in := range []bool{audited, exempt, readOnly[route]} {
			if in {
				lists++
			}
		}
		if lists != 1 {
			t.Errorf("%s: list it in exactly one of auditedRoutes, notAudited and readOnly", route)
		}
	}
	for route := range readOnly {
		if !routes[route] {
			t.Errorf("readOnly: no route %s", route)
		}
	}
	checked := map[string]bool{}
	for _, w := range wants {
		checked[w.action] = true
	}
	for route, action := range auditedRoutes {
		if !routes[route] {
			t.Errorf("auditedRoutes: no route %s", route)
		}
		if !checked[action] && auditedElsewhere[action] == "" {
			t.Errorf("%s: nothing checks that it writes %s", route, action)
		}
	}
	for route := range notAudited {
		if !routes[route] {
			t.Errorf("notAudited: no route %s", route)
		}
	}
	t.Logf("%d audited routes, %d entries checked", len(auditedRoutes), len(wants))
}

// bundleAgentToken reads the agent token out of a downloaded bundle.
func bundleAgentToken(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		if _, err := tr.Next(); err != nil {
			t.Fatalf("no agent token in the bundle: %v", err)
		}
		var cfg struct{ AgentToken string }
		if json.NewDecoder(tr).Decode(&cfg) == nil && cfg.AgentToken != "" {
			return cfg.AgentToken
		}
	}
}
