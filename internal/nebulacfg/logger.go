package nebulacfg

import (
	"log/slog"
	"os"

	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/logging"
)

// Logger is the logger a Nebula instance gets: on stderr, at the level its
// config names ("info" when it names none). Up to v1.10.3 Nebula set that
// level itself inside Main, whatever the caller had set before; since v1.11
// the caller has to.
func Logger(c *config.C) (*slog.Logger, error) {
	l := logging.NewLogger(os.Stderr)
	return l, logging.ApplyConfig(l, c)
}
