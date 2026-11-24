// Package hm is the Option B core: one Claude Code session per task, a Stop-hook gate, a fresh judge and a
// mechanical merge (docs/option-b-runner.md). Every SQL statement lives in this file, behind Store.
package hm

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // CGO-free driver, the same one the rest of go/ uses
)

// Task is the part of a factory task row the runner reads.
type Task struct {
	ID, Goal, Team, Title, Spec, Acceptance, Role, Status, DependsOn, Branch, Worktree, PRURL, Tier string
	Project, RepoPath, GHRepo, Base                                                                 string
	Priority                                                                                        sql.NullInt64
}

// Run is the runner's record of one task. It is stored as JSON in hm_runs so new fields need no migration.
type Run struct {
	Task, Goal, Project, Repo, Base, Worktree, Branch, Agent  string
	Judges                                                    []string
	State                                                     string // working | gated | judging | judged | merging | merged | failed | reverted | cancelled
	Round, GateBlocks, GateBlocksTotal, JudgeRounds, CIRounds int
	JudgeBlocks, Refreshes, RerunFailures                     int
	Risky                                                     bool
	RiskWhy, Model, Effort, Tier                              string
	Head, JudgeHead, PRURL, MergeSHA, MergeHold               string
	Dispatched, Gated, Merged, Ended                          string
	Outcome, FailReason, Kind, RevertedBy, GoalCheck          string
	Criteria, Proven, Manual, FindingsTotal, FindingsFixed    int
	IdleSince                                                 string
	IdleNudges                                                int
	Verdicts                                                  []Verdict
	History                                                   []string
	LastGate                                                  *GateRecord
}

// Session is one Claude session of a run (task, judge).
type Session struct {
	SID, Task, Role, Agent, Model, Effort, Started string
}

// Store is everything the runner keeps or reads. The SQLite implementation shares the factory DB; a Postgres one
// would implement the same methods (every timestamp is ISO-8601 with an offset, every id is a string).
type Store interface {
	Task(id string) (*Task, error)
	ReadyTasks(projects []string) ([]Task, error)
	TaskStatus(id, status, waits string) error
	TaskPR(id, prURL, tier string) error
	TaskClone(id, worktree, branch string) error
	NewTask(goal, title, spec, acceptance string) (string, error)
	GoalOpenTasks(goal string) (int, string, error)
	DoneTasks() (map[string]bool, error)
	FactoryCfg(key string) string
	Cfg(key string) string
	SetCfg(key, value string) error
	AgentLive(name, role, task, goal, team, model, sid, cwd, args string) error
	AgentEnded(name string) error
	AgentSession(name, sid string) error
	AgentRole(name string) string
	OldPipelineLive(task string) ([]string, error)
	Verdict(task, prURL, head, verdict, summary, author string) error
	LaneVerdict(task, head string) (string, bool, error)
	Event(actor, entity, entityID, action string, detail any) error
	LastEvent(task, action string) string
	Run(task string) (*Run, error)
	PutRun(r *Run) error
	Runs() ([]Run, error)
	AddSession(s Session) error
	Sessions(task string) ([]Session, error)
	RedMain(slug string) (map[string]any, bool)
	SetRedMainFix(slug, task string) error
	Close() error
}

// SQLite is the Store on the factory DB (WAL), plus the hm_* tables this package owns.
type SQLite struct {
	db  *sql.DB
	Now func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS hm_config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS hm_runs (task TEXT PRIMARY KEY, state TEXT NOT NULL, data TEXT NOT NULL, updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS hm_sessions (sid TEXT PRIMARY KEY, task TEXT NOT NULL, role TEXT NOT NULL, agent TEXT, model TEXT,
  effort TEXT, started TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS hm_sessions_task ON hm_sessions(task);
`

// OpenSQLite opens an existing factory DB read-write (never creates one) and makes sure the hm_* tables exist.
func OpenSQLite(path string) (*SQLite, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("cannot open database %s: %w", path, err)
	}
	q := url.Values{}
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(WAL)")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot create the hm tables in %s: %w", path, err)
	}
	return &SQLite{db: db, Now: time.Now}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) now() string { return s.Now().Format("2006-01-02T15:04:05-07:00") }

const taskCols = `t.id, t.goal_id, COALESCE(t.team_id,''), t.title, COALESCE(t.spec,''), COALESCE(t.acceptance,''), t.role, t.status,
 t.depends_on, COALESCE(t.branch,''), COALESCE(t.worktree,''), COALESCE(t.pr_url,''), COALESCE(t.tier,''), COALESCE(g.project_id,''),
 COALESCE(p.repo_path,''), COALESCE(p.gh_repo,''), COALESCE(p.base_branch,'main'), t.priority`

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := sc.Scan(&t.ID, &t.Goal, &t.Team, &t.Title, &t.Spec, &t.Acceptance, &t.Role, &t.Status, &t.DependsOn, &t.Branch,
		&t.Worktree, &t.PRURL, &t.Tier, &t.Project, &t.RepoPath, &t.GHRepo, &t.Base, &t.Priority)
	return &t, err
}

func (s *SQLite) Task(id string) (*Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks t JOIN goals g ON g.id=t.goal_id
		LEFT JOIN projects p ON p.id=g.project_id WHERE t.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no task %s", id)
	}
	return t, err
}

// ReadyTasks lists todo coder tasks of active goals in the given projects, in lead-less dispatch order.
func (s *SQLite) ReadyTasks(projects []string) ([]Task, error) {
	if len(projects) == 0 {
		return nil, nil
	}
	args := make([]any, len(projects))
	for i, p := range projects {
		args[i] = p
	}
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks t JOIN goals g ON g.id=t.goal_id LEFT JOIN projects p ON p.id=g.project_id
		WHERE t.status='todo' AND t.role='coder' AND g.status='active' AND g.project_id IN (?`+strings.Repeat(",?", len(projects)-1)+`)
		ORDER BY COALESCE(g.priority, 2), t.priority IS NULL, t.priority, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *SQLite) DoneTasks() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM tasks WHERE status='done'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *SQLite) TaskStatus(id, status, waits string) error {
	if waits != "" {
		_, err := s.db.Exec(`UPDATE tasks SET status=?, waits=?, updated=? WHERE id=?`, status, waits, s.now(), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE tasks SET status=?, updated=? WHERE id=?`, status, s.now(), id)
	return err
}

func (s *SQLite) TaskPR(id, prURL, tier string) error {
	_, err := s.db.Exec(`UPDATE tasks SET pr_url=?, tier=CASE WHEN ?='' THEN tier ELSE ? END, updated=? WHERE id=?`,
		prURL, tier, tier, s.now(), id)
	return err
}

func (s *SQLite) TaskClone(id, worktree, branch string) error {
	_, err := s.db.Exec(`UPDATE tasks SET worktree=?, branch=?, updated=? WHERE id=?`, worktree, branch, s.now(), id)
	return err
}

// NewTask files a coder task in `goal` with the next free T-NNNN id.
func (s *SQLite) NewTask(goal, title, spec, acceptance string) (string, error) {
	var max sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(CAST(substr(id, 3) AS INTEGER)) FROM tasks WHERE id LIKE 'T-%'`).Scan(&max); err != nil {
		return "", err
	}
	id := fmt.Sprintf("T-%04d", max.Int64+1)
	_, err := s.db.Exec(`INSERT INTO tasks(id,goal_id,title,spec,acceptance,role,depends_on,created,updated,priority)
		VALUES(?,?,?,?,?,'coder','[]',?,?,1)`, id, goal, title, spec, acceptance, s.now(), s.now())
	return id, err
}

func (s *SQLite) GoalOpenTasks(goal string) (int, string, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM tasks WHERE goal_id=? AND status NOT IN ('done','cancelled')`, goal).Scan(&n); err != nil {
		return 0, "", err
	}
	var crit sql.NullString
	_ = s.db.QueryRow(`SELECT success_criteria FROM goals WHERE id=?`, goal).Scan(&crit)
	return n, crit.String, nil
}

// FactoryCfg reads a key of the factory's own config table ("" when unset).
func (s *SQLite) FactoryCfg(key string) string {
	var v string
	_ = s.db.QueryRow(`SELECT value FROM config WHERE key=?`, key).Scan(&v)
	return v
}

// Cfg reads an hm_config key, falling back to Defaults.
func (s *SQLite) Cfg(key string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM hm_config WHERE key=?`, key).Scan(&v); err == nil {
		return v
	}
	return Defaults[key]
}

func (s *SQLite) SetCfg(key, value string) error {
	if _, ok := Defaults[key]; !ok {
		return fmt.Errorf("unknown key %s; known: %s", key, strings.Join(DefaultKeys(), ", "))
	}
	_, err := s.db.Exec(`INSERT INTO hm_config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// AgentLive writes the agents row of a runner session (role task or task-judge) with its session id.
func (s *SQLite) AgentLive(name, role, task, goal, team, model, sid, cwd, args string) error {
	_, err := s.db.Exec(`INSERT INTO agents(name,role,goal_id,team_id,task_id,status,pane_id,model,session_id,cwd,spawn_args,started,org)
		VALUES(?,?,?,NULLIF(?,''),?,'live',?,?,?,?,?,?,'claude') ON CONFLICT(name) DO UPDATE SET role=excluded.role, status='live',
		pane_id=excluded.pane_id, model=excluded.model, session_id=excluded.session_id, cwd=excluded.cwd, spawn_args=excluded.spawn_args,
		started=excluded.started, ended=NULL, summary=NULL`, name, role, goal, team, task, name, model, sid, cwd, args, s.now())
	return err
}

func (s *SQLite) AgentEnded(name string) error {
	_, err := s.db.Exec(`UPDATE agents SET status='exited', ended=? WHERE name=? AND status='live'`, s.now(), name)
	return err
}

func (s *SQLite) AgentSession(name, sid string) error {
	_, err := s.db.Exec(`UPDATE agents SET session_id=? WHERE name=?`, sid, name)
	return err
}

func (s *SQLite) AgentRole(name string) string {
	var r string
	_ = s.db.QueryRow(`SELECT role FROM agents WHERE name=?`, name).Scan(&r)
	return r
}

// OldPipelineLive names live agents of the old pipeline on a task (the runner refuses to start next to them).
func (s *SQLite) OldPipelineLive(task string) ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM agents WHERE task_id=? AND status IN ('live','starting','queued')
		AND role NOT IN ('task','task-judge')`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Verdict records a task-judge verdict ("pass" = MERGE, "fail" = REJECT) in the factory's verdicts table.
func (s *SQLite) Verdict(task, prURL, head, verdict, summary, author string) error {
	_, err := s.db.Exec(`INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)`,
		task, prURL, head, "task-judge", verdict, summary, author, s.now())
	return err
}

// LaneVerdict is the newest task-judge verdict on this exact head.
func (s *SQLite) LaneVerdict(task, head string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT verdict FROM verdicts WHERE task_id=? AND role='task-judge' AND head_sha=? ORDER BY id DESC LIMIT 1`,
		task, head).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *SQLite) Event(actor, entity, entityID, action string, detail any) error {
	var d any
	switch x := detail.(type) {
	case nil:
	case string:
		d = x
	default:
		b, _ := json.Marshal(x)
		d = string(b)
	}
	_, err := s.db.Exec(`INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)`, s.now(), actor, entity, entityID, action, d)
	return err
}

func (s *SQLite) LastEvent(task, action string) string {
	var d sql.NullString
	_ = s.db.QueryRow(`SELECT detail FROM events WHERE entity='task' AND entity_id=? AND action=? ORDER BY id DESC LIMIT 1`, task, action).Scan(&d)
	return d.String
}

func (s *SQLite) Run(task string) (*Run, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM hm_runs WHERE task=?`, task).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Run
	return &r, json.Unmarshal([]byte(data), &r)
}

func (s *SQLite) PutRun(r *Run) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO hm_runs(task,state,data,updated) VALUES(?,?,?,?) ON CONFLICT(task) DO UPDATE SET state=excluded.state,
		data=excluded.data, updated=excluded.updated`, r.Task, r.State, string(b), s.now())
	return err
}

func (s *SQLite) Runs() ([]Run, error) {
	rows, err := s.db.Query(`SELECT data FROM hm_runs ORDER BY task`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r Run
		if json.Unmarshal([]byte(data), &r) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func (s *SQLite) AddSession(x Session) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO hm_sessions(sid,task,role,agent,model,effort,started) VALUES(?,?,?,?,?,?,?)`,
		x.SID, x.Task, x.Role, x.Agent, x.Model, x.Effort, x.Started)
	return err
}

func (s *SQLite) Sessions(task string) ([]Session, error) {
	rows, err := s.db.Query(`SELECT sid,task,role,COALESCE(agent,''),COALESCE(model,''),COALESCE(effort,''),started FROM hm_sessions
		WHERE task=? ORDER BY started`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.SID, &x.Task, &x.Role, &x.Agent, &x.Model, &x.Effort, &x.Started); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RedMain is the red-main record the factory's monitor keeps for a repo (config red_main:<slug>), if any.
func (s *SQLite) RedMain(slug string) (map[string]any, bool) {
	v := s.FactoryCfg("red_main:" + slug)
	if v == "" {
		return nil, false
	}
	m := map[string]any{}
	if json.Unmarshal([]byte(v), &m) != nil {
		return map[string]any{"run": "?"}, true // unreadable keeps the freeze (fail closed)
	}
	return m, true
}

// SetRedMainFix names the task that may merge while the repo's main is red (what `factory redmain fix` does).
func (s *SQLite) SetRedMainFix(slug, task string) error {
	m, ok := s.RedMain(slug)
	if !ok {
		return fmt.Errorf("%s is not red", slug)
	}
	m["task"] = task
	b, _ := json.Marshal(m)
	_, err := s.db.Exec(`UPDATE config SET value=? WHERE key=?`, string(b), "red_main:"+slug)
	return err
}
