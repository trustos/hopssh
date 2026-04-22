package portmap

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// atomicAdd wraps atomic.AddInt32 so we can reference it by short name.
func atomicAdd(addr *int32, delta int32) int32 { return atomic.AddInt32(addr, delta) }

// stubClient is a test-only Client with caller-supplied behavior.
type stubClient struct {
	name    string
	delay   time.Duration
	mapFn   func(internalPort uint16) (netip.AddrPort, time.Duration, error)
	unmaps  int32
	mu      sync.Mutex
}

func (s *stubClient) Name() string { return s.name }

func (s *stubClient) Map(ctx context.Context, internalPort uint16) (netip.AddrPort, time.Duration, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return netip.AddrPort{}, 0, ctx.Err()
		}
	}
	return s.mapFn(internalPort)
}

func (s *stubClient) Unmap(ctx context.Context, internalPort uint16) error {
	s.mu.Lock()
	s.unmaps++
	s.mu.Unlock()
	return nil
}

func silentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel)
	return l
}

func TestManager_FirstWinnerKept(t *testing.T) {
	slow := &stubClient{
		name:  "slow",
		delay: 100 * time.Millisecond,
		mapFn: func(p uint16) (netip.AddrPort, time.Duration, error) {
			return netip.AddrPortFrom(netip.MustParseAddr("1.1.1.1"), p), time.Hour, nil
		},
	}
	fast := &stubClient{
		name: "fast",
		mapFn: func(p uint16) (netip.AddrPort, time.Duration, error) {
			return netip.AddrPortFrom(netip.MustParseAddr("2.2.2.2"), p), time.Hour, nil
		},
	}

	m := New(silentLogger(), 4242)
	m.SetClients([]Client{slow, fast})

	var got netip.AddrPort
	changed := make(chan struct{}, 1)
	m.OnChange(func(old, n netip.AddrPort) {
		got = n
		select {
		case changed <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnChange never fired")
	}

	if got.String() != "2.2.2.2:4242" {
		t.Fatalf("winner got %s, want 2.2.2.2:4242", got)
	}

	m.Stop()
	if fast.unmaps != 1 {
		t.Errorf("fast.unmaps = %d, want 1", fast.unmaps)
	}
	if slow.unmaps != 0 {
		t.Errorf("slow.unmaps = %d, want 0 (non-winner should not be unmapped)", slow.unmaps)
	}
}

func TestManager_NoProtocolSucceeds(t *testing.T) {
	fail := &stubClient{
		name: "fail",
		mapFn: func(p uint16) (netip.AddrPort, time.Duration, error) {
			return netip.AddrPort{}, 0, errors.New("nope")
		},
	}

	m := New(silentLogger(), 4242)
	m.SetClients([]Client{fail})
	m.probeTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the probe a moment to complete.
	time.Sleep(400 * time.Millisecond)

	if current := m.Current(); current.IsValid() {
		t.Errorf("expected zero AddrPort on total failure, got %v", current)
	}

	m.Stop()
	// No unmap should have been attempted since no winner was picked.
	if fail.unmaps != 0 {
		t.Errorf("fail.unmaps = %d, want 0", fail.unmaps)
	}
}

func TestManager_StopBeforeStart(t *testing.T) {
	m := New(silentLogger(), 4242)
	m.Stop() // should not panic
}

func TestManager_DoubleStartErrors(t *testing.T) {
	m := New(silentLogger(), 4242)
	m.SetClients([]Client{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := m.Start(ctx); err == nil {
		t.Fatal("expected error on second Start")
	}
}

// TestManager_RecoversAfterInitialFailure covers the v0.10.11 bug:
// pre-fix, a single startup probe failure parked the goroutine at
// <-ctx.Done() forever. Now probes retry on a backoff. Simulate by
// returning errors for the first two calls, then a success — the
// Manager must end up with a valid mapping.
func TestManager_RecoversAfterInitialFailure(t *testing.T) {
	var calls int32
	client := &stubClient{
		name: "flaky",
		mapFn: func(p uint16) (netip.AddrPort, time.Duration, error) {
			c := atomicAddInt32(&flakyCalls, 1)
			if c <= 2 {
				return netip.AddrPort{}, 0, errors.New("nope")
			}
			return netip.AddrPortFrom(netip.MustParseAddr("3.3.3.3"), p), time.Hour, nil
		},
	}
	_ = calls

	m := New(silentLogger(), 4242)
	m.SetClients([]Client{client})
	m.probeTimeout = 50 * time.Millisecond
	// Tight backoff so the test runs fast. Retry sequence: 50ms, 50ms.
	m.retryBackoff = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}

	changed := make(chan netip.AddrPort, 1)
	m.OnChange(func(_, cur netip.AddrPort) {
		select {
		case changed <- cur:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case got := <-changed:
		if got.String() != "3.3.3.3:4242" {
			t.Fatalf("recovered mapping = %s, want 3.3.3.3:4242", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("portmap never recovered after retries — retry loop is broken")
	}

	m.Stop()
}

// flakyCalls is module-level so the stubClient closure doesn't have to
// capture a *int32 shared with the test goroutine — avoids a data race
// warning under -race when the test exits while the Manager may still
// be mid-Stop.
var flakyCalls int32

// atomicAddInt32 is a tiny helper to keep imports minimal — tests in
// this package already pull in "sync" but not sync/atomic.
func atomicAddInt32(addr *int32, delta int32) int32 {
	return atomicAdd(addr, delta)
}

// TestManager_ReProbeDropsStaleMapping covers the watchNetworkChanges
// hook: on a WiFi→cellular hop the caller invokes ReProbe, which the
// refresh loop must observe promptly, drop the current mapping, and
// run a fresh probe. Here the second probe picks a different address,
// confirming the Manager is actually re-running the protocol selection
// rather than reusing the old winner.
func TestManager_ReProbeDropsStaleMapping(t *testing.T) {
	var idx int32
	addrs := []netip.AddrPort{
		netip.AddrPortFrom(netip.MustParseAddr("1.1.1.1"), 4242),
		netip.AddrPortFrom(netip.MustParseAddr("2.2.2.2"), 4242),
	}
	client := &stubClient{
		name: "addr-rotator",
		mapFn: func(p uint16) (netip.AddrPort, time.Duration, error) {
			i := atomicAdd(&idx, 1) - 1
			if int(i) >= len(addrs) {
				return addrs[len(addrs)-1], time.Hour, nil
			}
			return addrs[i], time.Hour, nil
		},
	}

	m := New(silentLogger(), 4242)
	m.SetClients([]Client{client})
	m.probeTimeout = 50 * time.Millisecond

	got := make(chan netip.AddrPort, 4)
	m.OnChange(func(_, cur netip.AddrPort) {
		select {
		case got <- cur:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First OnChange = first probe result.
	select {
	case first := <-got:
		if first.String() != "1.1.1.1:4242" {
			t.Fatalf("initial probe = %s, want 1.1.1.1:4242", first)
		}
	case <-time.After(time.Second):
		t.Fatal("initial probe never fired")
	}

	// Trigger re-probe.
	m.ReProbe()

	select {
	case second := <-got:
		if second.String() != "2.2.2.2:4242" {
			t.Fatalf("re-probe result = %s, want 2.2.2.2:4242", second)
		}
	case <-time.After(time.Second):
		t.Fatal("ReProbe never caused a fresh probe — signal is not observed")
	}

	m.Stop()
}

// TestManager_ReProbeCollapsesMultipleSignals — calling ReProbe many
// times in rapid succession must collapse into ONE re-probe (the
// channel is buffered 1). Otherwise a flapping network could backlog
// dozens of probes.
func TestManager_ReProbeCollapsesMultipleSignals(t *testing.T) {
	m := New(silentLogger(), 4242)
	// No Start — just exercise the non-blocking signal mechanic.
	for i := 0; i < 1000; i++ {
		m.ReProbe() // must not block, must not panic
	}
	if got := len(m.reprobe); got != 1 {
		t.Fatalf("reprobe channel len = %d after 1000 ReProbe calls, want 1 (coalesced)", got)
	}
}
