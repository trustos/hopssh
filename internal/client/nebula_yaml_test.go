package client

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

const writersTestYAML = `pki:
  ca: /x/ca.crt
  cert: /x/node.crt
  key: /x/node.key
static_host_map:
  "10.42.1.1": ["192.0.2.1:42001"]
lighthouse:
  am_lighthouse: false
  hosts: ["10.42.1.1"]
listen:
  host: 0.0.0.0
  port: 4242
relay:
  use_relays: true
tun:
  user: true
firewall:
  inbound:
    - port: any
      proto: icmp
      host: any
`

// TestNebulaYAML_WritersKeepEachOthersChanges: the renewal writer
// (applyNebulaConfigUpdate, renewal goroutine) and the routes writer
// (rewriteNebulaUnsafeRoutes, heartbeat goroutine) each read nebula.yaml,
// change one part and write it back. Run at once, the later write must not
// carry the other's old values.
func TestNebulaYAML_WritersKeepEachOthersChanges(t *testing.T) {
	// Control: one after the other, the check below passes. Without this a
	// wrong check would look like a race.
	{
		dir := t.TempDir()
		path := filepath.Join(dir, "nebula.yaml")
		os.WriteFile(path, []byte(writersTestYAML), 0644)
		inst := testInstance(t, "home", dir)
		off := false
		if err := rewriteNebulaUnsafeRoutes(inst, []string{"10.9.0.0/16"}); err != nil {
			t.Fatal(err)
		}
		if err := applyNebulaConfigUpdate(inst, &nebulaConfigUpdate{UseRelays: &off}); err != nil {
			t.Fatal(err)
		}
		cfg := readYAMLMap(t, path)
		tun, _ := cfg["tun"].(map[string]any)
		relay, _ := cfg["relay"].(map[string]any)
		if tun["unsafe_routes"] == nil || relay["use_relays"] != false {
			t.Fatalf("control failed: tun=%v relay=%v", tun, relay)
		}
	}

	const rounds = 300
	lost, failed := 0, 0
	for i := 0; i < rounds; i++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "nebula.yaml")
		if err := os.WriteFile(path, []byte(writersTestYAML), 0644); err != nil {
			t.Fatal(err)
		}
		inst := testInstance(t, "home", dir)
		off := false
		start := make(chan struct{})
		var wg sync.WaitGroup
		var errRoutes, errRenew error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errRoutes = rewriteNebulaUnsafeRoutes(inst, []string{"10.9.0.0/16"})
		}()
		go func() {
			defer wg.Done()
			<-start
			errRenew = applyNebulaConfigUpdate(inst, &nebulaConfigUpdate{UseRelays: &off})
		}()
		close(start)
		wg.Wait()
		if errRoutes != nil || errRenew != nil {
			failed++
			continue
		}
		cfg := readYAMLMap(t, path)
		tun, _ := cfg["tun"].(map[string]any)
		relay, _ := cfg["relay"].(map[string]any)
		if tun["unsafe_routes"] == nil || relay["use_relays"] != false {
			lost++
		}
	}
	if lost > 0 || failed > 0 {
		t.Errorf("%d of %d rounds lost a change, %d failed to write", lost, rounds, failed)
	}
}

// TestAtomicWrite_ConcurrentWriters: two writers of one file must each get
// either their whole content in place or nothing, never an error caused by
// the other or a mix of both.
func TestAtomicWrite_ConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebula.yaml")
	const writers, writes = 8, 40
	contents := make([][]byte, writers)
	for w := range contents {
		contents[w] = bytes.Repeat([]byte(fmt.Sprintf("writer %d\n", w)), 4096)
	}
	errs := make(chan error, writers*writes)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < writes; i++ {
				if err := atomicWrite(path, contents[w], 0644); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if n < 3 {
			t.Errorf("atomicWrite: %v", err)
		}
		n++
	}
	if n > 0 {
		t.Errorf("%d of %d writes failed", n, writers*writes)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	whole := false
	for _, c := range contents {
		whole = whole || bytes.Equal(got, c)
	}
	if !whole {
		t.Errorf("file holds %d bytes that are no writer's whole content", len(got))
	}
	left, _ := filepath.Glob(path + ".tmp*")
	if len(left) > 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cfg
}

// TestApplyRoutesUpdate_RetriesAfterFailedRewrite: when nebula.yaml can't be
// rewritten, the routes must not count as applied, or no later heartbeat
// tries again.
func TestApplyRoutesUpdate_RetriesAfterFailedRewrite(t *testing.T) {
	dir := t.TempDir()
	inst := testInstance(t, "home", dir)
	inst.svcMu.Lock()
	inst.closed = true // reloadNebula returns at once
	inst.svcMu.Unlock()
	path := filepath.Join(dir, "nebula.yaml")

	applyRoutesUpdate(inst, []string{"10.9.0.0/16"}) // no nebula.yaml: fails
	if err := os.WriteFile(path, []byte(writersTestYAML), 0644); err != nil {
		t.Fatal(err)
	}
	applyRoutesUpdate(inst, []string{"10.9.0.0/16"})

	tun, _ := readYAMLMap(t, path)["tun"].(map[string]any)
	if tun["unsafe_routes"] == nil {
		t.Errorf("routes never reached nebula.yaml after one failed rewrite: tun=%v", tun)
	}
}

// TestRepairNebulaYAML: a damaged nebula.yaml is replaced by a whole backup,
// and nothing else is touched.
func TestRepairNebulaYAML(t *testing.T) {
	whole := writersTestYAML
	cut := whole[:strings.Index(whole, "tun:")] // a write cut short
	cases := []struct {
		name         string
		file, backup *string
		want         string // content of nebula.yaml afterwards; "" = missing
		wantBackup   string
	}{
		{"whole file gets a backup", &whole, nil, whole, whole},
		{"whole file keeps it", &whole, &whole, whole, whole},
		{"empty", ptrTo(""), &whole, whole, whole},
		{"not yaml", ptrTo("pki: [\n"), &whole, whole, whole},
		{"cut short", &cut, &whole, whole, whole},
		{"missing", nil, &whole, whole, whole},
		{"no backup", &cut, nil, cut, ""},
		{"backup cut too", ptrTo(""), &cut, "", cut},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "nebula.yaml")
			if c.file != nil {
				os.WriteFile(path, []byte(*c.file), 0644)
			}
			if c.backup != nil {
				os.WriteFile(path+".bak", []byte(*c.backup), 0644)
			}
			repairNebulaYAML("home", path)
			got, _ := os.ReadFile(path)
			gotBackup, _ := os.ReadFile(path + ".bak")
			if string(got) != c.want {
				t.Errorf("nebula.yaml = %q, want %q", got, c.want)
			}
			if string(gotBackup) != c.wantBackup {
				t.Errorf("nebula.yaml.bak = %q, want %q", gotBackup, c.wantBackup)
			}
		})
	}
}

// TestNebulaYAML_WritersKeepBackup: after any writer, the backup holds what
// the file holds.
func TestNebulaYAML_WritersKeepBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebula.yaml")
	os.WriteFile(path, []byte(writersTestYAML), 0644)
	inst := testInstance(t, "home", dir)
	off := false
	for name, write := range map[string]func() error{
		"routes":  func() error { return rewriteNebulaUnsafeRoutes(inst, []string{"10.9.0.0/16"}) },
		"renewal": func() error { return applyNebulaConfigUpdate(inst, &nebulaConfigUpdate{UseRelays: &off}) },
	} {
		if err := write(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a, _ := os.ReadFile(path)
		b, _ := os.ReadFile(path + ".bak")
		if !bytes.Equal(a, b) {
			t.Errorf("after %s the backup differs from nebula.yaml", name)
		}
	}
}

// TestNebulaYAML_EveryWriterLocks finds every function that writes a
// nebula.yaml: one that names the file and writes something, or one that
// writes to a variable called configPath or cfgPath. Each must hold
// lockNebulaYAML and write with writeNebulaYAML. A new function that only
// names the file and writes others goes in notWriters, with the reason.
func TestNebulaYAML_EveryWriterLocks(t *testing.T) {
	notWriters := map[string]string{
		"runClientJoin": "writes the client's certs; writeClientNebulaConfig writes its nebula.yaml",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, f := range files {
		// The build skips names starting with "." or "_" (e.g. macOS's ._*).
		if strings.HasSuffix(f, "_test.go") || f == "nebula_yaml.go" || strings.HasPrefix(f, ".") || strings.HasPrefix(f, "_") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			names, writes, toConfig := false, false, false
			calls := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					names = names || x.Value == `"nebula.yaml"`
				case *ast.CallExpr:
					fn := exprName(x.Fun)
					calls[fn] = true
					if fn == "os.WriteFile" || fn == "atomicWrite" || fn == "writeFileSecure" || fn == "writeNebulaYAML" {
						writes = true
						if fn != "writeNebulaYAML" && len(x.Args) > 0 {
							a := exprName(x.Args[0])
							toConfig = toConfig || a == "configPath" || a == "cfgPath"
						}
					}
				}
				return true
			})
			if !(names && writes) && !toConfig {
				continue
			}
			if _, ok := notWriters[fd.Name.Name]; ok && !toConfig {
				continue
			}
			found++
			if !calls["lockNebulaYAML"] || !calls["writeNebulaYAML"] || toConfig {
				t.Errorf("%s (%s) writes nebula.yaml without lockNebulaYAML and writeNebulaYAML", fd.Name.Name, f)
			}
		}
	}
	if found < 7 {
		t.Errorf("found %d writers of nebula.yaml, want at least 7: the scan is broken", found)
	}
}

func exprName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprName(x.X) + "." + x.Sel.Name
	}
	return ""
}

func ptrTo(s string) *string { return &s }

// TestStartInstance_RestoresDamagedConfig: a network whose nebula.yaml was
// cut short starts from its backup instead of staying down.
func TestStartInstance_RestoresDamagedConfig(t *testing.T) {
	c := newServingClient(t)
	inst := newStartableInstance(t)
	path := filepath.Join(inst.dir(), "nebula.yaml")
	if err := os.WriteFile(path+".bak", []byte(writersTestYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	var startedWith error = fmt.Errorf("Nebula was not started")
	substituteStartMesh(t, func(cfgPath, _ string) (meshService, error) {
		data, _ := os.ReadFile(cfgPath)
		startedWith = checkNebulaYAML(data)
		return nil, fmt.Errorf("test stops here")
	})
	startWithoutNebula(t, c, inst)
	if startedWith != nil {
		t.Errorf("Nebula did not get a whole config: %v", startedWith)
	}
}
