package portmap

// UPnP-IGD client implementing the Client interface. Drives SSDP
// discovery → device-description fetch → SOAP AddPortMapping +
// GetExternalIPAddress.
//
// Cache the discovered service for the lifetime of the client so
// refresh calls don't re-do SSDP every cycle.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"time"
)

// UPnP is a UPnP-IGD client. Discovery is lazy and cached.
type UPnP struct {
	mu      sync.Mutex
	service *upnpService // cached after first successful discovery
}

// NewUPnP returns an inactive UPnP client. Discovery happens on the
// first Map() call.
func NewUPnP() *UPnP { return &UPnP{} }

func (c *UPnP) Name() string { return "upnp" }

// Map requests a UDP port mapping and returns the public AddrPort.
// Picks up cached discovery state on subsequent calls.
func (c *UPnP) Map(ctx context.Context, internalPort uint16) (netip.AddrPort, time.Duration, error) {
	svc, err := c.resolveService(ctx)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	clientIP, err := localIPToward(svc.ControlURL)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}

	const lease = uint32(7200)
	if err := c.addMapping(ctx, svc, internalPort, clientIP, lease); err != nil {
		return netip.AddrPort{}, 0, err
	}

	pubIP, err := c.getExternalIP(ctx, svc)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	addr, err := netip.ParseAddr(pubIP)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("upnp: parse public IP %q: %w", pubIP, err)
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() {
		return netip.AddrPort{}, 0, fmt.Errorf("upnp: router reported non-public external IP %s (likely double-NAT)", addr)
	}

	return netip.AddrPortFrom(addr, internalPort), time.Duration(lease) * time.Second, nil
}

// Unmap removes the mapping. Best-effort.
func (c *UPnP) Unmap(ctx context.Context, internalPort uint16) error {
	c.mu.Lock()
	svc := c.service
	c.mu.Unlock()
	if svc == nil {
		return nil // never mapped
	}
	body := deletePortMappingBody(internalPort)
	_, err := soapCall(ctx, svc.ControlURL, svc.ServiceType, "DeletePortMapping", body)
	return err
}

func (c *UPnP) resolveService(ctx context.Context) (*upnpService, error) {
	c.mu.Lock()
	if c.service != nil {
		svc := c.service
		c.mu.Unlock()
		return svc, nil
	}
	c.mu.Unlock()

	replies, err := discoverIGD(ctx)
	if err != nil {
		return nil, err
	}
	if len(replies) == 0 {
		return nil, errNoIGD
	}

	// Try replies in order; first device whose description gives us a
	// WANConnection service wins.
	var lastErr error
	for _, r := range replies {
		fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		svc, err := fetchService(fetchCtx, r.Location)
		cancel()
		if err == nil && svc != nil {
			c.mu.Lock()
			c.service = svc
			c.mu.Unlock()
			return svc, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("upnp: no usable service among IGD replies")
	}
	return nil, lastErr
}

// addMapping issues AddPortMapping with one retry on the
// OnlyPermanentLeases (725) fault — some routers reject any non-zero
// lease and want lease=0 (indefinite).
func (c *UPnP) addMapping(ctx context.Context, svc *upnpService, internalPort uint16, clientIP string, lease uint32) error {
	body := addPortMappingBody(internalPort, internalPort, clientIP, lease)
	_, err := soapCall(ctx, svc.ControlURL, svc.ServiceType, "AddPortMapping", body)
	if err == nil {
		return nil
	}
	se, ok := err.(*soapErr)
	if !ok {
		return err
	}
	if se.Code == upnpErrOnlyPermanentLeases {
		body = addPortMappingBody(internalPort, internalPort, clientIP, 0)
		_, err = soapCall(ctx, svc.ControlURL, svc.ServiceType, "AddPortMapping", body)
	}
	return err
}

func (c *UPnP) getExternalIP(ctx context.Context, svc *upnpService) (string, error) {
	resp, err := soapCall(ctx, svc.ControlURL, svc.ServiceType, "GetExternalIPAddress", "")
	if err != nil {
		return "", err
	}
	return parseGetExternalIP(resp)
}

// localIPToward returns the local IPv4 the kernel would use to reach
// host. Used as <NewInternalClient> in AddPortMapping — must be the
// address the router knows us by.
func localIPToward(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
	}
	conn, err := net.Dial("udp4", net.JoinHostPort(host, port))
	if err != nil {
		return "", fmt.Errorf("upnp: probe local IP toward %s: %w", host, err)
	}
	defer conn.Close()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil {
		return "", fmt.Errorf("upnp: cannot determine local IP")
	}
	return la.IP.String(), nil
}
