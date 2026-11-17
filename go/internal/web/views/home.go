package views

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// Overview constants (web/views/home.py).
const (
	needsMax      = 3
	teamScopeMax  = 120
	taskTitleMax  = 90
	agentsMax     = 4
	mergesMax     = 5
	bouncingMax   = 3
	homeDays      = 14
	titleOverview = "Overview"
)

var (
	shownTask = []string{"blocked", "in_review", "in_progress"} // level 3 lists these; todo sits behind "N more"
	counted   = []string{"todo", "in_progress", "in_review", "blocked"}
	openGoal  = map[string]bool{"active": true, "planning": true, "proposed": true, "blocked": true}
	hqRoles   = map[string]bool{"boss": true, "director": true, "staff-engineer": true}
)

// ── data ──

func waitText(iso string, ctx *Ctx) string {
	if a := Ago(iso, ctx.Now, ""); a != "" {
		return "waiting " + a
	}
	return ""
}

// needs returns the items waiting on the owner, most urgent first. Escalations carry the subject only. Each item has
// an Org (its goal's org; a message without a goal: the boss it was sent to, else its sender's prefix, else the default
// org). With org set only that org's items are returned.
func needs(ctx *Ctx, org string, op *data.Pending) ([]NeedsItem, error) {
	var err error
	if op == nil {
		if op, err = ctx.DB.OwnerPending(); err != nil {
			return nil, fmt.Errorf("views: owner pending: %w", err)
		}
	}
	ids := make([]int64, 0, len(op.Escalations))
	for _, m := range op.Escalations {
		ids = append(ids, m.ID)
	}
	refs, err := ctx.DB.MessageRefs(ids)
	if err != nil {
		return nil, fmt.Errorf("views: message refs: %w", err)
	}
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return nil, fmt.Errorf("views: goal orgs: %w", err)
	}
	taskGoal := map[string]string{}
	if len(gorgs) > 0 {
		rows, err := ctx.DB.Query("SELECT id, goal_id FROM tasks")
		if err != nil {
			return nil, fmt.Errorf("views: read tasks: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var g sql.NullString
			if err := rows.Scan(&id, &g); err != nil {
				return nil, fmt.Errorf("views: scan tasks: %w", err)
			}
			taskGoal[id] = g.String
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("views: read tasks: %w", err)
		}
	}
	gOrg := func(gid string) string { return data.OrgOfGoal(gorgs, gid) }
	var items []NeedsItem
	for _, t := range op.TasksBlocked {
		items = append(items, NeedsItem{Kind: "Blocked task", Tone: "danger", Subject: stripTags(t.Title), Ref: t.ID,
			Href: "/task/" + t.ID, Age: waitText(t.Updated, ctx), Org: gOrg(t.GoalID), Goal: t.GoalID})
	}
	for _, m := range op.Escalations {
		var goal, task string
		if r, ok := refs.Get(fmt.Sprint(m.ID)); ok {
			goal, task = deref(r.GoalID), deref(r.TaskID)
		}
		var ref, href string
		switch {
		case task != "":
			ref, href = task, "/task/"+task
		case goal != "":
			ref, href = goal, "/goal/"+goal
		}
		gid := goal
		if gid == "" {
			gid = taskGoal[task]
		}
		var o string
		switch {
		case gid != "":
			o = gOrg(gid)
		case orgPrefix(m.Recipient) != "":
			o = orgPrefix(m.Recipient)
		case orgPrefix(m.Sender) != "":
			o = orgPrefix(m.Sender)
		default:
			o = data.DefaultOrg
		}
		kind := "Request"
		if m.Kind == "escalation" {
			kind = "Escalation"
		}
		items = append(items, NeedsItem{Kind: kind, Tone: "warning", Subject: m.Subject, Ref: ref, Href: href, Message: m.ID, HasMessage: true,
			Age: waitText(m.Created, ctx), Org: o, Goal: gid})
	}
	for _, t := range op.JudgeBlockedAutoMerge {
		items = append(items, NeedsItem{Kind: "Merge needs OK", Tone: "warning", Subject: stripTags(t.Title), Ref: t.TaskID,
			Href: "/task/" + t.TaskID, Age: waitText(t.TS, ctx), Org: gOrg(t.GoalID), Goal: t.GoalID})
	}
	for _, g := range op.Goals {
		kind, tone := "Proposed goal", "neutral"
		if g.Status == "blocked" {
			kind, tone = "Blocked goal", "danger"
		}
		items = append(items, NeedsItem{Kind: kind, Tone: tone, Subject: g.Title, Ref: g.ID, Href: "/goal/" + g.ID,
			Age: waitText(g.Updated, ctx), Org: gOrg(g.ID), Goal: g.ID})
	}
	if org == "" {
		return items, nil
	}
	var out []NeedsItem
	for _, i := range items {
		if i.Org == org {
			out = append(out, i)
		}
	}
	return out, nil
}

// needsByGoal is {goal id: number of owner waits} from needs() items; an item with no goal counts for no goal.
func needsByGoal(items []NeedsItem) map[string]int {
	out := map[string]int{}
	for _, i := range items {
		if i.Goal != "" {
			out[i.Goal]++
		}
	}
	return out
}

// UsageData is the usage part of the System section.
type UsageData struct {
	FiveHour, SevenDay *float64
	CapFive, CapSeven  float64
	Stale              bool
}

// SystemData is the Overview's System section: the same numbers as the Resources page gauges.
type SystemData struct {
	Live, Queued, Cap int
	MemAvailable      *float64
	MemFloor          int
	Load1             *float64
	CPUs              int
	Usage             UsageData
	Cursor            *tokens.DisplayData
}

func system(ctx *Ctx, ag []data.Agent) (*SystemData, error) {
	s := &SystemData{}
	for _, a := range ag {
		switch a.Status {
		case "live":
			s.Live++
		case "queued":
			s.Queued++
		}
	}
	capV, err := ctx.cfgNum("max_live_agents", 6)
	if err != nil {
		return nil, err
	}
	floor, err := ctx.cfgNum("min_mem_available_mb", 1024)
	if err != nil {
		return nil, err
	}
	s.Cap, s.MemFloor = int(capV), int(floor)
	if ctx.Mem != nil {
		s.MemAvailable = ctx.Mem()
	}
	if ctx.Machine != nil {
		m := ctx.Machine()
		s.Load1, s.CPUs = m.Load1, m.CPUs
	}
	var u *data.Usage
	if ctx.Usage != nil {
		u = ctx.Usage()
	}
	fresh := u != nil && !u.Unreadable && (u.FiveHour != nil || u.SevenDay != nil)
	if s.Usage.CapFive, err = ctx.cfgNum("max_five_hour_pct", 92); err != nil {
		return nil, err
	}
	if s.Usage.CapSeven, err = ctx.cfgNum("max_seven_day_pct", 92); err != nil {
		return nil, err
	}
	if fresh {
		s.Usage.FiveHour, s.Usage.SevenDay = u.FiveHour, u.SevenDay
		s.Usage.Stale = !u.Fresh || u.AgeMin > 30
	}
	if s.Cursor, err = ctx.cursor(); err != nil {
		return nil, err
	}
	return s, nil
}

type taskRow struct {
	ID, Title, Status, Assignee, PRURL, Waits string
	Rejections, Attempts                      int64
}

func newTaskRow(t data.Task, rej map[string]int64) taskRow {
	return taskRow{ID: t.ID, Title: trunc(stripTags(t.Title), taskTitleMax), Status: t.Status, Assignee: deref(t.Assignee), PRURL: deref(t.PRURL),
		Rejections: rej[t.ID], Attempts: t.Attempts, Waits: t.Waits}
}

type teamNode struct {
	ID, Scope, Lead, LeadStatus string
	Running                     int
	Counts                      map[string]int
	Tasks, More                 []taskRow
	OwnerWait                   bool
}

// newTeamNode is one level-2 node. team is nil for the goal's tasks that have no team. Level 3 holds the in review /
// blocked / in progress tasks (blocked first); More holds the open rest (todo). Done and cancelled tasks are history.
func newTeamNode(team *data.Team, tasks []data.Task, agents []data.Agent, rej map[string]int64, asked map[string]bool) teamNode {
	by := map[string][]data.Task{}
	for _, t := range tasks {
		by[t.Status] = append(by[t.Status], t)
	}
	n := teamNode{Counts: map[string]int{}}
	for _, st := range shownTask {
		for _, t := range by[st] {
			n.Tasks = append(n.Tasks, newTaskRow(t, rej))
		}
	}
	for _, t := range by["todo"] {
		n.More = append(n.More, newTaskRow(t, rej))
	}
	for _, st := range counted {
		n.Counts[st] = len(by[st])
	}
	if team != nil {
		n.ID = team.ID
		if team.Scope != nil && *team.Scope != "" {
			n.Scope = trunc(*team.Scope, teamScopeMax)
		}
		n.Lead = deref(team.LeadAgent)
		for _, a := range agents {
			if a.Name == n.Lead {
				n.LeadStatus = a.Status
				break
			}
		}
	}
	for _, a := range agents {
		if a.Status == "live" {
			n.Running++
		}
	}
	for _, t := range by["blocked"] {
		if t.Waits == "owner" {
			n.OwnerWait = true
		}
	}
	for _, t := range tasks {
		if asked[t.ID] {
			n.OwnerWait = true
		}
	}
	return n
}

type treeGoal struct {
	ID, Title, Status, Org, LastActivity string
	Priority                             int64
	TasksTotal, TasksDone, OpenPRs       int64
	Criteria                             *data.CriteriaCount
	Needs                                int
	Teams                                []teamNode
}

// tree is level 1 to 3 of the overview tree for the open goals of gl: goals with an owner wait first, then by priority.
func tree(ctx *Ctx, gl []data.Goal, items []NeedsItem) ([]treeGoal, error) {
	now := ctx.Now
	grouped, err := ctx.DB.GoalTeams(now)
	if err != nil {
		return nil, fmt.Errorf("views: goal teams: %w", err)
	}
	crit, err := ctx.DB.CriteriaByGoal()
	if err != nil {
		return nil, fmt.Errorf("views: criteria: %w", err)
	}
	last, err := ctx.DB.LastActivityByGoal()
	if err != nil {
		return nil, fmt.Errorf("views: last activity: %w", err)
	}
	bounce, err := ctx.DB.BouncingTasks(1<<30, nil)
	if err != nil {
		return nil, fmt.Errorf("views: bouncing tasks: %w", err)
	}
	rej := map[string]int64{}
	for _, b := range bounce {
		rej[b.TaskID] = b.Rejections
	}
	wait := needsByGoal(items)
	asked := map[string]bool{}
	for _, i := range items {
		if i.Ref != "" {
			asked[i.Ref] = true
		}
	}
	var out []treeGoal
	for _, g := range gl {
		if !openGoal[g.Status] {
			continue
		}
		var nodes []teamNode
		grp, _ := grouped.Get(g.ID)
		if grp != nil {
			for _, t := range grp.Teams {
				team := t.Team
				nodes = append(nodes, newTeamNode(&team, t.Tasks, t.Agents, rej, asked))
			}
			var loose []data.Task
			for _, t := range grp.Loose {
				if t.Status != "done" && t.Status != "cancelled" {
					loose = append(loose, t)
				}
			}
			if len(loose) > 0 {
				nodes = append(nodes, newTeamNode(nil, loose, nil, rej, asked))
			}
		}
		sort.SliceStable(nodes, func(i, j int) bool {
			a, b := nodes[i], nodes[j]
			if a.OwnerWait != b.OwnerWait {
				return a.OwnerWait
			}
			if (a.ID == "") != (b.ID == "") {
				return a.ID != ""
			}
			return a.ID < b.ID
		})
		tg := treeGoal{ID: g.ID, Title: g.Title, Status: g.Status, Org: g.Org, Priority: g.Priority, TasksTotal: g.TasksTotal,
			TasksDone: g.TasksDone, OpenPRs: g.OpenPRs, Needs: wait[g.ID], Teams: nodes}
		if c, ok := crit.Get(g.ID); ok {
			cc := c
			tg.Criteria = &cc
		}
		tg.LastActivity = g.Updated
		if l, ok := last.Get(g.ID); ok && l != "" {
			tg.LastActivity = l
		}
		out = append(out, tg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Needs == 0) != (b.Needs == 0) {
			return a.Needs != 0
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	})
	return out, nil
}

type agentsData struct {
	Running, Queued, Total, Leads, HQ int
	Rows                              []AgentRow
}

func epochOf(iso string) float64 {
	t, ok := data.ParseTS(iso)
	if !ok {
		return 0
	}
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

func agentsSection(ctx *Ctx, ag []data.Agent) (*agentsData, error) {
	last, err := ctx.DB.LastEventByAgent()
	if err != nil {
		return nil, fmt.Errorf("views: last event by agent: %w", err)
	}
	rows := append([]data.Agent(nil), ag...)
	lastOf := func(n string) string { v, _ := last.Get(n); return v }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Status != "live") != (b.Status != "live") {
			return a.Status == "live"
		}
		if ea, eb := epochOf(lastOf(a.Name)), epochOf(lastOf(b.Name)); ea != eb {
			return ea > eb
		}
		return a.Name < b.Name
	})
	shown := rows
	if len(shown) > agentsMax {
		shown = shown[:agentsMax]
	}
	var ids []string
	for _, a := range shown {
		if a.TaskID != nil && *a.TaskID != "" {
			ids = append(ids, *a.TaskID)
		}
	}
	titles, err := ctx.DB.TaskTitles(ids, 80)
	if err != nil {
		return nil, fmt.Errorf("views: task titles: %w", err)
	}
	d := &agentsData{Total: len(ag)}
	for _, a := range ag {
		switch a.Status {
		case "live":
			d.Running++
			if a.Role == "lead" {
				d.Leads++
			}
			if hqRoles[a.Role] {
				d.HQ++
			}
		case "queued":
			d.Queued++
		}
	}
	for _, a := range shown {
		tid := deref(a.TaskID)
		title, _ := titles.Get(tid)
		rt := a.Runtime
		if rt == "" {
			rt = "claude"
		}
		d.Rows = append(d.Rows, AgentRow{Name: a.Name, Role: a.Role, Status: a.Status, Runtime: rt, Org: a.Org, TaskID: tid,
			TaskTitle: title, LastEvent: lastOf(a.Name)})
	}
	return d, nil
}

type qualityData struct {
	RejPerMerged *float64
	FirstTryPct  *int64
	MedianSpawnS *int64
	Bouncing     []data.Bouncing
}

func quality(ctx *Ctx, goals data.GoalSet) (*qualityData, error) {
	q, err := ctx.DB.Quality(nil)
	if err != nil {
		return nil, fmt.Errorf("views: quality: %w", err)
	}
	o := q.Overall
	d := &qualityData{}
	if o.RejectionsPerMerged != nil {
		v := o.RejectionsPerMerged.V
		d.RejPerMerged = &v
	}
	if o.Merged != 0 {
		v := pyRound(100 * float64(o.MergedFirstTry) / float64(o.Merged))
		d.FirstTryPct = &v
	}
	tp, err := ctx.DB.Throughput(homeDays, ctx.Now, nil)
	if err != nil {
		return nil, fmt.Errorf("views: throughput: %w", err)
	}
	var spawn []float64
	for _, t := range tp.Tasks {
		if t.TotalS != nil {
			spawn = append(spawn, float64(*t.TotalS))
		}
	}
	if len(spawn) > 0 {
		sort.Float64s(spawn)
		m := spawn[len(spawn)/2]
		if len(spawn)%2 == 0 {
			m = (spawn[len(spawn)/2-1] + spawn[len(spawn)/2]) / 2
		}
		v := pyRound(m)
		d.MedianSpawnS = &v
	}
	if d.Bouncing, err = ctx.DB.BouncingTasks(bouncingMax+1, goals); err != nil {
		return nil, fmt.Errorf("views: bouncing tasks: %w", err)
	}
	return d, nil
}

type hq struct{ Name, Status string }

type lane struct {
	Org, Label                                                                         string
	Boss, Director                                                                     *hq
	TeamsActive, LeadsAwake, LeadsParked, LeadsWaking, WorkersLive, AgentsLive, Queued int
	GoalsActive, GoalsOpen, Needs                                                      int
	HealthTone, HealthText                                                             string
}

// hqOf is the org's boss/director among the non-ended agents: live first, then queued/starting; nil when not running.
func hqOf(ag []data.Agent, org, role string) *hq {
	var rows []data.Agent
	for _, a := range ag {
		if a.Org == org && a.Role == role {
			rows = append(rows, a)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Status != "live") != (b.Status != "live") {
			return a.Status == "live"
		}
		if (a.Status != "starting") != (b.Status != "starting") {
			return a.Status == "starting"
		}
		want := org + "-" + role
		if (a.Name != want) != (b.Name != want) {
			return a.Name == want
		}
		return a.Name < b.Name
	})
	return &hq{rows[0].Name, rows[0].Status}
}

func parkedTeams(ctx *Ctx) (map[string]bool, error) {
	has, err := ctx.DB.HasColumn("teams", "parked_at")
	if err != nil {
		return nil, fmt.Errorf("views: teams columns: %w", err)
	}
	out := map[string]bool{}
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
	return out, rows.Err()
}

func newLane(org string, gl []data.Goal, ag []data.Agent, teams []data.Team, items []NeedsItem, parked map[string]bool) lane {
	statusBy := map[string]string{}
	for _, a := range ag {
		statusBy[a.Name] = a.Status
	}
	active := map[string]bool{}
	l := lane{Org: org, Label: OrgLabel(org)}
	var orgActive []data.Team
	for _, t := range teams {
		if t.Org == org && t.Status == "active" {
			orgActive = append(orgActive, t)
			active[t.ID] = true
		}
	}
	for _, t := range orgActive {
		st := ""
		if t.LeadAgent != nil {
			st = statusBy[*t.LeadAgent]
		}
		switch {
		case st == "live":
			l.LeadsAwake++
		case st == "queued" || st == "starting":
			l.LeadsWaking++
		case parked[t.ID]:
			l.LeadsParked++
		}
	}
	for _, a := range ag {
		if a.Org == org && a.Status == "live" {
			l.AgentsLive++
		}
		if a.Org == org && a.Status == "queued" {
			l.Queued++
		}
		if a.Status == "live" && a.TeamID != nil && active[*a.TeamID] && a.Role != "boss" && a.Role != "director" && a.Role != "lead" {
			l.WorkersLive++
		}
	}
	l.TeamsActive = len(orgActive)
	for _, g := range gl {
		if g.Org != org {
			continue
		}
		if openGoal[g.Status] {
			l.GoalsOpen++
		}
		if g.Status == "active" || g.Status == "planning" {
			l.GoalsActive++
		}
	}
	for _, i := range items {
		if i.Org == org {
			l.Needs++
		}
	}
	l.Boss, l.Director = hqOf(ag, org, "boss"), hqOf(ag, org, "director")
	busy := l.GoalsOpen > 0 || l.AgentsLive > 0 || l.Queued > 0
	up := func(x *hq) bool { return x != nil && x.Status == "live" }
	switch {
	case busy && !up(l.Boss):
		l.HealthTone, l.HealthText = "danger", "Boss not running"
	case l.GoalsActive > 0 && !up(l.Director):
		l.HealthTone, l.HealthText = "warning", "Director not running"
	case l.Needs > 0:
		s := "s"
		if l.Needs == 1 {
			s = ""
		}
		l.HealthTone, l.HealthText = "warning", fmt.Sprintf("%d need%s you", l.Needs, s)
	case l.Queued > 0:
		l.HealthTone, l.HealthText = "warning", fmt.Sprintf("%d queued", l.Queued)
	case !busy:
		l.HealthTone, l.HealthText = "neutral", "Idle"
	default:
		l.HealthTone, l.HealthText = "success", "All normal"
	}
	return l
}

// lanes are the org lanes: both orgs when both have goals or agents (or the one picked with ?org=); none otherwise.
func lanes(ctx *Ctx, gl []data.Goal, ag []data.Agent, items []NeedsItem, org string) ([]lane, error) {
	var present []string
	for _, o := range data.Orgs {
		has := false
		for _, g := range gl {
			has = has || g.Org == o
		}
		for _, a := range ag {
			has = has || a.Org == o
		}
		if has {
			present = append(present, o)
		}
	}
	var shown []string
	switch {
	case org != "":
		shown = []string{org}
	case len(present) > 1:
		shown = present
	default:
		return nil, nil
	}
	teams, err := ctx.DB.Teams()
	if err != nil {
		return nil, fmt.Errorf("views: teams: %w", err)
	}
	parked, err := parkedTeams(ctx)
	if err != nil {
		return nil, err
	}
	var out []lane
	for _, o := range shown {
		out = append(out, newLane(o, gl, ag, teams, items, parked))
	}
	return out, nil
}

// HomeData is everything the Overview renders.
type HomeData struct {
	Org         string
	Lanes       []lane
	Utilization *data.Util
	NeedsYou    []NeedsItem
	Tree        []treeGoal
	System      *SystemData
	Agents      *agentsData
	Recent      []data.RecentMerge
	PerDay      []data.DayCount
	ShippedSum  int64
	Quality     *qualityData
}

// LoadHome gathers the data of the Overview (web/views/home.py data()).
func LoadHome(ctx *Ctx) (*HomeData, error) {
	org := ctx.org()
	allGoals, err := ctx.DB.Goals()
	if err != nil {
		return nil, fmt.Errorf("views: goals: %w", err)
	}
	allAgents, err := ctx.DB.Agents(false, ctx.Now)
	if err != nil {
		return nil, fmt.Errorf("views: agents: %w", err)
	}
	op, err := ctx.DB.OwnerPending()
	if err != nil {
		return nil, fmt.Errorf("views: owner pending: %w", err)
	}
	allNeeds, err := needs(ctx, "", op)
	if err != nil {
		return nil, err
	}
	d := &HomeData{Org: org}
	if d.Lanes, err = lanes(ctx, allGoals, allAgents, allNeeds, org); err != nil {
		return nil, err
	}
	var gl []data.Goal
	var ag []data.Agent
	var goalIDs data.GoalSet
	if org != "" {
		goalIDs = data.GoalSet{}
	}
	for _, g := range allGoals {
		if org == "" || g.Org == org {
			gl = append(gl, g)
			if org != "" {
				goalIDs[g.ID] = true
			}
		}
	}
	for _, a := range allAgents {
		if org == "" || a.Org == org {
			ag = append(ag, a)
		}
	}
	for _, i := range allNeeds {
		if org == "" || i.Org == org {
			d.NeedsYou = append(d.NeedsYou, i)
		}
	}
	if d.Utilization, err = ctx.DB.UtilizationSample(ctx.Now); err != nil {
		return nil, fmt.Errorf("views: utilization: %w", err)
	}
	if d.Tree, err = tree(ctx, gl, d.NeedsYou); err != nil {
		return nil, err
	}
	if d.System, err = system(ctx, allAgents); err != nil {
		return nil, err
	}
	if d.Agents, err = agentsSection(ctx, ag); err != nil {
		return nil, err
	}
	if d.PerDay, err = ctx.DB.MergesPerDay(homeDays, ctx.Now, goalIDs); err != nil {
		return nil, fmt.Errorf("views: merges per day: %w", err)
	}
	for _, p := range d.PerDay {
		d.ShippedSum += p.Merges
	}
	if d.Recent, err = ctx.DB.RecentMerges(mergesMax, goalIDs); err != nil {
		return nil, fmt.Errorf("views: recent merges: %w", err)
	}
	if d.Quality, err = quality(ctx, goalIDs); err != nil {
		return nil, err
	}
	return d, nil
}

// ── render ──

func sect(id, title string, count any, more *EmptyAction, body ...any) Safe {
	var n, m any
	if count != nil {
		n = El("span", count, Cls("sect-n"))
	}
	if more != nil {
		m = El("a", more.Text, At("href", more.Href), Cls("sect-link"))
	}
	head := El("div", El("h2", title), n, m, Cls("sect-head"))
	return El("section", head, El("div", append(body, Cls("sect-box"))...), At("id", id), Cls("sect ov-sect"))
}

func hqCell(role string, a *hq) Safe {
	if a == nil {
		return El("span", El("span", role+":", Cls("lane-l")), " ", El("span", Glyph("exited"), "Not running", Cls("st fg-neutral")))
	}
	return El("span", El("span", role+":", Cls("lane-l")), " ", Status(a.Status, false), " ", El("span", a.Name, Cls("mono")))
}

func laneEl(ln lane) Safe {
	o := ln.Org
	head := El("div", El("h2", OrgDot(o), El("a", ln.Label+" org", At("href", "/?org="+o)), Cls("lane-title")),
		El("span", Glyph(healthGlyphOf(ln.HealthTone)), ln.HealthText, Cls("st st--pill tone-"+ln.HealthTone+" lane-health")), Cls("lane-head"))
	hqEl := El("div", hqCell("Boss", ln.Boss), hqCell("Director", ln.Director), Cls("lane-hq"))
	stat := func(n int, label, href, title string) Safe {
		var t any
		if title != "" {
			t = title
		}
		return El("a", El("span", n, Cls("lane-n")), El("span", label, Cls("lane-l")), At("href", href), Cls("lane-stat"), At("title", t))
	}
	teamLine := fmt.Sprintf(" teams · %d lead awake · %d parked · %d waking · %d workers", ln.LeadsAwake, ln.LeadsParked, ln.LeadsWaking, ln.WorkersLive)
	stats := El("div",
		stat(ln.TeamsActive, teamLine, "/agents?org="+o, fmt.Sprintf("%d%s", ln.TeamsActive, teamLine)),
		stat(ln.WorkersLive, "Live workers", "/agents?org="+o+"&status=live", fmt.Sprintf("%d agents live in total", ln.AgentsLive)),
		stat(ln.GoalsActive, "Active goals", "/goals?org="+o, fmt.Sprintf("%d open goals", ln.GoalsOpen)),
		stat(ln.Queued, "Queued", "/agents?org="+o+"&status=queued", ""), Cls("lane-stats"))
	return El("section", head, hqEl, stats, Cls("lane lane--"+o), At("aria-label", ln.Label+" org"))
}

func lanesSection(d *HomeData) any {
	if len(d.Lanes) == 0 {
		return nil
	}
	var els []any
	for _, l := range d.Lanes {
		els = append(els, laneEl(l))
	}
	var note any
	if d.Org != "" {
		note = El("p", "Showing the "+OrgLabel(d.Org)+" org only. System capacity is shared by both orgs; ", El("a", "show all", At("href", "/")), ".", Cls("org-note"))
	}
	return El("div", els, note, Cls("lanes"), At("id", "orgs"))
}

func orgPath(d *HomeData, path string) string { return OrgHref(path, d.Org) }

func needsSection(d *HomeData) any {
	if len(d.NeedsYou) == 0 {
		return nil
	}
	var rows []any
	for i, it := range d.NeedsYou {
		if i >= needsMax {
			break
		}
		rows = append(rows, NeedsYouRow(it))
	}
	return sect("needs-you", "Needs you", len(d.NeedsYou), &EmptyAction{orgPath(d, "/goals"), fmt.Sprintf("All %d", len(d.NeedsYou))},
		El("ul", rows, Cls("lrow-list")))
}

func toneOf(v, capV float64) Zone {
	switch {
	case v >= capV:
		return Zone{"danger", "Over cap"}
	case v >= 0.8*capV:
		return Zone{"warning", "Near cap"}
	}
	return Zone{"success", "OK"}
}

func mbText(mb float64) string {
	if mb >= 1024 {
		return f1(mb/1024) + " GB"
	}
	return fmt.Sprintf("%.0f MB", mb)
}

// GaugeRows are the Agents, Memory and Load gauges (C9) of the Overview's System section.
func GaugeRows(s *SystemData) []any {
	var rows []any
	capF := float64(s.Cap)
	full := math.Max(capF, float64(s.Live)) * 1.25
	if full == 0 {
		full = 1
	}
	zone := Zone{"success", "OK"}
	if s.Live > s.Cap {
		zone = Zone{"danger", "Over cap"}
	} else if s.Live >= s.Cap {
		zone = Zone{"warning", "At cap"}
	}
	capt := "none queued"
	if s.Queued > 0 {
		capt = fmt.Sprintf("%d queued", s.Queued)
	}
	rows = append(rows, Gauge("Agents", fmt.Sprintf("%d / %d", s.Live, s.Cap), 100*float64(s.Live)/full, []float64{100 * capF / full}, zone, capt))
	floor := float64(s.MemFloor)
	if s.MemAvailable == nil {
		rows = append(rows, Gauge("Memory", "unknown", 0, nil, Zone{"neutral", "Unknown"}, "floor "+mbText(floor)))
	} else {
		mem := *s.MemAvailable
		full = math.Max(mem, 4*floor)
		if full == 0 {
			full = 1
		}
		zone = Zone{"success", "OK"}
		if mem < floor {
			zone = Zone{"danger", "Low"}
		} else if mem < 2*floor {
			zone = Zone{"warning", "Tight"}
		}
		rows = append(rows, Gauge("Memory", mbText(mem)+" free", 100*mem/full, []float64{100 * floor / full}, zone, "floor "+mbText(floor)))
	}
	if s.Load1 == nil || s.CPUs == 0 {
		rows = append(rows, Gauge("Load", "unknown", 0, nil, Zone{"neutral", "Unknown"}, ""))
	} else {
		cpus := float64(s.CPUs)
		full = math.Max(*s.Load1, cpus) * 1.25
		zone = Zone{"success", "OK"}
		if *s.Load1 > cpus {
			zone = Zone{"danger", "Overloaded"}
		} else if *s.Load1 > 0.75*cpus {
			zone = Zone{"warning", "Busy"}
		}
		rows = append(rows, Gauge("Load", fmt.Sprintf("%.1f / %d CPUs", *s.Load1, s.CPUs), 100**s.Load1/full, []float64{100 * cpus / full}, zone, "1-min average"))
	}
	return rows
}

func pctTxt(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", *v)
}

// UsageRow is the "Claude usage" row (the same label on every page): 5-hour and weekly bars stacked, one zone word for
// the worse one. Either window may be missing (nil): it shows n/a and the other still drives the bar and the word.
func UsageRow(u UsageData) Safe {
	p5, p7 := u.FiveHour, u.SevenDay
	if p5 == nil && p7 == nil {
		return Gauge("Claude usage", "unknown", 0, nil, Zone{"neutral", "Unknown"}, "5 h · week")
	}
	var zones []Zone
	if p5 != nil {
		zones = append(zones, toneOf(*p5, u.CapFive))
	}
	if p7 != nil {
		zones = append(zones, toneOf(*p7, u.CapSeven))
	}
	rank := map[string]int{"success": 0, "warning": 1, "danger": 2}
	worst := zones[0]
	for _, z := range zones[1:] {
		if rank[z.Tone] > rank[worst.Tone] {
			worst = z
		}
	}
	word := worst.Word
	if u.Stale {
		if word == "OK" {
			word = "Stale"
		} else {
			word += ", stale"
		}
	}
	var bars []any
	if p5 != nil {
		bars = append(bars, GaugeBar(*p5, []float64{u.CapFive}, zones[0].Tone, fmt.Sprintf("Claude usage 5-hour %.0f%%, cap %.0f%%", *p5, u.CapFive)))
	}
	if p7 != nil {
		bars = append(bars, GaugeBar(*p7, []float64{u.CapSeven}, zones[len(zones)-1].Tone, fmt.Sprintf("Claude usage weekly %.0f%%, cap %.0f%%", *p7, u.CapSeven)))
	}
	text := pctTxt(p5) + " · " + pctTxt(p7)
	return El("div", El("span", "Claude usage", Cls("gl")), El("div", bars, Cls("gauge-stack")),
		El("span", text, El("span", El("b", word, Cls("gz fg-"+worst.Tone)), " · 5 h · week"), Cls("gv")), Cls("gauge-row"))
}

// CursorRow is the Cursor usage gauge row: today's agent-hours and the plan line.
func CursorRow(d *tokens.DisplayData) Safe {
	plan := tokens.PlanLine(d)
	tone := "success"
	for _, m := range d.Models {
		if m.Status == "limited" {
			tone = "danger"
		}
	}
	value := fmtG(d.Hours) + " h"
	bar := GaugeBar(0, nil, tone, fmt.Sprintf("Cursor usage %s, %s", value, plan))
	return El("div", El("span", "Cursor usage", Cls("gl")), El("div", bar, Cls("gauge-stack")), El("span", value, El("span", plan), Cls("gv")), Cls("gauge-row"))
}

func systemSection(d *HomeData) Safe {
	s := d.System
	rows := GaugeRows(s)
	rows = append(rows, UsageRow(s.Usage), CursorRow(s.Cursor))
	ut := "Utilization appears after the next tick sample"
	if d.Utilization != nil {
		ut = d.Utilization.Line
		if d.Utilization.Under {
			ut += " (UNDER TARGET)"
		}
	}
	return sect("system", "System", nil, &EmptyAction{"/resources", "Resources"}, El("div", rows, Cls("system")), El("p", ut, Cls("summary-line")))
}

// GoalRowIn is what GoalRow reads of a goal.
type GoalRowIn struct {
	ID, Title, Status, Org     string
	Priority                   int64
	TasksDone, TasksTotal, PRs int64
}

// GoalRow is the level-1 goal row, shared by the overview tree and /goals: state glyph, id, title, priority chip, org
// tag, progress, criteria n/m, open PRs, extra, a red "needs you N" when the goal has an owner wait, last activity.
// With body (the level-2 list) the row is a <details> that is open when opened.
func GoalRow(g GoalRowIn, ctx *Ctx, crit *data.CriteriaCount, last string, needsN int, extra any, body any, opened bool) Safe {
	var prs any
	if g.PRs != 0 {
		s := "s"
		if g.PRs == 1 {
			s = ""
		}
		prs = El("span", fmt.Sprintf("%d open PR%s", g.PRs, s), Cls("gprs"))
	}
	var critEl, needsEl, actEl any
	if crit != nil {
		critEl = El("span", fmt.Sprintf("criteria %d/%d", crit[0], crit[1]), Cls("gprs"))
	}
	if needsN != 0 {
		needsEl = El("span", fmt.Sprintf("needs you %d", needsN), Cls("gneeds fg-danger"))
	}
	if a := Ago(last, ctx.Now, ""); a != "" {
		actEl = El("span", "last activity "+a, Cls("gact"))
	}
	title := El("span", El("a", At("href", "/goal/"+g.ID), g.Title), El("span", fmt.Sprintf("P%d", g.Priority), Cls("chip-p")), " ", OrgBadge(g.Org, ""), Cls("gtitle"))
	meta := El("div", Meter(g.TasksDone, g.TasksTotal, fmt.Sprintf("%d/%d tasks", g.TasksDone, g.TasksTotal), "tasks"), critEl, prs, extra, needsEl, actEl, Cls("gm"))
	head := []any{RowStatus(g.Status), El("span", g.ID, Cls("mono lrow-id")), title, meta}
	if body == nil {
		return El("li", append(head, Cls("goalrow"))...)
	}
	var op any
	if opened {
		op = true
	}
	return El("li", El("details", El("summary", El("div", append(head, Cls("gsum"))...)), body, Cls("disc gnode"), At("data-key", "ov-goal-"+g.ID), At("open", op)),
		Cls("goalrow goalrow--tree"))
}

func taskLi(t taskRow) Safe {
	var meta []any
	if t.Assignee != "" {
		meta = append(meta, El("span", t.Assignee))
	}
	if t.PRURL != "" {
		meta = append(meta, El("span", Link(t.PRURL, PRLabel(t.PRURL))))
	}
	if t.Rejections != 0 {
		meta = append(meta, El("span", plural(int(t.Rejections), "rejection")))
	}
	if t.Attempts != 0 {
		w := "tries"
		if t.Attempts == 1 {
			w = "try"
		}
		meta = append(meta, El("span", fmt.Sprintf("%d %s", t.Attempts, w)))
	}
	return El("li", RowStatus(t.Status), El("a", At("href", "/task/"+t.ID), Cls("mono lrow-id"), t.ID), El("span", t.Title, Cls("gtitle")),
		El("span", meta, Cls("tkmeta")), Cls("tkrow"))
}

var countLabel = map[string]string{"todo": "to do", "in_progress": "in progress", "in_review": "in review", "blocked": "blocked"}

// teamEl is level 2: team id, scope, lead and its status, running agents, tasks by status. A <details> only when it
// has level-3 rows; a team with none is a plain row.
func teamEl(goalID string, t teamNode) Safe {
	var lead any
	if t.Lead != "" {
		var st any = El("span", Glyph("exited"), "not running", Cls("st fg-neutral"))
		if t.LeadStatus != "" {
			st = Status(t.LeadStatus, false)
		}
		lead = El("span", "lead ", El("span", t.Lead, Cls("mono")), " ", st)
	}
	var cs []string
	for _, st := range counted {
		if t.Counts[st] != 0 {
			cs = append(cs, fmt.Sprintf("%d %s", t.Counts[st], countLabel[st]))
		}
	}
	cText := "no open tasks"
	if len(cs) > 0 {
		cText = strings.Join(cs, " · ")
	}
	var running any
	if t.Running != 0 {
		running = El("span", fmt.Sprintf("%d running", t.Running))
	}
	meta := El("div", lead, running, El("span", cText), Cls("tm"))
	idText := t.ID
	if idText == "" {
		idText = "no team"
	}
	var scope any
	if t.Scope != "" {
		scope = El("span", t.Scope, Cls("tscope"))
	}
	head := El("div", El("span", idText, Cls("mono lrow-id")), scope, meta, Cls("tsum"))
	if len(t.Tasks) == 0 && len(t.More) == 0 {
		return El("li", head, Cls("team"))
	}
	keyID := t.ID
	if keyID == "" {
		keyID = "none"
	}
	key := "ov-team-" + goalID + "-" + keyID
	var tk, more any
	if len(t.Tasks) > 0 {
		var ls []any
		for _, x := range t.Tasks {
			ls = append(ls, taskLi(x))
		}
		tk = El("ul", ls, Cls("tk"))
	}
	if len(t.More) > 0 {
		var ls []any
		for _, x := range t.More {
			ls = append(ls, taskLi(x))
		}
		more = El("details", El("summary", fmt.Sprintf("%d more", len(t.More))), El("ul", ls, Cls("tk")), Cls("disc"), At("data-key", key+"-more"))
	}
	var op any
	if t.OwnerWait {
		op = true
	}
	return El("li", El("details", El("summary", head), tk, more, Cls("disc"), At("data-key", key), At("open", op)), Cls("team"))
}

func goalsSection(d *HomeData, ctx *Ctx) Safe {
	more := &EmptyAction{orgPath(d, "/goals"), "All goals"}
	if len(d.Tree) == 0 {
		return sect("goals", "Goals", nil, more, EmptyState("No open goals", nil))
	}
	var rows []any
	for _, g := range d.Tree {
		var body any
		if len(g.Teams) > 0 {
			var ts []any
			for _, t := range g.Teams {
				ts = append(ts, teamEl(g.ID, t))
			}
			body = El("ul", ts, Cls("teams"))
		}
		in := GoalRowIn{ID: g.ID, Title: g.Title, Status: g.Status, Org: g.Org, Priority: g.Priority, TasksDone: g.TasksDone,
			TasksTotal: g.TasksTotal, PRs: g.OpenPRs}
		rows = append(rows, GoalRow(in, ctx, g.Criteria, g.LastActivity, g.Needs, nil, body, g.Needs != 0))
	}
	return sect("goals", "Goals", len(d.Tree), more, El("ul", rows, Cls("lrow-list")))
}

func agentsSectionEl(d *HomeData, ctx *Ctx) Safe {
	a := d.Agents
	more := &EmptyAction{orgPath(d, "/agents"), "All agents"}
	if a.Total == 0 {
		return sect("agents", "Agents running", nil, more, EmptyState("No agents running", nil))
	}
	workers := a.Running - a.Leads - a.HQ
	summary := fmt.Sprintf("%d running · %s · %s · %d HQ · %d queued", a.Running, plural(workers, "worker"), plural(a.Leads, "lead"), a.HQ, a.Queued)
	var rows []any
	for _, r := range a.Rows {
		var spans []any
		for i, c := range AgentRowCells(r, ctx.Now) {
			spans = append(spans, El("span", c, Cls(AgentCols[i].Cls)))
		}
		rows = append(rows, El("li", spans, Cls("agentrow")))
	}
	var moreLink any
	if m := a.Total - len(a.Rows); m > 0 {
		moreLink = El("a", fmt.Sprintf("%d more · All %d agents", m, a.Total), At("href", orgPath(d, "/agents")), Cls("more-link"))
	}
	return sect("agents", "Agents running", a.Running, more, El("p", summary, Cls("summary-line")), El("ul", rows, Cls("lrow-list")), moreLink)
}

func shippedSection(d *HomeData, ctx *Ctx) Safe {
	var pts []Point
	for _, p := range d.PerDay {
		pts = append(pts, Point{p.Day, float64(p.Merges)})
	}
	head := El("div", El("span", fmt.Sprintf("%d in %d days", d.ShippedSum, homeDays), Cls("sect-n")), Sparkline(pts, "merges", "per day"), Cls("ov-trend"))
	more := &EmptyAction{"/changelog", "Changelog"}
	if len(d.Recent) == 0 {
		return sect("shipped", "Shipped", nil, more, head, EmptyState(fmt.Sprintf("No merges in the last %d days", homeDays), nil))
	}
	var rows []any
	for _, m := range d.Recent {
		rows = append(rows, FeedRow(m.Title, "/task/"+m.TaskID, deref(m.GoalID), deref(m.PRURL), deref(m.SHA), Ago(m.Merged, ctx.Now, " ago"), m.Merged))
	}
	return sect("shipped", "Shipped", nil, more, head, Feed(rows, ""))
}

func medianText(sec *int64) (string, string) {
	if sec == nil {
		return "n/a", ""
	}
	if *sec >= 3600 {
		return f1(float64(*sec) / 3600), " h"
	}
	return fmt.Sprintf("%.0f", float64(*sec)/60), " min"
}

func qualitySection(d *HomeData) Safe {
	q := d.Quality
	rej, first := "n/a", "n/a"
	if q.RejPerMerged != nil {
		rej = fmt.Sprintf("%.2f", *q.RejPerMerged)
	}
	firstUnit := ""
	if q.FirstTryPct != nil {
		first, firstUnit = fmt.Sprint(*q.FirstTryPct), "%"
	}
	med, medUnit := medianText(q.MedianSpawnS)
	tiles := El("div",
		Tile("Rejections per merged PR", rej, TileOpts{Definition: "Rejections (failed review, QA or judge) divided by merged PRs, all time. Lower is better."}),
		Tile("First-try merge rate", first, TileOpts{Unit: firstUnit, Definition: "Share of merged PRs that were never rejected. Higher is better."}),
		Tile("Median spawn to merge", med, TileOpts{Unit: medUnit, Definition: fmt.Sprintf("Median time from a task's first agent start to its merge, last %d days.", homeDays)}),
		Cls("tiles"))
	var bounceEl Safe
	if len(q.Bouncing) > 0 {
		var rows []any
		for i, b := range q.Bouncing {
			if i >= bouncingMax {
				break
			}
			rows = append(rows, ListRow("in_review", b.TaskID, b.Title, "/task/"+b.TaskID, plural(int(b.Rejections), "rejection"), fmt.Sprintf("%d tries", b.Attempts)))
		}
		var count any = len(q.Bouncing)
		if len(q.Bouncing)-bouncingMax > 0 {
			count = fmt.Sprintf("%d+", bouncingMax)
		}
		bounceEl = El("div", El("h3", "Bouncing now ", El("span", count, Cls("sect-n"))), El("ul", rows, Cls("lrow-list")), Cls("ov-bounce"))
	} else {
		bounceEl = El("div", El("h3", "Bouncing now"), EmptyState("Nothing is bouncing", nil), Cls("ov-bounce"))
	}
	return sect("quality", "Quality", nil, &EmptyAction{"/quality", "Details"}, tiles, bounceEl)
}

// RenderHome renders the Overview body.
func RenderHome(ctx *Ctx, d *HomeData) Safe {
	return El("div", lanesSection(d), needsSection(d), goalsSection(d, ctx), systemSection(d), agentsSectionEl(d, ctx),
		shippedSection(d, ctx), qualitySection(d), Cls("ov"))
}

type homeView struct{}

// Render implements View for "/".
func (homeView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadHome(ctx)
	if err != nil {
		return "", "", err
	}
	return titleOverview, RenderHome(ctx, d), nil
}
