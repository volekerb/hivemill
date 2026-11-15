package s2cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_smoke.py (5 cases; see ported_s2cli.txt).
// not ported: SmokeTests.test_py_compile - it byte-compiles bin/factory_cli.py; the Go counterpart is that factory-go
// builds, which TestSmokeGoBuilds asserts (ft.New fails the test when the build fails).

func TestSmokeGoBuilds(t *testing.T) {
	e := ft.New(t)
	if st, err := os.Stat(e.GoBin); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("factory-go %s is not an executable file: %v", e.GoBin, err)
	}
}

func TestSmokeHelpExitsZero(t *testing.T) {
	e := ft.New(t)
	cmd := exec.Command(e.GoBin, "--help")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.TempDir(), "FACTORY_HOME=" + os.TempDir() + "/zz-test-no-such-home"}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help: %v\nstderr: %s", err, se.String())
	}
	contains(t, "stdout", so.String(), "usage: factory")
}

func TestSmokeWhoamiOwnerAndAgent(t *testing.T) {
	e := ft.New(t)
	contains(t, "owner whoami", e.OK("whoami").Stdout, "owner")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm", Role: "pm"})
	contains(t, "agent whoami", e.OKWith(ft.Opts{Agent: "zz-test-pm"}, "whoami").Stdout, "role=pm")
}

func TestSmokeNoDatabaseRefused(t *testing.T) {
	e := ft.New(t)
	if err := os.Remove(e.DBPath); err != nil {
		t.Fatal(err)
	}
	r := e.Fail(1, "agents")
	contains(t, "stderr", r.Stderr, "run `factory init`")
}

func TestSmokeStubsAreFirstOnPath(t *testing.T) {
	e := ft.New(t)
	e.OK("res")
	// the Go harness also puts an hc stub first on PATH and the terminal backend (auto) prefers it, so `res` talks to hc
	// where the Python harness (one backend stub) talks to herdr: either way a stub, never a real backend, answered
	if len(e.Calls("herdr"))+len(e.Calls("hc")) == 0 {
		t.Fatal("no terminal-backend stub (herdr or hc) was called by `factory res`")
	}
	env := e.Environ("", nil)
	var path string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if first := strings.Split(path, ":")[0]; first != e.StubBin {
		t.Fatalf("PATH starts with %q, want the stub dir %q", first, e.StubBin)
	}
	herdr, err := os.ReadFile(filepath.Join(e.StubBin, "herdr"))
	if err != nil {
		t.Fatal(err)
	}
	gh, err := os.ReadFile(filepath.Join(e.StubBin, "gh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(herdr, gh) {
		t.Fatal("gh and herdr in the stub dir differ: both must be copies of the one stub binary")
	}
}
