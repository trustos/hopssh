//go:build cgo

package main

// Phase L slice 2 — agent-side clipboard sync.
//
// Per-enrollment goroutine that:
//
//   1. Watches the local OS clipboard (text only in v1) via
//      golang.design/x/clipboard. CGO-bound to NSPasteboard /
//      Win32 Clipboard / X11 selection / Wayland data-control.
//      On headless Linux (no DISPLAY/WAYLAND_DISPLAY), Init()
//      fails and the goroutine exits cleanly — clipboard sync
//      simply doesn't run on that machine.
//
//   2. POSTs each fresh local clip to the control plane's
//      /api/clipboard/announce endpoint. Loop-prevention:
//      remembers the hash of any clip we just wrote ourselves
//      (see step 3) and suppresses re-broadcast within 1 s.
//
//   3. Short-polls /api/clipboard/poll every 2 s for clips
//      originating on OTHER nodes in the same network. On a
//      hit: hash-dedup vs the last-known clip; if new, write
//      to the local clipboard. The hash + write timestamp are
//      stamped on `lastWriteHash`/`lastWriteAt` so the watcher
//      in step 1 doesn't re-announce our own remote-applied
//      write.
//
// Privacy + safety:
//   - Only spawns when inst.enrollment.ClipboardSync == true
//     (off by default; opt-in per-(device, network)).
//   - 256 KB hard cap on outgoing announcements (server enforces
//     same cap; agent skips locally).
//   - 1 s suppression window prevents the receive→write→watch
//     →rebroadcast loop classic to clipboard sync.
//   - Concealed-pasteboard items (1Password, Bitwarden) are
//     filtered out at the OS layer — the user-typed-password
//     case never reaches the announce body. macOS hooks the
//     `org.nspasteboard.ConcealedType` UTI; v1 doesn't yet
//     filter on Windows / Linux (deferred — explicit limitation
//     in /docs/features.md).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	xclip "golang.design/x/clipboard"

	"github.com/google/uuid"
)

// clipboardSendMaxBytes mirrors the server-side cap so we don't
// announce content the server will reject anyway. Agent-side check
// shortcuts the round trip + skips needless network use.
const clipboardSendMaxBytes = 256 * 1024

// clipboardWriteSuppress is how long after we just wrote-from-remote
// we ignore local change events. Enough to absorb the watcher's
// 1-tick latency on every supported OS.
const clipboardWriteSuppress = 1500 * time.Millisecond

// clipboardPollInterval governs the receive cadence. 2 s is the
// floor before users perceive sluggishness; combined with the
// 120 s server-side TTL we sit comfortably inside Apple Universal
// Clipboard's perceived UX window.
const clipboardPollInterval = 2 * time.Second

// clipboardSync is one agent-side coordinator per enrollment.
// Created on demand when enrollment.ClipboardSync flips on; closed
// when the enrollment is left or the toggle is flipped off.
type clipboardSync struct {
	inst         *meshInstance
	endpoint     string
	nodeID       string
	bearerToken  string

	mu             sync.Mutex
	lastWriteHash  string
	lastWriteAt    time.Time
	lastSeenClipID string
	lastSeenSince  int64
	httpClient     *http.Client
}

// startClipboardSync spawns a clipboard-sync goroutine for inst.
// Returns nil if the OS clipboard isn't available (e.g. headless
// Linux without DISPLAY) — the caller continues without sync.
func startClipboardSync(ctx context.Context, inst *meshInstance, endpoint, nodeID, token string) *clipboardSync {
	if err := xclip.Init(); err != nil {
		log.Printf("[clipboard %s] init failed (no display server?): %v — clipboard sync disabled for this enrollment",
			inst.name(), err)
		return nil
	}
	cs := &clipboardSync{
		inst:        inst,
		endpoint:    endpoint,
		nodeID:      nodeID,
		bearerToken: token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			// HTTPS-only DisableKeepAlives matches the rest of the
			// agent's control-plane client (see CLAUDE.md "HTTP
			// keep-alives must be DISABLED" entry).
			Transport: &http.Transport{DisableKeepAlives: true},
		},
	}
	go cs.runWatcher(ctx)
	go cs.runReceiver(ctx)
	log.Printf("[clipboard %s] started (announce + 2s poll)", inst.name())
	return cs
}

// runWatcher watches local clipboard changes and announces fresh
// content (i.e. content the agent didn't itself just write from a
// remote announce) to the control plane.
func (c *clipboardSync) runWatcher(ctx context.Context) {
	ch := xclip.Watch(ctx, xclip.FmtText)
	for {
		select {
		case <-ctx.Done():
			return
		case content, ok := <-ch:
			if !ok {
				return
			}
			if len(content) == 0 || len(content) > clipboardSendMaxBytes {
				continue
			}
			h := hashClipboard(content)
			c.mu.Lock()
			isOurOwnRemoteWrite := c.lastWriteHash == h &&
				time.Since(c.lastWriteAt) < clipboardWriteSuppress
			c.mu.Unlock()
			if isOurOwnRemoteWrite {
				continue
			}
			if err := c.announce(content, h); err != nil {
				log.Printf("[clipboard %s] announce failed: %v", c.inst.name(), err)
			}
		}
	}
}

// runReceiver polls /api/clipboard/poll for the latest clip on this
// agent's network from another node. On a hit, writes the content
// to the local clipboard and stamps lastWriteHash/lastWriteAt so the
// watcher doesn't re-announce.
func (c *clipboardSync) runReceiver(ctx context.Context) {
	t := time.NewTicker(clipboardPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.poll()
		}
	}
}

func (c *clipboardSync) announce(content []byte, hash string) error {
	body, _ := json.Marshal(map[string]any{
		"nodeId":  c.nodeID,
		"clipId":  uuid.NewString(),
		"hash":    hash,
		"type":    "text",
		"size":    len(content),
		"content": content,
	})
	req, err := http.NewRequest(http.MethodPost, c.endpoint+"/api/clipboard/announce", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("announce: %s", resp.Status)
	}
	return nil
}

func (c *clipboardSync) poll() {
	c.mu.Lock()
	since := c.lastSeenSince
	c.mu.Unlock()

	url := fmt.Sprintf("%s/api/clipboard/poll?nodeId=%s&since=%d",
		c.endpoint, c.nodeID, since)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return
	}

	var payload struct {
		ClipID     string `json:"clipId"`
		Hash       string `json:"hash"`
		Type       string `json:"type"`
		Originator string `json:"originator"`
		Ts         int64  `json:"ts"`
		Content    []byte `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return
	}
	if payload.Type != "text" || len(payload.Content) == 0 {
		return
	}

	c.mu.Lock()
	if payload.ClipID == c.lastSeenClipID {
		c.mu.Unlock()
		return
	}
	c.lastSeenClipID = payload.ClipID
	if payload.Ts > c.lastSeenSince {
		c.lastSeenSince = payload.Ts
	}
	c.lastWriteHash = payload.Hash
	c.lastWriteAt = time.Now()
	c.mu.Unlock()

	xclip.Write(xclip.FmtText, payload.Content)
}

// hashClipboard returns the SHA-256 hex of the clipboard payload.
// Used both server-side (announce body) and agent-side (loop break).
func hashClipboard(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
