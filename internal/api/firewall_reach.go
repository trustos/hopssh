package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/fwpolicy"
)

// reachLine is one thing that may reach a device: a protocol and port from
// a source. Origin says why: "always" (what hopssh keeps on every device),
// "preset" (the network's preset), "network" (one of its own rules) or
// "set" (a rule of the attached set SetName). From is "control" (the
// control plane: web terminal, health), "network" (every device of the
// network), "node" (SourceNodeID) or "any".
type reachLine struct {
	Origin       string `json:"origin"`
	SetID        string `json:"setId,omitempty"`
	SetName      string `json:"setName,omitempty"`
	Proto        string `json:"proto"`
	Port         string `json:"port"`
	From         string `json:"from"`
	SourceNodeID string `json:"sourceNodeId,omitempty"`
	Description  string `json:"description,omitempty"`
}

type nodeFirewallResponse struct {
	// Managed: the network's rules are sent to its devices. Until then a
	// device keeps the block it was enrolled with, which was that of the
	// preset at the time; Lines are those of the preset today.
	Managed bool        `json:"managed"`
	Preset  string      `json:"preset"`
	Lines   []reachLine `json:"lines"`
	// The device's last report, as in the network's device list.
	Device firewallDevice `json:"device"`
}

// presetLines: what hopssh always keeps, and what each preset adds. As the
// agent's block for each (internal/client agentFirewallYAML).
func presetLines(preset string) []reachLine {
	out := []reachLine{
		{Origin: "always", Proto: "tcp", Port: "41820", From: "control"},
		{Origin: "always", Proto: "tcp", Port: "41820", From: "network"},
		{Origin: "always", Proto: "icmp", Port: "any", From: "any"},
	}
	switch preset {
	case fwpolicy.PresetAll:
		out = append(out, reachLine{Origin: "preset", Proto: "any", Port: "any", From: "network"})
	case fwpolicy.PresetTCP:
		out = append(out, reachLine{Origin: "preset", Proto: "tcp", Port: "any", From: "network"})
	}
	return out
}

func ruleLines(origin, setID, setName string, rules []fwpolicy.Rule) []reachLine {
	out := make([]reachLine, 0, len(rules))
	for _, r := range rules {
		l := reachLine{Origin: origin, SetID: setID, SetName: setName, Proto: r.Proto, Port: r.Port,
			From: "network", SourceNodeID: r.SourceNodeID, Description: r.Description}
		if r.SourceNodeID != "" {
			l.From = "node"
		}
		out = append(out, l)
	}
	return out
}

// GetNodeFirewall says what may reach one device of a network, and why:
// what hopssh keeps, the preset, and each rule of the network and of its
// sets whose target is every device or this one, and whose source still
// exists. The same rules as the policy the device gets (fwpolicy.Reaching).
// @Summary      What may reach a device
// @Tags         firewall
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Param        nodeID path string true "Node ID"
// @Success      200 {object} object
// @Router       /api/networks/{networkID}/nodes/{nodeID}/firewall [get]
func (h *FirewallHandler) GetNodeFirewall(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID, nodeID := chi.URLParam(r, "networkID"), chi.URLParam(r, "nodeID")
	if _, _, ok := h.access(w, user, networkID); !ok {
		return
	}
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		http.Error(w, "failed to read the node", http.StatusInternalServerError)
		return
	}
	if node == nil || node.NetworkID != networkID {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	f, err := h.Firewall.NetworkFirewall(networkID)
	if err != nil || f == nil {
		http.Error(w, "failed to read firewall rules", http.StatusInternalServerError)
		return
	}
	sets, err := h.Firewall.AttachedSets(networkID)
	if err != nil {
		http.Error(w, "failed to read rule sets", http.StatusInternalServerError)
		return
	}
	devices, err := h.devices(f, sets)
	if err != nil {
		http.Error(w, "failed to read devices", http.StatusInternalServerError)
		return
	}
	nodes, err := h.Nodes.ListForNetwork(networkID)
	if err != nil {
		http.Error(w, "failed to read devices", http.StatusInternalServerError)
		return
	}
	addrs := nodeAddrs(nodes)
	resp := nodeFirewallResponse{Managed: f.Managed, Preset: f.Preset, Lines: presetLines(f.Preset),
		Device: firewallDevice{NodeID: nodeID}}
	resp.Lines = append(resp.Lines, ruleLines("network", "", "", fwpolicy.Reaching(f.Rules, nodeID, addrs))...)
	for _, s := range sets {
		resp.Lines = append(resp.Lines, ruleLines("set", s.ID, s.Name, fwpolicy.Reaching(s.Rules, nodeID, addrs))...)
	}
	for _, d := range devices {
		if d.NodeID == nodeID {
			resp.Device = d
		}
	}
	writeJSON(w, resp)
}
