package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The update is tried for real: the helper process is a copy of this test
// binary that updates itself. Its component is "e2e", which has no service to
// restart.
func TestMain(m *testing.M) {
	endpoint := os.Getenv("SELFUPDATE_E2E_ENDPOINT")
	if endpoint == "" {
		os.Exit(m.Run())
	}
	release, err := Check("e2e", endpoint)
	if err == nil && release != nil {
		err = Apply("e2e", release, endpoint)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestE2E_Update(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a copy of the test binary")
	}
	const newBinary = "the new version"
	name := fmt.Sprintf("hop-e2e-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sum := sha256.Sum256([]byte(newBinary))
	good := hex.EncodeToString(sum[:])

	for _, tc := range []struct {
		what    string
		version string
		binary  func(http.ResponseWriter)
		sums    func(http.ResponseWriter)
		updated bool
		failed  bool
	}{
		{what: "verified", sums: text("%s  hop-e2e-other-arch\n%s  %s\n", strings.Repeat("0", 64), good, name), updated: true},
		{what: "already up to date", version: "dev", sums: text("%s  %s\n", good, name)},
		{what: "wrong checksum", sums: text("%s  %s\n", strings.Repeat("0", 64), name), failed: true},
		{what: "no checksums", sums: status(404), failed: true},
		{what: "checksums unavailable", sums: status(502), failed: true},
		{what: "checksums unreachable", sums: hangUp, failed: true},
		{what: "checksums cut off", sums: cutOff, failed: true},
		{what: "not in the checksums", sums: text("%s  hop-e2e-other-arch\n", good), failed: true},
		{what: "a name that only ends alike", sums: text("%s  evil-%s\n", good, name), failed: true},
		{what: "empty checksum", sums: text("  %s\n", name), failed: true},
		{what: "no such binary", binary: status(404), sums: text("%s  %s\n", good, name), failed: true},
		{what: "download cut off", binary: cutOff, sums: text("%s  %s\n", good, name), failed: true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
				if tc.version == "" {
					tc.version = "v999.0.0"
				}
				fmt.Fprintf(w, `{"version":%q}`, tc.version)
			})
			mux.HandleFunc("/download/"+name, func(w http.ResponseWriter, _ *http.Request) {
				if tc.binary != nil {
					tc.binary(w)
					return
				}
				fmt.Fprint(w, newBinary)
			})
			mux.HandleFunc("/download/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { tc.sums(w) })
			server := httptest.NewServer(mux)
			defer server.Close()

			dir := t.TempDir()
			exe := filepath.Join(dir, "hop-e2e")
			if runtime.GOOS == "windows" {
				exe += ".exe"
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(self)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(exe, old, 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(), "SELFUPDATE_E2E_ENDPOINT="+server.URL)
			out, err := cmd.CombinedOutput()
			if failed := err != nil; failed != tc.failed {
				t.Errorf("failed %v, want %v\n%s", failed, tc.failed, out)
			}

			now, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			if updated := string(now) == newBinary; updated != tc.updated {
				t.Errorf("replaced %v, want %v\n%s", updated, tc.updated, out)
			} else if !updated && string(now) != string(old) {
				t.Error("the program is now neither the old nor the new one")
			}
			if info, err := os.Stat(exe); err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				t.Errorf("not executable: %v %v", info, err)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "hop-update-*")); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

func text(format string, args ...any) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { fmt.Fprintf(w, format, args...) }
}

func status(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, "test", code) }
}

// hangUp closes the connection without an answer.
func hangUp(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}

// cutOff breaks the connection in the middle of the answer.
func cutOff(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "1000")
	fmt.Fprint(w, "half of it")
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}
