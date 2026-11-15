package tokens

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Agent is one agents row (the columns web/tokens.py reads). An empty string is SQL NULL: Python treats None and ""
// alike everywhere these fields are used.
type Agent struct {
	Name, Role, GoalID, TaskID, SessionID, Started, Ended string
	Org, SpawnArgs                                        string
}

func ns(v sql.NullString) string { return v.String }

// agentRows is _agent_rows: every agents row ordered by started (SQLite's order for equal values is kept).
func agentRows(db *sql.DB) ([]Agent, error) {
	rows, err := db.Query("SELECT name, role, goal_id, task_id, session_id, started, ended FROM agents ORDER BY started")
	if err != nil {
		return nil, fmt.Errorf("tokens: read agents: %w", err)
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var n, r, g, t, s, st, e sql.NullString
		if err := rows.Scan(&n, &r, &g, &t, &s, &st, &e); err != nil {
			return nil, fmt.Errorf("tokens: scan agents: %w", err)
		}
		out = append(out, Agent{Name: ns(n), Role: ns(r), GoalID: ns(g), TaskID: ns(t), SessionID: ns(s), Started: ns(st), Ended: ns(e)})
	}
	return out, rows.Err()
}

// eventSessions is _event_sessions: session id -> agent name from the tick's session-change events.
func eventSessions(db *sql.DB) map[string]string {
	out := map[string]string{}
	rows, err := db.Query(`SELECT entity_id, detail FROM events WHERE entity='agent' AND (detail LIKE '%"stored"%' OR detail LIKE '%"herdr"%') ORDER BY id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, detail sql.NullString
		if rows.Scan(&id, &detail) != nil {
			continue
		}
		var d map[string]any
		if json.Unmarshal([]byte(detail.String), &d) != nil {
			continue
		}
		for _, k := range []string{"stored", "herdr"} {
			if s, ok := d[k].(string); ok {
				out[s] = id.String
			}
		}
	}
	return out
}

func mainModel(rows []Row) *string {
	tot := map[string]int64{}
	var order []string
	for _, r := range rows {
		if _, ok := tot[r.Model]; !ok {
			order = append(order, r.Model)
		}
		tot[r.Model] += r.Input + r.Output + r.CacheRead + r.CacheWrite5m + r.CacheWrite1h
	}
	if len(order) == 0 {
		return nil
	}
	best := order[0] // max() keeps the first maximum
	for _, m := range order[1:] {
		if tot[m] > tot[best] {
			best = m
		}
	}
	return &best
}

// Attr is one attribution entry; Via is session | event | name | worktree | unattributed. Nil = Python None.
type Attr struct {
	Agent     *string `json:"agent"`
	Role      *string `json:"role"`
	Task      *string `json:"task"`
	Goal      *string `json:"goal"`
	ModelMain *string `json:"model_main"`
	Via       string  `json:"via"`
}

func sp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Attribute is attribute(): session id -> attribution of every session in the snapshot.
func Attribute(snap *Snapshot, db *sql.DB) (map[string]Attr, error) {
	agents, err := agentRows(db)
	if err != nil {
		return nil, err
	}
	byName := map[string][]Agent{}
	bySess := map[string]Agent{}
	for _, a := range agents {
		byName[a.Name] = append(byName[a.Name], a)
		if a.SessionID != "" {
			bySess[a.SessionID] = a
		}
	}
	ev := eventSessions(db)
	now := Now()
	models := map[string][]Row{}
	for _, r := range snap.Rows {
		models[r.Session] = append(models[r.Session], r)
	}
	out := map[string]Attr{}
	for sid, s := range snap.Sessions {
		agent, found := bySess[sid]
		via := "session"
		if !found {
			if name, ok := ev[sid]; ok && len(byName[name]) > 0 {
				l := byName[name]
				agent, found, via = l[len(l)-1], true, "event"
			}
		}
		if !found && s.AgentName != nil && len(byName[*s.AgentName]) > 0 {
			first, fok := parseTS(str(s.FirstTS))
			for _, a := range byName[*s.AgentName] {
				start, sok := parseTS(a.Started)
				end, eok := now, true
				if a.Ended != "" {
					end, eok = parseTS(a.Ended)
				}
				if fok && sok && eok && !start.After(first) && !first.After(end) {
					agent, found, via = a, true, "name"
					break
				}
			}
		}
		info := Attr{ModelMain: mainModel(models[sid]), Via: "unattributed"}
		if found {
			info.Agent, info.Role, info.Task, info.Goal, info.Via = sp(agent.Name), sp(agent.Role), sp(agent.TaskID), sp(agent.GoalID), via
		} else {
			m := TaskRE.FindStringSubmatch(s.Project)
			if m == nil {
				m = TaskRE.FindStringSubmatch(str(s.Cwd))
			}
			if m != nil {
				info.Goal, info.Task, info.Via = sp(m[1]), sp(m[2]), "worktree"
			}
		}
		out[sid] = info
	}
	return out, nil
}

// Bucket is one rollup bucket: counters plus estimated cost. Cost nil is Python None (n/a).
type Bucket struct {
	Input        int64    `json:"input"`
	Output       int64    `json:"output"`
	CacheRead    int64    `json:"cache_read"`
	CacheWrite5m int64    `json:"cache_write_5m"`
	CacheWrite1h int64    `json:"cache_write_1h"`
	Requests     int64    `json:"requests"`
	Cost         *float64 `json:"cost"`
	CostUnknown  bool     `json:"cost_unknown"`
	Priced       int      `json:"priced"`
}

func newBucket() *Bucket { z := 0.0; return &Bucket{Cost: &z} }

// orderedBuckets is a dict of buckets that remembers insertion order (Python dict).
type orderedBuckets struct {
	keys []string
	m    map[string]*Bucket
}

func newOB() *orderedBuckets { return &orderedBuckets{m: map[string]*Bucket{}} }

func (o *orderedBuckets) add(key string, r Row, cost float64, priced bool) {
	e := o.m[key]
	if e == nil {
		e = newBucket()
		o.m[key] = e
		o.keys = append(o.keys, key)
	}
	e.Input += r.Input
	e.Output += r.Output
	e.CacheRead += r.CacheRead
	e.CacheWrite5m += r.CacheWrite5m
	e.CacheWrite1h += r.CacheWrite1h
	e.Requests += r.Requests
	if priced {
		c := *e.Cost + cost
		e.Cost = &c
		e.Priced++
	} else if r.Input != 0 || r.Output != 0 || r.CacheRead != 0 || r.CacheWrite5m != 0 || r.CacheWrite1h != 0 {
		e.CostUnknown = true // a zero-token row (e.g. model "<synthetic>") has no cost to be unknown
	}
}

// finish: a bucket with unpriced tokens and nothing priced has no cost at all: nil, not 0.
func finish(e *Bucket) {
	if e.CostUnknown && e.Priced == 0 {
		e.Cost = nil
	}
}

// RollupResult is the dict rollup() returns.
type RollupResult struct {
	ByAgent map[string]*Bucket `json:"by_agent"`
	ByTask  map[string]*Bucket `json:"by_task"`
	ByGoal  map[string]*Bucket `json:"by_goal"`
	ByModel map[string]*Bucket `json:"by_model"`
	ByDay   map[string]*Bucket `json:"by_day"`
	ByOrg   map[string]*Bucket `json:"by_org"`
	Total   *Bucket            `json:"total"`
}

func rowCost(r Row) (float64, bool) { return Cost(r.Model, r.usage(), r.speed()) }

func orDefault(p *string, d string) string {
	if p == nil || *p == "" {
		return d
	}
	return *p
}

// Rollup is rollup(): totals by agent / task / goal / model / day with tokens and estimated cost. orgOf (nil = none)
// maps a session id to its org and fills ByOrg.
func Rollup(snap *Snapshot, attr map[string]Attr, orgOf func(session string) string) *RollupResult {
	agent, task, goal, model, day, org, total := newOB(), newOB(), newOB(), newOB(), newOB(), newOB(), newOB()
	for _, r := range snap.Rows {
		a := attr[r.Session]
		c, priced := rowCost(r)
		if orgOf != nil {
			org.add(orgOf(r.Session), r, c, priced)
		}
		agent.add(orDefault(a.Agent, "unattributed"), r, c, priced)
		task.add(orDefault(a.Task, "unattributed"), r, c, priced)
		goal.add(orDefault(a.Goal, "unattributed"), r, c, priced)
		model.add(r.Model, r, c, priced)
		day.add(r.Day, r, c, priced)
		total.add("all", r, c, priced)
	}
	res := &RollupResult{ByAgent: agent.m, ByTask: task.m, ByGoal: goal.m, ByModel: model.m, ByDay: day.m, ByOrg: org.m, Total: total.m["all"]}
	if res.Total == nil {
		res.Total = newBucket()
	}
	for _, g := range []map[string]*Bucket{res.ByAgent, res.ByTask, res.ByGoal, res.ByModel, res.ByDay, res.ByOrg} {
		for _, e := range g {
			finish(e)
		}
	}
	finish(res.Total)
	return res
}

// ── per-task / per-goal usage ──

// States of a usage result.
const (
	NotReady     = "not_ready"
	NotAvailable = "not_available"
)

// UsageRow is one agent row of task_usage / goal_usage. Nil pointers are Python None.
type UsageRow struct {
	Agent      string   `json:"agent"`
	Role       *string  `json:"role"`
	Runtime    string   `json:"runtime"`
	Sessions   int      `json:"sessions"`
	WallS      *float64 `json:"wall_s"`
	Input      *int64   `json:"input"`
	Output     *int64   `json:"output"`
	CacheRead  *int64   `json:"cache_read"`
	CacheWrite *int64   `json:"cache_write"`
	Cost       *float64 `json:"cost"`
	Rework     bool     `json:"rework"`
	State      string   `json:"state"`
}

// RowTotals is the dict _totals returns.
type RowTotals struct {
	State       string   `json:"state"`
	Tokens      *int64   `json:"tokens"`
	Cost        *float64 `json:"cost"`
	ReworkCost  *float64 `json:"rework_cost"`
	ReworkShare *float64 `json:"rework_share"`
	Sessions    int      `json:"sessions"`
	CursorRows  int      `json:"cursor_rows"`
	WallS       *float64 `json:"wall_s"`
	Input       *int64   `json:"input"`
	Output      *int64   `json:"output"`
	CacheRead   *int64   `json:"cache_read"`
	CacheWrite  *int64   `json:"cache_write"`
}

// TaskUsageResult is the dict task_usage returns. Totals is nil in the not_ready state.
type TaskUsageResult struct {
	State          string     `json:"state"`
	Task           string     `json:"task"`
	Rows           []UsageRow `json:"rows"`
	Totals         *RowTotals `json:"totals"`
	FirstRejection *string    `json:"first_rejection"`
}

// Overhead is goal_usage's "overhead" entry.
type Overhead struct {
	Rows   []UsageRow `json:"rows"`
	Totals RowTotals  `json:"totals"`
}

// GoalUsageResult is the dict goal_usage returns.
type GoalUsageResult struct {
	State    string                      `json:"state"`
	Goal     string                      `json:"goal"`
	Tasks    map[string]*TaskUsageResult `json:"tasks"`
	Overhead *Overhead                   `json:"overhead"`
	Totals   *RowTotals                  `json:"totals"`
}

type agentSet struct {
	order []string
	m     map[string]*Agent
}

var orgPrefixRE = regexp.MustCompile(`^(claude|cursor)-`)

// isCursor is scout.orgs.of_agent(row) == "cursor": org column, else the name prefix, else spawn_args runtime.
func isCursor(a *Agent) bool {
	if a.Org == "claude" || a.Org == "cursor" {
		return a.Org == "cursor"
	}
	if m := orgPrefixRE.FindStringSubmatch(a.Name); m != nil {
		return m[1] == "cursor"
	}
	var sa map[string]any
	if json.Unmarshal([]byte(a.SpawnArgs), &sa) == nil {
		rt, _ := sa["runtime"].(string)
		return rt == "cursor"
	}
	return false
}

// usageAgents is _usage_agents: name -> agent row, plus org and spawn_args when the table has them.
func usageAgents(db *sql.DB) (*agentSet, error) {
	list, err := agentRows(db)
	if err != nil {
		return nil, err
	}
	set := &agentSet{m: map[string]*Agent{}}
	for _, a := range list {
		a := a
		if _, ok := set.m[a.Name]; !ok {
			set.order = append(set.order, a.Name)
		}
		set.m[a.Name] = &a
	}
	for _, q := range []string{"SELECT name, org, spawn_args FROM agents", "SELECT name, spawn_args FROM agents"} {
		rows, err := db.Query(q)
		if err != nil {
			continue // an older schema lacks the column; the prefix still decides
		}
		withOrg := q == "SELECT name, org, spawn_args FROM agents"
		for rows.Next() {
			var n, o, sa sql.NullString
			if withOrg {
				err = rows.Scan(&n, &o, &sa)
			} else {
				err = rows.Scan(&n, &sa)
			}
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("tokens: scan agents: %w", err)
			}
			if a, ok := set.m[n.String]; ok {
				a.Org, a.SpawnArgs = o.String, sa.String
			}
		}
		rows.Close()
		break
	}
	return set, nil
}

// startKey is _start_key: parsed start, a missing or unparseable one sorts last.
func startKey(a *Agent) (int, float64) {
	if a != nil && a.Started != "" {
		if t, ok := parseTS(a.Started); ok {
			return 0, float64(t.UnixNano()) / 1e9
		}
	}
	return 1, 0
}

// firstRejection is _first_rejection: the earliest fail/reject verdict or judge-reject event of the task.
func firstRejection(db *sql.DB, taskID string) (time.Time, bool) {
	var stamps []string
	collect := func(q string) bool {
		rows, err := db.Query(q, taskID)
		if err != nil {
			return false
		}
		defer rows.Close()
		for rows.Next() {
			var s sql.NullString
			if rows.Scan(&s) == nil {
				stamps = append(stamps, s.String)
			}
		}
		return rows.Err() == nil
	}
	if !collect("SELECT created FROM verdicts WHERE task_id=? AND verdict IN ('fail','reject')") ||
		!collect("SELECT ts FROM events WHERE entity='task' AND entity_id=? AND action='judge-reject'") {
		return time.Time{}, false
	}
	var best time.Time
	found := false
	for _, s := range stamps {
		if t, ok := parseTS(s); ok && (!found || t.Before(best)) {
			best, found = t, true
		}
	}
	return best, found
}

type bucketSet struct {
	by   *orderedBuckets
	sess map[string]map[string]struct{}
}

func newBucketSet() *bucketSet {
	return &bucketSet{by: newOB(), sess: map[string]map[string]struct{}{}}
}

func (b *bucketSet) add(attr Attr, r Row) {
	name := orDefault(attr.Agent, "unattributed")
	c, priced := rowCost(r)
	b.by.add(name, r, c, priced)
	if b.sess[name] == nil {
		b.sess[name] = map[string]struct{}{}
	}
	b.sess[name][r.Session] = struct{}{}
}

func (b *bucketSet) finish() {
	for _, e := range b.by.m {
		finish(e)
	}
}

func usageBuckets(snap *Snapshot, attr map[string]Attr, keep func(Attr) bool) *bucketSet {
	b := newBucketSet()
	for _, r := range snap.Rows {
		a := attr[r.Session]
		if keep(a) {
			b.add(a, r)
		}
	}
	b.finish()
	return b
}

// usageBucketsMulti is _usage_buckets_multi: one pass; task id -> buckets, and "" -> the goal's sessions without a task.
func usageBucketsMulti(snap *Snapshot, attr map[string]Attr, taskIDs map[string]bool, goalID string) map[string]*bucketSet {
	out := map[string]*bucketSet{}
	for _, r := range snap.Rows {
		a := attr[r.Session]
		var key string
		if t := str(a.Task); t != "" && taskIDs[t] {
			key = t
		} else if str(a.Goal) == goalID && str(a.Task) == "" {
			key = ""
		} else {
			continue
		}
		if out[key] == nil {
			out[key] = newBucketSet()
		}
		out[key].add(a, r)
	}
	for _, b := range out {
		b.finish()
	}
	return out
}

func wallSeconds(a *Agent, now time.Time) *float64 {
	start, ok := parseTS(a.Started)
	if a.Started == "" || !ok {
		return nil
	}
	end := now
	if a.Ended != "" {
		e, ok := parseTS(a.Ended)
		if !ok {
			return nil
		}
		end = e
	}
	s := max(0.0, end.Sub(start).Seconds())
	return &s
}

func cursorRow(a *Agent, now time.Time) UsageRow {
	return UsageRow{Agent: a.Name, Role: sp(a.Role), Runtime: "cursor", Sessions: 1, WallS: wallSeconds(a, now), State: NotAvailable}
}

func claudeRow(name string, e *Bucket, sessions int, a *Agent) UsageRow {
	in, out, cr := e.Input, e.Output, e.CacheRead
	cw := e.CacheWrite5m + e.CacheWrite1h
	row := UsageRow{Agent: name, Runtime: "claude", Sessions: sessions, Input: &in, Output: &out, CacheRead: &cr, CacheWrite: &cw, State: "ok"}
	if a != nil {
		row.Role = sp(a.Role)
	}
	if !e.CostUnknown {
		row.Cost = e.Cost
	}
	return row
}

func sumI(rows []UsageRow, f func(UsageRow) *int64) *int64 {
	var n int64
	for _, r := range rows {
		n += *f(r)
	}
	return &n
}

// totals is _totals: totals over the Claude rows; Cursor rows have no tokens or cost and are counted in cursor_rows.
func totals(rows []UsageRow) RowTotals {
	var real, cursor []UsageRow
	for _, r := range rows {
		switch r.State {
		case "ok":
			real = append(real, r)
		case NotAvailable:
			cursor = append(cursor, r)
		}
	}
	unknown := false
	for _, r := range real {
		if r.Cost == nil {
			unknown = true
		}
	}
	t := RowTotals{State: "ok", CursorRows: len(cursor)}
	for _, r := range rows {
		t.Sessions += r.Sessions
	}
	var total, rework *float64
	if !(unknown || (len(real) == 0 && len(cursor) > 0)) {
		cs := make([]float64, 0, len(real))
		for _, r := range real {
			cs = append(cs, *r.Cost)
		}
		s := pySum(cs...)
		total = &s
	}
	if !(unknown || len(real) == 0) {
		var cs []float64
		for _, r := range real {
			if r.Rework {
				cs = append(cs, *r.Cost)
			}
		}
		s := pySum(cs...)
		rework = &s
	}
	t.Cost, t.ReworkCost = total, rework
	if total != nil && *total != 0 && rework != nil {
		sh := *rework / *total
		t.ReworkShare = &sh
	}
	if len(real) > 0 {
		in := sumI(real, func(r UsageRow) *int64 { return r.Input })
		out := sumI(real, func(r UsageRow) *int64 { return r.Output })
		cr := sumI(real, func(r UsageRow) *int64 { return r.CacheRead })
		cw := sumI(real, func(r UsageRow) *int64 { return r.CacheWrite })
		tok := *in + *out + *cr + *cw
		t.Input, t.Output, t.CacheRead, t.CacheWrite, t.Tokens = in, out, cr, cw, &tok
	}
	switch {
	case len(real) > 0 && len(cursor) == 0:
		t.State = "ok"
	case len(real) > 0:
		t.State = "partial"
	case len(cursor) > 0:
		t.State = NotAvailable
	}
	if len(cursor) > 0 {
		w := 0.0
		for _, r := range cursor {
			if r.WallS != nil {
				w += *r.WallS
			}
		}
		t.WallS = &w
	}
	return t
}

// TaskUsage is task_usage: per-agent usage of one task. A snapshot that is not ready gives state "not_ready" and no rows.
// now zero = the current time.
func TaskUsage(snap *Snapshot, attr map[string]Attr, db *sql.DB, taskID string, now time.Time) (*TaskUsageResult, error) {
	if !snap.Ready {
		return &TaskUsageResult{State: NotReady, Task: taskID, Rows: []UsageRow{}}, nil
	}
	if now.IsZero() {
		now = Now()
	}
	agents, err := usageAgents(db)
	if err != nil {
		return nil, err
	}
	b := usageBuckets(snap, attr, func(a Attr) bool { return str(a.Task) == taskID })
	return taskUsage(taskID, agents, b, db, now), nil
}

func taskUsage(taskID string, agents *agentSet, b *bucketSet, db *sql.DB, now time.Time) *TaskUsageResult {
	rej, hasRej := firstRejection(db, taskID)
	rows := []UsageRow{}
	for _, name := range b.by.keys {
		a := agents.m[name]
		if a != nil && isCursor(a) {
			continue
		}
		rows = append(rows, claudeRow(name, b.by.m[name], len(b.sess[name]), a))
	}
	for _, name := range agents.order {
		if a := agents.m[name]; a.TaskID == taskID && isCursor(a) {
			rows = append(rows, cursorRow(a, now))
		}
	}
	for i := range rows {
		a := agents.m[rows[i].Agent]
		if a != nil && a.Started != "" && hasRej && rows[i].State == "ok" {
			if start, ok := parseTS(a.Started); ok && start.After(rej) {
				rows[i].Rework = true
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ki, vi := startKey(agents.m[rows[i].Agent])
		kj, vj := startKey(agents.m[rows[j].Agent])
		if ki != kj {
			return ki < kj
		}
		return vi < vj
	})
	t := totals(rows)
	res := &TaskUsageResult{State: "ok", Task: taskID, Rows: rows, Totals: &t}
	if hasRej {
		s := isoformat(rej)
		res.FirstRejection = &s
	}
	return res
}

// GoalUsage is goal_usage: a goal's tasks (TaskUsage each) plus "goal overhead" (sessions attributed to the goal
// without a task, and the goal's Cursor agents without a task); totals = tasks + overhead.
func GoalUsage(snap *Snapshot, attr map[string]Attr, db *sql.DB, goalID string, now time.Time) (*GoalUsageResult, error) {
	if !snap.Ready {
		return &GoalUsageResult{State: NotReady, Goal: goalID, Tasks: map[string]*TaskUsageResult{}}, nil
	}
	if now.IsZero() {
		now = Now()
	}
	agents, err := usageAgents(db)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, a := range attr {
		if str(a.Goal) == goalID && str(a.Task) != "" {
			ids[*a.Task] = true
		}
	}
	for _, a := range agents.m {
		if a.GoalID == goalID && a.TaskID != "" {
			ids[a.TaskID] = true
		}
	}
	buckets := usageBucketsMulti(snap, attr, ids, goalID)
	sorted := make([]string, 0, len(ids))
	for t := range ids {
		sorted = append(sorted, t)
	}
	sort.Strings(sorted)
	tasks := map[string]*TaskUsageResult{}
	for _, t := range sorted {
		b := buckets[t]
		if b == nil {
			b = newBucketSet()
		}
		tasks[t] = taskUsage(t, agents, b, db, now)
	}
	over := []UsageRow{}
	if b := buckets[""]; b != nil {
		for _, name := range b.by.keys {
			a := agents.m[name]
			if a != nil && isCursor(a) {
				continue
			}
			over = append(over, claudeRow(name, b.by.m[name], len(b.sess[name]), a))
		}
	}
	for _, name := range agents.order {
		if a := agents.m[name]; a.GoalID == goalID && a.TaskID == "" && isCursor(a) {
			over = append(over, cursorRow(a, now))
		}
	}
	all := append([]UsageRow{}, over...)
	for _, t := range sorted { // Python: for t in tasks.values() (sorted ids)
		all = append(all, tasks[t].Rows...)
	}
	ot, tt := totals(over), totals(all)
	return &GoalUsageResult{State: "ok", Goal: goalID, Tasks: tasks, Overhead: &Overhead{Rows: over, Totals: ot}, Totals: &tt}, nil
}
