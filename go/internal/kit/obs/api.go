package obs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/prflow"
)

// Thresholds of the page banners and amber cells (docs/obs.md "Rules").
const (
	PollingShareWarn = 0.20 // rule 1: polling above this share of spend
	SpawnShareWarn   = 0.15 // a group's spawn cell turns amber above this share of its spend
	MultiRoundWarn   = 0.30 // rule 4: PRs needing 2+ review rounds above this share
	StageShareWarn   = 0.30 // rule 6: one stage holding above this share of stage hours
)

// Windows are the time ranges the API answers, in hours.
var Windows = []int{24, 72, 168, 720}

// Groups are the Spend table's group-by keys; Drill says which group a click on a row opens.
var Groups = []string{"role", "model", "goal", "project", "agent", "session"}

var groupExpr = map[string]string{
	"role":    "COALESCE(s.role,'other')",
	"model":   "r.model",
	"goal":    "COALESCE(s.goal,'')",
	"project": "COALESCE(s.project,'')",
	"agent":   "COALESCE(s.agent,'')",
	"session": "r.session",
}

// Drill is the next group of a drill-in from a row of the given group ("" = the last level).
var Drill = map[string]string{"role": "model", "model": "session", "goal": "role", "project": "goal", "agent": "session"}

// SpendQuery selects one Spend answer. Filters keys are Groups; an empty value is "Not attributed".
type SpendQuery struct {
	Hours   int
	Group   string
	Filters map[string]string
	Now     time.Time
}

// Totals are the sums of one window.
type Totals struct {
	Cost           float64  `json:"cost_usd"`
	Requests       int      `json:"requests"`
	Sessions       int      `json:"sessions"`
	Spawn          float64  `json:"spawn_usd"`
	Polling        float64  `json:"polling_usd"`
	Work           float64  `json:"work_usd"`
	CacheWriteCost float64  `json:"cache_write_usd"`
	Unpriced       int      `json:"unpriced_requests"`
	PollingPct     float64  `json:"polling_pct"`
	CacheWritePct  float64  `json:"cache_write_pct"`
	MergedPRs      *int     `json:"merged_prs"`
	PerPR          *float64 `json:"per_merged_pr_usd"`
}

// Bin is one bar of the spend chart.
type Bin struct {
	Start   string  `json:"start"`
	Work    float64 `json:"work_usd"`
	Polling float64 `json:"polling_usd"`
	Spawn   float64 `json:"spawn_usd"`
}

// Row is one row of the group-by table.
type Row struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Sessions    int       `json:"sessions"`
	Requests    int       `json:"requests"`
	Cost        float64   `json:"cost_usd"`
	Share       float64   `json:"share_pct"`
	Spawn       float64   `json:"spawn_usd"`
	Polling     float64   `json:"polling_usd"`
	Work        float64   `json:"work_usd"`
	PerPR       *float64  `json:"per_merged_pr_usd"`
	PollingPct  float64   `json:"polling_pct"`
	SpawnPct    float64   `json:"spawn_pct"`
	PollingWarn bool      `json:"polling_warn"`
	SpawnWarn   bool      `json:"spawn_warn"`
	Spark       []float64 `json:"spark_usd"`
}

// SpendAnswer is the JSON of hm-obs api spend.
type SpendAnswer struct {
	Hours           int               `json:"window_h"`
	Since           string            `json:"since"`
	Until           string            `json:"until"`
	Group           string            `json:"group"`
	Drill           string            `json:"drill"`
	Filters         map[string]string `json:"filters"`
	Current         Totals            `json:"current"`
	Previous        Totals            `json:"previous"`
	UnattributedUSD float64           `json:"unattributed_usd"`
	UnattributedPct float64           `json:"unattributed_pct"`
	BinHours        int               `json:"bin_h"`
	Bins            []Bin             `json:"bins"`
	Rows            []Row             `json:"rows"`
	Banners         []string          `json:"banners"`
	IngestedAt      string            `json:"ingested_at"`
	FlowAt          string            `json:"flow_at"`
}

// Spend answers the Spend page from obs.db.
func Spend(store string, q SpendQuery) (*SpendAnswer, error) {
	if !validWindow(q.Hours) {
		return nil, fmt.Errorf("obs: --hours %d: want one of %v", q.Hours, Windows)
	}
	if _, ok := groupExpr[q.Group]; !ok {
		return nil, fmt.Errorf("obs: --group %q: want one of %s", q.Group, strings.Join(Groups, ", "))
	}
	db, err := openRO(store, "run hm-obs ingest first; the tick runs it every 5 minutes")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	until := q.Now.UTC()
	win := time.Duration(q.Hours) * time.Hour
	since, prevSince := until.Add(-win), until.Add(-2*win)
	where, args := filterSQL(q.Filters)
	a := &SpendAnswer{Hours: q.Hours, Since: stamp(since), Until: stamp(until), Group: q.Group, Drill: Drill[q.Group],
		Filters: q.Filters, Bins: []Bin{}, Rows: []Row{}, Banners: []string{}}
	if a.Filters == nil {
		a.Filters = map[string]string{}
	}
	if a.Current, err = totals(db, since, until, where, args); err != nil {
		return nil, err
	}
	if a.Previous, err = totals(db, prevSince, since, where, args); err != nil {
		return nil, err
	}
	project := q.Filters["project"]
	if n, ok := mergedPRs(db, q.Hours, project); ok {
		a.Current.setMerged(n)
		if n2, ok2 := mergedPRs(db, 2*q.Hours, project); ok2 && n2 >= n {
			a.Previous.setMerged(n2 - n)
		}
	}
	err = db.QueryRow(`SELECT COALESCE(SUM(r.cost),0) FROM requests r LEFT JOIN sessions s ON s.session=r.session
		WHERE r.ts_ms>=? AND r.ts_ms<? AND COALESCE(s.via,'unattributed')='unattributed'`+where,
		append([]any{since.UnixMilli(), until.UnixMilli()}, args...)...).Scan(&a.UnattributedUSD)
	if err != nil {
		return nil, fmt.Errorf("obs: unattributed spend: %w", err)
	}
	a.UnattributedPct = pct(a.UnattributedUSD, a.Current.Cost)
	a.BinHours = 24
	if q.Hours <= 72 {
		a.BinHours = 1
		if q.Hours > 24 {
			a.BinHours = 3
		}
	}
	if a.Bins, err = bins(db, since, until, a.BinHours, where, args); err != nil {
		return nil, err
	}
	if a.Rows, err = rows(db, q, since, until, a.BinHours, where, args, a.Current); err != nil {
		return nil, err
	}
	if a.Current.Cost > 0 && a.Current.Polling/a.Current.Cost > PollingShareWarn {
		a.Banners = append(a.Banners, fmt.Sprintf(
			"! Polling is %.0f%% of spend ($%.2f of $%.2f) in the last %s: over %.0f%% means agents pay to re-read context while they wait.",
			100*a.Current.Polling/a.Current.Cost, a.Current.Polling, a.Current.Cost, windowLabel(q.Hours), 100*PollingShareWarn))
	}
	a.Current.round()
	a.Previous.round()
	a.UnattributedUSD = round2(a.UnattributedUSD)
	a.IngestedAt = msStamp(metaGet(db, "ingested_ms"))
	a.FlowAt = msStamp(metaGet(db, "flow_ms"))
	return a, nil
}

func (t *Totals) setMerged(n int) {
	t.MergedPRs = &n
	if n > 0 {
		v := round2(t.Cost / float64(n))
		t.PerPR = &v
	}
}

func filterSQL(f map[string]string) (string, []any) {
	var b strings.Builder
	var args []any
	for _, g := range Groups {
		v, ok := f[g]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, " AND %s=?", groupExpr[g])
		args = append(args, v)
	}
	return b.String(), args
}

const sumCols = `COUNT(DISTINCT r.session), COUNT(*), COALESCE(SUM(r.cost),0), COALESCE(SUM(r.spawn),0),
	COALESCE(SUM(r.polling),0), COALESCE(SUM(r.work),0), COALESCE(SUM(r.cache_write_cost),0), COALESCE(SUM(1-r.priced),0)`

func totals(db *sql.DB, since, until time.Time, where string, args []any) (Totals, error) {
	var t Totals
	err := db.QueryRow(`SELECT `+sumCols+` FROM requests r LEFT JOIN sessions s ON s.session=r.session
		WHERE r.ts_ms>=? AND r.ts_ms<?`+where, append([]any{since.UnixMilli(), until.UnixMilli()}, args...)...).Scan(
		&t.Sessions, &t.Requests, &t.Cost, &t.Spawn, &t.Polling, &t.Work, &t.CacheWriteCost, &t.Unpriced)
	if err != nil {
		return t, fmt.Errorf("obs: spend totals: %w", err)
	}
	t.PollingPct, t.CacheWritePct = pct(t.Polling, t.Cost), pct(t.CacheWriteCost, t.Cost)
	return t, nil
}

// round rounds the dollar fields to cents for the answer; shares and banners use the exact sums first.
func (t *Totals) round() {
	t.Cost, t.Spawn, t.Polling, t.Work, t.CacheWriteCost = round2(t.Cost), round2(t.Spawn), round2(t.Polling),
		round2(t.Work), round2(t.CacheWriteCost)
}

func bins(db *sql.DB, since, until time.Time, binH int, where string, args []any) ([]Bin, error) {
	binMS := int64(binH) * int64(time.Hour/time.Millisecond)
	start := since.UnixMilli() / binMS * binMS
	out := []Bin{}
	idx := map[int64]int{}
	for t := start; t < until.UnixMilli(); t += binMS {
		idx[t] = len(out)
		out = append(out, Bin{Start: stamp(time.UnixMilli(t))})
	}
	rs, err := db.Query(`SELECT (r.ts_ms/?)*?, SUM(r.work), SUM(r.polling), SUM(r.spawn) FROM requests r
		LEFT JOIN sessions s ON s.session=r.session WHERE r.ts_ms>=? AND r.ts_ms<?`+where+` GROUP BY 1`,
		append([]any{binMS, binMS, since.UnixMilli(), until.UnixMilli()}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("obs: spend chart: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var t int64
		var b Bin
		if err := rs.Scan(&t, &b.Work, &b.Polling, &b.Spawn); err != nil {
			return nil, fmt.Errorf("obs: spend chart: %w", err)
		}
		if i, ok := idx[t]; ok {
			out[i].Work, out[i].Polling, out[i].Spawn = round2(b.Work), round2(b.Polling), round2(b.Spawn)
		}
	}
	return out, rs.Err()
}

func rows(db *sql.DB, q SpendQuery, since, until time.Time, binH int, where string, args []any, cur Totals) ([]Row, error) {
	expr := groupExpr[q.Group]
	rs, err := db.Query(`SELECT `+expr+`, `+sumCols+` FROM requests r LEFT JOIN sessions s ON s.session=r.session
		WHERE r.ts_ms>=? AND r.ts_ms<?`+where+` GROUP BY 1`, append([]any{since.UnixMilli(), until.UnixMilli()}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("obs: spend by %s: %w", q.Group, err)
	}
	out := []Row{}
	idx := map[string]int{}
	for rs.Next() {
		var r Row
		var cw float64
		var unpriced int
		if err := rs.Scan(&r.Key, &r.Sessions, &r.Requests, &r.Cost, &r.Spawn, &r.Polling, &r.Work, &cw, &unpriced); err != nil {
			rs.Close()
			return nil, fmt.Errorf("obs: spend by %s: %w", q.Group, err)
		}
		r.Label = r.Key
		if r.Key == "" {
			r.Label = "Not attributed"
		}
		r.Share = pct(r.Cost, cur.Cost)
		r.PollingPct, r.SpawnPct = pct(r.Polling, r.Cost), pct(r.Spawn, r.Cost)
		r.PollingWarn = r.Cost > 0 && r.Polling/r.Cost > PollingShareWarn
		r.SpawnWarn = r.Cost > 0 && r.Spawn/r.Cost > SpawnShareWarn
		if cur.MergedPRs != nil && *cur.MergedPRs > 0 {
			v := round2(r.Cost / float64(*cur.MergedPRs))
			r.PerPR = &v
		}
		out = append(out, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("obs: spend by %s: %w", q.Group, err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Key < out[j].Key
	})
	for i := range out {
		r := &out[i]
		r.Cost, r.Spawn, r.Polling, r.Work = round2(r.Cost), round2(r.Spawn), round2(r.Polling), round2(r.Work)
		idx[r.Key] = i
	}
	// Spark: the row's spend per chart bin.
	binMS := int64(binH) * int64(time.Hour/time.Millisecond)
	start := since.UnixMilli() / binMS * binMS
	n := int((until.UnixMilli() - start + binMS - 1) / binMS)
	for i := range out {
		out[i].Spark = make([]float64, n)
	}
	sp, err := db.Query(`SELECT `+expr+`, (r.ts_ms/?)*?, SUM(r.cost) FROM requests r LEFT JOIN sessions s ON
		s.session=r.session WHERE r.ts_ms>=? AND r.ts_ms<?`+where+` GROUP BY 1, 2`,
		append([]any{binMS, binMS, since.UnixMilli(), until.UnixMilli()}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("obs: spend sparklines: %w", err)
	}
	defer sp.Close()
	for sp.Next() {
		var k string
		var t int64
		var c float64
		if err := sp.Scan(&k, &t, &c); err != nil {
			return nil, fmt.Errorf("obs: spend sparklines: %w", err)
		}
		if i, ok := idx[k]; ok {
			if j := int((t - start) / binMS); j >= 0 && j < n {
				out[i].Spark[j] = round2(c)
			}
		}
	}
	return out, sp.Err()
}

// mergedPRs sums the merged PRs of the stored pr-flow snapshots of one window (one project, or all).
func mergedPRs(db *sql.DB, hours int, project string) (int, bool) {
	reps, err := flowReports(db, hours, project)
	if err != nil || len(reps) == 0 {
		return 0, false
	}
	n := 0
	for _, r := range reps {
		n += mergeStat(r.rep, "all").N
	}
	return n, true
}

type storedFlow struct {
	repo, project string
	at            int64
	rep           prflow.Report
}

func flowReports(db *sql.DB, hours int, project string) ([]storedFlow, error) {
	q := `SELECT repo, project, at_ms, report FROM flow WHERE hours=?`
	args := []any{hours}
	if project != "" {
		q += ` AND project=?`
		args = append(args, project)
	}
	rs, err := db.Query(q+` ORDER BY repo`, args...)
	if err != nil {
		return nil, fmt.Errorf("obs: read flow snapshots: %w", err)
	}
	defer rs.Close()
	var out []storedFlow
	for rs.Next() {
		var f storedFlow
		var body string
		if err := rs.Scan(&f.repo, &f.project, &f.at, &body); err != nil {
			return nil, fmt.Errorf("obs: read flow snapshots: %w", err)
		}
		if err := json.Unmarshal([]byte(body), &f.rep); err != nil {
			return nil, fmt.Errorf("obs: decode flow snapshot %s %dh: %w", f.repo, hours, err)
		}
		out = append(out, f)
	}
	return out, rs.Err()
}

func mergeStat(r prflow.Report, org string) prflow.MergeStats {
	for _, m := range r.OpenToMerge {
		if m.Org == org {
			return m
		}
	}
	return prflow.MergeStats{Org: org}
}

// Stage is one box of the Flow stage map.
type Stage struct {
	Stage string  `json:"stage"`
	Label string  `json:"label"`
	Lane  string  `json:"lane"` // main | rework | side | other
	Hours float64 `json:"hours"`
	Share float64 `json:"share_pct"`
}

// Stuck is one open PR older than 6 hours.
type Stuck struct {
	Repo   string  `json:"repo"`
	Number int     `json:"number"`
	Task   string  `json:"task"`
	Stage  string  `json:"stage"`
	AgeH   float64 `json:"age_h"`
}

// FlowAnswer is the JSON of hm-obs api flow.
type FlowAnswer struct {
	Hours       int             `json:"window_h"`
	Project     string          `json:"project"`
	Repos       []string        `json:"repos"`
	At          string          `json:"at"`
	Merged      int             `json:"merged"`
	MedianH     *float64        `json:"open_to_merge_median_h"`
	P90H        *float64        `json:"open_to_merge_p90_h"`
	PRs         int             `json:"prs"`
	MultiRound  int             `json:"prs_2plus_rounds"`
	MultiPct    float64         `json:"prs_2plus_rounds_pct"`
	OpenOver6h  int             `json:"open_over_6h"`
	StageHours  float64         `json:"stage_hours"`
	Stages      []Stage         `json:"stages"`
	Stuck       []Stuck         `json:"stuck"`
	MergedByDay []prflow.DayRow `json:"merged_per_day"`
	Banners     []string        `json:"banners"`
	Warnings    []string        `json:"warnings"`
}

var stageMeta = []struct{ stage, label, lane string }{
	{prflow.StageCIWait, "CI wait", "main"},
	{prflow.StageReviewQA, "Review + QA", "main"},
	{prflow.StageJudge, "Judge", "main"},
	{prflow.StageCoderRework, "Rework", "rework"},
	{prflow.StageHold, "Hold", "side"},
	{prflow.StageCapacityGate, "Capacity gate", "side"},
	{prflow.StageRedMain, "Red main", "side"},
	{prflow.StageRefresh, "Refresh", "other"},
	{prflow.StageRedPRUnowned, "Red PR, no owner", "other"},
	{prflow.StageBlocked, "Blocked", "other"},
}

// StageLabel is the page label of a pr-flow stage.
func StageLabel(s string) string {
	for _, m := range stageMeta {
		if m.stage == s {
			return m.label
		}
	}
	return s
}

// Flow answers the Flow page from the stored pr-flow snapshots of one window.
func Flow(store string, hours int, project string) (*FlowAnswer, error) {
	if !validWindow(hours) {
		return nil, fmt.Errorf("obs: --hours %d: want one of %v", hours, Windows)
	}
	db, err := openRO(store, "run hm-obs ingest first; the tick runs it every 5 minutes")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	reps, err := flowReports(db, hours, project)
	if err != nil {
		return nil, err
	}
	a := &FlowAnswer{Hours: hours, Project: project, Repos: []string{}, Stages: []Stage{}, Stuck: []Stuck{},
		MergedByDay: []prflow.DayRow{}, Banners: []string{}, Warnings: []string{}}
	stageH := map[string]float64{}
	days := map[string]*prflow.DayRow{}
	var at int64
	var medW, p90W float64
	for _, f := range reps {
		a.Repos = append(a.Repos, f.repo)
		if f.at > at {
			at = f.at
		}
		r := f.rep
		m := mergeStat(r, "all")
		a.Merged += m.N
		medW += m.Median * float64(m.N)
		p90W += m.P90 * float64(m.N)
		a.PRs += r.PRs
		a.MultiRound += r.MultiRoundPRs
		a.OpenOver6h += len(r.OpenOver6h)
		for _, s := range r.StageHours {
			stageH[s.Stage] += s.All
		}
		for _, o := range r.OpenOver6h {
			a.Stuck = append(a.Stuck, Stuck{Repo: f.repo, Number: o.Number, Task: o.Task, Stage: o.Stage, AgeH: o.AgeHours})
		}
		for _, d := range r.MergedPerDay {
			row := days[d.Day]
			if row == nil {
				row = &prflow.DayRow{Day: d.Day}
				days[d.Day] = row
			}
			row.All += d.All
			row.Claude += d.Claude
			row.Cursor += d.Cursor
			row.Other += d.Other
		}
		for _, w := range r.Warnings {
			a.Warnings = append(a.Warnings, f.repo+": "+w)
		}
	}
	if at > 0 {
		a.At = stamp(time.UnixMilli(at))
	}
	if a.Merged > 0 {
		// One repo: pr-flow's own median and p90. Several: their merge-weighted mean (docs/obs.md).
		med, p90 := round2(medW/float64(a.Merged)), round2(p90W/float64(a.Merged))
		a.MedianH, a.P90H = &med, &p90
	}
	a.MultiPct = pct(float64(a.MultiRound), float64(a.PRs))
	for _, v := range stageH {
		a.StageHours += v
	}
	a.StageHours = round2(a.StageHours)
	var top Stage
	for _, m := range stageMeta {
		s := Stage{Stage: m.stage, Label: m.label, Lane: m.lane, Hours: round2(stageH[m.stage]),
			Share: pct(stageH[m.stage], a.StageHours)}
		a.Stages = append(a.Stages, s)
		if s.Hours > top.Hours {
			top = s
		}
	}
	sort.SliceStable(a.Stuck, func(i, j int) bool { return a.Stuck[i].AgeH > a.Stuck[j].AgeH })
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a.MergedByDay = append(a.MergedByDay, *days[k])
	}
	if a.PRs > 0 && float64(a.MultiRound)/float64(a.PRs) > MultiRoundWarn {
		a.Banners = append(a.Banners, fmt.Sprintf(
			"! %d of %d PRs (%.0f%%) needed 2+ review rounds in the last %s: over %.0f%% points at unclear specs or a weak self-review.",
			a.MultiRound, a.PRs, a.MultiPct, windowLabel(hours), 100*MultiRoundWarn))
	}
	if a.StageHours > 0 && top.Hours/a.StageHours > StageShareWarn {
		a.Banners = append(a.Banners, fmt.Sprintf(
			"! %s holds %.0f%% of stage hours (%.0f of %.0f h) in the last %s: over %.0f%% makes it the bottleneck.",
			top.Label, top.Share, top.Hours, a.StageHours, windowLabel(hours), 100*StageShareWarn))
	}
	return a, nil
}

func validWindow(h int) bool {
	for _, w := range Windows {
		if w == h {
			return true
		}
	}
	return false
}

func windowLabel(h int) string {
	if h%24 == 0 && h > 72 {
		return strconv.Itoa(h/24) + " days"
	}
	return strconv.Itoa(h) + " h"
}

func pct(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return math.Round(1000*a/b) / 10
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func msStamp(s string) string {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return ""
	}
	return stamp(time.UnixMilli(ms))
}
