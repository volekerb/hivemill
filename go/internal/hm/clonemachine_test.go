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
		return run(timeout, m.home, stdin, env, "sh", "-c", command)
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
	if _, err := os.Stat(filepath.Join(wt, ".git", "hooks", "pre-push")); err != nil {
		t.Errorf("not written on the machine: %v", err)
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
