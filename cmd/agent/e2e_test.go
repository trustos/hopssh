//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slackhq/nebula/cert"
	"gopkg.in/yaml.v3"

	"github.com/trustos/hopssh/internal/e2etest"
	"github.com/trustos/hopssh/internal/nebulacfg"
)

// End-to-end tests: the real hop-server and real agents, each in its own
// process, over a real Nebula mesh on this machine. Agents run as the user who
// runs the tests: unprivileged that is "in-app" (userspace) mode, as root the
// agent moves itself to a kernel TUN device, as an installed service does (see
// agentTunMode). Skipped with -short.

// agentTunMode is the mode an agent started by these tests runs in. The agent
// decides by itself (internal/client readTunMode): root means kernel TUN.
// Before this, a root run (the Linux VM, 2026-11-27) failed TestE2E_Lifecycle
// twice for that reason alone.
func agentTunMode() string {
	if os.Getuid() == 0 {
		return "kernel"
	}
	return "userspace"
}

// TestMain turns this test binary into an agent when a test starts it as one.
func TestMain(m *testing.M) {
	if os.Getenv("HOP_E2E_AGENT") == "1" {
		nebulacfg.PortmapEnabled = false // don't ask the router for a port mapping
		main()
		return
	}
	os.Exit(m.Run())
}

type M = e2etest.M

// agent is a hop-agent process with its own config dir.
type agent struct {
	t     *testing.T
	bin   string
	dir   string
	cmd   *exec.Cmd
	log   *e2etest.Log
	base  string // local API
	token string
}

func newAgent(t *testing.T) *agent {
	t.Helper()
	return newAgentBinary(t, os.Args[0])
}

// newAgentBinary is newAgent for another hop-agent, such as a released one.
func newAgentBinary(t *testing.T, bin string) *agent {
	t.Helper()
	a := &agent{t: t, bin: bin, dir: t.TempDir()}
	t.Cleanup(func() {
		if a.cmd != nil {
			a.stop()
		}
		if t.Failed() {
			t.Logf("agent log (%s):\n%s", a.dir, a.log.Tail(80))
		}
	})
	a.run()
	return a
}

// run starts the agent process and waits for its local API, i.e. until every
// enrollment has been tried once.
func (a *agent) run() {
	a.t.Helper()
	a.start()
	a.ready()
}

func (a *agent) start() {
	a.t.Helper()
	a.log = &e2etest.Log{}
	a.cmd = exec.Command(a.bin, "serve", "--config-dir", filepath.Join(a.dir, "config"))
	a.cmd.Env = []string{"HOP_E2E_AGENT=1", "PATH=" + os.Getenv("PATH"), "HOME=" + a.dir, "TMPDIR=" + a.dir, "SHELL=/bin/sh"}
	a.cmd.Stdout, a.cmd.Stderr = a.log, a.log
	if err := a.cmd.Start(); err != nil {
		a.t.Fatal(err)
	}
}

func (a *agent) ready() {
	a.t.Helper()
	const prefix = "HOPSSH_LOCAL_API:"
	e2etest.Within(a.t, 2*time.Minute, "the agent's local API", func() bool {
		for _, line := range strings.Split(a.log.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, prefix); ok && strings.Count(rest, ":") == 2 {
				i := strings.LastIndex(rest, ":")
				a.base, a.token = "http://"+rest[:i], strings.TrimSpace(rest[i+1:])
				return true
			}
		}
		return false
	})
}

func (a *agent) stop() {
	a.t.Helper()
	e2etest.StopProcess(a.t, "agent", a.cmd, a.log)
	a.cmd = nil
}

// kill ends the agent like a crash or a power cut.
func (a *agent) kill() {
	a.cmd.Process.Kill()
	a.cmd.Wait()
	a.cmd = nil
}

// do calls the agent's local API, as the desktop app does.
func (a *agent) do(method, path string, body, out any) int {
	a.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, a.base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s: HTTP %d %s", method, path, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode
}

func (a *agent) want(status int, method, path string, body, out any) {
	a.t.Helper()
	if got := a.do(method, path, body, out); got != status {
		a.t.Fatalf("%s %s: HTTP %d, want %d", method, path, got, status)
	}
}

// enroll joins a network with an enrollment token and returns the node ID.
func (a *agent) enroll(s *e2etest.Server, owner *e2etest.Client, networkID, name string) string {
	a.t.Helper()
	nodeID, token := owner.EnrollToken(networkID)
	var out struct {
		Enrollment string
		Connected  bool
	}
	a.want(http.StatusOK, "POST", "/local/enroll/token", M{"token": token, "endpoint": s.URL, "name": name}, &out)
	if out.Enrollment != name || !out.Connected {
		a.t.Fatalf("enroll: %+v, want %q connected", out, name)
	}
	return nodeID
}

type status struct {
	Name, NodeID, NebulaIP, MeshIP, LastError, TunMode string
	Connected                                          bool
	ListenPort                                         int
}

func (a *agent) status(name string) status {
	a.t.Helper()
	var out struct{ Enrollments []status }
	a.want(http.StatusOK, "GET", "/local/status", nil, &out)
	for _, e := range out.Enrollments {
		if e.Name == name {
			return e
		}
	}
	return status{}
}

func (a *agent) file(name, file string) string { return filepath.Join(a.dir, "config", name, file) }

func (a *agent) read(name, file string) string {
	a.t.Helper()
	b, err := os.ReadFile(a.file(name, file))
	if err != nil {
		a.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// certLeft is how long the certificate on disk is still valid.
func (a *agent) certLeft(name string) time.Duration {
	a.t.Helper()
	c, _, err := cert.UnmarshalCertificateFromPEM([]byte(a.read(name, "node.crt") + "\n"))
	if err != nil {
		a.t.Fatal(err)
	}
	return time.Until(c.NotAfter())
}

func nodePath(networkID, nodeID string) string {
	return "/api/networks/" + networkID + "/nodes/" + nodeID
}

// healthy reports whether the control plane reaches the agent over the mesh.
func healthy(c *e2etest.Client, networkID, nodeID string) bool {
	code, body := c.Do("GET", nodePath(networkID, nodeID)+"/health", nil)
	return code == http.StatusOK && strings.Contains(string(body), `"status":"ok"`)
}

func waitHealthy(t *testing.T, d time.Duration, c *e2etest.Client, networkID, nodeID string) {
	t.Helper()
	e2etest.Within(t, d, "the control plane to reach the agent over the mesh", func() bool { return healthy(c, networkID, nodeID) })
}

func execOn(c *e2etest.Client, networkID, nodeID, command string, args ...string) (int, string) {
	c.T.Helper()
	code, out := c.Do("POST", nodePath(networkID, nodeID)+"/exec", M{"command": command, "args": args})
	return code, string(out)
}

// TestE2E_Lifecycle walks the product: two devices join (one with a token, one
// with the device flow), and everything the dashboard offers works on them.
func TestE2E_Lifecycle(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	n := owner.CreateNetwork("home")

	a := newAgent(t)
	aID := a.enroll(s, owner, n.ID, "home")

	// Device flow: the agent shows a code, the owner approves it.
	b := newAgent(t)
	var flow struct{ DeviceCode, UserCode string }
	b.want(http.StatusOK, "POST", "/local/enroll/device-flow/start", M{"endpoint": s.URL, "name": "home"}, &flow)
	var poll struct {
		Status, Message, Enrollment, ConnectError string
		Connected                                 bool
	}
	b.want(http.StatusOK, "POST", "/local/enroll/device-flow/poll", M{"deviceCode": flow.DeviceCode}, &poll)
	if poll.Status != "pending" {
		t.Fatalf("poll before approval: %+v, want pending", poll)
	}
	owner.Want(http.StatusOK, "POST", "/api/device/authorize", M{"userCode": flow.UserCode, "networkId": n.ID}, nil)
	b.want(http.StatusOK, "POST", "/local/enroll/device-flow/poll", M{"deviceCode": flow.DeviceCode}, &poll)
	if poll.Status != "complete" || !poll.Connected || poll.ConnectError != "" {
		t.Fatalf("poll after approval: %+v, want complete and connected", poll)
	}
	bID := b.status("home").NodeID

	waitHealthy(t, 20*time.Second, owner, n.ID, aID)
	waitHealthy(t, 20*time.Second, owner, n.ID, bID)
	e2etest.Within(t, 20*time.Second, "both nodes online", func() bool {
		nodes := owner.Nodes(n.ID)
		return nodes[aID].Status == "online" && nodes[bID].Status == "online"
	})
	if sa, sb := a.status("home"), b.status("home"); !sa.Connected || !sb.Connected || sa.TunMode != agentTunMode() || sb.TunMode != agentTunMode() || sa.ListenPort == sb.ListenPort {
		t.Errorf("status: %+v and %+v, want both connected in %s mode on different ports", sa, sb, agentTunMode())
	}

	t.Run("exec", func(t *testing.T) {
		code, out := execOn(owner, n.ID, aID, "sh", "-c", "echo out; echo err >&2; exit 3")
		if code != http.StatusOK || !strings.Contains(out, "out\n") || !strings.Contains(out, "err\n") || !strings.HasSuffix(strings.TrimSpace(out), "---EXIT:3---") {
			t.Errorf("exec: HTTP %d %q", code, out)
		}
		if code, out := execOn(owner, n.ID, aID, "no-such-command-xyz"); code != http.StatusOK || !strings.Contains(out, "ERROR") {
			t.Errorf("a command that doesn't exist: HTTP %d %q", code, out)
		}
	})

	t.Run("shell", func(t *testing.T) {
		ws := shell(t, s, owner, n.ID, aID)
		ws.WriteMessage(websocket.BinaryMessage, []byte{1, 0, 50, 0, 132}) // resize to 50x132
		if out := typeIn(ws, "echo answer=$((6*7)); stty size; exit\n", ""); !strings.Contains(out, "answer=42") || !strings.Contains(out, "50 132") {
			t.Errorf("shell output:\n%s", out)
		}
	})

	t.Run("http proxy", func(t *testing.T) {
		app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/old" {
				http.Redirect(w, r, "/new", http.StatusFound)
				return
			}
			if r.URL.Path == "/ws" {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				ws.WriteMessage(websocket.TextMessage, fmt.Appendf(nil, "%s auth=%q", r.URL, r.Header.Get("Authorization")))
				for {
					kind, msg, err := ws.ReadMessage()
					if err != nil {
						return
					}
					ws.WriteMessage(kind, msg)
				}
			}
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "%s %s body=%s auth=%q", r.Method, r.URL, body, r.Header.Get("Authorization"))
		}))
		defer app.Close()
		port := netip.MustParseAddrPort(app.Listener.Addr().String()).Port()
		base := fmt.Sprintf("%s/proxy/%d", nodePath(n.ID, aID), port)
		code, out := owner.Do("POST", base+"/deep/path?x=1", `{"hello":1}`)
		if want := `POST /deep/path?x=1 body={"hello":1} auth=""`; code != http.StatusOK || string(out) != want {
			t.Errorf("proxy: HTTP %d %q, want %q (and no agent token passed on)", code, out, want)
		}
		req, _ := http.NewRequest("GET", s.URL+base+"/old", nil)
		owner.Headers(req.Header)
		resp, err := (&http.Transport{}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); loc != base+"/new" {
			t.Errorf("redirect to %q, want %q", loc, base+"/new")
		}

		h := http.Header{}
		owner.Headers(h)
		ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(s.URL, "http", "ws", 1)+base+"/ws?x=1", h)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		ws.SetReadDeadline(time.Now().Add(20 * time.Second))
		if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != `/ws?x=1 auth=""` {
			t.Errorf("WebSocket: %q, %v", msg, err)
		}
		for _, want := range []struct {
			kind int
			data string
		}{{websocket.TextMessage, "text"}, {websocket.BinaryMessage, "\x00\xff" + strings.Repeat("x", 100_000)}} {
			ws.WriteMessage(want.kind, []byte(want.data))
			if kind, msg, err := ws.ReadMessage(); err != nil || kind != want.kind || string(msg) != want.data {
				t.Errorf("WebSocket echo: type %d, %d bytes, %v; want type %d, %d bytes", kind, len(msg), err, want.kind, len(want.data))
			}
		}
	})

	t.Run("port forward", func(t *testing.T) {
		var pf struct {
			ID        string
			LocalPort int
		}
		owner.Want(http.StatusCreated, "POST", nodePath(n.ID, aID)+"/port-forwards", M{"remotePort": 41820}, &pf)
		get := func() (int, error) {
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/health", pf.LocalPort), nil)
			req.Header.Set("Authorization", "Bearer "+a.read("home", "token"))
			resp, err := (&http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
			if err != nil {
				return 0, err
			}
			resp.Body.Close()
			return resp.StatusCode, nil
		}
		if code, err := get(); code != http.StatusOK {
			t.Errorf("through the port forward: HTTP %d, %v", code, err)
		}
		var list []struct{ ID string }
		owner.Want(http.StatusOK, "GET", "/api/networks/"+n.ID+"/port-forwards", nil, &list)
		if len(list) != 1 || list[0].ID != pf.ID {
			t.Errorf("port forwards: %+v", list)
		}
		owner.Want(http.StatusNoContent, "DELETE", "/api/networks/"+n.ID+"/port-forwards/"+pf.ID, nil, nil)
		if _, err := get(); err == nil {
			t.Error("the port forward still works after it was stopped")
		}
	})

	t.Run("capabilities", func(t *testing.T) {
		owner.Want(http.StatusOK, "PUT", nodePath(n.ID, aID)+"/capabilities", M{"capabilities": []string{"health"}}, nil)
		if code, _ := execOn(owner, n.ID, aID, "true"); code != http.StatusForbidden {
			t.Errorf("exec without the terminal capability: HTTP %d", code)
		}
		if code, _ := owner.Do("GET", nodePath(n.ID, aID)+"/proxy/9/", nil); code != http.StatusForbidden {
			t.Errorf("proxy without the forward capability: HTTP %d", code)
		}
		if !healthy(owner, n.ID, aID) {
			t.Error("health is still enabled")
		}
		owner.Want(http.StatusOK, "PUT", nodePath(n.ID, aID)+"/capabilities", M{"capabilities": []string{"terminal", "health", "forward"}}, nil)
	})

	t.Run("peer to peer", func(t *testing.T) {
		// A third device finds the agent through the lighthouse and talks to
		// it directly. Reaching the agent is not enough: its API wants the
		// token of that very node.
		peer := e2etest.StartPeer(t, owner.Enroll(n.ID, "peer"))
		hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: peer.DialContext, DisableKeepAlives: true}}
		get := func(token string) int {
			req, _ := http.NewRequest("GET", "http://"+meshIP(t, a)+":41820/health", nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := hc.Do(req)
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		e2etest.Within(t, 30*time.Second, "the peer to reach the agent", func() bool { return get("") != 0 })
		for who, want := range map[string]struct {
			token string
			code  int
		}{
			"no token":           {"", http.StatusUnauthorized},
			"another node's":     {b.read("home", "token"), http.StatusUnauthorized},
			"the user's session": {owner.Token, http.StatusUnauthorized},
			"that agent's own":   {a.read("home", "token"), http.StatusOK},
		} {
			if got := get(want.token); got != want.code {
				t.Errorf("token %s: HTTP %d, want %d", who, got, want.code)
			}
		}
	})

	t.Run("force renewal", func(t *testing.T) {
		ws := shell(t, s, owner, n.ID, aID)
		if out := typeIn(ws, "echo before=$((6*7))\n", "before=42"); !strings.Contains(out, "before=42") {
			t.Fatalf("shell output:\n%s", out)
		}
		before := a.read("home", "node.crt")
		a.want(http.StatusOK, "POST", "/local/renew", M{"enrollment": "home"}, nil)
		if a.read("home", "node.crt") == before {
			t.Error("the certificate on disk did not change")
		}
		waitHealthy(t, 30*time.Second, owner, n.ID, aID)
		after := func() bool {
			out := typeIn(ws, "echo after=$((6*7))\n", "after=42")
			return strings.Contains(out, "after=42")
		}
		if agentTunMode() == "kernel" {
			// The kernel holds the agent's end of the session, and the
			// renewal replaces only Nebula: the shell lives on. Seen as root
			// in the Linux VM, where the known gap below reported FIXED.
			if !after() {
				t.Error("the open shell is dead after the renewal of a kernel-TUN network")
			}
		} else {
			e2etest.KnownGap(t, "every certificate renewal (about every 12h) ends all open sessions of an in-app network", func(g *e2etest.Gap) {
				if !after() {
					g.Errorf("the open shell is dead after the renewal")
				}
			})
		}
		a.want(http.StatusTooManyRequests, "POST", "/local/renew", M{"enrollment": "home"}, nil)
	})

	t.Run("disconnect and connect", func(t *testing.T) {
		a.want(http.StatusOK, "POST", "/local/disconnect?enrollment=home", nil, nil)
		if healthy(owner, n.ID, aID) {
			t.Error("the agent still answers after Disconnect")
		}
		a.want(http.StatusOK, "POST", "/local/connect?enrollment=home", nil, nil)
		waitHealthy(t, 30*time.Second, owner, n.ID, aID)
	})

	t.Run("the local API needs its token", func(t *testing.T) {
		for _, token := range []string{"", "wrong", a.read("home", "token")} {
			req, _ := http.NewRequest("GET", a.base+"/local/status", nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("token %.6q…: HTTP %d, want 401", token, resp.StatusCode)
			}
		}
	})

	t.Run("crash and reboot", func(t *testing.T) {
		a.kill()
		a.run()
		waitHealthy(t, 30*time.Second, owner, n.ID, aID)
		if got := a.status("home").NodeID; got != aID {
			t.Errorf("node ID %s after the restart, was %s", got, aID)
		}
	})

	t.Run("control plane crash", func(t *testing.T) {
		s.Kill()
		if !a.status("home").Connected {
			t.Error("the agent dropped its mesh because the control plane died")
		}
		s.Run()
		waitHealthy(t, 40*time.Second, owner, n.ID, aID)
		waitHealthy(t, 40*time.Second, owner, n.ID, bID)
	})

	t.Run("leave", func(t *testing.T) {
		b.want(http.StatusOK, "POST", "/local/leave", M{"enrollment": "home"}, nil)
		if healthy(owner, n.ID, bID) || b.status("home").Name != "" {
			t.Error("the device is still in the network")
		}
		if _, err := os.Stat(b.file("home", "node.key")); err == nil {
			t.Error("leaving left the private key on disk")
		}
	})
}

// shell opens the dashboard's terminal on a node.
func shell(t *testing.T, s *e2etest.Server, c *e2etest.Client, networkID, nodeID string) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	c.Headers(h)
	ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(s.URL, "http", "ws", 1)+nodePath(networkID, nodeID)+"/shell", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// typeIn types a line into the terminal and returns what it shows until it
// contains want or the terminal is closed.
func typeIn(ws *websocket.Conn, line, want string) string {
	ws.WriteMessage(websocket.TextMessage, []byte(line))
	ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	var out strings.Builder
	for want == "" || !strings.Contains(out.String(), want) {
		_, msg, err := ws.ReadMessage()
		out.Write(msg)
		if err != nil {
			break
		}
	}
	return out.String()
}

func meshIP(t *testing.T, a *agent) string {
	t.Helper()
	st := a.status("home")
	ip := st.NebulaIP
	if ip == "" {
		ip = st.MeshIP
	}
	ip, _, _ = strings.Cut(ip, "/")
	if net.ParseIP(ip) == nil {
		t.Fatalf("no mesh IP in %+v", st)
	}
	return ip
}

// TestE2E_OffForHalfAYear is the 2026-11 MacBook against the real server: two
// networks, both certificates expired half a year ago, and the control plane
// down while the machine boots. Nobody touches the machine.
func TestE2E_OffForHalfAYear(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	home, work := owner.CreateNetwork("home"), owner.CreateNetwork("work")
	a := newAgent(t)
	homeID, workID := a.enroll(s, owner, home.ID, "home"), a.enroll(s, owner, work.ID, "work")
	waitHealthy(t, 20*time.Second, owner, home.ID, homeID)
	waitHealthy(t, 20*time.Second, owner, work.ID, workID)
	a.stop()
	s.Stop()

	const halfAYear = 182 * 24 * time.Hour
	s.IssuedAgo(home.ID, a.file("home", "node.crt"), halfAYear)
	s.IssuedAgo(work.ID, a.file("work", "node.crt"), halfAYear)
	s.Exec(`UPDATE nodes SET last_seen_at = ?, status = 'offline' WHERE node_type != 'lighthouse'`, time.Now().Add(-halfAYear+24*time.Hour).Unix())

	a.run()
	if st := a.status("home"); st.Connected || st.LastError == "" {
		t.Errorf("no control plane and an expired certificate: %+v, want not connected and an error", st)
	}
	s.Run()
	waitHealthy(t, 90*time.Second, owner, home.ID, homeID)
	waitHealthy(t, 90*time.Second, owner, work.ID, workID)
	for _, name := range []string{"home", "work"} {
		if left := a.certLeft(name); left < 23*time.Hour {
			t.Errorf("%s: the certificate is valid for another %s", name, left)
		}
		e2etest.Within(t, 30*time.Second, name+" to show as connected", func() bool { return a.status(name).Connected })
	}
}

// TestE2E_ServerCannotReadItsDatabase: the control plane is up but can't read
// its nodes. To the agent that must look like an outage, not like having been
// deleted: it keeps trying and comes back with the server.
func TestE2E_ServerCannotReadItsDatabase(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	n := owner.CreateNetwork("home")
	a := newAgent(t)
	id := a.enroll(s, owner, n.ID, "home")
	agentToken := a.read("home", "token")
	a.stop()
	s.IssuedAgo(n.ID, a.file("home", "node.crt"), 30*24*time.Hour)

	s.Exec(`ALTER TABLE nodes RENAME TO nodes_unreadable`)
	for _, path := range []string{"/api/renew", "/api/heartbeat"} {
		if code, body := s.Anonymous().As(agentToken).Do("POST", path, M{"nodeId": id}); code != http.StatusInternalServerError {
			t.Errorf("%s: HTTP %d %s, want 500", path, code, body)
		}
	}
	a.run()
	e2etest.Within(t, 30*time.Second, "the agent to report the failure", func() bool { return a.status("home").LastError != "" })
	s.Exec(`ALTER TABLE nodes_unreadable RENAME TO nodes`)
	waitHealthy(t, 90*time.Second, owner, n.ID, id)
}

// TestE2E_DeletedNode: a device removed in the dashboard stops renewing, its
// other networks keep working, and it can be enrolled again.
func TestE2E_DeletedNode(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	home, work := owner.CreateNetwork("home"), owner.CreateNetwork("work")
	a := newAgent(t)
	homeID, workID := a.enroll(s, owner, home.ID, "home"), a.enroll(s, owner, work.ID, "work")
	waitHealthy(t, 20*time.Second, owner, home.ID, homeID)
	token := a.read("home", "token")

	owner.Want(http.StatusNoContent, "DELETE", nodePath(home.ID, homeID), nil, nil)
	for _, path := range []string{"/api/renew", "/api/heartbeat"} {
		if code, _ := s.Anonymous().As(token).Do("POST", path, M{"nodeId": homeID}); code != http.StatusUnauthorized {
			t.Errorf("%s of a deleted node: HTTP %d, want 401", path, code)
		}
	}
	if code := a.do("POST", "/local/renew", M{"enrollment": "home"}, nil); code == http.StatusOK {
		t.Error("a deleted node renewed its certificate")
	}
	a.stop()
	s.IssuedAgo(home.ID, a.file("home", "node.crt"), 48*time.Hour)
	a.run()
	waitHealthy(t, 30*time.Second, owner, work.ID, workID)
	if st := a.status("home"); st.Connected || !strings.Contains(st.LastError, "removed from the network") {
		t.Errorf("status of a deleted node: %+v", st)
	}

	// The way back: leave, then enroll again.
	a.want(http.StatusOK, "POST", "/local/leave", M{"enrollment": "home"}, nil)
	waitHealthy(t, 60*time.Second, owner, home.ID, a.enroll(s, owner, home.ID, "home"))
}

// TestE2E_MemberShell and TestE2E_DeletedPeer pin what the evaluation of
// 2026-11-23 found and nobody has fixed yet.
func TestE2E_MemberShell(t *testing.T) {
	s := e2etest.Start(t)
	owner, member := s.Register("owner"), s.Register("member")
	n := owner.CreateNetwork("home")
	owner.Join(member, n.ID, "member")
	a := newAgent(t)
	id := a.enroll(s, owner, n.ID, "home")
	waitHealthy(t, 20*time.Second, owner, n.ID, id)

	e2etest.KnownGap(t, "any member runs commands as the agent's user (root on a server) on every node", func(g *e2etest.Gap) {
		if code, out := execOn(member, n.ID, id, "id"); code != http.StatusForbidden {
			g.Errorf("exec by a plain member: HTTP %d %.30q", code, out)
		}
	})
	e2etest.KnownGap(t, "the audit log does not say which command was run", func(g *e2etest.Gap) {
		execOn(owner, n.ID, id, "echo", "audited-marker")
		var log string
		for range 40 { // the audit log is written in batches
			_, b := owner.Do("GET", "/api/networks/"+n.ID+"/audit", nil)
			if log = string(b); strings.Contains(log, `"exec"`) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !strings.Contains(log, `"exec"`) {
			t.Fatalf("no exec in the audit log: %s", log)
		}
		if !strings.Contains(log, "audited-marker") {
			g.Errorf("the command is not in the audit log")
		}
	})
}

func TestE2E_DeletedPeer(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	n := owner.CreateNetwork("home")
	a := newAgent(t)
	a.enroll(s, owner, n.ID, "home")
	stolen := owner.Enroll(n.ID, "stolen-laptop")
	owner.Want(http.StatusNoContent, "DELETE", nodePath(n.ID, stolen.NodeID), nil, nil)

	e2etest.KnownGap(t, "a deleted device can join the mesh until its certificate expires, up to 24h later", func(g *e2etest.Gap) {
		peer := e2etest.StartPeer(t, stolen)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for ctx.Err() == nil {
			dial, done := context.WithTimeout(ctx, 3*time.Second)
			conn, err := peer.DialContext(dial, "tcp", meshIP(t, a)+":41820")
			done()
			if err == nil {
				fmt.Fprintf(conn, "GET /health HTTP/1.0\r\n\r\n")
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				line, _ := bufio.NewReader(conn).ReadString('\n')
				conn.Close()
				g.Errorf("a deleted device reached another node: %q", strings.TrimSpace(line))
				return
			}
		}
	})
}

// TestE2E_Upgrade goes from a released version to this one, server first as in
// production, and back as after a withdrawn release. HOP_E2E_OLD is a directory
// with that version's hop-server and hop-agent (built with nebulacfg's
// PortmapEnabled off).
func TestE2E_Upgrade(t *testing.T) {
	dir := os.Getenv("HOP_E2E_OLD")
	if dir == "" {
		t.Skip("HOP_E2E_OLD is not set")
	}
	oldServer, oldAgent := filepath.Join(dir, "hop-server"), filepath.Join(dir, "hop-agent")

	s := e2etest.StartBinary(t, oldServer)
	owner := s.Register("owner")
	n, gone := owner.CreateNetwork("home"), owner.CreateNetwork("gone")
	owner.Want(http.StatusCreated, "POST", "/api/networks/"+gone.ID+"/dns", M{"name": "nas", "nebulaIP": "10.42.2.9"}, nil)
	owner.Want(http.StatusCreated, "POST", "/api/networks/"+gone.ID+"/invites", M{}, nil)
	owner.Want(http.StatusNoContent, "DELETE", "/api/networks/"+gone.ID, nil, nil) // up to v0.11.26 that leaves rows behind
	a := newAgentBinary(t, oldAgent)
	id := a.enroll(s, owner, n.ID, "home")
	joinedIP, token := a.status("home").MeshIP, a.read("home", "token")

	check := func(what string) {
		t.Helper()
		before := time.Now().Unix()
		e2etest.Within(t, 90*time.Second, what+": a heartbeat", func() bool {
			at := owner.Nodes(n.ID)[id].LastSeenAt
			return at != nil && *at > before
		})
		s.Anonymous().As(token).Want(http.StatusOK, "POST", "/api/renew", M{"nodeId": id}, nil)
		if st := a.status("home"); st.MeshIP != joinedIP || st.NodeID != id {
			t.Errorf("%s: %+v, was node %s with mesh IP %s", what, st, id, joinedIP)
		}
		// On macOS, v0.11.26 holds packets of in-app networks back.
		if runtime.GOOS == "darwin" && (a.bin == oldAgent || s.Bin == oldServer) {
			return
		}
		waitHealthy(t, 2*time.Minute, owner, n.ID, id)
		if _, out := execOn(owner, n.ID, id, "echo", "hello"); !strings.Contains(out, "hello") {
			t.Errorf("%s: exec: %q", what, out)
		}
		if a.bin == oldAgent {
			return // a renewal closes the network, which can end a v0.11.26 agent
		}
		cert := a.read("home", "node.crt")
		e2etest.Within(t, 90*time.Second, what+": a renewal (one a minute)", func() bool {
			return a.do("POST", "/local/renew", M{"enrollment": "home"}, nil) == http.StatusOK
		})
		if a.read("home", "node.crt") == cert {
			t.Errorf("%s: the certificate was not renewed", what)
		}
		waitHealthy(t, 2*time.Minute, owner, n.ID, id)
	}
	serverNow := func(bin string) { s.Kill(); s.Bin = bin; s.Run() }
	agentNow := func(bin string) { a.kill(); a.bin = bin; a.run() }

	check("old server, old agent")
	serverNow("")
	check("new server, old agent")
	second := owner.Enroll(n.ID, "second")
	owner.Want(http.StatusNoContent, "DELETE", nodePath(n.ID, second.NodeID), nil, nil)
	agentNow(os.Args[0])
	check("new server, new agent")
	serverNow(oldServer)
	check("old server, new agent")
	agentNow(oldAgent)
	check("old server, old agent again")
	agentNow(os.Args[0]) // one that stops in time
}

// TestE2E_DebugListenerLoopback: `serve --token` with no enrollment serves
// the agent API (exec, shell, upload) for local testing. Without --listen it
// must stay on loopback, not on every interface of the machine.
func TestE2E_DebugListenerLoopback(t *testing.T) {
	dir := t.TempDir()
	l := &e2etest.Log{}
	cmd := exec.Command(os.Args[0], "serve", "--config-dir", filepath.Join(dir, "config"), "--token", "debug-token")
	cmd.Env = []string{"HOP_E2E_AGENT=1", "PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir}
	cmd.Stdout, cmd.Stderr = l, l
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer e2etest.StopProcess(t, "agent", cmd, l)

	var addr string
	e2etest.Within(t, time.Minute, "the debug listener", func() bool {
		_, rest, ok := strings.Cut(l.String(), "hop-agent listening on ")
		addr, _, _ = strings.Cut(rest, " ")
		return ok
	})
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() {
		t.Fatalf("debug listener on %q, want a loopback address", addr)
	}

	// An agent installed on this machine may listen on the same port at its
	// mesh addresses; only this one knows debug-token, so it is asked for
	// /health with it rather than just dialled.
	answers := func(hostPort string) bool {
		req, _ := http.NewRequest("GET", "http://"+hostPort+"/health", nil)
		req.Header.Set("Authorization", "Bearer debug-token")
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	if !answers(addr) {
		t.Fatalf("the debug listener on %s does not answer /health with its token", addr)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil {
			continue
		}
		lan := net.JoinHostPort(ipn.IP.String(), fmt.Sprint(ap.Port()))
		if answers(lan) {
			t.Errorf("the agent API answers on %s", lan)
		}
	}
}

// TestE2E_FirewallPreset: a real agent writes the firewall block its
// network's preset names. In a new network every node may reach it on every
// protocol; in one from before migration 009 on TCP only, as before.
func TestE2E_FirewallPreset(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	for preset, want := range map[string]string{"all": "any", "tcp": "tcp"} {
		t.Run(preset, func(t *testing.T) {
			n := owner.CreateNetwork(preset)
			if preset == "tcp" {
				s.Exec("UPDATE networks SET firewall_preset = 'tcp' WHERE id = ?", n.ID)
			}
			a := newAgent(t)
			a.enroll(s, owner, n.ID, preset)
			var cfg struct {
				Firewall struct {
					Inbound []struct {
						Port   any
						Proto  string
						Groups []string
					}
				}
			}
			if err := yaml.Unmarshal([]byte(a.read(preset, "nebula.yaml")), &cfg); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range cfg.Firewall.Inbound {
				if fmt.Sprint(r.Port) == "any" && len(r.Groups) == 1 && r.Groups[0] == "node" {
					got = append(got, r.Proto)
				}
			}
			if len(got) != 1 || got[0] != want {
				t.Errorf("rules for every port from group node allow %v, want [%s]", got, want)
			}
		})
	}
}

// TestE2E_FirewallApplied: phase 2 of docs/wiki/concepts/firewall-rules.md
// across the real processes. Rules saved in the dashboard reach the agent in
// a heartbeat answer, and it applies them in place in the mode it runs in
// (kernel TUN as root, as in the Linux VM). The control plane still reaches
// it, and its next heartbeat reports the rules, which the dashboard shows.
// Up to two heartbeats: about two minutes.
func TestE2E_FirewallApplied(t *testing.T) {
	s := e2etest.Start(t)
	owner := s.Register("owner")
	n := owner.CreateNetwork("home")
	a := newAgent(t)
	id := a.enroll(s, owner, n.ID, "home")
	waitHealthy(t, time.Minute, owner, n.ID, id)

	fw := "/api/networks/" + n.ID + "/firewall"
	owner.With("If-Match", `"0"`).Want(http.StatusOK, "PUT", fw, M{"preset": "ping", "rules": []M{
		{"proto": "tcp", "port": "7000", "targetNodeId": id},
	}}, nil)

	inFile := func() bool {
		var cfg struct {
			Firewall struct {
				Inbound []struct {
					Port   any
					Proto  string
					Groups []string
				}
			}
		}
		if err := yaml.Unmarshal([]byte(a.read("home", "nebula.yaml")), &cfg); err != nil {
			t.Fatal(err)
		}
		for _, r := range cfg.Firewall.Inbound {
			if fmt.Sprint(r.Port) == "7000" && r.Proto == "tcp" && len(r.Groups) == 1 && r.Groups[0] == "node" {
				return true
			}
		}
		return false
	}
	e2etest.Within(t, 90*time.Second, "the rule in the agent's nebula.yaml", inFile)

	type device struct {
		NodeID, Expected, Fingerprint, Error string
		ReportedAt                           int64
	}
	var last device
	e2etest.Within(t, 90*time.Second, "the dashboard to show the rules applied", func() bool {
		var p struct{ Devices []device }
		owner.Want(http.StatusOK, "GET", fw, nil, &p)
		for _, d := range p.Devices {
			if d.NodeID == id {
				last = d
			}
		}
		return last.ReportedAt != 0 && last.Expected != "" && last.Fingerprint == last.Expected && last.Error == ""
	})
	if !healthy(owner, n.ID, id) {
		t.Error("the control plane no longer reaches the agent under the rules")
	}
	if log := a.log.String(); !strings.Contains(log, "[firewall home] runs policy") || strings.Contains(log, "refused policy") || strings.Contains(log, "waits for the next heartbeat") {
		t.Errorf("the agent's log about the policy:\n%s", a.log.Tail(40))
	}
}
