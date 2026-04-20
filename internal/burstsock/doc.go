// Package burstsock implements simultaneous-burst port probing for
// NAT traversal across symmetric NAT with random port allocation.
// This is the "birthday paradox" technique that Tailscale's
// magicsock uses to punch through carrier-grade NAT (CGNAT) on both
// peers — the case our existing NAT-PMP/UPnP solution does NOT cover
// (only the asymmetric one-side-has-UPnP case is solved by Pillar 1).
//
// Math: the probability that ONE source port talking to ONE target
// port hits a CGNAT-allocated mapping is ~1/65535. With N source
// ports each probing K target ports, the collision probability per
// cycle is approximately 1 - (1 - NK/65536). At N=K=256 (sqrt of
// 65536), one cycle has ~63 % expected hit rate; two cycles ~87 %;
// three ~95 %. Carrier rate-limits cap us at ~100 packets/s/source,
// so a 256-socket cycle takes ~2.5 s.
//
// This package ships the socket-pool primitive + port-candidate
// generator. The full integration (lighthouse-coordinated burst
// trigger, handshake manager wiring) requires a Nebula vendor patch
// and ships in a follow-up PR.
package burstsock
