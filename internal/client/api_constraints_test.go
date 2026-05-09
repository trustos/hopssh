package client

import (
	"os/exec"
	"reflect"
	"testing"
)

// exportedTypes is the canonical set of types whose exported fields must
// satisfy the FFI constraint rules (no chan / func / interface{} fields).
//
// Add new exported struct types to this list as they're added to api.go.
// Interface types (InstanceHTTPHook, MeshService, EventCallback) are not
// scanned — interfaces are FFI-safe by definition (gomobile binds them
// as method-receiving objects).
func exportedTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(Config{}),
		reflect.TypeOf(Client{}),
		reflect.TypeOf(EnrollOptions{}),
		reflect.TypeOf(Snapshot{}),
		reflect.TypeOf(EnrollmentSummary{}),
		reflect.TypeOf(EnrollmentStatus{}), // local_api.go's JSON wire format
		reflect.TypeOf(LocalStatus{}),      // local_api.go's status response
		reflect.TypeOf(PeerInfo{}),
		reflect.TypeOf(PeerDetail{}),
		reflect.TypeOf(Enrollment{}), // enrollments.go
		reflect.TypeOf(Event{}),
		reflect.TypeOf(SubscriptionID{}),
	}
}

// walkExportedFields invokes visit on every EXPORTED field of every type in
// exportedTypes(), recursing into nested structs / slices / maps / pointers.
// Unexported fields are not crossed by gomobile so they're skipped.
func walkExportedFields(t *testing.T, visit func(typeName, fieldPath string, ft reflect.Type)) {
	t.Helper()
	for _, rt := range exportedTypes() {
		walkType(t, rt.Name(), "", rt, visit, map[reflect.Type]bool{})
	}
}

func walkType(t *testing.T, typeName, fieldPath string, rt reflect.Type, visit func(typeName, fieldPath string, ft reflect.Type), seen map[reflect.Type]bool) {
	t.Helper()
	if seen[rt] {
		return
	}
	seen[rt] = true
	switch rt.Kind() {
	case reflect.Struct:
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			path := fieldPath + "." + f.Name
			visit(typeName, path, f.Type)
			// Recurse into nested struct types so the rules apply transitively.
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walkType(t, typeName, path, ft, visit, seen)
			}
		}
	}
}

// TestNoChanInExportedFields enforces that no exported struct field is a
// channel — gomobile rejects chan-typed fields at the binding boundary.
//
// Methods that return channels (e.g. Client.Subscribe) are allowed; only
// FIELD types are constrained.
func TestNoChanInExportedFields(t *testing.T) {
	walkExportedFields(t, func(typeName, fieldPath string, ft reflect.Type) {
		if ft.Kind() == reflect.Chan {
			t.Errorf("%s%s: chan field is gomobile-incompatible (use Subscribe() method-return + SubscribeCallback() for FFI)", typeName, fieldPath)
		}
	})
}

// TestNoFuncInExportedFields enforces that no exported struct field is a
// function value — gomobile rejects func-typed fields at the binding
// boundary. Use callback-registration helpers (interfaces) instead.
func TestNoFuncInExportedFields(t *testing.T) {
	walkExportedFields(t, func(typeName, fieldPath string, ft reflect.Type) {
		if ft.Kind() == reflect.Func {
			t.Errorf("%s%s: func field is gomobile-incompatible (use a method or callback-interface argument instead)", typeName, fieldPath)
		}
	})
}

// TestNoEmptyInterfaceInExportedFields enforces that no exported struct
// field has type interface{} (or any) — gomobile cannot marshal an empty
// interface. Concrete types only.
//
// Named interfaces (InstanceHTTPHook, EventCallback) are allowed because
// gomobile binds them as method-receiving objects.
func TestNoEmptyInterfaceInExportedFields(t *testing.T) {
	walkExportedFields(t, func(typeName, fieldPath string, ft reflect.Type) {
		if ft.Kind() == reflect.Interface && ft.NumMethod() == 0 {
			t.Errorf("%s%s: interface{} field is gomobile-incompatible (use a concrete type)", typeName, fieldPath)
		}
	})
}

// TestExportedAPIIsGomobileCompatible runs `gomobile bind` against this
// package and asserts the binding produces no errors. Skipped if the
// gomobile binary is not in PATH; CI installs it.
func TestExportedAPIIsGomobileCompatible(t *testing.T) {
	if _, err := exec.LookPath("gomobile"); err != nil {
		t.Skip("gomobile not installed; install via `go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init`")
	}
	// Smoke-only: don't actually produce an artifact (slow). Just verify
	// the bind invocation succeeds. A real CI run would target -target=android,ios.
	cmd := exec.Command("gomobile", "bind", "-target=android", "-o", t.TempDir()+"/mobilehop.aar", "github.com/trustos/hopssh/internal/client")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gomobile bind failed: %v\n%s", err, out)
	}
}
