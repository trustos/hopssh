package api

// Tests for v0.10.26 fixes — server-side. Fix A removes the hardcoded
// listenPort push from /api/renew because the server has no per-node
// knowledge of which UDP port a given agent allocated.

import (
	"os"
	"strings"
	"testing"
)

// TestFixA_RenewHandlerSource_OmitsListenPort statically inspects the
// renew handler's source for the historical bug pattern. We can't
// easily call the full handler in a unit test (it needs a live
// `*db.Stores` + auth) — but the regression we're guarding against
// is one specific line of code, and a static check is sufficient
// to catch a future re-introduction.
//
// Rationale: prior to v0.10.26 the line
//
//   listenPort := nebulacfg.ListenPort
//
// existed in renew.go's Renew() handler, and `&listenPort` was
// included in the response's nebulaConfig. This silently corrupted
// multi-enrollment hosts. Fix A removed both. If anyone reintroduces
// either form, this test catches it.
func TestFixA_RenewHandlerSource_OmitsListenPort(t *testing.T) {
	src, err := os.ReadFile("renew.go")
	if err != nil {
		t.Fatalf("read renew.go: %v", err)
	}
	body := string(src)
	if strings.Contains(body, `listenPort := nebulacfg.ListenPort`) {
		t.Error("Fix A REGRESSED: `listenPort := nebulacfg.ListenPort` is back in renew.go " +
			"— the server is once again pushing a hardcoded port that overrides per-enrollment allocation. " +
			"See v0.10.26 plan and Discovery Log entry. " +
			"If you have a new reason to push listenPort, gate it on per-node DB state.")
	}
	if strings.Contains(body, `"listenPort": &listenPort`) {
		t.Error("Fix A REGRESSED: `\"listenPort\": &listenPort` appears in renew response.")
	}
}
