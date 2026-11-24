package views

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"factory/go/internal/web/data"
)

// Agents page constants (web/views/agents.py).
const (
	titleAgents  = "Agents"
	scopeMax     = 120 // SCOPE_MAX: teams.scope is a director-written summary; never the full text
	summaryMax   = 200 // SUMMARY_MAX
	endedWindowS = 24 * 3600
	endedMax     = 40
	matchMax     = 200
	agentTitleMx = 120 // TITLE_MAX
	goalChipsMax = 6   // GOAL_CHIPS
	blockedKey   = "blocked"
	idChunk      = 500
)

var (
	agentRoles    = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}
	agentRuntimes = []string{"claude", "cursor"}
	agentGoalRe   = regexp.MustCompile(`^G-\p{Nd}+\n?\z`) // ^G-\d+$ with Python's Unicode \d and a $ that matches before a final newline
	endedStatuses = []string{"done", "exited", "lost", "stopped"}
	workerRoles   = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}
	chipGroups    = []struct {
		Label    string
		Statuses []string
	}{{"Running", []string{"live"}}, {"Queued", []string{"queued"}}, {"Ended", []string{"exited", "stopped"}}, {"Blocked", []string{blockedKey}},
		{"Starting", []string{"starting"}}, {"Done", []string{"done"}}, {"Lost", []string{"lost"}}, {"Cancelled", []string{"cancelled"}}}
	chipAlways = map[string]bool{"Running": true, "Queued": true, "Ended": true, "Blocked": true}
)

// agentFilters is parse_filters' result: the valid values per key, in the order given.
type agentFilters struct {
	Status, Role, Goal, Runtime []string
}

func (f agentFilters) empty() bool {
	return len(f.Status)+len(f.Role)+len(f.Goal)+len(f.Runtime) == 0
}

// pairs lists the non-empty filters in Python's dict order (status, role, goal, runtime).
func (f agentFilters) pairs() [][2]any {
	var out [][2]any
	for _, p := range []struct {
		k string
		v []string
	}{{"status", f.Status}, {"role", f.Role}, {"goal", f.Goal}, {"runtime", f.Runtime}} {
		if len(p.v) > 0 {
			out = append(out, [2]any{p.k, p.v})
		}
	}
	return out
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// parseAgentFilters is agents.parse_filters: ?status=, ?role=, ?goal=, ?runtime= as comma-separated lists; invalid
// values are dropped, order and first occurrence are kept. statuses are the status values present in the agents table.
func parseAgentFilters(query map[string]string, statuses []string) agentFilters {
	known := func(v string) bool { return v == blockedKey || inList(statuses, v) }
	parse := func(key string, ok func(string) bool) []string {
		var vals []string
		for _, v := range strings.Split(query[key], ",") {
			v = pyStrip(v)
			if v != "" && ok(v) && !inList(vals, v) {
				vals = append(vals, v)
			}
		}
		return vals
	}
	return agentFilters{
		Status:  parse("status", known),
		Role:    parse("role", func(v string) bool { return inList(agentRoles, v) }),
		Goal:    parse("goal", agentGoalRe.MatchString),
		Runtime: parse("runtime", func(v string) bool { return inList(agentRuntimes, v) }),
	}
}

// blockedSince is agents.blocked_since: nil = NULL; the raw DB value otherwise.
type blockedSince struct {
	Set   bool
	Text  string  // the value as text when it was TEXT or a blob
	Num   float64 // the value when it was a number
	IsNum bool
}

// isBlocked is agents.is_blocked: the one definition of blocked (not NULL and not "").
func (b blockedSince) isBlocked() bool { return b.Set && (b.IsNum || b.Text != "") }

// hhmm is agents._hhmm: HH:MM (local) of an epoch, or "" when it cannot be parsed.
func (b blockedSince) hhmm() string {
	f := b.Num
	if !b.IsNum {
		v, ok := pyFloatText(b.Text)
		if !ok {
			return ""
		}
		f = v
	}
	if f != f || f < -62135596800 || f > 253402300799 { // NaN, inf or outside datetime's year 1..9999
		return ""
	}
	sec := int64(f)
	if float64(sec) > f {
		sec-- // floor
	}
	return time.Unix(sec, 0).In(data.Loc).Format("15:04")
}

// agentRec is one agent of the page: queries.agents plus the fields agents.data adds.
type agentRec struct {
	data.Agent
	Blocked       blockedSince
	Summary       string
	StopRequested bool
	Reason        *string
	LastEvent     *string
	TaskTitle     *string
}

// teamRec is a team with its live worker count and lead state.
type teamRec struct {
	data.Team
	ScopeShort  string
	LiveWorkers int
	LeadState   string
}

// AgentsData is what the Agents page shows (web/views/agents.py data()).
type AgentsData struct {
	Org, Cap            string
	CapSet              bool
	Orgs, LiveByOrg     map[string]int
	RuntimeMixed        bool
	Counts              map[string]int
	Blocked             int
	Roles, Runtimes     []countKV
	Goals               []countKV
	Filters             agentFilters
	Matches             []agentRec
	MatchesTotal        int
	Teams               []teamRec
	Live, Queued, Ended []agentRec
	EndedTotal          int
}

type countKV struct {
	K string
	N int
}

// lastEvents is agents._last_events: {agent name: ts of its newest event}.
func lastEvents(c *data.Conn, names []string) (map[string]string, error) {
	sort.Strings(names)
	out := map[string]string{}
	for i := 0; i < len(names); i += idChunk {
		part := names[i:min(i+idChunk, len(names))]
		rows, err := c.Query("SELECT actor, MAX(ts) FROM events WHERE actor IN ("+strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+") GROUP BY actor", anyArgs(part)...)
		if err != nil {
			return nil, fmt.Errorf("views: last events: %w", err)
		}
		for rows.Next() {
			var a string
			var ts sql.NullString
			if err := rows.Scan(&a, &ts); err != nil {
				rows.Close()
				return nil, fmt.Errorf("views: scan last events: %w", err)
			}
			if ts.Valid {
				out[a] = ts.String
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("views: last events: %w", err)
		}
	}
	return out, nil
}

func anyArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// taskTitles is agents._task_titles: {task id: title cut to TITLE_MAX}.
func taskTitles(c *data.Conn, ids []string) (map[string]string, error) {
	sort.Strings(ids)
	out := map[string]string{}
	for i := 0; i < len(ids); i += idChunk {
		part := ids[i:min(i+idChunk, len(ids))]
		rows, err := c.Query("SELECT id, title FROM tasks WHERE id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+")", anyArgs(part)...)
		if err != nil {
			return nil, fmt.Errorf("views: task titles: %w", err)
		}
		for rows.Next() {
			var id, t string
			if err := rows.Scan(&id, &t); err != nil {
				rows.Close()
				return nil, fmt.Errorf("views: scan task titles: %w", err)
			}
			out[id] = trunc(t, agentTitleMx)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("views: task titles: %w", err)
		}
	}
	return out, nil
}

// agentRecs is agents._agent_rows: every agent with blocked_since and the (cut) summary of an ended one.
func agentRecs(ctx *Ctx) ([]agentRec, error) {
	base, err := ctx.DB.Agents(true, ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: agents: %w", err)
	}
	blocked := map[string]blockedSince{}
	if has, err := ctx.DB.HasColumn("agents", "blocked_since"); err != nil {
		return nil, fmt.Errorf("views: agents columns: %w", err)
	} else if has {
		rows, err := ctx.DB.Query("SELECT name, blocked_since FROM agents WHERE blocked_since IS NOT NULL")
		if err != nil {
			return nil, fmt.Errorf("views: blocked agents: %w", err)
		}
		for rows.Next() {
			var name string
			var v any
			if err := rows.Scan(&name, &v); err != nil {
				rows.Close()
				return nil, fmt.Errorf("views: scan blocked agents: %w", err)
			}
			b := blockedSince{Set: true}
			switch x := v.(type) {
			case float64:
				b.Num, b.IsNum = x, true
			case int64:
				b.Num, b.IsNum = float64(x), true
			case string:
				b.Text = x
			case []byte:
				b.Text = string(x)
			}
			blocked[name] = b
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("views: blocked agents: %w", err)
		}
	}
	summ := map[string]string{}
	if has, err := ctx.DB.HasColumn("agents", "summary"); err != nil {
		return nil, fmt.Errorf("views: agents columns: %w", err)
	} else if has {
		rows, err := ctx.DB.Query("SELECT name, substr(summary,1,?) FROM agents WHERE status IN ('done','exited','lost','stopped')", summaryMax+1)
		if err != nil {
			return nil, fmt.Errorf("views: agent summaries: %w", err)
		}
		for rows.Next() {
			var name string
			var s sql.NullString
			if err := rows.Scan(&name, &s); err != nil {
				rows.Close()
				return nil, fmt.Errorf("views: scan agent summaries: %w", err)
			}
			if s.Valid {
				summ[name] = trunc(s.String, summaryMax)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("views: agent summaries: %w", err)
		}
	}
	out := make([]agentRec, len(base))
	for i, a := range base {
		out[i] = agentRec{Agent: a, Blocked: blocked[a.Name], Summary: summ[a.Name]}
	}
	return out, nil
}

func parkedTeamIDs(ctx *Ctx) (map[string]bool, error) {
	out := map[string]bool{}
	has, err := ctx.DB.HasColumn("teams", "parked_at")
	if err != nil {
		return nil, fmt.Errorf("views: teams columns: %w", err)
	}
	if !has {
		return out, nil
	}
	rows, err := ctx.DB.Query("SELECT id FROM teams WHERE status='active' AND parked_at IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("views: parked teams: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("views: scan parked teams: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("views: parked teams: %w", err)
	}
	return out, nil
}

// LeadCounts is agents.lead_counts: workers live, leads live, parked teams (the rules of lead_counts in bin/factory).
func LeadCounts(c *Ctx) (workers, leads, parked int, err error) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(workerRoles)), ",")
	if err = c.DB.QueryRow("SELECT count(*) FROM agents WHERE status='live' AND role IN ("+ph+")", anyArgs(workerRoles)...).Scan(&workers); err != nil {
		return 0, 0, 0, fmt.Errorf("views: count live workers: %w", err)
	}
	if err = c.DB.QueryRow("SELECT count(*) FROM agents WHERE status='live' AND role='lead'").Scan(&leads); err != nil {
		return 0, 0, 0, fmt.Errorf("views: count live leads: %w", err)
	}
	p, err := parkedTeamIDs(c)
	if err != nil {
		return 0, 0, 0, err
	}
	return workers, leads, len(p), nil
}

func leadState(t data.Team, status map[string]string, parked map[string]bool) string {
	s := ""
	if t.LeadAgent != nil && *t.LeadAgent != "" {
		s = status[*t.LeadAgent]
	}
	switch s {
	case "live", "starting":
		return "live"
	case "queued":
		return "queued"
	}
	if parked[t.ID] {
		return "parked"
	}
	return "-"
}

func epochOrZero(p *string) float64 {
	if p == nil {
		return 0
	}
	return epochOf(*p)
}

func recRuntime(a agentRec) string {
	if a.Runtime == "" {
		return "claude"
	}
	return a.Runtime
}

// LoadAgents gathers the data of the Agents page (web/views/agents.py data()).
func LoadAgents(ctx *Ctx) (*AgentsData, error) {
	org := ctx.org()
	d := &AgentsData{Org: org, Counts: map[string]int{}, Orgs: map[string]int{}, LiveByOrg: map[string]int{}}
	rows, err := agentRecs(ctx)
	if err != nil {
		return nil, err
	}
	var statuses []string
	for _, a := range rows { // the unfiltered counts (the filter's known statuses)
		if d.Counts[a.Status] == 0 {
			statuses = append(statuses, a.Status)
		}
		d.Counts[a.Status]++
	}
	d.Filters = parseAgentFilters(ctx.Query, statuses)
	for _, o := range data.Orgs {
		d.Orgs[o], d.LiveByOrg[o] = 0, 0
	}
	for _, a := range rows {
		d.Orgs[a.Org]++
		if a.Status == "live" {
			d.LiveByOrg[a.Org]++
		}
	}
	if org != "" { // the global org filter narrows every list and count below
		var kept []agentRec
		d.Counts = map[string]int{}
		for _, a := range rows {
			if a.Org == org {
				kept = append(kept, a)
				d.Counts[a.Status]++
			}
		}
		rows = kept
	}
	allRows := rows
	if !d.Filters.empty() {
		var kept []agentRec
		for _, a := range rows {
			if agentMatches(a, d.Filters) {
				kept = append(kept, a)
			}
		}
		rows = kept
	}
	var live, queued, ended []agentRec
	for _, a := range rows {
		switch a.Status {
		case "live":
			a.StopRequested = a.StopRequestedAt != nil && *a.StopRequestedAt != ""
			live = append(live, a)
		case "queued":
			queued = append(queued, a)
		}
	}
	res, err := ctx.DB.ResourcesDB(nil)
	if err != nil {
		return nil, fmt.Errorf("views: resources: %w", err)
	}
	reasons := map[string]*string{}
	for _, q := range res.Queued {
		reasons[q.Name] = q.Reason
	}
	for i := range queued {
		queued[i].Reason = reasons[queued[i].Name]
	}
	cut := float64(ctx.Now.Unix()) + float64(ctx.Now.Nanosecond())/1e9 - endedWindowS
	for _, a := range rows {
		if inList(endedStatuses, a.Status) && epochOrZero(a.Ended) >= cut {
			ended = append(ended, a)
		}
	}
	sort.SliceStable(ended, func(i, j int) bool { return epochOrZero(ended[i].Ended) > epochOrZero(ended[j].Ended) })
	nLive := map[string]int{}
	for _, a := range live {
		if a.TeamID != nil {
			nLive[*a.TeamID]++
		}
	}
	leadStatus := map[string]string{}
	lrows, err := ctx.DB.Query("SELECT name, status FROM agents WHERE role='lead'")
	if err != nil {
		return nil, fmt.Errorf("views: lead agents: %w", err)
	}
	for lrows.Next() {
		var n, s string
		if err := lrows.Scan(&n, &s); err != nil {
			lrows.Close()
			return nil, fmt.Errorf("views: scan lead agents: %w", err)
		}
		leadStatus[n] = s
	}
	err = lrows.Err()
	lrows.Close()
	if err != nil {
		return nil, fmt.Errorf("views: lead agents: %w", err)
	}
	parked, err := parkedTeamIDs(ctx)
	if err != nil {
		return nil, err
	}
	teams, err := ctx.DB.Teams()
	if err != nil {
		return nil, fmt.Errorf("views: teams: %w", err)
	}
	for _, t := range teams {
		if org != "" && t.Org != org {
			continue
		}
		short := ""
		if t.Scope != nil {
			short = trunc(*t.Scope, scopeMax)
		}
		d.Teams = append(d.Teams, teamRec{Team: t, ScopeShort: short, LiveWorkers: nLive[t.ID], LeadState: leadState(t, leadStatus, parked)})
	}
	byRole, byGoal, byRuntime := map[string]int{}, map[string]int{}, map[string]int{}
	for _, a := range allRows {
		byRole[a.Role]++
		byRuntime[recRuntime(a)]++
		if a.GoalID != nil && agentGoalRe.MatchString(*a.GoalID) {
			byGoal[*a.GoalID]++
		}
		if a.Blocked.isBlocked() {
			d.Blocked++
		}
		if recRuntime(a) != data.OrgRuntime(a.Org) {
			d.RuntimeMixed = true
		}
	}
	for _, r := range agentRoles {
		if n, ok := byRole[r]; ok {
			d.Roles = append(d.Roles, countKV{r, n})
		}
	}
	for _, r := range agentRuntimes {
		if n, ok := byRuntime[r]; ok {
			d.Runtimes = append(d.Runtimes, countKV{r, n})
		}
	}
	for g, n := range byGoal {
		d.Goals = append(d.Goals, countKV{g, n})
	}
	sort.Slice(d.Goals, func(i, j int) bool { return d.Goals[i].K < d.Goals[j].K })
	cfgRows, err := ctx.DB.Config()
	if err != nil {
		return nil, fmt.Errorf("views: config: %w", err)
	}
	for _, r := range cfgRows {
		if r.Key == "max_live_agents" {
			d.Cap, d.CapSet = r.Value, r.Value != ""
		}
	}
	if !d.CapSet {
		found := false
		for _, r := range cfgRows {
			found = found || r.Key == "max_live_agents"
		}
		if !found {
			for _, df := range ctx.Defaults {
				if df.Key == "max_live_agents" {
					d.Cap, d.CapSet = df.Value.String(), df.Value.V != 0
				}
			}
		}
	}
	// a filtered page lists the first MATCH_MAX matches by name; both lists show the last event and task title
	var matches []agentRec
	if !d.Filters.empty() {
		matches = append(matches, rows...)
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
		if len(matches) > matchMax {
			matches = matches[:matchMax]
		}
	}
	names, tids := map[string]bool{}, map[string]bool{}
	for _, list := range [][]agentRec{live, matches} {
		for _, a := range list {
			names[a.Name] = true
			if a.TaskID != nil && *a.TaskID != "" {
				tids[*a.TaskID] = true
			}
		}
	}
	last, err := lastEvents(ctx.DB, keysOf(names))
	if err != nil {
		return nil, err
	}
	titles, err := taskTitles(ctx.DB, keysOf(tids))
	if err != nil {
		return nil, err
	}
	decorate := func(list []agentRec) {
		for i := range list {
			a := &list[i]
			if ts, ok := last[a.Name]; ok {
				t := ts
				a.LastEvent = &t
			}
			if a.TaskID != nil {
				if t, ok := titles[*a.TaskID]; ok {
					a.TaskTitle = &t
				}
			}
		}
	}
	decorate(live)
	decorate(matches)
	d.Live, d.Queued, d.Matches = live, queued, matches
	if len(ended) > endedMax {
		d.Ended = ended[:endedMax]
	} else {
		d.Ended = ended
	}
	d.EndedTotal = len(ended)
	if !d.Filters.empty() {
		d.MatchesTotal = len(rows)
	}
	// matches never show the stop-requested badge (agents.keep has no stop_requested for them)
	for i := range d.Matches {
		d.Matches[i].StopRequested = false
	}
	return d, nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func agentMatches(a agentRec, f agentFilters) bool {
	if len(f.Status) > 0 && !(inList(f.Status, a.Status) || (inList(f.Status, blockedKey) && a.Blocked.isBlocked())) {
		return false
	}
	if len(f.Role) > 0 && !inList(f.Role, a.Role) {
		return false
	}
	if len(f.Runtime) > 0 && !inList(f.Runtime, recRuntime(a)) {
		return false
	}
	if len(f.Goal) > 0 {
		g := ""
		if a.GoalID != nil {
			g = *a.GoalID
		}
		return inList(f.Goal, g)
	}
	return true
}

// agentsHref is agents._href: the /agents URL for filters f with change applied (an empty value removes the key);
// the global ?org= filter is kept.
func agentsHref(f agentFilters, org string, change map[string][]string) string {
	cur := map[string][]string{"status": f.Status, "role": f.Role, "goal": f.Goal, "runtime": f.Runtime}
	for k, v := range change {
		cur[k] = v
	}
	var parts []string
	for _, k := range []string{"status", "role", "goal", "runtime"} {
		if len(cur[k]) == 0 {
			continue
		}
		q := make([]string, len(cur[k]))
		for i, v := range cur[k] {
			q[i] = strings.ReplaceAll(pyQuote(v), ",", "%2C") // quote(v, safe="")
		}
		parts = append(parts, k+"="+strings.Join(q, "%2C"))
	}
	if isOrg(org) {
		parts = append(parts, "org="+org)
	}
	if len(parts) == 0 {
		return "/agents"
	}
	return "/agents?" + strings.Join(parts, "&")
}

func orgRank(org string) int {
	for i, o := range data.Orgs {
		if o == org {
			return i
		}
	}
	return len(data.Orgs)
}

func statEl(s string) any {
	if s != "" && s != "-" {
		return Status(s, false)
	}
	return ""
}

func (a agentRec) row() AgentRow {
	r := AgentRow{Name: a.Name, Role: a.Role, Status: a.Status, Runtime: a.Runtime, Org: a.Org, TaskID: deref(a.TaskID),
		TaskTitle: deref(a.TaskTitle), LastEvent: deref(a.LastEvent), Blocked: a.Blocked.isBlocked()}
	return r
}

func blockedBadge(a agentRec) any {
	if !a.Blocked.isBlocked() {
		return ""
	}
	if t := a.Blocked.hhmm(); t != "" {
		return Badge("blocked since "+t, "bad")
	}
	return Badge("blocked (time unknown)", "bad")
}

var agentCols = []Col{AgentCols[0], AgentCols[1], AgentCols[2], {Key: "model", Label: "Model", Cls: "c-who", PhoneLabel: true}, AgentCols[3]}

// agentCells is agents._cells: the agent_row cells plus the model; the blocked and stop-requested badges sit next to
// the name.
func agentCells(a agentRec, now time.Time) []any {
	c := AgentRowCells(a.row(), now)
	extra := []any{" ", blockedBadge(a)}
	if a.StopRequested {
		extra = append(extra, " ", Badge("stop requested", "warn"))
	}
	return []any{c[0], El("span", append([]any{c[1]}, extra...)...), c[2], deref(a.Model), c[3]}
}

// labelGroup is one DataTable group whose header label may carry markup (a Safe).
type labelGroup struct {
	Key   string
	Label any
	Count int
	Rows  [][]any
}

// dataTableLabelled is DataTable for open groups whose label is markup: the group header keeps it unescaped.
func dataTableLabelled(cols []Col, groups []labelGroup, empty string) Safe {
	var gs []labelGroup
	for _, g := range groups {
		if len(g.Rows) > 0 {
			gs = append(gs, g)
		}
	}
	if len(gs) == 0 {
		return EmptyState(empty, nil)
	}
	var ths []any
	for _, c := range cols {
		ths = append(ths, thEl(c, nil, func(string) string { return "#" }))
	}
	var bodies []any
	for _, g := range gs {
		var rows []any
		if g.Label != nil && g.Label != "" {
			rows = append(rows, El("tr", El("th", g.Label, " ", El("span", g.Count, Cls("grp-n")), At("colspan", len(cols)), At("scope", "colgroup")), Cls("grp")))
		}
		for _, r := range g.Rows {
			var tds []any
			for i, c := range cols {
				var cell any
				if i < len(r) {
					cell = r[i]
				}
				var cls any
				if c.Cls != "" {
					cls = c.Cls
				}
				tds = append(tds, El("td", labelled(c, cell), At("class", cls)))
			}
			rows = append(rows, El("tr", tds))
		}
		bodies = append(bodies, El("tbody", rows))
	}
	cls := "dt-wrap"
	for _, c := range cols {
		if c.PhoneLabel {
			cls = "dt-wrap dt-wrap--cards"
		}
	}
	return El("div", El("table", El("thead", El("tr", ths)), bodies, Cls("dt")), Cls(cls))
}

func agentTeams(d *AgentsData) Safe {
	cols := []Col{{Key: "team", Label: "Team", Cls: "c-id mono"}, {Key: "org", Label: "Org", Cls: "c-who orgrow"}, {Key: "goal", Label: "Goal", Cls: "c-who"},
		{Key: "lead", Label: "Lead", Cls: "c-title"}, {Key: "status", Label: "Status", Cls: "c-who"}, {Key: "state", Label: "Lead state", Cls: "c-who"},
		{Key: "live", Label: "Live", Cls: "c-age num"}}
	teams := append([]teamRec(nil), d.Teams...)
	sort.SliceStable(teams, func(i, j int) bool {
		a, b := teams[i], teams[j]
		if ra, rb := orgRank(a.Org), orgRank(b.Org); ra != rb {
			return ra < rb
		}
		if na, nb := a.Status != "active", b.Status != "active"; na != nb {
			return nb
		}
		return a.ID < b.ID
	})
	var rows [][]any
	for _, t := range teams {
		var goal any = ""
		if t.GoalID != "" {
			goal = Link("/goal/"+t.GoalID, t.GoalID)
		}
		rows = append(rows, []any{t.ID, OrgBadge(t.Org, ""), goal, deref(t.LeadAgent), statEl(t.Status), statEl(t.LeadState), t.LiveWorkers})
	}
	var count any
	if len(rows) > 0 {
		count = len(rows)
	}
	return Section("Teams", count, DataTable(cols, []Group{{Key: "t", Rows: rows, Count: len(rows)}}, nil, nil, "teams", "No data yet."))
}

func agentRunning(d *AgentsData, now time.Time) Safe {
	teams := map[string]teamRec{}
	for _, t := range d.Teams {
		teams[t.ID] = t
	}
	orgOf := func(a agentRec) string { return AgentOrg(a.row()) }
	seen := map[string]bool{}
	for _, a := range d.Live {
		seen[orgOf(a)] = true
	}
	many := len(seen) > 1
	live := append([]agentRec(nil), d.Live...)
	urgent := func(a agentRec) int {
		if a.Blocked.isBlocked() || a.Status == "lost" {
			return 0
		}
		return 1
	}
	sort.SliceStable(live, func(i, j int) bool {
		a, b := live[i], live[j]
		if ra, rb := orgRank(orgOf(a)), orgRank(orgOf(b)); ra != rb {
			return ra < rb
		}
		if ha, hb := a.TeamID != nil, b.TeamID != nil; ha != hb {
			return hb
		}
		if ta, tb := deref(a.TeamID), deref(b.TeamID); ta != tb {
			return ta < tb
		}
		if ua, ub := urgent(a), urgent(b); ua != ub {
			return ua < ub
		}
		return a.Name < b.Name
	})
	var order []string
	groups := map[string]*labelGroup{}
	for _, a := range live {
		o := orgOf(a)
		tid := deref(a.TeamID)
		key := o + "\x00" + tid
		g := groups[key]
		if g == nil {
			t, ok := teams[tid]
			var text string
			switch {
			case ok && t.GoalID != "":
				text = tid + " · " + t.GoalID
			case tid != "":
				text = tid
			default:
				text = "HQ (no team)"
			}
			var label any = text
			if many {
				label = Safe(string(OrgBadge(o, "")) + " " + string(El("span", text)))
			}
			id := tid
			if id == "" {
				id = "none"
			}
			g = &labelGroup{Key: o + "-" + id, Label: label}
			groups[key] = g
			order = append(order, key)
		}
		g.Rows = append(g.Rows, agentCells(a, now))
		g.Count++
	}
	var gl []labelGroup
	for _, k := range order {
		gl = append(gl, *groups[k])
	}
	return Section("Running", len(d.Live), dataTableLabelled(agentCols, gl, "No live agents."))
}

func roleSpan(a agentRec) any {
	if a.Role != a.Name {
		return El("span", a.Role, Cls("agent-role"))
	}
	return nil
}

func taskLink(a agentRec) any {
	if a.TaskID != nil && *a.TaskID != "" {
		return Link("/task/"+*a.TaskID, *a.TaskID)
	}
	return ""
}

func agentQueued(d *AgentsData, now time.Time) Safe {
	cols := []Col{AgentCols[0], AgentCols[1], {Key: "org", Label: "Org", Cls: "c-who orgrow"}, AgentCols[2],
		{Key: "since", Label: "Queued", Cls: "c-age num", PhoneLabel: true}, {Key: "why", Label: "Reason", Cls: "c-who"}}
	var rows [][]any
	for _, a := range d.Queued {
		var why any = ""
		if a.Reason != nil && *a.Reason != "" {
			why = "Waiting: " + *a.Reason
		}
		rows = append(rows, []any{RowStatus("queued"), El("span", a.Name, " ", roleSpan(a)), OrgBadge(AgentOrg(a.row()), a.Runtime), taskLink(a),
			Ago(deref(a.QueuedAt), now, ""), why})
	}
	return Section("Queued", len(d.Queued), DataTable(cols, []Group{{Key: "q", Rows: rows, Count: len(rows)}}, nil, nil, "agents-queued", "Nothing is queued."))
}

func agentEnded(d *AgentsData, now time.Time) Safe {
	cols := []Col{AgentCols[0], AgentCols[1], {Key: "org", Label: "Org", Cls: "c-who orgrow"}, AgentCols[2],
		{Key: "ran", Label: "Ran", Cls: "c-age num", PhoneLabel: true, PhoneText: "Ended"}, {Key: "sum", Label: "Summary", Cls: "c-who"}}
	var rows [][]any
	for _, a := range d.Ended {
		rows = append(rows, []any{RowStatus(a.Status), El("span", a.Name, " ", roleSpan(a)), OrgBadge(AgentOrg(a.row()), a.Runtime), taskLink(a),
			Ago(deref(a.Ended), now, ""), a.Summary})
	}
	nAll := d.Counts["exited"] + d.Counts["stopped"] // the number of the Ended chip
	note := El("p", fmt.Sprintf("Showing the %d that ended in the last 24 h.", d.EndedTotal), Cls("muted"))
	var body Safe
	if len(rows) > 0 {
		body = Disclosure("Ended", "agents-ended", nAll, false, note, DataTable(cols, []Group{{Key: "e", Rows: rows, Count: len(rows)}}, nil, nil, "agents-ended-t", ""))
	} else {
		body = EmptyState("No agents ended in the last 24 h.", nil)
	}
	return El("section", body, Cls("sect"))
}

func agentChips(d *AgentsData) []any {
	f, org := d.Filters, d.Org
	cur := f.Status
	var items []Chip
	for _, g := range chipGroups {
		n := 0
		if len(g.Statuses) == 1 && g.Statuses[0] == blockedKey {
			n = d.Blocked
		} else {
			for _, s := range g.Statuses {
				n += d.Counts[s]
			}
		}
		if n > 0 || chipAlways[g.Label] {
			on := sameSet(cur, g.Statuses)
			var change []string
			if !on {
				change = g.Statuses
			}
			items = append(items, Chip{g.Label, N(n), agentsHref(f, org, map[string][]string{"status": change}), on})
		}
	}
	main := Chips("Status", items)
	total := 0
	for _, n := range d.Orgs {
		total += n
	}
	orgItems := []Chip{{"All", N(total), agentsHref(f, "", nil), org == ""}}
	for _, o := range data.Orgs {
		next := o
		if org == o {
			next = ""
		}
		orgItems = append(orgItems, Chip{OrgLabel(o), N(d.Orgs[o]), agentsHref(f, next, nil), org == o})
	}
	rest := []any{Chips("Org", orgItems)}
	facet := func(key string, vals []string, n int, name string) Chip {
		on := len(vals) == 1 && vals[0] == name
		var change []string
		if !on {
			change = []string{name}
		}
		return Chip{name, N(n), agentsHref(f, org, map[string][]string{key: change}), on}
	}
	var roleItems []Chip
	for _, r := range d.Roles {
		roleItems = append(roleItems, facet("role", f.Role, r.N, r.K))
	}
	rest = append(rest, Chips("Role", roleItems))
	var rts []Chip
	for _, r := range d.Runtimes {
		rts = append(rts, facet("runtime", f.Runtime, r.N, r.K))
	}
	if len(rts) > 0 && (d.RuntimeMixed || len(f.Runtime) > 0) {
		rest = append(rest, Chips("Runtime", rts))
	}
	goalRow := func(gs []countKV) []Chip {
		var out []Chip
		for _, g := range gs {
			out = append(out, facet("goal", f.Goal, g.N, g.K))
		}
		return out
	}
	first, more := d.Goals, []countKV(nil)
	if len(first) > goalChipsMax {
		first, more = d.Goals[:goalChipsMax], d.Goals[goalChipsMax:]
	}
	rest = append(rest, Chips("Goal", goalRow(first)))
	if len(more) > 0 {
		rest = append(rest, Disclosure(fmt.Sprintf("%d more goals", len(more)), "agents-goals", nil, false, Chips("More goals", goalRow(more))))
	}
	active := 0
	if org != "" {
		active++
	}
	for _, v := range [][]string{f.Role, f.Runtime, f.Goal} {
		if len(v) > 0 {
			active++
		}
	}
	out := []any{FilterBar("agents-filters", active, []any{main}, rest...)}
	if !f.empty() {
		var shown []string
		for _, p := range f.pairs() {
			shown = append(shown, fmt.Sprintf("%s=%s", p[0], strings.Join(p[1].([]string), ",")))
		}
		out = append(out, El("p", "Showing: "+strings.Join(shown, ", ")+" · ", Link(agentsHref(agentFilters{}, org, nil), "clear"), Cls("muted")))
	}
	return out
}

func sameSet(a, b []string) bool {
	sa, sb := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		sa[x] = true
	}
	for _, x := range b {
		sb[x] = true
	}
	if len(sa) != len(sb) {
		return false
	}
	for x := range sa {
		if !sb[x] {
			return false
		}
	}
	return true
}

func agentSummary(d *AgentsData) Safe {
	n := d.Counts
	parts := []string{fmt.Sprintf("%d running", n["live"])}
	if d.CapSet {
		parts = append(parts, "cap "+d.Cap)
	}
	parts = append(parts, fmt.Sprintf("%d queued", n["queued"]))
	if d.Blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", d.Blocked))
	}
	running := 0
	for _, v := range d.LiveByOrg {
		if v > 0 {
			running++
		}
	}
	if d.Org != "" {
		parts[0] = OrgLabel(d.Org) + " org: " + parts[0]
	} else if running > 1 {
		var bits []string
		for _, o := range data.Orgs {
			bits = append(bits, fmt.Sprintf("%d %s", d.LiveByOrg[o], OrgLabel(o)))
		}
		parts = append(parts[:1], append([]string{"(" + strings.Join(bits, " · ") + ")"}, parts[1:]...)...)
	}
	over := false
	if d.CapSet && allUnicodeDigits(d.Cap) {
		if c, ok := pyIntText(d.Cap); ok {
			over = int64(n["live"]) >= c
		}
	}
	info := El("details", El("summary", "(i)", At("aria-label", "About these states")),
		El("p", "Busy/idle is not tracked; last event age is shown. The chip is the agent's org (Claude or Cursor); "+
			"the org implies the runtime, and a runtime chip is added only when an agent runs on another one "+
			"(docs/runtimes.md).", Cls("muted")), Cls("disc disc--i"), At("data-key", "agents-info"))
	cls := "summary-line"
	if over {
		cls = "summary-line summary-line--warn"
	}
	return El("div", El("p", strings.Join(parts, " · "), Cls(cls)), info, Cls("summary"))
}

// allUnicodeDigits is str.isdigit() restricted to decimal digits (a superscript digit passes isdigit but fails int()).
func allUnicodeDigits(s string) bool {
	if s == "" {
		return false
	}
	_, ok := pyIntText(s)
	if !ok {
		return false
	}
	for _, r := range s {
		if r == '_' || r == '+' || r == '-' || pyIsSpace(r) {
			return false
		}
	}
	return true
}

func agentMatching(d *AgentsData, now time.Time) Safe {
	var rows [][]any
	for _, a := range d.Matches {
		rows = append(rows, agentCells(a, now))
	}
	return Section("Matching agents", d.MatchesTotal, DataTable(agentCols, []Group{{Key: "m", Rows: rows, Count: len(rows)}}, nil, nil, "agents-match", "No agents match these filters."))
}

// RenderAgents is agents.render.
func RenderAgents(ctx *Ctx, d *AgentsData) Safe {
	head := append([]any{agentSummary(d)}, agentChips(d)...)
	if !d.Filters.empty() {
		return El("div", head, agentMatching(d, ctx.Now), agentTeams(d))
	}
	return El("div", head, agentRunning(d, ctx.Now), agentQueued(d, ctx.Now), agentTeams(d), agentEnded(d, ctx.Now))
}

type agentsView struct{}

// Render implements View.
func (agentsView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadAgents(ctx)
	if err != nil {
		return "", "", err
	}
	return titleAgents, RenderAgents(ctx, d), nil
}
