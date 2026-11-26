package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cli struct {
	t *testing.T
}

func (c cli) run(stdin string, args ...string) (int, string, string) {
	c.t.Helper()
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func (c cli) ok(args ...string) string {
	c.t.Helper()
	code, out, errs := c.run("", args...)
	if code != 0 {
		c.t.Fatalf("%v: exit %d\n%s%s", args, code, out, errs)
	}
	return out
}

func standalone(t *testing.T) (cli, string) {
	home := filepath.Join(t.TempDir(), "hivemill")
	t.Setenv("FACTORY_HOME", home)
	t.Setenv("FACTORY_AGENT", "")
	c := cli{t}
	c.ok("init", "--home", home)
	return c, home
}

func TestInitThenProjectGoalTask(t *testing.T) {
	c, home := standalone(t)
	if out := c.ok("init"); !strings.Contains(out, "checked "+filepath.Join(home, "db", "factory.db")) {
		t.Errorf("second init: %q", out)
	}
	repo := t.TempDir()
	if code, _, errs := c.run("", "project", "add", "www", "--repo", "not a repo", "--path", repo); code != 2 || !strings.Contains(errs, "OWNER/NAME") {
		t.Errorf("bad --repo: exit %d %q", code, errs)
	}
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", repo, "--gate", "make test", "--gate", "make lint")
	if out := c.ok("project", "list"); !strings.Contains(out, `www zz-test/www `+repo+` base main gate "make test && make lint"`) {
		t.Errorf("project list: %q", out)
	}
	if code, _, _ := c.run("", "project", "add", "www", "--repo", "zz-test/www", "--path", repo); code != 3 {
		t.Errorf("duplicate project: exit %d, want 3", code)
	}
	if out := c.ok("goal", "new", "--project", "www", "--title", "zz-test goal"); !strings.HasPrefix(out, "G-001 active in www") {
		t.Errorf("goal new: %q", out)
	}
	if out := c.ok("goal", "list"); !strings.Contains(out, "G-001 active") {
		t.Errorf("goal list: %q", out)
	}

	// a criterion without a proof is refused, with the reason, and nothing is stored
	code, _, errs := c.run("", "task", "add", "--goal", "G-001", "--title", "zz-test task", "--spec", "do it", "--acceptance", "1. it works")
	if code != 3 || !strings.Contains(errs, "criterion 1 names no proof") || !strings.Contains(errs, "spec gate FAIL") {
		t.Errorf("unlinted task: exit %d %q", code, errs)
	}
	if out := c.ok("task", "list"); out != "" {
		t.Errorf("refused task was stored: %q", out)
	}
	// --draft stores it as draft
	if out := c.ok("task", "add", "--goal", "G-001", "--title", "zz-test draft", "--acceptance", "1. it works", "--draft"); !strings.Contains(out, "T-0001 draft in G-001") {
		t.Errorf("draft: %q", out)
	}
	// promoting the draft re-lints and is refused
	if code, _, errs := c.run("", "task", "update", "T-0001", "--status", "todo"); code != 3 || !strings.Contains(errs, "names no proof") {
		t.Errorf("promote unlinted draft: exit %d %q", code, errs)
	}
	c.ok("task", "update", "T-0001", "--status", "todo", "--spec", "do it", "--acceptance", "1. it works - cmd:true => exit 0")
	if out := c.ok("task", "show", "T-0001"); !strings.Contains(out, "T-0001 todo") || !strings.Contains(out, "spec gate PASS (1 criteria)") {
		t.Errorf("show: %q", out)
	}
	// acceptance from stdin
	code, out, errs := c.run("1. a - cmd:true => exit 0\n2. b - manual:the owner looks\n", "task", "add", "--goal", "G-001", "--title", "two",
		"--spec", "s", "--acceptance-file", "-", "--depends", "t-0001")
	if code != 0 || !strings.Contains(out, "T-0002 todo") || !strings.Contains(out, "spec gate PASS (2 criteria)") {
		t.Errorf("stdin task: exit %d %q %q", code, out, errs)
	}
	if out := c.ok("task", "show", "T-0002"); !strings.Contains(out, `depends ["T-0001"]`) {
		t.Errorf("depends: %q", out)
	}
	// breaking the criteria of a todo task is refused
	if code, _, _ := c.run("", "task", "update", "T-0002", "--acceptance", "1. nothing"); code != 3 {
		t.Errorf("unlinted update: exit %d, want 3", code)
	}
	if code, _, _ := c.run("", "goal", "close", "G-001"); code != 3 {
		t.Errorf("close with open tasks: exit %d, want 3", code)
	}
	c.ok("task", "cancel", "T-0001")
	c.ok("task", "cancel", "T-0002")
	if out := c.ok("task", "list", "--status", "cancelled"); strings.Count(out, "cancelled") != 2 {
		t.Errorf("cancelled: %q", out)
	}
	c.ok("goal", "close", "G-001")
	if out := c.ok("goal", "show", "G-001"); !strings.HasPrefix(out, "G-001 done") {
		t.Errorf("goal show: %q", out)
	}
}

// A session may read tasks, never change them: the gate re-runs the proofs stored in the task row.
func TestSessionsCannotChangeTasks(t *testing.T) {
	c, _ := standalone(t)
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", t.TempDir())
	c.ok("goal", "new", "--project", "www", "--title", "g")
	c.ok("task", "add", "--goal", "G-001", "--title", "t", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0")
	t.Setenv("FACTORY_AGENT", "claude-coder-t-0001-a7")
	for _, args := range [][]string{{"task", "update", "T-0001", "--acceptance", "1. x - cmd:true => exit 0"}, {"task", "cancel", "T-0001"},
		{"task", "add", "--goal", "G-001", "--title", "x", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0"}, {"goal", "close", "G-001"},
		{"project", "list"}, {"config", "model", "claude-opus-5-5"}} {
		if code, _, _ := c.run("", args...); code != 3 {
			t.Errorf("%v as a session: exit %d, want 3", args, code)
		}
	}
	for _, args := range [][]string{{"task", "lint", "T-0001"}, {"task", "show", "T-0001"}, {"task", "list"}} {
		if code, out, errs := c.run("", args...); code != 0 {
			t.Errorf("%v as a session: exit %d %q %q", args, code, out, errs)
		}
	}
}

func TestConfigRefusesFableModels(t *testing.T) {
	c, _ := standalone(t)
	if code, _, errs := c.run("", "config", "model", "claude-fable-5"); code == 0 || !strings.Contains(errs, "never Fable") {
		t.Errorf("fable model: exit %d %q", code, errs)
	}
	c.ok("config", "risk_model", "claude-opus-5-5")
}

func TestLaunchdDaemonPlist(t *testing.T) {
	c, home := standalone(t)
	out := filepath.Join(t.TempDir(), "hivemill.daemon.plist")
	c.ok("launchd", "daemon", "--home", home, "--out", out)
	b, _ := os.ReadFile(out)
	for _, want := range []string{"<string>" + home + "</string>", home + "/logs/hivemill.daemon.log", "/opt/homebrew/bin"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q:\n%s", want, b)
		}
	}
}

func TestGoalStatus(t *testing.T) {
	c, _ := standalone(t)
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", t.TempDir())
	c.ok("goal", "new", "--project", "www", "--title", "g")
	if out := c.ok("goal", "status", "G-001", "blocked"); out != "G-001 blocked\n" {
		t.Errorf("blocked: %q", out)
	}
	if out := c.ok("goal", "show", "G-001"); !strings.HasPrefix(out, "G-001 blocked") {
		t.Errorf("show: %q", out)
	}
	if out := c.ok("goal", "status", "G-001", "active"); out != "G-001 active\n" {
		t.Errorf("active: %q", out)
	}
	if code, _, errs := c.run("", "goal", "status", "G-001", "bogus"); code != 2 || !strings.Contains(errs, "unknown goal status") {
		t.Errorf("unknown status: exit %d %q", code, errs)
	}
	if code, _, errs := c.run("", "goal", "status", "G-099", "blocked"); code != 2 || !strings.Contains(errs, "unknown goal") {
		t.Errorf("unknown goal: exit %d %q", code, errs)
	}
	c.ok("task", "add", "--goal", "G-001", "--title", "x", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0")
	if code, _, _ := c.run("", "goal", "status", "G-001", "done"); code != 3 {
		t.Errorf("done with open tasks: exit %d, want 3", code)
	}
	c.ok("task", "cancel", "T-0001")
	if out := c.ok("goal", "status", "G-001", "Cancelled"); out != "G-001 cancelled\n" {
		t.Errorf("cancelled: %q", out)
	}
	if code, _, _ := c.run("", "goal", "status", "G-001"); code != 2 {
		t.Errorf("missing status: exit %d, want 2", code)
	}
}

func TestGuardExportCommand(t *testing.T) {
	c, home := standalone(t)
	p := filepath.Join(home, "run", "guard-export.json")
	_ = os.Remove(p)
	c.ok("guard", "export")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("guard export wrote nothing: %v", err)
	}
	if code, _, errs := c.run("", "guard", "export", "x"); code != 2 || !strings.Contains(errs, "usage: hivemill guard export") {
		t.Errorf("extra argument: exit %d %q", code, errs)
	}
}
