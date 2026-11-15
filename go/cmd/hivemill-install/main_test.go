package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary act as the command: the tests below run it as a subprocess, so the real entry
// point (flags, exit code, streams) is what is exercised.
func TestMain(m *testing.M) {
	if os.Getenv("HIVEMILL_INSTALL_TEST_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func runCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	home := t.TempDir()
	// A go toolchain download under HOME writes a read-only module cache that TempDir's RemoveAll cannot delete:
	// ask go for a writable one, and chmod anything left over before the TempDir cleanup runs (cleanups are LIFO).
	t.Cleanup(func() {
		_ = filepath.WalkDir(home, func(p string, _ fs.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
	cmd.Env = []string{"HIVEMILL_INSTALL_TEST_MAIN=1", "HOME=" + home, "PATH=/usr/bin:/bin", "GOFLAGS=-modcacherw"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	rc := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		rc = ee.ExitCode()
	}
	return rc, out.String(), errb.String()
}

func TestHelpListsEveryFlag(t *testing.T) {
	rc, out, _ := runCmd(t, "--help")
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	for _, f := range []string{"--check", "--dry-run", "--yes", "--ref", "--repo-url", "--project-id", "--project-repo"} {
		if !strings.Contains(out, f) {
			t.Errorf("--help lacks %s", f)
		}
	}
}

func TestNoModeIsAUsageError(t *testing.T) {
	rc, out, errb := runCmd(t)
	if rc != 2 || out != "" || !strings.Contains(errb, "pass exactly one of --check, --dry-run, --yes") {
		t.Errorf("rc=%d out=%q err=%q", rc, out, errb)
	}
}

func TestRejectedValueIsNotEchoed(t *testing.T) {
	rc, out, errb := runCmd(t, "--dry-run", "--port", "ghp_secretsecretsecretsecret1234")
	if rc != 2 || strings.Contains(out+errb, "ghp_") {
		t.Errorf("rc=%d out=%q err=%q", rc, out, errb)
	}
}

func TestCheckRunsReadOnly(t *testing.T) {
	rc, out, _ := runCmd(t, "--check")
	if rc != 0 && rc != 3 {
		t.Errorf("rc=%d, want 0 or 3", rc)
	}
	if !strings.Contains(out, "suggested-max-agents ") {
		t.Errorf("no report: %q", out)
	}
}
