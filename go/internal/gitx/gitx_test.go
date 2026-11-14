package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubGit writes a git stub into a temp dir that logs argv (one arg per line, "--" after each call) and the values of
// GIT_DIR, GIT_WORK_TREE, GIT_TERMINAL_PROMPT, GIT_CONFIG_NOSYSTEM, then runs body. It returns the PATH dir and log.
func stubGit(t *testing.T, body string) (dir, log string) {
	t.Helper()
	dir, log = t.TempDir(), filepath.Join(t.TempDir(), "log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\nprintf -- '--\\n' >> '" + log + "'\n" +
		"printf 'env GIT_DIR=%s WT=%s PROMPT=%s NOSYS=%s\\n' \"${GIT_DIR-unset}\" \"${GIT_WORK_TREE-unset}\" \"${GIT_TERMINAL_PROMPT-unset}\" \"${GIT_CONFIG_NOSYSTEM-unset}\" >> '" + log + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir, log
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestArgvGolden: goldens come from Python git_argv (one-off command in the PR body).
func TestArgvGolden(t *testing.T) {
	_, log := stubGit(t, "exit 0\n")
	cases := []struct {
		golden string
		run    func() error
	}{
		{"rev-parse.golden", func() error { _, err := RevParse("/repo", Options{}, "--verify", "origin/main"); return err }},
		{"ls-remote.golden", func() error {
			_, err := LsRemote("/repo", Options{}, "https://github.com/o/r.git", "refs/heads/main")
			return err
		}},
		{"merge-base.golden", func() error { _, err := MergeBase("/repo", Options{}, "a", "b"); return err }},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			os.Remove(log)
			if err := c.run(); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", c.golden))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.SplitN(readLog(t, log), "--\n", 2)[0]
			if got != string(want) {
				t.Fatalf("argv differs from Python git_argv\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestEnvScrub: Python drops GIT_DIR, GIT_WORK_TREE and GIT_COMMON_DIR (factory_cli.py config-refusal base_env) and
// git_run forces GIT_TERMINAL_PROMPT=0 and GIT_CONFIG_NOSYSTEM=1.
func TestEnvScrub(t *testing.T) {
	_, log := stubGit(t, "exit 0\n")
	t.Setenv("GIT_DIR", "/tmp/elsewhere")
	t.Setenv("GIT_WORK_TREE", "/tmp/elsewhere-wt")
	t.Setenv("GIT_COMMON_DIR", "/tmp/elsewhere-common")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	if _, err := Run("/repo", Options{}, "status"); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	if !strings.Contains(got, "env GIT_DIR=unset WT=unset PROMPT=0 NOSYS=1\n") {
		t.Fatalf("child env not scrubbed:\n%s", got)
	}
	for _, kv := range scrubEnv([]string{"GIT_COMMON_DIR=/x", "KEEP=1"}) {
		if strings.HasPrefix(kv, "GIT_COMMON_DIR=") {
			t.Fatalf("GIT_COMMON_DIR kept: %v", kv)
		}
	}
}

func TestEnvExplicitEnvIsScrubbedToo(t *testing.T) {
	_, log := stubGit(t, "exit 0\n")
	if _, err := Run("/repo", Options{Env: []string{"PATH=" + os.Getenv("PATH"), "GIT_DIR=/x"}}, "status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, log), "GIT_DIR=unset") {
		t.Fatal("explicit env kept GIT_DIR")
	}
}

// TestTimeout: Python str(subprocess.TimeoutExpired) for ["sleep","5"] with timeout 0.2 is
// "Command '['sleep', '5']' timed out after 0.2 seconds".
func TestTimeout(t *testing.T) {
	stubGit(t, "exec sleep 30\n")
	start := time.Now()
	_, err := Run("/repo", Options{Timeout: 200 * time.Millisecond}, "fetch")
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want *TimeoutError, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("hung stub was not killed")
	}
	if got, want := (&TimeoutError{Argv: []string{"sleep", "5"}, Seconds: 0.2}).Error(), "Command '['sleep', '5']' timed out after 0.2 seconds"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !strings.HasPrefix(te.Error(), "Command '['git', '-C', '/repo'") || !strings.HasSuffix(te.Error(), "timed out after 0.2 seconds") {
		t.Fatalf("unexpected text %q", te.Error())
	}
	rc, _, msg := RC("/repo", Options{Timeout: 100 * time.Millisecond}, "fetch")
	if rc != 124 || msg != "git fetch timed out" {
		t.Fatalf("RC timeout = %d %q", rc, msg)
	}
}

func TestExitCodeAndOutput(t *testing.T) {
	stubGit(t, "echo out; echo err >&2; exit 3\n")
	r, err := Run("/repo", Options{}, "status")
	if err != nil || r.ExitCode != 3 || !strings.HasSuffix(r.Stdout, "out\n") || r.Stderr != "err\n" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := RevParse("/repo", Options{}, "HEAD"); err == nil || !strings.Contains(err.Error(), "exit 3: err") {
		t.Fatalf("RevParse error = %v", err)
	}
}

// TestLiveCheckoutRefusal: checkout/switch/reset on a live home are refused without running git; same on a worktree is allowed.
func TestLiveCheckoutRefusal(t *testing.T) {
	_, log := stubGit(t, "exit 0\n")
	live, wt := t.TempDir(), t.TempDir()
	o := Options{Home: live, LiveHomes: []string{live}}
	for _, sub := range []string{"checkout", "switch", "reset"} {
		os.Remove(log)
		r, err := Run(live, o, sub, "x")
		if err != nil || r.ExitCode != 1 || !strings.Contains(r.Stderr, "refusing `git "+sub+"` on the live factory checkout") {
			t.Fatalf("%s: %+v %v", sub, r, err)
		}
		if _, err := os.Stat(log); err == nil {
			t.Fatalf("%s: git was run", sub)
		}
		if r, err := Run(wt, o, sub, "x"); err != nil || r.ExitCode != 0 {
			t.Fatalf("%s on worktree: %+v %v", sub, r, err)
		}
	}
	if r, _ := Run(live, o, "status"); r.ExitCode != 0 {
		t.Fatal("status refused")
	}
	// protocol.file.allow is never under a live home, always otherwise
	if got := o.Overrides()[4]; got != [2]string{"protocol.file.allow", "never"} {
		t.Fatalf("live: %v", got)
	}
	if got := (Options{Home: wt, LiveHomes: []string{live}}).Overrides()[4]; got[1] != "always" {
		t.Fatalf("temp: %v", got)
	}
}

func TestResetKeepOnMain(t *testing.T) {
	live := t.TempDir()
	o := Options{Home: live, LiveHomes: []string{live}}
	stubGit(t, "case \"$*\" in *symbolic-ref*) echo refs/heads/main;; esac\nexit 0\n")
	if r, _ := Run(live, o, "reset", "--keep", "origin/main"); r.ExitCode != 0 {
		t.Fatalf("reset --keep on main refused: %+v", r)
	}
	stubGit(t, "case \"$*\" in *symbolic-ref*) echo refs/heads/other;; esac\nexit 0\n")
	if r, _ := Run(live, o, "reset", "--keep", "origin/main"); r.ExitCode != 1 {
		t.Fatalf("reset --keep off main allowed: %+v", r)
	}
}
