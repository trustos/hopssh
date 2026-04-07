package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/trustos/hopssh/internal/buildinfo"
	"github.com/trustos/hopssh/internal/selfupdate"
)

func runServerUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "Check for updates without installing")
	version := fs.String("version", "", "Install a specific version (e.g. v0.2.0)")
	force := fs.Bool("force", false, "Update even if already at the latest version")
	fs.Parse(args)

	// Server can check its own endpoint or fall back to GitHub.
	// Since the server IS the control plane, it goes directly to GitHub.
	endpoint := ""

	if *version != "" {
		release := &selfupdate.Release{Version: *version, Source: "github"}
		if err := selfupdate.Apply("server", release, endpoint); err != nil {
			log.Fatal(err)
		}
		return
	}

	release, err := selfupdate.Check("server", endpoint)
	if err != nil {
		log.Fatal(err)
	}

	if release == nil && !*force {
		fmt.Printf("hop-server is already at the latest version (%s)\n", buildinfo.Version)
		return
	}
	if release == nil && *force {
		release = &selfupdate.Release{Version: buildinfo.Version, Source: "github"}
	}

	if *check {
		fmt.Printf("Update available: %s → %s (from %s)\n", buildinfo.Version, release.Version, release.Source)
		return
	}

	if err := selfupdate.Apply("server", release, endpoint); err != nil {
		log.Fatal(err)
	}
}
