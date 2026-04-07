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
  enroll      Join a mesh network
  status      Show connection status and certificate info
  info        Show node information
  restart     Restart the agent service
  stop        Stop the agent service
  install     Install as a system/user service
  uninstall   Remove the service
  update      Update to the latest version
  version     Print version and exit

Flags:
  --config-dir <path>   Override config directory (default: auto-detected)

Run 'hop-agent <command> --help' for command-specific flags.

Config directory: %s
`, buildinfo.Version, configDir)
}
