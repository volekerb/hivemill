package hm

import (
	"fmt"
	"time"
)

// The CI wait: while a run is gated the runner reads its PR checks every ci_poll_s, with no session involved. All checks
// green, the required ones present, starts the judge. Checks still pending after ci_timeout_s alert the owner once; the
// run stays gated and is still polled.

// CIWait is the runner's wait for one head's checks (Run.CI; nil outside a wait).
type CIWait struct {
	Head          string
	Since, Polled int64 // unix seconds: the wait began, the last poll
	Green         int64 // unix seconds: the first of the green reads in a row (0: the last read was not green)
	Alerted       bool  // the timeout alert was sent
}

// ciInterval is ci_poll_s less a little slack: the daemon passes about that often, and a pass that comes a moment early
// must not wait for the next one.
func (r *Runner) ciInterval() int64 { return int64(cfgInt(r.Store, "ci_poll_s")) - 2 }

// ciPollDue reports whether the gated run is due for its next read of the PR.
func (r *Runner) ciPollDue(run *Run, now time.Time) bool {
	return run.CI == nil || now.Unix()-run.CI.Polled >= r.ciInterval()
}

// ciPolled records a poll in the wait for the run's head, starting the wait when there is none.
func (r *Runner) ciPolled(run *Run, now time.Time) {
	if run.CI == nil || run.CI.Head != run.Head {
		r.endCIWait(run)
		run.CI = &CIWait{Head: run.Head, Since: now.Unix()}
	}
	run.CI.Polled = now.Unix()
}

// endCIWait drops the run's wait and closes its timeout alert.
func (r *Runner) endCIWait(run *Run) {
	if run.CI == nil {
		return
	}
	if run.CI.Alerted {
		if err := r.Store.CloseAlert("hivemill-ci", run.Task); err != nil {
			r.printf("%s: alert feed: %v\n", run.Task, err)
		}
	}
	run.CI = nil
}

// ciSettled reports whether a green rollup is final: every required check is on the PR (right after a push not all are
// registered yet). Without a required list, green must still hold one poll after the first green read.
func (r *Runner) ciSettled(run *Run, checks []map[string]any, now time.Time) bool {
	required, missing := r.missingChecks(run.Task, checks)
	if len(required) > 0 {
		return len(missing) == 0
	}
	return now.Unix()-run.CI.Green >= r.ciInterval()
}

// missingChecks returns the check names a task's PR must have (its project's list, else the factory's) and those of
// them that are not among checks.
func (r *Runner) missingChecks(task string, checks []map[string]any) (required, missing []string) {
	present := map[string]bool{}
	for _, c := range checks {
		if n, _ := c["name"].(string); n != "" {
			present[n] = true
		}
		if n, _ := c["context"].(string); n != "" {
			present[n] = true
		}
	}
	req := r.Store.FactoryCfg("required_checks")
	if t, _ := r.Store.Task(task); t != nil {
		if p := r.projectRequired(t.Project); p != "" {
			req = p
		}
	}
	required = csv(req)
	for _, c := range required {
		if !present[c] {
			missing = append(missing, c)
		}
	}
	return required, missing
}

// ciOverdue alerts the owner once per wait.
func (r *Runner) ciOverdue(run *Run, now time.Time) {
	limit := int64(cfgInt(r.Store, "ci_timeout_s"))
	if run.CI.Alerted || now.Unix()-run.CI.Since < limit {
		return
	}
	run.CI.Alerted = true
	text := fmt.Sprintf("%s: CI is not settled after %s on %s; the run stays and is still polled", run.Task,
		time.Duration(limit)*time.Second, run.PRURL)
	if err := r.Store.AlertFeed("hivemill-ci", run.Task, run.PRURL); err != nil {
		r.printf("%s: alert feed: %v\n", run.Task, err)
	}
	r.ownerAlert("hivemill-ci", text)
	_ = r.Log.Emit("ci.timeout", run, Event{Data: map[string]any{"head": run.Head, "limit_s": limit}})
}
