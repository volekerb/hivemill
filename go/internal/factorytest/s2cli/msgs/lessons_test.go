package s2msgs

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_lessons.py (10 cases).

func lessonsEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	for _, r := range []string{"boss", "director", "lead"} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r})
	}
	for _, r := range lessonWorkers {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r})
	}
	return e
}

func lessonCount(e *ft.Env, table string) int {
	if table == "" {
		table = "lessons"
	}
	return e.Count(fmt.Sprintf("SELECT count(*) FROM %s", table))
}

func lessonAdd(e *ft.Env, agent string, extra ...string) ft.Result {
	args := append([]string{"lesson", "add", "--title", "t", "--roles", "lead,director", "--rule", "r"}, extra...)
	return as(e, agent, args...)
}

func lessonNew(t *testing.T, e *ft.Env, extra ...string) string {
	t.Helper()
	p := lessonAdd(e, "zz-test-lead", extra...)
	if p.Exit != 0 {
		t.Fatalf("add: exit %d stderr %q", p.Exit, p.Stderr)
	}
	return strings.TrimSpace(p.Stdout)
}

func TestLessonsAddAsEachAllowedActorAndShow(t *testing.T) {
	e := lessonsEnv(t)
	agents := []string{"zz-test-boss", "zz-test-director", "zz-test-lead", "owner"}
	for i, agent := range agents {
		p := e.OKWith(ft.Opts{Agent: agent}, "lesson", "add", "--title", "t", "--roles", "lead,director", "--rule", "r", "--source", "M733,T-0016")
		if got := strings.TrimSpace(p.Stdout); got != fmt.Sprintf("L-%03d", i+1) {
			t.Fatalf("stdout %q want L-%03d", got, i+1)
		}
	}
	out := e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "show", "L-001").Stdout
	for _, s := range []string{"title: t", "roles: lead,director", "sources: M733,T-0016", "status: open", "## rule\nr",
		"## links\n_none_", "## history", "zz-test-boss add: t"} {
		contains(t, "show", out, s)
	}
}

func TestLessonsRuleAndWhatFromStdin(t *testing.T) {
	e := lessonsEnv(t)
	e.OKWith(ft.Opts{Agent: "zz-test-lead", Stdin: "multi\nline\n"}, "lesson", "add", "--title", "t", "--roles", "all", "--rule", "-")
	if got := e.SQL("SELECT rule FROM lessons")[0][0]; got != "multi\nline" {
		t.Fatalf("rule = %q", got)
	}
}

func TestLessonsWorkersRefused(t *testing.T) {
	needPy(t)
	e := lessonsEnv(t)
	lid := lessonNew(t, e)
	for _, role := range lessonWorkers {
		t.Run(role, func(t *testing.T) {
			if got := lessonAdd(e, "zz-test-"+role).Exit; got != 3 {
				t.Fatalf("add exit %d want 3", got)
			}
			p := as(e, "zz-test-"+role, "lesson", "edit", lid, "--title", "x")
			if p.Exit != 3 {
				t.Fatalf("edit exit %d want 3", p.Exit)
			}
		})
	}
	if lessonCount(e, "") != 1 {
		t.Fatalf("count = %d want 1", lessonCount(e, ""))
	}
	if e.SQL("SELECT title FROM lessons")[0][0] != "t" {
		t.Fatal("title changed")
	}
}

func TestLessonsWorkerProcessClaimingDirectorRefused(t *testing.T) {
	needPy(t)
	e := lessonsEnv(t)
	lid := lessonNew(t, e)
	e.FakeClaude(os.Getpid(), 1, "claude", "--settings", "/zz-tmp/factory/roles/settings/coder.json")
	if lessonAdd(e, "zz-test-director").Exit != 3 {
		t.Fatal("add should be refused")
	}
	if as(e, "zz-test-director", "lesson", "edit", lid, "--title", "x").Exit != 3 {
		t.Fatal("edit should be refused")
	}
	if lessonCount(e, "") != 1 {
		t.Fatal("extra lesson")
	}
}

func TestLessonsUnknownIdentity(t *testing.T) {
	e := lessonsEnv(t)
	lid := lessonNew(t, e)
	extras := []map[string]string{{"CLAUDECODE": "1"}, {"CLAUDECODE": "1", "HERDR_PANE_ID": "w9:p9"}}
	for _, extra := range extras {
		e.OKWith(ft.Opts{Env: extra}, "lesson", "list")
		e.OKWith(ft.Opts{Env: extra}, "lesson", "show", lid)
		if p := e.RunWith(ft.Opts{Env: extra}, "lesson", "add", "--title", "t", "--roles", "all", "--rule", "r"); p.Exit != 3 {
			t.Fatalf("add exit %d want 3", p.Exit)
		}
		if p := e.RunWith(ft.Opts{Env: extra}, "lesson", "edit", lid, "--title", "x"); p.Exit != 3 {
			t.Fatalf("edit exit %d want 3", p.Exit)
		}
	}
	if lessonCount(e, "") != 1 {
		t.Fatal("count")
	}
}

func TestLessonsValidationExits2AndWritesNothing(t *testing.T) {
	e := lessonsEnv(t)
	bad := [][]string{
		{"--title", " ", "--roles", "all", "--rule", "r"},
		{"--title", "t", "--roles", "all", "--rule", " "},
		{"--title", "t", "--roles", "", "--rule", "r"},
		{"--title", "t", "--roles", "lead,nobody", "--rule", "r"},
		{"--title", "t", "--roles", "all", "--rule", "r", "--source", "M733,bogus"},
		{"--title", "t", "--roles", "all", "--rule", "r", "--source", "https://example.com/pull/1"},
		{"--title", "t", "--roles", "all", "--rule", "-", "--what", "-"},
	}
	for _, args := range bad {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			p := e.RunWith(ft.Opts{Agent: "zz-test-lead", Stdin: "x"}, append([]string{"lesson", "add"}, args...)...)
			if p.Exit != 2 {
				t.Fatalf("exit %d stderr %q", p.Exit, p.Stderr)
			}
		})
	}
	p := as(e, "zz-test-lead", "lesson", "add", "--title", "t", "--roles", "x", "--rule", "r")
	contains(t, "stderr", p.Stderr, "valid:")
	if lessonCount(e, "") != 0 {
		t.Fatal("wrote lesson")
	}
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "add", "--title", "t", "--roles", "all", "--rule", "r", "--source",
		"G-003,#11,https://github.com/a/b/pull/12")
}

func TestLessonsEditAppendsSourcesAndRejectsNothing(t *testing.T) {
	e := lessonsEnv(t)
	lid := lessonNew(t, e, "--source", "M733,T-0016")
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "edit", lid, "--source", "T-0016,M900")
	if got := e.SQL("SELECT sources FROM lessons")[0][0]; got != "M733,T-0016,M900" {
		t.Fatalf("sources = %q", got)
	}
	e.OKWith(ft.Opts{Agent: "zz-test-director"}, "lesson", "edit", lid, "--title", "new", "--roles", "pm", "--rule", "r2", "--what", "w")
	row := e.SQL("SELECT title, roles, rule, what FROM lessons")[0]
	if !reflect.DeepEqual(row, []string{"new", "pm", "r2", "w"}) {
		t.Fatalf("row = %v", row)
	}
	contains(t, "show", e.OK("lesson", "show", lid).Stdout, "title,roles,rule,what")
	for _, args := range [][]string{{lid}, {lid, "--source", "M733"}, {lid, "--title", "new"}} {
		if as(e, "zz-test-lead", append([]string{"lesson", "edit"}, args...)...).Exit != 2 {
			t.Fatalf("edit %v should fail", args)
		}
	}
	if as(e, "zz-test-lead", "lesson", "edit", "L-099", "--title", "x").Exit != 2 {
		t.Fatal("missing id")
	}
	if as(e, "zz-test-lead", "lesson", "edit", lid, "--roles", "bad").Exit != 2 {
		t.Fatal("bad roles")
	}
	if e.Run([]string{"lesson", "show", "L-099"}, "").Exit != 2 {
		t.Fatal("show missing")
	}
}

func TestLessonsListFilters(t *testing.T) {
	e := lessonsEnv(t)
	a := lessonNew(t, e)
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "add", "--title", "everyone", "--roles", "all", "--rule", "r")
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "add", "--title", "coders", "--roles", "coder", "--rule", "r")
	sqlExec(t, e, "UPDATE lessons SET status='applied' WHERE id='L-003'")
	sqlExec(t, e, "INSERT INTO lesson_links(lesson_id,created) VALUES('L-001','2026-01-01')")
	out := e.OK("lesson", "list").Stdout
	lines := strings.Split(strings.TrimSpace(out), "\n")[1:]
	ids := make([]string, len(lines))
	for i, l := range lines {
		ids[i] = strings.Fields(l)[0]
	}
	if !reflect.DeepEqual(ids, []string{a, "L-002", "L-003"}) {
		t.Fatalf("ids = %v", ids)
	}
	if !strings.Contains(lines[0], "L-001") || !strings.Contains(lines[0], "open") {
		t.Fatalf("line %q", lines[0])
	}
	listIDs := func(args ...string) []string {
		o := e.OK(append([]string{"lesson", "list"}, args...)...).Stdout
		ls := strings.Split(strings.TrimSpace(o), "\n")[1:]
		var out []string
		for _, l := range ls {
			out = append(out, strings.Fields(l)[0])
		}
		return out
	}
	if !reflect.DeepEqual(listIDs("--status", "open", "--role", "lead"), []string{"L-001", "L-002"}) {
		t.Fatal("open+lead")
	}
	if !reflect.DeepEqual(listIDs("--role", "coder"), []string{"L-002", "L-003"}) {
		t.Fatal("coder")
	}
	if !reflect.DeepEqual(listIDs("--status", "applied"), []string{"L-003"}) {
		t.Fatal("applied")
	}
	gotVerified := listIDs("--status", "verified")
	if len(gotVerified) != 0 {
		t.Fatalf("verified = %v, out = %q", gotVerified, e.OK("lesson", "list", "--status", "verified").Stdout)
	}
	if e.Run([]string{"lesson", "list", "--status", "bogus"}, "").Exit != 2 {
		t.Fatal("bogus status")
	}
	contains(t, "show", e.OK("lesson", "show", "L-001").Stdout, "1")
}

func TestLessonsOnlyCaptureSubcommands(t *testing.T) {
	needPy(t)
	e := lessonsEnv(t)
	if e.Run([]string{"lesson", "bogus"}, "").Exit != 2 {
		t.Fatal("bogus cmd")
	}
	contains(t, "help", e.OK("lesson", "--help").Stdout, "{add,list,show,edit,link,apply,verify,reopen}")
}

func TestLessonsMigrationCreatesTablesIdempotently(t *testing.T) {
	needPy(t)
	e := lessonsEnv(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTask(ft.Task{ID: "T-0001"})
	sqlExec(t, e, "DROP TABLE lesson_links")
	sqlExec(t, e, "DROP TABLE lessons")
	before := [][][]string{tableRows(e, "goals"), tableRows(e, "tasks")}
	for i := 0; i < 2; i++ {
		e.OK("lesson", "list")
		names := map[string]bool{}
		for _, r := range e.SQL("SELECT name FROM sqlite_master WHERE type='table'") {
			names[r[0]] = true
		}
		for _, tb := range []string{"lessons", "lesson_links"} {
			if !names[tb] {
				t.Fatalf("missing %s iter %d", tb, i)
			}
		}
		after := [][][]string{tableRows(e, "goals"), tableRows(e, "tasks")}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("data changed iter %d", i)
		}
	}
	if lessonNew(t, e) != "L-001" {
		t.Fatal("new id")
	}
}
