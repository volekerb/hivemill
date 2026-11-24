package obs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Via says how a session was tied to its agent or task (web/tokens.py attribute, plus the kickoff name).
const (
	ViaSession      = "session"      // agents.session_id
	ViaEvent        = "event"        // a tick session-change event named the agent
	ViaName         = "name"         // the transcript's agent-name line, within the agent's run
	ViaKickoff      = "kickoff"      // the agent name in the kickoff message, within the agent's run
	ViaWorktree     = "worktree"     // the factory worktree in the project dir or cwd (goal and task only)
	ViaUnattributed = "unattributed" // none of the above
)

type agentRow struct {
	name, role, goal, task, session string
	start, end                      time.Time
	hasEnd                          bool
}

type sessInfo struct {
	session, parent, role, kickoffAgent, agentName, wtGoal, wtTask string
	first, last                                                    int64
}

type attribution struct {
	agent, role, task, goal, project, pr, via string
}

// attribute rebuilds the sessions table: every session in the store, joined to its agent, task, goal, project and PR
// from factory.db (read-only). A missing factory.db leaves every session to its kickoff role and worktree.
func attribute(db *sql.DB, factoryDB string, now time.Time) (int, error) {
	sess, err := loadSessions(db)
	if err != nil {
		return 0, err
	}
	fx := &factoryIndex{taskGoal: map[string]string{}, taskPR: map[string]string{}, goalProject: map[string]string{},
		bySession: map[string]*agentRow{}, byName: map[string][]*agentRow{}, events: map[string]string{}}
	if fdb, err := openRO(factoryDB, "no factory DB: sessions keep their kickoff role only"); err == nil {
		err = fx.load(fdb)
		fdb.Close()
		if err != nil {
			return 0, err
		}
	}
	out := map[string]attribution{}
	for id, s := range sess {
		if s.parent == "" {
			out[id] = fx.resolve(s, now)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("obs: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a committed tx makes this a no-op
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return 0, fmt.Errorf("obs: clear sessions: %w", err)
	}
	ins, err := tx.Prepare(`INSERT INTO sessions(session, parent, role, agent, task, goal, project, pr, via, first_ms,
		last_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, fmt.Errorf("obs: prepare: %w", err)
	}
	defer ins.Close()
	for id, s := range sess {
		a := out[id]
		if s.parent != "" {
			// A subagent belongs to its parent session (hm-spend: it inherits the parent's role).
			p, ok := out[s.parent]
			if !ok {
				p = attribution{via: ViaUnattributed}
			}
			a = p
			a.role = ""
			if ps, ok := sess[s.parent]; ok {
				a.role = ps.role
			}
			if a.role == "" {
				a.role = p.role
			}
		} else if s.role != "" {
			a.role = s.role
		}
		if a.role == "" {
			a.role = "other"
		}
		if _, err := ins.Exec(id, nullable(s.parent), a.role, a.agent, a.task, a.goal, a.project, a.pr, a.via,
			s.first, s.last); err != nil {
			return 0, fmt.Errorf("obs: write session: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("obs: commit sessions: %w", err)
	}
	return len(sess), nil
}

func loadSessions(db *sql.DB) (map[string]*sessInfo, error) {
	sess := map[string]*sessInfo{}
	rows, err := db.Query(`SELECT session, COALESCE(parent,''), role, kickoff_agent, agent_name, wt_goal, wt_task
		FROM files ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("obs: read files: %w", err)
	}
	for rows.Next() {
		var r sessInfo
		if err := rows.Scan(&r.session, &r.parent, &r.role, &r.kickoffAgent, &r.agentName, &r.wtGoal, &r.wtTask); err != nil {
			rows.Close()
			return nil, fmt.Errorf("obs: read files: %w", err)
		}
		s := sess[r.session]
		if s == nil {
			s = &sessInfo{session: r.session}
			sess[r.session] = s
		}
		fill(&s.parent, r.parent)
		fill(&s.role, r.role)
		fill(&s.kickoffAgent, r.kickoffAgent)
		fill(&s.agentName, r.agentName)
		fill(&s.wtGoal, r.wtGoal)
		fill(&s.wtTask, r.wtTask)
	}
	rows.Close()
	rows, err = db.Query(`SELECT session, MIN(ts_ms), MAX(ts_ms) FROM requests GROUP BY session`)
	if err != nil {
		return nil, fmt.Errorf("obs: read request times: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var lo, hi int64
		if err := rows.Scan(&id, &lo, &hi); err != nil {
			return nil, fmt.Errorf("obs: read request times: %w", err)
		}
		s := sess[id]
		if s == nil {
			s = &sessInfo{session: id}
			sess[id] = s
		}
		s.first, s.last = lo, hi
	}
	return sess, rows.Err()
}

func fill(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

type factoryIndex struct {
	taskGoal, taskPR, goalProject map[string]string
	bySession                     map[string]*agentRow
	byName                        map[string][]*agentRow
	events                        map[string]string // session id -> agent name
}

func (fx *factoryIndex) load(db *sql.DB) error {
	rows, err := db.Query(`SELECT name, role, COALESCE(goal_id,''), COALESCE(task_id,''), COALESCE(session_id,''),
		COALESCE(started,''), COALESCE(ended,'') FROM agents ORDER BY started`)
	if err != nil {
		return fmt.Errorf("obs: read agents from factory.db: %w", err)
	}
	for rows.Next() {
		a := &agentRow{}
		var started, ended string
		if err := rows.Scan(&a.name, &a.role, &a.goal, &a.task, &a.session, &started, &ended); err != nil {
			rows.Close()
			return fmt.Errorf("obs: read agents: %w", err)
		}
		a.start, _ = parseTime(started)
		a.end, a.hasEnd = parseTime(ended)
		if a.session != "" {
			fx.bySession[a.session] = a
		}
		fx.byName[a.name] = append(fx.byName[a.name], a)
	}
	rows.Close()
	if err := fx.loadEvents(db); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT id, goal_id, COALESCE(pr_url,'') FROM tasks`)
	if err != nil {
		return fmt.Errorf("obs: read tasks from factory.db: %w", err)
	}
	for rows.Next() {
		var id, goal, pr string
		if err := rows.Scan(&id, &goal, &pr); err != nil {
			rows.Close()
			return fmt.Errorf("obs: read tasks: %w", err)
		}
		fx.taskGoal[id], fx.taskPR[id] = goal, pr
	}
	rows.Close()
	rows, err = db.Query(`SELECT id, COALESCE(project_id,'') FROM goals`)
	if err != nil {
		return fmt.Errorf("obs: read goals from factory.db: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			return fmt.Errorf("obs: read goals: %w", err)
		}
		fx.goalProject[id] = p
	}
	return rows.Err()
}

// loadEvents maps session ids to agent names from the tick's session-change events (detail JSON stored / herdr).
func (fx *factoryIndex) loadEvents(db *sql.DB) error {
	rows, err := db.Query(`SELECT COALESCE(entity_id,''), COALESCE(detail,'') FROM events WHERE entity='agent' AND
		(detail LIKE '%"stored"%' OR detail LIKE '%"herdr"%') ORDER BY id`)
	if err != nil {
		return fmt.Errorf("obs: read events from factory.db: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, detail string
		if err := rows.Scan(&name, &detail); err != nil {
			return fmt.Errorf("obs: read events: %w", err)
		}
		var d map[string]any
		if json.Unmarshal([]byte(detail), &d) != nil {
			continue
		}
		for _, k := range []string{"stored", "herdr"} {
			if s, ok := d[k].(string); ok && s != "" {
				fx.events[s] = name
			}
		}
	}
	return rows.Err()
}

func (fx *factoryIndex) resolve(s *sessInfo, now time.Time) attribution {
	var agent *agentRow
	via := ViaSession
	agent = fx.bySession[s.session]
	if agent == nil {
		if rows := fx.byName[fx.events[s.session]]; len(rows) > 0 {
			agent, via = rows[len(rows)-1], ViaEvent
		}
	}
	first := time.UnixMilli(s.first)
	for _, c := range []struct{ name, via string }{{s.agentName, ViaName}, {s.kickoffAgent, ViaKickoff}} {
		if agent != nil || c.name == "" || s.first == 0 {
			continue
		}
		for _, a := range fx.byName[c.name] {
			end := now
			if a.hasEnd {
				end = a.end
			}
			// A session starts a few seconds before the agent row is stamped started: allow 5 minutes of slack.
			if !a.start.IsZero() && !first.Before(a.start.Add(-5*time.Minute)) && !first.After(end) {
				agent, via = a, c.via
				break
			}
		}
	}
	out := attribution{via: ViaUnattributed}
	switch {
	case agent != nil:
		out = attribution{agent: agent.name, role: agent.role, task: agent.task, goal: agent.goal, via: via}
	case s.wtTask != "":
		out = attribution{task: s.wtTask, goal: s.wtGoal, via: ViaWorktree}
	}
	if out.task != "" {
		if out.goal == "" {
			out.goal = fx.taskGoal[out.task]
		}
		out.pr = fx.taskPR[out.task]
	}
	if out.goal != "" {
		out.project = fx.goalProject[out.goal]
	}
	return out
}

// parseTime reads the timestamps factory.db holds (Python isoformat, with or without fraction or zone).
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
