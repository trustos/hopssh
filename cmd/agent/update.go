package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/trustos/hopssh/internal/buildinfo"
	"github.com/trustos/hopssh/internal/selfupdate"
)

func runAgentUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "Check for updates without installing")
	version := fs.String("version", "", "Install a specific version (e.g. v0.2.0)")
	force := fs.Bool("force", false, "Update even if already at the latest version")
	fs.Parse(args)

	loadPrimaryEnrollment()

	// Read endpoint from config if available.
	endpoint := ""
	if data, err := os.ReadFile(filepath.Join(activeEnrollDir(), "endpoint")); err == nil {
		endpoint = strings.TrimSpace(string(data))
	}

	if *version != "" {
		// Specific version requested — skip check, go straight to apply.
		release := &selfupdate.Release{Version: *version, Source: "github"}
		if endpoint != "" {
			release.Source = "control-plane"
		}
		if err := selfupdate.Apply("agent", release, endpoint); err != nil {
			log.Fatal(err)
		}
		return
	}

	release, err := selfupdate.Check("agent", endpoint)
	if err != nil {
		log.Fatal(err)
	}

	if release == nil && !*force {
		fmt.Printf("hop-agent is already at the latest version (%s)\n", buildinfo.Version)
		return
	}
	if release == nil && *force {
		// Force re-download of current version.
		release = &selfupdate.Release{Version: buildinfo.Version, Source: "github"}
		if endpoint != "" {
			release.Source = "control-plane"
		}
	}

	if *check {
		fmt.Printf("Update available: %s → %s (from %s)\n", buildinfo.Version, release.Version, release.Source)
		return
	}

	if err := selfupdate.Apply("agent", release, endpoint); err != nil {
		log.Fatal(err)
	}
}
