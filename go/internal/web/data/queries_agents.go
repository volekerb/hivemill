package data

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Team is a row of queries.teams(); Org is the org of the team's goal.
type Team struct {
	ID        string  `json:"id"`
	GoalID    string  `json:"goal_id"`
	Scope     *string `json:"scope"`
	LeadAgent *string `json:"lead_agent"`
	Status    string  `json:"status"`
	Created   string  `json:"created"`
	Org       string  `json:"org"`
}

// Teams is queries.teams.
func (c *Conn) Teams() ([]Team, error) {
	gorgs, err := c.GoalOrgs()
	if err != nil {
		return nil, err
	}
	out := []Team{}
	err = c.each("SELECT id, goal_id, scope, lead_agent, status, created FROM teams ORDER BY id", nil, func(r rowScanner) error {
		var t Team
		var scope, lead sql.NullString
		if err := r.Scan(&t.ID, &t.GoalID, &scope, &lead, &t.Status, &t.Created); err != nil {
			return err
		}
		t.Scope, t.LeadAgent = nsp(scope), nsp(lead)
		t.Org = OrgOfGoal(gorgs, t.GoalID)
		out = append(out, t)
		return nil
	})
	return out, err
}

func nsp(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// Agent is a row of queries.agents(): spawn_args is never returned, only the derived runtime.
type Agent struct {
	Name            string  `json:"name"`
	Role            string  `json:"role"`
	GoalID          *string `json:"goal_id"`
	TeamID          *string `json:"team_id"`
	TaskID          *string `json:"task_id"`
	Status          string  `json:"status"`
	Model           *string `json:"model"`
	PaneID          *string `json:"pane_id"`
	Started         *string `json:"started"`
	Ended           *string `json:"ended"`
	QueuedAt        *string `json:"queued_at"`
	SessionID       *string `json:"session_id"`
	StopRequestedAt *string `json:"stop_requested_at"`
	Org             string  `json:"org"`
	Runtime         string  `json:"runtime"`
	AgeS            *int64  `json:"age_s"`
}

func spawnRuntime(spawnArgs sql.NullString) string {
	if !spawnArgs.Valid || spawnArgs.String == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(spawnArgs.String), &v); err != nil {
		return ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if rt, _ := m["runtime"].(string); rt == "claude" || rt == "cursor" {
		return rt
	}
	return ""
}

// AgentRuntimeOf is queries.agent_runtime_of: claude|cursor (default claude), from spawn_args.runtime only.
func AgentRuntimeOf(spawnArgs string) string {
	if rt := spawnRuntime(sql.NullString{String: spawnArgs, Valid: spawnArgs != ""}); rt != "" {
		return rt
	}
	return "claude"
}

// Agents is queries.agents: agents with age_s (live or queued: since started/queued_at until now; ended: started to ended).
func (c *Conn) Agents(includeEnded bool, now time.Time) ([]Agent, error) {
	want := []string{"name", "role", "goal_id", "team_id", "task_id", "status", "model", "pane_id", "started", "ended",
		"queued_at", "session_id", "stop_requested_at"}
	var sel []string
	for _, col := range want {
		has, err := c.HasColumn("agents", col)
		if err != nil {
			return nil, err
		}
		if has {
			sel = append(sel, col)
		} else {
			sel = append(sel, "NULL AS "+col)
		}
	}
	s := strings.Join(sel, ", ")
	for _, x := range [][2]string{{"spawn_args", "_spawn_args"}, {"org", "_org"}} {
		has, err := c.HasColumn("agents", x[0])
		if err != nil {
			return nil, err
		}
		if has {
			s += ", " + x[0] + " AS " + x[1]
		} else {
			s += ", NULL AS " + x[1]
		}
	}
	gorgs, err := c.GoalOrgs()
	if err != nil {
		return nil, err
	}
	where, args := "", []any(nil)
	if !includeEnded {
		where = "WHERE status NOT IN (" + placeholders(len(endedAgent)) + ")"
		args = strArgs(endedAgent)
	}
	nowE := tsec(nowOr(now))
	out := []Agent{}
	err = c.each(fmt.Sprintf("SELECT %s FROM agents %s ORDER BY name", s, where), args, func(r rowScanner) error {
		var a Agent
		var goal, team, task, model, pane, started, ended, queued, sess, stopReq, spawn, org sql.NullString
		if err := r.Scan(&a.Name, &a.Role, &goal, &team, &task, &a.Status, &model, &pane, &started, &ended,
			&queued, &sess, &stopReq, &spawn, &org); err != nil {
			return err
		}
		a.GoalID, a.TeamID, a.TaskID, a.Model, a.PaneID = nsp(goal), nsp(team), nsp(task), nsp(model), nsp(pane)
		a.Started, a.Ended, a.QueuedAt, a.SessionID, a.StopRequestedAt = nsp(started), nsp(ended), nsp(queued), nsp(sess), nsp(stopReq)
		explicit := spawnRuntime(spawn)
		goalOrg := ""
		if a.GoalID != nil && *a.GoalID != "" {
			goalOrg = gorgs[*a.GoalID]
		}
		a.Org = AgentOrg(org.String, a.Name, explicit, goalOrg)
		a.Runtime = explicit
		if a.Runtime == "" {
			a.Runtime = OrgRuntime(a.Org)
		}
		start, ok := epochP(a.Started)
		if !ok || start == 0 {
			start, ok = epochP(a.QueuedAt)
		}
		end, eok := nowE, true
		if contains(endedAgent, a.Status) {
			end, eok = epochP(a.Ended)
		}
		if ok && eok {
			v := pyRound(end - start)
			a.AgeS = &v
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Task is a row of queries.tasks().
type Task struct {
	ID         string   `json:"id"`
	GoalID     string   `json:"goal_id"`
	TeamID     *string  `json:"team_id"`
	Title      string   `json:"title"`
	Role       string   `json:"role"`
	Status     string   `json:"status"`
	Assignee   *string  `json:"assignee"`
	DependsOn  []string `json:"depends_on"`
	PRURL      *string  `json:"pr_url"`
	Attempts   int64    `json:"attempts"`
	FollowUpOf *string  `json:"follow_up_of"`
	Waits      string   `json:"waits"`
	Created    string   `json:"created"`
	Updated    string   `json:"updated"`
}

// Tasks is queries.tasks; empty goal or status means no filter.
func (c *Conn) Tasks(goal, status string) ([]Task, error) {
	hasFU, err := c.HasColumn("tasks", "follow_up_of")
	if err != nil {
		return nil, err
	}
	hasW, err := c.HasColumn("tasks", "waits")
	if err != nil {
		return nil, err
	}
	sel := "id, goal_id, team_id, title, role, status, assignee, depends_on, pr_url, attempts, "
	if hasFU {
		sel += "follow_up_of"
	} else {
		sel += "NULL AS follow_up_of"
	}
	if hasW {
		sel += ", waits"
	} else {
		sel += ", 'internal' AS waits"
	}
	sel += ", created, updated"
	var cond []string
	var args []any
	if goal != "" {
		cond = append(cond, "goal_id=?")
		args = append(args, goal)
	}
	if status != "" {
		cond = append(cond, "status=?")
		args = append(args, status)
	}
	where := ""
	if len(cond) > 0 {
		where = "WHERE " + strings.Join(cond, " AND ")
	}
	out := []Task{}
	err = c.each(fmt.Sprintf("SELECT %s FROM tasks %s ORDER BY id", sel, where), args, func(r rowScanner) error {
		var t Task
		var team, asg, dep, pr, fu sql.NullString
		var att sql.NullInt64
		if err := r.Scan(&t.ID, &t.GoalID, &team, &t.Title, &t.Role, &t.Status, &asg, &dep, &pr, &att, &fu, &t.Waits, &t.Created, &t.Updated); err != nil {
			return err
		}
		t.TeamID, t.Assignee, t.PRURL, t.FollowUpOf = nsp(team), nsp(asg), nsp(pr), nsp(fu)
		t.Attempts = att.Int64
		t.DependsOn = dependsOn(dep)
		out = append(out, t)
		return nil
	})
	return out, err
}

// TeamRow is a team with its non-ended agents and its tasks (goal_teams).
type TeamRow struct {
	Team
	Agents []Agent `json:"agents"`
	Tasks  []Task  `json:"tasks"`
}

// GoalTeamsEntry holds a goal's teams and its tasks that have no team.
type GoalTeamsEntry struct {
	Teams []*TeamRow `json:"teams"`
	Loose []Task     `json:"loose"`
}

// GoalTeams is queries.goal_teams: teams(), agents() and tasks() joined by (goal, team), three reads whatever the
// number of goals. Goals with no team or task are absent.
func (c *Conn) GoalTeams(now time.Time) (*OMap[*GoalTeamsEntry], error) {
	teams, err := c.Teams()
	if err != nil {
		return nil, err
	}
	out := NewOMap[*GoalTeamsEntry]()
	entry := func(gid string) *GoalTeamsEntry {
		e, ok := out.Get(gid)
		if !ok {
			e = &GoalTeamsEntry{Teams: []*TeamRow{}, Loose: []Task{}}
			out.Set(gid, e)
		}
		return e
	}
	byTeam := map[string]*TeamRow{}
	for _, t := range teams {
		row := &TeamRow{Team: t, Agents: []Agent{}, Tasks: []Task{}}
		byTeam[t.ID] = row
		e := entry(t.GoalID)
		e.Teams = append(e.Teams, row)
	}
	ags, err := c.Agents(false, now)
	if err != nil {
		return nil, err
	}
	for _, a := range ags {
		if a.TeamID != nil {
			if row, ok := byTeam[*a.TeamID]; ok {
				row.Agents = append(row.Agents, a)
			}
		}
	}
	tks, err := c.Tasks("", "")
	if err != nil {
		return nil, err
	}
	for _, t := range tks {
		var row *TeamRow
		if t.TeamID != nil && *t.TeamID != "" {
			row = byTeam[*t.TeamID]
		}
		if row != nil && row.GoalID == t.GoalID {
			row.Tasks = append(row.Tasks, t)
		} else {
			e := entry(t.GoalID)
			e.Loose = append(e.Loose, t)
		}
	}
	return out, nil
}

// Util is queries.utilization_sample's result.
type Util struct {
	Line  string `json:"line"`
	Under bool   `json:"under"`
}

// UtilFreshS is how old (seconds) the newest util_samples row may be.
const UtilFreshS = 300

// UtilizationSample is queries.utilization_sample: the latest util_samples row, or nil when there is none, the
// newest is older than 5 minutes, or the table is missing (an old database). It reads only; the tick writes.
func (c *Conn) UtilizationSample(now time.Time) (*Util, error) {
	var ts, p1s string
	var live, workers, leads, judges, hq, queued, target, demand int64
	err := c.QueryRow("SELECT ts, live, workers, leads, judges, hq, queued, target, demand, p1_starved FROM util_samples ORDER BY id DESC LIMIT 1").
		Scan(&ts, &live, &workers, &leads, &judges, &hq, &queued, &target, &demand, &p1s)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	t, ok := ParseTS(ts)
	age := nowOr(now).Sub(t).Seconds()
	if !ok || age < 0 || age > UtilFreshS {
		return nil, nil
	}
	var p1 []string
	if err := json.Unmarshal([]byte(p1s), &p1); err != nil {
		p1 = nil
	}
	starved := "none"
	if len(p1) > 0 {
		starved = strings.Join(p1, ", ")
	}
	line := fmt.Sprintf("live %d: workers %d / leads %d / judges %d / HQ %d / queued %d; P1 goals without workers: %s",
		live, workers, leads, judges, hq, queued, starved)
	return &Util{Line: line, Under: workers < target && demand != 0}, nil
}

// CycleItem is one timeline entry of PRCycle. Verdict items also carry "head" (between label and detail).
type CycleItem struct {
	TS      string
	Kind    string
	Actor   *string
	Label   string
	HasHead bool
	Head    *string
	Detail  *string
	order   int64
}

// MarshalJSON writes ts, kind, actor, label, [head,] detail.
func (i CycleItem) MarshalJSON() ([]byte, error) {
	p := []kv{{"ts", i.TS}, {"kind", i.Kind}, {"actor", i.Actor}, {"label", i.Label}}
	if i.HasHead {
		p = append(p, kv{"head", i.Head})
	}
	return ordered(append(p, kv{"detail", i.Detail})...)
}

// Cycle is queries.pr_cycle's result.
type Cycle struct {
	TaskID     string      `json:"task_id"`
	Items      []CycleItem `json:"items"`
	Rejections int64       `json:"rejections"`
	Attempts   int64       `json:"attempts"`
	Retries    int64       `json:"retries"`
	Reopens    int64       `json:"reopens"`
}

var prCycleActions = []string{"add", "update", "note", "reopen", "follow-up", "judge-reject",
	"judge-blocked", "merge-failed", "merged", "merge-post-error"}

// PRCycle is queries.pr_cycle: the timeline of one task (events, verdicts, agent spawns and ends) ordered by time.
func (c *Conn) PRCycle(tid string) (*Cycle, error) {
	var items []CycleItem
	ev := map[string]int64{"judge-reject": 0, "reopen": 0}
	args := append([]any{tid}, strArgs(prCycleActions)...)
	err := c.each("SELECT id,ts,actor,action,detail FROM events WHERE entity='task' AND entity_id=? AND action IN ("+
		placeholders(len(prCycleActions))+") ORDER BY id", args, func(r rowScanner) error {
		var id int64
		var ts, action string
		var actor, detail sql.NullString
		if err := r.Scan(&id, &ts, &actor, &action, &detail); err != nil {
			return err
		}
		ev[action]++
		items = append(items, CycleItem{TS: ts, Kind: "event", Actor: nsp(actor), Label: action, Detail: truncP(nsp(detail), DetailMax), order: id})
		return nil
	})
	if err != nil {
		return nil, err
	}
	var rejV, judgeRejV int64
	err = c.each("SELECT id,role,verdict,head_sha,created,author,summary FROM verdicts WHERE task_id=? ORDER BY id", []any{tid}, func(r rowScanner) error {
		var id int64
		var role, verdict, created string
		var head, author, summary sql.NullString
		if err := r.Scan(&id, &role, &verdict, &head, &created, &author, &summary); err != nil {
			return err
		}
		if verdict == "fail" || verdict == "reject" {
			rejV++
			if role == "judge" {
				judgeRejV++
			}
		}
		items = append(items, CycleItem{TS: created, Kind: "verdict", Actor: nsp(author), Label: role + " " + verdict,
			HasHead: true, Head: first8(nsp(head)), Detail: truncP(nsp(summary), DetailMax), order: id})
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = c.each("SELECT name, role, started, ended, status FROM agents WHERE task_id=?", []any{tid}, func(r rowScanner) error {
		var name, role, status string
		var started, ended sql.NullString
		if err := r.Scan(&name, &role, &started, &ended, &status); err != nil {
			return err
		}
		if started.Valid && started.String != "" {
			items = append(items, CycleItem{TS: started.String, Kind: "agent", Actor: sp(name), Label: "spawn " + role})
		}
		if ended.Valid && ended.String != "" {
			items = append(items, CycleItem{TS: ended.String, Kind: "agent", Actor: sp(name), Label: "end " + role, Detail: sp(status)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	key := func(i CycleItem) float64 {
		if e, ok := epoch(i.TS); ok {
			return e
		}
		return math.Inf(1)
	}
	sort.SliceStable(items, func(a, b int) bool {
		ka, kb := key(items[a]), key(items[b])
		if ka != kb {
			return ka < kb
		}
		if items[a].Kind != items[b].Kind {
			return items[a].Kind < items[b].Kind
		}
		return items[a].order < items[b].order
	})
	var attempts int64
	err = c.QueryRow("SELECT attempts FROM tasks WHERE id=?", tid).Scan(&attempts)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	extra := ev["judge-reject"] - judgeRejV
	if extra < 0 {
		extra = 0
	}
	retries := attempts - 1
	if retries < 0 {
		retries = 0
	}
	if items == nil {
		items = []CycleItem{}
	}
	return &Cycle{TaskID: tid, Items: items, Rejections: rejV + extra, Attempts: attempts, Retries: retries, Reopens: ev["reopen"]}, nil
}

// Event is a row of queries.events().
type Event struct {
	ID       int64   `json:"id"`
	TS       string  `json:"ts"`
	Actor    *string `json:"actor"`
	Entity   *string `json:"entity"`
	EntityID *string `json:"entity_id"`
	Action   string  `json:"action"`
	Detail   *string `json:"detail"`
}

// Events is queries.events: newest first. since is an ISO timestamp compared as an instant, not as text.
func (c *Conn) Events(since string, actions []string, entity string, limit int) ([]Event, error) {
	var cond []string
	var args []any
	if len(actions) > 0 {
		cond = append(cond, "action IN ("+placeholders(len(actions))+")")
		args = append(args, strArgs(actions)...)
	}
	if entity != "" {
		cond = append(cond, "entity=?")
		args = append(args, entity)
	}
	where := ""
	if len(cond) > 0 {
		where = "WHERE " + strings.Join(cond, " AND ")
	}
	cut, hasCut := 0.0, false
	if since != "" {
		cut, hasCut = epoch(since)
	}
	out := []Event{}
	err := c.each("SELECT id,ts,actor,entity,entity_id,action,detail FROM events "+where+" ORDER BY id DESC", args, func(r rowScanner) error {
		var e Event
		var actor, ent, eid, detail sql.NullString
		if err := r.Scan(&e.ID, &e.TS, &actor, &ent, &eid, &e.Action, &detail); err != nil {
			return err
		}
		if hasCut {
			if ts, ok := epoch(e.TS); ok && ts < cut {
				return nil
			}
		}
		e.Actor, e.Entity, e.EntityID, e.Detail = nsp(actor), nsp(ent), nsp(eid), truncP(nsp(detail), DetailMax)
		out = append(out, e)
		if len(out) >= limit {
			return errStop
		}
		return nil
	})
	if err == errStop {
		err = nil
	}
	return out, err
}

var errStop = fmt.Errorf("stop")

// MessageMeta is a row of queries.messages_meta (never the body).
type MessageMeta struct {
	ID        int64   `json:"id"`
	Sender    string  `json:"sender"`
	Recipient string  `json:"recipient"`
	Kind      string  `json:"kind"`
	Subject   string  `json:"subject"`
	GoalID    *string `json:"goal_id"`
	TaskID    *string `json:"task_id"`
	Created   string  `json:"created"`
	Read      bool    `json:"read"`
}

// DefaultMessageKinds is messages_meta's default kinds.
var DefaultMessageKinds = []string{"decision", "escalation", "report", "alert"}

// MessagesMeta is queries.messages_meta: message metadata only, subject truncated to 120 characters, newest first.
func (c *Conn) MessagesMeta(kinds []string, limit int) ([]MessageMeta, error) {
	args := append(append([]any{SubjectMax}, strArgs(kinds)...), limit)
	out := []MessageMeta{}
	err := c.each("SELECT id, sender, recipient, kind, substr(subject,1,?) subject, goal_id, task_id, created, "+
		"read_at IS NOT NULL AS read FROM messages WHERE kind IN ("+placeholders(len(kinds))+") ORDER BY id DESC LIMIT ?", args, func(r rowScanner) error {
		var m MessageMeta
		var goal, task sql.NullString
		var read int64
		if err := r.Scan(&m.ID, &m.Sender, &m.Recipient, &m.Kind, &m.Subject, &goal, &task, &m.Created, &read); err != nil {
			return err
		}
		m.GoalID, m.TaskID, m.Read = nsp(goal), nsp(task), read != 0
		out = append(out, m)
		return nil
	})
	return out, err
}

// MsgRef is a message's goal and task ids; it marshals as the Python tuple [goal_id, task_id].
type MsgRef struct{ GoalID, TaskID *string }

// MarshalJSON writes [goal_id, task_id].
func (m MsgRef) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	g, _ := marshal(m.GoalID)
	t, _ := marshal(m.TaskID)
	b.WriteString("[")
	b.Write(g)
	b.WriteString(",")
	b.Write(t)
	b.WriteString("]")
	return b.Bytes(), nil
}

// MessageRefs is queries.message_refs: {message id: (goal_id, task_id)}; ids and refs only, never a subject or body.
func (c *Conn) MessageRefs(ids []int64) (*OMap[MsgRef], error) {
	ss := make([]string, len(ids))
	for i, id := range ids {
		ss[i] = fmt.Sprint(id)
	}
	out := NewOMap[MsgRef]()
	err := c.inRows("SELECT id, goal_id, task_id FROM messages WHERE id IN ({in})", ss, func(r rowScanner) error {
		var id int64
		var g, t sql.NullString
		if err := r.Scan(&id, &g, &t); err != nil {
			return err
		}
		out.Set(fmt.Sprint(id), MsgRef{nsp(g), nsp(t)})
		return nil
	})
	return out, err
}

// TaskTitles is queries.task_titles: {task id: title truncated to width, [SCn] tags removed}, one query per chunk.
func (c *Conn) TaskTitles(ids []string, width int) (*OMap[string], error) {
	out := NewOMap[string]()
	err := c.inRows("SELECT id, title FROM tasks WHERE id IN ({in})", ids, func(r rowScanner) error {
		var id, title string
		if err := r.Scan(&id, &title); err != nil {
			return err
		}
		out.Set(id, trunc(stripTags(title), width))
		return nil
	})
	return out, err
}

// Config is a row of queries.config().
type ConfigRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Config is queries.config.
func (c *Conn) Config() ([]ConfigRow, error) {
	out := []ConfigRow{}
	err := c.each("SELECT key, value FROM config ORDER BY key", nil, func(r rowScanner) error {
		var k ConfigRow
		if err := r.Scan(&k.Key, &k.Value); err != nil {
			return err
		}
		out = append(out, k)
		return nil
	})
	return out, err
}
