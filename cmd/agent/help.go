package main

import (
	"fmt"

	"github.com/trustos/hopssh/internal/buildinfo"
)

func runHelp() {
	fmt.Printf(`hop-agent %s — hopssh mesh networking agent

Usage:
  hop-agent <command> [flags]

Commands:
  serve       Start the agent (default if no command given)
  enroll      Join a mesh network (add an enrollment)
  leave       Leave a mesh network (remove an enrollment)
  status      Show status + certificate info for all enrollments
  info        Show node information
  restart     Restart the agent service
  stop        Stop the agent service
  install     Install as a system/user service
  uninstall   Remove the service
  update      Update to the latest version
  version     Print version and exit

Multi-network:
  hop-agent enroll --endpoint <url> [--name <label>]
      Adds an enrollment. Re-running to a second network is supported;
      use --name to pick a human-readable label (default: mesh DNS
      domain, falling back to CA fingerprint).
  hop-agent leave --network <name>
      Removes one enrollment. Required when more than one exists.
  hop-agent status [--network <name>]
      Lists all enrollments by default.

Flags:
  --config-dir <path>   Override config directory (default: auto-detected)

Run 'hop-agent <command> --help' for command-specific flags.

Config directory: %s
`, buildinfo.Version, configDir)
}
