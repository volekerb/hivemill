package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/hm"
)

// The planner session may plan (goals, tasks, its side of the owner queue) and read; it may not dispatch, merge,
// configure, answer its own question, or touch a running task.
func TestPlannerSessionPermissions(t *testing.T) {
	c, home := standalone(t)
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", t.TempDir())
	c.ok("goal", "new", "--project", "www", "--title", "g")
	c.ok("task", "add", "--goal", "G-001", "--title", "t", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0")
	s, err := hm.OpenSQLite(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AgentLive("planner", "planner", "planner", "", "", "claude-opus-5-5", "sid", home, "{}"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_AGENT", "planner")
	for _, args := range [][]string{
		{"task", "add", "--goal", "G-001", "--title", "t2", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0"},
		{"task", "update", "T-0002", "--priority", "3"}, {"task", "list"}, {"goal", "list"}, {"run", "status"}, {"report"},
		{"ask", "add", "--task", "T-0001", "Which one?"}, {"ask", "list"}, {"ask", "withdraw", "1", "moot"},
	} {
		if code, out, errs := c.run("", args...); code != 0 {
			t.Errorf("%v as the planner: exit %d %q %q", args, code, out, errs)
		}
	}
	c.ok("ask", "add", "--goal", "G-001", "Second?")
	for _, args := range [][]string{{"run"}, {"run", "T-0001"}, {"run", "stop", "T-0001"}, {"config", "projects", "www"},
		{"ask", "answer", "2", "yes"}, {"merge", "T-0001"}, {"drain"}, {"planner", "start"}, {"digest"},
		{"task", "update", "T-0001", "--status", "done"}, {"project", "list"}} {
		if code, _, _ := c.run("", args...); code != 3 {
			t.Errorf("%v as the planner: exit %d, want 3", args, code)
		}
	}
	// a running task is never changed by the planner
	if err := s.PutRun(&hm.Run{Task: "T-0001", State: hm.Working}); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := c.run("", "task", "update", "T-0001", "--acceptance", "1. b - cmd:true => exit 0"); code != 3 || !strings.Contains(errs, "active run") {
		t.Errorf("update of a running task: exit %d %q", code, errs)
	}
	// the owner answers
	t.Setenv("FACTORY_AGENT", "")
	if out := c.ok("ask", "answer", "2", "yes,", "ship", "it"); !strings.Contains(out, "question #2 answered") {
		t.Errorf("answer: %q", out)
	}
	if out := c.ok("ask", "list", "--all"); !strings.Contains(out, "answered by owner: yes, ship it") || !strings.Contains(out, "#1 withdrawn") {
		t.Errorf("list --all: %q", out)
	}
	if code, _, _ := c.run("", "ask", "answer", "2", "again"); code != 3 {
		t.Errorf("answer twice: exit %d, want 3", code)
	}
}

// upstream: the planner and the owner only, the planner never by file name; the factory side logs what it was sent.
func TestUpstreamCLI(t *testing.T) {
	c, home := standalone(t)
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", t.TempDir())
	c.ok("goal", "new", "--project", "www", "--title", "g")
	c.ok("task", "add", "--goal", "G-001", "--title", "t", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0",
		"--upstream-from", "zz-box")
	ev, err := (hm.Log{Path: filepath.Join(home, "state", "hivemill", "events.jsonl")}).Read()
	if err != nil || len(ev) != 1 || ev[0].Event != "upstream.filed" || ev[0].TaskID != "T-0001" || ev[0].GoalID != "G-001" ||
		ev[0].Project != "www" || ev[0].Data["from"] != "zz-box" {
		t.Errorf("events %+v, err %v", ev, err)
	}
	if out := c.ok("report", "--json"); strings.Contains(out, "T-0001") {
		t.Errorf("a filed task that never ran is in the report: %s", out)
	}
	add := []string{"upstream", "add", "--title", "t", "--spec", "s", "--acceptance", "1. a - cmd:true => exit 0"}
	if code, _, errs := c.run("", append(add, "--priority", "-5")...); code == 0 || !strings.Contains(errs, "--priority") {
		t.Errorf("negative priority: exit %d %q", code, errs)
	}
	if code, _, errs := c.run("", add...); code != 3 || !strings.Contains(errs, "upstream is not set") {
		t.Errorf("no config: exit %d %q", code, errs)
	}
	s, err := hm.OpenSQLite(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for name, role := range map[string]string{"planner": "planner", "zz-director": "director"} {
		if err := s.AgentLive(name, role, name, "", "", "claude-opus-5-5", name, home, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FACTORY_AGENT", "planner")
	file := filepath.Join(t.TempDir(), "spec")
	if err := os.WriteFile(file, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--spec-file", "--acceptance-file"} {
		if code, _, errs := c.run("", "upstream", "add", "--title", "t", flag, file); code != 3 || !strings.Contains(errs, "stdin") {
			t.Errorf("planner %s: exit %d %q", flag, code, errs)
		}
	}
	if code, _, errs := c.run("", add...); code != 3 || !strings.Contains(errs, "upstream is not set") {
		t.Errorf("planner add: exit %d %q", code, errs)
	}
	t.Setenv("FACTORY_AGENT", "zz-director")
	if code, _, errs := c.run("", add...); code != 3 || !strings.Contains(errs, "only the planner or the owner") {
		t.Errorf("director add: exit %d %q", code, errs)
	}
}

func TestPlannerHookCLI(t *testing.T) {
	c := cli{t}
	if code, _, errs := c.run(`{"tool_name":"Bash","tool_input":{"command":"hivemill run"}}`, "planner", "hook"); code != 2 || !strings.Contains(errs, "planner may") {
		t.Errorf("hivemill run: exit %d %q", code, errs)
	}
	if code, _, errs := c.run(`{"tool_name":"Bash","tool_input":{"command":"hivemill task list"}}`, "planner", "hook"); code != 0 {
		t.Errorf("task list: exit %d %q", code, errs)
	}
	if code, _, _ := c.run(`garbage`, "planner", "hook"); code != 2 {
		t.Errorf("garbage input: exit %d, want 2 (fail closed)", code)
	}
}

func TestDigestAndLaunchdDigest(t *testing.T) {
	c, home := standalone(t)
	if out := c.ok("digest", "--date", "2026-11-24"); !strings.Contains(out, "# hivemill digest 2026-11-24") || !strings.Contains(out, "0 merged") {
		t.Errorf("digest: %q", out)
	}
	c.ok("digest", "--date", "2026-11-24", "--send", "--json")
	if _, err := os.Stat(filepath.Join(home, "state", "hivemill", "digests", "2026-11-24.md")); err != nil {
		t.Error(err)
	}
	if code, _, _ := c.run("", "digest", "--date", "yesterday"); code != 2 {
		t.Errorf("bad date: exit %d", code)
	}
	out := filepath.Join(t.TempDir(), "hivemill.digest.plist")
	c.ok("launchd", "digest", "--home", home, "--out", out)
	b, _ := os.ReadFile(out)
	for _, want := range []string{"<string>hivemill.digest</string>", "digest --send", "<key>StartCalendarInterval</key>", home + "/logs/hivemill.digest.log"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q", want)
		}
	}
}
