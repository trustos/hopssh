-- Every certificate the server gives a node (docs/wiki/concepts/
-- deleted-devices.md). A node deleted in the dashboard keeps its
-- certificate, valid up to 24 h plus the hour of clock-skew tolerance: the
-- certificates of nodes that no longer exist, until then, are the network's
-- blocklist, which the heartbeat answer carries and every device and the
-- lighthouse give Nebula. node_id has no reference, as the row must outlive
-- the node. A row is written in the same transaction as the node's
-- certificate; rows past their time are removed then. The indexes serve
-- that removal, the network's list on every heartbeat, and the count of a
-- node's certificates that bounds its renewals.
CREATE TABLE node_certs (
    fingerprint TEXT PRIMARY KEY,
    network_id  TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL,
    not_after   INTEGER NOT NULL
);
CREATE INDEX idx_node_certs_network ON node_certs(network_id, not_after);
CREATE INDEX idx_node_certs_node ON node_certs(node_id, not_after);
CREATE INDEX idx_node_certs_not_after ON node_certs(not_after);
