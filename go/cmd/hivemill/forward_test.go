package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForwardArgv(t *testing.T) {
	argv, err := forwardArgv("worker-coord", "/srv/fh/worktrees/G-1-T-1",
		[]string{"FACTORY_HOME=/srv/fh", "FACTORY_AGENT=claude-coder-t-0001-a7", "PATH=/x", "HIVEMILL_COORDINATOR=w",
			"FACTORY_ROLE=task", "factory_lower=no"}, []string{"gate", "hook", "it's"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv[:5], " ") != "ssh -o BatchMode=yes -- worker-coord" || len(argv) != 6 {
		t.Fatalf("argv %q", argv)
	}
	want := `PATH="$HOME/.local/bin:$PATH"; export PATH; cd /srv/fh/worktrees/G-1-T-1 || exit 1; exec env ` +
		`HIVEMILL_FORWARDED=1 FACTORY_AGENT=claude-coder-t-0001-a7 FACTORY_HOME=/srv/fh FACTORY_ROLE=task hivemill gate hook 'it'"'"'s'`
	if argv[5] != want {
		t.Errorf("remote command\n got %s\nwant %s", argv[5], want)
	}
	for _, bad := range []string{"-oProxyCommand=x", "a b", "", "host;rm"} {
		if _, err := forwardArgv(bad, "/", nil, nil); err == nil {
			t.Errorf("coordinator %q must be refused", bad)
		}
	}
}

func TestCoordinatorSources(t *testing.T) {
	uh := t.TempDir()
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if c := coordinator(get, uh); c != "" {
		t.Errorf("no setting: %q", c)
	}
	_ = os.MkdirAll(filepath.Join(uh, ".config", "hivemill"), 0o700)
	_ = os.WriteFile(filepath.Join(uh, ".config", "hivemill", "coordinator"), []byte("coord-a\n"), 0o600)
	if c := coordinator(get, uh); c != "coord-a" {
		t.Errorf("file: %q", c)
	}
	env["HIVEMILL_COORDINATOR"] = "coord-b"
	if c := coordinator(get, uh); c != "coord-b" {
		t.Errorf("env wins: %q", c)
	}
	env[forwarded] = "1"
	if c := coordinator(get, uh); c != "" {
		t.Errorf("the coordinator side never forwards again: %q", c)
	}
}

// TestForwardRunsRemoteWithStdinEnvAndExitCode runs the real argv through a stand-in ssh that runs the remote command
// with sh, and a stand-in hivemill on the "coordinator" that echoes what it got and exits 7.
func TestForwardRunsRemoteWithStdinEnvAndExitCode(t *testing.T) {
	bin, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "ssh"), "#!/bin/sh\nfor a; do last=$a; done\nexec sh -c \"$last\"\n")
	write(filepath.Join(home, ".local", "bin", "hivemill"), "#!/bin/sh\necho \"args=$*\"\necho \"cwd=$(pwd -P)\"\n"+
		"echo \"agent=$FACTORY_AGENT fwd=$HIVEMILL_FORWARDED\"\necho \"stdin=$(cat)\"\nexit 7\n")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", home)
	argv, err := forwardArgv("coord", cwd, []string{"FACTORY_AGENT=claude-coder-t-0001-a7"}, []string{"gate", "hook"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := runForward(argv, strings.NewReader(`{"session_id":"s-1"}`), &out, &errs)
	if code != 7 {
		t.Fatalf("exit %d (stderr %s)", code, errs.String())
	}
	real, _ := filepath.EvalSymlinks(cwd)
	for _, want := range []string{"args=gate hook", "cwd=" + real, "agent=claude-coder-t-0001-a7 fwd=1", `stdin={"session_id":"s-1"}`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %q", want, out.String())
		}
	}
	argv, _ = forwardArgv("coord", filepath.Join(cwd, "gone"), nil, []string{"run", "status"})
	if code := runForward(argv, strings.NewReader(""), &out, &errs); code != 1 {
		t.Errorf("a cwd missing on the coordinator: exit %d, want 1", code)
	}
	if code := runForward([]string{filepath.Join(bin, "no-such-ssh")}, nil, &out, &errs); code != 255 {
		t.Errorf("ssh that cannot run: exit %d, want 255", code)
	}
}

func TestLocalNeverForwards(t *testing.T) {
	t.Setenv("HIVEMILL_COORDINATOR", "coord-c")
	t.Setenv(forwarded, "")
	var out, errs bytes.Buffer
	code, done := preMain([]string{"local", "coordinator"}, strings.NewReader(""), &out, &errs)
	if !done || code != 0 || strings.TrimSpace(out.String()) != "coord-c" {
		t.Errorf("local coordinator: %d %v %q %q", code, done, out.String(), errs.String())
	}
	t.Setenv("HIVEMILL_COORDINATOR", "")
	t.Setenv("HOME", t.TempDir())
	if _, done := preMain([]string{"run", "status"}, strings.NewReader(""), &out, &errs); done {
		t.Error("no coordinator: the call runs here")
	}
}
