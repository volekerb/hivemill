package spawn

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/gitx"
	_ "modernc.org/sqlite"
)

const execSchema = `
CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, team_id TEXT, task_id TEXT, status TEXT NOT NULL,
  pane_id TEXT, workspace_id TEXT, tab_id TEXT, model TEXT, session_id TEXT, cwd TEXT, spawn_args TEXT, summary TEXT,
  last_nudge REAL, blocked_since REAL, blocked_alerted INTEGER DEFAULT 0, queued_at TEXT, started TEXT, ended TEXT,
  flagless INTEGER NOT NULL DEFAULT 0, flagless_since TEXT, stop_requested_at TEXT, stop_attempt_at TEXT,
  stop_failed_kind TEXT, start_pending REAL, start_pending_info TEXT, org TEXT);
CREATE TABLE events (id INTEGER PRIMARY KEY, ts TEXT, actor TEXT, entity TEXT, entity_id TEXT, action TEXT, detail TEXT);
CREATE TABLE tasks (id TEXT PRIMARY KEY, goal_id TEXT, status TEXT, attempts INTEGER DEFAULT 0, assignee TEXT, updated TEXT,
  worktree TEXT, branch TEXT);
CREATE TABLE goals (id TEXT PRIMARY KEY, status TEXT, updated TEXT);
CREATE TABLE teams (id TEXT PRIMARY KEY, lead_agent TEXT, tab_id TEXT, parked_at TEXT);
`

type fakeHost struct {
	calls    [][]string
	startRes any
	startErr string
	err      error
}

func (h *fakeHost) Herdr(check bool, args ...string) (any, error) {
	h.calls = append(h.calls, args)
	if len(args) > 1 && args[0] == "agent" && args[1] == "start" {
		return h.startRes, h.err
	}
	return map[string]any{"ok": true}, nil
}

func (h *fakeHost) StartError(args ...string) (any, string) {
	h.calls = append(h.calls, args)
	return h.startRes, h.startErr
}

func newExecDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(execSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixedNow() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.FixedZone("", 3600)) }

func startPlan(t *testing.T, dir string) SpawnPlan {
	t.Helper()
	return SpawnPlan{
		Name: "claude-pm-t-1", Org: "claude", Role: "pm", Goal: "G-1", Task: "T-1", ArgsJSON: `{"role": "pm", "name": "claude-pm-t-1"}`,
		Start: &Start{Model: "m", Runtime: "claude", Cwd: dir, SettingsPath: filepath.Join(dir, "run", "settings", "pm.json"),
			SettingsText: "{}", PromptPath: filepath.Join(dir, "run", "claude-pm-t-1.prompt.md"), PromptText: "brief",
			Kickoff: "kick", ClaudeArgs: []string{"--model", "m", "--session-id", "sid-1"},
			Env: []KV{{"FACTORY_AGENT", "claude-pm-t-1"}}, EnvBase: 1},
	}
}

func deps(db *sql.DB, h Host, out *bytes.Buffer) ExecDeps {
	return ExecDeps{DB: db, Host: h, Now: fixedNow, Epoch: func() float64 { return 5 }, Sleep: func(time.Duration) {},
		HostExe: "hc", Stdout: out, Send: func(to, subject, body, kind, goal, task string) error { return nil }}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExecRequiresPassedPlan(t *testing.T) {
	dir := t.TempDir()
	good := startPlan(t, dir)
	refused, queued, bounced, requeued, none := good, good, good, good, good
	refused.Refusal = &Refusal{Msg: "no", Code: 3}
	queued.Queue = &QueueRow{Name: "x"}
	bounced.Bounce = "stale"
	requeued.Requeue = "held"
	none.Start = nil
	for name, p := range map[string]SpawnPlan{"refused": refused, "queued": queued, "bounced": bounced, "requeued": requeued, "nostart": none} {
		db, h := newExecDB(t), &fakeHost{}
		_, err := Exec(p, deps(db, h, &bytes.Buffer{}))
		if !errors.Is(err, ErrPlanNotRunnable) {
			t.Errorf("%s: err = %v, want ErrPlanNotRunnable", name, err)
		}
		if len(h.calls) != 0 {
			t.Errorf("%s: host calls %v, want none", name, h.calls)
		}
		if n := count(t, db, "SELECT count(*) FROM agents") + count(t, db, "SELECT count(*) FROM events"); n != 0 {
			t.Errorf("%s: %d rows written", name, n)
		}
		if _, err := os.Stat(filepath.Join(dir, "run")); err == nil {
			t.Errorf("%s: files written", name)
		}
	}
}

func TestExecHCStart(t *testing.T) {
	dir := t.TempDir()
	db, h, out := newExecDB(t), &fakeHost{startRes: map[string]any{"ok": true}}, &bytes.Buffer{}
	db.Exec("INSERT INTO tasks(id,goal_id,status,attempts) VALUES('T-1','G-1','todo',0)")
	db.Exec("INSERT INTO goals(id,status) VALUES('G-1','planning')")
	dd := deps(db, h, out)
	dd.TaskGoal = "G-1"
	n, err := Exec(startPlan(t, dir), dd)
	if err != nil || n != "claude-pm-t-1" {
		t.Fatalf("Exec = %q, %v", n, err)
	}
	var status, pane, sid, spawnArgs, org string
	if err := db.QueryRow("SELECT status,pane_id,session_id,spawn_args,org FROM agents").Scan(&status, &pane, &sid, &spawnArgs, &org); err != nil {
		t.Fatal(err)
	}
	if status != "live" || pane != "claude-pm-t-1" || sid != "sid-1" || org != "claude" {
		t.Errorf("row = %s %s %s %s", status, pane, sid, org)
	}
	if want := `{"role": "pm", "name": "claude-pm-t-1", "host_backend": "hc", "runtime": "claude"}`; spawnArgs != want {
		t.Errorf("spawn_args = %s\nwant %s", spawnArgs, want)
	}
	var ts, st string
	db.QueryRow("SELECT status FROM tasks").Scan(&st)
	db.QueryRow("SELECT status FROM goals").Scan(&ts)
	if st != "in_progress" || ts != "active" {
		t.Errorf("task %s goal %s", st, ts)
	}
	rows, _ := db.Query("SELECT action, detail FROM events ORDER BY id")
	var got []string
	for rows.Next() {
		var a, d string
		rows.Scan(&a, &d)
		got = append(got, a+"|"+d)
	}
	want := []string{"task-auto-start|spawn claude-pm-t-1", "goal-auto-active|task T-1 in_progress (spawn claude-pm-t-1)",
		`spawn|{"pane": "claude-pm-t-1", "model": "m", "cwd": "` + dir + `", "live": 1}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.HasPrefix(out.String(), "SPAWNED claude-pm-t-1 (pm, m) in claude-pm-t-1 cwd=") {
		t.Errorf("stdout %q", out.String())
	}
	if len(h.calls) != 2 || h.calls[0][1] != "start" || h.calls[1][1] != "prompt" {
		t.Errorf("host calls %v", h.calls)
	}
	if b, err := os.ReadFile(startPlan(t, dir).Start.PromptPath); err != nil || string(b) != "brief" {
		t.Errorf("prompt file %q %v", b, err)
	}
}

// TestPaneStartFailure: a start that fails leaves exactly what Python leaves, and a claimed queue row goes back to
// 'queued' instead of staying 'starting'.
func TestPaneStartFailure(t *testing.T) {
	dir := t.TempDir()
	// start returns None (agent_not_ready, a timeout, a missing binary): Python keeps the live row and records start-uncertain
	db, h := newExecDB(t), &fakeHost{}
	if _, err := Exec(startPlan(t, dir), deps(db, h, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	var ev string
	db.QueryRow("SELECT detail FROM events WHERE action='start-uncertain'").Scan(&ev)
	if ev != "claude-pm-t-1" {
		t.Errorf("start-uncertain detail %q", ev)
	}
	// an error before the row exists (the worktree): a claimed row goes back to 'queued' (spawn()'s except), never 'starting'
	db = newExecDB(t)
	db.Exec("INSERT INTO agents(name,role,status,spawn_args) VALUES('claude-pm-t-1','pm','starting','{\"claimed_at\": 1}')")
	d := deps(db, &fakeHost{}, &bytes.Buffer{})
	d.Claimed = true
	d.Worktree = func() (string, error) { return "", errors.New("worktree add failed: boom") }
	pl := startPlan(t, dir)
	pl.Start.NeedWorktree = true
	if _, err := Exec(pl, d); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	var st, args string
	db.QueryRow("SELECT status, spawn_args FROM agents").Scan(&st, &args)
	if st != "queued" || strings.Contains(args, "claimed_at") || count(t, db, "SELECT count(*) FROM events") != 0 {
		t.Errorf("claimed row after failure: %s %s", st, args)
	}
	// async: a real start error is start-failed, a pending one sets start_pending; neither stays unexplained
	db, h = newExecDB(t), &fakeHost{startErr: "no such pane"}
	d = deps(db, h, &bytes.Buffer{})
	d.AsyncStart = true
	if _, err := Exec(startPlan(t, dir), d); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT detail FROM events WHERE action='start-failed'").Scan(&ev)
	if ev != `{"pane": "claude-pm-t-1", "error": "no such pane"}` {
		t.Errorf("start-failed detail %q", ev)
	}
	// the rendered start timeout of async start is the shortest herdr accepts
	if got := h.calls[0][7]; got != "3001" {
		t.Errorf("async timeout %s", got)
	}
	// herdr host without a placeable pane: nothing is touched
	db, h = newExecDB(t), &fakeHost{}
	d = deps(db, h, &bytes.Buffer{})
	d.HostExe = "herdr"
	if _, err := Exec(startPlan(t, dir), d); !errors.Is(err, ErrNotPorted) || len(h.calls) != 0 || count(t, db, "SELECT count(*) FROM agents") != 0 {
		t.Errorf("herdr new pane: err=%v calls=%v", err, h.calls)
	}
}

func TestHerdrExistingPane(t *testing.T) {
	dir := t.TempDir()
	db, h := newExecDB(t), &fakeHost{startRes: map[string]any{}}
	d := deps(db, h, &bytes.Buffer{})
	d.HostExe, d.Pane = "herdr", "p-1"
	d.FindPane = func(p string) (string, string, bool) { return "w1", "t1", p == "p-1" }
	pl := startPlan(t, dir)
	pl.Start.Env = []KV{{"A", "1"}, {"FROM_SETTINGS", "x y"}}
	pl.Start.EnvBase = 1
	if _, err := Exec(pl, d); err != nil {
		t.Fatal(err)
	}
	if got := h.calls[0]; got[0] != "pane" || got[1] != "run" || !strings.HasPrefix(got[3], "export A=1 && hash -r && cd ") || strings.Contains(got[3], "FROM_SETTINGS") {
		t.Errorf("export call %v", got)
	}
	var ws, tab string
	db.QueryRow("SELECT workspace_id, tab_id FROM agents").Scan(&ws, &tab)
	if ws != "w1" || tab != "t1" {
		t.Errorf("ws/tab %s %s", ws, tab)
	}
}

// TestSandboxSpawn: --sandbox, a sandbox_required task and the gh credential are never started by Go; the whole spawn
// goes to Python (the sandbox entry is workerbox.prepare_agent, in-process Python) before any file, row or host call.
func TestSandboxSpawn(t *testing.T) {
	dir := t.TempDir()
	for name, mod := range map[string]func(*Start){
		"sandboxed": func(s *Start) { s.Sandboxed = true },
		"ghcred":    func(s *Start) { s.GhCred = true },
		"cursor":    func(s *Start) { s.Runtime = "cursor" },
		"evaluator": func(s *Start) { s.EvaluatorSettings = true },
	} {
		p := startPlan(t, dir)
		mod(p.Start)
		db, h := newExecDB(t), &fakeHost{}
		if _, err := Exec(p, deps(db, h, &bytes.Buffer{})); !errors.Is(err, ErrNotPorted) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(h.calls) != 0 || count(t, db, "SELECT count(*) FROM agents") != 0 {
			t.Errorf("%s: side effects", name)
		}
	}
}

func TestApplyQueue(t *testing.T) {
	db, out := newExecDB(t), &bytes.Buffer{}
	p := SpawnPlan{Queue: &QueueRow{Name: "a", Role: "coder", Goal: "G-1", Task: "T-1", Reason: "load", ArgsJSON: "{}", Org: "claude", Message: "QUEUED a: load."}}
	ok, err := ApplyQueue(p, false, deps(db, &fakeHost{}, out))
	if err != nil || !ok || out.String() != "QUEUED a: load.\n" {
		t.Fatalf("%v %v %q", ok, err, out.String())
	}
	db.Exec("UPDATE agents SET status='cancelled' WHERE name='a'")
	ok, err = ApplyQueue(p, true, deps(db, &fakeHost{}, &bytes.Buffer{}))
	var st string
	db.QueryRow("SELECT status FROM agents").Scan(&st)
	if err != nil || ok || st != "cancelled" {
		t.Errorf("from_queue over a cancelled row: %v %v %s", ok, err, st)
	}
}

func TestHCSpawnScript(t *testing.T) {
	env := []KV{{"FACTORY_ROLE", "coder"}, {"GH_TOKEN", "x"}, {"HERDR_SOCKET_PATH", "/live"}, {"B", "it's"}, {"A", "1"}, {"PATH", "/p"}}
	osEnv := func(k string) (string, bool) {
		v, ok := map[string]string{"LANG": "en_US", "TERM": ""}[k]
		return v, ok
	}
	got := HCSpawnScriptBody(env, "claude", []string{"--model", "m", ""}, osEnv, func(s string) bool { return s == "/live" })
	want := "#!/bin/sh\nexec env -i PATH=/p LANG=en_US TERM= A=1 'B=it'\"'\"'s' FACTORY_ROLE=coder claude --model m ''\n"
	if got != want {
		t.Errorf("script:\n%q\nwant\n%q", got, want)
	}
	dir := t.TempDir()
	tail, err := HCRunArgvTail("", dir, "n", env, "claude", []string{"x"}, osEnv, nil)
	if err != nil || len(tail) != 2 || tail[0] != "sh" || tail[1] != filepath.Join(dir, "n.hc-spawn.sh") {
		t.Fatalf("tail %v %v", tail, err)
	}
	if fi, err := os.Stat(tail[1]); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v %v", fi, err)
	}
	if tail, _ := HCRunArgvTail("", dir, "n", nil, "claude", []string{"x"}, osEnv, nil); strings.Join(tail, " ") != "claude x" {
		t.Errorf("no env: %v", tail)
	}
	if _, err := HCSpawnScriptPath("", "/proc/nope/x", "n"); err == nil {
		t.Error("an unwritable run dir is an error")
	}
}

// TestUnicodeShellQuote: shlex.quote uses re.ASCII, so NBSP, \v and U+0663 are quoted, like Python's output
// ('a\xa0b', 'x٣', "'a\vb'").
func TestUnicodeShellQuote(t *testing.T) {
	for _, s := range []string{"a b", "a\vb", "x٣"} {
		if got := shellQuote(s); got != "'"+s+"'" {
			t.Errorf("shellQuote(%q) = %q", s, got)
		}
	}
	if shellQuote("") != "''" || shellQuote("a/b-c.d") != "a/b-c.d" {
		t.Error("plain cases")
	}
}

func TestAddRuntimeArgs(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1}`:                          `{"a": 1, "host_backend": "hc", "runtime": "claude"}`,
		`{"host_backend": "herdr", "b": 2}`: `{"host_backend": "hc", "b": 2, "runtime": "claude"}`,
		`{"runtime": "claude"}`:             `{"runtime": "claude", "host_backend": "hc"}`,
		`{}`:                                `{"host_backend": "hc", "runtime": "claude"}`,
	} {
		if got := addRuntimeArgs(in, true, "hc"); got != want {
			t.Errorf("addRuntimeArgs(%s) = %s, want %s", in, got, want)
		}
	}
}

func FuzzAddRuntimeArgs(f *testing.F) {
	for _, s := range []string{`{}`, `{"a": 1}`, `{"host_backend": "herdr"}`, `{"host_backend": "`, ``, `}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _ = addRuntimeArgs(s, false, "herdr") })
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@users.noreply.github.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@users.noreply.github.com")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func wtRepo(t *testing.T) (repo, wts string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	repo = filepath.Join(root, "repo")
	wts = filepath.Join(root, "worktrees")
	gitT(t, root, "init", "-q", "-b", "main", origin)
	os.WriteFile(filepath.Join(origin, "f"), []byte("x"), 0o644)
	gitT(t, origin, "add", "f")
	gitT(t, origin, "commit", "-q", "-m", "init")
	gitT(t, root, "clone", "-q", origin, repo)
	return repo, wts
}

// TestWorktreeBranch: a coder's worktree is on factory/<task>, never main; an existing branch is reused (-B) and
// kept when the half-made worktree is dropped.
func TestWorktreeBranch(t *testing.T) {
	repo, wts := wtRepo(t)
	var recorded [][2]string
	d := WorktreeDeps{Git: gitx.Options{Env: append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")},
		ConfigGuard: func(string) []string { return nil }, PrePushHook: func(string) error { return nil },
		Record: func(w, b string) error { recorded = append(recorded, [2]string{w, b}); return nil }}
	s := WorktreeSpec{Task: "T-9", Goal: "G-1", Role: "coder", Repo: repo, Base: "main", WorktreesDir: wts}
	wt, err := PrepareWorktree(s, d)
	if err != nil {
		t.Fatal(err)
	}
	if wt != wts+"/G-1-T-9" || gitT(t, wt, "rev-parse", "--abbrev-ref", "HEAD") != "factory/t-9" {
		t.Errorf("worktree %s on %s", wt, gitT(t, wt, "rev-parse", "--abbrev-ref", "HEAD"))
	}
	if len(recorded) != 1 || recorded[0] != [2]string{wt, "factory/t-9"} {
		t.Errorf("recorded %v", recorded)
	}
	// an existing worktree is reused: a coder is switched to its recorded branch, the guard runs first
	s.Worktree, s.Branch = wt, "factory/t-9"
	guarded := 0
	d.ConfigGuard = func(string) []string { guarded++; return nil }
	if got, err := PrepareWorktree(s, d); err != nil || got != wt || guarded != 1 {
		t.Errorf("reuse: %s %v guard=%d", got, err, guarded)
	}
	// an existing branch is reused when a new worktree is made for it
	gitT(t, repo, "branch", "factory/t-10", "main")
	s2 := WorktreeSpec{Task: "T-10", Goal: "G-1", Role: "coder", Repo: repo, Base: "main", WorktreesDir: wts}
	wt2, err := PrepareWorktree(s2, d)
	if err != nil || gitT(t, wt2, "rev-parse", "--abbrev-ref", "HEAD") != "factory/t-10" {
		t.Fatalf("existing branch: %s %v", wt2, err)
	}
	// never main: a recorded branch that is the base is refused before anything is created
	for _, b := range []string{"main", "master"} {
		s3 := WorktreeSpec{Task: "T-11", Goal: "G-1", Role: "coder", Repo: repo, Base: "main", Branch: b, WorktreesDir: wts}
		_, err := PrepareWorktree(s3, d)
		var r *WorktreeRefusal
		if !errors.As(err, &r) || r.Code != 4 {
			t.Errorf("branch %s: err = %v", b, err)
		}
		if _, err := os.Stat(wts + "/G-1-T-11"); err == nil {
			t.Errorf("branch %s: a worktree was created", b)
		}
	}
	// a verifier without a worktree creates none; no repo means none
	if got, err := PrepareWorktree(WorktreeSpec{Task: "T-12", Role: "qa", Repo: repo, WorktreesDir: wts}, d); got != "" || err != nil {
		t.Errorf("qa: %q %v", got, err)
	}
	if got, err := PrepareWorktree(WorktreeSpec{Task: "T-12", Role: "coder"}, d); got != "" || err != nil {
		t.Errorf("no repo: %q %v", got, err)
	}
	// a config guard hit drops the half-made worktree and keeps nothing
	d.ConfigGuard = func(r string) []string {
		if strings.HasPrefix(r, wts) {
			return []string{"core.fsmonitor"}
		}
		return nil
	}
	s4 := WorktreeSpec{Task: "T-13", Goal: "G-1", Role: "coder", Repo: repo, Base: "main", WorktreesDir: wts}
	_, err = PrepareWorktree(s4, d)
	var r *WorktreeRefusal
	if !errors.As(err, &r) || r.Code != 4 || !strings.Contains(r.Msg, "core.fsmonitor") {
		t.Fatalf("guard: %v", err)
	}
	if _, err := os.Stat(wts + "/G-1-T-13"); err == nil {
		t.Error("guarded worktree left behind")
	}
	if out := gitT(t, repo, "branch", "--list", "factory/t-13"); out != "" {
		t.Errorf("new branch left behind: %s", out)
	}
	// without the injected guards nothing runs
	if _, err := PrepareWorktree(s4, WorktreeDeps{}); !errors.Is(err, ErrNotPorted) {
		t.Errorf("no guards: %v", err)
	}
	// a task_clones factory is Python's
	d.ConfigGuard = func(string) []string { return nil }
	s5 := s4
	s5.TaskClones, s5.Task = true, "T-14"
	if _, err := PrepareWorktree(s5, d); !errors.Is(err, ErrNotPorted) {
		t.Errorf("task clones: %v", err)
	}
}

// TestExecReviewFindings: the flagless marker of the old row, an aborted start, the task's own goal and the
// trust step of a task worktree.
func TestExecReviewFindings(t *testing.T) {
	dir := t.TempDir()
	// flagless old row: cleared after a successful sync start, with the event
	db, h := newExecDB(t), &fakeHost{startRes: map[string]any{}}
	db.Exec("INSERT INTO agents(name,role,status,flagless) VALUES('claude-pm-t-1','pm','exited',1)")
	d := deps(db, h, &bytes.Buffer{})
	d.Existing = &ExistingRow{SessionID: "old", Flagless: true}
	if _, err := Exec(startPlan(t, dir), d); err != nil {
		t.Fatal(err)
	}
	var fl int
	db.QueryRow("SELECT flagless FROM agents").Scan(&fl)
	if fl != 0 || count(t, db, "SELECT count(*) FROM events WHERE action='flagless-cleared'") != 1 {
		t.Errorf("flagless %d", fl)
	}
	// CheckStart error: nothing after the start call is written
	db, h = newExecDB(t), &fakeHost{startRes: map[string]any{}}
	d = deps(db, h, &bytes.Buffer{})
	d.CheckStart = func() error { return errors.New("script boom") }
	var out bytes.Buffer
	d.Stdout = &out
	if _, err := Exec(startPlan(t, dir), d); err == nil || !strings.Contains(err.Error(), "script boom") {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 || count(t, db, "SELECT count(*) FROM events WHERE action IN ('spawn','start-uncertain')") != 0 {
		t.Errorf("output or events after an aborted start: %q", out.String())
	}
	// goal auto-active follows the task's goal, not --goal
	db, h = newExecDB(t), &fakeHost{startRes: map[string]any{}}
	db.Exec("INSERT INTO tasks(id,goal_id,status) VALUES('T-1','G-1','todo')")
	db.Exec("INSERT INTO goals(id,status) VALUES('G-1','planning'),('G-2','planning')")
	d = deps(db, h, &bytes.Buffer{})
	d.TaskGoal = "G-1"
	p := startPlan(t, dir)
	p.Goal = "G-2"
	if _, err := Exec(p, d); err != nil {
		t.Fatal(err)
	}
	var g1, g2 string
	db.QueryRow("SELECT status FROM goals WHERE id='G-1'").Scan(&g1)
	db.QueryRow("SELECT status FROM goals WHERE id='G-2'").Scan(&g2)
	if g1 != "active" || g2 != "planning" {
		t.Errorf("goals %s %s", g1, g2)
	}
	// a qa/coder/reviewer with a task needs the trust step: without it nothing runs
	for _, role := range []string{"qa", "coder", "reviewer"} {
		db, h = newExecDB(t), &fakeHost{}
		p = startPlan(t, dir)
		p.Role = role
		if _, err := Exec(p, deps(db, h, &bytes.Buffer{})); !errors.Is(err, ErrNotPorted) || len(h.calls) != 0 || count(t, db, "SELECT count(*) FROM agents") != 0 {
			t.Errorf("%s: err=%v calls=%v", role, err, h.calls)
		}
	}
	// missing hooks are an error before any write
	db = newExecDB(t)
	d = deps(db, &fakeHost{}, &bytes.Buffer{})
	d.Sleep = nil
	if _, err := Exec(startPlan(t, dir), d); err == nil || errors.Is(err, ErrNotPorted) || count(t, db, "SELECT count(*) FROM agents") != 0 {
		t.Errorf("nil Sleep: %v", err)
	}
}
