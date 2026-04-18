-- Track the hop-agent version each node is running, self-reported
-- through the existing heartbeat. Nullable so old agents (pre-v0.9.15)
-- simply don't update the column — the dashboard renders an em-dash.
-- No index: filtering/sorting by version isn't a v1 goal; the Nodes
-- table shows it inline for drift visibility.
ALTER TABLE nodes ADD COLUMN agent_version TEXT;
