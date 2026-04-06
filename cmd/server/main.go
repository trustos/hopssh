package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/trustos/hopssh/internal/api"
	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/mesh"

	_ "github.com/trustos/hopssh/docs" // swagger generated docs
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
	addr := flag.String("addr", ":8080", "Listen address")
	dataDir := flag.String("data", "./data", "Data directory")
	endpoint := flag.String("endpoint", "http://localhost:8080", "Public URL of this server")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
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
			encKey = string(data)
		} else {
			k, err := generateEncryptionKey()
			if err != nil {
				log.Fatalf("Generate encryption key: %v", err)
			}
			encKey = k
			os.WriteFile(keyFile, []byte(encKey), 0600)
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

	// Initialize mesh manager.
	meshMgr := mesh.NewManager(networks, nodes)
	defer meshMgr.Stop()

	fwdMgr := mesh.NewForwardManager(meshMgr)

	// Initialize handlers.
	authH := &api.AuthHandler{Users: users, Sessions: sessions}
	networkH := &api.NetworkHandler{Networks: networks, Nodes: nodes}
	enrollH := &api.EnrollHandler{Networks: networks, Nodes: nodes, Endpoint: *endpoint}
	proxyH := &api.ProxyHandler{
		MeshManager:    meshMgr,
		ForwardManager: fwdMgr,
		Networks:       networks,
		Nodes:          nodes,
	}

	router := api.NewRouter(users, sessions, authH, networkH, enrollH, proxyH)

	// Clean up expired sessions periodically.
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			sessions.DeleteExpired()
		}
	}()

	log.Printf("hopssh control plane listening on %s", *addr)
	srv := &http.Server{
		Addr:         *addr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // streaming responses
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server: %v", err)
	}
}

func generateEncryptionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}
