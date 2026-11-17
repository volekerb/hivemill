package views

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/web/data"
)

// Resources page constants (web/views/resources.py).
const (
	titleResources = "Resources"
	resDays        = 7  // DAYS
	resPageSize    = 40 // PAGE_SIZE
	reasonMaxRes   = 200
)

var (
	mixHQRoles = []string{"boss", "director", "staff-engineer"}
	beforeRe   = regexp.MustCompile(`^([0-9]{1,12})-([01])-([0-9]{1,12})\z`)
	// eventPairs is the sorted _EVENTS set of (entity, action) pairs the timeline shows.
	eventPairs = [][2]string{{"agent", "done"}, {"agent", "lost"}, {"agent", "queued"}, {"agent", "reaped"}, {"agent", "spawn"}, {"agent", "stopped"},
		{"config", "set"}, {"task", "judge-reject"}, {"task", "merged"}, {"team", "close"}, {"team", "eval"}, {"team", "new"}}
	timelineKnd = map[string]string{"spawn": "info", "queued": "warn", "done": "ok", "reaped": "muted", "stopped": "muted", "lost": "bad",
		"set": "info", "new": "info", "close": "muted", "eval": "info", "merged": "ok", "judge-reject": "bad",
		"decision": "info", "escalation": "bad", "alert": "bad", "report": "muted"}
)

// pyNumText prints a number like Python's str(): a whole value as an integer (the helpers return ints), else the
// shortest round-trip form.
func pyNumText(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// cfgPy is resources._num over cfg.get(k, defaults.get(k)): the config value, else the default, parsed like Python's
// float(); anything that does not parse is the fallback.
func cfgPy(ctx *Ctx, key string, fallback float64) (float64, error) {
	rows, err := ctx.DB.Config()
	if err != nil {
		return 0, fmt.Errorf("views: read config: %w", err)
	}
	for _, r := range rows {
		if r.Key == key {
			if f, ok := pyFloatText(r.Value); ok {
				return f, nil
			}
			return fallback, nil
		}
	}
	for _, d := range ctx.Defaults {
		if d.Key == key {
			return d.Value.V, nil
		}
	}
	return fallback, nil
}

// pyIntOf is int(x) of a float: truncation, an error where Python raises (nan, inf).
func pyIntOf(f float64, what string) (int, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("views: %s is %v: set a finite number", what, f)
	}
	return int(f), nil
}

type orgMix struct {
	Org, Label                       string
	Live, Workers, Leads, HQ, Queued int
	Runtimes                         map[string]int
}

// gateRow is one row of the gate checks: label, value, limit, state (ok | over | unknown) and the word.
type gateRow struct{ Label, Value, Limit, State, Word string }

// Gate is resources.derive_gate: the spawn gate as derived from the database, /proc and the injected helpers.
type Gate struct {
	Open      bool
	Reason    string
	Rows      []gateRow
	Orgs      []orgMix
	Live, Cap int
	Queued    int
}

func pctText(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", *v)
}

// orgMixOf is resources.org_mix: the live agent mix per org, every org listed.
func orgMixOf(ctx *Ctx) ([]orgMix, error) {
	ag, err := ctx.DB.Agents(false, ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: agents: %w", err)
	}
	var out []orgMix
	idx := map[string]int{}
	for _, o := range data.Orgs {
		idx[o] = len(out)
		out = append(out, orgMix{Org: o, Label: OrgLabel(o), Runtimes: map[string]int{}})
	}
	for _, a := range ag {
		i, ok := idx[a.Org]
		if !ok {
			continue
		}
		m := &out[i]
		if a.Status == "queued" {
			m.Queued++
		}
		if a.Status != "live" {
			continue
		}
		m.Live++
		switch {
		case a.Role == "lead":
			m.Leads++
		case inList(mixHQRoles, a.Role):
			m.HQ++
		default:
			m.Workers++
		}
		m.Runtimes[a.Runtime]++
	}
	return out, nil
}

// DeriveGate is resources.derive_gate.
func DeriveGate(ctx *Ctx) (*Gate, error) {
	var err error
	var capF, totalCapF, floorF, h5, d7, leadsCapF float64
	for _, p := range []struct {
		dst *float64
		key string
		fb  float64
	}{{&capF, "max_live_agents", 6}, {&totalCapF, "max_total_claude", 10}, {&floorF, "min_mem_available_mb", 1024}, {&h5, "max_five_hour_pct", 92},
		{&d7, "max_seven_day_pct", 92}, {&leadsCapF, "max_live_leads", 4}} {
		if *p.dst, err = cfgPy(ctx, p.key, p.fb); err != nil {
			return nil, err
		}
	}
	var capN, totalCap, floor, leadsCap int
	for _, p := range []struct {
		dst  *int
		f    float64
		what string
	}{{&capN, capF, "max_live_agents"}, {&totalCap, totalCapF, "max_total_claude"}, {&floor, floorF, "min_mem_available_mb"}, {&leadsCap, leadsCapF, "max_live_leads"}} {
		if *p.dst, err = pyIntOf(p.f, p.what); err != nil {
			return nil, err
		}
	}
	var live, queued int
	if err := ctx.DB.QueryRow("SELECT count(*) FROM agents WHERE status='live'").Scan(&live); err != nil {
		return nil, fmt.Errorf("views: count live agents: %w", err)
	}
	mix, err := orgMixOf(ctx)
	if err != nil {
		return nil, err
	}
	total, haveTotal := ClaudeTotal(ProcSnapshot())
	workers, leads, _, err := LeadCounts(ctx)
	if err != nil {
		return nil, err
	}
	var mem *float64
	if ctx.Mem != nil {
		mem = ctx.Mem()
	}
	var u *data.Usage
	if ctx.Usage != nil {
		u = ctx.Usage()
	}
	var rows []gateRow
	add := func(label, value, limit, state, word string) {
		rows = append(rows, gateRow{label, value, limit, state, word})
	}
	vs := func(label string, value, c int, overWord string) gateRow {
		st, w := "ok", "OK"
		if value >= c {
			st = "over"
		}
		if value > c {
			w = overWord
		} else if value == c {
			w = "At cap"
		}
		return gateRow{label, strconv.Itoa(value), fmt.Sprintf("cap %d", c), st, w}
	}
	rows = append(rows, vs("Agents", live, capN, "Over cap"))
	add("Workers", strconv.Itoa(workers), "-", "ok", "OK")
	lead := vs("Leads", leads, leadsCap, "Over cap")
	lead.State = "ok" // at the lead cap is a limit reached, not a breach
	if leads > leadsCap {
		lead.State = "over"
	}
	rows = append(rows, lead)
	if !haveTotal {
		add("Claude processes", "unknown", fmt.Sprintf("cap %d", totalCap), "unknown", "Unknown")
	} else {
		rows = append(rows, vs("Claude processes", total, totalCap, "Over cap"))
	}
	if mem == nil {
		add("Memory available", "unknown", fmt.Sprintf("floor %d MB", floor), "unknown", "Unknown")
	} else {
		st, w := "ok", "OK"
		if *mem < float64(floor) {
			st, w = "over", "Low"
		}
		add("Memory available", pyNumText(*mem)+" MB", fmt.Sprintf("floor %d MB", floor), st, w)
	}
	capText := "cap " + fmtG(h5) + "% · " + fmtG(d7) + "%"
	if u == nil || u.Unreadable {
		v := "unknown"
		if u != nil {
			v = "unreadable"
		}
		add("Claude usage", v, capText, "unknown", "Unknown")
	} else {
		hot := (u.FiveHour != nil && *u.FiveHour >= h5) || (u.SevenDay != nil && *u.SevenDay >= d7)
		fresh := u.Fresh && u.AgeMin <= 30 // the gauge's stale rule
		word, st := "Stale", "unknown"
		if fresh {
			word, st = "OK", "ok"
			if hot {
				word, st = "Over cap", "over"
			}
		}
		add("Claude usage", pctText(u.FiveHour)+" 5-hour · "+pctText(u.SevenDay)+" weekly", capText, st, word)
	}
	reason := ""
	usageOK := u != nil && u.Fresh && !u.Unreadable
	switch {
	case live >= capN:
		reason = fmt.Sprintf("%d/%d agent panes live", live, capN)
	case haveTotal && total >= totalCap:
		reason = fmt.Sprintf("%d/%d claude processes on host", total, totalCap)
	case mem != nil && *mem < float64(floor):
		reason = fmt.Sprintf("MemAvailable %sMB < floor %dMB", pyNumText(*mem), floor)
	case usageOK && u.FiveHour != nil && *u.FiveHour >= h5:
		reason = fmt.Sprintf("5h usage %.0f%% ≥ %s%%", *u.FiveHour, fmtG(h5))
	case usageOK && u.SevenDay != nil && *u.SevenDay >= d7:
		reason = fmt.Sprintf("weekly usage %.0f%% ≥ %s%%", *u.SevenDay, fmtG(d7))
	}
	if err := ctx.DB.QueryRow("SELECT count(*) FROM agents WHERE status='queued'").Scan(&queued); err != nil {
		return nil, fmt.Errorf("views: count queued agents: %w", err)
	}
	return &Gate{Open: reason == "", Reason: reason, Rows: rows, Orgs: mix, Live: live, Cap: capN, Queued: queued}, nil
}

type timelineItem struct {
	ID    int64
	Src   int
	TS    string
	Epoch int64
	Kind  string
	Text  string
}

type configRow struct{ Key, Value, Source string }

type dayRow struct {
	Day                                 string
	Spawns, Queued, Merges, Escalations int
}

// ResourcesData is what the Resources page shows (web/views/resources.py data()).
type ResourcesData struct {
	Gate      *Gate
	System    *SystemData
	Usage     *data.Usage
	Config    []configRow
	Items     []timelineItem
	NextBack  string
	PerDay    []dayRow
	Before    string // the raw ?before= when it parses
	HasBefore bool
}

func pyStrOrNone(s sql.NullString) string {
	if !s.Valid {
		return "None"
	}
	return s.String
}

// eventText is resources._event_text; entity_id and detail may be NULL (Python prints None).
func eventText(entity string, entityID sql.NullString, action string, detail sql.NullString) string {
	switch entity {
	case "agent":
		base := pyStrOrNone(entityID) + " " + action
		if action == "queued" && detail.Valid && detail.String != "" {
			return base + ": " + trunc(detail.String, reasonMaxRes)
		}
		return base
	case "config":
		d := "None"
		if detail.Valid {
			d = trunc(detail.String, 60)
		}
		return "config " + pyStrOrNone(entityID) + " = " + d
	case "team":
		s := "team " + pyStrOrNone(entityID) + " " + action
		if action == "eval" && detail.Valid && detail.String != "" {
			s += " " + trunc(detail.String, 10)
		}
		return s
	case "task":
		return "task " + pyStrOrNone(entityID) + " " + action
	}
	return entity + " " + action
}

func tsecOf(t time.Time) float64 { return float64(t.Unix()) + float64(t.Nanosecond())/1e9 }

// timeline is resources.timeline: events (allowlisted actions) and message metadata of the last 7 days, newest first,
// one page; both sources form one stream ordered by (epoch, src, id) and before is the key of the last item of the
// previous page.
func timeline(c *data.Conn, now time.Time, before *[3]int64) ([]timelineItem, string, error) {
	cut := tsecOf(now) - resDays*86400
	low := time.Unix(int64(math.Floor(cut-86400)), 0).In(data.Loc).Format("2006-01-02T15:04:05-07:00")
	var items []timelineItem
	conds := make([]string, len(eventPairs))
	args := []any{low}
	for i, p := range eventPairs {
		conds[i] = "(entity=? AND action=?)"
		args = append(args, p[0], p[1])
	}
	rows, err := c.Query("SELECT id,ts,actor,entity,entity_id,action,detail FROM events WHERE ts >= ? AND ("+strings.Join(conds, " OR ")+" OR entity='digest') ORDER BY id DESC", args...)
	if err != nil {
		return nil, "", fmt.Errorf("views: timeline events: %w", err)
	}
	for rows.Next() {
		var id int64
		var ts, action string
		var actor, entity, eid, detail sql.NullString
		if err := rows.Scan(&id, &ts, &actor, &entity, &eid, &action, &detail); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("views: scan timeline events: %w", err)
		}
		e := 0.0
		if t, ok := data.ParseTS(ts); ok {
			if e = tsecOf(t); e < cut {
				continue
			}
		}
		kind := action
		if entity.String == "digest" {
			kind = "digest"
		}
		items = append(items, timelineItem{ID: id, Src: 1, TS: ts, Epoch: int64(e), Kind: kind, Text: eventText(entity.String, eid, action, detail)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", fmt.Errorf("views: timeline events: %w", err)
	}
	mrows, err := c.Query("SELECT id, sender, recipient, kind, substr(subject,1,?) subject, created FROM messages "+
		"WHERE created >= ? AND kind IN ('decision','escalation','alert','report') ORDER BY id DESC", data.SubjectMax, low)
	if err != nil {
		return nil, "", fmt.Errorf("views: timeline messages: %w", err)
	}
	for mrows.Next() {
		var id int64
		var sender, recipient, subject sql.NullString
		var kind, created string
		if err := mrows.Scan(&id, &sender, &recipient, &kind, &subject, &created); err != nil {
			mrows.Close()
			return nil, "", fmt.Errorf("views: scan timeline messages: %w", err)
		}
		t, ok := data.ParseTS(created)
		if !ok || tsecOf(t) < cut {
			continue
		}
		items = append(items, timelineItem{ID: id, Src: 0, TS: created, Epoch: int64(tsecOf(t)), Kind: kind,
			Text: pyStrOrNone(sender) + " → " + pyStrOrNone(recipient) + ": " + pyStrOrNone(subject)})
	}
	err = mrows.Err()
	mrows.Close()
	if err != nil {
		return nil, "", fmt.Errorf("views: timeline messages: %w", err)
	}
	sort.SliceStable(items, func(i, j int) bool { // newest first: descending (epoch, src, id)
		a, b := items[i], items[j]
		if a.Epoch != b.Epoch {
			return a.Epoch > b.Epoch
		}
		if a.Src != b.Src {
			return a.Src > b.Src
		}
		return a.ID > b.ID
	})
	if before != nil {
		var kept []timelineItem
		for _, i := range items {
			switch {
			case i.Epoch != before[0]:
				if i.Epoch < before[0] {
					kept = append(kept, i)
				}
			case int64(i.Src) != before[1]:
				if int64(i.Src) < before[1] {
					kept = append(kept, i)
				}
			case i.ID < before[2]:
				kept = append(kept, i)
			}
		}
		items = kept
	}
	next := ""
	if len(items) > resPageSize {
		items = items[:resPageSize]
		last := items[len(items)-1]
		next = fmt.Sprintf("%d-%d-%d", last.Epoch, last.Src, last.ID)
	}
	return items, next, nil
}

// perDay is resources.per_day: spawns, queued, merges and escalations of the last seven local days.
func perDay(c *data.Conn, now time.Time) ([]dayRow, error) {
	cut := tsecOf(now) - resDays*86400
	var out []dayRow
	idx := map[string]int{}
	for i := 0; i < resDays; i++ {
		d := now.Add(-time.Duration(i) * 24 * time.Hour).Format("2006-01-02")
		idx[d] = len(out)
		out = append(out, dayRow{Day: d})
	}
	bump := func(iso string, f func(*dayRow)) {
		t, ok := data.ParseTS(iso)
		if !ok || tsecOf(t) < cut {
			return
		}
		if i, ok := idx[t.In(data.Loc).Format("2006-01-02")]; ok {
			f(&out[i])
		}
	}
	rows, err := c.Query("SELECT ts,action FROM events WHERE (entity='agent' AND action IN ('spawn','queued')) OR (entity='task' AND action='merged') ORDER BY id DESC LIMIT 5000")
	if err != nil {
		return nil, fmt.Errorf("views: per-day events: %w", err)
	}
	for rows.Next() {
		var ts, action string
		if err := rows.Scan(&ts, &action); err != nil {
			rows.Close()
			return nil, fmt.Errorf("views: scan per-day events: %w", err)
		}
		switch action {
		case "spawn":
			bump(ts, func(r *dayRow) { r.Spawns++ })
		case "queued":
			bump(ts, func(r *dayRow) { r.Queued++ })
		case "merged":
			bump(ts, func(r *dayRow) { r.Merges++ })
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("views: per-day events: %w", err)
	}
	mrows, err := c.Query("SELECT created FROM messages WHERE kind='escalation' ORDER BY id DESC LIMIT 2000")
	if err != nil {
		return nil, fmt.Errorf("views: per-day messages: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var created string
		if err := mrows.Scan(&created); err != nil {
			return nil, fmt.Errorf("views: scan per-day messages: %w", err)
		}
		bump(created, func(r *dayRow) { r.Escalations++ })
	}
	if err := mrows.Err(); err != nil {
		return nil, fmt.Errorf("views: per-day messages: %w", err)
	}
	return out, nil
}

// parseBefore is resources._before: the timeline cursor "<epoch>-<src>-<id>" (ASCII digits only); anything else is
// ignored.
func parseBefore(v string) *[3]int64 {
	m := beforeRe.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	var out [3]int64
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseInt(m[i+1], 10, 64)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return &out
}

// LoadResources gathers the data of the Resources page.
func LoadResources(ctx *Ctx) (*ResourcesData, error) {
	d := &ResourcesData{}
	before := parseBefore(ctx.Query["before"])
	cfg, err := ctx.DB.Config()
	if err != nil {
		return nil, fmt.Errorf("views: config: %w", err)
	}
	have := map[string]bool{}
	for _, r := range cfg {
		have[r.Key] = true
		d.Config = append(d.Config, configRow{r.Key, r.Value, "config"})
	}
	defs := append([]data.Default(nil), ctx.Defaults...)
	sort.SliceStable(defs, func(i, j int) bool { return defs[i].Key < defs[j].Key })
	for _, df := range defs {
		if !have[df.Key] {
			d.Config = append(d.Config, configRow{df.Key, df.Value.String(), "default"})
		}
	}
	if d.Gate, err = DeriveGate(ctx); err != nil {
		return nil, err
	}
	ag, err := ctx.DB.Agents(false, ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: agents: %w", err)
	}
	if d.System, err = system(ctx, ag); err != nil {
		return nil, err
	}
	if ctx.Usage != nil {
		d.Usage = ctx.Usage()
	}
	if d.Items, d.NextBack, err = timeline(ctx.DB, ctx.Now, before); err != nil {
		return nil, err
	}
	if d.PerDay, err = perDay(ctx.DB, ctx.Now); err != nil {
		return nil, err
	}
	if before != nil {
		d.Before, d.HasBefore = ctx.Query["before"], true
	}
	return d, nil
}

func gateSection(g *Gate) Safe {
	key, word, tone := "done", "Gate open", "success"
	if !g.Open {
		key, word, tone = "blocked", "Gate closed", "danger"
	}
	sentence := fmt.Sprintf("%d agents running, cap %d; %d queued", g.Live, g.Cap, g.Queued)
	if g.Reason != "" {
		sentence += ". Closed because: " + g.Reason
	}
	return Section("Spawn gate", nil, El("p", El("span", Glyph(key), word, Cls("st st--pill tone-"+tone)), " ", sentence),
		El("p", "Derived from the database and /proc (no live pane check), so it can differ from the real gate for a moment.", Cls("muted")))
}

func orgsSection(g *Gate, org string) any {
	var rows [][]any
	for _, m := range g.Orgs {
		if org != "" && m.Org != org {
			continue
		}
		var rts []string
		for rt := range m.Runtimes {
			rts = append(rts, rt)
		}
		sort.Strings(rts)
		var bits []string
		for _, rt := range rts {
			bits = append(bits, fmt.Sprintf("%s %d", rt, m.Runtimes[rt]))
		}
		rt := strings.Join(bits, " · ")
		if rt == "" {
			rt = "-"
		}
		rows = append(rows, []any{OrgBadge(m.Org, ""), m.Live, m.Workers, m.Leads, m.HQ, m.Queued, rt})
	}
	if len(rows) == 0 {
		return nil
	}
	cols := []Col{{Key: "org", Label: "Org", Cls: "c-title"}, {Key: "live", Label: "Live", Cls: "num", PhoneLabel: true},
		{Key: "workers", Label: "Workers", Cls: "num", PhoneLabel: true}, {Key: "leads", Label: "Leads", Cls: "num", PhoneLabel: true},
		{Key: "hq", Label: "HQ", Cls: "num", PhoneLabel: true}, {Key: "queued", Label: "Queued", Cls: "num", PhoneLabel: true},
		{Key: "runtime", Label: "Runtime", PhoneLabel: true}}
	body := DataTable(cols, []Group{{Key: "all", Count: len(rows), Rows: rows}}, nil, nil, "orgs", "")
	return Section("Agents by org", nil, body, El("p", "The agent cap, memory and load are shared by both orgs.", Cls("muted")))
}

// whenText is resources._when: "10-28 14:00" in local time for an epoch, "" when there is none.
func whenText(epoch *int64) string {
	if epoch == nil || *epoch < -62135596800 || *epoch > 253402300799 {
		return ""
	}
	return time.Unix(*epoch, 0).In(data.Loc).Format("01-02 15:04")
}

// usageNote is resources.usage_note: reset times and data age under the Claude usage gauge, "" when there is none.
// The reset epochs and the age are nil when not known.
func usageNote(fiveReset, sevenReset *int64, ageMin *float64) any {
	var bits []string
	if w := whenText(fiveReset); w != "" {
		bits = append(bits, "5-hour resets "+w)
	}
	if w := whenText(sevenReset); w != "" {
		bits = append(bits, "weekly resets "+w)
	}
	if ageMin != nil {
		bits = append(bits, "data "+pyNumText(*ageMin)+" min old")
	}
	if len(bits) == 0 {
		return ""
	}
	return El("p", strings.Join(bits, " · "), Cls("muted"))
}

func gaugesSection(d *ResourcesData) Safe {
	s := d.System
	rows := GaugeRows(s)
	rows = append(rows, UsageRow(s.Usage), CursorRow(s.Cursor))
	var age *float64
	if u := d.Usage; u != nil && !u.Unreadable && (u.FiveHour != nil || u.SevenDay != nil) {
		a := u.AgeMin
		age = &a
	}
	return Section("Capacity", nil, El("div", rows, Cls("ins-gauges")), usageNote(nil, nil, age))
}

func checksSection(g *Gate) Safe {
	glyphs := map[string]string{"ok": "done", "over": "blocked", "unknown": "queued"}
	intro := El("p", "Gate inputs (read-only snapshot). The Capacity gauges above are the live view.", Cls("muted"))
	var items []any
	for _, r := range g.Rows {
		cls := "ins-check"
		if r.Word == "Over cap" {
			cls = "ins-check ins-check--over"
		}
		items = append(items, El("li", Glyph(glyphs[r.State]), El("span", r.Label, Cls("ins-name")), El("span", r.Value, Cls("mono")),
			El("span", r.Limit, Cls("muted")), El("b", r.Word, Cls("gz")), Cls(cls)))
	}
	return Section("Checks", nil, intro, El("ul", items, Cls("lrow-list")))
}

func daysSection(days []dayRow) Safe {
	nonzero := false
	for _, d := range days {
		nonzero = nonzero || d.Spawns+d.Queued+d.Merges+d.Escalations != 0
	}
	if !nonzero {
		return Section("Per day", nil, EmptyState("No data yet.", nil))
	}
	var rows [][]any
	for _, d := range days {
		rows = append(rows, []any{d.Day[5:], Td{d.Spawns, "num"}, Td{d.Queued, "num"}, Td{d.Merges, "num"}, Td{d.Escalations, "num"}})
	}
	return Section("Per day", nil, El("p", "Numbers align under each column (day labels stay left).", Cls("muted only-phone")),
		Table([]string{"Day", "Spawns", "Queued", "Merges", "Escalations"}, rows, "table table--compact per-day-grid"))
}

func configDisclosure(rows []configRow) Safe {
	if len(rows) == 0 {
		return Disclosure("Caps and config", "config", nil, false, EmptyState("No data yet.", nil))
	}
	var trs [][]any
	for _, r := range rows {
		trs = append(trs, []any{El("span", pyCapitalize(strings.ReplaceAll(r.Key, "_", " ")), " ", El("span", r.Key, Cls("mono ins-key"))),
			Td{El("span", r.Value, Cls("mono")), "num"}, r.Source})
	}
	return Disclosure("Caps and config", "config", len(rows), false, Table([]string{"Setting", "Value", "Source"}, trs, "table table--compact"))
}

func eventsDisclosure(d *ResourcesData) Safe {
	if len(d.Items) == 0 {
		return Disclosure("7-day events", "events", nil, d.HasBefore, EmptyState("No data yet.", nil))
	}
	var items []any
	for _, i := range d.Items {
		kind, ok := timelineKnd[i.Kind]
		if !ok {
			kind = "muted"
		}
		items = append(items, El("div", Badge(i.Kind, kind), " ", Ts(i.TS), " ", i.Text, Cls("timeline-item")))
	}
	var nav []any
	if d.NextBack != "" {
		nav = append(nav, Link("/resources?before="+d.NextBack, "More"))
	}
	if d.HasBefore {
		nav = append(nav, " ", Link("/resources", "Newest"))
	}
	var navEl any = ""
	if len(nav) > 0 {
		navEl = El("p", nav...)
	}
	// a "More" page is reached from inside the disclosure, so it opens there; the first page stays collapsed
	return Disclosure("7-day events", "events", len(d.Items), d.HasBefore, El("div", append(items, Cls("timeline"))...), navEl)
}

// RenderResources is resources.render.
func RenderResources(ctx *Ctx, d *ResourcesData) Safe {
	return El("div", gateSection(d.Gate), gaugesSection(d), orgsSection(d.Gate, ctx.org()), checksSection(d.Gate), daysSection(d.PerDay),
		eventsDisclosure(d), configDisclosure(d.Config))
}

type resourcesView struct{}

// Render implements View.
func (resourcesView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadResources(ctx)
	if err != nil {
		return "", "", err
	}
	return titleResources, RenderResources(ctx, d), nil
}
