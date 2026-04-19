//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

// withTempDropInPath swaps dropInPath to a tempfile for the test and
// clears dropInState so tests don't see each other's entries.
func withTempDropInPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/hopssh.conf"
	origPath := dropInPath
	dropInPath = path
	origEntries := dropInState.entries
	dropInState.entries = make(map[string]dropInEntry)
	t.Cleanup(func() {
		dropInPath = origPath
		dropInState.entries = origEntries
	})
	return path
}

func TestDropInMerger_SingleEntry(t *testing.T) {
	path := withTempDropInPath(t)
	if err := updateResolvedDropIn("home", &dropInEntry{domain: "home", addr: "10.0.0.1:15300"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "DNS=10.0.0.1:15300") {
		t.Errorf("missing DNS line: %s", content)
	}
	if !strings.Contains(content, "Domains=~home") {
		t.Errorf("missing Domains line: %s", content)
	}
}

func TestDropInMerger_MultiEntrySorted(t *testing.T) {
	path := withTempDropInPath(t)
	// Add in non-sorted order; file should still be deterministic.
	if err := updateResolvedDropIn("work", &dropInEntry{domain: "work", addr: "10.0.0.2:15300"}); err != nil {
		t.Fatal(err)
	}
	if err := updateResolvedDropIn("home", &dropInEntry{domain: "home", addr: "10.0.0.1:15300"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "DNS=10.0.0.1:15300 10.0.0.2:15300") {
		t.Errorf("expected home first then work: %s", content)
	}
	if !strings.Contains(content, "Domains=~home ~work") {
		t.Errorf("expected sorted domains: %s", content)
	}
}

func TestDropInMerger_RemoveEntry(t *testing.T) {
	path := withTempDropInPath(t)
	_ = updateResolvedDropIn("home", &dropInEntry{domain: "home", addr: "10.0.0.1:15300"})
	_ = updateResolvedDropIn("work", &dropInEntry{domain: "work", addr: "10.0.0.2:15300"})

	// Remove home → file should only have work.
	if err := updateResolvedDropIn("home", nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Contains(content, "10.0.0.1:15300") {
		t.Errorf("home still present after remove: %s", content)
	}
	if !strings.Contains(content, "10.0.0.2:15300") {
		t.Errorf("work missing: %s", content)
	}
}

func TestDropInMerger_RemoveLastEntry_DeletesFile(t *testing.T) {
	path := withTempDropInPath(t)
	_ = updateResolvedDropIn("home", &dropInEntry{domain: "home", addr: "10.0.0.1:15300"})

	// Confirm file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	// Remove last entry → file should be deleted.
	if err := updateResolvedDropIn("home", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be deleted, err=%v", err)
	}
}
