-- Mesh addresses of deleted nodes.
--
-- A deleted node's certificate stays valid until it expires (24 h), and
-- Nebula asks no database: if the next node got the same address, two valid
-- certificates would share it. Every deletion records the address here (the
-- API, pruning, the cascade of a deleted network, a DELETE by hand), and
-- NextNodeIndex never goes below the highest address ever used. Only when the
-- /24 is full does it reuse one, released longer ago than a certificate lives
-- (db.releasedIPQuarantine).
--
-- No foreign key on network_id: the cascade of a network delete inserts rows
-- for the network being deleted. The second trigger removes them.

CREATE TABLE released_node_ips (
    network_id  TEXT NOT NULL,
    nebula_ip   TEXT NOT NULL,
    released_at INTEGER NOT NULL,
    PRIMARY KEY (network_id, nebula_ip)
);

CREATE TRIGGER nodes_release_ip AFTER DELETE ON nodes
WHEN old.nebula_ip IS NOT NULL
BEGIN
    INSERT OR REPLACE INTO released_node_ips (network_id, nebula_ip, released_at)
    VALUES (old.network_id, old.nebula_ip, unixepoch());
END;

CREATE TRIGGER networks_forget_released_ips AFTER DELETE ON networks
BEGIN
    DELETE FROM released_node_ips WHERE network_id = old.id;
END;
