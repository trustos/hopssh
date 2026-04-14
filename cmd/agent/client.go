package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/trustos/hopssh/internal/nebulacfg"
)

const clientDir = "/etc/hop-client"

type joinResponse struct {
	NodeID         string `json:"nodeId"`
	CACert         string `json:"caCert"`
	NodeCert       string `json:"nodeCert"`
	NodeKey        string `json:"nodeKey"`
	AgentToken     string `json:"agentToken"`
	ServerIP       string `json:"serverIP"`
	NebulaIP       string `json:"nebulaIP"`
	LighthousePort int    `json:"lighthousePort"`
	LighthouseHost string `json:"lighthouseHost"`
	DNSDomain      string `json:"dnsDomain"`
}

// runClient handles `hop client join` — join a mesh network as a client device.
func runClient(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: hop-agent client <command>")
		fmt.Println("Commands:")
		fmt.Println("  join    Join a mesh network")
		os.Exit(1)
	}

	switch args[0] {
	case "join":
		runClientJoin(args[1:])
	default:
		fmt.Printf("Unknown client command: %s\n", args[0])
		os.Exit(1)
	}
}

func runClientJoin(args []string) {
	fs := flag.NewFlagSet("client join", flag.ExitOnError)
	networkID := fs.String("network", "", "Network ID to join")
	endpoint := fs.String("endpoint", defaultEndpoint, "Control plane URL")
	token := fs.String("token", "", "Auth token (or uses session cookie)")
	fs.Parse(args)

	if *networkID == "" {
		log.Fatal("--network is required")
	}

	fmt.Println("  Joining network...")

	// Call the join endpoint to get a client certificate.
	reqBody := fmt.Sprintf(`{"hostname":%q,"os":%q,"arch":%q}`,
		getHostname(), runtime.GOOS, runtime.GOARCH)

	req, err := http.NewRequest("POST", *endpoint+"/api/networks/"+*networkID+"/join", strings.NewReader(reqBody))
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to join network: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Join failed (HTTP %d): %s", resp.StatusCode, body)
	}

	var jr joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		log.Fatalf("Failed to parse join response: %v", err)
	}

	// Write certs and config.
	os.MkdirAll(clientDir, 0700)
	writeFileSecure(filepath.Join(clientDir, "ca.crt"), []byte(jr.CACert), 0644)
	writeFileSecure(filepath.Join(clientDir, "node.crt"), []byte(jr.NodeCert), 0644)
	writeFileSecure(filepath.Join(clientDir, "node.key"), []byte(jr.NodeKey), 0600)
	writeFileSecure(filepath.Join(clientDir, "token"), []byte(jr.AgentToken), 0600)
	writeFileSecure(filepath.Join(clientDir, "endpoint"), []byte(*endpoint), 0600)
	writeFileSecure(filepath.Join(clientDir, "node-id"), []byte(jr.NodeID), 0600)

	// Generate Nebula config for client mode.
	serverHost := jr.LighthouseHost
	if serverHost == "" {
		serverHost = extractHost(*endpoint)
	}
	writeClientNebulaConfig(jr.ServerIP, serverHost, jr.LighthousePort)

	fmt.Printf("\n  ✓ Joined network (%s)\n", jr.NebulaIP)
	fmt.Printf("  ✓ DNS domain: .%s\n", jr.DNSDomain)

	// Start cert renewal in background (certs are 24h, renew at 12h).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runCertRenewal(ctx, *endpoint, jr.NodeID, jr.AgentToken)

	// Start embedded Nebula and stay running.
	configPath := filepath.Join(clientDir, "nebula.yaml")
	svc, err := startNebula(configPath)
	if err != nil {
		log.Fatalf("Failed to start Nebula: %v", err)
	}
	nebulaMu.Lock()
	currentNebula = svc
	nebulaMu.Unlock()
	defer svc.Close()

	fmt.Println("  ✓ Connected to mesh")
	fmt.Printf("\n  Try: ping <hostname>.%s\n", jr.DNSDomain)
	fmt.Println("  Press Ctrl+C to disconnect.")

	// Wait for signal.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	fmt.Println("\n  Disconnecting...")
}

func writeClientNebulaConfig(serverIP, serverHost string, lighthousePort int) {
	if lighthousePort == 0 {
		lighthousePort = 42001
	}

	nebulaConfig := fmt.Sprintf(`pki:
  ca: %s/ca.crt
  cert: %s/node.crt
  key: %s/node.key

static_host_map:
  "%s": ["%s:%d"]

lighthouse:
  am_lighthouse: false
  hosts:
    - "%s"

%s
relay:
  relays:
    - "%s"
  use_relays: %t

listen:
  host: 0.0.0.0
  port: 0

punchy:
  punch: true
  respond: true
  punch_back: %t
  delay: %s

tun:
  user: true

firewall:
  outbound:
    - port: any
      proto: any
      host: any
  inbound:
    - port: any
      proto: icmp
      host: any
`, clientDir, clientDir, clientDir,
		serverIP, serverHost, lighthousePort,
		serverIP,
		nebulacfg.PreferredRangesYAML(),
		serverIP, nebulacfg.UseRelays,
		nebulacfg.PunchBack, nebulacfg.PunchDelay)

	writeFileSecure(filepath.Join(clientDir, "nebula.yaml"), []byte(nebulaConfig), 0644)
}

func getHostname() string {
	h, _ := os.Hostname()
	return h
}
