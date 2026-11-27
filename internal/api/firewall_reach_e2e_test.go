package api_test

import (
	"reflect"
	"testing"

	"github.com/trustos/hopssh/internal/e2etest"
	"github.com/trustos/hopssh/internal/fwpolicy"
)

type reachLine struct {
	Origin, SetID, SetName, Proto, Port, From, SourceNodeID, Description string
}

type nodeFirewall struct {
	Managed bool
	Preset  string
	Lines   []reachLine
	Device  struct {
		NodeID, Expected, Fingerprint, Error string
		Blocked                              bool
		ReportedAt                           int64
	}
}

// TestE2E_FirewallReach: phase 3 of docs/wiki/concepts/firewall-rules.md on
// the server. "Who can reach this device" lists what hopssh keeps, the
// preset, and the rules of the network and of its sets that reach the
// device, with where each comes from; the rules for other devices and those
// whose source was deleted are not there, as in the policy the device gets.
// A device's report says whether its own switch "Block incoming" is on; a
// heartbeat without a report says it is off, also after a restart of the
// server.
func TestE2E_FirewallReach(t *testing.T) {
	h := newHousehold(t)
	box, nas, tv := h.owner.Enroll(h.net.ID, "box"), h.owner.Enroll(h.net.ID, "nas"), h.owner.Enroll(h.net.ID, "tv")
	reach := func(c *e2etest.Client, e e2etest.Enrollment) nodeFirewall {
		t.Helper()
		var out nodeFirewall
		c.Want(200, "GET", h.url("/nodes/%s/firewall", e.NodeID), nil, &out)
		return out
	}
	beat := func(e e2etest.Enrollment, report *fwpolicy.Ack) *fwpolicy.Delivered {
		t.Helper()
		c := h.s.Anonymous()
		c.Token = e.AgentToken
		body := M{"nodeId": e.NodeID}
		if report != nil {
			body["firewall"] = report
		}
		var out struct{ Firewall *fwpolicy.Delivered }
		c.Want(200, "POST", "/api/heartbeat", body, &out)
		return out.Firewall
	}
	lines := func(who string, got nodeFirewall, want ...reachLine) {
		t.Helper()
		always := []reachLine{
			{Origin: "always", Proto: "tcp", Port: "41820", From: "control"},
			{Origin: "always", Proto: "tcp", Port: "41820", From: "network"},
			{Origin: "always", Proto: "icmp", Port: "any", From: "any"},
		}
		want = append(always, want...)
		if !reflect.DeepEqual(got.Lines, want) {
			t.Errorf("%s: what may reach it\n  %+v\nwant\n  %+v", who, got.Lines, want)
		}
	}

	// Nobody chose rules: the preset of a new network.
	r := reach(h.member, nas)
	if r.Managed || r.Preset != "all" || r.Device.NodeID != nas.NodeID {
		t.Errorf("before any save: %+v", r)
	}
	lines("nas before any save", r, reachLine{Origin: "preset", Proto: "any", Port: "any", From: "network"})

	// Rules of the network and of a set; only those that reach nas.
	h.admin.With("If-Match", version(0)).Want(200, "PUT", h.url("/firewall"), M{"preset": "ping", "rules": []M{
		{"proto": "tcp", "port": "8096", "sourceNodeId": tv.NodeID, "targetNodeId": nas.NodeID, "description": "Jellyfin"},
		{"proto": "tcp", "port": "22", "targetNodeId": box.NodeID},
		{"proto": "tcp", "port": "445", "sourceNodeId": box.NodeID},
	}}, nil)
	var set fwSet
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "discovery", "rules": []M{
		{"proto": "udp", "port": "7359", "sourceNodeId": tv.NodeID},
		{"proto": "udp", "port": "1900", "targetNodeId": box.NodeID},
	}}, &set)
	h.owner.Want(204, "PUT", h.url("/firewall/sets/%s", set.ID), nil, nil)
	jellyfin := reachLine{Origin: "network", Proto: "tcp", Port: "8096", From: "node", SourceNodeID: tv.NodeID, Description: "Jellyfin"}
	smb := reachLine{Origin: "network", Proto: "tcp", Port: "445", From: "node", SourceNodeID: box.NodeID}
	discovery := reachLine{Origin: "set", SetID: set.ID, SetName: "discovery", Proto: "udp", Port: "7359", From: "node", SourceNodeID: tv.NodeID}
	r = reach(h.member, nas)
	if !r.Managed || r.Preset != "ping" {
		t.Errorf("after a save: managed %v, preset %q", r.Managed, r.Preset)
	}
	lines("nas", r, jellyfin, smb, discovery)
	lines("box", reach(h.member, box),
		reachLine{Origin: "network", Proto: "tcp", Port: "22", From: "network"}, smb, discovery,
		reachLine{Origin: "set", SetID: set.ID, SetName: "discovery", Proto: "udp", Port: "1900", From: "network"})

	// The device's report: the policy it runs, and its switch.
	d := beat(nas, nil)
	if d == nil {
		t.Fatal("no policy for nas")
	}
	if r := reach(h.member, nas); r.Device.Expected != d.Fingerprint || r.Device.ReportedAt != 0 || r.Device.Blocked {
		t.Errorf("before a report: %+v", r.Device)
	}
	blockedSince := func(who string, want bool) {
		t.Helper()
		if got := reach(h.member, nas).Device; got.Blocked != want || got.Fingerprint != d.Fingerprint {
			t.Errorf("%s: nas's report %+v; want blocked %v", who, got, want)
		}
		var p struct{ Devices []fwDevice }
		h.member.Want(200, "GET", h.url("/firewall"), nil, &p)
		for _, dev := range p.Devices {
			if dev.NodeID == nas.NodeID && dev.Blocked != want {
				t.Errorf("%s: the network's device list says nas blocked %v; want %v", who, dev.Blocked, want)
			}
		}
	}
	beat(nas, &fwpolicy.Ack{Fingerprint: d.Fingerprint, Blocked: true})
	blockedSince("switched on", true)
	beat(nas, &fwpolicy.Ack{Fingerprint: d.Fingerprint, Blocked: true})
	blockedSince("switched on, the same report again", true)
	beat(nas, nil)
	blockedSince("a heartbeat without a report", false)
	beat(nas, nil)
	blockedSince("another one", false)

	// After a restart of the server nothing is cached: the switch on, the
	// server restarts, and the next heartbeat without a report clears it.
	beat(nas, &fwpolicy.Ack{Fingerprint: d.Fingerprint, Blocked: true})
	h.s.Stop()
	h.s.Run()
	blockedSince("after a restart", true)
	beat(nas, nil)
	blockedSince("a heartbeat without a report after a restart", false)
	// One that never reported: a heartbeat without one stores nothing.
	beat(tv, nil)
	if got := reach(h.member, tv).Device; got.ReportedAt != 0 || got.Blocked {
		t.Errorf("tv, which never reported: %+v", got)
	}

	// A deleted source matches nothing.
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", tv.NodeID), nil, nil)
	lines("nas after the TV's delete", reach(h.member, nas), smb)

	// A node of another network, or none.
	lab := h.owner.CreateNetwork("lab")
	other := h.owner.Enroll(lab.ID, "pi")
	h.owner.Want(404, "GET", h.url("/nodes/%s/firewall", other.NodeID), nil, nil)
	h.owner.Want(404, "GET", h.url("/nodes/%s/firewall", "nope"), nil, nil)
	h.owner.Want(404, "GET", h.url("/nodes/%s/firewall", tv.NodeID), nil, nil)
}
