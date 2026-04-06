package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// writeJSON encodes v as JSON to w, logging errors instead of silently dropping them.
// Must be called BEFORE WriteHeader, or pass the status via writeJSONStatus.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] json encode error: %v", err)
	}
}

// writeJSONStatus writes a JSON response with a specific HTTP status code.
// Sets Content-Type before WriteHeader to ensure the header is sent.
func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[api] json encode error: %v", err)
	}
}

// --- Request types ---

// RegisterRequest is the request body for user registration.
type RegisterRequest struct {
	Email    string `json:"email" example:"user@example.com"`
	Name     string `json:"name" example:"John Doe"`
	Password string `json:"password" example:"secretpassword"`
}

// LoginRequest is the request body for user login.
type LoginRequest struct {
	Email    string `json:"email" example:"user@example.com"`
	Password string `json:"password" example:"secretpassword"`
}

// CreateNetworkRequest is the request body for creating a network.
type CreateNetworkRequest struct {
	Name string `json:"name" example:"production"`
}

// EnrollRequest is the request body for agent enrollment.
type EnrollRequest struct {
	Token    string `json:"token" example:"a1b2c3d4..."`
	Hostname string `json:"hostname" example:"web-server-1"`
	OS       string `json:"os" example:"linux"`
	Arch     string `json:"arch" example:"arm64"`
}

// ExecRequest is the request body for command execution.
type ExecRequest struct {
	Command string   `json:"command" example:"ls"`
	Args    []string `json:"args" example:"-la,/var/log"`
	Dir     string   `json:"dir,omitempty" example:"/home/ubuntu"`
	Env     []string `json:"env,omitempty" example:"FOO=bar"`
}

// StartPortForwardRequest is the request body for starting a port forward.
type StartPortForwardRequest struct {
	RemotePort int `json:"remotePort" example:"5432"`
	LocalPort  int `json:"localPort" example:"15432"`
}

// --- Response types ---

// AuthResponse is returned after successful registration or login.
type AuthResponse struct {
	ID    string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email string `json:"email" example:"user@example.com"`
	Name  string `json:"name" example:"John Doe"`
	Token string `json:"token" example:"a1b2c3d4e5f6..."`
}

// UserResponse is returned for the current user.
type UserResponse struct {
	ID    string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email string `json:"email" example:"user@example.com"`
	Name  string `json:"name" example:"John Doe"`
}

// StatusResponse indicates whether any users exist.
type StatusResponse struct {
	HasUsers bool `json:"hasUsers" example:"true"`
}

// NetworkResponse is returned when creating or getting a network.
type NetworkResponse struct {
	ID        string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string `json:"name" example:"production"`
	Slug      string `json:"slug" example:"production"`
	Subnet    string `json:"subnet" example:"10.42.1.0/24"`
	NodeCount int    `json:"nodeCount" example:"3"`
	CreatedAt int64  `json:"createdAt" example:"1712361600"`
}

// CreateNodeResponse is returned when creating a node enrollment token.
type CreateNodeResponse struct {
	NodeID          string `json:"nodeId" example:"550e8400-e29b-41d4-a716-446655440000"`
	EnrollmentToken string `json:"enrollmentToken" example:"a1b2c3d4..."`
	InstallCommand  string `json:"installCommand" example:"curl -fsSL https://hopssh.com/install | sudo bash -s -- --token a1b2c3d4..."`
	NebulaIP        string `json:"nebulaIP" example:"10.42.1.2/24"`
}

// EnrollResponse is returned to the agent after successful enrollment.
type EnrollResponse struct {
	CACert     string `json:"caCert" example:"-----BEGIN NEBULA CERTIFICATE-----..."`
	NodeCert   string `json:"nodeCert" example:"-----BEGIN NEBULA CERTIFICATE-----..."`
	NodeKey    string `json:"nodeKey" example:"-----BEGIN NEBULA X25519 PRIVATE KEY-----..."`
	AgentToken string `json:"agentToken" example:"deadbeef1234..."`
	ServerIP   string `json:"serverIP" example:"10.42.1.1"`
	NebulaIP   string `json:"nebulaIP" example:"10.42.1.2/24"`
}

// NodeResponse represents a node in API responses.
type NodeResponse struct {
	ID          string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	NetworkID   string  `json:"networkId" example:"550e8400-e29b-41d4-a716-446655440000"`
	Hostname    string  `json:"hostname" example:"web-server-1"`
	OS          string  `json:"os" example:"linux"`
	Arch        string  `json:"arch" example:"arm64"`
	NebulaIP    string  `json:"nebulaIP" example:"10.42.1.2/24"`
	AgentRealIP *string `json:"agentRealIP" example:"203.0.113.10"`
	Status      string  `json:"status" example:"online"`
	LastSeenAt  *int64  `json:"lastSeenAt" example:"1712361600"`
	CreatedAt   int64   `json:"createdAt" example:"1712361600"`
}

// HealthResponse is returned from the agent health check.
type HealthResponse struct {
	Status   string `json:"status" example:"ok"`
	Hostname string `json:"hostname" example:"web-server-1"`
	OS       string `json:"os" example:"linux"`
	Arch     string `json:"arch" example:"arm64"`
	Uptime   string `json:"uptime" example:"2h30m15s"`
}

// PortForwardResponse represents an active port forward.
type PortForwardResponse struct {
	ID         string `json:"id" example:"fwd-1"`
	NetworkID  string `json:"networkId" example:"550e8400-..."`
	NodeID     string `json:"nodeId" example:"550e8400-..."`
	RemotePort int    `json:"remotePort" example:"5432"`
	LocalPort  int    `json:"localPort" example:"15432"`
	LocalAddr  string `json:"localAddr" example:"127.0.0.1:15432"`
	CreatedAt  int64  `json:"createdAt" example:"1712361600"`
}

// ErrorResponse is returned on errors.
type ErrorResponse struct {
	Error string `json:"error" example:"invalid credentials"`
}

// --- Device flow types ---

// DeviceCodeResponse is returned when requesting a device code.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"deviceCode" example:"abc123..."`
	UserCode        string `json:"userCode" example:"HOP-K9M2"`
	VerificationURI string `json:"verificationURI" example:"/device"`
	ExpiresIn       int    `json:"expiresIn" example:"600"`
	Interval        int    `json:"interval" example:"5"`
}

// DevicePollRequest is the request body for polling device code status.
type DevicePollRequest struct {
	DeviceCode string `json:"deviceCode" example:"abc123..."`
}

// DeviceAuthorizeRequest is the request body for authorizing a device code.
type DeviceAuthorizeRequest struct {
	UserCode  string `json:"userCode" example:"HOP-K9M2"`
	NetworkID string `json:"networkId" example:"550e8400-..."`
}

// BundleResponse is returned when creating an enrollment bundle.
type BundleResponse struct {
	BundleURL string `json:"bundleUrl" example:"https://hopssh.com/api/bundles/abc123"`
	ExpiresIn int    `json:"expiresIn" example:"900"`
}

// --- Cert renewal types ---

// RenewRequest is the request body for certificate renewal.
type RenewRequest struct {
	NodeID string `json:"nodeId" example:"550e8400-e29b-41d4-a716-446655440000"`
}

// RenewResponse is returned after successful cert renewal.
type RenewResponse struct {
	NodeCert  string `json:"nodeCert" example:"-----BEGIN NEBULA CERTIFICATE-----..."`
	NodeKey   string `json:"nodeKey" example:"-----BEGIN NEBULA X25519 PRIVATE KEY-----..."`
	ExpiresIn int    `json:"expiresIn" example:"86400"`
}
