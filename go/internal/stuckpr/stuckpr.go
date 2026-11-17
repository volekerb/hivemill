package stuckpr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// prURLRe is the only pr_url shape handed to gh: https, github.com, this repo.
var prURLRe = regexp.MustCompile(`^https://github\.com/volekerb/hivemill/pull/[0-9]+$`)

// ValidPRURL reports whether u is a hivemill pull request URL safe to pass to gh.
func ValidPRURL(u string) bool { return prURLRe.MatchString(u) }

const (
	staleSeconds    = 30 * 60
	director        = "cursor-director"
	unknownGrace    = 2 * staleSeconds // escalate with conflict "unknown" only after this age
	configWoke      = "stuckpr:woke:"
	configEscalated = "stuckpr:esc:"
)

// TaskRow is one factory task with an open PR to watch.
type TaskRow struct {
	ID, GoalID, TeamID, PRURL, Branch, Status string
	Updated                                   string
}

// PRView is the subset of gh pr view needed for classification.
type PRView struct {
	Number       int
	State        string
	HeadSHA      string
	Mergeable    string
	StatusRollup []any
	URL          string
	Conflict     string  // "yes", "no" or "unknown" (mergeable UNKNOWN/empty)
	LastCommit   float64 // unix seconds of the newest commit on the head; 0 = unknown
	CIState      string  // human summary for escalation
	StillRunning bool
	Verdict      string
	FailedChecks []string
}

// Deps are injected at the process boundary (gh, spawn, clock, messages).
type Deps struct {
	DB         *sql.DB
	Now        func() (float64, error)
	ListTasks  func(ctx context.Context, db *sql.DB) ([]TaskRow, error)
	ViewPR     func(ctx context.Context, prURL string) (*PRView, error)
	LiveCoder  func(ctx context.Context, db *sql.DB, taskID string) (bool, error)
	LiveAgent  func(ctx context.Context, db *sql.DB, taskID string) (bool, error)
	LastSignal func(ctx context.Context, db *sql.DB, task TaskRow) (float64, error)
	WakeCoder  func(ctx context.Context, db *sql.DB, task TaskRow, pr *PRView) error
	Escalate   func(ctx context.Context, db *sql.DB, task TaskRow, pr *PRView, age time.Duration) error
	ConfigGet  func(ctx context.Context, db *sql.DB, key string) (string, bool, error)
	ConfigSet  func(ctx context.Context, db *sql.DB, key, value string) error
}

// Action records what Run did to one task (tests assert on these).
type Action struct {
	TaskID string
	Kind   string // "", "wake", "escalate"
}

// Run scans open hivemill PRs once each and applies the stuck-PR rules.
func Run(ctx context.Context, d Deps) ([]Action, error) {
	if d.DB == nil {
		return nil, fmt.Errorf("stuckpr: nil DB")
	}
	if d.Now == nil {
		d.Now = func() (float64, error) { return float64(time.Now().UnixNano()) / 1e9, nil }
	}
	if d.ListTasks == nil {
		d.ListTasks = defaultListTasks
	}
	if d.LiveCoder == nil {
		d.LiveCoder = defaultLiveCoder
	}
	if d.LiveAgent == nil {
		d.LiveAgent = defaultLiveAgent
	}
	if d.LastSignal == nil {
		d.LastSignal = defaultLastSignal
	}
	if d.ConfigGet == nil {
		d.ConfigGet = defaultConfigGet
	}
	if d.ConfigSet == nil {
		d.ConfigSet = defaultConfigSet
	}
	now, err := d.Now()
	if err != nil {
		return nil, err
	}
	tasks, err := d.ListTasks(ctx, d.DB)
	if err != nil {
		return nil, err
	}
	var actions []Action
	var errs []error
	for _, t := range tasks {
		act, err := handleTask(ctx, d, t, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", t.ID, err))
		}
		if act.Kind != "" {
			actions = append(actions, act)
		}
	}
	return actions, errors.Join(errs...)
}

func handleTask(ctx context.Context, d Deps, t TaskRow, now float64) (Action, error) {
	act := Action{TaskID: t.ID}
	if d.ViewPR == nil {
		return act, fmt.Errorf("ViewPR not set")
	}
	if !ValidPRURL(t.PRURL) {
		return act, fmt.Errorf("pr_url %q is not a github.com/volekerb/hivemill pull URL; fix the task's pr", t.PRURL)
	}
	pr, err := d.ViewPR(ctx, t.PRURL)
	if err != nil {
		return act, err
	}
	if pr == nil || strings.ToUpper(pr.State) != "OPEN" {
		return act, nil
	}
	if pr.StillRunning {
		return act, nil
	}
	if d.WakeCoder != nil && pr.Verdict == "red" {
		live, err := d.LiveCoder(ctx, d.DB, t.ID)
		if err != nil {
			return act, err
		}
		wokeKey := configWoke + t.ID + ":" + pr.HeadSHA
		woke, ok, err := d.ConfigGet(ctx, d.DB, wokeKey)
		if err != nil {
			return act, fmt.Errorf("read wake marker: %w", err)
		}
		if !live && !(ok && woke != "") {
			if err := d.WakeCoder(ctx, d.DB, t, pr); err != nil {
				return act, err
			}
			if err := d.ConfigSet(ctx, d.DB, wokeKey, fmt.Sprintf("%.3f", now)); err != nil {
				return act, fmt.Errorf("record wake: %w", err)
			}
			act.Kind = "wake"
		}
	}
	liveAgent, err := d.LiveAgent(ctx, d.DB, t.ID)
	if err != nil {
		return act, err
	}
	if liveAgent {
		return act, nil
	}
	last := pr.LastCommit
	if last == 0 { // commit time unreadable: fall back to the newest task event
		last, err = d.LastSignal(ctx, d.DB, t)
		if err != nil {
			return act, err
		}
	}
	age := time.Duration((now - last) * float64(time.Second))
	if age < time.Duration(staleSeconds)*time.Second {
		return act, nil
	}
	escKey := configEscalated + t.ID + ":" + pr.HeadSHA
	v, ok, err := d.ConfigGet(ctx, d.DB, escKey)
	if err != nil {
		return act, fmt.Errorf("read escalation marker: %w", err)
	}
	if ok && v != "" {
		return act, nil
	}
	if pr.Conflict == "unknown" && age < time.Duration(unknownGrace)*time.Second {
		return act, nil // GitHub has not computed mergeability yet; ask again next tick
	}
	if d.Escalate == nil {
		return act, fmt.Errorf("Escalate not set")
	}
	if err := d.Escalate(ctx, d.DB, t, pr, age); err != nil {
		return act, err
	}
	if err := d.ConfigSet(ctx, d.DB, escKey, fmt.Sprintf("%.3f", now)); err != nil {
		return act, err
	}
	act.Kind = "escalate"
	return act, nil
}

// ParsePRView builds PRView from gh pr view JSON fields.
func ParsePRView(m map[string]any) (*PRView, error) {
	num, _ := m["number"].(float64)
	state, _ := m["state"].(string)
	head, _ := m["headRefOid"].(string)
	mergeable, _ := m["mergeable"].(string)
	url, _ := m["url"].(string)
	rollup, _ := m["statusCheckRollup"].([]any)
	verdict, failed, running, ok := ciVerdict(rollup)
	if !ok {
		return nil, fmt.Errorf("bad statusCheckRollup")
	}
	conflict := "unknown"
	switch strings.ToUpper(mergeable) {
	case "CONFLICTING", "DIRTY":
		conflict = "yes"
	case "MERGEABLE":
		conflict = "no"
	}
	var lastCommit float64
	if cs, ok := m["commits"].([]any); ok && len(cs) > 0 {
		if c, ok := cs[len(cs)-1].(map[string]any); ok {
			if ts, _ := c["committedDate"].(string); ts != "" {
				if tt, ok := parseTime(ts); ok {
					lastCommit = float64(tt.UnixNano()) / 1e9
				}
			}
		}
	}
	ciState := verdict
	if len(failed) > 0 {
		ciState = verdict + " (" + strings.Join(failed, ", ") + ")"
	} else if running {
		ciState = "pending"
	}
	return &PRView{
		Number:       int(num),
		State:        state,
		HeadSHA:      head,
		Mergeable:    mergeable,
		StatusRollup: rollup,
		URL:          url,
		Conflict:     conflict,
		LastCommit:   lastCommit,
		CIState:      ciState,
		StillRunning: running,
		Verdict:      verdict,
		FailedChecks: failed,
	}, nil
}

func defaultListTasks(ctx context.Context, db *sql.DB) ([]TaskRow, error) {
	const q = `SELECT t.id, t.goal_id, coalesce(t.team_id,''), t.pr_url, coalesce(t.branch,''), t.status, t.updated
FROM tasks t WHERE t.pr_url IS NOT NULL AND t.pr_url != ''
AND t.status IN ('in_progress','in_review','blocked')
AND t.pr_url LIKE 'https://github.com/volekerb/hivemill/pull/%' ORDER BY t.id`
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []TaskRow
	for rs.Next() {
		var r TaskRow
		if err := rs.Scan(&r.ID, &r.GoalID, &r.TeamID, &r.PRURL, &r.Branch, &r.Status, &r.Updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func defaultLiveCoder(ctx context.Context, db *sql.DB, taskID string) (bool, error) {
	return agentLive(ctx, db, taskID, "coder")
}

func defaultLiveAgent(ctx context.Context, db *sql.DB, taskID string) (bool, error) {
	return agentLive(ctx, db, taskID, "")
}

func agentLive(ctx context.Context, db *sql.DB, taskID, role string) (bool, error) {
	q := `SELECT 1 FROM agents WHERE task_id=? AND status IN ('live','starting','queued')`
	args := []any{taskID}
	if role != "" {
		q += ` AND role=?`
		args = append(args, role)
	}
	q += ` LIMIT 1`
	var one int
	err := db.QueryRowContext(ctx, q, args...).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func defaultLastSignal(ctx context.Context, db *sql.DB, t TaskRow) (float64, error) {
	var ts string
	err := db.QueryRowContext(ctx,
		`SELECT ts FROM events WHERE entity='task' AND entity_id=? ORDER BY id DESC LIMIT 1`, t.ID).Scan(&ts)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if ts != "" {
		if tt, ok := parseTime(ts); ok {
			return float64(tt.UnixNano()) / 1e9, nil
		}
	}
	if t.Updated != "" {
		if tt, ok := parseTime(t.Updated); ok {
			return float64(tt.UnixNano()) / 1e9, nil
		}
	}
	return 0, fmt.Errorf("no activity time for %s", t.ID)
}

func parseTime(s string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05+00:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func defaultConfigGet(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM config WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

func defaultConfigSet(ctx context.Context, db *sql.DB, key, value string) error {
	if value == "" {
		_, err := db.ExecContext(ctx, `DELETE FROM config WHERE key=?`, key)
		return err
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// DefaultEscalate sends one report to the cursor director naming the PR, age, CI state, and conflict.
func DefaultEscalate(ctx context.Context, db *sql.DB, task TaskRow, pr *PRView, age time.Duration) error {
	conf := pr.Conflict
	if conf == "" {
		conf = "unknown"
	}
	subject := fmt.Sprintf("stuck PR: %s #%d", task.ID, pr.Number)
	body := fmt.Sprintf("PR %s\nage %s\nCI %s\nconflict %s",
		pr.URL, age.Truncate(time.Second), pr.CIState, conf)
	_, err := db.ExecContext(ctx,
		`INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)`,
		"tick", director, "report", subject, body, task.GoalID, task.ID, time.Now().UTC().Format(time.RFC3339))
	return err
}
