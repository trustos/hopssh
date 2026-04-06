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
    nebula_subnet TEXT,
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
    agent_real_ip TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    last_seen_at INTEGER,
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
CREATE INDEX idx_nodes_enrollment ON nodes(enrollment_token);
CREATE INDEX idx_audit_user ON audit_log(user_id);
CREATE INDEX idx_audit_network ON audit_log(network_id);
CREATE INDEX idx_audit_created ON audit_log(created_at);
