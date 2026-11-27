package client

import (
	"log"

	"github.com/sirupsen/logrus"
)

// logrusSink writes to wherever the standard log writes at the moment the
// line is written.
type logrusSink struct{}

func (logrusSink) Write(p []byte) (int, error) { return log.Default().Writer().Write(p) }

// routeStandardLog points the standard logrus logger, which the port mapper
// uses, at the agent's own log.
//
// logrus took os.Stderr when the process started. A Windows service has no
// console handles, so that write fails: every portmap line was lost, and all
// the file got was logrus's complaint about it, once per start ("Failed to
// write to log, write /dev/stderr: The handle is invalid."). On the other
// systems the two are the same place already, so this changes nothing there
// except that logrus no longer colors its lines.
func routeStandardLog() {
	logrus.StandardLogger().SetOutput(logrusSink{})
}
