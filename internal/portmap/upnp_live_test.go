//go:build upnp_live

package portmap

import (
	"context"
	"testing"
	"time"
)

// Live test — requires a UPnP-IGD-capable router on the local network.
// Build-tag-gated so CI skips it:
//
//   go test -tags=upnp_live -v ./internal/portmap/ -run TestUPnP_Live
//
// On a UPnP-IGD WANIPConnection:1 router (verify via `upnpc -s`),
// this should yield the WAN public IP and an external port equal to
// the suggested internal port.
func TestUPnP_Live(t *testing.T) {
	c := NewUPnP()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const internalPort = uint16(44245)
	pub, ttl, err := c.Map(ctx, internalPort)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	t.Logf("mapped: public=%s ttl=%v", pub, ttl)

	if !pub.IsValid() {
		t.Errorf("invalid public addr: %v", pub)
	}
	if ttl <= 0 {
		t.Errorf("non-positive ttl: %v", ttl)
	}

	// Be a good citizen — unmap immediately so we don't churn the router.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := c.Unmap(ctx2, internalPort); err != nil {
		t.Errorf("Unmap: %v", err)
	}
}
