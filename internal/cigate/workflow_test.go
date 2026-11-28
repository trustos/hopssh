// Package cigate has no code: it checks the release workflows, which nothing
// else does. A workflow that stops needing the test job, or a test job that
// drops the race detector or the kernel-TUN test, ships binaries no test ran
// on, and every Go test in the repo still passes.
//
// The gate itself is what a laptop runs before `make release`: `make preflight`
// (vet, the tests including the end-to-end ones, and -race over the packages we
// patch) plus the kernel-TUN test, which needs root and so is not in `make test`.
// See docs/wiki/concepts/e2e-tests.md and CLAUDE.md rule 13, "run the tests on
// every system you ship for".
package cigate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jobBody returns one job of a workflow: its header and everything up to the
// next job header (two-space indent, ending in a colon).
func jobBody(t *testing.T, path, job string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if l == "  "+job+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s: no job named %q", path, job)
	}
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if len(l) > 2 && l[0] == ' ' && l[1] == ' ' && l[2] != ' ' && strings.HasSuffix(l, ":") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// needsTest reports whether a job waits for the job named test.
func needsTest(body string) bool {
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "needs:") {
			return strings.Contains(l, "test")
		}
	}
	return false
}

func TestServerReleaseRunsTheFullGate(t *testing.T) {
	for _, job := range []string{"build", "docker"} {
		if !needsTest(jobBody(t, "release.yml", job)) {
			t.Errorf("release.yml job %q does not need the test job: a release could ship binaries no test ran on", job)
		}
	}
	test := jobBody(t, "release.yml", "test")
	for _, want := range []string{"make preflight", "KernelTun", "unshare -n"} {
		if !strings.Contains(test, want) {
			t.Errorf("release.yml test job does not run %q: the release gate must be what a laptop runs (make preflight) plus the kernel-TUN test on Linux", want)
		}
	}
}

func TestDesktopReleaseWaitsForPreflight(t *testing.T) {
	for _, job := range []string{"build-macos", "build-windows", "build-linux"} {
		body := jobBody(t, "release-desktop.yml", job)
		if !strings.Contains(body, "needs: preflight") {
			t.Errorf("release-desktop.yml job %q builds a shipped binary without waiting for the preflight job", job)
		}
	}
}

// The code sqlc generates must match the migrations: CI and the release gate
// check it. Migrations 008 to 013 went in without it (found 2026-11-28), and
// the next `make generate` would have changed the types of two queries.
func TestGeneratedDatabaseCodeIsChecked(t *testing.T) {
	if !strings.Contains(jobBody(t, "ci.yml", "build"), "make sqlc-check") {
		t.Error("ci.yml build job does not run make sqlc-check")
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "preflight:") {
			if !strings.Contains(l, " sqlc-check") {
				t.Errorf("make preflight does not run sqlc-check: %q", l)
			}
			return
		}
	}
	t.Error("Makefile has no preflight target")
}
