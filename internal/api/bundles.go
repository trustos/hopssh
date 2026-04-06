package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/pki"
)

const bundleCertDuration = 24 * time.Hour // short-lived, auto-renewed by agent

// BundleHandler manages enrollment bundle generation and download.
type BundleHandler struct {
	Networks *db.NetworkStore
	Nodes    *db.NodeStore
	Bundles  *db.BundleStore
	Endpoint string
}

// CreateBundle generates a pre-enrolled node and a downloadable bundle.
// Authenticated endpoint.
// @Summary      Create enrollment bundle
// @Description  Creates a pre-enrolled node and generates a single-use download URL for a tarball containing all certs and config.
// @Tags         enrollment
// @Security     BearerAuth
// @Produce      json
// @Param        networkID path string true "Network ID"
// @Success      201 {object} BundleResponse
// @Failure      404 {object} ErrorResponse
// @Router       /api/networks/{networkID}/bundles [post]
func (h *BundleHandler) CreateBundle(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	networkID := chi.URLParam(r, "networkID")

	network, err := h.Networks.Get(networkID)
	if err != nil || network == nil || network.UserID != user.ID {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}

	// Allocate node.
	nextIndex, err := h.Nodes.NextNodeIndex(networkID)
	if err != nil {
		http.Error(w, "failed to allocate node IP: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nextIP, err := pki.NodeAddress(network.NebulaSubnet, nextIndex)
	if err != nil {
		http.Error(w, "failed to allocate node IP: "+err.Error(), http.StatusInternalServerError)
		return
	}

	agentToken := generateToken()
	nodeID := uuid.New().String()

	node := &db.Node{
		ID:         nodeID,
		NetworkID:  networkID,
		NebulaIP:   nextIP.String(),
		AgentToken: agentToken,
		Status:     "pending",
	}
	if err := h.Nodes.Create(node); err != nil {
		http.Error(w, "failed to create node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Issue node certificate.
	nodeCert, err := pki.IssueCert(network.NebulaCACert, network.NebulaCAKey,
		fmt.Sprintf("node-%s", nodeID[:8]), nextIP, []string{"agent"}, bundleCertDuration)
	if err != nil {
		http.Error(w, "failed to issue cert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.Nodes.CompleteEnrollment(nodeID, nodeCert.CertPEM, nodeCert.KeyPEM, "", "", ""); err != nil {
		http.Error(w, "failed to complete enrollment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create bundle record.
	bundle, err := h.Bundles.Create(uuid.New().String(), nodeID)
	if err != nil {
		http.Error(w, "failed to create bundle: "+err.Error(), http.StatusInternalServerError)
		return
	}

	bundleURL := fmt.Sprintf("%s/api/bundles/%s", h.Endpoint, bundle.DownloadToken)

	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"bundleUrl": bundleURL,
		"nodeId":    nodeID,
		"nebulaIP":  node.NebulaIP,
		"expiresIn": int(time.Until(time.Unix(bundle.ExpiresAt, 0)).Seconds()),
	})
}

// DownloadBundle serves the tarball for a given download token.
// Public endpoint — the token IS the auth.
// @Summary      Download enrollment bundle
// @Description  Downloads a pre-generated tarball containing agent certs and config. Single-use, expires after 15 minutes.
// @Tags         enrollment
// @Produce      application/gzip
// @Param        token path string true "Download token"
// @Success      200 {file} file "Tarball"
// @Failure      404 {object} ErrorResponse
// @Router       /api/bundles/{token} [get]
func (h *BundleHandler) DownloadBundle(w http.ResponseWriter, r *http.Request) {
	// Refuse to serve private key material over plain HTTP.
	if r.TLS == nil && !(TrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		http.Error(w, "bundle download requires HTTPS", http.StatusForbidden)
		return
	}

	token := chi.URLParam(r, "token")

	bundle, err := h.Bundles.ClaimByToken(token)
	if err != nil || bundle == nil {
		http.Error(w, "bundle not found, already downloaded, or expired", http.StatusNotFound)
		return
	}

	node, err := h.Nodes.Get(bundle.NodeID)
	if err != nil || node == nil {
		http.Error(w, "node not found", http.StatusInternalServerError)
		return
	}

	network, err := h.Networks.Get(node.NetworkID)
	if err != nil || network == nil {
		http.Error(w, "network not found", http.StatusInternalServerError)
		return
	}

	serverIP, _ := pki.ServerAddress(network.NebulaSubnet)

	// Build the config file content.
	config := fmt.Sprintf(`{
  "nodeId": %q,
  "networkId": %q,
  "agentToken": %q,
  "serverIP": %q,
  "nebulaIP": %q,
  "endpoint": %q
}
`, node.ID, node.NetworkID, node.AgentToken, serverIP.Addr().String(), node.NebulaIP, h.Endpoint)

	// Write tarball.
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=hop-bundle-%s.tar.gz", node.ID[:8]))

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	addFile := func(name string, data []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: mode,
			Size: int64(len(data)),
		}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	files := []struct {
		name string
		data []byte
		mode int64
	}{
		{"etc/hop-agent/ca.crt", network.NebulaCACert, 0644},
		{"etc/hop-agent/node.crt", node.NebulaCert, 0644},
		{"etc/hop-agent/node.key", node.NebulaKey, 0600},
		{"etc/hop-agent/token", []byte(node.AgentToken), 0600},
		{"etc/hop-agent/config.json", []byte(config), 0600},
	}
	for _, f := range files {
		if err := addFile(f.name, f.data, f.mode); err != nil {
			log.Printf("[bundles] tarball write error for %s: %v", f.name, err)
			return
		}
	}
}
