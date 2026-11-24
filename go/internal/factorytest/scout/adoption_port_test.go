package scout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	wd "factory/go/internal/web/data"
)

// Port of tests/test_adoption.py (AdoptionTests 18 cases + SessionCoverageTests 4 cases = 22; see ported_scout.txt).
// web.adoption is Python only (no Go port yet): the report is run as `python3 -m web.adoption report` in the repo
// root against the real fixtures tests/fixtures/adoption/proj, a temp factory DB and the gh stub, so every case
// calls e.NeedPython() and skips where python3 is absent. The three cases that import web.context in Python call
// the Go port internal/web/data (ConnectRO, Baseline, PerPR) instead.
// not ported: none.

const adURL = "https://example.com/o/r/pull/%d"

type adJ = map[string]any

type adFx struct {
	t        *testing.T
	e        *ft.Env
	projects string
}

func adRepoFile(t testing.TB, rel ...string) string {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{root}, rel...)...)
}

func adPR(files []string, add, del int) adJ {
	var fl []any
	for _, p := range files {
		fl = append(fl, adJ{"path": p, "additions": 1, "deletions": 0})
	}
	return adJ{"files": fl, "additions": add, "deletions": del}
}

func adExec(t testing.TB, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// adSetGH answers `gh pr view <url>` per PR from a scripted rule (the Go stub's form of the Python set_gh prs=);
// a PR without a rule makes gh fail, like a PR missing from FAKE_GH_STATE.
func adSetGH(t testing.TB, e *ft.Env, prs adJ) {
	t.Helper()
	urls := adKeys(prs)
	var rules []ft.StubRule
	for _, u := range urls {
		b, err := json.Marshal(prs[u])
		if err != nil {
			t.Fatal(err)
		}
		rules = append(rules, ft.StubRule{Match: []string{"pr", "view", u + " --json"}, Out: string(b) + "\n"})
	}
	e.Script("gh", rules...)
}

func adVerdict(t testing.TB, e *ft.Env, n int, verdict, day string) {
	adExec(t, e, "INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES(?,?,'judge',?,?)",
		fmt.Sprintf("T-%04d", n), fmt.Sprintf(adURL, n), verdict, day)
}

func adAgent(t testing.TB, e *ft.Env, name, role, task, sid string) {
	t.Helper()
	e.SeedAgent(ft.AgentRow{Name: name, Role: role, Goal: "G-001", Task: task})
	adExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", sid, name)
}

func adCopyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// adSetup is AdoptionTests.setUp.
func adSetup(t *testing.T) *adFx {
	e := ft.New(t)
	e.NeedPython()
	f := &adFx{t: t, e: e, projects: filepath.Join(e.Home, "claude-projects")}
	adCopyTree(t, adRepoFile(t, "tests", "fixtures", "adoption", "proj"), filepath.Join(f.projects, "proj"))
	e.SeedGoal("G-001", "", "zz-test goal")
	merged := map[int]string{1: "2026-10-10", 2: "2026-10-12", 3: "2026-10-15", 4: "2026-09-01",
		5: "2026-10-16", 6: "2026-10-16", 7: "2026-10-16", 8: "2026-10-16", 9: "2026-10-16", 10: "2026-10-16"}
	e.SeedTask(ft.Task{ID: "T-0001", Status: "done", PRURL: fmt.Sprintf(adURL, 1)})
	adExec(t, e, "INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES('T-0001',?,'judge','reject',?)",
		fmt.Sprintf(adURL, 1), "2026-10-09T12:00:00+00:00")
	for n := 1; n <= 10; n++ {
		if n != 1 {
			e.SeedTask(ft.Task{ID: fmt.Sprintf("T-%04d", n), Status: "done", PRURL: fmt.Sprintf(adURL, n)})
		}
		adVerdict(t, e, n, "merge", merged[n]+"T12:00:00+00:00")
	}
	for _, a := range [][4]string{
		{"zz-coder-1", "coder", "T-0001", "sess-c1"}, {"zz-reviewer-1", "reviewer", "T-0001", "sess-r1"},
		{"zz-qa-1", "qa", "T-0001", "sess-q1"}, {"zz-judge-1", "judge", "T-0001", "sess-j1"},
		{"zz-coder-2", "coder", "T-0002", "sess-c2"}, {"zz-reviewer-2", "reviewer", "T-0002", "sess-r2"},
		{"zz-coder-3", "coder", "T-0003", "sess-c3"}, {"zz-coder-4", "coder", "T-0004", "sess-c4"},
		{"zz-cleaner-1", "cleaner", "", "sess-cl1"}, {"zz-scout-1", "scout", "", "sess-sc1"},
		{"zz-cleaner-2", "cleaner", "", "sess-cl2"}} {
		adAgent(t, e, a[0], a[1], a[2], a[3])
	}
	adSetGH(t, e, adJ{ // PR 3 is missing: gh fails
		fmt.Sprintf(adURL, 1):  adPR([]string{"docs/x.md"}, 20, 0),
		fmt.Sprintf(adURL, 2):  adPR([]string{"bin/factory", "docs/x.md"}, 400, 0), // bin/factory alone no longer gates
		fmt.Sprintf(adURL, 5):  adPR([]string{"roles/hooks/x"}, 10, 0),
		fmt.Sprintf(adURL, 6):  adPR([]string{"roles/settings/x.json"}, 10, 0),
		fmt.Sprintf(adURL, 7):  adPR([]string{"ci/x"}, 10, 0),
		fmt.Sprintf(adURL, 8):  adPR([]string{"web/x.py"}, 300, 201),  // 501 changed lines
		fmt.Sprintf(adURL, 9):  adPR([]string{"docs/y.md"}, 250, 250), // 500 changed lines: not over the limit
		fmt.Sprintf(adURL, 10): adPR([]string{".github/workflows/x.yml"}, 10, 0)})
	return f
}

// run is run_cli: python3 -m web.adoption report args, TZ=UTC, cwd the repo root. extra overrides the environment.
func (f *adFx) run(extra map[string]string, args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	py, err := osexec.LookPath("python3")
	if err != nil {
		f.t.Skip("python3 is not on PATH")
	}
	env := map[string]string{"TZ": "UTC", "FACTORY_CLAUDE_PROJECTS": f.projects}
	for k, v := range extra {
		env[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(ctx, py, append([]string{"-m", "web.adoption", "report"}, args...)...)
	cmd.Dir = adRepoFile(f.t)
	cmd.Env = f.e.Environ("", env)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	if ctx.Err() != nil {
		f.t.Fatalf("web.adoption timed out after 60s\n%s", se.String())
	}
	if ee, ok := err.(*osexec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func (f *adFx) runOK(args ...string) string {
	f.t.Helper()
	so, se, code := f.run(nil, args...)
	if code != 0 {
		f.t.Fatalf("web.adoption %v: exit %d\n%s", args, code, se)
	}
	return so
}

func (f *adFx) report(args ...string) adJ {
	f.t.Helper()
	out := f.runOK(append([]string{"--since", "2026-10-01", "--until", "2026-10-31", "--json"}, args...)...)
	return adDecode(f.t, out)
}

func adDecode(t testing.TB, s string) adJ {
	t.Helper()
	var m adJ
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, s)
	}
	return m
}

func adM(v any) adJ   { m, _ := v.(adJ); return m }
func adL(v any) []any { l, _ := v.([]any); return l }
func adTask(t testing.TB, rep adJ, id string) adJ {
	t.Helper()
	for _, x := range adL(rep["tasks"]) {
		if adM(x)["task"] == id {
			return adM(x)
		}
	}
	t.Fatalf("task %s not in report", id)
	return nil
}

func adEq(t testing.TB, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func adKeys(m adJ) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func TestAdoptionWindowSelectsMergedTasks(t *testing.T) {
	f := adSetup(t)
	rep := f.report()
	var ids []string
	for _, x := range adL(rep["tasks"]) {
		ids = append(ids, adM(x)["task"].(string))
	}
	eqStrings(t, "tasks", ids, []string{"T-0001", "T-0002", "T-0003", "T-0005", "T-0006", "T-0007", "T-0008", "T-0009", "T-0010"})
	adEq(t, "merged_tasks (T-0004 merged 2026-09-01 is out)", adM(rep["window"])["merged_tasks"], 9)
	adEq(t, "unattributed_sessions", adM(rep["window"])["unattributed_sessions"], 1)
}

func TestAdoptionTokensPerMergedPRByRole(t *testing.T) {
	f := adSetup(t)
	// coder sess-c1: 1510 + 2000 + 1000 + 1000 + 500 (sidechain 99999 and the resumed duplicate not counted)
	rep := f.report()
	k := adM(adM(adTask(t, rep, "T-0001"))["tokens"])
	adEq(t, "coder/reviewer/qa/judge", []any{k["coder"], k["reviewer"], k["qa"], k["judge"]}, []any{6010, 3000, 300, 200})
	adEq(t, "other", k["other"], "unknown")
	adEq(t, "total", k["total"], 9510)
	adEq(t, "complete", k["complete"], true)
	adEq(t, "duplicate_requests", adM(rep["window"])["duplicate_requests"], 1)
}

func TestAdoptionTokensMatchWebContextFormula(t *testing.T) {
	f := adSetup(t)
	c, err := wd.ConnectRO(f.e.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	base, err := wd.Baseline(f.projects, c, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := base.InputPerTask.Get("coder")
	rep := f.report()
	coders := []any{adM(adTask(t, rep, "T-0001")["tokens"])["coder"], adM(adTask(t, rep, "T-0002")["tokens"])["coder"]}
	// web.context sums context per task agent: sess-c1 6010, sess-c2 700, sess-c4 5000 (T-0003 has no usage)
	if ctx.N != 3 || ctx.Median != 5000 {
		t.Fatalf("context coder n=%d median=%d, want 3 and 5000", ctx.N, ctx.Median)
	}
	adEq(t, "coders", coders, []any{6010, 700})
}

func TestAdoptionMediansSkipUnknown(t *testing.T) {
	f := adSetup(t)
	m := adM(adM(f.report()["medians"])["tokens_per_merged_pr"])
	adEq(t, "coder", m["coder"], (6010+700)/2)
	adEq(t, "reviewer", m["reviewer"], (3000+800)/2)
	adEq(t, "total", m["total"], (9510+1500)/2) // T-0002: coder 700 + reviewer 800
}

func TestAdoptionReviewerWorkflowFields(t *testing.T) {
	f := adSetup(t)
	rep := f.report()
	t1 := adM(adTask(t, rep, "T-0001")["reviewer"])
	// agent-1 (400 + 600) is linked to Workflow call w1; agent-2 (5000) belongs to Agent call a1 and is not counted
	adEq(t, "T-0001", []any{t1["tokens"], t1["workflow_calls"], t1["workflow_subagent_tokens"]}, []any{3000, 2, 1000})
	t2 := adM(adTask(t, rep, "T-0002")["reviewer"])
	adEq(t, "T-0002", []any{t2["workflow_calls"], t2["workflow_subagent_tokens"]}, []any{1, "not logged"})
	adEq(t, "median workflow calls (2, 1)", adM(rep["medians"])["reviewer_workflow_calls"], 1)
	adEq(t, "T-0003", adM(adTask(t, rep, "T-0003")["reviewer"])["workflow_calls"], "unknown")
}

func adWrite(t testing.TB, p string, lines ...any) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptionWorkflowRunSubagentsCountedWithoutToolUseID(t *testing.T) {
	f := adSetup(t)
	// Real Workflow runs store <session>/subagents/workflows/<run>/agent-*.jsonl, meta has no toolUseId.
	run := filepath.Join(f.projects, "proj", "sess-r2", "subagents", "workflows", "wf_a")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "agent-1.meta.json"), []byte(`{"agentType": "workflow-subagent"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var lines []any
	for _, c := range [][2]int{{1, 300}, {2, 200}} {
		n := c[0]
		lines = append(lines, adJ{"type": "assistant", "sessionId": "sess-r2", "isSidechain": true, "timestamp": "2026-10-12T10:00:00Z",
			"uuid": fmt.Sprintf("u-wf-%d", n), "requestId": fmt.Sprintf("r-wf-%d", n),
			"message": adJ{"id": fmt.Sprintf("m-wf-%d", n), "model": "claude-sonnet-5-5", "content": []any{},
				"usage": adJ{"input_tokens": 0, "output_tokens": 3, "cache_read_input_tokens": c[1], "cache_creation_input_tokens": 0}}})
	}
	adWrite(t, filepath.Join(run, "agent-1.jsonl"), lines...)
	rep := f.report()
	rv := adM(adTask(t, rep, "T-0002")["reviewer"])
	adEq(t, "workflow", []any{rv["workflow_calls"], rv["workflow_subagent_tokens"]}, []any{1, 500})
	// main-session totals are unchanged by sidechain lines
	adEq(t, "reviewer tokens", adM(adTask(t, rep, "T-0002")["tokens"])["reviewer"], 800)
}

func TestAdoptionWorkflowFilesDoNotDisturbPlainSubagentAttribution(t *testing.T) {
	f := adSetup(t)
	// sess-r1 has plain nested subagent files (linked to Workflow call w1). Add a workflows/<run> file under it that
	// sorts before sess-r2.jsonl, repeats one of sess-r2's request keys and carries a foreign agent-name record.
	base := filepath.Join(f.projects, "proj")
	raw, err := os.ReadFile(filepath.Join(base, "sess-r2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var dup adJ
	if err := json.Unmarshal([]byte(strings.Split(string(raw), "\n")[0]), &dup); err != nil {
		t.Fatal(err)
	}
	with := func(over adJ) adJ {
		m := adJ{}
		for k, v := range dup {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	own := with(adJ{"uuid": "u-wf-own", "requestId": "r-wf-own", "sessionId": "sess-r1", "isSidechain": true})
	own["message"] = func() adJ {
		m := adJ{}
		for k, v := range adM(dup["message"]) {
			m[k] = v
		}
		m["id"], m["content"] = "m-wf-own", []any{}
		m["usage"] = adJ{"input_tokens": 0, "output_tokens": 1, "cache_read_input_tokens": 70, "cache_creation_input_tokens": 0}
		return m
	}()
	run := filepath.Join(base, "sess-r1", "subagents", "workflows", "wf_b")
	adWrite(t, filepath.Join(run, "agent-1.jsonl"),
		adJ{"type": "agent-name", "agentName": "zz-qa-1", "sessionId": "sess-r1"},
		with(adJ{"sessionId": "sess-r1", "isSidechain": true}), own)
	if err := os.WriteFile(filepath.Join(run, "agent-1.meta.json"), []byte(`{"agentType": "workflow-subagent"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := f.report()
	t1, t2 := adTask(t, rep, "T-0001"), adTask(t, rep, "T-0002")
	adEq(t, "T-0001 workflow_calls", adM(t1["reviewer"])["workflow_calls"], 2)
	adEq(t, "T-0001 reviewer", adM(t1["tokens"])["reviewer"], 3000)
	adEq(t, "T-0001 qa", adM(t1["tokens"])["qa"], 300)
	adEq(t, "T-0001 workflow_subagent_tokens", adM(t1["reviewer"])["workflow_subagent_tokens"], 1000)
	adEq(t, "T-0002", []any{adM(t2["tokens"])["reviewer"], adM(t2["reviewer"])["workflow_calls"]}, []any{800, 1})
	adEq(t, "duplicate_requests", adM(rep["window"])["duplicate_requests"], 1)
}

func TestAdoptionCoderCIWaitAndSearch(t *testing.T) {
	f := adSetup(t)
	c := adM(adTask(t, f.report(), "T-0001")["coder"])
	adEq(t, "ci_checks_calls", c["ci_checks_calls"], 2)
	adEq(t, "ci_checks_background", c["ci_checks_background"], 1)
	adEq(t, "turns_pr_create_to_done", c["turns_pr_create_to_done"], 2) // two assistant turns between create and done
	adEq(t, "explore_calls", c["explore_calls"], 1)                     // general-purpose Agent call is not Explore
	adEq(t, "search_results", c["search_results"], 3)                   // Read, Grep, Glob; the Bash result and sidechain not
	adEq(t, "search_result_chars", c["search_result_chars"], 4+6+10)
}

func TestAdoptionScopeGate(t *testing.T) {
	f := adSetup(t)
	rep := f.report()
	got := map[string]any{}
	for _, x := range adL(rep["tasks"]) {
		got[adM(x)["task"].(string)] = adM(adM(adM(x)["reviewer"])["scope_gate"])["gated"]
	}
	want := map[string]any{"T-0001": false, "T-0002": false, "T-0003": "unknown", "T-0005": true, "T-0006": true,
		"T-0007": true, "T-0008": true, "T-0009": false, "T-0010": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gated = %v, want %v", got, want)
	}
	sg := func(id string) adJ { return adM(adM(adTask(t, rep, id)["reviewer"])["scope_gate"]) }
	adEq(t, "T-0002 changed_lines", sg("T-0002")["changed_lines"], 400)
	adEq(t, "T-0005 reasons", sg("T-0005")["reasons"], []string{"roles/hooks/x"})
	adEq(t, "T-0008 reasons", sg("T-0008")["reasons"], []string{"501 changed lines"})
	adEq(t, "T-0010 reasons", sg("T-0010")["reasons"], []string{".github/workflows/x.yml"})
	adEq(t, "summary", adM(rep["scope_gate_summary"]), adJ{"gated": 5, "not_gated": 3, "unknown": 1})
}

func TestAdoptionMissingDataIsUnknownNotZero(t *testing.T) {
	f := adSetup(t)
	rep := f.report()
	t3 := adTask(t, rep, "T-0003")
	adEq(t, "T-0003 coder (session exists but has no usage)", adM(t3["tokens"])["coder"], "unknown")
	adEq(t, "T-0003 total", adM(t3["tokens"])["total"], "unknown")
	adEq(t, "T-0003 complete", adM(t3["tokens"])["complete"], false)
	adEq(t, "T-0003 ci_checks_calls", adM(t3["coder"])["ci_checks_calls"], "unknown")
	adEq(t, "T-0002 qa (no qa agent)", adM(adTask(t, rep, "T-0002")["tokens"])["qa"], "unknown")
	adEq(t, "T-0003 gated (gh failed, exit 0)", adM(adM(t3["reviewer"])["scope_gate"])["gated"], "unknown")
}

func TestAdoptionCleanerScoutCost(t *testing.T) {
	f := adSetup(t)
	by := map[string]adJ{}
	for _, s := range adL(f.report()["cost_sessions"]) {
		by[adM(s)["agent"].(string)] = adM(s)
	}
	if len(by) != 2 || by["zz-cleaner-1"] == nil || by["zz-scout-1"] == nil { // zz-cleaner-2 ran in September
		t.Fatalf("cost sessions = %v, want zz-cleaner-1 and zz-scout-1", by)
	}
	adEq(t, "cleaner usd (1M in * 0.10 + 1M out * 0.50)", by["zz-cleaner-1"]["usd"], 0.6)
	adEq(t, "scout usd", by["zz-scout-1"]["usd"], "unknown")
	adEq(t, "scout unpriced_models", by["zz-scout-1"]["unpriced_models"], []string{"claude-mystery-9"})
}

func TestAdoptionGhNotInstalledIsUnknown(t *testing.T) {
	f := adSetup(t)
	if err := os.Remove(filepath.Join(f.e.StubBin, "gh")); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(f.e.Home, "empty-path")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	so, _, _ := f.run(map[string]string{"PATH": empty}, "--json", "--since", "2026-10-01") // python itself is found by absolute path
	if so == "" {
		so = "{}"
	}
	rep := adDecode(t, so)
	// with no gh on PATH every gate is unknown
	adEq(t, "summary", adM(rep["scope_gate_summary"]), adJ{"gated": 0, "not_gated": 0, "unknown": 9})
}

func TestAdoptionTextReport(t *testing.T) {
	f := adSetup(t)
	out := f.runOK("--since", "2026-10-01", "--until", "2026-10-31")
	for _, n := range []string{"tokens per merged PR", "T-0001", "9,510", "not logged", "unknown", "zz-cleaner-1"} {
		contains(t, "text report", out, n)
	}
}

func adSnap(t testing.TB, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		m[p] = fmt.Sprintf("%x%d", sha256.Sum256(b), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAdoptionReadOnly(t *testing.T) {
	f := adSetup(t)
	snapDB := func() string {
		b, err := os.ReadFile(f.e.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(f.e.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x%d", sha256.Sum256(b), fi.ModTime().UnixNano())
	}
	beforeDB, beforeTr := snapDB(), adSnap(t, f.projects)
	f.runOK("--json")
	if snapDB() != beforeDB {
		t.Fatal("the factory DB file changed")
	}
	if !reflect.DeepEqual(adSnap(t, f.projects), beforeTr) {
		t.Fatal("the transcript tree changed")
	}
	// (a WAL database may get empty -shm/-wal side files from any reader; the db file itself is untouched)
}

func TestAdoptionDBOpenedReadOnly(t *testing.T) {
	f := adSetup(t)
	src, err := os.ReadFile(adRepoFile(t, "web", "adoption.py"))
	if err != nil {
		t.Fatal(err)
	}
	contains(t, "web/adoption.py", string(src), "connect_ro")
	c, err := wd.ConnectRO(f.e.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Exec("UPDATE tasks SET title='x'"); err == nil {
		t.Fatal("a write on the read-only connection succeeded")
	}
}

func TestAdoptionMissingDBExits2(t *testing.T) {
	f := adSetup(t)
	_, se, code := f.run(map[string]string{"FACTORY_HOME": f.e.Root, "FACTORY_CLAUDE_PROJECTS": ""})
	if code != 2 {
		t.Fatalf("exit %d, want 2\n%s", code, se)
	}
	contains(t, "stderr", se, "factory database not found")
	contains(t, "stderr", se, "FACTORY_HOME")
}

func TestAdoptionNoTranscriptContentInOutput(t *testing.T) {
	f := adSetup(t)
	notContains(t, "report", f.runOK("--json"), "sidechain result not counted")
}

func TestAdoptionDocExists(t *testing.T) {
	p := adRepoFile(t, "docs", "adoption-metrics.md")
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%s: %v", p, err)
	}
}

// ---- SessionCoverageTests -----------------------------------------------------------------------------------------

type adTool struct {
	id, name string
	input    adJ
}

func adTline(sid string, n int, ts string, tools []adTool, in, cr, cw int) adJ {
	content := []any{}
	for _, x := range tools {
		content = append(content, adJ{"type": "tool_use", "id": x.id, "name": x.name, "input": x.input})
	}
	return adJ{"type": "assistant", "uuid": fmt.Sprintf("u-%s-%d", sid, n), "requestId": fmt.Sprintf("r-%s-%d", sid, n),
		"sessionId": sid, "timestamp": ts, "message": adJ{"id": fmt.Sprintf("m-%s-%d", sid, n), "model": "claude-sonnet-5-5",
			"content": content, "usage": adJ{"input_tokens": in, "output_tokens": 5, "cache_read_input_tokens": cr, "cache_creation_input_tokens": cw}}}
}

func adTres(sid, toolID, text string) adJ {
	return adJ{"type": "user", "sessionId": sid, "message": adJ{"content": []any{adJ{"type": "tool_result", "tool_use_id": toolID, "content": text}}}}
}

func adBash(i int, cmd string) adTool {
	return adTool{fmt.Sprintf("b%d", i), "Bash", adJ{"command": cmd}}
}

func adRep(n int, s string) string { return strings.Repeat(s, n) }

func adCovSetup(t *testing.T) *adFx {
	e := ft.New(t)
	e.NeedPython()
	f := &adFx{t: t, e: e, projects: filepath.Join(e.Home, "claude-projects")}
	if err := os.MkdirAll(filepath.Join(f.projects, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.SeedGoal("G-001", "", "zz-test goal")
	for _, n := range []int{20, 21, 22} {
		e.SeedTask(ft.Task{ID: fmt.Sprintf("T-00%d", n), Status: "done", PRURL: fmt.Sprintf(adURL, n)})
		adVerdict(t, e, n, "merge", "2026-10-20T12:00:00+00:00")
	}
	adSetGH(t, e, adJ{fmt.Sprintf(adURL, 20): adPR([]string{"docs/x.md"}, 20, 0)})
	return f
}

func (f *adFx) write(sid string, recs ...any) {
	adWrite(f.t, filepath.Join(f.projects, "proj", sid+".jsonl"), recs...)
}

// fallback is a session no agents row or tick event knows: only its cwd and its agent-name record identify it.
func (f *adFx) fallback(sid, name, task string, recs ...adJ) {
	out := []any{adJ{"type": "agent-name", "agentName": name, "sessionId": sid}}
	for _, r := range recs {
		if r["type"] == "assistant" {
			r["cwd"] = "/zz/worktrees/G-001-T-" + task
		}
		out = append(out, r)
	}
	f.write(sid, out...)
}

func (f *adFx) covReport() adJ {
	f.t.Helper()
	return adDecode(f.t, f.runOK("--json"))
}

func (f *adFx) buildT20() {
	adAgent(f.t, f.e, "zz-coder-t20", "coder", "T-0020", "c-2")
	adAgent(f.t, f.e, "zz-reviewer-t20", "reviewer", "T-0020", "r-2")
	adAgent(f.t, f.e, "zz-judge-t20", "judge", "T-0020", "j-1")
	rd := []adTool{{"t1", "Read", adJ{"file_path": "x"}}}
	f.fallback("c-1", "zz-coder-t20", "0020",
		adTline("c-1", 1, "2026-10-19T10:00:00Z", rd, 10, 1000, 100),
		adTres("c-1", "t1", adRep(7, "r")),
		adTline("c-1", 2, "2026-10-19T10:01:00Z", []adTool{adBash(1, "head -20 web/x.py")}, 0, 2000, 0),
		adTres("c-1", "b1", adRep(11, "h")),
		adTline("c-1", 3, "2026-10-19T10:02:00Z", []adTool{adBash(2, "gh pr create --fill")}, 0, 3000, 0),
		adTres("c-1", "b2", "created, not a search"))
	f.write("c-2",
		adTline("c-2", 1, "2026-10-20T10:00:00Z", []adTool{adBash(3, "git status")}, 5, 4000, 0),
		adTres("c-2", "b3", adRep(99, "s")),
		adTline("c-2", 2, "2026-10-20T10:01:00Z", []adTool{adBash(4, "cd /zz/w && grep -rn foo web/")}, 0, 4100, 0),
		adTres("c-2", "b4", adRep(13, "g")),
		adTline("c-2", 3, "2026-10-20T10:02:00Z", []adTool{adBash(5, "cat > f.py <<'EOF'\nx\nEOF")}, 0, 4200, 0),
		adTres("c-2", "b5", adRep(99, "w")),
		adTline("c-2", 4, "2026-10-20T10:03:00Z", []adTool{adBash(6, "python3 -m unittest 2>&1 | tail -5")}, 0, 4300, 0),
		adTres("c-2", "b6", adRep(99, "p")),
		adTline("c-2", 5, "2026-10-20T10:04:00Z", []adTool{adBash(7, "sed -n 1,5p web/x.py")}, 0, 4400, 0),
		adTres("c-2", "b7", adRep(17, "n")),
		adTline("c-2", 6, "2026-10-20T10:05:00Z", []adTool{adBash(8, "sed -i s/a/b/ web/x.py")}, 0, 4500, 0),
		adTres("c-2", "b8", adRep(99, "e")),
		adTline("c-2", 7, "2026-10-20T10:06:00Z", []adTool{adBash(9, "ls web")}, 0, 4600, 0),
		adTres("c-2", "b9", adRep(19, "l")),
		adTline("c-2", 8, "2026-10-20T10:07:00Z", []adTool{{"t2", "Grep", adJ{"pattern": "p"}}}, 0, 4700, 0),
		adTres("c-2", "t2", adRep(23, "G")),
		adTline("c-2", 9, "2026-10-20T10:08:00Z", []adTool{adBash(10, "factory done 'x'")}, 0, 4800, 0))
	f.fallback("r-1", "zz-reviewer-t20", "0020", adTline("r-1", 1, "2026-10-19T12:00:00Z", nil, 1, 500, 0))
	f.write("r-2", adTline("r-2", 1, "2026-10-20T11:00:00Z", nil, 2, 600, 0))
	f.write("j-1", adTline("j-1", 1, "2026-10-20T12:00:00Z", nil, 0, 700, 50))
}

func TestAdoptionAllSessionsAttributedToTheirRoleAndMatchWebContext(t *testing.T) {
	f := adCovSetup(t)
	f.buildT20()
	rep := f.covReport()
	tk := adM(adTask(t, rep, "T-0020")["tokens"])
	coder := (10 + 1000 + 100) + 2000 + 3000 + (4000 + 4100 + 4200 + 4300 + 4400 + 4500 + 4600 + 4700 + 4800) + 5
	adEq(t, "coder/reviewer/judge", []any{tk["coder"], tk["reviewer"], tk["judge"]}, []any{coder, 501 + 602, 750})
	adEq(t, "qa/other", []any{tk["qa"], tk["other"]}, []any{"unknown", "unknown"})
	adEq(t, "total", tk["total"], coder+1103+750)
	if coder != 45715 { // 6110 (attempt 1) + 39605 (attempt 2)
		t.Fatalf("coder = %d, want 45715", coder)
	}
	adEq(t, "unattributed_sessions", adM(rep["window"])["unattributed_sessions"], 0)
	c, err := wd.ConnectRO(f.e.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	per, err := wd.PerPR(f.projects, c, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range per.Tasks {
		if m.Task == "T-0020" {
			adEq(t, "context sessions/input", []any{m.Sessions, m.Input}, []any{5, tk["total"]})
			return
		}
	}
	t.Fatal("T-0020 not in web.context per_pr")
}

func TestAdoptionCoderSearchCountsBashAndToolResults(t *testing.T) {
	f := adCovSetup(t)
	f.buildT20()
	c := adM(adTask(t, f.covReport(), "T-0020")["coder"])
	// Read 7 + head 11 | grep 13 + sed -n 17 + ls 19 + Grep 23; git status, cat > <<EOF, | tail, sed -i, gh not
	adEq(t, "search_results", c["search_results"], 6)
	adEq(t, "search_result_chars", c["search_result_chars"], 7+11+13+17+19+23)
}

func TestAdoptionTurnsAcrossCoderSessionsAndUnknownCases(t *testing.T) {
	f := adCovSetup(t)
	f.buildT20()
	// T-0021: PR created, no `factory done` in any transcript. T-0022: `factory done` but no `gh pr create`.
	adAgent(t, f.e, "zz-coder-t21", "coder", "T-0021", "c-21")
	adAgent(t, f.e, "zz-coder-t22", "coder", "T-0022", "c-22")
	f.write("c-21", adTline("c-21", 1, "2026-10-20T10:00:00Z", []adTool{adBash(1, "gh pr create --fill")}, 0, 10, 0))
	f.write("c-22", adTline("c-22", 1, "2026-10-20T10:00:00Z", []adTool{adBash(1, "factory done 'x'")}, 0, 10, 0))
	got := map[string]any{}
	for _, x := range adL(f.covReport()["tasks"]) {
		got[adM(x)["task"].(string)] = adM(adM(x)["coder"])["turns_pr_create_to_done"]
	}
	// PR in attempt 1 (turn 3 of 3), done on turn 9 of attempt 2: 0 left + 8 turns before the done turn
	adEq(t, "turns", got, map[string]any{"T-0020": 8, "T-0021": "unknown", "T-0022": "unknown"})
}

func TestAdoptionJSONFieldNamesUnchanged(t *testing.T) {
	f := adCovSetup(t)
	f.buildT20()
	rep := f.covReport()
	tk := adM(adL(rep["tasks"])[0])
	chk := func(what string, got []string, want ...string) {
		t.Helper()
		eqStrings(t, what, got, want)
	}
	chk("report", adKeys(rep), "cost_sessions", "medians", "scope_gate_summary", "tasks", "window")
	chk("window", adKeys(adM(rep["window"])), "duplicate_requests", "files", "malformed_lines", "merged_tasks", "since", "unattributed_sessions", "until")
	chk("task", adKeys(tk), "coder", "goal", "merged_day", "pr_url", "reviewer", "task", "tokens")
	chk("tokens", adKeys(adM(tk["tokens"])), "coder", "complete", "judge", "other", "qa", "reviewer", "total")
	chk("reviewer", adKeys(adM(tk["reviewer"])), "scope_gate", "tokens", "workflow_calls", "workflow_subagent_tokens")
	chk("coder", adKeys(adM(tk["coder"])), "ci_checks_background", "ci_checks_calls", "explore_calls", "search_result_chars", "search_results", "turns_pr_create_to_done")
	chk("medians", adKeys(adM(rep["medians"])), "coder", "reviewer_workflow_calls", "tokens_per_merged_pr")
}
