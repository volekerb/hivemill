package hm

import (
	"slices"
	"testing"
)

func refileAdd(t *testing.T, s *SQLite, title, status, deps string) string {
	t.Helper()
	id, err := s.AddTask("G-001", title, "spec", "1. works - cmd:true => exit 0", status, deps, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func wantDeps(t *testing.T, s *SQLite, id, want string) {
	t.Helper()
	task, err := s.Task(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.DependsOn != want {
		t.Errorf("%s depends %s, want %s", id, task.DependsOn, want)
	}
}

func repoint(t *testing.T, r *Runner) {
	t.Helper()
	if err := r.RepointRefiles(); err != nil {
		t.Fatal(err)
	}
}

func repointEvents(t *testing.T, r *Runner) (out []string) {
	t.Helper()
	all, err := r.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if e.Event == "task.depends_repointed" {
			out = append(out, e.TaskID+" "+e.Data["from"].(string)+">"+e.Data["to"].(string))
		}
	}
	return out
}

func cancelTask(t *testing.T, s *SQLite, id string) {
	t.Helper()
	if err := s.UpdateTask(id, map[string]any{"status": "cancelled"}); err != nil {
		t.Fatal(err)
	}
}

func refileOf(t *testing.T, s *SQLite, id, of string) {
	t.Helper()
	if err := s.SetRefileOf(id, of); err != nil {
		t.Fatal(err)
	}
}

// The old task is cancelled or failed already when the re-file is added.
func TestRefileRepointsDependents(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	old := refileAdd(t, s, "zz-test old", "todo", "[]")                        // T-0001
	other := refileAdd(t, s, "zz-test other", "todo", "[]")                    // T-0002
	todo := refileAdd(t, s, "zz-test todo", "todo", `["T-0001","T-0002"]`)     // T-0003
	draft := refileAdd(t, s, "zz-test draft", "draft", `["T-0001"]`)           // T-0004
	running := refileAdd(t, s, "zz-test running", "in_progress", `["T-0001"]`) // T-0005
	failed := refileAdd(t, s, "zz-test failed", "blocked", "[]")               // T-0006
	waits := refileAdd(t, s, "zz-test waits", "todo", `["T-0006"]`)            // T-0007
	cancelTask(t, s, old)
	if err := s.PutRun(&Run{Task: failed, State: Failed}); err != nil {
		t.Fatal(err)
	}
	next := refileAdd(t, s, "zz-test again", "todo", `["T-0001"]`) // T-0008
	refileOf(t, s, next, old)
	next2 := refileAdd(t, s, "zz-test again", "todo", "[]") // T-0009
	refileOf(t, s, next2, failed)
	repoint(t, r)
	repoint(t, r)
	wantDeps(t, s, todo, `["T-0008","T-0002"]`)
	wantDeps(t, s, draft, `["T-0008"]`)
	wantDeps(t, s, running, `["T-0001"]`)
	wantDeps(t, s, waits, `["T-0009"]`)
	wantDeps(t, s, next, `["T-0001"]`) // never on itself
	wantDeps(t, s, other, `[]`)
	got := repointEvents(t, r)
	want := []string{"T-0003 T-0001>T-0008", "T-0004 T-0001>T-0008", "T-0007 T-0006>T-0009"}
	if !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// The old task is cancelled, or its run fails, after the re-file is added.
func TestRefileRepointsAfterCancel(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	old := refileAdd(t, s, "zz-test old", "todo", "[]")         // T-0001
	dep := refileAdd(t, s, "zz-test dep", "todo", `["T-0001"]`) // T-0002
	next := refileAdd(t, s, "zz-test again", "todo", "[]")      // T-0003
	refileOf(t, s, next, old)
	repoint(t, r)
	wantDeps(t, s, dep, `["T-0001"]`)
	cancelTask(t, s, old)
	repoint(t, r)
	wantDeps(t, s, dep, `["T-0003"]`)

	// a re-file of the re-file: the dependent follows the chain, also when the run fails
	third := refileAdd(t, s, "zz-test third", "todo", "[]") // T-0004
	refileOf(t, s, third, next)
	run := &Run{Task: next, Goal: "G-001", Project: "www"}
	r.fail(run, "zz-test failure")
	wantDeps(t, s, dep, `["T-0004"]`)

	// a dependent filed late on the first id goes to the end of the chain in one step
	late := refileAdd(t, s, "zz-test late", "todo", `["T-0001"]`)
	repoint(t, r)
	wantDeps(t, s, late, `["T-0004"]`)
}

// A planner title "(re-file of T-x" counts as the flag.
func TestRefileTitleFallback(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	a := refileAdd(t, s, "zz-test a", "todo", "[]")                      // T-0001
	b := refileAdd(t, s, "zz-test b", "todo", "[]")                      // T-0002
	dep := refileAdd(t, s, "zz-test dep", "todo", `["T-0001","T-0002"]`) // T-0003
	refileAdd(t, s, "(re-file of T-0001) zz-test a", "todo", "[]")       // T-0004
	refileAdd(t, s, "zz-test b (Re-file of T-0002: smaller)", "todo", "[]")
	cancelTask(t, s, a)
	cancelTask(t, s, b)
	repoint(t, r)
	wantDeps(t, s, dep, `["T-0004","T-0005"]`)
}
