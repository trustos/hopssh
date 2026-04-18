// shelltest is a proactive WebSocket test harness for the agent's /shell
// endpoint. It exists so we can iterate on ConPTY behavior without going
// through the browser → control plane → mesh → agent round trip.
//
// Typical use on a Windows box with the agent running:
//
//	GOOS=windows GOARCH=amd64 go build -o shelltest.exe
//	scp shelltest.exe user@windows-host:.
//	ssh user@windows-host ./shelltest.exe \
//	    --token-file "C:\Users\user\.config\hopssh\token" \
//	    --addr localhost:41820 \
//	    --duration 4s \
//	    --input "echo HOP_TEST_OK_42`r`n"
//
// Runs against any OS, not just Windows — the same binary diagnoses the
// Unix handler too.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	addr := flag.String("addr", "localhost:41820", "agent host:port")
	tokenFile := flag.String("token-file", "", "path to bearer token file")
	token := flag.String("token", "", "bearer token (overrides --token-file)")
	duration := flag.Duration("duration", 4*time.Second, "how long to read output")
	inputDelay := flag.Duration("input-delay", 1500*time.Millisecond, "wait before sending --input")
	input := flag.String("input", "", "optional input to send after input-delay (use \\r\\n for newline, \\x1b etc for escapes)")
	rows := flag.Int("rows", 24, "initial PTY rows")
	cols := flag.Int("cols", 80, "initial PTY cols")
	useTLS := flag.Bool("tls", false, "dial wss:// instead of ws://")
	insecure := flag.Bool("insecure", false, "skip TLS verification")
	previewBytes := flag.Int("preview", 96, "bytes of ASCII preview per frame")
	flag.Parse()

	tok := strings.TrimSpace(*token)
	if tok == "" && *tokenFile != "" {
		data, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Fatalf("read token file %s: %v", *tokenFile, err)
		}
		tok = strings.TrimSpace(string(data))
	}
	if tok == "" {
		log.Fatal("no token (use --token or --token-file)")
	}

	scheme := "ws"
	if *useTLS {
		scheme = "wss"
	}
	u := url.URL{Scheme: scheme, Host: *addr, Path: "/shell"}

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+tok)

	dialer := *websocket.DefaultDialer
	if *insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	dialer.HandshakeTimeout = 5 * time.Second

	logf("DIAL %s", u.String())
	t0 := time.Now()
	conn, resp, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		if resp != nil {
			log.Fatalf("dial: %v (status %d)", err, resp.StatusCode)
		}
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	logf("DIALED in %v, status=%d", time.Since(t0), resp.StatusCode)

	// Resize: binary frame [0x01, rows_hi, rows_lo, cols_hi, cols_lo].
	r16, c16 := uint16(*rows), uint16(*cols)
	resize := []byte{0x01, byte(r16 >> 8), byte(r16 & 0xff), byte(c16 >> 8), byte(c16 & 0xff)}
	if err := conn.WriteMessage(websocket.BinaryMessage, resize); err != nil {
		log.Fatalf("write resize: %v", err)
	}
	logf("SENT resize rows=%d cols=%d", *rows, *cols)

	var frames, bytesIn uint64
	deadline := time.Now().Add(*duration)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			conn.SetReadDeadline(deadline)
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				if strings.Contains(err.Error(), "i/o timeout") {
					logf("READ ended: deadline reached")
					return
				}
				logf("READ ended: %v", err)
				return
			}
			atomic.AddUint64(&frames, 1)
			atomic.AddUint64(&bytesIn, uint64(len(msg)))
			kind := "BIN"
			if mt == websocket.TextMessage {
				kind = "TXT"
			}
			logFrame(kind, msg, *previewBytes)
		}
	}()

	// Optional input after the configured delay.
	if *input != "" {
		time.Sleep(*inputDelay)
		decoded := decodeEscapes(*input)
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte(decoded)); err != nil {
			logf("WRITE input failed: %v", err)
		} else {
			logf("SENT input (%d bytes): %q", len(decoded), decoded)
		}
	}

	<-done

	logf("SUMMARY frames=%d bytes=%d duration=%v", frames, bytesIn, time.Since(t0))
}

// decodeEscapes expands \r \n \t \xHH in a user-supplied string.
func decodeEscapes(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'r':
			out.WriteByte('\r')
			i++
		case 'n':
			out.WriteByte('\n')
			i++
		case 't':
			out.WriteByte('\t')
			i++
		case '\\':
			out.WriteByte('\\')
			i++
		case 'x':
			if i+3 < len(s) {
				var b byte
				_, err := fmt.Sscanf(s[i+2:i+4], "%2x", &b)
				if err == nil {
					out.WriteByte(b)
					i += 3
					continue
				}
			}
			out.WriteByte(s[i])
		default:
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

func logFrame(kind string, msg []byte, preview int) {
	n := len(msg)
	if preview > n {
		preview = n
	}
	ascii := asciiPreview(msg[:preview])
	hex := hexPreview(msg[:preview])
	logf("RECV %s n=%d ascii=%q hex=%s%s", kind, n, ascii, hex, trailing(n, preview))
}

func asciiPreview(b []byte) string {
	var out strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out.WriteByte(c)
		} else {
			out.WriteByte('.')
		}
	}
	return out.String()
}

func hexPreview(b []byte) string {
	var out strings.Builder
	for i, c := range b {
		if i > 0 && i%2 == 0 {
			out.WriteByte(' ')
		}
		fmt.Fprintf(&out, "%02x", c)
	}
	return out.String()
}

func trailing(n, preview int) string {
	if n <= preview {
		return ""
	}
	return fmt.Sprintf(" ...(+%d)", n-preview)
}

func logf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().UTC().Format("15:04:05.000"), fmt.Sprintf(f, a...))
}
