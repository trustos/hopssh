// Package portmap requests UDP port forwardings from the local home router
// so a peer behind a home NAT can be reached directly by peers on other
// networks (including those behind CGNAT with no port-mapping support of
// their own).
//
// The package implements three router-mapping protocols, all from scratch
// with zero external dependencies:
//
//   - NAT-PMP (RFC 6886)  — smallest, Apple's original design
//   - PCP     (RFC 6887)  — NAT-PMP's successor, added in a later PR
//   - UPnP-IGD            — most universal legacy support, added in a later PR
//
// At startup, the coordinator probes the discovered default gateway with
// each protocol in parallel, keeps the first one that responds, and
// refreshes the mapping before the router's TTL expires. The resulting
// public AddrPort is surfaced to the caller so it can be advertised to
// peers (in our case, via Nebula's lighthouse advertise_addrs list).
//
// See spike/relay-vs-tailscale-evidence/IMPLEMENTATION-PLAN.md for design
// rationale + RFC citations.
package portmap
