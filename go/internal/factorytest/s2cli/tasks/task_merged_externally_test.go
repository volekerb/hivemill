package s2tasks

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_merged_externally.py (16 of 16 cases). Complete.
// `task update --merged-externally` is handed to Python by the Go CLI, so every case NeedPython and skips without it.
// The gh stub is the scripted Go stub: the Python harness seeds a state file (seed_pr, st["api"], st["files_fail"]),
// here each of those is one scripted rule with the same answer.

const meSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func meEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.NeedPython()
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTask(ft.Task{ID: "T-0001", Status: "in_progress"})
	seedTask(e, ft.Task{ID: "T-0002", Status: "todo"}, true)
	exec(e, "UPDATE tasks SET depends_on=? WHERE id='T-0002'", `["T-0001"]`)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	return e
}

// meMerged scripts the PR as the Python merged() helper does: state, mergeCommit, the compare answer; extra is merged
// into the pr dict and before are rules that must win over the compare rule.
func meMerged(e *ft.Env, state, sha, status string, extra map[string]any, before ...ft.StubRule) {
	e.T.Helper()
	pr := map[string]any{"mergeCommit": nil}
	if sha != "" {
		pr["mergeCommit"] = map[string]any{"oid": sha}
	}
	for k, v := range extra {
		pr[k] = v
	}
	rules := append([]ft.StubRule{}, before...)
	// the Python stub answers `repo view` with this url when no repo_url is set (an OPEN PR reads it for the squash check)
	rules = append(rules, ft.StubRule{Match: []string{"repo view"}, Out: `{"url": "/nonexistent/zz-test-remote.git"}` + "\n"})
	rules = append(rules, ft.StubRule{Match: []string{"compare"}, Out: `{"status": "` + status + `"}`})
	seedPR(e, prURL, state, nil, pr, rules...)
}

func meClose(e *ft.Env, agent string) ft.Result {
	return run(e, agent, "task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "owner merged by hand")
}

func meStatus(e *ft.Env) string { return val(e, "SELECT status FROM tasks WHERE id='T-0001'") }

func TestMergedExtDirectorClosesMergedPRAndDependantsBecomeReady(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	notContains(t, "task ready", ok(e, "", "task", "ready").Stdout, "T-0002")
	p := meClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 0)
	eq(t, "status", meStatus(e), "done")
	contains(t, "task ready", ok(e, "", "task", "ready").Stdout, "T-0002")
	hist := ok(e, "", "task", "show", "T-0001").Stdout
	for _, want := range []string{"zz-test-director", "merged-externally", prURL, meSHA, "owner merged by hand"} {
		contains(t, "history", hist, want)
	}
}

func TestMergedExtOwnerMayClose(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	eq(t, "exit", run(e, "", "task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "x").Exit, 0)
	eq(t, "status", meStatus(e), "done")
}

func TestMergedExtNotMergedRefused(t *testing.T) {
	e := meEnv(t)
	for _, state := range []string{"OPEN", "CLOSED"} {
		meMerged(e, state, "", "identical", nil)
		p := meClose(e, "zz-test-director")
		eq(t, state+" exit", p.Exit, 3)
		contains(t, "stderr", p.Stderr, "not MERGED")
		eq(t, "status", meStatus(e), "in_progress")
	}
}

func TestMergedExtMergeCommitNotOnBaseRefused(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "diverged", nil)
	p := meClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "not on main")
	eq(t, "status", meStatus(e), "in_progress")
}

func TestMergedExtGhFailureRefused(t *testing.T) {
	e := meEnv(t)
	seedPR(e, prURL, "MERGED", nil, map[string]any{"mergeCommit": map[string]any{"oid": meSHA}},
		ft.StubRule{Match: []string{"compare"}, Exit: 1, Err: "boom\n"})
	eq(t, "exit", meClose(e, "zz-test-director").Exit, 3)
	eq(t, "status", meStatus(e), "in_progress")
}

func TestMergedExtWrongRoleRefused(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	p := meClose(e, "zz-test-coder")
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "only the director or the owner")
	eq(t, "status", meStatus(e), "in_progress")
}

func TestMergedExtWorkerClaimingDirectorRefused(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	p := runO(e, ft.Opts{Agent: "zz-test-director", Env: map[string]string{"FACTORY_ROLE": "coder"}},
		"task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "x")
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "only the director or the owner")
	eq(t, "status", meStatus(e), "in_progress")
}

func TestMergedExtRunsAfterMergeLikeAJudgeMerge(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", map[string]any{"files": []any{map[string]any{"path": "roles/lead.md"}}})
	exec(e, "INSERT INTO lessons(id,title,roles,rule,created,updated) VALUES('L-001','x','all','r','t','t')")
	exec(e, "INSERT INTO lesson_links(lesson_id,task_id,created) VALUES('L-001','T-0001','t')")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001"})
	p := meClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 0)
	if v := val(e, "SELECT COALESCE(merged_at,'') FROM lesson_links"); v == "" {
		t.Fatal("lesson_links.merged_at is empty after the close")
	}
	eq(t, "lesson status", val(e, "SELECT status FROM lessons"), "applied")
	if len(sql(e, "SELECT * FROM events WHERE action='roles-changed'")) == 0 {
		t.Fatal("no roles-changed event")
	}
}

func TestMergedExtFailingHookDoesNotUndoClose(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil,
		ft.StubRule{Match: []string{"pr", "view", "files"}, Exit: 1, Err: "gh: files unavailable\n"})
	p := meClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 0)
	eq(t, "status", meStatus(e), "done")
	if len(sql(e, "SELECT * FROM events WHERE action='post-merge-hook-failed'")) == 0 {
		t.Fatal("no post-merge-hook-failed event")
	}
}

func TestMergedExtOtherFlagsRefused(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	p := run(e, "zz-test-director", "task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "x", "--priority", "1")
	eq(t, "exit", p.Exit, 2)
	eq(t, "status", meStatus(e), "in_progress")
}

func TestMergedExtAlreadyDoneAndNoPRRefused(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0001'")
	eq(t, "already done exit", meClose(e, "zz-test-director").Exit, 2)
	p := run(e, "zz-test-director", "task", "update", "T-0002", "--status", "done", "--merged-externally", "--reason", "x")
	eq(t, "no PR exit", p.Exit, 2)
	contains(t, "stderr", p.Stderr, "not a coder task with a PR")
}

func TestMergedExtNeedsReason(t *testing.T) {
	e := meEnv(t)
	meMerged(e, "MERGED", meSHA, "identical", nil)
	p := run(e, "zz-test-director", "task", "update", "T-0001", "--status", "done", "--merged-externally")
	eq(t, "exit", p.Exit, 2)
	eq(t, "status", meStatus(e), "in_progress")
}

// ---- MergedExternallyOpenSquashTests: OPEN PR + matching GitHub squash on origin/main (T-1399) ----------------------------

var meNoreply = []string{"GIT_COMMITTER_EMAIL=noreply@github.com", "GIT_COMMITTER_NAME=GitHub",
	"GIT_AUTHOR_EMAIL=zz-test@example.invalid", "GIT_AUTHOR_NAME=zz-test"}

type meSquash struct {
	e      *ft.Env
	wt     string
	remote string
}

func meGit(t testing.TB, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=zz-test", "GIT_AUTHOR_EMAIL=zz-test@example.invalid",
		"GIT_COMMITTER_NAME=zz-test", "GIT_COMMITTER_EMAIL=zz-test@example.invalid"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// script (re)writes the gh answers: the PR (OPEN, mergeCommit null, head = the local bare remote's PR head), repo view
// and api .../commits/main with the remote's main as of now (the Python stub reads it live; the Go stub is scripted).
func (s *meSquash) script() {
	s.e.T.Helper()
	main := meGit(s.e.T, s.remote, nil, "rev-parse", "refs/heads/main")
	seedPR(s.e, prURL, "OPEN", nil, map[string]any{"mergeCommit": nil, "headRefOid": s.e.HeadSHA},
		ft.StubRule{Match: []string{"repo view"}, Out: `{"url": "` + s.remote + `"}` + "\n"},
		ft.StubRule{Match: []string{"api", "/commits/main "}, Out: main + "\n"})
}

func meSquashEnv(t *testing.T) *meSquash {
	e := ft.New(t)
	e.NeedPython()
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTask(ft.Task{ID: "T-0001", Status: "in_progress"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	e.UseGitHead()
	s := &meSquash{e: e, wt: filepath.Join(e.Root, "head-wt"), remote: filepath.Join(e.Root, "head-origin.git")}
	meGit(t, e.Home, nil, "remote", "add", "origin", s.remote)
	s.script()
	return s
}

func (s *meSquash) squashOnMain(t *testing.T, msg string, mergeTree bool) string {
	t.Helper()
	meGit(t, s.wt, nil, "fetch", "-q", "origin", "main")
	head := meGit(t, s.wt, nil, "rev-parse", "HEAD")
	parent := meGit(t, s.wt, nil, "rev-parse", "origin/main")
	var sha string
	if mergeTree {
		tree := meGit(t, s.wt, nil, "merge-tree", "--write-tree", parent, head)
		sha = meGit(t, s.wt, meNoreply, "commit-tree", tree, "-p", parent, "-m", msg)
	} else {
		sha = meGit(t, s.wt, meNoreply, "commit-tree", head+"^{tree}", "-p", parent, "-m", msg)
	}
	meGit(t, s.wt, nil, "push", "-q", "origin", sha+":refs/heads/main")
	s.script()
	return sha
}

func (s *meSquash) close() ft.Result {
	return run(s.e, "zz-test-director", "task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "x")
}

func TestMergedExtOpenPRMatchingMergeTreeSquashCloses(t *testing.T) {
	s := meSquashEnv(t)
	main := s.squashOnMain(t, "feature (#1)", true)
	p := run(s.e, "zz-test-director", "task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "GitHub 502")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s\ncalls: %v", p.Exit, p.Stderr, s.e.CallLines())
	}
	eq(t, "status", val(s.e, "SELECT status FROM tasks WHERE id='T-0001'"), "done")
	hist := ok(s.e, "", "task", "show", "T-0001").Stdout
	contains(t, "history", hist, main)
	contains(t, "history", hist, "merged-externally")
}

func TestMergedExtOpenPRWrongTreeRefused(t *testing.T) {
	s := meSquashEnv(t)
	meGit(t, s.wt, nil, "fetch", "-q", "origin", "main")
	meGit(t, s.wt, nil, "checkout", "-q", "--detach", "origin/main")
	if err := os.WriteFile(filepath.Join(s.wt, "wrong-tree.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meGit(t, s.wt, nil, "add", "wrong-tree.txt")
	meGit(t, s.wt, meNoreply, "commit", "-q", "-m", "feature (#1)")
	sha := meGit(t, s.wt, nil, "rev-parse", "HEAD")
	meGit(t, s.wt, nil, "push", "-q", "origin", sha+":refs/heads/main")
	s.script()
	p := s.close()
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "not MERGED")
}

func TestMergedExtOpenPRSubjectWithoutPRNumberRefused(t *testing.T) {
	s := meSquashEnv(t)
	s.squashOnMain(t, "feature (no number)", true)
	p := s.close()
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "not MERGED")
}

func TestMergedExtOpenPRSquashBeyondTwentyFirstParentCommitsRefused(t *testing.T) {
	s := meSquashEnv(t)
	s.squashOnMain(t, "feature (#1)", true)
	meGit(t, s.wt, nil, "fetch", "-q", "origin", "main")
	meGit(t, s.wt, nil, "checkout", "-q", "-B", "main", "origin/main")
	for i := 0; i < 21; i++ {
		meGit(t, s.wt, meNoreply, "commit", "-q", "--allow-empty", "-m", "pad "+strconv.Itoa(i))
		meGit(t, s.wt, nil, "push", "-q", "origin", "HEAD:main")
	}
	s.script()
	p := s.close()
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "not MERGED")
}
