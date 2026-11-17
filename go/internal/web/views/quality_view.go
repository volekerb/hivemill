package views

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/web/data"
)

// Quality page constants (web/views/quality.py).
const (
	titleQuality = "Quality"
	qlWeeks      = 12 // WEEKS
	qlDeltaDays  = 14 // DELTA_DAYS
	qlBouncing   = 6  // BOUNCING_MAX
	qlFormula    = "Rejections per merged PR = rejection events / merged PRs; retries = attempts beyond the first; " +
		"CI pass rate = merged / (merged + tasks the judge blocked for red checks)."
	qlNotes = "Rejections count failed QA/review verdicts, plus old judge rejections that left no verdict row. " +
		"Merged counts tasks with a merge event. Tasks without a team are counted in every team of their goal. " +
		"Rework time is 0 when a task was never sent back."
	qlCauseDef = "Recorded causes on reviewer, QA and judge rejections and gate refusals (counts incidents, not events)."
)

var teamSorts = map[string]string{"team": "asc", "merged": "desc", "rejections": "desc", "rate": "desc"} // SORTS

// QualityData is what the Quality page shows (web/views/quality.py data()).
type QualityData struct {
	Org       string
	Overall   data.QualityRow
	Teams     []data.QualityRow
	Weeks     map[string]int64
	Windows   *data.QualityWindowsResult
	Bouncing  []data.Bouncing
	ByCause   *data.OMap[int64]
	GoalCount int
}

// withUnassigned is quality._with_unassigned: the team_id-NULL tasks of a team's goal are added to that team (the way
// bin/factory scorecard_rows attributes them); the per-team ratio is recomputed after the addition.
func withUnassigned(c *data.Conn, q *data.QualityResult) ([]data.QualityRow, error) {
	rows, err := c.Query("SELECT id, goal_id FROM teams")
	if err != nil {
		return nil, fmt.Errorf("views: read teams: %w", err)
	}
	goal := map[string]string{}
	for rows.Next() {
		var id, g string
		if err := rows.Scan(&id, &g); err != nil {
			rows.Close()
			return nil, fmt.Errorf("views: scan teams: %w", err)
		}
		goal[id] = g
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("views: read teams: %w", err)
	}
	var out []data.QualityRow
	for _, t := range q.Teams {
		if t.TeamID != nil {
			if g, ok := goal[*t.TeamID]; ok {
				if e, ok := q.Unassigned.M[g]; ok {
					t.Tasks += e.Tasks
					t.Merged += e.Merged
					t.Rejections += e.Rejections
					t.Retries += e.Retries
					t.MergedFirstTry += e.MergedFirstTry
					t.MergedAfterRejection += e.MergedAfterRejection
					t.RejReviewer += e.RejReviewer
					t.RejQA += e.RejQA
					t.RejJudge += e.RejJudge
					t.GateJudgeBlocked += e.GateJudgeBlocked
					t.GateMergeFailed += e.GateMergeFailed
					t.GateRefusals += e.GateRefusals
					t.RejectionsPerMerged = nil
					if t.Merged != 0 {
						t.RejectionsPerMerged = data.FloatP(roundN(float64(t.Rejections)/float64(t.Merged), 2))
					}
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// weekly is quality._weekly: rejection counts per local week (Monday), the last WEEKS weeks that have any.
func weekly(c *data.Conn, goals data.GoalSet) (map[string]int64, error) {
	tgoal := map[string]string{}
	rows, err := c.Query("SELECT id, goal_id FROM tasks")
	if err != nil {
		return nil, fmt.Errorf("views: read tasks: %w", err)
	}
	for rows.Next() {
		var id, g string
		if err := rows.Scan(&id, &g); err != nil {
			rows.Close()
			return nil, fmt.Errorf("views: scan tasks: %w", err)
		}
		tgoal[id] = g
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("views: read tasks: %w", err)
	}
	in := func(task string) bool {
		if goals == nil {
			return true
		}
		g, ok := tgoal[task]
		return ok && goals[g]
	}
	week := func(iso string) (string, bool) {
		t, ok := data.ParseTS(iso)
		if !ok {
			return "", false
		}
		return localMonday(t), true
	}
	counts := map[string]int64{}
	vcount := map[string]int{}
	vr, err := c.Query("SELECT task_id, role, created FROM verdicts WHERE verdict IN ('fail','reject')")
	if err != nil {
		return nil, fmt.Errorf("views: read verdicts: %w", err)
	}
	for vr.Next() {
		var task, role, created string
		if err := vr.Scan(&task, &role, &created); err != nil {
			vr.Close()
			return nil, fmt.Errorf("views: scan verdicts: %w", err)
		}
		if !in(task) {
			continue
		}
		if w, ok := week(created); ok {
			counts[w]++
		}
		if role == "judge" {
			vcount[task]++
		}
	}
	err = vr.Err()
	vr.Close()
	if err != nil {
		return nil, fmt.Errorf("views: read verdicts: %w", err)
	}
	evs := map[string][]string{}
	er, err := c.Query("SELECT entity_id, ts FROM events WHERE entity='task' AND action='judge-reject' ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("views: read judge rejects: %w", err)
	}
	var order []string
	for er.Next() {
		var task, ts string
		if err := er.Scan(&task, &ts); err != nil {
			er.Close()
			return nil, fmt.Errorf("views: scan judge rejects: %w", err)
		}
		if !in(task) {
			continue
		}
		if _, ok := evs[task]; !ok {
			order = append(order, task)
		}
		evs[task] = append(evs[task], ts)
	}
	err = er.Err()
	er.Close()
	if err != nil {
		return nil, fmt.Errorf("views: read judge rejects: %w", err)
	}
	for _, task := range order {
		tss := evs[task]
		extra := max(0, len(tss)-vcount[task])
		for _, ts := range tss[len(tss)-extra:] {
			if w, ok := week(ts); ok {
				counts[w]++
			}
		}
	}
	weeks := make([]string, 0, len(counts))
	for w := range counts {
		weeks = append(weeks, w)
	}
	sort.Strings(weeks)
	if len(weeks) > qlWeeks {
		weeks = weeks[len(weeks)-qlWeeks:]
	}
	out := map[string]int64{}
	for _, w := range weeks {
		out[w] = counts[w]
	}
	return out, nil
}

// LoadQuality gathers the data of the Quality page.
func LoadQuality(ctx *Ctx) (*QualityData, error) {
	goals, org, err := goalFilter(ctx)
	if err != nil {
		return nil, err
	}
	q, err := ctx.DB.Quality(goals)
	if err != nil {
		return nil, fmt.Errorf("views: quality: %w", err)
	}
	teams, err := withUnassigned(ctx.DB, q)
	if err != nil {
		return nil, err
	}
	if goals != nil {
		var kept []data.QualityRow
		for _, t := range teams {
			if t.Tasks != 0 {
				kept = append(kept, t)
			}
		}
		teams = kept
	}
	d := &QualityData{Org: org, Overall: q.Overall, Teams: teams, ByCause: q.RejectionsByCause}
	if d.Weeks, err = weekly(ctx.DB, goals); err != nil {
		return nil, err
	}
	if d.Windows, err = ctx.DB.QualityWindows(qlDeltaDays, ctx.Now, goals); err != nil {
		return nil, fmt.Errorf("views: quality windows: %w", err)
	}
	if d.Bouncing, err = ctx.DB.BouncingTasks(qlBouncing, goals); err != nil {
		return nil, fmt.Errorf("views: bouncing tasks: %w", err)
	}
	return d, nil
}

func pctOf(x *data.Num) string {
	if x == nil {
		return "-"
	}
	return fmt.Sprintf("%d%%", pyRound(100*x.V))
}

func rpmText(x *data.Num) string {
	if x == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", x.V)
}

// qlDelta is quality._delta: the text and tone of "↓ 0.25 vs prev 14 d", ok=false when either period has nothing to
// compare.
func qlDelta(cur, prev *float64, format func(float64) string, lowerIsBetter bool, days int) (string, string, bool) {
	if cur == nil || prev == nil {
		return "", "", false
	}
	diff := *cur - *prev
	if diff == 0 {
		return fmt.Sprintf("no change vs prev %d d", days), "neutral", true
	}
	arrow := "↓"
	if diff > 0 {
		arrow = "↑"
	}
	tone := "warning"
	if (diff < 0) == lowerIsBetter {
		tone = "success"
	}
	abs := diff
	if abs < 0 {
		abs = -abs
	}
	return fmt.Sprintf("%s %s vs prev %d d", arrow, format(abs), days), tone, true
}

func deltaOpts(def string, text, tone string, ok bool) TileOpts {
	o := TileOpts{Definition: def}
	if ok {
		o.Delta, o.DeltaTone = text, tone
	}
	return o
}

func f2(x float64) string { return fmt.Sprintf("%.2f", x) }

func intF(p *int64) *float64 {
	if p == nil {
		return nil
	}
	f := float64(*p)
	return &f
}

func numFP(n *data.Num) *float64 {
	if n == nil {
		return nil
	}
	f := n.V
	return &f
}

func qlHeadline(d *QualityData) Safe {
	o, w := d.Overall, d.Windows
	cur, prev, days := w.Current, w.Previous, w.Days
	since := fmt.Sprintf(" The change compares the last %d days with the %d days before.", days, days)
	first := "-"

	if o.Merged != 0 {
		n := pyRound(100 * float64(o.MergedFirstTry) / float64(o.Merged))
		first = fmt.Sprintf("%d%%", n)
	}
	t1, k1, ok1 := qlDelta(numFP(cur.RejectionsPerMerged), numFP(prev.RejectionsPerMerged), f2, true, days)
	t2, k2, ok2 := qlDelta(intF(cur.FirstTryPct), intF(prev.FirstTryPct), func(x float64) string { return fmtG(x) + " pts" }, false, days)
	ci, cp := float64(cur.CIBlocked), float64(prev.CIBlocked)
	t3, k3, ok3 := qlDelta(&ci, &cp, fmtG, true, days)
	t4, k4, ok4 := qlDelta(numFP(cur.MedianAttempts), numFP(prev.MedianAttempts), fmtG, true, days)
	med := "-"
	if m := w.All.MedianAttempts; m != nil {
		med = fmtG(m.V)
	}
	return El("div",
		Tile("Rejections per merged PR", rpmText(o.RejectionsPerMerged), deltaOpts("Rejections divided by merged PRs, over all time. Lower is better."+since, t1, k1, ok1)),
		Tile("First-try merge rate", first, deltaOpts("Merged tasks that were never rejected, as a share of merged PRs, over all time."+since, t2, k2, ok2)),
		Tile("CI-blocked merges", o.CIBlocked, deltaOpts("Times the judge refused a merge because checks were red or still pending, over all time."+since, t3, k3, ok3)),
		Tile("Median attempts", med, deltaOpts("Median number of attempts of the tasks that merged, over all time. 1 means most merged first time."+since, t4, k4, ok4)),
		Cls("tiles tiles--4"))
}

func causeTile(by *data.OMap[int64]) Safe {
	if by == nil || len(by.Keys) == 0 {
		return Tile("Rejections by cause", "Not measured yet", TileOpts{Definition: "No cause is stored yet (counts incidents, not events)."})
	}
	type kv struct {
		c string
		n int64
	}
	var items []kv
	var total int64
	for _, k := range by.Keys {
		items = append(items, kv{k, by.M[k]})
		total += by.M[k]
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		return items[i].c < items[j].c
	})
	var lines []string
	for _, it := range items {
		lines = append(lines, fmt.Sprintf("%s %d", strings.ReplaceAll(it.c, "_", " "), it.n))
	}
	return Tile("Rejections by cause", strconv.FormatInt(total, 10), TileOpts{Definition: fmt.Sprintf("%s %s.", qlCauseDef, strings.Join(lines, ", "))})
}

func qlMore(d *QualityData) Safe {
	o := d.Overall
	var scored []string
	for _, t := range d.Teams {
		if t.Score != nil {
			scored = append(scored, fmt.Sprintf("%s %d/5", deref(t.TeamID), *t.Score))
		}
	}
	scores := strings.Join(scored, ", ")
	if scores == "" {
		scores = "-"
	}
	retries := "-"
	if o.Merged != 0 {
		retries = fmt.Sprintf("%.2f", float64(o.Retries)/float64(o.Merged))
	}
	tiles := []any{
		Tile("Merged PRs", o.Merged, TileOpts{Definition: "Distinct tasks with a merge event."}),
		Tile("Merged first try", o.MergedFirstTry, TileOpts{Definition: "Merged tasks that were never rejected."}),
		Tile("Merged after >= 1 rejection", o.MergedAfterRejection, TileOpts{Definition: "Merged tasks rejected at least once before they merged."}),
		Tile("Rejections", o.Rejections, TileOpts{Definition: "Reviewer, QA and judge fail or reject verdicts, plus judge rejections without a verdict row."}),
		Tile("Rejections per merged PR", rpmText(o.RejectionsPerMerged), TileOpts{Definition: "Rejections divided by merged PRs."}),
		Tile("Gate refusals", o.GateRefusals, TileOpts{Definition: "Judge-blocked and merge-failed events: stale verdict, red or pending CI, conflicts."}),
		causeTile(d.ByCause),
		Tile("Retries per merged task", retries, TileOpts{Definition: "Attempts beyond the first, per merged task."}),
		Tile("CI pass rate", pctOf(o.CIPassRate), TileOpts{Definition: "Merged / (merged + tasks the judge blocked for red checks)."}),
		Tile("Latest team scores", scores, TileOpts{Definition: "Newest evaluation per team."}),
	}
	return Disclosure("More metrics", "more-metrics", len(tiles), false, El("div", append(tiles, Cls("tiles"))...), El("p", qlFormula, Cls("muted")), El("p", qlNotes, Cls("muted")))
}

func qlBouncingSection(d *QualityData) Safe {
	var rows []any
	for _, b := range d.Bouncing {
		s := ""
		if b.Rejections != 1 {
			s = "s"
		}
		rows = append(rows, ListRow(b.Status, b.TaskID, b.Title, "/task/"+b.TaskID, fmt.Sprintf("%d rejection%s", b.Rejections, s), fmt.Sprintf("attempt %d", b.Attempts)))
	}
	var body any
	var count any
	if len(rows) > 0 {
		body, count = El("ul", rows, Cls("lrow-list")), len(rows)
	} else {
		body = EmptyState("Nothing is bouncing", nil)
	}
	return Section("Bouncing now", count, body)
}

func qlTeams(ctx *Ctx, d *QualityData) Safe {
	key, dir := data.TableSort(ctx.Query, teamSorts, [2]string{"team", "asc"})
	if len(d.Teams) == 0 {
		return Section("Per team", nil, EmptyState("No teams yet.", nil))
	}
	getters := map[string]func(data.QualityRow) any{
		"team": func(t data.QualityRow) any {
			if t.TeamID == nil {
				return nil
			}
			return *t.TeamID
		},
		"merged":     func(t data.QualityRow) any { return t.Merged },
		"rejections": func(t data.QualityRow) any { return t.Rejections },
		"rate": func(t data.QualityRow) any {
			if t.RejectionsPerMerged == nil {
				return nil
			}
			return t.RejectionsPerMerged.V
		},
	}
	rows := data.SortRows(d.Teams, key, dir, getters)
	sortHref := func(col string) string {
		flip := teamSorts[col]
		if key == col {
			flip = "desc"
			if dir == "desc" {
				flip = "asc"
			}
		}
		return OrgHref("/quality", d.Org, KV{"sort", col}, KV{"dir", flip})
	}
	cols := []Col{{Key: "team", Label: "Team", Cls: "c-title", Sortable: true}, {Key: "merged", Label: "Merged", Cls: "num", Sortable: true, PhoneLabel: true},
		{Key: "rejections", Label: "Rejections", Cls: "num", Sortable: true, PhoneLabel: true},
		{Key: "rate", Label: "Rejections per merged PR", Cls: "num", Sortable: true, PhoneLabel: true, PhoneText: "Per merged PR"}}
	var table [][]any
	for _, t := range rows {
		table = append(table, []any{deref(t.TeamID), t.Merged, t.Rejections, rpmText(t.RejectionsPerMerged)})
	}
	return Section("Per team", nil, DataTable(cols, []Group{{Key: "all", Count: len(table), Rows: table}}, &SortState{key, dir}, sortHref, "teams", ""))
}

func sourceRow(name string, value, total int64) Safe {
	return El("div", El("span", name, Cls("ins-name")), Meter(value, total, strconv.FormatInt(value, 10), "rejections"), Cls("ins-row"))
}

func qlSources(d *QualityData) Safe {
	o := d.Overall
	parts := []struct {
		n string
		v int64
	}{{"Reviewer", o.RejReviewer}, {"QA", o.RejQA}, {"Judge", o.RejJudge}}
	var total int64
	var rows []any
	for _, p := range parts {
		total += p.v
	}
	for _, p := range parts {
		rows = append(rows, sourceRow(p.n, p.v, total))
	}
	gate := []struct {
		n string
		v int64
	}{{"CI", o.CIBlocked}, {"Gate: judge-blocked", o.GateJudgeBlocked}, {"Gate: merge-failed", o.GateMergeFailed}}
	var grows []any
	for _, p := range gate {
		grows = append(grows, sourceRow(p.n, p.v, o.GateRefusals))
	}
	return Section("Rejections by source", nil, El("div", rows, Cls("ins-list")),
		El("p", "CI counts merges the judge refused for red or pending checks (part of judge-blocked); they are not rejections.", Cls("muted")),
		El("div", grows, Cls("ins-list")))
}

// weekPoints is quality._week_points: the last WEEKS Mondays up to this week, oldest first, zero for a quiet week.
func weekPoints(now time.Time, weeks map[string]int64) []Point {
	l := now.In(data.Loc)
	today := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, time.UTC)
	this := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	var out []Point
	for i := qlWeeks - 1; i >= 0; i-- {
		w := this.AddDate(0, 0, -7*i).Format("2006-01-02")
		out = append(out, Point{w, float64(weeks[w])})
	}
	return out
}

func qlWeeksSection(ctx *Ctx, d *QualityData) Safe {
	if len(d.Weeks) == 0 {
		return Section("Per week", nil, EmptyState("No rejections recorded.", nil))
	}
	note := El("p", "Bars scale to the card width on desktop (weeks with more rejections are taller).", Cls("muted only-phone"))
	return Section("Per week", nil, note, Columns(weekPoints(ctx.Now, d.Weeks), "rejections", 96, "week"))
}

// RenderQuality is quality.render.
func RenderQuality(ctx *Ctx, d *QualityData) Safe {
	return El("div", qlHeadline(d), qlBouncingSection(d), qlTeams(ctx, d), qlSources(d), qlWeeksSection(ctx, d), qlMore(d))
}

type qualityView struct{}

// Render implements View.
func (qualityView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadQuality(ctx)
	if err != nil {
		return "", "", err
	}
	return titleQuality, RenderQuality(ctx, d), nil
}
