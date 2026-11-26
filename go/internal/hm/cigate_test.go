package hm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	headA = strings.Repeat("a", 40)
	headC = strings.Repeat("c", 40)
)

// localCIFake is a runner on project www with a gate_cmd, a recording gh and a clone whose HEAD follows checkouts.
type localCIFake struct {
	r        *Runner
	s        *SQLite
	run      *Run
	clone    string   // the task clone's HEAD
	gateHead string   // HEAD of the detached gate worktree
	gateDir  string   // its path while it exists
	moved    []string // checkouts in the task clone
	prHead   string   // the PR's head on GitHub
	checks   string   // statusCheckRollup of the PR
	gateRC   int      // gate_cmd exit code
	gateAt   []string // HEAD of the directory of each gate_cmd run
	gh       []string
	comments []string
}

func newLocalCIFake(t *testing.T, ciGate, gateCmd string) *localCIFake {
	t.Helper()
	r, s := testRunner(t, "")
	for _, q := range []string{
		`ALTER TABLE projects ADD COLUMN gate_cmd TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN gate_timeout_s INTEGER NOT NULL DEFAULT 3600`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE projects SET gate_cmd=?, ci_gate=? WHERE id='www'`, gateCmd, ciGate); err != nil {
		t.Fatal(err)
	}
	f := &localCIFake{r: r, s: s, clone: headA, prHead: headA, checks: `[]`}
	r.Tools.Git = func(dir string, args ...string) (int, string, string) {
		switch args[0] {
		case "rev-parse":
			if dir == f.gateDir {
				return 0, f.gateHead, ""
			}
			return 0, f.clone, ""
		case "checkout":
			f.moved = append(f.moved, strings.Join(args, " "))
		case "worktree":
			switch args[1] {
			case "add":
				f.gateDir, f.gateHead = args[len(args)-2], args[len(args)-1]
			case "remove":
				f.gateDir = ""
			}
		}
		return 0, "", ""
	}
	r.Tools.Sh = func(dir string, _ time.Duration, _ []string, a ...string) (int, string) {
		if len(a) == 3 && a[2] == gateCmd {
			at := "outside the gate worktree: " + dir
			if dir == f.gateDir {
				at = f.gateHead
			}
			f.gateAt = append(f.gateAt, at)
			return f.gateRC, "building\nzz-gate-output-line\n"
		}
		return 0, ""
	}
	r.Tools.GH = func(stdin string, args ...string) (int, string, string) {
		f.gh = append(f.gh, strings.Join(args, " "))
		switch {
		case len(args) > 1 && args[1] == "view":
			return 0, `{"number":1,"state":"OPEN","headRefOid":"` + f.prHead + `","isDraft":false,"mergeable":"MERGEABLE",` +
				`"statusCheckRollup":` + f.checks + `}`, ""
		case len(args) > 1 && args[1] == "comment":
			f.comments = append(f.comments, stdin)
		}
		return 0, "", ""
	}
	wt := t.TempDir()
	_ = os.MkdirAll(filepath.Join(wt, ".hivemill"), 0o755)
	f.run = &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: wt,
		Branch: "factory/t-0001", Agent: "claude-task-t-0001", State: Gated, Round: 1, Head: headA,
		PRURL: "https://github.com/zz-test/www/pull/1"}
	return f
}

func (f *localCIFake) factoryCfg(t *testing.T, key, value string) {
	t.Helper()
	if _, err := f.s.db.Exec(`INSERT OR REPLACE INTO config(key, value) VALUES(?, ?)`, key, value); err != nil {
		t.Fatal(err)
	}
}

func TestCIGateNoneSkipsChecks(t *testing.T) {
	f := newLocalCIFake(t, "none", "make test")
	f.factoryCfg(t, "required_checks", "build")
	// a PR check that failed is not read
	f.checks = `[{"__typename":"CheckRun","name":"other","status":"COMPLETED","conclusion":"FAILURE"}]`
	f.r.gated(f.run)
	if len(f.gateAt) != 1 || f.run.CIRounds != 0 || f.run.JudgeHead != headA {
		t.Fatalf("gate runs %v, ci rounds %d, judge head %q (%s)", f.gateAt, f.run.CIRounds, f.run.JudgeHead, f.run.FailReason)
	}
	_ = f.s.Verdict(f.run.Task, f.run.PRURL, headA, "pass", "ok", "judge")
	in, _, err := f.r.gatherMerge(f.run)
	if err != nil {
		t.Fatal(err)
	}
	if in.CI != "green" || len(in.MissingChecks) != 0 {
		t.Fatalf("ci %q missing %v", in.CI, in.MissingChecks)
	}
	// the other merge checks still decide
	in.Tier, in.IdentityOK, in.PublicTextOK, in.MainRun = "T1", true, true, "none"
	if d := Decide(in); d.Action != "merge" {
		t.Errorf("decision %+v", d)
	}
	in.IdentityOK = false
	if d := Decide(in); d.Action != "block" {
		t.Errorf("identity must still block: %+v", d)
	}
}

func TestCIGateNoneRerunsOnNewHead(t *testing.T) {
	f := newLocalCIFake(t, "none", "make test")
	f.run.LocalCI = &LocalCI{Head: headA}
	// before the judge: the PR head moved after the gate ran
	f.prHead = headC
	f.r.gated(f.run)
	if strings.Join(f.gateAt, ",") != headC || f.run.LocalCI.Head != headC || f.run.JudgeHead != headC {
		t.Fatalf("gate runs %v, result %+v, judge head %q", f.gateAt, f.run.LocalCI, f.run.JudgeHead)
	}
	// gate_cmd never runs in the task clone and its worktree does not stay
	if len(f.moved) != 0 || f.gateDir != "" {
		t.Errorf("checkouts in the clone %v, gate worktree left %q", f.moved, f.gateDir)
	}
	// before the merge: a head without a result gets one, and nothing merges in that pass
	f.run.LocalCI, f.run.State, f.gateAt = &LocalCI{Head: headA}, Merging, nil
	_ = f.s.Verdict(f.run.Task, f.run.PRURL, headC, "pass", "ok", "judge")
	f.r.Merge(f.run)
	if strings.Join(f.gateAt, ",") != headC || f.run.LocalCI.Head != headC || f.run.State != Merging || merged(f.gh) {
		t.Fatalf("gate runs %v, result %+v, state %s, hold %q, gh %v", f.gateAt, f.run.LocalCI, f.run.State, f.run.MergeHold, f.gh)
	}
	// the same head is not run twice, and the dry run never runs it
	f.r.Merge(f.run)
	f.prHead = headA
	if _, d, err := f.r.MergeDecision(f.run); err != nil || len(f.gateAt) != 1 {
		t.Fatalf("gate runs %v, %+v %v", f.gateAt, d, err)
	}
	// red at merge time counts as a CI round too
	h := newLocalCIFake(t, "none", "make test")
	h.run.LocalCI, h.run.State, h.run.CIRounds = &LocalCI{Head: headA, RC: 2}, Merging, 99
	_ = h.s.Verdict(h.run.Task, h.run.PRURL, headA, "pass", "ok", "judge")
	h.r.Merge(h.run)
	if h.run.State != Failed || !strings.Contains(h.run.FailReason, "gate_cmd exit 2") {
		t.Errorf("merge: state %s %q hold %q", h.run.State, h.run.FailReason, h.run.MergeHold)
	}
	// a red result on the new head resumes the task session instead of judging
	g := newLocalCIFake(t, "none", "make test")
	g.gateRC, g.run.CIRounds = 2, 99
	g.r.gated(g.run)
	if g.run.State != Failed || !strings.Contains(g.run.FailReason, "gate_cmd exit 2") || len(g.run.Judges) != 0 {
		t.Errorf("state %s %q judges %v", g.run.State, g.run.FailReason, g.run.Judges)
	}
}

func TestCIGateNoneNeedsGateCmd(t *testing.T) {
	f := newLocalCIFake(t, "none", "")
	_, _ = f.s.db.Exec(`UPDATE tasks SET status='todo' WHERE id='T-0001'`)
	err := f.r.Start("T-0001")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no gate_cmd") {
		t.Fatalf("dispatch: %v", err)
	}
	if run, _ := f.s.Run("T-0001"); run != nil {
		t.Errorf("a refused task has no run: %+v", run)
	}
	// a run that exists already is never merged untested either
	rec := f.r.Gate(f.run)
	if !strings.Contains(strings.Join(rec.Problems, "\n"), "never merged untested") {
		t.Errorf("gate problems %q", rec.Problems)
	}
	f.r.gated(f.run)
	if f.run.State != Gated || !strings.Contains(f.run.MergeHold, "never merged untested") {
		t.Errorf("state %s hold %q", f.run.State, f.run.MergeHold)
	}
}

func TestCIGateNonePostsExitCode(t *testing.T) {
	f := newLocalCIFake(t, "none", "make test")
	terms := filepath.Join(t.TempDir(), "terms")
	_ = os.WriteFile(terms, []byte("zz-never-matches-term\n"), 0o600)
	f.factoryCfg(t, "scrub_terms_file", terms)
	f.run.State = Merging
	_ = f.s.Verdict(f.run.Task, f.run.PRURL, headA, "pass", "ok", "judge")
	f.run.JudgeHead = headA
	f.r.Merge(f.run)
	f.r.postLocalCI(f.run)
	if len(f.comments) != 1 {
		t.Fatalf("one comment per head, got %q", f.comments)
	}
	for _, want := range []string{"exit 0 on head " + headA, "    zz-gate-output-line"} {
		if !strings.Contains(f.comments[0], want) {
			t.Errorf("comment lacks %q: %q", want, f.comments[0])
		}
	}
	// a new head gets its own comment with its own exit code
	f.run.LocalCI = &LocalCI{Head: headC, RC: 3, Tail: "boom"}
	f.r.postLocalCI(f.run)
	if len(f.comments) != 2 || !strings.Contains(f.comments[1], "exit 3 on head "+headC) {
		t.Errorf("comments %q", f.comments)
	}
	// without the public-text guard the log stays off the PR, the exit code does not
	f.factoryCfg(t, "scrub_terms_file", "")
	t.Setenv("SCRUB_TERMS_FILE", "")
	f.run.LocalCI = &LocalCI{Head: headA, RC: 1, Tail: "unchecked-output"}
	f.r.postLocalCI(f.run)
	if c := f.comments[len(f.comments)-1]; !strings.Contains(c, "exit 1") || strings.Contains(c, "unchecked-output") {
		t.Errorf("comment %q", c)
	}
}

func TestCIGatePRUnchanged(t *testing.T) {
	for _, mode := range []string{"", "pr"} {
		f := newLocalCIFake(t, mode, "make test")
		f.factoryCfg(t, "required_checks", "build")
		f.r.gated(f.run)
		if f.run.State != Gated || len(f.run.Judges) != 0 || len(f.gateAt) != 0 || len(f.comments) != 0 {
			t.Fatalf("%q: no checks yet is pending: state %s, gate runs %v", mode, f.run.State, f.gateAt)
		}
		// a recorded local result does not stand in for the PR checks
		f.run.LocalCI = &LocalCI{Head: headA}
		in, _, err := f.r.gatherMerge(f.run)
		if err != nil || in.CI != "pending" || strings.Join(in.MissingChecks, ",") != "build" {
			t.Errorf("%q: ci %q missing %v %v", mode, in.CI, in.MissingChecks, err)
		}
	}
}

func TestCIGateNoneEdges(t *testing.T) {
	f := newLocalCIFake(t, "none", "make test")
	// eval kickoffs carry no step for opening a PR
	f.run.CINone = true
	task := &Task{ID: "T-0001"}
	if k := EvalReviewKickoff(f.run, task); strings.Contains(k, "open the PR") {
		t.Errorf("eval kickoff %q", k)
	}
	// one huge last line leaves a note, not an empty tail
	if got := f.r.commentTail(strings.Repeat("x", 10000)); !strings.Contains(got, "gate-cmd.log") {
		t.Errorf("tail %q", got)
	}
	f.r.keepAlive(time.Hour)()
	if f.s.Meta("heartbeat") != "" {
		t.Error("a heartbeat outside the daemon")
	}
	f.r.inDaemon = true
	f.r.keepAlive(time.Hour)()
	if f.s.Meta("heartbeat") == "" {
		t.Error("no heartbeat during a gate")
	}
	// a killed gate_cmd is red, so ci_rounds_max bounds it
	f.gateRC = -1
	if err := f.r.localCI(f.run, headA); err != nil || f.run.LocalCI == nil || f.run.LocalCI.RC != -1 {
		t.Errorf("killed: %v %+v", err, f.run.LocalCI)
	}
	f.run.LocalCI = nil
	// a merged run is not cancelled by a late stop, and merge by hand only acts on a merging run
	f.run.State = Merged
	_ = f.s.PutRun(f.run)
	if err := f.r.Stop(f.run.Task); !errors.Is(err, ErrRefused) {
		t.Errorf("stop: %v", err)
	}
	if _, err := f.r.MergeTask(f.run.Task); !errors.Is(err, ErrRefused) {
		t.Errorf("merge: %v", err)
	}
	f.run.State = Gated
	// an override that cannot be read holds: no merge decision from the PR checks
	if _, err := f.s.db.Exec(`ALTER TABLE projects DROP COLUMN ci_gate`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.r.gatherMerge(f.run); err == nil {
		t.Error("gatherMerge decided without ci_gate")
	}
	f.r.gated(f.run)
	if f.run.State != Gated || len(f.run.Judges) != 0 || f.run.MergeHold == "" {
		t.Errorf("state %s hold %q", f.run.State, f.run.MergeHold)
	}
}

func TestCIGateProjectOverride(t *testing.T) {
	for _, c := range []struct {
		project, factory string
		none             bool
	}{
		{"", "", false},
		{"", "pr", false},
		{"", "none", true},
		{"", " None ", true},
		{"pr", "none", false},
		{"none", "pr", true},
		{"none", "", true},
	} {
		f := newLocalCIFake(t, c.project, "make test")
		f.factoryCfg(t, "judge_ci_gate", c.factory)
		if got := f.r.ciNone("www"); got != c.none {
			t.Errorf("project %q factory %q: none=%v", c.project, c.factory, got)
		}
	}
	// a factory DB from before the column gets it on open, with the factory key in charge
	f := newLocalCIFake(t, "", "make test")
	if _, err := f.s.db.Exec(`ALTER TABLE projects DROP COLUMN ci_gate`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := addCIGate(f.s.db); err != nil {
			t.Fatal(err)
		}
	}
	var v string
	if err := f.s.db.QueryRow(`SELECT ci_gate FROM projects WHERE id='www'`).Scan(&v); err != nil || v != "" {
		t.Errorf("ci_gate %q %v", v, err)
	}
}
