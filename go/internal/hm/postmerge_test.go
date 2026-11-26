package hm

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type pmFake struct {
	mu     sync.Mutex
	rc     int
	out    string
	calls  []string // dir | merge SHA | argv of every post_merge_cmd
	merged []string // PR URLs `gh pr merge` ran for
}

func (f *pmFake) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// pmRunner is testRunner with a post_merge_cmd on project www, a second project without one, and a gh that merges.
func pmRunner(t *testing.T) (*Runner, *SQLite, *pmFake) {
	t.Helper()
	r, s := testRunner(t, "")
	if _, err := s.db.Exec(`INSERT INTO projects(id,name,repo_path,gh_repo) VALUES('api','api',?,'zz-test/api')`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// the command runs only in a checkout that exists
	if _, err := s.db.Exec(`UPDATE projects SET repo_path=? WHERE id='www'`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProfile(ProjectProfile{ID: "www", PostMergeCmd: "./smoke.sh"}); err != nil {
		t.Fatal(err)
	}
	f := &pmFake{}
	r.Tools.Sh = func(dir string, _ time.Duration, env []string, argv ...string) (int, string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sha := ""
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "HIVEMILL_MERGE_SHA="); ok {
				sha = v
			}
		}
		f.calls = append(f.calls, dir+"|"+sha+"|"+strings.Join(argv, " "))
		return f.rc, f.out
	}
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		if len(args) > 2 && args[0] == "pr" && args[1] == "merge" {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.merged = append(f.merged, args[2])
		}
		if len(args) > 2 && args[0] == "pr" && args[1] == "view" {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !slices.Contains(f.merged, args[2]) {
				return 0, `{"state":"OPEN","url":"` + args[2] + `"}`, ""
			}
			return 0, `{"state":"MERGED","url":"` + args[2] + `","mergeCommit":{"oid":"` + strings.Repeat("c", 40) + `"}}`, ""
		}
		return 0, "", ""
	}
	return r, s, f
}

func pmRun(task, project string) *Run {
	return &Run{Task: task, Goal: "G-001", Project: project, Repo: "zz-test/" + project, Base: "main", State: Merging,
		PRURL: "https://github.com/zz-test/" + project + "/pull/" + task}
}

// pmMerge records the run as merged on sha, as the merge step does.
func pmMerge(t *testing.T, r *Runner, run *Run, sha string) {
	t.Helper()
	pr := &PR{State: "MERGED", MergeCommit: &struct {
		Oid string `json:"oid"`
	}{sha}}
	r.finish(run, pr)
	if err := r.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
}

func pmCheck(r *Runner) {
	r.PostMergeCheck()
	r.WaitPostMerge()
}

func pmEvents(t *testing.T, r *Runner, kind string) []Event {
	t.Helper()
	evs, err := r.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(evs, func(e Event) bool { return e.Event != kind })
}

func TestPostMergeCmdGreen(t *testing.T) {
	r, s, f := pmRunner(t)
	sha := strings.Repeat("b", 40)
	run := pmRun("T-0001", "www")
	pmMerge(t, r, run, sha)
	if x, _ := s.Run("T-0001"); x.PostMerge != PostMergePending {
		t.Fatalf("after the merge: %q", x.PostMerge)
	}
	// a record post_merge_cmd wrote earlier goes with the green result
	if err := s.SetRedMain("zz-test/www", map[string]any{"source": postMergeSource, "task": "T-0001"}); err != nil {
		t.Fatal(err)
	}
	pmCheck(r)
	pmCheck(r) // a finished command does not run again
	task, err := s.Task("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.ran(), []string{task.RepoPath + "|" + sha + "|bash -c exec 2>&1\n./smoke.sh"}; !slices.Equal(got, want) {
		t.Fatalf("commands: %q, want %q", got, want)
	}
	if x, _ := s.Run("T-0001"); x.PostMerge != PostMergeOK || x.State != Merged {
		t.Errorf("run: %s %q", x.State, x.PostMerge)
	}
	if st, red := s.RedMain("zz-test/www"); red {
		t.Errorf("still red: %v", st)
	}
	if evs := pmEvents(t, r, "post_merge.ok"); len(evs) != 1 || evs[0].Data["merge_sha"] != sha || evs[0].Project != "www" {
		t.Errorf("post_merge.ok: %+v", evs)
	}

	// the monitor's own red record is not this command's to clear
	if err := s.SetRedMain("zz-test/www", map[string]any{"run": 42}); err != nil {
		t.Fatal(err)
	}
	pmMerge(t, r, pmRun("T-0002", "www"), sha)
	pmCheck(r)
	if _, red := s.RedMain("zz-test/www"); !red {
		t.Error("a green post_merge_cmd cleared the monitor's record")
	}

	// a project without the setting gets no check
	pmMerge(t, r, pmRun("T-0003", "api"), sha)
	if x, _ := s.Run("T-0003"); x.PostMerge != "" {
		t.Errorf("project without post_merge_cmd: %q", x.PostMerge)
	}
}

func TestPostMergeCmdRedHoldsProject(t *testing.T) {
	r, s, f := pmRunner(t)
	f.rc, f.out = 1, "deploy FAILED"
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	st, red := s.RedMain("zz-test/www")
	if !red || !fromPostMerge(st) || st["sha"] != strings.Repeat("b", 40) {
		t.Fatalf("red record: %v %v", red, st)
	}
	if x, _ := s.Run("T-0001"); x.PostMerge != PostMergeRed {
		t.Errorf("run: %q", x.PostMerge)
	}

	// red_main_freeze is off here: the project's merges hold all the same
	next := pmRun("T-0900", "www")
	in, _, err := r.gatherMerge(&Run{Task: next.Task, Project: "www", Repo: next.Repo, PRURL: next.PRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !in.RedMain || !in.PostMergeRed || in.FixTask || in.PostMerge {
		t.Fatalf("merge input: %+v", in)
	}
	in.LaneVerdict, in.Behind, in.CI, in.Tier, in.MainRun, in.IdentityOK, in.PublicTextOK = "pass", false, "green", "T1", "none", true, true
	if d := Decide(in); d != (MergeDecision{"hold", postMergeHeld}) {
		t.Errorf("decision: %+v", d)
	}
	in.FixTask = true
	if d := Decide(in); d.Action != "merge" {
		t.Errorf("the fix-forward task: %+v", d)
	}
	r.mergeNow(next, "sha", false)
	if next.MergeHold != "hold: "+postMergeHeld || len(f.merged) != 0 {
		t.Fatalf("merge of a red project: %q %v", next.MergeHold, f.merged)
	}

	// another project is not affected
	other := pmRun("T-0901", "api")
	r.mergeNow(other, "sha", false)
	if other.State != Merged || !slices.Equal(f.merged, []string{other.PRURL}) {
		t.Fatalf("other project: %s %q %v", other.State, other.MergeHold, f.merged)
	}

	// the fix-forward task merges, and its green command ends the red
	f.mu.Lock()
	f.rc = 0
	f.mu.Unlock()
	fix := pmRun(st["task"].(string), "www")
	r.mergeNow(fix, "sha", false)
	if fix.State != Merged {
		t.Fatalf("fix-forward merge: %q", fix.MergeHold)
	}
	pmCheck(r)
	if st, red := s.RedMain("zz-test/www"); red {
		t.Errorf("still red after the green fix: %v", st)
	}
}

func TestPostMergeCmdFilesFixForward(t *testing.T) {
	r, s, f := pmRunner(t)
	var lines []string
	for i := range 100 {
		lines = append(lines, "line-"+string(rune('a'+i/26))+string(rune('a'+i%26)))
	}
	f.rc, f.out = 124, strings.Join(lines, "\n")+" "+r.Env.Home+"/x"
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	st, _ := s.RedMain("zz-test/www")
	id, _ := st["task"].(string)
	task, err := s.Task(id)
	if err != nil || task == nil || task.Goal != "G-001" {
		t.Fatalf("fix-forward task %q: %v %+v", id, err, task)
	}
	// the last 60 lines: line 40 is the first of them
	if !strings.Contains(task.Spec, "```\n"+lines[40]+"\n") || strings.Contains(task.Spec, lines[39]) ||
		!strings.Contains(task.Spec, lines[99]+" <factory>/x\n```") || !strings.Contains(task.Spec, "timed out") {
		t.Errorf("spec:\n%s", task.Spec)
	}
	if p := LintProblems(task.Acceptance); len(p) != 0 {
		t.Errorf("acceptance does not lint: %v", p)
	}
	if evs := pmEvents(t, r, "post_merge.red"); len(evs) != 1 || evs[0].Data["fix_task"] != id || evs[0].Data["why"] != "timed out" {
		t.Errorf("post_merge.red: %+v", evs)
	}

	// the same result recorded again files no second task
	x, _ := s.Run("T-0001")
	x.PostMerge = PostMergePending
	_ = s.PutRun(x)
	pmCheck(r)
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM tasks WHERE title LIKE 'Fix forward T-0001%'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d fix-forward tasks", n)
	}
}

func TestPostMergeRedKeepsMonitorRecord(t *testing.T) {
	r, s, f := pmRunner(t)
	if err := s.SetRedMain("zz-test/www", map[string]any{"run": 42, "task": "T-0042"}); err != nil {
		t.Fatal(err)
	}
	f.rc = 1
	for _, task := range []string{"T-0001", "T-0002"} { // two red commands in a row keep the monitor's record
		pmMerge(t, r, pmRun(task, "www"), strings.Repeat("b", 40))
		pmCheck(r)
	}
	f.rc = 0
	pmMerge(t, r, pmRun("T-0003", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	if st, red := s.RedMain("zz-test/www"); !red || fromPostMerge(st) || st["task"] != "T-0042" {
		t.Errorf("record after the green command: %v %v", red, st)
	}
}

func TestPostMergeRevertAndFixForwardBothMerge(t *testing.T) {
	r, s, f := pmRunner(t)
	if err := s.SetRedMain("zz-test/www", map[string]any{"run": 42, "task": "T-0042"}); err != nil {
		t.Fatal(err)
	}
	f.rc = 1
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	if err := r.nameRevert("zz-test/www", "T-0777"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.RedMain("zz-test/www")
	fix, _ := st["task"].(string)
	for _, task := range []string{fix, "T-0777", "T-0042"} {
		if !mayMergeRed(st, task) {
			t.Errorf("%q may not merge: %v", task, st)
		}
	}
	if mayMergeRed(st, "T-0900") || mayMergeRed(st, "") {
		t.Errorf("another task may merge: %v", st)
	}
}

func TestPostMergeFixForwardCanStart(t *testing.T) {
	r, s, f := pmRunner(t)
	if err := s.SetGoalStatus("G-001", "done"); err != nil {
		t.Fatal(err)
	}
	f.rc = 1
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	st, _ := s.RedMain("zz-test/www")
	id, _ := st["task"].(string)
	var status string
	if err := s.db.QueryRow(`SELECT status FROM goals WHERE id='G-001'`).Scan(&status); err != nil || status != "active" {
		t.Errorf("goal of the fix-forward task: %q %v", status, err)
	}
	if !r.isPostMergeFix(id) || r.isPostMergeFix("T-0001") {
		t.Errorf("fix-forward task %q is not told apart", id)
	}
}

func TestPostMergeRedactsOutput(t *testing.T) {
	r, _, _ := pmRunner(t)
	got := r.redactOutput("ok\nAuthorization: Bearer abcdefgh\nurl https://u:pw@example.com/x\n" + strings.Repeat("a", 2000))
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[0] != "ok" || strings.Contains(got, "abcdefgh") || strings.Contains(got, "u:pw") ||
		len(lines[3]) > postMergeLine+8 {
		t.Errorf("redacted:\n%s", got)
	}
	// made-up values: prefixed variable names, and a key block with and without its first line in the tail
	// (written in pieces so that the repo's public-text scan does not read the fixture as a real secret)
	begin := "-----BEGIN " + "PRIVATE KEY-----\n"
	got = r.redactOutput(strings.Join([]string{"AWS_SECRET_ACCESS_KEY=zzfake1", "SECRET_KEY=zzfake2", ""}, "\n") +
		begin + "zzfake3\n-----END PRIVATE KEY-----\ndone")
	if strings.Contains(got, "zzfake") || !strings.HasSuffix(got, "done") {
		t.Errorf("redacted:\n%s", got)
	}
	got = r.redactOutput("zzfake4\n-----END PRIVATE KEY-----\ndone")
	if strings.Contains(got, "zzfake") || !strings.HasSuffix(got, "done") {
		t.Errorf("tail that starts inside a key:\n%s", got)
	}
	got = r.redactOutput("redis://:zzfake5@cache:1\nDB_PASS=zzfake6\n--token zzfake7")
	if strings.Contains(got, "zzfake") {
		t.Errorf("redacted:\n%s", got)
	}
	plain := "./ci/check-disk-space.sh\nFAIL: task-runner smoke test\nunexpected token } in JSON"
	if got = r.redactOutput(plain); got != plain {
		t.Errorf("plain lines were redacted:\n%s", got)
	}
}

// The timeout and a stopping daemon kill what the command started, not only its shell.
func TestPostMergeShGroupKillsChildren(t *testing.T) {
	script := []string{"bash", "-c", "sleep 60 & wait"}
	start := time.Now()
	if rc, _ := shGroup(context.Background(), t.TempDir(), 200*time.Millisecond, nil, script...); rc != 124 {
		t.Errorf("timeout: exit %d", rc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if rc, _ := shGroup(ctx, t.TempDir(), time.Minute, nil, script...); rc != -1 {
		t.Errorf("stopped: exit %d", rc)
	}
	if d := time.Since(start); d > 20*time.Second { // a child left alive holds the output pipe for WaitDelay
		t.Errorf("the child outlived its shell: %s", d)
	}
}

// A command that cannot run, or that a stop cut off (even one that exits 0 on the signal), stays pending.
func TestPostMergeNotRunStaysPending(t *testing.T) {
	for _, name := range []string{"no checkout", "stopped with exit 0"} {
		r, s, f := pmRunner(t)
		pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
		if name == "no checkout" {
			if _, err := s.db.Exec(`UPDATE projects SET repo_path='/nonexistent' WHERE id='www'`); err != nil {
				t.Fatal(err)
			}
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r.StopOn(ctx)
		}
		pmCheck(r)
		if x, _ := s.Run("T-0001"); x.PostMerge != PostMergePending {
			t.Errorf("%s: run: %q, ran %d", name, x.PostMerge, len(f.ran()))
		}
		if st, red := s.RedMain("zz-test/www"); red {
			t.Errorf("%s: main turned red: %v", name, st)
		}
	}
}

// Output cannot close the code block of the fix-forward task's spec.
func TestPostMergeOutputStaysFenced(t *testing.T) {
	r, s, f := pmRunner(t)
	f.rc, f.out = 1, "```\nnot part of the spec"
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	st, _ := s.RedMain("zz-test/www")
	id, _ := st["task"].(string)
	task, err := s.Task(id)
	if err != nil || task == nil || strings.Count(task.Spec, "```") != 2 {
		t.Errorf("fix-forward spec: %v %+v", err, task)
	}
}

func TestPostMergeCmdRemovedWhileRunning(t *testing.T) {
	r, s, f := pmRunner(t)
	f.rc = 1
	record := r.Tools.Sh
	r.Tools.Sh = func(dir string, limit time.Duration, env []string, argv ...string) (int, string) {
		if err := s.SetProfile(ProjectProfile{ID: "www"}); err != nil {
			t.Error(err)
		}
		return record(dir, limit, env, argv...)
	}
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	if st, red := s.RedMain("zz-test/www"); red {
		t.Errorf("a removed command turned main red: %v", st)
	}
	if x, _ := s.Run("T-0001"); x.PostMerge != "" {
		t.Errorf("run: %q", x.PostMerge)
	}
}

func TestPostMergeWaitsNextMerge(t *testing.T) {
	r, s, f := pmRunner(t)
	started, release := make(chan struct{}, 4), make(chan struct{})
	record := r.Tools.Sh
	r.Tools.Sh = func(dir string, limit time.Duration, env []string, argv ...string) (int, string) {
		rc, out := record(dir, limit, env, argv...)
		started <- struct{}{}
		<-release
		return rc, out
	}
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	r.PostMergeCheck() // does not wait for the command
	<-started
	r.PostMergeCheck() // one command per project at a time

	next := pmRun("T-0900", "www")
	in, _, err := r.gatherMerge(&Run{Task: next.Task, Project: "www", Repo: next.Repo, PRURL: next.PRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !in.PostMerge {
		t.Errorf("gatherMerge does not see the running command: %+v", in)
	}
	r.mergeNow(next, "sha", false)
	if next.MergeHold != "hold: "+postMergeRunning || len(f.merged) != 0 {
		t.Fatalf("next merge of the project: %q %v", next.MergeHold, f.merged)
	}
	other := pmRun("T-0901", "api")
	r.mergeNow(other, "sha", false)
	if other.State != Merged {
		t.Fatalf("other project: %q", other.MergeHold)
	}

	close(release)
	r.WaitPostMerge()
	if n := len(f.ran()); n != 1 {
		t.Errorf("%d commands ran", n)
	}
	r.mergeNow(next, "sha", false)
	if next.State != Merged || !slices.Equal(f.merged, []string{other.PRURL, next.PRURL}) {
		t.Fatalf("after the command: %q %v", next.MergeHold, f.merged)
	}
	if x, _ := s.Run("T-0001"); x.PostMerge != PostMergeOK {
		t.Errorf("first run: %q", x.PostMerge)
	}
}

// A merge that is not confirmed, or has no merge commit yet, holds the project's next merge until the PR shows open.
func TestPostMergeUnconfirmedMergeHolds(t *testing.T) {
	for name, view := range map[string]string{"not confirmed": `{"state":"OPEN"}`, "no merge commit": `{"state":"MERGED"}`} {
		r, s, _ := pmRunner(t)
		r.Tools.GH = func(_ string, args ...string) (int, string, string) {
			if len(args) > 1 && args[1] == "view" {
				return 0, view, ""
			}
			return 0, "", ""
		}
		run := pmRun("T-0001", "www")
		r.mergeNow(run, "sha", false)
		if x, _ := s.Run("T-0001"); x == nil || x.PostMerge != PostMergePending || x.State == Merged {
			t.Fatalf("%s: stored run %+v", name, x)
		}
		next := pmRun("T-0900", "www")
		r.mergeNow(next, "sha", false)
		if next.MergeHold != "hold: "+postMergeRunning {
			t.Errorf("%s: next merge: %q", name, next.MergeHold)
		}
		// a stop releases the hold only when the PR is not merged
		err := r.Stop("T-0001")
		x, _ := s.Run("T-0001")
		if merged := strings.Contains(view, "MERGED"); merged != (err != nil) || merged != (x.PostMerge == PostMergePending) {
			t.Errorf("%s: stop: %v, post merge %q", name, err, x.PostMerge)
		}
	}
}

// A failed `gh pr merge` may still have merged (the branch delete comes after): with the PR unreadable, the project holds.
func TestPostMergeFailedMergeUnreadablePRHolds(t *testing.T) {
	r, s, _ := pmRunner(t)
	r.Tools.GH = func(string, ...string) (int, string, string) { return 1, "", "no network" }
	run := pmRun("T-0001", "www")
	r.mergeNow(run, "sha", false)
	if x, _ := s.Run("T-0001"); x == nil || x.PostMerge != PostMergePending {
		t.Fatalf("stored run %+v", x)
	}
}

func TestPostMergeRedRevertIsNotReverted(t *testing.T) {
	r, s, f := pmRunner(t)
	if err := s.SetCfg("auto_revert", "on"); err != nil {
		t.Fatal(err)
	}
	f.rc = 1
	run := pmRun("T-0001", "www")
	run.Kind = "revert"
	pmMerge(t, r, run, strings.Repeat("b", 40))
	pmCheck(r)
	// opening a revert would fail here (no repository), so no error means it was not tried
	if err := r.RevertCheck(); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.Run("T-0001"); x.RevertedBy != "" {
		t.Errorf("the revert was reverted by %s", x.RevertedBy)
	}
}

func TestPostMergeSecondRedKeepsFixForward(t *testing.T) {
	r, s, f := pmRunner(t)
	f.rc = 1
	// the second merged task; its id is one the filed fix-forward does not take
	if _, err := s.db.Exec(`INSERT INTO tasks(id,goal_id,title,spec,status,acceptance) VALUES('T-0009','G-001','x','spec','done','1. x - cmd:true => 0')`); err != nil {
		t.Fatal(err)
	}
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	st, _ := s.RedMain("zz-test/www")
	first, _ := st["task"].(string)
	pmMerge(t, r, pmRun("T-0009", "www"), strings.Repeat("d", 40))
	pmCheck(r)
	st, _ = s.RedMain("zz-test/www")
	if first == "" || st["task"] != first || st["sha"] != strings.Repeat("d", 40) {
		t.Errorf("record after the second red: %v, first fix-forward %q", st, first)
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM tasks WHERE title LIKE 'Fix forward%'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d fix-forward tasks", n)
	}
}

func TestPostMergeCancelledGoalStaysCancelled(t *testing.T) {
	r, s, f := pmRunner(t)
	if _, err := s.db.Exec(FactorySchema); err != nil { // adds the alert feed table
		t.Fatal(err)
	}
	if err := s.SetGoalStatus("G-001", "cancelled"); err != nil {
		t.Fatal(err)
	}
	f.rc = 1
	pmMerge(t, r, pmRun("T-0001", "www"), strings.Repeat("b", 40))
	pmCheck(r)
	var status string
	if err := s.db.QueryRow(`SELECT status FROM goals WHERE id='G-001'`).Scan(&status); err != nil || status != "cancelled" {
		t.Errorf("goal: %q %v", status, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='post-merge-stuck'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("alert rows: %d %v", n, err)
	}
}
