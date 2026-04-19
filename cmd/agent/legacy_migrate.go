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
// config dir is in a pathological state we can't resolve (e.g.,
// multiple half-migrated subdirs).
//
// The migration is idempotent: calling it on an already-migrated dir
// is a no-op. Calling it on a fresh install (no legacy files) is also
// a no-op. Crucially, calling it on a **half-migrated** dir (some
// files moved into a subdir but no enrollments.json yet) completes
// the move in place instead of leaving the agent bricked — this
// covers the case where a previous migration crashed midway on
// disk-full / permission / SIGKILL.
func migrateLegacyLayout(configDir string) (*Enrollment, error) {
	enrollmentsPath := filepath.Join(configDir, enrollmentsFile)
	legacyCertPath := filepath.Join(configDir, "node.crt")

	// If the registry file exists the migration is already done.
	if _, err := os.Stat(enrollmentsPath); err == nil {
		return nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", enrollmentsPath, err)
	}

	legacyAtRoot := false
	if _, err := os.Stat(legacyCertPath); err == nil {
		legacyAtRoot = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", legacyCertPath, err)
	}

	halfMigrated, err := detectHalfMigratedSubdir(configDir)
	if err != nil {
		return nil, err
	}

	switch {
	case legacyAtRoot && halfMigrated != "":
		// Both top-level legacy files AND a populated subdir — the
		// original migration started but the previous run's move loop
		// interleaved file creations in a way we don't want to guess
		// at. Bail and let the operator disambiguate.
		return nil, fmt.Errorf("configDir %s has both top-level legacy files and a half-migrated subdir %q — resolve manually (rm one or the other) and re-run", configDir, halfMigrated)

	case halfMigrated != "":
		log.Printf("[migrate] detected half-migrated subdir %s — completing", halfMigrated)
		return completeMigration(configDir, halfMigrated)

	case legacyAtRoot:
		log.Printf("[migrate] detected legacy config layout at %s — migrating to per-network subdir", configDir)
		name, err := chooseLegacyMigrationName(configDir)
		if err != nil {
			return nil, fmt.Errorf("choose enrollment name: %w", err)
		}
		subdir := enrollmentDir(configDir, name)
		if err := os.MkdirAll(subdir, 0700); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", subdir, err)
		}
		// Move every legacy file into the subdir. If anything fails
		// midway, the next boot will find the half-migrated subdir and
		// complete via the other branch above.
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
		return registerMigratedEnrollment(configDir, subdir, name)

	default:
		// Fresh install.
		return nil, nil
	}
}

// detectHalfMigratedSubdir scans configDir for a single directory
// child that (a) has a valid enrollment name and (b) contains a
// node.crt. Zero matches → fresh install (return ""). Exactly one →
// that's the in-flight migration target. Two or more → pathological,
// bail with an error so the operator decides.
func detectHalfMigratedSubdir(configDir string) (string, error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", configDir, err)
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if validateEnrollmentName(name) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(configDir, name, "node.crt")); err == nil {
			candidates = append(candidates, name)
		}
	}
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("configDir %s has multiple candidate subdirs %v with node.crt — cannot pick one automatically", configDir, candidates)
	}
}

// completeMigration finishes a migration that crashed after moving
// some files but before writing enrollments.json. Any remaining
// legacy files at the top level are moved into the target subdir,
// then the enrollment entry is created from whatever metadata is
// present in the subdir.
func completeMigration(configDir, name string) (*Enrollment, error) {
	subdir := enrollmentDir(configDir, name)
	for _, f := range legacyMigratableFiles {
		src := filepath.Join(configDir, f.name)
		if _, err := os.Stat(src); err != nil {
			// Source absent from top-level → either already moved or
			// was an optional file that never existed. Either way fine.
			continue
		}
		dst := filepath.Join(subdir, f.name)
		if _, err := os.Stat(dst); err == nil {
			// Both locations have the file. Trust the subdir copy
			// (later state) and remove the orphan at the root.
			log.Printf("[migrate] %s exists in both top-level and subdir — keeping subdir copy", f.name)
			_ = os.Remove(src)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, fmt.Errorf("complete-move %s → %s: %w", src, dst, err)
		}
	}
	return registerMigratedEnrollment(configDir, subdir, name)
}

// registerMigratedEnrollment reads metadata from a populated subdir
// and writes enrollments.json with a new entry. Shared by the fresh-
// migrate and complete-migrate paths so both produce an identical
// registry state.
func registerMigratedEnrollment(configDir, subdir, name string) (*Enrollment, error) {
	caCertPEM, err := os.ReadFile(filepath.Join(subdir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read ca.crt from %s: %w", subdir, err)
	}
	enrollment := &Enrollment{
		Name:          name,
		NodeID:        readTrim(filepath.Join(subdir, "node-id")),
		Endpoint:      readTrim(filepath.Join(subdir, "endpoint")),
		TunMode:       readTrim(filepath.Join(subdir, "tun-mode")),
		CAFingerprint: caFingerprint(caCertPEM),
		DNSDomain:     readTrim(filepath.Join(subdir, "dns-domain")),
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
