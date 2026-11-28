package e2etest

import (
	"testing"
	"time"
)

// withDeadline is a test with go test's -timeout deadline at end.
type withDeadline struct {
	testing.TB
	end time.Time
}

func (d withDeadline) Deadline() (time.Time, bool) { return d.end, !d.end.IsZero() }

// A test can wait for its turn until go test's -timeout is almost over: a
// package started beside others may wait for their whole run. Until
// 2026-11-28 it waited 25 minutes whatever the timeout, so a run with a
// longer one failed while it could still have had its turn.
func TestTurnDeadline(t *testing.T) {
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		end  time.Time
		want time.Time
	}{
		{"-timeout 60m", now.Add(60 * time.Minute), now.Add(59 * time.Minute)},
		{"-timeout 10m", now.Add(10 * time.Minute), now.Add(9 * time.Minute)},
		{"-timeout 0", time.Time{}, now.Add(time.Hour)},
	} {
		if got := turnDeadline(withDeadline{end: c.end}, now); !got.Equal(c.want) {
			t.Errorf("%s: waits until %v, want %v", c.name, got.Sub(now), c.want.Sub(now))
		}
	}
	// What the tests pass in: a real *testing.T.
	if end, ok := t.Deadline(); ok {
		if got := turnDeadline(t, now); !got.Equal(end.Add(-time.Minute)) {
			t.Errorf("with this test's own -timeout: waits until %v, want %v", got, end.Add(-time.Minute))
		}
	}
}
