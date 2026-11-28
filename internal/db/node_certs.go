package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/trustos/hopssh/internal/blocklist"
	"github.com/trustos/hopssh/internal/nebulacfg"
)

// ErrNodeGone: the node was deleted while its certificate was being made.
// The certificate is not stored, so it must not be handed out either.
var ErrNodeGone = errors.New("the node no longer exists")

// ErrTooManyCerts: the node holds MaxHeldCerts certificates that have not
// expired. The new one is not stored and must not be handed out.
var ErrTooManyCerts = errors.New("the node holds too many certificates that have not expired")

// MaxHeldCerts bounds the certificates of one node that have not expired.
// A device renews at half its certificate's life, about twice a day; only a
// flood of renewals (a stolen token) reaches this. Without the bound such a
// flood could push one of its certificates off a deleted node's blocklist,
// which is cut at blocklist.MaxEntries: with it, that takes
// MaxEntries/MaxHeldCerts devices deleted within a day.
const MaxHeldCerts = 16

// heldCerts counts the certificates of node id that Nebula still accepts.
func heldCerts(tx *sql.Tx, id string, now time.Time) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM node_certs WHERE node_id = ? AND not_after >= ?`,
		id, now.Add(-nebulacfg.ClockSkewTolerance).Unix()).Scan(&n)
	return n, err
}

// recordCert records the certificate given to node id, inside tx, where the
// node's row exists (its network is read from it). Rows whose certificate
// Nebula refuses anyway are removed.
func recordCert(tx *sql.Tx, id string, certPEM []byte, now time.Time) error {
	fp, notAfter, err := blocklist.Fingerprint(certPEM)
	if err != nil {
		return fmt.Errorf("the node's certificate: %w", err)
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO node_certs (fingerprint, network_id, node_id, not_after)
		SELECT ?, network_id, id, ? FROM nodes WHERE id = ?`, fp, notAfter.Unix(), id)
	if err != nil {
		return fmt.Errorf("record the node's certificate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM nodes WHERE id = ?`, id).Scan(&one); err != nil {
			return ErrNodeGone
		}
	}
	_, err = tx.Exec(`DELETE FROM node_certs WHERE not_after < ?`, now.Add(-nebulacfg.ClockSkewTolerance).Unix())
	return err
}

// Blocklist is what a network's devices must refuse at now: every
// certificate the server gave a node of the network that no longer exists,
// until Nebula refuses it as expired (NotAfter plus the clock-skew
// tolerance). Derived from the absence of the node's row, so every way of
// deleting one counts. Past blocklist.MaxEntries the certificates that
// expire first are left out, and it says so in the log.
func (s *NodeStore) Blocklist(networkID string, now time.Time) ([]blocklist.Entry, error) {
	rows, err := s.rdb.Query(`
		SELECT c.fingerprint, c.not_after FROM node_certs c
		WHERE c.network_id = ? AND c.not_after >= ?
		  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.id = c.node_id)
		ORDER BY c.not_after DESC, c.fingerprint LIMIT ?`,
		networkID, now.Add(-nebulacfg.ClockSkewTolerance).Unix(), blocklist.MaxEntries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []blocklist.Entry{}
	for rows.Next() {
		var e blocklist.Entry
		var notAfter int64
		if err := rows.Scan(&e.Fingerprint, &notAfter); err != nil {
			return nil, err
		}
		e.Until = notAfter + int64(nebulacfg.ClockSkewTolerance/time.Second)
		out = append(out, e)
	}
	if len(out) == blocklist.MaxEntries {
		log.Printf("[db] network %s: the blocklist holds its %d entries; the certificates of deleted nodes that expire first are not refused",
			networkID, blocklist.MaxEntries)
	}
	return out, rows.Err()
}

// RecordHeldCerts records the certificate each node holds, for nodes whose
// certificate was stored before migration 013. Older certificates of the
// same nodes are not known; they expire within a day. It returns how many
// it added.
func (s *NodeStore) RecordHeldCerts() (int, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, nebula_cert FROM nodes
		WHERE nebula_cert IS NOT NULL AND length(nebula_cert) > 0`)
	if err != nil {
		return 0, err
	}
	type held struct {
		id   string
		cert []byte
	}
	var all []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.cert); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	var before, after int
	if err := tx.QueryRow(`SELECT count(*) FROM node_certs`).Scan(&before); err != nil {
		return 0, err
	}
	now := time.Now()
	for _, h := range all {
		if err := recordCert(tx, h.id, h.cert, now); err != nil {
			log.Printf("[db] node %s: its certificate is not recorded: %v", h.id, err)
		}
	}
	if err := tx.QueryRow(`SELECT count(*) FROM node_certs`).Scan(&after); err != nil {
		return 0, err
	}
	return after - before, tx.Commit()
}
