package hm

import (
	"fmt"
	"strings"
	"time"
)

// The owner queue: every question only the owner can answer goes here, from the planner (`hivemill ask add`) or the
// runner (a held run). Unblocked work goes on while a question is open; `hivemill health --alert` sends the questions
// older than ask_alert_h to the owner in one digest, and an answer wakes the planner.

// AskAdd files an owner question.
func (r *Runner) AskAdd(a Ask) (int64, error) {
	s, err := r.sq()
	if err != nil {
		return 0, err
	}
	a.Question = strings.TrimSpace(a.Question)
	if a.Question == "" {
		return 0, fmt.Errorf("%w: an empty question", ErrRefused)
	}
	if a.Task != "" && a.Goal == "" {
		if t, err := r.Store.Task(a.Task); err == nil {
			a.Goal, a.Project = t.Goal, or(a.Project, t.Project)
		} else {
			return 0, err
		}
	}
	if a.Goal != "" && a.Project == "" {
		if g, err := s.Goal(a.Goal); err == nil {
			a.Project = g.Project
		} else {
			return 0, err
		}
	}
	a.AskedBy = r.actor()
	id, _, err := s.AddAsk(a)
	if err != nil {
		return 0, err
	}
	// the task goes in data, not task_id: a question is not a run, and the scoreboard makes a row per task_id
	_ = r.Log.Emit("ask.added", nil, Event{Project: a.Project, Data: map[string]any{"id": id, "by": a.AskedBy, "task": a.Task, "goal": a.Goal}})
	return id, nil
}

// AskClose answers (answered=true) or withdraws a question. An answer is an ask.answered event, which wakes the planner.
func (r *Runner) AskClose(id int64, answered bool, text string) error {
	s, err := r.sq()
	if err != nil {
		return err
	}
	status, kind := "withdrawn", "ask.withdrawn"
	if answered {
		status, kind = "answered", "ask.answered"
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("%w: an empty answer", ErrRefused)
		}
	}
	if err := s.CloseAsk(id, status, strings.TrimSpace(text), r.actor()); err != nil {
		return err
	}
	a, _ := s.Ask(id)
	e := Event{Data: map[string]any{"id": id, "by": r.actor()}}
	if a != nil {
		e.Project, e.Data["task"], e.Data["goal"] = a.Project, a.Task, a.Goal
	}
	_ = r.Log.Emit(kind, nil, e)
	return nil
}

// answeredHeld acts on the owner's answer to the question about a held run (ref held:T-x[:round]): an answer starting
// with "merge" merges a run the judge passed, the normal way (tier rules unchanged); any other answer goes to the task
// session as a new round. A merged PR is recorded by checkHeld before this. Called with the task lock held.
func (r *Runner) answeredHeld(run *Run, list []Terminal, listed bool) {
	s, err := r.sq()
	if err != nil {
		return
	}
	asks, err := s.Asks("answered")
	if err != nil {
		return
	}
	var found []Ask
	for _, x := range asks { // oldest first: the latest question wins
		if x.Ref == "held:"+run.Task || x.Ref == fmt.Sprintf("held:%s:%d", run.Task, run.Round) {
			found = append(found, x)
		}
	}
	if len(found) == 0 {
		return
	}
	a := found[len(found)-1]
	if t, err := r.Store.Task(run.Task); err != nil || t.Status == "cancelled" {
		return // the owner cancelled the task: no answer brings it back
	}
	if run.PRURL != "" {
		if pr, err := PRView(r.Tools, run.PRURL); err != nil || pr.State != "OPEN" {
			return // checkHeld records a merged or closed PR; an unread one waits for the next pass
		}
	}
	merge := strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Answer)), "merge") &&
		len(run.Verdicts) > 0 && run.Verdicts[len(run.Verdicts)-1].Verdict == VerdictMerge
	if !merge && (!listed || (run.Machine != "" && machineDown(r.Tools, run.Machine))) {
		return // cannot tell whether the session is there: next pass
	}
	if !merge {
		if err := r.remakeWorktree(run); err != nil { // the run stays held and the answer unused: next pass
			r.printf("%s: cannot make the worktree again: %v\n", run.Task, err)
			return
		}
	}
	// used once, and retired before acting: a run held again gets a new question, and no older answer is left over
	for _, x := range found {
		if err := s.RetireAskRef(x.ID); err != nil {
			r.printf("%s: question #%d: %v\n", run.Task, x.ID, err)
			return
		}
	}
	run.Outcome, run.FailReason, run.Ended, run.MergeHold = "", "", "", ""
	run.IdleSince, run.IdleNudges, run.Resumes = "", 0, 0 // the counts from before the hold are not this round's
	_ = r.Store.TaskStatus(run.Task, "in_progress", "internal")
	if merge {
		run.State = Merging
		_ = r.Store.Event(r.actor(), "task", run.Task, "run-answered-merge", a.ID)
		r.printf("%s: question #%d answered: merging\n", run.Task, a.ID)
		return
	}
	r.printf("%s: question #%d answered: back to the task session\n", run.Task, a.ID)
	r.resumeAs(run, "answer", fmt.Sprintf("The owner answered the question about your held run: %s\n\nAct on it, %s",
		a.Answer, r.pushAndStop(run.Project)))
}

// AskLine is one question as `ask list` prints it.
func AskLine(a Ask, now time.Time) string {
	scope := strings.TrimSpace(strings.Join([]string{a.Project, a.Goal, a.Task}, " "))
	age := ""
	if at, err := time.Parse(time.RFC3339, a.Asked); err == nil {
		age = now.Sub(at).Round(time.Minute).String()
	}
	line := fmt.Sprintf("#%d %s %s by %s, %s ago: %s", a.ID, a.Status, or(scope, "-"), a.AskedBy, age, a.Question)
	if a.Status != "open" {
		line += fmt.Sprintf("\n    %s by %s: %s", a.Status, a.AnsweredBy, a.Answer)
	}
	return line
}

// OwnerAlert is one condition `hivemill health --alert` reports to the owner.
type OwnerAlert struct {
	Kind string // hivemill-idle | hivemill-asks
	Ref  string // the feed row's text: the same ref while the condition is the same
	Text string // what `hivemill health` prints
}

// OwnerChecks are the starvation and owner-queue checks (they do not change the dead-man result):
//   - hivemill-idle: no task dispatched for idle_alert_min while an active goal exists and a run slot is free (drain off),
//     with the likely causes;
//   - hivemill-asks: owner questions open for longer than ask_alert_h, in one digest.
func (r *Runner) OwnerChecks(now time.Time) []OwnerAlert {
	s, err := r.sq()
	if err != nil {
		return nil
	}
	var out []OwnerAlert
	if a := r.idleCheck(s, now); a != nil {
		out = append(out, *a)
	}
	open, _ := s.Asks("open")
	maxAge := time.Duration(cfgInt(r.Store, "ask_alert_h")) * time.Hour
	var refs, lines []string
	for _, a := range open {
		at, err := time.Parse(time.RFC3339, a.Asked)
		if err != nil || now.Sub(at) < maxAge {
			continue
		}
		scope := or(a.Task, or(a.Goal, or(a.Project, "-")))
		refs = append(refs, fmt.Sprintf("#%d %s: %s", a.ID, scope, truncate(a.Question, 120)))
		lines = append(lines, fmt.Sprintf("  #%d %s (%s): %s", a.ID, scope, now.Sub(at).Round(time.Minute), a.Question))
	}
	if len(refs) > 0 {
		out = append(out, OwnerAlert{Kind: "hivemill-asks",
			Ref:  truncate(fmt.Sprintf("%d owner question(s) waiting: %s", len(refs), strings.Join(refs, "; ")), 1500),
			Text: fmt.Sprintf("%d owner question(s) open for over %s (answer with `hivemill ask answer N TEXT`):\n%s", len(refs), maxAge, strings.Join(lines, "\n"))})
	}
	return out
}

func (r *Runner) idleCheck(s *SQLite, now time.Time) *OwnerAlert {
	goals, err := s.Goals("active")
	if err != nil || len(goals) == 0 || r.Draining() {
		return nil
	}
	runs, _ := r.Store.Runs()
	active := 0
	var last time.Time
	for _, x := range runs {
		if busy(x.State) {
			active++
		}
		if at, err := time.Parse(time.RFC3339, x.Dispatched); err == nil && at.After(last) {
			last = at
		}
	}
	slots := cfgInt(r.Store, "max_concurrent")
	if active >= slots {
		return nil // every slot works: busy, not starved
	}
	if last.IsZero() { // nothing was ever dispatched: count from the oldest active goal
		for _, g := range goals {
			if at, err := time.Parse(time.RFC3339, g.Created); err == nil && (last.IsZero() || at.Before(last)) {
				last = at
			}
		}
	}
	limit := time.Duration(cfgInt(r.Store, "idle_alert_min")) * time.Minute
	if limit == 0 {
		limit = time.Duration(cfgInt(r.Store, "idle_alert_h")) * time.Hour
	}
	if last.IsZero() || now.Sub(last) < limit {
		return nil
	}
	projects := csv(r.Store.Cfg("projects"))
	in := map[string]bool{}
	for _, p := range projects {
		in[p] = true
	}
	var causes []string
	if len(projects) == 0 {
		causes = append(causes, "config projects is empty, so a pass dispatches nothing (`hivemill config projects ID,...`)")
	}
	seen := map[string]bool{}
	for _, g := range goals {
		if g.Project != "" && len(projects) > 0 && !in[g.Project] && !seen[g.Project] {
			seen[g.Project] = true
			causes = append(causes, fmt.Sprintf("project %s has an active goal but is not in config projects", g.Project))
		}
	}
	caps := r.ReadCaps(now)
	if why := caps.DispatchBlock(r.Store, ""); why != "" {
		causes = append(causes, "dispatch held: "+why)
	}
	if _, waits := r.runCounts(); waits >= cfgInt(r.Store, "max_waiting") {
		causes = append(causes, fmt.Sprintf("dispatch paused: %d runs wait, max_waiting=%d (see why they wait: CI, a red main or held PRs)", waits, cfgInt(r.Store, "max_waiting")))
	}
	if len(projects) > 0 {
		if ready, err := r.ReadyIn(projects); err == nil && len(ready) == 0 {
			causes = append(causes, "0 lint-passing ready tasks in the dispatched projects")
		}
	}
	if r.Store.Meta("planner") != "on" {
		causes = append(causes, "the planner is off (`hivemill planner start`)")
	} else if r.Store.Meta("planner_lost") != "" {
		causes = append(causes, "the planner is lost (`hivemill planner start`)")
	}
	if len(causes) == 0 {
		causes = append(causes, "no cause found; see `hivemill report` and `hivemill planner status`")
	}
	text := fmt.Sprintf("no task dispatched for %s while %d goal(s) are active (%d of %d run slots busy): %s",
		now.Sub(last).Round(time.Minute), len(goals), active, slots, strings.Join(causes, "; "))
	return &OwnerAlert{Kind: "hivemill-idle", Ref: truncate(text, 1500), Text: text}
}

// RaiseOwnerAlerts writes the owner alert feed rows of these checks: one open row per kind. An idle alert is
// raised at most once per alert_every_s; the question digest is raised again only when its list changes. A kind whose
// condition cleared has its open row closed.
func (r *Runner) RaiseOwnerAlerts(now time.Time, checks []OwnerAlert) ([]string, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	var sent []string
	have := map[string]bool{}
	for _, a := range checks {
		have[a.Kind] = true
		switch a.Kind {
		case "hivemill-idle":
			mark := r.Store.Meta("idle_alerted")
			if ts, err := time.Parse(time.RFC3339, mark); err == nil && now.Sub(ts) < time.Duration(cfgInt(r.Store, "alert_every_s"))*time.Second {
				continue
			}
			if _, err := s.ReplaceAlert(a.Kind, a.Ref, "hivemill planner status"); err != nil {
				return sent, err
			}
			_ = r.Store.SetMeta("idle_alerted", now.Format(time.RFC3339))
			sent = append(sent, a.Kind)
		case "hivemill-asks":
			opened, err := s.ReplaceAlert(a.Kind, a.Ref, "hivemill ask list")
			if err != nil {
				return sent, err
			}
			if opened {
				sent = append(sent, a.Kind)
			}
		}
		if len(sent) > 0 && sent[len(sent)-1] == a.Kind {
			_ = r.Log.Emit("owner.alert", nil, Event{Data: map[string]any{"kind": a.Kind, "text": truncate(a.Text, 500)}})
			r.ownerAlert(a.Kind, a.Text)
		}
	}
	for _, k := range []string{"hivemill-idle", "hivemill-asks"} {
		if !have[k] {
			_ = s.CloseAlertKind(k)
			if k == "hivemill-idle" {
				_ = r.Store.SetMeta("idle_alerted", "")
			}
		}
	}
	return sent, nil
}
