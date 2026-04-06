package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slackhq/nebula/cert"
)

// runCertRenewal runs a background loop that renews the Nebula certificate
// before it expires. Renews at 50% lifetime (12h for a 24h cert).
// Exits the process if the node has been deleted (HTTP 401).
func runCertRenewal(ctx context.Context, endpoint, nodeID, agentToken string) {
	for {
		renewAt, err := timeUntilRenewal()
		if err != nil {
			log.Printf("[renew] failed to determine renewal time: %v (retrying in 5m)", err)
			renewAt = 5 * time.Minute
		}

		log.Printf("[renew] next renewal in %s", renewAt.Truncate(time.Second))

		select {
		case <-ctx.Done():
			return
		case <-time.After(renewAt):
		}

		if err := renewCert(endpoint, nodeID, agentToken); err != nil {
			log.Printf("[renew] renewal failed: %v", err)
			// Retry with backoff: 1m, 2m, 4m, ..., capped at 30m, max 12 attempts.
			backoff := time.Minute
			for attempt := 0; attempt < 12; attempt++ {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if err := renewCert(endpoint, nodeID, agentToken); err != nil {
					log.Printf("[renew] retry %d failed: %v", attempt+1, err)
					backoff *= 2
					if backoff > 30*time.Minute {
						backoff = 30 * time.Minute
					}
					continue
				}
				break // success
			}
		}
	}
}

// timeUntilRenewal reads the current cert and returns the duration until
// renewal should happen (50% of remaining validity).
func timeUntilRenewal() (time.Duration, error) {
	certPEM, err := os.ReadFile(filepath.Join(enrollDir, "node.crt"))
	if err != nil {
		return 0, fmt.Errorf("read cert: %w", err)
	}

	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return 0, fmt.Errorf("parse cert: %w", err)
	}

	notAfter := c.NotAfter()
	remaining := time.Until(notAfter)
	if remaining <= 0 {
		return 0, nil // already expired, renew immediately
	}

	// Renew at 50% of remaining lifetime.
	return remaining / 2, nil
}

// renewCert calls the control plane's /api/renew endpoint to get a fresh cert.
func renewCert(endpoint, nodeID, agentToken string) error {
	reqBody := fmt.Sprintf(`{"nodeId":%q}`, nodeID)
	req, err := http.NewRequest("POST", endpoint+"/api/renew", strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		log.Fatal("[renew] node has been deleted or token revoked — shutting down")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var renewResp struct {
		NodeCert string `json:"nodeCert"`
		NodeKey  string `json:"nodeKey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&renewResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if renewResp.NodeCert == "" || renewResp.NodeKey == "" {
		return fmt.Errorf("empty cert or key in response")
	}

	// Write new cert atomically (temp file + rename) to prevent partial reads.
	certPath := filepath.Join(enrollDir, "node.crt")
	keyPath := filepath.Join(enrollDir, "node.key")

	if err := atomicWrite(certPath, []byte(renewResp.NodeCert), 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := atomicWrite(keyPath, []byte(renewResp.NodeKey), 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	// Signal Nebula to reload certs.
	reloadNebula()

	log.Printf("[renew] certificate renewed successfully")
	return nil
}

// atomicWrite writes data to a temp file then renames it to the target path.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// reloadNebula sends SIGHUP to the Nebula process to reload its certs.
func reloadNebula() {
	// Try systemctl reload first, fallback to finding PID.
	if _, err := runShellCmd("systemctl reload nebula"); err == nil {
		return
	}
	// Fallback: send SIGHUP directly.
	if _, err := runShellCmd("pkill -HUP nebula"); err != nil {
		log.Printf("[renew] failed to signal Nebula to reload (may need manual restart)")
	}
}
