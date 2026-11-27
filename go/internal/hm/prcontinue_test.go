package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/guard"
)

// fakePRs is the gh side of a repo: PRs by URL, and every call that changes one.
type fakePRs struct {
	prs    map[string]*PR
	calls  [][]string
	inputs []string
}

func newFakePRs(r *Runner, prs ...PR) *fakePRs {
	f := &fakePRs{prs: map[string]*PR{}}
	for _, p := range prs {
		f.prs[p.URL] = &p
	}
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		switch args[1] {
		case "view":
			p := f.prs[args[2]]
			if p == nil {
				return 1, "", "no such PR"
			}
			return 0, fmt.Sprintf(`{"state":%q,"headRefName":%q,"headRefOid":%q,"body":%q,"url":%q,"closedAt":%q}`,
				p.State, p.HeadRefName, p.HeadRefOid, p.Body, p.URL, p.ClosedAt), ""
		case "list":
			for _, p := range f.prs {
				if p.State == "OPEN" && p.HeadRefName == args[5] {
					return 0, p.URL, ""
				}
			}
			return 0, "", ""
		}
		f.calls, f.inputs = append(f.calls, args), append(f.inputs, in)
		switch args[1] {
		case "close":
			f.prs[args[2]].State, f.prs[args[2]].ClosedAt = "CLOSED", nowISO()
		case "reopen":
			f.prs[args[2]].State = "OPEN"
		case "edit":
			f.prs[args[2]].Body = in
		}
		return 0, "", ""
	}
	return f
}

// did lists the changing calls of one kind, each as one line.
func (f *fakePRs) did(kind string) []string {
	var out []string
	for _, c := range f.calls {
		if c[1] == kind {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

func openPRof(n int, branch string) PR {
	return PR{State: "OPEN", HeadRefName: branch, HeadRefOid: landHead, Body: "## Summary\nx",
		URL: fmt.Sprintf("https://github.com/zz-test/www/pull/%d", n)}
}

func endedRun(task, state, url string, ago time.Duration) *Run {
	return &Run{Task: task, Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Branch: "factory/" + strings.ToLower(task),
		Agent: "claude-coder-" + strings.ToLower(task) + "-a7", State: state, PRURL: url, Round: 1, FailReason: "budget spent",
		Dispatched: time.Now().Add(-2 * time.Hour).Format(time.RFC3339), Ended: time.Now().Add(-ago).Format(time.RFC3339)}
}

func setStatus(t *testing.T, s *SQLite, id, status string) {
	t.Helper()
	if err := s.TaskStatus(id, status, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRefileReusesBranchAndPR(t *testing.T) {
	r, s, _, _ := cloneRunner(t)
	if err := s.SetCfg("session_machines", ""); err != nil {
		t.Fatal(err)
	}
	// the first run pushed its branch and opened a PR, then failed
	task, err := s.Task("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(t.TempDir(), "old")
	_, origin, _ := r.Tools.Git(task.RepoPath, "remote", "get-url", "origin")
	mustGit(t, task.RepoPath, "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "old.txt"), []byte("old work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", "old.txt")
	mustGit(t, seed, "commit", "-qm", "old work")
	mustGit(t, seed, "push", "-q", "origin", "HEAD:factory/t-0001")
	oldHead := mustGit(t, seed, "rev-parse", "HEAD")

	old := openPRof(1, "factory/t-0001")
	old.HeadRefOid = oldHead
	f := newFakePRs(r, old)
	run1 := endedRun("T-0001", Failed, old.URL, time.Hour)
	run1.Repo = task.GHRepo
	putRuns(t, s, run1)
	setStatus(t, s, "T-0001", "blocked")
	id, err := s.AddTask("G-001", "zz-test again", "spec", "1. marker - cmd:test -f marker.txt => exit 0", "todo", "[]", 1, "T-0001")
	if err != nil || id != "T-0002" {
		t.Fatal(id, err)
	}

	if err := r.Start(id); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(id)
	if err != nil || run == nil {
		t.Fatal(run, err)
	}
	if run.Branch != "factory/t-0001" || run.PRURL != old.URL || run.RescuedHead != oldHead {
		t.Fatalf("branch %q, PR %q, rescued %q", run.Branch, run.PRURL, run.RescuedHead)
	}
	if got := mustGit(t, run.Worktree, "rev-parse", "HEAD"); got != oldHead {
		t.Errorf("the clone is at %s, want the old branch head %s", got, oldHead)
	}
	if x, _ := s.Task(id); x.PRURL != old.URL || x.Branch != "factory/t-0001" {
		t.Errorf("task records PR %q, branch %q", x.PRURL, x.Branch)
	}
	if body := f.prs[old.URL].Body; !strings.HasPrefix(body, "## Summary\nx") || !strings.Contains(body, "Re-filed as T-0002") {
		t.Errorf("PR body %q", body)
	}
	if got := r.taskEnv(run)[guard.PushTaskVar]; got != "t-0001" {
		t.Errorf("%s = %q: the worker guard would refuse the push", guard.PushTaskVar, got)
	}
	if got := r.taskEnv(run1)[guard.PushTaskVar]; got != "" {
		t.Errorf("a run on its own branch got %s = %q", guard.PushTaskVar, got)
	}

	// `hivemill pr create` of the new session updates that PR: no second one
	r.Env.Actor = run.Agent
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO agents(name,role,task_id,status) VALUES(?,'task',?,'live')`, run.Agent, id); err != nil {
		t.Fatal(err)
	}
	f.calls, f.inputs = nil, nil
	out, err := r.PR("create", "T-0002: finish the work", "## Summary\ny", run.Worktree)
	if err != nil || out != old.URL {
		t.Fatalf("pr create: %q %v", out, err)
	}
	if len(f.calls) != 1 || len(f.did("edit")) != 1 || !strings.Contains(f.inputs[0], "## Summary\ny") ||
		!strings.Contains(f.inputs[0], "Re-filed as T-0002") {
		t.Errorf("gh calls %v, body %q", f.calls, f.inputs)
	}

	// the old task's PR is the new run's now: no sweep closes it
	r.sweepPRs()
	if c := f.did("close"); len(c) != 0 {
		t.Errorf("closed a continued PR: %v", c)
	}

	// a re-file of a task whose PR the runner closed reopens it
	run.State, run.Ended, run.FailReason = Failed, nowISO(), "stalled"
	putRuns(t, s, run)
	setStatus(t, s, id, "cancelled")
	r.sweepPRs()
	if len(f.did("close")) != 1 || f.prs[old.URL].State != "CLOSED" {
		t.Fatalf("calls %v", f.calls)
	}
	id3, err := s.AddTask("G-001", "zz-test third", "spec", "1. marker - cmd:test -f marker.txt => exit 0", "todo", "[]", 1, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(id3); err != nil {
		t.Fatal(err)
	}
	if run3, _ := s.Run(id3); run3.Branch != "factory/t-0001" || run3.PRURL != old.URL || f.prs[old.URL].State != "OPEN" {
		t.Errorf("branch %q, PR %q, state %s", run3.Branch, run3.PRURL, f.prs[old.URL].State)
	}
}

func TestCancelledTaskClosesPR(t *testing.T) {
	r, s := testRunner(t, "")
	pr := openPRof(1, "factory/t-0001")
	f := newFakePRs(r, pr)
	putRuns(t, s, endedRun("T-0001", Cancelled, "", 0)) // no recorded URL: it is found by branch
	cancelTask(t, s, "T-0001")
	r.sweepPRs()
	r.sweepPRs()
	c := f.did("close")
	if len(c) != 1 || !strings.Contains(c[0], pr.URL) || !strings.Contains(c[0], "T-0001 was cancelled") ||
		!strings.Contains(c[0], "`factory/t-0001` is kept") || strings.Contains(c[0], "--delete-branch") {
		t.Fatalf("close calls %v", f.calls)
	}
	if n := events(t, r, "pr.closed"); n != 1 {
		t.Errorf("%d pr.closed events", n)
	}

	// a failed run waits 30 minutes for a re-file
	for _, c := range []struct {
		name   string
		ago    time.Duration
		refile bool
		closed int
	}{
		{"just failed", 29 * time.Minute, false, 0},
		{"nobody re-filed", 31 * time.Minute, false, 1},
		{"re-filed in time", 31 * time.Minute, true, 0},
	} {
		r, s := testRunner(t, "")
		f := newFakePRs(r, openPRof(1, "factory/t-0001"))
		putRuns(t, s, endedRun("T-0001", Failed, pr.URL, c.ago))
		setStatus(t, s, "T-0001", "blocked")
		if c.refile {
			if _, err := s.AddTask("G-001", "zz-test again", "spec", "1. works - cmd:true => exit 0", "todo", "[]", 1, "T-0001"); err != nil {
				t.Fatal(err)
			}
		}
		r.sweepPRs()
		got := f.did("close")
		if len(got) != c.closed || (c.closed == 1 && !strings.Contains(got[0], "budget spent")) {
			t.Errorf("%s: close calls %v", c.name, got)
		}
	}
}

func TestSupersededPRClosedOnLand(t *testing.T) {
	r, s := testRunner(t, "")
	oldPR, newPR := openPRof(1, "factory/t-0001"), openPRof(2, "factory/t-0002")
	f := newFakePRs(r, oldPR, newPR)
	putRuns(t, s, endedRun("T-0001", Failed, oldPR.URL, time.Minute))
	setStatus(t, s, "T-0001", "blocked")
	id, err := s.AddTask("G-001", "zz-test again", "spec", "1. works - cmd:true => exit 0", "in_progress", "[]", 1, "T-0001")
	if err != nil {
		t.Fatal(err)
	}
	run := endedRun(id, Merging, newPR.URL, 0)
	run.Worktree = t.TempDir()
	putRuns(t, s, run)
	r.sweepPRs()
	if len(f.calls) != 0 {
		t.Fatalf("before the re-file landed: %v", f.calls)
	}

	f.prs[newPR.URL].State = "MERGED"
	r.finish(run, &PR{State: "MERGED", HeadRefOid: landHead})
	putRuns(t, s, run)
	r.sweepPRs()
	c := f.did("close")
	if len(c) != 1 || !strings.Contains(c[0], oldPR.URL) || !strings.Contains(c[0], "superseded by #2") {
		t.Fatalf("close calls %v", f.calls)
	}
}

// A run held for the owner is theirs to decide: a landed re-file does not close its PR, their cancel does.
func TestHeldPRStaysOpenOnLand(t *testing.T) {
	r, s := testRunner(t, "")
	oldPR, newPR := openPRof(1, "factory/t-0001"), openPRof(2, "factory/t-0002")
	f := newFakePRs(r, oldPR, newPR)
	putRuns(t, s, endedRun("T-0001", Held, oldPR.URL, time.Hour))
	setStatus(t, s, "T-0001", "blocked")
	id, err := s.AddTask("G-001", "zz-test again", "spec", "1. works - cmd:true => exit 0", "in_progress", "[]", 1, "T-0001")
	if err != nil {
		t.Fatal(err)
	}
	landed := endedRun(id, Merged, newPR.URL, 0)
	r.closeSuperseded(landed)
	putRuns(t, s, landed)
	r.sweepPRs()
	if len(f.calls) != 0 {
		t.Fatalf("closed a held PR: %v", f.calls)
	}
	setStatus(t, s, "T-0001", "cancelled")
	r.sweepPRs()
	if len(f.did("close")) != 1 {
		t.Errorf("after the cancel: %v", f.calls)
	}
}

func TestStartBranchChecksTheContinuedBranch(t *testing.T) {
	r, s := testRunner(t, "")
	pr := openPRof(1, "factory/t-0001")
	f := newFakePRs(r, pr)
	putRuns(t, s, endedRun("T-0001", Failed, pr.URL, time.Hour))
	id, err := s.AddTask("G-001", "zz-test again", "spec", "1. works - cmd:true => exit 0", "todo", "[]", 1, "T-0001")
	if err != nil {
		t.Fatal(err)
	}
	start := func(task, recorded, url string) (string, error) {
		x, err := s.Task(task)
		if err != nil {
			t.Fatal(err)
		}
		x.Branch, x.PRURL = recorded, url
		return r.startBranch(x)
	}
	for _, recorded := range []string{"", "factory/t-0001"} {
		if got, err := start(id, recorded, pr.URL); err != nil || got != "factory/t-0001" {
			t.Errorf("recorded %q: %q %v", recorded, got, err)
		}
		f.prs[pr.URL].State = "MERGED"
		if got, err := start(id, recorded, pr.URL); err != nil || got != "factory/t-0002" {
			t.Errorf("recorded %q, merged: %q %v", recorded, got, err)
		}
		f.prs[pr.URL].State = "OPEN"
		delete(f.prs, pr.URL) // gh cannot read it: the start waits
		if got, err := start(id, recorded, pr.URL); err == nil {
			t.Errorf("recorded %q, unreadable PR: started on %q", recorded, got)
		}
		f.prs[pr.URL] = &pr
	}
	// the re-file's run holds the old branch: the old task cannot be retried onto it
	run := endedRun(id, Working, pr.URL, 0)
	run.Branch = "factory/t-0001"
	putRuns(t, s, run)
	if got, err := start("T-0001", "factory/t-0001", pr.URL); err == nil {
		t.Errorf("a second run on %q", got)
	}
}

func TestAdoptPRLeavesAHumanCloseAndForgetsIt(t *testing.T) {
	r, s := testRunner(t, "")
	pr := openPRof(1, "factory/t-0001")
	pr.State, pr.ClosedAt = "CLOSED", time.Now().Format(time.RFC3339)
	f := newFakePRs(r, pr)
	// the runner closed it an hour ago; somebody reopened it and closed it again just now
	if err := s.SetMeta(prClosedKey+pr.URL, time.Now().Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.TaskPR("T-0001", pr.URL, ""); err != nil {
		t.Fatal(err)
	}
	task, err := s.Task("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{Task: "T-0001", Repo: "zz-test/www", Branch: "factory/t-0001"}
	r.adoptPR(run, task)
	if len(f.calls) != 0 || run.PRURL != "" {
		t.Errorf("reopened a PR a person closed: %v, %q", f.calls, run.PRURL)
	}
	if x, _ := s.Task("T-0001"); x.PRURL != "" {
		t.Errorf("the task keeps the closed PR %q", x.PRURL)
	}
}

// Runs that cost no gh call do not use up the sweep's cap, and a failed one waits before it is tried again.
func TestPRSweepCapCountsGHCallsOnly(t *testing.T) {
	r, s := testRunner(t, "")
	f := newFakePRs(r)
	run := endedRun("T-0001", Cancelled, "https://github.com/zz-test/www/pull/9", 0) // gh cannot read it
	putRuns(t, s, run)
	cancelTask(t, s, "T-0001")
	views := 0
	gh := r.Tools.GH
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		views++
		return gh(in, args...)
	}
	if !r.closePR(run, "why", "") || r.closePR(run, "why", run.PRURL) {
		t.Error("called: want true for a gh read, false for the landed PR itself")
	}
	if err := s.SetMeta(prSweptKey+"T-0001", ""); err != nil {
		t.Fatal(err)
	}
	views = 0
	r.sweepPRs()
	if views != 0 || len(f.calls) != 0 {
		t.Errorf("%d gh calls right after a failure", views)
	}
}
