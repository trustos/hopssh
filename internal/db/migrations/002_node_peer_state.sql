-- Per-node peer connectivity: how many peers this node is currently
-- reaching directly (P2P) vs. via a relay. Reported through the
-- existing heartbeat (5-min cadence). All columns nullable so older
-- agents (which don't report these) keep working unchanged.
ALTER TABLE nodes ADD COLUMN peers_direct INTEGER;
ALTER TABLE nodes ADD COLUMN peers_relayed INTEGER;
ALTER TABLE nodes ADD COLUMN peers_reported_at INTEGER;
