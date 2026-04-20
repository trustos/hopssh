package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMeshInstance_StopPortmap_Idempotent ensures stopPortmap is safe
// to call when portmap was never started — it's invoked from close()
// which runs unconditionally for every instance, including those whose
// portmap probe failed.
func TestMeshInstance_StopPortmap_Idempotent(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4242})
	inst.stopPortmap() // never started — must not panic
	inst.stopPortmap() // again — must not panic
}

// TestMeshInstance_ReinjectPortmapAddr_NilPortmap is a no-op when no
// portmap manager is attached. Same call site as renew.go's hot-restart
// path; this guards against a regression that would NPE on a re-enroll
// before portmap is set up.
func TestMeshInstance_ReinjectPortmapAddr_NilPortmap(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4242})
	inst.reinjectPortmapAddr() // must not panic
}

// TestEnrollment_ListenPortPersistedInJSON ensures the field survives
// a registry round-trip. The migration layer is meaningless if the
// assigned port doesn't outlive a restart.
func TestEnrollment_ListenPortPersistedInJSON(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	if err := reg.Add(&Enrollment{Name: "home", ListenPort: 4242}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "work", ListenPort: 4243}); err != nil {
		t.Fatal(err)
	}

	reg2, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name string
		port int
	}{{"home", 4242}, {"work", 4243}} {
		got := reg2.Get(want.name)
		if got == nil {
			t.Fatalf("missing %s", want.name)
		}
		if got.ListenPort != want.port {
			t.Errorf("%s ListenPort: got %d want %d", want.name, got.ListenPort, want.port)
		}
	}
}

// TestEnrollment_ListenPortOmitemptyOnZero ensures legacy enrollments
// (pre-v0.10.3) don't carry a noisy "listenPort": 0 in their JSON.
// Field tag is `omitempty`; this catches accidental removal.
func TestEnrollment_ListenPortOmitemptyOnZero(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	if err := reg.Add(&Enrollment{Name: "legacy"}); err != nil { // ListenPort=0
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, enrollmentsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"listenPort":0`) || strings.Contains(string(data), `"listenPort": 0`) {
		t.Errorf("ListenPort=0 should be omitempty, but found in:\n%s", data)
	}
}
