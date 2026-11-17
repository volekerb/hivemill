package views

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// Tokens page constants (web/views/tokens.py).
const (
	titleTokens   = "Tokens"
	defaultPeriod = "7"
	tokensTop     = 20 // TOP
	cursorNote    = "Token usage is not reported by Cursor CLI: it leaves no token or usage counts on this machine, so Cursor " +
		"runtime agents show sessions and agent-hours per model from the agents table, and no cost."
)

var (
	tokenPeriods = []string{"1", "7", "30", "all"}
	tokenBy      = []string{"goal", "task", "agent", "model", "org"}
	byLabel      = map[string]string{"goal": "By goal", "task": "By task", "agent": "By agent", "model": "By model", "org": "By org"}
	viaKinds     = []string{"session", "event", "name", "worktree", "unattributed"}
	tokenGoalRe  = regexp.MustCompile(`^(G-\p{Nd}+|unattributed)\n?\z`) // ^(G-\d+|unattributed)$ with Python's Unicode \d
)

// CursorRow is one (org, model) row of cursor_activity: sessions, live now and agent-hours of the Cursor runtime.
type CursorAct struct {
	Org, Model string
	Sessions   int
	Live       int
	Hours      float64
}

// agentHours is tokens._hours: hours of an agent inside [sinceE, nowE] (started to ended, a live one to now); 0 when
// it has no start. hasSince is false for "all time".
func agentHours(a data.Agent, sinceE float64, hasSince bool, nowE float64) float64 {
	start, ok := epochPtr(a.Started)
	if !ok {
		return 0
	}
	end := nowE
	if inList(endedStatuses, a.Status) {
		if e, ok := epochPtr(a.Ended); ok {
			end = e
		}
	}
	end = math.Min(end, nowE)
	lo := start
	if hasSince {
		lo = math.Max(start, sinceE)
	}
	return math.Max(0, end-lo) / 3600
}

func epochPtr(s *string) (float64, bool) {
	if s == nil {
		return 0, false
	}
	t, ok := data.ParseTS(*s)
	if !ok {
		return 0, false
	}
	return tsecOf(t), true
}

// cursorActivity is tokens.cursor_activity: Cursor runtime agents in the range, per (org, model). Cursor CLI reports
// no token counts, so this is the honest measure of what the Cursor runtime did. since is a local ISO date or "".
func cursorActivity(ag []data.Agent, since string, now time.Time, org string) []CursorAct {
	var sinceE float64
	hasSince := since != ""
	if hasSince {
		l := now.In(data.Loc)
		_, off := l.Zone()
		d, err := time.Parse("2006-01-02", since)
		if err == nil {
			sinceE = tsecOf(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.FixedZone("", off)))
		} else {
			hasSince = false
		}
	}
	nowE := tsecOf(now)
	type key struct{ org, model string }
	acc := map[key]*CursorAct{}
	for _, a := range ag {
		if a.Runtime != "cursor" || (org != "" && a.Org != org) || a.Status == "queued" {
			continue
		}
		var end float64
		endOK := true
		if inList(endedStatuses, a.Status) {
			end, endOK = epochPtr(a.Ended)
		} else {
			end = nowE
		}
		if hasSince && endOK && end < sinceE {
			continue
		}
		model := "unknown"
		if a.Model != nil && *a.Model != "" {
			model = *a.Model
		}
		k := key{a.Org, model}
		e := acc[k]
		if e == nil {
			e = &CursorAct{Org: a.Org, Model: model}
			acc[k] = e
		}
		e.Sessions++
		if a.Status == "live" {
			e.Live++
		}
		e.Hours += agentHours(a, sinceE, hasSince, nowE)
	}
	rows := make([]CursorAct, 0, len(acc))
	for _, e := range acc {
		rows = append(rows, *e)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Hours != b.Hours {
			return a.Hours > b.Hours
		}
		if a.Org != b.Org {
			return a.Org < b.Org
		}
		return a.Model < b.Model
	})
	for i := range rows {
		rows[i].Hours = roundN(rows[i].Hours, 2)
	}
	return rows
}

type tokEntry struct {
	Key                              string
	Input, Output, CacheRead, CacheW int64
	Requests                         int64
	Cost                             *float64
	CostUnknown                      bool
	// the extras of a goal / task / agent / org row
	Title        string
	Role         string
	Sessions     []string
	MoreSessions int
	CursorSess   int
	CursorHours  float64
	UnpricedReqs int64
}

func entryOf(key string, b *tokens.Bucket) tokEntry {
	return tokEntry{Key: key, Input: b.Input, Output: b.Output, CacheRead: b.CacheRead, CacheW: b.CacheWrite5m + b.CacheWrite1h,
		Requests: b.Requests, Cost: b.Cost, CostUnknown: b.CostUnknown}
}

func (e tokEntry) all() int64 { return e.Input + e.Output + e.CacheRead + e.CacheW }

// topEntries is tokens._top: entries sorted by cost (unknown last), then tokens; the first TOP and the count of the rest.
func topEntries(buckets map[string]*tokens.Bucket, extra func(*tokEntry)) ([]tokEntry, int) {
	items := make([]tokEntry, 0, len(buckets))
	for k, b := range buckets {
		e := entryOf(k, b)
		if extra != nil {
			extra(&e)
		}
		items = append(items, e)
	}
	cost := func(e tokEntry) float64 {
		if e.Cost == nil {
			return 0
		}
		return *e.Cost
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ca, cb := -cost(a), -cost(b); ca != cb {
			return ca < cb
		}
		if ta, tb := -a.all(), -b.all(); ta != tb {
			return ta < tb
		}
		return a.Key < b.Key
	})
	more := max(0, len(items)-tokensTop)
	if len(items) > tokensTop {
		items = items[:tokensTop]
	}
	return items, more
}

type tokUsage struct {
	Unreadable bool
	Label      string
	FiveHour   *float64
	SevenDay   *float64
	Fresh      bool
	AgeMin     float64
}

// TokensData is what the Tokens page shows (web/views/tokens.py data()).
type TokensData struct {
	Period, By, Org, Goal, Model string
	Usage                        *tokUsage
	Ready                        bool
	Files                        int
	MBRead                       float64
	Cursor                       []CursorAct
	Totals                       tokEntry
	ByGoal, ByTask, ByAgent      []tokEntry
	ByModel, ByOrg, ByDay        []tokEntry
	More                         map[string]int
	Via                          map[string]int
	Unpriced                     []string
}

func tokenUsageData(ctx *Ctx) *tokUsage {
	if ctx.Usage == nil {
		return nil
	}
	u := ctx.Usage()
	if u == nil || u.Unreadable { // a usage_fn that answers None is as unreadable as one that raises (tokens._usage)
		return &tokUsage{Unreadable: true, Label: "unreadable"}
	}
	t := &tokUsage{FiveHour: u.FiveHour, SevenDay: u.SevenDay, Fresh: u.Fresh, AgeMin: u.AgeMin}
	if !u.Fresh {
		t.Label = "stale: " + pyNumText(u.AgeMin) + " min old"
	}
	return t
}

func periodSince(ctx *Ctx, period string) string {
	if period == "all" {
		return ""
	}
	n, _ := strconv.Atoi(period)
	l := ctx.Now.In(data.Loc)
	d := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -(n - 1)).Format("2006-01-02")
}

// LoadTokens gathers the data of the Tokens page.
func LoadTokens(ctx *Ctx) (*TokensData, error) {
	q := ctx.Query
	d := &TokensData{Period: defaultPeriod, By: "goal", Org: ctx.org(), More: map[string]int{}, Via: map[string]int{}}
	if inList(tokenPeriods, q["days"]) {
		d.Period = q["days"]
	}
	if inList(tokenBy, q["by"]) {
		d.By = q["by"]
	}
	var snap *tokens.Snapshot
	if ctx.Tokens != nil {
		snap = ctx.Tokens()
	}
	if snap == nil {
		snap = &tokens.Snapshot{}
	}
	d.Usage = tokenUsageData(ctx)
	since := periodSince(ctx, d.Period)
	ag, err := ctx.DB.Agents(true, ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: agents: %w", err)
	}
	d.Ready, d.Files = snap.Ready, snap.Files
	d.MBRead = roundN(float64(snap.BytesTotal)/1048576, 1)
	d.Cursor = cursorActivity(ag, since, ctx.Now, d.Org)
	if !d.Ready {
		return d, nil
	}
	rows := snap.Rows
	if since != "" {
		var kept []tokens.Row
		for _, r := range rows {
			if r.Day >= since { // an "unknown" day sorts after digits: only in "all"
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	att, err := tokens.Attribute(snap, ctx.DB.DB)
	if err != nil {
		return nil, fmt.Errorf("views: attribute sessions: %w", err)
	}
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return nil, fmt.Errorf("views: goal orgs: %w", err)
	}
	nameOrg := map[string]string{}
	for _, a := range ag {
		nameOrg[a.Name] = a.Org
	}
	orgOf := func(sid string) string {
		a := att[sid]
		if a.Agent != nil {
			if o, ok := nameOrg[*a.Agent]; ok {
				return o
			}
		}
		if a.Goal != nil && *a.Goal != "" {
			return data.OrgOfGoal(gorgs, *a.Goal)
		}
		return data.DefaultOrg
	}
	if d.Org != "" {
		var kept []tokens.Row
		for _, r := range rows {
			if orgOf(r.Session) == d.Org {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	modelSet := map[string]bool{}
	for _, r := range rows {
		modelSet[r.Model] = true
	}
	if m := q["model"]; modelSet[m] {
		d.Model = m
		var kept []tokens.Row
		for _, r := range rows {
			if r.Model == m {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if g := q["goal"]; tokenGoalRe.MatchString(g) {
		d.Goal = g
		var kept []tokens.Row
		for _, r := range rows {
			goal := "unattributed"
			if a := att[r.Session]; a.Goal != nil && *a.Goal != "" {
				goal = *a.Goal
			}
			if goal == g {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	roll := tokens.Rollup(&tokens.Snapshot{Rows: rows, Sessions: snap.Sessions}, att, orgOf)
	used := map[string]bool{}
	for _, r := range rows {
		used[r.Session] = true
	}
	sids := make([]string, 0, len(used))
	for s := range used {
		sids = append(sids, s)
	}
	sort.Strings(sids)
	for _, v := range viaKinds {
		d.Via[v] = 0
	}
	type info struct {
		role     string
		sessions []string
	}
	agentInfo := map[string]*info{}
	for _, sid := range sids {
		a, ok := att[sid]
		via := a.Via
		if !ok {
			via = "unattributed"
		}
		d.Via[via]++
		if a.Agent != nil && *a.Agent != "" {
			in := agentInfo[*a.Agent]
			if in == nil {
				in = &info{}
				if a.Role != nil {
					in.role = *a.Role
				}
				agentInfo[*a.Agent] = in
			}
			s8 := []rune(sid)
			if len(s8) > 8 {
				s8 = s8[:8]
			}
			in.sessions = append(in.sessions, string(s8))
		}
	}
	gt, err := titlesOf(ctx.DB, "goals")
	if err != nil {
		return nil, err
	}
	tt, err := titlesOf(ctx.DB, "tasks")
	if err != nil {
		return nil, err
	}
	days := make([]string, 0, len(roll.ByDay))
	for k := range roll.ByDay {
		days = append(days, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for i, k := range days {
		if i >= tokensTop {
			break
		}
		d.ByDay = append(d.ByDay, entryOf(k, roll.ByDay[k]))
	}
	d.More["day"] = max(0, len(days)-tokensTop)
	for m, e := range roll.ByModel {
		if e.CostUnknown {
			d.Unpriced = append(d.Unpriced, m)
		}
	}
	sort.Strings(d.Unpriced)
	var unpricedReqs int64
	for _, r := range rows {
		if inList(d.Unpriced, r.Model) {
			unpricedReqs += r.Requests
		}
	}
	d.Totals = entryOf("all", roll.Total)
	d.Totals.UnpricedReqs = unpricedReqs
	d.ByGoal, d.More["goal"] = topEntries(roll.ByGoal, func(e *tokEntry) { e.Title = stripTags(gt[e.Key]) })
	d.ByTask, d.More["task"] = topEntries(roll.ByTask, func(e *tokEntry) { e.Title = stripTags(tt[e.Key]) })
	d.ByAgent, d.More["agent"] = topEntries(roll.ByAgent, func(e *tokEntry) {
		if in := agentInfo[e.Key]; in != nil {
			e.Role = in.role
			e.Sessions = in.sessions
			if len(e.Sessions) > 3 {
				e.Sessions = e.Sessions[:3]
			}
			e.MoreSessions = max(0, len(in.sessions)-3)
		}
	})
	d.ByModel, d.More["model"] = topEntries(roll.ByModel, nil)
	d.ByOrg = orgEntries(roll.ByOrg, d.Cursor, d.Org)
	return d, nil
}

// orgEntries is tokens._by_org: one row per org (only the filtered one with ?org=): Claude-transcript tokens and cost,
// plus the Cursor-runtime sessions and agent-hours that have no token counts.
func orgEntries(buckets map[string]*tokens.Bucket, cursor []CursorAct, org string) []tokEntry {
	var items []tokEntry
	zero := 0.0
	for _, o := range data.Orgs {
		if org != "" && o != org {
			continue
		}
		var sess int
		var hours []float64
		for _, c := range cursor {
			if c.Org == o {
				sess += c.Sessions
				hours = append(hours, c.Hours)
			}
		}
		b, ok := buckets[o]
		if !ok && sess == 0 && len(hours) == 0 {
			continue
		}
		if !ok {
			b = &tokens.Bucket{Cost: &zero}
		}
		e := entryOf(o, b)
		e.CursorSess = sess
		e.CursorHours = roundN(pySumFloats(hours), 2)
		items = append(items, e)
	}
	return items
}

func titlesOf(c *data.Conn, table string) (map[string]string, error) {
	rows, err := c.Query("SELECT id, title FROM " + table)
	if err != nil {
		return nil, fmt.Errorf("views: read %s titles: %w", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, fmt.Errorf("views: scan %s titles: %w", table, err)
		}
		out[id] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("views: read %s titles: %w", table, err)
	}
	return out, nil
}

// ── rendering ──

func moneyOrNA(c *float64) string {
	if c == nil {
		return "n/a"
	}
	return fmt.Sprintf("$%.2f", *c)
}

func tokensHref(period, goal, model, by, org string) string {
	if !inList(tokenBy[1:], by) {
		by = ""
	}
	if !isOrg(org) {
		org = ""
	}
	return Href("/tokens", KV{"days", period}, KV{"goal", goal}, KV{"model", model}, KV{"by", by}, KV{"org", org})
}

// modelHref is tokens._model_href: a link for a model id that is plain word characters (re ^[\w.-]+$), else "".
func modelHref(period, goal, model, by, org string) string {
	s := strings.TrimSuffix(model, "\n") // $ also matches before one final newline
	if s == "" {
		return ""
	}
	for _, r := range s {
		if !(isWordRune(r) || r == '.' || r == '-') {
			return ""
		}
	}
	return tokensHref(period, goal, model, by, org)
}

func tokenControls(d *TokensData) any {
	labels := map[string]string{"1": "Today", "7": "7 days", "30": "30 days", "all": "All time"}
	var items []SegItem
	for _, p := range tokenPeriods {
		items = append(items, SegItem{labels[p], tokensHref(p, d.Goal, d.Model, d.By, d.Org), p == d.Period})
	}
	seg := Segmented("Range", items, false)
	var active []string
	if d.Goal != "" {
		active = append(active, "goal "+d.Goal)
	}
	if d.Model != "" {
		active = append(active, "model "+d.Model)
	}
	if len(active) == 0 {
		return seg
	}
	return El("div", seg, El("p", "Filtered by "+strings.Join(active, ", ")+" · ", Link(tokensHref(d.Period, "", "", d.By, d.Org), "clear"), Cls("muted")))
}

func tokenUsageSection(ctx *Ctx, d *TokensData) (Safe, error) {
	u := d.Usage
	if u == nil {
		return Section("Claude usage", nil, EmptyState("No usage data.", nil)), nil
	}
	if u.Unreadable {
		return Section("Claude usage", nil, El("p", "unreadable", Cls("stale"))), nil
	}
	cap5, err := cfgPy(ctx, "max_five_hour_pct", 92)
	if err != nil {
		return "", err
	}
	cap7, err := cfgPy(ctx, "max_seven_day_pct", 92)
	if err != nil {
		return "", err
	}
	view := UsageData{FiveHour: u.FiveHour, SevenDay: u.SevenDay, Stale: !u.Fresh, CapFive: cap5, CapSeven: cap7}
	var age *float64
	if u.Fresh {
		age = &u.AgeMin
	}
	var label any
	if u.Label != "" {
		label = El("p", u.Label, Cls("stale"))
	}
	return Section("Claude usage", nil, El("div", UsageRow(view), Cls("ins-gauges")), usageNote(nil, nil, age), label), nil
}

func costCell(e tokEntry) string {
	if e.Cost == nil {
		return "n/a"
	}
	s := moneyOrNA(e.Cost)
	if e.CostUnknown {
		s += "+"
	}
	return s
}

func tokenTiles(d *TokensData) Safe {
	t := d.Totals
	total := t.all()
	share := "-"
	if total != 0 {
		share = fmt.Sprintf("%d%%", pyRound(100*float64(t.CacheRead)/float64(total)))
	}
	var note any = ""
	if len(d.Unpriced) > 0 {
		note = El("p", fmt.Sprintf("%d requests on models without a price are not in the cost: ", t.UnpricedReqs), strings.Join(d.Unpriced, ", "), Cls("muted"))
	}
	grid := El("div",
		Tile("Total tokens", Compact(float64(total)), TileOpts{Definition: "Input, output and cache tokens (read and write) of every request in the range."}),
		Tile("Cost estimate", moneyOrNA(t.Cost), TileOpts{Definition: "Estimated API-equivalent cost at list prices as of " + tokens.PricesAsOf + ". " +
			"Requests on a model without a price are left out and listed below."}),
		Tile("Cache read share", share, TileOpts{Definition: "Cache-read tokens as a share of all tokens. A high share means " +
			"most context was served from the cache."}),
		Tile("Requests", Compact(float64(t.Requests)), TileOpts{Definition: "Model requests that carried usage counts."}),
		Cls("tiles tiles--4"))
	return El("div", grid, note)
}

func tokenDaySection(d *TokensData) Safe {
	var points []Point
	for i := len(d.ByDay) - 1; i >= 0; i-- {
		points = append(points, Point{d.ByDay[i].Key, float64(d.ByDay[i].all())})
	}
	var more any
	if n := d.More["day"]; n > 0 {
		more = El("p", fmt.Sprintf("+%d earlier days not shown", n), Cls("muted"))
	}
	return Section("By day", nil, Columns(points, "tokens", 96, "day"), more)
}

func pinLast(items []tokEntry) []tokEntry {
	var out, last []tokEntry
	for _, e := range items {
		if e.Key == "unattributed" {
			last = append(last, e)
		} else {
			out = append(out, e)
		}
	}
	return append(out, last...)
}

func notAttributed() Safe {
	return El("span", "Not attributed", " ", El("details", El("summary", "i", At("aria-label", "What is Not attributed?")),
		El("p", "Usage from sessions that could not be matched to a factory goal, task or agent. "+
			"See Attribution quality below.", Cls("tile-def")), Cls("disc--i")))
}

func tokenName(d *TokensData, by string, e tokEntry) any {
	k := e.Key
	if k == "unattributed" {
		return notAttributed()
	}
	switch by {
	case "goal":
		return El("span", Link(tokensHref(d.Period, k, d.Model, d.By, d.Org), k), " ", e.Title)
	case "task":
		return El("span", Link("/task/"+k, k), " ", e.Title)
	case "agent":
		sess := strings.Join(e.Sessions, ", ")
		if e.MoreSessions > 0 {
			sess += fmt.Sprintf(" +%d", e.MoreSessions)
		}
		var title any
		if sess != "" {
			title = "sessions: " + sess
		}
		return El("span", k, " ", El("span", e.Role, Cls("muted")), At("title", title))
	}
	if h := modelHref(d.Period, d.Goal, k, d.By, d.Org); h != "" {
		return Link(h, k)
	}
	return k
}

func hrs(h float64) string {
	if h >= 0.05 || h == 0 {
		return f1(h) + " h"
	}
	return "<0.1 h"
}

func orgBreakdown(d *TokensData, seg Safe) Safe {
	if len(d.ByOrg) == 0 {
		return Section("Breakdown", nil, seg, EmptyState("no data yet", nil))
	}
	var grand int64
	for _, e := range d.ByOrg {
		grand += e.all()
	}
	cols := []Col{{Key: "name", Label: "Org", Cls: "c-title"}, {Key: "tokens", Label: "Claude tokens", Cls: "num", PhoneLabel: true},
		{Key: "share", Label: "Share", Cls: "num", PhoneLabel: true}, {Key: "req", Label: "Requests", Cls: "num ins-wide", PhoneLabel: true},
		{Key: "cost", Label: "Cost", Cls: "num", PhoneLabel: true}, {Key: "cs", Label: "Cursor sessions", Cls: "num ins-wide", PhoneLabel: true, PhoneText: "Cursor sess."},
		{Key: "ch", Label: "Cursor agent-hours", Cls: "num", PhoneLabel: true, PhoneText: "Cursor h"}}
	var rows [][]any
	for _, e := range d.ByOrg {
		tot := e.all()
		share := "-"
		if grand != 0 {
			share = fmt.Sprintf("%.0f%%", 100*float64(tot)/float64(grand))
		}
		var cs, ch any = "-", "-"
		if e.CursorSess != 0 {
			cs, ch = e.CursorSess, hrs(e.CursorHours)
		}
		rows = append(rows, []any{OrgBadge(e.Key, ""), Compact(float64(tot)), share, e.Requests, costCell(e), cs, ch})
	}
	return Section("Breakdown", nil, seg, DataTable(cols, []Group{{Key: "all", Count: len(rows), Rows: rows}}, nil, nil, "breakdown", ""),
		El("p", "Claude tokens come from the Claude Code session logs (any agent of the org that ran on Claude). "+cursorNote, Cls("muted")))
}

func cursorSection(d *TokensData) any {
	if len(d.Cursor) == 0 && d.Org != "cursor" {
		return nil
	}
	var body any
	if len(d.Cursor) > 0 {
		var rows [][]any
		for _, e := range d.Cursor {
			rows = append(rows, []any{OrgBadge(e.Org, ""), El("span", e.Model, Cls("mono")), Td{e.Sessions, "num"}, Td{e.Live, "num"},
				Td{hrs(e.Hours), "num"}, El("span", "not reported", Cls("muted"))})
		}
		body = Table([]string{"Org", "Model", "Sessions", "Live now", "Agent-hours", "Tokens"}, rows, "table table--compact")
	} else {
		body = EmptyState("No Cursor runtime agents in this range.", nil)
	}
	return Section("Cursor runtime", nil, body, El("p", cursorNote, Cls("muted")))
}

func tokenBreakdown(d *TokensData) Safe {
	var items []SegItem
	for _, b := range tokenBy {
		items = append(items, SegItem{byLabel[b], tokensHref(d.Period, d.Goal, d.Model, b, d.Org), b == d.By})
	}
	seg := Segmented("Breakdown", items, true)
	if d.By == "org" {
		return orgBreakdown(d, seg)
	}
	var list []tokEntry
	switch d.By {
	case "goal":
		list = d.ByGoal
	case "task":
		list = d.ByTask
	case "agent":
		list = d.ByAgent
	case "model":
		list = d.ByModel
	}
	more := d.More[d.By]
	grand := d.Totals.all()
	if len(list) == 0 {
		return Section("Breakdown", nil, seg, EmptyState("no data yet", nil))
	}
	nameLbl := pyCapitalize(strings.TrimPrefix(byLabel[d.By], "By "))
	cols := []Col{{Key: "name", Label: nameLbl, Cls: "c-title"}, {Key: "tokens", Label: "Tokens", Cls: "num", PhoneLabel: true},
		{Key: "share", Label: "Share", Cls: "num", PhoneLabel: true}, {Key: "io", Label: "In / out", Cls: "num ins-wide", PhoneLabel: true, PhoneText: "In / out"},
		{Key: "req", Label: "Requests", Cls: "num ins-wide", PhoneLabel: true}, {Key: "cost", Label: "Cost", Cls: "num ins-wide", PhoneLabel: true}}
	var rows [][]any
	for _, e := range pinLast(list) {
		tot := e.all()
		pct := 0.0
		if grand != 0 {
			pct = 100 * float64(tot) / float64(grand)
		}
		ps := fmt.Sprintf("%.0f%%", pct)
		if pct < 10 {
			ps = fmt.Sprintf("%.1f%%", pct)
		}
		rows = append(rows, []any{tokenName(d, d.By, e), Compact(float64(tot)), ps, Compact(float64(e.Input)) + " / " + Compact(float64(e.Output)), e.Requests, costCell(e)})
	}
	var moreEl any
	if more > 0 {
		moreEl = El("p", fmt.Sprintf("+%d more", more), Cls("muted"))
	}
	return Section("Breakdown", nil, seg, DataTable(cols, []Group{{Key: "all", Count: len(rows), Rows: rows}}, nil, nil, "breakdown", ""), moreEl)
}

// RenderTokens is tokens.render.
func RenderTokens(ctx *Ctx, d *TokensData) (Safe, error) {
	parts := []any{tokenControls(d)}
	if d.Org != "cursor" { // the 5-hour / weekly gauges are the Claude plan's; the Cursor org's view leaves them out
		us, err := tokenUsageSection(ctx, d)
		if err != nil {
			return "", err
		}
		parts = append(parts, us)
	} else {
		parts = append(parts, nil)
	}
	parts = append(parts, cursorSection(d))
	if !d.Ready {
		parts = append(parts, Section("Token index", nil, El("p", fmt.Sprintf("indexing… %d files / %s MB read", d.Files, pyFloat(d.MBRead)), Cls("stale"))))
		return El("div", parts...), nil
	}
	if d.Org == "cursor" {
		parts = append(parts, El("p", "Below: Claude Code usage of Cursor-org agents that ran on the Claude runtime (e.g. evaluators).", Cls("muted")))
	}
	var via [][]any
	for _, v := range viaKinds {
		via = append(via, []any{v, Td{d.Via[v], "num"}})
	}
	parts = append(parts, tokenTiles(d), tokenDaySection(d), tokenBreakdown(d),
		Disclosure("Attribution quality", "attribution", nil, false, Table([]string{"Matched by", "Sessions"}, via, "table table--compact")),
		El("p", "Costs are estimates at list prices as of "+tokens.PricesAsOf+"; a trailing + means some tokens were on "+
			"models without a price.", Cls("muted")))
	return El("div", parts...), nil
}

type tokensView struct{}

// Render implements View.
func (tokensView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadTokens(ctx)
	if err != nil {
		return "", "", err
	}
	body, err := RenderTokens(ctx, d)
	if err != nil {
		return "", "", err
	}
	return titleTokens, body, nil
}
