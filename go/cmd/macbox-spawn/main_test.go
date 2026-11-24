package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/macbox"
)

func args(sub, role string, extra ...string) []string {
	a := []string{sub, "--role", role, "--owner-home", "/o", "--factory-home", "/o/factory", "--agent-dir", "/o/factory/a",
		"--coder-clone", "/o/w/c", "--claude", "/o/bin/claude", "--oauth", "/o/oauth", "--gh", "/o/gh"}
	return append(a, extra...)
}

func decode(t *testing.T, out string) result {
	t.Helper()
	var r result
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
		t.Fatalf("not one JSON line: %q (%v)", out, err)
	}
	return r
}

func TestPreflightRefusals(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		goos   string
		reason string
	}{
		{"not darwin", args("preflight", "coder"), "linux", "os"},
		{"researcher has no macOS account wiring", args("preflight", "researcher"), "darwin", "user"},
		{"qa without its own clone", args("preflight", "qa"), "darwin", "clone"},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		code := run(tc.args, &out, &errb, macbox.OSRunner{}, 501, tc.goos)
		res := decode(t, out.String())
		if code != 3 || res.OK || res.Reason != tc.reason || res.Why == "" {
			t.Errorf("%s: exit %d %+v (stderr %q)", tc.name, code, res, errb.String())
		}
	}
}

func TestUsageAndBadFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb, macbox.OSRunner{}, 501, ""); code != 2 || !strings.Contains(errb.String(), "usage") {
		t.Errorf("no args: %d %q", code, errb.String())
	}
	if code := run([]string{"preflight", "--nope"}, &out, &errb, macbox.OSRunner{}, 501, ""); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
}

// recRunner runs nothing: it records commands and writes files into a real temp dir.
type recRunner struct {
	macbox.OSRunner
	calls []string
}

func (r *recRunner) Run(name string, a ...string) (int, string, string) {
	r.calls = append(r.calls, name+" "+strings.Join(a, " "))
	if name == "/bin/mkdir" {
		_ = os.MkdirAll(a[len(a)-1], 0o755)
	}
	return 0, "", ""
}

func TestPrepareWritesProfileAndShim(t *testing.T) {
	dir := t.TempDir()
	r := &recRunner{}
	a := []string{"prepare", "--role", "qa", "--owner-home", "/o", "--factory-home", "/o/factory", "--agent-dir", dir,
		"--coder-clone", "/o/w/c", "--qa-clone", "/o/w/c-qa", "--claude", "/o/bin/claude", "--oauth", "/o/oauth", "--gh", "/o/gh"}
	var out, errb bytes.Buffer
	if code := run(a, &out, &errb, r, 501, "darwin"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	if res := decode(t, out.String()); !res.OK {
		t.Fatalf("%+v", res)
	}
	prof, err := os.ReadFile(filepath.Join(dir, "profile.sb"))
	if err != nil || !strings.Contains(string(prof), "/o/w/c-qa") {
		t.Fatalf("profile.sb: %v %q", err, prof)
	}
	shim, err := os.ReadFile(filepath.Join(dir, "bin", "claude"))
	if err != nil || !strings.Contains(string(shim), "_hivemill_ro") {
		t.Fatalf("shim: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "bin", "claude")); fi.Mode().Perm()&0o022 != 0 {
		t.Errorf("shim must not be group or world writable: %v", fi.Mode())
	}
	if got := strings.Join(r.calls, "\n"); !strings.Contains(got, "-N /o/w/c-qa") || strings.Contains(got, "user:_hivemill_rw allow") {
		t.Errorf("qa clone ACLs wrong:\n%s", got)
	}
}
