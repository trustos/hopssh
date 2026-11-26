package client

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Until v0.11.30 the token went to stdout wherever that led. For the system
// service it leads into the agent's log, which every local user can read; with
// the token they could enroll the root agent into a network of their own.
func TestAnnounceLocalAPI_NoTokenInAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hop-agent.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	announceLocalAPI(f, "127.0.0.1:1", "secret-token")
	f.Close()
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "secret-token") {
		t.Errorf("the token was written into a file: %q", got)
	}
}

// The desktop app starts the agent with a pipe as stdout and reads the line.
func TestAnnounceLocalAPI_ToAParent(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	announceLocalAPI(w, "127.0.0.1:1", "secret-token")
	w.Close()
	line, _ := bufio.NewReader(r).ReadString('\n')
	if line != "HOPSSH_LOCAL_API:127.0.0.1:1:secret-token\n" {
		t.Errorf("the parent read %q", line)
	}
}
