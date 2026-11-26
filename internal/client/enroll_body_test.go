package client

import (
	"encoding/json"
	"runtime"
	"testing"
)

// Until v0.11.30 a node enrolled with a token was listed as "linux" on every
// system: the dashboard showed a Windows machine with the Linux icon.
func TestTokenEnrollBody_ReportsThisSystem(t *testing.T) {
	var got struct{ Token, Hostname, OS, Arch string }
	if err := json.Unmarshal([]byte(tokenEnrollBody("tok", "host")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != "tok" || got.Hostname != "host" {
		t.Errorf("token %q, hostname %q", got.Token, got.Hostname)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("reported %s/%s, this system is %s/%s", got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
}
