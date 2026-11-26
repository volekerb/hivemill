package hm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// A run is bounded in wall time too: a session that waits forever costs almost nothing, so no budget ever stops it.

// workGrace is the time between the one resume at run_max_working_min and the failure.
const workGrace = 30 * time.Minute

// workingLimit watches the time a run has been in working in this round (run_max_working_min, 0 or less = off). A run
// that leaves working (gate pass) starts over at its next round. It emits run.long_working at 50% and 100% of the limit,
// resumes the session once at 100% and fails the run workGrace later. It returns true when it did either of the last two.
func (r *Runner) workingLimit(run *Run, now time.Time) bool {
	limit := cfgInt(r.Store, "run_max_working_min")
	start, err := time.Parse(time.RFC3339, run.RoundStart)
	if limit <= 0 || err != nil {
		return false
	}
	spent := now.Sub(start)
	mins, full := int(spent/time.Minute), time.Duration(limit)*time.Minute
	for _, pct := range []int{50, 100} {
		if run.LongPct < pct && spent >= full*time.Duration(pct)/100 {
			run.LongPct = pct
			_ = r.Log.Emit("run.long_working", run, Event{Data: map[string]any{"pct": pct, "minutes": mins, "limit": limit,
				"round": run.Round}})
		}
	}
	if spent < full {
		return false
	}
	reason := fmt.Sprintf("timed out: %d minutes in working", mins)
	if resumed, err := time.Parse(time.RFC3339, run.LongResumed); err == nil {
		if now.Sub(resumed) < workGrace {
			return false
		}
		r.fail(run, reason)
		return true
	}
	// Not resume(): the round goes on, so its findings and review evidence stay valid.
	text := fmt.Sprintf("You have worked %d minutes without a passing gate. Stop any wait loop, finish or report what "+
		"blocks you.", mins)
	if err := r.Prompt(run.Agent, text); err != nil {
		r.printf("%s: working limit resume: %v\n", run.Task, err)
		if spent < full+workGrace {
			return false // the next pass tries again
		}
		r.fail(run, reason)
		return true
	}
	run.LongResumed = now.Format(time.RFC3339)
	r.recordPrompt(run, "task", "resume", text)
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-resume", text)
	return true
}

// deadDeps lists the dependencies of t that can never be done, as id -> "cancelled" or "failed", in the order of the
// depends list: the task is cancelled, or its run failed and nothing finished the task since.
func (r *Runner) deadDeps(t Task) (ids []string, why map[string]string) {
	var deps []string
	_ = json.Unmarshal([]byte(t.DependsOn), &deps)
	why = map[string]string{}
	for _, id := range deps {
		d, err := r.Store.Task(id)
		if err != nil || d.Status == "done" {
			continue
		}
		state := ""
		if d.Status == "cancelled" {
			state = "cancelled"
		} else if run, err := r.Store.Run(id); err == nil && run != nil && run.State == Failed && !run.Eval {
			state = "failed"
		}
		if state != "" {
			ids, why[id] = append(ids, id), state
		}
	}
	return ids, why
}

// deadDepLevel is the planner work item of a todo task with a dead dependency ("re-point or drop the dependency"), as
// one word: dead_dep:T-2=T-1(cancelled). Empty when every dependency can still be done.
func (r *Runner) deadDepLevel(t Task) string {
	if t.Status != "todo" {
		return ""
	}
	ids, why := r.deadDeps(t)
	if len(ids) == 0 {
		return ""
	}
	for i, id := range ids {
		ids[i] = id + "(" + why[id] + ")"
	}
	return "dead_dep:" + t.ID + "=" + strings.Join(ids, ",")
}
