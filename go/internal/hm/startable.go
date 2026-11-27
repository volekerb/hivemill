package hm

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Why a pass skips a todo task.
const (
	BlockedRun  = "run"
	BlockedLint = "lint"
	BlockedDeps = "deps"
)

// BlockedTask is a todo task a pass skips: Kind is one of the Blocked constants, Reason is what the owner reads.
type BlockedTask struct {
	Task         Task
	Kind, Reason string
}

// Queue splits the todo coder tasks of the active goals of these projects into those a pass would start (deps done, spec
// set, spec gate passing, never run or retried: a task with a run is started again only after `hivemill task retry`,
// on its own branch and PR) and those it skips, each with the first reason that holds: a run, then lint, then deps.
func (r *Runner) Queue(projects []string) (ready []Task, blocked []BlockedTask, err error) {
	ts, err := r.Store.ReadyTasks(projects)
	if err != nil || len(ts) == 0 {
		return nil, nil, err
	}
	done, err := r.Store.DoneTasks()
	if err != nil {
		return nil, nil, err
	}
	for _, t := range ts {
		skip := func(kind, reason string) { blocked = append(blocked, BlockedTask{t, kind, reason}) }
		if old, _ := r.Store.Run(t.ID); old != nil && !r.retryPending(t.ID, old) {
			skip(BlockedRun, "has a run (`hivemill task retry` starts a failed one again)")
			continue
		}
		if strings.TrimSpace(t.Spec) == "" {
			skip(BlockedLint, "lint: no spec")
			continue
		}
		if probs := LintProblems(t.Acceptance); len(probs) > 0 {
			skip(BlockedLint, "lint: "+probs[0])
			continue
		}
		var deps, open []string
		_ = json.Unmarshal([]byte(t.DependsOn), &deps)
		_, dead := r.deadDeps(t)
		for _, d := range deps {
			if done[d] {
				continue
			}
			if why := dead[d]; why != "" { // can never be done: re-point or drop it
				open = append(open, d+" "+why)
				continue
			}
			state := "missing"
			if dt, err := r.Store.Task(d); err == nil {
				state = dt.Status
			}
			open = append(open, fmt.Sprintf("%s (%s)", d, state))
		}
		if len(open) > 0 {
			skip(BlockedDeps, "deps: "+strings.Join(open, ", "))
			continue
		}
		ready = append(ready, t)
	}
	return ready, blocked, nil
}

// ReadyIn are the tasks of these projects a pass would start (see Queue).
func (r *Runner) ReadyIn(projects []string) ([]Task, error) {
	ready, _, err := r.Queue(projects)
	return ready, err
}

// ListQueue is `hivemill task list --startable|--blocked` over the configured projects.
func (r *Runner) ListQueue(w io.Writer, showBlocked bool) error {
	ready, blocked, err := r.Queue(csv(r.Store.Cfg("projects")))
	if err != nil {
		return err
	}
	if showBlocked {
		for _, b := range blocked {
			fmt.Fprintf(w, "%s %s %s: %s\n", b.Task.ID, b.Task.Goal, b.Task.Title, b.Reason)
		}
		return nil
	}
	for _, t := range ready {
		fmt.Fprintf(w, "%s %s %s\n", t.ID, t.Goal, t.Title)
	}
	return nil
}

func blockedCounts(blocked []BlockedTask) map[string]int {
	n := map[string]int{}
	for _, b := range blocked {
		n[b.Kind]++
	}
	return n
}

// noneStartable is the planner's level reason for a starved queue: a run slot is free, no task of the configured projects
// is startable and todo tasks exist ("" otherwise, and while drain or max_waiting holds dispatch). Once the planner heard
// it, it holds with every slot busy too, so a slot that fills and frees again is not news. No spaces, as every level.
func (r *Runner) noneStartable(ready []Task, blocked []BlockedTask) string {
	if len(ready) > 0 || len(blocked) == 0 {
		return ""
	}
	working, waits := r.runCounts()
	if r.Draining() || waits >= cfgInt(r.Store, "max_waiting") ||
		(working >= cfgInt(r.Store, "max_concurrent") && !strings.Contains(r.Store.Meta("planner_fp"), "none_startable:")) {
		return ""
	}
	n := blockedCounts(blocked)
	why := fmt.Sprintf("none_startable:todo=%d,blocked_by_deps=%d,blocked_by_lint=%d", len(blocked), n[BlockedDeps], n[BlockedLint])
	if n[BlockedRun] > 0 {
		why += fmt.Sprintf(",have_a_run=%d", n[BlockedRun])
	}
	return why
}

// reportUnstartable emits one task.unstartable event for each task that newly fails the spec gate. A task that was fixed
// leaves the remembered list, so a later failure is reported again; one that only left the queue for a while (its goal
// paused, its project out of config projects) stays on it.
func (r *Runner) reportUnstartable() error {
	ready, blocked, err := r.Queue(csv(r.Store.Cfg("projects")))
	if err != nil {
		return err
	}
	told := csv(r.Store.Meta("unstartable"))
	var now []string
	queued := map[string]bool{}
	for _, t := range ready {
		queued[t.ID] = true
	}
	for _, b := range blocked {
		queued[b.Task.ID] = true
		if b.Kind != BlockedLint {
			continue
		}
		if !slices.Contains(told, b.Task.ID) {
			if err := r.Log.Emit("task.unstartable", nil, Event{TaskID: b.Task.ID, GoalID: b.Task.Goal, Project: b.Task.Project,
				Data: map[string]any{"reason": b.Reason}}); err != nil {
				return err
			}
			r.printf("%s: unstartable: %s\n", b.Task.ID, b.Reason)
		}
		now = append(now, b.Task.ID)
	}
	for _, id := range told {
		if t, err := r.Store.Task(id); err == nil && !queued[id] && t.Status == "todo" &&
			(strings.TrimSpace(t.Spec) == "" || len(LintProblems(t.Acceptance)) > 0) {
			now = append(now, id)
		}
	}
	slices.Sort(told)
	slices.Sort(now)
	if slices.Equal(told, now) {
		return nil
	}
	return r.Store.SetMeta("unstartable", strings.Join(now, ","))
}
