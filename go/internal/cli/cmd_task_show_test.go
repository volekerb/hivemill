package cli

import (
	"os"
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

// TestTaskShowCompactFromAncestry: an owner identity under a claude --settings coder ancestor gets the compact view
// (the goldens only reach the FACTORY_ROLE and DB-role signals); --full and a lead ancestor do not.
func TestTaskShowCompactFromAncestry(t *testing.T) {
	proc := t.TempDir()
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner", "FACTORY_PROC_DIR": proc})
	taskTables(r)
	r.sql("INSERT INTO tasks(id,goal_id,title,created,updated) VALUES('T-1','G-1','t','c','u')")
	for i := 0; i < 7; i++ {
		r.sql("INSERT INTO events(ts,actor,entity,entity_id,action) VALUES('2026-01-01','a','task','T-1','update')")
	}
	fakeProc{proc}.add(t, os.Getppid(), 1, "claude", "--settings", "/x/settings/coder.json")
	if r.run("task", "show", "T-1"); !strings.Contains(r.out.String(), "(2 earlier events: factory task show T-1 --full)") {
		t.Fatalf("want the compact history, got:\n%s%s", r.out.String(), r.errb.String())
	}
	if r.run("task", "show", "T-1", "--full"); strings.Contains(r.out.String(), "earlier events") {
		t.Fatalf("--full must be uncompacted:\n%s", r.out.String())
	}
	fakeProc{proc}.add(t, os.Getppid(), 1, "claude", "--settings", "/x/settings/lead.json")
	if r.run("task", "show", "T-1"); strings.Contains(r.out.String(), "earlier events") {
		t.Fatalf("a lead ancestor is not a worker:\n%s", r.out.String())
	}
}

// FuzzParseTaskShow: no panic, and it accepts only one <ID> not starting with "-" plus an optional --full.
func FuzzParseTaskShow(f *testing.F) {
	f.Add("T-1", "--full", "")
	f.Add("--full", "T-1", "--full")
	f.Add("", "-h", "x")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		got, ok := parseTaskShow([]string{a, b, c})
		if !ok {
			return
		}
		cmd := got.(taskShowCmd)
		ids := 0
		for _, x := range []string{a, b, c} {
			switch {
			case x == "--full":
			case x != "" && !strings.HasPrefix(x, "-"):
				ids++
			default:
				t.Fatalf("accepted %q", x)
			}
		}
		if ids != 1 || cmd.id == "" {
			t.Fatalf("accepted %q %q %q with id %q", a, b, c, cmd.id)
		}
	})
}
