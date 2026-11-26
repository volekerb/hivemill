package hm

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"
)

// The dashboard pages of `hivemill obs`: Now, Spend and Flow. Each is a plain model built from the Obs (so the numbers
// are the report's: the same SessionUsages, the same Row rules) that the template and /api/obs/* both render.

// StuckAfter is how long a run may sit in one stage before the Flow page lists it.
const StuckAfter = time.Hour

// FlowStages are the stages of a task, in order. A run sits in "held" while it waits for the owner.
var FlowStages = []string{"dispatched", "gate", "ci", "judge", "merge"}

// FlowTask is one task's time per stage, from its events.
type FlowTask struct {
	Key     string           `json:"key"`
	Task    string           `json:"task"`
	Goal    string           `json:"goal,omitempty"`
	Project string           `json:"project,omitempty"`
	Machine string           `json:"machine"`
	Outcome string           `json:"outcome"`
	Stage   string           `json:"stage,omitempty"` // the stage the run is in now; empty once it finished
	Since   string           `json:"since,omitempty"`
	AgeSec  int64            `json:"age_sec,omitempty"` // seconds in the current stage
	Seconds map[string]int64 `json:"seconds"`           // stage -> seconds (the current stage counts up to now)
	Total   int64            `json:"total_sec"`
	// The PR flow state of the pass (docs/pr-flow.md), from the newest pr.flow event.
	PRState string `json:"pr_state,omitempty"` // "<state> <minutes>/<budget>"
	PRSince string `json:"pr_since,omitempty"`
	PRStale string `json:"pr_stale,omitempty"` // why pr.stale was recorded for this state entry
}

// flowOf walks the task's events once. dispatched: the agent builds; gate: it fixes what the gate blocked; ci: gate
// passed, the PR waits for (or repairs) CI; judge: the judge session runs; merge: judged MERGE, waiting for the merge.
func flowOf(t *ObsTask, now time.Time) FlowTask {
	f := FlowTask{Key: t.Key, Task: t.Task, Goal: t.Goal, Project: t.Project, Machine: t.Machine, Outcome: t.Row.Outcome,
		Seconds: map[string]int64{}}
	evs := append([]Event(nil), t.Events...)
	sort.SliceStable(evs, func(i, j int) bool { return evTime(evs[i]).Before(evTime(evs[j])) })
	var stage string
	var at time.Time
	var pr Event // the newest pr.flow
	var stale string
	move := func(e Event, to string) {
		ts := evTime(e)
		if stage != "" && !at.IsZero() && ts.After(at) {
			f.Seconds[stage] += int64(ts.Sub(at).Seconds())
		}
		stage, at = to, ts
	}
	for _, e := range evs {
		switch e.Event {
		case "pr.flow":
			pr, stale = e, ""
		case "pr.stale":
			stale = fmt.Sprint(e.Data["reason"])
		case "task.dispatched":
			move(e, "dispatched")
		case "gate.blocked":
			move(e, "gate")
		case "gate.passed", "ci.red":
			move(e, "ci")
		case "session.started":
			if e.Role == "judge" {
				move(e, "judge")
			}
		case "judge.verdict":
			if strings.Contains(strings.ToUpper(fmt.Sprint(e.Data["verdict"])), "REJECT") {
				move(e, "dispatched")
			} else {
				move(e, "merge")
			}
		case "run.held":
			move(e, "held")
		default:
			if terminalEvents[e.Event] {
				move(e, "")
			}
		}
	}
	if stage != "" && t.Terminal == "" {
		f.Stage, f.Since = stage, at.UTC().Format(time.RFC3339)
		if now.After(at) {
			f.AgeSec = int64(now.Sub(at).Seconds())
			f.Seconds[stage] += f.AgeSec
		}
	}
	if state, _ := pr.Data["state"].(string); state != "" && t.Terminal == "" {
		since := evTime(pr)
		f.PRState = fmt.Sprintf("%s %d/%d", state, max(0, int(now.Sub(since).Minutes())), num(pr.Data["budget_s"])/60)
		f.PRSince, f.PRStale = since.UTC().Format(time.RFC3339), stale
	}
	for _, s := range f.Seconds {
		f.Total += s
	}
	return f
}

// DayCount is one bar of a per-day chart.
type DayCount struct {
	Day string `json:"day"`
	N   int    `json:"n"`
}

// FlowPage is the Flow page.
type FlowPage struct {
	Days        int        `json:"days"`
	Stages      []string   `json:"stages"`
	Tasks       []FlowTask `json:"tasks"`
	Stuck       []FlowTask `json:"stuck"`
	StuckAfter  string     `json:"stuck_after"`
	OpenPRs     []FlowTask `json:"open_prs"`  // PRs with a flow state, oldest entry first
	StuckPRs    []FlowTask `json:"stuck_prs"` // of those, the stale ones
	MergedByDay []DayCount `json:"merged_per_day"`
	Merged      int        `json:"merged"`
	PRs         int        `json:"prs"`              // tasks in the window with a PR
	Rounds2     int        `json:"prs_2plus_rounds"` // of PRs
	Banners     []string   `json:"banners"`
	// Chart is the merged-per-day SVG.
	Chart template.HTML `json:"-"`
}

// matches reports whether the project filter (empty = all) keeps the project.
func projectMatches(filter, project string) bool {
	return filter == "" || projectKey(filter) == project
}

// windowStart is midnight UTC of the first chart day, so the table, the total and the chart cover the same days.
func windowStart(now time.Time, days int) time.Time {
	n := now.UTC().AddDate(0, 0, -(days - 1))
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).Add(-time.Nanosecond)
}

// dayList is the UTC days from days-1 ago through today, oldest first.
func dayList(now time.Time, days int) []string {
	out := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		out = append(out, now.UTC().AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}

// BuildFlow is the Flow page over the last days days and one project (empty = all).
func (o *Obs) BuildFlow(days int, project string) *FlowPage {
	p := &FlowPage{Days: days, Stages: FlowStages, StuckAfter: StuckAfter.String()}
	wN := windowStart(o.Now, days)
	perDay := map[string]int{}
	keys := make([]string, 0, len(o.Tasks))
	for k := range o.Tasks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := o.Tasks[k]
		if t.Row.Eval || !projectMatches(project, t.Project) {
			continue
		}
		for _, e := range t.Events {
			if e.Event == "pr.merged" && evTime(e).After(wN) {
				perDay[evTime(e).UTC().Format("2006-01-02")]++
				p.Merged++
			}
		}
		f := flowOf(t, o.Now)
		if f.Stage != "" && f.Stage != "held" && time.Duration(f.AgeSec)*time.Second > StuckAfter {
			p.Stuck = append(p.Stuck, f) // stuck now, whatever the range
		}
		if f.PRState != "" {
			p.OpenPRs = append(p.OpenPRs, f) // open now, whatever the range
		}
		if f.PRStale != "" {
			p.StuckPRs = append(p.StuckPRs, f)
		}
		if parseTS(t.Last).After(wN) {
			p.Tasks = append(p.Tasks, f)
			if t.PR != "" {
				p.PRs++
				if t.Rounds >= 2 {
					p.Rounds2++
				}
			}
		}
	}
	sort.SliceStable(p.Tasks, func(i, j int) bool { return p.Tasks[i].Total > p.Tasks[j].Total })
	sort.SliceStable(p.Stuck, func(i, j int) bool { return p.Stuck[i].AgeSec > p.Stuck[j].AgeSec })
	for _, l := range [][]FlowTask{p.OpenPRs, p.StuckPRs} {
		sort.SliceStable(l, func(i, j int) bool { return l[i].PRSince < l[j].PRSince })
	}
	var bars []chartBar
	for _, d := range dayList(o.Now, days) {
		p.MergedByDay = append(p.MergedByDay, DayCount{d, perDay[d]})
		bars = append(bars, chartBar{Label: d, Vals: []float64{float64(perDay[d])}})
	}
	p.Chart = barChart(bars, []string{"merged"}, "merged PRs", func(v float64) string { return fmt.Sprintf("%.0f", v) })
	return p
}

// roundsBanners is the review-rounds recommendation: one line when strictly more than pct percent of PRs needed 2+
// rounds.
func (p *FlowPage) roundsBanners(pct int) []string {
	if p.PRs == 0 || p.Rounds2*100 <= pct*p.PRs {
		return []string{}
	}
	share := math.Round(100 * float64(p.Rounds2) / float64(p.PRs))
	return []string{fmt.Sprintf("%d of %d PRs (%.0f%%) needed 2+ review rounds.", p.Rounds2, p.PRs, share)}
}

// stageBanners is the largest-stage recommendation: one line when one stage holds strictly more than pct percent of
// the stage time of all tasks. It needs time in two or more stages: a lone stage is always 100%.
func (p *FlowPage) stageBanners(pct int) []string {
	sums := map[string]int64{}
	var total int64
	for _, t := range p.Tasks {
		for stage, s := range t.Seconds {
			if s > 0 {
				sums[stage] += s
				total += s
			}
		}
	}
	var top string
	for stage, s := range sums {
		if s > sums[top] || (s == sums[top] && stage < top) { // a tie names the same stage on every load
			top = stage
		}
	}
	if len(sums) < 2 || sums[top]*100 <= int64(pct)*total {
		return nil
	}
	return []string{fmt.Sprintf("%s holds %.0f of %.0f stage hours (%.0f%%).", top,
		float64(sums[top])/3600, float64(total)/3600, math.Round(100*float64(sums[top])/float64(total)))}
}

// SpendDay is the day's cost by role.
type SpendDay struct {
	Day     string  `json:"day"`
	Task    float64 `json:"task"`
	Judge   float64 `json:"judge"`
	Planner float64 `json:"planner"`
}

// SpendRow is one row of the grouped table.
type SpendRow struct {
	Key        string  `json:"key"`
	Sessions   int     `json:"sessions"`
	USD        float64 `json:"usd"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	Pending    int     `json:"cost_pending"`
	TaskKey    string  `json:"task_key,omitempty"` // by=task: the drill-in target
	GoalID     string  `json:"goal,omitempty"`     // by=goal
	Project    string  `json:"project,omitempty"`  // by=goal
}

// SpendRole is one role's spend and the part of it that was polling.
type SpendRole struct {
	Role       string  `json:"role"`
	USD        float64 `json:"usd"`
	PollingUSD float64 `json:"polling_usd"`
	PollingPct float64 `json:"polling_pct"` // of this role's USD
}

// SpendPage is the Spend page.
type SpendPage struct {
	Days    int     `json:"days"`
	By      string  `json:"by"`
	Total   float64 `json:"usd"`
	Pending int     `json:"cost_pending"`
	// Unattributed is the part of Total from sessions that join to no task; UnattributedPct is its percent of Total.
	Unattributed        float64       `json:"unattributed_usd"`
	UnattributedPct     float64       `json:"unattributed_pct"`
	UnattributedPending int           `json:"unattributed_cost_pending"` // such sessions with no cost yet
	PollingUSD          float64       `json:"polling_usd"`
	PollingPct          float64       `json:"polling_pct"` // of Total
	Roles               []SpendRole   `json:"roles"`
	Banners             []string      `json:"banners"`
	Daily               []SpendDay    `json:"daily"`
	Rows                []SpendRow    `json:"rows"`
	Chart               template.HTML `json:"-"`
}

// SpendGroups are the groupings of the table.
var SpendGroups = []string{"goal", "task", "model", "machine"}

// spendRole is the chart series a session belongs to.
func spendRole(s *ObsSession) string {
	switch s.Role {
	case "judge", "planner":
		return s.Role
	}
	return "task"
}

// pollingBanners is the polling recommendation: at most one line, for the factory when its share is strictly above
// pct, else for the role with the most polling $ among those above it.
func (p *SpendPage) pollingBanners(pct int) []string {
	over := func(polling, usd float64) bool { return usd > 0 && 100*polling-float64(pct)*usd > 1e-9 }
	line := func(share float64, of string, polling float64) []string {
		shown := fmt.Sprintf("%.0f", math.Round(share)) // %.0f alone rounds 20.5 down
		if math.Round(share) <= float64(pct) {          // never print a share that reads as not above the threshold
			shown = fmt.Sprintf("%.1f", math.Ceil(share*10)/10)
		}
		return []string{fmt.Sprintf("Polling is %s%% of %s ($%.2f). Action: replace wait turns with one blocking command.", shown, of, polling)}
	}
	if over(p.PollingUSD, p.Total) {
		return line(p.PollingPct, "spend", p.PollingUSD)
	}
	var worst *SpendRole
	for i, r := range p.Roles {
		if over(r.PollingUSD, r.USD) && (worst == nil || r.PollingUSD > worst.PollingUSD) {
			worst = &p.Roles[i]
		}
	}
	if worst == nil {
		return []string{}
	}
	return line(worst.PollingPct, worst.Role+" spend", worst.PollingUSD)
}

// BuildSpend sums the sessions that started in the window. A session's whole cost counts on its start day, as in the
// portfolio and the digest, so the total is the sum of the report's per-session costs.
func (o *Obs) BuildSpend(days int, project, by string) *SpendPage {
	if by != "task" && by != "model" && by != "machine" {
		by = "goal"
	}
	p := &SpendPage{Days: days, By: by}
	wN := windowStart(o.Now, days)
	byDay := map[string]*SpendDay{}
	list := dayList(o.Now, days)
	for _, d := range list {
		byDay[d] = &SpendDay{Day: d}
	}
	rows := map[string]*SpendRow{}
	roles := map[string]*SpendRole{}
	ids := make([]string, 0, len(o.Sessions))
	for id := range o.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := o.Sessions[id]
		at := parseTS(s.Start)
		if !o.Filter.onMachine(s.Machine) || !at.After(wN) || !projectMatches(project, s.Project) {
			continue
		}
		if d := byDay[at.UTC().Format("2006-01-02")]; d != nil {
			switch spendRole(s) {
			case "judge":
				d.Judge += s.Usage.USD
			case "planner":
				d.Planner += s.Usage.USD
			default:
				d.Task += s.Usage.USD
			}
		}
		role := spendRole(s)
		if roles[role] == nil {
			roles[role] = &SpendRole{Role: role}
		}
		roles[role].USD += s.Usage.USD
		roles[role].PollingUSD += s.Usage.PollingUSD
		p.PollingUSD += s.Usage.PollingUSD
		t := o.Tasks[s.TaskKey]
		if t == nil {
			p.Unattributed += s.Usage.USD
		}
		var r *SpendRow
		get := func(k string) *SpendRow {
			if rows[k] == nil {
				rows[k] = &SpendRow{Key: k}
			}
			return rows[k]
		}
		switch by {
		case "task":
			r = get(or(s.TaskKey, "(no task)"))
			r.TaskKey = s.TaskKey
		case "model":
			r = get(or(s.Model, "(unknown)"))
		case "machine":
			r = get(or(s.Machine, "(unknown)"))
		default:
			g := ""
			if t != nil {
				g = t.Goal
			}
			r = get(or(g, "(no goal)"))
			r.GoalID, r.Project = g, or(s.Project, NoProject)
		}
		r.Sessions++
		r.USD += s.Usage.USD
		r.Input += s.Usage.Input
		r.Output += s.Usage.Output
		r.CacheRead += s.Usage.CacheRead
		r.CacheWrite += s.Usage.CacheWrite
		if !s.CostKnown {
			r.Pending++
			p.Pending++
			if t == nil {
				p.UnattributedPending++
			}
		}
		p.Total += s.Usage.USD
	}
	if p.Total > 0 {
		p.UnattributedPct = 100 * p.Unattributed / p.Total
		p.PollingPct = 100 * p.PollingUSD / p.Total
	}
	p.Roles = []SpendRole{}
	for _, name := range []string{"task", "judge", "planner"} {
		if r := roles[name]; r != nil {
			if r.USD > 0 {
				r.PollingPct = 100 * r.PollingUSD / r.USD
			}
			p.Roles = append(p.Roles, *r)
		}
	}
	var bars []chartBar
	for _, d := range list {
		x := byDay[d]
		p.Daily = append(p.Daily, *x)
		bars = append(bars, chartBar{Label: d, Vals: []float64{x.Task, x.Judge, x.Planner}})
	}
	for _, r := range rows {
		p.Rows = append(p.Rows, *r)
	}
	sort.Slice(p.Rows, func(i, j int) bool {
		if p.Rows[i].USD != p.Rows[j].USD {
			return p.Rows[i].USD > p.Rows[j].USD
		}
		return p.Rows[i].Key < p.Rows[j].Key
	})
	p.Chart = barChart(bars, []string{"task", "judge", "planner"}, "USD per day", func(v float64) string { return fmt.Sprintf("$%.2f", v) })
	return p
}

// NowRun is a run in flight or held.
type NowRun struct {
	FlowTask
	USD      float64 `json:"usd"`
	Sessions int     `json:"sessions"`
}

// NowPlanner is a machine's planner: its newest event and what its sessions cost in the window.
type NowPlanner struct {
	Machine   string  `json:"machine"`
	Session   string  `json:"session"`
	LastEvent string  `json:"last_event"`
	LastAt    string  `json:"last_at"`
	Sessions  int     `json:"sessions"`
	USD       float64 `json:"usd"`
}

// NowAsk is the open owner questions of one project.
type NowAsk struct {
	Project string `json:"project"`
	Open    int    `json:"open"`
}

// NowPage is the Now page: what runs, what waits for the owner, the planner and the machines.
type NowPage struct {
	Running   []NowRun      `json:"running"`
	Held      []NowRun      `json:"held"`
	Asks      []NowAsk      `json:"open_asks"`
	AsksLocal bool          `json:"open_asks_local_only"`
	Planner   []NowPlanner  `json:"planner"`
	Portfolio []ProjectLine `json:"projects"`
	Machines  []sourceLine  `json:"machines"`
}

// BuildNow is the Now page for one project (empty = all). asks are this machine's open asks (local, when ok).
func (o *Obs) BuildNow(days int, project, me string, asks map[string]int, ok bool, srcs []ObsSource) *NowPage {
	p := &NowPage{AsksLocal: ok}
	keys := make([]string, 0, len(o.Tasks))
	for k := range o.Tasks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := o.Tasks[k]
		if t.Terminal != "" || t.Row.Eval || !projectMatches(project, t.Project) {
			continue
		}
		r := NowRun{FlowTask: flowOf(t, o.Now), USD: t.Row.USD, Sessions: len(t.Sessions)}
		switch {
		case strings.HasPrefix(t.Row.Outcome, "held"):
			p.Held = append(p.Held, r)
		default:
			p.Running = append(p.Running, r)
		}
	}
	for _, l := range [][]NowRun{p.Running, p.Held} {
		sort.SliceStable(l, func(i, j int) bool { return l[i].AgeSec > l[j].AgeSec })
	}
	all := o.OpenAsks(me, asks, ok)
	for pr, n := range all {
		if n > 0 && (project == "" || projectKeyLabel(pr) == projectKeyLabel(project)) {
			p.Asks = append(p.Asks, NowAsk{pr, n})
		}
	}
	sort.Slice(p.Asks, func(i, j int) bool { return p.Asks[i].Project < p.Asks[j].Project })
	wN := windowStart(o.Now, days)
	by := map[string]*NowPlanner{}
	var newest = map[string]time.Time{}
	ids := make([]string, 0, len(o.Sessions))
	for id := range o.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := o.Sessions[id]
		if s.Role != "planner" || !o.Filter.onMachine(s.Machine) {
			continue
		}
		pl := by[s.Machine]
		if pl == nil {
			pl = &NowPlanner{Machine: s.Machine}
			by[s.Machine] = pl
		}
		if parseTS(s.Start).After(wN) {
			pl.Sessions++
			pl.USD += s.Usage.USD
		}
		for _, e := range s.Events {
			if at := evTime(e); !at.Before(newest[s.Machine]) {
				newest[s.Machine] = at
				pl.Session, pl.LastEvent, pl.LastAt = s.ID, e.Event, e.TS
			}
		}
	}
	for _, pl := range by {
		pl.USD = round2(pl.USD)
		p.Planner = append(p.Planner, *pl)
	}
	sort.Slice(p.Planner, func(i, j int) bool { return p.Planner[i].Machine < p.Planner[j].Machine })
	for _, l := range o.Portfolio(days, all) {
		if project == "" || l.Project == projectKeyLabel(project) {
			p.Portfolio = append(p.Portfolio, l)
		}
	}
	listed := map[string]int{} // machine -> its row
	for _, src := range srcs {
		src.Machine = or(src.Machine, src.Label)
		row := sourceLine{ObsSource: src, Last: o.LastSeen[src.Machine]}
		i, ok := listed[src.Machine]
		switch {
		case !ok:
			listed[src.Machine] = len(p.Machines)
			p.Machines = append(p.Machines, row)
		case !p.Machines[i].Own && parseTS(src.Pulled).After(parseTS(p.Machines[i].Pulled)): // a newer copy of one machine's log
			p.Machines[i] = row
		}
	}
	for _, m := range o.Machines { // a session machine has no log of its own
		if _, ok := listed[m]; !ok {
			p.Machines = append(p.Machines, sourceLine{ObsSource: ObsSource{Label: m, Machine: m}, Last: o.LastSeen[m]})
		}
	}
	o.proofTime(p.Machines, wN)
	return p
}

// proofTime fills each machine's gate proofs since from: data.machine of gate.proof_run, "local" being the writer.
func (o *Obs) proofTime(rows []sourceLine, from time.Time) {
	runs, secs := map[string]int{}, map[string]float64{}
	for _, t := range o.Tasks {
		for _, e := range t.Events {
			if e.Event != "gate.proof_run" || !evTime(e).After(from) {
				continue
			}
			m, _ := e.Data["machine"].(string)
			if m == "" || m == localMachine {
				m = t.Origin // e.Machine is the run's placement, not where the proof ran
			}
			s, _ := e.Data["seconds"].(float64)
			runs[m]++
			secs[m] += s
		}
	}
	for i := range rows {
		rows[i].Proofs, rows[i].ProofMin = runs[rows[i].Machine], round2(secs[rows[i].Machine]/60)
	}
}

// projectKeyLabel maps the filter's value to the portfolio's project label.
func projectKeyLabel(p string) string { return or(p, NoProject) }

// chartBar is one x position: one value per series, stacked.
type chartBar struct {
	Label string
	Vals  []float64
}

// niceMax rounds the axis top up to 1, 2, 2.5 or 5 times a power of ten.
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	e := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*e {
			return m * e
		}
	}
	return 10 * e
}

// barChart draws stacked bars as inline SVG: a y axis with gridlines and ticks, an x axis with date labels, a legend.
// Colours come from the CSS classes s0, s1, s2, so both themes work; every bar carries a <title> with its value.
func barChart(bars []chartBar, series []string, yLabel string, fmtY func(float64) string) template.HTML {
	const w, h, ml, mr, mt, mb = 760.0, 240.0, 56.0, 12.0, 28.0, 28.0
	top := 0.0
	for _, b := range bars {
		sum := 0.0
		for _, v := range b.Vals {
			sum += v
		}
		top = math.Max(top, sum)
	}
	top = niceMax(top)
	pw, ph := w-ml-mr, h-mt-mb
	var sb strings.Builder
	// The box scrolls instead of letting the SVG scale below its viewBox, so chart text stays at 12px.
	fmt.Fprintf(&sb, `<div class="chartbox" tabindex="0" role="region" aria-label="%s chart">`, template.HTMLEscapeString(yLabel))
	fmt.Fprintf(&sb, `<svg class="chart" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`, w, h, template.HTMLEscapeString(yLabel))
	// legend
	for i, s := range series {
		fmt.Fprintf(&sb, `<rect class="s%d" x="%.0f" y="8" width="10" height="10"/><text class="lg" x="%.0f" y="17">%s</text>`,
			i, ml+float64(i)*84, ml+float64(i)*84+14, template.HTMLEscapeString(s))
	}
	const ticks = 4
	for i := 0; i <= ticks; i++ {
		v := top * float64(i) / ticks
		y := mt + ph - ph*float64(i)/ticks
		fmt.Fprintf(&sb, `<line class="grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><text class="tick" x="%.1f" y="%.1f" text-anchor="end">%s</text>`,
			ml, y, w-mr, y, ml-6, y+4, template.HTMLEscapeString(fmtY(v)))
	}
	n := len(bars)
	if n == 0 {
		n = 1
	}
	slot := pw / float64(n)
	bw := math.Max(1, slot*0.7)
	step := 1
	for step*int(slot) < 56 && step < n {
		step++
	}
	for i, b := range bars {
		x := ml + slot*float64(i) + (slot-bw)/2
		base := mt + ph
		var parts []string
		for j, v := range b.Vals {
			if v <= 0 {
				continue
			}
			hh := ph * v / top
			base -= hh
			fmt.Fprintf(&sb, `<rect class="s%d" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, j, x, base, bw, hh)
		}
		for j, v := range b.Vals {
			parts = append(parts, fmt.Sprintf("%s %s", series[j], fmtY(v)))
		}
		fmt.Fprintf(&sb, `<rect class="hit" x="%.1f" y="%.1f" width="%.1f" height="%.1f"><title>%s: %s</title></rect>`,
			ml+slot*float64(i), mt, slot, ph, template.HTMLEscapeString(b.Label), template.HTMLEscapeString(strings.Join(parts, ", ")))
		if i%step == 0 || i == len(bars)-1 && step == 1 {
			lbl := b.Label
			if len(lbl) >= 10 {
				lbl = lbl[5:]
			}
			fmt.Fprintf(&sb, `<line class="axis" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><text class="tick" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`,
				x+bw/2, mt+ph, x+bw/2, mt+ph+4, x+bw/2, h-8, template.HTMLEscapeString(lbl))
		}
	}
	fmt.Fprintf(&sb, `<line class="axis" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><line class="axis" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
		ml, mt, ml, mt+ph, ml, mt+ph, w-mr, mt+ph)
	sb.WriteString(`</svg></div>`)
	return template.HTML(sb.String())
}

// Top reports whether the page is a top section (now, spend, flow, machines) rather than a drill-down or a not-found page.
func (p *obsPage) Top() bool { return p.Kind == p.Section }

// Current reports whether the sidebar entry for section s is the page being shown; only top sections have one.
func (p *obsPage) Current(s string) bool { return p.Top() && p.Section == s }

// dur is a seconds count as "1h 05m", "12m" or "40s".
func dur(sec int64) string {
	switch {
	case sec >= 3600:
		return fmt.Sprintf("%dh %02dm", sec/3600, sec%3600/60)
	case sec >= 60:
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%ds", sec)
}
