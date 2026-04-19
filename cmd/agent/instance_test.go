package main

import (
	"sync"
	"testing"
)

func TestMeshInstance_SignalHeartbeatCoalesces(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	// Fire many signals in a row; the buffered-1 channel should absorb them.
	for i := 0; i < 100; i++ {
		inst.signalHeartbeat()
	}
	// Exactly one pending.
	select {
	case <-inst.heartbeatTrigger:
	default:
		t.Fatal("expected one pending signal")
	}
	select {
	case <-inst.heartbeatTrigger:
		t.Fatal("expected no second pending signal")
	default:
	}
}

func TestMeshInstance_SignalHeartbeatConcurrent(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				inst.signalHeartbeat()
			}
		}()
	}
	wg.Wait()
	// Drain up to 10 — we expect at most one pending, so 1 iteration
	// should succeed and the rest should fall through default.
	drained := 0
	for k := 0; k < 10; k++ {
		select {
		case <-inst.heartbeatTrigger:
			drained++
		default:
		}
	}
	if drained != 1 {
		t.Fatalf("expected exactly 1 pending signal after concurrent writers, got %d", drained)
	}
}

func TestInstanceRegistry_AddGetList(t *testing.T) {
	reg := newInstanceRegistry()

	a := newMeshInstance(&Enrollment{Name: "home"})
	b := newMeshInstance(&Enrollment{Name: "work"})
	reg.add(a)
	reg.add(b)

	if reg.len() != 2 {
		t.Fatalf("len=%d", reg.len())
	}
	if got := reg.get("home"); got != a {
		t.Fatalf("get home: got %p want %p", got, a)
	}
	if got := reg.get("missing"); got != nil {
		t.Fatalf("get missing: got %v", got)
	}

	// list snapshot doesn't expose registry internals.
	snap := reg.list()
	if len(snap) != 2 {
		t.Fatalf("list len=%d", len(snap))
	}
}

func TestInstanceRegistry_CloseAllIsIdempotent(t *testing.T) {
	reg := newInstanceRegistry()
	a := newMeshInstance(&Enrollment{Name: "home"})
	reg.add(a)

	reg.closeAll()
	if reg.len() != 0 {
		t.Fatalf("len after closeAll=%d", reg.len())
	}
	// Second closeAll must not panic.
	reg.closeAll()
}
