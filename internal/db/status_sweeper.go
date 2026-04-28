package db

import (
	"context"
	"log"
	"os"
	"strconv"
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

// pruneSweepInterval is how often the sweeper hard-deletes offline
// nodes older than pruneAge. Hourly is conservative — most users
// re-enroll within minutes, so the in-DB ghost is short-lived; this
// is for the long-tail cleanup so the topology doesn't accumulate
// rows from machines that won't come back.
const pruneSweepInterval = 1 * time.Hour

// pruneAge is the default age threshold for offline-node pruning.
// 7 days is conservative — well past "I'm on vacation" but well
// under "this machine is gone forever". Operators can override via
// HOPSSH_NODE_PRUNE_AGE_HOURS env var. 0 disables pruning entirely.
const pruneAgeDefaultHours = 24 * 7

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
	// Status transitions sweeper (every 30s).
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

	// Offline-node prune sweeper (hourly). Hard-deletes nodes that
	// have been offline longer than pruneAge so the topology graph
	// doesn't accumulate gray ghosts from re-enrolled-and-never-
	// returning machines. Independent goroutine; tick rate decoupled
	// from the status sweeper so prune runs are visible in logs as
	// distinct events.
	pruneAge := pruneAgeDuration()
	if pruneAge <= 0 {
		log.Printf("[sweep] auto-prune disabled (HOPSSH_NODE_PRUNE_AGE_HOURS=0)")
		return
	}
	go func() {
		ticker := time.NewTicker(pruneSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runPruneSweep(pruneAge)
			}
		}
	}()
}

// runPruneSweep is the body of the hourly prune tick. Logs the count
// when non-zero so operators see when zombie cleanup happens.
func (s *NodeStore) runPruneSweep(age time.Duration) {
	cutoff := time.Now().Add(-age).Unix()
	n, err := s.PruneOfflineSinceAllNetworks(cutoff)
	if err != nil {
		log.Printf("[sweep] prune offline nodes: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[sweep] pruned %d offline node(s) older than %v", n, age)
	}
}

// pruneAgeDuration returns the configured prune age, defaulting to 7
// days. Override via HOPSSH_NODE_PRUNE_AGE_HOURS=N (0 = disable).
func pruneAgeDuration() time.Duration {
	if v := os.Getenv("HOPSSH_NODE_PRUNE_AGE_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Hour
		}
	}
	return time.Duration(pruneAgeDefaultHours) * time.Hour
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
