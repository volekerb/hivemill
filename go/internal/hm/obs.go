package hm

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Observability (docs/hivemill-observability.md): one model built from the event logs of every machine, read by the
// `hivemill obs` page and the OpenTelemetry export. It reads; it never changes a run.

func init() {
	Defaults["machine_name"] = ""             // stamps every event line (field machine); empty = the hostname
	Defaults["obs_port"] = "7461"             // `hivemill obs`: the read-only page on 127.0.0.1:<port>
	Defaults["otel_endpoint"] = ""            // OTLP/HTTP base URL: the daemon sends each finished task as a trace; empty = off
	Defaults["obs_banner_polling_pct"] = "20" // the Spend page warns when polling is above this percent of spend
	Defaults["obs_banner_rounds_pct"] = "30"  // the Flow API returns a banner when more than this percent of PRs needed 2+ review rounds
	Defaults["obs_banner_stage_pct"] = "30"   // the Flow page warns when one stage holds more than this percent of stage hours
}

// MachineName is the configured machine name, else the short hostname.
func MachineName(cfg string) string {
	if s := strings.TrimSpace(cfg); s != "" {
		return s
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "local"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// ObsSource is one event log: this machine's own, or a copy pulled from another machine (`hivemill obs pull`).
type ObsSource struct {
	Label  string `json:"label"` // the machine name stamped on lines that have none
	Path   string `json:"path"`
	Own    bool   `json:"local"`            // this machine's own log
	Pulled string `json:"pulled,omitempty"` // when the copy was pulled
	From   string `json:"from,omitempty"`
}

// ObsDir holds the pulled copies: <state>/obs/<label>/events.jsonl.
func (e Env) ObsDir() string { return filepath.Join(e.StateDir(), "obs") }

// ObsSources is the local log plus every pulled copy (withPulled).
func (r *Runner) ObsSources(withPulled bool) []ObsSource {
	out := []ObsSource{{Label: r.Machine(), Path: r.Log.Path, Own: true}}
	if !withPulled {
		return out
	}
	dirs, _ := os.ReadDir(r.Env.ObsDir())
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		src := ObsSource{Label: d.Name(), Path: filepath.Join(r.Env.ObsDir(), d.Name(), "events.jsonl")}
		if _, err := os.Stat(src.Path); err != nil {
			continue
		}
		var m pullMeta
		if b, err := os.ReadFile(filepath.Join(r.Env.ObsDir(), d.Name(), "meta.json")); err == nil && json.Unmarshal(b, &m) == nil {
			src.Pulled, src.From = m.Pulled, m.From
		}
		out = append(out, src)
	}
	return out
}

// Machine is this machine's name (config machine_name, else the hostname).
func (r *Runner) Machine() string {
	if r.Log.Machine != "" {
		return r.Log.Machine
	}
	return MachineName(r.Store.Cfg("machine_name"))
}

// LoadObsEvents reads every source and orders everything by time. A line already read from another source (the same
// bytes, e.g. the local log also pulled under another label) is dropped; repeats within one file are kept, as the
// report counts them. A line without a machine gets its source's name: for a pulled log whose stamped lines all name
// one machine, that machine (so an ssh alias and the remote machine_name never show one box twice), else the label.
func LoadObsEvents(srcs []ObsSource) ([]Event, error) {
	from := map[[32]byte]int{}
	var out []Event
	for i, s := range srcs {
		f, err := os.Open(s.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var evs []Event
		named := map[string]bool{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			h := sha256.Sum256(sc.Bytes())
			if j, ok := from[h]; ok && j != i {
				continue
			}
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Event == "" {
				continue
			}
			from[h] = i
			if e.Machine != "" {
				named[e.Machine] = true
			}
			evs = append(evs, e)
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
		name := s.Label
		if ms := setKeys(named); !s.Own && len(ms) == 1 {
			name = ms[0]
		}
		for _, e := range evs {
			e.Machine, e.Harness = or(e.Machine, name), or(e.Harness, Harness)
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return evTime(out[i]).Before(evTime(out[j])) })
	return out, nil
}

func evTime(e Event) time.Time {
	t, _ := time.Parse(time.RFC3339, e.TS)
	return t
}

// ObsFilter narrows the model to one machine and/or one harness (empty = all).
type ObsFilter struct {
	Machine string `json:"machine,omitempty"`
	Harness string `json:"harness,omitempty"`
}

func (f ObsFilter) keep(e Event) bool {
	return (f.Machine == "" || e.Machine == f.Machine) && (f.Harness == "" || e.Harness == f.Harness)
}

// ObsSession is one agent session: its role, model, harness, machine, time span, $ and tokens.
type ObsSession struct {
	ID        string    `json:"id"`
	TaskKey   string    `json:"task_key,omitempty"`
	Project   string    `json:"project,omitempty"`
	Role      string    `json:"role"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Harness   string    `json:"harness"`
	Machine   string    `json:"machine"`
	Start     string    `json:"start"`
	End       string    `json:"end,omitempty"` // the last session.ended line
	Usage     SessUsage `json:"usage"`
	CostKnown bool      `json:"cost_known"` // false: no session.ended line and no local transcript (a remote run still active)
	Events    []Event   `json:"-"`
}

// ObsTask is one task on one machine: its scoreboard row, sessions and events. Task ids are per factory DB, so the
// key is machine/task.
type ObsTask struct {
	Key      string   `json:"key"`
	Task     string   `json:"task"`
	Goal     string   `json:"goal,omitempty"`
	Project  string   `json:"project,omitempty"`
	Machine  string   `json:"machine"`
	Harness  string   `json:"harness"`
	Start    string   `json:"start"`
	Last     string   `json:"last"`
	Terminal string   `json:"terminal,omitempty"` // the ts of the last terminal event (merged, failed, cancelled, eval done)
	PR       string   `json:"pr,omitempty"`
	Rounds   int      `json:"rounds"` // judge verdicts + red CI rounds (the 2+ rounds rule of the report)
	Row      Row      `json:"row"`
	Sessions []string `json:"sessions"`
	Events   []Event  `json:"-"`
}

// Obs is the whole model.
type Obs struct {
	Now       time.Time              `json:"now"`
	Filter    ObsFilter              `json:"filter"`
	Tasks     map[string]*ObsTask    `json:"-"`
	Sessions  map[string]*ObsSession `json:"-"`
	Machines  []string               `json:"machines"`  // every machine in the logs (before the filter)
	Harnesses []string               `json:"harnesses"` // every harness in the logs (before the filter)
	LastSeen  map[string]string      `json:"last_seen"` // machine -> its newest event
	// EventAsks is the open owner questions per machine and project, from ask.added minus ask.answered/withdrawn.
	EventAsks map[string]map[string]int `json:"-"`
}

// OpenAsks is the open owner questions per project: this machine's from its DB (local, when ok), every other
// machine's from its event log.
func (o *Obs) OpenAsks(me string, local map[string]int, ok bool) map[string]int {
	out := map[string]int{}
	for m, by := range o.EventAsks {
		if m == me && ok {
			continue
		}
		for p, n := range by {
			out[p] += n
		}
	}
	if ok {
		for p, n := range local {
			out[p] += n
		}
	}
	return out
}

var terminalEvents = map[string]bool{"pr.merged": true, "task.failed": true, "task.cancelled": true, "eval.done": true,
	"pr.reverted": true}

// TaskKey is machine/task.
func TaskKey(machine, task string) string { return machine + "/" + task }

// BuildObs folds the events into tasks and sessions. local is the usage read from this machine's transcripts: it
// overrides the session.ended figure of a session this machine ran (it is current while a session still runs).
func BuildObs(evs []Event, f ObsFilter, localMachine string, local map[string]SessUsage, now time.Time) *Obs {
	o := &Obs{Now: now, Filter: f, Tasks: map[string]*ObsTask{}, Sessions: map[string]*ObsSession{}, LastSeen: map[string]string{}}
	ms, hs := map[string]bool{}, map[string]bool{}
	openAsk := map[string][2]string{} // machine/id -> machine, project
	for _, e := range evs {
		ms[e.Machine], hs[e.Harness] = true, true
		if evTime(e).After(parseTS(o.LastSeen[e.Machine])) {
			o.LastSeen[e.Machine] = e.TS
		}
		if !f.keep(e) {
			continue
		}
		if strings.HasPrefix(e.Event, "ask.") {
			if id, ok := e.Data["id"].(float64); ok {
				k := fmt.Sprintf("%s/%d", e.Machine, int64(id))
				switch e.Event {
				case "ask.added":
					openAsk[k] = [2]string{e.Machine, e.Project}
				case "ask.answered", "ask.withdrawn":
					delete(openAsk, k)
				}
			}
		}
		var t *ObsTask
		if e.TaskID != "" {
			k := TaskKey(e.Machine, e.TaskID)
			if t = o.Tasks[k]; t == nil {
				t = &ObsTask{Key: k, Task: e.TaskID, Machine: e.Machine, Harness: e.Harness, Start: e.TS,
					Row: Row{Task: e.TaskID, Outcome: "open", Sessions: map[string]string{}}}
				o.Tasks[k] = t
			}
			t.Goal, t.Project = or(e.GoalID, t.Goal), or(e.Project, t.Project)
			t.Last = e.TS
			t.Events = append(t.Events, e)
			t.Row.apply(e)
			if terminalEvents[e.Event] {
				t.Terminal = e.TS
			}
			if pr, _ := e.Data["pr"].(string); pr != "" && e.Event != "ask.added" {
				t.PR = pr
			}
		}
		if e.Session == "" {
			continue
		}
		s := o.Sessions[e.Session]
		if s == nil {
			s = &ObsSession{ID: e.Session, Role: e.Role, Harness: e.Harness, Machine: e.Machine, Start: e.TS}
			o.Sessions[e.Session] = s
		}
		s.Events = append(s.Events, e)
		s.Role, s.Model, s.Effort = or(s.Role, e.Role), or(e.Model, s.Model), or(e.Effort, s.Effort)
		if t != nil {
			s.TaskKey, s.Project = t.Key, or(t.Project, s.Project)
			if _, ok := t.Row.Sessions[e.Session]; !ok && e.Role != "" {
				t.Row.Sessions[e.Session] = e.Role // a session seen only in its session.ended line still counts
			}
		} else if e.Project != "" {
			s.Project = e.Project
		}
		if e.Event == "session.ended" {
			if u, ok := usageOf(e); ok { // cumulative: the last line wins, never a sum
				s.Usage, s.CostKnown, s.End = u, true, e.TS
			}
		}
	}
	for _, s := range o.Sessions {
		if u, ok := local[s.ID]; ok && s.Machine == localMachine && (u.USD > 0 || !s.CostKnown) {
			s.Usage, s.CostKnown = u, true
		}
	}
	costs := map[string]float64{}
	for id, s := range o.Sessions {
		costs[id] = s.Usage.USD
	}
	for _, t := range o.Tasks {
		t.Row.finish(costs)
		t.Rounds = len(t.Row.Judge) + t.Row.CIRounds
		for sid := range t.Row.Sessions {
			t.Sessions = append(t.Sessions, sid)
		}
		sort.Slice(t.Sessions, func(i, j int) bool {
			a, b := o.Sessions[t.Sessions[i]], o.Sessions[t.Sessions[j]]
			if a == nil || b == nil {
				return t.Sessions[i] < t.Sessions[j]
			}
			return parseTS(a.Start).Before(parseTS(b.Start))
		})
	}
	o.EventAsks = map[string]map[string]int{}
	for _, v := range openAsk {
		if o.EventAsks[v[0]] == nil {
			o.EventAsks[v[0]] = map[string]int{}
		}
		o.EventAsks[v[0]][v[1]]++
	}
	o.Machines, o.Harnesses = setKeys(ms), setKeys(hs)
	return o
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TaskList is the tasks in key order, optionally only those of a project or goal (empty = any).
func (o *Obs) TaskList(project, goal string) []*ObsTask {
	var out []*ObsTask
	for _, t := range o.Tasks {
		if (project == "" || t.Project == project) && (goal == "" || t.Goal == goal) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ProjectLine is one project of the portfolio. The spend columns sum the sessions started in the window (a
// session's whole cost counts on its start day, as in the digest); the outcome columns are the report's Summarise
// over the tasks active in the last Days days.
type ProjectLine struct {
	Project     string   `json:"project"`
	USDToday    float64  `json:"usd_today"`
	USD7d       float64  `json:"usd_7d"`
	USD30d      float64  `json:"usd_30d"`
	Tasks       int      `json:"tasks"`
	Active      int      `json:"active"` // tasks without a terminal event
	Merged      int      `json:"merged"`
	USDWindow   float64  `json:"usd_window"`
	PerMergedPR *float64 `json:"usd_per_merged_pr"`
	TwoPlusPct  *float64 `json:"two_plus_rounds_pct"`
	MedianHours *float64 `json:"median_hours_to_merge"`
	OpenAsks    int      `json:"open_asks"`
	CostPending int      `json:"cost_pending"` // sessions in the window with no known cost yet
}

// NoProject labels sessions that belong to no project (the planner).
const NoProject = "(no project)"

// Portfolio is one line per project plus a total line (Project "all"), over the last days days.
func (o *Obs) Portfolio(days int, asks map[string]int) []ProjectLine {
	if days <= 0 {
		days = 30
	}
	y, m, d := o.Now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, o.Now.Location())
	w7, w30, wN := o.Now.AddDate(0, 0, -7), o.Now.AddDate(0, 0, -30), o.Now.AddDate(0, 0, -days)
	lines := map[string]*ProjectLine{}
	get := func(p string) *ProjectLine {
		if p == "" {
			p = NoProject
		}
		if lines[p] == nil {
			lines[p] = &ProjectLine{Project: p}
		}
		return lines[p]
	}
	all := &ProjectLine{Project: "all"}
	for _, s := range o.Sessions {
		at := parseTS(s.Start)
		for _, l := range []*ProjectLine{get(s.Project), all} {
			if !at.Before(today) {
				l.USDToday += s.Usage.USD
			}
			if at.After(w7) {
				l.USD7d += s.Usage.USD
			}
			if at.After(w30) {
				l.USD30d += s.Usage.USD
			}
			if at.After(wN) && !s.CostKnown {
				l.CostPending++
			}
		}
	}
	rows := map[string][]Row{}
	var allRows []Row
	for _, t := range o.Tasks {
		l := get(t.Project)
		l.Tasks++
		all.Tasks++
		if t.Terminal == "" && t.Row.Outcome == "open" {
			l.Active++
			all.Active++
		}
		if parseTS(t.Last).After(wN) {
			rows[l.Project] = append(rows[l.Project], t.Row)
			allRows = append(allRows, t.Row)
		}
	}
	for p, n := range asks {
		get(p).OpenAsks += n
		all.OpenAsks += n
	}
	fill := func(l *ProjectLine, rs []Row) {
		tot := Summarise(rs)
		l.Merged, l.USDWindow, l.PerMergedPR, l.TwoPlusPct, l.MedianHours = tot.Merged, tot.USD, tot.PerMergedPR,
			tot.TwoPlusRoundsPct, tot.MedianHours
		l.USDToday, l.USD7d, l.USD30d = round2(l.USDToday), round2(l.USD7d), round2(l.USD30d)
	}
	out := make([]ProjectLine, 0, len(lines)+1)
	for p, l := range lines {
		fill(l, rows[p])
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	fill(all, allRows)
	return append(out, *all)
}

// GoalLine is one goal of a project.
type GoalLine struct {
	Goal     string          `json:"goal"`
	Title    string          `json:"title,omitempty"`
	Tasks    int             `json:"tasks"`
	Merged   int             `json:"merged"`
	Active   int             `json:"active"`
	Failed   int             `json:"failed"`
	USD      float64         `json:"usd"`
	Rounds   int             `json:"rounds"`
	Machines map[string]bool `json:"machines"`
}

// MachineList is the goal's machines, sorted.
func (g GoalLine) MachineList() string { return strings.Join(setKeys(g.Machines), ", ") }

// Goals is one line per goal of the project ("" = tasks without a goal).
func (o *Obs) Goals(project string) []GoalLine {
	by := map[string]*GoalLine{}
	for _, t := range o.TaskList(project, "") {
		g := by[t.Goal]
		if g == nil {
			g = &GoalLine{Goal: t.Goal, Machines: map[string]bool{}}
			by[t.Goal] = g
		}
		g.Tasks++
		g.USD += t.Row.USD
		g.Rounds += t.Rounds
		g.Machines[t.Machine] = true
		switch {
		case t.Row.Outcome == "merged":
			g.Merged++
		case strings.HasPrefix(t.Row.Outcome, "FAIL"):
			g.Failed++
		case t.Terminal == "" && t.Row.Outcome == "open":
			g.Active++
		}
	}
	out := make([]GoalLine, 0, len(by))
	for _, g := range by {
		g.USD = round2(g.USD)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Goal < out[j].Goal })
	return out
}

// TaskSessions is the task's sessions in start order.
func (o *Obs) TaskSessions(t *ObsTask) []*ObsSession {
	var out []*ObsSession
	for _, id := range t.Sessions {
		if s := o.Sessions[id]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

// LoadObs reads every source and builds the model, pricing this machine's sessions from their transcripts.
func (r *Runner) LoadObs(f ObsFilter, withPulled bool, now time.Time) (*Obs, error) {
	evs, err := LoadObsEvents(r.ObsSources(withPulled))
	if err != nil {
		return nil, err
	}
	me := r.Machine()
	var ids []string
	for _, e := range evs {
		if e.Session != "" && e.Machine == me {
			ids = append(ids, e.Session)
		}
	}
	return BuildObs(evs, f, me, SessionUsages(r.Env.UserHome, uniq(ids)), now), nil
}

func uniq(xs []string) []string {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return setKeys(m)
}
