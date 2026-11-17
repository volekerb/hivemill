package views

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// Goals page constants (web/views/goals.py).
const (
	titleGoals    = "Goals"
	allDays       = 3650 // "all time" window for the goal's merged-PR count and median
	intentOpenMax = 480  // a longer intent collapses behind a disclosure
)

var (
	goalOrder = []string{"active", "planning", "proposed", "blocked"}
	goalSorts = []string{"priority", "progress", "updated"}
	badgeKind = map[string]string{"done": "ok", "active": "info", "in_progress": "info", "in_review": "info", "planning": "warn",
		"proposed": "warn", "todo": "muted", "blocked": "bad", "cancelled": "muted"}
	// statusClass is the status--<s> classes of html.CLASSES.
	statusClass = map[string]bool{"todo": true, "in_progress": true, "in_review": true, "blocked": true, "done": true, "cancelled": true,
		"proposed": true, "planning": true, "active": true, "live": true, "queued": true, "exited": true, "lost": true, "stopped": true}
)

func notFound(kind, ident, back, backLabel string, crumb Safe) Safe {
	return El("div", crumb, EmptyState("No "+kind+" "+ident, &EmptyAction{back, backLabel}))
}

// StatusBadge is a status as a badge: "Active", "Done", ... with its tone class.
func StatusBadge(status string) Safe {
	kind, ok := badgeKind[status]
	if !ok {
		kind = "muted"
	}
	cls := "badge badge--" + kind
	if statusClass[status] {
		cls += " status--" + status
	}
	text := status
	if l, ok := Labels[status]; ok {
		text = l
	}
	return El("span", text, Cls(cls))
}

// TaskBadge is a task status badge. Blocked splits by who it waits on: red only when the owner must act.
func TaskBadge(status, waits string) Safe {
	if status == "blocked" {
		if waits == "owner" {
			return Badge("blocked: owner", "bad")
		}
		return Badge("blocked (internal)", "muted")
	}
	return StatusBadge(status)
}

func goalRank(g data.Goal) int {
	for i, s := range goalOrder {
		if s == g.Status {
			return i
		}
	}
	return len(goalOrder)
}

// GoalsData is what the Goals page renders.
type GoalsData struct {
	Org          string
	Orgs         map[string]int
	Goals        []data.Goal
	Deferred     []data.BlockedTask
	NeedsYou     []NeedsItem
	NeedsByGoal  map[string]int
	Criteria     *data.OMap[data.CriteriaCount]
	LastActivity *data.OMap[string]
	Sort         string // "" = default order
}

// LoadGoals gathers the data of the Goals page (web/views/goals.py data()).
func LoadGoals(ctx *Ctx) (*GoalsData, error) {
	org := ctx.org()
	every, err := ctx.DB.Goals()
	if err != nil {
		return nil, fmt.Errorf("views: goals: %w", err)
	}
	d := &GoalsData{Org: org, Orgs: map[string]int{}}
	for _, o := range data.Orgs {
		for _, g := range every {
			if g.Org == o {
				d.Orgs[o]++
			}
		}
	}
	ids := map[string]bool{}
	for _, g := range every {
		if org == "" || g.Org == org {
			d.Goals = append(d.Goals, g)
			ids[g.ID] = true
		}
	}
	sort.SliceStable(d.Goals, func(i, j int) bool {
		a, b := d.Goals[i], d.Goals[j]
		if ra, rb := goalRank(a), goalRank(b); ra != rb {
			return ra < rb
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	})
	internal, err := ctx.DB.InternalBlocked()
	if err != nil {
		return nil, fmt.Errorf("views: internal blocked: %w", err)
	}
	for _, t := range internal {
		if org == "" || ids[t.GoalID] {
			d.Deferred = append(d.Deferred, t)
		}
	}
	if d.NeedsYou, err = needs(ctx, org, nil); err != nil {
		return nil, err
	}
	d.NeedsByGoal = needsByGoal(d.NeedsYou)
	if d.Criteria, err = ctx.DB.CriteriaByGoal(); err != nil {
		return nil, fmt.Errorf("views: criteria: %w", err)
	}
	if d.LastActivity, err = ctx.DB.LastActivityByGoal(); err != nil {
		return nil, fmt.Errorf("views: last activity: %w", err)
	}
	if s := ctx.Query["sort"]; s == "priority" || s == "progress" || s == "updated" {
		d.Sort = s
	}
	return d, nil
}

func internalCard(tasks []data.BlockedTask) any {
	if len(tasks) == 0 {
		return nil
	}
	var rows [][]any
	for _, t := range tasks {
		rows = append(rows, []any{Link("/task/"+t.ID, t.ID), t.Title, Badge("queued/deferred", "muted")})
	}
	return Card(fmt.Sprintf("Blocked (internal): %d", len(tasks)), Table([]string{"Item", "What", "State"}, rows, "table table--compact table--id-col"))
}

func blockedCell(g data.Goal) any {
	own, internal := g.TasksBlockedOwner, g.TasksBlocked-g.TasksBlockedOwner
	var parts []any
	if own != 0 {
		parts = append(parts, Badge(fmt.Sprintf("%d owner", own), "bad"))
	}
	if internal != 0 {
		parts = append(parts, Badge(fmt.Sprintf("%d queued", internal), "muted"))
	}
	if len(parts) == 0 {
		return nil
	}
	return El("span", parts, Cls("gblk"))
}

func sortedGoals(goals []data.Goal, sortKey string, last *data.OMap[string]) []data.Goal {
	out := append([]data.Goal(nil), goals...)
	switch sortKey {
	case "priority":
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Priority != out[j].Priority {
				return out[i].Priority < out[j].Priority
			}
			return out[i].ID < out[j].ID
		})
	case "progress":
		frac := func(g data.Goal) float64 {
			if g.TasksTotal == 0 {
				return 0
			}
			return float64(g.TasksDone) / float64(g.TasksTotal)
		}
		sort.SliceStable(out, func(i, j int) bool {
			if fi, fj := frac(out[i]), frac(out[j]); fi != fj {
				return fi > fj
			}
			return out[i].ID < out[j].ID
		})
	case "updated":
		ep := func(g data.Goal) float64 {
			ts, _ := last.Get(g.ID)
			if ts == "" {
				ts = g.Updated
			}
			return epochOf(ts)
		}
		sort.SliceStable(out, func(i, j int) bool { // Python: reverse=True on (epoch, id), so ties break by id descending
			if ei, ej := ep(out[i]), ep(out[j]); ei != ej {
				return ei > ej
			}
			return out[i].ID > out[j].ID
		})
	}
	return out
}

func goalRowOf(g data.Goal, d *GoalsData, ctx *Ctx) Safe {
	var crit *data.CriteriaCount
	if c, ok := d.Criteria.Get(g.ID); ok {
		cc := c
		crit = &cc
	}
	last, _ := d.LastActivity.Get(g.ID)
	if last == "" {
		last = g.Updated
	}
	in := GoalRowIn{ID: g.ID, Title: g.Title, Status: g.Status, Org: g.Org, Priority: g.Priority, TasksDone: g.TasksDone,
		TasksTotal: g.TasksTotal, PRs: g.OpenPRs}
	return GoalRow(in, ctx, crit, last, d.NeedsByGoal[g.ID], blockedCell(g), nil, false)
}

func goalList(goals []data.Goal, d *GoalsData, ctx *Ctx) Safe {
	var rows []any
	for _, g := range goals {
		rows = append(rows, goalRowOf(g, d, ctx))
	}
	return El("ul", rows, Cls("lrow-list"))
}

func goalChips(goals []data.Goal) Safe {
	n := func(st ...string) int {
		c := 0
		for _, g := range goals {
			for _, s := range st {
				if g.Status == s {
					c++
					break
				}
			}
		}
		return c
	}
	items := []Chip{{"Active", N(n("active", "planning")), "#open-goals", false}, {"Blocked", N(n("blocked")), "#open-goals", false},
		{"Proposed", N(n("proposed")), "#open-goals", false}, {"Done", N(n("done", "cancelled")), "#done-goals", false}}
	var cs []any
	for _, i := range items {
		cs = append(cs, chipEl(i))
	}
	return El("div", cs, Cls("chips gchips"))
}

// orgChips is the org facet: All / Claude / Cursor with goal counts, the same ?org= as the header filter (sort kept).
func orgChips(d *GoalsData) Safe {
	total := 0
	for _, n := range d.Orgs {
		total += n
	}
	items := []Chip{{"All", N(total), OrgHref("/goals", "", KV{"sort", d.Sort}), d.Org == ""}}
	for _, o := range data.Orgs {
		pick := o
		if d.Org == o {
			pick = ""
		}
		items = append(items, Chip{OrgLabel(o), N(d.Orgs[o]), OrgHref("/goals", pick, KV{"sort", d.Sort}), d.Org == o})
	}
	return Chips("Org", items)
}

// RenderGoals renders the Goals page body.
func RenderGoals(ctx *Ctx, d *GoalsData) Safe {
	var live, closed []data.Goal
	for _, g := range d.Goals {
		if g.Status == "done" || g.Status == "cancelled" {
			closed = append(closed, g)
		} else {
			live = append(live, g)
		}
	}
	active := 0
	if d.Org != "" {
		active = 1
	}
	parts := []any{FilterBar("goals-filters", active, []any{goalChips(d.Goals)}, orgChips(d))}
	if n := len(d.NeedsYou); n > 0 {
		lead := fmt.Sprintf("%d items need you", n)
		if n == 1 {
			lead = "1 item needs you"
		}
		summary := El("p", lead+" — same list as on the ", Link("/#needs-you", "Overview"), ".", Cls("summary-line"))
		var rows []any
		for _, i := range d.NeedsYou {
			rows = append(rows, NeedsYouRow(i))
		}
		parts = append(parts, Section("Needs you", n, summary, El("ul", rows, Cls("lrow-list"))))
	} else {
		parts = append(parts, Section("Needs you", nil, EmptyState("Nothing is waiting on the owner.", nil)))
	}
	if c := internalCard(d.Deferred); c != nil {
		parts = append(parts, c)
	}
	segs := []SegItem{{"Default", OrgHref("/goals", d.Org), d.Sort == ""}}
	for _, s := range goalSorts {
		segs = append(segs, SegItem{pyCapitalize(s), OrgHref("/goals", d.Org, KV{"sort", s}), d.Sort == s})
	}
	var body []any
	var count any
	if len(live) > 0 {
		body = []any{Segmented("Sort open goals", segs, false), goalList(sortedGoals(live, d.Sort, d.LastActivity), d, ctx)}
		count = len(live)
	} else {
		body = []any{EmptyState("No open goals.", nil)}
	}
	parts = append(parts, El("div", Section("Open goals", count, body...), At("id", "open-goals")))
	if len(closed) > 0 {
		parts = append(parts, El("div", Disclosure("Done", "goals-done", len(closed), false, goalList(closed, d, ctx)), At("id", "done-goals")))
	}
	return El("div", parts...)
}

type goalsView struct{}

// Render implements View for "/goals".
func (goalsView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadGoals(ctx)
	if err != nil {
		return "", "", err
	}
	return titleGoals, RenderGoals(ctx, d), nil
}

// ── detail ──

type planInfo struct {
	Version int64
	Created string
}

type goalUsage struct {
	State                                             string
	Merged, Timed                                     int
	MedianS                                           *int64
	Tokens                                            *int64
	Cost, TasksCost, OverheadCost, ReworkShare, Hours *float64
	Unpriced                                          bool
}

// GoalDetailData is what the goal detail page renders. Found false is the calm not-found state.
type GoalDetailData struct {
	ID    string
	Found bool
	data.GoalDetail
	TeamRows []data.TeamRow
	Tasks    []data.Task
	Plan     *planInfo
	Blocking []NeedsItem
	Usage    goalUsage
}

func medianInts(vs []int64) (int64, bool) {
	if len(vs) == 0 {
		return 0, false
	}
	s := append([]int64(nil), vs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s)%2 == 1 {
		return s[len(s)/2], true
	}
	return pyRound(float64(s[len(s)/2-1]+s[len(s)/2]) / 2), true
}

// usageOf is the numbers of the "Cost and time" card. Merged PRs and the median spawn-to-merge come from Throughput
// over all time; tokens, cost and rework share from tokens.GoalUsage. A Cursor goal has agent-hours and no tokens or
// cost; an index that is not ready is reported as such, never as zero.
func usageOf(ctx *Ctx, gid string) (goalUsage, error) {
	t, err := ctx.DB.Throughput(allDays, ctx.Now, data.GoalSet{gid: true})
	if err != nil {
		return goalUsage{}, fmt.Errorf("views: throughput: %w", err)
	}
	var times []int64
	for _, i := range t.Tasks {
		if i.TotalS != nil {
			times = append(times, *i.TotalS)
		}
	}
	out := goalUsage{State: "ok", Merged: len(t.Tasks), Timed: len(times)}
	if m, ok := medianInts(times); ok {
		out.MedianS = &m
	}
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return out, fmt.Errorf("views: goal orgs: %w", err)
	}
	var snap *tokens.Snapshot
	att := map[string]tokens.Attr{}
	if data.OrgOfGoal(gorgs, gid) == "cursor" {
		snap = &tokens.Snapshot{Ready: true}
		out.State = "cursor"
	} else {
		if ctx.Tokens != nil {
			snap = ctx.Tokens()
		}
		if snap == nil || !snap.Ready {
			out.State = "not_ready"
			return out, nil
		}
		if att, err = tokens.Attribute(snap, ctx.DB.DB); err != nil {
			return out, fmt.Errorf("views: token attribution: %w", err)
		}
	}
	u, err := tokens.GoalUsage(snap, att, ctx.DB.DB, gid, ctx.Now)
	if err != nil {
		return out, fmt.Errorf("views: goal usage: %w", err)
	}
	tot := u.Totals
	if tot == nil {
		return out, fmt.Errorf("views: goal usage for %s has no totals", gid)
	}
	if out.State == "cursor" {
		if tot.WallS != nil {
			h := roundTo2(*tot.WallS / 3600)
			out.Hours = &h
		}
		return out, nil
	}
	ids := make([]string, 0, len(u.Tasks))
	for id := range u.Tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var costs []*float64
	for _, id := range ids {
		x := u.Tasks[id]
		if x != nil && x.Totals != nil && x.Totals.Tokens != nil {
			costs = append(costs, x.Totals.Cost)
		}
	}
	out.Tokens, out.ReworkShare = tot.Tokens, tot.ReworkShare
	if tot.Tokens != nil {
		out.Cost = tot.Cost
	}
	anyNil := false
	sum := 0.0
	for _, c := range costs {
		if c == nil {
			anyNil = true
		} else {
			sum += *c
		}
	}
	if !anyNil && len(costs) > 0 {
		out.TasksCost = &sum
	}
	oh := u.Overhead
	if oh != nil && oh.Totals.Tokens != nil {
		out.OverheadCost = oh.Totals.Cost
	}
	out.Unpriced = anyNil || (oh != nil && oh.Totals.Tokens != nil && oh.Totals.Cost == nil)
	return out, nil
}

func roundTo2(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 2, 64), 64)
	return f
}

// LoadGoalDetail gathers the data of the goal detail page.
func LoadGoalDetail(ctx *Ctx, id string) (*GoalDetailData, error) {
	d := &GoalDetailData{ID: id}
	if id == "" {
		return d, nil
	}
	g, err := ctx.DB.GoalDetail(id)
	if err != nil {
		return nil, fmt.Errorf("views: goal detail: %w", err)
	}
	if g == nil {
		return d, nil
	}
	d.Found, d.GoalDetail = true, *g
	grouped, err := ctx.DB.GoalTeams(ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: goal teams: %w", err)
	}
	if e, ok := grouped.Get(id); ok && e != nil {
		for _, t := range e.Teams {
			row := *t
			if row.Scope != nil {
				s := trunc(*row.Scope, teamScopeMax)
				row.Scope = &s
			}
			d.TeamRows = append(d.TeamRows, row)
		}
	}
	if d.Tasks, err = ctx.DB.Tasks(id, ""); err != nil {
		return nil, fmt.Errorf("views: tasks: %w", err)
	}
	var version int64
	var created string
	err = ctx.DB.QueryRow("SELECT version, created FROM plans WHERE goal_id=? ORDER BY version DESC LIMIT 1", id).Scan(&version, &created)
	switch {
	case err == nil:
		d.Plan = &planInfo{version, created}
	case err != sql.ErrNoRows:
		return nil, fmt.Errorf("views: read plan: %w", err)
	}
	taskIDs := map[string]bool{}
	for _, t := range d.Tasks {
		taskIDs[t.ID] = true
	}
	all, err := needs(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	for _, i := range all {
		if i.Ref == id || (i.Ref != "" && taskIDs[i.Ref]) {
			d.Blocking = append(d.Blocking, i)
		}
	}
	if d.Usage, err = usageOf(ctx, id); err != nil {
		return nil, err
	}
	return d, nil
}

func costTimeCard(d *GoalDetailData) Safe {
	u := d.Usage
	m := func(x *float64) string {
		if x == nil {
			return "-"
		}
		return money(*x)
	}
	type kv struct {
		k string
		v any
	}
	var rows []kv
	var body Safe
	switch u.State {
	case "not_ready":
		body = El("p", "Cost: not ready (the token index is still warming up).", Cls("muted"))
	case "cursor":
		hours := "-"
		if u.Hours != nil {
			hours = pyFloat(*u.Hours) + " agent-hours"
		}
		body = El("p", "Tokens and cost: not available. The Cursor CLI reports no token counts.", Cls("muted"))
		rows = []kv{{"Agent-hours", hours}}
	default:
		note := fmt.Sprintf("Estimated at list prices as of %s (API-equivalent, not a bill).", tokens.PricesAsOf)
		if u.Unpriced {
			note += " Some tokens are on a model without a price, so the cost is unknown."
		}
		body = El("p", note, Cls("muted"))
		tk, rw := "-", "-"
		if u.Tokens != nil {
			tk = commas(*u.Tokens)
		}
		if u.ReworkShare != nil {
			rw = fmt.Sprintf("%d%%", pyRound(100**u.ReworkShare))
		}
		rows = []kv{{"Tokens", tk}, {"Estimated cost, tasks", m(u.TasksCost)}, {"Estimated cost, goal overhead", m(u.OverheadCost)},
			{"Estimated cost, total", m(u.Cost)}, {"Rework share", rw}}
	}
	med := "-"
	if u.MedianS != nil {
		med = Dur(float64(*u.MedianS), true)
	}
	tail := []kv{{"Merged PRs", u.Merged}, {"Median wall time per merged PR", med}}
	if u.State == "not_ready" {
		rows = tail
	} else {
		rows = append(rows, tail...)
	}
	var dl []any
	for _, r := range rows {
		dl = append(dl, []any{El("dt", r.k), El("dd", r.v)})
	}
	return Section("Cost and time", nil, El("dl", dl, Cls("rail-dl")), body)
}

func goalCrumb(id string) Safe { return Crumbs(Crumb{"/goals", "Goals"}, Crumb{"/goal/" + id, id}) }

func blockingSection(d *GoalDetailData, ctx *Ctx) any {
	shown := map[string]bool{}
	var rows []any
	for _, i := range d.Blocking {
		shown[i.Ref] = true
		rows = append(rows, NeedsYouRow(i))
	}
	for _, t := range d.Tasks {
		if t.Status == "blocked" && !shown[t.ID] {
			age := ""
			if a := Ago(t.Updated, ctx.Now, ""); a != "" {
				age = "since " + a
			}
			rows = append(rows, NeedsYouRow(NeedsItem{Kind: "Queued task", Tone: "neutral", Subject: stripTags(t.Title), Ref: t.ID, Href: "/task/" + t.ID, Age: age}))
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return Section("What's blocking", len(rows), El("ul", rows, Cls("lrow-list")))
}

// metMap is {criterion n: met} by the /goals rule: every non-cancelled [SCn]-tagged task is done, and at least one exists.
func metMap(d *GoalDetailData) map[int64]bool {
	by := map[string]data.Task{}
	for _, t := range d.Tasks {
		by[t.ID] = t
	}
	out := map[int64]bool{}
	for _, c := range d.Criteria {
		live, all := 0, true
		for _, id := range c.Tasks {
			if t, ok := by[id]; ok && t.Status != "cancelled" {
				live++
				all = all && t.Status == "done"
			}
		}
		out[c.N] = live > 0 && all
	}
	return out
}

func criteriaSection(d *GoalDetailData) Safe {
	by := map[string]data.Task{}
	for _, t := range d.Tasks {
		by[t.ID] = t
	}
	mets := metMap(d)
	var rows []any
	nMet := 0
	for _, c := range d.Criteria {
		met := mets[c.N]
		if met {
			nMet++
		}
		var linked []any
		for _, id := range c.Tasks {
			t, ok := by[id]
			if !ok {
				continue
			}
			lab := Labels[t.Status]
			linked = append(linked, El("a", RowStatus(t.Status), At("href", "/task/"+id), At("title", id+" "+lab), At("aria-label", id+", "+lab)))
		}
		gk, word := "todo", "Open"
		if met {
			gk, word = "done", "Met"
		}
		var lk any
		if len(linked) > 0 {
			lk = El("span", linked, Cls("crit-tasks"))
		}
		rows = append(rows, El("li", RowStatus(gk), El("span", word, Cls("sr-only")), El("span", fmt.Sprintf("SC%d", c.N), Cls("mono lrow-id")),
			El("span", c.Text, Cls("crit-text")), lk, Cls("lrow")))
	}
	var count any
	var body any = EmptyState("No success criteria.", nil)
	if len(rows) > 0 {
		body = El("ul", rows, Cls("lrow-list"))
		count = fmt.Sprintf("%d/%d", nMet, len(d.Criteria))
	}
	return Section("Success criteria", count, body)
}

func tasksTable(d *GoalDetailData) Safe {
	cols := []Col{{Key: "status", Label: "", Cls: "c-st"}, {Key: "id", Label: "ID", Cls: "c-id mono"}, {Key: "title", Label: "Task", Cls: "c-title"},
		{Key: "assignee", Label: "Assignee", Cls: "c-who"}, {Key: "pr", Label: "PR", Cls: "c-who"}, {Key: "tries", Label: "Tries", Cls: "c-age num"}}
	var groups []Group
	for _, s := range GroupOrder {
		var rows [][]any
		for _, t := range d.Tasks {
			if t.Status != s {
				continue
			}
			var badge any
			if s == "blocked" {
				badge = TaskBadge("blocked", t.Waits)
			}
			who := deref(t.Assignee)
			if who == "" {
				who = t.Role
			}
			var pr any = ""
			if t.PRURL != nil && *t.PRURL != "" {
				pr = Link(*t.PRURL, "PR")
			}
			var tries any = ""
			if t.Attempts != 0 {
				tries = t.Attempts
			}
			rows = append(rows, []any{RowStatus(t.Status), t.ID, El("span", El("a", At("href", "/task/"+t.ID), t.Title), " ", badge), who, pr, tries})
		}
		if len(rows) > 0 {
			groups = append(groups, Group{Key: s, Label: Labels[s], Count: len(rows), Rows: rows, Collapsed: Collapsed[s]})
		}
	}
	var count any
	if len(d.Tasks) > 0 {
		count = len(d.Tasks)
	}
	return Section("Tasks", count, DataTable(cols, groups, nil, nil, "goal-tasks", "No tasks yet."))
}

func intentSection(d *GoalDetailData) any {
	if d.Intent == nil || *d.Intent == "" {
		return nil
	}
	text := *d.Intent
	body := El("p", text, Cls("intent"))
	if utf8.RuneCountInString(text) > intentOpenMax || strings.Count(text, "\n") >= 6 {
		return El("div", Disclosure("Intent", "goal-intent", nil, false, body), Cls("sect"))
	}
	return Section("Intent", nil, body)
}

// teamsSection lists the goal's teams, each with its running agents, directly under Intent. None when the goal has no
// team: no empty box. A team with no agents is a plain row.
func teamsSection(d *GoalDetailData, ctx *Ctx) any {
	var rows []any
	for _, t := range d.TeamRows {
		var scope any
		if t.Scope != nil && *t.Scope != "" {
			scope = El("span", *t.Scope, Cls("tscope"))
		}
		head := El("div", El("span", t.ID, Cls("mono lrow-id")), " ", El("span", deref(t.LeadAgent)), " ", StatusBadge(t.Status), scope, Cls("tsum"))
		var ags []any
		for _, a := range t.Agents {
			var spans []any
			for i, c := range AgentRowCells(agentRowOf(a), ctx.Now) {
				spans = append(spans, El("span", c, Cls(AgentCols[i].Cls)))
			}
			ags = append(ags, El("li", spans, Cls("agentrow")))
		}
		var agEl any
		if len(ags) > 0 {
			agEl = El("ul", ags, Cls("lrow-list"))
		}
		rows = append(rows, El("li", head, agEl, Cls("team")))
	}
	if len(rows) == 0 {
		return nil
	}
	return Section("Teams", len(rows), El("ul", rows, Cls("teams")))
}

func agentRowOf(a data.Agent) AgentRow {
	return AgentRow{Name: a.Name, Role: a.Role, Status: a.Status, Runtime: a.Runtime, Org: a.Org, TaskID: deref(a.TaskID)}
}

func railEl(d *GoalDetailData) Safe {
	plan := []any{"No plan yet", nil}
	if d.Plan != nil {
		plan = []any{fmt.Sprintf("v%d · ", d.Plan.Version), Ts(d.Plan.Created)}
	}
	defs := [][]any{{"Plan", plan[0], plan[1]}, {"Created", Ts(d.Created), nil}, {"Updated", Ts(d.Updated), nil}}
	var items []any
	for _, x := range defs {
		items = append(items, []any{El("dt", x[0]), El("dd", x[1], x[2])})
	}
	return El("aside", El("dl", items, Cls("rail-dl")), Cls("rail"), At("aria-label", "Goal details"))
}

// RenderGoalDetail renders the goal detail body.
func RenderGoalDetail(ctx *Ctx, d *GoalDetailData) Safe {
	if !d.Found {
		id := d.ID
		crumbID := id
		if crumbID == "" {
			crumbID = "?"
		}
		return notFound("goal", id, "/goals", "Back to Goals", goalCrumb(crumbID))
	}
	done, total := d.TasksDone, d.TasksTotal
	tagged := false
	for _, c := range d.Criteria {
		tagged = tagged || len(c.Tasks) > 0
	}
	summary := fmt.Sprintf("%d/%d tasks", done, total)
	if tagged {
		n := 0
		for _, m := range metMap(d) {
			if m {
				n++
			}
		}
		summary += fmt.Sprintf(" · %d/%d criteria", n, len(d.Criteria))
	}
	team := "no team"
	if len(d.TeamRows) > 0 {
		team = d.TeamRows[0].ID
	}
	pri := fmt.Sprintf("P%d", d.Priority)
	meta := El("p", StatusBadge(d.Status), " ", OrgBadge(d.Org, ""), " · ", pri, " · ", team, " · updated ", Ts(d.Updated), Cls("metarow"))
	main := El("div", meta, intentSection(d), teamsSection(d, ctx), Meter(done, total, summary, "tasks"), costTimeCard(d), blockingSection(d, ctx),
		criteriaSection(d), tasksTable(d), Cls("gd-main"))
	return El("div", goalCrumb(d.ID), El("div", main, railEl(d), Cls("gd")))
}

type goalDetailView struct{}

// Render implements View for "/goal/<id>"; params["id"] is the goal id.
func (goalDetailView) Render(ctx *Ctx, params map[string]string) (string, Safe, error) {
	d, err := LoadGoalDetail(ctx, params["id"])
	if err != nil {
		return "", "", err
	}
	title := "Goal"
	if d.Found && d.Title != "" {
		title = d.Title
	}
	return title, RenderGoalDetail(ctx, d), nil
}
