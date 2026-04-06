package mesh

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

// DNSRecord represents a hostname → IP mapping for mesh DNS.
type DNSRecord struct {
	Name string // hostname part (e.g., "jellyfin")
	IP   net.IP // VPN IP
}

// MeshDNS serves DNS for a single network's domain.
// Resolves <hostname>.<domain> → Nebula VPN IP.
type MeshDNS struct {
	domain  string // e.g., "zero", "prod"
	listenIP string // lighthouse VPN IP to bind on

	mu      sync.RWMutex
	records map[string]net.IP // hostname → IP

	server *dns.Server
}

// NewMeshDNS creates a DNS server for a network.
// listenIP is the lighthouse's Nebula VPN IP (e.g., "10.42.1.1").
// domain is the user-defined domain (e.g., "zero").
func NewMeshDNS(listenIP, domain string) *MeshDNS {
	d := &MeshDNS{
		domain:   strings.ToLower(domain),
		listenIP: listenIP,
		records:  make(map[string]net.IP),
	}
	return d
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

// Start begins serving DNS on the lighthouse VPN IP, port 53.
// This runs in a goroutine — call Stop() to shut down.
func (d *MeshDNS) Start() error {
	mux := dns.NewServeMux()
	// Handle queries for our domain (e.g., "*.zero.")
	mux.HandleFunc(d.domain+".", d.handleQuery)

	addr := fmt.Sprintf("%s:53", d.listenIP)
	d.server = &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: mux,
	}

	go func() {
		log.Printf("[dns] serving .%s on %s", d.domain, addr)
		if err := d.server.ListenAndServe(); err != nil {
			log.Printf("[dns] server error for .%s: %v", d.domain, err)
		}
	}()

	return nil
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
		if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
			continue
		}

		// Extract hostname from FQDN: "jellyfin.zero." → "jellyfin"
		name := strings.TrimSuffix(q.Name, "."+d.domain+".")
		name = strings.TrimSuffix(name, ".")
		name = strings.ToLower(name)

		ip := d.Resolve(name)
		if ip == nil {
			continue
		}

		// Only serve A records (IPv4 — Nebula uses 10.42.x.x)
		if q.Qtype == dns.TypeA {
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
	}

	w.WriteMsg(m)
}
