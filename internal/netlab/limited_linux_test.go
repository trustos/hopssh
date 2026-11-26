package netlab

import (
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// push sends from the node to addr for the given time and says how fast it
// went and how many of the TCP segments had to be sent again.
func push(n *Node, addr string, d time.Duration) (mbit float64, sent, again uint32) {
	n.l.t.Helper()
	c, err := n.Dial("tcp", addr, 10*time.Second)
	n.l.must(err)
	defer c.Close()
	c.SetDeadline(time.Now().Add(d))
	start := time.Now()
	io.Copy(c, zeroes{})
	raw, err := c.(*net.TCPConn).SyscallConn()
	n.l.must(err)
	var info *unix.TCPInfo
	raw.Control(func(fd uintptr) { info, err = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO) })
	n.l.must(err)
	return float64(info.Bytes_acked) * 8 / 1e6 / time.Since(start).Seconds(), info.Data_segs_out, info.Total_retrans
}

// sink accepts connections on the node and throws away what comes.
func sink(n *Node, addr string) string {
	ln := n.Listen(addr)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(io.Discard, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// A line that carries less than the machines can send, as every line does. A TCP
// sender goes faster until something is lost, so some loss belongs to the line:
// the more the deeper its queue, and nearly all of it in the first seconds. The
// tunnel must not add to it, and must not be much slower than a connection beside
// it on the same line.
//
// Between two Macs 1 to 13 % of a short upload was sent again, which looked like
// a fault of ours. It is this: docs/wiki/concepts/loss-on-a-limited-line.md.
func TestLimitedLine(t *testing.T) {
	for _, line := range []struct {
		name string
		wire Wire
	}{
		{"deep", Wire{Delay: 8 * time.Millisecond, Rate: 20e6}},
		{"100ms", Wire{Delay: 8 * time.Millisecond, Rate: 20e6, Queue: 100 * time.Millisecond}},
		{"20ms", Wire{Delay: 8 * time.Millisecond, Rate: 20e6, Queue: 20 * time.Millisecond}},
		{"5ms", Wire{Delay: 8 * time.Millisecond, Rate: 20e6, Queue: 5 * time.Millisecond}},
		{"mobile", Wire{Delay: 25 * time.Millisecond, Jitter: 10 * time.Millisecond, Rate: 10e6, Queue: 50 * time.Millisecond}},
	} {
		t.Run(line.name, func(t *testing.T) {
			l := newLab(t)
			a, b, ra, _ := l.twoHomes(Home, Home)
			beside := l.host("beside", inetPrefix+"40", l.inet)
			tcp(a, b, time.Minute)
			want(a, b, "direct", 2*time.Minute)
			ra.line.set(line.wire)
			// Reno goes on doubling until something is lost, as the Macs were seen to.
			a.sh("sysctl -qw net.ipv4.tcp_congestion_control=reno")
			type sum struct {
				mbit  float64
				again uint32
			}
			var tunnel, plain sum
			for range 2 {
				for _, to := range []struct {
					what string
					addr string
					sum  *sum
				}{
					{"tunnel", sink(b.Node, b.MeshIP+":0"), &tunnel},
					{"beside", sink(beside, inetPrefix+"40:0"), &plain},
				} {
					before := ra.line.full.Load()
					mbit, sent, again := push(a.Node, to.addr, 15*time.Second)
					l.report("%s: %.1f Mbit/s, %d of %d segments sent again (%.2f %%), %d frames lost on the line",
						to.what, mbit, again, sent, 100*float64(again)/float64(max(sent, 1)), ra.line.full.Load()-before)
					to.sum.mbit += mbit
					to.sum.again += again
					time.Sleep(2 * time.Second)
				}
			}
			if tunnel.again > 2*plain.again+50 {
				t.Errorf("%d segments sent again through the tunnel, %d beside it", tunnel.again, plain.again)
			}
			if tunnel.mbit < 0.7*plain.mbit {
				t.Errorf("%.1f Mbit/s through the tunnel, %.1f beside it", tunnel.mbit/2, plain.mbit/2)
			}
		})
	}
}
