package client

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// The port mapper logs with logrus, which took os.Stderr when the process
// started. Under a Windows service that handle does not exist, so every
// portmap line was lost and the file got logrus's complaint about it instead
// ("Failed to write to log, write /dev/stderr: The handle is invalid.").
// The agent's own lines go through the standard log; a portmap line has to
// land in the same place.

func TestStandardLogrus_LandsInTheAgentLog(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	routeStandardLog()
	logrus.StandardLogger().Info("portmap: no protocol available")

	if !strings.Contains(buf.String(), "portmap: no protocol available") {
		t.Fatalf("the line did not reach the agent's log, which holds %q", buf.String())
	}
}

// Where the log goes is read when the line is written, not when the routing
// is done: the Windows service redirects the agent's log after the client is
// already built.
func TestStandardLogrus_FollowsTheLogWhenItMoves(t *testing.T) {
	var first, second bytes.Buffer
	log.SetOutput(&first)
	defer log.SetOutput(os.Stderr)

	routeStandardLog()
	logrus.StandardLogger().Info("before")
	log.SetOutput(&second)
	logrus.StandardLogger().Info("after")

	if !strings.Contains(first.String(), "before") {
		t.Fatalf("the first line is not in the first log: %q", first.String())
	}
	if !strings.Contains(second.String(), "after") {
		t.Fatalf("the second line did not follow the log: %q", second.String())
	}
}
