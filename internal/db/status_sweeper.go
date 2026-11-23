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
// nodes older than pruneAge.
const pruneSweepInterval = 1 * time.Hour

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

	// Offline-node prune sweeper (hourly, only if configured). Independent
	// goroutine; tick rate decoupled from the status sweeper so prune runs
	// are visible in logs as distinct events.
	pruneAge := pruneAgeDuration()
	if pruneAge <= 0 {
		return
	}
	log.Printf("[sweep] deleting nodes that have been offline for %v", pruneAge)
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

// pruneAgeDuration returns HOPSSH_NODE_PRUNE_AGE_HOURS, or 0: keep every
// node until someone deletes it. A pruned node can never renew its cert
// again; the only way back is to enroll it anew. From v0.10.51 to v0.11.26
// the default was 7 days, which locked out every machine that was off for a
// week (incident 2026-11-18).
func pruneAgeDuration() time.Duration {
	n, err := strconv.Atoi(os.Getenv("HOPSSH_NODE_PRUNE_AGE_HOURS"))
	if err != nil {
		return 0
	}
	return time.Duration(n) * time.Hour
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
