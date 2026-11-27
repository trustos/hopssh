package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trustos/hopssh/internal/fwpolicy"
)

// Firewall rules of networks (migration 011,
// docs/wiki/decisions/firewall-rule-sets.md).

var (
	// ErrStaleVersion: the change was made against a version that is no
	// longer the current one.
	ErrStaleVersion = errors.New("the rules were changed in the meantime")
	// ErrSetAttached: a rule set that is attached to a network is not deleted.
	ErrSetAttached = errors.New("the rule set is attached to a network")
)

// UnknownNodesError: rules name nodes that are not in the network.
type UnknownNodesError struct {
	NetworkID string
	IDs       []string
}

func (e *UnknownNodesError) Error() string {
	return fmt.Sprintf("not a device of this network: %s", strings.Join(e.IDs, ", "))
}

// NetworkFirewall is a network's preset and its own rules. Version 0 means
// nothing was ever stored: the preset of the network and no rules.
type NetworkFirewall struct {
	NetworkID string
	Preset    string
	Rules     []fwpolicy.Rule
	Version   int64
	UpdatedAt int64
	UpdatedBy *string
}

// FirewallSet is a rule set a user owns and attaches to their networks.
type FirewallSet struct {
	ID        string
	UserID    string
	Name      string
	Rules     []fwpolicy.Rule
	Version   int64
	CreatedAt int64
	UpdatedAt int64
	// NetworkIDs the set is attached to (filled by ListSets).
	NetworkIDs []string
}

type FirewallStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewFirewallStore(p *DBPair) *FirewallStore {
	return &FirewallStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

// querier is what both *sql.DB and *sql.Tx offer.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

// NetworkFirewall returns the network's preset and own rules, or nil when the
// network does not exist.
func (s *FirewallStore) NetworkFirewall(networkID string) (*NetworkFirewall, error) {
	return networkFirewall(s.rdb, networkID)
}

func networkFirewall(q querier, networkID string) (*NetworkFirewall, error) {
	var (
		f         = NetworkFirewall{NetworkID: networkID}
		rules     sql.NullString
		version   sql.NullInt64
		updatedAt sql.NullInt64
	)
	err := q.QueryRow(`
		SELECT n.firewall_preset, f.rules, f.version, f.updated_at, f.updated_by
		FROM networks n LEFT JOIN network_firewall f ON f.network_id = n.id
		WHERE n.id = ?`, networkID).Scan(&f.Preset, &rules, &version, &updatedAt, &f.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("network firewall: %w", err)
	}
	f.Version, f.UpdatedAt = version.Int64, updatedAt.Int64
	if f.Rules, err = decodeRules(rules.String); err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateNetworkFirewall stores a new preset and own rules, if the stored
// version is still ifVersion, and returns what was stored before and after.
// The rules must be valid (fwpolicy.ValidateRules) and name only nodes of
// the network. A network that does not exist is sql.ErrNoRows.
func (s *FirewallStore) UpdateNetworkFirewall(networkID string, ifVersion int64, preset string, rules []fwpolicy.Rule, userID string) (before, after *NetworkFirewall, err error) {
	text, err := encodeRules(rules)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	before, err = networkFirewall(tx, networkID)
	if err != nil {
		return nil, nil, err
	}
	if before == nil {
		return nil, nil, sql.ErrNoRows
	}
	if before.Version != ifVersion {
		return nil, nil, ErrStaleVersion
	}
	if err := checkNodes(tx, networkID, rules, before.Rules); err != nil {
		return nil, nil, err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE networks SET firewall_preset = ? WHERE id = ?`, preset, networkID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO network_firewall (network_id, rules, version, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(network_id) DO UPDATE SET rules = excluded.rules, version = excluded.version,
			updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		networkID, text, ifVersion+1, now, userID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	if rules == nil {
		rules = []fwpolicy.Rule{}
	}
	return before, &NetworkFirewall{NetworkID: networkID, Preset: preset, Rules: rules, Version: ifVersion + 1, UpdatedAt: now, UpdatedBy: &userID}, nil
}

// ListSets returns the user's rule sets, each with the networks it is
// attached to, by name.
func (s *FirewallStore) ListSets(userID string) ([]FirewallSet, error) {
	rows, err := s.rdb.Query(`
		SELECT id, user_id, name, rules, version, created_at, updated_at
		FROM firewall_sets WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	sets, err := scanSets(rows)
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].NetworkIDs, err = attachedTo(s.rdb, sets[i].ID); err != nil {
			return nil, err
		}
	}
	return sets, nil
}

// Set returns one rule set, or nil.
func (s *FirewallStore) Set(id string) (*FirewallSet, error) {
	rows, err := s.rdb.Query(`
		SELECT id, user_id, name, rules, version, created_at, updated_at
		FROM firewall_sets WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	sets, err := scanSets(rows)
	if err != nil || len(sets) == 0 {
		return nil, err
	}
	if sets[0].NetworkIDs, err = attachedTo(s.rdb, id); err != nil {
		return nil, err
	}
	return &sets[0], nil
}

// CreateSet stores a new rule set at version 1. A name the user already
// has is a unique violation (IsUniqueViolation).
func (s *FirewallStore) CreateSet(set *FirewallSet) error {
	text, err := encodeRules(set.Rules)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	set.Version, set.CreatedAt, set.UpdatedAt = 1, now, now
	_, err = s.wdb.Exec(`
		INSERT INTO firewall_sets (id, user_id, name, rules, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, set.ID, set.UserID, set.Name, text, now, now)
	return err
}

// UpdateSet changes the user's set if its version is still ifVersion. Its
// rules must name only nodes of every network it is attached to. Returns
// nil, nil when the user has no such set.
func (s *FirewallStore) UpdateSet(id, userID string, ifVersion int64, name string, rules []fwpolicy.Rule) (*FirewallSet, error) {
	text, err := encodeRules(rules)
	if err != nil {
		return nil, err
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var (
		version int64
		oldText string
	)
	err = tx.QueryRow(`SELECT version, rules FROM firewall_sets WHERE id = ? AND user_id = ?`, id, userID).Scan(&version, &oldText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if version != ifVersion {
		return nil, ErrStaleVersion
	}
	old, err := decodeRules(oldText)
	if err != nil {
		return nil, err
	}
	networks, err := attachedTo(tx, id)
	if err != nil {
		return nil, err
	}
	for _, n := range networks {
		if err := checkNodes(tx, n, rules, old); err != nil {
			return nil, err
		}
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE firewall_sets SET name = ?, rules = ?, version = ?, updated_at = ? WHERE id = ?`,
		name, text, version+1, now, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &FirewallSet{ID: id, UserID: userID, Name: name, Rules: rules, Version: version + 1, UpdatedAt: now, NetworkIDs: networks}, nil
}

// DeleteSet deletes the user's set unless it is attached to a network
// (ErrSetAttached), in one statement. Reports whether there was such a set.
func (s *FirewallStore) DeleteSet(id, userID string) (bool, error) {
	res, err := s.wdb.Exec(`
		DELETE FROM firewall_sets WHERE id = ? AND user_id = ?
		AND NOT EXISTS (SELECT 1 FROM network_firewall_sets WHERE set_id = ?)`, id, userID, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	var owned int
	if err := s.wdb.QueryRow(`SELECT COUNT(*) FROM firewall_sets WHERE id = ? AND user_id = ?`, id, userID).Scan(&owned); err != nil {
		return false, err
	}
	if owned == 1 {
		return true, ErrSetAttached
	}
	return false, nil
}

// AttachSet attaches a set to a network, if every node its rules name is a
// node of that network. Reports whether it was attached before. The caller
// checks that the user owns both.
func (s *FirewallStore) AttachSet(networkID, setID string) (bool, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var text string
	if err := tx.QueryRow(`SELECT rules FROM firewall_sets WHERE id = ?`, setID).Scan(&text); err != nil {
		return false, err
	}
	rules, err := decodeRules(text)
	if err != nil {
		return false, err
	}
	if err := checkNodes(tx, networkID, rules, nil); err != nil {
		return false, err
	}
	res, err := tx.Exec(`INSERT INTO network_firewall_sets (network_id, set_id, attached_at) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, networkID, setID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, tx.Commit()
}

// DetachSet removes a set from a network. Reports whether it was attached.
func (s *FirewallStore) DetachSet(networkID, setID string) (bool, error) {
	res, err := s.wdb.Exec(`DELETE FROM network_firewall_sets WHERE network_id = ? AND set_id = ?`, networkID, setID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AttachedSets returns the sets attached to a network, by name.
func (s *FirewallStore) AttachedSets(networkID string) ([]FirewallSet, error) {
	rows, err := s.rdb.Query(`
		SELECT s.id, s.user_id, s.name, s.rules, s.version, s.created_at, s.updated_at
		FROM firewall_sets s JOIN network_firewall_sets a ON a.set_id = s.id
		WHERE a.network_id = ? ORDER BY s.name`, networkID)
	if err != nil {
		return nil, err
	}
	return scanSets(rows)
}

// checkNodes refuses rules that name a node outside the network, among the
// IDs that the stored rules (old) do not name yet. One that was accepted
// stays when its node is deleted: it matches nothing, node IDs are never
// given out again, and the rest of the rules can still be changed.
func checkNodes(q querier, networkID string, rules, old []fwpolicy.Rule) error {
	known := map[string]bool{}
	for _, id := range fwpolicy.NodeIDs(old) {
		known[id] = true
	}
	var missing []string
	for _, id := range fwpolicy.NodeIDs(rules) {
		if known[id] {
			continue
		}
		var n int
		if err := q.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ? AND network_id = ?`, id, networkID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return &UnknownNodesError{NetworkID: networkID, IDs: missing}
	}
	return nil
}

func attachedTo(q querier, setID string) ([]string, error) {
	rows, err := q.Query(`SELECT network_id FROM network_firewall_sets WHERE set_id = ? ORDER BY network_id`, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanSets(rows *sql.Rows) ([]FirewallSet, error) {
	defer rows.Close()
	sets := []FirewallSet{}
	for rows.Next() {
		var (
			set  FirewallSet
			text string
		)
		if err := rows.Scan(&set.ID, &set.UserID, &set.Name, &text, &set.Version, &set.CreatedAt, &set.UpdatedAt); err != nil {
			return nil, err
		}
		var err error
		if set.Rules, err = decodeRules(text); err != nil {
			return nil, err
		}
		sets = append(sets, set)
	}
	return sets, rows.Err()
}

func encodeRules(rules []fwpolicy.Rule) (string, error) {
	if rules == nil {
		rules = []fwpolicy.Rule{}
	}
	b, err := json.Marshal(rules)
	return string(b), err
}

func decodeRules(text string) ([]fwpolicy.Rule, error) {
	rules := []fwpolicy.Rule{}
	if text == "" {
		return rules, nil
	}
	if err := json.Unmarshal([]byte(text), &rules); err != nil {
		return nil, fmt.Errorf("stored firewall rules: %w", err)
	}
	return rules, nil
}
