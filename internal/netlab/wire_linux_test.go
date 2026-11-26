package netlab

import (
	"fmt"
	"math/rand"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Wire is what a packet goes through between a machine and the network it is
// plugged into, in each direction. It can be changed while in use.
type Wire struct {
	Delay  time.Duration
	Jitter time.Duration // added at random, without reordering
	Loss   float64       // 0 to 1
	Rate   int           // bit/s, 0 for unlimited; what exceeds it waits in a queue
	Queue  time.Duration // how much of the line the queue holds, 200 ms if 0; what does not fit is lost
}

var (
	// Typical one-way times: the server is in a data centre, a home is on a fixed
	// line, a phone on a mobile network.
	DataCentre = Wire{Delay: 5 * time.Millisecond}
	FixedLine  = Wire{Delay: 8 * time.Millisecond}
	Mobile     = Wire{Delay: 25 * time.Millisecond, Jitter: 10 * time.Millisecond}
)

// Cable is a wire in use. It is two TAP devices with this process in between,
// because the kernel's own emulation (netem) is missing in many VMs.
type Cable struct {
	wire atomic.Pointer[Wire]
	cut  atomic.Bool
	full atomic.Int64 // frames lost because the queue was full
}

func (c *Cable) set(w Wire) { c.wire.Store(&w) }

// connect gives x and y an interface each, joined by a cable.
func (l *Lab) connect(w Wire, x *Node, xname string, y *Node, yname string) *Cable {
	l.t.Helper()
	c := &Cable{}
	c.set(w)
	a, b := x.tap(xname), y.tap(yname)
	go c.run(a, b)
	go c.run(b, a)
	return c
}

func (n *Node) tap(name string) *os.File {
	n.l.t.Helper()
	var f *os.File
	n.l.must(n.in(func() error {
		fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		req, err := unix.NewIfreq(name)
		if err != nil {
			return err
		}
		req.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
		if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, req); err != nil {
			return fmt.Errorf("%s: %s: %w", n.Name, name, err)
		}
		f = os.NewFile(uintptr(fd), name)
		return nil
	}))
	n.l.t.Cleanup(func() { f.Close() })
	return f
}

func (c *Cable) run(from, to *os.File) {
	type frame struct {
		due time.Time
		b   []byte
	}
	queue := make(chan frame, 20000)
	defer close(queue)
	go func() {
		for f := range queue {
			time.Sleep(time.Until(f.due))
			to.Write(f.b) // fails while the other end is down
		}
	}()
	buf := make([]byte, 65536)
	var sent, last time.Time // when the line is free again; when the last frame arrives
	for {
		n, err := from.Read(buf)
		if err != nil {
			return
		}
		w := c.wire.Load()
		now := time.Now()
		if c.cut.Load() || rand.Float64() < w.Loss {
			continue
		}
		if w.Rate > 0 {
			if sent.Before(now) {
				sent = now
			}
			queue := w.Queue
			if queue == 0 {
				queue = 200 * time.Millisecond
			}
			if sent.Sub(now) > queue {
				c.full.Add(1)
				continue
			}
			sent = sent.Add(time.Duration(n) * 8 * time.Second / time.Duration(w.Rate))
			now = sent
		}
		due := now.Add(w.Delay + time.Duration(rand.Float64()*float64(w.Jitter)))
		if due.Before(last) {
			due = last
		}
		last = due
		select {
		case queue <- frame{due, append([]byte(nil), buf[:n]...)}:
		default:
		}
	}
}
