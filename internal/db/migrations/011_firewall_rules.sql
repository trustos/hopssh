-- Firewall rules (docs/wiki/decisions/firewall-rule-sets.md).
--
-- A network allows the union of what hopssh always keeps, its preset
-- (networks.firewall_preset), its own rules (network_firewall) and the rules
-- of every set attached to it (network_firewall_sets). Rules only allow; a
-- rule names nodes by their ID, never by name or address.
--
-- Nothing is written for existing networks: a network without a row in
-- network_firewall is version 0, with no rules of its own.

-- The network's own rules, changed by its owner and admin members.
CREATE TABLE network_firewall (
    network_id  TEXT PRIMARY KEY REFERENCES networks(id) ON DELETE CASCADE,
    rules       TEXT NOT NULL DEFAULT '[]',   -- JSON array of rules
    version     INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    updated_by  TEXT                           -- user ID of the last change
);

-- Rule sets a user owns and attaches to networks they own. Only the owner
-- changes a set: an edit changes every network it is attached to.
CREATE TABLE firewall_sets (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id),
    name        TEXT NOT NULL,
    rules       TEXT NOT NULL DEFAULT '[]',
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (user_id, name)
);

-- Which sets a network uses. The API refuses to delete a set that is
-- attached anywhere (409), in the same statement that deletes it; a
-- network's delete removes its attachments.
CREATE TABLE network_firewall_sets (
    network_id  TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    set_id      TEXT NOT NULL REFERENCES firewall_sets(id) ON DELETE CASCADE,
    attached_at INTEGER NOT NULL,
    PRIMARY KEY (network_id, set_id)
);
CREATE INDEX idx_network_firewall_sets_set ON network_firewall_sets(set_id);
