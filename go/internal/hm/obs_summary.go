package hm

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GET /api/obs/v1/summary: everything one card of a factory needs, in one small answer. The factory computes it once
// (counts, what needs the owner, planner, merges, spend, budget, machines), so the hub and the Herdr dashboard only
// lay summaries side by side. Design: docs/design/multi-factory.md.

// ObsAPI is the version of /api/obs/v1/*. A field is only ever added within a version.
const ObsAPI = 1

func init() {
	Defaults["obs_label"] = ""                   // this factory's display name ("Workshop"); served in the API, never a host name
	Defaults["obs_machine_labels"] = ""          // display names of machines: "name=Label,name2=Label 2"; keys and links keep the names
	Defaults["obs_peers"] = ""                   // other factories' obs URLs, comma-separated: turns on the All factories page (/all)
	Defaults["obs_hub_url"] = ""                 // the hub's URL, for the "All factories" link of a factory that is not the hub
	Defaults["obs_stuck_dispatched_min"] = "120" // a run longer than this in one stage is listed as stuck
	Defaults["obs_stuck_gate_min"] = "15"
	Defaults["obs_stuck_ci_min"] = "45"
	Defaults["obs_stuck_judge_min"] = "15"
	Defaults["obs_stuck_merge_min"] = "120"
}

// RunnerInfo names the factory that answers.
type RunnerInfo struct {
	ID       string   `json:"id"` // stable over a label change; lets a hub see one factory listed twice
	Label    string   `json:"label"`
	Version  string   `json:"version"`
	Projects []string `json:"projects"`
	Hub      string   `json:"hub"`
}

// SummaryCounts are the open runs by what they wait for. The four add up to the open runs.
type SummaryCounts struct {
	Working int `json:"working"` // dispatched, gate, judge
	CI      int `json:"ci"`
	Merge   int `json:"merge"`
	Held    int `json:"held"`
}

// Need is one row of "needs you". Act rows wait for the owner and nothing else (questions, held runs); the others
// are worth a look (failed, stuck, system).
type Need struct {
	Kind    string `json:"kind"` // question | held | failed | stuck | system
	Act     bool   `json:"act"`
	Task    string `json:"task,omitempty"`
	Key     string `json:"key,omitempty"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
	Text    string `json:"text"`
	Since   string `json:"since,omitempty"`
	AgeSec  int64  `json:"age_sec"`
	PR      string `json:"pr,omitempty"`
	Href    string `json:"href"` // a path on this factory, or the PR
}

// SummaryPlanner is the planner's state from the runner's own record, not from the event log.
type SummaryPlanner struct {
	On        bool   `json:"on"`
	LastWake  string `json:"last_wake,omitempty"`
	Pending   string `json:"pending,omitempty"`
	LostSince string `json:"lost_since,omitempty"`
}

// SummaryMerged is merged PRs: today and the last 7 UTC days, oldest first.
type SummaryMerged struct {
	Today  int        `json:"today"`
	PerDay []DayCount `json:"per_day"`
}

// SummarySpend is spend over all projects, at API prices.
type SummarySpend struct {
	TodayUSD    float64  `json:"today_usd"`
	WeekUSD     float64  `json:"week_usd"`
	PerMergedPR *float64 `json:"per_merged_pr_usd"`
	CostPending int      `json:"cost_pending"`
}

// SummaryBudget is each cap that is set (null: off) and the usage reading of this factory's account.
type SummaryBudget struct {
	WeeklyCapUSD       *float64 `json:"weekly_cap_usd"`
	ProjectDailyCapUSD *float64 `json:"project_daily_cap_usd"`
	TaskCapUSD         *float64 `json:"task_cap_usd"`
	Usage7dPct         *float64 `json:"usage_7d_pct"`
	Usage5hPct         *float64 `json:"usage_5h_pct"`
	Usage7dCapPct      int      `json:"usage_7d_cap_pct"`
	Usage5hCapPct      int      `json:"usage_5h_cap_pct"`
	UsageAgeSec        *int     `json:"usage_age_sec"` // null: no reading
}

// SummaryMachine is one machine of the factory.
type SummaryMachine struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Coordinator bool   `json:"coordinator"`
	Down        bool   `json:"down"`
	Running     int    `json:"running"`
	FreeSlots   int    `json:"free_slots"`
	MemMB       *int   `json:"mem_available_mb"`
	LastSeen    string `json:"last_seen"`
}

// Summary is the answer of /api/obs/v1/summary.
type Summary struct {
	API        int              `json:"api"`
	Now        time.Time        `json:"now"`
	Runner     RunnerInfo       `json:"runner"`
	Counts     SummaryCounts    `json:"counts"`
	NeedsYou   []Need           `json:"needs_you"`       // act rows first, then look rows; each kind oldest first; at most maxNeeds
	NeedsCount int              `json:"needs_you_count"` // the act rows: the number a bar or a card shows
	NeedKinds  map[string]int   `json:"needs_you_kinds"` // every row per kind, before the cut
	Planner    SummaryPlanner   `json:"planner"`
	Merged     SummaryMerged    `json:"merged"`
	Spend      SummarySpend     `json:"spend"`
	Budget     SummaryBudget    `json:"budget"`
	Cap        int              `json:"max_concurrent"`
	Machines   []SummaryMachine `json:"machines"`
}

const (
	maxNeeds    = 60
	summaryTTL  = 10 * time.Second
	failedSince = 24 * time.Hour
)

// needOrder is the order of the kinds in the list.
var needOrder = map[string]int{"question": 0, "held": 1, "failed": 2, "stuck": 3, "system": 4}

// buildVersion is the short commit the binary was built from ("dev" when the build carries none).
func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 8 {
				return s.Value[:8]
			}
		}
	}
	return "dev"
}

// RunnerID is stable for one factory home on one machine and says nothing about either.
func (r *Runner) RunnerID() string {
	sum := sha256.Sum256([]byte(r.Machine() + "\x00" + r.Env.Home))
	return "r-" + hex.EncodeToString(sum[:6])
}

// MachineLabels reads obs_machine_labels ("name=Label,name2=Label 2").
func MachineLabels(cfg string) map[string]string {
	out := map[string]string{}
	for _, e := range csv(cfg) {
		if k, v, ok := strings.Cut(e, "="); ok && strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// StuckLimits are the per-stage limits of "stuck", from obs_stuck_<stage>_min.
func StuckLimits(s Store) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, st := range FlowStages {
		if n := cfgInt(s, "obs_stuck_"+st+"_min"); n > 0 {
			out[st] = time.Duration(n) * time.Minute
		}
	}
	return out
}

// heldWhy is the reason and PR of the task's last run.held event.
func heldWhy(t *ObsTask) (why, pr string) {
	for _, e := range t.Events {
		if e.Event == "run.held" {
			why, _ = e.Data["why"].(string)
			if p, _ := e.Data["pr"].(string); p != "" {
				pr = p
			}
		}
	}
	return why, or(pr, t.PR)
}

func httpsURL(s string) string {
	if strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

func taskHref(key string) string { return "/task?k=" + url.QueryEscape(key) }

// refiled is the tasks that already have a re-file: the planner handled their failure.
func (r *Runner) refiled() map[string]bool {
	out := map[string]bool{}
	sq, err := r.sq()
	if err != nil {
		return out
	}
	rows, err := sq.refileRows()
	if err != nil {
		return out
	}
	for _, x := range rows {
		of := x.of
		if of == "" {
			if m := refileTitle.FindStringSubmatch(x.title); m != nil {
				of = strings.ToUpper(m[1])
			}
		}
		if of != "" && x.status != "cancelled" {
			out[of] = true
		}
	}
	return out
}

// summary is the cached answer: one build serves every client for summaryTTL.
func (s *obsServer) summary() (*Summary, error) {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	if s.sum != nil && s.now().Sub(s.sumAt) < summaryTTL {
		return s.sum, nil
	}
	o, srcs, err := s.load(ObsFilter{})
	if err != nil {
		return nil, err
	}
	sum := s.buildSummary(o, srcs)
	s.sum, s.sumAt = sum, s.now()
	return sum, nil
}

func (s *obsServer) buildSummary(o *Obs, srcs []ObsSource) *Summary {
	r, me := s.r, s.r.Machine()
	out := &Summary{API: ObsAPI, Now: o.Now, NeedsYou: []Need{}, NeedKinds: map[string]int{}, Cap: cfgInt(r.Store, "max_concurrent")}
	out.Runner = RunnerInfo{ID: r.RunnerID(), Label: strings.TrimSpace(r.Store.Cfg("obs_label")), Version: buildVersion(),
		Projects: []string{}, Hub: strings.TrimSpace(r.Store.Cfg("obs_hub_url"))}

	title := func(t *ObsTask) string {
		if t.Origin != me {
			return ""
		}
		if x, err := r.Store.Task(t.Task); err == nil && x != nil {
			return x.Title
		}
		return ""
	}
	var needs []Need
	add := func(n Need) {
		n.Act = n.Kind == "question" || n.Kind == "held"
		needs = append(needs, n)
	}

	// questions: this factory's own queue, with their text
	localAsks, ok := map[string]int{}, false
	if sq, err := r.sq(); err == nil {
		if open, err := sq.Asks("open"); err == nil {
			ok = true
			for _, a := range open {
				localAsks[a.Project]++
				n := Need{Kind: "question", Task: a.Task, Project: a.Project, Text: truncate(a.Question, 400), Since: a.Asked, Href: "/#questions"}
				if at := parseTS(a.Asked); !at.IsZero() && o.Now.After(at) {
					n.AgeSec = int64(o.Now.Sub(at).Seconds())
				}
				add(n)
			}
		}
	}

	now := o.BuildNow(30, "", me, localAsks, ok, srcs)
	limits := StuckLimits(r.Store)
	for _, x := range now.Running {
		switch x.Stage {
		case "ci":
			out.Counts.CI++
		case "merge":
			out.Counts.Merge++
		default:
			out.Counts.Working++
		}
		if lim, has := limits[x.Stage]; has && time.Duration(x.AgeSec)*time.Second > lim {
			t := o.Tasks[x.Key]
			n := Need{Kind: "stuck", Task: x.Task, Key: x.Key, Project: x.Project, Since: x.Since, AgeSec: x.AgeSec,
				Text: x.Stage + " for " + dur(x.AgeSec) + ", limit " + dur(int64(lim.Seconds())), Href: taskHref(x.Key)}
			if t != nil {
				n.Title, n.PR = title(t), httpsURL(t.PR)
			}
			add(n)
		}
	}
	out.Counts.Held = len(now.Held)
	for _, x := range now.Held {
		n := Need{Kind: "held", Task: x.Task, Key: x.Key, Project: x.Project, Since: x.Since, AgeSec: x.AgeSec,
			Text: strings.TrimPrefix(x.Outcome, "held: "), Href: taskHref(x.Key)}
		if t := o.Tasks[x.Key]; t != nil {
			why, pr := heldWhy(t)
			n.Title, n.PR = title(t), httpsURL(pr)
			if why != "" {
				n.Text = truncate(why, 400)
			}
			if n.PR != "" {
				n.Href = n.PR
			}
		}
		add(n)
	}

	// failed in the last 24 h and not re-filed. A PR the owner closed is the owner's own doing.
	refiled := r.refiled()
	for _, t := range o.Tasks {
		if t.Row.Eval || !strings.HasPrefix(t.Row.Outcome, "FAIL") || strings.Contains(t.Row.Outcome, "the owner closed") {
			continue
		}
		at := parseTS(t.Terminal)
		if at.IsZero() || o.Now.Sub(at) > failedSince || (t.Origin == me && refiled[t.Task]) {
			continue
		}
		add(Need{Kind: "failed", Task: t.Task, Key: t.Key, Title: title(t), Project: t.Project, Since: t.Terminal,
			AgeSec: int64(o.Now.Sub(at).Seconds()), Text: truncate(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t.Row.Outcome, "FAIL"), ":")), 400),
			PR: httpsURL(t.PR), Href: taskHref(t.Key)})
	}

	// planner, from the runner's own record
	out.Planner = SummaryPlanner{On: r.Store.Meta("planner") == "on", Pending: r.Store.Meta("planner_pending"), LostSince: r.Store.Meta("planner_lost")}
	if ts, err := strconv.ParseInt(r.Store.Meta("planner_last_wake"), 10, 64); err == nil && ts > 0 {
		out.Planner.LastWake = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	switch {
	case out.Planner.LostSince != "":
		add(Need{Kind: "system", Text: "the planner session is lost since " + out.Planner.LostSince, Since: out.Planner.LostSince, Href: "/"})
	case !out.Planner.On:
		add(Need{Kind: "system", Text: "the planner is off", Href: "/"})
	}

	// merges and spend
	flow := o.BuildFlow(7, "")
	out.Merged.PerDay = flow.MergedByDay
	if n := len(flow.MergedByDay); n > 0 {
		out.Merged.Today = flow.MergedByDay[n-1].N
	}
	for _, l := range now.Portfolio {
		switch {
		case l.Project == "all":
			out.Spend = SummarySpend{TodayUSD: l.USDToday, WeekUSD: l.USD7d, PerMergedPR: l.PerMergedPR, CostPending: l.CostPending}
		case l.Project != NoProject:
			out.Runner.Projects = append(out.Runner.Projects, l.Project)
		}
	}

	// budget: the caps that are set, and the account's usage reading
	capOf := func(key string) *float64 {
		if f, ok := cfgFloat(r.Store, key); ok {
			return &f
		}
		return nil
	}
	u := r.newestUsage(s.now())
	out.Budget = SummaryBudget{WeeklyCapUSD: capOf("weekly_budget_usd"), ProjectDailyCapUSD: capOf("project_daily_budget_usd"), TaskCapUSD: capOf("task_budget_usd"),
		Usage7dPct: u.SevenDay, Usage5hPct: u.FiveHour, Usage7dCapPct: cfgInt(r.Store, "max_seven_day_pct"), Usage5hCapPct: cfgInt(r.Store, "max_five_hour_pct")}
	if u.Found && u.AgeS >= 0 {
		age := u.AgeS
		out.Budget.UsageAgeSec = &age
	}
	if c := out.Budget.WeeklyCapUSD; c != nil && out.Spend.WeekUSD >= *c {
		add(Need{Kind: "system", Text: "weekly budget reached: $" + strconv.FormatFloat(out.Spend.WeekUSD, 'f', 2, 64) + " of $" + strconv.FormatFloat(*c, 'f', 2, 64), Href: "/spend"})
	}

	// machines
	labels := MachineLabels(r.Store.Cfg("obs_machine_labels"))
	out.Machines = []SummaryMachine{}
	for _, c := range s.machinesPage(o).Cards {
		m := SummaryMachine{Name: c.Name, Label: or(labels[c.Name], c.Name), Coordinator: c.Coordinator, Down: c.Down, Running: len(c.Running),
			FreeSlots: c.FreeSlots, MemMB: c.MemMB, LastSeen: c.LastSeen}
		out.Machines = append(out.Machines, m)
		if c.Down {
			add(Need{Kind: "system", Text: "machine " + m.Label + " is down", Href: "/machines"})
		}
	}

	sort.SliceStable(needs, func(i, j int) bool {
		if a, b := needOrder[needs[i].Kind], needOrder[needs[j].Kind]; a != b {
			return a < b
		}
		return needs[i].AgeSec > needs[j].AgeSec
	})
	for _, n := range needs {
		out.NeedKinds[n.Kind]++
		if n.Act {
			out.NeedsCount++
		}
	}
	if len(needs) > maxNeeds {
		needs = needs[:maxNeeds]
	}
	if needs != nil {
		out.NeedsYou = needs
	}
	sort.Strings(out.Runner.Projects)
	return out
}
