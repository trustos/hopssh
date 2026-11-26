package client

import (
	"context"
	"log"
	"time"
)

// A socket the kernel takes nothing from. On a Mac with a content filter every
// send of one network ran into its limit of a second for a full minute
// (2026-11-26, 20:09), and for four minutes before there was a limit: the
// network was dead, the others in the same process were not. Ending the agent
// brought it back in 10 s, so after sendStuckLimit the network gets a new
// socket. docs/wiki/incidents/2026-11-26-mbp-send-stuck-in-kernel.md
const (
	sendStuckLimit = 5 * time.Second
	sendWatchTick  = time.Second
)

func runSendWatch(ctx context.Context, inst *meshInstance) {
	t := time.NewTicker(sendWatchTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if ctrl := inst.control(); ctrl != nil && sendStuck(inst, ctrl.SendStuckFor()) {
			return
		}
	}
}

// sendStuck starts the network anew when its socket has been stuck for the
// limit, and reports whether it did. Like every watchdog it holds back while
// the enrollment was restarted a short while ago.
func sendStuck(inst *meshInstance, stuck time.Duration) bool {
	if stuck < sendStuckLimit || inst.restartFn == nil || inst.restartCoolingDown() {
		return false
	}
	log.Printf("[send-watch %s] the kernel has taken no packet for %s: starting the network anew",
		inst.name(), stuck.Truncate(time.Second))
	if err := inst.restartFn(); err != nil {
		log.Printf("[send-watch %s] restart failed: %v", inst.name(), err)
		return false
	}
	return true
}
