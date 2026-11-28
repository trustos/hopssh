package e2etest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// A test process builds hop-server in a folder of its own and never removes
// it. Until 2026-11-28 nothing else did either: 478 folders, 26 GB, on a Mac,
// and the Linux VM's /tmp (a 3.7 GB tmpfs) full, which failed the next run's
// build ("no space left on device"). A folder named with its process's pid
// now goes at the next build once that process has ended; the folders from
// before, named without one, stay. The test's folder has a "[" in its path,
// which filepath.Glob would read as a pattern.
func TestSweepBuilds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "[a]")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ended := exec.Command(os.Args[0], "-test.run=^$")
	if err := ended.Run(); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "hop-server"), []byte("x"), 0700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// A living process this user may not signal (EPERM): pid 1 on Unix,
	// the System process on Windows, where OpenProcess refuses it.
	notOurs := 1
	if runtime.GOOS == "windows" {
		notOurs = 4
	}
	want := map[string]bool{
		mk(fmt.Sprintf("%s%d-1", binPrefix, ended.Process.Pid)): false,
		mk(fmt.Sprintf("%s%d-2", binPrefix, os.Getpid())):       true,
		mk(fmt.Sprintf("%s%d-3", binPrefix, os.Getppid())):      true, // alive, not this process
		mk(fmt.Sprintf("%s%d-5", binPrefix, notOurs)):           true,
		mk("hop-e2e-bin123456"):                                 true, // before names had a pid
		mk(binPrefix + "x-4"):                                   true,
		mk("something-else"):                                    true,
	}
	sweepBuilds(dir)
	for p, kept := range want {
		if _, err := os.Stat(p); (err == nil) != kept {
			t.Errorf("%s: kept %v, want %v", filepath.Base(p), err == nil, kept)
		}
	}
}

// build puts hop-server in a folder named with this process, in the temp
// folder, and first removes the folders of processes that have ended.
func TestBuildSweeps(t *testing.T) {
	if testing.Short() {
		t.Skip("builds hop-server")
	}
	// Nothing else in this package builds hop-server, so the build is this
	// test's, in its own temp folder. build runs once per process: with
	// -count=2 the second run would get the first one's, in a folder that is
	// gone, and no sweep. After the test the next Start builds again.
	reset := func() { buildOnce, buildPath, buildErr = sync.Once{}, "", nil }
	reset()
	t.Cleanup(reset)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", tmp)
	}
	ended := exec.Command(os.Args[0], "-test.run=^$")
	if err := ended.Run(); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(tmp, fmt.Sprintf("%s%d-1", binPrefix, ended.Process.Pid))
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the folder of a process that has ended is still there after a build")
	}
	got := filepath.Dir(p)
	if prefix := fmt.Sprintf("%s%d-", binPrefix, os.Getpid()); filepath.Dir(got) != tmp || !strings.HasPrefix(filepath.Base(got), prefix) {
		t.Errorf("hop-server is built in %s; want a folder %s… in %s", got, prefix, tmp)
	}
}
