package hm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		r.plannerRevive(now)
		return
	}
	r.plannerWake(now, t.Status == "idle" || t.Status == "done", false, "")
}

// plannerRevive restarts a dead planner with `claude --resume` of its last session, at most resume_max times in 24 h;
// after that it opens one owner alert and stops trying until `hivemill planner start`.
func (r *Runner) plannerRevive(now time.Time) {
	if r.Store.Meta("planner_lost") != "" {
		return
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
		return
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
	data := map[string]any{"resumed": sid, "restarts_24h": len(recent)}
	if err != nil {
		data["error"] = firstLine(err.Error())
	}
	_ = r.Log.Emit("planner.restarted", nil, Event{Session: sid, Role: "planner", Data: data})
	r.printf("planner: restarted (%d in 24h)\n", len(recent))
}

// PlannerWake wakes the planner now, past the debounce (`hivemill planner wake`).
func (r *Runner) PlannerWake(now time.Time, why string) error {
	list, ok := Terminals(r.Tools)
	if ok && !alive(list, true, PlannerName) {
		return fmt.Errorf("%w: the planner is not running (`hivemill planner start`)", ErrRefused)
	}
	if !r.plannerWake(now, true, true, why) {
		return errors.New("the wake was not delivered")
	}
	return nil
}

// plannerWake decides and, when due, sends one wake line. Reasons that cannot go out now (debounce, busy, a failed
// send) stay pending: the event cursor only moves once the planner has them.
func (r *Runner) plannerWake(now time.Time, idle, force bool, manual string) bool {
	levels, err := r.PlannerLevels()
	if err != nil {
		r.printf("planner: %v\n", err)
		return false
	}
	evs, cursor := r.wakeEvents()
	if manual != "" {
		evs = append(evs, "manual:"+strings.ReplaceAll(manual, " ", "_"))
	}
	r.enqueuePlannerWork(nil, levels, true)
	var last time.Time
	if ts, err := strconv.ParseInt(r.Store.Meta("planner_last_wake"), 10, 64); err == nil {
		last = time.Unix(ts, 0)
	}
	d := DecideWake(WakeInput{Now: now, LastWake: last, Levels: levels, Events: evs, LastFingerprint: r.Store.Meta("planner_fp"),
		MinGap: time.Duration(cfgInt(r.Store, "planner_wake_gap_s")) * time.Second, Idle: idle, Force: force})
	if !d.Wake {
		if d.Advance {
			_ = r.Store.SetMeta("planner_cursor", strconv.FormatInt(cursor, 10))
			_ = r.Store.SetMeta("planner_fp", d.Fingerprint)
			_ = r.Store.SetMeta("planner_pending", "")
		} else {
			_ = r.Store.SetMeta("planner_pending", d.Why+": "+strings.Join(d.Reasons, " "))
		}
		return false
	}
	asks := 0
	if s, err := r.sq(); err == nil {
		open, _ := s.Asks("open")
		asks = len(open)
	}
	msg := WakeMessage(now, d.Reasons, asks)
	if err := r.Prompt(PlannerName, msg); err != nil {
		_ = r.Store.SetMeta("planner_pending", "send failed: "+firstLine(err.Error()))
		return false
	}
	r.enqueuePlannerWork(evs, nil, false)
	_ = r.Store.SetMeta("planner_cursor", strconv.FormatInt(cursor, 10))
	_ = r.Store.SetMeta("planner_fp", d.Fingerprint)
	_ = r.Store.SetMeta("planner_last_wake", strconv.FormatInt(now.Unix(), 10))
	_ = r.Store.SetMeta("planner_pending", "")
	_ = r.Log.Emit("planner.woken", nil, Event{Role: "planner", Data: map[string]any{"reasons": d.Reasons}})
	return true
}

// enqueuePlannerWork records wake reasons as open planner_work rows ("task.failed:T-1" is kind task.failed, ref T-1). A level
// ("queue_low:www=0/3", kind queue_low, ref www) is a state: its row is opened while it holds and closed as cleared once it
// no longer does. Events are enqueued only when the event cursor moves past them, so a replayed event never reopens a row.
func (r *Runner) enqueuePlannerWork(events, levels []string, syncLevels bool) {
	s, err := r.sq()
	if err != nil {
		return
	}
	add := func(x string, isLevel bool) {
		kind, ref, _ := strings.Cut(x, ":")
		if isLevel {
			ref, _, _ = strings.Cut(ref, "=")
		}
		if err := s.EnqueuePlannerWork(kind, ref); err != nil {
			r.printf("planner: work queue: %v\n", err)
		}
	}
	for _, x := range events {
		add(x, false)
	}
	if !syncLevels {
		return
	}
	live := map[string]bool{}
	for _, x := range levels {
		add(x, true)
		kind, ref, _ := strings.Cut(x, ":")
		ref, _, _ = strings.Cut(ref, "=")
		live[kind+":"+ref] = true
	}
	open, err := s.OpenPlannerWork()
	if err != nil {
		r.printf("planner: work queue: %v\n", err)
		return
	}
	for _, w := range open {
		if plannerLevelKinds[w.Kind] && !live[w.Kind+":"+w.Ref] {
			_ = s.ClosePlannerWork([]int64{w.ID}, "cleared")
		}
	}
}

var plannerLevelKinds = map[string]bool{"queue_low": true, "not_dispatched": true, "goal_done": true}

// PlannerDone closes planner work rows with close_reason done. An unknown or closed id fails and closes none.
func (r *Runner) PlannerDone(ids []int64, note string) error {
	s, err := r.sq()
	if err != nil {
		return err
	}
	if err := s.ClosePlannerWork(ids, "done"); err != nil {
		return err
	}
	_ = r.Log.Emit("planner.work_done", nil, Event{Role: r.Env.Actor, Data: map[string]any{"ids": ids, "note": note}})
	return nil
}

// WakeInput is what DecideWake reads: level reasons (a state that holds, such as a short queue) and event reasons (what
// happened since the last delivered wake).
type WakeInput struct {
	Now, LastWake   time.Time
	Levels, Events  []string
	LastFingerprint string // the levels the planner last heard about
	MinGap          time.Duration
	Idle, Force     bool // Idle: the planner waits for input; Force: a manual wake (no debounce, no busy check)
}

// WakeDecision says whether to wake the planner now and with what.
type WakeDecision struct {
	Wake        bool
	Reasons     []string
	Fingerprint string
	Advance     bool   // no wake, and nothing is pending: record the cursor and the fingerprint
	Why         string // why there is no wake
}

// DecideWake is the pure wake rule: wake when an event happened or a level appeared that the planner has not heard about,
// at most once per MinGap and only while the planner is idle. A level that has not changed never wakes it again, and
// neither does one that went away.
func DecideWake(in WakeInput) WakeDecision {
	levels := append([]string(nil), in.Levels...)
	sort.Strings(levels)
	d := WakeDecision{Fingerprint: strings.Join(levels, " ")}
	heard := map[string]bool{}
	for _, x := range strings.Fields(in.LastFingerprint) {
		heard[x] = true
	}
	changed := len(in.Events) > 0
	for _, x := range levels {
		changed = changed || !heard[x] // a level that went away (a refilled queue) is no news
	}
	if !changed && !in.Force {
		d.Advance, d.Why = true, "nothing changed"
		return d
	}
	d.Reasons = Coalesce(in.Events, levels)
	if !in.Force {
		if !in.LastWake.IsZero() && in.Now.Sub(in.LastWake) < in.MinGap {
			d.Why = fmt.Sprintf("debounce: next wake after %s", in.LastWake.Add(in.MinGap).Format(time.RFC3339))
			return d
		}
		if !in.Idle {
			d.Why = "planner busy"
			return d
		}
	}
	if len(d.Reasons) == 0 {
		d.Reasons = []string{"manual"}
	}
	d.Wake = true
	return d
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
func WakeMessage(now time.Time, reasons []string, asksOpen int) string {
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
	return fmt.Sprintf("hivemill-wake v1 at=%s asks_open=%d reasons=%s", now.UTC().Format(time.RFC3339), asksOpen, b.String())
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
		work, werr := s.OpenPlannerWork()
		if werr != nil {
			return werr
		}
		r.printf("open work: %d\n", len(work))
		for _, w := range work {
			age := "?"
			if t, err := time.Parse("2006-01-02T15:04:05-07:00", w.Opened); err == nil {
				age = now.Sub(t).Round(time.Second).String()
			}
			r.printf("  #%d %s %s age %s attempts %d\n", w.ID, w.Kind, w.Ref, age, w.Attempts)
		}
	}
	return nil
}
