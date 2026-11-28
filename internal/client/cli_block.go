package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// localAPIAddrFile is where the running agent writes its local API's
// address (mode 0600, next to localAPITokenFile), for its own CLI.
const localAPIAddrFile = "local-api-addr"

// RunBlockIncoming is `hop-agent block-incoming on|off [--network <name>]`:
// the switch "Block incoming from other devices" (firewall_block.go). The
// running agent applies it at once, through its local API, so nothing
// restarts and no terminal session ends.
func RunBlockIncoming(args []string) {
	flags := flag.NewFlagSet("block-incoming", flag.ExitOnError)
	network := flags.String("network", "", "Network (enrollment name); required when there is more than one")
	cfgDir := flags.String("config-dir", "", "Override config directory")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: hop-agent block-incoming on|off [--network <name>]\n\n"+
			"on:  other devices of the network reach this one only for what hopssh\n"+
			"     itself needs (the web terminal, health, ping), whatever the network's\n"+
			"     rules allow. Kept across restarts until you switch it off.\n"+
			"off: the network's rules apply again.\n\n")
		flags.PrintDefaults()
	}
	flags.Parse(args)
	rest := flags.Args()
	if len(rest) > 0 { // flags may follow on|off
		flags.Parse(rest[1:])
		rest = append(rest[:1], flags.Args()...)
	}
	if len(rest) != 1 || (rest[0] != "on" && rest[0] != "off") {
		flags.Usage()
		os.Exit(2)
	}
	on := rest[0] == "on"
	if *cfgDir != "" {
		configDir = resolveConfigDir(*cfgDir)
	}

	name := strings.TrimSpace(*network)
	if name == "" {
		reg, err := loadEnrollmentRegistry(configDir)
		if err != nil {
			failCLI("Load enrollments: %v", err)
		}
		switch reg.Len() {
		case 0:
			failCLI("No enrollments.")
		case 1:
			name = reg.List()[0].Name
		default:
			failCLI("Multiple enrollments present (%v); specify --network <name>.", reg.Names())
		}
	}

	out, err := postBlockIncoming(configDir, name, on)
	if err != nil {
		failCLI("%v", err)
	}
	switch {
	case on && out.Applied:
		fmt.Printf("%s: incoming from other devices is blocked, apart from what hopssh needs.\n", name)
	case !on && out.Applied:
		fmt.Printf("%s: the network's rules apply again.\n", name)
	default:
		fmt.Printf("%s: saved (%s); %s\n", name, rest[0], out.Error)
	}
}

type blockIncomingResult struct {
	BlockIncoming bool   `json:"blockIncoming"`
	Applied       bool   `json:"applied"`
	Error         string `json:"error"`
}

// postBlockIncoming asks the agent that runs from dir to set the switch.
func postBlockIncoming(dir, name string, on bool) (blockIncomingResult, error) {
	var out blockIncomingResult
	token, err := os.ReadFile(filepath.Join(dir, localAPITokenFile))
	if err == nil {
		var addr []byte
		addr, err = os.ReadFile(filepath.Join(dir, localAPIAddrFile))
		if err == nil {
			return out, callBlockIncoming(strings.TrimSpace(string(addr)), strings.TrimSpace(string(token)), name, on, &out)
		}
	}
	if errors.Is(err, fs.ErrPermission) {
		return out, fmt.Errorf("can't read the agent's files in %s: run it with sudo", dir)
	}
	return out, fmt.Errorf("the agent is not running from %s (%v)", dir, err)
}

func callBlockIncoming(addr, token, name string, on bool, out *blockIncomingResult) error {
	body, _ := json.Marshal(map[string]any{"enrollment": name, "enabled": on})
	req, err := http.NewRequest("POST", "http://"+addr+"/local/block-incoming", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// The reload has firewallReloadLimit; the rest is local.
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("the agent is not running (%v)", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("the agent refused: %s", e.Error)
		}
		return fmt.Errorf("the agent answered HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

func failCLI(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
