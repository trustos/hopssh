package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The session probe is the lab's Session on real machines: one TCP
// connection, a short message every 100 ms and back. TCP repeats what is
// lost, so a path that is gone for a while shows as a silence, and a
// connection that breaks shows as "lost": that is an SSH session that died.

const (
	probeEvery   = 100 * time.Millisecond
	silenceAfter = time.Second     // shorter gaps are not written down
	giveUpAfter  = 3 * time.Minute // then the connection counts as lost
)

func runEcho(args []string) error {
	fs := flag.NewFlagSet("echo", flag.ExitOnError)
	listen := fs.String("listen", ":47600", "address to listen on")
	fs.Parse(args)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			r := bufio.NewReader(c)
			for {
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				if _, err := c.Write(line); err != nil {
					return
				}
			}
		}()
	}
}

// event is one line of hold.jsonl.
type event struct {
	T      string  `json:"t"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"` // connected, silence, lost, minute
	Sec    float64 `json:"sec,omitempty"`
	From   string  `json:"from,omitempty"` // when a silence began
	Err    string  `json:"err,omitempty"`
	Sent   int     `json:"sent,omitempty"`
	Got    int     `json:"got,omitempty"`
	MaxGap float64 `json:"maxGapSec,omitempty"`
}

type eventLog struct {
	mu sync.Mutex
	f  *os.File
}

func (l *eventLog) write(e event) {
	e.T = time.Now().Format(time.RFC3339Nano)
	b, _ := json.Marshal(e)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Write(append(b, '\n'))
}

func runHold(args []string) error {
	fs := flag.NewFlagSet("hold", flag.ExitOnError)
	to := fs.String("to", "", "targets, name=host:port, separated by commas")
	dir := fs.String("dir", defaultDir(), "directory for hold.jsonl")
	fs.Parse(args)
	if *to == "" {
		return fmt.Errorf("hold: -to is required")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(*dir, "hold.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	out := &eventLog{f: f}
	var wg sync.WaitGroup
	for _, t := range strings.Split(*to, ",") {
		name, addr, ok := strings.Cut(strings.TrimSpace(t), "=")
		if !ok {
			return fmt.Errorf("hold: target %q is not name=host:port", t)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				holdOnce(out, name, addr)
				time.Sleep(time.Second)
			}
		}()
	}
	wg.Wait()
	return nil
}

// holdOnce opens one connection and stays on it until it breaks.
func holdOnce(out *eventLog, name, addr string) {
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		// A machine that is away is not news every second.
		time.Sleep(4 * time.Second)
		return
	}
	defer c.Close()
	out.write(event{Name: name, Kind: "connected", Sec: time.Since(start).Seconds()})

	var mu sync.Mutex
	lastGot := time.Now()
	sent, got := 0, 0
	maxGap := 0.0
	done := make(chan error, 2)

	// answer notes one echo and writes down the silence before it.
	answer := func() {
		now := time.Now()
		mu.Lock()
		gap := now.Sub(lastGot)
		from := lastGot
		lastGot = now
		got++
		if gap.Seconds() > maxGap {
			maxGap = gap.Seconds()
		}
		mu.Unlock()
		if gap >= silenceAfter {
			out.write(event{Name: name, Kind: "silence", Sec: gap.Seconds(), From: from.Format(time.RFC3339Nano)})
		}
	}

	go func() {
		r := bufio.NewReader(c)
		for {
			if _, err := r.ReadBytes('\n'); err != nil {
				done <- err
				return
			}
			answer()
		}
	}()
	go func() {
		tick := time.NewTicker(probeEvery)
		defer tick.Stop()
		minute := time.NewTicker(time.Minute)
		defer minute.Stop()
		for {
			select {
			case <-tick.C:
				mu.Lock()
				quiet := time.Since(lastGot)
				mu.Unlock()
				if quiet > giveUpAfter {
					done <- fmt.Errorf("no answer for %s", quiet.Round(time.Second))
					return
				}
				// A full send buffer must not block the clock: what is
				// not sent now is simply not sent.
				c.SetWriteDeadline(time.Now().Add(probeEvery))
				if _, err := fmt.Fprintf(c, "%d\n", time.Now().UnixNano()); err != nil {
					if ne, ok := err.(net.Error); ok && ne.Timeout() {
						continue
					}
					done <- err
					return
				}
				mu.Lock()
				sent++
				mu.Unlock()
			case <-minute.C:
				mu.Lock()
				e := event{Name: name, Kind: "minute", Sent: sent, Got: got, MaxGap: maxGap}
				sent, got, maxGap = 0, 0, 0
				mu.Unlock()
				out.write(e)
			}
		}
	}()

	err = <-done
	mu.Lock()
	quiet := time.Since(lastGot)
	mu.Unlock()
	out.write(event{Name: name, Kind: "lost", Sec: quiet.Seconds(), Err: err.Error()})
}
