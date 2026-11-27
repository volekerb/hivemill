package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionEnvValues(t *testing.T) {
	env := sessionEnv("/h", "t-1")
	for k, want := range map[string]string{
		"TMPDIR":             "/h/.cache/hivemill/tmp/t-1",
		"CLAUDE_CODE_TMPDIR": "/tmp",
		"GOTMPDIR":           "/h/.cache/hivemill/go/tmp",
		"GOCACHE":            "/h/.cache/hivemill/go/build",
		"HOME":               "/h",
		"FACTORY_AGENT":      "t-1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
}

func TestSessionEnvNotUnderTmp(t *testing.T) {
	env := sessionEnv("/h", "t-1")
	for _, k := range []string{"TMPDIR", "GOTMPDIR"} {
		if env[k] == "" || strings.HasPrefix(env[k], "/tmp") {
			t.Errorf("%s = %q", k, env[k])
		}
	}
}

func TestSessionEnvPlacedAndLocal(t *testing.T) {
	for _, machine := range []string{"w1", ""} {
		r, _, _ := placementRunner(t, map[string]fakeWorker{"w1": {memMB: 9000, shared: true, coord: "c"}})
		setupRoles(t, r)
		run := &Run{Task: "T-0001", Goal: "G-001", Agent: "claude-coder-t-0001-a7", State: Working, Round: 1, Worktree: t.TempDir(),
			Base: "main", Model: "claude-sonnet-5-5", Effort: "medium", Machine: machine}
		if err := r.launchTask(run, "go"); err != nil {
			t.Fatal(err)
		}
		script, err := os.ReadFile(filepath.Join(r.Env.StateDir(), "spawn", run.Agent+".sh"))
		if err != nil {
			t.Fatal(err)
		}
		local := machine == ""
		for _, k := range []string{" TMPDIR=", " CLAUDE_CODE_TMPDIR=", " GOTMPDIR=", " GOCACHE="} {
			if strings.Contains(string(script), k) != local {
				t.Errorf("machine %q: %s set = %v, want %v", machine, k, !local, local)
			}
		}
		_, err = os.Stat(filepath.Join(r.Env.UserHome, ".cache", "hivemill", "tmp", run.Agent))
		if (err == nil) != local {
			t.Errorf("machine %q: session tmp dir on the coordinator: %v", machine, err)
		}
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseRemovesSessionTmp(t *testing.T) {
	r, _ := testRunner(t, "zz-test")
	root := filepath.Join(r.Env.UserHome, ".cache", "hivemill", "tmp")
	own, sibling := filepath.Join(root, "t-1"), filepath.Join(root, "t-2")
	mkdirs(t, filepath.Join(own, "sub"), sibling)
	r.Close("t-1")
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Errorf("%s still there: %v", own, err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling removed: %v", err)
	}
	r.Close("t-1") // a missing dir is fine
}

func TestCloseRefusesBadName(t *testing.T) {
	r, _ := testRunner(t, "zz-test")
	cache := filepath.Join(r.Env.UserHome, ".cache", "hivemill")
	keep := []string{filepath.Join(cache, "tmp", "a", "b"), filepath.Join(cache, "tmp", "t-2"), filepath.Join(cache, "go")}
	mkdirs(t, keep...)
	for _, name := range []string{"", "a/b", "..", "."} {
		r.Close(name)
		for _, d := range keep {
			if _, err := os.Stat(d); err != nil {
				t.Fatalf("Close(%q) removed %s: %v", name, d, err)
			}
		}
	}
}
