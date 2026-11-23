package e2etest

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"
)

// Peer is another device of the network, in the test process: real Nebula with
// the certificate the server issued, but no agent.
type Peer struct{ svc *service.Service }

// StartPeer joins the mesh as e. The peer knows only the lighthouse, so it has
// to discover other nodes the way an agent does.
func StartPeer(t testing.TB, e Enrollment) *Peer {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"ca.crt": e.CACert, "node.crt": e.NodeCert, "node.key": e.NodeKey} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var cfg config.C
	if err := cfg.LoadString(fmt.Sprintf(`
pki: {ca: %[1]s/ca.crt, cert: %[1]s/node.crt, key: %[1]s/node.key}
static_host_map: {%[2]q: ["127.0.0.1:%[3]d"]}
lighthouse: {am_lighthouse: false, hosts: [%[2]q]}
cipher: aes
listen: {host: 0.0.0.0, port: 0}
tun: {user: true}
firewall:
  outbound: [{port: any, proto: any, host: any}]
  inbound: [{port: any, proto: any, host: any}]
`, dir, e.ServerIP, e.LighthousePort)); err != nil {
		t.Fatal(err)
	}
	l := logrus.New()
	l.SetLevel(logrus.ErrorLevel)
	ctrl, err := nebula.Main(&cfg, false, "e2e", l, overlay.NewUserDeviceFromConfig)
	if err != nil {
		t.Fatalf("start the peer's Nebula: %v", err)
	}
	svc, err := service.New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return &Peer{svc}
}

// Listen accepts TCP connections on a mesh port.
func (p *Peer) Listen(t testing.TB, port int) net.Listener {
	t.Helper()
	l, err := p.svc.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	once := &onceListener{Listener: l}
	t.Cleanup(func() { once.Close() })
	return once
}

// onceListener: Nebula's listener panics when it is closed a second time.
type onceListener struct {
	net.Listener
	once sync.Once
}

func (l *onceListener) Close() (err error) {
	l.once.Do(func() { err = l.Listener.Close() })
	return err
}

// DialContext dials a mesh address.
func (p *Peer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return p.svc.DialContext(ctx, network, addr)
}
