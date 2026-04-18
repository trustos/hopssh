package db

import (
	"context"
	"log"
	"time"
)

// statusSweepInterval is how often the sweeper scans for stale online
// nodes. 30s gives ~3min + one sweep-cycle of display latency before
// the "offline" row shows up in the Activity log, while matching the
// client-side stale-detection cadence (the dashboard's 1-second ticker
// already flips the display at the 180s boundary via displayStatus;
// the sweeper is about persisting the transition event, not about
// frontend freshness).
const statusSweepInterval = 30 * time.Second

// StatusEventEmitter is the narrow interface the sweeper needs — set
// on start-up so the sweeper can record offline transitions into the
// activity log. Defined in this package to avoid an import cycle with
// internal/api.
type StatusEventEmitter interface {
	Record(networkID, eventType string, targetID, status, details *string)
}

// StartStatusTransitionSweeper scans for nodes still marked "online"
// whose last_seen_at is older than (now - stale). For each, flips the
// DB row to "offline" + emits a node.status offline event to the
// network_events log (via emitter, if non-nil).
//
// stale must be ≥ the server-side nodeStaleThreshold (3 min) — using
// the same value keeps the dashboard and the persisted log in sync:
// as soon as the client-side displayStatus flips a node to "offline",
// the next sweep persists the transition.
//
// Runs until ctx is cancelled.
func (s *NodeStore) StartStatusTransitionSweeper(ctx context.Context, emitter StatusEventEmitter, stale time.Duration) {
	go func() {
		ticker := time.NewTicker(statusSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runStatusSweep(emitter, stale)
			}
		}
	}()
}

func (s *NodeStore) runStatusSweep(emitter StatusEventEmitter, stale time.Duration) {
	cutoff := time.Now().Add(-stale).Unix()
	stales, err := s.StaleOnlineNodes(cutoff)
	if err != nil {
		log.Printf("[sweep] scan stale nodes: %v", err)
		return
	}
	for _, n := range stales {
		transitioned := s.MarkOfflineOnStale(n.ID)
		if !transitioned || emitter == nil {
			continue
		}
		status := "offline"
		targetID := n.ID
		emitter.Record(n.NetworkID, "node.status", &targetID, &status, nil)
	}
}
