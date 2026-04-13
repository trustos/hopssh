package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trustos/hopssh/internal/api"
	"github.com/trustos/hopssh/internal/buildinfo"
	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/mesh"

	_ "github.com/trustos/hopssh/swagger" // swagger generated docs
)

// @title        hopssh API
// @version      0.1.0
// @description  Browser-based server access through auto-provisioned encrypted mesh tunnels. No SSH keys, no VPN, no bastion hosts.
//
// @contact.name   hopssh
// @contact.url    https://hopssh.com
//
// @license.name  Proprietary
//
// @host      localhost:8080
// @BasePath  /
//
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Session token from login/register. Format: "Bearer {token}"
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("hop-server %s (%s)\n", buildinfo.Version, buildinfo.Commit)
			return
		case "install":
			runServerInstall(os.Args[2:])
			return
		case "uninstall":
			runServerUninstall(os.Args[2:])
			return
		case "update":
			runServerUpdate(os.Args[2:])
			return
		case "healthz":
			// Probe the local server — used by Docker HEALTHCHECK in distroless images.
			resp, err := http.Get("http://localhost:9473/healthz")
			if err != nil || resp.StatusCode != 200 {
				os.Exit(1)
			}
			fmt.Println("ok")
			return
		}
	}

	addr := flag.String("addr", envOrDefault("HOPSSH_ADDR", ":9473"), "Listen address (env: HOPSSH_ADDR)")
	dataDir := flag.String("data", envOrDefault("HOPSSH_DATA", "./data"), "Data directory (env: HOPSSH_DATA)")
	endpoint := flag.String("endpoint", envOrDefault("HOPSSH_ENDPOINT", ""), "Public URL of this server (env: HOPSSH_ENDPOINT, required)")
	lighthouseHost := flag.String("lighthouse-host", envOrDefault("HOPSSH_LIGHTHOUSE_HOST", ""), "Public IP/host for Nebula lighthouse UDP (env: HOPSSH_LIGHTHOUSE_HOST)")
	trustedProxy := flag.Bool("trusted-proxy", os.Getenv("HOPSSH_TRUSTED_PROXY") == "true", "Trust X-Forwarded-Proto header (env: HOPSSH_TRUSTED_PROXY)")
	allowedOrigins := flag.String("allowed-origins", envOrDefault("HOPSSH_ALLOWED_ORIGINS", ""), "Comma-separated CORS origins (env: HOPSSH_ALLOWED_ORIGINS)")
	flag.Parse()

	if *endpoint == "" {
		log.Fatal("--endpoint is required (or set HOPSSH_ENDPOINT env var).\nExample: hop-server --endpoint http://YOUR_IP:9473")
	}

	api.TrustedProxy = *trustedProxy
	api.AllowedOrigins = api.ParseOriginsFlag(*allowedOrigins)

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		log.Fatalf("Create data dir: %v", err)
	}

	// Open database.
	database, err := db.Open(*dataDir + "/hopssh.db")
	if err != nil {
		log.Fatalf("Open database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database.WriteDB); err != nil {
		log.Fatalf("Migrate database: %v", err)
	}

	// Encryption key: from env or auto-generate.
	encKey := os.Getenv("HOPSSH_ENCRYPTION_KEY")
	if encKey == "" {
		keyFile := *dataDir + "/encryption.key"
		if data, err := os.ReadFile(keyFile); err == nil {
			encKey = strings.TrimSpace(string(data))
		} else {
			k, err := generateEncryptionKey()
			if err != nil {
				log.Fatalf("Generate encryption key: %v", err)
			}
			encKey = k
			if err := os.WriteFile(keyFile, []byte(encKey), 0600); err != nil {
				log.Fatalf("Write encryption key file: %v", err)
			}
			log.Printf("Generated encryption key at %s", keyFile)
		}
	}

	enc, err := crypto.NewEncryptor(encKey)
	if err != nil {
		log.Fatalf("Init encryptor: %v", err)
	}

	// Initialize stores.
	users := db.NewUserStore(database)
	sessions := db.NewSessionStore(database)
	networks := db.NewNetworkStore(database, enc)
	nodes := db.NewNodeStore(database, enc)
	audit := db.NewAuditStore(database)
	deviceCodes := db.NewDeviceCodeStore(database)
	bundles := db.NewBundleStore(database)

	dnsRecords := db.NewDNSRecordStore(database)
	members := db.NewNetworkMemberStore(database)
	invites := db.NewInviteStore(database)

	// Initialize network manager (persistent per-network Nebula lighthouse+relay+DNS).
	netMgr, err := mesh.NewNetworkManager(networks, nodes, dnsRecords)
	if err != nil {
		log.Fatalf("Init network manager: %v", err)
	}
	defer netMgr.Stop()

	// Start idle network reaper: checks every 15 minutes, reaps after 2 hours idle.
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	defer reaperCancel()
	netMgr.StartIdleReaper(reaperCtx, 15*time.Minute, 2*time.Hour)

	// Mesh keepalive: dial online agents every 30s to keep NAT mappings alive.
	netMgr.StartMeshKeepalive(reaperCtx, nodes, 30*time.Second)

	fwdMgr := mesh.NewForwardManager(netMgr)
	fwdMgr.StartIdleReaper(reaperCtx)
	audit.StartFlusher(reaperCtx)

	// Initialize handlers.
	authH := &api.AuthHandler{Users: users, Sessions: sessions, Audit: audit}
	networkH := &api.NetworkHandler{Networks: networks, Nodes: nodes, Members: members, NetworkManager: netMgr, ForwardManager: fwdMgr}
	enrollH := &api.EnrollHandler{Networks: networks, Nodes: nodes, NetworkManager: netMgr, Endpoint: *endpoint, LighthouseHost: *lighthouseHost}
	proxyH := &api.ProxyHandler{
		NetworkManager: netMgr,
		ForwardManager: fwdMgr,
		Networks:       networks,
		Nodes:          nodes,
		Members:        members,
		Audit:          audit,
		AllowedOrigins: api.AllowedOrigins,
	}
	networkH.ProxyCache = proxyH
	deviceH := &api.DeviceHandler{
		DeviceCodes:    deviceCodes,
		Networks:       networks,
		Nodes:          nodes,
		NetworkManager: netMgr,
		LighthouseHost: *lighthouseHost,
	}
	bundleH := &api.BundleHandler{
		Networks:       networks,
		Nodes:          nodes,
		Bundles:        bundles,
		Endpoint:       *endpoint,
		LighthouseHost: *lighthouseHost,
	}

	renewH := &api.RenewHandler{Networks: networks, Nodes: nodes}
	dnsH := &api.DNSHandler{Networks: networks, DNSRecords: dnsRecords, NetworkManager: netMgr}

	auditH := &api.AuditHandler{Audit: audit, Networks: networks, Members: members}

	distH := &api.DistributionHandler{Endpoint: *endpoint}
	memberH := &api.MemberHandler{Networks: networks, Members: members, ProxyCache: proxyH}
	inviteH := &api.InviteHandler{Networks: networks, Members: members, Invites: invites}

	eventHub := api.NewEventHub()
	eventsH := &api.EventsHandler{Networks: networks, Members: members, Hub: eventHub}

	// Wire event hub into handlers that should publish events.
	proxyH.EventHub = eventHub
	enrollH.EventHub = eventHub
	deviceH.EventHub = eventHub
	renewH.EventHub = eventHub

	router := api.NewRouter(users, sessions, authH, networkH, enrollH, proxyH, deviceH, bundleH, renewH, dnsH, auditH, distH, memberH, inviteH, eventsH)

	// Clean up expired sessions periodically with graceful shutdown.
	stopCleanup := make(chan struct{})
	go func() {
		hourly := time.NewTicker(1 * time.Hour)
		daily := time.NewTicker(24 * time.Hour)
		defer hourly.Stop()
		defer daily.Stop()
		for {
			select {
			case <-hourly.C:
				if err := sessions.DeleteExpired(); err != nil {
					log.Printf("[cleanup] sessions: %v", err)
				}
				if err := deviceCodes.DeleteExpired(); err != nil {
					log.Printf("[cleanup] device codes: %v", err)
				}
				if err := bundles.DeleteExpired(); err != nil {
					log.Printf("[cleanup] bundles: %v", err)
				}
				if err := invites.DeleteExpired(); err != nil {
					log.Printf("[cleanup] invites: %v", err)
				}
			case <-daily.C:
				// WAL checkpoint + query planner optimization (PocketBase pattern).
				database.WriteDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
				database.WriteDB.Exec("PRAGMA optimize")
				log.Printf("[db] daily maintenance: WAL checkpoint + optimize")
			case <-stopCleanup:
				return
			}
		}
	}()

	log.Printf("hopssh control plane listening on %s", *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           router,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		WriteTimeout:      0, // streaming responses
	}

	// Graceful shutdown on signals.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down control plane...")
	close(stopCleanup)
	audit.Flush()
	fwdMgr.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func generateEncryptionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
