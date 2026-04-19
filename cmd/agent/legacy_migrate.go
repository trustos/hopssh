package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// legacyMigratableFiles are the files that live at the root of a
// pre-v0.10 (single-network) config dir. During migration we move each
// into a per-network subdir. Files marked optional may be absent.
var legacyMigratableFiles = []struct {
	name     string
	optional bool
}{
	{"ca.crt", false},
	{"node.crt", false},
	{"node.key", false},
	{"token", false},
	{"endpoint", false},
	{"node-id", true},     // may be missing on very old installs
	{"nebula.yaml", true}, // regenerated on demand if missing
	{"tun-mode", true},
	{"dns-domain", true},
	{"dns-server", true},
}

// migrateLegacyLayout converts a pre-v0.10 single-network config dir
// into the v0.10 per-network layout. Returns the migrated Enrollment
// on success, nil if nothing needed migrating, or an error if the
// config dir is in a partially-migrated state that we can't resolve
// automatically.
//
// The migration is idempotent: calling it on an already-migrated dir
// is a no-op. Calling it on a fresh install (no legacy files) is also
// a no-op.
func migrateLegacyLayout(configDir string) (*Enrollment, error) {
	enrollmentsPath := filepath.Join(configDir, enrollmentsFile)
	legacyCertPath := filepath.Join(configDir, "node.crt")

	// If the registry file exists the migration is already done.
	if _, err := os.Stat(enrollmentsPath); err == nil {
		return nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", enrollmentsPath, err)
	}

	// No legacy cert → fresh install, nothing to do.
	if _, err := os.Stat(legacyCertPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", legacyCertPath, err)
	}

	log.Printf("[migrate] detected legacy config layout at %s — migrating to per-network subdir", configDir)

	name, err := chooseLegacyMigrationName(configDir)
	if err != nil {
		return nil, fmt.Errorf("choose enrollment name: %w", err)
	}

	subdir := enrollmentDir(configDir, name)
	if err := os.MkdirAll(subdir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", subdir, err)
	}

	// Read metadata off the legacy files before moving them so we can
	// build the Enrollment struct below.
	endpoint := readTrim(filepath.Join(configDir, "endpoint"))
	nodeID := readTrim(filepath.Join(configDir, "node-id"))
	tunMode := readTrim(filepath.Join(configDir, "tun-mode"))
	dnsDomain := readTrim(filepath.Join(configDir, "dns-domain"))

	caCertPEM, err := os.ReadFile(filepath.Join(configDir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read legacy ca.crt: %w", err)
	}

	// Move each legacy file into the subdir.
	for _, f := range legacyMigratableFiles {
		src := filepath.Join(configDir, f.name)
		dst := filepath.Join(subdir, f.name)
		if err := os.Rename(src, dst); err != nil {
			if f.optional && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("move %s → %s: %w", src, dst, err)
		}
	}

	enrollment := &Enrollment{
		Name:          name,
		NodeID:        nodeID,
		Endpoint:      endpoint,
		TunMode:       tunMode,
		CAFingerprint: caFingerprint(caCertPEM),
		DNSDomain:     dnsDomain,
		EnrolledAt:    time.Now().UTC(),
	}

	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		return nil, fmt.Errorf("load empty registry for write: %w", err)
	}
	if err := reg.Add(enrollment); err != nil {
		return nil, fmt.Errorf("write enrollments.json: %w", err)
	}

	log.Printf("[migrate] legacy config migrated → enrollment %q at %s", name, subdir)
	return enrollment, nil
}

// chooseLegacyMigrationName picks a directory name for the one pre-v0.10
// enrollment. Priority: DNS domain on disk → CA fingerprint → fallback
// "default". Collisions can't happen (the registry is empty by
// definition when we get here), so we never need to prompt.
func chooseLegacyMigrationName(configDir string) (string, error) {
	dnsDomain := readTrim(filepath.Join(configDir, "dns-domain"))

	caCertPEM, err := os.ReadFile(filepath.Join(configDir, "ca.crt"))
	if err != nil {
		return "", fmt.Errorf("read ca.crt for fingerprint: %w", err)
	}
	fp := caFingerprint(caCertPEM)

	name := defaultEnrollmentName(dnsDomain, fp)
	// defaultEnrollmentName guarantees a valid name, but double-check
	// since the fingerprint starts with a hex digit and is 12 chars.
	if err := validateEnrollmentName(name); err != nil {
		return "", fmt.Errorf("computed name %q invalid: %w", name, err)
	}
	return name, nil
}

// readTrim is a convenience wrapper that returns the trimmed contents
// of a file or "" on any error. Used during migration for optional
// fields where "missing" and "empty" are equivalent.
func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
