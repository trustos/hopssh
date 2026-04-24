package main

import (
	"net/netip"
	"testing"
)

func TestSelfEndpoints_NilInstanceIsNil(t *testing.T) {
	if got := selfEndpoints(nil); got != nil {
		t.Errorf("nil instance returned %v, want nil", got)
	}
}

func TestSelfEndpoints_NoListenPortIsNil(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	// inst.enrollment.ListenPort is zero — pre-A2 layout. Skip silently.
	if got := selfEndpoints(inst); got != nil {
		t.Errorf("zero listen port returned %v, want nil", got)
	}
}

func TestSelfEndpoints_ReturnsLocalAddrsWithPort(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4242})
	got := selfEndpoints(inst)
	// On any test host with at least one non-loopback non-mesh interface
	// (true in CI and on dev machines), this should return >=1 entry,
	// and every entry should parse as IP:port with port=4242.
	if len(got) == 0 {
		t.Skip("no usable local interface addrs on this host (likely an unusual CI box)")
	}
	for _, s := range got {
		ap, err := netip.ParseAddrPort(s)
		if err != nil || !ap.IsValid() {
			t.Errorf("invalid AddrPort %q: %v", s, err)
		}
		if ap.Port() != 4242 {
			t.Errorf("entry %q has port %d, want 4242", s, ap.Port())
		}
		// Mesh subnet must be excluded.
		if ap.Addr().Is4() {
			b := ap.Addr().As4()
			if b[0] == 10 && b[1] == 42 {
				t.Errorf("entry %q is in mesh subnet, must be excluded", s)
			}
		}
	}
}

func TestLocalUnicastAddrs_FiltersMeshAndLoopback(t *testing.T) {
	got := localUnicastAddrs()
	for _, addr := range got {
		if addr.IsLoopback() {
			t.Errorf("loopback %v leaked through", addr)
		}
		if addr.IsLinkLocalUnicast() {
			t.Errorf("link-local %v leaked through", addr)
		}
		if addr.IsMulticast() {
			t.Errorf("multicast %v leaked through", addr)
		}
		if addr.Is4() {
			b := addr.As4()
			if b[0] == 10 && b[1] == 42 {
				t.Errorf("mesh subnet %v leaked through", addr)
			}
		}
	}
}
