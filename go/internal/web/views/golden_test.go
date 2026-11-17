package views

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// The goldens were produced by Python with TZ=UTC; "local" therefore means UTC here.
func TestMain(m *testing.M) {
	data.Loc = time.UTC
	os.Exit(m.Run())
}

// testNow is 2026-11-14T12:00:00+01:00, the "now" of every golden (the same instant the Python generator uses).
var testNow = time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)

func fp(f float64) *float64 { return &f }
func sp(s string) *string   { return &s }

var testDefaults = []data.Default{{Key: "max_live_agents", Value: data.Int(6)}, {Key: "max_five_hour_pct", Value: data.Int(92)},
	{Key: "max_seven_day_pct", Value: data.Int(92)}, {Key: "min_mem_available_mb", Value: data.Int(1024)}}

// testSnap is the token snapshot the Python generator uses (gen_views_golden.py SNAP).
func testSnap() *tokens.Snapshot {
	return &tokens.Snapshot{Ready: true, Sessions: map[string]*tokens.Session{
		"s-t5": {FirstTS: sp("2026-11-06T08:00:00+01:00"), LastTS: sp("2026-11-06T09:00:00+01:00"), AgentName: sp("claude-coder-t-5"), Project: "p", Cwd: sp("/w/worktrees/G-001-T-5")},
		"s-r2": {FirstTS: sp("2026-11-12T09:00:00+01:00"), LastTS: sp("2026-11-12T10:00:00+01:00"), AgentName: sp("claude-reviewer-t-2"), Project: "p", Cwd: sp("/w/worktrees/G-001-T-2")},
	}, Rows: []tokens.Row{
		{Session: "s-t5", Model: "claude-sonnet-5-5", Day: "2026-11-06", Input: 1000, Output: 2000, CacheRead: 30000, CacheWrite5m: 4000, Requests: 5},
		{Session: "s-r2", Model: "claude-opus-5-5", Day: "2026-11-12", Input: 500, Output: 800, CacheRead: 9000, CacheWrite5m: 1000, Requests: 3},
		{Session: "s-nobody", Model: "claude-sonnet-5-5", Day: "2026-11-12", Input: 10, Output: 20, Requests: 1},
	}}
}

// newTestDB builds the seeded fixture of the data layer (+ testdata/extra.sql) and opens it read-only.
func newTestDB(t testing.TB, extra ...string) *data.Conn {
	t.Helper()
	dbPath := dbtest.New(t, t.TempDir(), 2)
	d := dbtest.Raw(t, dbPath)
	defer d.Close()
	for _, f := range append([]string{filepath.Join("..", "data", "testdata", "fixture.sql"), filepath.Join("testdata", "extra.sql")}, extra...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	c, err := data.Connect(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func newTestCtx(c *data.Conn, query map[string]string) *Ctx {
	mem := 4096.0
	return &Ctx{
		DB: c, Now: testNow, Query: query, Defaults: testDefaults,
		Tokens:  func() *tokens.Snapshot { return testSnap() },
		Usage:   func() *data.Usage { return &data.Usage{FiveHour: fp(50), SevenDay: fp(10), Fresh: true, AgeMin: 1} },
		Mem:     func() *float64 { return &mem },
		Machine: func() Machine { return Machine{Load1: fp(1.5), CPUs: 8} },
		CI: func(string) *data.CIEntry {
			return &data.CIEntry{State: "OPEN", Checks: []data.Check{{Name: "lint", Conclusion: "success"}}}
		},
		Cursor: func() (*tokens.DisplayData, error) { return tokens.Display(c.DB, 0) },
	}
}

// renderPage renders path like web/server.py _view: route, view, shell status, page.
func renderPage(t testing.TB, c *data.Conn, path string, query map[string]string) string {
	t.Helper()
	v, params, active, ok := Match(path)
	if !ok {
		t.Fatalf("no route for %s", path)
	}
	ctx := newTestCtx(c, query)
	title, body, err := v.Render(ctx, params)
	if err != nil {
		t.Fatalf("render %s: %v", path, err)
	}
	mem := 4096.0
	sh, err := c.ShellStatus(&data.Usage{FiveHour: fp(50), SevenDay: fp(10), Fresh: true, AgeMin: 1}, testDefaults, &mem, testNow,
		func(cc *data.Conn, _ time.Time) (any, error) { return tokens.Display(cc.DB, 0) })
	if err != nil {
		t.Fatal(err)
	}
	return Page(title, body, PageOpts{Active: active, Shell: &ShellView{Shell: *sh, Now: testNow}, Org: data.OrgParam(query), Path: path})
}

type goldenCase struct {
	name, path string
	query      map[string]string
}

// goldenCases mirror CASES of the generator (see the PR body); the golden file of each is testdata/golden/<name>.txt.
func goldenCases() []goldenCase {
	q := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	return []goldenCase{
		{"home", "/", q()}, {"home_org_cursor", "/", q("org", "cursor")}, {"home_org_claude", "/", q("org", "claude")},
		{"goals", "/goals", q()}, {"goals_sort_progress", "/goals", q("sort", "progress")}, {"goals_sort_updated", "/goals", q("sort", "updated")},
		{"goals_org_cursor", "/goals", q("org", "cursor", "sort", "priority")},
		{"goal_g001", "/goal/G-001", q()}, {"goal_g002", "/goal/G-002", q()}, {"goal_g003", "/goal/G-003", q()}, {"goal_missing", "/goal/G-999", q()},
		{"tasks", "/tasks", q()}, {"tasks_review", "/tasks", q("status", "in_review", "sort", "cost", "dir", "asc")},
		{"tasks_goal", "/tasks", q("goal", "G-001", "sort", "title", "dir", "desc")},
		{"tasks_done_wall", "/tasks", q("status", "done", "sort", "wall", "dir", "desc")},
		{"tasks_org_cursor", "/tasks", q("org", "cursor")}, {"tasks_none", "/tasks", q("goal", "G-003", "status", "blocked")},
		{"tasks_pr_sort", "/tasks", q("sort", "pr", "dir", "asc")},
		{"task_t1", "/task/T-1", q()}, {"task_t2", "/task/T-2", q()}, {"task_t5", "/task/T-5", q()}, {"task_t9", "/task/T-9", q()},
		{"task_t11", "/task/T-11", q()}, {"task_t3", "/task/T-3", q()}, {"task_missing", "/task/T-404", q()},
	}
}

func goldenText(text string, links []string) string {
	return "TEXT\n" + text + "\nLINKS\n" + strings.Join(links, "\n") + "\n"
}

// TestViewsDOMGolden compares the normalised DOM text and the link/anchor set of every page with the Python page on the
// same seeded database (testdata/golden/<case>.txt, generated by the script in the PR body).
func TestViewsDOMGolden(t *testing.T) {
	c := newTestDB(t)
	cases := goldenCases()
	files, _ := filepath.Glob(filepath.Join("testdata", "golden", "*.txt"))
	if len(files) != len(cases) {
		t.Errorf("%d golden files but %d cases: every golden must have a case and the other way round", len(files), len(cases))
	}
	for _, gc := range cases {
		gc := gc
		t.Run(gc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "golden", gc.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			_, want, ok := strings.Cut(string(raw), "\n") // drop the "# path?query" header
			if !ok {
				t.Fatalf("golden %s has no header line", gc.name)
			}
			text, links := NormalizeDOM(renderPage(t, c, gc.path, gc.query))
			if got := goldenText(text, links); got != want {
				t.Errorf("%s differs from the Python page\n%s", gc.name, firstDiff(got, want))
			}
		})
	}
}

// firstDiff shows the first differing line (text is one long line, so a window around the first differing byte).
func firstDiff(got, want string) string {
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gl) && i < len(wl); i++ {
		if gl[i] == wl[i] {
			continue
		}
		a, b := gl[i], wl[i]
		k := 0
		for k < len(a) && k < len(b) && a[k] == b[k] {
			k++
		}
		lo := k - 80
		if lo < 0 {
			lo = 0
		}
		cut := func(s string) string {
			hi := k + 120
			if hi > len(s) {
				hi = len(s)
			}
			if lo > len(s) {
				return ""
			}
			return s[lo:hi]
		}
		return fmt.Sprintf("line %d, byte %d:\n  go : …%s\n  py : …%s", i, k, cut(a), cut(b))
	}
	return fmt.Sprintf("line counts differ: go %d, python %d", len(gl), len(wl))
}
