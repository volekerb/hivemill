package views

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// Throughput page constants (web/views/throughput.py).
const (
	titleThroughput = "Throughput"
	defaultDays     = 14
	slowestN        = 10 // SLOWEST
	limitsText      = "Counted from factory records only. It cannot see a push the owner makes by hand, or an owner wait set on a task that was already blocked."
)

var (
	throughputDays = []int{7, 14, 30}
	// slowSorts is SORTS: the sortable column of the slowest table -> its first-click direction.
	slowSorts = map[string]string{"total": "desc", "task": "asc", "merged": "desc"}
	stageDefs = []struct{ Key, Label string }{{"coding_s", "Coding"}, {"review_s", "Review / QA wait"}, {"judge_s", "Judge"}, {"rework_s", "Rework"}}
	hqRolesTp = []string{"boss", "director", "staff-engineer"}
)

// costInfo is the "cost per merged PR" figures (throughput._cost).
type costInfo struct {
	State                  string // ready | cursor | not_ready
	Figure                 *float64
	Priced, LeftOut        int
	Merged, CursorTasks    int
	CursorHours            string // "" = not computed; Python prints round(sum(...), 2): "0" for no agents, else a float
	CursorIDs, LeftOutIDs  []string
	CostMedian, CostP90    *float64
	TokensMedian, TokensP9 *int64
}

type stageRow struct {
	Label       string
	Sum         int64
	Median      *int64
	Known, Unkn int
}

type goalRow struct {
	GoalID, Title string
	HasGoal       bool
	Done          int
	Median        *int64
}

type slowRow struct {
	TaskID, Title, GoalID, Merged string
	Total                         int64
}

// ThroughputData is what the Throughput page shows (web/views/throughput.py data()).
type ThroughputData struct {
	Org                         string
	Days, Merges, Unattended    int
	Cost                        costInfo
	Running, Leads, HQ, Workers int
	Median, P90                 *int64
	Open, LiveWorkers           int
	PerDay                      []data.DayCount
	Stages                      []stageRow
	Goals                       []goalRow
	Slowest                     []slowRow
}

func throughputDaysOf(ctx *Ctx) int {
	n, ok := pyIntText(ctx.Query["days"])
	if ok {
		for _, d := range throughputDays {
			if int64(d) == n {
				return d
			}
		}
	}
	return defaultDays
}

func ptrInt(v int64, ok bool) *int64 {
	if !ok {
		return nil
	}
	return &v
}

// mergedCostOf is throughput._cost: cost per merged PR over the Claude-org merged tasks of the period; tasks with no
// attributed tokens or a part-priced cost are counted as left out, never as $0. It never waits for the token index.
func mergedCostOf(ctx *Ctx, items []data.Stages, org string, days int) (costInfo, error) {
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return costInfo{}, fmt.Errorf("views: goal orgs: %w", err)
	}
	var cursorIDs, claudeIDs []string
	for _, i := range items {
		g := ""
		if i.GoalID != nil {
			g = *i.GoalID
		}
		if data.OrgOfGoal(gorgs, g) == "cursor" {
			cursorIDs = append(cursorIDs, i.TaskID)
		} else {
			claudeIDs = append(claudeIDs, i.TaskID)
		}
	}
	out := costInfo{State: "ready", Merged: len(claudeIDs), CursorTasks: len(cursorIDs)}
	sort.Strings(cursorIDs)
	out.CursorIDs = cursorIDs
	if org == "" || org == "cursor" {
		l := ctx.Now.In(data.Loc)
		since := l.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
		ag, err := ctx.DB.Agents(true, ctx.Now)
		if err != nil {
			return costInfo{}, fmt.Errorf("views: agents: %w", err)
		}
		var hs []float64
		for _, r := range cursorActivity(ag, since, ctx.Now, org) {
			hs = append(hs, r.Hours)
		}
		out.CursorHours = "0" // sum() of nothing is the int 0, and round(0, 2) stays an int
		if len(hs) > 0 {
			out.CursorHours = pyFloat(roundN(pySumFloats(hs), 2))
		}
	}
	if org == "cursor" {
		out.State = "cursor"
		return out, nil
	}
	var snap *tokens.Snapshot
	if ctx.Tokens != nil {
		snap = ctx.Tokens()
	}
	if snap == nil || !snap.Ready {
		out.State = "not_ready"
		return out, nil
	}
	if len(claudeIDs) == 0 {
		return out, nil
	}
	att, err := tokens.Attribute(snap, ctx.DB.DB)
	if err != nil {
		return costInfo{}, fmt.Errorf("views: attribute sessions: %w", err)
	}
	byTask := tokens.Rollup(snap, att, nil).ByTask
	var costs []float64
	var toks []int64
	priced := map[string]bool{}
	for _, t := range claudeIDs {
		b, ok := byTask[t]
		if !ok || b.Cost == nil || b.CostUnknown { // a part-priced task has a partial cost: left out too
			continue
		}
		priced[t] = true
		costs = append(costs, *b.Cost)
		toks = append(toks, b.Input+b.Output+b.CacheRead+b.CacheWrite5m+b.CacheWrite1h)
	}
	out.Priced, out.LeftOut = len(costs), len(claudeIDs)-len(costs)
	seen := map[string]bool{}
	for _, t := range claudeIDs {
		if !priced[t] && !seen[t] {
			seen[t] = true
			out.LeftOutIDs = append(out.LeftOutIDs, t)
		}
	}
	sort.Strings(out.LeftOutIDs)
	if len(costs) > 0 {
		fig := roundN(pySumFloats(costs)/float64(len(costs)), 4)
		cm, cp := roundN(pyMedianFloats(costs), 4), roundN(p90Float(costs), 4)
		out.Figure, out.CostMedian, out.CostP90 = &fig, &cm, &cp
		tm, _ := medianRounded(toks)
		tp, _ := p90Int(toks)
		out.TokensMedian, out.TokensP9 = &tm, &tp
	}
	return out, nil
}

func countWhere(c *data.Conn, query string, args ...any) (int, error) {
	var n int
	if err := c.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("views: count: %w", err)
	}
	return n, nil
}

// LoadThroughput gathers the data of the Throughput page.
func LoadThroughput(ctx *Ctx) (*ThroughputData, error) {
	days := throughputDaysOf(ctx)
	goals, org, err := goalFilter(ctx)
	if err != nil {
		return nil, err
	}
	t, err := ctx.DB.Throughput(days, ctx.Now, goals)
	if err != nil {
		return nil, fmt.Errorf("views: throughput: %w", err)
	}
	items := t.Tasks
	d := &ThroughputData{Org: org, Days: days, Merges: len(items), PerDay: t.MergesPerDay}
	var totals []int64
	for _, i := range items {
		if i.TotalS != nil {
			totals = append(totals, *i.TotalS)
		}
	}
	for _, sd := range stageDefs {
		var vals []int64
		var sum int64
		for _, i := range items {
			var v *int64
			switch sd.Key {
			case "coding_s":
				v = i.CodingS
			case "review_s":
				v = i.ReviewS
			case "judge_s":
				v = i.JudgeS
			case "rework_s":
				v = i.ReworkS
			}
			if v != nil {
				vals = append(vals, *v)
				sum += *v
			}
		}
		m, ok := medianRounded(vals)
		d.Stages = append(d.Stages, stageRow{Label: sd.Label, Sum: sum, Median: ptrInt(m, ok), Known: len(vals), Unkn: len(items) - len(vals)})
	}
	titles, err := titlesOf(ctx.DB, "tasks")
	if err != nil {
		return nil, err
	}
	gtitles, err := titlesOf(ctx.DB, "goals")
	if err != nil {
		return nil, err
	}
	perGoal := map[string][]data.Stages{}
	var noGoal []data.Stages
	for _, i := range items {
		if i.GoalID == nil {
			noGoal = append(noGoal, i)
		} else {
			perGoal[*i.GoalID] = append(perGoal[*i.GoalID], i)
		}
	}
	gids := make([]string, 0, len(perGoal))
	for g := range perGoal {
		gids = append(gids, g)
	}
	sort.Strings(gids)
	rowOf := func(gid string, has bool, v []data.Stages) goalRow {
		var ts []int64
		for _, i := range v {
			if i.TotalS != nil {
				ts = append(ts, *i.TotalS)
			}
		}
		m, ok := medianRounded(ts)
		return goalRow{GoalID: gid, HasGoal: has && gid != "", Title: gtitles[gid], Done: len(v), Median: ptrInt(m, ok)}
	}
	for _, g := range gids {
		d.Goals = append(d.Goals, rowOf(g, true, perGoal[g]))
	}
	if len(noGoal) > 0 {
		d.Goals = append(d.Goals, rowOf("", false, noGoal))
	}
	var timed []data.Stages
	for _, i := range items {
		if i.TotalS != nil {
			timed = append(timed, i)
		}
	}
	sort.SliceStable(timed, func(a, b int) bool { return *timed[a].TotalS > *timed[b].TotalS })
	if len(timed) > slowestN {
		timed = timed[:slowestN]
	}
	for _, i := range timed {
		g := ""
		if i.GoalID != nil {
			g = *i.GoalID
		}
		d.Slowest = append(d.Slowest, slowRow{TaskID: i.TaskID, Title: stripTags(titles[i.TaskID]), GoalID: g, Total: *i.TotalS, Merged: i.Merged})
	}
	if goals != nil {
		if len(goals) > 0 {
			ids := make([]string, 0, len(goals))
			for g := range goals {
				ids = append(ids, g)
			}
			ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
			if d.Open, err = countWhere(ctx.DB, "SELECT COUNT(*) FROM tasks WHERE status IN ('in_progress','in_review') AND goal_id IN ("+ph+")", anyArgs(ids)...); err != nil {
				return nil, err
			}
			if d.LiveWorkers, err = countWhere(ctx.DB, "SELECT COUNT(*) FROM agents a JOIN tasks t ON t.id=a.task_id WHERE a.status='live' "+
				"AND a.task_id IS NOT NULL AND t.goal_id IN ("+ph+")", anyArgs(ids)...); err != nil {
				return nil, err
			}
		}
	} else {
		if d.Open, err = countWhere(ctx.DB, "SELECT COUNT(*) FROM tasks WHERE status IN ('in_progress','in_review')"); err != nil {
			return nil, err
		}
		if d.LiveWorkers, err = countWhere(ctx.DB, "SELECT COUNT(*) FROM agents WHERE status='live' AND task_id IS NOT NULL"); err != nil {
			return nil, err
		}
	}
	ag, err := ctx.DB.Agents(false, ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: agents: %w", err)
	}
	for _, a := range ag {
		if a.Status != "live" || (org != "" && a.Org != org) {
			continue
		}
		d.Running++
		switch {
		case a.Role == "lead":
			d.Leads++
		case inList(hqRolesTp, a.Role):
			d.HQ++
		}
	}
	d.Workers = d.Running - d.Leads - d.HQ
	ids := make([]string, len(items))
	for k, i := range items {
		ids[k] = i.TaskID
	}
	attended, err := ctx.DB.AttendedTasks(ids)
	if err != nil {
		return nil, fmt.Errorf("views: attended tasks: %w", err)
	}
	d.Unattended = len(items) - len(attended)
	if m, ok := medianRounded(totals); ok {
		d.Median = &m
	}
	if p, ok := p90Int(totals); ok {
		d.P90 = &p
	}
	if d.Cost, err = mergedCostOf(ctx, items, org, days); err != nil {
		return nil, err
	}
	return d, nil
}

func tpHref(d *ThroughputData, params ...KV) string {
	return OrgHref("/throughput", d.Org, params...)
}

func tpRange(d *ThroughputData) Safe {
	var items []SegItem
	for _, n := range throughputDays {
		items = append(items, SegItem{fmt.Sprintf("%d days", n), tpHref(d, KV{"days", strconv.Itoa(n)}), n == d.Days})
	}
	return Segmented("Range", items, false)
}

func costTile(d *ThroughputData) Safe {
	c := d.Cost
	definition := fmt.Sprintf("Average estimated API-equivalent cost of a merged PR at list prices as of %s: the whole cost of each "+
		"merged Claude-org task in the period (not cut to the period), divided by the tasks with a known cost. "+
		"Tasks with no attributed tokens or no price are left out and counted. Cursor reports no tokens, so it has no cost.", tokens.PricesAsOf)
	var value, delta string
	switch c.State {
	case "not_ready":
		value, delta = "not ready", "The token index is still warming up."
	case "cursor":
		value, delta = "-", fmt.Sprintf("Cursor: %s agent-hours in %d days, no cost or tokens reported.", c.CursorHours, d.Days)
	default:
		value = "-"
		if c.Figure != nil {
			value = fmt.Sprintf("$%.2f", *c.Figure)
		}
		delta = fmt.Sprintf("%d of %d tasks priced", c.Priced, c.Merged)
		more := []string{fmt.Sprintf("The figure is a list-price estimate (as of %s).", tokens.PricesAsOf)}
		if c.LeftOut > 0 {
			more = append(more, fmt.Sprintf("%d left out: no tokens or no price.", c.LeftOut))
		}
		if c.CursorHours != "" {
			more = append(more, fmt.Sprintf("Cursor: %s agent-hours, no cost.", c.CursorHours))
		}
		definition += " " + strings.Join(more, " ")
	}
	return Tile("Cost per merged PR", value, TileOpts{Delta: delta, Definition: definition})
}

func tpTiles(d *ThroughputData) Safe {
	unattended := "-"
	if d.Merges > 0 {
		unattended = fmt.Sprintf("%d of %d", d.Unattended, d.Merges)
	}
	dur := func(p *int64) string { return DurP(p) }
	grid := El("div",
		Tile("Merges", d.Merges, TileOpts{Unit: fmt.Sprintf("in %d days", d.Days), UnitNL: true,
			Definition: "Tasks merged in the selected period. A task counts once, on the day of its last merge."}),
		Tile("Median spawn to merge", dur(d.Median), TileOpts{Definition: "Median time from a task's first agent spawn to its merge. Tasks without a recorded spawn are left out."}),
		Tile("p90 spawn to merge", dur(d.P90), TileOpts{Definition: "90th percentile (nearest rank) of the same times: nine in ten tasks merged faster."}),
		Tile("Agents running now", d.Running, TileOpts{Delta: fmt.Sprintf("%d workers · %d leads · %d HQ", d.Workers, d.Leads, d.HQ),
			Definition: "Agents with a live pane right now: workers, team leads and HQ (boss, director, staff engineer)."}),
		Tile("Unattended share", unattended, TileOpts{Definition: "Merged tasks in the selected period with no owner involvement: no owner wait, " +
			"no auto-merge block, no boss decision about the task."}),
		costTile(d),
		Cls("tiles tiles--6"))
	return El("div", grid, El("p", limitsText, Cls("muted")))
}

func idsText(ids []string) string {
	const capN = 8
	s := strings.Join(ids[:min(len(ids), capN)], ", ")
	if len(ids) > capN {
		s += fmt.Sprintf(" and %d more", len(ids)-capN)
	}
	return s
}

func dashOrMoney(f *float64) string {
	if f == nil {
		return "-"
	}
	return money(*f)
}

func dashOrCount(n *int64) string {
	if n == nil {
		return "-"
	}
	return commas(*n)
}

// tpSpread is throughput._spread: median and p90 of cost and of tokens per merged PR, over the same priced set as
// the average cost tile.
func tpSpread(d *ThroughputData) Safe {
	c := d.Cost
	title := "Cost and tokens per merged PR"
	switch c.State {
	case "cursor":
		return Section(title, nil, EmptyState(fmt.Sprintf("Not available for Cursor: %s agent-hours, no cost or tokens reported.", c.CursorHours), nil))
	case "not_ready":
		return Section(title, nil, EmptyState("Not ready: the token index is still warming up.", nil))
	}
	grid := El("div",
		Tile("Median cost per merged PR", dashOrMoney(c.CostMedian), TileOpts{Definition: "Median estimated cost of a merged Claude-org PR, over the same priced tasks as the average."}),
		Tile("p90 cost per merged PR", dashOrMoney(c.CostP90), TileOpts{Definition: "90th percentile (nearest rank) of the same costs."}),
		Tile("Median tokens per merged PR", dashOrCount(c.TokensMedian), TileOpts{Definition: "Median of input, output and cache tokens of the same priced tasks."}),
		Tile("p90 tokens per merged PR", dashOrCount(c.TokensP9), TileOpts{Definition: "90th percentile (nearest rank) of the same token counts."}),
		Cls("tiles"))
	notes := []string{fmt.Sprintf("%d of %d Claude tasks priced; estimated at list prices as of %s (API-equivalent, not a bill).", c.Priced, c.Merged, tokens.PricesAsOf)}
	if len(c.LeftOutIDs) > 0 {
		notes = append(notes, fmt.Sprintf("Left out (no tokens or no price): %s.", idsText(c.LeftOutIDs)))
	}
	if len(c.CursorIDs) > 0 {
		notes = append(notes, fmt.Sprintf("Cursor tasks, no cost or tokens reported: %s.", idsText(c.CursorIDs)))
	}
	return Section(title, nil, grid, El("p", strings.Join(notes, " "), Cls("muted")))
}

func tpPerDay(d *ThroughputData) Safe {
	var pts []Point
	for _, r := range d.PerDay {
		pts = append(pts, Point{r.Day, float64(r.Merges)})
	}
	return Section("Merges per day", nil, Columns(pts, "merges", 96, "day"))
}

func tpStageCard(d *ThroughputData) Safe {
	if d.Merges == 0 {
		return Section("Where the time goes", nil, EmptyState("No merged tasks in this period.", nil))
	}
	var total int64
	for _, s := range d.Stages {
		total += s.Sum
	}
	var rows []any
	for _, s := range d.Stages {
		unk := "all known"
		if s.Unkn > 0 {
			unk = fmt.Sprintf("%d unknown", s.Unkn)
		}
		var share int64
		if total != 0 {
			share = pyRound(100 * float64(s.Sum) / float64(total))
		}
		rows = append(rows, El("div", El("span", s.Label, Cls("ins-name")),
			Meter(s.Sum, total, fmt.Sprintf("%s · %d%% of total", Dur(float64(s.Sum), true), share), "seconds"),
			El("span", fmt.Sprintf("median %s · %s", DurP(s.Median), unk), Cls("muted")), Cls("ins-row")))
	}
	return Section("Where the time goes", nil, El("div", rows, Cls("ins-list")),
		El("p", "Stages that could not be measured for a task are left out and counted as unknown.", Cls("muted")))
}

func tpGoals(d *ThroughputData) Safe {
	if len(d.Goals) == 0 {
		return Section("Per goal", nil, EmptyState("No merged tasks in this period.", nil))
	}
	cols := []Col{{Key: "goal", Label: "Goal", Cls: "c-title"}, {Key: "done", Label: "Tasks done", Cls: "num"}, {Key: "median", Label: "Median per task", Cls: "num"}}
	var rows [][]any
	for _, g := range d.Goals {
		var cell any = "-"
		if g.HasGoal {
			cell = El("span", Link("/goal/"+g.GoalID, g.GoalID), " ", g.Title)
		}
		rows = append(rows, []any{cell, g.Done, DurP(g.Median)})
	}
	return Section("Per goal", nil, DataTable(cols, []Group{{Key: "all", Count: len(rows), Rows: rows}}, nil, nil, "goals", ""))
}

func tpSlowest(ctx *Ctx, d *ThroughputData) Safe {
	title := fmt.Sprintf("Slowest %d tasks", slowestN)
	if len(d.Slowest) == 0 {
		return Section(title, nil, EmptyState("No timed tasks in this period.", nil))
	}
	key, dir := data.TableSort(ctx.Query, slowSorts, [2]string{"total", "desc"})
	getters := map[string]func(slowRow) any{
		"total":  func(s slowRow) any { return s.Total },
		"task":   func(s slowRow) any { return s.TaskID },
		"merged": func(s slowRow) any { return s.Merged },
	}
	items := data.SortRows(d.Slowest, key, dir, getters)
	sortHref := func(col string) string {
		flip := slowSorts[col]
		if key == col {
			flip = "desc"
			if dir == "desc" {
				flip = "asc"
			}
		}
		return OrgHref("/throughput", d.Org, KV{"days", strconv.Itoa(d.Days)}, KV{"sort", col}, KV{"dir", flip})
	}
	cols := []Col{{Key: "task", Label: "Task", Cls: "c-title", Sortable: true}, {Key: "total", Label: "Spawn to merge", Cls: "num", Sortable: true},
		{Key: "merged", Label: "Merged", Cls: "num", Sortable: true}}
	var rows [][]any
	for _, s := range items {
		var merged any
		if _, ok := data.ParseTS(s.Merged); ok {
			merged = pyPrefix(s.Merged, 10)
		}
		rows = append(rows, []any{El("span", Link("/task/"+s.TaskID, s.TaskID), " ", s.Title), Dur(float64(s.Total), true), merged})
	}
	return Section(title, nil, DataTable(cols, []Group{{Key: "all", Count: len(rows), Rows: rows}}, &SortState{key, dir}, sortHref, "slowest", ""))
}

// pyPrefix is s[:n] on characters.
func pyPrefix(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// RenderThroughput is throughput.render.
func RenderThroughput(ctx *Ctx, d *ThroughputData) Safe {
	openNote := El("p", fmt.Sprintf("%d tasks open now (in progress or in review), %d agents working on a task.", d.Open, d.LiveWorkers), Cls("muted"))
	return El("div", tpRange(d), tpTiles(d), tpSpread(d), openNote, tpPerDay(d), tpStageCard(d), tpGoals(d), tpSlowest(ctx, d))
}

type throughputView struct{}

// Render implements View.
func (throughputView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadThroughput(ctx)
	if err != nil {
		return "", "", err
	}
	return titleThroughput, RenderThroughput(ctx, d), nil
}
