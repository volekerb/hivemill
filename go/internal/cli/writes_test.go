package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"factory/go/internal/identity"
)

func wctx(t *testing.T, who string) (*ctx, *rig) {
	t.Helper()
	r := newRig(t, map[string]string{"FACTORY_AGENT": who})
	r.sql("CREATE TABLE IF NOT EXISTS goals (id TEXT PRIMARY KEY, title TEXT, status TEXT, created TEXT, updated TEXT, org TEXT)")
	r.sql("CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, goal_id TEXT, title TEXT, role TEXT, status TEXT, created TEXT, updated TEXT)")
	c, closeDB := testCtx(t, r, who)
	t.Cleanup(closeDB)
	return c, r
}

func exitOf(t *testing.T, err error) *identity.ExitError {
	t.Helper()
	var ee *identity.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *identity.ExitError, got %v", err)
	}
	return ee
}

func TestEventRowAndTrace(t *testing.T) {
	c, _ := wctx(t, "claude-director")
	var trace []Stmt
	c.trace = &trace
	if err := event(c, "task", "T-0001", "add", strp("hello")); err != nil {
		t.Fatal(err)
	}
	if err := eventAs(c, "owner", "goal", "G-001", "close", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := c.db.Query("SELECT ts,actor,entity,entity_id,action,detail FROM events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][]any
	for rows.Next() {
		var ts, actor, ent, eid, act string
		var det *string
		if err := rows.Scan(&ts, &actor, &ent, &eid, &act, &det); err != nil {
			t.Fatal(err)
		}
		d := "<nil>"
		if det != nil {
			d = *det
		}
		got = append(got, []any{ts, actor, ent, eid, act, d})
	}
	want := [][]any{
		{"2026-11-12T01:02:03+00:00", "claude-director", "task", "T-0001", "add", "hello"},
		{"2026-11-12T01:02:03+00:00", "owner", "goal", "G-001", "close", "<nil>"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if !reflect.DeepEqual(trace, []Stmt{{"INSERT", "events"}, {"INSERT", "events"}}) {
		t.Fatalf("trace %v", trace)
	}
}

func TestNextIDWidthIsMinimum(t *testing.T) {
	c, r := wctx(t, "owner")
	if id, err := nextID(c, "tasks", "T", 4); err != nil || id != "T-0001" {
		t.Fatalf("empty table: %q %v", id, err)
	}
	r.sql("INSERT INTO goals(id,title,status,created,updated) VALUES('G-001','g','active','x','x')")
	for _, id := range []string{"T-0009", "T-0010", "T-99999"} {
		r.sql("INSERT INTO tasks(id,goal_id,title,role,status,created,updated) VALUES(?,'G-001','t','coder','todo','x','x')", id)
	}
	if id, err := nextID(c, "tasks", "T", 4); err != nil || id != "T-100000" {
		t.Fatalf("got %q %v, want T-100000 (numeric max, width is a minimum)", id, err)
	}
}

func TestNextIDUnparsableSuffixDelegates(t *testing.T) {
	c, r := wctx(t, "owner")
	r.sql("INSERT INTO goals(id,title,status,created,updated) VALUES('G-001','g','active','x','x'),('G-abc','g','active','x','x')")
	if _, err := nextID(c, "goals", "G", 3); !errors.Is(err, errDelegate) {
		t.Fatalf("want delegate, got %v", err)
	}
}

func TestSlugMatchesPython(t *testing.T) { // expected values from python3 bin/factory_cli.py slug()
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"Hello World!", 32, "hello-world"},
		{"9lives", 32, "a-9lives"},
		{"", 32, "a"},
		{"---", 32, "a"},
		{strings.Repeat("x", 10) + strings.Repeat("-", 5) + strings.Repeat("y", 30), 32, "xxxxxxxxxx-----yyyyyyyyyyyyyyyyy"},
		{"abcd-efgh", 5, "abcd"}, // cut at n, then rstrip("-")
		{"İstanbul", 32, "i-stanbul"},
		{"a_b  c", 32, "a-b-c"},
		{"Hello World!", -2, "hello-wor"},
		{"", -2, ""},
	}
	for _, tc := range cases {
		if got := slug(tc.in, tc.n); got != tc.want {
			t.Errorf("slug(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestReadTextStdinOnce(t *testing.T) {
	c, _ := wctx(t, "owner")
	c.deps.Stdin = strings.NewReader("  body\r\nline2 \n\n")
	dash := "-"
	got, err := readText(c, &dash, false)
	if err != nil || *got != "body\nline2" {
		t.Fatalf("got %v %v", got, err)
	}
	_, err = readText(c, &dash, false)
	ee := exitOf(t, err)
	if ee.Code != 2 || !strings.HasPrefix(ee.Msg, "only one argument per call can be '-' (stdin). Set the other field with a second call (e.g. `factory goal edit`/`factory task update`), or pass it inline.") {
		t.Fatalf("second '-': %+v", ee)
	}
	if got, err := readText(c, nil, false); got != nil || err != nil {
		t.Fatalf("nil in: %v %v", got, err)
	}
}

func TestReadTextAtPath(t *testing.T) {
	c, _ := wctx(t, "owner")
	dir := t.TempDir()
	f := filepath.Join(dir, "spec.txt")
	os.WriteFile(f, []byte("\n file text \n"), 0o644)
	c.deps.Getwd = func() (string, error) { return dir, nil }

	at := "@" + f
	if got, err := readText(c, &at, true); err != nil || *got != "file text" {
		t.Fatalf("@file: %v %v", got, err)
	}
	for _, spelling := range []string{"@" + f + "/", "@" + f + "/.", "@" + dir + "//./spec.txt"} { // pathlib normalises these
		if got, err := readText(c, &spelling, false); err != nil || *got != "file text" {
			t.Fatalf("%q: %v %v", spelling, got, err)
		}
	}
	missing := "@missing"
	if got, err := readText(c, &missing, false); err != nil || *got != "@missing" {
		t.Fatalf("@missing must stay literal: %v %v", got, err)
	}
	plain := "just text"
	if got, _ := readText(c, &plain, false); *got != plain {
		t.Fatalf("literal: %q", *got)
	}
	// within_cwd: a file outside the cwd is refused with exit 2
	other := t.TempDir()
	c.deps.Getwd = func() (string, error) { return other, nil }
	_, err := readText(c, &at, true)
	ee := exitOf(t, err)
	if ee.Code != 2 || ee.Msg != at+": @path must be inside the current directory ("+other+"); copy the file there or pass the text inline" {
		t.Fatalf("outside cwd: %+v", ee)
	}
	if got, err := readText(c, &at, false); err != nil || *got != "file text" {
		t.Fatalf("without within_cwd: %v %v", got, err)
	}
	// a symlink inside the cwd that points outside is refused (resolve follows symlinks)
	link := filepath.Join(other, "link.txt")
	os.Symlink(f, link)
	l := "@" + link
	if _, err := readText(c, &l, true); exitOf(t, err).Code != 2 {
		t.Fatal("symlink escaping the cwd must be refused")
	}
	// a directory is something Python crashes on: delegate
	d := "@" + dir
	if _, err := readText(c, &d, false); !errors.Is(err, errDelegate) {
		t.Fatalf("directory: %v", err)
	}
}

func TestRequireRole(t *testing.T) {
	c, r := wctx(t, "claude-coder-x")
	r.sql("INSERT INTO agents(name,role,status) VALUES('claude-coder-x','coder','live'),('claude-director','director','live')")
	if role, err := requireRole(c, "coder", "qa"); err != nil || role != "coder" {
		t.Fatalf("allowed role: %q %v", role, err)
	}
	_, err := requireRole(c, "director", "lead")
	if ee := exitOf(t, err); ee.Code != 3 || ee.Msg != "role 'coder' (claude-coder-x) may not do this; allowed: director, lead" {
		t.Fatalf("refusal: %+v", ee)
	}
	c.who = "owner"
	if role, err := requireRole(c, "director"); err != nil || role != "owner" {
		t.Fatalf("owner: %q %v", role, err)
	}
	c.who = "ghost"
	_, err = requireRole(c, "director")
	if ee := exitOf(t, err); ee.Msg != "role 'unknown' (ghost) may not do this; allowed: director" {
		t.Fatalf("unknown agent: %+v", ee)
	}
}

func TestPmOwnGoal(t *testing.T) {
	c, r := wctx(t, "claude-pm-a")
	r.sql("INSERT INTO agents(name,role,status,goal_id) VALUES('claude-pm-a','pm','live','G-001'),('claude-pm-b','pm','live',NULL),('claude-coder-x','coder','live','G-009')")
	if err := pmOwnGoal(c, "G-001", "goal plan"); err != nil {
		t.Fatalf("own goal: %v", err)
	}
	err := pmOwnGoal(c, "G-002", "goal plan")
	if ee := exitOf(t, err); ee.Code != 3 || ee.Msg != "claude-pm-a works on G-001; goal plan is in G-002" {
		t.Fatalf("other goal: %+v", ee)
	}
	c.who = "claude-pm-b"
	err = pmOwnGoal(c, "G-002", "goal plan")
	if ee := exitOf(t, err); ee.Msg != "claude-pm-b works on no goal; goal plan is in G-002" {
		t.Fatalf("no goal: %+v", ee)
	}
	c.who = "claude-coder-x" // only a pm is limited
	if err := pmOwnGoal(c, "G-002", "x"); err != nil {
		t.Fatalf("non-pm: %v", err)
	}
}

func TestCfgText(t *testing.T) {
	c, r := wctx(t, "owner")
	r.sql("DELETE FROM config")
	if v, err := cfgText(c, "org_prefix_default"); err != nil || v != defaultMap()["org_prefix_default"] {
		t.Fatalf("default: %q %v", v, err)
	}
	r.sql("INSERT INTO config(key,value) VALUES('org_prefix_default','1')")
	if v, _ := cfgText(c, "org_prefix_default"); v != "1" {
		t.Fatalf("row: %q", v)
	}
	if _, err := cfgText(c, "no_such_key"); !errors.Is(err, errDelegate) {
		t.Fatalf("unknown key: %v", err)
	}
}
