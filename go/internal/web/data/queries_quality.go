package data

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QualityRow is one team's (or the overall) quality numbers.
type QualityRow struct {
	TeamID               *string `json:"team_id"`
	Tasks                int64   `json:"tasks"`
	Merged               int64   `json:"merged"`
	Rejections           int64   `json:"rejections"`
	RejReviewer          int64   `json:"rej_reviewer"`
	RejQA                int64   `json:"rej_qa"`
	RejJudge             int64   `json:"rej_judge"`
	GateJudgeBlocked     int64   `json:"gate_judge_blocked"`
	GateMergeFailed      int64   `json:"gate_merge_failed"`
	GateRefusals         int64   `json:"gate_refusals"`
	MergedFirstTry       int64   `json:"merged_first_try"`
	MergedAfterRejection int64   `json:"merged_after_rejection"`
	RejectionsPerMerged  *Num    `json:"rejections_per_merged"`
	Retries              int64   `json:"retries"`
	Score                *int64  `json:"score"`
	CIBlocked            int64   `json:"ci_blocked"`
	CIPassRate           *Num    `json:"ci_pass_rate"`
}

// QualityResult is queries.quality's result.
type QualityResult struct {
	Teams             []QualityRow      `json:"teams"`
	Overall           QualityRow        `json:"overall"`
	Unassigned        *OMap[QualityRow] `json:"unassigned"`
	RejectionsByCause *OMap[int64]      `json:"rejections_by_cause"`
}

type taskInfo struct {
	id, goal string
	team     *string
	attempts int64
}

// Quality is queries.quality: per team and overall quality numbers (merged = distinct tasks with a merged event;
// rejections = fail/reject verdict rows plus judge-reject events with no judge reject verdict row).
func (c *Conn) Quality(goals GoalSet) (*QualityResult, error) {
	var tinfoOrder []taskInfo
	tinfo := map[string]taskInfo{}
	err := c.each("SELECT id, team_id, goal_id, attempts FROM tasks", nil, func(r rowScanner) error {
		var t taskInfo
		var team sql.NullString
		var att sql.NullInt64
		if err := r.Scan(&t.id, &team, &t.goal, &att); err != nil {
			return err
		}
		t.team, t.attempts = nsp(team), att.Int64
		if goals != nil && !goals[t.goal] {
			return nil
		}
		tinfo[t.id] = t
		tinfoOrder = append(tinfoOrder, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	err = c.each("SELECT DISTINCT entity_id FROM events WHERE entity='task' AND action='merged'", nil, func(r rowScanner) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		if _, ok := tinfo[id]; goals == nil || ok {
			merged[id] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	src := map[string]map[string]int64{}
	err = c.each("SELECT task_id, role FROM verdicts WHERE verdict IN ('fail','reject')", nil, func(r rowScanner) error {
		var id, role string
		if err := r.Scan(&id, &role); err != nil {
			return err
		}
		if src[id] == nil {
			src[id] = map[string]int64{}
		}
		src[id][role]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	judgeE := map[string]int64{}
	err = c.each("SELECT entity_id FROM events WHERE entity='task' AND action='judge-reject'", nil, func(r rowScanner) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		judgeE[id]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	for tid, n := range judgeE {
		if src[tid] == nil {
			src[tid] = map[string]int64{}
		}
		d := src[tid]
		if extra := n - d["judge"]; extra > 0 {
			d["judge"] += extra
		}
	}
	rej := map[string]int64{}
	for tid, d := range src {
		for _, n := range d {
			rej[tid] += n
		}
	}
	gate := map[string]map[string]int64{}
	err = c.each("SELECT entity_id, action FROM events WHERE entity='task' AND action IN ('judge-blocked','merge-failed')", nil, func(r rowScanner) error {
		var id, a string
		if err := r.Scan(&id, &a); err != nil {
			return err
		}
		if gate[id] == nil {
			gate[id] = map[string]int64{}
		}
		gate[id][a]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	ciEvents := map[string]int64{}
	err = c.each("SELECT entity_id FROM events WHERE entity='task' AND action='judge-blocked' AND detail LIKE '%checks not green%'", nil, func(r rowScanner) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		ciEvents[id]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	score := map[string]int64{}
	err = c.each("SELECT team_id, score FROM evaluations ORDER BY id", nil, func(r rowScanner) error {
		var id string
		var s int64
		if err := r.Scan(&id, &s); err != nil {
			return err
		}
		score[id] = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	row := func(team *string, ids []string) QualityRow {
		var m, first, rj, gb, gf, retries, ciB int64
		var rv, rq, rjd int64
		blocked := 0
		for _, i := range ids {
			if merged[i] {
				m++
				if rej[i] == 0 {
					first++
				}
			}
			rj += rej[i]
			rv += src[i]["reviewer"]
			rq += src[i]["qa"]
			rjd += src[i]["judge"]
			gb += gate[i]["judge-blocked"]
			gf += gate[i]["merge-failed"]
			if a := tinfo[i].attempts - 1; a > 0 {
				retries += a
			}
			if n, ok := ciEvents[i]; ok {
				blocked++
				ciB += n
			}
		}
		q := QualityRow{TeamID: team, Tasks: int64(len(ids)), Merged: m, Rejections: rj, RejReviewer: rv, RejQA: rq, RejJudge: rjd,
			GateJudgeBlocked: gb, GateMergeFailed: gf, GateRefusals: gb + gf, MergedFirstTry: first, MergedAfterRejection: m - first,
			Retries: retries, CIBlocked: ciB}
		if m > 0 {
			q.RejectionsPerMerged = FloatP(pyRoundN(float64(rj)/float64(m), 2))
		}
		if team != nil {
			if s, ok := score[*team]; ok {
				q.Score = &s
			}
		}
		if m+int64(blocked) > 0 {
			q.CIPassRate = FloatP(pyRoundN(float64(m)/float64(m+int64(blocked)), 3))
		}
		return q
	}
	byTeam := map[string][]string{}
	byGoal := NewOMap[[]string]()
	var all []string
	for _, t := range tinfoOrder {
		all = append(all, t.id)
		if t.team != nil {
			byTeam[*t.team] = append(byTeam[*t.team], t.id)
		} else {
			cur, _ := byGoal.Get(t.goal)
			byGoal.Set(t.goal, append(cur, t.id))
		}
	}
	teamSet := map[string]bool{}
	err = c.each("SELECT id FROM teams", nil, func(r rowScanner) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		teamSet[id] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	for t := range byTeam {
		if t != "" {
			teamSet[t] = true
		}
	}
	var teamIDs []string
	for t := range teamSet {
		teamIDs = append(teamIDs, t)
	}
	sort.Strings(teamIDs)
	res := &QualityResult{Teams: []QualityRow{}, Unassigned: NewOMap[QualityRow](), RejectionsByCause: NewOMap[int64]()}
	for _, t := range teamIDs {
		t := t
		res.Teams = append(res.Teams, row(&t, byTeam[t]))
	}
	res.Overall = row(nil, all)
	for _, g := range byGoal.Keys {
		res.Unassigned.Set(g, row(nil, byGoal.M[g]))
	}
	tgoal, err := c.taskGoals()
	if err != nil {
		return nil, err
	}
	err = c.each("SELECT cause, task_id FROM rejection_incidents", nil, func(r rowScanner) error {
		var cause, tid string
		if err := r.Scan(&cause, &tid); err != nil {
			return err
		}
		if goals != nil && !goals[tgoal[tid]] {
			return nil
		}
		n, _ := res.RejectionsByCause.Get(cause)
		res.RejectionsByCause.Set(cause, n+1)
		return nil
	})
	return res, err
}

// ChangeItem is one merged task of the changelog.
type ChangeItem struct {
	TaskID string  `json:"task_id"`
	Title  string  `json:"title"`
	PRURL  *string `json:"pr_url"`
	Merged string  `json:"merged"`
	SHA    *string `json:"sha"`
}

// ChangeGroup is merged tasks of one goal.
type ChangeGroup struct {
	GoalID    string       `json:"goal_id"`
	GoalTitle *string      `json:"goal_title"`
	Items     []ChangeItem `json:"items"`
}

func shaOf(detail string) *string {
	return nz(strings.TrimSpace(strings.TrimLeft(detail, "@")))
}

// Changelog is queries.changelog: merged tasks grouped by goal.
func (c *Conn) Changelog(goals GoalSet) ([]ChangeGroup, error) {
	type lastEv struct{ ts, detail string }
	last := map[string]lastEv{}
	err := c.each("SELECT id, ts, entity_id, detail FROM events WHERE entity='task' AND action='merged' ORDER BY id", nil, func(r rowScanner) error {
		var id int64
		var ts, eid string
		var d sql.NullString
		if err := r.Scan(&id, &ts, &eid, &d); err != nil {
			return err
		}
		last[eid] = lastEv{ts, d.String}
		return nil
	})
	if err != nil {
		return nil, err
	}
	groups := map[string]*ChangeGroup{}
	err = c.each("SELECT t.id, t.title, t.pr_url, t.goal_id, g.title gtitle FROM tasks t LEFT JOIN goals g ON g.id=t.goal_id ORDER BY t.goal_id, t.id", nil, func(r rowScanner) error {
		var id, title, goal string
		var pr, gt sql.NullString
		if err := r.Scan(&id, &title, &pr, &goal, &gt); err != nil {
			return err
		}
		if goals != nil && !goals[goal] {
			return nil
		}
		ev, ok := last[id]
		if !ok {
			return nil
		}
		g := groups[goal]
		if g == nil {
			g = &ChangeGroup{GoalID: goal, GoalTitle: nsp(gt), Items: []ChangeItem{}}
			groups[goal] = g
		}
		g.Items = append(g.Items, ChangeItem{id, stripTags(title), nsp(pr), ev.ts, shaOf(ev.detail)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []ChangeGroup{}
	for _, k := range keys {
		g := groups[k]
		sort.SliceStable(g.Items, func(i, j int) bool {
			a, _ := epoch(g.Items[i].Merged)
			b, _ := epoch(g.Items[j].Merged)
			return a > b
		})
		out = append(out, *g)
	}
	return out, nil
}

// QueuedAgent is a queued agent with the reason it waits.
type QueuedAgent struct {
	Name     string  `json:"name"`
	Role     string  `json:"role"`
	QueuedAt *string `json:"queued_at"`
	Reason   *string `json:"reason"`
}

// ResEvent is a recent queued/config event.
type ResEvent struct {
	ID       int64   `json:"id"`
	TS       string  `json:"ts"`
	Actor    *string `json:"actor"`
	EntityID *string `json:"entity_id"`
	Detail   *string `json:"detail"`
}

// Default is one cap default (key and its number), in the order callers list them.
type Default struct {
	Key   string
	Value Num
}

// Cap is a cap value and where it came from (config or default). Value is a config string or a default Num.
type Cap struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// ResourcesResult is queries.resources_db's result.
type ResourcesResult struct {
	Live         int64         `json:"live"`
	QueuedCount  int           `json:"queued_count"`
	Queued       []QueuedAgent `json:"queued"`
	Caps         *OMap[Cap]    `json:"caps"`
	QueuedEvents []ResEvent    `json:"queued_events"`
	ConfigEvents []ResEvent    `json:"config_events"`
}

// ResourcesDB is queries.resources_db: live and queued counts, queue reasons, caps (config over defaults), recent events.
func (c *Conn) ResourcesDB(defaults []Default) (*ResourcesResult, error) {
	res := &ResourcesResult{Queued: []QueuedAgent{}, Caps: NewOMap[Cap]()}
	if err := c.QueryRow("SELECT COUNT(*) FROM agents WHERE status='live'").Scan(&res.Live); err != nil {
		return nil, err
	}
	err := c.each("SELECT name, role, queued_at FROM agents WHERE status='queued' ORDER BY queued_at, name", nil, func(r rowScanner) error {
		var q QueuedAgent
		var at sql.NullString
		if err := r.Scan(&q.Name, &q.Role, &at); err != nil {
			return err
		}
		q.QueuedAt = nsp(at)
		res.Queued = append(res.Queued, q)
		return nil
	})
	if err != nil {
		return nil, err
	}
	reasons := map[string]*string{}
	err = c.each("SELECT entity_id, detail FROM events WHERE entity='agent' AND action='queued' ORDER BY id DESC LIMIT 500", nil, func(r rowScanner) error {
		var id, d sql.NullString
		if err := r.Scan(&id, &d); err != nil {
			return err
		}
		if _, ok := reasons[id.String]; !ok {
			reasons[id.String] = truncP(nsp(d), DetailMax)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range res.Queued {
		res.Queued[i].Reason = reasons[res.Queued[i].Name]
	}
	res.QueuedCount = len(res.Queued)
	cfg := map[string]string{}
	err = c.each("SELECT key, value FROM config", nil, func(r rowScanner) error {
		var k, v string
		if err := r.Scan(&k, &v); err != nil {
			return err
		}
		cfg[k] = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range defaults {
		if v, ok := cfg[d.Key]; ok {
			res.Caps.Set(d.Key, Cap{v, "config"})
		} else {
			res.Caps.Set(d.Key, Cap{d.Value, "default"})
		}
	}
	recent := func(entity, action string, tr bool) ([]ResEvent, error) {
		out := []ResEvent{}
		err := c.each("SELECT id, ts, actor, entity_id, detail FROM events WHERE entity=? AND action=? ORDER BY id DESC LIMIT 50", []any{entity, action}, func(r rowScanner) error {
			var e ResEvent
			var actor, eid, d sql.NullString
			if err := r.Scan(&e.ID, &e.TS, &actor, &eid, &d); err != nil {
				return err
			}
			e.Actor, e.EntityID, e.Detail = nsp(actor), nsp(eid), nsp(d)
			if tr {
				e.Detail = truncP(e.Detail, DetailMax)
			}
			out = append(out, e)
			return nil
		})
		return out, err
	}
	if res.QueuedEvents, err = recent("agent", "queued", true); err != nil {
		return nil, err
	}
	if res.ConfigEvents, err = recent("config", "set", false); err != nil {
		return nil, err
	}
	return res, nil
}

// Usage is the usage_fn() dict shell_status reads (nil when unknown).
type Usage struct {
	Unreadable bool
	FiveHour   *float64
	SevenDay   *float64
	Fresh      bool
	AgeMin     float64
}

// Health is the global status pill.
type Health struct {
	Tone string `json:"tone"`
	Text string `json:"text"`
}

// CursorFunc builds the Cursor panel data of shell_status (web/cursor_limits.display); it lives with the views.
type CursorFunc func(c *Conn, now time.Time) (any, error)

// Shell is queries.shell_status's result.
type Shell struct {
	Needs           int    `json:"needs"`
	AgentsAttention int64  `json:"agents_attention"`
	Health          Health `json:"health"`
	Cursor          any    `json:"cursor"`
}

func cfgNum(cfg map[string]string, defaults []Default, key string, fallback float64) float64 {
	v, ok := cfg[key]
	if !ok {
		for _, d := range defaults {
			if d.Key == key {
				return d.Value.V
			}
		}
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return fallback
	}
	return f
}

// ShellStatus is queries.shell_status: data for the global status header and nav badges. mem is MB available (nil
// when unknown). Health, worst first: memory low, usage cap reached, gate closed, usage high, usage unknown, normal.
func (c *Conn) ShellStatus(usage *Usage, defaults []Default, mem *float64, now time.Time, cursor CursorFunc) (*Shell, error) {
	pend, err := c.OwnerPending()
	if err != nil {
		return nil, err
	}
	n := map[string]int64{}
	err = c.each("SELECT status, COUNT(*) FROM agents GROUP BY status", nil, func(r rowScanner) error {
		var s string
		var k int64
		if err := r.Scan(&s, &k); err != nil {
			return err
		}
		n[s] = k
		return nil
	})
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	err = c.each("SELECT key, value FROM config", nil, func(r rowScanner) error {
		var k, v string
		if err := r.Scan(&k, &v); err != nil {
			return err
		}
		cfg[k] = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	queued := n["queued"]
	cp := int64(cfgNum(cfg, defaults, "max_live_agents", 6))
	cap5 := cfgNum(cfg, defaults, "max_five_hour_pct", 92)
	cap7 := cfgNum(cfg, defaults, "max_seven_day_pct", 92)
	fresh := usage != nil && !usage.Unreadable && (usage.FiveHour != nil || usage.SevenDay != nil)
	stale := fresh && (!usage.Fresh || usage.AgeMin > 30)
	var p5, p7 *float64
	if fresh {
		p5, p7 = usage.FiveHour, usage.SevenDay
	}
	ok := fresh && !stale
	var health Health
	switch {
	case mem != nil && *mem < cfgNum(cfg, defaults, "min_mem_available_mb", 1024):
		health = Health{"danger", "Memory low"}
	case ok && ((p5 != nil && *p5 >= cap5) || (p7 != nil && *p7 >= cap7)):
		health = Health{"danger", "Usage cap reached"}
	case n["live"] >= cp:
		t := "Gate closed"
		if queued > 0 {
			t = "Gate closed · " + strconv.FormatInt(queued, 10) + " queued"
		}
		health = Health{"warning", t}
	case ok && p5 != nil && *p5 >= 0.8*cap5:
		health = Health{"warning", "Claude usage " + strconv.FormatFloat(*p5, 'f', 0, 64) + "%"}
	case ok && p7 != nil && *p7 >= 0.8*cap7:
		health = Health{"warning", "Weekly usage " + strconv.FormatFloat(*p7, 'f', 0, 64) + "%"}
	case !fresh || stale:
		health = Health{"neutral", "Usage unknown"}
	default:
		health = Health{"success", "All normal"}
	}
	sh := &Shell{Needs: pend.Count(), AgentsAttention: queued + n["blocked"], Health: health}
	if cursor != nil {
		if sh.Cursor, err = cursor(c, nowOr(now)); err != nil {
			return nil, err
		}
	}
	return sh, nil
}

// LastEventByAgent is queries.last_event_by_agent: {agent name: ts of its newest event} for live and queued agents.
func (c *Conn) LastEventByAgent() (*OMap[string], error) {
	out := NewOMap[string]()
	err := c.each("SELECT actor, MAX(ts) FROM events WHERE actor IN (SELECT name FROM agents WHERE status IN ('live','queued')) GROUP BY actor", nil, func(r rowScanner) error {
		var a, ts string
		if err := r.Scan(&a, &ts); err != nil {
			return err
		}
		out.Set(a, ts)
		return nil
	})
	return out, err
}

// LastActivityByGoal is queries.last_activity_by_goal: {goal id: ts of the newest event on the goal or one of its tasks}.
func (c *Conn) LastActivityByGoal() (*OMap[string], error) {
	out := NewOMap[string]()
	collect := func(q string) error {
		return c.each(q, nil, func(r rowScanner) error {
			var gid, ts sql.NullString
			if err := r.Scan(&gid, &ts); err != nil {
				return err
			}
			if gid.String == "" || ts.String == "" {
				return nil
			}
			cur, has := out.Get(gid.String)
			e, _ := epoch(ts.String)
			ce := 0.0
			if has {
				ce, _ = epoch(cur)
			}
			if e >= ce {
				out.Set(gid.String, ts.String)
			}
			return nil
		})
	}
	if err := collect("SELECT t.goal_id, MAX(e.ts) FROM events e JOIN tasks t ON t.id=e.entity_id WHERE e.entity='task' GROUP BY t.goal_id"); err != nil {
		return nil, err
	}
	if err := collect("SELECT entity_id, MAX(ts) FROM events WHERE entity='goal' GROUP BY entity_id"); err != nil {
		return nil, err
	}
	return out, nil
}

// RecentMerge is one entry of RecentMerges.
type RecentMerge struct {
	TaskID string  `json:"task_id"`
	GoalID *string `json:"goal_id"`
	Title  string  `json:"title"`
	PRURL  *string `json:"pr_url"`
	Merged string  `json:"merged"`
	SHA    *string `json:"sha"`
}

// RecentMerges is queries.recent_merges: the newest limit merged tasks, newest first; a task merged twice counts once.
func (c *Conn) RecentMerges(limit int, goals GoalSet) ([]RecentMerge, error) {
	seen := map[string]bool{}
	out := []RecentMerge{}
	err := c.each("SELECT e.entity_id, e.ts, e.detail, t.goal_id, t.title, t.pr_url FROM events e "+
		"LEFT JOIN tasks t ON t.id=e.entity_id WHERE e.entity='task' AND e.action='merged' ORDER BY e.id DESC", nil, func(r rowScanner) error {
		var id, ts string
		var d, goal, title, pr sql.NullString
		if err := r.Scan(&id, &ts, &d, &goal, &title, &pr); err != nil {
			return err
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		if goals != nil && !(goal.Valid && goals[goal.String]) {
			return nil
		}
		t := title.String
		if t == "" {
			t = id
		}
		out = append(out, RecentMerge{id, nsp(goal), stripTags(t), nsp(pr), ts, shaOf(d.String)})
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

// Bouncing is one entry of BouncingTasks.
type Bouncing struct {
	TaskID     string `json:"task_id"`
	GoalID     string `json:"goal_id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Attempts   int64  `json:"attempts"`
	Rejections int64  `json:"rejections"`
}

// BouncingTasks is queries.bouncing_tasks: open tasks with at least one rejection, most rejections first.
func (c *Conn) BouncingTasks(limit int, goals GoalSet) ([]Bouncing, error) {
	rej := map[string]map[string]int64{}
	err := c.each("SELECT v.task_id, v.role FROM verdicts v JOIN tasks t ON t.id=v.task_id "+
		"WHERE v.verdict IN ('fail','reject') AND t.status NOT IN ('done','cancelled')", nil, func(r rowScanner) error {
		var id, role string
		if err := r.Scan(&id, &role); err != nil {
			return err
		}
		if rej[id] == nil {
			rej[id] = map[string]int64{}
		}
		rej[id][role]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = c.each("SELECT e.entity_id, COUNT(*) n FROM events e JOIN tasks t ON t.id=e.entity_id "+
		"WHERE e.entity='task' AND e.action='judge-reject' AND t.status NOT IN ('done','cancelled') GROUP BY e.entity_id", nil, func(r rowScanner) error {
		var id string
		var n int64
		if err := r.Scan(&id, &n); err != nil {
			return err
		}
		if rej[id] == nil {
			rej[id] = map[string]int64{}
		}
		if extra := n - rej[id]["judge"]; extra > 0 {
			rej[id]["judge"] += extra
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	var ids []string
	for id, d := range rej {
		var s int64
		for _, n := range d {
			s += n
		}
		if s > 0 {
			counts[id] = s
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	type meta struct {
		goal, title, status string
		attempts            int64
	}
	metas := map[string]meta{}
	err = c.inRows("SELECT id, goal_id, title, status, attempts FROM tasks WHERE id IN ({in})", ids, func(r rowScanner) error {
		var id string
		var m meta
		var att sql.NullInt64
		if err := r.Scan(&id, &m.goal, &m.title, &m.status, &att); err != nil {
			return err
		}
		m.attempts = att.Int64
		metas[id] = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []Bouncing{}
	for _, id := range ids {
		if m, ok := metas[id]; ok && (goals == nil || goals[m.goal]) {
			out = append(out, Bouncing{id, m.goal, stripTags(m.title), m.status, m.attempts, counts[id]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rejections != out[j].Rejections {
			return out[i].Rejections > out[j].Rejections
		}
		return out[i].TaskID < out[j].TaskID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// WindowStats is one window of QualityWindows.
type WindowStats struct {
	Merged              int64  `json:"merged"`
	Rejections          int64  `json:"rejections"`
	RejectionsPerMerged *Num   `json:"rejections_per_merged"`
	FirstTryPct         *int64 `json:"first_try_pct"`
	CIBlocked           int64  `json:"ci_blocked"`
	MedianAttempts      *Num   `json:"median_attempts"`
}

// QualityWindowsAll is the "all" entry of QualityWindows.
type QualityWindowsAll struct {
	MedianAttempts *Num `json:"median_attempts"`
}

// QualityWindowsResult is queries.quality_windows's result.
type QualityWindowsResult struct {
	Days     int               `json:"days"`
	All      QualityWindowsAll `json:"all"`
	Current  WindowStats       `json:"current"`
	Previous WindowStats       `json:"previous"`
}

type rejAt struct {
	task string
	e    float64
	ok   bool
}

// QualityWindows is queries.quality_windows: quality numbers for the last days days and the days before them.
func (c *Conn) QualityWindows(days int, now time.Time, goals GoalSet) (*QualityWindowsResult, error) {
	end := tsec(nowOr(now))
	d := float64(days) * 86400
	type win struct{ lo, hi float64 }
	cur, prev := win{end - d, end + 1}, win{end - 2*d, end - d}
	tgoal, err := c.taskGoals()
	if err != nil {
		return nil, err
	}
	in := func(id string) bool {
		if goals == nil {
			return true
		}
		g, ok := tgoal[id]
		return ok && goals[g]
	}
	merged := map[string]float64{}
	var mergedOrder []string
	err = c.each("SELECT entity_id, ts FROM events WHERE entity='task' AND action='merged' ORDER BY id", nil, func(r rowScanner) error {
		var id, ts string
		if err := r.Scan(&id, &ts); err != nil {
			return err
		}
		if !in(id) {
			return nil
		}
		if e, ok := epoch(ts); ok {
			if _, had := merged[id]; !had {
				mergedOrder = append(mergedOrder, id)
			}
			merged[id] = e
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var rej []rejAt
	judgeRows := map[string]int{}
	err = c.each("SELECT task_id, role, created FROM verdicts WHERE verdict IN ('fail','reject')", nil, func(r rowScanner) error {
		var id, role, created string
		if err := r.Scan(&id, &role, &created); err != nil {
			return err
		}
		if !in(id) {
			return nil
		}
		e, ok := epoch(created)
		rej = append(rej, rejAt{id, e, ok})
		if role == "judge" {
			judgeRows[id]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	byTask := map[string][]rejAt{}
	var byTaskOrder []string
	err = c.each("SELECT entity_id, ts FROM events WHERE entity='task' AND action='judge-reject' ORDER BY id", nil, func(r rowScanner) error {
		var id, ts string
		if err := r.Scan(&id, &ts); err != nil {
			return err
		}
		if !in(id) {
			return nil
		}
		e, ok := epoch(ts)
		if _, had := byTask[id]; !had {
			byTaskOrder = append(byTaskOrder, id)
		}
		byTask[id] = append(byTask[id], rejAt{id, e, ok})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range byTaskOrder {
		es := byTask[id]
		extra := len(es) - judgeRows[id]
		if extra < 0 {
			extra = 0
		}
		rej = append(rej, es[len(es)-extra:]...)
	}
	rejected := map[string]bool{}
	for _, r := range rej {
		rejected[r.task] = true
	}
	var ci []rejAt
	err = c.each("SELECT entity_id, ts FROM events WHERE entity='task' AND action='judge-blocked' AND detail LIKE '%checks not green%'", nil, func(r rowScanner) error {
		var id, ts string
		if err := r.Scan(&id, &ts); err != nil {
			return err
		}
		if !in(id) {
			return nil
		}
		e, ok := epoch(ts)
		ci = append(ci, rejAt{id, e, ok})
		return nil
	})
	if err != nil {
		return nil, err
	}
	attempts := map[string]int64{}
	err = c.each("SELECT id, attempts FROM tasks", nil, func(r rowScanner) error {
		var id string
		var a sql.NullInt64
		if err := r.Scan(&id, &a); err != nil {
			return err
		}
		attempts[id] = a.Int64
		return nil
	})
	if err != nil {
		return nil, err
	}
	var every []int64
	for _, id := range mergedOrder {
		if a, ok := attempts[id]; ok {
			every = append(every, a)
		}
	}
	res := &QualityWindowsResult{Days: days}
	if m, ok := median(every); ok {
		res.All.MedianAttempts = NumP(m)
	}
	calc := func(w win) WindowStats {
		var ids []string
		for _, id := range mergedOrder {
			if e := merged[id]; w.lo <= e && e < w.hi {
				ids = append(ids, id)
			}
		}
		var s WindowStats
		for _, r := range rej {
			if r.ok && w.lo <= r.e && r.e < w.hi {
				s.Rejections++
			}
		}
		first := int64(0)
		var att []int64
		for _, id := range ids {
			if !rejected[id] {
				first++
			}
			if a, ok := attempts[id]; ok {
				att = append(att, a)
			}
		}
		for _, r := range ci {
			if r.ok && w.lo <= r.e && r.e < w.hi {
				s.CIBlocked++
			}
		}
		s.Merged = int64(len(ids))
		if len(ids) > 0 {
			s.RejectionsPerMerged = FloatP(pyRoundN(float64(s.Rejections)/float64(len(ids)), 2))
			p := pyRound(100 * float64(first) / float64(len(ids)))
			s.FirstTryPct = &p
		}
		if m, ok := median(att); ok {
			s.MedianAttempts = NumP(m)
		}
		return s
	}
	res.Current, res.Previous = calc(cur), calc(prev)
	return res, nil
}

// TableSort is queries.table_sort: the effective (column key, "asc"|"desc") for a sortable table from ?sort= and
// ?dir=. columns maps each sortable key to its default direction; unknown values fall back to def.
func TableSort(query map[string]string, columns map[string]string, def [2]string) (string, string) {
	key := query["sort"]
	dir0, ok := columns[key]
	if !ok {
		return def[0], def[1]
	}
	if d := query["dir"]; d == "asc" || d == "desc" {
		return key, d
	}
	return key, dir0
}

// SortRows is queries.sort_rows: rows ordered by getters[key](row); rows whose value is nil go last in either
// direction (stable). Values must be string, int, int64 or float64 and of one type per key.
func SortRows[T any](rows []T, key, direction string, getters map[string]func(T) any) []T {
	get := getters[key]
	var have, miss []T
	for _, r := range rows {
		if get(r) != nil {
			have = append(have, r)
		} else {
			miss = append(miss, r)
		}
	}
	less := func(a, b any) bool {
		switch x := a.(type) {
		case string:
			return x < b.(string)
		case int:
			return x < b.(int)
		case int64:
			return x < b.(int64)
		case float64:
			return x < b.(float64)
		}
		return false
	}
	sort.SliceStable(have, func(i, j int) bool {
		if direction == "desc" {
			return less(get(have[j]), get(have[i]))
		}
		return less(get(have[i]), get(have[j]))
	})
	return append(have, miss...)
}
