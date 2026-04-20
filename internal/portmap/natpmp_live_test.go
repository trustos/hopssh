//go:build natpmp_live

package portmap

import (
	"context"
	"testing"
	"time"
)

// Live test — requires a NAT-PMP-capable gateway on the local network.
// Guarded by build tag `natpmp_live` so CI skips it:
//
//   go test -tags=natpmp_live -v ./internal/portmap/ -run TestNATPMP_Live
//
// On the user's TP-Link (confirmed NAT-PMP-capable in diagnostic),
// this should return the public IP 46.10.240.91 and an external port
// equal to or near the suggested internal port 44244.
func TestNATPMP_Live(t *testing.T) {
	gw, err := DiscoverGateway()
	if err != nil {
		t.Fatalf("DiscoverGateway: %v", err)
	}
	t.Logf("gateway: %s", gw)

	c := NewNATPMP(gw)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	const internalPort = uint16(44244)
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
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := c.Unmap(ctx2, internalPort); err != nil {
		t.Errorf("Unmap: %v", err)
	}
}
