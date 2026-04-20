package portmap

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

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
