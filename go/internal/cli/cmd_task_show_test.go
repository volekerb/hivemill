package cli

import (
	"strings"
	"testing"
)

func TestTaskShowParse(t *testing.T) {
	ok := map[string]taskShowCmd{
		"T-1":               {id: "T-1"},
		"T-1 --full":        {id: "T-1", full: true},
		"--full T-1":        {id: "T-1", full: true},
		"--full T-1 --full": {id: "T-1", full: true},
	}
	for in, want := range ok {
		got, good := parseTaskShow(strings.Fields(in))
		if !good || got.(taskShowCmd) != want {
			t.Errorf("%q: got %v ok=%v, want %v", in, got, good, want)
		}
	}
	for _, in := range [][]string{{}, {"--full"}, {"T-1", "x"}, {"--full=1", "T-1"}, {"-h"}, {"-T"}, {""}, {"T-1", ""}, {"--", "T-1"}} {
		if _, good := parseTaskShow(in); good {
			t.Errorf("%q must delegate", in)
		}
	}
}

// TestTaskShowColumnOrderAndFloat: columns print in the live table's order (a migrated column sits last), and a
// float value is left to Python.
func TestTaskShowColumnOrderAndFloat(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	taskTables(r)
	r.sql("ALTER TABLE tasks ADD COLUMN zz_extra TEXT")
	r.sql("INSERT INTO tasks(id,goal_id,title,created,updated,zz_extra) VALUES('T-1','G-1','t','c','u','tail')")
	if code := r.run("task", "show", "T-1"); code != 0 {
		t.Fatalf("code %d: %s", code, r.errb.String())
	}
	out := r.out.String()
	if !strings.Contains(out, "updated: u\nzz_extra: tail\n") || !strings.HasPrefix(out, "id: T-1\n") {
		t.Fatalf("column order wrong:\n%s", out)
	}
	r.sql("ALTER TABLE tasks ADD COLUMN zz_f REAL")
	r.sql("UPDATE tasks SET zz_f = 1.5 WHERE id='T-1'")
	if code := r.run("task", "show", "T-1"); code != 0 || len(r.execs) != 1 || r.out.Len() != 0 {
		t.Fatalf("a float column must delegate: code %d execs %d out %q", code, len(r.execs), r.out.String())
	}
}

// taskTables is the part of the Python schema `task show` reads (the rig's database has only agents, config, events).
func taskTables(r *rig) {
	r.sql("CREATE TABLE tasks (id TEXT PRIMARY KEY, goal_id TEXT NOT NULL, title TEXT NOT NULL, created TEXT NOT NULL, updated TEXT NOT NULL)")
	r.sql("CREATE TABLE verdicts (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, pr_url TEXT, head_sha TEXT, " +
		"role TEXT NOT NULL, verdict TEXT NOT NULL, summary TEXT, author TEXT, created TEXT NOT NULL)")
}
