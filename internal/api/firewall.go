package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/authz"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/fwpolicy"
)

// FirewallHandler serves the firewall rules of networks and the rule sets
// their owners share between them (docs/wiki/decisions/firewall-rule-sets.md).
//
//   - A network's preset and own rules: members read, the owner and admin
//     members change them.
//   - A rule set: only its owner sees and changes it, since an edit changes
//     every network it is attached to.
//   - Attaching and detaching: only the network's owner, with their own sets.
//
// Changes need If-Match with the version the change was made against. The
// heartbeat answer carries a managed network's rules to its devices
// (RenewHandler), and each device's report is shown here.
type FirewallHandler struct {
	Networks *db.NetworkStore
	Members  *db.NetworkMemberStore
	Nodes    *db.NodeStore
	Firewall *db.FirewallStore
	Audit    *db.AuditStore
}

// firewallDevice: what a device of the network should run and what it last
// said it runs. Expected is empty while the network is not managed. A device
// that never reported has ReportedAt 0: an agent too old to apply rules, or
// one that has not had a heartbeat since.
type firewallDevice struct {
	NodeID      string `json:"nodeId"`
	Expected    string `json:"expected,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
	// Blocked: the device's own switch "Block incoming" is on, so it lets
	// in only what hopssh always keeps, whatever the rules say.
	Blocked    bool  `json:"blocked,omitempty"`
	ReportedAt int64 `json:"reportedAt,omitempty"`
}

type firewallSetResponse struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Version    int64           `json:"version"`
	Rules      []fwpolicy.Rule `json:"rules"`
	UpdatedAt  int64           `json:"updatedAt"`
	NetworkIDs []string        `json:"networkIds,omitempty"`
}

type networkFirewallResponse struct {
	Version   int64                 `json:"version"`
	Preset    string                `json:"preset"`
	Rules     []fwpolicy.Rule       `json:"rules"`
	UpdatedAt int64                 `json:"updatedAt,omitempty"`
	Sets      []firewallSetResponse `json:"sets"`
	// What the caller may do: change the preset and rules (owner, admins),
	// attach and detach sets (owner).
	CanEdit   bool `json:"canEdit"`
	CanAttach bool `json:"canAttach"`
	// Managed: the rules are sent to the network's devices (from its first
	// save or attach on). Until then each keeps the block it was enrolled with.
	Managed bool             `json:"managed"`
	Devices []firewallDevice `json:"devices"`
}

func setResponse(s db.FirewallSet, withNetworks bool) firewallSetResponse {
	r := firewallSetResponse{ID: s.ID, Name: s.Name, Version: s.Version, Rules: s.Rules, UpdatedAt: s.UpdatedAt}
	if withNetworks {
		r.NetworkIDs = s.NetworkIDs
	}
	return r
}

// access answers the request itself unless the user may see the network:
// 404 for a network that does not exist or that the user is not in.
func (h *FirewallHandler) access(w http.ResponseWriter, user *db.UserProfile, networkID string) (*db.Network, authz.Access, bool) {
	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return nil, authz.Access{}, false
	}
	var membership *db.NetworkMember
	if h.Members != nil {
		membership, _ = h.Members.GetMembership(networkID, user.ID)
	}
	access := authz.CheckAccess(user, network, membership)
	if !access.CanView() {
		http.Error(w, "network not found", http.StatusNotFound)
		return nil, access, false
	}
	return network, access, true
}

// GetNetworkFirewall returns a network's preset, own rules and attached sets.
// @Summary      Network firewall rules
// @Tags         firewall
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      200 {object} object
// @Router       /api/networks/{networkID}/firewall [get]
func (h *FirewallHandler) GetNetworkFirewall(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	network, access, ok := h.access(w, user, networkID)
	if !ok {
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
	h.writeNetworkFirewall(w, http.StatusOK, f, sets, devices, access, network.UserID == user.ID)
}

func (h *FirewallHandler) writeNetworkFirewall(w http.ResponseWriter, status int, f *db.NetworkFirewall, sets []db.FirewallSet, devices []firewallDevice, access authz.Access, owner bool) {
	resp := networkFirewallResponse{
		Version: f.Version, Preset: f.Preset, Rules: f.Rules, UpdatedAt: f.UpdatedAt,
		Sets: make([]firewallSetResponse, 0, len(sets)), CanEdit: access.CanAdmin(), CanAttach: owner,
		Managed: f.Managed, Devices: devices,
	}
	for _, s := range sets {
		resp.Sets = append(resp.Sets, setResponse(s, false))
	}
	w.Header().Set("ETag", etag(f.Version))
	writeJSONStatus(w, status, resp)
}

// devices: for each node of the network, the fingerprint of the policy it
// gets from the heartbeat (the same fwpolicy.Resolve) and its last report.
func (h *FirewallHandler) devices(f *db.NetworkFirewall, sets []db.FirewallSet) ([]firewallDevice, error) {
	out := []firewallDevice{}
	if h.Nodes == nil {
		return out, nil
	}
	nodes, err := h.Nodes.ListForNetwork(f.NetworkID)
	if err != nil {
		return nil, err
	}
	reports, err := h.Firewall.Reports(f.NetworkID)
	if err != nil {
		return nil, err
	}
	rules := append([]fwpolicy.Rule(nil), f.Rules...)
	for _, s := range sets {
		rules = append(rules, s.Rules...)
	}
	addrs := nodeAddrs(nodes)
	for _, n := range nodes {
		d := firewallDevice{NodeID: n.ID}
		if f.Managed {
			d.Expected = fwpolicy.Resolve(f.Preset, rules, n.ID, addrs).Fingerprint
		}
		if r, ok := reports[n.ID]; ok {
			d.Fingerprint, d.Error, d.Blocked, d.ReportedAt = r.Fingerprint, r.Error, r.Blocked, r.ReportedAt
		}
		out = append(out, d)
	}
	return out, nil
}

// nodeAddrs maps each node to its mesh address; one not enrolled yet has none.
func nodeAddrs(nodes []*db.Node) map[string]netip.Addr {
	out := make(map[string]netip.Addr, len(nodes))
	for _, n := range nodes {
		if p, err := netip.ParsePrefix(n.NebulaIP); err == nil {
			out[n.ID] = p.Addr()
		} else if a, err := netip.ParseAddr(n.NebulaIP); err == nil {
			out[n.ID] = a
		}
	}
	return out
}

// maxReportError: the longest reason for a refusal that is stored, in
// characters.
const maxReportError = 300

// cleanReport checks a device's report: a fingerprint is empty or 64
// lowercase hex characters, and the reason is cut to maxReportError
// characters, with control characters and invalid UTF-8 made spaces.
func cleanReport(a fwpolicy.Ack) (fwpolicy.Ack, bool) {
	if a.Fingerprint != "" {
		if len(a.Fingerprint) != 64 || strings.Trim(a.Fingerprint, "0123456789abcdef") != "" {
			return a, false
		}
	}
	var b strings.Builder
	n := 0
	for _, r := range a.Error {
		if n == maxReportError {
			break
		}
		if r == utf8.RuneError || unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	a.Error = b.String()
	return a, true
}

// UpdateNetworkFirewall replaces a network's preset and own rules.
// @Summary      Change network firewall rules
// @Tags         firewall
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Param        If-Match header string true "Version the change is made against"
// @Success      200 {object} object
// @Failure      412 {string} string "changed in the meantime"
// @Failure      428 {string} string "If-Match missing"
// @Router       /api/networks/{networkID}/firewall [put]
func (h *FirewallHandler) UpdateNetworkFirewall(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	network, access, ok := h.access(w, user, networkID)
	if !ok {
		return
	}
	if !access.CanAdmin() {
		http.Error(w, "admin access required", http.StatusForbidden)
		return
	}
	ifVersion, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var body struct {
		Preset string          `json:"preset"`
		Rules  []fwpolicy.Rule `json:"rules"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if !fwpolicy.ValidPreset(body.Preset) {
		http.Error(w, "preset must be all, tcp or ping", http.StatusBadRequest)
		return
	}
	if err := fwpolicy.ValidateRules(body.Rules); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	before, f, err := h.Firewall.UpdateNetworkFirewall(networkID, ifVersion, body.Preset, body.Rules, user.ID)
	if !h.storeError(w, err) {
		return
	}
	logAudit(h.Audit, user.ID, "firewall.update", networkID, "", map[string]any{
		"fromVersion": before.Version, "toVersion": f.Version,
		"fromPreset": before.Preset, "preset": f.Preset, "rules": len(f.Rules),
	})
	sets, err := h.Firewall.AttachedSets(networkID)
	if err != nil {
		http.Error(w, "failed to read rule sets", http.StatusInternalServerError)
		return
	}
	// The change is stored: a failed read of the devices leaves them out of
	// the answer rather than report the save as failed.
	devices, err := h.devices(f, sets)
	if err != nil {
		log.Printf("[firewall] network %s: devices: %v", networkID, err)
	}
	h.writeNetworkFirewall(w, http.StatusOK, f, sets, devices, access, network.UserID == user.ID)
}

// ListFirewallSets returns the caller's rule sets.
// @Summary      List my rule sets
// @Tags         firewall
// @Security     BearerAuth
// @Produce      json
// @Success      200 {array} object
// @Router       /api/firewall/sets [get]
func (h *FirewallHandler) ListFirewallSets(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	sets, err := h.Firewall.ListSets(user.ID)
	if err != nil {
		http.Error(w, "failed to read rule sets", http.StatusInternalServerError)
		return
	}
	out := make([]firewallSetResponse, 0, len(sets))
	for _, s := range sets {
		out = append(out, setResponse(s, true))
	}
	writeJSON(w, out)
}

type firewallSetBody struct {
	Name  string          `json:"name"`
	Rules []fwpolicy.Rule `json:"rules"`
}

// decodeStrict decodes a firewall body and refuses fields it does not know:
// a misspelt "sourceNodeID" left out would allow every node.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func decodeSetBody(w http.ResponseWriter, r *http.Request) (*firewallSetBody, bool) {
	var body firewallSetBody
	if !decodeStrict(w, r, &body) {
		return nil, false
	}
	body.Name = strings.TrimSpace(body.Name)
	if err := fwpolicy.ValidateSetName(body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if err := fwpolicy.ValidateRules(body.Rules); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return &body, true
}

// CreateFirewallSet makes a rule set owned by the caller.
// @Summary      Create a rule set
// @Tags         firewall
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Success      201 {object} object
// @Router       /api/firewall/sets [post]
func (h *FirewallHandler) CreateFirewallSet(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	body, ok := decodeSetBody(w, r)
	if !ok {
		return
	}
	set := &db.FirewallSet{ID: uuid.New().String(), UserID: user.ID, Name: body.Name, Rules: body.Rules}
	if set.Rules == nil {
		set.Rules = []fwpolicy.Rule{}
	}
	if err := h.Firewall.CreateSet(set); err != nil {
		if db.IsUniqueViolation(err) {
			http.Error(w, "you already have a rule set with that name", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create rule set", http.StatusInternalServerError)
		return
	}
	logAudit(h.Audit, user.ID, "firewall_set.create", "", "", map[string]any{"setId": set.ID, "name": set.Name, "rules": len(set.Rules)})
	w.Header().Set("ETag", etag(set.Version))
	writeJSONStatus(w, http.StatusCreated, setResponse(*set, true))
}

// UpdateFirewallSet changes one of the caller's rule sets, in every network
// it is attached to.
// @Summary      Change a rule set
// @Tags         firewall
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        setID path string true "Rule set ID"
// @Param        If-Match header string true "Version the change is made against"
// @Success      200 {object} object
// @Failure      409 {string} string "a rule set with that name exists"
// @Failure      412 {string} string "changed in the meantime"
// @Router       /api/firewall/sets/{setID} [put]
func (h *FirewallHandler) UpdateFirewallSet(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	setID := chi.URLParam(r, "setID")
	// Not the caller's set: 404, whatever else the request says.
	if set, err := h.Firewall.Set(setID); err != nil {
		http.Error(w, "failed to read rule set", http.StatusInternalServerError)
		return
	} else if set == nil || set.UserID != user.ID {
		http.Error(w, "rule set not found", http.StatusNotFound)
		return
	}
	ifVersion, ok := ifMatch(w, r)
	if !ok {
		return
	}
	body, ok := decodeSetBody(w, r)
	if !ok {
		return
	}
	set, err := h.Firewall.UpdateSet(setID, user.ID, ifVersion, body.Name, body.Rules)
	if db.IsUniqueViolation(err) {
		http.Error(w, "you already have a rule set with that name", http.StatusConflict)
		return
	}
	if !h.storeError(w, err) {
		return
	}
	if set == nil {
		http.Error(w, "rule set not found", http.StatusNotFound)
		return
	}
	logAudit(h.Audit, user.ID, "firewall_set.update", "", "", map[string]any{
		"setId": set.ID, "name": set.Name, "fromVersion": ifVersion, "toVersion": set.Version,
		"rules": len(set.Rules), "networks": len(set.NetworkIDs),
	})
	w.Header().Set("ETag", etag(set.Version))
	writeJSON(w, setResponse(*set, true))
}

// DeleteFirewallSet deletes one of the caller's rule sets. One that is
// attached to a network is refused with 409: detach it first.
// @Summary      Delete a rule set
// @Tags         firewall
// @Security     BearerAuth
// @Param        setID path string true "Rule set ID"
// @Success      204
// @Failure      409 {string} string "attached to a network"
// @Router       /api/firewall/sets/{setID} [delete]
func (h *FirewallHandler) DeleteFirewallSet(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	setID := chi.URLParam(r, "setID")
	set, err := h.Firewall.Set(setID)
	if err != nil {
		http.Error(w, "failed to read rule set", http.StatusInternalServerError)
		return
	}
	if set == nil || set.UserID != user.ID {
		http.Error(w, "rule set not found", http.StatusNotFound)
		return
	}
	found, err := h.Firewall.DeleteSet(setID, user.ID)
	if errors.Is(err, db.ErrSetAttached) {
		http.Error(w, "the rule set is attached to a network: detach it first", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "failed to delete rule set", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "rule set not found", http.StatusNotFound)
		return
	}
	logAudit(h.Audit, user.ID, "firewall_set.delete", "", "", map[string]any{"setId": setID, "name": set.Name})
	w.WriteHeader(http.StatusNoContent)
}

// attachTarget checks that the caller owns the network and the set. Admin
// members get 403: attaching brings in rules only the owner can change.
func (h *FirewallHandler) attachTarget(w http.ResponseWriter, r *http.Request) (*db.UserProfile, *db.FirewallSet, string, bool) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")
	network, _, ok := h.access(w, user, networkID)
	if !ok {
		return nil, nil, "", false
	}
	if network.UserID != user.ID {
		http.Error(w, "only the network's owner attaches rule sets", http.StatusForbidden)
		return nil, nil, "", false
	}
	set, err := h.Firewall.Set(chi.URLParam(r, "setID"))
	if err != nil {
		http.Error(w, "failed to read rule set", http.StatusInternalServerError)
		return nil, nil, "", false
	}
	if set == nil || set.UserID != user.ID {
		http.Error(w, "rule set not found", http.StatusNotFound)
		return nil, nil, "", false
	}
	return user, set, networkID, true
}

// AttachFirewallSet attaches one of the caller's rule sets to their network.
// @Summary      Attach a rule set
// @Tags         firewall
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Param        setID path string true "Rule set ID"
// @Success      204
// @Router       /api/networks/{networkID}/firewall/sets/{setID} [put]
func (h *FirewallHandler) AttachFirewallSet(w http.ResponseWriter, r *http.Request) {
	user, set, networkID, ok := h.attachTarget(w, r)
	if !ok {
		return
	}
	already, err := h.Firewall.AttachSet(networkID, set.ID)
	if !h.storeError(w, err) {
		return
	}
	if !already {
		logAudit(h.Audit, user.ID, "firewall_set.attach", networkID, "", map[string]any{"setId": set.ID, "name": set.Name, "version": set.Version})
	}
	w.WriteHeader(http.StatusNoContent)
}

// DetachFirewallSet removes a rule set from the caller's network.
// @Summary      Detach a rule set
// @Tags         firewall
// @Security     BearerAuth
// @Param        networkID path string true "Network ID"
// @Param        setID path string true "Rule set ID"
// @Success      204
// @Router       /api/networks/{networkID}/firewall/sets/{setID} [delete]
func (h *FirewallHandler) DetachFirewallSet(w http.ResponseWriter, r *http.Request) {
	user, set, networkID, ok := h.attachTarget(w, r)
	if !ok {
		return
	}
	was, err := h.Firewall.DetachSet(networkID, set.ID)
	if err != nil {
		http.Error(w, "failed to detach rule set", http.StatusInternalServerError)
		return
	}
	if was {
		logAudit(h.Audit, user.ID, "firewall_set.detach", networkID, "", map[string]any{"setId": set.ID, "name": set.Name})
	}
	w.WriteHeader(http.StatusNoContent)
}

// storeError answers the request for an error of the firewall store and
// reports whether there was none.
func (h *FirewallHandler) storeError(w http.ResponseWriter, err error) bool {
	var (
		unknown *db.UnknownNodesError
		wide    *db.TooManyPortsError
	)
	switch {
	case err == nil:
		return true
	case errors.Is(err, db.ErrStaleVersion):
		// RFC 9110 13.1.1: a false If-Match is answered with 412.
		http.Error(w, err.Error()+": reload and try again", http.StatusPreconditionFailed)
	case errors.As(err, &unknown), errors.As(err, &wide):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "failed to store firewall rules", http.StatusInternalServerError)
	}
	return false
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// ifMatch reads If-Match: "<version>" (quotes optional). 428 without it,
// 400 when it is not one version (a weak tag, a list or * included).
func ifMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		http.Error(w, "If-Match with the version is required", http.StatusPreconditionRequired)
		return 0, false
	}
	n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
	if err != nil || n < 0 {
		http.Error(w, "If-Match must be a version", http.StatusBadRequest)
		return 0, false
	}
	return n, true
}
