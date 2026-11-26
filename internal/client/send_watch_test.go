package client

import (
	"errors"
	"testing"
	"time"
)

func TestSendStuck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stuck   time.Duration
		cooling bool
		fail    bool
		calls   int
		want    bool
	}{
		{"sends pass", 0, false, false, 0, false},
		{"stuck for less than the limit", sendStuckLimit - time.Second, false, false, 0, false},
		{"stuck for the limit", sendStuckLimit, false, false, 1, true},
		{"stuck, restarted a short while ago", time.Minute, true, false, 0, false},
		{"stuck, the restart fails", time.Minute, false, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := newMeshInstance(&Enrollment{Name: "home"})
			calls := 0
			inst.restartFn = func() error {
				calls++
				if tc.fail {
					return errors.New("no")
				}
				return nil
			}
			if tc.cooling {
				inst.restartNotBefore = time.Now().Add(time.Minute)
			}
			if got := sendStuck(inst, tc.stuck); got != tc.want || calls != tc.calls {
				t.Errorf("restarted %v with %d calls, want %v with %d", got, calls, tc.want, tc.calls)
			}
		})
	}
}
