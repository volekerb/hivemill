package hm

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const factorySchema = `
CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL, repo_path TEXT, gh_repo TEXT, base_branch TEXT NOT NULL DEFAULT 'main',
  required_checks TEXT NOT NULL DEFAULT '', created TEXT NOT NULL DEFAULT 't');
CREATE TABLE goals (id TEXT PRIMARY KEY, project_id TEXT, title TEXT NOT NULL, success_criteria TEXT, status TEXT NOT NULL DEFAULT 'active',
  priority INTEGER NOT NULL DEFAULT 2, created TEXT NOT NULL DEFAULT 't', updated TEXT NOT NULL DEFAULT 't');
CREATE TABLE tasks (id TEXT PRIMARY KEY, goal_id TEXT NOT NULL, team_id TEXT, title TEXT NOT NULL, spec TEXT, acceptance TEXT,
  role TEXT NOT NULL DEFAULT 'coder', status TEXT NOT NULL DEFAULT 'todo', assignee TEXT, depends_on TEXT NOT NULL DEFAULT '[]', branch TEXT,
  worktree TEXT, pr_url TEXT, created TEXT NOT NULL DEFAULT 't', updated TEXT NOT NULL DEFAULT 't', priority INTEGER,
  waits TEXT NOT NULL DEFAULT 'internal', tier TEXT);
CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, team_id TEXT, task_id TEXT, status TEXT NOT NULL, pane_id TEXT,
  model TEXT, session_id TEXT, cwd TEXT, spawn_args TEXT, summary TEXT, started TEXT, ended TEXT, org TEXT);
CREATE TABLE verdicts (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, pr_url TEXT, head_sha TEXT, role TEXT NOT NULL,
  verdict TEXT NOT NULL, summary TEXT, author TEXT, created TEXT NOT NULL);
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT, entity_id TEXT, action TEXT NOT NULL, detail TEXT);
CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO projects(id,name,repo_path,gh_repo) VALUES('www','www','/nonexistent','zz-test/www');
INSERT INTO goals(id,project_id,title) VALUES('G-001','www','zz-test goal');
INSERT INTO tasks(id,goal_id,title,spec,acceptance,status) VALUES('T-0001','G-001','zz-test task','spec','1. x - cmd:true => 0','in_progress');
`

func testRunner(t *testing.T, actor string) (*Runner, *SQLite) {
	t.Helper()
	home := t.TempDir()
	dbp := filepath.Join(home, "db", "factory.db")
	_ = os.MkdirAll(filepath.Dir(dbp), 0o700)
	raw, err := sql.Open("sqlite", dbp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(factorySchema); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	s, err := OpenSQLite(dbp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := Env{Home: home, UserHome: t.TempDir(), Actor: actor}
	tools := Tools{
		Git: func(repo string, args ...string) (int, string, string) {
			switch args[0] {
			case "status":
				return 0, " M index.html", ""
			case "rev-parse":
				return 0, strings.Repeat("a", 40), ""
			}
			return 1, "", "no"
		},
		GH: func(_ string, args ...string) (int, string, string) {
			if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
				return 0, "https://github.com/zz-test/www/pull/1", ""
			}
			return 1, "", "no"
		},
		HC: func(...string) (int, string, string) { return 0, `{"terminals": []}`, "" },
		Sh: func(string, time.Duration, []string, ...string) (int, string) { return 0, "" },
	}
	return &Runner{Env: e, Store: s, Tools: tools, Log: Log{Path: filepath.Join(e.StateDir(), "events.jsonl")}, Out: io.Discard, ReadyWait: 20 * time.Millisecond,
		Poll: time.Millisecond}, s
}

func TestGateHookBlocksThenFailsAtTheCap(t *testing.T) {
	r, s := testRunner(t, "claude-task-t-0001")
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: t.TempDir(),
		Branch: "factory/t-0001", Agent: "claude-task-t-0001", State: Working, Round: 1}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	// the session has written its findings: each stop is a gate attempt
	_ = os.MkdirAll(filepath.Join(run.Worktree, ".hivemill"), 0o755)
	_ = os.WriteFile(filepath.Join(run.Worktree, ".hivemill", "findings.json"), []byte(`{"findings": []}`), 0o600)
	var outs []string
	for i := 0; i < 3; i++ {
		var out bytes.Buffer
		if err := r.GateHook(strings.NewReader(`{"session_id":"s-1"}`), &out); err != nil {
			t.Fatal(err)
		}
		outs = append(outs, out.String())
	}
	for i, want := range []string{"(1/3)", "(2/3)"} {
		var d map[string]string
		if json.Unmarshal([]byte(outs[i]), &d) != nil || d["decision"] != "block" || !strings.Contains(d["reason"], want) ||
			!strings.Contains(d["reason"], "uncommitted") {
			t.Errorf("stop %d: %q", i+1, outs[i])
		}
	}
	if outs[2] != "" {
		t.Errorf("third stop must be allowed (the cap), got %q", outs[2])
	}
	got, _ := s.Run("T-0001")
	if got.State != Failed || got.GateBlocksTotal != 3 {
		t.Errorf("run %s blocks %d", got.State, got.GateBlocksTotal)
	}
	var status, waits string
	_ = s.db.QueryRow(`SELECT status, waits FROM tasks WHERE id='T-0001'`).Scan(&status, &waits)
	if status != "blocked" || waits != "owner" {
		t.Errorf("task %s/%s, want blocked/owner", status, waits)
	}
	var out bytes.Buffer // a later stop is not gated again: no loop
	_ = r.GateHook(strings.NewReader(`{}`), &out)
	if out.Len() != 0 {
		t.Errorf("stop after FAIL: %q", out.String())
	}
	if ss, _ := s.Sessions("T-0001"); len(ss) != 1 || ss[0].SID != "s-1" {
		t.Errorf("sessions %+v", ss)
	}
	evs, _ := r.Log.Read()
	n := 0
	for _, e := range evs {
		if e.Event == "gate.blocked" {
			n++
		}
	}
	if n != 3 || evs[len(evs)-1].Event != "task.failed" {
		t.Errorf("events: %d blocked, last %s", n, evs[len(evs)-1].Event)
	}
}

func TestJudgeHookRecordsVerdictAndBlocksOnceWithoutOne(t *testing.T) {
	r, s := testRunner(t, "claude-task-judge-t-0001-r1")
	head := strings.Repeat("b", 40)
	run := &Run{Task: "T-0001", Goal: "G-001", Agent: "claude-task-t-0001", Judges: []string{"claude-task-judge-t-0001-r1"},
		State: Judging, Round: 1, JudgeHead: head, PRURL: "https://github.com/zz-test/www/pull/1"}
	_ = s.PutRun(run)
	var out bytes.Buffer
	_ = r.JudgeHook(strings.NewReader(`{"last_assistant_message":"looks fine"}`), &out)
	if !strings.Contains(out.String(), `"block"`) {
		t.Fatalf("no verdict must block once: %q", out.String())
	}
	out.Reset()
	msg := "ok\n```json\n{\"verdict\": \"MERGE\", \"reasons\": [\"does what was asked\"], \"criteria_assessed\": [{\"id\": 1, \"proven\": true}]}\n```"
	in, _ := json.Marshal(map[string]string{"last_assistant_message": msg, "session_id": "j-1"})
	if err := r.JudgeHook(bytes.NewReader(in), &out); err != nil || out.Len() != 0 {
		t.Fatalf("verdict: err %v out %q", err, out.String())
	}
	got, _ := s.Run("T-0001")
	if got.State != Judged || got.Verdicts[0].Verdict != "MERGE" {
		t.Errorf("run %s %+v", got.State, got.Verdicts)
	}
	if v, ok, _ := s.LaneVerdict("T-0001", head); !ok || v != "pass" {
		t.Errorf("lane verdict %q %v", v, ok)
	}
	if _, ok, _ := s.LaneVerdict("T-0001", strings.Repeat("c", 40)); ok {
		t.Error("a verdict holds only for its own head")
	}
}

func TestParseVerdict(t *testing.T) {
	for _, bad := range []string{"no block", "```json\n{\"verdict\": \"maybe\", \"reasons\": [\"x\"], \"criteria_assessed\": []}\n```",
		"```json\n{\"verdict\": \"REJECT\", \"reasons\": [], \"criteria_assessed\": []}\n```"} {
		if _, err := ParseVerdict(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestFindingsAndEvidence(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "f.json")
	_ = os.WriteFile(p, []byte(`{"findings": [{"severity": "high", "file": "a", "line": 1, "issue": "x", "resolution": "fixed: y"}]}`), 0o600)
	if probs, f := CheckFindings(p); len(probs) != 0 || len(f) != 1 {
		t.Errorf("%v %v", probs, f)
	}
	_ = os.WriteFile(p, []byte(`{"findings": [{"severity": "urgent", "issue": "", "resolution": "later"}]}`), 0o600)
	if probs, _ := CheckFindings(p); len(probs) != 3 {
		t.Errorf("%v", probs)
	}
	_ = os.WriteFile(p, []byte(`{"findings": []}`), 0o600)
	if probs, _ := CheckFindings(p); len(probs) != 0 {
		t.Errorf("empty list: %v", probs)
	}
	e := filepath.Join(d, "e.json")
	_ = os.WriteFile(e, []byte(`{"criteria": [{"id": 1}]}`), 0o600)
	if probs := CheckEvidence(e, Criteria(good)); len(probs) != 1 || !strings.Contains(probs[0], "2, 3") {
		t.Errorf("%v", probs)
	}
}

func TestAffectedFullNeededLeavesItToCI(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Tools.Git = func(string, ...string) (int, string, string) { return 0, "", "" }
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) {
		return 0, "FULL needed: rely on CI (no test module mentions go/x.go; run the full suite)\n"
	}
	if ok, note := r.affected(t.TempDir(), "main"); !ok || !strings.Contains(note, "CI runs the full suite") {
		t.Errorf("FULL needed must pass to CI: %v %q", ok, note)
	}
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { return 0, "nothing\n" }
	if ok, _ := r.affected(t.TempDir(), "main"); ok {
		t.Error("exit 0 with no tests run must fail")
	}
}

func TestGateHookDefersWithoutAPR(t *testing.T) {
	r, s := testRunner(t, "claude-task-t-0001")
	r.Tools.GH = func(string, ...string) (int, string, string) { return 0, "", "" }
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Repo: "zz-test/www", Base: "main", Worktree: t.TempDir(), Branch: "factory/t-0001",
		Agent: "claude-task-t-0001", State: Working, Round: 1})
	var out bytes.Buffer
	_ = r.GateHook(strings.NewReader(`{}`), &out)
	got, _ := s.Run("T-0001")
	if out.Len() != 0 || got.GateBlocks != 0 || got.State != Working {
		t.Errorf("no PR yet must not block or count: %q blocks %d %s", out.String(), got.GateBlocks, got.State)
	}
}

func TestGateDefersUntilTheReviewRan(t *testing.T) {
	// A takeover: the PR exists from an earlier branch, the session stops while still working (no review, no findings).
	r, s := testRunner(t, "claude-task-t-0001")
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: t.TempDir(),
		Branch: "factory/t-0001", Agent: "claude-task-t-0001", State: Working, Round: 1}
	_ = s.PutRun(run)
	for i := 0; i < 5; i++ {
		var out bytes.Buffer
		if err := r.GateHook(strings.NewReader(`{"session_id":"s-1"}`), &out); err != nil || out.Len() != 0 {
			t.Fatalf("stop %d: %q %v", i+1, out.String(), err)
		}
	}
	got, _ := s.Run("T-0001")
	if got.State != Working || got.GateBlocksTotal != 0 {
		t.Errorf("run %s blocks %d: mid-work stops are not gate attempts", got.State, got.GateBlocksTotal)
	}
}

func TestHeldRunFinishesWhenTheOwnerMerges(t *testing.T) {
	r, s := testRunner(t, "")
	state := "OPEN"
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
			return 0, `{"number":1,"state":"` + state + `","headRefOid":"a","mergedAt":"2026-11-24T19:46:00Z","mergeCommit":{"oid":"m"}}`, ""
		}
		return 0, "", ""
	}
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", State: Merging,
		PRURL: "https://github.com/zz-test/www/pull/1"}
	r.hold(run, "tier T3 is above T2; the owner merges")
	_ = s.PutRun(run)
	if run.State != Held || Active(run.State) {
		t.Fatalf("held run %s must hold no slot", run.State)
	}
	_ = r.Pass(false)
	if got, _ := s.Run("T-0001"); got.State != Held {
		t.Fatalf("an open held PR stays held, got %s", got.State)
	}
	state = "MERGED"
	_ = r.Pass(false)
	got, _ := s.Run("T-0001")
	if got.State != Merged || got.Merged != "2026-11-24T19:46:00Z" {
		t.Errorf("after the owner's merge: %s merged %q", got.State, got.Merged)
	}
	// a run failed by the older binary on the same block is picked up too
	_ = s.PutRun(&Run{Task: "T-0001", State: Failed, FailReason: "merge blocked: tier T3 is above T2; the owner merges",
		PRURL: "https://github.com/zz-test/www/pull/1"})
	_ = r.Pass(false)
	if got, _ := s.Run("T-0001"); got.State != Merged {
		t.Errorf("legacy failed hold: %s", got.State)
	}
}
