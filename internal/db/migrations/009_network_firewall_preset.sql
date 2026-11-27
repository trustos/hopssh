-- The firewall preset of a network: which inbound block an agent writes into
-- its nebula.yaml when it enrolls (agentFirewallYAML in
-- internal/client/enroll.go). The enrollment answers carry it; agents from
-- before it ignore the field and write the "tcp" block.
--
--   tcp  TCP on every port and ICMP from every node. Every network created
--        before this migration, which keeps it.
--   all  every protocol from every node. Networks created from now on.
--
-- Nothing rewrites a device's firewall later: a changed preset reaches only
-- devices that enroll afterwards.

ALTER TABLE networks ADD COLUMN firewall_preset TEXT NOT NULL DEFAULT 'all';
UPDATE networks SET firewall_preset = 'tcp';
