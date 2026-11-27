package hm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"factory/go/internal/gitx"
)

type flowPR struct {
	head, mergeable, state, checks string // checks: the rollup context nodes, comma separated
	files                          []string
}

// flowFake is a runner whose gh answers the pass's one query from prs and base.
type flowFake struct {
	r     *Runner
	s     *SQLite
	prs   map[int]*flowPR
	base  []string // history nodes of main, newest first
	main  string   // origin/main
	calls []string // every gh call, "api graphql" or the whole command
	repo  string   // a real repository: the tier and public-text checks run git themselves
}

func flowRunner(t *testing.T) *flowFake {
	t.Helper()
	r, s := testRunner(t, "")
	f := &flowFake{r: r, s: s, prs: map[int]*flowPR{}, main: "m0"}
	if _, err := s.db.Exec(`CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL, kind TEXT NOT NULL,
		ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, closed TEXT, sent TEXT);
		INSERT INTO config(key,value) VALUES('noreply_email','1+zz@users.noreply.github.com')`); err != nil {
		t.Fatal(err)
	}
	r.Tools.GH = f.gh
	r.Tools.Git = f.git
	f.repo = t.TempDir()
	mustGit(t, f.repo, "init", "-q", "-b", "main")
	f.commit(t, "README")
	mustGit(t, f.repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	terms := filepath.Join(t.TempDir(), "terms")
	if err := os.WriteFile(terms, []byte("zz-private-term\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO config(key,value) VALUES('scrub_terms_file',?)`, terms); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *flowFake) commit(t *testing.T, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo, file), []byte(file+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, f.repo, "add", file)
	mustGit(t, f.repo, "-c", "user.name=zz", "-c", "user.email=1+zz@users.noreply.github.com", "commit", "-q", "-m", "add "+file)
}

func (f *flowFake) gh(_ string, args ...string) (int, string, string) {
	cmd := strings.Join(args, " ")
	if strings.HasPrefix(cmd, "api graphql") {
		f.calls = append(f.calls, "api graphql")
		return 0, f.answer(), ""
	}
	f.calls = append(f.calls, cmd)
	switch {
	case len(args) > 2 && args[0] == "pr" && args[1] == "merge":
		n := prNumber(args[2])
		f.prs[n].state = "MERGED"
		f.main = fmt.Sprintf("sha%d", n)
		f.base = append([]string{baseNode(f.main, "IN_PROGRESS", "")}, f.base...)
		return 0, "", ""
	case len(args) > 2 && args[0] == "pr" && args[1] == "view":
		n := prNumber(args[2])
		return 0, fmt.Sprintf(`{"number":%d,"state":"%s","headRefOid":"%s","mergeCommit":{"oid":"sha%d"},"url":"%s"}`,
			n, f.prs[n].state, f.prs[n].head, n, args[2]), ""
	}
	return 0, "", ""
}

func (f *flowFake) git(_ string, args ...string) (int, string, string) {
	switch args[0] {
	case "rev-parse":
		if args[len(args)-1] == "origin/main" {
			return 0, f.main, ""
		}
		return 0, "local", ""
	case "diff":
		for _, p := range f.prs {
			if strings.HasSuffix(args[len(args)-1], "..."+p.head) {
				return 0, strings.Join(p.files, "\n"), ""
			}
		}
	}
	return 0, "", ""
}

func prNumber(url string) int {
	n, _ := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	return n
}

func (f *flowFake) answer() string {
	repo := []string{fmt.Sprintf(`"b0":{"branchProtectionRule":null,"target":{"history":{"nodes":[%s]}}}`, strings.Join(f.base, ","))}
	for n, p := range f.prs {
		repo = append(repo, fmt.Sprintf(`"p%d":{"number":%d,"state":"%s","mergeable":"%s","headRefOid":"%s","url":"%s","title":"t","body":"b",`+
			`"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false},"nodes":[%s]}}}}]}}`,
			n, n, p.state, p.mergeable, p.head, prLink(n), p.checks))
	}
	return `{"data":{"r0":{` + strings.Join(repo, ",") + `}}}`
}

func prLink(n int) string { return fmt.Sprintf("https://github.com/zz-test/www/pull/%d", n) }

// check is one rollup node of Actions run id.
func check(name, status, conclusion string, id int) string {
	return fmt.Sprintf(`{"__typename":"CheckRun","name":"%s","status":"%s","conclusion":"%s","startedAt":"%s",`+
		`"completedAt":"%s","detailsUrl":"https://github.com/zz-test/www/actions/runs/%d/job/%d",`+
		`"checkSuite":{"workflowRun":{"workflow":{"name":"tests"}}}}`, name, status, conclusion, time.Now().Add(-3*time.Hour).Format(time.RFC3339),
		map[bool]string{true: "2026-11-26T10:05:00Z"}[status == "COMPLETED"], id, id*10)
}

// baseNode is a base commit with its tests.yml push run.
func baseNode(oid, status, conclusion string) string {
	jobs, _ := json.Marshal([]baseJob{{Name: "unit", Conclusion: conclusion, CompletedAt: time.Now().Add(-time.Hour).Format(time.RFC3339)}})
	return fmt.Sprintf(`{"oid":"%s","checkSuites":{"nodes":[{"status":"%s","conclusion":"%s","workflowRun":{"event":"push",`+
		`"workflow":{"name":"tests","resourcePath":"/zz-test/www/actions/workflows/tests.yml"}},"checkRuns":{"nodes":%s}}]}}`,
		oid, status, conclusion, jobs)
}

// add stores a run in state with PR number n.
func (f *flowFake) add(t *testing.T, n int, state, checks string) *Run {
	t.Helper()
	f.prs[n] = &flowPR{head: fmt.Sprintf("head%d", n), mergeable: "MERGEABLE", state: "OPEN", checks: checks, files: []string{fmt.Sprintf("f%d", n)}}
	run := &Run{Task: fmt.Sprintf("T-%04d", n), Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Branch: "factory/x",
		State: state, Round: 1, Head: f.prs[n].head, PRURL: prLink(n), Worktree: t.TempDir()}
	f.put(t, run)
	if _, err := f.s.db.Exec(`INSERT OR IGNORE INTO tasks(id,goal_id,title,spec,acceptance,status) VALUES(?,'G-001','zz-test','spec','','in_progress')`,
		run.Task); err != nil {
		t.Fatal(err)
	}
	return run
}

// judged adds a run at the merge step, judged at ts.
func (f *flowFake) judged(t *testing.T, n int, ts string, files ...string) {
	t.Helper()
	run := f.add(t, n, Merging, check("unit", "COMPLETED", "SUCCESS", n))
	f.prs[n].files = files
	mustGit(t, f.repo, "checkout", "-q", "-B", fmt.Sprintf("pr%d", n), "refs/remotes/origin/main")
	f.commit(t, fmt.Sprintf("pr%d.txt", n))
	rc, head, errs := gitx.RC(f.repo, gitx.Options{Home: f.r.Env.Home}, "rev-parse", "HEAD")
	if rc != 0 {
		t.Fatal(errs)
	}
	mustGit(t, f.repo, "checkout", "-q", "main")
	f.prs[n].head = strings.TrimSpace(head)
	run.Head, run.Worktree = f.prs[n].head, f.repo
	run.Verdicts = []Verdict{{Verdict: VerdictMerge, TS: ts}}
	f.put(t, run)
	if err := f.s.Verdict(run.Task, run.PRURL, run.Head, "pass", "ok", "task-judge"); err != nil {
		t.Fatal(err)
	}
}

func (f *flowFake) put(t *testing.T, run *Run) {
	t.Helper()
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
}

func (f *flowFake) run(t *testing.T, n int) *Run {
	t.Helper()
	run, err := f.s.Run(fmt.Sprintf("T-%04d", n))
	if err != nil || run == nil {
		t.Fatal("no run", err)
	}
	return run
}

func (f *flowFake) pass(t *testing.T) {
	t.Helper()
	if err := f.r.Pass(false); err != nil {
		t.Fatal(err)
	}
}

// age moves the run's flow entry back.
func (f *flowFake) age(t *testing.T, n int, d time.Duration) {
	t.Helper()
	run := f.run(t, n)
	if run.Flow == nil {
		t.Fatalf("run %d has no flow state", n)
	}
	run.Flow.Since = time.Now().Add(-d).Format(time.RFC3339)
	f.put(t, run)
}

// ghCalls returns the recorded calls that start with prefix.
func (f *flowFake) ghCalls(prefix string) (out []string) {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func TestPRFlowSingleBatchedPoll(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("m0", "COMPLETED", "SUCCESS")}
	f.add(t, 1, Gated, check("unit", "QUEUED", "", 1))
	f.add(t, 2, Gated, check("unit", "IN_PROGRESS", "", 2))
	f.add(t, 3, Held, "")
	f.pass(t)
	if !slices.Equal(f.calls, []string{"api graphql"}) {
		t.Fatalf("one pass must make one gh call, got %q", f.calls)
	}
	for n, want := range map[int]string{1: "ci_queued 0/40", 2: "ci_running 0/", 3: "held_owner 0/"} {
		if got := f.run(t, n).Flow; got == nil || !strings.HasPrefix(got.String(), want) {
			t.Errorf("PR %d flow %v, want %q", n, got, want)
		}
	}
	if got := f.s.Meta(ghKey(time.Now())); got != "1" {
		t.Errorf("gh calls counted: %q", got)
	}
}

func TestPRFlowCIRunningTimeoutReruns(t *testing.T) {
	f := flowRunner(t)
	f.add(t, 1, Gated, check("unit", "IN_PROGRESS", "", 7))
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	f.pass(t)
	if got := f.ghCalls("run "); !slices.Equal(got, []string{"run cancel 7 -R zz-test/www"}) {
		t.Fatalf("first overrun: %q", got)
	}
	f.prs[1].checks = check("unit", "COMPLETED", "CANCELLED", 7)
	for range 2 { // re-run once, and the cancelled result does not resume the session
		run := f.run(t, 1)
		run.CI.Polled = 0
		f.put(t, run)
		f.pass(t)
	}
	if got := f.ghCalls("run rerun"); !slices.Equal(got, []string{"run rerun 7 -R zz-test/www"}) {
		t.Fatalf("re-run: %q", got)
	}
	if run := f.run(t, 1); run.State != Gated {
		t.Fatalf("run %s while its checks are re-run", run.State)
	}
	f.prs[1].checks = strings.Replace(check("unit", "IN_PROGRESS", "", 7), "job/70", "job/71", 1)
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	f.pass(t)
	if run := f.run(t, 1); run.State != Failed || run.FailReason != "CI timed out" {
		t.Errorf("second overrun: %s %q", run.State, run.FailReason)
	}
	if got := f.ghCalls("run cancel"); len(got) != 1 {
		t.Errorf("cancelled again: %q", got)
	}
}

func TestPRFlowTrainRefusesUnknownRiders(t *testing.T) {
	f := flowRunner(t)
	b := &basePoll{}
	f.r.poll = &prPoll{bases: map[string]*basePoll{baseKey("zz-test/www", "main"): b}}
	rides := func(car []string) bool {
		f.r.setTrain("zz-test/www", "main", train{Cars: []trainCar{{SHA: "sha1", Files: car}}})
		_, ride := f.r.trainState("zz-test/www", "main", "sha1", "pending", []string{"b"})
		return ride
	}
	if !rides([]string{"a"}) {
		t.Fatal("a disjoint PR does not ride")
	}
	if rides(nil) {
		t.Error("rode behind a merge with no file list")
	}
	b.Commits = []baseCommit{{Oid: "sha1", Suites: []baseSuite{{Status: "COMPLETED", Conclusion: "CANCELLED", Event: "push",
		Path: "/zz-test/www/actions/workflows/tests.yml"}}}}
	if got := b.pushState("sha1"); got != "cancelled" || rides([]string{"a"}) {
		t.Errorf("rode on a cancelled head (%q)", got)
	}
}

func TestPRFlowRefusedCancelStillEscalates(t *testing.T) {
	f := flowRunner(t)
	f.r.Tools.GH = func(dir string, args ...string) (int, string, string) {
		rc, out, errs := f.gh(dir, args...)
		if len(args) > 1 && args[0] == "run" && args[1] == "cancel" {
			return 1, "", "zz: server error"
		}
		return rc, out, errs
	}
	f.add(t, 1, Gated, check("unit", "IN_PROGRESS", "", 7))
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	f.pass(t)
	if run := f.run(t, 1); len(run.Rerun.Pending)+len(run.Rerun.Masked) != 0 {
		t.Fatalf("masked a run that was not cancelled: %+v", run.Rerun)
	}
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	f.pass(t)
	if run := f.run(t, 1); run.State != Failed || run.FailReason != "CI timed out" {
		t.Errorf("second overrun: %s %q", run.State, run.FailReason)
	}
}

func TestPRFlowFlakyRerunOnce(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("m0", "COMPLETED", "SUCCESS")} // "unit" passed on main an hour ago
	f.add(t, 1, Gated, check("unit", "COMPLETED", "FAILURE", 7))
	f.pass(t)
	if got := f.ghCalls("run rerun"); !slices.Equal(got, []string{"run rerun 7 --failed -R zz-test/www"}) {
		t.Fatalf("flaky re-run: %q", got)
	}
	if run := f.run(t, 1); run.State != Gated || run.Round != 1 {
		t.Fatalf("after the re-run: %s round %d", run.State, run.Round)
	}
	// the second red is a new job of the same run: the session is resumed, nothing is re-run
	f.prs[1].checks = strings.Replace(check("unit", "COMPLETED", "FAILURE", 7), "job/70", "job/71", 1)
	run := f.run(t, 1)
	run.CI.Polled = 0
	f.put(t, run)
	f.pass(t)
	if got := f.ghCalls("run rerun"); len(got) != 1 {
		t.Errorf("re-run twice: %q", got)
	}
	if ev := eventsOf(t, f.r, "ci.red"); len(ev) != 1 {
		t.Errorf("the second red must take the CI-red path: %d ci.red events, run %s", len(ev), f.run(t, 1).State)
	}
}

// a T3 auto-merge run gets the flaky re-run before the hard limits; a red that stays holds it for the owner
func TestPRFlowT3FlakyRerunBeforeLimits(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("m0", "COMPLETED", "SUCCESS")}
	f.judged(t, 1, "2026-11-26T10:00:00Z", "go/internal/shlex/zz.go")
	mustGit(t, f.repo, "checkout", "-q", "pr1")
	if err := os.MkdirAll(filepath.Join(f.repo, "go/internal/shlex"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.commit(t, "go/internal/shlex/zz.go") // a T3 path
	rc, head, errs := gitx.RC(f.repo, gitx.Options{Home: f.r.Env.Home}, "rev-parse", "HEAD")
	if rc != 0 {
		t.Fatal(errs)
	}
	mustGit(t, f.repo, "checkout", "-q", "main")
	run := f.run(t, 1)
	run.Head = strings.TrimSpace(head)
	f.prs[1].head = run.Head
	f.put(t, run)
	if err := f.s.Verdict(run.Task, run.PRURL, run.Head, "pass", "ok", "task-judge"); err != nil {
		t.Fatal(err)
	}
	f.prs[1].checks = check("unit", "COMPLETED", "FAILURE", 7)
	f.pass(t)
	if got := f.ghCalls("run rerun"); len(got) != 1 {
		t.Fatalf("flaky re-run: %q, %s %q", got, f.run(t, 1).State, f.run(t, 1).MergeHold)
	}
	if run := f.run(t, 1); run.State != Merging {
		t.Fatalf("after the re-run: %s %q", run.State, run.MergeHold)
	}
	f.prs[1].checks = strings.Replace(f.prs[1].checks, "job/70", "job/71", 1)
	f.pass(t)
	if run := f.run(t, 1); run.State != Held || !strings.Contains(run.MergeHold, "T3 hard limits: e)") {
		t.Errorf("a red that stays: %s %q", run.State, run.MergeHold)
	}
}

func TestPRFlowMergeTrain(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("m0", "COMPLETED", "SUCCESS")}
	// judged order is 4, 2, 3, 1, 5; merge_train 3 lets three ride behind the first
	for n, ts := range map[int]string{1: "10:03", 2: "10:01", 3: "10:02", 4: "10:00", 5: "10:04"} {
		f.judged(t, n, "2026-11-26T"+ts+":00Z", fmt.Sprintf("f%d", n))
	}
	for range 3 { // the head moves with each merge, as the next pass reads it
		f.pass(t)
	}
	var got []string
	for _, c := range f.ghCalls("pr merge") {
		got = append(got, strconv.Itoa(prNumber(strings.Fields(c)[2])))
	}
	if !slices.Equal(got, []string{"4", "2", "3", "1"}) {
		t.Fatalf("merge order %v, want 4 2 3 1 and 5 waiting; %q %s", got, f.run(t, 4).MergeHold, f.run(t, 4).State)
	}
	if run := f.run(t, 5); run.Flow == nil || run.Flow.State != FlowMainWait {
		t.Errorf("PR 5 flow %v", run.Flow)
	}
	// a red push run of an earlier car stops the train
	f.base[len(f.base)-2] = baseNode("sha4", "COMPLETED", "FAILURE")
	f.pass(t)
	if len(f.ghCalls("pr merge")) != 4 || !strings.Contains(f.run(t, 5).MergeHold, "failed") {
		t.Errorf("after a red push run: %d merges, hold %q", len(f.ghCalls("pr merge")), f.run(t, 5).MergeHold)
	}
	if sha := f.r.culprit("zz-test/www", "main", "sha1"); sha != "sha1" {
		t.Errorf("outside a pass the head is blamed, got %s", sha)
	}
	f.r.poll = f.r.pollPRs([]Run{*f.run(t, 5)})
	if sha := f.r.culprit("zz-test/www", "main", "sha1"); sha != "sha4" {
		t.Errorf("culprit %s, want the first red car", sha)
	}
}

func TestPRFlowMergeTrainStopsOnSharedFile(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("m0", "COMPLETED", "SUCCESS")}
	f.judged(t, 1, "2026-11-26T10:00:00Z", "a.go")
	f.judged(t, 2, "2026-11-26T10:01:00Z", "b.go", "a.go")
	f.pass(t)
	if got := f.ghCalls("pr merge"); len(got) != 1 || !strings.Contains(got[0], "/pull/1 ") {
		t.Fatalf("merges while a shared file waits: %q", got)
	}
	f.base[0] = baseNode("sha1", "COMPLETED", "SUCCESS")
	f.pass(t)
	if got := f.ghCalls("pr merge"); len(got) != 2 {
		t.Errorf("after main is green: %q", got)
	}
}

func TestPRFlowStaleEvent(t *testing.T) {
	f := flowRunner(t)
	if err := f.s.SetCfg("ask_alert_h", "48"); err != nil {
		t.Fatal(err)
	}
	f.add(t, 1, Held, "")
	f.pass(t)
	f.age(t, 1, 100*time.Minute) // past stale_pr_min 90, under ask_alert_h
	f.pass(t)
	f.pass(t)
	ev := eventsOf(t, f.r, "pr.stale")
	if len(ev) != 1 || ev[0].Data["state"] != FlowHeldOwner || ev[0].Data["reason"] == "" {
		t.Fatalf("pr.stale events: %+v", ev)
	}
	sent := 0
	f.r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { sent++; return 0, "" }
	if err := f.s.SetCfg("alert_cmd", "true"); err != nil {
		t.Fatal(err)
	}
	f.age(t, 1, 100*time.Hour)
	f.pass(t)
	f.pass(t)
	if sent != 1 || len(eventsOf(t, f.r, "pr.held_alert")) != 1 {
		t.Errorf("held over its budget: %d alerts, %d events", sent, len(eventsOf(t, f.r, "pr.held_alert")))
	}
}

func TestPRFlowNoRefreshWhileQueueSlow(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("m0", "COMPLETED", "SUCCESS")}
	f.judged(t, 1, "2026-11-26T10:00:00Z", "a.go")
	f.add(t, 2, Gated, check("unit", "QUEUED", "", 2))
	// PR 1 is behind main; its refresh is the merge of main into the branch, which fails here and shows in the hold
	fake := f.r.Tools.Git
	f.r.Tools.Git = func(wt string, args ...string) (int, string, string) {
		if args[0] == "merge-base" {
			return 1, "", ""
		}
		return fake(wt, args...)
	}
	run := f.run(t, 2)
	run.Flow = &FlowState{State: FlowCIQueued, Since: time.Now().Add(-time.Hour).Format(time.RFC3339), Head: run.Head, BudgetS: 2400}
	f.put(t, run)
	f.pass(t)
	f.pass(t)
	got := f.run(t, 1)
	if got.Refreshes != 0 || !strings.HasPrefix(got.MergeHold, "refresh: held") || got.Flow == nil || got.Flow.State != FlowRefresh {
		t.Fatalf("refreshes %d, hold %q, flow %v", got.Refreshes, got.MergeHold, got.Flow)
	}
	if ev := eventsOf(t, f.r, "ci.queue_slow"); len(ev) != 1 {
		t.Errorf("ci.queue_slow events: %d", len(ev))
	}
	if other := f.ghCalls("run "); len(other) != 0 {
		t.Errorf("a slow queue takes no action on GitHub: %q", other)
	}
	run = f.run(t, 1)
	run.Worktree = t.TempDir() // not a repository: gitAs fails
	f.put(t, run)
	f.prs[2].checks = check("unit", "IN_PROGRESS", "", 2)
	f.pass(t)
	if hold := f.run(t, 1).MergeHold; strings.HasPrefix(hold, "refresh: held") {
		t.Errorf("the queue moved: hold %q", hold)
	}
}

func TestPRFlowTimeoutChargesOnlyRunnerTime(t *testing.T) {
	f := flowRunner(t)
	f.add(t, 1, Gated, check("unit", "COMPLETED", "SUCCESS", 7)+","+check("e2e", "QUEUED", "", 7))
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	f.pass(t)
	if got := f.ghCalls("run "); len(got) != 0 {
		t.Fatalf("a job still waiting for a runner was cancelled: %q", got)
	}
	if run := f.run(t, 1); run.Flow.Acted || run.Flow.BudgetS <= 7200 {
		t.Errorf("the budget must move on: %+v", run.Flow)
	}
}

func TestPRFlowQueueSlowNeedsAQueuedJob(t *testing.T) {
	f := flowRunner(t)
	f.add(t, 1, Gated, "")
	f.pass(t)
	f.age(t, 1, time.Hour)
	f.r.poll = f.r.pollPRs([]Run{*f.run(t, 1)})
	if f.r.queuedTooLong([]Run{*f.run(t, 1)}) {
		t.Error("a head with no checks says nothing about the queue")
	}
}

func TestPRFlowTrainCulpritAndLateConfirm(t *testing.T) {
	f := flowRunner(t)
	run := f.add(t, 1, Merging, "")
	run.Car, run.Rode = &trainCar{Files: []string{"a.go"}}, false
	f.r.finish(run, &PR{State: "MERGED", MergeCommit: &struct {
		Oid string `json:"oid"`
	}{Oid: "sha1"}})
	if got := f.r.train("zz-test/www", "main"); len(got.Cars) != 1 || got.Cars[0].SHA != "sha1" || run.Car != nil {
		t.Fatalf("a merge confirmed later must join the train: %+v", got)
	}
	f.r.setTrain("zz-test/www", "main", train{Cars: []trainCar{{SHA: "sha1"}, {SHA: "sha2"}}})
	f.r.poll = &prPoll{bases: map[string]*basePoll{}}
	if got := f.r.culprit("zz-test/www", "main", "sha2"); got != "" {
		t.Errorf("an unread base blames nobody, got %q", got)
	}
	f.base = []string{baseNode("sha2", "COMPLETED", "FAILURE"), baseNode("sha1", "COMPLETED", "CANCELLED")}
	f.add(t, 2, Gated, "")
	f.r.poll = f.r.pollPRs([]Run{*f.run(t, 2)})
	if got := f.r.culprit("zz-test/www", "main", "sha2"); got != "" {
		t.Errorf("an earlier cancelled push run leaves the blame to the bisect task, got %q", got)
	}
}

func TestPRFlowDisabledTestsWorkflowIsNoMainRun(t *testing.T) {
	f := flowRunner(t)
	f.base = []string{baseNode("old", "COMPLETED", "SUCCESS")} // no push run for main's head m0
	f.add(t, 1, Gated, "")
	state := "disabled_manually"
	f.r.Tools.GH = func(dir string, args ...string) (int, string, string) {
		rc, out, errs := f.gh(dir, args...)
		if len(args) > 1 && args[0] == "api" && strings.HasSuffix(args[1], "/actions/workflows/tests.yml") {
			return 0, state + "\n", ""
		}
		return rc, out, errs
	}
	for _, c := range []struct{ state, want string }{{"disabled_manually", "none"}, {"active", "pending"}} {
		state = c.state
		f.r.poll = f.r.pollPRs([]Run{*f.run(t, 1)})
		for range 2 {
			if got := f.r.mainRun("zz-test/www", f.repo, "main"); got != c.want {
				t.Errorf("tests.yml %s: %q, want %q", c.state, got, c.want)
			}
		}
	}
	if got := f.ghCalls("api repos/"); len(got) != 2 {
		t.Errorf("one read per pass: %q", got)
	}
	state = "disabled_manually"
	f.base = []string{baseNode("m0", "COMPLETED", "CANCELLED")}
	f.r.poll = f.r.pollPRs([]Run{*f.run(t, 1)})
	if got := f.r.mainRun("zz-test/www", f.repo, "main"); got != "none" {
		t.Errorf("a cancelled head run of a disabled tests.yml: %q", got)
	}
}

func TestPRFlowShortJobsStillAlert(t *testing.T) {
	f := flowRunner(t)
	young := `"startedAt":"` + time.Now().UTC().Format(time.RFC3339) + `"`
	f.add(t, 1, Gated, regexp.MustCompile(`"startedAt":"[^"]*"`).ReplaceAllString(check("unit", "IN_PROGRESS", "", 7), young))
	f.pass(t)
	run := f.run(t, 1)
	run.CI.Since, run.CI.Polled = time.Now().Add(-2*time.Hour).Unix(), 0
	f.put(t, run)
	f.pass(t)
	if run = f.run(t, 1); run.CI == nil || !run.CI.Alerted {
		t.Errorf("a gated run over twice the budget: %+v", run.CI)
	}
	run.State = Merging
	run.Flow = &FlowState{State: FlowCIRunning, Since: time.Now().Add(-80 * time.Minute).Format(time.RFC3339), Head: run.Head, BudgetS: 4800}
	f.r.poll = f.r.pollPRs([]Run{*run})
	f.r.ciTimedOut(run)
	if run.Flow.Alerted || run.Flow.BudgetS != 5400 {
		t.Errorf("the next look must be at twice the budget: %+v", run.Flow)
	}
	run.Flow.Since = time.Now().Add(-90 * time.Minute).Format(time.RFC3339)
	f.r.ciTimedOut(run)
	if !run.Flow.Alerted {
		t.Errorf("a merging run over twice the budget: %+v", run.Flow)
	}
	if got := f.ghCalls("run "); len(got) != 0 {
		t.Errorf("no job ran over the budget: %q", got)
	}
}

func TestPRFlowHungJobAfterASlowStatus(t *testing.T) {
	f := flowRunner(t)
	status := func(state string) string {
		return `,{"__typename":"StatusContext","context":"ext","state":"` + state + `","targetUrl":"https://zz.invalid/1"}`
	}
	f.add(t, 1, Gated, check("unit", "IN_PROGRESS", "", 7)+status("PENDING"))
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	f.pass(t)
	if got := f.ghCalls("run "); len(got) != 0 {
		t.Fatalf("cancelled while a status is unfinished: %q", got)
	}
	f.prs[1].checks = check("unit", "IN_PROGRESS", "", 7) + status("SUCCESS")
	f.pass(t)
	if got := f.ghCalls("run cancel"); len(got) != 1 {
		t.Errorf("the hung job is still cancelled: %q", got)
	}
}

func TestPRFlowTimeoutOfAJobThatStartedLate(t *testing.T) {
	f := flowRunner(t)
	hourAgo := `"startedAt":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"`
	f.add(t, 1, Gated, regexp.MustCompile(`"startedAt":"[^"]*"`).ReplaceAllString(check("unit", "IN_PROGRESS", "", 7), hourAgo))
	f.pass(t)
	f.age(t, 1, 2*time.Hour)
	run := f.run(t, 1)
	run.Flow.BudgetS, run.CI.Polled = 7000, 0 // moved on while the job waited for a runner
	f.put(t, run)
	f.pass(t)
	if got := f.ghCalls("run cancel"); len(got) != 1 {
		t.Errorf("an hour on a runner is over the budget: %q", got)
	}
}
