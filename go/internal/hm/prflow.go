package hm

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// The PR flow controller (docs/pr-flow.md): the flow state of each run with a PR, its time budget, the one action of an
// overrun, the merge train and the count of gh calls. All of it runs in the pass, from the pass's poll.

// Flow states.
const (
	FlowCIQueued   = "ci_queued"
	FlowCIRunning  = "ci_running"
	FlowJudge      = "judge"
	FlowMergeReady = "merge_ready"
	FlowMainWait   = "main_wait"
	FlowRefresh    = "refresh"
	FlowHeldOwner  = "held_owner"

	flowStageCI = "ci" // Merge's hint: the merge waits on checks (ci_queued or ci_running)

	rerunTries = 5 // `gh run rerun` of a cancelled run, before the real result is read again
)

// FlowState is the run's current flow state (Run.Flow; nil: no PR in flight).
type FlowState struct {
	State, Since, Head string
	BudgetS            int64
	Acted, Stale       bool // the overrun action and the stale event of this entry are done
}

// RerunState is what the controller re-ran on one head (Run.Rerun).
type RerunState struct {
	Head     string
	Overruns int  // ci_running budgets that ran out
	Flaky    bool // the failed jobs were re-run once
	Tries    int
	Pending  []string // cancelled Actions run ids still to re-run
	Masked   []string // check links whose old result reads as pending until the new run replaces them
}

// String is "<state> <minutes>/<budget>".
func (f *FlowState) String() string {
	since, _ := time.Parse(time.RFC3339, f.Since)
	return fmt.Sprintf("%s %d/%d", f.State, int(time.Since(since).Minutes()), f.BudgetS/60)
}

func link(c map[string]any) string {
	if s, _ := c["detailsUrl"].(string); s != "" {
		return s
	}
	s, _ := c["targetUrl"].(string)
	return s
}

func actionsRun(c map[string]any) string {
	if m := actionsLinkRe.FindStringSubmatch(link(c)); m != nil {
		return m[1]
	}
	return ""
}

func rowStr(c map[string]any, k string) string { s, _ := c[k].(string); return s }

// unfinished reports whether a rollup row has no result yet.
func unfinished(c map[string]any) bool {
	v, _ := ciRows([]map[string]any{c})
	return v == "pending"
}

// unfinishedActions returns the unfinished rows and whether there are some and every one is an Actions run.
func unfinishedActions(checks []map[string]any) (rows []map[string]any, all bool) {
	for _, c := range checks {
		if unfinished(c) {
			rows = append(rows, c)
		}
	}
	return rows, len(rows) > 0 && !slices.ContainsFunc(rows, func(c map[string]any) bool { return actionsRun(c) == "" })
}

// maskRows are the PR's checks with the results the controller is re-running read as queued.
func maskRows(rr *RerunState, pr *PR) []map[string]any {
	if rr == nil || rr.Head != pr.HeadRefOid || len(rr.Masked) == 0 {
		return pr.Checks
	}
	out := make([]map[string]any, len(pr.Checks))
	for i, c := range pr.Checks {
		if slices.Contains(rr.Masked, link(c)) {
			c = maps.Clone(c)
			c["status"] = "QUEUED"
			delete(c, "conclusion")
			delete(c, "completedAt")
		}
		out[i] = c
	}
	return out
}

// ciFlow is ci_queued until a check of the head has started; ci_running then stays for that head.
func (r *Runner) ciFlow(run *Run) (state, head string, ok bool) {
	pr, err := r.prView(run)
	if err != nil {
		if run.Flow == nil {
			return FlowCIQueued, run.Head, true
		}
		return "", "", false // unreadable: keep what was known
	}
	head = pr.HeadRefOid
	if f := run.Flow; f != nil && f.State == FlowCIRunning && f.Head == head {
		return FlowCIRunning, head, true
	}
	started := r.ciNone(run.Project) || slices.ContainsFunc(pr.Checks, func(c map[string]any) bool {
		s := strings.ToUpper(rowStr(c, "status"))
		return s == "IN_PROGRESS" || (s == "COMPLETED" && !strings.EqualFold(rowStr(c, "conclusion"), "SKIPPED"))
	})
	if started {
		return FlowCIRunning, head, true
	}
	return FlowCIQueued, head, true
}

// flowOfRun is the flow state the run is in now; "" for none, ok false to keep the one it has.
func (r *Runner) flowOfRun(run *Run) (state, head string, ok bool) {
	if run.Eval || run.PRURL == "" {
		return "", "", true
	}
	switch {
	case heldForOwner(run):
		return FlowHeldOwner, run.Head, true
	case run.State == Gated:
		return r.ciFlow(run)
	case run.State == Judging || run.State == Judged:
		return FlowJudge, run.Head, true
	case run.State == Merging:
		switch run.mergeStage {
		case flowStageCI:
			return r.ciFlow(run)
		case "":
			if run.Flow != nil && run.Flow.State != FlowJudge {
				return "", "", false
			}
			return FlowMergeReady, run.Head, true
		}
		return run.mergeStage, run.Head, true
	}
	return "", "", true
}

func (r *Runner) flowBudget(state string) int64 {
	switch state {
	case FlowCIQueued:
		return int64(cfgInt(r.Store, "ci_queue_max_min")) * 60
	case FlowCIRunning:
		return int64(cfgInt(r.Store, "ci_timeout_s"))
	case FlowHeldOwner:
		return int64(cfgInt(r.Store, "ask_alert_h")) * 3600
	}
	return int64(cfgInt(r.Store, "stale_pr_min")) * 60
}

// enterFlow starts a new entry of a flow state; "" leaves the flow.
func (r *Runner) enterFlow(run *Run, state, head string) {
	if f := run.Flow; f != nil && f.Acted {
		kind := map[string]string{FlowHeldOwner: "hivemill-held", FlowCIRunning: "hivemill-ci"}[f.State]
		if kind != "" {
			if err := r.Store.CloseAlert(kind, run.Task); err != nil {
				r.printf("%s: alert feed: %v\n", run.Task, err)
			}
		}
	}
	var budget int64
	run.Flow = nil
	if state != "" {
		budget = r.flowBudget(state)
		run.Flow = &FlowState{State: state, Since: nowISO(), Head: head, BudgetS: budget}
	}
	_ = r.Log.Emit("pr.flow", run, Event{Data: map[string]any{"state": state, "budget_s": budget, "pr": run.PRURL}})
}

// trackFlow moves the run to its flow state and takes the action of a budget that ran out.
func (r *Runner) trackFlow(run *Run) {
	state, head, ok := r.flowOfRun(run)
	if ok {
		f := run.Flow
		ci := state == FlowCIQueued || state == FlowCIRunning
		switch {
		case f == nil && state == "":
		case f == nil || f.State != state || (ci && f.Head != head):
			r.enterFlow(run, state, head)
		}
	}
	f := run.Flow
	if f == nil {
		return
	}
	since, err := time.Parse(time.RFC3339, f.Since)
	if err != nil {
		return
	}
	age := int64(time.Since(since).Seconds())
	if !f.Stale && age >= int64(cfgInt(r.Store, "stale_pr_min"))*60 {
		f.Stale = true
		reason := f.State
		if run.MergeHold != "" {
			reason = truncate(run.MergeHold, 300)
		}
		_ = r.Log.Emit("pr.stale", run, Event{Data: map[string]any{"state": f.State, "reason": reason, "minutes": age / 60, "pr": run.PRURL}})
	}
	if f.Acted || age < f.BudgetS {
		return
	}
	switch f.State {
	case FlowCIQueued:
		f.Acted = true
		_ = r.Log.Emit("ci.queue_slow", run, Event{Data: map[string]any{"head": f.Head, "limit_s": f.BudgetS, "pr": run.PRURL}})
	case FlowCIRunning:
		r.ciTimedOut(run)
	case FlowHeldOwner:
		f.Acted = true
		text := fmt.Sprintf("%s: held for the owner for over %s on %s", run.Task, time.Duration(f.BudgetS)*time.Second, run.PRURL)
		if err := r.Store.AlertFeed("hivemill-held", run.Task, run.PRURL); err != nil {
			r.printf("%s: alert feed: %v\n", run.Task, err)
		}
		r.ownerAlert("hivemill-held", text)
		_ = r.Log.Emit("pr.held_alert", run, Event{Data: map[string]any{"limit_s": f.BudgetS, "pr": run.PRURL}})
	}
}

// ciTimedOut is the ci_running overrun: the first one cancels the unfinished Actions runs to re-run them, the second
// one fails the run.
func (r *Runner) ciTimedOut(run *Run) {
	pr, err := r.prView(run)
	if err != nil {
		return // the next pass
	}
	if rr := run.Rerun; rr != nil && rr.Head == pr.HeadRefOid && rr.Overruns > 0 {
		r.fail(run, "CI timed out")
		r.enterFlow(run, "", "")
		return
	}
	rows, actions := unfinishedActions(pr.Checks)
	if !actions {
		run.Flow.Acted = true
		if run.State == Merging && len(rows) > 0 { // a gated run is alerted by its CI wait (ciOverdue)
			if err := r.Store.AlertFeed("hivemill-ci", run.Task, run.PRURL); err != nil {
				r.printf("%s: alert feed: %v\n", run.Task, err)
			}
			r.ownerAlert("hivemill-ci", run.Task+": checks that cannot be re-run are over their budget on "+run.PRURL)
		}
		return
	}
	rr := r.rerunState(run, pr.HeadRefOid)
	rr.Overruns++
	for _, c := range rows {
		if id := actionsRun(c); !slices.Contains(rr.Pending, id) {
			rr.Pending = append(rr.Pending, id)
		}
	}
	for _, c := range pr.Checks { // a re-run of the whole run replaces its finished jobs too
		if slices.Contains(rr.Pending, actionsRun(c)) {
			rr.Masked = append(rr.Masked, link(c))
		}
	}
	for _, id := range rr.Pending {
		if rc, _, errs := r.Tools.GH("", "run", "cancel", id, "-R", run.Repo); rc != 0 {
			r.printf("%s: gh run cancel %s: %s\n", run.Task, id, firstLine(errs))
		}
	}
	r.enterFlow(run, FlowCIQueued, pr.HeadRefOid) // a new budget for the second attempt
}

func (r *Runner) rerunState(run *Run, head string) *RerunState {
	if run.Rerun == nil || run.Rerun.Head != head {
		run.Rerun = &RerunState{Head: head}
	}
	return run.Rerun
}

// rerunPending re-runs the cancelled runs once they have stopped.
func (r *Runner) rerunPending(run *Run) {
	rr := run.Rerun
	if rr == nil || len(rr.Pending) == 0 {
		return
	}
	pr := r.poll.prs[run.PRURL] // the unmasked results
	if pr == nil || pr.HeadRefOid != rr.Head {
		return
	}
	var left, done []string
	for _, id := range rr.Pending {
		if slices.ContainsFunc(pr.Checks, func(c map[string]any) bool { return actionsRun(c) == id && unfinished(c) }) {
			left = append(left, id)
			continue
		}
		if rc, _, errs := r.Tools.GH("", "run", "rerun", id, "-R", run.Repo); rc != 0 {
			r.printf("%s: gh run rerun %s: %s\n", run.Task, id, firstLine(errs))
			if rr.Tries++; rr.Tries >= rerunTries {
				rr.Pending, rr.Masked = nil, nil
				return
			}
			left = append(left, id)
			continue
		}
		done = append(done, id)
	}
	rr.Pending = left
	if len(done) > 0 {
		_ = r.Log.Emit("ci.rerun", run, Event{Data: map[string]any{"head": rr.Head, "reason": "timeout", "runs": done}})
	}
}

// flakyRerun re-runs the failed jobs of a head once when every one of them passed on the base in the last 24 h.
func (r *Runner) flakyRerun(run *Run, head string, failed []map[string]any) bool {
	if r.poll == nil || run.Eval || len(failed) == 0 {
		return false
	}
	if rr := run.Rerun; rr != nil && rr.Head == head && rr.Flaky {
		return false
	}
	b := r.poll.bases[baseKey(run.Repo, run.Base)]
	if b == nil {
		return false
	}
	var ids, links []string
	for _, c := range failed {
		id := actionsRun(c)
		if id == "" || !b.passedSince(rowStr(c, "workflowName"), rowStr(c, "name"), time.Now().Add(-24*time.Hour)) {
			return false
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
		links = append(links, link(c))
	}
	for _, id := range ids {
		if rc, _, errs := r.Tools.GH("", "run", "rerun", id, "--failed", "-R", run.Repo); rc != 0 {
			r.printf("%s: gh run rerun %s: %s\n", run.Task, id, firstLine(errs))
			return false
		}
	}
	rr := r.rerunState(run, head)
	rr.Flaky, rr.Masked = true, append(rr.Masked, links...)
	_ = r.Log.Emit("ci.rerun", run, Event{Data: map[string]any{"head": head, "reason": "flaky", "runs": ids}})
	return true
}

// queuedTooLong reports whether any PR has been waiting for a CI runner over its budget.
func (r *Runner) queuedTooLong(runs []Run) bool {
	for _, x := range pollRuns(runs) {
		f := x.Flow
		if f == nil || f.State != FlowCIQueued {
			continue
		}
		since, err := time.Parse(time.RFC3339, f.Since)
		if err != nil || int64(time.Since(since).Seconds()) < f.BudgetS {
			continue
		}
		if state, _, ok := r.ciFlow(&x); !ok || state == FlowCIQueued {
			return true
		}
	}
	return false
}

// judgedAt is when the judge gave the verdict a run merges on; zero for a run that is not at the merge step.
func judgedAt(x Run) time.Time {
	if (x.State != Merging && x.State != Judged) || len(x.Verdicts) == 0 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, x.Verdicts[len(x.Verdicts)-1].TS)
	return t
}

// fifo puts the runs at the merge step first, in the order they were judged.
func fifo(runs []Run) {
	slices.SortStableFunc(runs, func(a, b Run) int {
		x, y := judgedAt(a), judgedAt(b)
		switch {
		case x.IsZero() && y.IsZero():
			return 0
		case x.IsZero():
			return 1
		case y.IsZero():
			return -1
		}
		return x.Compare(y)
	})
}

// The merge train: the runner's merges onto a base since its push run was last seen green, kept in Meta.

type trainCar struct {
	SHA   string   `json:"sha"`
	Files []string `json:"files"`
}

type train struct {
	Cars []trainCar `json:"cars"`
	Rode int        `json:"rode"` // merges that went out while the previous push run was pending
}

func trainKey(repo, base string) string { return "train:" + baseKey(repo, base) }

func (r *Runner) train(repo, base string) (t train) {
	_ = json.Unmarshal([]byte(r.Store.Meta(trainKey(repo, base))), &t)
	return t
}

func (r *Runner) setTrain(repo, base string, t train) {
	b, err := json.Marshal(t)
	if err == nil {
		err = r.Store.SetMeta(trainKey(repo, base), string(b))
	}
	if err != nil {
		r.printf("merge train of %s: %v\n", repo, err)
	}
}

// trainState adjusts main's push state for the train: a red commit of the train is a red main; a pending head that is
// the train's last merge lets a PR sharing no file with the train merge, merge_train times at most.
func (r *Runner) trainState(repo, base, head, main string, files []string) (string, bool) {
	if r.poll == nil {
		return main, false // by hand: one merge per green push run
	}
	t := r.train(repo, base)
	if len(t.Cars) == 0 {
		return main, false
	}
	if main == "green" {
		r.setTrain(repo, base, train{})
		return main, false
	}
	if b := r.poll.bases[baseKey(repo, base)]; b != nil {
		for _, c := range t.Cars {
			if b.pushState(c.SHA) == "red" {
				return "red", false
			}
		}
	}
	if main != "pending" || t.Cars[len(t.Cars)-1].SHA != head || t.Rode >= cfgInt(r.Store, "merge_train") || len(files) == 0 {
		return main, false
	}
	for _, c := range t.Cars {
		if slices.ContainsFunc(files, func(f string) bool { return slices.Contains(c.Files, f) }) {
			return main, false
		}
	}
	return main, true
}

// joinTrain records a merge; rode means it went out while the previous push run was pending.
func (r *Runner) joinTrain(run *Run, files []string, rode bool) {
	if run.MergeSHA == "" {
		return
	}
	t := train{}
	if rode {
		t = r.train(run.Repo, run.Base)
		t.Rode++
		run.Rode = true
	}
	t.Cars = append(t.Cars, trainCar{SHA: run.MergeSHA, Files: files})
	r.setTrain(run.Repo, run.Base, t)
}

// culprit is the merge a red main at sha is blamed on: the first red commit of the train up to sha. "" while an earlier
// push run of the train is still pending.
func (r *Runner) culprit(repo, base, sha string) string {
	if r.poll == nil {
		return sha
	}
	b := r.poll.bases[baseKey(repo, base)]
	t := r.train(repo, base)
	if b == nil || !slices.ContainsFunc(t.Cars, func(c trainCar) bool { return c.SHA == sha }) {
		return sha
	}
	for _, c := range t.Cars {
		if c.SHA == sha {
			break
		}
		switch b.pushState(c.SHA) {
		case "red":
			return c.SHA
		case "pending":
			return ""
		}
	}
	return sha
}

// gh API use: the calls of the passes, counted per UTC hour for the digest.

const ghHour = "2006-01-02T15"

func ghKey(t time.Time) string { return "gh_calls:" + t.UTC().Format(ghHour) }

// countGH counts the gh calls until the returned function is called, which records them and restores the tool.
func (r *Runner) countGH() func() {
	gh, n := r.Tools.GH, 0
	r.Tools.GH = func(input string, args ...string) (int, string, string) {
		n++
		return gh(input, args...)
	}
	return func() {
		r.Tools.GH = gh
		if n == 0 {
			return
		}
		key := ghKey(time.Now())
		var old int
		_, _ = fmt.Sscan(r.Store.Meta(key), &old)
		if err := r.Store.SetMeta(key, fmt.Sprint(old+n)); err != nil {
			r.printf("gh call count: %v\n", err)
		}
	}
}
