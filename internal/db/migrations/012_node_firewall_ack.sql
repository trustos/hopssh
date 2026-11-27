-- What each device reports about the firewall policy of its network (phase 2
-- of docs/wiki/concepts/firewall-rules.md). The heartbeat answer of a
-- managed network carries the policy; the device's next heartbeat says the
-- fingerprint it runs (empty for the block it was enrolled with) and, when
-- it refused the last policy, why. A device that never reports has an agent
-- too old to apply rules.
CREATE TABLE node_firewall (
    node_id      TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    fingerprint  TEXT NOT NULL,
    error        TEXT NOT NULL DEFAULT '',
    reported_at  INTEGER NOT NULL           -- when this report was first made
);
