package netlab

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"golang.org/x/sys/unix"

	"github.com/trustos/hopssh/internal/e2etest"
)

// TestMain starts over in namespaces of its own, so that nothing the lab does
// reaches the machine it runs on: networks, processes, /run and (per node) /etc.
// When this process ends, the kernel removes all of it.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv("HOP_NETLAB_NODE") != "":
		nodeMain()
	case os.Getenv("HOP_NETLAB") == "":
		os.Exit(m.Run()) // every test is skipped
	case os.Getenv("HOP_NETLAB_INNER") == "":
		os.Exit(isolate())
	}
	for _, err := range []error{
		unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""),
		unix.Mount("proc", "/proc", "proc", 0, ""),
		unix.Mount("tmpfs", "/run", "tmpfs", 0, ""), // hides systemd and D-Bus
		exec.Command("ip", "link", "set", "lo", "up").Run(),
	} {
		if err != nil {
			fmt.Fprintln(os.Stderr, "netlab: cannot isolate itself:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	printReport()
	os.Exit(code)
}

func isolate() int {
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr, "netlab: needs root")
		return 1
	}
	runtime.LockOSThread() // Pdeathsig follows the thread
	cmd := exec.Command("/proc/self/exe", os.Args[1:]...)
	cmd.Env = append(os.Environ(), "HOP_NETLAB_INNER=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS | unix.CLONE_NEWPID,
		Pdeathsig:  syscall.SIGKILL,
	}
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "netlab:", err)
		return 1
	}
	return 0
}

// nodeMain is a machine of the lab: a process that holds its namespaces open.
func nodeMain() {
	dir := os.Getenv("HOP_NETLAB_DIR")
	fail := func(err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "netlab node:", err)
			os.Exit(1)
		}
	}
	fail(unix.Sethostname([]byte(os.Getenv("HOP_NETLAB_NODE"))))
	fail(unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""))
	fail(unix.Mount("tmpfs", "/run", "tmpfs", 0, ""))
	up, work := filepath.Join(dir, "etc"), filepath.Join(dir, "work")
	fail(os.Mkdir(up, 0o755))
	fail(os.Mkdir(work, 0o755))
	fail(unix.Mount("overlay", "/etc", "overlay", 0, "lowerdir=/etc,upperdir="+up+",workdir="+work))
	fail(os.RemoveAll("/etc/hop-agent"))
	fail(exec.Command("ip", "link", "set", "lo", "up").Run())
	fmt.Println("ready")
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// Lab is one test's own world: an internet, a hop-server on it, and whatever
// routers and machines the test adds.
type Lab struct {
	t      *testing.T
	inet   *Segment
	server *Server
	owner  *e2etest.Client
	net    e2etest.Network
	agents []*Agent
}

const (
	inetPrefix = "203.0.113."
	serverIP   = inetPrefix + "10"
	serverPort = "9473"
	labName    = "lab" // the agents' local name for the network
)

func newLab(t *testing.T) *Lab {
	t.Helper()
	if os.Getenv("HOP_NETLAB") == "" {
		t.Skip("needs root and Linux namespaces: make netlab")
	}
	t.Parallel()
	l := &Lab{t: t}
	inet := l.node("inet")
	inet.sh(`ip link add name sw type bridge; ip link set sw up; ip addr add ` + inetPrefix + `1/24 dev sw; sysctl -qw net.ipv4.ip_forward=1`)
	l.inet = &Segment{node: inet, bridge: "sw"}
	l.startServer()
	return l
}

// Node is a machine: its own network, hostname, /etc and /run.
type Node struct {
	l    *Lab
	Name string
	pid  int
	ns   *os.File
}

func (l *Lab) node(name string) *Node {
	l.t.Helper()
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(), "HOP_NETLAB_NODE="+name, "HOP_NETLAB_DIR="+l.t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS | unix.CLONE_NEWUTS}
	stdin, err := cmd.StdinPipe()
	l.must(err)
	stdout, err := cmd.StdoutPipe()
	l.must(err)
	cmd.Stderr = os.Stderr
	l.must(cmd.Start())
	l.t.Cleanup(func() { stdin.Close(); cmd.Wait() })
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "ready\n" {
		l.t.Fatalf("node %s did not start: %q", name, line)
	}
	ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", cmd.Process.Pid))
	l.must(err)
	l.t.Cleanup(func() { ns.Close() })
	return &Node{l: l, Name: name, pid: cmd.Process.Pid, ns: ns}
}

func (l *Lab) must(err error) {
	l.t.Helper()
	if err != nil {
		l.t.Fatal(err)
	}
}

// cmd is a program on the node.
func (n *Node) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command("nsenter", append([]string{"--target", strconv.Itoa(n.pid), "--net", "--mount", "--uts", "--", name}, args...)...)
	c.Dir = "/"
	c.Env = []string{"PATH=" + os.Getenv("PATH") + ":/usr/sbin:/sbin", "HOME=/root", "SHELL=/bin/sh"}
	return c
}

// sh runs a script on the node, which must succeed.
func (n *Node) sh(script string) string {
	n.l.t.Helper()
	out, err := n.cmd("sh", "-euc", script).CombinedOutput()
	if err != nil {
		n.l.t.Fatalf("%s: %s: %v\n%s", n.Name, script, err, out)
	}
	return string(out)
}

// in runs fn on a thread that is in the node's network; sockets made there
// stay there.
func (n *Node) in(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		if err := unix.Setns(int(n.ns.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- err
			return
		}
		done <- fn()
	}()
	return <-done
}

// Dial connects from the node.
func (n *Node) Dial(network, addr string, timeout time.Duration) (c net.Conn, err error) {
	err = n.in(func() (err error) { c, err = net.DialTimeout(network, addr, timeout); return })
	return
}

// Listen accepts TCP connections on the node.
func (n *Node) Listen(addr string) net.Listener {
	n.l.t.Helper()
	var ln net.Listener
	n.l.must(n.in(func() (err error) { ln, err = net.Listen("tcp4", addr); return }))
	n.l.t.Cleanup(func() { ln.Close() })
	return ln
}

// ListenUDP receives datagrams on the node.
func (n *Node) ListenUDP(addr string) net.PacketConn {
	n.l.t.Helper()
	var pc net.PacketConn
	n.l.must(n.in(func() (err error) { pc, err = net.ListenPacket("udp4", addr); return }))
	n.l.t.Cleanup(func() { pc.Close() })
	return pc
}

// Segment is a network that machines are plugged into: a bridge on a node.
type Segment struct {
	node   *Node
	bridge string
	gw     string // default gateway of the machines on it, "" for none
	ports  int
}

var links atomic.Int32

// plug connects the node to the segment with a new interface.
func (n *Node) plug(ifname, ip string, s *Segment) {
	n.l.t.Helper()
	id := links.Add(1)
	x, y := fmt.Sprintf("v%da", id), fmt.Sprintf("v%db", id)
	port := s.port()
	for _, args := range [][]string{
		{"link", "add", x, "type", "veth", "peer", "name", y},
		{"link", "set", x, "netns", strconv.Itoa(n.pid), "name", ifname},
		{"link", "set", y, "netns", strconv.Itoa(s.node.pid), "name", port},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			n.l.t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	n.attach(ifname, ip, s, port)
}

// plugWire is plug with a wire in between.
func (n *Node) plugWire(w Wire, ifname, ip string, s *Segment) *Cable {
	n.l.t.Helper()
	port := s.port()
	c := n.l.connect(w, n, ifname, s.node, port)
	n.attach(ifname, ip, s, port)
	return c
}

func (s *Segment) port() string { s.ports++; return fmt.Sprintf("p%d", s.ports) }

func (n *Node) attach(ifname, ip string, s *Segment, port string) {
	n.l.t.Helper()
	s.node.sh(fmt.Sprintf("ip link set %s master %s up", port, s.bridge))
	n.configure(ifname, ip, s)
}

func (n *Node) configure(ifname, ip string, s *Segment) {
	n.l.t.Helper()
	script := fmt.Sprintf("ip addr add %s/24 dev %s; ip link set %s up", ip, ifname, ifname)
	if s.gw != "" {
		script += "; ip route replace default via " + s.gw
	}
	n.sh(script)
}

// unplug removes the interface, as when a cable is pulled or WiFi is left.
func (n *Node) unplug(ifname string) { n.l.t.Helper(); n.sh("ip link del " + ifname) }

// host is a machine on a segment.
func (l *Lab) host(name, ip string, s *Segment) *Node {
	l.t.Helper()
	n := l.node(name)
	n.plug("eth0", ip, s)
	return n
}

// NAT is what a router does with the addresses of its machines.
type NAT string

const (
	// Home: the same outside port for every destination, if it is free, and only
	// destinations that were sent something may answer. Every Linux-based home
	// router. Two machines that use the same port are told apart by destination.
	Home NAT = "masquerade"
	// Carrier: a new random outside port for every destination. Mobile networks,
	// many hotels and offices.
	Carrier NAT = "masquerade fully-random"
)

// Router connects a LAN to the internet (or to another router's LAN).
type Router struct {
	*Node
	lan  *Segment
	wan  string
	line *Cable
	zone int
	loop bool
}

// router adds a router with the given address on the outside, and lanIP/24 on
// the inside.
func (l *Lab) router(name, wanIP, lanIP string, nat NAT, outside *Segment) *Router {
	l.t.Helper()
	n := l.node(name)
	n.sh("ip link add name lan type bridge; ip link set lan up; sysctl -qw net.ipv4.ip_forward=1")
	n.sh("ip addr add " + lanIP + "/24 dev lan")
	w := FixedLine
	if nat == Carrier {
		w = Mobile
	}
	line := n.plugWire(w, "wan", wanIP, outside)
	n.sh(fmt.Sprintf(`nft -f - <<'E'
table ip nat {
	chain pre { type nat hook prerouting priority dstnat; }
	chain post { type nat hook postrouting priority srcnat; oifname "wan" %s; }
}
table ip filter {
	chain forward {
		type filter hook forward priority filter;
		ct state established,related accept
		ct status dnat accept
		iifname "wan" drop
	}
	# Packets nobody asked for must not leave a conntrack entry: it would take
	# the outside port away from a machine that is about to punch a hole.
	chain input {
		type filter hook input priority filter;
		ct state established,related accept
		iifname "wan" drop
	}
}
E`, nat))
	return &Router{Node: n, wan: wanIP, line: line, lan: &Segment{node: n, bridge: "lan", gw: lanIP}}
}

// forget drops everything the router knows about connections through it, as a
// reboot does.
func (r *Router) forget() {
	r.l.t.Helper()
	// Few systems have the conntrack tool, and reloading the rules forgets
	// nothing. A new zone does: its table is empty.
	r.zone++
	r.sh(fmt.Sprintf(`nft -f - <<'E'
table ip fresh {
	chain pre { type filter hook prerouting priority raw; }
	chain out { type filter hook output priority raw; }
}
flush table ip fresh
table ip fresh {
	chain pre { ct zone set %d; }
	chain out { ct zone set %d; }
}
E`, r.zone, r.zone))
}

// newAddress gives the router another public address, as after a reconnect.
func (r *Router) newAddress(ip string) {
	r.l.t.Helper()
	// Delete first: with the old address goes every other one of its subnet.
	r.sh(fmt.Sprintf("ip addr del %s/24 dev wan; ip addr add %s/24 dev wan", r.wan, ip))
	r.wan = ip
	r.forget()
}

// natTimeout sets after how many quiet seconds the router forgets a UDP
// connection.
func (r *Router) natTimeout(seconds int) {
	r.l.t.Helper()
	r.sh(fmt.Sprintf("sysctl -qw net.netfilter.nf_conntrack_udp_timeout=%d net.netfilter.nf_conntrack_udp_timeout_stream=%d", seconds, seconds))
}

// firewall adds a rule to what the router forwards, before all others.
func (r *Router) firewall(rule string) {
	r.l.t.Helper()
	r.sh("nft insert rule ip filter forward " + rule)
}

// hairpin makes the router's outside address work from the inside too. Call it
// before natPMP. Routers differ in the address that the machines inside then see:
// the router's own one, or the outside address.
func (r *Router) hairpin(seen string) {
	r.l.t.Helper()
	r.loop = true
	r.sh("nft insert rule ip nat post iifname lan oifname lan ct status dnat snat to " + seen)
}

// natPMP makes the router open ports on request (RFC 6886).
func (r *Router) natPMP() {
	r.l.t.Helper()
	pc := r.ListenUDP(r.lan.gw + ":5351")
	done := make(chan struct{})
	r.l.t.Cleanup(func() { pc.Close(); <-done })
	go func() {
		defer close(done)
		buf := make([]byte, 64)
		taken := map[int]string{} // by whom
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 2 || buf[0] != 0 {
				continue // PCP
			}
			resp := make([]byte, 8, 16)
			resp[1] = buf[1] | 0x80
			switch {
			case buf[1] == 0:
				resp = append(resp, net.ParseIP(r.wan).To4()...)
			case buf[1] == 1 && n >= 12:
				in, out := int(buf[4])<<8|int(buf[5]), int(buf[6])<<8|int(buf[7])
				host := from.(*net.UDPAddr).IP.String()
				if out == 0 {
					out = in
				}
				for taken[out] != "" && taken[out] != host {
					out++
				}
				taken[out] = host
				rule := fmt.Sprintf("add rule ip nat pre iifname wan udp dport %d dnat to %s:%d", out, host, in)
				if r.loop {
					rule = strings.Replace(rule, "iifname wan", "ip daddr "+r.wan, 1)
				}
				if msg, err := r.cmd("nft", rule).CombinedOutput(); err != nil {
					r.l.t.Errorf("%s: nft %s: %v %s", r.Name, rule, err, msg)
				}
				resp = append(resp, buf[4], buf[5], byte(out>>8), byte(out))
				resp = append(resp, buf[8:12]...)
			default:
				continue
			}
			if _, err := pc.WriteTo(resp, from); err != nil && !errors.Is(err, net.ErrClosed) {
				r.l.t.Errorf("%s: NAT-PMP answer to %s: %v", r.Name, from, err)
			}
		}
	}()
}

// Server is the control plane.
type Server struct {
	*Node
	line      *Cable
	data, url string // url is for machines of the lab
	cmd       *exec.Cmd
	log       *e2etest.Log
}

func (l *Lab) startServer() {
	l.t.Helper()
	s := &Server{Node: l.node("server"), data: l.t.TempDir(), url: "http://" + serverIP + ":" + serverPort, log: &e2etest.Log{}}
	s.line = s.plugWire(DataCentre, "eth0", serverIP, l.inet)
	s.sh("ip route replace default via " + inetPrefix + "1")
	l.server = s
	l.t.Cleanup(func() {
		if s.cmd != nil {
			s.stop()
		}
		if l.t.Failed() {
			l.t.Logf("hop-server log:\n%s", s.log.Tail(150))
		}
	})
	s.run()

	// The test process is in no network of the lab: it reaches the API through
	// a forwarder.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	l.must(err)
	l.t.Cleanup(func() { ln.Close() })
	go forward(ln, func() (net.Conn, error) { return s.Dial("tcp", serverIP+":"+serverPort, 5*time.Second) })
	anon := &e2etest.Client{T: l.t, URL: "http://" + ln.Addr().String(), IP: "198.18.0.1"}
	var u struct{ ID, Token string }
	anon.Want(http.StatusOK, "POST", "/api/auth/register", e2etest.M{"email": "owner@example.test", "name": "owner", "password": e2etest.Password}, &u)
	owner := *anon
	owner.ID, owner.Token = u.ID, u.Token
	l.owner = &owner
	l.net = l.owner.CreateNetwork("lab")
}

func (s *Server) run() {
	s.l.t.Helper()
	s.cmd = s.Node.cmd(binary("hop-server"), "--addr", ":"+serverPort, "--data", s.data, "--endpoint", s.url, "--lighthouse-host", serverIP)
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	s.l.must(s.cmd.Start())
	e2etest.Within(s.l.t, 20*time.Second, "hop-server to answer", func() bool {
		c, err := s.Dial("tcp", serverIP+":"+serverPort, time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	})
}

func (s *Server) stop() {
	s.l.t.Helper()
	e2etest.StopProcess(s.l.t, "hop-server", s.cmd, s.log)
	s.cmd = nil
}

// kill ends the server without a goodbye, as a crash does.
func (s *Server) kill() {
	s.l.t.Helper()
	s.l.must(s.cmd.Process.Kill())
	s.cmd.Wait()
	s.cmd = nil
}

func forward(ln net.Listener, dial func() (net.Conn, error)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			up, err := dial()
			if err != nil {
				return
			}
			defer up.Close()
			go io.Copy(up, c)
			io.Copy(c, up)
		}()
	}
}

func binary(name string) string { return filepath.Join(os.Getenv("HOP_NETLAB_BIN"), name) }

// Agent is a machine that joined the network, with the agent running as root
// and a kernel TUN device, configured as `hop-agent install` would.
type Agent struct {
	*Node
	NodeID, MeshIP string
	cmd            *exec.Cmd
	log            *e2etest.Log
	base, token    string
}

// join enrolls the machine with the CLI and starts the agent.
func (l *Lab) join(n *Node) *Agent {
	l.t.Helper()
	a := &Agent{Node: n, log: &e2etest.Log{}}
	id, token := l.owner.EnrollToken(l.net.ID)
	a.NodeID = id
	e := n.cmd(binary("hop-agent"), "enroll", "--token-stdin", "--endpoint", l.server.url, "--no-service", "--name", labName)
	e.Stdin = strings.NewReader(token + "\n")
	if out, err := e.CombinedOutput(); err != nil {
		l.t.Fatalf("%s: enroll: %v\n%s", n.Name, err, out)
	}
	l.t.Cleanup(func() {
		if a.cmd != nil {
			a.stop()
		}
		if l.t.Failed() {
			l.t.Logf("agent log of %s:\n%s", n.Name, a.log.Tail(150))
		}
	})
	a.run()
	var st struct {
		Enrollments []struct{ NebulaIP, MeshIP, TunMode string }
	}
	l.must(a.local("GET", "/local/status", nil, &st))
	if len(st.Enrollments) != 1 || st.Enrollments[0].TunMode != "kernel" {
		l.t.Fatalf("%s: status %+v, want one enrollment with a kernel TUN", n.Name, st)
	}
	a.MeshIP = st.Enrollments[0].NebulaIP
	if a.MeshIP == "" {
		a.MeshIP = st.Enrollments[0].MeshIP
	}
	a.MeshIP, _, _ = strings.Cut(a.MeshIP, "/")
	l.agents = append(l.agents, a)
	return a
}

// joinAll enrolls several machines at once.
func (l *Lab) joinAll(nodes ...*Node) []*Agent {
	l.t.Helper()
	out := make([]*Agent, len(nodes))
	for i, n := range nodes {
		out[i] = l.join(n)
	}
	return out
}

func (a *Agent) run() {
	a.l.t.Helper()
	a.cmd = a.Node.cmd(binary("hop-agent"), "serve")
	a.cmd.Stdout, a.cmd.Stderr = a.log, a.log
	from := len(a.log.String())
	a.l.must(a.cmd.Start())
	const prefix = "HOPSSH_LOCAL_API:"
	e2etest.Within(a.l.t, 2*time.Minute, a.Name+"'s local API", func() bool {
		for _, line := range strings.Split(a.log.String()[from:], "\n") {
			if rest, ok := strings.CutPrefix(line, prefix); ok && strings.Count(rest, ":") == 2 {
				i := strings.LastIndex(rest, ":")
				a.base, a.token = "http://"+rest[:i], strings.TrimSpace(rest[i+1:])
				return true
			}
		}
		return false
	})
}

func (a *Agent) stop() {
	a.l.t.Helper()
	e2etest.StopProcess(a.l.t, a.Name+"'s agent", a.cmd, a.log)
	a.cmd = nil
}

// signal sends a signal to the agent (nsenter replaces itself with it).
func (a *Agent) signal(sig syscall.Signal) {
	a.l.t.Helper()
	a.l.must(a.cmd.Process.Signal(sig))
}

// local calls the agent's local API.
func (a *Agent) local(method, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, a.base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+a.token)
	c := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			return a.Dial(network, addr, 5*time.Second)
		}}}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// path is how a's packets get to b: "direct <address>", "relay" or "none".
func (a *Agent) path(b *Agent) string {
	var out struct {
		Peers []struct {
			VpnAddr, RemoteAddr string
			Direct              bool
		}
	}
	if err := a.local("GET", "/local/peers?enrollment="+labName, nil, &out); err != nil {
		return "unknown: " + err.Error()
	}
	for _, p := range out.Peers {
		if p.VpnAddr == b.MeshIP {
			if p.Direct {
				return "direct " + p.RemoteAddr
			}
			return "relay"
		}
	}
	return "none"
}

// report collects one line per measurement, printed when all tests are done.
var report struct {
	sync.Mutex
	rows []string
}

func (l *Lab) report(format string, args ...any) {
	row := fmt.Sprintf("%-42s %s", strings.TrimPrefix(l.t.Name(), "Test"), fmt.Sprintf(format, args...))
	l.t.Log(row)
	report.Lock()
	report.rows = append(report.rows, row)
	report.Unlock()
}

func printReport() {
	if len(report.rows) > 0 {
		fmt.Printf("\n--- netlab report\n%s\n", strings.Join(report.rows, "\n"))
	}
}
