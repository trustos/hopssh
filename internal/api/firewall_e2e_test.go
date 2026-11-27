package api_test

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/trustos/hopssh/internal/e2etest"
	"github.com/trustos/hopssh/internal/fwpolicy"
)

type fwSet struct {
	ID, Name   string
	Version    int64
	Rules      []fwpolicy.Rule
	NetworkIDs []string
}

type fwPolicy struct {
	Version            int64
	Preset             string
	Rules              []fwpolicy.Rule
	Sets               []fwSet
	CanEdit, CanAttach bool
}

func version(v int64) string { return fmt.Sprintf(`"%d"`, v) }

// etagOf is the ETag of a GET.
func etagOf(t *testing.T, c *e2etest.Client, path string) string {
	t.Helper()
	req, err := http.NewRequest("GET", c.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Headers(req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("ETag")
}

// TestE2E_Firewall: a network's preset and own rules, and the rule sets its
// owner shares between networks (docs/wiki/decisions/firewall-rule-sets.md).
func TestE2E_Firewall(t *testing.T) {
	h := newHousehold(t)
	box := h.owner.Enroll(h.net.ID, "box")
	fw := h.url("/firewall")
	get := func(c *e2etest.Client) fwPolicy {
		t.Helper()
		var p fwPolicy
		c.Want(200, "GET", fw, nil, &p)
		return p
	}

	// A new network: version 0, preset all, nothing of its own.
	if p := get(h.member); p.Version != 0 || p.Preset != "all" || len(p.Rules) != 0 || len(p.Sets) != 0 || p.CanEdit || p.CanAttach {
		t.Fatalf("new network, as a member: %+v", p)
	}
	if p := get(h.admin); !p.CanEdit || p.CanAttach {
		t.Errorf("as the admin: %+v, want canEdit and not canAttach", p)
	}
	if p := get(h.owner); !p.CanEdit || !p.CanAttach {
		t.Errorf("as the owner: %+v, want canEdit and canAttach", p)
	}
	if tag := etagOf(t, h.member, fw); tag != `"0"` {
		t.Errorf("ETag %s, want \"0\"", tag)
	}

	// A change needs the version it was made against, and the right to make it.
	ssh := []fwpolicy.Rule{{Proto: "tcp", Port: "22", TargetNodeID: box.NodeID, Description: "ssh to the box"}}
	change := M{"preset": "ping", "rules": ssh}
	h.member.With("If-Match", version(0)).Want(403, "PUT", fw, change, nil)
	h.outsider.With("If-Match", version(0)).Want(404, "PUT", fw, change, nil)
	h.anon.With("If-Match", version(0)).Want(401, "PUT", fw, change, nil)
	h.owner.Want(428, "PUT", fw, change, nil)
	for _, tag := range []string{"zero", `W/"0"`, "*", `"0", "1"`, "-1"} {
		h.owner.With("If-Match", tag).Want(400, "PUT", fw, change, nil)
	}
	h.owner.With("If-Match", version(1)).Want(412, "PUT", fw, change, nil)
	var p fwPolicy
	h.admin.With("If-Match", version(0)).Want(200, "PUT", fw, change, &p)
	if p.Version != 1 || p.Preset != "ping" || !reflect.DeepEqual(p.Rules, ssh) {
		t.Errorf("after the admin's change: %+v", p)
	}
	h.owner.With("If-Match", version(0)).Want(412, "PUT", fw, M{"preset": "all", "rules": []any{}}, nil)
	if p := get(h.member); p.Version != 1 || p.Preset != "ping" || !reflect.DeepEqual(p.Rules, ssh) {
		t.Errorf("as a member, after the change: %+v", p)
	}
	if tag := etagOf(t, h.member, fw); tag != `"1"` {
		t.Errorf("ETag %s, want \"1\"", tag)
	}
	h.owner.With("If-Match", "1").Want(200, "PUT", fw, change, nil) // quotes are optional
	cur := int64(2)

	// Two changes against the same version: exactly one is stored.
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i, c := range []*e2etest.Client{h.owner, h.admin} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = c.With("If-Match", version(cur)).Do("PUT", fw, M{"preset": "tcp", "rules": ssh})
		}()
	}
	wg.Wait()
	sort.Ints(statuses)
	if fmt.Sprint(statuses) != "[200 412]" {
		t.Errorf("two changes against version %d: %v, want [200 412]", cur, statuses)
	}
	cur++

	// What is refused, and that nothing of it is stored.
	lab := h.owner.CreateNetwork("lab")
	labBox := h.owner.Enroll(lab.ID, "labbox")
	many := make([]M, fwpolicy.MaxRules+1)
	for i := range many {
		many[i] = M{"proto": "tcp", "port": fmt.Sprint(i + 1)}
	}
	for what, body := range map[string]any{
		"not JSON":       "{",
		"no preset":      M{"rules": []any{}},
		"unknown preset": M{"preset": "none", "rules": []any{}},
		"unknown proto":  M{"preset": "all", "rules": []M{{"proto": "gre", "port": "any"}}},
		"port 0":         M{"preset": "all", "rules": []M{{"proto": "tcp", "port": "0"}}},
		"icmp port":      M{"preset": "all", "rules": []M{{"proto": "icmp", "port": "8"}}},
		"too many rules": M{"preset": "all", "rules": many},
		// A rule without its source would allow every node. (Field names
		// match without regard to case, so "sourceNodeID" is sourceNodeId.)
		"misspelt field":       M{"preset": "all", "rules": []M{{"proto": "tcp", "port": "22", "source": box.NodeID}}},
		"unknown top field":    M{"preset": "all", "rules": []any{}, "version": 7},
		"unknown node":         M{"preset": "all", "rules": []M{{"proto": "tcp", "port": "22", "sourceNodeId": "00000000-0000-4000-8000-000000000000"}}},
		"other network's node": M{"preset": "all", "rules": []M{{"proto": "tcp", "port": "22", "targetNodeId": labBox.NodeID}}},
	} {
		status, b := h.owner.With("If-Match", version(cur)).Do("PUT", fw, body)
		if status != 400 {
			t.Errorf("%s: HTTP %d %s, want 400", what, status, b)
		}
		if strings.HasSuffix(what, "node") && !strings.Contains(string(b), "not a device of this network") {
			t.Errorf("%s: %q does not say which node", what, b)
		}
	}
	if p := get(h.owner); p.Version != cur {
		t.Errorf("after refused changes: version %d, want %d", p.Version, cur)
	}

	// Rule sets: owned by one user, names unique per user.
	var web fwSet
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "web", "rules": []M{{"proto": "tcp", "port": "443"}}}, &web)
	if web.Version != 1 || len(web.Rules) != 1 {
		t.Errorf("new set: %+v", web)
	}
	h.owner.Want(409, "POST", "/api/firewall/sets", M{"name": " web "}, nil)
	h.owner.Want(400, "POST", "/api/firewall/sets", M{"name": ""}, nil)
	h.owner.Want(400, "POST", "/api/firewall/sets", M{"name": "x", "rules": []M{{"proto": "tcp", "port": "0"}}}, nil)
	var adminWeb fwSet
	h.admin.Want(201, "POST", "/api/firewall/sets", M{"name": "web"}, &adminWeb)
	if adminWeb.Rules == nil {
		t.Error("a set without rules: rules is null, want []")
	}
	var mine []fwSet
	h.owner.Want(200, "GET", "/api/firewall/sets", nil, &mine)
	if len(mine) != 1 || mine[0].ID != web.ID {
		t.Errorf("the owner's sets: %+v", mine)
	}
	h.admin.Want(200, "GET", "/api/firewall/sets", nil, &mine)
	if len(mine) != 1 || mine[0].ID != adminWeb.ID {
		t.Errorf("the admin's sets: %+v", mine)
	}
	setURL := func(id string) string { return "/api/firewall/sets/" + id }
	for _, c := range []*e2etest.Client{h.admin, h.member, h.outsider} {
		c.With("If-Match", version(1)).Want(404, "PUT", setURL(web.ID), M{"name": "mine now"}, nil)
		c.Want(404, "PUT", setURL(web.ID), M{"name": "mine now"}, nil) // 404 before 428
		c.Want(404, "DELETE", setURL(web.ID), nil, nil)
	}

	// Attaching: only the network's owner, only their own sets.
	attach := func(networkID, setID string) string { return "/api/networks/" + networkID + "/firewall/sets/" + setID }
	h.admin.Want(403, "PUT", attach(h.net.ID, adminWeb.ID), nil, nil)
	h.admin.Want(403, "PUT", attach(h.net.ID, web.ID), nil, nil)
	h.member.Want(403, "PUT", attach(h.net.ID, web.ID), nil, nil)
	h.outsider.Want(404, "PUT", attach(h.net.ID, web.ID), nil, nil)
	h.owner.Want(404, "PUT", attach(h.net.ID, adminWeb.ID), nil, nil)
	h.owner.Want(404, "PUT", attach(h.net.ID, "no-such-set"), nil, nil)
	h.owner.Want(204, "PUT", attach(h.net.ID, web.ID), nil, nil)
	h.owner.Want(204, "PUT", attach(h.net.ID, web.ID), nil, nil) // again: nothing changes
	h.owner.Want(204, "PUT", attach(lab.ID, web.ID), nil, nil)
	p = get(h.member)
	if p.Version != cur || len(p.Sets) != 1 || p.Sets[0].ID != web.ID || len(p.Sets[0].NetworkIDs) != 0 {
		t.Errorf("after attaching, as a member: %+v (the version stays; other networks are not shown)", p)
	}

	// A change of an attached set must hold in every network it is in.
	boxOnly := M{"name": "web", "rules": []M{{"proto": "tcp", "port": "443", "targetNodeId": box.NodeID}}}
	if status, b := h.owner.With("If-Match", version(1)).Do("PUT", setURL(web.ID), boxOnly); status != 400 || !strings.Contains(string(b), box.NodeID) {
		t.Errorf("a set naming a node of one of its two networks: HTTP %d %s, want 400 naming it", status, b)
	}
	h.owner.With("If-Match", version(1)).Want(200, "PUT", setURL(web.ID), M{"name": "web", "rules": []M{{"proto": "tcp", "port": "443"}, {"proto": "udp", "port": "443"}}}, &web)
	if web.Version != 2 || len(web.Rules) != 2 {
		t.Errorf("after the change: %+v", web)
	}
	got := append([]string(nil), web.NetworkIDs...)
	want := []string{h.net.ID, lab.ID}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("networks of the set: %v, want %v", got, want)
	}
	h.owner.With("If-Match", version(1)).Want(412, "PUT", setURL(web.ID), M{"name": "web"}, nil)

	// A rename to a name the owner already has is a conflict of names (409),
	// not a stale version (412) and not a fault of the server; nothing changes.
	var api fwSet
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "api"}, &api)
	if status, b := h.owner.With("If-Match", version(2)).Do("PUT", setURL(web.ID), M{"name": " api "}); status != 409 || !strings.Contains(string(b), "name") {
		t.Errorf("renaming a set to a name the owner has: HTTP %d %s, want 409 about the name", status, b)
	}
	var sets []fwSet
	h.owner.Want(200, "GET", "/api/firewall/sets", nil, &sets)
	for _, s := range sets {
		if s.ID == web.ID && (s.Name != "web" || s.Version != 2) {
			t.Errorf("after a refused rename: %+v, want web at version 2", s)
		}
	}
	h.owner.Want(204, "DELETE", setURL(api.ID), nil, nil)

	// A set naming a node is attached only where that node is.
	var boxSet fwSet
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "box", "rules": []M{{"proto": "tcp", "port": "8096", "targetNodeId": box.NodeID}}}, &boxSet)
	if status, b := h.owner.Do("PUT", attach(lab.ID, boxSet.ID), nil); status != 400 || !strings.Contains(string(b), box.NodeID) {
		t.Errorf("attaching a set naming another network's node: HTTP %d %s, want 400 naming it", status, b)
	}
	h.owner.Want(204, "PUT", attach(h.net.ID, boxSet.ID), nil, nil)

	// A set is deleted only once it is attached nowhere.
	h.owner.Want(409, "DELETE", setURL(web.ID), nil, nil)
	h.owner.Want(204, "DELETE", attach(h.net.ID, web.ID), nil, nil)
	h.owner.Want(204, "DELETE", attach(h.net.ID, web.ID), nil, nil) // again: nothing to do
	h.owner.Want(409, "DELETE", setURL(web.ID), nil, nil)           // still in lab
	h.owner.Want(204, "DELETE", attach(lab.ID, web.ID), nil, nil)
	h.owner.Want(204, "DELETE", setURL(web.ID), nil, nil)
	h.owner.Want(404, "DELETE", setURL(web.ID), nil, nil)

	// A network's delete takes its attachments, not the set.
	var labSet fwSet
	h.owner.Want(201, "POST", "/api/firewall/sets", M{"name": "lab", "rules": []M{{"proto": "tcp", "port": "22", "sourceNodeId": labBox.NodeID}}}, &labSet)
	h.owner.Want(204, "PUT", attach(lab.ID, labSet.ID), nil, nil)
	h.owner.Want(204, "DELETE", "/api/networks/"+lab.ID, nil, nil)
	mine = nil // decoding into old elements would keep fields the answer leaves out
	h.owner.Want(200, "GET", "/api/firewall/sets", nil, &mine)
	for _, s := range mine {
		if s.ID == labSet.ID && len(s.NetworkIDs) != 0 {
			t.Errorf("a set of a deleted network: still attached to %v", s.NetworkIDs)
		}
	}
	h.owner.Want(204, "DELETE", setURL(labSet.ID), nil, nil)

	// A deleted node stays where it was named, and the rules around it can
	// still be changed.
	h.owner.Want(204, "DELETE", h.url("/nodes/%s", box.NodeID), nil, nil)
	if p := get(h.member); !reflect.DeepEqual(p.Rules, ssh) || len(p.Sets) != 1 || p.Sets[0].Rules[0].TargetNodeID != box.NodeID {
		t.Errorf("after deleting the node: %+v", p)
	}
	h.owner.With("If-Match", version(cur)).Want(200, "PUT", fw, M{"preset": "ping", "rules": append(ssh, fwpolicy.Rule{Proto: "icmp", Port: "any"})}, nil)
	cur++
	h.owner.With("If-Match", version(1)).Want(200, "PUT", setURL(boxSet.ID), M{"name": "box (gone)", "rules": boxSet.Rules}, nil)

	// A new enrollment gets the network's preset.
	_, token := h.owner.EnrollToken(h.net.ID)
	var e struct{ Firewall string }
	h.s.Anonymous().Want(200, "POST", "/api/enroll", M{"token": token, "hostname": "new", "os": "linux", "arch": "arm64"}, &e)
	if e.Firewall != "ping" {
		t.Errorf("a new enrollment got preset %q, want ping", e.Firewall)
	}
}
