-- Persistent activity log for network-level events (separate from
-- audit_log which captures user actions). Populated alongside every
-- EventHub.Publish call on the control plane. Enables the dashboard's
-- Activity tab to survive page refresh + support search, filter, and
-- time-range drill-down for post-hoc debugging.
--
-- node.status is a high-volume event type (every 60s heartbeat). To
-- keep write rate bounded, persistence is selective: only actual
-- transitions (offline→online or online→offline) are recorded.
-- Other event types (rare: enrolled, renamed, capabilities, deleted,
-- dns.changed, member.changed) are always persisted.
--
-- `details` stores a compact JSON payload with event-specific fields
-- (new hostname, capability list, etc.). `target_id` is the node ID
-- when applicable; NULL otherwise.
CREATE TABLE network_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    target_id TEXT,
    status TEXT,
    details TEXT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX idx_network_events_network_time ON network_events(network_id, created_at DESC);
CREATE INDEX idx_network_events_type ON network_events(network_id, event_type, created_at DESC);
