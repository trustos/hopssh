package mesh

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// DNSRecord represents a hostname → IP mapping for mesh DNS.
type DNSRecord struct {
	Name string // hostname part (e.g., "jellyfin")
	IP   net.IP // VPN IP
}

// MeshDNS serves DNS for a single network's domain.
// Resolves <hostname>.<domain> → Nebula VPN IP.
//
// Binds on 0.0.0.0 with a per-network port (not on the VPN IP, which
// doesn't exist as an OS interface in userspace Nebula mode).
type MeshDNS struct {
	domain string // e.g., "zero", "prod"
	port   int    // UDP port to listen on

	mu      sync.RWMutex
	records map[string]net.IP // hostname → IP

	server *dns.Server
}

// NewMeshDNS creates a DNS server for a network.
// port is the UDP port to listen on (unique per network).
// domain is the user-defined domain (e.g., "zero").
func NewMeshDNS(port int, domain string) *MeshDNS {
	return &MeshDNS{
		domain:  strings.ToLower(domain),
		port:    port,
		records: make(map[string]net.IP),
	}
}

// Port returns the port the DNS server is listening on.
func (d *MeshDNS) Port() int {
	return d.port
}

// SetRecords replaces all DNS records atomically.
func (d *MeshDNS) SetRecords(records []DNSRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.records = make(map[string]net.IP, len(records))
	for _, r := range records {
		d.records[strings.ToLower(r.Name)] = r.IP
	}
}

// AddRecord adds or updates a single DNS record.
func (d *MeshDNS) AddRecord(name string, ip net.IP) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[strings.ToLower(name)] = ip
}

// RemoveRecord removes a DNS record.
func (d *MeshDNS) RemoveRecord(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.records, strings.ToLower(name))
}

// Resolve looks up a hostname and returns the IP, or nil if not found.
func (d *MeshDNS) Resolve(name string) net.IP {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.records[strings.ToLower(name)]
}

// Start begins serving DNS. Blocks briefly to verify the bind succeeds.
func (d *MeshDNS) Start() error {
	mux := dns.NewServeMux()
	mux.HandleFunc(d.domain+".", d.handleQuery)

	addr := fmt.Sprintf("0.0.0.0:%d", d.port)
	d.server = &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: mux,
	}

	// Use a channel to detect bind errors during startup.
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.server.ListenAndServe()
	}()

	// Wait briefly for the server to either bind or fail.
	select {
	case err := <-errCh:
		return fmt.Errorf("DNS server failed to start on %s: %w", addr, err)
	case <-time.After(100 * time.Millisecond):
		// Server started successfully (no immediate error).
		log.Printf("[dns] serving .%s on %s", d.domain, addr)
		return nil
	}
}

// Stop shuts down the DNS server.
func (d *MeshDNS) Stop() {
	if d.server != nil {
		d.server.Shutdown()
	}
}

// handleQuery processes DNS queries for the network's domain.
func (d *MeshDNS) handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	for _, q := range r.Question {
		if q.Qtype != dns.TypeA {
			continue
		}

		// Extract hostname from FQDN: "jellyfin.zero." → "jellyfin"
		fqdn := strings.ToLower(q.Name)
		suffix := "." + d.domain + "."
		if !strings.HasSuffix(fqdn, suffix) {
			continue
		}
		name := strings.TrimSuffix(fqdn, suffix)

		ip := d.Resolve(name)
		if ip == nil {
			continue
		}

		if ip4 := ip.To4(); ip4 != nil {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				A: ip4,
			})
		}
	}

	w.WriteMsg(m)
}
