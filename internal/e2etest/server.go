// Package e2etest runs the real hop-server for end-to-end tests: the binary is
// built from this tree and runs as its own process on a temporary data
// directory, so a test talks to it exactly as an agent or a browser does.
// Only tests import this package.
package e2etest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"

	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db"
)

// Server is a running hop-server.
type Server struct {
	URL string // also the --endpoint
	Dir string // data directory
	Bin string // what Run runs; empty for the hop-server built from this tree

	t    testing.TB
	addr string
	cmd  *exec.Cmd
	log  Log
	ips  atomic.Uint32
}

// binPrefix starts the name of the folder a test process builds hop-server
// in, followed by the process's pid: nothing removes the folder when the
// process ends, so the next build removes it (sweepBuilds).
const binPrefix = "hop-e2e-bin-"

// sweepBuilds removes the build folders in dir of test processes that have
// ended. Until 2026-11-28 the folders stayed: 26 GB on a Mac, and a full
// /tmp in the Linux VM. The folder of a living process stays, another
// user's too, and so do the folders from before, named without a pid. It
// reads dir rather than globbing it: a "[" in the temp path is a pattern
// to filepath.Glob.
func sweepBuilds(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), binPrefix)
		if !ok {
			continue
		}
		pid, _, _ := strings.Cut(rest, "-")
		if n, err := strconv.Atoi(pid); err == nil && ended(n) {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// ended says whether process pid has ended. Signal 0 tests for it on Unix,
// where FindProcess never fails; a process this user may not signal
// answers EPERM and is living. On Windows FindProcess opens the process:
// OpenProcess answers ERROR_INVALID_PARAMETER (87) for a pid no process
// has, and any other refusal, access denied above all, is a living
// process. A process it can open is living too, as far as this goes:
// Signal(0) is not supported there.
func ended(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return errors.Is(err, syscall.Errno(87))
	}
	defer p.Release()
	err = p.Signal(syscall.Signal(0))
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

// build compiles hop-server once per test process, race-enabled when the test
// is. HOP_E2E_COVER=<dir> makes the servers write coverage data there.
func build() (string, error) {
	buildOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..")
		tmp := os.TempDir()
		sweepBuilds(tmp)
		dir, err := os.MkdirTemp(tmp, fmt.Sprintf("%s%d-", binPrefix, os.Getpid()))
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "hop-server")
		args := []string{"build", "-o", buildPath}
		if raceEnabled {
			args = append(args, "-race")
		}
		if os.Getenv("HOP_E2E_COVER") != "" {
			args = append(args, "-cover", "-coverpkg=./internal/...,./cmd/server")
		}
		cmd := exec.Command("go", append(args, "./cmd/server")...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build hop-server: %v\n%s", err, out)
		}
	})
	return buildPath, buildErr
}

// Start builds and starts a hop-server, stopped when the test ends. Every
// network's lighthouse is on a fixed UDP port (42001 and up), so only one
// server runs at a time on a machine; Start waits for its turn.
func Start(t testing.TB, env ...string) *Server {
	t.Helper()
	return StartBinary(t, "", env...)
}

// StartBinary is Start for another hop-server, such as a released one.
func StartBinary(t testing.TB, bin string, env ...string) *Server {
	t.Helper()
	if testing.Short() {
		t.Skip("runs a real hop-server")
	}
	if _, err := build(); err != nil {
		t.Fatal(err)
	}
	takeTurn(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{t: t, addr: l.Addr().String(), Dir: t.TempDir(), Bin: bin}
	l.Close()
	s.URL = "http://" + s.addr
	t.Cleanup(func() {
		s.Stop()
		if t.Failed() {
			t.Logf("hop-server log:\n%s", s.log.Tail(120))
		}
	})
	s.Run(env...)
	return s
}

// takeTurn holds a loopback port for the rest of the test, as a lock between
// test processes that the kernel releases whatever happens to us.
func takeTurn(t testing.TB) {
	t.Helper()
	const lock = "127.0.0.1:41999"
	// Not shared fairly: whoever has it mostly gets it for their next test too,
	// so a test may wait for another package's whole run (cmd/agent's alone
	// takes 11 minutes in the Linux VM).
	deadline := turnDeadline(t, time.Now())
	for {
		l, err := net.Listen("tcp", lock)
		if err == nil {
			t.Cleanup(func() { l.Close() })
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("another e2e test still holds %s: %v", lock, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// A lighthouse from outside the tests (a dev server on this machine).
	p, err := net.ListenPacket("udp4", ":42001")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("UDP 42001 is in use: %v", err)
		}
		t.Skipf("UDP 42001 is in use, is a hop-server running here? (%v)", err)
	}
	p.Close()
}

// turnDeadline is when takeTurn gives up waiting: a minute before go test's
// -timeout ends the test binary, so the failure says why; an hour when there
// is no timeout.
func turnDeadline(t testing.TB, now time.Time) time.Time {
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if end, ok := d.Deadline(); ok {
			return end.Add(-time.Minute)
		}
	}
	return now.Add(time.Hour)
}

// Run starts the server process on the same data directory and address, and
// waits until it answers.
func (s *Server) Run(env ...string) {
	s.t.Helper()
	bin := s.Bin
	if bin == "" {
		bin = buildPath
	}
	s.cmd = exec.Command(bin, "--addr", s.addr, "--data", s.Dir, "--endpoint", s.URL,
		"--lighthouse-host", "127.0.0.1", "--trusted-proxy")
	s.cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + s.Dir, "GORACE=halt_on_error=0"}, env...)
	if d := os.Getenv("HOP_E2E_COVER"); d != "" {
		s.cmd.Env = append(s.cmd.Env, "GOCOVERDIR="+d)
	}
	s.cmd.Stdout, s.cmd.Stderr = &s.log, &s.log
	if err := s.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	Within(s.t, 20*time.Second, "hop-server to answer", func() bool {
		resp, err := http.Get(s.URL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// Stop shuts the server down, and fails the test if it panicked or raced.
func (s *Server) Stop() {
	s.t.Helper()
	if s.cmd == nil {
		return
	}
	s.cmd.Process.Signal(syscall.SIGTERM)
	waitOrKill(s.cmd, 20*time.Second, func() { s.t.Error("hop-server did not stop within 20s of SIGTERM") })
	s.cmd = nil
	CheckLog(s.t, "hop-server", &s.log)
}

// Kill ends the server without a shutdown, as a crash or a power cut does.
func (s *Server) Kill() {
	s.cmd.Process.Kill()
	s.cmd.Wait()
	s.cmd = nil
}

// T is the test the server runs for.
func (s *Server) T() testing.TB { return s.t }

// Log is the server's output so far.
func (s *Server) Log() string { return s.log.String() }

// DB opens the server's database, for state no API can produce (a node last
// seen half a year ago, an expired token) and for the keys a test signs with.
func (s *Server) DB() (*db.DBPair, *crypto.Encryptor) {
	s.t.Helper()
	pair, err := db.Open(filepath.Join(s.Dir, "hopssh.db"))
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { pair.Close() })
	key, err := os.ReadFile(filepath.Join(s.Dir, "encryption.key"))
	if err != nil {
		s.t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(strings.TrimSpace(string(key)))
	if err != nil {
		s.t.Fatal(err)
	}
	return pair, enc
}

// Exec runs one statement on the server's database.
func (s *Server) Exec(query string, args ...any) int64 {
	s.t.Helper()
	pair, _ := s.DB()
	res, err := pair.WriteDB.Exec(query, args...)
	if err != nil {
		s.t.Fatalf("%s: %v", query, err)
	}
	n, _ := res.RowsAffected()
	return n
}

// IssuedAgo rewrites the node certificate at path as if the server had issued
// it that long ago, for the same key and valid for 24 hours.
func (s *Server) IssuedAgo(networkID, path string, age time.Duration) {
	s.t.Helper()
	pair, enc := s.DB()
	network, err := db.NewNetworkStore(pair, enc).Get(networkID)
	if err != nil || network == nil {
		s.t.Fatalf("network %s: %v, %v", networkID, network, err)
	}
	ca, _, err := cert.UnmarshalCertificateFromPEM(network.NebulaCACert)
	if err != nil {
		s.t.Fatal(err)
	}
	key, _, curve, err := cert.UnmarshalSigningPrivateKeyFromPEM(network.NebulaCAKey)
	if err != nil {
		s.t.Fatal(err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		s.t.Fatal(err)
	}
	c, _, err := cert.UnmarshalCertificateFromPEM(old)
	if err != nil {
		s.t.Fatal(err)
	}
	// Signing refuses a certificate older than its CA, and the test's network
	// was created just now: sign with the CA's key under an older validity.
	nb := time.Now().Add(-age)
	olderCA, err := (&cert.TBSCertificate{Version: ca.Version(), Name: ca.Name(), IsCA: true, PublicKey: ca.PublicKey(), Curve: ca.Curve(),
		NotBefore: nb.Add(-time.Hour), NotAfter: ca.NotAfter()}).Sign(nil, curve, key)
	if err != nil {
		s.t.Fatal(err)
	}
	aged, err := (&cert.TBSCertificate{Version: c.Version(), Name: c.Name(), PublicKey: c.PublicKey(), Curve: c.Curve(),
		Networks: c.Networks(), Groups: c.Groups(), NotBefore: nb, NotAfter: nb.Add(24 * time.Hour)}).Sign(olderCA, curve, key)
	if err != nil {
		s.t.Fatal(err)
	}
	pem, err := aged.MarshalPEM()
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, pem, 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// Client is one caller of the API with its own IP address.
type Client struct {
	T     testing.TB
	URL   string
	IP    string
	Token string // bearer token; "" for an anonymous caller
	ID    string // user ID
	Email string
	// Header is sent with every request (With adds to it).
	Header http.Header
}

// Anonymous returns a caller without a session, from an IP of its own so that
// callers don't share a rate limit.
func (s *Server) Anonymous() *Client {
	n := s.ips.Add(1)
	return &Client{T: s.t, URL: s.URL, IP: fmt.Sprintf("198.18.%d.%d", n>>8, n&0xff)}
}

// Password is every registered user's password.
const Password = "correct horse battery"

// Register creates a user and returns them signed in.
func (s *Server) Register(name string) *Client {
	s.t.Helper()
	c := s.Anonymous()
	var out struct{ ID, Token string }
	c.Email = name + "@example.test"
	c.Want(http.StatusOK, "POST", "/api/auth/register", M{"email": c.Email, "name": name, "password": Password}, &out)
	c.ID, c.Token = out.ID, out.Token
	return c
}

// M is a JSON object.
type M map[string]any

// Do sends a request and returns the status and body. A nil body sends none.
func (c *Client) Do(method, path string, body any) (int, []byte) {
	c.T.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			c.T.Fatal(err)
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.URL+path, r)
	if err != nil {
		c.T.Fatal(err)
	}
	c.Headers(req.Header)
	resp, err := httpClient.Do(req)
	if err != nil {
		c.T.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		c.T.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return resp.StatusCode, out
}

var httpClient = &http.Client{
	Timeout:       60 * time.Second,
	Transport:     &http.Transport{DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Headers sets c.Header and the caller's identity.
func (c *Client) Headers(h http.Header) {
	for k, v := range c.Header {
		h[k] = append([]string(nil), v...)
	}
	h.Set("X-Forwarded-For", c.IP)
	if c.Token != "" {
		h.Set("Authorization", "Bearer "+c.Token)
	}
}

// Want sends a request, requires the status and decodes the body into out
// (unless out is nil).
func (c *Client) Want(status int, method, path string, body, out any) {
	c.T.Helper()
	got, b := c.Do(method, path, body)
	if got != status {
		c.T.Fatalf("%s %s: HTTP %d %s, want %d", method, path, got, strings.TrimSpace(string(b)), status)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.T.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
}

// Network is what creating one returns.
type Network struct {
	ID, Name, Slug, Subnet, DNSDomain string
	LighthousePort                    int
}

// CreateNetwork creates a network owned by c.
func (c *Client) CreateNetwork(name string) Network {
	c.T.Helper()
	var n Network
	c.Want(http.StatusCreated, "POST", "/api/networks", M{"name": name}, &n)
	return n
}

// Join makes user a member of c's network through an invite.
func (c *Client) Join(user *Client, networkID, role string) {
	c.T.Helper()
	var inv struct{ Code string }
	c.Want(http.StatusCreated, "POST", "/api/networks/"+networkID+"/invites", M{"role": role}, &inv)
	user.Want(http.StatusCreated, "POST", "/api/invites/"+inv.Code+"/accept", nil, nil)
}

// Node is one entry of the node list.
type Node struct {
	ID, Hostname, NebulaIP, NodeType, Status string
	OS, Arch                                 string
	LastSeenAt                               *int64
	PeersDirect, PeersRelayed                *int64
	Capabilities                             []string
}

// Nodes lists a network's nodes by ID, without the lighthouse.
func (c *Client) Nodes(networkID string) map[string]Node {
	c.T.Helper()
	var list []Node
	c.Want(http.StatusOK, "GET", "/api/networks/"+networkID+"/nodes", nil, &list)
	out := map[string]Node{}
	for _, n := range list {
		if n.NodeType != "lighthouse" {
			out[n.ID] = n
		}
	}
	return out
}

// Enrollment is what an agent gets for an enrollment token.
type Enrollment struct {
	NodeID, CACert, NodeCert, NodeKey, AgentToken string
	ServerIP, NebulaIP, DNSDomain, LighthouseHost string
	LighthousePort                                int
}

// EnrollToken creates a node and returns its ID and enrollment token.
func (c *Client) EnrollToken(networkID string) (nodeID, token string) {
	c.T.Helper()
	var out struct{ NodeID, EnrollmentToken string }
	c.Want(http.StatusCreated, "POST", "/api/networks/"+networkID+"/nodes", M{}, &out)
	return out.NodeID, out.EnrollmentToken
}

// Enroll enrolls a node as an agent would, without running one.
func (c *Client) Enroll(networkID, hostname string) Enrollment {
	c.T.Helper()
	_, token := c.EnrollToken(networkID)
	var e Enrollment
	anon := *c
	anon.Token = ""
	anon.IP = fmt.Sprintf("198.19.%d.%d", rand.Intn(256), rand.Intn(256)) // a machine of its own
	anon.Want(http.StatusOK, "POST", "/api/enroll", M{"token": token, "hostname": hostname, "os": "linux", "arch": "arm64"}, &e)
	return e
}

// With is c sending one more header with every request.
func (c *Client) With(key, value string) *Client {
	w := *c
	w.Header = c.Header.Clone()
	if w.Header == nil {
		w.Header = http.Header{}
	}
	w.Header.Set(key, value)
	return &w
}

// As is c with another bearer token, as an agent of the household sends it.
func (c *Client) As(token string) *Client {
	a := *c
	a.Token = token
	return &a
}

// Within polls until cond holds.
func Within(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

// Log collects a process's output.
type Log struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *Log) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Tail is the last n lines.
func (l *Log) Tail(n int) string {
	lines := strings.Split(l.String(), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// CheckLog fails the test if the process panicked or the race detector fired.
func CheckLog(t testing.TB, name string, l *Log) {
	t.Helper()
	out := l.String()
	for _, bad := range []string{"WARNING: DATA RACE", "panic:", "fatal error:"} {
		if i := strings.Index(out, bad); i >= 0 {
			end := min(len(out), i+4000)
			t.Errorf("%s: %s", name, out[i:end])
			return
		}
	}
}

func waitOrKill(cmd *exec.Cmd, d time.Duration, late func()) {
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		late()
		cmd.Process.Kill()
		<-done
	}
}

// StopProcess ends an agent process and checks its log.
func StopProcess(t testing.TB, name string, cmd *exec.Cmd, l *Log) {
	t.Helper()
	cmd.Process.Signal(syscall.SIGTERM)
	waitOrKill(cmd, 40*time.Second, func() { t.Errorf("%s did not stop within 40s of SIGTERM", name) })
	CheckLog(t, name, l)
}

// KnownGap documents behaviour that is wrong today and not fixed yet: check
// asserts what it should be, and is expected to fail. Once the gap is fixed
// the test fails until the KnownGap marker is removed, so the list of gaps
// can't go stale.
func KnownGap(t *testing.T, why string, check func(t *Gap)) {
	t.Helper()
	g := &Gap{}
	check(g)
	if len(g.failures) == 0 {
		t.Fatalf("FIXED: %s. Turn this known gap into a normal test.", why)
	}
	t.Logf("KNOWN GAP: %s (%s)", why, strings.Join(g.failures, "; "))
}

// Gap records the failures of a KnownGap check.
type Gap struct{ failures []string }

// Errorf records that the gap is still there.
func (g *Gap) Errorf(format string, args ...any) {
	g.failures = append(g.failures, fmt.Sprintf(format, args...))
}
