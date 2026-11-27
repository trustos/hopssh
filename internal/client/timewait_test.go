package client

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// TestTimeWaitReopen: patch gvisor-time-wait without Nebula and without
// chance. Two gVisor stacks; the server accepts through a Forwarder, as
// Nebula's userspace service does, and closes first, so it keeps the 4-tuple
// in TIME_WAIT. The client then connects again from the same port. Without
// the patch that SYN and every resend are dropped for the 60 s of the
// TIME_WAIT. It runs under -race, where the tests through Nebula skip
// themselves (TestE2E_ConnectAgainAfterTimeWait).
func TestTimeWaitReopen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		acceptOnce bool // the server refuses (RST) every connection after the first
	}{
		{"server listens", false},
		{"server listens no more", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srvAddr := tcpip.AddrFrom4([4]byte{10, 99, 0, 1})
			cliAddr := tcpip.AddrFrom4([4]byte{10, 99, 0, 2})
			srv, srvLink := timeWaitStack(t, srvAddr)
			cli, cliLink := timeWaitStack(t, cliAddr)
			ctx, cancel := context.WithCancel(context.Background())
			var pumps sync.WaitGroup
			pumps.Add(2)
			go func() { defer pumps.Done(); timeWaitPump(ctx, srvLink, cliLink) }()
			go func() { defer pumps.Done(); timeWaitPump(ctx, cliLink, srvLink) }()
			t.Cleanup(func() { cancel(); pumps.Wait() })

			var accepted atomic.Int32
			fwd := tcp.NewForwarder(srv, 0, 16, func(r *tcp.ForwarderRequest) {
				if tc.acceptOnce && accepted.Load() > 0 {
					r.Complete(true) // what Nebula's service does where nobody listens
					return
				}
				var wq waiter.Queue
				ep, err := r.CreateEndpoint(&wq)
				if err != nil {
					r.Complete(true)
					return
				}
				r.Complete(false)
				accepted.Add(1)
				c := gonet.NewTCPConn(&wq, ep)
				c.Write([]byte{1})
				c.Close() // first: the TIME_WAIT is the server's
			})
			srv.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)

			local := tcpip.FullAddress{NIC: 1, Addr: cliAddr, Port: 40000}
			remote := tcpip.FullAddress{NIC: 1, Addr: srvAddr, Port: 7000}
			connect := func(timeout time.Duration) error {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				c, err := gonet.DialTCPWithBind(ctx, cli, local, remote, ipv4.ProtocolNumber)
				if err != nil {
					return err
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(timeout))
				_, err = io.ReadAll(c) // until the server's FIN, so the client closes second
				return err
			}
			if err := connect(5 * time.Second); err != nil {
				t.Fatalf("first connection: %v", err)
			}
			within(t, 5*time.Second, "the 4-tuple in the server's TIME_WAIT and the client's port free", func() bool {
				return timeWaitHeld(srv) && timeWaitPortFree(cli, local)
			})

			t0 := time.Now()
			err := connect(3 * time.Second)
			took := time.Since(t0)
			switch {
			case !tc.acceptOnce && err != nil:
				t.Fatalf("second connection from the same port: %v after %v, want it accepted", err, took.Round(time.Millisecond))
			case tc.acceptOnce && (err == nil || !strings.Contains(err.Error(), "refused")):
				t.Fatalf("second connection from the same port: %v after %v, want it refused", err, took.Round(time.Millisecond))
			case !raceEnabled && took > 500*time.Millisecond:
				// A SYN dropped once and resent after gVisor's 1 s shows here.
				t.Fatalf("second connection from the same port: %v after %v, want an answer to the first SYN", err, took.Round(time.Millisecond))
			}
		})
	}
}

// timeWaitStack is a gVisor stack with one address, set up as Nebula's
// service sets up its own, minus UDP and ICMP.
func timeWaitStack(t *testing.T, addr tcpip.Address) (*stack.Stack, *channel.Endpoint) {
	t.Helper()
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	t.Cleanup(func() { s.Close(); s.Wait() })
	link := channel.New(64, 1280, "")
	if err := s.CreateNIC(1, link); err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	pa := tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: addr.WithPrefix()}
	if err := s.AddProtocolAddress(1, pa, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	return s, link
}

// timeWaitPump hands every packet from one stack to the other.
func timeWaitPump(ctx context.Context, from, to *channel.Endpoint) {
	for {
		pkt := from.ReadContext(ctx)
		if pkt == nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		v := pkt.ToView()
		pkt.DecRef()
		in := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(v.AsSlice())})
		v.Release()
		to.InjectInbound(ipv4.ProtocolNumber, in)
		in.DecRef()
	}
}

// timeWaitHeld: the stack has an endpoint in TIME_WAIT.
func timeWaitHeld(s *stack.Stack) bool {
	for _, e := range s.RegisteredEndpoints() {
		if ep, ok := e.(*tcp.Endpoint); ok && tcp.EndpointState(ep.State()) == tcp.StateTimeWait {
			return true
		}
	}
	return false
}

// timeWaitPortFree: the client's last connection from addr is closed.
func timeWaitPortFree(s *stack.Stack, addr tcpip.FullAddress) bool {
	var wq waiter.Queue
	ep, err := s.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if err != nil {
		return false
	}
	defer ep.Close()
	return ep.Bind(addr) == nil
}
