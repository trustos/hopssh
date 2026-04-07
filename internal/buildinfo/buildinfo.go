package buildinfo

// Version and Commit are set at build time via -ldflags.
// Example: go build -ldflags="-X github.com/trustos/hopssh/internal/buildinfo.Version=v0.1.0"
var (
	Version = "dev"
	Commit  = "unknown"
)
