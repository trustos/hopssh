-- Teams and invites: network membership + invite codes.

CREATE TABLE network_members (
    id TEXT PRIMARY KEY,
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id),
    role TEXT NOT NULL DEFAULT 'member',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE(network_id, user_id)
);

CREATE TABLE network_invites (
    id TEXT PRIMARY KEY,
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL REFERENCES users(id),
    code TEXT NOT NULL UNIQUE,
    role TEXT NOT NULL DEFAULT 'member',
    max_uses INTEGER,
    use_count INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX idx_members_network ON network_members(network_id);
CREATE INDEX idx_members_user ON network_members(user_id);
CREATE INDEX idx_invites_code ON network_invites(code);

-- Backfill: existing network owners become admin members.
INSERT INTO network_members (id, network_id, user_id, role)
SELECT id || '-owner', id, user_id, 'admin' FROM networks;
