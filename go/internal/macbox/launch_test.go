package macbox

import (
	"strings"
	"testing"
)

func TestRefuseUid(t *testing.T) {
	if err := RefuseLauncherUID(0, 501); err == nil {
		t.Fatal("uid 0 must be refused")
	}
	if err := RefuseLauncherUID(501, 501); err == nil {
		t.Fatal("owner uid must be refused")
	}
	if err := RefuseLauncherUID(502, 0); err == nil {
		t.Fatal("missing SUDO_UID must be refused")
	}
	if err := RefuseLauncherUID(502, 501); err != nil {
		t.Fatalf("worker uid should pass: %v", err)
	}
}

func TestSudoUIDFromEnv(t *testing.T) {
	for in, want := range map[string]int{"SUDO_UID=501": 501, "SUDO_UID=": 0, "SUDO_UID=-1": 0, "SUDO_UID=5x": 0, "OTHER=1": 0} {
		if got := SudoUIDFromEnv([]string{in}); got != want {
			t.Errorf("%s: got %d want %d", in, got, want)
		}
	}
}

func TestDropsEnv(t *testing.T) {
	in := []string{
		"PATH=/bin", "HOME=/hidden", "LANG=C", "SSH_AUTH_SOCK=/tmp/s",
		"FACTORY_AGENT=a", "LC_CTYPE=UTF-8", "GH_TOKEN=tok",
	}
	got := EnvironMap(FilterEnviron(in))
	if _, ok := got["HOME"]; ok {
		t.Fatal("HOME leaked through the filter")
	}
	if _, ok := got["SSH_AUTH_SOCK"]; ok {
		t.Fatal("SSH_AUTH_SOCK leaked through the filter")
	}
	if got["PATH"] != "/bin" || got["GH_TOKEN"] != "tok" || got["LC_CTYPE"] != "UTF-8" {
		t.Fatalf("allow-listed keys missing: %v", got)
	}
	m := EnvironMap(FilterEnviron([]string{
		"PATH=/bin", "MACBOX_AGENT_HOME=/a", "MACBOX_CLAUDE_CONFIG_DIR=/c",
		"MACBOX_GIT_CONFIG_GLOBAL=/g", "MACBOX_TMPDIR=/t", "DROP=1",
	}))
	if err := ApplyLauncherDirs(m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PATH": "/bin", "HOME": "/a", "CLAUDE_CONFIG_DIR": "/c", "GIT_CONFIG_GLOBAL": "/g", "TMPDIR": "/t", "GIT_CONFIG_NOSYSTEM": "1"}
	if strings.Join(EnvList(m), " ") != strings.Join(EnvList(want), " ") {
		t.Fatalf("launcher env\n got %v\nwant %v", EnvList(m), EnvList(want))
	}
}

func TestApplyLauncherDirsRefusesMissing(t *testing.T) {
	m := map[string]string{"MACBOX_AGENT_HOME": "/a", "MACBOX_CLAUDE_CONFIG_DIR": "/c", "MACBOX_GIT_CONFIG_GLOBAL": "/g"}
	if err := ApplyLauncherDirs(m); err == nil {
		t.Fatal("missing MACBOX_TMPDIR must be refused")
	}
	m["MACBOX_TMPDIR"] = "relative"
	if err := ApplyLauncherDirs(m); err == nil {
		t.Fatal("relative MACBOX_TMPDIR must be refused")
	}
}

func TestSplitLauncherArgv(t *testing.T) {
	rest, err := SplitLauncherArgv([]string{"--", SandboxExec, "-f", "/p.sb", "/c", "x"})
	if err != nil || rest[0] != SandboxExec || rest[4] != "x" {
		t.Fatalf("split: %v err %v", rest, err)
	}
	for _, bad := range [][]string{
		{"no-separator"},
		{"--"},
		{"--", "sandbox-exec", "-f", "/p.sb", "/c"},      // bare name: no PATH search
		{"--", "/tmp/sandbox-exec", "-f", "/p.sb", "/c"}, // other binary
		{"--", SandboxExec, "-p", "(allow default)", "/c"},
		{"--", SandboxExec, "-f", "p.sb", "/c"},
		{"--", SandboxExec, "-f", "/p.sb"},
		{"--", "/bin/sh", "-c", "id"},
	} {
		if _, err := SplitLauncherArgv(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
