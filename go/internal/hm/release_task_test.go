package hm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const releaseJWT = "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ6ei10ZXN0In0.c2lnbmF0dXJl"

// releaseRig is a runner whose project www has a real origin, a stale and dirty checkout of it, and a fake release_cmd.
type releaseRig struct {
	r                      *Runner
	s                      *SQLite
	task, calls, tip, seed string
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=zz", "-c", "user.email=zz@example.invalid"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeT(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// newReleaseRig files one release task. The fake command records its arguments, directory, HEAD, branch and git status,
// then runs body.
func newReleaseRig(t *testing.T, config, body string) *releaseRig {
	t.Helper()
	r, s := testRunner(t, "owner")
	root := t.TempDir()
	origin, seed, checkout := filepath.Join(root, "origin"), filepath.Join(root, "seed"), filepath.Join(root, "checkout")
	gitT(t, root, "init", "--quiet", "--bare", "--initial-branch=main", origin)
	gitT(t, root, "clone", "--quiet", origin, seed)
	if err := os.MkdirAll(filepath.Join(seed, ".hivemill"), 0o700); err != nil {
		t.Fatal(err)
	}
	if config == "" {
		config = `{"submitExternal": false}`
	}
	writeT(t, filepath.Join(seed, ".hivemill", "ios-release.json"), config, 0o600)
	gitT(t, seed, "add", ".")
	gitT(t, seed, "commit", "--quiet", "-m", "one")
	gitT(t, seed, "push", "--quiet", "origin", "HEAD:main")
	gitT(t, root, "clone", "--quiet", origin, checkout)
	gitT(t, checkout, "checkout", "--quiet", "-b", "work")
	writeT(t, filepath.Join(checkout, "dirty"), "x", 0o600)
	// origin moves after the checkout was cloned: only a fetch sees it
	gitT(t, seed, "commit", "--quiet", "--allow-empty", "-m", "two")
	gitT(t, seed, "push", "--quiet", "origin", "HEAD:main")
	if _, err := s.db.Exec(`UPDATE projects SET repo_path=? WHERE id='www'`, checkout); err != nil {
		t.Fatal(err)
	}

	r.Tools.Git = func(repo string, args ...string) (int, string, string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			return 1, "", string(out)
		}
		return 0, string(out), ""
	}

	calls := filepath.Join(root, "calls")
	script := "#!/bin/sh\n" +
		`echo "args=$* dir=$(pwd -P) head=$(git rev-parse HEAD) branch=$(git rev-parse --abbrev-ref HEAD) dirty=$(git status --porcelain | wc -l)" >> '` +
		calls + "'\n" + body
	writeT(t, filepath.Join(r.Env.UserHome, "release.sh"), script, 0o700)
	if err := s.SetReleaseCmd("www", "~/release.sh"); err != nil {
		t.Fatal(err)
	}
	task, err := s.AddReleaseTask("G-001", "release build of www", "", "todo", "[]", 1)
	if err != nil {
		t.Fatal(err)
	}
	return &releaseRig{r: r, s: s, task: task, calls: calls, tip: gitT(t, seed, "rev-parse", "HEAD"), seed: seed}
}

const releaseOK = `echo "uploading" >&2
echo '{"ok":true,"exitCode":0,"mode":"release","build":42,"groups":[{"name":"Team","status":"added"}],"error":null}'
`

func (g *releaseRig) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(g.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (g *releaseRig) start(t *testing.T) {
	t.Helper()
	if err := g.r.Start(g.task); err != nil {
		t.Fatal(err)
	}
}

func (g *releaseRig) want(t *testing.T, status, state string) string {
	t.Helper()
	got, _, err := g.s.TaskState(g.task)
	if err != nil {
		t.Fatal(err)
	}
	st, result, err := g.s.Release(g.task)
	if err != nil {
		t.Fatal(err)
	}
	if got != status || st != state {
		t.Fatalf("task %s, release %q; want %s, %q\nlog:\n%s", got, st, status, state, g.log())
	}
	return result
}

func (g *releaseRig) log() string {
	b, _ := os.ReadFile(g.r.ReleaseLog(g.task))
	return string(b)
}

func (g *releaseRig) openAsk(t *testing.T) Ask {
	t.Helper()
	asks, err := g.s.Asks("open")
	if err != nil {
		t.Fatal(err)
	}
	if len(asks) != 1 || asks[0].Task != g.task {
		t.Fatalf("open questions: %+v", asks)
	}
	return asks[0]
}

func TestReleaseTaskRunsFromCleanMain(t *testing.T) {
	g := newReleaseRig(t, "", releaseOK)
	ready, _, err := g.r.Queue([]string{"www"})
	if err != nil || len(ready) != 1 || ready[0].ID != g.task || ready[0].Kind != KindRelease {
		t.Fatalf("a release task needs no spec or criteria to be startable: %+v %v", ready, err)
	}
	g.start(t)
	g.want(t, "done", releaseDone)
	wt := filepath.Join(g.r.Env.Home, "worktrees", "release-"+g.task)
	real, err := filepath.EvalSymlinks(g.r.Env.Home)
	if err != nil {
		t.Fatal(err)
	}
	where := "dir=" + filepath.Join(real, "worktrees", "release-"+g.task) + " head=" + g.tip + " branch=HEAD dirty=0"
	for _, line := range g.lines(t) {
		if !strings.Contains(line, where) {
			t.Errorf("ran %q, want %q", line, where)
		}
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("the worktree is still there: %v", err)
	}
	if run, _ := g.s.Run(g.task); run != nil {
		t.Errorf("a release task has no session run: %+v", run)
	}
}

func TestReleaseTaskDryRunFirst(t *testing.T) {
	g := newReleaseRig(t, "", releaseOK)
	g.start(t)
	lines := g.lines(t)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "args=--dry-run dir=") || !strings.HasPrefix(lines[1], "args= dir=") {
		t.Fatalf("calls: %q", lines)
	}

	t.Setenv("HIVEMILL_REDACT_VALUES", "/zz/keys/AuthKey_ZZTEST.p8")
	g = newReleaseRig(t, "", `i=0; while [ $i -lt 60 ]; do echo "line $i"; i=$((i+1)); done
echo "token `+releaseJWT+` key AuthKey_ZZTEST.p8"
echo '{"ok":false,"exitCode":4,"mode":"dry-run","build":null,"error":"no signing identity"}'
exit 4
`)
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	if lines := g.lines(t); len(lines) != 1 {
		t.Fatalf("a failed dry run must not be followed by a release: %q", lines)
	}
	q := g.openAsk(t).Question
	for _, s := range []string{"exit 4", "no signing identity", "line 59", redactMask} {
		if !strings.Contains(q, s) {
			t.Errorf("the question lacks %q:\n%s", s, q)
		}
	}
	if strings.Contains(q, "line 5\n") {
		t.Errorf("the question holds more than the last %d lines:\n%s", releaseTailLines, q)
	}
	for _, secret := range []string{releaseJWT, "AuthKey_ZZTEST"} {
		if strings.Contains(q, secret) || strings.Contains(g.log(), secret) {
			t.Errorf("%q is not redacted", secret)
		}
	}
	var work int
	if err := g.s.db.QueryRow(`SELECT COUNT(*) FROM planner_work WHERE kind='release_failed' AND ref=?`, g.task).Scan(&work); err != nil || work != 1 {
		t.Errorf("planner work items: %d %v", work, err)
	}
}

func TestReleaseTaskRecordsBuild(t *testing.T) {
	g := newReleaseRig(t, "", releaseOK)
	g.start(t)
	result := g.want(t, "done", releaseDone)
	for _, s := range []string{`"build":42`, `"state":"VALID"`, `"name":"Team"`} {
		if !strings.Contains(result, s) {
			t.Errorf("the result lacks %s: %s", s, result)
		}
	}
	if err := g.r.Start(g.task); err == nil {
		t.Error("a done release started again")
	}

	// the release job that died is failed, not run again
	g = newReleaseRig(t, "", releaseOK)
	if err := g.s.SetRelease(g.task, releaseRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.s.TaskStatus(g.task, "in_progress", ""); err != nil {
		t.Fatal(err)
	}
	g.r.sweepReleases()
	g.want(t, "blocked", releaseFailed)
	if lines := g.lines(t); len(lines) != 0 {
		t.Errorf("calls: %q", lines)
	}
}

func TestReleaseTaskRefusesSubmit(t *testing.T) {
	answer := func(g *releaseRig, id int64, text string) {
		t.Helper()
		if err := g.s.CloseAsk(id, "answered", text, "owner"); err != nil {
			t.Fatal(err)
		}
		if err := g.s.TaskStatus(g.task, "todo", ""); err != nil {
			t.Fatal(err)
		}
	}

	// a command that names submit does not run at all
	g := newReleaseRig(t, "", releaseOK)
	if err := g.s.SetReleaseCmd("www", "~/release.sh --submit"); err != nil {
		t.Fatal(err)
	}
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	q := g.openAsk(t)
	if !strings.Contains(q.Question, "release build N") || len(g.lines(t)) != 0 {
		t.Fatalf("question %q, calls %q", q.Question, g.lines(t))
	}
	// an answer for another build lets the dry run happen and nothing else
	answer(g, q.ID, "release build 41")
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	if lines := g.lines(t); len(lines) != 1 || !strings.HasPrefix(lines[0], "args=--submit --dry-run ") {
		t.Fatalf("calls: %q", lines)
	}

	// submitExternal true in the config: the dry run only, until the owner names the build
	g = newReleaseRig(t, `{"submitExternal": true}`, releaseOK)
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	q = g.openAsk(t)
	if lines := g.lines(t); len(lines) != 1 || !strings.Contains(q.Question, `"release build 42"`) {
		t.Fatalf("question %q, calls %q", q.Question, lines)
	}
	answer(g, q.ID, "release build 42")
	g.start(t)
	g.want(t, "done", releaseDone)
	if lines := g.lines(t); len(lines) != 3 {
		t.Fatalf("calls: %q", lines)
	}

	// a config nobody can read is refused too
	g = newReleaseRig(t, `{"submitExternal": `, releaseOK)
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	if lines := g.lines(t); len(lines) != 0 {
		t.Fatalf("calls: %q", lines)
	}

	// and so is a --config that is not there
	g = newReleaseRig(t, "", releaseOK)
	if err := g.s.SetReleaseCmd("www", "~/release.sh --config=~/missing.json"); err != nil {
		t.Fatal(err)
	}
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	if q := g.openAsk(t).Question; !strings.Contains(q, filepath.Join(g.r.Env.UserHome, "missing.json")) || len(g.lines(t)) != 0 {
		t.Errorf("question %q", q)
	}
}

// An approved submit cuts the approved build and no other, and a running release is not started again.
func TestReleaseTaskGuards(t *testing.T) {
	other := `case "$1" in --dry-run) echo '{"ok":true,"build":42}' ;; *) echo '{"ok":true,"build":43}' ;; esac
`
	g := newReleaseRig(t, `{"submitExternal": true}`, other)
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	if err := g.s.CloseAsk(g.openAsk(t).ID, "answered", "release build 42", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := g.s.TaskStatus(g.task, "todo", ""); err != nil {
		t.Fatal(err)
	}
	g.start(t)
	g.want(t, "blocked", releaseFailed)
	if q := g.openAsk(t).Question; !strings.Contains(q, "allowed build 42") {
		t.Errorf("question %q", q)
	}

	g = newReleaseRig(t, "", releaseOK)
	if err := g.s.SetRelease(g.task, releaseRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.r.Start(g.task); err == nil || len(g.lines(t)) != 0 {
		t.Errorf("a running release started again: %v", err)
	}
}
