package data

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	tagRe = regexp.MustCompile(`\[(SC\d+(?:\s*,\s*SC\d+)*)\]`)
	numRe = regexp.MustCompile(`^\s*(\d+)[.)]\s*(.*)$`)
	digRe = regexp.MustCompile(`\d+`)
	wsRe  = regexp.MustCompile(`\s+`)
)

// tags is queries._tags: criterion numbers from [SCn] / [SC1,SC3] tags in a task title.
func tags(title string) map[int64]bool {
	out := map[int64]bool{}
	for _, m := range tagRe.FindAllStringSubmatch(title, -1) {
		for _, d := range digRe.FindAllString(m[1], -1) {
			n, _ := strconv.ParseInt(d, 10, 64)
			out[n] = true
		}
	}
	return out
}

// stripTags is queries._strip_tags.
func stripTags(title string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(tagRe.ReplaceAllString(title, " "), " "))
}

// Criterion is one parsed success criterion.
type CriterionText struct {
	N    int64
	Text string
}

// ParseCriteria is queries.parse_criteria: items start on lines like "1." or "2)"; fallback one per line.
func ParseCriteria(text string) []CriterionText {
	var lines []string
	for _, l := range pySplitLines(text) {
		lines = append(lines, strings.TrimRightFunc(l, pyIsSpace))
	}
	var items []CriterionText
	for _, l := range lines {
		if m := numRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			items = append(items, CriterionText{n, strings.TrimSpace(m[2])})
		} else if len(items) > 0 && strings.TrimSpace(l) != "" {
			last := &items[len(items)-1]
			last.Text = strings.TrimSpace(last.Text + " " + strings.TrimSpace(l))
		}
	}
	if len(items) > 0 {
		return items
	}
	i := int64(0)
	var out []CriterionText
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			i++
			out = append(out, CriterionText{i, strings.TrimSpace(l)})
		}
	}
	return out
}

func pyIsSpace(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r') || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x1f || r == 0x85 || r == 0xa0 || r == 0x2028 || r == 0x2029
}

// pySplitLines is str.splitlines for the common separators.
func pySplitLines(s string) []string {
	var out []string
	cur := []rune{}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(cur))
			cur = cur[:0]
		case '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			out = append(out, string(cur))
			cur = cur[:0]
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// dependsOn is queries._depends: a JSON list of task ids; anything else is empty.
func dependsOn(raw sql.NullString) []string {
	out := []string{}
	if !raw.Valid || raw.String == "" {
		return out
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(raw.String))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return out
	}
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, x := range list {
		out = append(out, pyStr(x))
	}
	return out
}

// Goal is one row of queries.goals().
type Goal struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Status            string   `json:"status"`
	Priority          int64    `json:"priority"`
	ProjectID         *string  `json:"project_id"`
	Created           string   `json:"created"`
	Updated           string   `json:"updated"`
	Org               string   `json:"org"`
	TasksTotal        int64    `json:"tasks_total"`
	TasksDone         int64    `json:"tasks_done"`
	TasksBlocked      int64    `json:"tasks_blocked"`
	TasksBlockedOwner int64    `json:"tasks_blocked_owner"`
	OpenPRs           int64    `json:"open_prs"`
	Teams             []string `json:"teams"`
}

// Goals is queries.goals: one row per goal with task counts (cancelled included) and team ids.
func (c *Conn) Goals() ([]Goal, error) {
	hasWaits, err := c.HasColumn("tasks", "waits")
	if err != nil {
		return nil, err
	}
	blockedOwner := "0 blocked_owner, "
	if hasWaits {
		blockedOwner = "SUM(status='blocked' AND waits='owner') blocked_owner, "
	}
	type cnt struct{ total, done, blocked, blockedOwner, prs int64 }
	counts := map[string]cnt{}
	err = c.each("SELECT goal_id, COUNT(*) total, SUM(status='done') done, SUM(status='blocked') blocked, "+blockedOwner+
		"SUM(status='in_review' AND pr_url IS NOT NULL AND pr_url<>'') prs FROM tasks GROUP BY goal_id", nil, func(r rowScanner) error {
		var gid string
		var total int64
		var done, blocked, bo, prs sql.NullInt64
		if err := r.Scan(&gid, &total, &done, &blocked, &bo, &prs); err != nil {
			return err
		}
		counts[gid] = cnt{total, done.Int64, blocked.Int64, bo.Int64, prs.Int64}
		return nil
	})
	if err != nil {
		return nil, err
	}
	teams := map[string][]string{}
	err = c.each("SELECT id, goal_id FROM teams ORDER BY id", nil, func(r rowScanner) error {
		var id, gid string
		if err := r.Scan(&id, &gid); err != nil {
			return err
		}
		teams[gid] = append(teams[gid], id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	gorgs, err := c.GoalOrgs()
	if err != nil {
		return nil, err
	}
	out := []Goal{}
	err = c.each("SELECT id,title,status,priority,project_id,created,updated FROM goals ORDER BY priority, id", nil, func(r rowScanner) error {
		var g Goal
		var pid sql.NullString
		if err := r.Scan(&g.ID, &g.Title, &g.Status, &g.Priority, &pid, &g.Created, &g.Updated); err != nil {
			return err
		}
		if pid.Valid {
			g.ProjectID = &pid.String
		}
		g.Org = OrgOfGoal(gorgs, g.ID)
		k := counts[g.ID]
		g.TasksTotal, g.TasksDone, g.TasksBlocked, g.TasksBlockedOwner, g.OpenPRs = k.total, k.done, k.blocked, k.blockedOwner, k.prs
		g.Teams = teams[g.ID]
		if g.Teams == nil {
			g.Teams = []string{}
		}
		out = append(out, g)
		return nil
	})
	return out, err
}

// Criterion is one success criterion of GoalDetail.
type Criterion struct {
	N     int64    `json:"n"`
	Text  string   `json:"text"`
	Tasks []string `json:"tasks"`
	State string   `json:"state"`
}

// GoalDetail is queries.goal_detail: the goals() row plus the truncated intent and the criteria.
type GoalDetail struct {
	Goal
	Intent   *string     `json:"intent"`
	Criteria []Criterion `json:"criteria"`
}

// GoalDetail returns nil for an unknown goal.
func (c *Conn) GoalDetail(gid string) (*GoalDetail, error) {
	all, err := c.Goals()
	if err != nil {
		return nil, err
	}
	var base *Goal
	for i := range all {
		if all[i].ID == gid {
			base = &all[i]
			break
		}
	}
	if base == nil {
		return nil, nil
	}
	var intent, crit sql.NullString
	if err := c.QueryRow("SELECT intent, success_criteria FROM goals WHERE id=?", gid).Scan(&intent, &crit); err != nil {
		return nil, err
	}
	type tk struct{ id, title, status string }
	var tasks []tk
	err = c.each("SELECT id,title,status FROM tasks WHERE goal_id=? ORDER BY id", []any{gid}, func(r rowScanner) error {
		var t tk
		if err := r.Scan(&t.id, &t.title, &t.status); err != nil {
			return err
		}
		tasks = append(tasks, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	err = c.each("SELECT DISTINCT e.entity_id FROM events e JOIN tasks t ON t.id=e.entity_id "+
		"WHERE e.entity='task' AND e.action='merged' AND t.goal_id=?", []any{gid}, func(r rowScanner) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		merged[id] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	d := &GoalDetail{Goal: *base, Criteria: []Criterion{}}
	if intent.Valid {
		d.Intent = sp(trunc(intent.String, IntentMax))
	}
	for _, ct := range ParseCriteria(crit.String) {
		var tagged, live []tk
		for _, t := range tasks {
			if tags(t.title)[ct.N] {
				tagged = append(tagged, t)
				if t.status != "cancelled" {
					live = append(live, t)
				}
			}
		}
		state := "in_progress"
		allDone, anyMerged := true, false
		for _, t := range live {
			allDone = allDone && t.status == "done"
			anyMerged = anyMerged || merged[t.id]
		}
		switch {
		case base.Status == "done":
			state = "met"
		case len(live) == 0:
			state = "untracked"
		case allDone && anyMerged:
			state = "met"
		}
		ids := []string{}
		for _, t := range tagged {
			ids = append(ids, t.id)
		}
		d.Criteria = append(d.Criteria, Criterion{ct.N, ct.Text, ids, state})
	}
	return d, nil
}

// BlockedTask is a row of InternalBlocked / owner_pending tasks_blocked.
type BlockedTask struct {
	ID      string `json:"id"`
	GoalID  string `json:"goal_id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Updated string `json:"updated"`
}

func (c *Conn) blockedTasks(waits string) ([]BlockedTask, error) {
	has, err := c.HasColumn("tasks", "waits")
	if err != nil {
		return nil, err
	}
	out := []BlockedTask{}
	if !has && waits == "owner" {
		return out, nil
	}
	q := "SELECT id,goal_id,title,status,updated FROM tasks WHERE status='blocked' ORDER BY updated DESC"
	var args []any
	if has {
		q = "SELECT id,goal_id,title,status,updated FROM tasks WHERE status='blocked' AND waits=? ORDER BY updated DESC"
		args = []any{waits}
	}
	err = c.each(q, args, func(r rowScanner) error {
		var t BlockedTask
		if err := r.Scan(&t.ID, &t.GoalID, &t.Title, &t.Status, &t.Updated); err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

// InternalBlocked is queries.internal_blocked: blocked tasks that wait on the factory itself, not on the owner.
func (c *Conn) InternalBlocked() ([]BlockedTask, error) { return c.blockedTasks("internal") }

// PendingGoal is a proposed or blocked goal in OwnerPending.
type PendingGoal struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int64  `json:"priority"`
	Updated  string `json:"updated"`
}

// AutoMergeOff is a task whose latest judge-blocked event names auto_merge=0.
type AutoMergeOff struct {
	TaskID string `json:"task_id"`
	GoalID string `json:"goal_id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	TS     string `json:"ts"`
}

// Escalation is an unread escalation/request to a boss (never the body).
type Escalation struct {
	ID        int64  `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Created   string `json:"created"`
}

// Pending is queries.owner_pending.
type Pending struct {
	Goals                 []PendingGoal  `json:"goals"`
	TasksBlocked          []BlockedTask  `json:"tasks_blocked"`
	JudgeBlockedAutoMerge []AutoMergeOff `json:"judge_blocked_auto_merge"`
	Escalations           []Escalation   `json:"escalations"`
}

// Count is the sum of the four list lengths (shell_status "needs").
func (p *Pending) Count() int {
	return len(p.Goals) + len(p.TasksBlocked) + len(p.JudgeBlockedAutoMerge) + len(p.Escalations)
}

// OwnerPending is queries.owner_pending: what waits on the owner or boss. Never returns message bodies.
func (c *Conn) OwnerPending() (*Pending, error) {
	p := &Pending{Goals: []PendingGoal{}, JudgeBlockedAutoMerge: []AutoMergeOff{}, Escalations: []Escalation{}}
	err := c.each("SELECT id,title,status,priority,updated FROM goals WHERE status IN ('proposed','blocked') ORDER BY priority, id", nil, func(r rowScanner) error {
		var g PendingGoal
		if err := r.Scan(&g.ID, &g.Title, &g.Status, &g.Priority, &g.Updated); err != nil {
			return err
		}
		p.Goals = append(p.Goals, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p.TasksBlocked, err = c.blockedTasks("owner"); err != nil {
		return nil, err
	}
	type latestEv struct{ ts, detail string }
	latest := map[string]latestEv{}
	var order []string
	err = c.each("SELECT entity_id, ts, detail FROM events WHERE entity='task' AND action='judge-blocked' ORDER BY id", nil, func(r rowScanner) error {
		var id string
		var ts string
		var d sql.NullString
		if err := r.Scan(&id, &ts, &d); err != nil {
			return err
		}
		if _, ok := latest[id]; !ok {
			order = append(order, id)
		}
		latest[id] = latestEv{ts, d.String}
		return nil
	})
	if err != nil {
		return nil, err
	}
	type tinfo struct{ goal, title, status string }
	status := map[string]tinfo{}
	err = c.each("SELECT id, goal_id, title, status FROM tasks", nil, func(r rowScanner) error {
		var id string
		var t tinfo
		if err := r.Scan(&id, &t.goal, &t.title, &t.status); err != nil {
			return err
		}
		status[id] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, tid := range order {
		t, ok := status[tid]
		if ok && strings.Contains(latest[tid].detail, "auto_merge=0") && t.status != "done" {
			p.JudgeBlockedAutoMerge = append(p.JudgeBlockedAutoMerge, AutoMergeOff{tid, t.goal, t.title, t.status, latest[tid].ts})
		}
	}
	bosses := bossNames(true)
	args := append([]any{SubjectMax}, strArgs(bosses)...)
	err = c.each("SELECT id, sender, recipient, kind, substr(subject,1,?) subject, created FROM messages "+
		"WHERE read_at IS NULL AND recipient IN ("+placeholders(len(bosses))+") AND kind IN ('escalation','request') ORDER BY id", args, func(r rowScanner) error {
		var e Escalation
		if err := r.Scan(&e.ID, &e.Sender, &e.Recipient, &e.Kind, &e.Subject, &e.Created); err != nil {
			return err
		}
		p.Escalations = append(p.Escalations, e)
		return nil
	})
	return p, err
}

// CriteriaCount is a criteria_by_goal value: [met, total].
type CriteriaCount [2]int64

// CriteriaByGoal is queries.criteria_by_goal: {goal id: [met, total]} for goals that have [SCn]-tagged tasks.
func (c *Conn) CriteriaByGoal() (*OMap[CriteriaCount], error) {
	text := map[string]string{}
	err := c.each("SELECT id, success_criteria FROM goals", nil, func(r rowScanner) error {
		var id string
		var s sql.NullString
		if err := r.Scan(&id, &s); err != nil {
			return err
		}
		text[id] = s.String
		return nil
	})
	if err != nil {
		return nil, err
	}
	tagged := NewOMap[map[int64][]string]()
	err = c.each("SELECT goal_id, title, status FROM tasks WHERE title LIKE '%[SC%'", nil, func(r rowScanner) error {
		var gid, title, st string
		if err := r.Scan(&gid, &title, &st); err != nil {
			return err
		}
		for n := range tags(title) {
			m, ok := tagged.Get(gid)
			if !ok {
				m = map[int64][]string{}
				tagged.Set(gid, m)
			}
			m[n] = append(m[n], st)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := NewOMap[CriteriaCount]()
	for _, gid := range tagged.Keys {
		byN := tagged.M[gid]
		nums := map[int64]bool{}
		for _, ct := range ParseCriteria(text[gid]) {
			nums[ct.N] = true
		}
		met := int64(0)
		ns := make([]int64, 0, len(byN))
		for n := range byN {
			nums[n] = true
			ns = append(ns, n)
		}
		sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
		for _, n := range ns {
			live, all := 0, true
			for _, s := range byN[n] {
				if s != "cancelled" {
					live++
					all = all && s == "done"
				}
			}
			if live > 0 && all {
				met++
			}
		}
		out.Set(gid, CriteriaCount{met, int64(len(nums))})
	}
	return out, nil
}
