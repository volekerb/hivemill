package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListDelegates(t *testing.T) {
	cases := [][]string{
		{"task", "list", "--goal=G-1"},
		{"task", "list", "--stat", "x"},
		{"task", "list", "--goal"},
		{"task", "list", "-h"},
		{"task", "list", "X"},
		{"goal", "list", "--org"},
		{"goal", "list", "G-1"},
		{"task", "list", "--goal", "G-1", "--goal", "G-2"},
		{"task", "list", "--goal", "-x"},
		{"task", "list", "--goal", ""},
		{"task", "ready", "--all"},
		{"task", "ready", "--goal=G-1"},
		{"goal", "list", "--all", "--all"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
			r.run(args...)
			if len(r.execs) != 1 {
				t.Fatalf("want one exec, got %d (stdout %q)", len(r.execs), r.out.String())
			}
			want := append([]string{"python3", filepath.Join(r.repo, "bin", "factory")}, args...)
			if !reflect.DeepEqual(r.execs[0].argv, want) {
				t.Fatalf("argv %q, want %q", r.execs[0].argv, want)
			}
			impl := 0
			for _, kv := range r.execs[0].env {
				if strings.HasPrefix(kv, "FACTORY_CLI_IMPL=") {
					impl++
					if kv != "FACTORY_CLI_IMPL=python" {
						t.Fatalf("env %q", kv)
					}
				}
			}
			if impl != 1 {
				t.Fatalf("FACTORY_CLI_IMPL appears %d times", impl)
			}
		})
	}
}

// baseRig is a rig whose database also has the Python schema's tables.
func baseRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	for _, sfx := range []string{"", "-wal", "-shm"} {
		os.Remove(r.dbPath + sfx)
	}
	if _, err := buildGoldenDB(t, r.home, "none"); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTaskReadyBadDependsOnDelegates(t *testing.T) {
	r := baseRig(t)
	r.sql("INSERT INTO goals(id,title,created,updated) VALUES('G-1','g','x','x')")
	for _, bad := range []string{"not json", "null", "{}", "[1]"} {
		r.sql("DELETE FROM tasks")
		r.sql("INSERT INTO tasks(id,goal_id,title,depends_on,created,updated) VALUES('T-1','G-1','t',?,'x','x')", bad)
		r.run("task", "ready")
		if len(r.execs) != 1 || r.out.Len() != 0 {
			t.Fatalf("depends_on %q: want a clean delegation, got %d execs, stdout %q", bad, len(r.execs), r.out.String())
		}
	}
}

func TestListFlagsAnyOrder(t *testing.T) {
	r := baseRig(t)
	r.sql("INSERT INTO goals(id,title,created,updated) VALUES('G-1','g','x','x')")
	r.sql("INSERT INTO tasks(id,goal_id,title,status,created,updated) VALUES('T-1','G-1','t','done','x','x')")
	var a, b string
	if r.run("task", "list", "--status", "done", "--goal", "G-1") != 0 {
		t.Fatal(r.errb.String())
	}
	a = r.out.String()
	if r.run("task", "list", "--goal", "G-1", "--all", "--status", "done") != 0 {
		t.Fatal(r.errb.String())
	}
	b = r.out.String()
	if a != b || !strings.Contains(a, "T-1") || len(r.execs) != 0 {
		t.Fatalf("flag order changed the answer: %q vs %q", a, b)
	}
}
