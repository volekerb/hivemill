package hm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The planner (docs/hivemill-planner.md) is one long-lived interactive session, named `planner`, that keeps each
// project's ready queue full of small lint-passing tasks and turns failed and held runs into a next step. It never
// writes code. The daemon keeps it alive and wakes it on events; it sleeps otherwise.

// PlannerName is the planner's hc terminal, FACTORY_AGENT and agents row name (role planner).
const PlannerName = "planner"

// sq is the SQLite store (the planner, the owner queue and the digest read goals, tasks and asks through it).
func (r *Runner) sq() (*SQLite, error) {
	s, ok := r.Store.(*SQLite)
	if !ok {
		return nil, errors.New("the planner needs the SQLite store")
	}
	return s, nil
}

func (r *Runner) plannerModel() (string, string, error) {
	m, eff := r.Store.Cfg("planner_model"), r.Store.Cfg("planner_effort")
	if err := CheckModel(m); err != nil {
		return "", "", err
	}
	switch eff {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return "", "", fmt.Errorf("planner_effort %q must be low, medium, high, xhigh or max", eff)
	}
	return m, eff, nil
}

// PlannerStart turns the planner on (the daemon keeps it alive from now on) and starts its session unless it runs.
func (r *Runner) PlannerStart(now time.Time) error {
	if _, _, err := r.plannerModel(); err != nil {
		return err
	}
	l := r.lock("planner", true) // a daemon pass never revives or wakes the planner while it starts
	if l != nil {
		defer l.Close()
	}
	on := func() {
		_ = r.Store.SetMeta("planner", "on")
		_ = r.Store.SetMeta("planner_lost", "")
		_ = r.Store.SetMeta("planner_resumes", "")
		// a started planner reads the state itself (its kickoff): the event log's history is not news
		_ = r.Store.SetMeta("planner_cursor", strconv.FormatInt(fileSize(r.Log.Path), 10))
		_ = r.Store.SetMeta("planner_last_wake", strconv.FormatInt(now.Unix(), 10)) // the kickoff is a wake
	}
	if list, ok := Terminals(r.Tools); ok && alive(list, true, PlannerName) {
		on()
		r.printf("planner already runs\n")
		return nil
	}
	if err := r.launchPlanner("", r.plannerKickoff()); err != nil {
		return err
	}
	on()
	r.printf("planner started (%s)\n", r.Store.Meta("planner_sid"))
	return nil
}

// PlannerStop turns the planner off and closes its terminal (its transcript stays resumable).
func (r *Runner) PlannerStop() error {
	_ = r.Store.SetMeta("planner", "off")
	r.Close(PlannerName)
	r.printf("planner off\n")
	return nil
}

func (r *Runner) launchPlanner(resume, kickoff string) error {
	model, effort, err := r.plannerModel()
	if err != nil {
		return err
	}
	cwd := filepath.Join(r.Env.StateDir(), "planner")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return err
	}
	if err := trustPath(r.Env, cwd); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: trust %s: %v (the trust dialog may show)\n", cwd, err)
	}
	sid, err := r.StartSession(nil, Launch{Role: "planner", Name: PlannerName, Cwd: cwd, Model: model, Effort: effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", "planner.md"), Kickoff: kickoff, Resume: resume,
		SessRole: "planner", Task: PlannerName})
	if sid != "" {
		_ = r.Store.SetMeta("planner_sid", sid)
	}
	return err
}

// notePlannerSession records a new planner session id (/clear, resume), so a later resume picks the current one.
func (r *Runner) notePlannerSession(sid string) error {
	if sid == r.Store.Meta("planner_sid") {
		return nil
	}
	_ = r.Store.SetMeta("planner_sid", sid)
	_ = r.Store.AddSession(Session{SID: sid, Task: PlannerName, Role: "planner", Agent: PlannerName, Started: nowISO()})
	return r.Store.AgentSession(PlannerName, sid)
}

func (r *Runner) plannerKickoff() string {
	levels, _ := r.PlannerLevels()
	return "You are the hivemill planner. Do one planning round now: read the state (`hivemill goal list --status active`, " +
		"`hivemill task list --status todo`, `hivemill run status`, `hivemill ask list`), then follow your role steps for every " +
		"project with an active goal, and stop. Later messages that start with `hivemill-wake` say why you were woken. " +
		"State now: " + or(strings.Join(levels, " "), "nothing below the queue minimum")
}

// PlannerPass is the daemon's planner step: file an owner question for each held run, keep the planner alive and wake it
// when something changed (DecideWake). It is a no-op for the session while the planner is off.
func (r *Runner) PlannerPass(now time.Time) {
	r.syncHeldAsks()
	if r.Store.Meta("planner") != "on" {
		return
	}
	l := r.lock("planner", false)
	if l == nil {
		return // `planner start` is launching it
	}
	defer l.Close()
	list, ok := Terminals(r.Tools)
	if !ok {
		return // hc cannot say: never restart or wake blind
	}
	var t *Terminal
	for i := range list {
		if list[i].Name == PlannerName && list[i].State == "live" {
			t = &list[i]
		}
	}
	if t == nil {
		r.plannerRevive(now, "terminal missing")
		return
	}
	r.plannerWake(now, t.Status == "idle" || t.Status == "done", false, "")
}

// plannerRevive restarts a dead or stuck planner with `claude --resume` of its last session, at most resume_max times in
// 24 h; after that it opens one owner alert and stops trying until `hivemill planner start`. It reports whether it
// restarted the planner.
func (r *Runner) plannerRevive(now time.Time, why string) bool {
	if r.Store.Meta("planner_lost") != "" {
		return false
	}
	var recent []string
	for _, x := range csv(r.Store.Meta("planner_resumes")) {
		if ts, err := strconv.ParseInt(x, 10, 64); err == nil && now.Sub(time.Unix(ts, 0)) < 24*time.Hour {
			recent = append(recent, x)
		}
	}
	if max := cfgInt(r.Store, "resume_max"); len(recent) >= max {
		_ = r.Store.SetMeta("planner_lost", now.Format(time.RFC3339))
		what := fmt.Sprintf("planner lost after %d restarts in 24h", len(recent))
		_ = r.Store.AlertFeed("hivemill-planner", what, "hivemill planner start")
		r.ownerAlert("hivemill-planner", what+". Restart it with `hivemill planner start`.")
		_ = r.Log.Emit("planner.lost", nil, Event{Data: map[string]any{"restarts_24h": len(recent)}})
		r.printf("planner: lost after %d restarts in 24h; owner alerted\n", len(recent))
		return false
	}
	if why != "terminal missing" {
		r.Close(PlannerName) // a stuck planner still holds its terminal name
	}
	recent = append(recent, strconv.FormatInt(now.Unix(), 10))
	_ = r.Store.SetMeta("planner_resumes", strings.Join(recent, ","))
	sid := r.Store.Meta("planner_sid")
	kick := "The factory restarted your session after it was lost. Re-read the state and continue your role steps."
	if sid == "" {
		kick = r.plannerKickoff()
	}
	err := r.launchPlanner(sid, kick)
	_ = r.Store.SetMeta("planner_last_wake", strconv.FormatInt(now.Unix(), 10)) // the restart kickoff counts as a wake
	data := map[string]any{"resumed": sid, "restarts_24h": len(recent), "why": why}
	if err != nil {
		data["error"] = firstLine(err.Error())
	}
	_ = r.Log.Emit("planner.restarted", nil, Event{Session: sid, Role: "planner", Data: data})
	r.printf("planner: restarted (%d in 24h): %s\n", len(recent), why)
	return err == nil
}

// PlannerWake wakes the planner now, past the debounce (`hivemill planner wake`).
func (r *Runner) PlannerWake(now time.Time, why string) error {
	list, ok := Terminals(r.Tools)
	if ok && !alive(list, true, PlannerName) {
		return fmt.Errorf("%w: the planner is not running (`hivemill planner start`)", ErrRefused)
	}
	if why == "" {
		why = "manual"
	}
	if !r.plannerWake(now, true, true, why) {
		return errors.New("the wake was not delivered")
	}
	return nil
}

// levelKinds are the work kinds that stand for a condition (see PlannerLevels): their row stays open while the condition
// holds and is closed "cleared" by the pass that sees it gone. Every other kind is an event row.
var levelKinds = map[string]bool{"queue_low": true, "not_dispatched": true, "goal_done": true}

// splitLevel cuts a level string ("queue_low:www=0/3") into its kind and ref ("queue_low", "www").
func splitLevel(l string) (kind, ref string) {
	kind, ref, _ = strings.Cut(l, ":")
	ref, _, _ = strings.Cut(ref, "=")
	return kind, ref
}

// plannerSync turns what the planner must know into rows: each level opens a row (idempotent) and a level that is gone
// closes its row "cleared"; each new event in the log and a manual wake open an event row. It returns the text of each
// current level by "kind:ref", and the event-log cursor to record once the rows exist.
func (r *Runner) plannerSync(s *SQLite, now time.Time, manual string) (map[string]string, int64, error) {
	levels, err := r.PlannerLevels()
	if err != nil {
		return nil, 0, err
	}
	text := map[string]string{}
	for _, l := range levels {
		k, ref := splitLevel(l)
		text[k+":"+ref] = l
		if _, _, err := s.PlannerWorkOpen(k, ref, now); err != nil {
			return nil, 0, err
		}
	}
	open, err := s.PlannerWorkOpenRows()
	if err != nil {
		return nil, 0, err
	}
	for _, w := range open {
		if _, held := text[w.Kind+":"+w.Ref]; levelKinds[w.Kind] && !held {
			if err := s.PlannerWorkClose(w.ID, "cleared", now); err != nil {
				return nil, 0, err
			}
		}
	}
	evs, cursor := r.wakeEvents()
	if manual != "" {
		evs = append(evs, "manual:"+strings.ReplaceAll(manual, " ", "_"))
	}
	for _, e := range evs {
		k, ref, _ := strings.Cut(e, ":")
		if _, _, err := s.PlannerWorkOpen(k, ref, now); err != nil {
			return nil, 0, err
		}
	}
	return text, cursor, nil
}

// plannerWake is the delivery pass. It syncs the rows, escalates a stuck planner, and when a row is due (never delivered,
// or delivered more than planner_ack_s ago) and the planner is idle and the gap has passed, types one wake line for the due
// rows. A row stays open until `hivemill planner done`; a row that cannot go out now (gap, busy, a failed send) stays due.
func (r *Runner) plannerWake(now time.Time, idle, force bool, manual string) bool {
	s, err := r.sq()
	if err != nil {
		r.printf("planner: %v\n", err)
		return false
	}
	text, cursor, err := r.plannerSync(s, now, manual)
	if err != nil {
		r.printf("planner: %v\n", err)
		return false
	}
	_ = r.Store.SetMeta("planner_cursor", strconv.FormatInt(cursor, 10))
	open, err := s.PlannerWorkOpenRows()
	if err != nil {
		r.printf("planner: %v\n", err)
		return false
	}
	ack := time.Duration(cfgInt(r.Store, "planner_ack_s")) * time.Second
	var due []PlannerWork
	for _, w := range open {
		if d, ok := parseWorkTime(w.Delivered); !ok || now.Sub(d) > ack {
			due = append(due, w)
		}
	}
	// how long the planner has been busy: the start is recorded the first pass that sees it working
	if idle {
		_ = r.Store.SetMeta("planner_busy_since", "")
	} else if r.Store.Meta("planner_busy_since") == "" {
		_ = r.Store.SetMeta("planner_busy_since", strconv.FormatInt(now.Unix(), 10))
	}
	if len(due) == 0 {
		_ = r.Store.SetMeta("planner_pending", "")
		return false
	}
	if why := r.plannerStuck(now, due, idle, ack); why != "" && !force {
		if r.plannerEscalate(now, why, due) {
			return false // the restart kickoff is the delivery
		}
	}
	if !force {
		var last time.Time
		if ts, err := strconv.ParseInt(r.Store.Meta("planner_last_wake"), 10, 64); err == nil {
			last = time.Unix(ts, 0)
		}
		gap := time.Duration(cfgInt(r.Store, "planner_wake_gap_s")) * time.Second
		why := ""
		if !last.IsZero() && now.Sub(last) < gap {
			why = fmt.Sprintf("debounce: next wake after %s", last.Add(gap).Format(time.RFC3339))
		} else if !idle {
			why = "planner busy"
		}
		if why != "" {
			var rs []string
			for _, w := range due {
				rs = append(rs, workReason(w, text))
			}
			_ = r.Store.SetMeta("planner_pending", why+": "+strings.Join(Coalesce(rs, nil), " "))
			return false
		}
	}
	asks := 0
	open2, _ := s.Asks("open")
	asks = len(open2)
	var items, reasons []string
	var sent []PlannerWork
	size := 0
	for _, w := range due {
		it := fmt.Sprintf("%d:%s:%s", w.ID, w.Kind, w.Ref)
		if size+len(it) > 800 && len(sent) > 0 {
			break // the rest stays due and goes out with the next wake
		}
		size += len(it) + 1
		items, reasons, sent = append(items, it), append(reasons, workReason(w, text)), append(sent, w)
	}
	msg := WakeMessage(now, Coalesce(reasons, nil), asks, items)
	if err := r.Prompt(PlannerName, msg); err != nil {
		_ = r.Store.SetMeta("planner_pending", "send failed: "+firstLine(err.Error()))
		return false
	}
	for _, w := range sent {
		_ = s.PlannerWorkDelivered(w.ID, now)
	}
	_ = r.Store.SetMeta("planner_last_wake", strconv.FormatInt(now.Unix(), 10))
	_ = r.Store.SetMeta("planner_pending", "")
	_ = r.Log.Emit("planner.woken", nil, Event{Role: "planner", Data: map[string]any{"reasons": reasons, "items": items}})
	return true
}

func parseWorkTime(v string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

// workReason is the wake-line reason of a row: a level shows its current numbers, an event is "kind:ref".
func workReason(w PlannerWork, levels map[string]string) string {
	if l, ok := levels[w.Kind+":"+w.Ref]; ok && levelKinds[w.Kind] {
		return l
	}
	if w.Ref == "" {
		return w.Kind
	}
	return w.Kind + ":" + w.Ref
}

// plannerStuck says why the planner needs a restart ("" = it does not): a due row that was delivered 3 times without an
// acknowledgement, or a planner busy longer than planner_busy_max_s while a row is due.
func (r *Runner) plannerStuck(now time.Time, due []PlannerWork, idle bool, ack time.Duration) string {
	for _, w := range due {
		if w.Attempts >= 3 {
			return fmt.Sprintf("item %d (%s:%s) delivered %d times without done", w.ID, w.Kind, w.Ref, w.Attempts)
		}
	}
	if !idle {
		if ts, err := strconv.ParseInt(r.Store.Meta("planner_busy_since"), 10, 64); err == nil {
			since := time.Unix(ts, 0)
			// the clock starts when the oldest row became due, so long work the planner started before it is not a stall
			for _, w := range due {
				at, ok := parseWorkTime(w.Opened)
				if d, dok := parseWorkTime(w.Delivered); dok {
					at, ok = d.Add(ack), true
				}
				if ok && at.After(since) {
					since = at
				}
				break // due is oldest first
			}
			if max := time.Duration(cfgInt(r.Store, "planner_busy_max_s")) * time.Second; max > 0 && now.Sub(since) > max {
				return fmt.Sprintf("busy for %s with %d item(s) due", now.Sub(since).Round(time.Second), len(due))
			}
		}
	}
	return ""
}

// plannerEscalate restarts the stuck planner. Past the restart limit it raises the hivemill-planner owner alert once per
// row instead. It reports whether the planner was restarted.
func (r *Runner) plannerEscalate(now time.Time, why string, due []PlannerWork) bool {
	if r.Store.Meta("planner_lost") == "" && r.plannerRevive(now, why) {
		if s, err := r.sq(); err == nil {
			_ = s.PlannerWorkRestarted(now)
		}
		_ = r.Store.SetMeta("planner_busy_since", "")
		return true
	}
	if r.Store.Meta("planner_lost") == "" {
		return false // the relaunch failed: not the restart limit, so no alert; the next pass tries again
	}
	alerted := csv(r.Store.Meta("planner_alerted"))
	for _, w := range due {
		id := strconv.FormatInt(w.ID, 10)
		if !(w.Attempts >= 3 || strings.HasPrefix(why, "busy")) || slices.Contains(alerted, id) {
			continue
		}
		alerted = append(alerted, id)
		what := fmt.Sprintf("planner work item %d (%s:%s) is stuck: %s", w.ID, w.Kind, w.Ref, why)
		_ = r.Store.AlertFeed("hivemill-planner", what, "hivemill planner status")
		r.ownerAlert("hivemill-planner", what+". The planner restart limit is used up.")
	}
	_ = r.Store.SetMeta("planner_alerted", strings.Join(alerted, ","))
	return false
}

// Coalesce groups event reasons by kind ("task.failed:T-1" + "task.failed:T-2" = "task.failed:T-1,T-2"), keeps first-seen
// order, drops duplicates and appends the levels.
func Coalesce(events, levels []string) []string {
	var kinds []string
	ids := map[string][]string{}
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e] {
			continue
		}
		seen[e] = true
		k, id, _ := strings.Cut(e, ":")
		if _, ok := ids[k]; !ok {
			kinds = append(kinds, k)
		}
		if id != "" {
			ids[k] = append(ids[k], id)
		} else if ids[k] == nil {
			ids[k] = []string{}
		}
	}
	var out []string
	for _, k := range kinds {
		if len(ids[k]) == 0 {
			out = append(out, k)
		} else {
			out = append(out, k+":"+strings.Join(ids[k], ","))
		}
	}
	return append(out, levels...)
}

// WakeMessage is the one machine-readable line typed into the planner.
func WakeMessage(now time.Time, reasons []string, asksOpen int, items []string) string {
	var b strings.Builder
	n := 0
	for _, x := range reasons {
		if b.Len()+len(x) > 600 {
			break
		}
		if n > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(x)
		n++
	}
	if n < len(reasons) {
		fmt.Fprintf(&b, " +%d more", len(reasons)-n)
	}
	return fmt.Sprintf("hivemill-wake v1 at=%s asks_open=%d items=%s reasons=%s", now.UTC().Format(time.RFC3339), asksOpen,
		strings.Join(items, ","), b.String())
}

// wakeKinds are the event-log kinds that wake the planner.
var wakeKinds = map[string]bool{"task.failed": true, "run.held": true, "budget.reached": true, "goal.checked": true, "ask.answered": true}

// wakeEvents reads the event log from the planner's byte cursor: the wake reasons in it, and the offset after its last
// complete line. A log shorter than the cursor was replaced, so it is read from the start.
func (r *Runner) wakeEvents() ([]string, int64) {
	cur, _ := strconv.ParseInt(r.Store.Meta("planner_cursor"), 10, 64)
	f, err := os.Open(r.Log.Path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < cur {
		cur = 0
	}
	if _, err := f.Seek(cur, io.SeekStart); err != nil {
		return nil, cur
	}
	var out []string
	rd := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			break // EOF, or a line still being written: it is read on the next pass
		}
		cur += int64(len(line))
		if reason := wakeReason(line); reason != "" {
			out = append(out, reason)
		}
	}
	return out, cur
}

func wakeReason(line []byte) string {
	var e Event
	if json.Unmarshal(line, &e) != nil || !wakeKinds[e.Event] {
		return ""
	}
	switch e.Event {
	case "budget.reached":
		return "budget.reached"
	case "goal.checked":
		return "goal.checked:" + e.GoalID
	case "ask.answered":
		return fmt.Sprintf("ask.answered:#%v", e.Data["id"])
	}
	return e.Event + ":" + e.TaskID
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// PlannerLevels are the level reasons: projects with an active goal whose ready queue is below planner_queue_min, projects
// with an active goal that a pass never dispatches (not in config projects), and active goals whose tasks are all done.
func (r *Runner) PlannerLevels() ([]string, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	goals, err := s.Goals("active")
	if err != nil {
		return nil, err
	}
	dispatched := map[string]bool{}
	for _, p := range csv(r.Store.Cfg("projects")) {
		dispatched[p] = true
	}
	want := cfgInt(r.Store, "planner_queue_min")
	projects := map[string]bool{}
	var out []string
	for _, g := range goals {
		if g.Project != "" && !projects[g.Project] {
			projects[g.Project] = true
			ready, err := r.ReadyIn([]string{g.Project})
			if err != nil {
				return nil, err
			}
			if len(ready) < want {
				out = append(out, fmt.Sprintf("queue_low:%s=%d/%d", g.Project, len(ready), want))
			}
			if !dispatched[g.Project] {
				out = append(out, "not_dispatched:"+g.Project)
			}
		}
		ts, err := s.Tasks(g.ID, "")
		if err != nil {
			return nil, err
		}
		open, done := 0, 0
		for _, t := range ts {
			switch t.Status {
			case "done":
				done++
			case "cancelled":
			default:
				open++
			}
		}
		if open == 0 && done > 0 {
			out = append(out, "goal_done:"+g.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// syncHeldAsks files one owner question per held run (a run is never held silently), and withdraws the question once the
// run is no longer held (the owner merged or closed the PR).
func (r *Runner) syncHeldAsks() {
	s, err := r.sq()
	if err != nil {
		return
	}
	runs, _ := r.Store.Runs()
	held := map[string]bool{}
	for i := range runs {
		x := &runs[i]
		if !heldForOwner(x) || x.Eval {
			continue
		}
		ref := fmt.Sprintf("held:%s:%d", x.Task, x.Round)
		held[ref] = true
		q := fmt.Sprintf("%s is held for you: %s. PR %s. Merge it by hand, close it, or answer with what to change.", x.Task,
			truncate(or(x.MergeHold, x.FailReason), 300), or(x.PRURL, "-"))
		if id, created, err := s.AddAsk(Ask{Ref: ref, Project: x.Project, Goal: x.Goal, Task: x.Task, Question: q, AskedBy: "runner"}); err == nil && created {
			_ = r.Log.Emit("ask.added", nil, Event{Project: x.Project, Data: map[string]any{"id": id, "by": "runner", "task": x.Task}})
		}
	}
	open, _ := s.Asks("open")
	for _, a := range open {
		if strings.HasPrefix(a.Ref, "held:") && !held[a.Ref] {
			_ = s.CloseAsk(a.ID, "withdrawn", "the run is no longer held", "runner")
		}
	}
}

// PlannerStatus prints the planner's state.
func (r *Runner) PlannerStatus(now time.Time) error {
	on := or(r.Store.Meta("planner"), "off")
	term := "missing"
	if list, ok := Terminals(r.Tools); !ok {
		term = "unknown (hc cannot list)"
	} else {
		for _, t := range list {
			if t.Name == PlannerName && t.State == "live" {
				term = "live " + t.Status
			}
		}
	}
	model, effort, _ := r.plannerModel()
	r.printf("planner %s, terminal %s, %s effort %s, session %s\n", on, term, model, effort, or(r.Store.Meta("planner_sid"), "-"))
	if ts, err := strconv.ParseInt(r.Store.Meta("planner_last_wake"), 10, 64); err == nil {
		r.printf("last wake %s (%s ago)\n", time.Unix(ts, 0).Format(time.RFC3339), now.Sub(time.Unix(ts, 0)).Round(time.Second))
	}
	r.printf("restarts in 24h: %d of %d%s\n", len(csv(r.Store.Meta("planner_resumes"))), cfgInt(r.Store, "resume_max"),
		map[bool]string{true: ", LOST since " + r.Store.Meta("planner_lost")}[r.Store.Meta("planner_lost") != ""])
	if p := r.Store.Meta("planner_pending"); p != "" {
		r.printf("pending: %s\n", p)
	}
	levels, err := r.PlannerLevels()
	if err != nil {
		return err
	}
	r.printf("levels: %s\n", or(strings.Join(levels, " "), "none"))
	if s, err := r.sq(); err == nil {
		open, _ := s.Asks("open")
		r.printf("open owner questions: %d\n", len(open))
		rows, _ := s.PlannerWorkOpenRows()
		r.printf("open work items: %d\n", len(rows))
		for _, w := range rows {
			age := "-"
			if t, ok := parseWorkTime(w.Opened); ok {
				age = now.Sub(t).Round(time.Second).String()
			}
			r.printf("  %d  %s  %s  age %s  attempts %d\n", w.ID, w.Kind, or(w.Ref, "-"), age, w.Attempts)
		}
	}
	return nil
}
