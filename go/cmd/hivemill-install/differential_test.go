package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubTools writes fixed-answer git/gh/herdr/claude/python3/systemctl shells into a fresh dir, so the Python
// reference and the Go installer see the same machine.
func stubTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tags := "aaaa\trefs/tags/v0.1.0\naaaa\trefs/tags/v0.10.0\naaaa\trefs/tags/v0.9.0\naaaa\trefs/tags/v1.0.0-rc1\n"
	scripts := map[string]string{
		"git": `case "$*" in
*ls-remote*) printf '` + strings.ReplaceAll(tags, "\n", `\n`) + `';;
*"--version"*) echo "git version 2.40.0";;
esac
exit 0`,
		"gh":        `case "$1" in auth) echo "Logged in";; *) echo "gh version 2.40.0";; esac; exit 0`,
		"herdr":     `echo "herdr 1.0"; exit 0`,
		"claude":    `echo "claude 1.0"; exit 0`,
		"python3":   `echo "Python 3.12.1"; exit 0`,
		"systemctl": `exit 0`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runWith(t *testing.T, argv []string, env []string) (int, string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	rc := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		rc = ee.ExitCode()
	}
	return rc, out.String()
}

// TestDryRunMatchesPythonReference is the r2 guard: the same inputs through plugins/hivemill/scripts/install.py
// and through this command must print the same dry-run plan and exit with the same code. See comparable for what is
// not compared: the Python reference still sets up and starts the old chain.
func TestDryRunMatchesPythonReference(t *testing.T) {
	ref := filepath.Join("..", "..", "..", "plugins", "hivemill", "scripts", "install.py")
	if _, err := os.Stat(ref); err != nil {
		t.Skipf("Python reference not in this tree (%v): the merged-tree Go proof extracts only go/", err)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("no python3 on PATH: %v", err)
	}
	stubs := stubTools(t)
	cases := [][]string{
		{"--dry-run", "--ref", "v0.3.0"},
		{"--dry-run", "--ref", "latest"},
		{"--dry-run", "--ref", "v0.3.0", "--port", "9000", "--max-agents", "3", "--no-scout"},
		{"--dry-run", "--ref", "v0.3.0", "--project-id", "demo", "--project-repo", "/r", "--project-gh", "o/r"},
		{"--dry-run", "--ref", "v0.3.0", "--no-systemd", "--linger"},
		{"--dry-run", "--ref", "v0.3.0", "--repo-url", "https://example.com/x/y.git"},
		{"--dry-run", "--ref", "v0.3.0", "--port", "70000"},
		{"--dry-run", "--yes"},
		{"--dry-run", "--ref", "bad ref"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir() // one HOME for both: a dry-run changes nothing, so the paths in the plan match
			mkenv := func() []string {
				return []string{"HOME=" + home, "PATH=" + stubs + ":/usr/bin:/bin", "LANG=C.UTF-8"}
			}
			prc, pout := runWith(t, append([]string{py, ref}, args...), mkenv())
			grc, gout := runWith(t, append([]string{os.Args[0]}, args...), append(mkenv(), "HIVEMILL_INSTALL_TEST_MAIN=1"))
			if prc != grc {
				t.Errorf("exit code: python %d, go %d\npython:\n%s\ngo:\n%s", prc, grc, pout, gout)
			}
			// A usage error (exit 2) ends in an argparse usage block that names install.py and wraps differently:
			// the error line before it must match, the block is not compared.
			if prc == 2 {
				pout, gout = beforeUsage(pout), beforeUsage(gout)
			}
			pout, gout = comparable(pout), comparable(gout)
			if pout != gout {
				t.Errorf("output differs\npython:\n%s\ngo:\n%s", pout, gout)
			}
		})
	}
}

// comparable cuts what the two installers no longer share: the init, config and project steps the Go installer
// dropped (and with them the step numbers), the lines of the services step, and the old smoke checks.
func comparable(s string) string {
	var kept []string
	skip := false
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(ln, "[plan] ") {
			skip = false
			f := strings.Fields(ln)
			name := f[len(f)-1]
			switch name {
			case "init", "config", "project":
				skip = true
				continue
			case "services":
				skip = true
			}
			kept = append(kept, "[plan] "+name)
			continue
		}
		oldSmoke := strings.Contains(ln, "/factory agents ") || strings.HasPrefix(strings.TrimSpace(ln), "$ GET http://")
		// the closing line follows the last step, so it is kept even when that step is skipped
		if (!skip && !oldSmoke) || strings.HasPrefix(ln, "plan only:") {
			kept = append(kept, ln)
		}
	}
	return strings.Join(kept, "\n")
}

func beforeUsage(s string) string {
	if i := strings.Index(s, "\nusage:"); i >= 0 {
		return s[:i]
	}
	return s
}
