package buildinfo

// Version and Commit are set at build time via -ldflags.
// Example: go build -ldflags="-X github.com/trustos/hopssh/internal/buildinfo.Version=v0.1.0"
var (
	Version = "dev"
	Commit  = "unknown"
	// ClientType identifies which packaging the agent is part of:
	//   "desktop" — bundled inside or installed by the macOS .app
	//   "cli"     — standalone hop-agent binary (curl|sh, package manager)
	//   ""        — legacy / dev build that didn't set the flag
	// Surfaced to the control plane via heartbeats; the dashboard's
	// Nodes tab renders a 🖥/⌨ pill so users can tell GUI-managed vs
	// CLI-managed machines apart at a glance.
	//
	// Set via:
	//   -ldflags="-X github.com/trustos/hopssh/internal/buildinfo.ClientType=desktop"
	// Defaults to "" — the server treats empty as unknown and renders "—".
	ClientType = ""
)
