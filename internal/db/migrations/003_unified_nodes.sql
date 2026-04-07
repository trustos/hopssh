-- Unified node model: replace server/client types with per-node capabilities.

ALTER TABLE nodes ADD COLUMN capabilities TEXT NOT NULL DEFAULT '["terminal","health","forward"]';

-- Migrate existing: agent (server) nodes keep all capabilities, user (client) nodes get none.
UPDATE nodes SET capabilities = '[]' WHERE node_type = 'user';
