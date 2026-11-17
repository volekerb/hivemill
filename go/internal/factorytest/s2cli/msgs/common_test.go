package s2msgs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// workers is the factory worker roles used by lesson ports (tests/test_lessons.py setup).
var workers = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}

// msgSlots bounds parallel msgs tests (each may start Python for unported commands).
var msgSlots = make(chan struct{}, 3)

const (
	gateFormat = "Gate line format:"
	goodGates  = "## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests -v\n- compile exit=0 cmd=python3 -m py_compile bin/factory\n"
	shaA       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldPR      = "https://github.com/zz-test/zz-test/pull/11"
)

// lessonWorkers matches tests/test_lessons.py WORKERS (seven roles).
var lessonWorkers = workers[:7]

func greenChecks() []map[string]string {
	return []map[string]string{{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}}
}

func lessonCol(e *ft.Env, id, col string) string {
	e.T.Helper()
	return sql1(e.T, e, "SELECT "+col+" FROM lessons WHERE id=?", id)
}

func ghRead(t *testing.T, e *ft.Env) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.Root, "gh.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	st := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func ghWrite(t *testing.T, e *ft.Env, st map[string]any) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.Root, "gh.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func setGH(t *testing.T, e *ft.Env, kw map[string]any) {
	t.Helper()
	st := ghRead(t, e)
	for k, v := range kw {
		st[k] = v
	}
	ghWrite(t, e, st)
}

func appendGhRules(t *testing.T, e *ft.Env, rules ...ft.StubRule) {
	t.Helper()
	all := map[string][]ft.StubRule{}
	if b, err := os.ReadFile(e.ScriptFile); err == nil {
		if err := json.Unmarshal(b, &all); err != nil {
			t.Fatal(err)
		}
	}
	all["gh"] = append(rules, all["gh"]...)
	out, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.ScriptFile, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func prViewRule(url, head, state string, files []string, checks []map[string]string) ft.StubRule {
	rollup := []any{}
	for _, c := range checks {
		m := map[string]any{}
		for k, v := range c {
			m[k] = v
		}
		rollup = append(rollup, m)
	}
	num := 1
	if strings.Contains(url, "/pull/2") {
		num = 2
	}
	obj := map[string]any{
		"number": num, "headRefOid": head, "state": state, "mergeable": "MERGEABLE", "isDraft": false,
		"mergeStateStatus":  "CLEAN",
		"statusCheckRollup": rollup, "headRefName": "factory/t-0001", "baseRefName": "main", "url": url,
	}
	if state == "MERGED" {
		obj["mergedAt"] = "2026-10-01T10:00:00Z"
	}
	if files != nil {
		fs := make([]any, len(files))
		for i, p := range files {
			fs[i] = map[string]any{"path": p}
		}
		obj["files"] = fs
	}
	b, _ := json.Marshal(obj)
	return ft.StubRule{Match: []string{"pr", "view", url}, Out: string(b) + "\n"}
}

func installFlowGh(t *testing.T, e *ft.Env, views ...ft.StubRule) {
	t.Helper()
	appendGhRules(t, e, append(views, ft.StubRule{Match: []string{"pr", "merge"}, Exit: 0})...)
}

func appendGhMainAPI(t *testing.T, e *ft.Env) {
	t.Helper()
	if e.RepoURL == "" {
		t.Fatal("appendGhMainAPI: UseGitHead first")
	}
	cmd := exec.Command("git", "-C", e.RepoURL, "rev-parse", "refs/heads/main")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse main: %v", err)
	}
	appendGhRules(t, e, ft.StubRule{Match: []string{"api", "commits/main"}, Out: strings.TrimSpace(string(out)) + "\n"})
}

func prFiles(t *testing.T, e *ft.Env, url string, files []string) {
	t.Helper()
	st := ghRead(t, e)
	prs, _ := st["prs"].(map[string]any)
	pr, _ := prs[url].(map[string]any)
	list := make([]any, len(files))
	for i, p := range files {
		list[i] = map[string]any{"path": p}
	}
	pr["files"] = list
	ghWrite(t, e, st)
}

func slot(t *testing.T) {
	t.Helper()
	t.Parallel()
	msgSlots <- struct{}{}
	t.Cleanup(func() { <-msgSlots })
}

func as(e *ft.Env, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.RunWith(ft.Opts{Agent: agent}, args...)
}

func ok(t testing.TB, e *ft.Env, agent string, extra map[string]string, args ...string) ft.Result {
	t.Helper()
	r := e.RunWith(ft.Opts{Agent: agent, Env: extra}, args...)
	if r.Exit != 0 {
		t.Fatalf("%v as %q: exit %d\nstdout: %s\nstderr: %s", args, agent, r.Exit, r.Stdout, r.Stderr)
	}
	return r
}

func refused(t testing.TB, e *ft.Env, agent string, code int, extra map[string]string, args ...string) ft.Result {
	t.Helper()
	r := e.RunWith(ft.Opts{Agent: agent, Env: extra}, args...)
	if r.Exit != code {
		t.Fatalf("%v as %q: exit %d, want %d\nstdout: %s\nstderr: %s", args, agent, r.Exit, code, r.Stdout, r.Stderr)
	}
	return r
}

func contains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want substring %q", what, got, want)
	}
}

func notContains(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

func sqlExec(t testing.TB, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func sql1(t testing.TB, e *ft.Env, q string, args ...any) string {
	t.Helper()
	rows := e.SQL(q, args...)
	if len(rows) != 1 || len(rows[0]) < 1 {
		t.Fatalf("%s: got %v", q, rows)
	}
	return rows[0][0]
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// verdictAutoSHA mirrors FactoryEnv.run auto_sha: append --sha <PR head> to `verdict --pass` when missing.
func verdictAutoSHA(args ...string) []string {
	if len(args) == 0 || args[0] != "verdict" || !hasArg(args, "--pass") || hasArg(args, "--sha") {
		return args
	}
	out := append([]string(nil), args...)
	return append(out, "--sha", shaA)
}

func okAs(e *ft.Env, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.OKWith(ft.Opts{Agent: agent}, verdictAutoSHA(args...)...)
}

func failAs(e *ft.Env, agent string, code int, args ...string) ft.Result {
	e.T.Helper()
	return e.FailWith(ft.Opts{Agent: agent}, code, verdictAutoSHA(args...)...)
}

func gatesSummary(text ...string) string {
	t := "ok"
	if len(text) > 0 && text[0] != "" {
		t = text[0]
	}
	return t + "\n\n## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests -v\n"
}

func seedProject(e *ft.Env, pid string) {
	e.T.Helper()
	if pid == "" {
		pid = "zz-test-proj"
	}
	sqlExec(e.T, e, "INSERT INTO projects(id,name,auto_merge,created) VALUES(?,?,1,'t')", pid, pid)
}

func validPRBody() string {
	return "## Summary\nWhat changed. \n\n## Why\nT-0001 motivation.\n\n## Changes\n- one change\n\n" +
		"## Test evidence\n`python3 -m unittest` -> OK\n\n## Risk / rollback\nRevert this PR.\n\n" +
		"<details><summary>Acceptance walk</summary>\n\n## Acceptance walk\n1. met - cmd -> out\n</details>\n"
}

type prStubOpts struct {
	sha    string
	checks []map[string]string
	body   string
}

// seedPRView scripts gh pr view for ft.PRURL (Go stub does not read gh.json).
func seedPRView(t *testing.T, e *ft.Env, o prStubOpts) {
	t.Helper()
	sha := o.sha
	if sha == "" {
		sha = shaA
	}
	rollup := "[]"
	if o.checks != nil {
		b, err := json.Marshal(o.checks)
		if err != nil {
			t.Fatal(err)
		}
		rollup = string(b)
	}
	obj := map[string]any{
		"number":            1,
		"headRefOid":        sha,
		"state":             "OPEN",
		"mergeable":         "MERGEABLE",
		"isDraft":           false,
		"statusCheckRollup": json.RawMessage(rollup),
		"headRefName":       "factory/t-0001",
		"baseRefName":       "main",
		"url":               ft.PRURL,
	}
	if o.body != "" {
		obj["body"] = o.body
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	scriptPRView(t, e, ft.PRURL, b)
}

// scriptPRView adds a gh pr view rule without replacing other gh rules (e.g. from UseGitHead).
func scriptPRView(t *testing.T, e *ft.Env, url string, payload []byte) {
	t.Helper()
	all := map[string][]ft.StubRule{}
	if b, err := os.ReadFile(e.ScriptFile); err == nil {
		if err := json.Unmarshal(b, &all); err != nil {
			t.Fatal(err)
		}
	}
	rule := ft.StubRule{Match: []string{"pr", "view", url}, Out: string(payload) + "\n"}
	rules := append([]ft.StubRule{rule}, all["gh"]...)
	all["gh"] = rules
	out, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.ScriptFile, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedPRViewURL scripts gh pr view for an arbitrary PR url.
func seedPRViewURL(t *testing.T, e *ft.Env, url string, o prStubOpts) {
	t.Helper()
	sha := o.sha
	if sha == "" {
		sha = shaA
	}
	rollup := "[]"
	if o.checks != nil {
		b, err := json.Marshal(o.checks)
		if err != nil {
			t.Fatal(err)
		}
		rollup = string(b)
	}
	obj := map[string]any{
		"number": 9, "headRefOid": sha, "state": "OPEN", "mergeable": "MERGEABLE", "url": url,
		"statusCheckRollup": json.RawMessage(rollup),
	}
	if o.body != "" {
		obj["body"] = o.body
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	scriptPRView(t, e, url, b)
}

func verdictCounts(e *ft.Env) (verdicts, events int) {
	return e.Count("SELECT count(*) FROM verdicts"), e.Count("SELECT count(*) FROM events")
}

func assertGateRefused(t *testing.T, e *ft.Env, agent, summary string, extra []string, reason string) {
	t.Helper()
	beforeV, beforeE := verdictCounts(e)
	args := append([]string{"verdict", "T-0001", "--pass", "--summary", summary}, extra...)
	r := failAs(e, agent, 5, args...)
	combined := r.Stdout + r.Stderr
	contains(t, "gate refusal output", combined, gateFormat)
	if reason != "" {
		contains(t, "gate refusal output", combined, reason)
	}
	afterV, afterE := verdictCounts(e)
	if afterV != beforeV || afterE != beforeE {
		t.Fatalf("counts after refusal = verdicts %d events %d, want unchanged %d %d", afterV, afterE, beforeV, beforeE)
	}
}

func reportRecv(e *ft.Env) string {
	rows := e.SQL("SELECT recipient FROM messages WHERE kind='report' ORDER BY id DESC LIMIT 1")
	if len(rows) == 0 {
		return ""
	}
	return rows[0][0]
}

func report(e *ft.Env, agent string, extra ...string) (ft.Result, string) {
	e.T.Helper()
	args := append([]string{"report", "h", "b", "--goal", "G-001"}, extra...)
	r := as(e, agent, args...)
	return r, reportRecv(e)
}

func ghPRMergeCalls(e *ft.Env) int {
	n := 0
	for _, c := range e.Calls("gh") {
		if len(c.Argv) >= 2 && c.Argv[0] == "pr" && c.Argv[1] == "merge" {
			n++
		}
	}
	return n
}

func tableRows(e *ft.Env, table string) [][]string {
	return e.SQL("SELECT * FROM " + table)
}

// needPy skips a test that drives a command the Go CLI still hands to Python (config, spawn, tick, judge, ...) when
// python3 is not on PATH (AC6). Same message as Env.NeedPython, but usable before the Env exists.
func needPy(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH: this test drives a command the Go CLI still hands to Python; it runs where python3 exists")
	}
}
