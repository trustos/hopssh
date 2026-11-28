// hop-field records how hopssh behaves on a real machine. It is a test
// tool, not part of the product, and reads only what the agent already
// offers: the local API, the agent's log and the network.
//
//	hop-field record   one sample of this machine, appended to rec.jsonl
//	hop-field echo     answers the session probe of another machine
//	hop-field hold     holds a connection open and writes down every silence
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "record":
		err = runRecord(os.Args[2:])
	case "echo":
		err = runEcho(os.Args[2:])
	case "hold":
		err = runHold(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hop-field:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hop-field record|echo|hold [flags]")
	os.Exit(2)
}

// defaultDir is where everything is written unless -dir says otherwise.
func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "hopssh-field"
	}
	return filepath.Join(home, "hopssh-field")
}
