CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    github_id TEXT UNIQUE,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE sessions (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    expires_at INTEGER NOT NULL
);

CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    last_used_at INTEGER,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE networks (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    nebula_ca_cert BLOB,
    nebula_ca_key BLOB,
    nebula_subnet TEXT UNIQUE,
    server_cert BLOB,
    server_key BLOB,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE nodes (
    id TEXT PRIMARY KEY,
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    hostname TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    nebula_cert BLOB,
    nebula_key BLOB,
    nebula_ip TEXT,
    agent_token TEXT NOT NULL,
    enrollment_token TEXT UNIQUE,
    enrollment_expires_at INTEGER,      -- TTL for enrollment token
    agent_real_ip TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    last_seen_at INTEGER,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Device authorization flow (RFC 8628) for interactive enrollment.
CREATE TABLE device_codes (
    device_code TEXT PRIMARY KEY,       -- crypto-random, used by agent to poll
    user_code TEXT NOT NULL UNIQUE,     -- short human-readable code (e.g. HOP-K9M2)
    user_id TEXT,                       -- set when user authorizes in browser
    network_id TEXT,                    -- set when user selects network
    node_id TEXT,                       -- set after enrollment completes
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, authorized, completed, expired
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Enrollment bundles for air-gapped/offline installs.
CREATE TABLE enrollment_bundles (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    download_token TEXT NOT NULL UNIQUE, -- crypto-random URL token
    downloaded INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    node_id TEXT,
    network_id TEXT,
    action TEXT NOT NULL,
    details TEXT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE INDEX idx_networks_user ON networks(user_id);
CREATE INDEX idx_nodes_network ON nodes(network_id);
CREATE UNIQUE INDEX idx_nodes_nebula_ip ON nodes(network_id, nebula_ip);
CREATE INDEX idx_nodes_enrollment ON nodes(enrollment_token);
CREATE INDEX idx_device_codes_user_code ON device_codes(user_code);
CREATE INDEX idx_device_codes_expires ON device_codes(expires_at);
CREATE INDEX idx_bundles_token ON enrollment_bundles(download_token);
CREATE INDEX idx_bundles_expires ON enrollment_bundles(expires_at);
CREATE INDEX idx_audit_user ON audit_log(user_id);
CREATE INDEX idx_audit_network ON audit_log(network_id);
CREATE INDEX idx_audit_created ON audit_log(created_at);
