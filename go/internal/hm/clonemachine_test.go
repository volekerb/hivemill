package hm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRemote is a clone-mode machine that is a directory of this one: the fake ssh runs the command here with that
// directory as $HOME, so the scripts and git are real. stub writes a program into its ~/.local/bin.
type fakeRemote struct {
	t    *testing.T
	home string
	log  []string
	env  []string // the machine's own extra environment
}

func newFakeRemote(t *testing.T, r *Runner) *fakeRemote {
	m := &fakeRemote{t: t, home: t.TempDir()}
	r.SSH = func(machine, stdin string, timeout time.Duration, command string) (int, string, string) {
		if machine != "w1" {
			return 255, "", "no such host"
		}
		m.log = append(m.log, command)
		env := []string{"HOME=" + m.home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=zz", "GIT_AUTHOR_EMAIL=zz@users.noreply.github.com", "GIT_COMMITTER_NAME=zz",
			"GIT_COMMITTER_EMAIL=zz@users.noreply.github.com"}
		return run(timeout, m.home, stdin, append(env, m.env...), "sh", "-c", command)
	}
	return m
}

func (m *fakeRemote) put(rel, text string, mode os.FileMode) string {
	p := filepath.Join(m.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := writeFile(p, text, mode); err != nil {
		m.t.Fatal(err)
	}
	return p
}

func (m *fakeRemote) stub(name, body string) {
	m.put(".local/bin/"+name, "#!/bin/sh\n"+body+"\n", 0o755)
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"git", "-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "user.name=zz",
		"-c", "user.email=zz@users.noreply.github.com"}, args...)
	rc, out, errs := run(time.Minute, "", "", nil, argv...)
	if rc != 0 {
		t.Fatalf("git %v: %s", args, errs)
	}
	return strings.TrimSpace(out)
}

// cloneRunner is a runner with a real origin repository, a project clone of it, and the clone-mode machine w1.
func cloneRunner(t *testing.T) (*Runner, *SQLite, *fakeRemote, *[]string) {
	t.Helper()
	r, s := testRunner(t, "")
	withLocalMem(t, 8000)
	root := t.TempDir()
	origin, seed, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "seed"), filepath.Join(root, "repo")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	mustGit(t, root, "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "index.html"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", "index.html")
	mustGit(t, seed, "commit", "-qm", "seed")
	mustGit(t, seed, "push", "-q", "origin", "HEAD:main")
	mustGit(t, root, "clone", "-q", origin, repo)

	for _, q := range []string{`UPDATE projects SET repo_path='` + repo + `'`,
		`UPDATE tasks SET status='todo', acceptance='1. marker - cmd:test -f marker.txt => exit 0'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"projects": "www", "session_machines": "w1", "machine_w1_mode": "clone",
		"machine_mem_floor_mb": "0"} {
		if err := s.SetCfg(k, v); err != nil {
			t.Fatal(err)
		}
	}
	_ = writeFile(filepath.Join(r.Env.UserHome, ".claude.json"), "{}", 0o600)
	_ = os.MkdirAll(filepath.Join(r.Env.UserHome, ".claude"), 0o700)
	_ = writeFile(filepath.Join(r.Env.UserHome, ".claude", "usage-limits.json"),
		`{"five_hour_pct": "1", "seven_day_pct": "1", "updated_at": `+strconv.FormatInt(time.Now().Unix(), 10)+`}`, 0o600)

	r.Tools.Git = func(dir string, args ...string) (int, string, string) {
		rc, out, errs := run(time.Minute, "", "", nil, append([]string{"git", "-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
		return rc, strings.TrimSpace(out), strings.TrimSpace(errs)
	}
	var hc []string
	r.Tools.HC = func(args ...string) (int, string, string) {
		hc = append(hc, strings.Join(args, " "))
		switch args[0] {
		case "server":
			return 0, `{"core": {"name": "coord-core"}}`, ""
		case "ls":
			return 0, `{"machines": [{"name": "w1", "state": "connected"}], "terminals": []}`, ""
		}
		return 0, "{}", ""
	}

	m := newFakeRemote(t, r)
	m.stub("hivemill", "exit 0")
	m.put(".config/hivemill/coordinator", "coord-core\nclone\n"+r.Env.Home+"\n", 0o600)
	return r, s, m, &hc
}

func TestCloneMachineWorktree(t *testing.T) {
	r, s, m, hc := cloneRunner(t)
	if err := r.Start("T-0001"); err != nil {
		t.Fatal(err)
	}
	run1, err := s.Run("T-0001")
	if err != nil || run1 == nil {
		t.Fatal(run1, err)
	}
	base := filepath.Join(m.home, "hivemill")
	wt := filepath.Join(base, "worktrees", "G-001-T-0001")
	if run1.Machine != "w1" || run1.Worktree != wt || run1.RemoteHome != m.home ||
		run1.Mirror != filepath.Join(r.Env.Home, "worktrees", "G-001-T-0001") {
		t.Fatalf("run placed as %q in %q (mirror %q)", run1.Machine, run1.Worktree, run1.Mirror)
	}
	if got := mustGit(t, wt, "branch", "--show-current"); got != "factory/t-0001" {
		t.Errorf("branch %q", got)
	}
	alt, err := os.ReadFile(filepath.Join(wt, ".git", "objects", "info", "alternates"))
	if err != nil || !strings.Contains(string(alt), filepath.Join(base, "cache")) {
		t.Errorf("the clone must reference the machine's cache: %q %v", alt, err)
	}
	for _, f := range []string{filepath.Join(wt, ".git", "hooks", "pre-push"), filepath.Join(base, "run", "guard-export.json"),
		filepath.Join(base, "state", "hivemill", "settings", "task.json"),
		filepath.Join(base, "state", "hivemill", "spawn", run1.Agent+".sh")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("not written on the machine: %v", err)
		}
	}
	settings, _ := os.ReadFile(filepath.Join(base, "state", "hivemill", "settings", "task.json"))
	if !strings.Contains(string(settings), "hivemill-guard") || strings.Contains(string(settings), "roles/hooks/worker-guard") {
		t.Error("the remote settings must run that machine's `hivemill guard`")
	}
	started := ""
	for _, c := range *hc {
		if strings.HasPrefix(c, "run ") {
			started = c
		}
	}
	if !strings.Contains(started, "--cwd "+wt) || !strings.Contains(started, "--on w1") {
		t.Errorf("session start: %q", started)
	}

	// an ended run with unpushed work keeps its worktree; a pushed one loses it
	if err := os.WriteFile(filepath.Join(wt, "marker.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wt, "add", "marker.txt")
	mustGit(t, wt, "commit", "-qm", "work")
	r.removeRemoteWorktrees() // still active
	run1.State = Failed
	_ = s.PutRun(run1)
	r.removeRemoteWorktrees()
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("a worktree with unpushed work must stay")
	}
	mustGit(t, wt, "push", "-q", "origin", "HEAD:factory/t-0001")
	run1.RemoteDone = false
	_ = s.PutRun(run1)
	r.removeRemoteWorktrees()
	if _, err := os.Stat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the pushed worktree must be removed: %v", err)
	}
	if got, _ := s.Run("T-0001"); !got.RemoteDone {
		t.Error("the run must record the removal")
	}
}

func TestCloneMachineGateReadsRemote(t *testing.T) {
	r, s, _, _ := cloneRunner(t)
	if err := r.Start("T-0001"); err != nil {
		t.Fatal(err)
	}
	run1, _ := s.Run("T-0001")
	wt := run1.Worktree
	// the session's work and its .hivemill files exist on the machine only
	if err := os.WriteFile(filepath.Join(wt, "marker.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wt, "add", "marker.txt")
	mustGit(t, wt, "commit", "-qm", "work")
	mustGit(t, wt, "push", "-q", "origin", "HEAD:factory/t-0001")
	head := mustGit(t, wt, "rev-parse", "HEAD")
	_ = os.MkdirAll(filepath.Join(wt, ".hivemill"), 0o755)
	_ = writeFile(filepath.Join(wt, ".hivemill", "findings.json"), fmt.Sprintf(`{"head": %q, "findings": [{"severity": "low",
		"file": "marker.txt", "line": 1, "issue": "zz remote finding", "resolution": "fixed: done"}]}`, head), 0o600)
	_ = writeFile(filepath.Join(wt, ".hivemill", "evidence.json"), `{"criteria": [{"id": 1}]}`, 0o600)

	rec := r.Gate(run1)
	if rec.Head != head {
		t.Errorf("gate head %q, want the remote HEAD %q", rec.Head, head)
	}
	if len(rec.Findings) != 1 || rec.Findings[0].Issue != "zz remote finding" {
		t.Errorf("findings not read from the machine: %+v", rec.Findings)
	}
	for _, p := range rec.Problems {
		if strings.HasPrefix(p, "review:") || strings.HasPrefix(p, "pr:") {
			continue
		}
		if strings.Contains(p, "findings.json") || strings.Contains(p, "evidence.json") || strings.HasPrefix(p, "criterion ") ||
			strings.HasPrefix(p, "committed") || strings.HasPrefix(p, "identity") {
			t.Errorf("problem: %s", p)
		}
	}
	// the proof passes only where marker.txt is: in the worktree on the machine
	if len(rec.Reruns) != 1 || rec.Reruns[0].OK == nil || !*rec.Reruns[0].OK {
		t.Errorf("rerun on the machine: %+v (problems %v)", rec.Reruns, rec.Problems)
	}
	if _, err := os.Stat(filepath.Join(run1.Mirror, ".hivemill", "findings.json")); err == nil {
		t.Error("the coordinator's clone must not hold the session's files")
	}
	if got := mustGit(t, run1.Mirror, "rev-parse", "HEAD"); got != head {
		t.Errorf("the mirror is at %q, want %q", got, head)
	}
}

func TestCloneMachineProofEnv(t *testing.T) {
	r, s, m, _ := cloneRunner(t)
	if err := r.Start("T-0001"); err != nil {
		t.Fatal(err)
	}
	run1, _ := s.Run("T-0001")
	m.env = []string{"CLAUDECODE=1", "CLAUDE_CODE_OAUTH_TOKEN=zz", "GH_TOKEN=zz", "GITHUB_TOKEN=zz", "ZZ_KEPT=1"}
	// the proof sees none of the machine's tokens or Claude variables, and the rest of its environment
	got := r.RerunProofs(run1, []Criterion{{ID: 1, Kind: "cmd", Expect: "exit 0",
		Proof: `test "$ZZ_KEPT" = 1 && ! env | grep -E '^(CLAUDE|GH_TOKEN=|GITHUB_TOKEN=)'`}}, t.TempDir())
	if len(got) != 1 || got[0].OK == nil || !*got[0].OK {
		t.Errorf("a proof on the machine sees its Claude or GitHub variables: %+v", got)
	}
}

// startedClone is a started run with one pushed commit in its worktree on w1.
func startedClone(t *testing.T) (*Runner, *SQLite, *fakeRemote, *Run) {
	t.Helper()
	r, s, m, _ := cloneRunner(t)
	if err := r.Start("T-0001"); err != nil {
		t.Fatal(err)
	}
	x, _ := s.Run("T-0001")
	if err := os.WriteFile(filepath.Join(x.Worktree, "marker.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, x.Worktree, "add", "marker.txt")
	mustGit(t, x.Worktree, "commit", "-qm", "work")
	mustGit(t, x.Worktree, "push", "-q", "origin", "HEAD:factory/t-0001")
	return r, s, m, x
}

func TestCloneMachineLostConnectionIsNoVerdict(t *testing.T) {
	r, _, _, x := startedClone(t)
	ssh := r.SSH
	r.SSH = func(machine, stdin string, timeout time.Duration, command string) (int, string, string) {
		if strings.Contains(command, "status") {
			return 255, "", "connection lost"
		}
		return ssh(machine, stdin, timeout, command)
	}
	rec := r.Gate(x)
	if !rec.Undecided || len(rec.Problems) != 1 || !strings.Contains(rec.Problems[0], "not decided") {
		t.Errorf("undecided=%v problems %v", rec.Undecided, rec.Problems)
	}
	r.SSH = func(string, string, time.Duration, string) (int, string, string) { return 255, "", "connection lost" }
	if r.worktreeLost(x) {
		t.Error("an unreachable machine has not lost the worktree")
	}
	if _, err := r.Packet(x); err == nil {
		t.Error("no judge packet without the diff")
	}
}

func TestCloneMachineReusedWorktreeFollowsBranch(t *testing.T) {
	r, s, _, x := startedClone(t)
	old := mustGit(t, x.Worktree, "rev-parse", "HEAD")
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, x.Mirror, "fetch", "-q", "origin")
	mustGit(t, filepath.Dir(other), "clone", "-q", "-b", "factory/t-0001", mustGit(t, x.Mirror, "remote", "get-url", "origin"), other)
	mustGit(t, other, "commit", "-q", "--allow-empty", "-m", "refresh")
	mustGit(t, other, "push", "-q", "origin", "HEAD:factory/t-0001")
	head := mustGit(t, other, "rev-parse", "HEAD")

	task, _ := s.Task("T-0001")
	if _, err := r.remoteWorktree("w1", x.RemoteHome, task, x.Branch); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, x.Worktree, "rev-parse", "HEAD"); got != head {
		t.Errorf("reused worktree at %s, want the pushed head %s", got, head)
	}

	if !r.worktreeLost(x) {
		if err := os.RemoveAll(x.Worktree); err != nil {
			t.Fatal(err)
		}
	}
	if !r.worktreeLost(x) {
		t.Error("a missing worktree must be seen")
	}
	if _, err := r.remoteWorktree("w1", x.RemoteHome, task, x.Branch); err != nil {
		t.Fatal(err)
	}

	// a merged run whose worktree stayed behind the merged head holds nothing unmerged
	mustGit(t, x.Worktree, "reset", "-q", "--hard", old)
	mustGit(t, other, "push", "-q", "origin", ":factory/t-0001")
	x.State, x.Head = Merged, head
	_ = s.PutRun(x)
	r.removeRemoteWorktrees()
	if _, err := os.Stat(x.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the merged worktree must be removed: %v", err)
	}
}

func TestCloneMachineTranscriptsReachCoordinator(t *testing.T) {
	r, _, m, x := startedClone(t)
	names := []string{".claude/projects/-wt/sid-1.jsonl", ".claude/projects/-wt/sid-1/subagents/a.jsonl",
		".claude/projects/-wt/sid-1/subagents/workflows/w/b.jsonl"}
	for _, n := range names {
		m.put(n, "one\n", 0o600)
	}
	m.put(".claude/projects/-wt/other.jsonl", "not ours\n", 0o600)
	r.syncTranscripts(x, []string{"sid-1", "x; touch pwned"})
	for _, n := range names {
		if b, err := os.ReadFile(filepath.Join(r.Env.UserHome, n)); err != nil || string(b) != "one\n" {
			t.Errorf("%s: %q %v", n, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.Env.UserHome, ".claude/projects/-wt/other.jsonl")); err == nil {
		t.Error("another session's transcript was copied")
	}
	if _, err := os.Stat(filepath.Join(m.home, "pwned")); err == nil {
		t.Error("a session id reached the shell")
	}
	m.put(names[0], "one\ntwo\n", 0o600)
	r.syncTranscripts(x, []string{"sid-1"})
	if b, _ := os.ReadFile(filepath.Join(r.Env.UserHome, names[0])); string(b) != "one\ntwo\n" {
		t.Errorf("a grown transcript is copied again: %q", b)
	}
}

func TestCloneMachineJudgeStartIsRetried(t *testing.T) {
	r, _, _, x := startedClone(t)
	x.State, x.Head = Gated, mustGit(t, x.Worktree, "rev-parse", "HEAD")
	r.SSH = func(string, string, time.Duration, string) (int, string, string) { return 255, "", "connection lost" }
	r.startJudge(x)
	if x.State != Gated || len(x.Judges) != 0 || x.judgeStartFails() != 1 {
		t.Errorf("state %s judges %v fails %d", x.State, x.Judges, x.judgeStartFails())
	}
}
