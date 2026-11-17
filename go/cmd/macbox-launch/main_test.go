package main

import (
	"bytes"
	"strings"
	"testing"

	"factory/go/internal/macbox"
)

type recorded struct {
	called bool
	argv0  string
	argv   []string
	env    []string
	umask  int
}

// stub swaps the uid, exec and umask hooks and restores them after the test.
func stub(t *testing.T, uid int) *recorded {
	t.Helper()
	rec := &recorded{umask: -1}
	oldUID, oldExec, oldUmask := macbox.LauncherUID, execFn, umaskFn
	t.Cleanup(func() { macbox.LauncherUID, execFn, umaskFn = oldUID, oldExec, oldUmask })
	macbox.LauncherUID = func() int { return uid }
	execFn = func(argv0 string, argv, env []string) error {
		rec.called, rec.argv0, rec.argv, rec.env = true, argv0, argv, env
		return nil
	}
	umaskFn = func(m int) int { rec.umask = m; return 0 }
	return rec
}

var goodArgv = []string{"macbox-launch", "--", macbox.SandboxExec, "-f", "/a/profile.sb", "/opt/claude", "--version"}

func goodEnv() []string {
	return []string{
		"SUDO_UID=501", "PATH=/usr/bin:/bin", "GH_TOKEN=tok", "SSH_AUTH_SOCK=/tmp/agent",
		"HOME=/Users/owner", "AWS_SECRET_ACCESS_KEY=x",
		"MACBOX_AGENT_HOME=/a/claude", "MACBOX_CLAUDE_CONFIG_DIR=/a/claude",
		"MACBOX_GIT_CONFIG_GLOBAL=/a/gitconfig", "MACBOX_TMPDIR=/a/tmp",
	}
}

func TestRefuseUid(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  int
		env  []string
	}{
		{"root", 0, goodEnv()},
		{"owner", 501, goodEnv()},
		{"no-sudo-uid", 502, []string{"PATH=/bin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := stub(t, tc.uid)
			var stderr bytes.Buffer
			if code := run(goodArgv, tc.env, &stderr); code != 3 {
				t.Fatalf("exit %d want 3 (stderr %s)", code, stderr.String())
			}
			if rec.called {
				t.Fatal("refused launcher must not exec")
			}
		})
	}
	t.Run("worker-accepted", func(t *testing.T) {
		rec := stub(t, 502)
		var stderr bytes.Buffer
		if code := run(goodArgv, goodEnv(), &stderr); code != 0 || !rec.called {
			t.Fatalf("exit %d called %v stderr %s", code, rec.called, stderr.String())
		}
	})
}

func TestLauncherDropsEnv(t *testing.T) {
	rec := stub(t, 502)
	var stderr bytes.Buffer
	if code := run(goodArgv, goodEnv(), &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if rec.argv0 != macbox.SandboxExec || strings.Join(rec.argv, " ") != strings.Join(goodArgv[2:], " ") {
		t.Fatalf("exec %s %q", rec.argv0, rec.argv)
	}
	if rec.umask != 0o022 {
		t.Fatalf("umask %o want 022", rec.umask)
	}
	want := []string{
		"CLAUDE_CONFIG_DIR=/a/claude", "GH_TOKEN=tok", "GIT_CONFIG_GLOBAL=/a/gitconfig",
		"GIT_CONFIG_NOSYSTEM=1", "HOME=/a/claude", "PATH=/usr/bin:/bin", "TMPDIR=/a/tmp",
	}
	if strings.Join(rec.env, " ") != strings.Join(want, " ") {
		t.Fatalf("env\n got %q\nwant %q", rec.env, want)
	}
}

func TestLauncherRefusesBadArgvAndDirs(t *testing.T) {
	rec := stub(t, 502)
	var stderr bytes.Buffer
	if code := run([]string{"macbox-launch", "--", "sandbox-exec", "-f", "/p", "/c"}, goodEnv(), &stderr); code != 2 {
		t.Fatalf("bare sandbox-exec: exit %d want 2", code)
	}
	env := []string{"SUDO_UID=501", "PATH=/bin"}
	if code := run(goodArgv, env, &stderr); code != 2 {
		t.Fatalf("missing per-agent dirs: exit %d want 2", code)
	}
	if rec.called {
		t.Fatal("refused launcher must not exec")
	}
}
