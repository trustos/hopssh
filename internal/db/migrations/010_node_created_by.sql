-- The user who brought a node in: who made its enrollment token or bundle,
-- joined it as a client device, or approved its device code. A machine
-- enrolls and fetches its bundle without a session, so the audit log names
-- this user for those two steps. NULL for nodes from before this migration.

ALTER TABLE nodes ADD COLUMN created_by TEXT;
