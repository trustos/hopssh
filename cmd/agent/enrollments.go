package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Enrollment is one mesh membership held by the agent. The agent can
// hold N of these simultaneously (roadmap #29). Stored in
// <configDir>/enrollments.json as part of an enrollmentRegistry.
type Enrollment struct {
	Name          string    `json:"name"`                    // local label, e.g. "home"; unique within the registry
	NodeID        string    `json:"nodeId"`                  // server-assigned node id (opaque string)
	Endpoint      string    `json:"endpoint"`                // control plane URL (per-enrollment so one agent can span planes)
	TunMode       string    `json:"tunMode"`                 // "kernel" or "userspace"
	CAFingerprint string    `json:"caFingerprint,omitempty"` // sha256 of ca.crt bytes (hex), used as fallback name
	DNSDomain     string    `json:"dnsDomain,omitempty"`     // e.g. "home"; empty if the network has no mesh DNS
	EnrolledAt    time.Time `json:"enrolledAt"`
}

// enrollmentsFile is the registry filename inside configDir.
const enrollmentsFile = "enrollments.json"

// enrollmentRegistrySchema is the on-disk document wrapping the list.
// Versioned so future format changes can migrate in place.
type enrollmentRegistrySchema struct {
	Version     int           `json:"version"`
	Enrollments []*Enrollment `json:"enrollments"`
}

const enrollmentRegistryVersion = 1

// enrollmentRegistry is the in-process view of the persisted registry.
// Thread-safe; all mutations go through the registry's mutex.
type enrollmentRegistry struct {
	mu          sync.Mutex
	path        string
	enrollments []*Enrollment
}

// loadEnrollmentRegistry reads <configDir>/enrollments.json and returns
// a registry. A missing file is not an error — returns an empty registry
// pointed at the would-be path so subsequent Save() materializes it.
func loadEnrollmentRegistry(configDir string) (*enrollmentRegistry, error) {
	path := filepath.Join(configDir, enrollmentsFile)
	r := &enrollmentRegistry{path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc enrollmentRegistrySchema
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Version != enrollmentRegistryVersion {
		return nil, fmt.Errorf("unsupported enrollments.json version %d (expected %d)", doc.Version, enrollmentRegistryVersion)
	}
	r.enrollments = doc.Enrollments
	return r, nil
}

// save atomically writes the registry to disk. Called under the mutex.
func (r *enrollmentRegistry) saveLocked() error {
	doc := enrollmentRegistrySchema{
		Version:     enrollmentRegistryVersion,
		Enrollments: r.enrollments,
	}
	data, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(r.path, data, 0600)
}

// List returns a snapshot of enrollments. Safe to iterate without the lock.
func (r *enrollmentRegistry) List() []*Enrollment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Enrollment, len(r.enrollments))
	copy(out, r.enrollments)
	return out
}

// Len returns the number of enrollments.
func (r *enrollmentRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.enrollments)
}

// Get returns the enrollment with the given name, or nil.
func (r *enrollmentRegistry) Get(name string) *Enrollment {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.enrollments {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// Add appends an enrollment and persists. Rejects duplicate names.
func (r *enrollmentRegistry) Add(e *Enrollment) error {
	if err := validateEnrollmentName(e.Name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.enrollments {
		if existing.Name == e.Name {
			return fmt.Errorf("enrollment %q already exists", e.Name)
		}
	}
	r.enrollments = append(r.enrollments, e)
	if err := r.saveLocked(); err != nil {
		// Roll back in-memory state so retries see the original set.
		r.enrollments = r.enrollments[:len(r.enrollments)-1]
		return err
	}
	return nil
}

// Remove deletes the named enrollment and persists. Returns an error if
// the name is not present.
func (r *enrollmentRegistry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := -1
	for i, e := range r.enrollments {
		if e.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("enrollment %q not found", name)
	}
	removed := r.enrollments[idx]
	r.enrollments = append(r.enrollments[:idx], r.enrollments[idx+1:]...)
	if err := r.saveLocked(); err != nil {
		// Roll back so in-memory matches disk.
		r.enrollments = append(r.enrollments[:idx], append([]*Enrollment{removed}, r.enrollments[idx:]...)...)
		return err
	}
	return nil
}

// Names returns a sorted list of enrollment names. Used for deterministic
// iteration order (DNS drop-in generation, status output, etc.).
func (r *enrollmentRegistry) Names() []string {
	list := r.List()
	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e.Name
	}
	sort.Strings(names)
	return names
}

// enrollmentDir returns the per-enrollment subdirectory path
// (<configDir>/<name>). No filesystem I/O.
func enrollmentDir(configDir, name string) string {
	return filepath.Join(configDir, name)
}

// activeEnrollment is the enrollment the current agent process (or CLI
// subcommand) is operating on. Phase A: single-network — set once from
// the registry's primary entry by callers like runServe or runStatus.
// Phase B will retire this global in favor of per-instance state.
var activeEnrollment *Enrollment

// setActiveEnrollment records the enrollment context for subsequent
// file-path lookups. Safe to call with nil to clear.
func setActiveEnrollment(e *Enrollment) {
	activeEnrollment = e
}

// activeEnrollDir returns the subdir of the active enrollment, or
// configDir if nothing is set (safety fallback used only in edge cases
// like a completely un-enrolled agent).
func activeEnrollDir() string {
	if activeEnrollment == nil {
		return configDir
	}
	return enrollmentDir(configDir, activeEnrollment.Name)
}

// loadPrimaryEnrollment performs the common bootstrap for any CLI
// subcommand that wants to read per-enrollment state: runs the
// legacy-layout migration (idempotent), loads the registry, and sets
// the first entry as the active enrollment. Returns the registry so
// callers can enumerate. Missing enrollments are not an error — the
// returned registry will have Len() == 0.
func loadPrimaryEnrollment() *enrollmentRegistry {
	if _, err := migrateLegacyLayout(configDir); err != nil {
		// Surface the error but don't exit — status/info commands
		// should still be able to report "not enrolled" cleanly.
		// Agents that need the migration to succeed (serve path)
		// will hit the failure again with a fatal log.
		return &enrollmentRegistry{path: filepath.Join(configDir, enrollmentsFile)}
	}
	reg, err := loadEnrollmentRegistry(configDir)
	if err != nil {
		return &enrollmentRegistry{path: filepath.Join(configDir, enrollmentsFile)}
	}
	if reg.Len() > 0 {
		setActiveEnrollment(reg.List()[0])
	}
	return reg
}

// enrollmentNameRegex matches a valid local enrollment name: lowercase
// alphanumeric plus hyphens, starting with a letter or digit, 1–32 chars.
// Deliberately conservative: the name becomes a filesystem directory and
// shows up in CLI output — no whitespace, slashes, or dots.
var enrollmentNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// reservedEnrollmentNames are directory names that can't be used as an
// enrollment name because they collide with registry artifacts.
var reservedEnrollmentNames = map[string]struct{}{
	"enrollments.json": {},
}

func validateEnrollmentName(name string) error {
	if name == "" {
		return fmt.Errorf("enrollment name is empty")
	}
	if !enrollmentNameRegex.MatchString(name) {
		return fmt.Errorf("invalid enrollment name %q (must match [a-z0-9][a-z0-9-]{0,31})", name)
	}
	if _, reserved := reservedEnrollmentNames[name]; reserved {
		return fmt.Errorf("enrollment name %q is reserved", name)
	}
	return nil
}

// caFingerprint returns the first 12 hex chars of SHA-256(caCertPEM).
// Used as a fallback enrollment name when no DNS domain is available.
// Short enough to be typable, long enough to disambiguate across any
// realistic number of control planes a user joins.
func caFingerprint(caCertPEM []byte) string {
	sum := sha256.Sum256(caCertPEM)
	return hex.EncodeToString(sum[:])[:12]
}

// defaultEnrollmentName picks a name from the available hints, in
// priority order: DNS domain (if it's a valid enrollment name), else
// CA fingerprint. Caller is responsible for checking collision against
// the registry and prompting the user for an override if needed.
func defaultEnrollmentName(dnsDomain, caFingerprintHex string) string {
	if validateEnrollmentName(dnsDomain) == nil {
		return dnsDomain
	}
	return caFingerprintHex
}
