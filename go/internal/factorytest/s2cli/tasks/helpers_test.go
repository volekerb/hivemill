package s2tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Shared helpers of the s2cli/tasks ports. Everything here is the Go form of a tests/harness.py helper or of a
// FactoryTestCase method; each port file names the Python module it ports in its header.

const prURL = ft.PRURL

// run runs args as agent ("" is the owner: no FACTORY_AGENT) and returns the result without judging the exit code.
func run(e *ft.Env, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.RunWith(ft.Opts{Agent: agent}, args...)
}

// runO runs args with full options (stdin, extra environment).
func runO(e *ft.Env, o ft.Opts, args ...string) ft.Result {
	e.T.Helper()
	return e.RunWith(o, args...)
}

// ok is FactoryTestCase.ok: exit code 0 or the test fails.
func ok(e *ft.Env, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.OKWith(ft.Opts{Agent: agent}, args...)
}

// okS is ok with stdin.
func okS(e *ft.Env, agent, stdin string, args ...string) ft.Result {
	e.T.Helper()
	return e.OKWith(ft.Opts{Agent: agent, Stdin: stdin}, args...)
}

// refused is FactoryTestCase.refused: exactly this exit code or the test fails.
func refused(e *ft.Env, code int, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.FailWith(ft.Opts{Agent: agent}, code, args...)
}

// refusedO is refused with full options.
func refusedO(e *ft.Env, code int, o ft.Opts, args ...string) ft.Result {
	e.T.Helper()
	return e.FailWith(o, code, args...)
}

func contains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func notContains(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

func eq[T comparable](t testing.TB, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func eqS(t testing.TB, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") || len(got) != len(want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// sql runs a statement and returns its rows (every value a string, NULL is "NULL").
func sql(e *ft.Env, q string, args ...any) [][]string {
	e.T.Helper()
	return e.SQL(q, args...)
}

// val returns the first column of the first row; the test fails when there is no row.
func val(e *ft.Env, q string, args ...any) string {
	e.T.Helper()
	r := e.SQL(q, args...)
	if len(r) == 0 || len(r[0]) == 0 {
		e.T.Fatalf("no row for %s %v", q, args)
	}
	return r[0][0]
}

// col returns the first column of every row.
func col(e *ft.Env, q string, args ...any) []string {
	e.T.Helper()
	var out []string
	for _, r := range e.SQL(q, args...) {
		out = append(out, r[0])
	}
	return out
}

// exec runs a write statement on the env's DB (harness env.sql with an UPDATE/INSERT).
func exec(e *ft.Env, q string, args ...any) {
	e.T.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		e.T.Fatalf("%s: %v", q, err)
	}
}

// seedTask is harness seed_task; noPR stores a NULL pr_url (seed_task(pr_url=None)).
func seedTask(e *ft.Env, t ft.Task, noPR bool) {
	e.T.Helper()
	e.SeedTask(t)
	if noPR {
		id := t.ID
		if id == "" {
			id = "T-0001"
		}
		exec(e, "UPDATE tasks SET pr_url=NULL WHERE id=?", id)
	}
}

// seedProject is harness seed_project.
func seedProject(e *ft.Env, id string, autoMerge int) {
	e.T.Helper()
	exec(e, "INSERT INTO projects(id,name,auto_merge,created) VALUES(?,?,?,'t')", id, id, autoMerge)
}

// prJSON is the pr dict tests/harness.py seed_pr stores (the Python gh stub prints it for any `pr view <url>`).
func prJSON(url, sha, state, mergeable string, draft bool, body *string, extra map[string]any) string {
	m := map[string]any{
		"number": 1, "state": state, "headRefOid": sha, "headRefName": "factory/t-0001",
		"baseRefName": "main", "mergeable": mergeable, "isDraft": draft, "url": url,
		"statusCheckRollup": []any{},
	}
	if body != nil {
		m["body"] = *body
	}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("prJSON: %v", err))
	}
	return string(b) + "\n"
}

// seedPR scripts `gh pr view <url>` like harness seed_pr (state OPEN, head sha aaaa..., mergeable) with the given
// body; extra fields are merged into the pr dict. Further rules (api, ...) go first.
func seedPR(e *ft.Env, url, state string, body *string, extra map[string]any, before ...ft.StubRule) {
	e.T.Helper()
	rules := append([]ft.StubRule{}, before...)
	rules = append(rules, ft.StubRule{Match: []string{"pr", "view", url}, Out: prJSON(url, strings.Repeat("a", 40), state, "MERGEABLE", false, body, extra)})
	e.Script("gh", rules...)
}

func ptr(s string) *string { return &s }

// notes returns the detail of every event of entity/id with action.
func events(e *ft.Env, entity, id, action string) []string {
	e.T.Helper()
	return col(e, "SELECT detail FROM events WHERE entity=? AND entity_id=? AND action=? ORDER BY id", entity, id, action)
}
