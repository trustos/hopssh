-- cleanup-orphan-nodes.sql
--
-- v0.10.85 manual one-shot cleanup for orphan node rows produced by the
-- pre-fix /api/device/poll path. The defect: server committed
--   Nodes.Create(status='pending') → IssueCert → CompleteEnrollment(status='enrolled')
-- BEFORE the agent had a chance to detect a duplicate enrollment locally
-- and reject. When the agent rejected (existingEnrollmentForNetwork),
-- the row was left with status='enrolled' and last_seen_at=NULL forever.
-- The F2 conflict-detection header (v0.10.85+) prevents new orphans, but
-- pre-existing rows in production need a one-time sweep.
--
-- Orphan signature:
--   * status = 'enrolled'
--   * last_seen_at IS NULL
--   * created_at older than 24 hours (so we never sweep a legitimate
--     just-enrolled-not-yet-beaconed node)
--   * node_type = 'node' (lighthouses are synthetic and have NULL last_seen)
--
-- Run procedure:
--   1. Connect to the production sqlite DB on the control plane host:
--        ssh <control-plane>
--        sudo sqlite3 /var/lib/hopssh/hopssh.db   (path may vary)
--   2. Run the SELECT to review candidates. Note the network_id grouping
--      and the per-row hostname/os/arch — recognize anything? Anything
--      that's a real device a user is mid-enrolling on?
--   3. If satisfied, uncomment and run the DELETE.
--   4. Confirm the count with the post-delete SELECT.
--
-- This script is read-mostly by default. The DELETE is COMMENTED OUT so
-- a copy-paste-without-reading doesn't nuke real rows.

-- 1) Candidate list — review before proceeding.
SELECT
    id,
    network_id,
    hostname,
    os,
    arch,
    nebula_ip,
    datetime(created_at, 'unixepoch') AS created_utc,
    status,
    last_seen_at,
    node_type
FROM nodes
WHERE status = 'enrolled'
  AND last_seen_at IS NULL
  AND created_at < unixepoch() - 86400
  AND node_type = 'node'
ORDER BY network_id, created_at;

-- 2) Summary count by network — gives you a sense of scale.
SELECT
    network_id,
    COUNT(*) AS orphan_count
FROM nodes
WHERE status = 'enrolled'
  AND last_seen_at IS NULL
  AND created_at < unixepoch() - 86400
  AND node_type = 'node'
GROUP BY network_id
ORDER BY orphan_count DESC;

-- 3) Total rows that would be deleted.
SELECT
    COUNT(*) AS total_orphan_rows
FROM nodes
WHERE status = 'enrolled'
  AND last_seen_at IS NULL
  AND created_at < unixepoch() - 86400
  AND node_type = 'node';

-- 4) DELETE — uncomment to execute. Read the candidate list above first.
--    The CASCADE rules on nodes.network_id ON DELETE only cascade FROM
--    network deletion, so deleting nodes does not affect networks.
--
-- DELETE FROM nodes
-- WHERE status = 'enrolled'
--   AND last_seen_at IS NULL
--   AND created_at < unixepoch() - 86400
--   AND node_type = 'node';

-- 5) Post-delete confirmation — should return 0 after the DELETE.
SELECT
    COUNT(*) AS remaining_orphans
FROM nodes
WHERE status = 'enrolled'
  AND last_seen_at IS NULL
  AND created_at < unixepoch() - 86400
  AND node_type = 'node';
