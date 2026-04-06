package pki

import (
	"fmt"
	"net/netip"
)

// SubnetIP returns a specific host address within a /24 subnet.
// hostIndex 1 = server (e.g. 10.42.1.1), hostIndex 2 = first node, etc.
func SubnetIP(subnet string, hostIndex int) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse subnet %q: %w", subnet, err)
	}
	base := prefix.Addr().As4()
	base[3] = byte(hostIndex)
	addr := netip.AddrFrom4(base)
	return netip.PrefixFrom(addr, 24), nil
}

// ServerAddress returns the server's Nebula IP within the network's subnet (.1).
func ServerAddress(subnet string) (netip.Prefix, error) {
	return SubnetIP(subnet, 1)
}

// NodeAddress returns a node's Nebula IP within the network's subnet.
// Node 0 → .2, Node 1 → .3, etc.
func NodeAddress(subnet string, nodeIndex int) (netip.Prefix, error) {
	return SubnetIP(subnet, nodeIndex+2)
}
