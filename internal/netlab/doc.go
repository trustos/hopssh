// Package netlab is a network laboratory: the real hop-server and real
// hop-agents, as root with a kernel TUN device, on simulated networks with
// home routers, carrier NAT, lossy links, sleep and outages. It is made of
// Linux namespaces, so it needs root and no second machine, and nothing of it
// is left when it ends. It contains only tests; run them with
//
//	make netlab
//
// See docs/wiki/concepts/netlab.md.
package netlab
