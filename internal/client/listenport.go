package client

import (
	"log"
	"os"
	"path/filepath"

	"github.com/trustos/hopssh/internal/nebulacfg"
	"gopkg.in/yaml.v3"
)

// migrateListenPorts assigns deterministic listen ports to any enrollment
// missing one, then heals duplicates and rewrites each enrollment's
// nebula.yaml to match. Idempotent.
//
// Pre-v0.10.26 the server's /api/renew handler pushed a hardcoded
// listenPort=4242 that silently corrupted multi-enrollment hosts; this
// startup check is the v0.10.26 self-heal that ensures we recover even
// if a future regression or a manual edit leaves two enrollments with
// the same port. See CLAUDE.md Discovery Log § "Server stopped pushing
// `listenPort`".
func migrateListenPorts(reg *enrollmentRegistry) {
	updated, err := reg.AssignMissingListenPorts(nebulacfg.ListenPort)
	if err != nil {
		log.Printf("[migrate] WARNING: failed to assign listen ports: %v", err)
		return
	}
	if updated > 0 {
		log.Printf("[migrate] assigned listen ports to %d legacy enrollment(s)", updated)
	}

	renumbered, err := reg.HealDuplicateListenPorts(nebulacfg.ListenPort)
	if err != nil {
		log.Printf("[migrate] WARNING: heal duplicate listen ports: %v", err)
	} else if len(renumbered) > 0 {
		log.Printf("[migrate] healed duplicate listen ports — reassigned: %v", renumbered)
	}

	for _, e := range reg.List() {
		if err := healListenPortYAML(e); err != nil {
			log.Printf("[migrate %s] WARNING: heal listen.port: %v", e.Name, err)
		}
	}
}

// healListenPortYAML rewrites listen.port in this enrollment's
// nebula.yaml if it doesn't match the persisted ListenPort.
// Idempotent — no-op if already in sync.
func healListenPortYAML(e *Enrollment) error {
	cfgPath := filepath.Join(enrollmentDir(configDir, e.Name), "nebula.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}
	listen, _ := cfg["listen"].(map[string]any)
	if listen == nil {
		listen = map[string]any{"host": "0.0.0.0"}
	}
	curPort, _ := listen["port"].(int)
	if curPort == e.ListenPort {
		return nil
	}
	listen["port"] = e.ListenPort
	cfg["listen"] = listen
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, out, 0644); err != nil {
		return err
	}
	log.Printf("[migrate %s] nebula.yaml listen.port updated %d → %d", e.Name, curPort, e.ListenPort)
	return nil
}
