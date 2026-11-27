package api_test

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/trustos/hopssh/internal/e2etest"
	"github.com/trustos/hopssh/internal/fwpolicy"
)

type fwDevice struct {
	NodeID      string
	Expected    string
	Fingerprint string
	Error       string
	ReportedAt  int64
}

// TestE2E_FirewallDelivery: phase 2 of docs/wiki/concepts/firewall-rules.md.
// The heartbeat answer of a managed network carries the policy for the
// device that asked; the device's next heartbeat reports what it runs, and
// the network's firewall answer shows each device's report.
func TestE2E_FirewallDelivery(t *testing.T) {
	h := newHousehold(t)
	box, nas, tv := h.owner.Enroll(h.net.ID, "box"), h.owner.Enroll(h.net.ID, "nas"), h.owner.Enroll(h.net.ID, "tv")
	as := func(e e2etest.Enrollment) *e2etest.Client { c := h.s.Anonymous(); c.Token = e.AgentToken; return c }
	beat := func(e e2etest.Enrollment, report *fwpolicy.Ack) *fwpolicy.Delivered {
		t.Helper()
		body := M{"nodeId": e.NodeID}
		if report != nil {
			body["firewall"] = report
		}
		var out struct{ Firewall *fwpolicy.Delivered }
		as(e).Want(200, "POST", "/api/heartbeat", body, &out)
		return out.Firewall
	}
	ip := func(e e2etest.Enrollment) string { return strings.TrimSuffix(e.NebulaIP, "/24") }
	check := func(who string, d *fwpolicy.Delivered, preset string, want ...fwpolicy.DeliveredRule) {
		t.Helper()
		if d == nil {
			t.Errorf("%s: no policy in the heartbeat answer", who)
			return
		}
		if want == nil {
			want = []fwpolicy.DeliveredRule{}
		}
		if d.Preset != preset || !reflect.DeepEqual(d.Rules, want) {
			t.Errorf("%s got preset %q, rules %+v; want %q, %+v", who, d.Preset, d.Rules, preset, want)
		}
		if d.Fingerprint != d.Sum() {
			t.Errorf("%s: fingerprint %q is not that of the policy", who, d.Fingerprint)
		}
	}
	fw := h.url("/firewall")
	devices := func(c *e2etest.Client, path string) map[string]fwDevice {
		t.Helper()
		var p struct{ Devices []fwDevice }
		c.Want(200, "GET", path, nil, &p)
		out := map[string]fwDevice{}
		for _, d := range p.Devices {
			out[d.NodeID] = d
		}
		return out
	}
	icmp := fwpolicy.DeliveredRule{Proto: "icmp", Port: "any"}

	// Nobody chose rules yet: the devices keep the block they were enrolled
	// with, so the answer carries none, and the firewall answer expects none.
	if d := beat(nas, nil); d != nil {
		t.Fatalf("a network nobody managed sends a policy: %+v", d)
	}
	if d := devices(h.member, fw); len(d) != 3 || d[nas.NodeID].Expected != "" {
		t.Errorf("devices of a network nobody managed: %+v", d)
	}

	// A save: each device gets the rules that reach it.
	h.admin.With("If-Match", version(0)).Want(200, "PUT", fw, M{"preset": "ping", "rules": []M{
		{"proto": "tcp", "port": "8096", "sourceNodeId": tv.NodeID, "targetNodeId": nas.NodeID},
		{"proto": "tcp", "port": "22", "targetNodeId": box.NodeID},
		{"proto": "icmp", "port": "any"},
	}}, nil)
	jellyfin := fwpolicy.DeliveredRule{Proto: "tcp", Port: "8096", Source: ip(tv)}
	check("nas", beat(nas, nil), "ping", icmp, jellyfin)
	check("box", beat(box, nil), "ping", icmp, fwpolicy.DeliveredRule{Proto: "tcp", Port: "22"})
	check("tv", beat(tv, nil), "ping", icmp)

	// An attached set adds its rules. Detached, they go, and the network
	// stays managed.
	var set fwSet
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "discovery", "rules": []M{{"proto": "udp", "port": "7359", "sourceNodeId": tv.NodeID}}}, &set)
	setOn := "/api/networks/" + h.net.ID + "/firewall/sets/" + set.ID
	h.owner.Want(204, "PUT", setOn, nil, nil)
	withSet := beat(nas, nil)
	check("nas with the set", withSet, "ping", icmp, jellyfin, fwpolicy.DeliveredRule{Proto: "udp", Port: "7359", Source: ip(tv)})
	if withSet != nil && devices(h.member, fw)[nas.NodeID].Expected != withSet.Fingerprint {
		t.Errorf("the firewall answer expects another policy than the heartbeat sends, with a set attached")
	}
	h.owner.Want(204, "DELETE", setOn, nil, nil)
	d := beat(nas, nil)
	check("nas without the set", d, "ping", icmp, jellyfin)
	if d == nil {
		t.FailNow()
	}

	// Reports: none yet, then what the device says, kept from the first time
	// it said it.
	if dev := devices(h.member, fw)[nas.NodeID]; dev.Expected != d.Fingerprint || dev.ReportedAt != 0 || dev.Fingerprint != "" {
		t.Errorf("before a report: %+v, want expected %s and nothing reported", dev, d.Fingerprint)
	}
	beat(nas, &fwpolicy.Ack{Fingerprint: d.Fingerprint})
	first := devices(h.member, fw)[nas.NodeID]
	if first.Fingerprint != d.Fingerprint || first.Error != "" || first.ReportedAt == 0 {
		t.Errorf("after the report: %+v", first)
	}
	time.Sleep(1100 * time.Millisecond) // a report time is in seconds
	beat(nas, &fwpolicy.Ack{Fingerprint: d.Fingerprint})
	if again := devices(h.member, fw)[nas.NodeID]; again != first {
		t.Errorf("the same report again changed it: %+v, was %+v", again, first)
	}
	beat(box, &fwpolicy.Ack{Error: "rule 2: proto must be tcp"})
	if dev := devices(h.member, fw)[box.NodeID]; dev.Fingerprint != "" || dev.Error != "rule 2: proto must be tcp" || dev.ReportedAt < first.ReportedAt {
		t.Errorf("a refusal: %+v", dev)
	}

	// A report that is not one is ignored, and the heartbeat still answers:
	// a bug in it must not take the device offline.
	for _, bad := range []fwpolicy.Ack{{Fingerprint: "zz"}, {Fingerprint: strings.Repeat("a", 65)}, {Fingerprint: strings.ToUpper(d.Fingerprint)}} {
		check("tv after a bad report", beat(tv, &bad), "ping", icmp)
		if dev := devices(h.member, fw)[tv.NodeID]; dev.ReportedAt != 0 {
			t.Errorf("report %+v was stored: %+v", bad, dev)
		}
	}
	// A long reason is cut, and control characters become spaces.
	beat(tv, &fwpolicy.Ack{Error: "a\nb\x00" + strings.Repeat("я", 400)})
	if dev := devices(h.member, fw)[tv.NodeID]; !strings.HasPrefix(dev.Error, "a b ") || utf8.RuneCountInString(dev.Error) != 300 {
		t.Errorf("a long reason with control characters: %d characters, %.8q…", utf8.RuneCountInString(dev.Error), dev.Error)
	}

	// A rule whose source node was deleted matches nothing, and is left out.
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", tv.NodeID), nil, nil)
	check("nas after the TV's delete", beat(nas, nil), "ping", icmp)
	if d := devices(h.member, fw); len(d) != 2 {
		t.Errorf("devices after a delete: %+v", d)
	}

	// Another network that nobody managed: nothing.
	lab := h.owner.CreateNetwork("lab")
	if d := beat(h.owner.Enroll(lab.ID, "labbox"), nil); d != nil {
		t.Errorf("another network nobody managed sends a policy: %+v", d)
	}
}
