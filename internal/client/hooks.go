package client

import "os/exec"

// execCommand is a wrappable exec.Command for testing. enroll.go and any
// other moved file that shells out should use this instead of exec.Command
// directly so tests can intercept the call.
//
// Mirror of cmd/agent's package-level execCommand (which still exists for
// the agent-side handlers). The two are independent.
var execCommand = exec.Command
