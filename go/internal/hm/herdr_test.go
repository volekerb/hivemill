package hm

import (
	"context"
	"encoding/json"

	"slices"
	"strings"
	"testing"
	"time"
)

// herdrRunner is plannerRunner with an hc that lists every terminal it started as an idle Claude Code session.
func herdrRunner(t *testing.T) (*Runner, *SQLite, *fakeHC) {
	t.Helper()
	r, s, f := plannerRunner(t, "")
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "run" {
			f.terms = append(f.terms, Terminal{Name: flagValues(args, "--name")[0], State: "live", Agent: "claude-code", Status: "idle"})
		}
		return f.hc(args...)
	}
	return r, s, f
}

// flagValues are the values of one `hc run` flag, read from the flags before the command.
func flagValues(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args) && args[i] != "sh"; i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

// runLine reports that one recorded call has every part.
func runLine(calls []string, parts ...string) bool {
	return slices.ContainsFunc(calls, func(c string) bool {
		return !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(c, p) })
	})
}

// runLabels are the --label values of the `hc run` of a terminal.
func runLabels(t *testing.T, f *fakeHC, name string) []string {
	t.Helper()
	for _, c := range f.calls {
		if c[0] == "run" && slices.Equal(flagValues(c, "--name"), []string{name}) {
			return flagValues(c, "--label")
		}
	}
	t.Fatalf("no hc run for %s in %v", name, f.calls)
	return nil
}

func startHerdrSession(t *testing.T, r *Runner, run *Run, role, name, machine string) {
	t.Helper()
	prompt := map[string]string{"task": "task.md", "task-judge": "task-judge.md"}[role]
	if _, err := r.StartSession(run, Launch{Role: role, Name: name, Cwd: t.TempDir(), Model: "claude-sonnet-5-5", Effort: "medium",
		PromptFile: r.Env.Home + "/roles/hivemill/" + prompt, Kickoff: "x", Machine: machine}); err != nil {
		t.Fatal(err)
	}
}

func herdrTask(t *testing.T, s *SQLite) *Run {
	t.Helper()
	id, err := s.AddTask("G-001", "zz-test  add a\nfooter", "spec", "1. works - cmd:true => exit 0", "todo", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	return &Run{Task: id, Goal: "G-001", Project: "www"}
}

func TestSessionLaunchHasHerdrLabels(t *testing.T) {
	r, s, f := herdrRunner(t)
	run := herdrTask(t, s)
	startHerdrSession(t, r, run, "task", "claude-coder-a", "")
	startHerdrSession(t, r, run, "task-judge", "claude-judge-a", "")
	if err := s.SetCfg("machine_label.box-b", "Mini"); err != nil {
		t.Fatal(err)
	}
	startHerdrSession(t, r, run, "task", "claude-coder-b", "box-b")
	tab := run.Task + " · zz-test add a footer"
	for name, where := range map[string]string{
		"claude-coder-a": "core-a · workers / " + tab, // no machine_label: the hc name
		"claude-judge-a": "core-a · workers / " + tab + " (judge)",
		"claude-coder-b": "Mini · workers / " + tab,
	} {
		want := []string{"herdr.space=workers", "herdr.tab=tab-" + name, "where=" + where}
		if got := runLabels(t, f, name); !slices.Equal(got, want) {
			t.Errorf("%s: labels %q, want %q", name, got, want)
		}
	}
}

func TestPlannerLaunchHasHerdrLabels(t *testing.T) {
	r, s, f := herdrRunner(t)
	if err := s.SetCfg("machine_label.core-a", "Lenovo"); err != nil {
		t.Fatal(err)
	}
	_ = s.SetMeta("planner", "on")
	hc, startedBeforePass := r.Tools.HC, false
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "run" {
			startedBeforePass = s.Meta("heartbeat") == "" // written after the first good pass
		}
		return hc(args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one turn of the loop
	if err := r.Daemon(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"herdr.space=hivemill", "herdr.tab=tab-planner", "where=Lenovo · hivemill / planner"}
	if got := runLabels(t, f, PlannerName); !slices.Equal(got, want) {
		t.Errorf("planner labels %q, want %q", got, want)
	}
	if !startedBeforePass || f.count("run") != 1 {
		t.Errorf("daemon start: planner before the first pass %v, %d terminals started", startedBeforePass, f.count("run"))
	}
}

func TestDaemonStartTriesAFailingPlannerOnce(t *testing.T) {
	r, s, f := plannerRunner(t, "")
	_ = s.SetMeta("planner", "on")
	r.Tools.HC = func(args ...string) (int, string, string) {
		rc, out, errs := f.hc(args...)
		if args[0] == "run" {
			return 1, "", "no"
		}
		return rc, out, errs
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Daemon(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count("run"); n != 1 {
		t.Errorf("a daemon start tried the planner %d times, want 1", n)
	}
}

func TestHerdrNamesJSON(t *testing.T) {
	r, s, _ := herdrRunner(t)
	if spaces, err := r.HerdrNames(); err != nil || spaces == nil || len(spaces) != 0 {
		t.Fatalf("nothing runs: %v %v, want an empty list", spaces, err)
	}
	for k, v := range map[string]string{"machine_label.core-a": "Lenovo", "machine_label.box-b": "Mini"} {
		if err := s.SetCfg(k, v); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.SetMeta("planner", "on") // named while it is on, with or without a live row
	run := herdrTask(t, s)
	run.Machine = "box-b"
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	startHerdrSession(t, r, run, "task", "claude-coder-a", run.Machine)
	startHerdrSession(t, r, run, "task-judge", "claude-judge-a", "")
	startHerdrSession(t, r, run, "task", "claude-coder-gone", "")
	r.Close("claude-coder-gone")
	spaces, err := r.HerdrNames()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(spaces)
	tab := run.Task + " · zz-test add a footer"
	want := `[{"id":"hivemill","name":"Lenovo · hivemill","machine":"core-a","tabs":[{"id":"tab-planner","terminal":"planner","name":"planner"}]},` +
		`{"id":"workers","name":"Mini · workers","machine":"box-b","tabs":[{"id":"tab-claude-coder-a","terminal":"claude-coder-a","name":"` + tab + `"}]},` +
		`{"id":"workers","name":"Lenovo · workers","machine":"core-a","tabs":[{"id":"tab-claude-judge-a","terminal":"claude-judge-a","name":"` + tab + ` (judge)"}]}]`
	// sorted by space id, then hc machine
	if string(got) != want {
		t.Errorf("names:\n got %s\nwant %s", got, want)
	}
}

func TestHerdrSpacesOff(t *testing.T) {
	r, s, f := herdrRunner(t)
	if s.Cfg("herdr_spaces") != "on" {
		t.Errorf("default %q, want on", s.Cfg("herdr_spaces"))
	}
	for k, v := range map[string]string{"herdr_spaces": "yes", "machine_label.": "x", "machine_label.-a": "x"} {
		if err := s.SetCfg(k, v); err == nil {
			t.Errorf("config %s=%s accepted", k, v)
		}
	}
	if err := s.SetCfg("herdr_spaces", "off"); err != nil {
		t.Fatal(err)
	}
	startHerdrSession(t, r, herdrTask(t, s), "task", "claude-coder-a", "")
	if err := r.PlannerStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-coder-a", PlannerName} {
		if got := runLabels(t, f, name); len(got) != 0 {
			t.Errorf("%s: labels %q while herdr_spaces is off", name, got)
		}
	}
}
