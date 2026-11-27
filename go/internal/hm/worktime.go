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
	if limit <= 0 || err != nil || strings.EqualFold(strings.TrimSpace(r.Store.Cfg("run_max_working_min")), "off") {
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
		// a disconnected worker keeps its session, which may be finishing: its grace starts over until it is back
		if run.Machine != "" && machineDown(r.Tools, run.Machine) {
			run.LongResumed = now.Format(time.RFC3339)
			return false
		}
		if now.Sub(resumed) < workGrace {
			return false
		}
		r.failUnlessRescued(run, reason)
		return true
	}
	// Not resume(): the round goes on, so its findings and review evidence stay valid.
	text := fmt.Sprintf("You have worked %d minutes without a passing gate. Stop any wait loop, finish or report what "+
		"blocks you.", mins)
	if err := r.Prompt(run.Agent, text); err != nil {
		r.printf("%s: working limit resume: %v\n", run.Task, err)
		// The next pass tries again. The grace runs from the first failed send, not from the limit: the daemon may have
		// been down past it. A disconnected worker keeps its session: it is never failed, and its outage does not count.
		if run.Machine != "" && machineDown(r.Tools, run.Machine) {
			run.LongSendFailed = ""
			return false
		}
		first, perr := time.Parse(time.RFC3339, run.LongSendFailed)
		if perr != nil {
			run.LongSendFailed = now.Format(time.RFC3339)
			return false
		}
		if now.Sub(first) < workGrace {
			return false
		}
		r.failUnlessRescued(run, reason)
		return true
	}
	run.LongResumed = now.Format(time.RFC3339)
	// the message is new work: a quick answer must not read as 15 minutes idle, nor an earlier idle nudge fail it as
	// stalled inside the grace
	run.IdleSince, run.IdleNudges = "", 0
	r.recordPrompt(run, "task", "resume", text)
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-resume", text)
	return true
}

// deadDeps lists the dependencies of t that can never be done, as id -> "cancelled" or "failed", in the order of the
// depends list: the task is cancelled, or its run failed and the task is still where the failure left it (blocked; a
// task set back to todo is being retried, and a run held for the owner's merge can still merge).
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
		} else if run, err := r.Store.Run(id); err == nil && run != nil && d.Status == "blocked" && run.State == Failed && !run.Eval &&
			!heldForOwner(run) {
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
