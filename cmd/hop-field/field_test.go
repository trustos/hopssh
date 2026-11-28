package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewLogLines(t *testing.T) {
	dir := t.TempDir()
	logf, pos := filepath.Join(dir, "log"), filepath.Join(dir, "pos")
	write := func(s string, flag int) {
		f, err := os.OpenFile(logf, flag|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	write("2026/11/25 10:00:00 [renew home] renewal failed: 401: old\n", os.O_APPEND)
	if got, _ := newLogLines(logf, pos); len(got) != 0 {
		t.Fatalf("the first call must only note the end of the log, got %q", got)
	}
	write("2026/11/25 10:01:00 [renew home] cert OK, renewal due in 3h (next poll in 1m0s)\n"+
		"2026/11/25 10:02:00 [renew home] certificate renewed successfully\n"+
		`time="x" level=info msg="Loaded send_recv_error config" sendRecvError=always`+"\n"+
		`time="x" level=error msg="Failed to send"`+"\n"+
		"2026/11/25 10:03:00 [agent home] WATCHDOG: invoking auto-restart\n", os.O_APPEND)
	got, more := newLogLines(logf, pos)
	if len(got) != 3 || more != 0 {
		t.Fatalf("want the renewal, the error and the watchdog, got %d: %q", len(got), got)
	}
	if got, _ := newLogLines(logf, pos); len(got) != 0 {
		t.Fatalf("nothing new, got %q", got)
	}
	// A rotated log is shorter than where we stood: read it from its start.
	write("2026/11/26 00:00:00 [renew work] certificate renewed successfully\n", os.O_TRUNC)
	if got, _ := newLogLines(logf, pos); len(got) != 1 || !strings.Contains(got[0], "work") {
		t.Fatalf("after a rotation want the one new line, got %q", got)
	}
}

// TestHoldWritesSilence puts a line between hold and echo that stops
// carrying for a while, as a path that is gone does, and then is cut.
func TestHoldWritesSilence(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					c.Write(line)
				}
			}()
		}
	}()

	var stalled atomic.Bool
	line, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer line.Close()
	conns := make(chan net.Conn, 4)
	go func() {
		for {
			a, err := line.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", echo.Addr().String())
			if err != nil {
				return
			}
			conns <- a
			carry := func(dst, src net.Conn) {
				buf := make([]byte, 4096)
				for {
					n, err := src.Read(buf)
					if err != nil {
						dst.Close()
						return
					}
					for stalled.Load() {
						time.Sleep(10 * time.Millisecond)
					}
					dst.Write(buf[:n])
				}
			}
			go carry(b, a)
			go carry(a, b)
		}
	}()

	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "hold.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		holdOnce(&eventLog{f: f}, "t", line.Addr().String())
		close(done)
	}()

	time.Sleep(400 * time.Millisecond)
	stalled.Store(true)
	time.Sleep(1500 * time.Millisecond)
	stalled.Store(false)
	time.Sleep(400 * time.Millisecond)
	(<-conns).Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hold did not notice that the connection was cut")
	}

	f.Seek(0, io.SeekStart)
	var kinds []string
	var silence float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		kinds = append(kinds, e.Kind)
		if e.Kind == "silence" {
			silence = e.Sec
		}
	}
	if strings.Join(kinds, " ") != "connected silence lost" {
		t.Fatalf("want connected, one silence, lost; got %v", kinds)
	}
	if silence < 1.4 || silence > 2.2 {
		t.Fatalf("the line stood still for 1.5 s, the silence says %.2f s", silence)
	}
}
